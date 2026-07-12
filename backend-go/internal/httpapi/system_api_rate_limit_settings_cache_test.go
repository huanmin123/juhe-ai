package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementauth"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

func TestRouterSystemAPIRateLimitMiddlewaresShareSnapshotAcrossFourBuckets(t *testing.T) {
	reader := &mutableSystemAPIRateLimitReader{
		settings: port.SystemAPIRateLimitSettings{
			IPReadPerMinute:          11,
			IPReadBurstPer10Seconds:  12,
			IPWritePerMinute:         21,
			IPWriteBurstPer10Seconds: 22,
			UserReadPerMinute:        31,
			UserWritePerMinute:       41,
		},
	}
	ipLimiter := &systemAPIIPRateLimiterRecorder{}
	userLimiter := &systemAPIAuthenticatedRateLimiterRecorder{}
	router := newSystemAPIRateLimitSettingsTestRouter(
		t,
		reader,
		nil,
		nil,
		ipLimiter,
		userLimiter,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeData(w, http.StatusOK, map[string]bool{"ok": true})
		}),
	)

	serveSystemAPIRateLimitSettingsTestRequest(t, router, http.MethodGet, "/__aisys__/api/providers", "")
	serveSystemAPIRateLimitSettingsTestRequest(t, router, http.MethodPatch, "/__aisys__/api/settings", `{}`)

	if reader.calls != 1 {
		t.Fatalf("settings reader calls = %d, want one shared snapshot read", reader.calls)
	}
	if len(ipLimiter.settings) != 2 {
		t.Fatalf("IP limiter calls = %d, want 2", len(ipLimiter.settings))
	}
	if ipLimiter.settings[0] != (SystemAPIIPRateLimitSettings{PerMinute: 11, BurstPer10Seconds: 12}) {
		t.Fatalf("IP read settings = %+v", ipLimiter.settings[0])
	}
	if ipLimiter.settings[1] != (SystemAPIIPRateLimitSettings{PerMinute: 21, BurstPer10Seconds: 22}) {
		t.Fatalf("IP write settings = %+v", ipLimiter.settings[1])
	}
	if got := userLimiter.limits; len(got) != 2 || got[0] != 31 || got[1] != 41 {
		t.Fatalf("user limits = %v, want [31 41]", got)
	}
}

func TestRouterSystemAPIRateLimitSettingsPatchClearsSnapshotWithoutAdvancingClock(t *testing.T) {
	reader := &mutableSystemAPIRateLimitReader{
		settings: port.SystemAPIRateLimitSettings{
			IPReadPerMinute:          11,
			IPReadBurstPer10Seconds:  12,
			IPWritePerMinute:         21,
			IPWriteBurstPer10Seconds: 22,
			UserReadPerMinute:        31,
			UserWritePerMinute:       41,
		},
	}
	cache := NewSystemAPIRateLimitSettingsCache(nil)
	ipLimiter := &systemAPIIPRateLimiterRecorder{}
	userLimiter := &systemAPIAuthenticatedRateLimiterRecorder{}
	updateHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reader.settings = port.SystemAPIRateLimitSettings{
			IPReadPerMinute:          111,
			IPReadBurstPer10Seconds:  112,
			IPWritePerMinute:         121,
			IPWriteBurstPer10Seconds: 122,
			UserReadPerMinute:        131,
			UserWritePerMinute:       141,
		}
		cache.ClearSystemAPIRateLimitSettingsCache()
		writeData(w, http.StatusOK, map[string]bool{"updated": true})
	})
	router := newSystemAPIRateLimitSettingsTestRouter(
		t,
		reader,
		cache,
		nil,
		ipLimiter,
		userLimiter,
		updateHandler,
	)

	serveSystemAPIRateLimitSettingsTestRequest(t, router, http.MethodGet, "/__aisys__/api/providers", "")
	serveSystemAPIRateLimitSettingsTestRequest(t, router, http.MethodPatch, "/__aisys__/api/settings", `{}`)
	serveSystemAPIRateLimitSettingsTestRequest(t, router, http.MethodGet, "/__aisys__/api/providers", "")
	serveSystemAPIRateLimitSettingsTestRequest(t, router, http.MethodPatch, "/__aisys__/api/settings", `{}`)

	if reader.calls != 2 {
		t.Fatalf("settings reader calls = %d, want one prewarm read and one immediate reload", reader.calls)
	}
	if got := ipLimiter.settings[2]; got != (SystemAPIIPRateLimitSettings{PerMinute: 111, BurstPer10Seconds: 112}) {
		t.Fatalf("IP read settings after PATCH = %+v", got)
	}
	if got := ipLimiter.settings[3]; got != (SystemAPIIPRateLimitSettings{PerMinute: 121, BurstPer10Seconds: 122}) {
		t.Fatalf("IP write settings after PATCH = %+v", got)
	}
	if got := userLimiter.limits; len(got) != 4 || got[2] != 131 || got[3] != 141 {
		t.Fatalf("user limits after PATCH = %v, want read/write 131/141", got)
	}
}

