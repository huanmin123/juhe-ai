package accounthealth

// w16f_units_test.go 波次 w16f 第一批：纯函数未覆盖臂（无 DB、无网络）：
//   - config.go：LoadConfig 的连接池参数、INPUT pool 校验、PROBE_TIMEOUT
//     解析失败臂，以及 Windows 跨盘符 input 目录 Rel 失败臂；
//   - probe.go：validateInput 的 OAuth 到期 / Code Assist 缺 project 臂、
//     buildProbeRequest 的未知 endpoint mode 与 codeAssist 包装臂、
//     probeTransport 的代理凭据失败臂、verifyResponse 的 profile images /
//     default / images data 非法 / url 字段臂、verifyResponsesSSE 缺挑战臂、
//     consumeResponsesSSEEvent 的 [DONE] / output_text.done 臂、
//     transportFailure / responseReadFailure 的网络超时臂；
//   - scheduler.go：Ready nil 接收器、nextDue 的 pending_test / 冷却状态
//     分裂臂、boundedCooldownRemaining 负剩余臂、cooldownDefer 边界钳制臂、
//     preserveStateForSourceOnlyOutcome 冷却保留臂；
//   - direct_input.go：ToInput 的来源协议元数据不一致、hybrid 探活目标失败、
//     hybrid 缺 base_url、chatgpt_user_id 回退臂；isSupportedDirectProfile 的
//     xai provider 不匹配臂、directBaseURL 的 gemini native 非 code-assist
//     分支、directProxyEnvelope 空 secret 臂；
//   - executor.go：ExecuteInputProbe 的 OAuth access 直通臂。

import (
	"context"
	"encoding/base64"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"runtime"
	"strings"
	"testing"
	"time"
)

// w16fConfigEnv 构造 LoadConfig 的可覆盖 getenv。
func w16fConfigEnv(base map[string]string) func(string) string {
	return func(key string) string { return base[key] }
}

func w16fSigningKey() string {
	return base64.RawURLEncoding.EncodeToString(make([]byte, 32))
}

func w16fMergeEnv(base, overlay map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return merged
}

// TestW16fLoadConfigPoolArms 覆盖 LoadConfig 的连接池与超时配置失败臂。
func TestW16fLoadConfigPoolArms(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":   "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":  "w16f-instance",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":        "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL": "postgres://w16f.invalid:5432/db",
		// INPUT_SOURCE 显式 files：缺省已改为跟随 store 模式（2026-09-19），
		// postgres store 会默认 direct input 并要求 INPUT_POSTGRES_URL。
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":      "files",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   `C:\w16f\inputs`,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": w16fSigningKey(),
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "w16f-secret",
	}
	// 清理批次 C4（2026-09-30）：连接池 env 校验臂已随收编退役。
	// PROBE_TIMEOUT 非法 → configDuration 失败臂。
	if _, err := LoadConfig(w16fConfigEnv(w16fMergeEnv(base, map[string]string{"JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT": "w16f"}))); err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("probe timeout 非法必须报错: %v", err)
	}
}

// TestW16fLoadConfigSQLiteCrossDrive 覆盖 Windows 跨盘符 input 目录的
// filepath.Rel 失败臂（非 Windows 跳过）。
func TestW16fLoadConfigSQLiteCrossDrive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Rel 跨盘符失败臂只在 Windows 可达")
	}
	cfg := map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "w16f-instance",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     `C:\w16f\state.sqlite3`,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   `Q:\w16f-inputs`,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": w16fSigningKey(),
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "w16f-secret",
	}
	if _, err := LoadConfig(w16fConfigEnv(cfg)); err == nil {
		t.Fatal("跨盘符 input 目录必须报 Rel 错误")
	}
}

// TestW16fRunnerNilReady 覆盖 Ready 的 nil 接收器臂。
func TestW16fRunnerNilReady(t *testing.T) {
	var runner *Runner
	if runner.Ready() {
		t.Fatal("nil runner 必须返回 false")
	}
}

// TestW16fNextDueCooldownArms 覆盖 nextDue 的 pending_test、冷却状态分裂与
// fence 失效臂。
func TestW16fNextDueCooldownArms(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	sourceRevision := int64(5)
	base := exactkeyprobe.Input{
		AccountID: "w16f-acc", InputVersion: 2, ConfigRevision: 3, DispatchRevision: 4,
		IssuedAt:    now.Add(-time.Hour),
		Eligibility: exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", BoundGroup: true, AuthorizationEligible: true, SourceConfigRevision: &sourceRevision},
	}
	validFence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-time.Hour), Generation: "gen-w16f", SourceConfigRevision: &sourceRevision}
	cooldownUntil := now.Add(time.Hour)
	// pending_test + 无状态 → health/IssuedAt。
	pending := base
	pending.Eligibility.AccountStatus = "pending_test"
	kind, due, ok := nextDue(pending, CurrentState{}, false, now)
	if !ok || kind != "health" || !due.Equal(pending.IssuedAt) {
		t.Fatalf("pending_test 无状态必须 health/IssuedAt: %s %v %t", kind, due, ok)
	}
	// 状态匹配 + state.AccountStatus 为空（隔离基线）+ 无 fence → false。
	quarantinedState := CurrentState{InputVersion: 2, ConfigRevision: 3, DispatchRevision: 4}
	noFence := base
	if _, _, ok := nextDue(noFence, quarantinedState, true, now); ok {
		t.Fatal("无 fence 的隔离基线必须不可调度")
	}
	// 隔离基线 + 合法 fence + state 无 NextDueAt → 用 input cooldown_until。
	fenced := base
	fenced.Cooldown = validFence
	fenced.Eligibility.CooldownUntil = &cooldownUntil
	kind, due, ok = nextDue(fenced, quarantinedState, true, now)
	if !ok || kind != "cooldown_retest" || !due.Equal(cooldownUntil) {
		t.Fatalf("隔离基线必须用 cooldown_until: %s %v %t", kind, due, ok)
	}
	// 业务行状态与 input 状态分裂 + 无 fence → false。
	splitState := quarantinedState
	splitState.AccountStatus = "active"
	if _, _, ok := nextDue(noFence, splitState, true, now); ok {
		t.Fatal("状态分裂且无 fence 必须不可调度")
	}
	// state 冷却中 + fence 五元一致但 source revision 与当前 input 不一致 → false。
	mismatchSource := int64(9)
	stateFence := &exactkeyprobe.CooldownFence{ObservationStartedAt: now.Add(-2 * time.Hour), Generation: "gen-w16f", SourceConfigRevision: &mismatchSource}
	cooldownState := CurrentState{InputVersion: 2, ConfigRevision: 3, DispatchRevision: 4, AccountStatus: "temporary_unavailable", CooldownFence: stateFence, NextDueAt: ptrTime(now.Add(30 * time.Minute))}
	sameFenceInput := base
	sameFenceInput.Cooldown = &exactkeyprobe.CooldownFence{ObservationStartedAt: stateFence.ObservationStartedAt, Generation: stateFence.Generation, SourceConfigRevision: &mismatchSource}
	sameFenceInput.Eligibility.CooldownUntil = &cooldownUntil
	if _, _, ok := nextDue(sameFenceInput, cooldownState, true, now); ok {
		t.Fatal("fence source revision 与当前 input 不一致必须不可调度")
	}
}

