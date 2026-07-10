package managementgroups

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListMapsAdminOwnerPageFromPreaggregates(t *testing.T) {
	now := time.Date(2026, 7, 10, 16, 30, 0, 0, time.UTC)
	lastUsedAt := now.Add(-time.Hour)
	policyJSON := validManagementGroupListPolicyJSON()
	store := &managementGroupListStoreStub{
		page: port.ManagementGroupListPage{
			Rows: []port.ManagementGroupListRow{{
				ID:                   "grp_owner",
				SystemAccountID:      "sys_owner",
				SystemAccountName:    "所有者",
				Name:                 "默认分组",
				ProviderCode:         "openai",
				Description:          stringPointer("主分组"),
				Enabled:              true,
				IsDefault:            true,
				GroupType:            "high_concurrency",
				SchedulingPolicyJSON: &policyJSON,
				AccessType:           "owner",
			}},
			HasMore: true,
		},
		stats: []port.ManagementGroupAccountStatsRow{{
			SystemAccountID:    "sys_owner",
			GroupID:            "grp_owner",
			Total:              4,
			Available:          3,
			Active:             2,
			Disabled:           1,
			Error:              1,
			RateLimited:        2,
			CurrentConcurrency: 5,
			ConcurrencyLimit:   8,
		}},
		totalUsage: []port.ManagementGroupUsageRow{{
			Key: "grp_owner",
			Usage: port.ManagementAccountUsageSummary{
				RequestCount: 10,
				InputTokens:  120,
				OutputTokens: 30,
				TotalTokens:  999,
				TotalCost:    1.25,
				LastUsedAt:   &lastUsedAt,
			},
		}},
		todayUsage: []port.ManagementGroupUsageRow{{
			Key: "grp_owner",
			Usage: port.ManagementAccountUsageSummary{
				RequestCount: 2,
				InputTokens:  20,
				OutputTokens: 5,
				TotalTokens:  999,
				TotalCost:    0.25,
			},
		}},
		timezone:      "Asia/Shanghai",
		timezoneFound: true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})

	result, err := service.List(context.Background(), ListInput{
		ActorSystemAccountID: " sys_admin ",
		ActorRole:            "admin",
		SystemAccountID:      " all ",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.SystemAccountID != "" || store.listInput.Limit != 51 || store.listInput.Offset != 0 {
		t.Fatalf("list input = %+v", store.listInput)
	}
	if store.dailyStatDate != "2026-07-11" {
		t.Fatalf("daily stat date = %q, want 2026-07-11", store.dailyStatDate)
	}
	if len(store.statsGroupIDs) != 1 || store.statsGroupIDs[0] != "grp_owner" {
		t.Fatalf("stats group ids = %#v", store.statsGroupIDs)
	}
	if len(store.totalUsageInputs) != 1 {
		t.Fatalf("total usage inputs = %+v", store.totalUsageInputs)
	}
	usageInput := store.totalUsageInputs[0]
	if usageInput.Key != "grp_owner" ||
		usageInput.SystemAccountID != "sys_owner" ||
		usageInput.ScopeType != "group" ||
		usageInput.ScopeID != "grp_owner" {
		t.Fatalf("usage input = %+v", usageInput)
	}
	if result.Page != 1 || result.PageSize != 50 || !result.HasMore || result.Total != 2 {
		t.Fatalf("result page = %+v", result)
	}
	if !result.RuntimeSnapshot.AccountConcurrencyAvailable {
		t.Fatalf("runtime snapshot = %+v", result.RuntimeSnapshot)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %+v", result.Items)
	}
	item := result.Items[0]
	if item.SystemAccountID != "sys_owner" ||
		item.SystemAccountName != "所有者" ||
		item.OwnerSystemAccountID != "sys_owner" ||
		item.OwnerSystemAccountName != "所有者" ||
		item.AccessType != "owner" ||
		!item.IsDefault ||
		item.AccountCount != 4 ||
		item.AuthorizationSourceSummary != nil {
		t.Fatalf("owner item = %+v", item)
	}
	if item.SchedulingPolicy == nil ||
		item.SchedulingPolicy.DefaultSoftConcurrency != 5 ||
		item.SchedulingPolicy.ClientIPConcurrencyOverflowMode != "reject" {
		t.Fatalf("scheduling policy = %+v", item.SchedulingPolicy)
	}
	if item.AccountStats.Total != 4 ||
		item.AccountStats.Available != 3 ||
		item.AccountStats.Usage.TotalTokens != 150 ||
		item.AccountStats.TodayUsage.TotalTokens != 25 ||
		item.AccountStats.Usage.LastUsedAt == nil ||
		!item.AccountStats.Usage.LastUsedAt.Equal(lastUsedAt) {
		t.Fatalf("account stats = %+v", item.AccountStats)
	}
	if item.Permissions != ownerPermissions() {
		t.Fatalf("permissions = %+v", item.Permissions)
	}
	assertManagementGroupListDoesNotExposeDetails(t, result)
}

func TestServiceListMapsTargetAuthorizedRowsAndSourceSummary(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	expiredAt := now
	policyJSON := validManagementGroupListPolicyJSON()
	limitsJSON := `{"daily":{"requestCount":100}}`
	store := &managementGroupListStoreStub{
		page: port.ManagementGroupListPage{Rows: []port.ManagementGroupListRow{
			{
				ID:                "grp_owned",
				SystemAccountID:   "sys_target",
				SystemAccountName: "目标账户",
				Name:              "自有分组",
				ProviderCode:      "openai",
				Enabled:           true,
				IsDefault:         true,
				GroupType:         "personal",
				AccessType:        "owner",
			},
			{
				ID:                      "grp_authorized",
				SystemAccountID:         "sys_owner",
				SystemAccountName:       "授权方",
				Name:                    "授权分组",
				ProviderCode:            "openai",
				Enabled:                 false,
				IsDefault:               true,
				GroupType:               "high_concurrency",
				SchedulingPolicyJSON:    &policyJSON,
				AccessType:              "authorized",
				GroupAuthorizationID:    "rauthgrant_group",
				AuthorizationStatus:     "active",
				AuthorizationExpiresAt:  &expiredAt,
				AuthorizationLimitsJSON: &limitsJSON,
			},
		}},
		stats: []port.ManagementGroupAccountStatsRow{
			{SystemAccountID: "sys_target", GroupID: "grp_owned", Total: 2},
			{SystemAccountID: "sys_owner", GroupID: "grp_authorized", Total: 9, Available: 8},
		},
		totalUsage: []port.ManagementGroupUsageRow{
			{Key: "grp_owned", Usage: port.ManagementAccountUsageSummary{RequestCount: 3}},
			{Key: "rauthgrant_group", Usage: port.ManagementAccountUsageSummary{RequestCount: 7}},
		},
		todayUsage: []port.ManagementGroupUsageRow{
			{Key: "grp_owned", Usage: port.ManagementAccountUsageSummary{RequestCount: 1}},
			{Key: "rauthgrant_group", Usage: port.ManagementAccountUsageSummary{RequestCount: 4}},
		},
		sources: []port.ManagementGroupAuthorizationSourceRow{
			{AuthorizationID: "rauthgrant_group", SourceType: "manual", Status: "active"},
			{AuthorizationID: "rauthgrant_group", SourceType: "team", Status: "active", SourceTeamName: "研发组"},
			{AuthorizationID: "rauthgrant_group", SourceType: "team", Status: "active", SourceTeamName: " 研发组 "},
			{AuthorizationID: "rauthgrant_group", SourceType: "team", Status: "ended", SourceTeamName: "历史组"},
		},
		timezone:      "UTC",
		timezoneFound: true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})

	result, err := service.List(context.Background(), ListInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "super_admin",
		SystemAccountID:      " sys_target ",
		Page:                 999,
		PageSize:             500,
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listInput.SystemAccountID != "sys_target" ||
		store.listInput.Limit != 501 ||
		store.listInput.Offset != 500 {
		t.Fatalf("list input = %+v", store.listInput)
	}
	if result.Page != 2 || result.PageSize != 500 || result.Total != 502 || result.HasMore {
		t.Fatalf("result page = %+v", result)
	}
	if len(store.totalUsageInputs) != 2 {
		t.Fatalf("usage inputs = %+v", store.totalUsageInputs)
	}
	authorizedUsage := store.totalUsageInputs[1]
	if authorizedUsage.Key != "rauthgrant_group" ||
		authorizedUsage.SystemAccountID != "sys_owner" ||
		authorizedUsage.ScopeType != "group_authorization" ||
		authorizedUsage.ScopeID != "rauthgrant_group" {
		t.Fatalf("authorized usage input = %+v", authorizedUsage)
	}
	if len(store.sourceAuthorizationIDs) != 1 || store.sourceAuthorizationIDs[0] != "rauthgrant_group" {
		t.Fatalf("source authorization ids = %#v", store.sourceAuthorizationIDs)
	}
	authorized := result.Items[1]
	if authorized.SystemAccountID != "sys_owner" ||
		authorized.OwnerSystemAccountID != "sys_owner" ||
		authorized.AccessType != "authorized" ||
		authorized.Enabled ||
		authorized.IsDefault ||
		authorized.AccountCount != 0 ||
		authorized.AccountStats.Total != 9 ||
		authorized.AccountStats.Usage.RequestCount != 7 ||
		authorized.GroupAuthorizationID != "rauthgrant_group" ||
		authorized.AuthorizationStatus != "active" ||
		authorized.AuthorizationLimits["daily"] == nil {
		t.Fatalf("authorized item = %+v", authorized)
	}
	if authorized.Permissions.CanBindToAPIKey ||
		!authorized.Permissions.CanReturnAuthorization ||
		authorized.Permissions.CanDelete ||
		authorized.Permissions.CanAuthorize {
		t.Fatalf("authorized permissions = %+v", authorized.Permissions)
	}
	summary := authorized.AuthorizationSourceSummary
	if summary == nil ||
		summary.ActiveSourceCount != 3 ||
		!summary.HasManual ||
		!summary.HasTeam ||
		len(summary.TeamNames) != 1 ||
		summary.TeamNames[0] != "研发组" {
		t.Fatalf("source summary = %+v", summary)
	}
	if result.Items[0].AuthorizationSourceSummary != nil || result.Items[0].AccountCount != 2 {
		t.Fatalf("owner item = %+v", result.Items[0])
	}
	assertManagementGroupListDoesNotExposeDetails(t, result)
}

func TestServiceListForcesSelfScopeForUsersAndAdminMyGroups(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	for _, test := range []struct {
		name     string
		role     string
		selfOnly bool
	}{
		{name: "ordinary user", role: "user"},
		{name: "admin my groups", role: "admin", selfOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &managementGroupListStoreStub{
				page: port.ManagementGroupListPage{Rows: []port.ManagementGroupListRow{{
					ID:                     "grp_authorized",
					SystemAccountID:        "sys_owner",
					SystemAccountName:      "授权方",
					Name:                   "授权分组",
					ProviderCode:           "openai",
					Enabled:                true,
					GroupType:              "personal",
					AccessType:             "authorized",
					GroupAuthorizationID:   "rauthgrant_self",
					AuthorizationStatus:    "active",
					AuthorizationExpiresAt: &future,
				}}},
				timezone:      "UTC",
				timezoneFound: true,
			}
			service := NewServiceWithOptions(ServiceOptions{
				Store: store,
				Now:   func() time.Time { return now },
			})

			result, err := service.List(context.Background(), ListInput{
				ActorSystemAccountID: " sys_current ",
				ActorRole:            test.role,
				SystemAccountID:      "sys_other",
				SelfOnly:             test.selfOnly,
			})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if store.listInput.SystemAccountID != "sys_current" {
				t.Fatalf("system account id = %q, want sys_current", store.listInput.SystemAccountID)
			}
			if len(result.Items) != 1 {
				t.Fatalf("items = %+v", result.Items)
			}
			item := result.Items[0]
			if item.SystemAccountID != "" || item.SystemAccountName != "" {
				t.Fatalf("self item leaked admin fields = %+v", item)
			}
			if !item.Permissions.CanBindToAPIKey || item.Permissions.CanReturnAuthorization {
				t.Fatalf("permissions = %+v", item.Permissions)
			}
			if item.AuthorizationSourceSummary == nil ||
				item.AuthorizationSourceSummary.TeamNames == nil ||
				len(item.AuthorizationSourceSummary.TeamNames) != 0 {
				t.Fatalf("source summary = %+v", item.AuthorizationSourceSummary)
			}
		})
	}
}

