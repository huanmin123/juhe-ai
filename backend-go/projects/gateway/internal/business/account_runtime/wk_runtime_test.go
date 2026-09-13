package accountruntime

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// wkFailQuota 驱动配额端口错误分支。
type wkFailQuota struct{}

func (wkFailQuota) ReadAPIKeyQuotaCosts(context.Context, string, string, time.Time, *int) (QuotaCosts, error) {
	return QuotaCosts{}, errors.New("stats unavailable")
}

// wkFailSchedule 驱动排程评估错误分支。
type wkFailSchedule struct{}

func (wkFailSchedule) EvaluateSchedule(context.Context, string, time.Time) (ScheduleDecision, error) {
	return ScheduleDecision{}, errors.New("schedule parse failed")
}

func wkDeps(t *testing.T, extra ...Dependencies) (*Store, *sql.DB) {
	t.Helper()
	deps := Dependencies{}
	if len(extra) > 0 {
		deps = extra[0]
	}
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, deps)
	return s, db
}

func TestNewValidation(t *testing.T) {
	if _, err := New(nil, SQLite, "", OwnerGate{}); err == nil {
		t.Fatal("nil db 必须被拒绝")
	}
	if _, err := New(&sql.DB{}, Mode("mysql"), "", OwnerGate{}); !errors.Is(err, ErrInvalidMode) {
		t.Fatalf("非法 mode err=%v", err)
	}
	if _, err := New(&sql.DB{}, Postgres, "bad schema", OwnerGate{}); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("非法 schema err=%v", err)
	}
	if _, err := NewStore(&sql.DB{}, Postgres, " ", OwnerGate{}); err != nil {
		t.Fatalf("空 schema 应回落默认: %v", err)
	}
	// Postgres 模式下的 SQL 文改写。
	s, db := testStore(t, OwnerGate{}, Dependencies{})
	defer db.Close()
	s.mode = Postgres
	if got := s.authorizationExpiryAfterNow("ga.expires_at"); !strings.Contains(got, "::timestamptz") {
		t.Fatalf("postgres 过期谓词=%q", got)
	}
}

func TestCheckContractVerifiesRelations(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	ctx := context.Background()
	if err := s.CheckContract(ctx); err != nil {
		t.Fatalf("完整契约: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE group_authorization_settings`); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckContract(ctx); err == nil {
		t.Fatal("缺表必须失败")
	}
	var nilStore *Store
	if err := nilStore.CheckContract(ctx); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil store err=%v", err)
	}
}

func TestParseTimeAndHelpers(t *testing.T) {
	if _, err := parseTime("   "); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空白时间 err=%v", err)
	}
	if _, err := parseTime("not-a-time"); err == nil {
		t.Fatal("非法时间必须失败")
	}
	if got := sanitize(strings.Repeat("x", 1200)); len(got) != 1000 {
		t.Fatalf("sanitize 长度=%d", len(got))
	}
	if got := sanitize("  trimmed  "); got != "trimmed" {
		t.Fatalf("sanitize=%q", got)
	}
	if clamp(0, 3, 3600) != 3 || clamp(9999, 3, 3600) != 3600 || clamp(50, 3, 3600) != 50 {
		t.Fatal("clamp 边界不符")
	}
	if boolInt(true) != 1 || boolInt(false) != 0 {
		t.Fatal("boolInt 不符")
	}
	if firstNonEmpty("  ", "b") != "b" || firstNonEmpty("a", "b") != "a" {
		t.Fatal("firstNonEmpty 不符")
	}
	if randomToken() == "" {
		t.Fatal("random token 为空")
	}
}

func TestQuotaCostsPortsAndLimits(t *testing.T) {
	s, db := wkDeps(t, Dependencies{QuotaUsage: wkFailQuota{}})
	defer db.Close()
	ctx := context.Background()
	// 别名方法转发（启用额度才能触达端口）。
	aliasStore := s
	enabledKey := GatewayAPIKey{QuotaLimitsJSON: `{"total":{"enabled":true,"limit":10}}`}
	if _, err := aliasStore.CheckApiKeyQuota(ctx, enabledKey); err == nil || !strings.Contains(err.Error(), "stats unavailable") {
		t.Fatalf("CheckApiKeyQuota 转发 err=%v", err)
	}
	// 未启用额度的别名转发直接零开销返回。
	if costs, err := aliasStore.ReadApiKeyQuotaCosts(ctx, GatewayAPIKey{}); err != nil || costs != (QuotaCosts{}) {
		t.Fatalf("别名零开销返回=%+v err=%v", costs, err)
	}
	// 启用额度时别名转发端口错误。
	if _, err := aliasStore.ReadApiKeyQuotaCosts(ctx, enabledKey); err == nil {
		t.Fatal("ReadApiKeyQuotaCosts 应转发端口错误")
	}
	// 额度未启用时零开销直接放行。
	disabled := GatewayAPIKey{QuotaLimitsJSON: "{}"}
	costs, err := s.ReadAPIKeyQuotaCosts(ctx, disabled)
	if err != nil || costs != (QuotaCosts{}) {
		t.Fatalf("未启用额度: %+v %v", costs, err)
	}
	if _, err := s.ReadAPIKeyQuotaCosts(ctx, GatewayAPIKey{QuotaLimitsJSON: "{bad"}); err == nil {
		t.Fatal("非法 JSON 必须失败")
	}
	decision, err := s.CheckAPIKeyQuota(ctx, disabled)
	if err != nil || !decision.Allowed {
		t.Fatalf("未启用额度决策: %+v %v", decision, err)
	}
	if _, err := s.CheckAPIKeyQuota(ctx, GatewayAPIKey{QuotaLimitsJSON: "{bad"}); err == nil {
		t.Fatal("非法 JSON 决策必须失败")
	}
	// 启用额度但端口失败。
	enabled := GatewayAPIKey{QuotaLimitsJSON: `{"hourly":{"enabled":true,"limit":10,"windowHours":2},"total":{"enabled":true,"limit":10}}`}
	if _, err := s.ReadAPIKeyQuotaCosts(ctx, enabled); err == nil {
		t.Fatal("端口失败必须透出")
	}
	// 启用额度且超额 → 拒绝。
	failing := Dependencies{QuotaUsage: QuotaUsageFunc(func(_ context.Context, _ string, _ string, _ time.Time, hours *int) (QuotaCosts, error) {
		if hours != nil && *hours != 2 {
			t.Fatalf("小时窗口=%d want 2", *hours)
		}
		return QuotaCosts{Hourly: 99, Total: 99}, nil
	})}
	s2, db2 := wkDeps(t, failing)
	defer db2.Close()
	decision, err = s2.CheckAPIKeyQuota(ctx, enabled)
	if err != nil || decision.Allowed || decision.Message == "" {
		t.Fatalf("超额决策=%+v err=%v", decision, err)
	}
}

