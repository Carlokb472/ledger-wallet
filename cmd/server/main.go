// Command server runs the ledger-wallet HTTP API.
//
//	go run ./cmd/server          # listens on :8080
//	ADDR=:9000 go run ./cmd/server
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/Carlokb472/ledger-wallet/internal/api"
	"github.com/Carlokb472/ledger-wallet/internal/ledger"
)

func main() {
	l := ledger.New()
	// Bootstrap a funding "world" account so money can legitimately enter the
	// system (it is allowed to go negative; user accounts are not).
	if _, err := l.OpenAccount("world", "HKD", true); err != nil {
		log.Fatalf("bootstrap world account: %v", err)
	}

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	log.Printf("ledger-wallet listening on %s", addr)
	if err := http.ListenAndServe(addr, api.NewServer(l)); err != nil {
		log.Fatal(err)
	}
}
