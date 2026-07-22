package managementstats

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestAccountUsageNormalizesAdminGlobalAndSelfScopes(t *testing.T) {
	now := time.Date(2026, 7, 22, 3, 0, 0, 0, time.UTC)
	reader := &managementStatsReadStub{
		accountUsage: port.ManagementAccountUsageReadResult{
			Rows: []port.ManagementAccountUsageRow{{
				Account: port.ManagementStatsAccount{ID: "acc_1", Name: "A", ProviderCode: "openai", Type: "api_key", Status: "active", SystemAccountID: "sys_owner", OwnerSystemAccountID: "sys_owner", AccessType: "owner"},
				Usage:   port.ManagementUsageAggregate{RequestCount: 2, InputTokens: 3, OutputTokens: 4},
			}},
			PageRowCount: 1,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:       &usageStatsTimezoneStoreStub{timezone: "Asia/Shanghai", found: true},
		StatsReader: reader,
		Now:         func() time.Time { return now },
	})

	admin, err := service.AccountUsage(context.Background(), ReadScope{ActorSystemAccountID: "sys_admin", Admin: true}, AccountUsageInput{})
	if err != nil {
		t.Fatalf("admin AccountUsage() error = %v", err)
	}
	if got := reader.accountUsageInputs[0].Scope; got.SystemAccountID != "global" || got.ScopeType != "account" || !got.IncludeSystemAccountFields {
		t.Fatalf("admin global scope = %+v", got)
	}
	if admin.Range.StartDate != "2026-06-22" || admin.Range.EndDate != "2026-07-22" || admin.Page != 1 || admin.PageSize != 10 {
		t.Fatalf("admin overview = %+v", admin)
	}
	if admin.Rows[0].SystemAccountID == nil || *admin.Rows[0].SystemAccountID != "sys_owner" {
		t.Fatalf("admin row fields = %+v", admin.Rows[0])
	}

	self, err := service.AccountUsage(context.Background(), ReadScope{ActorSystemAccountID: "sys_self"}, AccountUsageInput{Page: -8, PageSize: 500})
	if err != nil {
		t.Fatalf("self AccountUsage() error = %v", err)
	}
	if got := reader.accountUsageInputs[1].Scope; got.SystemAccountID != "sys_self" || got.ScopeType != "caller_account" || got.IncludeSystemAccountFields {
		t.Fatalf("self scope = %+v", got)
	}
	if self.Page != 1 || self.PageSize != 200 || self.Rows[0].SystemAccountID != nil {
		t.Fatalf("self overview = %+v", self)
	}
}

func TestAccountUsageTrendCapsAccountsAndMapsDailyUsage(t *testing.T) {
	reader := &managementStatsReadStub{
		accountTrend: port.ManagementAccountUsageTrendReadResult{
			Accounts:  []port.ManagementStatsAccount{{ID: "acc_1", Name: "A", ProviderCode: "gpt", SystemAccountID: "sys_self", OwnerSystemAccountID: "sys_owner", AccessType: "authorized"}},
			DailyRows: []port.ManagementAccountUsageDailyRow{{AccountID: "acc_1", StatDate: "2026-07-22", Usage: port.ManagementUsageAggregate{RequestCount: 1}}},
		},
	}
	service := readServiceForTest(reader)
	ids := []string{"acc_1", "acc_1", "acc_2", "acc_3", "acc_4", "acc_5", "acc_6", "acc_7", "acc_8", "acc_9", "acc_10", "acc_11"}

	got, err := service.AccountUsageTrend(context.Background(), ReadScope{ActorSystemAccountID: "sys_self"}, AccountUsageTrendInput{StartDate: "2026-07-22", EndDate: "2026-07-22", AccountIDs: ids})
	if err != nil {
		t.Fatalf("AccountUsageTrend() error = %v", err)
	}
	if len(reader.accountTrendInputs) != 1 || len(reader.accountTrendInputs[0].AccountIDs) != 10 {
		t.Fatalf("trend input = %+v", reader.accountTrendInputs)
	}
	if len(got.Rows) != 1 || len(got.Rows[0].DailyUsage) != 1 || got.Rows[0].DailyUsage[0].RequestCount != 1 {
		t.Fatalf("trend = %+v", got)
	}
}

