package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	modelQualityEnforcementMaximumMessageBytes  = 64 << 10
	modelQualityEnforcementMaximumSnapshotBytes = 64 << 10
)

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
		if prior.Action != input.Action || prior.SystemAccountID != input.SystemAccountID ||
			prior.ConfigSource != modelQualityEnforcementConfigSource(input) || prior.ConfigSourceID != input.ScheduleID ||
			prior.PolicyRevision != input.ExpectedPolicyRevision || prior.Profile != input.Profile ||
			prior.PenaltyThreshold != input.PenaltyThreshold || prior.RecoveryInterval != input.RecoveryInterval ||
			prior.RecoveryModel != input.RecoveryModel {
			status = port.ModelQualityEnforcementStale
		}
		before, after := account.Status, account.Status
		return commitModelQualityEnforcementResult(ctx, tx, &committed, port.ModelQualityEnforcementApplyResult{
			Status: status, BeforeStatus: &before, AfterStatus: &after, Enforcement: &prior,
		})
	}
	runMatches, err := lockAndValidateModelQualityEnforcementRun(ctx, tx, input)
	if err != nil {
		return port.ModelQualityEnforcementApplyResult{}, err
	}
	if !runMatches {
		before := account.Status
		return commitModelQualityEnforcementResult(ctx, tx, &committed, port.ModelQualityEnforcementApplyResult{
			Status: port.ModelQualityEnforcementStale, BeforeStatus: &before, AfterStatus: &before,
		})
	}

	policy, modelMatches, err := readModelQualityEnforcementConfiguration(ctx, tx, input)
	if err != nil {
		return port.ModelQualityEnforcementApplyResult{}, err
	}
	request := modelquality.EnforcementRequest{
		Trigger: input.Trigger, RunID: input.RunID, Action: input.Action,
		Profile: input.Profile, PenaltyThreshold: input.PenaltyThreshold,
		RecoveryIntervalMinutes: int(input.RecoveryInterval / time.Minute),
		PolicyRevision:          input.ExpectedPolicyRevision, AccountRevision: input.ExpectedAccountConfigRevision,
	}
	plan, err := modelquality.PlanEnforcement(request, policy.Policy, account)
	if err != nil {
		return port.ModelQualityEnforcementApplyResult{}, fmt.Errorf("plan model quality enforcement: %w", err)
	}
	before := account.Status
	if !modelMatches {
		plan.Result = modelquality.EnforcementStale
	}
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
			int64(policy.Policy.Revision), string(modelQualityEnforcementConfigSource(input)), modelQualityEnforcementConfigSourceID(input),
			string(input.Profile), input.PenaltyThreshold, int(input.RecoveryInterval/time.Minute), input.RecoveryModel, string(input.Trigger),
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

type modelQualityEnforcementRunSnapshot struct {
	PolicyRevision           modelquality.PolicyRevision
	ConfigSource             port.ModelQualityConfigSource
	Profile                  modelquality.Profile
	ManualEnforcementEnabled bool
	PenaltyThreshold         int
	Action                   modelquality.Action
	RecoveryInterval         time.Duration
	ScheduleID               string
	AccountConfigRevision    modelquality.AccountRevision
}

