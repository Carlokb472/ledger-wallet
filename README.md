# ledger-wallet

A minimal **append-only, double-entry ledger** with an **idempotent** HTTP transfer API, written in Go with **zero third-party dependencies** (standard library only).

This is a portfolio project that demonstrates the core money-handling patterns every fintech / crypto-trading backend relies on. The goal is not feature breadth — it is to get the *invariants* right and to be explicit about the trade-offs.

## Why these design choices (the interesting part)

| Decision | Reason |
|---|---|
| **Money as `int64` minor units, never `float64`** | Floats can't represent `0.10` exactly; arithmetic drifts and you lose cents. Integers are exact. Format to a decimal string only at the edge (`FormatMinor`). |
| **Append-only log; balances are *derived*** | The ledger never mutates a balance field. Every balance is recomputed by folding the immutable transaction log, so it can never silently disagree with history. This is what makes the system auditable and reconcilable. |
| **Double-entry: postings must sum to zero** | Money is only ever *moved*, never created or destroyed. A transaction that doesn't balance is rejected (`ErrUnbalanced`). The whole system always nets to zero. |
| **Idempotency keys on every write** | A client that retries after a network timeout must not double-charge. Replaying a key returns the original transaction; reusing a key with a *different* body is a `409 Conflict` — same contract as Stripe. |
| **A funding `world` account that may go negative** | Money enters the system from somewhere. `world` is flagged `allow_negative`; ordinary user accounts are not, so they can't overdraft. |

## Layout

```
cmd/server/        # main: wires the ledger to the HTTP server
internal/ledger/   # the domain core — Ledger, Posting, Transaction (no HTTP here)
internal/api/      # thin HTTP adapter over the ledger (stdlib net/http)
```

The domain (`internal/ledger`) has **no knowledge of HTTP**; the API layer maps domain errors to status codes. You can test, reuse, or swap either side independently.

## Run

Requires Go 1.22+.

```bash
go test ./...          # run the test suite
go vet ./...           # static checks
go run ./cmd/server    # start the API on :8080
```

## API

| Method & path | Body / headers | Purpose |
|---|---|---|
| `POST /accounts` | `{"id","currency","allow_negative"}` | Open an account |
| `GET /accounts/{id}/balance` | — | Current balance (minor units + display) |
| `POST /transfers` | `Idempotency-Key` header + `{"from","to","amount"}` | Move money |
| `GET /transactions` | — | The append-only log |

### Example session

```bash
# Open two user accounts (a "world" funding account is created at startup).
curl -s localhost:8080/accounts -d '{"id":"alice","currency":"HKD"}'
curl -s localhost:8080/accounts -d '{"id":"bob","currency":"HKD"}'

# Fund alice with 100.00 from world.
curl -s localhost:8080/transfers \
  -H 'Idempotency-Key: seed-1' \
  -d '{"from":"world","to":"alice","amount":10000}'

# Move 30.00 alice -> bob.
curl -s localhost:8080/transfers \
  -H 'Idempotency-Key: pay-42' \
  -d '{"from":"alice","to":"bob","amount":3000}'

# Retrying the SAME key is safe — bob is credited only once.
curl -s localhost:8080/transfers \
  -H 'Idempotency-Key: pay-42' \
  -d '{"from":"alice","to":"bob","amount":3000}'

curl -s localhost:8080/accounts/bob/balance   # -> {"balance":3000,"display":"30.00",...}
```

## Tested invariants

- transfers move money and the system always nets to zero
- replaying an idempotency key charges exactly once
- reusing a key with a different body is a conflict
- unbalanced postings are rejected
- ordinary accounts cannot overdraft (blocked transfers leave balances untouched)
- currency mismatches and unknown accounts are rejected

## Next steps (deliberately out of scope here)

- **Persistence**: swap the in-memory log for Postgres (`transactions` + `postings` tables, the key being an append-only insert). Add a `UNIQUE` constraint on the idempotency key to enforce it at the DB layer too.
- **Balance snapshots**: cache per-account balances and update them transactionally to avoid folding the whole log on every read.
- **Concurrency**: the current `sync.RWMutex` serialises writes; a DB-backed version would use row locks / serializable transactions instead.
```
