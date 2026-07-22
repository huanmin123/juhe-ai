package httpapi

import (
	"context"
	"log/slog"
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
		Handle: func(ctx context.Context, input port.PublicAPILogInput) error {
			err := writePublicAPILog(ctx, client, input)
			if err != nil {
				warnPublicAPILogEnqueueFailure(logger, input, err)
			}
			return err
		},
	})
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
