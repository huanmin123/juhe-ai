package main

// 流式零计费修复 + 火山语音凭据显式错误的 chain_driver 层测试：
//   - openai 协议标准账户 chat completions 流式出站注入
//     stream_options.include_usage=true（触发条件逐项钉住：客户端显式带参保持
//     原样、stream=false 不注入、非 chat 出站路径不注入、responses→chat 桥改写
//     后的最终出站路径注入）；
//   - volcengine 账户缺语音双值凭据以 GatewayRequestValidationError（400 +
//     speech_credentials_missing）显式失败——不再落入 503 upstream_retryable
//     的未预期异常渲染；凭据齐全路径零回归。
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func streamUsageInjectAccount() gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{
		ID:           "acc_chat_usage_inject",
		ProviderCode: "openai",
		ProtocolCode: "openai",
		Type:         "api_key",
		BaseURL:      "https://api.example",
		APIKey:       "gk-1",
	}
}

func decodePartsBodyObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode parts body: %v body=%s", err, body)
	}
	return parsed
}

func streamOptionsOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	options, ok := body["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("body missing stream_options object: %#v", body["stream_options"])
	}
	return options
}

// TestChainDriverInjectsStreamUsageOptionForChatStream：客户端未带
// stream_options 的流式 chat 出站 body 注入 include_usage=true，注入发生在
// 顶层对象首键前（字节级最小 splice），其余字段零扰动。
func TestChainDriverInjectsStreamUsageOptionForChatStream(t *testing.T) {
	driver := newChainProviderDriver()
	account := streamUsageInjectAccount()
	clientBody := `{"model":"gpt-x","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/chat/completions", clientBody)

	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	const injectedPrefix = `{"stream_options":{"include_usage":true},`
	if !bytes.HasPrefix(parts.Body, []byte(injectedPrefix)) {
		t.Fatalf("injected body must start with %s, got %s", injectedPrefix, parts.Body)
	}
	body := decodePartsBodyObject(t, parts.Body)
	options := streamOptionsOf(t, body)
	if options["include_usage"] != true {
		t.Fatalf("include_usage = %#v, want true", options["include_usage"])
	}
	if body["model"] != "gpt-x" || body["stream"] != true {
		t.Fatalf("注入不得扰动既有字段: %#v", body)
	}
	if _, ok := body["messages"]; !ok {
		t.Fatalf("messages 字段丢失: %#v", body)
	}

	// URL 构造零改动：chat 路径维持 openai 归一。
	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build urls: %v", err)
	}
	if len(urls) != 1 || !strings.Contains(urls[0], "/v1/chat/completions") {
		t.Fatalf("urls = %#v", urls)
	}
}

// TestChainDriverKeepsExplicitStreamUsageOption：客户端显式带
// include_usage=true 时出站 body 保持原样（字节级不变）。
func TestChainDriverKeepsExplicitStreamUsageOption(t *testing.T) {
	driver := newChainProviderDriver()
	clientBody := `{"model":"gpt-x","stream":true,"stream_options":{"include_usage":true},"messages":[]}`
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/chat/completions", clientBody)

	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, streamUsageInjectAccount(), gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	if !bytes.Equal(parts.Body, []byte(clientBody)) {
		t.Fatalf("explicit include_usage body must stay byte-equal:\n got %s\nwant %s", parts.Body, clientBody)
	}
}

// TestChainDriverInjectsWhenStreamUsageNotTrue：stream_options 存在但
// include_usage != true（缺失/false/非对象）时按契约注入 true。
func TestChainDriverInjectsWhenStreamUsageNotTrue(t *testing.T) {
	driver := newChainProviderDriver()
	cases := []struct {
		name       string
		clientBody string
	}{
		{"空对象", `{"model":"gpt-x","stream":true,"stream_options":{},"messages":[]}`},
		{"显式 false", `{"model":"gpt-x","stream":true,"stream_options":{"include_usage":false},"messages":[]}`},
		{"非对象 null", `{"model":"gpt-x","stream":true,"stream_options":null,"messages":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mappedURLTestRequest(t, http.MethodPost, "/v1/chat/completions", tc.clientBody)
			parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, streamUsageInjectAccount(), gatewaydispatch.UsageIdentity{}, "")
			if err != nil {
				t.Fatalf("build parts: %v", err)
			}
			options := streamOptionsOf(t, decodePartsBodyObject(t, parts.Body))
			if options["include_usage"] != true {
				t.Fatalf("include_usage = %#v, want true", options["include_usage"])
			}
			if _, ok := options["include_usage"]; !ok {
				t.Fatalf("stream_options 键丢失: %#v", options)
			}
		})
	}
}

