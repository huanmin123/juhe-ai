package main

// 深挖复审修复批钉住测试（DD-G1 / DD-G2 / DD3-F1 / DD3-F3）：
// - DD-G1 停机顺序：HTTP 排空以最后一个 supervisor 组件注册，逆序 Close
//   时其 Close 最先执行——三个 HTTP 监听的有界排空必须先于 F4/F3 owner
//   组件的租约释放与 store 关闭（main.go 组件注册顺序：F3 首位、F4 随后、
//   registry 之后、HTTP drain 最后）。
// - DD-G2 运行期失败排空：failRuntime 的排空钩子在 exit 前以硬预算执行，
//   超时强制返回，nil 钩子（启动早期）快速跳过。
// - DD3-F1（R3 回归修复）：F3 owner 组件 Close 必须先释放共享 F3 lease 再
//   关 audit store——store.Close 释放共享 "gateway-store" 池引用，refs 归零
//   即物理关池，之后租约释放必失败，继任进程只能等 TTL（f3OwnerClose）。
// - DD3-F3（R3 回归修复）：启动期失败出口 fail 经 os.Exit 跳过 main 的全部
//   defer，exit 前必须尽力释放已获取的 F3/F4 租约（startupFailureCleanup +
//   startupLeaseRelease 钩子），否则继任进程在 TTL 内重启即 fail-fast。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
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

// recordStep 记录无返回值的清理步骤（func() 形态的租约释放钩子用）。
func (r *ddCloseRecorder) recordStep(name string) func() {
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.order = append(r.order, name)
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

// ddCloseOnlyStore 是 f3OwnerClose 测试所需的最小 store 面：只实现 Close，
// 可选记录调用并可注入关闭错误。
type ddCloseOnlyStore struct {
	closeErr error
	onClose  func()
}

func (s *ddCloseOnlyStore) Close() error {
	if s.onClose != nil {
		s.onClose()
	}
	return s.closeErr
}

// ddAssertCloseOrder 断言 Close/释放步骤的实际顺序与期望完全一致。
func ddAssertCloseOrder(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("close order length=%d want=%d order=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("close order[%d]=%q want=%q full order=%v", i, got[i], want[i], got)
		}
	}
}

// TestDDG1_F3ComponentCloseReleasesLeaseBeforeStore 钉住 DD3-F1 停机序：F3
// owner 组件经 f3OwnerClose 接线（main.go 同款装配），经 supervisor.Run 触发
// 真实的逆序 Close，lease 释放必须先于 audit store 关闭；F3 注册在 HTTP
// drain 之前，store 关闭仍是 owner 组件中最先执行的一个（先于 F4）。顺序颠
// 倒时 store.Close 物理关掉共享 "gateway-store" 池，租约释放必失败。
func TestDDG1_F3ComponentCloseReleasesLeaseBeforeStore(t *testing.T) {
	recorder := &ddCloseRecorder{}
	components := []supervisor.Component{
		{
			Name: "F3 audit-log-owner",
			Run:  ddWaitContextDone,
			Close: f3OwnerClose(
				recorder.recordStep("f3-audit-lease-release"),
				&ddCloseOnlyStore{onClose: recorder.recordStep("f3-audit-store-close")},
			),
		},
	}
	components = appendHTTPDrainLast(components, recorder.record("main-http-drain"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := supervisor.Run(ctx, components, discardLogger); err != nil {
		t.Fatalf("supervisor.Run returned unexpected error: %v", err)
	}

	ddAssertCloseOrder(t, recorder.snapshot(), []string{
		"main-http-drain",
		"f3-audit-lease-release",
		"f3-audit-store-close",
	})
}

// TestDD3_F1_F3OwnerCloseContract 直接钉住 f3OwnerClose 的行为契约：lease
// 释放严格先于 store 关闭、store 关闭错误原样透传、nil 释放钩子惰性跳过
// （store 仍关闭）。
func TestDD3_F1_F3OwnerCloseContract(t *testing.T) {
	t.Run("release before store close and store error propagated", func(t *testing.T) {
		recorder := &ddCloseRecorder{}
		wantErr := errors.New("close store failed")
		store := &ddCloseOnlyStore{closeErr: wantErr, onClose: recorder.recordStep("f3-audit-store-close")}
		if err := f3OwnerClose(recorder.recordStep("f3-audit-lease-release"), store)(); !errors.Is(err, wantErr) {
			t.Fatalf("f3OwnerClose 返回 %v, want %v", err, wantErr)
		}
		ddAssertCloseOrder(t, recorder.snapshot(), []string{"f3-audit-lease-release", "f3-audit-store-close"})
	})

	t.Run("nil lease hook is a no-op", func(t *testing.T) {
		storeClosed := false
		store := &ddCloseOnlyStore{onClose: func() { storeClosed = true }}
		if err := f3OwnerClose(nil, store)(); err != nil {
			t.Fatalf("nil 释放钩子下 f3OwnerClose 返回错误: %v", err)
		}
		if !storeClosed {
			t.Fatal("nil 释放钩子下 store 未关闭")
		}
	})
}

// TestDD3_F3_StartupFailureCleanupReleasesLease 钉住 DD3-F3 启动期失败出口：
// fail 在 exit 前调用启动期释放钩子（fake lease 断言释放被调），钩子为 nil
// （租约未获取的启动早期失败）时 no-op、不 panic。
func TestDD3_F3_StartupFailureCleanupReleasesLease(t *testing.T) {
	var released atomic.Bool
	startupFailureCleanup(func() { released.Store(true) })
	if !released.Load() {
		t.Fatal("启动期失败清理未释放已获取的租约")
	}
	// nil 钩子（租约未获取）：no-op。
	startupFailureCleanup(nil)
}

// TestDD3_LeaseReleaseWiringGuard 把 DD3-F1/DD3-F3 的组合根接线固化为 main.go
// 源码文本断言（与 composition_wiring_guard_test.go 同款闸门，本测试只盯
// main.go）：防止 F3 组件 Close 退回 Close: auditStore.Close、或启动期释放
// 钩子被拆线。接线的具体行为语义由上方行为级测试承担，二者互补。
func TestDD3_LeaseReleaseWiringGuard(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	pool := string(source)
	entries := []struct {
		identifier string
		purpose    string
	}{
		{"Close: f3OwnerClose(auditLease.Close, auditStore),", "F3 组件 Close 必须先释放共享 lease 再关 store（DD3-F1）"},
		{"startupLeaseRelease = auditLease.Close", "F3 租约获取成功后必须注册启动期释放钩子（DD3-F3）"},
		{"startupLeaseRelease = func() {", "F4 租约获取成功后必须并入启动期释放钩子（DD3-F3）"},
		{"startupFailureCleanup(startupLeaseRelease)", "fail 必须在 exit 前调用启动期释放钩子（DD3-F3）"},
	}
	for _, entry := range entries {
		if !strings.Contains(pool, entry.identifier) {
			t.Fatalf("main.go 缺失接线 %q：%s", entry.identifier, entry.purpose)
		}
	}
}
