package managementauthorizations

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestAuthorizationGroupWritesPublishDependentPageDataResets(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	summary := Summary{
		ID: "rauthgrant_group", ResourceType: "group", ResourceID: "grp_main",
		ResourceOwnerSystemAccountID: "owner", GranteeType: "system_account", GranteeSystemAccountID: "grantee",
	}
	tests := []struct {
		name   string
		invoke func(*authorizationPageDataPublisherStub) error
	}{
		{
			name: "create",
			invoke: func(publisher *authorizationPageDataPublisherStub) error {
				service := NewServiceWithOptions(ServiceOptions{Store: &authorizationCreateStoreStub{result: summary}, Now: func() time.Time { return now }, Publisher: publisher})
				_, err := service.Create(context.Background(), CreateInput{
					ResourceType: "group", ResourceID: "grp_main", ResourceOwnerSystemAccountID: "owner",
					GranteeType: "system_account", GranteeID: "grantee", ActorSystemAccountID: "admin",
				})
				return err
			},
		},
		{
			name: "update",
			invoke: func(publisher *authorizationPageDataPublisherStub) error {
				service := NewServiceWithOptions(ServiceOptions{UpdateStore: &authorizationUpdateStoreStub{result: summary, found: true}, Now: func() time.Time { return now }, Publisher: publisher})
				_, _, err := service.Update(context.Background(), UpdateInput{AuthorizationID: summary.ID, ActorSystemAccountID: "admin", ActorRole: "admin", HasStatus: true, Status: "paused"})
				return err
			},
		},
		{
			name: "return",
			invoke: func(publisher *authorizationPageDataPublisherStub) error {
				service := NewServiceWithOptions(ServiceOptions{ReturnStore: &authorizationReturnStoreStub{result: summary, found: true}, Now: func() time.Time { return now }, Publisher: publisher})
				_, _, err := service.Return(context.Background(), ReturnInput{AuthorizationID: summary.ID, GranteeSystemAccountID: "grantee", ActorSystemAccountID: "admin"})
				return err
			},
		},
		{
			name: "return by resource",
			invoke: func(publisher *authorizationPageDataPublisherStub) error {
				service := NewServiceWithOptions(ServiceOptions{ResourceReturnStore: &authorizationResourceReturnStoreStub{result: summary, found: true}, Now: func() time.Time { return now }, Publisher: publisher})
				_, _, err := service.ReturnByResource(context.Background(), ResourceReturnInput{ResourceType: "group", ResourceID: "grp_main", GranteeSystemAccountID: "grantee", ActorSystemAccountID: "admin"})
				return err
			},
		},
		{
			name: "revoke",
			invoke: func(publisher *authorizationPageDataPublisherStub) error {
				service := NewServiceWithOptions(ServiceOptions{RevokeStore: &authorizationRevokeStoreStub{result: summary, found: true}, Now: func() time.Time { return now }, Publisher: publisher})
				_, _, err := service.Revoke(context.Background(), RevokeInput{AuthorizationID: summary.ID, ActorSystemAccountID: "admin", ActorRole: "admin"})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher := &authorizationPageDataPublisherStub{}
			if err := test.invoke(publisher); err != nil {
				t.Fatalf("write error = %v", err)
			}
			assertAuthorizationGroupPageDataResets(t, publisher, 0)
		})
	}
}

func TestAuthorizationExpirySweepPublishesGroupAndAccountPageData(t *testing.T) {
	store := &authorizationExpirySweepStoreStub{result: port.ManagementResourceAuthorizationExpirySweepResult{
		Expired: 2,
		Authorizations: []port.ManagementResourceAuthorizationExpiryFanout{
			{AuthorizationID: "auth_account", ResourceType: "account", ResourceID: "acct_main", ResourceOwnerSystemAccountID: "owner", GranteeType: "system_account", GranteeSystemAccountID: "grantee"},
			{AuthorizationID: "auth_group", ResourceType: "group", ResourceID: "grp_main", ResourceOwnerSystemAccountID: "owner", GranteeType: "system_account", GranteeSystemAccountID: "grantee"},
		},
	}}
	publisher := &authorizationPageDataPublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{ExpirySweepStore: store, Publisher: publisher})

	result, err := service.ExpireDue(context.Background(), ExpirySweepInput{Limit: 2})
	if err != nil || result.Expired != 2 {
		t.Fatalf("ExpireDue() result=%+v error=%v", result, err)
	}
	assertAuthorizationGroupPageDataResets(t, publisher, 1)
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if publisher.accountCalls != 1 || !reflect.DeepEqual(publisher.accountOwners, []string{"grantee", "owner"}) || publisher.accountAllScopes {
		t.Fatalf("account reset calls=%d owners=%#v allScopes=%v", publisher.accountCalls, publisher.accountOwners, publisher.accountAllScopes)
	}
}

