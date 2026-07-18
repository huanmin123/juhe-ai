package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

func TestAccountsStaticResetPublisherAdapterBuildsAndPublishesEveryEvent(t *testing.T) {
	core := &pageDataCorePublisherStub{
		events: []redisplatform.PageDataChangeEvent{
			{EventID: "event-1", OwnerSystemAccountIDs: []string{"owner-a"}},
			{EventID: "event-2", OwnerSystemAccountIDs: []string{"owner-b"}},
		},
	}
	adapter := accountsStaticResetPublisherAdapter{publisher: core}

	if err := adapter.PublishAccountsStaticReset(t.Context(), []string{"owner-b", "owner-a"}, false); err != nil {
		t.Fatalf("PublishAccountsStaticReset() error = %v", err)
	}
	if !reflect.DeepEqual(core.ownerIDs, []string{"owner-b", "owner-a"}) || core.allScopes {
		t.Fatalf("build input owners=%#v allScopes=%v", core.ownerIDs, core.allScopes)
	}
	if got, want := core.publishedIDs, []string{"event-1", "event-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("published ids = %#v, want %#v", got, want)
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

func TestServerWiresPageDataPublisherWithRootRedisNamespace(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := string(source)
	if !strings.Contains(text, "newAccountsStaticResetPublisher(stateRedis, cfg.RedisNamespace)") {
		t.Fatal("server must construct page data publisher with the root Redis namespace")
	}
	if strings.Contains(text, `newAccountsStaticResetPublisher(stateRedis, cfg.RedisNamespace+":state")`) {
		t.Fatal("server must not pass the state client namespace to the page data publisher")
	}
}

type pageDataCorePublisherStub struct {
	ownerIDs     []string
	allScopes    bool
	events       []redisplatform.PageDataChangeEvent
	publishedIDs []string
	buildErr     error
	publishErr   error
}

func (s *pageDataCorePublisherStub) NewAccountsStaticResetEvents(ownerIDs []string, allScopes bool) ([]redisplatform.PageDataChangeEvent, error) {
	s.ownerIDs = append([]string(nil), ownerIDs...)
	s.allScopes = allScopes
	return append([]redisplatform.PageDataChangeEvent(nil), s.events...), s.buildErr
}

func (s *pageDataCorePublisherStub) Publish(_ context.Context, event redisplatform.PageDataChangeEvent) error {
	s.publishedIDs = append(s.publishedIDs, event.EventID)
	return s.publishErr
}
