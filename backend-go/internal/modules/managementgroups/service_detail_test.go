package managementgroups

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceDetailMapsOwnerWithLiveConcurrency(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	policyJSON := validManagementGroupListPolicyJSON()
	store := &managementGroupDetailStoreStub{
		managementGroupListStoreStub: managementGroupListStoreStub{
			stats: []port.ManagementGroupAccountStatsRow{{
				SystemAccountID:    "sys_owner",
				GroupID:            "grp_owner",
				Total:              99,
				Available:          2,
				Active:             2,
				CurrentConcurrency: 88,
				ConcurrencyLimit:   10,
			}},
			totalUsage: []port.ManagementGroupUsageRow{{
				Key:   "grp_owner",
				Usage: port.ManagementAccountUsageSummary{RequestCount: 7, InputTokens: 10, OutputTokens: 5},
			}},
			todayUsage: []port.ManagementGroupUsageRow{{
				Key:   "grp_owner",
				Usage: port.ManagementAccountUsageSummary{RequestCount: 2},
			}},
			timezone:      "UTC",
			timezoneFound: true,
		},
		row: port.ManagementGroupListRow{
			ID:                   "grp_owner",
			SystemAccountID:      "sys_owner",
			SystemAccountName:    "所有者",
			Name:                 "主分组",
			ProviderCode:         "openai",
			Enabled:              true,
			IsDefault:            true,
			GroupType:            "high_concurrency",
			SchedulingPolicyJSON: &policyJSON,
			AccessType:           "owner",
		},
		found:      true,
		accountIDs: []string{"acct_1", "acct_2"},
	}
	concurrency := &managementGroupAccountConcurrencyStub{
		values: map[string]int{"acct_1": 2, "acct_2": 3},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              store,
		AccountConcurrency: concurrency,
		Now:                func() time.Time { return now },
	})

	result, err := service.Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		GroupID:              "grp_owner",
	})
	if err != nil {
		t.Fatalf("Detail() error = %v", err)
	}
	if store.detailInput.SystemAccountID != "" ||
		result.SystemAccountID != "sys_owner" ||
		result.OwnerSystemAccountName != "所有者" ||
		result.AccessType != "owner" ||
		result.AuthorizationSources != nil ||
		result.Permissions != ownerPermissions() {
		t.Fatalf("owner detail = %+v input=%+v", result, store.detailInput)
	}
	if !slices.Equal(result.AccountIDs, []string{"acct_1", "acct_2"}) ||
		result.AccountStats.Total != 2 ||
		result.AccountStats.CurrentConcurrency != 5 ||
		result.AccountStats.Available != 2 ||
		result.AccountStats.ConcurrencyLimit != 10 ||
		result.AccountStats.Usage.TotalTokens != 15 {
		t.Fatalf("owner account data = ids=%v stats=%+v", result.AccountIDs, result.AccountStats)
	}
	if concurrency.calls != 1 || !slices.Equal(concurrency.accountIDs, store.accountIDs) {
		t.Fatalf("concurrency calls=%d ids=%v", concurrency.calls, concurrency.accountIDs)
	}
}

