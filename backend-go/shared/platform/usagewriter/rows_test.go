package usagewriter

import (
	"context"
	"strings"
	"testing"
)

func writeRowParam(row ShardWriteRow, column string) any {
	for index, name := range UsageRecordColumns {
		if name == column {
			return row.Params[index]
		}
	}
	return nil
}

func planOptions() WritePlanOptions {
	return WritePlanOptions{
		ShardCount: 16,
	}
}

func TestBuildWritePlanCompactionExpectedParam(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	expected := UsageRecordInput{
		SystemAccountID: "sys1", TraceID: "t1", TrafficSource: TrafficSourceGateway,
		Success: true, CompactionExpected: true,
	}
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{expected}, planOptions(), clock)
	if err != nil {
		t.Fatal(err)
	}
	params := plan.RowsByShard[0].Rows[0].Params
	if len(params) != len(UsageRecordColumns) {
		t.Fatalf("column/param count mismatch: %d vs %d", len(UsageRecordColumns), len(params))
	}
	if columnIndexOf("compaction_expected") != columnIndexOf("route_group_id")+1 {
		t.Fatalf("compaction_expected must follow route_group_id (got %d, route_group_id at %d)",
			columnIndexOf("compaction_expected"), columnIndexOf("route_group_id"))
	}
	if got := params[columnIndexOf("compaction_expected")]; got != 1 {
		t.Fatalf("compaction_expected = %v, want 1", got)
	}

	plain := UsageRecordInput{SystemAccountID: "sys1", TraceID: "t2", TrafficSource: TrafficSourceGateway, Success: true}
	plan, err = BuildWritePlan(context.Background(), []UsageRecordInput{plain}, planOptions(), clock)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.RowsByShard[0].Rows[0].Params[columnIndexOf("compaction_expected")]; got != 0 {
		t.Fatalf("default compaction_expected = %v, want 0", got)
	}
}

// 安全策略两列落库 golden（API Key 安全策略与出站内容脱敏设计 §8，批D）：
// 列序紧邻 compaction_expected（与 pg_schema_usage.go DDL、mockdata
// usageRecordColumns 一致）；applied 走 boolToInt，pii_hits_json nil/空 map
// 落 NULL、非空落紧凑 JSON。
func TestBuildWritePlanSecurityPolicyParams(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	if columnIndexOf("security_policy_applied") != columnIndexOf("compaction_expected")+1 ||
		columnIndexOf("pii_hits_json") != columnIndexOf("security_policy_applied")+1 {
		t.Fatalf("security policy columns must follow compaction_expected (security_policy_applied at %d, pii_hits_json at %d, compaction_expected at %d)",
			columnIndexOf("security_policy_applied"), columnIndexOf("pii_hits_json"), columnIndexOf("compaction_expected"))
	}
	applied := UsageRecordInput{
		SystemAccountID: "sys1", TraceID: "t1", TrafficSource: TrafficSourceGateway,
		Success: true, SecurityPolicyApplied: true, PIIHits: map[string]int{"PHONE": 2},
	}
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{applied}, planOptions(), clock)
	if err != nil {
		t.Fatal(err)
	}
	params := plan.RowsByShard[0].Rows[0].Params
	if len(params) != len(UsageRecordColumns) {
		t.Fatalf("column/param count mismatch: %d vs %d", len(UsageRecordColumns), len(params))
	}
	if got := params[columnIndexOf("security_policy_applied")]; got != 1 {
		t.Fatalf("security_policy_applied = %v, want 1", got)
	}
	if got := params[columnIndexOf("pii_hits_json")]; got != `{"PHONE":2}` {
		t.Fatalf(`pii_hits_json = %v, want {"PHONE":2}`, got)
	}

	// 无值记录两字段零值：applied 0、hits NULL；空 map 同样落 NULL。
	plain := UsageRecordInput{SystemAccountID: "sys1", TraceID: "t2", TrafficSource: TrafficSourceGateway, Success: true, PIIHits: map[string]int{}}
	plan, err = BuildWritePlan(context.Background(), []UsageRecordInput{plain}, planOptions(), clock)
	if err != nil {
		t.Fatal(err)
	}
	params = plan.RowsByShard[0].Rows[0].Params
	if got := params[columnIndexOf("security_policy_applied")]; got != 0 {
		t.Fatalf("default security_policy_applied = %v, want 0", got)
	}
	if got := params[columnIndexOf("pii_hits_json")]; got != nil {
		t.Fatalf("empty pii_hits_json = %v, want NULL", got)
	}

	// NormalizeUsageRecordInput 归一化：nil/空 map 一律归一为 nil。
	normalized, err := NormalizeUsageRecordInput(UsageRecordInput{PIIHits: map[string]int{}}, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.PIIHits != nil {
		t.Fatalf("normalized empty PIIHits = %#v, want nil", normalized.PIIHits)
	}
}

