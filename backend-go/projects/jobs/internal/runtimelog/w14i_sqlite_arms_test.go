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
)

// w14i_sqlite_arms_test.go 覆盖 SQLite store、indexer、legacy 迁移与 lease
// 续租循环的剩余可注入臂。全部使用本地临时 SQLite 与脚本化 fake store。
//
// w14i 波次不可达清单（沿用 w12d 登记，均已核对）：
//   - OpenStore SQLite PRAGMA/busy_timeout/journal_mode 失败臂与 sql.Open
//     错误臂：真实驱动下无连接级注入点；
//   - legacy 迁移的 verifyLegacyMigration 数量/缺失臂、列读取 scan/rows.Err
//     臂、integrity_check 失败臂、tx.Commit/DETACH 错误臂、canonicalSQLitePath
//     的 stat/symlink 竞态臂：INSERT OR IGNORE 同键复制与本地健康文件使其
//     不可达；
//   - identity_windows.go 非 Windows 编译臂；danglingSQLiteSymlink 的
//     ReadDir 错误臂与 entry.Info 错误臂；
//   - indexer 的 os.Open/handle.Stat/Seek/读错误臂：stat 与 open 之间的
//     竞态窗口无法确定性制造；FileIdentity 错误臂需要合法 stat 后的句柄
//     失败，同理不可达；
//   - indexer commit/copyCursor/replaceCursor 内 ownerLeaseFromContext 错误
//     臂：RunOnce 入口已校验同一 ctx 的 lease，不可达。

// ---- SQLite store 臂 ----

func TestW14iOpenStoreSQLiteRejectsCorruptBusinessFile(t *testing.T) {
	// 契约：business 库文件损坏时只读打开必须 fail-closed。
	dir := t.TempDir()
	// business 路径指向目录：modernc sqlite 的 Ping 阶段即拒绝。
	businessPath := filepath.Join(dir, "w14i-business-as-dir")
	if err := os.MkdirAll(businessPath, 0o700); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenStore(context.Background(), Config{
		Mode:                   ModeSQLite,
		RuntimeLogDatabasePath: filepath.Join(dir, "w14i-rl.sqlite3"),
		BusinessPath:           businessPath,
	})
	if err == nil {
		_ = opened.Close()
		t.Fatalf("business 路径为目录时 OpenStore 应失败")
	}
}

func TestW14iEnsureSchemaMigratesLegacyFenceColumn(t *testing.T) {
	// 契约：缺 fence_token 的历史 owner lease 表必须被就地补列。
	path := filepath.Join(t.TempDir(), "w14i-legacy-lease.sqlite3")
	handle, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec(`CREATE TABLE runtime_log_index_owner_leases (lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, lease_until TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	handle.Close()
	store, _, err := openSQLiteStoreAtPath(t, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatalf("EnsureSchema 应补齐 fence_token 列: %v", err)
	}
	var columns int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('runtime_log_index_owner_leases') WHERE name = 'fence_token'`).Scan(&columns); err != nil || columns != 1 {
		t.Fatalf("fence_token 列应存在: columns=%d err=%v", columns, err)
	}
}

