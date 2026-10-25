package retention

import (
	"context"
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

func TestRecordCleanupRetryScheduleMetadata(t *testing.T) {
	if RecordCleanupRetryInterval != time.Minute {
		t.Fatalf("retry interval = %v, want 1m", RecordCleanupRetryInterval)
	}
	if APIKeyRecordRetryInitialDelay != 24*time.Second || AccountRecordRetryInitialDelay != 42*time.Second {
		t.Fatalf("initial delays drifted: %v/%v", APIKeyRecordRetryInitialDelay, AccountRecordRetryInitialDelay)
	}
}
