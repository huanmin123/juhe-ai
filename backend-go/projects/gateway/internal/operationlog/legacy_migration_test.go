package operationlog

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestMigrateLegacySQLiteRequiresOfflineGates(t *testing.T) {
	_, err := MigrateLegacySQLite(context.Background(), Config{Mode: ModeSQLite}, LegacyMigrationOptions{})
	if err == nil || !strings.Contains(err.Error(), "停机") {
		t.Fatalf("expected stop gate error, got %v", err)
	}
	_, err = MigrateLegacySQLite(context.Background(), Config{Mode: ModeSQLite}, LegacyMigrationOptions{NodeStopped: true, GoStopped: true})
	if err == nil || !strings.Contains(err.Error(), "备份") {
		t.Fatalf("expected backup gate error, got %v", err)
	}
}

func TestMigrateLegacySQLiteCopiesNodeRowsWithoutMutatingSource(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "node-dataset.sqlite3")
	targetPath := filepath.Join(root, "f4-operation.sqlite3")
	businessPath := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, businessPath, "365")
	createLegacyNodeOperationLogSQLite(t, sourcePath)
	cfg := Config{Mode: ModeSQLite, InstanceID: "f4-legacy-test", DatabasePath: targetPath, BusinessSettingsPath: businessPath, OwnerLease: time.Minute}
	options := LegacyMigrationOptions{SourceDatabasePath: sourcePath, NodeStopped: true, GoStopped: true, BackupConfirmed: true}
	result, err := MigrateLegacySQLite(context.Background(), cfg, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.NoOp || result.MigratedOperationLogs != 1 || result.SourceCounts["operation_log_targets"] != 1 || result.TargetCounts["operation_log_viewers"] != 1 || !result.SearchTermsRebuilt {
		t.Fatalf("unexpected first migration result: %+v", result)
	}
	source, err := sql.Open("sqlite", "file:"+filepath.ToSlash(sourcePath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var sourceCount int
	if err := source.QueryRow(`SELECT COUNT(*) FROM operation_logs`).Scan(&sourceCount); err != nil || sourceCount != 1 {
		t.Fatalf("source must remain unchanged: count=%d err=%v", sourceCount, err)
	}
	target, err := sql.Open("sqlite", "file:"+filepath.ToSlash(targetPath))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	var createdAt, summary string
	if err := target.QueryRow(`SELECT created_at,summary FROM operation_logs WHERE id='oplog-legacy-1'`).Scan(&createdAt, &summary); err != nil || createdAt != "2026-08-13T00:00:00.100000000Z" || summary != "legacy operation summary" {
		t.Fatalf("target projection=%q/%q err=%v", createdAt, summary, err)
	}
	for _, table := range []string{"operation_logs", "operation_log_targets", "operation_log_viewers"} {
		var count int
		if err := target.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("target %s count=%d err=%v", table, count, err)
		}
	}
	var targetID string
	if err := target.QueryRow(`SELECT id FROM operation_log_targets WHERE operation_log_id='oplog-legacy-1'`).Scan(&targetID); err != nil || targetID != "optgt-legacy-1" {
		t.Fatalf("history copy must retain original target ID: id=%q err=%v", targetID, err)
	}
	repeated, err := MigrateLegacySQLite(context.Background(), cfg, options)
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.NoOp || repeated.MigratedOperationLogs != 0 {
		t.Fatalf("repeat must be idempotent: %+v", repeated)
	}
	var searchTerms int
	if err := target.QueryRow(`SELECT COUNT(*) FROM operation_log_summary_search_terms`).Scan(&searchTerms); err != nil || searchTerms == 0 {
		t.Fatalf("target search terms must be rebuilt: count=%d err=%v", searchTerms, err)
	}
}

func TestMigrateLegacySQLiteRejectsConflictingExistingID(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "node-dataset.sqlite3")
	targetPath := filepath.Join(root, "f4-operation.sqlite3")
	businessPath := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, businessPath, "365")
	createLegacyNodeOperationLogSQLite(t, sourcePath)
	cfg := Config{Mode: ModeSQLite, InstanceID: "f4-conflict-test", DatabasePath: targetPath, BusinessSettingsPath: businessPath, OwnerLease: time.Minute}
	target, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := target.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := target.AcquireOwnerLease(context.Background(), cfg.InstanceID, time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	_, err = target.Persist(context.Background(), lease, Input{ID: "oplog-legacy-1", ActorSystemAccountID: "actor", ActorRole: "user", Mode: "self", Module: "accounts", Action: "update", OperationKey: "accounts.update", ResourceType: "account", Summary: "conflicting summary", CreatedAt: "2026-08-13T00:00:00.100000000Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.ReleaseOwnerLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	_, err = MigrateLegacySQLite(context.Background(), cfg, LegacyMigrationOptions{SourceDatabasePath: sourcePath, NodeStopped: true, GoStopped: true, BackupConfirmed: true})
	if err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("conflicting stable ID must fail closed: %v", err)
	}
}

func TestMigrateLegacySQLiteRejectsModifiedExistingFacts(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "node-dataset.sqlite3")
	targetPath := filepath.Join(root, "f4-operation.sqlite3")
	businessPath := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, businessPath, "365")
	createLegacyNodeOperationLogSQLite(t, sourcePath)
	cfg := Config{Mode: ModeSQLite, InstanceID: "f4-existing-facts-test", DatabasePath: targetPath, BusinessSettingsPath: businessPath, OwnerLease: time.Minute}
	options := LegacyMigrationOptions{SourceDatabasePath: sourcePath, NodeStopped: true, GoStopped: true, BackupConfirmed: true}
	if _, err := MigrateLegacySQLite(context.Background(), cfg, options); err != nil {
		t.Fatal(err)
	}
	target, err := sql.Open("sqlite", "file:"+filepath.ToSlash(targetPath))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if _, err := target.Exec(`UPDATE operation_logs SET changes_json='[]' WHERE id='oplog-legacy-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacySQLite(context.Background(), cfg, options); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("modified raw JSON must fail closed: %v", err)
	}
	if _, err := target.Exec(`UPDATE operation_logs SET changes_json='[{"field":"name","label":"Name","before":"old","after":"new"}]' WHERE id='oplog-legacy-1'; UPDATE operation_log_targets SET target_name='conflicting target' WHERE operation_log_id='oplog-legacy-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateLegacySQLite(context.Background(), cfg, options); err == nil || !strings.Contains(err.Error(), "target 或 viewer") {
		t.Fatalf("modified target/viewer facts must fail closed: %v", err)
	}
}

