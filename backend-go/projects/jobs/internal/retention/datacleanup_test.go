package retention

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedPolicy mirrors validPolicySettings; tests assert cutoff strings
// against the fixed clock 2026-09-04T20:30:00Z.
func fixedNow() time.Time {
	return time.Date(2026, 9, 4, 20, 30, 0, 0, time.UTC)
}

func sqliteJob(t *testing.T, mutate func(j *DataRetentionJob)) (*DataRetentionJob, *fakePublicApiLogs, *fakeUsageRecords, *fakeStatsWriter, *fakeDbService, *fakeCodexStorage, *fakeEnqueuer, *countingSleeper) {
	t.Helper()
	publicApiLogs := &fakePublicApiLogs{script: []int64{0}}
	usageRecords := &fakeUsageRecords{script: []UsageRecordsBatch{{CutoffCreatedAt: "x"}}}
	stats := &fakeStatsWriter{}
	db := &fakeDbService{}
	codex := &fakeCodexStorage{}
	enqueuer := &fakeEnqueuer{}
	sleeper := &countingSleeper{}
	job := NewDataRetentionJob(ModeSQLite, "worker", "ingest-worker")
	job.Settings = func(context.Context) (map[string]any, error) { return validPolicySettings(), nil }
	job.Timezone = func(context.Context) (string, error) { return "UTC", nil }
	job.Clock = fixedClock(fixedNow())
	job.Sleep = sleeper.sleep
	job.PublicApiLogs = publicApiLogs
	job.UsageRecords = usageRecords
	job.Stats = stats
	job.DB = db
	job.CodexStorage = codex
	job.Enqueuer = enqueuer
	if mutate != nil {
		mutate(job)
	}
	return job, publicApiLogs, usageRecords, stats, db, codex, enqueuer, sleeper
}