func TestBuildWritePlanFailureAttributionDefaults(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	tests := []struct {
		name  string
		input UsageRecordInput
		want  string
	}{
		{
			name:  "no account defaults to gateway_policy",
			input: UsageRecordInput{TraceID: "t", TrafficSource: TrafficSourceGateway, SystemAccountID: "sys"},
			want:  FailureAttributionGatewayPolicy,
		},
		{
			name: "account defaults to account_upstream",
			input: UsageRecordInput{TraceID: "t", TrafficSource: TrafficSourceGateway, SystemAccountID: "sys",
				AccountID: "acc1", AccountOwnerSystemAccountID: "sys", AccountAccessType: AccountAccessTypeOwner},
			want: FailureAttributionAccountUpstream,
		},
		{
			name: "explicit attribution kept",
			input: UsageRecordInput{TraceID: "t", TrafficSource: TrafficSourceGateway, SystemAccountID: "sys",
				FailureAttribution: FailureAttributionDownstreamClosed},
			want: FailureAttributionDownstreamClosed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.Success = false
			plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{tt.input}, planOptions(), clock)
			if err != nil {
				t.Fatal(err)
			}
			if got := writeRowParam(plan.RowsByShard[0].Rows[0], "failure_attribution"); got == nil || got != tt.want {
				t.Fatalf("attribution = %+v, want %q", got, tt.want)
			}
		})
	}

	invalid := UsageRecordInput{TraceID: "t", TrafficSource: TrafficSourceGateway, SystemAccountID: "sys", Success: false, FailureAttribution: "nope"}
	if _, err := BuildWritePlan(context.Background(), []UsageRecordInput{invalid}, planOptions(), clock); err == nil || err.Error() != "使用记录失败归因无效" {
		t.Fatalf("invalid attribution error = %v", err)
	}
}

func TestBuildWritePlanInvalidTrafficSource(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	input := UsageRecordInput{TraceID: "t", TrafficSource: "teleport", SystemAccountID: "sys", Success: true}
	_, err := BuildWritePlan(context.Background(), []UsageRecordInput{input}, planOptions(), clock)
	if err == nil || err.Error() != "使用记录来源无效" {
		t.Fatalf("traffic source error = %v", err)
	}
}

func TestBuildWritePlanDiagnosticTrafficSkipsSideEffects(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	input := UsageRecordInput{
		SystemAccountID: "sys", TraceID: "t", TrafficSource: TrafficSourceManualAccountTest,
		Success: true, AccountID: "acc", AccountOwnerSystemAccountID: "sys", AccountAccessType: AccountAccessTypeOwner,
	}
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{input}, planOptions(), clock)
	if err != nil {
		t.Fatal(err)
	}
	row := plan.RowsByShard[0].Rows[0]
	if row.AccountLastUsedAt != "" || row.AccountHealthSuccessAt != "" {
		t.Fatalf("diagnostic traffic produced side effects: %+v", row)
	}
}

