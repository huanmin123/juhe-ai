package main

// 深挖复审修复批钉住测试（DD-G1 / DD-G2）：
// - DD-G1 停机顺序：HTTP 排空以最后一个 supervisor 组件注册，逆序 Close
//   时其 Close 最先执行——三个 HTTP 监听的有界排空必须先于 F4/F3 owner
//   组件的租约释放与 store 关闭（main.go 组件注册顺序：F3 首位、F4 随后、
//   registry 之后、HTTP drain 最后）。
// - DD-G2 运行期失败排空：failRuntime 的排空钩子在 exit 前以硬预算执行，
//   超时强制返回，nil 钩子（启动早期）快速跳过。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/supervisor"
)

// ddCloseRecorder 记录 Close 调用顺序（并发安全）。
type ddCloseRecorder struct {
	mu    sync.Mutex
	order []string
}

func (r *ddCloseRecorder) record(name string) func() error {
	return func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.order = append(r.order, name)
		return nil
	}
}

func (r *ddCloseRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
}

func ddWaitContextDone(runCtx context.Context) error {
	<-runCtx.Done()
	return runCtx.Err()
}

// TestDDG1_HTTPDrainClosesBeforeOwnerStores 按 main.go 的真实注册顺序组装
// 组件（F3 首位、F4 随后、registry 之后、HTTP drain 最后），经 supervisor.Run
// 触发真实的逆序 Close，断言：HTTP 排空最先执行，F3 auditStore 最后关闭。
func TestDDG1_HTTPDrainClosesBeforeOwnerStores(t *testing.T) {
	recorder := &ddCloseRecorder{}
	components := []supervisor.Component{
		{Name: "F3 audit-log-owner", Run: ddWaitContextDone, Close: recorder.record("F3-audit-store-close")},
		{Name: "F4 operation-log-owner", Run: ddWaitContextDone, Close: recorder.record("F4-operation-lease-and-store-close")},
		{Name: "Internal gateway registry", Run: ddWaitContextDone, Close: recorder.record("registry-close")},
	}
	components = appendHTTPDrainLast(components, recorder.record("main-http-drain"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := supervisor.Run(ctx, components, discardLogger); err != nil {
		t.Fatalf("supervisor.Run returned unexpected error: %v", err)
	}

	got := recorder.snapshot()
	want := []string{
		"main-http-drain",
		"registry-close",
		"F4-operation-lease-and-store-close",
		"F3-audit-store-close",
	}
	if len(got) != len(want) {
		t.Fatalf("close order length=%d want=%d order=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("close order[%d]=%q want=%q full order=%v", i, got[i], want[i], got)
		}
	}
}

// TestDDG1_HTTPDrainComponentWaitsForShutdownSignal 单独钉住 drain 组件的
// Run 语义：ctx 未取消时保持运行（不提前退出、不触发重试），取消后返回。
func TestDDG1_HTTPDrainComponentWaitsForShutdownSignal(t *testing.T) {
	components := appendHTTPDrainLast(nil, func() error { return nil })
	if len(components) != 1 || components[0].Name != "Main HTTP drain" {
		t.Fatalf("appendHTTPDrainLast produced unexpected component: %+v", components)
	}
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { returned <- components[0].Run(ctx) }()
	select {
	case err := <-returned:
		t.Fatalf("drain Run returned before ctx cancellation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("drain Run error=%v want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("drain Run did not return after ctx cancellation")
	}
}

// TestDDG1_ShutdownGatewayHTTPServersWaitsForInFlightRequests 钉住排空行为：
// 主端口有在途请求未完成时 shutdownGatewayHTTPServers 不返回；请求完成后
// 以 nil 错误返回，且监听不再接受新连接。
func TestDDG1_ShutdownGatewayHTTPServersWaitsForInFlightRequests(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	reached := make(chan struct{})
	release := make(chan struct{})
	mainServer := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		close(reached)
		<-release
		response.WriteHeader(http.StatusOK)
	}), ReadHeaderTimeout: 5 * time.Second}
	mainServeErr := make(chan error, 1)
	go func() { mainServeErr <- mainServer.Serve(listener) }()

	healthListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen health: %v", err)
	}
	healthServer := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
	}), ReadHeaderTimeout: 5 * time.Second}
	healthServeErr := make(chan error, 1)
	go func() { healthServeErr <- healthServer.Serve(healthListener) }()

	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		client := &http.Client{Timeout: 10 * time.Second}
		response, err := client.Get("http://" + listener.Addr().String() + "/")
		if err != nil {
			t.Errorf("in-flight request failed: %v", err)
			return
		}
		_ = response.Body.Close()
	}()
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was never reached")
	}

	servers := gatewayHTTPServers{
		mainServer:     mainServer,
		mainServeErr:   mainServeErr,
		healthServer:   healthServer,
		healthServeErr: healthServeErr,
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- shutdownGatewayHTTPServers(servers) }()
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned while in-flight request was still running: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not return after in-flight request completed")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("in-flight request did not complete")
	}
	if _, dialErr := net.DialTimeout("tcp", listener.Addr().String(), 200*time.Millisecond); dialErr == nil {
		t.Fatal("main listener still accepting connections after shutdown")
	}
}

