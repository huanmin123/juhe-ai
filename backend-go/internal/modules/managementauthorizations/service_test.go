package managementauthorizations

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
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

func TestServiceReturnByResourceNormalizesInputAndInvalidatesAuthorizationCache(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	for _, tt := range []struct {
		name         string
		resourceType string
		resourceID   string
	}{
		{name: "account", resourceType: "account", resourceID: "acct_authorized"},
		{name: "group", resourceType: "group", resourceID: "grp_authorized"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &authorizationResourceReturnStoreStub{
				found: true,
				result: Summary{
					ID:                           "rauthgrant_main",
					ResourceType:                 tt.resourceType,
					ResourceID:                   tt.resourceID,
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
				ResourceReturnStore:      store,
				Now:                      func() time.Time { return now },
				AuthorizationInvalidator: invalidator,
			})

			got, found, err := service.ReturnByResource(context.Background(), ResourceReturnInput{
				ResourceType:           " " + tt.resourceType + " ",
				ResourceID:             " " + tt.resourceID + " ",
				GranteeSystemAccountID: " sys_grantee ",
				ActorSystemAccountID:   " sys_admin ",
			})

			if err != nil {
				t.Fatalf("ReturnByResource() error = %v", err)
			}
			if !found || got.ID != "rauthgrant_main" {
				t.Fatalf("ReturnByResource() = (%+v, %v), want returned summary", got, found)
			}
			if !store.called ||
				store.input.ResourceType != tt.resourceType ||
				store.input.ResourceID != tt.resourceID ||
				store.input.GranteeSystemAccountID != "sys_grantee" ||
				store.input.ActorSystemAccountID != "sys_admin" ||
				!store.input.ReturnedAt.Equal(now) {
				t.Fatalf("store input = %+v", store.input)
			}
			if invalidator.calls != 1 || invalidator.reason != ResourceAuthorizationReturnedReason {
				t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
			}
		})
	}
}

func TestServiceReturnByResourceValidatesInputAndSkipsInvalidationWhenNotFound(t *testing.T) {
	now := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	store := &authorizationResourceReturnStoreStub{}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		ResourceReturnStore:      store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})

	if _, _, err := service.ReturnByResource(context.Background(), ResourceReturnInput{
		ResourceType:           "account",
		GranteeSystemAccountID: "sys_grantee",
		ActorSystemAccountID:   "sys_admin",
	}); !errors.Is(err, ErrAuthorizationReturnInvalid) {
		t.Fatalf("ReturnByResource() error = %v, want invalid input", err)
	}
	if store.called {
		t.Fatal("store was called for invalid resource return input")
	}

	_, found, err := service.ReturnByResource(context.Background(), ResourceReturnInput{
		ResourceType:           "group",
		ResourceID:             "grp_missing",
		GranteeSystemAccountID: "sys_grantee",
		ActorSystemAccountID:   "sys_admin",
	})
	if err != nil {
		t.Fatalf("ReturnByResource() missing error = %v", err)
	}
	if found {
		t.Fatal("ReturnByResource() found missing authorization")
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

func TestServiceExpireDueUsesDefaultBatchAndInvalidatesAuthorizationCache(t *testing.T) {
	now := time.Date(2026, 7, 9, 13, 30, 0, 0, time.UTC)
	store := &authorizationExpirySweepStoreStub{
		result: port.ManagementResourceAuthorizationExpirySweepResult{Expired: 2},
	}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		ExpirySweepStore:         store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})

	got, err := service.ExpireDue(context.Background(), ExpirySweepInput{})

	if err != nil {
		t.Fatalf("ExpireDue() error = %v", err)
	}
	if got.Expired != 2 {
		t.Fatalf("ExpireDue() = %+v, want expired 2", got)
	}
	if !store.called ||
		store.input.Limit != defaultAuthorizationExpirySweepBatchSize ||
		!store.input.ExpiredAt.Equal(now) {
		t.Fatalf("store input = %+v", store.input)
	}
	if invalidator.calls != 1 || invalidator.reason != ResourceAuthorizationExpiredReason {
		t.Fatalf("invalidator calls=%d reason=%q", invalidator.calls, invalidator.reason)
	}
}

func TestServiceExpireDueNormalizesNegativeLimitAndSkipsInvalidationWhenEmpty(t *testing.T) {
	now := time.Date(2026, 7, 9, 13, 30, 0, 0, time.UTC)
	store := &authorizationExpirySweepStoreStub{}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		ExpirySweepStore:         store,
		Now:                      func() time.Time { return now },
		AuthorizationInvalidator: invalidator,
	})

	got, err := service.ExpireDue(context.Background(), ExpirySweepInput{Limit: -10})

	if err != nil {
		t.Fatalf("ExpireDue() error = %v", err)
	}
	if got.Expired != 0 {
		t.Fatalf("ExpireDue() = %+v, want expired 0", got)
	}
	if !store.called || store.input.Limit != 1 {
		t.Fatalf("store input = %+v, want limit 1", store.input)
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidator calls = %d, want 0", invalidator.calls)
	}
}

func TestServiceExpireDueDoesNotInvalidateWhenStoreFails(t *testing.T) {
	storeErr := errors.New("store failed")
	store := &authorizationExpirySweepStoreStub{err: storeErr}
	invalidator := &authorizationInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		ExpirySweepStore:         store,
		AuthorizationInvalidator: invalidator,
	})

	_, err := service.ExpireDue(context.Background(), ExpirySweepInput{Limit: 5})

	if !errors.Is(err, storeErr) {
		t.Fatalf("ExpireDue() error = %v, want store error", err)
	}
	if invalidator.calls != 0 {
		t.Fatalf("invalidator calls = %d, want 0", invalidator.calls)
	}
}

