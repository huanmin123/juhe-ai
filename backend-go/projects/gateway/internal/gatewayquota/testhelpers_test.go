package gatewayquota

// testhelpers_test.go 是 gatewayquota 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，gatewayquota 测试迁移
// 切片）：pgtestkit 每测试独立库，测试建库走生产 store（StatsStore /
// DBTimezoneSource，juhe_stats / juhe_business schema 限定 + $n
// 占位符，与生产 PG 装配同一方言）。
//
// 三组分类（D9/D17）：
//   - 组A（断言可逐字节保持）：建库走本文件 newTestDB，
//     statsSchema / seedCost / authzSchema 系列 seed 对齐生产方言；
//     测试体断言逐字节保持。
//   - 组B（ServerRole 同步守卫）：守卫文案是生产契约本身，相关测试保留。
//   - scripted 假驱动与 fake（miniredis、mockDBService、wjMockShared、
//     fakeClock、manualTimerQueue 等）不依赖真库，保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// newTestDB 创建 pgtestkit 隔离 PG 测试库并注入 juhe_stats / juhe_business
// schema（只建 schema 不建表：缺表错误注入类测试依赖空库前提）。label 参数
// 为 SQLite 共享内存库时代的隔离标记，PG 每测试独立建库后不再需要，保留
// 签名以免 80+ 调用点机械改名。
func newTestDB(t *testing.T, label string) *sql.DB {
	t.Helper()
	_ = label
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_stats;
CREATE SCHEMA IF NOT EXISTS juhe_business;`)
		return err
	})
}

// quotaStatsPGFixtureSchema 是配额读侧的 juhe_stats 夹具 schema：列集按
// groups pgkit 先例放宽为本包实际读写的键列与配额口径成本列，类型照抄
// maintenance 生产 DDL（pg_schema_stats.go）；省略生产计数列、updated_at 与
// 主键（配额读侧无唯一约束依赖，seed 行不满足生产外键/默认值链）。
const quotaStatsPGFixtureSchema = `
	CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_totals (
	  system_account_id text NOT NULL,
	  scope_type text NOT NULL,
	  scope_id text NOT NULL DEFAULT '',
	  total_cost_usd double precision NOT NULL DEFAULT 0,
	  success_cost_usd double precision NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_daily (
	  system_account_id text NOT NULL,
	  scope_type text NOT NULL,
	  scope_id text NOT NULL DEFAULT '',
	  stat_date text NOT NULL,
	  total_cost_usd double precision NOT NULL DEFAULT 0,
	  success_cost_usd double precision NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_weekly (
	  system_account_id text NOT NULL,
	  scope_type text NOT NULL,
	  scope_id text NOT NULL DEFAULT '',
	  stat_week text NOT NULL,
	  total_cost_usd double precision NOT NULL DEFAULT 0,
	  success_cost_usd double precision NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS juhe_stats.usage_stats_monthly (
	  system_account_id text NOT NULL,
	  scope_type text NOT NULL,
	  scope_id text NOT NULL DEFAULT '',
	  stat_month text NOT NULL,
	  total_cost_usd double precision NOT NULL DEFAULT 0,
	  success_cost_usd double precision NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS juhe_stats.usage_quota_hourly_windows (
	  system_account_id text NOT NULL,
	  scope_type text NOT NULL,
	  scope_id text NOT NULL DEFAULT '',
	  window_hours integer NOT NULL,
	  total_cost_usd double precision NOT NULL DEFAULT 0
	);
`

// statsSchema creates the five usage projections the quota reads. The
// usage_stats tables expose both cost columns: quota reads success_cost_usd
// (成功交付口径；total_cost_usd 只留给账号成本观测，fixture 可用来断言失败
// 尝试成本不计入配额)。usage_quota_hourly_windows.total_cost_usd 单列即配额
// 口径成本（statsagg 窗口刷新按 success_cost_usd 汇总写入）。
func statsSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(quotaStatsPGFixtureSchema); err != nil {
		t.Fatalf("create stats schema: %v", err)
	}
}

// seedCost inserts one cost row into the juhe_stats projection named by
// table（生产 PG 臂方言：schema 限定 + $n 占位符）。
func seedCost(t *testing.T, db *sql.DB, table string, columns []string, values []any) {
	t.Helper()
	placeholders := make([]string, len(columns))
	for i := range placeholders {
		placeholders[i] = "$" + strconv.Itoa(i+1)
	}
	query := "INSERT INTO juhe_stats." + table + " (" + strings.Join(columns, ", ") + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	if _, err := db.Exec(query, values...); err != nil {
		t.Fatalf("seed %s: %v", table, err)
	}
}

// fakeClock is a manual clock for deterministic TTL/cache behaviour.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{now: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// manualTimerQueue records scheduled timers so tests fire them explicitly.
type manualTimerQueue struct {
	mu     sync.Mutex
	timers []*manualTimer
}

type manualTimer struct {
	delay   time.Duration
	fn      func()
	stopped bool
}

func (q *manualTimerQueue) schedule(delay time.Duration, fn func()) (stop func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	timer := &manualTimer{delay: delay, fn: fn}
	q.timers = append(q.timers, timer)
	return func() { timer.stopped = true }
}

// fireAll runs every pending timer whose delay is <= elapsed, oldest first,
// and reports how many fired.
func (q *manualTimerQueue) fireAll() int {
	q.mu.Lock()
	pending := append([]*manualTimer(nil), q.timers...)
	q.mu.Unlock()
	fired := 0
	for _, timer := range pending {
		if !timer.stopped {
			timer.fn()
			fired++
		}
	}
	return fired
}

func (q *manualTimerQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.timers)
}

func fmtAny(v any) string { return fmt.Sprintf("%v", v) }
