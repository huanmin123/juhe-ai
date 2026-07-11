package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestAPIKeyCleanupTargetSQLCNameIsSharedAcrossPostgresAdapters(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		required  []string
		forbidden []string
	}{
		{
			name: "query",
			path: "queries/w1b_public_api_keys.sql",
			required: []string{
				"-- name: UpsertAPIKeyRecordCleanupTarget :exec",
			},
			forbidden: []string{
				"UpsertPublicAPIKeyRecordCleanupTarget",
			},
		},
		{
			name: "generated query",
			path: "postgresqueries/w1b_public_api_keys.sql.go",
			required: []string{
				"type UpsertAPIKeyRecordCleanupTargetParams struct",
				"func (q *Queries) UpsertAPIKeyRecordCleanupTarget(",
			},
			forbidden: []string{
				"UpsertPublicAPIKeyRecordCleanupTarget",
			},
		},
		{
			name: "public adapter",
			path: "publicapikeys.go",
			required: []string{
				"type publicAPIKeyCleanupTargetQueries interface {\n\tUpsertAPIKeyRecordCleanupTarget(",
				"postgresqueries.UpsertAPIKeyRecordCleanupTargetParams",
				"q.UpsertAPIKeyRecordCleanupTarget(",
			},
			forbidden: []string{
				"postgresqueries.UpsertPublicAPIKeyRecordCleanupTargetParams",
				"q.UpsertPublicAPIKeyRecordCleanupTarget(",
			},
		},
		{
			name: "management delete adapter",
			path: "managementapikeydelete.go",
			required: []string{
				"UpsertAPIKeyRecordCleanupTarget(",
				"postgresqueries.UpsertAPIKeyRecordCleanupTargetParams",
			},
			forbidden: []string{
				"UpsertPublicAPIKeyRecordCleanupTarget",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}
			text := string(source)
			for _, required := range tt.required {
				if !strings.Contains(text, required) {
					t.Fatalf("%s missing %q", tt.path, required)
				}
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(text, forbidden) {
					t.Fatalf("%s must not contain %q", tt.path, forbidden)
				}
			}
		})
	}
}

func TestPublicAPIKeyUpsertCleanupTargetMapsOwnerAndUTCMutationTime(t *testing.T) {
	now := time.Date(2026, 7, 12, 2, 3, 4, 0, time.UTC)
	q := &publicAPIKeyCleanupTargetQueriesStub{}

	err := publicAPIKeyUpsertCleanupTarget(context.Background(), q, port.PublicAPIKeyRecordCleanupTargetInput{
		APIKeyID:        "key_1",
		SystemAccountID: "sys_owner",
		Now:             now,
	})
	if err != nil {
		t.Fatalf("publicAPIKeyUpsertCleanupTarget() error = %v", err)
	}
	if q.input != (postgresqueries.UpsertAPIKeyRecordCleanupTargetParams{
		ApiKeyID:        "key_1",
		SystemAccountID: "sys_owner",
		CreatedAt:       pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:       pgtype.Timestamptz{Time: now, Valid: true},
	}) {
		t.Fatalf("query input = %+v", q.input)
	}
}

func TestPublicAPIKeyUpsertCleanupTargetWrapsQueryError(t *testing.T) {
	wantErr := errors.New("dataset write failed")
	q := &publicAPIKeyCleanupTargetQueriesStub{err: wantErr}

	err := publicAPIKeyUpsertCleanupTarget(context.Background(), q, port.PublicAPIKeyRecordCleanupTargetInput{})

	if !errors.Is(err, wantErr) {
		t.Fatalf("publicAPIKeyUpsertCleanupTarget() error = %v, want %v", err, wantErr)
	}
}

type publicAPIKeyCleanupTargetQueriesStub struct {
	input postgresqueries.UpsertAPIKeyRecordCleanupTargetParams
	err   error
}

func (s *publicAPIKeyCleanupTargetQueriesStub) UpsertAPIKeyRecordCleanupTarget(
	_ context.Context,
	input postgresqueries.UpsertAPIKeyRecordCleanupTargetParams,
) error {
	s.input = input
	return s.err
}
