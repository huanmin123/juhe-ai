package gatewaydispatch

// w14e 覆盖率补强：纯函数助手的错误臂与边界分支（第一批）。

import (
	"errors"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

func errorsNewW14E(message string) error { return errors.New(message) }

func errorsIsW14E(err, target error) bool { return errors.Is(err, target) }

func TestW14EErrorHelpersTransportArms(t *testing.T) {
	if got := errorTypeForTransport(TransportFailureKindTimeout); got != "upstream_timeout_error" {
		t.Fatalf("timeout type = %q", got)
	}
	if got := errorTypeForTransport(TransportFailureKindConnection); got != "upstream_transport_error" {
		t.Fatalf("transport type = %q", got)
	}
	if got := errorTypeForTransport(""); got != "" {
		t.Fatalf("empty type = %q", got)
	}
	if got := errorCodeForTransport(TransportFailureKindTimeout); got != "upstream_timeout" {
		t.Fatalf("timeout code = %q", got)
	}
	if got := errorCodeForTransport(TransportFailureKindConnection); got != "upstream_"+TransportFailureKindConnection {
		t.Fatalf("transport code = %q", got)
	}
	if got := errorCodeForTransport(""); got != "" {
		t.Fatalf("empty code = %q", got)
	}
}

func TestW14EErrorHelpersPayloadArms(t *testing.T) {
	if got := headersFromObject(nil); len(got) != 0 {
		t.Fatalf("nil headers = %v", got)
	}
	withValues := headersFromObject(map[string]string{"X-Test": "a"})
	if got := withValues.Get("X-Test"); got != "a" {
		t.Fatalf("header value = %q", got)
	}
	if got := parseGatewayNonStreamJSONBody("", nil); got != nil {
		t.Fatalf("empty body = %v", got)
	}
	if got := parseGatewayNonStreamJSONBody("[1]", nil); got != nil {
		t.Fatalf("array body = %v", got)
	}
	if got := parseGatewayNonStreamJSONBody(`{"error":{"message":"boom","type":"t","code":"c","extra":1}}`, nil); got == nil {
		t.Fatal("object body must parse")
	}
	if hasErrorObject(nil) {
		t.Fatal("nil payload has no error object")
	}
	if hasErrorObject(map[string]any{"error": "string"}) {
		t.Fatal("string error is not an object")
	}
	if !hasErrorObject(map[string]any{"error": map[string]any{"message": "m"}}) {
		t.Fatal("error object must be detected")
	}
	payload := gatewayErrorPayloadFromObject(map[string]any{
		"error": map[string]any{"message": " m ", "type": "t", "code": "c", "ratelimit": 3},
	})
	if payload.Error.Message != "m" || payload.Error.Type != "t" || payload.Error.Code != "c" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Error.Extra == nil || payload.Error.Extra["ratelimit"] != 3 {
		t.Fatalf("extra = %+v", payload.Error.Extra)
	}
	if isHTTPStatusCode(399) || !isHTTPStatusCode(400) || !isHTTPStatusCode(599) || isHTTPStatusCode(600) {
		t.Fatal("isHTTPStatusCode boundaries wrong")
	}
	if got := firstNonEmpty(" ", "", "x"); got != "x" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := stringValue(7); got != "" {
		t.Fatalf("stringValue(7) = %q", got)
	}
	if got := stringValue("  v "); got != "v" {
		t.Fatalf("stringValue = %q", got)
	}
	if got := parseJSONObject("{bad"); got != nil {
		t.Fatalf("invalid object = %v", got)
	}
}

func TestW14EUtilAndErrorsArms(t *testing.T) {
	if got := gatewayupstream.JSONCloneValue(map[string]any{"a": 1}); got == nil {
		t.Fatal("clone must succeed")
	}
	if got := gatewayupstream.JSONCloneValue(func() {}); got != nil {
		t.Fatal("unmarshalable value must clone to nil")
	}
	started := &StartedBodyTransportError{Err: errorsNewW14E("boom")}
	if started.Error() != "boom" {
		t.Fatalf("started error = %q", started.Error())
	}
	if errorsIsW14E(started, started.Err) == false {
		// Unwrap 链必须能找到原始错误。
		t.Fatal("unwrap must expose the original error")
	}
	primary := &PrimaryStartedGatewayTransportError{Err: started}
	if !IsProvenUpstreamBodyTransportError(primary) {
		t.Fatal("primary started error must be proven transport")
	}
}

func TestW14ECandFilterHelpers(t *testing.T) {
	if got := GatewayModelFilterFailureMessage(ModelFilterResult{Reason: "missing_model"}); !strings.Contains(got, "缺少 model") {
		t.Fatalf("missing model message = %q", got)
	}
	if got := GatewayModelFilterFailureMessage(ModelFilterResult{RequestedModel: ""}); !strings.Contains(got, "未知模型") {
		t.Fatalf("unknown model message = %q", got)
	}
	if got := GatewayModelFilterFailureMessage(ModelFilterResult{RequestedModel: "gpt-9"}); !strings.Contains(got, "gpt-9") {
		t.Fatalf("named model message = %q", got)
	}
	if got := GatewayAccountModelPriorityFor("a", nil); got != ModelPriorityRankDirect {
		t.Fatalf("nil priority rank = %d", got)
	}
	priority := &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{"a": ModelPriorityRankMapping, "b": ModelPriorityRankUnsupported}}
	if got := GatewayAccountModelPriorityFor("a", priority); got != ModelPriorityRankMapping {
		t.Fatalf("mapped rank = %d", got)
	}
	if got := GatewayAccountModelPriorityFor("missing", priority); got != ModelPriorityRankUnsupported {
		t.Fatalf("missing rank = %d", got)
	}
	if isMappingAllowedBySupportedModels("m", nil) {
		t.Fatal("empty supported list must reject")
	}
	if !isMappingAllowedBySupportedModels("m", []string{"m"}) {
		t.Fatal("matching model must pass")
	}
	if resolveGatewayAccountModelMatch("", []string{"m"}) {
		t.Fatal("empty requested model must not match")
	}
	if !resolveGatewayAccountModelMatch("m", []string{"m"}) {
		t.Fatal("direct match must pass")
	}
	if got := openAIEndpointFamilyFromPath(""); got != "" {
		t.Fatalf("empty path family = %q", got)
	}
	if got := openAIEndpointFamilyFromPath("/v1/Chat/Completions?x=1"); got != gatewayrouting.EndpointFamilyChatCompletions {
		t.Fatalf("chat family = %q", got)
	}
	if got := openAIEndpointFamilyFromPath("/v1/responses"); got != gatewayrouting.EndpointFamilyResponses {
		t.Fatalf("responses family = %q", got)
	}
	if got := openAIEndpointFamilyFromPath("/v1/other"); got != "" {
		t.Fatalf("other family = %q", got)
	}
	if got := splitPath("/v1/messages?beta=true"); got != "/v1/messages" {
		t.Fatalf("splitPath = %q", got)
	}
	if got := splitPath("/v1/messages"); got != "/v1/messages" {
		t.Fatalf("splitPath no query = %q", got)
	}
}

