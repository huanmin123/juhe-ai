package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/managementauthorizations"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	defaultAuthorizationExpirySweepInterval     = time.Minute
	defaultAuthorizationExpirySweepInitialDelay = 54 * time.Second
)

type AuthorizationExpirySweepWorkerOptions struct {
	Limit        int
	Interval     time.Duration
	InitialDelay time.Duration
	RunOnce      bool
}

func RunAuthorizationExpirySweepWorker(ctx context.Context, cfg config.Config, logger *slog.Logger, opts AuthorizationExpirySweepWorkerOptions) error {
	if cfg.PostgresURL == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL 不能为空")
	}
	if cfg.RedisStateURL == "" {
		return fmt.Errorf("JUHE_AI_REDIS_STATE_URL 不能为空")
	}
	if cfg.RedisCacheURL == "" {
		return fmt.Errorf("JUHE_AI_REDIS_CACHE_URL 不能为空")
	}
	interval := opts.Interval
	if interval == 0 {
		interval = defaultAuthorizationExpirySweepInterval
	}
	if interval <= 0 {
		return fmt.Errorf("授权到期扫描间隔必须大于 0")
	}
	initialDelay := opts.InitialDelay
	if initialDelay < 0 {
		return fmt.Errorf("授权到期扫描初始延迟不能小于 0")
	}
	if logger == nil {
		logger = slog.Default()
	}

	store, err := postgresstore.Open(ctx, cfg.PostgresURL)
	if err != nil {
		return err
	}
	defer store.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := store.Ping(pingCtx); err != nil {
		cancel()
		return err
	}
	cancel()

	stateRedis, err := redisplatform.NewClient(cfg.RedisStateURL, cfg.RedisNamespace+":state")
	if err != nil {
		return fmt.Errorf("JUHE_AI_REDIS_STATE_URL 无效: %w", err)
	}
	defer func() { _ = stateRedis.Close() }()
	statePingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := stateRedis.Ping(statePingCtx); err != nil {
		cancel()
		return err
	}
	cancel()

	cacheRedis, err := redisplatform.NewClient(cfg.RedisCacheURL, cfg.RedisNamespace+":cache")
	if err != nil {
		return fmt.Errorf("JUHE_AI_REDIS_CACHE_URL 无效: %w", err)
	}
	defer func() { _ = cacheRedis.Close() }()
	cachePingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := cacheRedis.Ping(cachePingCtx); err != nil {
		cancel()
		return err
	}
	cancel()

	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		Cache:     cacheRedis,
		State:     stateRedis,
		Namespace: cfg.RedisNamespace,
	})
	if err != nil {
		return err
	}
	publisher, closePublisher, err := newRecoveringAccountsStaticResetPublisher(
		ctx, stateRedis, cacheRedis, cfg.RedisNamespace, store, logger,
	)
	if err != nil {
		return err
	}
	defer closePublisher()
	service := newAuthorizationExpirySweepService(store, invalidator, publisher, store, logger)
	sweep := func() error {
		result, err := service.ExpireDue(ctx, managementauthorizations.ExpirySweepInput{Limit: opts.Limit})
		if err != nil {
			return err
		}
		logger.Info("授权到期扫描完成", slog.Int("expired", result.Expired))
		return nil
	}

	if opts.RunOnce {
		return sweep()
	}
	logger.Info("Go 授权到期扫描 worker 启动",
		slog.Duration("interval", interval),
		slog.Duration("initialDelay", initialDelay),
		slog.Int("limit", opts.Limit),
	)
	if err := waitAuthorizationExpirySweepWorker(ctx, initialDelay); err != nil {
		return nil
	}
	for {
		if err := sweep(); err != nil {
			logger.Error("授权到期扫描失败", slog.Any("error", err))
		}
		if err := waitAuthorizationExpirySweepWorker(ctx, interval); err != nil {
			return nil
		}
	}
}

func newAuthorizationExpirySweepService(
	expiryStore port.ManagementResourceAuthorizationExpirySweeper,
	invalidator managementauthorizations.AuthorizationInvalidator,
	publisher managementauthorizations.AccountsStaticResetPublisher,
	teamReader managementauthorizations.TeamReader,
	logger *slog.Logger,
) *managementauthorizations.Service {
	return managementauthorizations.NewServiceWithOptions(managementauthorizations.ServiceOptions{
		ExpirySweepStore:         expiryStore,
		AuthorizationInvalidator: invalidator,
		Publisher:                publisher,
		TeamReader:               teamReader,
		Logger:                   logger,
	})
}

func waitAuthorizationExpirySweepWorker(ctx context.Context, duration time.Duration) error {
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
