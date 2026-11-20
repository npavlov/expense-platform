-- Privileges of the runtime role. Migrations run as expenses_migrator, which
-- owns the schema; the service connects as expenses_app, which can only read
-- and write the rows it needs. Roles are created by deploy/k8s/base/db/bootstrap.sql.

-- +goose Up
GRANT USAGE ON SCHEMA expenses TO expenses_app;

-- Only the decision columns can change after submission: the amount and
-- description a reviewer approves cannot be edited afterwards, even by the app.
GRANT SELECT, INSERT ON expenses.claims TO expenses_app;
GRANT UPDATE (status, decided_by, decided_at) ON expenses.claims TO expenses_app;

GRANT SELECT, INSERT ON expenses.idempotency_keys TO expenses_app;

-- The worker updates delivery state but cannot rewrite an event payload.
GRANT SELECT, INSERT ON expenses.outbox TO expenses_app;
GRANT UPDATE (attempts, next_attempt_at, locked_until, last_error, delivered_at, parked_at)
    ON expenses.outbox TO expenses_app;

-- Readiness checks read the migration version.
GRANT SELECT ON expenses.goose_db_version TO expenses_app;

-- +goose Down
REVOKE ALL ON expenses.goose_db_version FROM expenses_app;
REVOKE ALL ON expenses.outbox FROM expenses_app;
REVOKE ALL ON expenses.idempotency_keys FROM expenses_app;
REVOKE ALL ON expenses.claims FROM expenses_app;
REVOKE USAGE ON SCHEMA expenses FROM expenses_app;
