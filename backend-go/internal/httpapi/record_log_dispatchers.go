package httpapi

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	"juhe-ai/backend-go/internal/recorddispatch"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	recordLogDispatchCapacity = 256
	recordLogDispatchWorkers  = 1
	recordLogDispatchTimeout  = 5 * time.Second
)

type ManagementOperationLogSubmitter interface {
	Submit(context.Context, port.OperationLogInput) bool
}

type PublicAPILogSubmitter interface {
	Submit(context.Context, port.PublicAPILogInput) bool
}

func NewManagementOperationLogDispatcher(
	client operationlogjob.EnqueueClient,
	settingsReader OperationLogSettingsReader,
	logger *slog.Logger,
) *recorddispatch.Dispatcher[port.OperationLogInput] {
	if client == nil {
		return nil
	}
	return newManagementOperationLogDispatcher(recordLogDispatchCapacity, settingsReader, client, logger)
}

func newManagementOperationLogDispatcher(
	capacity int,
	settingsReader OperationLogSettingsReader,
	client operationlogjob.EnqueueClient,
	logger *slog.Logger,
) *recorddispatch.Dispatcher[port.OperationLogInput] {
	return recorddispatch.New(recorddispatch.Options[port.OperationLogInput]{
		Capacity: capacity,
		Workers:  recordLogDispatchWorkers,
		Timeout:  recordLogDispatchTimeout,
		Clone:    cloneOperationLogInput,
		Handle: func(ctx context.Context, input port.OperationLogInput) error {
			err := writeManagementOperationLog(ctx, client, settingsReader, input)
			if err != nil {
				warnManagementOperationLogEnqueueFailure(logger, input, err)
			}
			return err
		},
	})
}

type synchronousManagementOperationLogSubmitter struct {
	client         operationlogjob.EnqueueClient
	settingsReader OperationLogSettingsReader
	logger         *slog.Logger
}

func newSynchronousManagementOperationLogSubmitter(
	client operationlogjob.EnqueueClient,
	settingsReader OperationLogSettingsReader,
	logger *slog.Logger,
) ManagementOperationLogSubmitter {
	if client == nil {
		return nil
	}
	return synchronousManagementOperationLogSubmitter{
		client:         client,
		settingsReader: settingsReader,
		logger:         logger,
	}
}

func (s synchronousManagementOperationLogSubmitter) Submit(ctx context.Context, input port.OperationLogInput) bool {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordLogDispatchTimeout)
	defer cancel()
	err := writeManagementOperationLog(writeCtx, s.client, s.settingsReader, input)
	if err != nil {
		warnManagementOperationLogEnqueueFailure(s.logger, input, err)
		return false
	}
	return true
}

func writeManagementOperationLog(
	ctx context.Context,
	client operationlogjob.EnqueueClient,
	settingsReader OperationLogSettingsReader,
	input port.OperationLogInput,
) error {
	maxChanges, err := operationLogMaxChangesPerRecord(ctx, settingsReader)
	if err != nil {
		return err
	}
	input.Changes = sanitizeManagementOperationLogChanges(input.Changes, maxChanges)
	_, err = operationlogjob.EnqueueWrite(ctx, client, input)
	return err
}

func warnManagementOperationLogEnqueueFailure(logger *slog.Logger, input port.OperationLogInput, err error) {
	if logger == nil {
		return
	}
	logger.Warn("管理端操作日志入队失败",
		slog.String("event", "operation_log_enqueue_failed"),
		slog.String("operation_key", input.OperationKey),
		slog.String("resource_id", input.ResourceID),
		slog.String("request_id", input.TraceID),
		slog.Any("error", err),
	)
}

func NewPublicAPILogDispatcher(
	client publicapilogjob.EnqueueClient,
	logger *slog.Logger,
) *recorddispatch.Dispatcher[port.PublicAPILogInput] {
	if client == nil {
		return nil
	}
	return recorddispatch.New(recorddispatch.Options[port.PublicAPILogInput]{
		Capacity: recordLogDispatchCapacity,
		Workers:  recordLogDispatchWorkers,
		Timeout:  recordLogDispatchTimeout,
		Clone:    clonePublicAPILogInput,
		Handle: func(ctx context.Context, input port.PublicAPILogInput) error {
			err := writePublicAPILog(ctx, client, input)
			if err != nil {
				warnPublicAPILogEnqueueFailure(logger, input, err)
			}
			return err
		},
	})
}

type operationLogTrySubmitter interface {
	TrySubmit(context.Context, port.OperationLogInput) recorddispatch.SubmitOutcome
}

type publicAPILogTrySubmitter interface {
	TrySubmit(context.Context, port.PublicAPILogInput) recorddispatch.SubmitOutcome
}

