package postgres

import (
	"context"
	"errors"
	"fmt"
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

func TestManagementGroupDeleteSQLIsBoundedLocksAndHardDeletes(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_group_delete.sql")
	if err != nil {
		t.Fatalf("read management group delete query: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"-- name: LockManagementGroupDeleteTarget :one",
		"groups.id = sqlc.arg(group_id)::text",
		"sqlc.arg(can_access_all)::boolean",
		"groups.system_account_id = sqlc.arg(effective_system_account_id)::text",
		"FOR UPDATE OF groups",
		"-- name: LockManagementGroupDeleteRouteStrategies :many",
		"route_strategies.name",
		"target_bindings.group_id = sqlc.arg(group_id)::text",
		"target_authorization.status = 'active'",
		"coalesce(target_settings.enabled, true) = true",
		"ORDER BY route_strategies.id ASC",
		"LIMIT 101",
		"FOR UPDATE OF route_strategies",
		"-- name: CountManagementGroupDeleteRouteStrategyLoss :one",
		"route_strategies.id = ANY(sqlc.arg(route_strategy_ids)::text[])",
		"other_bindings.group_id <> target_bindings.group_id",
		"other_authorization.status = 'active'",
		"coalesce(other_settings.enabled, true) = true",
		"-- name: HardDeleteManagementGroup :one",
		"DELETE FROM juhe_business.groups",
		"system_account_id = sqlc.arg(owner_system_account_id)::text",
		"RETURNING id",
		"-- name: MarkManagementGroupDeletedStatsDirty :exec",
		"INSERT INTO juhe_business.group_account_stats_dirty",
		"'group_deleted'",
		"ON CONFLICT (group_id) DO UPDATE SET",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("management group delete SQL missing %q", want)
		}
	}
	if got := strings.Count(sql, "LIMIT 101"); got != 1 {
		t.Fatalf("management group delete SQL LIMIT 101 count = %d, want 1", got)
	}
	deleteIndex := strings.Index(sql, "-- name: HardDeleteManagementGroup :one")
	dirtyIndex := strings.Index(sql, "-- name: MarkManagementGroupDeletedStatsDirty :exec")
	if deleteIndex < 0 || dirtyIndex <= deleteIndex {
		t.Fatalf("dirty marker query must follow hard delete query: delete=%d dirty=%d", deleteIndex, dirtyIndex)
	}
	for _, forbidden := range []string{
		"juhe_business.group_accounts",
		"deleted_at =",
		"juhe_business.api_keys",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("management group delete SQL should not contain %q", forbidden)
		}
	}
}

func TestDeleteManagementGroupPassesOwnerScopeAndOriginalGroupID(t *testing.T) {
	now := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	deletedAt := now.Add(time.Second)
	tests := []struct {
		name      string
		input     port.ManagementGroupDeleteInput
		wantScope string
		wantAll   bool
	}{
		{
			name: "self owner scope",
			input: port.ManagementGroupDeleteInput{
				GroupID:                  " group-1 ",
				EffectiveSystemAccountID: "sys-self",
				DeletedAt:                deletedAt,
				Now:                      now,
			},
			wantScope: "sys-self",
		},
		{
			name: "admin global owner scope",
			input: port.ManagementGroupDeleteInput{
				GroupID:      " group-1 ",
				CanAccessAll: true,
				DeletedAt:    deletedAt,
				Now:          now,
			},
			wantAll: true,
		},
		{
			name: "admin selected owner scope",
			input: port.ManagementGroupDeleteInput{
				GroupID:                  " group-1 ",
				CanAccessAll:             true,
				EffectiveSystemAccountID: "sys-selected",
				DeletedAt:                deletedAt,
				Now:                      now,
			},
			wantScope: "sys-selected",
			wantAll:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := successfulManagementGroupDeleteQueries(" group-1 ")
			result, err := deleteManagementGroup(context.Background(), q, tt.input)
			if err != nil {
				t.Fatalf("deleteManagementGroup() error = %v", err)
			}
			if q.lockTargetParams.GroupID != tt.input.GroupID {
				t.Fatalf("group id = %q, want original %q", q.lockTargetParams.GroupID, tt.input.GroupID)
			}
			if q.lockTargetParams.CanAccessAll != tt.wantAll ||
				q.lockTargetParams.EffectiveSystemAccountID != tt.wantScope {
				t.Fatalf("scope params = %#v", q.lockTargetParams)
			}
			if result.OwnerSystemAccountID != "sys-owner" {
				t.Fatalf("owner id = %q, want sys-owner", result.OwnerSystemAccountID)
			}
		})
	}
}