func TestMigrateLegacySQLiteRepeatsEmptyTargetsAndViewers(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "node-dataset.sqlite3")
	targetPath := filepath.Join(root, "f4-operation.sqlite3")
	businessPath := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, businessPath, "365")
	createLegacyNodeOperationLogSQLite(t, sourcePath)
	source, err := sql.Open("sqlite", "file:"+filepath.ToSlash(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Exec(`INSERT INTO operation_logs (id,actor_system_account_id,actor_role,mode,module,action,operation_key,resource_type,summary,detail_level,visibility_scope,changes_json,metadata_json,created_at) VALUES ('oplog-empty-children','actor','user','self','settings','update','settings.update','setting','no children','summary','all_users','[]','{}','2026-08-13T08:00:01.100000000+08:00')`)
	_ = source.Close()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Mode: ModeSQLite, InstanceID: "f4-empty-children-test", DatabasePath: targetPath, BusinessSettingsPath: businessPath, OwnerLease: time.Minute}
	options := LegacyMigrationOptions{SourceDatabasePath: sourcePath, NodeStopped: true, GoStopped: true, BackupConfirmed: true}
	if _, err := MigrateLegacySQLite(context.Background(), cfg, options); err != nil {
		t.Fatal(err)
	}
	repeated, err := MigrateLegacySQLite(context.Background(), cfg, options)
	if err != nil || !repeated.NoOp || repeated.MigratedOperationLogs != 0 {
		t.Fatalf("empty target/viewer history must repeat cleanly: result=%+v err=%v", repeated, err)
	}
}

func TestMigrateLegacySQLiteRejectsSameFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "operation.sqlite3")
	createBusinessSettings(t, filepath.Join(root, "business.sqlite3"), "365")
	_, err := MigrateLegacySQLite(context.Background(), Config{Mode: ModeSQLite, InstanceID: "same-file", DatabasePath: path, BusinessSettingsPath: filepath.Join(root, "business.sqlite3"), OwnerLease: time.Minute}, LegacyMigrationOptions{SourceDatabasePath: path, NodeStopped: true, GoStopped: true, BackupConfirmed: true})
	if err == nil || !strings.Contains(err.Error(), "同一") {
		t.Fatalf("source and target same file must be rejected: %v", err)
	}
}

