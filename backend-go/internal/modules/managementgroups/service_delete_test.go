package managementgroups

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceDeleteUsesSelfOwnerScopeAndInvalidatesCaches(t *testing.T) {
	now := time.Date(2026, time.July, 11, 10, 30, 0, 0, time.UTC)
	store := &managementGroupDeleteStoreStub{
		result: port.ManagementGroupDeleteResult{
			Before: port.ManagementGroupMutationSummary{
				ID:   "grp_owner",
				Name: "生产组",
			},
			OwnerSystemAccountID: "sys_owner",
			AffectedRouteStrategies: []port.ManagementGroupDeletedRouteStrategy{{
				ID:   "route_1",
				Name: "主策略",
			}},
		},
	}
	invalidator := &managementGroupDeleteInvalidatorStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                      store,
		Invalidator:                invalidator,
		GroupLookupInvalidator:     invalidator,
		GroupAccountIDsInvalidator: invalidator,
		Now:                        func() time.Time { return now },
	})

	result, err := service.Delete(context.Background(), DeleteInput{
		ActorSystemAccountID: "sys_owner",
		ActorRole:            "user",
		SelfOnly:             true,
		GroupID:              " grp_owner ",
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if store.input.GroupID != " grp_owner " ||
		store.input.CanAccessAll ||
		store.input.EffectiveSystemAccountID != "sys_owner" ||
		!store.input.DeletedAt.Equal(now) ||
		!store.input.Now.Equal(now) {
		t.Fatalf("DeleteManagementGroup() input = %+v", store.input)
	}
	if result.Before.Name != "生产组" ||
		result.OwnerSystemAccountID != "sys_owner" ||
		len(result.AffectedRouteStrategies) != 1 ||
		result.AffectedRouteStrategies[0].Name != "主策略" {
		t.Fatalf("Delete() result = %+v", result)
	}
	if invalidator.lookupCalls != 1 ||
		invalidator.accountIDsCalls != 1 ||
		len(invalidator.runtimeReasons) != 1 ||
		invalidator.runtimeReasons[0] != GroupDeletedReason {
		t.Fatalf("invalidation = %+v", invalidator)
	}
}

func TestServiceDeleteUsesAdminAllAndTargetedOwnerScopes(t *testing.T) {
	tests := []struct {
		name                   string
		systemAccountID        string
		wantCanAccessAll       bool
		wantEffectiveAccountID string
	}{
		{
			name:             "all owners by blank",
			wantCanAccessAll: true,
		},
		{
			name:             "all owners by literal all",
			systemAccountID:  "all",
			wantCanAccessAll: true,
		},
		{
			name:                   "targeted owner",
			systemAccountID:        "sys_target",
			wantEffectiveAccountID: "sys_target",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &managementGroupDeleteStoreStub{result: port.ManagementGroupDeleteResult{
				Before:               port.ManagementGroupMutationSummary{ID: "grp_1"},
				OwnerSystemAccountID: "sys_owner",
			}}
			service := NewServiceWithOptions(ServiceOptions{Store: store})

			_, err := service.Delete(context.Background(), DeleteInput{
				ActorSystemAccountID: "sys_admin",
				ActorRole:            "admin",
				SystemAccountID:      tt.systemAccountID,
				GroupID:              "grp_1",
			})
			if err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
			if store.input.CanAccessAll != tt.wantCanAccessAll ||
				store.input.EffectiveSystemAccountID != tt.wantEffectiveAccountID {
				t.Fatalf("DeleteManagementGroup() input = %+v", store.input)
			}
		})
	}
}

