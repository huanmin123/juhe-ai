package retention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func validSnapshotJob(now time.Time) RecordMaintenanceJob {
	return RecordMaintenanceJob{
		Type:      JobTypeAccountUsageSnapshotUpsert,
		AccountID: "acc-1",
		Kind:      AccountUsageSnapshotKindOpenAICodex,
		Snapshot:  map[string]any{"status": "ok"},
		UpdatedAt: ISOString(now),
	}
}

func TestNormalizeRecordMaintenanceJob(t *testing.T) {
	now := fixedNow()
	tests := []struct {
		name    string
		input   RecordMaintenanceJob
		wantErr string
		wantID  bool
	}{
		{
			name:   "api key job fills id and createdAt",
			input:  RecordMaintenanceJob{Type: JobTypeAPIKeyRelatedCleanup, APIKeyID: "key-1", SystemAccountID: "sys-1"},
			wantID: true,
		},
		{
			name:   "account job fills id and createdAt",
			input:  RecordMaintenanceJob{Type: JobTypeAccountRelatedCleanup, AccountID: "acc-1", SystemAccountID: "sys-1"},
			wantID: true,
		},
		{
			name:    "invalid createdAt rejected",
			input:   RecordMaintenanceJob{Type: JobTypeAPIKeyRelatedCleanup, APIKeyID: "k", SystemAccountID: "s", CreatedAt: "not-a-time"},
			wantErr: "数据维护任务 createdAt必须是带 Z 或数值 offset 的 RFC3339 时间",
		},
		{
			name:    "usage records job requires cutoffAt",
			input:   RecordMaintenanceJob{Type: JobTypeUsageRecordsCleanup},
			wantErr: "数据维护清理 cutoffAt必须是带 Z 或数值 offset 的 RFC3339 时间",
		},
		{
			name:    "non business job requires cutoffAt",
			input:   RecordMaintenanceJob{Type: JobTypeNonBusinessDataCleanup, CutoffAt: "2026-09-04T12:00:00"},
			wantErr: "数据维护清理 cutoffAt必须是带 Z 或数值 offset 的 RFC3339 时间",
		},
		{
			name:    "snapshot job requires updatedAt",
			input:   RecordMaintenanceJob{Type: JobTypeAccountUsageSnapshotUpsert, UpdatedAt: "nope"},
			wantErr: "账号用量快照 updatedAt必须是带 Z 或数值 offset 的 RFC3339 时间",
		},
		{
			name:    "unknown type rejected",
			input:   RecordMaintenanceJob{Type: "mystery_job"},
			wantErr: "未知数据维护任务：mystery_job",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, err := NormalizeRecordMaintenanceJob(tt.input, now)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantID {
				if !strings.HasPrefix(job.ID, "recmaint_") {
					t.Fatalf("id = %q, want recmaint_ prefix", job.ID)
				}
				if job.CreatedAt != ISOString(now) {
					t.Fatalf("createdAt = %q, want the injected now", job.CreatedAt)
				}
			}
		})
	}
	t.Run("existing id and createdAt preserved", func(t *testing.T) {
		job, err := NormalizeRecordMaintenanceJob(RecordMaintenanceJob{
			Type: JobTypeAPIKeyRelatedCleanup, APIKeyID: "k", SystemAccountID: "s",
			ID: "recmaint_keep", CreatedAt: "2026-01-02T03:04:05.000Z",
		}, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if job.ID != "recmaint_keep" || job.CreatedAt != "2026-01-02T03:04:05.000Z" {
			t.Fatalf("identity not preserved: %+v", job)
		}
	})
}

func TestParseRfc3339Instant(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantOK  bool
		wantISO string
	}{
		{name: "zulu", value: "2026-09-04T20:30:00Z", wantOK: true, wantISO: "2026-09-04T20:30:00.000Z"},
		{name: "numeric offset canonicalizes", value: "2026-09-04T22:30:00+02:00", wantOK: true, wantISO: "2026-09-04T20:30:00.000Z"},
		{name: "fraction allowed", value: "2026-09-04T20:30:00.5Z", wantOK: true, wantISO: "2026-09-04T20:30:00.500Z"},
		{name: "nine digit fraction", value: "2026-09-04T20:30:00.123456789Z", wantOK: true},
		{name: "bare datetime rejected", value: "2026-09-04T20:30:00", wantOK: false},
		{name: "space separator rejected", value: "2026-09-04 20:30:00Z", wantOK: false},
		{name: "impossible month rejected", value: "2026-13-04T20:30:00Z", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, ok := parseRfc3339Instant(tt.value)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantOK && tt.wantISO != "" && ISOString(parsed) != tt.wantISO {
				t.Fatalf("canonical = %q, want %q", ISOString(parsed), tt.wantISO)
			}
		})
	}
}

