package ledger

import "context"

// Store is the persistence-agnostic interface the HTTP layer depends on. Both
// MemStore (in-memory) and PostgresStore implement it, so the same API server
// can run against either backend — chosen at startup. This is the ports-and-
// adapters boundary: the HTTP layer knows nothing about how money is stored.
type Store interface {
	OpenAccount(ctx context.Context, id, currency string, allowNegative bool) (Account, error)
	GetAccount(ctx context.Context, id string) (Account, bool, error)
	Balance(ctx context.Context, id string) (int64, error)
	Post(ctx context.Context, idempotencyKey string, postings []Posting) (Transaction, error)
	Transactions(ctx context.Context) ([]Transaction, error)
}

// Transfer moves a positive amount (minor units) between two accounts as a
// balanced, two-leg posting. It works against any Store and is idempotent on
// idempotencyKey. Kept as a free function so both backends share it.
func Transfer(ctx context.Context, s Store, idempotencyKey, from, to string, amount int64) (Transaction, error) {
	if amount <= 0 {
		return Transaction{}, ErrInvalidAmount
	}
	return s.Post(ctx, idempotencyKey, []Posting{
		{AccountID: from, Amount: -amount},
		{AccountID: to, Amount: amount},
	})
}

// samePostings reports whether two posting sets are equal as multisets (order
// does not matter). Posting is comparable, so it can key a map directly. Used by
// both backends to tell an idempotent replay from a genuine conflict.
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
