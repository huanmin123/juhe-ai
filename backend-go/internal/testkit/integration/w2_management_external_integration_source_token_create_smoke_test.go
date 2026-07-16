//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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
	w2ExternalSourceTokenCreateAdminID           = "sys_w2_external_source_token_create_admin"
	w2ExternalSourceTokenCreateAdminSessionID    = "sess_w2_external_source_token_create_admin"
	w2ExternalSourceTokenCreateAdminSessionToken = "w2-external-source-token-create-admin-session-token"
	w2ExternalSourceTokenCreateSecret            = "w2-external-source-token-create-secret"
	w2ExternalSourceTokenCreateSourceID          = "extsrc_w2_token_create"
	w2ExternalSourceTokenCreateExistingTokenID   = "exttok_w2_token_create_existing"
	w2ExternalSourceTokenCreateNewTokenID        = "exttok_w2_token_create_new"
	w2ExternalSourceTokenCreateCollisionSourceID = "extsrc_w2_token_create_collision"
	w2ExternalSourceTokenCreateHolderSourceID    = "extsrc_w2_token_create_holders"
	w2ExternalSourceTokenCreateName              = "W2 External Source Token Create"
	w2ExternalSourceTokenCreateNewName           = "W2 新增生产 Token"
)

func TestW2ManagementExternalIntegrationSourceTokenCreatePostgresSmoke(t *testing.T) {
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

	now := time.Date(2026, 7, 16, 9, 10, 11, 123_000_000, time.UTC)
	fixture := insertW2ExternalSourceTokenCreateFixtures(t, ctx, db, now)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		cleanupW2ExternalSourceTokenCreateFixtures(t, cleanupCtx, db)
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

	plainToken := w2ExternalSourceTokenCreateToken('s')
	service := managementexternalintegrationsources.NewTokenCreateServiceWithOptions(
		managementexternalintegrationsources.TokenCreateServiceOptions{
			Store:    store,
			Secret:   w2ExternalSourceTokenCreateSecret,
			Now:      func() time.Time { return now },
			NewID:    func(prefix string) string { return w2ExternalSourceTokenCreateNewTokenID },
			NewToken: func() (string, error) { return plainToken, nil },
		},
	)
	server := httptest.NewServer(w2ExternalSourceTokenCreateRouter(authenticator, service))
	defer server.Close()
	client := server.Client()
	client.Timeout = 15 * time.Second

	sourceBefore := readW2ExternalSourceTokenCreateSourceSnapshot(t, ctx, db, w2ExternalSourceTokenCreateSourceID)
	existingTokenBefore := readW2ExternalSourceTokenCreateTokenSnapshot(t, ctx, db, w2ExternalSourceTokenCreateExistingTokenID)
	success := requestW2ExternalSourceTokenCreate(
		t,
		ctx,
		client,
		server.URL,
		w2ExternalSourceTokenCreateSourceID,
		w2ExternalSourceTokenCreateBody(w2ExternalSourceTokenCreateNewName),
	)
	if success.status != http.StatusCreated || success.cacheControl != "no-store" || success.pragma != "no-cache" {
		t.Fatalf("token create response status=%d Cache-Control=%q Pragma=%q", success.status, success.cacheControl, success.pragma)
	}
	var createdResponse struct {
		Data managementexternalintegrationsources.TokenCreateResult `json:"data"`
	}
	if err := json.Unmarshal(success.body, &createdResponse); err != nil {
		t.Fatalf("decode token create success response: %v", err)
	}
	assertW2ExternalSourceTokenCreateResponse(t, createdResponse.Data, success.body, plainToken, fixture)
	assertW2ExternalSourceTokenCreateStored(t, ctx, db, createdResponse.Data, plainToken, now)
	if sourceAfter := readW2ExternalSourceTokenCreateSourceSnapshot(t, ctx, db, w2ExternalSourceTokenCreateSourceID); sourceAfter != sourceBefore {
		t.Fatal("token create changed the existing source row")
	}
	if existingTokenAfter := readW2ExternalSourceTokenCreateTokenSnapshot(t, ctx, db, w2ExternalSourceTokenCreateExistingTokenID); existingTokenAfter != existingTokenBefore {
		t.Fatal("token create changed the existing disabled token row")
	}

	stateBeforeMissing := readW2ExternalSourceTokenCreateRelevantState(t, ctx, db)
	missing := requestW2ExternalSourceTokenCreate(
		t,
		ctx,
		client,
		server.URL,
		"extsrc_w2_token_create_missing",
		w2ExternalSourceTokenCreateBody("Missing Source Token"),
	)
	assertW2ExternalSourceTokenCreateError(t, missing, "来源系统不存在")
	assertW2ExternalSourceTokenCreateRelevantStateUnchanged(t, ctx, db, stateBeforeMissing, "missing source token create")

	stateBeforeBuiltIn := readW2ExternalSourceTokenCreateRelevantState(t, ctx, db)
	builtIn := requestW2ExternalSourceTokenCreate(
		t,
		ctx,
		client,
		server.URL,
		publicapi.BuiltInTestSourceID,
		w2ExternalSourceTokenCreateBody("Built-in Source Token"),
	)
	assertW2ExternalSourceTokenCreateError(t, builtIn, managementexternalintegrationsources.ErrBuiltInTokenCreateRestricted.Error())
	assertW2ExternalSourceTokenCreateRelevantStateUnchanged(t, ctx, db, stateBeforeBuiltIn, "built-in source token create")
	assertW2ExternalSourceTokenCreateTargetTokenCount(t, ctx, db, publicapi.BuiltInTestSourceID, 0)

	collisionTokens := []string{
		w2ExternalSourceTokenCreateToken('a'),
		w2ExternalSourceTokenCreateToken('b'),
		w2ExternalSourceTokenCreateToken('c'),
	}
	insertW2ExternalSourceTokenCreateCollisionHolders(t, ctx, db, collisionTokens, now)
	rowsBeforeCollision := countW2ExternalSourceTokenCreateRows(t, ctx, db)
	collisionCalls := 0
	collisionIDs := 0
	collisionService := managementexternalintegrationsources.NewTokenCreateServiceWithOptions(
		managementexternalintegrationsources.TokenCreateServiceOptions{
			Store:  store,
			Secret: w2ExternalSourceTokenCreateSecret,
			Now:    func() time.Time { return now.Add(time.Minute) },
			NewID: func(prefix string) string {
				collisionIDs++
				return "exttok_w2_token_create_collision_" + string(rune('0'+collisionIDs))
			},
			NewToken: func() (string, error) {
				token := collisionTokens[collisionCalls]
				collisionCalls++
				return token, nil
			},
		},
	)
	collisionServer := httptest.NewServer(w2ExternalSourceTokenCreateRouter(authenticator, collisionService))
	defer collisionServer.Close()
	collisionClient := collisionServer.Client()
	collisionClient.Timeout = 15 * time.Second
	collision := requestW2ExternalSourceTokenCreate(
		t,
		ctx,
		collisionClient,
		collisionServer.URL,
		w2ExternalSourceTokenCreateCollisionSourceID,
		w2ExternalSourceTokenCreateBody("Collision Token"),
	)
	assertW2ExternalSourceTokenCreateError(t, collision, managementexternalintegrationsources.ErrTokenExists.Error())
	if collisionCalls != 3 || collisionIDs != 3 {
		t.Fatalf("token collision attempts = generated:%d ids:%d, want 3/3", collisionCalls, collisionIDs)
	}
	if rowsAfterCollision := countW2ExternalSourceTokenCreateRows(t, ctx, db); rowsAfterCollision != rowsBeforeCollision {
		t.Fatal("token hash collisions left source or token rows")
	}
	assertW2ExternalSourceTokenCreateTargetTokenCount(t, ctx, db, w2ExternalSourceTokenCreateCollisionSourceID, 0)

}

