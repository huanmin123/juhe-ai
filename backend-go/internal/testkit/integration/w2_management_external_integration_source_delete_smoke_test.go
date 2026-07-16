//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w2ExternalSourceDeleteID             = "extsrc_w2_delete"
	w2ExternalSourceDeleteTokenOneID     = "exttok_w2_delete_1"
	w2ExternalSourceDeleteTokenTwoID     = "exttok_w2_delete_2"
	w2ExternalSourceDeletePublicLogID    = "publog_w2_external_source_delete"
	w2ExternalSourceDeleteAdminID        = "sys_w2_external_source_delete_admin"
	w2ExternalSourceDeleteAdminSessionID = "sess_w2_external_source_delete_admin"
	w2ExternalSourceDeleteAdminToken     = "w2-external-source-delete-admin-session-token"
)

func TestW2ManagementExternalIntegrationSourceDeletePostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		terminateContainer(t, cleanupCtx, container)
	}()

	postgresURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)

	now := time.Date(2026, 7, 16, 2, 0, 0, 0, time.UTC)
	insertW2ExternalSourceDeleteFixtures(t, ctx, db, now)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanupW2ExternalSourceDeleteFixtures(t, cleanupCtx, db)
	}()

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()
	service := managementexternalintegrationsources.NewDeleteService(store)
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
			TrustProxy:           "false",
		},
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementExternalIntegrationSourceDeleteHandler: httpapi.NewManagementExternalIntegrationSourceDeleteHandlerWithOperationLog(
			service,
			httpapi.ManagementOperationLogOptions{},
		),
	})
	server := httptest.NewServer(router)
	defer server.Close()
	client := server.Client()
	client.Timeout = 15 * time.Second
	deleteRequestCtx, cancelDeleteRequest := context.WithCancel(ctx)
	defer cancelDeleteRequest()

	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin external source delete blocker: %v", err)
	}
	blockerReleased := false
	releaseBlocker := func() error {
		if blockerReleased {
			return nil
		}
		blockerReleased = true
		return blocker.Rollback()
	}
	defer func() {
		_ = releaseBlocker()
	}()
	if err := blocker.QueryRowContext(ctx, `
		SELECT id
		FROM juhe_business.external_integration_sources
		WHERE id = $1
		FOR UPDATE
	`, w2ExternalSourceDeleteID).Scan(new(string)); err != nil {
		t.Fatalf("lock external source delete fixture: %v", err)
	}

	type deleteHTTPOutcome struct {
		status       int
		cacheControl string
		body         []byte
		err          error
	}
	deleteDone := make(chan deleteHTTPOutcome, 1)
	go func() {
		status, cacheControl, body, err := requestW2ExternalSourceDelete(
			deleteRequestCtx,
			client,
			server.URL,
			w2ExternalSourceDeleteID,
		)
		deleteDone <- deleteHTTPOutcome{
			status:       status,
			cacheControl: cacheControl,
			body:         body,
			err:          err,
		}
	}()

	assertW2ExternalSourceDeleteWaitingOnRowLock(t, ctx, db)
	select {
	case outcome := <-deleteDone:
		t.Fatalf("external source delete completed before row lock release: %+v", outcome)
	default:
	}
	if err := releaseBlocker(); err != nil {
		t.Fatalf("release external source delete blocker: %v", err)
	}

	var outcome deleteHTTPOutcome
	select {
	case outcome = <-deleteDone:
	case <-time.After(10 * time.Second):
		cancelDeleteRequest()
		_ = releaseBlocker()
		select {
		case <-deleteDone:
		case <-time.After(5 * time.Second):
			server.CloseClientConnections()
		}
		t.Fatal("external source delete did not complete after row lock release")
	}
	if outcome.err != nil {
		t.Fatalf("external source delete request: %v", outcome.err)
	}
	if outcome.status != http.StatusNoContent || len(outcome.body) != 0 || outcome.cacheControl != "no-store" {
		t.Fatalf("external source delete status=%d Cache-Control=%q body=%q", outcome.status, outcome.cacheControl, outcome.body)
	}
	assertW2ExternalSourceDeleteCascadeAndLogPreservation(t, ctx, db)

	_, err = service.Delete(ctx, managementexternalintegrationsources.DeleteInput{SourceID: w2ExternalSourceDeleteID})
	if !errors.Is(err, managementexternalintegrationsources.ErrNotFound) {
		t.Fatalf("repeat external source delete error = %v", err)
	}

	builtInStatus, _, builtInBody, err := requestW2ExternalSourceDelete(
		ctx,
		client,
		server.URL,
		publicapi.BuiltInTestSourceID,
	)
	if err != nil {
		t.Fatalf("built-in external source delete request: %v", err)
	}
	if builtInStatus != http.StatusBadRequest {
		t.Fatalf("built-in external source delete status=%d body=%s", builtInStatus, builtInBody)
	}
	var builtInResponse map[string]any
	if err := json.Unmarshal(builtInBody, &builtInResponse); err != nil {
		t.Fatalf("decode built-in external source delete response: %v", err)
	}
	if len(builtInResponse) != 1 || builtInResponse["message"] != managementexternalintegrationsources.ErrBuiltInDeleteRestricted.Error() {
		t.Fatalf("built-in external source delete response=%s", builtInBody)
	}
	if _, err := store.DeleteManagementExternalIntegrationSource(ctx, publicapi.BuiltInTestSourceID); !errors.Is(err, port.ErrManagementExternalIntegrationSourceBuiltInDeleteRestricted) {
		t.Fatalf("store built-in external source delete error = %v", err)
	}
	var builtInSourceCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM juhe_business.external_integration_sources
		WHERE id = $1
	`, publicapi.BuiltInTestSourceID).Scan(&builtInSourceCount); err != nil {
		t.Fatalf("count built-in external source after rejects: %v", err)
	}
	var builtInTokenCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM juhe_business.external_integration_source_tokens
		WHERE source_ref_id = $1
	`, publicapi.BuiltInTestSourceID).Scan(&builtInTokenCount); err != nil {
		t.Fatalf("count built-in external source tokens after rejects: %v", err)
	}
	if builtInSourceCount != 1 || builtInTokenCount != 1 {
		t.Fatalf("built-in external source after rejects sourceCount=%d tokenCount=%d", builtInSourceCount, builtInTokenCount)
	}
}

