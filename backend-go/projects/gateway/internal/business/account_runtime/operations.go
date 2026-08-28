package accountruntime

import (
	"context"
	"database/sql"
	"time"
)

// ManifestOperations is the exact operation set assigned to this package.
// It is evidence for review only; this package does not update the capability
// manifest or close the owner handoff by itself.
var ManifestOperations = []string{
	"account_api_key_pool_probe_cursor",
	"apply_account_error_handling",
	"check_api_key_quota",
	"clear_account_failure_state",
	"clear_account_stream_failure_state",
	"defer_account_api_key_probe",
	"list_account_api_key_runtime_states_due_for_probe",
	"mark_account_exception",
	"mark_account_precheck_temporary_unavailable",
	"mark_account_temporary_unavailable",
	"read_api_key_quota_costs",
	"record_account_api_key_failure",
	"record_account_api_key_success",
	"record_account_stream_failure",
	"sync_api_key_availability_schedule_statuses",
	"validate_gateway_api_key",
}

// CoveredManifestOperations names the operation methods present in this
// package.  They are not a statement that all handoff gates are complete.
var CoveredManifestOperations = append([]string(nil), ManifestOperations...)

// OutstandingManifestOperations are dependencies intentionally kept
// fail-closed until their owners are migrated and wired by the integration
// owner.  In particular, quota usage is written by the stats owner and probe
// credentials are decrypted by the account owner.
var OutstandingManifestOperations = []string{
	"check_api_key_quota/read_api_key_quota_costs: stats/usage writer is not migrated; requires QuotaUsagePort",
	"list_account_api_key_runtime_states_due_for_probe: credential decrypt/key-pool resolver is not migrated; requires CredentialResolver",
	"sync_api_key_availability_schedule_statuses: schedule parser/evaluator is not migrated; requires ScheduleEvaluator",
	"apply_account_error_handling: account-health projection/outcome writer remains outside this package",
}

// The following aliases use the operation spelling used by the Node
// db-service contract.  The acronym-style methods in runtime.go remain the
// canonical Go API, while these wrappers make the mapping explicit for future
// gateway wiring without registering any handler here.
func (s *Store) CheckApiKeyQuota(ctx context.Context, key GatewayAPIKey) (QuotaDecision, error) {
	return s.CheckAPIKeyQuota(ctx, key)
}

func (s *Store) ReadApiKeyQuotaCosts(ctx context.Context, key GatewayAPIKey) (QuotaCosts, error) {
	return s.ReadAPIKeyQuotaCosts(ctx, key)
}

func (s *Store) ListAccountApiKeyRuntimeStatesDueForProbe(ctx context.Context, limit int) ([]ProbeCandidate, error) {
	return s.ListAccountAPIKeyRuntimeStatesDueForProbe(ctx, limit)
}

func (s *Store) RecordAccountApiKeyFailure(ctx context.Context, account Account, input FailureInput) (MutationResult, error) {
	return s.RecordAccountAPIKeyFailure(ctx, account, input)
}

func (s *Store) RecordAccountApiKeySuccess(ctx context.Context, account Account, input SuccessInput) (MutationResult, error) {
	return s.RecordAccountAPIKeySuccess(ctx, account, input)
}

func (s *Store) DeferAccountApiKeyProbe(ctx context.Context, account Account, input ProbeDeferInput) (MutationResult, error) {
	return s.DeferAccountAPIKeyProbe(ctx, account, input)
}

func (s *Store) SyncApiKeyAvailabilityScheduleStatuses(ctx context.Context) (MutationResult, error) {
	return s.SyncAPIKeyAvailabilityScheduleStatuses(ctx)
}

func (s *Store) ReadAccountAPIKeyPoolProbeCursor(ctx context.Context, accountID string, purpose CursorPurpose) (ProbeCursor, error) {
	result, err := s.AccountAPIKeyPoolProbeCursor(ctx, ProbeCursor{AccountID: accountID, Purpose: purpose}, "read")
	if err != nil {
		return ProbeCursor{}, err
	}
	if result.Cursor == nil {
		return ProbeCursor{}, sql.ErrNoRows
	}
	return *result.Cursor, nil
}

func (s *Store) SaveAccountAPIKeyPoolProbeCursor(ctx context.Context, input ProbeCursor) (ProbeCursor, error) {
	result, err := s.AccountAPIKeyPoolProbeCursor(ctx, input, "save")
	if err != nil {
		return ProbeCursor{}, err
	}
	if result.Cursor != nil {
		return *result.Cursor, nil
	}
	input.UpdatedAt = nowString(s.clock())
	return input, nil
}

func (s *Store) DeleteAccountAPIKeyPoolProbeCursor(ctx context.Context, accountID string, purpose CursorPurpose) error {
	_, err := s.AccountAPIKeyPoolProbeCursor(ctx, ProbeCursor{AccountID: accountID, Purpose: purpose}, "delete")
	return err
}

func (s *Store) MarkAccountTestTemporaryUnavailable(ctx context.Context, account Account, reason, traceID string, configRevision int64, checkedAt string, failureCount int, observedAt string) (MutationResult, error) {
	if configRevision < 1 || failureCount < 0 {
		return MutationResult{}, ErrInvalidInput
	}
	if _, err := parseTime(checkedAt); err != nil {
		return MutationResult{}, err
	}
	if _, err := parseTime(observedAt); err != nil {
		return MutationResult{}, err
	}
	if account.Status != "active" && account.Status != "temporary_unavailable" && account.Status != "rate_limited" {
		return MutationResult{}, nil
	}
	where := " AND config_revision=? AND last_health_check_at=? AND health_check_failure_count=? AND (last_health_success_at IS NULL OR last_health_success_at<?)"
	args := []any{configRevision, checkedAt, failureCount, observedAt}
	if account.Status != "" {
		where += " AND status=?"
		args = append(args, account.Status)
	}
	now := s.clock()
	set := "status='temporary_unavailable',schedulable=1,cooldown_until=?,last_error_code='test_temporary_unavailable',last_error_message=?,last_error_trace_id=?,cooldown_retest_observation_started_at=?,cooldown_retest_generation=?,stream_failure_count=0,stream_failure_window_started_at=NULL"
	return s.updateAccount(ctx, account.ID, set, []any{now.Add(time.Minute).Format(time.RFC3339Nano), sanitize(reason), sanitize(traceID), nowString(now), "account-runtime-" + randomToken()}, where, args)
}