func TestServiceDetailMapsAuthorizedAndSanitizesSources(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	expiresAt := now.Add(time.Hour)
	limitsJSON := `{"daily":{"enabled":true,"limit":12.5}}`
	createdAt := now.Add(-time.Hour)
	store := &managementGroupDetailStoreStub{
		managementGroupListStoreStub: managementGroupListStoreStub{
			stats: []port.ManagementGroupAccountStatsRow{{
				SystemAccountID:    "sys_owner",
				GroupID:            "grp_authorized",
				Total:              7,
				CurrentConcurrency: 4,
			}},
			totalUsage: []port.ManagementGroupUsageRow{{
				Key:   "rauth_group",
				Usage: port.ManagementAccountUsageSummary{RequestCount: 9},
			}},
			todayUsage: []port.ManagementGroupUsageRow{{
				Key:   "rauth_group",
				Usage: port.ManagementAccountUsageSummary{RequestCount: 3},
			}},
			timezone:      "UTC",
			timezoneFound: true,
		},
		row: port.ManagementGroupListRow{
			ID:                      "grp_authorized",
			SystemAccountID:         "sys_owner",
			SystemAccountName:       "授权方",
			Name:                    "授权分组",
			ProviderCode:            "openai",
			Enabled:                 true,
			IsDefault:               true,
			GroupType:               "personal",
			AccessType:              "authorized",
			GroupAuthorizationID:    "rauth_group",
			AuthorizationStatus:     "active",
			AuthorizationExpiresAt:  &expiresAt,
			AuthorizationLimitsJSON: &limitsJSON,
		},
		found:      true,
		accountIDs: []string{"acct_hidden"},
		sources: []port.ManagementResourceAuthorizationSourceSummary{{
			ID:              "rasrc_manual",
			AuthorizationID: "rauth_group",
			SourceType:      "manual",
			SourceTeamID:    "team_sensitive",
			Status:          "active",
			CreatedBy:       "sys_sensitive",
			CreatedAt:       createdAt,
			RevokedBy:       "sys_sensitive",
			RevokedAt:       &createdAt,
			UpdatedAt:       now,
		}},
	}
	concurrency := &managementGroupAccountConcurrencyStub{
		values: map[string]int{"acct_hidden": 8},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              store,
		AccountConcurrency: concurrency,
		Now:                func() time.Time { return now },
	})

	result, err := service.Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_viewer",
		ActorRole:            "admin",
		SystemAccountID:      "sys_viewer",
		GroupID:              "grp_authorized",
	})
	if err != nil {
		t.Fatalf("Detail() error = %v", err)
	}
	if len(result.AccountIDs) != 0 ||
		result.AccountStats.Total != 7 ||
		result.AccountStats.CurrentConcurrency != 4 ||
		result.IsDefault ||
		result.AccessType != "authorized" ||
		result.AuthorizationSources == nil ||
		len(*result.AuthorizationSources) != 1 ||
		!result.Permissions.CanReturnAuthorization ||
		!result.Permissions.CanBindToAPIKey {
		t.Fatalf("authorized detail = %+v", result)
	}
	source := (*result.AuthorizationSources)[0]
	if source.CreatedBy != "" || source.ID != "rasrc_manual" || source.AuthorizationID != "rauth_group" {
		t.Fatalf("sanitized source = %+v", source)
	}
	if concurrency.calls != 1 || !slices.Equal(concurrency.accountIDs, []string{"acct_hidden"}) {
		t.Fatalf("concurrency calls=%d ids=%v", concurrency.calls, concurrency.accountIDs)
	}
}

func TestServiceDetailForcesSelfScopeAndStopsOnNotFound(t *testing.T) {
	store := &managementGroupDetailStoreStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              store,
		AccountConcurrency: &managementGroupAccountConcurrencyStub{},
	})

	_, err := service.Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_current",
		ActorRole:            "admin",
		SystemAccountID:      "sys_other",
		SelfOnly:             true,
		GroupID:              "grp_missing",
	})
	if !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("Detail() error = %v, want ErrGroupNotFound", err)
	}
	if store.detailInput.SystemAccountID != "sys_current" ||
		store.findCalls != 1 ||
		store.accountIDCalls != 0 ||
		store.sourceCalls != 0 {
		t.Fatalf("detail input=%+v find calls=%d account calls=%d source calls=%d", store.detailInput, store.findCalls, store.accountIDCalls, store.sourceCalls)
	}
}

func TestServiceDetailPreservesLookupKeysAndRejectsBlankGroupID(t *testing.T) {
	store := &managementGroupDetailStoreStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              store,
		AccountConcurrency: &managementGroupAccountConcurrencyStub{},
	})

	_, err := service.Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		SystemAccountID:      "\u0085",
		GroupID:              " grp_missing ",
	})
	if !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("Detail() exact lookup error = %v, want ErrGroupNotFound", err)
	}
	if store.findCalls != 1 ||
		store.detailInput.GroupID != " grp_missing " ||
		store.detailInput.SystemAccountID != "\u0085" {
		t.Fatalf("exact lookup calls=%d input=%+v", store.findCalls, store.detailInput)
	}

	for _, groupID := range []string{"", " \t\r\n"} {
		store.findCalls = 0
		store.detailInput = port.ManagementGroupDetailInput{}
		_, err = service.Detail(context.Background(), DetailInput{
			ActorSystemAccountID: "sys_admin",
			ActorRole:            "admin",
			GroupID:              groupID,
		})
		if !errors.Is(err, ErrGroupNotFound) || store.findCalls != 0 {
			t.Fatalf("blank group %q error=%v find calls=%d input=%+v", groupID, err, store.findCalls, store.detailInput)
		}
	}
}

