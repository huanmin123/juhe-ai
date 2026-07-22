package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
)

func TestAPIKeyRecordCleanupSQLClaimsOnlyBoundedTargetsAndPreservesCreatedAt(t *testing.T) {
	wants := []string{
		"FROM juhe_dataset.api_key_record_cleanup_targets",
		"ORDER BY COALESCE(last_attempt_at, created_at) ASC, created_at ASC, api_key_id ASC",
		"LIMIT $1",
		"FOR UPDATE SKIP LOCKED",
		"attempt_count = target.attempt_count + 1",
		"last_attempt_at = $2",
		"last_blocked_reason = $3",
		"last_error_message = NULL",
		"updated_at = $2",
	}
	for _, want := range wants {
		if !strings.Contains(runAPIKeyRecordCleanupOnceSQL, want) {
			t.Fatalf("cleanup SQL missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"SET created_at",
		"juhe_usage.usage_records",
		"juhe_dataset.audit_logs",
		"juhe_stats.usage_record_cleanup_deductions",
		"DELETE FROM",
	} {
		if strings.Contains(runAPIKeyRecordCleanupOnceSQL, forbidden) {
			t.Fatalf("cleanup SQL must not contain %q", forbidden)
		}
	}
}

func TestRunAPIKeyRecordCleanupOnceCommitsAtomicDeferredState(t *testing.T) {
	now := time.Date(2026, 7, 22, 9, 30, 0, 0, time.UTC)
	tx := &apiKeyRecordCleanupTxStub{}
	input := port.APIKeyRecordCleanupRunInput{
		Limit:         2,
		AttemptedAt:   now,
		BlockedReason: "schema contract unavailable",
	}

	result, err := runAPIKeyRecordCleanupOnceInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		func(_ context.Context, gotTx pgx.Tx, gotInput port.APIKeyRecordCleanupRunInput) (int64, error) {
			if gotTx != tx || gotInput != input {
				t.Fatalf("run input tx=%v input=%+v", gotTx, gotInput)
			}
			return 2, nil
		},
		input,
	)
	if err != nil {
		t.Fatalf("runAPIKeyRecordCleanupOnceInTx() error = %v", err)
	}
	if result.Attempted != 2 || result.Deferred != 2 {
		t.Fatalf("result = %+v", result)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("commit=%d rollback=%d", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestRunAPIKeyRecordCleanupOnceRollsBackRetryably(t *testing.T) {
	wantErr := errors.New("defer target failed")
	tx := &apiKeyRecordCleanupTxStub{}

	result, err := runAPIKeyRecordCleanupOnceInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		func(context.Context, pgx.Tx, port.APIKeyRecordCleanupRunInput) (int64, error) {
			return 0, wantErr
		},
		port.APIKeyRecordCleanupRunInput{Limit: 1, AttemptedAt: time.Now(), BlockedReason: "blocked"},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if result != (port.APIKeyRecordCleanupRunResult{}) {
		t.Fatalf("result = %+v, want zero", result)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("commit=%d rollback=%d", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestValidateAPIKeyRecordCleanupRunInputRejectsLimitAbovePortBound(t *testing.T) {
	err := validateAPIKeyRecordCleanupRunInput(port.APIKeyRecordCleanupRunInput{
		Limit:         port.MaxAPIKeyRecordCleanupTargetLimit + 1,
		AttemptedAt:   time.Now(),
		BlockedReason: "blocked",
	})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("validateAPIKeyRecordCleanupRunInput() error = %v", err)
	}
}

type apiKeyRecordCleanupTxStub struct {
	pgx.Tx
	commitCalls   int
	rollbackCalls int
	commitErr     error
}

func (s *apiKeyRecordCleanupTxStub) Commit(context.Context) error {
	s.commitCalls++
	return s.commitErr
}

func (s *apiKeyRecordCleanupTxStub) Rollback(context.Context) error {
	s.rollbackCalls++
	return nil
}

var _ pgx.Tx = (*apiKeyRecordCleanupTxStub)(nil)
