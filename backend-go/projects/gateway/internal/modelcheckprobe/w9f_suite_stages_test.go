package modelcheckprobe

// w9f 覆盖收尾：RunSuite 各阶段的终局失败短路分支（脚本化目标服务器）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

// w9fScriptedServer 按调用序号返回脚本化响应：负数表示 HTTP 状态码，
// 0 表示成功输出 OK-MODEL-CHECK（后续调用沿用最后一条脚本）。
func w9fScriptedServer(t *testing.T, script []int) *httptest.Server {
	t.Helper()
	var call int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(atomic.AddInt64(&call, 1)) - 1
		if index >= len(script) {
			index = len(script) - 1
		}
		status := script[index]
		if status < 0 {
			w.WriteHeader(-status)
			_, _ = w.Write([]byte(`{"error":{"message":"stage failure"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK","usage":{"total_tokens":2}}`))
	}))
}

func w9fSuite(endpoint string, profile string) Suite {
	return Suite{
		Endpoint: endpoint, ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai_openai_v1",
		Client: &http.Client{}, Model: "gpt-5.6-sol", Profile: profile,
		Protocol: modelcheckprofile.ProtocolOpenAIResponses,
	}
}

// TestW9FSuiteCoreExecuteErrorsSurface：目标服务器不可达 → execute 返回
// 结果带 ErrorMessage，RunSuite 上层直接失败。
func TestW9FSuiteCoreExecuteErrorsSurface(t *testing.T) {
	server := w9fScriptedServer(t, []int{0})
	server.Close()
	// 连接失败 → ErrorMessage + 非 200 状态 → 终局短路，仅保留 basic+usage。
	items, err := RunSuite(context.Background(), w9fSuite(server.URL, "full"), time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	joined := ""
	for _, item := range items {
		joined += unscopedKind(item.Kind) + ","
	}
	if !strings.Contains(joined, "protocol_basic") || !strings.Contains(joined, "usage") {
		t.Fatalf("kinds = %s", joined)
	}
	if strings.Contains(joined, "structured") {
		t.Fatalf("terminal basic must stop families: %s", joined)
	}
}

// TestW9FSuiteStructuredStageTerminalStopsBeforeTool。
func TestW9FSuiteStructuredStageTerminalStopsBeforeTool(t *testing.T) {
	server := w9fScriptedServer(t, []int{0, 0, -502})
	defer server.Close()
	items, err := RunSuite(context.Background(), w9fSuite(server.URL, "full"), time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	joined := ""
	for _, item := range items {
		joined += unscopedKind(item.Kind) + ","
	}
	if !strings.Contains(joined, "structured") || strings.Contains(joined, "tool_evaluation") {
		t.Fatalf("kinds = %s", joined)
	}
}

// TestW9FSuiteToolStageTerminalStops。
func TestW9FSuiteToolStageTerminalStops(t *testing.T) {
	server := w9fScriptedServer(t, []int{0, 0, 0, -502})
	defer server.Close()
	items, err := RunSuite(context.Background(), w9fSuite(server.URL, "full"), time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	joined := ""
	for _, item := range items {
		joined += unscopedKind(item.Kind) + ","
	}
	if !strings.Contains(joined, "tool") || !strings.Contains(joined, "usage") {
		t.Fatalf("kinds = %s", joined)
	}
}

// TestW9FSuiteNoCoreSuccessAddsUsageOnly：200 但响应携带 error 对象 →
// Success=false 且非终局（状态码 200），四项核心探针全部失败后只补 usage。
func TestW9FSuiteNoCoreSuccessAddsUsageOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"message":"soft upstream error"}}`))
	}))
	defer server.Close()
	suite := w9fSuite(server.URL, "full")
	items, err := RunSuite(context.Background(), suite, time.Second)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	joined := ""
	for _, item := range items {
		joined += unscopedKind(item.Kind) + ","
	}
	if strings.Contains(joined, "behavior") || strings.Contains(joined, "long_context") {
		t.Fatalf("no core success must stop families: %s", joined)
	}
	if !strings.Contains(joined, "usage") {
		t.Fatalf("usage evidence missing: %s", joined)
	}
}
