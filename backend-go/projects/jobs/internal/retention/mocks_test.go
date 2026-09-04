package retention

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// Shared mock ports for the retention test suite. Every store records the
// cutoffs and limits it was called with so tests can assert the Node batch
// rhythm and deletion conditions end to end.

type fakePublicApiLogs struct {
	mutex   sync.Mutex
	script  []int64 // deleted counts per call; last value repeats
	cutoffs []string
	limits  []int
	calls   int
	err     error
}

func (f *fakePublicApiLogs) CleanupBefore(_ context.Context, cutoffCreatedAt string, limit int) (int64, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.cutoffs = append(f.cutoffs, cutoffCreatedAt)
	f.limits = append(f.limits, limit)
	deleted := f.script[len(f.script)-1]
	if f.calls < len(f.script) {
		deleted = f.script[f.calls]
	}
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	return deleted, nil
}

type fakeUsageRecords struct {
	mutex   sync.Mutex
	script  []UsageRecordsBatch
	cutoffs []string
	limits  []int
	calls   int
	err     error
}

func (f *fakeUsageRecords) CleanupProcessedBefore(_ context.Context, cutoffCreatedAt string, limit int) (UsageRecordsBatch, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.cutoffs = append(f.cutoffs, cutoffCreatedAt)
	f.limits = append(f.limits, limit)
	batch := UsageRecordsBatch{CutoffCreatedAt: cutoffCreatedAt}
	if len(f.script) > 0 {
		batch = f.script[len(f.script)-1]
		if f.calls < len(f.script) {
			batch = f.script[f.calls]
		}
	}
	f.calls++
	if f.err != nil {
		return UsageRecordsBatch{}, f.err
	}
	return batch, nil
}

type fakeStatsWriter struct {
	mutex             sync.Mutex
	usageScript       []UsageStatsRetentionCounts
	metricsScript     []SystemMetricsRetentionCounts
	usageInputs       []UsageStatsRetentionInput
	metricsInputs     []SystemMetricsRetentionInput
	usageCalls        int
	metricsCalls      int
	nonBusinessCalls  int
	nonBusinessInputs []string
	err               error
}

func (f *fakeStatsWriter) CleanupUsageStatsRetention(_ context.Context, input UsageStatsRetentionInput) (UsageStatsRetentionCounts, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.usageInputs = append(f.usageInputs, input)
	counts := UsageStatsRetentionCounts{}
	if f.usageCalls < len(f.usageScript) {
		counts = f.usageScript[f.usageCalls]
	}
	f.usageCalls++
	if f.err != nil {
		return UsageStatsRetentionCounts{}, f.err
	}
	return counts, nil
}

func (f *fakeStatsWriter) CleanupSystemMetricsRetention(_ context.Context, input SystemMetricsRetentionInput) (SystemMetricsRetentionCounts, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.metricsInputs = append(f.metricsInputs, input)
	counts := SystemMetricsRetentionCounts{}
	if f.metricsCalls < len(f.metricsScript) {
		counts = f.metricsScript[f.metricsCalls]
	}
	f.metricsCalls++
	if f.err != nil {
		return SystemMetricsRetentionCounts{}, f.err
	}
	return counts, nil
}

func (f *fakeStatsWriter) CleanupNonBusinessStatsData(_ context.Context, cutoffAt string, _ int) (NonBusinessDataCleanupCounts, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.nonBusinessInputs = append(f.nonBusinessInputs, cutoffAt)
	f.nonBusinessCalls++
	return NonBusinessDataCleanupCounts{CutoffAt: cutoffAt, TableRows: map[string]int64{}, FileDeletes: map[string]int64{}}, nil
}

func (f *fakeStatsWriter) CleanupDeletedApiKeyRecordStats(context.Context, DeletedApiKeyRecordStatsCleanupInput) error {
	return nil
}

func (f *fakeStatsWriter) CleanupDeletedAccountRecordStats(context.Context, DeletedAccountRecordStatsCleanupInput) error {
	return nil
}

func (f *fakeStatsWriter) UpsertAccountUsageSnapshots(context.Context, []AccountUsageSnapshotUpsertInput) error {
	return nil
}

