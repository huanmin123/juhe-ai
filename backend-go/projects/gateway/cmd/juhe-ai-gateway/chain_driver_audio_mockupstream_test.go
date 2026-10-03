package main

// M1 同步音频 chain 层 live 上游回环（音频设计 §5/契约 §5.1）：gemini 账户的
// speech 请求经 driver 改写 URL/body 后打到 mockupstream 的
// media_gemini_tts_ok 场景，断言三件事：
//   - 上游实际收到的 URL 是 /v1beta/models/{model}:generateContent（客户端
//     /v1/audio/speech 被改写），body 是转换后的 contents/speechConfig 报文；
//   - gemini 认证头（X-Goog-Api-Key）随 driver 产物注入；
//   - 上游 inlineData base64 PCM 响应经 SpeechAdapter.TransformResponse 得到
//     裸 PCM 字节与 audio/L16;rate=24000（响应转换链的入参事实与非流式
//     gatewayresponse 转换共用同一 adapter）。
import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

func TestChainDriverGeminiSpeechUpstreamRoundTrip(t *testing.T) {
	mock := platformmock.New()
	defer mock.Close()

	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID:           "acc_gem_tts_live",
		ProviderCode: "gemini",
		ProtocolCode: "gemini",
		Type:         "api_key",
		BaseURL:      mock.URL,
		APIKey:       "gk-live-1",
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/audio/speech",
		`{"model":"gemini-2.5-flash-preview-tts","input":"Say cheerfully: hi","voice":"Kore","response_format":"pcm"}`)

	urls, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err != nil {
		t.Fatalf("build urls: %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("urls = %#v", urls)
	}
	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	if got := parts.Headers.Get("X-Goog-Api-Key"); got != "gk-live-1" {
		t.Fatalf("gemini auth header = %q", got)
	}

	// 用 driver 产物直连 mock 上游（media_gemini_tts_ok 场景显式点名）。
	upstream, err := http.NewRequest(http.MethodPost, urls[0], bytes.NewReader(parts.Body))
	if err != nil {
		t.Fatalf("new upstream request: %v", err)
	}
	for name, values := range parts.Headers {
		for _, value := range values {
			upstream.Header.Add(name, value)
		}
	}
	upstream.Header.Set("X-Mock-Scenario", string(platformmock.ScenarioMediaGeminiTTSOK))
	response, err := http.DefaultClient.Do(upstream)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read upstream body: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("upstream status = %d body = %s", response.StatusCode, body)
	}

	// 上游侧录制断言：URL 改写 + body 转换。
	requests := mock.Requests()
	if len(requests) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(requests))
	}
	recorded := requests[0]
	if recorded.Path != "/v1beta/models/gemini-2.5-flash-preview-tts:generateContent" {
		t.Fatalf("upstream path = %q, want gemini generateContent rewrite", recorded.Path)
	}
	if !strings.Contains(recorded.Body, `"responseModalities":["AUDIO"]`) ||
		!strings.Contains(recorded.Body, `"voiceName":"Kore"`) ||
		!strings.Contains(recorded.Body, "Say cheerfully: hi") {
		t.Fatalf("upstream body must be the transformed gemini speech payload: %s", recorded.Body)
	}

	// 响应转换：inlineData base64 PCM → 裸 PCM + audio/L16;rate=24000。
	adapter := gatewaymedia.SpeechAdapterForProvider("gemini")
	if adapter == nil {
		t.Fatal("gemini speech adapter missing")
	}
	audio, contentType, err := adapter.TransformResponse(body)
	if err != nil {
		t.Fatalf("transform response: %v", err)
	}
	if contentType != gatewaymedia.GeminiSpeechOutputContentType {
		t.Fatalf("content type = %q, want %q", contentType, gatewaymedia.GeminiSpeechOutputContentType)
	}
	var decoded struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if len(decoded.Candidates) == 0 || len(decoded.Candidates[0].Content.Parts) == 0 {
		t.Fatalf("upstream body missing candidates/parts: %s", body)
	}
	raw, err := base64.StdEncoding.DecodeString(decoded.Candidates[0].Content.Parts[0].InlineData.Data)
	if err != nil {
		t.Fatalf("decode inlineData: %v", err)
	}
	if len(audio) == 0 || !bytes.Equal(audio, raw) {
		t.Fatalf("transformed audio must equal the upstream inlineData payload (%d vs %d bytes)", len(audio), len(raw))
	}
}

// TestChainDriverGeminiSpeechFormatGateIsLocal400：response_format 非 pcm 的
// 能力边界错误（契约 §5.1：零转码）必须以 GatewayRequestValidationError
// （400 / invalid_request_error）语义上抛——该类型沿 dispatch 错误通路由
// HandleGatewayRequestKnownErrorResponse 渲染客户端 400；若退化为裸 error
// 会落入 503"上游暂时不可用"契约，把客户端参数错误误报成上游故障。
func TestChainDriverGeminiSpeechFormatGateIsLocal400(t *testing.T) {
	driver := newChainProviderDriver()
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_gem_tts_400", ProviderCode: "gemini", ProtocolCode: "gemini",
		Type: "api_key", BaseURL: "https://gem.example", APIKey: "gk-1",
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/audio/speech",
		`{"model":"gemini-2.5-flash-preview-tts","input":"hi","voice":"Kore","response_format":"mp3"}`)

	_, err := driver.BuildGatewayUpstreamURLsForAccount(context.Background(), account, req)
	if err == nil {
		t.Fatal("non-pcm response_format must be rejected")
	}
	var validation *gatewaypreauth.GatewayRequestValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("format gate error must be a GatewayRequestValidationError (local 400), got %T: %v", err, err)
	}
	if validation.StatusCode != http.StatusBadRequest {
		t.Fatalf("validation status = %d, want 400", validation.StatusCode)
	}
	if !strings.Contains(validation.Message, "pcm") {
		t.Fatalf("validation message must carry the pcm boundary: %q", validation.Message)
	}

	// prepared-parts 分派点同语义（URL 与 body 同源 BuildRequest）。
	_, err = driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if !errors.As(err, &validation) {
		t.Fatalf("parts format gate error must be a GatewayRequestValidationError, got %T: %v", err, err)
	}
}
