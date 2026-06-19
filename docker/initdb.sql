-- Runs once when the Postgres data volume is first initialised.
-- POSTGRES_DB already created the `ledger` database; add a separate one for the
-- integration tests so `make test-pg` never truncates app data.
CREATE DATABASE ledger_test;
