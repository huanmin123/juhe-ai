package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/modelqualityhealthsync"
	"juhe-ai/backend-go/internal/store/port"
)

func TestModelQualityHealthSyncWorkerIsDisabledByDefault(t *testing.T) {
	deps := modelQualityHealthSyncWorkerDependencies{
		openStore: func(context.Context, string) (modelQualityHealthSyncWorkerStore, error) {
			t.Fatal("disabled worker opened PostgreSQL")
			return nil, nil
		},
	}
	if err := runModelQualityHealthSyncWorker(t.Context(), config.Config{}, nil, ModelQualityHealthSyncWorkerOptions{}, deps); err != nil {
		t.Fatalf("disabled worker error = %v", err)
	}
}

func TestModelQualityHealthSyncWorkerRejectsNilContextBeforeResources(t *testing.T) {
	deps := modelQualityHealthSyncWorkerDependencies{
		openStore: func(context.Context, string) (modelQualityHealthSyncWorkerStore, error) {
			t.Fatal("nil context opened PostgreSQL")
			return nil, nil
		},
	}
	err := runModelQualityHealthSyncWorker(nil, config.Config{}, nil, ModelQualityHealthSyncWorkerOptions{}, deps)
	if err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("worker error = %v, want context error", err)
	}
}

func TestModelQualityHealthSyncWorkerChecksCutoverGatesBeforeResources(t *testing.T) {
	opened := 0
	deps := modelQualityHealthSyncWorkerDependencies{
		openStore: func(context.Context, string) (modelQualityHealthSyncWorkerStore, error) {
			opened++
			return nil, errors.New("must not open")
		},
	}
	base := ModelQualityHealthSyncWorkerOptions{Enabled: true}
	for _, test := range []struct {
		name string
		opts ModelQualityHealthSyncWorkerOptions
		want string
	}{
		{name: "exclusive owner", opts: base, want: "exclusive"},
		{name: "legacy drained", opts: ModelQualityHealthSyncWorkerOptions{Enabled: true, GoExclusiveOwner: true}, want: "drained"},
		{name: "retention safe", opts: ModelQualityHealthSyncWorkerOptions{Enabled: true, GoExclusiveOwner: true, LegacyWorkerDrained: true}, want: "retention"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runModelQualityHealthSyncWorker(t.Context(), enabledModelQualityHealthSyncWorkerConfig(), nil, test.opts, deps)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("worker error = %v, want %q", err, test.want)
			}
		})
	}
	if opened != 0 {
		t.Fatalf("PostgreSQL opens = %d, want 0", opened)
	}
}

func TestModelQualityHealthSyncWorkerRequiresWorkerOwnerLockBeforeResources(t *testing.T) {
	opened := 0
	deps := modelQualityHealthSyncWorkerDependencies{
		openStore: func(context.Context, string) (modelQualityHealthSyncWorkerStore, error) {
			opened++
			return nil, errors.New("must not open")
		},
	}
	for _, cfg := range []config.Config{
		{PostgresURL: "postgres://unused"},
		{PostgresURL: "postgres://unused", OwnerLockEnabled: true, OwnerLockRole: "server"},
	} {
		err := runModelQualityHealthSyncWorker(t.Context(), cfg, nil, enabledModelQualityHealthSyncWorkerOptions(), deps)
		if err == nil || !strings.Contains(err.Error(), "owner lock") {
			t.Fatalf("worker error = %v, want owner lock error", err)
		}
	}
	if opened != 0 {
		t.Fatalf("PostgreSQL opens = %d, want 0", opened)
	}
}

