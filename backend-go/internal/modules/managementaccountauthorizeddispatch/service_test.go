package managementaccountauthorizeddispatch

import (
	"context"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/accountpagedata"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceUpdateUsesSelfScopeAndInvalidatesRuntimeAndPageData(t *testing.T) {
	store := &dispatchStoreStub{result: port.ManagementAccountAuthorizedDispatchResult{
		Account:       port.ManagementAccountAuthorizedDispatchAccount{ID: "acct_auth", SystemAccountID: "sys_self", Name: "授权实例", Status: "active", Schedulable: true, Priority: 8, BoundGroupID: "group_1", AccountAuthorizationID: "auth_1"},
		ChangedFields: []string{"status", "priority", "clearFailureState"},
	}, found: true}
	runtime := &dispatchRuntimeStub{}
	cache := &dispatchInvalidatorStub{}
	page := &dispatchPageStub{}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	service := NewService(Options{Store: store, RuntimeClearer: runtime, AccountLookupInvalidator: cache, GatewayInvalidator: cache, PageDataPublisher: page, Now: func() time.Time { return now }})
	status, priority := "active", 8
	result, err := service.Update(context.Background(), Input{ActorSystemAccountID: "sys_self", ActorRole: "user", SelfOnly: true, AccountID: "acct_auth", Status: &status, Priority: &priority, ClearFailureState: true})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if store.input.EffectiveSystemAccountID != "sys_self" || store.input.CanAccessAll {
		t.Fatalf("store input = %+v", store.input)
	}
	if result.Account.AccessType != "authorized" || result.Account.BoundGroupID != "group_1" {
		t.Fatalf("account = %+v", result.Account)
	}
	if runtime.calls != 1 || runtime.target.AccountAuthorizationID != "auth_1" || cache.lookupCalls != 1 || cache.gatewayCalls != 1 {
		t.Fatalf("runtime=%+v cache=%+v", runtime, cache)
	}
	if page.staticCalls != 1 || page.runtimeCalls != 1 || page.last.OwnerSystemAccountIDs[0] != "sys_self" {
		t.Fatalf("page = %+v", page)
	}
}

func TestServiceUpdateAdminScopeAndValidation(t *testing.T) {
	store := &dispatchStoreStub{result: port.ManagementAccountAuthorizedDispatchResult{Account: port.ManagementAccountAuthorizedDispatchAccount{ID: "acct_auth", SystemAccountID: "sys_target"}}, found: true}
	service := NewService(Options{Store: store})
	priority := -1
	if _, err := service.Update(context.Background(), Input{ActorSystemAccountID: "sys_admin", ActorRole: "admin", AccountID: "acct_auth", Priority: &priority}); err != ErrInvalid {
		t.Fatalf("invalid error = %v", err)
	}
	priority = 1
	if _, err := service.Update(context.Background(), Input{ActorSystemAccountID: "sys_admin", ActorRole: "admin", SystemAccountID: "sys_target", AccountID: "acct_auth", Priority: &priority}); err != nil {
		t.Fatalf("admin Update() error = %v", err)
	}
	if store.input.EffectiveSystemAccountID != "sys_target" || store.input.CanAccessAll {
		t.Fatalf("store input = %+v", store.input)
	}
}

type dispatchStoreStub struct {
	input  port.ManagementAccountAuthorizedDispatchInput
	result port.ManagementAccountAuthorizedDispatchResult
	found  bool
	err    error
}

func (s *dispatchStoreStub) UpdateManagementAccountAuthorizedDispatch(_ context.Context, input port.ManagementAccountAuthorizedDispatchInput) (port.ManagementAccountAuthorizedDispatchResult, bool, error) {
	s.input = input
	return s.result, s.found, s.err
}

type dispatchRuntimeStub struct {
	calls  int
	target RuntimeTarget
}

func (s *dispatchRuntimeStub) ClearAuthorizedAccountRuntimeAvailability(_ context.Context, target RuntimeTarget) error {
	s.calls++
	s.target = target
	return nil
}

type dispatchInvalidatorStub struct{ lookupCalls, gatewayCalls int }

func (s *dispatchInvalidatorStub) InvalidateAccountLookupCache(context.Context, string) error {
	s.lookupCalls++
	return nil
}
func (s *dispatchInvalidatorStub) InvalidateGatewayRuntime(context.Context, string) error {
	s.gatewayCalls++
	return nil
}

type dispatchPageStub struct {
	staticCalls, runtimeCalls int
	last                      accountpagedata.ChangeInput
}

func (s *dispatchPageStub) PublishAccountStaticChange(_ context.Context, input accountpagedata.ChangeInput) error {
	s.staticCalls++
	s.last = input
	return nil
}
func (s *dispatchPageStub) PublishAccountRuntimeChange(_ context.Context, input accountpagedata.ChangeInput) error {
	s.runtimeCalls++
	s.last = input
	return nil
}