func lockAndValidateModelQualityEnforcementRun(
	ctx context.Context,
	tx pgx.Tx,
	input port.ModelQualityEnforcementApplyInput,
) (bool, error) {
	var (
		systemAccountID, targetType, targetID              string
		model, profileRaw, triggerRaw, statusRaw           string
		accountID, targetOwnerID, scheduleID, snapshotJSON pgtype.Text
		snapshotBytes                                      int64
	)
	err := tx.QueryRow(ctx, lockModelQualityEnforcementRunSQL,
		input.RunID, modelQualityEnforcementMaximumSnapshotBytes,
	).Scan(
		&systemAccountID, &accountID, &targetType, &targetID, &targetOwnerID,
		&model, &profileRaw, &triggerRaw,
		&scheduleID, &statusRaw, &snapshotJSON, &snapshotBytes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock model quality enforcement run: %w", err)
	}
	if snapshotBytes < 1 || snapshotBytes > modelQualityEnforcementMaximumSnapshotBytes || !snapshotJSON.Valid {
		return false, fmt.Errorf("persisted model quality enforcement policy snapshot exceeds its bounded contract")
	}
	if !validModelQualityScheduleText(systemAccountID, 256) || !validModelQualityScheduleText(model, 4096) {
		return false, fmt.Errorf("persisted model quality enforcement run identity is invalid")
	}
	// account_id is nullable for legitimate group/API-key diagnostic runs. Such
	// a run cannot authorize an account mutation, but it is not corrupt durable
	// state and should resolve as stale instead of becoming a retrying error.
	if !accountID.Valid {
		return false, nil
	}
	if !validModelQualityScheduleText(accountID.String, 256) {
		return false, fmt.Errorf("persisted model quality enforcement run account identity is invalid")
	}
	if !validModelQualityScheduleText(targetType, 64) || !validModelQualityScheduleText(targetID, 256) {
		return false, fmt.Errorf("persisted model quality enforcement run target identity is invalid")
	}
	if targetOwnerID.Valid && !validModelQualityScheduleText(targetOwnerID.String, 256) {
		return false, fmt.Errorf("persisted model quality enforcement run target owner is invalid")
	}
	profile := modelquality.Profile(profileRaw)
	if profile != modelquality.ProfileQuick && profile != modelquality.ProfileFull {
		return false, fmt.Errorf("persisted model quality enforcement run profile is invalid")
	}
	trigger := modelquality.Trigger(triggerRaw)
	if trigger != modelquality.TriggerManual && trigger != modelquality.TriggerScheduled && trigger != modelquality.TriggerQualityRecovery {
		return false, fmt.Errorf("persisted model quality enforcement run trigger is invalid")
	}
	status := modelquality.RunStatus(statusRaw)
	if status != modelquality.RunStatusRunning && status != modelquality.RunStatusCompleted &&
		status != modelquality.RunStatusFailed && status != modelquality.RunStatusCanceled {
		return false, fmt.Errorf("persisted model quality enforcement run status is invalid")
	}
	if scheduleID.Valid && !validModelQualityScheduleText(scheduleID.String, 256) {
		return false, fmt.Errorf("persisted model quality enforcement run schedule is invalid")
	}
	snapshot, err := decodeModelQualityEnforcementRunSnapshot(snapshotJSON.String)
	if err != nil {
		return false, fmt.Errorf("invalid persisted model quality enforcement policy snapshot: %w", err)
	}
	if snapshot.Profile != profile {
		return false, fmt.Errorf("persisted model quality enforcement run profile conflicts with its policy snapshot")
	}
	if trigger == modelquality.TriggerManual {
		if scheduleID.Valid || snapshot.ConfigSource != port.ModelQualityConfigSourceManual || snapshot.ScheduleID != "" {
			return false, fmt.Errorf("persisted manual model quality enforcement run has a schedule source")
		}
	} else if trigger == modelquality.TriggerScheduled {
		if !scheduleID.Valid || snapshot.ConfigSource != port.ModelQualityConfigSourceSchedule || snapshot.ScheduleID != scheduleID.String {
			return false, fmt.Errorf("persisted scheduled model quality enforcement run source is inconsistent")
		}
	}
	callerSource := modelQualityEnforcementConfigSource(input)
	callerScheduleID := input.ScheduleID
	return systemAccountID == input.SystemAccountID && accountID.String == input.AccountID &&
		targetType == "account" && targetID == accountID.String && targetID == input.AccountID &&
		targetOwnerID.Valid && targetOwnerID.String == input.SystemAccountID &&
		model == input.RecoveryModel && profile == input.Profile && trigger == input.Trigger &&
		status == modelquality.RunStatusCompleted && snapshot.ConfigSource == callerSource &&
		snapshot.ScheduleID == callerScheduleID && snapshot.PolicyRevision == input.ExpectedPolicyRevision &&
		snapshot.Profile == input.Profile && snapshot.PenaltyThreshold == input.PenaltyThreshold &&
		snapshot.Action == input.Action && snapshot.RecoveryInterval == input.RecoveryInterval &&
		snapshot.AccountConfigRevision == input.ExpectedAccountConfigRevision &&
		(input.Trigger != modelquality.TriggerManual || snapshot.ManualEnforcementEnabled), nil
}

