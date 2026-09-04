package oauthrefresh

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The refresh golden suite replays the Node refresh request/response shapes
// against a fixed mock token endpoint and asserts header/body/parsed-credential
// parity field by field.

func TestOpenAIRefreshRequestGolden(t *testing.T) {
	exchanger := &recordingExchanger{}
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	info, err := RefreshOpenAIToken(context.Background(), exchanger, "rt-old", "client-custom", now)
	if err != nil {
		t.Fatal(err)
	}
	request := exchanger.lastRequest()
	if request.URL != "https://auth.openai.com/oauth/token" {
		t.Fatalf("url=%s", request.URL)
	}
	if request.Headers["accept"] != "application/json" ||
		request.Headers["content-type"] != "application/x-www-form-urlencoded" ||
		request.Headers["content-length"] != itoa(len(request.Body)) {
		t.Fatalf("headers=%v", request.Headers)
	}
	values, err := url.ParseQuery(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("grant_type") != "refresh_token" ||
		values.Get("refresh_token") != "rt-old" ||
		values.Get("client_id") != "client-custom" ||
		values.Get("scope") != "openid profile email" {
		t.Fatalf("form=%v", values)
	}
	// Missing client id falls back to the Codex CLI constant.
	_, err = RefreshOpenAIToken(context.Background(), exchanger, "rt-old", "", now)
	if err != nil {
		t.Fatal(err)
	}
	values, _ = url.ParseQuery(exchanger.lastRequest().Body)
	if values.Get("client_id") != OpenAIOAuthClientID {
		t.Fatalf("fallback client_id=%q", values.Get("client_id"))
	}
	// Empty refresh token errors before the call.
	if _, err := RefreshOpenAIToken(context.Background(), exchanger, "  ", "", now); err == nil || err.Error() != "刷新令牌不能为空" {
		t.Fatalf("empty refresh token err=%v", err)
	}
	if info.ClientID != "client-custom" {
		t.Fatalf("info client=%q", info.ClientID)
	}
}

func TestOpenAIRefreshResponseGolden(t *testing.T) {
	const (
		// header.payload.signature with the OpenAI auth claim payload.
		idToken = "eyJhbGciOiJFUzI1NiJ9.eyJlbWFpbCI6InVzZXJAZXhhbXBsZS5jb20iLCJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoiYWNjLTEiLCJjaGF0Z3B0X3VzZXJfaWQiOiJ1c2VyLTEiLCJjaGF0Z3B0X3BsYW5fdHlwZSI6InBybyJ9fQ.sig"
	)
	exchanger := &recordingExchanger{respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		body := `{"access_token":"` + idToken + `","id_token":"` + idToken + `","refresh_token":"rt-new","expires_in":3600}`
		return TokenHTTPResponse{StatusCode: 200, Body: body}, nil
	}}
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	info, err := RefreshOpenAIToken(context.Background(), exchanger, "rt-old", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if info.AccessToken != idToken || info.IDToken != idToken || info.RefreshToken != "rt-new" || info.ExpiresIn != 3600 {
		t.Fatalf("info=%+v", info)
	}
	if info.ExpiresAt != "2026-09-04T09:00:00.000Z" {
		t.Fatalf("expires_at=%s", info.ExpiresAt)
	}
	if info.Email != "user@example.com" || info.AccountID != "acc-1" || info.ChatGPTUserID != "user-1" || info.PlanType != "pro" {
		t.Fatalf("claims=%+v", info)
	}
	credentials := BuildOpenAIOAuthCredentials(info, "rt-old")
	expect := map[string]any{
		"access_token":    idToken,
		"expires_at":      "2026-09-04T09:00:00.000Z",
		"client_id":       OpenAIOAuthClientID,
		"base_url":        "https://api.openai.com/v1",
		"refresh_token":   "rt-new",
		"id_token":        idToken,
		"email":           "user@example.com",
		"account_id":      "acc-1",
		"chatgpt_user_id": "user-1",
		"plan_type":       "pro",
	}
	assertCredentialsEqual(t, credentials, expect)
	// A missing rotated refresh token keeps the input one.
	info.RefreshToken = ""
	credentials = BuildOpenAIOAuthCredentials(info, "rt-old")
	if credentials["refresh_token"] != "rt-old" {
		t.Fatalf("fallback refresh_token=%v", credentials["refresh_token"])
	}
}

func TestOpenAIRefreshUpstreamErrorGolden(t *testing.T) {
	exchanger := &recordingExchanger{respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 401, Body: `{"error":"invalid_grant","error_description":"token expired"}`}, nil
	}}
	_, err := RefreshOpenAIToken(context.Background(), exchanger, "rt-old", "", time.Now())
	upstream, ok := AsUpstreamError(err)
	if !ok {
		t.Fatalf("err=%v want UpstreamError", err)
	}
	if upstream.Message != "OpenAI OAuth 令牌请求失败：HTTP 401，token expired" {
		t.Fatalf("message=%q", upstream.Message)
	}
}

