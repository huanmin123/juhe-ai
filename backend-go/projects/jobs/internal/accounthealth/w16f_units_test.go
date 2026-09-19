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
	"net"
	"net/url"
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
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "w16f-instance",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":      "postgres://w16f.invalid:5432/db",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   `C:\w16f\inputs`,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": w16fSigningKey(),
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "w16f-secret",
	}
	// POSTGRES_MAX_IDLE_CONNS=0 → configPositiveInt 失败臂。
	invalidIdle := w16fConfigEnv(w16fMergeEnv(base, map[string]string{"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_IDLE_CONNS": "0"}))
	if _, err := LoadConfig(invalidIdle); err == nil || !strings.Contains(err.Error(), "必须是正整数") {
		t.Fatalf("idle=0 必须报错: %v", err)
	}
	// INPUT_SOURCE=postgres + INPUT_POSTGRES_MAX_OPEN_CONNS 非法 → 输入池 open 失败臂。
	inputPoolBase := w16fMergeEnv(base, map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":       "postgres",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": "postgres://w16f.invalid:5432/business",
	})
	if _, err := LoadConfig(w16fConfigEnv(w16fMergeEnv(inputPoolBase, map[string]string{"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_OPEN_CONNS": "w16f"}))); err == nil || !strings.Contains(err.Error(), "必须是正整数") {
		t.Fatalf("input pool open 无效必须报错: %v", err)
	}
	// idle > open → sqlpool.ValidatePoolLimits 失败臂。
	if _, err := LoadConfig(w16fConfigEnv(w16fMergeEnv(inputPoolBase, map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_OPEN_CONNS": "4",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_MAX_IDLE_CONNS": "999",
	}))); err == nil || !strings.Contains(err.Error(), "连接池配置无效") {
		t.Fatalf("input pool idle>open 必须报错: %v", err)
	}
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

// w16fTimeoutNetError 是实现 net.Error 的固定超时错误。
type w16fTimeoutNetError struct{}

func (w16fTimeoutNetError) Error() string   { return "w16f 网络超时" }
func (w16fTimeoutNetError) Timeout() bool   { return true }
func (w16fTimeoutNetError) Temporary() bool { return true }

// TestW16fValidateInputOAuthArms 覆盖 validateInput 的 OAuth 到期与
// Code Assist 缺 project_id 臂。
func TestW16fValidateInputOAuthArms(t *testing.T) {
	// OAuth access token 已到期。
	expired := testInput("https://api.example.com", "responses_json")
	expired.Type = "oauth"
	expired.OAuthExpiresAt = ptrTime(time.Now().UTC().Add(-time.Minute))
	if err := validateInput(expired, ProbeOptions{}); err == nil || !strings.Contains(err.Error(), "OAuth access token 已到期") {
		t.Fatalf("OAuth 到期必须报错: %v", err)
	}
	// google_oauth + code_assist 缺 project_id。
	assist := testInput("https://example.com", "generate_content_json")
	assist.ProtocolProfileID = "profile_gemini_native_v1beta"
	assist.Provider = "gemini"
	assist.ProtocolVersion = "v1beta"
	assist.Type = "google_oauth"
	assist.OAuthType = "code_assist"
	assist.OAuthExpiresAt = ptrTime(time.Now().UTC().Add(time.Hour))
	if err := validateInput(assist, ProbeOptions{}); err == nil || !strings.Contains(err.Error(), "Code Assist") {
		t.Fatalf("code_assist 缺 project_id 必须报错: %v", err)
	}
}

