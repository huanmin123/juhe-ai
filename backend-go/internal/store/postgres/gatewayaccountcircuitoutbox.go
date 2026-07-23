package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) ClaimGatewayAccountCircuitOutbox(ctx context.Context, input port.GatewayAccountCircuitOutboxClaimInput) ([]port.GatewayAccountCircuitOutboxEvent, error) {
	if err := validateGatewayAccountCircuitOutboxClaim(input); err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin gateway account circuit outbox claim: %w", err)
	}
	committed := false
	defer rollbackGatewayAccountCircuitOutboxTx(tx, &committed)()

	nowMS := input.Now.UTC().UnixMilli()
	claimSeed := uuid.NewString()
	claimUntilMS := input.Now.UTC().Add(input.Lease).UnixMilli()
	rows, err := tx.Query(ctx, claimGatewayAccountCircuitOutboxSQL,
		port.GatewayAccountCircuitProjectionKey,
		nowMS,
		input.Limit,
		claimSeed,
		input.OwnerID,
		claimUntilMS,
	)
	if err != nil {
		return nil, fmt.Errorf("claim gateway account circuit outbox: %w", err)
	}
	claimed := make([]port.GatewayAccountCircuitOutboxEvent, 0, input.Limit)
	for rows.Next() {
		var event port.GatewayAccountCircuitOutboxEvent
		var createdAtMS int64
		if err := rows.Scan(
			&event.EventID,
			&event.ProjectionKey,
			&event.AccountID,
			&event.AccountRuntimeKey,
			&event.TransitionID,
			&event.DispatchRevision,
			&event.ClaimToken,
			&event.AttemptCount,
			&createdAtMS,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan gateway account circuit outbox claim: %w", err)
		}
		event.CreatedAt = time.UnixMilli(createdAtMS).UTC()
		claimed = append(claimed, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read gateway account circuit outbox claims: %w", err)
	}
	rows.Close()

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit gateway account circuit outbox claim: %w", err)
	}
	committed = true
	return claimed, nil
}

func (s *Store) AcknowledgeGatewayAccountCircuitOutbox(ctx context.Context, input port.GatewayAccountCircuitOutboxAcknowledgeInput) (bool, error) {
	if err := validateGatewayAccountCircuitOutboxAcknowledge(input); err != nil {
		return false, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin gateway account circuit outbox acknowledge: %w", err)
	}
	committed := false
	defer rollbackGatewayAccountCircuitOutboxTx(tx, &committed)()

	var projectionKey, eventType, accountID, status string
	var dispatchRevision int64
	var claimToken pgtype.Text
	err = tx.QueryRow(ctx, lockGatewayAccountCircuitOutboxForAcknowledgeSQL, input.EventID).Scan(
		&projectionKey, &eventType, &accountID, &dispatchRevision, &status, &claimToken,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock gateway account circuit outbox acknowledge: %w", err)
	}
	if projectionKey != input.ProjectionKey || eventType != "dispatch_revision_changed" {
		return false, nil
	}
	if status == "dispatched" {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit idempotent gateway account circuit outbox acknowledge: %w", err)
		}
		committed = true
		return true, nil
	}
	if status != "processing" || !claimToken.Valid || claimToken.String != input.ClaimToken {
		return false, nil
	}
	nowMS := input.AcknowledgedAt.UTC().UnixMilli()
	command, err := tx.Exec(ctx, acknowledgeGatewayAccountCircuitOutboxSQL, input.EventID, input.ProjectionKey, input.ClaimToken, nowMS)
	if err != nil {
		return false, fmt.Errorf("acknowledge gateway account circuit outbox: %w", err)
	}
	if command.RowsAffected() != 1 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, advanceGatewayAccountCircuitProjectionRevisionSQL, accountID, dispatchRevision); err != nil {
		return false, fmt.Errorf("advance gateway account circuit projection revision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit gateway account circuit outbox acknowledge: %w", err)
	}
	committed = true
	return true, nil
}

func (s *Store) ReleaseGatewayAccountCircuitOutbox(ctx context.Context, input port.GatewayAccountCircuitOutboxReleaseInput) (bool, error) {
	if err := validateGatewayAccountCircuitOutboxRelease(input); err != nil {
		return false, err
	}
	now := input.Now.UTC()
	command, err := s.pool.Exec(ctx, releaseGatewayAccountCircuitOutboxSQL,
		input.EventID,
		input.ClaimToken,
		input.ErrorClass,
		now.Add(input.RetryDelay).UnixMilli(),
		now.UnixMilli(),
	)
	if err != nil {
		return false, fmt.Errorf("release gateway account circuit outbox: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

func rollbackGatewayAccountCircuitOutboxTx(tx pgx.Tx, committed *bool) func() {
	return func() {
		if *committed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	}
}

func validateGatewayAccountCircuitOutboxClaim(input port.GatewayAccountCircuitOutboxClaimInput) error {
	if !validGatewayAccountCircuitOutboxText(input.OwnerID, 128) || input.Now.IsZero() || input.Lease <= 0 || input.Lease > time.Hour || input.Limit < 1 || input.Limit > port.GatewayAccountCircuitOutboxMaxBatch {
		return fmt.Errorf("gateway account circuit outbox claim is invalid")
	}
	return nil
}

func validateGatewayAccountCircuitOutboxAcknowledge(input port.GatewayAccountCircuitOutboxAcknowledgeInput) error {
	if !validGatewayAccountCircuitOutboxText(input.EventID, 256) || input.ProjectionKey != port.GatewayAccountCircuitProjectionKey || !validGatewayAccountCircuitOutboxText(input.ClaimToken, 256) || input.AcknowledgedAt.IsZero() {
		return fmt.Errorf("gateway account circuit outbox acknowledge is invalid")
	}
	return nil
}

func validateGatewayAccountCircuitOutboxRelease(input port.GatewayAccountCircuitOutboxReleaseInput) error {
	if !validGatewayAccountCircuitOutboxText(input.EventID, 256) || !validGatewayAccountCircuitOutboxText(input.ClaimToken, 256) || !validGatewayAccountCircuitOutboxText(input.ErrorClass, 64) || input.Now.IsZero() || input.RetryDelay < 0 || input.RetryDelay > 24*time.Hour {
		return fmt.Errorf("gateway account circuit outbox release is invalid")
	}
	return nil
}

func validGatewayAccountCircuitOutboxText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

var _ port.GatewayAccountCircuitOutboxStore = (*Store)(nil)
