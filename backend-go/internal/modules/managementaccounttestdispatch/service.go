package managementaccounttestdispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/jobs/accounttest"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrInvalidInput  = errors.New("账户测试参数无效")
	ErrNotFound      = errors.New("账户测试资源不存在")
	ErrEnqueueFailed = errors.New("账户测试任务投递失败")
)

type EnqueueClient interface {
	Enqueue(context.Context, string, []byte, queue.EnqueueOptions) (queue.TaskInfo, error)
}

type Options struct {
	Store         port.ManagementAccountTestDispatchStore
	EnqueueClient EnqueueClient
	Codec         CredentialCodec
	NewID         func(string) string
}

type CredentialCodec interface {
	EncryptJSON(map[string]any) (string, error)
}

type Service struct{ opts Options }

type Input struct {
	AccountID        string
	SessionID        string
	Model            string
	TestEndpointMode string
	Access           port.ManagementAccountTestAccess
	DraftAccount     map[string]any
}

type Task = port.ManagementAccountTestTask

func NewService(opts Options) *Service {
	if opts.NewID == nil {
		opts.NewID = func(prefix string) string {
			return prefix + "_" + strings.ReplaceAll(fmt.Sprint(time.Now().UnixNano()), "-", "")
		}
	}
	return &Service{opts: opts}
}

func (s *Service) Dispatch(ctx context.Context, input Input) (Task, error) {
	if s.opts.Store == nil || strings.TrimSpace(input.AccountID) == "" || strings.TrimSpace(input.Access.ActorSystemAccountID) == "" {
		return Task{}, ErrInvalidInput
	}
	account, found, err := s.opts.Store.ResolveManagementAccountTestAccount(ctx, strings.TrimSpace(input.AccountID), input.Access)
	if err != nil {
		return Task{}, err
	}
	if !found || account.AccessType == "authorized" {
		return Task{}, ErrInvalidInput
	}
	if input.DraftAccount != nil {
		provider, _ := input.DraftAccount["providerCode"].(string)
		profile, _ := input.DraftAccount["providerProtocolProfileId"].(string)
		if strings.TrimSpace(provider) != account.ProviderCode || strings.TrimSpace(profile) != account.ProviderProtocolProfileID || s.opts.Codec == nil {
			return Task{}, ErrInvalidInput
		}
	}
	taskID := s.opts.NewID("accttest")
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = account.HealthCheckModel
	}
	endpoint := strings.TrimSpace(input.TestEndpointMode)
	if endpoint == "" {
		endpoint = account.HealthCheckEndpointMode
	}
	task, ok, err := s.opts.Store.CreateManagementAccountTestTask(ctx, port.ManagementAccountTestDispatchCreateInput{
		TaskID: taskID, SessionID: strings.TrimSpace(input.SessionID), AccountID: account.ID, AccountName: account.Name,
		ProviderCode: account.ProviderCode, ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode: account.ProtocolCode, ProtocolVersion: account.ProtocolVersion, AccountType: account.Type,
		Model: model, TestEndpointMode: endpoint, Access: input.Access,
	})
	if err != nil || !ok {
		if err != nil {
			return Task{}, err
		}
		return Task{}, ErrInvalidInput
	}
	if s.opts.EnqueueClient == nil {
		return task, ErrEnqueueFailed
	}
	if _, err := accounttest.Enqueue(ctx, s.opts.EnqueueClient, accounttest.EnqueuePayload{TaskID: task.ID}); err != nil {
		failed, _, markErr := s.opts.Store.MarkManagementAccountTestEnqueueFailed(ctx, task.ID, input.Access, err.Error())
		if markErr != nil {
			return task, fmt.Errorf("%w: %v", ErrEnqueueFailed, markErr)
		}
		return failed, ErrEnqueueFailed
	}
	return task, nil
}

type GetTaskInput struct {
	TaskID string
	Access port.ManagementAccountTestAccess
}

func (s *Service) GetTask(ctx context.Context, input GetTaskInput) (Task, error) {
	if s.opts.Store == nil || strings.TrimSpace(input.TaskID) == "" {
		return Task{}, ErrInvalidInput
	}
	task, found, err := s.opts.Store.GetManagementAccountTestTask(ctx, strings.TrimSpace(input.TaskID), input.Access)
	if err != nil {
		return Task{}, err
	}
	if !found {
		return Task{}, ErrNotFound
	}
	return task, nil
}
