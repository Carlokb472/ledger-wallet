package ledger

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/0001_init.sql
var schemaSQL string

// PostgresStore is a durable Store backed by Postgres. Unlike MemStore it
// survives restarts and is safe across multiple server processes, because
// atomicity comes from the database (UNIQUE constraints + row locks) rather than
// an in-process mutex.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore connects (and pings) using a standard libpq connection
// string, e.g. "postgres://user:pass@localhost:5432/ledger".
func NewPostgresStore(ctx context.Context, url string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// Migrate applies the schema. It is idempotent (CREATE TABLE IF NOT EXISTS).
func (s *PostgresStore) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, schemaSQL)
	return err
}

// Close releases the connection pool.
func (s *PostgresStore) Close() { s.pool.Close() }

func (s *PostgresStore) OpenAccount(ctx context.Context, id, currency string, allowNegative bool) (Account, error) {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO accounts (id, currency, allow_negative) VALUES ($1, $2, $3)`,
		id, currency, allowNegative)
	if err != nil {
		if isUniqueViolation(err) {
			return Account{}, fmt.Errorf("%w: %s", ErrAccountExists, id)
		}
		return Account{}, err
	}
	return Account{ID: id, Currency: currency, AllowNegative: allowNegative}, nil
}

func (s *PostgresStore) GetAccount(ctx context.Context, id string) (Account, bool, error) {
	var a Account
	err := s.pool.QueryRow(ctx,
		`SELECT id, currency, allow_negative FROM accounts WHERE id = $1`, id).
		Scan(&a.ID, &a.Currency, &a.AllowNegative)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	return a, true, nil
}

func (s *PostgresStore) Balance(ctx context.Context, id string) (int64, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM accounts WHERE id = $1)`, id).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, fmt.Errorf("%w: %s", ErrAccountNotFound, id)
	}
	var bal int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM postings WHERE account_id = $1`, id).Scan(&bal); err != nil {
		return 0, err
	}
	return bal, nil
}

// Post runs the whole operation inside one SQL transaction so it is atomic.
//
// The idempotency claim is the interesting part: we INSERT the key with
// ON CONFLICT DO NOTHING ... RETURNING. If we get a row back we won the race and
// own this transaction; if not, the key already exists, so we load the original
// and either replay it (same postings) or reject as a conflict. Two concurrent
// requests with the same key block on the UNIQUE index, so exactly one ever
// posts — no in-process locking required.
func (s *PostgresStore) Post(ctx context.Context, idempotencyKey string, postings []Posting) (Transaction, error) {
	if idempotencyKey == "" {
		return Transaction{}, ErrEmptyIdempotencyKey
	}
	if len(postings) < 2 {
		return Transaction{}, ErrTooFewPostings
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Transaction{}, err
	}
	defer tx.Rollback(ctx) // no-op after a successful Commit

	// 1. Atomically claim the idempotency key.
	var seq int64
	err = tx.QueryRow(ctx,
		`INSERT INTO transactions (idempotency_key) VALUES ($1)
		 ON CONFLICT (idempotency_key) DO NOTHING
		 RETURNING seq`, idempotencyKey).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		// Key already used -> replay the original or reject as a conflict.
		existing, lerr := loadTxByKey(ctx, tx, idempotencyKey)
		if lerr != nil {
			return Transaction{}, lerr
		}
		if !samePostings(existing.Postings, postings) {
			return Transaction{}, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return Transaction{}, err
		}
		return existing, nil
	} else if err != nil {
		return Transaction{}, err
	}

	// 2. Lock the involved accounts (sorted, to avoid deadlock) and validate.
	accs := make(map[string]Account)
	for _, id := range uniqueSortedIDs(postings) {
		var a Account
		err := tx.QueryRow(ctx,
			`SELECT id, currency, allow_negative FROM accounts WHERE id = $1 FOR UPDATE`, id).
			Scan(&a.ID, &a.Currency, &a.AllowNegative)
		if errors.Is(err, pgx.ErrNoRows) {
			return Transaction{}, fmt.Errorf("%w: %s", ErrAccountNotFound, id)
		} else if err != nil {
			return Transaction{}, err
		}
		accs[id] = a
	}

	var sum int64
	currency := ""
	for _, p := range postings {
		a := accs[p.AccountID]
		if currency == "" {
			currency = a.Currency
		} else if a.Currency != currency {
			return Transaction{}, ErrCurrencyMismatch
		}
		sum += p.Amount
	}
	if sum != 0 {
		return Transaction{}, ErrUnbalanced
	}

	// 3. Overdraft check against the locked balances.
	deltas := make(map[string]int64)
	for _, p := range postings {
		deltas[p.AccountID] += p.Amount
	}
	for id, d := range deltas {
		if d >= 0 || accs[id].AllowNegative {
			continue
		}
		var bal int64
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(SUM(amount), 0) FROM postings WHERE account_id = $1`, id).Scan(&bal); err != nil {
			return Transaction{}, err
		}
		if bal+d < 0 {
			return Transaction{}, fmt.Errorf("%w: %s", ErrInsufficientFunds, id)
		}
	}

	// 4. Append the postings and commit.
	for _, p := range postings {
		if _, err := tx.Exec(ctx,
			`INSERT INTO postings (tx_seq, account_id, amount) VALUES ($1, $2, $3)`,
			seq, p.AccountID, p.Amount); err != nil {
			return Transaction{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Transaction{}, err
	}
	return Transaction{
		ID:             fmt.Sprintf("tx_%d", seq),
		IdempotencyKey: idempotencyKey,
		Postings:       append([]Posting(nil), postings...),
		Seq:            seq,
	}, nil
}

func (s *PostgresStore) Transactions(ctx context.Context) ([]Transaction, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT seq, idempotency_key FROM transactions ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	out := make([]Transaction, 0)
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(&t.Seq, &t.IdempotencyKey); err != nil {
			rows.Close()
			return nil, err
		}
		t.ID = fmt.Sprintf("tx_%d", t.Seq)
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ps, err := loadPostings(ctx, s.pool, out[i].Seq)
		if err != nil {
			return nil, err
		}
		out[i].Postings = ps
	}
	return out, nil
}

// rowQuerier is satisfied by both *pgxpool.Pool and pgx.Tx, so the load helpers
// work inside or outside a transaction.
type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func loadPostings(ctx context.Context, q rowQuerier, seq int64) ([]Posting, error) {
	rows, err := q.Query(ctx,
		`SELECT account_id, amount FROM postings WHERE tx_seq = $1 ORDER BY id`, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ps := make([]Posting, 0)
	for rows.Next() {
		var p Posting
		if err := rows.Scan(&p.AccountID, &p.Amount); err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

func loadTxByKey(ctx context.Context, tx pgx.Tx, key string) (Transaction, error) {
	var seq int64
	if err := tx.QueryRow(ctx,
		`SELECT seq FROM transactions WHERE idempotency_key = $1`, key).Scan(&seq); err != nil {
		return Transaction{}, err
	}
	ps, err := loadPostings(ctx, tx, seq)
	if err != nil {
		return Transaction{}, err
	}
	return Transaction{
		ID:             fmt.Sprintf("tx_%d", seq),
		IdempotencyKey: key,
		Postings:       ps,
		Seq:            seq,
	}, nil
}

func uniqueSortedIDs(postings []Posting) []string {
	set := make(map[string]struct{}, len(postings))
	for _, p := range postings {
		set[p.AccountID] = struct{}{}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