// scriptedRelatedCleaner records the statsWriter handed to each call so the
// postgres-nil-callback contract is observable.
type scriptedRelatedCleaner struct {
	result  RelatedCleanupResult
	err     error
	writers []StatsWriter
}

func (s *scriptedRelatedCleaner) CleanupApiKeyRelated(_ context.Context, _ RecordMaintenanceJob, statsWriter StatsWriter) (RelatedCleanupResult, error) {
	s.writers = append(s.writers, statsWriter)
	return s.result, s.err
}

func (s *scriptedRelatedCleaner) CleanupAccountRelated(_ context.Context, _ RecordMaintenanceJob, statsWriter StatsWriter) (RelatedCleanupResult, error) {
	s.writers = append(s.writers, statsWriter)
	return s.result, s.err
}

func TestRunOnceRelatedCleanupLogPaths(t *testing.T) {
	now := fixedNow()
	tests := []struct {
		name        string
		job         RecordMaintenanceJob
		result      RelatedCleanupResult
		wantEvent   string
		wantMessage string
	}{
		{
			name:        "api key completed",
			job:         RecordMaintenanceJob{Type: JobTypeAPIKeyRelatedCleanup, APIKeyID: "k", SystemAccountID: "s"},
			result:      RelatedCleanupResult{DeletedRows: 10},
			wantEvent:   "record_maintenance_api_key_cleanup_completed",
			wantMessage: "API Key 关联数据清理完成",
		},
		{
			name:        "api key deferred on hasMore",
			job:         RecordMaintenanceJob{Type: JobTypeAPIKeyRelatedCleanup, APIKeyID: "k", SystemAccountID: "s"},
			result:      RelatedCleanupResult{DeletedRows: 5, HasMore: true},
			wantEvent:   "record_maintenance_api_key_cleanup_deferred",
			wantMessage: "API Key 关联数据清理等待统计游标追平",
		},
		{
			name:        "api key deferred on blockedReason",
			job:         RecordMaintenanceJob{Type: JobTypeAPIKeyRelatedCleanup, APIKeyID: "k", SystemAccountID: "s"},
			result:      RelatedCleanupResult{BlockedReason: "等待统计安全游标追平"},
			wantEvent:   "record_maintenance_api_key_cleanup_deferred",
			wantMessage: "API Key 关联数据清理等待统计游标追平",
		},
		{
			name:        "account completed",
			job:         RecordMaintenanceJob{Type: JobTypeAccountRelatedCleanup, AccountID: "a", SystemAccountID: "s"},
			result:      RelatedCleanupResult{DeletedRows: 3},
			wantEvent:   "record_maintenance_account_cleanup_completed",
			wantMessage: "AI 账户关联数据清理完成",
		},
		{
			name:        "account deferred",
			job:         RecordMaintenanceJob{Type: JobTypeAccountRelatedCleanup, AccountID: "a", SystemAccountID: "s"},
			result:      RelatedCleanupResult{HasMore: true, DeletedRows: 999},
			wantEvent:   "record_maintenance_account_cleanup_deferred",
			wantMessage: "AI 账户关联数据清理等待统计游标追平",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buffer := newTestLogger()
			cleaner := &scriptedRelatedCleaner{result: tt.result}
			runner := &RecordMaintenanceRunner{Mode: ModeSQLite, Clock: fixedClock(now), Logger: logger}
			runner.Executor = RecordMaintenanceExecutor{RelatedRecords: cleaner, StatsWriter: &fakeStatsWriter{}}
			if _, err := runner.RunOnce(context.Background(), tt.job); err != nil {
				t.Fatalf("RunOnce() unexpected error: %v", err)
			}
			assertLogContains(t, buffer, tt.wantEvent, tt.wantMessage)
			if len(cleaner.writers) != 1 || cleaner.writers[0] == nil {
				t.Fatal("sqlite mode must forward the stats writer")
			}
		})
	}
	t.Run("postgres mode forwards nil stats writer", func(t *testing.T) {
		logger, _ := newTestLogger()
		cleaner := &scriptedRelatedCleaner{result: RelatedCleanupResult{DeletedRows: 1}}
		runner := &RecordMaintenanceRunner{Mode: ModePostgres, Clock: fixedClock(now), Logger: logger}
		runner.Executor = RecordMaintenanceExecutor{RelatedRecords: cleaner, StatsWriter: &fakeStatsWriter{}}
		if _, err := runner.RunOnce(context.Background(), RecordMaintenanceJob{Type: JobTypeAPIKeyRelatedCleanup, APIKeyID: "k", SystemAccountID: "s"}); err != nil {
			t.Fatalf("RunOnce() unexpected error: %v", err)
		}
		if len(cleaner.writers) != 1 || cleaner.writers[0] != nil {
			t.Fatal("postgres mode must pass a nil stats writer")
		}
	})
	t.Run("cleaner error propagates", func(t *testing.T) {
		logger, _ := newTestLogger()
		cleaner := &scriptedRelatedCleaner{err: errors.New("shard locked")}
		runner := &RecordMaintenanceRunner{Mode: ModeSQLite, Clock: fixedClock(now), Logger: logger}
		runner.Executor = RecordMaintenanceExecutor{RelatedRecords: cleaner}
		if _, err := runner.RunOnce(context.Background(), RecordMaintenanceJob{Type: JobTypeAPIKeyRelatedCleanup, APIKeyID: "k", SystemAccountID: "s"}); err == nil {
			t.Fatal("expected the cleaner error to propagate")
		}
	})
}

