package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
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

	deps, status := h.checkDependencies(ctx)

	writeJSON(w, http.StatusOK, HealthResponse{
		Success:      status == "ok",
		Status:       status,
		Service:      "juhe-ai-go",
		Version:      version.Version,
		Dependencies: deps,
	})
}

type ReadinessHandler struct {
	checkDependencies func(context.Context) (map[string]CheckResult, string)
	now               func() time.Time
	cacheTTL          time.Duration
	cacheMu           sync.Mutex
	cachedUntil       time.Time
	cachedDeps        map[string]CheckResult
	cachedStatus      string
	inflight          chan struct{}
}

func NewReadinessHandler(cfg config.Config, logger *slog.Logger) *ReadinessHandler {
	health := NewHealthHandler(cfg, logger)
	return &ReadinessHandler{
		checkDependencies: health.checkDependencies,
		now:               time.Now,
		cacheTTL:          2 * time.Second,
	}
}

func (h *ReadinessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	w.Header().Set("Cache-Control", "no-store")
	deps, status := h.current(ctx)
	statusCode := http.StatusOK
	if status != "ok" {
		statusCode = http.StatusServiceUnavailable
	}

	writeJSON(w, statusCode, HealthResponse{
		Success:      status == "ok",
		Status:       status,
		Service:      "juhe-ai-go",
		Version:      version.Version,
		Dependencies: deps,
	})
}

func (h *ReadinessHandler) current(ctx context.Context) (map[string]CheckResult, string) {
	now := h.now()
	h.cacheMu.Lock()
	if !h.cachedUntil.IsZero() && now.Before(h.cachedUntil) {
		deps, status := h.cachedDeps, h.cachedStatus
		h.cacheMu.Unlock()
		return deps, status
	}
	done := h.inflight
	if done == nil {
		done = make(chan struct{})
		h.inflight = done
		go h.runCheck(done)
	}
	h.cacheMu.Unlock()

	select {
	case <-done:
		h.cacheMu.Lock()
		deps, status := h.cachedDeps, h.cachedStatus
		h.cacheMu.Unlock()
		return deps, status
	case <-ctx.Done():
		return map[string]CheckResult{}, "degraded"
	}
}

func (h *ReadinessHandler) runCheck(done chan struct{}) {
	deps := map[string]CheckResult{}
	status := "degraded"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer func() {
		if recover() != nil {
			deps = map[string]CheckResult{}
			status = "degraded"
		}

		completedAt := h.now()
		h.cacheMu.Lock()
		h.cachedDeps = deps
		h.cachedStatus = status
		h.cachedUntil = completedAt.Add(h.cacheTTL)
		h.inflight = nil
		close(done)
		h.cacheMu.Unlock()
	}()

	deps, status = h.checkDependencies(ctx)
}

func (h HealthHandler) checkDependencies(ctx context.Context) (map[string]CheckResult, string) {
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
	for name, required := range map[string]bool{
		"postgres":   h.cfg.PublicAPIEnabled || h.cfg.ManagementAPIEnabled,
		"redisCache": h.cfg.PublicAPIEnabled || h.cfg.ManagementAPIEnabled,
		"redisState": h.cfg.PublicAPIEnabled || h.cfg.ManagementAPIEnabled,
		"asynqQueue": h.cfg.PublicAPIEnabled || h.cfg.ManagementAPIEnabled,
	} {
		if required && !deps[name].Configured {
			deps[name] = CheckResult{
				Configured: false,
				Status:     "error",
				Error:      "dependency check failed",
			}
			status = "degraded"
		}
	}
	return deps, status
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
