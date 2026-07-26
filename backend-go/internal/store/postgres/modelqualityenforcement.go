package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
)

const modelQualityEnforcementMaximumMessageBytes = 64 << 10

type modelQualityEnforcementIDGenerator func() (string, error)

func (s *Store) ApplyModelQualityEnforcement(ctx context.Context, input port.ModelQualityEnforcementApplyInput) (port.ModelQualityEnforcementApplyResult, error) {
	return applyModelQualityEnforcement(ctx, s.pool.BeginTx, input, func() (string, error) {
		value, err := uuid.NewRandom()
		if err != nil {
			return "", err
		}
		return "mqe_" + strings.ReplaceAll(value.String(), "-", ""), nil
	})
}

func applyModelQualityEnforcement(
	ctx context.Context,
	beginTx modelQualityScheduleBeginTx,
	input port.ModelQualityEnforcementApplyInput,
	newID modelQualityEnforcementIDGenerator,
) (port.ModelQualityEnforcementApplyResult, error) {
	if err := validateModelQualityEnforcementApplyInput(input); err != nil {
		return port.ModelQualityEnforcementApplyResult{}, err
	}
	if newID == nil {
		return port.ModelQualityEnforcementApplyResult{}, fmt.Errorf("model quality enforcement ID generator is required")
	}
	tx, err := beginModelQualityScheduleTx(ctx, beginTx, "enforcement apply")
	if err != nil {
		return port.ModelQualityEnforcementApplyResult{}, err
	}
	committed := false
	defer rollbackModelQualityScheduleTx(tx, &committed)()

	account, found, eligible, err := lockModelQualityEnforcementAccount(ctx, tx, input)
	if err != nil {
		return port.ModelQualityEnforcementApplyResult{}, err
	}
	if !found || !eligible {
		return commitModelQualityEnforcementResult(ctx, tx, &committed, port.ModelQualityEnforcementApplyResult{Status: port.ModelQualityEnforcementSkipped})
	}
	prior, priorFound, err := lockPriorModelQualityEnforcement(ctx, tx, input.AccountID)
	if err != nil {
		return port.ModelQualityEnforcementApplyResult{}, err
	}
	// trigger_run_id is a consumption key, not merely an active-state key. A
	// delayed durable retry must not resurrect a penalty after recovery cleared
	// the original enforcement generation.
	if priorFound && prior.TriggerRunID == input.RunID {
		status := port.ModelQualityEnforcementAlreadyEffective
		if prior.Action != input.Action || prior.SystemAccountID != input.SystemAccountID {
			status = port.ModelQualityEnforcementStale
		}
		before, after := account.Status, account.Status
		return commitModelQualityEnforcementResult(ctx, tx, &committed, port.ModelQualityEnforcementApplyResult{
			Status: status, BeforeStatus: &before, AfterStatus: &after, Enforcement: &prior,
		})
	}

	policy, err := readModelQualityPolicy(ctx, tx, input.SystemAccountID)
	if err != nil {
		return port.ModelQualityEnforcementApplyResult{}, fmt.Errorf("read model quality enforcement policy: %w", err)
	}
	request := modelquality.EnforcementRequest{
		Trigger: input.Trigger, RunID: input.RunID, Action: input.Action,
		PolicyRevision: input.ExpectedPolicyRevision, AccountRevision: input.ExpectedAccountConfigRevision,
	}
	plan, err := modelquality.PlanEnforcement(request, policy.Policy, account)
	if err != nil {
		return port.ModelQualityEnforcementApplyResult{}, fmt.Errorf("plan model quality enforcement: %w", err)
	}
	before := account.Status
	if plan.Result == modelquality.EnforcementSkipped || plan.Result == modelquality.EnforcementStale {
		status := port.ModelQualityEnforcementSkipped
		if plan.Result == modelquality.EnforcementStale {
			status = port.ModelQualityEnforcementStale
		}
		return commitModelQualityEnforcementResult(ctx, tx, &committed, port.ModelQualityEnforcementApplyResult{
			Status: status, BeforeStatus: &before, AfterStatus: &before,
		})
	}

	generation, err := nextModelQualityEnforcementGeneration(prior, priorFound)
	if err != nil {
		return port.ModelQualityEnforcementApplyResult{}, err
	}
	enforcementID, err := newID()
	if err != nil {
		return port.ModelQualityEnforcementApplyResult{}, fmt.Errorf("generate model quality enforcement ID: %w", err)
	}
	if !validModelQualityScheduleText(enforcementID, 256) {
		return port.ModelQualityEnforcementApplyResult{}, fmt.Errorf("generated model quality enforcement ID is invalid")
	}

	var recoveryDueAt *time.Time
	if input.Action == modelquality.ActionQualityIsolate {
		value, err := addModelQualityScheduleInterval(input.DecidedAt, input.RecoveryInterval)
		if err != nil {
			return port.ModelQualityEnforcementApplyResult{}, err
		}
		recoveryDueAt = &value
	}
	after := plan.TargetStatus
	accountRevisionAfter := account.ConfigRevision
	accountChanged := plan.Result == modelquality.EnforcementApply
	if accountChanged {
		if account.ConfigRevision >= modelquality.AccountRevision(math.MaxInt32) {
			return port.ModelQualityEnforcementApplyResult{}, fmt.Errorf("model quality enforcement account revision is exhausted")
		}
		command, err := tx.Exec(ctx, updateModelQualityEnforcementAccountSQL,
			string(after), string(input.Action), truncateModelQualityTextRunes(input.Message, 1000), input.DecidedAt.UTC(),
			input.AccountID, input.SystemAccountID, string(before), int64(account.ConfigRevision),
			int64(policy.Policy.Revision), string(input.Trigger),
		)
		if err != nil {
			return port.ModelQualityEnforcementApplyResult{}, fmt.Errorf("update model quality enforcement account: %w", err)
		}
		if command.RowsAffected() != 1 {
			return commitModelQualityEnforcementResult(ctx, tx, &committed, port.ModelQualityEnforcementApplyResult{
				Status: port.ModelQualityEnforcementStale, BeforeStatus: &before, AfterStatus: &before,
			})
		}
		accountRevisionAfter++
	}

	args := modelQualityEnforcementWriteArgs(input, account, enforcementID, generation, after, recoveryDueAt, accountRevisionAfter)
	var record port.ModelQualityEnforcementRecord
	if priorFound {
		args = append(args, prior.Token.ID, int64(prior.Token.Generation))
		record, err = scanModelQualityEnforcement(tx.QueryRow(ctx, replaceModelQualityEnforcementSQL, args...))
	} else {
		record, err = scanModelQualityEnforcement(tx.QueryRow(ctx, insertModelQualityEnforcementSQL, args...))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ModelQualityEnforcementApplyResult{}, fmt.Errorf("model quality enforcement write lost its account, policy, or generation CAS")
	}
	if err != nil {
		return port.ModelQualityEnforcementApplyResult{}, fmt.Errorf("write model quality enforcement: %w", err)
	}
	status := port.ModelQualityEnforcementApplied
	if plan.Result == modelquality.EnforcementAlreadyEffective {
		status = port.ModelQualityEnforcementAlreadyEffective
	}
	return commitModelQualityEnforcementResult(ctx, tx, &committed, port.ModelQualityEnforcementApplyResult{
		Status: status, BeforeStatus: &before, AfterStatus: &after, Enforcement: &record,
	})
}

