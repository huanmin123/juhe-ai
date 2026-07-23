package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

const gatewayAccountPolicyProjectionKey = "account_circuit_runtime_v1"

type gatewayAccountPolicyLockedRow struct {
	id                   string
	systemAccountID      string
	status               string
	schedulable          bool
	expiresAt            *time.Time
	configRevision       int
	dispatchRevision     int64
	authorizationSource  string
	authorizationID      string
	authorizationOwnerID string
	deleted              bool
}

type gatewayAccountPolicyOutboxRow struct {
	eventID          string
	eventType        string
	accountID        string
	runtimeKey       string
	transitionID     string
	dispatchRevision int64
}

func (s *Store) ApplyGatewayAccountPolicy(ctx context.Context, input port.GatewayAccountPolicyWriteInput) (port.GatewayAccountPolicyWriteResult, error) {
	if err := validateGatewayAccountPolicyWriteInput(input); err != nil {
		return port.GatewayAccountPolicyWriteResult{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.GatewayAccountPolicyWriteResult{}, fmt.Errorf("begin gateway account policy transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	result, err := applyGatewayAccountPolicyInTx(ctx, tx, input)
	if err != nil {
		return port.GatewayAccountPolicyWriteResult{}, err
	}
	if result.Status != port.GatewayAccountPolicyWriteApplied {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(rollbackCtx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			return port.GatewayAccountPolicyWriteResult{}, fmt.Errorf("rollback unapplied gateway account policy transaction: %w", err)
		}
		committed = true
		return result, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return port.GatewayAccountPolicyWriteResult{}, fmt.Errorf("commit gateway account policy transaction: %w", err)
	}
	committed = true
	return result, nil
}

func applyGatewayAccountPolicyInTx(ctx context.Context, tx pgx.Tx, input port.GatewayAccountPolicyWriteInput) (port.GatewayAccountPolicyWriteResult, error) {
	locked, err := lockGatewayAccountPolicyRows(ctx, tx, input)
	if err != nil {
		return port.GatewayAccountPolicyWriteResult{}, err
	}
	dedupeKey := "dispatch:" + input.TransitionID
	replay, replayFound, err := findGatewayAccountPolicyOutbox(ctx, tx, dedupeKey)
	if err != nil {
		return port.GatewayAccountPolicyWriteResult{}, err
	}
	if replayFound {
		if replay.eventType != "dispatch_revision_changed" || replay.accountID != input.Target.AccountID || replay.runtimeKey != input.Target.AccountRuntimeKey || replay.transitionID != input.TransitionID {
			return port.GatewayAccountPolicyWriteResult{}, fmt.Errorf("gateway account policy transition conflicts with existing outbox identity")
		}
		return gatewayAccountPolicyStatus(input, port.GatewayAccountPolicyWriteIdempotent, replay.dispatchRevision, replay.eventID), nil
	}
	target, targetFound := locked[input.Target.AccountID]
	if !targetFound {
		return gatewayAccountPolicyStatus(input, port.GatewayAccountPolicyWriteIneligible, 0, ""), nil
	}
	source, sourceFound := locked[input.Source.AccountID]
	if !sourceFound {
		return gatewayAccountPolicyStatus(input, port.GatewayAccountPolicyWriteStaleSource, 0, ""), nil
	}

	if target.configRevision != input.Target.ExpectedConfigRevision || target.dispatchRevision != input.Target.ExpectedDispatchRevision || target.status != input.Target.ExpectedStatus {
		return gatewayAccountPolicyStatus(input, port.GatewayAccountPolicyWriteStaleTarget, target.dispatchRevision, ""), nil
	}
	if source.configRevision != input.Source.ExpectedConfigRevision || source.dispatchRevision != input.Source.ExpectedDispatchRevision {
		return gatewayAccountPolicyStatus(input, port.GatewayAccountPolicyWriteStaleSource, target.dispatchRevision, ""), nil
	}
	if !gatewayAccountPolicyTargetEligible(target, source, input) {
		return gatewayAccountPolicyStatus(input, port.GatewayAccountPolicyWriteIneligible, target.dispatchRevision, ""), nil
	}

	var eligible bool
	if err := tx.QueryRow(ctx, lockGatewayAccountPolicyBindingSQL,
		input.Target.GroupID,
		input.Target.AccountID,
		input.Target.SystemAccountID,
		input.Target.AccountAuthorizationID,
	).Scan(&eligible); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gatewayAccountPolicyStatus(input, port.GatewayAccountPolicyWriteIneligible, target.dispatchRevision, ""), nil
		}
		return port.GatewayAccountPolicyWriteResult{}, fmt.Errorf("revalidate gateway account policy binding: %w", err)
	}
	if input.Target.AccountAuthorizationID != "" {
		if err := tx.QueryRow(ctx, lockGatewayAccountPolicyAuthorizationSQL,
			input.Target.AccountAuthorizationID,
			input.Target.AuthorizationSourceAccountID,
			input.Target.AuthorizationOwnerSystemAccountID,
			input.Target.SystemAccountID,
			input.AppliedAt.UTC(),
		).Scan(&eligible); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return gatewayAccountPolicyStatus(input, port.GatewayAccountPolicyWriteIneligible, target.dispatchRevision, ""), nil
			}
			return port.GatewayAccountPolicyWriteResult{}, fmt.Errorf("revalidate gateway account policy authorization: %w", err)
		}
	}

	var dispatchRevision int64
	switch input.Action {
	case port.GatewayAccountPolicyCooldown:
		err = tx.QueryRow(ctx, applyGatewayAccountPolicyCooldownSQL,
			input.Target.AccountID,
			string(input.CooldownStatus),
			input.CooldownUntil.UTC(),
			input.Reason,
			input.TraceID,
			input.AppliedAt.UTC(),
			input.Target.ExpectedStatus,
			input.Target.ExpectedConfigRevision,
			input.Target.ExpectedDispatchRevision,
		).Scan(&dispatchRevision)
	case port.GatewayAccountPolicyDisable:
		err = tx.QueryRow(ctx, applyGatewayAccountPolicyDisableSQL,
			input.Target.AccountID,
			input.Reason,
			input.AppliedAt.UTC(),
			input.Target.ExpectedStatus,
			input.Target.ExpectedConfigRevision,
			input.Target.ExpectedDispatchRevision,
		).Scan(&dispatchRevision)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return gatewayAccountPolicyStatus(input, port.GatewayAccountPolicyWriteStaleTarget, target.dispatchRevision, ""), nil
	}
	if err != nil {
		return port.GatewayAccountPolicyWriteResult{}, fmt.Errorf("update gateway account policy target: %w", err)
	}

	eventID := uuid.NewString()
	nowMS := input.AppliedAt.UTC().UnixMilli()
	if _, err := tx.Exec(ctx, insertGatewayAccountPolicyOutboxSQL,
		eventID,
		gatewayAccountPolicyProjectionKey,
		dedupeKey,
		input.Target.AccountID,
		input.Target.AccountRuntimeKey,
		input.TransitionID,
		dispatchRevision,
		nowMS,
	); err != nil {
		return port.GatewayAccountPolicyWriteResult{}, fmt.Errorf("insert gateway account policy outbox: %w", err)
	}
	if input.Target.AccountAuthorizationID == "" && input.Source.AccountID == input.Target.AccountID {
		for _, row := range locked {
			if row.id == input.Target.AccountID || row.authorizationSource != input.Target.AccountID || row.deleted {
				continue
			}
			var familyRevision int64
			if err := tx.QueryRow(ctx, advanceGatewayAccountPolicyFamilyRevisionSQL, row.id, row.dispatchRevision, input.AppliedAt.UTC()).Scan(&familyRevision); err != nil {
				return port.GatewayAccountPolicyWriteResult{}, fmt.Errorf("advance authorized policy family revision: %w", err)
			}
			familyTransitionID := gatewayAccountPolicyFamilyTransitionID(input.TransitionID, row.id)
			if _, err := tx.Exec(ctx, insertGatewayAccountPolicyOutboxSQL,
				uuid.NewString(),
				gatewayAccountPolicyProjectionKey,
				"dispatch:"+familyTransitionID,
				row.id,
				row.id,
				familyTransitionID,
				familyRevision,
				nowMS,
			); err != nil {
				return port.GatewayAccountPolicyWriteResult{}, fmt.Errorf("insert authorized policy family outbox: %w", err)
			}
		}
	}
	if _, err := tx.Exec(ctx, markGatewayAccountPolicyStatsDirtySQL, input.Target.AccountID, "account_error_policy", input.AppliedAt.UTC()); err != nil {
		return port.GatewayAccountPolicyWriteResult{}, fmt.Errorf("mark gateway account policy stats dirty: %w", err)
	}
	return gatewayAccountPolicyStatus(input, port.GatewayAccountPolicyWriteApplied, dispatchRevision, eventID), nil
}

