package operationlog

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// F4 业务设置镜像运行期同步（consumer 侧读业务库兜底）：镜像文件是静态同步
// 产物（seed 后不再更新）时，运行期新建/改名的系统账户不在镜像里；F4 读
// 模型用业务库本体句柄兜底解析 actor 显示名。

// seedFallbackBusinessFile 建一个业务库文件：镜像账户之外再带一个运行期
// 新建的系统账户（runtime-created）。
func seedFallbackBusinessFile(t *testing.T, path string, withRuntimeAccount bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		`CREATE TABLE system_settings (system_account_id TEXT NOT NULL,key TEXT NOT NULL,value_json TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(system_account_id,key))`,
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT NOT NULL)`,
		`INSERT INTO system_accounts VALUES ('actor','actor','Actor'),('viewer','viewer','Viewer')`,
		`INSERT INTO system_settings VALUES ('sys_admin','operationLogRetentionDays', '365', '2026-08-13T00:00:00Z')`,
	}
	if withRuntimeAccount {
		statements = append(statements, `INSERT INTO system_accounts VALUES ('runtime','runtime','Runtime Created')`)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}
// seedStaticMirror 建一个只含 seed 时刻账户的静态镜像文件（无 runtime 账户）。
func seedStaticMirror(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL,key TEXT NOT NULL,value_json TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(system_account_id,key)); CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT NOT NULL); INSERT INTO system_accounts VALUES ('actor','actor','Actor'),('viewer','viewer','Viewer'); INSERT INTO system_settings VALUES ('sys_admin','operationLogRetentionDays', '365', '2026-08-13T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
}

// TestSQLiteBusinessFallbackResolvesRuntimeAccounts：镜像缺运行期账户时，
// 配置了业务库兜底句柄的 store 能解析其显示名；未配置兜底的 store 保持
// 原行为（名字留空）。
func TestSQLiteBusinessFallbackResolvesRuntimeAccounts(t *testing.T) {
	root := t.TempDir()
	mirror := filepath.Join(root, "oplog-mirror.sqlite3")
	business := filepath.Join(root, "business.sqlite3")
	seedStaticMirror(t, mirror)
	seedFallbackBusinessFile(t, business, true)
	probe, err := sql.Open("sqlite", business)
	if err != nil {
		t.Fatal(err)
	}
	var names int
	if err := probe.QueryRow(`SELECT COUNT(*) FROM system_accounts WHERE id = 'runtime'`).Scan(&names); err != nil || names != 1 {
		t.Fatalf("runtime account must exist in business file: %d %v", names, err)
	}
	probe.Close()

	openWithFallback := func(withFallback bool) Store {
		t.Helper()
		store, err := OpenStore(Config{
			Enabled: true, InstanceID: "fallback-test", Mode: ModeSQLite,
			DatabasePath:         filepath.Join(root, "operation-"+map[bool]string{true: "fb", false: "plain"}[withFallback]+".sqlite3"),
			BusinessSettingsPath: mirror,
			BusinessDatabasePath: map[bool]string{true: business, false: ""}[withFallback],
		})
		if err != nil {
			t.Fatal(err)
		}
		return store
	}

	store := openWithFallback(true)
	defer store.Close()
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "fallback-test", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	input := Input{ID: "oplog-fb-1", ActorSystemAccountID: "runtime", ActorRole: "admin", Module: "accounts", Action: "create", OperationKey: "accounts.create", ResourceType: "account", Summary: "created runtime account", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if _, err := store.Persist(context.Background(), lease, input); err != nil {
		t.Fatal(err)
	}
	result, err := store.List(context.Background(), ListOptions{})
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("list: %+v err=%v", result, err)
	}
	if result.Items[0].ActorSystemAccountName != "Runtime Created" {
		t.Fatalf("runtime-created actor name must resolve from the business fallback: %+v", result.Items[0])
	}

	// 对照：不配置兜底句柄的 store 保持镜像语义（新账户名字解析为空）。
	plain := openWithFallback(false)
	defer plain.Close()
	plainLease, ok, err := plain.AcquireOwnerLease(context.Background(), "fallback-test", time.Minute)
	if err != nil || !ok {
		t.Fatalf("plain lease: ok=%v err=%v", ok, err)
	}
	plainInput := input
	plainInput.ID = "oplog-fb-2"
	if _, err := plain.Persist(context.Background(), plainLease, plainInput); err != nil {
		t.Fatal(err)
	}
	plainResult, err := plain.List(context.Background(), ListOptions{})
	if err != nil || len(plainResult.Items) != 1 {
		t.Fatalf("plain list: %+v err=%v", plainResult, err)
	}
	if plainResult.Items[0].ActorSystemAccountName != "" {
		t.Fatalf("store without fallback keeps mirror semantics (name stays empty): %+v", plainResult.Items[0])
	}
}

// TestSQLiteBusinessFallbackSameFileReusesHandle：部署契约里镜像路径直接指向
// 业务库文件时，store 不额外开句柄，名字解析照常。
func TestSQLiteBusinessFallbackSameFileReusesHandle(t *testing.T) {
	root := t.TempDir()
	business := filepath.Join(root, "business.sqlite3")
	seedFallbackBusinessFile(t, business, true)
	store, err := OpenStore(Config{
		Enabled: true, InstanceID: "same-file-test", Mode: ModeSQLite,
		DatabasePath:         filepath.Join(root, "operation.sqlite3"),
		BusinessSettingsPath: business,
		BusinessDatabasePath: business,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	concrete := store.(*sqlStore)
	if concrete.businessFallbackDB != nil || !concrete.businessFallbackSameFile {
		t.Fatalf("same-file deployment must reuse the mirror handle: fallback=%v sameFile=%v",
			concrete.businessFallbackDB != nil, concrete.businessFallbackSameFile)
	}
	lease, ok, err := store.AcquireOwnerLease(context.Background(), "same-file-test", time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	input := Input{ID: "oplog-same-1", ActorSystemAccountID: "runtime", ActorRole: "admin", Module: "accounts", Action: "create", OperationKey: "accounts.create", ResourceType: "account", Summary: "created runtime account", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if _, err := store.Persist(context.Background(), lease, input); err != nil {
		t.Fatal(err)
	}
	result, err := store.List(context.Background(), ListOptions{})
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("list: %+v err=%v", result, err)
	}
	if result.Items[0].ActorSystemAccountName != "Runtime Created" {
		t.Fatalf("live business file must resolve runtime accounts directly: %+v", result.Items[0])
	}
}
