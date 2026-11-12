package main

// 深挖复审修复批钉住测试（DD-G9 / DD-A3）：
//   - DD-G9：realtime 每 API Key 连接计数器在活跃归零时删除键（原 release 只
//     减不删，进程生命期内出现过的 apiKeyID 无界累积），且并发 acquire/release
//     交错后既无孤儿计数也无键残留，限额语义保持。
//   - DD-A3②：realtime ?token= 的 Verify 失败（无效/过期 token）与缺 token 计
//     入 preauth 认证前熔断（与 Bearer 面同款 reason/阈值），达阈值被 429 拒绝。
import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// TestDDG9ConnectionLimiterReleasesKeysAtZero：归零删键、幂等 release 不回弹、
// 删键后重建正常。
func TestDDG9ConnectionLimiterReleasesKeysAtZero(t *testing.T) {
	limiter := &chainRealtimeConnectionLimiter{}
	if !limiter.acquire("key_dd_g9", 2) {
		t.Fatal("首个 acquire 必须成功")
	}
	if !limiter.acquire("key_dd_g9", 2) {
		t.Fatal("第二个 acquire 必须成功")
	}
	if limiter.acquire("key_dd_g9", 2) {
		t.Fatal("超限 acquire 必须拒绝")
	}
	limiter.release("key_dd_g9", "")
	if limiter.count("key_dd_g9") != 1 {
		t.Fatalf("count = %d, want 1", limiter.count("key_dd_g9"))
	}
	limiter.release("key_dd_g9", "")
	if limiter.count("key_dd_g9") != 0 {
		t.Fatalf("count = %d, want 0", limiter.count("key_dd_g9"))
	}
	limiter.mutex.Lock()
	mapSize := len(limiter.counts)
	limiter.mutex.Unlock()
	if mapSize != 0 {
		t.Fatalf("活跃归零后键未删除: map 大小 = %d, want 0（DD-G9 无界累积）", mapSize)
	}
	// 幂等 release：对已删键继续 release 不回弹、不复活键。
	limiter.release("key_dd_g9", "")
	limiter.mutex.Lock()
	mapSize = len(limiter.counts)
	limiter.mutex.Unlock()
	if mapSize != 0 || limiter.count("key_dd_g9") != 0 {
		t.Fatalf("幂等 release 后 mapSize=%d count=%d, want 0/0", mapSize, limiter.count("key_dd_g9"))
	}
	// 删键后重新 acquire 正常重建。
	if !limiter.acquire("key_dd_g9", 2) || limiter.count("key_dd_g9") != 1 {
		t.Fatalf("删键后重建失败: acquire ok / count = %d", limiter.count("key_dd_g9"))
	}
}

// TestDDG9ConnectionLimiterConcurrentAcquireRelease：32 goroutine 在同一 key
// 上并发 acquire/release 交错（含归零删除与重建），结束后活跃计数为 0、map
// 为空（无孤儿计数/键残留），限额语义仍正确。
func TestDDG9ConnectionLimiterConcurrentAcquireRelease(t *testing.T) {
	limiter := &chainRealtimeConnectionLimiter{}
	const workers = 32
	const limit = int64(4)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if limiter.acquire("hot_dd_g9", limit) {
					limiter.release("hot_dd_g9", "")
				}
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	if active := limiter.count("hot_dd_g9"); active != 0 {
		t.Fatalf("并发交错后活跃计数 = %d, want 0", active)
	}
	limiter.mutex.Lock()
	mapSize := len(limiter.counts)
	limiter.mutex.Unlock()
	if mapSize != 0 {
		t.Fatalf("并发交错后 map 大小 = %d, want 0（孤儿键）", mapSize)
	}
	for i := int64(0); i < limit; i++ {
		if !limiter.acquire("hot_dd_g9", limit) {
			t.Fatalf("交错后第 %d 个 acquire 必须成功", i+1)
		}
	}
	if limiter.acquire("hot_dd_g9", limit) {
		t.Fatal("交错后超限 acquire 必须拒绝")
	}
}

// TestDDA3RealtimeInvalidTokenCircuitOpensAtThreshold：同一个无效 ?token= 连拨
// 7 次（invalid-token specific 臂阈值 8，gatewayclientip.preAuthInvalidToken-
// Threshold 私有常量）→ 401；第 8 次 → preauth 熔断 429（Retry-After）。
// 第 8 次 429 本身证明前 7 次都进了失败计数（缺计数则阈值不会触发）。
func TestDDA3RealtimeInvalidTokenCircuitOpensAtThreshold(t *testing.T) {
	fixture := newChainFixture(t)
	mock := platformmock.New()
	defer mock.Close()
	seedRealtimeAccount(t, fixture, "acc_dd_a3", mock.URL, "sk-dd-a3-realtime", 0)
	chain, _, _, tokens := realtimeBridgeCompose(t, fixture, true)
	if tokens == nil {
		t.Fatal("redis 臂必须装配 realtime token 服务")
	}
	server := httptest.NewServer(chain)
	defer server.Close()

	const invalidToken = "dd-invalid-token-not-issued-anywhere"
	const invalidTokenThreshold = 8 // gatewayclientip.preAuthInvalidTokenThreshold（包私有，语义对齐）
	target := "/v1/realtime?model=gpt-realtime&token=" + invalidToken
	for i := 1; i < invalidTokenThreshold; i++ {
		_, response, err := realtimeDial(t, server.URL, target, "")
		if err != nil && response == nil {
			t.Fatalf("第 %d 次 dial 无响应: %v", i, err)
		}
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("第 %d 次无效 token 状态 = %d, want 401", i, response.StatusCode)
		}
	}
	_, response, err := realtimeDial(t, server.URL, target, "")
	if err != nil && response == nil {
		t.Fatalf("阈值次 dial 无响应: %v", err)
	}
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("阈值次无效 token 状态 = %d, want 429（preauth 熔断）", response.StatusCode)
	}
	if response.Header.Get("Retry-After") == "" {
		t.Fatal("熔断 429 必须携带 Retry-After")
	}
}
