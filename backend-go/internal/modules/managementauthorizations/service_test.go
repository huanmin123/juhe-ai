package managementauthorizations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceCreateNormalizesQuotaAndInvalidatesAuthorizationCache(t *testing.T) {
	now := time.Date(2026, 7, 9, 8, 30, 0, 0, time.UTC)
	store := &authorizationCreateStoreStub{
		result: Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "account",
			ResourceID:                   "acct_main",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "system_account",
			GranteeSystemAccountID:       "sys_grantee",
			Scope:                        "use",
			Status:                       "active",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_admin",
			CreatedAt:                    now,
			UpdatedAt:                    now,
		},
	}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		Now:                      func() time.Time { return now },
		Secret:                   "test-secret",
		AuthorizationInvalidator: invalidator,
	})

	got, err := service.Create(context.Background(), CreateInput{
		ResourceType:                 "account",
		ResourceID:                   " acct_main ",
		ResourceOwnerSystemAccountID: " sys_owner ",
		GranteeType:                  "system_account",
		GranteeID:                    " sys_grantee ",
		TargetGroupID:                " grp_target ",
		Remark:                       "  项目授权  ",
		HasRemark:                    true,
		ExpiresAt:                    "2026-07-10T00:00:00.000Z",
		HasExpiresAt:                 true,
		Limits: map[string]any{
			"hourly": map[string]any{"enabled": true, "hours": float64(6), "limit": float64(1.5)},
			"daily":  map[string]any{"enabled": true, "limit": float64(10)},
		},
		HasLimits:            true,
		ActorSystemAccountID: " sys_admin ",
	})

	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got.ID != "rauthgrant_main" {
		t.Fatalf("summary = %+v", got)
	}
	if !store.called {
		t.Fatal("store was not called")
	}
	if store.input.ResourceID != "acct_main" ||
		store.input.ResourceOwnerSystemAccountID != "sys_owner" ||
		store.input.GranteeID != "sys_grantee" ||
		store.input.TargetGroupID != "grp_target" ||
		store.input.Remark != "项目授权" ||
		!store.input.HasRemark ||
		store.input.ActorSystemAccountID != "sys_admin" ||
		!store.input.CreatedAt.Equal(now) {
		t.Fatalf("store input = %+v", store.input)
	}
	if store.input.ExpiresAt == nil || !store.input.ExpiresAt.Equal(time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiresAt = %+v", store.input.ExpiresAt)
	}
	if store.input.Limits.Hourly == nil ||
		store.input.Limits.Hourly.Hours != 6 ||
		store.input.Limits.Hourly.Limit != 1.5 ||
		store.input.Limits.Daily == nil ||
		store.input.Limits.Daily.Limit != 10 {
		t.Fatalf("limits = %+v", store.input.Limits)
	}
	wantJSON := `{"hourly":{"enabled":true,"hours":6,"limit":1.5},"daily":{"enabled":true,"limit":10}}`
	if store.input.LimitsJSON == nil || *store.input.LimitsJSON != wantJSON {
		t.Fatalf("limits json = %+v, want %s", store.input.LimitsJSON, wantJSON)
	}
	if store.input.LimitHourlyWindowHours != 6 {
		t.Fatalf("hourly window = %d, want 6", store.input.LimitHourlyWindowHours)
	}
	if !strings.HasPrefix(store.input.AuthorizationInstanceSecretJSON, "v1:") {
		t.Fatalf("authorization instance credential = %q, want encrypted v1 payload", store.input.AuthorizationInstanceSecretJSON)
	}
	if invalidator.calls != 1 || invalidator.reason != ResourceAuthorizationCreatedReason {
		t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestServiceCreateValidatesAuthorizationShape(t *testing.T) {
	now := time.Date(2026, 7, 9, 8, 30, 0, 0, time.UTC)
	service := NewServiceWithOptions(ServiceOptions{
		Store: &authorizationCreateStoreStub{},
		Now:   func() time.Time { return now },
	})
	tests := []struct {
		name  string
		input CreateInput
		want  string
	}{
		{
			name: "account personal grant requires target group",
			input: CreateInput{
				ResourceType:                 "account",
				ResourceID:                   "acct_main",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "system_account",
				GranteeID:                    "sys_grantee",
				ActorSystemAccountID:         "sys_admin",
			},
			want: "授权 AI 账户给个人时必须选择目标分组",
		},
		{
			name: "target group only allowed for account personal grant",
			input: CreateInput{
				ResourceType:                 "group",
				ResourceID:                   "grp_owner",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "team",
				GranteeID:                    "team_ops",
				TargetGroupID:                "grp_target",
				ActorSystemAccountID:         "sys_admin",
			},
			want: "只有授权 AI 账户给个人时可以指定目标分组",
		},
		{
			name: "expired at must be future",
			input: CreateInput{
				ResourceType:                 "group",
				ResourceID:                   "grp_owner",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "team",
				GranteeID:                    "team_ops",
				ExpiresAt:                    "2026-07-08T00:00:00.000Z",
				HasExpiresAt:                 true,
				ActorSystemAccountID:         "sys_admin",
			},
			want: "授权到期时间不能早于当前时间",
		},
		{
			name: "hourly quota requires enabled true",
			input: CreateInput{
				ResourceType:                 "group",
				ResourceID:                   "grp_owner",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "team",
				GranteeID:                    "team_ops",
				Limits:                       map[string]any{"hourly": map[string]any{"enabled": false, "hours": float64(1), "limit": float64(1)}},
				HasLimits:                    true,
				ActorSystemAccountID:         "sys_admin",
			},
			want: "小时额度启用状态必须为 true",
		},
		{
			name: "quota limit precision",
			input: CreateInput{
				ResourceType:                 "group",
				ResourceID:                   "grp_owner",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "team",
				GranteeID:                    "team_ops",
				Limits:                       map[string]any{"daily": map[string]any{"enabled": true, "limit": float64(1.1234567)}},
				HasLimits:                    true,
				ActorSystemAccountID:         "sys_admin",
			},
			want: "日额度金额最多支持 6 位小数",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.Create(context.Background(), tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Create() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestServiceCreateDoesNotInvalidateWhenStoreFails(t *testing.T) {
	now := time.Date(2026, 7, 9, 8, 30, 0, 0, time.UTC)
	storeErr := errors.New("store failed")
	store := &authorizationCreateStoreStub{err: storeErr}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                    store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})

	_, err := service.Create(context.Background(), CreateInput{
		ResourceType:                 "group",
		ResourceID:                   "grp_owner",
		ResourceOwnerSystemAccountID: "sys_owner",
		GranteeType:                  "team",
		GranteeID:                    "team_ops",
		ActorSystemAccountID:         "sys_admin",
	})

	if !errors.Is(err, storeErr) {
		t.Fatalf("Create() error = %v, want store error", err)
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidator calls = %d, want 0", invalidator.calls)
	}
}

func TestServiceUpdateNormalizesInputAndInvalidatesAuthorizationCache(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	store := &authorizationUpdateStoreStub{
		found: true,
		result: Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "account",
			ResourceID:                   "acct_main",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "system_account",
			GranteeSystemAccountID:       "sys_grantee",
			Scope:                        "use",
			Status:                       "paused",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_owner",
			CreatedAt:                    now,
			UpdatedAt:                    now,
		},
	}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		UpdateStore:              store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})
	expiresAt := "2026-07-10T00:00:00.000Z"

	got, found, err := service.Update(context.Background(), UpdateInput{
		AuthorizationID:       " rauthgrant_main ",
		ActorSystemAccountID:  " sys_admin ",
		ActorRole:             "admin",
		ScopedSystemAccountID: " sys_owner ",
		HasStatus:             true,
		Status:                " paused ",
		HasExpiresAt:          true,
		ExpiresAt:             &expiresAt,
		HasLimits:             true,
		Limits: map[string]any{
			"hourly": map[string]any{"enabled": true, "hours": float64(12), "limit": float64(3.25)},
			"total":  map[string]any{"enabled": true, "limit": float64(99)},
		},
	})

	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !found || got.ID != "rauthgrant_main" {
		t.Fatalf("Update() = (%+v, %v), want updated summary", got, found)
	}
	if !store.called ||
		store.input.AuthorizationID != "rauthgrant_main" ||
		store.input.ActorSystemAccountID != "sys_admin" ||
		!store.input.CanAccessAll ||
		store.input.ScopedSystemAccountID != "sys_owner" ||
		!store.input.HasStatus ||
		store.input.Status != "paused" ||
		!store.input.HasExpiresAt ||
		!store.input.HasLimits ||
		!store.input.UpdatedAt.Equal(now) {
		t.Fatalf("store input = %+v", store.input)
	}
	if store.input.ExpiresAt == nil || !store.input.ExpiresAt.Equal(time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiresAt = %+v", store.input.ExpiresAt)
	}
	wantJSON := `{"hourly":{"enabled":true,"hours":12,"limit":3.25},"total":{"enabled":true,"limit":99}}`
	if store.input.LimitsJSON == nil || *store.input.LimitsJSON != wantJSON {
		t.Fatalf("limits json = %+v, want %s", store.input.LimitsJSON, wantJSON)
	}
	if store.input.LimitHourlyWindowHours != 12 {
		t.Fatalf("hourly window = %d, want 12", store.input.LimitHourlyWindowHours)
	}
	if invalidator.calls != 1 || invalidator.reason != ResourceAuthorizationUpdatedReason {
		t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestServiceUpdateValidatesInputAndScopesNonAdmin(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	store := &authorizationUpdateStoreStub{
		found: true,
		result: Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "group",
			ResourceID:                   "grp_owner",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "team",
			GranteeTeamID:                "team_ops",
			Scope:                        "use",
			Status:                       "active",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedAt:                    now,
			UpdatedAt:                    now,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		UpdateStore: store,
		Now:         func() time.Time { return now },
	})

	if _, _, err := service.Update(context.Background(), UpdateInput{
		AuthorizationID:      "rauthgrant_main",
		ActorSystemAccountID: "sys_owner",
	}); !errors.Is(err, ErrAuthorizationUpdateInvalid) {
		t.Fatalf("Update() error = %v, want invalid input", err)
	}
	if _, _, err := service.Update(context.Background(), UpdateInput{
		AuthorizationID:      "rauthgrant_main",
		ActorSystemAccountID: "sys_owner",
		HasStatus:            true,
		Status:               "revoked",
	}); !errors.Is(err, ErrAuthorizationUpdateInvalid) {
		t.Fatalf("Update() bad status error = %v, want invalid input", err)
	}
	pastExpiresAt := "2026-07-08T00:00:00.000Z"
	if _, _, err := service.Update(context.Background(), UpdateInput{
		AuthorizationID:      "rauthgrant_main",
		ActorSystemAccountID: "sys_owner",
		HasExpiresAt:         true,
		ExpiresAt:            &pastExpiresAt,
	}); err == nil || !strings.Contains(err.Error(), "授权到期时间不能早于当前时间") {
		t.Fatalf("Update() past expiresAt error = %v", err)
	}

	got, found, err := service.Update(context.Background(), UpdateInput{
		AuthorizationID:       "rauthgrant_main",
		ActorSystemAccountID:  " sys_owner ",
		ActorRole:             "user",
		ScopedSystemAccountID: "sys_other",
		HasLimits:             true,
		LimitsIsNull:          true,
	})
	if err != nil {
		t.Fatalf("Update() clear limits error = %v", err)
	}
	if !found || got.ID != "rauthgrant_main" {
		t.Fatalf("Update() clear limits = (%+v, %v), want summary", got, found)
	}
	if !store.called ||
		store.input.ScopedSystemAccountID != "sys_owner" ||
		store.input.CanAccessAll ||
		!store.input.HasLimits ||
		store.input.LimitsJSON != nil {
		t.Fatalf("store input = %+v", store.input)
	}
}

func TestServiceUpdateSkipsInvalidationWhenNotFound(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	store := &authorizationUpdateStoreStub{}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		UpdateStore:              store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})

	_, found, err := service.Update(context.Background(), UpdateInput{
		AuthorizationID:      "rauthgrant_missing",
		ActorSystemAccountID: "sys_owner",
		ActorRole:            "admin",
		HasStatus:            true,
		Status:               "paused",
	})
	if err != nil {
		t.Fatalf("Update() missing error = %v", err)
	}
	if found {
		t.Fatal("Update() found missing authorization")
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidator calls = %d, want 0", invalidator.calls)
	}
}