func decodeModelQualityEnforcementRunSnapshot(raw string) (modelQualityEnforcementRunSnapshot, error) {
	fields, err := decodeUniqueModelQualityEnforcementSnapshotObject(raw)
	if err != nil {
		return modelQualityEnforcementRunSnapshot{}, err
	}
	policyRevision, err := requiredModelQualityEnforcementSnapshotInt(fields, "policyRevision", 0, math.MaxInt32)
	if err != nil {
		return modelQualityEnforcementRunSnapshot{}, err
	}
	configSourceRaw, err := requiredModelQualityEnforcementSnapshotString(fields, "configSource")
	if err != nil {
		return modelQualityEnforcementRunSnapshot{}, err
	}
	configSource := port.ModelQualityConfigSource(configSourceRaw)
	if configSource != port.ModelQualityConfigSourceManual && configSource != port.ModelQualityConfigSourceSchedule {
		return modelQualityEnforcementRunSnapshot{}, fmt.Errorf("policy snapshot configSource is invalid")
	}
	profileRaw, err := requiredModelQualityEnforcementSnapshotString(fields, "profile")
	if err != nil {
		return modelQualityEnforcementRunSnapshot{}, err
	}
	profile := modelquality.Profile(profileRaw)
	if profile != modelquality.ProfileQuick && profile != modelquality.ProfileFull {
		return modelQualityEnforcementRunSnapshot{}, fmt.Errorf("policy snapshot profile is invalid")
	}
	manualEnforcementEnabled, err := requiredModelQualityEnforcementSnapshotBool(fields, "manualEnforcementEnabled")
	if err != nil {
		return modelQualityEnforcementRunSnapshot{}, err
	}
	threshold, err := requiredModelQualityEnforcementSnapshotInt(fields, "threshold", 40, 100)
	if err != nil {
		return modelQualityEnforcementRunSnapshot{}, err
	}
	actionRaw, err := requiredModelQualityEnforcementSnapshotString(fields, "action")
	if err != nil {
		return modelQualityEnforcementRunSnapshot{}, err
	}
	action := modelquality.Action(actionRaw)
	if action != modelquality.ActionDisable && action != modelquality.ActionFallback && action != modelquality.ActionQualityIsolate {
		return modelQualityEnforcementRunSnapshot{}, fmt.Errorf("policy snapshot action is invalid")
	}
	recoveryMinutes, err := requiredModelQualityEnforcementSnapshotInt(fields, "recoveryIntervalMinutes", 10, 10080)
	if err != nil {
		return modelQualityEnforcementRunSnapshot{}, err
	}
	accountRevision, err := requiredModelQualityEnforcementSnapshotInt(fields, "accountConfigRevision", 1, math.MaxInt32)
	if err != nil {
		return modelQualityEnforcementRunSnapshot{}, err
	}
	var scheduleID string
	if value, ok := fields["scheduleId"]; ok && string(value) != "null" {
		if err := json.Unmarshal(value, &scheduleID); err != nil || !validModelQualityScheduleText(scheduleID, 256) {
			return modelQualityEnforcementRunSnapshot{}, fmt.Errorf("policy snapshot scheduleId is invalid")
		}
	}
	if configSource == port.ModelQualityConfigSourceManual && scheduleID != "" {
		return modelQualityEnforcementRunSnapshot{}, fmt.Errorf("manual policy snapshot cannot name a schedule")
	}
	if configSource == port.ModelQualityConfigSourceSchedule && scheduleID == "" {
		return modelQualityEnforcementRunSnapshot{}, fmt.Errorf("scheduled policy snapshot requires a schedule")
	}
	return modelQualityEnforcementRunSnapshot{
		PolicyRevision: modelquality.PolicyRevision(policyRevision), ConfigSource: configSource,
		Profile: profile, ManualEnforcementEnabled: manualEnforcementEnabled,
		PenaltyThreshold: threshold, Action: action,
		RecoveryInterval: time.Duration(recoveryMinutes) * time.Minute, ScheduleID: scheduleID,
		AccountConfigRevision: modelquality.AccountRevision(accountRevision),
	}, nil
}

