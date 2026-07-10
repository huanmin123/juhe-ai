package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
	"juhe-ai/backend-go/internal/systemsettings"
)

type managementGlobalSettingsQueries interface {
	LockManagementGlobalSettings(ctx context.Context) ([]postgresqueries.LockManagementGlobalSettingsRow, error)
	UpdateManagementGlobalSetting(ctx context.Context, arg postgresqueries.UpdateManagementGlobalSettingParams) (postgresqueries.UpdateManagementGlobalSettingRow, error)
}

type managementSystemSettingsQueries interface {
	LockManagementSystemSettings(ctx context.Context) ([]postgresqueries.LockManagementSystemSettingsRow, error)
	UpdateManagementSystemSetting(ctx context.Context, arg postgresqueries.UpdateManagementSystemSettingParams) (postgresqueries.UpdateManagementSystemSettingRow, error)
}

type managementSystemSettingRow struct {
	key       string
	valueJSON string
}

func (s *Store) UpdateGlobalSettings(ctx context.Context, input port.ManagementGlobalSettingsUpdateInput) (port.ManagementGlobalSettingsUpdateResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementGlobalSettingsUpdateResult{}, fmt.Errorf("begin management global settings update tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	result, err := updateManagementGlobalSettings(ctx, s.queries().WithTx(tx), input)
	if err != nil {
		return port.ManagementGlobalSettingsUpdateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.ManagementGlobalSettingsUpdateResult{}, fmt.Errorf("commit management global settings update tx rolled back: %w", err)
		}
		return port.ManagementGlobalSettingsUpdateResult{}, fmt.Errorf("commit management global settings update tx: %w", err)
	}
	committed = true
	return result, nil
}

func updateManagementGlobalSettings(
	ctx context.Context,
	q managementGlobalSettingsQueries,
	input port.ManagementGlobalSettingsUpdateInput,
) (port.ManagementGlobalSettingsUpdateResult, error) {
	rows, err := q.LockManagementGlobalSettings(ctx)
	if err != nil {
		return port.ManagementGlobalSettingsUpdateResult{}, fmt.Errorf("lock management global settings: %w", err)
	}
	before, err := managementGlobalSettingsFromLockedRows(rows)
	if err != nil {
		return port.ManagementGlobalSettingsUpdateResult{}, err
	}
	settings := before

	if input.AppIcon != nil {
		settings.AppIcon, err = updateManagementGlobalSetting(ctx, q, "appIcon", *input.AppIcon, input.UpdatedAt)
		if err != nil {
			return port.ManagementGlobalSettingsUpdateResult{}, err
		}
	}
	if input.AppName != nil {
		settings.AppName, err = updateManagementGlobalSetting(ctx, q, "appName", *input.AppName, input.UpdatedAt)
		if err != nil {
			return port.ManagementGlobalSettingsUpdateResult{}, err
		}
	}

	return port.ManagementGlobalSettingsUpdateResult{
		Before:   before,
		Settings: settings,
	}, nil
}

func managementGlobalSettingsFromLockedRows(rows []postgresqueries.LockManagementGlobalSettingsRow) (port.PublicGlobalSettings, error) {
	values := make(map[string]string, 2)
	for _, row := range rows {
		if row.Key != "appIcon" && row.Key != "appName" {
			return port.PublicGlobalSettings{}, fmt.Errorf("lock management global settings returned unexpected key %q", row.Key)
		}
		if _, exists := values[row.Key]; exists {
			return port.PublicGlobalSettings{}, fmt.Errorf("lock management global settings returned duplicate key %q", row.Key)
		}
		value, err := parsePublicSettingValue(row.ValueJson, row.Key)
		if err != nil {
			return port.PublicGlobalSettings{}, fmt.Errorf("parse locked management global setting %s: %w", row.Key, err)
		}
		values[row.Key] = value
	}

	appName, ok := values["appName"]
	if !ok {
		return port.PublicGlobalSettings{}, fmt.Errorf("locked management global settings missing key appName")
	}
	appIcon, ok := values["appIcon"]
	if !ok {
		return port.PublicGlobalSettings{}, fmt.Errorf("locked management global settings missing key appIcon")
	}
	return port.PublicGlobalSettings{
		AppName: appName,
		AppIcon: appIcon,
	}, nil
}

func updateManagementGlobalSetting(
	ctx context.Context,
	q managementGlobalSettingsQueries,
	key string,
	value string,
	updatedAt time.Time,
) (string, error) {
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal management global setting %s: %w", key, err)
	}
	if _, err := parsePublicSettingValue(string(valueJSON), key); err != nil {
		return "", fmt.Errorf("validate management global setting %s: %w", key, err)
	}

	row, err := q.UpdateManagementGlobalSetting(ctx, postgresqueries.UpdateManagementGlobalSettingParams{
		ValueJson: string(valueJSON),
		UpdatedAt: pgTimestamptz(updatedAt),
		Key:       key,
	})
	if err != nil {
		return "", fmt.Errorf("update management global setting %s: %w", key, err)
	}
	if row.Key != key {
		return "", fmt.Errorf("update management global setting %s returned key %q", key, row.Key)
	}
	updatedValue, err := parsePublicSettingValue(row.ValueJson, row.Key)
	if err != nil {
		return "", fmt.Errorf("parse updated management global setting %s: %w", key, err)
	}
	return updatedValue, nil
}

