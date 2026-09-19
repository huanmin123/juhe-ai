package gatewayresponse

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// W3 回归：Stop() 必须同步等待心跳 goroutine 完全退出。修复前 Stop 只
// cancel context，已在途的一笔 Res.Write/Flush/MarkTransportCommitted 仍会
// 与主流程恢复写并发（http.ResponseWriter 非并发安全，-race 必报）。

// w3HeartbeatRecordingRes 是带锁记录心跳写的 fake Res；entered/release
// 非 nil 时每笔 Write 先报告进入并阻塞到放行，用于制造在途写窗口。
type w3HeartbeatRecordingRes struct {
	mu          sync.Mutex
	writes      []string
	header      http.Header
	headersSent bool

	entered chan struct{}
	release chan struct{}
}

func newW3HeartbeatRecordingRes() *w3HeartbeatRecordingRes {
	return &w3HeartbeatRecordingRes{header: http.Header{}}
}

func (w *w3HeartbeatRecordingRes) Header() http.Header { return w.header }

func (w *w3HeartbeatRecordingRes) WriteHeader(status int) {
	w.mu.Lock()
	w.headersSent = true
	w.mu.Unlock()
}

func (w *w3HeartbeatRecordingRes) Write(p []byte) (int, error) {
	entered, release := w.entered, w.release
	if entered != nil {
		entered <- struct{}{}
		<-release
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

func (w *w3HeartbeatRecordingRes) HeadersSent() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.headersSent
}

func (w *w3HeartbeatRecordingRes) StatusCode() int { return http.StatusOK }

func (w *w3HeartbeatRecordingRes) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writes)
}

func (w *w3HeartbeatRecordingRes) bodyText() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.writes, "")
}

func w3WaitWriteCount(t *testing.T, res *w3HeartbeatRecordingRes, min int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for res.writeCount() < min && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if res.writeCount() < min {
		t.Fatalf("等待至少 %d 笔心跳写超时，当前 %d", min, res.writeCount())
	}
}

// TestW3HeartbeatStopPreventsWritesAfterReturn 反复 Start/Stop 交错：Stop
// 返回后心跳 goroutine 已退出，残留节拍无人消费，不再出现新的心跳写。
func TestW3HeartbeatStopPreventsWritesAfterReturn(t *testing.T) {
	res := newW3HeartbeatRecordingRes()
	// ticks 每轮替换：主线程在上一轮 Stop 返回后、下一轮 Start 前写变量，
	// 与 goroutine 读取之间经 Stop 同步化存在 happens-before，无竞争。
	var ticks chan time.Time
	heartbeat := CreateGatewaySseWaitHeartbeat(HeartbeatDeps{
		Res:                res,
		DownstreamProtocol: "messages_sse",
		DownstreamCommit:   &DownstreamCommitState{},
		// IntervalMs 会被钳到最小 1000，节拍由 ticks 注入控制。
		IntervalMs: 1,
		After: func(time.Duration) <-chan time.Time {
			return ticks
		},
	})
	if heartbeat == nil {
		t.Fatal("SSE 协议应得心跳")
	}
	for round := 0; round < 6; round++ {
		ticks = make(chan time.Time, 4)
		heartbeat.Start()
		w3WaitWriteCount(t, res, 1, 2*time.Second)
		ticks <- time.Now()
		ticks <- time.Now()
		time.Sleep(2 * time.Millisecond)
		heartbeat.Stop()
		heartbeat.Stop() // 多次串行调用幂等。
		frozen := res.writeCount()
		// Stop 已返回：goroutine 退出，残留节拍必须无人消费。
		ticks <- time.Now()
		ticks <- time.Now()
		time.Sleep(10 * time.Millisecond)
		if got := res.writeCount(); got != frozen {
			t.Fatalf("第 %d 轮 Stop 返回后仍出现新心跳写：%d -> %d", round, frozen, got)
		}
	}
	heartbeat.mu.Lock()
	running := heartbeat.run
	done := heartbeat.lastDone
	heartbeat.mu.Unlock()
	if running != nil {
		t.Fatal("Stop 后不应存在运行轮次")
	}
	select {
	case <-done:
	default:
		t.Fatal("Stop 返回后最近一轮 goroutine 应已退出（done 未关闭）")
	}
}

// TestW3HeartbeatStopDrainsInFlightWrite 验证 Stop 的排空语义：存在一笔
// 在途写时 Stop 阻塞等待其完成，放行后 Stop 才返回，且只写出这一笔。
func TestW3HeartbeatStopDrainsInFlightWrite(t *testing.T) {
	res := newW3HeartbeatRecordingRes()
	res.entered = make(chan struct{})
	res.release = make(chan struct{})
	heartbeat := CreateGatewaySseWaitHeartbeat(HeartbeatDeps{
		Res:                res,
		DownstreamProtocol: "messages_sse",
		DownstreamCommit:   &DownstreamCommitState{},
		IntervalMs:         15_000,
	})
	if heartbeat == nil {
		t.Fatal("SSE 协议应得心跳")
	}
	heartbeat.Start()
	select {
	case <-res.entered: // 首个心跳进入 Write 且尚未返回。
	case <-time.After(2 * time.Second):
		t.Fatal("首个心跳应进入在途写")
	}
	stopReturned := make(chan struct{})
	go func() {
		heartbeat.Stop()
		close(stopReturned)
	}()
	// 在途写未放行前 Stop 不得返回：done 未关闭即 goroutine 未退出。
	select {
	case <-stopReturned:
		t.Fatal("在途写完成前 Stop 不应返回")
	case <-time.After(20 * time.Millisecond):
	}
	res.release <- struct{}{}
	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("在途写完成后 Stop 应及时返回")
	}
	if got := res.writeCount(); got != 1 {
		t.Fatalf("应恰好完成一笔在途写，got %d", got)
	}
}

// TestW3HeartbeatConcurrentStop 并发调用 Stop：全部返回后最近一轮
// goroutine 已退出且不再有新写（-race 下验证无数据竞争）。
func TestW3HeartbeatConcurrentStop(t *testing.T) {
	res := newW3HeartbeatRecordingRes()
	heartbeat := CreateGatewaySseWaitHeartbeat(HeartbeatDeps{
		Res:                res,
		DownstreamProtocol: "responses_sse",
		DownstreamCommit:   &DownstreamCommitState{},
		IntervalMs:         15_000,
	})
	if heartbeat == nil {
		t.Fatal("SSE 协议应得心跳")
	}
	heartbeat.Start()
	w3WaitWriteCount(t, res, 1, 2*time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			heartbeat.Stop()
		}()
	}
	wg.Wait()
	heartbeat.mu.Lock()
	done := heartbeat.lastDone
	heartbeat.mu.Unlock()
	select {
	case <-done:
	default:
		t.Fatal("并发 Stop 返回后最近一轮 goroutine 应已退出（done 未关闭）")
	}
	frozen := res.writeCount()
	time.Sleep(10 * time.Millisecond)
	if got := res.writeCount(); got != frozen {
		t.Fatalf("并发 Stop 后仍出现新心跳写：%d -> %d", frozen, got)
	}
}
