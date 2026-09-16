package accountkeystates

// w11e RevalidatePool 理由码与 CAS 分支：not_found / not_supported /
// config_revision_conflict / no_revalidatable_key / 成功重置与租约保护。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW11ERevalidatePoolReasonArms(t *testing.T) {
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
	}{id: "w11e-rv", keyCount: 2, status: "active", schedulable: 1, configRev: 5,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	var singleFps []string
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
	}{id: "w11e-single", keyCount: 1, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &singleFps})
	var inactiveFps []string
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
	}{id: "w11e-inactive", keyCount: 2, status: "disabled", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &inactiveFps})
	ctx := context.Background()

	// 参数无效。
	if _, err := h.store.RevalidatePool(ctx, "  ", 1); err == nil || !strings.Contains(err.Error(), "参数无效") {
		t.Fatalf("空账户必须报错: %v", err)
	}
	if _, err := h.store.RevalidatePool(ctx, "w11e-rv", 0); err == nil || !strings.Contains(err.Error(), "参数无效") {
		t.Fatalf("非法 revision 必须报错: %v", err)
	}
	// 账户缺失。
	if result, err := h.store.RevalidatePool(ctx, "w11e-missing", 1); err != nil || result.Reason != ReasonAccountNotFound {
		t.Fatalf("缺账户=%+v err=%v", result, err)
	}
	// 非 active 账户。
	if result, err := h.store.RevalidatePool(ctx, "w11e-inactive", 1); err != nil || result.Reason != ReasonAccountNotActive {
		t.Fatalf("非 active=%+v err=%v", result, err)
	}
	// config revision 冲突。
	if result, err := h.store.RevalidatePool(ctx, "w11e-rv", 4); err != nil || result.Reason != ReasonConfigRevisionConflict {
		t.Fatalf("revision 冲突=%+v err=%v", result, err)
	}
	// 单 Key 账户不支持。
	if result, err := h.store.RevalidatePool(ctx, "w11e-single", 1); err != nil || result.Reason != ReasonNotSupported {
		t.Fatalf("单 Key=%+v err=%v", result, err)
	}
	// 全部 Key active → 无可重校验 Key。
	if result, err := h.store.RevalidatePool(ctx, "w11e-rv", 5); err != nil || result.Reason != ReasonNoRevalidatableKey {
		t.Fatalf("全 active=%+v err=%v", result, err)
	}
	// 一个 Key 进入 rate_limited → 重校验成功。
	first := w9dPoolAccount("w11e-rv", fps)
	if _, err := h.store.RecordFailure(ctx, FailureInput{Account: first, Status: "rate_limited"}); err != nil {
		t.Fatal(err)
	}
	result, err := h.store.RevalidatePool(ctx, "w11e-rv", 5)
	if err != nil || result.Changed != 1 || !result.Eligible {
		t.Fatalf("重校验=%+v err=%v", result, err)
	}
	// 重校验只把可探 Key 置为到期，状态仍为 rate_limited，可再次调度。
	if result, err := h.store.RevalidatePool(ctx, "w11e-rv", 5); err != nil || result.Changed != 1 || !result.Eligible {
		t.Fatalf("二次重校验=%+v err=%v", result, err)
	}
	// 成功写入后恢复 active，无 Key 可重校验。
	h.advance(time.Second)
	if _, err := h.store.RecordSuccess(ctx, first, SuccessInput{}); err != nil {
		t.Fatal(err)
	}
	if result, err := h.store.RevalidatePool(ctx, "w11e-rv", 5); err != nil || result.Reason != ReasonNoRevalidatableKey {
		t.Fatalf("恢复后重校验=%+v err=%v", result, err)
	}
	// error 状态 Key 手工恢复前不参与重校验。
	second := w9dPoolAccount("w11e-rv", fps)
	second.SelectedAPIKeyFingerprint = fps[1]
	second.SelectedAPIKeyIndex = 1
	h.advance(time.Second)
	if _, err := h.store.RecordFailure(ctx, FailureInput{Account: second, Status: "error"}); err != nil {
		t.Fatal(err)
	}
	// error → unverified（可重校验，execRevalidateUpdate 的 CASE 分支）。
	result, err = h.store.RevalidatePool(ctx, "w11e-rv", 5)
	if err != nil || result.Changed != 1 || !result.Eligible {
		t.Fatalf("error 重校验=%+v err=%v", result, err)
	}
	// 缓存失效通知被触发。
	if len(h.invalCalls) == 0 {
		t.Fatal("运行态缓存失效通知必须触发")
	}
}