func (s *Store) ManagementSystemSettings(ctx context.Context) (systemsettings.Snapshot, error) {
	rows, err := s.queries().ListManagementSystemSettings(ctx)
	if err != nil {
		return systemsettings.Snapshot{}, fmt.Errorf("list management system settings: %w", err)
	}
	values := make([]managementSystemSettingRow, 0, len(rows))
	for _, row := range rows {
		values = append(values, managementSystemSettingRow{
			key:       row.Key,
			valueJSON: row.ValueJson,
		})
	}
	return managementSystemSettingsSnapshot(values, "validate management system settings")
}

func (s *Store) UpdateManagementSystemSettings(
	ctx context.Context,
	input port.ManagementSystemSettingsUpdateInput,
) (port.ManagementSystemSettingsUpdateResult, error) {
	return updateManagementSystemSettingsInTx(
		ctx,
		s.pool.BeginTx,
		func(tx pgx.Tx) managementSystemSettingsQueries {
			return s.queries().WithTx(tx)
		},
		input,
	)
}

func updateManagementSystemSettingsInTx(
	ctx context.Context,
	beginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error),
	queriesForTx func(pgx.Tx) managementSystemSettingsQueries,
	input port.ManagementSystemSettingsUpdateInput,
) (port.ManagementSystemSettingsUpdateResult, error) {
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return port.ManagementSystemSettingsUpdateResult{}, fmt.Errorf("begin management system settings update tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()

	result, err := updateManagementSystemSettings(ctx, queriesForTx(tx), input)
	if err != nil {
		return port.ManagementSystemSettingsUpdateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return port.ManagementSystemSettingsUpdateResult{}, fmt.Errorf("commit management system settings update tx rolled back: %w", err)
		}
		return port.ManagementSystemSettingsUpdateResult{}, fmt.Errorf("commit management system settings update tx: %w", err)
	}
	committed = true
	return result, nil
}

func updateManagementSystemSettings(
	ctx context.Context,
	q managementSystemSettingsQueries,
	input port.ManagementSystemSettingsUpdateInput,
) (port.ManagementSystemSettingsUpdateResult, error) {
	rows, err := q.LockManagementSystemSettings(ctx)
	if err != nil {
		return port.ManagementSystemSettingsUpdateResult{}, fmt.Errorf("lock management system settings: %w", err)
	}
	values := make([]managementSystemSettingRow, 0, len(rows))
	for _, row := range rows {
		values = append(values, managementSystemSettingRow{
			key:       row.Key,
			valueJSON: row.ValueJson,
		})
	}
	before, err := managementSystemSettingsSnapshot(values, "validate locked management system settings")
	if err != nil {
		return port.ManagementSystemSettingsUpdateResult{}, err
	}
	settings, err := before.Apply(input.Patch)
	if err != nil {
		return port.ManagementSystemSettingsUpdateResult{}, fmt.Errorf("apply management system settings patch: %w", err)
	}

	for _, entry := range input.Patch.Entries() {
		row, err := q.UpdateManagementSystemSetting(ctx, postgresqueries.UpdateManagementSystemSettingParams{
			ValueJson: string(entry.Value),
			UpdatedAt: pgTimestamptz(input.UpdatedAt),
			Key:       entry.Key,
		})
		if err != nil {
			return port.ManagementSystemSettingsUpdateResult{}, fmt.Errorf("update management system setting %s: %w", entry.Key, err)
		}
		if row.Key != entry.Key {
			return port.ManagementSystemSettingsUpdateResult{}, fmt.Errorf(
				"update management system setting %s returned key %q",
				entry.Key,
				row.Key,
			)
		}
		updated, err := systemsettings.NewPatch(map[string]json.RawMessage{
			row.Key: json.RawMessage(row.ValueJson),
		})
		if err != nil {
			return port.ManagementSystemSettingsUpdateResult{}, fmt.Errorf(
				"validate updated management system setting %s: %w",
				entry.Key,
				err,
			)
		}
		updatedValue, ok := updated.Value(entry.Key)
		if !ok || !bytes.Equal(updatedValue, entry.Value) {
			return port.ManagementSystemSettingsUpdateResult{}, fmt.Errorf(
				"update management system setting %s returned unexpected value %q",
				entry.Key,
				row.ValueJson,
			)
		}
	}

	return port.ManagementSystemSettingsUpdateResult{
		Before:   before,
		Settings: settings,
	}, nil
}

func managementSystemSettingsSnapshot(
	rows []managementSystemSettingRow,
	operation string,
) (systemsettings.Snapshot, error) {
	entries := make([]systemsettings.Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, systemsettings.Entry{
			Key:   row.key,
			Value: json.RawMessage(row.valueJSON),
		})
	}
	settings, err := systemsettings.NewSnapshotFromEntries(entries)
	if err != nil {
		return systemsettings.Snapshot{}, fmt.Errorf("%s: %w", operation, err)
	}
	return settings, nil
}

var _ port.ManagementGlobalSettingsWriter = (*Store)(nil)
var _ port.ManagementSystemSettingsReader = (*Store)(nil)
var _ port.ManagementSystemSettingsWriter = (*Store)(nil)
