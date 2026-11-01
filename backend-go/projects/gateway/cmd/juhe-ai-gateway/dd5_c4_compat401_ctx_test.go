package main

// DD5-C4 波次测试（2026-11-01，R5 confirmed 中级）：compat 401 计数发生在响
// 应写出之后（chain.compat.ServeHTTP 返回 → recordCompatPreAuthFailure），
// 此前沿用请求 ctx（req.HTTP.Context()）——客户端读到 401 立即断连即取消
// 请求 ctx，Redis evalLua IO 随取消失败、计数仅 Warn，无效凭据得以系统性
// 规避 preauth 熔断计数（主链/realtime 均早于写出计数，无此暴露）。修复：
// 计数 ctx 改 context.WithoutCancel（与 token 刷新链先例同口径：保留值链、
// 剥离取消传播）+ 短超时兜底；计数失败仍 Warn 不阻断已写出的 401。
// DD6-S5（2026-11-01）适配：计数移入有界后台面异步执行——断连取消发生在
// ServeHTTP 返回之后、worker 执行之前，WithoutCancel 语义在更强的时序下钉住。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// dd5C4CtxSensingCircuits 在 RecordPreAuthFailure 入口检查 ctx 存活：ctx 已
// 取消即返回错误（复现 Redis evalLua 随断连取消失败的形态），仅 ctx 可用时
// 入账——把「沿用请求 ctx」的计数丢失变成确定性断言。
type dd5C4CtxSensingCircuits struct {
	ddC1RecordingCircuits
}

func (c *dd5C4CtxSensingCircuits) RecordPreAuthFailure(ctx context.Context, input gatewaypreauth.PreAuthFailureInput) (gatewaypreauth.CircuitDecision, error) {
	if err := ctx.Err(); err != nil {
		return gatewaypreauth.CircuitDecision{}, err
	}
	return c.ddC1RecordingCircuits.RecordPreAuthFailure(ctx, input)
}

// TestDD5C4CompatUnauthorizedCountsAfterRequestContextCanceled 钉住：请求
// ctx 已取消（客户端读 401 后立即断连的等价形态）时，compat 401 计数仍成功
// 入账，且 401 响应本身不受影响。
func TestDD5C4CompatUnauthorizedCountsAfterRequestContextCanceled(t *testing.T) {
	circuits := &dd5C4CtxSensingCircuits{}
	chain := ddC1Chain(t, circuits)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 客户端断连：请求 ctx 在计数发生前已取消
	request := withBearer("bad-key").WithContext(ctx)

	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, want 401（计数不得阻断/改写已写出的 401）", recorder.Code)
	}
	// DD6-S5：计数由后台 worker 异步执行（此时请求 ctx 已取消、handler 已返
	// 回），WithoutCancel 必须保证计数不随断连取消丢失。
	records := waitForCompatRecords(t, &circuits.ddC1RecordingCircuits, 1)
	if records[0].Reason != gatewaypreauth.PreAuthFailureInvalidAPIKey {
		t.Fatalf("reason = %q, want invalid_api_key", records[0].Reason)
	}
}
