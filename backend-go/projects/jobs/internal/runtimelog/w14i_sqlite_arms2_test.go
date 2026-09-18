package runtimelog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// w14i_sqlite_arms2_test.go 用本地 SQLite RAISE(ABORT) 触发器、只读文件、
// 悬空符号链接与独占句柄覆盖剩余可注入臂：
//   - ReplaceCursor/cleanupSQLite/decrementSQLiteFacets 的语句错误臂；
//   - CheckSchema 缺列错误臂与 ensureSQLiteFenceTokenColumn 的 ALTER 错误臂；
//   - OpenStore 的 journal_mode 失败臂；
//   - indexer 的 FileIdentity 失败/非常规文件/批量 flush 错误/Seek 错误臂；
//   - cleanupRotatedFiles 的非常规文件保护与溢出删除错误臂；
//   - legacy 迁移的完整性/缺列错误臂与 canonical 路径符号链接臂；
//   - validateSQLiteIsolation 的 Codex shard 校验错误臂。
//
// w14i 波次不可达清单（均已核对）：rows.Scan/rows.Err/RowsAffected/tx.Commit
// 级错误臂、os.Open 与 stat-open 竞态臂、cleaned-up ctx 检查臂、commit/
// copyCursor/replaceCursor 的 lease 缺失臂（RunOnce 入口已校验）、legacy 的
// 数量/缺失校验臂（INSERT OR IGNORE 同键复制保证）、只读数据源 sql.Open 臂、
// parser 的二次 Unmarshal 失败臂、owner_lease 的 stopRenewal select 臂
// （依赖 select 随机性）。

// ---- SQLite 触发器注入 ----

func w14iOpenReadyStore(t *testing.T) (*sqliteStore, context.Context, OwnerLease) {
	t.Helper()
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	ctx := testOwnerContext(t, store)
	lease, err := ownerLeaseFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return store, ctx, lease
}

func w14iExecTrigger(t *testing.T, store *sqliteStore, ddl string) {
	t.Helper()
	if _, err := store.db.Exec(ddl); err != nil {
		t.Fatalf("创建触发器失败: %v", err)
	}
}

func w14iCommitSeedRecord(t *testing.T, store *sqliteStore, ctx context.Context, lease OwnerLease) {
	t.Helper()
	cursor := Cursor{LogFile: "w14i-trigger.log", FileIdentity: "w14i-trigger-identity", CursorOffset: 10, FileSize: 10}
	records := []Record{{ID: "w14i-trigger-rec", LogFile: "w14i-trigger.log", LogOffset: 0, LineNumber: 1, Time: "2026-08-01T00:00:00.000Z", Level: "info", Event: "w14i-trigger-event", CreatedAt: "2026-08-01T00:00:00.000Z", RawJSON: "{}"}}
	if err := store.Commit(ctx, lease, records, cursor, time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("种子 Commit 失败: %v", err)
	}
}

func TestW14iCleanupDeleteLogsErrorViaTrigger(t *testing.T) {
	store, ctx, lease := w14iOpenReadyStore(t)
	w14iCommitSeedRecord(t, store, ctx, lease)
	w14iExecTrigger(t, store, `CREATE TRIGGER w14i_t_del_log BEFORE DELETE ON runtime_logs BEGIN SELECT RAISE(ABORT, 'w14i del log boom'); END;`)
	_, err := store.Cleanup(ctx, lease, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), 10, 3)
	if err == nil || !strings.Contains(err.Error(), "w14i del log boom") {
		t.Fatalf("记录删除触发器应中止 Cleanup: %v", err)
	}
}

