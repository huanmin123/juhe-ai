//go:build integration

package integration

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	publicapilogjob "juhe-ai/backend-go/internal/jobs/publicapilog"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW1bPublicAPIFoundationPostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateContainer(t, ctx, container)

	postgresURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	if err := goose.Up(db, filepath.Join(repoRoot(t), "db", "migrations")); err != nil {
		t.Fatalf("goose up: %v", err)
	}

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	token := "juis_w1b_foundation_token"
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	insertW1bSourceAndToken(t, ctx, db, token, now)

	record, found, err := store.FindPublicAPIAuthTokenByHash(ctx, publicapiauth.HashExternalSourceToken(token))
	if err != nil {
		t.Fatalf("find public api auth token: %v", err)
	}
	if !found {
		t.Fatal("public api auth token not found")
	}
	if record.SourceRefID != "extsrc_w1b_smoke" || record.TokenID != "exttok_w1b_smoke" {
		t.Fatalf("auth record = %+v", record)
	}
	if len(record.SourceRateLimits) != 1 || record.SourceRateLimits[0].WindowSeconds != 60 || record.SourceRateLimits[0].MaxRequests != 10 {
		t.Fatalf("rate limits = %+v", record.SourceRateLimits)
	}

	touchedAt := now.Add(time.Minute)
	if err := store.TouchPublicAPIAuthLastUsed(ctx, port.PublicAPIAuthLastUsedTouch{
		SourceRefID: "extsrc_w1b_smoke",
		TokenID:     "exttok_w1b_smoke",
		Now:         touchedAt,
		TouchSource: true,
		TouchToken:  true,
	}); err != nil {
		t.Fatalf("touch public api auth last used: %v", err)
	}
	assertTimestampEquals(t, db, "SELECT last_used_at FROM juhe_business.external_integration_sources WHERE id = $1", "extsrc_w1b_smoke", touchedAt)
	assertTimestampEquals(t, db, "SELECT last_used_at FROM juhe_business.external_integration_source_tokens WHERE id = $1", "exttok_w1b_smoke", touchedAt)

	statusCode := 200
	durationMs := int64(12)
	publicLogInput := port.PublicAPILogInput{
		ID:                    "publog_w1b_smoke",
		TraceID:               "trace_w1b_smoke",
		SourceRefID:           "extsrc_w1b_smoke",
		SourceName:            "W1b Smoke Source",
		TokenID:               "exttok_w1b_smoke",
		TokenName:             "W1b Smoke Token",
		TokenPrefix:           "juis_w1b",
		Method:                "GET",
		Path:                  "/__aipublic__/group/list",
		StatusCode:            &statusCode,
		Success:               true,
		DurationMs:            &durationMs,
		RequestSizeBytes:      20,
		ResponseSizeBytes:     30,
		RequestCaptureStatus:  port.PublicAPILogCaptureComplete,
		ResponseCaptureStatus: port.PublicAPILogCaptureComplete,
		RequestData:           map[string]any{"query": map[string]any{"targetUsername": "admin"}},
		ResponseData:          map[string]any{"body": map[string]any{"items": []any{}}},
		StartedAt:             now,
		EndedAt:               now.Add(time.Duration(durationMs) * time.Millisecond),
	}
	publicLogPayload, err := publicapilogjob.EncodeWriteTaskPayload(publicLogInput)
	if err != nil {
		t.Fatalf("encode public api log payload: %v", err)
	}
	if err := publicapilogjob.HandleWriteTask(ctx, store, publicLogPayload); err != nil {
		t.Fatalf("handle public api log write task: %v", err)
	}
	if err := publicapilogjob.HandleWriteTask(ctx, store, publicLogPayload); err != nil {
		t.Fatalf("handle duplicate public api log write task: %v", err)
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM juhe_dataset.public_api_logs WHERE id = $1", "publog_w1b_smoke").Scan(&count); err != nil {
		t.Fatalf("count public api log: %v", err)
	}
	if count != 1 {
		t.Fatalf("public api log count = %d, want 1", count)
	}
}

func insertW1bSourceAndToken(t *testing.T, ctx context.Context, db *sql.DB, token string, now time.Time) {
	t.Helper()

	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_sources (
			id, name, status, scopes_json, rate_limits_json, created_at, updated_at
		) VALUES ($1, $2, 'active', $3, $4, $5, $6)
	`, "extsrc_w1b_smoke", "W1b Smoke Source", `["juhe_ai_public:group_list:read"]`, `[{"windowSeconds":60,"maxRequests":10}]`, now, now)
	if err != nil {
		t.Fatalf("insert external integration source: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_source_tokens (
			id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, $9, $10)
	`, "exttok_w1b_smoke", "extsrc_w1b_smoke", "W1b Smoke Token", publicapiauth.HashExternalSourceToken(token), "encrypted", "juis_w1b", "n_token", `["juhe_ai_public:group_list:read"]`, now, now)
	if err != nil {
		t.Fatalf("insert external integration source token: %v", err)
	}
}

func assertTimestampEquals(t *testing.T, db *sql.DB, query string, id string, want time.Time) {
	t.Helper()

	var got time.Time
	if err := db.QueryRow(query, id).Scan(&got); err != nil {
		t.Fatalf("query timestamp: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("timestamp = %v, want %v", got, want)
	}
}
