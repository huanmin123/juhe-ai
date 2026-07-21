package managementexternalintegrationsources

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
)

func TestTokenCreateServiceAppliesDefaultsAndReturnsMappedRows(t *testing.T) {
	now := time.Date(2026, 7, 16, 15, 0, 0, 0, time.UTC)
	plainToken := "juis_abcdefghijklmnopqrstuvwxyzABCDEFGH123456789"
	store := &managementExternalIntegrationSourceTokenCreateStoreStub{}
	service := NewTokenCreateServiceWithOptions(TokenCreateServiceOptions{
		Store:    store,
		Secret:   "external-source-token-create-test-secret",
		Now:      func() time.Time { return now },
		NewID:    func(prefix string) string { return prefix + "_fixed" },
		NewToken: func() (string, error) { return plainToken, nil },
	})

	result, err := service.Create(context.Background(), TokenCreateInput{
		SourceID: "\ufeffsource_1\u3000",
		Name:     "  新 Token  ",
	})
	if err != nil {
		t.Fatalf("create external source token: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d", store.calls)
	}
	input := store.inputs[0]
	if input.TokenID != "exttok_fixed" || input.SourceID != "source_1" || input.Name != "新 Token" ||
		input.Status != publicapi.TokenStatusActive || input.ScopesJSON != "[]" || input.ExpiresAt != nil ||
		input.TokenHash != publicapiauth.HashExternalSourceToken(plainToken) ||
		input.TokenPrefix != plainToken[:8] || input.TokenSuffix != plainToken[len(plainToken)-8:] ||
		input.CreatedAt != now || input.UpdatedAt != now {
		t.Fatalf("token create store input = %#v", input)
	}
	if input.TokenSecretEncrypted == "" || strings.Contains(input.TokenSecretEncrypted, plainToken) {
		t.Fatalf("encrypted token leaked plaintext or is empty: %q", input.TokenSecretEncrypted)
	}
	payload, err := secretcrypto.NewJSONCodec("external-source-token-create-test-secret").DecryptJSON(input.TokenSecretEncrypted)
	if err != nil || len(payload) != 1 || payload["token"] != plainToken {
		t.Fatalf("encrypted token payload = %#v err=%v", payload, err)
	}
	if result.Token.Token != plainToken || result.Token.ID != input.TokenID || result.Token.Name != input.Name ||
		result.Token.TokenPrefix != input.TokenPrefix || result.Token.TokenSuffix != input.TokenSuffix ||
		len(result.Token.Scopes) != 0 || result.Token.ExpiresAt != nil {
		t.Fatalf("created token = %#v", result.Token)
	}
	if result.Source.ID != input.SourceID || result.Source.Name != "映射后的来源" ||
		result.Source.Status != publicapi.SourceStatusDisabled ||
		!reflect.DeepEqual(result.Source.Scopes, []string{publicapi.ScopeGroupListRead}) ||
		result.Source.Notes == nil || *result.Source.Notes != "来源备注" || result.Source.IsBuiltIn ||
		result.Source.TokenCount != 0 || result.Source.ActiveTokenCount != 0 ||
		result.Source.PrimaryToken != nil || len(result.Source.Tokens) != 0 {
		t.Fatalf("mapped source = %#v", result.Source)
	}
}

