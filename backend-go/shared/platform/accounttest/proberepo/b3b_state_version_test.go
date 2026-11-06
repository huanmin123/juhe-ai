package proberepo

// B3b：jobs 冷却复测写 Key 运行态（RecordKeySuccess/RecordKeyFailure）后必须按
// 来源 + 授权实例 bump 账户状态序号，gateway runtime 下一次读重载；
// DeferKeyProbe 不 bump，bump 失败不影响已提交的 Key 状态。

import (
	"context"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

type fakeAccountStateVersionBumper struct {
	ids []string
	err error
}

func (f *fakeAccountStateVersionBumper) BumpAccountState(_ context.Context, accountID string) error {
	f.ids = append(f.ids, accountID)
	return f.err
}

func b3bSeedInstance(t *testing.T, h *testDB, instanceID, sourceID string) {
	t.Helper()
	h.exec(t, h.store.bind(`
    INSERT INTO `+h.store.table("accounts")+` (
      id, system_account_id, name, type, status, schedulable, vendor_code, connection_driver_code,
      protocol_code, protocol_version, health_check_model, health_check_endpoint_mode,
      credentials_encrypted, dispatch_revision, authorization_instance_source_account_id, created_at, updated_at)
    VALUES (?, 'sys-1', '实例', 'api_key', 'active', 1, 'gpt', 'gpt', 'openai', 'v1', 'gpt-test', 'chat_json', ?, 3, ?, ?, ?)`),
		instanceID, h.sealCredentials(t, map[string]any{"api_keys": []any{"sk-inst"}}), sourceID, nowMillisText(), nowMillisText())
}

func TestB3BRecordKeySuccessBumpsSourceAndInstances(t *testing.T) {
	h := openTestDBPostgres(t)
	bumper := &fakeAccountStateVersionBumper{}
	h.store.stateVersion = bumper
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
	b3bSeedInstance(t, h, "acc-inst", "acc-1")

	result, err := h.store.RecordKeySuccess(context.Background(), accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Expected: w7cPoolFence(h, "acc-1", fp1),
	})
	if err != nil || !result.Changed {
		t.Fatalf("RecordKeySuccess = %+v err=%v", result, err)
	}
	if len(bumper.ids) != 2 || !containsID(bumper.ids, "acc-1") || !containsID(bumper.ids, "acc-inst") {
		t.Fatalf("bump ids = %v, want acc-1 + acc-inst", bumper.ids)
	}
}

func TestB3BRecordKeyFailureBumpsSourceAndInstances(t *testing.T) {
	h := openTestDBPostgres(t)
	bumper := &fakeAccountStateVersionBumper{}
	h.store.stateVersion = bumper
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "active", plusMillis(-1000))
	b3bSeedInstance(t, h, "acc-inst", "acc-1")

	result, err := h.store.RecordKeyFailure(context.Background(), accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Status: "temporary_unavailable", StatusCode: 500, ObservedAt: nowMillisText(),
		Expected: accountquality.KeyMutationExpected{Status: "active", StateUpdatedAt: plusMillis(0)},
	})
	if err != nil || !result.Changed {
		t.Fatalf("RecordKeyFailure = %+v err=%v", result, err)
	}
	if len(bumper.ids) != 2 || !containsID(bumper.ids, "acc-1") || !containsID(bumper.ids, "acc-inst") {
		t.Fatalf("bump ids = %v, want acc-1 + acc-inst", bumper.ids)
	}
}

func TestB3BNoStateChangeDoesNotBump(t *testing.T) {
	h := openTestDBPostgres(t)
	bumper := &fakeAccountStateVersionBumper{}
	h.store.stateVersion = bumper
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "disabled", plusMillis(-1000))

	result, err := h.store.RecordKeyFailure(context.Background(), accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Status: "temporary_unavailable", StatusCode: 500, ObservedAt: nowMillisText(),
	})
	if err != nil || result.Changed {
		t.Fatalf("disabled key mutation = %+v err=%v", result, err)
	}
	if len(bumper.ids) != 0 {
		t.Fatalf("changed=false bumped %v", bumper.ids)
	}
}

func TestB3BNewStoreWiresStateVersionPort(t *testing.T) {
	h := openTestDBPostgres(t)
	bumper := &fakeAccountStateVersionBumper{}
	store, err := NewStore(Config{DB: h.db, Secret: "test-secret", StateVersion: bumper})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if store.stateVersion != bumper {
		t.Fatal("Config.StateVersion 未装配到 Store")
	}
}

func TestB3BDeferKeyProbeNoBump(t *testing.T) {
	h := openTestDBPostgres(t)
	bumper := &fakeAccountStateVersionBumper{}
	h.store.stateVersion = bumper
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "active", plusMillis(-1000))

	if _, err := h.store.DeferKeyProbe(context.Background(), accountquality.KeyDeferInput{
		AccountID: "acc-1", KeyFingerprint: fp1,
		Expected: accountquality.KeyMutationExpected{NextProbeAt: plusMillis(-1000)},
	}); err != nil {
		t.Fatal(err)
	}
	if len(bumper.ids) != 0 {
		t.Fatalf("DeferKeyProbe bumped %v", bumper.ids)
	}
}

func TestB3BBumpFailureDoesNotFailMutation(t *testing.T) {
	h := openTestDBPostgres(t)
	h.store.stateVersion = &fakeAccountStateVersionBumper{err: errors.New("redis down")}
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))

	result, err := h.store.RecordKeySuccess(context.Background(), accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Expected: w7cPoolFence(h, "acc-1", fp1),
	})
	if err != nil || !result.Changed {
		t.Fatalf("bump failure must not fail mutation: %+v err=%v", result, err)
	}
}

func TestB3BPrecheckStatusBump(t *testing.T) {
	h := openTestDBPostgres(t)
	bumper := &fakeAccountStateVersionBumper{}
	h.store.stateVersion = bumper
	h.seedPoolAccount(t, "acc-precheck")
	b3bSeedInstance(t, h, "acc-precheck-inst", "acc-precheck")
	result, err := h.store.MarkPrecheckTemporaryUnavailable(context.Background(), accountquality.PrecheckMutationInput{
		AccountID: "acc-precheck", Reason: "probe failed",
		PrecheckStartedAt: nowMillisText(), ExpectedDispatchRevision: 3, ExpectedStatus: "active",
	})
	if err != nil || !result.Updated {
		t.Fatalf("precheck mutation = %+v err=%v", result, err)
	}
	if len(bumper.ids) != 2 || !containsID(bumper.ids, "acc-precheck") || !containsID(bumper.ids, "acc-precheck-inst") {
		t.Fatalf("precheck bump ids = %v, want source + instance", bumper.ids)
	}
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