// TestW16fBuildProbeRequestArms 覆盖 buildProbeRequest 的未知 endpoint mode
// 与 codeAssist 请求包装臂。
func TestW16fBuildProbeRequestArms(t *testing.T) {
	base, err := url.Parse("https://api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	// 未知 endpoint mode → 请求构造失败臂。
	unknown := testInput("https://api.example.com", "chat_json")
	unknown.EndpointMode = "w16f-unknown"
	if _, err := buildProbeRequest(context.Background(), base, unknown, "token"); err == nil || !strings.Contains(err.Error(), "未冻结的 endpoint mode") {
		t.Fatalf("未知 mode 必须报错: %v", err)
	}
	// codeAssist 包装：gemini 协议 + google_oauth code_assist。
	assist := testInput("https://cloudcode-pa.googleapis.com", "generate_content_json")
	assist.ProtocolProfileID = "profile_gemini_native_v1beta"
	assist.Provider = "gemini"
	assist.Type = "google_oauth"
	assist.OAuthType = "code_assist"
	assist.OAuthProjectID = "w16f-project"
	request, err := buildProbeRequest(context.Background(), base, assist, "token")
	if err != nil {
		t.Fatalf("codeAssist 请求必须构造成功: %v", err)
	}
	if !strings.Contains(request.URL.Path, "v1internal") {
		t.Fatalf("codeAssist 必须改写路径: %s", request.URL.Path)
	}
}

// TestW16fProbeTransportBadProxy 覆盖 probeTransport 的代理凭据解封失败臂。
func TestW16fProbeTransportBadProxy(t *testing.T) {
	input := testInput("https://api.example.com", "chat_json")
	input.Proxy = &CredentialEnvelope{Kind: "proxy_url", Ciphertext: "w16f-not-an-envelope"}
	if _, err := probeTransport(input, ProbeOptions{Secret: "w16f-secret"}); err == nil || !strings.Contains(err.Error(), "代理凭据不可用") {
		t.Fatalf("坏代理 envelope 必须报错: %v", err)
	}
}

// TestW16fVerifyResponseArms 覆盖 verifyResponse 的 profile images、default
// 与 images data 非法/url 臂。
func TestW16fVerifyResponseArms(t *testing.T) {
	// profile 非空 + images_json。
	if err := verifyResponse(Input{ProtocolProfileID: "profile_openai_openai_v1", EndpointMode: "images_json"}, []byte(`{"data":[{"b64_json":"dzE2Zg=="}]}`)); err != nil {
		t.Fatalf("profile images 必须通过: %v", err)
	}
	// profile 非空 + 未知 mode + 含挑战 → default 成功臂。
	if err := verifyResponse(Input{ProtocolProfileID: "profile_openai_openai_v1", EndpointMode: "w16f-custom"}, []byte(`{"note":"JUHE"}`)); err != nil {
		t.Fatalf("default 含挑战必须通过: %v", err)
	}
	// default 无挑战 → 失败臂。
	if err := verifyResponse(Input{ProtocolProfileID: "profile_openai_openai_v1", EndpointMode: "w16f-custom"}, []byte(`{"note":"none"}`)); err == nil {
		t.Fatal("default 无挑战必须报错")
	}
	// images data 元素非对象 → continue 后报缺少有效图片。
	if err := verifyImagesJSON(map[string]any{"data": []any{1, "w16f"}}); err == nil || !strings.Contains(err.Error(), "缺少有效图片") {
		t.Fatalf("非法 data 元素必须报错: %v", err)
	}
	// images url 字段 → 成功臂。
	if err := verifyImagesJSON(map[string]any{"data": []any{map[string]any{"url": "https://img.example/w16f"}}}); err != nil {
		t.Fatalf("url 字段必须通过: %v", err)
	}
}

// TestW16fResponsesSSEArms 覆盖 verifyResponsesSSE 的缺挑战臂与
// consumeResponsesSSEEvent 的 [DONE] / output_text.done 臂。
func TestW16fResponsesSSEArms(t *testing.T) {
	// completed 事件但输出不含挑战。
	body := []byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	if err := verifyResponsesSSE(body); err == nil || !strings.Contains(err.Error(), "未包含挑战值") {
		t.Fatalf("缺挑战必须报错: %v", err)
	}
	// [DONE] 数据行短路臂。
	var output strings.Builder
	completed := false
	if err := consumeResponsesSSEEvent("data: [DONE]", &output, &completed); err != nil || completed {
		t.Fatalf("[DONE] 必须短路: %v completed=%t", err, completed)
	}
	// output_text.done 聚合臂。
	if err := consumeResponsesSSEEvent("data: {\"type\":\"response.output_text.done\",\"text\":\"juhe\"}", &output, &completed); err != nil {
		t.Fatalf("output_text.done 必须聚合: %v", err)
	}
	if !strings.Contains(output.String(), "juhe") {
		t.Fatalf("输出必须包含聚合文本: %q", output.String())
	}
}

// TestW16fFailureClassifiers 覆盖 transportFailure / responseReadFailure 的
// 网络超时臂。
func TestW16fFailureClassifiers(t *testing.T) {
	var networkErr net.Error = w16fTimeoutNetError{}
	timeout := transportFailure(networkErr)
	if timeout.Outcome != OutcomeUpstreamFailed || timeout.ErrorCode != "upstream_timeout" {
		t.Fatalf("transport 超时分类错误: %+v", timeout)
	}
	readTimeout := responseReadFailure(networkErr)
	if readTimeout.ErrorCode != "upstream_timeout" {
		t.Fatalf("read 超时分类错误: %+v", readTimeout)
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
	base := Input{
		AccountID: "w16f-acc", InputVersion: 2, ConfigRevision: 3, DispatchRevision: 4,
		IssuedAt: now.Add(-time.Hour),
		Eligibility: Eligibility{AccountStatus: "temporary_unavailable", BoundGroup: true, AuthorizationEligible: true, SourceConfigRevision: &sourceRevision},
	}
	validFence := &CooldownFence{ObservationStartedAt: now.Add(-time.Hour), Generation: "gen-w16f", SourceConfigRevision: &sourceRevision}
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
	stateFence := &CooldownFence{ObservationStartedAt: now.Add(-2 * time.Hour), Generation: "gen-w16f", SourceConfigRevision: &mismatchSource}
	cooldownState := CurrentState{InputVersion: 2, ConfigRevision: 3, DispatchRevision: 4, AccountStatus: "temporary_unavailable", CooldownFence: stateFence, NextDueAt: ptrTime(now.Add(30 * time.Minute))}
	sameFenceInput := base
	sameFenceInput.Cooldown = &CooldownFence{ObservationStartedAt: stateFence.ObservationStartedAt, Generation: stateFence.Generation, SourceConfigRevision: &mismatchSource}
	sameFenceInput.Eligibility.CooldownUntil = &cooldownUntil
	if _, _, ok := nextDue(sameFenceInput, cooldownState, true, now); ok {
		t.Fatal("fence source revision 与当前 input 不一致必须不可调度")
	}
}

// TestW16fBoundedCooldownNegativeRemaining 覆盖 boundedCooldownRemaining 的
// 负剩余钳制臂。
func TestW16fBoundedCooldownNegativeRemaining(t *testing.T) {
	enabled := false
	input := Input{Eligibility: Eligibility{AccountStatus: "temporary_unavailable", TemporaryUnavailableContinuousProbeEnabled: &enabled}}
	fence := &CooldownFence{ObservationStartedAt: time.Now().UTC().Add(-20 * time.Minute), Generation: "gen-w16f"}
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
	input := Input{
		InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
		Eligibility: Eligibility{AccountStatus: "temporary_unavailable", CooldownUntil: &cooldownUntil, SourceConfigRevision: &sourceRevision},
		Cooldown:    &CooldownFence{ObservationStartedAt: now, Generation: "gen-w16f", SourceConfigRevision: &sourceRevision},
	}
	outcome := Outcome{}
	preserveStateForSourceOnlyOutcome(&outcome, input, CurrentState{}, false)
	if outcome.CooldownFence == nil || outcome.NextDueAt == nil || !outcome.NextDueAt.Equal(cooldownUntil) {
		t.Fatalf("冷却保留必须写入 fence 与 next due: %+v", outcome)
	}
}

// w16fDirectFixture 构造通过基础校验的 DirectInput。
func w16fDirectFixture(t *testing.T) (DirectInput, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	credentials, err := EncryptV1Envelope("w16f-secret", []byte(`{"api_key":"sk-w16f"}`))
	if err != nil {
		t.Fatal(err)
	}
	input := DirectInput{
		Account: DirectAccount{
			ID: "w16f-acc", ConfigRevision: 1, DispatchRevision: 1,
			Provider: "gpt", ProtocolProfileID: "profile_gpt_openai_v1", ProtocolCode: "openai", ProtocolVersion: "v1",
			Type: "api_key", Status: "active", Schedulable: true,
			EndpointMode: "chat_json", HealthModel: "w16f-model",
			CredentialsEncrypted: credentials,
		},
		Binding:      DirectBinding{GroupID: "w16f-group", Enabled: true},
		InputVersion: 1,
		IssuedAt:     now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Hour),
		TLSPolicy:    "j1-direct-upstream-v1",
	}
	return input, now
}

func w16fSource(t *testing.T, provider, profile, code string) *DirectSource {
	t.Helper()
	credentials, err := EncryptV1Envelope("w16f-secret", []byte(`{"api_key":"sk-w16f-src"}`))
	if err != nil {
		t.Fatal(err)
	}
	return &DirectSource{
		ID: "w16f-src", ConfigRevision: 1, Provider: provider,
		ProtocolProfileID: profile, ProtocolCode: code, ProtocolVersion: "v1",
		Type: "api_key", Status: "active", Schedulable: true,
		CredentialsEncrypted: credentials,
	}
}

// TestW16fToInputSourceMetadataArms 覆盖 ToInput 的来源协议元数据不一致、
// hybrid 探活目标失败与 hybrid 缺 base_url 臂。
func TestW16fToInputSourceMetadataArms(t *testing.T) {
	direct, now := w16fDirectFixture(t)
	direct.Authorization = &DirectAuthorization{ID: "w16f-auth", Status: "active", QuotaEligible: true}
	direct.Binding.AuthorizationBindingID = "w16f-auth"
	// 来源 protocol_code 与 profile 不一致。
	direct.Source = w16fSource(t, "gpt", "profile_gpt_openai_v1", "anthropic")
	if _, err := direct.ToInput("w16f-secret", now); err == nil || !strings.Contains(err.Error(), "protocol_code") {
		t.Fatalf("来源 protocol_code 不一致必须报错: %v", err)
	}
	// hybrid 来源缺模型映射 → 探活目标解析失败。
	hybridDirect := direct
	hybridDirect.Source = w16fSource(t, "hybrid", "profile_hybrid_openai_chat_v1", "openai")
	if _, err := hybridDirect.ToInput("w16f-secret", now); err == nil {
		t.Fatal("hybrid 缺模型映射必须报错")
	}
	// hybrid 带映射但凭据缺 base_url → base URL 失败臂。
	mappedDirect := direct
	mappedDirect.Account.MappedUpstreamModel = "w16f-upstream-model"
	mappedDirect.Account.MappedUpstreamEndpointFamily = "chat_completions"
	mappedDirect.Source = w16fSource(t, "hybrid", "profile_hybrid_openai_chat_v1", "openai")
	if _, err := mappedDirect.ToInput("w16f-secret", now); err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("hybrid 缺 base_url 必须报错: %v", err)
	}
}