func TestTokenCreateServiceNormalizesFullInputAndMapsRevokedToken(t *testing.T) {
	now := time.Date(2026, 7, 16, 16, 0, 0, 123456000, time.FixedZone("UTC+8", 8*60*60))
	store := &managementExternalIntegrationSourceTokenCreateStoreStub{}
	service := NewTokenCreateServiceWithOptions(TokenCreateServiceOptions{
		Store:    store,
		Secret:   "external-source-token-create-test-secret",
		Now:      func() time.Time { return now },
		NewID:    func(prefix string) string { return prefix + "_full" },
		NewToken: func() (string, error) { return "juis_abcdefghijklmnopqrstuvwxyzABCDEFGH123456789", nil },
	})

	result, err := service.Create(context.Background(), TokenCreateInput{
		SourceID: "source_1",
		Name:     "\ufeff完整 Token\ufeff",
		Status:   publicapi.TokenStatusRevoked,
		Scopes: []any{
			publicapi.ScopeGroupListRead,
			"\u3000" + publicapi.ScopeAPIKeyListRead + "\ufeff",
			publicapi.ScopeGroupListRead,
		},
		ExpiresAt: "2026-08-01T02:03:04.567Z",
	})
	if err != nil {
		t.Fatalf("create full external source token: %v", err)
	}
	input := store.inputs[0]
	if input.Name != "完整 Token" || input.Status != publicapi.TokenStatusRevoked ||
		input.ScopesJSON != `["juhe_ai_public:api_key_list:read","juhe_ai_public:group_list:read"]` ||
		input.ExpiresAt == nil || input.ExpiresAt.Format(time.RFC3339Nano) != "2026-08-01T02:03:04.567Z" ||
		input.CreatedAt.Location() != time.UTC || input.CreatedAt.Format(time.RFC3339Nano) != "2026-07-16T08:00:00.123456Z" {
		t.Fatalf("normalized full token input = %#v", input)
	}
	if result.Token.ExpiresAt == nil || *result.Token.ExpiresAt != "2026-08-01T02:03:04.567Z" ||
		!reflect.DeepEqual(result.Token.Scopes, []string{publicapi.ScopeAPIKeyListRead, publicapi.ScopeGroupListRead}) {
		t.Fatalf("created full token = %#v", result.Token)
	}
	if result.Source.TokenCount != 0 || result.Source.ActiveTokenCount != 0 ||
		result.Source.PrimaryToken != nil || len(result.Source.Tokens) != 0 {
		t.Fatalf("mapped revoked source detail = %#v", result.Source)
	}
}

func TestTokenCreateServiceReturnsExistingActiveAndNewDisabledTokens(t *testing.T) {
	store := &managementExternalIntegrationSourceTokenCreateStoreStub{}
	service := newDeterministicTokenCreateService(store, []string{
		"juis_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	result, err := service.Create(context.Background(), TokenCreateInput{
		SourceID: "source_1",
		Name:     "停用 Token",
		Status:   publicapi.TokenStatusDisabled,
	})
	if err != nil {
		t.Fatalf("create disabled external source token: %v", err)
	}
	if result.Source.TokenCount != 0 || result.Source.ActiveTokenCount != 0 ||
		result.Source.PrimaryToken != nil || len(result.Source.Tokens) != 0 {
		t.Fatalf("mapped disabled source detail = %#v", result.Source)
	}
	if result.Token.Token == "" || result.Token.Token != "juis_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("created token plaintext = %q", result.Token.Token)
	}
}

func TestTokenCreateServiceRejectsInvalidCreatedTokenReference(t *testing.T) {
	plainToken := "juis_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name      string
		configure func(*managementExternalIntegrationSourceTokenCreateStoreStub)
		want      string
	}{
		{
			name: "mismatched created token id",
			configure: func(store *managementExternalIntegrationSourceTokenCreateStoreStub) {
				store.createdTokenID = "existing_active"
			},
			want: "created token id mismatched",
		},
		{
			name: "missing created token row",
			configure: func(store *managementExternalIntegrationSourceTokenCreateStoreStub) {
				store.omitCreatedToken = true
			},
			want: "created token row is missing",
		},
		{
			name: "duplicate created token rows",
			configure: func(store *managementExternalIntegrationSourceTokenCreateStoreStub) {
				store.duplicateCreatedToken = true
			},
			want: "created token row is duplicated",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementExternalIntegrationSourceTokenCreateStoreStub{}
			test.configure(store)
			service := newDeterministicTokenCreateService(store, []string{plainToken})

			result, err := service.Create(context.Background(), TokenCreateInput{SourceID: "source_1", Name: "Token"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("created token reference error = %v, want %q", err, test.want)
			}
			if result.Token.Token != "" {
				t.Fatalf("invalid created token reference leaked plaintext result: %#v", result)
			}
		})
	}
}

