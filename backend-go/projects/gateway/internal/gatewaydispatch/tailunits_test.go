package gatewaydispatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// 零散尾部缺口收尾：包装错误的 Error/Unwrap、协议头白名单其他画像、
// anthropic 传输标记、key-model 主探针路由匹配、无操作释放闭包。

func TestWrappedErrorSurfaceMethods(t *testing.T) {
	inner := errors.New("底层错误")
	started := &StartedTransportError{Err: inner}
	if started.Error() != "底层错误" {
		t.Fatalf("Error() = %q", started.Error())
	}
	if started.Unwrap() != inner {
		t.Fatal("Unwrap 必须返回底层错误")
	}
	aborted := &UpstreamRequestAbortedError{Message: "请求已取消"}
	if aborted.Error() != "请求已取消" {
		t.Fatalf("Error() = %q", aborted.Error())
	}
	adapter := &OpenAIOAuthCodexAdapterError{Message: "适配失败"}
	if adapter.Error() != "适配失败" {
		t.Fatalf("Error() = %q", adapter.Error())
	}
	pipeErr := &NonStreamUpstreamBodyPipeError{Message: "管道中断", OriginalError: inner}
	if pipeErr.Error() != "管道中断" || pipeErr.Unwrap() != inner {
		t.Fatalf("pipe = %q", pipeErr.Error())
	}
	attemptErr := &UpstreamAttemptError{Message: "上游尝试失败"}
	if attemptErr.Error() != "上游尝试失败" {
		t.Fatalf("Error() = %q", attemptErr.Error())
	}
	cutover := &NormalRouteFirstByteCutoverError{Message: "切换"}
	if cutover.Error() != "切换" {
		t.Fatalf("Error() = %q", cutover.Error())
	}
	unsupported := &UnsupportedUpstreamResponseEncodingError{Message: "编码"}
	if unsupported.Error() != "编码" {
		t.Fatalf("Error() = %q", unsupported.Error())
	}
	unsafeErr := &UnsafeResolvedUpstreamURLError{Message: "不安全地址"}
	if unsafeErr.Error() != "不安全地址" {
		t.Fatalf("Error() = %q", unsafeErr.Error())
	}
	primary := &PrimaryStartedGatewayTransportError{Err: inner}
	if primary.Error() != "底层错误" || primary.Unwrap() != inner {
		t.Fatal("PrimaryStartedGatewayTransportError 面不符")
	}
	unresolved := &UnsafeUpstreamURLError{Message: "静态不安全"}
	if unresolved.Error() != "静态不安全" {
		t.Fatalf("Error() = %q", unresolved.Error())
	}
}

func TestAsOverrideErrorUnwrapChain(t *testing.T) {
	wrapped := fmt.Errorf("外层: %w", &GptAccountRequestOverrideError{Message: "内层"})
	var target *GptAccountRequestOverrideError
	if !asOverrideError(wrapped, &target) || target.Message != "内层" {
		t.Fatalf("wrapped 链必须解开, target = %#v", target)
	}
	if asOverrideError(errors.New("普通"), &target) {
		t.Fatal("普通错误不匹配")
	}
	// 无 Unwrap 的自定义错误终止链。
	if asOverrideError(fmt.Errorf("外层: %d", 1), &target) {
		t.Fatal("无法解开的链返回 false")
	}
}

func TestHeadersFromObjectNil(t *testing.T) {
	output := headersFromObject(nil)
	if output == nil || output.Get("X") != "" {
		t.Fatalf("output = %#v", output)
	}
}

func TestCopyOfficialOAuthClientHeadersGeminiAndXai(t *testing.T) {
	input := http.Header{
		"Api-Revision":          []string{"r1"},
		"X-Goog-Api-Client":     []string{"gem-cli"},
		"X-Grok-Client-Version": []string{"v9"},
		"X-Xai-Token-Auth":      []string{"tok"},
		"X-Ignored":             []string{"no"},
	}
	gemini := CopyOfficialOAuthClientRequestHeaders(input, OAuthHeaderProfileGeminiCLI)
	if gemini.Get("Api-Revision") != "r1" || gemini.Get("X-Goog-Api-Client") != "gem-cli" {
		t.Fatalf("gemini = %#v", gemini)
	}
	if gemini.Get("X-Ignored") != "" {
		t.Fatal("非白名单头必须剔除")
	}
	xai := CopyOfficialOAuthClientRequestHeaders(input, OAuthHeaderProfileXAIGrok)
	if xai.Get("X-Grok-Client-Version") != "v9" || xai.Get("X-Xai-Token-Auth") != "tok" {
		t.Fatalf("xai = %#v", xai)
	}
	// 空值 header 剔除。
	emptyValues := http.Header{"User-Agent": {}}
	if got := CopyOfficialOAuthClientRequestHeaders(emptyValues, OAuthHeaderProfileOpenAICodex); len(got) != 0 {
		t.Fatalf("empty values = %#v", got)
	}
	// 未知画像仅保留公共头。
	common := http.Header{"Accept": []string{"json"}, "X-Other": []string{"x"}}
	unknown := CopyOfficialOAuthClientRequestHeaders(common, OfficialOAuthClientHeaderProfile("mystery"))
	if unknown.Get("Accept") != "json" || unknown.Get("X-Other") != "" {
		t.Fatalf("unknown profile = %#v", unknown)
	}
}

