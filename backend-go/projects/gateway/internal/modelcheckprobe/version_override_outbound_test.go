package modelcheckprobe

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// resetProbeClientVersionOverrides 恢复手动/自动两层为内置默认，避免进程级
// 全局覆盖状态泄漏到同包其他测试（对齐 upstreamidentity 既有模式）。
func resetProbeClientVersionOverrides(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		upstreamidentity.SetClientVersionOverrides(nil)
		upstreamidentity.SetClientVersionAutoOverrides(nil)
	})
}

// codexProbeHeaders 构造带 Authorization 的探针请求头基线。
func codexProbeHeaders() http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer probe-token")
	return headers
}

// codexProbeRequest 构造 J3b OpenAI OAuth Codex 探针请求基线。
func codexProbeRequest() Request {
	return Request{
		Protocol:     modelcheckprofile.ProtocolOpenAIResponses,
		EndpointMode: modelcheckprofile.EndpointModeResponsesJSON,
		Body:         json.RawMessage(`{"model":"gpt-5.2","input":"ping"}`),
	}
}

// 出站实效（设计 §10）：自动覆盖生效后，J3b 模型检测产出的出站 user-agent
// 必须为覆盖值；清空自动层后回退内置常量。
func TestNormalizeOpenAIOAuthCodexRequestUsesAutoOverrideUserAgent(t *testing.T) {
	resetProbeClientVersionOverrides(t)
	upstreamidentity.SetClientVersionAutoOverrides(map[string]string{"codex": "9.9.9"})

	_, headers, err := normalizeOpenAIOAuthCodexRequest(codexProbeRequest(), codexProbeHeaders())
	if err != nil {
		t.Fatalf("normalizeOpenAIOAuthCodexRequest() err=%v", err)
	}
	wantUA := "Codex Desktop/9.9.9 (Windows 10.0.22621; x86_64) unknown (codex_exec; 9.9.9)"
	if got := headers.Get("user-agent"); got != wantUA {
		t.Fatalf("probe outbound user-agent=%q, want %q", got, wantUA)
	}

	upstreamidentity.SetClientVersionAutoOverrides(nil)
	_, headers, err = normalizeOpenAIOAuthCodexRequest(codexProbeRequest(), codexProbeHeaders())
	if err != nil {
		t.Fatalf("normalizeOpenAIOAuthCodexRequest() after clearing err=%v", err)
	}
	if got := headers.Get("user-agent"); got != upstreamidentity.CodexDesktopUserAgent {
		t.Fatalf("probe outbound user-agent after clearing=%q, want builtin %q", got, upstreamidentity.CodexDesktopUserAgent)
	}
}
