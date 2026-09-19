package accounthealth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wgValidDirectInput 构造通过全链校验的 api_key 形态 DirectInput。
func wgValidDirectInput(t *testing.T, secret string) DirectInput {
	t.Helper()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	credentials := mustEnvelopeJSON(t, secret, `{"api_key":"sk-1","base_url":"https://api.test/v1"}`)
	return DirectInput{
		Account: DirectAccount{
			ID: "acct-in", ConfigRevision: 2, DispatchRevision: 3,
			Provider: "openai", ProtocolCode: "openai", ProtocolVersion: "v1",
			Type: "api_key", Status: "active", Schedulable: true,
			EndpointMode: "chat_json", HealthModel: "gpt-test",
			CredentialsEncrypted: credentials,
		},
		Binding:      DirectBinding{GroupID: "g-1", Enabled: true, AuthorizationBindingID: "auth-1"},
		InputVersion: 4,
		IssuedAt:     now.Add(-time.Minute),
		ExpiresAt:    now.Add(time.Hour),
		TLSPolicy:    "tls-v1",
	}
}

// TestToInputSuccessPaths 覆盖 api_key 与 OAuth 两条成功装配路径。
func TestToInputSuccessPaths(t *testing.T) {
	secret := "toinput-secret"
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	input, err := wgValidDirectInput(t, secret).ToInput(secret, now)
	if err != nil {
		t.Fatalf("api_key 装配必须成功: %v", err)
	}
	if input.BaseURL != "https://api.test/v1" || len(input.APIKeys) != 1 || input.APIKeys[0].Fingerprint == "" {
		t.Fatalf("api_key 输入形状错误: %+v", input)
	}
	if input.Provider != "openai" || input.EndpointMode != "chat_json" || !input.Eligibility.BoundGroup {
		t.Fatalf("输入协议/资格错误: %+v", input)
	}
	// OAuth 形态：access_token + expires_at + account_id + quota project。
	oauth := wgValidDirectInput(t, secret)
	oauth.Account.Type = "oauth"
	oauth.Account.EndpointMode = "responses_sse"
	oauth.Account.CredentialsEncrypted = mustEnvelopeJSON(t, secret,
		`{"access_token":"tok-1","expires_at":"2027-09-10T12:00:00Z","account_id":"chatgpt-1","quota_project_id":"qp","oauth_type":"chatgpt","project_id":"pj"}`)
	oauthInput, err := oauth.ToInput(secret, now)
	if err != nil {
		t.Fatalf("OAuth 装配必须成功: %v", err)
	}
	if oauthInput.OAuthAccess == nil || oauthInput.OAuthAccountID != "chatgpt-1" || oauthInput.OAuthQuotaProjectID != "qp" || oauthInput.OAuthType != "chatgpt" || oauthInput.OAuthProjectID != "pj" {
		t.Fatalf("OAuth 附加字段错误: %+v", oauthInput)
	}
	if oauthInput.OAuthExpiresAt == nil || !oauthInput.OAuthExpiresAt.After(now) {
		t.Fatalf("OAuth 过期时间必须保留: %v", oauthInput.OAuthExpiresAt)
	}
}

