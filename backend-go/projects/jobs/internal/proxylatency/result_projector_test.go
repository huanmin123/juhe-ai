package proxylatency

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPostgresProjectorContractIncludesProxyDirtyTriggerDependencies(t *testing.T) {
	projector := &ResultProjector{mode: StorePostgres}
	statements := strings.Join(projector.contractStatements(), "\n")
	for _, required := range []string{
		"juhe_business.accounts",
		"juhe_business.account_list_availability_projections",
		"juhe_business.account_list_availability_dirty",
		"INSERT INTO juhe_business.account_list_availability_dirty",
		"UPDATE juhe_business.account_list_availability_dirty",
	} {
		if !strings.Contains(statements, required) {
			t.Fatalf("PostgreSQL result projector contract missing proxy dirty-trigger dependency %q", required)
		}
	}
}

func TestProjectionBaseAndSummary(t *testing.T) {
	items := []ItemResult{
		{Status: ItemPassed, LatencyMS: 10},
		{Status: ItemWarning, LatencyMS: 11},
		{Status: ItemFailed, LatencyMS: 0},
	}
	base, latency := projectionBase(items)
	if base != ItemWarning {
		t.Fatalf("projectionBase status=%q want %q", base, ItemWarning)
	}
	if latency == nil || *latency != 11 {
		t.Fatalf("projectionBase latency=%v want 11", latency)
	}
	summary := summarizeProjectionItems(items)
	if summary.Status != string(OverallFailed) {
		t.Fatalf("summary status=%q want %q", summary.Status, OverallFailed)
	}
	if summary.Message != "代理检测存在 1 项失败" {
		t.Fatalf("summary message=%q", summary.Message)
	}
}

func TestProjectionBaseIncludesZeroPassedLatencyOnly(t *testing.T) {
	base, latency := projectionBase([]ItemResult{
		{Status: ItemPassed, LatencyMS: 0},
		{Status: ItemPassed, LatencyMS: 10},
		{Status: ItemFailed, LatencyMS: 0},
		{Status: ItemUnknown, LatencyMS: 0},
	})
	if base != ItemWarning {
		t.Fatalf("projectionBase status=%q want %q", base, ItemWarning)
	}
	if latency == nil || *latency != 5 {
		t.Fatalf("projectionBase average=%v want 5 including passed 0ms", latency)
	}
	_, zero := projectionBase([]ItemResult{{Status: ItemPassed, LatencyMS: 0}})
	if zero == nil || *zero != 0 {
		t.Fatalf("projectionBase all-zero passed average=%v want pointer to 0", zero)
	}
}

func TestValidateProjectionOutcomeFailsClosed(t *testing.T) {
	valid := Outcome{
		OutcomeID:       "outcome-1",
		RequestID:       "request-1",
		ProxyID:         "proxy-1",
		InputVersion:    1,
		ConfigRevision:  "2026-08-23T00:00:00Z",
		ObservedAt:      mustProjectionTestTime("2026-08-23T00:00:01.000Z"),
		Trigger:         TriggerPeriodic,
		OwnerFenceToken: 1,
		ProxyFenceToken: 1,
		OverallStatus:   OverallPassed,
		Items:           []ItemResult{{Provider: "openai", ProfileID: "openai", Status: ItemPassed, LatencyMS: 10}},
	}
	if reason := validateProjectionOutcome(valid); reason != "" {
		t.Fatalf("valid outcome rejected: %s", reason)
	}
	invalid := valid
	invalid.OverallStatus = OverallFailed
	if reason := validateProjectionOutcome(invalid); reason != "overall_status_mismatch" {
		t.Fatalf("invalid outcome reason=%q want overall_status_mismatch", reason)
	}
}

func TestManualProjectionDisposition(t *testing.T) {
	tests := []struct {
		name         string
		result       ProjectionResult
		wantOutbound bool
		wantMissing  bool
		wantAnyError bool
	}{
		{name: "applied writes outbound", result: ProjectionResult{Disposition: ProjectionApplied}, wantOutbound: true},
		{name: "stale returns report without outbound write", result: ProjectionResult{Disposition: ProjectionStale}},
		{name: "deleted proxy is not found", result: ProjectionResult{Disposition: ProjectionIgnored, Reason: "proxy_missing_or_deleted"}, wantMissing: true},
		{name: "other ignored outcome fails closed", result: ProjectionResult{Disposition: ProjectionIgnored, Reason: "receipt_replay"}, wantAnyError: true},
		{name: "rejected outcome fails closed", result: ProjectionResult{Disposition: ProjectionRejected, Reason: "payload_mismatch"}, wantAnyError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeOutbound, err := manualProjectionDisposition(tt.result)
			if writeOutbound != tt.wantOutbound {
				t.Fatalf("writeOutbound=%v want %v", writeOutbound, tt.wantOutbound)
			}
			if tt.wantMissing && !errors.Is(err, ErrManualProxyMissing) {
				t.Fatalf("error=%v want ErrManualProxyMissing", err)
			}
			if tt.wantAnyError && err == nil {
				t.Fatal("expected fail-closed error")
			}
			if !tt.wantMissing && !tt.wantAnyError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func mustProjectionTestTime(value string) (result time.Time) {
	result, err := parseProxyLatencyUTC(value, "test")
	if err != nil {
		panic(err)
	}
	return result
}
