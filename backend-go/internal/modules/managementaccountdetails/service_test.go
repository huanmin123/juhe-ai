package managementaccountdetails

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
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

	basic, found, err := service.Get(t.Context(), Input{AccountID: "account-1"}, LevelBasic)
	if err != nil || !found {
		t.Fatalf("Get(basic) = found %v, err %v", found, err)
	}
	for _, key := range []string{"credentials", "supportedModels", "modelMappings"} {
		if _, exists := basic[key]; exists {
			t.Fatalf("basic detail unexpectedly contains %s", key)
		}
	}

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
}

func (s *detailReaderStub) GetManagementAccountDetailSource(
	_ context.Context,
	_ port.ManagementAccountDetailInput,
) (port.ManagementAccountDetailSource, bool, error) {
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
}

func (s *detailCodecStub) DecryptJSON(_ string) (map[string]any, error) {
	return s.value, s.err
}
