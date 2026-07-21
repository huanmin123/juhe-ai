package managementaccounttestsession

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

type CancelDispatcher interface {
	DispatchAccountTestCancel(taskID string)
}

type Service struct {
	store      port.ManagementAccountTestSessionStore
	dispatcher CancelDispatcher
}

func NewService(store port.ManagementAccountTestSessionStore, dispatcher CancelDispatcher) *Service {
	return &Service{store: store, dispatcher: dispatcher}
}

func (s *Service) Create(ctx context.Context, access port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, error) {
	if err := validateAccess(access); err != nil {
		return port.ManagementAccountTestSession{}, err
	}
	if s.store == nil {
		return port.ManagementAccountTestSession{}, fmt.Errorf("management account test session store is required")
	}
	id := "acctsess_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	return s.store.CreateManagementAccountTestSession(ctx, id, access)
}

func (s *Service) Heartbeat(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	if err := validateIDAndAccess(id, access); err != nil {
		return port.ManagementAccountTestSession{}, false, err
	}
	return s.store.HeartbeatManagementAccountTestSession(ctx, strings.TrimSpace(id), access)
}

func (s *Service) Complete(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	if err := validateIDAndAccess(id, access); err != nil {
		return port.ManagementAccountTestSession{}, false, err
	}
	return s.store.CompleteManagementAccountTestSession(ctx, strings.TrimSpace(id), access)
}

func (s *Service) CancelSession(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	if err := validateIDAndAccess(id, access); err != nil {
		return port.ManagementAccountTestSession{}, false, err
	}
	session, taskIDs, found, err := s.store.CancelManagementAccountTestSession(ctx, strings.TrimSpace(id), access)
	if err != nil || !found {
		return session, found, err
	}
	if s.dispatcher != nil {
		for _, taskID := range taskIDs {
			s.dispatcher.DispatchAccountTestCancel(taskID)
		}
	}
	return session, true, nil
}

func (s *Service) CancelTask(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	if err := validateIDAndAccess(id, access); err != nil {
		return port.ManagementAccountTestTask{}, false, err
	}
	task, found, err := s.store.CancelManagementAccountTestTask(ctx, strings.TrimSpace(id), access)
	if err == nil && found && s.dispatcher != nil {
		s.dispatcher.DispatchAccountTestCancel(task.ID)
	}
	return task, found, err
}

func validateIDAndAccess(id string, access port.ManagementAccountTestAccess) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("账户测试资源 ID 不能为空")
	}
	return validateAccess(access)
}

func validateAccess(access port.ManagementAccountTestAccess) error {
	if strings.TrimSpace(access.ActorSystemAccountID) == "" || strings.TrimSpace(access.ActorRole) == "" {
		return fmt.Errorf("缺少系统账户上下文")
	}
	return nil
}
