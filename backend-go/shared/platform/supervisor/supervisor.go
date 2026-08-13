// Package supervisor provides independent component restart boundaries for a
// single Go project. It has no dependency on any business function.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
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

func loggerOrDefault(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}
