package gatewayoauthcodex

import (
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// resetClientVersionOverrideLayers 恢复手动/自动两层为内置默认，避免进程级
// 全局覆盖状态泄漏到同包其他测试（对齐 upstreamidentity 既有模式）。
func resetClientVersionOverrideLayers(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		upstreamidentity.SetClientVersionOverrides(nil)
		upstreamidentity.SetClientVersionAutoOverrides(nil)
	})
}

// buildCodexOutboundHeaders 走生产头构造函数（网关 Codex OAuth 出站链），
// 输入头不带 Codex 身份（普通客户端请求），由构造函数注入系统身份。
func buildCodexOutboundHeaders(t *testing.T) http.Header {
	t.Helper()
	inputHeaders := http.Header{}
	inputHeaders.Set("Accept", "text/event-stream")
	headers, err := buildOpenAIOAuthCodexHeaders(
		inputHeaders,
		OpenAIOAuthCodexAccount{ID: "acct-1", APIKey: "sk-test", Credentials: map[string]any{"account_id": "acc-123"}},
		codexHeaderInput{stream: true, model: "gpt-5.2"},
	)
	if err != nil {
		t.Fatalf("buildOpenAIOAuthCodexHeaders() err=%v", err)
	}
	return headers
}

// 出站实效（设计 §10）：自动覆盖生效后，经生产头构造函数构造的出站
// User-Agent 必须逐字等于覆盖值，Originator 家族身份保持不变。
func TestBuildOpenAIOAuthCodexHeadersUsesAutoOverrideUserAgent(t *testing.T) {
	resetClientVersionOverrideLayers(t)
	upstreamidentity.SetClientVersionAutoOverrides(map[string]string{"codex": "9.9.9"})

	headers := buildCodexOutboundHeaders(t)
	wantUA := "Codex Desktop/9.9.9 (Windows 10.0.22621; x86_64) unknown (codex_exec; 9.9.9)"
	if got := headers.Get("User-Agent"); got != wantUA {
		t.Fatalf("outbound User-Agent=%q, want %q", got, wantUA)
	}
	if got := headers.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("outbound Originator=%q, want Codex Desktop", got)
	}
}

// 手动覆盖优先级高于自动覆盖：两层同时设置时出站 UA 取手动值。
func TestBuildOpenAIOAuthCodexHeadersManualBeatsAutoOverride(t *testing.T) {
	resetClientVersionOverrideLayers(t)
	upstreamidentity.SetClientVersionOverrides(map[string]string{"codex": "8.8.8"})
	upstreamidentity.SetClientVersionAutoOverrides(map[string]string{"codex": "9.9.9"})

	headers := buildCodexOutboundHeaders(t)
	wantUA := "Codex Desktop/8.8.8 (Windows 10.0.22621; x86_64) unknown (codex_exec; 8.8.8)"
	if got := headers.Get("User-Agent"); got != wantUA {
		t.Fatalf("outbound User-Agent=%q, want manual override %q", got, wantUA)
	}
}

// 清空自动层后出站 UA 回退内置常量（0.159.3），不得残留覆盖值。
func TestBuildOpenAIOAuthCodexHeadersClearingAutoFallsBackToBuiltin(t *testing.T) {
	resetClientVersionOverrideLayers(t)
	upstreamidentity.SetClientVersionAutoOverrides(map[string]string{"codex": "9.9.9"})

	headers := buildCodexOutboundHeaders(t)
	if got := headers.Get("User-Agent"); got != "Codex Desktop/9.9.9 (Windows 10.0.22621; x86_64) unknown (codex_exec; 9.9.9)" {
		t.Fatalf("setup failed: outbound User-Agent=%q", got)
	}

	upstreamidentity.SetClientVersionAutoOverrides(nil)
	headers = buildCodexOutboundHeaders(t)
	if got := headers.Get("User-Agent"); got != upstreamidentity.CodexDesktopUserAgent {
		t.Fatalf("outbound User-Agent after clearing auto layer=%q, want builtin %q", got, upstreamidentity.CodexDesktopUserAgent)
	}
}
