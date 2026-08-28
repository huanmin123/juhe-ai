package systemaccounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type testCipher struct{}

func (testCipher) Encrypt(_ context.Context, plaintext []byte) (string, error) {
	if len(plaintext) == 0 {
		return "", errors.New("empty secret")
	}
	return "v1:test-ciphertext", nil
}

func testStore(t *testing.T, gate OwnerGate) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:system-accounts-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE providers (code TEXT PRIMARY KEY)`,
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, description TEXT, role TEXT NOT NULL, status TEXT NOT NULL, password_hash TEXT NOT NULL, must_change_password INTEGER NOT NULL, image_generation_enabled INTEGER NOT NULL, ai_account_limit INTEGER, request_limits_json TEXT, last_login_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL, FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, provider_code TEXT NOT NULL, description TEXT, enabled INTEGER NOT NULL, is_default INTEGER NOT NULL, group_type TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY (provider_code) REFERENCES providers(code), FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE)`,
		`CREATE TABLE route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, mode TEXT NOT NULL, status TEXT NOT NULL, is_default INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE)`,
		`CREATE TABLE route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL, weight INTEGER NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY (route_strategy_id) REFERENCES route_strategies(id) ON DELETE CASCADE, FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE, FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE)`,
		`CREATE TABLE api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT, key_hash TEXT NOT NULL UNIQUE, key_prefix TEXT NOT NULL, key_suffix TEXT NOT NULL, key_secret_encrypted TEXT NOT NULL, status TEXT NOT NULL, is_default INTEGER NOT NULL, purpose TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, FOREIGN KEY (system_account_id) REFERENCES system_accounts(id) ON DELETE CASCADE, FOREIGN KEY (route_strategy_id) REFERENCES route_strategies(id) ON DELETE CASCADE)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	for _, provider := range []string{"openai", "gpt", "xai", "deepseek", "anthropic", "gemini", "glm", "hybrid"} {
		if _, err := db.Exec(`INSERT INTO providers(code) VALUES (?)`, provider); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewStore(db, SQLite, "", gate, testCipher{})
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }
	return store, db
}

func TestOwnerGateAndContractFailClosed(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true})
	if _, err := store.List(context.Background(), ListOptions{}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("list owner gate error=%v", err)
	}
	if err := store.CheckContract(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE api_keys`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckContract(context.Background()); !errors.Is(err, ErrContract) {
		t.Fatalf("contract error=%v", err)
	}
}

func TestCreateAtomicDefaultFanoutAndNoSecretOutput(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	account, err := store.Create(ctx, CreateInput{ID: "sys-1", Username: "alice", DisplayName: "Alice", PasswordHash: "pbkdf2$hash", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if account.ID != "sys-1" || account.MustChangePassword != true || account.Role != "user" {
		t.Fatalf("account=%+v", account)
	}
	if strings.Contains(account.DisplayName, "pbkdf2") {
		t.Fatal("unexpected secret-like output")
	}
	encoded, err := json.Marshal(account)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"password_hash", "passwordHash", "key_secret_encrypted", "sk-"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("secret field leaked in account response: %s", forbidden)
		}
	}
	var groups, routes, bindings, keys int
	if err := db.QueryRow(`SELECT COUNT(*) FROM groups WHERE system_account_id='sys-1'`).Scan(&groups); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_strategies WHERE system_account_id='sys-1'`).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_strategy_groups WHERE system_account_id='sys-1'`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE system_account_id='sys-1'`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if groups != 8 || routes != 7 || bindings != 7 || keys != 8 {
		t.Fatalf("fanout groups=%d routes=%d bindings=%d keys=%d", groups, routes, bindings, keys)
	}
	list, err := store.List(ctx, ListOptions{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	listJSON, err := json.Marshal(list.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(listJSON), "createdAt") || strings.Contains(string(listJSON), "updatedAt") {
		t.Fatalf("list item leaked full summary timestamps: %s", listJSON)
	}
	var encrypted string
	if err := db.QueryRow(`SELECT key_secret_encrypted FROM api_keys WHERE system_account_id='sys-1' LIMIT 1`).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted == "" || strings.Contains(encrypted, "sk-") {
		t.Fatalf("invalid persisted key secret=%q", encrypted)
	}
	var groupDescription sql.NullString
	if err := db.QueryRow(`SELECT description FROM groups WHERE system_account_id='sys-1' AND provider_code='openai'`).Scan(&groupDescription); err != nil {
		t.Fatal(err)
	}
	if !groupDescription.Valid || groupDescription.String != "" {
		t.Fatalf("default group description=%v", groupDescription)
	}
}

func TestCreateRollsBackFanoutWhenCipherMissing(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	store.cipher = nil
	if _, err := store.Create(context.Background(), CreateInput{ID: "sys-1", Username: "alice", DisplayName: "Alice", PasswordHash: "hash"}); !errors.Is(err, ErrSecretCipher) {
		t.Fatalf("cipher error=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM system_accounts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("account was written without cipher: %d", count)
	}
}

func TestCreateRollsBackWhenDefaultChildFails(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if _, err := db.Exec(`CREATE TRIGGER fail_default_api_key BEFORE INSERT ON api_keys BEGIN SELECT RAISE(ABORT, 'default api key failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), CreateInput{ID: "sys-1", Username: "alice", DisplayName: "Alice", PasswordHash: "hash"}); err == nil {
		t.Fatal("expected child resource failure")
	}
	for _, table := range []string{"system_accounts", "groups", "route_strategies", "route_strategy_groups", "api_keys"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("transaction leaked %s rows: %d", table, count)
		}
	}
}

