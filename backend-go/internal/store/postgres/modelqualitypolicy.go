package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
)

// modelQualityPolicyQueryer is deliberately the small common surface of a
// pgx pool and transaction. Policy CAS is one SQL statement, so it does not
// need a read-modify-write transaction (which would widen the future owner
// hand-off unnecessarily).
type modelQualityPolicyQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const modelQualityPolicyColumns = `
system_account_id, revision, profile, manual_enforcement_enabled,
penalty_threshold, penalty_action, recovery_interval_minutes, created_at, updated_at`

// ReadModelQualityPolicy returns the one persisted policy for the system
// account, or the exact Node-compatible effective default when the durable row
// has not yet been created. Callers therefore never need to duplicate default
// policy values outside this adapter.
func (s *Store) ReadModelQualityPolicy(ctx context.Context, systemAccountID string) (port.ModelQualityPolicyRecord, error) {
	return readModelQualityPolicy(ctx, s.pool, systemAccountID)
}

func readModelQualityPolicy(ctx context.Context, q modelQualityPolicyQueryer, systemAccountID string) (port.ModelQualityPolicyRecord, error) {
	if err := validateModelQualityPolicySystemAccountID(systemAccountID); err != nil {
		return port.ModelQualityPolicyRecord{}, err
	}
	record, err := scanModelQualityPolicy(q.QueryRow(ctx, `
SELECT `+modelQualityPolicyColumns+`
FROM juhe_business.model_quality_policies
WHERE system_account_id = $1`, systemAccountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ModelQualityPolicyRecord{
			Policy:    modelquality.DefaultPolicy(systemAccountID),
			Persisted: false,
		}, nil
	}
	if err != nil {
		return port.ModelQualityPolicyRecord{}, fmt.Errorf("read model quality policy: %w", err)
	}
	return record, nil
}

// SaveModelQualityPolicy atomically creates the first policy only from the
// implicit default revision (zero), or updates an existing policy only when
// the durable revision matches. A losing writer receives the current effective
// policy as a conflict result and never overwrites the winner.
func (s *Store) SaveModelQualityPolicy(ctx context.Context, input port.ModelQualityPolicySaveInput) (port.ModelQualityPolicySaveResult, error) {
	return saveModelQualityPolicy(ctx, s.pool, input)
}

func saveModelQualityPolicy(ctx context.Context, q modelQualityPolicyQueryer, input port.ModelQualityPolicySaveInput) (port.ModelQualityPolicySaveResult, error) {
	if err := validateModelQualityPolicySaveInput(input); err != nil {
		return port.ModelQualityPolicySaveResult{}, err
	}

	var (
		record port.ModelQualityPolicyRecord
		err    error
	)
	if input.ExpectedRevision == 0 {
		record, err = scanModelQualityPolicy(q.QueryRow(ctx, `
INSERT INTO juhe_business.model_quality_policies (
  system_account_id, revision, profile, manual_enforcement_enabled,
  penalty_threshold, penalty_action, recovery_interval_minutes, created_at, updated_at
) VALUES ($1, 1, $2, $3, $4, $5, $6, $7, $7)
ON CONFLICT (system_account_id) DO NOTHING
RETURNING `+modelQualityPolicyColumns,
			input.SystemAccountID,
			string(input.Profile),
			modelQualityPolicyBoolInt(input.ManualEnforcementEnabled),
			input.PenaltyThreshold,
			string(input.PenaltyAction),
			int(input.RecoveryInterval/time.Minute),
			modelQualityPolicyTimeText(input.UpdatedAt),
		))
	} else {
		record, err = scanModelQualityPolicy(q.QueryRow(ctx, `
UPDATE juhe_business.model_quality_policies
SET revision = revision + 1,
    profile = $2,
    manual_enforcement_enabled = $3,
    penalty_threshold = $4,
    penalty_action = $5,
    recovery_interval_minutes = $6,
    updated_at = $7
WHERE system_account_id = $1 AND revision = $8
RETURNING `+modelQualityPolicyColumns,
			input.SystemAccountID,
			string(input.Profile),
			modelQualityPolicyBoolInt(input.ManualEnforcementEnabled),
			input.PenaltyThreshold,
			string(input.PenaltyAction),
			int(input.RecoveryInterval/time.Minute),
			modelQualityPolicyTimeText(input.UpdatedAt),
			int64(input.ExpectedRevision),
		))
	}
	if err == nil {
		return port.ModelQualityPolicySaveResult{Status: port.ModelQualityPolicySaved, Policy: record}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return port.ModelQualityPolicySaveResult{}, fmt.Errorf("save model quality policy: %w", err)
	}

	current, readErr := readModelQualityPolicy(ctx, q, input.SystemAccountID)
	if readErr != nil {
		return port.ModelQualityPolicySaveResult{}, fmt.Errorf("read model quality policy after CAS conflict: %w", readErr)
	}
	return port.ModelQualityPolicySaveResult{Status: port.ModelQualityPolicyConflict, Policy: current}, nil
}

type modelQualityPolicyScanner interface{ Scan(...any) error }

func scanModelQualityPolicy(row modelQualityPolicyScanner) (port.ModelQualityPolicyRecord, error) {
	var (
		systemAccountID string
		revision        int64
		profile         string
		manualEnabled   int64
		threshold       int64
		action          string
		recoveryMinutes int64
		createdRaw      string
		updatedRaw      string
	)
	if err := row.Scan(
		&systemAccountID, &revision, &profile, &manualEnabled, &threshold,
		&action, &recoveryMinutes, &createdRaw, &updatedRaw,
	); err != nil {
		return port.ModelQualityPolicyRecord{}, err
	}
	if err := validateModelQualityPolicySystemAccountID(systemAccountID); err != nil {
		return port.ModelQualityPolicyRecord{}, fmt.Errorf("invalid persisted model quality policy system account ID: %w", err)
	}
	if revision < 1 || revision > math.MaxInt32 {
		return port.ModelQualityPolicyRecord{}, fmt.Errorf("invalid persisted model quality policy revision %d", revision)
	}
	if manualEnabled != 0 && manualEnabled != 1 {
		return port.ModelQualityPolicyRecord{}, fmt.Errorf("invalid persisted model quality policy manual_enforcement_enabled %d", manualEnabled)
	}
	if threshold < 40 || threshold > 100 || recoveryMinutes < 10 || recoveryMinutes > 10080 {
		return port.ModelQualityPolicyRecord{}, fmt.Errorf("invalid persisted model quality policy numeric range")
	}
	createdAt, err := modelQualityPolicyParseTime(createdRaw)
	if err != nil {
		return port.ModelQualityPolicyRecord{}, fmt.Errorf("parse persisted model quality policy created_at: %w", err)
	}
	updatedAt, err := modelQualityPolicyParseTime(updatedRaw)
	if err != nil {
		return port.ModelQualityPolicyRecord{}, fmt.Errorf("parse persisted model quality policy updated_at: %w", err)
	}
	policy := modelquality.Policy{
		SystemAccountID:          systemAccountID,
		Revision:                 modelquality.PolicyRevision(revision),
		Profile:                  modelquality.Profile(profile),
		ManualEnforcementEnabled: manualEnabled == 1,
		PenaltyThreshold:         int(threshold),
		PenaltyAction:            modelquality.Action(action),
		RecoveryIntervalMinutes:  int(recoveryMinutes),
	}
	if err := policy.Validate(); err != nil {
		return port.ModelQualityPolicyRecord{}, fmt.Errorf("invalid persisted model quality policy: %w", err)
	}
	return port.ModelQualityPolicyRecord{
		Policy:    policy,
		Persisted: true,
		CreatedAt: &createdAt,
		UpdatedAt: &updatedAt,
	}, nil
}

func validateModelQualityPolicySaveInput(input port.ModelQualityPolicySaveInput) error {
	if err := validateModelQualityPolicySystemAccountID(input.SystemAccountID); err != nil {
		return err
	}
	if input.ExpectedRevision >= modelquality.PolicyRevision(math.MaxInt32) {
		return fmt.Errorf("model quality policy expected revision is outside writable PostgreSQL INTEGER range")
	}
	if input.UpdatedAt.IsZero() {
		return fmt.Errorf("model quality policy updated time is required")
	}
	if input.RecoveryInterval < 10*time.Minute || input.RecoveryInterval > 10080*time.Minute || input.RecoveryInterval%time.Minute != 0 {
		return fmt.Errorf("model quality policy recovery interval must be whole minutes from 10 to 10080")
	}
	policy := modelquality.Policy{
		SystemAccountID:          input.SystemAccountID,
		Profile:                  input.Profile,
		ManualEnforcementEnabled: input.ManualEnforcementEnabled,
		PenaltyThreshold:         input.PenaltyThreshold,
		PenaltyAction:            input.PenaltyAction,
		RecoveryIntervalMinutes:  int(input.RecoveryInterval / time.Minute),
	}
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("invalid model quality policy save input: %w", err)
	}
	return nil
}

func validateModelQualityPolicySystemAccountID(value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return fmt.Errorf("model quality policy system account ID is invalid")
	}
	return nil
}

func modelQualityPolicyTimeText(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func modelQualityPolicyParseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func modelQualityPolicyBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

var _ port.ModelQualityPolicyReader = (*Store)(nil)
var _ port.ModelQualityPolicyWriter = (*Store)(nil)
