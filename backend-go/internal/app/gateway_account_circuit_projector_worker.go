package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/gatewaycircuitoutbox"
	"juhe-ai/backend-go/internal/modules/gatewaycircuitprojection"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const defaultGatewayAccountCircuitProjectorInterval = time.Second

type GatewayAccountCircuitProjectorWorkerOptions struct {
	Enabled                    bool
	GoExclusiveProjectionOwner bool
	OwnerID                    string
	BatchSize                  int
	Lease                      time.Duration
	RetryDelay                 time.Duration
	Interval                   time.Duration
	InitialDelay               time.Duration
	ClosedRetention            time.Duration
	RuntimeCapacity            int
	RebuildPageSize            int
	RebuildMaxPages            int
	RunOnce                    bool
}

func RunGatewayAccountCircuitProjectorWorker(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	opts GatewayAccountCircuitProjectorWorkerOptions,
) error {
	if !opts.Enabled {
		return nil
	}
	if !opts.GoExclusiveProjectionOwner {
		return fmt.Errorf("Go account circuit projector requires exclusive projection ownership")
	}
	if strings.TrimSpace(cfg.PostgresURL) == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL is required")
	}
	if strings.TrimSpace(cfg.RedisStateURL) == "" {
		return fmt.Errorf("JUHE_AI_REDIS_STATE_URL is required")
	}
	input, interval, err := normalizeGatewayAccountCircuitProjectorWorkerOptions(opts)
	if err != nil {
		return err
	}
	if logger == nil {
		logger = slog.Default()
	}

	store, err := postgresstore.Open(ctx, cfg.PostgresURL)
	if err != nil {
		return fmt.Errorf("open account circuit outbox PostgreSQL: %w", err)
	}
	defer store.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = store.Ping(pingCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("ping account circuit outbox PostgreSQL: %w", err)
	}

	// The Node compatibility keys use the base namespace, without the Go :state suffix.
	redisClient, err := redisplatform.NewClient(cfg.RedisStateURL, cfg.RedisNamespace)
	if err != nil {
		return fmt.Errorf("open account circuit projector Redis: %w", err)
	}
	defer func() { _ = redisClient.Close() }()
	pingCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
	err = redisClient.Ping(pingCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("ping account circuit projector Redis: %w", err)
	}

	projector, err := redisplatform.NewAccountCircuitRevisionProjector(redisClient, opts.ClosedRetention)
	if err != nil {
		return err
	}
	incidentRestorer, err := redisplatform.NewAccountCircuitIncidentRestorer(redisClient, opts.ClosedRetention, opts.RuntimeCapacity)
	if err != nil {
		return err
	}
	incidentProjector, err := gatewaycircuitprojection.NewIncidentProjector(store, incidentRestorer)
	if err != nil {
		return err
	}
	rebuild, err := incidentProjector.Rebuild(ctx, gatewaycircuitprojection.RebuildInput{
		PageSize: opts.RebuildPageSize,
		MaxPages: opts.RebuildMaxPages,
	})
	if err != nil {
		return fmt.Errorf("rebuild gateway account circuit incidents: %w", err)
	}
	logger.Info("Go account circuit incident rebuild completed",
		slog.Int("loaded", rebuild.Loaded),
		slog.Int("pages", rebuild.Pages),
	)
	service, err := gatewaycircuitoutbox.NewService(store, projector)
	if err != nil {
		return err
	}
	service.WithIncidentProjector(incidentProjector)
	run := func() error {
		result, err := service.RunOnce(ctx, input)
		if err != nil {
			return err
		}
		if result.Claimed > 0 {
			logger.Info("Go account circuit outbox projection batch completed",
				slog.Int("claimed", result.Claimed),
				slog.Int("acknowledged", result.Acknowledged),
				slog.Int("failed", result.Failed),
			)
		}
		return nil
	}
	return runGatewayAccountCircuitProjectorLoop(ctx, logger, opts.InitialDelay, interval, opts.RunOnce, run)
}

func normalizeGatewayAccountCircuitProjectorWorkerOptions(opts GatewayAccountCircuitProjectorWorkerOptions) (gatewaycircuitoutbox.RunOnceInput, time.Duration, error) {
	ownerID := strings.TrimSpace(opts.OwnerID)
	if ownerID == "" {
		ownerID = fmt.Sprintf("go-account-circuit-projector:%d", os.Getpid())
	}
	interval := opts.Interval
	if interval == 0 {
		interval = defaultGatewayAccountCircuitProjectorInterval
	}
	if interval <= 0 {
		return gatewaycircuitoutbox.RunOnceInput{}, 0, fmt.Errorf("account circuit projector interval must be positive")
	}
	if opts.InitialDelay < 0 {
		return gatewaycircuitoutbox.RunOnceInput{}, 0, fmt.Errorf("account circuit projector initial delay cannot be negative")
	}
	input := gatewaycircuitoutbox.RunOnceInput{
		OwnerID:    ownerID,
		Lease:      opts.Lease,
		RetryDelay: opts.RetryDelay,
		Limit:      opts.BatchSize,
	}
	if err := gatewaycircuitoutbox.ValidateRunOnceInput(input); err != nil {
		return gatewaycircuitoutbox.RunOnceInput{}, 0, err
	}
	if opts.ClosedRetention < 0 || opts.ClosedRetention > 24*time.Hour {
		return gatewaycircuitoutbox.RunOnceInput{}, 0, fmt.Errorf("account circuit closed retention is invalid")
	}
	if opts.RuntimeCapacity < 0 || opts.RuntimeCapacity > 1000000 {
		return gatewaycircuitoutbox.RunOnceInput{}, 0, fmt.Errorf("account circuit runtime capacity is invalid")
	}
	if opts.RebuildPageSize < 0 || opts.RebuildPageSize > port.GatewayAccountCircuitIncidentMaxPage {
		return gatewaycircuitoutbox.RunOnceInput{}, 0, fmt.Errorf("account circuit rebuild page size is invalid")
	}
	if opts.RebuildMaxPages < 0 || opts.RebuildMaxPages > gatewaycircuitprojection.DefaultRebuildMaxPages {
		return gatewaycircuitoutbox.RunOnceInput{}, 0, fmt.Errorf("account circuit rebuild max pages is invalid")
	}
	return input, interval, nil
}

func runGatewayAccountCircuitProjectorLoop(ctx context.Context, logger *slog.Logger, initialDelay, interval time.Duration, runOnce bool, run func() error) error {
	if runOnce {
		return run()
	}
	if err := waitGatewayAccountCircuitProjector(ctx, initialDelay); err != nil {
		return nil
	}
	for {
		if err := run(); err != nil {
			logger.Error("Go account circuit outbox projection batch failed", slog.Any("error", err))
		}
		if err := waitGatewayAccountCircuitProjector(ctx, interval); err != nil {
			return nil
		}
	}
}

func waitGatewayAccountCircuitProjector(ctx context.Context, duration time.Duration) error {
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
