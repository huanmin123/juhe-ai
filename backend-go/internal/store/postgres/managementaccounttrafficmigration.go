package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

func (s *Store) MigrateManagementAccountTraffic(ctx context.Context, input port.ManagementAccountTrafficMigrationInput) (port.ManagementAccountTrafficMigrationResult, bool, error) {
	if strings.TrimSpace(input.SourceAccountID) == strings.TrimSpace(input.TargetAccountID) {
		return port.ManagementAccountTrafficMigrationResult{}, false, port.ErrManagementAccountTrafficMigrationSameAccount
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementAccountTrafficMigrationResult{}, false, fmt.Errorf("begin account traffic migration tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	accounts := make(map[string]migrationAccountRow, 2)
	ids := []string{strings.TrimSpace(input.SourceAccountID), strings.TrimSpace(input.TargetAccountID)}
	if ids[1] < ids[0] {
		ids[0], ids[1] = ids[1], ids[0]
	}
	for _, id := range ids {
		row, found, loadErr := lockTrafficMigrationAccount(ctx, tx, id, input)
		if loadErr != nil {
			return port.ManagementAccountTrafficMigrationResult{}, false, loadErr
		}
		if !found {
			return port.ManagementAccountTrafficMigrationResult{}, false, port.ErrManagementAccountTrafficMigrationNotFound
		}
		accounts[id] = row
	}
	source := accounts[strings.TrimSpace(input.SourceAccountID)]
	target := accounts[strings.TrimSpace(input.TargetAccountID)]
	if source.account.SystemAccountID != target.account.SystemAccountID {
		return port.ManagementAccountTrafficMigrationResult{}, false, port.ErrManagementAccountTrafficMigrationDifferentOwner
	}
	if source.account.ProviderCode != target.account.ProviderCode {
		return port.ManagementAccountTrafficMigrationResult{}, false, port.ErrManagementAccountTrafficMigrationDifferentProvider
	}
	if source.account.BoundGroupID == "" || source.account.BoundGroupID != target.account.BoundGroupID {
		return port.ManagementAccountTrafficMigrationResult{}, false, port.ErrManagementAccountTrafficMigrationDifferentGroup
	}
	if !target.effectiveAvailable {
		return port.ManagementAccountTrafficMigrationResult{}, false, port.ErrManagementAccountTrafficMigrationTargetUnavailable
	}

	result := port.ManagementAccountTrafficMigrationResult{SourceAccount: source.account, TargetAccount: target.account, GroupID: source.account.BoundGroupID}
	if input.SourceStatus != port.ManagementAccountTrafficMigrationUnchanged {
		status, schedulable := "temporary_unavailable", source.account.Schedulable
		var cooldownUntil, observationStartedAt *time.Time
		if source.account.AccessType == "authorized" {
			schedulable = true
		}
		if input.SourceStatus == port.ManagementAccountTrafficMigrationDisabled {
			status, schedulable = "disabled", false
		} else {
			observed := input.UpdatedAt.UTC()
			cooldown := observed.Add(3 * time.Second)
			observationStartedAt, cooldownUntil = &observed, &cooldown
			result.SourceCooldownUntil = cooldown
		}
		tag, execErr := tx.Exec(ctx, updateManagementAccountTrafficMigrationSourceSQL,
			status, schedulable, cooldownUntil, observationStartedAt, input.UpdatedAt.UTC(),
			source.account.ID, source.account.SystemAccountID, nullableTextParam(source.account.AccountAuthorizationID),
		)
		if execErr != nil {
			return port.ManagementAccountTrafficMigrationResult{}, false, fmt.Errorf("update account traffic migration source: %w", execErr)
		}
		if tag.RowsAffected() != 1 {
			return port.ManagementAccountTrafficMigrationResult{}, false, port.ErrManagementAccountTrafficMigrationStateChanged
		}
		if _, execErr = tx.Exec(ctx, markManagementAccountTrafficMigrationStatsDirtySQL, result.GroupID, input.UpdatedAt.UTC()); execErr != nil {
			return port.ManagementAccountTrafficMigrationResult{}, false, fmt.Errorf("mark account traffic migration stats dirty: %w", execErr)
		}
		result.SourceAccount.Status, result.SourceAccount.Schedulable = status, schedulable
		result.SourceAccount.CooldownUntil = result.SourceCooldownUntil
		result.SourceChanged = true
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ManagementAccountTrafficMigrationResult{}, false, fmt.Errorf("commit account traffic migration tx: %w", err)
	}
	committed = true
	return result, true, nil
}

type migrationAccountRow struct {
	account            port.ManagementAccountTrafficMigrationAccount
	effectiveAvailable bool
}

func lockTrafficMigrationAccount(ctx context.Context, tx pgx.Tx, accountID string, input port.ManagementAccountTrafficMigrationInput) (migrationAccountRow, bool, error) {
	var row migrationAccountRow
	var cooldown pgtype.Timestamptz
	var authorizationID pgtype.Text
	var bindingAuthorizationID pgtype.Text
	err := tx.QueryRow(ctx, lockManagementAccountTrafficMigrationAccountSQL,
		accountID, input.CanAccessAll, strings.TrimSpace(input.EffectiveSystemAccountID), input.UpdatedAt.UTC(),
	).Scan(
		&row.account.ID, &row.account.SystemAccountID, &row.account.OwnerSystemAccountID, &row.account.Name,
		&row.account.ProviderCode, &row.account.Type, &row.account.Status, &row.account.Schedulable,
		&cooldown, &row.account.BoundGroupID, &bindingAuthorizationID, &authorizationID, &row.account.AccessType, &row.effectiveAvailable,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return migrationAccountRow{}, false, nil
	}
	if err != nil {
		return migrationAccountRow{}, false, fmt.Errorf("lock account traffic migration account: %w", err)
	}
	if cooldown.Valid {
		row.account.CooldownUntil = cooldown.Time.UTC()
	}
	if authorizationID.Valid {
		row.account.AccountAuthorizationID = strings.TrimSpace(authorizationID.String)
	}
	return row, true, nil
}

func nullableTextParam(value string) any {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return nil
}

var _ port.ManagementAccountTrafficMigrator = (*Store)(nil)