func TestServiceAuthorizationInvalidationFailureDoesNotOverrideSuccessfulWrite(t *testing.T) {
	now := time.Date(2026, 7, 9, 14, 30, 0, 0, time.UTC)
	invalidationErr := errors.New("invalidation failed")
	type writeResult struct {
		id      string
		found   bool
		expired int
	}
	tests := []struct {
		name        string
		wantReason  string
		wantID      string
		wantFound   bool
		wantExpired int
		run         func(*authorizationInvalidatorStub) (writeResult, error)
	}{
		{
			name:       "create",
			wantReason: ResourceAuthorizationCreatedReason,
			wantID:     "rauthgrant_created",
			wantFound:  true,
			run: func(invalidator *authorizationInvalidatorStub) (writeResult, error) {
				service := NewServiceWithOptions(ServiceOptions{
					Store:                    &authorizationCreateStoreStub{result: Summary{ID: "rauthgrant_created"}},
					Now:                      func() time.Time { return now },
					AuthorizationInvalidator: invalidator,
				})
				result, err := service.Create(context.Background(), CreateInput{
					ResourceType:                 "group",
					ResourceID:                   "grp_owner",
					ResourceOwnerSystemAccountID: "sys_owner",
					GranteeType:                  "team",
					GranteeID:                    "team_ops",
					ActorSystemAccountID:         "sys_admin",
				})
				return writeResult{id: result.ID, found: err == nil}, err
			},
		},
		{
			name:       "update",
			wantReason: ResourceAuthorizationUpdatedReason,
			wantID:     "rauthgrant_updated",
			wantFound:  true,
			run: func(invalidator *authorizationInvalidatorStub) (writeResult, error) {
				service := NewServiceWithOptions(ServiceOptions{
					UpdateStore:              &authorizationUpdateStoreStub{result: Summary{ID: "rauthgrant_updated"}, found: true},
					Now:                      func() time.Time { return now },
					AuthorizationInvalidator: invalidator,
				})
				result, found, err := service.Update(context.Background(), UpdateInput{
					AuthorizationID:      "rauthgrant_updated",
					ActorSystemAccountID: "sys_admin",
					ActorRole:            "admin",
					HasStatus:            true,
					Status:               "paused",
				})
				return writeResult{id: result.ID, found: found}, err
			},
		},
		{
			name:       "return",
			wantReason: ResourceAuthorizationReturnedReason,
			wantID:     "rauthgrant_returned",
			wantFound:  true,
			run: func(invalidator *authorizationInvalidatorStub) (writeResult, error) {
				service := NewServiceWithOptions(ServiceOptions{
					ReturnStore:              &authorizationReturnStoreStub{result: Summary{ID: "rauthgrant_returned"}, found: true},
					Now:                      func() time.Time { return now },
					AuthorizationInvalidator: invalidator,
				})
				result, found, err := service.Return(context.Background(), ReturnInput{
					AuthorizationID:        "rauthgrant_returned",
					GranteeSystemAccountID: "sys_grantee",
					ActorSystemAccountID:   "sys_admin",
				})
				return writeResult{id: result.ID, found: found}, err
			},
		},
		{
			name:       "return by resource",
			wantReason: ResourceAuthorizationReturnedReason,
			wantID:     "rauthgrant_resource_returned",
			wantFound:  true,
			run: func(invalidator *authorizationInvalidatorStub) (writeResult, error) {
				service := NewServiceWithOptions(ServiceOptions{
					ResourceReturnStore:      &authorizationResourceReturnStoreStub{result: Summary{ID: "rauthgrant_resource_returned"}, found: true},
					Now:                      func() time.Time { return now },
					AuthorizationInvalidator: invalidator,
				})
				result, found, err := service.ReturnByResource(context.Background(), ResourceReturnInput{
					ResourceType:           "account",
					ResourceID:             "acct_authorized",
					GranteeSystemAccountID: "sys_grantee",
					ActorSystemAccountID:   "sys_admin",
				})
				return writeResult{id: result.ID, found: found}, err
			},
		},
		{
			name:       "revoke",
			wantReason: ResourceAuthorizationRevokedReason,
			wantID:     "rauthgrant_revoked",
			wantFound:  true,
			run: func(invalidator *authorizationInvalidatorStub) (writeResult, error) {
				service := NewServiceWithOptions(ServiceOptions{
					RevokeStore:              &authorizationRevokeStoreStub{result: Summary{ID: "rauthgrant_revoked"}, found: true},
					Now:                      func() time.Time { return now },
					AuthorizationInvalidator: invalidator,
				})
				result, found, err := service.Revoke(context.Background(), RevokeInput{
					AuthorizationID:      "rauthgrant_revoked",
					ActorSystemAccountID: "sys_admin",
					ActorRole:            "admin",
				})
				return writeResult{id: result.ID, found: found}, err
			},
		},
		{
			name:        "expire due",
			wantReason:  ResourceAuthorizationExpiredReason,
			wantExpired: 2,
			run: func(invalidator *authorizationInvalidatorStub) (writeResult, error) {
				service := NewServiceWithOptions(ServiceOptions{
					ExpirySweepStore:         &authorizationExpirySweepStoreStub{result: port.ManagementResourceAuthorizationExpirySweepResult{Expired: 2}},
					Now:                      func() time.Time { return now },
					AuthorizationInvalidator: invalidator,
				})
				result, err := service.ExpireDue(context.Background(), ExpirySweepInput{Limit: 2})
				return writeResult{expired: result.Expired}, err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalidator := &authorizationInvalidatorStub{err: invalidationErr}

			result, err := tt.run(invalidator)

			if err != nil {
				t.Fatalf("write error = %v, want nil despite invalidation error", err)
			}
			if result.id != tt.wantID || result.found != tt.wantFound || result.expired != tt.wantExpired {
				t.Fatalf("write result = %+v, want id=%q found=%v expired=%d", result, tt.wantID, tt.wantFound, tt.wantExpired)
			}
			if invalidator.calls != 1 || invalidator.reason != tt.wantReason {
				t.Fatalf("invalidator calls=%d reason=%q, want calls=1 reason=%q", invalidator.calls, invalidator.reason, tt.wantReason)
			}
		})
	}
}

