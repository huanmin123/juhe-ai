package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/config"
	job "juhe-ai/backend-go/internal/jobs/cooldownaccountretest"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/jobs/worker"
	module "juhe-ai/backend-go/internal/modules/cooldownaccountretest"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
	"juhe-ai/backend-go/internal/systemsettings"
)

const (
	defaultCooldownAccountRetestInitialDelay = 60 * time.Second
	defaultCooldownAccountRetestInterval     = 3 * time.Second
)

type CooldownAccountRetestWorkerOptions struct {
	InitialDelay time.Duration
	Probe        module.Probe
	Outcomes     port.CooldownAccountRetestOutcomeStore
}

type CooldownAccountRetestScheduleSettings struct {
	Interval         time.Duration
	BatchSize        int
	MaxPauseMinutes  int
	MaxRecoveryHours int
}

type cooldownAccountRetestSettingsReader interface {
	CooldownAccountRetestScheduleSettings(context.Context) (CooldownAccountRetestScheduleSettings, error)
}

type cooldownAccountRetestRuntimeStore interface {
	port.CooldownAccountRetestStore
	port.CooldownAccountRetestQuotaSubjectReader
	port.GatewayQuotaCostReader
	port.ManagementUsageStatsTimezoneReader
	port.ManagementSystemSettingsReader
	Ping(context.Context) error
	Close()
}

type cooldownAccountRetestQueueClient interface {
	job.EnqueueClient
	Ping() error
	Close() error
}

type cooldownAccountRetestQueueInspector interface {
	QueueInfo(string) (queue.QueueInfo, error)
	Close() error
}

type cooldownAccountRetestWorkerDependencies struct {
	openStore         func(context.Context, string) (cooldownAccountRetestRuntimeStore, error)
	newQueueClient    func(queue.RedisOptions) cooldownAccountRetestQueueClient
	newQueueInspector func(queue.RedisOptions) cooldownAccountRetestQueueInspector
	runConsumer       func(context.Context, worker.CooldownAccountRetestConsumerOptions) error
}

func defaultCooldownAccountRetestWorkerDependencies() cooldownAccountRetestWorkerDependencies {
	return cooldownAccountRetestWorkerDependencies{
		openStore: func(ctx context.Context, rawURL string) (cooldownAccountRetestRuntimeStore, error) {
			return postgresstore.Open(ctx, rawURL)
		},
		newQueueClient: func(opts queue.RedisOptions) cooldownAccountRetestQueueClient {
			return queue.NewClient(opts)
		},
		newQueueInspector: func(opts queue.RedisOptions) cooldownAccountRetestQueueInspector {
			return queue.NewInspector(opts)
		},
		runConsumer: worker.RunCooldownAccountRetestConsumer,
	}
}

func RunCooldownAccountRetestWorker(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	opts CooldownAccountRetestWorkerOptions,
) error {
	return runCooldownAccountRetestWorker(ctx, cfg, logger, opts, defaultCooldownAccountRetestWorkerDependencies())
}