func decodeUniqueModelQualityEnforcementSnapshotObject(raw string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("policy snapshot must be a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("policy snapshot field name must be a string")
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf("policy snapshot contains a duplicate top-level field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	token, err = decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok = token.(json.Delim)
	if !ok || delim != '}' {
		return nil, fmt.Errorf("policy snapshot object is not terminated")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("policy snapshot contains trailing data")
	}
	return fields, nil
}

func requiredModelQualityEnforcementSnapshotString(fields map[string]json.RawMessage, name string) (string, error) {
	var value string
	raw, ok := fields[name]
	if !ok || json.Unmarshal(raw, &value) != nil || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("policy snapshot %s is invalid", name)
	}
	return value, nil
}

func requiredModelQualityEnforcementSnapshotInt(fields map[string]json.RawMessage, name string, minimum, maximum int64) (int, error) {
	var value int64
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("policy snapshot %s is invalid", name)
	}
	return int(value), nil
}

func requiredModelQualityEnforcementSnapshotBool(fields map[string]json.RawMessage, name string) (bool, error) {
	raw, ok := fields[name]
	if !ok {
		return false, fmt.Errorf("policy snapshot %s is invalid", name)
	}
	switch string(bytes.TrimSpace(raw)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("policy snapshot %s is invalid", name)
	}
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
	lockSQL := lockModelQualityEnforcementAccountSQL
	if input.Trigger == modelquality.TriggerManual {
		lockSQL = lockManualModelQualityEnforcementAccountSQL
	}
	err := tx.QueryRow(ctx, lockSQL, input.AccountID, input.SystemAccountID).Scan(
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
		input.RunID, string(modelQualityEnforcementConfigSource(input)), modelQualityEnforcementConfigSourceID(input),
		int64(input.ExpectedPolicyRevision), string(input.Profile), input.PenaltyThreshold,
		int(input.RecoveryInterval / time.Minute), modelQualityEnforcementRecoveryModel(input),
		int64(input.ExpectedAccountConfigRevision), string(account.Status), string(after),
		modelQualityPolicyBoolInt(account.FallbackEnabled), modelQualityPolicyBoolInt(account.SuperPrioritySet),
		modelQualityPolicyTimeText(input.DecidedAt), recoveryDue, int64(accountRevisionAfter), string(input.Trigger),
	}
}