func TestServiceReturnTrimsInputAndInvalidatesAuthorizationCache(t *testing.T) {
	now := time.Date(2026, 7, 9, 9, 30, 0, 0, time.UTC)
	store := &authorizationReturnStoreStub{
		found: true,
		result: Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "account",
			ResourceID:                   "acct_main",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "system_account",
			GranteeSystemAccountID:       "sys_grantee",
			Scope:                        "use",
			Status:                       "returned",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_owner",
			CreatedAt:                    now,
			UpdatedAt:                    now,
		},
	}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		ReturnStore:              store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})

	got, found, err := service.Return(context.Background(), ReturnInput{
		AuthorizationID:        " rauthgrant_main ",
		GranteeSystemAccountID: " sys_grantee ",
		ActorSystemAccountID:   " sys_admin ",
	})

	if err != nil {
		t.Fatalf("Return() error = %v", err)
	}
	if !found || got.ID != "rauthgrant_main" {
		t.Fatalf("Return() = (%+v, %v), want returned summary", got, found)
	}
	if !store.called ||
		store.input.AuthorizationID != "rauthgrant_main" ||
		store.input.GranteeSystemAccountID != "sys_grantee" ||
		store.input.ActorSystemAccountID != "sys_admin" ||
		!store.input.ReturnedAt.Equal(now) {
		t.Fatalf("store input = %+v", store.input)
	}
	if invalidator.calls != 1 || invalidator.reason != ResourceAuthorizationReturnedReason {
		t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestServiceReturnValidatesInputAndSkipsInvalidationWhenNotFound(t *testing.T) {
	now := time.Date(2026, 7, 9, 9, 30, 0, 0, time.UTC)
	store := &authorizationReturnStoreStub{}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		ReturnStore:              store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})

	if _, _, err := service.Return(context.Background(), ReturnInput{
		GranteeSystemAccountID: "sys_grantee",
		ActorSystemAccountID:   "sys_admin",
	}); !errors.Is(err, ErrAuthorizationReturnInvalid) {
		t.Fatalf("Return() error = %v, want invalid input", err)
	}
	if store.called {
		t.Fatal("store was called for invalid return input")
	}

	_, found, err := service.Return(context.Background(), ReturnInput{
		AuthorizationID:        "rauthgrant_missing",
		GranteeSystemAccountID: "sys_grantee",
		ActorSystemAccountID:   "sys_admin",
	})
	if err != nil {
		t.Fatalf("Return() missing error = %v", err)
	}
	if found {
		t.Fatal("Return() found missing authorization")
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidator calls = %d, want 0", invalidator.calls)
	}
}

