package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/modelqualityhealthsync"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	defaultModelQualityHealthSyncInterval       = time.Minute
	defaultModelQualityHealthSyncInitialDelay   = 58 * time.Second
	defaultModelQualityHealthSyncBatchSize      = modelqualityhealthsync.DefaultClaimLimit
	defaultModelQualityHealthSyncWorkers        = modelqualityhealthsync.DefaultWorkerCount
	defaultModelQualityHealthSyncLease          = modelqualityhealthsync.DefaultLeaseDuration
	defaultModelQualityHealthSyncAttemptTimeout = modelqualityhealthsync.DefaultCompleteTimeout
	modelQualityHealthSyncPingTimeout           = 5 * time.Second
)

type ModelQualityHealthSyncWorkerOptions struct {
	Enabled             bool
	GoExclusiveOwner    bool
	LegacyWorkerDrained bool
	NodeRetentionSafe   bool
	OwnerID             string
	Interval            time.Duration
	InitialDelay        time.Duration
	BatchSize           int
	Workers             int
	Lease               time.Duration
	AttemptTimeout      time.Duration
	RunOnce             bool
}

type modelQualityHealthSyncWorkerStore interface {
	port.ModelQualityHealthSyncClaimer
	port.ModelQualityHealthSyncCompleter
	port.ModelQualityHealthSyncReleaser
	Ping(context.Context) error
	Close()
}

type modelQualityHealthSyncRunner interface {
	RunOnce(context.Context, modelqualityhealthsync.RunOnceInput) (modelqualityhealthsync.RunOnceResult, error)
}

type modelQualityHealthSyncWorkerDependencies struct {
	openStore      func(context.Context, string) (modelQualityHealthSyncWorkerStore, error)
	newService     func(modelQualityHealthSyncWorkerStore) (modelQualityHealthSyncRunner, error)
	processOwnerID func() (string, error)
}

var modelQualityHealthSyncProcessOwnerID = sync.OnceValues(generateModelQualityHealthSyncOwnerID)

func defaultModelQualityHealthSyncWorkerDependencies() modelQualityHealthSyncWorkerDependencies {
	return modelQualityHealthSyncWorkerDependencies{
		openStore: func(ctx context.Context, rawURL string) (modelQualityHealthSyncWorkerStore, error) {
			return postgresstore.Open(ctx, rawURL)
		},
		newService: func(store modelQualityHealthSyncWorkerStore) (modelQualityHealthSyncRunner, error) {
			return modelqualityhealthsync.NewService(store, store, store)
		},
		processOwnerID: modelQualityHealthSyncProcessOwnerID,
	}
}

func RunModelQualityHealthSyncWorker(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	opts ModelQualityHealthSyncWorkerOptions,
) error {
	return runModelQualityHealthSyncWorker(ctx, cfg, logger, opts, defaultModelQualityHealthSyncWorkerDependencies())
}

func runModelQualityHealthSyncWorker(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	opts ModelQualityHealthSyncWorkerOptions,
	deps modelQualityHealthSyncWorkerDependencies,
) error {
	if ctx == nil {
		return fmt.Errorf("model-quality health-sync worker context is required")
	}
	if !opts.Enabled {
		return nil
	}
	if !cfg.OwnerLockEnabled || strings.TrimSpace(cfg.OwnerLockRole) != "worker" {
		return fmt.Errorf("Go model-quality health-sync worker must run with the worker owner lock")
	}
	if !opts.GoExclusiveOwner {
		return fmt.Errorf("Go model-quality health-sync worker requires exclusive ownership")
	}
	if !opts.LegacyWorkerDrained {
		return fmt.Errorf("Go model-quality health-sync worker requires the legacy worker to be drained")
	}
	if !opts.NodeRetentionSafe {
		return fmt.Errorf("Go model-quality health-sync worker requires Node retention to preserve pending and claimed runs")
	}
	if strings.TrimSpace(cfg.PostgresURL) == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL is required")
	}
	if deps.openStore == nil || deps.newService == nil || deps.processOwnerID == nil {
		return fmt.Errorf("model-quality health-sync worker dependencies are required")
	}

	input, interval, initialDelay, err := normalizeModelQualityHealthSyncWorkerOptions(opts, deps.processOwnerID)
	if err != nil {
		return err
	}
	if logger == nil {
		logger = slog.Default()
	}

	store, err := deps.openStore(ctx, cfg.PostgresURL)
	if err != nil {
		return fmt.Errorf("open model-quality health-sync PostgreSQL: %w", err)
	}
	defer store.Close()

	pingCtx, cancelPing := context.WithTimeout(ctx, modelQualityHealthSyncPingTimeout)
	err = store.Ping(pingCtx)
	cancelPing()
	if err != nil {
		return fmt.Errorf("ping model-quality health-sync PostgreSQL: %w", err)
	}

	service, err := deps.newService(store)
	if err != nil {
		return fmt.Errorf("create model-quality health-sync service: %w", err)
	}
	if service == nil {
		return fmt.Errorf("model-quality health-sync service is required")
	}

	runBatch := func(runCtx context.Context) error {
		startedAt := time.Now()
		result, runErr := service.RunOnce(runCtx, input)
		attributes := []any{
			slog.String("event", "model_quality_health_sync_batch"),
			slog.String("owner_id", string(input.OwnerID)),
			slog.Int("claimed", result.Claimed),
			slog.Int("quarantined", result.Quarantined),
			slog.Int("completed", result.Completed),
			slog.Int("stale", result.Stale),
			slog.Int("released", result.Released),
			slog.Int("release_stale", result.ReleaseStale),
			slog.Int("failed", result.Failed),
			slog.Int("release_failed", result.ReleaseFailed),
			slog.Duration("duration", time.Since(startedAt)),
		}
		if runErr != nil {
			attributes = append(attributes, slog.Any("error", runErr))
			logger.Error("Go model-quality health-sync batch failed", attributes...)
			return runErr
		}
		logger.Info("Go model-quality health-sync batch completed", attributes...)
		return nil
	}

	return runModelQualityHealthSyncLoop(ctx, initialDelay, interval, opts.RunOnce, runBatch)
}

