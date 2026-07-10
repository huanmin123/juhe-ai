package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestW5ManagementGroupDetailSQLUsesExactVisibilityAndCurrentTables(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_group_detail.sql")
	if err != nil {
		t.Fatalf("read W5 management group detail query: %v", err)
	}
	sql := string(source)
	detailSQL := querySection(t, sql, "-- name: FindManagementGroupDetail :one", "-- name: ListManagementGroupDetailAccountIDs :many")
	assertSQLContainsAll(t, detailSQL, []string{
		"groups.id = sqlc.arg(group_id)::text",
		"sqlc.arg(system_account_id)::text = ''",
		"groups.system_account_id = sqlc.arg(system_account_id)::text",
		"'owner'::text AS access_type",
		"UNION ALL",
		"resource_authorizations.grantee_system_account_id = sqlc.arg(system_account_id)::text",
		"resource_authorizations.status IN ('active', 'paused', 'expired')",
		"groups.system_account_id <> sqlc.arg(system_account_id)::text",
		"WHEN groups.enabled THEN coalesce(group_authorization_settings.enabled, true)",
		"coalesce(group_authorization_settings.group_type, groups.group_type)",
		"coalesce(group_authorization_settings.scheduling_policy_json, groups.scheduling_policy_json)",
		"resource_authorizations.limits_json AS authorization_limits_json",
	})
	assertSQLExcludesAll(t, detailSQL, []string{"LIMIT sqlc.arg", "OFFSET", "usage_records"})

	accountSQL := querySection(t, sql, "-- name: ListManagementGroupDetailAccountIDs :many", "-- name: ListManagementGroupDetailAuthorizationSources :many")
	assertSQLContainsAll(t, accountSQL, []string{
		"groups.id = sqlc.arg(group_id)::text",
		"group_accounts.enabled = true",
		"accounts.deleted_at IS NULL",
		"group_accounts.account_authorization_id IS NULL",
		"accounts.authorization_instance_authorization_id IS NULL",
		"account_authorizations.id = group_accounts.account_authorization_id",
		"account_authorizations.id = accounts.authorization_instance_authorization_id",
		"account_authorizations.resource_type = 'account'",
		"account_authorizations.status IN ('active', 'paused', 'expired')",
		"ORDER BY group_accounts.created_at ASC, group_accounts.account_id ASC",
	})
	assertSQLExcludesAll(t, accountSQL, []string{"usage_records", "COUNT(", "SUM(", "GROUP BY"})

	sourceSQL := querySection(t, sql, "-- name: ListManagementGroupDetailAuthorizationSources :many", "")
	assertSQLContainsAll(t, sourceSQL, []string{
		"groups.id = sqlc.arg(group_id)::text",
		"groups.system_account_id <> sqlc.arg(system_account_id)::text",
		"resource_authorizations.status IN ('active', 'paused', 'expired')",
		"INNER JOIN juhe_business.resource_authorization_sources",
		"authorization_sources.activated_at",
		"authorization_sources.ended_at",
		"authorization_sources.ended_reason",
		"authorization_sources.created_by",
		"authorization_sources.revoked_by",
		"authorization_sources.revoked_at",
		"authorization_sources.updated_at",
	})
	assertSQLExcludesAll(t, sourceSQL, []string{"usage_records", "COUNT(", "SUM(", "GROUP BY"})
}