type w2ExternalSourceTokenCreateFixture struct {
	sourceCreatedAt   time.Time
	sourceUpdatedAt   time.Time
	sourceExpiresAt   time.Time
	sourceLastUsedAt  time.Time
	existingCreatedAt time.Time
	existingUpdatedAt time.Time
}

type w2ExternalSourceTokenCreateHTTPResult struct {
	status       int
	cacheControl string
	pragma       string
	body         []byte
}

type w2ExternalSourceTokenCreateSourceSnapshot struct {
	id             string
	name           string
	status         string
	scopesJSON     string
	rateLimitsJSON string
	expiresAt      sql.NullTime
	notes          sql.NullString
	lastUsedAt     sql.NullTime
	createdAt      time.Time
	updatedAt      time.Time
}

type w2ExternalSourceTokenCreateTokenSnapshot struct {
	id              string
	sourceID        string
	name            string
	hash            string
	secretEncrypted string
	prefix          string
	suffix          string
	status          string
	scopesJSON      string
	expiresAt       sql.NullTime
	lastUsedAt      sql.NullTime
	createdAt       time.Time
	updatedAt       time.Time
	revokedAt       sql.NullTime
}

type w2ExternalSourceTokenCreateRowCounts struct {
	sources int
	tokens  int
}

type w2ExternalSourceTokenCreateRelevantState struct {
	targetSource  w2ExternalSourceTokenCreateSourceSnapshot
	builtInSource w2ExternalSourceTokenCreateSourceSnapshot
	existingToken w2ExternalSourceTokenCreateTokenSnapshot
	relatedTokens []w2ExternalSourceTokenCreateTokenSnapshot
	allRowCounts  w2ExternalSourceTokenCreateRowCounts
}

