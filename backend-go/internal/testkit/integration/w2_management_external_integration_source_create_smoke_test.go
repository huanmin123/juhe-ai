//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	"juhe-ai/backend-go/internal/secretcrypto"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w2ExternalSourceCreateAdminID        = "sys_w2_external_source_create_admin"
	w2ExternalSourceCreateAdminSessionID = "sess_w2_external_source_create_admin"
	w2ExternalSourceCreateAdminToken     = "w2-external-source-create-admin-session-token"
	w2ExternalSourceCreateSecret         = "w2-external-source-create-secret-key"
	w2ExternalSourceCreateName           = "W2 External Source Create"
	w2ExternalSourceCreateConflictName   = "W2 External Source Create Conflict"
	w2ExternalSourceCreateNotes          = "W2 external source private notes"
)

func TestW2ManagementExternalIntegrationSourceCreatePostgresSmoke(t *testing.T) {
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

	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	insertW2ExternalSourceCreateAdminFixture(t, ctx, db, now)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanupW2ExternalSourceCreateFixtures(t, cleanupCtx, db)
	}()

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	service := managementexternalintegrationsources.NewCreateServiceWithOptions(
		managementexternalintegrationsources.CreateServiceOptions{
			Store:  store,
			Secret: w2ExternalSourceCreateSecret,
			Now:    func() time.Time { return now },
		},
	)
	server := httptest.NewServer(w2ExternalSourceCreateRouter(authenticator, service))
	defer server.Close()
	client := server.Client()
	client.Timeout = 15 * time.Second

	status, cacheControl, pragma, response := requestW2ExternalSourceCreate(
		t,
		ctx,
		client,
		server.URL,
		w2ExternalSourceCreateRequestBody(w2ExternalSourceCreateName, w2ExternalSourceCreateNotes),
	)
	if status != http.StatusCreated || cacheControl != "no-store" || pragma != "no-cache" {
		t.Fatalf("create response status=%d Cache-Control=%q Pragma=%q", status, cacheControl, pragma)
	}
	assertW2ExternalSourceCreateResponse(t, response)
	tokenHash, tokenCipher := assertW2ExternalSourceCreateStored(t, ctx, db, response)
	assertW2ExternalSourceCreatePublicLogsEmpty(t, ctx, db)

	var generatedTokens int
	conflictService := managementexternalintegrationsources.NewCreateServiceWithOptions(
		managementexternalintegrationsources.CreateServiceOptions{
			Store:  store,
			Secret: w2ExternalSourceCreateSecret,
			Now:    func() time.Time { return now.Add(time.Minute) },
			NewID:  func(prefix string) string { return prefix + "_w2_create_conflict" },
			NewToken: func() (string, error) {
				generatedTokens++
				return response.Data.Token.Token, nil
			},
		},
	)
	conflictServer := httptest.NewServer(w2ExternalSourceCreateRouter(authenticator, conflictService))
	defer conflictServer.Close()
	conflictClient := conflictServer.Client()
	conflictClient.Timeout = 15 * time.Second

	beforeSources, beforeTokens := w2ExternalSourceCreateCounts(t, ctx, db)
	conflictStatus, _, _, conflictResponse := requestW2ExternalSourceCreate(
		t,
		ctx,
		conflictClient,
		conflictServer.URL,
		w2ExternalSourceCreateRequestBody(w2ExternalSourceCreateConflictName, "conflict notes"),
	)
	if conflictStatus != http.StatusBadRequest ||
		conflictResponse.Message != managementexternalintegrationsources.ErrTokenExists.Error() ||
		generatedTokens != 3 {
		t.Fatalf("conflict response status=%d messageMatches=%t attempts=%d", conflictStatus, conflictResponse.Message == managementexternalintegrationsources.ErrTokenExists.Error(), generatedTokens)
	}
	afterSources, afterTokens := w2ExternalSourceCreateCounts(t, ctx, db)
	if afterSources != beforeSources || afterTokens != beforeTokens {
		t.Fatalf("token conflict left rows: sources %d->%d tokens %d->%d", beforeSources, afterSources, beforeTokens, afterTokens)
	}
	var conflictSources, conflictTokens int
	if err := db.QueryRowContext(ctx, `
		SELECT
			(SELECT count(*) FROM juhe_business.external_integration_sources WHERE name = $1),
			(SELECT count(*) FROM juhe_business.external_integration_source_tokens WHERE id = $2)
	`, w2ExternalSourceCreateConflictName, "exttok_w2_create_conflict").Scan(&conflictSources, &conflictTokens); err != nil {
		t.Fatalf("count conflict create rows: %v", err)
	}
	if conflictSources != 0 || conflictTokens != 0 {
		t.Fatalf("token conflict orphan rows: sources=%d tokens=%d", conflictSources, conflictTokens)
	}
	if tokenHash == "" || tokenCipher == "" {
		t.Fatal("stored token hash or ciphertext became empty")
	}
}