// TestDataRetentionSQLiteHappyPath walks the full ingest-worker orchestration
// and asserts the Node stage order, cutoff strings, batch rhythm and result
// accumulation.
func TestSQLiteHappyPath(t *testing.T) {
	job, publicApiLogs, usageRecords, stats, db, codex, _, sleeper := sqliteJob(t, nil)
	publicApiLogs.script = []int64{CleanupBatchSize, 500}
	usageRecords.script = []UsageRecordsBatch{
		{CutoffCreatedAt: "c1", DeletedRows: CleanupBatchSize, HasMore: true},
		{CutoffCreatedAt: "c1", DeletedRows: 300, HasMore: false},
	}
	stats.usageScript = []UsageStatsRetentionCounts{
		{UsageStatsMinute: CleanupBatchSize, UsageStatsDaily: 5},
		{},
	}
	stats.metricsScript = []SystemMetricsRetentionCounts{
		{SystemMetricsSamples: 10},
		{},
	}
	db.sessionsScript = []int64{CleanupBatchSize, 700}
	db.codexScript = []*CodexContextExpiredCleanup{
		{DeletedSessions: CleanupBatchSize, DeletedResponses: 3, DeletedCompacts: 2, StorageKeys: []string{"k1"}, HasMore: true},
		{DeletedSessions: 100, DeletedResponses: 1, StorageKeys: []string{"k2"}, HasMore: false},
	}
	codex.deleted = []int64{1, 1}

	result, err := job.CleanupExpiredRetainedData(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpiredRetainedData() unexpected error: %v", err)
	}
	if result.PublicApiLogs != CleanupBatchSize+500 {
		t.Fatalf("publicApiLogs = %d, want %d", result.PublicApiLogs, CleanupBatchSize+500)
	}
	if result.UsageRecords != CleanupBatchSize+300 {
		t.Fatalf("usageRecords = %d, want %d", result.UsageRecords, CleanupBatchSize+300)
	}
	if result.UsageStatsMinute != CleanupBatchSize || result.UsageStatsDaily != 5 {
		t.Fatalf("usage stats counters not accumulated: %+v", result)
	}
	if result.SystemMetricsSamples != 10 {
		t.Fatalf("systemMetricsSamples = %d, want 10", result.SystemMetricsSamples)
	}
	if result.SystemSessions != CleanupBatchSize+700 {
		t.Fatalf("systemSessions = %d, want %d", result.SystemSessions, CleanupBatchSize+700)
	}
	if result.CodexContextSessions != 1100 || result.CodexContextResponses != 4 || result.CodexContextCompacts != 2 {
		t.Fatalf("codex counters not accumulated: %+v", result)
	}
	if result.CodexContextFiles != 2 {
		t.Fatalf("codexContextFiles = %d, want 2", result.CodexContextFiles)
	}

	// Cutoff strings: now minus configured retention days.
	wantPublicCutoff := "2026-08-05T20:30:00.000Z" // 30 days
	if publicApiLogs.cutoffs[0] != wantPublicCutoff {
		t.Fatalf("publicApiLogs cutoff = %q, want %q", publicApiLogs.cutoffs[0], wantPublicCutoff)
	}
	if len(publicApiLogs.limits) == 0 || publicApiLogs.limits[0] != CleanupBatchSize {
		t.Fatalf("publicApiLogs limit = %v, want %d", publicApiLogs.limits, CleanupBatchSize)
	}
	wantUsageCutoff := "2026-08-21T20:30:00.000Z" // 14 days
	if usageRecords.cutoffs[0] != wantUsageCutoff {
		t.Fatalf("usageRecords cutoff = %q, want %q", usageRecords.cutoffs[0], wantUsageCutoff)
	}
	if len(stats.usageInputs) == 0 {
		t.Fatal("stats writer was never called")
	}
	input := stats.usageInputs[0]
	checks := map[string][2]string{
		"minuteCutoffMinute":    {input.MinuteCutoffMinute, "2026-09-02T20:30"},            // 48 configured hours
		"hourlyCutoffHour":      {input.HourlyCutoffHour, "2026-07-06T20"},                 // 60 configured days
		"dailyCutoffDate":       {input.DailyCutoffDate, "2026-06-06"},                     // 90 configured days
		"rankSnapshotCutoffIso": {input.RankSnapshotCutoffIso, "2026-03-08T20:30:00.000Z"}, // 180 configured days
		"windowCutoffDate":      {input.WindowCutoffDate, "2026-08-05"},                    // fixed 30 days
		"windowCutoffIso":       {input.WindowCutoffIso, "2026-08-05T20:30:00.000Z"},
	}
	for name, check := range checks {
		if check[0] != check[1] {
			t.Fatalf("%s = %q, want %q", name, check[0], check[1])
		}
	}
	if input.AccountQualityMinuteCutoffMinute != "2026-09-03T20:30" {
		t.Fatalf("accountQualityMinuteCutoffMinute = %q", input.AccountQualityMinuteCutoffMinute)
	}
	if input.MonthlyCutoffMonth != "2024-09" {
		t.Fatalf("MonthlyCutoffMonth = %q, want 2024-09 (24 months)", input.MonthlyCutoffMonth)
	}
	weeklyChecks := [2]string{input.WeeklyCutoffWeek, "2025-09-01"} // 52 weeks -> 2025-09-05 (Friday) snaps back
	if weeklyChecks[0] != weeklyChecks[1] {
		t.Fatalf("weeklyCutoffWeek = %q, want %q", weeklyChecks[0], weeklyChecks[1])
	}
	if input.Limit != CleanupBatchSize {
		t.Fatalf("stats input limit = %d, want %d", input.Limit, CleanupBatchSize)
	}
	if len(db.sessionsCutoffs) == 0 || db.sessionsCutoffs[0] != "2026-09-04T20:30:00.000Z" {
		t.Fatalf("sessions cutoff = %v", db.sessionsCutoffs)
	}
	if len(codex.keys) != 2 || len(codex.keys[0]) != 1 || codex.keys[0][0] != "k1" {
		t.Fatalf("codex storage batches = %+v", codex.keys)
	}
	// Pauses: publicApiLogs(1 full) + usageRecords(1 full) + stats(1 full) +
	// metrics(1 full) + sessions(1 full) + codex(1 full).
	if sleeper.pauses != 6 {
		t.Fatalf("pauses = %d, want 6", sleeper.pauses)
	}
}

