package gatewayaccounteffects

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---------------------------------------------------------------------------
// AccountAPIKeyEffects（apikeyeffects.go）：账户内 API Key 运行态写入面
// ---------------------------------------------------------------------------

// weAPIKeyWriter 可脚本化的 AccountAPIKeyWriter，用 channel 让异步写入可等待。
type weAPIKeyWriter struct {
	mu            sync.Mutex
	failureWrites []AccountAPIKeyFailureWrite
	successWrites []AccountAPIKeySuccessWrite
	failureResult APIKeyWriteResult
	failureErr    error
	successResult APIKeyWriteResult
	successErr    error
	failureDone   chan struct{}
	successDone   chan struct{}
}

func (w *weAPIKeyWriter) RecordFailure(_ context.Context, write AccountAPIKeyFailureWrite) (APIKeyWriteResult, error) {
	w.mu.Lock()
	w.failureWrites = append(w.failureWrites, write)
	result, err := w.failureResult, w.failureErr
	w.mu.Unlock()
	if w.failureDone != nil {
		w.failureDone <- struct{}{}
	}
	return result, err
}

func (w *weAPIKeyWriter) RecordSuccess(_ context.Context, write AccountAPIKeySuccessWrite) (APIKeyWriteResult, error) {
	w.mu.Lock()
	w.successWrites = append(w.successWrites, write)
	result, err := w.successResult, w.successErr
	w.mu.Unlock()
	if w.successDone != nil {
		w.successDone <- struct{}{}
	}
	return result, err
}

func (w *weAPIKeyWriter) failureCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.failureWrites)
}

func (w *weAPIKeyWriter) successCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.successWrites)
}

// weTransientStore 可脚本化的 transient store，记录 mutation 并按脚本返回。
type weTransientStore struct {
	mu         sync.Mutex
	failures   []TransientMutationInput
	successes  []TransientMutationInput
	loads      []string
	err        error
	loadResult []AccountApiKeyTransientDispatchState
}

func (s *weTransientStore) RecordFailure(_ context.Context, input TransientMutationInput) (AccountApiKeyTransientMutationResult, error) {
	s.mu.Lock()
	s.failures = append(s.failures, input)
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return AccountApiKeyTransientMutationResult{}, err
	}
	return AccountApiKeyTransientMutationResult{Applied: true, Reason: TransientReasonApplied}, nil
}

func (s *weTransientStore) RecordSuccess(_ context.Context, input TransientMutationInput) (AccountApiKeyTransientMutationResult, error) {
	s.mu.Lock()
	s.successes = append(s.successes, input)
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return AccountApiKeyTransientMutationResult{}, err
	}
	return AccountApiKeyTransientMutationResult{Applied: true, Reason: TransientReasonApplied}, nil
}

func (s *weTransientStore) LoadMany(_ context.Context, accountID string, keyFingerprints []string) ([]AccountApiKeyTransientDispatchState, error) {
	s.mu.Lock()
	s.loads = append(s.loads, accountID+"/"+strings_Join(keyFingerprints, ","))
	result, err := s.loadResult, s.err
	s.mu.Unlock()
	return result, err
}

func (s *weTransientStore) failureMutationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.failures)
}

func (s *weTransientStore) successMutationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.successes)
}

// strings_Join 避免与标准库 strings 的导入名冲突的小工具。
func strings_Join(values []string, sep string) string {
	out := ""
	for index, value := range values {
		if index > 0 {
			out += sep
		}
		out += value
	}
	return out
}

func weAPIKeyAccount(id string) gatewayruntimecache.OpenAIAccountSecret {
	fingerprint := "fp-1"
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                        id,
		Status:                    "active",
		SelectedAPIKeyFingerprint: &fingerprint,
	}
}

func weExplicitFailureContext() *AccountApiKeyPersistentMutationContext {
	return &AccountApiKeyPersistentMutationContext{
		Authority:     MutationAuthorityExplicitUserPolicy,
		TrafficSource: TrafficSourceGateway,
	}
}

