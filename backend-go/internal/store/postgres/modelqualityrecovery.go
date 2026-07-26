package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/modules/apikeyschedule"
	"juhe-ai/backend-go/internal/store/port"
)

type modelQualityRecoveryTokenGenerator func() (string, error)

func (s *Store) ClaimDueModelQualityRecoveries(ctx context.Context, input port.ModelQualityRecoveryClaimInput) ([]port.ModelQualityRecoveryClaim, error) {
	return claimDueModelQualityRecoveries(ctx, s.pool.BeginTx, input, func() (string, error) {
		value, err := uuid.NewRandom()
		if err != nil {
			return "", err
		}
		return "mqr_claim_" + strings.ReplaceAll(value.String(), "-", ""), nil
	})
}

func claimDueModelQualityRecoveries(
	ctx context.Context,
	beginTx modelQualityScheduleBeginTx,
	input port.ModelQualityRecoveryClaimInput,
	newToken modelQualityRecoveryTokenGenerator,
) ([]port.ModelQualityRecoveryClaim, error) {
	input = normalizeModelQualityRecoveryClaimInput(input)
	if err := validateModelQualityRecoveryClaimInput(input); err != nil {
		return nil, err
	}
	if newToken == nil {
		return nil, fmt.Errorf("model quality recovery token generator is required")
	}
	tx, err := beginModelQualityScheduleTx(ctx, beginTx, "recovery claim")
	if err != nil {
		return nil, err
	}
	committed := false
	defer rollbackModelQualityScheduleTx(tx, &committed)()

	rows, err := tx.Query(ctx, claimDueModelQualityRecoveryCandidatesSQL, modelQualityPolicyTimeText(input.Now), input.Limit)
	if err != nil {
		return nil, fmt.Errorf("select due model quality recoveries: %w", err)
	}
	type candidate struct {
		enforcement     port.ModelQualityEnforcementRecord
		model           string
		accountRevision modelquality.AccountRevision
	}
	candidates := make([]candidate, 0, input.Limit)
	for rows.Next() {
		enforcement, model, accountRevision, scanErr := scanModelQualityRecoveryCandidate(rows)
		if scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("scan due model quality recovery: %w", scanErr)
		}
		candidates = append(candidates, candidate{enforcement: enforcement, model: model, accountRevision: accountRevision})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read due model quality recoveries: %w", err)
	}
	rows.Close()

	claims := make([]port.ModelQualityRecoveryClaim, 0, len(candidates))
	policyCache := make(map[string]port.ModelQualityPolicyRecord, len(candidates))
	for _, candidate := range candidates {
		policy, ok := policyCache[candidate.enforcement.SystemAccountID]
		if !ok {
			policy, err = readModelQualityPolicy(ctx, tx, candidate.enforcement.SystemAccountID)
			if err != nil {
				return nil, fmt.Errorf("read model quality recovery policy: %w", err)
			}
			policyCache[candidate.enforcement.SystemAccountID] = policy
		}
		current := modelQualityEnforcementState(candidate.enforcement)
		account := modelquality.Account{
			ID: candidate.enforcement.AccountID, SystemAccountID: candidate.enforcement.SystemAccountID,
			Status: modelquality.AccountStatusQualityIsolated, ConfigRevision: candidate.accountRevision, OwnPhysical: true,
		}
		plan, err := modelquality.ClaimRecovery(modelquality.RecoveryClaimRequest{
			PolicyRevision: policy.Policy.Revision,
			Enforcement:    candidate.enforcement.Token,
		}, policy.Policy.Revision, current, account)
		if err != nil {
			return nil, fmt.Errorf("plan model quality recovery claim: %w", err)
		}
		if plan.Result != modelquality.RecoveryClaimed {
			continue
		}
		tokenValue, err := newToken()
		if err != nil {
			return nil, fmt.Errorf("generate model quality recovery token: %w", err)
		}
		token := port.ModelQualityRecoveryClaimToken(tokenValue)
		if !validModelQualityScheduleText(string(token), 256) {
			return nil, fmt.Errorf("generated model quality recovery token is invalid")
		}
		command, err := tx.Exec(ctx, claimModelQualityRecoverySQL,
			string(input.OwnerID), string(token), modelQualityPolicyTimeText(input.LeaseUntil),
			int64(plan.State.AccountRevision), modelQualityPolicyTimeText(input.Now),
			candidate.enforcement.AccountID, candidate.enforcement.Token.ID,
			int64(candidate.enforcement.Token.Generation), int64(policy.Policy.Revision),
		)
		if err != nil {
			return nil, fmt.Errorf("claim model quality recovery for account %q: %w", candidate.enforcement.AccountID, err)
		}
		if command.RowsAffected() != 1 {
			continue
		}
		lease := port.ModelQualityRecoveryLease{OwnerID: input.OwnerID, ClaimToken: token, Until: input.LeaseUntil.UTC()}
		candidate.enforcement.AccountConfigRevision = plan.State.AccountRevision
		candidate.enforcement.RecoveryLease = &lease
		candidate.enforcement.UpdatedAt = input.Now.UTC()
		claims = append(claims, port.ModelQualityRecoveryClaim{
			AccountID: candidate.enforcement.AccountID, SystemAccountID: candidate.enforcement.SystemAccountID,
			Model: candidate.model, Policy: policy, ExpectedAccountConfigRevision: plan.State.AccountRevision,
			Enforcement: candidate.enforcement, Lease: lease,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit model quality recovery claim: %w", err)
	}
	committed = true
	return claims, nil
}

func (s *Store) CompleteModelQualityRecovery(ctx context.Context, input port.ModelQualityRecoveryCompleteInput) (port.ModelQualityRecoveryCompleteResult, error) {
	return completeModelQualityRecovery(ctx, s.pool.BeginTx, input)
}

func completeModelQualityRecovery(ctx context.Context, beginTx modelQualityScheduleBeginTx, input port.ModelQualityRecoveryCompleteInput) (port.ModelQualityRecoveryCompleteResult, error) {
	if err := validateModelQualityRecoveryCompleteInput(input); err != nil {
		return port.ModelQualityRecoveryCompleteResult{}, err
	}
	tx, err := beginModelQualityScheduleTx(ctx, beginTx, "recovery completion")
	if err != nil {
		return port.ModelQualityRecoveryCompleteResult{}, err
	}
	committed := false
	defer rollbackModelQualityScheduleTx(tx, &committed)()

	var systemAccountID string
	err = tx.QueryRow(ctx, findModelQualityRecoveryScopeSQL,
		input.AccountID, input.ExpectedEnforcement.ID, int64(input.ExpectedEnforcement.Generation),
		string(input.Lease.OwnerID), string(input.Lease.ClaimToken), modelQualityPolicyTimeText(input.Lease.Until),
		modelQualityPolicyTimeText(input.CompletedAt),
	).Scan(&systemAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return commitModelQualityRecoveryResult(ctx, tx, &committed, port.ModelQualityRecoveryCompleteResult{Status: port.ModelQualityRecoveryStale})
	}
	if err != nil {
		return port.ModelQualityRecoveryCompleteResult{}, fmt.Errorf("find model quality recovery scope: %w", err)
	}

	account, found, err := lockModelQualityRecoveryAccount(ctx, tx, input.AccountID, systemAccountID)
	if err != nil {
		return port.ModelQualityRecoveryCompleteResult{}, err
	}
	if !found {
		return commitModelQualityRecoveryResult(ctx, tx, &committed, port.ModelQualityRecoveryCompleteResult{Status: port.ModelQualityRecoveryStale})
	}
	enforcement, found, err := lockModelQualityRecoveryEnforcement(ctx, tx, input)
	if err != nil {
		return port.ModelQualityRecoveryCompleteResult{}, err
	}
	if !found {
		return commitModelQualityRecoveryResult(ctx, tx, &committed, recoveryStaleResult(account.Status))
	}
	policy, err := readModelQualityPolicy(ctx, tx, enforcement.SystemAccountID)
	if err != nil {
		return port.ModelQualityRecoveryCompleteResult{}, fmt.Errorf("read model quality recovery completion policy: %w", err)
	}
	availableNow := false
	if input.Passed && input.ExpectedPolicyRevision == policy.Policy.Revision &&
		input.ExpectedAccountConfigRevision == enforcement.AccountConfigRevision &&
		enforcement.AccountConfigRevision == account.ConfigRevision &&
		account.Status == modelquality.AccountStatusQualityIsolated {
		availableNow, err = modelQualityRecoveryAvailable(account.AvailabilityScheduleJSON, input.CompletedAt)
		if err != nil {
			return port.ModelQualityRecoveryCompleteResult{}, err
		}
	}
	plan, err := modelquality.PlanRecovery(modelquality.RecoveryRequest{
		PolicyRevision: input.ExpectedPolicyRevision, AccountRevision: input.ExpectedAccountConfigRevision,
		Enforcement: input.ExpectedEnforcement, Passed: input.Passed,
	}, policy.Policy.Revision, modelQualityEnforcementState(enforcement), account.Account, availableNow)
	if err != nil {
		return port.ModelQualityRecoveryCompleteResult{}, fmt.Errorf("plan model quality recovery completion: %w", err)
	}

	before := account.Status
	if plan.NeedsReschedule {
		next, err := addModelQualityScheduleInterval(input.CompletedAt, input.RecoveryInterval)
		if err != nil {
			return port.ModelQualityRecoveryCompleteResult{}, err
		}
		command, err := tx.Exec(ctx, rescheduleModelQualityRecoverySQL,
			input.RunID, modelQualityPolicyTimeText(next), modelQualityPolicyTimeText(input.CompletedAt),
			input.AccountID, input.ExpectedEnforcement.ID, int64(input.ExpectedEnforcement.Generation),
			int64(enforcement.AccountConfigRevision), string(input.Lease.OwnerID), string(input.Lease.ClaimToken),
			modelQualityPolicyTimeText(input.Lease.Until), int64(policy.Policy.Revision),
		)
		if err != nil {
			return port.ModelQualityRecoveryCompleteResult{}, fmt.Errorf("reschedule model quality recovery: %w", err)
		}
		if command.RowsAffected() != 1 {
			return port.ModelQualityRecoveryCompleteResult{}, fmt.Errorf("locked model quality recovery was not rescheduled")
		}
		status := port.ModelQualityRecoveryStale
		if plan.Result == modelquality.RecoveryKeptIsolated {
			status = port.ModelQualityRecoveryKeptIsolated
		}
		return commitModelQualityRecoveryResult(ctx, tx, &committed, port.ModelQualityRecoveryCompleteResult{
			Status: status, BeforeStatus: &before, AfterStatus: &before, NextRecoveryAt: &next,
		})
	}
	if plan.Result != modelquality.RecoveryRecovered || plan.TargetSchedulable == nil {
		return commitModelQualityRecoveryResult(ctx, tx, &committed, recoveryStaleResult(account.Status))
	}
	command, err := tx.Exec(ctx, recoverModelQualityAccountSQL,
		string(plan.TargetStatus), *plan.TargetSchedulable, input.CompletedAt.UTC(),
		input.AccountID, enforcement.SystemAccountID, int64(account.ConfigRevision),
	)
	if err != nil {
		return port.ModelQualityRecoveryCompleteResult{}, fmt.Errorf("recover model quality account: %w", err)
	}
	if command.RowsAffected() != 1 {
		return port.ModelQualityRecoveryCompleteResult{}, fmt.Errorf("locked model quality account was not recovered")
	}
	command, err = tx.Exec(ctx, clearModelQualityRecoveryEnforcementSQL,
		input.RunID, modelQualityPolicyTimeText(input.CompletedAt), input.AccountID,
		input.ExpectedEnforcement.ID, int64(input.ExpectedEnforcement.Generation),
		int64(enforcement.AccountConfigRevision), string(input.Lease.OwnerID), string(input.Lease.ClaimToken),
		modelQualityPolicyTimeText(input.Lease.Until), int64(policy.Policy.Revision),
	)
	if err != nil {
		return port.ModelQualityRecoveryCompleteResult{}, fmt.Errorf("clear model quality recovery enforcement: %w", err)
	}
	if command.RowsAffected() != 1 {
		return port.ModelQualityRecoveryCompleteResult{}, fmt.Errorf("recovered model quality enforcement was not cleared")
	}
	after := plan.TargetStatus
	return commitModelQualityRecoveryResult(ctx, tx, &committed, port.ModelQualityRecoveryCompleteResult{
		Status: port.ModelQualityRecoveryRecovered, BeforeStatus: &before, AfterStatus: &after,
	})
}

type modelQualityRecoveryAccount struct {
	modelquality.Account
	AvailabilityScheduleJSON *string
}

func lockModelQualityRecoveryAccount(ctx context.Context, tx pgx.Tx, accountID, systemAccountID string) (modelQualityRecoveryAccount, bool, error) {
	var status string
	var revision int64
	var schedule pgtype.Text
	err := tx.QueryRow(ctx, lockModelQualityRecoveryAccountSQL, accountID, systemAccountID).Scan(&status, &revision, &schedule)
	if errors.Is(err, pgx.ErrNoRows) {
		return modelQualityRecoveryAccount{}, false, nil
	}
	if err != nil {
		return modelQualityRecoveryAccount{}, false, fmt.Errorf("lock model quality recovery account: %w", err)
	}
	account := modelQualityRecoveryAccount{Account: modelquality.Account{
		ID: accountID, SystemAccountID: systemAccountID, Status: modelquality.AccountStatus(status),
		ConfigRevision: modelquality.AccountRevision(revision), OwnPhysical: true,
	}}
	if schedule.Valid {
		account.AvailabilityScheduleJSON = &schedule.String
	}
	if revision < 1 || revision > math.MaxInt32 {
		return modelQualityRecoveryAccount{}, false, fmt.Errorf("invalid model quality recovery account revision %d", revision)
	}
	if err := account.Account.Validate(); err != nil {
		return modelQualityRecoveryAccount{}, false, fmt.Errorf("invalid model quality recovery account: %w", err)
	}
	return account, true, nil
}

func lockModelQualityRecoveryEnforcement(ctx context.Context, tx pgx.Tx, input port.ModelQualityRecoveryCompleteInput) (port.ModelQualityEnforcementRecord, bool, error) {
	record, err := scanModelQualityEnforcement(tx.QueryRow(ctx, lockModelQualityRecoveryEnforcementSQL,
		input.AccountID, input.ExpectedEnforcement.ID, int64(input.ExpectedEnforcement.Generation),
		string(input.Lease.OwnerID), string(input.Lease.ClaimToken), modelQualityPolicyTimeText(input.Lease.Until),
		modelQualityPolicyTimeText(input.CompletedAt),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ModelQualityEnforcementRecord{}, false, nil
	}
	if err != nil {
		return port.ModelQualityEnforcementRecord{}, false, fmt.Errorf("lock model quality recovery enforcement: %w", err)
	}
	return record, true, nil
}

func scanModelQualityRecoveryCandidate(row modelQualityScheduleScanner) (port.ModelQualityEnforcementRecord, string, modelquality.AccountRevision, error) {
	var model string
	var accountRevision int64
	record, err := scanModelQualityEnforcementWithTail(row, &model, &accountRevision)
	if err != nil {
		return port.ModelQualityEnforcementRecord{}, "", 0, err
	}
	if record.RecoveryDueAt == nil || !validModelQualityScheduleText(model, 4096) || accountRevision < 1 || accountRevision > math.MaxInt32 {
		return port.ModelQualityEnforcementRecord{}, "", 0, fmt.Errorf("invalid model quality recovery candidate")
	}
	return record, model, modelquality.AccountRevision(accountRevision), nil
}

func scanModelQualityEnforcement(row modelQualityScheduleScanner) (port.ModelQualityEnforcementRecord, error) {
	return scanModelQualityEnforcementWithTail(row)
}

func scanModelQualityEnforcementWithTail(row modelQualityScheduleScanner, tail ...any) (port.ModelQualityEnforcementRecord, error) {
	var (
		accountID, systemAccountID, enforcementID, state, action, triggerRunID string
		generation, policyRevision, accountRevision                            int64
		beforeStatus, afterStatus                                              string
		fallbackWasEnabled, superPriorityWasEnabled                            int64
		recoveryDueAt, leaseOwner, leaseToken, leaseUntil                      pgtype.Text
		lastRecoveryRunID, clearedAt                                           pgtype.Text
		startedAt, updatedAt                                                   string
	)
	destinations := []any{
		&accountID, &systemAccountID, &enforcementID, &generation, &state, &action,
		&triggerRunID, &policyRevision, &accountRevision, &beforeStatus, &afterStatus,
		&fallbackWasEnabled, &superPriorityWasEnabled, &recoveryDueAt, &leaseOwner,
		&leaseToken, &leaseUntil, &lastRecoveryRunID, &startedAt, &clearedAt, &updatedAt,
	}
	destinations = append(destinations, tail...)
	if err := row.Scan(destinations...); err != nil {
		return port.ModelQualityEnforcementRecord{}, err
	}
	if !validModelQualityScheduleText(accountID, 256) || !validModelQualityScheduleText(systemAccountID, 256) ||
		!validModelQualityScheduleText(enforcementID, 256) || !validModelQualityScheduleText(triggerRunID, 256) ||
		generation < 1 || generation > math.MaxInt32 || policyRevision < 0 || policyRevision > math.MaxInt32 ||
		accountRevision < 1 || accountRevision > math.MaxInt32 || fallbackWasEnabled < 0 || fallbackWasEnabled > 1 ||
		superPriorityWasEnabled < 0 || superPriorityWasEnabled > 1 {
		return port.ModelQualityEnforcementRecord{}, fmt.Errorf("invalid persisted model quality enforcement identity or numeric range")
	}
	record := port.ModelQualityEnforcementRecord{
		AccountID: accountID, SystemAccountID: systemAccountID,
		Token: modelquality.EnforcementToken{ID: enforcementID, Generation: modelquality.EnforcementGeneration(generation)},
		State: port.ModelQualityEnforcementState(state), Action: modelquality.Action(action), TriggerRunID: triggerRunID,
		PolicyRevision: modelquality.PolicyRevision(policyRevision), AccountConfigRevision: modelquality.AccountRevision(accountRevision),
		BeforeStatus: modelquality.AccountStatus(beforeStatus), AfterStatus: modelquality.AccountStatus(afterStatus),
		FallbackWasEnabled: fallbackWasEnabled == 1, SuperPriorityWasEnabled: superPriorityWasEnabled == 1,
	}
	if record.State != port.ModelQualityEnforcementActive && record.State != port.ModelQualityEnforcementCleared {
		return port.ModelQualityEnforcementRecord{}, fmt.Errorf("invalid persisted model quality enforcement state %q", state)
	}
	if !modelQualityRecoveryActionValid(record.Action) || !modelQualityRecoveryStatusValid(record.BeforeStatus) || !modelQualityRecoveryStatusValid(record.AfterStatus) {
		return port.ModelQualityEnforcementRecord{}, fmt.Errorf("invalid persisted model quality enforcement transition")
	}
	var err error
	record.StartedAt, err = modelQualityPolicyParseTime(startedAt)
	if err != nil {
		return port.ModelQualityEnforcementRecord{}, fmt.Errorf("parse model quality enforcement started_at: %w", err)
	}
	record.UpdatedAt, err = modelQualityPolicyParseTime(updatedAt)
	if err != nil {
		return port.ModelQualityEnforcementRecord{}, fmt.Errorf("parse model quality enforcement updated_at: %w", err)
	}
	if recoveryDueAt.Valid {
		value, err := modelQualityPolicyParseTime(recoveryDueAt.String)
		if err != nil {
			return port.ModelQualityEnforcementRecord{}, fmt.Errorf("parse model quality enforcement recovery_due_at: %w", err)
		}
		record.RecoveryDueAt = &value
	}
	if leaseToken.Valid {
		if !leaseOwner.Valid || !leaseUntil.Valid || !validModelQualityScheduleText(leaseOwner.String, 128) || !validModelQualityScheduleText(leaseToken.String, 256) {
			return port.ModelQualityEnforcementRecord{}, fmt.Errorf("invalid persisted model quality recovery lease")
		}
		until, err := modelQualityPolicyParseTime(leaseUntil.String)
		if err != nil {
			return port.ModelQualityEnforcementRecord{}, fmt.Errorf("parse model quality recovery lease_until: %w", err)
		}
		record.RecoveryLease = &port.ModelQualityRecoveryLease{
			OwnerID: port.ModelQualityClaimOwnerID(leaseOwner.String), ClaimToken: port.ModelQualityRecoveryClaimToken(leaseToken.String), Until: until,
		}
	} else if leaseOwner.Valid != leaseUntil.Valid {
		return port.ModelQualityEnforcementRecord{}, fmt.Errorf("invalid persisted legacy model quality recovery lease")
	} else if leaseOwner.Valid && (!validModelQualityScheduleText(leaseOwner.String, 128) || modelQualityScheduleTimeInvalid(leaseUntil.String)) {
		return port.ModelQualityEnforcementRecord{}, fmt.Errorf("invalid persisted legacy model quality recovery lease")
	}
	if lastRecoveryRunID.Valid {
		if !validModelQualityScheduleText(lastRecoveryRunID.String, 256) {
			return port.ModelQualityEnforcementRecord{}, fmt.Errorf("invalid persisted model quality last recovery run ID")
		}
		record.LastRecoveryRunID = lastRecoveryRunID.String
	}
	if clearedAt.Valid {
		value, err := modelQualityPolicyParseTime(clearedAt.String)
		if err != nil {
			return port.ModelQualityEnforcementRecord{}, fmt.Errorf("parse model quality enforcement cleared_at: %w", err)
		}
		record.ClearedAt = &value
	}
	return record, nil
}

func modelQualityEnforcementState(record port.ModelQualityEnforcementRecord) modelquality.EnforcementState {
	return modelquality.EnforcementState{
		SystemAccountID: record.SystemAccountID, Token: record.Token,
		AccountRevision: record.AccountConfigRevision, Active: record.State == port.ModelQualityEnforcementActive,
		Action: record.Action,
	}
}

func modelQualityRecoveryAvailable(raw *string, now time.Time) (bool, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return true, nil
	}
	allowed, err := apikeyschedule.AllowedAt(raw, now)
	if err != nil {
		return false, fmt.Errorf("parse model quality recovery availability schedule: %w", err)
	}
	return allowed, nil
}

func normalizeModelQualityRecoveryClaimInput(input port.ModelQualityRecoveryClaimInput) port.ModelQualityRecoveryClaimInput {
	if input.Limit == 0 {
		input.Limit = port.ModelQualityRecoveryClaimDefaultLimit
	}
	return input
}

func validateModelQualityRecoveryClaimInput(input port.ModelQualityRecoveryClaimInput) error {
	lease := input.LeaseUntil.Sub(input.Now)
	if !validModelQualityScheduleText(string(input.OwnerID), 128) || input.Now.IsZero() || input.LeaseUntil.IsZero() ||
		lease < port.ModelQualityClaimMinimumLease || lease > port.ModelQualityClaimMaximumLease ||
		input.Limit < 1 || input.Limit > port.ModelQualityRecoveryClaimMaximumLimit {
		return fmt.Errorf("model quality recovery claim is invalid")
	}
	return nil
}

func validateModelQualityRecoveryCompleteInput(input port.ModelQualityRecoveryCompleteInput) error {
	if !validModelQualityScheduleText(input.AccountID, 256) || input.ExpectedEnforcement.Validate() != nil ||
		input.ExpectedEnforcement.Generation > modelquality.EnforcementGeneration(math.MaxInt32) ||
		input.ExpectedPolicyRevision > modelquality.PolicyRevision(math.MaxInt32) || input.ExpectedAccountConfigRevision == 0 ||
		input.ExpectedAccountConfigRevision > modelquality.AccountRevision(math.MaxInt32) ||
		!validModelQualityScheduleText(string(input.Lease.OwnerID), 128) || !validModelQualityScheduleText(string(input.Lease.ClaimToken), 256) ||
		input.Lease.Until.IsZero() || input.CompletedAt.IsZero() || !input.Lease.Until.After(input.CompletedAt) ||
		!validModelQualityScheduleInterval(input.RecoveryInterval) || !validModelQualityScheduleText(input.RunID, 256) {
		return fmt.Errorf("model quality recovery completion is invalid")
	}
	return nil
}

func modelQualityRecoveryActionValid(action modelquality.Action) bool {
	return action == modelquality.ActionFallback || action == modelquality.ActionDisable || action == modelquality.ActionQualityIsolate
}

func modelQualityRecoveryStatusValid(status modelquality.AccountStatus) bool {
	switch status {
	case modelquality.AccountStatusActive, modelquality.AccountStatusPendingTest, modelquality.AccountStatusDisabled,
		modelquality.AccountStatusError, modelquality.AccountStatusRateLimited, modelquality.AccountStatusTemporaryUnavailable,
		modelquality.AccountStatusQualityIsolated:
		return true
	default:
		return false
	}
}

func recoveryStaleResult(status modelquality.AccountStatus) port.ModelQualityRecoveryCompleteResult {
	return port.ModelQualityRecoveryCompleteResult{Status: port.ModelQualityRecoveryStale, BeforeStatus: &status, AfterStatus: &status}
}

func commitModelQualityRecoveryResult(ctx context.Context, tx pgx.Tx, committed *bool, result port.ModelQualityRecoveryCompleteResult) (port.ModelQualityRecoveryCompleteResult, error) {
	if err := tx.Commit(ctx); err != nil {
		return port.ModelQualityRecoveryCompleteResult{}, fmt.Errorf("commit model quality recovery completion: %w", err)
	}
	*committed = true
	return result, nil
}

var _ port.ModelQualityRecoveryClaimer = (*Store)(nil)
var _ port.ModelQualityRecoveryCompleter = (*Store)(nil)