// TestToInputRejectsInvalidMutations 表驱动覆盖 ToInput 的前置守卫。
func TestToInputRejectsInvalidMutations(t *testing.T) {
	secret := "toinput-reject-secret"
	// 前置守卫逐条断言。
	base := wgValidDirectInput(t, secret)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if _, err := base.ToInput("", now); err == nil {
		t.Fatal("缺 secret 必须报错")
	}
	noBinding := base
	noBinding.Binding.Enabled = false
	if _, err := noBinding.ToInput(secret, now); err == nil || !strings.Contains(err.Error(), "group binding") {
		t.Fatalf("绑定禁用必须报错: %v", err)
	}
	noVersion := base
	noVersion.InputVersion = 0
	if _, err := noVersion.ToInput(secret, now); err == nil {
		t.Fatal("版本无效必须报错")
	}
	noTLS := base
	noTLS.TLSPolicy = ""
	if _, err := noTLS.ToInput(secret, now); err == nil {
		t.Fatal("缺 TLS policy 必须报错")
	}
	expired := base
	expired.ExpiresAt = now
	if _, err := expired.ToInput(secret, now); err == nil {
		t.Fatal("已过期必须报错")
	}
	// 授权形态缺 authorization/source。
	authz := base
	authz.Authorization = &DirectAuthorization{ID: "auth-1", Status: "active", QuotaEligible: true}
	if _, err := authz.ToInput(secret, now); err == nil {
		t.Fatal("授权缺 source 必须报错")
	}
	// 授权可用但 binding id 不匹配（binding 校验先于 source 检查）。
	fullSource := base
	fullSource.Authorization = &DirectAuthorization{ID: "auth-2", Status: "active", QuotaEligible: true}
	fullSource.Source = &DirectSource{ID: "src-1", ConfigRevision: 1, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, CredentialsEncrypted: mustEnvelopeJSON(t, secret, `{"api_key":"sk-2"}`)}
	if _, err := fullSource.ToInput(secret, now); err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("binding id 不匹配必须报错: %v", err)
	}
	// binding 匹配但物理来源 provider 不支持。
	badSource := base
	badSource.Authorization = &DirectAuthorization{ID: "auth-1", Status: "active", QuotaEligible: true}
	badSource.Source = &DirectSource{ID: "src-1", ConfigRevision: 1, Provider: "gopher", Type: "api_key", Status: "active", Schedulable: true, CredentialsEncrypted: mustEnvelopeJSON(t, secret, `{"api_key":"sk-2"}`)}
	if _, err := badSource.ToInput(secret, now); err == nil || !strings.Contains(err.Error(), "物理来源") {
		t.Fatalf("不支持来源必须报错: %v", err)
	}
	// 冷却账户缺五元 fence。
	cooldown := base
	cooldown.Account.Status = "temporary_unavailable"
	if _, err := cooldown.ToInput(secret, now); err == nil || !strings.Contains(err.Error(), "fence") {
		t.Fatalf("冷却账户缺 fence 必须报错: %v", err)
	}
	// Google OAuth code_assist 只支持 GenerateContent（Interactions 形态先
	// 通过资格校验，再在 base URL 之后被显式拒绝）。
	gemini := base
	gemini.Account.Type = "google_oauth"
	gemini.Account.Provider = "gemini"
	gemini.Account.ProtocolProfileID = "profile_gemini_native_v1beta"
	gemini.Account.ProtocolCode = "gemini"
	gemini.Account.ProtocolVersion = "v1beta"
	gemini.Account.EndpointMode = "interactions_json"
	gemini.Account.CredentialsEncrypted = mustEnvelopeJSON(t, secret, `{"access_token":"tok","expires_at":"2027-09-10T12:00:00Z","oauth_type":"code_assist"}`)
	if _, err := gemini.ToInput(secret, now); err == nil || !strings.Contains(err.Error(), "GenerateContent") {
		t.Fatalf("code_assist 必须限定 GenerateContent: %v", err)
	}
	// api_key 池为空。
	emptyKeys := base
	emptyKeys.Account.CredentialsEncrypted = mustEnvelopeJSON(t, secret, `{"base_url":"https://api.test"}`)
	if _, err := emptyKeys.ToInput(secret, now); err == nil {
		t.Fatal("空 Key 池必须报错")
	}
}