func w2ExternalSourceTokenCreateRouter(
	authenticator *managementauth.Authenticator,
	service *managementexternalintegrationsources.TokenCreateService,
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
		ManagementExternalSourceTokenCreateHandler: httpapi.NewManagementExternalIntegrationSourceTokenCreateHandlerWithOperationLog(
			service,
			httpapi.ManagementOperationLogOptions{},
		),
	})
}

func w2ExternalSourceTokenCreateBody(name string) []byte {
	body, err := json.Marshal(map[string]any{
		"name":   name,
		"status": publicapi.TokenStatusActive,
		"scopes": []string{
			publicapi.ScopeGroupListRead,
			publicapi.ScopeAPIKeyListRead,
			publicapi.ScopeGroupListRead,
		},
		"expiresAt": "2026-08-02T03:04:05.678Z",
	})
	if err != nil {
		panic(err)
	}
	return body
}

func requestW2ExternalSourceTokenCreate(
	t *testing.T,
	ctx context.Context,
	client *http.Client,
	baseURL string,
	sourceID string,
	body []byte,
) w2ExternalSourceTokenCreateHTTPResult {
	t.Helper()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		baseURL+"/__aisys__/api/external-integration-sources/"+sourceID+"/tokens",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("build external source token create request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", "juhe_ai_session="+w2ExternalSourceTokenCreateAdminSessionToken)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("external source token create request: %v", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("read external source token create response: %v", err)
	}
	return w2ExternalSourceTokenCreateHTTPResult{
		status:       response.StatusCode,
		cacheControl: response.Header.Get("Cache-Control"),
		pragma:       response.Header.Get("Pragma"),
		body:         responseBody,
	}
}

