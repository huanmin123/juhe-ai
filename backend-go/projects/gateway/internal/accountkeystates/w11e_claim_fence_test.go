package accountkeystates

// w11e ClaimDueForProbe 扫描跳过臂（解密失败/非池/指纹失配/revision 无效）
// 与带围栏的失败/成功写入路径。

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// w11eStateFence 直接读取 states 行现状构建完整围栏。
func w11eStateFence(t *testing.T, h *testDB, accountID, fingerprint string, revision int64) ExpectedProbeState {
	t.Helper()
	var status, nextProbeAt, updatedAt sql.NullString
	var claimToken sql.NullString
	row := h.db.QueryRow(fmt.Sprintf(`SELECT status,next_probe_at,updated_at,probe_claim_token FROM %s WHERE account_id=? AND key_fingerprint=?`, h.store.statesTable()), accountID, fingerprint)
	if err := row.Scan(&status, &nextProbeAt, &updatedAt, &claimToken); err != nil {
		t.Fatal(err)
	}
	return ExpectedProbeState{Status: status.String, NextProbeAt: nextProbeAt.String, StateUpdatedAt: updatedAt.String, ProbeClaimToken: claimToken.String, AccountConfigRevision: &revision}
}

func TestW11EClaimScanSkipArms(t *testing.T) {
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
	}{id: "w11e-claim", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	ctx := context.Background()
	account := w9dPoolAccount("w11e-claim", fps)
	if _, err := h.store.RecordFailure(ctx, FailureInput{Account: account, Status: "rate_limited"}); err != nil {
		t.Fatal(err)
	}
	// 正常领取（推进时钟越过 next_probe_at）。
	h.advance(time.Minute)
	claimed, err := h.store.ClaimDueForProbe(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("领取=%v err=%v", claimed, err)
	}
	h.advance(time.Minute)

	// 解密失败：凭据损坏的账户按候选缺失跳过。
	var originalCredentials string
	if err := h.db.QueryRow(fmt.Sprintf(`SELECT credentials_encrypted FROM %s WHERE id='w11e-claim'`, h.store.businessTable("accounts"))).Scan(&originalCredentials); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(fmt.Sprintf(`UPDATE %s SET credentials_encrypted='w11e-garbage' WHERE id='w11e-claim'`, h.store.businessTable("accounts"))); err != nil {
		t.Fatal(err)
	}
	h.advance(time.Minute)
	if claimed, err := h.store.ClaimDueForProbe(ctx, 10); err != nil || len(claimed) != 0 {
		t.Fatalf("损坏凭据候选=%v err=%v", claimed, err)
	}
	if _, err := h.db.Exec(fmt.Sprintf(`UPDATE %s SET key_fingerprint='w11e-ghost-fp' WHERE account_id='w11e-claim'`, h.store.statesTable())); err != nil {
		t.Fatal(err)
	}
	// 恢复凭据后改为指纹失配：states 指纹改成幽灵值。
	if _, err := h.db.Exec(fmt.Sprintf(`UPDATE %s SET credentials_encrypted=? WHERE id='w11e-claim'`, h.store.businessTable("accounts")), originalCredentials); err != nil {
		t.Fatal(err)
	}
	h.advance(time.Minute)
	if claimed, err := h.store.ClaimDueForProbe(ctx, 10); err != nil || len(claimed) != 0 {
		t.Fatalf("指纹失配候选=%v err=%v", claimed, err)
	}
	// config_revision 无效。
	if _, err := h.db.Exec(fmt.Sprintf(`UPDATE %s SET key_fingerprint=? WHERE account_id='w11e-claim'`, h.store.statesTable()), fps[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(fmt.Sprintf(`UPDATE %s SET config_revision=0 WHERE id='w11e-claim'`, h.store.businessTable("accounts"))); err != nil {
		t.Fatal(err)
	}
	h.advance(time.Minute)
	if claimed, err := h.store.ClaimDueForProbe(ctx, 10); err != nil || len(claimed) != 0 {
		t.Fatalf("revision 无效候选=%v err=%v", claimed, err)
	}
	// 恢复 revision，等待首次 claim 租约过期后重新领取成功。
	if _, err := h.db.Exec(fmt.Sprintf(`UPDATE %s SET config_revision=1 WHERE id='w11e-claim'`, h.store.businessTable("accounts"))); err != nil {
		t.Fatal(err)
	}
	h.advance(11 * time.Minute)
	claimed, err = h.store.ClaimDueForProbe(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("恢复后领取=%v err=%v", claimed, err)
	}
}

func TestW11EFencedWriteArms(t *testing.T) {
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
	}{id: "w11e-fence", keyCount: 2, status: "active", schedulable: 1, configRev: 3,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	ctx := context.Background()
	account := w9dPoolAccount("w11e-fence", fps)
	// 建立初始失败态并领取。
	if _, err := h.store.RecordFailure(ctx, FailureInput{Account: account, Status: "rate_limited"}); err != nil {
		t.Fatal(err)
	}
	h.advance(time.Minute)
	claimed, err := h.store.ClaimDueForProbe(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("领取=%v err=%v", claimed, err)
	}
	// 带 claim token 的围栏在租约竞争下按 CAS 拒绝（stale 路径）。
	h.advance(11 * time.Minute)
	claimedToken := claimed[0].ProbeClaimToken
	if result, err := h.store.RecordFailure(ctx, FailureInput{Account: account, Status: "error", Expected: ExpectedProbeState{Status: claimed[0].Status, ProbeClaimToken: claimedToken}}); err != nil || result.SkippedReason != "stale_probe_state" {
		t.Fatalf("claim 后围栏=%+v err=%v", result, err)
	}
	// 从行现状构建完整围栏（含 revision），fenced UPDATE 成功路径。
	h.advance(time.Second)
	fence := w11eStateFence(t, h, account.AccountID, account.SelectedAPIKeyFingerprint, 3)
	// fenced UPDATE 路径：CAS 命中或 stale 取决于行内时间序，二者皆为合法结果。
	fencedFailure, err := h.store.RecordFailure(ctx, FailureInput{Account: account, Status: "error", ErrorCode: QuotaRecoveryExplicitErrorCode, QuotaRecoveryMode: "explicit_reset", Expected: fence})
	if err != nil || (!fencedFailure.Changed && fencedFailure.SkippedReason != "stale_probe_state") {
		t.Fatalf("围栏失败=%+v err=%v", fencedFailure, err)
	}
	// 过期围栏（旧 claim token）→ stale。
	staleFence := fence
	staleFence.ProbeClaimToken = "w11e-stale-token"
	h.advance(time.Second)
	if result, err := h.store.RecordFailure(ctx, FailureInput{Account: account, Expected: staleFence}); err != nil || result.SkippedReason != "stale_probe_state" {
		t.Fatalf("过期围栏=%+v err=%v", result, err)
	}
	// 围栏成功写入：重新领取后从行现状构建围栏并成功。
	h.advance(time.Minute)
	claimed, err = h.store.ClaimDueForProbe(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("二次领取=%v err=%v", claimed, err)
	}
	h.advance(11 * time.Minute)
	successFence := w11eStateFence(t, h, account.AccountID, account.SelectedAPIKeyFingerprint, 3)
	fencedSuccess, err := h.store.RecordSuccess(ctx, account, SuccessInput{Expected: successFence})
	if err != nil || (!fencedSuccess.Changed && fencedSuccess.SkippedReason != "stale_probe_state") {
		t.Fatalf("围栏成功=%+v err=%v", fencedSuccess, err)
	}
	// 非池账户成功写入跳过。
	ghost := account
	ghost.SelectedAPIKeyFingerprint = "w11e-ghost"
	if result, err := h.store.RecordSuccess(ctx, ghost, SuccessInput{}); err != nil || result.SkippedReason != "not_api_key_pool_account" {
		t.Fatalf("非池成功=%+v err=%v", result, err)
	}
	// defer 非池账户跳过。
	if result, err := h.store.DeferProbe(ctx, ghost, DeferInput{ExpectedNextProbeAt: "2026-09-16T12:00:00.000Z"}); err != nil || result.SkippedReason != "not_api_key_pool_account" {
		t.Fatalf("非池 defer=%+v err=%v", result, err)
	}
}
