// 波次 w13g8：accounthealth 剩余臂批测（选取与既有 w9h/w12d/w13g5 波次
// 不重叠的主题）：
//   - direct_input.go 纯函数臂：directProbeTarget 失败、decryptJSONObject
//     解封/解析失败、validateDirectAuthorization 校验臂、EncryptV1Envelope
//     空 secret、collectDirectCandidatePagesWithCap 页界校验、
//     directInputScanCap 上限臂；
//   - direct_input_reader.go PG 辅助臂：loadDirectSchedule 设置校验
//     （回滚事务内注入坏 settings，不污染 w1cover）、loadDirectQuotaCosts
//     的 ErrNoRows/小时窗路径、authorizationQuotaEligible 的 limits 解析臂；
//   - store.go：EnsureSchema 的 PRAGMA/ALTER 注入失败臂（复用 w13g5 注入
//     kit）、AcquireOwnerLease 参数校验、nil store Close；
//   - outbox_drain.go：claim 错误透传与 deadline 缺失收敛臂（stub store）。
//
// PG 门禁连接复用 w9hPgDB（w1cover 覆盖库），数据 ID 全部 w13g8- 前缀，
// 注入的坏 settings 位于回滚事务内、不落库。
package accounthealth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestW13G8DirectProbeTargetArms 驱动 directProbeTarget 的 hybrid 解析失败臂。
func TestW13G8DirectProbeTargetArms(t *testing.T) {
	// hybrid profile 缺模型映射 → ResolveHybridProbeTarget 失败。
	if _, _, err := directProbeTarget(DirectAccount{ProtocolProfileID: "profile_hybrid_openai_chat_v1", EndpointMode: "chat_json", MappedUpstreamModel: "", MappedUpstreamEndpointFamily: ""}); err == nil || !strings.Contains(err.Error(), "PG direct input") {
		t.Fatalf("hybrid 缺模型映射必须报错: %v", err)
	}
	// 合法 profile → 成功路径。
	mode, model, err := directProbeTarget(DirectAccount{ProtocolProfileID: "profile_gpt_openai_v1", EndpointMode: "responses_json", HealthModel: "gpt-w13g8"})
	if err != nil || mode == "" || model == "" {
		t.Fatalf("合法 profile 必须解析成功: %q %q %v", mode, model, err)
	}
}

// TestW13G8DecryptJSONObjectArms 驱动 decryptJSONObject 的解封与解析失败臂。
func TestW13G8DecryptJSONObjectArms(t *testing.T) {
	if _, err := decryptJSONObject("w13g8-secret", "w13g8-not-an-envelope", "proxy "); err == nil || !strings.Contains(err.Error(), "解封 PG direct input") {
		t.Fatalf("坏 envelope 必须报解封错误: %v", err)
	}
	envelope, err := EncryptV1Envelope("w13g8-secret", []byte("w13g8-not-json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decryptJSONObject("w13g8-secret", envelope, "proxy "); err == nil || !strings.Contains(err.Error(), "解析 PG direct input") {
		t.Fatalf("非 JSON 明文必须报解析错误: %v", err)
	}
}

// TestW13G8ValidateDirectAuthorizationArms 驱动 validateDirectAuthorization
// 的全部校验失败臂。
func TestW13G8ValidateDirectAuthorizationArms(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	validAccount := DirectAccount{ID: "w13g8-acc", ConfigRevision: 1, DispatchRevision: 1, Type: "api_key", Provider: "gpt", ProtocolProfileID: "profile_gpt_openai_v1", EndpointMode: "responses_json", Status: "active", Schedulable: true}
	validSource := func() *DirectSource {
		credentials, err := EncryptV1Envelope("w13g8-secret", []byte(`{"api_key":"sk-w13g8"}`))
		if err != nil {
			t.Fatal(err)
		}
		return &DirectSource{ID: "w13g8-src", ConfigRevision: 1, Provider: "gpt", ProtocolProfileID: "profile_gpt_openai_v1", Type: "api_key", Status: "active", Schedulable: true, CredentialsEncrypted: credentials}
	}
	validAuthorization := func() *DirectAuthorization {
		return &DirectAuthorization{ID: "w13g8-auth", Status: "active", QuotaEligible: true}
	}
	// 授权/来源缺失。
	if err := validateDirectAuthorization(DirectInput{Account: validAccount}, now); err == nil || !strings.Contains(err.Error(), "authorization/source") {
		t.Fatalf("缺 authorization 必须报错: %v", err)
	}
	// 授权不可用（状态/配额/过期）。
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: &DirectAuthorization{ID: "w13g8-auth", Status: "inactive", QuotaEligible: true}, Source: validSource(), Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil || !strings.Contains(err.Error(), "授权不可用") {
		t.Fatalf("inactive 授权必须报错: %v", err)
	}
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: &DirectAuthorization{ID: "w13g8-auth", Status: "active", QuotaEligible: false}, Source: validSource(), Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil {
		t.Fatal("配额不可用授权必须报错")
	}
	expired := now.Add(-time.Hour)
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: &DirectAuthorization{ID: "w13g8-auth", Status: "active", QuotaEligible: true, ExpiresAt: &expired}, Source: validSource(), Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil {
		t.Fatal("过期授权必须报错")
	}
	// binding 不匹配。
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: validAuthorization(), Source: validSource(), Binding: DirectBinding{AuthorizationBindingID: "w13g8-other"}}, now); err == nil || !strings.Contains(err.Error(), "binding 不匹配") {
		t.Fatalf("binding 不匹配必须报错: %v", err)
	}
	// 物理来源不可用（缺凭据）。
	badSource := validSource()
	badSource.CredentialsEncrypted = ""
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: validAuthorization(), Source: badSource, Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil || !strings.Contains(err.Error(), "物理来源账户不可用") {
		t.Fatalf("缺凭据来源必须报错: %v", err)
	}
	// 物理来源已到期。
	expiredSource := validSource()
	expiredSource.AccountExpiresAt = &expired
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: validAuthorization(), Source: expiredSource, Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil || !strings.Contains(err.Error(), "已到期") {
		t.Fatalf("到期来源必须报错: %v", err)
	}
	// 物理来源冷却中。
	cooldown := now.Add(time.Hour)
	cooldownSource := validSource()
	cooldownSource.CooldownUntil = &cooldown
	if err := validateDirectAuthorization(DirectInput{Account: validAccount, Authorization: validAuthorization(), Source: cooldownSource, Binding: DirectBinding{AuthorizationBindingID: "w13g8-auth"}}, now); err == nil || !strings.Contains(err.Error(), "冷却") {
		t.Fatalf("冷却来源必须报错: %v", err)
	}
}

