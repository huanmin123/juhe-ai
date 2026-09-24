// Tests for the gateway SQLite startup storage preflight: a fresh six-path
// environment ends up ensured+seeded, the run is idempotent, and a missing
// auxiliary path fails fast with the Node-aligned Chinese contract.

package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
)

// seedActiveModelCatalogRowCount 在临时库上重放与
// ensureGatewaySQLiteStoragePreflight 完全相同的 ensure+seed（同一个
// bootstrap 出口、同一默认墙钟），返回种子自身的 ModelCatalogRows——
// 即按当前 UTC 日期过滤 shutdown 到期行后的活跃种子行数。expected 断言
// 与被测种子同源计算，种子快照演进或 shutdown 到期漂移时不再失真；
// maintenance 的 internal/schema 因 Go internal 可见性规则不可跨模块
// import，bootstrap.SQLiteSeedResult 是既有的受控同源出口。
func seedActiveModelCatalogRowCount(t *testing.T, secret string) int {
	t.Helper()
	db, err := bootstrap.OpenSQLiteFile(filepath.Join(t.TempDir(), "catalog-count-source.sqlite3"))
	if err != nil {
		t.Fatalf("open catalog count source db: %v", err)
	}
	defer db.Close()
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("ensure catalog count source schema: %v", err)
	}
	result, err := bootstrap.SeedSQLiteBusiness(context.Background(), db, bootstrap.SeedOptions{Secret: secret})
	if err != nil {
		t.Fatalf("seed catalog count source: %v", err)
	}
	return result.ModelCatalogRows
}

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
	// 行数与被测种子同源计算：期望值取自同一 bootstrap 种子在临时库上写出的
	// 活跃行数（按当前 UTC 日期过滤 shutdown 到期行），替换原硬编码 113——
	// 种子快照演进或 shutdown 到期漂移时断言不再失真。期望库与被测库的两次
	// 种子间隔若跨 UTC 午夜存在理论竞态，概率可忽略，不做防御。
	wantCatalogRows := seedActiveModelCatalogRowCount(t, cfg.Secret)
	if catalogRows != wantCatalogRows {
		t.Fatalf("model catalog rows = %d, want %d (同源活跃种子行数)", catalogRows, wantCatalogRows)
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

// TestZeroConfigStoragePreflightWithDerivedPaths 取代原 MissingPaths 用例
// （2026-09-19 起路径未配置按 datadir 固定名表派生，preflight 不再缺路径
// fail-fast）：空 env（DATA_DIR 指向临时目录）派生出的六库路径直接通过
// ensure+seed preflight，且幂等、文件落位正确。
func TestZeroConfigStoragePreflightWithDerivedPaths(t *testing.T) {
	root := t.TempDir()
	cfg, err := loadRuntimeConfig(w1iFakeEnv(map[string]string{"JUHE_AI_DATA_DIR": root}))
	if err != nil {
		t.Fatalf("空 env loadRuntimeConfig: %v", err)
	}
	businessDB, err := bootstrap.OpenSQLiteFile(cfg.DatabasePath)
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
	assertPreflightTables(t, filepath.Join(root, "stats.sqlite3"))
	assertPreflightTables(t, filepath.Join(root, "chat.sqlite3"))
	assertPreflightTables(t, filepath.Join(root, "dataset.sqlite3"))
	assertPreflightTables(t, filepath.Join(root, "usage-catalog.sqlite3"))
	assertPreflightTables(t, filepath.Join(root, "codex-context", "state-shards", "state-000.sqlite3"))
	assertPreflightTables(t, filepath.Join(root, "codex-context", "state-shards", "state-015.sqlite3"))
	var adminRows int
	if err := businessDB.QueryRow("SELECT count(*) FROM system_accounts WHERE id = 'sys_admin' AND username = 'admin' AND role = 'super_admin'").Scan(&adminRows); err != nil {
		t.Fatalf("query seeded admin: %v", err)
	}
	if adminRows != 1 {
		t.Fatalf("admin rows = %d, want 1", adminRows)
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