func lockGatewayAccountPolicyRows(ctx context.Context, tx pgx.Tx, input port.GatewayAccountPolicyWriteInput) (map[string]gatewayAccountPolicyLockedRow, error) {
	ids := []string{input.Target.AccountID}
	if input.Source.AccountID != input.Target.AccountID {
		ids = append(ids, input.Source.AccountID)
	}
	familySourceID := ""
	if input.Target.AccountAuthorizationID == "" && input.Source.AccountID == input.Target.AccountID {
		familySourceID = input.Target.AccountID
	}
	rows, err := tx.Query(ctx, lockGatewayAccountPolicyRowsSQL, ids, familySourceID)
	if err != nil {
		return nil, fmt.Errorf("lock gateway account policy rows: %w", err)
	}
	defer rows.Close()
	result := make(map[string]gatewayAccountPolicyLockedRow, len(ids))
	for rows.Next() {
		var row gatewayAccountPolicyLockedRow
		var expiresAt, deletedAt pgtype.Timestamptz
		var sourceID, authorizationID, ownerID pgtype.Text
		if err := rows.Scan(
			&row.id,
			&row.systemAccountID,
			&row.status,
			&row.schedulable,
			&expiresAt,
			&row.configRevision,
			&row.dispatchRevision,
			&sourceID,
			&authorizationID,
			&ownerID,
			&deletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan gateway account policy row: %w", err)
		}
		row.expiresAt = timestamptzPtr(expiresAt)
		row.authorizationSource = textValue(sourceID)
		row.authorizationID = textValue(authorizationID)
		row.authorizationOwnerID = textValue(ownerID)
		row.deleted = deletedAt.Valid
		result[row.id] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read gateway account policy rows: %w", err)
	}
	return result, nil
}