func TestServiceListUsesProgressivePaginationBounds(t *testing.T) {
	tests := []struct {
		name         string
		page         int
		pageSize     int
		wantPage     int
		wantPageSize int
		wantOffset   int
		wantLimit    int
	}{
		{name: "defaults", wantPage: 1, wantPageSize: 50, wantOffset: 0, wantLimit: 51},
		{name: "negative defaults", page: -2, pageSize: -3, wantPage: 1, wantPageSize: 50, wantOffset: 0, wantLimit: 51},
		{name: "caps page size", page: 99, pageSize: 900, wantPage: 2, wantPageSize: 500, wantOffset: 500, wantLimit: 501},
		{name: "floors max page", page: 999, pageSize: 333, wantPage: 3, wantPageSize: 333, wantOffset: 666, wantLimit: 334},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementGroupListStoreStub{}
			service := NewServiceWithOptions(ServiceOptions{ListStore: store})

			result, err := service.List(context.Background(), ListInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				Page:                 test.page,
				PageSize:             test.pageSize,
			})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if store.listInput.Offset != test.wantOffset || store.listInput.Limit != test.wantLimit {
				t.Fatalf("list input = %+v", store.listInput)
			}
			if result.Page != test.wantPage || result.PageSize != test.wantPageSize {
				t.Fatalf("result = %+v", result)
			}
			if result.Items == nil || !result.RuntimeSnapshot.AccountConcurrencyAvailable {
				t.Fatalf("empty result = %+v", result)
			}
			if store.timezoneCalls != 0 ||
				store.statsCalls != 0 ||
				store.totalUsageCalls != 0 ||
				store.todayUsageCalls != 0 ||
				store.sourceCalls != 0 {
				t.Fatalf("empty page enrichment calls = timezone:%d stats:%d total:%d today:%d sources:%d",
					store.timezoneCalls,
					store.statsCalls,
					store.totalUsageCalls,
					store.todayUsageCalls,
					store.sourceCalls,
				)
			}
		})
	}
}

