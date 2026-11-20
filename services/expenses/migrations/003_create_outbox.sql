-- Transactional outbox: events written in the same transaction as the state
-- change they describe, delivered to other services by a background worker.

-- +goose Up
CREATE TABLE expenses.outbox (
    event_id        uuid        PRIMARY KEY,
    event_type      text        NOT NULL,
    aggregate_id    uuid        NOT NULL REFERENCES expenses.claims (id),
    payload         jsonb       NOT NULL,
    -- Request ID of the API call that produced the event, sent along so one
    -- decision can be followed through the logs of both services.
    request_id      text,
    created_at      timestamptz NOT NULL DEFAULT now(),

    -- Delivery state, owned by the worker.
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    locked_until    timestamptz,
    last_error      text,
    delivered_at    timestamptz,
    -- Set when delivery failed permanently or ran out of attempts.
    -- An operator inspects and retries parked events via the admin endpoint.
    parked_at       timestamptz,

    -- A claim is decided once, so it has at most one decision event.
    CONSTRAINT outbox_one_event_per_claim UNIQUE (aggregate_id, event_type),
    CONSTRAINT outbox_event_type_known    CHECK (event_type IN ('ExpenseDecided')),
    CONSTRAINT outbox_last_error_length   CHECK (char_length(last_error) <= 500)
);

-- Work queue for the worker.
CREATE INDEX outbox_due_idx ON expenses.outbox (next_attempt_at)
    WHERE delivered_at IS NULL AND parked_at IS NULL;
-- Backlog metrics: count and age of undelivered events.
CREATE INDEX outbox_undelivered_idx ON expenses.outbox (created_at)
    WHERE delivered_at IS NULL;

-- +goose Down
DROP TABLE expenses.outbox;