type w2ExternalSourceCreateHTTPResponse struct {
	Data    managementexternalintegrationsources.CreateResult `json:"data"`
	Message string                                            `json:"message"`
}

func w2ExternalSourceCreateRouter(
	authenticator *managementauth.Authenticator,
	service *managementexternalintegrationsources.CreateService,
) http.Handler {
	return httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
			TrustProxy:           "false",
		},
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementExternalIntegrationSourceCreateHandler: httpapi.NewManagementExternalIntegrationSourceCreateHandlerWithOperationLog(
			service,
			httpapi.ManagementOperationLogOptions{},
		),
	})
}

func w2ExternalSourceCreateRequestBody(name string, notes string) []byte {
	body, err := json.Marshal(map[string]any{
		"name":       name,
		"status":     publicapi.SourceStatusDisabled,
		"scopes":     []string{publicapi.ScopeGroupListRead, publicapi.ScopeAPIKeyListRead},
		"rateLimits": []map[string]int{{"windowSeconds": 60, "maxRequests": 12}},
		"expiresAt":  "2026-08-01T00:00:00.000Z",
		"notes":      notes,
	})
	if err != nil {
		panic(err)
	}
	return body
}

func requestW2ExternalSourceCreate(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	baseURL string,
	body []byte,
) (int, string, string, w2ExternalSourceCreateHTTPResponse) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/__aisys__/api/external-integration-sources", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build external source create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "juhe_ai_session="+w2ExternalSourceCreateAdminToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("external source create request: %v", err)
	}
	defer resp.Body.Close()
	var decoded w2ExternalSourceCreateHTTPResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode external source create response: %v", err)
	}
	return resp.StatusCode, resp.Header.Get("Cache-Control"), resp.Header.Get("Pragma"), decoded
}

func assertW2ExternalSourceCreateResponse(t *testing.T, response w2ExternalSourceCreateHTTPResponse) {
	t.Helper()
	source := response.Data.Source
	token := response.Data.Token
	if source.ID == "" || token.ID == "" || source.ID == token.ID || source.Name != w2ExternalSourceCreateName ||
		source.Status != publicapi.SourceStatusDisabled || source.TokenCount != 1 || source.ActiveTokenCount != 0 ||
		source.PrimaryToken == nil || source.PrimaryToken.ID != token.ID || token.Name != w2ExternalSourceCreateName+" 生产 Token" ||
		token.TokenPrefix == "" || token.TokenSuffix == "" || token.ExpiresAt == nil || source.ExpiresAt == nil ||
		source.Notes == nil || *source.Notes != w2ExternalSourceCreateNotes {
		t.Fatalf("created source/token response fields are incomplete or inconsistent")
	}
	if !strings.HasPrefix(token.Token, "juis_") || len(token.Token) != 48 || strings.ContainsAny(token.Token, "+/=") ||
		token.TokenPrefix != token.Token[:8] || token.TokenSuffix != token.Token[len(token.Token)-8:] {
		t.Fatal("created plaintext token format or preview is invalid")
	}
}

