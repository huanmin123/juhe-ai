package gatewayaccounteffects

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// RedisAccountApiKeyTransientStateStore 补充分支（miniredis）
// ---------------------------------------------------------------------------

func TestWeTransientMutateLifecycleOnMiniredis(t *testing.T) {
	store, server := newTransientStoreForTest(t, 0)
	ctx := context.Background()
	target := AccountApiKeyTransientTarget{AccountID: "acc-1", KeyFingerprint: "fp-1"}
	input := TransientMutationInput{Target: target, Status: APIKeyStatusRateLimited, ExpectedGeneration: "gen-1"}

	// 无状态：missing_state，不产生写入。
	missing, err := store.RecordFailure(ctx, input)
	if err != nil || missing.Applied || missing.Reason != TransientReasonMissingState {
		t.Fatalf("missing = %+v err = %v", missing, err)
	}

	// 预置状态（generation gen-1，failure 计数 1，suppressUntil 未来）。
	until := time.Now().Add(time.Hour).UnixMilli()
	seed := AccountApiKeyTransientState{
		SchemaVersion: 1, AccountID: "acc-1", KeyFingerprint: "fp-1", Generation: "gen-1",
		LastObservedAtMs: time.Now().UnixMilli() - 1000, ObservationKind: "failure",
		FailureCount: 1, Status: APIKeyStatusRateLimited, SuppressUntilMs: &until,
	}
	seedJSON, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRawStateForTest(ctx, target, string(seedJSON)); err != nil {
		t.Fatalf("SetRawStateForTest = %v", err)
	}

	// generation 匹配的失败写入：applied，窗口内计数递增到 2。
	applied, err := store.RecordFailure(ctx, input)
	if err != nil || !applied.Applied || applied.Reason != TransientReasonApplied {
		t.Fatalf("applied = %+v err = %v", applied, err)
	}
	if applied.State == nil || applied.State.FailureCount != 2 || applied.State.Generation != "gen-1" {
		t.Fatalf("state = %+v", applied.State)
	}

	// generation 漂移：stale_generation 并回读当前状态。
	stale, err := store.RecordFailure(ctx, TransientMutationInput{Target: target, Status: APIKeyStatusError, ExpectedGeneration: "gen-other"})
	if err != nil || stale.Applied || stale.Reason != TransientReasonStaleGeneration {
		t.Fatalf("stale = %+v err = %v", stale, err)
	}
	if stale.State == nil || stale.State.Generation != "gen-1" {
		t.Fatalf("stale state = %+v", stale.State)
	}

	// 成功写入：generation 轮换，观察类型翻转为 success，清除抑制。
	success, err := store.RecordSuccess(ctx, TransientMutationInput{Target: target, ExpectedGeneration: "gen-1"})
	if err != nil || !success.Applied {
		t.Fatalf("success = %+v err = %v", success, err)
	}
	if success.State == nil || success.State.ObservationKind != "success" || success.State.Generation == "gen-1" {
		t.Fatalf("success state = %+v", success.State)
	}

	// 缺少 expectedGeneration：参数校验错误。
	if _, err := store.RecordFailure(ctx, TransientMutationInput{Target: target}); err == nil {
		t.Fatal("空 expectedGeneration 应报错")
	}
	if _, err := store.RecordSuccess(ctx, TransientMutationInput{Target: target}); err == nil {
		t.Fatal("空 expectedGeneration 应报错")
	}
	_ = server
}

