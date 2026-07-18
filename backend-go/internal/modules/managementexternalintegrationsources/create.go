package managementexternalintegrationsources

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	ErrCreateInvalid = errors.New("来源系统参数无效")
	ErrTokenExists   = errors.New("来源系统 token 已存在，请重新生成")
)

type createValidationError struct {
	cause error
}

func (e createValidationError) Error() string { return e.cause.Error() }
func (e createValidationError) Unwrap() error { return e.cause }

func IsCreateValidationError(err error) bool {
	if errors.Is(err, ErrCreateInvalid) {
		return true
	}
	var target createValidationError
	return errors.As(err, &target)
}

type CreateInput struct {
	Name       string
	Status     string
	Scopes     any
	RateLimits any
	ExpiresAt  any
	Notes      any
}

type CreatedToken struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Token       string   `json:"token"`
	TokenPrefix string   `json:"tokenPrefix"`
	TokenSuffix string   `json:"tokenSuffix"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   *string  `json:"expiresAt,omitempty"`
}

type CreateResult struct {
	Source Source       `json:"source"`
	Token  CreatedToken `json:"token"`
}

type createTokenJSONCodec interface {
	EncryptJSON(value map[string]any) (string, error)
}

type CreateServiceOptions struct {
	Store    port.ManagementExternalIntegrationSourceCreator
	Secret   string
	Now      func() time.Time
	NewID    func(prefix string) string
	NewToken func() (string, error)
	Codec    createTokenJSONCodec
}

type CreateService struct {
	store    port.ManagementExternalIntegrationSourceCreator
	codec    createTokenJSONCodec
	now      func() time.Time
	newID    func(prefix string) string
	newToken func() (string, error)
}

type normalizedCreateInput struct {
	name           string
	status         string
	scopesJSON     string
	rateLimitsJSON string
	expiresAt      *time.Time
	notes          *string
	now            time.Time
}

func NewCreateService(
	store port.ManagementExternalIntegrationSourceCreator,
	secret string,
) *CreateService {
	return NewCreateServiceWithOptions(CreateServiceOptions{Store: store, Secret: secret})
}

func NewCreateServiceWithOptions(opts CreateServiceOptions) *CreateService {
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
	return &CreateService{
		store:    opts.Store,
		codec:    codec,
		now:      now,
		newID:    newID,
		newToken: newToken,
	}
}

func (s *CreateService) Create(ctx context.Context, input CreateInput) (CreateResult, error) {
	if s == nil || s.store == nil {
		return CreateResult{}, fmt.Errorf("management external integration source creator is required")
	}
	normalized, err := normalizeCreateInput(input)
	if err != nil {
		return CreateResult{}, err
	}
	normalized.now = s.now().UTC()

	for attempt := 0; attempt < 3; attempt++ {
		result, err := s.createOnce(ctx, normalized)
		switch {
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceTokenHashExists):
			continue
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceNameExists):
			return CreateResult{}, ErrNameExists
		case err != nil:
			return CreateResult{}, err
		default:
			return result, nil
		}
	}
	return CreateResult{}, ErrTokenExists
}

func (s *CreateService) createOnce(
	ctx context.Context,
	input normalizedCreateInput,
) (CreateResult, error) {
	sourceID := s.newID("extsrc")
	tokenID := s.newID("exttok")
	token, err := s.newToken()
	if err != nil {
		return CreateResult{}, fmt.Errorf("generate external integration source token: %w", err)
	}
	if token == "" {
		return CreateResult{}, errors.New("generate external integration source token: empty token")
	}
	encrypted, err := s.codec.EncryptJSON(map[string]any{"token": token})
	if err != nil {
		return CreateResult{}, fmt.Errorf("encrypt external integration source token: %w", err)
	}
	tokenPrefix, tokenSuffix := externalIntegrationSourceTokenPreview(token)
	stored, err := s.store.CreateManagementExternalIntegrationSource(
		ctx,
		port.ManagementExternalIntegrationSourceCreateInput{
			SourceID:             sourceID,
			Name:                 input.name,
			Status:               input.status,
			ScopesJSON:           input.scopesJSON,
			RateLimitsJSON:       input.rateLimitsJSON,
			ExpiresAt:            input.expiresAt,
			Notes:                input.notes,
			TokenID:              tokenID,
			TokenName:            input.name + " 生产 Token",
			TokenHash:            publicapiauth.HashExternalSourceToken(token),
			TokenSecretEncrypted: encrypted,
			TokenPrefix:          tokenPrefix,
			TokenSuffix:          tokenSuffix,
			TokenStatus:          input.status,
			TokenScopesJSON:      input.scopesJSON,
			TokenExpiresAt:       input.expiresAt,
			CreatedAt:            input.now,
			UpdatedAt:            input.now,
		},
	)
	if err != nil {
		return CreateResult{}, err
	}
	source, err := sourceFromStore(stored.Source)
	if err != nil {
		return CreateResult{}, err
	}
	tokenSummary, err := tokenFromStore(stored.Token)
	if err != nil {
		return CreateResult{}, err
	}
	source.TokenCount = 1
	if tokenSummary.Status == publicapi.TokenStatusActive {
		source.ActiveTokenCount = 1
	}
	source.PrimaryToken = &tokenSummary
	return CreateResult{
		Source: source,
		Token: CreatedToken{
			ID:          tokenSummary.ID,
			Name:        tokenSummary.Name,
			Token:       token,
			TokenPrefix: tokenSummary.TokenPrefix,
			TokenSuffix: tokenSummary.TokenSuffix,
			Scopes:      append([]string{}, tokenSummary.Scopes...),
			ExpiresAt:   tokenSummary.ExpiresAt,
		},
	}, nil
}

func normalizeCreateInput(input CreateInput) (normalizedCreateInput, error) {
	status := input.Status
	if status == "" {
		status = publicapi.SourceStatusActive
	}
	scopes := input.Scopes
	if scopes == nil {
		scopes = []any{}
	}
	rateLimits := input.RateLimits
	if rateLimits == nil {
		rateLimits = []any{}
	}
	normalized, err := normalizeUpdateInput(UpdateInput{
		SourceID:      "create",
		HasName:       true,
		Name:          input.Name,
		HasStatus:     true,
		Status:        status,
		HasScopes:     true,
		Scopes:        scopes,
		HasRateLimits: true,
		RateLimits:    rateLimits,
		HasExpiresAt:  true,
		ExpiresAt:     input.ExpiresAt,
		HasNotes:      true,
		Notes:         input.Notes,
	})
	if err != nil {
		return normalizedCreateInput{}, createValidationError{cause: err}
	}
	return normalizedCreateInput{
		name:           normalized.Name,
		status:         normalized.Status,
		scopesJSON:     normalized.ScopesJSON,
		rateLimitsJSON: normalized.RateLimitsJSON,
		expiresAt:      normalized.ExpiresAt,
		notes:          normalized.Notes,
	}, nil
}

func GenerateExternalIntegrationSourceToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "juis_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func externalIntegrationSourceTokenPreview(token string) (string, string) {
	const previewLength = 8
	if len(token) <= previewLength {
		return token, token
	}
	return token[:previewLength], token[len(token)-previewLength:]
}