func weNewEffects(t *testing.T, driver string, writer AccountAPIKeyWriter, guard *AccountAPIKeyFailureGuard, invalidate func()) (*AccountAPIKeyEffects, *ManualScheduler, *FakeClock) {
	t.Helper()
	clock := NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	scheduler := NewManualScheduler()
	effects := NewAccountAPIKeyEffects(SideEffectsConfig{RuntimeStateDriver: driver}, guard, writer, invalidate, clock, scheduler, NopLogger{})
	return effects, scheduler, clock
}

func TestWeAPIKeyEffectsRecordFailureSkipPaths(t *testing.T) {
	tests := []struct {
		name    string
		account gatewayruntimecache.OpenAIAccountSecret
	}{
		{name: "未选择 key fingerprint", account: gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}},
		{name: "运行态写开关关闭", account: func() gatewayruntimecache.OpenAIAccountSecret {
			account := weAPIKeyAccount("acc-1")
			account.APIKeyRuntimeStateDisabled = true
			return account
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &weAPIKeyWriter{}
			guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
			effects, _, _ := weNewEffects(t, "", writer, guard, nil)
			effects.RecordFailure(context.Background(), tt.account, RecordFailureInput{
				Status:        APIKeyStatusRateLimited,
				TrafficSource: TrafficSourceGateway,
			})
			if writer.failureCount() != 0 {
				t.Fatalf("failure writes = %d, want 0", writer.failureCount())
			}
		})
	}
}

func TestWeAPIKeyEffectsRecordFailureMemoryDriverPersists(t *testing.T) {
	writer := &weAPIKeyWriter{failureResult: APIKeyWriteResult{Changed: true}}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	invalidations := 0
	effects, _, _ := weNewEffects(t, "", writer, guard, func() { invalidations++ })
	account := weAPIKeyAccount("acc-1")
	cooldown := "2026-01-01T01:00:00.000Z"
	effects.RecordFailure(context.Background(), account, RecordFailureInput{
		Status:            APIKeyStatusRateLimited,
		ErrorCode:         stringPtr("rate_limited"),
		TrafficSource:     TrafficSourceGateway,
		MutationContext:   weExplicitFailureContext(),
		CooldownUntil:     &cooldown,
		QuotaRecoveryMode: QuotaRecoveryModeExplicitReset,
		Source:            "gateway_request",
	})
	if writer.failureCount() != 1 {
		t.Fatalf("failure writes = %d, want 1", writer.failureCount())
	}
	write := writer.failureWrites[0]
	if write.Input.Status != APIKeyStatusRateLimited || write.Input.CooldownUntil == nil || *write.Input.CooldownUntil != cooldown {
		t.Fatalf("write input = %+v", write.Input)
	}
	// quota 恢复模式要求携带期望的账户配置版本，用于乐观并发控制。
	if write.Input.ExpectedAccountConfigRevision != nil {
		t.Fatalf("未设置 ConfigRevision 时期望版本应为 nil: %v", *write.Input.ExpectedAccountConfigRevision)
	}
	if invalidations != 1 {
		t.Fatalf("invalidations = %d, want 1", invalidations)
	}
}

func TestWeAPIKeyEffectsRecordFailureMemoryDriverErrorSwallowed(t *testing.T) {
	// 契约：失败写错误只告警，不影响网关请求路径。
	writer := &weAPIKeyWriter{failureErr: errors.New("db 失败")}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	effects, _, _ := weNewEffects(t, "", writer, guard, nil)
	effects.RecordFailure(context.Background(), weAPIKeyAccount("acc-1"), RecordFailureInput{
		Status:          APIKeyStatusError,
		TrafficSource:   TrafficSourceGateway,
		MutationContext: weExplicitFailureContext(),
	})
	if writer.failureCount() != 1 {
		t.Fatalf("failure writes = %d, want 1", writer.failureCount())
	}
}

