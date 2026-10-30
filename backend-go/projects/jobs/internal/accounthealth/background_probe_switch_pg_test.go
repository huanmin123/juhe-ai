package accounthealth

// 后台自动探测总开关（PLAN-20261030T062141000Z W2）的 J1 门禁回归：
//   - 候选 SQL 过滤：flag=0 账户与「实例 flag=1 但来源 flag=0」不出现在
//     周期候选与显式 LoadAccount；LoadAccountConfigRevision 返回生效开关；
//   - 派发前守卫：flag=0 时零上游请求、不写 current_state，显式请求按
//     probe_skipped_disabled 终态消费；
//   - outbox 消费守卫：关闭账户的显式请求行按终态出队并结算 fence=unknown；
//   - 定时路径拒绝诊断按账户限频（10 分钟窗口）。
// PG 门禁走 w9hOpenKitDB 的 pgtestkit 隔离测试库（accounts DDL 含
// background_probe_enabled 权威列），fresh 库随测试结束删除。

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

const w2bFixtureNow = "2026-10-30T00:00:00Z"

const w2bSecret = "w2b-credential-secret"

// w2bSeedAccountStatus 把已种账户置为指定生效开关（默认 1 不需要调用）。
func w2bSetProbeSwitch(t *testing.T, db *sql.DB, accountID string, enabled bool) {
	t.Helper()
	value := 0
	if enabled {
		value = 1
	}
	w9hMustExec(t, db, `UPDATE juhe_business.accounts SET background_probe_enabled = $1 WHERE id = $2`, value, accountID)
}

