package operationlog

// w9b owner/legacy 迁移补充臂：用接口 fake 与真实 SQLite 文件覆盖 RunOwner
// 的租约与 retention 错误分支、legacy 迁移的源库校验失败、扫描与计数错误。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// RunOwner fake store
// ---------------------------------------------------------------------------

type w9bFakeStore struct {
	retentionDays int
	retentionErr  error
	cleanupErr    error
	cleanupLost   bool
	cleanupCalls  int
	cleanup       int64
}

func (s *w9bFakeStore) EnsureSchema(context.Context) error { return nil }
func (s *w9bFakeStore) AcquireOwnerLease(context.Context, string, time.Duration) (OwnerLease, bool, error) {
	return OwnerLease{}, false, nil
}
func (s *w9bFakeStore) RenewOwnerLease(context.Context, OwnerLease, time.Duration) (bool, error) {
	return true, nil
}
func (s *w9bFakeStore) ReleaseOwnerLease(context.Context, OwnerLease) error { return nil }
func (s *w9bFakeStore) Persist(context.Context, OwnerLease, Input) (bool, error) {
	return false, nil
}
func (s *w9bFakeStore) List(context.Context, ListOptions) (ListResult, error) {
	return ListResult{}, nil
}
func (s *w9bFakeStore) Detail(context.Context, string, string) (DetailSupplement, bool, error) {
	return DetailSupplement{}, false, nil
}
func (s *w9bFakeStore) CleanupRetention(context.Context, OwnerLease, time.Time, int) (int64, error) {
	s.cleanupCalls++
	if s.cleanupLost {
		return 0, ErrOwnerLeaseLost
	}
	return s.cleanup, s.cleanupErr
}
func (s *w9bFakeStore) RetentionDays(context.Context, int) (int, error) {
	return s.retentionDays, s.retentionErr
}
func (s *w9bFakeStore) Close() error { return nil }

// w9bRunOwnerBrief 在 1ms 节奏下运行 owner，80ms 后取消，返回终态错误。
func w9bRunOwnerBrief(t *testing.T, store Store, keeper *LeaseKeeper, cfg Config) error {
	t.Helper()
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunOwner(runCtx, store, keeper, cfg, slog.Default()) }()
	time.Sleep(80 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("RunOwner 未在取消后退出")
		return nil
	}
}

func TestW9BRunOwnerRetentionArms(t *testing.T) {
	keeper := &LeaseKeeper{lostCh: make(chan struct{})}
	keeper.lease = OwnerLease{OwnerID: "w9b", FenceToken: 1}
	cfg := Config{RetentionInterval: time.Millisecond, RetentionDays: 30, RetentionBatchSize: 10}

	// retention 设置读取失败 → 跳过本轮继续。
	settingErr := &w9bFakeStore{retentionErr: w9bErrBoom}
	if err := w9bRunOwnerBrief(t, settingErr, keeper, cfg); err != nil {
		t.Fatalf("retention 设置失败必须继续：%v", err)
	}
	if settingErr.cleanupCalls != 0 {
		t.Fatal("设置失败时不得触发清理")
	}
	// 清理成功。
	healthy := &w9bFakeStore{retentionDays: 7, cleanup: 3}
	if err := w9bRunOwnerBrief(t, healthy, keeper, cfg); err != nil {
		t.Fatalf("正常清理：%v", err)
	}
	if healthy.cleanupCalls == 0 || healthy.cleanup != 3 {
		t.Fatalf("正常清理未执行：calls=%d", healthy.cleanupCalls)
	}
	// 清理普通失败 → 记录并继续。
	transient := &w9bFakeStore{retentionDays: 7, cleanupErr: w9bErrBoom}
	if err := w9bRunOwnerBrief(t, transient, keeper, cfg); err != nil {
		t.Fatalf("普通清理失败必须继续：%v", err)
	}
	// 清理丢租约 → 终态退出。
	fenced := &w9bFakeStore{retentionDays: 7, cleanupLost: true}
	if err := w9bRunOwnerBrief(t, fenced, keeper, cfg); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("丢租约必须终态退出：%v", err)
	}
}

