// Package gatewaycacherate implements the cache-rate snapshot loader of the
// cache-rate-aware scheduling design (docs/functions/缓存率感知调度与用量缓存
// 率展示设计.md section 8.1): one aggregate SQL per refresh cycle against the
// stats database's usage_stats_hourly, a 60s-TTL background refresh so the
// request path (Snapshot) is a zero-I/O in-memory read, and the 10-minute
// staleness gate that turns the whole cache-rate ordering dimension neutral
// (section 7) once no successful refresh landed in time.
//
// Semantics fixed by the contract:
//   - the timezone comes from the injected statreads.TimezoneSource (the same
//     usageStatsTimezone resolver the aggregation side uses); a missing or
//     invalid setting fails the refresh cycle and keeps the previous snapshot.
//     The loader never installs its own default timezone.
//   - a failed refresh keeps the old snapshot until it ages out; warnings are
//     rate-limited to one per minute.
//   - Snapshot never performs I/O: on TTL expiry it schedules one background
//     refresh (singleflight via a CAS gate) and returns the in-memory data.
package gatewaycacherate

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/statreads"
)

// 契约常量（设计 8.1/10）：TTL 内后台刷新一次，超过 MaxStale 视为失效——
// 缓存率维度整体退化为中性。调整任一值属于行为契约变更，须先改设计文档。
const (
	// CacheRateSnapshotTTL is the background refresh interval (section 8.1).
	CacheRateSnapshotTTL = 60 * time.Second
	// CacheRateSnapshotMaxStale is the staleness gate (section 8.1): a
	// snapshot older than this turns the cache-rate dimension neutral.
	CacheRateSnapshotMaxStale = 10 * time.Minute
	// cacheRateWindowHours is the rolling window the cutoff looks back.
	cacheRateWindowHours = 24 * time.Hour
	// cacheRateWarnInterval rate-limits refresh-failure warnings.
	cacheRateWarnInterval = time.Minute
)

// usageStatsHourKey mirrors jobs statsagg hourKey（usage_stats_hourly.
// stat_hour 的键格式，字典序即时间序，支撑 >= cutoff 比较）。
func usageStatsHourKey(t time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02dT%02d", t.Year(), int(t.Month()), t.Day(), t.Hour())
}

// SnapshotSource is the CacheRateSnapshotSource port of section 8.1: rates
// map physical account IDs (usage_stats_hourly scope_id of the global/account
// rows) to their rolling 24h cache-read windows. Safe for concurrent use.
type SnapshotSource struct {
	db       *sql.DB
	postgres bool
	tz       statreads.TimezoneSource
	now      func() time.Time
	warn     func(string)

	// refreshScheduled is the singleflight gate: at most one background
	// refresh is queued at any time; lastAttemptAt throttles re-scheduling
	// (a failing refresh retries at most once per TTL instead of per request).
	refreshScheduled atomic.Bool

	mu            sync.RWMutex
	rates         map[string]gatewayhotquality.CacheRateWindow
	loadedAt      time.Time
	lastAttemptAt time.Time
	lastWarnAt    time.Time
}

// NewSnapshotSource builds the loader. db must point at the stats database
// (usage_stats_hourly); tz is the shared usageStatsTimezone resolver; now and
// warn are injectable for tests (nil falls back to time.Now / no-op).
func NewSnapshotSource(db *sql.DB, postgres bool, tz statreads.TimezoneSource, now func() time.Time, warn func(string)) (*SnapshotSource, error) {
	if db == nil {
		return nil, fmt.Errorf("缓存率快照加载器要求统计库句柄")
	}
	if tz == nil {
		return nil, fmt.Errorf("缓存率快照加载器要求统计时区源")
	}
	if now == nil {
		now = time.Now
	}
	if warn == nil {
		warn = func(string) {}
	}
	return &SnapshotSource{db: db, postgres: postgres, tz: tz, now: now, warn: warn}, nil
}

// Warmup performs the first synchronous refresh (composition-root startup).
// A failure is reported through the rate-limited warn hook and leaves the
// loader unloaded — Snapshot then reports stale until a later refresh lands.
// The attempt timestamp is claimed like a scheduled refresh so the first
// Snapshot after warmup does not queue a redundant cycle.
func (s *SnapshotSource) Warmup(ctx context.Context) {
	s.mu.Lock()
	s.lastAttemptAt = s.now()
	s.mu.Unlock()
	if err := s.refresh(ctx); err != nil {
		s.warnf(err.Error())
	}
}

