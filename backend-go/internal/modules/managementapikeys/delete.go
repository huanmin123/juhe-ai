package managementapikeys

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	apiKeyDeletedReason             = "api_key_deleted"
	apiKeyDeleteInvalidationTimeout = 5 * time.Second
)

var (
	ErrAPIKeyDeleteInvalid                     = errors.New("API Key 删除参数无效")
	ErrAPIKeyDefaultDelete                     = errors.New("默认 API Key 不允许删除")
	ErrAPIKeyChatDelete                        = errors.New("AI 对话 API Key 不允许删除")
	ErrAPIKeyDeleteValidationCacheInvalidation = errors.New("API Key 删除后校验缓存失效失败")
)

type DeleteInput struct {
	ActorSystemAccountID string
	ActorRole            string
	SystemAccountID      string
	SelfOnly             bool
	APIKeyID             string
}

type DeleteResult struct {
	APIKeyID             string
	Name                 string
	OwnerSystemAccountID string
	Committed            bool
}

func (s *Service) Delete(ctx context.Context, input DeleteInput) (DeleteResult, error) {
	if s.deleter == nil {
		return DeleteResult{}, fmt.Errorf("management API Key deleter is required")
	}
	if s.invalidator == nil {
		return DeleteResult{}, fmt.Errorf("management API Key cache invalidator is required")
	}
	apiKeyID, ownerSystemAccountID, err := deleteScope(input)
	if err != nil {
		return DeleteResult{}, err
	}

	stored, err := s.deleter.DeleteManagementAPIKey(ctx, port.ManagementAPIKeyDeleteInput{
		APIKeyID:             apiKeyID,
		OwnerSystemAccountID: ownerSystemAccountID,
		DeletedAt:            s.now().UTC(),
	})
	if err != nil {
		switch {
		case errors.Is(err, port.ErrManagementAPIKeyNotFound):
			return DeleteResult{}, ErrAPIKeyNotFound
		case errors.Is(err, port.ErrManagementAPIKeyDefaultDelete):
			return DeleteResult{}, ErrAPIKeyDefaultDelete
		case errors.Is(err, port.ErrManagementAPIKeyChatDelete):
			return DeleteResult{}, ErrAPIKeyChatDelete
		default:
			return DeleteResult{}, err
		}
	}

	result := DeleteResult{
		APIKeyID:             stored.APIKeyID,
		Name:                 stored.Name,
		OwnerSystemAccountID: stored.OwnerSystemAccountID,
		Committed:            true,
	}
	invalidationCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		apiKeyDeleteInvalidationTimeout,
	)
	defer cancel()
	if err := s.invalidator.InvalidateAPIKeyValidationCache(invalidationCtx); err != nil {
		return result, ErrAPIKeyDeleteValidationCacheInvalidation
	}
	_ = s.invalidator.InvalidateAPIKeyLookupCache(
		invalidationCtx,
		stored.APIKeyID,
		apiKeyDeletedReason,
	)
	_ = s.invalidator.InvalidateGatewayRuntime(invalidationCtx, apiKeyDeletedReason)
	_ = s.invalidator.InvalidateAPIKeyQuotaChanged(
		invalidationCtx,
		stored.APIKeyID,
		apiKeyDeletedReason,
	)
	return result, nil
}

func deleteScope(input DeleteInput) (string, string, error) {
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	apiKeyID := strings.TrimSpace(input.APIKeyID)
	if actorSystemAccountID == "" || apiKeyID == "" {
		return "", "", ErrAPIKeyDeleteInvalid
	}
	if input.SelfOnly || !isAdminRole(input.ActorRole) {
		return apiKeyID, actorSystemAccountID, nil
	}
	ownerSystemAccountID := strings.TrimSpace(input.SystemAccountID)
	if ownerSystemAccountID == "all" {
		ownerSystemAccountID = ""
	}
	return apiKeyID, ownerSystemAccountID, nil
}