func requestW2ExternalSourceDelete(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	sourceID string,
) (int, string, []byte, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodDelete,
		baseURL+"/__aisys__/api/external-integration-sources/"+sourceID,
		nil,
	)
	if err != nil {
		return 0, "", nil, err
	}
	req.Header.Set("Cookie", "juhe_ai_session="+w2ExternalSourceDeleteAdminToken)
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", nil, err
	}
	return resp.StatusCode, resp.Header.Get("Cache-Control"), body, nil
}

func insertW2ExternalSourceDeleteFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			$1, 'w2-external-source-delete-admin', 'W2 External Source Delete Admin', NULL,
			'admin', 'active', 'hash', false, false, $2, $2
		)
	`, w2ExternalSourceDeleteAdminID, now); err != nil {
		t.Fatalf("insert external source delete admin: %v", err)
	}
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w2ExternalSourceDeleteAdminSessionID,
		w2ExternalSourceDeleteAdminID,
		w2ExternalSourceDeleteAdminToken,
		now,
	)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_sources (
			id, name, status, scopes_json, rate_limits_json, notes, created_at, updated_at
		) VALUES
			($1, 'W2 External Source Delete', 'active', '[]', '[]', 'delete fixture', $3, $3),
			($2, 'W2 Built-in Source Delete Guard', 'active', '[]', '[]', 'built-in fixture', $3, $3)
	`, w2ExternalSourceDeleteID, publicapi.BuiltInTestSourceID, now); err != nil {
		t.Fatalf("insert external source delete fixtures: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_source_tokens (
			id, source_ref_id, name, token_hash, token_secret_encrypted,
			token_prefix, token_suffix, status, scopes_json, created_at, updated_at
		) VALUES
			($1, $3, 'W2 Delete Token One', 'w2-delete-hash-1', 'w2-delete-secret-1', 'juis_w2_delete_1', 'delete01', 'active', '[]', $5, $5),
			($2, $3, 'W2 Delete Token Two', 'w2-delete-hash-2', 'w2-delete-secret-2', 'juis_w2_delete_2', 'delete02', 'disabled', '[]', $5, $5),
			($4, $6, 'W2 Built-in Delete Token', 'w2-built-in-delete-hash', 'w2-built-in-delete-secret', 'juis_w2_builtin', 'built001', 'active', '[]', $5, $5)
	`,
		w2ExternalSourceDeleteTokenOneID,
		w2ExternalSourceDeleteTokenTwoID,
		w2ExternalSourceDeleteID,
		"exttok_w2_builtin_delete",
		now,
		publicapi.BuiltInTestSourceID,
	); err != nil {
		t.Fatalf("insert external source delete token fixtures: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_dataset.public_api_logs (
			id, trace_id, source_ref_id, source_name, token_id, token_name, token_prefix,
			is_test_token, method, path, status_code, success, duration_ms,
			request_capture_status, response_capture_status,
			request_data_json, response_data_json, started_at, ended_at, created_at
		) VALUES (
			$1, 'trace_w2_external_source_delete', $2, 'W2 External Source Delete',
			$3, 'W2 Delete Token One', 'juis_w2_delete_1', false,
			'GET', '/__aipublic__/group/list', 200, true, 7,
			'empty', 'empty', '{}', '{}', $4, $4, $4
		)
	`, w2ExternalSourceDeletePublicLogID, w2ExternalSourceDeleteID, w2ExternalSourceDeleteTokenOneID, now); err != nil {
		t.Fatalf("insert external source delete public log fixture: %v", err)
	}
}

