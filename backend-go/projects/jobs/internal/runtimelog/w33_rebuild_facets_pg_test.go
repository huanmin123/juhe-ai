package runtimelog

// W33 回归（真实 PG）：facet 全量重建入口（RebuildPostgresFacets）。
//
// 背景：W23 修复（commit ef61f99aa）把 facet 链的 timestamptz 读出统一为
// nodeISO 文本后，新写入已正确；但修复前增量写入对与 cutoff 同一自然日的
// 记录系统性少计（空格<'T' 误判），历史行计数无法由增量链自愈。本文件验证
// 一次性重建：
//  1. 从 runtime_logs 直接聚合的预期（cutoff 边界两侧、跨 level/event、空白
//     event）与重建结果一致，且整体替换陈旧 facet 行（历史少计与幽灵行）；
//  2. 重建幂等：重跑结果不变、计数不翻倍；
//  3. 与增量写入路径交叉验证：同批数据「增量写入得到的 facet 状态 ==
//     重建结果」；
//  4. owner lease 被运行中实例持有时显式拒绝，facet 表不动；
//  5. 空 runtime_logs 不留零行 summary（对齐增量链 total_count<=0 删除语义）。

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// w33SeededRetentionDays 与 openKitPostgresStore 的业务设置种子
// （kitBusinessSettingsSeed）一致：重建入口读取的生效保留天数。
const w33SeededRetentionDays = 14

// w33CutoffEstimate 与重建内部的 cutoff 派生同构（now - 保留天数，毫秒截断）；
// 测试造数与重建调用之间只差毫秒级时钟误差，±1h 的夹具间隔远大于该误差。
func w33CutoffEstimate() time.Time {
	return time.Now().UTC().AddDate(0, 0, -w33SeededRetentionDays).Truncate(time.Millisecond)
}

type w33EventFacet struct {
	count      int
	latestTime string
}

type w33FacetSnapshot struct {
	summaryExists bool
	totalCount    int64
	earliest      string
	latest        string
	levels        map[string]int
	events        map[string]w33EventFacet
}

// w33FixtureRecords 是重建测试的共享夹具：cutoff 之下两条（含边界下侧同一
// 对比点）、cutoff 之上五条，覆盖多 level、重复 event、空 event 与纯空白
// event（后者只进 summary/level，不进 event facets）。
func w33FixtureRecords(now time.Time, cutoff time.Time) []Record {
	recordTimes := map[string]string{
		"w33-old":            nodeISO(now.AddDate(0, 0, -20)),
		"w33-boundary-below": nodeISO(cutoff.Add(-time.Hour)),
		"w33-boundary-above": nodeISO(cutoff.Add(time.Hour)),
		"w33-usage-a":        nodeISO(now.Add(-2 * time.Hour)),
		"w33-usage-b":        nodeISO(now.Add(-time.Hour)),
		"w33-blank-event":    nodeISO(now.Add(-45 * time.Minute)),
		"w33-silent":         nodeISO(now.Add(-30 * time.Minute)),
	}
	order := []string{"w33-old", "w33-boundary-below", "w33-boundary-above", "w33-usage-a", "w33-usage-b", "w33-blank-event", "w33-silent"}
	levels := map[string]string{
		"w33-old":            "info",
		"w33-boundary-below": "warn",
		"w33-boundary-above": "info",
		"w33-usage-a":        "warn",
		"w33-usage-b":        "warn",
		"w33-blank-event":    "error",
		"w33-silent":         "error",
	}
	events := map[string]string{
		"w33-old":            "w33-old-event",
		"w33-boundary-below": "w33-boundary-below",
		"w33-boundary-above": "w33-boundary-above",
		"w33-usage-a":        "w33-usage",
		"w33-usage-b":        "w33-usage",
		"w33-blank-event":    "\t",
		"w33-silent":         "",
	}
	records := make([]Record, 0, len(order))
	for index, id := range order {
		records = append(records, Record{
			ID:         id,
			LogFile:    "w33.log",
			LogOffset:  int64(index + 1),
			LineNumber: int64(index + 1),
			Time:       recordTimes[id],
			CreatedAt:  recordTimes[id],
			Level:      levels[id],
			Event:      events[id],
			Message:    "W33 facet 重建夹具 " + id,
		})
	}
	return records
}

