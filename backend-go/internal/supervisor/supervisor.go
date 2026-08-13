// Package supervisor runs the four independent Go sidecar functions in one
// process. It deliberately does not merge their configuration, stores, schema
// or owner leases: those boundaries are part of the F1/F2/F3 contracts.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go/internal/operationlog"
	"github.com/huanminabc/juhe-ai/backend-go/internal/runtimelog"
	"github.com/huanminabc/juhe-ai/backend-go/internal/tablemonitor"
)

// Component is one independently configured and fenced sidecar function.
// Close is called only after every Run invocation has stopped.
type Component struct {
	Name  string
	Run   func(context.Context) error
	Close func() error
}

var ErrComponentStopped = errors.New("sidecar component stopped without cancellation")

const (
	defaultInitialRetryDelay = time.Second
	defaultMaxRetryDelay     = 30 * time.Second
)

// Options bounds the retry delay after a runtime component failure. Startup
// preflight errors are returned by New and are intentionally not retried.
type Options struct {
	InitialRetryDelay time.Duration
	MaxRetryDelay     time.Duration
}

// Run starts all components from the same context. Runtime failures stay
// inside the affected component's retry boundary; only caller cancellation
// stops the whole sidecar.
func Run(ctx context.Context, components []Component, logger *slog.Logger) error {
	return RunWithOptions(ctx, components, logger, Options{})
}

// RunWithOptions is Run with an explicit bounded retry policy. It exists so
// lifecycle tests can exercise recovery without waiting for production delays.
func RunWithOptions(ctx context.Context, components []Component, logger *slog.Logger, options Options) error {
	if len(components) == 0 {
		return errors.New("sidecar requires at least one component")
	}
	for _, component := range components {
		if component.Name == "" || component.Run == nil {
			return fmt.Errorf("sidecar component definition is incomplete")
		}
	}
	logger = loggerOrDefault(logger)
	options = normalizeOptions(options)
	var group sync.WaitGroup
	for _, component := range components {
		component := component
		group.Add(1)
		go func() {
			defer group.Done()
			runComponent(ctx, component, logger, options)
		}()
	}
	group.Wait()
	for index := len(components) - 1; index >= 0; index-- {
		component := components[index]
		if component.Close == nil {
			continue
		}
		if err := closeRecoverably(component.Close); err != nil {
			logger.Error("sidecar component store close failed", "component", component.Name, "error", err)
		}
	}
	return nil
}

func normalizeOptions(options Options) Options {
	if options.InitialRetryDelay <= 0 {
		options.InitialRetryDelay = defaultInitialRetryDelay
	}
	if options.MaxRetryDelay <= 0 {
		options.MaxRetryDelay = defaultMaxRetryDelay
	}
	if options.MaxRetryDelay < options.InitialRetryDelay {
		options.MaxRetryDelay = options.InitialRetryDelay
	}
	return options
}

func runComponent(ctx context.Context, component Component, logger *slog.Logger, options Options) {
	consecutiveFailures := 0
	for {
		if ctx.Err() != nil {
			logger.Info("sidecar component stopped", "component", component.Name, "cause", ctx.Err())
			return
		}
		logger.Info("sidecar component started", "component", component.Name)
		err := runRecoverably(ctx, component.Run)
		if ctx.Err() != nil {
			logger.Info("sidecar component stopped", "component", component.Name, "cause", ctx.Err())
			return
		}
		if err == nil {
			err = ErrComponentStopped
			consecutiveFailures = 1
		} else {
			consecutiveFailures++
		}
		delay := retryDelay(options, consecutiveFailures)
		logger.Error("sidecar component failed; retrying", "component", component.Name, "cause", err, "consecutiveFailures", consecutiveFailures, "retryDelay", delay.String())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			logger.Info("sidecar component stopped", "component", component.Name, "cause", ctx.Err())
			return
		case <-timer.C:
		}
	}
}

