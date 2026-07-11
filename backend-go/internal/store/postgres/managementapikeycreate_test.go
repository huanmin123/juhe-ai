package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestW5ManagementAPIKeyCreateSQLLocksRouteAndWritesAtomically(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_api_key_create.sql")
	if err != nil {
		t.Fatalf("read W5 management API Key create SQL: %v", err)
	}
	sql := string(source)
	for _, required := range []string{
		"-- name: CreateManagementAPIKey :one",
		"route_target AS MATERIALIZED",
		"FOR UPDATE OF route_strategies",
		"inserted_api_key AS",
		"INSERT INTO juhe_business.api_keys",
		"key_secret_encrypted",
		"availability_schedule_next_check_at",
		"false",
		"request_quota_hourly_window_configs",
		"ON CONFLICT (window_hours) DO UPDATE",
		"updated_at = EXCLUDED.updated_at",
		"FROM inserted_api_key",
		"LEFT JOIN inserted_api_key",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("W5 management API Key create SQL missing %q", required)
		}
	}
	lockIndex := strings.Index(sql, "FOR UPDATE OF route_strategies")
	insertIndex := strings.Index(sql, "INSERT INTO juhe_business.api_keys")
	hourlyIndex := strings.Index(sql, "request_quota_hourly_window_configs")
	if lockIndex < 0 || insertIndex <= lockIndex || hourlyIndex <= insertIndex {
		t.Fatalf("create SQL does not lock, insert, and upsert in one ordered CTE: %s", sql)
	}
	for _, forbidden := range []string{
		"BEGIN",
		"COMMIT",
		"usage_records",
		"SUM(",
		"GROUP BY",
	} {
		if strings.Contains(strings.ToUpper(sql), strings.ToUpper(forbidden)) {
			t.Fatalf("W5 management API Key create SQL must not contain %q", forbidden)
		}
	}
}

