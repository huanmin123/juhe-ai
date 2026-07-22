package managementgroups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceUpdateOwner(t *testing.T) {
	now := time.Date(2026, time.July, 11, 8, 0, 0, 0, time.UTC)
	store := managementGroupUpdateStoreStub{
		managementGroupDetailStoreStub: managementGroupDetailStoreStub{
			managementGroupListStoreStub: managementGroupListStoreStub{
				stats:         []port.ManagementGroupAccountStatsRow{{SystemAccountID: "sys_owner", GroupID: "grp_owner", Total: 1}},
				timezone:      "Asia/Shanghai",
				timezoneFound: true,
			},
			row: port.ManagementGroupListRow{
				ID:                   "grp_owner",
				SystemAccountID:      "sys_owner",
				SystemAccountName:    "所有者",
				Name:                 "更新后",
				ProviderCode:         "gpt",
				Enabled:              false,
				GroupType:            "high_concurrency",
				SchedulingPolicyJSON: stringPointer(mustSchedulingPolicyJSON(t, SchedulingPolicyInput{DefaultSoftConcurrency: intPointer(9)})),
				AccessType:           "owner",
			},
			found:      true,
			accountIDs: []string{"acc_1"},
		},
		updateResult: port.ManagementGroupUpdateResult{
			Before: port.ManagementGroupMutationSummary{
				ID:           "grp_owner",
				Name:         "更新前",
				ProviderCode: "openai",
				Enabled:      true,
				GroupType:    "personal",
			},
			After: port.ManagementGroupMutationSummary{
				ID:           "grp_owner",
				Name:         "更新后",
				ProviderCode: "gpt",
				Enabled:      false,
				GroupType:    "high_concurrency",
			},
			AccessType:               "owner",
			OwnerSystemAccountID:     "sys_owner",
			EffectiveSystemAccountID: "sys_owner",
		},
	}
	invalidator := &managementGroupUpdateInvalidatorStub{}
	concurrency := &managementGroupAccountConcurrencyStub{values: map[string]int{"acc_1": 2}}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              &store,
		ListStore:          &store,
		DetailStore:        &store,
		AccountConcurrency: concurrency,
		Invalidator:        invalidator,
		Now:                func() time.Time { return now },
	})
	description := "   "
	result, err := service.Update(context.Background(), UpdateInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		GroupID:              "grp_owner",
		HasName:              true,
		Name:                 "  更新后  ",
		HasProviderCode:      true,
		ProviderCode:         " gpt ",
		HasDescription:       true,
		Description:          &description,
		HasEnabled:           true,
		Enabled:              false,
		HasGroupType:         true,
		GroupType:            "high_concurrency",
		HasSchedulingPolicy:  true,
		SchedulingPolicy:     &SchedulingPolicyInput{DefaultSoftConcurrency: intPointer(9)},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !store.updateInput.CanAccessAll || store.updateInput.EffectiveSystemAccountID != "" {
		t.Fatalf("UpdateManagementGroup() scope = %+v, want admin all-owner scope", store.updateInput)
	}
	if store.updateInput.Name != "更新后" || store.updateInput.ProviderCode != "gpt" {
		t.Fatalf("UpdateManagementGroup() normalized input = %+v", store.updateInput)
	}
	if store.updateInput.Description != nil {
		t.Fatalf("UpdateManagementGroup() description = %#v, want nil", store.updateInput.Description)
	}
	if store.updateInput.SchedulingPolicyJSON == nil ||
		!strings.Contains(*store.updateInput.SchedulingPolicyJSON, `"defaultSoftConcurrency":9`) {
		t.Fatalf("UpdateManagementGroup() scheduling policy = %#v", store.updateInput.SchedulingPolicyJSON)
	}
	if store.updateInput.DefaultSchedulingPolicyJSON == "" {
		t.Fatal("UpdateManagementGroup() default scheduling policy is empty")
	}
	if invalidator.lookupCalls != 1 {
		t.Fatalf("InvalidateGroupLookupCache() calls = %d, want 1", invalidator.lookupCalls)
	}
	if invalidator.runtimeReasons == nil || invalidator.runtimeReasons[0] != GroupUpdatedReason {
		t.Fatalf("InvalidateGatewayRuntime() reasons = %#v, want %q", invalidator.runtimeReasons, GroupUpdatedReason)
	}
	if result.Group.Name != "更新后" || result.Group.AccountStats.CurrentConcurrency != 2 {
		t.Fatalf("Update() group = %+v", result.Group)
	}
	if result.Before.Name != "更新前" || result.AccessType != "owner" {
		t.Fatalf("Update() result = %+v", result)
	}
}

