package proberepo

// 账号#F8 缺口回归：jobs 探针族 RecordKeySuccess/RecordKeyFailure 翻转 key
// 运行态（changed>0）后必须标脏 group_account_stats_dirty（对齐 Node
// markRuntimeStateChanged 与 gateway accountkeystates finishMutation 语义）；
// DeferKeyProbe 与 changed=false 路径不标脏（Node 同款）。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

func (h *testDB) dirtyRows(t *testing.T) map[string][2]string {
	t.Helper()
	rows, err := h.db.Query(`SELECT group_id, reason, updated_at FROM group_account_stats_dirty ORDER BY group_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][2]string{}
	for rows.Next() {
		var groupID, reason, updatedAt string
		if err := rows.Scan(&groupID, &reason, &updatedAt); err != nil {
			t.Fatal(err)
		}
		out[groupID] = [2]string{reason, updatedAt}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestF8RecordKeySuccessMarksStatsDirty(t *testing.T) {
	ctx := context.Background()

	// fence UPDATE 分支：翻转 temporary_unavailable → active，标脏账户所在分组。
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
	result, err := h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Expected: w7cPoolFence(h, "acc-1", fp1),
	})
	if err != nil || !result.Changed {
		t.Fatalf("fence success = %+v err=%v", result, err)
	}
	rows := h.dirtyRows(t)
	if len(rows) != 1 {
		t.Fatalf("脏行数 = %d (%v)", len(rows), rows)
	}
	entry, ok := rows["group-1"]
	if !ok || entry[0] != "account_api_key_runtime" || entry[1] != nowMillisText() {
		t.Fatalf("group-1 脏行 = %v", entry)
	}

	// INSERT 分支：新指纹成功，同样标脏。
	h2 := openTestDB(t)
	keyA, _ := h2.seedPoolAccount(t, "acc-ins")
	h2.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acc-ins'`,
		h2.sealCredentials(t, map[string]any{"base_url": "https://upstream.example.com/v1", "api_keys": []any{keyA, "sk-ins-new"}}))
	result, err = h2.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-ins", KeyFingerprint: h2.store.FingerprintAPIKey("sk-ins-new"),
		TrafficSource: "cooldown_retest", ObservedAt: nowMillisText(),
	})
	if err != nil || !result.Changed {
		t.Fatalf("insert success = %+v err=%v", result, err)
	}
	if rows := h2.dirtyRows(t); len(rows) != 1 {
		t.Fatalf("INSERT 分支脏行 = %v", rows)
	}
}

func TestF8RecordKeyFailureMarksStatsDirty(t *testing.T) {
	ctx := context.Background()

	// UPDATE 分支：active → temporary_unavailable。
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "active", plusMillis(-1000))
	result, err := h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Status: "temporary_unavailable", StatusCode: 500, ObservedAt: nowMillisText(),
		Expected: accountquality.KeyMutationExpected{Status: "active", StateUpdatedAt: plusMillis(0)},
	})
	if err != nil || !result.Changed {
		t.Fatalf("update failure = %+v err=%v", result, err)
	}
	rows := h.dirtyRows(t)
	if entry, ok := rows["group-1"]; !ok || entry[0] != "account_api_key_runtime" || entry[1] != nowMillisText() {
		t.Fatalf("group-1 脏行 = %v present=%v", entry, ok)
	}

	// INSERT 分支：新 key 首次失败同样标脏。
	h2 := openTestDB(t)
	keyB, _ := h2.seedPoolAccount(t, "acc-ins")
	h2.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acc-ins'`,
		h2.sealCredentials(t, map[string]any{"base_url": "https://upstream.example.com/v1", "api_keys": []any{keyB, "sk-fail-new"}}))
	result, err = h2.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-ins", KeyFingerprint: h2.store.FingerprintAPIKey("sk-fail-new"),
		TrafficSource: "cooldown_retest", StatusCode: 503, ObservedAt: nowMillisText(),
	})
	if err != nil || !result.Changed {
		t.Fatalf("insert failure = %+v err=%v", result, err)
	}
	if rows := h2.dirtyRows(t); len(rows) != 1 {
		t.Fatalf("INSERT 分支脏行 = %v", rows)
	}
}

func TestF8NoDirtyWithoutStateChange(t *testing.T) {
	ctx := context.Background()
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "disabled", plusMillis(-1000))

	// disabled key 拒绝失败写入：changed=false 不标脏。
	if result, err := h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Status: "temporary_unavailable", StatusCode: 500, ObservedAt: nowMillisText(),
		Expected: w7cPoolFence(h, "acc-1", fp1),
	}); err != nil || result.Changed {
		t.Fatalf("disabled key = %+v err=%v", result, err)
	}
	// stale fence：changed=false 不标脏。
	if result, err := h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Expected: w7cPoolFence(h, "acc-1", fp1),
	}); err != nil || result.Changed {
		t.Fatalf("stale fence = %+v err=%v", result, err)
	}
	// DeferKeyProbe 只推 next_probe_at，不标脏（Node defer 路径同款）。
	if result, err := h.store.DeferKeyProbe(ctx, accountquality.KeyDeferInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		DelaySeconds: 120, ObservedAt: nowMillisText(),
		Expected: accountquality.KeyMutationExpected{
			Status: "disabled", NextProbeAt: plusMillis(-1000), StateUpdatedAt: plusMillis(0),
		},
	}); err != nil || !result.Changed {
		t.Fatalf("defer = %+v err=%v", result, err)
	}
	if rows := h.dirtyRows(t); len(rows) != 0 {
		t.Fatalf("changed=false / defer 不得标脏 = %v", rows)
	}
}