func TestQuotaUnderLimitAllowed(t *testing.T) {
	deps := Dependencies{QuotaUsage: QuotaUsageFunc(func(context.Context, string, string, time.Time, *int) (QuotaCosts, error) {
		return QuotaCosts{Hourly: 1}, nil
	})}
	s, db := wkDeps(t, deps)
	defer db.Close()
	enabled := GatewayAPIKey{QuotaLimitsJSON: `{"hourly":{"enabled":true,"limit":10}}`}
	decision, err := s.CheckAPIKeyQuota(context.Background(), enabled)
	if err != nil || !decision.Allowed {
		t.Fatalf("未超额决策=%+v err=%v", decision, err)
	}
}

func TestCursorSaveReadDeleteLifecycle(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	ctx := context.Background()
	seedAccount(t, db, "active")
	// save 缺失字段 → ErrInvalidInput。
	if _, err := s.AccountAPIKeyPoolProbeCursor(ctx, ProbeCursor{Purpose: HealthCheck}, "save"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("缺失 account err=%v", err)
	}
	badCursor := ProbeCursor{AccountID: "acct-1", Purpose: HealthCheck, KeySetFingerprint: "set-1", ConfigRevision: 0}
	if _, err := s.AccountAPIKeyPoolProbeCursor(ctx, badCursor, "save"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("缺失 fingerprint/revision err=%v", err)
	}
	// save 命中。
	in := ProbeCursor{AccountID: "acct-1", Purpose: HealthCheck, KeySetFingerprint: "set-1", ConfigRevision: 3, DispatchRevision: int64Ptr(9), CooldownGeneration: "cd-1", SourceConfigRevision: int64Ptr(2)}
	result, err := s.AccountAPIKeyPoolProbeCursor(ctx, in, "save")
	if err != nil || !result.Changed {
		t.Fatalf("save=%+v err=%v", result, err)
	}
	read, err := s.AccountAPIKeyPoolProbeCursor(ctx, ProbeCursor{AccountID: "acct-1", Purpose: HealthCheck}, "read")
	if err != nil || read.Cursor == nil || read.Cursor.ConfigRevision != 3 || *read.Cursor.DispatchRevision != 9 || *read.Cursor.SourceConfigRevision != 2 || read.Cursor.CooldownGeneration != "cd-1" {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	// 保存别名。
	saved, err := s.SaveAccountAPIKeyPoolProbeCursor(ctx, in)
	if err != nil || saved.KeySetFingerprint != "set-1" {
		t.Fatalf("save alias=%+v err=%v", saved, err)
	}
	// 删除与缺失。
	if err := s.DeleteAccountAPIKeyPoolProbeCursor(ctx, "acct-1", HealthCheck); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadAccountAPIKeyPoolProbeCursor(ctx, "acct-1", HealthCheck); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("缺失 cursor err=%v", err)
	}
	if _, err := s.AccountAPIKeyPoolProbeCursor(ctx, ProbeCursor{AccountID: "acct-1", Purpose: HealthCheck}, "read"); err == nil {
		t.Fatal("底层 read 缺失必须报错")
	}
	deleted, err := s.AccountAPIKeyPoolProbeCursor(ctx, ProbeCursor{AccountID: "acct-1", Purpose: HealthCheck}, "delete")
	if err != nil || deleted.Changed {
		t.Fatalf("重复 delete=%+v err=%v", deleted, err)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestDeferProbeValidation(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	seedAccount(t, db, "active")
	account := poolAccount()
	ctx := context.Background()
	// 缺失 expected probe at。
	if result, err := s.DeferAccountAPIKeyProbe(ctx, account, ProbeDeferInput{ExpectedStatus: RuntimeTemporaryUnavailable, DelaySeconds: 5}); err != nil || result.SkippedReason != "missing_expected_probe_at" {
		t.Fatalf("missing expected: %+v %v", result, err)
	}
	// 非法 expected probe at。
	if result, err := s.DeferAccountAPIKeyProbe(ctx, account, ProbeDeferInput{ExpectedStatus: RuntimeTemporaryUnavailable, ExpectedNextProbeAt: "bad-time"}); err != nil || result.SkippedReason != "invalid_expected_probe_at" {
		t.Fatalf("invalid expected: %+v %v", result, err)
	}
	// 非法 expected status。
	if result, err := s.DeferAccountAPIKeyProbe(ctx, account, ProbeDeferInput{ExpectedStatus: RuntimeActive, ExpectedNextProbeAt: "2030-01-01T00:00:00Z"}); err != nil || result.SkippedReason != "invalid_expected_status" {
		t.Fatalf("invalid status: %+v %v", result, err)
	}
	// 状态缺失 → stale skip。
	if result, err := s.DeferAccountAPIKeyProbe(ctx, account, ProbeDeferInput{ExpectedStatus: RuntimeTemporaryUnavailable, ExpectedNextProbeAt: "2030-01-01T00:00:00Z", DelaySeconds: 7}); err != nil || result.SkippedReason != "stale_probe_state" {
		t.Fatalf("missing state defer: %+v %v", result, err)
	}
	// 先制造失败状态，再以真实 next_probe_at/updated_at 为期望值顺延。
	if _, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{Status: RuntimeTemporaryUnavailable, ObservedAt: "2030-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	var realNext, realUpdated string
	if err := db.QueryRow(`SELECT next_probe_at,updated_at FROM account_api_key_runtime_states WHERE account_id='acct-1'`).Scan(&realNext, &realUpdated); err != nil {
		t.Fatal(err)
	}
	if result, err := s.DeferAccountAPIKeyProbe(ctx, account, ProbeDeferInput{ExpectedStatus: RuntimeTemporaryUnavailable, ExpectedNextProbeAt: realNext, ExpectedStateUpdatedAt: realUpdated, DelaySeconds: 9, ObservedAt: "2030-01-01T00:00:30Z"}); err != nil || !result.Changed {
		t.Fatalf("defer apply: %+v err=%v", result, err)
	}
	var next string
	if err := db.QueryRow(`SELECT next_probe_at FROM account_api_key_runtime_states WHERE account_id='acct-1'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	// 顺延的 next_probe_at 基于存储时钟（now=00:00:00）+ DelaySeconds。
	if !strings.HasPrefix(next, "2030-01-01T00:00:09") {
		t.Fatalf("顺延 next=%q", next)
	}
	// breakQuotaRecoveryWindow 分支。
	if result, err := s.DeferAccountAPIKeyProbe(ctx, account, ProbeDeferInput{ExpectedStatus: RuntimeTemporaryUnavailable, ExpectedNextProbeAt: next, DelaySeconds: 11, BreakQuotaRecoveryWindow: true, ObservedAt: "2030-01-01T00:00:40Z"}); err != nil || !result.Changed {
		t.Fatalf("break window defer: %+v err=%v", result, err)
	}
}

func TestSuccessInsertAndDisabledGuards(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	seedAccount(t, db, "active")
	account := poolAccount()
	ctx := context.Background()
	// ExpectedStatus disabled → manual_restore_required。
	if result, err := s.RecordAccountAPIKeySuccess(ctx, account, SuccessInput{ExpectedStatus: RuntimeDisabled}); err != nil || result.SkippedReason != "manual_restore_required" {
		t.Fatalf("disabled success: %+v %v", result, err)
	}
	if result, err := s.RecordAccountAPIKeySuccess(ctx, account, SuccessInput{ExpectedStatus: "error"}); err != nil || result.SkippedReason != "manual_restore_required" {
		t.Fatalf("error success: %+v %v", result, err)
	}
	// 状态不存在且未提供期望 → 直接 INSERT active。
	result, err := s.RecordAccountAPIKeySuccess(ctx, account, SuccessInput{ObservedAt: "2030-01-01T00:00:00Z"})
	if err != nil || !result.Changed {
		t.Fatalf("insert success: %+v %v", result, err)
	}
	var successCount int
	if err := db.QueryRow(`SELECT success_count FROM account_api_key_runtime_states WHERE account_id='acct-1'`).Scan(&successCount); err != nil || successCount != 1 {
		t.Fatalf("success count=%d err=%v", successCount, err)
	}
	// 提供期望但状态缺失 → stale skip。
	s2, db2 := wkDeps(t)
	defer db2.Close()
	seedAccount(t, db2, "active")
	if result, err := s2.RecordAccountAPIKeySuccess(ctx, account, SuccessInput{ExpectedStatus: RuntimeTemporaryUnavailable, ExpectedStateUpdatedAt: "2030-01-01T00:00:00Z"}); err != nil || result.SkippedReason != "stale_probe_state" {
		t.Fatalf("stale success: %+v %v", result, err)
	}
	// 非 success 且 status=active → ErrInvalidInput。
	if _, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{Status: RuntimeActive}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("failure with active status err=%v", err)
	}
	// 非法 status。
	if _, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{Status: RuntimeDisabled}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("failure with disabled status err=%v", err)
	}
}

func TestFailureUpdateQuotaRecoveryAndCooldown(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	seedAccount(t, db, "active")
	account := poolAccount()
	ctx := context.Background()
	// quota recovery 模式派生错误码（有序执行以保证幂等护栏可断言）。
	if _, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{Status: RuntimeTemporaryUnavailable, QuotaRecoveryMode: "explicit_reset", ObservedAt: "2030-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{Status: RuntimeTemporaryUnavailable, QuotaRecoveryMode: "generic", ObservedAt: "2030-01-01T00:00:01Z"}); err != nil {
		t.Fatal(err)
	}
	var code string
	// 行为契约：generic 模式的失败观察不得覆盖尚未过期的显式配额冷却
	//（SQL 侧 NOT(?='generic' AND last_error_code IN (...)) 幂等护栏）。
	if err := db.QueryRow(`SELECT last_error_code FROM account_api_key_runtime_states WHERE account_id='acct-1'`).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code != "api_key_quota_explicit_reset" {
		t.Fatalf("generic 观察不应覆盖显式冷却: last code=%q", code)
	}
	// http 状态码派生。
	if _, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{StatusCode: 503, ObservedAt: "2030-01-01T00:00:02Z"}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT last_error_code FROM account_api_key_runtime_states WHERE account_id='acct-1'`).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code != "http_503" {
		t.Fatalf("http code=%q", code)
	}
	// 冷却截止晚于退避。
	if _, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{Status: RuntimeRateLimited, CooldownUntil: "2030-01-01T01:00:00Z", ObservedAt: "2030-01-01T00:00:03Z"}); err != nil {
		t.Fatal(err)
	}
	var cooldown sql.NullString
	if err := db.QueryRow(`SELECT cooldown_until FROM account_api_key_runtime_states WHERE account_id='acct-1'`).Scan(&cooldown); err != nil {
		t.Fatal(err)
	}
	if cooldown.String != "2030-01-01T01:00:00Z" {
		t.Fatalf("cooldown=%q", cooldown.String)
	}
	// 非法冷却时间。
	if _, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{Status: RuntimeRateLimited, CooldownUntil: "bad", ObservedAt: "2030-01-01T00:00:04Z"}); err == nil {
		t.Fatal("非法冷却时间必须失败")
	}
	// generic 模式幂等护栏：显式重置后的 generic 冷却不覆盖。
	if _, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{Status: RuntimeRateLimited, QuotaRecoveryMode: "explicit_reset", CooldownUntil: "2030-01-01T02:00:00Z", ObservedAt: "2030-01-01T00:00:05Z"}); err != nil {
		t.Fatal(err)
	}
	if result, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{Status: RuntimeRateLimited, QuotaRecoveryMode: "generic", CooldownUntil: "2030-01-01T03:00:00Z", ObservedAt: "2030-01-01T00:00:06Z"}); err != nil || result.Changed {
		t.Fatalf("generic 幂等护栏: %+v err=%v", result, err)
	}
	// key_disabled 守卫：手工禁用后写入被跳过。
	if _, err := db.Exec(`UPDATE account_api_key_runtime_states SET status='disabled' WHERE account_id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{Status: RuntimeTemporaryUnavailable, ObservedAt: "2030-01-01T00:00:07Z"}); err != nil || result.SkippedReason != "key_disabled" {
		t.Fatalf("key_disabled 守卫: %+v err=%v", result, err)
	}
}

func TestKeyTargetAndScopeGuards(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	ctx := context.Background()
	// ensureTarget 失败：无密钥池。
	if _, err := s.RecordAccountAPIKeyFailure(ctx, Account{ID: "acct", SystemAccountID: "sys", SelectedKeyFingerprint: "f"}, FailureInput{}); err == nil {
		t.Fatal("非密钥池账户必须失败")
	}
	// updateAccountScoped 校验。
	if _, err := s.updateAccountScoped(ctx, Account{ID: "acct"}, "", nil, "", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("缺 owner err=%v", err)
	}
	if _, err := s.updateAccountScoped(ctx, Account{ID: "acct", SystemAccountID: "sys"}, "", nil, "", nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("缺 revision err=%v", err)
	}
	seedAccount(t, db, "active")
	result, err := s.updateAccountScoped(ctx, Account{ID: "acct-1", SystemAccountID: "sys-1", ConfigRevision: 1}, "status='rate_limited'", []any{}, " AND status='active'", nil)
	if err != nil || !result.Changed {
		t.Fatalf("scoped update=%+v err=%v", result, err)
	}
	// revision 不匹配 → 无变更。
	result, err = s.updateAccountScoped(ctx, Account{ID: "acct-1", SystemAccountID: "sys-1", ConfigRevision: 99}, "status='rate_limited'", []any{}, "", nil)
	if err != nil || result.Changed {
		t.Fatalf("stale scoped=%+v err=%v", result, err)
	}
}

func TestMarkAccountExceptionVariants(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	seedAccount(t, db, "active")
	ctx := context.Background()
	// 输入缺失。
	if _, err := s.MarkAccountException(ctx, ExceptionInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空输入 err=%v", err)
	}
	// 基本 exception。
	if result, err := s.MarkAccountException(ctx, ExceptionInput{AccountID: "acct-1", ErrorCode: "boom", Reason: "r"}); err != nil || !result.Changed {
		t.Fatalf("exception=%+v err=%v", result, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM accounts WHERE id='acct-1'`).Scan(&status); err != nil || status != "error" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	// 已是 error → 无变更。
	if result, err := s.MarkAccountException(ctx, ExceptionInput{AccountID: "acct-1", ErrorCode: "boom"}); err != nil || result.Changed {
		t.Fatalf("重复 exception=%+v err=%v", result, err)
	}
	// preserveDisabled：disabled 保持 disabled。
	if _, err := db.Exec(`UPDATE accounts SET status='disabled' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.MarkAccountExceptionWithOptions(ctx, "acct-1", "boom", "r", "t", AccountErrorHandlingOptions{PreserveDisabled: true}); err != nil || !result.Changed {
		t.Fatalf("preserve disabled=%+v err=%v", result, err)
	}
	if err := db.QueryRow(`SELECT status FROM accounts WHERE id='acct-1'`).Scan(&status); err != nil || status != "disabled" {
		t.Fatalf("preserved status=%s err=%v", status, err)
	}
	// expected status 守卫。
	if _, err := db.Exec(`UPDATE accounts SET status='active' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.MarkAccountException(ctx, ExceptionInput{AccountID: "acct-1", ErrorCode: "boom", ExpectedStatus: "rate_limited"}); err != nil || result.Changed {
		t.Fatalf("expected status 守卫=%+v err=%v", result, err)
	}
	if result, err := s.MarkAccountException(ctx, ExceptionInput{AccountID: "acct-1", ErrorCode: "boom", ExpectedConfigRevision: 42}); err != nil || result.Changed {
		t.Fatalf("expected revision 守卫=%+v err=%v", result, err)
	}
}