func TestFindManagementGroupDetailMapsAuthorizedRow(t *testing.T) {
	updatedAt := time.Date(2026, 7, 11, 8, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	expiresAt := updatedAt.Add(24 * time.Hour)
	q := &managementGroupDetailQueriesStub{
		detailRow: postgresqueries.FindManagementGroupDetailRow{
			ID:                      "group_1",
			SystemAccountID:         "sys_owner",
			SystemAccountName:       "Owner",
			Name:                    "Shared",
			ProviderCode:            "openai",
			Description:             pgtype.Text{String: "detail", Valid: true},
			Enabled:                 true,
			GroupType:               "high_concurrency",
			SchedulingPolicyJson:    pgtype.Text{String: `{"mode":"balanced_fast"}`, Valid: true},
			AccessType:              "authorized",
			GroupAuthorizationID:    pgtype.Text{String: "auth_group", Valid: true},
			AuthorizationStatus:     pgtype.Text{String: "paused", Valid: true},
			AuthorizationExpiresAt:  pgtype.Timestamptz{Time: expiresAt, Valid: true},
			AuthorizationLimitsJson: pgtype.Text{String: `{"daily":{"limit":10}}`, Valid: true},
			EffectiveUpdatedAt:      pgtype.Timestamptz{Time: updatedAt, Valid: true},
		},
	}

	row, found, err := findManagementGroupDetail(context.Background(), q, port.ManagementGroupDetailInput{
		GroupID:         " group_1 ",
		SystemAccountID: " viewer ",
	})
	if err != nil {
		t.Fatalf("findManagementGroupDetail() error = %v", err)
	}
	if !found || row.ID != "group_1" || row.AccessType != "authorized" || row.AuthorizationStatus != "paused" {
		t.Fatalf("detail = %#v, found = %v", row, found)
	}
	if len(q.detailCalls) != 1 || q.detailCalls[0].GroupID != "group_1" || q.detailCalls[0].SystemAccountID != "viewer" {
		t.Fatalf("detail calls = %#v", q.detailCalls)
	}
	if row.AuthorizationExpiresAt == nil || !row.AuthorizationExpiresAt.Equal(expiresAt.UTC()) {
		t.Fatalf("authorization expiry = %#v", row.AuthorizationExpiresAt)
	}
	if row.SchedulingPolicyJSON == nil || *row.SchedulingPolicyJSON != `{"mode":"balanced_fast"}` {
		t.Fatalf("scheduling policy = %#v", row.SchedulingPolicyJSON)
	}
	if !row.EffectiveUpdatedAt.Equal(updatedAt.UTC()) {
		t.Fatalf("effective updated at = %v", row.EffectiveUpdatedAt)
	}
}

func TestManagementGroupDetailReadersHandleNotFoundAccountsAndSources(t *testing.T) {
	q := &managementGroupDetailQueriesStub{
		detailErr:  pgx.ErrNoRows,
		accountIDs: []string{"account_owner", "account_authorized"},
		sourceRows: []postgresqueries.ListManagementGroupDetailAuthorizationSourcesRow{
			{
				ID:              "source_1",
				AuthorizationID: "auth_group",
				SourceType:      "team",
				SourceTeamID:    "team_1",
				SourceTeamName:  "Ops",
				Status:          "revoked",
				ActivatedAt:     pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 1, 0, 0, 0, time.UTC), Valid: true},
				EndedAt:         pgtype.Timestamptz{Time: time.Date(2026, 7, 2, 1, 0, 0, 0, time.UTC), Valid: true},
				EndedReason:     "team_member_removed",
				CreatedBy:       "sys_admin",
				CreatedAt:       pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true},
				RevokedBy:       "sys_admin",
				RevokedAt:       pgtype.Timestamptz{Time: time.Date(2026, 7, 2, 1, 0, 0, 0, time.UTC), Valid: true},
				UpdatedAt:       pgtype.Timestamptz{Time: time.Date(2026, 7, 2, 1, 0, 0, 0, time.UTC), Valid: true},
			},
		},
	}
	input := port.ManagementGroupDetailInput{GroupID: "group_1", SystemAccountID: "viewer"}
	_, found, err := findManagementGroupDetail(context.Background(), q, input)
	if err != nil || found {
		t.Fatalf("not found result: found=%v err=%v", found, err)
	}
	ids, err := listManagementGroupDetailAccountIDs(context.Background(), q, input)
	if err != nil || !reflect.DeepEqual(ids, q.accountIDs) {
		t.Fatalf("account ids = %#v, err = %v", ids, err)
	}
	sources, err := listManagementGroupDetailAuthorizationSources(context.Background(), q, input)
	if err != nil {
		t.Fatalf("listManagementGroupDetailAuthorizationSources() error = %v", err)
	}
	if len(sources) != 1 || sources[0].SourceTeamName != "Ops" || sources[0].EndedReason != "team_member_removed" {
		t.Fatalf("sources = %#v", sources)
	}
	if sources[0].ActivatedAt == nil || sources[0].EndedAt == nil || sources[0].RevokedAt == nil {
		t.Fatalf("source timestamps = %#v", sources[0])
	}
	if !reflect.DeepEqual(q.accountCalls, []postgresqueries.ListManagementGroupDetailAccountIDsParams{{GroupID: "group_1", SystemAccountID: "viewer"}}) {
		t.Fatalf("account calls = %#v", q.accountCalls)
	}
	if !reflect.DeepEqual(q.sourceCalls, []postgresqueries.ListManagementGroupDetailAuthorizationSourcesParams{{GroupID: "group_1", SystemAccountID: "viewer"}}) {
		t.Fatalf("source calls = %#v", q.sourceCalls)
	}
}

