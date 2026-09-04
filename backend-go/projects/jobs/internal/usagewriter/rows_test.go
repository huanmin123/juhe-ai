package usagewriter

import (
	"context"
	"strings"
	"testing"
)

func planOptions() WritePlanOptions {
	return WritePlanOptions{
		CatalogSnapshotEnabled: true,
		ShardCount:             16,
		ShardRoot:              "/shards",
	}
}

func TestBuildWritePlanColumnsAndDefaults(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	input := UsageRecordInput{
		SystemAccountID: "sys1",
		TraceID:         "t1",
		TrafficSource:   TrafficSourceGateway,
		Success:         true,
		Model:           "gpt-5",
	}
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{input}, planOptions(), clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.RowsByShard) != 1 || len(plan.RowsByShard[0].Rows) != 1 {
		t.Fatalf("plan shape = %+v", plan)
	}
	if len(UsageRecordColumns) != len(plan.RowsByShard[0].Rows[0].Params) {
		t.Fatalf("column/param count mismatch: %d vs %d", len(UsageRecordColumns), len(plan.RowsByShard[0].Rows[0].Params))
	}
	params := plan.RowsByShard[0].Rows[0].Params
	columnIndex := map[string]int{}
	for index, column := range UsageRecordColumns {
		columnIndex[column] = index
	}
	// Service tier defaults (requested/effective/billed fall back to 'default';
	// reported stays null).
	if params[columnIndex["requested_service_tier"]] != "default" {
		t.Fatalf("requested tier = %v", params[columnIndex["requested_service_tier"]])
	}
	if params[columnIndex["billed_service_tier"]] != "default" {
		t.Fatalf("billed tier = %v", params[columnIndex["billed_service_tier"]])
	}
	if params[columnIndex["reported_service_tier"]] != nil {
		t.Fatalf("reported tier = %v", params[columnIndex["reported_service_tier"]])
	}
	if params[columnIndex["success"]] != 1 {
		t.Fatalf("success = %v", params[columnIndex["success"]])
	}
	if params[columnIndex["failure_attribution"]] != nil {
		t.Fatalf("success attribution = %v", params[columnIndex["failure_attribution"]])
	}
	if params[columnIndex["created_at"]] != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("created_at = %v", params[columnIndex["created_at"]])
	}
	// Fallback pricing snapshot written as JSON for a record with cost facts?
	// No cost facts here → null snapshot.
	if params[columnIndex["cost_breakdown_snapshot_json"]] != nil {
		t.Fatalf("snapshot without facts = %v", params[columnIndex["cost_breakdown_snapshot_json"]])
	}
	// Gateway traffic produces the account side effect.
	if plan.RowsByShard[0].Rows[0].AccountLastUsedAt != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("gateway lastUsed missing: %+v", plan.RowsByShard[0].Rows[0])
	}
	// Catalog entry mirrors the row.
	if len(plan.ShardEntries) != 1 || plan.ShardEntries[0].TraceID != "t1" || plan.ShardEntries[0].TrafficSource != TrafficSourceGateway {
		t.Fatalf("shard entries = %+v", plan.ShardEntries)
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
			if plan.ShardEntries[0].FailureAttribution == nil || *plan.ShardEntries[0].FailureAttribution != tt.want {
				t.Fatalf("attribution = %+v, want %q", plan.ShardEntries[0].FailureAttribution, tt.want)
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

func TestBuildWritePlanAPIKeyFilter(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	options := planOptions()
	options.Scope = &ScopeLookup{
		APIKeyExists: func(apiKeyID string) bool { return apiKeyID == "key-live" },
		SystemAccountIDForAPIKey: func(apiKeyID string) string {
			if apiKeyID == "key-live" {
				return "sys-from-key"
			}
			return ""
		},
	}
	kept := UsageRecordInput{TraceID: "t1", TrafficSource: TrafficSourceGateway, Success: true, APIKeyID: "key-live"}
	dropped := UsageRecordInput{TraceID: "t2", TrafficSource: TrafficSourceGateway, Success: true, APIKeyID: "key-gone"}
	plan, err := BuildWritePlan(context.Background(), []UsageRecordInput{kept, dropped}, options, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ShardEntries) != 1 || plan.ShardEntries[0].TraceID != "t1" {
		t.Fatalf("api key filter result = %+v", plan.ShardEntries)
	}
	if plan.ShardEntries[0].SystemAccountID != "sys-from-key" {
		t.Fatalf("system account resolution = %q", plan.ShardEntries[0].SystemAccountID)
	}
}

func TestBuildWritePlanPostgresRequiresSystemAccount(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	options := planOptions()
	options.Postgres = true
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
	if !strings.HasPrefix(plan.RowsByShard[0].Location.FilePath, "postgres:juhe_usage.usage_records:") {
		t.Fatalf("postgres location = %q", plan.RowsByShard[0].Location.FilePath)
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
	options.CatalogSnapshotEnabled = false
	options.Catalog = nil
	plan, err := BuildWritePlan(context.Background(), inputs, options, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.RowsByShard) != 1 {
		t.Fatalf("same-bucket ids should share one shard, got %d (%v)", len(plan.RowsByShard), plan.Locations)
	}
	if len(plan.ShardEntries) != 5 {
		t.Fatalf("entries = %d", len(plan.ShardEntries))
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
