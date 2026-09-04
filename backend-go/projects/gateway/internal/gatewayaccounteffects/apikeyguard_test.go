package gatewayaccounteffects

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func guardTestAccount(id, fingerprint string) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                        id,
		Status:                    "active",
		SelectedAPIKeyFingerprint: stringPtr(fingerprint),
	}
}

func newGuardForTest(t *testing.T, driver string) (*AccountAPIKeyFailureGuard, *FakeClock) {
	t.Helper()
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return NewAccountAPIKeyFailureGuard(SideEffectsConfig{RuntimeStateDriver: driver}, clock, nil, nil), clock
}

func TestFailureGuardThresholdBranches(t *testing.T) {
	tests := []struct {
		name          string
		account       gatewayruntimecache.OpenAIAccountSecret
		input         GatewayAccountApiKeyFailureGuardInput
		wantPersist   bool
		wantReason    string
	}{
		{
			name:        "未选择 key fingerprint",
			account:     gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"},
			input:       GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway},
			wantPersist: false, wantReason: GuardReasonNotSelectedAPIKey,
		},
		{
			name:    "持久变更授权直接放行",
			account: guardTestAccount("acc-1", "fp-1"),
			input: GatewayAccountApiKeyFailureGuardInput{
				TrafficSource:   TrafficSourceGateway,
				MutationContext: &AccountApiKeyPersistentMutationContext{Authority: MutationAuthorityExplicitUserPolicy, TrafficSource: TrafficSourceGateway},
			},
			wantPersist: true, wantReason: GuardReasonPersistentMutationAuthorized,
		},
		{
			name:    "带上下文但未授权",
			account: guardTestAccount("acc-1", "fp-1"),
			input: GatewayAccountApiKeyFailureGuardInput{
				TrafficSource:   TrafficSourceGateway,
				MutationContext: &AccountApiKeyPersistentMutationContext{Authority: MutationAuthorityAutomaticProbe, TrafficSource: TrafficSourceGateway, ProbeOutcome: ProbeOutcomeCompleteSuccess},
			},
			wantPersist: false, wantReason: GuardReasonPersistentMutationUnauthorized,
		},
		{
			name:        "非 gateway 流量来源",
			account:     guardTestAccount("acc-1", "fp-1"),
			input:       GatewayAccountApiKeyFailureGuardInput{TrafficSource: "account_health_check"},
			wantPersist: false, wantReason: GuardReasonPersistentMutationUnauthorized,
		},
		{
			name:        "redis driver 走短暂避让",
			account:     guardTestAccount("acc-1", "fp-1"),
			input:       GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway},
			wantPersist: false, wantReason: GuardReasonRedisTransientOnly,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := "memory"
			if tt.wantReason == GuardReasonRedisTransientOnly {
				driver = "redis"
			}
			guard, _ := newGuardForTest(t, driver)
			decision := guard.RecordFailureGuard(tt.account, tt.input)
			if decision.Persist != tt.wantPersist || decision.Reason != tt.wantReason {
				t.Fatalf("decision = %+v, want persist=%v reason=%s", decision, tt.wantPersist, tt.wantReason)
			}
		})
	}
}

func TestFailureGuardSuppressionLadder(t *testing.T) {
	guard, clock := newGuardForTest(t, "memory")
	account := guardTestAccount("acc-1", "fp-1")
	gatewayInput := GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway}
	ctx := context.Background()

	// 无 epoch 的首次观测：接受并进入本地屏蔽。
	wantDelays := []int64{3_000, 5_000, 10_000, 10_000}
	for attempt, wantDelay := range wantDelays {
		decision := guard.RecordFailureGuard(account, gatewayInput)
		if decision.Reason != GuardReasonGatewayLocalOnly || decision.Persist {
			t.Fatalf("attempt %d decision = %+v", attempt+1, decision)
		}
		states := guard.LocalRuntimeStatesForDispatch("acc-1")
		if len(states) != 1 {
			t.Fatalf("attempt %d states = %d, want 1", attempt+1, len(states))
		}
		wantUntil := NowMs(clock) + wantDelay
		nextProbe := canonicalRFC3339(time.UnixMilli(wantUntil))
		if *states[0].NextProbeAt != nextProbe {
			t.Fatalf("attempt %d suppressUntil = %s, want %s", attempt+1, *states[0].NextProbeAt, nextProbe)
		}
		if states[0].Status != string(APIKeyStatusTemporaryUnavailable) {
			t.Fatalf("attempt %d status = %s", attempt+1, states[0].Status)
		}
		clock.Advance(500 * time.Millisecond)
	}
	// 成功 guard 清除本地屏蔽并推进 fence。
	if !guard.RecordSuccessGuard(account) {
		t.Fatal("success guard should clear the suppression")
	}
	if states := guard.LocalRuntimeStatesForDispatch("acc-1"); len(states) != 0 {
		t.Fatalf("states after success = %d, want 0", len(states))
	}
	_ = ctx
}

