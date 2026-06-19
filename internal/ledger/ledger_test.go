package ledger

import (
	"errors"
	"testing"
)

// newFunded builds a ledger with a funding "world" account and two user
// accounts, then seeds alice with 100.00 (10000 minor units).
func newFunded(t *testing.T) *Ledger {
	t.Helper()
	l := New()
	for _, a := range []struct {
		id            string
		allowNegative bool
	}{{"world", true}, {"alice", false}, {"bob", false}} {
		if _, err := l.OpenAccount(a.id, "HKD", a.allowNegative); err != nil {
			t.Fatalf("open %s: %v", a.id, err)
		}
	}
	if _, err := l.Transfer("seed-alice", "world", "alice", 10000); err != nil {
		t.Fatalf("fund alice: %v", err)
	}
	return l
}

func mustBalance(t *testing.T, l *Ledger, id string) int64 {
	t.Helper()
	b, err := l.Balance(id)
	if err != nil {
		t.Fatalf("balance %s: %v", id, err)
	}
	return b
}

func TestTransferMovesMoney(t *testing.T) {
	l := newFunded(t)
	if _, err := l.Transfer("t1", "alice", "bob", 3000); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if got := mustBalance(t, l, "alice"); got != 7000 {
		t.Errorf("alice = %d, want 7000", got)
	}
	if got := mustBalance(t, l, "bob"); got != 3000 {
		t.Errorf("bob = %d, want 3000", got)
	}
	// Double-entry invariant: the whole system always nets to zero.
	sum := mustBalance(t, l, "world") + mustBalance(t, l, "alice") + mustBalance(t, l, "bob")
	if sum != 0 {
		t.Errorf("system sum = %d, want 0", sum)
	}
}

func TestIdempotentReplayChargesOnce(t *testing.T) {
	l := newFunded(t)
	tx1, err := l.Transfer("dup", "alice", "bob", 2500)
	if err != nil {
		t.Fatal(err)
	}
	tx2, err := l.Transfer("dup", "alice", "bob", 2500)
	if err != nil {
		t.Fatal(err)
	}
	if tx1.ID != tx2.ID {
		t.Errorf("replay created a new tx: %s vs %s", tx1.ID, tx2.ID)
	}
	if got := mustBalance(t, l, "bob"); got != 2500 {
		t.Errorf("bob = %d, want 2500 (charged exactly once)", got)
	}
	if n := len(l.Transactions()); n != 2 { // seed + one transfer
		t.Errorf("log has %d txns, want 2", n)
	}
}

func TestIdempotencyConflict(t *testing.T) {
	l := newFunded(t)
	if _, err := l.Transfer("k", "alice", "bob", 1000); err != nil {
		t.Fatal(err)
	}
	_, err := l.Transfer("k", "alice", "bob", 9999) // same key, different amount
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Errorf("err = %v, want ErrIdempotencyConflict", err)
	}
}

func TestRejectsUnbalanced(t *testing.T) {
	l := newFunded(t)
	_, err := l.Post("bad", []Posting{
		{AccountID: "alice", Amount: -1000},
		{AccountID: "bob", Amount: 900}, // 100 vanished into thin air
	})
	if !errors.Is(err, ErrUnbalanced) {
		t.Errorf("err = %v, want ErrUnbalanced", err)
	}
}

func TestPreventsOverdraft(t *testing.T) {
	l := newFunded(t)
	_, err := l.Transfer("od", "alice", "bob", 10001) // alice only has 10000
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("err = %v, want ErrInsufficientFunds", err)
	}
	// The blocked transfer must leave balances untouched.
	if got := mustBalance(t, l, "alice"); got != 10000 {
		t.Errorf("alice = %d, want 10000 (unchanged)", got)
	}
}

func TestCurrencyMismatch(t *testing.T) {
	l := New()
	l.OpenAccount("hkd", "HKD", true)
	l.OpenAccount("usd", "USD", true)
	_, err := l.Post("x", []Posting{
		{AccountID: "hkd", Amount: -1000},
		{AccountID: "usd", Amount: 1000},
	})
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("err = %v, want ErrCurrencyMismatch", err)
	}
}

func TestUnknownAccount(t *testing.T) {
	l := newFunded(t)
	_, err := l.Transfer("u", "alice", "ghost", 100)
	if !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("err = %v, want ErrAccountNotFound", err)
	}
}

func TestRequiresIdempotencyKey(t *testing.T) {
	l := newFunded(t)
	_, err := l.Transfer("", "alice", "bob", 100)
	if !errors.Is(err, ErrEmptyIdempotencyKey) {
		t.Errorf("err = %v, want ErrEmptyIdempotencyKey", err)
	}
}

func TestRejectsNonPositiveAmount(t *testing.T) {
	l := newFunded(t)
	if _, err := l.Transfer("z", "alice", "bob", 0); !errors.Is(err, ErrInvalidAmount) {
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