func TestWeTransientMutateInvalidTargetAndURL(t *testing.T) {
	store, _ := newTransientStoreForTest(t, 0)
	ctx := context.Background()
	// 空 accountId/keyFingerprint 在连接前即拒绝。
	if _, err := store.RecordFailure(ctx, TransientMutationInput{Target: AccountApiKeyTransientTarget{KeyFingerprint: "fp"}}); err == nil {
		t.Fatal("空 accountId 应报错")
	}
	if _, err := store.RecordSuccess(ctx, TransientMutationInput{Target: AccountApiKeyTransientTarget{AccountID: "a"}}); err == nil {
		t.Fatal("空 keyFingerprint 应报错")
	}

	// 非法 Redis URL：clientForUse 初始化失败向上传播。
	badStore, err := NewRedisAccountApiKeyTransientStateStore(RedisAccountApiKeyTransientStateStoreOptions{
		RedisURL: "not-a-url", Namespace: "we-ns", AllowUnsafeShortStateTtlForTest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = badStore.Close() })
	if _, err := badStore.RecordFailure(ctx, TransientMutationInput{
		Target: AccountApiKeyTransientTarget{AccountID: "a", KeyFingerprint: "f"}, ExpectedGeneration: "g",
	}); err == nil {
		t.Fatal("非法 URL 应报错")
	}
}

