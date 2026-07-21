package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/logging"
	"juhe-ai/backend-go/internal/store/port"
)

const DefaultIngestConcurrency = 1

type IngestOptions struct {
	Redis             queue.RedisOptions
	PublicAPILogStore port.PublicAPILogStore
	OperationLogStore port.OperationLogStore
	ShutdownTimeout   time.Duration
	LogLevel          string
	Concurrency       int
	Logger            *slog.Logger
}

type ingestTaskRuntime struct {
	taskID     string
	queue      string
	retryCount int
	maxRetry   int
	known      bool
}

type preparedIngestTask struct {
	traceID     string
	requestID   string
	logRecordID string
	write       func(context.Context) error
}

type loggedIngestTaskError struct {
	cause error
}

func (e *loggedIngestTaskError) Error() string { return e.cause.Error() }

func (e *loggedIngestTaskError) Unwrap() error { return e.cause }

func RunIngest(ctx context.Context, opts IngestOptions) error {
	if opts.PublicAPILogStore == nil {
		return fmt.Errorf("public api log store is required")
	}
	if opts.OperationLogStore == nil {
		return fmt.Errorf("operation log store is required")
	}
	if opts.Logger == nil {
		return fmt.Errorf("ingest logger is required")
	}

	server := asynq.NewServer(asynqRedisOptions(opts.Redis), newIngestAsynqConfig(ctx, opts))
	logger := opts.Logger
	mux := asynq.NewServeMux()
	mux.HandleFunc(publicapilogjob.TaskTypeWrite, func(ctx context.Context, task *asynq.Task) error {
		return handlePublicAPILogTaskLogged(ctx, logger, opts.PublicAPILogStore, task, nil)
	})
	mux.HandleFunc(operationlogjob.TaskTypeWrite, func(ctx context.Context, task *asynq.Task) error {
		return handleOperationLogTaskLogged(ctx, logger, opts.OperationLogStore, task, nil)
	})

	if err := server.Start(mux); err != nil {
		return err
	}

	<-ctx.Done()
	server.Shutdown()
	return nil
}

func newIngestAsynqConfig(ctx context.Context, opts IngestOptions) asynq.Config {
	logger := opts.Logger
	return asynq.Config{
		Concurrency:     defaultInt(opts.Concurrency, DefaultIngestConcurrency),
		Queues:          map[string]int{publicapilogjob.QueueName: 1, operationlogjob.QueueName: 1},
		ShutdownTimeout: defaultDuration(opts.ShutdownTimeout, 10*time.Second),
		LogLevel:        asynqLogLevel(opts.LogLevel),
		Logger:          newAsynqSlogLogger(logger),
		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
			logAsynqTaskError(ctx, logger, task, err, nil)
		}),
		BaseContext: func() context.Context {
			// Keep in-flight task contexts alive after process cancellation;
			// Asynq ShutdownTimeout owns the graceful drain window.
			return context.WithoutCancel(ctx)
		},
	}
}

type asynqSlogLogger struct {
	logger *slog.Logger
}

func newAsynqSlogLogger(logger *slog.Logger) asynq.Logger {
	return &asynqSlogLogger{logger: logger}
}

func (l *asynqSlogLogger) Debug(args ...interface{}) {
	l.logger.Debug("Asynq: "+asynqLogMessage(args...), slog.String("event", "asynq.log.debug"))
}

func (l *asynqSlogLogger) Info(args ...interface{}) {
	l.logger.Info("Asynq: "+asynqLogMessage(args...), slog.String("event", "asynq.log.info"))
}

func (l *asynqSlogLogger) Warn(args ...interface{}) {
	l.logger.Warn("Asynq: "+asynqLogMessage(args...), slog.String("event", "asynq.log.warn"))
}

func (l *asynqSlogLogger) Error(args ...interface{}) {
	l.logger.Error("Asynq: "+asynqLogMessage(args...),
		slog.String("event", "asynq.log.error"),
		slog.String("failureClass", "infrastructure"),
	)
}