func TestCreateManagementAPIKeyMapsAllFieldsAndHourlyWindow(t *testing.T) {
	now := time.Date(2026, 7, 13, 1, 2, 3, 456000000, time.UTC)
	expiresAt := now.Add(24 * time.Hour)
	nextCheckAt := now.Add(time.Hour)
	description := "说明"
	quotaJSON := `{"hourly":{"enabled":true,"hours":6,"limit":1.25}}`
	scheduleJSON := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1],"start":"01:00","end":"02:00"}]}`
	hours := 6
	q := &managementAPIKeyCreateQueriesStub{
		row: postgresqueries.CreateManagementAPIKeyRow{
			ApiKeyID:                 pgtype.Text{String: "key_1", Valid: true},
			SystemAccountID:          pgtype.Text{String: "sys_owner", Valid: true},
			SystemAccountName:        "所有者",
			ApiKeyName:               pgtype.Text{String: "生产 Key", Valid: true},
			Description:              pgtype.Text{String: description, Valid: true},
			KeyPrefix:                pgtype.Text{String: "sk-prefix", Valid: true},
			KeySuffix:                pgtype.Text{String: "suffix", Valid: true},
			ApiKeyStatus:             pgtype.Text{String: "active", Valid: true},
			IsDefault:                pgtype.Bool{Bool: false, Valid: true},
			RouteStrategyID:          pgtype.Text{String: "route_1", Valid: true},
			RouteStrategyName:        "默认策略",
			RouteStrategyMode:        "normal",
			RouteStrategyStatus:      "active",
			ExpiresAt:                pgtype.Timestamptz{Time: expiresAt, Valid: true},
			QuotaLimitsJson:          pgtype.Text{String: quotaJSON, Valid: true},
			AvailabilityScheduleJson: pgtype.Text{String: scheduleJSON, Valid: true},
			HourlyUpsertCount:        1,
		},
	}

	row, err := createManagementAPIKey(context.Background(), q, port.ManagementAPIKeyCreateInput{
		ID:                              " key_1 ",
		SystemAccountID:                 " sys_owner ",
		RouteStrategyID:                 " route_1 ",
		Name:                            "生产 Key",
		Description:                     &description,
		KeyHash:                         "hash",
		KeyPrefix:                       "sk-prefix",
		KeySuffix:                       "suffix",
		KeySecretEncrypted:              "v1:nonce:tag:cipher",
		Status:                          "active",
		IsDefault:                       false,
		ExpiresAt:                       &expiresAt,
		QuotaLimitsJSON:                 &quotaJSON,
		HourlyQuotaHours:                &hours,
		AvailabilityScheduleJSON:        &scheduleJSON,
		AvailabilityScheduleNextCheckAt: &nextCheckAt,
		CreatedAt:                       now,
		UpdatedAt:                       now,
	})
	if err != nil {
		t.Fatalf("createManagementAPIKey() error = %v", err)
	}
	if q.calls != 1 {
		t.Fatalf("query calls = %d, want 1", q.calls)
	}
	if q.input != (postgresqueries.CreateManagementAPIKeyParams{
		ID:                              "key_1",
		SystemAccountID:                 "sys_owner",
		RouteStrategyID:                 "route_1",
		Name:                            "生产 Key",
		Description:                     pgtype.Text{String: description, Valid: true},
		KeyHash:                         "hash",
		KeyPrefix:                       "sk-prefix",
		KeySuffix:                       "suffix",
		KeySecretEncrypted:              "v1:nonce:tag:cipher",
		Status:                          "active",
		ExpiresAt:                       pgtype.Timestamptz{Time: expiresAt, Valid: true},
		QuotaLimitsJson:                 pgtype.Text{String: quotaJSON, Valid: true},
		AvailabilityScheduleJson:        pgtype.Text{String: scheduleJSON, Valid: true},
		AvailabilityScheduleNextCheckAt: pgtype.Timestamptz{Time: nextCheckAt, Valid: true},
		CreatedAt:                       pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:                       pgtype.Timestamptz{Time: now, Valid: true},
		HourlyHours:                     pgtype.Int4{Int32: 6, Valid: true},
	}) {
		t.Fatalf("query input = %+v", q.input)
	}
	if row.ID != "key_1" ||
		row.SystemAccountID != "sys_owner" ||
		row.SystemAccountName != "所有者" ||
		row.Name != "生产 Key" ||
		row.Description == nil ||
		*row.Description != description ||
		row.RouteStrategyID != "route_1" ||
		row.RouteStrategyName != "默认策略" ||
		row.RouteStrategyMode != "normal" ||
		row.RouteStrategyStatus != "active" ||
		row.ExpiresAt == nil ||
		!row.ExpiresAt.Equal(expiresAt) ||
		row.QuotaLimitsJSON == nil ||
		*row.QuotaLimitsJSON != quotaJSON ||
		row.AvailabilityScheduleJSON == nil ||
		*row.AvailabilityScheduleJSON != scheduleJSON {
		t.Fatalf("row = %+v", row)
	}
}

func TestCreateManagementAPIKeyMapsRouteAndConstraintErrors(t *testing.T) {
	tests := []struct {
		name     string
		row      postgresqueries.CreateManagementAPIKeyRow
		queryErr error
		wantErr  error
	}{
		{
			name:     "route missing",
			queryErr: pgx.ErrNoRows,
			wantErr:  port.ErrManagementAPIKeyRouteStrategyNotFound,
		},
		{
			name: "route disabled",
			row: postgresqueries.CreateManagementAPIKeyRow{
				RouteStrategyStatus: "disabled",
			},
			wantErr: port.ErrManagementAPIKeyRouteStrategyDisabled,
		},
		{
			name: "duplicate name",
			queryErr: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "idx_api_keys_owner_name_unique_lower",
			},
			wantErr: port.ErrManagementAPIKeyNameExists,
		},
		{
			name: "duplicate hash",
			queryErr: &pgconn.PgError{
				Code:           "23505",
				ConstraintName: "idx_api_keys_key_hash_unique",
			},
			wantErr: port.ErrManagementAPIKeyHashExists,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			q := &managementAPIKeyCreateQueriesStub{row: test.row, err: test.queryErr}
			_, err := createManagementAPIKey(context.Background(), q, port.ManagementAPIKeyCreateInput{
				ID:              "key_1",
				SystemAccountID: "sys_owner",
				RouteStrategyID: "route_1",
				Name:            "生产 Key",
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("createManagementAPIKey() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

type managementAPIKeyCreateQueriesStub struct {
	input postgresqueries.CreateManagementAPIKeyParams
	row   postgresqueries.CreateManagementAPIKeyRow
	err   error
	calls int
}

func (s *managementAPIKeyCreateQueriesStub) CreateManagementAPIKey(
	_ context.Context,
	arg postgresqueries.CreateManagementAPIKeyParams,
) (postgresqueries.CreateManagementAPIKeyRow, error) {
	s.calls++
	s.input = arg
	return s.row, s.err
}

var _ managementAPIKeyCreateQueries = (*managementAPIKeyCreateQueriesStub)(nil)
