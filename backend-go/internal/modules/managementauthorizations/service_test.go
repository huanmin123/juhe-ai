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
var _ port.ManagementResourceAuthorizationReturner = (*authorizationReturnStoreStub)(nil)
var _ AuthorizationInvalidator = (*authorizationInvalidatorStub)(nil)