func TestCreateRejectsSuperAdminRole(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	_, err := store.Create(context.Background(), CreateInput{
		ID:           "sys-super-admin",
		Username:     "root2",
		DisplayName:  "Root 2",
		PasswordHash: "hash",
		Role:         "super_admin",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("super_admin create error=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM system_accounts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("super_admin create wrote account: %d", count)
	}
}

func TestCreateRejectsShortUsername(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	_, err := store.Create(context.Background(), CreateInput{ID: "sys-1", Username: "a", DisplayName: "Alice", PasswordHash: "hash"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("short username error=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM system_accounts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("short username wrote account: %d", count)
	}
}

func TestRequestLimitsCanonicalOrderMatchesNode(t *testing.T) {
	value := `{"expiresOn":"2026-09-01","perMonth":4,"perMinute":1}`
	normalized, err := normalizeJSONPointer(&value)
	if err != nil {
		t.Fatal(err)
	}
	if normalized == nil || *normalized != `{"perMinute":1,"perMonth":4,"expiresOn":"2026-09-01"}` {
		t.Fatalf("normalized request limits=%v", normalized)
	}
}

func TestListOptionsAndPatchCASRevokeSessions(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	account, err := store.Create(ctx, CreateInput{ID: "sys-1", Username: "alice", DisplayName: "Alice", PasswordHash: "hash", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_sessions(id,system_account_id,token_hash,expires_at,created_at,last_seen_at) VALUES ('s1','sys-1','h1','2099-01-01','2026-01-01','2026-01-01'),('s2','sys-1','h2','2099-01-01','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx, ListOptions{PageSize: 1})
	if err != nil || len(list.Items) != 1 || list.HasMore || list.Total != 1 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	options, err := store.Options(ctx, OptionListOptions{IDs: []string{"sys-1"}})
	if err != nil || len(options) != 1 || options[0].Name != "Alice" {
		t.Fatalf("options=%+v err=%v", options, err)
	}
	name := "Alice2"
	password := "hash-2"
	patched, err := store.PatchCAS(ctx, account.ID, Patch{ExpectedUpdatedAt: account.UpdatedAt, DisplayName: &name, PasswordHash: &password})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Account.DisplayName != name || patched.RevokedSessionCount != 2 || len(patched.Changes) != 2 {
		t.Fatalf("patched=%+v", patched)
	}
	if _, err := store.PatchCAS(ctx, account.ID, Patch{ExpectedUpdatedAt: account.UpdatedAt, Status: stringPtr("disabled")}); !errors.Is(err, ErrCAS) {
		t.Fatalf("stale patch=%v", err)
	}
}

func TestPatchRejectsEmptyOrWhitespaceStatus(t *testing.T) {
	store, _ := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	account, err := store.Create(context.Background(), CreateInput{ID: "sys-1", Username: "alice", DisplayName: "Alice", PasswordHash: "hash"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchCAS(context.Background(), account.ID, Patch{ExpectedUpdatedAt: account.UpdatedAt}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty patch error=%v", err)
	}
	status := " disabled "
	if _, err := store.PatchCAS(context.Background(), account.ID, Patch{ExpectedUpdatedAt: account.UpdatedAt, Status: &status}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("whitespace status error=%v", err)
	}
	role := ""
	if _, err := store.PatchCAS(context.Background(), account.ID, Patch{ExpectedUpdatedAt: account.UpdatedAt, Role: &role}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty role error=%v", err)
	}
}

func TestLastActiveSuperAdminInvariant(t *testing.T) {
	store, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	updatedAt := "2026-08-28T12:00:00Z"
	if _, err := db.Exec(`INSERT INTO system_accounts(id,username,display_name,role,status,password_hash,must_change_password,image_generation_enabled,created_at,updated_at) VALUES ('sys-admin','admin','Admin','super_admin','active','hash',0,0,?,?)`, updatedAt, updatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PatchCAS(ctx, "sys-admin", Patch{ExpectedUpdatedAt: updatedAt, Status: stringPtr("disabled")}); !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("invariant error=%v", err)
	}
}

func TestPostgresQualificationAndPlaceholder(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db, Postgres, "juhe_business", OwnerGate{}, testCipher{})
	if err != nil {
		t.Fatal(err)
	}
	query := store.bind(`SELECT id FROM ` + store.table("system_accounts") + ` WHERE id=? AND status=? LIMIT ?`)
	for _, want := range []string{"juhe_business.system_accounts", "id=$1", "status=$2", "LIMIT $3"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q: %s", want, query)
		}
	}
	if strings.Contains(query, "?") {
		t.Fatalf("question mark retained: %s", query)
	}
	if _, err := NewStore(db, Postgres, "bad.schema", OwnerGate{}, testCipher{}); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("invalid schema=%v", err)
	}
}

func stringPtr(value string) *string { return &value }
