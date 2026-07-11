package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

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
	if q.input != (postgresqueries.UpsertPublicAPIKeyRecordCleanupTargetParams{
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
	input postgresqueries.UpsertPublicAPIKeyRecordCleanupTargetParams
	err   error
}

func (s *publicAPIKeyCleanupTargetQueriesStub) UpsertPublicAPIKeyRecordCleanupTarget(
	_ context.Context,
	input postgresqueries.UpsertPublicAPIKeyRecordCleanupTargetParams,
) error {
	s.input = input
	return s.err
}
