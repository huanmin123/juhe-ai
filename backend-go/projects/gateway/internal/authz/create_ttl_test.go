package authz

// 第三轮常驻审查 #1 对齐测试：authorizations.create 的 mutationGuard 配置
// （internal/authz/routes.go 的 POST /authorizations 注册）必须忠实归档
// authorizations.routes.ts:219 的 succeededTtlMs: 0 —— 成功后同指纹立即
// 可重试（成功不保留去重条目），失败保持 Node 默认 10s 失败窗口（未传
// failedTtlMs 时 complete 走默认值）。
//
// 本文件逐字段镜像 routes.go 内联的 MutationGuardOptions（operationKey、
// SucceededTTL、FailedTTL、ProcessingTTL）；两侧必须同步修改。

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

type guardClock struct{ now time.Time }

func (c *guardClock) Now() time.Time          { return c.now }
func (c *guardClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// createGuardRequest 构造直连 recorder 的 POST：httptest.NewRequest 只设置
// ContentLength 字段而不设置 Content-Length 头，guard 的 express.json 阶段
// （hasRequestBody）读的是头，必须显式补齐才能走到与真实请求相同的解析路径。
func createGuardRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/authorizations", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return request
}

func authorizationsCreateGuardForTest(store *kernel.DeduplicationStore) http.Handler {
	return kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey:  "authorizations.create",
		Store:         store,
		SucceededTTL:  kernel.DedupNoRetention,
		FailedTTL:     0,
		ProcessingTTL: authorizationsProcessingTTL,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"resourceType": kernel.TextField(kernel.BodyField(r, "resourceType")),
				"resourceId":   kernel.TextField(kernel.BodyField(r, "resourceId")),
				"granteeId":    kernel.TextField(kernel.BodyField(r, "granteeId")),
			}, nil
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
}

const createAuthzBody = `{"resourceType":"account","resourceId":"acc-1","granteeId":"user-1"}`

func TestAuthorizationsCreateSuccessIsImmediatelyRetryable(t *testing.T) {
	clock := &guardClock{now: time.Unix(1_000_000, 0)}
	store := kernel.NewDeduplicationStore(clock.Now)
	handler := authorizationsCreateGuardForTest(store)

	post := func() *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, createGuardRequest(createAuthzBody))
		return recorder
	}

	if got := post().Code; got != http.StatusCreated {
		t.Fatalf("first create = %d, want 201", got)
	}
	// 冻结时钟下的立即重试：成功不保留条目（succeededTtlMs: 0），同指纹
	// 必须再次到达 handler 而不是 409。
	if got := post().Code; got != http.StatusCreated {
		t.Fatalf("immediate retry after success must reach the handler (succeededTtlMs 0), got %d", got)
	}
}

func TestAuthorizationsCreateFailureKeepsDefaultTenSecondWindow(t *testing.T) {
	clock := &guardClock{now: time.Unix(2_000_000, 0)}
	store := kernel.NewDeduplicationStore(clock.Now)
	attempt := 0
	guard := kernel.MutationGuardMiddleware(kernel.MutationGuardOptions{
		OperationKey:  "authorizations.create",
		Store:         store,
		SucceededTTL:  kernel.DedupNoRetention,
		FailedTTL:     0,
		ProcessingTTL: authorizationsProcessingTTL,
		Fingerprint: func(r *http.Request) (any, error) {
			return map[string]any{
				"resourceType": kernel.TextField(kernel.BodyField(r, "resourceType")),
				"resourceId":   kernel.TextField(kernel.BodyField(r, "resourceId")),
				"granteeId":    kernel.TextField(kernel.BodyField(r, "granteeId")),
			}, nil
		},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			kernel.WriteBadRequest(w, "创建授权失败")
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))

	post := func() *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		guard.ServeHTTP(recorder, createGuardRequest(createAuthzBody))
		return recorder
	}

	if got := post().Code; got != http.StatusBadRequest {
		t.Fatalf("failing create = %d, want 400", got)
	}
	// 失败条目按默认 10s 窗口保留：立即重试必须 409 且命中失败文案
	// （mutationGuard duplicateMessage(failed)）。
	duplicate := post()
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("retry inside the failed window = %d, want 409", duplicate.Code)
	}
	if body := duplicate.Body.String(); !strings.Contains(body, "请求刚刚失败，请稍后重试") {
		t.Fatalf("failed duplicate message mismatch: %s", body)
	}
	// 越过默认失败窗口后 claim 重新可用。
	clock.advance(10 * time.Second)
	if got := post().Code; got != http.StatusCreated {
		t.Fatalf("retry after the 10s failed window must reach the handler, got %d", got)
	}
}
