package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/accountpagedata"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

type pageDataCacheWriterStub struct {
	mu     sync.Mutex
	keys   []string
	values []string
	ttls   []time.Duration
	err    error
}

func (s *pageDataCacheWriterStub) SetPageDataVersion(_ context.Context, key, value string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	s.values = append(s.values, value)
	s.ttls = append(s.ttls, ttl)
	return s.err
}

func TestAccountsStaticResetPublisherAdapterBuildsAndPublishesEveryEvent(t *testing.T) {
	core := &pageDataCorePublisherStub{
		events: []redisplatform.PageDataChangeEvent{
			{EventID: "event-1", OwnerSystemAccountIDs: []string{"owner-a"}},
		},
	}
	cache := &pageDataCacheWriterStub{}
	adapter := accountsStaticResetPublisherAdapter{publisher: core, cache: cache, logger: slog.Default(), redisNamespace: "prod"}

	if err := adapter.PublishAccountsStaticReset(t.Context(), []string{"owner-b", "owner-a"}, false); err != nil {
		t.Fatalf("PublishAccountsStaticReset() error = %v", err)
	}
	if !reflect.DeepEqual(core.ownerIDs, []string{"owner-b", "owner-a"}) || core.allScopes {
		t.Fatalf("build input owners=%#v allScopes=%v", core.ownerIDs, core.allScopes)
	}
	if got, want := core.publishedIDs, []string{"event-1", "event-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("published ids = %#v, want %#v", got, want)
	}
	if got, want := len(cache.keys), 2; got != want {
		t.Fatalf("cache version writes = %d, want %d", got, want)
	}
	keys := append([]string(nil), cache.keys...)
	sort.Strings(keys)
	wantKeys := []string{"juhe-ai:prod:cache-version:page_data_accounts_options", "juhe-ai:prod:cache-version:page_data_accounts_static"}
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("cache keys = %#v, want %#v", keys, wantKeys)
	}
	if cache.ttls[0] != 30*24*time.Hour || cache.ttls[1] != 30*24*time.Hour {
		t.Fatalf("cache TTLs = %#v", cache.ttls)
	}
}

func TestAccountsStaticResetPublisherAdapterContinuesEventsWhenCacheWriteFails(t *testing.T) {
	cause := errors.New("cache down")
	core := &pageDataCorePublisherStub{events: []redisplatform.PageDataChangeEvent{{EventID: "event-1"}}}
	adapter := accountsStaticResetPublisherAdapter{publisher: core, cache: &pageDataCacheWriterStub{err: cause}, logger: slog.Default(), redisNamespace: "prod"}
	if err := adapter.PublishAccountsStaticReset(t.Context(), []string{"owner-a"}, false); err != nil {
		t.Fatalf("cache failure must not fail event publish: %v", err)
	}
	if got, want := len(core.publishedIDs), 2; got != want {
		t.Fatalf("published events = %d, want %d", got, want)
	}
}

func TestAccountsStaticResetPublisherAdapterPreservesPublishCause(t *testing.T) {
	cause := errors.New("redis down")
	core := &pageDataCorePublisherStub{
		events:     []redisplatform.PageDataChangeEvent{{EventID: "event-1"}},
		publishErr: cause,
	}
	adapter := accountsStaticResetPublisherAdapter{publisher: core}

	if err := adapter.PublishAccountsStaticReset(t.Context(), []string{"owner-a"}, false); !errors.Is(err, cause) {
		t.Fatalf("PublishAccountsStaticReset() error = %v, want cause", err)
	}
}

func TestAccountsStaticResetPublisherAdapterAttemptsOptionsAfterStaticFailure(t *testing.T) {
	cause := errors.New("static reset failed")
	core := &pageDataCorePublisherStub{buildErr: cause}
	adapter := accountsStaticResetPublisherAdapter{publisher: core}

	err := adapter.PublishAccountsStaticReset(t.Context(), []string{"owner-a"}, false)
	if !errors.Is(err, cause) {
		t.Fatalf("PublishAccountsStaticReset() error = %v, want %v", err, cause)
	}
	domains := append([]string(nil), core.domains...)
	sort.Strings(domains)
	if got, want := domains, []string{"accounts.options", "accounts.static"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attempted domains = %#v, want %#v", got, want)
	}
}