var (
	managementRecordRejectWarnAt atomic.Int64
	publicRecordRejectWarnAt     atomic.Int64
)

func submitManagementOperationLog(ctx context.Context, submitter ManagementOperationLogSubmitter, input port.OperationLogInput) recorddispatch.SubmitOutcome {
	if typed, ok := submitter.(operationLogTrySubmitter); ok {
		return typed.TrySubmit(ctx, input)
	}
	if submitter.Submit(ctx, input) {
		return recorddispatch.SubmitOutcome{Accepted: true}
	}
	return recorddispatch.SubmitOutcome{RejectionReason: recorddispatch.RejectionStopped}
}

func submitPublicAPILog(ctx context.Context, submitter PublicAPILogSubmitter, input port.PublicAPILogInput) recorddispatch.SubmitOutcome {
	if typed, ok := submitter.(publicAPILogTrySubmitter); ok {
		return typed.TrySubmit(ctx, input)
	}
	if submitter.Submit(ctx, input) {
		return recorddispatch.SubmitOutcome{Accepted: true}
	}
	return recorddispatch.SubmitOutcome{RejectionReason: recorddispatch.RejectionStopped}
}

func warnRecordDispatchRejection(logger *slog.Logger, clock *atomic.Int64, kind string, reason recorddispatch.RejectionReason) {
	if logger == nil || !allowRecordDispatchWarning(clock, time.Now()) {
		return
	}
	logger.Warn("记录异步派发被拒绝",
		slog.String("event", "record_dispatch_rejected"),
		slog.String("record_kind", kind),
		slog.String("rejection_reason", string(reason)),
	)
}

func allowRecordDispatchWarning(clock *atomic.Int64, now time.Time) bool {
	const interval = time.Minute
	next := now.Add(interval).UnixNano()
	for {
		previous := clock.Load()
		if previous > now.UnixNano() {
			return false
		}
		if clock.CompareAndSwap(previous, next) {
			return true
		}
	}
}

func cloneOperationLogInput(input port.OperationLogInput) port.OperationLogInput {
	result := input
	result.Changes = append([]port.OperationLogChange(nil), input.Changes...)
	for index := range result.Changes {
		result.Changes[index].Before = cloneLogValue(result.Changes[index].Before)
		result.Changes[index].After = cloneLogValue(result.Changes[index].After)
	}
	result.Metadata = cloneStringAnyMap(input.Metadata)
	result.Targets = append([]port.OperationLogTargetInput(nil), input.Targets...)
	result.Viewers = append([]port.OperationLogViewerInput(nil), input.Viewers...)
	if input.StatusCode != nil {
		statusCode := *input.StatusCode
		result.StatusCode = &statusCode
	}
	return result
}

func clonePublicAPILogInput(input port.PublicAPILogInput) port.PublicAPILogInput {
	result := input
	result.RequestData = cloneStringAnyMap(input.RequestData)
	result.ResponseData = cloneStringAnyMap(input.ResponseData)
	if input.StatusCode != nil {
		statusCode := *input.StatusCode
		result.StatusCode = &statusCode
	}
	if input.DurationMs != nil {
		duration := *input.DurationMs
		result.DurationMs = &duration
	}
	return result
}

func cloneStringAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = cloneLogValue(value)
	}
	return result
}

func cloneLogValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneStringAnyMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index := range typed {
			result[index] = cloneLogValue(typed[index])
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	case nil, string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return typed
	default:
		return typed
	}
}

type synchronousPublicAPILogSubmitter struct {
	client publicapilogjob.EnqueueClient
	logger *slog.Logger
}

func newSynchronousPublicAPILogSubmitter(client publicapilogjob.EnqueueClient, logger *slog.Logger) PublicAPILogSubmitter {
	if client == nil {
		return nil
	}
	return synchronousPublicAPILogSubmitter{client: client, logger: logger}
}

func (s synchronousPublicAPILogSubmitter) Submit(ctx context.Context, input port.PublicAPILogInput) bool {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publicAPILogEnqueueTimeout)
	defer cancel()
	err := writePublicAPILog(writeCtx, s.client, input)
	if err != nil {
		warnPublicAPILogEnqueueFailure(s.logger, input, err)
		return false
	}
	return true
}

func writePublicAPILog(ctx context.Context, client publicapilogjob.EnqueueClient, input port.PublicAPILogInput) error {
	_, err := publicapilogjob.EnqueueWrite(ctx, client, input)
	return err
}

func warnPublicAPILogEnqueueFailure(logger *slog.Logger, input port.PublicAPILogInput, err error) {
	if logger == nil {
		return
	}
	logger.Warn("公开接口日志入队失败",
		slog.String("method", input.Method),
		slog.String("path", input.Path),
		slog.Any("error", err),
	)
}