func TestServiceUpdateAuthorizedSettings(t *testing.T) {
	now := time.Date(2026, time.July, 11, 8, 0, 0, 0, time.UTC)
	policyJSON := mustSchedulingPolicyJSON(t, SchedulingPolicyInput{})
	store := managementGroupUpdateStoreStub{
		managementGroupDetailStoreStub: managementGroupDetailStoreStub{
			managementGroupListStoreStub: managementGroupListStoreStub{
				timezone:      "UTC",
				timezoneFound: true,
			},
			row: port.ManagementGroupListRow{
				ID:                      "grp_authorized",
				SystemAccountID:         "sys_owner",
				SystemAccountName:       "授权方",
				Name:                    "授权分组",
				ProviderCode:            "gpt",
				Enabled:                 false,
				GroupType:               "high_concurrency",
				SchedulingPolicyJSON:    &policyJSON,
				AccessType:              "authorized",
				GroupAuthorizationID:    "auth_1",
				AuthorizationStatus:     "active",
				AuthorizationLimitsJSON: stringPointer("{}"),
			},
			found: true,
		},
		updateResult: port.ManagementGroupUpdateResult{
			Before: port.ManagementGroupMutationSummary{
				ID:        "grp_authorized",
				Name:      "授权分组",
				Enabled:   true,
				GroupType: "personal",
			},
			After: port.ManagementGroupMutationSummary{
				ID:        "grp_authorized",
				Name:      "授权分组",
				Enabled:   false,
				GroupType: "high_concurrency",
			},
			AccessType:               "authorized",
			OwnerSystemAccountID:     "sys_owner",
			EffectiveSystemAccountID: "sys_grantee",
			GroupAuthorizationID:     "auth_1",
		},
	}
	invalidator := &managementGroupUpdateInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              &store,
		ListStore:          &store,
		DetailStore:        &store,
		AccountConcurrency: &managementGroupAccountConcurrencyStub{values: map[string]int{}},
		Invalidator:        invalidator,
		Now:                func() time.Time { return now },
	})
	result, err := service.Update(context.Background(), UpdateInput{
		ActorSystemAccountID: "sys_grantee",
		ActorRole:            "user",
		SelfOnly:             true,
		GroupID:              "grp_authorized",
		HasEnabled:           true,
		Enabled:              false,
		HasGroupType:         true,
		GroupType:            "high_concurrency",
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if store.updateInput.CanAccessAll || store.updateInput.EffectiveSystemAccountID != "sys_grantee" {
		t.Fatalf("UpdateManagementGroup() scope = %+v, want self grantee scope", store.updateInput)
	}
	if invalidator.lookupCalls != 0 {
		t.Fatalf("InvalidateGroupLookupCache() calls = %d, want 0", invalidator.lookupCalls)
	}
	if len(invalidator.runtimeReasons) != 1 ||
		invalidator.runtimeReasons[0] != GroupAuthorizationSettingsUpdatedReason {
		t.Fatalf(
			"InvalidateGatewayRuntime() reasons = %#v, want %q",
			invalidator.runtimeReasons,
			GroupAuthorizationSettingsUpdatedReason,
		)
	}
	if result.AccessType != "authorized" ||
		result.EffectiveSystemAccountID != "sys_grantee" ||
		result.Group.GroupAuthorizationID != "auth_1" {
		t.Fatalf("Update() result = %+v", result)
	}
}

func TestServiceUpdateMapsStoreErrors(t *testing.T) {
	tests := []struct {
		name     string
		storeErr error
		wantErr  error
		wantText string
	}{
		{name: "not found", storeErr: port.ErrManagementGroupNotFound, wantErr: ErrGroupNotFound},
		{name: "default", storeErr: port.ErrManagementGroupDefaultReadonly, wantErr: ErrGroupDefaultReadonly},
		{name: "provider accounts", storeErr: port.ErrManagementGroupProviderHasAccounts, wantErr: ErrGroupProviderHasAccounts},
		{
			name:     "authorized fields",
			storeErr: fmt.Errorf("%w: 授权分组使用配置包含未知字段：name", port.ErrManagementGroupAuthorizedFields),
			wantText: "授权分组使用配置包含未知字段：name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := managementGroupUpdateStoreStub{updateErr: tt.storeErr}
			service := NewServiceWithOptions(ServiceOptions{Store: &store})
			_, err := service.Update(context.Background(), UpdateInput{
				ActorSystemAccountID: "sys_owner",
				ActorRole:            "user",
				SelfOnly:             true,
				GroupID:              "grp_1",
				HasEnabled:           true,
				Enabled:              false,
			})
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Update() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantText != "" {
				message, ok := UpdateRejectedMessage(err)
				if !ok || message != tt.wantText {
					t.Fatalf("UpdateRejectedMessage() = %q, %v", message, ok)
				}
			}
		})
	}
}

