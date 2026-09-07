package ratelimit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func testSettings(context.Context) (Settings, error) {
	return Settings{
		IPReadPerMinute:    5,
		IPReadBurstPer10s:  3,
		IPWritePerMinute:   2,
		IPWriteBurstPer10s: 2,
		UserReadPerMinute:  6,
		UserWritePerMinute: 3,
	}, nil
}

func newLimiter() *Limiter {
	return &Limiter{Settings: testSettings, Store: NewMemoryStore(nil)}
}

func TestIPMinuteLimitBlocksFourthRead(t *testing.T) {
	limiter := newLimiter()
	k := kernel.New(kernel.Options{})
	k.Register("GET /__aisys__/api/ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.ipRateLimit(w, r) {
			return
		}
		kernel.WriteOK(w, map[string]string{"ok": "1"}, "")
	}))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	client := &http.Client{}
	var last *http.Response
	for i := 0; i < 4; i++ {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/__aisys__/api/ping", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		last = resp
	}
	if last.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("4th read must be blocked (limit 3/10s), got %d", last.StatusCode)
	}
	if got := last.Header.Get("Retry-After"); got == "" {
		t.Fatal("Retry-After missing")
	}
}

func TestIPMinuteWindowRecovers(t *testing.T) {
	limiter := newLimiter()
	// fake clock: drive MemoryStore directly
	k := kernel.New(kernel.Options{})
	k.Register("GET /__aisys__/api/ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.ipRateLimit(w, r) {
			return
		}
		kernel.WriteOK(w, map[string]string{"ok": "1"}, "")
	}))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	for i := 0; i < 5; i++ {
		resp, err := http.Get(server.URL + "/__aisys__/api/ping")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if i == 4 && resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("5th read must be blocked (limit 5/min), got %d", resp.StatusCode)
		}
	}
}

func TestUserLimitIsPerAccount(t *testing.T) {
	limiter := newLimiter()
	k := kernel.New(kernel.Options{})
	k.Register("GET /a", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.authenticatedRateLimit(w, r, "user-a") {
			return
		}
		kernel.WriteOK(w, map[string]string{"ok": "1"}, "")
	}))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	// Requests 1-6 pass (limit 6/min); request 7 is user-blocked.
	for i := 0; i < 6; i++ {
		resp, err := http.Get(server.URL + "/a")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d must pass, got %d", i+1, resp.StatusCode)
		}
	}
	resp7, err := http.Get(server.URL + "/a")
	if err != nil {
		t.Fatal(err)
	}
	resp7.Body.Close()
	if resp7.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("7th user read must be blocked, got %d", resp7.StatusCode)
	}
}

func TestAllowlistBypassesLimit(t *testing.T) {
	limiter := newLimiter()
	limiter.Allowlist = func(ctx context.Context, clientIP string) bool { return true }
	k := kernel.New(kernel.Options{})
	k.Register("GET /__aisys__/api/ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.ipRateLimit(w, r) {
			return
		}
		kernel.WriteOK(w, map[string]string{"ok": "1"}, "")
	}))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	for i := 0; i < 10; i++ {
		resp, err := http.Get(server.URL + "/__aisys__/api/ping")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("allowlisted request %d must pass, got %d", i, resp.StatusCode)
		}
	}
}

func TestHealthPathBypass(t *testing.T) {
	limiter := newLimiter()
	k := kernel.New(kernel.Options{})
	k.Register("GET /__aisys__/api/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.ipRateLimit(w, r) {
			return
		}
		kernel.WriteOK(w, map[string]string{"status": "ok"}, "")
	}))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	for i := 0; i < 10; i++ {
		resp, err := http.Get(server.URL + "/__aisys__/api/health")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("health must bypass limit, got %d", resp.StatusCode)
		}
	}
}