// TestValidateDirectAccountMatrix 表驱动覆盖账户资格校验的每个拒绝分支。
func TestValidateDirectAccountMatrix(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	valid := DirectAccount{
		ID: "acct", ConfigRevision: 1, DispatchRevision: 1,
		Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
		EndpointMode: "chat_json", HealthModel: "gpt-x", CredentialsEncrypted: "envelope",
	}
	if err := validateDirectAccount(valid, now); err != nil {
		t.Fatalf("合法账户必须通过: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*DirectAccount)
	}{
		{"缺 ID", func(a *DirectAccount) { a.ID = " " }},
		{"revision 0", func(a *DirectAccount) { a.ConfigRevision = 0 }},
		{"type 不支持", func(a *DirectAccount) { a.Type = "gopher" }},
		{"mode 不支持", func(a *DirectAccount) { a.EndpointMode = "gopher_json" }},
		{"协议元数据不一致", func(a *DirectAccount) {
			a.ProtocolProfileID = "profile_openai_openai_v1"
			a.ProtocolCode = "anthropic"
		}},
		{"状态不可探活", func(a *DirectAccount) { a.Status = "disabled" }},
		{"active 不可调度", func(a *DirectAccount) { a.Schedulable = false }},
		{"已到期", func(a *DirectAccount) { expired := now.Add(-time.Hour); a.AccountExpiresAt = &expired }},
		{"仍在冷却", func(a *DirectAccount) { cooldown := now.Add(time.Hour); a.CooldownUntil = &cooldown }},
		{"缺健康模型", func(a *DirectAccount) { a.HealthModel = " " }},
		{"缺凭据", func(a *DirectAccount) { a.CredentialsEncrypted = "" }},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			mutated := valid
			item.mutate(&mutated)
			if err := validateDirectAccount(mutated, now); err == nil {
				t.Fatal("非法账户必须报错")
			}
		})
	}
	// pending_test 无需 schedulable。
	pending := valid
	pending.Status = "pending_test"
	pending.Schedulable = false
	if err := validateDirectAccount(pending, now); err != nil {
		t.Fatalf("pending_test 必须可探活: %v", err)
	}
}

// TestProbeValidateInputMismatch 覆盖探针输入校验的 provider/协议不一致分支。
func TestProbeValidateInputMismatch(t *testing.T) {
	input := wgSSEInput("chat_json", "anthropic", "")
	// provider=openai 与 anthropic 语义组合 → profile 判定后协议不一致。
	if err := validateInput(input, ProbeOptions{}); err == nil {
		t.Fatal("协议不一致必须报错")
	}
	noModel := wgSSEInput("chat_json", "openai", "profile_openai_openai_v1")
	noModel.HealthModel = ""
	if err := validateInput(noModel, ProbeOptions{}); err == nil {
		t.Fatal("缺模型必须报错")
	}
	badMetadata := wgSSEInput("chat_json", "openai", "profile_openai_openai_v1")
	badMetadata.ProtocolCode = "anthropic"
	if err := validateInput(badMetadata, ProbeOptions{}); err == nil {
		t.Fatal("协议元数据不一致必须报错")
	}
}

