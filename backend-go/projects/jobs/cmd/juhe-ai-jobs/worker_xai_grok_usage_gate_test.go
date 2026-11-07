package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

// 本文件钉住 Grok 426 版本门观测日志的同源契约（2026-11-07 终审 S3）：
// grok_version_gate_426_observed 日志里的 grok_cli_version 必须是请求头
// 实际消费的请求内快照值（fetchXAIGrokJSON 的 NewSnapshot），不得在观测
// 时刻重读 EffectiveGrokCLIVersion()——热刷新窗口内两者可分叉，日志会把
// 从未发出的版本记成在线版本，误导跟版诊断。

// resetGateTestIdentityOverrides 清空两层客户端版本覆盖（进程级 atomic，
// 测试间共享），避免覆盖状态泄漏到同包其他测试。命名与既有 helper 区分，
// 不依赖其他测试文件的符号。
func resetGateTestIdentityOverrides(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		upstreamidentity.SetClientVersionOverrides(nil)
		upstreamidentity.SetClientVersionAutoOverrides(nil)
	})
}

// gateCaptureRecord 是一条捕获的结构化日志。
type gateCaptureRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// gateCaptureHandler 捕获 slog 记录供断言。
type gateCaptureHandler struct {
	mu      sync.Mutex
	records []gateCaptureRecord
}

func (h *gateCaptureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *gateCaptureHandler) Handle(_ context.Context, record slog.Record) error {
	entry := gateCaptureRecord{level: record.Level, msg: record.Message, attrs: map[string]string{}}
	record.Attrs(func(a slog.Attr) bool {
		entry.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, entry)
	return nil
}

func (h *gateCaptureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }

func (h *gateCaptureHandler) WithGroup(_ string) slog.Handler { return h }

func (h *gateCaptureHandler) findByEvent(event string) (gateCaptureRecord, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, record := range h.records {
		if record.attrs["event"] == event {
			return record, true
		}
	}
	return gateCaptureRecord{}, false
}

// 直接观测：快照版本作为参数传入后，日志记录的 grok_cli_version 是快照
// 值，而非观测时刻的 EffectiveGrokCLIVersion()（两者已人为分叉）。
func TestObserveGrokVersionGate426LogsRequestSnapshotVersion(t *testing.T) {
	resetGateTestIdentityOverrides(t)
	upstreamidentity.SetClientVersionOverrides(map[string]string{"grokCLI": "9.8.7"})
	runtime := &xaiGrokUsageRuntime{nowFunc: func() time.Time { return time.Now().UTC() }}
	capture := &gateCaptureHandler{}
	runtime.logger = slog.New(capture)

	// 模拟请求内快照（fetchXAIGrokJSON 构造请求头的同款取值）。
	snapshotVersion := upstreamidentity.NewSnapshot().GrokCLIVersion()
	if snapshotVersion != "9.8.7" {
		t.Fatalf("快照版本 = %q, want 9.8.7", snapshotVersion)
	}
	// 快照之后、观测之前热刷新生效：旧实现在观测时刻读 Effective 会记成
	// 1.2.3（从未发出的版本）。
	upstreamidentity.SetClientVersionOverrides(map[string]string{"grokCLI": "1.2.3"})
	if upstreamidentity.EffectiveGrokCLIVersion() != "1.2.3" {
		t.Fatalf("分叉前置条件失效：EffectiveGrokCLIVersion() = %q, want 1.2.3", upstreamidentity.EffectiveGrokCLIVersion())
	}

	runtime.observeGrokVersionGate426(http.StatusUpgradeRequired, []byte("upgrade required"), snapshotVersion)

	record, ok := capture.findByEvent("grok_version_gate_426_observed")
	if !ok {
		t.Fatalf("未捕获 grok_version_gate_426_observed 日志: %+v", capture.records)
	}
	if record.attrs["grok_cli_version"] != "9.8.7" {
		t.Fatalf("grok_cli_version = %q, want 请求头快照值 9.8.7（观测时刻 Effective=1.2.3）",
			record.attrs["grok_cli_version"])
	}
	if record.attrs["status_code"] != "426" {
		t.Fatalf("status_code = %q, want 426", record.attrs["status_code"])
	}
}

// fetch 级接线断言：426 响应路径下日志 grok_cli_version 与本次请求头
// x-grok-client-version 逐字一致（严格同源）。
func TestFetchXAIGrokJSONVersionGate426LogMatchesRequestHeader(t *testing.T) {
	resetGateTestIdentityOverrides(t)
	upstreamidentity.SetClientVersionOverrides(map[string]string{"grokCLI": "7.7.7"})
	var (
		mu             sync.Mutex
		headerVersions []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headerVersions = append(headerVersions, r.Header.Get("x-grok-client-version"))
		mu.Unlock()
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"error":"upgrade"}`))
	}))
	t.Cleanup(server.Close)

	capture := &gateCaptureHandler{}
	runtime := &xaiGrokUsageRuntime{nowFunc: func() time.Time { return time.Now().UTC() }, logger: slog.New(capture)}
	_, err := runtime.fetchXAIGrokJSON(context.Background(), server.Client(), "token", server.URL+"/billing?format=credits")
	if err == nil || !strings.Contains(err.Error(), "426") {
		t.Fatalf("fetchXAIGrokJSON() err = %v, want 426 错误", err)
	}
	if len(headerVersions) != 1 {
		t.Fatalf("captured %d requests, want 1", len(headerVersions))
	}
	record, ok := capture.findByEvent("grok_version_gate_426_observed")
	if !ok {
		t.Fatalf("未捕获 grok_version_gate_426_observed 日志: %+v", capture.records)
	}
	if record.attrs["grok_cli_version"] != headerVersions[0] {
		t.Fatalf("日志 grok_cli_version = %q, 请求头 x-grok-client-version = %q，必须严格同源",
			record.attrs["grok_cli_version"], headerVersions[0])
	}
}
