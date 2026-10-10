package main

// w1: chain_compose.go 装配收割——上游响应模型观察者、
// 客户端模型目录选择（Node client-model-catalog.service.ts 纯函数面）、
// body 拒绝记录缺席守卫与用量 spool 组装。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

func TestW1UpstreamResponseModelObserver(t *testing.T) {
	observer := newChainUpstreamResponseModelObserver()
	// nil response / nil body / nil publish：直接返回不包装。
	observer(nil, gatewaydispatch.UpstreamResponseModelObservationInfo{}, nil)
	emptyResponse := &gatewaydispatch.GatewayUpstreamResponse{}
	observer(emptyResponse, gatewaydispatch.UpstreamResponseModelObservationInfo{}, func(string) {})
	// 有效输入：body 被观察器替换（字节透传，发布观察到的模型）。
	wrapped := gatewaydispatch.NewGatewayUpstreamResponseForTransform(http.StatusOK,
		http.Header{}, io.NopCloser(strings.NewReader(`{"model":"gpt-5"}`)))
	var published string
	observer(wrapped, gatewaydispatch.UpstreamResponseModelObservationInfo{ProviderCode: "openai"}, func(model string) { published = model })
	if wrapped.Body == nil {
		t.Fatal("body 必须保留")
	}
	data, err := io.ReadAll(wrapped.Body)
	if err != nil || !strings.Contains(string(data), "gpt-5") {
		t.Fatalf("body 透传失败 = %q, %v", data, err)
	}
	_ = published
	// joinChinese：顿号拼接。
	if got := joinChinese([]string{"甲", "乙"}); got != "甲；乙" {
		t.Fatalf("join = %q", got)
	}
}

func TestW1ClientCatalogSelection(t *testing.T) {
	// 排序与去重：provider 码规范化去重后排序。
	codes := sortedUniqueProviderCodes([]string{"OpenAI", "openai ", "", "anthropic", "OPENAI"})
	if len(codes) != 2 || codes[0] != "anthropic" || codes[1] != "openai" {
		t.Fatalf("codes = %v", codes)
	}
	// 作用域秩（字典构建的每模型最优行选取依据；成员过滤已随并集契约退役，
	// 字典端口行为见 chain_gatewaykeymodels_test.go）。
	if got := clientCatalogScopeRank(gatewayruntimecache.ProviderModelCatalogItem{Scope: "personal"}); got != 3 {
		t.Fatalf("personal rank = %d", got)
	}
	// 条目投影：nil 指针字段回落零值。
	inputPrice := 1.5
	entry := clientCatalogEntryOf(gatewayruntimecache.ProviderModelCatalogItem{
		Model: "gpt-5", Scope: "global", InputUsdPer1M: &inputPrice,
		CodexSupportedReasoningLevels: json.RawMessage(`["low","high"]`),
		CodexDefaultReasoningLevel:    json.RawMessage(`"high"`),
	})
	if entry.Model != "gpt-5" || len(entry.CodexSupportedReasoningLevels) != 2 || entry.CodexDefaultReasoningLevel != "high" {
		t.Fatalf("entry = %+v", entry)
	}
	// nil 指针与非法 JSON 助手。
	if nilString(nil) != "" || nilInt(nil) != 0 {
		t.Fatal("nil 助手必须零值")
	}
	if got := rawMessageStringList(json.RawMessage(`"not-list"`)); got != nil {
		t.Fatalf("invalid list = %v", got)
	}
	if got := rawMessageString(json.RawMessage(`12`)); got != "" {
		t.Fatalf("invalid string = %q", got)
	}
}

