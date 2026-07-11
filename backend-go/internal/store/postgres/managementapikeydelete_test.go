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

func TestManagementAPIKeyDeleteSQLLocksAndDeletesByOwner(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_api_key_delete.sql")
	if err != nil {
		t.Fatalf("read management API Key delete SQL: %v", err)
	}
	sql := string(source)
	for _, want := range []string{
		"-- name: LockManagementAPIKeyDeleteTarget :one",
		"api_keys.id = sqlc.arg(api_key_id)::text",
		"sqlc.arg(owner_system_account_id)::text = ''",
		"api_keys.system_account_id = sqlc.arg(owner_system_account_id)::text",
		"api_keys.name",
		"api_keys.is_default",
		"FOR UPDATE OF api_keys",
		"-- name: HardDeleteManagementAPIKey :one",
		"DELETE FROM juhe_business.api_keys",
		"id = sqlc.arg(api_key_id)::text",
		"system_account_id = sqlc.arg(owner_system_account_id)::text",
		"RETURNING id",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("management API Key delete SQL missing %q", want)
		}
	}
	lockIndex := strings.Index(sql, "-- name: LockManagementAPIKeyDeleteTarget :one")
	deleteIndex := strings.Index(sql, "-- name: HardDeleteManagementAPIKey :one")
	if lockIndex < 0 || deleteIndex <= lockIndex {
		t.Fatalf("management API Key delete SQL order is invalid")
	}
	for _, forbidden := range []string{
		"api_key_record_cleanup_targets",
		"usage_records",
		"usage_stats",
		"audit_logs",
		"operation_logs",
		"SUM(",
		"GROUP BY",
	} {
		if strings.Contains(strings.ToUpper(sql), strings.ToUpper(forbidden)) {
			t.Fatalf("management API Key delete SQL must not contain %q", forbidden)
		}
	}
}