func TestDeleteManagementGroupMapsInvisibleAuthorizedAndMissingToNotFound(t *testing.T) {
	q := &managementGroupDeleteQueriesStub{lockTargetErr: pgx.ErrNoRows}
	_, err := deleteManagementGroup(context.Background(), q, port.ManagementGroupDeleteInput{
		GroupID:                  "group-authorized-or-missing",
		EffectiveSystemAccountID: "sys-grantee",
	})
	if !errors.Is(err, port.ErrManagementGroupNotFound) {
		t.Fatalf("deleteManagementGroup() error = %v, want not found", err)
	}
	if !reflect.DeepEqual(q.calls, []string{"lock_target"}) {
		t.Fatalf("query calls = %#v, want only target lock", q.calls)
	}
}

func TestDeleteManagementGroupRejectsDefault(t *testing.T) {
	q := successfulManagementGroupDeleteQueries("group-default")
	q.lockTargetRow.IsDefault = true

	_, err := deleteManagementGroup(context.Background(), q, port.ManagementGroupDeleteInput{
		GroupID:                  "group-default",
		EffectiveSystemAccountID: "sys-owner",
	})
	if !errors.Is(err, port.ErrManagementGroupDefaultReadonly) {
		t.Fatalf("deleteManagementGroup() error = %v, want default readonly", err)
	}
	if !reflect.DeepEqual(q.calls, []string{"lock_target"}) {
		t.Fatalf("query calls = %#v, want only target lock", q.calls)
	}
}

func TestDeleteManagementGroupRejectsMoreThanOneHundredRouteStrategies(t *testing.T) {
	q := successfulManagementGroupDeleteQueries("group-many-routes")
	q.routeStrategies = make([]postgresqueries.LockManagementGroupDeleteRouteStrategiesRow, maxManagementGroupDeleteRouteStrategyCount+1)
	for index := range q.routeStrategies {
		q.routeStrategies[index] = postgresqueries.LockManagementGroupDeleteRouteStrategiesRow{
			ID:   fmt.Sprintf("route-%03d", index),
			Name: fmt.Sprintf("策略 %03d", index),
		}
	}

	_, err := deleteManagementGroup(context.Background(), q, port.ManagementGroupDeleteInput{
		GroupID:                  "group-many-routes",
		EffectiveSystemAccountID: "sys-owner",
		Now:                      time.Now(),
	})
	if !errors.Is(err, port.ErrManagementGroupRouteStrategyWouldLose) {
		t.Fatalf("deleteManagementGroup() error = %v, want route strategy guard", err)
	}
	if !strings.Contains(err.Error(), "超过 100 个") || !strings.Contains(err.Error(), "删除分组") {
		t.Fatalf("route strategy limit message = %q", err.Error())
	}
	if !reflect.DeepEqual(q.calls, []string{"lock_target", "lock_routes"}) {
		t.Fatalf("query calls = %#v, want bounded route lock only", q.calls)
	}
}

func TestDeleteManagementGroupRejectsRouteStrategyLoss(t *testing.T) {
	now := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	q := successfulManagementGroupDeleteQueries("group-only")
	q.routeStrategies = []postgresqueries.LockManagementGroupDeleteRouteStrategiesRow{
		{ID: "route-b", Name: "策略 B"},
		{ID: "route-a", Name: "策略 A"},
	}
	q.routeStrategyLossCount = 1

	_, err := deleteManagementGroup(context.Background(), q, port.ManagementGroupDeleteInput{
		GroupID:                  "group-only",
		EffectiveSystemAccountID: "sys-owner",
		Now:                      now,
	})
	if !errors.Is(err, port.ErrManagementGroupRouteStrategyWouldLose) {
		t.Fatalf("deleteManagementGroup() error = %v, want route strategy guard", err)
	}
	if !strings.Contains(err.Error(), "删除后将有活跃策略路由失去唯一可用的启用分组") {
		t.Fatalf("route strategy loss message = %q", err.Error())
	}
	if !reflect.DeepEqual(q.countLossParams.RouteStrategyIds, []string{"route-a", "route-b"}) {
		t.Fatalf("loss route ids = %#v, want stable sort", q.countLossParams.RouteStrategyIds)
	}
	if !q.countLossParams.NowAt.Valid || !q.countLossParams.NowAt.Time.Equal(now) {
		t.Fatalf("loss now = %+v, want %s", q.countLossParams.NowAt, now)
	}
	if !reflect.DeepEqual(q.calls, []string{"lock_target", "lock_routes", "count_loss"}) {
		t.Fatalf("query calls = %#v, want guard before delete", q.calls)
	}
}

