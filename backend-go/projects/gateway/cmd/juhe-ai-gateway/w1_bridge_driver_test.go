package main

// w1: chain_bridge_response.go / chain_driver.go 的纯函数与投影直测。

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestW1BridgePureHelpers(t *testing.T) {
	if got := chainBridgePreviousResponseIDOf(nil); got != "" {
		t.Fatalf("nil body = %q", got)
	}
	if got := chainBridgePreviousResponseIDOf(map[string]any{"previous_response_id": "resp_1"}); got != "resp_1" {
		t.Fatalf("id = %q", got)
	}
	if got := chainBridgePreviousResponseIDOf(map[string]any{"previous_response_id": 42}); got != "" {
		t.Fatalf("非字符串 = %q", got)
	}
	rendered := bridgeChatUpstreamInvalidJSONErrorBody("gemini-2", "解析失败")
	if !strings.Contains(rendered, `"modelVersion":"gemini-2"`) || !strings.Contains(rendered, `"finishReason":"STOP"`) || !strings.Contains(rendered, "解析失败") {
		t.Fatalf("guidance = %s", rendered)
	}
}

func TestW1DriverCodexProjections(t *testing.T) {
	boundGroup := "grp_1"
	account := gatewaydispatch.AccountCandidate{
		ID: "acc_1", APIKey: "sk-codex", SystemAccountID: "sys_1", BoundGroupID: &boundGroup,
		Credentials: map[string]any{"access_token": "tok"},
	}
	codex := codexAccountOf(account)
	if codex.ID != "acc_1" || codex.APIKey != "sk-codex" || codex.Credentials == nil {
		t.Fatalf("codex account = %+v", codex)
	}
	identity := codexIdentityOf(account)
	if identity.SystemAccountID != "sys_1" || identity.APIKeyID != "acc_1" || identity.GroupID != "grp_1" {
		t.Fatalf("codex identity = %+v", identity)
	}
	// 无绑定组：GroupID 空。
	identity = codexIdentityOf(gatewaydispatch.AccountCandidate{ID: "acc_2", SystemAccountID: "sys_2"})
	if identity.GroupID != "" {
		t.Fatalf("nil 绑定组 = %q", identity.GroupID)
	}
}

func TestW1ChainStripGatewayVersionPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/v1/chat/completions", "/chat/completions"},
		{"/v1beta/models", "/models"},
		{"/v1", "/"},
		{"/v1beta", "/"},
		{"/other", "/other"},
	}
	for _, testCase := range cases {
		if got := chainStripGatewayVersionPrefix(testCase.in); got != testCase.want {
			t.Fatalf("strip(%q) = %q，want %q", testCase.in, got, testCase.want)
		}
	}
}

func TestW1NormalizeCodexResponsesInputItems(t *testing.T) {
	items := []any{
		map[string]any{"role": "system", "content": "指令"},
		map[string]any{"role": "user", "content": "问题"},
		"非对象元素",
	}
	out := normalizeCodexResponsesInputItems(items)
	if len(out) != 3 {
		t.Fatalf("items = %d", len(out))
	}
	first, ok := out[0].(map[string]any)
	if !ok || first["role"] != "developer" || first["content"] != "指令" {
		t.Fatalf("system 转换 = %#v", out[0])
	}
	second := out[1].(map[string]any)
	if second["role"] != "user" {
		t.Fatalf("user 保留 = %#v", second)
	}
	if out[2] != "非对象元素" {
		t.Fatalf("非对象保留 = %#v", out[2])
	}
}

func TestW1ClientErrorProtocol(t *testing.T) {
	// 未识别请求回落 openai。
	if got := clientErrorProtocol(gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))); got != gatewaypreauth.GatewayErrorProtocolOpenAI {
		t.Fatalf("openai 协议 = %q", got)
	}
}
