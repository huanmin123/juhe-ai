package retention

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// scriptedRetryer captures the statsWriter and limit it was called with.
type scriptedApiKeyRetryer struct {
	summary PendingCleanupSummary
	err     error
	limits  []int
	writers []StatsWriter
}

func (s *scriptedApiKeyRetryer) CleanupPendingTargets(_ context.Context, limit int, statsWriter StatsWriter) (PendingCleanupSummary, error) {
	s.limits = append(s.limits, limit)
	s.writers = append(s.writers, statsWriter)
	return s.summary, s.err
}

type scriptedAccountRetryer struct {
	summary PendingCleanupSummary
	err     error
	limits  []int
	writers []StatsWriter
}

func (s *scriptedAccountRetryer) CleanupPendingTargets(_ context.Context, limit int, statsWriter StatsWriter) (PendingCleanupSummary, error) {
	s.limits = append(s.limits, limit)
	s.writers = append(s.writers, statsWriter)
	return s.summary, s.err
}

func TestRecordCleanupRetryLimitIsOne(t *testing.T) {
	apiKey := &scriptedApiKeyRetryer{summary: PendingCleanupSummary{Attempted: 1, Completed: 1}}
	account := &scriptedAccountRetryer{summary: PendingCleanupSummary{Attempted: 1, Completed: 1}}
	job := &RecordCleanupRetryJob{Mode: ModeSQLite, APIKey: apiKey, Account: account, Stats: &fakeStatsWriter{}}
	if err := job.RunAPIKey(context.Background()); err != nil {
		t.Fatalf("RunAPIKey() unexpected error: %v", err)
	}
	if err := job.RunAccount(context.Background()); err != nil {
		t.Fatalf("RunAccount() unexpected error: %v", err)
	}
	for _, got := range append(apiKey.limits, account.limits...) {
		if got != RecordCleanupRetryTargetLimit {
			t.Fatalf("limit = %d, want 1", got)
		}
	}
}

// TestRecordCleanupRetryLogGate mirrors the Node gate: the completion info
// log fires only when summary.attempted > 0.
func TestRecordCleanupRetryLogGate(t *testing.T) {
	tests := []struct {
		name    string
		summary PendingCleanupSummary
		wantLog bool
		event   string
		message string
	}{
		{
			name:    "no attempted target stays silent",
			summary: PendingCleanupSummary{},
		},
		{
			name:    "attempted target logs api key completion",
			summary: PendingCleanupSummary{Attempted: 1, Completed: 1, DeletedRows: 12},
			wantLog: true,
			event:   "background_api_key_record_cleanup_retry_completed",
			message: "已删除 API Key 关联数据清理重试完成",
		},
		{
			name:    "deferred target still logs",
			summary: PendingCleanupSummary{Attempted: 1, Deferred: 1},
			wantLog: true,
			event:   "background_account_record_cleanup_retry_completed",
			message: "已删除 AI 账户关联数据清理重试完成",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buffer := newTestLogger()
			job := &RecordCleanupRetryJob{
				Mode:    ModeSQLite,
				Logger:  logger,
				APIKey:  &scriptedApiKeyRetryer{summary: tt.summary},
				Account: &scriptedAccountRetryer{summary: tt.summary},
				Stats:   &fakeStatsWriter{},
			}
			if err := job.RunAPIKey(context.Background()); err != nil {
				t.Fatalf("RunAPIKey() unexpected error: %v", err)
			}
			if err := job.RunAccount(context.Background()); err != nil {
				t.Fatalf("RunAccount() unexpected error: %v", err)
			}
			contents := buffer.String()
			if !tt.wantLog {
				if contents != "" {
					t.Fatalf("silent retry must not log:\n%s", contents)
				}
				return
			}
			if !strings.Contains(contents, tt.event) || !strings.Contains(contents, tt.message) {
				t.Fatalf("log missing %q/%q:\n%s", tt.event, tt.message, contents)
			}
		})
	}
}

// TestRecordCleanupRetryModeContract asserts postgres forwards no stats
// writer while sqlite forwards the injected one.
func TestRecordCleanupRetryModeContract(t *testing.T) {
	tests := []struct {
		name    string
		mode    Mode
		wantNil bool
	}{
		{name: "sqlite forwards stats writer", mode: ModeSQLite},
		{name: "postgres forwards nil", mode: ModePostgres, wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiKey := &scriptedApiKeyRetryer{}
			account := &scriptedAccountRetryer{}
			job := &RecordCleanupRetryJob{Mode: tt.mode, APIKey: apiKey, Account: account, Stats: &fakeStatsWriter{}}
			if err := job.RunAPIKey(context.Background()); err != nil {
				t.Fatalf("RunAPIKey() unexpected error: %v", err)
			}
			if err := job.RunAccount(context.Background()); err != nil {
				t.Fatalf("RunAccount() unexpected error: %v", err)
			}
			for name, retryer := range map[string]struct {
				writers []StatsWriter
			}{"apiKey": {apiKey.writers}, "account": {account.writers}} {
				if len(retryer.writers) != 1 {
					t.Fatalf("%s not called once", name)
				}
				isNil := retryer.writers[0] == nil
				if isNil != tt.wantNil {
					t.Fatalf("%s statsWriter nil = %v, want %v", name, isNil, tt.wantNil)
				}
			}
		})
	}
}

// TestRecordCleanupRetryErrorPaths asserts failures log the exact Node events
// and messages and rethrow.
func TestRecordCleanupRetryErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		run     func(*RecordCleanupRetryJob) error
		event   string
		message string
	}{
		{
			name:    "api key retry failure",
			run:     func(j *RecordCleanupRetryJob) error { return j.RunAPIKey(context.Background()) },
			event:   "background_api_key_record_cleanup_retry_failed",
			message: "已删除 API Key 关联数据清理重试失败",
		},
		{
			name:    "account retry failure",
			run:     func(j *RecordCleanupRetryJob) error { return j.RunAccount(context.Background()) },
			event:   "background_account_record_cleanup_retry_failed",
			message: "已删除 AI 账户关联数据清理重试失败",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buffer := newTestLogger()
			job := &RecordCleanupRetryJob{
				Mode:    ModeSQLite,
				Logger:  logger,
				APIKey:  &scriptedApiKeyRetryer{err: errors.New("deduction write failed")},
				Account: &scriptedAccountRetryer{err: errors.New("deduction write failed")},
			}
			if err := tt.run(job); err == nil || err.Error() != "deduction write failed" {
				t.Fatalf("error = %v, want deduction write failed", err)
			}
			assertLogContains(t, buffer, tt.event, tt.message)
		})
	}
}

func TestRecordCleanupRetryScheduleMetadata(t *testing.T) {
	if RecordCleanupRetryInterval != time.Minute {
		t.Fatalf("retry interval = %v, want 1m", RecordCleanupRetryInterval)
	}
	if APIKeyRecordRetryInitialDelay != 24*time.Second || AccountRecordRetryInitialDelay != 42*time.Second {
		t.Fatalf("initial delays drifted: %v/%v", APIKeyRecordRetryInitialDelay, AccountRecordRetryInitialDelay)
	}
}