func TestServiceRevokeNormalizesScopeAndInvalidatesAuthorizationCache(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC)
	store := &authorizationRevokeStoreStub{
		found: true,
		result: Summary{
			ID:                           "rauthgrant_main",
			ResourceType:                 "account",
			ResourceID:                   "acct_main",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "system_account",
			GranteeSystemAccountID:       "sys_grantee",
			Scope:                        "use",
			Status:                       "revoked",
			AuthorizationSources:         []port.ManagementResourceAuthorizationSourceSummary{},
			Usage:                        port.ManagementAccountUsageSummary{},
			CreatedBy:                    "sys_owner",
			CreatedAt:                    now,
			UpdatedAt:                    now,
		},
	}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		RevokeStore:              store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})

	got, found, err := service.Revoke(context.Background(), RevokeInput{
		AuthorizationID:       " rauthgrant_main ",
		ActorSystemAccountID:  " sys_owner ",
		ActorRole:             "user",
		ScopedSystemAccountID: "sys_other",
	})

	if err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if !found || got.ID != "rauthgrant_main" {
		t.Fatalf("Revoke() = (%+v, %v), want revoked summary", got, found)
	}
	if !store.called ||
		store.input.AuthorizationID != "rauthgrant_main" ||
		store.input.ActorSystemAccountID != "sys_owner" ||
		store.input.ScopedSystemAccountID != "sys_owner" ||
		store.input.CanAccessAll ||
		!store.input.RevokedAt.Equal(now) {
		t.Fatalf("store input = %+v", store.input)
	}
	if invalidator.calls != 1 || invalidator.reason != ResourceAuthorizationRevokedReason {
		t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestServiceRevokeValidatesInputAndSkipsInvalidationWhenNotFound(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 30, 0, 0, time.UTC)
	store := &authorizationRevokeStoreStub{}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		RevokeStore:              store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})

	if _, _, err := service.Revoke(context.Background(), RevokeInput{
		ActorSystemAccountID: "sys_owner",
	}); !errors.Is(err, ErrAuthorizationRevokeInvalid) {
		t.Fatalf("Revoke() error = %v, want invalid input", err)
	}
	if store.called {
		t.Fatal("store was called for invalid revoke input")
	}

	_, found, err := service.Revoke(context.Background(), RevokeInput{
		AuthorizationID:      "rauthgrant_missing",
		ActorSystemAccountID: "sys_owner",
		ActorRole:            "admin",
	})
	if err != nil {
		t.Fatalf("Revoke() missing error = %v", err)
	}
	if found {
		t.Fatal("Revoke() found missing authorization")
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidator calls = %d, want 0", invalidator.calls)
	}
}

