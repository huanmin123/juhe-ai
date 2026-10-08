package gatewayresponse

// BUG-0298 回归测试：加密上下文信号未命中的预提交流失败换号重试。
// 生产事实：同一"上游拒绝加密续上下文"失败随上游实现漂移报错形状
//（encrypted_context_invalid → invalid_request 通用参数文案），恢复臂白名单
// 未命中时既有两分支判否，失败带"请重试"文案落客户端，Codex 自动重试但
// 上下文不变 → 同账户死循环。修复：恢复臂门控域（openai 协议 + /v1/responses
// 族）内白名单未命中的预提交失败复用既有 pre-commit 换号通道（排除当前账户，
// 受既有重试预算约束）；白名单命中面维持清理重放优先，恢复臂不可恢复终态
// 维持终态不换号。

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// bug0298ProductionDriftFailedEvent 是 2026-10-08 生产漂移形状：同一上游拒绝
// 改报 invalid_request 通用参数文案（trace bab0a5b5-9cc0-455f-b1f0-63ee83bc7d91）。
const bug0298ProductionDriftFailedEvent = "event: response.failed\n" +
	`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_request","message":"The request could not be processed. Please check the request parameters."}}}` + "\n\n"

// TestBug0298WhitelistMissPreCommitFailureSwitchesAccount：白名单未命中的
// 检查拦截失败必须复用既有 pre-commit 换号通道（排除当前账户），而不是把
// 可重试文案交回客户端造成同账户死循环；错误码归因保持上游原码。
func TestBug0298WhitelistMissPreCommitFailureSwitchesAccount(t *testing.T) {
	input, recorder, audit := bug0289NewResponsesInput(t, NewSliceUpstreamBody([]byte(bug0298ProductionDriftFailedEvent)))
	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.RetryUpstream {
		t.Fatalf("白名单未命中的预提交失败必须换号重试: %+v", result)
	}
	if result.RetryReason != StreamServerRetryPreCommitStreamFailure {
		t.Fatalf("retryReason = %q", result.RetryReason)
	}
	if !result.ExcludeCurrentAccount {
		t.Fatal("换号重试必须排除当前账户")
	}
	if result.SameAccountRetryEligible {
		t.Fatal("非传输失败不得原账户重试")
	}
	if result.ResponseInspection == nil {
		t.Fatal("检查拦截决策必须随 verdict 交接（审计归因通道）")
	}
	if result.AlreadyFinalized {
		t.Fatal("不得落客户端终态")
	}
	if recorder.Body.String() != "" {
		t.Fatalf("换号 verdict 不得向客户端写失败终态: %q", recorder.Body.String())
	}
	metadata := audit.findMetadata("pre_commit_stream_server_retry")
	if metadata == nil {
		t.Fatalf("pre_commit_stream_server_retry metadata 缺失: %v", audit.metadataLabels())
	}
	if len(audit.completed) != 1 {
		t.Fatalf("completed = %d", len(audit.completed))
	}
	// usage 错误码走既有通道（换号不改变归因形状）——与生产事故行
	//（usage_records.error_code=upstream_retryable_error）一致。
	if audit.completed[0].ErrorCode != "upstream_retryable_error" {
		t.Fatalf("usage 错误码归因通道必须保持不变: %+v", audit.completed[0])
	}
}

// TestBug0298Guards：换号分支的边界守卫。
func TestBug0298Guards(t *testing.T) {
	t.Run("chat request keeps existing terminal behavior", func(t *testing.T) {
		input, recorder, _ := bug0289NewResponsesInput(t, NewSliceUpstreamBody([]byte(bug0298ProductionDriftFailedEvent)))
		input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
		result, err := HandleStreamUpstreamResponse(input)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if result.RetryUpstream {
			t.Fatalf("非 responses 请求不进换号分支: %+v", result)
		}
		if !result.AlreadyFinalized {
			t.Fatalf("非 responses 请求保持既有客户端失败终态行为: %+v", result)
		}
		if !strings.Contains(recorder.Body.String(), GatewayStreamClientRetryMessage) {
			t.Fatalf("非 responses 请求保持既有可重试文案: %q", recorder.Body.String())
		}
	})
	t.Run("heuristic hit still prefers recovery replay over switching", func(t *testing.T) {
		// 码漂移但文案仍含解密失败启发：白名单命中面维持 0289 清理重放优先，
		// 换号分支不得抢跑（锁定两臂优先级）。
		event := "event: response.failed\n" +
			`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_request","message":"The request could not be processed: the encrypted continuation context could not be decrypted."}}}` + "\n\n"
		input, _, _ := bug0289NewResponsesInput(t, NewSliceUpstreamBody([]byte(event)))
		result, err := HandleStreamUpstreamResponse(input)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if !result.RetryUpstream || result.RetryReason != StreamServerRetryCodexEncryptedContentRecovery {
			t.Fatalf("启发命中必须走 0289 清理重放而非换号: %+v", result)
		}
		if result.ExcludeCurrentAccount {
			t.Fatal("清理重放不得排除当前账户")
		}
	})
	t.Run("recovery exhausted final copy stays terminal", func(t *testing.T) {
		// 白名单命中 + 体无可清理项：恢复臂已把文案改写为恢复终态
		//（BUG-0289 语义），换号分支不得把它重新变成换号重试。
		plainBody := `{"model":"gpt-test","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
		input, recorder, _ := bug0289NewResponsesInput(t, NewSliceUpstreamBody([]byte(bug0289ProductionFailedEvent)))
		input.RequestBody = []byte(plainBody)
		result, err := HandleStreamUpstreamResponse(input)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if result.RetryUpstream || !result.AlreadyFinalized {
			t.Fatalf("恢复终态必须保持终态不换号: %+v", result)
		}
		if !strings.Contains(recorder.Body.String(), gatewaycodex.CodexEncryptedContentRecoveryExhaustedMessage) {
			t.Fatalf("客户端 body 必须保持恢复终态文案: %q", recorder.Body.String())
		}
	})
}
