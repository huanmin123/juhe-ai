package managementexternalintegrationsources

import (
	"context"
	"errors"
	"fmt"
	"time"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrTokenNotFound                       = errors.New("Token 不存在")
	ErrBuiltInTokenUpdateRestricted        = errors.New("内置测试 Token 不支持编辑")
	errTokenUpdateSnapshotIdentityMismatch = errors.New(
		"management external integration source token update snapshot identity mismatched",
	)
)

type tokenUpdateValidationError struct {
	cause error
}

func (e tokenUpdateValidationError) Error() string { return e.cause.Error() }
func (e tokenUpdateValidationError) Unwrap() error { return e.cause }

func IsTokenUpdateValidationError(err error) bool {
	var target tokenUpdateValidationError
	return errors.As(err, &target)
}

type TokenUpdateInput struct {
	SourceID     string
	TokenID      string
	HasName      bool
	Name         string
	HasStatus    bool
	Status       string
	HasScopes    bool
	Scopes       any
	HasExpiresAt bool
	ExpiresAt    any
}

type TokenUpdateResult struct {
	Before    Token
	After     Token
	Committed bool
}

type TokenUpdateServiceOptions struct {
	Store port.ManagementExternalIntegrationSourceTokenUpdater
	Now   func() time.Time
}

type TokenUpdateService struct {
	store port.ManagementExternalIntegrationSourceTokenUpdater
	now   func() time.Time
}

func NewTokenUpdateService(store port.ManagementExternalIntegrationSourceTokenUpdater) *TokenUpdateService {
	return NewTokenUpdateServiceWithOptions(TokenUpdateServiceOptions{Store: store})
}

func NewTokenUpdateServiceWithOptions(opts TokenUpdateServiceOptions) *TokenUpdateService {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &TokenUpdateService{store: opts.Store, now: now}
}

func (s *TokenUpdateService) Update(ctx context.Context, input TokenUpdateInput) (TokenUpdateResult, error) {
	if s == nil || s.store == nil {
		return TokenUpdateResult{}, fmt.Errorf("management external integration source token updater is required")
	}
	normalized, err := normalizeTokenUpdateInput(input)
	if err != nil {
		return TokenUpdateResult{}, err
	}
	normalized.UpdatedAt = s.now().UTC()

	var result TokenUpdateResult
	_, err = s.store.UpdateManagementExternalIntegrationSourceToken(ctx, normalized, func(stored port.ManagementExternalIntegrationSourceTokenUpdateResult) error {
		if err := validateTokenUpdateSnapshotIdentity(stored, normalized.SourceID, normalized.TokenID); err != nil {
			return err
		}
		var mapErr error
		result.Before, mapErr = tokenFromStore(stored.BeforeToken)
		if mapErr != nil {
			return mapErr
		}
		result.After, mapErr = tokenFromStore(stored.AfterToken)
		return mapErr
	})
	if err != nil {
		switch {
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceNotFound),
			errors.Is(err, port.ErrManagementExternalIntegrationSourceTokenNotFound):
			return TokenUpdateResult{}, ErrTokenNotFound
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceBuiltInTokenUpdateRestricted):
			return TokenUpdateResult{}, ErrBuiltInTokenUpdateRestricted
		default:
			return TokenUpdateResult{}, err
		}
	}

	result.Committed = true
	return result, nil
}

func validateTokenUpdateSnapshotIdentity(
	stored port.ManagementExternalIntegrationSourceTokenUpdateResult,
	sourceID string,
	tokenID string,
) error {
	if stored.BeforeToken.SourceRefID != sourceID ||
		stored.AfterToken.SourceRefID != sourceID ||
		stored.BeforeToken.ID != tokenID ||
		stored.AfterToken.ID != tokenID ||
		stored.BeforeToken.SourceRefID != stored.AfterToken.SourceRefID ||
		stored.BeforeToken.ID != stored.AfterToken.ID {
		return errTokenUpdateSnapshotIdentityMismatch
	}
	return nil
}

func normalizeTokenUpdateInput(input TokenUpdateInput) (port.ManagementExternalIntegrationSourceTokenUpdateInput, error) {
	output := port.ManagementExternalIntegrationSourceTokenUpdateInput{
		SourceID:     trimECMAScriptWhitespace(input.SourceID),
		TokenID:      trimECMAScriptWhitespace(input.TokenID),
		HasName:      input.HasName,
		HasStatus:    input.HasStatus,
		HasScopes:    input.HasScopes,
		HasExpiresAt: input.HasExpiresAt,
	}
	if output.SourceID == "" {
		return output, tokenUpdateValidationError{cause: ErrNotFound}
	}
	if output.TokenID == "" {
		return output, tokenUpdateValidationError{cause: ErrTokenNotFound}
	}
	if output.SourceID == publicapi.BuiltInTestSourceID || output.TokenID == publicapi.BuiltInTestTokenID {
		return output, ErrBuiltInTokenUpdateRestricted
	}
	if input.HasName {
		output.Name = trimECMAScriptWhitespace(input.Name)
		if output.Name == "" {
			return output, tokenUpdateValidationError{cause: errors.New("Token 名称不能为空")}
		}
		if utf16LengthOf(output.Name) > 80 {
			return output, tokenUpdateValidationError{cause: errors.New("Token 名称不能超过 80 个字符")}
		}
	}
	var err error
	if input.HasStatus {
		output.Status, err = normalizeTokenStatus(input.Status)
		if err != nil {
			return output, tokenUpdateValidationError{cause: err}
		}
	}
	if input.HasScopes {
		output.ScopesJSON, err = normalizeUpdateScopes(input.Scopes)
		if err != nil {
			return output, tokenUpdateValidationError{cause: err}
		}
	}
	if input.HasExpiresAt {
		output.ExpiresAt, err = normalizeUpdateExpiresAt(input.ExpiresAt)
		if err != nil {
			return output, tokenUpdateValidationError{cause: err}
		}
	}
	return output, nil
}