func TestWeTransientLoadManyOnMiniredis(t *testing.T) {
	store, _ := newTransientStoreForTest(t, 0)
	ctx := context.Background()

	// 全部缺失：为每个指纹创建默认 success 状态（generation 全新）。
	states, err := store.LoadMany(ctx, "acc-1", []string{"fp-1", "fp-2", "fp-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("states = %d, want 2（指纹去重）", len(states))
	}
	for _, entry := range states {
		if entry.State == nil || entry.State.ObservationKind != "success" || entry.Suppressed {
			t.Fatalf("默认状态 = %+v", entry)
		}
		if entry.State.AccountID != "acc-1" {
			t.Fatalf("accountId = %s", entry.State.AccountID)
		}
	}

	// 空 accountId / 全空指纹：参数校验。
	if _, err := store.LoadMany(ctx, " ", []string{"fp-1"}); err == nil {
		t.Fatal("空 accountId 应报错")
	}
	if empty, err := store.LoadMany(ctx, "acc-1", []string{"", " "}); err != nil || len(empty) != 0 {
		t.Fatalf("空指纹应返回空: %+v err = %v", empty, err)
	}

	// 抑制中的状态：LoadMany 投影 suppressed=true。
	target := AccountApiKeyTransientTarget{AccountID: "acc-1", KeyFingerprint: "fp-9"}
	until := time.Now().Add(time.Hour).UnixMilli()
	seed := AccountApiKeyTransientState{
		SchemaVersion: 1, AccountID: "acc-1", KeyFingerprint: "fp-9", Generation: "gen-9",
		LastObservedAtMs: time.Now().UnixMilli(), ObservationKind: "failure",
		FailureCount: 1, Status: APIKeyStatusError, SuppressUntilMs: &until,
	}
	seedJSON, _ := json.Marshal(seed)
	if err := store.SetRawStateForTest(ctx, target, string(seedJSON)); err != nil {
		t.Fatal(err)
	}
	states, err = store.LoadMany(ctx, "acc-1", []string{"fp-9"})
	if err != nil || len(states) != 1 {
		t.Fatalf("states = %+v err = %v", states, err)
	}
	if !states[0].Suppressed || states[0].State.Status != APIKeyStatusError {
		t.Fatalf("suppressed 状态 = %+v", states[0])
	}
}

func TestWeTransientDeleteManyForTest(t *testing.T) {
	store, _ := newTransientStoreForTest(t, 0)
	ctx := context.Background()
	target := AccountApiKeyTransientTarget{AccountID: "acc-1", KeyFingerprint: "fp-1"}

	if err := store.SetRawStateForTest(ctx, target, `{"schemaVersion":1}`); err != nil {
		t.Fatalf("SetRawStateForTest = %v", err)
	}
	// DeleteManyForTest 移除原始 key 后 LoadMany 重建默认状态。
	if err := store.DeleteManyForTest(ctx, []AccountApiKeyTransientTarget{target}); err != nil {
		t.Fatalf("DeleteManyForTest = %v", err)
	}
	states, err := store.LoadMany(ctx, "acc-1", []string{"fp-1"})
	if err != nil || len(states) != 1 || states[0].State.ObservationKind != "success" {
		t.Fatalf("删除后应重建默认状态: %+v err = %v", states, err)
	}

	// 空列表 no-op；非法目标报错。
	if err := store.DeleteManyForTest(ctx, nil); err != nil {
		t.Fatalf("空列表 = %v", err)
	}
	if err := store.DeleteManyForTest(ctx, []AccountApiKeyTransientTarget{{KeyFingerprint: "fp"}}); err == nil {
		t.Fatal("空 accountId 应报错")
	}
	if err := store.SetRawStateForTest(ctx, AccountApiKeyTransientTarget{AccountID: "a"}, "x"); err == nil {
		t.Fatal("SetRawStateForTest 空 fingerprint 应报错")
	}
}

func TestWeTransientNormalizeTarget(t *testing.T) {
	index := 2
	normalized, err := normalizeTransientTarget(AccountApiKeyTransientTarget{AccountID: " a ", KeyFingerprint: " f ", KeyIndex: &index})
	if err != nil || normalized.AccountID != "a" || normalized.KeyFingerprint != "f" || normalized.KeyIndex == nil || *normalized.KeyIndex != 2 {
		t.Fatalf("normalized = %+v err = %v", normalized, err)
	}
	negative := -1
	if _, err := normalizeTransientTarget(AccountApiKeyTransientTarget{AccountID: "a", KeyFingerprint: "f", KeyIndex: &negative}); err == nil {
		t.Fatal("负 keyIndex 应报错")
	}
	if _, err := normalizeTransientTarget(AccountApiKeyTransientTarget{AccountID: " ", KeyFingerprint: "f"}); err == nil {
		t.Fatal("空 accountId 应报错")
	}
	if _, err := normalizeTransientTarget(AccountApiKeyTransientTarget{AccountID: "a", KeyFingerprint: ""}); err == nil {
		t.Fatal("空 keyFingerprint 应报错")
	}
}

func TestWeTransientParseStateValueShapes(t *testing.T) {
	validBase := AccountApiKeyTransientState{
		SchemaVersion: 1, AccountID: "a", KeyFingerprint: "f", Generation: "g",
		LastObservedAtMs: 100, ObservationKind: "success", FailureCount: 0,
	}
	if _, err := parseTransientStateValue(validBase); err != nil {
		t.Fatalf("合法 success 状态不应报错: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*AccountApiKeyTransientState)
	}{
		{name: "schemaVersion 错误", mutate: func(s *AccountApiKeyTransientState) { s.SchemaVersion = 2 }},
		{name: "缺 accountId", mutate: func(s *AccountApiKeyTransientState) { s.AccountID = "" }},
		{name: "缺 keyFingerprint", mutate: func(s *AccountApiKeyTransientState) { s.KeyFingerprint = "" }},
		{name: "缺 generation", mutate: func(s *AccountApiKeyTransientState) { s.Generation = "" }},
		{name: "lastObservedAtMs 为负", mutate: func(s *AccountApiKeyTransientState) { s.LastObservedAtMs = -1 }},
		{name: "lastObservedAtMs 越界", mutate: func(s *AccountApiKeyTransientState) { s.LastObservedAtMs = safeIntegerMax + 1 }},
		{name: "observationKind 未知", mutate: func(s *AccountApiKeyTransientState) { s.ObservationKind = "other" }},
		{name: "failureCount 为负", mutate: func(s *AccountApiKeyTransientState) { s.FailureCount = -1 }},
		{name: "keyIndex 为负", mutate: func(s *AccountApiKeyTransientState) { negative := -1; s.KeyIndex = &negative }},
		{
			name:   "failure 缺合法 status",
			mutate: func(s *AccountApiKeyTransientState) { s.ObservationKind = "failure" },
		},
		{
			name: "failure 缺 suppressUntilMs",
			mutate: func(s *AccountApiKeyTransientState) {
				s.ObservationKind = "failure"
				s.Status = APIKeyStatusRateLimited
			},
		},
		{
			name: "failure suppressUntilMs 为负",
			mutate: func(s *AccountApiKeyTransientState) {
				s.ObservationKind = "failure"
				s.Status = APIKeyStatusRateLimited
				negative := int64(-5)
				s.SuppressUntilMs = &negative
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validBase
			tt.mutate(&candidate)
			if _, err := parseTransientStateValue(candidate); err == nil {
				t.Fatal("非法状态应报错")
			}
		})
	}

	// 合法 failure 状态。
	failure := validBase
	failure.ObservationKind = "failure"
	failure.Status = APIKeyStatusTemporaryUnavailable
	until := int64(123)
	failure.SuppressUntilMs = &until
	if _, err := parseTransientStateValue(failure); err != nil {
		t.Fatalf("合法 failure 状态不应报错: %v", err)
	}
}

func TestWeTransientMiscHelpers(t *testing.T) {
	if orDefaultString("", "fallback") != "fallback" {
		t.Fatal("空值应回落 fallback")
	}
	if orDefaultString("value", "fallback") != "value" {
		t.Fatal("非空值应原样返回")
	}
	if mustJSON(map[string]int{"k": 1}) != `{"k":1}` {
		t.Fatalf("mustJSON = %s", mustJSON(map[string]int{"k": 1}))
	}
	if sanitizeRedisKeyPart("  bad name!  ") != "bad_name" {
		t.Fatalf("sanitize = %s", sanitizeRedisKeyPart("  bad name!  "))
	}
	if sanitizeRedisKeyPart("!!!") != "default" {
		t.Fatalf("全非法字符应回落 default: %s", sanitizeRedisKeyPart("!!!"))
	}
	if got := uniqueNonEmpty([]string{" a ", "a", "", "b", " "}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("uniqueNonEmpty = %v", got)
	}
	if _, err := requireText("  ", "字段"); err == nil || !strings.Contains(err.Error(), "字段") {
		t.Fatalf("requireText 错误 = %v", err)
	}
	// 构造器补充分支：suppressionDelayMs / failureCounterWindowMs 非法。
	_, err := NewRedisAccountApiKeyTransientStateStore(RedisAccountApiKeyTransientStateStoreOptions{
		RedisURL: "redis://localhost:6379/0", Namespace: "ns",
		SuppressionDelayMs: []int64{1000, 0}, AllowUnsafeShortStateTtlForTest: true,
	})
	if err == nil || !strings.Contains(err.Error(), "suppressionDelayMs[1]") {
		t.Fatalf("非正 suppressionDelay 应报错: %v", err)
	}
	_, err = NewRedisAccountApiKeyTransientStateStore(RedisAccountApiKeyTransientStateStoreOptions{
		RedisURL: "redis://localhost:6379/0", Namespace: "ns", FailureCounterWindowMs: -1,
		AllowUnsafeShortStateTtlForTest: true,
	})
	if err == nil || !strings.Contains(err.Error(), "failureCounterWindowMs") {
		t.Fatalf("非正 failureCounterWindowMs 应报错: %v", err)
	}
	_, err = NewRedisAccountApiKeyTransientStateStore(RedisAccountApiKeyTransientStateStoreOptions{
		RedisURL: "redis://localhost:6379/0", Namespace: "ns", StateTtlMs: -5,
		AllowUnsafeShortStateTtlForTest: true,
	})
	if err == nil || !strings.Contains(err.Error(), "stateTtlMs") {
		t.Fatalf("负 stateTtlMs 应报错: %v", err)
	}
	_, err = NewRedisAccountApiKeyTransientStateStore(RedisAccountApiKeyTransientStateStoreOptions{
		RedisURL: "redis://localhost:6379/0", Namespace: "ns", StateTtlMs: 100,
	})
	if err == nil || !strings.Contains(err.Error(), "不得少于") {
		t.Fatalf("过短 stateTtlMs 应报错: %v", err)
	}
	_, err = NewRedisAccountApiKeyTransientStateStore(RedisAccountApiKeyTransientStateStoreOptions{
		RedisURL: "redis://localhost:6379/0", Namespace: "ns", StateTtlMs: 100,
		AllowUnsafeShortStateTtlForTest: true, SuppressionDelayMs: []int64{200},
	})
	if err == nil || !strings.Contains(err.Error(), "不得短于最大 suppression delay") {
		t.Fatalf("stateTtlMs 短于最大延迟应报错: %v", err)
	}
}
