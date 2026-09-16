package modelcheckruntime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckactive"
)

// w12a_runtime_test.go 覆盖 Durable Issue 失败（取消上下文）与
// run 内部 ctx 回退后的错误传播。

func TestW12aRunIssueFailsOnCanceledContext(t *testing.T) {
	durable, dataset, _, now := openRuntimeStores(t)
	defer durable.Close()
	defer dataset.Close()
	svc := newRuntimeService(durable, dataset, "http://127.0.0.1:9", now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.Run(ctx, runtimeRequest(now))
	if err == nil {
		t.Fatalf("取消上下文应使 Issue 失败")
	}
	if !strings.Contains(err.Error(), "issue model check input") && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("错误应来自输入签发或上下文: %v", err)
	}
}

func TestW12aRunWithActiveLeaseUsesHandleContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
	}))
	defer server.Close()
	durable, dataset, _, now := openRuntimeStores(t)
	defer durable.Close()
	defer dataset.Close()
	svc := newRuntimeService(durable, dataset, server.URL, now)
	registry := modelcheckactive.NewRegistry()
	handle, started, _ := registry.TryStart(context.Background(), "w12a-key", modelcheckactive.Summary{})
	if !started {
		t.Fatalf("应能获取活动句柄")
	}
	request := runtimeRequest(now)
	request.ActiveLease = &handle
	result, err := svc.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("带活动租约的运行不应失败: %v", err)
	}
	if result.RunStatus != "completed" {
		t.Fatalf("运行状态应为 completed: %q", result.RunStatus)
	}
	// 租约结束后条目应被释放。
	if _, stillActive := registry.Get("w12a-key"); stillActive {
		t.Fatalf("运行结束应释放活动条目")
	}
}

func TestW12aRunWithProgressOnCanceledContextEmitsFailure(t *testing.T) {
	durable, dataset, _, now := openRuntimeStores(t)
	defer durable.Close()
	defer dataset.Close()
	svc := newRuntimeService(durable, dataset, "http://127.0.0.1:9", now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := 0
	_, err := svc.RunWithProgress(ctx, runtimeRequest(now), func(ProgressEvent) { events++ })
	if err == nil {
		t.Fatalf("取消上下文应产生失败运行")
	}
}
