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

// wn_gaps_test.go 覆盖 SQLite store、配置解析、日志行解析、旧 SQLite 迁移
// 与文件清理中的剩余分支。业务契约与既有测试保持一致。

func TestSQLiteStoreVerifyOwnerLeaseRejectsStaleToken(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	ctx := context.Background()
	lease, acquired, err := store.AcquireOwnerLease(ctx, "verify-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("获取 lease 失败: lease=%#v acquired=%t err=%v", lease, acquired, err)
	}
	t.Cleanup(func() {
		if err := store.ReleaseOwnerLease(ctx, lease); err != nil && !errors.Is(err, ErrOwnerLeaseFenced) {
			t.Errorf("清理 lease 失败: %v", err)
		}
	})
	if err := store.VerifyOwnerLease(ctx, lease); err != nil {
		t.Fatalf("有效 lease 校验必须通过: %v", err)
	}
	stale := OwnerLease{OwnerID: "verify-owner", FenceToken: lease.FenceToken - 1}
	if err := store.VerifyOwnerLease(ctx, stale); !errors.Is(err, ErrOwnerLeaseFenced) {
		t.Fatalf("过期 fence token 校验必须失败: %v", err)
	}
}

func TestSQLiteStoreCommitRejectsInvalidRecordBeforeWrite(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)
	lease := testOwnerLease(t, ctx)
	cursor := Cursor{LogFile: filepath.Join(config.LogDirectory, "juhe-ai.log"), FileIdentity: "invalid:1:1"}
	if err := store.Commit(ctx, lease, []Record{{ID: "bad-time", Time: "garbage", CreatedAt: "2026-08-08T00:00:00.000Z", RawJSON: "{}"}}, cursor, time.Now()); err == nil {
		t.Fatal("非法 time 的记录必须在写库前被拒绝")
	}
	assertRuntimeLogCount(t, store, 0)
	if err := store.Commit(ctx, lease, []Record{{ID: "bad-created", Time: "2026-08-08T00:00:00.000Z", CreatedAt: "garbage", RawJSON: "{}"}}, cursor, time.Now()); err == nil {
		t.Fatal("非法 createdAt 的记录必须在写库前被拒绝")
	}
	assertRuntimeLogCount(t, store, 0)
}

func TestSQLiteStoreRuntimeRetentionDaysFallbackAndErrors(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := context.Background()

	// businessDB 为空时必须直接回退（PG 模式对齐语义）。
	withoutBusiness := &sqliteStore{db: store.db}
	days, err := withoutBusiness.RuntimeRetentionDays(ctx, 9)
	if err != nil || days != 9 {
		t.Fatalf("无业务库必须回退默认保留期: days=%d err=%v", days, err)
	}

	// 空设置表回退默认值。
	emptyBusiness := filepath.Join(t.TempDir(), "empty.sqlite")
	emptyDB, err := sql.Open("sqlite", emptyBusiness)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyDB.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatal(err)
	}
	if err := emptyDB.Close(); err != nil {
		t.Fatal(err)
	}
	config.BusinessPath = emptyBusiness
	businessStore := &sqliteStore{db: store.db, businessDB: openReadOnlyBusiness(t, emptyBusiness)}
	t.Cleanup(func() { _ = businessStore.businessDB.Close() })
	if days, err = businessStore.RuntimeRetentionDays(ctx, 5); err != nil || days != 5 {
		t.Fatalf("空设置表必须回退默认保留期: days=%d err=%v", days, err)
	}

	// 查询失败必须暴露（只读连接关闭后查询立即报错）。
	if err := businessStore.businessDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = businessStore.RuntimeRetentionDays(ctx, 5); err == nil {
		t.Fatal("业务设置查询失败必须返回错误")
	}
}