func TestAuthorizationWritesPublishAccountsStaticResetAfterCommittedAccountWrite(t *testing.T) {
	now := time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)
	summary := Summary{
		ID:                           "rauthgrant_main",
		ResourceType:                 "account",
		ResourceOwnerSystemAccountID: " owner-b ",
		GranteeType:                  "system_account",
		GranteeSystemAccountID:       "owner-a",
	}
	tests := []struct {
		name   string
		invoke func(context.Context, *accountsStaticResetPublisherStub) error
	}{
		{
			name: "create",
			invoke: func(ctx context.Context, publisher *accountsStaticResetPublisherStub) error {
				service := NewServiceWithOptions(ServiceOptions{Store: &authorizationCreateStoreStub{result: summary}, Now: func() time.Time { return now }, Publisher: publisher})
				_, err := service.Create(ctx, CreateInput{ResourceType: "account", ResourceID: "acct_main", ResourceOwnerSystemAccountID: "owner-b", GranteeType: "system_account", GranteeID: "owner-a", TargetGroupID: "grp_target", ActorSystemAccountID: "admin"})
				return err
			},
		},
		{
			name: "update",
			invoke: func(ctx context.Context, publisher *accountsStaticResetPublisherStub) error {
				service := NewServiceWithOptions(ServiceOptions{UpdateStore: &authorizationUpdateStoreStub{result: summary, found: true}, Now: func() time.Time { return now }, Publisher: publisher})
				_, _, err := service.Update(ctx, UpdateInput{AuthorizationID: "rauthgrant_main", ActorSystemAccountID: "admin", ActorRole: "admin", HasStatus: true, Status: "paused"})
				return err
			},
		},
		{
			name: "return",
			invoke: func(ctx context.Context, publisher *accountsStaticResetPublisherStub) error {
				service := NewServiceWithOptions(ServiceOptions{ReturnStore: &authorizationReturnStoreStub{result: summary, found: true}, Now: func() time.Time { return now }, Publisher: publisher})
				_, _, err := service.Return(ctx, ReturnInput{AuthorizationID: "rauthgrant_main", GranteeSystemAccountID: "owner-a", ActorSystemAccountID: "admin"})
				return err
			},
		},
		{
			name: "return by resource",
			invoke: func(ctx context.Context, publisher *accountsStaticResetPublisherStub) error {
				service := NewServiceWithOptions(ServiceOptions{ResourceReturnStore: &authorizationResourceReturnStoreStub{result: summary, found: true}, Now: func() time.Time { return now }, Publisher: publisher})
				_, _, err := service.ReturnByResource(ctx, ResourceReturnInput{ResourceType: "account", ResourceID: "acct_main", GranteeSystemAccountID: "owner-a", ActorSystemAccountID: "admin"})
				return err
			},
		},
		{
			name: "revoke",
			invoke: func(ctx context.Context, publisher *accountsStaticResetPublisherStub) error {
				service := NewServiceWithOptions(ServiceOptions{RevokeStore: &authorizationRevokeStoreStub{result: summary, found: true}, Now: func() time.Time { return now }, Publisher: publisher})
				_, _, err := service.Revoke(ctx, RevokeInput{AuthorizationID: "rauthgrant_main", ActorSystemAccountID: "admin", ActorRole: "admin"})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher := &accountsStaticResetPublisherStub{}
			if err := test.invoke(context.Background(), publisher); err != nil {
				t.Fatalf("write error = %v", err)
			}
			if publisher.calls != 1 || !reflect.DeepEqual(publisher.owners, []string{"owner-a", "owner-b"}) || publisher.allScopes {
				t.Fatalf("publisher calls=%d owners=%#v allScopes=%v", publisher.calls, publisher.owners, publisher.allScopes)
			}
		})
	}
}

func TestAuthorizationAccountTeamGranteePublishesActiveMembers(t *testing.T) {
	publisher := &accountsStaticResetPublisherStub{}
	teamReader := &authorizationTeamReaderStub{
		found: true,
		result: port.ManagementSystemTeamDetail{Members: []port.ManagementSystemTeamMemberSummary{
			{SystemAccountID: " member-b ", Status: "active"},
			{SystemAccountID: "member-a", Status: "active"},
			{SystemAccountID: "member-disabled", Status: "disabled"},
		}},
	}
	service := NewServiceWithOptions(ServiceOptions{
		UpdateStore: &authorizationUpdateStoreStub{found: true, result: Summary{
			ResourceType: "account", ResourceOwnerSystemAccountID: "owner", GranteeType: "team", GranteeTeamID: "team_ops",
		}},
		Publisher:  publisher,
		TeamReader: teamReader,
	})

	if _, found, err := service.Update(context.Background(), UpdateInput{AuthorizationID: "rauthgrant_team", ActorSystemAccountID: "admin", ActorRole: "admin", HasStatus: true, Status: "paused"}); err != nil || !found {
		t.Fatalf("Update() found=%v error=%v", found, err)
	}
	if teamReader.calls != 1 || teamReader.teamID != "team_ops" || teamReader.systemAccountID != "" {
		t.Fatalf("team reader calls=%d teamID=%q systemAccountID=%q", teamReader.calls, teamReader.teamID, teamReader.systemAccountID)
	}
	if !reflect.DeepEqual(publisher.owners, []string{"member-a", "member-b", "owner"}) || publisher.allScopes {
		t.Fatalf("publisher owners=%#v allScopes=%v", publisher.owners, publisher.allScopes)
	}
}