func TestSystemAPIRateLimitSettingsCacheReloadsAcrossRuntimesOnSharedVersionChange(t *testing.T) {
	versionReader := &mutableSystemAPIRateLimitSettingsVersionReader{version: "version-a"}
	reader := &mutableSystemAPIRateLimitReader{
		settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600},
	}
	firstRuntime := NewSystemAPIRateLimitSettingsCache(versionReader)
	secondRuntime := NewSystemAPIRateLimitSettingsCache(versionReader)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	if _, err := firstRuntime.current(context.Background(), reader, now); err != nil {
		t.Fatalf("first runtime prewarm: %v", err)
	}
	cached, err := secondRuntime.current(context.Background(), reader, now)
	if err != nil {
		t.Fatalf("second runtime prewarm: %v", err)
	}
	if cached.IPReadPerMinute != 600 {
		t.Fatalf("second runtime cached settings = %+v", cached)
	}

	reader.settings = port.SystemAPIRateLimitSettings{IPReadPerMinute: 900}
	versionReader.version = "version-b"

	refreshed, err := secondRuntime.current(context.Background(), reader, now)
	if err != nil {
		t.Fatalf("second runtime refresh: %v", err)
	}
	if refreshed.IPReadPerMinute != 900 {
		t.Fatalf("second runtime refreshed settings = %+v, want IP read 900", refreshed)
	}
	if reader.calls != 3 {
		t.Fatalf("settings reader calls = %d, want two prewarms and one version-triggered reload", reader.calls)
	}
}

func TestSystemAPIRateLimitSettingsCacheClearWhileReadBlockedDoesNotRefillStaleSettings(t *testing.T) {
	reader := newBlockingSystemAPIRateLimitReader(
		port.SystemAPIRateLimitSettings{IPReadPerMinute: 600},
		port.SystemAPIRateLimitSettings{IPReadPerMinute: 900},
	)
	cache := NewSystemAPIRateLimitSettingsCache(nil)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	result := make(chan systemAPIRateLimitSettingsResult, 1)

	go func() {
		settings, err := cache.current(context.Background(), reader, now)
		result <- systemAPIRateLimitSettingsResult{settings: settings, err: err}
	}()

	waitForSystemAPIRateLimitTestSignal(t, reader.started, "blocked PostgreSQL settings read")
	cache.ClearSystemAPIRateLimitSettingsCache()
	close(reader.release)

	got := waitForSystemAPIRateLimitSettingsResult(t, result)
	if got.err != nil {
		t.Fatalf("current() error = %v", got.err)
	}
	if got.settings.IPReadPerMinute != 900 {
		t.Fatalf("current() settings = %+v, want post-Clear settings", got.settings)
	}
	if reader.Calls() != 2 {
		t.Fatalf("settings reader calls = %d, want stale read discarded and retried", reader.Calls())
	}

	cached, err := cache.current(context.Background(), reader, now)
	if err != nil {
		t.Fatalf("cached current(): %v", err)
	}
	if cached.IPReadPerMinute != 900 || reader.Calls() != 2 {
		t.Fatalf("cached settings = %+v, reader calls = %d; stale read refilled cache", cached, reader.Calls())
	}
}