func TestWeAPIKeyEffectsRecordFailureQuotaModeCarriesConfigRevision(t *testing.T) {
	writer := &weAPIKeyWriter{failureResult: APIKeyWriteResult{Changed: true}}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	effects, _, _ := weNewEffects(t, "", writer, guard, nil)
	account := weAPIKeyAccount("acc-1")
	revision := int64(9)
	account.ConfigRevision = &revision
	effects.RecordFailure(context.Background(), account, RecordFailureInput{
		Status:            APIKeyStatusTemporaryUnavailable,
		TrafficSource:     TrafficSourceGateway,
		MutationContext:   weExplicitFailureContext(),
		QuotaRecoveryMode: QuotaRecoveryModeGeneric,
	})
	if writer.failureCount() != 1 {
		t.Fatalf("failure writes = %d", writer.failureCount())
	}
	carried := writer.failureWrites[0].Input.ExpectedAccountConfigRevision
	if carried == nil || *carried != 9 {
		t.Fatalf("ExpectedAccountConfigRevision = %v, want 9", carried)
	}
}

func TestWeAPIKeyEffectsRecordFailureUnauthorizedTrafficSourceSkipped(t *testing.T) {
	// 契约：非 gateway 流量且无授权上下文时，guard 不持久化也不本地记账。
	writer := &weAPIKeyWriter{}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	effects, _, _ := weNewEffects(t, "", writer, guard, nil)
	effects.RecordFailure(context.Background(), weAPIKeyAccount("acc-1"), RecordFailureInput{
		Status:        APIKeyStatusRateLimited,
		TrafficSource: "account_health_check",
	})
	if writer.failureCount() != 0 {
		t.Fatalf("failure writes = %d, want 0", writer.failureCount())
	}
	if guard.SnapshotForTest() != nil && len(guard.SnapshotForTest()) != 0 {
		t.Fatalf("不应产生本地抑制: %+v", guard.SnapshotForTest())
	}
}

func TestWeAPIKeyEffectsRecordFailureRedisDriverAsyncWrite(t *testing.T) {
	writer := &weAPIKeyWriter{failureResult: APIKeyWriteResult{Changed: true}, failureDone: make(chan struct{}, 1)}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{RuntimeStateDriver: "redis"}, nil, nil, nil)
	invalidations := 0
	effects, _, _ := weNewEffects(t, "redis", writer, guard, func() { invalidations++ })
	effects.RecordFailure(context.Background(), weAPIKeyAccount("acc-1"), RecordFailureInput{
		Status:          APIKeyStatusRateLimited,
		TrafficSource:   TrafficSourceGateway,
		MutationContext: weExplicitFailureContext(),
	})
	select {
	case <-writer.failureDone:
	case <-time.After(5 * time.Second):
		t.Fatal("异步失败写未完成")
	}
	if invalidations != 1 {
		t.Fatalf("invalidations = %d, want 1", invalidations)
	}
}

func TestWeAPIKeyEffectsRecordFailureRedisTransientOnlyWritesTransient(t *testing.T) {
	// 契约：Redis 驱动下 guard 记账退化为 Redis 瞬时避让，本地抑制不可用。
	store := &weTransientStore{}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{RuntimeStateDriver: "redis"}, nil, nil, nil)
	guard.SetTransientStateStoreForTest(store)
	writer := &weAPIKeyWriter{}
	effects, _, _ := weNewEffects(t, "redis", writer, guard, nil)
	account := weAPIKeyAccount("acc-1")
	generation := "gen-1"
	account.SelectedAPIKeyTransientGeneration = &generation
	effects.RecordFailure(context.Background(), account, RecordFailureInput{
		Status:        APIKeyStatusRateLimited,
		TrafficSource: TrafficSourceGateway,
	})
	if store.failureMutationCount() != 1 {
		t.Fatalf("transient failures = %d, want 1", store.failureMutationCount())
	}
	if writer.failureCount() != 0 {
		t.Fatalf("持久失败写应为 0, got %d", writer.failureCount())
	}
}

