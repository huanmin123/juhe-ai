package sessionretention

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:session-retention-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE system_sessions (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			token_hash TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL
		);
		CREATE INDEX idx_system_sessions_expires_at ON system_sessions(expires_at);
	`); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertSession(t *testing.T, db *sql.DB, id, expires string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO system_sessions
		(id,system_account_id,token_hash,expires_at,created_at,last_seen_at)
		VALUES (?,?,?,?,?,?)`, id, "account", id+"-hash", expires, expires, expires)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCleanupSQLiteStrictCutoffBoundedAndReplayable(t *testing.T) {
	db := openSQLite(t)
	store, err := New(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	cutoff := "2026-01-01T00:00:00.000Z"
	insertSession(t, db, "old-1", "2025-01-01T00:00:00.000Z")
	insertSession(t, db, "old-2", "2025-06-01T00:00:00.000Z")
	insertSession(t, db, "equal", cutoff)
	insertSession(t, db, "future", "2027-01-01T00:00:00.000Z")

	got, err := store.CleanupExpiredSystemSessions(context.Background(), cutoff, 1)
	if err != nil || got != 1 {
		t.Fatalf("first cleanup deleted=%d err=%v", got, err)
	}
	var id string
	if err := db.QueryRow("SELECT id FROM system_sessions WHERE rowid=(SELECT MIN(rowid) FROM system_sessions WHERE expires_at < ?)", cutoff).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != "old-2" {
		t.Fatalf("bounded cleanup selected wrong next row %q", id)
	}
	var equalCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM system_sessions WHERE id='equal'").Scan(&equalCount); err != nil {
		t.Fatal(err)
	}
	if equalCount != 1 {
		t.Fatal("strict cutoff deleted equal timestamp")
	}
	got, err = store.CleanupExpiredSystemSessions(context.Background(), cutoff, 10)
	if err != nil || got != 1 {
		t.Fatalf("replay cleanup deleted=%d err=%v", got, err)
	}
	got, err = store.CleanupExpiredSystemSessions(context.Background(), cutoff, 10)
	if err != nil || got != 0 {
		t.Fatalf("idempotent replay deleted=%d err=%v", got, err)
	}
}

func TestCleanupOwnerGateAndInputFailClosed(t *testing.T) {
	db := openSQLite(t)
	insertSession(t, db, "old", "2020-01-01T00:00:00.000Z")
	store, err := New(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(context.Background(), CleanupInput{ExpiredBefore: "2021-01-01T00:00:00.000Z", Limit: 1}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("partial owner gate err=%v", err)
	}
	store, err = New(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(context.Background(), CleanupInput{ExpiredBefore: "not-a-time", Limit: 1}); !errors.Is(err, ErrInvalidExpiry) {
		t.Fatalf("invalid expiry err=%v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM system_sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("failed cleanup changed rows")
	}
}

func TestPostgresSQLQualificationAndPlaceholders(t *testing.T) {
	db := openSQLite(t)
	store, err := New(db, Postgres, "juhe_business", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	q := store.bind(store.deleteSQL())
	for _, want := range []string{
		"DELETE FROM juhe_business.system_sessions",
		"SELECT ctid FROM juhe_business.system_sessions",
		"expires_at < $1",
		"LIMIT $2",
		"ORDER BY expires_at ASC, ctid ASC",
	} {
		if !strings.Contains(q, want) {
			t.Fatalf("postgres SQL missing %q: %s", want, q)
		}
	}
	if strings.Contains(q, "?") {
		t.Fatalf("postgres SQL retained question-mark placeholder: %s", q)
	}
}

func TestPostgresSchemaValidationAndContract(t *testing.T) {
	db := openSQLite(t)
	if _, err := New(db, Postgres, "bad.schema", OwnerGate{}); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("invalid schema err=%v", err)
	}
	if _, err := New(db, Mode("mysql"), "", OwnerGate{}); !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("invalid mode err=%v", err)
	}
	store, err := New(db, SQLite, "", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckContract(context.Background()); err != nil {
		t.Fatalf("sqlite contract err=%v", err)
	}
}

func TestExpiryUsesNodeMillisecondRepresentation(t *testing.T) {
	db := openSQLite(t)
	store, err := New(db, SQLite, "", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 123000000, time.FixedZone("CST", 8*60*60)) }
	got, err := store.expiry("")
	if err != nil || got != "2025-12-31T16:00:00.123Z" {
		t.Fatalf("default expiry=%q err=%v", got, err)
	}
}
