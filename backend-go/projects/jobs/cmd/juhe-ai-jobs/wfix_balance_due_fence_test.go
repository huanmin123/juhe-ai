package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
)

// 余额探测意图围栏（TEXT 列 vs time 参数）修复回归：balance_query_next_refresh_at
// 在 PG/SQLite 里都是 text 列，此前把 time.Time 参数直接与 TEXT 列做等值/比较，
// 驱动端类型推断落入文本语义 → CommitDetectionDue 永远 0 行（生产 2026-09-26
// 连续 6 轮 selectedCount:2 staleCount:2，探测意图永不收口、候选被 J2 每 10s
// 重探打爆上游）。修复后：PG 分支 fence/比较统一 `::timestamptz` cast，写入值
// 用毫秒截断的 RFC3339Nano 预格式化文本（对齐 shared accountbalance
// AdvancePeriodicDue 范式）；SQLite 分支保持规范文本比较、写入值同 helper。

// TestBalanceDueTextTruncatesSubMillisecond：balanceDueText 的毫秒截断与
// 往返稳定——写入值 parse 读回后再 fence，期望与存储恒落同一毫秒（PG
// ::timestamptz 等值把两侧 cast 到同一微秒精度，±1μs 亚微秒差异由同源
// 截断在设计上消除）。
func TestBalanceDueTextTruncatesSubMillisecond(t *testing.T) {
	nano := time.Date(2026, 9, 26, 15, 26, 25, 690411968, time.UTC)
	got := balanceDueText(nano)
	if got != "2026-09-26T15:26:25.69Z" {
		t.Fatalf("纳秒时刻必须截断到毫秒文本，得到 %s", got)
	}
	// 截断（非四舍五入）语义：亚毫秒部分直接丢弃。
	belowRounding := time.Date(2026, 9, 26, 15, 26, 25, 690999999, time.UTC)
	if got := balanceDueText(belowRounding); got != "2026-09-26T15:26:25.69Z" {
		t.Fatalf("亚毫秒必须截断而非进位，得到 %s", got)
	}
	// 往返稳定：parse(写入文本) 再次写入/围栏得到同一文本（fence 恒可命中）。
	parsed, err := parseBalanceInstant(got)
	if err != nil {
		t.Fatalf("写入文本必须可解析: %v", err)
	}
	if again := balanceDueText(parsed); again != got {
		t.Fatalf("毫秒文本往返必须稳定: %s -> %s", got, again)
	}
	// RFC3339Nano 无小数时省略小数点（timeParam 的既有规范形态）。
	whole := time.Date(2026, 9, 26, 16, 0, 0, 0, time.UTC)
	if got := balanceDueText(whole); got != "2026-09-26T16:00:00Z" {
		t.Fatalf("整秒时刻的规范文本错误，得到 %s", got)
	}
}

// TestBalanceDueColumnExpressions：方言列表达式契约——PG 必须 cast 到
// timestamptz，SQLite 保持裸列（文本比较）。
func TestBalanceDueColumnExpressions(t *testing.T) {
	if got := balanceDueColumn(true); got != "balance_query_next_refresh_at::timestamptz" {
		t.Fatalf("PG 分支 due 比较必须 cast 到 timestamptz，得到 %q", got)
	}
	if got := balanceDueColumn(false); got != "balance_query_next_refresh_at" {
		t.Fatalf("SQLite 分支必须保持裸列文本比较，得到 %q", got)
	}
}