// TestW16fToInputChatGPTUserIDFallback 覆盖 OAuth chatgpt_user_id 回退臂。
func TestW16fToInputChatGPTUserIDFallback(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	credentials, err := EncryptV1Envelope("w16f-secret", []byte(`{"access_token":"at-w16f","expires_at":"2027-01-01T00:00:00Z","chatgpt_user_id":"w16f-user"}`))
	if err != nil {
		t.Fatal(err)
	}
	direct := DirectInput{
		Account: DirectAccount{
			ID: "w16f-oauth", ConfigRevision: 1, DispatchRevision: 1,
			Provider: "gpt", ProtocolProfileID: "profile_gpt_openai_v1", ProtocolCode: "openai", ProtocolVersion: "v1",
			Type: "oauth", Status: "active", Schedulable: true,
			EndpointMode: "responses_json", HealthModel: "w16f-model",
			CredentialsEncrypted: credentials,
		},
		Binding:      DirectBinding{GroupID: "w16f-group", Enabled: true},
		InputVersion: 1,
		IssuedAt:     now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Hour),
		TLSPolicy:    "j1-direct-upstream-v1",
	}
	input, err := direct.ToInput("w16f-secret", now)
	if err != nil {
		t.Fatalf("oauth ToInput 必须成功: %v", err)
	}
	if input.OAuthAccountID != "w16f-user" {
		t.Fatalf("chatgpt_user_id 必须回退为账户 ID: %q", input.OAuthAccountID)
	}
}

