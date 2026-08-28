package groups

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

type testCipher struct{}

func (testCipher) Encrypt(_ context.Context, plaintext []byte) (string, error) {
	return "enc:" + string(plaintext), nil
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:groups-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL, is_default INTEGER NOT NULL DEFAULT 0, group_type TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, mode TEXT NOT NULL, status TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL, weight INTEGER NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, key_hash TEXT NOT NULL UNIQUE, key_prefix TEXT NOT NULL, key_suffix TEXT NOT NULL, key_secret_encrypted TEXT NOT NULL, status TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0, purpose TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestOwnerGateAndCrossOwnerFailClosed(t *testing.T) {
	db := testDB(t)
	blocked, err := New(db, modelcheckauth.SQLite, OwnerGate{Confirmed: true, SchemaReady: true}, nil, testCipher{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocked.CreateGroup(context.Background(), Actor{SystemAccountID: "sys-1"}, GroupInput{Name: "blocked", ProviderCode: "openai"}); err != ErrOwnerGate {
		t.Fatalf("owner gate error=%v", err)
	}
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM groups`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("blocked write count=%d err=%v", before, err)
	}
	svc, err := New(db, modelcheckauth.SQLite, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, nil, testCipher{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	g, err := svc.CreateGroup(ctx, Actor{SystemAccountID: "sys-1", Role: "user"}, GroupInput{Name: "g", ProviderCode: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateGroup(ctx, Actor{SystemAccountID: "sys-2", Role: "user"}, GroupInput{SystemAccountID: "sys-1", Name: "x", ProviderCode: "openai"}); err != ErrForbidden {
		t.Fatalf("cross owner error=%v", err)
	}
	if _, err := svc.UpdateGroup(ctx, Actor{SystemAccountID: "sys-1", Role: "user"}, g.ID, "stale", GroupInput{Name: "g2", ProviderCode: "openai"}); err != ErrRevisionConflict {
		t.Fatalf("revision error=%v", err)
	}
	listed, err := svc.ListGroups(ctx, Actor{SystemAccountID: "sys-1", Role: "user"}, "", 10)
	if err != nil || len(listed) != 1 || listed[0].ID != g.ID {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
}

func TestRouteStrategyBindingsAreAtomicAndScoped(t *testing.T) {
	db := testDB(t)
	svc, _ := New(db, modelcheckauth.SQLite, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, nil, testCipher{})
	ctx := context.Background()
	actor := Actor{SystemAccountID: "sys-1", Role: "admin"}
	g, err := svc.CreateGroup(ctx, actor, GroupInput{Name: "g", ProviderCode: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.CreateRouteStrategy(ctx, actor, RouteStrategyInput{Name: "r", Bindings: []RouteBinding{{GroupID: g.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_strategy_groups WHERE route_strategy_id=?`, r.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("bindings count=%d err=%v", count, err)
	}
	if _, err := svc.CreateRouteStrategy(ctx, actor, RouteStrategyInput{Name: "bad", Bindings: []RouteBinding{{GroupID: "missing"}}}); err == nil {
		t.Fatal("missing group must fail closed")
	}
}

func TestAPIKeySecretUsesCipherAndReferencesActiveRoute(t *testing.T) {
	db := testDB(t)
	svc, _ := New(db, modelcheckauth.SQLite, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, nil, testCipher{})
	ctx := context.Background()
	actor := Actor{SystemAccountID: "sys-1", Role: "admin"}
	g, _ := svc.CreateGroup(ctx, actor, GroupInput{Name: "g", ProviderCode: "openai"})
	r, _ := svc.CreateRouteStrategy(ctx, actor, RouteStrategyInput{Name: "r", Bindings: []RouteBinding{{GroupID: g.ID}}})
	k, secret, err := svc.CreateAPIKey(ctx, actor, APIKeyInput{RouteStrategyID: r.ID, Name: "k", Secret: "sk-test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if secret != "sk-test-secret" {
		t.Fatal("secret should be returned once")
	}
	var stored string
	if err := db.QueryRow(`SELECT key_secret_encrypted FROM api_keys WHERE id=?`, k.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "enc:") {
		t.Fatalf("secret not encrypted: %q", stored)
	}
	rotated, raw, err := svc.RotateAPIKeySecret(ctx, actor, k.ID, k.Revision, "sk-rotated-secret")
	if err != nil || raw != "sk-rotated-secret" || rotated.KeySuffix != "d-secret" {
		t.Fatalf("rotation=%+v raw=%q err=%v", rotated, raw, err)
	}
	if _, _, err := svc.RotateAPIKeySecret(ctx, actor, k.ID, k.Revision, "sk-stale"); err != ErrRevisionConflict {
		t.Fatalf("stale rotation error=%v", err)
	}
}

func TestPostgresBindingQualifiesTablesAndPlaceholders(t *testing.T) {
	s := &Service{mode: modelcheckauth.Postgres}
	q := s.bind(`SELECT id FROM route_strategies WHERE id=? AND system_account_id=?`)
	if !strings.Contains(q, "juhe_business.route_strategies") || !strings.Contains(q, "$1") || !strings.Contains(q, "$2") {
		t.Fatalf("query=%s", q)
	}
}
