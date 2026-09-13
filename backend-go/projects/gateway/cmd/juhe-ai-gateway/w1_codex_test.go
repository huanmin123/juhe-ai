package main

// w1: chain_driver.go Codex Responses 兼容形态与
// compose_account_balance_refresh.go 协议档案纯函数直测。

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func w1CodexRequest(t *testing.T, body string) *gatewaypreauth.GatewayRequest {
	t.Helper()
	request := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req := gatewaypreauth.NewGatewayRequest(request)
	req.Body = &gatewaybody.Request{RawBody: []byte(body), Body: mustJSONMap(t, body)}
	return req
}

func TestW1ParseOpenAIClientCompatibilityJSONObject(t *testing.T) {
	// nil req：空对象。
	object, err := parseOpenAIClientCompatibilityJSONObject(nil)
	if err != nil || len(object) != 0 {
		t.Fatalf("nil req = %v, %v", object, err)
	}
	// 挂载 JSON body：克隆返回。
	object, err = parseOpenAIClientCompatibilityJSONObject(w1CodexRequest(t, `{"model":"gpt-5","input":"hi"}`))
	if err != nil || object["model"] != "gpt-5" {
		t.Fatalf("parsed = %v, %v", object, err)
	}
	// 无 body：空对象。
	empty := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/responses", nil))
	object, err = parseOpenAIClientCompatibilityJSONObject(empty)
	if err != nil || len(object) != 0 {
		t.Fatalf("无 body = %v, %v", object, err)
	}
	// 无挂载 body 但有 rawBody：二次解析路径。
	rawOnly := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/responses", nil))
	rawOnly.Body = &gatewaybody.Request{RawBody: []byte(`{"model":"gpt-x"}`)}
	object, err = parseOpenAIClientCompatibilityJSONObject(rawOnly)
	if err != nil || object["model"] != "gpt-x" {
		t.Fatalf("raw 解析 = %v, %v", object, err)
	}
	// rawBody 非法 JSON：错误。
	broken := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/responses", nil))
	broken.Body = &gatewaybody.Request{RawBody: []byte("not-json")}
	if _, err := parseOpenAIClientCompatibilityJSONObject(broken); err == nil {
		t.Fatal("非法 JSON 必须报错")
	}
	// rawBody 是数组：非对象错误。
	arrayBody := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/responses", nil))
	arrayBody.Body = &gatewaybody.Request{RawBody: []byte("[1,2]")}
	if _, err := parseOpenAIClientCompatibilityJSONObject(arrayBody); err == nil {
		t.Fatal("数组 body 必须报错")
	}
}

func TestW1EnsureCodexResponsesReasoningEncryptedContent(t *testing.T) {
	// nil：补默认。
	got := ensureCodexResponsesReasoningEncryptedContent(nil)
	if len(got) != 1 || got[0] != "reasoning.encrypted_content" {
		t.Fatalf("nil = %v", got)
	}
	// 已存在：不重复。
	existing := []any{"reasoning.encrypted_content", "other"}
	got = ensureCodexResponsesReasoningEncryptedContent(existing)
	if len(got) != 2 {
		t.Fatalf("已有 = %v", got)
	}
	// 有其他项：追加。
	got = ensureCodexResponsesReasoningEncryptedContent([]any{"other", "  "})
	if len(got) != 2 || got[0] != "other" || got[1] != "reasoning.encrypted_content" {
		t.Fatalf("追加 = %v", got)
	}
	// codexResponsesInputHasAdditionalTools。
	if codexResponsesInputHasAdditionalTools(nil) {
		t.Fatal("nil 必须false")
	}
	if !codexResponsesInputHasAdditionalTools([]any{map[string]any{"type": "additional_tools"}}) {
		t.Fatal("additional_tools 必须命中")
	}
}

func TestW1ApplyCodexResponsesCompatibility(t *testing.T) {
	// 字符串 input → message 数组；缺省字段补齐；预算字段删除。
	body := map[string]any{"input": "原始问题", "max_output_tokens": 100, "temperature": 0.5}
	applyCodexResponsesCompatibility(body)
	items, ok := body["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %#v", body["input"])
	}
	message := items[0].(map[string]any)
	if message["role"] != "user" {
		t.Fatalf("message = %#v", message)
	}
	if body["instructions"] != "" || body["stream"] != true || body["store"] != false {
		t.Fatalf("缺省字段 = %+v", body)
	}
	if body["tool_choice"] != "auto" || body["parallel_tool_calls"] != true {
		t.Fatalf("工具缺省 = %+v", body)
	}
	if _, has := body["tools"]; !has {
		t.Fatal("tools 必须补齐空数组")
	}
	for _, removed := range []string{"max_output_tokens", "temperature", "top_p", "truncation", "user"} {
		if _, has := body[removed]; has {
			t.Fatalf("%s 必须删除", removed)
		}
	}
	// 数组 input：system→developer 归一 + additional_tools 保留 tools。
	body2 := map[string]any{"input": []any{map[string]any{"role": "system", "content": "指令"}}}
	applyCodexResponsesCompatibility(body2)
	items = body2["input"].([]any)
	if items[0].(map[string]any)["role"] != "developer" {
		t.Fatalf("system 归一 = %#v", items[0])
	}
	if _, has := body2["tools"]; !has {
		t.Fatal("无 additional_tools 必须补 tools")
	}
	// additional_tools 场景：不覆盖已有 tools 缺省。
	body3 := map[string]any{"input": []any{map[string]any{"type": "additional_tools"}}}
	applyCodexResponsesCompatibility(body3)
	if _, has := body3["tools"]; has {
		t.Fatal("additional_tools 场景不得注入空 tools")
	}
}

func TestW1ProviderModelSupportsProtocolProfile(t *testing.T) {
	// 无协议清单：全部支持。
	if !providerModelSupportsProtocolProfile(nil, "openai", "openai") {
		t.Fatal("空清单必须支持")
	}
	// gpt 提供方：直接支持。
	if !providerModelSupportsProtocolProfile([]string{"messages"}, "GPT", "openai") {
		t.Fatal("gpt 必须支持")
	}
	// openai 档案 + 模型含 chat_completions。
	if !providerModelSupportsProtocolProfile([]string{"chat_completions"}, "openai", "openai") {
		t.Fatal("openai chat 必须支持")
	}
	// 交集缺失：false。
	if providerModelSupportsProtocolProfile([]string{"messages"}, "openai", "openai") {
		t.Fatal("openai 档案不含 messages")
	}
	// anthropic/gemini 档案。
	if !providerModelSupportsProtocolProfile([]string{"messages"}, "anthropic", "anthropic") {
		t.Fatal("anthropic messages 必须支持")
	}
	if !providerModelSupportsProtocolProfile([]string{"count_tokens"}, "gemini", "gemini") {
		t.Fatal("gemini count_tokens 必须支持")
	}
	// 未知协议：空集合。
	if providerModelSupportsProtocolProfile([]string{"chat_completions"}, "unknown", "bogus") {
		t.Fatal("未知协议必须 false")
	}
	if got := defaultProtocolFamilies(" OpenAI "); !got["responses"] {
		t.Fatalf("default families = %v", got)
	}
}