// TestW13G8EncryptV1EnvelopeEmptySecret 驱动 EncryptV1Envelope 的空 secret 臂。
func TestW13G8EncryptV1EnvelopeEmptySecret(t *testing.T) {
	if _, err := EncryptV1Envelope("  ", []byte("w13g8")); err == nil || !strings.Contains(err.Error(), "secret 不能为空") {
		t.Fatalf("空 secret 必须报错: %v", err)
	}
}

// TestW13G8CollectDirectCandidatePagesArms 驱动 collectDirectCandidatePagesWithCap
// 的 limit 校验、页行数校验与错误透传臂。
func TestW13G8CollectDirectCandidatePagesArms(t *testing.T) {
	// limit 越界。
	if err := collectDirectCandidatePagesWithCap(0, 16, func(int) (int, error) { return 0, nil }, func() int { return 0 }); err == nil {
		t.Fatal("limit=0 必须报错")
	}
	if err := collectDirectCandidatePagesWithCap(maxJ1Capacity+1, 16, func(int) (int, error) { return 0, nil }, func() int { return 0 }); err == nil {
		t.Fatal("limit 越上限必须报错")
	}
	// loadPage 错误透传。
	if err := collectDirectCandidatePagesWithCap(4, 16, func(int) (int, error) { return 0, errW13G8Injected }, func() int { return 0 }); err == nil {
		t.Fatal("loadPage 错误必须透传")
	}
	// 页行数非法（>limit 与 <0）。
	if err := collectDirectCandidatePagesWithCap(4, 16, func(int) (int, error) { return 5, nil }, func() int { return 0 }); err == nil || !strings.Contains(err.Error(), "页行数无效") {
		t.Fatalf("页行数超限必须报错: %v", err)
	}
	if err := collectDirectCandidatePagesWithCap(4, 16, func(int) (int, error) { return -1, nil }, func() int { return 0 }); err == nil || !strings.Contains(err.Error(), "页行数无效") {
		t.Fatalf("负页行数必须报错: %v", err)
	}
	// 单页不足 → 提前收口（成功路径）。
	if err := collectDirectCandidatePagesWithCap(4, 16, func(int) (int, error) { return 2, nil }, func() int { return 0 }); err != nil {
		t.Fatalf("单页不足必须成功收口: %v", err)
	}
}