func (l *asynqSlogLogger) Fatal(args ...interface{}) {
	// Asynq's default Fatal exits the process; keep the supervisor alive so the
	// structured failure lane can flush and the process manager can decide restart.
	l.logger.Error("Asynq: "+asynqLogMessage(args...),
		slog.String("event", "asynq.log.fatal"),
		slog.String("failureClass", "infrastructure"),
		slog.Bool("fatal", true),
	)
}

func asynqLogMessage(args ...interface{}) string {
	message := fmt.Sprint(args...)
	const maxBytes = 8 * 1024
	if len(message) <= maxBytes {
		return message
	}
	return message[:maxBytes] + " [truncated]"
}

func logAsynqTaskError(
	ctx context.Context,
	logger *slog.Logger,
	task *asynq.Task,
	err error,
	runtimeOverride *ingestTaskRuntime,
) {
	if logger == nil {
		logger = slog.Default()
	}
	var loggedErr *loggedIngestTaskError
	if errors.As(err, &loggedErr) {
		return
	}
	runtime := ingestTaskRuntimeFromContext(ctx)
	if runtimeOverride != nil {
		runtime = *runtimeOverride
	}
	taskType := "unknown"
	var payloadBytes int64
	if task != nil {
		taskType = task.Type()
		payloadBytes = int64(len(task.Payload()))
	}
	if runtime.queue == "" {
		runtime.queue = "unknown"
	}
	ctx = withIngestTaskLogContext(ctx, runtime, "", "")
	attrs := ingestTaskLogAttrs(taskType, runtime, "")
	expected := errors.Is(err, asynq.SkipRetry)
	failureClass := "unexpected"
	retryable := true
	if expected {
		failureClass = "expected"
		retryable = false
	}
	willRetry := retryable && (!runtime.known || runtime.retryCount < runtime.maxRetry)
	logger.ErrorContext(ctx, "Asynq 任务处理失败", append(attrs,
		slog.String("event", "asynq.task.handler.failed"),
		slog.String("failureClass", failureClass),
		slog.Bool("retryable", retryable),
		slog.Bool("willRetry", willRetry),
		slog.Int64("payloadBytes", payloadBytes),
		slog.Any("error", err),
	)...)
}

func handlePublicAPILogTask(ctx context.Context, store port.PublicAPILogStore, payload []byte) error {
	prepared, err := preparePublicAPILogTask(store, payload)
	if err != nil {
		if errors.Is(err, publicapilogjob.ErrInvalidPayload) {
			return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
		}
		return err
	}
	return prepared.write(ctx)
}

func handleOperationLogTask(ctx context.Context, store port.OperationLogStore, payload []byte) error {
	prepared, err := prepareOperationLogTask(store, payload)
	if err != nil {
		if errors.Is(err, operationlogjob.ErrInvalidPayload) {
			return fmt.Errorf("%w: %w", err, asynq.SkipRetry)
		}
		return err
	}
	return prepared.write(ctx)
}

func handlePublicAPILogTaskLogged(
	ctx context.Context,
	logger *slog.Logger,
	store port.PublicAPILogStore,
	task *asynq.Task,
	runtimeOverride *ingestTaskRuntime,
) error {
	return runLoggedIngestTask(
		ctx,
		logger,
		task,
		publicapilogjob.QueueName,
		publicapilogjob.ErrInvalidPayload,
		runtimeOverride,
		func(payload []byte) (preparedIngestTask, error) {
			return preparePublicAPILogTask(store, payload)
		},
	)
}

func handleOperationLogTaskLogged(
	ctx context.Context,
	logger *slog.Logger,
	store port.OperationLogStore,
	task *asynq.Task,
	runtimeOverride *ingestTaskRuntime,
) error {
	return runLoggedIngestTask(
		ctx,
		logger,
		task,
		operationlogjob.QueueName,
		operationlogjob.ErrInvalidPayload,
		runtimeOverride,
		func(payload []byte) (preparedIngestTask, error) {
			return prepareOperationLogTask(store, payload)
		},
	)
}

