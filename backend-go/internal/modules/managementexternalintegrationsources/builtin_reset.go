package managementexternalintegrationsources

import (
	"context"
	"errors"
	"fmt"
	"time"

	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	"juhe-ai/backend-go/internal/secretcrypto"
	"juhe-ai/backend-go/internal/store/port"
)

var ErrBuiltInResetNotFound = errors.New("内置测试 Token 不存在")

type BuiltInResetServiceOptions struct {
	Store    port.ManagementExternalIntegrationSourceBuiltInResetter
	Secret   string
	Now      func() time.Time
	NewToken func() (string, error)
	Codec    createTokenJSONCodec
}

type BuiltInResetService struct {
	store    port.ManagementExternalIntegrationSourceBuiltInResetter
	codec    createTokenJSONCodec
	now      func() time.Time
	newToken func() (string, error)
}

func NewBuiltInResetService(store port.ManagementExternalIntegrationSourceBuiltInResetter, secret string) *BuiltInResetService {
	return NewBuiltInResetServiceWithOptions(BuiltInResetServiceOptions{Store: store, Secret: secret})
}

func NewBuiltInResetServiceWithOptions(opts BuiltInResetServiceOptions) *BuiltInResetService {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	newToken := opts.NewToken
	if newToken == nil {
		newToken = GenerateExternalIntegrationSourceToken
	}
	codec := opts.Codec
	if codec == nil {
		codec = secretcrypto.NewJSONCodec(opts.Secret)
	}
	return &BuiltInResetService{store: opts.Store, codec: codec, now: now, newToken: newToken}
}

func (s *BuiltInResetService) Reset(ctx context.Context) (TokenCreateResult, error) {
	if s == nil || s.store == nil {
		return TokenCreateResult{}, errors.New("management external integration source built-in resetter is required")
	}
	for attempt := 0; attempt < 3; attempt++ {
		result, err := s.resetOnce(ctx)
		if errors.Is(err, port.ErrManagementExternalIntegrationSourceTokenHashExists) {
			continue
		}
		if errors.Is(err, port.ErrManagementExternalIntegrationSourceBuiltInResetNotFound) {
			return TokenCreateResult{}, ErrBuiltInResetNotFound
		}
		return result, err
	}
	return TokenCreateResult{}, ErrTokenExists
}

func (s *BuiltInResetService) resetOnce(ctx context.Context) (TokenCreateResult, error) {
	token, err := s.newToken()
	if err != nil {
		return TokenCreateResult{}, fmt.Errorf("generate built-in external integration source token: %w", err)
	}
	if token == "" {
		return TokenCreateResult{}, errTokenCreateEmptyGeneratedToken
	}
	encrypted, err := s.codec.EncryptJSON(map[string]any{"token": token})
	if err != nil {
		return TokenCreateResult{}, fmt.Errorf("encrypt built-in external integration source token: %w", err)
	}
	tokenPrefix, tokenSuffix := externalIntegrationSourceTokenPreview(token)
	stored, err := s.store.ResetManagementExternalIntegrationSourceBuiltInToken(ctx, port.ManagementExternalIntegrationSourceBuiltInResetInput{
		TokenHash:            publicapiauth.HashExternalSourceToken(token),
		TokenSecretEncrypted: encrypted,
		TokenPrefix:          tokenPrefix,
		TokenSuffix:          tokenSuffix,
		UpdatedAt:            s.now().UTC(),
	})
	if err != nil {
		return TokenCreateResult{}, err
	}
	source, err := updateDetailFromStore(stored.Source, []port.ManagementExternalIntegrationSourcePrimaryTokenRow{stored.Token})
	if err != nil {
		return TokenCreateResult{}, fmt.Errorf("map built-in external integration source reset detail: %w", err)
	}
	tokenSummary, err := tokenFromStore(stored.Token)
	if err != nil {
		return TokenCreateResult{}, fmt.Errorf("map built-in external integration source reset token: %w", err)
	}
	return TokenCreateResult{Source: source, Token: CreatedToken{
		ID: tokenSummary.ID, Name: tokenSummary.Name, Token: token,
		TokenPrefix: tokenSummary.TokenPrefix, TokenSuffix: tokenSummary.TokenSuffix,
		Scopes: append([]string(nil), source.Source.Scopes...),
	}}, nil
}