func TestModelQualityHealthSyncWorkerValidatesAllOptionsBeforeOpeningPostgres(t *testing.T) {
	opened := 0
	deps := modelQualityHealthSyncWorkerDependencies{
		openStore: func(context.Context, string) (modelQualityHealthSyncWorkerStore, error) {
			opened++
			return nil, errors.New("must not open")
		},
		newService:     func(modelQualityHealthSyncWorkerStore) (modelQualityHealthSyncRunner, error) { return nil, nil },
		processOwnerID: func() (string, error) { return "owner", nil },
	}
	base := ModelQualityHealthSyncWorkerOptions{
		Enabled: true, GoExclusiveOwner: true, LegacyWorkerDrained: true, NodeRetentionSafe: true,
		OwnerID: "owner", RunOnce: true,
	}
	for _, test := range []struct {
		name string
		edit func(*ModelQualityHealthSyncWorkerOptions)
	}{
		{name: "interval", edit: func(opts *ModelQualityHealthSyncWorkerOptions) { opts.Interval = -time.Second }},
		{name: "initial delay", edit: func(opts *ModelQualityHealthSyncWorkerOptions) { opts.InitialDelay = -time.Second }},
		{name: "owner", edit: func(opts *ModelQualityHealthSyncWorkerOptions) { opts.OwnerID = " owner " }},
		{name: "batch", edit: func(opts *ModelQualityHealthSyncWorkerOptions) { opts.BatchSize = 101 }},
		{name: "workers", edit: func(opts *ModelQualityHealthSyncWorkerOptions) { opts.Workers = 17 }},
		{name: "lease", edit: func(opts *ModelQualityHealthSyncWorkerOptions) { opts.Lease = time.Second }},
		{name: "attempt timeout", edit: func(opts *ModelQualityHealthSyncWorkerOptions) { opts.AttemptTimeout = 31 * time.Minute }},
		{name: "batch budget", edit: func(opts *ModelQualityHealthSyncWorkerOptions) {
			opts.BatchSize = 1
			opts.Workers = 1
			opts.Lease = time.Minute
			opts.AttemptTimeout = 27*time.Second + time.Millisecond
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := base
			test.edit(&opts)
			if err := runModelQualityHealthSyncWorker(t.Context(), enabledModelQualityHealthSyncWorkerConfig(), nil, opts, deps); err == nil {
				t.Fatal("worker accepted invalid options")
			}
		})
	}
	if opened != 0 {
		t.Fatalf("PostgreSQL opens = %d, want 0", opened)
	}
}

func TestNormalizeModelQualityHealthSyncWorkerOptionsUsesServiceLeaseBudget(t *testing.T) {
	opts := enabledModelQualityHealthSyncWorkerOptions()
	opts.OwnerID = "owner"
	opts.BatchSize = 1
	opts.Workers = 1
	opts.Lease = time.Minute
	opts.AttemptTimeout = 27 * time.Second
	if _, _, _, err := normalizeModelQualityHealthSyncWorkerOptions(opts, nil); err != nil {
		t.Fatalf("normalize exact lease budget error = %v", err)
	}
	opts.AttemptTimeout += time.Millisecond
	if _, _, _, err := normalizeModelQualityHealthSyncWorkerOptions(opts, nil); err == nil {
		t.Fatal("normalize over lease budget error = nil")
	}
}

func TestModelQualityHealthSyncWorkerGeneratedOwnerIsStableAcrossBatches(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	store := &modelQualityHealthSyncWorkerStoreStub{}
	runner := &modelQualityHealthSyncRunnerStub{afterRun: func(call int) {
		if call == 2 {
			cancel()
		}
	}}
	ownerCalls := 0
	deps := modelQualityHealthSyncWorkerDependencies{
		openStore:  func(context.Context, string) (modelQualityHealthSyncWorkerStore, error) { return store, nil },
		newService: func(modelQualityHealthSyncWorkerStore) (modelQualityHealthSyncRunner, error) { return runner, nil },
		processOwnerID: func() (string, error) {
			ownerCalls++
			return "generated-owner-with-random-nonce", nil
		},
	}
	opts := enabledModelQualityHealthSyncWorkerOptions()
	opts.InitialDelay = time.Nanosecond
	opts.Interval = time.Nanosecond
	if err := runModelQualityHealthSyncWorker(ctx, enabledModelQualityHealthSyncWorkerConfig(), discardLogger(), opts, deps); err != nil {
		t.Fatalf("worker error = %v", err)
	}
	if ownerCalls != 1 {
		t.Fatalf("owner factory calls = %d, want 1", ownerCalls)
	}
	if len(runner.inputs) != 2 || runner.inputs[0].OwnerID != runner.inputs[1].OwnerID {
		t.Fatalf("owner IDs = %+v, want stable owner", runner.inputs)
	}
}

