package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
	"juhe-ai/backend-go/internal/systemsettings"
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

	updateSQL := querySection(t, sql, "-- name: UpdateManagementGlobalSetting :one", "-- name: ListManagementSystemSettings :many")
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

func TestW5SystemSettingsMigrationSeedsNodeDefaults(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000024_w5_system_settings.sql")
	if err != nil {
		t.Fatalf("read W5 system settings migration: %v", err)
	}
	sql := string(source)
	defaults := nodeSystemSettingDefaultJSON()
	if len(defaults) != 55 {
		t.Fatalf("initial migration default count = %d, want 55", len(defaults))
	}
	if count := strings.Count(sql, "'sys_admin'"); count != len(defaults) {
		t.Fatalf("migration sys_admin row count = %d, want %d", count, len(defaults))
	}
	for key, valueJSON := range defaults {
		if key == systemsettings.UsageStatsTimezoneKey {
			continue
		}
		want := fmt.Sprintf("('sys_admin', '%s', '%s', now())", key, valueJSON)
		if !strings.Contains(sql, want) {
			t.Fatalf("W5 system settings migration missing %q", want)
		}
	}
	for _, want := range []string{
		"'usageStatsTimezone'",
		"-- +goose ENVSUB ON",
		"NULLIF('${JUHE_AI_USAGE_STATS_TIMEZONE:-}', '')",
		"NULLIF(current_setting('TimeZone', true), '')",
		"-- +goose ENVSUB OFF",
		"ON CONFLICT (system_account_id, key) DO NOTHING",
		"-- +goose Down",
		"-- no-op:",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("W5 system settings migration missing %q", want)
		}
	}
	for _, key := range []string{"gptPriorityPriceMultiplier", "gptFlexPriceMultiplier"} {
		if strings.Contains(sql, "'"+key+"'") {
			t.Fatalf("executed migration 000024 must not be modified with %s", key)
		}
	}
}

func TestW5RemoveGPTServiceTierMultipliersMigration(t *testing.T) {
	source, err := os.ReadFile("../../../db/migrations/000043_w5_remove_gpt_service_tier_multipliers.sql")
	if err != nil {
		t.Fatalf("read remove GPT service tier multiplier settings migration: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"DELETE FROM juhe_business.system_settings",
		"WHERE key IN ('gptPriorityPriceMultiplier', 'gptFlexPriceMultiplier')",
		"('sys_admin', 'gptPriorityPriceMultiplier', '2', now())",
		"('sys_admin', 'gptFlexPriceMultiplier', '0.5', now())",
		"ON CONFLICT (system_account_id, key) DO NOTHING",
		"-- +goose Down",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("remove GPT service tier multiplier settings migration missing %q", want)
		}
	}
}

func TestManagementSystemSettingsSQLUsesFixedKeysAndStableOrder(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_settings.sql")
	if err != nil {
		t.Fatalf("read management settings query: %v", err)
	}
	sql := string(source)
	listSQL := querySection(
		t,
		sql,
		"-- name: ListManagementSystemSettings :many",
		"-- name: LockManagementSystemSettings :many",
	)
	lockSQL := querySection(
		t,
		sql,
		"-- name: LockManagementSystemSettings :many",
		"-- name: UpdateManagementSystemSetting :one",
	)
	updateSQL := querySection(t, sql, "-- name: UpdateManagementSystemSetting :one", "")

	for name, query := range map[string]string{
		"list": listSQL,
		"lock": lockSQL,
	} {
		for _, want := range []string{
			"FROM juhe_business.system_settings",
			"WHERE system_account_id = 'sys_admin'",
			"ORDER BY key ASC",
		} {
			if !strings.Contains(query, want) {
				t.Fatalf("%s management system settings query missing %q", name, want)
			}
		}
		for _, key := range systemsettings.Keys() {
			if count := strings.Count(query, "'"+key+"'"); count != 1 {
				t.Fatalf("%s management system settings query key %s count = %d, want 1", name, key, count)
			}
		}
	}
	if strings.Contains(listSQL, "FOR UPDATE") {
		t.Fatal("management system settings read query must not lock rows")
	}
	if !strings.Contains(lockSQL, "FOR UPDATE") {
		t.Fatal("management system settings update must lock all fixed rows")
	}
	for _, want := range []string{
		"UPDATE juhe_business.system_settings",
		"value_json = sqlc.arg(value_json)::text",
		"updated_at = sqlc.arg(updated_at)::timestamptz",
		"WHERE system_account_id = 'sys_admin'",
		"AND key = sqlc.arg(key)::text",
		"RETURNING key, value_json",
	} {
		if !strings.Contains(updateSQL, want) {
			t.Fatalf("management system settings update query missing %q", want)
		}
	}
}

