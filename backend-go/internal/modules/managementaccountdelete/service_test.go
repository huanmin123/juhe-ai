package managementaccountdelete

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceDeleteUsesSelfScopeAndInvalidatesDeletedAccounts(t *testing.T) {
	now := time.Date(2026, time.July, 20, 12, 0, 0, 0, time.UTC)
	store := &deleteStoreStub{result: port.ManagementAccountDeleteResult{
		Before:            port.ManagementAccountDeleteSummary{ID: "acc_source", SystemAccountID: "sys_owner", Name: "主账户"},
		DeletedAccountIDs: []string{"acc_source", "acc_instance"},
	}}
	invalidator := &deleteInvalidatorStub{}
	service := NewService(Options{
		Store:                      store,
		AccountLookupInvalidator:   invalidator,
		GroupAccountIDsInvalidator: invalidator,
		AuthorizationInvalidator:   invalidator,
		GatewayRuntimeInvalidator:  invalidator,
		Now:                        func() time.Time { return now },
	})

	result, err := service.Delete(context.Background(), DeleteInput{
		ActorSystemAccountID: "sys_owner",
		ActorRole:            "user",
		SystemAccountID:      "sys_other",
		SelfOnly:             true,
		AccountID:            " acc_source ",
	})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if store.input.AccountID != "acc_source" || store.input.EffectiveSystemAccountID != "sys_owner" || store.input.CanAccessAll {
		t.Fatalf("store input = %+v", store.input)
	}
	if !store.input.DeletedAt.Equal(now) || store.input.DeletedBy != "sys_owner" {
		t.Fatalf("delete audit input = %+v", store.input)
	}
	if result.Before.ID != "acc_source" || len(result.DeletedAccountIDs) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if len(invalidator.accountIDs) != 2 || invalidator.groupCalls != 1 || invalidator.authorizationReasons[0] != AccountDeletedReason || invalidator.runtimeReasons[0] != AccountDeletedReason {
		t.Fatalf("invalidations = %+v", invalidator)
	}
}

func TestServiceDeleteMapsRepositoryOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		store   *deleteStoreStub
		input   DeleteInput
		wantErr error
	}{
		{
			name:    "not found",
			store:   &deleteStoreStub{err: port.ErrManagementAccountDeleteNotFound},
			input:   DeleteInput{ActorSystemAccountID: "sys_admin", ActorRole: "admin", AccountID: "missing"},
			wantErr: ErrAccountNotFound,
		},
		{
			name:    "authorization instance rejected",
			store:   &deleteStoreStub{err: port.ErrManagementAccountDeleteAuthorizationInstance},
			input:   DeleteInput{ActorSystemAccountID: "sys_owner", ActorRole: "user", SelfOnly: true, AccountID: "acc_instance"},
			wantErr: ErrAuthorizationInstance,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewService(Options{Store: tt.store})
			_, err := service.Delete(context.Background(), tt.input)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Delete() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

type deleteStoreStub struct {
	input  port.ManagementAccountDeleteInput
	result port.ManagementAccountDeleteResult
	err    error
}

func (s *deleteStoreStub) DeleteManagementAccount(_ context.Context, input port.ManagementAccountDeleteInput) (port.ManagementAccountDeleteResult, error) {
	s.input = input
	return s.result, s.err
}

type deleteInvalidatorStub struct {
	accountIDs           []string
	groupCalls           int
	authorizationReasons []string
	runtimeReasons       []string
}

func (s *deleteInvalidatorStub) InvalidateAccountLookupCache(_ context.Context, accountID string) error {
	s.accountIDs = append(s.accountIDs, accountID)
	return nil
}

func (s *deleteInvalidatorStub) InvalidateGroupAccountIDsCache(context.Context) error {
	s.groupCalls++
	return nil
}

func (s *deleteInvalidatorStub) InvalidateAuthorizationChanged(_ context.Context, reason string) error {
	s.authorizationReasons = append(s.authorizationReasons, reason)
	return nil
}

func (s *deleteInvalidatorStub) InvalidateGatewayRuntime(_ context.Context, reason string) error {
	s.runtimeReasons = append(s.runtimeReasons, reason)
	return nil
}