func TestAuthorizationGroupResourceDoesNotPublishAccountsStaticReset(t *testing.T) {
	publisher := &accountsStaticResetPublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{
		RevokeStore: &authorizationRevokeStoreStub{found: true, result: Summary{ResourceType: "group", ResourceOwnerSystemAccountID: "owner"}},
		Publisher:   publisher,
	})
	if _, found, err := service.Revoke(context.Background(), RevokeInput{AuthorizationID: "rauthgrant_group", ActorSystemAccountID: "admin", ActorRole: "admin"}); err != nil || !found {
		t.Fatalf("Revoke() found=%v error=%v", found, err)
	}
	if publisher.calls != 0 {
		t.Fatalf("publisher calls=%d, want 0", publisher.calls)
	}
}

func TestAuthorizationTeamLookupFailurePublishesAllScopes(t *testing.T) {
	tests := []struct {
		name     string
		found    bool
		readErr  error
		noReader bool
	}{
		{name: "failure", readErr: errors.New("database unavailable")},
		{name: "not found"},
		{name: "reader unavailable", noReader: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher := &accountsStaticResetPublisherStub{}
			var reader TeamReader = &authorizationTeamReaderStub{found: test.found, err: test.readErr}
			if test.noReader {
				reader = nil
			}
			var logs bytes.Buffer
			service := NewServiceWithOptions(ServiceOptions{
				ReturnStore: &authorizationReturnStoreStub{found: true, result: Summary{ResourceType: "account", ResourceOwnerSystemAccountID: "owner", GranteeType: "team", GranteeTeamID: "team_missing"}},
				Publisher:   publisher,
				TeamReader:  reader,
				Logger:      slog.New(slog.NewTextHandler(&logs, nil)),
			})
			if _, found, err := service.Return(context.Background(), ReturnInput{AuthorizationID: "rauthgrant_team", GranteeSystemAccountID: "member", ActorSystemAccountID: "member"}); err != nil || !found {
				t.Fatalf("Return() found=%v error=%v", found, err)
			}
			if publisher.calls != 1 || !reflect.DeepEqual(publisher.owners, []string{"owner"}) || !publisher.allScopes {
				t.Fatalf("publisher calls=%d owners=%#v allScopes=%v", publisher.calls, publisher.owners, publisher.allScopes)
			}
			if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "teamId=team_missing") {
				t.Fatalf("warning log = %q", logs.String())
			}
		})
	}
}