func TestManagementAPIKeyDeleteReusesCleanupUpsertWithoutResettingRetryOrCreatedFields(t *testing.T) {
	source, err := os.ReadFile("queries/w1b_public_api_keys.sql")
	if err != nil {
		t.Fatalf("read public API Key SQL: %v", err)
	}
	sql := string(source)
	start := strings.Index(sql, "-- name: UpsertPublicAPIKeyRecordCleanupTarget :exec")
	if start < 0 {
		t.Fatal("cleanup target upsert query is missing")
	}
	upsert := sql[start:]
	for _, want := range []string{
		"ON CONFLICT (api_key_id) DO UPDATE SET",
		"system_account_id = EXCLUDED.system_account_id",
		"updated_at = EXCLUDED.updated_at",
	} {
		if !strings.Contains(upsert, want) {
			t.Fatalf("cleanup target upsert missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"created_at = EXCLUDED.created_at",
		"attempt_count =",
		"last_attempt_at =",
		"last_blocked_reason =",
		"last_error_message =",
	} {
		if strings.Contains(upsert, forbidden) {
			t.Fatalf("cleanup target upsert must preserve retry/created fields, found %q", forbidden)
		}
	}
}

func TestDeleteManagementAPIKeyLocksDeletesAndUpsertsCleanupTarget(t *testing.T) {
	deletedAt := time.Date(2026, 7, 12, 3, 4, 5, 0, time.UTC)
	q := successfulManagementAPIKeyDeleteQueries()

	result, err := deleteManagementAPIKey(context.Background(), q, port.ManagementAPIKeyDeleteInput{
		APIKeyID:             " key_1 ",
		OwnerSystemAccountID: " sys_owner ",
		DeletedAt:            deletedAt,
	})
	if err != nil {
		t.Fatalf("deleteManagementAPIKey() error = %v", err)
	}
	if got, want := q.calls, []string{"lock", "delete", "upsert"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("query calls = %v, want %v", got, want)
	}
	if q.lockInput.ApiKeyID != "key_1" ||
		q.lockInput.OwnerSystemAccountID != "sys_owner" {
		t.Fatalf("lock input = %+v", q.lockInput)
	}
	if q.deleteInput.ApiKeyID != "key_1" ||
		q.deleteInput.OwnerSystemAccountID != "sys_owner" {
		t.Fatalf("delete input = %+v", q.deleteInput)
	}
	if q.upsertInput != (postgresqueries.UpsertPublicAPIKeyRecordCleanupTargetParams{
		ApiKeyID:        "key_1",
		SystemAccountID: "sys_owner",
		CreatedAt:       pgtype.Timestamptz{Time: deletedAt, Valid: true},
		UpdatedAt:       pgtype.Timestamptz{Time: deletedAt, Valid: true},
	}) {
		t.Fatalf("cleanup upsert input = %+v", q.upsertInput)
	}
	if result != (port.ManagementAPIKeyDeleteResult{
		APIKeyID:             "key_1",
		Name:                 "生产 Key",
		OwnerSystemAccountID: "sys_owner",
	}) {
		t.Fatalf("delete result = %+v", result)
	}
}

func TestDeleteManagementAPIKeyMapsMissingOrWrongOwnerToNotFound(t *testing.T) {
	q := &managementAPIKeyDeleteQueriesStub{lockErr: pgx.ErrNoRows}

	_, err := deleteManagementAPIKey(context.Background(), q, port.ManagementAPIKeyDeleteInput{
		APIKeyID:             "key_missing",
		OwnerSystemAccountID: "sys_wrong",
	})
	if !errors.Is(err, port.ErrManagementAPIKeyNotFound) {
		t.Fatalf("deleteManagementAPIKey() error = %v, want not found", err)
	}
	if got, want := q.calls, []string{"lock"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("query calls = %v, want %v", got, want)
	}
}

func TestDeleteManagementAPIKeyRejectsDefaultBeforeDelete(t *testing.T) {
	q := successfulManagementAPIKeyDeleteQueries()
	q.lockRow.IsDefault = true

	_, err := deleteManagementAPIKey(context.Background(), q, port.ManagementAPIKeyDeleteInput{
		APIKeyID: "key_default",
	})
	if !errors.Is(err, port.ErrManagementAPIKeyDefaultDelete) {
		t.Fatalf("deleteManagementAPIKey() error = %v, want default delete", err)
	}
	if got, want := q.calls, []string{"lock"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("query calls = %v, want %v", got, want)
	}
}

func TestDeleteManagementAPIKeyMapsConcurrentDeleteToNotFoundAndSkipsCleanup(t *testing.T) {
	q := successfulManagementAPIKeyDeleteQueries()
	q.deleteErr = pgx.ErrNoRows

	_, err := deleteManagementAPIKey(context.Background(), q, port.ManagementAPIKeyDeleteInput{
		APIKeyID: "key_1",
	})
	if !errors.Is(err, port.ErrManagementAPIKeyNotFound) {
		t.Fatalf("deleteManagementAPIKey() error = %v, want not found", err)
	}
	if got, want := q.calls, []string{"lock", "delete"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("query calls = %v, want %v", got, want)
	}
}

func TestDeleteManagementAPIKeyTransactionCommits(t *testing.T) {
	tx := &managementAPIKeyDeleteTxStub{}
	q := successfulManagementAPIKeyDeleteQueries()

	result, err := deleteManagementAPIKeyInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(got pgx.Tx) managementAPIKeyDeleteQueries {
			if got != tx {
				t.Fatalf("queries tx = %T, want transaction stub", got)
			}
			return q
		},
		port.ManagementAPIKeyDeleteInput{
			APIKeyID:  "key_1",
			DeletedAt: time.Date(2026, 7, 12, 3, 4, 5, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("deleteManagementAPIKeyInTx() error = %v", err)
	}
	if result.APIKeyID != "key_1" ||
		tx.commitCalls != 1 ||
		tx.rollbackCalls != 0 {
		t.Fatalf("result=%+v commit/rollback=%d/%d", result, tx.commitCalls, tx.rollbackCalls)
	}
}

func TestDeleteManagementAPIKeyTransactionRollsBackWhenCleanupUpsertFails(t *testing.T) {
	upsertErr := errors.New("dataset unavailable")
	tx := &managementAPIKeyDeleteTxStub{}
	q := successfulManagementAPIKeyDeleteQueries()
	q.upsertErr = upsertErr

	_, err := deleteManagementAPIKeyInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(pgx.Tx) managementAPIKeyDeleteQueries {
			return q
		},
		port.ManagementAPIKeyDeleteInput{
			APIKeyID: "key_1",
		},
	)
	if !errors.Is(err, upsertErr) {
		t.Fatalf("deleteManagementAPIKeyInTx() error = %v, want %v", err, upsertErr)
	}
	if got, want := q.calls, []string{"lock", "delete", "upsert"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("query calls = %v, want %v", got, want)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback calls = %d/%d, want 0/1", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestDeleteManagementAPIKeyTransactionReturnsCommitFailureAndRollsBack(t *testing.T) {
	commitErr := errors.New("commit failed")
	tx := &managementAPIKeyDeleteTxStub{commitErr: commitErr}
	q := successfulManagementAPIKeyDeleteQueries()

	_, err := deleteManagementAPIKeyInTx(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			return tx, nil
		},
		func(pgx.Tx) managementAPIKeyDeleteQueries {
			return q
		},
		port.ManagementAPIKeyDeleteInput{
			APIKeyID: "key_1",
		},
	)
	if !errors.Is(err, commitErr) ||
		!strings.Contains(err.Error(), "commit management API Key delete tx") {
		t.Fatalf("deleteManagementAPIKeyInTx() error = %v, want commit failure", err)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback calls = %d/%d, want 1/1", tx.commitCalls, tx.rollbackCalls)
	}
}

type managementAPIKeyDeleteQueriesStub struct {
	lockInput   postgresqueries.LockManagementAPIKeyDeleteTargetParams
	lockRow     postgresqueries.LockManagementAPIKeyDeleteTargetRow
	lockErr     error
	deleteInput postgresqueries.HardDeleteManagementAPIKeyParams
	deleteID    string
	deleteErr   error
	upsertInput postgresqueries.UpsertPublicAPIKeyRecordCleanupTargetParams
	upsertErr   error
	calls       []string
}

func successfulManagementAPIKeyDeleteQueries() *managementAPIKeyDeleteQueriesStub {
	return &managementAPIKeyDeleteQueriesStub{
		lockRow: postgresqueries.LockManagementAPIKeyDeleteTargetRow{
			ID:              "key_1",
			SystemAccountID: "sys_owner",
			Name:            "生产 Key",
		},
		deleteID: "key_1",
	}
}

func (s *managementAPIKeyDeleteQueriesStub) LockManagementAPIKeyDeleteTarget(
	_ context.Context,
	input postgresqueries.LockManagementAPIKeyDeleteTargetParams,
) (postgresqueries.LockManagementAPIKeyDeleteTargetRow, error) {
	s.calls = append(s.calls, "lock")
	s.lockInput = input
	return s.lockRow, s.lockErr
}

func (s *managementAPIKeyDeleteQueriesStub) HardDeleteManagementAPIKey(
	_ context.Context,
	input postgresqueries.HardDeleteManagementAPIKeyParams,
) (string, error) {
	s.calls = append(s.calls, "delete")
	s.deleteInput = input
	return s.deleteID, s.deleteErr
}

func (s *managementAPIKeyDeleteQueriesStub) UpsertPublicAPIKeyRecordCleanupTarget(
	_ context.Context,
	input postgresqueries.UpsertPublicAPIKeyRecordCleanupTargetParams,
) error {
	s.calls = append(s.calls, "upsert")
	s.upsertInput = input
	return s.upsertErr
}

type managementAPIKeyDeleteTxStub struct {
	pgx.Tx
	commitErr     error
	rollbackErr   error
	commitCalls   int
	rollbackCalls int
}

func (s *managementAPIKeyDeleteTxStub) Commit(context.Context) error {
	s.commitCalls++
	return s.commitErr
}

func (s *managementAPIKeyDeleteTxStub) Rollback(context.Context) error {
	s.rollbackCalls++
	return s.rollbackErr
}

var _ pgx.Tx = (*managementAPIKeyDeleteTxStub)(nil)
