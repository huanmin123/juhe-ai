package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/publicsettings"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func RunServer(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
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
		return err
	}
	defer func() { _ = stateRedis.Close() }()
	redisPingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := stateRedis.Ping(redisPingCtx); err != nil {
		cancel()
		return err
	}
	cancel()

	publicSettingsService := publicsettings.NewService(store)
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                     cfg,
		Logger:                     logger,
		PublicSettingsService:      &publicSettingsService,
		SystemAPIIPRateLimitReader: store,
		SystemAPIIPReadRateLimiter: httpapi.NewRedisSystemAPIIPReadRateLimiter(stateRedis),
	})

	server := &http.Server{
		Addr:              cfg.Address(),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("Go 后端 HTTP 服务启动", slog.String("address", cfg.Address()))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}