func TestW14iSQLiteCheckSchemaRejectsMissingColumns(t *testing.T) {
	// 契约：空库缺列时 CheckSchema 必须 fail-closed。
	path := filepath.Join(t.TempDir(), "w14i-empty.sqlite3")
	store, _, err := openSQLiteStoreAtPath(t, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(context.Background()); err == nil {
		t.Fatalf("空库 CheckSchema 应报错")
	}
}

func TestW14iSQLiteCommitAndCursorValidationArms(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	ctx := w12dLeaseContext(context.Background())
	lease := OwnerLease{OwnerID: "w12d-fake-owner", FenceToken: 1}
	if _, acquired, err := store.AcquireOwnerLease(ctx, lease.OwnerID, time.Minute); err != nil || !acquired {
		t.Fatalf("acquire=%t err=%v", acquired, err)
	}
	goodCursor := Cursor{LogFile: "w14i-sqlite.log", FileIdentity: "w14i-sqlite-identity", CursorOffset: 10, FileSize: 10}
	goodRecord := Record{ID: "w14i-sqlite-rec", LogFile: "w14i-sqlite.log", LogOffset: 0, LineNumber: 1, Time: "2026-09-17T00:00:00.000Z", CreatedAt: "2026-09-17T00:00:00.000Z", RawJSON: "{}"}
	if err := store.Commit(ctx, lease, []Record{goodRecord}, goodCursor, time.Now().UTC()); err != nil {
		t.Fatalf("合法 Commit 应成功: %v", err)
	}
	// 非法记录：空 time。
	if err := store.Commit(ctx, lease, []Record{{ID: "w14i-bad-rec"}}, goodCursor, time.Now().UTC()); err == nil {
		t.Fatalf("空 time 记录应使 Commit 失败")
	}
	// 非法游标：坏 CreatedAt。
	badCursor := goodCursor
	badCursor.CreatedAt = "not-a-time"
	if err := store.Commit(ctx, lease, nil, badCursor, time.Now().UTC()); err == nil {
		t.Fatalf("坏 CreatedAt 游标应使 Commit 失败")
	}
	// ReplaceCursor 的 displaced / replacement 校验臂。
	good := Cursor{LogFile: "w14i-sqlite.log", FileIdentity: "w14i-sqlite-identity", CursorOffset: 12, FileSize: 12}
	if err := store.ReplaceCursor(ctx, lease, &Cursor{LogFile: "w14i-sqlite.log", CreatedAt: "not-a-time"}, good); err == nil {
		t.Fatalf("非法 displaced 应使 ReplaceCursor 失败")
	}
	if err := store.ReplaceCursor(ctx, lease, nil, Cursor{LogFile: "w14i-sqlite.log", LastReadAt: "not-a-time"}); err == nil {
		t.Fatalf("非法 replacement 应使 ReplaceCursor 失败")
	}
	// 续租与错误 fence。
	if renewed, err := store.RenewOwnerLease(ctx, lease, time.Minute); err != nil || !renewed {
		t.Fatalf("续租应成功: %t %v", renewed, err)
	}
	if renewed, err := store.RenewOwnerLease(ctx, OwnerLease{OwnerID: lease.OwnerID, FenceToken: 99}, time.Minute); err != nil || renewed {
		t.Fatalf("错误 fence 续租应返回 false: %t %v", renewed, err)
	}
	if err := store.VerifyOwnerLease(ctx, OwnerLease{OwnerID: lease.OwnerID, FenceToken: 98}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("错误 fence 校验应判丢失: %v", err)
	}
	if err := store.ReleaseOwnerLease(ctx, OwnerLease{OwnerID: lease.OwnerID, FenceToken: 97}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("错误 fence 释放应判丢失: %v", err)
	}
	// 关闭后写入必须报错而不是静默成功。
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(ctx, lease, nil, goodCursor, time.Now().UTC()); err == nil {
		t.Fatalf("关闭后 Commit 应报错")
	}
	if err := store.CopyCursor(ctx, lease, good); err == nil {
		t.Fatalf("关闭后 CopyCursor 应报错")
	}
	if err := store.VerifyOwnerLease(ctx, lease); err == nil {
		t.Fatalf("关闭后 VerifyOwnerLease 应报错")
	}
	_ = config
}

// ---- Indexer 臂 ----

func w14iIndexerConfig(t *testing.T, logDirectory string, store Store) (Indexer, context.CancelFunc) {
	t.Helper()
	config := Config{
		Mode:                   ModeSQLite,
		RuntimeLogDatabasePath: filepath.Join(t.TempDir(), "w14i-idx-rl.sqlite3"),
		BusinessPath:           filepath.Join(t.TempDir(), "w14i-idx-business.sqlite3"),
		LogDirectory:           logDirectory,
		PollInterval:           2 * time.Millisecond,
		RetentionInterval:      3 * time.Millisecond,
		RetentionDays:          7,
		BatchSize:              4,
	}
	indexer := *NewIndexer(config, store)
	return indexer, func() {}
}

func TestW14iIndexerRunLoopTimersAndCancellation(t *testing.T) {
	fake := &w12dFakeStore{retentionDays: 7}
	indexer, _ := w14iIndexerConfig(t, t.TempDir(), fake)
	ctx, cancel := context.WithCancel(w12dLeaseContext(context.Background()))
	go func() {
		time.Sleep(85 * time.Millisecond)
		cancel()
	}()
	err := indexer.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run 应以 ctx 取消结束: %v", err)
	}
}