// w2bSeedInstanceChain 建来源 + 授权 + 实例（w16fSeedAuthorizationChain 的
// 参数化同构；FK 顺序：team → 授权 → 实例 → 绑定改写）。
func w2bSeedInstanceChain(t *testing.T, db *sql.DB, team, sourceID, authID, instanceID string) {
	t.Helper()
	w9hMustExec(t, db, `INSERT INTO juhe_business.system_teams (id, name, status, created_by, created_at, updated_at)
		VALUES ($1, $1, 'active', 'w2b', $2, $2) ON CONFLICT (id) DO NOTHING`, team, w2bFixtureNow)
	w9hSeedBusinessAccount(t, db, sourceID, "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w9hMustExec(t, db, `INSERT INTO juhe_business.resource_authorizations (
		id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
		effective_source_type, effective_source_team_id, activated_at, limits_json, created_by, created_at, updated_at
	) VALUES ($2, 'account', $3, 'sys_admin', 'sys_admin', 'health_check', 'active',
		'direct', $1, $4, NULL, 'w2b', $4, $4)
	ON CONFLICT (id) DO NOTHING`, team, authID, sourceID, w2bFixtureNow)
	w9hSeedBusinessAccount(t, db, instanceID, "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json",
		sql.NullString{String: sourceID, Valid: true}, sql.NullString{String: authID, Valid: true})
	w9hMustExec(t, db, `UPDATE juhe_business.group_accounts SET account_authorization_id = $2 WHERE account_id = $1`, instanceID, authID)
}

// TestW2BackgroundProbeCandidateFilterPG 断言候选 SQL 的生效开关过滤与
// LoadAccountConfigRevision 的生效语义（实例跟随来源）。
func TestW2BackgroundProbeCandidateFilterPG(t *testing.T) {
	db, _ := w9hOpenKitDB(t)
	ctx := context.Background()
	w9hSeedBusinessAccount(t, db, "w2b-on", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w9hSeedBusinessAccount(t, db, "w2b-off", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w2bSetProbeSwitch(t, db, "w2b-off", false)
	w2bSeedInstanceChain(t, db, "w2b-team", "w2b-src", "w2b-auth", "w2b-inst")
	w2bSetProbeSwitch(t, db, "w2b-src", false)
	// 实例行仍显式为 1（模拟传播滞后/存量行），生效值必须跟随来源收敛为关。
	w2bSetProbeSwitch(t, db, "w2b-inst", true)

	now := func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	// reader secret 与 w9hSeedBusinessAccount 的凭据 envelope secret 对齐。
	reader, err := NewPostgresDirectInputReader(db, "w9h-credential-secret", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}

	result, err := reader.LoadDueWithFailures(ctx, 10)
	if err != nil {
		t.Fatalf("load 周期候选必须成功: %v", err)
	}
	for _, input := range result.Inputs {
		if input.AccountID == "w2b-off" || input.AccountID == "w2b-inst" {
			t.Fatalf("关闭账户 %s 不得进入周期候选: %#v", input.AccountID, result.Inputs)
		}
	}
	for _, failure := range result.Failures {
		if failure.AccountID == "w2b-off" || failure.AccountID == "w2b-inst" {
			t.Fatalf("开关过滤不是候选构造失败: %#v", result.Failures)
		}
	}
	onFound := false
	for _, input := range result.Inputs {
		if input.AccountID == "w2b-on" {
			onFound = true
		}
	}
	if !onFound {
		t.Fatalf("默认开启的对照账户必须在候选中: inputs=%#v failures=%#v", result.Inputs, result.Failures)
	}

	// 显式 LoadAccount 同受生效开关过滤（实例行取来源值）。
	for _, accountID := range []string{"w2b-off", "w2b-inst"} {
		inputs, err := reader.LoadAccount(ctx, accountID)
		if err != nil {
			t.Fatalf("load 显式候选 %s 必须成功: %v", accountID, err)
		}
		if len(inputs) != 0 {
			t.Fatalf("关闭账户 %s 的显式候选必须为空: %#v", accountID, inputs)
		}
	}

	// 候选行开关事实按列扫描（对照行为 true；开启实例行的来源列为 0）。
	rows, err := db.Query(directInputCandidatesSQL, now().Format(time.RFC3339Nano), 10, true, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	scanned := map[string]directCandidate{}
	for rows.Next() {
		candidate, scanErr := scanDirectCandidate(rows)
		if scanErr != nil {
			t.Fatalf("扫描候选失败: %v", scanErr)
		}
		scanned[candidate.account.ID] = candidate
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if candidate, ok := scanned["w2b-on"]; !ok || !candidate.backgroundProbeEnabled || !candidate.sourceBackgroundProbeEnabled {
		t.Fatalf("对照候选的开关列必须为 true: %#v", candidate)
	}

	// LoadAccountConfigRevision 返回生效开关；缺失来源 COALESCE 归 1。
	facts, found, err := reader.LoadAccountConfigRevision(ctx, "w2b-off")
	if err != nil || !found || facts.BackgroundProbeEnabled || facts.ConfigRevision != 1 {
		t.Fatalf("关闭账户 freshness 事实必须 (found, enabled=false): %+v %t %v", facts, found, err)
	}
	facts, found, err = reader.LoadAccountConfigRevision(ctx, "w2b-inst")
	if err != nil || !found || facts.BackgroundProbeEnabled {
		t.Fatalf("实例生效开关必须跟随来源关闭: %+v %t %v", facts, found, err)
	}
	facts, found, err = reader.LoadAccountConfigRevision(ctx, "w2b-on")
	if err != nil || !found || !facts.BackgroundProbeEnabled {
		t.Fatalf("对照账户 freshness 事实必须开启: %+v %t %v", facts, found, err)
	}
	if _, found, err := reader.LoadAccountConfigRevision(ctx, "w2b-missing"); found || err != nil {
		t.Fatalf("缺失账户必须 (false, nil): %t %v", found, err)
	}

	// 来源翻回开启后，实例生效值随之恢复且显式候选可见（跟随语义）。
	w2bSetProbeSwitch(t, db, "w2b-src", true)
	facts, found, err = reader.LoadAccountConfigRevision(ctx, "w2b-inst")
	if err != nil || !found || !facts.BackgroundProbeEnabled {
		t.Fatalf("来源恢复开启后实例生效值必须开启: %+v %t %v", facts, found, err)
	}
	inputs, err := reader.LoadAccount(ctx, "w2b-inst")
	if err != nil || len(inputs) != 1 || inputs[0].AccountID != "w2b-inst" {
		t.Fatalf("来源恢复开启后实例显式候选必须可见: %#v %v", inputs, err)
	}
}

// w2bGuardUpstream 是带请求计数的假上游（任何请求都以 500 收敛，探针单次
// 快速失败，不触发超时重试梯子）。
func w2bGuardUpstream(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var count atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"w2b fake upstream"}}`))
	}))
	t.Cleanup(server.Close)
	return server, &count
}