func TestW1BodyRejectionRecorderGuards(t *testing.T) {
	// 缺席审计：仅解析请求面，不写审计/用量，也不 panic。
	recorder := &chainBodyRejectionRecorder{clock: gatewaypreauth.SystemClock{}}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?beta=1", nil)
	recorder.RecordGatewayBodyRejection(request, nil, gatewaybody.RejectionInput{
		StatusCode: http.StatusRequestEntityTooLarge, Reason: "payload_too_large",
		ErrorMessage: "请求体过大", LimitBytes: 1024, LimitScope: "text",
		RawBodyBytes:    2048,
		ResponsePayload: gatewaybody.ErrorPayload{},
	})
	// 审计已装配但设置关闭：同样丢弃。
	disabled := &chainBodyRejectionRecorder{
		audit:        nopAuditDispatcher{},
		auditEnabled: func() bool { return false },
		clock:        gatewaypreauth.SystemClock{},
	}
	disabled.RecordGatewayBodyRejection(request, nil, gatewaybody.RejectionInput{Reason: "lane_full"})
	// pathWithoutQueryOf：常规 path 原样返回（查询串不并入）。
	if got := pathWithoutQueryOf(httptest.NewRequest(http.MethodGet, "/some/path?x=2", nil)); got != "/some/path" {
		t.Fatalf("path = %q", got)
	}
	// runtime 快照：nil 请求 / 无 context 值 / 有值。
	if chainGatewayRuntimeOf(nil) != nil {
		t.Fatal("nil 请求必须 nil")
	}
	if chainGatewayRuntimeOf(request) != nil {
		t.Fatal("无快照必须 nil")
	}
	runtime := &gatewayruntimecache.GatewayRuntime{}
	withRuntime := request.WithContext(context.WithValue(request.Context(), chainGatewayRuntimeKey{}, runtime))
	if chainGatewayRuntimeOf(withRuntime) != runtime {
		t.Fatal("快照必须从 context 取回")
	}
	// 文本 raw body 上限 provider：cache 缺席保持 unconfigured。
	limit := chainTextRawBodyLimitOf(nil)
	if value, ok := limit(); ok || value != 0 {
		t.Fatalf("nil cache limit = %d, %v", value, ok)
	}
	// 审计 ID 形状：audit_ 前缀 + 时间戳 + uuid。
	auditID := chainNewAuditID(gatewaypreauth.SystemClock{})
	if !strings.HasPrefix(auditID, "audit_") || len(auditID) < len("audit_1728000000000_")+8 {
		t.Fatalf("audit id = %q", auditID)
	}
	// gatewaybodyLogger：四级日志走 slog。
	var buffer bytes.Buffer
	bodyLogger := gatewaybodyLogger{inner: slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	bodyLogger.Debug("调试", nil)
	bodyLogger.Info("信息", nil)
	bodyLogger.Warn("警告", nil)
	bodyLogger.Error("错误", nil)
	for _, want := range []string{"调试", "信息", "警告", "错误"} {
		if !strings.Contains(buffer.String(), want) {
			t.Fatalf("日志缺少 %q: %s", want, buffer.String())
		}
	}
}

func TestW1UsageSpoolAssembly(t *testing.T) {
	// 空目录：不装配。
	if got := newUsageSpool("", gatewaypreauth.SystemClock{}, slog.New(slog.NewTextHandler(io.Discard, nil)), usageSpoolCapacity{}); got != nil {
		t.Fatal("空目录必须 nil")
	}
	// 容量旋钮 <=0 回落默认后装配成功。
	spool := newUsageSpool(t.TempDir(), gatewaypreauth.SystemClock{}, slog.New(slog.NewTextHandler(io.Discard, nil)), usageSpoolCapacity{})
	if spool == nil {
		t.Fatal("临时目录必须装配 spool")
	}
	// 溢出写：nil spool 安全；非 nil 委托 PersistOverflow。
	if err := (spoolOverflow{}).PersistOverflow(context.Background(), gatewayusage.UsageRecordInput{}); err != nil {
		t.Fatalf("nil overflow = %v", err)
	}
	overflow := spoolOverflow{spool: spool}
	if err := overflow.PersistOverflow(context.Background(), gatewayusage.UsageRecordInput{TraceID: "t1"}); err != nil {
		t.Fatalf("persist = %v", err)
	}
}
