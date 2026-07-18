package managementprovidermodels

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceSetDefaultHealthCheckModelPublishesScopedPageDataResets(t *testing.T) {
	tests := []struct {
		name      string
		actorRole string
		owners    []string
		allScopes bool
	}{
		{name: "personal", actorRole: "user", owners: []string{"sys_user"}},
		{name: "system", actorRole: "admin", allScopes: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &providerModelStoreStub{
				providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
				catalog: []port.ManagementProviderModelCatalogItem{{
					ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active", Mode: "text",
					SupportedAPIProtocols: []string{"responses"},
				}},
			}
			publisher := &providerModelPageDataPublisherStub{}
			service := NewServiceWithOptions(ServiceOptions{Store: store, PageDataPublisher: publisher})

			_, err := service.SetDefaultHealthCheckModel(context.Background(), DefaultHealthCheckModelInput{
				ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: test.actorRole, Model: "gpt-5.5",
			})
			if err != nil {
				t.Fatalf("SetDefaultHealthCheckModel() error = %v", err)
			}
			assertProviderModelPageDataResets(t, publisher, test.owners, test.allScopes)
		})
	}
}

func TestServiceCreateCustomModelPublishesPersonalPageDataResets(t *testing.T) {
	price := 1.25
	store := &providerModelStoreStub{providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}}}
	publisher := &providerModelPageDataPublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{
		Store: store, PageDataPublisher: publisher, NewID: func(prefix string) string { return prefix + "_fixed" },
	})

	_, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
		ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user",
		Fields: CustomModelMutation{Model: OptionalString{Set: true, Value: "custom-chat"}, InputUSDPer1M: OptionalFloat{Set: true, Value: &price}},
	})
	if err != nil {
		t.Fatalf("CreateCustomModel() error = %v", err)
	}
	assertProviderModelPageDataResets(t, publisher, []string{"sys_user"}, false)
}

func TestServiceUpdateBuiltInModelPublishesGlobalPageDataResets(t *testing.T) {
	price := 4.0
	store := &providerModelStoreStub{catalog: []port.ManagementProviderModelCatalogItem{{
		ID: "provider_model_gpt_real", ProviderCode: "gpt", Model: "gpt-real", Scope: "built_in", Status: "active",
	}}}
	publisher := &providerModelPageDataPublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{Store: store, PageDataPublisher: publisher})

	_, err := service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
		ProviderCode: "gpt", ID: "provider_model_gpt_real", ActorSystemAccountID: "sys_admin", ActorRole: "admin",
		Fields: CustomModelMutation{InputUSDPer1M: OptionalFloat{Set: true, Value: &price}},
	})
	if err != nil {
		t.Fatalf("UpdateCustomModel() error = %v", err)
	}
	assertProviderModelPageDataResets(t, publisher, nil, true)
}

func TestServicePublishesPersonalPageDataResetsBeforePostCommitCleanupFailure(t *testing.T) {
	price := 1.25
	existing := port.ManagementProviderModelCatalogItem{
		ID: "custom_model_1", ProviderCode: "gpt", Model: "custom-chat", Scope: "personal",
		SystemAccountID: "sys_user", Status: "active", InputUSDPer1M: &price,
	}
	tests := []struct {
		name string
		run  func(*Service) error
	}{
		{
			name: "update",
			run: func(service *Service) error {
				_, err := service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
					ProviderCode: "gpt", ID: "custom_model_1", ActorSystemAccountID: "sys_user", ActorRole: "user",
					Fields: CustomModelMutation{Status: OptionalString{Set: true, Value: "disabled"}},
				})
				return err
			},
		},
		{
			name: "delete",
			run: func(service *Service) error {
				_, err := service.DeleteCustomModel(context.Background(), CustomModelDeleteInput{
					ProviderCode: "gpt", ID: "custom_model_1", ActorSystemAccountID: "sys_user", ActorRole: "user",
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cleanupErr := errors.New("clear default failed")
			store := &providerModelStoreStub{
				customByID:   map[string]port.ManagementProviderModelCatalogItem{"custom_model_1": existing},
				deleteResult: true, clearErr: cleanupErr,
			}
			publisher := &providerModelPageDataPublisherStub{}
			service := NewServiceWithOptions(ServiceOptions{Store: store, PageDataPublisher: publisher})

			err := test.run(service)
			if !errors.Is(err, cleanupErr) {
				t.Fatalf("operation error = %v, want %v", err, cleanupErr)
			}
			assertProviderModelPageDataResets(t, publisher, []string{"sys_user"}, false)
		})
	}
}

