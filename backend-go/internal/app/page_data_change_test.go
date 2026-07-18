package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

type pageDataCacheWriterStub struct {
	keys   []string
	values []string
	ttls   []time.Duration
	err    error
}

func (s *pageDataCacheWriterStub) SetPageDataVersion(_ context.Context, key, value string, ttl time.Duration) error {
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
	if cache.keys[0] != "juhe-ai:prod:cache-version:page_data_accounts_static" || cache.keys[1] != "juhe-ai:prod:cache-version:page_data_accounts_options" {
		t.Fatalf("cache keys = %#v", cache.keys)
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
	ownerIDs     []string
	domains      []string
	allScopes    bool
	events       []redisplatform.PageDataChangeEvent
	publishedIDs []string
	buildErr     error
	publishErr   error
}

func (s *pageDataCorePublisherStub) NewRangeResetEvents(domain string, ownerIDs []string, allScopes bool) ([]redisplatform.PageDataChangeEvent, error) {
	s.domains = append(s.domains, domain)
	s.ownerIDs = append([]string(nil), ownerIDs...)
	s.allScopes = allScopes
	return append([]redisplatform.PageDataChangeEvent(nil), s.events...), s.buildErr
}

func (s *pageDataCorePublisherStub) Publish(_ context.Context, event redisplatform.PageDataChangeEvent) error {
	s.publishedIDs = append(s.publishedIDs, event.EventID)
	return s.publishErr
}
