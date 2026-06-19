// Package api exposes the ledger over HTTP using only the standard library.
// Routing uses the Go 1.22 method+path patterns in net/http.ServeMux, so there
// are no third-party dependencies.
package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Carlokb472/ledger-wallet/internal/ledger"
)

// Server adapts a *ledger.Ledger to an http.Handler.
type Server struct {
	ledger *ledger.Ledger
	mux    *http.ServeMux
}

// NewServer wires the routes and returns a ready handler.
func NewServer(l *ledger.Ledger) *Server {
	s := &Server{ledger: l, mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /accounts", s.handleOpenAccount)
	s.mux.HandleFunc("GET /accounts/{id}/balance", s.handleBalance)
	s.mux.HandleFunc("POST /transfers", s.handleTransfer)
	s.mux.HandleFunc("GET /transactions", s.handleTransactions)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

type openAccountReq struct {
	ID            string `json:"id"`
	Currency      string `json:"currency"`
	AllowNegative bool   `json:"allow_negative"`
}

func (s *Server) handleOpenAccount(w http.ResponseWriter, r *http.Request) {
	var req openAccountReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	acc, err := s.ledger.OpenAccount(req.ID, req.Currency, req.AllowNegative)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, acc)
}

func (s *Server) handleBalance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bal, err := s.ledger.Balance(id)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account_id": id,
		"balance":    bal,
		"display":    ledger.FormatMinor(bal),
	})
}

type transferReq struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount int64  `json:"amount"`
}

// handleTransfer reads the idempotency key from the Idempotency-Key header — the
// real-world convention — so a client that retries on a timeout cannot
// double-charge.
func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeErr(w, http.StatusBadRequest, "missing Idempotency-Key header")
		return
	}
	var req transferReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	tx, err := s.ledger.Transfer(key, req.From, req.To, req.Amount)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (s *Server) handleTransactions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ledger.Transactions())
}

// statusFor maps domain errors to HTTP status codes via errors.Is, keeping the
// HTTP layer ignorant of the ledger's internals beyond its sentinel errors.
func statusFor(err error) int {
	switch {
	case errors.Is(err, ledger.ErrAccountNotFound):
		return http.StatusNotFound
	case errors.Is(err, ledger.ErrAccountExists),
		errors.Is(err, ledger.ErrIdempotencyConflict):
		return http.StatusConflict
	case errors.Is(err, ledger.ErrInsufficientFunds),
		errors.Is(err, ledger.ErrUnbalanced),
		errors.Is(err, ledger.ErrCurrencyMismatch),
		errors.Is(err, ledger.ErrInvalidAmount),
		errors.Is(err, ledger.ErrEmptyIdempotencyKey),
		errors.Is(err, ledger.ErrTooFewPostings):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
