package proberepo

import (
	"context"
	"testing"
)

// releaseClaimRow 断言 runtime 行的 claim 列当前值（token 空串表示 NULL）。
func releaseClaimRow(t *testing.T, h *testDB, accountID, fingerprint string) string {
	t.Helper()
	var token *string
	if err := h.db.QueryRow(h.store.bind(`SELECT probe_claim_token FROM `+h.store.table("account_api_key_runtime_states")+
		` WHERE account_id = ? AND key_fingerprint = ?`), accountID, fingerprint).Scan(&token); err != nil {
		t.Fatalf("read probe_claim_token: %v", err)
	}
	if token == nil {
		return ""
	}
	return *token
}

// TestReleaseProbeClaimReleasesOwnToken 锁定 T207 C6 的 compare-delete 语义：
// 携带本进程 claim token 才清空 claim token 与租约列（返回 true）；token 不
// 匹配（已被其他进程重新 claim）不命中（返回 false）且不改写既有 claim。
func TestReleaseProbeClaimReleasesOwnToken(t *testing.T) {
	h := openTestDBPostgres(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
	// 模拟本进程 claim 写入（与 claimProbeCandidates 同列）。
	h.exec(t, h.store.bind(`UPDATE `+h.store.table("account_api_key_runtime_states")+`
SET probe_claim_token = ?, probe_claimed_until = ?
WHERE account_id = ? AND key_fingerprint = ?`),
		"account_api_key_probe_claim_t207own", plusMillis(600_000), "acc-1", fp1)

	// token 不匹配：不命中，既有 claim 原样保留。
	released, err := h.store.ReleaseProbeClaim(context.Background(), "acc-1", fp1, "account_api_key_probe_claim_other")
	if err != nil || released {
		t.Fatalf("他人 token 不得命中: released=%t err=%v", released, err)
	}
	if got := releaseClaimRow(t, h, "acc-1", fp1); got != "account_api_key_probe_claim_t207own" {
		t.Fatalf("他人 token 不得改写既有 claim: %q", got)
	}

	// 本进程 token：命中并清空 claim token 与租约。
	released, err = h.store.ReleaseProbeClaim(context.Background(), "acc-1", fp1, "account_api_key_probe_claim_t207own")
	if err != nil || !released {
		t.Fatalf("本进程 token 必须命中: released=%t err=%v", released, err)
	}
	if got := releaseClaimRow(t, h, "acc-1", fp1); got != "" {
		t.Fatalf("命中后 probe_claim_token 必须为 NULL: %q", got)
	}
	var until *string
	if err := h.db.QueryRow(h.store.bind(`SELECT probe_claimed_until FROM `+h.store.table("account_api_key_runtime_states")+
		` WHERE account_id = ? AND key_fingerprint = ?`), "acc-1", fp1).Scan(&until); err != nil {
		t.Fatal(err)
	}
	if until != nil {
		t.Fatalf("命中后 probe_claimed_until 必须为 NULL: %q", *until)
	}
}

// TestReleaseProbeClaimValidation 锁定参数 guard：任一参数为空是调用方错误。
func TestReleaseProbeClaimValidation(t *testing.T) {
	h := openTestDBPostgres(t)
	for _, args := range [][3]string{
		{"", "fp", "token"},
		{"acc-1", "", "token"},
		{"acc-1", "fp", ""},
		{"  ", "fp", "token"},
	} {
		if _, err := h.store.ReleaseProbeClaim(context.Background(), args[0], args[1], args[2]); err == nil {
			t.Fatalf("空参数必须报错: %+v", args)
		}
	}
}