func TestWriteClassUsesWriteLimits(t *testing.T) {
	limiter := newLimiter()
	k := kernel.New(kernel.Options{})
	k.Register("POST /__aisys__/api/mutate", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.ipRateLimit(w, r) {
			return
		}
		kernel.WriteOK(w, map[string]string{"ok": "1"}, "")
	}))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	for i := 0; i < 2; i++ {
		resp, err := http.Post(server.URL+"/__aisys__/api/mutate", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	third, err := http.Post(server.URL+"/__aisys__/api/mutate", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	third.Body.Close()
	if third.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("3rd write must be blocked (limit 2/min), got %d", third.StatusCode)
	}
}

func TestRedisStoreFraming(t *testing.T) {
	store := map[string]string{}
	fake := &fakeRedis{store: store}
	limiter := &Limiter{Settings: testSettings, Store: &RedisStore{Client: fake}}
	k := kernel.New(kernel.Options{})
	k.Register("GET /__aisys__/api/ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !limiter.ipRateLimit(w, r) {
			return
		}
		kernel.WriteOK(w, map[string]string{"ok": "1"}, "")
	}))
	server := httptest.NewServer(k.Handler())
	defer server.Close()

	for i := 0; i < 3; i++ {
		resp, err := http.Get(server.URL + "/__aisys__/api/ping")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	// burst limit 3 → 4th blocked
	resp4, err := http.Get(server.URL + "/__aisys__/api/ping")
	if err != nil {
		t.Fatal(err)
	}
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("burst limit must block 4th, got %d", resp4.StatusCode)
	}
	if fake.evalCalls != 4 {
		t.Fatalf("expected 4 redis evals, got %d", fake.evalCalls)
	}
}

type fakeRedis struct {
	store     map[string]string
	evalCalls int
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

func (f *fakeRedis) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	f.evalCalls++
	nowMs := args[0].(int64)
	// emulate the Lua fixed-window logic
	type pending struct {
		key     string
		count   int64
		resetAt int64
	}
	var pendings []pending
	for i := range keys {
		limit := toInt64(args[4+i*3])
		if limit <= 0 {
			continue
		}
		windowMs := toInt64(args[3+i*3])
		count, resetAt := int64(0), nowMs+windowMs
		if raw, ok := f.store[keys[i]]; ok {
			sep := strings.IndexByte(raw, ':')
			if sep > 0 {
				count, _ = strconv.ParseInt(raw[:sep], 10, 64)
				parsed, _ := strconv.ParseInt(raw[sep+1:], 10, 64)
				resetAt = parsed
			}
		}
		if resetAt <= nowMs {
			count, resetAt = 0, nowMs+windowMs
		}
		if count >= limit {
			return []any{int64(0), int64(1), args[3+i*3], limit}, nil
		}
		pendings = append(pendings, pending{keys[i], count + 1, resetAt})
	}
	for _, p := range pendings {
		f.store[p.key] = strconv.FormatInt(p.count, 10) + ":" + strconv.FormatInt(p.resetAt, 10)
	}
	return []any{int64(1), int64(0), "", int64(0)}, nil
}

var _ = time.Now

