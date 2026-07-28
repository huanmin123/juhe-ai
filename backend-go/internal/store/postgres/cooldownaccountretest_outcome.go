package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/accounthealth"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	cooldownOutcomeSuccess cooldownOutcomeKind = "success"
	cooldownOutcomeDefer   cooldownOutcomeKind = "defer"
	cooldownOutcomeFailure cooldownOutcomeKind = "failure"

	cooldownOutcomeProjectionKey                   = "account_circuit_runtime_v1"
	cooldownOutcomeInitialBackoffSeconds           = 3
	cooldownOutcomeFastThresholdSeconds            = 60
	cooldownOutcomeBackoffMultiplier               = 2
	cooldownOutcomeLongTermIntervalSeconds         = 60 * 60
	cooldownOutcomeObservationTimeoutSeconds       = 7 * 24 * 60 * 60
	cooldownOutcomeLimitedProbeTimeoutSeconds      = 10 * 60
	cooldownOutcomeLongTermUnavailableCode         = "cooldown_retest_long_term_unavailable"
	cooldownOutcomeObservationTimeoutCode          = "cooldown_retest_observation_timeout"
	cooldownOutcomeLimitedProbeTimeoutCode         = "cooldown_retest_limited_probe_timeout"
	cooldownOutcomeExplicitPolicyCode              = "explicit_account_error_policy_cooldown"
	cooldownOutcomeLegacyExplicitPolicyMessageLead = "账户错误策略「"
	cooldownOutcomeMaxAuthorizedFamilySize         = 1000
)

type cooldownOutcomeKind string

type cooldownOutcomeInput struct {
	task  port.CooldownAccountRetestTask
	kind  cooldownOutcomeKind
	probe port.CooldownAccountRetestProbeResult
	delay time.Duration
	now   time.Time
}

type cooldownOutcomeAccountRow struct {
	id                         string
	systemAccountID            string
	status                     string
	schedulable                bool
	expiresAt                  *time.Time
	configRevision             int
	dispatchRevision           int64
	cooldownUntil              *time.Time
	lastErrorCode              string
	lastErrorMessage           string
	failureCount               int
	observationStartedAt       *time.Time
	generation                 string
	continuousProbeEnabled     int
	authorizationSourceID      string
	authorizationID            string
	authorizationOwnerSystemID string
	providerPresent            bool
	deleted                    bool
}

type cooldownOutcomeReplayRow struct {
	eventID          string
	eventType        string
	accountID        string
	runtimeKey       string
	transitionID     string
	dispatchRevision int64
}

type cooldownOutcomeFamilyBatchItem struct {
	AccountID                string `json:"account_id"`
	ExpectedDispatchRevision int64  `json:"expected_dispatch_revision"`
	TransitionID             string `json:"transition_id"`
	EventID                  string `json:"event_id"`
}

type cooldownOutcomeRecoveryPlan struct {
	stage                     string
	backoffSeconds            int
	maxRecoverySeconds        int
	observationStartedAt      time.Time
	observationElapsedSeconds int
	observationTimeoutSeconds int
}

func (s *Store) RecordCooldownAccountRetestSuccess(ctx context.Context, task port.CooldownAccountRetestTask) error {
	return s.recordCooldownAccountRetestOutcome(ctx, cooldownOutcomeInput{task: task, kind: cooldownOutcomeSuccess})
}

func (s *Store) DeferCooldownAccountRetest(ctx context.Context, task port.CooldownAccountRetestTask, delay time.Duration) error {
	return s.recordCooldownAccountRetestOutcome(ctx, cooldownOutcomeInput{task: task, kind: cooldownOutcomeDefer, delay: delay})
}

func (s *Store) RecordCooldownAccountRetestFailure(ctx context.Context, task port.CooldownAccountRetestTask, probe port.CooldownAccountRetestProbeResult) error {
	return s.recordCooldownAccountRetestOutcome(ctx, cooldownOutcomeInput{task: task, kind: cooldownOutcomeFailure, probe: probe})
}