func TestAccountUsageTrendFillsMissingDaysWithZeroUsage(t *testing.T) {
	reader := &managementStatsReadStub{
		accountTrend: port.ManagementAccountUsageTrendReadResult{
			Accounts:  []port.ManagementStatsAccount{{ID: "acc_1", Name: "A", ProviderCode: "gpt", SystemAccountID: "sys_self", OwnerSystemAccountID: "sys_self", AccessType: "owner"}},
			DailyRows: []port.ManagementAccountUsageDailyRow{{AccountID: "acc_1", StatDate: "2026-07-21", Usage: port.ManagementUsageAggregate{RequestCount: 2}}},
		},
	}
	service := readServiceForTest(reader)

	got, err := service.AccountUsageTrend(context.Background(), ReadScope{ActorSystemAccountID: "sys_self"}, AccountUsageTrendInput{StartDate: "2026-07-20", EndDate: "2026-07-22", AccountIDs: []string{"acc_1"}})
	if err != nil {
		t.Fatalf("AccountUsageTrend() error = %v", err)
	}
	points := got.Rows[0].DailyUsage
	if len(points) != 3 || points[0].StatDate != "2026-07-20" || points[0].RequestCount != 0 || points[1].StatDate != "2026-07-21" || points[1].RequestCount != 2 || points[2].StatDate != "2026-07-22" || points[2].RequestCount != 0 {
		t.Fatalf("daily points = %+v", points)
	}
}

func TestAccountUsageSingleDateBoundaryMatchesNodeNormalization(t *testing.T) {
	reader := &managementStatsReadStub{}
	service := readServiceForTest(reader)

	got, err := service.AccountUsage(context.Background(), ReadScope{ActorSystemAccountID: "sys_self"}, AccountUsageInput{EndDate: "2026-07-10"})
	if err != nil {
		t.Fatalf("AccountUsage() error = %v", err)
	}
	if got.Range.StartDate != "2026-07-10" || got.Range.EndDate != "2026-07-10" {
		t.Fatalf("single-boundary range = %+v", got.Range)
	}
}

func TestAIPerformanceCapsSelectionMergesAccountsAndFillsHourlyBuckets(t *testing.T) {
	reader := &managementStatsReadStub{
		aiPerformance: port.ManagementAIPerformanceReadResult{
			DefaultAccounts:  []port.ManagementStatsAccount{{ID: "acc_default", Name: "Default", ProviderCode: "openai", Status: "active", SystemAccountID: "sys_owner", OwnerSystemAccountID: "sys_owner", AccessType: "owner", RequestCountLast7d: 9}},
			SelectedAccounts: []port.ManagementStatsAccount{{ID: "acc_selected", Name: "Selected", ProviderCode: "gpt", Status: "active", SystemAccountID: "sys_self", OwnerSystemAccountID: "sys_owner", AccessType: "authorized", RequestCountLast7d: 3}},
			HourlyRows:       []port.ManagementAIPerformanceHourlyRow{{AccountID: "acc_default", StatHour: "2026-07-22T01", RequestCount: 2, FirstTokenMSSum: 5, FirstTokenMSCount: 2, FirstTokenMSMax: 4, DurationMSSum: 11, DurationMSCount: 2, DurationMSMax: 8}},
			Summary:          port.ManagementAIPerformanceAggregate{RequestCount: 2, FirstTokenMSSum: 5, FirstTokenMSCount: 2, FirstTokenMSMax: 4},
		},
	}
	service := readServiceForTest(reader)
	ids := make([]string, 25)
	for index := range ids {
		ids[index] = "acc_" + string(rune('a'+index))
	}

	got, err := service.AIPerformance(context.Background(), ReadScope{ActorSystemAccountID: "sys_self"}, AIPerformanceInput{StartDate: "2026-07-22", EndDate: "2026-07-22", AccountIDs: ids})
	if err != nil {
		t.Fatalf("AIPerformance() error = %v", err)
	}
	if len(reader.aiPerformanceInputs[0].AccountIDs) != 20 {
		t.Fatalf("selected ids = %d", len(reader.aiPerformanceInputs[0].AccountIDs))
	}
	if len(got.Accounts) != 2 || len(got.DefaultAccounts) != 1 || len(got.SelectedAccounts) != 1 || len(got.HourlySeries) != 2 || len(got.HourlySeries[0].Points) != 24 {
		t.Fatalf("overview sizes = %+v", got)
	}
	point := got.HourlySeries[0].Points[1]
	if point.RequestCount != 2 || point.AverageFirstTokenMS == nil || *point.AverageFirstTokenMS != 3 || point.MaxDurationMS == nil || *point.MaxDurationMS != 8 {
		t.Fatalf("hour point = %+v", point)
	}
	if got.Summary.AverageFirstTokenMS == nil || *got.Summary.AverageFirstTokenMS != 3 {
		t.Fatalf("summary = %+v", got.Summary)
	}
}