func runRecoverably(ctx context.Context, run func(context.Context) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("component panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return run(ctx)
}

func closeRecoverably(close func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("component close panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return close()
}

func retryDelay(options Options, consecutiveFailures int) time.Duration {
	delay := options.InitialRetryDelay
	for failure := 1; failure < consecutiveFailures && delay < options.MaxRetryDelay; failure++ {
		if delay > options.MaxRetryDelay/2 {
			return options.MaxRetryDelay
		}
		delay *= 2
	}
	return min(delay, options.MaxRetryDelay)
}

func min(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

// Supervisor owns the independently opened F1, F2, F3 and optional F4 stores until Run
// returns. Constructing it performs every configuration, store and schema
// preflight before any long-running component goroutine starts.
type Supervisor struct {
	components []Component
}

func New(ctx context.Context, getenv func(string) string, logger *slog.Logger) (*Supervisor, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	logger = loggerOrDefault(logger)

	runtimeConfig, err := runtimelog.LoadConfig(getenv)
	if err != nil {
		return nil, fmt.Errorf("load F1 runtime-log-indexer config: %w", err)
	}
	if runtimeConfig.Once {
		return nil, fmt.Errorf("JUHE_AI_RUNTIME_LOG_ONCE=true is not supported by the persistent juhe-ai-go-sidecar; use the explicit offline command instead")
	}
	tableConfig, err := tablemonitor.LoadConfig(getenv)
	if err != nil {
		return nil, fmt.Errorf("load F2 table-monitor config: %w", err)
	}
	auditConfig, err := auditlog.LoadConfig(getenv)
	if err != nil {
		return nil, fmt.Errorf("load F3 audit-log-writer config: %w", err)
	}
	auditInputConfig, err := auditlog.LoadInputServerConfig(getenv)
	if err != nil {
		return nil, fmt.Errorf("load F3 audit input config: %w", err)
	}
	operationConfig, err := operationlog.LoadConfig(getenv)
	if err != nil {
		return nil, fmt.Errorf("load F4 operation-log config: %w", err)
	}
	var operationInputConfig operationlog.InputServerConfig
	var operationStore operationlog.Store
	if operationConfig.Enabled {
		operationInputConfig, err = operationlog.LoadInputServerConfig(getenv)
		if err != nil {
			return nil, fmt.Errorf("load F4 operation-log input config: %w", err)
		}
		operationStore, err = operationlog.OpenStore(operationConfig)
		if err != nil {
			return nil, fmt.Errorf("open F4 operation-log store: %w", err)
		}
		if err = operationStore.EnsureSchema(ctx); err != nil {
			_ = operationStore.Close()
			return nil, fmt.Errorf("initialize F4 operation-log schema: %w", err)
		}
	}

	runtimeStore, err := runtimelog.OpenStore(ctx, runtimeConfig)
	if err != nil {
		return nil, fmt.Errorf("open F1 runtime-log-indexer store: %w", err)
	}
	cleanup := func() {
		_ = runtimeStore.Close()
		if operationStore != nil {
			_ = operationStore.Close()
		}
	}
	if err := runtimelog.EnsureSchema(ctx, runtimeStore); err != nil {
		cleanup()
		return nil, fmt.Errorf("initialize F1 runtime-log-indexer schema: %w", err)
	}
	if err := runtimeStore.CheckSchema(ctx); err != nil {
		cleanup()
		return nil, fmt.Errorf("verify F1 runtime-log-indexer schema: %w", err)
	}

	tableStore, err := tablemonitor.OpenStore(tableConfig)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("open F2 table-monitor store: %w", err)
	}
	cleanup = func() {
		_ = tableStore.Close()
		_ = runtimeStore.Close()
		if operationStore != nil {
			_ = operationStore.Close()
		}
	}
	if err := tableStore.EnsureSchema(ctx); err != nil {
		cleanup()
		return nil, fmt.Errorf("initialize F2 table-monitor schema: %w", err)
	}

	auditStore, err := auditlog.OpenStore(auditConfig)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("open F3 audit-log-writer store: %w", err)
	}
	cleanup = func() {
		_ = auditStore.Close()
		_ = tableStore.Close()
		_ = runtimeStore.Close()
		if operationStore != nil {
			_ = operationStore.Close()
		}
	}
	if err := auditStore.EnsureSchema(ctx); err != nil {
		cleanup()
		return nil, fmt.Errorf("initialize F3 audit-log-writer schema: %w", err)
	}

	runtimeIndexer := runtimelog.NewIndexer(runtimeConfig, runtimeStore)
	runTableOnce := func(runCtx context.Context) error {
		attemptCtx, cancel := context.WithTimeout(runCtx, tableConfig.RunTimeout)
		defer cancel()
		result, err := tablemonitor.RunOnce(attemptCtx, tableConfig, tableStore, time.Now().UTC())
		if err != nil {
			return err
		}
		logger.Info("table-monitor sample complete", "sampledAt", result.SampledAt.Format(time.RFC3339Nano), "databaseSnapshots", result.DatabaseSnapshots, "tableSnapshots", result.TableSnapshots, "deletedSnapshots", result.DeletedSnapshots)
		return nil
	}
	runTableMonitor := func(runCtx context.Context) error {
		if err := runTableOnce(runCtx); err != nil {
			return err
		}
		ticker := time.NewTicker(tableConfig.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return runCtx.Err()
			case <-ticker.C:
				if err := runTableOnce(runCtx); err != nil {
					return err
				}
			}
		}
	}

	components := []Component{
		{
			Name: "F1 runtime-log-indexer",
			Run: func(runCtx context.Context) error {
				return runtimelog.RunWithOwnerLease(runCtx, runtimeConfig, runtimeStore, runtimeIndexer.Run)
			},
			Close: runtimeStore.Close,
		},
		{
			Name: "F2 table-monitor",
			Run: func(runCtx context.Context) error {
				return tablemonitor.RunWithOwnerLease(runCtx, tableConfig, tableStore, runTableMonitor)
			},
			Close: tableStore.Close,
		},
		{
			Name: "F3 audit-log-writer",
			Run: func(runCtx context.Context) error {
				return auditlog.RunInputServer(runCtx, auditStore, auditConfig, auditInputConfig, logger)
			},
			Close: auditStore.Close,
		},
	}
	if operationConfig.Enabled {
		components = append(components, Component{Name: "F4 operation-log-owner", Run: func(runCtx context.Context) error {
			return operationlog.RunInputServer(runCtx, operationStore, operationConfig, operationInputConfig, logger)
		}, Close: operationStore.Close})
	}
	return &Supervisor{components: components}, nil
}

func (supervisor *Supervisor) Run(ctx context.Context, logger *slog.Logger) error {
	if supervisor == nil {
		return errors.New("sidecar supervisor is nil")
	}
	return Run(ctx, supervisor.components, logger)
}

func loggerOrDefault(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}