func TestWeAPIKeyEffectsRecordFailureConfirmedRotationWritesTransient(t *testing.T) {
	// 契约：Redis 驱动下已确认的账户内 key 切换额外写一次瞬时避让。
	store := &weTransientStore{}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{RuntimeStateDriver: "redis"}, nil, nil, nil)
	guard.SetTransientStateStoreForTest(store)
	writer := &weAPIKeyWriter{failureDone: make(chan struct{}, 1)}
	effects, _, _ := weNewEffects(t, "redis", writer, guard, nil)
	account := weAPIKeyAccount("acc-1")
	generation := "gen-1"
	account.SelectedAPIKeyTransientGeneration = &generation
	effects.RecordFailure(context.Background(), account, RecordFailureInput{
		Status:          APIKeyStatusError,
		TrafficSource:   TrafficSourceGateway,
		MutationContext: weExplicitFailureContext(),
		Source:          "same_account_api_key_rotation_confirmed",
	})
	select {
	case <-writer.failureDone:
	case <-time.After(5 * time.Second):
		t.Fatal("异步持久写未完成")
	}
	if store.failureMutationCount() != 1 {
		t.Fatalf("transient failures = %d, want 1", store.failureMutationCount())
	}
	if writer.failureCount() != 1 {
		t.Fatalf("持久失败写 = %d, want 1", writer.failureCount())
	}
}

func TestWeAPIKeyEffectsRecordFailureTransientStoreErrorSwallowed(t *testing.T) {
	store := &weTransientStore{err: errors.New("redis 失败")}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{RuntimeStateDriver: "redis"}, nil, nil, nil)
	guard.SetTransientStateStoreForTest(store)
	writer := &weAPIKeyWriter{}
	effects, _, _ := weNewEffects(t, "redis", writer, guard, nil)
	account := weAPIKeyAccount("acc-1")
	generation := "gen-1"
	account.SelectedAPIKeyTransientGeneration = &generation
	effects.RecordFailure(context.Background(), account, RecordFailureInput{
		Status:        APIKeyStatusRateLimited,
		TrafficSource: TrafficSourceGateway,
	})
	if store.failureMutationCount() != 1 {
		t.Fatalf("transient failures = %d, want 1", store.failureMutationCount())
	}
	if writer.failureCount() != 0 {
		t.Fatalf("持久失败写应为 0, got %d", writer.failureCount())
	}
}

func weAutomaticSuccessContext() *AccountApiKeyPersistentMutationContext {
	return &AccountApiKeyPersistentMutationContext{
		Authority:     MutationAuthorityAutomaticProbe,
		TrafficSource: string(TrafficSourceAccountHealthCheck),
		ProbeOutcome:  ProbeOutcomeCompleteSuccess,
	}
}

func TestWeAPIKeyEffectsRecordSuccessCoalescesWrites(t *testing.T) {
	writer := &weAPIKeyWriter{successResult: APIKeyWriteResult{Changed: true}}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	invalidations := 0
	effects, _, clock := weNewEffects(t, "", writer, guard, func() { invalidations++ })
	account := weAPIKeyAccount("acc-1")
	ctx := weAutomaticSuccessContext()

	// 同一 (账户, fingerprint) 的两次成功在 250ms 合并窗口内只落一次库。
	effects.RecordSuccess(context.Background(), account, RecordSuccessInput{
		Source: "probe", TrafficSource: string(TrafficSourceAccountHealthCheck), MutationContext: ctx,
	})
	clock.Advance(5 * time.Millisecond)
	effects.RecordSuccess(context.Background(), account, RecordSuccessInput{
		Source: "probe", TrafficSource: string(TrafficSourceAccountHealthCheck), MutationContext: ctx,
	})
	effects.FlushSuccessWritesForTest(context.Background())

	if writer.successCount() != 1 {
		t.Fatalf("success writes = %d, want 1", writer.successCount())
	}
	write := writer.successWrites[0]
	if write.TrafficSource != string(TrafficSourceAccountHealthCheck) {
		t.Fatalf("trafficSource = %s", write.TrafficSource)
	}
	if invalidations != 1 {
		t.Fatalf("invalidations = %d, want 1", invalidations)
	}
	// 合并后应保留最新 observedAt（时钟前进 5ms 后的投影）。
	if write.ObservedAt == "2026-01-01T00:00:00.000Z" {
		t.Fatalf("应保留较新的 observedAt, got %s", write.ObservedAt)
	}
}