func findGatewayAccountPolicyOutbox(ctx context.Context, tx pgx.Tx, dedupeKey string) (gatewayAccountPolicyOutboxRow, bool, error) {
	var row gatewayAccountPolicyOutboxRow
	err := tx.QueryRow(ctx, findGatewayAccountPolicyOutboxSQL, gatewayAccountPolicyProjectionKey, dedupeKey).Scan(
		&row.eventID,
		&row.eventType,
		&row.accountID,
		&row.runtimeKey,
		&row.transitionID,
		&row.dispatchRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return gatewayAccountPolicyOutboxRow{}, false, nil
	}
	if err != nil {
		return gatewayAccountPolicyOutboxRow{}, false, fmt.Errorf("find gateway account policy outbox: %w", err)
	}
	return row, true, nil
}

func gatewayAccountPolicyTargetEligible(target, source gatewayAccountPolicyLockedRow, input port.GatewayAccountPolicyWriteInput) bool {
	now := input.AppliedAt.UTC()
	if target.deleted || source.deleted || !target.schedulable || !source.schedulable || target.systemAccountID != input.Target.SystemAccountID {
		return false
	}
	if (target.expiresAt != nil && !target.expiresAt.After(now)) || (source.expiresAt != nil && !source.expiresAt.After(now)) {
		return false
	}
	if !gatewayPolicyMutableStatus(target.status) || !gatewayPolicyMutableStatus(source.status) {
		return false
	}
	if source.id != target.id && source.status != "active" {
		return false
	}
	if input.Target.AccountAuthorizationID == "" {
		return target.authorizationSource == "" && target.authorizationID == "" && target.authorizationOwnerID == "" && input.Source.AccountID == input.Target.AccountID
	}
	return target.authorizationSource == input.Target.AuthorizationSourceAccountID &&
		target.authorizationID == input.Target.AccountAuthorizationID &&
		target.authorizationOwnerID == input.Target.AuthorizationOwnerSystemAccountID &&
		input.Source.AccountID == input.Target.AuthorizationSourceAccountID &&
		source.systemAccountID == input.Target.AuthorizationOwnerSystemAccountID
}

func gatewayPolicyMutableStatus(status string) bool {
	switch status {
	case "active", "rate_limited", "temporary_unavailable":
		return true
	default:
		return false
	}
}

