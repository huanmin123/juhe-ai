package runtimelog

// W23 回归（真实 PG）：facet 计数的时间比较格式归一。
//
// 缺陷：retainedFacetRows 以 nodeISO 毫秒文本（"2026-10-24T04:00:00.000Z"）
// 与 PG 臂行时间做字典序比较，而行时间来自 timestamptz 默认 ::text 渲染
// （"2026-10-24 04:00:00+00"）。空格(0x20) < 'T'，与 cutoff 同一自然日的
// 记录被系统性判为早于 cutoff，facet 计数少计。
//
// 修复：facet 链全部 timestamptz 读出经 postgresNodeInstantText 归一为
// nodeISO 同格式，字典序恒等于时间序。本测试用真实 PG 断言：
//  1. 与 cutoff 同一自然日、早于 cutoff 时刻的记录不计入 facet；
//  2. 同一自然日、晚于 cutoff 时刻的记录计入（修复前被空格<'T' 误判排除）；
//  3. cleanup 扣减链的基线（earliest_time）读出同格式，删除基线之下记录
//     不影响 facet 计数。

import (
	"context"
	"testing"
	"time"
)

func TestW23FacetCountsSameDayRowsAroundCutoffOnPostgres(t *testing.T) {
	store, _ := openKitPostgresStore(t)
	ctx := context.Background()

	lease, acquired, err := store.AcquireOwnerLease(ctx, "w23-owner", time.Hour)
	if err != nil || !acquired {
		t.Fatalf("获取 owner lease 失败: acquired=%v err=%v", acquired, err)
	}

	cutoff := time.Date(2026, 10, 24, 4, 0, 0, 0, time.UTC)
	records := []Record{
		{ID: "w23-before-cutoff", LogFile: "w23.log", LogOffset: 1, LineNumber: 1, Time: "2026-10-24T03:30:00.000Z", CreatedAt: "2026-10-24T03:30:00.000Z", Level: "info", Event: "w23-before", Message: "同日早于 cutoff"},
		{ID: "w23-after-cutoff", LogFile: "w23.log", LogOffset: 2, LineNumber: 2, Time: "2026-10-24T04:30:00.000Z", CreatedAt: "2026-10-24T04:30:00.000Z", Level: "warn", Event: "w23-after", Message: "同日晚于 cutoff"},
	}
	cursor := Cursor{LogFile: "w23.log", CursorOffset: 2, LineNumber: 2, FileSize: 2}
	if err := store.Commit(ctx, lease, records, cursor, cutoff); err != nil {
		t.Fatalf("Commit 失败: %v", err)
	}

	var totalCount int
	var earliestText, latestText string
	if err := store.pool.QueryRow(ctx,
		"SELECT total_count, COALESCE("+postgresNodeInstantText("earliest_time")+", ''), COALESCE("+postgresNodeInstantText("latest_time")+", '') FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1",
		facetBucketKey).Scan(&totalCount, &earliestText, &latestText); err != nil {
		t.Fatalf("读取 facet summary 失败: %v", err)
	}
	if totalCount != 1 {
		t.Fatalf("同日晚于 cutoff 的记录必须计入 facet（W23 修复前同日记录被空格<'T' 误判排除）: total_count=%d want=1", totalCount)
	}
	if earliestText != "2026-10-24T04:30:00.000Z" || latestText != "2026-10-24T04:30:00.000Z" {
		t.Fatalf("facet 时间边界必须为保留记录且为 nodeISO 格式: earliest=%q latest=%q", earliestText, latestText)
	}

	var warnCount, infoCount int
	rows, err := store.pool.Query(ctx, "SELECT level, count FROM juhe_dataset.runtime_log_level_facets WHERE bucket_key = $1", facetBucketKey)
	if err != nil {
		t.Fatalf("读取 level facets 失败: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var level string
		var count int
		if err := rows.Scan(&level, &count); err != nil {
			t.Fatal(err)
		}
		switch level {
		case "warn":
			warnCount = count
		case "info":
			infoCount = count
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if warnCount != 1 || infoCount != 0 {
		t.Fatalf("level facets 必须只计入 cutoff 之后的记录: warn=%d info=%d want warn=1 info=0", warnCount, infoCount)
	}

	var eventCount int
	var eventLatest string
	if err := store.pool.QueryRow(ctx,
		"SELECT count, COALESCE("+postgresNodeInstantText("latest_time")+", '') FROM juhe_dataset.runtime_log_event_facets WHERE bucket_key = $1 AND event = $2",
		facetBucketKey, "w23-after").Scan(&eventCount, &eventLatest); err != nil {
		t.Fatalf("读取 event facet 失败: %v", err)
	}
	if eventCount != 1 || eventLatest != "2026-10-24T04:30:00.000Z" {
		t.Fatalf("event facet 计数与最新时间不符: count=%d latest=%q", eventCount, eventLatest)
	}

	// cleanup 链：删除早于 cutoff 的记录后，facet 计数保持（被删记录不在
	// facet 计数内，扣减基线 earliest_time 比较必须与行时间同格式）。
	result, err := store.Cleanup(ctx, lease, cutoff, 100, 10)
	if err != nil {
		t.Fatalf("Cleanup 失败: %v", err)
	}
	if result.RuntimeLogs != 1 {
		t.Fatalf("cleanup 必须只删除早于 cutoff 的 1 条: %+v", result)
	}
	if err := store.pool.QueryRow(ctx,
		"SELECT total_count, COALESCE("+postgresNodeInstantText("earliest_time")+", '') FROM juhe_dataset.runtime_log_facet_summary WHERE bucket_key = $1",
		facetBucketKey).Scan(&totalCount, &earliestText); err != nil {
		t.Fatalf("cleanup 后读取 facet summary 失败: %v", err)
	}
	if totalCount != 1 || earliestText != "2026-10-24T04:30:00.000Z" {
		t.Fatalf("cleanup 删除基线之下记录不得影响 facet 计数: total_count=%d earliest=%q want 1 / 2026-10-24T04:30:00.000Z", totalCount, earliestText)
	}
}