// TestBalanceRuntimeFenceHitsNanoAndMillisecondStoredText（SQLite 实测）：
// 存储文本为纳秒/毫秒两种 RFC3339Nano 精度时，due 扫描、Commit 围栏、收口
// NULL、毫秒截断写入后的二次围栏、Enable 围栏与 updated_at 预格式化全部命中。
func TestBalanceRuntimeFenceHitsNanoAndMillisecondStoredText(t *testing.T) {
	upstream := "http://127.0.0.1:1" // Commit/Enable 路径不访问上游
	runtime, businessDB, _ := wgNewBalanceRuntime(t, upstream, false)
	fixedNow := time.Date(2026, 9, 26, 16, 0, 0, 0, time.UTC)
	runtime.nowFunc = func() time.Time { return fixedNow }
	ctx := context.Background()

	nanoDue := "2026-09-26T15:26:25.690411968Z" // 生产事故形态：Go 写入的纳秒文本
	milliDue := "2026-09-26T14:00:00.123Z"
	seedBalanceDueRawText(t, businessDB, "acc-nano", nanoDue)
	seedBalanceDueRawText(t, businessDB, "acc-milli", milliDue)

	// due 过滤命中：纳秒与毫秒精度的到期行都被选出。
	candidates, err := runtime.ListDueCandidates(ctx, 100)
	if err != nil {
		t.Fatalf("候选扫描失败: %v", err)
	}
	ids := map[string]string{}
	for _, candidate := range candidates {
		ids[candidate.ID] = *candidate.NextRefreshAt
	}
	if ids["acc-nano"] != nanoDue || ids["acc-milli"] != milliDue {
		t.Fatalf("到期候选必须含纳秒/毫秒两种精度的读回文本: %v", ids)
	}

	// Commit 围栏命中（纳秒精度围栏）+ 收口 NULL 语义（NextRefreshAt 缺省
	// 即 nil = 清空意图）。
	changed, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID: "acc-nano", ExpectedConfigRevision: 1, ExpectedNextRefreshAt: &nanoDue,
	})
	if err != nil || !changed {
		t.Fatalf("纳秒围栏的收口提交必须命中: %v %v", changed, err)
	}
	var stored sql.NullString
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_next_refresh_at FROM accounts WHERE id = 'acc-nano'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.Valid {
		t.Fatalf("收口提交必须把 due 清空为 NULL，得到 %s", stored.String)
	}

	// Commit 推进：写入值必须是毫秒截断的预格式化文本（不再是 time 参数）。
	milliNext := balanceDueText(time.Date(2026, 9, 27, 15, 0, 0, 500000000, time.UTC))
	if milliNext != "2026-09-27T15:00:00.5Z" {
		t.Fatalf("推进写入文本应为毫秒截断形态，得到 %s", milliNext)
	}
	changed, err = runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID: "acc-milli", ExpectedConfigRevision: 1, ExpectedNextRefreshAt: &milliDue, NextRefreshAt: &milliNext,
	})
	if err != nil || !changed {
		t.Fatalf("毫秒围栏的推进提交必须命中: %v %v", changed, err)
	}
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_next_refresh_at FROM accounts WHERE id = 'acc-milli'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Valid || stored.String != milliNext {
		t.Fatalf("推进写入必须是预格式化毫秒文本 %s，得到 %v", milliNext, stored)
	}
	// 二次围栏命中：以刚写入的毫秒文本作为 expected 再次提交（围栏与写入
	// 值同源恒可比）。
	changed, err = runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID: "acc-milli", ExpectedConfigRevision: 1, ExpectedNextRefreshAt: &milliNext,
	})
	if err != nil || !changed {
		t.Fatalf("毫秒截断写入后的二次围栏必须命中: %v %v", changed, err)
	}

	// EnableDetectedQuery：围栏命中 + next/updated_at 预格式化写入。
	enableDue := "2026-09-26T14:30:00Z"
	seedBalanceDueRawText(t, businessDB, "acc-enable", enableDue)
	enableNext := balanceDueText(time.Date(2026, 9, 27, 16, 0, 0, 250000000, time.UTC))
	enabled, err := runtime.EnableDetectedQuery(ctx, opsjobs.BalanceEnableInput{
		AccountID: "acc-enable", ExpectedConfigRevision: 1, ExpectedNextRefreshAt: &enableDue,
		Config: opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5}, NextRefreshAt: enableNext,
	})
	if err != nil || !enabled {
		t.Fatalf("Enable 围栏必须命中: %v %v", enabled, err)
	}
	var enabledFlag int
	var enableStored, updatedAt string
	if err := businessDB.QueryRowContext(ctx, `SELECT balance_query_enabled, balance_query_next_refresh_at, updated_at FROM accounts WHERE id = 'acc-enable'`).Scan(&enabledFlag, &enableStored, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if enabledFlag != 1 || enableStored != enableNext {
		t.Fatalf("开启后 next 必须是预格式化文本 %s，得到 enabled=%d due=%v", enableNext, enabledFlag, enableStored)
	}
	if want := balanceDueText(fixedNow); updatedAt != want {
		t.Fatalf("updated_at 是 text 列，必须预格式化为 %s，得到 %s", want, updatedAt)
	}
}