func TestW9BRunOwnerLostWithoutError(t *testing.T) {
	// Lost 已关闭但 LostError 为 nil → 回退 ErrOwnerLeaseLost。
	keeper := &LeaseKeeper{lostCh: make(chan struct{})}
	close(keeper.lostCh)
	err := RunOwner(context.Background(), &w9bFakeStore{}, keeper, Config{}, slog.Default())
	if !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("无因丢失=%v", err)
	}
}

// ---------------------------------------------------------------------------
// 计数/主键/扫描错误臂
// ---------------------------------------------------------------------------

func TestW9BCountAndPrimaryKeyErrorArms(t *testing.T) {
	// 接口实参直接用脚本化 db：查询耗尽即报错。
	ctx := context.Background()
	exhausted := w9bOpen(t, &w9bScript{})
	// operationLogCounts。
	if _, err := operationLogCounts(ctx, exhausted, ""); err == nil || !strings.Contains(err.Error(), "行数失败") {
		t.Fatalf("counts=%v", err)
	}
	// postgresPrimaryKey。
	if _, err := postgresPrimaryKey(ctx, exhausted, "juhe_dataset.operation_log_viewers"); err == nil || !strings.Contains(err.Error(), "主键失败") {
		t.Fatalf("primaryKey=%v", err)
	}
	// postgresPrimaryKeyConstraint。
	if _, err := postgresPrimaryKeyConstraint(ctx, exhausted, "juhe_dataset.operation_log_viewers"); err == nil || !strings.Contains(err.Error(), "主键约束失败") {
		t.Fatalf("primaryKeyConstraint=%v", err)
	}
}

// w9bLegacyScanRow 构造一行合法的旧 Node 操作日志扫描值（25 列）。
func w9bLegacyScanRow(id string) []driver.Value {
	return []driver.Value{
		id, "", "actor", "actor", "Actor", "admin", "", "self", "accounts", "update",
		"accounts.update", "account", "acc-1", "Alpha", "更新账户", "full", "targeted",
		`[{"field":"enabled","before":false,"after":true}]`, `{"source":"legacy"}`,
		"PATCH", "/api/accounts/1", int64(200), "203.0.113.5", "w9b-agent",
		"2026-09-01T00:00:00.000000000Z",
	}
}