// w33ExpectedSnapshot 是夹具的人算期望：计入 5 条（w33-old、
// w33-boundary-below 被 cutoff 排除），earliest 为 boundary-above，latest 为
// w33-silent（now-30m）。
func w33ExpectedSnapshot(now time.Time, cutoff time.Time) w33FacetSnapshot {
	boundaryAbove := nodeISO(cutoff.Add(time.Hour))
	return w33FacetSnapshot{
		summaryExists: true,
		totalCount:    5,
		earliest:      boundaryAbove,
		latest:        nodeISO(now.Add(-30 * time.Minute)),
		levels:        map[string]int{"info": 1, "warn": 2, "error": 2},
		events: map[string]w33EventFacet{
			"w33-boundary-above": {count: 1, latestTime: boundaryAbove},
			"w33-usage":          {count: 2, latestTime: nodeISO(now.Add(-time.Hour))},
		},
	}
}

func w33ReadFacetSnapshot(t *testing.T, store *postgresStore) w33FacetSnapshot {
	t.Helper()
	ctx := context.Background()
	snapshot := w33FacetSnapshot{levels: map[string]int{}, events: map[string]w33EventFacet{}}
	err := store.pool.QueryRow(ctx,
		"SELECT total_count, COALESCE("+postgresNodeInstantText("earliest_time")+", ''), COALESCE("+postgresNodeInstantText("latest_time")+", '') FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1",
		facetBucketKey).Scan(&snapshot.totalCount, &snapshot.earliest, &snapshot.latest)
	switch {
	case err == nil:
		snapshot.summaryExists = true
	case errors.Is(err, pgx.ErrNoRows):
	default:
		t.Fatal(err)
	}
	levelRows, err := store.pool.Query(ctx, "SELECT level, count FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = $1", facetBucketKey)
	if err != nil {
		t.Fatal(err)
	}
	for levelRows.Next() {
		var level string
		var count int
		if err := levelRows.Scan(&level, &count); err != nil {
			levelRows.Close()
			t.Fatal(err)
		}
		snapshot.levels[level] = count
	}
	if err := levelRows.Err(); err != nil {
		levelRows.Close()
		t.Fatal(err)
	}
	levelRows.Close()
	eventRows, err := store.pool.Query(ctx, "SELECT event, count, COALESCE("+postgresNodeInstantText("latest_time")+", '') FROM juhe_dataset.runtime_log_event_facets WHERE bucket_key = $1", facetBucketKey)
	if err != nil {
		t.Fatal(err)
	}
	for eventRows.Next() {
		var event string
		var facet w33EventFacet
		if err := eventRows.Scan(&event, &facet.count, &facet.latestTime); err != nil {
			eventRows.Close()
			t.Fatal(err)
		}
		snapshot.events[event] = facet
	}
	if err := eventRows.Err(); err != nil {
		eventRows.Close()
		t.Fatal(err)
	}
	eventRows.Close()
	return snapshot
}

// w33DirectAggregate 独立于重建实现，直接对 runtime_logs 聚合出三表期望
// （边界用 ORDER BY..LIMIT 1 而非 min/max，计数用独立 GROUP BY），作为重建
// 结果的对照真值。
func w33DirectAggregate(t *testing.T, store *postgresStore, cutoff time.Time) w33FacetSnapshot {
	t.Helper()
	ctx := context.Background()
	snapshot := w33FacetSnapshot{levels: map[string]int{}, events: map[string]w33EventFacet{}}
	if err := store.pool.QueryRow(ctx, "SELECT COUNT(*) FROM juhe_dataset.runtime_logs WHERE time >= $1", cutoff).Scan(&snapshot.totalCount); err != nil {
		t.Fatal(err)
	}
	snapshot.summaryExists = snapshot.totalCount > 0
	if snapshot.totalCount == 0 {
		return snapshot
	}
	if err := store.pool.QueryRow(ctx, "SELECT "+postgresNodeInstantText("time")+" FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time ASC, id ASC LIMIT 1", cutoff).Scan(&snapshot.earliest); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, "SELECT "+postgresNodeInstantText("time")+" FROM juhe_dataset.runtime_logs WHERE time >= $1 ORDER BY time DESC, id DESC LIMIT 1", cutoff).Scan(&snapshot.latest); err != nil {
		t.Fatal(err)
	}
	levelRows, err := store.pool.Query(ctx, "SELECT level, COUNT(*) FROM juhe_dataset.runtime_logs WHERE time >= $1 GROUP BY level", cutoff)
	if err != nil {
		t.Fatal(err)
	}
	for levelRows.Next() {
		var level string
		var count int
		if err := levelRows.Scan(&level, &count); err != nil {
			levelRows.Close()
			t.Fatal(err)
		}
		snapshot.levels[level] = count
	}
	if err := levelRows.Err(); err != nil {
		levelRows.Close()
		t.Fatal(err)
	}
	levelRows.Close()
	eventRows, err := store.pool.Query(ctx, "SELECT event, COUNT(*), COALESCE("+postgresNodeInstantText("max(time)")+", '') FROM juhe_dataset.runtime_logs WHERE time >= $1 AND COALESCE(event, '') !~ '^\\s*$' GROUP BY event", cutoff)
	if err != nil {
		t.Fatal(err)
	}
	for eventRows.Next() {
		var event string
		var facet w33EventFacet
		if err := eventRows.Scan(&event, &facet.count, &facet.latestTime); err != nil {
			eventRows.Close()
			t.Fatal(err)
		}
		snapshot.events[event] = facet
	}
	if err := eventRows.Err(); err != nil {
		eventRows.Close()
		t.Fatal(err)
	}
	eventRows.Close()
	return snapshot
}