func TestServicePageDataPublishFailureDoesNotFailCommittedWrite(t *testing.T) {
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
		catalog: []port.ManagementProviderModelCatalogItem{{
			ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active", Mode: "text",
			SupportedAPIProtocols: []string{"responses"},
		}},
	}
	publisher := &providerModelPageDataPublisherStub{err: errors.New("page data unavailable")}
	service := NewServiceWithOptions(ServiceOptions{Store: store, PageDataPublisher: publisher})

	result, err := service.SetDefaultHealthCheckModel(context.Background(), DefaultHealthCheckModelInput{
		ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user", Model: "gpt-5.5",
	})
	if err != nil || result.DefaultHealthCheckModel != "gpt-5.5" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	assertProviderModelPageDataResets(t, publisher, []string{"sys_user"}, false)
}

func TestServiceDeleteCustomModelDoesNotPublishWhenNothingWasDeleted(t *testing.T) {
	existing := port.ManagementProviderModelCatalogItem{
		ID: "custom_model_1", ProviderCode: "gpt", Model: "custom-chat", Scope: "personal", SystemAccountID: "sys_user",
	}
	store := &providerModelStoreStub{customByID: map[string]port.ManagementProviderModelCatalogItem{"custom_model_1": existing}}
	publisher := &providerModelPageDataPublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{Store: store, PageDataPublisher: publisher})

	result, err := service.DeleteCustomModel(context.Background(), CustomModelDeleteInput{
		ProviderCode: "gpt", ID: "custom_model_1", ActorSystemAccountID: "sys_user", ActorRole: "user",
	})
	if err != nil || result.Deleted {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if len(publisher.calls) != 0 {
		t.Fatalf("page data calls = %#v, want none", publisher.calls)
	}
}

func TestServiceDoesNotPublishPageDataWhenPrimaryWriteFails(t *testing.T) {
	writeErr := errors.New("write failed")
	price := 1.25
	model := port.ManagementProviderModelCatalogItem{
		ID: "custom_model_1", ProviderCode: "gpt", Model: "custom-chat", Scope: "personal",
		SystemAccountID: "sys_user", Status: "active", InputUSDPer1M: &price,
	}
	tests := []struct {
		name  string
		store *providerModelStoreStub
		run   func(*Service) error
	}{
		{
			name: "set default",
			store: &providerModelStoreStub{
				providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
				catalog: []port.ManagementProviderModelCatalogItem{{
					ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active", Mode: "text",
					SupportedAPIProtocols: []string{"responses"},
				}},
				setDefaultErr: writeErr,
			},
			run: func(service *Service) error {
				_, err := service.SetDefaultHealthCheckModel(context.Background(), DefaultHealthCheckModelInput{
					ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user", Model: "gpt-5.5",
				})
				return err
			},
		},
		{
			name:  "create",
			store: &providerModelStoreStub{providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}}, saveErr: writeErr},
			run: func(service *Service) error {
				_, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
					ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user",
					Fields: CustomModelMutation{Model: OptionalString{Set: true, Value: "custom-chat"}, InputUSDPer1M: OptionalFloat{Set: true, Value: &price}},
				})
				return err
			},
		},
		{
			name:  "update",
			store: &providerModelStoreStub{customByID: map[string]port.ManagementProviderModelCatalogItem{"custom_model_1": model}, customUpdateErr: writeErr},
			run: func(service *Service) error {
				_, err := service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
					ProviderCode: "gpt", ID: "custom_model_1", ActorSystemAccountID: "sys_user", ActorRole: "user",
					Fields: CustomModelMutation{Notes: OptionalString{Set: true, Value: "updated"}},
				})
				return err
			},
		},
		{
			name:  "delete",
			store: &providerModelStoreStub{customByID: map[string]port.ManagementProviderModelCatalogItem{"custom_model_1": model}, deleteErr: writeErr},
			run: func(service *Service) error {
				_, err := service.DeleteCustomModel(context.Background(), CustomModelDeleteInput{
					ProviderCode: "gpt", ID: "custom_model_1", ActorSystemAccountID: "sys_user", ActorRole: "user",
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher := &providerModelPageDataPublisherStub{}
			err := test.run(NewServiceWithOptions(ServiceOptions{Store: test.store, PageDataPublisher: publisher}))
			if err == nil {
				t.Fatal("operation error = nil, want write failure")
			}
			publisher.mu.Lock()
			defer publisher.mu.Unlock()
			if len(publisher.calls) != 0 {
				t.Fatalf("page data calls = %#v, want none", publisher.calls)
			}
		})
	}
}