func lockModelQualityEnforcementAccount(
	ctx context.Context,
	tx pgx.Tx,
	input port.ModelQualityEnforcementApplyInput,
) (modelquality.Account, bool, bool, error) {
	var (
		systemAccountID                string
		status                         string
		configRevision                 int64
		fallbackEnabled, superPriority bool
		notDeleted, ownPhysical        bool
	)
	err := tx.QueryRow(ctx, lockModelQualityEnforcementAccountSQL, input.AccountID).Scan(
		&systemAccountID, &status, &configRevision, &fallbackEnabled, &superPriority, &notDeleted, &ownPhysical,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return modelquality.Account{}, false, false, nil
	}
	if err != nil {
		return modelquality.Account{}, false, false, fmt.Errorf("lock model quality enforcement account: %w", err)
	}
	if configRevision < 1 || configRevision > math.MaxInt32 {
		return modelquality.Account{}, false, false, fmt.Errorf("invalid model quality enforcement account revision %d", configRevision)
	}
	account := modelquality.Account{
		ID: input.AccountID, SystemAccountID: systemAccountID, Status: modelquality.AccountStatus(status),
		ConfigRevision: modelquality.AccountRevision(configRevision), OwnPhysical: ownPhysical,
		FallbackEnabled: fallbackEnabled, SuperPrioritySet: superPriority,
	}
	if err := account.Validate(); err != nil {
		return modelquality.Account{}, false, false, fmt.Errorf("invalid model quality enforcement account: %w", err)
	}
	eligible := notDeleted && ownPhysical && systemAccountID == input.SystemAccountID
	return account, true, eligible, nil
}

