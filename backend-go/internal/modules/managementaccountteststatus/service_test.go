package managementaccountteststatus

import (
	"context"
	"fmt"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestListTasksDeduplicatesAndBoundsIDs(t *testing.T) {
	reader := &statusReaderStub{}
	service := NewService(reader)
	access := port.ManagementAccountTestAccess{ActorSystemAccountID: "user", ActorRole: "user"}
	_, err := service.ListTasks(context.Background(), []string{" a ", "a", "b", ""}, access)
	if err != nil || len(reader.ids) != 2 || reader.ids[0] != "a" || reader.ids[1] != "b" {
		t.Fatalf("ids=%v err=%v", reader.ids, err)
	}
	ids := make([]string, MaxTaskReadCount+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("task_%d", i)
	}
	if _, err = service.ListTasks(context.Background(), ids, access); err == nil {
		t.Fatal("expected bounded read error")
	}
}
func TestSessionTasksRequestsOneOverflowRow(t *testing.T) {
	reader := &statusReaderStub{}
	service := NewService(reader)
	_, _, err := service.ListSessionTasks(context.Background(), "session", port.ManagementAccountTestAccess{ActorSystemAccountID: "user", ActorRole: "user"})
	if err != nil || reader.limit != MaxTaskReadCount+1 {
		t.Fatalf("limit=%d err=%v", reader.limit, err)
	}
}

type statusReaderStub struct {
	ids   []string
	limit int
}

func (s *statusReaderStub) GetManagementAccountTestSession(context.Context, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return port.ManagementAccountTestSession{}, true, nil
}
func (s *statusReaderStub) ListManagementAccountTestSessionTasks(_ context.Context, _ string, _ port.ManagementAccountTestAccess, limit int) ([]port.ManagementAccountTestTask, bool, error) {
	s.limit = limit
	return nil, true, nil
}
func (s *statusReaderStub) GetManagementAccountTestTask(context.Context, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	return port.ManagementAccountTestTask{}, true, nil
}
func (s *statusReaderStub) ListManagementAccountTestTasks(_ context.Context, ids []string, _ port.ManagementAccountTestAccess) ([]port.ManagementAccountTestTask, error) {
	s.ids = ids
	return nil, nil
}