func (s *Store) recordCooldownAccountRetestOutcome(ctx context.Context, input cooldownOutcomeInput) error {
	if err := validateCooldownOutcomeInput(input); err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin cooldown account retest outcome: %w", err)
	}
	committed := false
	defer rollbackCooldownAccountRetestOutcomeTx(tx, &committed)()

	applied, err := applyCooldownAccountRetestOutcomeInTx(ctx, tx, input)
	if err != nil {
		return err
	}
	if !applied {
		if err := rollbackCooldownAccountRetestOutcome(tx); err != nil {
			return err
		}
		committed = true
		return nil
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit cooldown account retest outcome: %w", err)
	}
	committed = true
	return nil
}

func applyCooldownAccountRetestOutcomeInTx(ctx context.Context, tx pgx.Tx, input cooldownOutcomeInput) (bool, error) {
	rows, err := lockCooldownAccountRetestOutcomeAccounts(ctx, tx, input.task)
	if err != nil {
		return false, err
	}
	if err := tx.QueryRow(ctx, selectCooldownAccountRetestOutcomeNowSQL).Scan(&input.now); err != nil {
		return false, fmt.Errorf("read cooldown account retest outcome lock time: %w", err)
	}
	input.now = input.now.UTC()
	transitionID := cooldownAccountRetestOutcomeTransitionID(input.task, input.kind)
	dedupeKey := "dispatch:" + transitionID
	replay, found, err := findCooldownAccountRetestOutcomeReplay(ctx, tx, dedupeKey)
	if err != nil {
		return false, err
	}
	if found {
		if replay.eventType != port.GatewayAccountCircuitDispatchRevisionChanged || replay.accountID != input.task.AccountID || replay.runtimeKey != input.task.AccountID || replay.transitionID != transitionID {
			return false, fmt.Errorf("cooldown account retest outcome replay identity conflicts with durable outbox")
		}
		return false, nil
	}

	target, source, eligible, err := revalidateCooldownAccountRetestOutcome(ctx, tx, input.task, input.now, rows)
	if err != nil {
		return false, err
	}
	if !eligible {
		return false, nil
	}

	dispatchRevision, applied, err := mutateCooldownAccountRetestOutcome(ctx, tx, input, target)
	if err != nil {
		return false, err
	}
	if !applied {
		return false, nil
	}
	if err := insertCooldownAccountRetestOutcomeOutbox(ctx, tx, input.task.AccountID, transitionID, dispatchRevision, input.now); err != nil {
		return false, err
	}

	if source.id == target.id {
		family := make([]cooldownOutcomeFamilyBatchItem, 0, len(rows)-1)
		for _, row := range rows {
			if row.id == target.id || row.authorizationSourceID != target.id || row.deleted {
				continue
			}
			familyTransitionID := cooldownAccountRetestFamilyTransitionID(transitionID, row.id)
			family = append(family, cooldownOutcomeFamilyBatchItem{
				AccountID:                row.id,
				ExpectedDispatchRevision: row.dispatchRevision,
				TransitionID:             familyTransitionID,
				EventID:                  uuid.NewString(),
			})
		}
		if err := advanceCooldownAccountRetestFamily(ctx, tx, family, input.now); err != nil {
			return false, err
		}
	}

	if input.kind != cooldownOutcomeDefer {
		reason := "account_cooldown_retest_restored"
		if input.kind == cooldownOutcomeFailure {
			reason = "account_cooldown_retest_backoff"
			if targetFailureBecomesTerminal(target, input) {
				reason = "account_cooldown_retest_timeout"
			}
		}
		if _, err := tx.Exec(ctx, markCooldownAccountRetestOutcomeStatsDirtySQL, target.id, reason, input.now); err != nil {
			return false, fmt.Errorf("mark cooldown account retest group stats dirty: %w", err)
		}
	}
	return true, nil
}

