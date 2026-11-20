-- Privileges of the runtime role audit_app: append and read, nothing else.

-- +goose Up
GRANT USAGE ON SCHEMA audit TO audit_app;
GRANT SELECT, INSERT ON audit.decisions TO audit_app;
GRANT SELECT ON audit.goose_db_version TO audit_app;

-- +goose Down
REVOKE ALL ON audit.goose_db_version FROM audit_app;
REVOKE ALL ON audit.decisions FROM audit_app;
REVOKE USAGE ON SCHEMA audit FROM audit_app;
