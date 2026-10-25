package retention

import (
	"context"
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
