package statsagg

// w13g5_statsagg_arms_test.go 用注入 kit（w13g5_statsagg_kit_test.go）覆盖
// 聚合主链路的深层 err 传播臂：游标状态读写、批次查询/扫描、逐表 UPSERT、
// 授权日报写入与派生窗口脏标记。每个用例先建好 schema（注入未武装），再
// armOnce 精确命中单条语句，保证可回放、互不干扰。

import (
	"context"
	"strings"
	"testing"
)

// w13g5QualitySeedRow 构造一条能进入账户质量聚合的最小 usage 行。
func w13g5QualitySeedRow(id string) UsageStatsRecordRow {
	return UsageStatsRecordRow{
		ID: id, SystemAccountID: "w13g5-alice", TraceID: "w13g5-tr",
		TrafficSource: "gateway",
		APIKeyID:      strPtr("w13g5-key"), Endpoint: strPtr("/v1/chat/completions"),
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-5"),
		Success:    1,
		DurationMs: f64Ptr(100), FirstTokenMs: f64Ptr(20),
		InputTokens: f64Ptr(10), OutputTokens: f64Ptr(5), CostUsd: f64Ptr(0.01),
		CreatedAt:                    "2026-09-18T07:15:00.000Z",
		AccountID:                    strPtr("w13g5-acc"),
		AccountOwnerSystemAccountID:  strPtr("w13g5-alice"),
		AccountAccessType:            strPtr("owner"),
		GroupID:                      strPtr("w13g5-grp"),
		GroupOwnerSystemAccountID:    strPtr("w13g5-alice"),
		GroupAccessType:              strPtr("owner"),
	}
}

func TestW13g5StatsAggregateClockErrorArm(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	aggregator := env.aggregator()
	aggregator.Clock = w13g5StatsErrClock{}
	if _, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("时钟失败必须传播")
	}
}

func TestW13g5StatsAggregateBeginTxErrorArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	spec.arm("w13g5-BEGIN")
	defer spec.disarm()
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("BeginTx 失败必须传播")
	}
}

func TestW13g5StatsAggregateJobStateReadErrorArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	spec.armOnce("cursor_created_at, cursor_id, lag_seconds")
	defer spec.disarm()
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("游标状态读取失败必须传播")
	}
}

func TestW13g5StatsAggregateBatchQueryErrorArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	spec.armOnce("ORDER BY created_at ASC, id ASC")
	defer spec.disarm()
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("批次查询失败必须传播")
	}
}

func TestW13g5StatsAggregateScanErrorArms(t *testing.T) {
	env, _ := w13g5StatsOpenFailEnv(t)
	// status_code 列写入非数值文本，使 float64 扫描失败。
	env.exec(`INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, status_code, success, created_at) VALUES ('w13g5-bad-scan', 'w13g5-alice', 'w13g5-tr', 'gateway', 'w13g5-not-number', 1, '2026-09-18T07:15:00.000Z')`)
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("扫描失败必须传播")
	}
}

func TestW13g5StatsAggregateEmptyBatchStateWriteErrorArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	spec.armOnce("INSERT INTO stats_job_state")
	defer spec.disarm()
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("空批次游标写入失败必须传播")
	}
}

func TestW13g5StatsAggregateNonEmptyBatchStateWriteErrorArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	env.seedUsageRecord(w13g5QualitySeedRow("w13g5-rec-state"))
	spec.armOnce("INSERT INTO stats_job_state")
	defer spec.disarm()
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("非空批次游标写入失败必须传播")
	}
}

func TestW13g5StatsAggregateCommitErrorArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	env.seedUsageRecord(w13g5QualitySeedRow("w13g5-rec-commit"))
	spec.armOnce("w13g5-COMMIT")
	defer spec.disarm()
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("事务提交失败必须传播")
	}
}

// TestW13g5StatsAggregateUpsertErrorArms 逐表注入 UPSERT 失败，覆盖
// aggregateRows 的每个错误传播点。
func TestW13g5StatsAggregateUpsertErrorArms(t *testing.T) {
	cases := []struct {
		name  string
		match string
	}{
		{"totals", "INSERT INTO usage_stats_totals"},
		{"time buckets", "INSERT INTO usage_stats_minute"},
		{"latency", "INSERT INTO usage_latency"},
		{"model", "INSERT INTO usage_model_"},
		{"error", "INSERT INTO usage_error_"},
		{"quality", "INSERT INTO account_quality_minute_stats"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, spec := w13g5StatsOpenFailEnv(t)
			row := w13g5QualitySeedRow("w13g5-rec-" + strings.ReplaceAll(tc.name, " ", "-"))
			if tc.name == "error" {
				// 错误表只聚合失败行。
				row.Success = 0
				row.StatusCode = f64Ptr(429)
				row.ErrorCode = strPtr("w13g5-rate-limited")
			}
			env.seedUsageRecord(row)
			spec.armOnce(tc.match)
			defer spec.disarm()
			if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
				t.Fatalf("%s UPSERT 失败必须传播", tc.name)
			}
		})
	}
}

func TestW13g5StatsAggregateHealthUpsertErrorArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	row := w13g5QualitySeedRow("w13g5-rec-health")
	// 账户健康小时表只聚合探针流量。
	row.TrafficSource = "account_health_check"
	env.seedUsageRecord(row)
	spec.armOnce("INSERT INTO account_health_hourly")
	defer spec.disarm()
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("健康小时表 UPSERT 失败必须传播")
	}
}

func TestW13g5StatsAggregateAuthorizationUpsertErrorArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	row := w13g5QualitySeedRow("w13g5-rec-auth")
	row.SystemAccountID = "w13g5-grantee"
	row.AccountAccessType = strPtr("account_authorized")
	row.AccountAuthorizationID = strPtr("w13g5-authz")
	row.AccountAuthorizationSourceType = strPtr("team")
	row.AccountAuthorizationSourceTeamID = strPtr("w13g5-team")
	env.seedUsageRecord(row)
	spec.armOnce("INSERT INTO authorization_team_usage_summary_daily")
	defer spec.disarm()
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("授权 team 日报 UPSERT 失败必须传播")
	}
}

func TestW13g5StatsAggregateAuthorizationUserUpsertErrorArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	row := w13g5QualitySeedRow("w13g5-rec-auth-user")
	row.SystemAccountID = "w13g5-grantee"
	row.AccountAccessType = strPtr("account_authorized")
	row.AccountAuthorizationID = strPtr("w13g5-authz")
	row.AccountAuthorizationSourceType = strPtr("manual")
	env.seedUsageRecord(row)
	spec.armOnce("INSERT INTO authorization_user_usage_summary_daily")
	defer spec.disarm()
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("授权 user 日报 UPSERT 失败必须传播")
	}
}

func TestW13g5StatsAggregateDirtyScopesErrorArm(t *testing.T) {
	env, spec := w13g5StatsOpenFailEnv(t)
	env.seedUsageRecord(w13g5QualitySeedRow("w13g5-rec-dirty"))
	// 派生窗口脏标记语句位于 aggregateRows 末尾，最后注入才命中。
	spec.armOnce("INSERT INTO usage_overview_dirty_scopes")
	defer spec.disarm()
	if _, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{}); err == nil {
		t.Fatal("派生窗口脏标记失败必须传播")
	}
}
