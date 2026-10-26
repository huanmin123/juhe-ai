package gatewaycacherate

// 缓存率快照加载器契约回归（设计 8.1）：SQL 口径（global/account 行、
// stat_hour >= cutoff、按 scope_id 聚合）、cutoff 小时键按注入时区、TTL 内
// 不重复查询、到点后台刷新（请求路径零 I/O）、失败保留旧快照、超龄 stale、
// 限频 warn 与 juhe_stats 限定表名/$1 绑定。fixture 走 pgtestkit 隔离 PG 库
// （SQLite 与内存模式退役后唯一形态）：预建 juhe_stats.usage_stats_hourly，
// 夹具 INSERT 的 `?` 占位经 sqldialect 改写 driver 落为 $n，生产查询的 $1
// 原样透传。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
	stdlib "github.com/jackc/pgx/v5/stdlib"
)

const cacherateFixtureDDL = `CREATE TABLE %s (
		system_account_id text NOT NULL,
		scope_type text NOT NULL,
		scope_id text NOT NULL DEFAULT '',
		stat_hour text NOT NULL,
		input_tokens bigint NOT NULL DEFAULT 0,
		cache_read_tokens bigint NOT NULL DEFAULT 0,
		PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour)
	)`

// cacherateFixture 持有可注入 now/tz/warn 的加载器与隔离 PG 统计库句柄。
type cacherateFixture struct {
	db     *sql.DB
	source *SnapshotSource
	table  string

	mu       sync.Mutex
	nowValue time.Time
	tzFail   bool
	warns    []string
}

func newCacherateFixture(t *testing.T) *cacherateFixture {
	t.Helper()
	table := "juhe_stats.usage_stats_hourly"
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_stats`); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, ddlFor(table))
		return err
	})
	// 夹具 INSERT 用 `?` 占位：经 sqldialect 改写 driver 直开测试池；生产
	// SnapshotSource 的 $1 绑定不含 `?`，原样透传。
	db, err := sqldialect.OpenDB(stdlib.GetDefaultDriver(), testDB.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := &cacherateFixture{
		db:       db,
		table:    table,
		nowValue: time.Date(2026, 10, 8, 4, 0, 0, 0, time.UTC),
	}
	source, err := NewSnapshotSource(db, fixture.timezone, fixture.now, fixture.warn)
	if err != nil {
		t.Fatal(err)
	}
	fixture.source = source
	return fixture
}

func ddlFor(table string) string {
	return fmt.Sprintf(cacherateFixtureDDL, table)
}

func (f *cacherateFixture) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nowValue
}

func (f *cacherateFixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nowValue = f.nowValue.Add(d)
}

func (f *cacherateFixture) setTime(value time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nowValue = value
}

func (f *cacherateFixture) failTimezone() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tzFail = true
}

func (f *cacherateFixture) timezone(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tzFail {
		return "", errors.New("统计时区源注入失败")
	}
	return "Asia/Shanghai", nil
}

func (f *cacherateFixture) warn(message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.warns = append(f.warns, message)
}

func (f *cacherateFixture) warnCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.warns)
}

func (f *cacherateFixture) insert(t *testing.T, scopeID, statHour string, cacheRead, input int64) {
	t.Helper()
	f.insertRow(t, "global", "account", scopeID, statHour, cacheRead, input)
}

func (f *cacherateFixture) insertRow(t *testing.T, systemAccountID, scopeType, scopeID, statHour string, cacheRead, input int64) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO `+f.table+`
		(system_account_id, scope_type, scope_id, stat_hour, input_tokens, cache_read_tokens)
		VALUES (?, ?, ?, ?, ?, ?)`, systemAccountID, scopeType, scopeID, statHour, input, cacheRead); err != nil {
		t.Fatalf("fixture insert: %v", err)
	}
}

// waitForScheduledRefresh 等待最近一次 Snapshot 调度的后台刷新结束（CAS 闸门
// 释放），保证后续断言观察到已收敛状态。
func waitForScheduledRefresh(t *testing.T, fixture *cacherateFixture) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !fixture.source.refreshScheduled.Load() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("后台刷新未在截止时间内结束")
}

// TestSnapshotSourceLoadsGlobalAccountWindow 锁定 8.1 SQL 口径：只取
// system_account_id='global' + scope_type='account' 行、按 scope_id 聚合、
// stat_hour >= cutoff（注入时区 Asia/Shanghai；now=UTC 04:00 → 本地 12:00 →
// cutoff 键 2026-10-07T12）。
func TestSnapshotSourceLoadsGlobalAccountWindow(t *testing.T) {
	fixture := newCacherateFixture(t)
	fixture.insert(t, "acc-1", "2026-10-07T12", 100, 400)
	fixture.insert(t, "acc-1", "2026-10-07T23", 100, 100)
	fixture.insert(t, "acc-1", "2026-10-07T11", 999, 999) // cutoff 之前
	fixture.insertRow(t, "tenant", "account", "acc-1", "2026-10-08T00", 999, 999)
	fixture.insertRow(t, "global", "system", "acc-1", "2026-10-08T00", 999, 999)

	fixture.source.Warmup(context.Background())
	rates, stale := fixture.source.Snapshot()
	if stale {
		t.Fatalf("首次成功加载后 stale 应为 false")
	}
	if len(rates) != 1 {
		t.Fatalf("rates = %v, 只应有 acc-1 一行", rates)
	}
	window := rates["acc-1"]
	if window.CacheReadTokens != 200 || window.InputTokens != 500 {
		t.Fatalf("acc-1 window = %+v, 期望 cache=200 input=500", window)
	}
}