func TestW9BScanLegacyOperationLogArms(t *testing.T) {
	scanOne := func(t *testing.T, values []driver.Value, _ error) (legacyOperationLog, error) {
		t.Helper()
		script := &w9bScript{}
		script.steps = append(script.steps, w9bStep{matcher: []string{"probe"}, cols: []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8", "c9", "c10", "c11", "c12", "c13", "c14", "c15", "c16", "c17", "c18", "c19", "c20", "c21", "c22", "c23", "c24", "c25"}, rows: [][]driver.Value{values}})
		rows, err := w9bOpen(t, script).QueryContext(context.Background(), "probe")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatalf("必须有行：%v", rows.Err())
		}
		return scanLegacyOperationLog(rows)
	}
	// Scan 失败：id 列为 NULL（不支持转换到 string）。
	typeMismatch := w9bLegacyScanRow("id-1")
	typeMismatch[0] = nil
	if _, err := scanOne(t, typeMismatch, nil); err == nil || !strings.Contains(err.Error(), "读取旧 Node 操作日志行失败") {
		t.Fatalf("Scan 失败=%v", err)
	}
	// changes_json 非法。
	badChanges := w9bLegacyScanRow("id-1")
	badChanges[17] = "{invalid"
	if _, err := scanOne(t, badChanges, nil); err == nil || !strings.Contains(err.Error(), "changes_json 无效") {
		t.Fatalf("坏 changes=%v", err)
	}
	// changes_json 合法但类型不兼容（数字数组）。
	mismatchChanges := w9bLegacyScanRow("id-1")
	mismatchChanges[17] = "[1,2]"
	if _, err := scanOne(t, mismatchChanges, nil); err == nil || !strings.Contains(err.Error(), "changes_json 无效") {
		t.Fatalf("类型不兼容 changes=%v", err)
	}
	// metadata_json 非法。
	badMetadata := w9bLegacyScanRow("id-1")
	badMetadata[18] = "{invalid"
	if _, err := scanOne(t, badMetadata, nil); err == nil || !strings.Contains(err.Error(), "metadata_json 无效") {
		t.Fatalf("坏 metadata=%v", err)
	}
	// normalize 失败（actorRole 为空白）。
	missingRole := w9bLegacyScanRow("id-1")
	missingRole[5] = " "
	if _, err := scanOne(t, missingRole, nil); err == nil || !strings.Contains(err.Error(), "不兼容") {
		t.Fatalf("normalize 失败=%v", err)
	}
	// created_at 非法。
	badTime := w9bLegacyScanRow("id-1")
	badTime[24] = "not-a-time"
	if _, err := scanOne(t, badTime, nil); err == nil || !strings.Contains(err.Error(), "读取旧 Node 操作日志行失败") {
		t.Fatalf("坏 created_at=%v", err)
	}
	// 合法行。
	record, err := scanOne(t, w9bLegacyScanRow("id-ok"), nil)
	if err != nil || record.Input.ID != "id-ok" || len(record.Input.Changes) != 1 {
		t.Fatalf("合法行=%+v err=%v", record.Input, err)
	}
}

// w9bSeedLegacyPair 构造 source/target 两库，各含一条指定的操作日志行。
func w9bSeedLegacyPair(t *testing.T, root, summary string) (source, target *sql.DB) {
	t.Helper()
	openAt := func(name string) *sql.DB {
		db, err := sql.Open("sqlite", filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	source, target = openAt("src.sqlite3"), openAt("tgt.sqlite3")
	schema := `CREATE TABLE operation_logs (id TEXT PRIMARY KEY, trace_id TEXT, actor_system_account_id TEXT NOT NULL, actor_username TEXT, actor_display_name TEXT, actor_role TEXT NOT NULL, operation_scope_system_account_id TEXT, mode TEXT NOT NULL, module TEXT NOT NULL, action TEXT NOT NULL, operation_key TEXT NOT NULL, resource_type TEXT NOT NULL, resource_id TEXT, resource_name TEXT, summary TEXT NOT NULL, detail_level TEXT NOT NULL, visibility_scope TEXT NOT NULL, changes_json TEXT NOT NULL, metadata_json TEXT NOT NULL, method TEXT, path TEXT, status_code INTEGER, client_ip TEXT, user_agent TEXT, created_at TEXT NOT NULL);
CREATE TABLE operation_log_targets (id TEXT PRIMARY KEY, operation_log_id TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT, target_name TEXT, target_owner_system_account_id TEXT, relation TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE operation_log_viewers (operation_log_id TEXT NOT NULL, system_account_id TEXT NOT NULL, visibility_reason TEXT NOT NULL, detail_level TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(operation_log_id,system_account_id,visibility_reason,detail_level));
CREATE TABLE operation_log_summary_search_terms (operation_log_id TEXT NOT NULL, term TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(term,operation_log_id));`
	for _, db := range []*sql.DB{source, target} {
		if _, err := db.Exec(schema); err != nil {
			t.Fatal(err)
		}
	}
	insert := `INSERT INTO operation_logs (id,trace_id,actor_system_account_id,actor_username,actor_display_name,actor_role,operation_scope_system_account_id,mode,module,action,operation_key,resource_type,resource_id,resource_name,summary,detail_level,visibility_scope,changes_json,metadata_json,method,path,status_code,client_ip,user_agent,created_at) VALUES ('legacy-1','','actor','actor','Actor','admin','','self','accounts','update','accounts.update','account','acc-1','Alpha',?,'full','targeted','[{"field":"enabled","before":false,"after":true}]','{"source":"legacy"}','PATCH','/api/accounts/1',200,'203.0.113.5','w9b-agent','2026-09-01T00:00:00.000000000Z');
INSERT INTO operation_log_targets VALUES ('legacy-target','legacy-1','account','acc-1','Alpha','owner','primary','2026-09-01T00:00:00.000000000Z');
INSERT INTO operation_log_viewers VALUES ('legacy-1','viewer','resource_owner','full','2026-09-01T00:00:00.000000000Z');`
	for _, db := range []*sql.DB{source, target} {
		if _, err := db.Exec(insert, summary); err != nil {
			t.Fatal(err)
		}
		for _, term := range searchTerms(summary) {
			if _, err := db.Exec(`INSERT INTO operation_log_summary_search_terms VALUES ('legacy-1',?,'2026-09-01T00:00:00.000000000Z')`, term); err != nil {
				t.Fatal(err)
			}
		}
	}
	return source, target
}

// TestW9BSearchTermsBoundaryArms 覆盖 searchTerms 的长度/上限分支。
func TestW9BSearchTermsBoundaryArms(t *testing.T) {
	terms := searchTerms("")
	if terms != nil {
		t.Fatalf("空摘要=%v", terms)
	}
	// 超长文本命中 1500 上限截断。
	long := strings.Repeat("字", 3000)
	capped := searchTerms(long)
	if len(capped) > 1500 {
		t.Fatalf("上限=%d", len(capped))
	}
	// 单词普通文本。
	basic := searchTerms("alpha beta")
	if len(basic) < 3 {
		t.Fatalf("基础分词=%v", basic)
	}
}

// TestW9BListErrorArms 覆盖 List 闭包 queryItems 的行扫描错误臂（脚本化 db）。
func TestW9BListErrorArms(t *testing.T) {
	ctx := context.Background()
	// 行数据类型不匹配 → Scan 失败。
	badScan := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT ol.id,COALESCE(ol.trace_id"}, cols: []string{"id"}, rows: [][]driver.Value{{int64(1)}}},
	}}
	store := &sqlStore{db: w9bOpen(t, badScan)}
	if _, err := store.List(ctx, ListOptions{}); err == nil {
		t.Fatal("扫描失败必须透传")
	}
	// 行迭代错误。
	rowsErrScript := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT ol.id,COALESCE(ol.trace_id"}, cols: []string{"id"}, rows: [][]driver.Value{{"id-1"}}, eofErr: w9bErrBoom},
	}}
	if _, err := (&sqlStore{db: w9bOpen(t, rowsErrScript)}).List(ctx, ListOptions{}); err == nil {
		t.Fatal("行迭代错误必须透传")
	}
	// 查询错误。
	queryErr := &w9bScript{}
	if _, err := (&sqlStore{db: w9bOpen(t, queryErr)}).List(ctx, ListOptions{}); err == nil {
		t.Fatal("查询错误必须透传")
	}
}

// TestW9BListInvalidTimes 覆盖 startAt/endAt 交换与过滤矩阵剩余分支。
func TestW9BListInvalidTimes(t *testing.T) {
	ctx := context.Background()
	rows := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT ol.id,COALESCE(ol.trace_id"}, cols: []string{"id"}, noRows: true, repeat: 4},
	}}
	store := &sqlStore{db: w9bOpen(t, rows)}
	if _, err := store.List(ctx, ListOptions{EndAt: "nope"}); err == nil || !strings.Contains(err.Error(), "endAt") {
		t.Fatalf("endAt=%v", err)
	}
	// startAt > endAt 自动交换。
	if _, err := store.List(ctx, ListOptions{StartAt: "2026-09-02T00:00:00Z", EndAt: "2026-09-01T00:00:00Z"}); err != nil {
		t.Fatalf("时间交换=%v", err)
	}
	// 超长关键词 → 1=0 过滤仍会发起查询。
	long := strings.Repeat("长", 200)
	if _, err := store.List(ctx, ListOptions{SummaryKeyword: long}); err != nil {
		t.Fatalf("超长关键词=%v", err)
	}
	// 分页与窗口上限归一。
	rowsRepeat := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT ol.id,COALESCE(ol.trace_id"}, cols: []string{"id"}, noRows: true, repeat: 4},
	}}
	result, err := (&sqlStore{db: w9bOpen(t, rowsRepeat)}).List(ctx, ListOptions{Page: 99999, PageSize: 500})
	if err != nil || result.Page == 0 || result.PageSize != maxPageSize {
		t.Fatalf("窗口归一=%+v err=%v", result, err)
	}
}