func TestSystemAPIRateLimitSettingsCacheRetriesWhenVersionChangesDuringRead(t *testing.T) {
	versionReader := newSequenceSystemAPIRateLimitSettingsVersionReader("version-a", "version-b", "version-b")
	reader := newSequenceSystemAPIRateLimitReader(
		port.SystemAPIRateLimitSettings{IPReadPerMinute: 600},
		port.SystemAPIRateLimitSettings{IPReadPerMinute: 900},
	)
	cache := NewSystemAPIRateLimitSettingsCache(versionReader)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	settings, err := cache.current(context.Background(), reader, now)
	if err != nil {
		t.Fatalf("current(): %v", err)
	}
	if settings.IPReadPerMinute != 900 {
		t.Fatalf("settings = %+v, want value read after version-b", settings)
	}
	if reader.Calls() != 2 {
		t.Fatalf("settings reader calls = %d, want retry after version change", reader.Calls())
	}
	if versionReader.Calls() != 3 {
		t.Fatalf("version reader calls = %d, want initial and post-read checks for both attempts", versionReader.Calls())
	}
}

func TestRouterSystemAPIRateLimitUserLimiterKeepsRequestSnapshotAfterIPLimiterClearsCache(t *testing.T) {
	oldSettings := port.SystemAPIRateLimitSettings{
		IPReadPerMinute:         11,
		IPReadBurstPer10Seconds: 12,
		UserReadPerMinute:       31,
	}
	newSettings := port.SystemAPIRateLimitSettings{
		IPReadPerMinute:         111,
		IPReadBurstPer10Seconds: 112,
		UserReadPerMinute:       131,
	}
	reader := newSynchronizedSystemAPIRateLimitReader(oldSettings)
	cache := NewSystemAPIRateLimitSettingsCache(nil)
	ipLimiter := &systemAPIIPRateLimiterHookRecorder{
		hook: func() {
			reader.SetSettings(newSettings)
			cache.ClearSystemAPIRateLimitSettingsCache()
		},
	}
	userLimiter := &systemAPIAuthenticatedRateLimiterRecorder{}
	router := newSystemAPIRateLimitSettingsTestRouter(
		t,
		reader,
		cache,
		nil,
		ipLimiter,
		userLimiter,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeData(w, http.StatusOK, map[string]bool{"updated": true})
		}),
	)

	serveSystemAPIRateLimitSettingsTestRequest(t, router, http.MethodGet, "/__aisys__/api/providers", "")
	serveSystemAPIRateLimitSettingsTestRequest(t, router, http.MethodGet, "/__aisys__/api/providers", "")

	if got := ipLimiter.Settings(); len(got) != 2 ||
		got[0] != (SystemAPIIPRateLimitSettings{PerMinute: 11, BurstPer10Seconds: 12}) ||
		got[1] != (SystemAPIIPRateLimitSettings{PerMinute: 111, BurstPer10Seconds: 112}) {
		t.Fatalf("IP limiter settings = %+v", got)
	}
	if got := userLimiter.limits; len(got) != 2 || got[0] != 31 || got[1] != 131 {
		t.Fatalf("user limiter settings = %v, want current request 31 then next request 131", got)
	}
	if reader.Calls() != 2 {
		t.Fatalf("settings reader calls = %d, want one read per request around Clear", reader.Calls())
	}
}

func TestSystemAPIRateLimitSettingsCacheBlockedReadHonorsCancellationWithoutBlockingClear(t *testing.T) {
	reader := newContextBlockingSystemAPIRateLimitReader()
	cache := NewSystemAPIRateLimitSettingsCache(nil)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan systemAPIRateLimitSettingsResult, 1)

	go func() {
		settings, err := cache.current(ctx, reader, now)
		result <- systemAPIRateLimitSettingsResult{settings: settings, err: err}
	}()

	waitForSystemAPIRateLimitTestSignal(t, reader.started, "context-blocked PostgreSQL settings read")
	clearDone := make(chan struct{})
	go func() {
		cache.ClearSystemAPIRateLimitSettingsCache()
		close(clearDone)
	}()
	waitForSystemAPIRateLimitTestSignal(t, clearDone, "cache Clear while PostgreSQL read is blocked")

	cancel()
	got := waitForSystemAPIRateLimitSettingsResult(t, result)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("current() error = %v, want context.Canceled", got.err)
	}

	healthyReader := newSynchronizedSystemAPIRateLimitReader(
		port.SystemAPIRateLimitSettings{IPReadPerMinute: 777},
	)
	settings, err := cache.current(context.Background(), healthyReader, now)
	if err != nil {
		t.Fatalf("current() after cancellation: %v", err)
	}
	if settings.IPReadPerMinute != 777 {
		t.Fatalf("settings after cancellation = %+v", settings)
	}
}