// seedBalanceDueRawText 以任意 RFC3339 文本种入一个到期候选账户（不经过
// time.Now，用于锁定存储精度形态）。
func seedBalanceDueRawText(t *testing.T, db *sql.DB, id, dueText string) {
	t.Helper()
	if _, err := db.Exec(`
INSERT INTO accounts (id, system_account_id, type, status, schedulable, credentials_encrypted, balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, updated_at)
VALUES (?, 'sys-1', 'api_key', 'active', 1, '{"api_key":"sk-x","base_url":"http://127.0.0.1:1"}', 0, '{}', ?, ?)
`, id, dueText, dueText); err != nil {
		t.Fatal(err)
	}
}

// ---- PG 分支录制驱动（对齐 proxylatency wf_projector_pg_test 的 pgRecorder
// 模式）：不依赖真实 PostgreSQL，锁定 fence 的 ::timestamptz SQL 文本契约与
// time.Time/预格式化文本参数形态。----

type fenceStatement struct {
	query string
	args  []driver.Value
}

type fenceRecorder struct {
	mu         sync.Mutex
	statements []fenceStatement
}

func (r *fenceRecorder) capture(query string, args []driver.NamedValue) {
	values := make([]driver.Value, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, fenceStatement{query: query, args: values})
}

func (r *fenceRecorder) all() []fenceStatement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]fenceStatement{}, r.statements...)
}

type fenceRecorderConnector struct{ rec *fenceRecorder }

func (c fenceRecorderConnector) Connect(context.Context) (driver.Conn, error) {
	return &fenceRecorderConn{rec: c.rec}, nil
}

func (c fenceRecorderConnector) Driver() driver.Driver { return c }

func (c fenceRecorderConnector) Open(string) (driver.Conn, error) {
	return &fenceRecorderConn{rec: c.rec}, nil
}

type fenceRecorderConn struct{ rec *fenceRecorder }

func (c *fenceRecorderConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recorder: Prepare 不应被调用（走 QueryerContext/ExecerContext）")
}

func (c *fenceRecorderConn) Close() error { return nil }

func (c *fenceRecorderConn) Begin() (driver.Tx, error) {
	return nil, errors.New("recorder: Begin 不应被调用")
}

func (c *fenceRecorderConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.rec.capture(query, args)
	return driver.RowsAffected(1), nil
}

func (c *fenceRecorderConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.rec.capture(query, args)
	return &fenceEmptyRows{columns: []string{"id", "system_account_id", "dispatch_revision", "config_revision", "credentials_encrypted", "balance_query_next_refresh_at", "proxy_profile_id"}}, nil
}

func (c *fenceRecorderConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, int64, float64, bool, []byte, string, time.Time:
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	}
	return errors.New("recorder: 不支持的参数类型")
}

type fenceEmptyRows struct {
	columns []string
}

func (r *fenceEmptyRows) Columns() []string { return r.columns }
func (r *fenceEmptyRows) Close() error      { return nil }
func (r *fenceEmptyRows) Next([]driver.Value) error {
	return io.EOF // 空结果集的正常终止
}

// newFencePGRuntime 构造 postgres=true 的录制 runtime。
func newFencePGRuntime(t *testing.T) (*balanceDetectRuntime, *fenceRecorder) {
	t.Helper()
	recorder := &fenceRecorder{}
	db := sql.OpenDB(fenceRecorderConnector{rec: recorder})
	t.Cleanup(func() { _ = db.Close() })
	fixedNow := time.Date(2026, 9, 26, 16, 0, 0, 0, time.UTC)
	runtime := &balanceDetectRuntime{
		business: &businessDB{db: db, postgres: true},
		statsDB:  db,
		statsPG:  true,
		secret:   wgBalanceSecret,
		nowFunc:  func() time.Time { return fixedNow },
	}
	return runtime, recorder
}

func fenceRequireText(t *testing.T, haystack, needle, label string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%s 缺少 %q:\n%s", label, needle, haystack)
	}
}