func lockCooldownAccountRetestOutcomeAccounts(ctx context.Context, tx pgx.Tx, task port.CooldownAccountRetestTask) ([]cooldownOutcomeAccountRow, error) {
	rows, err := tx.Query(ctx, lockCooldownAccountRetestOutcomeAccountsSQL, task.AccountID, task.SourceConfigRevision == nil, cooldownOutcomeMaxAuthorizedFamilySize+2)
	if err != nil {
		return nil, fmt.Errorf("lock cooldown account retest outcome accounts: %w", err)
	}
	defer rows.Close()
	result := make([]cooldownOutcomeAccountRow, 0, 4)
	for rows.Next() {
		var row cooldownOutcomeAccountRow
		var expiresAt, cooldownUntil, observationStartedAt pgtype.Timestamptz
		var lastErrorCode, lastErrorMessage, generation pgtype.Text
		var sourceID, authorizationID, ownerSystemID pgtype.Text
		if err := rows.Scan(
			&row.id,
			&row.systemAccountID,
			&row.status,
			&row.schedulable,
			&expiresAt,
			&row.configRevision,
			&row.dispatchRevision,
			&cooldownUntil,
			&lastErrorCode,
			&lastErrorMessage,
			&row.failureCount,
			&observationStartedAt,
			&generation,
			&row.continuousProbeEnabled,
			&sourceID,
			&authorizationID,
			&ownerSystemID,
			&row.providerPresent,
			&row.deleted,
		); err != nil {
			return nil, fmt.Errorf("scan cooldown account retest outcome account: %w", err)
		}
		row.expiresAt = timestamptzPtr(expiresAt)
		row.cooldownUntil = timestamptzPtr(cooldownUntil)
		row.observationStartedAt = timestamptzPtr(observationStartedAt)
		row.lastErrorCode = textValue(lastErrorCode)
		row.lastErrorMessage = textValue(lastErrorMessage)
		row.generation = textValue(generation)
		row.authorizationSourceID = textValue(sourceID)
		row.authorizationID = textValue(authorizationID)
		row.authorizationOwnerSystemID = textValue(ownerSystemID)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read cooldown account retest outcome accounts: %w", err)
	}
	if task.SourceConfigRevision == nil {
		familySize := 0
		for _, row := range result {
			if row.id != task.AccountID && row.authorizationSourceID == task.AccountID && !row.deleted {
				familySize++
			}
		}
		if err := validateCooldownOutcomeFamilySize(familySize); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func advanceCooldownAccountRetestFamily(ctx context.Context, tx pgx.Tx, family []cooldownOutcomeFamilyBatchItem, now time.Time) error {
	if len(family) == 0 {
		return nil
	}
	if err := validateCooldownOutcomeFamilySize(len(family)); err != nil {
		return err
	}
	payload, err := json.Marshal(family)
	if err != nil {
		return fmt.Errorf("encode cooldown account retest authorized family: %w", err)
	}
	var inputCount, updatedCount, insertedCount int64
	if err := tx.QueryRow(ctx, advanceCooldownAccountRetestFamilySQL, string(payload), now, now.UnixMilli(), cooldownOutcomeProjectionKey).Scan(&inputCount, &updatedCount, &insertedCount); err != nil {
		return fmt.Errorf("advance cooldown account retest authorized family: %w", err)
	}
	expectedCount := int64(len(family))
	if inputCount != expectedCount || updatedCount != expectedCount || insertedCount != expectedCount {
		return fmt.Errorf("advance cooldown account retest authorized family count mismatch: input=%d updated=%d outbox=%d expected=%d", inputCount, updatedCount, insertedCount, len(family))
	}
	return nil
}

func validateCooldownOutcomeFamilySize(size int) error {
	if size < 0 || size > cooldownOutcomeMaxAuthorizedFamilySize {
		return fmt.Errorf("cooldown account retest authorized family exceeds limit %d", cooldownOutcomeMaxAuthorizedFamilySize)
	}
	return nil
}

func findCooldownAccountRetestOutcomeReplay(ctx context.Context, tx pgx.Tx, dedupeKey string) (cooldownOutcomeReplayRow, bool, error) {
	var row cooldownOutcomeReplayRow
	err := tx.QueryRow(ctx, findCooldownAccountRetestOutcomeReplaySQL, cooldownOutcomeProjectionKey, dedupeKey).Scan(
		&row.eventID,
		&row.eventType,
		&row.accountID,
		&row.runtimeKey,
		&row.transitionID,
		&row.dispatchRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return cooldownOutcomeReplayRow{}, false, nil
	}
	if err != nil {
		return cooldownOutcomeReplayRow{}, false, fmt.Errorf("find cooldown account retest outcome replay: %w", err)
	}
	return row, true, nil
}

func revalidateCooldownAccountRetestOutcome(
	ctx context.Context,
	tx pgx.Tx,
	task port.CooldownAccountRetestTask,
	now time.Time,
	rows []cooldownOutcomeAccountRow,
) (cooldownOutcomeAccountRow, cooldownOutcomeAccountRow, bool, error) {
	byID := make(map[string]cooldownOutcomeAccountRow, len(rows))
	for _, row := range rows {
		byID[row.id] = row
	}
	target, ok := byID[task.AccountID]
	if !ok || !cooldownOutcomeTargetCurrent(target, task, now) {
		return cooldownOutcomeAccountRow{}, cooldownOutcomeAccountRow{}, false, nil
	}

	source := target
	if task.SourceConfigRevision == nil {
		if target.authorizationSourceID != "" || target.authorizationID != "" || target.authorizationOwnerSystemID != "" {
			return target, cooldownOutcomeAccountRow{}, false, nil
		}
	} else {
		if target.authorizationSourceID == "" || target.authorizationID == "" || target.authorizationOwnerSystemID == "" {
			return target, cooldownOutcomeAccountRow{}, false, nil
		}
		var found bool
		source, found = byID[target.authorizationSourceID]
		if !found || source.deleted || source.configRevision != *task.SourceConfigRevision || !source.providerPresent || source.status != "active" || !source.schedulable || source.systemAccountID != target.authorizationOwnerSystemID {
			return target, source, false, nil
		}
		if source.expiresAt != nil && !source.expiresAt.After(now) {
			return target, source, false, nil
		}
		if source.cooldownUntil != nil && source.cooldownUntil.After(now) {
			return target, source, false, nil
		}
		if source.lastErrorCode == "account_expired" {
			return target, source, false, nil
		}
		var authorized bool
		err := tx.QueryRow(ctx, findCooldownAccountRetestOutcomeAuthorizationSQL,
			target.authorizationID,
			target.authorizationSourceID,
			target.authorizationOwnerSystemID,
			target.systemAccountID,
			now,
		).Scan(&authorized)
		if errors.Is(err, pgx.ErrNoRows) {
			return target, source, false, nil
		}
		if err != nil {
			return target, source, false, fmt.Errorf("find cooldown account retest authorization: %w", err)
		}
	}

	bound, err := findCooldownAccountRetestOutcomeBinding(ctx, tx, target)
	if err != nil || !bound {
		return target, source, false, err
	}
	return target, source, true, nil
}

func cooldownOutcomeTargetCurrent(row cooldownOutcomeAccountRow, task port.CooldownAccountRetestTask, now time.Time) bool {
	if row.deleted || !row.schedulable || (row.status != "temporary_unavailable" && row.status != "rate_limited") || row.configRevision != task.ConfigRevision || row.dispatchRevision != int64(task.DispatchRevision) {
		return false
	}
	if row.expiresAt != nil && !row.expiresAt.After(now) {
		return false
	}
	if row.observationStartedAt == nil || task.ObservationStartedAt == nil || !row.observationStartedAt.Equal(*task.ObservationStartedAt) {
		return false
	}
	return row.generation == task.Generation
}

func findCooldownAccountRetestOutcomeBinding(ctx context.Context, tx pgx.Tx, target cooldownOutcomeAccountRow) (bool, error) {
	var groupID string
	err := tx.QueryRow(ctx, findCooldownAccountRetestOutcomeBindingSQL, target.id, target.systemAccountID, target.authorizationID).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find cooldown account retest binding: %w", err)
	}
	return groupID != "", nil
}

func mutateCooldownAccountRetestOutcome(ctx context.Context, tx pgx.Tx, input cooldownOutcomeInput, target cooldownOutcomeAccountRow) (int64, bool, error) {
	var revision int64
	var err error
	switch input.kind {
	case cooldownOutcomeSuccess:
		err = tx.QueryRow(ctx, restoreCooldownAccountRetestOutcomeSQL,
			target.id,
			input.now,
			input.task.ConfigRevision,
			input.task.DispatchRevision,
			input.task.ObservationStartedAt,
			input.task.Generation,
			target.systemAccountID,
			target.authorizationID,
			target.authorizationSourceID,
			target.authorizationOwnerSystemID,
		).Scan(&revision)
	case cooldownOutcomeDefer:
		cooldownUntil := input.now.Add(normalizeCooldownOutcomeDeferDelay(input.delay))
		err = tx.QueryRow(ctx, deferCooldownAccountRetestOutcomeSQL,
			target.id,
			cooldownUntil,
			input.now,
			input.task.ConfigRevision,
			input.task.DispatchRevision,
			input.task.ObservationStartedAt,
			input.task.Generation,
			target.systemAccountID,
			target.authorizationID,
			target.authorizationSourceID,
			target.authorizationOwnerSystemID,
		).Scan(&revision)
	case cooldownOutcomeFailure:
		currentFailureCount := target.failureCount
		if currentFailureCount < 0 {
			currentFailureCount = 0
		}
		if currentFailureCount >= math.MaxInt32 {
			return 0, false, fmt.Errorf("cooldown account retest failure count overflow")
		}
		target.failureCount = currentFailureCount
		plan := cooldownAccountRetestRecoveryPlan(target, input.task, input.now)
		terminal := plan.stage == "terminal"
		var cooldownUntil *time.Time
		if !terminal {
			calculated := input.now.Add(time.Duration(plan.backoffSeconds) * time.Second)
			if target.cooldownUntil != nil && target.cooldownUntil.After(calculated) {
				calculated = *target.cooldownUntil
			}
			cooldownUntil = &calculated
		}
		errorCode, testMessage, traceID := normalizeCooldownAccountRetestFailure(input.probe)
		persistedCode := cooldownAccountRetestPersistedErrorCode(target, terminal, plan.stage, errorCode)
		failureMessage := cooldownAccountRetestFailureMessage(target.failureCount+1, plan, testMessage)
		var statusCode *int
		if input.probe.StatusCode != 0 {
			value := input.probe.StatusCode
			statusCode = &value
		}
		err = tx.QueryRow(ctx, failCooldownAccountRetestOutcomeSQL,
			target.id,
			terminal,
			cooldownUntil,
			persistedCode,
			failureMessage,
			traceID,
			target.failureCount+1,
			plan.observationStartedAt,
			input.now,
			statusCode,
			target.status,
			input.task.ConfigRevision,
			input.task.DispatchRevision,
			input.task.ObservationStartedAt,
			input.task.Generation,
			target.systemAccountID,
			target.authorizationID,
			target.authorizationSourceID,
			target.authorizationOwnerSystemID,
		).Scan(&revision)
	default:
		return 0, false, fmt.Errorf("unsupported cooldown account retest outcome %q", input.kind)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("mutate cooldown account retest %s outcome: %w", input.kind, err)
	}
	return revision, true, nil
}

func insertCooldownAccountRetestOutcomeOutbox(ctx context.Context, tx pgx.Tx, accountID, transitionID string, revision int64, now time.Time) error {
	if _, err := tx.Exec(ctx, insertCooldownAccountRetestOutcomeOutboxSQL,
		uuid.NewString(),
		cooldownOutcomeProjectionKey,
		"dispatch:"+transitionID,
		accountID,
		transitionID,
		revision,
		now.UnixMilli(),
	); err != nil {
		return fmt.Errorf("insert cooldown account retest outcome outbox: %w", err)
	}
	return nil
}

func validateCooldownOutcomeInput(input cooldownOutcomeInput) error {
	version := accounthealth.RetestTaskVersion{
		ConfigRevision: input.task.ConfigRevision, DispatchRevision: input.task.DispatchRevision,
		ObservationStartedAt: input.task.ObservationStartedAt, Generation: input.task.Generation,
		SourceConfigRevision: input.task.SourceConfigRevision,
	}
	if strings.TrimSpace(input.task.AccountID) == "" || strings.TrimSpace(input.task.AccountID) != input.task.AccountID || !utf8.ValidString(input.task.AccountID) || !accounthealth.CooldownRetestTaskVersionValid(version) {
		return fmt.Errorf("cooldown account retest outcome fence is invalid")
	}
	if input.task.MaxPauseMinutes < 1 || input.task.MaxPauseMinutes > 1440 || input.task.MaxRecoveryHours < 1 || input.task.MaxRecoveryHours > 24*30 {
		return fmt.Errorf("cooldown account retest outcome recovery settings are invalid")
	}
	if input.kind != cooldownOutcomeSuccess && input.kind != cooldownOutcomeDefer && input.kind != cooldownOutcomeFailure {
		return fmt.Errorf("cooldown account retest outcome kind is invalid")
	}
	if input.kind == cooldownOutcomeFailure && (!utf8.ValidString(input.probe.ErrorCode) || !utf8.ValidString(input.probe.Message) || !utf8.ValidString(input.probe.TraceID)) {
		return fmt.Errorf("cooldown account retest outcome diagnostics are invalid")
	}
	return nil
}

func normalizeCooldownOutcomeDeferDelay(delay time.Duration) time.Duration {
	seconds := int64(delay / time.Second)
	if seconds < 3 {
		seconds = 3
	}
	if seconds > 15*60 {
		seconds = 15 * 60
	}
	return time.Duration(seconds) * time.Second
}

func cooldownAccountRetestOutcomeTransitionID(task port.CooldownAccountRetestTask, kind cooldownOutcomeKind) string {
	observation := ""
	if task.ObservationStartedAt != nil {
		observation = task.ObservationStartedAt.UTC().Format(time.RFC3339Nano)
	}
	sourceRevision := "owner"
	if task.SourceConfigRevision != nil {
		sourceRevision = fmt.Sprintf("%d", *task.SourceConfigRevision)
	}
	raw := fmt.Sprintf("%s\x1f%d\x1f%d\x1f%s\x1f%s\x1f%s\x1f%s",
		task.AccountID, task.ConfigRevision, task.DispatchRevision, observation, task.Generation, sourceRevision, kind)
	digest := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("cooldown-retest:v1:%x", digest[:])
}

func cooldownAccountRetestFamilyTransitionID(rootTransitionID, accountID string) string {
	digest := sha256.Sum256([]byte(rootTransitionID + "\x1f" + accountID))
	return fmt.Sprintf("cooldown-retest-family:v1:%x", digest[:])
}

func cooldownAccountRetestRecoveryPlan(target cooldownOutcomeAccountRow, task port.CooldownAccountRetestTask, now time.Time) cooldownOutcomeRecoveryPlan {
	observationStartedAt := now
	if target.observationStartedAt != nil {
		observationStartedAt = target.observationStartedAt.UTC()
	}
	elapsed := 0
	if now.After(observationStartedAt) {
		elapsed = int(now.Sub(observationStartedAt) / time.Second)
	}
	bounded := target.status == "temporary_unavailable" && target.continuousProbeEnabled == 0
	timeout := cooldownOutcomeObservationTimeoutSeconds
	if bounded {
		timeout = cooldownOutcomeLimitedProbeTimeoutSeconds
	}
	maxRecoverySeconds := task.MaxRecoveryHours * 60 * 60
	terminal := elapsed >= timeout
	longTerm := !bounded && elapsed >= maxRecoverySeconds
	backoff := boundedCooldownOutcomeBackoff(target.failureCount+1, task.MaxPauseMinutes*60)
	stage := "fast"
	if terminal {
		stage = "terminal"
		backoff = 0
	} else if longTerm {
		stage = "long_term"
		backoff = cooldownOutcomeLongTermIntervalSeconds
	} else if backoff > cooldownOutcomeFastThresholdSeconds {
		stage = "slow"
	}
	if bounded && !terminal {
		remaining := timeout - elapsed
		if remaining < 1 {
			remaining = 1
		}
		if backoff > remaining {
			backoff = remaining
		}
		if backoff <= cooldownOutcomeFastThresholdSeconds {
			stage = "fast"
		} else {
			stage = "slow"
		}
	}
	return cooldownOutcomeRecoveryPlan{
		stage: stage, backoffSeconds: backoff, maxRecoverySeconds: maxRecoverySeconds,
		observationStartedAt: observationStartedAt, observationElapsedSeconds: elapsed,
		observationTimeoutSeconds: timeout,
	}
}

func boundedCooldownOutcomeBackoff(failureCount, maxPauseSeconds int) int {
	backoff := cooldownOutcomeInitialBackoffSeconds
	for index := 1; index < failureCount && backoff < maxPauseSeconds; index++ {
		if backoff > maxPauseSeconds/cooldownOutcomeBackoffMultiplier {
			backoff = maxPauseSeconds
			break
		}
		backoff *= cooldownOutcomeBackoffMultiplier
	}
	if backoff > maxPauseSeconds {
		return maxPauseSeconds
	}
	return backoff
}

func normalizeCooldownAccountRetestFailure(probe port.CooldownAccountRetestProbeResult) (string, string, string) {
	errorCode := probe.ErrorCode
	if errorCode == "" {
		if probe.StatusCode != 0 {
			errorCode = fmt.Sprintf("http_%d", probe.StatusCode)
		} else {
			errorCode = "cooldown_retest_failed"
		}
	}
	errorCode = truncateCooldownOutcomeUTF16(errorCode, 120)
	message := probe.Message
	if message == "" {
		message = "后台冷却复测失败"
	}
	rawTraceID := probe.TraceID
	traceID := truncateCooldownOutcomeUTF16(rawTraceID, 200)
	parts := make([]string, 0, 4)
	if rawTraceID != "" && !strings.Contains(message, rawTraceID) {
		parts = append(parts, "traceId "+rawTraceID)
	}
	if probe.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", probe.StatusCode))
	}
	if errorCode != "" && !strings.HasPrefix(errorCode, "http_") && !strings.Contains(message, errorCode) {
		parts = append(parts, errorCode)
	}
	parts = append(parts, message)
	return errorCode, truncateCooldownOutcomeUTF16(strings.Join(parts, "；"), 1000), traceID
}