func TestDeleteManagementGroupHardDeletesThenMarksDirtyAndReturnsSummary(t *testing.T) {
	now := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	deletedAt := now.Add(250 * time.Millisecond)
	description := "owner group"
	policy := `{"mode":"balanced_fast"}`
	q := successfulManagementGroupDeleteQueries("group-delete")
	q.lockTargetRow.Description = pgtype.Text{String: description, Valid: true}
	q.lockTargetRow.GroupType = "high_concurrency"
	q.lockTargetRow.SchedulingPolicyJson = pgtype.Text{String: policy, Valid: true}
	q.routeStrategies = []postgresqueries.LockManagementGroupDeleteRouteStrategiesRow{
		{ID: "route-c", Name: "策略 C"},
		{ID: "route-a", Name: "策略 A"},
		{ID: "route-b", Name: "策略 B"},
	}

	result, err := deleteManagementGroup(context.Background(), q, port.ManagementGroupDeleteInput{
		GroupID:                  "group-delete",
		EffectiveSystemAccountID: "sys-owner",
		DeletedAt:                deletedAt,
		Now:                      now,
	})
	if err != nil {
		t.Fatalf("deleteManagementGroup() error = %v", err)
	}
	if !reflect.DeepEqual(q.calls, []string{"lock_target", "lock_routes", "count_loss", "hard_delete", "mark_dirty"}) {
		t.Fatalf("query calls = %#v", q.calls)
	}
	if q.hardDeleteParams.GroupID != "group-delete" ||
		q.hardDeleteParams.OwnerSystemAccountID != "sys-owner" {
		t.Fatalf("hard delete params = %#v", q.hardDeleteParams)
	}
	if q.markDirtyParams.GroupID != "group-delete" ||
		!q.markDirtyParams.DeletedAt.Valid ||
		!q.markDirtyParams.DeletedAt.Time.Equal(deletedAt) {
		t.Fatalf("dirty params = %#v, want deleted at %s", q.markDirtyParams, deletedAt)
	}
	if result.Before.ID != "group-delete" ||
		result.Before.Name != "group name" ||
		result.Before.Description == nil ||
		*result.Before.Description != description ||
		result.Before.SchedulingPolicyJSON == nil ||
		*result.Before.SchedulingPolicyJSON != policy {
		t.Fatalf("before summary = %#v", result.Before)
	}
	if result.OwnerSystemAccountID != "sys-owner" {
		t.Fatalf("owner id = %q, want sys-owner", result.OwnerSystemAccountID)
	}
	wantStrategies := []port.ManagementGroupDeletedRouteStrategy{
		{ID: "route-a", Name: "策略 A"},
		{ID: "route-b", Name: "策略 B"},
		{ID: "route-c", Name: "策略 C"},
	}
	if !reflect.DeepEqual(result.AffectedRouteStrategies, wantStrategies) {
		t.Fatalf("affected route strategies = %#v, want %#v", result.AffectedRouteStrategies, wantStrategies)
	}
}