func validateModelQualityEnforcementApplyInput(input port.ModelQualityEnforcementApplyInput) error {
	request := modelquality.EnforcementRequest{
		Trigger: input.Trigger, RunID: input.RunID, Action: input.Action,
		Profile: input.Profile, PenaltyThreshold: input.PenaltyThreshold,
		RecoveryIntervalMinutes: int(input.RecoveryInterval / time.Minute),
		PolicyRevision:          input.ExpectedPolicyRevision, AccountRevision: input.ExpectedAccountConfigRevision,
	}
	if err := request.Validate(); err != nil {
		return err
	}
	if !validModelQualityScheduleText(input.SystemAccountID, 256) || !validModelQualityScheduleText(input.AccountID, 256) ||
		!validModelQualityScheduleText(input.RunID, 256) || !validModelQualityScheduleText(input.RecoveryModel, 4096) ||
		input.ExpectedPolicyRevision > modelquality.PolicyRevision(math.MaxInt32) ||
		input.ExpectedAccountConfigRevision > modelquality.AccountRevision(math.MaxInt32) || input.DecidedAt.IsZero() ||
		!validModelQualityScheduleInterval(input.RecoveryInterval) || !utf8.ValidString(input.Message) ||
		len(input.Message) > modelQualityEnforcementMaximumMessageBytes || strings.IndexByte(input.Message, 0) >= 0 {
		return fmt.Errorf("model quality enforcement input is invalid")
	}
	if input.Trigger == modelquality.TriggerScheduled {
		if !validModelQualityScheduleText(input.ScheduleID, 256) || input.ExpectedPolicyRevision == 0 {
			return fmt.Errorf("scheduled model quality enforcement source is invalid")
		}
	} else if input.ScheduleID != "" {
		return fmt.Errorf("manual model quality enforcement cannot name a schedule")
	}
	return nil
}

func readModelQualityEnforcementConfiguration(ctx context.Context, tx pgx.Tx, input port.ModelQualityEnforcementApplyInput) (port.ModelQualityPolicyRecord, bool, error) {
	if input.Trigger != modelquality.TriggerScheduled {
		policy, err := readLockedModelQualityEnforcementPolicy(ctx, tx, input.SystemAccountID)
		if err != nil {
			return port.ModelQualityPolicyRecord{}, false, fmt.Errorf("read manual model quality enforcement policy: %w", err)
		}
		return policy, true, nil
	}
	schedule, err := scanModelQualitySchedule(tx.QueryRow(ctx, lockModelQualityEnforcementScheduleSQL, input.ScheduleID, input.SystemAccountID, input.AccountID))
	if errors.Is(err, pgx.ErrNoRows) {
		// Construct a valid but non-matching snapshot so the caller returns the
		// normal stale result without treating an operator edit/delete as storage
		// corruption.
		return port.ModelQualityPolicyRecord{Policy: modelquality.Policy{
			SystemAccountID: input.SystemAccountID, Revision: input.ExpectedPolicyRevision,
			Profile: input.Profile, ManualEnforcementEnabled: true, PenaltyThreshold: input.PenaltyThreshold,
			PenaltyAction: input.Action, RecoveryIntervalMinutes: int(input.RecoveryInterval / time.Minute),
		}}, false, nil
	}
	if err != nil {
		return port.ModelQualityPolicyRecord{}, false, fmt.Errorf("read scheduled model quality enforcement configuration: %w", err)
	}
	return modelQualitySchedulePolicy(schedule), schedule.Model == input.RecoveryModel, nil
}

func readLockedModelQualityEnforcementPolicy(
	ctx context.Context,
	tx pgx.Tx,
	systemAccountID string,
) (port.ModelQualityPolicyRecord, error) {
	record, err := scanModelQualityPolicy(tx.QueryRow(ctx, lockModelQualityEnforcementPolicySQL, systemAccountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ModelQualityPolicyRecord{
			Policy:    modelquality.DefaultPolicy(systemAccountID),
			Persisted: false,
		}, nil
	}
	if err != nil {
		return port.ModelQualityPolicyRecord{}, err
	}
	return record, nil
}

func modelQualityEnforcementConfigSource(input port.ModelQualityEnforcementApplyInput) port.ModelQualityConfigSource {
	if input.Trigger == modelquality.TriggerScheduled {
		return port.ModelQualityConfigSourceSchedule
	}
	return port.ModelQualityConfigSourceManual
}

func modelQualityEnforcementConfigSourceID(input port.ModelQualityEnforcementApplyInput) any {
	if input.ScheduleID == "" {
		return nil
	}
	return input.ScheduleID
}

func modelQualityEnforcementRecoveryModel(input port.ModelQualityEnforcementApplyInput) any {
	if input.RecoveryModel == "" {
		return nil
	}
	return input.RecoveryModel
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
