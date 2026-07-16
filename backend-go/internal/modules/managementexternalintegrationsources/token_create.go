package managementexternalintegrationsources

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrTokenCreateInvalid             = errors.New("来源系统 Token 参数无效")
	ErrBuiltInTokenCreateRestricted   = errors.New("内置测试 Token 不支持新增 Token")
	errTokenCreateEmptyGeneratedToken = errors.New("generate external integration source token: empty token")
)

type tokenCreateValidationError struct {
	cause error
}

func (e tokenCreateValidationError) Error() string { return e.cause.Error() }
func (e tokenCreateValidationError) Unwrap() error { return e.cause }

func IsTokenCreateValidationError(err error) bool {
	if errors.Is(err, ErrTokenCreateInvalid) {
		return true
	}
	var target tokenCreateValidationError
	return errors.As(err, &target)
}

type TokenCreateInput struct {
	SourceID  string
	Name      string
	Status    string
	Scopes    any
	ExpiresAt any
}

type TokenCreateResult struct {
	Source Detail       `json:"source"`
	Token  CreatedToken `json:"token"`
}

type TokenCreateServiceOptions struct {
	Store    port.ManagementExternalIntegrationSourceTokenCreator
	Secret   string
	Now      func() time.Time
	NewID    func(prefix string) string
	NewToken func() (string, error)
	Codec    createTokenJSONCodec
}

type TokenCreateService struct {
	store    port.ManagementExternalIntegrationSourceTokenCreator
	codec    createTokenJSONCodec
	now      func() time.Time
	newID    func(prefix string) string
	newToken func() (string, error)
}

type normalizedTokenCreateInput struct {
	sourceID   string
	name       string
	status     string
	scopesJSON string
	expiresAt  *time.Time
	now        time.Time
}

func NewTokenCreateService(
	store port.ManagementExternalIntegrationSourceTokenCreator,
	secret string,
) *TokenCreateService {
	return NewTokenCreateServiceWithOptions(TokenCreateServiceOptions{Store: store, Secret: secret})
}

func NewTokenCreateServiceWithOptions(opts TokenCreateServiceOptions) *TokenCreateService {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newID := opts.NewID
	if newID == nil {
		newID = func(prefix string) string {
			return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	newToken := opts.NewToken
	if newToken == nil {
		newToken = GenerateExternalIntegrationSourceToken
	}
	codec := opts.Codec
	if codec == nil {
		codec = secretcrypto.NewJSONCodec(opts.Secret)
	}
	return &TokenCreateService{
		store:    opts.Store,
		codec:    codec,
		now:      now,
		newID:    newID,
		newToken: newToken,
	}
}

func (s *TokenCreateService) Create(ctx context.Context, input TokenCreateInput) (TokenCreateResult, error) {
	if s == nil || s.store == nil {
		return TokenCreateResult{}, errors.New("management external integration source token creator is required")
	}
	normalized, err := normalizeTokenCreateInput(input)
	if err != nil {
		return TokenCreateResult{}, err
	}
	normalized.now = s.now().UTC()

	for attempt := 0; attempt < 3; attempt++ {
		result, err := s.createOnce(ctx, normalized)
		switch {
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceTokenHashExists):
			continue
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceNotFound):
			return TokenCreateResult{}, ErrNotFound
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceBuiltInTokenCreateRestricted):
			return TokenCreateResult{}, ErrBuiltInTokenCreateRestricted
		case err != nil:
			return TokenCreateResult{}, err
		default:
			return result, nil
		}
	}
	return TokenCreateResult{}, ErrTokenExists
}

func (s *TokenCreateService) createOnce(
	ctx context.Context,
	input normalizedTokenCreateInput,
) (TokenCreateResult, error) {
	token, err := s.newToken()
	if err != nil {
		return TokenCreateResult{}, fmt.Errorf("generate external integration source token: %w", err)
	}
	if token == "" {
		return TokenCreateResult{}, errTokenCreateEmptyGeneratedToken
	}
	encrypted, err := s.codec.EncryptJSON(map[string]any{"token": token})
	if err != nil {
		return TokenCreateResult{}, fmt.Errorf("encrypt external integration source token: %w", err)
	}
	tokenPrefix, tokenSuffix := externalIntegrationSourceTokenPreview(token)
	stored, err := s.store.CreateManagementExternalIntegrationSourceToken(
		ctx,
		port.ManagementExternalIntegrationSourceTokenCreateInput{
			TokenID:              s.newID("exttok"),
			SourceID:             input.sourceID,
			Name:                 input.name,
			TokenHash:            publicapiauth.HashExternalSourceToken(token),
			TokenSecretEncrypted: encrypted,
			TokenPrefix:          tokenPrefix,
			TokenSuffix:          tokenSuffix,
			Status:               input.status,
			ScopesJSON:           input.scopesJSON,
			ExpiresAt:            input.expiresAt,
			CreatedAt:            input.now,
			UpdatedAt:            input.now,
		},
	)
	if err != nil {
		return TokenCreateResult{}, err
	}
	source, err := updateDetailFromStore(stored.Source, stored.Tokens)
	if err != nil {
		return TokenCreateResult{}, err
	}
	tokenSummary, err := tokenFromStore(stored.CreatedToken)
	if err != nil {
		return TokenCreateResult{}, err
	}
	return TokenCreateResult{
		Source: source,
		Token: CreatedToken{
			ID:          tokenSummary.ID,
			Name:        tokenSummary.Name,
			Token:       token,
			TokenPrefix: tokenSummary.TokenPrefix,
			TokenSuffix: tokenSummary.TokenSuffix,
			Scopes:      append([]string(nil), tokenSummary.Scopes...),
			ExpiresAt:   tokenSummary.ExpiresAt,
		},
	}, nil
}

func normalizeTokenCreateInput(input TokenCreateInput) (normalizedTokenCreateInput, error) {
	scopes := input.Scopes
	if scopes == nil {
		scopes = []any{}
	}
	normalized, err := normalizeUpdateInput(UpdateInput{
		SourceID:     input.SourceID,
		HasName:      true,
		Name:         input.Name,
		HasScopes:    true,
		Scopes:       scopes,
		HasExpiresAt: true,
		ExpiresAt:    input.ExpiresAt,
	})
	if err != nil {
		return normalizedTokenCreateInput{}, tokenCreateValidationError{cause: err}
	}
	status := input.Status
	if status == "" {
		status = publicapi.TokenStatusActive
	}
	status, err = normalizeTokenStatus(status)
	if err != nil {
		return normalizedTokenCreateInput{}, tokenCreateValidationError{cause: err}
	}
	return normalizedTokenCreateInput{
		sourceID:   normalized.SourceID,
		name:       normalized.Name,
		status:     status,
		scopesJSON: normalized.ScopesJSON,
		expiresAt:  normalized.ExpiresAt,
	}, nil
}