// w2bGuardInput 构造通过 validateScheduledInput/inputEligible 的有效 input，
// fence 与 fixture 账户（revision 1/1/1）对齐，上游指向计数 server。
func w2bGuardInput(t *testing.T, serverURL, accountID string, now time.Time) exactkeyprobe.Input {
	t.Helper()
	envelope, err := exactkeyprobe.EncryptV1Envelope(w2bSecret, []byte(`{"api_key":"sk-w2b-guard"}`))
	if err != nil {
		t.Fatal(err)
	}
	input := exactkeyprobe.Input{
		AccountID:        accountID,
		InputVersion:     1,
		ConfigRevision:   1,
		DispatchRevision: 1,
		// 空 ProtocolProfileID 的可探针形态要求 Provider=openai + chat_json
		// （probe.go validateInput 的 provider/协议一致性门）。
		Provider:         "openai",
		Type:             "api_key",
		EndpointMode:     "chat_json",
		HealthModel:      "w2b-model",
		BaseURL:          serverURL,
		IssuedAt:         now.Add(-time.Minute),
		ExpiresAt:        now.Add(time.Hour),
		TLSPolicyVersion: "w2b",
		// httptest 上游是 http://127.0.0.1，须显式放行明文 base URL。
		AllowInsecureBaseURL: true,
		APIKeys:              []exactkeyprobe.APIKeyInput{{Index: 0, Fingerprint: "w2b-fp", Credential: exactkeyprobe.CredentialEnvelope{Kind: "api_key", Ciphertext: envelope}}},
		KeySetFingerprint:    "w2b-keyset",
		Eligibility:          exactkeyprobe.Eligibility{AccountStatus: "active", Schedulable: true, BoundGroup: true, AuthorizationEligible: true},
		Schedule:             exactkeyprobe.Schedule{HealthIntervalMS: 60_000, HealthJitterMS: 0, FailureThreshold: 3, FailureRetryMS: 300_000, CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 900_000, CooldownFailureBackoffMS: 3_000, MaxPauseMinutes: 60, MaxRecoveryHours: 24},
	}
	return input
}

// TestW2BackgroundProbeDispatchGuardPG 断言派发前守卫：flag=0 时定时路径
// 零上游请求零状态写入；显式请求按 probe_skipped_disabled 终态消费且不写
// current_state；flag=1 对照照常发起上游请求。
func TestW2BackgroundProbeDispatchGuardPG(t *testing.T) {
	db, dsn := w9hOpenKitDB(t)
	ctx := context.Background()
	w9hSeedBusinessAccount(t, db, "w2b-guard", "deepseek", "profile_deepseek_openai_v1", "api_key", "active", "chat_json", sql.NullString{}, sql.NullString{})
	w2bSetProbeSwitch(t, db, "w2b-guard", false)

	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: dsn})
	if err != nil {
		t.Fatalf("open jobs store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure store schema: %v", err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w2b-guard-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })

	server, count := w2bGuardUpstream(t)
	fixedNow := func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	reader, err := NewPostgresDirectInputReader(db, w2bSecret, time.Hour, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Now: fixedNow, CredentialSecret: w2bSecret, ProbeTimeout: 5 * time.Second, MaxResponseBytes: 1 << 20}
	runner := NewRunnerWithDirectInputReader(cfg, store, nil, reader)
	if runner.inputFreshness == nil {
		t.Fatal("PG reader 必须自动装配执行前新鲜度门")
	}
	input := w2bGuardInput(t, server.URL, "w2b-guard", fixedNow())

	// (a) 定时路径：flag=0 → 任务不 ready、零上游请求、零 current_state。
	task, err := runner.prepareScheduledInput(ctx, lease, input, fixedNow())
	if err != nil {
		t.Fatalf("定时守卫必须按已处理 skip 收敛而非错误: %v", err)
	}
	if task.ready {
		t.Fatal("flag=0 的定时输入不得 ready")
	}
	if got := count.Load(); got != 0 {
		t.Fatalf("flag=0 定时路径上游请求数 = %d want 0", got)
	}
	if _, found, err := store.LoadCurrentState(ctx, input.AccountID); found || err != nil {
		t.Fatalf("守卫拒绝不得写 current_state: found=%t err=%v", found, err)
	}

	// (b) 显式请求路径：终态 probe_skipped_disabled 消费，零上游请求。
	request := ProbeRequest{RequestID: "w2b-req-disabled", AccountID: input.AccountID, Reason: "request_failure",
		InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision,
		Deadline: fixedNow().Add(time.Minute)}
	if err := runner.runExplicitRequest(ctx, lease, input, request, fixedNow()); err != nil {
		t.Fatalf("显式请求守卫必须按终态消费: %v", err)
	}
	if got := count.Load(); got != 0 {
		t.Fatalf("flag=0 显式请求上游请求数 = %d want 0", got)
	}
	consumed, err := store.HasRequest(ctx, request.RequestID)
	if err != nil || !consumed {
		t.Fatalf("显式请求必须已按终态落库: %t %v", consumed, err)
	}
	var errorCode string
	if err := db.QueryRow(`SELECT error_code FROM juhe_jobs.account_health_outcomes WHERE request_id = $1`, request.RequestID).Scan(&errorCode); err != nil {
		t.Fatalf("读取终态 outcome: %v", err)
	}
	if errorCode != probeSkippedDisabledCode {
		t.Fatalf("终态码 = %q want %q", errorCode, probeSkippedDisabledCode)
	}
	if _, found, err := store.LoadCurrentState(ctx, input.AccountID); found || err != nil {
		t.Fatalf("显式终态消费不得写 current_state: found=%t err=%v", found, err)
	}

	// (c) 对照：flag=1（revision 不变）→ 守卫放行，上游请求真实发生。
	w2bSetProbeSwitch(t, db, "w2b-guard", true)
	task, err = runner.prepareScheduledInput(ctx, lease, input, fixedNow())
	if err != nil {
		t.Fatalf("对照定时路径必须成功: %v", err)
	}
	if !task.ready {
		t.Fatal("flag=1 对照输入必须 ready")
	}
	if got := count.Load(); got == 0 {
		t.Fatal("flag=1 对照必须发起上游请求")
	}
}

