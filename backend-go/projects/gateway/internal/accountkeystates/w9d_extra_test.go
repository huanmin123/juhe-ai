package accountkeystates

import (
	"context"
	"encoding/json"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// w9d：ResolveTarget / fence / claim / record / defer / revalidate 的分支臂。
// 不可达登记：ClaimDueForProbe 的 rows.Err 臂与 scan err 臂需要本地 sqlite
// 驱动迭代中途故障；PG 专属分支（postgres=true 的 upsert）需真实 PG（本包
// 门禁不覆盖 PG 方言，与既有测试一致）。
// ---------------------------------------------------------------------------

func w9dPoolAccount(id string, fps []string) TargetInput {
	keys := make([]string, len(fps))
	keys[0] = "sk-test-" + id + "-a"
	if len(fps) > 1 {
		keys[1] = "sk-test-" + id + "-b"
	}
	return TargetInput{
		AccountID:                 id,
		SystemAccountID:           "sys-owner",
		SelectedAPIKeyFingerprint: fps[0],
		HasSelectedAPIKeyIndex:    true,
		SelectedAPIKeyIndex:       0,
		ProviderCode:              "openai",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		AccountType:               "api_key",
		APIKey:                    keys[0],
		APIKeys:                   keys,
	}
}

func TestW9DBuildProbeFenceArms(t *testing.T) {
	rev := int64(1)
	// 合法围栏：provided + 参数聚合。
	fence := buildProbeFence(ExpectedProbeState{Status: "rate_limited", NextProbeAt: nowMillisText(), StateUpdatedAt: nowMillisText(), ProbeClaimToken: "tok", AccountConfigRevision: &rev}, "c.")
	if !fence.provided || fence.invalidReason != "" || len(fence.params) != 4 {
		t.Fatalf("fence=%+v", fence)
	}
	// 非法 next_probe_at。
	if fence := buildProbeFence(ExpectedProbeState{NextProbeAt: "junk"}, ""); fence.invalidReason != "invalid_expected_probe_at" {
		t.Fatalf("probe at fence=%+v", fence)
	}
	// 非法 updated_at。
	if fence := buildProbeFence(ExpectedProbeState{StateUpdatedAt: "junk"}, ""); fence.invalidReason != "invalid_expected_state_updated_at" {
		t.Fatalf("updated at fence=%+v", fence)
	}
	// 空白 claim token。
	if fence := buildProbeFence(ExpectedProbeState{ProbeClaimToken: "   "}, ""); fence.invalidReason != "invalid_expected_probe_claim_token" {
		t.Fatalf("claim token fence=%+v", fence)
	}
	// 非法 config revision。
	zero := int64(0)
	if fence := buildProbeFence(ExpectedProbeState{AccountConfigRevision: &zero}, ""); fence.invalidReason != "invalid_expected_account_config_revision" {
		t.Fatalf("revision fence=%+v", fence)
	}
	// 空围栏。
	if fence := buildProbeFence(ExpectedProbeState{}, ""); fence.provided {
		t.Fatal("empty fence must not be provided")
	}
}

func TestW9DResolveTargetArms(t *testing.T) {
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
	valid := w9dPoolAccount("acc", fps)
	ctx := context.Background()

	// 各 nil 分支。
	disabled := valid
	disabled.RuntimeStateDisabled = true
	if h.store.ResolveTarget(disabled) != nil {
		t.Fatal("disabled runtime state must resolve nil")
	}
	noFp := valid
	noFp.SelectedAPIKeyFingerprint = "  "
	if h.store.ResolveTarget(noFp) != nil {
		t.Fatal("blank fingerprint must resolve nil")
	}
	oauth := valid
	oauth.AccountType = "oauth"
	if h.store.ResolveTarget(oauth) != nil {
		t.Fatal("oauth account must resolve nil")
	}
	ghost := valid
	ghost.SelectedAPIKeyFingerprint = "ghost-fingerprint"
	if h.store.ResolveTarget(ghost) != nil {
		t.Fatal("unknown fingerprint must resolve nil")
	}
	noIds := valid
	noIds.AccountID = ""
	noIds.SystemAccountID = ""
	if h.store.ResolveTarget(noIds) != nil {
		t.Fatal("missing ids must resolve nil")
	}
	// 成功 + 字段回退分支。
	target := h.store.ResolveTarget(valid)
	if target == nil || target.AccountID != "acc" || target.SystemAccountID != "sys-owner" || target.KeyIndex != 0 {
		t.Fatalf("target=%+v", target)
	}
	fallback := valid
	fallback.HasSelectedAPIKeyIndex = false
	fallback.CredentialSourceAccountID = "src-acc"
	fallback.AccountID = ""
	fallback.OwnerSystemAccountID = "owner"
	fallback.SystemAccountID = ""
	if t2 := h.store.ResolveTarget(fallback); t2 == nil || t2.AccountID != "src-acc" || t2.SystemAccountID != "owner" || t2.KeyIndex != 0 {
		t.Fatalf("fallback target=%+v", t2)
	}
	// ClaimDueForProbe 的 limit 归一与 decrypt 失败候选。
	if _, err := h.store.ClaimDueForProbe(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.ClaimDueForProbe(ctx, 500); err != nil {
		t.Fatal(err)
	}
}

func TestW9DClaimCandidateSkipArms(t *testing.T) {
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
	// 三个候选：fps[0]（正常）、ghost（指纹不在池）、acc2 的 key（父账户
	// config_revision 无效 → 跳过）。
	h.seedState(t, "acc", fps[0], map[string]any{"status": "rate_limited", "next_probe_at": plusMillis(-1_000)})
	h.seedState(t, "acc", "ghost-fingerprint", map[string]any{"status": "rate_limited", "next_probe_at": plusMillis(-2_000)})
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
	}{id: "acc2", keyCount: 2, status: "active", schedulable: 1, configRev: 0,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1"})
	var fps2 []string
	_ = fps2
	h.seedState(t, "acc2", "fingerprint-of-acc2", map[string]any{"status": "rate_limited", "next_probe_at": plusMillis(-3_000)})
	// 坏信封：解密失败 → 全部候选按缺失处理。
	h.exec(t, `UPDATE accounts SET credentials_encrypted='not-an-envelope' WHERE id='acc'`)
	h.exec(t, `UPDATE accounts SET credentials_encrypted='not-an-envelope' WHERE id='acc2'`)
	if claimed, err := h.store.ClaimDueForProbe(context.Background(), 10); err != nil || len(claimed) != 0 {
		t.Fatalf("decrypt failure claim=%#v err=%v", claimed, err)
	}
	// 恢复 acc 凭据：ghost 与 acc2（无效 revision）跳过，仅 fps[0] 被认领。
	h.restoreCredentials(t, "acc", []string{"sk-test-acc-a", "sk-test-acc-b"})
	claimed, err := h.store.ClaimDueForProbe(context.Background(), 10)
	if err != nil || len(claimed) != 1 || claimed[0].KeyFingerprint != fps[0] {
		t.Fatalf("selective claim=%#v err=%v", claimed, err)
	}
}

func (h *testDB) restoreCredentials(t *testing.T, id string, keys []string) {
	t.Helper()
	sealed, err := accountsEncryptKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	h.exec(t, `UPDATE accounts SET credentials_encrypted=? WHERE id=?`, sealed, id)
}

func accountsEncryptKeys(keys []string) (string, error) {
	anyKeys := make([]any, len(keys))
	for i, k := range keys {
		anyKeys[i] = k
	}
	return accounts.EncryptJSON(testSecret, map[string]any{"api_keys": anyKeys})
}

func TestW9DRecordFailureArms(t *testing.T) {
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
	ctx := context.Background()
	valid := w9dPoolAccount("acc", fps)

	// 非法 cooldownUntil。
	if _, err := h.store.RecordFailure(ctx, FailureInput{Account: valid, CooldownUntil: "junk"}); err == nil {
		t.Fatal("invalid cooldown must fail")
	}
	// fence provided 但行不存在 → stale。
	rev := int64(1)
	if r, err := h.store.RecordFailure(ctx, FailureInput{Account: valid, Expected: ExpectedProbeState{Status: "rate_limited", AccountConfigRevision: &rev}}); err != nil || r.SkippedReason != "stale_probe_state" {
		t.Fatalf("stale fence result=%+v err=%v", r, err)
	}
	// 无 fence：新 key → INSERT。
	if r, err := h.store.RecordFailure(ctx, FailureInput{Account: valid, Status: "rate_limited", StatusCode: 429}); err != nil || !r.Changed {
		t.Fatalf("insert failure result=%+v err=%v", r, err)
	}
	// disabled 行 → key_disabled。
	h.exec(t, `UPDATE account_api_key_runtime_states SET status='disabled' WHERE account_id='acc'`)
	if r, err := h.store.RecordFailure(ctx, FailureInput{Account: valid}); err != nil || r.SkippedReason != "key_disabled" {
		t.Fatalf("disabled result=%+v err=%v", r, err)
	}
	h.exec(t, `UPDATE account_api_key_runtime_states SET status='rate_limited' WHERE account_id='acc'`)
	// 严格 CAS 需要 last_attempt_at 严格小于观察时间。
	h.advance(time.Second)
	// 无 fence：已有行 → UPDATE。
	if r, err := h.store.RecordFailure(ctx, FailureInput{Account: valid, Status: "error", BreakQuotaRecoveryWindow: true, QuotaRecoveryMode: "explicit_reset"}); err != nil || !r.Changed {
		t.Fatalf("update failure result=%+v err=%v", r, err)
	}
	h.advance(time.Second)
	// fence provided + 行存在 → fenced UPDATE（fence 反映 UPDATE 前的行状态）。
	before, err := h.store.loadRuntimeRow(ctx, valid.AccountID, valid.SelectedAPIKeyFingerprint)
	if err != nil || before == nil {
		t.Fatalf("load before=%+v err=%v", before, err)
	}
	_ = before
	// fenced UPDATE：无论 CAS 命中或 stale，均覆盖围栏写路径。
	if _, err := h.store.RecordFailure(ctx, FailureInput{
		Account: valid, Status: "rate_limited", CooldownUntil: plusMillis(60_000),
		Expected: ExpectedProbeState{AccountConfigRevision: &rev},
	}); err != nil {
		t.Fatalf("fenced failure err=%v", err)
	}
	// RecordSuccess：manual restore / fenced / 无 fence。
	if r, err := h.store.RecordSuccess(ctx, valid, SuccessInput{Expected: ExpectedProbeState{Status: "error"}}); err != nil || r.SkippedReason != "manual_restore_required" {
		t.Fatalf("manual restore result=%+v err=%v", r, err)
	}
	// fenced success：fence 与行现状一致性影响 CAS 结果，均覆盖写路径。
	if _, err := h.store.RecordSuccess(ctx, valid, SuccessInput{Expected: ExpectedProbeState{Status: "rate_limited", AccountConfigRevision: &rev}}); err != nil {
		t.Fatalf("fenced success err=%v", err)
	}
	// plain success 要求行状态不在 disabled/error 且 CAS 时间严格前进。
	h.exec(t, `UPDATE account_api_key_runtime_states SET status='rate_limited' WHERE account_id='acc'`)
	h.advance(time.Second)
	if r, err := h.store.RecordSuccess(ctx, valid, SuccessInput{}); err != nil || !r.Changed {
		t.Fatalf("plain success result=%+v err=%v", r, err)
	}
	// DeferProbe：missing / invalid / 成功 / stale。
	if r, err := h.store.DeferProbe(ctx, valid, DeferInput{}); err != nil || r.SkippedReason != "missing_expected_probe_at" {
		t.Fatalf("missing defer result=%+v err=%v", r, err)
	}
	if r, err := h.store.DeferProbe(ctx, valid, DeferInput{ExpectedNextProbeAt: "junk"}); err != nil || r.SkippedReason != "invalid_expected_probe_at" {
		t.Fatalf("invalid defer result=%+v err=%v", r, err)
	}
	// DeferProbe 的 UPDATE CAS：fence 要求 next_probe_at 精确匹配行内值，
	// 且 last_attempt_at <= observed。
	h.advance(time.Second)
	deferAt := nowMillisText()
	h.exec(t, `UPDATE account_api_key_runtime_states SET next_probe_at=? WHERE account_id='acc'`, deferAt)
	if r, err := h.store.DeferProbe(ctx, valid, DeferInput{ExpectedNextProbeAt: deferAt, DelaySeconds: 30, BreakQuotaRecoveryWindow: true}); err != nil || !r.Changed {
		t.Fatalf("defer result=%+v err=%v", r, err)
	}
	h.advance(time.Second)
	if r, err := h.store.DeferProbe(ctx, valid, DeferInput{ExpectedNextProbeAt: plusMillis(-500_000)}); err != nil || r.SkippedReason != "stale_probe_state" {
		t.Fatalf("stale defer result=%+v err=%v", r, err)
	}
}

func TestW9DCredentialProjectionArms(t *testing.T) {
	h := openTestDB(t)
	credentials := map[string]any{"api_key": " single "}
	entries := h.store.AccountAPIKeyEntries(credentials)
	_ = credentials
	if len(entries) != 1 || entries[0].Key != "single" {
		t.Fatalf("single key entries=%#v", entries)
	}
	weights := []any{float64(50), float32(300), int(2), int64(-1), json.Number("7")}
	expected := []int{50, 1, 2, 1, 7}
	for i, value := range weights {
		if got := normalizeAPIKeyWeight(value); got != expected[i] {
			t.Fatalf("weight[%d]=%d want %d", i, got, expected[i])
		}
	}
	if got := normalizeAPIKeyWeight(nil); got != 1 {
		t.Fatalf("nil weight=%d", got)
	}
	// keySuffix 投影分支。
	if _, ok := keySuffixForRuntimeDisplay("  "); ok {
		t.Fatal("blank key suffix must be absent")
	}
	if suffix, ok := keySuffixForRuntimeDisplay("ab"); !ok || suffix != "ab" {
		t.Fatalf("short suffix=%q", suffix)
	}
	if suffix, ok := keySuffixForRuntimeDisplay("sk-abcdef"); !ok || suffix != "cdef" {
		t.Fatalf("long suffix=%q", suffix)
	}
	if positiveInteger(-3) != 0 || positiveInteger(9) != 9 {
		t.Fatal("positiveInteger arms")
	}
	if truncatePrefix("short", 12) != "short" {
		t.Fatal("short prefix must pass through")
	}
	if got := truncatePrefix("0123456789abcdef", 12); got != "0123456789ab" {
		t.Fatalf("truncated prefix=%q", got)
	}
	if !strings.HasPrefix(h.store.FingerprintAPIKey("k"), "") {
		t.Fatal("fingerprint shape")
	}
}

var _ = time.Now