func TestModelQualityHealthSyncWorkerOwnerGenerationFailureDoesNotOpenPostgres(t *testing.T) {
	want := errors.New("entropy unavailable")
	opened := false
	deps := modelQualityHealthSyncWorkerDependencies{
		openStore: func(context.Context, string) (modelQualityHealthSyncWorkerStore, error) {
			opened = true
			return nil, nil
		},
		newService:     func(modelQualityHealthSyncWorkerStore) (modelQualityHealthSyncRunner, error) { return nil, nil },
		processOwnerID: func() (string, error) { return "", want },
	}
	err := runModelQualityHealthSyncWorker(t.Context(), enabledModelQualityHealthSyncWorkerConfig(), nil, enabledModelQualityHealthSyncWorkerOptions(), deps)
	if !errors.Is(err, want) {
		t.Fatalf("worker error = %v, want %v", err, want)
	}
	if opened {
		t.Fatal("owner generation failure opened PostgreSQL")
	}
}

func TestModelQualityHealthSyncWorkerCancellationClosesPostgres(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store := &modelQualityHealthSyncWorkerStoreStub{}
	runner := &modelQualityHealthSyncRunnerStub{}
	deps := modelQualityHealthSyncWorkerDependencies{
		openStore:      func(context.Context, string) (modelQualityHealthSyncWorkerStore, error) { return store, nil },
		newService:     func(modelQualityHealthSyncWorkerStore) (modelQualityHealthSyncRunner, error) { return runner, nil },
		processOwnerID: func() (string, error) { return "owner", nil },
	}
	opts := enabledModelQualityHealthSyncWorkerOptions()
	err := runModelQualityHealthSyncWorker(ctx, enabledModelQualityHealthSyncWorkerConfig(), discardLogger(), opts, deps)
	if err != nil {
		t.Fatalf("worker error = %v", err)
	}
	if len(runner.inputs) != 0 {
		t.Fatalf("batch calls = %d, want 0", len(runner.inputs))
	}
	if store.closeCalls != 1 {
		t.Fatalf("PostgreSQL close calls = %d, want 1", store.closeCalls)
	}
}

func TestModelQualityHealthSyncWorkerRunOnceSkipsWaitAndClosesPostgres(t *testing.T) {
	store := &modelQualityHealthSyncWorkerStoreStub{}
	runner := &modelQualityHealthSyncRunnerStub{}
	deps := modelQualityHealthSyncWorkerDependencies{
		openStore:      func(context.Context, string) (modelQualityHealthSyncWorkerStore, error) { return store, nil },
		newService:     func(modelQualityHealthSyncWorkerStore) (modelQualityHealthSyncRunner, error) { return runner, nil },
		processOwnerID: func() (string, error) { return "owner", nil },
	}
	opts := enabledModelQualityHealthSyncWorkerOptions()
	opts.RunOnce = true
	if err := runModelQualityHealthSyncWorker(t.Context(), enabledModelQualityHealthSyncWorkerConfig(), discardLogger(), opts, deps); err != nil {
		t.Fatalf("worker error = %v", err)
	}
	if store.pingCalls != 1 || store.closeCalls != 1 {
		t.Fatalf("PostgreSQL ping/close = %d/%d, want 1/1", store.pingCalls, store.closeCalls)
	}
	if len(runner.inputs) != 1 {
		t.Fatalf("batch calls = %d, want 1", len(runner.inputs))
	}
	input := runner.inputs[0]
	if input.ClaimLimit != 20 || input.WorkerCount != 4 || input.LeaseDuration != 5*time.Minute || input.ClaimTimeout != 15*time.Second || input.CompleteTimeout != 15*time.Second {
		t.Fatalf("RunOnce input = %+v", input)
	}
}

