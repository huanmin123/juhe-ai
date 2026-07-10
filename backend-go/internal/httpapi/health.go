package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/platform/postgres"
	redishealth "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/version"
)

type HealthHandler struct {
	cfg    config.Config
	logger *slog.Logger
}

type HealthResponse struct {
	Success      bool                   `json:"success"`
	Status       string                 `json:"status"`
	Service      string                 `json:"service"`
	Version      string                 `json:"version"`
	Dependencies map[string]CheckResult `json:"dependencies"`
}

type CheckResult struct {
	Configured bool   `json:"configured"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

func NewHealthHandler(cfg config.Config, logger *slog.Logger) HealthHandler {
	return HealthHandler{cfg: cfg, logger: logger}
}

func (h HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	deps := map[string]CheckResult{
		"postgres":   mapCheck(postgres.Check(ctx, h.cfg.PostgresURL)),
		"redisCache": mapCheck(redishealth.Check(ctx, h.cfg.RedisCacheURL)),
		"redisState": mapCheck(redishealth.Check(ctx, h.cfg.RedisStateURL)),
		"asynqQueue": mapCheck(queue.Check(ctx, h.cfg.RedisQueueURL)),
	}

	status := "ok"
	for _, dep := range deps {
		if dep.Status == "error" {
			status = "degraded"
			break
		}
	}

	writeJSON(w, http.StatusOK, HealthResponse{
		Success:      status == "ok",
		Status:       status,
		Service:      "juhe-ai-go",
		Version:      version.Version,
		Dependencies: deps,
	})
}

func mapCheck(result postgres.CheckResult) CheckResult {
	mapped := CheckResult{
		Configured: result.Configured,
		Status:     result.Status,
	}
	if result.Error != "" {
		mapped.Error = "dependency check failed"
	}
	return mapped
}