// TestSnapshotSourceCutoffFollowsInjectedTimezone 锁定时区链路：cutoff 小时
// 键按注入的 usageStatsTimezone（Asia/Shanghai=UTC+8）计算。now=15:59Z →
// 上海 23:59 → cutoff 键 2026-10-07T23；22 点行必须被排除——若误用 UTC，
// cutoff 会是 07T15，两行都会进窗。
func TestSnapshotSourceCutoffFollowsInjectedTimezone(t *testing.T) {
	fixture := newCacherateFixture(t)
	fixture.setTime(time.Date(2026, 10, 8, 15, 59, 0, 0, time.UTC))
	fixture.insert(t, "acc-inside", "2026-10-07T23", 10, 100)
	fixture.insert(t, "acc-outside", "2026-10-07T22", 20, 100)

	fixture.source.Warmup(context.Background())
	rates, stale := fixture.source.Snapshot()
	if stale || len(rates) != 1 {
		t.Fatalf("rates = %v, stale = %v", rates, stale)
	}
	if _, ok := rates["acc-inside"]; !ok {
		t.Fatalf("窗内行缺失: %v", rates)
	}
	if _, ok := rates["acc-outside"]; ok {
		t.Fatalf("窗外行不应入窗（时区 cutoff 失效）: %v", rates)
	}
}

// TestSnapshotSourceNoRequeryWithinTTLAndZeroIO 证明请求路径零 I/O：TTL 内
// 数据库变化不反映（不重复查询）；到点后 Snapshot 同步路径仍返回旧数据、
// 仅调度一次后台刷新，新数据随后落地。
func TestSnapshotSourceNoRequeryWithinTTLAndZeroIO(t *testing.T) {
	fixture := newCacherateFixture(t)
	fixture.insert(t, "acc-1", "2026-10-08T00", 100, 200)
	fixture.source.Warmup(context.Background())

	// TTL 内：数据库变化不反映——内存快照获胜。
	fixture.insert(t, "acc-2", "2026-10-08T00", 300, 300)
	fixture.advance(30 * time.Second)
	rates, stale := fixture.source.Snapshot()
	if stale || len(rates) != 1 {
		t.Fatalf("TTL 内 Snapshot 应命中内存快照: rates=%v stale=%v", rates, stale)
	}

	// 到点：同步路径仍返回旧数据（零 I/O），后台刷新随后落地。
	fixture.advance(31 * time.Second) // 距上次刷新尝试 61s >= TTL
	rates, stale = fixture.source.Snapshot()
	if stale || len(rates) != 1 {
		t.Fatalf("到点瞬间仍应返回旧内存数据: rates=%v stale=%v", rates, stale)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
		rates, stale = fixture.source.Snapshot()
		if !stale && len(rates) == 2 {
			break
		}
	}
	if rates == nil || len(rates) != 2 {
		t.Fatalf("后台刷新未落地: rates=%v", rates)
	}
}

// TestSnapshotSourceFailureKeepsOldSnapshotAndStales 锁定失败语义：时区解析
// 失败使整轮刷新失败（不得另设默认时区），旧快照保留至超龄，之后维度中性
// （rates=nil, stale=true）。
func TestSnapshotSourceFailureKeepsOldSnapshotAndStales(t *testing.T) {
	fixture := newCacherateFixture(t)
	fixture.insert(t, "acc-1", "2026-10-08T00", 50, 100)
	fixture.source.Warmup(context.Background())

	fixture.failTimezone()
	fixture.advance(CacheRateSnapshotTTL)
	rates, stale := fixture.source.Snapshot()
	waitForScheduledRefresh(t, fixture)
	if stale {
		t.Fatalf("未超龄的旧快照不应判定 stale")
	}
	if _, ok := rates["acc-1"]; !ok {
		t.Fatalf("刷新失败应保留旧快照: %v", rates)
	}

	fixture.advance(CacheRateSnapshotMaxStale)
	rates, stale = fixture.source.Snapshot()
	if !stale || rates != nil {
		t.Fatalf("超龄快照应 stale 且不上报数据: rates=%v stale=%v", rates, stale)
	}
}

// TestSnapshotSourceNeverLoadedIsStale：Warmup 失败后加载器保持未加载——
// Snapshot 报 nil + stale，直到某轮刷新成功。
func TestSnapshotSourceNeverLoadedIsStale(t *testing.T) {
	fixture := newCacherateFixture(t)
	fixture.failTimezone()
	fixture.source.Warmup(context.Background())
	rates, stale := fixture.source.Snapshot()
	if !stale || rates != nil {
		t.Fatalf("从未成功加载应 rates=nil stale=true: rates=%v stale=%v", rates, stale)
	}
}

// TestSnapshotSourceWarnRateLimited 锁定刷新失败的 1/min 限频 warn。
func TestSnapshotSourceWarnRateLimited(t *testing.T) {
	fixture := newCacherateFixture(t)
	fixture.failTimezone()
	for i := 0; i < 5; i++ {
		fixture.source.Warmup(context.Background())
		fixture.advance(time.Second)
	}
	if got := fixture.warnCount(); got != 1 {
		t.Fatalf("限频 warn 应每分钟至多一条, got %d", got)
	}
	fixture.advance(cacheRateWarnInterval)
	fixture.source.Warmup(context.Background())
	if got := fixture.warnCount(); got != 2 {
		t.Fatalf("限频窗口后应放行下一条 warn, got %d", got)
	}
}
