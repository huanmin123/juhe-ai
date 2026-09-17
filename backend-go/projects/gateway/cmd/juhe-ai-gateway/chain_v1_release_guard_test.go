package main

// 守护 F13（E2E-FINDING #13 第二层）修复：v1DispatchLoop.run 在
// handleUpstreamResponse 返回后必须释放账户并发槽（chain_v1.go 的嵌套闭包
// + defer，onceFunc 幂等）。修复前 ReleaseConcurrency 全仓库无调用点，成功
// 请求的账户并发槽永不释放，高并发分组的排队者收不到唤醒。
//
// 构造路径：真实链（newChainFixture + w1vSeedMultiGroupKey 指向 httptest
// 活上游 + w1vComposeChain）承载一次成功请求，engine.Concurrency 外包
// releaseGuardConcurrencyStore 统计派发槽 Release 闭包的实际调用次数。
// w1vServeV1 返回即 handler 返回即 run() 已退出：下游已收到完整 200 证明
// handleUpstreamResponse 已完成，此刻 releases==1 即「响应处理返回后槽恰好
// 释放一次」；成功派发路径 keepConcurrencySlot=true（engine 内
// releaseTransientState 对槽为空操作），ReleaseConcurrency 是槽 Release 的
// 唯一生产调用点，计数不会被别处污染。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// releaseGuardConcurrencyStore 在真实账户并发存储外统计派发槽 Release
// 闭包调用次数，并记录成功获取过槽的账户 ID。
type releaseGuardConcurrencyStore struct {
	inner    gatewaydispatch.AccountConcurrencyStore
	mu       sync.Mutex
	releases int
	acquired map[string]bool
}

func (s *releaseGuardConcurrencyStore) LoadCurrentAsync(ctx context.Context, accountIDs []string) (map[string]int, error) {
	return s.inner.LoadCurrentAsync(ctx, accountIDs)
}

func (s *releaseGuardConcurrencyStore) LoadCurrentByLaneAsync(ctx context.Context, accountIDs []string, lane string) (map[string]int, error) {
	return s.inner.LoadCurrentByLaneAsync(ctx, accountIDs, lane)
}

func (s *releaseGuardConcurrencyStore) TryAcquireAsync(ctx context.Context, accountID string, concurrencyLimit int, options gatewaydispatch.AccountConcurrencyAcquireOptions) (gatewaydispatch.ConcurrencySlot, error) {
	slot, err := s.inner.TryAcquireAsync(ctx, accountID, concurrencyLimit, options)
	if err != nil || !slot.Acquired {
		return slot, err
	}
	s.mu.Lock()
	if s.acquired == nil {
		s.acquired = map[string]bool{}
	}
	s.acquired[accountID] = true
	s.mu.Unlock()
	release := slot.Release
	slot.Release = func() {
		s.mu.Lock()
		s.releases++
		s.mu.Unlock()
		release()
	}
	return slot, nil
}

func (s *releaseGuardConcurrencyStore) releaseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releases
}

func (s *releaseGuardConcurrencyStore) acquiredAccountIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.acquired))
	for id := range s.acquired {
		ids = append(ids, id)
	}
	return ids
}

// TestV1DispatchLoopReleasesAccountSlotAfterResponseHandling 断言 run 循环内
// handleUpstreamResponse 返回后账户并发槽恰好被释放一次。
func TestV1DispatchLoopReleasesAccountSlotAfterResponseHandling(t *testing.T) {
	goodUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-release-guard","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"释放守护"},"finish_reason":"stop"}]}`))
	}))
	defer goodUpstream.Close()

	fixture := newChainFixture(t)
	secret := w1vSeedMultiGroupKey(t, fixture, []string{"w1v_group_release_guard"},
		map[int]bool{0: true}, map[int]string{0: goodUpstream.URL})
	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()

	guard := &releaseGuardConcurrencyStore{inner: chain.engine.Concurrency}
	chain.engine.Concurrency = guard

	status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s（成功派发是本守护的前提）", status, body)
	}
	if !strings.Contains(body, "释放守护") {
		t.Fatalf("下游必须收到完整上游响应: %s", body)
	}

	// handler 返回即 run() 已退出：handleUpstreamResponse 已完成（200 已提交），
	// 账户并发槽必须恰好释放一次——修复缺失时该值为 0（槽泄漏给排队者）。
	if releases := guard.releaseCount(); releases != 1 {
		t.Fatalf("账户并发槽释放次数=%d，要求恰好 1（handleUpstreamResponse 返回后交还排队者）", releases)
	}

	// 释放必须真实回滚存储：账户当前并发归零，排队者可见容量。
	acquiredIDs := guard.acquiredAccountIDs()
	if len(acquiredIDs) != 1 {
		t.Fatalf("成功请求应恰好获取一个账户槽: %v", acquiredIDs)
	}
	current, err := guard.LoadCurrentAsync(context.Background(), acquiredIDs)
	if err != nil {
		t.Fatalf("load current: %v", err)
	}
	if current[acquiredIDs[0]] != 0 {
		t.Fatalf("释放后账户并发必须归零: %v", current)
	}
}
