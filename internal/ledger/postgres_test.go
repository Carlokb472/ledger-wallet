package ledger

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
)

// These tests run against a real Postgres. They are skipped unless
// TEST_DATABASE_URL is set, e.g.:
//
//	TEST_DATABASE_URL=postgres://localhost:5432/ledger_test go test ./...
//
// Each test starts from a truncated schema so runs are independent.
func newPGStore(t *testing.T) (*PostgresStore, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run Postgres integration tests")
	}
	ctx := context.Background()
	st, err := NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.pool.Exec(ctx,
		`TRUNCATE postings, transactions, accounts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(st.Close)
	return st, ctx
}

func pgFunded(t *testing.T) (*PostgresStore, context.Context) {
	t.Helper()
	st, ctx := newPGStore(t)
	for _, a := range []struct {
		id  string
		neg bool
	}{{"world", true}, {"alice", false}, {"bob", false}} {
		if _, err := st.OpenAccount(ctx, a.id, "HKD", a.neg); err != nil {
			t.Fatalf("open %s: %v", a.id, err)
		}
	}
	if _, err := Transfer(ctx, st, "seed", "world", "alice", 10000); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return st, ctx
}

func pgBalance(t *testing.T, st *PostgresStore, ctx context.Context, id string) int64 {
	t.Helper()
	b, err := st.Balance(ctx, id)
	if err != nil {
		t.Fatalf("balance %s: %v", id, err)
	}
	return b
}

func TestPGTransferAndBalance(t *testing.T) {
	st, ctx := pgFunded(t)
	if _, err := Transfer(ctx, st, "t1", "alice", "bob", 3000); err != nil {
		t.Fatal(err)
	}
	if got := pgBalance(t, st, ctx, "alice"); got != 7000 {
		t.Errorf("alice = %d, want 7000", got)
	}
	if got := pgBalance(t, st, ctx, "bob"); got != 3000 {
		t.Errorf("bob = %d, want 3000", got)
	}
}

func TestPGIdempotentReplay(t *testing.T) {
	st, ctx := pgFunded(t)
	tx1, err := Transfer(ctx, st, "dup", "alice", "bob", 2500)
	if err != nil {
		t.Fatal(err)
	}
	tx2, err := Transfer(ctx, st, "dup", "alice", "bob", 2500)
	if err != nil {
		t.Fatal(err)
	}
	if tx1.Seq != tx2.Seq {
		t.Errorf("replay produced a new tx: %d vs %d", tx1.Seq, tx2.Seq)
	}
	if got := pgBalance(t, st, ctx, "bob"); got != 2500 {
		t.Errorf("bob = %d, want 2500 (charged once)", got)
	}
}

func TestPGIdempotencyConflict(t *testing.T) {
	st, ctx := pgFunded(t)
	if _, err := Transfer(ctx, st, "k", "alice", "bob", 1000); err != nil {
		t.Fatal(err)
	}
	_, err := Transfer(ctx, st, "k", "alice", "bob", 2000)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Errorf("err = %v, want ErrIdempotencyConflict", err)
	}
}

func TestPGOverdraft(t *testing.T) {
	st, ctx := pgFunded(t)
	_, err := Transfer(ctx, st, "od", "alice", "bob", 10001)
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("err = %v, want ErrInsufficientFunds", err)
	}
	if got := pgBalance(t, st, ctx, "alice"); got != 10000 {
		t.Errorf("alice = %d, want 10000 (unchanged)", got)
	}
}

// TestPGConcurrentSameKey is the headline test: fire the SAME idempotency key
// from many goroutines at once. The UNIQUE constraint + INSERT ... ON CONFLICT
// must ensure exactly one charge lands, no error, no double-spend.
func TestPGConcurrentSameKey(t *testing.T) {
	st, ctx := pgFunded(t)
	const n = 12
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = Transfer(ctx, st, "race", "alice", "bob", 1000)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error %v", i, err)
		}
	}
	if got := pgBalance(t, st, ctx, "bob"); got != 1000 {
		t.Errorf("bob = %d, want 1000 (charged once despite %d concurrent requests)", got, n)
	}
}

// TestPGPersistsAcrossConnections proves data is durable: a brand-new store
// (fresh pool) sees what a previous one wrote.
func TestPGPersistsAcrossConnections(t *testing.T) {
	st, ctx := pgFunded(t)
	if _, err := Transfer(ctx, st, "p1", "alice", "bob", 1500); err != nil {
		t.Fatal(err)
	}
	url := os.Getenv("TEST_DATABASE_URL")
	st2, err := NewPostgresStore(ctx, url)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer st2.Close()
	if got := pgBalance(t, st2, ctx, "bob"); got != 1500 {
		t.Errorf("bob = %d on a fresh connection, want 1500 (durable)", got)
	}
}
