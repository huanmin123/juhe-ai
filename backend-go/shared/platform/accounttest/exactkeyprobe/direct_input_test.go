// 本文件由 jobs internal/accounthealth/direct_input_test.go 拆分而来（成对关系见 probe.go 包注释）：
// 承载随探针执行器闭包下沉的被移函数测试；同源文件的留守测试仍在 jobs 包内，
// 两侧不重复、不丢失。
package exactkeyprobe

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDirectInputAcceptsOAuthResponsesSSE(t *testing.T) {
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	err := validateDirectAccount(DirectAccount{
		ID:                   "oauth-account",
		ConfigRevision:       1,
		DispatchRevision:     1,
		Provider:             "openai",
		Type:                 "oauth",
		Status:               "active",
		Schedulable:          true,
		EndpointMode:         "responses_sse",
		HealthModel:          "gpt-test",
		CredentialsEncrypted: "encrypted",
	}, now)
	if err != nil {
		t.Fatalf("OAuth responses_sse must remain supported by the current J1 profile scope: %v", err)
	}
}

func TestDirectInputRejectsOAuthForOpenAICompatibleProfile(t *testing.T) {
	if isSupportedDirectProfile("profile_openai_openai_v1", "openai", "oauth", "responses_json") {
		t.Fatal("OpenAI-compatible profile must remain API-key-only")
	}
}

func TestDirectInputRejectsProtocolMetadataMismatch(t *testing.T) {
	if err := validateDirectProtocolMetadata("profile_gpt_openai_v1", "anthropic", "v1"); err == nil {
		t.Fatal("profile/protocol mismatch must be rejected")
	}
}

