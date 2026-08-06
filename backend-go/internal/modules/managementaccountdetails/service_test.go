package managementaccountdetails

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestServiceDetailLevelsPreserveRawOwnerCredentials(t *testing.T) {
	reader := &detailReaderStub{source: ownerDetailSource()}
	service := NewService(ServiceOptions{
		Reader: reader,
		CredentialCodec: &detailCodecStub{value: map[string]any{
			"api_key": "sk-owner-secret", "base_url": "https://example.test/v1", "custom": "原始值",
		}},
		Now: func() time.Time { return time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC) },
	})

	editBasic, found, err := service.Get(t.Context(), Input{AccountID: "account-1"}, LevelEditBasic)
	if err != nil || !found {
		t.Fatalf("Get(edit-basic) = found %v, err %v", found, err)
	}
	if _, exists := editBasic["modelMappings"]; exists {
		t.Fatal("edit-basic detail unexpectedly contains modelMappings")
	}
	if got := editBasic["credentials"].(map[string]any)["api_key"]; got != "sk-owner-secret" {
		t.Fatalf("edit-basic api_key = %#v", got)
	}

	advanced, found, err := service.Get(t.Context(), Input{AccountID: "account-1"}, LevelAdvanced)
	if err != nil || !found {
		t.Fatalf("Get(advanced) = found %v, err %v", found, err)
	}
	if got := advanced["credentials"].(map[string]any)["custom"]; got != "原始值" {
		t.Fatalf("advanced custom credential = %#v", got)
	}
	if got := advanced["modelMappings"].([]any); len(got) != 1 {
		t.Fatalf("advanced modelMappings = %#v", got)
	}
}

func TestServiceAuthorizedDetailEnforcesCredentialBoundary(t *testing.T) {
	source := ownerDetailSource()
	source.AccessType = "authorized"
	source.SourceAccountID = "source-1"
	source.CredentialsEncrypted = ""
	source.HasActiveManualSource = true
	reader := &detailReaderStub{source: source}
	service := NewService(ServiceOptions{Reader: reader, CredentialCodec: &detailCodecStub{err: errors.New("must not decrypt")}})

	advanced, found, err := service.Get(t.Context(), Input{AccountID: "account-1"}, LevelAdvanced)
	if err != nil || !found {
		t.Fatalf("Get(advanced authorized) = found %v, err %v", found, err)
	}
	if _, exists := advanced["credentials"]; exists {
		t.Fatal("authorized advanced detail contains source credentials")
	}
	permissions := advanced["permissions"].(ResourcePermissions)
	if permissions.CanEdit || permissions.CanViewCredentials || !permissions.CanReturnAuthorization {
		t.Fatalf("authorized permissions = %#v", permissions)
	}

	if _, _, err := service.Get(t.Context(), Input{AccountID: "account-1"}, LevelEditBasic); !errors.Is(err, ErrCredentialsForbidden) {
		t.Fatalf("Get(edit-basic authorized) error = %v", err)
	}
	if _, _, err := service.APIKeyRuntime(t.Context(), Input{AccountID: "account-1"}); !errors.Is(err, ErrRuntimeForbidden) {
		t.Fatalf("APIKeyRuntime(authorized) error = %v", err)
	}
}

func TestServiceAPIKeyRuntimeMergesCredentialEntriesAndStoredState(t *testing.T) {
	secret := "runtime-secret"
	fingerprint := testFingerprint(secret, "sk-second-secret")
	reader := &detailReaderStub{
		source: ownerDetailSource(),
		states: []port.ManagementAccountAPIKeyRuntimeState{{
			KeyFingerprint: fingerprint, KeyIndex: 1, Status: "rate_limited", FailureCount: 3,
			ConsecutiveFailures: 2, SuccessCount: 7, LastErrorMessage: "upstream raw failure",
		}},
	}
	service := NewService(ServiceOptions{
		Reader: reader,
		CredentialCodec: &detailCodecStub{value: map[string]any{
			"api_keys":        []any{"sk-first-secret", "sk-second-secret"},
			"api_key_weights": []any{float64(2), float64(5)},
		}},
		FingerprintSecret: secret,
	})

	result, found, err := service.APIKeyRuntime(t.Context(), Input{AccountID: "account-1"})
	if err != nil || !found {
		t.Fatalf("APIKeyRuntime() = found %v, err %v", found, err)
	}
	if result.AccountID != "account-1" || result.ConfigRevision != 3 || len(result.Items) != 2 {
		t.Fatalf("APIKeyRuntime() = %#v", result)
	}
	want := APIKeyRuntimeDetail{
		KeyIndex: 1, KeyFingerprintPrefix: fingerprint[:12], KeySuffix: "cret", Weight: 5,
		Status: "rate_limited", FailureCount: 3, ConsecutiveFailures: 2, SuccessCount: 7,
		LastErrorMessage: "upstream raw failure",
	}
	if !reflect.DeepEqual(result.Items[1], want) {
		t.Fatalf("runtime second item = %#v, want %#v", result.Items[1], want)
	}
}

