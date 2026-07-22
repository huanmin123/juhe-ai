package managementaccounttestdispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/jobs/accounttest"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementaccountdraft"
	"juhe-ai/backend-go/internal/modules/managementaccounttestoptions"
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
	Drafts        DraftPreparer
	TestOptions   TestOptionsProvider
	NewID         func(string) string
}

type DraftPreparer interface {
	Prepare(context.Context, managementaccountdraft.Input) (managementaccountdraft.Snapshot, error)
}

type TestOptionsProvider interface {
	Get(context.Context, managementaccounttestoptions.Input) (managementaccounttestoptions.Result, bool, error)
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

type DraftInput struct {
	SessionID        string
	TestEndpointMode string
	Account          managementaccountdraft.Account
	Access           port.ManagementAccountTestAccess
}

type Task = port.ManagementAccountTestTask

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }
func (e *ValidationError) Unwrap() error { return ErrInvalidInput }

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
	if !found {
		return Task{}, ErrInvalidInput
	}

	diagnostics := "full"
	if account.AccessType == "authorized" && !isAdminRole(input.Access.ActorRole) {
		diagnostics = "limited"
	}

	taskID := s.opts.NewID("accttest")
	var model, endpoint string
	if input.DraftAccount != nil {
		if account.AccessType == "authorized" {
			return Task{}, validationError("授权账户测试不支持使用未保存表单配置")
		}
		return Task{}, validationError("Go 账户测试暂不支持未保存草稿配置")
	}
	model, endpoint, err = s.resolveSavedSelection(ctx, account.ID, input)
	if err != nil {
		return Task{}, err
	}

	task, ok, err := s.opts.Store.CreateManagementAccountTestTask(ctx, port.ManagementAccountTestDispatchCreateInput{
		TaskID: taskID, SessionID: strings.TrimSpace(input.SessionID), AccountID: account.ID, AccountName: account.Name,
		ProviderCode: account.ProviderCode, ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode: account.ProtocolCode, ProtocolVersion: account.ProtocolVersion, AccountType: account.Type,
		Diagnostics: diagnostics, Model: model, TestEndpointMode: endpoint, Access: input.Access,
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

func (s *Service) DispatchDraft(ctx context.Context, input DraftInput) (Task, error) {
	if s.opts.Store == nil || s.opts.Drafts == nil || s.opts.Codec == nil || strings.TrimSpace(input.Access.ActorSystemAccountID) == "" {
		return Task{}, ErrInvalidInput
	}
	snapshot, err := s.opts.Drafts.Prepare(ctx, managementaccountdraft.Input{Access: input.Access, Account: input.Account})
	if err != nil {
		return Task{}, err
	}
	endpoint := strings.TrimSpace(input.TestEndpointMode)
	if endpoint == "" {
		endpoint = snapshot.HealthCheckEndpointMode
	}
	if err := managementaccountdraft.ValidateTestEndpoint(snapshot, endpoint); err != nil {
		return Task{}, err
	}
	payload, err := snapshot.Map()
	if err != nil {
		return Task{}, fmt.Errorf("encode account draft snapshot: %w", err)
	}
	encrypted, err := s.opts.Codec.EncryptJSON(payload)
	if err != nil {
		return Task{}, fmt.Errorf("encrypt account draft snapshot: %w", err)
	}
	taskID := s.opts.NewID("accttest")
	task, ok, err := s.opts.Store.CreateManagementAccountTestTask(ctx, port.ManagementAccountTestDispatchCreateInput{
		TaskID: taskID, SessionID: strings.TrimSpace(input.SessionID), AccountID: snapshot.ID, AccountName: snapshot.Name,
		ProviderCode: snapshot.ProviderCode, ProviderProtocolProfileID: snapshot.ProviderProtocolProfileID,
		ProtocolCode: snapshot.ProtocolCode, ProtocolVersion: snapshot.ProtocolVersion, AccountType: snapshot.Type,
		Diagnostics: "full", Model: snapshot.HealthCheckModel, TestEndpointMode: endpoint,
		DraftAccountEncrypted: encrypted, Access: input.Access,
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

func (s *Service) resolveSavedSelection(ctx context.Context, accountID string, input Input) (string, string, error) {
	model := strings.TrimSpace(input.Model)
	if model == "" {
		return "", "", validationError("请选择测试模型")
	}
	if s.opts.TestOptions == nil {
		return "", "", fmt.Errorf("management account test options provider is required")
	}
	options, found, err := s.opts.TestOptions.Get(ctx, managementaccounttestoptions.Input{
		AccountID:       accountID,
		SystemAccountID: strings.TrimSpace(input.Access.FilterSystemAccountID),
	})
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "", ErrInvalidInput
	}
	var selected *managementaccounttestoptions.ModelOption
	for index := range options.Models {
		if options.Models[index].Model == model {
			selected = &options.Models[index]
			break
		}
	}
	if selected == nil {
		return "", "", validationError("模型不在当前账户供应商可用目录中：" + model)
	}
	endpoint := input.TestEndpointMode
	if endpoint == "" && len(selected.TestEndpointModes) > 0 {
		endpoint = selected.TestEndpointModes[0]
	}
	if endpoint == "" || !containsString(selected.TestEndpointModes, endpoint) {
		label := input.TestEndpointMode
		if label == "" {
			label = "未选择"
		}
		return "", "", validationError(fmt.Sprintf("模型 %s 不支持本次检查协议：%s", model, label))
	}
	return model, endpoint, nil
}

func isAdminRole(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	return role == "admin" || role == "super_admin"
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validationError(message string) error { return &ValidationError{Message: message} }

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