// TestDDG1_ShutdownGatewayHTTPServersPropagatesServeError 钉住 Serve 异常
// 退出路径：health Serve 错误值经排空函数以可鉴别消息返回（main 的 drain
// Close 据此调用 failRuntime）。
func TestDDG1_ShutdownGatewayHTTPServersPropagatesServeError(t *testing.T) {
	healthServeErr := make(chan error, 1)
	healthServeErr <- errors.New("boom: http: ServeMux handler panicked")
	servers := gatewayHTTPServers{
		healthServer:   &http.Server{},
		healthServeErr: healthServeErr,
	}
	err := shutdownGatewayHTTPServers(servers)
	if err == nil {
		t.Fatal("shutdown returned nil, want wrapped serve error")
	}
	if want := "gateway health endpoint stopped: boom: http: ServeMux handler panicked"; err.Error() != want {
		t.Fatalf("error=%q want=%q", err.Error(), want)
	}
}

// TestDDG2_DrainRuntimeBeforeExit 钉住 DD-G2 排空语义：nil 钩子快速跳过、
// 正常钩子被执行、超预算钩子在预算内强制返回（对应 HTTP Shutdown 超时后
// 仍执行排空再退出的失败路径）。
func TestDDG2_DrainRuntimeBeforeExit(t *testing.T) {
	t.Run("nil drain returns immediately", func(t *testing.T) {
		done := make(chan struct{})
		go func() { drainRuntimeBeforeExit(nil, time.Second); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("nil drain did not return immediately")
		}
	})

	t.Run("drain hook executes", func(t *testing.T) {
		var invoked atomic.Bool
		drainCompleted := make(chan struct{})
		drainRuntimeBeforeExit(func() {
			invoked.Store(true)
			close(drainCompleted)
		}, 5*time.Second)
		select {
		case <-drainCompleted:
		case <-time.After(time.Second):
			t.Fatal("drain hook did not execute")
		}
		if !invoked.Load() {
			t.Fatal("drain hook flag not set")
		}
	})

	t.Run("budget forces bounded return", func(t *testing.T) {
		release := make(chan struct{})
		started := time.Now()
		drainRuntimeBeforeExit(func() { <-release }, 20*time.Millisecond)
		elapsed := time.Since(started)
		close(release) // 释放泄漏的排空 goroutine，避免污染测试进程
		if elapsed >= 500*time.Millisecond {
			t.Fatalf("drain returned after %v, want bounded by the 20ms budget", elapsed)
		}
	})
}

// TestDDG2_RuntimeFailureCleanupReleasesLease 锁定 DD-RC-A2：运行期失败
// 退出路径（failRuntime → runtimeFailureCleanup）在有界排空完成后、exit
// 前尽力释放 F4 owner lease——os.Exit 跳过 main 的全部 defer，lease 是
// 错误路径上唯一的跨进程语义（继任进程不必等 TTL 接管）；释放必须发生在
// 排空之后。nil 钩子（未启用 system-api/F4 或启动早期）惰性跳过。
func TestDDG2_RuntimeFailureCleanupReleasesLease(t *testing.T) {
	var mu sync.Mutex
	var order []string
	appendStep := func(name string) func() {
		return func() {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, name)
		}
	}

	runtimeFailureCleanup(appendStep("runtime-drain"), appendStep("f4-lease-release"))
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "runtime-drain" || got[1] != "f4-lease-release" {
		t.Fatalf("失败退出清理顺序 = %v, want [runtime-drain f4-lease-release]", got)
	}

	// nil 钩子惰性：启动早期（两钩子皆 nil）不 panic、不挂起。
	runtimeFailureCleanup(nil, nil)
}
