package managementaccountbatchedit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrAccess          = errors.New("账户不存在或无权批量编辑")
	ErrSameScope       = errors.New("批量编辑账户必须属于同一系统账户作用域")
	ErrNoUpdates       = errors.New("请至少选择一项需要覆盖的配置")
	ErrVersionConflict = errors.New("账户配置已发生变化，请刷新后重试")
)

type Service struct {
	Reader port.ManagementAccountBatchEditReader
	Editor port.ManagementAccountBatchEditor
	Now    func() time.Time
}

func NewService(reader port.ManagementAccountBatchEditReader, editor port.ManagementAccountBatchEditor) *Service {
	return &Service{Reader: reader, Editor: editor, Now: time.Now}
}

func (s *Service) Context(ctx context.Context, systemAccountID string, ids []string) ([]port.ManagementAccountBatchEditAccount, error) {
	if len(ids) < 2 || len(ids) > 100 || s.Reader == nil {
		return nil, ErrAccess
	}
	if hasDuplicateIDs(ids) {
		return nil, ErrAccess
	}
	accounts, ok, err := s.Reader.LoadManagementAccountBatchEditContext(ctx, strings.TrimSpace(systemAccountID), ids)
	if err != nil {
		return nil, err
	}
	if !ok || len(accounts) != len(ids) {
		return nil, ErrAccess
	}
	owner := accounts[0].SystemAccountID
	for _, account := range accounts {
		if account.SystemAccountID != owner {
			return nil, ErrSameScope
		}
	}
	return accounts, nil
}

func (s *Service) Update(ctx context.Context, input port.ManagementAccountBatchEditInput) (port.ManagementAccountBatchEditResult, error) {
	if len(input.Targets) < 2 || len(input.Targets) > 100 {
		return port.ManagementAccountBatchEditResult{}, ErrAccess
	}
	if len(input.Updates) == 0 {
		return port.ManagementAccountBatchEditResult{}, ErrNoUpdates
	}
	ids := make([]string, 0, len(input.Targets))
	for _, target := range input.Targets {
		if strings.TrimSpace(target.AccountID) == "" || target.ConfigRevision < 1 {
			return port.ManagementAccountBatchEditResult{}, ErrAccess
		}
		ids = append(ids, target.AccountID)
	}
	if hasDuplicateIDs(ids) {
		return port.ManagementAccountBatchEditResult{}, ErrAccess
	}
	if super, _ := input.Updates["super_priority_enabled"].(bool); super {
		if fallback, _ := input.Updates["fallback_enabled"].(bool); fallback {
			return port.ManagementAccountBatchEditResult{}, errors.New("超级优先和降级备用不能同时开启")
		}
	}
	if s.Editor == nil {
		return port.ManagementAccountBatchEditResult{}, fmt.Errorf("batch editor is required")
	}
	if input.Now.IsZero() {
		input.Now = s.Now().UTC()
	}
	result, ok, err := s.Editor.UpdateManagementAccountsBatch(ctx, input)
	if err != nil {
		return result, err
	}
	if !ok {
		return result, ErrVersionConflict
	}
	return result, nil
}

func hasDuplicateIDs(ids []string) bool {
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if _, ok := seen[id]; ok {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}