// TestW13G8DirectInputScanCapUpperBound 驱动 directInputScanCap 的
// maxDirectInputScanCandidates 上限臂（limit > 318 时 16 倍窗口越过 5096）。
func TestW13G8DirectInputScanCapUpperBound(t *testing.T) {
	if got := directInputScanCap(400); got != maxDirectInputScanCandidates {
		t.Fatalf("limit=400 的扫描上限必须收敛到 %d: %d", maxDirectInputScanCandidates, got)
	}
}

// TestW13G8MustJSONContract 固定 mustJSON 的契约（string 序列化不会 panic，
// panic 分支为防御性不可达）。
func TestW13G8MustJSONContract(t *testing.T) {
	if got := mustJSON("w13g8"); got != `"w13g8"` {
		t.Fatalf("mustJSON 必须输出 JSON 字符串: %s", got)
	}
}

// TestW13G8DirectScheduleSettingArms 在回滚事务内注入坏 settings，驱动
// loadDirectSchedule/directSettingInt 的缺设置、无效设置与时区臂（不污染
// w1cover 的 sys_admin 行）。
func TestW13G8DirectScheduleSettingArms(t *testing.T) {
	db := w9hPgDB(t)
	ctx := context.Background()
	seed := func(t *testing.T, values map[string]string) *sql.Tx {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range values {
			if _, err := tx.ExecContext(ctx, `INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at)
				VALUES ('sys_admin', $1, $2, '2026-09-18T00:00:00Z') ON CONFLICT (system_account_id, key) DO UPDATE SET value_json = EXCLUDED.value_json`, key, value); err != nil {
				t.Fatal(err)
			}
		}
		return tx
	}
	// 事务内先清空 settings 行（回滚恢复，不落库）→ directSettingInt 缺设置臂。
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM juhe_business.system_settings WHERE system_account_id = 'sys_admin'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadDirectSchedule(ctx, tx); err == nil || !strings.Contains(err.Error(), "缺少系统设置") {
		t.Fatalf("缺 settings 必须报错: %v", err)
	}
	_ = tx.Rollback()
	// 无效整数 → directSettingInt 无效臂。
	tx = seed(t, map[string]string{"accountHealthCheckIntervalHours": `"w13g8"`})
	if _, _, err := loadDirectSchedule(ctx, tx); err == nil || !strings.Contains(err.Error(), "系统设置 accountHealthCheckIntervalHours 无效") {
		t.Fatalf("无效 interval 必须报错: %v", err)
	}
	_ = tx.Rollback()
	// 缺少有效 usageStatsTimezone → 时区臂。
	tx = seed(t, map[string]string{"usageStatsTimezone": `"  "`})
	if _, _, err := loadDirectSchedule(ctx, tx); err == nil || !strings.Contains(err.Error(), "usageStatsTimezone") {
		t.Fatalf("空时区必须报错: %v", err)
	}
	_ = tx.Rollback()
	// 非法时区值 → LoadLocation 失败臂。
	tx = seed(t, map[string]string{"usageStatsTimezone": `"w13g8/Not-A-Zone"`})
	if _, _, err := loadDirectSchedule(ctx, tx); err == nil || !strings.Contains(err.Error(), "usageStatsTimezone 无效") {
		t.Fatalf("非法时区必须报错: %v", err)
	}
	_ = tx.Rollback()
}