func TestServiceListNormalizesScopeAndRedactsNonOwnerSourceDetails(t *testing.T) {
	createdAt := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	store := &authorizationListStoreStub{
		result: port.ManagementResourceAuthorizationListResult{
			Items: []port.ManagementResourceAuthorizationSummary{{
				ID:                           "rauthgrant_team",
				ResourceType:                 "account",
				ResourceID:                   "acct_main",
				ResourceName:                 "主账号",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "team",
				GranteeTeamID:                "team_ops",
				GranteeTeamName:              "运维团队",
				Scope:                        "use",
				Status:                       "active",
				EffectiveSourceType:          "team",
				EffectiveSourceTeamID:        "team_ops",
				EffectiveSourceTeamName:      "运维团队",
				AuthorizationSources: []port.ManagementResourceAuthorizationSourceSummary{{
					ID:             "rauthgrant_team",
					SourceType:     "team",
					SourceTeamID:   "team_ops",
					SourceTeamName: "运维团队",
					Status:         "active",
					CreatedAt:      createdAt,
					UpdatedAt:      createdAt,
				}},
				CreatedBy: "sys_owner",
				CreatedAt: createdAt,
				UpdatedAt: createdAt,
			}},
			HasMore: true,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{ListStore: store})

	got, err := service.List(context.Background(), ListInput{
		ActorSystemAccountID:  " sys_grantee ",
		ActorRole:             "user",
		ScopedSystemAccountID: "sys_other",
		ResourceType:          " account ",
		Status:                "active",
		Direction:             "inbound",
		SourceType:            "team",
		Keyword:               "  主  ",
		Page:                  2,
		PageSize:              1,
	})

	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !store.called ||
		store.input.ActorSystemAccountID != "sys_grantee" ||
		store.input.ScopedSystemAccountID != "sys_grantee" ||
		store.input.CanAccessAll ||
		store.input.ResourceType != "account" ||
		store.input.Status != "active" ||
		store.input.Direction != "inbound" ||
		store.input.SourceType != "team" ||
		store.input.Keyword != "主" ||
		store.input.Limit != 2 ||
		store.input.Offset != 1 {
		t.Fatalf("store input = %+v", store.input)
	}
	if got.Page != 2 || got.PageSize != 1 || got.Total != 3 || !got.HasMore || len(got.Items) != 1 {
		t.Fatalf("list result = %+v", got)
	}
	item := got.Items[0]
	if item.Permissions.CanEdit || item.Permissions.CanAuthorize ||
		item.EffectiveSourceTeamID != "" ||
		item.EffectiveSourceTeamName != "" ||
		item.CreatedBy != "" {
		t.Fatalf("non-owner item was not redacted: %+v", item)
	}
	if item.SourceSummary.ActiveSourceCount != 1 ||
		!item.SourceSummary.HasTeam ||
		len(item.SourceSummary.TeamSources) != 0 {
		t.Fatalf("source summary = %+v", item.SourceSummary)
	}
}

func TestServiceListValidatesInput(t *testing.T) {
	service := NewServiceWithOptions(ServiceOptions{ListStore: &authorizationListStoreStub{}})
	longKeyword := strings.Repeat("字", 121)
	for _, input := range []ListInput{
		{ActorSystemAccountID: "", ResourceType: "account"},
		{ActorSystemAccountID: "sys_actor", ResourceType: "invalid"},
		{ActorSystemAccountID: "sys_actor", Status: "deleted"},
		{ActorSystemAccountID: "sys_actor", Direction: "sideways"},
		{ActorSystemAccountID: "sys_actor", SourceType: "api"},
		{ActorSystemAccountID: "sys_actor", Keyword: longKeyword},
	} {
		if _, err := service.List(context.Background(), input); !errors.Is(err, ErrAuthorizationListInvalid) {
			t.Fatalf("List(%+v) error = %v, want invalid input", input, err)
		}
	}
}

func TestServiceGetNormalizesScopeAndRedactsNonOwnerDetail(t *testing.T) {
	createdAt := time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC)
	endedAt := createdAt.Add(time.Hour)
	store := &authorizationGetStoreStub{
		result: port.ManagementResourceAuthorizationSummary{
			ID:                           "rauthgrant_team",
			ResourceType:                 "account",
			ResourceID:                   "acct_main",
			ResourceName:                 "主账号",
			ResourceOwnerSystemAccountID: "sys_owner",
			GranteeType:                  "team",
			GranteeTeamID:                "team_ops",
			GranteeTeamName:              "运维团队",
			Scope:                        "use",
			Status:                       "active",
			EffectiveSourceType:          "team",
			EffectiveSourceTeamID:        "team_ops",
			EffectiveSourceTeamName:      "运维团队",
			AuthorizationSources: []port.ManagementResourceAuthorizationSourceSummary{{
				ID:              "ras_team",
				AuthorizationID: "rauth_runtime",
				SourceType:      "team",
				SourceTeamID:    "team_ops",
				SourceTeamName:  "运维团队",
				Status:          "active",
				ActivatedAt:     &createdAt,
				EndedAt:         &endedAt,
				EndedReason:     "team_disabled",
				CreatedBy:       "sys_owner",
				CreatedAt:       createdAt,
				RevokedBy:       "sys_admin",
				RevokedAt:       &endedAt,
				UpdatedAt:       endedAt,
			}},
			CreatedBy: "sys_owner",
			CreatedAt: createdAt,
			RevokedBy: "sys_admin",
			UpdatedAt: createdAt,
		},
		found: true,
	}
	service := NewServiceWithOptions(ServiceOptions{GetStore: store})

	got, found, err := service.Get(context.Background(), GetInput{
		AuthorizationID:       " rauthgrant_team ",
		ActorSystemAccountID:  " sys_viewer ",
		ActorRole:             "user",
		ScopedSystemAccountID: "sys_other",
	})

	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !found {
		t.Fatal("Get() found = false, want true")
	}
	if !store.called ||
		store.input.AuthorizationID != "rauthgrant_team" ||
		store.input.ActorSystemAccountID != "sys_viewer" ||
		store.input.ScopedSystemAccountID != "sys_viewer" ||
		store.input.CanAccessAll {
		t.Fatalf("store input = %+v", store.input)
	}
	if got.Permissions.CanEdit || got.Permissions.CanAuthorize ||
		got.EffectiveSourceTeamID != "" ||
		got.EffectiveSourceTeamName != "" ||
		got.CreatedBy != "" ||
		got.RevokedBy != "" {
		t.Fatalf("non-owner detail was not redacted: %+v", got)
	}
	if len(got.AuthorizationSources) != 1 {
		t.Fatalf("authorization sources = %+v", got.AuthorizationSources)
	}
	source := got.AuthorizationSources[0]
	if source.SourceTeamID != "" ||
		source.CreatedBy != "" ||
		source.RevokedBy != "" ||
		source.EndedAt != nil ||
		source.RevokedAt != nil ||
		source.SourceTeamName != "运维团队" ||
		source.EndedReason != "team_disabled" {
		t.Fatalf("sanitized source = %+v", source)
	}
}