func assertW2ExternalSourceDeleteWaitingOnRowLock(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := db.QueryRowContext(waitCtx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE datname = current_database()
				  AND pid <> pg_backend_pid()
				  AND state = 'active'
				  AND wait_event_type = 'Lock'
				  AND cardinality(pg_blocking_pids(pid)) > 0
				  AND query LIKE '-- name: FindManagementExternalIntegrationSourceForUpdate :one%'
			)
		`).Scan(&waiting)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				t.Fatal("external source delete never entered a PostgreSQL row lock wait")
			}
			t.Fatalf("inspect external source delete row lock wait: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ticker.C:
		case <-waitCtx.Done():
			t.Fatal("external source delete never entered a PostgreSQL row lock wait")
		}
	}
}

func assertW2ExternalSourceDeleteCascadeAndLogPreservation(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) {
	t.Helper()
	var sourceCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM juhe_business.external_integration_sources
		WHERE id = $1
	`, w2ExternalSourceDeleteID).Scan(&sourceCount); err != nil {
		t.Fatalf("count deleted external source: %v", err)
	}
	var tokenCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM juhe_business.external_integration_source_tokens
		WHERE source_ref_id = $1
	`, w2ExternalSourceDeleteID).Scan(&tokenCount); err != nil {
		t.Fatalf("count cascaded external source tokens: %v", err)
	}
	if sourceCount != 0 || tokenCount != 0 {
		t.Fatalf("external source delete cascade sourceCount=%d tokenCount=%d", sourceCount, tokenCount)
	}

	var sourceRefID, sourceName, tokenID, tokenName string
	if err := db.QueryRowContext(ctx, `
		SELECT source_ref_id, source_name, token_id, token_name
		FROM juhe_dataset.public_api_logs
		WHERE id = $1
	`, w2ExternalSourceDeletePublicLogID).Scan(&sourceRefID, &sourceName, &tokenID, &tokenName); err != nil {
		t.Fatalf("read public API log after external source delete: %v", err)
	}
	if sourceRefID != w2ExternalSourceDeleteID ||
		sourceName != "W2 External Source Delete" ||
		tokenID != w2ExternalSourceDeleteTokenOneID ||
		tokenName != "W2 Delete Token One" {
		t.Fatalf("public API log snapshot after external source delete = %q/%q/%q/%q", sourceRefID, sourceName, tokenID, tokenName)
	}
}

func cleanupW2ExternalSourceDeleteFixtures(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		DELETE FROM juhe_dataset.public_api_logs
		WHERE id = $1
	`, w2ExternalSourceDeletePublicLogID); err != nil {
		t.Errorf("cleanup external source delete public log: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM juhe_business.external_integration_sources
		WHERE id IN ($1, $2)
	`, w2ExternalSourceDeleteID, publicapi.BuiltInTestSourceID); err != nil {
		t.Errorf("cleanup external source delete fixtures: %v", err)
	}
}