func runLoggedIngestTask(
	ctx context.Context,
	logger *slog.Logger,
	task *asynq.Task,
	defaultQueue string,
	invalidPayloadError error,
	runtimeOverride *ingestTaskRuntime,
	prepare func([]byte) (preparedIngestTask, error),
) error {
	if logger == nil {
		logger = slog.Default()
	}
	startedAt := time.Now()
	runtime := ingestTaskRuntimeFromContext(ctx)
	if runtimeOverride != nil {
		runtime = *runtimeOverride
	}
	if runtime.queue == "" {
		runtime.queue = defaultQueue
	}

	prepared, prepareErr := prepare(task.Payload())
	ctx = withIngestTaskLogContext(ctx, runtime, prepared.traceID, prepared.requestID)
	baseAttrs := ingestTaskLogAttrs(task.Type(), runtime, prepared.logRecordID)
	logger.InfoContext(ctx, "ingest worker 任务开始", append(baseAttrs,
		slog.String("event", "worker.ingest.task.started"),
		slog.String("outcome", "started"),
	)...)

	if prepareErr != nil {
		err := prepareErr
		expected := errors.Is(err, invalidPayloadError)
		if expected {
			err = fmt.Errorf("%w: %w", err, asynq.SkipRetry)
		}
		logIngestTaskFailure(ctx, logger, baseAttrs, runtime, startedAt, err, expected)
		return &loggedIngestTaskError{cause: err}
	}
	if err := prepared.write(ctx); err != nil {
		logIngestTaskFailure(ctx, logger, baseAttrs, runtime, startedAt, err, false)
		return &loggedIngestTaskError{cause: err}
	}
	logger.InfoContext(ctx, "ingest worker 任务完成", append(baseAttrs,
		slog.String("event", "worker.ingest.task.completed"),
		slog.String("outcome", "success"),
		slog.Int64("durationMs", time.Since(startedAt).Milliseconds()),
	)...)
	return nil
}

func logIngestTaskFailure(
	ctx context.Context,
	logger *slog.Logger,
	baseAttrs []any,
	runtime ingestTaskRuntime,
	startedAt time.Time,
	err error,
	expected bool,
) {
	failureClass := "unexpected"
	retryable := true
	if expected {
		failureClass = "expected"
		retryable = false
	}
	willRetry := retryable && (!runtime.known || runtime.retryCount < runtime.maxRetry)
	logger.ErrorContext(ctx, "ingest worker 任务失败", append(baseAttrs,
		slog.String("event", "worker.ingest.task.failed"),
		slog.String("outcome", "failure"),
		slog.String("failureClass", failureClass),
		slog.Bool("retryable", retryable),
		slog.Bool("willRetry", willRetry),
		slog.Int64("durationMs", time.Since(startedAt).Milliseconds()),
		slog.Any("error", err),
	)...)
}

func ingestTaskRuntimeFromContext(ctx context.Context) ingestTaskRuntime {
	taskID, _ := asynq.GetTaskID(ctx)
	queueName, _ := asynq.GetQueueName(ctx)
	retryCount, retryCountKnown := asynq.GetRetryCount(ctx)
	maxRetry, maxRetryKnown := asynq.GetMaxRetry(ctx)
	return ingestTaskRuntime{
		taskID:     taskID,
		queue:      queueName,
		retryCount: retryCount,
		maxRetry:   maxRetry,
		known:      retryCountKnown && maxRetryKnown,
	}
}

func withIngestTaskLogContext(
	ctx context.Context,
	runtime ingestTaskRuntime,
	traceID string,
	requestID string,
) context.Context {
	fields := logging.LogContextFrom(ctx)
	fields.JobID = runtime.taskID
	fields.TraceID = traceID
	fields.RequestID = requestID
	if requestID != "" {
		fields.ParentID = requestID
	}
	return logging.WithLogContext(ctx, fields)
}