func gatewayAccountPolicyStatus(input port.GatewayAccountPolicyWriteInput, status port.GatewayAccountPolicyWriteStatus, revision int64, eventID string) port.GatewayAccountPolicyWriteResult {
	return port.GatewayAccountPolicyWriteResult{
		Status:                 status,
		TransitionID:           input.TransitionID,
		TargetDispatchRevision: revision,
		OutboxEventID:          eventID,
	}
}

func gatewayAccountPolicyFamilyTransitionID(rootTransitionID, accountID string) string {
	digest := sha256.Sum256([]byte(rootTransitionID + "\x1f" + accountID))
	return fmt.Sprintf("gateway-policy-family:v1:%x", digest[:])
}

func validateGatewayAccountPolicyWriteInput(input port.GatewayAccountPolicyWriteInput) error {
	if !canonicalGatewayPolicyText(input.TransitionID) || len(input.TransitionID) > 247 || strings.ContainsAny(input.TransitionID, "\r\n") {
		return fmt.Errorf("gateway account policy transition id is invalid")
	}
	if !canonicalGatewayPolicyText(input.Target.AccountID) || !canonicalGatewayPolicyText(input.Target.SystemAccountID) || !canonicalGatewayPolicyText(input.Target.GroupID) ||
		!canonicalGatewayPolicyText(input.Target.AccountRuntimeKey) || input.Target.AccountRuntimeKey != input.Target.AccountID || len(input.Target.AccountRuntimeKey) > 1024 || !canonicalGatewayPolicyText(input.Source.AccountID) {
		return fmt.Errorf("gateway account policy target identity is invalid")
	}
	for _, value := range []string{input.Target.AccountAuthorizationID, input.Target.AuthorizationSourceAccountID, input.Target.AuthorizationOwnerSystemAccountID} {
		if value != "" && !canonicalGatewayPolicyText(value) {
			return fmt.Errorf("gateway account policy authorization identity is invalid")
		}
	}
	if input.Target.AccountAuthorizationID == "" {
		if input.Target.AuthorizationSourceAccountID != "" || input.Target.AuthorizationOwnerSystemAccountID != "" || input.Source.AccountID != input.Target.AccountID {
			return fmt.Errorf("gateway account policy direct target identity is invalid")
		}
	} else if input.Target.AuthorizationSourceAccountID == "" || input.Target.AuthorizationOwnerSystemAccountID == "" || input.Source.AccountID != input.Target.AuthorizationSourceAccountID {
		return fmt.Errorf("gateway account policy authorized target identity is invalid")
	}
	if input.Target.ExpectedConfigRevision < 1 || input.Target.ExpectedDispatchRevision < 1 || input.Source.ExpectedConfigRevision < 1 || input.Source.ExpectedDispatchRevision < 1 {
		return fmt.Errorf("gateway account policy revision fence is invalid")
	}
	if !gatewayPolicyMutableStatus(input.Target.ExpectedStatus) {
		return fmt.Errorf("gateway account policy expected status is invalid")
	}
	if input.AppliedAt.IsZero() {
		return fmt.Errorf("gateway account policy applied time is required")
	}
	if strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 1000 || !utf8.ValidString(input.Reason) || len(input.TraceID) > 200 || !utf8.ValidString(input.TraceID) {
		return fmt.Errorf("gateway account policy diagnostic facts are invalid")
	}
	switch input.Action {
	case port.GatewayAccountPolicyCooldown:
		if input.CooldownStatus != port.GatewayAccountPolicyRateLimited && input.CooldownStatus != port.GatewayAccountPolicyTemporaryUnavailable {
			return fmt.Errorf("gateway account policy cooldown status is invalid")
		}
		if input.CooldownUntil == nil || !input.CooldownUntil.After(input.AppliedAt) {
			return fmt.Errorf("gateway account policy cooldown deadline is invalid")
		}
	case port.GatewayAccountPolicyDisable:
		if input.CooldownStatus != "" || input.CooldownUntil != nil {
			return fmt.Errorf("gateway account disable policy cannot contain cooldown")
		}
	default:
		return fmt.Errorf("gateway account policy action is invalid")
	}
	return nil
}

func canonicalGatewayPolicyText(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.ValidString(value)
}

var _ port.GatewayAccountPolicyWriter = (*Store)(nil)
