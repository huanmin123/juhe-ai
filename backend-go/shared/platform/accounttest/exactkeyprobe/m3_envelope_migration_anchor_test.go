// m3_envelope_migration_anchor_test.go 钉住信封统一设计 §6 M3 的迁移锚点：
// F4 exactkeyprobe 的请求构造已上收 upstreamenvelope——预算常量取自信封、
// 登记端点的 URL 归一化经信封 UpstreamURL 构造、Accept 标准头经信封
// AcceptHeader 构造、认证头经信封 Auth.Apply 写入。本测试以代表性形态断言
// 请求与信封同名能力逐字一致，防止私有拼接回流。
package exactkeyprobe

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
)

func TestM3EnvelopeMigrationAnchor(t *testing.T) {
	// 预算常量来自信封：本包别名只是 DefaultProbeOutputTokens 的引用。
	if probeOutputTokenLimit != upstreamenvelope.DefaultProbeOutputTokens {
		t.Fatalf("probeOutputTokenLimit = %d, want upstreamenvelope.DefaultProbeOutputTokens %d", probeOutputTokenLimit, upstreamenvelope.DefaultProbeOutputTokens)
	}

	// 代表性 URL/认证/标准头：GLM OpenAI 官方根（剥请求 /v1 的路径规约）
	// 的 chat_json 请求与信封 UpstreamURL 逐字一致，预算字段值等于信封常量。
	base, err := url.Parse("https://open.bigmodel.cn/api/paas/v4")
	if err != nil {
		t.Fatal(err)
	}
	glmInput := Input{
		Type:              "api_key",
		Provider:          "glm",
		ProtocolProfileID: "profile_glm_general_openai_v1",
		EndpointMode:      "chat_json",
		HealthModel:       "anchor-model",
	}
	request, err := buildProbeRequest(context.Background(), base, glmInput, "anchor-key", testChallenge)
	if err != nil {
		t.Fatal(err)
	}
	wantURL, err := upstreamenvelope.UpstreamURL("https://open.bigmodel.cn/api/paas/v4", upstreamenvelope.ModeChatJSON, "profile_glm_general_openai_v1", upstreamenvelope.AdapterNone, "/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	// R2：pathAndQuery 传去版本前缀操作路径（旧值 /v1/chat/completions 依赖
	// 运行时剥前缀，已随 R1 删除）；版本段只随 base 版本根传入（R3）。
	if request.URL.String() != wantURL {
		t.Fatalf("chat_json URL = %q, want envelope UpstreamURL %q", request.URL.String(), wantURL)
	}
	if request.Header.Get("Authorization") != "Bearer anchor-key" {
		t.Fatalf("chat_json Authorization = %q, want envelope AuthBearer value", request.Header.Get("Authorization"))
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["max_tokens"] != float64(upstreamenvelope.DefaultProbeOutputTokens) {
		t.Fatalf("chat_json max_tokens = %#v, want envelope DefaultProbeOutputTokens %d", payload["max_tokens"], upstreamenvelope.DefaultProbeOutputTokens)
	}

	// Accept 标准头经信封 AcceptHeader 构造：api_key SSE 双 accept、oauth
	// SSE 纯流式，线上头与同名能力输出一致。
	apiKeyBase, _ := url.Parse("https://upstream.test")
	apiKeyInput := Input{Type: "api_key", Provider: "openai", EndpointMode: "responses_sse", HealthModel: "anchor-model"}
	apiKeyRequest, err := buildProbeRequest(context.Background(), apiKeyBase, apiKeyInput, "anchor-key", testChallenge)
	if err != nil {
		t.Fatal(err)
	}
	wantAccept, err := upstreamenvelope.AcceptHeader(upstreamenvelope.ModeResponsesSSE, upstreamenvelope.AuthBearer)
	if err != nil {
		t.Fatal(err)
	}
	if got := apiKeyRequest.Header.Get("Accept"); got != wantAccept {
		t.Fatalf("api-key responses_sse Accept = %q, want envelope AcceptHeader %q", got, wantAccept)
	}
	oauthInput := apiKeyInput
	oauthInput.Type = "oauth"
	oauthRequest, err := buildProbeRequest(context.Background(), apiKeyBase, oauthInput, "anchor-key", testChallenge)
	if err != nil {
		t.Fatal(err)
	}
	wantOAuthAccept, err := upstreamenvelope.AcceptHeader(upstreamenvelope.ModeResponsesSSE, upstreamenvelope.AuthOAuth)
	if err != nil {
		t.Fatal(err)
	}
	if got := oauthRequest.Header.Get("Accept"); got != wantOAuthAccept {
		t.Fatalf("oauth responses_sse Accept = %q, want envelope AcceptHeader %q", got, wantOAuthAccept)
	}
}