func TestModelQualityHealthSyncWorkerDefaultsAndStructuredBatchLog(t *testing.T) {
	input, interval, initialDelay, err := normalizeModelQualityHealthSyncWorkerOptions(
		enabledModelQualityHealthSyncWorkerOptions(),
		func() (string, error) { return "owner", nil },
	)
	if err != nil {
		t.Fatalf("normalize options error = %v", err)
	}
	if interval != time.Minute || initialDelay != 58*time.Second {
		t.Fatalf("interval/initial delay = %s/%s, want 1m/58s", interval, initialDelay)
	}
	if input.ClaimLimit != 20 || input.WorkerCount != 4 || input.LeaseDuration != 5*time.Minute || input.ClaimTimeout != 15*time.Second || input.CompleteTimeout != 15*time.Second {
		t.Fatalf("RunOnce input = %+v", input)
	}

	store := &modelQualityHealthSyncWorkerStoreStub{}
	runner := &modelQualityHealthSyncRunnerStub{result: modelqualityhealthsync.RunOnceResult{
		Claimed: 3, Quarantined: 1, Completed: 2, Stale: 1,
	}}
	deps := modelQualityHealthSyncWorkerDependencies{
		openStore:      func(context.Context, string) (modelQualityHealthSyncWorkerStore, error) { return store, nil },
		newService:     func(modelQualityHealthSyncWorkerStore) (modelQualityHealthSyncRunner, error) { return runner, nil },
		processOwnerID: func() (string, error) { return "owner", nil },
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	opts := enabledModelQualityHealthSyncWorkerOptions()
	opts.RunOnce = true
	if err := runModelQualityHealthSyncWorker(t.Context(), enabledModelQualityHealthSyncWorkerConfig(), logger, opts, deps); err != nil {
		t.Fatalf("worker error = %v", err)
	}
	for _, field := range []string{`"event":"model_quality_health_sync_batch"`, `"claimed":3`, `"quarantined":1`, `"completed":2`, `"stale":1`} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("batch log = %s, want field %s", logs.String(), field)
		}
	}
}

func enabledModelQualityHealthSyncWorkerOptions() ModelQualityHealthSyncWorkerOptions {
	return ModelQualityHealthSyncWorkerOptions{
		Enabled: true, GoExclusiveOwner: true, LegacyWorkerDrained: true, NodeRetentionSafe: true,
	}
}

func enabledModelQualityHealthSyncWorkerConfig() config.Config {
	return config.Config{
		PostgresURL:      "postgres://unused",
		OwnerLockEnabled: true,
		OwnerLockRole:    "worker",
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type modelQualityHealthSyncWorkerStoreStub struct {
	pingCalls  int
	closeCalls int
}

func (s *modelQualityHealthSyncWorkerStoreStub) Ping(context.Context) error {
	s.pingCalls++
	return nil
}

func (s *modelQualityHealthSyncWorkerStoreStub) Close() { s.closeCalls++ }

func (s *modelQualityHealthSyncWorkerStoreStub) ClaimFailedModelQualityHealthSyncs(context.Context, port.ModelQualityHealthSyncClaimInput) (port.ModelQualityHealthSyncClaimBatch, error) {
	return port.ModelQualityHealthSyncClaimBatch{}, nil
}

func (s *modelQualityHealthSyncWorkerStoreStub) CompleteModelQualityHealthSync(context.Context, port.ModelQualityHealthSyncCompleteInput) (port.ModelQualityHealthSyncCompleteResult, error) {
	return port.ModelQualityHealthSyncCompleteResult{}, nil
}

func (s *modelQualityHealthSyncWorkerStoreStub) ReleaseModelQualityHealthSync(context.Context, port.ModelQualityHealthSyncReleaseInput) (bool, error) {
	return false, nil
}

type modelQualityHealthSyncRunnerStub struct {
	inputs   []modelqualityhealthsync.RunOnceInput
	afterRun func(int)
	result   modelqualityhealthsync.RunOnceResult
	err      error
}

func (s *modelQualityHealthSyncRunnerStub) RunOnce(_ context.Context, input modelqualityhealthsync.RunOnceInput) (modelqualityhealthsync.RunOnceResult, error) {
	s.inputs = append(s.inputs, input)
	if s.afterRun != nil {
		s.afterRun(len(s.inputs))
	}
	return s.result, s.err
}