// TestW16fBoundedCooldownNegativeRemaining 覆盖 boundedCooldownRemaining 的
// 负剩余钳制臂。
func TestW16fBoundedCooldownNegativeRemaining(t *testing.T) {
	enabled := false
	input := exactkeyprobe.Input{Eligibility: exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", TemporaryUnavailableContinuousProbeEnabled: &enabled}}
	fence := &exactkeyprobe.CooldownFence{ObservationStartedAt: time.Now().UTC().Add(-20 * time.Minute), Generation: "gen-w16f"}
	remaining, bounded := boundedCooldownRemaining(input, "temporary_unavailable", fence, time.Now().UTC())
	if !bounded || remaining != 0 {
		t.Fatalf("负剩余必须钳制为 0: remaining=%s bounded=%t", remaining, bounded)
	}
}

// TestW16fCooldownDeferBoundaryArms 覆盖 cooldownDefer 的上下限钳制臂。
func TestW16fCooldownDeferBoundaryArms(t *testing.T) {
	// base 超过 maxScheduleDuration → 钳制后与 maximum 取小。
	if got := cooldownDefer(0, maxScheduleDuration+time.Hour, 15*time.Minute); got != 15*time.Minute {
		t.Fatalf("base 超上限必须被 maximum 收敛: %s", got)
	}
	// maximum 超过 maxScheduleDuration → 钳制。
	if got := cooldownDefer(0, 30*time.Second, maxScheduleDuration+time.Hour); got != 30*time.Second {
		t.Fatalf("maximum 超上限时结果必须等于 base: %s", got)
	}
	// maximum 低于最小值 → 抬升到最小值后再钳制 base。
	if got := cooldownDefer(0, 30*time.Second, time.Second); got != 3*time.Second {
		t.Fatalf("maximum 低于下限必须钳制 base: %s", got)
	}
}

// TestW16fPreserveStateCooldownArm 覆盖 preserveStateForSourceOnlyOutcome 的
// 冷却保留臂。
func TestW16fPreserveStateCooldownArm(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	sourceRevision := int64(5)
	cooldownUntil := now.Add(time.Hour)
	input := exactkeyprobe.Input{
		InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
		Eligibility: exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable", CooldownUntil: &cooldownUntil, SourceConfigRevision: &sourceRevision},
		Cooldown:    &exactkeyprobe.CooldownFence{ObservationStartedAt: now, Generation: "gen-w16f", SourceConfigRevision: &sourceRevision},
	}
	outcome := Outcome{}
	preserveStateForSourceOnlyOutcome(&outcome, input, CurrentState{}, false)
	if outcome.CooldownFence == nil || outcome.NextDueAt == nil || !outcome.NextDueAt.Equal(cooldownUntil) {
		t.Fatalf("冷却保留必须写入 fence 与 next due: %+v", outcome)
	}
}

// TestW16fExecuteInputProbeOAuthAccessArm 覆盖 ExecuteInputProbe 的 OAuth
// access 直通臂（不走 API Key cursor）。
func TestW16fExecuteInputProbeOAuthAccessArm(t *testing.T) {
	secret := "w16f-executor-secret"
	input := testInput("https://api.example.com", "responses_json")
	input.Type = "oauth"
	input.OAuthAccess = &exactkeyprobe.CredentialEnvelope{Kind: "oauth_access", Ciphertext: testEnvelope(t, secret, `{"access_token":"at-w16f"}`)}
	request := ProbeRequest{
		RequestID: "w16f-request-oauth", AccountID: input.AccountID,
		InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision,
		Deadline: time.Now().UTC().Add(time.Minute),
	}
	outcome, err := ExecuteInputProbe(context.Background(), nil, OwnerLease{}, input, request, exactkeyprobe.ProbeOptions{Secret: secret, Now: time.Now})
	if err != nil {
		t.Fatalf("OAuth 直通臂不应返回 error: %v", err)
	}
	if outcome.OutcomeID == "" || outcome.RequestID != request.RequestID {
		t.Fatalf("outcome 必须保留幂等字段: %+v", outcome)
	}
}