func ingestTaskLogAttrs(taskType string, runtime ingestTaskRuntime, logRecordID string) []any {
	retryStatus := "unknown"
	if runtime.known {
		switch {
		case runtime.maxRetry == 0:
			retryStatus = "only_attempt"
		case runtime.retryCount == 0:
			retryStatus = "first_attempt"
		case runtime.retryCount >= runtime.maxRetry:
			retryStatus = "last_attempt"
		default:
			retryStatus = "retrying"
		}
	}
	attrs := []any{
		slog.String("taskType", taskType),
		slog.String("queue", runtime.queue),
		slog.Int("retryCount", runtime.retryCount),
		slog.Int("maxRetry", runtime.maxRetry),
		slog.String("retryStatus", retryStatus),
	}
	if logRecordID != "" {
		attrs = append(attrs, slog.String("logRecordId", logRecordID))
	}
	return attrs
}

// Decode the envelope once so correlation is available before the start log, then
// write the same validated input. Calling HandleWriteTask here would decode potentially
// large captured request/response data a second time.
func preparePublicAPILogTask(store port.PublicAPILogStore, payload []byte) (preparedIngestTask, error) {
	if store == nil {
		return preparedIngestTask{}, fmt.Errorf("public api log store is required")
	}
	envelope, err := publicapilogjob.DecodeWriteTaskEnvelope(payload)
	input := envelope.Log
	traceID := ""
	requestID := input.TraceID
	if envelope.Correlation != nil {
		traceID = envelope.Correlation.TraceID
		if envelope.Correlation.RequestID != "" {
			requestID = envelope.Correlation.RequestID
		}
	}
	prepared := preparedIngestTask{
		traceID:     traceID,
		requestID:   requestID,
		logRecordID: input.ID,
		write: func(ctx context.Context) error {
			if err := store.InsertPublicAPILog(ctx, input); err != nil {
				return fmt.Errorf("write public api log task: %w", err)
			}
			return nil
		},
	}
	if err != nil {
		return prepared, err
	}
	return prepared, nil
}

func prepareOperationLogTask(store port.OperationLogStore, payload []byte) (preparedIngestTask, error) {
	if store == nil {
		return preparedIngestTask{}, fmt.Errorf("operation log store is required")
	}
	envelope, err := operationlogjob.DecodeWriteTaskEnvelope(payload)
	input := envelope.Log
	traceID := ""
	requestID := input.TraceID
	if envelope.Correlation != nil {
		traceID = envelope.Correlation.TraceID
		if envelope.Correlation.RequestID != "" {
			requestID = envelope.Correlation.RequestID
		}
	}
	prepared := preparedIngestTask{
		traceID:     traceID,
		requestID:   requestID,
		logRecordID: input.ID,
		write: func(ctx context.Context) error {
			if err := store.InsertOperationLog(ctx, input); err != nil {
				return fmt.Errorf("write operation log task: %w", err)
			}
			return nil
		},
	}
	if err != nil {
		return prepared, err
	}
	return prepared, nil
}

func asynqRedisOptions(opts queue.RedisOptions) asynq.RedisClientOpt {
	return asynq.RedisClientOpt{
		Addr:         opts.Addr,
		Username:     opts.Username,
		Password:     opts.Password,
		DB:           opts.DB,
		TLSConfig:    opts.TLS,
		DialTimeout:  opts.DialTimeout,
		ReadTimeout:  opts.ReadTimeout,
		WriteTimeout: opts.WriteTimeout,
	}
}

func asynqLogLevel(value string) asynq.LogLevel {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return asynq.DebugLevel
	case "warn", "warning":
		return asynq.WarnLevel
	case "error":
		return asynq.ErrorLevel
	default:
		return asynq.InfoLevel
	}
}

func defaultDuration(value time.Duration, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func defaultInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