func TestTokenCreateServiceValidation(t *testing.T) {
	tests := []struct {
		name  string
		input TokenCreateInput
		want  string
	}{
		{name: "source id", input: TokenCreateInput{Name: "Token"}, want: "来源系统参数无效"},
		{name: "status", input: TokenCreateInput{SourceID: "source_1", Name: "Token", Status: "paused"}, want: "token 状态无效"},
		{name: "scopes type", input: TokenCreateInput{SourceID: "source_1", Name: "Token", Scopes: map[string]any{}}, want: "scopes 必须是字符串数组"},
		{name: "scope empty", input: TokenCreateInput{SourceID: "source_1", Name: "Token", Scopes: []any{"\u3000"}}, want: "scopes 不能为空"},
		{name: "scope unsupported", input: TokenCreateInput{SourceID: "source_1", Name: "Token", Scopes: []any{"unknown:scope"}}, want: "scope 不受支持"},
		{name: "expires type", input: TokenCreateInput{SourceID: "source_1", Name: "Token", ExpiresAt: 1}, want: "过期时间无效"},
		{name: "expires format", input: TokenCreateInput{SourceID: "source_1", Name: "Token", ExpiresAt: "2026-08-01"}, want: "过期时间无效"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementExternalIntegrationSourceTokenCreateStoreStub{}
			service := NewTokenCreateService(store, "test-secret")
			result, err := service.Create(context.Background(), test.input)
			if err == nil || !IsTokenCreateValidationError(err) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("token create validation error = %v", err)
			}
			if store.calls != 0 || result.Token.Token != "" {
				t.Fatalf("invalid token create reached store or leaked token: calls=%d result=%#v", store.calls, result)
			}
		})
	}
}

func TestTokenCreateServiceUsesNodeTokenNameValidationMessages(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "\uFEFF\u3000", want: "Token 名称不能为空"},
		{name: "over 80 UTF-16 code units", value: strings.Repeat("😀", 40) + "a", want: "Token 名称不能超过 80 个字符"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementExternalIntegrationSourceTokenCreateStoreStub{}
			_, err := NewTokenCreateService(store, "test-secret").Create(context.Background(), TokenCreateInput{
				SourceID: "source_1",
				Name:     test.value,
			})
			if err == nil || !IsTokenCreateValidationError(err) || err.Error() != test.want {
				t.Fatalf("token name validation error = %v, want %q", err, test.want)
			}
			if store.calls != 0 {
				t.Fatalf("invalid token create reached store: %d", store.calls)
			}
		})
	}
}

func TestTokenCreateServiceValidationUsesNodeFirstErrorOrder(t *testing.T) {
	tests := []struct {
		name  string
		input TokenCreateInput
		want  string
	}{
		{
			name: "name before status scopes and expires",
			input: TokenCreateInput{
				SourceID: "source_1", Status: "paused", Scopes: []any{"unknown:scope"}, ExpiresAt: "invalid",
			},
			want: "Token 名称不能为空",
		},
		{
			name: "status before scopes and expires",
			input: TokenCreateInput{
				SourceID: "source_1", Name: "Token", Status: "paused", Scopes: []any{"unknown:scope"}, ExpiresAt: "invalid",
			},
			want: `来源系统 token 状态无效: "paused"`,
		},
		{
			name: "scopes before expires",
			input: TokenCreateInput{
				SourceID: "source_1", Name: "Token", Status: publicapi.TokenStatusRevoked, Scopes: []any{"unknown:scope"}, ExpiresAt: "invalid",
			},
			want: "来源系统 scope 不受支持：unknown:scope",
		},
		{
			name: "expires after valid revoked status and scopes",
			input: TokenCreateInput{
				SourceID: "source_1", Name: "Token", Status: publicapi.TokenStatusRevoked, Scopes: []any{}, ExpiresAt: "invalid",
			},
			want: "过期时间无效",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementExternalIntegrationSourceTokenCreateStoreStub{}
			_, err := NewTokenCreateService(store, "test-secret").Create(context.Background(), test.input)
			if err == nil || !IsTokenCreateValidationError(err) || err.Error() != test.want {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
			if store.calls != 0 {
				t.Fatalf("invalid token create reached store: %d", store.calls)
			}
		})
	}
}

