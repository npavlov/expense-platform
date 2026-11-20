-- Creates the database roles and schemas. Runs as the PostgreSQL superuser,
-- before any service migration, and is safe to run again: existing roles keep
-- their privileges and get their password reset to the value passed in.
--
-- psql -v ON_ERROR_STOP=1 \
--   -v db=expense_platform \
--   -v expenses_migrator_password=... -v expenses_app_password=... \
--   -v audit_migrator_password=...    -v audit_app_password=... \
--   -f bootstrap.sql
--
-- Role layout (one database, one schema per service):
--   <service>_migrator  owns the schema, runs DDL; used only by migration jobs
--   <service>_app       DML on its own tables only; used by the running service
-- No role except the superuser can read another service's schema.

\set ON_ERROR_STOP on

SELECT format('CREATE ROLE %I', r)
FROM unnest(ARRAY['expenses_migrator', 'expenses_app', 'audit_migrator', 'audit_app']) AS r
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r)
\gexec

ALTER ROLE expenses_migrator WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD :'expenses_migrator_password';
ALTER ROLE expenses_app      WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD :'expenses_app_password';
ALTER ROLE audit_migrator    WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD :'audit_migrator_password';
ALTER ROLE audit_app         WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD :'audit_app_password';

-- Limit how long a runaway statement can hold locks.
ALTER ROLE expenses_app SET statement_timeout = '5s';
ALTER ROLE audit_app    SET statement_timeout = '5s';

-- Nobody but the four roles may connect; nobody may create objects in public.
REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"db" TO expenses_migrator, expenses_app, audit_migrator, audit_app;
-- Migrators need CREATE on the database because expenses migration 001 runs
-- CREATE SCHEMA IF NOT EXISTS, and PostgreSQL checks the privilege before it
-- checks for existence. Trade-off: a migrator could create another schema;
-- runtime roles cannot.
GRANT CREATE ON DATABASE :"db" TO expenses_migrator, audit_migrator;
REVOKE ALL ON SCHEMA public FROM PUBLIC;

CREATE SCHEMA IF NOT EXISTS expenses AUTHORIZATION expenses_migrator;
CREATE SCHEMA IF NOT EXISTS audit    AUTHORIZATION audit_migrator;
ALTER SCHEMA expenses OWNER TO expenses_migrator;
ALTER SCHEMA audit    OWNER TO audit_migrator;
REVOKE ALL ON SCHEMA expenses FROM PUBLIC;
REVOKE ALL ON SCHEMA audit    FROM PUBLIC;