func TestUpstreamTransportForAttempt(t *testing.T) {
	anthropicHeaders := http.Header{"Anthropic-Version": []string{"2023-06-01"}, "X-Api-Key": []string{"k"}}
	if got := upstreamTransportForAttempt(anthropicHeaders, "https://u.example/v1/messages"); got != "fetch" {
		t.Fatalf("transport = %q", got)
	}
	if got := upstreamTransportForAttempt(anthropicHeaders, "https://u.example/v1/messages/count_tokens"); got != "fetch" {
		t.Fatalf("count_tokens transport = %q", got)
	}
	if got := upstreamTransportForAttempt(anthropicHeaders, "https://u.example/v1/chat/completions"); got != "" {
		t.Fatalf("非 messages 路径无标记, got %q", got)
	}
	plain := http.Header{"X-Api-Key": []string{"k"}}
	if got := upstreamTransportForAttempt(plain, "https://u.example/v1/messages"); got != "" {
		t.Fatalf("缺 Anthropic-Version 无标记, got %q", got)
	}
	if got := upstreamTransportForAttempt(anthropicHeaders, "://bad"); got != "" {
		t.Fatalf("非法 URL 无标记, got %q", got)
	}
}

func TestGatewayKeyModelRouteMatchesMainProbe(t *testing.T) {
	revision := int64(1)
	fingerprint := "fp-probe"
	account := AccountCandidate{
		ID:                        "a-1",
		ProviderCode:              "openai",
		HealthCheckModel:          "gpt-probe",
		HealthCheckEndpointMode:   "chat_sse",
		DispatchRevision:          &revision,
		SelectedAPIKeyFingerprint: &fingerprint,
	}
	capability := gatewayKeyModelCapabilityForRoute(account, "gpt-probe", gatewayrouting.EndpointFamilyChatCompletions, true)
	if capability == nil {
		t.Fatal("能力键必须解析")
	}
	if !gatewayKeyModelRouteMatchesMainProbe(account, "gpt-probe", gatewayrouting.EndpointFamilyChatCompletions, true, *capability) {
		t.Fatal("相同路由必须匹配主探针")
	}
	// 模型不同不匹配。
	if gatewayKeyModelRouteMatchesMainProbe(account, "other", gatewayrouting.EndpointFamilyChatCompletions, true, *capability) {
		t.Fatal("不同模型不应匹配")
	}
	// 无指纹时无能力键。
	noFingerprint := account
	noFingerprint.SelectedAPIKeyFingerprint = nil
	if gatewayKeyModelCapabilityForRoute(noFingerprint, "gpt-probe", gatewayrouting.EndpointFamilyChatCompletions, true) != nil {
		t.Fatal("无指纹不应产生能力键")
	}
	// revision < 1 时无能力键。
	zero := int64(0)
	noRevision := account
	noRevision.DispatchRevision = &zero
	if gatewayKeyModelCapabilityForRoute(noRevision, "gpt-probe", gatewayrouting.EndpointFamilyChatCompletions, true) != nil {
		t.Fatal("revision<1 不应产生能力键")
	}
}

func TestResolveGatewayKeyModelAttemptCapabilityVariants(t *testing.T) {
	// 非 POST 不产生能力。
	getReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil))
	revision := int64(1)
	fingerprint := "fp"
	account := AccountCandidate{DispatchRevision: &revision, SelectedAPIKeyFingerprint: &fingerprint}
	if ResolveGatewayKeyModelAttemptCapability(getReq, account) != nil {
		t.Fatal("GET 请求无能力")
	}
	// POST chat completions 流式。
	postReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	postReq.Body = newTestRequestBody(t, `{"model":"gpt-test","stream":true}`)
	capability := ResolveGatewayKeyModelAttemptCapability(postReq, account)
	if capability == nil {
		t.Fatal("POST 流式请求必须产生能力")
	}
}

func TestPreparationNoopRelease(t *testing.T) {
	// 普通分组的准备结果提供 no-op 释放闭包（调用安全）。
	pipeline, _, _, _ := newPipeline(t)
	input := dispatchPreparationInput(t, testAccounts("a-1"))
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.ReleaseClientIPConcurrency == nil {
		t.Fatal("普通分组也必须提供释放闭包")
	}
	result.ReleaseClientIPConcurrency()
	result.ReleaseClientIPConcurrency()
}
