package managementexternalintegrationsources

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
)

func TestCreateServiceAppliesDefaultsAndReturnsOneTimeToken(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 30, 0, 0, time.UTC)
	plainToken := "juis_abcdefghijklmnopqrstuvwxyzABCDEFGH123456789"
	store := &managementExternalIntegrationSourceCreateStoreStub{}
	service := NewCreateServiceWithOptions(CreateServiceOptions{
		Store:    store,
		Secret:   "external-source-create-test-secret",
		Now:      func() time.Time { return now },
		NewID:    func(prefix string) string { return prefix + "_fixed" },
		NewToken: func() (string, error) { return plainToken, nil },
	})

	result, err := service.Create(context.Background(), CreateInput{Name: "  新来源  "})
	if err != nil {
		t.Fatalf("create external source: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d", store.calls)
	}
	input := store.inputs[0]
	if input.SourceID != "extsrc_fixed" || input.TokenID != "exttok_fixed" ||
		input.Name != "新来源" || input.Status != publicapi.SourceStatusActive ||
		input.ScopesJSON != "[]" || input.RateLimitsJSON != "[]" ||
		input.ExpiresAt != nil || input.Notes != nil ||
		input.TokenName != "新来源 生产 Token" || input.TokenStatus != publicapi.TokenStatusActive ||
		input.TokenScopesJSON != "[]" || input.TokenExpiresAt != nil ||
		input.TokenHash != publicapiauth.HashExternalSourceToken(plainToken) ||
		input.TokenPrefix != plainToken[:8] || input.TokenSuffix != plainToken[len(plainToken)-8:] ||
		input.CreatedAt != now || input.UpdatedAt != now {
		t.Fatalf("create store input = %#v", input)
	}
	if strings.Contains(input.TokenSecretEncrypted, plainToken) || input.TokenSecretEncrypted == "" {
		t.Fatalf("encrypted token leaked plaintext or is empty: %q", input.TokenSecretEncrypted)
	}
	payload, err := secretcrypto.NewJSONCodec("external-source-create-test-secret").DecryptJSON(input.TokenSecretEncrypted)
	if err != nil || payload["token"] != plainToken || len(payload) != 1 {
		t.Fatalf("encrypted token payload = %#v err=%v", payload, err)
	}
	if result.Token.Token != plainToken || result.Token.ID != input.TokenID ||
		result.Token.Name != input.TokenName || result.Token.TokenPrefix != input.TokenPrefix ||
		result.Token.TokenSuffix != input.TokenSuffix || result.Token.Scopes == nil || len(result.Token.Scopes) != 0 || result.Token.ExpiresAt != nil {
		t.Fatalf("created token result = %#v", result.Token)
	}
	if result.Source.ID != input.SourceID || result.Source.Name != "新来源" ||
		result.Source.Status != publicapi.SourceStatusActive || result.Source.TokenCount != 1 ||
		result.Source.ActiveTokenCount != 1 || result.Source.PrimaryToken == nil ||
		result.Source.PrimaryToken.ID != input.TokenID || result.Source.IsBuiltIn {
		t.Fatalf("created source result = %#v", result.Source)
	}
}

