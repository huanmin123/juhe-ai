package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

type managementGlobalSettingsQueries interface {
	LockManagementGlobalSettings(ctx context.Context) ([]postgresqueries.LockManagementGlobalSettingsRow, error)
	UpdateManagementGlobalSetting(ctx context.Context, arg postgresqueries.UpdateManagementGlobalSettingParams) (postgresqueries.UpdateManagementGlobalSettingRow, error)
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

var _ port.ManagementGlobalSettingsWriter = (*Store)(nil)
