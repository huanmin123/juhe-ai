// 波次 w14j：RedisStore 转换校验臂与纯工具函数收口（miniredis + 纯函数）。
package circuitstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func w14jMiniStore(t *testing.T) *RedisStore {
	t.Helper()
	server := miniredis.RunT(t)
	t.Cleanup(func() { server.Close() })
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store, err := NewRedisStore(RedisStoreOptions{
		Client: client, Namespace: "w14j-ns", Capacity: 4,
		ClosedRetentionMs: 1000, ReplayLimitPerScope: 8,
		Now: func() int64 { return time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC).UnixMilli() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestW14JConfirmationEvidenceValidationArms 覆盖 acquire/complete 转换的
// evidence key 归一化错误传播与 transport_failure 证据分支。
func TestW14JConfirmationEvidenceValidationArms(t *testing.T) {
	store := w14jMiniStore(t)
	ctx := context.Background()
	badKey := "not-sha256"
	scope := Scope{Kind: "account", AccountRuntimeKey: "w14j-rk", KeyFingerprint: "fp", ProtocolProfile: "openai", RequestLane: "default", ModelBucket: "gpt"}
	leaseID := "w14j-lease"
	if _, err := store.AcquireConfirmationLease(ctx, AcquireLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "1", TransitionID: "w14j-tr",
		LeaseID: leaseID, LeaseUntilMs: 9999, ExpectedFailureEvidenceKey: &badKey,
	}); err == nil {
		t.Fatal("acquire 非法 expectedFailureEvidenceKey 必须报错")
	}
	if _, err := store.AcquireConfirmationLease(ctx, AcquireLeaseInput{
		Scope: scope, Generation: 1, DispatchRevision: "1", TransitionID: "w14j-tr",
		LeaseID: leaseID, LeaseUntilMs: 9999, ConfirmationEvidenceKey: &badKey,
	}); err == nil {
		t.Fatal("acquire 非法 confirmationEvidenceKey 必须报错")
	}
	badOutcome := "unknown-x"
	if _, err := store.CompleteConfirmation(ctx, CompleteInput{
		Scope: scope, LeaseID: leaseID, Outcome: badOutcome,
	}); err == nil {
		t.Fatal("complete 非法 outcome 必须报错")
	}
	if _, err := store.CompleteConfirmation(ctx, CompleteInput{
		Scope: scope, LeaseID: leaseID, Outcome: "transport_failure", FailureEvidenceKey: &badKey,
	}); err == nil {
		t.Fatal("transport_failure 非法证据必须报错")
	}
	// 合法 transport_failure 带证据（Lua 脚本执行失败在无状态键时幂等可接受，
	// 这里只验证参数归一化路径不报参数错误）。
	if _, err := store.CompleteConfirmation(ctx, CompleteInput{
		Scope: scope, TransitionID: "w14j-tr", LeaseID: "w14j-missing-lease", Outcome: "framing_complete",
		Reason: nil, FramingCompleteDisposition: nil,
	}); err != nil {
		t.Fatalf("framing_complete 缺失租约应走 Redis 幂等路径: %v", err)
	}
	// ClearAccountEscalationEvidence 校验臂。
	if _, err := store.ClearAccountEscalationEvidence(ctx, "", "1", "e", nil); err == nil {
		t.Fatal("空 runtimeKey 必须报错")
	}
	if _, err := store.ClearAccountEscalationEvidence(ctx, "rk", " ", "e", nil); err == nil {
		t.Fatal("空 dispatchRevision 必须报错")
	}
	if _, err := store.ClearAccountEscalationEvidence(ctx, "rk", "1", "", nil); err == nil {
		t.Fatal("空 evidenceId 必须报错")
	}
}

// TestW14JStorePureHelpers 收口小工具函数的分支。
func TestW14JStorePureHelpers(t *testing.T) {
	fallback := "fallback"
	if got := pointerNowMs(map[string]any{"nowMs": int64(7)}); got == nil || *got != 7 {
		t.Fatalf("int64 nowMs: %v", got)
	}
	if got := pointerNowMs(map[string]any{"nowMs": float64(0)}); got != nil {
		t.Fatalf("float64 nowMs 不支持: %v", got)
	}
	if got := cursorString("value", fallback); got != "value" {
		t.Fatalf("string cursor: %s", got)
	}
	if got := cursorString("", fallback); got != fallback {
		t.Fatalf("空 string cursor 回退: %s", got)
	}
	if got := cursorString(float64(12), fallback); got != "12" {
		t.Fatalf("float cursor: %s", got)
	}
	if got := cursorString(json.Number("34"), fallback); got != "34" {
		t.Fatalf("json.Number cursor: %s", got)
	}
	if got := cursorString(nil, fallback); got != fallback {
		t.Fatalf("未知类型回退: %s", got)
	}
	if got := encodeJSON(map[string]string{"k": "v"}); got != `{"k":"v"}` {
		t.Fatalf("encodeJSON: %s", got)
	}
	if got := redisNamespacedKey("plain", "w14j-ns"); got != "juhe-ai:w14j-ns:plain" {
		t.Fatalf("无前缀键: %s", got)
	}
	if got := redisNamespacedKey("juhe-ai:other:plain", "w14j-ns"); got != "juhe-ai:w14j-ns:other:plain" {
		t.Fatalf("根前缀键: %s", got)
	}
	if got := redisNamespacedKey("juhe-ai:w14j-ns:already", "w14j-ns"); got != "juhe-ai:w14j-ns:already" {
		t.Fatalf("已带 namespace 前缀: %s", got)
	}
	// validateOperationPayload 关键分支。
	if err := validateOperationPayload("suspect", map[string]any{"transitionId": "t"}); err == nil {
		t.Fatal("suspect 缺 dispatchRevision 必须报错")
	}
	if err := validateOperationPayload("acquire_confirmation", map[string]any{
		"transitionId": "t", "leaseId": "l",
	}); err == nil {
		t.Fatal("acquire 缺 leaseUntilMs 必须报错")
	}
	if err := validateOperationPayload("acquire_confirmation", map[string]any{
		"transitionId": "t", "leaseId": "l", "nowMs": int64(10), "leaseUntilMs": int64(5),
	}); err == nil {
		t.Fatal("租约早于当前时间必须报错")
	}
	if err := validateOperationPayload("acquire_confirmation", map[string]any{
		"transitionId": "t", "leaseId": "l", "nowMs": int64(10), "leaseUntilMs": int64(20),
		"expectedFailureEvidenceKey": "bad",
	}); err == nil {
		t.Fatal("非法 expectedFailureEvidenceKey 必须报错")
	}
	if err := validateOperationPayload("complete_confirmation", map[string]any{
		"transitionId": "t", "leaseId": "l", "outcome": "bogus",
	}); err == nil {
		t.Fatal("非法 outcome 必须报错")
	}
	if err := validateOperationPayload("get", nil); err != nil {
		t.Fatalf("get 无需 transitionId: %v", err)
	}
	// payloadInt64。
	if _, ok := payloadInt64("nope"); ok {
		t.Fatal("字符串不是 int64")
	}
	if v, ok := payloadInt64(float64(9)); !ok || v != 9 {
		t.Fatalf("float64: %v %v", v, ok)
	}
	// accountCircuitDueAtMs 各阶段。
	if due := accountCircuitDueAtMs(State{Phase: "CLOSED"}); due == 0 {
		t.Fatal("CLOSED 应为 MaxInt64")
	}
	retry := int64(55)
	if due := accountCircuitDueAtMs(State{Phase: "SUSPECT", RetryAtMs: &retry}); due != 55 {
		t.Fatalf("SUSPECT retry: %d", due)
	}
	if due := accountCircuitDueAtMs(State{Phase: "OPEN"}); due == 0 {
		t.Fatal("OPEN 无 retry 应为 MaxInt64")
	}
	if due := accountCircuitDueAtMs(State{Phase: "OTHER"}); due == 0 {
		t.Fatal("未知阶段应为 MaxInt64")
	}
}
