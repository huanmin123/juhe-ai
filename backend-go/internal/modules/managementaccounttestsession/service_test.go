package managementaccounttestsession

import (
	"context"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestCreateAndCancelDispatch(t *testing.T) {
	store := &sessionStoreStub{}
	dispatcher := &cancelDispatcherStub{}
	service := NewService(store, dispatcher)
	access := port.ManagementAccountTestAccess{ActorSystemAccountID: "admin", ActorRole: "admin", FilterSystemAccountID: "owner"}
	session, err := service.Create(context.Background(), access)
	if err != nil || !strings.HasPrefix(store.createdID, "acctsess_") || session.ID != store.createdID {
		t.Fatalf("session=%+v id=%q err=%v", session, store.createdID, err)
	}
	_, found, err := service.CancelSession(context.Background(), session.ID, access)
	if err != nil || !found || strings.Join(dispatcher.ids, ",") != "task_1,task_2" {
		t.Fatalf("found=%v dispatch=%v err=%v", found, dispatcher.ids, err)
	}
}

type sessionStoreStub struct{ createdID string }

func (s *sessionStoreStub) CreateManagementAccountTestSession(_ context.Context, id string, _ port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, error) {
	s.createdID = id
	return port.ManagementAccountTestSession{ID: id, Status: "running"}, nil
}
func (s *sessionStoreStub) HeartbeatManagementAccountTestSession(context.Context, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return port.ManagementAccountTestSession{}, false, nil
}
func (s *sessionStoreStub) CompleteManagementAccountTestSession(context.Context, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return port.ManagementAccountTestSession{}, false, nil
}
func (s *sessionStoreStub) CancelManagementAccountTestSession(context.Context, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, []string, bool, error) {
	return port.ManagementAccountTestSession{Status: "canceled"}, []string{"task_1", "task_2"}, true, nil
}
func (s *sessionStoreStub) CancelManagementAccountTestTask(context.Context, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	return port.ManagementAccountTestTask{}, false, nil
}

type cancelDispatcherStub struct{ ids []string }

func (s *cancelDispatcherStub) Cancel(_ context.Context, id string) error {
	s.ids = append(s.ids, id)
	return nil
}
