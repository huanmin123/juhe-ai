package accountprobe

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
	"juhe-ai/backend-go/internal/store/port"
)

func TestOAuthRefreshRequestGolden(t *testing.T) {
	t.Setenv("GEMINI_CLI_OAUTH_CLIENT_SECRET", "")
	tests := []struct {
		name, wantURL, contentType string
		provider                   OAuthProvider
		values                     map[string]any
		assertBody                 func(*testing.T, []byte)
	}{
		{"OpenAI", "https://auth.openai.com/oauth/token", "application/x-www-form-urlencoded", OAuthOpenAI, map[string]any{"refresh_token": "openai-refresh"}, func(t *testing.T, body []byte) {
			values, _ := url.ParseQuery(string(body))
			if values.Get("client_id") != openAIClientID || values.Get("scope") != openAIRefreshScope || values.Get("refresh_token") != "openai-refresh" {
				t.Fatalf("form = %v", values)
			}
		}},
		{"Anthropic", "https://platform.claude.com/v1/oauth/token", "application/json", OAuthAnthropic, map[string]any{"refresh_token": "anthropic-refresh"}, func(t *testing.T, body []byte) {
			var values map[string]string
			_ = json.Unmarshal(body, &values)
			if values["client_id"] != anthropicClientID || values["grant_type"] != "refresh_token" {
				t.Fatalf("JSON = %v", values)
			}
		}},
		{"Gemini Code Assist built-in client", "https://oauth2.googleapis.com/token", "application/x-www-form-urlencoded", OAuthGemini, map[string]any{"refresh_token": "gemini-refresh", "oauth_type": "code_assist", "client_id": "legacy", "client_secret": "legacy-secret"}, func(t *testing.T, body []byte) {
			values, _ := url.ParseQuery(string(body))
			if values.Get("client_id") != geminiCLIClientID || values.Get("client_secret") != geminiCLISecret {
				t.Fatalf("form = %v", values)
			}
		}},
		{"xAI", "https://auth.x.ai/oauth2/token", "application/x-www-form-urlencoded", OAuthXAI, map[string]any{"refresh_token": "xai-refresh"}, func(t *testing.T, body []byte) {
			values, _ := url.ParseQuery(string(body))
			if values.Get("client_id") != xaiClientID || values.Get("refresh_token") != "xai-refresh" {
				t.Fatalf("form = %v", values)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			credentials, err := ParseOAuthCredentials(test.provider, test.values)
			if err != nil {
				t.Fatal(err)
			}
			request, err := BuildOAuthRefreshRequest(credentials)
			if err != nil {
				t.Fatal(err)
			}
			wantLength := fmt.Sprint(len(request.Body()))
			if test.provider == OAuthGemini {
				wantLength = ""
			}
			if request.URL() != test.wantURL || request.Header().Get("Content-Type") != test.contentType || request.Header().Get("Content-Length") != wantLength {
				t.Fatalf("request URL=%q headers=%v", request.URL(), request.Header())
			}
			wantTimeout := 25 * time.Second
			if test.provider == OAuthXAI {
				wantTimeout = time.Minute
			}
			if request.Timeout() != wantTimeout || request.MaxResponseBytes() != oauthResponseMaxSize {
				t.Fatalf("limits timeout=%s bytes=%d", request.Timeout(), request.MaxResponseBytes())
			}
			test.assertBody(t, request.Body())
		})
	}
}

func TestGeminiAIStudioRefreshRequiresAndUsesClientSecret(t *testing.T) {
	t.Setenv("GEMINI_OAUTH_CLIENT_ID", "")
	t.Setenv("GEMINI_OAUTH_CLIENT_SECRET", "")
	credentials, _ := ParseOAuthCredentials(OAuthGemini, map[string]any{"refresh_token": "refresh", "oauth_type": "ai_studio", "client_id": "client"})
	if _, err := BuildOAuthRefreshRequest(credentials); err == nil {
		t.Fatal("missing client_secret accepted")
	}
	credentials, _ = ParseOAuthCredentials(OAuthGemini, map[string]any{"refresh_token": "refresh", "oauth_type": "ai_studio", "client_id": "client", "client_secret": "secret"})
	request, err := BuildOAuthRefreshRequest(credentials)
	if err != nil {
		t.Fatal(err)
	}
	values, _ := url.ParseQuery(string(request.Body()))
	if values.Get("client_id") != "client" || values.Get("client_secret") != "secret" {
		t.Fatalf("form = %v", values)
	}
}

func TestGeminiAIStudioRefreshUsesNodeEnvironmentFallback(t *testing.T) {
	t.Setenv("GEMINI_OAUTH_CLIENT_ID", "env-client")
	t.Setenv("GEMINI_OAUTH_CLIENT_SECRET", "env-secret")
	credentials, _ := ParseOAuthCredentials(OAuthGemini, map[string]any{"refresh_token": "refresh", "oauth_type": "ai_studio"})
	request, err := BuildOAuthRefreshRequest(credentials)
	if err != nil {
		t.Fatal(err)
	}
	values, _ := url.ParseQuery(string(request.Body()))
	if values.Get("client_id") != "env-client" || values.Get("client_secret") != "env-secret" {
		t.Fatalf("form = %v", values)
	}
}

func TestGeminiCodeAssistRefreshExposesLegacyClientFallback(t *testing.T) {
	t.Setenv("GEMINI_CLI_OAUTH_CLIENT_SECRET", "runtime-secret")
	credentials, _ := ParseOAuthCredentials(OAuthGemini, map[string]any{
		"refresh_token": "refresh", "oauth_type": "code_assist", "client_id": "legacy-client", "client_secret": "legacy-secret",
	})
	request, err := BuildOAuthRefreshRequest(credentials)
	if err != nil {
		t.Fatal(err)
	}
	primary, _ := url.ParseQuery(string(request.Body()))
	if primary.Get("client_secret") != "runtime-secret" {
		t.Fatalf("primary did not use environment override: %v", primary)
	}
	if _, ok := request.FallbackForResponse(400, []byte(`{"error":"invalid_client"}`), false); ok {
		t.Fatal("invalid_client incorrectly enabled legacy fallback")
	}
	if _, ok := request.FallbackForResponse(400, []byte(`{"error":"unauthorized_client"}`), true); ok {
		t.Fatal("truncated response enabled fallback")
	}
	fallback, ok := request.FallbackForResponse(400, []byte(`{"error":"unauthorized_client"}`), false)
	if !ok {
		t.Fatal("legacy fallback missing")
	}
	values, _ := url.ParseQuery(string(fallback.Body()))
	if values.Get("client_id") != "legacy-client" || values.Get("client_secret") != "legacy-secret" {
		t.Fatalf("fallback form = %v", values)
	}
	if _, nested := fallback.FallbackForResponse(400, []byte(`{"error":"unauthorized_client"}`), false); nested {
		t.Fatal("fallback recursively contains another fallback")
	}
	if request.MaxAttempts() != 4 || request.RetryBackoff(1) != time.Second || request.RetryBackoff(2) != 2*time.Second || request.RetryBackoff(3) != 4*time.Second {
		t.Fatalf("retry policy attempts=%d", request.MaxAttempts())
	}
	for _, code := range []string{"invalid_grant", "invalid_client", "unauthorized_client", "access_denied"} {
		if request.RetryableResponse(500, 1, []byte(code)) {
			t.Fatalf("%s marked retryable", code)
		}
	}
	if !request.RetryableResponse(500, 1, []byte(`{"error":"server_error"}`)) || request.RetryableResponse(500, 4, nil) {
		t.Fatal("Gemini retryability mismatch")
	}
	if request.Timeout() != 25*time.Second || request.MaxResponseBytes() != 256<<10 {
		t.Fatalf("limits timeout=%s bytes=%d", request.Timeout(), request.MaxResponseBytes())
	}
	result, err := ParseOAuthRefreshResponse(request, 200, []byte(`{"access_token":"access","expires_in":3600}`), time.Now())
	if err != nil || !result.RequiresGeminiEnrichment() {
		t.Fatalf("result=%v err=%v", result, err)
	}
	patch, err := MergeOAuthRefreshCredentials(credentials, result)
	modes, _ := patch.Values()["supported_endpoint_modes"].([]string)
	if err != nil || len(modes) != 2 {
		t.Fatalf("patch=%v err=%v", patch, err)
	}
}

func TestGeminiOAuthTypeDefaultsAreContextSpecific(t *testing.T) {
	t.Setenv("GEMINI_CLI_OAUTH_CLIENT_SECRET", "")
	if got := geminiRefreshOAuthType(nil); got != "code_assist" {
		t.Fatalf("refresh default = %q", got)
	}
	if got := geminiRuntimeOAuthType(nil); got != "ai_studio" {
		t.Fatalf("runtime default = %q", got)
	}
	legacyProject := map[string]any{"project_id": "legacy-project"}
	if got := geminiRuntimeOAuthType(legacyProject); got != "code_assist" {
		t.Fatalf("legacy project runtime type = %q", got)
	}
	legacyHintsWithoutProject := map[string]any{"base_url": "https://cloudcode-pa.googleapis.com", "client_id": geminiCLIClientID}
	if got := geminiRuntimeOAuthType(legacyHintsWithoutProject); got != "ai_studio" {
		t.Fatalf("non-authoritative legacy hints changed runtime type = %q", got)
	}
	for _, oauthType := range []string{"code_assist", "google_one", "ai_studio"} {
		values := map[string]any{"oauth_type": oauthType}
		if got := geminiRefreshOAuthType(values); got != oauthType {
			t.Fatalf("refresh explicit %s = %q", oauthType, got)
		}
		if got := geminiRuntimeOAuthType(values); got != oauthType {
			t.Fatalf("runtime explicit %s = %q", oauthType, got)
		}
	}
	refreshCredentials, _ := ParseOAuthCredentials(OAuthGemini, map[string]any{"refresh_token": "refresh"})
	refreshRequest, err := BuildOAuthRefreshRequest(refreshCredentials)
	if err != nil {
		t.Fatal(err)
	}
	refreshForm, _ := url.ParseQuery(string(refreshRequest.Body()))
	if refreshForm.Get("client_id") != geminiCLIClientID || refreshForm.Get("client_secret") != geminiCLISecret {
		t.Fatalf("refresh default form = %v", refreshForm)
	}
	runtimeCandidate := oauthCandidateSpec("gemini", "gemini", "google_oauth", "https://generativelanguage.googleapis.com", map[string]any{"access_token": "access"})
	prepared, err := PrepareRequest(runtimeCandidate, RequestInput{Mode: ModeGenerateContentJSON, Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := PrepareOAuthAttempt(runtimeCandidate, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.URL() != "https://generativelanguage.googleapis.com/v1beta/models/model:generateContent" || attempt.EvidenceMode() != ModeGenerateContentJSON {
		t.Fatalf("runtime default URL=%q evidence=%s", attempt.URL(), attempt.EvidenceMode())
	}
}

func TestShouldRefreshOAuthMatchesProviderLeadWindows(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		provider OAuthProvider
		expiry   time.Duration
		want     bool
	}{
		{OAuthOpenAI, 59 * time.Second, true}, {OAuthOpenAI, time.Minute, false},
		{OAuthAnthropic, time.Minute, true}, {OAuthGemini, time.Minute, true},
		{OAuthXAI, 5 * time.Minute, true}, {OAuthXAI, 5*time.Minute + time.Second, false},
	} {
		credentials, _ := ParseOAuthCredentials(test.provider, map[string]any{"access_token": "access", "refresh_token": "refresh", "expires_at": now.Add(test.expiry).Format(time.RFC3339Nano)})
		if got := ShouldRefreshOAuth(credentials, now); got != test.want {
			t.Fatalf("provider=%s expiry=%s got=%v", test.provider, test.expiry, got)
		}
	}
	withoutRefresh, _ := ParseOAuthCredentials(OAuthOpenAI, map[string]any{"access_token": "access", "expires_at": now.Add(time.Hour).Format(time.RFC3339)})
	if ShouldRefreshOAuth(withoutRefresh, now) {
		t.Fatal("fresh access-only credential should remain usable")
	}
	expiredWithoutRefresh, _ := ParseOAuthCredentials(OAuthOpenAI, map[string]any{"access_token": "access", "expires_at": now.Add(-time.Hour).Format(time.RFC3339)})
	if ShouldRefreshOAuth(expiredWithoutRefresh, now) {
		t.Fatal("access-only credential cannot be refreshed")
	}
	for _, provider := range []OAuthProvider{OAuthAnthropic, OAuthGemini, OAuthXAI} {
		fresh, _ := ParseOAuthCredentials(provider, map[string]any{"access_token": "access", "expires_at": now.Add(time.Hour).Format(time.RFC3339)})
		if ShouldRefreshOAuth(fresh, now) {
			t.Fatalf("provider=%s fresh access-only credential marked for refresh", provider)
		}
		expired, _ := ParseOAuthCredentials(provider, map[string]any{"access_token": "access", "expires_at": now.Add(-time.Hour).Format(time.RFC3339)})
		if !ShouldRefreshOAuth(expired, now) {
			t.Fatalf("provider=%s expired access-only credential was not marked for refresh", provider)
		}
		if _, err := BuildOAuthRefreshRequest(expired); err == nil {
			t.Fatalf("provider=%s access-only refresh unexpectedly built", provider)
		}
	}
}

func TestOAuthRefreshResponseAndMergeGolden(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	openAIClaims := map[string]any{"email": "owner@example.test", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct", "chatgpt_plan_type": "pro"}}
	openAIJWT := oauthTestJWT(t, openAIClaims)
	tests := []struct {
		name       string
		provider   OAuthProvider
		current    map[string]any
		response   string
		wantExpiry time.Duration
		checks     func(*testing.T, map[string]any)
	}{
		{"OpenAI numeric expires and claims", OAuthOpenAI, map[string]any{"refresh_token": "old-refresh", "metadata_marker": "keep"}, fmt.Sprintf(`{"access_token":"new-access","id_token":%q,"expires_in":"3600"}`, openAIJWT), time.Hour, func(t *testing.T, values map[string]any) {
			if values["account_id"] != "acct" || values["plan_type"] != "pro" || values["base_url"] != "https://api.openai.com/v1" {
				t.Fatalf("values=%v", values)
			}
		}},
		{"Anthropic optional expiry and metadata", OAuthAnthropic, map[string]any{"refresh_token": "old-refresh", "base_url": "https://anthropic.proxy/v1", "metadata_marker": "keep"}, `{"access_token":"new-access","expires_in":7200,"account":{"email_address":"a@example.test","uuid":"a1"},"organization":{"uuid":"o1"}}`, 2 * time.Hour, func(t *testing.T, values map[string]any) {
			if values["email"] != "a@example.test" || values["organization_id"] != "o1" || values["base_url"] != "https://anthropic.proxy/v1" {
				t.Fatalf("values=%v", values)
			}
		}},
		{"Gemini expiry safety margin", OAuthGemini, map[string]any{"refresh_token": "old-refresh", "oauth_type": "ai_studio", "client_id": "client", "client_secret": "secret", "base_url": "https://generativelanguage.googleapis.com", "scope": "stale-scope"}, `{"access_token":"new-access","expires_in":3600,"scope":"response-scope"}`, 55 * time.Minute, func(t *testing.T, values map[string]any) {
			if values["client_secret"] != "secret" || values["oauth_type"] != "ai_studio" || values["scope"] != "response-scope" {
				t.Fatalf("values=%v", values)
			}
			if _, exists := values["supported_endpoint_modes"]; exists {
				t.Fatalf("AI Studio received Code Assist modes: %v", values)
			}
		}},
		{"xAI default expiry and custom base", OAuthXAI, map[string]any{"refresh_token": "old-refresh", "base_url": "https://api.x.ai/v1"}, `{"access_token":"new-access"}`, 6 * time.Hour, func(t *testing.T, values map[string]any) {
			if values["token_type"] != "Bearer" || values["base_url"] != "https://api.x.ai/v1" {
				t.Fatalf("values=%v", values)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current, _ := ParseOAuthCredentials(test.provider, test.current)
			request, err := BuildOAuthRefreshRequest(current)
			if err != nil {
				t.Fatal(err)
			}
			result, err := ParseOAuthRefreshResponse(request, 200, []byte(test.response), now)
			if err != nil {
				t.Fatal(err)
			}
			patch, err := MergeOAuthRefreshCredentials(current, result)
			if err != nil {
				t.Fatal(err)
			}
			values := patch.Values()
			if values["refresh_token"] != "old-refresh" || values["metadata_marker"] != "keep" && test.provider != OAuthGemini && test.provider != OAuthXAI {
				t.Fatalf("merge lost current fields: %v", values)
			}
			expiresAt, err := time.Parse(time.RFC3339, values["expires_at"].(string))
			if err != nil || !expiresAt.Equal(now.Add(test.wantExpiry)) {
				t.Fatalf("expires_at=%v err=%v", values["expires_at"], err)
			}
			test.checks(t, values)
		})
	}
}

func TestOAuthResponseRejectsInvalidCompletionEvidence(t *testing.T) {
	credentials, _ := ParseOAuthCredentials(OAuthOpenAI, map[string]any{"refresh_token": "refresh"})
	request, _ := BuildOAuthRefreshRequest(credentials)
	for _, input := range []struct {
		status int
		body   string
	}{{500, `{"access_token":"secret"}`}, {200, `{"expires_in":3600}`}, {200, `{"access_token":"secret"}`}, {200, `{"access_token":"secret","expires_in":0}`}, {200, `{"access_token":"secret","expires_in":10000000000}`}, {200, `{"access_token":"secret","expires_in":3600}{}`}, {200, `not-json`}} {
		if _, err := ParseOAuthRefreshResponse(request, input.status, []byte(input.body), time.Now()); err == nil {
			t.Fatalf("accepted status=%d body=%s", input.status, input.body)
		}
	}
	anthropic, _ := ParseOAuthCredentials(OAuthAnthropic, map[string]any{"refresh_token": "refresh"})
	anthropicRequest, _ := BuildOAuthRefreshRequest(anthropic)
	now := time.Now().UTC()
	result, err := ParseOAuthRefreshResponse(anthropicRequest, 200, []byte(`{"access_token":"secret","expires_in":"1.5"}`), now)
	expiresAt, _ := time.Parse(time.RFC3339Nano, oauthString(result.values, "expires_at"))
	if err != nil || !expiresAt.Equal(now.Add(time.Second)) {
		t.Fatalf("Anthropic numeric expires_in should truncate: result=%v err=%v", result, err)
	}
}

func TestPrepareOAuthAttemptProviderGolden(t *testing.T) {
	tests := []struct {
		name                           string
		candidate                      gatewaycandidatewindow.Candidate
		input                          RequestInput
		wantURL, wantHeader, wantValue string
		check                          func(*testing.T, OAuthAttempt)
	}{
		{"OpenAI Codex", oauthCandidateSpec("openai", "openai", "oauth", "https://ignored.example/v1", map[string]any{"access_token": "oa", "account_id": "acct"}), RequestInput{Mode: ModeResponsesSSE, Model: "model", OAuth: false, ClientCompatibility: "codex_responses"}, "https://chatgpt.com/backend-api/codex/responses", "ChatGPT-Account-Id", "acct", func(t *testing.T, a OAuthAttempt) {
			var body map[string]any
			_ = json.Unmarshal(a.Body(), &body)
			if body["store"] != false || body["stream"] != true || body["max_output_tokens"] != nil || a.EvidenceMode() != ModeResponsesSSE || a.Header().Get("Accept") != "text/event-stream" {
				t.Fatalf("body=%v", body)
			}
		}},
		{"Anthropic", oauthCandidateSpec("anthropic", "anthropic", "oauth", "https://api.anthropic.com/v1", map[string]any{"access_token": "an"}), RequestInput{Mode: ModeMessagesJSON, Model: "model", SessionID: "session", Today: "2026-07-28", WorkingDirectory: `F:\work`}, "https://api.anthropic.com/v1/messages?beta=true", "Anthropic-Version", "2023-06-01", func(t *testing.T, a OAuthAttempt) {
			if a.Header().Get("Anthropic-Beta") != "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14,effort-2025-11-24" || a.Header().Get("X-Stainless-Lang") != "js" {
				t.Fatalf("headers=%v", a.Header())
			}
		}},
		{"Gemini AI Studio", oauthCandidateSpec("gemini", "gemini", "google_oauth", "https://generativelanguage.googleapis.com", map[string]any{"access_token": "ge", "oauth_type": "ai_studio", "quota_project_id": "quota"}), RequestInput{Mode: ModeGenerateContentJSON, Model: "model"}, "https://generativelanguage.googleapis.com/v1beta/models/model:generateContent", "X-Goog-User-Project", "quota", nil},
		{"Gemini Code Assist", oauthCandidateSpec("gemini", "gemini", "google_oauth", "https://cloudcode-pa.googleapis.com", map[string]any{"access_token": "gc", "oauth_type": "code_assist", "project_id": "project"}), RequestInput{Mode: ModeGenerateContentJSON, Model: "model"}, "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse", "User-Agent", "GeminiCLI/0.1.5 (Windows; AMD64)", func(t *testing.T, a OAuthAttempt) {
			var body map[string]any
			_ = json.Unmarshal(a.Body(), &body)
			if body["model"] != "model" || body["project"] != "project" || body["request"] == nil || a.EvidenceMode() != ModeGenerateContentSSE {
				t.Fatalf("body=%v", body)
			}
			evidence, err := InspectEvidence(a.EvidenceMode(), []byte("data: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}]}}\n\n"), false)
			if err != nil || !evidence.Complete {
				t.Fatalf("nested Code Assist evidence=%+v err=%v", evidence, err)
			}
		}},
		{"xAI CLI", oauthCandidateSpec("openai", "xai", "oauth", "https://cli-chat-proxy.grok.com/v1", map[string]any{"access_token": "xa", "base_url": "https://cli-chat-proxy.grok.com/v1"}), RequestInput{Mode: ModeResponsesJSON, Model: "model", OAuth: true}, "https://cli-chat-proxy.grok.com/v1/responses", "X-Xai-Token-Auth", "xai-grok-cli", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := PrepareRequest(test.candidate, test.input)
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := PrepareOAuthAttempt(test.candidate, prepared)
			if err != nil {
				t.Fatal(err)
			}
			if attempt.URL() != test.wantURL || attempt.Header().Get(test.wantHeader) != test.wantValue || !strings.HasPrefix(attempt.Header().Get("Authorization"), "Bearer ") {
				t.Fatalf("url=%q headers=%v", attempt.URL(), attempt.Header())
			}
			if test.check != nil {
				test.check(t, attempt)
			}
		})
	}
}

func TestOAuthSpecValuesAreRedactedAndDefensivelyCopied(t *testing.T) {
	credentials, _ := ParseOAuthCredentials(OAuthOpenAI, map[string]any{"refresh_token": "super-secret"})
	request, _ := BuildOAuthRefreshRequest(credentials)
	for name, value := range map[string]any{"credentials": credentials, "request": request, "patch": OAuthCredentialPatch{values: map[string]any{"refresh_token": "super-secret"}}, "attempt": OAuthAttempt{body: []byte("super-secret")}} {
		if got := fmt.Sprintf("%v", value); got != "[REDACTED]" || strings.Contains(got, "super-secret") {
			t.Fatalf("%s format=%q", name, got)
		}
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != "{}" {
			t.Fatalf("%s JSON=%s err=%v", name, encoded, err)
		}
	}
	body := request.Body()
	body[0] = 'X'
	if string(request.Body()) == string(body) {
		t.Fatal("Body() exposed mutable storage")
	}
	header := request.Header()
	header.Set("Authorization", "secret")
	if request.Header().Get("Authorization") != "" {
		t.Fatal("Header() exposed mutable storage")
	}
}

func TestXAIModelFallbackMatchesAccessDeniedContract(t *testing.T) {
	candidate := oauthCandidateSpec("openai", "xai", "oauth", "https://cli-chat-proxy.grok.com/v1", map[string]any{"access_token": "token", "base_url": "https://cli-chat-proxy.grok.com/v1"})
	prepared, err := PrepareRequest(candidate, RequestInput{Mode: ModeResponsesJSON, Model: "model", OAuth: true})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := PrepareOAuthAttempt(candidate, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := attempt.XAIModelFallback(403, []byte("access denied"), true); ok {
		t.Fatal("truncated denial enabled fallback")
	}
	fallback, ok := attempt.XAIModelFallback(403, []byte("Access Denied"), false)
	if !ok || fallback.URL() != "https://api.x.ai/v1/responses" || fallback.Header().Get("X-Xai-Token-Auth") != "" || fallback.Header().Get("User-Agent") != "" || fallback.Header().Get("Authorization") != "Bearer token" {
		t.Fatalf("fallback=%v ok=%v headers=%v", fallback, ok, fallback.Header())
	}
}

func TestOpenAIOAuthAttemptReusesExistingCodexSessionMetadata(t *testing.T) {
	candidate := oauthCandidateSpec("openai", "gpt", "oauth", "https://ignored.example/v1", map[string]any{"access_token": "token"})
	prepared, err := PrepareRequest(candidate, RequestInput{Mode: ModeResponsesSSE, Model: "model", OAuth: true, ClientCompatibility: "codex_responses"})
	if err != nil {
		t.Fatal(err)
	}
	sessionID := prepared.Request.Header.Get("Session-Id")
	attempt, err := PrepareOAuthAttempt(candidate, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if sessionID == "" || attempt.Header().Get("Session-Id") != sessionID {
		t.Fatalf("session before=%q after=%q", sessionID, attempt.Header().Get("Session-Id"))
	}
}

func TestOpenAIOAuthJSONConfigurationUsesSSEWireEvidence(t *testing.T) {
	candidate := oauthCandidateSpec("openai", "gpt", "oauth", "https://ignored.example/v1", map[string]any{"access_token": "token"})
	prepared, err := PrepareRequest(candidate, RequestInput{Mode: ModeResponsesJSON, Model: "model", OAuth: true, ClientCompatibility: "codex_responses"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := PrepareOAuthAttempt(candidate, prepared)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(attempt.Body(), &body)
	if attempt.EvidenceMode() != ModeResponsesSSE || body["stream"] != true || body["max_output_tokens"] != nil {
		t.Fatalf("mode=%s body=%v", attempt.EvidenceMode(), body)
	}
	evidence, err := InspectEvidence(attempt.EvidenceMode(), []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"object\":\"response\"}}\n\n"), false)
	if err != nil || !evidence.Complete {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func oauthCandidateSpec(protocol, provider, accountType, baseURL string, credentials map[string]any) gatewaycandidatewindow.Candidate {
	return gatewaycandidatewindow.Candidate{Projection: port.GatewayAccountCandidate{AccountID: "account", ProviderCode: provider, ProviderProtocolProfileID: "profile", ProtocolCode: protocol, Type: accountType}, Credentials: gatewaycandidatewindow.NewCredentialSet(credentials), DefaultBaseURL: baseURL, SupportedModels: []string{"model"}}
}

func oauthTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