// TestSQLiteEmptySetRunsSingleBatch asserts the empty-set path: one call per
// domain, no pauses, zero result.
func TestSQLiteEmptySet(t *testing.T) {
	job, publicApiLogs, usageRecords, stats, db, codex, _, sleeper := sqliteJob(t, nil)
	result, err := job.CleanupExpiredRetainedData(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Sum() != 0 {
		t.Fatalf("result not empty: %+v", result)
	}
	if publicApiLogs.calls != 1 || usageRecords.calls != 1 || stats.usageCalls != 1 || stats.metricsCalls != 1 || db.sessionCalls != 1 || db.codexCalls != 1 || codex.calls != 1 {
		t.Fatalf("empty set must call each domain once: public=%d usage=%d usageStats=%d metrics=%d sessions=%d codex=%d storage=%d",
			publicApiLogs.calls, usageRecords.calls, stats.usageCalls, stats.metricsCalls, db.sessionCalls, db.codexCalls, codex.calls)
	}
	if sleeper.pauses != 0 {
		t.Fatalf("pauses = %d, want 0", sleeper.pauses)
	}
}

// TestBatchBoundaryFullBatchContinues asserts the "deleted == batchSize keeps
// going, one less stops" boundary at the orchestration level.
func TestBatchBoundaryFullBatchContinues(t *testing.T) {
	tests := []struct {
		name      string
		script    []int64
		wantCalls int
		wantPause int
	}{
		{name: "exactly full batch continues", script: []int64{CleanupBatchSize, 0}, wantCalls: 2, wantPause: 1},
		{name: "full batch minus one stops", script: []int64{CleanupBatchSize - 1}, wantCalls: 1, wantPause: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, publicApiLogs, _, _, _, _, _, sleeper := sqliteJob(t, nil)
			publicApiLogs.script = tt.script
			result, err := job.CleanupExpiredRetainedData(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if publicApiLogs.calls != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", publicApiLogs.calls, tt.wantCalls)
			}
			if sleeper.pauses != tt.wantPause {
				t.Fatalf("pauses = %d, want %d", sleeper.pauses, tt.wantPause)
			}
			wantTotal := tt.script[0]
			if result.PublicApiLogs != wantTotal {
				t.Fatalf("deleted = %d, want %d", result.PublicApiLogs, wantTotal)
			}
		})
	}
}

// TestMaxBatchesCap asserts a run never exceeds
// DATA_RETENTION_CLEANUP_MAX_BATCHES_PER_RUN batches even when every batch is
// full, and keeps the Node pause placement: cleanupInBatches pauses after
// every full batch, including the last allowed one.
func TestMaxBatchesCap(t *testing.T) {
	job, publicApiLogs, _, _, _, _, _, sleeper := sqliteJob(t, nil)
	publicApiLogs.script = []int64{CleanupBatchSize}
	if _, err := job.CleanupExpiredRetainedData(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if publicApiLogs.calls != CleanupMaxBatchesPerRun {
		t.Fatalf("calls = %d, want %d", publicApiLogs.calls, CleanupMaxBatchesPerRun)
	}
	if sleeper.pauses != CleanupMaxBatchesPerRun {
		t.Fatalf("pauses = %d, want %d", sleeper.pauses, CleanupMaxBatchesPerRun)
	}
}

// TestSettingsMissingFailsClosed asserts the fail-closed setting validation:
// no store call may happen when a retention setting is missing or invalid.
func TestSettingsMissingFailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
		wantErr  string
	}{
		{
			name:     "all missing",
			settings: map[string]any{},
			wantErr:  "系统设置 publicApiLogRetentionDays 必须是整数",
		},
		{
			name: "one key out of range",
			settings: func() map[string]any {
				settings := validPolicySettings()
				settings[SettingUsageRecordRetentionDays] = usageRecordRetentionMaxDays + 1
				return settings
			}(),
			wantErr: "系统设置 usageRecordRetentionDays 必须在 1 到 180 之间",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, publicApiLogs, usageRecords, stats, db, _, _, _ := sqliteJob(t, func(j *DataRetentionJob) {
				j.Settings = func(context.Context) (map[string]any, error) { return tt.settings, nil }
			})
			_, err := job.CleanupExpiredRetainedData(context.Background())
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if publicApiLogs.calls != 0 || usageRecords.calls != 0 || stats.usageCalls != 0 || db.sessionCalls != 0 {
				t.Fatal("stores must not be called when settings fail validation")
			}
		})
	}
	t.Run("settings source failure propagates", func(t *testing.T) {
		job, publicApiLogs, _, _, _, _, _, _ := sqliteJob(t, func(j *DataRetentionJob) {
			j.Settings = func(context.Context) (map[string]any, error) { return nil, errors.New("settings unavailable") }
		})
		_, err := job.CleanupExpiredRetainedData(context.Background())
		if err == nil || err.Error() != "settings unavailable" {
			t.Fatalf("error = %v, want settings unavailable", err)
		}
		if publicApiLogs.calls != 0 {
			t.Fatal("no store call expected")
		}
	})
}