func lockPriorModelQualityEnforcement(ctx context.Context, tx pgx.Tx, accountID string) (port.ModelQualityEnforcementRecord, bool, error) {
	record, err := scanModelQualityEnforcement(tx.QueryRow(ctx, lockModelQualityEnforcementSQL, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ModelQualityEnforcementRecord{}, false, nil
	}
	if err != nil {
		return port.ModelQualityEnforcementRecord{}, false, fmt.Errorf("lock prior model quality enforcement: %w", err)
	}
	return record, true, nil
}

func nextModelQualityEnforcementGeneration(prior port.ModelQualityEnforcementRecord, found bool) (modelquality.EnforcementGeneration, error) {
	previous := modelquality.EnforcementGeneration(0)
	if found {
		previous = prior.Token.Generation
	}
	next, err := modelquality.NextGeneration(previous)
	if err != nil {
		return 0, err
	}
	if next > modelquality.EnforcementGeneration(math.MaxInt32) {
		return 0, fmt.Errorf("model quality enforcement generation is exhausted")
	}
	return next, nil
}

func modelQualityEnforcementWriteArgs(
	input port.ModelQualityEnforcementApplyInput,
	account modelquality.Account,
	enforcementID string,
	generation modelquality.EnforcementGeneration,
	after modelquality.AccountStatus,
	recoveryDueAt *time.Time,
	accountRevisionAfter modelquality.AccountRevision,
) []any {
	var recoveryDue any
	if recoveryDueAt != nil {
		recoveryDue = modelQualityPolicyTimeText(*recoveryDueAt)
	}
	return []any{
		input.AccountID, input.SystemAccountID, enforcementID, int64(generation), string(input.Action),
		input.RunID, int64(input.ExpectedPolicyRevision), int64(input.ExpectedAccountConfigRevision),
		string(account.Status), string(after), modelQualityPolicyBoolInt(account.FallbackEnabled),
		modelQualityPolicyBoolInt(account.SuperPrioritySet), modelQualityPolicyTimeText(input.DecidedAt), recoveryDue,
		int64(accountRevisionAfter), string(input.Trigger),
	}
}

func validateModelQualityEnforcementApplyInput(input port.ModelQualityEnforcementApplyInput) error {
	request := modelquality.EnforcementRequest{
		Trigger: input.Trigger, RunID: input.RunID, Action: input.Action,
		PolicyRevision: input.ExpectedPolicyRevision, AccountRevision: input.ExpectedAccountConfigRevision,
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if !validModelQualityScheduleText(input.SystemAccountID, 256) || !validModelQualityScheduleText(input.AccountID, 256) ||
		!validModelQualityScheduleText(input.RunID, 256) || input.ExpectedPolicyRevision > modelquality.PolicyRevision(math.MaxInt32) ||
		input.ExpectedAccountConfigRevision > modelquality.AccountRevision(math.MaxInt32) || input.DecidedAt.IsZero() ||
		!validModelQualityScheduleInterval(input.RecoveryInterval) || !utf8.ValidString(input.Message) ||
		len(input.Message) > modelQualityEnforcementMaximumMessageBytes || strings.IndexByte(input.Message, 0) >= 0 {
		return fmt.Errorf("model quality enforcement input is invalid")
	}
	return nil
}

func commitModelQualityEnforcementResult(
	ctx context.Context,
	tx pgx.Tx,
	committed *bool,
	result port.ModelQualityEnforcementApplyResult,
) (port.ModelQualityEnforcementApplyResult, error) {
	if err := tx.Commit(ctx); err != nil {
		return port.ModelQualityEnforcementApplyResult{}, fmt.Errorf("commit model quality enforcement apply: %w", err)
	}
	*committed = true
	return result, nil
}

var _ port.ModelQualityEnforcementApplier = (*Store)(nil)
