package modelcheckprobe

import (
	"context"
	"net/http"
	"testing"
	"time"

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

// 出站实效（设计 §10）：自动覆盖生效后，J3b 模型检测产出的出站 user-agent
// 必须为覆盖值；清空自动层后回退内置常量。身份头由信封 Build 统一写入
// （W2 F3 接线），覆盖值经 upstreamidentity 全局状态生效，因此断言对象是
// Execute 实际发出的请求（Mock transport 捕获）。
func TestExecuteOpenAIOAuthCodexOutboundUserAgentUsesAutoOverride(t *testing.T) {
	resetProbeClientVersionOverrides(t)
	upstreamidentity.SetClientVersionAutoOverrides(map[string]string{"codex": "9.9.9"})

	request, err := BuildBasicForEndpointMode(modelcheckprofile.ProtocolOpenAIResponses, "gpt-5.2", "hello", modelcheckprofile.EndpointModeResponsesJSON)
	if err != nil {
		t.Fatal(err)
	}
	capture := &w2f3Capture{}
	if _, err := Execute(context.Background(), request, Options{Adapter: AdapterOpenAIOAuthCodex, Endpoint: "https://custom.example", Headers: codexProbeHeaders(), Client: &http.Client{Transport: capture.transport()}, Timeout: time.Second}); err != nil {
		t.Fatalf("Execute() err=%v", err)
	}
	wantUA := "codex_exec/9.9.9 (Windows 10.0.22621; x86_64) unknown"
	if got := capture.request.Header.Get("User-Agent"); got != wantUA {
		t.Fatalf("probe outbound user-agent=%q, want %q", got, wantUA)
	}

	upstreamidentity.SetClientVersionAutoOverrides(nil)
	capture = &w2f3Capture{}
	if _, err := Execute(context.Background(), request, Options{Adapter: AdapterOpenAIOAuthCodex, Endpoint: "https://custom.example", Headers: codexProbeHeaders(), Client: &http.Client{Transport: capture.transport()}, Timeout: time.Second}); err != nil {
		t.Fatalf("Execute() after clearing err=%v", err)
	}
	if got := capture.request.Header.Get("User-Agent"); got != upstreamidentity.CodexExecUserAgent {
		t.Fatalf("probe outbound user-agent after clearing=%q, want builtin %q", got, upstreamidentity.CodexExecUserAgent)
	}
}
