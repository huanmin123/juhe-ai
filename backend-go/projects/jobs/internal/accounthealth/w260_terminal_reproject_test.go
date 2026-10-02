package accounthealth

// BUG-0260（J1 冷却复测终态投影丢失脑裂死循环）用例：
//  1. nextDue 对「jobs current_state=error 终态 × business 仍冷却」脑裂形态
//     返回 cooldown_terminal_reproject（不再放行复测探针）；
//  2. runInput 全链：不发探针，直接补落地 cooldown_error 终态投影到
//     business（status=error/schedulable=0/fence/last_error 正确），jobs
//     current_state 保持 error 终态不变，audit outcome 行幂等落库；
//  3. 投影落地后循环终止：business=error 不再构成候选；同一 input_version
//     内同 kind 只尝试一次（request_id 幂等）；
//  4. epoch 前进/fence 无效形态不触发补投影（走原路径或不 due）；
//     business fence 失配（并发编辑）时投影被业务侧守卫拒绝为 stale，
//     单次尝试有界，不形成新的高频循环。

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var w260Now = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

// w260Fence 模拟生产形态：观察期起点在 8 天前（超过 7 天观察期上限）。
func w260Fence() *CooldownFence {
	return &CooldownFence{ObservationStartedAt: w260Now.Add(-8 * 24 * time.Hour), Generation: "w260-gen"}
}

// w260Fixture 组装完整链路：jobs store + 业务库 + 投影器 + 绑定投影器的
// Runner + 探针计数服务器（任何探针命中都会被用例断言捕获）。
type w260Fixture struct {
	*projectionFixture
	runner *Runner
	lease  OwnerLease
	probes atomic.Int64
	server *httptest.Server
}

func newW260Fixture(t *testing.T, businessSeed map[string]any) *w260Fixture {
	t.Helper()
	fixture := newProjectionFixture(t)
	lease, acquired, err := fixture.store.AcquireOwnerLease(context.Background(), "w260-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: acquired=%t err=%v", acquired, err)
	}
	result := &w260Fixture{projectionFixture: fixture, lease: lease}
	result.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		result.probes.Add(1)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	t.Cleanup(result.server.Close)
	if businessSeed != nil {
		fixture.seedAccount(t, businessSeed)
	}
	result.runner = NewRunner(Config{
		CredentialSecret:  "projection-test-secret",
		ProbeTimeout:      time.Second,
		MaxResponseBytes:  4096,
		MaxConcurrency:    1,
		Now:               func() time.Time { return w260Now },
		InputDirectory:    t.TempDir(),
		InputKeys:         map[string][]byte{"current": []byte("w260-input-signing-key-1234567890")},
	}, fixture.store, nil)
	result.runner.SetOutcomeProjector(fixture.projector)
	return result
}

// w260SeedTerminalState 通过真实 AppendOutcome 落一条与生产 2026-09-29 终态
// 判定同形的 cooldown_error outcome，建立 jobs current_state 的 error 终态。
func w260SeedTerminalState(t *testing.T, fixture *w260Fixture, fence *CooldownFence, observed time.Time) {
	t.Helper()
	terminal := Outcome{
		OutcomeID:        "w260-terminal-original",
		RequestID:        "w260-terminal-original-request",
		AccountID:        "acct-1",
		Outcome:          OutcomeUpstreamFailed,
		ObservedAt:       observed,
		InputVersion:     1,
		ConfigRevision:   5,
		DispatchRevision: 7,
		StatusCode:       401,
		ErrorCode:        "cooldown_retest_observation_timeout",
		ErrorMessage:     "冷却复测观察期已超过 7 天",
		AccountStatus:    "error",
		CooldownFence:    fence,
		Projection: &Projection{
			TargetAccountID:       "acct-1",
			TransitionKind:        "cooldown_error",
			InputVersion:          1,
			ConfigRevision:        5,
			DispatchRevision:      7,
			ExpectedAccountStatus: "temporary_unavailable",
			ExpectedCooldownFence: fence,
			CooldownFence:         fence,
		},
	}
	appendStoreOutcome(t, fixture.store, fixture.lease, terminal)
}

// w260SplitBrainInput 构造与 jobs state epoch 一致的 business 冷却快照
// （携带真实可解密凭据：红态下若仍发探针，计数服务器会留下命中证据）。
func w260SplitBrainInput(t *testing.T, fixture *w260Fixture, fence *CooldownFence) Input {
	t.Helper()
	input := testInput(fixture.server.URL, "chat_json")
	input.AccountID = "acct-1"
	input.InputVersion, input.ConfigRevision, input.DispatchRevision = 1, 5, 7
	input.IssuedAt = w260Now.Add(-time.Minute)
	input.ExpiresAt = w260Now.Add(time.Hour)
	cooldownUntil := w260Now.Add(-time.Hour)
	input.Eligibility = Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	input.Schedule = Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 1, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: 30_000, CooldownNeutralMaxMS: 15 * 60_000, CooldownFailureBackoffMS: 3_000}
	input.APIKeys = []APIKeyInput{{Index: 0, Fingerprint: "key-1", Credential: CredentialEnvelope{Kind: "api_key", Ciphertext: testEnvelope(t, "projection-test-secret", `{"api_key":"sk-w260"}`)}}}
	input.KeySetFingerprint = "keyset-w260"
	return input
}

