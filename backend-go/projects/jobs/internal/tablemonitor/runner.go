package tablemonitor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Runner owns the F2 scheduling lifecycle. It deliberately keeps scheduling
// here instead of a generic queue: F2 has one lease-protected, periodic owner.
type Runner struct {
	cfg    Config
	store  *Store
	logger *slog.Logger

	mu     sync.RWMutex
	status runnerStatus
}

type runnerStatus struct {
	OwnerHeld   bool
	LastAttempt time.Time
	LastSuccess time.Time
	LastError   string
	LastResult  SampleResult
}

type healthResponse struct {
	Ready         bool   `json:"ready"`
	OwnerHeld     bool   `json:"ownerHeld"`
	LastAttemptAt string `json:"lastAttemptAt,omitempty"`
	LastSuccessAt string `json:"lastSuccessAt,omitempty"`
}

func NewRunner(cfg Config, store *Store, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{cfg: cfg, store: store, logger: logger}
}

func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.store == nil {
		return errors.New("F2 table-monitor runner is not initialized")
	}
	return RunWithOwnerLease(ctx, r.cfg, r.store, func(ownerCtx context.Context) error {
		r.setOwnerHeld(true)
		defer r.setOwnerHeld(false)
		if err := r.runCycle(ownerCtx); err != nil {
			return err
		}
		ticker := time.NewTicker(r.cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ownerCtx.Done():
				return ownerCtx.Err()
			case <-ticker.C:
				if err := r.runCycle(ownerCtx); err != nil {
					return err
				}
			}
		}
	})
}

// RunSingleCycle is an explicit one-shot maintenance entry. It still acquires
// the same owner lease, so it cannot race a running jobs process.
func RunSingleCycle(ctx context.Context, cfg Config, store *Store) (SampleResult, error) {
	var result SampleResult
	err := RunWithOwnerLease(ctx, cfg, store, func(ownerCtx context.Context) error {
		attemptCtx, cancel := context.WithTimeout(ownerCtx, cfg.RunTimeout)
		defer cancel()
		var err error
		result, err = RunOnce(attemptCtx, cfg, store, time.Now().UTC())
		return err
	})
	return result, err
}

func (r *Runner) runCycle(ctx context.Context) error {
	attemptedAt := time.Now().UTC()
	r.mu.Lock()
	r.status.LastAttempt = attemptedAt
	r.mu.Unlock()
	attemptCtx, cancel := context.WithTimeout(ctx, r.cfg.RunTimeout)
	result, err := RunOnce(attemptCtx, r.cfg, r.store, attemptedAt)
	cancel()
	if err == nil {
		r.mu.Lock()
		r.status.LastSuccess = time.Now().UTC()
		r.status.LastError = ""
		r.status.LastResult = result
		r.mu.Unlock()
		r.logger.Info("F2 table-monitor sample complete", "sampledAt", result.SampledAt.Format(time.RFC3339Nano), "databaseSnapshots", result.DatabaseSnapshots, "tableSnapshots", result.TableSnapshots, "deletedSnapshots", result.DeletedSnapshots)
		return nil
	}
	if errors.Is(err, ErrOwnerLeaseLost) || errors.Is(context.Cause(ctx), ErrOwnerLeaseLost) {
		return ErrOwnerLeaseLost
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.mu.Lock()
	r.status.LastError = err.Error()
	r.mu.Unlock()
	r.logger.Error("F2 table-monitor sample failed; scheduler will retry next interval", "error", err, "nextInterval", r.cfg.Interval)
	return nil
}

func (r *Runner) setOwnerHeld(value bool) {
	r.mu.Lock()
	r.status.OwnerHeld = value
	r.mu.Unlock()
}

func (r *Runner) Ready() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status.OwnerHeld && !r.status.LastSuccess.IsZero() && r.status.LastError == ""
}

func (r *Runner) HealthHandler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			http.NotFound(response, request)
			return
		}
		r.mu.RLock()
		status := r.status
		r.mu.RUnlock()
		ready := r.Ready()
		payload := healthResponse{Ready: ready, OwnerHeld: status.OwnerHeld}
		if !status.LastAttempt.IsZero() {
			payload.LastAttemptAt = status.LastAttempt.UTC().Format(time.RFC3339Nano)
		}
		if !status.LastSuccess.IsZero() {
			payload.LastSuccessAt = status.LastSuccess.UTC().Format(time.RFC3339Nano)
		}
		response.Header().Set("Content-Type", "application/json")
		if !ready {
			response.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(response).Encode(payload)
	})
}