// Snapshot returns the in-memory window map (nil before the first successful
// load) and the staleness flag (true while unloaded or older than
// CacheRateSnapshotMaxStale — section 8.1: the wiring side must not feed
// CacheRates into the ordering input when stale, keeping the dimension
// neutral). The call path performs no I/O; when the TTL has elapsed it
// schedules exactly one background refresh and returns immediately.
func (s *SnapshotSource) Snapshot() (map[string]gatewayhotquality.CacheRateWindow, bool) {
	s.mu.RLock()
	rates, loadedAt, lastAttemptAt := s.rates, s.loadedAt, s.lastAttemptAt
	s.mu.RUnlock()
	now := s.now()
	if !loadedAt.IsZero() && now.Sub(loadedAt) > CacheRateSnapshotMaxStale {
		// 超龄快照不上报数据：调用方据 stale 关闭缓存率维度（第 7 节）。
		rates = nil
	}
	if lastAttemptAt.IsZero() || now.Sub(lastAttemptAt) >= CacheRateSnapshotTTL {
		s.scheduleRefresh()
	}
	return rates, rates == nil
}

// scheduleRefresh queues one background refresh (CAS gate = singleflight;
// lastAttemptAt is claimed before the goroutine starts so concurrent
// Snapshots cannot queue a storm while the query runs or fails).
func (s *SnapshotSource) scheduleRefresh() {
	if !s.refreshScheduled.CompareAndSwap(false, true) {
		return
	}
	s.mu.Lock()
	s.lastAttemptAt = s.now()
	s.mu.Unlock()
	go func() {
		defer s.refreshScheduled.Store(false)
		s.refresh(context.Background())
	}()
}

// refresh runs one aggregate SQL cycle (section 8.1): resolve the effective
// usageStatsTimezone, cut off 24h in that timezone's hour-key space, and
// aggregate the global/account rows per physical account. Failures keep the
// previous snapshot (never a default timezone, never a partial map).
func (s *SnapshotSource) refresh(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	name, err := s.tz(ctx)
	if err != nil {
		s.warnf("缓存率快照刷新失败（解析统计时区）：" + err.Error())
		return err
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		err = fmt.Errorf("统计时区不存在：%s", name)
		s.warnf("缓存率快照刷新失败：" + err.Error())
		return err
	}
	cutoff := usageStatsHourKey(s.now().In(location).Add(-cacheRateWindowHours))
	// 契约 8.1 聚合 SQL 原文；唯一参数是 cutoff 小时键（PG 走 $1 绑定）。
	query := fmt.Sprintf(`SELECT scope_id, SUM(cache_read_tokens), SUM(input_tokens)
		FROM %s WHERE system_account_id = 'global' AND scope_type = 'account' AND stat_hour >= %s
		GROUP BY scope_id`, s.table("usage_stats_hourly"), s.dialectPlaceholder())
	rows, err := s.db.QueryContext(ctx, query, cutoff)
	if err != nil {
		s.warnf("缓存率快照刷新失败：" + err.Error())
		return err
	}
	defer rows.Close()
	rates := map[string]gatewayhotquality.CacheRateWindow{}
	for rows.Next() {
		var scopeID string
		var window gatewayhotquality.CacheRateWindow
		if err := rows.Scan(&scopeID, &window.CacheReadTokens, &window.InputTokens); err != nil {
			s.warnf("缓存率快照刷新失败：" + err.Error())
			return err
		}
		rates[scopeID] = window
	}
	if err := rows.Err(); err != nil {
		s.warnf("缓存率快照刷新失败：" + err.Error())
		return err
	}
	s.mu.Lock()
	s.rates = rates
	s.loadedAt = s.now()
	s.mu.Unlock()
	return nil
}

// warnf emits a refresh-failure warning at most once per minute (section 8.1
// 限频 WARN); Warmup 的首次失败同样经此节流，保持“失败仅告警”的出口唯一。
func (s *SnapshotSource) warnf(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.lastWarnAt.IsZero() && now.Sub(s.lastWarnAt) < cacheRateWarnInterval {
		return
	}
	s.lastWarnAt = now
	s.warn(message)
}

// table qualifies stats-schema tables for PostgreSQL (same contract as
// accounts.StatsUsageSource.table: PG reaches juhe_stats through schema
// qualification on the shared pool, SQLite uses the bare name).
func (s *SnapshotSource) table(name string) string {
	if s.postgres {
		return "juhe_stats." + name
	}
	return name
}

// dialectPlaceholder renders the placeholder for the query's single cutoff
// parameter: PostgreSQL binds positionally with $N, SQLite with ?.
func (s *SnapshotSource) dialectPlaceholder() string {
	if s.postgres {
		return "$1"
	}
	return "?"
}