// TestRunOnceUsageRecordsGuard24h asserts the minimum-age guard:
// 不能清理最近 1 天内的使用记录, with no store call and zero counters.
func TestRunOnceUsageRecordsGuard24h(t *testing.T) {
	now := fixedNow()
	tests := []struct {
		name      string
		cutoffAge time.Duration
		blocked   bool
	}{
		{name: "23h old cutoff is blocked", cutoffAge: 23 * time.Hour, blocked: true},
		{name: "exactly 24h old cutoff is allowed", cutoffAge: 24 * time.Hour, blocked: false},
		{name: "25h old cutoff is allowed", cutoffAge: 25 * time.Hour, blocked: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buffer := newTestLogger()
			usageRecords := &fakeUsageRecords{}
			runner := &RecordMaintenanceRunner{Mode: ModeSQLite, Clock: fixedClock(now), Logger: logger}
			runner.Executor = RecordMaintenanceExecutor{UsageRecords: usageRecords}
			cutoffAt := ISOString(now.Add(-tt.cutoffAge))
			job, err := UsageRecordsCleanupJob(cutoffAt, CleanupBatchSize, 3, now)
			if err != nil {
				t.Fatalf("job build failed: %v", err)
			}
			result, err := runner.RunOnce(context.Background(), job)
			if err != nil {
				t.Fatalf("RunOnce() unexpected error: %v", err)
			}
			if tt.blocked {
				if usageRecords.calls != 0 {
					t.Fatal("blocked cleanup must not touch the store")
				}
				if result["blockedReason"] != "不能清理最近 1 天内的使用记录" {
					t.Fatalf("blockedReason = %v", result["blockedReason"])
				}
				if result["deletedRows"] != int64(0) || result["batches"] != 0 {
					t.Fatalf("blocked result not zero: %+v", result)
				}
				return
			}
			if usageRecords.calls == 0 {
				t.Fatal("allowed cleanup must touch the store")
			}
			assertLogContains(t, buffer, "record_maintenance_usage_records_cleanup_completed", "使用记录后台清理完成")
		})
	}
}

// TestRunOnceUsageRecordsBatchLoop asserts the temporary-worker loop: no
// pauses, droppedPartitions count as changed batches, hasMore ends the loop.
func TestRunOnceUsageRecordsBatchLoop(t *testing.T) {
	now := fixedNow()
	usageRecords := &fakeUsageRecords{script: []UsageRecordsBatch{
		{DeletedRows: CleanupBatchSize, DroppedPartitions: 2, HasMore: true},
		{DeletedRows: 0, DroppedPartitions: 1, HasMore: true},
		{DeletedRows: 100, HasMore: false},
	}}
	runner := &RecordMaintenanceRunner{Mode: ModeSQLite, Clock: fixedClock(now), Logger: discardLogger()}
	runner.Executor = RecordMaintenanceExecutor{UsageRecords: usageRecords}
	job, err := UsageRecordsCleanupJob(ISOString(now.Add(-48*time.Hour)), CleanupBatchSize, 5, now)
	if err != nil {
		t.Fatalf("job build failed: %v", err)
	}
	result, err := runner.RunOnce(context.Background(), job)
	if err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if usageRecords.calls != 3 {
		t.Fatalf("calls = %d, want 3 (third batch has no hasMore)", usageRecords.calls)
	}
	if result["batches"] != 3 {
		t.Fatalf("batches = %v, want 3 (every batch changed rows or dropped partitions)", result["batches"])
	}
	if result["deletedRows"] != int64(1100) {
		t.Fatalf("deletedRows = %v, want 1100", result["deletedRows"])
	}
	if result["hasMore"] != false {
		t.Fatalf("hasMore = %v, want false", result["hasMore"])
	}
}

type scriptedNonBusinessCleaner struct {
	batches []NonBusinessDataCleanupCounts
	calls   int
}

