# ledger-wallet

A minimal **append-only, double-entry ledger** with an **idempotent** HTTP transfer API, written in Go. It ships with **two interchangeable storage backends** behind one interface:

- **in-memory** — zero dependencies, used for tests and quick demos
- **Postgres** — durable, multi-process safe, with idempotency enforced atomically by a `UNIQUE` constraint

This is a portfolio project demonstrating the core money-handling patterns every fintech / crypto-trading backend relies on. The goal isn't feature breadth — it's getting the *invariants* right and being explicit about the trade-offs.

## Why these design choices (the interesting part)

| Decision | Reason |
|---|---|
| **Money as `int64` minor units, never `float64`** | Floats can't represent `0.10` exactly; arithmetic drifts and you lose cents. Integers are exact. Format to a decimal string only at the edge (`FormatMinor`). |
| **Append-only log; balances are *derived*** | The ledger never mutates a balance field. Every balance is recomputed by folding the immutable transaction log (`SUM(amount)`), so it can never silently disagree with history. This is what makes it auditable and reconcilable. |
| **Double-entry: postings must sum to zero** | Money is only ever *moved*, never created or destroyed. A transaction that doesn't balance is rejected. The whole system always nets to zero. |
| **Idempotency keys on every write** | A client that retries after a network timeout must not double-charge. Replaying a key returns the original transaction; reusing a key with a *different* body is a `409 Conflict` — same contract as Stripe. |
| **Postgres idempotency via `UNIQUE` + `INSERT ... ON CONFLICT`** | The key is claimed atomically by the database. Two concurrent requests with the same key block on the unique index, so **exactly one** ever posts — no application-level lock, correct even across multiple server processes. |
| **A funding `world` account that may go negative** | Money enters the system from somewhere. `world` is flagged `allow_negative`; ordinary user accounts are not, so they can't overdraft. |

## Layout

```
cmd/server/        # main: selects backend (DATABASE_URL) and serves HTTP
internal/ledger/   # the domain core — no HTTP knowledge
  ledger.go        #   types + sentinel errors
  store.go         #   Store interface + Transfer() + idempotency comparison
  memstore.go      #   in-memory backend (sync.RWMutex)
  postgres.go      #   Postgres backend (pgx, transactions, row locks)
  money.go         #   int64 minor-unit helpers
  migrations/      #   SQL schema (embedded)
internal/api/      # thin stdlib net/http adapter over a ledger.Store
```

Both backends implement `ledger.Store`, so the HTTP layer is identical regardless of where money is stored — the ports-and-adapters boundary.

## Run

Requires Go 1.22+.

```bash
go test ./...                    # in-memory tests (Postgres tests auto-skip)
go run ./cmd/server              # in-memory backend on :8080
```

### With Postgres (durable backend)

```bash
make db-up        # start Postgres in Docker (creates `ledger` + `ledger_test`)
make run-pg       # run the server against it (migrates on startup)
```

Or by hand, against any Postgres:

```bash
export DATABASE_URL=postgres://ledger:ledger@localhost:5432/ledger
go run ./cmd/server
```

Data now survives restarts, and the idempotency guarantee holds across process restarts because it lives in the database.

> Port 5432 conflict: if a local (e.g. Homebrew) Postgres is already running,
> stop it first — `brew services stop postgresql@16` — so Docker can bind 5432.

### Run the Postgres integration tests

```bash
make db-up        # if not already running
make test-pg      # runs the full suite against the `ledger_test` database
```

`make help` lists every command.

## API

| Method & path | Body / headers | Purpose |
|---|---|---|
| `POST /accounts` | `{"id","currency","allow_negative"}` | Open an account |
| `GET /accounts/{id}/balance` | — | Current balance (minor units + display) |
| `POST /transfers` | `Idempotency-Key` header + `{"from","to","amount"}` | Move money |
| `GET /transactions` | — | The append-only log |

### Example session

```bash
curl -s localhost:8080/accounts -d '{"id":"alice","currency":"HKD"}'
curl -s localhost:8080/accounts -d '{"id":"bob","currency":"HKD"}'

# Fund alice with 100.00 from the world account.
curl -s localhost:8080/transfers -H 'Idempotency-Key: seed-1' \
  -d '{"from":"world","to":"alice","amount":10000}'

# Move 30.00 alice -> bob.
curl -s localhost:8080/transfers -H 'Idempotency-Key: pay-42' \
  -d '{"from":"alice","to":"bob","amount":3000}'

# Retrying the SAME key is safe — bob is credited only once.
curl -s localhost:8080/transfers -H 'Idempotency-Key: pay-42' \
  -d '{"from":"alice","to":"bob","amount":3000}'

curl -s localhost:8080/accounts/bob/balance   # -> {"balance":3000,"display":"30.00",...}
```

## Tested invariants

In-memory **and** Postgres backends are covered by the same scenarios:

- transfers move money and the system always nets to zero
- replaying an idempotency key charges exactly once
- reusing a key with a different body is a conflict
- unbalanced postings are rejected; ordinary accounts cannot overdraft
- currency mismatches and unknown accounts are rejected

Postgres-only tests additionally prove:

- **concurrency** — 12 goroutines firing the *same* idempotency key result in exactly one charge (the `INSERT ... ON CONFLICT` claim)
- **durability** — a fresh connection sees what a previous one committed

## How the Postgres `Post` stays correct

One SQL transaction does the whole thing:

1. **Claim the key** — `INSERT INTO transactions (idempotency_key) ... ON CONFLICT DO NOTHING RETURNING seq`. A returned row means we won; no row means the key exists, so we load the original and either replay it or reject the conflict.
2. **Lock the accounts** — `SELECT ... FOR UPDATE` in sorted order (deadlock-free) so concurrent transfers on the same account serialise.
3. **Validate** — accounts exist, single currency, postings sum to zero, no overdraft.
4. **Append postings and commit.**

## Next steps

- **Balance snapshots**: cache per-account balances, updated transactionally, to avoid `SUM`-ing the whole postings table on every read.
- **Idempotency-key TTL**: a cleanup job deleting keys older than the retry window (e.g. 24h), plus an `in-progress`/`completed` status column to handle crashed mid-flight requests.
- **Multi-currency transfers**: FX postings through an intermediary account.