func TestSystemAPIRateLimitSettingsCacheVersionFailureDoesNotUseStaleSnapshot(t *testing.T) {
	versionReader := &mutableSystemAPIRateLimitSettingsVersionReader{version: "version-a"}
	reader := &mutableSystemAPIRateLimitReader{
		settings: port.SystemAPIRateLimitSettings{IPReadPerMinute: 600},
	}
	cache := NewSystemAPIRateLimitSettingsCache(versionReader)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	if _, err := cache.current(context.Background(), reader, now); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	versionReader.err = errors.New("redis unavailable")

	if _, err := cache.current(context.Background(), reader, now); !errors.Is(err, versionReader.err) {
		t.Fatalf("current() error = %v, want Redis dependency error", err)
	}
	if reader.calls != 1 {
		t.Fatalf("settings reader calls = %d, want stale snapshot rejected before PostgreSQL read", reader.calls)
	}
}

func TestRouterSystemAPIRateLimitSettingsVersionFailureKeepsDependencyFailureSafe(t *testing.T) {
	reader := &mutableSystemAPIRateLimitReader{
		settings: port.SystemAPIRateLimitSettings{
			IPReadPerMinute:         600,
			IPReadBurstPer10Seconds: 120,
			UserReadPerMinute:       300,
		},
	}
	versionReader := &mutableSystemAPIRateLimitSettingsVersionReader{
		err: errors.New("redis unavailable"),
	}
	ipLimiter := &systemAPIIPRateLimiterRecorder{}
	userLimiter := &systemAPIAuthenticatedRateLimiterRecorder{}
	router := newSystemAPIRateLimitSettingsTestRouter(
		t,
		reader,
		nil,
		versionReader,
		ipLimiter,
		userLimiter,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeData(w, http.StatusOK, map[string]bool{"updated": true})
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/__aisys__/api/providers", nil)
	req.RemoteAddr = "203.0.113.9:43123"
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
	}
	if reader.calls != 0 || len(ipLimiter.settings) != 0 || len(userLimiter.limits) != 0 {
		t.Fatalf(
			"dependency failure continued: reader=%d ip=%d user=%d",
			reader.calls,
			len(ipLimiter.settings),
			len(userLimiter.limits),
		)
	}
}

func TestRedisSystemAPIRateLimitSettingsVersionReader(t *testing.T) {
	tests := []struct {
		name        string
		raw         []byte
		err         error
		wantVersion string
		wantErr     bool
	}{
		{name: "value", raw: []byte(" version-a "), wantVersion: "version-a"},
		{name: "missing", err: redisplatform.ErrNotFound},
		{name: "failure", err: errors.New("redis unavailable"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &systemAPIRateLimitSettingsVersionStoreStub{raw: tt.raw, err: tt.err}
			reader, err := NewRedisSystemAPIRateLimitSettingsVersionReader(store, "juhe-ai")
			if err != nil {
				t.Fatalf("construct version reader: %v", err)
			}
			version, err := reader.SystemAPIRateLimitSettingsVersion(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("version error = %v, wantErr %v", err, tt.wantErr)
			}
			if version != tt.wantVersion {
				t.Fatalf("version = %q, want %q", version, tt.wantVersion)
			}
			if store.key != "juhe-ai:juhe-ai:cache-version:settings:system" {
				t.Fatalf("version key = %q", store.key)
			}
		})
	}
}

func newSystemAPIRateLimitSettingsTestRouter(
	t *testing.T,
	reader port.SystemAPIRateLimitReader,
	cache SystemAPIRateLimitSettingsCache,
	versionReader SystemAPIRateLimitSettingsVersionReader,
	ipLimiter SystemAPIIPRateLimiter,
	userLimiter SystemAPIAuthenticatedRateLimiter,
	updateHandler http.Handler,
) http.Handler {
	t.Helper()
	authenticator := &systemAPIRateLimitSettingsAuthenticatorStub{
		context: managementauth.Context{
			SystemAccountID: "sys_admin",
			Username:        "admin",
			Role:            "admin",
			SessionID:       "sess_admin",
		},
	}
	return NewRouter(RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		Logger:                                  slog.New(slog.NewTextHandler(io.Discard, nil)),
		SystemAPIRateLimitReader:                reader,
		SystemAPIRateLimitSettingsCache:         cache,
		SystemAPIRateLimitSettingsVersionReader: versionReader,
		SystemAPIIPRateLimiter:                  ipLimiter,
		SystemAPIAuthenticatedRateLimiter:       userLimiter,
		ManagementAPIAuthMiddleware:             NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:        NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementProvidersHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeData(w, http.StatusOK, []string{})
		}),
		ManagementSystemSettingsUpdateHandler: updateHandler,
	})
}

