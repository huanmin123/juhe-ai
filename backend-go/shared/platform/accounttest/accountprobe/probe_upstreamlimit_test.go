package accountprobe

// 同上游 origin 并发桶测试（2026-11-22 用户裁决）：同一上游 base_url 的在途
// 探针 ≤ 桶上限，不同上游并行互不占额；连接测试通道（ConnectivityOnly）由
// connectivityhealth runner 层在探测预算外排队，Service 槽位层跳过。
import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamlimit"
)

func chatChallengeServer(t *testing.T, hold time.Duration, peak, inflight *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		current := inflight.Add(1)
		for {
			old := peak.Load()
			if current <= old || peak.CompareAndSwap(old, current) {
				break
			}
		}
		defer inflight.Add(-1)
		time.Sleep(hold)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + challengeAnswerFromBody(body) + `"},"finish_reason":"stop"}]}`))
	}))
}

// urlViewSource 按 AccountID 返回对应 base_url 的视图：跨源测试用不同账户
// 指向不同 upstream。
type urlViewSource struct{ views map[string]*View }

func (f *urlViewSource) LoadProbeView(_ context.Context, req accountquality.ProbeRequest) (*View, error) {
	return f.views[req.AccountID], nil
}

func concurrentProbeRequest(accountID string) accountquality.ProbeRequest {
	return accountquality.ProbeRequest{
		AccountID: accountID, GroupID: "group-1", SystemAccountID: "sys-1",
		TrafficSource: "runtime_recovery_probe", Full: false,
	}
}

// TestUpstreamLimiterBoundsSameOriginConcurrency 锁定同源上限：同 origin 的
// 并发探针峰值 ≤ 桶上限，超出的等桶排队。
func TestUpstreamLimiterBoundsSameOriginConcurrency(t *testing.T) {
	const limit = 2
	var peak, inflight atomic.Int64
	server := chatChallengeServer(t, 150*time.Millisecond, &peak, &inflight)
	defer server.Close()

	source := &urlViewSource{views: map[string]*View{}}
	for i := 0; i < limit*2; i++ {
		id := "acc-same-" + string(rune('a'+i))
		source.views[id] = probeView(server.URL)
	}
	service, err := NewService(Options{Source: source, Secret: "test-secret", UpstreamLimiter: upstreamlimit.NewLimiter(limit)})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for id := range source.views {
		wg.Add(1)
		go func(accountID string) {
			defer wg.Done()
			observation, err := service.ProbeAccountView(context.Background(), concurrentProbeRequest(accountID))
			if err != nil {
				t.Errorf("并发探测失败: %v", err)
				return
			}
			if observation == nil || !observation.Result.Success {
				t.Errorf("并发探测应成功，got %+v", observation)
			}
		}(id)
	}
	wg.Wait()

	if got := peak.Load(); got > limit {
		t.Fatalf("同 origin 并发峰值 %d 超过桶上限 %d", got, limit)
	}
	if got := peak.Load(); got < 2 {
		t.Fatalf("同 origin 并发峰值 %d 异常偏低，探测没有形成并发", got)
	}
}

// TestUpstreamLimiterAllowsCrossOriginParallelism 锁定跨源并行：不同 origin
// 各自独立计数，总峰值可以超过单桶上限。
func TestUpstreamLimiterAllowsCrossOriginParallelism(t *testing.T) {
	const limit = 1
	var peakA, inflightA atomic.Int64
	var peakB, inflightB atomic.Int64
	serverA := chatChallengeServer(t, 150*time.Millisecond, &peakA, &inflightA)
	defer serverA.Close()
	serverB := chatChallengeServer(t, 150*time.Millisecond, &peakB, &inflightB)
	defer serverB.Close()

	source := &urlViewSource{views: map[string]*View{
		"acc-a1": probeView(serverA.URL),
		"acc-a2": probeView(serverA.URL),
		"acc-b1": probeView(serverB.URL),
		"acc-b2": probeView(serverB.URL),
	}}
	service, err := NewService(Options{Source: source, Secret: "test-secret", UpstreamLimiter: upstreamlimit.NewLimiter(limit)})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for id := range source.views {
		wg.Add(1)
		go func(accountID string) {
			defer wg.Done()
			observation, err := service.ProbeAccountView(context.Background(), concurrentProbeRequest(accountID))
			if err != nil {
				t.Errorf("并发探测失败: %v", err)
				return
			}
			if observation == nil || !observation.Result.Success {
				t.Errorf("并发探测应成功，got %+v", observation)
			}
		}(id)
	}
	wg.Wait()

	total := peakA.Load() + peakB.Load()
	if total < limit+1 {
		t.Fatalf("跨源总并发峰值 %d 未超过单桶上限 %d，跨源并行未生效", total, limit)
	}
	if peakA.Load() > limit || peakB.Load() > limit {
		t.Fatalf("单源峰值超限：A=%d B=%d limit=%d", peakA.Load(), peakB.Load(), limit)
	}
}

// TestUpstreamLimiterSkipsConnectivityOnly 锁定跳过语义：连接测试通道的排队
// 由 connectivityhealth runner 层在探测预算外承载，Service 桶对其跳过——桶
// 塞满时 ConnectivityOnly 探测仍按短预算完成（否则等桶会吃掉单账户硬顶）。
func TestUpstreamLimiterSkipsConnectivityOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"空 prompt"}}`))
	}))
	defer server.Close()

	limiter := upstreamlimit.NewLimiter(1)
	sem := limiter.Bucket(upstreamlimit.Origin(server.URL))
	sem <- struct{}{} // 塞满同源桶
	defer func() { <-sem }()

	service, err := NewService(Options{Source: &fakeSource{}, Secret: "test-secret", UpstreamLimiter: limiter})
	if err != nil {
		t.Fatal(err)
	}
	view := connectivityView(server.URL, ModeImagesJSON, "gpt-test")
	view.ConnectivityOnly = true

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observation, _, err := service.ManualConnectivityDiagnostics(ctx, view, true)
	if err != nil {
		t.Fatalf("ConnectivityOnly 探测不得被桶阻塞: %v", err)
	}
	if observation == nil || !observation.Result.Success {
		t.Fatalf("400 属预期拒绝码应判通过，got %+v", observation)
	}
}
