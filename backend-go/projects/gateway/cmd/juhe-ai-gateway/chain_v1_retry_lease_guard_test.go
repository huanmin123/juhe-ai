package main

// BUG-0247 项 1 回归：v1DispatchLoop 在响应终态消费引擎随结果带出的重试租约
// 释放回调（chain_v1_loop.go run() 响应闭包的 defer，Node routes.ts:2498-2501
// finally 的 releaseAccountLockRetryLease(accountLockLeaseScheduleNextRetry)）。
// 修复前 chain 面零消费——同账户重试链路里引擎摘走的派发租约（5 分钟
// lease_until）悬挂至过期，同账户后续请求 AcquireRetryLease 返回 WaitMs 空耗
// 墙钟预算。
//
// 构造路径：真实链 + 瞬态上游（首请求 500、次请求 200）触发引擎内同账户重试；
// recordingLocks 顶替 engine.Locks 让 reserveSameAccountRetry 获取并消费一个
// 重试租约，重试成功的结果携带 ReleaseAccountLockRetryLease 回调。w1vServeV1
// 返回 200 即 run() 已退出：此刻 releases 恰好一条且 ScheduleNextRetry=false，
// 即「响应处理返回后租约被链面释放、不排下次重试」；同时协议验证成功的
// CompleteSuccessAsync 亦经 ConfirmAccountLockSuccess 结算一次。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// recordingLocks 记录账户锁端口操作（同账户重试链路：Acquire/Consume 走
// 引擎内路径，Release 由 chain 响应终态触发，CompleteSuccess 由协议成功
// 结算触发）。
type recordingLocks struct {
	mu           sync.Mutex
	acquires     int
	consumes     int
	releases     []gatewaydispatch.ReleaseRetryLeaseInput
	completeOK   int
	acquireCalls atomic.Int64
}

func (l *recordingLocks) FindStateAsync(context.Context, string) (*gatewaydispatch.AccountLockStateView, error) {
	return nil, nil // 无锁态观测：不阻断跨账户派发
}

func (l *recordingLocks) ListStatesAsync(context.Context, []string) (map[string]gatewaydispatch.AccountLockStateView, error) {
	return nil, nil
}

func (l *recordingLocks) AcquireRetryLeaseAsync(context.Context, string, int64) (gatewaydispatch.LockLeaseAcquire, error) {
	l.acquireCalls.Add(1)
	l.mu.Lock()
	l.acquires++
	l.mu.Unlock()
	return gatewaydispatch.LockLeaseAcquire{Allowed: true, LeaseID: "lease-v1-guard", WaitMs: 0}, nil
}

func (l *recordingLocks) ConsumeRetryLeaseAsync(context.Context, string, string) (bool, error) {
	l.mu.Lock()
	l.consumes++
	l.mu.Unlock()
	return true, nil
}

func (l *recordingLocks) ReleaseRetryLeaseAsync(_ context.Context, input gatewaydispatch.ReleaseRetryLeaseInput) (bool, error) {
	l.mu.Lock()
	l.releases = append(l.releases, input)
	l.mu.Unlock()
	return true, nil
}

func (l *recordingLocks) AbandonRetryReservationAsync(context.Context, gatewaydispatch.AccountLockRetryLease) error {
	return nil
}

func (l *recordingLocks) RecordFailureAsync(context.Context, string, string, *gatewaydispatch.AccountLockObservation) error {
	return nil
}

func (l *recordingLocks) SettleDeadlineAsync(context.Context, string, int64, *gatewaydispatch.AccountLockObservation) error {
	return nil
}

func (l *recordingLocks) CompleteSuccessAsync(context.Context, string, string, *gatewaydispatch.AccountLockObservation) error {
	l.mu.Lock()
	l.completeOK++
	l.mu.Unlock()
	return nil
}

// TestV1DispatchLoopConsumesAccountLockRetryLeaseAfterResponseHandling：
// 同账户重试成功后，重试租约在响应终态被 chain 释放（scheduleNextRetry=false，
// 不排下次重试），协议成功结算走 CompleteSuccessAsync。
func TestV1DispatchLoopConsumesAccountLockRetryLeaseAfterResponseHandling(t *testing.T) {
	var upstreamCalls atomic.Int64
	transientUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"transient boom","type":"server_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-lease-guard","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"租约守护"},"finish_reason":"stop"}]}`))
	}))
	defer transientUpstream.Close()

	fixture := newChainFixture(t)
	// 同账户重试的 configuredDelayMs = temporaryUnschedulableRetryIntervalSeconds
	// （引擎内真实 sleep），归零避免测试空等；重试次数默认 ≥1 即可。
	if _, err := fixture.db.Exec(`UPDATE system_settings SET value_json = '0' WHERE key = ?`, "temporaryUnschedulableRetryIntervalSeconds"); err != nil {
		t.Fatalf("shorten retry interval: %v", err)
	}
	secret := w1vSeedMultiGroupKey(t, fixture, []string{"w1v_group_retry_lease_guard"},
		map[int]bool{0: true}, map[int]string{0: transientUpstream.URL})
	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()

	locks := &recordingLocks{}
	chain.engine.Locks = locks

	status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s（同账户重试后成功派发是本守护的前提）", status, body)
	}
	if !strings.Contains(body, "租约守护") {
		t.Fatalf("下游必须收到重试成功的完整响应: %s", body)
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("上游调用次数=%d，期望 2（首次瞬态失败 + 同账户重试成功）", upstreamCalls.Load())
	}

	// 同账户重试链路确实发生：引擎内获取并消费了一个租约。
	if locks.consumes == 0 {
		t.Fatal("前置失败：同账户重试未消费租约（测试构造失效，检查重试触发条件）")
	}

	// 核心断言：响应终态恰好一次释放，scheduleNextRetry=false（修复前 0 次，
	// 租约悬挂至 5 分钟 lease_until 过期）。
	locks.mu.Lock()
	releases := append([]gatewaydispatch.ReleaseRetryLeaseInput(nil), locks.releases...)
	completeOK := locks.completeOK
	locks.mu.Unlock()
	if len(releases) != 1 {
		t.Fatalf("重试租约释放次数=%d，要求恰好 1（响应终态消费 ReleaseAccountLockRetryLease(false)；修复前为 0）", len(releases))
	}
	if releases[0].LeaseID != "lease-v1-guard" {
		t.Fatalf("释放的租约 = %+v（应为同账户重试获取的 lease-v1-guard）", releases[0])
	}
	if releases[0].ScheduleNextRetry {
		t.Fatalf("释放不得排下次重试（Go 链面无 Node 的 same-account 携带路径，Node 对应分支同样以 false 释放）: %+v", releases[0])
	}
	if completeOK != 1 {
		t.Fatalf("协议成功结算 CompleteSuccessAsync 次数=%d，要求恰好 1", completeOK)
	}
}
