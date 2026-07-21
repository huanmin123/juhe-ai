package managementaccountgroupbinding

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/accountpagedata"
	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceBindAdminOwnerScopeAndInvalidations(t *testing.T) {
	now := time.Date(2026, 7, 20, 9, 30, 0, 0, time.UTC)
	store := &bindingStoreStub{result: bindingStoreResult("group_old", "group_new")}
	pageData := &bindingPageDataStub{}
	invalidator := &bindingInvalidatorStub{}
	service := NewService(Options{
		Store:                      store,
		GranteeReader:              bindingGranteeReaderStub{ids: []string{"sys_grantee"}},
		PageDataPublisher:          pageData,
		RuntimeInvalidator:         invalidator,
		GroupAccountIDsInvalidator: invalidator,
		Now:                        func() time.Time { return now },
	})

	result, err := service.Bind(t.Context(), BindInput{
		ActorSystemAccountID: "sys_admin",
		ActorRole:            "admin",
		SystemAccountID:      "sys_owner",
		AccountID:            " acct_main ",
		GroupID:              " group_new ",
	})
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	if store.input.AccountID != "acct_main" || store.input.GroupID != "group_new" ||
		store.input.EffectiveSystemAccountID != "sys_owner" || store.input.CanAccessAll ||
		!store.input.UpdatedAt.Equal(now) {
		t.Fatalf("store input = %+v", store.input)
	}
	if result.Account.AccessType != "owner" || result.PreviousGroupID != "group_old" || result.Account.BoundGroupID != "group_new" {
		t.Fatalf("result = %+v", result)
	}
	if len(pageData.inputs) != 1 {
		t.Fatalf("page data calls = %d, want 1", len(pageData.inputs))
	}
	wantOwners := []string{"sys_grantee", "sys_owner"}
	if got := pageData.inputs[0]; got.AccountID != "acct_main" ||
		!reflect.DeepEqual(got.OwnerSystemAccountIDs, wantOwners) ||
		!reflect.DeepEqual(got.FieldMask, []string{"boundGroupId"}) ||
		!got.MembershipChanged || !got.FilterChanged || !got.PageChanged || got.AllScopes {
		t.Fatalf("page data input = %+v", got)
	}
	if invalidator.groupAccountIDsCalls != 1 || !reflect.DeepEqual(invalidator.runtimeReasons, []string{GatewayRuntimeReason}) {
		t.Fatalf("invalidations = %+v", invalidator)
	}
}

func TestServiceBindSelfForcesActorScope(t *testing.T) {
	store := &bindingStoreStub{result: bindingStoreResult("", "group_new")}
	service := NewService(Options{Store: store})

	_, err := service.Bind(t.Context(), BindInput{
		ActorSystemAccountID: "sys_self",
		ActorRole:            "admin",
		SystemAccountID:      "sys_other",
		SelfOnly:             true,
		AccountID:            "acct_main",
		GroupID:              "group_new",
	})
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	if store.input.EffectiveSystemAccountID != "sys_self" || store.input.CanAccessAll {
		t.Fatalf("store scope = %+v", store.input)
	}
}

func TestServiceBindAdminWithoutFilterUsesAllOwnerScopes(t *testing.T) {
	store := &bindingStoreStub{result: bindingStoreResult("", "group_new")}
	service := NewService(Options{Store: store})

	_, err := service.Bind(t.Context(), BindInput{
		ActorSystemAccountID: "sys_admin", ActorRole: "super_admin", AccountID: "acct_main", GroupID: "group_new",
	})
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	if store.input.EffectiveSystemAccountID != "" || !store.input.CanAccessAll {
		t.Fatalf("store scope = %+v", store.input)
	}
}

