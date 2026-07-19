package app

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

func TestRecoveringPageDataPublisherMarksAndRecoversFailedDomain(t *testing.T) {
	cause := errors.New("redis unavailable")
	core := &recoveringPageDataCoreStub{publishErrors: []error{cause, nil}}
	store := newPageDataDirtyDomainStoreStub()
	publisher := newRecoveringPageDataCorePublisher(core, store, slog.Default())

	if err := publisher.Publish(t.Context(), redisplatform.PageDataChangeEvent{EventID: "event-1", Domain: "accounts.static"}); !errors.Is(err, cause) {
		t.Fatalf("Publish() error = %v, want %v", err, cause)
	}
	if got, want := store.markedDomains(), []string{"accounts.static"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("marked domains = %#v, want %#v", got, want)
	}
	if publisher.recoverOnce(t.Context()) {
		t.Fatal("recoverOnce() failed = true, want false")
	}
	if got, want := core.resetDomains(), []string{"accounts.static"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reset domains = %#v, want %#v", got, want)
	}
	if got, want := core.resetAllScopes(), []bool{true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reset allScopes = %#v, want %#v", got, want)
	}
	if got, want := store.clearedGenerations(), []int64{1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleared generations = %#v, want %#v", got, want)
	}
	if publisher.hasPending() {
		t.Fatal("publisher still has pending domains after successful recovery")
	}
}

func TestRecoveringPageDataPublisherLoadsPersistentDirtyDomains(t *testing.T) {
	core := &recoveringPageDataCoreStub{}
	store := newPageDataDirtyDomainStoreStub()
	store.rows = []pageDataDirtyDomain{{Domain: "accounts.runtime", Generation: 7}}
	publisher := newRecoveringPageDataCorePublisher(core, store, slog.Default())
	if err := publisher.loadPersistent(t.Context()); err != nil {
		t.Fatalf("loadPersistent() error = %v", err)
	}
	if publisher.recoverOnce(t.Context()) {
		t.Fatal("recoverOnce() failed = true, want false")
	}
	if got, want := store.clearedGenerations(), []int64{7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleared generations = %#v, want %#v", got, want)
	}
}

func TestRecoveringPageDataPublisherDoesNotClearNewerGeneration(t *testing.T) {
	core := &recoveringPageDataCoreStub{}
	store := newPageDataDirtyDomainStoreStub()
	store.rows = []pageDataDirtyDomain{{Domain: "accounts.options", Generation: 3}}
	store.beforeClear = func() {
		store.mu.Lock()
		store.rows = []pageDataDirtyDomain{{Domain: "accounts.options", Generation: 4}}
		store.mu.Unlock()
	}
	publisher := newRecoveringPageDataCorePublisher(core, store, slog.Default())
	if err := publisher.loadPersistent(t.Context()); err != nil {
		t.Fatalf("loadPersistent() error = %v", err)
	}
	if failed := publisher.recoverOnce(t.Context()); !failed {
		t.Fatal("recoverOnce() failed = false, want true while newer generation remains dirty")
	}
	if got := publisher.pendingGeneration("accounts.options"); got != 4 {
		t.Fatalf("pending generation = %d, want 4", got)
	}
}

func TestRecoveringPageDataPublisherKeepsVolatileFailureAfterOlderReset(t *testing.T) {
	resetStarted := make(chan struct{})
	releaseReset := make(chan struct{})
	redisCause := errors.New("redis unavailable")
	core := &recoveringPageDataCoreStub{}
	core.publishHook = func(ctx context.Context, event redisplatform.PageDataChangeEvent) error {
		if event.EventID == "reset-accounts.static" {
			select {
			case <-resetStarted:
			default:
				close(resetStarted)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-releaseReset:
				return nil
			}
		}
		return redisCause
	}
	store := newPageDataDirtyDomainStoreStub()
	store.rows = []pageDataDirtyDomain{{Domain: "accounts.static", Generation: 1}}
	store.markErr = errors.New("postgres unavailable")
	publisher := newRecoveringPageDataCorePublisher(core, store, slog.Default())
	if err := publisher.loadPersistent(t.Context()); err != nil {
		t.Fatalf("loadPersistent() error = %v", err)
	}
	recovered := make(chan bool, 1)
	go func() { recovered <- publisher.recoverOnce(t.Context()) }()
	<-resetStarted
	if err := publisher.Publish(t.Context(), redisplatform.PageDataChangeEvent{EventID: "new-event", Domain: "accounts.static"}); !errors.Is(err, redisCause) {
		t.Fatalf("Publish() error = %v, want %v", err, redisCause)
	}
	close(releaseReset)
	if failed := <-recovered; failed {
		t.Fatal("older recoverOnce() failed = true, want false")
	}
	if !publisher.hasPending() {
		t.Fatal("new volatile dirty state was deleted by the older generation recovery")
	}
}

