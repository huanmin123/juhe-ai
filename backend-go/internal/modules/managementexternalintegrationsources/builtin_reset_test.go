package managementexternalintegrationsources

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type builtInResetStoreStub struct {
	results []port.ManagementExternalIntegrationSourceBuiltInResetResult
	errors  []error
	inputs  []port.ManagementExternalIntegrationSourceBuiltInResetInput
}

func (s *builtInResetStoreStub) ResetManagementExternalIntegrationSourceBuiltInToken(_ context.Context, input port.ManagementExternalIntegrationSourceBuiltInResetInput) (port.ManagementExternalIntegrationSourceBuiltInResetResult, error) {
	s.inputs = append(s.inputs, input)
	index := len(s.inputs) - 1
	if index < len(s.errors) && s.errors[index] != nil {
		return port.ManagementExternalIntegrationSourceBuiltInResetResult{}, s.errors[index]
	}
	if index < len(s.results) {
		return s.results[index], nil
	}
	return builtInResetStoreResult(input.UpdatedAt), nil
}

type builtInResetCodecStub struct{ encrypted string }

func (s builtInResetCodecStub) EncryptJSON(map[string]any) (string, error) { return s.encrypted, nil }

func TestBuiltInResetServiceRotatesTokenAndReturnsPlaintextOnce(t *testing.T) {
	now := time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)
	store := &builtInResetStoreStub{}
	service := NewBuiltInResetServiceWithOptions(BuiltInResetServiceOptions{
		Store: store, Now: func() time.Time { return now },
		NewToken: func() (string, error) { return "juis_abcdefghijklmnopqrstuvwxyz1234567890", nil },
		Codec:    builtInResetCodecStub{encrypted: "encrypted-json"},
	})
	result, err := service.Reset(t.Context())
	if err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if len(store.inputs) != 1 || store.inputs[0].TokenSecretEncrypted != "encrypted-json" || !store.inputs[0].UpdatedAt.Equal(now) {
		t.Fatalf("store inputs = %+v", store.inputs)
	}
	if result.Token.Token != "juis_abcdefghijklmnopqrstuvwxyz1234567890" || result.Token.ID != "exttok_builtin_test" || result.Token.ExpiresAt != nil {
		t.Fatalf("result token = %+v", result.Token)
	}
	if result.Source.Source.ID != "extsrc_builtin_test" || len(result.Token.Scopes) != 1 || result.Token.Scopes[0] != "juhe_ai_public:group_list:read" {
		t.Fatalf("result = %+v", result)
	}
}

func TestBuiltInResetServiceRetriesHashCollisionAndMapsNotFound(t *testing.T) {
	store := &builtInResetStoreStub{errors: []error{port.ErrManagementExternalIntegrationSourceTokenHashExists, nil}}
	tokens := []string{"juis_first_12345678901234567890123456789012", "juis_second_1234567890123456789012345678901"}
	service := NewBuiltInResetServiceWithOptions(BuiltInResetServiceOptions{
		Store: store, NewToken: func() (string, error) { token := tokens[0]; tokens = tokens[1:]; return token, nil },
		Codec: builtInResetCodecStub{encrypted: "encrypted"},
	})
	if _, err := service.Reset(t.Context()); err != nil || len(store.inputs) != 2 {
		t.Fatalf("Reset() error=%v calls=%d", err, len(store.inputs))
	}

	store = &builtInResetStoreStub{errors: []error{port.ErrManagementExternalIntegrationSourceBuiltInResetNotFound}}
	service = NewBuiltInResetServiceWithOptions(BuiltInResetServiceOptions{Store: store, NewToken: func() (string, error) { return "juis_missing_123456789012345678901234567890", nil }, Codec: builtInResetCodecStub{encrypted: "encrypted"}})
	if _, err := service.Reset(t.Context()); !errors.Is(err, ErrBuiltInResetNotFound) {
		t.Fatalf("Reset() error = %v", err)
	}
}

func TestBuiltInResetServiceStopsAfterThreeHashCollisions(t *testing.T) {
	store := &builtInResetStoreStub{errors: []error{
		port.ErrManagementExternalIntegrationSourceTokenHashExists,
		port.ErrManagementExternalIntegrationSourceTokenHashExists,
		port.ErrManagementExternalIntegrationSourceTokenHashExists,
	}}
	generated := 0
	service := NewBuiltInResetServiceWithOptions(BuiltInResetServiceOptions{
		Store: store,
		NewToken: func() (string, error) {
			generated++
			return "juis_collision_123456789012345678901234567890", nil
		},
		Codec: builtInResetCodecStub{encrypted: "encrypted"},
	})

	if _, err := service.Reset(t.Context()); !errors.Is(err, ErrTokenExists) {
		t.Fatalf("Reset() error = %v, want ErrTokenExists", err)
	}
	if generated != 3 || len(store.inputs) != 3 {
		t.Fatalf("generated=%d storeCalls=%d, want 3/3", generated, len(store.inputs))
	}
}

func builtInResetStoreResult(now time.Time) port.ManagementExternalIntegrationSourceBuiltInResetResult {
	return port.ManagementExternalIntegrationSourceBuiltInResetResult{
		Source: port.ManagementExternalIntegrationSourceListRow{ID: "extsrc_builtin_test", Name: "内置测试来源", Status: "active", ScopesJSON: `["juhe_ai_public:group_list:read"]`, RateLimitsJSON: `[]`, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
		Token:  port.ManagementExternalIntegrationSourcePrimaryTokenRow{SourceRefID: "extsrc_builtin_test", ID: "exttok_builtin_test", Name: "内置测试 Token", TokenPrefix: "juis_abc", TokenSuffix: "12345678", Status: "active", ScopesJSON: `["juhe_ai_public:group_list:read"]`, CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
	}
}