func TestTokenCreateServiceRetriesHashConflictThenSucceeds(t *testing.T) {
	tokens := []string{
		"juis_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"juis_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"juis_ccccccccccccccccccccccccccccccccccccccccccc",
	}
	store := &managementExternalIntegrationSourceTokenCreateStoreStub{errors: []error{
		port.ErrManagementExternalIntegrationSourceTokenHashExists,
		port.ErrManagementExternalIntegrationSourceTokenHashExists,
		nil,
	}}
	service := newDeterministicTokenCreateService(store, tokens)

	result, err := service.Create(context.Background(), TokenCreateInput{SourceID: "source_1", Name: "Token"})
	if err != nil {
		t.Fatalf("retry token hash conflict: %v", err)
	}
	if store.calls != 3 || result.Token.Token != tokens[2] ||
		store.inputs[0].TokenID == store.inputs[1].TokenID || store.inputs[1].TokenID == store.inputs[2].TokenID ||
		store.inputs[0].TokenHash == store.inputs[1].TokenHash || store.inputs[1].TokenHash == store.inputs[2].TokenHash {
		t.Fatalf("retry calls=%d result=%#v inputs=%#v", store.calls, result, store.inputs)
	}
}

func TestTokenCreateServiceMapsStoreErrorsAndStopsImmediately(t *testing.T) {
	storeFailure := errors.New("store unavailable")
	tests := []struct {
		name      string
		storeErr  error
		wantErr   error
		wantCalls int
	}{
		{name: "source missing", storeErr: port.ErrManagementExternalIntegrationSourceNotFound, wantErr: ErrNotFound, wantCalls: 1},
		{name: "built in restricted", storeErr: port.ErrManagementExternalIntegrationSourceBuiltInTokenCreateRestricted, wantErr: ErrBuiltInTokenCreateRestricted, wantCalls: 1},
		{name: "hash exhausted", storeErr: port.ErrManagementExternalIntegrationSourceTokenHashExists, wantErr: ErrTokenExists, wantCalls: 3},
		{name: "other store error", storeErr: storeFailure, wantErr: storeFailure, wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementExternalIntegrationSourceTokenCreateStoreStub{errors: []error{test.storeErr, test.storeErr, test.storeErr}}
			service := newDeterministicTokenCreateService(store, []string{
				"juis_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"juis_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"juis_ccccccccccccccccccccccccccccccccccccccccccc",
			})
			result, err := service.Create(context.Background(), TokenCreateInput{SourceID: "source_1", Name: "Token"})
			if !errors.Is(err, test.wantErr) || store.calls != test.wantCalls {
				t.Fatalf("error = %v calls=%d", err, store.calls)
			}
			if result.Token.Token != "" {
				t.Fatalf("error result leaked token: %#v", result)
			}
		})
	}
}

func TestTokenCreateServiceReturnsGeneratorAndCodecErrorsWithoutCallingStore(t *testing.T) {
	generatorErr := errors.New("entropy unavailable")
	codecErr := errors.New("codec unavailable")
	tests := []struct {
		name     string
		newToken func() (string, error)
		codec    createTokenJSONCodec
		wantErr  error
	}{
		{name: "generator", newToken: func() (string, error) { return "", generatorErr }, wantErr: generatorErr},
		{name: "empty token", newToken: func() (string, error) { return "", nil }, wantErr: errTokenCreateEmptyGeneratedToken},
		{name: "codec", newToken: func() (string, error) { return "juis_secret_token", nil }, codec: tokenCreateCodecStub{err: codecErr}, wantErr: codecErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &managementExternalIntegrationSourceTokenCreateStoreStub{}
			service := NewTokenCreateServiceWithOptions(TokenCreateServiceOptions{
				Store: store, NewToken: test.newToken, Codec: test.codec,
			})
			result, err := service.Create(context.Background(), TokenCreateInput{SourceID: "source_1", Name: "Token"})
			if !errors.Is(err, test.wantErr) || store.calls != 0 || result.Token.Token != "" {
				t.Fatalf("dependency error = %v calls=%d result=%#v", err, store.calls, result)
			}
		})
	}
}