func TestServiceGlobalCustomMutationsPublishGlobalPageDataResets(t *testing.T) {
	price := 1.25
	global := port.ManagementProviderModelCatalogItem{
		ID: "custom_model_global", ProviderCode: "gpt", Model: "global-chat", Scope: "global", Status: "active", InputUSDPer1M: &price,
	}
	tests := []struct {
		name  string
		store *providerModelStoreStub
		run   func(*Service) error
	}{
		{
			name:  "create",
			store: &providerModelStoreStub{providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}}},
			run: func(service *Service) error {
				_, err := service.CreateCustomModel(context.Background(), CustomModelCreateInput{
					ProviderCode: "gpt", ActorSystemAccountID: "sys_admin", ActorRole: "admin",
					Fields: CustomModelMutation{Scope: OptionalString{Set: true, Value: "global"}, Model: OptionalString{Set: true, Value: "global-chat"}, InputUSDPer1M: OptionalFloat{Set: true, Value: &price}},
				})
				return err
			},
		},
		{
			name:  "update",
			store: &providerModelStoreStub{customByID: map[string]port.ManagementProviderModelCatalogItem{"custom_model_global": global}},
			run: func(service *Service) error {
				_, err := service.UpdateCustomModel(context.Background(), CustomModelUpdateInput{
					ProviderCode: "gpt", ID: "custom_model_global", ActorSystemAccountID: "sys_admin", ActorRole: "admin",
					Fields: CustomModelMutation{Notes: OptionalString{Set: true, Value: "updated"}},
				})
				return err
			},
		},
		{
			name:  "delete",
			store: &providerModelStoreStub{customByID: map[string]port.ManagementProviderModelCatalogItem{"custom_model_global": global}, deleteResult: true},
			run: func(service *Service) error {
				_, err := service.DeleteCustomModel(context.Background(), CustomModelDeleteInput{
					ProviderCode: "gpt", ID: "custom_model_global", ActorSystemAccountID: "sys_admin", ActorRole: "admin",
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			publisher := &providerModelPageDataPublisherStub{}
			if err := test.run(NewServiceWithOptions(ServiceOptions{Store: test.store, PageDataPublisher: publisher})); err != nil {
				t.Fatalf("operation error = %v", err)
			}
			assertProviderModelPageDataResets(t, publisher, nil, true)
		})
	}
}

func TestServicePageDataPublisherDetachesFromCanceledParentContext(t *testing.T) {
	store := &providerModelStoreStub{
		providers: map[string]port.ManagementProviderModelProvider{"gpt": {Code: "gpt", Enabled: true}},
		catalog: []port.ManagementProviderModelCatalogItem{{
			ProviderCode: "gpt", Model: "gpt-5.5", Scope: "built_in", Status: "active", Mode: "text",
			SupportedAPIProtocols: []string{"responses"},
		}},
	}
	publisher := &providerModelPageDataPublisherStub{}
	service := NewServiceWithOptions(ServiceOptions{Store: store, PageDataPublisher: publisher})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.SetDefaultHealthCheckModel(ctx, DefaultHealthCheckModelInput{
		ProviderCode: "gpt", ActorSystemAccountID: "sys_user", ActorRole: "user", Model: "gpt-5.5",
	})
	if err != nil {
		t.Fatalf("SetDefaultHealthCheckModel() error = %v", err)
	}
	assertProviderModelPageDataResets(t, publisher, []string{"sys_user"}, false)
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	for _, call := range publisher.calls {
		if call.deadlineRemaining <= pageDataPublishTimeout-time.Second || call.deadlineRemaining > pageDataPublishTimeout {
			t.Fatalf("deadline remaining = %v, want approximately %v", call.deadlineRemaining, pageDataPublishTimeout)
		}
	}
}

type providerModelPageDataResetCall struct {
	domain            string
	owners            []string
	allScopes         bool
	contextErr        error
	hasDeadline       bool
	deadlineRemaining time.Duration
}

type providerModelPageDataPublisherStub struct {
	mu    sync.Mutex
	calls []providerModelPageDataResetCall
	err   error
}

func (s *providerModelPageDataPublisherStub) PublishPageDataReset(ctx context.Context, domain string, owners []string, allScopes bool) error {
	deadline, hasDeadline := ctx.Deadline()
	deadlineRemaining := time.Duration(0)
	if hasDeadline {
		deadlineRemaining = time.Until(deadline)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, providerModelPageDataResetCall{
		domain: domain, owners: append([]string(nil), owners...), allScopes: allScopes, contextErr: ctx.Err(), hasDeadline: hasDeadline,
		deadlineRemaining: deadlineRemaining,
	})
	return s.err
}

func assertProviderModelPageDataResets(t *testing.T, publisher *providerModelPageDataPublisherStub, owners []string, allScopes bool) {
	t.Helper()
	publisher.mu.Lock()
	calls := append([]providerModelPageDataResetCall(nil), publisher.calls...)
	publisher.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("page data calls = %#v, want two domains", calls)
	}
	byDomain := make(map[string]providerModelPageDataResetCall, len(calls))
	for _, call := range calls {
		byDomain[call.domain] = call
	}
	for _, domain := range []string{"providers.catalog", "accounts.options"} {
		call, ok := byDomain[domain]
		if !ok || !slices.Equal(call.owners, owners) || call.allScopes != allScopes || call.contextErr != nil || !call.hasDeadline {
			t.Fatalf("page data call for %q = %+v found=%v, want owners=%v allScopes=%v detached deadline", domain, call, ok, owners, allScopes)
		}
	}
}