type fakeDbService struct {
	mutex           sync.Mutex
	sessionsScript  []int64
	sessionsCutoffs []string
	sessionCalls    int
	sessionsErr     error

	codexScript  []*CodexContextExpiredCleanup
	codexCutoffs []string
	codexCalls   int
	codexErr     error

	chatResult *ChatRetentionResult
	chatInputs []ChatRetentionInput
	chatErr    error

	expiredSummary *ExpiredDeletedAccountSummary
	expiredErr     error
	expiredCalls   int
}

func (f *fakeDbService) CleanupChatRetention(_ context.Context, input ChatRetentionInput) (*ChatRetentionResult, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.chatInputs = append(f.chatInputs, input)
	if f.chatErr != nil {
		return nil, f.chatErr
	}
	return f.chatResult, nil
}

func (f *fakeDbService) CleanupExpiredSystemSessions(_ context.Context, expiredBefore string, _ int) (int64, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.sessionsCutoffs = append(f.sessionsCutoffs, expiredBefore)
	deleted := int64(0)
	if f.sessionCalls < len(f.sessionsScript) {
		deleted = f.sessionsScript[f.sessionCalls]
	}
	f.sessionCalls++
	if f.sessionsErr != nil {
		return 0, f.sessionsErr
	}
	return deleted, nil
}

func (f *fakeDbService) CleanupExpiredCodexContextStates(_ context.Context, expiredBefore string, _ int) (*CodexContextExpiredCleanup, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.codexCutoffs = append(f.codexCutoffs, expiredBefore)
	result := &CodexContextExpiredCleanup{}
	if f.codexCalls < len(f.codexScript) {
		result = f.codexScript[f.codexCalls]
	}
	f.codexCalls++
	if f.codexErr != nil {
		return nil, f.codexErr
	}
	return result, nil
}

func (f *fakeDbService) SettleCodexContextStorageCleanup(context.Context, CodexContextSettlement) (CodexContextSettlementResult, error) {
	return CodexContextSettlementResult{}, nil
}

func (f *fakeDbService) CleanupExpiredDeletedAccounts(context.Context) (*ExpiredDeletedAccountSummary, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.expiredCalls++
	if f.expiredErr != nil {
		return nil, f.expiredErr
	}
	return f.expiredSummary, nil
}

type fakeCodexStorage struct {
	mutex      sync.Mutex
	keys       [][]string
	deleted    []int64
	calls      int
	processErr error
}

func (f *fakeCodexStorage) ProcessBatch(_ context.Context, storageKeys []string) (int64, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.keys = append(f.keys, storageKeys)
	deleted := int64(0)
	if f.calls < len(f.deleted) {
		deleted = f.deleted[f.calls]
	}
	f.calls++
	if f.processErr != nil {
		return 0, f.processErr
	}
	return deleted, nil
}

type fakeEnqueuer struct {
	mutex    sync.Mutex
	jobs     []RecordMaintenanceJob
	asyncErr error
	result   EnqueueResult
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, job RecordMaintenanceJob) EnqueueResult {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.jobs = append(f.jobs, job)
	if f.result.DroppedReason == "" && !f.result.Queued {
		return EnqueueResult{Queued: true}
	}
	return f.result
}

func (f *fakeEnqueuer) EnqueueAsync(_ context.Context, job RecordMaintenanceJob) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.jobs = append(f.jobs, job)
	return f.asyncErr
}

type countingSleeper struct {
	mutex  sync.Mutex
	pauses int
	err    error
}

func (s *countingSleeper) sleep(context.Context) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.pauses++
	return s.err
}

type fakeCheckpointer struct {
	calls    int
	failWith error
}

func (c *fakeCheckpointer) CheckpointAfterDelete(context.Context) error {
	c.calls++
	return c.failWith
}

// newTestLogger captures slog records for event/message assertions.
func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	buffer := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buffer, nil))
	return logger, buffer
}

func assertLogContains(t *testing.T, buffer *bytes.Buffer, needles ...string) {
	t.Helper()
	contents := buffer.String()
	for _, needle := range needles {
		if !strings.Contains(contents, needle) {
			t.Fatalf("log output missing %q:\n%s", needle, contents)
		}
	}
}

func fixedClock(now time.Time) Clock {
	return func() time.Time { return now }
}

// mustJobJSON encodes a record-maintenance job payload for decode tests.
func mustJobJSON(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return encoded
}

// discardLogger returns a logger whose output is dropped.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