func TestMarkPrecheckAndCooldown(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	seedAccount(t, db, "active")
	ctx := context.Background()
	// precheck：非法时间。
	if _, err := s.MarkAccountPrecheckTemporaryUnavailable(ctx, PrecheckTemporaryUnavailableInput{PrecheckStartedAt: "bad"}); err == nil {
		t.Fatal("非法时间必须失败")
	}
	// precheck：非法输入。
	if _, err := s.MarkAccountPrecheckTemporaryUnavailable(ctx, PrecheckTemporaryUnavailableInput{PrecheckStartedAt: "2030-01-01T00:00:00Z"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("缺 account err=%v", err)
	}
	if result, err := s.MarkAccountPrecheckTemporaryUnavailable(ctx, PrecheckTemporaryUnavailableInput{
		Account: Account{ID: "acct-1"}, Reason: "precheck", PrecheckStartedAt: "2030-01-01T00:00:00Z",
		ExpectedDispatchRevision: 1, ExpectedStatus: "active",
	}); err != nil || !result.Changed {
		t.Fatalf("precheck=%+v err=%v", result, err)
	}
	// cooldown：非法输入。
	if _, err := s.MarkAccountCooldown(ctx, Account{}, "until", "reason", RuntimeRateLimited, "t"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("缺 id err=%v", err)
	}
	if _, err := s.MarkAccountCooldown(ctx, Account{ID: "acct-1"}, "  ", "reason", RuntimeRateLimited, "t"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("rate_limited 缺 until err=%v", err)
	}
	if _, err := s.MarkAccountCooldown(ctx, Account{ID: "acct-1"}, "bad-time", "reason", RuntimeRateLimited, "t"); err == nil {
		t.Fatal("非法 until 必须失败")
	}
	// temporary 委派分支。
	if result, err := s.MarkAccountCooldown(ctx, Account{ID: "acct-1"}, "", "cooldown reason", RuntimeTemporaryUnavailable, "t-1"); err != nil || !result.Changed {
		t.Fatalf("cooldown temporary=%+v err=%v", result, err)
	}
	var code string
	if err := db.QueryRow(`SELECT last_error_code FROM accounts WHERE id='acct-1'`).Scan(&code); err != nil || code != "temporary_unavailable" {
		t.Fatalf("code=%s err=%v", code, err)
	}
	// rate_limited 分支。
	if result, err := s.MarkAccountCooldown(ctx, Account{ID: "acct-1"}, "2030-01-01T02:00:00Z", "rate limit", RuntimeRateLimited, "t-2"); err != nil || !result.Changed {
		t.Fatalf("cooldown rate_limited=%+v err=%v", result, err)
	}
	if err := db.QueryRow(`SELECT last_error_code FROM accounts WHERE id='acct-1'`).Scan(&code); err != nil || code != "rate_limited" {
		t.Fatalf("rate code=%s err=%v", code, err)
	}
}

func TestClearFailureStateBranches(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	seedAccount(t, db, "active")
	ctx := context.Background()
	// 空输入。
	if _, err := s.ClearAccountFailureState(ctx, ClearFailureInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空输入 err=%v", err)
	}
	// 不存在的账户 → 空结果无错误。
	if result, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "ghost"}); err != nil || result.Changed || result.SkippedReason != "" {
		t.Fatalf("ghost=%+v err=%v", result, err)
	}
	// expected codes 不匹配。
	if _, err := s.MarkAccountTemporaryUnavailable(ctx, TemporaryUnavailableInput{Account: Account{ID: "acct-1"}, Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1", ExpectedLastErrorCodes: []string{"other"}}); err != nil || result.SkippedReason != "stale_failure_state" {
		t.Fatalf("expected codes=%+v err=%v", result, err)
	}
	// 显式策略冷却 → 需要显式恢复。
	if _, err := db.Exec(`UPDATE accounts SET last_error_code='account_error_policy_cooldown' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1"}); err != nil || result.SkippedReason != "explicit_policy_restore_required" {
		t.Fatalf("policy restore=%+v err=%v", result, err)
	}
	// pending_test 恢复围栏。
	if _, err := db.Exec(`UPDATE accounts SET status='pending_test',last_error_code='' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1"}); err != nil || result.Changed || result.SkippedReason != "" {
		t.Fatalf("pending_test 默认=%+v err=%v", result, err)
	}
	if result, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1", AllowPendingTestRestore: true}); err != nil || !result.Changed {
		t.Fatalf("pending_test 恢复=%+v err=%v", result, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM accounts WHERE id='acct-1'`).Scan(&status); err != nil || status != "pending_test" {
		t.Fatalf("pending_test 保持=%s err=%v", status, err)
	}
	// error 恢复围栏。
	if _, err := db.Exec(`UPDATE accounts SET status='error' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1", AllowPendingTestRestore: true}); err != nil || result.Changed {
		t.Fatalf("error 默认=%+v err=%v", result, err)
	}
	if result, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1", AllowErrorRestore: true}); err != nil || !result.Changed {
		t.Fatalf("error 恢复=%+v err=%v", result, err)
	}
	// 临时不可用 → active 恢复，含 observation 围栏。
	if _, err := s.MarkAccountTemporaryUnavailable(ctx, TemporaryUnavailableInput{Account: Account{ID: "acct-1"}, Reason: "x"}); err != nil {
		t.Fatal(err)
	}
	var observation sql.NullString
	if err := db.QueryRow(`SELECT cooldown_retest_observation_started_at FROM accounts WHERE id='acct-1'`).Scan(&observation); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1", ExpectedCooldownRetestObservationStartedAt: "mismatch"}); err != nil || result.Changed {
		t.Fatalf("observation 围栏=%+v err=%v", result, err)
	}
	if result, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1", ExpectedCooldownRetestObservationStartedAt: observation.String}); err != nil || !result.Changed {
		t.Fatalf("active 恢复=%+v err=%v", result, err)
	}
	// 过期账户 → 自动停用。
	expired := "2029-01-01T00:00:00Z"
	if _, err := db.Exec(`UPDATE accounts SET account_expires_at=?,status='temporary_unavailable' WHERE id='acct-1'`, expired); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1"}); err != nil || !result.Changed {
		t.Fatalf("过期停用=%+v err=%v", result, err)
	}
	if err := db.QueryRow(`SELECT status FROM accounts WHERE id='acct-1'`).Scan(&status); err != nil || status != "disabled" {
		t.Fatalf("过期状态=%s err=%v", status, err)
	}
	// 非法过期时间。
	if _, err := db.Exec(`UPDATE accounts SET account_expires_at='bad-time',status='temporary_unavailable' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearAccountFailureState(ctx, ClearFailureInput{AccountID: "acct-1"}); err == nil {
		t.Fatal("非法过期时间必须失败")
	}
}

func TestStreamFailureActionsAndWindow(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	seedAccount(t, db, "active")
	ctx := context.Background()
	// 输入非法。
	if _, err := s.RecordAccountStreamFailure(ctx, StreamFailureInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空输入 err=%v", err)
	}
	if _, err := s.ClearAccountStreamFailureState(ctx, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空 id err=%v", err)
	}
	// disabled/error 账户只返回计数。
	if _, err := db.Exec(`UPDATE accounts SET status='disabled',stream_failure_count=7 WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.RecordAccountStreamFailure(ctx, StreamFailureInput{AccountID: "acct-1", ThresholdCount: 1, ThresholdWindowMinutes: 5}); err != nil || result.Count != 7 || result.Triggered {
		t.Fatalf("disabled stream=%+v err=%v", result, err)
	}
	if _, err := db.Exec(`UPDATE accounts SET status='active',stream_failure_count=0 WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	// 行为存疑：窗口起点陈旧时计数被重置为 1，但 COALESCE 不重锚
	// stream_failure_window_started_at，窗口起点保持陈旧值（下一次仍会重置）。
	// 当前按实际行为断言，不修改生产代码。
	if _, err := db.Exec(`UPDATE accounts SET stream_failure_count=5,stream_failure_window_started_at='2020-01-01T00:00:00Z' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	result, err := s.RecordAccountStreamFailure(ctx, StreamFailureInput{AccountID: "acct-1", ThresholdCount: 2, ThresholdWindowMinutes: 10})
	if err != nil || result.Count != 1 || result.Triggered {
		t.Fatalf("陈旧窗口=%+v err=%v", result, err)
	}
	var staleWindow sql.NullString
	if err := db.QueryRow(`SELECT stream_failure_window_started_at FROM accounts WHERE id='acct-1'`).Scan(&staleWindow); err != nil {
		t.Fatal(err)
	}
	if staleWindow.String != "2020-01-01T00:00:00Z" {
		t.Fatalf("陈旧窗口起点被重锚: %q", staleWindow.String)
	}
	// 全新窗口：两次失败在窗口内累计并触发 cooldown 动作。
	if _, err := db.Exec(`UPDATE accounts SET stream_failure_count=0,stream_failure_window_started_at=NULL,status='active' WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	result, err = s.RecordAccountStreamFailure(ctx, StreamFailureInput{AccountID: "acct-1", ThresholdCount: 2, ThresholdWindowMinutes: 10})
	if err != nil || result.Count != 1 || result.Triggered {
		t.Fatalf("首笔失败=%+v err=%v", result, err)
	}
	result, err = s.RecordAccountStreamFailure(ctx, StreamFailureInput{AccountID: "acct-1", ThresholdCount: 2, ThresholdWindowMinutes: 10, Action: "cooldown", Reason: "stream broke"})
	if err != nil || !result.Triggered {
		t.Fatalf("cooldown 触发=%+v err=%v", result, err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM accounts WHERE id='acct-1'`).Scan(&status); err != nil || status != "temporary_unavailable" {
		t.Fatalf("cooldown status=%s err=%v", status, err)
	}
	// 清理流失败状态（cooldown 后状态已变更，清理为 no-op 或按条件更新）。
	if _, err := s.ClearAccountStreamFailureState(ctx, "acct-1"); err != nil {
		t.Fatalf("清理流失败: %v", err)
	}
}

func TestApplyAccountErrorHandlingBranches(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	seedAccount(t, db, "active")
	ctx := context.Background()
	// 空 id。
	if _, err := s.ApplyAccountErrorHandling(ctx, Account{}, ErrorHandlingInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("空 id err=%v", err)
	}
	// 成功路径。
	if result, err := s.ApplyAccountErrorHandling(ctx, Account{ID: "acct-1"}, ErrorHandlingInput{Success: true, ObservedAt: "2030-01-01T00:00:00Z"}); err != nil || !result.Changed {
		t.Fatalf("成功路径=%+v err=%v", result, err)
	}
	// 无策略。
	if result, err := s.ApplyAccountErrorHandling(ctx, Account{ID: "acct-1"}, ErrorHandlingInput{}); err != nil || result.SkippedReason != "no_explicit_policy" {
		t.Fatalf("无策略=%+v err=%v", result, err)
	}
	// key scoped 跳过。
	if result, err := s.ApplyAccountErrorHandling(ctx, Account{ID: "acct-1"}, ErrorHandlingInput{PolicyDecision: &ErrorPolicyDecision{KeyScoped: true}}); err != nil || result.SkippedReason != "api_key_key_scoped_quota_recovery" {
		t.Fatalf("key scoped=%+v err=%v", result, err)
	}
	// retry_next / none。
	for _, action := range []string{"retry_next", "retry_next_account", "none"} {
		result, err := s.ApplyAccountErrorHandling(ctx, Account{ID: "acct-1"}, ErrorHandlingInput{PolicyDecision: &ErrorPolicyDecision{Action: action}})
		if err != nil || result.Action != action || result.SkippedReason != "policy_no_account_mutation" {
			t.Fatalf("%s=%+v err=%v", action, result, err)
		}
	}
	// unknown action。
	if result, err := s.ApplyAccountErrorHandling(ctx, Account{ID: "acct-1"}, ErrorHandlingInput{PolicyDecision: &ErrorPolicyDecision{Action: "mystery"}}); err != nil || result.SkippedReason != "unknown_policy_action" {
		t.Fatalf("unknown=%+v err=%v", result, err)
	}
	// cooldown 动作。
	if result, err := s.ApplyAccountErrorHandling(ctx, Account{ID: "acct-1"}, ErrorHandlingInput{
		PolicyDecision: &ErrorPolicyDecision{Action: "cooldown", CooldownUntil: "2030-01-01T05:00:00Z"},
		ErrorMessage:   "policy cooldown", TraceID: "tr-1", ObservedAt: "2030-01-01T00:00:00Z",
	}); err != nil || !result.Changed {
		t.Fatalf("cooldown 动作=%+v err=%v", result, err)
	}
	var cooldown sql.NullString
	var code string
	if err := db.QueryRow(`SELECT cooldown_until,last_error_code FROM accounts WHERE id='acct-1'`).Scan(&cooldown, &code); err != nil {
		t.Fatal(err)
	}
	if cooldown.String != "2030-01-01T05:00:00Z" || code != "account_error_policy" {
		t.Fatalf("cooldown=%q code=%q", cooldown.String, code)
	}
	// disable 动作 → exception。
	if result, err := s.ApplyAccountErrorHandling(ctx, Account{ID: "acct-1"}, ErrorHandlingInput{
		PolicyDecision: &ErrorPolicyDecision{Action: "disable", ErrorCode: "policy_disable"},
		ErrorMessage:   "disable it", TraceID: "tr-2",
	}); err != nil || !result.Changed {
		t.Fatalf("disable 动作=%+v err=%v", result, err)
	}
}

func TestScheduleSyncBranches(t *testing.T) {
	ctx := context.Background()
	// 缺失评估器 fail-closed。
	s, db := wkDeps(t)
	defer db.Close()
	if _, err := s.SyncAPIKeyAvailabilityScheduleStatuses(ctx); !errors.Is(err, ErrOutstandingScheduleEvaluator) {
		t.Fatalf("缺失评估器 err=%v", err)
	}
	if _, err := s.SyncApiKeyAvailabilityScheduleStatuses(ctx); !errors.Is(err, ErrOutstandingScheduleEvaluator) {
		t.Fatalf("别名转发 err=%v", err)
	}
	// 评估器失败。
	s2, db2 := wkDeps(t, Dependencies{Schedule: wkFailSchedule{}})
	defer db2.Close()
	if _, err := db2.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,availability_schedule_json) VALUES ('k1','sys-1','r','h','active','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.SyncAPIKeyAvailabilityScheduleStatuses(ctx); err == nil {
		t.Fatal("评估失败必须透出")
	}
	// 正常评估：状态变更 + 事件幂等 + next 缺省回落。
	evaluated := false
	deps := Dependencies{Schedule: ScheduleEvaluatorFunc(func(_ context.Context, _ string, _ time.Time) (ScheduleDecision, error) {
		if evaluated {
			return ScheduleDecision{Status: "disabled", EventKey: "close"}, nil
		}
		evaluated = true
		return ScheduleDecision{Status: "disabled", EventKey: "close"}, nil
	})}
	s3, db3 := wkDeps(t, deps)
	defer db3.Close()
	if _, err := db3.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,availability_schedule_json) VALUES ('k2','sys-1','r','h','active','{}')`); err != nil {
		t.Fatal(err)
	}
	first, err := s3.SyncAPIKeyAvailabilityScheduleStatuses(ctx)
	if err != nil || !first.Changed || first.Count != 1 {
		t.Fatalf("first sync=%+v err=%v", first, err)
	}
	// 同 event key 幂等：第二次事件插入被忽略，状态相同不更新 → Changed=false。
	second, err := s3.SyncAPIKeyAvailabilityScheduleStatuses(ctx)
	if err != nil || second.Changed || second.Count != 0 {
		t.Fatalf("second sync=%+v err=%v", second, err)
	}
	// 空状态：只推进 next check。
	depsEmpty := Dependencies{Schedule: ScheduleEvaluatorFunc(func(context.Context, string, time.Time) (ScheduleDecision, error) {
		return ScheduleDecision{}, nil
	})}
	s4, db4 := wkDeps(t, depsEmpty)
	defer db4.Close()
	if _, err := db4.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,availability_schedule_json) VALUES ('k3','sys-1','r','h','active','{}')`); err != nil {
		t.Fatal(err)
	}
	empty, err := s4.SyncAPIKeyAvailabilityScheduleStatuses(ctx)
	if err != nil || !empty.Changed || empty.Count != 1 {
		t.Fatalf("empty sync=%+v err=%v", empty, err)
	}
}