func TestNonWorkerRoleReturnsEmptyWithoutCalls(t *testing.T) {
	for _, role := range []string{"server", "db-service", ""} {
		job, publicApiLogs, usageRecords, stats, db, _, _, _ := sqliteJob(t, func(j *DataRetentionJob) {
			j.ProcessRole = role
		})
		result, err := job.CleanupExpiredRetainedData(context.Background())
		if err != nil {
			t.Fatalf("role %q unexpected error: %v", role, err)
		}
		if result.Sum() != 0 {
			t.Fatalf("role %q result not empty", role)
		}
		if publicApiLogs.calls != 0 || usageRecords.calls != 0 || stats.usageCalls != 0 || db.sessionCalls != 0 {
			t.Fatalf("role %q stores must not be called", role)
		}
	}
}

func TestPostgresModeRejectedInSQLiteChain(t *testing.T) {
	job, publicApiLogs, _, _, _, _, _, _ := sqliteJob(t, func(j *DataRetentionJob) {
		j.Mode = ModePostgres
	})
	_, err := job.CleanupExpiredRetainedData(context.Background())
	want := "高性能模式禁止运行单机数据保留清理 worker；请使用 PostgreSQL 数据维护任务清理非业务数据，禁止静默跳过或回落 SQLite 清理链路"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if publicApiLogs.calls != 0 {
		t.Fatal("stores must not be called")
	}
}

func TestSecondConcurrentRunReturnsEmpty(t *testing.T) {
	release := make(chan struct{})
	job, _, _, _, _, _, _, _ := sqliteJob(t, func(j *DataRetentionJob) {
		j.PublicApiLogs = &blockingCleaner{release: release}
	})
	var wg sync.WaitGroup
	results := make(chan CleanupResult, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := job.CleanupExpiredRetainedData(context.Background())
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			results <- result
		}()
	}
	// One caller runs the cleanup; the other must return an empty result
	// immediately without waiting.
	deadline := time.After(2 * time.Second)
	empty := 0
	for empty < 1 {
		select {
		case result := <-results:
			if result.Sum() == 0 {
				empty++
			}
		case <-deadline:
			t.Fatal("second run did not return an empty result while the first was running")
		}
	}
	close(release)
	wg.Wait()
}

type blockingCleaner struct {
	release chan struct{}
}

func (b *blockingCleaner) CleanupBefore(_ context.Context, _ string, _ int) (int64, error) {
	<-b.release
	return 0, nil
}

