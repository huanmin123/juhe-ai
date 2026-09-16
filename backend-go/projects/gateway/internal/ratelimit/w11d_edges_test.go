package ratelimit

// w11d 边缘臂：MemoryStore cleanup/trim/零限幅跳过、AuthenticatedRateLimit
// 公开入口与 user write/allowlist/settings 失败臂、check 与 RedisStore 的
// retryAfter 下限、numeric 类型收敛。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func TestW11DMemoryStoreSkipsZeroLimitBuckets(t *testing.T) {
	store := NewMemoryStore(nil)
	allowed, retry, name, _, err := store.Check(context.Background(), 1_000, []BucketInput{
		{StoreName: "disabled_bucket", WindowMs: 60_000, Limit: 0, Key: "k"},
	})
	if err != nil || !allowed || retry != 0 || name != "" {
		t.Fatalf("limit<=0 的桶必须被跳过: allowed=%v retry=%d name=%q err=%v", allowed, retry, name, err)
	}
}

func TestW11DMemoryStoreCleanupDropsExpiredEntries(t *testing.T) {
	store := NewMemoryStore(nil)
	bucket := BucketInput{StoreName: "w11d_minute", WindowMs: 60_000, Limit: 2, Key: "w11d-ip:read"}
	if allowed, _, _, _, err := store.Check(context.Background(), 1_000, []BucketInput{bucket}); err != nil || !allowed {
		t.Fatalf("首次必须放行: allowed=%v err=%v", allowed, err)
	}
	if len(store.entries) != 1 {
		t.Fatalf("应有一条活跃窗口: %d", len(store.entries))
	}
	// 时间越过 cleanup 间隔与窗口过期点：cleanup 必须删除过期行。
	if allowed, _, _, _, err := store.Check(context.Background(), 61_000+cleanupIntervalMs, []BucketInput{bucket}); err != nil || !allowed {
		t.Fatalf("过期后必须重建窗口: allowed=%v err=%v", allowed, err)
	}
	if store.next <= 61_000+cleanupIntervalMs {
		t.Fatalf("cleanup 后 next 必须前移: %d", store.next)
	}
}

func TestW11DMemoryStoreTrimsOverMaxEntries(t *testing.T) {
	store := NewMemoryStore(nil)
	// 预置超限条目：一半已过期、一半仍在窗口内。
	for i := 0; i < maxEntriesPerSan+1; i++ {
		resetAt := int64(10_000_000)
		if i%2 == 0 {
			resetAt = 1 // 已过期
		}
		store.entries[fmt.Sprintf("w11d-k-%d", i)] = memoryEntry{count: 1, resetAtMs: resetAt}
	}
	store.next = 0 // 强制下一轮 cleanup
	nowMs := int64(2_000_000)
	allowed, _, _, _, err := store.Check(context.Background(), nowMs, []BucketInput{
		{StoreName: "w11d_trim", WindowMs: 60_000, Limit: 1, Key: "w11d-live"},
	})
	if err != nil || !allowed {
		t.Fatalf("trim 路径必须放行合法请求: allowed=%v err=%v", allowed, err)
	}
	if len(store.entries) > maxEntriesPerSan*9/10 {
		t.Fatalf("trim 后条目必须降到目标以下: %d", len(store.entries))
	}
}

func TestW11DAuthenticatedRateLimitDelegateAndArms(t *testing.T) {
	// 非健康路径走 authenticatedRateLimit：user write 桶。
	limiter := &Limiter{Settings: testSettings, Store: NewMemoryStore(nil)}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/accounts", nil)
	if !limiter.AuthenticatedRateLimit(recorder, request, "w11d-user") {
		t.Fatalf("首次写请求必须放行: code=%d", recorder.Code)
	}
	// UserWritePerMinute=3：共放行 3 次，第 4 次写被拒。
	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		if !limiter.AuthenticatedRateLimit(recorder, httptest.NewRequest(http.MethodPost, "/__aisys__/api/accounts", nil), "w11d-user") {
			t.Fatalf("写请求 %d 不应被拒", i+2)
		}
	}
	denied := httptest.NewRecorder()
	if limiter.AuthenticatedRateLimit(denied, httptest.NewRequest(http.MethodPost, "/__aisys__/api/accounts", nil), "w11d-user") {
		t.Fatal("第 4 次写必须被 user write 桶拒绝")
	}
	assertJSONError(t, denied, http.StatusTooManyRequests)
	if got := denied.Header().Get("Retry-After"); got == "" {
		t.Fatal("Retry-After 缺失")
	}
}