func TestTokenCreateServiceRequiresStore(t *testing.T) {
	_, err := NewTokenCreateServiceWithOptions(TokenCreateServiceOptions{}).Create(
		context.Background(),
		TokenCreateInput{SourceID: "source_1", Name: "Token"},
	)
	if err == nil {
		t.Fatal("token create without store succeeded")
	}
}

func newDeterministicTokenCreateService(
	store *managementExternalIntegrationSourceTokenCreateStoreStub,
	tokens []string,
) *TokenCreateService {
	tokenIndex := 0
	idIndex := 0
	return NewTokenCreateServiceWithOptions(TokenCreateServiceOptions{
		Store:  store,
		Secret: "external-source-token-create-test-secret",
		Now:    func() time.Time { return time.Date(2026, 7, 16, 17, 0, 0, 0, time.UTC) },
		NewID: func(prefix string) string {
			idIndex++
			return prefix + "_" + string(rune('a'+idIndex-1))
		},
		NewToken: func() (string, error) {
			token := tokens[tokenIndex]
			tokenIndex++
			return token, nil
		},
	})
}

type tokenCreateCodecStub struct {
	err error
}

func (s tokenCreateCodecStub) EncryptJSON(map[string]any) (string, error) {
	return "", s.err
}

type managementExternalIntegrationSourceTokenCreateStoreStub struct {
	inputs                []port.ManagementExternalIntegrationSourceTokenCreateInput
	errors                []error
	calls                 int
	createdTokenID        string
	omitCreatedToken      bool
	duplicateCreatedToken bool
}

func (s *managementExternalIntegrationSourceTokenCreateStoreStub) CreateManagementExternalIntegrationSourceToken(
	_ context.Context,
	input port.ManagementExternalIntegrationSourceTokenCreateInput,
) (port.ManagementExternalIntegrationSourceTokenCreateResult, error) {
	s.calls++
	s.inputs = append(s.inputs, input)
	if len(s.errors) >= s.calls && s.errors[s.calls-1] != nil {
		return port.ManagementExternalIntegrationSourceTokenCreateResult{}, s.errors[s.calls-1]
	}
	notes := "来源备注"
	expiresAt := input.CreatedAt.Add(24 * time.Hour)
	createdToken := port.ManagementExternalIntegrationSourcePrimaryTokenRow{
		SourceRefID: input.SourceID,
		ID:          input.TokenID,
		Name:        input.Name,
		TokenPrefix: input.TokenPrefix,
		TokenSuffix: input.TokenSuffix,
		Status:      input.Status,
		ScopesJSON:  input.ScopesJSON,
		ExpiresAt:   input.ExpiresAt,
		CreatedAt:   input.CreatedAt,
		UpdatedAt:   input.UpdatedAt,
	}
	tokens := []port.ManagementExternalIntegrationSourcePrimaryTokenRow{
		{
			SourceRefID: input.SourceID,
			ID:          "existing_active",
			Name:        "已有启用 Token",
			TokenPrefix: "juis_old",
			TokenSuffix: "existing",
			Status:      publicapi.TokenStatusActive,
			ScopesJSON:  `[]`,
			CreatedAt:   input.CreatedAt.Add(-30 * time.Minute),
			UpdatedAt:   input.CreatedAt.Add(-30 * time.Minute),
		},
	}
	if !s.omitCreatedToken {
		tokens = append(tokens, createdToken)
	}
	if s.duplicateCreatedToken {
		tokens = append(tokens, createdToken)
	}
	createdTokenID := input.TokenID
	if s.createdTokenID != "" {
		createdTokenID = s.createdTokenID
	}
	return port.ManagementExternalIntegrationSourceTokenCreateResult{
		Source: port.ManagementExternalIntegrationSourceListRow{
			ID:             input.SourceID,
			Name:           "映射后的来源",
			Status:         publicapi.SourceStatusDisabled,
			ScopesJSON:     `["juhe_ai_public:group_list:read"]`,
			RateLimitsJSON: `[]`,
			ExpiresAt:      &expiresAt,
			Notes:          &notes,
			CreatedAt:      input.CreatedAt.Add(-time.Hour),
			UpdatedAt:      input.UpdatedAt,
		},
		Tokens:         tokens,
		CreatedTokenID: createdTokenID,
	}, nil
}
