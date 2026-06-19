-- Schema for the Postgres-backed ledger. Safe to run repeatedly (IF NOT EXISTS).

CREATE TABLE IF NOT EXISTS accounts (
    id             TEXT PRIMARY KEY,
    currency       TEXT    NOT NULL,
    allow_negative BOOLEAN NOT NULL DEFAULT FALSE
);

-- One row per transaction. The UNIQUE constraint on idempotency_key is the heart
-- of the whole design: it lets a concurrent "INSERT ... ON CONFLICT DO NOTHING"
-- atomically decide which request owns a key, so a retried/duplicated request
-- can never post twice. The public id is derived in Go as 'tx_' || seq.
CREATE TABLE IF NOT EXISTS transactions (
    seq             BIGSERIAL   PRIMARY KEY,
    idempotency_key TEXT        NOT NULL UNIQUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The append-only postings. Balances are derived by SUM(amount), never stored.
CREATE TABLE IF NOT EXISTS postings (
    id         BIGSERIAL PRIMARY KEY,
    tx_seq     BIGINT    NOT NULL REFERENCES transactions(seq),
    account_id TEXT      NOT NULL REFERENCES accounts(id),
    amount     BIGINT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_postings_account ON postings(account_id);