func TestAnthropicRefreshGolden(t *testing.T) {
	exchanger := &recordingExchanger{}
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	if _, err := RefreshAnthropicToken(context.Background(), exchanger, "rt-a", "client-a", now); err != nil {
		t.Fatal(err)
	}
	request := exchanger.lastRequest()
	if request.URL != "https://platform.claude.com/v1/oauth/token" {
		t.Fatalf("url=%s", request.URL)
	}
	if request.Headers["content-type"] != "application/json" ||
		request.Headers["user-agent"] != "axios/1.13.6" ||
		request.Headers["accept"] != "application/json, text/plain, */*" {
		t.Fatalf("headers=%v", request.Headers)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(request.Body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["grant_type"] != "refresh_token" || payload["refresh_token"] != "rt-a" || payload["client_id"] != "client-a" {
		t.Fatalf("payload=%v", payload)
	}

	exchanger = &recordingExchanger{respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-a","refresh_token":"rt-a2","expires_in":3600,
			"scope":"user:inference","token_type":"bearer",
			"account":{"email_address":"a@b.c","uuid":"acc-uuid"},
			"organization":{"uuid":"org-uuid"}}`}, nil
	}}
	info, err := RefreshAnthropicToken(context.Background(), exchanger, "rt-a", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if info.ExpiresAt != "2026-09-04T09:00:00.000Z" || info.Email != "a@b.c" || info.AccountID != "acc-uuid" || info.OrganizationID != "org-uuid" {
		t.Fatalf("info=%+v", info)
	}
	credentials := BuildAnthropicOAuthCredentials(info, "rt-a")
	expect := map[string]any{
		"access_token": "at-a", "base_url": "https://api.anthropic.com/v1", "client_id": AnthropicOAuthClientID,
		"refresh_token": "rt-a2", "expires_at": "2026-09-04T09:00:00.000Z", "email": "a@b.c",
		"account_id": "acc-uuid", "organization_id": "org-uuid", "scope": "user:inference", "token_type": "bearer",
	}
	assertCredentialsEqual(t, credentials, expect)

	if _, err := RefreshAnthropicToken(context.Background(), exchanger, "", "", now); err == nil || err.Error() != "Anthropic Refresh Token 不能为空" {
		t.Fatalf("empty err=%v", err)
	}
}

func TestGeminiRefreshGolden(t *testing.T) {
	exchanger := &recordingExchanger{}
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	fallback := GeminiCredentialFallback{RefreshToken: "rt-g", OAuthType: "code_assist", ProjectID: "proj-1"}
	if _, err := RefreshGeminiToken(context.Background(), exchanger, "rt-g", fallback, now); err != nil {
		t.Fatal(err)
	}
	request := exchanger.lastRequest()
	if request.URL != "https://oauth2.googleapis.com/token" {
		t.Fatalf("url=%s", request.URL)
	}
	values, err := url.ParseQuery(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("grant_type") != "refresh_token" || values.Get("refresh_token") != "rt-g" ||
		values.Get("client_id") != GeminiCLIOAuthClientID || values.Get("client_secret") != GeminiCLIOAuthClientSecret {
		t.Fatalf("form=%v", values)
	}

	// expires_in 3600 keeps a 5-minute skew buffer (3600-300).
	exchanger = &recordingExchanger{respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-g","refresh_token":"rt-g2","expires_in":3600,"token_type":"Bearer"}`}, nil
	}}
	info, err := RefreshGeminiToken(context.Background(), exchanger, "rt-g", fallback, now)
	if err != nil {
		t.Fatal(err)
	}
	if info.ExpiresAt != "2026-09-04T08:55:00.000Z" {
		t.Fatalf("skew expires_at=%s", info.ExpiresAt)
	}
	if info.TierID != "gcp_standard" {
		t.Fatalf("tier=%q", info.TierID)
	}
	credentials := BuildGeminiOAuthCredentials(info, &fallback)
	if credentials["oauth_type"] != "code_assist" || credentials["project_id"] != "proj-1" || credentials["tier_id"] != "gcp_standard" {
		t.Fatalf("credentials=%v", credentials)
	}
	modes, ok := credentials["supported_endpoint_modes"].([]string)
	if !ok || len(modes) != 2 || modes[0] != "generate_content_json" {
		t.Fatalf("endpoint modes=%v", credentials["supported_endpoint_modes"])
	}

	// ai_studio resolves the stored client pair; missing pair errors.
	aiStudio := GeminiCredentialFallback{RefreshToken: "rt-g", OAuthType: "ai_studio", ClientID: "cid", ClientSecret: "sec", BaseURL: "https://custom.example"}
	exchanger = &recordingExchanger{}
	if _, err := RefreshGeminiToken(context.Background(), exchanger, "rt-g", aiStudio, now); err != nil {
		t.Fatal(err)
	}
	values, _ = url.ParseQuery(exchanger.lastRequest().Body)
	if values.Get("client_id") != "cid" || values.Get("client_secret") != "sec" {
		t.Fatalf("ai studio client=%v", values)
	}
	missingPair := GeminiCredentialFallback{RefreshToken: "rt-g", OAuthType: "ai_studio"}
	if _, err := RefreshGeminiToken(context.Background(), &recordingExchanger{}, "rt-g", missingPair, now); err == nil ||
		err.Error() != "Gemini AI Studio OAuth 需要同时配置 Client ID 和 Client Secret" {
		t.Fatalf("missing pair err=%v", err)
	}
	exchanger = &recordingExchanger{respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-g","expires_in":100}`}, nil
	}}
	info, _ = RefreshGeminiToken(context.Background(), exchanger, "rt-g", fallback, now)
	if info.ExpiresAt != "2026-09-04T08:00:30.000Z" {
		t.Fatalf("floor expires_at=%s", info.ExpiresAt)
	}
}

