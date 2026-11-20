-- Decisions on claims and idempotent claim submission.

-- +goose Up
ALTER TABLE expenses.claims
    ADD COLUMN decided_by text,
    ADD COLUMN decided_at timestamptz,
    -- Input bounds mirror the API validation, so a bug in the service cannot
    -- store a value the API contract does not allow.
    ADD CONSTRAINT claims_amount_max          CHECK (amount_cents <= 10000000),
    ADD CONSTRAINT claims_description_length  CHECK (char_length(description) <= 500),
    ADD CONSTRAINT claims_employee_id_length  CHECK (char_length(employee_id) <= 128),
    -- A claim is pending exactly when nobody has decided it.
    ADD CONSTRAINT claims_decision_consistent CHECK ((status = 'pending') = (decided_by IS NULL)),
    ADD CONSTRAINT claims_decided_at_present  CHECK ((decided_by IS NULL) = (decided_at IS NULL)),
    -- Four-eyes rule, enforced by the database as a last line of defence.
    ADD CONSTRAINT claims_no_self_decision    CHECK (decided_by IS NULL OR decided_by <> employee_id);

-- "My claims", newest first.
CREATE INDEX claims_employee_created_idx ON expenses.claims (employee_id, created_at DESC, id);
-- Review queue.
CREATE INDEX claims_pending_idx ON expenses.claims (created_at, id) WHERE status = 'pending';

-- One row per (employee, Idempotency-Key). The key is scoped to the employee
-- taken from the verified token, so two employees can use the same key.
-- request_hash is SHA-256 of the normalized request; a retry with the same key
-- but a different body is rejected instead of silently returning the old claim.
CREATE TABLE expenses.idempotency_keys (
    employee_id  text        NOT NULL,
    key          text        NOT NULL,
    request_hash bytea       NOT NULL,
    claim_id     uuid        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (employee_id, key),
    -- Deferred so the key can be inserted first: a concurrent duplicate then
    -- waits on the primary key instead of creating a second claim.
    CONSTRAINT idempotency_keys_claim_fk FOREIGN KEY (claim_id)
        REFERENCES expenses.claims (id) DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT idempotency_keys_key_format CHECK (key ~ '^[A-Za-z0-9._:-]{8,128}$'),
    CONSTRAINT idempotency_keys_hash_size  CHECK (octet_length(request_hash) = 32)
);

-- +goose Down
DROP TABLE expenses.idempotency_keys;
DROP INDEX expenses.claims_pending_idx;
DROP INDEX expenses.claims_employee_created_idx;
ALTER TABLE expenses.claims
    DROP CONSTRAINT claims_no_self_decision,
    DROP CONSTRAINT claims_decided_at_present,
    DROP CONSTRAINT claims_decision_consistent,
    DROP CONSTRAINT claims_employee_id_length,
    DROP CONSTRAINT claims_description_length,
    DROP CONSTRAINT claims_amount_max,
    DROP COLUMN decided_at,
    DROP COLUMN decided_by;
