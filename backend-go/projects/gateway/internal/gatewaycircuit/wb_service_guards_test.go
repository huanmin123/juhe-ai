package gatewaycircuit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// wbParentFailStore 在第二次读取父作用域时注入失败，模拟父状态读取竞态。
type wbParentFailStore struct {
	Store
	mu          chan struct{}
	parentCalls int
}

func (s *wbParentFailStore) Get(ctx context.Context, scope Scope, nowMs *int64) (State, error) {
	if scope.Kind == ScopeKindAccount {
		s.mu <- struct{}{}
		s.parentCalls++
		if s.parentCalls > 1 {
			<-s.mu
			return State{}, errors.New("父状态读取失败")
		}
		<-s.mu
	}
	return s.Store.Get(ctx, scope, nowMs)
}

// 释放已获确认租约的契约：父状态在租约建立后读取失败必须释放租约并上抛。
func TestWBServiceReleasesConfirmationWhenParentReadFails(t *testing.T) {
	now := int64(0)
	clock := &now
	inner := newNonExpiringMemoryStore(t)
	store := &wbParentFailStore{Store: inner, mu: make(chan struct{}, 1)}
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	if _, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope:                        protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision:             revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason:                       "transport:connect failed",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3000
	if _, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("b", 64)),
	}); err == nil || !strings.Contains(err.Error(), "父状态读取失败") {
		t.Fatalf("父读取失败必须上抛: %v", err)
	}
}

// 重建超时契约：分页读取超过页超时必须以 rebuildError 终止并返回超时原因。
func TestWBBridgeRebuildPageTimeout(t *testing.T) {
	db := wbNewFakeDB()
	store := newNonExpiringMemoryStore(t)
	options := BridgeOptions{
		Store: store, DB: db, OwnerID: "wb-timeout",
		Now:                  func() int64 { return 1_000 },
		Sleep:                func(context.Context, time.Duration) error { return nil },
		RebuildPageTimeoutMs: 1,
		LoadRebuildPage: func(ctx context.Context, _ RebuildPageInput) (RebuildPage, error) {
			<-ctx.Done()
			return RebuildPage{}, ctx.Err()
		},
	}
	bridge, err := NewBridge(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bridge.Close)
	result, err := bridge.Rebuild(context.Background())
	if err != nil || !result.Blocked || result.Reason != RebuildReasonRebuildTimeout {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// 重新准备契约：确认租约不匹配 / 证据重复 / 资格关闭都必须阻塞且不穿透。
// 顺序敏感：合法重递送必须先于消耗租约的 ineligible 用例执行。
func TestWBServicePrepareAttemptConfirmationGuards(t *testing.T) {
	now := int64(0)
	clock := &now
	store := newNonExpiringMemoryStore(t)
	service, _ := newTestService(t, store, ServiceOptions{Now: func() int64 { return *clock }})
	if _, err := service.SuspectForegroundFailure(context.Background(), suspectForegroundInput{
		scope:                        protocolModelScope(testAccount(), LaneText, strPtr("gpt-4o")),
		dispatchRevision:             revisionOf(t, testAccount()),
		confirmationFailuresRequired: int64Ptr(2),
		reason:                       "transport:connect failed",
	}); err != nil {
		t.Fatalf("suspect: %v", err)
	}
	*clock = 3000
	acquired, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
		Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
		ConfirmationLeaseDurationMs: 30_000, FailureEvidenceKey: strPtr(strings.Repeat("b", 64)),
	})
	if err != nil || !acquired.Attempt.IsConfirmation() {
		t.Fatalf("acquire = %+v err=%v", acquired, err)
	}
	confirmation := *acquired.Attempt.confirmation

	t.Run("valid confirmation redelivers attempt", func(t *testing.T) {
		result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
			Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
			ConfirmationLeaseDurationMs: 30_000, Confirmation: &confirmation,
			FailureEvidenceKey: strPtr(strings.Repeat("c", 64)),
		})
		if err != nil || result.Outcome != PrepareDispatchable || !result.Attempt.IsConfirmation() {
			t.Fatalf("valid confirmation = %+v err=%v", result, err)
		}
	})

	t.Run("acquisition evidence reentry stays dispatchable", func(t *testing.T) {
		result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
			Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
			ConfirmationLeaseDurationMs: 30_000, Confirmation: &confirmation,
			FailureEvidenceKey: strPtr(strings.Repeat("b", 64)),
		})
		if err != nil || result.Outcome != PrepareDispatchable {
			t.Fatalf("同一请求重入必须继续确认: %+v err=%v", result, err)
		}
	})

	t.Run("scope mismatch blocks", func(t *testing.T) {
		drifted := confirmation
		drifted.ScopeKey = MustScopeKey(protocolModelScope(testAccount(), LaneImage, strPtr("gpt-4o")))
		result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
			Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
			ConfirmationLeaseDurationMs: 30_000, Confirmation: &drifted,
		})
		if err != nil || result.Outcome != PrepareBlocked {
			t.Fatalf("scope mismatch = %+v err=%v", result, err)
		}
	})

	t.Run("ineligible completes unknown", func(t *testing.T) {
		eligible := false
		result, err := service.PrepareAttempt(context.Background(), PrepareAttemptInput{
			Account: testAccount(), RequestLane: LaneText, Model: strPtr("gpt-4o"),
			ConfirmationLeaseDurationMs: 30_000, Confirmation: &confirmation,
			ConfirmationEligible: &eligible,
		})
		if err != nil || result.Outcome != PrepareBlocked {
			t.Fatalf("ineligible = %+v err=%v", result, err)
		}
	})
}
