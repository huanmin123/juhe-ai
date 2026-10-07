package gatewaydispatch

// BUG-0289 回归测试：200 流内失败恢复的引擎消费面——chain 层经
// RequestCoordinationContext.RequestBodyOverride + SameAccountRetry 把清理后的
// 请求体钉回同账户重放。关键正确性约束：override 的 body 注入点在
// dispatchsingle.go:397-400（body := requestParts.Body 之后），即
// BuildPreparedUpstreamRequestParts（模型映射发生处，accountpreparation.go:455
// Driver.BuildGatewayUpstreamRequestParts）之后——override 的 body 不会再过模型
// 映射，必须携带"已映射"的请求体（引擎实际发送的 UpstreamDispatchResult.
// RequestBody，attemptoutcomes.go:63/:201 由 c.loop.body 填充），否则重放丢映射。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestFetchFirstAvailableUpstreamRequestBodyOverrideReplaysSameAccount：
// RequestBodyOverride + SameAccountRetry 消费路径——重放请求只打在钉住账户上，
// body 使用 override 原文，且 model 字段与首次尝试一致（不被二次映射破坏）。
func TestFetchFirstAvailableUpstreamRequestBodyOverrideReplaysSameAccount(t *testing.T) {
	var mu sync.Mutex
	var upstreamBodies []string
	var upstreamAuths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		upstreamBodies = append(upstreamBodies, string(raw))
		upstreamAuths = append(upstreamAuths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// 首次尝试返回 200 SSE 流内失败形态之外，本测试只关心重放请求的构造：
		// 全部返回 2xx，让引擎选中该尝试并带出结果。
		_, _ = w.Write([]byte(`{"id":"resp-ok"}`))
	}))
	defer server.Close()

	// fakeDriver 的 partsBody 即"模型映射后"的已映射体（Driver 是映射的宿主）。
	const mappedBody = `{"model":"gpt-test-mapped","input":"hi"}`
	// override 体基于已映射体构造（模拟 chain 层对 dispatched.RequestBody 的
	// 清理变体），model 字段保持映射后的值。
	const overrideBody = `{"model":"gpt-test-mapped","input":"hi-cleaned"}`

	engine, driver, _ := newTestEngine(t)
	driver.urlByAccount = map[string][]string{
		"a-1": {server.URL + "/v1/responses"},
		"a-2": {server.URL + "/v1/responses"},
	}
	driver.partsBody = mappedBody
	req := newTestRequest(t, mappedBody)
	accounts := testAccounts("a-1", "a-2")
	accountByID := map[string]AccountCandidate{"a-1": accounts[0], "a-2": accounts[1]}
	coordination := newTestCoordination(t)
	coordination.SemanticRetryID = "codex_encrypted_content_cleanup:encrypted_context_invalid"
	coordination.RequestBodyOverride = &RequestBodyOverride{AccountID: "a-1", Body: []byte(overrideBody)}
	// RetryID 留空：SameAccountRetryID 注册通道与 SemanticRetryID 互斥
	//（routecoordination.go:1076），钉住只靠 SameAccountRetry.Account 塌缩
	// 候选窗口（upstreamdispatch.go:536-538）。
	coordination.SameAccountRetry = &SameAccountRetry{
		RetryID: "",
		Account: accountByID["a-1"],
	}
	args := dispatchArgs(t, req, accounts)
	args.RequestCoordination = coordination
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("重放必须钉在同账户 a-1，实际 %q", result.Account.ID)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(upstreamBodies) != 1 {
		t.Fatalf("upstream hits = %d, want 1", len(upstreamBodies))
	}
	// 关键断言：重放上游收到的 body 是 override 原文——model 字段与首次尝试
	// 的已映射值一致，不被二次映射破坏。
	if !strings.Contains(upstreamBodies[0], `"model":"gpt-test-mapped"`) {
		t.Fatalf("重放 body 必须保留已映射 model 字段: %s", upstreamBodies[0])
	}
	if !strings.Contains(upstreamBodies[0], "hi-cleaned") {
		t.Fatalf("重放 body 必须是 override 体: %s", upstreamBodies[0])
	}
	if strings.Contains(upstreamBodies[0], "hi\"") || strings.Contains(upstreamBodies[0], `"input":"hi"`) {
		t.Fatalf("重放 body 不得回退到未清理体: %s", upstreamBodies[0])
	}
	// 选中结果带出的 RequestBody 与实际发送体一致（chain 层清理重放的数据源）。
	if string(result.RequestBody) != overrideBody {
		t.Fatalf("result.RequestBody = %s, want override body", result.RequestBody)
	}
	_ = upstreamAuths
}
