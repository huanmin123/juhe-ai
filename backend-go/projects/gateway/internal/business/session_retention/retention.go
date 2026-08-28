// Package sessionretention owns the Gateway primitive for the
// cleanup_expired_system_sessions transaction group.
//
// This package deliberately does not call businessauth. The existing auth
// lifecycle remains unchanged; this operation preserves the Node retention
// contract, including its strict expires_at < expiredBefore boundary.
package sessionretention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	ErrOwnerGate     = errors.New("Business SQLite owner handoff gate is not satisfied")
	ErrInvalidMode   = errors.New("session retention database mode is invalid")
	ErrInvalidSchema = errors.New("session retention PostgreSQL schema is invalid")
	ErrInvalidExpiry = errors.New("expiredBefore must be a valid RFC3339 timestamp")
)

type Mode string

const (
	SQLite   Mode = "sqlite"
	Postgres Mode = "postgres"
)

// OwnerGate is immutable handoff evidence. A partial handoff never permits a
// cleanup write, even when the database is reachable.
type OwnerGate struct {
	Confirmed         bool
	SchemaReady       bool
	NodeWriterStopped bool
}

func (g OwnerGate) Ready() bool {
	return g.Confirmed && g.SchemaReady && g.NodeWriterStopped
}

// Result is intentionally small: the Node operation returns only the number
// of rows changed, so callers cannot mistake an attempted count for a commit.
type Result struct {
	Deleted int64
}

// CleanupInput mirrors the db-service operation payload. An empty
// ExpiredBefore uses the runtime clock, matching Node's nowIso default.
type CleanupInput struct {
	ExpiredBefore string
	Limit         int
}

type Store struct {
	db     *sql.DB
	mode   Mode
	schema string
	gate   OwnerGate
	now    func() time.Time
}

var postgresIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func New(db *sql.DB, mode Mode, schema string, gate OwnerGate) (*Store, error) {
	if db == nil {
		return nil, errors.New("session retention database is required")
	}
	if mode != SQLite && mode != Postgres {
		return nil, ErrInvalidMode
	}
	schema = strings.TrimSpace(schema)
	if mode == Postgres {
		if schema == "" {
			schema = "juhe_business"
		}
		if !postgresIdentifier.MatchString(schema) {
			return nil, ErrInvalidSchema
		}
	}
	return &Store{db: db, mode: mode, schema: schema, gate: gate, now: time.Now}, nil
}

func (s *Store) requireOwner() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}

// CheckContract verifies the existing relation without creating or altering
// schema. It is safe to run before the owner gate is complete.
func (s *Store) CheckContract(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrOwnerGate
	}
	rows, err := s.db.QueryContext(ctx, s.bind("SELECT id,system_account_id,token_hash,expires_at,created_at,last_seen_at FROM "+s.table()+" LIMIT 0"))
	if err != nil {
		return fmt.Errorf("session retention contract: %w", err)
	}
	return rows.Close()
}

// Cleanup executes exactly one bounded cleanup transaction. Selection order
// and the strict cutoff match Node's data-retention repository:
// expires_at < expiredBefore, ordered by expires_at then rowid/ctid.
func (s *Store) Cleanup(ctx context.Context, input CleanupInput) (Result, error) {
	if err := s.requireOwner(); err != nil {
		return Result{}, err
	}
	limit := normalizeLimit(input.Limit)
	expiredBefore, err := s.expiry(input.ExpiredBefore)
	if err != nil {
		return Result{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("begin session retention: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.bind(s.deleteSQL()), expiredBefore, limit)
	if err != nil {
		return Result{}, fmt.Errorf("cleanup expired system sessions: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return Result{}, fmt.Errorf("read session cleanup count: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Result{}, fmt.Errorf("commit session retention: %w", err)
	}
	return Result{Deleted: deleted}, nil
}

// CleanupExpiredSystemSessions is the operation-shaped convenience method.
func (s *Store) CleanupExpiredSystemSessions(ctx context.Context, expiredBefore string, limit int) (int64, error) {
	result, err := s.Cleanup(ctx, CleanupInput{ExpiredBefore: expiredBefore, Limit: limit})
	if err != nil {
		return 0, err
	}
	return result.Deleted, nil
}

// CleanupExpiredSessions is retained as a concise Port-compatible alias.
func (s *Store) CleanupExpiredSessions(ctx context.Context, expiredBefore string, limit int) (int64, error) {
	return s.CleanupExpiredSystemSessions(ctx, expiredBefore, limit)
}

func normalizeLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	return limit
}

func (s *Store) expiry(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return s.now().UTC().Format(nodeTimeLayout), nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidExpiry, err)
	}
	if t.IsZero() {
		return "", ErrInvalidExpiry
	}
	// Preserve a caller-provided timestamp byte-for-byte after validation. Node
	// compares the stored ISO text directly; normalizing precision or timezone
	// here would silently change its lexical cutoff semantics.
	return raw, nil
}

const nodeTimeLayout = "2006-01-02T15:04:05.000Z"

func (s *Store) table() string {
	if s.mode == Postgres {
		return s.schema + ".system_sessions"
	}
	return "system_sessions"
}

func (s *Store) deleteSQL() string {
	if s.mode == Postgres {
		return "DELETE FROM " + s.table() + " WHERE ctid IN (SELECT ctid FROM " + s.table() + " WHERE expires_at < ? ORDER BY expires_at ASC, ctid ASC LIMIT ?)"
	}
	return "DELETE FROM system_sessions WHERE rowid IN (SELECT rowid FROM system_sessions WHERE expires_at < ? ORDER BY expires_at ASC, rowid ASC LIMIT ?)"
}

func (s *Store) bind(query string) string {
	if s.mode != Postgres {
		return query
	}
	var b strings.Builder
	position := 1
	for _, r := range query {
		if r == '?' {
			fmt.Fprintf(&b, "$%d", position)
			position++
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Port keeps future Gateway handlers from reaching through to raw SQL.
type Port interface {
	Cleanup(context.Context, CleanupInput) (Result, error)
	CleanupExpiredSystemSessions(context.Context, string, int) (int64, error)
}

var _ Port = (*Store)(nil)

// CoveredManifestOperations is evidence for this isolated port. It does not
// change manifest status or authorize a cutover by itself.
var CoveredManifestOperations = []string{"cleanup_expired_system_sessions"}
