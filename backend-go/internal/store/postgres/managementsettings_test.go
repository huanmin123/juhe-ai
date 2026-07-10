package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementGlobalSettingsSQLLocksRowsInStableOrder(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_settings.sql")
	if err != nil {
		t.Fatalf("read management settings query: %v", err)
	}
	sql := string(source)
	lockSQL := querySection(t, sql, "-- name: LockManagementGlobalSettings :many", "-- name: UpdateManagementGlobalSetting :one")
	for _, want := range []string{
		"FROM juhe_business.global_settings",
		"WHERE key IN ('appName', 'appIcon')",
		"ORDER BY key ASC",
		"FOR UPDATE",
	} {
		if !strings.Contains(lockSQL, want) {
			t.Fatalf("management settings lock query missing %q", want)
		}
	}

	updateSQL := querySection(t, sql, "-- name: UpdateManagementGlobalSetting :one", "")
	for _, want := range []string{
		"UPDATE juhe_business.global_settings",
		"value_json = sqlc.arg(value_json)::text",
		"updated_at = sqlc.arg(updated_at)::timestamptz",
		"WHERE key = sqlc.arg(key)::text",
		"RETURNING key, value_json",
	} {
		if !strings.Contains(updateSQL, want) {
			t.Fatalf("management settings update query missing %q", want)
		}
	}
}

func TestUpdateManagementGlobalSettingsUpdatesOnlyProvidedField(t *testing.T) {
	updatedAt := time.Date(2026, 7, 10, 12, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	appName := "新的聚合 AI"
	q := &managementGlobalSettingsQueriesStub{
		lockedRows: validManagementGlobalSettingsRows(),
	}

	result, err := updateManagementGlobalSettings(context.Background(), q, port.ManagementGlobalSettingsUpdateInput{
		AppName:   &appName,
		UpdatedAt: updatedAt,
	})
	if err != nil {
		t.Fatalf("updateManagementGlobalSettings() error = %v", err)
	}
	if len(q.updateCalls) != 1 {
		t.Fatalf("update calls = %d, want 1", len(q.updateCalls))
	}
	call := q.updateCalls[0]
	if call.Key != "appName" {
		t.Fatalf("updated key = %q, want appName", call.Key)
	}
	if call.ValueJson != `"新的聚合 AI"` {
		t.Fatalf("updated value_json = %q", call.ValueJson)
	}
	if !call.UpdatedAt.Valid || !call.UpdatedAt.Time.Equal(updatedAt) {
		t.Fatalf("updated_at = %+v, want %s", call.UpdatedAt, updatedAt)
	}
	if result.Before.AppName != "聚合 AI" || result.Before.AppIcon != "/__aisys__/brand-icon.svg" {
		t.Fatalf("before = %+v", result.Before)
	}
	if result.Settings.AppName != appName || result.Settings.AppIcon != result.Before.AppIcon {
		t.Fatalf("settings = %+v", result.Settings)
	}
}

func TestUpdateManagementGlobalSettingsUsesStableKeyOrder(t *testing.T) {
	appName := "新的名称"
	appIcon := "/assets/new-icon.svg"
	q := &managementGlobalSettingsQueriesStub{
		lockedRows: validManagementGlobalSettingsRows(),
	}

	result, err := updateManagementGlobalSettings(context.Background(), q, port.ManagementGlobalSettingsUpdateInput{
		AppName: &appName,
		AppIcon: &appIcon,
	})
	if err != nil {
		t.Fatalf("updateManagementGlobalSettings() error = %v", err)
	}
	if len(q.updateCalls) != 2 {
		t.Fatalf("update calls = %d, want 2", len(q.updateCalls))
	}
	if q.updateCalls[0].Key != "appIcon" || q.updateCalls[1].Key != "appName" {
		t.Fatalf("update order = [%s, %s], want [appIcon, appName]", q.updateCalls[0].Key, q.updateCalls[1].Key)
	}
	if result.Settings.AppName != appName || result.Settings.AppIcon != appIcon {
		t.Fatalf("settings = %+v", result.Settings)
	}
}

func TestUpdateManagementGlobalSettingsRejectsInvalidCurrentRowsBeforeWriting(t *testing.T) {
	tests := []struct {
		name string
		rows []postgresqueries.LockManagementGlobalSettingsRow
	}{
		{
			name: "missing app icon",
			rows: []postgresqueries.LockManagementGlobalSettingsRow{
				{Key: "appName", ValueJson: `"聚合 AI"`},
			},
		},
		{
			name: "non string app icon",
			rows: []postgresqueries.LockManagementGlobalSettingsRow{
				{Key: "appIcon", ValueJson: `123`},
				{Key: "appName", ValueJson: `"聚合 AI"`},
			},
		},
		{
			name: "blank app name",
			rows: []postgresqueries.LockManagementGlobalSettingsRow{
				{Key: "appIcon", ValueJson: `"/__aisys__/brand-icon.svg"`},
				{Key: "appName", ValueJson: `" "`},
			},
		},
	}
	appName := "新的名称"
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &managementGlobalSettingsQueriesStub{lockedRows: tt.rows}
			if _, err := updateManagementGlobalSettings(context.Background(), q, port.ManagementGlobalSettingsUpdateInput{
				AppName: &appName,
			}); err == nil {
				t.Fatal("updateManagementGlobalSettings() error = nil, want error")
			}
			if len(q.updateCalls) != 0 {
				t.Fatalf("update calls = %d, want 0", len(q.updateCalls))
			}
		})
	}
}

func validManagementGlobalSettingsRows() []postgresqueries.LockManagementGlobalSettingsRow {
	return []postgresqueries.LockManagementGlobalSettingsRow{
		{Key: "appIcon", ValueJson: `"/__aisys__/brand-icon.svg"`},
		{Key: "appName", ValueJson: `"聚合 AI"`},
	}
}

type managementGlobalSettingsQueriesStub struct {
	lockedRows  []postgresqueries.LockManagementGlobalSettingsRow
	lockErr     error
	updateCalls []postgresqueries.UpdateManagementGlobalSettingParams
	updateErr   error
}

func (s *managementGlobalSettingsQueriesStub) LockManagementGlobalSettings(context.Context) ([]postgresqueries.LockManagementGlobalSettingsRow, error) {
	return s.lockedRows, s.lockErr
}

func (s *managementGlobalSettingsQueriesStub) UpdateManagementGlobalSetting(
	_ context.Context,
	arg postgresqueries.UpdateManagementGlobalSettingParams,
) (postgresqueries.UpdateManagementGlobalSettingRow, error) {
	s.updateCalls = append(s.updateCalls, arg)
	if s.updateErr != nil {
		return postgresqueries.UpdateManagementGlobalSettingRow{}, s.updateErr
	}
	return postgresqueries.UpdateManagementGlobalSettingRow{
		Key:       arg.Key,
		ValueJson: arg.ValueJson,
	}, nil
}