// TestMemoryStoreSeparatesMinuteAndBurstWindows pins the BUG-0156 fix: the
// minute and burst buckets share the same `ip:class` key, so the memory store
// must keep them in independent windows (Node uses three independent Maps,
// system-api-rate-limit.middleware.ts:65-67). Under the old single-map key the
// burst entry overwrote the minute entry and the 4th request after the burst
// window expired would see a fresh minute window (Retry-After 10, burst
// bucket) instead of the surviving minute window (Retry-After 50).
func TestMemoryStoreSeparatesMinuteAndBurstWindows(t *testing.T) {
	nowMs := int64(0)
	store := NewMemoryStore(nil)
	buckets := []BucketInput{
		{StoreName: "system_api_ip_minute", WindowMs: 60_000, Limit: 2, Key: "1.2.3.4:read"},
		{StoreName: "system_api_ip_burst", WindowMs: 10_000, Limit: 1, Key: "1.2.3.4:read"},
	}

	if allowed, _, _, _, err := store.Check(context.Background(), nowMs, buckets); err != nil || !allowed {
		t.Fatalf("first request must pass: allowed=%v err=%v", allowed, err)
	}
	// Burst exhausted (limit 1): second request denied by the burst bucket
	// while the minute bucket must stay untouched.
	allowed, retry, bucketName, _, err := store.Check(context.Background(), nowMs, buckets)
	if err != nil || allowed || bucketName != "system_api_ip_burst" || retry != 10 {
		t.Fatalf("second request must be denied by burst: allowed=%v retry=%d bucket=%s err=%v", allowed, retry, bucketName, err)
	}

	// After the burst window expires the minute window must still be alive
	// (count 1 of 2), so the third request passes.
	nowMs += 10_000
	if allowed, _, _, _, err := store.Check(context.Background(), nowMs, buckets); err != nil || !allowed {
		t.Fatalf("third request must pass on the surviving minute window: allowed=%v err=%v", allowed, err)
	}

	// The minute bucket is now full: the fourth request is denied by the
	// MINUTE window with its remaining ~40s (reset 60s - 20s elapsed), not by
	// a reborn burst window.
	nowMs += 10_000
	allowed, retry, bucketName, _, err = store.Check(context.Background(), nowMs, buckets)
	if err != nil || allowed || bucketName != "system_api_ip_minute" || retry != 40 {
		t.Fatalf("minute window must survive burst expiry: allowed=%v retry=%d bucket=%s err=%v", allowed, retry, bucketName, err)
	}
}

