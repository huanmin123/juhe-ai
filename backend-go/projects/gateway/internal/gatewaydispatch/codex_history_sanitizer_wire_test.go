package gatewaydispatch

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayoauthcodex"
)

func withCodexHistorySanitizerWired(t *testing.T) {
	t.Helper()
	previous := gatewayoauthcodex.SanitizeCodexHistory
	t.Cleanup(func() { gatewayoauthcodex.SanitizeCodexHistory = previous })
	WireCodexHistorySanitizer()
}

func TestWireCodexHistorySanitizerStripsCrossUpstreamItemIDs(t *testing.T) {
	withCodexHistorySanitizerWired(t)

	// 用户实证场景：Grok 来源的 history item id 在会话切到真实 GPT 上游后
	// 不被上游接受。Node 在 store=false / persistence=none 下会把所有已知
	// 类型 item 的 id 剥离；接线后 Go 必须产生同样的结果。
	items := []any{
		map[string]any{"type": "message", "role": "user", "content": []any{}, "id": "msg_grok_origin"},
		map[string]any{"type": "message", "role": "assistant", "content": []any{}, "id": "msg_gpt_origin"},
		map[string]any{"type": "message", "role": "user", "content": []any{}},
	}
	result, ok := sanitizeCodexHistoryHookIfSet(items, SanitizeCodexHistoryOptions{
		Store:                  false,
		TargetScopeKey:         "account:gpt-account",
		TargetPersistenceScope: "none",
	})
	if !ok || !result.Changed {
		t.Fatalf("sanitizer not applied: ok=%v changed=%v", ok, result.Changed)
	}
	for index, item := range result.Items {
		if _, hasID := item.(map[string]any)["id"]; hasID {
			t.Fatalf("item %d still has id: %#v", index, item)
		}
	}
}

func TestWireCodexHistorySanitizerAppliesToOAuthAdapterPath(t *testing.T) {
	withCodexHistorySanitizerWired(t)

	body := map[string]any{"input": []any{
		map[string]any{"type": "message", "role": "user", "content": []any{}, "id": "msg_grok_origin"},
	}}
	sanitizeOpenAIOAuthCodexHistory(body, "gpt-account")
	item, ok := body["input"].([]any)[0].(map[string]any)
	if !ok {
		t.Fatalf("input = %#v", body["input"])
	}
	if _, hasID := item["id"]; hasID {
		t.Fatalf("OAuth adapter path still has id: %#v", item)
	}
}

func TestWireCodexHistorySanitizerLeavesUnknownItemTypes(t *testing.T) {
	withCodexHistorySanitizerWired(t)

	items := []any{map[string]any{"type": "unknown_item", "id": "x_1"}}
	result, ok := sanitizeCodexHistoryHookIfSet(items, SanitizeCodexHistoryOptions{
		Store:                  false,
		TargetScopeKey:         "account:gpt-account",
		TargetPersistenceScope: "none",
	})
	if !ok {
		t.Fatal("sanitizer hook must be installed")
	}
	if result.Changed {
		t.Fatalf("unknown item type must be left intact: %#v", result.Items)
	}
}