func (s *scriptedNonBusinessCleaner) CleanupBefore(_ context.Context, cutoffAt string, _ int) (NonBusinessDataCleanupCounts, error) {
	batch := s.batches[len(s.batches)-1]
	if s.calls < len(s.batches) {
		batch = s.batches[s.calls]
	}
	s.calls++
	batch.CutoffAt = cutoffAt
	return batch, nil
}

// TestRunOnceNonBusinessDataMerge asserts the dataset+stats merge loop and
// the stop condition (!hasMore || zero deletes).
func TestRunOnceNonBusinessDataMerge(t *testing.T) {
	now := fixedNow()
	logger, buffer := newTestLogger()
	cleaner := &scriptedNonBusinessCleaner{batches: []NonBusinessDataCleanupCounts{
		{DeletedRows: 10, DeletedFiles: 2, HasMore: true, TableRows: map[string]int64{"probe_runs": 10}, FileDeletes: map[string]int64{"payloads": 2}},
		{DeletedRows: 0, DeletedFiles: 3, HasMore: false, FileDeletes: map[string]int64{"payloads": 3}},
	}}
	stats := &fakeStatsWriter{}
	runner := &RecordMaintenanceRunner{Mode: ModeSQLite, Clock: fixedClock(now), Logger: logger}
	runner.Executor = RecordMaintenanceExecutor{NonBusinessData: cleaner, StatsWriter: stats}
	job, err := NormalizeRecordMaintenanceJob(RecordMaintenanceJob{
		Type: JobTypeNonBusinessDataCleanup, CutoffAt: ISOString(now.Add(-48 * time.Hour)),
		BatchSize: 500, MaxBatches: 4,
	}, now)
	if err != nil {
		t.Fatalf("job build failed: %v", err)
	}
	result, err := runner.RunOnce(context.Background(), job)
	if err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if cleaner.calls != 2 || stats.nonBusinessCalls != 2 {
		t.Fatalf("cleaner=%d stats=%d calls, want 2/2", cleaner.calls, stats.nonBusinessCalls)
	}
	if result["deletedRows"] != int64(10) || result["deletedFiles"] != int64(5) {
		t.Fatalf("merged counters wrong: %+v", result)
	}
	if result["batches"] != 2 {
		t.Fatalf("batches = %v, want 2", result["batches"])
	}
	assertLogContains(t, buffer, "record_maintenance_non_business_data_cleanup_completed", "非业务数据后台硬清理完成")
}

func TestRunOnceSnapshotUpsert(t *testing.T) {
	now := fixedNow()
	logger, buffer := newTestLogger()
	stats := &fakeStatsWriter{}
	runner := &RecordMaintenanceRunner{Mode: ModeSQLite, Clock: fixedClock(now), Logger: logger}
	runner.Executor = RecordMaintenanceExecutor{StatsWriter: stats}
	job := validSnapshotJob(now)
	result, err := runner.RunOnce(context.Background(), job)
	if err != nil {
		t.Fatalf("RunOnce() unexpected error: %v", err)
	}
	if result["upsertedCount"] != 1 {
		t.Fatalf("result = %+v", result)
	}
	assertLogContains(t, buffer, "record_maintenance_account_usage_snapshots_upserted", "账号用量快照后台批量写入完成")
}

func TestDecodeRecordMaintenanceJob(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		wantErr string
	}{
		{
			name: "valid api key job",
			payload: map[string]any{
				"type": JobTypeAPIKeyRelatedCleanup, "apiKeyId": "k", "systemAccountId": "s",
			},
		},
		{
			name:    "missing apiKeyId rejected",
			payload: map[string]any{"type": JobTypeAPIKeyRelatedCleanup, "systemAccountId": "s"},
			wantErr: "Redis Stream 数据维护消息格式无效",
		},
		{
			name:    "unknown type rejected",
			payload: map[string]any{"type": "other"},
			wantErr: "Redis Stream 数据维护消息格式无效",
		},
		{
			name:    "usage records job needs cutoffAt",
			payload: map[string]any{"type": JobTypeUsageRecordsCleanup, "batchSize": 1000, "maxBatches": 5},
			wantErr: "Redis Stream 数据维护消息格式无效",
		},
		{
			name: "valid snapshot job",
			payload: map[string]any{
				"type": JobTypeAccountUsageSnapshotUpsert, "accountId": "a", "kind": "openai_codex",
				"snapshot": map[string]any{"status": "ok"}, "updatedAt": "2026-09-04T20:30:00Z",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job, err := DecodeRecordMaintenanceJob(mustJobJSON(t, tt.payload))
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if job.Type == "" {
				t.Fatal("decoded job lost its type")
			}
		})
	}
	t.Run("malformed json rejected", func(t *testing.T) {
		if _, err := DecodeRecordMaintenanceJob([]byte("{not json")); err == nil {
			t.Fatal("expected a decode error")
		}
	})
}