func TestWeAPIKeyEffectsRecordSuccessFlushErrorDropsEntry(t *testing.T) {
	// 契约：成功写失败只告警且合并条目被丢弃（单次写入机会，不进入重试循环），
	// 网关请求路径不受影响。
	writer := &weAPIKeyWriter{successErr: errors.New("db 失败")}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	effects, _, _ := weNewEffects(t, "", writer, guard, nil)
	effects.RecordSuccess(context.Background(), weAPIKeyAccount("acc-1"), RecordSuccessInput{
		Source: "probe", TrafficSource: string(TrafficSourceAccountHealthCheck), MutationContext: weAutomaticSuccessContext(),
	})
	effects.FlushSuccessWritesForTest(context.Background())
	if writer.successCount() != 1 {
		t.Fatalf("第一次 flush = %d, want 1", writer.successCount())
	}
	// 失败的条目已删除：再次 flush 不产生第二次写入。
	effects.FlushSuccessWritesForTest(context.Background())
	if writer.successCount() != 1 {
		t.Fatalf("失败的合并条目不应重试, got %d", writer.successCount())
	}
}

func TestWeAPIKeyEffectsRecordSuccessGatewayGuardWithoutContext(t *testing.T) {
	// 契约：gateway 流量 + 无授权上下文时只更新本地成功围栏，不落持久成功写。
	writer := &weAPIKeyWriter{}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	effects, _, _ := weNewEffects(t, "", writer, guard, nil)
	effects.RecordSuccess(context.Background(), weAPIKeyAccount("acc-1"), RecordSuccessInput{
		Source: "gateway", TrafficSource: TrafficSourceGateway,
	})
	if writer.successCount() != 0 {
		t.Fatalf("success writes = %d, want 0", writer.successCount())
	}
}

func TestWeAPIKeyEffectsRecordSuccessDeniedAuthoritySkipped(t *testing.T) {
	// explicit_user_policy 授权只允许失败写；成功写必须被拒绝。
	writer := &weAPIKeyWriter{}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	effects, _, _ := weNewEffects(t, "", writer, guard, nil)
	effects.RecordSuccess(context.Background(), weAPIKeyAccount("acc-1"), RecordSuccessInput{
		Source: "gateway", TrafficSource: TrafficSourceGateway, MutationContext: weExplicitFailureContext(),
	})
	if writer.successCount() != 0 {
		t.Fatalf("success writes = %d, want 0", writer.successCount())
	}
}

func TestWeAPIKeyEffectsRecordSuccessDisabledOrMissingFingerprint(t *testing.T) {
	writer := &weAPIKeyWriter{}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	effects, _, _ := weNewEffects(t, "", writer, guard, nil)

	disabled := weAPIKeyAccount("acc-1")
	disabled.APIKeyRuntimeStateDisabled = true
	effects.RecordSuccess(context.Background(), disabled, RecordSuccessInput{
		Source: "probe", TrafficSource: string(TrafficSourceAccountHealthCheck), MutationContext: weAutomaticSuccessContext(),
	})

	noFingerprint := gatewayruntimecache.OpenAIAccountSecret{ID: "acc-2", Status: "active"}
	effects.RecordSuccess(context.Background(), noFingerprint, RecordSuccessInput{
		Source: "probe", TrafficSource: string(TrafficSourceAccountHealthCheck), MutationContext: weAutomaticSuccessContext(),
	})

	if writer.successCount() != 0 {
		t.Fatalf("success writes = %d, want 0", writer.successCount())
	}
}

