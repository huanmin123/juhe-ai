package managementaccountteststatus

import (
	"context"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

const MaxTaskReadCount = 200

type Service struct {
	reader port.ManagementAccountTestStatusReader
}

func NewService(reader port.ManagementAccountTestStatusReader) *Service {
	return &Service{reader: reader}
}

func (s *Service) ListTasks(ctx context.Context, ids []string, access port.ManagementAccountTestAccess) ([]port.ManagementAccountTestTask, error) {
	if err := validateAccess(access); err != nil {
		return nil, err
	}
	ids = normalizeIDs(ids)
	if len(ids) > MaxTaskReadCount {
		return nil, fmt.Errorf("账户测试任务最多查询 %d 项", MaxTaskReadCount)
	}
	if s.reader == nil {
		return nil, fmt.Errorf("management account test status reader is required")
	}
	return s.reader.ListManagementAccountTestTasks(ctx, ids, access)
}

func (s *Service) GetSession(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	if err := validateResource(id, access); err != nil {
		return port.ManagementAccountTestSession{}, false, err
	}
	if s.reader == nil {
		return port.ManagementAccountTestSession{}, false, fmt.Errorf("management account test status reader is required")
	}
	return s.reader.GetManagementAccountTestSession(ctx, strings.TrimSpace(id), access)
}

func (s *Service) ListSessionTasks(ctx context.Context, id string, access port.ManagementAccountTestAccess) ([]port.ManagementAccountTestTask, bool, error) {
	if err := validateResource(id, access); err != nil {
		return nil, false, err
	}
	if s.reader == nil {
		return nil, false, fmt.Errorf("management account test status reader is required")
	}
	tasks, found, err := s.reader.ListManagementAccountTestSessionTasks(ctx, strings.TrimSpace(id), access, MaxTaskReadCount+1)
	if err == nil && len(tasks) > MaxTaskReadCount {
		return nil, true, fmt.Errorf("账户测试会话任务超过读取上限 %d", MaxTaskReadCount)
	}
	return tasks, found, err
}

func (s *Service) GetTask(ctx context.Context, id string, access port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	if err := validateResource(id, access); err != nil {
		return port.ManagementAccountTestTask{}, false, err
	}
	if s.reader == nil {
		return port.ManagementAccountTestTask{}, false, fmt.Errorf("management account test status reader is required")
	}
	return s.reader.GetManagementAccountTestTask(ctx, strings.TrimSpace(id), access)
}

func normalizeIDs(ids []string) []string {
	result := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				result = append(result, id)
			}
		}
	}
	return result
}

func validateResource(id string, access port.ManagementAccountTestAccess) error {
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