func serveSystemAPIRateLimitSettingsTestRequest(
	t *testing.T,
	router http.Handler,
	method string,
	path string,
	body string,
) {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.RemoteAddr = "203.0.113.9:43123"
	req.Header.Set("Cookie", "juhe_ai_session=session-token")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s status = %d; body = %s", method, path, rec.Code, rec.Body.String())
	}
}

type mutableSystemAPIRateLimitReader struct {
	settings port.SystemAPIRateLimitSettings
	err      error
	calls    int
}

func (r *mutableSystemAPIRateLimitReader) SystemAPIRateLimitSettings(
	context.Context,
) (port.SystemAPIRateLimitSettings, error) {
	r.calls++
	return r.settings, r.err
}

type systemAPIRateLimitSettingsResult struct {
	settings port.SystemAPIRateLimitSettings
	err      error
}

type blockingSystemAPIRateLimitReader struct {
	mu       sync.Mutex
	settings []port.SystemAPIRateLimitSettings
	calls    int
	started  chan struct{}
	release  chan struct{}
}

func newBlockingSystemAPIRateLimitReader(
	settings ...port.SystemAPIRateLimitSettings,
) *blockingSystemAPIRateLimitReader {
	return &blockingSystemAPIRateLimitReader{
		settings: settings,
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (r *blockingSystemAPIRateLimitReader) SystemAPIRateLimitSettings(
	ctx context.Context,
) (port.SystemAPIRateLimitSettings, error) {
	r.mu.Lock()
	call := r.calls
	r.calls++
	settings := r.settings[min(call, len(r.settings)-1)]
	r.mu.Unlock()

	if call == 0 {
		close(r.started)
		select {
		case <-r.release:
		case <-ctx.Done():
			return port.SystemAPIRateLimitSettings{}, ctx.Err()
		}
	}
	return settings, nil
}

func (r *blockingSystemAPIRateLimitReader) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type sequenceSystemAPIRateLimitReader struct {
	mu       sync.Mutex
	settings []port.SystemAPIRateLimitSettings
	calls    int
}

func newSequenceSystemAPIRateLimitReader(
	settings ...port.SystemAPIRateLimitSettings,
) *sequenceSystemAPIRateLimitReader {
	return &sequenceSystemAPIRateLimitReader{settings: settings}
}

func (r *sequenceSystemAPIRateLimitReader) SystemAPIRateLimitSettings(
	context.Context,
) (port.SystemAPIRateLimitSettings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	settings := r.settings[min(r.calls, len(r.settings)-1)]
	r.calls++
	return settings, nil
}

func (r *sequenceSystemAPIRateLimitReader) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type synchronizedSystemAPIRateLimitReader struct {
	mu       sync.Mutex
	settings port.SystemAPIRateLimitSettings
	calls    int
}

func newSynchronizedSystemAPIRateLimitReader(
	settings port.SystemAPIRateLimitSettings,
) *synchronizedSystemAPIRateLimitReader {
	return &synchronizedSystemAPIRateLimitReader{settings: settings}
}

func (r *synchronizedSystemAPIRateLimitReader) SystemAPIRateLimitSettings(
	context.Context,
) (port.SystemAPIRateLimitSettings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.settings, nil
}

func (r *synchronizedSystemAPIRateLimitReader) SetSettings(settings port.SystemAPIRateLimitSettings) {
	r.mu.Lock()
	r.settings = settings
	r.mu.Unlock()
}

func (r *synchronizedSystemAPIRateLimitReader) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type sequenceSystemAPIRateLimitSettingsVersionReader struct {
	mu       sync.Mutex
	versions []string
	calls    int
}

func newSequenceSystemAPIRateLimitSettingsVersionReader(
	versions ...string,
) *sequenceSystemAPIRateLimitSettingsVersionReader {
	return &sequenceSystemAPIRateLimitSettingsVersionReader{versions: versions}
}

func (r *sequenceSystemAPIRateLimitSettingsVersionReader) SystemAPIRateLimitSettingsVersion(
	context.Context,
) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	version := r.versions[min(r.calls, len(r.versions)-1)]
	r.calls++
	return version, nil
}

func (r *sequenceSystemAPIRateLimitSettingsVersionReader) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type contextBlockingSystemAPIRateLimitReader struct {
	started chan struct{}
	once    sync.Once
}

func newContextBlockingSystemAPIRateLimitReader() *contextBlockingSystemAPIRateLimitReader {
	return &contextBlockingSystemAPIRateLimitReader{started: make(chan struct{})}
}

func (r *contextBlockingSystemAPIRateLimitReader) SystemAPIRateLimitSettings(
	ctx context.Context,
) (port.SystemAPIRateLimitSettings, error) {
	r.once.Do(func() {
		close(r.started)
	})
	<-ctx.Done()
	return port.SystemAPIRateLimitSettings{}, ctx.Err()
}

type mutableSystemAPIRateLimitSettingsVersionReader struct {
	version string
	err     error
	calls   int
}

func (r *mutableSystemAPIRateLimitSettingsVersionReader) SystemAPIRateLimitSettingsVersion(
	context.Context,
) (string, error) {
	r.calls++
	return r.version, r.err
}

type systemAPIIPRateLimiterRecorder struct {
	settings []SystemAPIIPRateLimitSettings
}

func (r *systemAPIIPRateLimiterRecorder) AllowSystemAPIIP(
	_ context.Context,
	_ string,
	settings SystemAPIIPRateLimitSettings,
) (SystemAPIRateLimitDecision, error) {
	r.settings = append(r.settings, settings)
	return SystemAPIRateLimitDecision{Allowed: true}, nil
}

type systemAPIIPRateLimiterHookRecorder struct {
	mu       sync.Mutex
	settings []SystemAPIIPRateLimitSettings
	once     sync.Once
	hook     func()
}

func (r *systemAPIIPRateLimiterHookRecorder) AllowSystemAPIIP(
	_ context.Context,
	_ string,
	settings SystemAPIIPRateLimitSettings,
) (SystemAPIRateLimitDecision, error) {
	r.mu.Lock()
	r.settings = append(r.settings, settings)
	r.mu.Unlock()
	r.once.Do(r.hook)
	return SystemAPIRateLimitDecision{Allowed: true}, nil
}

func (r *systemAPIIPRateLimiterHookRecorder) Settings() []SystemAPIIPRateLimitSettings {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]SystemAPIIPRateLimitSettings(nil), r.settings...)
}

