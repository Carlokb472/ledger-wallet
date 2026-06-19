// Command server runs the ledger-wallet HTTP API.
//
//	go run ./cmd/server                       # in-memory backend, :8080
//	DATABASE_URL=postgres://localhost:5432/ledger go run ./cmd/server
//	ADDR=:9000 go run ./cmd/server            # custom listen address
//
// If DATABASE_URL is set the durable Postgres backend is used (and migrated on
// startup); otherwise the in-memory backend is used.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/Carlokb472/ledger-wallet/internal/api"
	"github.com/Carlokb472/ledger-wallet/internal/ledger"
)

func main() {
	ctx := context.Background()

	store, err := openStore(ctx)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	// Bootstrap a funding "world" account so money can legitimately enter the
	// system. With Postgres this already exists after the first run, so an
	// "already exists" error is expected and ignored.
	if _, err := store.OpenAccount(ctx, "world", "HKD", true); err != nil && !errors.Is(err, ledger.ErrAccountExists) {
		log.Fatalf("bootstrap world account: %v", err)
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("ledger-wallet listening on %s", addr)
	if err := http.ListenAndServe(addr, api.NewServer(store)); err != nil {
		log.Fatal(err)
	}
}

func openStore(ctx context.Context) (ledger.Store, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Print("using in-memory backend (set DATABASE_URL for Postgres)")
		return ledger.NewMemStore(), nil
	}
	log.Print("using Postgres backend")
	ps, err := ledger.NewPostgresStore(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	if err := ps.Migrate(ctx); err != nil {
		return nil, err
	}
	return ps, nil
}
