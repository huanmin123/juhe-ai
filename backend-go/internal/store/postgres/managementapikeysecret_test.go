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

func TestW5ManagementAPIKeySecretSQLScopesRevealLocksRefreshAndAvoidsDetailScans(t *testing.T) {
	source, err := os.ReadFile("queries/w5_management_api_key_secret.sql")
	if err != nil {
		t.Fatalf("read W5 management API Key secret query: %v", err)
	}
	sql := string(source)
	for _, required := range []string{
		"-- name: FindManagementAPIKeySecret :one",
		"api_keys.key_secret_encrypted",
		"api_keys.id = sqlc.arg(api_key_id)::text",
		"sqlc.arg(system_account_id)::text = ''",
		"OR api_keys.system_account_id = sqlc.arg(system_account_id)::text",
		"-- name: LockManagementAPIKeySecretRefreshTarget :one",
		"FOR UPDATE OF api_keys",
		"-- name: UpdateManagementAPIKeySecret :one",
		"SET key_hash = sqlc.arg(key_hash)::text",
		"key_prefix = sqlc.arg(key_prefix)::text",
		"key_suffix = sqlc.arg(key_suffix)::text",
		"key_secret_encrypted = sqlc.arg(key_secret_encrypted)::text",
		"updated_at = sqlc.arg(updated_at)::timestamptz",
		"RETURNING id",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("W5 management API Key secret SQL missing %q", required)
		}
	}
	lockMarker := "-- name: LockManagementAPIKeySecretRefreshTarget :one"
	updateMarker := "-- name: UpdateManagementAPIKeySecret :one"
	lockIndex := strings.Index(sql, lockMarker)
	updateIndex := strings.Index(sql, updateMarker)
	if lockIndex < 0 || updateIndex <= lockIndex {
		t.Fatalf("secret SQL query order is invalid")
	}
	lockSQL := sql[lockIndex:updateIndex]
	if strings.Contains(lockSQL, "key_secret_encrypted") {
		t.Fatalf("refresh lock must not read ciphertext: %s", lockSQL)
	}
	for _, forbidden := range []string{
		"usage_records",
		"COUNT(",
		"SUM(",
		"GROUP BY",
		"juhe_dataset.",
	} {
		if strings.Contains(strings.ToLower(sql), strings.ToLower(forbidden)) {
			t.Fatalf("W5 management API Key secret SQL must not contain %q", forbidden)
		}
	}
}

func TestFindManagementAPIKeySecretMapsScopedCiphertextAndNull(t *testing.T) {
	encrypted := pgtype.Text{String: "v1:nonce:tag:cipher", Valid: true}
	q := &managementAPIKeySecretQueriesStub{
		findRow: postgresqueries.FindManagementAPIKeySecretRow{
			ID:                 "key_1",
			SystemAccountID:    "sys_owner",
			Name:               "生产 Key",
			KeyPrefix:          "sk-prefix",
			KeySuffix:          "suffix",
			KeySecretEncrypted: encrypted,
		},
	}

	row, found, err := findManagementAPIKeySecret(context.Background(), q, port.ManagementAPIKeySecretScope{
		APIKeyID:        " key_1 ",
		SystemAccountID: " sys_owner ",
	})
	if err != nil {
		t.Fatalf("findManagementAPIKeySecret() error = %v", err)
	}
	if !found ||
		q.findInput.ApiKeyID != "key_1" ||
		q.findInput.SystemAccountID != "sys_owner" ||
		row.KeySecretEncrypted == nil ||
		*row.KeySecretEncrypted != encrypted.String {
		t.Fatalf("row=%+v found=%t input=%+v", row, found, q.findInput)
	}

	q.findRow.KeySecretEncrypted = pgtype.Text{}
	row, found, err = findManagementAPIKeySecret(context.Background(), q, port.ManagementAPIKeySecretScope{
		APIKeyID: "key_1",
	})
	if err != nil || !found || row.KeySecretEncrypted != nil {
		t.Fatalf("null ciphertext row=%+v found=%t err=%v", row, found, err)
	}
}