func TestRecoveringPageDataPublisherMarksAfterRequestCancellation(t *testing.T) {
	cause := errors.New("redis unavailable")
	core := &recoveringPageDataCoreStub{publishErrors: []error{cause}}
	store := newPageDataDirtyDomainStoreStub()
	publisher := newRecoveringPageDataCorePublisher(core, store, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := publisher.Publish(ctx, redisplatform.PageDataChangeEvent{EventID: "event-1", Domain: "accounts.static"}); !errors.Is(err, cause) {
		t.Fatalf("Publish() error = %v, want %v", err, cause)
	}
	if store.markContextCanceled {
		t.Fatal("dirty-domain mark inherited canceled request context")
	}
}

func TestPageDataRecoveryDelayIsBounded(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	got := make([]time.Duration, 0, len(want))
	for round := range want {
		got = append(got, pageDataRecoveryDelay(round))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recovery delays = %#v, want %#v", got, want)
	}
}

func TestRecoveringPageDataPublisherStartLoadsAndRecovers(t *testing.T) {
	core := &recoveringPageDataCoreStub{}
	store := newPageDataDirtyDomainStoreStub()
	store.rows = []pageDataDirtyDomain{{Domain: "groups.static", Generation: 9}}
	store.clearNotify = make(chan struct{}, 1)
	publisher := newRecoveringPageDataCorePublisher(core, store, slog.Default())
	publisher.Start(t.Context())
	t.Cleanup(publisher.Close)

	select {
	case <-store.clearNotify:
	case <-time.After(2 * time.Second):
		t.Fatal("background recovery did not clear the startup dirty domain")
	}
	if got, want := core.resetDomains(), []string{"groups.static"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reset domains = %#v, want %#v", got, want)
	}
}

func TestRecoveringPageDataPublisherDiscoversDirtyDomainWrittenByAnotherProcess(t *testing.T) {
	core := &recoveringPageDataCoreStub{}
	store := newPageDataDirtyDomainStoreStub()
	store.clearNotify = make(chan struct{}, 1)
	publisher := newRecoveringPageDataCorePublisher(core, store, slog.Default())
	publisher.scanInterval = 10 * time.Millisecond
	publisher.Start(t.Context())
	t.Cleanup(publisher.Close)

	time.Sleep(20 * time.Millisecond)
	store.mu.Lock()
	store.rows = []pageDataDirtyDomain{{Domain: "teams.options", Generation: 11}}
	store.mu.Unlock()

	select {
	case <-store.clearNotify:
	case <-time.After(2 * time.Second):
		t.Fatal("background recovery did not discover the externally persisted dirty domain")
	}
	if got, want := core.resetDomains(), []string{"teams.options"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reset domains = %#v, want %#v", got, want)
	}
}

func TestRecoveringPageDataPublisherWakeDoesNotBypassFailureBackoff(t *testing.T) {
	core := &recoveringPageDataCoreStub{
		defaultPublishErr: errors.New("redis unavailable"),
		publishNotify:     make(chan struct{}, 8),
	}
	store := newPageDataDirtyDomainStoreStub()
	store.rows = []pageDataDirtyDomain{{Domain: "accounts.static", Generation: 1}}
	publisher := newRecoveringPageDataCorePublisher(core, store, slog.Default())
	publisher.recoveryDelay = func(int) time.Duration { return 150 * time.Millisecond }
	publisher.Start(t.Context())
	t.Cleanup(publisher.Close)

	select {
	case <-core.publishNotify:
	case <-time.After(2 * time.Second):
		t.Fatal("background recovery did not make its first publish attempt")
	}
	for range 16 {
		publisher.signal()
	}
	select {
	case <-core.publishNotify:
		t.Fatal("wake signal bypassed the failure backoff")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-core.publishNotify:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("background recovery did not retry after the configured backoff")
	}
}

func TestRecoveringPageDataPublisherDiscoversExternalDomainWhileAnotherDomainKeepsFailing(t *testing.T) {
	core := &recoveringPageDataCoreStub{
		defaultPublishErr: errors.New("redis unavailable"),
		buildNotify:       make(chan string, 16),
	}
	store := newPageDataDirtyDomainStoreStub()
	store.rows = []pageDataDirtyDomain{{Domain: "accounts.static", Generation: 1}}
	publisher := newRecoveringPageDataCorePublisher(core, store, slog.Default())
	publisher.recoveryDelay = func(int) time.Duration { return 20 * time.Millisecond }
	publisher.Start(t.Context())
	t.Cleanup(publisher.Close)

	select {
	case domain := <-core.buildNotify:
		if domain != "accounts.static" {
			t.Fatalf("first recovery domain = %q, want accounts.static", domain)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background recovery did not attempt the initial dirty domain")
	}
	store.mu.Lock()
	store.rows = append(store.rows, pageDataDirtyDomain{Domain: "teams.options", Generation: 1})
	store.mu.Unlock()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case domain := <-core.buildNotify:
			if domain == "teams.options" {
				return
			}
		case <-deadline:
			t.Fatal("recovery did not discover the external dirty domain while another domain kept failing")
		}
	}
}