// TestW13G8DirectQuotaCostsArms 驱动 loadDirectQuotaCosts 的 ErrNoRows 零值
// 路径与小时窗查询臂（w1cover 上无 w13g8- 行 → 全部 ErrNoRows）。
func TestW13G8DirectQuotaCostsArms(t *testing.T) {
	db := w9hPgDB(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	location := time.UTC
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	limits, err := ParseDirectQuotaLimits(`{"monthly":{"enabled":true,"limit":10},"hourly":{"enabled":true,"limit":5,"hours":1}}`)
	if err != nil {
		t.Fatalf("limits 解析必须成功: %v", err)
	}
	costs, err := loadDirectQuotaCosts(ctx, tx, "w13g8-sys", "account_authorization", "w13g8-auth", limits, now, location)
	if err != nil {
		t.Fatalf("ErrNoRows 路径必须返回零值成本: %v", err)
	}
	if costs.Total != 0 || costs.Daily != 0 || costs.Weekly != 0 || costs.Monthly != 0 || costs.Hourly != 0 {
		t.Fatalf("无行时成本必须为零: %+v", costs)
	}
}

// TestW13G8AuthorizationQuotaEligibleArms 驱动 authorizationQuotaEligible 的
// nil 授权短路与 limits 解析失败臂。
func TestW13G8AuthorizationQuotaEligibleArms(t *testing.T) {
	db := w9hPgDB(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	// nil 授权 → 直接 true。
	eligible, err := authorizationQuotaEligible(ctx, tx, directCandidate{}, now, time.UTC)
	if err != nil || !eligible {
		t.Fatalf("nil 授权必须 true,nil: %v %v", eligible, err)
	}
	// 非法 limits json → ParseDirectQuotaLimits 失败臂。
	candidate := directCandidate{authorization: &DirectAuthorization{ID: "w13g8-auth"}, authorizationLimits: "w13g8-not-json"}
	if _, err := authorizationQuotaEligible(ctx, tx, candidate, now, time.UTC); err == nil {
		t.Fatal("非法 limits json 必须报错")
	}
}

// TestW13G8StoreInjectionArms 复用 w13g5 注入 kit 驱动 EnsureSchema 的
// PRAGMA/ALTER 失败臂与 AcquireOwnerLease 参数校验。
func TestW13G8StoreInjectionArms(t *testing.T) {
	store, spec := w13g5HealthInjectStore(t)
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("基线 EnsureSchema 必须成功: %v", err)
	}
	// PRAGMA table_info 失败 → 读 state schema 失败臂。
	spec.arm("PRAGMA table_info")
	if err := store.EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "state schema") {
		t.Fatalf("PRAGMA 注入必须报 schema 读取错误: %v", err)
	}
	spec.disarm()
	// ALTER TABLE 失败 → 扩展列失败臂：预置旧形状 current_state 表（缺新列），
	// EnsureSchema 的 CREATE IF NOT EXISTS 空转后触发列扩展。
	legacyStore, legacySpec := w13g5HealthInjectStore(t)
	if _, err := legacyStore.db.ExecContext(ctx, `CREATE TABLE account_health_current_state (
		account_id TEXT PRIMARY KEY, outcome_id TEXT, outcome TEXT, observed_at TEXT,
		input_version INTEGER, config_revision INTEGER, dispatch_revision INTEGER,
		status_code INTEGER, error_code TEXT, error_message TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	legacySpec.arm("ALTER TABLE account_health_current_state ADD COLUMN")
	if err := legacyStore.EnsureSchema(ctx); err == nil || !strings.Contains(err.Error(), "扩展 account-health sqlite state 列") {
		t.Fatalf("ALTER 注入必须报扩展列错误: %v", err)
	}
	legacySpec.disarm()
	// AcquireOwnerLease 参数校验臂。
	if _, _, err := store.AcquireOwnerLease(ctx, "  ", time.Minute); err == nil || !strings.Contains(err.Error(), "参数无效") {
		t.Fatalf("空 ownerID 必须报错: %v", err)
	}
	if _, _, err := store.AcquireOwnerLease(ctx, "w13g8-owner", 0); err == nil || !strings.Contains(err.Error(), "参数无效") {
		t.Fatalf("零时长必须报错: %v", err)
	}
	// nil db 的 Close 短路臂。
	empty := &Store{}
	if err := empty.Close(); err != nil {
		t.Fatalf("nil db Close 必须返回 nil: %v", err)
	}
}

// w13g8StubOutboxStore 是 ProbeRequestOutboxStore 的 stub（错误注入与
// deadline 缺失行）。
type w13g8StubOutboxStore struct {
	rows []ProbeOutboxRow
	err  error
}

func (s *w13g8StubOutboxStore) ClaimPendingProbeRequests(context.Context, int, time.Time) ([]ProbeOutboxRow, error) {
	return s.rows, s.err
}

func (s *w13g8StubOutboxStore) CompleteProbeRequest(context.Context, string, time.Time) (bool, error) {
	return true, nil
}

// TestW13G8OutboxDrainArms 驱动 drainProbeRequestOutbox 的 claim 错误透传臂。
func TestW13G8OutboxDrainArms(t *testing.T) {
	runner := NewRunner(Config{Now: func() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) }}, nil, nil)
	runner.SetProbeRequestDrain(&ProbeRequestDrain{
		Store:    &w13g8StubOutboxStore{err: errW13G8Injected},
		Boundary: w13g8StubBoundary{},
	})
	if err := runner.drainProbeRequestOutbox(context.Background(), OwnerLease{}); err == nil {
		t.Fatal("claim 错误必须透传")
	}
}

// w13g8StubBoundary 是 ProbeRequestBoundary 的空 stub。
type w13g8StubBoundary struct{}

func (w13g8StubBoundary) CurrentProbeInput(context.Context, string) (int64, int64, int64, bool, error) {
	return 0, 0, 0, false, nil
}

// errW13G8Injected 是本文件的固定注入错误。
var errW13G8Injected = errors.New("w13g8 注入失败")