func TestFailureGuardStaleObservationEpoch(t *testing.T) {
	guard, clock := newGuardForTest(t, "memory")
	account := guardTestAccount("acc-1", "fp-1")
	ctx := context.Background()
	_ = ctx

	epoch := guard.CaptureFailureObservation(account)
	if epoch == nil {
		t.Fatal("capture should return an epoch")
	}
	// 陈旧 epoch：被拒绝。
	staleEpoch := *epoch - 1
	decision := guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway, ObservationEpoch: &staleEpoch})
	if decision.Reason != GuardReasonStaleGatewayObservation {
		t.Fatalf("stale decision = %+v", decision)
	}
	// 当前 epoch：接受。
	decision = guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway, ObservationEpoch: epoch})
	if decision.Reason != GuardReasonGatewayLocalOnly {
		t.Fatalf("current decision = %+v", decision)
	}
	// fence 语义：等于最新 epoch 的观测在保留期内可重复接受。
	decision = guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway, ObservationEpoch: epoch})
	if decision.Reason != GuardReasonGatewayLocalOnly {
		t.Fatalf("replayed decision = %+v", decision)
	}
	// fence 过期后，同 epoch 也失效。
	clock.Advance(11 * time.Minute)
	decision = guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway, ObservationEpoch: epoch})
	if decision.Reason != GuardReasonStaleGatewayObservation {
		t.Fatalf("expired fence decision = %+v", decision)
	}
	// 非法 epoch（0/负数）被拒绝。
	zero := int64(0)
	decision = guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway, ObservationEpoch: &zero})
	if decision.Reason != GuardReasonStaleGatewayObservation {
		t.Fatalf("zero epoch decision = %+v", decision)
	}
}

func TestFailureGuardCredentialSourceAccountScope(t *testing.T) {
	guard, _ := newGuardForTest(t, "memory")
	source := "source-acc"
	account := guardTestAccount("acc-1", "fp-1")
	account.CredentialSourceAccountID = &source
	account.SelectedAPIKeyFingerprint = stringPtr(" fp-1 ")
	guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway})
	states := guard.LocalRuntimeStatesForDispatch("source-acc")
	if len(states) != 1 || states[0].KeyFingerprint != "fp-1" {
		t.Fatalf("states = %+v", states)
	}
	// 空 accountId 不返回状态。
	if states := guard.LocalRuntimeStatesForDispatch("  "); len(states) != 0 {
		t.Fatalf("blank account states = %d", len(states))
	}
}

func TestFailureGuardRedisDriverClearsLocalState(t *testing.T) {
	guard, _ := newGuardForTest(t, "memory")
	account := guardTestAccount("acc-1", "fp-1")
	guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway})
	if len(guard.LocalRuntimeStatesForDispatch("acc-1")) != 1 {
		t.Fatal("memory driver should keep the suppression")
	}
	// 切到 redis driver：进程内状态被清除且不再记录。
	guard.config.RuntimeStateDriver = "redis"
	if states := guard.LocalRuntimeStatesForDispatch("acc-1"); len(states) != 0 {
		t.Fatalf("redis driver states = %d, want 0", len(states))
	}
	if guard.RecordSuccessGuard(account) {
		t.Fatal("redis driver must not track process-local state")
	}
	guard.config.RuntimeStateDriver = "memory"
	if guard.RecordSuccessGuard(account) {
		t.Fatal("cleared local state should not report success")
	}
}

func TestFailureGuardSnapshotAndSuppressionsExpiry(t *testing.T) {
	guard, clock := newGuardForTest(t, "memory")
	account := guardTestAccount("acc-1", "fp-1")
	guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway, ErrorMessage: stringPtr("boom")})
	snapshot := guard.SnapshotForTest()
	if len(snapshot) != 1 || !snapshot[0].Suppressed || snapshot[0].LocalFailureCount != 1 || snapshot[0].Status != APIKeyStatusTemporaryUnavailable {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	clock.Advance(11 * time.Minute)
	snapshot = guard.SnapshotForTest()
	if len(snapshot) != 0 {
		t.Fatalf("expired snapshot = %+v", snapshot)
	}
}

func TestFailureGuardConcurrentAccess(t *testing.T) {
	guard, _ := newGuardForTest(t, "memory")
	var wg sync.WaitGroup
	for index := 0; index < 16; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			account := guardTestAccount("acc-1", "fp-1")
			guard.RecordFailureGuard(account, GatewayAccountApiKeyFailureGuardInput{TrafficSource: TrafficSourceGateway})
			guard.RecordSuccessGuard(account)
			guard.LocalRuntimeStatesForDispatch("acc-1")
			guard.CaptureFailureObservation(account)
		}(index)
	}
	wg.Wait()
}
