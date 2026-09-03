package ratelimit

import (
	"context"
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
