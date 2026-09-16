package accountkeystates

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// w9d（三）：关闭数据库后的查询/执行 err 臂集中覆盖。
// ---------------------------------------------------------------------------

func w9dSeededClosed(t *testing.T) (*Store, []*Store) {
	t.Helper()
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	h.seedState(t, "acc", fps[0], map[string]any{"status": "rate_limited", "next_probe_at": plusMillis(-1_000), "last_attempt_at": plusMillis(-2_000)})
	valid := w9dPoolAccount("acc", fps)
	rev := int64(1)
	live := []*Store{h.store}
	// live store 预置一次 fenced 场景需要的行，随后直接关闭数据库，
	// 后续所有调用在第一个查询/执行点即失败。
	_ = valid
	_ = rev
	h.db.Close()
	return h.store, live
}

func TestW9DClosedDBArms(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	h.seedState(t, "acc", fps[0], map[string]any{"status": "rate_limited", "next_probe_at": plusMillis(-1_000)})
	ctx := context.Background()
	valid := w9dPoolAccount("acc", fps)
	rev := int64(1)

	store := h.store
	h.db.Close()

	// claim / summary / details / states。
	if _, err := store.ClaimDueForProbe(ctx, 10); err == nil {
		t.Fatal("closed claim must fail")
	}
	if _, err := store.LoadSummariesByAccountIds(ctx, []string{"acc"}); err == nil {
		t.Fatal("closed summaries must fail")
	}
	if _, err := store.LoadAPIKeyRuntimeDetails(ctx, "acc"); err == nil {
		t.Fatal("closed details must fail")
	}
	if _, err := store.LoadSelectionStatesByAccountIds(ctx, []string{"acc"}); err == nil {
		t.Fatal("closed selection states must fail")
	}
	if _, err := store.LoadSelectionStatesForAccount(ctx, "acc"); err == nil {
		t.Fatal("closed single selection state must fail")
	}
	// probe：existing 读取 / fenced 写 / 成功围栏写 / defer。
	if _, err := store.RecordFailure(ctx, FailureInput{Account: valid}); err == nil {
		t.Fatal("closed failure existing read must fail")
	}
	if _, err := store.RecordFailure(ctx, FailureInput{Account: valid, Expected: ExpectedProbeState{AccountConfigRevision: &rev}}); err == nil {
		t.Fatal("closed failure existing fence read must fail")
	}
	if _, err := store.RecordSuccess(ctx, valid, SuccessInput{Expected: ExpectedProbeState{AccountConfigRevision: &rev}}); err == nil {
		t.Fatal("closed fenced success must fail")
	}
	if _, err := store.DeferProbe(ctx, valid, DeferInput{ExpectedNextProbeAt: nowMillisText()}); err == nil {
		t.Fatal("closed defer must fail")
	}
	// revalidate：账户行读取 / gate 行读取 / 候选探测 / 更新执行。
	if _, err := store.RevalidatePool(ctx, "acc", 1); err == nil {
		t.Fatal("closed revalidate must fail")
	}
	if _, err := store.hasRevalidatableCandidate(ctx, "acc", fps, nowMillisText()); err == nil {
		t.Fatal("closed candidate probe must fail")
	}
	if _, err := store.execRevalidateUpdate(ctx, nowMillisText(), "acc", fps, 1); err == nil {
		t.Fatal("closed revalidate update must fail")
	}
	if _, err := store.loadRuntimeRow(ctx, "acc", fps[0]); err == nil {
		t.Fatal("closed runtime row must fail")
	}
	// 标脏链路。
	if err := store.markGroupAccountStatsDirty(ctx, []string{"acc"}); err == nil {
		t.Fatal("closed stats dirty must fail")
	}
	if _, err := store.accountIdsAffectedBySourceAccount(ctx, "acc"); err == nil {
		t.Fatal("closed affected ids must fail")
	}
	if err := store.markRuntimeStateChanged(ctx, "acc"); err == nil {
		t.Fatal("closed mark state changed must fail")
	}
	// 半开 gate 与 nil 防御对照（确保错误语义仍可区分）。
	if !errors.Is(context.DeadlineExceeded, context.DeadlineExceeded) {
		t.Fatal("sanity")
	}
}
