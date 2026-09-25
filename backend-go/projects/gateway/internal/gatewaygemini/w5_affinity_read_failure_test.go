package gatewaygemini

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

// w5AffinityLogger 捕获 warn 日志，用于断言读失败可观测。
type w5AffinityLogger struct {
	buffer bytes.Buffer
}

func (l *w5AffinityLogger) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (l *w5AffinityLogger) Handle(_ context.Context, record slog.Record) error {
	l.buffer.WriteString(record.Message)
	record.Attrs(func(attr slog.Attr) bool {
		l.buffer.WriteString("|" + attr.Key + "=" + attr.Value.String())
		return true
	})
	l.buffer.WriteString("\n")
	return nil
}

func (l *w5AffinityLogger) WithAttrs(_ []slog.Attr) slog.Handler { return l }
func (l *w5AffinityLogger) WithGroup(_ string) slog.Handler      { return l }

// W5（BUG-0175 后续杂项）：亲和存储读失败必须可观测，同时保持"降级为未命中"
// 的既有行为（对齐 gatewaysession 亲和读失败 warn + 跳过）。
func TestGeminiInteractionAffinityReadFailureLogsWarnAndDegrades(t *testing.T) {
	store := newWaAffinityStore()
	store.getErr = errors.New("redis read boom")
	handler := &w5AffinityLogger{}
	affinity := NewInteractionAffinity(store).WithLogger(slog.New(handler))

	request := httptest.NewRequest("GET", "/v1beta/interactions/inter-123", nil)
	binding, found, err := affinity.Resolve(context.Background(), request, AffinityScope{
		SystemAccountID: "sys-1",
		APIKeyID:        "key-1",
		GroupID:         "group-1",
	})
	if err != nil {
		t.Fatalf("Resolve 读失败必须降级而非上抛: %v", err)
	}
	if found {
		t.Fatal("读失败必须降级为未命中")
	}
	if binding != (AffinityBinding{}) {
		t.Fatalf("降级未命中必须返回零值绑定: %+v", binding)
	}
	logged := handler.buffer.String()
	if !strings.Contains(logged, "gemini_interaction_affinity_read_failed") {
		t.Fatalf("读失败必须产出 warn 事件日志: %q", logged)
	}
	if !strings.Contains(logged, "redis read boom") {
		t.Fatalf("warn 日志必须保留原始错误: %q", logged)
	}
	if !strings.Contains(logged, "key=interaction:") {
		t.Fatalf("warn 日志必须带 key 摘要: %q", logged)
	}
}

// 无 logger 注入时保持静默（既有构造路径零行为变化）。
func TestGeminiInteractionAffinityReadFailureWithoutLoggerStaysSilent(t *testing.T) {
	store := newWaAffinityStore()
	store.getErr = errors.New("redis read boom")
	affinity := NewInteractionAffinity(store)

	request := httptest.NewRequest("GET", "/v1beta/interactions/inter-123", nil)
	_, found, err := affinity.Resolve(context.Background(), request, AffinityScope{
		SystemAccountID: "sys-1",
		APIKeyID:        "key-1",
		GroupID:         "group-1",
	})
	if err != nil || found {
		t.Fatalf("无 logger 时行为必须与历史一致（降级未命中）: found=%v err=%v", found, err)
	}
}

// UpdateAfterSuccess 的 refresh 路径经由 Resolve，读失败同样记 warn 且不报错。
func TestGeminiInteractionAffinityUpdateAfterSuccessLogsReadFailure(t *testing.T) {
	store := newWaAffinityStore()
	store.getErr = errors.New("redis read boom")
	handler := &w5AffinityLogger{}
	affinity := NewInteractionAffinity(store).WithLogger(slog.New(handler))

	request := httptest.NewRequest("GET", "/v1beta/interactions/inter-123", nil)
	result, err := affinity.UpdateAfterSuccess(context.Background(), UpdateAfterSuccessInput{
		Request: request,
		Account: UpstreamAccount{ID: "acc-1", ProviderCode: ProviderCode},
		Scope: AffinityScope{
			SystemAccountID: "sys-1",
			APIKeyID:        "key-1",
			GroupID:         "group-1",
		},
	})
	if err != nil {
		t.Fatalf("UpdateAfterSuccess 读失败必须不报错: %v", err)
	}
	if result.Action != AffinityActionNone {
		t.Fatalf("读失败 refresh 路径必须返回 none: %+v", result)
	}
	if !strings.Contains(handler.buffer.String(), "gemini_interaction_affinity_read_failed") {
		t.Fatalf("refresh 路径读失败必须产出 warn 日志: %q", handler.buffer.String())
	}
}
