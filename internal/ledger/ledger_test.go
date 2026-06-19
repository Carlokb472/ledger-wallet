package ledger

import (
	"context"
	"errors"
	"testing"
)

// newFunded builds an in-memory store with a funding "world" account and two
// user accounts, then seeds alice with 100.00 (10000 minor units).
func newFunded(t *testing.T) (*MemStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	s := NewMemStore()
	for _, a := range []struct {
		id            string
		allowNegative bool
	}{{"world", true}, {"alice", false}, {"bob", false}} {
		if _, err := s.OpenAccount(ctx, a.id, "HKD", a.allowNegative); err != nil {
			t.Fatalf("open %s: %v", a.id, err)
		}
	}
	if _, err := Transfer(ctx, s, "seed-alice", "world", "alice", 10000); err != nil {
		t.Fatalf("fund alice: %v", err)
	}
	return s, ctx
}

func mustBalance(t *testing.T, s Store, ctx context.Context, id string) int64 {
	t.Helper()
	b, err := s.Balance(ctx, id)
	if err != nil {
		t.Fatalf("balance %s: %v", id, err)
	}
	return b
}

func TestTransferMovesMoney(t *testing.T) {
	s, ctx := newFunded(t)
	if _, err := Transfer(ctx, s, "t1", "alice", "bob", 3000); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if got := mustBalance(t, s, ctx, "alice"); got != 7000 {
		t.Errorf("alice = %d, want 7000", got)
	}
	if got := mustBalance(t, s, ctx, "bob"); got != 3000 {
		t.Errorf("bob = %d, want 3000", got)
	}
	// Double-entry invariant: the whole system always nets to zero.
	sum := mustBalance(t, s, ctx, "world") + mustBalance(t, s, ctx, "alice") + mustBalance(t, s, ctx, "bob")
	if sum != 0 {
		t.Errorf("system sum = %d, want 0", sum)
	}
}

func TestIdempotentReplayChargesOnce(t *testing.T) {
	s, ctx := newFunded(t)
	tx1, err := Transfer(ctx, s, "dup", "alice", "bob", 2500)
	if err != nil {
		t.Fatal(err)
	}
	tx2, err := Transfer(ctx, s, "dup", "alice", "bob", 2500)
	if err != nil {
		t.Fatal(err)
	}
	if tx1.ID != tx2.ID {
		t.Errorf("replay created a new tx: %s vs %s", tx1.ID, tx2.ID)
	}
	if got := mustBalance(t, s, ctx, "bob"); got != 2500 {
		t.Errorf("bob = %d, want 2500 (charged exactly once)", got)
	}
	txns, _ := s.Transactions(ctx)
	if len(txns) != 2 { // seed + one transfer
		t.Errorf("log has %d txns, want 2", len(txns))
	}
}

func TestIdempotencyConflict(t *testing.T) {
	s, ctx := newFunded(t)
	if _, err := Transfer(ctx, s, "k", "alice", "bob", 1000); err != nil {
		t.Fatal(err)
	}
	_, err := Transfer(ctx, s, "k", "alice", "bob", 9999) // same key, different amount
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Errorf("err = %v, want ErrIdempotencyConflict", err)
	}
}

func TestRejectsUnbalanced(t *testing.T) {
	s, ctx := newFunded(t)
	_, err := s.Post(ctx, "bad", []Posting{
		{AccountID: "alice", Amount: -1000},
		{AccountID: "bob", Amount: 900}, // 100 vanished into thin air
	})
	if !errors.Is(err, ErrUnbalanced) {
		t.Errorf("err = %v, want ErrUnbalanced", err)
	}
}

func TestPreventsOverdraft(t *testing.T) {
	s, ctx := newFunded(t)
	_, err := Transfer(ctx, s, "od", "alice", "bob", 10001) // alice only has 10000
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("err = %v, want ErrInsufficientFunds", err)
	}
	if got := mustBalance(t, s, ctx, "alice"); got != 10000 {
		t.Errorf("alice = %d, want 10000 (unchanged)", got)
	}
}

func TestCurrencyMismatch(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	s.OpenAccount(ctx, "hkd", "HKD", true)
	s.OpenAccount(ctx, "usd", "USD", true)
	_, err := s.Post(ctx, "x", []Posting{
		{AccountID: "hkd", Amount: -1000},
		{AccountID: "usd", Amount: 1000},
	})
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("err = %v, want ErrCurrencyMismatch", err)
	}
}

func TestUnknownAccount(t *testing.T) {
	s, ctx := newFunded(t)
	_, err := Transfer(ctx, s, "u", "alice", "ghost", 100)
	if !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("err = %v, want ErrAccountNotFound", err)
	}
}

func TestRequiresIdempotencyKey(t *testing.T) {
	s, ctx := newFunded(t)
	_, err := Transfer(ctx, s, "", "alice", "bob", 100)
	if !errors.Is(err, ErrEmptyIdempotencyKey) {
		t.Errorf("err = %v, want ErrEmptyIdempotencyKey", err)
	}
}

func TestRejectsNonPositiveAmount(t *testing.T) {
	s, ctx := newFunded(t)
	if _, err := Transfer(ctx, s, "z", "alice", "bob", 0); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("err = %v, want ErrInvalidAmount", err)
	}
}

func TestFormatMinor(t *testing.T) {
	cases := map[int64]string{0: "0.00", 5: "0.05", 100: "1.00", 12345: "123.45", -250: "-2.50"}
	for in, want := range cases {
		if got := FormatMinor(in); got != want {
			t.Errorf("FormatMinor(%d) = %q, want %q", in, got, want)
		}
	}
}