func cooldownAccountRetestPersistedErrorCode(target cooldownOutcomeAccountRow, terminal bool, stage, errorCode string) string {
	if terminal {
		if target.status == "temporary_unavailable" && target.continuousProbeEnabled == 0 {
			return cooldownOutcomeLimitedProbeTimeoutCode
		}
		return cooldownOutcomeObservationTimeoutCode
	}
	if target.lastErrorCode == cooldownOutcomeExplicitPolicyCode || (target.lastErrorCode == "" && strings.HasPrefix(target.lastErrorMessage, cooldownOutcomeLegacyExplicitPolicyMessageLead)) {
		return cooldownOutcomeExplicitPolicyCode
	}
	if stage == "long_term" {
		return cooldownOutcomeLongTermUnavailableCode
	}
	return errorCode
}

func cooldownAccountRetestFailureMessage(failureCount int, plan cooldownOutcomeRecoveryPlan, lastError string) string {
	var message string
	switch plan.stage {
	case "terminal":
		message = fmt.Sprintf("后台冷却复测连续失败 %d 次，从自动恢复观察开始 %s 起已持续 %s仍未恢复，账户已转为异常；最后错误：%s",
			failureCount,
			plan.observationStartedAt.Format("2006-01-02T15:04:05.000Z"),
			formatCooldownOutcomeObservationWindow(plan.observationTimeoutSeconds),
			lastError,
		)
	case "long_term":
		message = fmt.Sprintf("后台冷却复测连续失败 %d 次，已超过自动恢复观察窗口 %s，进入长期不可用每 1 小时复测；下次复测延后 %s；最后错误：%s",
			failureCount,
			formatCooldownOutcomeDuration(plan.maxRecoverySeconds),
			formatCooldownOutcomeDuration(plan.backoffSeconds),
			lastError,
		)
	default:
		stageText := "快速恢复通道"
		if plan.stage == "slow" {
			stageText = "慢速恢复通道"
		}
		message = fmt.Sprintf("后台冷却复测连续失败 %d 次，%s下次复测延后 %s；最后错误：%s",
			failureCount, stageText, formatCooldownOutcomeDuration(plan.backoffSeconds), lastError)
	}
	return truncateCooldownOutcomeUTF16(message, 1000)
}