func TestAccountsStaticResetPublisherAdapterStartsBothDomainsConcurrently(t *testing.T) {
	core := newBlockingPageDataCorePublisherStub()
	adapter := accountsStaticResetPublisherAdapter{publisher: core}
	done := make(chan error, 1)
	go func() {
		done <- adapter.PublishAccountsStaticReset(t.Context(), []string{"owner-a"}, false)
	}()

	<-core.staticStarted
	select {
	case <-core.optionsStarted:
		close(core.releaseStatic)
	case <-time.After(2 * time.Second):
		close(core.releaseStatic)
		t.Fatal("accounts.options did not start while accounts.static was blocked")
	}
	if err := <-done; err != nil {
		t.Fatalf("PublishAccountsStaticReset() error = %v", err)
	}
}

func TestAccountsStaticResetPublisherAdapterPublishesRequestedDomainReset(t *testing.T) {
	core := &pageDataCorePublisherStub{
		events: []redisplatform.PageDataChangeEvent{{EventID: "event-1"}},
	}
	cache := &pageDataCacheWriterStub{}
	adapter := accountsStaticResetPublisherAdapter{
		publisher: core, cache: cache, logger: slog.Default(), redisNamespace: "prod",
	}

	if err := adapter.PublishPageDataReset(t.Context(), "groups.static", nil, true); err != nil {
		t.Fatalf("PublishPageDataReset() error = %v", err)
	}
	if got, want := core.domains, []string{"groups.static"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("build domains = %#v, want %#v", got, want)
	}
	if !core.allScopes {
		t.Fatal("build allScopes = false, want true")
	}
	if got, want := cache.keys, []string{"juhe-ai:prod:cache-version:page_data_groups_static"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cache keys = %#v, want %#v", got, want)
	}
}