func TestW14iIndexerRunStopsWhenRetentionFails(t *testing.T) {
	fake := &w12dFakeStore{retentionDays: 7, cleanupErr: errors.New("w14i cleanup boom")}
	indexer, _ := w14iIndexerConfig(t, t.TempDir(), fake)
	err := indexer.Run(w12dLeaseContext(context.Background()))
	if err == nil || !strings.Contains(err.Error(), "w14i cleanup boom") {
		t.Fatalf("RunRetention 失败应终止 Run: %v", err)
	}
}

func TestW14iIndexerDiscoverFilesError(t *testing.T) {
	fake := &w12dFakeStore{retentionDays: 7}
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	indexer, _ := w14iIndexerConfig(t, blocker, fake)
	if err := indexer.RunOnce(w12dLeaseContext(context.Background())); err == nil {
		t.Fatalf("LogDirectory 是文件时 RunOnce 应报错")
	}
}

func w14iCurrentLogFile(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, "juhe-ai.log")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func w14iFileIdentity(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := FileIdentity(path, info)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestW14iIndexerCommitFailurePersistsFailureCursorJoin(t *testing.T) {
	// 契约：flush 失败后写入失败游标再次失败时，两个错误必须合并返回。
	dir := t.TempDir()
	path := w14iCurrentLogFile(t, dir, "w14i line one\n", "w14i line two\n")
	identity := w14iFileIdentity(t, path)
	fake := &w12dFakeStore{
		retentionDays: 7,
		findCursor:    &Cursor{LogFile: path, FileIdentity: identity, CursorOffset: 0, FileSize: 0},
		commitErr:     errors.New("w14i commit boom"),
	}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	err := indexer.RunOnce(w12dLeaseContext(context.Background()))
	if err == nil || !strings.Contains(err.Error(), "w14i commit boom") {
		t.Fatalf("失败游标持久化错误应合并上抛: %v", err)
	}
}

func TestW14iIndexerTruncationResetCommitError(t *testing.T) {
	dir := t.TempDir()
	path := w14iCurrentLogFile(t, dir, "w14i short\n")
	identity := w14iFileIdentity(t, path)
	fake := &w12dFakeStore{
		retentionDays: 7,
		findCursor:    &Cursor{LogFile: path, FileIdentity: identity, CursorOffset: int64(10 * 1024), FileSize: int64(10 * 1024)},
		commitErr:     errors.New("w14i reset boom"),
	}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	err := indexer.RunOnce(w12dLeaseContext(context.Background()))
	if err == nil || !strings.Contains(err.Error(), "w14i reset boom") {
		t.Fatalf("截断重置提交失败应上抛: %v", err)
	}
}

func TestW14iIndexerIdentityChangeReplaceError(t *testing.T) {
	dir := t.TempDir()
	path := w14iCurrentLogFile(t, dir, "w14i content\n")
	fake := &w12dFakeStore{
		retentionDays: 7,
		findCursor:    &Cursor{LogFile: path, FileIdentity: "w14i-old-identity", CursorOffset: 0, FileSize: 0},
		replaceErr:    errors.New("w14i replace boom"),
	}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	err := indexer.RunOnce(w12dLeaseContext(context.Background()))
	if err == nil || !strings.Contains(err.Error(), "w14i replace boom") {
		t.Fatalf("identity 轮换 ReplaceCursor 失败应上抛: %v", err)
	}
}

func TestW14iIndexerIdentityCursorResetCommitError(t *testing.T) {
	dir := t.TempDir()
	path := w14iCurrentLogFile(t, dir, "w14i short\n")
	identity := w14iFileIdentity(t, path)
	fake := &w12dFakeStore{
		retentionDays: 7,
		findByIdentity: &Cursor{LogFile: "w14i/rotated/juhe-ai.log", FileIdentity: identity,
			CursorOffset: int64(9 * 1024), FileSize: int64(9 * 1024)},
		commitErr: errors.New("w14i identity reset boom"),
	}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	err := indexer.RunOnce(w12dLeaseContext(context.Background()))
	if err == nil || !strings.Contains(err.Error(), "w14i identity reset boom") {
		t.Fatalf("按 identity 截断重置提交失败应上抛: %v", err)
	}
}

func TestW14iIndexerIdentityCursorRelocateCopyError(t *testing.T) {
	dir := t.TempDir()
	path := w14iCurrentLogFile(t, dir, "w14i content\n")
	identity := w14iFileIdentity(t, path)
	fake := &w12dFakeStore{
		retentionDays: 7,
		findByIdentity: &Cursor{LogFile: "w14i/other/juhe-ai.log", FileIdentity: identity,
			CursorOffset: 0, FileSize: 0},
		copyErr: errors.New("w14i copy boom"),
	}
	indexer, _ := w14iIndexerConfig(t, dir, fake)
	err := indexer.RunOnce(w12dLeaseContext(context.Background()))
	if err == nil || !strings.Contains(err.Error(), "w14i copy boom") {
		t.Fatalf("游标搬迁 CopyCursor 失败应上抛: %v", err)
	}
}

// ---- Legacy 迁移补充臂 ----

func TestW14iMigrateLegacyMismatchFailsVerification(t *testing.T) {
	// 契约：目标库已存在同键不同值记录时，INSERT OR IGNORE 跳过复制，
	// 迁移后值校验必须 fail-closed。
	store, config := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	dataset := filepath.Join(t.TempDir(), "w14i-legacy-conflict.sqlite3")
	w12dCreateLegacyDataset(t, dataset, "2026-08-08T00:00:00.000Z")
	// 目标库预置同 id 不同值的记录，制造迁移后不一致。
	handle, err := sql.Open("sqlite", config.RuntimeLogDatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if _, err := handle.Exec(`INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at)
		VALUES ('w12d-legacy-1', 'conflicting.log', 999, 9, '2026-08-08T00:00:00.000Z', 'error', '', 'conflict', 'conflict', '', '{}', '2026-08-08T00:00:00.000Z')`); err != nil {
		t.Fatal(err)
	}
	var storeIface Store = store
	if err := MigrateLegacySQLite(testOwnerContext(t, storeIface), w12dMigrationConfig(t, dataset), storeIface); err == nil || !strings.Contains(err.Error(), "字段值不一致") {
		t.Fatalf("同键不同值应使迁移校验失败: %v", err)
	}
}

func TestW14iMigrateLegacyRejectsInvalidRuntimeLogPath(t *testing.T) {
	// 契约：RuntimeLogDatabasePath 非法（含 NUL）时，隔离校验必须报错。
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	var storeIface Store = store
	dataset := filepath.Join(t.TempDir(), "w14i-legacy-invalid-path.sqlite3")
	w12dCreateLegacyDataset(t, dataset, "2026-08-08T00:00:00.000Z")
	config := w12dMigrationConfig(t, dataset)
	config.RuntimeLogDatabasePath = "w14i-\x00-invalid.sqlite3"
	if err := MigrateLegacySQLite(context.Background(), config, storeIface); err == nil || !strings.Contains(err.Error(), "隔离") {
		t.Fatalf("非法路径应使隔离校验失败: %v", err)
	}
}

func TestW14iCanonicalSQLitePathDetectsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	dangling := filepath.Join(dir, "w14i-dangling.sqlite3")
	if err := os.Symlink(filepath.Join(dir, "w14i-missing-target.sqlite3"), dangling); err != nil {
		t.Skipf("w14i: 无法创建符号链接: %v", err)
	}
	if _, err := canonicalSQLitePath(dangling); err == nil {
		t.Fatalf("悬空符号链接应被拒绝")
	}
}

// ---- Lease 续租循环臂 ----

func TestW14iRunWithOwnerLeaseRenewalArms(t *testing.T) {
	// 契约：续租成功时循环继续；续租返回未持有（false）时必须以 lease
	// 丢失取消运行。
	store, config := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	renewConfig := config
	renewConfig.OwnerID = "w14i-renew-owner"
	renewConfig.OwnerLease = 30 * time.Millisecond
	// 关闭与续租 tick 存在固有的取消竞态（production defer Join 会把
	// context.Canceled 带回），这里以重试确认干净路径存在，其他终止只允许
	// 是该竞态本身。
	clean := false
	var lastErr error
	for attempt := 0; attempt < 6 && !clean; attempt++ {
		err := RunWithOwnerLease(context.Background(), renewConfig, store, func(ctx context.Context) error {
			time.Sleep(85 * time.Millisecond)
			return nil
		})
		if err == nil {
			clean = true
			break
		}
		if !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("续租循环终止原因异常: %v", err)
		}
		lastErr = err
	}
	if !clean {
		t.Fatalf("续租循环始终以取消竞态结束: %v", lastErr)
	}
	lost := &w14iLostRenewStore{w12dFakeStore: w12dFakeStore{retentionDays: 7}}
	lostErr := RunWithOwnerLease(context.Background(), renewConfig, lost, func(ctx context.Context) error {
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	if lostErr == nil {
		t.Fatalf("续租丢失应产生错误")
	}
}

// w14iLostRenewStore 模拟续租时 lease 已被其他实例接走。
type w14iLostRenewStore struct {
	w12dFakeStore
}

func (s *w14iLostRenewStore) AcquireOwnerLease(ctx context.Context, ownerID string, duration time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{OwnerID: ownerID, FenceToken: 41}, true, nil
}

func (s *w14iLostRenewStore) RenewOwnerLease(ctx context.Context, lease OwnerLease, duration time.Duration) (bool, error) {
	return false, nil
}

// openSQLiteStoreAtPath 以指定运行日志库路径打开 sqliteStore。business 库
// 先按 openTestSQLiteStore 同样的最小 schema 预创建。
func openSQLiteStoreAtPath(t *testing.T, rlPath string) (*sqliteStore, Config, error) {
	t.Helper()
	businessPath := filepath.Join(t.TempDir(), "w14i-business.sqlite3")
	business, err := sql.Open("sqlite", businessPath)
	if err != nil {
		return nil, Config{}, err
	}
	if _, err := business.Exec(`CREATE TABLE IF NOT EXISTS system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`); err != nil {
		business.Close()
		return nil, Config{}, err
	}
	business.Close()
	config := Config{
		Mode:                   ModeSQLite,
		RuntimeLogDatabasePath: rlPath,
		BusinessPath:           businessPath,
		LogDirectory:           t.TempDir(),
		PollInterval:           time.Hour,
		RetentionInterval:      time.Hour,
		RetentionDays:          7,
		BatchSize:              16,
	}
	opened, err := OpenStore(context.Background(), config)
	if err != nil {
		return nil, config, err
	}
	store, ok := opened.(*sqliteStore)
	if !ok {
		_ = opened.Close()
		return nil, config, errors.New("unexpected store type")
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, config, nil
}