func TestAPIKeyPoolSupportedMatchesNodeProviderContract(t *testing.T) {
	for _, provider := range []string{"openai", "gpt", "deepseek", "glm", "gemini", "anthropic"} {
		if !apiKeyPoolSupported(port.ManagementAccountDetailSource{ProviderCode: provider, Type: "api_key"}, 2) {
			t.Fatalf("provider %q should support API key pool", provider)
		}
	}
	if !apiKeyPoolSupported(port.ManagementAccountDetailSource{ProviderCode: "custom", ProtocolCode: "anthropic", ProtocolVersion: "v1", Type: "api_key"}, 2) {
		t.Fatal("anthropic protocol profile should support API key pool")
	}
	for _, provider := range []string{"xai", "hybrid", "custom"} {
		if apiKeyPoolSupported(port.ManagementAccountDetailSource{ProviderCode: provider, Type: "api_key"}, 2) {
			t.Fatalf("provider %q should not support API key pool", provider)
		}
	}
}

func TestServiceOAuthReauthorizationContextProjectsGeminiMetadata(t *testing.T) {
	tests := []struct {
		name        string
		credentials map[string]any
		wantType    string
		want        OAuthReauthorizationContext
	}{
		{name: "code assist", credentials: map[string]any{"oauth_type": " code_assist ", "project_id": " project ", "client_id": "secret-client", "client_secret": "secret", "access_token": "access"}, wantType: "code_assist", want: OAuthReauthorizationContext{ID: "account-1", ConfigRevision: 1, OAuthType: "code_assist", ProjectID: "project"}},
		{name: "google one", credentials: map[string]any{"oauth_type": "google_one", "quota_project_id": " quota ", "tier_id": " tier ", "base_url": " https://cloudcode-pa.googleapis.com "}, wantType: "google_one", want: OAuthReauthorizationContext{ID: "account-1", ConfigRevision: 1, OAuthType: "google_one", QuotaProjectID: "quota", TierID: "tier", BaseURL: "https://cloudcode-pa.googleapis.com"}},
		{name: "ai studio", credentials: map[string]any{"oauth_type": "ai_studio", "client_id": " client ", "client_secret": " secret ", "base_url": " https://generativelanguage.googleapis.com "}, wantType: "ai_studio", want: OAuthReauthorizationContext{ID: "account-1", ConfigRevision: 1, OAuthType: "ai_studio", ClientID: "client", ClientSecret: "secret", BaseURL: "https://generativelanguage.googleapis.com"}},
		{name: "inferred ai studio", credentials: map[string]any{"client_id": "other-client"}, wantType: "ai_studio", want: OAuthReauthorizationContext{ID: "account-1", ConfigRevision: 1, OAuthType: "ai_studio", ClientID: "other-client"}},
		{name: "inferred code assist", credentials: map[string]any{"client_id": geminiCLIClientID}, wantType: "code_assist", want: OAuthReauthorizationContext{ID: "account-1", ConfigRevision: 1, OAuthType: "code_assist"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := ownerDetailSource()
			source.ProviderCode, source.Type, source.ConfigRevision = "gemini", "google_oauth", 0
			codec := &detailCodecStub{value: tt.credentials}
			service := NewService(ServiceOptions{Reader: &detailReaderStub{source: source}, CredentialCodec: codec})
			got, found, err := service.OAuthReauthorizationContext(t.Context(), Input{AccountID: " account-1 "})
			if err != nil || !found {
				t.Fatalf("OAuthReauthorizationContext() = found %v, err %v", found, err)
			}
			if got != tt.want {
				t.Fatalf("context = %#v, want %#v", got, tt.want)
			}
			if got.OAuthType != tt.wantType {
				t.Fatalf("oauthType = %q, want %q", got.OAuthType, tt.wantType)
			}
			if strings.Contains(fmt.Sprintf("%#v", got), "access") || strings.Contains(fmt.Sprintf("%#v", got), "secret") && tt.wantType != "ai_studio" {
				t.Fatalf("context leaks credentials: %#v", got)
			}
		})
	}
}

