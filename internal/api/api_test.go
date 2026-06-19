package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Carlokb472/ledger-wallet/internal/ledger"
)

func setup(t *testing.T) *Server {
	t.Helper()
	l := ledger.New()
	l.OpenAccount("world", "HKD", true)
	l.OpenAccount("alice", "HKD", false)
	l.OpenAccount("bob", "HKD", false)
	if _, err := l.Transfer("seed", "world", "alice", 10000); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return NewServer(l)
}

func do(t *testing.T, s *Server, method, path, key string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func balance(t *testing.T, s *Server, id string) int64 {
	t.Helper()
	rec := do(t, s, "GET", "/accounts/"+id+"/balance", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("balance %s: status %d", id, rec.Code)
	}
	var got struct {
		Balance int64 `json:"balance"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode balance: %v", err)
	}
	return got.Balance
}

func TestTransferEndpoint(t *testing.T) {
	s := setup(t)
	rec := do(t, s, "POST", "/transfers", "k1", transferReq{From: "alice", To: "bob", Amount: 3000})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := balance(t, s, "bob"); got != 3000 {
		t.Errorf("bob balance = %d, want 3000", got)
	}
	if got := balance(t, s, "alice"); got != 7000 {
		t.Errorf("alice balance = %d, want 7000", got)
	}
}

func TestTransferIsIdempotentOverHTTP(t *testing.T) {
	s := setup(t)
	body := transferReq{From: "alice", To: "bob", Amount: 2000}
	do(t, s, "POST", "/transfers", "same-key", body)
	do(t, s, "POST", "/transfers", "same-key", body) // retry
	if got := balance(t, s, "bob"); got != 2000 {
		t.Errorf("bob balance = %d, want 2000 (charged once despite retry)", got)
	}
}

func TestTransferMissingIdempotencyKey(t *testing.T) {
	s := setup(t)
	rec := do(t, s, "POST", "/transfers", "", transferReq{From: "alice", To: "bob", Amount: 1000})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestOverdraftReturns422(t *testing.T) {
	s := setup(t)
	rec := do(t, s, "POST", "/transfers", "od", transferReq{From: "alice", To: "bob", Amount: 99999})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

func TestUnknownAccountReturns404(t *testing.T) {
	s := setup(t)
	rec := do(t, s, "GET", "/accounts/ghost/balance", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDuplicateAccountReturns409(t *testing.T) {
	s := setup(t)
	rec := do(t, s, "POST", "/accounts", "", openAccountReq{ID: "alice", Currency: "HKD"})
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}