func TestGrokRefreshGolden(t *testing.T) {
	exchanger := &recordingExchanger{}
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	if _, err := RefreshGrokToken(context.Background(), exchanger, "rt-x", "client-x", now); err != nil {
		t.Fatal(err)
	}
	request := exchanger.lastRequest()
	if request.URL != "https://auth.x.ai/oauth2/token" {
		t.Fatalf("url=%s", request.URL)
	}
	if request.Headers["user-agent"] != "sub2api-grok-oauth/1.0" {
		t.Fatalf("user-agent=%q", request.Headers["user-agent"])
	}
	values, err := url.ParseQuery(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if values.Get("grant_type") != "refresh_token" || values.Get("refresh_token") != "rt-x" || values.Get("client_id") != "client-x" {
		t.Fatalf("form=%v", values)
	}

	// Missing expires_in falls back to the 6h TTL; a missing rotated refresh
	// token keeps the input one.
	exchanger = &recordingExchanger{respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at-x","token_type":"Bearer","scope":"openid"}`}, nil
	}}
	info, err := RefreshGrokToken(context.Background(), exchanger, "rt-x", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if info.ExpiresAt != "2026-09-04T14:00:00.000Z" {
		t.Fatalf("default ttl expires_at=%s", info.ExpiresAt)
	}
	if info.RefreshToken != "rt-x" || info.ClientID != GrokOAuthClientID {
		t.Fatalf("fallback info=%+v", info)
	}
	credentials := BuildGrokOAuthCredentials(info, "rt-x")
	if credentials["base_url"] != "https://cli-chat-proxy.grok.com/v1" || credentials["expires_at"] != "2026-09-04T14:00:00.000Z" {
		t.Fatalf("credentials=%v", credentials)
	}

	// 403 with an explicit entitlement denial maps to status 403.
	exchanger = &recordingExchanger{respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 403, Body: `{"error":"entitlement_denied"}`}, nil
	}}
	_, err = RefreshGrokToken(context.Background(), exchanger, "rt-x", "", now)
	upstream, ok := AsUpstreamError(err)
	if !ok || upstream.StatusCode != 403 {
		t.Fatalf("entitlement err=%v", err)
	}
	// 403 without denial language stays 502.
	exchanger = &recordingExchanger{respond: func(int, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 403, Body: `{"error":"other"}`}, nil
	}}
	_, err = RefreshGrokToken(context.Background(), exchanger, "rt-x", "", now)
	upstream, ok = AsUpstreamError(err)
	if !ok || upstream.StatusCode != 502 {
		t.Fatalf("non-entitlement err=%v", err)
	}
	if !strings.HasPrefix(upstream.Message, "Grok OAuth 令牌请求失败：HTTP 403，") {
		t.Fatalf("message=%q", upstream.Message)
	}
}

func TestNetworkFailureSurfacesTransportError(t *testing.T) {
	exchanger := ExchangerFunc(func(context.Context, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{}, errors.New("dial tcp: connection refused")
	})
	_, err := RefreshOpenAIToken(context.Background(), exchanger, "rt", "", time.Now())
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("transport err=%v", err)
	}
}

func assertCredentialsEqual(t *testing.T, got, want map[string]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("credential size %d != %d: %v", len(got), len(want), got)
	}
	for key, wantValue := range want {
		gotValue, ok := got[key]
		if !ok {
			t.Fatalf("missing key %q in %v", key, got)
		}
		switch wantTyped := wantValue.(type) {
		case string:
			if text, _ := gotValue.(string); text != wantTyped {
				t.Fatalf("key %q=%v want %v", key, gotValue, wantTyped)
			}
		default:
			if !credentialsEqual(map[string]any{key: gotValue}, map[string]any{key: wantValue}) {
				t.Fatalf("key %q=%v want %v", key, gotValue, wantValue)
			}
		}
	}
}
