package managementgroups

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceListMapsAdminOwnerPageFromPreaggregates(t *testing.T) {
	now := time.Date(2026, 7, 10, 16, 30, 0, 0, time.UTC)
	store := &managementGroupListStoreStub{
		page: port.ManagementGroupListPage{
			Rows: []port.ManagementGroupListRow{{
				ID:                "grp_owner",
				SystemAccountID:   "sys_owner",
				SystemAccountName: "所有者",
				Name:              "默认分组",
				ProviderCode:      "openai",
				Description:       stringPointer("主分组"),
				Enabled:           true,
				IsDefault:         true,
				GroupType:         "high_concurrency",
				AccessType:        "owner",
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
	}
	concurrency := &managementGroupConcurrencyReaderStub{err: errors.New("must not be called")}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              store,
		AccountConcurrency: concurrency,
		Now:                func() time.Time { return now },
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
	if len(store.statsGroupIDs) != 1 || store.statsGroupIDs[0] != "grp_owner" {
		t.Fatalf("stats group ids = %#v", store.statsGroupIDs)
	}
	if len(concurrency.calls) != 0 {
		t.Fatalf("concurrency calls = %#v, want none", concurrency.calls)
	}
	if result.Page != 1 || result.PageSize != 50 || !result.HasMore || result.Total != 2 {
		t.Fatalf("result page = %+v", result)
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
		item.AuthorizationSourceSummary != nil {
		t.Fatalf("owner item = %+v", item)
	}
	if item.AccountStats.Total != 4 ||
		item.AccountStats.Available != 3 ||
		item.AccountStats.ConcurrencyLimit != 8 {
		t.Fatalf("account stats = %+v", item.AccountStats)
	}
	if item.CanEdit || item.CanDelete || item.CanReturn {
		t.Fatalf("row actions = %+v", item)
	}
	assertManagementGroupListDoesNotExposeDetails(t, result)
}

func TestServiceListMapsTargetAuthorizedRowsAndSourceSummary(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	expiredAt := now
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
				ID:                     "grp_authorized",
				SystemAccountID:        "sys_owner",
				SystemAccountName:      "授权方",
				Name:                   "授权分组",
				ProviderCode:           "openai",
				Enabled:                false,
				IsDefault:              true,
				GroupType:              "high_concurrency",
				AccessType:             "authorized",
				GroupAuthorizationID:   "rauthgrant_group",
				AuthorizationStatus:    "active",
				AuthorizationExpiresAt: &expiredAt,
			},
		}},
		stats: []port.ManagementGroupAccountStatsRow{
			{SystemAccountID: "sys_target", GroupID: "grp_owned", Total: 2},
			{SystemAccountID: "sys_owner", GroupID: "grp_authorized", Total: 9, Available: 8},
		},
		sources: []port.ManagementGroupAuthorizationSourceRow{
			{AuthorizationID: "rauthgrant_group", SourceType: "manual", Status: "active"},
			{AuthorizationID: "rauthgrant_group", SourceType: "team", Status: "active", SourceTeamName: "研发组"},
			{AuthorizationID: "rauthgrant_group", SourceType: "team", Status: "active", SourceTeamName: " 研发组 "},
			{AuthorizationID: "rauthgrant_group", SourceType: "team", Status: "ended", SourceTeamName: "历史组"},
		},
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
	if len(store.sourceAuthorizationIDs) != 1 || store.sourceAuthorizationIDs[0] != "rauthgrant_group" {
		t.Fatalf("source authorization ids = %#v", store.sourceAuthorizationIDs)
	}
	authorized := result.Items[1]
	if authorized.SystemAccountID != "sys_owner" ||
		authorized.OwnerSystemAccountID != "sys_owner" ||
		authorized.AccessType != "authorized" ||
		authorized.Enabled ||
		authorized.IsDefault ||
		authorized.AccountStats.Total != 9 ||
		authorized.GroupAuthorizationID != "rauthgrant_group" ||
		authorized.AuthorizationStatus != "active" {
		t.Fatalf("authorized item = %+v", authorized)
	}
	if !authorized.CanEdit || authorized.CanDelete || authorized.CanReturn {
		t.Fatalf("authorized actions = %+v", authorized)
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
	if result.Items[0].AuthorizationSourceSummary != nil || result.Items[0].AccountStats.Total != 2 {
		t.Fatalf("owner item = %+v", result.Items[0])
	}
	assertManagementGroupListDoesNotExposeDetails(t, result)
}

func TestServiceListUsesPreaggregatedConcurrencyWithoutRuntimeReads(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	store := &managementGroupListStoreStub{
		page: port.ManagementGroupListPage{Rows: []port.ManagementGroupListRow{
			{
				ID:              "grp_owner_a",
				SystemAccountID: "sys_owner",
				Name:            "Owner A",
				ProviderCode:    "openai",
				GroupType:       "personal",
				AccessType:      "owner",
			},
			{
				ID:                   "grp_authorized",
				SystemAccountID:      "sys_other",
				Name:                 "Authorized",
				ProviderCode:         "openai",
				GroupType:            "personal",
				AccessType:           "authorized",
				GroupAuthorizationID: "rauth_group",
			},
			{
				ID:              "grp_owner_empty",
				SystemAccountID: "sys_owner",
				Name:            "Owner Empty",
				ProviderCode:    "openai",
				GroupType:       "personal",
				AccessType:      "owner",
			},
		}},
		stats: []port.ManagementGroupAccountStatsRow{
			{SystemAccountID: "sys_owner", GroupID: "grp_owner_a", Total: 205, CurrentConcurrency: 999},
			{SystemAccountID: "sys_other", GroupID: "grp_authorized", Total: 4, CurrentConcurrency: 77},
			{SystemAccountID: "sys_owner", GroupID: "grp_owner_empty", CurrentConcurrency: 8},
		},
	}
	concurrency := &managementGroupConcurrencyReaderStub{err: errors.New("must not be called")}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              store,
		AccountConcurrency: concurrency,
		Now:                func() time.Time { return now },
	})

	result, err := service.List(context.Background(), ListInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(concurrency.calls) != 0 {
		t.Fatalf("concurrency calls = %#v, want none", concurrency.calls)
	}
	owner := result.Items[0]
	if owner.AccountStats.Total != 205 {
		t.Fatalf("owner stats = %+v", owner.AccountStats)
	}
	authorized := result.Items[1]
	if authorized.AccountStats.Total != 4 {
		t.Fatalf("authorized stats = %+v", authorized.AccountStats)
	}
	emptyOwner := result.Items[2]
	if emptyOwner.AccountStats.Total != 0 {
		t.Fatalf("empty owner stats = %+v", emptyOwner.AccountStats)
	}
}

func TestServiceListKeepsPreaggregatesWhenRuntimeReaderIsUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	store := &managementGroupListStoreStub{
		page: port.ManagementGroupListPage{Rows: []port.ManagementGroupListRow{
			{
				ID:              "grp_owner_live",
				SystemAccountID: "sys_owner",
				Name:            "Owner Live",
				ProviderCode:    "openai",
				GroupType:       "personal",
				AccessType:      "owner",
			},
			{
				ID:              "grp_owner_empty",
				SystemAccountID: "sys_owner",
				Name:            "Owner Empty",
				ProviderCode:    "openai",
				GroupType:       "personal",
				AccessType:      "owner",
			},
			{
				ID:                   "grp_authorized",
				SystemAccountID:      "sys_other",
				Name:                 "Authorized",
				ProviderCode:         "openai",
				GroupType:            "personal",
				AccessType:           "authorized",
				GroupAuthorizationID: "rauth_group",
			},
		}},
		stats: []port.ManagementGroupAccountStatsRow{
			{SystemAccountID: "sys_owner", GroupID: "grp_owner_live", Total: 1, CurrentConcurrency: 17},
			{SystemAccountID: "sys_owner", GroupID: "grp_owner_empty", CurrentConcurrency: 8},
			{SystemAccountID: "sys_other", GroupID: "grp_authorized", Total: 1, CurrentConcurrency: 9},
		},
	}
	concurrency := &managementGroupConcurrencyReaderStub{err: errors.New("redis unavailable")}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              store,
		AccountConcurrency: concurrency,
		Now:                func() time.Time { return now },
	})

	result, err := service.List(context.Background(), ListInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(concurrency.calls) != 0 {
		t.Fatalf("concurrency calls = %#v, want none", concurrency.calls)
	}
	liveOwner := result.Items[0]
	if liveOwner.AccountStats.Total != 1 {
		t.Fatalf("live owner stats = %+v", liveOwner.AccountStats)
	}
	emptyOwner := result.Items[1]
	if emptyOwner.AccountStats.Total != 0 {
		t.Fatalf("empty owner stats = %+v", emptyOwner.AccountStats)
	}
	authorized := result.Items[2]
	if authorized.AccountStats.Total != 1 {
		t.Fatalf("authorized stats = %+v", authorized.AccountStats)
	}
}