func TestDeleteManagementGroupTransactionCommits(t *testing.T) {
	tx := &managementGroupDeleteTxStub{}
	q := successfulManagementGroupDeleteQueries("group-delete")

	_, err := deleteManagementGroupInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(got pgx.Tx) managementGroupDeleteQueries {
			if got != tx {
				t.Fatalf("queries tx = %T, want transaction stub", got)
			}
			return q
		},
		port.ManagementGroupDeleteInput{
			GroupID:                  "group-delete",
			EffectiveSystemAccountID: "sys-owner",
		},
	)
	if err != nil {
		t.Fatalf("deleteManagementGroupInTx() error = %v", err)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("commit/rollback calls = %d/%d, want 1/0", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestDeleteManagementGroupTransactionReturnsCommitFailure(t *testing.T) {
	commitErr := errors.New("commit failed")
	tx := &managementGroupDeleteTxStub{commitErr: commitErr}
	q := successfulManagementGroupDeleteQueries("group-delete")

	_, err := deleteManagementGroupInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(pgx.Tx) managementGroupDeleteQueries {
			return q
		},
		port.ManagementGroupDeleteInput{
			GroupID:                  "group-delete",
			EffectiveSystemAccountID: "sys-owner",
		},
	)
	if !errors.Is(err, commitErr) {
		t.Fatalf("deleteManagementGroupInTx() error = %v, want %v", err, commitErr)
	}
	if !strings.Contains(err.Error(), "commit management group delete tx") {
		t.Fatalf("commit error = %q", err.Error())
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback calls = %d/%d, want 1/1", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestDeleteManagementGroupTransactionRollsBackWhenDirtyMarkFails(t *testing.T) {
	dirtyErr := errors.New("dirty mark failed")
	tx := &managementGroupDeleteTxStub{}
	q := successfulManagementGroupDeleteQueries("group-delete")
	q.markDirtyErr = dirtyErr

	_, err := deleteManagementGroupInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(pgx.Tx) managementGroupDeleteQueries {
			return q
		},
		port.ManagementGroupDeleteInput{
			GroupID:                  "group-delete",
			EffectiveSystemAccountID: "sys-owner",
		},
	)
	if !errors.Is(err, dirtyErr) {
		t.Fatalf("deleteManagementGroupInTx() error = %v, want %v", err, dirtyErr)
	}
	if !reflect.DeepEqual(q.calls, []string{"lock_target", "lock_routes", "hard_delete", "mark_dirty"}) {
		t.Fatalf("query calls = %#v, want hard delete and dirty mark in one transaction", q.calls)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback calls = %d/%d, want 0/1", tx.commitCalls, tx.rollbackCalls)
	}
}

type managementGroupDeleteQueriesStub struct {
	lockTargetRow          postgresqueries.LockManagementGroupDeleteTargetRow
	lockTargetErr          error
	lockTargetParams       postgresqueries.LockManagementGroupDeleteTargetParams
	routeStrategies        []postgresqueries.LockManagementGroupDeleteRouteStrategiesRow
	lockRoutesErr          error
	lockRoutesParams       postgresqueries.LockManagementGroupDeleteRouteStrategiesParams
	routeStrategyLossCount int64
	countLossErr           error
	countLossParams        postgresqueries.CountManagementGroupDeleteRouteStrategyLossParams
	hardDeleteID           string
	hardDeleteErr          error
	hardDeleteParams       postgresqueries.HardDeleteManagementGroupParams
	markDirtyErr           error
	markDirtyParams        postgresqueries.MarkManagementGroupDeletedStatsDirtyParams
	calls                  []string
}

func successfulManagementGroupDeleteQueries(groupID string) *managementGroupDeleteQueriesStub {
	return &managementGroupDeleteQueriesStub{
		lockTargetRow: postgresqueries.LockManagementGroupDeleteTargetRow{
			ID:              groupID,
			SystemAccountID: "sys-owner",
			Name:            "group name",
			ProviderCode:    "openai",
			Enabled:         true,
			GroupType:       "personal",
		},
		hardDeleteID: groupID,
	}
}

func (s *managementGroupDeleteQueriesStub) LockManagementGroupDeleteTarget(
	_ context.Context,
	arg postgresqueries.LockManagementGroupDeleteTargetParams,
) (postgresqueries.LockManagementGroupDeleteTargetRow, error) {
	s.calls = append(s.calls, "lock_target")
	s.lockTargetParams = arg
	return s.lockTargetRow, s.lockTargetErr
}

func (s *managementGroupDeleteQueriesStub) LockManagementGroupDeleteRouteStrategies(
	_ context.Context,
	arg postgresqueries.LockManagementGroupDeleteRouteStrategiesParams,
) ([]postgresqueries.LockManagementGroupDeleteRouteStrategiesRow, error) {
	s.calls = append(s.calls, "lock_routes")
	s.lockRoutesParams = arg
	return append([]postgresqueries.LockManagementGroupDeleteRouteStrategiesRow(nil), s.routeStrategies...), s.lockRoutesErr
}

func (s *managementGroupDeleteQueriesStub) CountManagementGroupDeleteRouteStrategyLoss(
	_ context.Context,
	arg postgresqueries.CountManagementGroupDeleteRouteStrategyLossParams,
) (int64, error) {
	s.calls = append(s.calls, "count_loss")
	s.countLossParams = arg
	return s.routeStrategyLossCount, s.countLossErr
}

func (s *managementGroupDeleteQueriesStub) HardDeleteManagementGroup(
	_ context.Context,
	arg postgresqueries.HardDeleteManagementGroupParams,
) (string, error) {
	s.calls = append(s.calls, "hard_delete")
	s.hardDeleteParams = arg
	return s.hardDeleteID, s.hardDeleteErr
}

func (s *managementGroupDeleteQueriesStub) MarkManagementGroupDeletedStatsDirty(
	_ context.Context,
	arg postgresqueries.MarkManagementGroupDeletedStatsDirtyParams,
) error {
	s.calls = append(s.calls, "mark_dirty")
	s.markDirtyParams = arg
	return s.markDirtyErr
}

type managementGroupDeleteTxStub struct {
	pgx.Tx
	commitErr     error
	rollbackErr   error
	commitCalls   int
	rollbackCalls int
}

func (s *managementGroupDeleteTxStub) Commit(context.Context) error {
	s.commitCalls++
	return s.commitErr
}

func (s *managementGroupDeleteTxStub) Rollback(context.Context) error {
	s.rollbackCalls++
	return s.rollbackErr
}

var _ pgx.Tx = (*managementGroupDeleteTxStub)(nil)