func TestServiceOAuthReauthorizationContextRejectsUnsupportedAndAuthorizationInstances(t *testing.T) {
	codec := &detailCodecStub{value: map[string]any{"oauth_type": "ai_studio"}}
	source := ownerDetailSource()
	source.ProviderCode, source.Type = "openai", "api_key"
	reader := &detailReaderStub{source: source}
	service := NewService(ServiceOptions{Reader: reader, CredentialCodec: codec})
	if _, found, err := service.OAuthReauthorizationContext(t.Context(), Input{AccountID: "account-1"}); err != nil || found {
		t.Fatalf("unsupported account = found %v, err %v", found, err)
	}
	if codec.calls != 0 {
		t.Fatalf("codec calls for unsupported account = %d", codec.calls)
	}
	source.ProviderCode, source.Type, source.AccessType, source.SourceAccountID = "gemini", "google_oauth", "authorized", "source-1"
	reader.source = source
	if _, found, err := service.OAuthReauthorizationContext(t.Context(), Input{AccountID: "account-1"}); !errors.Is(err, ErrOAuthReauthorizationForbidden) || found {
		t.Fatalf("authorization instance = found %v, err %v", found, err)
	}
	if codec.calls != 0 {
		t.Fatalf("codec calls for authorization instance = %d", codec.calls)
	}
}

func TestServiceOAuthReauthorizationContextPropagatesCredentialErrors(t *testing.T) {
	source := ownerDetailSource()
	source.ProviderCode, source.Type = "gemini", "google_oauth"
	service := NewService(ServiceOptions{Reader: &detailReaderStub{source: source}, CredentialCodec: &detailCodecStub{err: errors.New("invalid JSON")}})
	if _, found, err := service.OAuthReauthorizationContext(t.Context(), Input{AccountID: "account-1"}); err == nil || found || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("credential error = found %v, err %v", found, err)
	}
	service = NewService(ServiceOptions{Reader: &detailReaderStub{source: source}})
	if _, found, err := service.OAuthReauthorizationContext(t.Context(), Input{AccountID: "account-1"}); err == nil || found || !strings.Contains(err.Error(), "credential codec is required") {
		t.Fatalf("missing codec error = found %v, err %v", found, err)
	}
}

func ownerDetailSource() port.ManagementAccountDetailSource {
	return port.ManagementAccountDetailSource{
		ID: "account-1", SourceAccountID: "account-1", AccessType: "owner", ProviderCode: "gpt",
		ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key", ConfigRevision: 3,
		CredentialsEncrypted: "cipher", DetailJSON: `{
			"id":"account-1","status":"active","schedulable":true,
			"supportedModels":["gpt-5.6-sol"],
			"modelMappings":[{"sourceModel":"gpt-5.6-sol","sourceEndpointFamily":"responses","upstreamModel":"gpt-5.6-sol","upstreamEndpointFamily":"responses","enabled":true}]
		}`,
	}
}

func testFingerprint(secret string, key string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

type detailReaderStub struct {
	source port.ManagementAccountDetailSource
	found  bool
	err    error
	states []port.ManagementAccountAPIKeyRuntimeState
	input  port.ManagementAccountDetailInput
}

func (s *detailReaderStub) GetManagementAccountDetailSource(
	_ context.Context,
	input port.ManagementAccountDetailInput,
) (port.ManagementAccountDetailSource, bool, error) {
	s.input = input
	found := s.found
	if !found && s.err == nil && s.source.ID != "" {
		found = true
	}
	return s.source, found, s.err
}

func (s *detailReaderStub) ListManagementAccountAPIKeyRuntimeStates(
	_ context.Context,
	_ string,
) ([]port.ManagementAccountAPIKeyRuntimeState, error) {
	return append([]port.ManagementAccountAPIKeyRuntimeState(nil), s.states...), s.err
}

type detailCodecStub struct {
	value map[string]any
	err   error
	calls int
}

func (s *detailCodecStub) DecryptJSON(_ string) (map[string]any, error) {
	s.calls++
	return s.value, s.err
}