func TestServiceBindMapsUnavailableTargetToNodeMessage(t *testing.T) {
	service := NewService(Options{Store: &bindingStoreStub{ok: false}})
	_, err := service.Bind(t.Context(), BindInput{
		ActorSystemAccountID: "sys_self", ActorRole: "user", SelfOnly: true, AccountID: "acct_missing", GroupID: "group_new",
	})
	if !errors.Is(err, ErrBindingRejected) || err.Error() != "账户不存在、授权已失效或分组不可用" {
		t.Fatalf("Bind() error = %v", err)
	}
}

func TestServiceBindInvalidationFailuresDoNotRollBackSavedBinding(t *testing.T) {
	wantErr := errors.New("redis unavailable")
	store := &bindingStoreStub{result: bindingStoreResult("", "group_new")}
	service := NewService(Options{
		Store:                      store,
		GranteeReader:              bindingGranteeReaderStub{err: wantErr},
		PageDataPublisher:          &bindingPageDataStub{err: wantErr},
		RuntimeInvalidator:         &bindingInvalidatorStub{runtimeErr: wantErr},
		GroupAccountIDsInvalidator: &bindingInvalidatorStub{groupAccountIDsErr: wantErr},
	})

	if _, err := service.Bind(t.Context(), BindInput{
		ActorSystemAccountID: "sys_self", ActorRole: "user", SelfOnly: true, AccountID: "acct_main", GroupID: "group_new",
	}); err != nil {
		t.Fatalf("Bind() error = %v, want saved binding", err)
	}
}

type bindingStoreStub struct {
	input  port.ManagementAccountGroupBindingInput
	result port.ManagementAccountGroupBindingResult
	ok     bool
	err    error
}

func (s *bindingStoreStub) BindManagementAccountGroup(_ context.Context, input port.ManagementAccountGroupBindingInput) (port.ManagementAccountGroupBindingResult, bool, error) {
	s.input = input
	if s.err != nil {
		return port.ManagementAccountGroupBindingResult{}, false, s.err
	}
	if !s.ok && s.result.Account.ID == "" {
		return port.ManagementAccountGroupBindingResult{}, false, nil
	}
	return s.result, true, nil
}

func bindingStoreResult(previousGroupID, groupID string) port.ManagementAccountGroupBindingResult {
	return port.ManagementAccountGroupBindingResult{
		Account: port.ManagementAccountGroupBindingAccount{
			ID: "acct_main", SystemAccountID: "sys_owner", Name: "主账号", ProviderCode: "openai",
			ProviderProtocolProfileID: "profile_openai", ProtocolCode: "openai", ProtocolVersion: "v1",
			Type: "api_key", Status: "active", ClientCompatibility: "openai_standard",
			BoundGroupID: groupID, BoundGroupName: "主分组", Schedulable: true,
			ConcurrencyLimit: 20, Priority: 3, SuperPriorityEnabled: true, HealthCheckModel: "gpt-5.5",
		},
		PreviousGroupID: previousGroupID,
	}
}

type bindingGranteeReaderStub struct {
	ids []string
	err error
}

func (s bindingGranteeReaderStub) ListAccountAuthorizationGranteeIDs(context.Context, string) ([]string, error) {
	return s.ids, s.err
}

type bindingPageDataStub struct {
	inputs []accountpagedata.ChangeInput
	err    error
}

func (s *bindingPageDataStub) PublishAccountStaticChange(_ context.Context, input accountpagedata.ChangeInput) error {
	s.inputs = append(s.inputs, input)
	return s.err
}

func (*bindingPageDataStub) PublishAccountRuntimeChange(context.Context, accountpagedata.ChangeInput) error {
	return nil
}

type bindingInvalidatorStub struct {
	runtimeReasons       []string
	groupAccountIDsCalls int
	runtimeErr           error
	groupAccountIDsErr   error
}

func (s *bindingInvalidatorStub) InvalidateGatewayRuntime(_ context.Context, reason string) error {
	s.runtimeReasons = append(s.runtimeReasons, reason)
	return s.runtimeErr
}

func (s *bindingInvalidatorStub) InvalidateGroupAccountIDsCache(context.Context) error {
	s.groupAccountIDsCalls++
	return s.groupAccountIDsErr
}