// TestF8DirtyCoversAuthorizationInstanceFamily 验证受影响账户扩展方向与 Node
// accountIdsAffectedBySourceAccount 一致：源账户翻转覆盖源 + 全部授权实例的
// 分组；实例翻转只覆盖实例自身分组（不向上扩展到源）。
func TestF8DirtyCoversAuthorizationInstanceFamily(t *testing.T) {
	ctx := context.Background()
	h := openTestDB(t)
	_, _ = h.seedPoolAccount(t, "acc-src")
	h.exec(t, `
    INSERT INTO accounts (id, system_account_id, name, type, status, credentials_encrypted,
      authorization_instance_source_account_id)
    VALUES ('acc-inst', 'sys-1', '实例', 'api_key', 'active', ?, 'acc-src')
  `, h.sealCredentials(t, map[string]any{"base_url": "https://upstream.example.com/v1", "api_keys": []any{"sk-i1", "sk-i2"}}))
	h.exec(t, `INSERT INTO account_supported_models (account_id, model) VALUES ('acc-inst', 'gpt-test')`)
	h.exec(t, `INSERT INTO groups (id, system_account_id, provider_code, enabled) VALUES ('group-2', 'sys-1', 'openai', 1)`)
	h.exec(t, `INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled) VALUES ('group-2', 'sys-1', 'acc-inst', 1)`)

	// 源账户翻转：group-1（源）+ group-2（实例）都标脏。
	srcFp := h.store.FingerprintAPIKey("sk-key-one")
	h.seedRuntimeState(t, "acc-src", srcFp, 0, "temporary_unavailable", plusMillis(-1000))
	result, err := h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-src", KeyFingerprint: srcFp, TrafficSource: "cooldown_retest",
		Expected: accountquality.KeyMutationExpected{
			Status: "temporary_unavailable", NextProbeAt: plusMillis(-1000), StateUpdatedAt: plusMillis(0),
		},
	})
	if err != nil || !result.Changed {
		t.Fatalf("source success = %+v err=%v", result, err)
	}
	rows := h.dirtyRows(t)
	if len(rows) != 2 {
		t.Fatalf("源+实例分组都须标脏 = %v", rows)
	}
	for _, groupID := range []string{"group-1", "group-2"} {
		if entry, ok := rows[groupID]; !ok || entry[0] != "account_api_key_runtime" {
			t.Fatalf("%s 脏行缺失或 reason 错 = %v", groupID, entry)
		}
	}

	// 实例翻转：仅实例分组标脏（受影响账户查询以实例为锚无向上扩展，Node 同款）。
	h2 := openTestDB(t)
	_, _ = h2.seedPoolAccount(t, "acc-src")
	h2.exec(t, `
    INSERT INTO accounts (id, system_account_id, name, type, status, credentials_encrypted,
      authorization_instance_source_account_id)
    VALUES ('acc-inst', 'sys-1', '实例', 'api_key', 'active', ?, 'acc-src')
  `, h2.sealCredentials(t, map[string]any{"base_url": "https://upstream.example.com/v1", "api_keys": []any{"sk-i1", "sk-i2"}}))
	h2.exec(t, `INSERT INTO account_supported_models (account_id, model) VALUES ('acc-inst', 'gpt-test')`)
	h2.exec(t, `INSERT INTO groups (id, system_account_id, provider_code, enabled) VALUES ('group-2', 'sys-1', 'openai', 1)`)
	h2.exec(t, `INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled) VALUES ('group-2', 'sys-1', 'acc-inst', 1)`)
	instFp := h2.store.FingerprintAPIKey("sk-i1")
	h2.seedRuntimeState(t, "acc-inst", instFp, 0, "temporary_unavailable", plusMillis(-1000))
	result, err = h2.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-inst", KeyFingerprint: instFp, TrafficSource: "cooldown_retest",
		Expected: accountquality.KeyMutationExpected{
			Status: "temporary_unavailable", NextProbeAt: plusMillis(-1000), StateUpdatedAt: plusMillis(0),
		},
	})
	if err != nil || !result.Changed {
		t.Fatalf("instance success = %+v err=%v", result, err)
	}
	rows = h2.dirtyRows(t)
	if len(rows) != 1 {
		t.Fatalf("实例翻转只标实例分组 = %v", rows)
	}
	if _, ok := rows["group-2"]; !ok {
		t.Fatalf("group-2 缺失 = %v", rows)
	}
}
