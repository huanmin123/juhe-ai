// Tests for the gateway SQLite startup storage preflight: a fresh six-path
// environment ends up ensured+seeded, the run is idempotent, and a missing
// auxiliary path fails fast with the Node-aligned Chinese contract.

package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
)

func gatewayPreflightTestConfig(t *testing.T) (runtimeConfig, string) {
	t.Helper()
	root := t.TempDir()
	cfg := runtimeConfig{
		Secret:                   "juhe-ai-seed-test-secret",
		DatabasePath:             filepath.Join(root, "business.sqlite3"),
		ChatDatabasePath:         filepath.Join(root, "chat.sqlite3"),
		DatasetDatabasePath:      filepath.Join(root, "dataset.sqlite3"),
		UsageCatalogDatabasePath: filepath.Join(root, "usage-catalog.sqlite3"),
		StatsDatabasePath:        filepath.Join(root, "stats.sqlite3"),
		CodexContextShardRoot:    filepath.Join(root, "shards"),
		CodexContextShardCount:   2,
		BusinessDatabasePath:     filepath.Join(root, "business.sqlite3"),
	}
	return cfg, root
}

func TestEnsureGatewaySQLiteStoragePreflight(t *testing.T) {
	cfg, root := gatewayPreflightTestConfig(t)
	businessDB, err := bootstrap.OpenSQLiteFile(cfg.BusinessDatabasePath)
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	defer businessDB.Close()

	if err := ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, businessDB); err != nil {
		t.Fatalf("first preflight: %v", err)
	}
	if err := ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, businessDB); err != nil {
		t.Fatalf("second preflight (idempotency): %v", err)
	}

	var adminRows int
	if err := businessDB.QueryRow("SELECT count(*) FROM system_accounts WHERE id = 'sys_admin' AND username = 'admin' AND role = 'super_admin'").Scan(&adminRows); err != nil {
		t.Fatalf("query seeded admin: %v", err)
	}
	if adminRows != 1 {
		t.Fatalf("admin rows = %d, want 1", adminRows)
	}
	var catalogRows int
	if err := businessDB.QueryRow("SELECT count(*) FROM provider_model_catalog").Scan(&catalogRows); err != nil {
		t.Fatalf("query model catalog: %v", err)
	}
	if catalogRows != 106 {
		t.Fatalf("model catalog rows = %d, want 106", catalogRows)
	}
	var apiKeys int
	if err := businessDB.QueryRow("SELECT count(*) FROM api_keys").Scan(&apiKeys); err != nil {
		t.Fatalf("query api keys: %v", err)
	}
	if apiKeys != 8 {
		t.Fatalf("api keys = %d, want 8 (7 default + 1 chat)", apiKeys)
	}
	var defaultGroups int
	if err := businessDB.QueryRow("SELECT count(*) FROM groups WHERE is_default = 1 AND system_account_id = 'sys_admin'").Scan(&defaultGroups); err != nil {
		t.Fatalf("query groups: %v", err)
	}
	if defaultGroups != 8 {
		t.Fatalf("default groups = %d, want 8", defaultGroups)
	}

	assertPreflightTables(t, filepath.Join(root, "stats.sqlite3"))
	assertPreflightTables(t, filepath.Join(root, "chat.sqlite3"))
	assertPreflightTables(t, filepath.Join(root, "dataset.sqlite3"))
	assertPreflightTables(t, filepath.Join(root, "usage-catalog.sqlite3"))
	assertPreflightTables(t, filepath.Join(root, "shards", "state-000.sqlite3"))
	assertPreflightTables(t, filepath.Join(root, "shards", "state-001.sqlite3"))
}

func TestEnsureGatewaySQLiteStoragePreflightMissingPaths(t *testing.T) {
	cfg, root := gatewayPreflightTestConfig(t)
	cfg.ChatDatabasePath = ""
	businessDB, err := bootstrap.OpenSQLiteFile(filepath.Join(root, "business.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer businessDB.Close()
	err = ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, businessDB)
	if err == nil {
		t.Fatal("preflight with missing chat path should fail")
	}
	if got := err.Error(); !strings.Contains(got, "JUHE_AI_CHAT_DATABASE_PATH") {
		t.Fatalf("missing path error = %q", got)
	}
}

func assertPreflightTables(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&tables); err != nil {
		t.Fatalf("query %s tables: %v", path, err)
	}
	if tables == 0 {
		t.Fatalf("%s has no tables after the preflight", path)
	}
}

// TestSeedAdminPasswordVerifiesThroughModelcheckauth is the seed-interop
// regression (X05 defect: hashSeedPassword derived PBKDF2 from the raw salt
// bytes while Node crypto.ts and the Go gateway verifyNodePBKDF2Password both
// derive from the base64url salt TEXT bytes, so fresh-seed admins could not
// log in on either runtime). The seed here runs through the real maintenance
// bootstrap surface and the verification through the exact gateway login
// path (modelcheckauth.Authenticator.Login).
func TestSeedAdminPasswordVerifiesThroughModelcheckauth(t *testing.T) {
	cfg, _ := gatewayPreflightTestConfig(t)
	businessDB, err := bootstrap.OpenSQLiteFile(cfg.BusinessDatabasePath)
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	defer businessDB.Close()
	if err := ensureGatewaySQLiteStoragePreflight(context.Background(), cfg, businessDB); err != nil {
		t.Fatalf("ensure+seed business db: %v", err)
	}

	authenticator, err := modelcheckauth.New(businessDB, modelcheckauth.SQLite, time.Now)
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	_, verified, ok, err := authenticator.Login(context.Background(), "admin", "admin", 1)
	if err != nil {
		t.Fatalf("seed admin login: %v", err)
	}
	if !ok {
		t.Fatal("seed admin login rejected: seed hash does not verify under the gateway Node-compatible PBKDF2 semantics")
	}
	if verified.SystemAccountID != "sys_admin" || verified.Role != "super_admin" {
		t.Fatalf("seed admin identity wrong: %#v", verified)
	}
}