type systemAPIAuthenticatedRateLimiterRecorder struct {
	limits []int
}

func (r *systemAPIAuthenticatedRateLimiterRecorder) AllowSystemAPIAuthenticated(
	_ context.Context,
	_ string,
	limit int,
) (SystemAPIRateLimitDecision, error) {
	r.limits = append(r.limits, limit)
	return SystemAPIRateLimitDecision{Allowed: true}, nil
}

type systemAPIRateLimitSettingsVersionStoreStub struct {
	raw []byte
	err error
	key string
}

type systemAPIRateLimitSettingsAuthenticatorStub struct {
	context managementauth.Context
}

func (s *systemAPIRateLimitSettingsAuthenticatorStub) AuthenticateCookie(
	context.Context,
	string,
) (managementauth.Context, error) {
	return s.context, nil
}

func (s *systemAPIRateLimitSettingsAuthenticatorStub) AuthenticateCookieAndTouch(
	context.Context,
	string,
) (managementauth.Context, error) {
	return s.context, nil
}

func (s *systemAPIRateLimitSettingsVersionStoreStub) GetRaw(
	_ context.Context,
	key string,
) ([]byte, error) {
	s.key = key
	return s.raw, s.err
}

func waitForSystemAPIRateLimitTestSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForSystemAPIRateLimitSettingsResult(
	t *testing.T,
	result <-chan systemAPIRateLimitSettingsResult,
) systemAPIRateLimitSettingsResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for rate limit settings result")
		return systemAPIRateLimitSettingsResult{}
	}
}