func TestServiceUpdateReturnsDetailReadFailureAfterCommittedWrite(t *testing.T) {
	wantErr := errors.New("detail read failed")
	store := managementGroupUpdateStoreStub{
		managementGroupDetailStoreStub: managementGroupDetailStoreStub{findErr: wantErr},
		updateResult: port.ManagementGroupUpdateResult{
			AccessType:               "owner",
			OwnerSystemAccountID:     "sys_owner",
			EffectiveSystemAccountID: "sys_owner",
		},
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              &store,
		DetailStore:        &store,
		AccountConcurrency: &managementGroupAccountConcurrencyStub{},
	})

	_, err := service.Update(context.Background(), UpdateInput{
		ActorSystemAccountID: "sys_owner",
		ActorRole:            "user",
		SelfOnly:             true,
		GroupID:              "grp_owner",
		HasEnabled:           true,
		Enabled:              false,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Update() error = %v, want %v", err, wantErr)
	}
	if store.updateCalls != 1 {
		t.Fatalf("UpdateManagementGroup() calls = %d, want 1", store.updateCalls)
	}
}

func TestServiceUpdateUsesStoreConflictNameForProviderOnlyPatch(t *testing.T) {
	store := managementGroupUpdateStoreStub{
		updateErr: fmt.Errorf("%w: 已有名称", port.ErrManagementGroupNameExists),
	}
	service := NewServiceWithOptions(ServiceOptions{Store: &store})
	_, err := service.Update(context.Background(), UpdateInput{
		ActorSystemAccountID: "sys_owner",
		ActorRole:            "user",
		SelfOnly:             true,
		GroupID:              "grp_1",
		HasProviderCode:      true,
		ProviderCode:         "gpt",
	})
	message, ok := NameExistsMessage(err)
	if !ok || message != "同一供应商下分组名称已存在：已有名称" {
		t.Fatalf("NameExistsMessage() = %q, %v", message, ok)
	}
}