func runCooldownAccountRetestWorker(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	opts CooldownAccountRetestWorkerOptions,
	deps cooldownAccountRetestWorkerDependencies,
) error {
	if !cfg.CooldownAccountRetestWorkerEnabled {
		return fmt.Errorf("Go 账户级冷却复测 worker 默认关闭；当前 Node worker 继续持有 owner")
	}
	if !cfg.OwnerLockEnabled || strings.TrimSpace(cfg.OwnerLockRole) != "worker" {
		return fmt.Errorf("Go 账户级冷却复测 worker 必须通过 worker owner lock 启动")
	}
	if opts.Probe == nil {
		return module.ErrProbeNotConfigured
	}
	if opts.Outcomes == nil {
		return fmt.Errorf("cooldown account retest outcome store is not configured")
	}
	if strings.TrimSpace(cfg.PostgresURL) == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL 不能为空")
	}
	if strings.TrimSpace(cfg.RedisQueueURL) == "" {
		return fmt.Errorf("JUHE_AI_REDIS_QUEUE_URL 不能为空")
	}
	if deps.openStore == nil || deps.newQueueClient == nil || deps.newQueueInspector == nil || deps.runConsumer == nil {
		return fmt.Errorf("cooldown account retest runtime dependencies are required")
	}
	initialDelay := opts.InitialDelay
	if initialDelay == 0 {
		initialDelay = defaultCooldownAccountRetestInitialDelay
	}
	if initialDelay < 0 {
		return fmt.Errorf("冷却复测初始延迟不能小于 0")
	}
	if logger == nil {
		logger = slog.Default()
	}

	store, err := deps.openStore(ctx, cfg.PostgresURL)
	if err != nil {
		return fmt.Errorf("open cooldown account retest PostgreSQL: %w", err)
	}
	defer store.Close()
	pingCtx, cancelPing := context.WithTimeout(ctx, 5*time.Second)
	err = store.Ping(pingCtx)
	cancelPing()
	if err != nil {
		return fmt.Errorf("ping cooldown account retest PostgreSQL: %w", err)
	}

	redisOptions, err := queue.ParseRedisURL(cfg.RedisQueueURL)
	if err != nil {
		return fmt.Errorf("JUHE_AI_REDIS_QUEUE_URL 无效: %w", err)
	}
	queueClient := deps.newQueueClient(redisOptions)
	if queueClient == nil {
		return fmt.Errorf("cooldown account retest queue client is required")
	}
	defer func() { _ = queueClient.Close() }()
	if err := queueClient.Ping(); err != nil {
		return fmt.Errorf("ping cooldown account retest queue Redis: %w", err)
	}
	inspector := deps.newQueueInspector(redisOptions)
	if inspector == nil {
		return fmt.Errorf("cooldown account retest queue inspector is required")
	}
	defer func() { _ = inspector.Close() }()

	scheduler := newCooldownAccountRetestSchedulerRunner(
		store,
		postgresCooldownAccountRetestSettingsReader{store: store},
		job.Enqueuer{Client: queueClient},
		cooldownAccountRetestQueueCapacity{inspector: inspector},
	)
	consumerOptions := worker.CooldownAccountRetestConsumerOptions{
		Redis: redisOptions,
		Processor: module.Processor{
			Store: store, Outcomes: opts.Outcomes, Probe: opts.Probe,
			Quota: module.QuotaEligibility{Subjects: store, Costs: store, Timezones: store},
		},
		ShutdownTimeout: cfg.ShutdownTimeout,
		LogLevel:        cfg.LogLevel,
		Concurrency:     worker.DefaultCooldownAccountRetestConcurrency,
	}
	return runCooldownAccountRetestRuntime(ctx, logger, initialDelay, scheduler, func(runCtx context.Context) error {
		return deps.runConsumer(runCtx, consumerOptions)
	})
}

type cooldownAccountRetestSchedulerRunner struct {
	store    port.CooldownAccountRetestStore
	settings cooldownAccountRetestSettingsReader
	enqueuer module.Enqueuer
	capacity module.QueueCapacity
	quota    module.QuotaChecker
	cursor   *port.CooldownAccountRetestCursor
}

type cooldownAccountRetestScheduleRun struct {
	module.ScheduleResult
	Interval time.Duration
}

type cooldownAccountRetestConsumerRun struct {
	done chan struct{}
	err  error
}

func newCooldownAccountRetestSchedulerRunner(
	store cooldownAccountRetestRuntimeStore,
	settings cooldownAccountRetestSettingsReader,
	enqueuer module.Enqueuer,
	capacity module.QueueCapacity,
) *cooldownAccountRetestSchedulerRunner {
	return &cooldownAccountRetestSchedulerRunner{
		store: store, settings: settings, enqueuer: enqueuer, capacity: capacity,
		quota: module.QuotaEligibility{Subjects: store, Costs: store, Timezones: store},
	}
}

func (r *cooldownAccountRetestSchedulerRunner) RunPage(ctx context.Context, now time.Time) (cooldownAccountRetestScheduleRun, error) {
	if r == nil || r.store == nil || r.settings == nil || r.enqueuer == nil {
		return cooldownAccountRetestScheduleRun{}, fmt.Errorf("cooldown account retest scheduler dependencies are required")
	}
	settings, err := r.settings.CooldownAccountRetestScheduleSettings(ctx)
	if err != nil {
		return cooldownAccountRetestScheduleRun{}, err
	}
	scheduler := module.Scheduler{
		Store: r.store, Enqueuer: r.enqueuer, Capacity: r.capacity, Quota: r.quota,
		BatchSize: settings.BatchSize, EnqueueWorkers: module.DefaultEnqueueWorkers,
		MaxPauseMinutes: settings.MaxPauseMinutes, MaxRecoveryHours: settings.MaxRecoveryHours,
	}
	result, next, err := scheduler.RunPage(ctx, r.cursor, now)
	if err != nil {
		return cooldownAccountRetestScheduleRun{}, err
	}
	r.cursor = next
	return cooldownAccountRetestScheduleRun{ScheduleResult: result, Interval: settings.Interval}, nil
}

func (r *cooldownAccountRetestSchedulerRunner) Cursor() *port.CooldownAccountRetestCursor {
	if r == nil {
		return nil
	}
	return r.cursor
}

