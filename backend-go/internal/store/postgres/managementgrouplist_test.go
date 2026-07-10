package postgres

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestListManagementGroupsUsesPageSizePlusOneAndMapsRows(t *testing.T) {
	effectiveUpdatedAt := time.Date(2026, 7, 11, 8, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	authorizationExpiresAt := effectiveUpdatedAt.Add(24 * time.Hour)
	q := &managementGroupListQueriesStub{
		groupRows: []postgresqueries.ListManagementGroupsRow{
			{
				ID:                      "group_authorized",
				SystemAccountID:         "sys_owner",
				SystemAccountName:       "Owner",
				Name:                    "Authorized Group",
				ProviderCode:            "openai",
				Description:             pgtype.Text{String: "description", Valid: true},
				Enabled:                 true,
				GroupType:               "high_concurrency",
				SchedulingPolicyJson:    pgtype.Text{String: `{"mode":"balanced_fast"}`, Valid: true},
				AccessType:              "authorized",
				GroupAuthorizationID:    pgtype.Text{String: "auth_group", Valid: true},
				AuthorizationStatus:     pgtype.Text{String: "active", Valid: true},
				AuthorizationExpiresAt:  pgtype.Timestamptz{Time: authorizationExpiresAt, Valid: true},
				AuthorizationLimitsJson: pgtype.Text{String: `{"daily":{"limit":10}}`, Valid: true},
				EffectiveUpdatedAt:      pgtype.Timestamptz{Time: effectiveUpdatedAt, Valid: true},
			},
			{
				ID:                 "group_extra",
				SystemAccountID:    "sys_owner",
				SystemAccountName:  "Owner",
				Name:               "Extra",
				ProviderCode:       "openai",
				AccessType:         "owner",
				EffectiveUpdatedAt: pgtype.Timestamptz{Time: effectiveUpdatedAt.Add(-time.Hour), Valid: true},
			},
		},
	}

	page, err := listManagementGroups(context.Background(), q, port.ManagementGroupListInput{
		SystemAccountID: "  sys_viewer  ",
		Limit:           2,
		Offset:          -10,
	})
	if err != nil {
		t.Fatalf("listManagementGroups() error = %v", err)
	}
	if len(q.groupCalls) != 1 {
		t.Fatalf("ListManagementGroups call count = %d, want 1", len(q.groupCalls))
	}
	if got := q.groupCalls[0]; got.SystemAccountID != "sys_viewer" || got.RowLimit != 2 || got.RowOffset != 0 {
		t.Fatalf("ListManagementGroups params = %#v", got)
	}
	if !page.HasMore || len(page.Rows) != 1 {
		t.Fatalf("page = %#v, want one row with hasMore", page)
	}
	row := page.Rows[0]
	if row.ID != "group_authorized" ||
		row.SystemAccountID != "sys_owner" ||
		row.SystemAccountName != "Owner" ||
		row.AccessType != "authorized" ||
		row.GroupAuthorizationID != "auth_group" ||
		row.AuthorizationStatus != "active" {
		t.Fatalf("mapped row = %#v", row)
	}
	if row.Description == nil || *row.Description != "description" {
		t.Fatalf("description = %#v", row.Description)
	}
	if row.SchedulingPolicyJSON == nil || *row.SchedulingPolicyJSON != `{"mode":"balanced_fast"}` {
		t.Fatalf("scheduling policy = %#v", row.SchedulingPolicyJSON)
	}
	if row.AuthorizationLimitsJSON == nil || *row.AuthorizationLimitsJSON != `{"daily":{"limit":10}}` {
		t.Fatalf("authorization limits = %#v", row.AuthorizationLimitsJSON)
	}
	if row.AuthorizationExpiresAt == nil || !row.AuthorizationExpiresAt.Equal(authorizationExpiresAt.UTC()) {
		t.Fatalf("authorization expiry = %#v", row.AuthorizationExpiresAt)
	}
	if !row.EffectiveUpdatedAt.Equal(effectiveUpdatedAt.UTC()) {
		t.Fatalf("effective updated at = %v, want %v", row.EffectiveUpdatedAt, effectiveUpdatedAt.UTC())
	}
}

func TestManagementGroupListBatchReadersUseBoundedArrayQueries(t *testing.T) {
	lastUsedAt := "2026-07-11T01:02:03.456Z"
	q := &managementGroupListQueriesStub{
		accountStatsRows: []postgresqueries.ListManagementGroupAccountStatsRow{
			{
				SystemAccountID:    "sys_owner",
				GroupID:            "group_1",
				Total:              9,
				Available:          7,
				Active:             6,
				Disabled:           1,
				Error:              1,
				RateLimited:        1,
				CurrentConcurrency: 3,
				ConcurrencyLimit:   10,
			},
		},
		usageTotalRows: []postgresqueries.ListManagementGroupUsageTotalsRow{
			{
				LookupKey:          "group_1",
				SystemAccountID:    "sys_owner",
				ScopeType:          "group",
				ScopeID:            "group_1",
				RequestCount:       2,
				InputTokens:        3,
				OutputTokens:       4,
				CacheReadTokens:    5,
				CacheReadCostUsd:   0.1,
				CacheWriteTokens:   6,
				CacheWrite1hTokens: 7,
				CacheWriteCostUsd:  0.2,
				ThinkingTokens:     8,
				InputImageTokens:   9,
				OutputImageTokens:  10,
				TotalCostUsd:       0.3,
				LastUsedAt:         pgtype.Text{String: lastUsedAt, Valid: true},
			},
		},
		usageDailyRows: []postgresqueries.ListManagementGroupUsageDailyRow{
			{
				LookupKey:       "auth_1",
				SystemAccountID: "sys_owner",
				ScopeType:       "group_authorization",
				ScopeID:         "auth_1",
				RequestCount:    1,
				InputTokens:     11,
				OutputTokens:    12,
			},
		},
		sourceRows: []postgresqueries.ListManagementGroupAuthorizationSourcesRow{
			{
				AuthorizationID: "auth_1",
				SourceType:      "team",
				Status:          "active",
				SourceTeamName:  "Ops",
			},
		},
	}

	stats, err := listManagementGroupAccountStats(context.Background(), q, []string{" group_1 ", "", "group_1", "group_2"})
	if err != nil {
		t.Fatalf("listManagementGroupAccountStats() error = %v", err)
	}
	if !reflect.DeepEqual(q.accountStatsCalls, [][]string{{"group_1", "group_2"}}) {
		t.Fatalf("account stats calls = %#v", q.accountStatsCalls)
	}
	if len(stats) != 1 || stats[0].Total != 9 || stats[0].CurrentConcurrency != 3 {
		t.Fatalf("account stats = %#v", stats)
	}

	inputs := []port.ManagementGroupUsageLookupInput{
		{Key: " group_1 ", SystemAccountID: " sys_owner ", ScopeType: " group ", ScopeID: " group_1 "},
		{Key: "group_1", SystemAccountID: "sys_other", ScopeType: "group", ScopeID: "group_other"},
		{Key: "", SystemAccountID: "sys_owner", ScopeType: "group", ScopeID: "ignored"},
		{Key: " auth_1 ", SystemAccountID: " sys_owner ", ScopeType: " group_authorization ", ScopeID: " auth_1 "},
	}
	totals, err := listManagementGroupUsageTotals(context.Background(), q, inputs)
	if err != nil {
		t.Fatalf("listManagementGroupUsageTotals() error = %v", err)
	}
	if len(q.usageTotalCalls) != 1 {
		t.Fatalf("usage total call count = %d, want 1", len(q.usageTotalCalls))
	}
	totalCall := q.usageTotalCalls[0]
	if !reflect.DeepEqual(totalCall.LookupKeys, []string{"group_1", "auth_1"}) ||
		!reflect.DeepEqual(totalCall.SystemAccountIds, []string{"sys_owner", "sys_owner"}) ||
		!reflect.DeepEqual(totalCall.ScopeTypes, []string{"group", "group_authorization"}) ||
		!reflect.DeepEqual(totalCall.ScopeIds, []string{"group_1", "auth_1"}) {
		t.Fatalf("usage total params = %#v", totalCall)
	}
	if len(totals) != 1 || totals[0].Usage.TotalTokens != 7 {
		t.Fatalf("usage totals = %#v", totals)
	}
	if totals[0].Usage.LastUsedAt == nil || totals[0].Usage.LastUsedAt.Format(time.RFC3339Nano) != lastUsedAt {
		t.Fatalf("last used at = %#v", totals[0].Usage.LastUsedAt)
	}

	daily, err := listManagementGroupUsageDaily(context.Background(), q, " 2026-07-11 ", inputs)
	if err != nil {
		t.Fatalf("listManagementGroupUsageDaily() error = %v", err)
	}
	if len(q.usageDailyCalls) != 1 || q.usageDailyCalls[0].StatDate != "2026-07-11" {
		t.Fatalf("usage daily calls = %#v", q.usageDailyCalls)
	}
	if len(daily) != 1 || daily[0].Usage.TotalTokens != 23 {
		t.Fatalf("daily usage = %#v", daily)
	}

	sources, err := listManagementGroupAuthorizationSources(context.Background(), q, []string{" auth_1 ", "", "auth_1", "auth_2"})
	if err != nil {
		t.Fatalf("listManagementGroupAuthorizationSources() error = %v", err)
	}
	if !reflect.DeepEqual(q.sourceCalls, [][]string{{"auth_1", "auth_2"}}) {
		t.Fatalf("source calls = %#v", q.sourceCalls)
	}
	if len(sources) != 1 || sources[0].SourceTeamName != "Ops" {
		t.Fatalf("sources = %#v", sources)
	}
}

func TestManagementGroupUsageRejectsInvalidLastUsedAt(t *testing.T) {
	q := &managementGroupListQueriesStub{
		usageTotalRows: []postgresqueries.ListManagementGroupUsageTotalsRow{
			{
				LookupKey:       "group_1",
				SystemAccountID: "sys_owner",
				ScopeType:       "group",
				ScopeID:         "group_1",
				LastUsedAt:      pgtype.Text{String: "not-a-time", Valid: true},
			},
		},
	}
	_, err := listManagementGroupUsageTotals(context.Background(), q, []port.ManagementGroupUsageLookupInput{
		{Key: "group_1", SystemAccountID: "sys_owner", ScopeType: "group", ScopeID: "group_1"},
	})
	if err == nil || !strings.Contains(err.Error(), "parse last used time") {
		t.Fatalf("listManagementGroupUsageTotals() error = %v, want parse error", err)
	}
}

func TestManagementGroupUsageBatchIsBoundedAndKeepsArraysAligned(t *testing.T) {
	inputs := make([]port.ManagementGroupUsageLookupInput, 0, maxManagementGroupListBatch+2)
	for i := 0; i < maxManagementGroupListBatch+2; i++ {
		key := "key_" + strings.Repeat("x", i%3) + time.Unix(int64(i), 0).UTC().Format("150405")
		inputs = append(inputs, port.ManagementGroupUsageLookupInput{
			Key:             key,
			SystemAccountID: "sys_owner",
			ScopeType:       "group",
			ScopeID:         key,
		})
	}
	batch := managementGroupUsageBatch(inputs)
	if len(batch.lookupKeys) != maxManagementGroupListBatch ||
		len(batch.systemAccountIDs) != maxManagementGroupListBatch ||
		len(batch.scopeTypes) != maxManagementGroupListBatch ||
		len(batch.scopeIDs) != maxManagementGroupListBatch {
		t.Fatalf(
			"batch lengths = %d/%d/%d/%d, want %d",
			len(batch.lookupKeys),
			len(batch.systemAccountIDs),
			len(batch.scopeTypes),
			len(batch.scopeIDs),
			maxManagementGroupListBatch,
		)
	}
	for i := range batch.lookupKeys {
		if batch.lookupKeys[i] != batch.scopeIDs[i] {
			t.Fatalf("batch arrays misaligned at %d: key=%q scopeID=%q", i, batch.lookupKeys[i], batch.scopeIDs[i])
		}
	}
}

type managementGroupListQueriesStub struct {
	groupRows         []postgresqueries.ListManagementGroupsRow
	groupErr          error
	groupCalls        []postgresqueries.ListManagementGroupsParams
	accountStatsRows  []postgresqueries.ListManagementGroupAccountStatsRow
	accountStatsErr   error
	accountStatsCalls [][]string
	usageTotalRows    []postgresqueries.ListManagementGroupUsageTotalsRow
	usageTotalErr     error
	usageTotalCalls   []postgresqueries.ListManagementGroupUsageTotalsParams
	usageDailyRows    []postgresqueries.ListManagementGroupUsageDailyRow
	usageDailyErr     error
	usageDailyCalls   []postgresqueries.ListManagementGroupUsageDailyParams
	sourceRows        []postgresqueries.ListManagementGroupAuthorizationSourcesRow
	sourceErr         error
	sourceCalls       [][]string
}

func (s *managementGroupListQueriesStub) ListManagementGroups(
	_ context.Context,
	arg postgresqueries.ListManagementGroupsParams,
) ([]postgresqueries.ListManagementGroupsRow, error) {
	s.groupCalls = append(s.groupCalls, arg)
	return s.groupRows, s.groupErr
}

func (s *managementGroupListQueriesStub) ListManagementGroupAccountStats(
	_ context.Context,
	groupIDs []string,
) ([]postgresqueries.ListManagementGroupAccountStatsRow, error) {
	s.accountStatsCalls = append(s.accountStatsCalls, append([]string(nil), groupIDs...))
	return s.accountStatsRows, s.accountStatsErr
}

func (s *managementGroupListQueriesStub) ListManagementGroupUsageTotals(
	_ context.Context,
	arg postgresqueries.ListManagementGroupUsageTotalsParams,
) ([]postgresqueries.ListManagementGroupUsageTotalsRow, error) {
	s.usageTotalCalls = append(s.usageTotalCalls, arg)
	return s.usageTotalRows, s.usageTotalErr
}

func (s *managementGroupListQueriesStub) ListManagementGroupUsageDaily(
	_ context.Context,
	arg postgresqueries.ListManagementGroupUsageDailyParams,
) ([]postgresqueries.ListManagementGroupUsageDailyRow, error) {
	s.usageDailyCalls = append(s.usageDailyCalls, arg)
	return s.usageDailyRows, s.usageDailyErr
}

func (s *managementGroupListQueriesStub) ListManagementGroupAuthorizationSources(
	_ context.Context,
	authorizationIDs []string,
) ([]postgresqueries.ListManagementGroupAuthorizationSourcesRow, error) {
	s.sourceCalls = append(s.sourceCalls, append([]string(nil), authorizationIDs...))
	return s.sourceRows, s.sourceErr
}

var _ managementGroupListQueries = (*managementGroupListQueriesStub)(nil)
