// 本文件由 jobs internal/accounthealth/wg_input_scheduler_units_test.go 拆分而来（成对关系见 probe.go 包注释）：
// 承载随探针执行器闭包下沉的被移函数测试；同源文件的留守测试仍在 jobs 包内，
// 两侧不重复、不丢失。
package exactkeyprobe

import (
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