func assertW2ExternalSourceTokenCreateResponse(
	t *testing.T,
	result managementexternalintegrationsources.TokenCreateResult,
	rawBody []byte,
	plainToken string,
	fixture w2ExternalSourceTokenCreateFixture,
) {
	t.Helper()
	if result.Token.ID != w2ExternalSourceTokenCreateNewTokenID ||
		result.Token.Name != w2ExternalSourceTokenCreateNewName ||
		result.Token.Token != plainToken ||
		result.Token.TokenPrefix != plainToken[:8] ||
		result.Token.TokenSuffix != plainToken[len(plainToken)-8:] ||
		!reflect.DeepEqual(result.Token.Scopes, []string{publicapi.ScopeAPIKeyListRead, publicapi.ScopeGroupListRead}) ||
		result.Token.ExpiresAt == nil || *result.Token.ExpiresAt != "2026-08-02T03:04:05.678Z" {
		t.Fatal("created token response fields are incomplete or inconsistent")
	}
	if !strings.HasPrefix(result.Token.Token, "juis_") || len(result.Token.Token) != 48 ||
		bytes.Count(rawBody, []byte(plainToken)) != 1 {
		t.Fatal("created token plaintext format or one-time response placement is invalid")
	}

	source := result.Source
	if source.ID != w2ExternalSourceTokenCreateSourceID || source.Name != w2ExternalSourceTokenCreateName ||
		source.Status != publicapi.SourceStatusDisabled ||
		!reflect.DeepEqual(source.Scopes, []string{publicapi.ScopeGroupListRead}) ||
		!reflect.DeepEqual(source.RateLimits, []managementexternalintegrationsources.RateLimitRule{{WindowSeconds: 60, MaxRequests: 9}}) ||
		source.ExpiresAt == nil || *source.ExpiresAt != fixture.sourceExpiresAt.Format("2006-01-02T15:04:05.000Z") ||
		source.Notes == nil || *source.Notes != "W2 token create source notes" ||
		source.LastUsedAt == nil || *source.LastUsedAt != fixture.sourceLastUsedAt.Format("2006-01-02T15:04:05.000Z") ||
		source.CreatedAt != fixture.sourceCreatedAt.Format("2006-01-02T15:04:05.000Z") ||
		source.UpdatedAt != fixture.sourceUpdatedAt.Format("2006-01-02T15:04:05.000Z") ||
		source.TokenCount != 2 || source.ActiveTokenCount != 1 || source.PrimaryToken != nil || source.IsBuiltIn ||
		len(source.Tokens) != 2 {
		t.Fatal("created token source detail fields are incomplete or inconsistent")
	}
	newToken := source.Tokens[0]
	if newToken.ID != w2ExternalSourceTokenCreateNewTokenID ||
		newToken.Name != w2ExternalSourceTokenCreateNewName ||
		newToken.TokenPrefix != plainToken[:8] ||
		newToken.TokenSuffix != plainToken[len(plainToken)-8:] ||
		newToken.Status != publicapi.TokenStatusActive ||
		!reflect.DeepEqual(newToken.Scopes, []string{publicapi.ScopeAPIKeyListRead, publicapi.ScopeGroupListRead}) ||
		newToken.ExpiresAt == nil || *newToken.ExpiresAt != "2026-08-02T03:04:05.678Z" ||
		newToken.LastUsedAt != nil ||
		newToken.CreatedAt != "2026-07-16T09:10:11.123Z" ||
		newToken.UpdatedAt != "2026-07-16T09:10:11.123Z" ||
		newToken.RevokedAt != nil || newToken.IsBuiltIn {
		t.Fatal("created token source detail new token fields are incomplete or inconsistent")
	}
	oldToken := source.Tokens[1]
	if oldToken.ID != w2ExternalSourceTokenCreateExistingTokenID ||
		oldToken.Name != "W2 Existing Disabled Token" ||
		oldToken.TokenPrefix != "juis_old" || oldToken.TokenSuffix != "oldtoken" ||
		oldToken.Status != publicapi.TokenStatusDisabled ||
		!reflect.DeepEqual(oldToken.Scopes, []string{publicapi.ScopeGroupListRead}) ||
		oldToken.ExpiresAt != nil || oldToken.LastUsedAt != nil ||
		oldToken.CreatedAt != fixture.existingCreatedAt.Format("2006-01-02T15:04:05.000Z") ||
		oldToken.UpdatedAt != fixture.existingUpdatedAt.Format("2006-01-02T15:04:05.000Z") ||
		oldToken.RevokedAt != nil || oldToken.IsBuiltIn {
		t.Fatal("created token source detail existing token fields are incomplete or inconsistent")
	}
	var envelope struct {
		Data struct {
			Source map[string]json.RawMessage `json:"source"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		t.Fatalf("decode token create response shape: %v", err)
	}
	if _, exists := envelope.Data.Source["primaryToken"]; exists {
		t.Fatal("created token source detail must omit primaryToken")
	}
}

func assertW2ExternalSourceTokenCreateStored(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	result managementexternalintegrationsources.TokenCreateResult,
	plainToken string,
	now time.Time,
) {
	t.Helper()
	var count int
	var row w2ExternalSourceTokenCreateTokenSnapshot
	if err := db.QueryRowContext(ctx, `
		SELECT
			COUNT(*), MIN(id), MIN(source_ref_id), MIN(name), MIN(token_hash),
			MIN(token_secret_encrypted), MIN(token_prefix), MIN(token_suffix), MIN(status),
			MIN(scopes_json), MIN(expires_at), MIN(last_used_at), MIN(created_at),
			MIN(updated_at), MIN(revoked_at)
		FROM juhe_business.external_integration_source_tokens
		WHERE id = $1 AND source_ref_id = $2
	`, w2ExternalSourceTokenCreateNewTokenID, w2ExternalSourceTokenCreateSourceID).Scan(
		&count,
		&row.id,
		&row.sourceID,
		&row.name,
		&row.hash,
		&row.secretEncrypted,
		&row.prefix,
		&row.suffix,
		&row.status,
		&row.scopesJSON,
		&row.expiresAt,
		&row.lastUsedAt,
		&row.createdAt,
		&row.updatedAt,
		&row.revokedAt,
	); err != nil {
		t.Fatalf("read created external source token row: %v", err)
	}
	wantExpiresAt := time.Date(2026, 8, 2, 3, 4, 5, 678_000_000, time.UTC)
	if count != 1 || row.id != result.Token.ID || row.sourceID != result.Source.ID ||
		row.name != w2ExternalSourceTokenCreateNewName || row.status != publicapi.TokenStatusActive ||
		row.prefix != plainToken[:8] || row.suffix != plainToken[len(plainToken)-8:] ||
		row.scopesJSON != `["juhe_ai_public:api_key_list:read","juhe_ai_public:group_list:read"]` ||
		!row.expiresAt.Valid || !row.expiresAt.Time.UTC().Equal(wantExpiresAt) || row.lastUsedAt.Valid || row.revokedAt.Valid ||
		!row.createdAt.UTC().Equal(now) || !row.updatedAt.UTC().Equal(now) {
		t.Fatal("stored external source token fields do not match the normalized request")
	}
	if row.hash != publicapiauth.HashExternalSourceToken(plainToken) || row.secretEncrypted == "" ||
		strings.Contains(row.secretEncrypted, plainToken) {
		t.Fatal("stored external source token hash or ciphertext is invalid")
	}
	payload, err := secretcrypto.NewJSONCodec(w2ExternalSourceTokenCreateSecret).DecryptJSON(row.secretEncrypted)
	if err != nil || len(payload) != 1 || payload["token"] != plainToken {
		t.Fatalf("stored external source token AES-GCM decryption matched response=%t", err == nil)
	}
}

func assertW2ExternalSourceTokenCreateError(
	t *testing.T,
	result w2ExternalSourceTokenCreateHTTPResult,
	wantMessage string,
) {
	t.Helper()
	if result.status != http.StatusBadRequest || result.cacheControl != "no-store" {
		t.Fatalf("token create error status=%d Cache-Control=%q", result.status, result.cacheControl)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(result.body, &response); err != nil {
		t.Fatalf("decode token create error response: %v", err)
	}
	if len(response) != 1 {
		t.Fatal("token create error response must contain only message")
	}
	var message string
	if err := json.Unmarshal(response["message"], &message); err != nil {
		t.Fatalf("decode token create error message: %v", err)
	}
	if message != wantMessage {
		t.Fatal("token create error message did not match the exact public contract")
	}
}

func insertW2ExternalSourceTokenCreateFixtures(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	now time.Time,
) w2ExternalSourceTokenCreateFixture {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, description, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES (
			$1, 'w2-external-source-token-create-admin', 'W2 Token Create Admin', NULL,
			'admin', 'active', 'hash', false, false, $2, $2
		)
	`, w2ExternalSourceTokenCreateAdminID, now); err != nil {
		t.Fatalf("insert token create admin: %v", err)
	}
	insertW2ManagementSessionForAccountFixture(
		t,
		ctx,
		db,
		w2ExternalSourceTokenCreateAdminSessionID,
		w2ExternalSourceTokenCreateAdminID,
		w2ExternalSourceTokenCreateAdminSessionToken,
		now,
	)

	sourceCreatedAt := now.Add(-24 * time.Hour)
	sourceUpdatedAt := sourceCreatedAt.Add(30 * time.Minute)
	sourceExpiresAt := now.Add(14 * 24 * time.Hour)
	sourceLastUsedAt := now.Add(-time.Hour)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_sources (
			id, name, status, scopes_json, rate_limits_json,
			expires_at, notes, last_used_at, created_at, updated_at
		) VALUES
			($1, $2, 'disabled', '["juhe_ai_public:group_list:read"]', '[{"windowSeconds":60,"maxRequests":9}]', $7, 'W2 token create source notes', $8, $9, $10),
			($3, 'W2 Built-in Token Create Guard', 'active', '[]', '[]', NULL, NULL, NULL, $6, $6),
			($4, 'W2 Token Create Collision Target', 'active', '[]', '[]', NULL, NULL, NULL, $6, $6),
			($5, 'W2 Token Create Collision Holders', 'active', '[]', '[]', NULL, NULL, NULL, $6, $6)
	`,
		w2ExternalSourceTokenCreateSourceID,
		w2ExternalSourceTokenCreateName,
		publicapi.BuiltInTestSourceID,
		w2ExternalSourceTokenCreateCollisionSourceID,
		w2ExternalSourceTokenCreateHolderSourceID,
		now,
		sourceExpiresAt,
		sourceLastUsedAt,
		sourceCreatedAt,
		sourceUpdatedAt,
	); err != nil {
		t.Fatalf("insert token create source fixtures: %v", err)
	}

	existingCreatedAt := now.Add(-2 * time.Hour)
	existingUpdatedAt := existingCreatedAt.Add(15 * time.Minute)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_source_tokens (
			id, source_ref_id, name, token_hash, token_secret_encrypted,
			token_prefix, token_suffix, status, scopes_json, created_at, updated_at
		) VALUES (
			$1, $2, 'W2 Existing Disabled Token', 'w2-existing-disabled-hash',
			'w2-existing-disabled-ciphertext', 'juis_old', 'oldtoken', 'disabled',
			'["juhe_ai_public:group_list:read"]', $3, $4
		)
	`, w2ExternalSourceTokenCreateExistingTokenID, w2ExternalSourceTokenCreateSourceID, existingCreatedAt, existingUpdatedAt); err != nil {
		t.Fatalf("insert existing disabled token fixture: %v", err)
	}

	return w2ExternalSourceTokenCreateFixture{
		sourceCreatedAt:   sourceCreatedAt,
		sourceUpdatedAt:   sourceUpdatedAt,
		sourceExpiresAt:   sourceExpiresAt,
		sourceLastUsedAt:  sourceLastUsedAt,
		existingCreatedAt: existingCreatedAt,
		existingUpdatedAt: existingUpdatedAt,
	}
}

