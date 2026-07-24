package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/config"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

// GatewayAccountCircuitRuntimeIndexBackfillWorkerOptions intentionally carries
// explicit human gates. The Redis lock only prevents another Go maintenance
// task from racing; it cannot stop old Node writers, so a caller must prove both
// exclusive Go runtime ownership and that every legacy writer has drained.
type GatewayAccountCircuitRuntimeIndexBackfillWorkerOptions struct {
	Enabled                      bool
	GoExclusiveRuntimeStateOwner bool
	LegacyRuntimeWritersDrained  bool
	RuntimeStateWritesPaused     bool
	ControlPlaneWritesPaused     bool
	OwnerID                      string
	LockTTL                      time.Duration
	ScanCount                    int
	MaxPages                     int
	MaxFields                    int
	MaxBytes                     int
	MaxScopeMembers              int
}

func RunGatewayAccountCircuitRuntimeIndexBackfillWorker(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	opts GatewayAccountCircuitRuntimeIndexBackfillWorkerOptions,
) error {
	if !opts.Enabled {
		return nil
	}
	if !opts.GoExclusiveRuntimeStateOwner {
		return fmt.Errorf("Go account circuit runtime index backfill requires exclusive runtime-state ownership")
	}
	if !opts.LegacyRuntimeWritersDrained {
		return fmt.Errorf("Go account circuit runtime index backfill requires drained legacy runtime writers")
	}
	if !opts.RuntimeStateWritesPaused {
		return fmt.Errorf("Go account circuit runtime index backfill requires paused Go runtime-state writes")
	}
	if !opts.ControlPlaneWritesPaused {
		return fmt.Errorf("Go account circuit runtime index backfill requires paused account control-plane writes")
	}
	if ctx == nil {
		return fmt.Errorf("account circuit runtime index backfill context is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	ownerID := strings.TrimSpace(opts.OwnerID)
	if ownerID == "" {
		ownerID = fmt.Sprintf("go-account-circuit-runtime-index:%d", os.Getpid())
	}
	input, err := normalizeGatewayAccountCircuitRuntimeIndexBackfillWorkerInput(ownerID, opts)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.PostgresURL) == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL is required")
	}
	if strings.TrimSpace(cfg.RedisStateURL) == "" {
		return fmt.Errorf("JUHE_AI_REDIS_STATE_URL is required")
	}
	store, err := postgresstore.Open(ctx, cfg.PostgresURL)
	if err != nil {
		return fmt.Errorf("open account circuit dispatch revision PostgreSQL: %w", err)
	}
	defer store.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = store.Ping(pingCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("ping account circuit dispatch revision PostgreSQL: %w", err)
	}
	client, err := redisplatform.NewClient(cfg.RedisStateURL, cfg.RedisNamespace)
	if err != nil {
		return fmt.Errorf("open account circuit runtime index Redis: %w", err)
	}
	defer func() { _ = client.Close() }()
	pingCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
	err = client.Ping(pingCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("ping account circuit runtime index Redis: %w", err)
	}
	backfiller, err := redisplatform.NewAccountCircuitRuntimeIndexBackfiller(client)
	if err != nil {
		return err
	}
	result, err := backfiller.WithDispatchRevisionReader(store).BackfillGatewayAccountCircuitRuntimeIndex(ctx, input)
	if err != nil {
		return err
	}
	logger.Info("Go account circuit runtime index published ready",
		slog.String("epoch", result.Epoch),
		slog.Int("states", result.StateCount),
		slog.Int("evidence", result.EvidenceCount),
		slog.Int("revisions", result.RevisionCount),
		slog.Int("pages", result.Pages),
	)
	return nil
}

func normalizeGatewayAccountCircuitRuntimeIndexBackfillWorkerInput(ownerID string, opts GatewayAccountCircuitRuntimeIndexBackfillWorkerOptions) (redisplatform.GatewayAccountCircuitRuntimeIndexBackfillInput, error) {
	input := redisplatform.GatewayAccountCircuitRuntimeIndexBackfillInput{
		OwnerID: ownerID, LockTTL: opts.LockTTL, ScanCount: opts.ScanCount,
		MaxPages: opts.MaxPages, MaxFields: opts.MaxFields, MaxBytes: opts.MaxBytes, MaxScopeMembers: opts.MaxScopeMembers,
	}
	// The platform implementation owns detailed numerical bounds. Keeping the
	// app seam as a single validation call prevents a worker from opening Redis
	// and only then discovering an invalid run-once configuration.
	if err := redisplatform.ValidateGatewayAccountCircuitRuntimeIndexBackfillInput(input); err != nil {
		return redisplatform.GatewayAccountCircuitRuntimeIndexBackfillInput{}, err
	}
	return input, nil
}