func TestRoundedAccountPerformanceAverageMatchesNodeMathRound(t *testing.T) {
	if got := roundedAccountPerformanceAverage(5, 2); got == nil || *got != 3 {
		t.Fatalf("roundedAccountPerformanceAverage(5, 2) = %v", got)
	}
	if got := roundedAccountPerformanceAverage(-3, 2); got == nil || *got != -1 {
		t.Fatalf("roundedAccountPerformanceAverage(-3, 2) = %v", got)
	}
	if got := roundedAccountPerformanceAverage(5, 0); got != nil {
		t.Fatalf("roundedAccountPerformanceAverage(5, 0) = %v, want nil", *got)
	}
}

func TestAIPerformanceAccountsCapsLimitAndKeepsSelectedIDs(t *testing.T) {
	reader := &managementStatsReadStub{aiAccounts: []port.ManagementStatsAccount{{ID: "acc_1", Name: "A", ProviderCode: "openai", Status: "active", SystemAccountID: "sys_self", OwnerSystemAccountID: "sys_self", AccessType: "owner"}}}
	service := readServiceForTest(reader)

	got, err := service.AIPerformanceAccounts(context.Background(), ReadScope{ActorSystemAccountID: "sys_self"}, AIPerformanceAccountsInput{Keyword: "  A  ", Limit: 500, AccountIDs: []string{"acc_1", "acc_1", "acc_2"}})
	if err != nil {
		t.Fatalf("AIPerformanceAccounts() error = %v", err)
	}
	input := reader.aiAccountInputs[0]
	if input.Limit != 50 || input.Keyword != "A" || len(input.AccountIDs) != 2 || len(got) != 1 {
		t.Fatalf("options input/result = %+v / %+v", input, got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal options: %v", err)
	}
	if strings.Contains(string(encoded), "selected") || strings.Contains(string(encoded), "defaultVisible") {
		t.Fatalf("options leaked overview-only flags: %s", encoded)
	}
}

func readServiceForTest(reader port.ManagementStatsReader) *Service {
	return NewServiceWithOptions(ServiceOptions{
		Store:       &usageStatsTimezoneStoreStub{timezone: "UTC", found: true},
		StatsReader: reader,
		Now:         func() time.Time { return time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) },
	})
}

type managementStatsReadStub struct {
	accountUsage        port.ManagementAccountUsageReadResult
	accountTrend        port.ManagementAccountUsageTrendReadResult
	aiPerformance       port.ManagementAIPerformanceReadResult
	aiAccounts          []port.ManagementStatsAccount
	accountUsageInputs  []port.ManagementAccountUsageReadInput
	accountTrendInputs  []port.ManagementAccountUsageTrendReadInput
	aiPerformanceInputs []port.ManagementAIPerformanceReadInput
	aiAccountInputs     []port.ManagementAIPerformanceAccountsReadInput
}

func (s *managementStatsReadStub) ReadManagementAccountUsage(_ context.Context, input port.ManagementAccountUsageReadInput) (port.ManagementAccountUsageReadResult, error) {
	s.accountUsageInputs = append(s.accountUsageInputs, input)
	return s.accountUsage, nil
}

func (s *managementStatsReadStub) ReadManagementAccountUsageTrend(_ context.Context, input port.ManagementAccountUsageTrendReadInput) (port.ManagementAccountUsageTrendReadResult, error) {
	s.accountTrendInputs = append(s.accountTrendInputs, input)
	return s.accountTrend, nil
}

func (s *managementStatsReadStub) ReadManagementAIPerformance(_ context.Context, input port.ManagementAIPerformanceReadInput) (port.ManagementAIPerformanceReadResult, error) {
	s.aiPerformanceInputs = append(s.aiPerformanceInputs, input)
	return s.aiPerformance, nil
}

func (s *managementStatsReadStub) ReadManagementAIPerformanceAccounts(_ context.Context, input port.ManagementAIPerformanceAccountsReadInput) ([]port.ManagementStatsAccount, error) {
	s.aiAccountInputs = append(s.aiAccountInputs, input)
	return s.aiAccounts, nil
}

var _ port.ManagementStatsReader = (*managementStatsReadStub)(nil)
