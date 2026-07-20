package managementaccounttestdispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/jobs/accounttest"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/store/port"
)

func TestDispatchCreatesQueuedTaskAndEnqueuesTaskID(t *testing.T) {
	store := &dispatchStoreStub{account: testAccount(), found: true}
	client := &dispatchQueueStub{}
	service := NewService(Options{Store: store, EnqueueClient: client, NewID: func(string) string { return "accttest_1" }})

	task, err := service.Dispatch(context.Background(), Input{
		AccountID: " account_1 ", Model: " gpt-5.5 ", TestEndpointMode: " responses_sse ",
		Access: port.ManagementAccountTestAccess{ActorSystemAccountID: "admin_1", ActorRole: "admin", FilterSystemAccountID: "owner_1"},
	})
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if task.ID != "accttest_1" || task.Status != "queued" || store.createInput.Model != "gpt-5.5" {
		t.Fatalf("task=%+v create=%+v", task, store.createInput)
	}
	decoded, err := accounttest.Decode(client.payload)
	if err != nil || decoded.TaskID != task.ID || client.taskType != accounttest.TaskType {
		t.Fatalf("decoded=%+v err=%v type=%q", decoded, err, client.taskType)
	}
}

func TestDispatchDraftValidatesSavedAccountIdentity(t *testing.T) {
	store := &dispatchStoreStub{account: testAccount(), found: true}
	service := NewService(Options{Store: store, EnqueueClient: &dispatchQueueStub{}, Codec: dispatchCodecStub{}, NewID: func(string) string { return "accttest_1" }})
	input := Input{
		AccountID: "account_1",
		Access:    port.ManagementAccountTestAccess{ActorSystemAccountID: "owner_1", ActorRole: "user", FilterSystemAccountID: "owner_1"},
		DraftAccount: map[string]any{
			"providerCode": "anthropic", "providerProtocolProfileId": "profile_openai", "name": "Draft", "type": "api_key",
			"groupId": "group_1", "healthCheckModel": "gpt-5.5", "healthCheckEndpointMode": "responses_sse",
		},
	}
	if _, err := service.Dispatch(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Dispatch() error = %v, want ErrInvalidInput", err)
	}
}

func TestDispatchDraftRejectsAuthorizedAccount(t *testing.T) {
	account := testAccount()
	account.AccessType = "authorized"
	service := NewService(Options{Store: &dispatchStoreStub{account: account, found: true}, EnqueueClient: &dispatchQueueStub{}, Codec: dispatchCodecStub{}})
	_, err := service.Dispatch(context.Background(), Input{
		AccountID: "account_1", Access: port.ManagementAccountTestAccess{ActorSystemAccountID: "viewer_1", ActorRole: "user", FilterSystemAccountID: "viewer_1"},
		DraftAccount: map[string]any{"providerCode": "openai", "providerProtocolProfileId": "profile_openai", "name": "Draft", "type": "api_key", "groupId": "group_1", "healthCheckModel": "gpt-5.5", "healthCheckEndpointMode": "responses_sse"},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Dispatch() error = %v", err)
	}
}

func TestDispatchMarksQueuedTaskFailedWhenEnqueueFails(t *testing.T) {
	queueErr := errors.New("redis unavailable")
	store := &dispatchStoreStub{account: testAccount(), found: true}
	client := &dispatchQueueStub{err: queueErr}
	service := NewService(Options{Store: store, EnqueueClient: client, NewID: func(string) string { return "accttest_1" }})

	task, err := service.Dispatch(context.Background(), Input{AccountID: "account_1", Access: port.ManagementAccountTestAccess{ActorSystemAccountID: "admin_1", ActorRole: "admin"}})
	if !errors.Is(err, ErrEnqueueFailed) || task.Status != "failed" || store.failedTaskID != "accttest_1" {
		t.Fatalf("task=%+v err=%v failedTaskID=%q", task, err, store.failedTaskID)
	}
}

func TestGetTaskUsesExistingScopedProjection(t *testing.T) {
	want := queuedTask("accttest_1")
	store := &dispatchStoreStub{task: want, found: true}
	service := NewService(Options{Store: store})
	got, err := service.GetTask(context.Background(), GetTaskInput{TaskID: " accttest_1 ", Access: port.ManagementAccountTestAccess{ActorSystemAccountID: "owner_1", ActorRole: "user", FilterSystemAccountID: "owner_1"}})
	if err != nil || got.ID != want.ID || store.getTaskID != want.ID {
		t.Fatalf("got=%+v err=%v getTaskID=%q", got, err, store.getTaskID)
	}
}

func testAccount() port.ManagementAccountTestDispatchAccount {
	return port.ManagementAccountTestDispatchAccount{ID: "account_1", Name: "Account", ProviderCode: "openai", ProviderProtocolProfileID: "profile_openai", ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key", AccessType: "owner", HealthCheckModel: "gpt-5.5", HealthCheckEndpointMode: "responses_sse"}
}

func queuedTask(id string) Task {
	now := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	return Task{ID: id, AccountID: "account_1", Status: "queued", Message: "等待后台测试", CreatedAt: now, QueuedAt: now, UpdatedAt: now}
}

type dispatchStoreStub struct {
	account      port.ManagementAccountTestDispatchAccount
	task         Task
	found        bool
	createInput  port.ManagementAccountTestDispatchCreateInput
	failedTaskID string
	getTaskID    string
}

func (s *dispatchStoreStub) ResolveManagementAccountTestAccount(context.Context, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestDispatchAccount, bool, error) {
	return s.account, s.found, nil
}
func (s *dispatchStoreStub) CreateManagementAccountTestTask(_ context.Context, input port.ManagementAccountTestDispatchCreateInput) (port.ManagementAccountTestTask, bool, error) {
	s.createInput = input
	if s.task.ID == "" {
		s.task = queuedTask(input.TaskID)
		s.task.Model = input.Model
		s.task.TestEndpointMode = input.TestEndpointMode
	}
	return s.task, true, nil
}
func (s *dispatchStoreStub) GetManagementAccountTestTask(_ context.Context, id string, _ port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	s.getTaskID = id
	return s.task, s.found, nil
}
func (s *dispatchStoreStub) MarkManagementAccountTestEnqueueFailed(_ context.Context, id string, _ port.ManagementAccountTestAccess, _ string) (port.ManagementAccountTestTask, bool, error) {
	s.failedTaskID = id
	task := s.task
	if task.ID == "" {
		task = queuedTask(id)
	}
	task.Status = "failed"
	return task, true, nil
}

type dispatchQueueStub struct {
	taskType string
	payload  []byte
	err      error
}

func (s *dispatchQueueStub) Enqueue(_ context.Context, taskType string, payload []byte, _ queue.EnqueueOptions) (queue.TaskInfo, error) {
	s.taskType = taskType
	s.payload = append([]byte(nil), payload...)
	return queue.TaskInfo{}, s.err
}

type dispatchCodecStub struct{}

func (dispatchCodecStub) EncryptJSON(map[string]any) (string, error) { return "encrypted-draft", nil }