func TestAuthorizationGroupPageDataPublishFailureDoesNotFailCommittedWrite(t *testing.T) {
	publisher := &authorizationPageDataPublisherStub{domainErr: errors.New("page data unavailable")}
	service := NewServiceWithOptions(ServiceOptions{
		RevokeStore: &authorizationRevokeStoreStub{found: true, result: Summary{ID: "auth_group", ResourceType: "group", ResourceID: "grp_main"}},
		Publisher:   publisher,
	})

	_, found, err := service.Revoke(context.Background(), RevokeInput{AuthorizationID: "auth_group", ActorSystemAccountID: "admin", ActorRole: "admin"})
	if err != nil || !found {
		t.Fatalf("Revoke() found=%v error=%v", found, err)
	}
	assertAuthorizationGroupPageDataResets(t, publisher, 0)
}

func TestAuthorizationAccountWritePublishesStatsPageDataResets(t *testing.T) {
	publisher := &authorizationPageDataPublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{Publisher: publisher})

	service.publishAuthorizationPageDataAfterCommit(context.Background(), Summary{
		ID: "auth_account", ResourceType: "account", ResourceID: "acct_main",
		ResourceOwnerSystemAccountID: "owner", GranteeType: "system_account", GranteeSystemAccountID: "grantee",
	})

	assertAuthorizationPageDataDomains(t, publisher, []string{"stats.overview", "stats.accountUsage", "stats.aiPerformance"}, []string{"grantee", "owner"}, false, 1)
}

func TestAuthorizationAccountOnlyExpirySweepPublishesStatsPageDataResets(t *testing.T) {
	store := &authorizationExpirySweepStoreStub{result: port.ManagementResourceAuthorizationExpirySweepResult{
		Expired: 1,
		Authorizations: []port.ManagementResourceAuthorizationExpiryFanout{{
			AuthorizationID: "auth_account", ResourceType: "account", ResourceID: "acct_main",
			ResourceOwnerSystemAccountID: "owner", GranteeType: "system_account", GranteeSystemAccountID: "grantee",
		}},
	}}
	publisher := &authorizationPageDataPublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{ExpirySweepStore: store, Publisher: publisher})

	result, err := service.ExpireDue(context.Background(), ExpirySweepInput{Limit: 1})
	if err != nil || result.Expired != 1 {
		t.Fatalf("ExpireDue() result=%+v error=%v", result, err)
	}
	assertAuthorizationPageDataDomains(t, publisher, []string{"stats.overview", "stats.accountUsage", "stats.aiPerformance"}, []string{"grantee", "owner"}, false, 1)
}

func TestAuthorizationGroupOnlyExpirySweepPublishesGlobalPageDataResets(t *testing.T) {
	store := &authorizationExpirySweepStoreStub{result: port.ManagementResourceAuthorizationExpirySweepResult{
		Expired: 1,
		Authorizations: []port.ManagementResourceAuthorizationExpiryFanout{{
			AuthorizationID: "auth_group", ResourceType: "group", ResourceID: "grp_main",
			ResourceOwnerSystemAccountID: "owner", GranteeType: "system_account", GranteeSystemAccountID: "grantee",
		}},
	}}
	publisher := &authorizationPageDataPublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{ExpirySweepStore: store, Publisher: publisher})

	result, err := service.ExpireDue(context.Background(), ExpirySweepInput{Limit: 1})
	if err != nil || result.Expired != 1 {
		t.Fatalf("ExpireDue() result=%+v error=%v", result, err)
	}
	assertAuthorizationGroupPageDataResets(t, publisher, 0)
}