func TestServiceGetValidatesInput(t *testing.T) {
	service := NewServiceWithOptions(ServiceOptions{GetStore: &authorizationGetStoreStub{}})
	for _, input := range []GetInput{
		{AuthorizationID: "", ActorSystemAccountID: "sys_actor"},
		{AuthorizationID: "rauthgrant_main", ActorSystemAccountID: ""},
	} {
		if _, _, err := service.Get(context.Background(), input); !errors.Is(err, ErrAuthorizationListInvalid) {
			t.Fatalf("Get(%+v) error = %v, want invalid input", input, err)
		}
	}
}

type authorizationCreateStoreStub struct {
	called bool
	input  port.ManagementResourceAuthorizationCreateInput
	result Summary
	err    error
}

func (s *authorizationCreateStoreStub) CreateManagementResourceAuthorization(_ context.Context, input port.ManagementResourceAuthorizationCreateInput) (port.ManagementResourceAuthorizationSummary, error) {
	s.called = true
	s.input = input
	if s.err != nil {
		return port.ManagementResourceAuthorizationSummary{}, s.err
	}
	return s.result, nil
}

type authorizationReturnStoreStub struct {
	called bool
	input  port.ManagementResourceAuthorizationReturnInput
	result Summary
	found  bool
	err    error
}

