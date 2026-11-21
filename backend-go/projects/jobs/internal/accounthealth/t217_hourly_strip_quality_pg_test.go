package accounthealth

// T217 AI 健康小时条带写侧口径统一（真实 PG，pgtestkit 隔离库）：
// 非质量终态（upstream_http_failure / opaque_upstream / gateway_capacity /
// gateway_policy / downstream_closed / framing_complete_neutral / stale）不再
// 写 account_health_hourly——与 gateway statreads j1NonQualityOutcomes、统计
// 口径 §2.9 成对；质量终态（complete_success / upstream_failure /
// probe_task_failure）保持 success/failure 写入。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

type t217HourlySink struct {
	observations []AccountHealthHourlyObservation
	err          error
}

func (s *t217HourlySink) RecordAccountHealthHourly(ctx context.Context, observation AccountHealthHourlyObservation) error {
	if s.err != nil {
		return s.err
	}
	s.observations = append(s.observations, observation)
	return nil
}

func newT217HourlyStripDB(t *testing.T) *ProjectionBusinessDB {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `CREATE TABLE juhe_business.accounts (
			id text PRIMARY KEY,
			vendor_code text,
			system_account_id text NOT NULL
		)`)
		if err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, `INSERT INTO juhe_business.accounts (id, vendor_code, system_account_id)
			VALUES ('acct-t217', 'gpt', 'sys-t217')`)
		return err
	})
	business, err := NewProjectionBusinessDB(db)
	if err != nil {
		t.Fatalf("business handle: %v", err)
	}
	return business
}

// TestT217NonQualityOutcomeListLocked 把名单全集硬编码锚定到统计口径 §2.9
// 的固定列表：修改 nonQualityHealthOutcomes 必须同步改本断言与 gateway 读面
// j1NonQualityOutcomes（跨 module 无编译期锚定，以此测试作为漂移闸）。
func TestT217NonQualityOutcomeListLocked(t *testing.T) {
	want := []string{
		"stale",
		"upstream_http_failure",
		"framing_complete_neutral",
		"opaque_upstream",
		"gateway_capacity",
		"gateway_policy",
		"downstream_closed",
	}
	if len(nonQualityHealthOutcomes) != len(want) {
		t.Fatalf("nonQualityHealthOutcomes 成员数 = %d, want %d（%v）", len(nonQualityHealthOutcomes), len(want), nonQualityHealthOutcomes)
	}
	for _, outcome := range want {
		if !nonQualityHealthOutcomes[outcome] {
			t.Fatalf("非质量名单缺少 %s（须与 gateway 读面、统计口径 §2.9 三处同步）", outcome)
		}
	}
}

func TestT217HourlyStripSkipsNonQualityOutcomes(t *testing.T) {
	business := newT217HourlyStripDB(t)
	observedAt := time.Date(2026, 11, 21, 9, 30, 0, 0, time.UTC)
	nonQuality := []string{
		OutcomeStale,
		"upstream_http_failure",
		"framing_complete_neutral",
		"opaque_upstream",
		"gateway_capacity",
		"gateway_policy",
		"downstream_closed",
	}
	for _, outcome := range nonQuality {
		sink := &t217HourlySink{}
		if err := recordAccountHealthHourlyObservation(context.Background(), sink, business, Outcome{
			OutcomeID: "out-" + outcome, AccountID: "acct-t217", Outcome: outcome, ObservedAt: observedAt,
		}); err != nil {
			t.Fatalf("非质量终态 %s 应静默跳过，err=%v", outcome, err)
		}
		if len(sink.observations) != 0 {
			t.Fatalf("非质量终态 %s 不得写小时条带: %#v", outcome, sink.observations)
		}
	}
}

func TestT217HourlyStripKeepsQualityOutcomes(t *testing.T) {
	business := newT217HourlyStripDB(t)
	observedAt := time.Date(2026, 11, 21, 9, 30, 0, 0, time.UTC)
	cases := []struct {
		outcome string
		success bool
	}{
		{"complete_success", true},
		{"upstream_failure", false},
		{"probe_task_failure", false},
	}
	for _, tc := range cases {
		sink := &t217HourlySink{}
		if err := recordAccountHealthHourlyObservation(context.Background(), sink, business, Outcome{
			OutcomeID: "out-" + tc.outcome, AccountID: "acct-t217", Outcome: tc.outcome,
			ObservedAt: observedAt, StatusCode: 500, ErrorCode: "upstream_error", ErrorMessage: "boom",
		}); err != nil {
			t.Fatalf("质量终态 %s 写入失败: %v", tc.outcome, err)
		}
		if len(sink.observations) != 1 {
			t.Fatalf("质量终态 %s 应写一条: %#v", tc.outcome, sink.observations)
		}
		got := sink.observations[0]
		if got.Success != tc.success || got.AccountID != "acct-t217" || got.ProviderCode != "gpt" ||
			got.SystemAccountID != "sys-t217" || got.OutcomeID != "out-"+tc.outcome || got.ObservedAt != observedAt {
			t.Fatalf("质量终态 %s 条目字段错误: %#v", tc.outcome, got)
		}
	}
}