func TestAuthorizationPublisherFailureUsesDetachedTimeoutAndWarns(t *testing.T) {
	publisher := &accountsStaticResetPublisherStub{err: errors.New("redis unavailable")}
	var logs bytes.Buffer
	service := NewServiceWithOptions(ServiceOptions{
		Publisher: publisher,
		Logger:    slog.New(slog.NewTextHandler(&logs, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	service.publishAccountsStaticResetAfterCommit(ctx, Summary{
		ID:                           "rauthgrant_main",
		ResourceType:                 "account",
		ResourceID:                   "acct_main",
		ResourceOwnerSystemAccountID: "owner",
		GranteeType:                  "system_account",
		GranteeSystemAccountID:       "grantee",
	})
	if publisher.contextErr != nil || !publisher.hasDeadline || publisher.deadlineRemaining <= 0 || publisher.deadlineRemaining > accountsStaticResetPublishTimeout {
		t.Fatalf("publisher contextErr=%v deadline=%v remaining=%v", publisher.contextErr, publisher.hasDeadline, publisher.deadlineRemaining)
	}
	if !strings.Contains(logs.String(), "level=WARN") ||
		!strings.Contains(logs.String(), "domain=accounts.static") ||
		!strings.Contains(logs.String(), "authorizationId=rauthgrant_main") ||
		!strings.Contains(logs.String(), "resourceId=acct_main") ||
		!strings.Contains(logs.String(), "redis unavailable") {
		t.Fatalf("warning log = %q", logs.String())
	}
}

func TestServiceRefreshUsageRangeWindowsBuildsHotRanges(t *testing.T) {
	now := time.Date(2026, 7, 9, 13, 30, 0, 0, time.UTC)
	store := &authorizationUsageRangeWindowStoreStub{
		result: port.ManagementAuthorizationUsageRangeWindowRefreshResult{
			Ranges:   5,
			TeamRows: 3,
			UserRows: 4,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		UsageRangeWindowStore: store,
		Now:                   func() time.Time { return now },
	})

	got, err := service.RefreshUsageRangeWindows(context.Background(), UsageRangeWindowRefreshInput{Timezone: "UTC"})

	if err != nil {
		t.Fatalf("RefreshUsageRangeWindows() error = %v", err)
	}
	wantRanges := []port.ManagementAccountUsageStatsRange{
		{StartDate: "2026-07-09", EndDate: "2026-07-09", Days: 1, MaxDays: fixedUsageStatsRangeWindowDays},
		{StartDate: "2026-07-08", EndDate: "2026-07-08", Days: 1, MaxDays: fixedUsageStatsRangeWindowDays},
		{StartDate: "2026-07-03", EndDate: "2026-07-09", Days: 7, MaxDays: fixedUsageStatsRangeWindowDays},
		{StartDate: "2026-06-09", EndDate: "2026-07-09", Days: 31, MaxDays: fixedUsageStatsRangeWindowDays},
		{StartDate: "2026-07-01", EndDate: "2026-07-09", Days: 9, MaxDays: fixedUsageStatsRangeWindowDays},
	}
	assertUsageRangesEqual(t, got.Ranges, wantRanges)
	assertUsageRangesEqual(t, store.input.Ranges, wantRanges)
	if got.RangeCount != 5 || got.TeamRows != 3 || got.UserRows != 4 || got.Today != "2026-07-09" || got.Timezone != "UTC" {
		t.Fatalf("RefreshUsageRangeWindows() = %+v", got)
	}
	if !store.called || !store.input.RefreshedAt.Equal(now) {
		t.Fatalf("store input = %+v", store.input)
	}
}

func TestServiceRefreshUsageRangeWindowsUsesTimezoneStore(t *testing.T) {
	now := time.Date(2026, 7, 8, 16, 30, 0, 0, time.UTC)
	store := &authorizationUsageRangeWindowStoreStub{
		result: port.ManagementAuthorizationUsageRangeWindowRefreshResult{Ranges: 5},
	}
	timezoneStore := &authorizationUsageStatsTimezoneStoreStub{timezone: "Asia/Shanghai", found: true}
	service := NewServiceWithOptions(ServiceOptions{
		UsageStatsTimezoneStore: timezoneStore,
		UsageRangeWindowStore:   store,
		Now:                     func() time.Time { return now },
	})

	got, err := service.RefreshUsageRangeWindows(context.Background(), UsageRangeWindowRefreshInput{})

	if err != nil {
		t.Fatalf("RefreshUsageRangeWindows() error = %v", err)
	}
	if !timezoneStore.called {
		t.Fatal("timezone store was not called")
	}
	if got.Today != "2026-07-09" || got.Timezone != "Asia/Shanghai" {
		t.Fatalf("RefreshUsageRangeWindows() today/timezone = %q/%q", got.Today, got.Timezone)
	}
	if len(store.input.Ranges) == 0 || store.input.Ranges[0].StartDate != "2026-07-09" {
		t.Fatalf("store ranges = %+v", store.input.Ranges)
	}
}

func TestServiceRefreshUsageRangeWindowsRejectsInvalidTimezone(t *testing.T) {
	store := &authorizationUsageRangeWindowStoreStub{}
	service := NewServiceWithOptions(ServiceOptions{
		UsageRangeWindowStore: store,
		Now:                   func() time.Time { return time.Date(2026, 7, 9, 13, 30, 0, 0, time.UTC) },
	})

	_, err := service.RefreshUsageRangeWindows(context.Background(), UsageRangeWindowRefreshInput{Timezone: "Invalid/Timezone"})

	if err == nil || !strings.Contains(err.Error(), "usageStatsTimezone") {
		t.Fatalf("RefreshUsageRangeWindows() error = %v, want invalid timezone", err)
	}
	if store.called {
		t.Fatal("store was called for invalid timezone")
	}
}

func assertUsageRangesEqual(t *testing.T, got []port.ManagementAccountUsageStatsRange, want []port.ManagementAccountUsageStatsRange) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ranges length = %d, want %d: %+v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("range[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestServiceListNormalizesScopeAndRedactsNonOwnerSourceDetails(t *testing.T) {
	createdAt := time.Date(2026, 7, 9, 10, 30, 0, 0, time.UTC)
	store := &authorizationListStoreStub{
		result: port.ManagementResourceAuthorizationListResult{
			Items: []port.ManagementResourceAuthorizationListRow{{
				ID:                           "rauthgrant_team",
				ResourceType:                 "account",
				ResourceID:                   "acct_main",
				ResourceName:                 "主账号",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "team",
				GranteeTeamID:                "team_ops",
				GranteeTeamName:              "运维团队",
				Status:                       "active",
				CreatedAt:                    createdAt,
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
		item.EffectiveSourceTeamName != "" {
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

func TestServiceUsageOverviewNormalizesScopeRangeAndPagination(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	store := &authorizationUsageStoreStub{
		teamResult: port.ManagementAuthorizationTeamUsageOverviewResult{
			Summary: port.ManagementAccountUsageSummary{RequestCount: 10},
			Rows: []port.ManagementAuthorizationTeamUsageRow{{
				ID: "team_ops:account:acct_main",
			}},
			HasMore: true,
		},
		userResult: port.ManagementAuthorizationUserUsageOverviewResult{
			Summary: port.ManagementAccountUsageSummary{RequestCount: 7},
			Rows: []port.ManagementAuthorizationUserUsageRow{{
				ID: "sys_grantee:group:grp_main",
			}},
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		UsageStore:              store,
		UsageStatsTimezoneStore: &authorizationUsageStatsTimezoneStoreStub{timezone: "UTC", found: true},
		Now:                     func() time.Time { return now },
	})

	teamOverview, err := service.TeamUsageOverview(context.Background(), UsageOverviewInput{
		ActorSystemAccountID:  " sys_admin ",
		ActorRole:             "admin",
		ScopedSystemAccountID: " sys_owner ",
		ResourceType:          " account ",
		ResourceID:            " acct_main ",
		TeamID:                " team_ops ",
		StartDate:             "2026-06-01",
		EndDate:               "2026-07-10",
		Page:                  2,
		PageSize:              1,
	})
	if err != nil {
		t.Fatalf("TeamUsageOverview() error = %v", err)
	}
	if !store.teamCalled ||
		store.teamInput.ActorSystemAccountID != "sys_admin" ||
		!store.teamInput.CanAccessAll ||
		store.teamInput.ScopedSystemAccountID != "sys_owner" ||
		store.teamInput.ResourceType != "account" ||
		store.teamInput.ResourceID != "acct_main" ||
		store.teamInput.TeamID != "team_ops" ||
		store.teamInput.StartDate != "2026-06-09" ||
		store.teamInput.EndDate != "2026-07-09" ||
		store.teamInput.Limit != 2 ||
		store.teamInput.Offset != 1 {
		t.Fatalf("team usage input = %+v", store.teamInput)
	}
	if teamOverview.Range.Days != 31 || teamOverview.Total != 3 || teamOverview.TeamCount != 3 || !teamOverview.HasMore {
		t.Fatalf("team overview = %+v", teamOverview)
	}

	userOverview, err := service.UserUsageOverview(context.Background(), UsageOverviewInput{
		ActorSystemAccountID:  " sys_grantee ",
		ActorRole:             "user",
		ScopedSystemAccountID: "sys_other",
		ResourceType:          "group",
	})
	if err != nil {
		t.Fatalf("UserUsageOverview() error = %v", err)
	}
	if !store.userCalled ||
		store.userInput.CanAccessAll ||
		store.userInput.ScopedSystemAccountID != "sys_grantee" ||
		store.userInput.StartDate != "2026-06-09" ||
		store.userInput.EndDate != "2026-07-09" ||
		store.userInput.Limit != defaultAuthorizationUsagePageSize+1 {
		t.Fatalf("user usage input = %+v", store.userInput)
	}
	if userOverview.Range.Days != 31 || userOverview.Summary.RequestCount != 7 {
		t.Fatalf("user overview = %+v", userOverview)
	}
}

func TestServiceUsageOverviewValidatesInput(t *testing.T) {
	service := NewServiceWithOptions(ServiceOptions{
		UsageStore:              &authorizationUsageStoreStub{},
		UsageStatsTimezoneStore: &authorizationUsageStatsTimezoneStoreStub{timezone: "UTC", found: true},
		Now:                     func() time.Time { return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC) },
	})
	for _, input := range []UsageOverviewInput{
		{ActorSystemAccountID: "", ResourceType: "account"},
		{ActorSystemAccountID: "sys_actor", ResourceType: "invalid"},
		{ActorSystemAccountID: "sys_actor", StartDate: "2026-99-99"},
		{ActorSystemAccountID: "sys_actor", EndDate: "bad-date"},
	} {
		if _, err := service.TeamUsageOverview(context.Background(), input); !errors.Is(err, ErrAuthorizationUsageInvalid) {
			t.Fatalf("TeamUsageOverview(%+v) error = %v, want invalid input", input, err)
		}
	}
}

func TestServiceAuthorizationUsageRangeMatchesNodeContract(t *testing.T) {
	defaultNow := time.Date(2026, 7, 9, 4, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		now       time.Time
		startDate string
		endDate   string
		want      port.ManagementAccountUsageStatsRange
	}{
		{
			name: "no dates defaults to recent 31 calendar days",
			now:  defaultNow,
			want: port.ManagementAccountUsageStatsRange{
				StartDate: "2026-06-09",
				EndDate:   "2026-07-09",
				Days:      31,
				MaxDays:   31,
			},
		},
		{
			name:      "start date only is a single day",
			now:       defaultNow,
			startDate: "2026-07-03",
			want: port.ManagementAccountUsageStatsRange{
				StartDate: "2026-07-03",
				EndDate:   "2026-07-03",
				Days:      1,
				MaxDays:   31,
			},
		},
		{
			name:    "end date only is a single day",
			now:     defaultNow,
			endDate: "2026-07-04",
			want: port.ManagementAccountUsageStatsRange{
				StartDate: "2026-07-04",
				EndDate:   "2026-07-04",
				Days:      1,
				MaxDays:   31,
			},
		},
		{
			name:      "complete range is capped at 31 days",
			now:       defaultNow,
			startDate: "2026-05-01",
			endDate:   "2026-07-20",
			want: port.ManagementAccountUsageStatsRange{
				StartDate: "2026-06-09",
				EndDate:   "2026-07-09",
				Days:      31,
				MaxDays:   31,
			},
		},
		{
			name: "configured timezone crosses the UTC date boundary",
			now:  time.Date(2026, 7, 8, 16, 30, 0, 0, time.UTC),
			want: port.ManagementAccountUsageStatsRange{
				StartDate: "2026-06-09",
				EndDate:   "2026-07-09",
				Days:      31,
				MaxDays:   31,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &authorizationUsageStoreStub{detailFound: true}
			timezoneStore := &authorizationUsageStatsTimezoneStoreStub{timezone: "Asia/Shanghai", found: true}
			service := NewServiceWithOptions(ServiceOptions{
				UsageStore:              store,
				UsageDetailStore:        store,
				UsageStatsTimezoneStore: timezoneStore,
				Now:                     func() time.Time { return test.now },
			})

			overview, err := service.TeamUsageOverview(context.Background(), UsageOverviewInput{
				ActorSystemAccountID: "sys_actor",
				StartDate:            test.startDate,
				EndDate:              test.endDate,
			})
			if err != nil {
				t.Fatalf("TeamUsageOverview() error = %v", err)
			}
			if overview.Range != test.want ||
				store.teamInput.StartDate != test.want.StartDate ||
				store.teamInput.EndDate != test.want.EndDate {
				t.Fatalf("overview range/input = %+v / %+v, want %+v", overview.Range, store.teamInput, test.want)
			}

			detail, found, err := service.UsageDetail(context.Background(), UsageDetailInput{
				AuthorizationID:      "rauthgrant_main",
				ActorSystemAccountID: "sys_actor",
				StartDate:            test.startDate,
				EndDate:              test.endDate,
			})
			if err != nil {
				t.Fatalf("UsageDetail() error = %v", err)
			}
			if !found {
				t.Fatal("UsageDetail() found = false, want true")
			}
			if detail.UsageRange != test.want ||
				store.detailInput.StartDate != test.want.StartDate ||
				store.detailInput.EndDate != test.want.EndDate {
				t.Fatalf("detail range/input = %+v / %+v, want %+v", detail.UsageRange, store.detailInput, test.want)
			}
			if !timezoneStore.called {
				t.Fatal("usageStatsTimezone store was not called")
			}
		})
	}
}

func TestServiceUsageDetailNormalizesScopeRangePaginationAndRedacts(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC)
	store := &authorizationUsageStoreStub{
		detailFound: true,
		detailResult: port.ManagementResourceAuthorizationUsageResult{
			Summary: port.ManagementResourceAuthorizationSummary{
				ID:                           "rauthgrant_team",
				ResourceType:                 "account",
				ResourceID:                   "acct_main",
				ResourceOwnerSystemAccountID: "sys_owner",
				GranteeType:                  "team",
				GranteeTeamID:                "team_ops",
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
					CreatedBy:       "sys_owner",
					CreatedAt:       createdAt,
					UpdatedAt:       createdAt,
				}},
				Usage:     port.ManagementAccountUsageSummary{RequestCount: 9},
				CreatedBy: "sys_owner",
				CreatedAt: createdAt,
				UpdatedAt: createdAt,
			},
			UsageBySystemAccount: []port.ManagementResourceAuthorizationUsageDetail{{
				SystemAccountID:               "sys_grantee",
				SystemAccountName:             "被授权人",
				ManagementAccountUsageSummary: port.ManagementAccountUsageSummary{RequestCount: 5},
				RangeUsage:                    port.ManagementAccountUsageSummary{RequestCount: 5},
			}},
			UsageBySystemAccountTotal:   3,
			UsageBySystemAccountHasMore: true,
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		UsageDetailStore:        store,
		UsageStatsTimezoneStore: &authorizationUsageStatsTimezoneStoreStub{timezone: "UTC", found: true},
		Now:                     func() time.Time { return now },
	})

	got, found, err := service.UsageDetail(context.Background(), UsageDetailInput{
		AuthorizationID:       " rauthgrant_team ",
		ActorSystemAccountID:  " sys_viewer ",
		ActorRole:             "user",
		ScopedSystemAccountID: "sys_other",
		StartDate:             "2026-06-01",
		EndDate:               "2026-07-10",
		Page:                  2,
		PageSize:              1,
	})
	if err != nil {
		t.Fatalf("UsageDetail() error = %v", err)
	}
	if !found {
		t.Fatal("UsageDetail() found = false, want true")
	}
	if !store.detailCalled ||
		store.detailInput.AuthorizationID != "rauthgrant_team" ||
		store.detailInput.ActorSystemAccountID != "sys_viewer" ||
		store.detailInput.ScopedSystemAccountID != "sys_viewer" ||
		store.detailInput.CanAccessAll ||
		store.detailInput.StartDate != "2026-06-09" ||
		store.detailInput.EndDate != "2026-07-09" ||
		store.detailInput.Limit != 2 ||
		store.detailInput.Offset != 1 {
		t.Fatalf("detail input = %+v", store.detailInput)
	}
	if got.UsageRange.Days != 31 ||
		got.UsageBySystemAccountTotal != 3 ||
		got.UsageBySystemAccountPage != 2 ||
		got.UsageBySystemAccountPageSize != 1 ||
		!got.UsageBySystemAccountHasMore ||
		got.Usage.RequestCount != 9 ||
		got.UsageBySystemAccount[0].RequestCount != 5 {
		t.Fatalf("usage detail = %+v", got)
	}
	if got.Permissions.CanEdit ||
		got.EffectiveSourceTeamID != "" ||
		got.CreatedBy != "" ||
		got.AuthorizationSources[0].SourceTeamID != "" {
		t.Fatalf("non-owner usage detail was not redacted: %+v", got)
	}
}

func TestServiceUsageDetailUsesDefaultPageSizeAndValidatesInput(t *testing.T) {
	store := &authorizationUsageStoreStub{detailFound: true}
	service := NewServiceWithOptions(ServiceOptions{
		UsageDetailStore:        store,
		UsageStatsTimezoneStore: &authorizationUsageStatsTimezoneStoreStub{timezone: "UTC", found: true},
		Now:                     func() time.Time { return time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC) },
	})
	if _, _, err := service.UsageDetail(context.Background(), UsageDetailInput{
		AuthorizationID:      "rauthgrant_main",
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
	}); err != nil {
		t.Fatalf("UsageDetail() error = %v", err)
	}
	if store.detailInput.Limit != defaultAuthorizationUsageDetailPageSize+1 {
		t.Fatalf("detail default limit = %d", store.detailInput.Limit)
	}
	for _, input := range []UsageDetailInput{
		{AuthorizationID: "", ActorSystemAccountID: "sys_actor"},
		{AuthorizationID: "rauthgrant_main", ActorSystemAccountID: ""},
		{AuthorizationID: "rauthgrant_main", ActorSystemAccountID: "sys_actor", StartDate: "bad-date"},
		{AuthorizationID: "rauthgrant_main", ActorSystemAccountID: "sys_actor", EndDate: "2026-99-99"},
	} {
		if _, _, err := service.UsageDetail(context.Background(), input); !errors.Is(err, ErrAuthorizationUsageInvalid) {
			t.Fatalf("UsageDetail(%+v) error = %v, want invalid input", input, err)
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

type authorizationResourceReturnStoreStub struct {
	called bool
	input  port.ManagementResourceAuthorizationReturnResourceInput
	result Summary
	found  bool
	err    error
}

func (s *authorizationResourceReturnStoreStub) ReturnManagementResourceAuthorizationForGranteeByResource(_ context.Context, input port.ManagementResourceAuthorizationReturnResourceInput) (port.ManagementResourceAuthorizationSummary, bool, error) {
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

type authorizationExpirySweepStoreStub struct {
	called bool
	input  port.ManagementResourceAuthorizationExpirySweepInput
	result port.ManagementResourceAuthorizationExpirySweepResult
	err    error
}

func (s *authorizationExpirySweepStoreStub) ExpireDueManagementResourceAuthorizations(_ context.Context, input port.ManagementResourceAuthorizationExpirySweepInput) (port.ManagementResourceAuthorizationExpirySweepResult, error) {
	s.called = true
	s.input = input
	if s.err != nil {
		return port.ManagementResourceAuthorizationExpirySweepResult{}, s.err
	}
	return s.result, nil
}

type authorizationUsageStatsTimezoneStoreStub struct {
	called   bool
	timezone string
	found    bool
	err      error
}

func (s *authorizationUsageStatsTimezoneStoreStub) GetManagementUsageStatsTimezone(_ context.Context) (string, bool, error) {
	s.called = true
	if s.err != nil {
		return "", false, s.err
	}
	return s.timezone, s.found, nil
}

type authorizationUsageRangeWindowStoreStub struct {
	called bool
	input  port.ManagementAuthorizationUsageRangeWindowRefreshInput
	result port.ManagementAuthorizationUsageRangeWindowRefreshResult
	err    error
}

func (s *authorizationUsageRangeWindowStoreStub) RefreshManagementAuthorizationUsageRangeWindows(_ context.Context, input port.ManagementAuthorizationUsageRangeWindowRefreshInput) (port.ManagementAuthorizationUsageRangeWindowRefreshResult, error) {
	s.called = true
	s.input = input
	if s.err != nil {
		return port.ManagementAuthorizationUsageRangeWindowRefreshResult{}, s.err
	}
	return s.result, nil
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

type authorizationUsageStoreStub struct {
	teamCalled   bool
	teamInput    port.ManagementAuthorizationUsageOverviewInput
	teamResult   port.ManagementAuthorizationTeamUsageOverviewResult
	teamErr      error
	userCalled   bool
	userInput    port.ManagementAuthorizationUsageOverviewInput
	userResult   port.ManagementAuthorizationUserUsageOverviewResult
	userErr      error
	detailCalled bool
	detailInput  port.ManagementResourceAuthorizationUsageInput
	detailResult port.ManagementResourceAuthorizationUsageResult
	detailFound  bool
	detailErr    error
}

func (s *authorizationUsageStoreStub) ListManagementAuthorizationTeamUsageOverview(_ context.Context, input port.ManagementAuthorizationUsageOverviewInput) (port.ManagementAuthorizationTeamUsageOverviewResult, error) {
	s.teamCalled = true
	s.teamInput = input
	if s.teamErr != nil {
		return port.ManagementAuthorizationTeamUsageOverviewResult{}, s.teamErr
	}
	return s.teamResult, nil
}

func (s *authorizationUsageStoreStub) ListManagementAuthorizationUserUsageOverview(_ context.Context, input port.ManagementAuthorizationUsageOverviewInput) (port.ManagementAuthorizationUserUsageOverviewResult, error) {
	s.userCalled = true
	s.userInput = input
	if s.userErr != nil {
		return port.ManagementAuthorizationUserUsageOverviewResult{}, s.userErr
	}
	return s.userResult, nil
}

func (s *authorizationUsageStoreStub) FindManagementResourceAuthorizationUsage(_ context.Context, input port.ManagementResourceAuthorizationUsageInput) (port.ManagementResourceAuthorizationUsageResult, bool, error) {
	s.detailCalled = true
	s.detailInput = input
	if s.detailErr != nil {
		return port.ManagementResourceAuthorizationUsageResult{}, false, s.detailErr
	}
	return s.detailResult, s.detailFound, nil
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

type accountsStaticResetPublisherStub struct {
	calls             int
	owners            []string
	allScopes         bool
	contextErr        error
	hasDeadline       bool
	deadlineRemaining time.Duration
	err               error
}

func (s *accountsStaticResetPublisherStub) PublishAccountsStaticReset(ctx context.Context, owners []string, allScopes bool) error {
	s.calls++
	s.owners = append([]string(nil), owners...)
	s.allScopes = allScopes
	s.contextErr = ctx.Err()
	deadline, ok := ctx.Deadline()
	s.hasDeadline = ok
	if ok {
		s.deadlineRemaining = time.Until(deadline)
	}
	return s.err
}

type authorizationTeamReaderStub struct {
	calls           int
	teamID          string
	systemAccountID string
	result          port.ManagementSystemTeamDetail
	found           bool
	err             error
}

func (s *authorizationTeamReaderStub) FindManagementSystemTeam(_ context.Context, teamID string, systemAccountID string) (port.ManagementSystemTeamDetail, bool, error) {
	s.calls++
	s.teamID = teamID
	s.systemAccountID = systemAccountID
	return s.result, s.found, s.err
}

var _ port.ManagementResourceAuthorizationCreator = (*authorizationCreateStoreStub)(nil)
var _ port.ManagementResourceAuthorizationGetter = (*authorizationGetStoreStub)(nil)
var _ port.ManagementResourceAuthorizationLister = (*authorizationListStoreStub)(nil)
var _ port.ManagementResourceAuthorizationRevoker = (*authorizationRevokeStoreStub)(nil)
var _ port.ManagementResourceAuthorizationReturner = (*authorizationReturnStoreStub)(nil)
var _ port.ManagementResourceAuthorizationResourceReturner = (*authorizationResourceReturnStoreStub)(nil)
var _ port.ManagementResourceAuthorizationUpdater = (*authorizationUpdateStoreStub)(nil)
var _ port.ManagementResourceAuthorizationExpirySweeper = (*authorizationExpirySweepStoreStub)(nil)
var _ port.ManagementUsageStatsTimezoneReader = (*authorizationUsageStatsTimezoneStoreStub)(nil)
var _ port.ManagementAuthorizationUsageRangeWindowRefresher = (*authorizationUsageRangeWindowStoreStub)(nil)
var _ port.ManagementAuthorizationUsageOverviewReader = (*authorizationUsageStoreStub)(nil)
var _ port.ManagementResourceAuthorizationUsageReader = (*authorizationUsageStoreStub)(nil)
var _ AuthorizationInvalidator = (*authorizationInvalidatorStub)(nil)
var _ AccountsStaticResetPublisher = (*accountsStaticResetPublisherStub)(nil)
var _ TeamReader = (*authorizationTeamReaderStub)(nil)