func runCooldownAccountRetestRuntime(
	ctx context.Context,
	logger *slog.Logger,
	initialDelay time.Duration,
	scheduler *cooldownAccountRetestSchedulerRunner,
	runConsumer func(context.Context) error,
) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	consumer := &cooldownAccountRetestConsumerRun{done: make(chan struct{})}
	go func() {
		consumer.err = runConsumer(runCtx)
		close(consumer.done)
	}()
	if err := waitCooldownAccountRetestRuntime(runCtx, consumer, initialDelay); err != nil {
		cancel()
		<-consumer.done
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	for {
		run, err := scheduler.RunPage(runCtx, time.Now().UTC())
		interval := defaultCooldownAccountRetestInterval
		if err != nil {
			logger.Error("Go 账户级冷却复测调度失败", slog.Any("error", err))
		} else {
			interval = run.Interval
			logger.Info("Go 账户级冷却复测调度完成",
				slog.Int("candidateCount", run.CandidateCount),
				slog.Int("enqueuedCount", run.EnqueuedCount),
				slog.Int("duplicateCount", run.DuplicateCount),
				slog.Int("invalidCandidateCount", run.InvalidCandidateCount),
				slog.Int("quotaRejectedCount", run.QuotaRejectedCount),
				slog.Int("availableSlots", run.AvailableSlots),
			)
		}
		if interval <= 0 {
			interval = defaultCooldownAccountRetestInterval
		}
		if err := waitCooldownAccountRetestRuntime(runCtx, consumer, interval); err != nil {
			cancel()
			<-consumer.done
			if errors.Is(err, context.Canceled) && ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func waitCooldownAccountRetestRuntime(ctx context.Context, consumer *cooldownAccountRetestConsumerRun, duration time.Duration) error {
	if duration <= 0 {
		select {
		case <-consumer.done:
			return cooldownAccountRetestConsumerResult(ctx, consumer.err)
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-consumer.done:
		return cooldownAccountRetestConsumerResult(ctx, consumer.err)
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func cooldownAccountRetestConsumerResult(ctx context.Context, err error) error {
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("cooldown account retest consumer stopped unexpectedly")
}

type postgresCooldownAccountRetestSettingsReader struct {
	store port.ManagementSystemSettingsReader
}

func (r postgresCooldownAccountRetestSettingsReader) CooldownAccountRetestScheduleSettings(ctx context.Context) (CooldownAccountRetestScheduleSettings, error) {
	snapshot, err := r.store.ManagementSystemSettings(ctx)
	if err != nil {
		return CooldownAccountRetestScheduleSettings{}, fmt.Errorf("read cooldown account retest settings: %w", err)
	}
	intervalSeconds, err := systemSettingInteger(snapshot, "cooldownAccountRetestIntervalSeconds")
	if err != nil {
		return CooldownAccountRetestScheduleSettings{}, err
	}
	batchSize, err := systemSettingInteger(snapshot, "cooldownAccountRetestBatchSize")
	if err != nil {
		return CooldownAccountRetestScheduleSettings{}, err
	}
	maxPauseMinutes, err := systemSettingInteger(snapshot, "defaultTemporaryUnschedulableMinutes")
	if err != nil {
		return CooldownAccountRetestScheduleSettings{}, err
	}
	maxRecoveryHours, err := systemSettingInteger(snapshot, "cooldownAccountRetestMaxBackoffHours")
	if err != nil {
		return CooldownAccountRetestScheduleSettings{}, err
	}
	return CooldownAccountRetestScheduleSettings{
		Interval: time.Duration(intervalSeconds) * time.Second, BatchSize: batchSize,
		MaxPauseMinutes: maxPauseMinutes, MaxRecoveryHours: maxRecoveryHours,
	}, nil
}

func systemSettingInteger(snapshot systemsettings.Snapshot, key string) (int, error) {
	raw, ok := snapshot.Value(key)
	if !ok {
		return 0, fmt.Errorf("系统设置缺少 %s", key)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("系统设置 %s 不是整数: %w", key, err)
	}
	definition, ok := systemsettings.DefinitionFor(key)
	if !ok || value < definition.Minimum || value > definition.Maximum {
		return 0, fmt.Errorf("系统设置 %s 超出允许范围: %s", key, strconv.Itoa(value))
	}
	return value, nil
}

type cooldownAccountRetestQueueCapacity struct {
	inspector cooldownAccountRetestQueueInspector
}

func (c cooldownAccountRetestQueueCapacity) CooldownAccountRetestQueueSnapshot(ctx context.Context) (module.QueueSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return module.QueueSnapshot{}, err
	}
	info, err := c.inspector.QueueInfo(job.QueueName)
	if err != nil {
		return module.QueueSnapshot{}, err
	}
	return module.QueueSnapshot{PendingCount: info.Pending, RunningCount: info.Active, RetryCount: info.Retry}, nil
}