// TestChainDriverSkipsInjectForNonStreamAndNonChatOutbound：stream=false 与
// 非 chat 出站路径（/responses 原生直通）零注入（字节级不变）。
func TestChainDriverSkipsInjectForNonStreamAndNonChatOutbound(t *testing.T) {
	driver := newChainProviderDriver()
	cases := []struct {
		name       string
		target     string
		clientBody string
	}{
		{"非流式 chat", "/v1/chat/completions", `{"model":"gpt-x","stream":false,"messages":[]}`},
		{"responses 原生路径", "/v1/responses", `{"model":"gpt-x","stream":true,"input":"hi"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mappedURLTestRequest(t, http.MethodPost, tc.target, tc.clientBody)
			parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, streamUsageInjectAccount(), gatewaydispatch.UsageIdentity{}, "")
			if err != nil {
				t.Fatalf("build parts: %v", err)
			}
			if !bytes.Equal(parts.Body, []byte(tc.clientBody)) {
				t.Fatalf("body must stay byte-equal:\n got %s\nwant %s", parts.Body, tc.clientBody)
			}
		})
	}
}

// TestChainDriverSkipsInjectForNonOpenAIProtocol：非 openai 协议账户（跨协议
// 面不属注入对象）零注入。
func TestChainDriverSkipsInjectForNonOpenAIProtocol(t *testing.T) {
	driver := newChainProviderDriver()
	clientBody := `{"model":"claude-x","stream":true,"messages":[]}`
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/chat/completions", clientBody)
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_anthropic", ProviderCode: "anthropic", ProtocolCode: "anthropic",
		Type: "api_key", BaseURL: "https://ant.example", APIKey: "gk-2",
	}
	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	if bytes.Contains(parts.Body, []byte(`"stream_options"`)) {
		t.Fatalf("非 openai 协议不得注入 stream_options: %s", parts.Body)
	}
}

// TestChainDriverInjectsForResponsesToChatBridge：模型映射 responses→chat
// 改写后的最终出站路径为 /chat/completions，注入照常生效（出站模型为映射
// 上游拼写，include_usage 补齐）。
func TestChainDriverInjectsForResponsesToChatBridge(t *testing.T) {
	driver := newChainProviderDriver()
	account := streamUsageInjectAccount()
	account.ProviderCode = "hybrid"
	account.ModelMappings = []gatewayruntimecache.AccountModelMapping{{
		SourceModel:            "gpt-x",
		SourceEndpointFamily:   "responses",
		UpstreamModel:          "doubao-seed",
		UpstreamEndpointFamily: "chat_completions",
		Enabled:                true,
	}}
	clientBody := `{"model":"gpt-x","stream":true,"input":"hi"}`
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/responses", clientBody)

	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build urls: %v", err)
	}
	if len(urls) != 1 || !strings.Contains(urls[0], "/chat/completions") {
		t.Fatalf("urls = %#v, want responses→chat 改写", urls)
	}
	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	body := decodePartsBodyObject(t, parts.Body)
	if body["model"] != "doubao-seed" {
		t.Fatalf("model = %#v, want 映射上游拼写 doubao-seed", body["model"])
	}
	options := streamOptionsOf(t, body)
	if options["include_usage"] != true {
		t.Fatalf("include_usage = %#v, want true（桥改写后的 chat 流式出站同样注入）", options["include_usage"])
	}
}

// TestChainDriverVolcengineSpeechMissingCredentialsIsLocal400：缺语音双值
// 凭据以 GatewayRequestValidationError（400 + speech_credentials_missing，
// message 保留原文语义）在 URL/parts 两个消费点显式失败——不再落入 503
// upstream_retryable 的未预期异常渲染。
func TestChainDriverVolcengineSpeechMissingCredentialsIsLocal400(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_vol_tts_nocred", ProviderCode: "volcengine", ProtocolCode: "openai",
		Type: "api_key", BaseURL: "https://ark.example", APIKey: "ark-key",
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/audio/speech",
		`{"model":"doubao-tts","input":"hi","voice":"BV700_streaming"}`)

	_, urlErr := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if urlErr == nil {
		t.Fatal("missing speech credentials must be rejected")
	}
	var validation *gatewaypreauth.GatewayRequestValidationError
	if !errors.As(urlErr, &validation) {
		t.Fatalf("url error must be GatewayRequestValidationError (local 400), got %T: %v", urlErr, urlErr)
	}
	if validation.StatusCode != http.StatusBadRequest {
		t.Fatalf("validation status = %d, want 400", validation.StatusCode)
	}
	if validation.Code != "speech_credentials_missing" {
		t.Fatalf("validation code = %q, want speech_credentials_missing", validation.Code)
	}
	if !strings.Contains(validation.Message, "缺少火山语音凭据") {
		t.Fatalf("validation message 必须保留原文语义: %q", validation.Message)
	}

	// prepared-parts 消费点同语义。
	_, partsErr := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if !errors.As(partsErr, &validation) || validation.Code != "speech_credentials_missing" {
		t.Fatalf("parts error must carry the same validation semantics, got %v", partsErr)
	}
}

// TestChainDriverVolcengineSpeechCredentialsRoundTrip：凭据齐全路径零回归
// （2026-10-17 迁移批后的单值语义）——URL 为语音服务域根，鉴权为
// `X-Api-Key: <speech_token>` 单头（通用透传的 ark Authorization 删除，两
// 鉴权头不并存出站）。
func TestChainDriverVolcengineSpeechCredentialsRoundTrip(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_vol_tts_ok", ProviderCode: "volcengine", ProtocolCode: "openai",
		Type: "api_key", BaseURL: "https://ark.example", APIKey: "ark-key",
		Credentials: map[string]any{"speech_token": "volc-tts-token-1"},
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/audio/speech",
		`{"model":"doubao-tts","input":"hi","voice":"BV700_streaming"}`)

	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build urls: %v", err)
	}
	if len(urls) != 1 || !strings.HasPrefix(urls[0], "https://openspeech.bytedance.com/") {
		t.Fatalf("urls = %#v, want 语音服务域根出站", urls)
	}
	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	if got := parts.Headers.Get("X-Api-Key"); got != "volc-tts-token-1" {
		t.Fatalf("X-Api-Key = %q, want volc-tts-token-1（单值语音凭据鉴权面）", got)
	}
	if got := parts.Headers.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want 空（ark Bearer 不得与语音 X-Api-Key 并存出站）", got)
	}
	if bytes.Contains(parts.Body, []byte(`"stream_options"`)) {
		t.Fatalf("TTS 出站与 stream 注入无关，不得携带 stream_options: %s", parts.Body)
	}
}
