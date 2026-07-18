package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestManagementExternalIntegrationSourceDeleteSQLContract(t *testing.T) {
	raw, err := os.ReadFile("queries/w2_management_external_integration_source_list.sql")
	if err != nil {
		t.Fatalf("read external integration source SQL: %v", err)
	}
	sql := string(raw)
	for _, required := range []string{
		"-- name: FindManagementExternalIntegrationSourceForUpdate :one",
		"FOR UPDATE;",
		"-- name: CountManagementExternalIntegrationSourceTokensForDelete :one",
		"SELECT COUNT(*)::bigint",
		"-- name: DeleteManagementExternalIntegrationSource :one",
		"DELETE FROM juhe_business.external_integration_sources",
		"RETURNING id;",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("external integration source delete SQL missing %q", required)
		}
	}
	deleteStart := strings.Index(sql, "-- name: DeleteManagementExternalIntegrationSource :one")
	updateStart := strings.Index(sql, "-- name: UpdateManagementExternalIntegrationSource :one")
	if deleteStart < 0 || updateStart <= deleteStart {
		t.Fatal("external integration source delete query ordering is invalid")
	}
	deleteSQL := sql[deleteStart:updateStart]
	if strings.Contains(deleteSQL, "external_integration_source_tokens") {
		t.Fatalf("delete query must rely on the schema cascade instead of duplicating token deletion:\n%s", deleteSQL)
	}
}

func TestDeleteManagementExternalIntegrationSourceTx(t *testing.T) {
	queries := &managementExternalIntegrationSourceDeleteQueriesStub{
		current: postgresqueries.JuheBusinessExternalIntegrationSource{
			ID:   "source_1",
			Name: "测试来源",
		},
		tokenCount: 3,
		deletedID:  "source_1",
	}
	result, err := deleteManagementExternalIntegrationSourceTx(context.Background(), queries, "source_1")
	if err != nil {
		t.Fatalf("delete tx: %v", err)
	}
	if result.SourceID != "source_1" || result.SourceName != "测试来源" || result.TokenCount != 3 {
		t.Fatalf("delete result = %#v", result)
	}
	if strings.Join(queries.events, ",") != "lock,count,delete" {
		t.Fatalf("query events = %v", queries.events)
	}
}

func TestDeleteManagementExternalIntegrationSourceTxErrors(t *testing.T) {
	tests := []struct {
		name    string
		queries *managementExternalIntegrationSourceDeleteQueriesStub
		want    error
	}{
		{name: "not found", queries: &managementExternalIntegrationSourceDeleteQueriesStub{lockErr: pgx.ErrNoRows}, want: port.ErrManagementExternalIntegrationSourceNotFound},
		{name: "built in", queries: &managementExternalIntegrationSourceDeleteQueriesStub{current: postgresqueries.JuheBusinessExternalIntegrationSource{ID: publicapi.BuiltInTestSourceID}}, want: port.ErrManagementExternalIntegrationSourceBuiltInDeleteRestricted},
		{name: "count", queries: &managementExternalIntegrationSourceDeleteQueriesStub{current: postgresqueries.JuheBusinessExternalIntegrationSource{ID: "source_1"}, countErr: errors.New("count failed")}, want: errors.New("count management external integration source tokens")},
		{name: "delete missing", queries: &managementExternalIntegrationSourceDeleteQueriesStub{current: postgresqueries.JuheBusinessExternalIntegrationSource{ID: "source_1"}, deleteErr: pgx.ErrNoRows}, want: port.ErrManagementExternalIntegrationSourceNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := deleteManagementExternalIntegrationSourceTx(context.Background(), test.queries, "source_1")
			if test.name == "count" {
				if err == nil || !strings.Contains(err.Error(), test.want.Error()) {
					t.Fatalf("delete tx error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("delete tx error = %v, want %v", err, test.want)
			}
		})
	}
}

type managementExternalIntegrationSourceDeleteQueriesStub struct {
	current    postgresqueries.JuheBusinessExternalIntegrationSource
	tokenCount int64
	deletedID  string
	lockErr    error
	countErr   error
	deleteErr  error
	events     []string
}

func (s *managementExternalIntegrationSourceDeleteQueriesStub) FindManagementExternalIntegrationSourceForUpdate(
	context.Context,
	string,
) (postgresqueries.JuheBusinessExternalIntegrationSource, error) {
	s.events = append(s.events, "lock")
	return s.current, s.lockErr
}

func (s *managementExternalIntegrationSourceDeleteQueriesStub) CountManagementExternalIntegrationSourceTokensForDelete(
	context.Context,
	string,
) (int64, error) {
	s.events = append(s.events, "count")
	return s.tokenCount, s.countErr
}

func (s *managementExternalIntegrationSourceDeleteQueriesStub) DeleteManagementExternalIntegrationSource(
	context.Context,
	string,
) (string, error) {
	s.events = append(s.events, "delete")
	return s.deletedID, s.deleteErr
}