func TestServiceListAuthorizedOnlySkipsAccountIDAndConcurrencyReads(t *testing.T) {
	store := &managementGroupListStoreStub{
		page: port.ManagementGroupListPage{Rows: []port.ManagementGroupListRow{{
			ID:                   "grp_authorized",
			SystemAccountID:      "sys_owner",
			Name:                 "Authorized",
			ProviderCode:         "openai",
			GroupType:            "personal",
			AccessType:           "authorized",
			GroupAuthorizationID: "rauth_group",
		}}},
		stats: []port.ManagementGroupAccountStatsRow{{
			SystemAccountID:    "sys_owner",
			GroupID:            "grp_authorized",
			CurrentConcurrency: 12,
		}},
	}
	concurrency := &managementGroupConcurrencyReaderStub{
		err: errors.New("must not be called"),
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              store,
		AccountConcurrency: concurrency,
	})

	result, err := service.List(context.Background(), ListInput{
		ActorSystemAccountID: "sys_user",
		ActorRole:            "user",
	})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(concurrency.calls) != 0 {
		t.Fatalf("concurrency calls = %#v, want none", concurrency.calls)
	}
	if result.Items[0].AccountStats.Total != 0 {
		t.Fatalf("result = %+v", result)
	}
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
			if !item.CanEdit || item.CanDelete || item.CanReturn {
				t.Fatalf("row actions = %+v", item)
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
		pageSizeSet  bool
		wantPage     int
		wantPageSize int
		wantOffset   int
		wantLimit    int
	}{
		{name: "defaults", wantPage: 1, wantPageSize: 50, wantOffset: 0, wantLimit: 51},
		{name: "negative values clamp", page: -2, pageSize: -3, wantPage: 1, wantPageSize: 1, wantOffset: 0, wantLimit: 2},
		{name: "caps page size", page: 99, pageSize: 900, wantPage: 2, wantPageSize: 500, wantOffset: 500, wantLimit: 501},
		{name: "floors max page", page: 999, pageSize: 333, wantPage: 3, wantPageSize: 333, wantOffset: 666, wantLimit: 334},
		{name: "explicit zero page size clamps", page: 1, pageSizeSet: true, wantPage: 1, wantPageSize: 1, wantOffset: 0, wantLimit: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementGroupListStoreStub{}
			service := NewServiceWithOptions(ServiceOptions{ListStore: store})

			input := ListInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				Page:                 test.page,
				PageSize:             test.pageSize,
				PageSizeProvided:     test.pageSizeSet,
			}
			result, err := service.List(context.Background(), input)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if store.listInput.Offset != test.wantOffset || store.listInput.Limit != test.wantLimit {
				t.Fatalf("list input = %+v", store.listInput)
			}
			if result.Page != test.wantPage || result.PageSize != test.wantPageSize {
				t.Fatalf("result = %+v", result)
			}
			if result.Items == nil {
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewServiceWithOptions(ServiceOptions{ListStore: test.store})
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

func TestServiceListRejectsMissingActor(t *testing.T) {
	store := &managementGroupListStoreStub{}
	service := NewServiceWithOptions(ServiceOptions{ListStore: store})
	_, err := service.List(context.Background(), ListInput{ActorRole: "admin"})
	if !errors.Is(err, ErrGroupListInvalid) {
		t.Fatalf("List() error = %v, want %v", err, ErrGroupListInvalid)
	}
	if store.listCalls != 0 {
		t.Fatalf("list calls = %d, want 0", store.listCalls)
	}
}

func TestParseManagementGroupAuthorizationLimitsMatchesNodeContract(t *testing.T) {
	t.Run("empty values return an empty object", func(t *testing.T) {
		for _, value := range []*string{nil, stringPointer(""), stringPointer("null"), stringPointer("{}")} {
			limits, err := parseManagementGroupAuthorizationLimits(value)
			if err != nil {
				t.Fatalf("parseManagementGroupAuthorizationLimits() error = %v", err)
			}
			if limits != (port.ManagementRequestQuotaLimits{}) {
				t.Fatalf("limits = %+v, want empty", limits)
			}
		}
	})

	t.Run("valid limits are normalized", func(t *testing.T) {
		value := `{
			"hourly":{"enabled":true,"hours":24,"limit":1.25},
			"daily":{"enabled":true,"limit":2.5},
			"weekly":{"enabled":true,"limit":3},
			"monthly":{"enabled":true,"limit":4},
			"total":{"enabled":true,"limit":5}
		}`
		limits, err := parseManagementGroupAuthorizationLimits(&value)
		if err != nil {
			t.Fatalf("parseManagementGroupAuthorizationLimits() error = %v", err)
		}
		if limits.Hourly == nil ||
			limits.Hourly.Hours != 24 ||
			limits.Hourly.Limit != 1.25 ||
			limits.Daily == nil ||
			limits.Daily.Limit != 2.5 ||
			limits.Weekly == nil ||
			limits.Monthly == nil ||
			limits.Total == nil {
			t.Fatalf("limits = %+v", limits)
		}
	})

	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "unknown top level field", value: `{"daily":{"enabled":true,"limit":1},"extra":{}}`},
		{name: "null quota", value: `{"daily":null}`},
		{name: "disabled quota", value: `{"daily":{"enabled":false,"limit":1}}`},
		{name: "unknown nested field", value: `{"daily":{"enabled":true,"limit":1,"extra":1}}`},
		{name: "non positive amount", value: `{"daily":{"enabled":true,"limit":0}}`},
		{name: "too precise amount", value: `{"daily":{"enabled":true,"limit":0.0000001}}`},
		{name: "hour window below range", value: `{"hourly":{"enabled":true,"hours":0,"limit":1}}`},
		{name: "hour window above range", value: `{"hourly":{"enabled":true,"hours":721,"limit":1}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseManagementGroupAuthorizationLimits(&test.value); err == nil {
				t.Fatalf("parseManagementGroupAuthorizationLimits(%s) error = nil", test.value)
			}
		})
	}
}

func TestParseManagementGroupListSchedulingPolicyRejectsNullRequiredFields(t *testing.T) {
	for _, key := range []string{
		"fastFirstEnabled",
		"fallbackOnQueueEnabled",
		"breakAffinityOnSoftLimit",
		"breakAffinityOnQueueWaitMs",
		"clientIpConcurrencyLimit",
		"imageLaneMaxConcurrency",
	} {
		t.Run(key, func(t *testing.T) {
			var policy map[string]any
			if err := json.Unmarshal([]byte(validManagementGroupListPolicyJSON()), &policy); err != nil {
				t.Fatalf("decode valid policy: %v", err)
			}
			policy[key] = nil
			encoded, err := json.Marshal(policy)
			if err != nil {
				t.Fatalf("encode policy: %v", err)
			}
			value := string(encoded)
			if _, err := parseManagementGroupListSchedulingPolicy(&value, "high_concurrency"); err == nil {
				t.Fatalf("null %s error = nil", key)
			}
		})
	}
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
		for _, forbidden := range []string{
			"accountIds", "accountCount", "authorizationLimits", "authorizationSources",
			"currentConcurrency", "currentConcurrencyAvailable", "permissions", "runtimeSnapshot",
			"schedulingPolicy", "todayUsage", "usage",
		} {
			if _, exists := item[forbidden]; exists {
				t.Fatalf("list item exposed %s: %#v", forbidden, item)
			}
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

type managementGroupConcurrencyReaderStub struct {
	calls [][]string
	err   error
}

func (s *managementGroupConcurrencyReaderStub) LoadAccountCurrentConcurrencyByIDs(
	_ context.Context,
	accountIDs []string,
	_ time.Time,
) (map[string]int, error) {
	s.calls = append(s.calls, append([]string(nil), accountIDs...))
	if s.err != nil {
		return nil, s.err
	}
	return map[string]int{}, nil
}

var _ AccountConcurrencyReader = (*managementGroupConcurrencyReaderStub)(nil)