// w2bStubFlagBoundary 同时实现 ProbeRequestBoundary 与 ProbeRequestFlagReader
// 的 stub：fence 事实恒在范围内，开关由字段注入。
type w2bStubFlagBoundary struct {
	enabled bool
	found   bool
	err     error
	settled []string
}

func (b *w2bStubFlagBoundary) CurrentProbeInput(context.Context, string) (int64, int64, int64, bool, error) {
	return 1, 1, 1, true, nil
}

func (b *w2bStubFlagBoundary) BackgroundProbeEnabled(context.Context, string) (bool, bool, error) {
	return b.enabled, b.found, b.err
}

// w2bStubOutboxRows 是返回固定 pending 行的 outbox store stub。
type w2bStubOutboxRows struct {
	rows    []ProbeOutboxRow
	doneIDs []string
}

func (s *w2bStubOutboxRows) ClaimPendingProbeRequests(context.Context, int, time.Time) ([]ProbeOutboxRow, error) {
	return s.rows, nil
}

func (s *w2bStubOutboxRows) CompleteProbeRequest(_ context.Context, requestID string, _ time.Time) (bool, error) {
	s.doneIDs = append(s.doneIDs, requestID)
	return true, nil
}

// TestW2BackgroundProbeOutboxDrainGuardPG 断言 outbox 消费守卫：关闭账户的
// 显式请求行按 probe_skipped_disabled 终态出队（fence 结算 unknown），零
// 上游请求、零 current_state；开启账户不受影响。
func TestW2BackgroundProbeOutboxDrainGuardPG(t *testing.T) {
	db, dsn := w9hOpenKitDB(t)
	ctx := context.Background()
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: dsn})
	if err != nil {
		t.Fatalf("open jobs store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure store schema: %v", err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w2b-drain-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: %t %v", acquired, err)
	}
	t.Cleanup(func() { _ = store.ReleaseOwnerLease(ctx, lease) })

	fixedNow := func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	runner := NewRunner(Config{Now: fixedNow}, store, nil)

	newRow := func(requestID string, withFence bool) ProbeOutboxRow {
		row := ProbeOutboxRow{RequestID: requestID, AccountID: "w2b-drain", Reason: "request_failure", Deadline: fixedNow().Add(time.Minute)}
		if withFence {
			row.SourceFence = &SourceFence{StateKey: "w2b-state", AccountID: "w2b-drain", SourceGeneration: 1, SourceFenceID: "w2b-fence", RuntimeKey: "w2b-rt", ProbeGeneration: 1, ConfigRevision: 1}
		}
		return row
	}

	// (a) flag=0 + source fence：终态出队 + fence=unknown 结算。
	outbox := &w2bStubOutboxRows{rows: []ProbeOutboxRow{newRow("w2b-outbox-disabled", true)}}
	boundary := &w2bStubFlagBoundary{enabled: false, found: true}
	var settledStates []string
	runner.SetProbeRequestDrain(&ProbeRequestDrain{
		Store:    outbox,
		Boundary: boundary,
		SettleFence: func(_ context.Context, fence SourceFence, state string) error {
			boundary.settled = append(boundary.settled, fence.SourceFenceID+"="+state)
			settledStates = append(settledStates, state)
			return nil
		},
	})
	if err := runner.drainProbeRequestOutbox(ctx, lease); err != nil {
		t.Fatalf("drain 必须成功收敛: %v", err)
	}
	if len(outbox.doneIDs) != 1 || outbox.doneIDs[0] != "w2b-outbox-disabled" {
		t.Fatalf("关闭账户行必须出队: %#v", outbox.doneIDs)
	}
	if len(settledStates) != 1 || settledStates[0] != "unknown" {
		t.Fatalf("fence 必须结算 unknown: %#v", settledStates)
	}
	var errorCode string
	if err := db.QueryRow(`SELECT error_code FROM juhe_jobs.account_health_outcomes WHERE request_id = $1`, "w2b-outbox-disabled").Scan(&errorCode); err != nil {
		t.Fatalf("读取终态 outcome: %v", err)
	}
	if errorCode != probeSkippedDisabledCode {
		t.Fatalf("outbox 终态码 = %q want %q", errorCode, probeSkippedDisabledCode)
	}
	if _, found, err := store.LoadCurrentState(ctx, "w2b-drain"); found || err != nil {
		t.Fatalf("outbox 守卫不得写 current_state: found=%t err=%v", found, err)
	}

	// (b) flag=1：不触发守卫，无 fence 结算（未装配 input 通道时行按既有
	// input_stale 终态收敛，此处只断言守卫未拦截）。
	secondOutbox := &w2bStubOutboxRows{rows: []ProbeOutboxRow{newRow("w2b-outbox-enabled", false)}}
	enabledBoundary := &w2bStubFlagBoundary{enabled: true, found: true}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: secondOutbox, Boundary: enabledBoundary})
	if err := runner.drainProbeRequestOutbox(ctx, lease); err != nil {
		t.Fatalf("开启账户 drain 必须成功: %v", err)
	}
	if len(enabledBoundary.settled) != 0 {
		t.Fatalf("开启账户不得触发 fence 结算: %#v", enabledBoundary.settled)
	}
	if len(secondOutbox.doneIDs) != 1 {
		t.Fatalf("开启账户行必须照常出队: %#v", secondOutbox.doneIDs)
	}

	// (c) 读取失败属瞬态：行保持 pending（不出队、不写终态），drain 上抛。
	failingOutbox := &w2bStubOutboxRows{rows: []ProbeOutboxRow{newRow("w2b-outbox-flag-error", false)}}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{
		Store:    failingOutbox,
		Boundary: &w2bStubFlagBoundary{found: true, err: errors.New("w2b 注入开关读取失败")},
	})
	if err := runner.drainProbeRequestOutbox(ctx, lease); err == nil {
		t.Fatal("开关读取失败必须上抛保持 pending")
	}
	if len(failingOutbox.doneIDs) != 0 {
		t.Fatalf("瞬态失败的行不得出队: %#v", failingOutbox.doneIDs)
	}
}

