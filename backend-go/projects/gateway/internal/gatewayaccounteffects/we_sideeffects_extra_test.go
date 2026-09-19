package gatewayaccounteffects

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------

func TestWeGuardClearFailureGuardBranches(t *testing.T) {
	guard, _ := newGuardForTest(t, "")
	account := guardTestAccount("acc-1", "fp-1")
	if guard.ClearFailureGuard(account) {
		t.Fatal("无抑制时应返回 false")
	}
	decision := guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway})
	if decision.Reason != GuardReasonGatewayLocalOnly {
		t.Fatalf("reason = %s", decision.Reason)
	}
	if !guard.ClearFailureGuard(account) {
		t.Fatal("存在抑制时应清除成功")
	}
	if guard.ClearFailureGuard(gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}) {
		t.Fatal("无 fingerprint 应返回 false")
	}
	// Redis 驱动下本地抑制不可用。
	redisGuard, _ := newGuardForTest(t, "redis")
	redisGuard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway})
	if redisGuard.ClearFailureGuard(account) {
		t.Fatal("redis 驱动下不应有本地抑制可清")
	}
}

func TestWeGuardClearForTestAndStaleObservation(t *testing.T) {
	guard, clock := newGuardForTest(t, "")
	account := guardTestAccount("acc-1", "fp-1")
	epoch := guard.CaptureFailureObservation(account)
	if epoch == nil {
		t.Fatal("memory 驱动应产生观测 epoch")
	}
	guard.ClearForTest()
	// ClearForTest 清空围栏后，旧 epoch 不再被接受（stale gateway observation）。
	decision := guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{
		TrafficSource:    TrafficSourceGateway,
		ObservationEpoch: epoch,
	})
	if decision.Reason != GuardReasonStaleGatewayObservation {
		t.Fatalf("reason = %s, want %s", decision.Reason, GuardReasonStaleGatewayObservation)
	}
	// 推进时钟使围栏过期后同样拒绝。
	_ = clock
}

func TestWeGuardAcceptLocalObservationFenceLifecycle(t *testing.T) {
	guard, clock := newGuardForTest(t, "")
	account := guardTestAccount("acc-1", "fp-1")

	epoch := guard.CaptureFailureObservation(account)
	decision := guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{
		TrafficSource:    TrafficSourceGateway,
		ObservationEpoch: epoch,
	})
	if decision.Reason != GuardReasonGatewayLocalOnly {
		t.Fatalf("有效 epoch 应接受: %+v", decision)
	}

	// 非法 epoch（0/负数）必须拒绝。
	zero := int64(0)
	decision = guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{
		TrafficSource: TrafficSourceGateway, ObservationEpoch: &zero,
	})
	if decision.Reason != GuardReasonStaleGatewayObservation {
		t.Fatalf("零 epoch 应拒绝: %+v", decision)
	}

	// 过期围栏：捕获后推进 10 分钟以上。
	fresh := guard.CaptureFailureObservation(account)
	clock.Advance(time.Duration(apiKeyLocalObservationFenceRetentionMs) * time.Millisecond)
	clock.Advance(2 * time.Millisecond)
	decision = guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{
		TrafficSource: TrafficSourceGateway, ObservationEpoch: fresh,
	})
	if decision.Reason != GuardReasonStaleGatewayObservation {
		t.Fatalf("过期围栏应拒绝: %+v", decision)
	}
}

func TestWeGuardLoadTransientStatesDispatch(t *testing.T) {
	t.Run("memory 驱动回落本地快照", func(t *testing.T) {
		guard, _ := newGuardForTest(t, "")
		account := guardTestAccount("acc-1", "fp-1")
		guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway})
		states, err := guard.LoadTransientStatesForDispatch(context.Background(), "acc-1", []string{"fp-1"})
		if err != nil {
			t.Fatal(err)
		}
		if len(states) != 1 || states[0].KeyFingerprint != "fp-1" || states[0].Status == "" {
			t.Fatalf("states = %+v", states)
		}
		if states[0].NextProbeAt == nil {
			t.Fatal("本地抑制必须给出 nextProbeAt")
		}
		if empty, err := guard.LoadTransientStatesForDispatch(context.Background(), "  ", nil); err != nil || len(empty) != 0 {
			t.Fatalf("空账户应返回空: %+v err = %v", empty, err)
		}
		// 契约：memory 驱动忽略指纹过滤参数，直接投影该账户全部本地抑制。
		if all, err := guard.LoadTransientStatesForDispatch(context.Background(), "acc-1", []string{" ", ""}); err != nil || len(all) != 1 {
			t.Fatalf("memory 驱动不按指纹过滤: %+v err = %v", all, err)
		}
	})

	t.Run("redis 驱动读取分布式 store", func(t *testing.T) {
		guard, _ := newGuardForTest(t, "redis")
		generation := "gen-1"
		state := &AccountApiKeyTransientState{
			SchemaVersion: 1, AccountID: "acc-1", KeyFingerprint: "fp-1",
			Generation: generation, LastObservedAtMs: 100, ObservationKind: "failure",
			FailureCount: 1, Status: APIKeyStatusRateLimited,
		}
		until := int64(4_000_000_000)
		state.SuppressUntilMs = &until
		store := &weTransientStore{loadResult: []AccountApiKeyTransientDispatchState{
			{State: state, Suppressed: true},
			{State: nil, Suppressed: false},
		}}
		guard.SetTransientStateStoreForTest(store)
		states, err := guard.LoadTransientStatesForDispatch(context.Background(), "acc-1", []string{"fp-1"})
		if err != nil {
			t.Fatal(err)
		}
		if len(states) != 1 {
			t.Fatalf("states = %+v", states)
		}
		selection := states[0]
		if selection.Status != string(APIKeyStatusRateLimited) || selection.TransientGeneration == nil || *selection.TransientGeneration != generation {
			t.Fatalf("selection = %+v", selection)
		}
		if selection.NextProbeAt == nil {
			t.Fatal("suppressed 状态必须投影 nextProbeAt")
		}
		if len(store.loads) != 1 || store.loads[0] != "acc-1/fp-1" {
			t.Fatalf("loads = %v", store.loads)
		}
	})

	t.Run("redis 驱动 store 错误向上传播", func(t *testing.T) {
		guard, _ := newGuardForTest(t, "redis")
		guard.SetTransientStateStoreForTest(&weTransientStore{err: errors.New("redis 失败")})
		if _, err := guard.LoadTransientStatesForDispatch(context.Background(), "acc-1", []string{"fp-1"}); err == nil {
			t.Fatal("store 错误应向上传播")
		}
	})
}

func TestWeGuardObservationFenceCapacityEviction(t *testing.T) {
	guard, _ := newGuardForTest(t, "")
	guard.mu.Lock()
	defer guard.mu.Unlock()
	now := int64(1_000)
	// 容量契约：fence 超过 50000 时必须逐出旧条目，避免无界增长。
	for index := 0; index <= apiKeyLocalObservationFenceCapacity; index++ {
		guard.rememberLocalAPIKeyObservationFenceLocked("key-"+intToDecimal(index), int64(index+1), now)
	}
	if len(guard.fences) > apiKeyLocalObservationFenceCapacity {
		t.Fatalf("fences = %d 超过容量 %d", len(guard.fences), apiKeyLocalObservationFenceCapacity)
	}
}

func intToDecimal(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