func (f *w260Fixture) businessRow(t *testing.T) (status string, schedulable int64, lastErrorCode, lastErrorMessage, cooldownGeneration, cooldownObservation, cooldownUntil string) {
	t.Helper()
	var errorCode, errorMessage, generation, observation, until sql.NullString
	row := f.business.QueryRow(`SELECT status, schedulable, last_error_code, last_error_message, cooldown_retest_generation, cooldown_retest_observation_started_at, cooldown_until FROM accounts WHERE id='acct-1'`)
	if err := row.Scan(&status, &schedulable, &errorCode, &errorMessage, &generation, &observation, &until); err != nil {
		t.Fatalf("读取业务账户行失败: %v", err)
	}
	return status, schedulable, errorCode.String, errorMessage.String, generation.String, observation.String, until.String
}

func (f *w260Fixture) receipts(t *testing.T) []string {
	t.Helper()
	rows, err := f.business.Query(`SELECT disposition || ':' || COALESCE(reason, '') FROM account_health_projection_receipts WHERE account_id='acct-1' ORDER BY applied_at, outcome_id`)
	if err != nil {
		t.Fatalf("读取 receipts 失败: %v", err)
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("解码 receipt 失败: %v", err)
		}
		result = append(result, value)
	}
	return result
}

func (f *w260Fixture) outcomeCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM account_health_outcomes`).Scan(&count); err != nil {
		t.Fatalf("统计 outcome 失败: %v", err)
	}
	return count
}

// 1. nextDue 对 error 终态脑裂返回新 kind；request ID 不含时钟。
func TestW260NextDueErrorTerminalSplitBrainReturnsReprojectKind(t *testing.T) {
	fence := w260Fence()
	input := testInput("https://probe.invalid", "chat_json")
	input.InputVersion, input.ConfigRevision, input.DispatchRevision = 1, 5, 7
	cooldownUntil := w260Now.Add(-time.Hour)
	input.Eligibility = Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence
	state := CurrentState{InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, AccountStatus: "error", ErrorCode: "cooldown_retest_observation_timeout", CooldownFence: fence}

	kind, due, ok := nextDue(input, state, true, w260Now)
	if !ok || kind != cooldownTerminalReprojectKind || !due.Equal(w260Now) {
		t.Fatalf("error 终态脑裂必须返回补投影 kind 且立即到期: kind=%q due=%s ok=%t", kind, due, ok)
	}

	// rate_limited 形态同语义。
	input.Eligibility.AccountStatus = "rate_limited"
	kind, _, ok = nextDue(input, state, true, w260Now)
	if !ok || kind != cooldownTerminalReprojectKind {
		t.Fatalf("rate_limited 脑裂必须同样返回补投影 kind: kind=%q ok=%t", kind, ok)
	}
	input.Eligibility.AccountStatus = "temporary_unavailable"

	// 幂等键不含时钟：同一 input_version 内恒定，且与普通调度键不冲突。
	first := cooldownTerminalReprojectRequestID(input)
	stateObservedLater := state
	if first == "" || first != cooldownTerminalReprojectRequestID(input) {
		t.Fatal("补投影 request ID 必须在同一 input epoch 内稳定")
	}
	if first == scheduledRequestID(input, cooldownTerminalReprojectKind, stateObservedLater.ObservedAt) {
		t.Fatal("补投影 request ID 不得依赖时钟")
	}
}

// 2. 全链：不发探针、business 落终态、jobs state 不变、audit 行幂等落库。
func TestW260TerminalReprojectRunInputLandsBusinessWithoutProbe(t *testing.T) {
	fence := w260Fence()
	terminalObserved := w260Now.Add(-48 * time.Hour)
	fixture := newW260Fixture(t, map[string]any{
		"status":                                 "temporary_unavailable",
		"schedulable":                            1,
		"cooldown_until":                         w260Now.Add(-time.Hour).Format(time.RFC3339Nano),
		"cooldown_retest_observation_started_at": fenceGuardText(fence.ObservationStartedAt),
		"cooldown_retest_generation":             fence.Generation,
		"last_error_code":                        "upstream_5xx",
		"last_error_message":                     "上游失败",
	})
	w260SeedTerminalState(t, fixture, fence, terminalObserved)
	stateBefore, found, err := fixture.store.LoadCurrentState(context.Background(), "acct-1")
	if err != nil || !found || stateBefore.AccountStatus != "error" {
		t.Fatalf("终态种子未建立: found=%t state=%#v err=%v", found, stateBefore, err)
	}
	input := w260SplitBrainInput(t, fixture, fence)

	if err := fixture.runner.runInput(context.Background(), fixture.lease, input, w260Now); err != nil {
		t.Fatalf("runInput 失败: %v", err)
	}
	if hits := fixture.probes.Load(); hits != 0 {
		t.Fatalf("终态脑裂不得再发复测探针，实际命中 %d 次", hits)
	}
	status, schedulable, lastErrorCode, lastErrorMessage, generation, observation, cooldownUntil := fixture.businessRow(t)
	if status != "error" {
		t.Fatalf("business 必须补落地 error 终态: status=%s", status)
	}
	if schedulable != 0 {
		t.Fatalf("business schedulable 必须置 0: %d", schedulable)
	}
	if lastErrorCode != "cooldown_retest_observation_timeout" || lastErrorMessage != "冷却复测观察期已超过 7 天" {
		t.Fatalf("last_error 必须透传原终态审计语义: code=%q message=%q", lastErrorCode, lastErrorMessage)
	}
	if generation != fence.Generation || observation != fenceGuardText(fence.ObservationStartedAt) {
		t.Fatalf("cooldown fence 必须保持 business 现存值: generation=%q observation=%q", generation, observation)
	}
	if cooldownUntil != "" {
		t.Fatalf("终态冷却必须清空 cooldown_until: %q", cooldownUntil)
	}
	stateAfter, _, err := fixture.store.LoadCurrentState(context.Background(), "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if stateAfter.AccountStatus != "error" || !stateAfter.ObservedAt.Equal(terminalObserved) || stateAfter.ErrorCode != "cooldown_retest_observation_timeout" || stateAfter.CooldownFence == nil || stateAfter.CooldownFence.Generation != fence.Generation {
		t.Fatalf("jobs current_state 必须保持原 error 终态不被改写: %#v", stateAfter)
	}
	receipts := fixture.receipts(t)
	if len(receipts) != 1 || !strings.HasPrefix(receipts[0], "applied:") {
		t.Fatalf("补投影必须落 applied receipt: %v", receipts)
	}
	already, err := fixture.store.HasRequest(context.Background(), cooldownTerminalReprojectRequestID(input))
	if err != nil || !already {
		t.Fatalf("audit outcome 行必须落库: already=%t err=%v", already, err)
	}
	if count := fixture.outcomeCount(t); count != 2 {
		t.Fatalf("outcome 行应为终态种子+补投影 audit 共 2 行: %d", count)
	}
}

// 3. 循环终止与防放大：落地后 business=error 不再构成候选；同一 input_version
// 内同 kind 只尝试一次（重复 runInput 不再产生投影/audit/receipt）。
func TestW260TerminalReprojectTerminatesAndAttemptsOncePerEpoch(t *testing.T) {
	fence := w260Fence()
	fixture := newW260Fixture(t, map[string]any{
		"status":                                 "temporary_unavailable",
		"schedulable":                            1,
		"cooldown_until":                         w260Now.Add(-time.Hour).Format(time.RFC3339Nano),
		"cooldown_retest_observation_started_at": fenceGuardText(fence.ObservationStartedAt),
		"cooldown_retest_generation":             fence.Generation,
	})
	w260SeedTerminalState(t, fixture, fence, w260Now.Add(-48*time.Hour))
	input := w260SplitBrainInput(t, fixture, fence)
	if err := fixture.runner.runInput(context.Background(), fixture.lease, input, w260Now); err != nil {
		t.Fatalf("首次 runInput 失败: %v", err)
	}

	// 同一 input_version 重复触发（模拟下轮扫描仍读到旧快照）：request_id 幂等
	// 直接跳过，不再产生第二次投影或 audit 行。
	if err := fixture.runner.runInput(context.Background(), fixture.lease, input, w260Now); err != nil {
		t.Fatalf("重复 runInput 失败: %v", err)
	}
	if hits := fixture.probes.Load(); hits != 0 {
		t.Fatalf("重复扫描不得发探针: %d", hits)
	}
	if receipts := fixture.receipts(t); len(receipts) != 1 {
		t.Fatalf("同一 input_version 只允许一次补投影 receipt: %v", receipts)
	}
	if count := fixture.outcomeCount(t); count != 2 {
		t.Fatalf("同一 input_version 只允许一行补投影 audit: %d", count)
	}

	// 投影落地后 business=error/schedulable=0：直读输入形态不再构成候选，
	// runInput 无任务可做（循环终止）。
	landed := w260SplitBrainInput(t, fixture, fence)
	landed.Eligibility.AccountStatus = "error"
	landed.Eligibility.Schedulable = false
	landed.Eligibility.CooldownUntil = nil
	landed.Cooldown = nil
	if err := fixture.runner.runInput(context.Background(), fixture.lease, landed, w260Now); err != nil {
		t.Fatalf("落地后 runInput 失败: %v", err)
	}
	if hits := fixture.probes.Load(); hits != 0 {
		t.Fatalf("落地后不得再发探针: %d", hits)
	}
	if count := fixture.outcomeCount(t); count != 2 {
		t.Fatalf("落地后不得新增 outcome: %d", count)
	}
}

// 4. epoch 前进/fence 无效不触发补投影；business fence 失配时投影被守卫拒绝
// 为 stale 且单次有界。
func TestW260TerminalReprojectFenceAndEpochGuards(t *testing.T) {
	fence := w260Fence()
	input := testInput("https://probe.invalid", "chat_json")
	input.InputVersion, input.ConfigRevision, input.DispatchRevision = 1, 5, 7
	cooldownUntil := w260Now.Add(-time.Hour)
	input.Eligibility = Eligibility{AccountStatus: "temporary_unavailable", Schedulable: true, BoundGroup: true, AuthorizationEligible: true, CooldownUntil: &cooldownUntil}
	input.Cooldown = fence

	// epoch 前进（state 停留在旧 input epoch）：走原 cooldown_retest 路径。
	staleEpoch := CurrentState{InputVersion: 2, ConfigRevision: 5, DispatchRevision: 7, AccountStatus: "error", CooldownFence: fence}
	kind, _, ok := nextDue(input, staleEpoch, true, w260Now)
	if !ok || kind != "cooldown_retest" {
		t.Fatalf("epoch 前进形态必须保持原路径: kind=%q ok=%t", kind, ok)
	}

	// business fence 无效（source revision 与 input 不一致）：不 due。
	badFence := &CooldownFence{ObservationStartedAt: fence.ObservationStartedAt, Generation: fence.Generation}
	sourceRevision := int64(99)
	badFence.SourceConfigRevision = &sourceRevision
	badInput := input
	badInput.Eligibility.SourceConfigRevision = &sourceRevision
	badInput.Cooldown = badFence
	badInput.Cooldown.SourceConfigRevision = nil
	if _, _, ok := nextDue(badInput, CurrentState{InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7, AccountStatus: "error", CooldownFence: fence}, true, w260Now); ok {
		t.Fatal("business fence 无效时不得调度")
	}

	// business fence 失配（并发编辑后 generation 漂移）：补投影被业务侧守卫
	// 拒绝为 stale，business 不被改写，且单次尝试有界。
	fixture := newW260Fixture(t, map[string]any{
		"status":                                 "temporary_unavailable",
		"schedulable":                            1,
		"cooldown_until":                         w260Now.Add(-time.Hour).Format(time.RFC3339Nano),
		"cooldown_retest_observation_started_at": fenceGuardText(fence.ObservationStartedAt),
		"cooldown_retest_generation":             "concurrent-edit-generation",
	})
	w260SeedTerminalState(t, fixture, fence, w260Now.Add(-48*time.Hour))
	mismatched := w260SplitBrainInput(t, fixture, fence)
	if err := fixture.runner.runInput(context.Background(), fixture.lease, mismatched, w260Now); err != nil {
		t.Fatalf("fence 失配形态 runInput 失败: %v", err)
	}
	if hits := fixture.probes.Load(); hits != 0 {
		t.Fatalf("fence 失配形态同样不得发探针: %d", hits)
	}
	status, schedulable, _, _, generation, _, _ := fixture.businessRow(t)
	if status != "temporary_unavailable" || schedulable != 1 || generation != "concurrent-edit-generation" {
		t.Fatalf("失配守卫必须拒绝改写 business: status=%s schedulable=%d generation=%s", status, schedulable, generation)
	}
	receipts := fixture.receipts(t)
	if len(receipts) != 1 || !strings.HasPrefix(receipts[0], "stale:") {
		t.Fatalf("失配形态必须落 stale receipt: %v", receipts)
	}
	// 重复扫描：request_id 幂等，不再新增 receipt/audit。
	if err := fixture.runner.runInput(context.Background(), fixture.lease, mismatched, w260Now); err != nil {
		t.Fatalf("fence 失配重复 runInput 失败: %v", err)
	}
	if receipts := fixture.receipts(t); len(receipts) != 1 {
		t.Fatalf("失配形态必须单次有界: %v", receipts)
	}
	if count := fixture.outcomeCount(t); count != 2 {
		t.Fatalf("失配形态必须单行 audit: %d", count)
	}
}
