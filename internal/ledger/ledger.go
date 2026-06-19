// Package ledger implements an append-only, double-entry ledger with two
// interchangeable storage backends (in-memory and Postgres) behind a single
// Store interface.
//
// Design in one breath:
//   - Money never mutates. Transactions are appended to an immutable log; a
//     balance is *derived* by folding that log, so it always reconciles with
//     history.
//   - Every transaction is double-entry: its postings must sum to zero, so money
//     is only ever moved, never created or destroyed.
//   - Writes are idempotent on a caller-supplied key, so a retried request can
//     never double-charge. In Postgres this is enforced atomically by a UNIQUE
//     constraint via INSERT ... ON CONFLICT.
package ledger

import "errors"

// Sentinel errors. Callers use errors.Is to map these to HTTP status codes.
var (
	ErrAccountExists       = errors.New("ledger: account already exists")
	ErrAccountNotFound     = errors.New("ledger: account not found")
	ErrCurrencyMismatch    = errors.New("ledger: postings mix currencies")
	ErrUnbalanced          = errors.New("ledger: postings do not sum to zero")
	ErrInsufficientFunds   = errors.New("ledger: insufficient funds")
	ErrInvalidAmount       = errors.New("ledger: amount must be positive")
	ErrEmptyIdempotencyKey = errors.New("ledger: idempotency key required")
	ErrIdempotencyConflict = errors.New("ledger: idempotency key reused with a different request")
	ErrTooFewPostings      = errors.New("ledger: a transaction needs at least two postings")
)

// Account is a named bucket money can sit in. AllowNegative marks a funding or
// "world" account that represents money entering/leaving the system and is
// therefore permitted to go negative; ordinary user accounts are not.
type Account struct {
	ID            string `json:"id"`
	Currency      string `json:"currency"`
	AllowNegative bool   `json:"allow_negative"`
}

// Posting is one leg of a double-entry transaction. Amount is in minor units and
// is signed: positive credits the account, negative debits it. The postings of a
// single transaction MUST sum to zero.
type Posting struct {
	AccountID string `json:"account_id"`
	Amount    int64  `json:"amount"`
}

// Transaction is one immutable entry in the append-only log. Seq is the
// monotonic primary key; ID is the public "tx_<seq>" rendering of it.
type Transaction struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotency_key"`
	Postings       []Posting `json:"postings"`
	Seq            int64     `json:"seq"`
}