func TestW14iDecrementFacetsArmsViaTriggers(t *testing.T) {
	cases := []struct {
		name string
		ddl  string
		boom string
	}{
		{"summary-update", `CREATE TRIGGER w14i_t_upd_summary BEFORE UPDATE ON runtime_log_facet_summary BEGIN SELECT RAISE(ABORT, 'w14i upd summary boom'); END;`, "w14i upd summary boom"},
		{"summary-delete", `CREATE TRIGGER w14i_t_del_summary BEFORE DELETE ON runtime_log_facet_summary BEGIN SELECT RAISE(ABORT, 'w14i del summary boom'); END;`, "w14i del summary boom"},
		{"level-update", `CREATE TRIGGER w14i_t_upd_level BEFORE UPDATE ON runtime_log_level_facets BEGIN SELECT RAISE(ABORT, 'w14i upd level boom'); END;`, "w14i upd level boom"},
		{"level-delete", `CREATE TRIGGER w14i_t_del_level BEFORE DELETE ON runtime_log_level_facets BEGIN SELECT RAISE(ABORT, 'w14i del level boom'); END;`, "w14i del level boom"},
		{"event-update", `CREATE TRIGGER w14i_t_upd_event BEFORE UPDATE ON runtime_log_event_facets BEGIN SELECT RAISE(ABORT, 'w14i upd event boom'); END;`, "w14i upd event boom"},
		{"event-delete", `CREATE TRIGGER w14i_t_del_event BEFORE DELETE ON runtime_log_event_facets BEGIN SELECT RAISE(ABORT, 'w14i del event boom'); END;`, "w14i del event boom"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store, ctx, lease := w14iOpenReadyStore(t)
			w14iCommitSeedRecord(t, store, ctx, lease)
			w14iExecTrigger(t, store, testCase.ddl)
			_, err := store.Cleanup(ctx, lease, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), 10, 3)
			if err == nil || !strings.Contains(err.Error(), testCase.boom) {
				t.Fatalf("%s 应中止 facet 扣减: %v", testCase.name, err)
			}
		})
	}
}

