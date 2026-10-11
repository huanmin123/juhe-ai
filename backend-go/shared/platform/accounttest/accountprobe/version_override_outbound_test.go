package accountprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// 出站实效（设计 §10）：claudeCode 族自动覆盖生效后，账号探测构造的
// Anthropic messages 载荷系统提示中 cc_version 挑战文本必须携带覆盖版本；
// 清空自动层后回退内置常量拼接值。W4 起载荷断言指向 buildTestRequest 的
// 最终出站请求（body 编码归信封 Build）。
func TestBuildAnthropicMessagesPayloadUsesAutoOverrideCCVersion(t *testing.T) {
	t.Cleanup(func() {
		upstreamidentity.SetClientVersionOverrides(nil)
		upstreamidentity.SetClientVersionAutoOverrides(nil)
	})
	readSystemHead := func(t *testing.T) string {
		t.Helper()
		view := probeView("https://anthropic.test")
		view.ProtocolCode = "anthropic"
		request, err := buildTestRequest(context.Background(), view, ModeMessagesJSON, "claude-sonnet-4-5", OutputChallenge{ExpectedOutput: "juhe", Prompt: "ping"}, "sk-test")
		if err != nil {
			t.Fatalf("buildTestRequest() err=%v", err)
		}
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
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
		return body.System[0].Text
	}

	upstreamidentity.SetClientVersionAutoOverrides(map[string]string{"claudeCode": "9.9.9"})
	wantBilling := fmt.Sprintf("x-anthropic-billing-header: cc_version=9.9.9.%s; cc_entrypoint=sdk-cli;", anthropicBuildID)
	if got := readSystemHead(t); got != wantBilling {
		t.Fatalf("system[0].text=%q, want %q", got, wantBilling)
	}

	upstreamidentity.SetClientVersionAutoOverrides(nil)
	wantBuiltin := fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=sdk-cli;", upstreamidentity.BuiltInClientVersion("claudeCode"), anthropicBuildID)
	if got := readSystemHead(t); got != wantBuiltin {
		t.Fatalf("system[0].text after clearing=%q, want builtin %q", got, wantBuiltin)
	}
}