func normalizeModelQualityHealthSyncWorkerOptions(
	opts ModelQualityHealthSyncWorkerOptions,
	processOwnerID func() (string, error),
) (modelqualityhealthsync.RunOnceInput, time.Duration, time.Duration, error) {
	interval := opts.Interval
	if interval == 0 {
		interval = defaultModelQualityHealthSyncInterval
	}
	if interval <= 0 {
		return modelqualityhealthsync.RunOnceInput{}, 0, 0, fmt.Errorf("model-quality health-sync interval must be positive")
	}
	initialDelay := opts.InitialDelay
	if initialDelay == 0 {
		initialDelay = defaultModelQualityHealthSyncInitialDelay
	}
	if initialDelay < 0 {
		return modelqualityhealthsync.RunOnceInput{}, 0, 0, fmt.Errorf("model-quality health-sync initial delay cannot be negative")
	}

	ownerID := opts.OwnerID
	if ownerID == "" {
		generated, err := processOwnerID()
		if err != nil {
			return modelqualityhealthsync.RunOnceInput{}, 0, 0, fmt.Errorf("generate model-quality health-sync owner ID: %w", err)
		}
		ownerID = generated
	}
	batchSize := opts.BatchSize
	if batchSize == 0 {
		batchSize = defaultModelQualityHealthSyncBatchSize
	}
	workers := opts.Workers
	if workers == 0 {
		workers = defaultModelQualityHealthSyncWorkers
	}
	lease := opts.Lease
	if lease == 0 {
		lease = defaultModelQualityHealthSyncLease
	}
	attemptTimeout := opts.AttemptTimeout
	if attemptTimeout == 0 {
		attemptTimeout = defaultModelQualityHealthSyncAttemptTimeout
	}
	input := modelqualityhealthsync.RunOnceInput{
		OwnerID:         port.ModelQualityClaimOwnerID(ownerID),
		ClaimLimit:      batchSize,
		LeaseDuration:   lease,
		WorkerCount:     workers,
		ClaimTimeout:    attemptTimeout,
		CompleteTimeout: attemptTimeout,
	}
	if err := modelqualityhealthsync.ValidateRunOnceInput(input); err != nil {
		return modelqualityhealthsync.RunOnceInput{}, 0, 0, err
	}
	return input, interval, initialDelay, nil
}

func runModelQualityHealthSyncLoop(
	ctx context.Context,
	initialDelay time.Duration,
	interval time.Duration,
	runOnce bool,
	run func(context.Context) error,
) error {
	if runOnce {
		return run(ctx)
	}
	if err := waitModelQualityHealthSync(ctx, initialDelay); err != nil {
		return nil
	}
	for {
		if err := run(ctx); err != nil && ctx.Err() != nil {
			return nil
		}
		if err := waitModelQualityHealthSync(ctx, interval); err != nil {
			return nil
		}
	}
}

func waitModelQualityHealthSync(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func generateModelQualityHealthSyncOwnerID() (string, error) {
	var nonce [16]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("go-model-quality-health-sync:%d:%x", os.Getpid(), nonce), nil
}