func TestServiceListDoesNotMaskEnrichmentFailures(t *testing.T) {
	wantErr := errors.New("postgres unavailable")
	basePage := port.ManagementGroupListPage{Rows: []port.ManagementGroupListRow{{
		ID:                "grp_owner",
		SystemAccountID:   "sys_owner",
		SystemAccountName: "所有者",
		Name:              "分组",
		ProviderCode:      "openai",
		Enabled:           true,
		GroupType:         "personal",
		AccessType:        "owner",
	}}}
	tests := []struct {
		name  string
		store *managementGroupListStoreStub
	}{
		{name: "list", store: &managementGroupListStoreStub{listErr: wantErr}},
		{name: "stats", store: &managementGroupListStoreStub{page: basePage, statsErr: wantErr}},
		{name: "total usage", store: &managementGroupListStoreStub{page: basePage, totalUsageErr: wantErr}},
		{
			name: "timezone setting",
			store: &managementGroupListStoreStub{
				page:          basePage,
				timezoneErr:   wantErr,
				timezoneFound: true,
			},
		},
		{
			name: "today usage",
			store: &managementGroupListStoreStub{
				page:          basePage,
				timezone:      "UTC",
				timezoneFound: true,
				todayUsageErr: wantErr,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewServiceWithOptions(ServiceOptions{ListStore: test.store, UsageStatsTimezoneStore: test.store})
			_, err := service.List(context.Background(), ListInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
			})
			if !errors.Is(err, wantErr) {
				t.Fatalf("List() error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestServiceListRejectsMissingActorAndInvalidTimezoneOrStoredJSON(t *testing.T) {
	t.Run("missing actor", func(t *testing.T) {
		store := &managementGroupListStoreStub{}
		service := NewServiceWithOptions(ServiceOptions{ListStore: store})
		_, err := service.List(context.Background(), ListInput{ActorRole: "admin"})
		if !errors.Is(err, ErrGroupListInvalid) {
			t.Fatalf("List() error = %v, want %v", err, ErrGroupListInvalid)
		}
		if store.listCalls != 0 {
			t.Fatalf("list calls = %d, want 0", store.listCalls)
		}
	})

	t.Run("invalid timezone", func(t *testing.T) {
		store := &managementGroupListStoreStub{
			page: port.ManagementGroupListPage{Rows: []port.ManagementGroupListRow{{
				ID:              "grp_owner",
				SystemAccountID: "sys_owner",
				Name:            "分组",
				ProviderCode:    "openai",
				GroupType:       "personal",
			}}},
			timezone:      "Invalid/Timezone",
			timezoneFound: true,
		}
		service := NewServiceWithOptions(ServiceOptions{ListStore: store, UsageStatsTimezoneStore: store})
		_, err := service.List(context.Background(), ListInput{
			ActorSystemAccountID: "sys_admin",
			ActorRole:            "admin",
		})
		if err == nil || !strings.Contains(err.Error(), "usageStatsTimezone") {
			t.Fatalf("List() error = %v, want usageStatsTimezone error", err)
		}
	})

	t.Run("malformed authorization limits", func(t *testing.T) {
		limitsJSON := "{"
		store := &managementGroupListStoreStub{
			page: port.ManagementGroupListPage{Rows: []port.ManagementGroupListRow{{
				ID:                      "grp_authorized",
				SystemAccountID:         "sys_owner",
				Name:                    "分组",
				ProviderCode:            "openai",
				GroupType:               "personal",
				AccessType:              "authorized",
				GroupAuthorizationID:    "rauthgrant_bad",
				AuthorizationLimitsJSON: &limitsJSON,
			}}},
			timezone:      "UTC",
			timezoneFound: true,
		}
		service := NewServiceWithOptions(ServiceOptions{ListStore: store, UsageStatsTimezoneStore: store})
		_, err := service.List(context.Background(), ListInput{
			ActorSystemAccountID: "sys_admin",
			ActorRole:            "admin",
		})
		if err == nil || !strings.Contains(err.Error(), "authorization limits") {
			t.Fatalf("List() error = %v, want authorization limits error", err)
		}
	})
}

func assertManagementGroupListDoesNotExposeDetails(t *testing.T, result ListResult) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal list result: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode list result: %v", err)
	}
	items, ok := payload["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("encoded items = %#v", payload["items"])
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("encoded item = %#v", raw)
		}
		if _, exists := item["accountIds"]; exists {
			t.Fatalf("list item exposed accountIds: %#v", item)
		}
		if _, exists := item["authorizationSources"]; exists {
			t.Fatalf("list item exposed authorizationSources: %#v", item)
		}
	}
}

func validManagementGroupListPolicyJSON() string {
	policy, err := json.Marshal(SchedulingPolicy{
		Mode:                            "balanced_fast",
		DefaultSoftConcurrency:          5,
		FastFirstEnabled:                true,
		FallbackOnQueueEnabled:          true,
		BreakAffinityOnSoftLimit:        true,
		BreakAffinityOnQueueWaitMs:      0,
		SlowRequestThresholdMs:          30000,
		FirstOutputSlowThresholdMs:      15000,
		RecentTimeoutWindowSeconds:      120,
		RecentTimeoutPenaltyThreshold:   2,
		MaxQueueWaitMs:                  60000,
		MaxQueueSize:                    1000,
		PerAPIKeyQueueLimit:             1000,
		ClientIPConcurrencyLimit:        0,
		ClientIPConcurrencyOverflowMode: "reject",
		ImageLaneMaxConcurrency:         0,
	})
	if err != nil {
		panic(err)
	}
	return string(policy)
}

func stringPointer(value string) *string {
	return &value
}

type managementGroupListStoreStub struct {
	listInput              port.ManagementGroupListInput
	statsGroupIDs          []string
	totalUsageInputs       []port.ManagementGroupUsageLookupInput
	todayUsageInputs       []port.ManagementGroupUsageLookupInput
	dailyStatDate          string
	sourceAuthorizationIDs []string
	page                   port.ManagementGroupListPage
	stats                  []port.ManagementGroupAccountStatsRow
	totalUsage             []port.ManagementGroupUsageRow
	todayUsage             []port.ManagementGroupUsageRow
	sources                []port.ManagementGroupAuthorizationSourceRow
	timezone               string
	timezoneFound          bool
	listErr                error
	statsErr               error
	totalUsageErr          error
	todayUsageErr          error
	sourceErr              error
	timezoneErr            error
	listCalls              int
	statsCalls             int
	totalUsageCalls        int
	todayUsageCalls        int
	sourceCalls            int
	timezoneCalls          int
}

func (s *managementGroupListStoreStub) ListManagementGroupOptions(
	context.Context,
	port.ManagementGroupOptionListInput,
) ([]port.ManagementGroupOption, error) {
	return nil, nil
}

func (s *managementGroupListStoreStub) ListManagementGroupAccountOptions(
	context.Context,
	port.ManagementGroupOptionListInput,
) ([]port.ManagementGroupAccountOption, error) {
	return nil, nil
}

func (s *managementGroupListStoreStub) ListManagementGroups(
	_ context.Context,
	input port.ManagementGroupListInput,
) (port.ManagementGroupListPage, error) {
	s.listCalls++
	s.listInput = input
	return s.page, s.listErr
}

func (s *managementGroupListStoreStub) ListManagementGroupAccountStats(
	_ context.Context,
	groupIDs []string,
) ([]port.ManagementGroupAccountStatsRow, error) {
	s.statsCalls++
	s.statsGroupIDs = append([]string(nil), groupIDs...)
	return s.stats, s.statsErr
}

func (s *managementGroupListStoreStub) ListManagementGroupUsageTotals(
	_ context.Context,
	inputs []port.ManagementGroupUsageLookupInput,
) ([]port.ManagementGroupUsageRow, error) {
	s.totalUsageCalls++
	s.totalUsageInputs = append([]port.ManagementGroupUsageLookupInput(nil), inputs...)
	return s.totalUsage, s.totalUsageErr
}

func (s *managementGroupListStoreStub) ListManagementGroupUsageDaily(
	_ context.Context,
	statDate string,
	inputs []port.ManagementGroupUsageLookupInput,
) ([]port.ManagementGroupUsageRow, error) {
	s.todayUsageCalls++
	s.dailyStatDate = statDate
	s.todayUsageInputs = append([]port.ManagementGroupUsageLookupInput(nil), inputs...)
	return s.todayUsage, s.todayUsageErr
}

func (s *managementGroupListStoreStub) ListManagementGroupAuthorizationSources(
	_ context.Context,
	authorizationIDs []string,
) ([]port.ManagementGroupAuthorizationSourceRow, error) {
	s.sourceCalls++
	s.sourceAuthorizationIDs = append([]string(nil), authorizationIDs...)
	return s.sources, s.sourceErr
}

func (s *managementGroupListStoreStub) GetManagementUsageStatsTimezone(
	context.Context,
) (string, bool, error) {
	s.timezoneCalls++
	return s.timezone, s.timezoneFound, s.timezoneErr
}

var _ port.ManagementGroupOptionReader = (*managementGroupListStoreStub)(nil)
var _ port.ManagementGroupListReader = (*managementGroupListStoreStub)(nil)
var _ port.ManagementUsageStatsTimezoneReader = (*managementGroupListStoreStub)(nil)