// TestBalancePGDueSQLContract：PG 分支 SQL 契约——Commit/Enable 的 fence 为
// `::timestamptz = ?`（pgpool 改写后 `$n`），fence 参数为 time.Time，写入值
// 为预格式化文本；List 的 due 过滤与游标比较全部 cast。
func TestBalancePGDueSQLContract(t *testing.T) {
	ctx := context.Background()
	runtime, recorder := newFencePGRuntime(t)

	// CommitDetectionDue：收口（next=nil）。
	nanoDue := "2026-09-26T15:26:25.690411968Z"
	if _, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID: "acc-pg", ExpectedConfigRevision: 1, ExpectedNextRefreshAt: &nanoDue,
	}); err != nil {
		t.Fatalf("录制 Commit 失败: %v", err)
	}
	statement := recorder.all()[0]
	fenceRequireText(t, statement.query, "SET balance_query_next_refresh_at = ?", "Commit SET 子句")
	fenceRequireText(t, statement.query, "updated_at = ?", "Commit updated_at 写入")
	fenceRequireText(t, statement.query, "AND balance_query_next_refresh_at::timestamptz = ?", "Commit fence cast")
	// pgpool driver 层把 ? 改写为 $n（与生产同源），cast 不被破坏。
	bound := sqldialect.BindSQL(true, statement.query)
	fenceRequireText(t, bound, "AND balance_query_next_refresh_at::timestamptz = $5", "Commit fence 改写后契约")
	// 参数形态：$1 收口 NULL、$2 预格式化 updated_at、$5 time.Time 期望值。
	if statement.args[0] != nil {
		t.Fatalf("收口提交的 next 参数必须是 NULL，得到 %v", statement.args[0])
	}
	if text, ok := statement.args[1].(string); !ok || text != balanceDueText(runtime.nowFunc()) {
		t.Fatalf("updated_at 参数必须是预格式化文本，得到 %v", statement.args[1])
	}
	expected, err := parseBalanceInstant(nanoDue)
	if err != nil {
		t.Fatal(err)
	}
	if fenceTime, ok := statement.args[4].(time.Time); !ok || !fenceTime.Equal(expected) {
		t.Fatalf("fence 参数必须是 time.Time 期望值 %v，得到 %v", expected, statement.args[4])
	}

	// CommitDetectionDue：推进（next=毫秒文本）。
	milliNext := balanceDueText(time.Date(2026, 9, 27, 15, 0, 0, 500000000, time.UTC))
	if _, err := runtime.CommitDetectionDue(ctx, opsjobs.BalanceCommitDueInput{
		AccountID: "acc-pg", ExpectedConfigRevision: 1, ExpectedNextRefreshAt: &nanoDue, NextRefreshAt: &milliNext,
	}); err != nil {
		t.Fatalf("录制推进 Commit 失败: %v", err)
	}
	statement = recorder.all()[1]
	if text, ok := statement.args[0].(string); !ok || text != milliNext {
		t.Fatalf("推进写入参数必须是预格式化文本 %s，得到 %v", milliNext, statement.args[0])
	}

	// EnableDetectedQuery：fence cast 与参数形态。
	if _, err := runtime.EnableDetectedQuery(ctx, opsjobs.BalanceEnableInput{
		AccountID: "acc-pg", ExpectedConfigRevision: 1, ExpectedNextRefreshAt: &nanoDue,
		Config: opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5}, NextRefreshAt: milliNext,
	}); err != nil {
		t.Fatalf("录制 Enable 失败: %v", err)
	}
	statement = recorder.all()[2]
	fenceRequireText(t, statement.query, " AND balance_query_next_refresh_at::timestamptz = ?", "Enable fence cast")
	if _, ok := statement.args[5].(time.Time); !ok {
		t.Fatalf("Enable fence 参数必须是 time.Time，得到 %T", statement.args[5])
	}
	if nextText, ok := statement.args[1].(string); !ok || nextText != milliNext {
		t.Fatalf("Enable next 参数必须是预格式化文本 %s，得到 %v", milliNext, statement.args[1])
	}

	// ListDueCandidates：due 过滤与游标比较全部 cast，now 参数为 time.Time。
	if _, err := runtime.ListDueCandidates(ctx, 10); err != nil {
		t.Fatalf("录制候选扫描失败: %v", err)
	}
	statement = recorder.all()[3]
	fenceRequireText(t, statement.query, "AND balance_query_next_refresh_at::timestamptz <= ?", "List due 过滤 cast")
	fenceRequireText(t, statement.query, "OR balance_query_next_refresh_at::timestamptz > ? OR (balance_query_next_refresh_at::timestamptz = ? AND id > ?)", "List 游标比较 cast")
	boundList := sqldialect.BindSQL(true, statement.query)
	if strings.Contains(boundList, "?") {
		t.Fatalf("List SQL 经 pgpool 改写后不得残留 ? 占位符:\n%s", boundList)
	}
	if _, ok := statement.args[0].(time.Time); !ok {
		t.Fatalf("List now 参数必须是 time.Time，得到 %T", statement.args[0])
	}
}