func TestFindManagementAPIKeySecretReturnsNotFoundForMissingOrWrongOwner(t *testing.T) {
	q := &managementAPIKeySecretQueriesStub{findErr: pgx.ErrNoRows}
	_, found, err := findManagementAPIKeySecret(context.Background(), q, port.ManagementAPIKeySecretScope{
		APIKeyID:        "key_1",
		SystemAccountID: "sys_wrong",
	})
	if err != nil || found {
		t.Fatalf("found=%t err=%v, want not found", found, err)
	}
}

func TestManagementAPIKeySecretRefreshStoreLocksMapsAndUpdatesCredentialOnly(t *testing.T) {
	expiresAt := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 7, 11, 7, 0, 0, 0, time.UTC)
	q := &managementAPIKeySecretQueriesStub{
		lockRow: postgresqueries.LockManagementAPIKeySecretRefreshTargetRow{
			ID:                       "key_1",
			SystemAccountID:          "sys_owner",
			SystemAccountName:        "所有者",
			Name:                     "生产 Key",
			Description:              pgtype.Text{String: "desc", Valid: true},
			KeyPrefix:                "sk-before",
			KeySuffix:                "before",
			Status:                   "active",
			IsDefault:                true,
			RouteStrategyID:          "route_1",
			RouteStrategyName:        "默认策略",
			RouteStrategyMode:        "normal",
			RouteStrategyStatus:      "active",
			ExpiresAt:                pgtype.Timestamptz{Time: expiresAt, Valid: true},
			QuotaLimitsJson:          pgtype.Text{String: `{"daily":{"enabled":true,"limit":1}}`, Valid: true},
			AvailabilityScheduleJson: pgtype.Text{},
		},
		updateID: "key_1",
	}

	row, found, err := lockManagementAPIKeySecretRefreshTarget(
		context.Background(),
		q,
		port.ManagementAPIKeySecretScope{APIKeyID: " key_1 ", SystemAccountID: " sys_owner "},
	)
	if err != nil {
		t.Fatalf("lockManagementAPIKeySecretRefreshTarget() error = %v", err)
	}
	if !found ||
		q.lockInput.ApiKeyID != "key_1" ||
		q.lockInput.SystemAccountID != "sys_owner" ||
		row.ID != "key_1" ||
		row.Description == nil ||
		*row.Description != "desc" ||
		row.ExpiresAt == nil ||
		!row.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("row=%+v found=%t input=%+v", row, found, q.lockInput)
	}

	updated, err := updateManagementAPIKeySecret(context.Background(), q, port.ManagementAPIKeySecretUpdateInput{
		APIKeyID:           "key_1",
		SystemAccountID:    "sys_owner",
		KeyHash:            "hash",
		KeyPrefix:          "sk-after",
		KeySuffix:          "after",
		KeySecretEncrypted: "v1:nonce:tag:cipher",
		UpdatedAt:          updatedAt,
	})
	if err != nil || !updated {
		t.Fatalf("updated=%t err=%v", updated, err)
	}
	if q.updateInput != (postgresqueries.UpdateManagementAPIKeySecretParams{
		KeyHash:            "hash",
		KeyPrefix:          "sk-after",
		KeySuffix:          "after",
		KeySecretEncrypted: "v1:nonce:tag:cipher",
		UpdatedAt:          pgtype.Timestamptz{Time: updatedAt, Valid: true},
		ApiKeyID:           "key_1",
		SystemAccountID:    "sys_owner",
	}) {
		t.Fatalf("update input = %+v", q.updateInput)
	}
}