func TestDirectInputUsesGrokCLIProxyForOAuthWithoutBaseURL(t *testing.T) {
	baseURL, err := directBaseURL(map[string]json.RawMessage{}, DirectAccount{ProtocolProfileID: "profile_xai_openai_v1", Type: "oauth"}, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if baseURL != "https://cli-chat-proxy.grok.com/v1" {
		t.Fatalf("xAI OAuth base URL = %q", baseURL)
	}
}

func TestDirectInputUsesCodexBaseURLForOAuthEvenWhenCredentialHasOpenAIBaseURL(t *testing.T) {
	baseURL, err := directBaseURL(map[string]json.RawMessage{
		"base_url": json.RawMessage(`"https://api.openai.com/v1"`),
	}, DirectAccount{ProtocolProfileID: "profile_gpt_openai_v1", Type: "oauth"}, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if baseURL != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("GPT OAuth base URL = %q", baseURL)
	}
}

func TestDirectInputSupportsGeminiGoogleOAuthProfile(t *testing.T) {
	secret := "j1-direct-input-secret"
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	credentials, err := EncryptV1Envelope(secret, []byte(`{"access_token":"google-token","expires_at":"2030-08-16T02:00:00Z","quota_project_id":"quota-1","oauth_type":"ai_studio","base_url":"https://generativelanguage.googleapis.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	input, err := (DirectInput{
		Account:      DirectAccount{ID: "gemini-account", ConfigRevision: 1, DispatchRevision: 1, Provider: "gemini", ProtocolProfileID: "profile_gemini_native_v1beta", ProtocolCode: "gemini", ProtocolVersion: "v1beta", Type: "google_oauth", Status: "pending_test", EndpointMode: "generate_content_json", HealthModel: "gemini-test", CredentialsEncrypted: credentials},
		Binding:      DirectBinding{GroupID: "group-1", Enabled: true},
		InputVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}).ToInput(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if input.Provider != "gemini" || input.OAuthAccess == nil || input.OAuthQuotaProjectID != "quota-1" || input.EndpointMode != "generate_content_json" {
		t.Fatalf("mapped Gemini direct input = %#v", input)
	}
}

func TestDirectInputToInputUsesEffectiveSourceAndProxy(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentialCiphertext, err := EncryptV1Envelope(secret, []byte(`{"api_keys":["key-a","key-b"],"base_url":"https://upstream.example/"}`))
	if err != nil {
		t.Fatal(err)
	}
	passwordCiphertext, err := EncryptV1Envelope(secret, []byte(`{"password":"p@ss"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	input, err := (DirectInput{
		Account:      DirectAccount{ID: "account-1", ConfigRevision: 7, DispatchRevision: 9, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, EndpointMode: "responses_json", HealthModel: "gpt-test", CredentialsEncrypted: credentialCiphertext},
		Binding:      DirectBinding{GroupID: "group-1", Enabled: true},
		Proxy:        &DirectProxy{ID: "proxy-1", Enabled: true, Type: "socks5", Host: "127.0.0.1", Port: 1080, Username: "user", PasswordEncrypted: passwordCiphertext},
		InputVersion: 3, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1",
		Schedule: Schedule{HealthIntervalMS: int64(time.Hour / time.Millisecond), FailureThreshold: 2, FailureRetryMS: int64(time.Minute / time.Millisecond), CooldownNeutralBaseMS: int64(time.Second / time.Millisecond), CooldownNeutralMaxMS: int64(time.Minute / time.Millisecond), CooldownFailureBackoffMS: int64(time.Minute / time.Millisecond)},
	}).ToInput(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if input.BaseURL != "https://upstream.example" || len(input.APIKeys) != 2 || input.Proxy == nil {
		t.Fatalf("mapped input = %#v", input)
	}
	proxyPlaintext, err := DecryptV1Envelope(secret, input.Proxy.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var proxyPayload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(proxyPlaintext, &proxyPayload); err != nil {
		t.Fatal(err)
	}
	if proxyPayload.URL != "socks5h://user:p%40ss@127.0.0.1:1080" {
		t.Fatalf("proxy = %q", proxyPayload.URL)
	}
}

func TestDirectInputNormalizesGPTProviderToOpenAIProtocol(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentials, err := EncryptV1Envelope(secret, []byte(`{"api_key":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	input, err := (DirectInput{
		Account:      DirectAccount{ID: "gpt-account", ConfigRevision: 1, DispatchRevision: 1, Provider: "gpt", Type: "api_key", Status: "pending_test", EndpointMode: "responses_sse", HealthModel: "gpt-test", CredentialsEncrypted: credentials},
		Binding:      DirectBinding{GroupID: "group-1", Enabled: true},
		InputVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}).ToInput(secret, now)
	if err != nil {
		t.Fatalf("GPT OpenAI-v1 provider must be accepted: %v", err)
	}
	if input.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", input.Provider)
	}
	if input.EndpointMode != "responses_sse" {
		t.Fatalf("endpoint mode = %q, want responses_sse", input.EndpointMode)
	}
}

func TestDirectInputRejectsIncompleteAuthorization(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentials, err := EncryptV1Envelope(secret, []byte(`{"api_key":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	_, err = (DirectInput{
		Account:       DirectAccount{ID: "account-1", ConfigRevision: 1, DispatchRevision: 1, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, EndpointMode: "chat_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials},
		Authorization: &DirectAuthorization{ID: "auth-1", Status: "active", QuotaEligible: false},
		Source:        &DirectSource{ID: "source-1", ConfigRevision: 1, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, CredentialsEncrypted: credentials},
		Binding:       DirectBinding{GroupID: "group-1", Enabled: true, AuthorizationBindingID: "auth-1"},
		InputVersion:  1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}).ToInput(secret, now)
	if err == nil {
		t.Fatal("expected authorization quota failure")
	}
}

func TestDirectInputAllowsOwnerCooldownFenceWithoutSourceRevision(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentials, err := EncryptV1Envelope(secret, []byte(`{"api_key":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	fence := &CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "owner-generation"}
	cooldownUntil := now.Add(-time.Second)
	input, err := (DirectInput{
		Account:      DirectAccount{ID: "account-owner", ConfigRevision: 1, DispatchRevision: 1, Provider: "openai", Type: "api_key", Status: "temporary_unavailable", Schedulable: true, EndpointMode: "chat_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials, CooldownUntil: &cooldownUntil, Cooldown: fence},
		Binding:      DirectBinding{GroupID: "group-1", Enabled: true},
		InputVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}).ToInput(secret, now)
	if err != nil {
		t.Fatalf("owner cooldown input must be accepted: %v", err)
	}
	if !validCooldownFence(input.Cooldown, input) {
		t.Fatal("owner cooldown fence without source revision must remain valid")
	}
}

func TestDirectInputRejectsUnavailableProxy(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentials, err := EncryptV1Envelope(secret, []byte(`{"api_key":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	base := DirectInput{
		Account:      DirectAccount{ID: "account-proxy", ConfigRevision: 1, DispatchRevision: 1, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, EndpointMode: "chat_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials},
		Binding:      DirectBinding{GroupID: "group-1", Enabled: true},
		InputVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}
	for _, proxy := range []*DirectProxy{
		{ID: "disabled", Enabled: false, Type: "http", Host: "127.0.0.1", Port: 8080},
		{ID: "bad-password", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 8080, Username: "user", PasswordEncrypted: "not-an-envelope"},
	} {
		candidate := base
		candidate.Proxy = proxy
		if _, err := candidate.ToInput(secret, now); err == nil {
			t.Fatalf("proxy %q must fail closed", proxy.ID)
		}
	}
}

func TestDirectInputAllowsAuthorizedCooldownFenceWithSourceRevision(t *testing.T) {
	secret := "j1-direct-input-secret"
	credentials, err := EncryptV1Envelope(secret, []byte(`{"api_key":"key"}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	sourceRevision := int64(7)
	cooldownUntil := now.Add(-time.Second)
	fence := &CooldownFence{ObservationStartedAt: now.Add(-time.Minute), Generation: "authorized-generation", SourceConfigRevision: &sourceRevision}
	input, err := (DirectInput{
		Account:       DirectAccount{ID: "authorized-cooldown", ConfigRevision: 1, DispatchRevision: 1, Provider: "openai", Type: "api_key", Status: "temporary_unavailable", Schedulable: true, EndpointMode: "chat_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials, CooldownUntil: &cooldownUntil, Cooldown: fence},
		Authorization: &DirectAuthorization{ID: "auth-1", Status: "active", QuotaEligible: true},
		Source:        &DirectSource{ID: "source-1", ConfigRevision: sourceRevision, Provider: "openai", Type: "api_key", Status: "active", Schedulable: true, CredentialsEncrypted: credentials},
		Binding:       DirectBinding{GroupID: "group-1", Enabled: true, AuthorizationBindingID: "auth-1"},
		InputVersion:  1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1", Schedule: Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
	}).ToInput(secret, now)
	if err != nil {
		t.Fatalf("authorized cooldown input must be accepted: %v", err)
	}
	if !validCooldownFence(input.Cooldown, input) {
		t.Fatal("authorized cooldown fence must retain source revision")
	}
}

// TestDirectInputAcceptsGeminiOpenAIChatProfileMetadata 保护 BUG-0262 修复：
// profile_gemini_openai_chat_v1beta 的规范协议是 openai/v1（provider_protocol_profiles
// 种子），J1 输入校验必须对照规范元数据，不得按 profile 名子串猜测协议。
func TestDirectInputAcceptsGeminiOpenAIChatProfileMetadata(t *testing.T) {
	if err := validateDirectProtocolMetadata("profile_gemini_openai_chat_v1beta", "openai", "v1"); err != nil {
		t.Fatalf("gemini openai-chat 规范协议必须通过校验: %v", err)
	}
	if err := validateDirectProtocolMetadata("profile_gemini_openai_chat_v1beta", "gemini", "v1beta"); err == nil {
		t.Fatal("gemini openai-chat profile 携带 gemini 原生协议必须拒绝")
	}
	if err := validateDirectProtocolMetadata("profile_gemini_native_v1beta", "gemini", "v1beta"); err != nil {
		t.Fatalf("gemini native 规范协议必须通过校验: %v", err)
	}
	if err := validateDirectProtocolMetadata("profile_nonexistent_v9", "openai", "v1"); err == nil {
		t.Fatal("未注册协议元数据的 profile 必须拒绝")
	}
}

// TestDirectProbeTargetSharesHybridRouteWithManualDiagnostics protects the
// common resolver used by J1 and accountprobe. A change in endpoint family,
// streaming intent, or model must therefore affect both callers together.
func TestDirectProbeTargetSharesHybridRouteWithManualDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		sourceMode string
		family     string
		wantMode   string
	}{
		{name: "chat to messages", sourceMode: "chat_json", family: "messages", wantMode: "messages_json"},
		{name: "responses stream to chat", sourceMode: "responses_sse", family: "chat_completions", wantMode: "chat_sse"},
		{name: "messages to gemini", sourceMode: "messages_json", family: "generate_content", wantMode: "generate_content_json"},
		{name: "gemini stream to messages", sourceMode: "generate_content_sse", family: "messages", wantMode: "messages_sse"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, model, err := directProbeTarget(DirectAccount{
				ProtocolProfileID:            "profile_hybrid_openai_chat_v1",
				EndpointMode:                 test.sourceMode,
				HealthModel:                  "client-model",
				MappedUpstreamModel:          "upstream-model",
				MappedUpstreamEndpointFamily: test.family,
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode != test.wantMode || model != "upstream-model" {
				t.Fatalf("route mode=%q model=%q", mode, model)
			}
		})
	}
}
