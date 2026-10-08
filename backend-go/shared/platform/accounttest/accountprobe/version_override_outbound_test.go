package accountprobe

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// 出站实效（设计 §10）：claudeCode 族自动覆盖生效后，账号探测构造的
// Anthropic messages 载荷系统提示中 cc_version 挑战文本必须携带覆盖版本；
// 清空自动层后回退内置常量拼接值。
func TestBuildAnthropicMessagesPayloadUsesAutoOverrideCCVersion(t *testing.T) {
	t.Cleanup(func() {
		upstreamidentity.SetClientVersionOverrides(nil)
		upstreamidentity.SetClientVersionAutoOverrides(nil)
	})
	upstreamidentity.SetClientVersionAutoOverrides(map[string]string{"claudeCode": "9.9.9"})

	payload, err := buildAnthropicMessagesPayload("claude-sonnet-4-5", "ping", false, "sess-1")
	if err != nil {
		t.Fatalf("buildAnthropicMessagesPayload() err=%v", err)
	}
	var body struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if len(body.System) == 0 {
		t.Fatal("payload system block is empty")
	}
	wantBilling := fmt.Sprintf("x-anthropic-billing-header: cc_version=9.9.9.%s; cc_entrypoint=sdk-cli;", anthropicBuildID)
	if got := body.System[0].Text; got != wantBilling {
		t.Fatalf("system[0].text=%q, want %q", got, wantBilling)
	}

	upstreamidentity.SetClientVersionAutoOverrides(nil)
	payload, err = buildAnthropicMessagesPayload("claude-sonnet-4-5", "ping", false, "sess-1")
	if err != nil {
		t.Fatalf("buildAnthropicMessagesPayload() after clearing err=%v", err)
	}
	body = struct {
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}{}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	wantBuiltin := fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=sdk-cli;", upstreamidentity.BuiltInClientVersion("claudeCode"), anthropicBuildID)
	if got := body.System[0].Text; got != wantBuiltin {
		t.Fatalf("system[0].text after clearing=%q, want builtin %q", got, wantBuiltin)
	}
}
