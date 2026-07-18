package managementexternalintegrationsources

import (
	"context"
	"errors"
	"fmt"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrDeleteInvalid           = errors.New("来源系统不存在")
	ErrBuiltInDeleteRestricted = errors.New("内置测试 Token 不支持删除")
)

type DeleteInput struct {
	SourceID string
}

type DeleteResult struct {
	SourceID   string
	SourceName string
	TokenCount int64
}

type DeleteService struct {
	store port.ManagementExternalIntegrationSourceDeleter
}

func NewDeleteService(store port.ManagementExternalIntegrationSourceDeleter) *DeleteService {
	return &DeleteService{store: store}
}

func (s *DeleteService) Delete(ctx context.Context, input DeleteInput) (DeleteResult, error) {
	if s == nil || s.store == nil {
		return DeleteResult{}, fmt.Errorf("management external integration source deleter is required")
	}
	sourceID := trimECMAScriptWhitespace(input.SourceID)
	if sourceID == "" {
		return DeleteResult{}, ErrDeleteInvalid
	}
	if sourceID == publicapi.BuiltInTestSourceID {
		return DeleteResult{}, ErrBuiltInDeleteRestricted
	}
	stored, err := s.store.DeleteManagementExternalIntegrationSource(ctx, sourceID)
	if err != nil {
		switch {
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceNotFound):
			return DeleteResult{}, ErrNotFound
		case errors.Is(err, port.ErrManagementExternalIntegrationSourceBuiltInDeleteRestricted):
			return DeleteResult{}, ErrBuiltInDeleteRestricted
		default:
			return DeleteResult{}, err
		}
	}
	return DeleteResult{
		SourceID:   stored.SourceID,
		SourceName: stored.SourceName,
		TokenCount: stored.TokenCount,
	}, nil
}