func TestRunDispatchesByMode(t *testing.T) {
	t.Run("postgres mode dispatches maintenance jobs", func(t *testing.T) {
		job, publicApiLogs, _, stats, db, codex, enqueuer, sleeper := sqliteJob(t, func(j *DataRetentionJob) {
			j.Mode = ModePostgres
		})
		publicApiLogs.script = []int64{CleanupBatchSize, 0}
		stats.usageScript = []UsageStatsRetentionCounts{{UsageStatsMinute: 1}, {}}
		stats.metricsScript = []SystemMetricsRetentionCounts{{}}
		db.sessionsScript = []int64{CleanupBatchSize, 0}
		db.codexScript = []*CodexContextExpiredCleanup{
			{DeletedSessions: CleanupBatchSize, StorageKeys: []string{"k"}, HasMore: true},
			{DeletedSessions: 1, HasMore: false},
		}
		result, err := job.Run(context.Background())
		if err != nil {
			t.Fatalf("Run() unexpected error: %v", err)
		}
		if result.Sum() != 0 {
			t.Fatal("postgres dispatch returns an empty in-process result")
		}
		if len(enqueuer.jobs) != 1 {
			t.Fatalf("enqueued jobs = %d, want 1", len(enqueuer.jobs))
		}
		enqueued := enqueuer.jobs[0]
		if enqueued.Type != JobTypeUsageRecordsCleanup {
			t.Fatalf("enqueued type = %q", enqueued.Type)
		}
		if enqueued.CutoffAt != "2026-08-21T20:30:00.000Z" {
			t.Fatalf("enqueued cutoffAt = %q, want the usageRecordDays cutoff", enqueued.CutoffAt)
		}
		if enqueued.BatchSize != CleanupBatchSize || enqueued.MaxBatches != CleanupMaxBatchesPerRun {
			t.Fatalf("enqueued batch config = %d/%d", enqueued.BatchSize, enqueued.MaxBatches)
		}
		if enqueued.ID == "" || !strings.HasPrefix(enqueued.ID, "recmaint_") {
			t.Fatalf("enqueued job id not normalized: %q", enqueued.ID)
		}
		if publicApiLogs.calls != 2 {
			t.Fatalf("publicApiLogs calls = %d, want 2 (full batch then stop)", publicApiLogs.calls)
		}
		if stats.usageCalls != 2 {
			t.Fatalf("stats calls = %d, want 2 (fullBatchSize=1 semantics)", stats.usageCalls)
		}
		if db.sessionCalls != 2 {
			t.Fatalf("session calls = %d, want 2", db.sessionCalls)
		}
		if db.codexCalls != 2 || codex.calls != 2 {
			t.Fatalf("codex calls db=%d storage=%d, want 2/2", db.codexCalls, codex.calls)
		}
		if sleeper.pauses == 0 {
			t.Fatal("postgres stages must keep the inter-batch pause")
		}
	})
	t.Run("sqlite mode dispatches in-process cleanup", func(t *testing.T) {
		job, publicApiLogs, _, _, _, _, enqueuer, _ := sqliteJob(t, nil)
		if _, err := job.Run(context.Background()); err != nil {
			t.Fatalf("Run() unexpected error: %v", err)
		}
		if len(enqueuer.jobs) != 0 {
			t.Fatal("sqlite mode must not enqueue record maintenance jobs")
		}
		if publicApiLogs.calls != 1 {
			t.Fatalf("publicApiLogs calls = %d, want 1", publicApiLogs.calls)
		}
	})
	t.Run("postgres enqueue failure is logged and wrapped", func(t *testing.T) {
		logger, buffer := newTestLogger()
		job, _, _, _, _, _, _, _ := sqliteJob(t, func(j *DataRetentionJob) {
			j.Mode = ModePostgres
			j.Logger = logger
			j.Enqueuer = &fakeEnqueuer{asyncErr: errors.New("redis stream down")}
		})
		_, err := job.Run(context.Background())
		if err == nil || err.Error() != "redis stream down" {
			t.Fatalf("error = %v, want redis stream down", err)
		}
		assertLogContains(t, buffer, "postgres_data_retention_maintenance_jobs_enqueue_failed", "PostgreSQL 高性能数据保留维护任务投递失败")
	})
}

func TestAbortedContextStopsBetweenBatches(t *testing.T) {
	job, publicApiLogs, _, _, _, _, _, _ := sqliteJob(t, nil)
	publicApiLogs.script = []int64{CleanupBatchSize}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The injected sleeper aborts deterministically between the first full
	// batch and the second one, mirroring an abort during the 25ms pause.
	job.Sleep = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}
	_, err := job.CleanupExpiredRetainedData(ctx)
	if err == nil {
		t.Fatal("expected abort error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if publicApiLogs.calls != 1 {
		t.Fatalf("calls = %d, want 1 (abort must end the loop)", publicApiLogs.calls)
	}
}