func assertW2ExternalSourceCreateStored(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	response w2ExternalSourceCreateHTTPResponse,
) (string, string) {
	t.Helper()
	var sourceID, sourceName, sourceStatus, scopesJSON, rateLimitsJSON, notes string
	var expiresAt time.Time
	if err := db.QueryRowContext(ctx, `
		SELECT id, name, status, scopes_json, rate_limits_json, expires_at, notes
		FROM juhe_business.external_integration_sources
		WHERE id = $1
	`, response.Data.Source.ID).Scan(&sourceID, &sourceName, &sourceStatus, &scopesJSON, &rateLimitsJSON, &expiresAt, &notes); err != nil {
		t.Fatalf("read created external source: %v", err)
	}
	if sourceID != response.Data.Source.ID || sourceName != w2ExternalSourceCreateName || sourceStatus != publicapi.SourceStatusDisabled ||
		scopesJSON != `["juhe_ai_public:api_key_list:read","juhe_ai_public:group_list:read"]` ||
		rateLimitsJSON != `[{"windowSeconds":60,"maxRequests":12}]` || notes != w2ExternalSourceCreateNotes ||
		!expiresAt.UTC().Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("stored external source fields do not match the normalized request")
	}

	var tokenID, tokenSourceID, tokenHash, tokenCipher, tokenStatus string
	var tokenCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*), min(id), min(source_ref_id), min(token_hash), min(token_secret_encrypted), min(status)
		FROM juhe_business.external_integration_source_tokens
		WHERE source_ref_id = $1
	`, sourceID).Scan(&tokenCount, &tokenID, &tokenSourceID, &tokenHash, &tokenCipher, &tokenStatus); err != nil {
		t.Fatalf("read created external source token: %v", err)
	}
	if tokenCount != 1 || tokenID != response.Data.Token.ID || tokenSourceID != sourceID || tokenStatus != publicapi.TokenStatusDisabled ||
		tokenHash == "" || tokenCipher == "" || tokenHash != publicapiauth.HashExternalSourceToken(response.Data.Token.Token) {
		t.Fatal("stored external source token fields are incomplete or inconsistent")
	}
	payload, err := secretcrypto.NewJSONCodec(w2ExternalSourceCreateSecret).DecryptJSON(tokenCipher)
	if err != nil || len(payload) != 1 || payload["token"] != response.Data.Token.Token {
		t.Fatalf("stored external source token decrypts correctly=%t", err == nil)
	}
	return tokenHash, tokenCipher
}

func assertW2ExternalSourceCreatePublicLogsEmpty(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.public_api_logs`).Scan(&count); err != nil {
		t.Fatalf("count public API logs after management create: %v", err)
	}
	if count != 0 {
		t.Fatalf("management create unexpectedly wrote %d public API logs", count)
	}
}

func w2ExternalSourceCreateCounts(t *testing.T, ctx context.Context, db *sql.DB) (int, int) {
	t.Helper()
	var sources, tokens int
	if err := db.QueryRowContext(ctx, `
		SELECT
			(SELECT count(*) FROM juhe_business.external_integration_sources),
			(SELECT count(*) FROM juhe_business.external_integration_source_tokens)
	`).Scan(&sources, &tokens); err != nil {
		t.Fatalf("count external source create rows: %v", err)
	}
	return sources, tokens
}

func insertW2ExternalSourceCreateAdminFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			$1, 'w2-external-source-create-admin', 'W2 External Source Create Admin', NULL,
			'admin', 'active', 'hash', false, false, $2, $2
		)
	`, w2ExternalSourceCreateAdminID, now); err != nil {
		t.Fatalf("insert external source create admin: %v", err)
	}
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w2ExternalSourceCreateAdminSessionID,
		w2ExternalSourceCreateAdminID,
		w2ExternalSourceCreateAdminToken,
		now,
	)
}

func cleanupW2ExternalSourceCreateFixtures(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		DELETE FROM juhe_business.external_integration_sources
		WHERE name IN ($1, $2)
	`, w2ExternalSourceCreateName, w2ExternalSourceCreateConflictName); err != nil {
		t.Errorf("cleanup external source create rows: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM juhe_business.system_accounts
		WHERE id = $1
	`, w2ExternalSourceCreateAdminID); err != nil {
		t.Errorf("cleanup external source create admin: %v", err)
	}
}