// TestLoadConfigErrorMatrix 表驱动覆盖 LoadConfig 的 fail closed 分支。
func TestLoadConfigErrorMatrix(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
			"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "wg",
			"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
			"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     "/tmp/wg/j1.sqlite3",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   "/tmp/wg/inputs",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
			"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "secret",
		}
	}
	cases := []struct {
		name   string
		mutate func(env map[string]string)
	}{
		{"缺 owner 声明", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER"] = "node" }},
		{"store 模式非法", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_STORE"] = "oracle" }},
		{"sqlite 缺路径", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH"] = "" }},
		{"PG 缺 URL", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_STORE"] = "postgres" }},
		{"缺 input 目录", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY"] = "" }},
		{"input 源非法", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE"] = "redis" }},
		{"签名键缺失", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY"] = "" }},
		{"签名键过短", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY"] = "AAAA" }},
		{"缺凭据 secret", func(env map[string]string) { env["JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"] = "" }},
		{"owner lease 小于探针超时", func(env map[string]string) {
			env["JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE"] = "16s"
			env["JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT"] = "30s"
		}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			env := base()
			item.mutate(env)
			if _, err := LoadConfig(getenvFrom(env)); err == nil {
				t.Fatal("非法配置必须 fail closed")
			}
		})
	}
	// 合法基线必须通过。
	if _, err := LoadConfig(getenvFrom(base())); err != nil {
		t.Fatalf("基线配置必须通过: %v", err)
	}
}

// TestRunnerRunLoopBranches 覆盖 Run 循环的租约获取失败与竞争等待分支。
func TestRunnerRunLoopBranches(t *testing.T) {
	store := wgNewStore(t)
	// 场景一：租约被他人持有 → 竞争等待路径。
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "other-owner", 10*time.Minute)
	if err != nil || !acquired {
		t.Fatalf("预占租约: %v %v", acquired, err)
	}
	runner := NewRunner(Config{InstanceID: "wg-loop", ScanInterval: 20 * time.Millisecond, OwnerLease: time.Minute}, store, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := runner.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("竞争等待必须可被 ctx 收口: %v", err)
	}
	if runner.Status().LastError != "" {
		t.Fatalf("竞争等待不算错误: %q", runner.Status().LastError)
	}
	_ = store.ReleaseOwnerLease(context.Background(), lease)

	// 场景二：store 句柄已关闭 → 获取失败 → setError 后由 ctx 收口。
	broken, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "broken.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	brokenRunner := NewRunner(Config{InstanceID: "wg-broken", ScanInterval: 20 * time.Millisecond, OwnerLease: time.Minute}, broken, nil)
	brokenCtx, brokenCancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer brokenCancel()
	if err := brokenRunner.Run(brokenCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("获取失败循环必须可被 ctx 收口: %v", err)
	}
	if brokenRunner.Status().LastError == "" {
		t.Fatal("获取失败必须记录 LastError")
	}
	if brokenRunner.Ready() {
		t.Fatal("失败状态不得就绪")
	}
}

// TestSetOwnerHeldTransitions 覆盖 owner 状态翻转与 LastScan 更新。
func TestSetOwnerHeldTransitions(t *testing.T) {
	store := wgNewStore(t)
	runner := NewRunner(Config{InstanceID: "wg-owned"}, store, nil)
	runner.setOwnerHeld(true)
	if !runner.Status().OwnerHeld {
		t.Fatal("setOwnerHeld(true) 必须生效")
	}
	runner.setOwnerHeld(false)
	if runner.Status().OwnerHeld {
		t.Fatal("setOwnerHeld(false) 必须生效")
	}
}

// ---- outbox 消费面 fake 端口（脚本化、可回放） ----

type wgFakeOutboxStore struct {
	rows      []ProbeOutboxRow
	completed []string
	claimErr  error
}

func (s *wgFakeOutboxStore) ClaimPendingProbeRequests(_ context.Context, limit int, _ time.Time) ([]ProbeOutboxRow, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if limit > len(s.rows) {
		limit = len(s.rows)
	}
	return s.rows[:limit], nil
}

func (s *wgFakeOutboxStore) CompleteProbeRequest(_ context.Context, requestID string, _ time.Time) (bool, error) {
	s.completed = append(s.completed, requestID)
	return true, nil
}

type wgFakeBoundary struct {
	revision int64
	ok       bool
	err      error
}

func (b *wgFakeBoundary) CurrentProbeInput(context.Context, string) (int64, int64, int64, bool, error) {
	if b.err != nil {
		return 0, 0, 0, false, b.err
	}
	return b.revision, b.revision, 1, b.ok, nil
}

// TestDrainProbeRequestOutboxSemantics 覆盖 outbox 消费的确定性收敛、
// 瞬态错误与 fence 结算分支。
func TestDrainProbeRequestOutboxSemantics(t *testing.T) {
	store := wgNewStore(t)
	runner := NewRunner(Config{InstanceID: "wg-drain", Now: func() time.Time { return time.Unix(0, 0).UTC() }}, store, nil)
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	lease := wgStoreLease(t, store, "wg-drain")

	// 未装配 → 静默跳过。
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("未装配必须静默: %v", err)
	}

	// 字段缺失 → 确定性收敛（返回 nil 并删行）。
	invalidStore := &wgFakeOutboxStore{rows: []ProbeOutboxRow{{RequestID: " ", AccountID: "acct"}}}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: invalidStore, Boundary: &wgFakeBoundary{ok: true}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("字段缺失必须收敛: %v", err)
	}
	if len(invalidStore.completed) != 1 {
		t.Fatalf("确定性失败必须删行: %v", invalidStore.completed)
	}

	// 范围外账户 → 结算 fence = unknown。
	settled := SourceFence{StateKey: "s", AccountID: "a"}
	outOfScope := &wgFakeOutboxStore{rows: []ProbeOutboxRow{{
		RequestID: "req-out", AccountID: "acct", Reason: "manual",
		SourceFence: &settled, Deadline: now.Add(time.Minute),
	}}}
	settleCalls := 0
	runner.SetProbeRequestDrain(&ProbeRequestDrain{
		Store: outOfScope, Boundary: &wgFakeBoundary{ok: false},
		SettleFence: func(_ context.Context, fence SourceFence, state string) error {
			settleCalls++
			if state != "unknown" || fence.AccountID != "a" {
				t.Fatalf("结算参数错误: %+v %s", fence, state)
			}
			return nil
		},
	})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("范围外消费必须成功: %v", err)
	}
	if settleCalls != 1 {
		t.Fatalf("fence 必须结算一次: %d", settleCalls)
	}

	// fence 与 config revision 不一致 → 确定性放弃。
	staleFence := settled
	staleFence.ConfigRevision = 99
	staleStore := &wgFakeOutboxStore{rows: []ProbeOutboxRow{{
		RequestID: "req-stale", AccountID: "acct", Reason: "manual",
		SourceFence: &staleFence, Deadline: now.Add(time.Minute),
	}}}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: staleStore, Boundary: &wgFakeBoundary{ok: true, revision: 1}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("stale fence 必须确定性收敛: %v", err)
	}
	if len(staleStore.completed) != 1 {
		t.Fatal("stale fence 行必须删行")
	}

	// boundary 瞬态错误 → 返回错误、行保持 pending。
	errorStore := &wgFakeOutboxStore{rows: []ProbeOutboxRow{{RequestID: "req-err", AccountID: "acct", Reason: "manual", Deadline: now.Add(time.Minute)}}}
	boundaryErr := errors.New("业务库读取失败")
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: errorStore, Boundary: &wgFakeBoundary{err: boundaryErr}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); !errors.Is(err, boundaryErr) {
		t.Fatalf("瞬态错误必须透传: %v", err)
	}
	if len(errorStore.completed) != 0 {
		t.Fatal("瞬态错误行不得删行")
	}

	// claim 失败 → 返回错误。
	failingStore := &wgFakeOutboxStore{claimErr: errors.New("claim 失败")}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: failingStore, Boundary: &wgFakeBoundary{ok: true}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err == nil {
		t.Fatal("claim 失败必须透传")
	}

	// 范围内账户 → runExplicitRequest 落 stale 终态（无直读器输入）并删行。
	inScope := &wgFakeOutboxStore{rows: []ProbeOutboxRow{{
		RequestID: "req-in", AccountID: "acct", Reason: "manual", Deadline: now.Add(time.Minute),
	}}}
	runner.SetProbeRequestDrain(&ProbeRequestDrain{Store: inScope, Boundary: &wgFakeBoundary{ok: true, revision: 7}})
	if err := runner.drainProbeRequestOutbox(context.Background(), lease); err != nil {
		t.Fatalf("范围内消费必须成功: %v", err)
	}
	if len(inScope.completed) != 1 {
		t.Fatal("范围内行必须删行")
	}
	found, err := store.HasRequest(context.Background(), "req-in")
	if err != nil || !found {
		t.Fatalf("stale 终态必须持久化: %v %v", found, err)
	}
}
