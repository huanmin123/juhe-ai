package gatewayquotasnapshot

import (
	"context"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceBuildMatchesNodeSnapshotScopes(t *testing.T) {
	store := &snapshotStoreStub{
		apiKeys: port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAPIKeyRow]{
			Rows: []port.GatewayQuotaSnapshotAPIKeyRow{{
				ID:              "key_1",
				SystemAccountID: "sys_admin",
				Limits: port.ManagementRequestQuotaLimits{
					Hourly: &port.ManagementRequestHourlyQuotaLimit{Enabled: true, Hours: 6, Limit: 100},
					Daily:  &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 1000},
				},
			}},
			Complete: true,
		},
		authorizations: port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAuthorizationRow]{
			Rows: []port.GatewayQuotaSnapshotAuthorizationRow{{
				ID:                           "auth_account",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeSystemAccountID:       "sys_grantee",
				ResourceType:                 "account",
				ResourceID:                   "acct_source",
				EffectiveSourceTeamID:        "team_ops",
				Limits: port.ManagementRequestQuotaLimits{
					Total: &port.ManagementRequestQuotaLimit{Enabled: true, Limit: 100},
				},
			}},
			Complete: true,
		},
		teamAuthorizations: port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotTeamAuthorizationRow]{
			Rows: []port.GatewayQuotaSnapshotTeamAuthorizationRow{{
				AuthorizationID:                     "auth_account",
				ResourceOwnerSystemAccountID:        "sys_owner",
				AuthorizationGranteeSystemAccountID: "sys_grantee",
				ResourceType:                        "account",
				ResourceID:                          "acct_source",
				AuthorizationInstanceAccountID:      "acct_instance",
				EffectiveSourceTeamID:               "team_ops",
				Limits: port.ManagementRequestQuotaLimits{
					Hourly: &port.ManagementRequestHourlyQuotaLimit{Enabled: true, Hours: 6, Limit: 10},
				},
			}},
			Complete: true,
		},
		costs: map[string]port.GatewayQuotaCosts{
			"sys_admin\x00api_key\x00key_1\x002026-07-09\x002026-07-06\x002026-07\x006": {
				Hourly: 3, Daily: 5, Weekly: 7, Monthly: 9, Total: 11,
			},
			"sys_grantee\x00account_authorization\x00auth_account\x002026-07-09\x002026-07-06\x002026-07\x00": {
				Total: 3,
			},
			"sys_grantee\x00account_authorization_team\x00acct_instance:team_ops\x002026-07-09\x002026-07-06\x002026-07\x006": {
				Hourly: 10,
			},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now: func() time.Time {
			return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
		},
	})

	snapshot, err := service.Build(context.Background(), BuildInput{Timezone: "UTC"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if snapshot.GeneratedAt != "2026-07-09T12:00:00.000Z" ||
		snapshot.StatDate != "2026-07-09" ||
		snapshot.StatWeek != "2026-07-06" ||
		snapshot.StatMonth != "2026-07" {
		t.Fatalf("snapshot dates = generated %q date/week/month %q/%q/%q", snapshot.GeneratedAt, snapshot.StatDate, snapshot.StatWeek, snapshot.StatMonth)
	}
	if len(snapshot.CostEntries) != 1 {
		t.Fatalf("cost entries = %+v", snapshot.CostEntries)
	}
	costEntry := snapshot.CostEntries[0]
	if costEntry.SystemAccountID != "sys_admin" ||
		costEntry.ScopeType != "api_key" ||
		costEntry.ScopeID != "key_1" ||
		costEntry.HourlyWindowHours != 6 ||
		costEntry.Costs.Total != 11 {
		t.Fatalf("cost entry = %+v", costEntry)
	}
	if len(snapshot.AuthorizationEntries) != 1 {
		t.Fatalf("authorization entries = %+v", snapshot.AuthorizationEntries)
	}
	authorizationEntry := snapshot.AuthorizationEntries[0]
	if authorizationEntry.ScopeType != "account_authorization" ||
		authorizationEntry.AuthorizationID != "auth_account" ||
		authorizationEntry.Decision.Allowed ||
		authorizationEntry.Decision.Message == "" {
		t.Fatalf("authorization entry = %+v", authorizationEntry)
	}
	wantInputs := []port.GatewayQuotaCostLookupInput{
		{
			Key:               "sys_admin\x00api_key\x00key_1\x002026-07-09\x002026-07-06\x002026-07\x006",
			SystemAccountID:   "sys_admin",
			ScopeType:         "api_key",
			ScopeID:           "key_1",
			StatDate:          "2026-07-09",
			StatWeek:          "2026-07-06",
			StatMonth:         "2026-07",
			HourlyWindowHours: 6,
		},
		{
			Key:               "sys_grantee\x00account_authorization\x00auth_account\x002026-07-09\x002026-07-06\x002026-07\x00",
			SystemAccountID:   "sys_grantee",
			ScopeType:         "account_authorization",
			ScopeID:           "auth_account",
			StatDate:          "2026-07-09",
			StatWeek:          "2026-07-06",
			StatMonth:         "2026-07",
			HourlyWindowHours: 0,
		},
		{
			Key:               "sys_grantee\x00account_authorization_team\x00acct_instance:team_ops\x002026-07-09\x002026-07-06\x002026-07\x006",
			SystemAccountID:   "sys_grantee",
			ScopeType:         "account_authorization_team",
			ScopeID:           "acct_instance:team_ops",
			StatDate:          "2026-07-09",
			StatWeek:          "2026-07-06",
			StatMonth:         "2026-07",
			HourlyWindowHours: 6,
		},
	}
	if !reflect.DeepEqual(store.costInputs, wantInputs) {
		t.Fatalf("cost inputs = %#v, want %#v", store.costInputs, wantInputs)
	}
}

func TestServiceBuildSkipsTeamAccountQuotaWithoutInstanceAccount(t *testing.T) {
	store := &snapshotStoreStub{
		apiKeys: port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAPIKeyRow]{Complete: true},
		authorizations: port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAuthorizationRow]{
			Rows: []port.GatewayQuotaSnapshotAuthorizationRow{{
				ID:                           "auth_account",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeSystemAccountID:       "sys_grantee",
				ResourceType:                 "account",
				ResourceID:                   "acct_source",
				EffectiveSourceTeamID:        "team_ops",
			}},
			Complete: true,
		},
		teamAuthorizations: port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotTeamAuthorizationRow]{
			Rows: []port.GatewayQuotaSnapshotTeamAuthorizationRow{{
				AuthorizationID:              "auth_account",
				ResourceOwnerSystemAccountID: "sys_owner",
				ResourceType:                 "account",
				ResourceID:                   "acct_source",
				EffectiveSourceTeamID:        "team_ops",
				Limits: port.ManagementRequestQuotaLimits{
					Hourly: &port.ManagementRequestHourlyQuotaLimit{Enabled: true, Hours: 6, Limit: 10},
				},
			}},
			Complete: false,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store,
		Now:   func() time.Time { return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC) },
	})
	snapshot, err := service.Build(context.Background(), BuildInput{Timezone: "UTC"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(snapshot.AuthorizationEntries) != 0 {
		t.Fatalf("authorization entries = %+v, want none", snapshot.AuthorizationEntries)
	}
	if snapshot.AuthorizationEntriesComplete {
		t.Fatal("authorization entries complete = true, want false from bounded team window")
	}
	if len(store.costInputs) != 0 {
		t.Fatalf("cost inputs = %+v, want none", store.costInputs)
	}
}

type snapshotStoreStub struct {
	apiKeys            port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAPIKeyRow]
	authorizations     port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAuthorizationRow]
	teamAuthorizations port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotTeamAuthorizationRow]
	costs              map[string]port.GatewayQuotaCosts
	costInputs         []port.GatewayQuotaCostLookupInput
}

func (s *snapshotStoreStub) ListGatewayQuotaSnapshotAPIKeys(context.Context, int) (port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAPIKeyRow], error) {
	return s.apiKeys, nil
}

func (s *snapshotStoreStub) ListGatewayQuotaSnapshotAuthorizations(context.Context, int) (port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotAuthorizationRow], error) {
	return s.authorizations, nil
}

func (s *snapshotStoreStub) ListGatewayQuotaSnapshotTeamAuthorizations(context.Context, int) (port.GatewayQuotaSnapshotRows[port.GatewayQuotaSnapshotTeamAuthorizationRow], error) {
	return s.teamAuthorizations, nil
}

func (s *snapshotStoreStub) LoadGatewayQuotaSnapshotCosts(_ context.Context, inputs []port.GatewayQuotaCostLookupInput) (map[string]port.GatewayQuotaCosts, error) {
	s.costInputs = append([]port.GatewayQuotaCostLookupInput(nil), inputs...)
	out := map[string]port.GatewayQuotaCosts{}
	for _, input := range inputs {
		out[input.Key] = s.costs[input.Key]
	}
	return out, nil
}

var _ port.GatewayQuotaSnapshotReader = (*snapshotStoreStub)(nil)