func TestManagementSystemSettingsSnapshotReadsAllSettings(t *testing.T) {
	settings, err := managementSystemSettingsSnapshot(validManagementSystemSettingValues(), "test read")
	if err != nil {
		t.Fatalf("managementSystemSettingsSnapshot() error = %v", err)
	}
	if settings.Len() != len(systemsettings.Definitions()) {
		t.Fatalf("settings length = %d, want %d", settings.Len(), len(systemsettings.Definitions()))
	}
	value, ok := settings.Value("usageHotWindowRefreshIntervalSeconds")
	if !ok || string(value) != "600" {
		t.Fatalf("usageHotWindowRefreshIntervalSeconds = %q, %v; want 600", value, ok)
	}
	timezone, ok := settings.Value(systemsettings.UsageStatsTimezoneKey)
	if !ok || string(timezone) != `"UTC"` {
		t.Fatalf("usageStatsTimezone = %q, %v; want UTC", timezone, ok)
	}
}

func TestManagementSystemSettingsSnapshotRejectsIncompleteOrInvalidRows(t *testing.T) {
	tests := []struct {
		name string
		rows func() []managementSystemSettingRow
		want string
	}{
		{
			name: "missing",
			rows: func() []managementSystemSettingRow {
				rows := validManagementSystemSettingValues()
				return rows[1:]
			},
			want: "系统设置缺少字段",
		},
		{
			name: "duplicate",
			rows: func() []managementSystemSettingRow {
				rows := validManagementSystemSettingValues()
				return append(rows, rows[0])
			},
			want: "系统设置字段重复",
		},
		{
			name: "unknown",
			rows: func() []managementSystemSettingRow {
				rows := validManagementSystemSettingValues()
				return append(rows, managementSystemSettingRow{key: "unknownSetting", valueJSON: "1"})
			},
			want: "未知系统设置字段：unknownSetting",
		},
		{
			name: "invalid json",
			rows: func() []managementSystemSettingRow {
				rows := validManagementSystemSettingValues()
				rows[0].valueJSON = "{"
				return rows
			},
			want: "必须是有效 JSON",
		},
		{
			name: "out of range",
			rows: func() []managementSystemSettingRow {
				rows := validManagementSystemSettingValues()
				for index := range rows {
					if rows[index].key == "usageHotWindowRefreshIntervalSeconds" {
						rows[index].valueJSON = "59"
						break
					}
				}
				return rows
			},
			want: "usageHotWindowRefreshIntervalSeconds 必须在 60 到 3600 之间",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := managementSystemSettingsSnapshot(tt.rows(), "test read")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("managementSystemSettingsSnapshot() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestUpdateManagementSystemSettingsUsesStablePatchOrder(t *testing.T) {
	updatedAt := time.Date(2026, 7, 10, 13, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	patch := mustSystemSettingsPatch(t, map[string]json.RawMessage{
		"usageHotWindowRefreshIntervalSeconds": json.RawMessage(`900`),
		"gatewayTextRawBodyLimitMegabytes":     json.RawMessage(`32`),
		"accountHealthCheckBatchSize":          json.RawMessage(`40`),
		"systemMetricsHourlyRetentionDays":     json.RawMessage(`20`),
	})
	q := &managementSystemSettingsQueriesStub{
		lockedRows: validManagementSystemSettingsRows(),
	}

	result, err := updateManagementSystemSettings(context.Background(), q, port.ManagementSystemSettingsUpdateInput{
		Patch:     patch,
		UpdatedAt: updatedAt,
	})
	if err != nil {
		t.Fatalf("updateManagementSystemSettings() error = %v", err)
	}
	wantKeys := []string{
		"accountHealthCheckBatchSize",
		"gatewayTextRawBodyLimitMegabytes",
		"systemMetricsHourlyRetentionDays",
		"usageHotWindowRefreshIntervalSeconds",
	}
	if len(q.updateCalls) != len(wantKeys) {
		t.Fatalf("update calls = %d, want %d", len(q.updateCalls), len(wantKeys))
	}
	for index, wantKey := range wantKeys {
		call := q.updateCalls[index]
		if call.Key != wantKey {
			t.Fatalf("update call %d key = %q, want %q", index, call.Key, wantKey)
		}
		if !call.UpdatedAt.Valid || !call.UpdatedAt.Time.Equal(updatedAt) {
			t.Fatalf("update call %d updated_at = %+v, want %s", index, call.UpdatedAt, updatedAt)
		}
	}
	beforeValue, _ := result.Before.Value("usageHotWindowRefreshIntervalSeconds")
	updatedValue, _ := result.Settings.Value("usageHotWindowRefreshIntervalSeconds")
	if string(beforeValue) != "600" || string(updatedValue) != "900" {
		t.Fatalf("usage hot window before/after = %s/%s, want 600/900", beforeValue, updatedValue)
	}
	hourlyRetention, _ := result.Settings.Value("systemMetricsHourlyRetentionDays")
	if string(hourlyRetention) != "20" {
		t.Fatalf("system metrics hourly retention = %s, want 20", hourlyRetention)
	}
	if result.Settings.Len() != len(systemsettings.Definitions()) {
		t.Fatalf("updated settings length = %d, want %d", result.Settings.Len(), len(systemsettings.Definitions()))
	}
}

func TestUpdateManagementSystemSettingsRejectsInvalidLockedSnapshotBeforeWriting(t *testing.T) {
	rows := validManagementSystemSettingsRows()
	rows = rows[1:]
	q := &managementSystemSettingsQueriesStub{lockedRows: rows}
	patch := mustSystemSettingsPatch(t, map[string]json.RawMessage{
		"usageHotWindowRefreshIntervalSeconds": json.RawMessage(`900`),
	})

	if _, err := updateManagementSystemSettings(context.Background(), q, port.ManagementSystemSettingsUpdateInput{
		Patch: patch,
	}); err == nil {
		t.Fatal("updateManagementSystemSettings() error = nil, want error")
	}
	if len(q.updateCalls) != 0 {
		t.Fatalf("update calls = %d, want 0", len(q.updateCalls))
	}
}

func TestUpdateManagementSystemSettingsTransactionCommits(t *testing.T) {
	tx := &managementSystemSettingsTxStub{}
	q := &managementSystemSettingsQueriesStub{lockedRows: validManagementSystemSettingsRows()}
	patch := mustSystemSettingsPatch(t, map[string]json.RawMessage{
		"usageHotWindowRefreshIntervalSeconds": json.RawMessage(`900`),
	})

	_, err := updateManagementSystemSettingsInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(got pgx.Tx) managementSystemSettingsQueries {
			if got != tx {
				t.Fatalf("queries tx = %T, want transaction stub", got)
			}
			return q
		},
		port.ManagementSystemSettingsUpdateInput{Patch: patch},
	)
	if err != nil {
		t.Fatalf("updateManagementSystemSettingsInTx() error = %v", err)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("commit/rollback calls = %d/%d, want 1/0", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestUpdateManagementSystemSettingsTransactionRollsBackOnUpdateFailure(t *testing.T) {
	updateErr := errors.New("update failed")
	tx := &managementSystemSettingsTxStub{}
	q := &managementSystemSettingsQueriesStub{
		lockedRows:     validManagementSystemSettingsRows(),
		updateFailCall: 2,
		updateErr:      updateErr,
	}
	patch := mustSystemSettingsPatch(t, map[string]json.RawMessage{
		"usageHotWindowRefreshIntervalSeconds": json.RawMessage(`900`),
		"gatewayTextRawBodyLimitMegabytes":     json.RawMessage(`32`),
	})

	_, err := updateManagementSystemSettingsInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(pgx.Tx) managementSystemSettingsQueries {
			return q
		},
		port.ManagementSystemSettingsUpdateInput{Patch: patch},
	)
	if !errors.Is(err, updateErr) {
		t.Fatalf("updateManagementSystemSettingsInTx() error = %v, want %v", err, updateErr)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback calls = %d/%d, want 0/1", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestUpdateManagementSystemSettingsTransactionRollsBackOnCommitFailure(t *testing.T) {
	commitErr := errors.New("commit failed")
	tx := &managementSystemSettingsTxStub{commitErr: commitErr}
	q := &managementSystemSettingsQueriesStub{lockedRows: validManagementSystemSettingsRows()}
	patch := mustSystemSettingsPatch(t, map[string]json.RawMessage{
		"usageHotWindowRefreshIntervalSeconds": json.RawMessage(`900`),
	})

	_, err := updateManagementSystemSettingsInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(pgx.Tx) managementSystemSettingsQueries {
			return q
		},
		port.ManagementSystemSettingsUpdateInput{Patch: patch},
	)
	if !errors.Is(err, commitErr) {
		t.Fatalf("updateManagementSystemSettingsInTx() error = %v, want %v", err, commitErr)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback calls = %d/%d, want 1/1", tx.commitCalls, tx.rollbackCalls)
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

func validManagementSystemSettingValues() []managementSystemSettingRow {
	defaults := nodeSystemSettingDefaultJSON()
	rows := make([]managementSystemSettingRow, 0, len(defaults))
	for _, key := range systemsettings.SortedKeys() {
		rows = append(rows, managementSystemSettingRow{
			key:       key,
			valueJSON: defaults[key],
		})
	}
	return rows
}

func validManagementSystemSettingsRows() []postgresqueries.LockManagementSystemSettingsRow {
	values := validManagementSystemSettingValues()
	rows := make([]postgresqueries.LockManagementSystemSettingsRow, 0, len(values))
	for _, value := range values {
		rows = append(rows, postgresqueries.LockManagementSystemSettingsRow{
			Key:       value.key,
			ValueJson: value.valueJSON,
		})
	}
	return rows
}

func mustSystemSettingsPatch(t *testing.T, values map[string]json.RawMessage) systemsettings.Patch {
	t.Helper()
	patch, err := systemsettings.NewPatch(values)
	if err != nil {
		t.Fatalf("systemsettings.NewPatch() error = %v", err)
	}
	return patch
}

func nodeSystemSettingDefaultJSON() map[string]string {
	return map[string]string{
		"gatewayTextRawBodyLimitMegabytes":           "16",
		"systemApiRateLimitIpReadPerMinute":          "600",
		"systemApiRateLimitIpReadBurstPer10Seconds":  "120",
		"systemApiRateLimitIpWritePerMinute":         "180",
		"systemApiRateLimitIpWriteBurstPer10Seconds": "40",
		"systemApiRateLimitUserReadPerMinute":        "300",
		"systemApiRateLimitUserWritePerMinute":       "120",
		"defaultTemporaryUnschedulableMinutes":       "2",
		"temporaryUnschedulableRetryIntervalSeconds": "3",
		"temporaryUnschedulableRetryAttempts":        "3",
		"textFirstResponseTimeoutSeconds":           "120",
		"textStreamIdleTimeoutSeconds":              "30",
		"textUncommittedAttemptMaxLifetimeSeconds":  "1800",
		"imageFirstResponseTimeoutSeconds":          "600",
		"imageStreamIdleTimeoutSeconds":             "120",
		"imageUncommittedAttemptMaxLifetimeSeconds": "3600",
		"noAvailableAccountWaitTimeoutSeconds":      "270",
		"streamFailureThresholdCount":                "3",
		"streamFailureThresholdWindowMinutes":        "5",
		"operationLogRetentionDays":                  "365",
		"operationLogMaxChangesPerRecord":            "100",
		"statsAggregationIntervalSeconds":            "60",
		"statsAggregationBatchSize":                  "2000",
		"statsAggregationMaxBatchesPerRun":           "5",
		"usageHotWindowRefreshIntervalSeconds":       "600",
		"groupAccountStatsRefreshIntervalSeconds":    "60",
		"systemMetricsSampleIntervalSeconds":         "30",
		"tableMonitorMaxTablesPerRun":                "4",
		"accountQualityRefreshIntervalSeconds":       "600",
		"accountQualityWindowMinutes":                "10",
		"accountTestTaskConcurrency":                 "100",
		"accountHealthCheckIntervalHours":            "12",
		"accountHealthCheckJitterMinutes":            "120",
		"accountHealthCheckBatchSize":                "20",
		"accountHealthCheckFailureThreshold":         "3",
		"cooldownAccountRetestIntervalSeconds":       "3",
		"cooldownAccountRetestBatchSize":             "10",
		"cooldownAccountRetestMaxBackoffHours":       "12",
		"oauthAccessTokenRefreshIntervalSeconds":     "60",
		"oauthAccessTokenRefreshLeadSeconds":         "300",
		"oauthAccessTokenRefreshBatchSize":           "20",
		"oauthAccessTokenRefreshRetryBackoffSeconds": "300",
		"modelCheckRetentionDays":                    "30",
		"runtimeLogIndexRetentionDays":               "14",
		"publicApiLogRetentionDays":                  "30",
		"usageRecordRetentionDays":                   "30",
		systemsettings.UsageStatsTimezoneKey:         `"UTC"`,
		"usageStatsMinuteRetentionHours":             "48",
		"usageStatsHourlyRetentionDays":              "60",
		"usageStatsDailyRetentionDays":               "400",
		"usageStatsWeeklyRetentionWeeks":             "104",
		"usageStatsMonthlyRetentionMonths":           "24",
		"usageRankSnapshotRetentionDays":             "30",
		"systemMetricsRetentionDays":                 "7",
		"systemMetricsHourlyRetentionDays":           "30",
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

type managementSystemSettingsQueriesStub struct {
	lockedRows     []postgresqueries.LockManagementSystemSettingsRow
	lockErr        error
	updateCalls    []postgresqueries.UpdateManagementSystemSettingParams
	updateFailCall int
	updateErr      error
}

func (s *managementSystemSettingsQueriesStub) LockManagementSystemSettings(
	context.Context,
) ([]postgresqueries.LockManagementSystemSettingsRow, error) {
	return s.lockedRows, s.lockErr
}

func (s *managementSystemSettingsQueriesStub) UpdateManagementSystemSetting(
	_ context.Context,
	arg postgresqueries.UpdateManagementSystemSettingParams,
) (postgresqueries.UpdateManagementSystemSettingRow, error) {
	s.updateCalls = append(s.updateCalls, arg)
	if s.updateErr != nil && len(s.updateCalls) == s.updateFailCall {
		return postgresqueries.UpdateManagementSystemSettingRow{}, s.updateErr
	}
	return postgresqueries.UpdateManagementSystemSettingRow{
		Key:       arg.Key,
		ValueJson: arg.ValueJson,
	}, nil
}

type managementSystemSettingsTxStub struct {
	pgx.Tx
	commitErr     error
	rollbackErr   error
	commitCalls   int
	rollbackCalls int
}

func (s *managementSystemSettingsTxStub) Commit(context.Context) error {
	s.commitCalls++
	return s.commitErr
}

func (s *managementSystemSettingsTxStub) Rollback(context.Context) error {
	s.rollbackCalls++
	return s.rollbackErr
}

var _ pgx.Tx = (*managementSystemSettingsTxStub)(nil)