func TestManagementAPIKeySecretInTxCommitsAndRollsBack(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		tx := &managementAPIKeySecretTxStub{}
		q := &managementAPIKeySecretQueriesStub{}
		calls := []string{}

		err := managementAPIKeySecretInTx(
			context.Background(),
			func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
				calls = append(calls, "begin")
				return tx, nil
			},
			func(got pgx.Tx) port.ManagementAPIKeySecretStore {
				if got != tx {
					t.Fatalf("tx = %T, want stub", got)
				}
				calls = append(calls, "queries")
				return managementAPIKeySecretTxStore{queries: q}
			},
			func(_ context.Context, store port.ManagementAPIKeySecretStore) error {
				calls = append(calls, "callback")
				if _, ok := store.(managementAPIKeySecretTxStore); !ok {
					t.Fatalf("store = %T, want tx store", store)
				}
				return nil
			},
		)
		if err != nil {
			t.Fatalf("managementAPIKeySecretInTx() error = %v", err)
		}
		if !reflect.DeepEqual(calls, []string{"begin", "queries", "callback"}) ||
			tx.commitCalls != 1 ||
			tx.rollbackCalls != 0 {
			t.Fatalf("calls=%v commit=%d rollback=%d", calls, tx.commitCalls, tx.rollbackCalls)
		}
	})

	t.Run("callback failure rolls back", func(t *testing.T) {
		wantErr := errors.New("update failed")
		tx := &managementAPIKeySecretTxStub{}
		err := managementAPIKeySecretInTx(
			context.Background(),
			func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
			func(pgx.Tx) port.ManagementAPIKeySecretStore {
				return managementAPIKeySecretTxStore{queries: &managementAPIKeySecretQueriesStub{}}
			},
			func(context.Context, port.ManagementAPIKeySecretStore) error { return wantErr },
		)
		if !errors.Is(err, wantErr) || tx.commitCalls != 0 || tx.rollbackCalls != 1 {
			t.Fatalf("err=%v commit=%d rollback=%d", err, tx.commitCalls, tx.rollbackCalls)
		}
	})

	t.Run("commit failure rolls back", func(t *testing.T) {
		commitErr := errors.New("commit failed")
		tx := &managementAPIKeySecretTxStub{commitErr: commitErr}
		err := managementAPIKeySecretInTx(
			context.Background(),
			func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
			func(pgx.Tx) port.ManagementAPIKeySecretStore {
				return managementAPIKeySecretTxStore{queries: &managementAPIKeySecretQueriesStub{}}
			},
			func(context.Context, port.ManagementAPIKeySecretStore) error { return nil },
		)
		if !errors.Is(err, commitErr) || tx.commitCalls != 1 || tx.rollbackCalls != 1 {
			t.Fatalf("err=%v commit=%d rollback=%d", err, tx.commitCalls, tx.rollbackCalls)
		}
	})
}

type managementAPIKeySecretQueriesStub struct {
	findInput   postgresqueries.FindManagementAPIKeySecretParams
	lockInput   postgresqueries.LockManagementAPIKeySecretRefreshTargetParams
	updateInput postgresqueries.UpdateManagementAPIKeySecretParams
	findRow     postgresqueries.FindManagementAPIKeySecretRow
	lockRow     postgresqueries.LockManagementAPIKeySecretRefreshTargetRow
	updateID    string
	findErr     error
	lockErr     error
	updateErr   error
}

func (s *managementAPIKeySecretQueriesStub) FindManagementAPIKeySecret(
	_ context.Context,
	arg postgresqueries.FindManagementAPIKeySecretParams,
) (postgresqueries.FindManagementAPIKeySecretRow, error) {
	s.findInput = arg
	return s.findRow, s.findErr
}

func (s *managementAPIKeySecretQueriesStub) LockManagementAPIKeySecretRefreshTarget(
	_ context.Context,
	arg postgresqueries.LockManagementAPIKeySecretRefreshTargetParams,
) (postgresqueries.LockManagementAPIKeySecretRefreshTargetRow, error) {
	s.lockInput = arg
	return s.lockRow, s.lockErr
}

func (s *managementAPIKeySecretQueriesStub) UpdateManagementAPIKeySecret(
	_ context.Context,
	arg postgresqueries.UpdateManagementAPIKeySecretParams,
) (string, error) {
	s.updateInput = arg
	return s.updateID, s.updateErr
}

type managementAPIKeySecretTxStub struct {
	pgx.Tx
	commitErr     error
	rollbackErr   error
	commitCalls   int
	rollbackCalls int
}

func (s *managementAPIKeySecretTxStub) Commit(context.Context) error {
	s.commitCalls++
	return s.commitErr
}

func (s *managementAPIKeySecretTxStub) Rollback(context.Context) error {
	s.rollbackCalls++
	return s.rollbackErr
}

var _ managementAPIKeySecretQueries = (*managementAPIKeySecretQueriesStub)(nil)
var _ pgx.Tx = (*managementAPIKeySecretTxStub)(nil)