func TestAccountsStaticResetPublisherAdapterPublishesAccountStaticChangeAndDependentResets(t *testing.T) {
	core := &pageDataCorePublisherStub{
		upsertEvent: redisplatform.PageDataChangeEvent{EventID: "account-event"},
		events:      []redisplatform.PageDataChangeEvent{{EventID: "reset-event"}},
	}
	cache := &pageDataCacheWriterStub{}
	adapter := accountsStaticResetPublisherAdapter{publisher: core, cache: cache, logger: slog.Default(), redisNamespace: "prod"}
	input := accountpagedata.ChangeInput{
		AccountID: "account-1", OwnerSystemAccountIDs: []string{"owner-b", "owner-a"},
		Operation: accountpagedata.OperationUpsert, FieldMask: []string{"tags"}, FilterChanged: true, PageChanged: true,
	}

	if err := adapter.PublishAccountStaticChange(t.Context(), input); err != nil {
		t.Fatalf("PublishAccountStaticChange() error = %v", err)
	}
	if got, want := core.upsertInput, (redisplatform.AccountChangeInput{
		AccountID: "account-1", OwnerSystemAccountIDs: []string{"owner-b", "owner-a"},
		FieldMask: []string{"tags"}, FilterChanged: true, PageChanged: true,
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("upsert input = %#v, want %#v", got, want)
	}
	domains := append([]string(nil), core.domains...)
	sort.Strings(domains)
	wantDomains := []string{"accounts.options", "stats.accountUsage", "stats.aiPerformance", "stats.overview"}
	if !reflect.DeepEqual(domains, wantDomains) {
		t.Fatalf("reset domains = %#v, want %#v", domains, wantDomains)
	}
	published := append([]string(nil), core.publishedIDs...)
	sort.Strings(published)
	if got, want := published, []string{"account-event", "reset-event", "reset-event", "reset-event", "reset-event"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("published ids = %#v, want %#v", got, want)
	}
	if got, want := len(cache.keys), 5; got != want {
		t.Fatalf("cache version writes = %d, want %d", got, want)
	}
}

func TestAccountsStaticResetPublisherAdapterAttemptsEveryAccountChangeDomainAfterFailures(t *testing.T) {
	cause := errors.New("page data unavailable")
	core := &pageDataCorePublisherStub{buildErr: cause}
	adapter := accountsStaticResetPublisherAdapter{publisher: core}

	err := adapter.PublishAccountStaticChange(t.Context(), accountpagedata.ChangeInput{
		AccountID: "account-1", OwnerSystemAccountIDs: []string{"owner-1"}, FieldMask: []string{"tags"},
		Operation: accountpagedata.OperationUpsert,
	})
	if !errors.Is(err, cause) {
		t.Fatalf("PublishAccountStaticChange() error = %v, want %v", err, cause)
	}
	if core.upsertInput.AccountID != "account-1" {
		t.Fatalf("accounts.static upsert was not attempted: %#v", core.upsertInput)
	}
	domains := append([]string(nil), core.domains...)
	sort.Strings(domains)
	wantDomains := []string{"accounts.options", "stats.accountUsage", "stats.aiPerformance", "stats.overview"}
	if !reflect.DeepEqual(domains, wantDomains) {
		t.Fatalf("attempted reset domains = %#v, want %#v", domains, wantDomains)
	}
}

func TestAccountsStaticResetPublisherAdapterPublishesStaticDeleteAndRuntimeUpsert(t *testing.T) {
	core := &pageDataCorePublisherStub{
		deleteEvent:  redisplatform.PageDataChangeEvent{EventID: "delete-event"},
		runtimeEvent: redisplatform.PageDataChangeEvent{EventID: "runtime-event"},
		events:       []redisplatform.PageDataChangeEvent{{EventID: "reset-event"}},
	}
	adapter := accountsStaticResetPublisherAdapter{publisher: core}
	deleteInput := accountpagedata.ChangeInput{
		AccountID: "account-1", Operation: accountpagedata.OperationDelete,
		OwnerSystemAccountIDs: []string{"owner-1"}, MembershipChanged: true, OrderChanged: true,
		FilterChanged: true, PageChanged: true,
	}
	if err := adapter.PublishAccountStaticChange(t.Context(), deleteInput); err != nil {
		t.Fatalf("PublishAccountStaticChange(delete) error = %v", err)
	}
	if core.deleteInput.AccountID != "account-1" || core.upsertInput.AccountID != "" {
		t.Fatalf("static delete builders: delete=%#v upsert=%#v", core.deleteInput, core.upsertInput)
	}

	core.domains = nil
	if err := adapter.PublishAccountRuntimeChange(t.Context(), accountpagedata.ChangeInput{
		AccountID: "account-1", Operation: accountpagedata.OperationUpsert,
		OwnerSystemAccountIDs: []string{"owner-1"}, FieldMask: []string{"status", "schedulable"},
	}); err != nil {
		t.Fatalf("PublishAccountRuntimeChange() error = %v", err)
	}
	if core.runtimeInput.AccountID != "account-1" {
		t.Fatalf("runtime upsert input = %#v", core.runtimeInput)
	}
	if got, want := core.domains, []string{"accounts.options"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime reset domains = %#v, want %#v", got, want)
	}
}

func TestServerWiresPageDataPublisherWithRootRedisNamespace(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := string(source)
	if !strings.Contains(text, "newAccountsStaticResetPublisher(stateRedis, cacheRedis, cfg.RedisNamespace)") {
		t.Fatal("server must construct page data publisher with state and cache Redis plus the root namespace")
	}
	if strings.Contains(text, `newAccountsStaticResetPublisher(stateRedis, cfg.RedisNamespace+":state")`) {
		t.Fatal("server must not pass the state client namespace to the page data publisher")
	}
	groupBlock := sourceBlockBetween(t, text,
		"groupService := managementgroups.NewServiceWithOptions",
		"accountService := managementaccounts.NewService",
	)
	if !strings.Contains(groupBlock, "PageDataPublisher:       accountsStaticResetPublisher") {
		t.Fatal("server must inject the page data publisher into management groups")
	}
	accountBlock := sourceBlockBetween(t, text,
		"accountService := managementaccounts.NewServiceWithOptions",
		"accountTestOptionsService := managementaccounttestoptions.NewServiceWithOptions",
	)
	for _, want := range []string{
		"Store:             store",
		"GranteeReader:     store",
		"PageDataPublisher: accountsStaticResetPublisher",
		"Logger:            logger",
	} {
		if !strings.Contains(accountBlock, want) {
			t.Fatalf("management accounts wiring missing %q", want)
		}
	}
	systemAccountBlock := sourceBlockBetween(t, text,
		"systemAccountService := managementsystemaccounts.NewServiceWithOptions",
		"systemTeamService := managementsystemteams.NewServiceWithOptions",
	)
	if !strings.Contains(systemAccountBlock, "PageDataPublisher:        accountsStaticResetPublisher") {
		t.Fatal("server must inject the page data publisher into management system accounts")
	}
	systemTeamBlock := sourceBlockBetween(t, text,
		"systemTeamService := managementsystemteams.NewServiceWithOptions",
		"authorizationService := managementauthorizations.NewServiceWithOptions",
	)
	if !strings.Contains(systemTeamBlock, "Publisher:                accountsStaticResetPublisher") {
		t.Fatal("server must inject the page data publisher into management system teams")
	}
	providerModelBlock := sourceBlockBetween(t, text,
		"providerModelService := managementprovidermodels.NewServiceWithOptions",
		"routeStrategyService := managementroutestrategies.NewServiceWithOptions",
	)
	if !strings.Contains(providerModelBlock, "PageDataPublisher: accountsStaticResetPublisher") {
		t.Fatal("server must inject the page data publisher into management provider models")
	}
}

func sourceBlockBetween(t *testing.T, source string, startMarker string, endMarker string) string {
	t.Helper()
	start := strings.Index(source, startMarker)
	if start < 0 {
		t.Fatalf("server source missing block start %q", startMarker)
	}
	relativeEnd := strings.Index(source[start:], endMarker)
	if relativeEnd < 0 {
		t.Fatalf("server source missing block end %q after %q", endMarker, startMarker)
	}
	return source[start : start+relativeEnd]
}

type pageDataCorePublisherStub struct {
	mu           sync.Mutex
	ownerIDs     []string
	domains      []string
	allScopes    bool
	events       []redisplatform.PageDataChangeEvent
	upsertInput  redisplatform.AccountChangeInput
	upsertEvent  redisplatform.PageDataChangeEvent
	deleteInput  redisplatform.AccountChangeInput
	deleteEvent  redisplatform.PageDataChangeEvent
	runtimeInput redisplatform.AccountChangeInput
	runtimeEvent redisplatform.PageDataChangeEvent
	publishedIDs []string
	buildErr     error
	publishErr   error
}

func (s *pageDataCorePublisherStub) NewAccountStaticUpsertEvent(input redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsertInput = input
	return s.upsertEvent, s.buildErr
}

func (s *pageDataCorePublisherStub) NewAccountStaticDeleteEvent(input redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteInput = input
	return s.deleteEvent, s.buildErr
}

func (s *pageDataCorePublisherStub) NewAccountRuntimeUpsertEvent(input redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeInput = input
	return s.runtimeEvent, s.buildErr
}

type blockingPageDataCorePublisherStub struct {
	staticStarted  chan struct{}
	optionsStarted chan struct{}
	releaseStatic  chan struct{}
}

func newBlockingPageDataCorePublisherStub() *blockingPageDataCorePublisherStub {
	return &blockingPageDataCorePublisherStub{
		staticStarted: make(chan struct{}), optionsStarted: make(chan struct{}), releaseStatic: make(chan struct{}),
	}
}

func (s *blockingPageDataCorePublisherStub) NewRangeResetEvents(domain string, _ []string, _ bool) ([]redisplatform.PageDataChangeEvent, error) {
	switch domain {
	case "accounts.static":
		close(s.staticStarted)
		<-s.releaseStatic
	case "accounts.options":
		close(s.optionsStarted)
	}
	return nil, nil
}

func (s *blockingPageDataCorePublisherStub) NewAccountStaticUpsertEvent(redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	return redisplatform.PageDataChangeEvent{}, nil
}

func (s *blockingPageDataCorePublisherStub) NewAccountStaticDeleteEvent(redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	return redisplatform.PageDataChangeEvent{}, nil
}

func (s *blockingPageDataCorePublisherStub) NewAccountRuntimeUpsertEvent(redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	return redisplatform.PageDataChangeEvent{}, nil
}

func (s *blockingPageDataCorePublisherStub) Publish(_ context.Context, _ redisplatform.PageDataChangeEvent) error {
	return nil
}

func (s *pageDataCorePublisherStub) NewRangeResetEvents(domain string, ownerIDs []string, allScopes bool) ([]redisplatform.PageDataChangeEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.domains = append(s.domains, domain)
	s.ownerIDs = append([]string(nil), ownerIDs...)
	s.allScopes = allScopes
	return append([]redisplatform.PageDataChangeEvent(nil), s.events...), s.buildErr
}

func (s *pageDataCorePublisherStub) Publish(_ context.Context, event redisplatform.PageDataChangeEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishedIDs = append(s.publishedIDs, event.EventID)
	return s.publishErr
}
