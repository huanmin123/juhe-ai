package cleanuprepo

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// usage-record-shards.ts SQLite 清理侧的语义测试：真实 SQLite 目录库 +
// 临时目录分片文件，断言分片定位、目录条目删除的 scope 收缩与空分片
// 文件清理（主库 + -wal/-shm/-journal 副产品）。

func newKitCatalog(t *testing.T) *DB {
	t.Helper()
	db := openKitSQLite(t, "usage_catalog")
	createKitUsageCatalogSchema(t, db.DB)
	return db
}

func seedKitShard(t *testing.T, catalog *DB, shardKey, bucketDate string, shardID int64, filePath string) {
	t.Helper()
	mustExecKit(t, catalog, `INSERT INTO usage_record_shards (shard_key, bucket_date, shard_id, file_path, status)
      VALUES (?, ?, ?, ?, 'active')`, shardKey, bucketDate, shardID, filePath)
}

func newKitShardStore(t *testing.T, root string) *ShardStore {
	t.Helper()
	store := NewShardStore(root)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestShardLocationFromRegistryRow(t *testing.T) {
	location := shardLocationFromRegistryRow(" shard-a ", "2026-01-05", 3, " /tmp/shard.sqlite3 ")
	if location == nil {
		t.Fatalf("合法注册行不应返回 nil")
	}
	if location.ShardKey != "shard-a" || location.BucketDate != "2026-01-05" ||
		location.BucketDateKey != "20260105" || location.ShardID != 3 || location.FilePath != "/tmp/shard.sqlite3" {
		t.Fatalf("location = %+v", location)
	}
	for name, row := range map[string][4]any{
		"空 shardKey": {"", "2026-01-05", int64(1), "p"},
		"空 bucket":   {"k", " ", int64(1), "p"},
		"空 path":     {"k", "2026-01-05", int64(1), ""},
		"非法 shardID": {"k", "2026-01-05", int64(0), "p"},
	} {
		if got := shardLocationFromRegistryRow(row[0].(string), row[1].(string), row[2].(int64), row[3].(string)); got != nil {
			t.Fatalf("%s 应过滤为 nil，实际 %+v", name, got)
		}
	}
}

func TestListLocationsForApiKeyAndAccount(t *testing.T) {
	ctx := context.Background()
	catalog := newKitCatalog(t)
	seedKitShard(t, catalog, "sk-1", "2026-01-05", 1, "/tmp/s1.sqlite3")
	seedKitShard(t, catalog, "sk-2", "2026-01-06", 2, "/tmp/s2.sqlite3")
	seedKitShard(t, catalog, "sk-bad", "2026-01-07", 3, "")
	mustExecKit(t, catalog, `INSERT INTO usage_record_api_key_shards (api_key_id, system_account_id, shard_key, first_created_at)
      VALUES ('key-1','sys-1','sk-1','2026-01-05T00:00:00.000Z'),
             ('key-1','sys-1','sk-2','2026-01-05T00:00:00.000Z'),
             ('key-1','sys-1','sk-bad','2026-01-05T00:00:00.000Z')`)
	mustExecKit(t, catalog, `INSERT INTO usage_record_account_shards (account_id, shard_key, first_created_at)
      VALUES ('acc-1','sk-1','2026-01-05T00:00:00.000Z')`)

	window, err := ListLocationsForApiKey(ctx, catalog, "key-1", "sys-1", 10)
	if err != nil {
		t.Fatalf("ListLocationsForApiKey: %v", err)
	}
	// 空 file_path 的注册行必须被过滤（shardLocationFromRegistryRow 契约）。
	if len(window.Locations) != 2 || window.HasMore {
		t.Fatalf("api-key 窗口 = %+v", window)
	}
	if window.Locations[0].ShardKey != "sk-1" || window.Locations[1].BucketDateKey != "20260106" {
		t.Fatalf("窗口顺序/键错误：%+v", window.Locations)
	}

	// limit+1 探测：取 1 条仍有第二条 → HasMore。
	window, err = ListLocationsForApiKey(ctx, catalog, "key-1", "sys-1", 1)
	if err != nil {
		t.Fatalf("ListLocationsForApiKey limit=1: %v", err)
	}
	if len(window.Locations) != 1 || !window.HasMore {
		t.Fatalf("limit=1 窗口 = %+v", window)
	}

	// 空 scope 参数直接返回空窗口（不触库）。
	window, err = ListLocationsForApiKey(ctx, catalog, " ", "sys-1", 5)
	if err != nil || len(window.Locations) != 0 || window.HasMore {
		t.Fatalf("空 apiKeyID 窗口 = %+v, %v", window, err)
	}
	window, err = ListLocationsForAccount(ctx, catalog, "", 5)
	if err != nil || len(window.Locations) != 0 {
		t.Fatalf("空 accountID 窗口 = %+v, %v", window, err)
	}

	accountWindow, err := ListLocationsForAccount(ctx, catalog, "acc-1", 5)
	if err != nil {
		t.Fatalf("ListLocationsForAccount: %v", err)
	}
	if len(accountWindow.Locations) != 1 || accountWindow.Locations[0].ShardKey != "sk-1" {
		t.Fatalf("account 窗口 = %+v", accountWindow)
	}
}

func TestShardStoreOpenCacheAndClose(t *testing.T) {
	root := t.TempDir()
	store := NewShardStore(root)
	var opens int32
	store.SetOpener(func(filePath string) (*sql.DB, error) {
		atomic.AddInt32(&opens, 1)
		db, err := sql.Open("sqlite", filePath)
		if err != nil {
			return nil, err
		}
		return db, nil
	})
	path := filepath.Join(root, "shard.sqlite3")
	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open cached: %v", err)
	}
	if first != second {
		t.Fatalf("同一路径应命中缓存")
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("打开次数 = %d, 期望 1", opens)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("Close 不应额外打开")
	}
	// 关闭后缓存清空：再次 Open 需要重新打开。
	if _, err := store.Open(path); err != nil {
		t.Fatalf("重开: %v", err)
	}
	if atomic.LoadInt32(&opens) != 2 {
		t.Fatalf("重开后打开次数 = %d, 期望 2", opens)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestShardStoreOpenOpenerError(t *testing.T) {
	store := NewShardStore(t.TempDir())
	store.SetOpener(func(string) (*sql.DB, error) { return nil, errors.New("注入打开失败") })
	if _, err := store.Open("x.sqlite3"); err == nil || !strings.Contains(err.Error(), "open usage shard sqlite 失败") {
		t.Fatalf("打开器错误应包装：%v", err)
	}
	_ = store.Close()
}

func TestShardStoreDeleteShardEntries(t *testing.T) {
	ctx := context.Background()
	catalog := newKitCatalog(t)
	store := newKitShardStore(t, t.TempDir())
	seedKitShard(t, catalog, "sk-1", "2026-01-05", 1, "/tmp/s1.sqlite3")
	seedEntries := func(rows [][5]string) {
		t.Helper()
		for _, row := range rows {
			mustExecKit(t, catalog, `INSERT INTO usage_record_shard_entries
        (usage_id, shard_key, system_account_id, api_key_id, account_id, created_at)
        VALUES (?, ?, ?, ?, ?, '2026-01-05T00:00:00.000Z')`,
				row[0], row[1], row[2], row[3], row[4])
		}
	}
	// usage-1 删除后 (key-1, sys-1, sk-1) / (acc-1, sk-1) 无残余 → catalog 收缩；
	// usage-2 属于另一 scope（sys-2/key-1/acc-2）→ 其 scope 保留。
	seedEntries([][5]string{
		{"usage-1", "sk-1", "sys-1", "key-1", "acc-1"},
		{"usage-2", "sk-1", "sys-2", "key-1", "acc-2"},
	})
	mustExecKit(t, catalog, `INSERT INTO usage_record_api_key_shards (api_key_id, system_account_id, shard_key)
      VALUES ('key-1','sys-1','sk-1'), ('key-1','sys-2','sk-1')`)
	mustExecKit(t, catalog, `INSERT INTO usage_record_account_shards (account_id, shard_key)
      VALUES ('acc-1','sk-1'), ('acc-2','sk-1')`)

	// 空 ids 直接返回 0。
	deleted, err := store.DeleteShardEntries(ctx, catalog, []string{" ", ""})
	if err != nil || deleted != 0 {
		t.Fatalf("空 ids 删除 = %d, %v", deleted, err)
	}

	deleted, err = store.DeleteShardEntries(ctx, catalog, []string{"usage-1", "usage-1 "})
	if err != nil {
		t.Fatalf("DeleteShardEntries: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("删除行数 = %d, 期望 1", deleted)
	}
	if got := mustQueryCountKit(t, catalog, `SELECT COUNT(*) FROM usage_record_shard_entries`); got != 1 {
		t.Fatalf("残余条目数 = %d, 期望 1", got)
	}
	// (key-1, sys-1, sk-1) 条目已删光 → api-key scope 收缩；(key-1, sys-2, sk-1) 保留。
	if got := mustQueryCountKit(t, catalog, `SELECT COUNT(*) FROM usage_record_api_key_shards
      WHERE api_key_id = 'key-1' AND system_account_id = 'sys-1'`); got != 0 {
		t.Fatalf("已清空 scope 的 catalog 行应被收缩")
	}
	if got := mustQueryCountKit(t, catalog, `SELECT COUNT(*) FROM usage_record_api_key_shards
      WHERE api_key_id = 'key-1' AND system_account_id = 'sys-2'`); got != 1 {
		t.Fatalf("仍有条目的 scope 不应收缩")
	}
	// account scope：acc-1 的条目（usage-1）已删 → 收缩；acc-2（usage-2）保留。
	if got := mustQueryCountKit(t, catalog, `SELECT COUNT(*) FROM usage_record_account_shards WHERE account_id = 'acc-1'`); got != 0 {
		t.Fatalf("已清空 account scope 应被收缩")
	}
	if got := mustQueryCountKit(t, catalog, `SELECT COUNT(*) FROM usage_record_account_shards WHERE account_id = 'acc-2'`); got != 1 {
		t.Fatalf("仍有条目的 account scope 不应收缩")
	}
}

func TestShardStoreCleanupEmptyShardFilesBefore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	catalog := newKitCatalog(t)
	store := newKitShardStore(t, root)
	emptyPath := shardFilePathForTest(root, "20260105", 1)
	busyPath := shardFilePathForTest(root, "20260106", 1)
	if err := os.MkdirAll(filepath.Dir(emptyPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(busyPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.WriteFile(emptyPath+suffix, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := os.WriteFile(busyPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	seedKitShard(t, catalog, "sk-empty", "2026-01-05", 1, emptyPath)
	seedKitShard(t, catalog, "sk-busy", "2026-01-06", 1, busyPath)
	mustExecKit(t, catalog, `INSERT INTO usage_record_shard_entries (usage_id, shard_key, system_account_id, created_at)
      VALUES ('usage-busy', 'sk-busy', 'sys-1', '2026-01-06T00:00:00.000Z')`)

	// 非法 cutoff 直接报错。
	if _, err := store.CleanupEmptyShardFilesBefore(ctx, catalog, "not-a-time", 10); err == nil {
		t.Fatalf("非法 cutoff 应报错")
	}

	result, err := store.CleanupEmptyShardFilesBefore(ctx, catalog, kitUpdatedAt, 10)
	if err != nil {
		t.Fatalf("CleanupEmptyShardFilesBefore: %v", err)
	}
	if result.UsageRecordShards != 1 || result.UsageShardFiles != 4 {
		t.Fatalf("result = %+v, 期望 1 目录行 / 4 文件", result)
	}
	if result.HasMore {
		t.Fatalf("无更多候选时 HasMore 应为 false")
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Stat(emptyPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("空分片文件 %s 应被删除", emptyPath+suffix)
		}
	}
	if _, err := os.Stat(busyPath); err != nil {
		t.Fatalf("仍持有条目的分片文件不应被删除：%v", err)
	}
	if got := mustQueryCountKit(t, catalog, `SELECT COUNT(*) FROM usage_record_shards`); got != 1 {
		t.Fatalf("残余目录行数 = %d, 期望 1（sk-busy）", got)
	}

	// limit=1 探测：候选多于批大小时 HasMore（重建 sk-empty 目录行）。
	seedKitShard(t, catalog, "sk-empty", "2026-01-05", 1, emptyPath)
	limited, err := store.CleanupEmptyShardFilesBefore(ctx, catalog, "2026-01-05T12:00:00.000Z", 1)
	if err != nil {
		t.Fatalf("limited: %v", err)
	}
	if limited.HasMore {
		// sk-empty(01-05) 截止 01-05 之后仍只有 1 个候选（sk-busy 有条目）。
		t.Fatalf("单候选时不应 HasMore")
	}
	if limited.UsageRecordShards != 1 {
		t.Fatalf("limited 删除目录行 = %d", limited.UsageRecordShards)
	}
}

func TestDeleteShardFileSet(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "usage-20260105-s01.sqlite3")
	deleted, err := deleteShardFileSet(path)
	if err != nil || deleted != 0 {
		t.Fatalf("全缺失时 deleted = %d, %v", deleted, err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.WriteFile(path+suffix, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	deleted, err = deleteShardFileSet(path)
	if err != nil || deleted != 4 {
		t.Fatalf("deleted = %d, %v", deleted, err)
	}
}

func TestShardFilePathForTest(t *testing.T) {
	got := shardFilePathForTest("root", "20260105", 2)
	if !strings.HasSuffix(got, filepath.Join("root", "2026", "01", "05", "usage-20260105-s02.sqlite3")) {
		t.Fatalf("shardFilePathForTest = %q", got)
	}
	negative := shardFilePathForTest("root", "20260105", -1)
	if !strings.HasSuffix(negative, "usage-20260105-s00.sqlite3") {
		t.Fatalf("负 shardID 应回落 0：%q", negative)
	}
}