func openReadOnlyBusiness(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := openSQLiteReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestNormalizeLevelCoversNodeLevels(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: `"ERROR"`, want: "error"},
		{raw: `60`, want: "fatal"},
		{raw: `50`, want: "error"},
		{raw: `40`, want: "warn"},
		{raw: `30`, want: "info"},
		{raw: `20`, want: "debug"},
		{raw: `10`, want: "trace"},
		{raw: `5`, want: "trace"},
		{raw: `null`, want: "trace"},
		{raw: `[30]`, want: "info"},
		{raw: ``, want: "info"},
	}
	for _, test := range tests {
		if got := normalizeLevel([]byte(test.raw)); got != test.want {
			t.Fatalf("normalizeLevel(%s) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestParseLineFieldFallbacks(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	record := ParseLine(`{"time":"2026-09-01T00:00:00.000Z","level":"INFO","message":"from-message","err":{"message":"err-message"}}`, LineOptions{Now: func() time.Time { return now }})
	if record == nil {
		t.Fatal("JSON 对象行必须生成记录")
	}
	if record.Message != "from-message" {
		t.Fatalf("msg 缺失时必须回退 message 字段: %q", record.Message)
	}
	if record.ErrorMessage != "err-message" {
		t.Fatalf("errorMessage 必须读取 err.message: %q", record.ErrorMessage)
	}

	emptyErr := ParseLine(`{"time":"2026-09-01T00:00:00.000Z","err":{"other":1}}`, LineOptions{Now: func() time.Time { return now }})
	if emptyErr == nil || emptyErr.ErrorMessage != "" {
		t.Fatalf("err 缺少 message 时 errorMessage 必须为空: %#v", emptyErr)
	}
	stringErr := ParseLine(`{"time":"2026-09-01T00:00:00.000Z","err":"plain"}`, LineOptions{Now: func() time.Time { return now }})
	if stringErr == nil || stringErr.ErrorMessage != "" {
		t.Fatalf("err 为字符串时 errorMessage 必须为空: %#v", stringErr)
	}

	// SourceKey 缺省时 stable id 以整行为键；Now 缺省时使用真实时间并保持 RFC3339。
	defaultKey := ParseLine("fallback-line", LineOptions{})
	if defaultKey == nil {
		t.Fatal("回退行必须生成记录")
	}
	if defaultKey.ID != stableID("fallback-line") {
		t.Fatalf("无 SourceKey 时 stable id 必须以整行为键: %s", defaultKey.ID)
	}
	if _, err := normalizeNodeTimestamp(defaultKey.CreatedAt); err != nil {
		t.Fatalf("默认 now 生成的 CreatedAt 必须是合法 RFC3339: %q err=%v", defaultKey.CreatedAt, err)
	}

	if ParseLine("   \t ", LineOptions{}) != nil {
		t.Fatal("空白行不得生成记录")
	}
}

func TestRangeTimesAndFacetRowsFrom(t *testing.T) {
	rows := []facetRow{
		{Time: "2026-08-02T00:00:00.000Z"},
		{Time: "2026-08-01T00:00:00.000Z"},
		{Time: "2026-08-03T00:00:00.000Z"},
	}
	earliest, latest := rangeTimes(rows)
	if earliest != "2026-08-01T00:00:00.000Z" || latest != "2026-08-03T00:00:00.000Z" {
		t.Fatalf("rangeTimes 计算错误: earliest=%q latest=%q", earliest, latest)
	}
	if got := facetRowsFrom(rows, ""); len(got) != 3 {
		t.Fatalf("空 earliest 计数基线必须保留全部行: %d", len(got))
	}
	counted := facetRowsFrom(rows, "2026-08-02T00:00:00.000Z")
	if len(counted) != 2 {
		t.Fatalf("计数基线必须过滤早于基线的行: %#v", counted)
	}
}

func TestFacetEventCountsSkipsBlankEvents(t *testing.T) {
	counts := facetEventCounts([]facetRow{
		{Time: "2026-08-01T00:00:00.000Z", Event: ""},
		{Time: "2026-08-02T00:00:00.000Z", Event: "  "},
		{Time: "2026-08-03T00:00:00.000Z", Event: "login"},
	})
	if len(counts) != 1 {
		t.Fatalf("空白 event 不得计入聚合: %#v", counts)
	}
	if counts["login"].count != 1 || counts["login"].latestTime != "2026-08-03T00:00:00.000Z" {
		t.Fatalf("event 聚合不正确: %#v", counts["login"])
	}
}

func TestConfigValueParsers(t *testing.T) {
	if _, err := parseBool("X", "not-bool", true); err == nil || !strings.Contains(err.Error(), "X") {
		t.Fatalf("非法布尔值必须报错: %v", err)
	}
	if _, err := parseBool("X", "false", true); err != nil {
		t.Fatalf("显式布尔值必须生效: %v", err)
	}
	if _, err := durationOrDefault("X", "0s", time.Second); err == nil {
		t.Fatal("非正 duration 必须报错")
	}
	if _, err := intOrDefault("X", "abc", 3, 1, 10); err == nil {
		t.Fatal("非整数必须报错")
	}
	if value, err := intOrDefault("X", "", 3, 1, 10); err != nil || value != 3 {
		t.Fatalf("缺省值必须生效: %d %v", value, err)
	}
	if _, err := intOrDefault("X", "0", 3, 1, 10); err == nil {
		t.Fatal("低于下界必须报错")
	}
	if _, err := intOrDefault("X", "11", 3, 1, 10); err == nil {
		t.Fatal("高于上界必须报错")
	}
	if _, err := positiveIntOrDefault("X", "-1", 3); err == nil {
		t.Fatal("负连接数必须报错")
	}
	if value, err := positiveIntOrDefault("X", "", 3); err != nil || value != 3 {
		t.Fatalf("连接数缺省值必须生效: %d %v", value, err)
	}
	if mode, err := parseStoreMode(func(name string) string {
		if name == "JUHE_AI_RUNTIME_LOG_STORE" {
			return " postgres "
		}
		return ""
	}); err != nil || mode != ModePostgres {
		t.Fatalf("postgres 模式必须可解析: %s %v", mode, err)
	}
	if _, err := parseStoreMode(func(name string) string {
		if name == "JUHE_AI_RUNTIME_LOG_STORE" {
			return "bogus"
		}
		return ""
	}); err == nil {
		t.Fatal("未知 Store 模式必须报错")
	}
	if mode, err := parseStoreMode(func(string) string { return "" }); err != nil || mode != ModeSQLite {
		t.Fatalf("缺省 Store 必须跟随默认 sqlite 驱动: %s %v", mode, err)
	}
	if mode, err := parseStoreMode(func(name string) string {
		if name == "JUHE_AI_DATABASE_DRIVER" {
			return "postgres"
		}
		return ""
	}); err != nil || mode != ModePostgres {
		t.Fatalf("缺省 Store 必须跟随 postgres 驱动: %s %v", mode, err)
	}
}

func TestLoadConfigPostgresModeAndRejects(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":  "inst-1",
		"JUHE_AI_RUNTIME_LOG_STORE":        "postgres",
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL": "postgres://jobs:secret@127.0.0.1:5432/db?sslmode=disable",
		"JUHE_AI_LOG_DIR":                  "logs",
	}
	config, err := LoadConfig(func(name string) string { return base[name] })
	if err != nil {
		t.Fatalf("postgres 模式合法配置必须通过: %v", err)
	}
	if config.Mode != ModePostgres || config.PostgresURL == "" || !config.FileEnabled || config.Once {
		t.Fatalf("postgres 配置解析不正确: %#v", config)
	}
	if config.OwnerID == "" || !strings.HasPrefix(config.OwnerID, "inst-1:") {
		t.Fatalf("OwnerID 必须由实例 ID 与进程号组成: %q", config.OwnerID)
	}

	missingURL := map[string]string{}
	for name, value := range base {
		missingURL[name] = value
	}
	delete(missingURL, "JUHE_AI_RUNTIME_LOG_POSTGRES_URL")
	if _, err := LoadConfig(func(name string) string { return missingURL[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_POSTGRES_URL") {
		t.Fatalf("postgres 模式缺少 URL 必须拒绝: %v", err)
	}

	fileDisabled := map[string]string{}
	for name, value := range base {
		fileDisabled[name] = value
	}
	fileDisabled["JUHE_AI_LOG_FILE_ENABLED"] = "false"
	if _, err := LoadConfig(func(name string) string { return fileDisabled[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_LOG_FILE_ENABLED") {
		t.Fatalf("文件索引关闭必须拒绝启动: %v", err)
	}
	fileDisabled["JUHE_AI_LOG_FILE_ENABLED"] = "not-bool"
	if _, err := LoadConfig(func(name string) string { return fileDisabled[name] }); err == nil {
		t.Fatalf("非法布尔值必须拒绝启动: %v", err)
	}

	onceInvalid := map[string]string{}
	for name, value := range base {
		onceInvalid[name] = value
	}
	onceInvalid["JUHE_AI_RUNTIME_LOG_ONCE"] = "maybe"
	if _, err := LoadConfig(func(name string) string { return onceInvalid[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_ONCE") {
		t.Fatalf("非法 ONCE 值必须拒绝: %v", err)
	}

	missingLogDir := map[string]string{}
	for name, value := range base {
		missingLogDir[name] = value
	}
	delete(missingLogDir, "JUHE_AI_LOG_DIR")
	// 缺省 LOG_DIR 改为派生 <DATA_DIR>/logs 并代建目录（2026-09-19 零配置
	// 决策）；DATA_DIR 指向临时目录，验证派生与创建且不污染包目录。
	dataRoot := t.TempDir()
	missingLogDir["JUHE_AI_DATA_DIR"] = dataRoot
	derivedConfig, deriveErr := LoadConfig(func(name string) string { return missingLogDir[name] })
	if deriveErr != nil {
		t.Fatalf("缺省 LOG_DIR 必须从 DATA_DIR 派生: %v", deriveErr)
	}
	expectedLogDir := filepath.Join(dataRoot, "logs")
	if derivedConfig.LogDirectory != expectedLogDir {
		t.Fatalf("LOG_DIR 必须派生为 <DATA_DIR>/logs: %q", derivedConfig.LogDirectory)
	}
	if _, statErr := os.Stat(expectedLogDir); statErr != nil {
		t.Fatalf("派生日志目录必须被创建: %v", statErr)
	}

	badLease := map[string]string{}
	for name, value := range base {
		badLease[name] = value
	}
	badLease["JUHE_AI_RUNTIME_LOG_OWNER_LEASE"] = "-1s"
	if _, err := LoadConfig(func(name string) string { return badLease[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_OWNER_LEASE") {
		t.Fatalf("非正 owner lease 必须拒绝: %v", err)
	}

	tooManyMinConns := map[string]string{}
	for name, value := range base {
		tooManyMinConns[name] = value
	}
	tooManyMinConns["JUHE_AI_RUNTIME_LOG_POSTGRES_MIN_CONNS"] = "11"
	if _, err := LoadConfig(func(name string) string { return tooManyMinConns[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_POSTGRES_MIN_CONNS") {
		t.Fatalf("最小连接数超过平台上限必须拒绝: %v", err)
	}

	outOfRangeRetention := map[string]string{}
	for name, value := range base {
		outOfRangeRetention[name] = value
	}
	outOfRangeRetention["JUHE_AI_RUNTIME_LOG_RETENTION_DAYS"] = "91"
	if _, err := LoadConfig(func(name string) string { return outOfRangeRetention[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_RUNTIME_LOG_RETENTION_DAYS") {
		t.Fatalf("保留天数越界必须拒绝: %v", err)
	}
}

func TestCheckSQLiteColumnsReportsMissingColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec("CREATE TABLE runtime_logs (id TEXT)"); err != nil {
		t.Fatal(err)
	}
	err = checkSQLiteColumns(context.Background(), db, "runtime_logs")
	if err == nil || !strings.Contains(err.Error(), "缺少运行日志字段") {
		t.Fatalf("缺少字段的表必须报可诊断错误: %v", err)
	}
}

func TestSQLiteCleanupBoundaryPaths(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	lease := testOwnerLease(t, testOwnerContext(t, store))
	ctx := context.Background()
	cutoff := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)

	// 无过期记录时必须直接返回零结果。
	result, err := store.Cleanup(ctx, lease, cutoff, 10, 3)
	if err != nil || result.RuntimeLogs != 0 || result.RuntimeLogCursors != 0 {
		t.Fatalf("空库清理必须返回零结果: %#v err=%v", result, err)
	}

	// 未满批的过期记录删除后必须立即结束批次循环。
	old := nodeISO(cutoff.Add(-time.Hour))
	if err := store.Commit(ctx, lease, []Record{{ID: "only-one", Time: old, Level: "info", Event: "cleanup", RawJSON: "{}", CreatedAt: old}},
		Cursor{LogFile: filepath.Join(config.LogDirectory, "juhe-ai.log"), FileIdentity: "current:fixture", CursorOffset: 1, FileSize: 1, LastReadAt: old, CreatedAt: old}, cutoff); err != nil {
		t.Fatal(err)
	}
	result, err = store.Cleanup(ctx, lease, cutoff, 100, 3)
	if err != nil || result.RuntimeLogs != 1 {
		t.Fatalf("单条过期记录必须一次清理完成: %#v err=%v", result, err)
	}

	// maxBatches=0 时不得进入任何批次循环。
	result, err = store.Cleanup(ctx, lease, cutoff, 10, 0)
	if err != nil || result.RuntimeLogs != 0 {
		t.Fatalf("maxBatches=0 必须跳过清理: %#v err=%v", result, err)
	}
}

func TestSQLiteCleanupSkipsCursorBatchWithoutCandidates(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	lease := testOwnerLease(t, testOwnerContext(t, store))
	ctx := context.Background()
	cutoff := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	old := nodeISO(cutoff.Add(-time.Hour))
	cursor := Cursor{LogFile: filepath.Join(config.LogDirectory, "juhe-ai.log"), FileIdentity: "current:fixture", CursorOffset: 1, FileSize: 1, LastReadAt: old, CreatedAt: old}
	// 只有 current cursor（不应被清理）与带错误的 rotated cursor（不满足清理条件）。
	if err := store.CopyCursor(ctx, lease, cursor); err != nil {
		t.Fatal(err)
	}
	if err := store.CopyCursor(ctx, lease, Cursor{LogFile: filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log"), FileIdentity: "rotated:fixture", CursorOffset: 1, FileSize: 1, LastReadAt: old, CreatedAt: old, LastErrorMessage: "保留失败的 cursor"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE runtime_log_file_cursors SET updated_at = ?", old); err != nil {
		t.Fatal(err)
	}
	result, err := store.Cleanup(ctx, lease, cutoff, 10, 2)
	if err != nil || result.RuntimeLogCursors != 0 {
		t.Fatalf("无候选 cursor 必须跳过 cursor 批: %#v err=%v", result, err)
	}
}

func TestSQLiteCleanupSkipsRecordsBelowCountedBaseline(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	lease := testOwnerLease(t, testOwnerContext(t, store))
	ctx := context.Background()
	cutoff := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	old := nodeISO(cutoff.Add(-time.Hour))
	cursor := Cursor{LogFile: filepath.Join(config.LogDirectory, "juhe-ai.log"), FileIdentity: "current:fixture", CursorOffset: 1, FileSize: 1, LastReadAt: old, CreatedAt: old}
	if err := store.Commit(ctx, lease, []Record{{ID: "below-baseline", Time: old, Level: "info", Event: "cleanup", RawJSON: "{}", CreatedAt: old}},
		cursor, time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	// facet 汇总基线晚于被删记录：已删记录不得再扣减 facet。
	future := nodeISO(cutoff.Add(time.Hour))
	if _, err := store.db.Exec("UPDATE runtime_log_facet_summary SET earliest_time = ? WHERE bucket_key = ?", future, facetBucketKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cleanup(ctx, lease, cutoff, 10, 1); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := store.db.QueryRow("SELECT total_count FROM runtime_log_facet_summary WHERE bucket_key = ?", facetBucketKey).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("低于计数基线的记录不得扣减 facet 汇总: total=%d", total)
	}
}

func TestOpenStoreSQLiteRejectsMissingPaths(t *testing.T) {
	if _, err := OpenStore(context.Background(), Config{Mode: ModeSQLite, RuntimeLogDatabasePath: " "}); err == nil {
		t.Fatal("缺少运行日志 SQLite 路径必须拒绝启动")
	}
	missingBusiness := filepath.Join(t.TempDir(), "runtime-log.sqlite")
	_, err := OpenStore(context.Background(), Config{Mode: ModeSQLite, RuntimeLogDatabasePath: missingBusiness, BusinessPath: filepath.Join(t.TempDir(), "missing.sqlite")})
	if err == nil || !strings.Contains(err.Error(), "只读数据源") {
		t.Fatalf("业务库不可访问必须拒绝启动: %v", err)
	}
}

func TestOpenSQLiteReadOnlyReportsMissingSource(t *testing.T) {
	_, err := openSQLiteReadOnly(context.Background(), filepath.Join(t.TempDir(), "missing.sqlite"))
	if err == nil {
		t.Fatal("缺失只读数据源必须报错")
	}
}

func TestOwnerLeaseContextHelpers(t *testing.T) {
	if _, err := ownerLeaseFromContext(context.Background()); err == nil {
		t.Fatal("无 lease 上下文必须报错")
	}
	lease, err := ownerLeaseFromContext(withOwnerLease(context.Background(), OwnerLease{OwnerID: "owner", FenceToken: 3}))
	if err != nil || lease.FenceToken != 3 {
		t.Fatalf("lease 上下文必须可还原: %#v err=%v", lease, err)
	}
	if _, err := ownerLeaseFromContext(withOwnerLease(context.Background(), OwnerLease{FenceToken: 3})); err == nil {
		t.Fatal("缺少 owner 的 lease 必须报错")
	}
	if _, err := ownerLeaseFromContext(withOwnerLease(context.Background(), OwnerLease{OwnerID: "owner"})); err == nil {
		t.Fatal("缺少 fence token 的 lease 必须报错")
	}
}

type stubLeaseStore struct {
	Store
	acquireErr    error
	acquireLost   bool
	releasePanics bool
}

func (store *stubLeaseStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	if store.acquireErr != nil {
		return OwnerLease{}, false, store.acquireErr
	}
	return OwnerLease{}, !store.acquireLost, nil
}

func (store *stubLeaseStore) ReleaseOwnerLease(context.Context, OwnerLease) error {
	if store.releasePanics {
		panic("release lease fixture panic")
	}
	return nil
}

func TestRunWithOwnerLeaseReportsAcquireFailures(t *testing.T) {
	config := Config{OwnerID: "stub-owner", OwnerLease: time.Minute}
	run := func(context.Context) error { return nil }

	err := RunWithOwnerLease(context.Background(), config, &stubLeaseStore{acquireErr: errors.New("acquire fixture failure")}, run)
	if err == nil || !strings.Contains(err.Error(), "acquire fixture failure") {
		t.Fatalf("获取 lease 失败必须返回错误: %v", err)
	}
	err = RunWithOwnerLease(context.Background(), config, &stubLeaseStore{acquireLost: true}, run)
	if err == nil || !strings.Contains(err.Error(), "已由另一个 Go 实例持有") {
		t.Fatalf("lease 被占用必须返回错误: %v", err)
	}
	err = RunWithOwnerLease(context.Background(), config, &stubLeaseStore{releasePanics: true}, run)
	if err == nil || !strings.Contains(err.Error(), "释放运行日志 owner lease panic") {
		t.Fatalf("释放 panic 必须转换为错误: %v", err)
	}
}

func TestRunRetentionRejectsStaleLease(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	// 不持有 lease 的上下文必须直接失败。
	indexer := NewIndexer(Config{BatchSize: 1}, store)
	if err := indexer.RunRetention(context.Background()); err == nil {
		t.Fatal("缺少 owner lease 必须拒绝保留清理")
	}
}

func TestRemoveRotatedLogFileHandlesMissingAndCanceled(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	lease := testOwnerLease(t, testOwnerContext(t, store))
	if err := removeRotatedLogFile(context.Background(), store, lease, filepath.Join(t.TempDir(), "already-gone.log")); err != nil {
		t.Fatalf("不存在的轮转文件必须视为已删除: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := removeRotatedLogFile(ctx, store, lease, filepath.Join(t.TempDir(), "target.log")); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消的上下文必须中止删除: %v", err)
	}
}

func TestCleanupRotatedFilesHonorsMaxFilesAndIdentityFallback(t *testing.T) {
	t.Run("maxRotatedFilesZeroDeletesFreshRotated", func(t *testing.T) {
		store, config := openTestSQLiteStore(t)
		ctx := testOwnerContext(t, store)
		currentPath := filepath.Join(config.LogDirectory, "juhe-ai.log")
		rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
		writeTestFile(t, currentPath, logLine("current", "2026-08-08T00:00:00.000Z")+"\n")
		writeTestFile(t, rotatedPath, logLine("rotated", "2026-08-08T00:00:01.000Z")+"\n")
		config.LogMaxFiles = 1
		indexer := NewIndexer(config, store)
		if err := indexer.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		deleted, err := indexer.cleanupRotatedFiles(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 1 {
			t.Fatalf("配额为 0 时新轮转文件也必须删除，实际删除 %d", deleted)
		}
		if _, err := os.Stat(rotatedPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("超配额轮转文件未删除: %v", err)
		}
	})

	t.Run("identityCursorFallbackProtectsIncompleteFile", func(t *testing.T) {
		store, config := openTestSQLiteStore(t)
		ctx := testOwnerContext(t, store)
		rotatedPath := filepath.Join(config.LogDirectory, "juhe-ai.20260721T121500Z.a1b2.log")
		writeTestFile(t, rotatedPath, logLine("archived", "2026-08-08T00:00:00.000Z")+"\n")
		old := time.Now().AddDate(0, 0, -config.LogRetentionDays-1)
		if err := os.Chtimes(rotatedPath, old, old); err != nil {
			t.Fatal(err)
		}
		indexer := NewIndexer(config, store)
		if err := indexer.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		// 把 cursor 行改写为 displaced identity 形态，模拟直接路径 cursor 丢失。
		var identity string
		if err := store.db.QueryRow("SELECT file_identity FROM runtime_log_file_cursors WHERE log_file = ?", rotatedPath).Scan(&identity); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec("UPDATE runtime_log_file_cursors SET log_file = ? WHERE log_file = ?", displacedIdentityPrefix+identity, rotatedPath); err != nil {
			t.Fatal(err)
		}
		deleted, err := indexer.cleanupRotatedFiles(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if deleted != 1 {
			t.Fatalf("identity cursor 命中且已索引完成的轮转文件必须删除，实际删除 %d", deleted)
		}
		if _, err := os.Stat(rotatedPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("identity fallback 后轮转文件未删除: %v", err)
		}
	})
}

func TestSameSQLitePathVariants(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "a.sqlite")
	if err := os.WriteFile(left, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	same, err := sameSQLitePath(left, filepath.Join(root, ".", "a.sqlite"))
	if err != nil || !same {
		t.Fatalf("同文件的相对写法必须判定相同: same=%t err=%v", same, err)
	}
	other := filepath.Join(root, "b.sqlite")
	if err := os.WriteFile(other, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if same, err = sameSQLitePath(left, other); err != nil || same {
		t.Fatalf("不同文件必须判定不同: same=%t err=%v", same, err)
	}
	if _, err = sameSQLitePath("", left); err == nil {
		t.Fatal("空路径必须报错")
	}
	if _, err = sameSQLitePath(left, ""); err == nil {
		t.Fatal("空路径必须报错")
	}
	// 两个都不存在但绝对路径相同的场景走 canonical 分支。
	missing := filepath.Join(root, "missing.sqlite")
	if same, err = sameSQLitePath(missing, filepath.Join(root, "missing.sqlite")); err != nil || !same {
		t.Fatalf("同路径的不存在文件必须判定相同: same=%t err=%v", same, err)
	}
	if same, err = sameSQLitePath(missing, filepath.Join(root, "missing2.sqlite")); err != nil || same {
		t.Fatalf("不同路径的不存在文件必须判定不同: same=%t err=%v", same, err)
	}
}

func TestSQLitePathWithinVariants(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "state.sqlite")
	within, err := sqlitePathWithin(root, inside)
	if err != nil || !within {
		t.Fatalf("根内路径必须判定在内: within=%t err=%v", within, err)
	}
	within, err = sqlitePathWithin(root, root)
	if err != nil || !within {
		t.Fatalf("根自身必须判定在内: within=%t err=%v", within, err)
	}
	within, err = sqlitePathWithin(root, filepath.Join(root, "..", "outside.sqlite"))
	if err != nil || within {
		t.Fatalf("根外路径必须判定在外: within=%t err=%v", within, err)
	}
	if _, err = sqlitePathWithin("", inside); err == nil {
		t.Fatal("空根路径必须报错")
	}
}

func TestEqualSQLitePathCaseInsensitiveOnWindows(t *testing.T) {
	if !equalSQLitePath(filepath.Clean(`C:\A\b.sqlite`), filepath.Clean(`c:\a\B.SQLITE`)) {
		t.Fatal("Windows 下路径比较必须忽略大小写")
	}
	if equalSQLitePath("x.sqlite", "y.sqlite") {
		t.Fatal("不同路径不得判定相等")
	}
}

func TestCanonicalSQLitePathResolvesParentChains(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "not-created.sqlite")
	canonical, err := canonicalSQLitePath(missing)
	if err != nil || filepath.IsAbs(canonical) == false {
		t.Fatalf("未创建文件必须返回规范绝对路径: %q err=%v", canonical, err)
	}
	// TempDir 可能返回 8.3 短路径，与 EvalSymlinks 后的真实父目录比较。
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !equalSQLitePath(canonical, filepath.Join(realRoot, "not-created.sqlite")) {
		t.Fatalf("未创建文件必须落在真实父目录下: got=%q want=%q", canonical, filepath.Join(realRoot, "not-created.sqlite"))
	}
	if _, err = canonicalSQLitePath("   "); err == nil {
		t.Fatal("空路径必须报错")
	}
	// 父路径是文件时必须 fail-closed。
	filePath := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = canonicalSQLitePath(filepath.Join(filePath, "child.sqlite")); err == nil {
		t.Fatal("父路径不是目录必须报错")
	}
}

func TestDanglingSQLiteSymlinkDetection(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "dangling.sqlite")
	target := filepath.Join(root, "missing-target.sqlite")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("当前环境不能创建悬空 symlink fixture: %v", err)
	}
	t.Cleanup(func() {
		// Windows 在目标缺失时可能无法删除悬空 symlink，先落地目标再清理。
		_ = os.WriteFile(target, []byte("cleanup"), 0o600)
		_ = os.Remove(link)
	})
	// 无论 Lstat 还是 EvalSymlinks 命中，悬空 symlink 都必须 fail-closed。
	if _, err := canonicalSQLitePath(link); err == nil {
		t.Fatal("悬空 symlink 必须 fail-closed")
	}
	if !danglingSQLiteSymlink(link) {
		t.Fatal("悬空 symlink 必须被识别")
	}
	if danglingSQLiteSymlink(filepath.Join(root, "not-listed.sqlite")) {
		t.Fatal("目录中不存在的名称不得判定为 symlink")
	}
}

func TestMigrateLegacySQLiteRejectsInvalidInputs(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	ctx := testOwnerContext(t, store)

	if err := MigrateLegacySQLite(ctx, Config{Mode: ModePostgres}, store); err == nil || !strings.Contains(err.Error(), "仅能迁移到 sqlite Store") {
		t.Fatalf("非 sqlite 模式必须拒绝迁移: %v", err)
	}
	if err := MigrateLegacySQLite(ctx, Config{Mode: ModeSQLite}, store); err == nil || !strings.Contains(err.Error(), "JUHE_AI_DATASET_DATABASE_PATH") {
		t.Fatalf("缺少数据源路径必须拒绝迁移: %v", err)
	}
	sameConfig := config
	sameConfig.DatasetPath = config.RuntimeLogDatabasePath
	if err := MigrateLegacySQLite(ctx, sameConfig, store); err == nil || !strings.Contains(err.Error(), "共用文件") {
		t.Fatalf("与专用库共用文件必须拒绝迁移: %v", err)
	}
	missingConfig := config
	missingConfig.DatasetPath = filepath.Join(t.TempDir(), "missing.sqlite")
	if err := MigrateLegacySQLite(ctx, missingConfig, store); err == nil || !strings.Contains(err.Error(), "无法访问旧运行日志 SQLite 数据源") {
		t.Fatalf("数据源不可访问必须拒绝迁移: %v", err)
	}
	if err := MigrateLegacySQLite(ctx, config, stubStore{Store: store}); err == nil || !strings.Contains(err.Error(), "需要 sqlite Store") {
		t.Fatalf("非 sqlite Store 必须拒绝迁移: %v", err)
	}
	if err := MigrateLegacySQLite(context.Background(), config, store); err == nil || !strings.Contains(err.Error(), "owner fence token") {
		t.Fatalf("缺少 owner lease 必须拒绝迁移: %v", err)
	}
}

type stubStore struct{ Store }

// 数据源缺表时迁移必须失败（verifyLegacyTables 分支）。
func TestMigrateLegacySQLiteRejectsMissingLegacyTables(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	legacy, err := sql.Open("sqlite", config.DatasetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(sqliteSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec("DROP TABLE runtime_log_event_facets"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MigrateLegacySQLite(testOwnerContext(t, store), config, store); err == nil || !strings.Contains(err.Error(), "缺少表 runtime_log_event_facets") {
		t.Fatalf("旧库缺表必须拒绝迁移: %v", err)
	}
}

// createNullableLegacyDataset 构造一张列全部可空的旧 dataset 库，
// 便于构造 NOT NULL 约束挡住的 NULL fixture。
func createNullableLegacyDataset(t *testing.T, path string) {
	t.Helper()
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = legacy.Close() })
	if _, err := legacy.Exec(`
CREATE TABLE runtime_logs (id TEXT, log_file TEXT, log_offset INTEGER, line_number INTEGER, time TEXT, level TEXT, trace_id TEXT, event TEXT, message TEXT, error_message TEXT, raw_json TEXT, created_at TEXT);
CREATE TABLE runtime_log_file_cursors (log_file TEXT, file_identity TEXT, cursor_offset INTEGER, line_number INTEGER, file_size INTEGER, truncation_generation INTEGER, file_mtime_ms INTEGER, last_read_at TEXT, last_error_message TEXT, created_at TEXT, updated_at TEXT);
CREATE TABLE runtime_log_facet_summary (bucket_key TEXT, total_count INTEGER, earliest_time TEXT, latest_time TEXT, updated_at TEXT);
CREATE TABLE runtime_log_level_facets (bucket_key TEXT, level TEXT, count INTEGER, updated_at TEXT);
CREATE TABLE runtime_log_event_facets (bucket_key TEXT, event TEXT, count INTEGER, latest_time TEXT, updated_at TEXT);
`); err != nil {
		t.Fatal(err)
	}
}

// 可选绝对时间列为 NULL 的旧库必须迁移成功；必填列为 NULL 必须拒绝。
func TestMigrateLegacySQLiteInstantColumnNullability(t *testing.T) {
	t.Run("nullable optional columns accepted", func(t *testing.T) {
		store, config := openTestSQLiteStore(t)
		// openTestSQLiteStore 已预建 dataset 文件，可空 schema 使用全新文件。
		config.DatasetPath = filepath.Join(t.TempDir(), "nullable-legacy.sqlite")
		createNullableLegacyDataset(t, config.DatasetPath)
		legacy, err := sql.Open("sqlite", config.DatasetPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.Exec(`
INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, raw_json, created_at)
VALUES ('legacy-null-optional', 'juhe-ai.log', 1, 1, '2026-08-09T00:00:00.000Z', 'info', '{}', '2026-08-09T00:00:00.000Z');
INSERT INTO runtime_log_file_cursors (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at)
VALUES ('juhe-ai.log', 'legacy:1:1', 1, 1, 1, 0, 0, NULL, NULL, '2026-08-09T00:00:00.000Z', '2026-08-09T00:00:00.000Z');
INSERT INTO runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at)
VALUES ('current', 1, NULL, NULL, '2026-08-09T00:00:00.000Z');
INSERT INTO runtime_log_level_facets (bucket_key, level, count, updated_at)
VALUES ('current', 'info', 1, '2026-08-09T00:00:00.000Z');
INSERT INTO runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at)
VALUES ('current', 'legacy-event', 1, NULL, '2026-08-09T00:00:00.000Z')`); err != nil {
			t.Fatal(err)
		}
		if err := legacy.Close(); err != nil {
			t.Fatal(err)
		}
		if err := MigrateLegacySQLite(testOwnerContext(t, store), config, store); err != nil {
			t.Fatalf("可选绝对时间列为 NULL 的旧库必须迁移成功: %v", err)
		}
		detachLegacy(t, store)
	})

	t.Run("nullable required column rejected", func(t *testing.T) {
		store, config := openTestSQLiteStore(t)
		config.DatasetPath = filepath.Join(t.TempDir(), "nullable-required-legacy.sqlite")
		createNullableLegacyDataset(t, config.DatasetPath)
		legacy, err := sql.Open("sqlite", config.DatasetPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.Exec(`INSERT INTO runtime_log_level_facets (bucket_key, level, count, updated_at) VALUES ('current', 'info', 1, NULL)`); err != nil {
			t.Fatal(err)
		}
		if err := legacy.Close(); err != nil {
			t.Fatal(err)
		}
		if err := MigrateLegacySQLite(testOwnerContext(t, store), config, store); err == nil || !strings.Contains(err.Error(), "缺少绝对时间") {
			t.Fatalf("必填绝对时间列为 NULL 必须拒绝迁移: %v", err)
		}
		detachLegacy(t, store)
	})
}

// detachLegacy 显式分离旧库，避免 Windows 文件句柄延迟释放影响临时目录清理。
func detachLegacy(t *testing.T, store *sqliteStore) {
	t.Helper()
	_, _ = store.db.Exec("DETACH DATABASE legacy_runtime_log")
}