// TestW2BackgroundProbeDisabledWarnRateLimit 断言定时路径开关拒绝诊断的
// 10 分钟每账户限频。
func TestW2BackgroundProbeDisabledWarnRateLimit(t *testing.T) {
	recording := &w2bRecordingHandler{}
	runner := NewRunner(Config{Now: func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }}, nil, slog.New(recording))
	input := exactkeyprobe.Input{AccountID: "w2b-noisy"}
	runner.warnProbeSkippedDisabled(input)
	runner.warnProbeSkippedDisabled(input)
	if records := recording.count(probeSkippedDisabledEvent); records != 1 {
		t.Fatalf("10 分钟窗口内重复拒绝必须限频为 1 条: %d", records)
	}
	runner = NewRunner(Config{Now: func() time.Time { return time.Date(2026, 10, 30, 12, 21, 0, 0, time.UTC) }}, nil, slog.New(recording))
	runner.warnProbeSkippedDisabled(input)
	if records := recording.count(probeSkippedDisabledEvent); records != 2 {
		t.Fatalf("窗口外拒绝必须再次告警: %d", records)
	}
}

// w2bRecordingHandler 是按 event 属性计数的 slog handler（测试专用）。
type w2bRecordingHandler struct {
	counts map[string]int
}

func (h *w2bRecordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *w2bRecordingHandler) Handle(_ context.Context, record slog.Record) error {
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "event" {
			if h.counts == nil {
				h.counts = map[string]int{}
			}
			h.counts[attr.Value.String()]++
		}
		return true
	})
	return nil
}

func (h *w2bRecordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *w2bRecordingHandler) WithGroup(string) slog.Handler { return h }

func (h *w2bRecordingHandler) count(event string) int { return h.counts[event] }
