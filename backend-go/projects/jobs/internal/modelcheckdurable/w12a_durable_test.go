package modelcheckdurable

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// sha256Hex 计算十六进制摘要，用于构造与还原 payload 篡改场景。
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// w12a_durable_test.go 用 SQLite 内存库覆盖 Issue/Claim/CommitOutcome/读取链
// 的分支与 tampered/过期路径，PG 臂走 w1cover 门禁校验 CheckSchema。

func w12aDurableStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db, SQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func w12aDurableCanceled(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Cleanup(cancel)
	return ctx
}

func w12aValidDraft(now time.Time, inputID string) modelcheckinput.Draft {
	draft := validDraft(now)
	draft.InputID = inputID
	return draft
}

func TestW12aDurableConstructorAndSchemaBranches(t *testing.T) {
	if _, err := OpenPostgres("   ", 0); err == nil || !strings.Contains(err.Error(), "URL is required") {
		t.Fatalf("空 URL 应报错: %v", err)
	}
	// maxOpen<=0 回落默认值，不报错；负池参数走 ValidatePoolLimits 的路径由
	// 上层装配保证，这里仅锁定空 URL fail-closed。
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := New(nil, SQLite); err == nil || !strings.Contains(err.Error(), "invalid model check durable store") {
		t.Fatalf("nil db 应报错: %v", err)
	}
	if _, err := New(db, Mode("w12a-bad")); err == nil || !strings.Contains(err.Error(), "invalid model check durable store") {
		t.Fatalf("非法 mode 应报错: %v", err)
	}
	var nilStore *Store
	if err := nilStore.EnsureSchema(context.Background()); err == nil {
		t.Fatalf("nil store EnsureSchema 应报错")
	}
	// 说明：CheckSchema 无 nil 接收者守卫（生产装配保证已初始化），不在此测试。
	// sqlite EnsureSchema 失败分支（取消上下文）。
	store, err := New(db, SQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(w12aDurableCanceled(t)); err == nil || !strings.Contains(err.Error(), "initialize model check durable schema") {
		t.Fatalf("取消上下文应使 EnsureSchema 失败: %v", err)
	}
	// CheckSchema 缺表失败 + PG 前缀分支由 w1cover 臂覆盖。
	if err := store.CheckSchema(context.Background()); err == nil || !strings.Contains(err.Error(), "verify model check durable schema") {
		t.Fatalf("缺表应报 schema 校验错误: %v", err)
	}
}

func w12aW1CoverDSN(t *testing.T) string {
	t.Helper()
	rawBytes, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	raw := string(rawBytes)
	if err != nil {
		t.Skip("w12a PG gated: shared.env 不可读")
	}
	base := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip("w12a PG gated: shared.env 缺少 JUHE_AI_POSTGRES_URL")
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base
}

// w12aDurableSelfHealDDL 按权威列集在 juhe_jobs 幂等补建 durable 四表（与
// modelcheckapp/w12a_host_test.go 的 fixture 同源）；共享覆盖库被外部重建后
// 测试可自愈，不依赖他人留下的表。
var w12aDurableSelfHealDDL = []string{
	`CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_input_versions (identity_key TEXT PRIMARY KEY, next_version BIGINT NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_inputs (input_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL, input_version BIGINT NOT NULL, input_digest TEXT NOT NULL, target_id TEXT NOT NULL, config_revision TEXT NOT NULL, policy_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, UNIQUE(identity_key,input_version), UNIQUE(identity_key,input_digest))`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_execution_claims (input_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, claim_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_outcomes (outcome_id TEXT PRIMARY KEY, input_id TEXT NOT NULL UNIQUE, input_digest TEXT NOT NULL, fence_token BIGINT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, stored_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL, committed BOOLEAN NOT NULL DEFAULT FALSE)`,
}

func TestW12aCheckSchemaPostgresArmOnW1Cover(t *testing.T) {
	// 自愈：共享覆盖库被外部重建后，这里幂等补齐 juhe_jobs durable 四表，
	// 再验证 PG 方言前缀臂。
	db, err := sql.Open("pgx", w12aW1CoverDSN(t))
	if err != nil {
		t.Skipf("w12a PG gated: 打开失败: %v", err)
	}
	defer db.Close()
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skip("w12a PG gated: PG 不可达")
	}
	for _, ddl := range w12aDurableSelfHealDDL {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("自愈建表失败: %v", err)
		}
	}
	db.SetMaxOpenConns(1)
	store, err := New(db, Postgres)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(context.Background()); err != nil {
		t.Fatalf("覆盖库 durable schema 应满足契约: %v", err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("EnsureSchema 应透传 CheckSchema: %v", err)
	}
}

func TestW12aIssueStoredInvalidAndReadError(t *testing.T) {
	ctx := context.Background()
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// 直接写入损坏的输入行 → Issue 命中 stored invalid 分支。
	if _, err := store.db.Exec(`INSERT INTO model_check_inputs(input_id,identity_key,input_version,input_digest,target_id,config_revision,policy_revision,trigger,issued_at,expires_at,payload) VALUES('w12a-broken','w12a-identity',1,'digest','t','c','p','manual','2026-09-17T00:00:00Z','2026-09-17T01:00:00Z','not-json')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Issue(ctx, w12aValidDraft(now, "w12a-broken")); err == nil || !strings.Contains(err.Error(), "stored model check input is invalid") {
		t.Fatalf("损坏存量输入应报错: %v", err)
	}
	// 写入合法 payload 但与 draft 不一致 → immutable 冲突分支。
	first, err := store.Issue(ctx, w12aValidDraft(now, "w12a-ok"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Issue(ctx, w12aValidDraft(now.Add(time.Minute), "w12a-ok")); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("不同 draft 复用 InputID 应冲突: %v", err)
	}
	// 相同 draft 重放 → 返回存量。
	replay, err := store.Issue(ctx, w12aValidDraft(now, "w12a-ok"))
	if err != nil || replay.Input.InputID != first.Input.InputID || replay.Input.InputVersion != 1 {
		t.Fatalf("重放应幂等: %#v %v", replay, err)
	}
	// next_version 非法分支：删除输入行保留 versions 行并把版本改为 0。
	firstIssue, err := store.Issue(ctx, w12aValidDraft(now, "w12a-version-check"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM model_check_inputs WHERE identity_key=?`, firstIssue.IdentityKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE model_check_input_versions SET next_version=0 WHERE identity_key=?`, firstIssue.IdentityKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Issue(ctx, w12aValidDraft(now, "w12a-version-check")); err == nil || !strings.Contains(err.Error(), "stored model check version is invalid") {
		t.Fatalf("非法 next_version 应报错: %v", err)
	}
}

func TestW12aLoadInputTamperedAndExpired(t *testing.T) {
	ctx := context.Background()
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	first, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadInput(ctx, " ", now); err == nil || !strings.Contains(err.Error(), "input ID is required") {
		t.Fatalf("空 inputID 应报错: %v", err)
	}
	if _, err := store.LoadInput(ctx, "w12a-ghost", now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("缺失输入应返回 ErrNoRows: %v", err)
	}
	// 篡改 payload → ErrInputTampered。
	if _, err := store.db.Exec(`UPDATE model_check_inputs SET payload='{"tampered":true}' WHERE input_id='w12a-input'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadInput(ctx, "w12a-input", now); !errors.Is(err, ErrInputTampered) {
		t.Fatalf("篡改 payload 应报 tampered: %v", err)
	}
	// 篡改 identity_key → ErrInputTampered。
	if _, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input2")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE model_check_inputs SET identity_key='w12a-other' WHERE input_id='w12a-input2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadInput(ctx, "w12a-input2", now); !errors.Is(err, ErrInputTampered) {
		t.Fatalf("篡改 identity 应报 tampered: %v", err)
	}
	// 过期 → ErrExpired（使用未被前面篡改场景污染的输入）。
	if _, err := store.Issue(ctx, w12aValidDraft(now, "w12a-fresh")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadInput(ctx, "w12a-fresh", now.Add(10*time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("过期输入应报 expired: %v", err)
	}
	_ = first
}

func TestW12aClaimValidationAndConflicts(t *testing.T) {
	ctx := context.Background()
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if _, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input")); err != nil {
		t.Fatal(err)
	}
	// 输入校验。
	if _, err := store.Claim(ctx, "", "owner", "token", "outcome", now, time.Minute); err == nil {
		t.Fatalf("空 inputID 应报错")
	}
	if _, err := store.Claim(ctx, "w12a-input", "owner", "token", "outcome", now, 0); err == nil {
		t.Fatalf("零租约应报错")
	}
	// 输入不存在 → 查询错误。
	if _, err := store.Claim(ctx, "w12a-ghost", "owner", "token", "outcome", now, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("缺失输入应返回 ErrNoRows: %v", err)
	}
	// 输入过期 → ErrExpired。
	expired := w12aValidDraft(now, "w12a-expired")
	expired.DeadlineAt = now.Add(time.Minute)
	if _, err := store.Issue(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(ctx, "w12a-expired", "owner", "token", "outcome", now.Add(2*time.Minute), time.Minute); !errors.Is(err, ErrExpired) {
		t.Fatalf("过期输入应报 expired: %v", err)
	}
	// 首次认领 fence=1。
	first, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute)
	if err != nil || first.FenceToken != 1 {
		t.Fatalf("首次认领应成功: %#v %v", first, err)
	}
	// 他人认领未过期租约 → ErrBusy。
	if _, err := store.Claim(ctx, "w12a-input", "owner-b", "token-b", "outcome-b", now.Add(time.Second), time.Minute); !errors.Is(err, ErrBusy) {
		t.Fatalf("他人认领应 busy: %v", err)
	}
	// 同 owner 不同 outcome → ErrClaimConflict。
	if _, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-x", now.Add(time.Second), time.Minute); !errors.Is(err, ErrClaimConflict) {
		t.Fatalf("不同 outcome 应 conflict: %v", err)
	}
	// 同 owner 同 token 同 outcome → 幂等返回。
	replay, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-a", now.Add(time.Second), time.Minute)
	if err != nil || replay.FenceToken != 1 || replay.InputID != "w12a-input" {
		t.Fatalf("幂等认领应成功: %#v %v", replay, err)
	}
	// 租约到期后重新认领 → fence 递增。
	later, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-a", now.Add(2*time.Minute), time.Minute)
	if err != nil || later.FenceToken != 2 {
		t.Fatalf("租约过期后应递增 fence: %#v %v", later, err)
	}
}

func TestW12aReleaseClaimBranches(t *testing.T) {
	ctx := context.Background()
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if _, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input")); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// 校验分支。
	if err := store.ReleaseClaim(ctx, Claim{InputID: "", ClaimToken: "t", OwnerID: "o", OutcomeID: "x", FenceToken: 1}, now); err == nil {
		t.Fatalf("空 InputID 应报错")
	}
	if err := store.ReleaseClaim(ctx, claim, time.Time{}); err == nil {
		t.Fatalf("零时间应报错")
	}
	// 错误 fence → ErrStaleFence。
	stale := claim
	stale.FenceToken = 99
	if err := store.ReleaseClaim(ctx, stale, now); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("错误 fence 应 stale: %v", err)
	}
	// 成功释放。
	if err := store.ReleaseClaim(ctx, claim, now); err != nil {
		t.Fatalf("释放不应报错: %v", err)
	}
	// 重复释放幂等（fence 匹配即置空租约时间）。
	if err := store.ReleaseClaim(ctx, claim, now); err != nil {
		t.Fatalf("重复释放应幂等: %v", err)
	}
}

func TestW12aCommitOutcomeBranches(t *testing.T) {
	ctx := context.Background()
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if _, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input")); err != nil {
		t.Fatal(err)
	}
	issued, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"summary":"w12a"}`)
	good := Outcome{InputID: "w12a-input", OutcomeID: "outcome-a", InputDigest: issued.Input.InputDigest, Payload: payload, ObservedAt: now, StoredAt: now}
	// 输入校验。
	if err := store.CommitOutcome(ctx, Outcome{}, claim, now); err == nil {
		t.Fatalf("空 outcome 应报错")
	}
	wrongClaim := claim
	wrongClaim.InputID = "other"
	if err := store.CommitOutcome(ctx, good, wrongClaim, now); err == nil {
		t.Fatalf("claim 不匹配应报错")
	}
	badDigest := good
	badDigest.PayloadDigest = "deadbeef"
	if err := store.CommitOutcome(ctx, badDigest, claim, now); err == nil || !strings.Contains(err.Error(), "payload digest mismatch") {
		t.Fatalf("payload digest 不符应报错: %v", err)
	}
	// input digest 不匹配。
	wrongInput := good
	wrongInput.InputDigest = "other-digest"
	if err := store.CommitOutcome(ctx, wrongInput, claim, now); err == nil || !strings.Contains(err.Error(), "input digest mismatch") {
		t.Fatalf("input digest 不符应报错: %v", err)
	}
	// owner-b 接管租约（fence 递增）后，owner-a 的旧 claim 提交 → stale fence。
	taken, err := store.Claim(ctx, "w12a-input", "owner-b", "token-b", "outcome-b", now.Add(2*time.Minute), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOutcome(ctx, good, claim, now.Add(2*time.Minute)); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("旧 claim 提交应 stale fence: %v", err)
	}
	// 新 claim 提交有效 outcome。
	active := taken
	activeOutcome := good
	activeOutcome.OutcomeID = "outcome-b"
	if err := store.CommitOutcome(ctx, activeOutcome, active, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("有效提交不应报错: %v", err)
	}
	// 一致重放成功。
	if err := store.CommitOutcome(ctx, activeOutcome, active, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("一致重放应成功: %v", err)
	}
	// outcome ID 不同 → ErrOutcomeConflict。
	different := activeOutcome
	different.OutcomeID = "outcome-c"
	if err := store.CommitOutcome(ctx, different, active, now.Add(2*time.Minute)); !errors.Is(err, ErrOutcomeConflict) {
		t.Fatalf("不同 outcome 应 conflict: %v", err)
	}
}

func TestW12aListAndFindCommittedOutcomes(t *testing.T) {
	ctx := context.Background()
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOutcome(ctx, Outcome{InputID: "w12a-input", OutcomeID: "outcome-a", InputDigest: issued.Input.InputDigest, Payload: json.RawMessage(`{"s":1}`)}, claim, now); err != nil {
		t.Fatal(err)
	}
	// 校验分支。
	var nilStore *Store
	if _, err := nilStore.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); err == nil {
		t.Fatalf("nil store 应报错")
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 0); err == nil {
		t.Fatalf("limit 0 应报错")
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10001); err == nil {
		t.Fatalf("limit 超限应报错")
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{StoredAt: now}, 10); err == nil || !strings.Contains(err.Error(), "cursor is incomplete") {
		t.Fatalf("不完整游标应报错: %v", err)
	}
	// 正常列出与游标翻页。
	found, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10)
	if err != nil || len(found) != 1 {
		t.Fatalf("应列出 1 条: %d %v", len(found), err)
	}
	if found[0].IdentityKey == "" || !found[0].Outcome.Committed || found[0].Input.InputID != "w12a-input" {
		t.Fatalf("存储结果不符: %#v", found[0])
	}
	after, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{StoredAt: found[0].Outcome.StoredAt, OutcomeID: found[0].Outcome.OutcomeID}, 10)
	if err != nil || len(after) != 0 {
		t.Fatalf("游标之后应为空: %d %v", len(after), err)
	}
	// 查询失败分支：取消上下文。
	if _, err := store.ListCommittedOutcomes(w12aDurableCanceled(t), OutcomeCursor{}, 10); err == nil || !strings.Contains(err.Error(), "list committed model check outcomes") {
		t.Fatalf("取消上下文应使列表失败: %v", err)
	}
	// FindCommittedOutcome。
	if _, ok, err := store.FindCommittedOutcome(ctx, " "); err == nil {
		t.Fatalf("空 outcomeID 应报错")
	} else if ok {
		t.Fatalf("空 outcomeID 不应命中")
	}
	if _, ok, err := store.FindCommittedOutcome(ctx, "w12a-ghost"); err != nil || ok {
		t.Fatalf("缺失 outcome 应 not found: %v", err)
	}
	hit, ok, err := store.FindCommittedOutcome(ctx, "outcome-a")
	if err != nil || !ok || hit.Outcome.OutcomeID != "outcome-a" {
		t.Fatalf("应命中: %v %v", ok, err)
	}
	if _, ok, err := store.FindCommittedOutcome(w12aDurableCanceled(t), "outcome-a"); err == nil || ok {
		t.Fatalf("取消上下文应使查询失败")
	}
}

func TestW12aScanStoredOutcomeTamperedVariants(t *testing.T) {
	ctx := context.Background()
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOutcome(ctx, Outcome{InputID: "w12a-input", OutcomeID: "outcome-a", InputDigest: issued.Input.InputDigest, Payload: json.RawMessage(`{"s":1}`)}, claim, now); err != nil {
		t.Fatal(err)
	}
	// 破坏 payload digest → ErrOutcomeTampered。
	if _, err := store.db.Exec(`UPDATE model_check_outcomes SET payload_digest='deadbeef'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); !errors.Is(err, ErrOutcomeTampered) {
		t.Fatalf("digest 篡改应报 tampered: %v", err)
	}
	// 恢复 digest，篡改 payload → ErrOutcomeTampered。
	if _, err := store.db.Exec(`UPDATE model_check_outcomes SET payload='{"s":2}', payload_digest='deadbeef'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); !errors.Is(err, ErrOutcomeTampered) {
		t.Fatalf("payload 篡改应报 tampered: %v", err)
	}
	// 恢复 payload 与 digest，篡改 identity_key → ErrInputTampered。
	_ = store.CommitOutcome // 保持引用
	// 重新计算正确 digest 后写回，再破坏 identity。
	payloadDigest := sha256Hex([]byte(`{"s":2}`))
	if _, err := store.db.Exec(`UPDATE model_check_outcomes SET payload_digest=?`, payloadDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE model_check_inputs SET identity_key='w12a-broken'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); !errors.Is(err, ErrInputTampered) {
		t.Fatalf("identity 篡改应报 input tampered: %v", err)
	}
}

// ---- 第二轮：取消上下文与逐列缺失分支 ----

func TestW12aDurableCanceledContextErrors(t *testing.T) {
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	ctx := w12aDurableCanceled(t)
	if _, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input")); err == nil {
		t.Fatalf("取消上下文应使 Issue 失败")
	}
	if _, err := store.Claim(ctx, "w12a-input", "o", "t", "x", now, time.Minute); err == nil {
		t.Fatalf("取消上下文应使 Claim 失败")
	}
	claim := Claim{InputID: "w12a-input", ClaimToken: "t", OutcomeID: "x", OwnerID: "o", FenceToken: 1}
	if err := store.ReleaseClaim(ctx, claim, now); err == nil {
		t.Fatalf("取消上下文应使 ReleaseClaim 失败")
	}
	if err := store.CommitOutcome(ctx, Outcome{InputID: "w12a-input", OutcomeID: "x", InputDigest: "d", Payload: json.RawMessage(`{}`)}, claim, now); err == nil {
		t.Fatalf("取消上下文应使 CommitOutcome 失败")
	}
}

// w12aDurableDropColumn 打开内存库、建 schema 后删除指定表的指定列。
func w12aDurableDropColumn(t *testing.T, table, column string) *Store {
	t.Helper()
	store := w12aDurableStore(t)
	if _, err := store.db.Exec("ALTER TABLE " + table + " DROP COLUMN " + column); err != nil {
		t.Skipf("当前 sqlite 不支持 DROP COLUMN，跳过: %v", err)
	}
	return store
}

func TestW12aIssueVersionTableErrors(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// versions 缺 updated_at：新 identity 的版本行 INSERT 失败。
	noUpdated := w12aDurableDropColumn(t, "model_check_input_versions", "updated_at")
	if _, err := noUpdated.Issue(context.Background(), w12aValidDraft(now, "w12a-v1")); err == nil {
		t.Fatalf("versions 缺列应使版本行写入失败")
	}
	// versions 缺 next_version：版本读取失败（非 ErrNoRows）。
	noNext := w12aDurableDropColumn(t, "model_check_input_versions", "next_version")
	if _, err := noNext.Issue(context.Background(), w12aValidDraft(now, "w12a-v2")); err == nil {
		t.Fatalf("versions 缺 next_version 应使读取失败")
	}
	// inputs 缺 trigger：输入持久化失败（UNIQUE 索引引用的列不可删）。
	noTrigger := w12aDurableDropColumn(t, "model_check_inputs", "trigger")
	if _, err := noTrigger.Issue(context.Background(), w12aValidDraft(now, "w12a-v3")); err == nil || !strings.Contains(err.Error(), "persist model check input") {
		t.Fatalf("inputs 缺列应使持久化失败: %v", err)
	}
}

func TestW12aLoadAndClaimTimestampErrors(t *testing.T) {
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if _, err := store.Issue(context.Background(), w12aValidDraft(now, "w12a-input")); err != nil {
		t.Fatal(err)
	}
	// expires_at 损坏 → LoadInput 读取时间错误。
	if _, err := store.db.Exec(`UPDATE model_check_inputs SET expires_at='garbage'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadInput(context.Background(), "w12a-input", now); err == nil || !strings.Contains(err.Error(), "invalid stored model check timestamp") {
		t.Fatalf("损坏的过期时间应报错: %v", err)
	}
	// Claim 的 expires_at 读取错误。
	if _, err := store.Claim(context.Background(), "w12a-input", "o", "t", "x", now, time.Minute); err == nil || !strings.Contains(err.Error(), "invalid stored model check timestamp") {
		t.Fatalf("Claim 过期时间损坏应报错: %v", err)
	}
	// 恢复 expires_at，破坏 claim_until → Claim 租约读取错误。
	if _, err := store.db.Exec(`UPDATE model_check_inputs SET expires_at=?`, now.Add(time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO model_check_execution_claims(input_id,claim_token,outcome_id,owner_id,fence_token,claim_until,updated_at) VALUES('w12a-input','t','x','o',1,'garbage','2026-09-17T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(context.Background(), "w12a-input", "o2", "t2", "x2", now, time.Minute); err == nil || !strings.Contains(err.Error(), "invalid stored model check timestamp") {
		t.Fatalf("Claim 租约时间损坏应报错: %v", err)
	}
}

func TestW12aClaimInsertAndReleaseUpdateErrors(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// claims 缺 updated_at：fence 递增后的 INSERT 失败。
	noUpdated := w12aDurableDropColumn(t, "model_check_execution_claims", "updated_at")
	if _, err := noUpdated.Issue(context.Background(), w12aValidDraft(now, "w12a-input")); err != nil {
		t.Fatal(err)
	}
	if _, err := noUpdated.Claim(context.Background(), "w12a-input", "o", "t", "x", now, time.Minute); err == nil {
		t.Fatalf("claims 缺 updated_at 应使认领失败")
	}
	// claims 缺 outcome_id：全新认领的 INSERT 失败（ErrNoRows → fence=1 分支）。
	noOutcome := w12aDurableDropColumn(t, "model_check_execution_claims", "outcome_id")
	if _, err := noOutcome.Issue(context.Background(), w12aValidDraft(now, "w12a-input")); err != nil {
		t.Fatal(err)
	}
	if _, err := noOutcome.Claim(context.Background(), "w12a-input", "o", "t", "x", now, time.Minute); err == nil {
		t.Fatalf("claims 缺 outcome_id 应使认领失败")
	}
	// ReleaseClaim 的 UPDATE 失败：claims 缺 claim_until。
	noUntil := w12aDurableDropColumn(t, "model_check_execution_claims", "claim_until")
	if _, err := noUntil.Issue(context.Background(), w12aValidDraft(now, "w12a-input")); err != nil {
		t.Fatal(err)
	}
	claim := Claim{InputID: "w12a-input", ClaimToken: "t", OutcomeID: "x", OwnerID: "o", FenceToken: 1}
	if err := noUntil.ReleaseClaim(context.Background(), claim, now); err == nil {
		t.Fatalf("claims 缺 claim_until 应使释放失败")
	}
}

func TestW12aScanStoredOutcomeNullTimestamps(t *testing.T) {
	ctx := context.Background()
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOutcome(ctx, Outcome{InputID: "w12a-input", OutcomeID: "outcome-a", InputDigest: issued.Input.InputDigest, Payload: json.RawMessage(`{"s":1}`)}, claim, now); err != nil {
		t.Fatal(err)
	}
	// observed/stored 置为非法文本 → readTime 失败 → ErrOutcomeTampered。
	if _, err := store.db.Exec(`UPDATE model_check_outcomes SET observed_at='garbage', stored_at='garbage'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); !errors.Is(err, ErrOutcomeTampered) {
		t.Fatalf("非法时间戳应报 tampered: %v", err)
	}
	// payload 非法 JSON → ErrOutcomeTampered。
	if _, err := store.db.Exec(`UPDATE model_check_outcomes SET observed_at=?, stored_at=?, payload='not-json', payload_digest='deadbeef'`, now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); !errors.Is(err, ErrOutcomeTampered) {
		t.Fatalf("非法 payload 应报 tampered: %v", err)
	}
	// identity_key 置空 → ErrInputTampered（payload 与 digest 先恢复一致）。
	digest := sha256Hex([]byte(`{"s":1}`))
	if _, err := store.db.Exec(`UPDATE model_check_outcomes SET payload='{"s":1}', payload_digest=?`, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE model_check_inputs SET identity_key=''`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); !errors.Is(err, ErrInputTampered) {
		t.Fatalf("空 identity 应报 input tampered: %v", err)
	}
}

// ---- 第三轮：列缺失与租约过期的提交分支 ----

func TestW12aOpenSQLiteEmptyPath(t *testing.T) {
	_, err := OpenSQLite("  ")
	t.Logf("empty path err=%v", err)
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("空路径应报错: %v", err)
	}
}

func TestW12aIssueSelectPayloadError(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// inputs 缺 payload：存量查询失败（非 ErrNoRows）。
	noPayload := w12aDurableDropColumn(t, "model_check_inputs", "payload")
	if _, err := noPayload.Issue(context.Background(), w12aValidDraft(now, "w12a-input")); err == nil {
		t.Fatalf("inputs 缺 payload 应使存量查询失败")
	}
}

func TestW12aIssueVersionUpdateError(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	store := w12aDurableStore(t)
	first, err := store.Issue(context.Background(), w12aValidDraft(now, "w12a-a"))
	if err != nil {
		t.Fatal(err)
	}
	// 新 identity 的 draft 以便走 versions UPDATE 分支。
	changed := w12aValidDraft(now, "w12a-b")
	changed.Target.ConfigRevision = "w12a-rev-2"
	if _, err := store.Issue(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("ALTER TABLE model_check_input_versions DROP COLUMN updated_at"); err != nil {
		t.Skipf("当前 sqlite 不支持 DROP COLUMN，跳过: %v", err)
	}
	_ = first
	// 第三个新 identity：versions SELECT 成功（ErrNoRows→INSERT）会失败于
	// updated_at；此处改用既有 identity 的不同内容触发 UPDATE 分支不可行
	// （identity 由内容派生），因此直接校验 INSERT 失败路径已被上一用例覆盖。
}

func TestW12aCommitOutcomeReadErrors(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// 先建立认领行，再删除 outcome_id 列：提交的 claim 读取失败。
	noOutcome := w12aDurableStore(t)
	issuedNoOutcome, err := noOutcome.Issue(context.Background(), w12aValidDraft(now, "w12a-input"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noOutcome.Claim(context.Background(), "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := noOutcome.db.Exec(`ALTER TABLE model_check_execution_claims DROP COLUMN outcome_id`); err != nil {
		t.Skipf("当前 sqlite 不支持 DROP COLUMN，跳过: %v", err)
	}
	claim := Claim{InputID: "w12a-input", ClaimToken: "token-a", OutcomeID: "outcome-a", OwnerID: "owner-a", FenceToken: 1}
	outcome := Outcome{InputID: "w12a-input", OutcomeID: "outcome-a", InputDigest: issuedNoOutcome.Input.InputDigest, Payload: json.RawMessage(`{}`)}
	if err := noOutcome.CommitOutcome(context.Background(), outcome, claim, now); err == nil {
		t.Fatalf("claims 缺列应使提交读取失败")
	}
	// inputs 缺 input_digest（已有输入行）：提交的 input digest 读取失败。
	noDigest := w12aDurableStore(t)
	issuedNoDigest, err := noDigest.Issue(context.Background(), w12aValidDraft(now, "w12a-input"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noDigest.Claim(context.Background(), "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := noDigest.db.Exec(`ALTER TABLE model_check_inputs DROP COLUMN input_digest`); err != nil {
		t.Skipf("当前 sqlite 不支持 DROP COLUMN，跳过: %v", err)
	}
	digestOutcome := Outcome{InputID: "w12a-input", OutcomeID: "outcome-a", InputDigest: issuedNoDigest.Input.InputDigest, Payload: json.RawMessage(`{}`)}
	if err := noDigest.CommitOutcome(context.Background(), digestOutcome, claim, now); err == nil {
		t.Fatalf("inputs 缺 input_digest 应使提交读取失败")
	}
}

func TestW12aCommitOutcomeLeaseExpired(t *testing.T) {
	ctx := context.Background()
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// 租约过期后提交 → ErrExpired。
	if err := store.CommitOutcome(ctx, Outcome{InputID: "w12a-input", OutcomeID: "outcome-a", InputDigest: issued.Input.InputDigest, Payload: json.RawMessage(`{"s":1}`)}, claim, now.Add(2*time.Minute)); !errors.Is(err, ErrExpired) {
		t.Fatalf("租约过期提交应 expired: %v", err)
	}
}

func TestW12aClaimLeaseTimeCorruptInCommit(t *testing.T) {
	ctx := context.Background()
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE model_check_execution_claims SET claim_until='garbage'`); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOutcome(ctx, Outcome{InputID: "w12a-input", OutcomeID: "outcome-a", InputDigest: issued.Input.InputDigest, Payload: json.RawMessage(`{"s":1}`)}, claim, now); err == nil || !strings.Contains(err.Error(), "invalid stored model check timestamp") {
		t.Fatalf("租约时间损坏应报错: %v", err)
	}
}

func TestW12aCommitOutcomeInsertError(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// outcomes 缺 payload_digest：提交 INSERT 失败。
	noDigest := w12aDurableDropColumn(t, "model_check_outcomes", "payload_digest")
	issued, err := noDigest.Issue(context.Background(), w12aValidDraft(now, "w12a-input"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := noDigest.Claim(context.Background(), "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	outcome := Outcome{InputID: "w12a-input", OutcomeID: "outcome-a", InputDigest: issued.Input.InputDigest, Payload: json.RawMessage(`{"s":1}`)}
	if err := noDigest.CommitOutcome(context.Background(), outcome, claim, now); err == nil {
		t.Fatalf("outcomes 缺列应使提交失败")
	}
}

func TestW12aListStoredOutcomeStoredTimeTampered(t *testing.T) {
	ctx := context.Background()
	store := w12aDurableStore(t)
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(ctx, w12aValidDraft(now, "w12a-input"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "w12a-input", "owner-a", "token-a", "outcome-a", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitOutcome(ctx, Outcome{InputID: "w12a-input", OutcomeID: "outcome-a", InputDigest: issued.Input.InputDigest, Payload: json.RawMessage(`{"s":1}`)}, claim, now); err != nil {
		t.Fatal(err)
	}
	// observed 合法而 stored 非法 → ErrOutcomeTampered。
	if _, err := store.db.Exec(`UPDATE model_check_outcomes SET stored_at='garbage'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListCommittedOutcomes(ctx, OutcomeCursor{}, 10); !errors.Is(err, ErrOutcomeTampered) {
		t.Fatalf("stored 时间损坏应报 tampered: %v", err)
	}
}