func (s *authorizationReturnStoreStub) ReturnManagementResourceAuthorizationForGrantee(_ context.Context, input port.ManagementResourceAuthorizationReturnInput) (port.ManagementResourceAuthorizationSummary, bool, error) {
	s.called = true
	s.input = input
	if s.err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, s.err
	}
	return s.result, s.found, nil
}

type authorizationUpdateStoreStub struct {
	called bool
	input  port.ManagementResourceAuthorizationUpdateInput
	result Summary
	found  bool
	err    error
}

func (s *authorizationUpdateStoreStub) UpdateManagementResourceAuthorization(_ context.Context, input port.ManagementResourceAuthorizationUpdateInput) (port.ManagementResourceAuthorizationSummary, bool, error) {
	s.called = true
	s.input = input
	if s.err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, s.err
	}
	return s.result, s.found, nil
}

type authorizationRevokeStoreStub struct {
	called bool
	input  port.ManagementResourceAuthorizationRevokeInput
	result Summary
	found  bool
	err    error
}

func (s *authorizationRevokeStoreStub) RevokeManagementResourceAuthorization(_ context.Context, input port.ManagementResourceAuthorizationRevokeInput) (port.ManagementResourceAuthorizationSummary, bool, error) {
	s.called = true
	s.input = input
	if s.err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, s.err
	}
	return s.result, s.found, nil
}

