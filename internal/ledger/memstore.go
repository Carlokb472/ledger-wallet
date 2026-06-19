package ledger

import (
	"context"
	"fmt"
	"sync"
)

// MemStore is an in-memory, concurrency-safe Store. It is the zero-dependency
// backend used for tests and for running the server without a database. State is
// lost on restart — which is exactly the limitation PostgresStore removes.
type MemStore struct {
	mu       sync.RWMutex
	accounts map[string]Account
	log      []Transaction  // append-only; entries are never mutated
	idem     map[string]int // idempotency key -> index into log
	seq      int64
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		accounts: make(map[string]Account),
		idem:     make(map[string]int),
	}
}

func (s *MemStore) OpenAccount(_ context.Context, id, currency string, allowNegative bool) (Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[id]; ok {
		return Account{}, fmt.Errorf("%w: %s", ErrAccountExists, id)
	}
	a := Account{ID: id, Currency: currency, AllowNegative: allowNegative}
	s.accounts[id] = a
	return a, nil
}

func (s *MemStore) GetAccount(_ context.Context, id string) (Account, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.accounts[id]
	return a, ok, nil
}

func (s *MemStore) Balance(_ context.Context, id string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.accounts[id]; !ok {
		return 0, fmt.Errorf("%w: %s", ErrAccountNotFound, id)
	}
	return s.balanceLocked(id), nil
}

// balanceLocked sums every posting touching id. Caller must hold s.mu.
func (s *MemStore) balanceLocked(id string) int64 {
	var bal int64
	for i := range s.log {
		for _, p := range s.log[i].Postings {
			if p.AccountID == id {
				bal += p.Amount
			}
		}
	}
	return bal
}

func (s *MemStore) Transactions(_ context.Context) ([]Transaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Transaction(nil), s.log...), nil
}

// Post atomically appends a balanced double-entry transaction. The mutex makes
// the check-then-append sequence atomic on this single process — which is why a
// multi-process deployment needs PostgresStore's DB-level uniqueness instead.
func (s *MemStore) Post(_ context.Context, idempotencyKey string, postings []Posting) (Transaction, error) {
	if idempotencyKey == "" {
		return Transaction{}, ErrEmptyIdempotencyKey
	}
	if len(postings) < 2 {
		return Transaction{}, ErrTooFewPostings
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Idempotency: replay or conflict.
	if idx, ok := s.idem[idempotencyKey]; ok {
		existing := s.log[idx]
		if !samePostings(existing.Postings, postings) {
			return Transaction{}, ErrIdempotencyConflict
		}
		return existing, nil
	}

	// 2. Validate: accounts exist, one currency, sum to zero (double-entry).
	var sum int64
	currency := ""
	for _, p := range postings {
		acc, ok := s.accounts[p.AccountID]
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
		if d >= 0 || s.accounts[id].AllowNegative {
			continue
		}
		if s.balanceLocked(id)+d < 0 {
			return Transaction{}, fmt.Errorf("%w: %s", ErrInsufficientFunds, id)
		}
	}

	// 4. Append (the stored postings are a copy so the log stays immutable).
	s.seq++
	tx := Transaction{
		ID:             fmt.Sprintf("tx_%d", s.seq),
		IdempotencyKey: idempotencyKey,
		Postings:       append([]Posting(nil), postings...),
		Seq:            s.seq,
	}
	s.log = append(s.log, tx)
	s.idem[idempotencyKey] = len(s.log) - 1
	return tx, nil
}