func TestProbeDueForAliasDelegates(t *testing.T) {
	resolver := CredentialResolverFunc(func(context.Context, Account) ([]APIKeyEntry, error) {
		return []APIKeyEntry{{Key: "sk-one", Fingerprint: hashKey("sk-one"), Index: 0}}, nil
	})
	s, db := wkDeps(t, Dependencies{Credentials: resolver})
	defer db.Close()
	seedAccount(t, db, "active")
	// 别名转发且缺失凭据时 ErrOutstanding。
	noResolver, noDB := wkDeps(t)
	defer noDB.Close()
	seedAccount(t, noDB, "active")
	if _, err := noResolver.ListAccountApiKeyRuntimeStatesDueForProbe(context.Background(), 1); !errors.Is(err, ErrOutstandingCredentialResolver) {
		t.Fatalf("别名 resolver err=%v", err)
	}
	// 正常路径别名（带运行态）。
	if _, err := db.Exec(`INSERT INTO account_api_key_runtime_states(id,system_account_id,account_id,key_fingerprint,key_index,status,next_probe_at,created_at,updated_at) VALUES('st-1','sys-1','acct-1',?,0,'temporary_unavailable','2029-01-01T00:00:00Z','2029-01-01T00:00:00Z','2029-01-01T00:00:00Z')`, hashKey("sk-one")); err != nil {
		t.Fatal(err)
	}
	candidates, err := s.ListAccountApiKeyRuntimeStatesDueForProbe(context.Background(), 1)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	if _, err := s.RecordAccountApiKeyFailure(context.Background(), poolAccount(), FailureInput{ObservedAt: "2030-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("failure alias: %v", err)
	}
	if _, err := s.RecordAccountApiKeySuccess(context.Background(), poolAccount(), SuccessInput{ObservedAt: "2030-01-01T00:00:01Z"}); err != nil {
		t.Fatalf("success alias: %v", err)
	}
	if _, err := s.DeferAccountApiKeyProbe(context.Background(), poolAccount(), ProbeDeferInput{ExpectedStatus: RuntimeActive, ExpectedNextProbeAt: "bad"}); err != nil {
		t.Fatalf("defer alias: %v", err)
	}
}

func TestValidateGatewayAPIKeyExpiryAndImageFlag(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	seedAccount(t, db, "active")
	if _, err := db.Exec(`INSERT INTO route_strategies(id,system_account_id,mode,status) VALUES('route-1','sys-1','normal','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups(id,system_account_id,provider_code,enabled) VALUES('group-1','sys-1','openai',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO route_strategy_groups(id,route_strategy_id,system_account_id,group_id,priority,weight,status,created_at) VALUES('b-1','route-1','sys-1','group-1',1,1,'active','2029-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE system_accounts SET image_generation_enabled=1 WHERE id='sys-1'`); err != nil {
		t.Fatal(err)
	}
	// 未过期 key 正常返回并携带 image 开关。
	if _, err := db.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,expires_at) VALUES('k-exp','sys-1','route-1',?,'active','2099-01-01T00:00:00Z')`, hashKey("sk-future")); err != nil {
		t.Fatal(err)
	}
	row, err := s.ValidateGatewayAPIKey(context.Background(), "sk-future")
	if err != nil || !row.SystemAccountImageGenerationEnabled || row.ExpiresAt == "" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	// 已过期 key → ErrNoRows。
	if _, err := db.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,expires_at) VALUES('k-old','sys-1','route-1',?,'active','2020-01-01T00:00:00Z')`, hashKey("sk-old")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateGatewayAPIKey(context.Background(), "sk-old"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("过期 key err=%v", err)
	}
	// 非法过期格式 → 错误。
	if _, err := db.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,expires_at) VALUES('k-bad','sys-1','route-1',?,'active','bad-time')`, hashKey("sk-bad")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateGatewayAPIKey(context.Background(), "sk-bad"); err == nil || !strings.Contains(err.Error(), "invalid gateway API key expiry") {
		t.Fatalf("非法过期 err=%v", err)
	}
}

func TestMarkAccountTestTemporaryUnavailableBranches(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	seedAccount(t, db, "active")
	ctx := context.Background()
	account := poolAccount()
	// revision 非法。
	if _, err := s.MarkAccountTestTemporaryUnavailable(ctx, account, "reason", "t", 0, "2030-01-01T00:00:00Z", 0, "2030-01-01T00:00:01Z"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("revision<1 err=%v", err)
	}
	// failureCount 非法。
	if _, err := s.MarkAccountTestTemporaryUnavailable(ctx, account, "reason", "t", 1, "2030-01-01T00:00:00Z", -1, "2030-01-01T00:00:01Z"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("failureCount<0 err=%v", err)
	}
	// 时间非法。
	if _, err := s.MarkAccountTestTemporaryUnavailable(ctx, account, "reason", "t", 1, "bad", 0, "2030-01-01T00:00:01Z"); err == nil {
		t.Fatal("非法 checkedAt 必须失败")
	}
	if _, err := s.MarkAccountTestTemporaryUnavailable(ctx, account, "reason", "t", 1, "2030-01-01T00:00:00Z", 0, "bad"); err == nil {
		t.Fatal("非法 observedAt 必须失败")
	}
	// 状态不在允许集合 → no-op。
	disabled := account
	disabled.Status = "disabled"
	if result, err := s.MarkAccountTestTemporaryUnavailable(ctx, disabled, "reason", "t", 1, "2030-01-01T00:00:00Z", 0, "2030-01-01T00:00:01Z"); err != nil || result.Changed {
		t.Fatalf("disabled 账户=%+v err=%v", result, err)
	}
	// 成功路径：预置与健康检查期望一致的行；账户状态须显式为 active。
	if _, err := db.Exec(`UPDATE accounts SET last_health_check_at='2030-01-01T00:00:00Z',health_check_failure_count=2 WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	account.Status = "active"
	result, err := s.MarkAccountTestTemporaryUnavailable(ctx, account, "health degraded", "trace-1", 1, "2030-01-01T00:00:00Z", 2, "2030-01-01T00:00:01Z")
	if err != nil || !result.Changed {
		t.Fatalf("test unavailable=%+v err=%v", result, err)
	}
	var code string
	if err := db.QueryRow(`SELECT last_error_code FROM accounts WHERE id='acct-1'`).Scan(&code); err != nil || code != "test_temporary_unavailable" {
		t.Fatalf("code=%s err=%v", code, err)
	}
	// status 期望守卫：状态已变更后旧期望不再命中。
	stale := account
	stale.Status = "active"
	if result, err := s.MarkAccountTestTemporaryUnavailable(ctx, stale, "again", "t", 1, "2030-01-01T00:00:05Z", 0, "2030-01-01T00:00:06Z"); err != nil || result.Changed {
		t.Fatalf("stale 期望=%+v err=%v", result, err)
	}
}