func TestAuthorizationGroupPageDataSkipsFailedOrMissingWrites(t *testing.T) {
	tests := []struct {
		name    string
		service *Service
		invoke  func(*Service) error
	}{
		{
			name: "store error",
			service: NewServiceWithOptions(ServiceOptions{
				ExpirySweepStore: &authorizationExpirySweepStoreStub{err: errors.New("store failed")},
			}),
			invoke: func(service *Service) error {
				_, err := service.ExpireDue(context.Background(), ExpirySweepInput{Limit: 1})
				return err
			},
		},
		{
			name: "missing write",
			service: NewServiceWithOptions(ServiceOptions{
				RevokeStore: &authorizationRevokeStoreStub{},
			}),
			invoke: func(service *Service) error {
				_, found, err := service.Revoke(context.Background(), RevokeInput{AuthorizationID: "missing", ActorSystemAccountID: "admin", ActorRole: "admin"})
				if err == nil && found {
					return errors.New("unexpected found result")
				}
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher := &authorizationPageDataPublisherStub{}
			test.service.pageDataPublisher = publisher
			_ = test.invoke(test.service)
			publisher.mu.Lock()
			defer publisher.mu.Unlock()
			if publisher.accountCalls != 0 || len(publisher.domainCalls) != 0 {
				t.Fatalf("publisher accountCalls=%d domainCalls=%#v, want none", publisher.accountCalls, publisher.domainCalls)
			}
		})
	}
}

type authorizationPageDataResetCall struct {
	domain      string
	owners      []string
	allScopes   bool
	contextErr  error
	hasDeadline bool
}

type authorizationPageDataPublisherStub struct {
	mu               sync.Mutex
	accountCalls     int
	accountOwners    []string
	accountAllScopes bool
	domainCalls      []authorizationPageDataResetCall
	domainErr        error
}

func (s *authorizationPageDataPublisherStub) PublishAccountsStaticReset(_ context.Context, owners []string, allScopes bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountCalls++
	s.accountOwners = append([]string(nil), owners...)
	s.accountAllScopes = allScopes
	return nil
}

func (s *authorizationPageDataPublisherStub) PublishPageDataReset(ctx context.Context, domain string, owners []string, allScopes bool) error {
	_, hasDeadline := ctx.Deadline()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.domainCalls = append(s.domainCalls, authorizationPageDataResetCall{
		domain: domain, owners: append([]string(nil), owners...), allScopes: allScopes, contextErr: ctx.Err(), hasDeadline: hasDeadline,
	})
	return s.domainErr
}

func assertAuthorizationGroupPageDataResets(t *testing.T, publisher *authorizationPageDataPublisherStub, wantAccountCalls int) {
	t.Helper()
	assertAuthorizationPageDataDomains(t, publisher, []string{"groups.static", "stats.overview", "stats.accountUsage", "stats.aiPerformance"}, nil, true, wantAccountCalls)
}

func assertAuthorizationPageDataDomains(
	t *testing.T,
	publisher *authorizationPageDataPublisherStub,
	wantDomains []string,
	wantOwners []string,
	wantAllScopes bool,
	wantAccountCalls int,
) {
	t.Helper()
	publisher.mu.Lock()
	calls := append([]authorizationPageDataResetCall(nil), publisher.domainCalls...)
	accountCalls := publisher.accountCalls
	publisher.mu.Unlock()
	if accountCalls != wantAccountCalls {
		t.Fatalf("account reset calls = %d, want %d", accountCalls, wantAccountCalls)
	}
	if len(calls) != len(wantDomains) {
		t.Fatalf("group page data calls = %#v, want domains %#v", calls, wantDomains)
	}
	byDomain := make(map[string]authorizationPageDataResetCall, len(calls))
	for _, call := range calls {
		byDomain[call.domain] = call
	}
	for _, domain := range wantDomains {
		call, ok := byDomain[domain]
		if !ok || !reflect.DeepEqual(call.owners, wantOwners) || call.allScopes != wantAllScopes || call.contextErr != nil || !call.hasDeadline {
			t.Fatalf("page data call for %q = %+v found=%v, want owners=%#v allScopes=%v detached deadline", domain, call, ok, wantOwners, wantAllScopes)
		}
	}
}