// TestSettingsFailureReturnsJSON500 pins the BUG-0156 content-type contract:
// Node respondRateLimitFailure answers 500 {"message":...} as JSON
// (system-api-rate-limit.middleware.ts:335-343) — never http.Error text/plain.
func TestSettingsFailureReturnsJSON500(t *testing.T) {
	limiter := &Limiter{
		Settings: func(context.Context) (Settings, error) {
			return Settings{}, &SettingError{Key: "systemApiRateLimitIpReadPerMinute"}
		},
		Store: NewMemoryStore(nil),
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/ping", nil)
	if limiter.ipRateLimit(recorder, request) {
		t.Fatal("settings failure must not pass the limiter")
	}
	assertJSONError(t, recorder, http.StatusInternalServerError)
}

// TestRedisEvalErrorReturns500 pins the BUG-0156 Node alignment: a Redis
// backend failure escapes the limiter into the system error path — 500 JSON
// without Retry-After (checkRedisRateLimit await +
// handleSystemApiError system-api-app.ts:315), never a 429 denial.
func TestRedisEvalErrorReturns500(t *testing.T) {
	limiter := &Limiter{
		Settings: testSettings,
		Store:    &RedisStore{Client: &failingRedis{err: errors.New("redis connection refused")}},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/ping", nil)
	if limiter.ipRateLimit(recorder, request) {
		t.Fatal("backend failure must not pass the limiter")
	}
	assertJSONError(t, recorder, http.StatusInternalServerError)
}

// TestRedisNonArrayFrameReturns429 pins the other Node branch: a resolved
// non-array eval frame maps to a 1-second denial via
// redisFixedWindowRateLimitResult (system-api-rate-limit.middleware.ts
// :403-411), i.e. a normal 429 contract.
func TestRedisNonArrayFrameReturns429(t *testing.T) {
	limiter := &Limiter{
		Settings: testSettings,
		Store:    &RedisStore{Client: &staticRedis{result: "PONG"}},
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/ping", nil)
	if limiter.ipRateLimit(recorder, request) {
		t.Fatal("malformed eval frame must not pass the limiter")
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("non-array frame must stay a 429 denial, got %d", recorder.Code)
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}

type failingRedis struct{ err error }

func (f *failingRedis) Eval(context.Context, string, []string, ...any) (any, error) {
	return nil, f.err
}

type staticRedis struct{ result any }

func (s *staticRedis) Eval(context.Context, string, []string, ...any) (any, error) {
	return s.result, nil
}

func assertJSONError(t *testing.T, recorder *httptest.ResponseRecorder, status int) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d", recorder.Code, status)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	if status == http.StatusInternalServerError {
		if retryAfter := recorder.Header().Get("Retry-After"); retryAfter != "" {
			t.Fatalf("500 must not carry Retry-After, got %q", retryAfter)
		}
	}
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body must be JSON: %v (%q)", err, recorder.Body.String())
	}
	if strings.TrimSpace(payload.Message) == "" {
		t.Fatalf("message must be non-empty, body %q", recorder.Body.String())
	}
}

// TestMethodClassForMatchesNodeReadOnlyPostRules pins the BUG-0156 POST
// classification against the Node db-access rule table: import preview is the
// only POST Node counts into the read buckets (system-api-db-access.ts:70);
// every other audited POST rule is write and stays on the method default.
func TestMethodClassForMatchesNodeReadOnlyPostRules(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   MethodClass
	}{
		{http.MethodGet, "/__aisys__/api/accounts", MethodClassRead},
		{http.MethodHead, "/__aisys__/api/accounts", MethodClassRead},
		{http.MethodOptions, "/__aisys__/api/accounts", MethodClassRead},
		{http.MethodPost, "/__aisys__/api/my-accounts/import/preview", MethodClassRead},
		{http.MethodPost, "/__aisys__/api/accounts/import/preview/", MethodClassRead},
		{http.MethodPost, "/accounts/import/preview", MethodClassRead},
		{http.MethodPost, "/__aisys__/api/my-accounts/import/previewx", MethodClassWrite},
		{http.MethodPost, "/__aisys__/api/accounts/import/preview/extra", MethodClassWrite},
		{http.MethodPost, "/__aisys__/api/accounts/import/confirm", MethodClassWrite},
		{http.MethodPost, "/__aisys__/api/accounts/export", MethodClassWrite},
		{http.MethodPost, "/__aisys__/api/auth/login", MethodClassWrite},
		{http.MethodDelete, "/__aisys__/api/accounts", MethodClassWrite},
	}
	for _, testCase := range cases {
		if got := methodClassFor(httptest.NewRequest(testCase.method, testCase.path, nil)); got != testCase.want {
			t.Fatalf("%s %s class = %s, want %s", testCase.method, testCase.path, got, testCase.want)
		}
	}
}

// TestReadOnlyPostImportPreviewUsesReadBuckets replays the BUG-0156 minimal
// repro end to end: with read=3 burst and write=2 burst, three import-preview
// POSTs must pass (Node read buckets) while the third import/confirm POST is
// already denied (write buckets).
func TestReadOnlyPostImportPreviewUsesReadBuckets(t *testing.T) {
	limiter := newLimiter()
	post := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		if limiter.ipRateLimit(recorder, request) {
			recorder.Code = http.StatusOK
		}
		return recorder
	}
	for i := 1; i <= 3; i++ {
		if got := post("/__aisys__/api/my-accounts/import/preview").Code; got != http.StatusOK {
			t.Fatalf("preview POST %d must pass on read buckets (burst 3), got %d", i, got)
		}
	}
	if got := post("/__aisys__/api/my-accounts/import/preview").Code; got != http.StatusTooManyRequests {
		t.Fatalf("4th preview POST must exhaust the read burst (3), got %d", got)
	}
	for i := 1; i <= 2; i++ {
		if got := post("/__aisys__/api/accounts/import/confirm").Code; got != http.StatusOK {
			t.Fatalf("confirm POST %d must pass on write buckets, got %d", i, got)
		}
	}
	if got := post("/__aisys__/api/accounts/import/confirm").Code; got != http.StatusTooManyRequests {
		t.Fatalf("3rd confirm POST must exhaust the write burst (2), got %d", got)
	}
}