func TestSuccessWithClaimTokenAndProbeExpectations(t *testing.T) {
	s, db := wkDeps(t)
	defer db.Close()
	seedAccount(t, db, "active")
	account := poolAccount()
	ctx := context.Background()
	if _, err := s.RecordAccountAPIKeyFailure(ctx, account, FailureInput{Status: RuntimeTemporaryUnavailable, ObservedAt: "2030-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	var updatedAt, next string
	if err := db.QueryRow(`SELECT updated_at,COALESCE(next_probe_at,'') FROM account_api_key_runtime_states WHERE account_id='acct-1'`).Scan(&updatedAt, &next); err != nil {
		t.Fatal(err)
	}
	// ExpectedProbeClaimToken 非空但库中为 NULL → 期望子句不命中 → stale skip。
	result, err := s.RecordAccountAPIKeySuccess(ctx, account, SuccessInput{
		ExpectedStatus: RuntimeTemporaryUnavailable, ExpectedNextProbeAt: next,
		ExpectedStateUpdatedAt: updatedAt, ExpectedProbeClaimToken: "stale-token",
	})
	if err != nil || result.Changed || result.SkippedReason != "stale_probe_state" {
		t.Fatalf("claim token 守卫=%+v err=%v", result, err)
	}
	// ExpectedNextProbeAt 不匹配 → stale skip。
	if result, err := s.RecordAccountAPIKeySuccess(ctx, account, SuccessInput{
		ExpectedStatus: RuntimeTemporaryUnavailable, ExpectedNextProbeAt: "1999-01-01T00:00:00Z",
		ExpectedStateUpdatedAt: updatedAt,
	}); err != nil || result.Changed || result.SkippedReason != "stale_probe_state" {
		t.Fatalf("next probe 期望守卫=%+v err=%v", result, err)
	}
	// 完整期望命中 → 成功更新。
	if result, err := s.RecordAccountAPIKeySuccess(ctx, account, SuccessInput{
		ExpectedStatus: RuntimeTemporaryUnavailable, ExpectedNextProbeAt: next,
		ExpectedStateUpdatedAt: updatedAt, ObservedAt: "2030-01-01T00:00:05Z",
	}); err != nil || !result.Changed {
		t.Fatalf("成功更新=%+v err=%v", result, err)
	}
}

func TestProbeDueLeaseConflictAndLimitBounds(t *testing.T) {
	resolver := CredentialResolverFunc(func(context.Context, Account) ([]APIKeyEntry, error) {
		return []APIKeyEntry{{Key: "sk-one", Fingerprint: hashKey("sk-one"), Index: 0}}, nil
	})
	s, db := wkDeps(t, Dependencies{Credentials: resolver})
	defer db.Close()
	seedAccount(t, db, "active")
	if _, err := db.Exec(`INSERT INTO account_api_key_runtime_states(id,system_account_id,account_id,key_fingerprint,key_index,status,next_probe_at,created_at,updated_at) VALUES('st-2','sys-1','acct-1',?,0,'rate_limited','2029-01-01T00:00:00Z','2029-01-01T00:00:00Z','2029-01-01T00:00:00Z')`, hashKey("sk-one")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := s.ListAccountAPIKeyRuntimeStatesDueForProbe(ctx, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	// 认领租约未到期 → 二次扫描为空。
	second, err := s.ListAccountAPIKeyRuntimeStatesDueForProbe(ctx, 10)
	if err != nil || len(second) != 0 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	// 账户不可调度或已删除 → 候选被过滤。
	if _, err := db.Exec(`UPDATE accounts SET schedulable=0 WHERE id='acct-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE account_api_key_runtime_states SET probe_claimed_until=NULL WHERE id='st-2'`); err != nil {
		t.Fatal(err)
	}
	third, err := s.ListAccountAPIKeyRuntimeStatesDueForProbe(ctx, 10)
	if err != nil || len(third) != 0 {
		t.Fatalf("不可调度过滤=%+v err=%v", third, err)
	}
}