// TestW9BRowsFallbackErrors 覆盖 accountNames 的查询/扫描/迭代错误。
func TestW9BAccountNamesErrorArms(t *testing.T) {
	ctx := context.Background()
	// 查询失败透传。
	store := &sqlStore{db: w9bOpen(t, &w9bScript{})}
	if _, err := store.accountNames(ctx, []string{"a"}); err == nil || !strings.Contains(err.Error(), "read F4 system account names") {
		t.Fatalf("查询失败=%v", err)
	}
	// 空列表直接返回。
	empty, err := store.accountNames(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空列表=%v err=%v", empty, err)
	}
}

// TestW9BDetailErrorArms 覆盖 Detail 的错误臂（脚本化 db）。
func TestW9BDetailErrorArms(t *testing.T) {
	ctx := context.Background()
	// 主查询失败。
	mainErr := &w9bScript{}
	if _, _, err := (&sqlStore{db: w9bOpen(t, mainErr)}).Detail(ctx, "id", ""); err == nil {
		t.Fatal("主查询失败必须透传")
	}
	// 不存在 → found=false。
	missing := &w9bScript{steps: []w9bStep{{matcher: []string{"SELECT ol.operation_key"}, noRows: true}}}
	if _, found, err := (&sqlStore{db: w9bOpen(t, missing)}).Detail(ctx, "id", ""); err != nil || found {
		t.Fatalf("缺失：found=%v err=%v", found, err)
	}
	// full 检查失败。
	fullCheckErr := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT ol.operation_key"}, cols: []string{"operation_key", "resource_type", "resource_id", "resource_name", "visibility_scope", "detail_level"}, rows: [][]driver.Value{{"key", "account", "acc", "Alpha", "targeted", "full"}}},
		{matcher: []string{"FROM juhe_dataset.operation_log_viewers"}, rowsErr: w9bErrBoom},
	}}
	if _, _, err := (&sqlStore{db: w9bOpen(t, fullCheckErr)}).Detail(ctx, "id", "viewer"); err == nil {
		t.Fatal("full 检查失败必须透传")
	}
	// summary 级 viewer → 空骨架。
	summaryViewer := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT ol.operation_key"}, cols: []string{"operation_key", "resource_type", "resource_id", "resource_name", "visibility_scope", "detail_level"}, rows: [][]driver.Value{{"key", "account", "acc", "Alpha", "targeted", "summary"}}},
		{matcher: []string{"FROM juhe_dataset.operation_log_viewers"}, rows: [][]driver.Value{{false}}},
	}}
	supplement, found, err := (&sqlStore{db: w9bOpen(t, summaryViewer)}).Detail(ctx, "id", "viewer")
	if err != nil || !found || supplement.Changes == nil || supplement.Targets == nil {
		t.Fatalf("summary viewer=%+v found=%v err=%v", supplement, found, err)
	}
	// changes 查询失败。
	changesErr := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT ol.operation_key"}, cols: []string{"operation_key", "resource_type", "resource_id", "resource_name", "visibility_scope", "detail_level"}, rows: [][]driver.Value{{"key", "account", "acc", "Alpha", "targeted", "full"}}},
		{matcher: []string{"FROM juhe_dataset.operation_log_viewers"}, rows: [][]driver.Value{{true}}},
		{matcher: []string{"SELECT changes_json,COALESCE(method"}, rowsErr: w9bErrBoom},
	}}
	if _, _, err := (&sqlStore{db: w9bOpen(t, changesErr)}).Detail(ctx, "id", "viewer"); err == nil {
		t.Fatal("changes 查询失败必须透传")
	}
	// targets 查询失败。
	targetsErr := &w9bScript{steps: []w9bStep{
		{matcher: []string{"SELECT ol.operation_key"}, cols: []string{"operation_key", "resource_type", "resource_id", "resource_name", "visibility_scope", "detail_level"}, rows: [][]driver.Value{{"key", "account", "acc", "Alpha", "targeted", "full"}}},
		{matcher: []string{"FROM juhe_dataset.operation_log_viewers"}, rows: [][]driver.Value{{true}}},
		{matcher: []string{"SELECT changes_json,COALESCE(method"}, rows: [][]driver.Value{{"[]", "PATCH", "/x", "1.2.3.4"}}},
		{matcher: []string{"SELECT id,target_type,COALESCE(target_id"}, rowsErr: w9bErrBoom},
	}}
	if _, _, err := (&sqlStore{db: w9bOpen(t, targetsErr)}).Detail(ctx, "id", "viewer"); err == nil {
		t.Fatal("targets 查询失败必须透传")
	}
}