func TestServiceDeleteMapsUserFacingErrorsAndSkipsInvalidation(t *testing.T) {
	tests := []struct {
		name        string
		storeErr    error
		wantErr     error
		wantMessage string
	}{
		{
			name:     "not found",
			storeErr: port.ErrManagementGroupNotFound,
			wantErr:  ErrGroupNotFound,
		},
		{
			name:     "default",
			storeErr: port.ErrManagementGroupDefaultReadonly,
			wantErr:  ErrGroupDefaultDelete,
		},
		{
			name:        "route guard",
			storeErr:    fmt.Errorf("%w: 无法删除测试组", port.ErrManagementGroupRouteStrategyWouldLose),
			wantMessage: "无法删除测试组",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &managementGroupDeleteStoreStub{err: tt.storeErr}
			invalidator := &managementGroupDeleteInvalidatorStub{}
			service := NewServiceWithOptions(ServiceOptions{
				Store:                      store,
				Invalidator:                invalidator,
				GroupLookupInvalidator:     invalidator,
				GroupAccountIDsInvalidator: invalidator,
			})

			_, err := service.Delete(context.Background(), DeleteInput{
				ActorSystemAccountID: "sys_owner",
				ActorRole:            "user",
				SelfOnly:             true,
				GroupID:              "grp_1",
			})
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("Delete() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantMessage != "" {
				message, ok := UpdateRejectedMessage(err)
				if !ok || message != tt.wantMessage {
					t.Fatalf("Delete() rejected message = %q, %v", message, ok)
				}
			}
			if invalidator.lookupCalls != 0 ||
				invalidator.accountIDsCalls != 0 ||
				len(invalidator.runtimeReasons) != 0 {
				t.Fatalf("failed delete invalidation = %+v", invalidator)
			}
		})
	}
}

func TestServiceDeleteKeepsCommittedWriteWhenInvalidationFails(t *testing.T) {
	store := &managementGroupDeleteStoreStub{result: port.ManagementGroupDeleteResult{
		Before:               port.ManagementGroupMutationSummary{ID: "grp_1"},
		OwnerSystemAccountID: "sys_owner",
	}}
	invalidator := &managementGroupDeleteInvalidatorStub{
		lookupErr:     errors.New("lookup redis down"),
		accountIDsErr: errors.New("account IDs redis down"),
		runtimeErr:    errors.New("runtime redis down"),
	}
	service := NewServiceWithOptions(ServiceOptions{
		Store:                      store,
		Invalidator:                invalidator,
		GroupLookupInvalidator:     invalidator,
		GroupAccountIDsInvalidator: invalidator,
		Logger:                     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.Delete(ctx, DeleteInput{
		ActorSystemAccountID: "sys_owner",
		ActorRole:            "user",
		SelfOnly:             true,
		GroupID:              "grp_1",
	})
	if err != nil || result.Before.ID != "grp_1" {
		t.Fatalf("Delete() result=%+v error=%v", result, err)
	}
	if invalidator.lookupContextErr != nil ||
		invalidator.accountIDsContextErr != nil ||
		invalidator.runtimeContextErr != nil {
		t.Fatalf(
			"invalidation contexts lookup=%v accountIDs=%v runtime=%v",
			invalidator.lookupContextErr,
			invalidator.accountIDsContextErr,
			invalidator.runtimeContextErr,
		)
	}
}

type managementGroupDeleteStoreStub struct {
	groupOptionStoreStub
	input  port.ManagementGroupDeleteInput
	result port.ManagementGroupDeleteResult
	err    error
	calls  int
}

func (s *managementGroupDeleteStoreStub) DeleteManagementGroup(
	_ context.Context,
	input port.ManagementGroupDeleteInput,
) (port.ManagementGroupDeleteResult, error) {
	s.calls++
	s.input = input
	return s.result, s.err
}

type managementGroupDeleteInvalidatorStub struct {
	lookupCalls          int
	lookupErr            error
	lookupContextErr     error
	accountIDsCalls      int
	accountIDsErr        error
	accountIDsContextErr error
	runtimeReasons       []string
	runtimeErr           error
	runtimeContextErr    error
}

func (s *managementGroupDeleteInvalidatorStub) InvalidateGroupLookupCache(ctx context.Context) error {
	s.lookupCalls++
	s.lookupContextErr = ctx.Err()
	return s.lookupErr
}

func (s *managementGroupDeleteInvalidatorStub) InvalidateGroupAccountIDsCache(ctx context.Context) error {
	s.accountIDsCalls++
	s.accountIDsContextErr = ctx.Err()
	return s.accountIDsErr
}

func (s *managementGroupDeleteInvalidatorStub) InvalidateGatewayRuntime(ctx context.Context, reason string) error {
	s.runtimeReasons = append(s.runtimeReasons, reason)
	s.runtimeContextErr = ctx.Err()
	return s.runtimeErr
}