func TestW11DAuthenticatedRateLimitSettingsFailure(t *testing.T) {
	limiter := &Limiter{
		Settings: func(context.Context) (Settings, error) {
			return Settings{}, errors.New("w11d settings down")
		},
		Store: NewMemoryStore(nil),
	}
	recorder := httptest.NewRecorder()
	if limiter.AuthenticatedRateLimit(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/x", nil), "w11d-user") {
		t.Fatal("settings 失败必须拒绝")
	}
	assertJSONError(t, recorder, http.StatusInternalServerError)
}

func TestW11DAuthenticatedRateLimitAllowlistBypass(t *testing.T) {
	limiter := &Limiter{
		Settings:  testSettings,
		Allowlist: func(context.Context, string) bool { return true },
		Store:     NewMemoryStore(nil),
	}
	recorder := httptest.NewRecorder()
	if !limiter.AuthenticatedRateLimit(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/x", nil), "w11d-user") {
		t.Fatal("allowlist 必须直通")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("直通不得写响应: %d", recorder.Code)
	}
}

type w11dZeroRetryStore struct{}

func (w11dZeroRetryStore) Check(context.Context, int64, []BucketInput) (bool, int, string, int, error) {
	return false, 0, "w11d_bucket", 9, nil
}

func TestW11DLimiterCheckClampsRetryAfterToOne(t *testing.T) {
	limiter := &Limiter{Settings: testSettings, Store: w11dZeroRetryStore{}}
	recorder := httptest.NewRecorder()
	if limiter.ipRateLimit(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/x", nil)) {
		t.Fatal("拒绝态必须拦截")
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("retryAfter=0 必须被钳到 1: %q", got)
	}
}

func TestW11DRedisStoreEmptyBucketsAndRetryClamp(t *testing.T) {
	store := &RedisStore{Client: &fakeRedis{}}
	allowed, retry, _, _, err := store.Check(context.Background(), 1_000, nil)
	if err != nil || !allowed || retry != 0 {
		t.Fatalf("空桶必须直接放行: allowed=%v retry=%d err=%v", allowed, retry, err)
	}

	clamp := &RedisStore{Client: w11dStaticRedis{frame: []any{int64(0), int64(0), "w11d_name", int64(4)}}}
	allowed, retry, name, limit, err := clamp.Check(context.Background(), 1_000, []BucketInput{
		{StoreName: "w11d_redis", WindowMs: 60_000, Limit: 4, Key: "w11d-k"},
	})
	if err != nil || allowed || retry != 1 || name != "w11d_name" || limit != 4 {
		t.Fatalf("redis retry=0 必须钳到 1: allowed=%v retry=%d name=%q limit=%d err=%v", allowed, retry, name, limit, err)
	}
}

type w11dStaticRedis struct{ frame any }

func (f w11dStaticRedis) Eval(context.Context, string, []string, ...any) (any, error) {
	return f.frame, nil
}

func TestW11DNumericTypeArms(t *testing.T) {
	if numeric(int64(7)) != 7 || numeric(int(9)) != 9 || numeric(float64(2.9)) != 2 {
		t.Fatal("int64/int/float64 收敛错误")
	}
	if numeric("w11d-not-number") != 0 || numeric(nil) != 0 {
		t.Fatal("未知类型必须归零")
	}
}

func TestW11DAuthenticatedRateLimitJSONContractShape(t *testing.T) {
	// 429 响应体必须是 JSON 契约（message 字段），与 kernel.WriteError 对齐。
	limiter := &Limiter{Settings: testSettings, Store: NewMemoryStore(nil)}
	for i := 0; i < 7; i++ {
		recorder := httptest.NewRecorder()
		limiter.AuthenticatedRateLimit(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/list", nil), "w11d-json")
		if i < 6 {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("429 响应必须是 JSON: %v", err)
		}
		if _, ok := payload["message"]; !ok {
			t.Fatalf("429 JSON 缺少 message: %v", payload)
		}
	}
}

var _ = time.Now
var _ = kernel.New
