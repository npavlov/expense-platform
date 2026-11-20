-- Decision history: one row per ExpenseDecided event, append-only.

-- +goose Up
CREATE SCHEMA IF NOT EXISTS audit;

CREATE TABLE audit.decisions (
    -- Stable event ID from the producer. The primary key makes redelivery
    -- harmless: a second copy of the same event cannot create a second row.
    event_id       uuid        PRIMARY KEY,
    schema_version integer     NOT NULL,
    claim_id       uuid        NOT NULL,
    employee_id    text        NOT NULL,
    amount_cents   bigint      NOT NULL,
    currency       text        NOT NULL,
    decision       text        NOT NULL,
    decided_by     text        NOT NULL,
    decided_at     timestamptz NOT NULL,
    occurred_at    timestamptz NOT NULL,
    correlation_id text,
    -- SHA-256 of the event content; a redelivery with the same event ID but
    -- different content is a producer bug and is rejected, not ignored.
    content_hash   bytea       NOT NULL,
    received_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT decisions_decision_valid   CHECK (decision IN ('approved', 'rejected')),
    CONSTRAINT decisions_amount_positive  CHECK (amount_cents > 0),
    CONSTRAINT decisions_currency_format  CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT decisions_no_self_decision CHECK (decided_by <> employee_id),
    CONSTRAINT decisions_hash_size        CHECK (octet_length(content_hash) = 32)
);

CREATE INDEX decisions_claim_idx ON audit.decisions (claim_id, decided_at);

-- Defence in depth for immutability: the runtime role has no UPDATE/DELETE
-- privilege, and these triggers also stop the table owner.
-- +goose StatementBegin
CREATE FUNCTION audit.reject_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit.decisions is append-only' USING ERRCODE = 'insufficient_privilege';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER decisions_no_update BEFORE UPDATE OR DELETE ON audit.decisions
    FOR EACH ROW EXECUTE FUNCTION audit.reject_change();
CREATE TRIGGER decisions_no_truncate BEFORE TRUNCATE ON audit.decisions
    FOR EACH STATEMENT EXECUTE FUNCTION audit.reject_change();

-- +goose Down
DROP TABLE audit.decisions;
DROP FUNCTION audit.reject_change();