func TestRecoveringPageDataPublisherWakeForcesPersistentScan(t *testing.T) {
	core := &recoveringPageDataCoreStub{}
	store := newPageDataDirtyDomainStoreStub()
	store.clearNotify = make(chan struct{}, 1)
	publisher := newRecoveringPageDataCorePublisher(core, store, slog.Default())
	publisher.Start(t.Context())
	t.Cleanup(publisher.Close)

	time.Sleep(20 * time.Millisecond)
	store.mu.Lock()
	store.rows = []pageDataDirtyDomain{{Domain: "routeStrategies.options", Generation: 5}}
	store.mu.Unlock()
	for range 16 {
		publisher.signal()
	}

	select {
	case <-store.clearNotify:
	case <-time.After(2 * time.Second):
		t.Fatal("wake did not force a scan for externally persisted dirty domains")
	}
}

type recoveringPageDataCoreStub struct {
	mu                sync.Mutex
	publishErrors     []error
	defaultPublishErr error
	publishNotify     chan struct{}
	buildNotify       chan string
	publishHook       func(context.Context, redisplatform.PageDataChangeEvent) error
	builtDomains      []string
	builtAllScopes    []bool
}

func (s *recoveringPageDataCoreStub) NewAccountStaticUpsertEvent(redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	return redisplatform.PageDataChangeEvent{EventID: "static-upsert", Domain: "accounts.static"}, nil
}
func (s *recoveringPageDataCoreStub) NewAccountStaticDeleteEvent(redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	return redisplatform.PageDataChangeEvent{EventID: "static-delete", Domain: "accounts.static"}, nil
}
func (s *recoveringPageDataCoreStub) NewAccountRuntimeUpsertEvent(redisplatform.AccountChangeInput) (redisplatform.PageDataChangeEvent, error) {
	return redisplatform.PageDataChangeEvent{EventID: "runtime-upsert", Domain: "accounts.runtime"}, nil
}
func (s *recoveringPageDataCoreStub) NewRangeResetEvents(domain string, _ []string, allScopes bool) ([]redisplatform.PageDataChangeEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.builtDomains = append(s.builtDomains, domain)
	s.builtAllScopes = append(s.builtAllScopes, allScopes)
	if s.buildNotify != nil {
		select {
		case s.buildNotify <- domain:
		default:
		}
	}
	return []redisplatform.PageDataChangeEvent{{EventID: "reset-" + domain, Domain: domain}}, nil
}
func (s *recoveringPageDataCoreStub) Publish(ctx context.Context, event redisplatform.PageDataChangeEvent) error {
	s.mu.Lock()
	if s.publishNotify != nil {
		select {
		case s.publishNotify <- struct{}{}:
		default:
		}
	}
	hook := s.publishHook
	if hook != nil {
		s.mu.Unlock()
		return hook(ctx, event)
	}
	defer s.mu.Unlock()
	if len(s.publishErrors) == 0 {
		return s.defaultPublishErr
	}
	err := s.publishErrors[0]
	s.publishErrors = s.publishErrors[1:]
	return err
}
func (s *recoveringPageDataCoreStub) resetDomains() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.builtDomains...)
}
func (s *recoveringPageDataCoreStub) resetAllScopes() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.builtAllScopes...)
}

type pageDataDirtyDomainStoreStub struct {
	mu                  sync.Mutex
	rows                []pageDataDirtyDomain
	marks               []string
	clears              []int64
	beforeClear         func()
	clearNotify         chan struct{}
	markContextCanceled bool
	markErr             error
}

func newPageDataDirtyDomainStoreStub() *pageDataDirtyDomainStoreStub {
	return &pageDataDirtyDomainStoreStub{}
}
func (s *pageDataDirtyDomainStoreStub) ListPageDataDirtyDomains(context.Context) ([]pageDataDirtyDomain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]pageDataDirtyDomain(nil), s.rows...), nil
}
func (s *pageDataDirtyDomainStoreStub) MarkPageDataDomainDirty(ctx context.Context, domain string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markContextCanceled = ctx.Err() != nil
	s.marks = append(s.marks, domain)
	if s.markErr != nil {
		return 0, s.markErr
	}
	for index := range s.rows {
		if s.rows[index].Domain == domain {
			s.rows[index].Generation++
			return s.rows[index].Generation, nil
		}
	}
	s.rows = append(s.rows, pageDataDirtyDomain{Domain: domain, Generation: 1})
	return 1, nil
}
func (s *pageDataDirtyDomainStoreStub) ClearPageDataDomainDirty(_ context.Context, domain string, generation int64) (bool, error) {
	if s.beforeClear != nil {
		callback := s.beforeClear
		s.beforeClear = nil
		callback()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clears = append(s.clears, generation)
	if s.clearNotify != nil {
		select {
		case s.clearNotify <- struct{}{}:
		default:
		}
	}
	for index, row := range s.rows {
		if row.Domain == domain && row.Generation == generation {
			s.rows = append(s.rows[:index], s.rows[index+1:]...)
			return true, nil
		}
	}
	return false, nil
}
func (s *pageDataDirtyDomainStoreStub) markedDomains() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.marks...)
}
func (s *pageDataDirtyDomainStoreStub) clearedGenerations() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.clears...)
}