func TestManagementGroupDetailReadersWrapQueryErrors(t *testing.T) {
	q := &managementGroupDetailQueriesStub{
		detailErr:  errors.New("detail failed"),
		accountErr: errors.New("accounts failed"),
		sourceErr:  errors.New("sources failed"),
	}
	input := port.ManagementGroupDetailInput{GroupID: "group_1"}
	if _, _, err := findManagementGroupDetail(context.Background(), q, input); err == nil || !strings.Contains(err.Error(), "find management group detail") {
		t.Fatalf("detail error = %v", err)
	}
	if _, err := listManagementGroupDetailAccountIDs(context.Background(), q, input); err == nil || !strings.Contains(err.Error(), "list management group detail account ids") {
		t.Fatalf("account error = %v", err)
	}
	if _, err := listManagementGroupDetailAuthorizationSources(context.Background(), q, input); err == nil || !strings.Contains(err.Error(), "list management group detail authorization sources") {
		t.Fatalf("source error = %v", err)
	}
}

type managementGroupDetailQueriesStub struct {
	detailRow    postgresqueries.FindManagementGroupDetailRow
	detailErr    error
	detailCalls  []postgresqueries.FindManagementGroupDetailParams
	accountIDs   []string
	accountErr   error
	accountCalls []postgresqueries.ListManagementGroupDetailAccountIDsParams
	sourceRows   []postgresqueries.ListManagementGroupDetailAuthorizationSourcesRow
	sourceErr    error
	sourceCalls  []postgresqueries.ListManagementGroupDetailAuthorizationSourcesParams
}

func (s *managementGroupDetailQueriesStub) FindManagementGroupDetail(
	_ context.Context,
	arg postgresqueries.FindManagementGroupDetailParams,
) (postgresqueries.FindManagementGroupDetailRow, error) {
	s.detailCalls = append(s.detailCalls, arg)
	return s.detailRow, s.detailErr
}

func (s *managementGroupDetailQueriesStub) ListManagementGroupDetailAccountIDs(
	_ context.Context,
	arg postgresqueries.ListManagementGroupDetailAccountIDsParams,
) ([]string, error) {
	s.accountCalls = append(s.accountCalls, arg)
	return s.accountIDs, s.accountErr
}

func (s *managementGroupDetailQueriesStub) ListManagementGroupDetailAuthorizationSources(
	_ context.Context,
	arg postgresqueries.ListManagementGroupDetailAuthorizationSourcesParams,
) ([]postgresqueries.ListManagementGroupDetailAuthorizationSourcesRow, error) {
	s.sourceCalls = append(s.sourceCalls, arg)
	return s.sourceRows, s.sourceErr
}

var _ managementGroupDetailQueries = (*managementGroupDetailQueriesStub)(nil)