// TestW16fDirectProfileAndBaseURLArms 覆盖 isSupportedDirectProfile 的 xai
// provider 不匹配臂与 directBaseURL 的 gemini native 非 code-assist 分支。
func TestW16fDirectProfileAndBaseURLArms(t *testing.T) {
	if isSupportedDirectProfile("profile_xai_openai_v1", "gpt", "api_key", "chat_json") {
		t.Fatal("xai profile 配 gpt provider 必须不支持")
	}
	base, err := directBaseURL(nil, DirectAccount{ProtocolProfileID: "profile_gemini_native_v1beta", Type: "api_key"}, "gemini")
	if err != nil || base != "https://generativelanguage.googleapis.com" {
		t.Fatalf("gemini native 非 code-assist 必须用公共端点: %q %v", base, err)
	}
}

// TestW16fDirectProxyEnvelopeEmptySecret 覆盖 directProxyEnvelope 的空
// secret 加密失败臂。
func TestW16fDirectProxyEnvelopeEmptySecret(t *testing.T) {
	proxy := DirectProxy{ID: "w16f-proxy", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 8080}
	if _, err := directProxyEnvelope("  ", proxy); err == nil || !strings.Contains(err.Error(), "secret 不能为空") {
		t.Fatalf("空 secret 必须报错: %v", err)
	}
}