func TestLegacyLeaseRenewerKeepsSQLiteMigrationLeaseAndStops(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	createBusinessSettings(t, businessPath, "365")
	store, err := OpenStore(Config{Mode: ModeSQLite, InstanceID: "legacy-renewer", DatabasePath: filepath.Join(root, "operation.sqlite3"), BusinessSettingsPath: businessPath})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	// LoadConfig rejects leases below five seconds. Keep the test on that
	// supported boundary instead of coupling migration safety to scheduler-level
	// millisecond timing.
	leaseDuration := 5 * time.Second
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "legacy-renewer", leaseDuration)
	if err != nil || !ok {
		t.Fatalf("acquire lease: ok=%v err=%v", ok, err)
	}
	renewer := startLegacyLeaseRenewer(context.Background(), store, lease, leaseDuration)
	time.Sleep(leaseDuration + time.Second)
	if err := renewer.Err(); err != nil {
		t.Fatalf("lease renewer failed before migration completes: %v", err)
	}
	if _, err := store.Persist(context.Background(), lease, Input{ID: "oplog-renewed", ActorSystemAccountID: "actor", ActorRole: "admin", Module: "migration", Action: "copy", OperationKey: "migration.copy", ResourceType: "operation_log", Summary: "lease remains current", CreatedAt: storageTime(time.Now())}); err != nil {
		t.Fatalf("renewed lease must remain usable: %v", err)
	}
	if err := renewer.Stop(); err != nil {
		t.Fatalf("stopping a healthy renewer: %v", err)
	}
	time.Sleep(leaseDuration + time.Second)
	if _, err := store.Persist(context.Background(), lease, Input{ID: "oplog-expired", ActorSystemAccountID: "actor", ActorRole: "admin", Module: "migration", Action: "copy", OperationKey: "migration.copy", ResourceType: "operation_log", Summary: "lease must expire after renewal stops", CreatedAt: storageTime(time.Now())}); err != ErrOwnerLeaseLost {
		t.Fatalf("stopped renewer must not keep lease alive: %v", err)
	}
}

func createLegacyNodeOperationLogSQLite(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE operation_logs (id TEXT PRIMARY KEY,trace_id TEXT,actor_system_account_id TEXT NOT NULL,actor_username TEXT,actor_display_name TEXT,actor_role TEXT NOT NULL,operation_scope_system_account_id TEXT,mode TEXT NOT NULL,module TEXT NOT NULL,action TEXT NOT NULL,operation_key TEXT NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT,resource_name TEXT,summary TEXT NOT NULL,detail_level TEXT NOT NULL,visibility_scope TEXT NOT NULL,changes_json TEXT NOT NULL,metadata_json TEXT NOT NULL,method TEXT,path TEXT,status_code INTEGER,client_ip TEXT,user_agent TEXT,created_at TEXT NOT NULL);
CREATE TABLE operation_log_targets (id TEXT PRIMARY KEY,operation_log_id TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT,target_name TEXT,target_owner_system_account_id TEXT,relation TEXT NOT NULL,created_at TEXT NOT NULL,FOREIGN KEY(operation_log_id) REFERENCES operation_logs(id) ON DELETE CASCADE);
CREATE TABLE operation_log_viewers (operation_log_id TEXT NOT NULL,system_account_id TEXT NOT NULL,visibility_reason TEXT NOT NULL,detail_level TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(operation_log_id,system_account_id,visibility_reason),FOREIGN KEY(operation_log_id) REFERENCES operation_logs(id) ON DELETE CASCADE);
CREATE TABLE operation_log_summary_search_terms (operation_log_id TEXT NOT NULL,term TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(term,operation_log_id),FOREIGN KEY(operation_log_id) REFERENCES operation_logs(id) ON DELETE CASCADE);`)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := "2026-08-13T08:00:00.100000000+08:00"
	_, err = db.Exec(`INSERT INTO operation_logs (id,actor_system_account_id,actor_role,mode,module,action,operation_key,resource_type,resource_id,resource_name,summary,detail_level,visibility_scope,changes_json,metadata_json,created_at) VALUES ('oplog-legacy-1','actor','user','self','accounts','update','accounts.update','account','account-1','Legacy Account','legacy operation summary','full','targeted','[{"field":"name","label":"Name","before":"old","after":"new"}]','{}',?)`, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO operation_log_targets VALUES ('optgt-legacy-1','oplog-legacy-1','account','account-1','Legacy Account','owner','primary',?); INSERT INTO operation_log_viewers VALUES ('oplog-legacy-1','actor','actor_self','full',?); INSERT INTO operation_log_summary_search_terms VALUES ('oplog-legacy-1','legacy',?)`, createdAt, createdAt, createdAt)
	if err != nil {
		t.Fatal(err)
	}
}
