// Package ledger implements an append-only, double-entry ledger.
//
// Design in one breath:
//   - Money never mutates. We keep an append-only log of transactions; a balance
//     is *derived* by folding the log, so it always reconciles with history.
//   - Every transaction is double-entry: its postings must sum to zero, so money
//     is only ever moved, never created or destroyed.
//   - Writes are idempotent on a caller-supplied key, so a retried request can
//     never double-charge.
package ledger

import (
	"errors"
	"fmt"
	"sync"
)

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

// Transaction is one immutable entry in the append-only log.
type Transaction struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotency_key"`
	Postings       []Posting `json:"postings"`
	Seq            int64     `json:"seq"`
}

// Ledger is an in-memory, concurrency-safe ledger. Persistence (e.g. Postgres)
// is intentionally out of scope for this skeleton — see README "Next steps".
type Ledger struct {
	mu       sync.RWMutex
	accounts map[string]Account
	log      []Transaction  // append-only; entries are never mutated
	idem     map[string]int // idempotency key -> index into log
	seq      int64
}

// New returns an empty ledger.
func New() *Ledger {
	return &Ledger{
		accounts: make(map[string]Account),
		idem:     make(map[string]int),
	}
}

// OpenAccount registers a new account. It is an error to open an ID twice.
func (l *Ledger) OpenAccount(id, currency string, allowNegative bool) (Account, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.accounts[id]; ok {
		return Account{}, fmt.Errorf("%w: %s", ErrAccountExists, id)
	}
	a := Account{ID: id, Currency: currency, AllowNegative: allowNegative}
	l.accounts[id] = a
	return a, nil
}

// GetAccount returns the account and whether it exists.
func (l *Ledger) GetAccount(id string) (Account, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	a, ok := l.accounts[id]
	return a, ok
}

// Balance returns the account's current balance in minor units, derived from the
// append-only log.
func (l *Ledger) Balance(id string) (int64, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if _, ok := l.accounts[id]; !ok {
		return 0, fmt.Errorf("%w: %s", ErrAccountNotFound, id)
	}
	return l.balanceLocked(id), nil
}

// balanceLocked sums every posting touching id. Caller must hold l.mu.
// Deriving the balance from the log (instead of storing a mutable number) is the
// whole point: it can never silently disagree with history. Snapshotting for
// performance is a later optimisation, noted in the README.
func (l *Ledger) balanceLocked(id string) int64 {
	var bal int64
	for i := range l.log {
		for _, p := range l.log[i].Postings {
			if p.AccountID == id {
				bal += p.Amount
			}
		}
	}
	return bal
}

// Transactions returns a copy of the append-only log.
func (l *Ledger) Transactions() []Transaction {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]Transaction(nil), l.log...)
}

// Transfer moves a positive amount (minor units) between two accounts as a
// balanced, two-leg posting. It is idempotent on idempotencyKey.
func (l *Ledger) Transfer(idempotencyKey, from, to string, amount int64) (Transaction, error) {
	if amount <= 0 {
		return Transaction{}, ErrInvalidAmount
	}
	return l.Post(idempotencyKey, []Posting{
		{AccountID: from, Amount: -amount},
		{AccountID: to, Amount: amount},
	})
}

// Post atomically appends a balanced double-entry transaction.
//
// Idempotency contract (mirrors real payment APIs like Stripe):
//   - Replaying the SAME key with the SAME postings returns the original
//     transaction without posting again.
//   - Reusing a key with DIFFERENT postings is a conflict (ErrIdempotencyConflict).
func (l *Ledger) Post(idempotencyKey string, postings []Posting) (Transaction, error) {
	if idempotencyKey == "" {
		return Transaction{}, ErrEmptyIdempotencyKey
	}
	if len(postings) < 2 {
		return Transaction{}, ErrTooFewPostings
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// 1. Idempotency: replay or conflict.
	if idx, ok := l.idem[idempotencyKey]; ok {
		existing := l.log[idx]
		if !samePostings(existing.Postings, postings) {
			return Transaction{}, ErrIdempotencyConflict
		}
		return existing, nil
	}

	// 2. Validate: accounts exist, one currency, sum to zero (double-entry).
	var sum int64
	currency := ""
	for _, p := range postings {
		acc, ok := l.accounts[p.AccountID]
		if !ok {
			return Transaction{}, fmt.Errorf("%w: %s", ErrAccountNotFound, p.AccountID)
		}
		if currency == "" {
			currency = acc.Currency
		} else if acc.Currency != currency {
			return Transaction{}, ErrCurrencyMismatch
		}
		sum += p.Amount
	}
	if sum != 0 {
		return Transaction{}, ErrUnbalanced
	}

	// 3. Overdraft: project each account's resulting balance.
	deltas := make(map[string]int64)
	for _, p := range postings {
		deltas[p.AccountID] += p.Amount
	}
	for id, d := range deltas {
		if d >= 0 {
			continue
		}
		if l.accounts[id].AllowNegative {
			continue
		}
		if l.balanceLocked(id)+d < 0 {
			return Transaction{}, fmt.Errorf("%w: %s", ErrInsufficientFunds, id)
		}
	}

	// 4. Append. The stored postings are a copy so the log stays immutable even
	// if the caller mutates its slice afterwards.
	l.seq++
	tx := Transaction{
		ID:             fmt.Sprintf("tx_%d", l.seq),
		IdempotencyKey: idempotencyKey,
		Postings:       append([]Posting(nil), postings...),
		Seq:            l.seq,
	}
	l.log = append(l.log, tx)
	l.idem[idempotencyKey] = len(l.log) - 1
	return tx, nil
}

// samePostings reports whether two posting sets are equal as multisets (order
// does not matter). Posting is comparable, so it can key a map directly.
func samePostings(a, b []Posting) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[Posting]int, len(a))
	for _, p := range a {
		counts[p]++
	}
	for _, p := range b {
		counts[p]--
		if counts[p] < 0 {
			return false
		}
	}
	return true
}
