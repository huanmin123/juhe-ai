package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/runtimelogretention"
)

func TestRuntimeLogRetentionCleanupWorkerSkipsWhenIndexDisabled(t *testing.T) {
	err := RunRuntimeLogRetentionCleanupWorker(context.Background(), config.Config{
		RuntimeLogIndexEnabled: false,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), RuntimeLogRetentionCleanupWorkerOptions{})
	if err != nil {
		t.Fatalf("RunRuntimeLogRetentionCleanupWorker() error = %v", err)
	}
}

func TestRuntimeLogRetentionCleanupWorkerValidatesEnabledOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Config
		opts RuntimeLogRetentionCleanupWorkerOptions
		want string
	}{
		{name: "postgres", cfg: config.Config{RuntimeLogIndexEnabled: true}, want: "JUHE_AI_POSTGRES_URL"},
		{name: "interval", cfg: config.Config{RuntimeLogIndexEnabled: true, PostgresURL: "postgres://unused"}, opts: RuntimeLogRetentionCleanupWorkerOptions{Interval: -time.Second}, want: "间隔"},
		{name: "initial delay", cfg: config.Config{RuntimeLogIndexEnabled: true, PostgresURL: "postgres://unused"}, opts: RuntimeLogRetentionCleanupWorkerOptions{InitialDelay: -time.Second}, want: "初始延迟"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRuntimeLogRetentionCleanupWorkerOptions(tc.cfg, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestRunRuntimeLogRetentionCleanupLoopSupportsRunOnce(t *testing.T) {
	calls := 0
	err := runRuntimeLogRetentionCleanupLoop(context.Background(), slog.Default(), RuntimeLogRetentionCleanupWorkerOptions{RunOnce: true}, func(context.Context) (runtimelogretention.CleanupResult, error) {
		calls++
		return runtimelogretention.CleanupResult{IndexEnabled: true, RuntimeLogs: 2}, nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestRunRuntimeLogRetentionCleanupLoopStopsOnCancelledInitialDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := runRuntimeLogRetentionCleanupLoop(ctx, slog.Default(), RuntimeLogRetentionCleanupWorkerOptions{InitialDelay: time.Hour}, func(context.Context) (runtimelogretention.CleanupResult, error) {
		calls++
		return runtimelogretention.CleanupResult{}, errors.New("must not run")
	})
	if err != nil || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}
