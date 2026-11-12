package gatewaydispatch

// 2026-11-12 计费漏记修复：上游执行 signal 与客户端生命周期脱钩——客户端
// 断开后，已在途的上游请求与响应读取必须继续完成（最终按 usage 结算）。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestUpstreamDetachedSignalSurvivesClientCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":1}`))
	}))
	defer server.Close()

	clientCtx, cancelClient := context.WithCancel(context.Background())
	upstreamSignal := context.WithoutCancel(clientCtx)
	go func() {
		<-started
		// 客户端断开：原始请求 ctx 取消；上游 signal 不受影响。
		cancelClient()
		close(release)
	}()

	response, err := RequestUpstream(clientCtx, server.URL, UpstreamRequestOptions{
		Method: http.MethodGet,
		Signal: upstreamSignal,
	}, TransportDeps{})
	if err != nil {
		t.Fatalf("RequestUpstream err = %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read upstream body err = %v", err)
	}
	if string(body) != `{"ok":1}` {
		t.Fatalf("body = %q", string(body))
	}
}