func TestServiceUpdateKeepsCommittedOwnerWriteWhenInvalidationFails(t *testing.T) {
	now := time.Date(2026, time.July, 11, 8, 30, 0, 0, time.UTC)
	store := managementGroupUpdateStoreStub{
		managementGroupDetailStoreStub: managementGroupDetailStoreStub{
			managementGroupListStoreStub: managementGroupListStoreStub{
				timezone:      "UTC",
				timezoneFound: true,
			},
			row: port.ManagementGroupListRow{
				ID:              "grp_owner",
				SystemAccountID: "sys_owner",
				Name:            "更新后",
				ProviderCode:    "openai",
				Enabled:         false,
				GroupType:       "personal",
				AccessType:      "owner",
			},
			found: true,
		},
		updateResult: port.ManagementGroupUpdateResult{
			Before: port.ManagementGroupMutationSummary{
				ID:           "grp_owner",
				Name:         "更新前",
				ProviderCode: "openai",
				Enabled:      true,
				GroupType:    "personal",
			},
			After: port.ManagementGroupMutationSummary{
				ID:           "grp_owner",
				Name:         "更新后",
				ProviderCode: "openai",
				Enabled:      false,
				GroupType:    "personal",
			},
			AccessType:               "owner",
			OwnerSystemAccountID:     "sys_owner",
			EffectiveSystemAccountID: "sys_owner",
		},
	}
	invalidator := &managementGroupUpdateInvalidatorStub{
		runtimeErr: errors.New("runtime redis down"),
		lookupErr:  errors.New("lookup redis down"),
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:              &store,
		ListStore:          &store,
		DetailStore:        &store,
		AccountConcurrency: &managementGroupAccountConcurrencyStub{values: map[string]int{}},
		Invalidator:        invalidator,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:                func() time.Time { return now },
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.Update(ctx, UpdateInput{
		ActorSystemAccountID: "sys_owner",
		ActorRole:            "user",
		SelfOnly:             true,
		GroupID:              "grp_owner",
		HasEnabled:           true,
		Enabled:              false,
	})
	if err != nil {
		t.Fatalf("Update() error = %v, want committed write success", err)
	}
	if result.Group.ID != "grp_owner" || result.Group.Enabled {
		t.Fatalf("Update() group = %+v", result.Group)
	}
	if invalidator.lookupCalls != 1 || len(invalidator.runtimeReasons) != 1 {
		t.Fatalf(
			"invalidation calls lookup=%d runtime=%#v",
			invalidator.lookupCalls,
			invalidator.runtimeReasons,
		)
	}
	if invalidator.lookupContextErr != nil || invalidator.runtimeContextErr != nil {
		t.Fatalf(
			"invalidation contexts lookup=%v runtime=%v, want detached contexts",
			invalidator.lookupContextErr,
			invalidator.runtimeContextErr,
		)
	}
}

type managementGroupUpdateStoreStub struct {
	managementGroupDetailStoreStub
	updateInput  port.ManagementGroupUpdateInput
	updateResult port.ManagementGroupUpdateResult
	updateErr    error
	updateCalls  int
}

func (s *managementGroupUpdateStoreStub) UpdateManagementGroup(
	_ context.Context,
	input port.ManagementGroupUpdateInput,
) (port.ManagementGroupUpdateResult, error) {
	s.updateCalls++
	s.updateInput = input
	return s.updateResult, s.updateErr
}

type managementGroupUpdateInvalidatorStub struct {
	runtimeReasons    []string
	runtimeErr        error
	runtimeContextErr error
	lookupCalls       int
	lookupErr         error
	lookupContextErr  error
}

func (s *managementGroupUpdateInvalidatorStub) InvalidateGatewayRuntime(ctx context.Context, reason string) error {
	s.runtimeReasons = append(s.runtimeReasons, reason)
	s.runtimeContextErr = ctx.Err()
	return s.runtimeErr
}

func (s *managementGroupUpdateInvalidatorStub) InvalidateGroupLookupCache(ctx context.Context) error {
	s.lookupCalls++
	s.lookupContextErr = ctx.Err()
	return s.lookupErr
}

func mustSchedulingPolicyJSON(t *testing.T, input SchedulingPolicyInput) string {
	t.Helper()
	policy, err := normalizeSchedulingPolicy(&input)
	if err != nil {
		t.Fatalf("normalizeSchedulingPolicy() error = %v", err)
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(encoded)
}

func intPointer(value int) *int {
	return &value
}