func columnIndexOf(name string) int {
	for index, column := range UsageRecordColumns {
		if column == name {
			return index
		}
	}
	return -1
}

func TestMergeShardWriteResult(t *testing.T) {
	lastUsed := map[string]string{}
	health := map[string]string{}
	rows := []ShardWriteRow{
		{AccountID: "a", AccountLastUsedAt: "2026-01-02T00:00:00.000Z", AccountHealthSuccessAt: "2026-01-02T00:00:00.000Z"},
		{AccountID: "a", AccountLastUsedAt: "2026-01-03T00:00:00.000Z"},
		{AccountID: "", AccountLastUsedAt: "2026-01-09T00:00:00.000Z"},
	}
	MergeShardWriteResult(lastUsed, health, rows)
	if lastUsed["a"] != "2026-01-03T00:00:00.000Z" {
		t.Fatalf("lastUsed merge = %v", lastUsed)
	}
	if health["a"] != "2026-01-02T00:00:00.000Z" {
		t.Fatalf("health merge = %v", health)
	}
}

func TestBuildWritePlanPostgresRequiresSystemAccount(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	options := planOptions()
	input := UsageRecordInput{TraceID: "t", TrafficSource: TrafficSourceGateway, Success: true}
	if _, err := BuildWritePlan(context.Background(), []UsageRecordInput{input}, options, clock); err == nil ||
		err.Error() != "PostgreSQL 使用记录写入必须提供 systemAccountId" {
		t.Fatalf("postgres system account error = %v", err)
	}
	input.SystemAccountID = "sys1"
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{input}, options, clock)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plan.RowsByShard[0].ShardKey, "20260102:s") {
		t.Fatalf("postgres shard key = %q", plan.RowsByShard[0].ShardKey)
	}
}

func TestBuildWritePlanShardGroupsAndFallbackSnapshot(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	// Shared explicit id prefix → same shard routing; per-record random ids
	// (Node randomUUID entropy) would spread across shards.
	inputs := make([]UsageRecordInput, 0, 5)
	for i := 0; i < 5; i++ {
		inputs = append(inputs, UsageRecordInput{
			SystemAccountID: "sys", TraceID: "t", TrafficSource: TrafficSourceGateway, Success: true,
			ID:      "usage_20260102_s05_" + itoa(1767225600000+i) + "_e" + itoa(i),
			CostUsd: floatPtr(0.1), // cost facts → deterministic fallback snapshot
		})
	}
	options := planOptions()
	options.Catalog = nil
	plan, err := BuildWritePlan(context.Background(), inputs, options, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.RowsByShard) != 1 {
		t.Fatalf("same-bucket ids should share one shard, got %d", len(plan.RowsByShard))
	}
	if got := writePlanRowCount(plan); got != 5 {
		t.Fatalf("rows = %d", got)
	}
	// Random-id records spread across the shard grid (Node parity).
	for i := 0; i < 12; i++ {
		inputs = append(inputs, UsageRecordInput{
			SystemAccountID: "sys", TraceID: "t", TrafficSource: TrafficSourceGateway, Success: true,
			CostUsd: floatPtr(0.1),
		})
	}
	plan, err = BuildWritePlan(context.Background(), inputs, options, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.RowsByShard) < 2 {
		t.Fatalf("random ids should spread across shards, got %d", len(plan.RowsByShard))
	}
	snapshotJSON := plan.RowsByShard[0].Rows[0].Params[columnIndexOf("cost_breakdown_snapshot_json")]
	if snapshotJSON == nil {
		t.Fatal("missing fallback snapshot json")
	}
	text := snapshotJSON.(string)
	if !strings.Contains(text, `"multiplier":1`) || !strings.Contains(text, `"serviceTierPricingSource":"unknown"`) {
		t.Fatalf("fallback snapshot json = %s", text)
	}
}