func TestWeAPIKeyEffectsRecordSuccessClearsTransientOnGatewayRedis(t *testing.T) {
	// 契约：Redis 驱动下 gateway 成功观测要清理该 key 的瞬时避让。
	// gateway + 非空授权上下文的成功写会被拒绝，因此用 nil 上下文触发清理分支：
	// 此处改用带指纹 + redis 驱动 + gateway 流量（上下文为 nil → 只走 guard 清理）。
	store := &weTransientStore{}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{RuntimeStateDriver: "redis"}, nil, nil, nil)
	guard.SetTransientStateStoreForTest(store)
	writer := &weAPIKeyWriter{}
	effects, _, _ := weNewEffects(t, "redis", writer, guard, nil)
	account := weAPIKeyAccount("acc-1")
	generation := "gen-1"
	account.SelectedAPIKeyTransientGeneration = &generation
	effects.RecordSuccess(context.Background(), account, RecordSuccessInput{
		Source: "gateway", TrafficSource: TrafficSourceGateway,
	})
	if store.successMutationCount() != 1 {
		t.Fatalf("transient successes = %d, want 1", store.successMutationCount())
	}
}

func TestWeAPIKeyEffectsRecordSuccessTransientClearErrorSwallowed(t *testing.T) {
	store := &weTransientStore{err: errors.New("redis 失败")}
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{RuntimeStateDriver: "redis"}, nil, nil, nil)
	guard.SetTransientStateStoreForTest(store)
	writer := &weAPIKeyWriter{}
	effects, _, _ := weNewEffects(t, "redis", writer, guard, nil)
	account := weAPIKeyAccount("acc-1")
	generation := "gen-1"
	account.SelectedAPIKeyTransientGeneration = &generation
	effects.RecordSuccess(context.Background(), account, RecordSuccessInput{
		Source: "gateway", TrafficSource: TrafficSourceGateway,
	})
	if store.successMutationCount() != 1 {
		t.Fatalf("transient successes = %d, want 1", store.successMutationCount())
	}
}

func TestWeAPIKeyEffectsDefaultDepsAndWriteKey(t *testing.T) {
	// 契约：nil 依赖回落系统时钟/真实调度器/NopLogger，缓存失效可缺省。
	guard := NewAccountAPIKeyFailureGuard(SideEffectsConfig{}, nil, nil, nil)
	effects := NewAccountAPIKeyEffects(SideEffectsConfig{}, guard, &weAPIKeyWriter{}, nil, nil, nil, nil)
	// 不触发任何定时路径的空操作调用。
	effects.RecordFailure(context.Background(), gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", Status: "active"}, RecordFailureInput{})

	fingerprint := "fp-9"
	source := "src-1"
	if got := accountAPIKeySuccessWriteKey(gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", SelectedAPIKeyFingerprint: &fingerprint}); got != "acc-1\x00fp-9" {
		t.Fatalf("key = %q", got)
	}
	if got := accountAPIKeySuccessWriteKey(gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1", CredentialSourceAccountID: &source, SelectedAPIKeyFingerprint: &fingerprint}); got != "src-1\x00fp-9" {
		t.Fatalf("key = %q", got)
	}
	if got := accountAPIKeySuccessWriteKey(gatewayruntimecache.OpenAIAccountSecret{ID: "acc-1"}); got != "acc-1\x00" {
		t.Fatalf("key = %q", got)
	}
}