func w33AssertSnapshotsEqual(t *testing.T, label string, got w33FacetSnapshot, want w33FacetSnapshot) {
	t.Helper()
	if got.summaryExists != want.summaryExists {
		t.Fatalf("%s: summary 行存在性 = %v, want %v", label, got.summaryExists, want.summaryExists)
	}
	if got.totalCount != want.totalCount {
		t.Fatalf("%s: total_count = %d, want %d", label, got.totalCount, want.totalCount)
	}
	if got.earliest != want.earliest {
		t.Fatalf("%s: earliest_time = %q, want %q", label, got.earliest, want.earliest)
	}
	if got.latest != want.latest {
		t.Fatalf("%s: latest_time = %q, want %q", label, got.latest, want.latest)
	}
	if !reflect.DeepEqual(got.levels, want.levels) {
		t.Fatalf("%s: level facets = %v, want %v", label, got.levels, want.levels)
	}
	if !reflect.DeepEqual(got.events, want.events) {
		t.Fatalf("%s: event facets = %v, want %v", label, got.events, want.events)
	}
}

// w33SeedFixtureRows 经增量 Commit 路径写入夹具（retentionCutoff 与重建的
// cutoff 派生一致），随后释放租约，让重建入口可获取。
func w33SeedFixtureRows(t *testing.T, store *postgresStore, now time.Time, cutoff time.Time) {
	t.Helper()
	ctx := context.Background()
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w33-seed-owner", time.Hour)
	if err != nil || !acquired {
		t.Fatalf("获取种子 owner lease 失败: acquired=%v err=%v", acquired, err)
	}
	records := w33FixtureRecords(now, cutoff)
	cursor := Cursor{LogFile: "w33.log", CursorOffset: int64(len(records)), LineNumber: int64(len(records)), FileSize: int64(len(records))}
	if err := store.Commit(ctx, lease, records, cursor, cutoff); err != nil {
		t.Fatalf("Commit 失败: %v", err)
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
}

func w33RebuildConfig() Config {
	return Config{OwnerID: "w33-rebuild-owner", OwnerLease: time.Minute, RetentionDays: w33SeededRetentionDays}
}

func TestW33RebuildPostgresFacetsReplacesStaleRowsMatchesDirectAggregation(t *testing.T) {
	store, _ := openKitPostgresStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cutoff := w33CutoffEstimate()
	w33SeedFixtureRows(t, store, now, cutoff)

	// 预置历史损伤（模拟 W23 修复前的生产形态）：total_count 少计 2、
	// earliest 停留在更早位置，另有增量链永远不会产生的幽灵 level/event 行。
	nowText := nowISO()
	if _, err := store.pool.Exec(ctx, "UPDATE juhe_dataset.runtime_log_facet_summary SET total_count = total_count - 2, earliest_time = $1, updated_at = $2 WHERE bucket_key = $3", nodeISO(cutoff.AddDate(0, 0, -20)), nowText, facetBucketKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "INSERT INTO juhe_dataset.runtime_log_level_facets (bucket_key, level, count, updated_at) VALUES ($1, 'w33-ghost-level', 99, $2)", facetBucketKey, nowText); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "INSERT INTO juhe_dataset.runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at) VALUES ($1, 'w33-ghost-event', 7, NULL, $2)", facetBucketKey, nowText); err != nil {
		t.Fatal(err)
	}
	corrupted := w33ReadFacetSnapshot(t, store)
	if corrupted.totalCount != 3 {
		t.Fatalf("预置损伤后 total_count = %d, want 3（少计 2 的历史形态）", corrupted.totalCount)
	}

	result, err := RebuildPostgresFacets(ctx, store, w33RebuildConfig())
	if err != nil {
		t.Fatalf("重建失败: %v", err)
	}
	expected := w33ExpectedSnapshot(now, cutoff)
	if result.BucketKey != facetBucketKey || result.TotalCount != expected.totalCount || result.EarliestTime != expected.earliest || result.LatestTime != expected.latest {
		t.Fatalf("重建结果 = %+v, want bucket=%s total=%d earliest=%q latest=%q", result, facetBucketKey, expected.totalCount, expected.earliest, expected.latest)
	}
	w33AssertSnapshotsEqual(t, "重建后", w33ReadFacetSnapshot(t, store), expected)
	w33AssertSnapshotsEqual(t, "直接聚合对照", w33DirectAggregate(t, store, cutoff), expected)

	// 幂等：重跑结果与快照逐字段不变，计数不翻倍。
	resultAgain, err := RebuildPostgresFacets(ctx, store, w33RebuildConfig())
	if err != nil {
		t.Fatalf("重入重建失败: %v", err)
	}
	if resultAgain != result {
		t.Fatalf("重入重建结果变化: %+v, want %+v", resultAgain, result)
	}
	w33AssertSnapshotsEqual(t, "重入后", w33ReadFacetSnapshot(t, store), expected)
}