type authorizationListStoreStub struct {
	called bool
	input  port.ManagementResourceAuthorizationListInput
	result port.ManagementResourceAuthorizationListResult
	err    error
}

func (s *authorizationListStoreStub) ListManagementResourceAuthorizations(_ context.Context, input port.ManagementResourceAuthorizationListInput) (port.ManagementResourceAuthorizationListResult, error) {
	s.called = true
	s.input = input
	if s.err != nil {
		return port.ManagementResourceAuthorizationListResult{}, s.err
	}
	return s.result, nil
}

type authorizationGetStoreStub struct {
	called bool
	input  port.ManagementResourceAuthorizationGetInput
	result port.ManagementResourceAuthorizationSummary
	found  bool
	err    error
}

func (s *authorizationGetStoreStub) FindManagementResourceAuthorization(_ context.Context, input port.ManagementResourceAuthorizationGetInput) (port.ManagementResourceAuthorizationSummary, bool, error) {
	s.called = true
	s.input = input
	if s.err != nil {
		return port.ManagementResourceAuthorizationSummary{}, false, s.err
	}
	return s.result, s.found, nil
}

type authorizationInvalidatorStub struct {
	calls  int
	reason string
	err    error
}

func (s *authorizationInvalidatorStub) InvalidateAuthorizationChanged(_ context.Context, reason string) error {
	s.calls++
	s.reason = reason
	return s.err
}

var _ port.ManagementResourceAuthorizationCreator = (*authorizationCreateStoreStub)(nil)
var _ port.ManagementResourceAuthorizationGetter = (*authorizationGetStoreStub)(nil)
var _ port.ManagementResourceAuthorizationLister = (*authorizationListStoreStub)(nil)
var _ port.ManagementResourceAuthorizationRevoker = (*authorizationRevokeStoreStub)(nil)
var _ port.ManagementResourceAuthorizationReturner = (*authorizationReturnStoreStub)(nil)
var _ port.ManagementResourceAuthorizationUpdater = (*authorizationUpdateStoreStub)(nil)
var _ AuthorizationInvalidator = (*authorizationInvalidatorStub)(nil)