func insertW2ExternalSourceTokenCreateCollisionHolders(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	tokens []string,
	now time.Time,
) {
	t.Helper()
	for index, token := range tokens {
		ciphertext, err := secretcrypto.NewJSONCodec(w2ExternalSourceTokenCreateSecret).EncryptJSON(map[string]any{"token": token})
		if err != nil {
			t.Fatal("encrypt token collision holder fixture")
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.external_integration_source_tokens (
				id, source_ref_id, name, token_hash, token_secret_encrypted,
				token_prefix, token_suffix, status, scopes_json, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, 'disabled', '[]', $8, $8)
		`,
			"exttok_w2_token_create_holder_"+string(rune('1'+index)),
			w2ExternalSourceTokenCreateHolderSourceID,
			"W2 Collision Holder "+string(rune('1'+index)),
			publicapiauth.HashExternalSourceToken(token),
			ciphertext,
			token[:8],
			token[len(token)-8:],
			now.Add(time.Duration(index+1)*time.Second),
		); err != nil {
			t.Fatalf("insert token collision holder fixture %d: %v", index+1, err)
		}
	}
}

func readW2ExternalSourceTokenCreateSourceSnapshot(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	sourceID string,
) w2ExternalSourceTokenCreateSourceSnapshot {
	t.Helper()
	var row w2ExternalSourceTokenCreateSourceSnapshot
	if err := db.QueryRowContext(ctx, `
		SELECT id, name, status, scopes_json, rate_limits_json,
		       expires_at, notes, last_used_at, created_at, updated_at
		FROM juhe_business.external_integration_sources
		WHERE id = $1
	`, sourceID).Scan(
		&row.id,
		&row.name,
		&row.status,
		&row.scopesJSON,
		&row.rateLimitsJSON,
		&row.expiresAt,
		&row.notes,
		&row.lastUsedAt,
		&row.createdAt,
		&row.updatedAt,
	); err != nil {
		t.Fatalf("read external source snapshot: %v", err)
	}
	return row
}

func readW2ExternalSourceTokenCreateTokenSnapshot(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	tokenID string,
) w2ExternalSourceTokenCreateTokenSnapshot {
	t.Helper()
	var row w2ExternalSourceTokenCreateTokenSnapshot
	if err := db.QueryRowContext(ctx, `
		SELECT id, source_ref_id, name, token_hash, token_secret_encrypted,
		       token_prefix, token_suffix, status, scopes_json, expires_at,
		       last_used_at, created_at, updated_at, revoked_at
		FROM juhe_business.external_integration_source_tokens
		WHERE id = $1
	`, tokenID).Scan(
		&row.id,
		&row.sourceID,
		&row.name,
		&row.hash,
		&row.secretEncrypted,
		&row.prefix,
		&row.suffix,
		&row.status,
		&row.scopesJSON,
		&row.expiresAt,
		&row.lastUsedAt,
		&row.createdAt,
		&row.updatedAt,
		&row.revokedAt,
	); err != nil {
		t.Fatalf("read external source token snapshot: %v", err)
	}
	return row
}

func readW2ExternalSourceTokenCreateRelatedTokenSnapshots(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) []w2ExternalSourceTokenCreateTokenSnapshot {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT id, source_ref_id, name, token_hash, token_secret_encrypted,
		       token_prefix, token_suffix, status, scopes_json, expires_at,
		       last_used_at, created_at, updated_at, revoked_at
		FROM juhe_business.external_integration_source_tokens
		WHERE source_ref_id IN ($1, $2)
		ORDER BY source_ref_id ASC, id ASC
	`, w2ExternalSourceTokenCreateSourceID, publicapi.BuiltInTestSourceID)
	if err != nil {
		t.Fatalf("read related external source token snapshots: %v", err)
	}
	defer rows.Close()

	snapshots := make([]w2ExternalSourceTokenCreateTokenSnapshot, 0, 2)
	for rows.Next() {
		var row w2ExternalSourceTokenCreateTokenSnapshot
		if err := rows.Scan(
			&row.id,
			&row.sourceID,
			&row.name,
			&row.hash,
			&row.secretEncrypted,
			&row.prefix,
			&row.suffix,
			&row.status,
			&row.scopesJSON,
			&row.expiresAt,
			&row.lastUsedAt,
			&row.createdAt,
			&row.updatedAt,
			&row.revokedAt,
		); err != nil {
			t.Fatalf("scan related external source token snapshot: %v", err)
		}
		snapshots = append(snapshots, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate related external source token snapshots: %v", err)
	}
	return snapshots
}

func readW2ExternalSourceTokenCreateRelevantState(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) w2ExternalSourceTokenCreateRelevantState {
	t.Helper()
	return w2ExternalSourceTokenCreateRelevantState{
		targetSource:  readW2ExternalSourceTokenCreateSourceSnapshot(t, ctx, db, w2ExternalSourceTokenCreateSourceID),
		builtInSource: readW2ExternalSourceTokenCreateSourceSnapshot(t, ctx, db, publicapi.BuiltInTestSourceID),
		existingToken: readW2ExternalSourceTokenCreateTokenSnapshot(t, ctx, db, w2ExternalSourceTokenCreateExistingTokenID),
		relatedTokens: readW2ExternalSourceTokenCreateRelatedTokenSnapshots(t, ctx, db),
		allRowCounts:  countW2ExternalSourceTokenCreateRows(t, ctx, db),
	}
}

func assertW2ExternalSourceTokenCreateRelevantStateUnchanged(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	before w2ExternalSourceTokenCreateRelevantState,
	operation string,
) {
	t.Helper()
	after := readW2ExternalSourceTokenCreateRelevantState(t, ctx, db)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("%s changed protected source or token fields", operation)
	}
}

func countW2ExternalSourceTokenCreateRows(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
) w2ExternalSourceTokenCreateRowCounts {
	t.Helper()
	var counts w2ExternalSourceTokenCreateRowCounts
	if err := db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM juhe_business.external_integration_sources),
			(SELECT COUNT(*) FROM juhe_business.external_integration_source_tokens)
	`).Scan(&counts.sources, &counts.tokens); err != nil {
		t.Fatalf("count external source token create rows: %v", err)
	}
	return counts
}

func assertW2ExternalSourceTokenCreateTargetTokenCount(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	sourceID string,
	want int,
) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM juhe_business.external_integration_source_tokens
		WHERE source_ref_id = $1
	`, sourceID).Scan(&count); err != nil {
		t.Fatalf("count target external source tokens: %v", err)
	}
	if count != want {
		t.Fatalf("target external source token count=%d, want %d", count, want)
	}
}

func cleanupW2ExternalSourceTokenCreateFixtures(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		DELETE FROM juhe_business.external_integration_sources
		WHERE id IN ($1, $2, $3, $4)
	`,
		w2ExternalSourceTokenCreateSourceID,
		publicapi.BuiltInTestSourceID,
		w2ExternalSourceTokenCreateCollisionSourceID,
		w2ExternalSourceTokenCreateHolderSourceID,
	); err != nil {
		t.Errorf("cleanup external source token create rows: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM juhe_business.system_accounts
		WHERE id = $1
	`, w2ExternalSourceTokenCreateAdminID); err != nil {
		t.Errorf("cleanup external source token create admin: %v", err)
	}
}

func w2ExternalSourceTokenCreateToken(character byte) string {
	return "juis_" + strings.Repeat(string(character), 43)
}