// TestW16fExecuteInputProbeOAuthAccessArm 覆盖 ExecuteInputProbe 的 OAuth
// access 直通臂（不走 API Key cursor）。
func TestW16fExecuteInputProbeOAuthAccessArm(t *testing.T) {
	secret := "w16f-executor-secret"
	input := testInput("https://api.example.com", "responses_json")
	input.Type = "oauth"
	input.OAuthAccess = &CredentialEnvelope{Kind: "oauth_access", Ciphertext: testEnvelope(t, secret, `{"access_token":"at-w16f"}`)}
	request := ProbeRequest{
		RequestID: "w16f-request-oauth", AccountID: input.AccountID,
		InputVersion: input.InputVersion, ConfigRevision: input.ConfigRevision, DispatchRevision: input.DispatchRevision,
		Deadline: time.Now().UTC().Add(time.Minute),
	}
	outcome, err := ExecuteInputProbe(context.Background(), nil, OwnerLease{}, input, request, ProbeOptions{Secret: secret, Now: time.Now})
	if err != nil {
		t.Fatalf("OAuth 直通臂不应返回 error: %v", err)
	}
	if outcome.OutcomeID == "" || outcome.RequestID != request.RequestID {
		t.Fatalf("outcome 必须保留幂等字段: %+v", outcome)
	}
}