func TestW14EHotQualityProtocolProfileOf(t *testing.T) {
	if got := hotQualityProtocolProfileOf(AccountCandidate{ProviderProtocolProfileID: "prof"}); got != "prof" {
		t.Fatalf("profile id = %q", got)
	}
	if got := hotQualityProtocolProfileOf(AccountCandidate{ProtocolCode: "openai", ProtocolVersion: "v1"}); got != "openai:v1" {
		t.Fatalf("fallback profile = %q", got)
	}
}

func TestW14EHeaderPolicyArms(t *testing.T) {
	// 官方 OAuth 客户端头策略：通用名 + 各 profile 前缀。
	if !isAllowedOfficialOAuthClientHeader("User-Agent", OAuthHeaderProfileOpenAICodex) {
		t.Fatal("common header must be allowed")
	}
	if !isAllowedOfficialOAuthClientHeader("x-codex-anything", OAuthHeaderProfileOpenAICodex) {
		t.Fatal("codex prefix must be allowed")
	}
	if !isAllowedOfficialOAuthClientHeader("x-claude-code-x", OAuthHeaderProfileAnthropicClaude) {
		t.Fatal("claude code prefix must be allowed")
	}
	if !isAllowedOfficialOAuthClientHeader("x-stainless-os", OAuthHeaderProfileAnthropicClaude) {
		t.Fatal("stainless prefix must be allowed")
	}
	if isAllowedOfficialOAuthClientHeader("x-codex-anything", OAuthHeaderProfileGeminiCLI) {
		t.Fatal("codex prefix must not match gemini profile")
	}
	if isAllowedOfficialOAuthClientHeader("x-unknown", OAuthHeaderProfileOpenAICodex) {
		t.Fatal("unknown header must be rejected")
	}
	// Gemini scoped header 判定。
	if !IsGeminiGenerateContentScopedHeaderName("x-gemini-client") {
		t.Fatal("gemini prefix must match")
	}
	if IsGeminiGenerateContentScopedHeaderName("x-other") {
		t.Fatal("unknown header must not match gemini")
	}
	// Anthropic scoped header 判定。
	if !IsAnthropicMessagesScopedHeaderName("anthropic-beta") {
		t.Fatal("anthropic header must match prefix")
	}
	if !IsAnthropicMessagesScopedHeaderName("x-api-key") {
		t.Fatal("anthropic header must match name set")
	}
	// 头拷贝：非法名剔除、合法名合并。
	input := IncomingHeaders{"User-Agent": {"a", "b"}, "X-Private": {"x"}}
	output := CopyOfficialOAuthClientRequestHeaders(input, OAuthHeaderProfileOpenAICodex)
	if output.Get("User-Agent") != "a, b" {
		t.Fatalf("copied header = %q", output.Get("User-Agent"))
	}
	if output.Get("X-Private") != "" {
		t.Fatal("private header must be dropped")
	}
	if got := CopyOfficialOAuthClientRequestHeaders(nil, OAuthHeaderProfileOpenAICodex); len(got) != 0 {
		t.Fatal("nil input must yield empty header")
	}
	StripAnthropicMessagesScopedHeaders(output)
	StripGeminiGenerateContentScopedHeaders(output)
}

type w14eNamedCodedError struct{}

func (w14eNamedCodedError) Error() string { return "named" }
func (w14eNamedCodedError) Name() string  { return "w14e_name" }
func (w14eNamedCodedError) Code() string  { return "w14e_code" }

func TestW14ECircuitFacadeErrorNameArms(t *testing.T) {
	if got := errorNameOf(w14eNamedCodedError{}); got != "w14e_name" {
		t.Fatalf("error name = %q", got)
	}
	if got := errorCodeOf(w14eNamedCodedError{}); got != "w14e_code" {
		t.Fatalf("error code = %q", got)
	}
	if got := errorNameOf(errorsNewW14E("plain")); got != "" {
		t.Fatalf("plain error name = %q", got)
	}
	if got := errorCodeOf(errorsNewW14E("plain")); got != "" {
		t.Fatalf("plain error code = %q", got)
	}
}
