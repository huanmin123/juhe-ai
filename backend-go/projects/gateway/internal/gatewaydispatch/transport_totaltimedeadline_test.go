package gatewaydispatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 总时间兜底截止（设计 6.3/6.6）的 transport 层回归：timer 安装不受
// DisableTimeouts 短路（压缩请求 lane 硬超时全豁免但总时间兜底必须生效），
// abort 生效前二次复查响应头（决策闭包含网络往返，TOCTOU 窗口内到达的
// 响应不得被截断）。

func TestRequestUpstreamTotalTimeDeadlineInstalledDespiteDisabledTimeouts(t *testing.T) {
	// 压缩请求形态：DisableTimeouts=true（lane 硬超时全豁免）+ 慢上游
	// （header 400ms 不达）。总时间档 50ms 到点必须仍触发决策回调并中止；
	// 若 timer 被 DisableTimeouts 块短路，请求会等满 400ms 正常返回。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deadlineMs := int64(50)
	handlerCalled := false
	started := time.Now()
	_, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{
		Method:              http.MethodGet,
		DisableTimeouts:     true,
		TotalTimeDeadlineMs: &deadlineMs,
		OnTotalTimeDeadline: func(input TotalTimeDeadlineDecisionInput) FirstByteDeadlineAction {
			handlerCalled = true
			return FirstByteDeadlineActionAbort
		},
	}, TransportDeps{})
	elapsed := time.Since(started)
	if !handlerCalled {
		t.Fatal("DisableTimeouts=true 时总时间决策回调未被调用：timer 被短路")
	}
	if !IsNormalRouteTotalTimeTimeoutError(err) {
		t.Fatalf("expected NormalRouteTotalTimeTimeoutError, got %v", err)
	}
	if elapsed >= 400*time.Millisecond {
		t.Fatalf("abort 未在总时间档生效（elapsed=%v，请求等满了慢上游）", elapsed)
	}
}

func TestRequestUpstreamTotalTimeDeadlineAbortRecheckResponseReceived(t *testing.T) {
	// 时间线：timer 30ms 到点时 header 未到（上游 100ms 才发 header）→
	// 入口守卫通过，进入决策回调；回调 sleep 150ms 模拟多次网络往返，
	// 期间（100ms）响应头到达。abort 生效前二次复查命中 → 软观察（慢样本
	// 已在回调记过），请求不得被截断。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(400 * time.Millisecond)
		_, _ = w.Write([]byte("-done"))
	}))
	defer server.Close()

	deadlineMs := int64(30)
	var handlerCalled atomic.Bool
	response, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{
		Method:              http.MethodGet,
		TotalTimeDeadlineMs: &deadlineMs,
		OnTotalTimeDeadline: func(input TotalTimeDeadlineDecisionInput) FirstByteDeadlineAction {
			handlerCalled.Store(true)
			time.Sleep(150 * time.Millisecond)
			return FirstByteDeadlineActionAbort
		},
	}, TransportDeps{})
	if err != nil {
		t.Fatalf("响应头已到达后 abort 必须降级为软观察，got %v", err)
	}
	if !handlerCalled.Load() {
		t.Fatal("总时间决策回调未被调用")
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if len(body) == 0 {
		t.Fatal("响应体为空：流被中止")
	}
}

// Continue 决策必须让请求继续（守护 transport.go 总时间臂的 continue 分支：
// 若坏成 abort，所有速度优先请求会在总时间到点被硬中断而下面的 abort 用例
// 无法报警——abort 用例本来就期待失败）。
func TestRequestUpstreamTotalTimeDeadlineContinueKeepsRequestAlive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"late":true}`))
	}))
	defer server.Close()

	deadlineMs := int64(30)
	response, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{
		Method:              http.MethodGet,
		TotalTimeDeadlineMs: &deadlineMs,
		OnTotalTimeDeadline: func(input TotalTimeDeadlineDecisionInput) FirstByteDeadlineAction {
			return FirstByteDeadlineActionContinue
		},
	}, TransportDeps{})
	if err != nil {
		t.Fatalf("expected deadline-continue success, got %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if len(body) == 0 {
		t.Fatal("响应体为空：请求被中止")
	}
}

// 决策回调 panic 必须转成本地终止错误（RunTotalTimeDeadlineHandler 的
// recover 是 timer goroutine 的最后防线），不能带崩进程。
func TestRequestUpstreamTotalTimeDeadlineHandlerPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	deadlineMs := int64(30)
	_, err := RequestUpstream(context.Background(), server.URL, UpstreamRequestOptions{
		Method:              http.MethodGet,
		TotalTimeDeadlineMs: &deadlineMs,
		OnTotalTimeDeadline: func(input TotalTimeDeadlineDecisionInput) FirstByteDeadlineAction {
			panic("总时间决策崩溃")
		},
	}, TransportDeps{})
	if err == nil {
		t.Fatal("expected the handler panic to fail the request")
	}
	if IsNormalRouteTotalTimeTimeoutError(err) {
		t.Fatalf("panic 不得伪装成正常总时间截止错误: %v", err)
	}
}