func TestW14iReplaceCursorDeleteErrorViaTrigger(t *testing.T) {
	store, ctx, lease := w14iOpenReadyStore(t)
	// BEFORE DELETE 触发器只对实际命中的行触发，先落一行游标。
	if _, err := store.db.Exec(`INSERT INTO runtime_log_file_cursors (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at) VALUES ('w14i-replace.log', 'old', 1, 1, 1, 0, 0, '2026-08-01T00:00:00.000Z', NULL, '2026-08-01T00:00:00.000Z', '2026-08-01T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	w14iExecTrigger(t, store, `CREATE TRIGGER w14i_t_del_cursor BEFORE DELETE ON runtime_log_file_cursors BEGIN SELECT RAISE(ABORT, 'w14i del cursor boom'); END;`)
	replacement := Cursor{LogFile: "w14i-replace.log", FileIdentity: "w14i-replace-identity", CursorOffset: 1, FileSize: 1}
	err := store.ReplaceCursor(ctx, lease, nil, replacement)
	if err == nil || !strings.Contains(err.Error(), "w14i del cursor boom") {
		t.Fatalf("游标删除触发器应中止 ReplaceCursor: %v", err)
	}
}

func TestW14iVerifyLeaseUpdateErrorViaTrigger(t *testing.T) {
	store, ctx, lease := w14iOpenReadyStore(t)
	w14iExecTrigger(t, store, `CREATE TRIGGER w14i_t_upd_lease BEFORE UPDATE ON runtime_log_index_owner_leases BEGIN SELECT RAISE(ABORT, 'w14i upd lease boom'); END;`)
	// 触发器先于 owner lease 释放清理拆除，避免清理路径被中止。
	t.Cleanup(func() { _, _ = store.db.Exec(`DROP TRIGGER w14i_t_upd_lease`) })
	err := store.VerifyOwnerLease(ctx, lease)
	if err == nil || !strings.Contains(err.Error(), "w14i upd lease boom") {
		t.Fatalf("lease UPDATE 触发器应中止校验: %v", err)
	}
}

func TestW14iCheckSchemaRejectsWrongColumns(t *testing.T) {
	// 契约：表存在但列不齐时 CheckSchema 必须经 checkSQLiteColumns fail-closed。
	path := filepath.Join(t.TempDir(), "w14i-wrong-columns.sqlite3")
	handle, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range runtimeLogTables {
		if _, err := handle.Exec("CREATE TABLE " + table + " (dummy_column TEXT)"); err != nil {
			t.Fatal(err)
		}
	}
	handle.Close()
	store, _, err := openSQLiteStoreAtPath(t, path)
	if err != nil {
		t.Fatal(err)
	}
	err = store.CheckSchema(context.Background())
	if err == nil || !strings.Contains(err.Error(), "缺少运行日志字段") {
		t.Fatalf("缺列表应报缺失字段: %v", err)
	}
}

func TestW14iFenceColumnAlterError(t *testing.T) {
	// 契约：只读连接上的 ALTER 迁移必须上抛原始错误。
	path := filepath.Join(t.TempDir(), "w14i-readonly-lease.sqlite3")
	handle, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if _, err := handle.Exec(`CREATE TABLE runtime_log_index_owner_leases (lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, lease_until TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatal(err)
	}
	err = ensureSQLiteFenceTokenColumn(context.Background(), handle)
	if err == nil {
		t.Fatalf("只读连接上的 ALTER 应失败")
	}
}

func TestW14iOpenStoreSQLiteRejectsReadOnlyWAL(t *testing.T) {
	dir := t.TempDir()
	rlPath := filepath.Join(dir, "w14i-readonly-rl.sqlite3")
	handle, err := sql.Open("sqlite", rlPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(rlPath, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(rlPath, 0o666) })
	_, err = OpenStore(context.Background(), Config{
		Mode:                   ModeSQLite,
		RuntimeLogDatabasePath: rlPath,
		BusinessPath:           filepath.Join(dir, "w14i-business.sqlite3"),
	})
	if err == nil {
		t.Fatalf("只读运行日志库启用 WAL 应失败")
	}
}

// ---- Indexer 补充臂 ----

func TestW14iIndexerFileIdentityErrorViaExclusiveLock(t *testing.T) {
	// 契约：文件被独占持有（不允许共享读）时 FileIdentity 必须 fail-closed。
	dir := t.TempDir()
	path := filepath.Join(dir, "juhe-ai.log")
	if err := os.WriteFile(path, []byte("w14i locked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	Ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(Ptr, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Skipf("w14i: 无法建立独占句柄: %v", err)
	}
	defer windows.CloseHandle(handle)

	if _, err := FileIdentity(path, nil); err == nil {
		t.Fatalf("独占文件应使 FileIdentity 失败")
	}
	// 走 indexer 导入流程：identity 读取失败必须作为导入错误上抛。
	fake := &w12dFakeStore{retentionDays: 7}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	if err := indexer.RunOnce(w12dLeaseContext(context.Background())); err == nil {
		t.Fatalf("独占文件应使 RunOnce 报导入错误")
	}
}

func TestW14iCleanupRotatedFileIdentityErrorIsProtected(t *testing.T) {
	// 契约：rotated 文件 identity 读取失败时按保护处理，不删除也不报错。
	dir := t.TempDir()
	current := filepath.Join(dir, "juhe-ai.log")
	if err := os.WriteFile(current, []byte("w14i current\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "juhe-ai.log.20260817T000000Z.11112222-3333-4444-5555-666677778893.log")
	if err := os.WriteFile(locked, []byte("w14i rotated locked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockedPtr, err := windows.UTF16PtrFromString(locked)
	if err != nil {
		t.Fatal(err)
	}
	lockHandle, err := windows.CreateFile(lockedPtr, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Skipf("w14i: 无法建立独占句柄: %v", err)
	}
	defer windows.CloseHandle(lockHandle)

	fake := &w12dFakeStore{retentionDays: 7}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	indexer.config.LogMaxFiles = 2
	indexer.config.LogRetentionDays = 30
	if runErr := indexer.RunRetention(w12dLeaseContext(context.Background())); runErr != nil {
		t.Fatalf("identity 失败的 rotated 文件应被保护而不是报错: %v", runErr)
	}
	if _, statErr := os.Stat(locked); statErr != nil {
		t.Fatalf("受保护文件应仍然存在")
	}
}

func TestW14iIndexerNonRegularRotatedFile(t *testing.T) {
	// 指向目录的 symlink 按非常规文件保护，不参与导入。
	dir := t.TempDir()
	target := filepath.Join(dir, "w14i-target-dir")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "juhe-ai.log.20260817T000000Z.11112222-3333-4444-5555-666677778888.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("w14i: 无法创建符号链接: %v", err)
	}
	fake := &w12dFakeStore{retentionDays: 7}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	if err := indexer.RunOnce(w12dLeaseContext(context.Background())); err != nil {
		t.Fatalf("非常规文件应被保护性跳过而不是报错: %v", err)
	}
}

func TestW14iIndexerSymlinkLoopStatError(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "juhe-ai.log.20260817T000000Z.11112222-3333-4444-5555-666677778889.log")
	b := filepath.Join(dir, "w14i-loop-b")
	if err := os.Symlink(b, a); err != nil {
		t.Skipf("w14i: 无法创建符号链接: %v", err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Skipf("w14i: 无法创建符号链接环: %v", err)
	}
	fake := &w12dFakeStore{retentionDays: 7}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	if err := indexer.RunOnce(w12dLeaseContext(context.Background())); err == nil {
		t.Fatalf("符号链接环应使 os.Stat 失败")
	}
}

func TestW14iIndexerSeekErrorBubblesAsPlainError(t *testing.T) {
	// 契约：负游标偏移导致 Seek 失败时，错误必须按普通失败处理并合并上抛。
	dir := t.TempDir()
	path := filepath.Join(dir, "juhe-ai.log")
	if err := os.WriteFile(path, []byte("w14i seek line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := w14iFileIdentity(t, path)
	fake := &w12dFakeStore{
		retentionDays: 7,
		findCursor:    &Cursor{LogFile: path, FileIdentity: identity, CursorOffset: -5, FileSize: 0},
	}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	err := indexer.RunOnce(w12dLeaseContext(context.Background()))
	if err == nil {
		t.Fatalf("Seek 失败应上抛")
	}
}

func TestW14iIndexerBatchLimitFlushError(t *testing.T) {
	dir := t.TempDir()
	path := w14iCurrentLogFile(t, dir, "w14i line one\n", "w14i line two\n")
	identity := w14iFileIdentity(t, path)
	fake := &w12dFakeStore{
		retentionDays: 7,
		findCursor:    &Cursor{LogFile: path, FileIdentity: identity, CursorOffset: 0, FileSize: 0},
		commitErr:     errors.New("w14i batch flush boom"),
	}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	indexer.config.BatchSize = 1
	err := indexer.RunOnce(w12dLeaseContext(context.Background()))
	if err == nil || !strings.Contains(err.Error(), "w14i batch flush boom") {
		t.Fatalf("批间 flush 失败应上抛: %v", err)
	}
}

// ---- cleanupRotatedFiles 补充臂 ----

func TestW14iCleanupRotatedProtectsAndDeletes(t *testing.T) {
	dir := t.TempDir()
	current := filepath.Join(dir, "juhe-ai.log")
	if err := os.WriteFile(current, []byte("w14i current\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotatedSuffix := ".20260817T000000Z.11112222-3333-4444-5555-66667777888a.log"
	normal := filepath.Join(dir, "juhe-ai.log"+rotatedSuffix)
	if err := os.WriteFile(normal, []byte("w14i rotated normal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 非常规文件（指向文件的 symlink）作为保护对象；目录 symlink 会被
	// FindFirstFile 归类为目录而提前跳过。
	protected := filepath.Join(dir, "juhe-ai.log.20260817T000000Z.11112222-3333-4444-5555-66667777888c.log")
	if err := os.Symlink(current, protected); err != nil {
		t.Skipf("w14i: 无法创建符号链接: %v", err)
	}

	fake := &w14iFenceBoomStore{w12dFakeStore: w12dFakeStore{
		retentionDays: 7,
		findCursor:    &Cursor{LogFile: "whatever", FileIdentity: "x", CursorOffset: 99999, FileSize: 99999},
	}}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	indexer.config.LogMaxFiles = 2
	indexer.config.LogRetentionDays = 30
	err := indexer.RunRetention(w12dLeaseContext(context.Background()))
	if err == nil || !strings.Contains(err.Error(), "w14i fence boom") {
		t.Fatalf("溢出删除经 fence 失败应上抛: %v", err)
	}
}

// w14iFenceBoomStore 让 WithOwnerLeaseFence 始终失败，用于注入溢出删除错误。
type w14iFenceBoomStore struct {
	w12dFakeStore
}

func (s *w14iFenceBoomStore) WithOwnerLeaseFence(ctx context.Context, lease OwnerLease, callback func() error) error {
	return errors.New("w14i fence boom")
}

func TestW14iCleanupRotatedFindCursorByIdentityError(t *testing.T) {
	dir := t.TempDir()
	rotated := filepath.Join(dir, "juhe-ai.log.20260817T000000Z.11112222-3333-4444-5555-66667777888d.log")
	if err := os.WriteFile(rotated, []byte("w14i rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &w12dFakeStore{
		retentionDays:  7,
		findByIdentErr: errors.New("w14i identity lookup boom"),
	}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	indexer.config.LogMaxFiles = 2
	if err := indexer.RunRetention(w12dLeaseContext(context.Background())); err == nil || !strings.Contains(err.Error(), "w14i identity lookup boom") {
		t.Fatalf("identity 游标查询失败应上抛: %v", err)
	}
}

// ---- legacy 迁移补充臂 ----

func TestW14iMigrateLegacyGarbageDatasetFailsIntegrity(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	dataset := filepath.Join(t.TempDir(), "w14i-legacy-garbage.sqlite3")
	if err := os.WriteFile(dataset, []byte("w14i this is not a sqlite database at all........"), 0o600); err != nil {
		t.Fatal(err)
	}
	var storeIface Store = store
	migrateErr := MigrateLegacySQLite(testOwnerContext(t, storeIface), w12dMigrationConfig(t, dataset), storeIface)
	if migrateErr == nil || !strings.Contains(migrateErr.Error(), "附加旧运行日志数据库失败") {
		t.Fatalf("非 SQLite 数据源应在 ATTACH 处 fail-closed: %v", migrateErr)
	}
}

func TestW14iMigrateLegacyMissingInstantColumnFails(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	dataset := filepath.Join(t.TempDir(), "w14i-legacy-nocol.sqlite3")
	handle, err := sql.Open("sqlite", dataset)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range runtimeLogTables {
		if _, err := handle.Exec("CREATE TABLE " + table + " (dummy_column TEXT)"); err != nil {
			t.Fatal(err)
		}
	}
	handle.Close()
	var storeIface Store = store
	err = MigrateLegacySQLite(testOwnerContext(t, storeIface), w12dMigrationConfig(t, dataset), storeIface)
	if err == nil {
		t.Fatalf("缺列数据源应使迁移失败")
	}
}

func TestW14iMigrateLegacyCorruptedPageFailsIntegrity(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	dataset := filepath.Join(t.TempDir(), "w14i-legacy-corrupt-page.sqlite3")
	w12dCreateLegacyDataset(t, dataset, "2026-08-08T00:00:00.000Z")
	raw, err := os.ReadFile(dataset)
	if err != nil {
		t.Fatal(err)
	}
	// 破坏 page 1 的 sqlite_master b-tree 区域（header 之后）。
	for offset := 100; offset < 200 && offset < len(raw); offset++ {
		raw[offset] = 0xAB
	}
	if err := os.WriteFile(dataset, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var storeIface Store = store
	err = MigrateLegacySQLite(testOwnerContext(t, storeIface), w12dMigrationConfig(t, dataset), storeIface)
	if err == nil {
		t.Fatalf("页损坏应使迁移在完整性校验处失败")
	}
}

func TestW14iMigrateLegacyAcceptsSymlinkedDataset(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	dataset := filepath.Join(t.TempDir(), "w14i-legacy-real.sqlite3")
	w12dCreateLegacyDataset(t, dataset, "2026-08-08T00:00:00.000Z")
	link := filepath.Join(t.TempDir(), "w14i-legacy-link.sqlite3")
	if err := os.Symlink(dataset, link); err != nil {
		t.Skipf("w14i: 无法创建符号链接: %v", err)
	}
	var storeIface Store = store
	if err := MigrateLegacySQLite(testOwnerContext(t, storeIface), w12dMigrationConfig(t, link), storeIface); err != nil {
		t.Fatalf("指向真实数据源的 symlink 应可迁移: %v", err)
	}
}

// ---- validateSQLiteIsolation / FileIdentity 补充臂 ----

func TestW14iValidateIsolationRejectsDanglingShardSymlink(t *testing.T) {
	dir := t.TempDir()
	shardRoot := filepath.Join(dir, "w14i-shards")
	if err := os.MkdirAll(shardRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(shardRoot, "w14i-shard.sqlite3")
	if err := os.Symlink(filepath.Join(dir, "w14i-missing.sqlite3"), dangling); err != nil {
		t.Skipf("w14i: 无法创建符号链接: %v", err)
	}
	business := filepath.Join(dir, "business.sqlite3")
	if err := os.WriteFile(business, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := Config{
		BusinessPath:           business,
		DatasetPath:            filepath.Join(dir, "dataset.sqlite3"),
		UsageCatalogPath:       filepath.Join(dir, "usage.sqlite3"),
		StatsPath:              filepath.Join(dir, "stats.sqlite3"),
		RuntimeLogDatabasePath: filepath.Join(dir, "runtime-log.sqlite3"),
		CodexShardRoot:         shardRoot,
	}
	err := validateSQLiteIsolation(config, filepath.Join(dir, "table-monitor.sqlite3"))
	if err == nil {
		t.Fatalf("悬空 shard symlink 应使隔离校验失败")
	}
}

func TestW14iFileIdentityRejectsNulPath(t *testing.T) {
	if _, err := FileIdentity("w14i-\x00-invalid", nil); err == nil {
		t.Fatalf("含 NUL 的路径应使 FileIdentity 失败")
	}
}

func TestW14iCleanupRotatedCtxCancelInsideOverflowLoop(t *testing.T) {
	// 契约：溢出删除循环的每一次删除前都要响应 ctx 取消。
	dir := t.TempDir()
	current := filepath.Join(dir, "juhe-ai.log")
	if err := os.WriteFile(current, []byte("w14i current\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "juhe-ai.log.20260817T000000Z.11112222-3333-4444-5555-66667777888e.log")
	if err := os.WriteFile(first, []byte("w14i rotated first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "juhe-ai.log.20260817T000000Z.11112222-3333-4444-5555-66667777888f.log")
	if err := os.WriteFile(second, []byte("w14i rotated second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 非常规文件（指向文件的 symlink）抬高保护计数，压缩可删除配额。
	protected := filepath.Join(dir, "juhe-ai.log.20260817T000000Z.11112222-3333-4444-5555-666677778890.log")
	if err := os.Symlink(current, protected); err != nil {
		t.Skipf("w14i: 无法创建符号链接: %v", err)
	}

	ctx, cancel := context.WithCancel(w12dLeaseContext(context.Background()))
	defer cancel()
	fake := &w14iCancelInFenceStore{
		w12dFakeStore: w12dFakeStore{
			retentionDays: 7,
			findCursor:    &Cursor{LogFile: "whatever", FileIdentity: "x", CursorOffset: 99999, FileSize: 99999},
		},
		cancel: cancel,
	}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	indexer.config.LogMaxFiles = 2
	indexer.config.LogRetentionDays = 30
	err := indexer.RunRetention(ctx)
	if err == nil {
		t.Fatalf("溢出删除循环应响应 ctx 取消")
	}
}

// w14iCancelInFenceStore 在 fence 回调前取消 ctx，模拟删除过程中的取消。
type w14iCancelInFenceStore struct {
	w12dFakeStore
	cancel context.CancelFunc
}

func (s *w14iCancelInFenceStore) WithOwnerLeaseFence(ctx context.Context, lease OwnerLease, callback func() error) error {
	err := callback()
	// 第一次删除成功后取消 ctx，令下一次循环迭代在入口处停止。
	s.cancel()
	return err
}

func TestW14iIndexerSecondFileSeesCanceledContext(t *testing.T) {
	// 契约：第一个文件提交失败并取消 ctx 后，第二个文件的读取循环必须在
	// ctx 检查处失败，且该失败不作为 ctx 取消被吞掉。
	dir := t.TempDir()
	first := filepath.Join(dir, "juhe-ai.log.20260817T000000Z.11112222-3333-4444-5555-666677778891.log")
	if err := os.WriteFile(first, []byte("w14i first file line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "juhe-ai.log.20260817T000000Z.11112222-3333-4444-5555-666677778892.log")
	if err := os.WriteFile(second, []byte("w14i second file line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := w14iFileIdentity(t, first)
	ctx, cancel := context.WithCancel(w12dLeaseContext(context.Background()))
	defer cancel()
	fake := &w14iCancelOnCommitStore{
		w12dFakeStore: &w12dFakeStore{
			retentionDays: 7,
			findCursor:    &Cursor{LogFile: first, FileIdentity: identity, CursorOffset: 0, FileSize: 0},
			commitErr:     errors.New("w14i commit boom"),
		},
		cancel: cancel,
	}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	indexer.config.BatchSize = 1
	err := indexer.RunOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "w14i commit boom") {
		t.Fatalf("首文件提交错误应聚合上抛: %v", err)
	}
	if fake.calls != 2 {
		t.Fatalf("第二个文件应在 ctx 检查处失败并继续聚合: calls=%d", fake.calls)
	}
}

// w14iCancelOnCommitStore 在首次 Commit 时取消 ctx 并注入提交错误。
type w14iCancelOnCommitStore struct {
	*w12dFakeStore
	cancel context.CancelFunc
	calls  int
}

func (s *w14iCancelOnCommitStore) Commit(ctx context.Context, lease OwnerLease, records []Record, cursor Cursor, retentionCutoff time.Time) error {
	s.calls++
	if s.calls == 1 {
		s.cancel()
	}
	return s.commitErr
}