func TestServiceDetailPropagatesConcurrencyFailureForAuthorizedView(t *testing.T) {
	store := &managementGroupDetailStoreStub{
		managementGroupListStoreStub: managementGroupListStoreStub{
			timezone:      "UTC",
			timezoneFound: true,
		},
		row: port.ManagementGroupListRow{
			ID:                   "grp_authorized",
			SystemAccountID:      "sys_owner",
			GroupType:            "personal",
			AccessType:           "authorized",
			GroupAuthorizationID: "rauth_group",
		},
		found:      true,
		accountIDs: []string{"acct_hidden"},
	}
	readerErr := errors.New("redis unavailable")
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		AccountConcurrency: &managementGroupAccountConcurrencyStub{
			err: readerErr,
		},
	})

	_, err := service.Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_viewer",
		ActorRole:            "user",
		SelfOnly:             true,
		GroupID:              "grp_authorized",
	})
	if !errors.Is(err, readerErr) {
		t.Fatalf("Detail() error = %v, want reader error", err)
	}
}

func TestServiceDetailRejectsMalformedStoredPolicy(t *testing.T) {
	policyJSON := `{"mode":"balanced_fast"}`
	store := &managementGroupDetailStoreStub{
		managementGroupListStoreStub: managementGroupListStoreStub{
			timezone:      "UTC",
			timezoneFound: true,
		},
		row: port.ManagementGroupListRow{
			ID:                   "grp_invalid",
			SystemAccountID:      "sys_owner",
			GroupType:            "high_concurrency",
			SchedulingPolicyJSON: &policyJSON,
			AccessType:           "owner",
		},
		found: true,
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              store,
		AccountConcurrency: &managementGroupAccountConcurrencyStub{},
	})

	_, err := service.Detail(context.Background(), DetailInput{
		ActorSystemAccountID: "sys_owner",
		ActorRole:            "user",
		SelfOnly:             true,
		GroupID:              "grp_invalid",
	})
	if err == nil || !strings.Contains(err.Error(), "调度策略") {
		t.Fatalf("Detail() error = %v, want policy error", err)
	}
}

type managementGroupDetailStoreStub struct {
	managementGroupListStoreStub
	detailInput    port.ManagementGroupDetailInput
	row            port.ManagementGroupListRow
	found          bool
	findCalls      int
	findErr        error
	accountIDs     []string
	accountIDCalls int
	accountIDErr   error
	sources        []port.ManagementResourceAuthorizationSourceSummary
	sourceCalls    int
	sourceErr      error
}

func (s *managementGroupDetailStoreStub) FindManagementGroupDetail(
	_ context.Context,
	input port.ManagementGroupDetailInput,
) (port.ManagementGroupListRow, bool, error) {
	s.findCalls++
	s.detailInput = input
	return s.row, s.found, s.findErr
}

func (s *managementGroupDetailStoreStub) ListManagementGroupDetailAccountIDs(
	_ context.Context,
	input port.ManagementGroupDetailInput,
) ([]string, error) {
	s.accountIDCalls++
	s.detailInput = input
	return append([]string(nil), s.accountIDs...), s.accountIDErr
}

func (s *managementGroupDetailStoreStub) ListManagementGroupDetailAuthorizationSources(
	_ context.Context,
	input port.ManagementGroupDetailInput,
) ([]port.ManagementResourceAuthorizationSourceSummary, error) {
	s.sourceCalls++
	s.detailInput = input
	return append([]port.ManagementResourceAuthorizationSourceSummary(nil), s.sources...), s.sourceErr
}

type managementGroupAccountConcurrencyStub struct {
	calls      int
	accountIDs []string
	now        time.Time
	values     map[string]int
	err        error
}

func (s *managementGroupAccountConcurrencyStub) LoadAccountCurrentConcurrencyByIDs(
	_ context.Context,
	accountIDs []string,
	now time.Time,
) (map[string]int, error) {
	s.calls++
	s.accountIDs = append([]string(nil), accountIDs...)
	s.now = now
	return s.values, s.err
}
