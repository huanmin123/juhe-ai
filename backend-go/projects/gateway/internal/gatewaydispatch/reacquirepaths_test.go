package gatewaydispatch

// P0-1（PLAN-20260918T142845703Z W1）reacquire 失败出口回归测试：
// key-model 准入失败/busy/blocked 显式释放槽并置 reacquire 后，下一轮
// rotationLoop 重新获取并发槽的两个失败出口——
//   - 未获取出口：生产并发实现未 Acquired 时 Release 为 nil
//     （cmd/juhe-ai-gateway/chain_dispatch.go:247-248），旧版在 break 后经
//     releaseTransientState 调用 nil 函数 panic；
//   - acquireErr 出口：旧版对已显式释放的旧槽二次 Release（生产 Release
//     非幂等，会多减并发计数吞掉其他在途请求的槽）。
// 与 w13g3AcquireConcurrency（未获取槽带非 nil Release，掩盖本缺陷）不同，
// 这里的 fake 贴近生产实现。全部 fake 驱动，无真实 PG/Redis。

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
)

var errReacquireScripted = errors.New("reacquire scripted failure")

// reacquireScriptedConcurrency 第一次 TryAcquire 成功并以计数闭包记录该槽
// 的 Release 次数；第二次（reacquire）起按脚本失败：errFromSecond 非 nil
// 时返回错误，否则返回贴近生产的未获取槽（Release=nil）。
type reacquireScriptedConcurrency struct {
	fakeConcurrencyStore
	errFromSecond error
	firstReleases atomic.Int64
	calls         int
}

func (c *reacquireScriptedConcurrency) TryAcquireAsync(ctx context.Context, accountID string, concurrencyLimit int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	c.calls++
	if c.calls == 1 {
		return ConcurrencySlot{
			Acquired:        true,
			Current:         1,
			Limit:           concurrencyLimit,
			Lane:            options.Lane,
			Release:         func() { c.firstReleases.Add(1) },
			MarkFirstOutput: func() {},
		}, nil
	}
	if c.errFromSecond != nil {
		return ConcurrencySlot{}, c.errFromSecond
	}
	// 生产实现（chain_dispatch.go:247-248）未 Acquired 时不设置 Release。
	return ConcurrencySlot{Acquired: false, Current: concurrencyLimit, Limit: concurrencyLimit, Lane: options.Lane}, nil
}

// busyEngine 构造"第一个 key 准入 busy → 显式释放槽 → reacquire"的公共
// 前置：busy 触发点在 key-model 准入（上游请求发出之前），fake 默认 URL
// 不会被访问。
func busyEngine(t *testing.T) *Engine {
	t.Helper()
	engine, _, _ := newTestEngine(t)
	engine.Config.AccountConcurrencyRetryBudgetMs = 0
	engine.Config.KeyModelForegroundQueuePollMs = 5
	engine.KeyModel = &fakeKeyModelAdmission{statuses: map[string][]gatewayaccounteffects.AttemptPreparationStatus{
		"a-1": {gatewayaccounteffects.AttemptPreparationBusy},
	}}
	return engine
}

// TestReacquireNotAcquiredNilReleaseSlotDoesNotPanic: reacquire 未获取且
// 槽 Release=nil（生产形态）时，单账户以 capacity-limit 跳过并优雅收敛为
// UpstreamAttemptError，不 panic；旧槽 Release 恰好被调用一次（busy 路径
// 的显式释放，无二次释放）。
func TestReacquireNotAcquiredNilReleaseSlotDoesNotPanic(t *testing.T) {
	engine := busyEngine(t)
	concurrency := &reacquireScriptedConcurrency{}
	engine.Concurrency = concurrency
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	// panic 会直接失败本测试；此处断言优雅返回错误。
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, []AccountCandidate{
		multiKeyTestAccount("a-1", "key-a", "key-b"),
	}))
	var attemptErr *UpstreamAttemptError
	if !errorsAs(err, &attemptErr) {
		t.Fatalf("expected graceful UpstreamAttemptError after reacquire not-acquired, got %v", err)
	}
	if concurrency.calls < 2 {
		t.Fatalf("the busy release must be followed by a reacquire acquire, TryAcquire calls = %d", concurrency.calls)
	}
	if got := concurrency.firstReleases.Load(); got != 1 {
		t.Fatalf("the old slot must be released exactly once by the busy path, releases = %d", got)
	}
}

// TestReacquireAcquireErrorReleasesOldSlotExactlyOnce: reacquire 获取返回
// error 时错误原样上抛，且旧槽 Release 恰好被调用一次（busy 路径的显式释
// 放；acquireErr 出口的 releaseTransientState 不得二次释放旧槽）。
func TestReacquireAcquireErrorReleasesOldSlotExactlyOnce(t *testing.T) {
	engine := busyEngine(t)
	concurrency := &reacquireScriptedConcurrency{errFromSecond: errReacquireScripted}
	engine.Concurrency = concurrency
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	_, err := engine.FetchFirstAvailableUpstream(context.Background(), dispatchArgs(t, req, []AccountCandidate{
		multiKeyTestAccount("a-1", "key-a", "key-b"),
	}))
	if !errors.Is(err, errReacquireScripted) {
		t.Fatalf("expected the reacquire acquire error to surface, got %v", err)
	}
	if concurrency.calls < 2 {
		t.Fatalf("the busy release must be followed by a reacquire acquire, TryAcquire calls = %d", concurrency.calls)
	}
	if got := concurrency.firstReleases.Load(); got != 1 {
		t.Fatalf("the old slot must be released exactly once by the busy path, releases = %d", got)
	}
}
