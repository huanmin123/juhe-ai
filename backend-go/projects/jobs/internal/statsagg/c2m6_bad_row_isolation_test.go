package statsagg

// C2-M6 回归（2026-11-01）：单行缺 account_access_type 不得卡死整批统计。
//
// 缺陷：postgresAccountQualityStatsSystemAccountID 对 nil/空 access type 返回
// error，aggregateRows 的调用点直接 return → 整个聚合事务回滚、stats_job_state
// 游标不前进 → 同一坏行让后续所有轮次永久停摆（其它正常记录也无法聚合）。
//
// 修复：进入任何统计桶前逐行判定坏行（与错误臂同源的归属字段检查），坏行
// Warn（record id + scope 标识）+ 累计跳过计数后整体跳过——不进入任何统计桶、
// 不做默认归属推断；游标照常推进。其它真实 DB 错误仍整批返回错误。

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestC2M6BadAccessTypeRowIsolatedAndCursorAdvances 钉住主场景：1 条空
// access_type 行 + 1 条正常行 → 正常行被聚合、游标前进到批尾、坏行被跳过并
// Warn、下一轮不再卡住。
func TestC2M6BadAccessTypeRowIsolatedAndCursorAdvances(t *testing.T) {
	env := newPGTestEnv(t)
	var logs bytes.Buffer
	aggregator := env.aggregator()
	aggregator.Logger = slog.New(slog.NewJSONHandler(&logs, nil))

	// 坏行：有 account/apiKey 与真实 token 事实（会进入账户质量统计口径），
	// 但 account_access_type / account_owner_system_account_id 缺失。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-bad-access", SystemAccountID: "alice", TraceID: "tr-bad", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), AccountID: strPtr("acc-1"),
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-bad"),
		Success: 1, InputTokens: f64Ptr(11),
		CreatedAt: "2026-04-18T10:10:00.000Z",
	})
	// 正常行：完整归属字段（owner）。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-good", SystemAccountID: "alice", TraceID: "tr-good", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), AccountID: strPtr("acc-1"),
		AccountOwnerSystemAccountID: strPtr("alice"), AccountAccessType: strPtr("owner"),
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-good"),
		Success: 1, InputTokens: f64Ptr(100),
		CreatedAt: "2026-04-18T10:20:00.000Z",
	})

	processed, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{
		BatchSize: 10, SafeCreatedBefore: "2026-04-18T11:00:00.000Z",
	})
	if err != nil {
		t.Fatalf("坏行必须逐行隔离而不是整批报错: %v", err)
	}
	if processed != 2 {
		t.Fatalf("processed = %d, want 2（坏行也随游标前进）", processed)
	}

	// 正常行被聚合。
	env.assertFloats("good model aggregated",
		env.queryRowFloats(`SELECT request_count, input_tokens FROM juhe_stats.usage_stats_totals
			WHERE scope_type='model' AND scope_id='gpt-good'`),
		1, 100)
	// 坏行不进任何统计桶（不伪造归属，也不产生 system_account/global 请求数）。
	env.assertFloats("bad model must not be aggregated",
		env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.usage_stats_totals
			WHERE scope_type='model' AND scope_id='gpt-bad'`),
		0)
	env.assertFloats("bad row contributes no request",
		env.queryRowFloats(`SELECT COALESCE(SUM(request_count), 0) FROM juhe_stats.usage_stats_totals
			WHERE system_account_id='global' AND scope_type='system_account' AND scope_id='global'`),
		1)

	// 游标推进到批尾（坏行在前，正常行在后），不会被坏行钉住。
	if got := env.queryString(`SELECT cursor_created_at || '|' || cursor_id FROM juhe_stats.stats_job_state
		WHERE scope_type='global' AND job_name='usage_stats_aggregation'`); len(got) != 1 || got[0] != "2026-04-18T10:20:00.000Z|rec-good" {
		t.Fatalf("cursor = %v, want 批尾 rec-good", got)
	}
	// Warn 含 record id 与原因（不含请求内容）。
	logText := logs.String()
	if !strings.Contains(logText, "usage_stats_record_skipped") || !strings.Contains(logText, "rec-bad-access") {
		t.Fatalf("缺少坏行 Warn 留痕: %s", logText)
	}
	if !strings.Contains(logText, "account_access_type") {
		t.Fatalf("Warn 必须指名缺失字段: %s", logText)
	}
	if aggregator.SkippedRecordCount() != 1 {
		t.Fatalf("skipped count = %d, want 1", aggregator.SkippedRecordCount())
	}

	// 下一轮不重复卡住：无剩余记录、无错误、计数不重复累计。
	processed, err = aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{
		BatchSize: 10, SafeCreatedBefore: "2026-04-18T11:00:00.000Z",
	})
	if err != nil || processed != 0 {
		t.Fatalf("next round processed=%d err=%v, want 0/nil", processed, err)
	}
	if aggregator.SkippedRecordCount() != 1 {
		t.Fatalf("next round must not re-count the already-passed row: %d", aggregator.SkippedRecordCount())
	}
}

// TestC2M6OnlyBadRowBatchStillAdvancesCursor：批内只有坏行时也不能回滚事务，
// 游标仍推进（否则后续正常记录永远排在坏行之后无法被消费）。
func TestC2M6OnlyBadRowBatchStillAdvancesCursor(t *testing.T) {
	env := newPGTestEnv(t)
	aggregator := env.aggregator()
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-only-bad", SystemAccountID: "alice", TraceID: "tr-only-bad", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), AccountID: strPtr("acc-1"),
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-only-bad"),
		Success: 1, InputTokens: f64Ptr(7),
		CreatedAt: "2026-04-18T10:10:00.000Z",
	})

	processed, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{
		BatchSize: 10, SafeCreatedBefore: "2026-04-18T11:00:00.000Z",
	})
	if err != nil || processed != 1 {
		t.Fatalf("only-bad batch processed=%d err=%v, want 1/nil", processed, err)
	}
	if got := env.queryString(`SELECT cursor_id FROM juhe_stats.stats_job_state
		WHERE scope_type='global' AND job_name='usage_stats_aggregation'`); len(got) != 1 || got[0] != "rec-only-bad" {
		t.Fatalf("cursor = %v, want rec-only-bad", got)
	}
	env.assertFloats("bad row must not be aggregated",
		env.queryRowFloats(`SELECT COUNT(*) FROM juhe_stats.usage_stats_totals`),
		0)
}

// TestC2M6RowSkipReasonMatchesErrorArm 钉住跳过判定与
// postgresAccountQualityStatsSystemAccountID 错误臂同源，避免两处口径漂移。
func TestC2M6RowSkipReasonMatchesErrorArm(t *testing.T) {
	base := UsageStatsRecordRow{
		ID: "rec-1", SystemAccountID: "alice", TrafficSource: "gateway", Success: 1,
		AccountID: strPtr("acc-1"), APIKeyID: strPtr("key-1"),
	}
	cases := []struct {
		name     string
		mutate   func(*UsageStatsRecordRow)
		wantSkip bool
		wantErr  bool
	}{
		{name: "完整 owner 行", mutate: func(r *UsageStatsRecordRow) {
			r.AccountOwnerSystemAccountID = strPtr("alice")
			r.AccountAccessType = strPtr("owner")
		}},
		{name: "缺 access type", mutate: func(*UsageStatsRecordRow) {}, wantSkip: true, wantErr: true},
		{name: "缺 owner", mutate: func(r *UsageStatsRecordRow) {
			r.AccountAccessType = strPtr("owner")
		}, wantSkip: true, wantErr: true},
		{name: "account_authorized 不要求 owner", mutate: func(r *UsageStatsRecordRow) {
			r.AccountAccessType = strPtr("account_authorized")
		}},
		{name: "探针流量不进入质量统计", mutate: func(r *UsageStatsRecordRow) {
			r.TrafficSource = "runtime_recovery_probe"
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			row := base
			testCase.mutate(&row)
			reason := usageStatsRowSkipReason(row)
			if got := reason != ""; got != testCase.wantSkip {
				t.Fatalf("usageStatsRowSkipReason = %q, wantSkip %v", reason, testCase.wantSkip)
			}
			_, err := postgresAccountQualityStatsSystemAccountID(row)
			if !shouldRecordPostgresAccountQualityStats(row) {
				// 不在账户质量统计口径：调用点不可达，跳过判定必须为 false。
				if testCase.wantSkip || err == nil {
					t.Fatalf("非质量统计流量不应被隔离且错误臂不可达: skip=%v err=%v", testCase.wantSkip, err)
				}
				return
			}
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("错误臂必须与跳过判定同源，得到 nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
		})
	}
}