func TestUsageRecordsBlockedReasonWarnsAndStops(t *testing.T) {
	logger, buffer := newTestLogger()
	job, _, usageRecords, _, _, _, _, _ := sqliteJob(t, func(j *DataRetentionJob) {
		j.Logger = logger
	})
	usageRecords.script = []UsageRecordsBatch{{
		CutoffCreatedAt: "c", DeletedRows: 0, HasMore: false, BlockedReason: "等待统计安全游标追平",
	}}
	_, err := job.CleanupExpiredRetainedData(context.Background())
	if err != nil {
		t.Fatalf("blocked cleanup must not fail the run: %v", err)
	}
	if usageRecords.calls != 1 {
		t.Fatalf("usage records calls = %d, want 1 (blocked stops the loop)", usageRecords.calls)
	}
	assertLogContains(t, buffer,
		"data_retention_usage_records_cleanup_blocked",
		"使用记录保留清理被统计安全游标拦截",
		"等待统计安全游标追平",
	)
}

func TestCheckpointAfterDatasetDeletes(t *testing.T) {
	tests := []struct {
		name          string
		script        []int64
		wantCalls     int
		checkpointErr error
	}{
		{name: "checkpoint runs after deletes", script: []int64{5}, wantCalls: 1},
		{name: "no checkpoint on empty dataset", script: []int64{0}, wantCalls: 0},
		{name: "checkpoint failure only warns", script: []int64{5}, wantCalls: 1, checkpointErr: errors.New("wal busy")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buffer := newTestLogger()
			checkpointer := &fakeCheckpointer{failWith: tt.checkpointErr}
			job, _, _, _, _, _, _, _ := sqliteJob(t, func(j *DataRetentionJob) {
				j.Logger = logger
				j.Checkpointer = checkpointer
				j.PublicApiLogs = &fakePublicApiLogs{script: tt.script}
			})
			if _, err := job.CleanupExpiredRetainedData(context.Background()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if checkpointer.calls != tt.wantCalls {
				t.Fatalf("checkpoint calls = %d, want %d", checkpointer.calls, tt.wantCalls)
			}
			if tt.wantCalls == 1 && tt.checkpointErr == nil {
				assertLogContains(t, buffer, "data_retention_dataset_checkpoint_completed", "数据集与使用记录分片 WAL checkpoint 完成")
			}
			if tt.checkpointErr != nil {
				assertLogContains(t, buffer, "data_retention_dataset_checkpoint_failed", "数据集与使用记录分片 WAL checkpoint 失败，等待下一轮清理继续维护")
			}
		})
	}
}

func TestDataRetentionCompletionLog(t *testing.T) {
	logger, buffer := newTestLogger()
	job, _, _, _, _, _, _, _ := sqliteJob(t, func(j *DataRetentionJob) {
		j.Logger = logger
	})
	if _, err := job.CleanupExpiredRetainedData(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertLogContains(t, buffer, "data_retention_cleanup_completed", "数据保留清理完成")
}

// TestDomainFailureIsolation asserts cross-domain isolation: a failing data
// retention stage never blocks the independently owned chat retention job,
// which runs against its own port in the same process.
func TestDomainFailureIsolation(t *testing.T) {
	failingData, _, _, _, _, _, _, _ := sqliteJob(t, func(j *DataRetentionJob) {
		j.Stats = &fakeStatsWriter{err: errors.New("stats writer down")}
	})
	failingData.UsageRecords = &fakeUsageRecords{script: []UsageRecordsBatch{{}}}
	chatDB := &fakeDbService{chatResult: &ChatRetentionResult{DeletedMessages: 3}}
	chat := &ChatRetentionJob{DB: chatDB, Clock: fixedClock(fixedNow()), RetentionDays: 3}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := failingData.CleanupExpiredRetainedData(context.Background())
		errs <- err
	}()
	go func() {
		defer wg.Done()
		errs <- chat.Run(context.Background())
	}()
	wg.Wait()
	close(errs)
	var dataErr, chatErr error
	for err := range errs {
		if err != nil {
			if dataErr == nil {
				dataErr = err
			} else if chatErr == nil {
				chatErr = err
			}
		}
	}
	if dataErr == nil || dataErr.Error() != "stats writer down" {
		t.Fatalf("data job error = %v, want stats writer down", dataErr)
	}
	if chatErr != nil {
		t.Fatalf("chat job must stay independent of the failing data job: %v", chatErr)
	}
	if len(chatDB.chatInputs) != 1 {
		t.Fatal("chat job must have run exactly once")
	}
}
