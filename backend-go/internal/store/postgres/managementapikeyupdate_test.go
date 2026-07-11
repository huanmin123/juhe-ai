package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementAPIKeyUpdatePortCarriesSparseMutationWithoutSecrets(t *testing.T) {
	input := port.ManagementAPIKeyUpdateInput{
		APIKeyID:                "key_1",
		OwnerSystemAccountID:    "sys_owner",
		HasName:                 true,
		Name:                    "生产 Key",
		HasDescription:          true,
		Description:             ptrManagementAPIKeyUpdateText("说明"),
		HasRouteStrategyID:      true,
		RouteStrategyID:         "route_2",
		HasStatus:               true,
		Status:                  "disabled",
		HasExpiresAt:            true,
		ExpiresAt:               ptrManagementAPIKeyUpdateTime(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)),
		HasQuotaLimits:          true,
		QuotaLimitsJSON:         ptrManagementAPIKeyUpdateText(`{"daily":{"enabled":true,"limit":1}}`),
		HourlyQuotaHours:        nil,
		HasAvailabilitySchedule: true,
		AvailabilityScheduleJSON: ptrManagementAPIKeyUpdateText(
			`{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[]}`,
		),
		AvailabilityScheduleNextCheckAt: ptrManagementAPIKeyUpdateTime(
			time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC),
		),
		UpdatedAt: time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC),
	}
	if input.APIKeyID == "" || !input.HasName || !input.HasAvailabilitySchedule {
		t.Fatalf("update input = %+v", input)
	}

	for _, typ := range []reflect.Type{
		reflect.TypeOf(port.ManagementAPIKeyUpdateInput{}),
		reflect.TypeOf(port.ManagementAPIKeyListRow{}),
		reflect.TypeOf(port.ManagementAPIKeyUpdateResult{}),
	} {
		for index := 0; index < typ.NumField(); index++ {
			name := strings.ToLower(typ.Field(index).Name)
			for _, forbidden := range []string{"secret", "hash", "cipher"} {
				if strings.Contains(name, forbidden) {
					t.Fatalf("%s leaks forbidden field %q", typ.Name(), typ.Field(index).Name)
				}
			}
		}
	}
}

func TestW5ManagementAPIKeyUpdateSQLLocksAndMutatesAtomically(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_api_key_update.sql")
	if err != nil {
		t.Fatalf("read W5 management API Key update SQL: %v", err)
	}
	sql := string(source)
	for _, required := range []string{
		"-- name: UpdateManagementAPIKey :one",
		"current_target AS MATERIALIZED",
		"FOR UPDATE OF api_keys",
		"changed_route_target AS MATERIALIZED",
		"FOR UPDATE OF route_strategies",
		"updated_api_key AS",
		"UPDATE juhe_business.api_keys",
		"CASE WHEN sqlc.arg(has_name)::boolean",
		"CASE WHEN sqlc.arg(has_description)::boolean",
		"CASE WHEN sqlc.arg(has_route_strategy_id)::boolean",
		"CASE WHEN sqlc.arg(has_status)::boolean",
		"CASE WHEN sqlc.arg(has_expires_at)::boolean",
		"CASE WHEN sqlc.arg(has_quota_limits)::boolean",
		"CASE WHEN sqlc.arg(has_availability_schedule)::boolean",
		"request_quota_hourly_window_configs",
		"sqlc.arg(has_quota_limits)::boolean",
		"ON CONFLICT (window_hours) DO UPDATE",
		"before_api_key_id",
		"after_api_key_id",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("W5 management API Key update SQL missing %q", required)
		}
	}
	keyLockIndex := strings.Index(sql, "FOR UPDATE OF api_keys")
	routeLockIndex := strings.Index(sql, "FOR UPDATE OF route_strategies")
	updateIndex := strings.Index(sql, "UPDATE juhe_business.api_keys")
	hourlyIndex := strings.Index(sql, "request_quota_hourly_window_configs")
	if keyLockIndex < 0 ||
		routeLockIndex <= keyLockIndex ||
		updateIndex <= routeLockIndex ||
		hourlyIndex <= updateIndex {
		t.Fatalf("update SQL lock/write order is invalid")
	}
	for _, forbidden := range []string{
		"usage_records",
		"audit_logs",
		"raw_audit",
		"SUM(",
		"GROUP BY",
		"key_secret_encrypted",
		"key_hash",
	} {
		if strings.Contains(strings.ToUpper(sql), strings.ToUpper(forbidden)) {
			t.Fatalf("W5 management API Key update SQL must not contain %q", forbidden)
		}
	}
}