func TestCreateServiceNormalizesFullInput(t *testing.T) {
	now := time.Date(2026, 7, 16, 13, 0, 0, 0, time.UTC)
	store := &managementExternalIntegrationSourceCreateStoreStub{}
	service := NewCreateServiceWithOptions(CreateServiceOptions{
		Store:    store,
		Secret:   "external-source-create-test-secret",
		Now:      func() time.Time { return now },
		NewID:    func(prefix string) string { return prefix + "_full" },
		NewToken: func() (string, error) { return "juis_abcdefghijklmnopqrstuvwxyzABCDEFGH123456789", nil },
	})

	result, err := service.Create(context.Background(), CreateInput{
		Name:   "\ufeff完整来源\ufeff",
		Status: publicapi.SourceStatusDisabled,
		Scopes: []any{publicapi.ScopeGroupListRead, publicapi.ScopeAPIKeyListRead, publicapi.ScopeGroupListRead},
		RateLimits: []any{
			map[string]any{"windowSeconds": 60, "maxRequests": 10},
			map[string]any{"windowSeconds": 1, "maxRequests": 2},
		},
		ExpiresAt: "2026-08-01T00:00:00.000Z",
		Notes:     "  备注  ",
	})
	if err != nil {
		t.Fatalf("create full external source: %v", err)
	}
	input := store.inputs[0]
	if input.Name != "完整来源" || input.Status != publicapi.SourceStatusDisabled ||
		input.ScopesJSON != `["juhe_ai_public:api_key_list:read","juhe_ai_public:group_list:read"]` ||
		input.RateLimitsJSON != `[{"windowSeconds":1,"maxRequests":2},{"windowSeconds":60,"maxRequests":10}]` ||
		input.ExpiresAt == nil || input.ExpiresAt.Format(time.RFC3339Nano) != "2026-08-01T00:00:00Z" ||
		input.Notes == nil || *input.Notes != "备注" || input.TokenStatus != publicapi.TokenStatusDisabled {
		t.Fatalf("normalized full create input = %#v", input)
	}
	if result.Source.ActiveTokenCount != 0 || result.Source.PrimaryToken == nil ||
		result.Source.PrimaryToken.Status != publicapi.TokenStatusDisabled || result.Token.ExpiresAt == nil {
		t.Fatalf("full create result = %#v", result)
	}
}

func TestCreateServiceValidation(t *testing.T) {
	tests := []struct {
		name  string
		input CreateInput
		want  string
	}{
		{name: "name", input: CreateInput{}, want: "来源系统名称不能为空"},
		{name: "status", input: CreateInput{Name: "来源", Status: "paused"}, want: "来源系统状态无效"},
		{name: "scopes", input: CreateInput{Name: "来源", Scopes: []any{"unknown"}}, want: "来源系统 scope 不受支持"},
		{name: "rate limits", input: CreateInput{Name: "来源", RateLimits: []any{map[string]any{"windowSeconds": 0, "maxRequests": 1}}}, want: "来源系统限频窗口必须在"},
		{name: "expires", input: CreateInput{Name: "来源", ExpiresAt: "2026-01-01"}, want: "过期时间无效"},
		{name: "notes", input: CreateInput{Name: "来源", Notes: strings.Repeat("a", 501)}, want: "备注不能超过 500 个字符"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementExternalIntegrationSourceCreateStoreStub{}
			service := NewCreateServiceWithOptions(CreateServiceOptions{Store: store})
			_, err := service.Create(context.Background(), test.input)
			if err == nil || !IsCreateValidationError(err) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("create validation error = %v", err)
			}
			if store.calls != 0 {
				t.Fatalf("invalid create reached store: %d", store.calls)
			}
		})
	}
}

func TestCreateServiceMapsNameConflict(t *testing.T) {
	store := &managementExternalIntegrationSourceCreateStoreStub{
		errors: []error{port.ErrManagementExternalIntegrationSourceNameExists},
	}
	service := newDeterministicCreateService(store, []string{"juis_abcdefghijklmnopqrstuvwxyzABCDEFGH123456789"})
	_, err := service.Create(context.Background(), CreateInput{Name: "来源"})
	if !errors.Is(err, ErrNameExists) || store.calls != 1 {
		t.Fatalf("name conflict error = %v calls=%d", err, store.calls)
	}
}

func TestCreateServiceRetriesTokenHashConflict(t *testing.T) {
	store := &managementExternalIntegrationSourceCreateStoreStub{
		errors: []error{
			port.ErrManagementExternalIntegrationSourceTokenHashExists,
			port.ErrManagementExternalIntegrationSourceTokenHashExists,
			nil,
		},
	}
	tokens := []string{
		"juis_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"juis_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"juis_ccccccccccccccccccccccccccccccccccccccccccc",
	}
	service := newDeterministicCreateService(store, tokens)
	result, err := service.Create(context.Background(), CreateInput{Name: "来源"})
	if err != nil {
		t.Fatalf("retry token hash conflict: %v", err)
	}
	if store.calls != 3 || result.Token.Token != tokens[2] ||
		store.inputs[0].TokenHash == store.inputs[1].TokenHash ||
		store.inputs[1].TokenHash == store.inputs[2].TokenHash {
		t.Fatalf("retry calls=%d result=%#v inputs=%#v", store.calls, result, store.inputs)
	}
}