func TestW33RebuildPostgresFacetsMatchesIncrementalCommitPath(t *testing.T) {
	store, _ := openKitPostgresStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	cutoff := w33CutoffEstimate()
	w33SeedFixtureRows(t, store, now, cutoff)
	incremental := w33ReadFacetSnapshot(t, store)
	w33AssertSnapshotsEqual(t, "增量写入基线（健全性）", incremental, w33ExpectedSnapshot(now, cutoff))

	result, err := RebuildPostgresFacets(ctx, store, w33RebuildConfig())
	if err != nil {
		t.Fatalf("重建失败: %v", err)
	}
	rebuilt := w33ReadFacetSnapshot(t, store)
	w33AssertSnapshotsEqual(t, "重建 vs 增量", rebuilt, incremental)
	if result.TotalCount != incremental.totalCount {
		t.Fatalf("重建 total_count = %d, want 增量基线 %d", result.TotalCount, incremental.totalCount)
	}
}

func TestW33RebuildPostgresFacetsRefusesWhenOwnerLeaseHeld(t *testing.T) {
	store, _ := openKitPostgresStore(t)
	ctx := context.Background()
	// 刻意不持有 lease 值：租约由 w33-blocker-owner 占据直至过期，重建必须
	// 在入口处显式失败。
	_, acquired, err := store.AcquireOwnerLease(ctx, "w33-blocker-owner", time.Hour)
	if err != nil || !acquired {
		t.Fatalf("获取阻塞 owner lease 失败: acquired=%v err=%v", acquired, err)
	}
	_, err = RebuildPostgresFacets(ctx, store, w33RebuildConfig())
	if err == nil {
		t.Fatal("owner lease 被持有时重建必须显式失败")
	}
	if !strings.Contains(err.Error(), "owner lease") {
		t.Fatalf("重建错误必须披露 owner lease 冲突: %v", err)
	}
	remaining := w33ReadFacetSnapshot(t, store)
	if remaining.summaryExists || len(remaining.levels) != 0 || len(remaining.events) != 0 {
		t.Fatalf("被拒绝的重建不得改动 facet 表: %+v", remaining)
	}
}

func TestW33RebuildPostgresFacetsEmptyRuntimeLogsLeavesNoRows(t *testing.T) {
	store, _ := openKitPostgresStore(t)
	ctx := context.Background()
	result, err := RebuildPostgresFacets(ctx, store, w33RebuildConfig())
	if err != nil {
		t.Fatalf("空库重建失败: %v", err)
	}
	if result.BucketKey != facetBucketKey || result.TotalCount != 0 || result.EarliestTime != "" || result.LatestTime != "" {
		t.Fatalf("空库重建结果 = %+v, want total=0 且时间字段为空", result)
	}
	// 对齐增量链语义：零行 bucket 不留 summary 行，level/event 亦无行。
	w33AssertSnapshotsEqual(t, "空库重建后", w33ReadFacetSnapshot(t, store), w33FacetSnapshot{levels: map[string]int{}, events: map[string]w33EventFacet{}})
}