func TestUpdateManagementAPIKeyMapsSparseInputAndBeforeAfterRows(t *testing.T) {
	now := time.Date(2026, 7, 11, 1, 2, 3, 0, time.UTC)
	expiresAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	nextCheckAt := time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC)
	description := "新说明"
	quotaJSON := `{"hourly":{"enabled":true,"hours":6,"limit":1}}`
	scheduleJSON := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[]}`
	hours := 6
	q := &managementAPIKeyUpdateQueriesStub{
		row: managementAPIKeyUpdateQueryRow(
			"route_before",
			"active",
			"route_after",
			"disabled",
		),
	}
	q.row.AfterDescription = pgtype.Text{String: description, Valid: true}
	q.row.AfterExpiresAt = pgtype.Timestamptz{Time: expiresAt, Valid: true}
	q.row.AfterQuotaLimitsJson = pgtype.Text{String: quotaJSON, Valid: true}
	q.row.AfterAvailabilityScheduleJson = pgtype.Text{String: scheduleJSON, Valid: true}
	q.row.RouteChanged = true
	q.row.HourlyUpsertCount = 1

	result, err := updateManagementAPIKey(context.Background(), q, port.ManagementAPIKeyUpdateInput{
		APIKeyID:                        " key_1 ",
		OwnerSystemAccountID:            " sys_owner ",
		HasName:                         true,
		Name:                            "新名称",
		HasDescription:                  true,
		Description:                     &description,
		HasRouteStrategyID:              true,
		RouteStrategyID:                 " route_after ",
		HasStatus:                       true,
		Status:                          "disabled",
		HasExpiresAt:                    true,
		ExpiresAt:                       &expiresAt,
		HasQuotaLimits:                  true,
		QuotaLimitsJSON:                 &quotaJSON,
		HourlyQuotaHours:                &hours,
		HasAvailabilitySchedule:         true,
		AvailabilityScheduleJSON:        &scheduleJSON,
		AvailabilityScheduleNextCheckAt: &nextCheckAt,
		UpdatedAt:                       now,
	})
	if err != nil {
		t.Fatalf("updateManagementAPIKey() error = %v", err)
	}
	if q.calls != 1 {
		t.Fatalf("query calls = %d, want 1", q.calls)
	}
	if q.input.ApiKeyID != "key_1" ||
		q.input.OwnerSystemAccountID != "sys_owner" ||
		!q.input.HasName ||
		q.input.Name != "新名称" ||
		!q.input.HasDescription ||
		q.input.Description.String != description ||
		!q.input.HasRouteStrategyID ||
		q.input.RouteStrategyID != "route_after" ||
		!q.input.HasStatus ||
		q.input.Status != "disabled" ||
		!q.input.HasExpiresAt ||
		!q.input.ExpiresAt.Time.Equal(expiresAt) ||
		!q.input.HasQuotaLimits ||
		q.input.QuotaLimitsJson.String != quotaJSON ||
		q.input.HourlyHours.Int32 != 6 ||
		!q.input.HasAvailabilitySchedule ||
		q.input.AvailabilityScheduleJson.String != scheduleJSON ||
		!q.input.AvailabilityScheduleNextCheckAt.Time.Equal(nextCheckAt) ||
		!q.input.UpdatedAt.Time.Equal(now) {
		t.Fatalf("query input = %+v", q.input)
	}
	if result.Before.RouteStrategyID != "route_before" ||
		result.Before.RouteStrategyStatus != "active" ||
		result.After.RouteStrategyID != "route_after" ||
		result.After.RouteStrategyStatus != "disabled" ||
		result.After.Description == nil ||
		*result.After.Description != description ||
		result.After.ExpiresAt == nil ||
		!result.After.ExpiresAt.Equal(expiresAt) ||
		result.After.QuotaLimitsJSON == nil ||
		*result.After.QuotaLimitsJSON != quotaJSON ||
		result.After.AvailabilityScheduleJSON == nil ||
		*result.After.AvailabilityScheduleJSON != scheduleJSON {
		t.Fatalf("result = %+v", result)
	}
}

func TestUpdateManagementAPIKeyMapsAtomicDecisionErrors(t *testing.T) {
	tests := []struct {
		name     string
		row      postgresqueries.UpdateManagementAPIKeyRow
		queryErr error
		wantErr  error
	}{
		{
			name:    "missing or wrong owner",
			row:     postgresqueries.UpdateManagementAPIKeyRow{},
			wantErr: port.ErrManagementAPIKeyNotFound,
		},
		{
			name: "default actual route change",
			row: func() postgresqueries.UpdateManagementAPIKeyRow {
				row := managementAPIKeyUpdateQueryRow("route_1", "active", "", "")
				row.RouteChanged = true
				row.DefaultRouteChange = true
				row.RouteFound = true
				row.RouteActive = true
				row.AfterApiKeyID = pgtype.Text{}
				return row
			}(),
			wantErr: port.ErrManagementAPIKeyDefaultRouteChange,
		},
		{
			name: "changed route missing or foreign",
			row: func() postgresqueries.UpdateManagementAPIKeyRow {
				row := managementAPIKeyUpdateQueryRow("route_1", "active", "", "")
				row.RouteChanged = true
				row.RouteFound = false
				row.AfterApiKeyID = pgtype.Text{}
				return row
			}(),
			wantErr: port.ErrManagementAPIKeyRouteStrategyNotFound,
		},
		{
			name: "changed route disabled",
			row: func() postgresqueries.UpdateManagementAPIKeyRow {
				row := managementAPIKeyUpdateQueryRow("route_1", "active", "", "")
				row.RouteChanged = true
				row.RouteFound = true
				row.RouteActive = false
				row.AfterApiKeyID = pgtype.Text{}
				return row
			}(),
			wantErr: port.ErrManagementAPIKeyRouteStrategyDisabled,
		},
		{
			name: "case insensitive duplicate name",
			queryErr: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "idx_api_keys_owner_name_unique_lower",
			},
			wantErr: port.ErrManagementAPIKeyNameExists,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			q := &managementAPIKeyUpdateQueriesStub{row: test.row, err: test.queryErr}
			_, err := updateManagementAPIKey(context.Background(), q, port.ManagementAPIKeyUpdateInput{
				APIKeyID: "key_1",
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("updateManagementAPIKey() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestUpdateManagementAPIKeyAllowsSameDisabledRouteAndPreservesOmittedFields(t *testing.T) {
	row := managementAPIKeyUpdateQueryRow("route_disabled", "disabled", "route_disabled", "disabled")
	row.RouteChanged = false
	row.RouteFound = true
	row.RouteActive = true
	row.BeforeIsDefault = pgtype.Bool{Bool: true, Valid: true}
	row.AfterIsDefault = pgtype.Bool{Bool: true, Valid: true}
	row.BeforeName = pgtype.Text{String: "旧名称", Valid: true}
	row.AfterName = pgtype.Text{String: "新名称", Valid: true}
	row.BeforeDescription = pgtype.Text{String: "不变说明", Valid: true}
	row.AfterDescription = row.BeforeDescription
	row.BeforeQuotaLimitsJson = pgtype.Text{String: `{"daily":{"enabled":true,"limit":1}}`, Valid: true}
	row.AfterQuotaLimitsJson = row.BeforeQuotaLimitsJson
	q := &managementAPIKeyUpdateQueriesStub{row: row}

	result, err := updateManagementAPIKey(context.Background(), q, port.ManagementAPIKeyUpdateInput{
		APIKeyID:             "key_1",
		OwnerSystemAccountID: "sys_owner",
		HasName:              true,
		Name:                 "新名称",
		HasRouteStrategyID:   true,
		RouteStrategyID:      "route_disabled",
	})
	if err != nil {
		t.Fatalf("updateManagementAPIKey() error = %v", err)
	}
	if result.After.Name != "新名称" ||
		!result.After.IsDefault ||
		result.After.RouteStrategyStatus != "disabled" ||
		result.After.Description == nil ||
		*result.After.Description != "不变说明" ||
		result.After.QuotaLimitsJSON == nil ||
		*result.After.QuotaLimitsJSON != `{"daily":{"enabled":true,"limit":1}}` {
		t.Fatalf("result = %+v", result)
	}
	if q.input.HasDescription || q.input.HasQuotaLimits || q.input.HasStatus {
		t.Fatalf("omitted fields became patches: %+v", q.input)
	}
}

func managementAPIKeyUpdateQueryRow(
	beforeRouteID string,
	beforeRouteStatus string,
	afterRouteID string,
	afterRouteStatus string,
) postgresqueries.UpdateManagementAPIKeyRow {
	return postgresqueries.UpdateManagementAPIKeyRow{
		BeforeApiKeyID:            pgtype.Text{String: "key_1", Valid: true},
		BeforeSystemAccountID:     pgtype.Text{String: "sys_owner", Valid: true},
		BeforeSystemAccountName:   pgtype.Text{String: "所有者", Valid: true},
		BeforeName:                pgtype.Text{String: "旧名称", Valid: true},
		BeforeKeyPrefix:           pgtype.Text{String: "sk-before", Valid: true},
		BeforeKeySuffix:           pgtype.Text{String: "before", Valid: true},
		BeforeStatus:              pgtype.Text{String: "active", Valid: true},
		BeforeIsDefault:           pgtype.Bool{Bool: false, Valid: true},
		BeforeRouteStrategyID:     pgtype.Text{String: beforeRouteID, Valid: true},
		BeforeRouteStrategyName:   pgtype.Text{String: "旧策略", Valid: true},
		BeforeRouteStrategyMode:   pgtype.Text{String: "normal", Valid: true},
		BeforeRouteStrategyStatus: pgtype.Text{String: beforeRouteStatus, Valid: true},
		AfterApiKeyID:             pgtype.Text{String: "key_1", Valid: true},
		AfterSystemAccountID:      pgtype.Text{String: "sys_owner", Valid: true},
		AfterSystemAccountName:    pgtype.Text{String: "所有者", Valid: true},
		AfterName:                 pgtype.Text{String: "旧名称", Valid: true},
		AfterKeyPrefix:            pgtype.Text{String: "sk-before", Valid: true},
		AfterKeySuffix:            pgtype.Text{String: "before", Valid: true},
		AfterStatus:               pgtype.Text{String: "active", Valid: true},
		AfterIsDefault:            pgtype.Bool{Bool: false, Valid: true},
		AfterRouteStrategyID:      pgtype.Text{String: afterRouteID, Valid: true},
		AfterRouteStrategyName:    "新策略",
		AfterRouteStrategyMode:    "normal",
		AfterRouteStrategyStatus:  afterRouteStatus,
		RouteFound:                true,
		RouteActive:               true,
	}
}

type managementAPIKeyUpdateQueriesStub struct {
	input postgresqueries.UpdateManagementAPIKeyParams
	row   postgresqueries.UpdateManagementAPIKeyRow
	err   error
	calls int
}

func (s *managementAPIKeyUpdateQueriesStub) UpdateManagementAPIKey(
	_ context.Context,
	input postgresqueries.UpdateManagementAPIKeyParams,
) (postgresqueries.UpdateManagementAPIKeyRow, error) {
	s.calls++
	s.input = input
	return s.row, s.err
}

func ptrManagementAPIKeyUpdateText(value string) *string {
	return &value
}

func ptrManagementAPIKeyUpdateTime(value time.Time) *time.Time {
	return &value
}