func TestCreateServiceTokenHashConflictExhausted(t *testing.T) {
	store := &managementExternalIntegrationSourceCreateStoreStub{
		errors: []error{
			port.ErrManagementExternalIntegrationSourceTokenHashExists,
			port.ErrManagementExternalIntegrationSourceTokenHashExists,
			port.ErrManagementExternalIntegrationSourceTokenHashExists,
		},
	}
	service := newDeterministicCreateService(store, []string{
		"juis_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"juis_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"juis_ccccccccccccccccccccccccccccccccccccccccccc",
	})
	_, err := service.Create(context.Background(), CreateInput{Name: "来源"})
	if !errors.Is(err, ErrTokenExists) || store.calls != 3 {
		t.Fatalf("token conflict exhausted error = %v calls=%d", err, store.calls)
	}
}

func TestCreateServiceDependencyAndTokenGenerationErrors(t *testing.T) {
	service := NewCreateServiceWithOptions(CreateServiceOptions{})
	if _, err := service.Create(context.Background(), CreateInput{Name: "来源"}); err == nil {
		t.Fatal("create without store succeeded")
	}
	service = NewCreateServiceWithOptions(CreateServiceOptions{
		Store:    &managementExternalIntegrationSourceCreateStoreStub{},
		NewToken: func() (string, error) { return "", errors.New("entropy unavailable") },
	})
	if _, err := service.Create(context.Background(), CreateInput{Name: "来源"}); err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("token generation error = %v", err)
	}
}

func TestGenerateExternalIntegrationSourceTokenFormat(t *testing.T) {
	token, err := GenerateExternalIntegrationSourceToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if !strings.HasPrefix(token, "juis_") || len(token) != 48 || strings.ContainsAny(token, "+/=") {
		t.Fatalf("generated token format = %q", token)
	}
}

func newDeterministicCreateService(
	store *managementExternalIntegrationSourceCreateStoreStub,
	tokens []string,
) *CreateService {
	index := 0
	return NewCreateServiceWithOptions(CreateServiceOptions{
		Store:  store,
		Secret: "external-source-create-test-secret",
		Now:    func() time.Time { return time.Date(2026, 7, 16, 14, 0, 0, 0, time.UTC) },
		NewID:  func(prefix string) string { return prefix + "_" + string(rune('a'+index)) },
		NewToken: func() (string, error) {
			token := tokens[index]
			index++
			return token, nil
		},
	})
}

type managementExternalIntegrationSourceCreateStoreStub struct {
	inputs []port.ManagementExternalIntegrationSourceCreateInput
	errors []error
	calls  int
}

func (s *managementExternalIntegrationSourceCreateStoreStub) CreateManagementExternalIntegrationSource(
	_ context.Context,
	input port.ManagementExternalIntegrationSourceCreateInput,
) (port.ManagementExternalIntegrationSourceCreateResult, error) {
	s.calls++
	s.inputs = append(s.inputs, input)
	if len(s.errors) >= s.calls && s.errors[s.calls-1] != nil {
		return port.ManagementExternalIntegrationSourceCreateResult{}, s.errors[s.calls-1]
	}
	return port.ManagementExternalIntegrationSourceCreateResult{
		Source: port.ManagementExternalIntegrationSourceListRow{
			ID:             input.SourceID,
			Name:           input.Name,
			Status:         input.Status,
			ScopesJSON:     input.ScopesJSON,
			RateLimitsJSON: input.RateLimitsJSON,
			ExpiresAt:      input.ExpiresAt,
			Notes:          input.Notes,
			CreatedAt:      input.CreatedAt,
			UpdatedAt:      input.UpdatedAt,
		},
		Token: port.ManagementExternalIntegrationSourcePrimaryTokenRow{
			SourceRefID: input.SourceID,
			ID:          input.TokenID,
			Name:        input.TokenName,
			TokenPrefix: input.TokenPrefix,
			TokenSuffix: input.TokenSuffix,
			Status:      input.TokenStatus,
			ScopesJSON:  input.TokenScopesJSON,
			ExpiresAt:   input.TokenExpiresAt,
			CreatedAt:   input.CreatedAt,
			UpdatedAt:   input.UpdatedAt,
		},
	}, nil
}