func formatCooldownOutcomeObservationWindow(seconds int) string {
	if days := seconds / (24 * 60 * 60); days > 0 && days*24*60*60 == seconds {
		return fmt.Sprintf("%d 天", days)
	}
	return formatCooldownOutcomeDuration(seconds)
}

func formatCooldownOutcomeDuration(seconds int) string {
	if seconds < 1 {
		seconds = 1
	}
	if seconds < 60 {
		return fmt.Sprintf("%d 秒", seconds)
	}
	minutes := seconds / 60
	restSeconds := seconds % 60
	if minutes < 60 {
		if restSeconds > 0 {
			return fmt.Sprintf("%d 分钟 %d 秒", minutes, restSeconds)
		}
		return fmt.Sprintf("%d 分钟", minutes)
	}
	hours := minutes / 60
	restMinutes := minutes % 60
	if restMinutes > 0 {
		return fmt.Sprintf("%d 小时 %d 分钟", hours, restMinutes)
	}
	return fmt.Sprintf("%d 小时", hours)
}

func truncateCooldownOutcomeUTF16(value string, maxUnits int) string {
	if maxUnits <= 0 || value == "" {
		return ""
	}
	units := utf16.Encode([]rune(value))
	if len(units) <= maxUnits {
		return value
	}
	// Node slices UTF-16 code units. A dangling surrogate becomes U+FFFD when
	// node-postgres encodes the string as UTF-8 for PostgreSQL.
	return string(utf16.Decode(units[:maxUnits]))
}

func targetFailureBecomesTerminal(target cooldownOutcomeAccountRow, input cooldownOutcomeInput) bool {
	return cooldownAccountRetestRecoveryPlan(target, input.task, input.now).stage == "terminal"
}

func rollbackCooldownAccountRetestOutcome(tx pgx.Tx) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tx.Rollback(rollbackCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return fmt.Errorf("rollback unapplied cooldown account retest outcome: %w", err)
	}
	return nil
}

func rollbackCooldownAccountRetestOutcomeTx(tx pgx.Tx, committed *bool) func() {
	return func() {
		if *committed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	}
}

var _ port.CooldownAccountRetestOutcomeStore = (*Store)(nil)
