//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	"juhe-ai/backend-go/internal/store/port"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w2TokenUpdateAdminID        = "sys_w2_external_source_token_update_admin"
	w2TokenUpdateSessionID      = "sess_w2_external_source_token_update_admin"
	w2TokenUpdateSessionToken   = "w2-external-source-token-update-admin-session"
	w2TokenUpdateSecret         = "w2-external-source-token-update-secret"
	w2TokenUpdateSourceID       = "extsrc_w2_token_update"
	w2TokenUpdateOtherSourceID  = "extsrc_w2_token_update_other"
	w2TokenUpdateLockSourceID   = "extsrc_w2_token_update_lock_order"
	w2TokenUpdateMainID         = "exttok_w2_token_update_main"
	w2TokenUpdatePreserveID     = "exttok_w2_token_update_preserve"
	w2TokenUpdateNilRevokedID   = "exttok_w2_token_update_nil_revoked"
	w2TokenUpdateActiveID       = "exttok_w2_token_update_activate"
	w2TokenUpdateDisabledID     = "exttok_w2_token_update_disable"
	w2TokenUpdateResidueID      = "exttok_w2_token_update_residue"
	w2TokenUpdateEmptyID        = "exttok_w2_token_update_empty"
	w2TokenUpdateOtherID        = "exttok_w2_token_update_other"
	w2TokenUpdateRollbackID     = "exttok_w2_token_update_rollback"
	w2TokenUpdateConcurrentID   = "exttok_w2_token_update_concurrent"
	w2TokenUpdateLockTokenID    = "exttok_w2_token_update_lock_order"
	w2TokenUpdateBuiltInGuardID = "exttok_w2_token_update_builtin_guard"
)

type w2TokenUpdateHTTPResult struct {
	status       int
	cacheControl string
	pragma       string
	body         []byte
	err          error
}

type w2TokenUpdateSnapshot struct {
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

func TestW2ManagementExternalIntegrationSourceTokenUpdatePostgresSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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

	baseTime := time.Date(2026, 7, 17, 12, 0, 0, 123_000_000, time.UTC)
	insertW2TokenUpdateFixtures(t, ctx, db, baseTime)
	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return baseTime },
	})
	service := managementexternalintegrationsources.NewTokenUpdateServiceWithOptions(
		managementexternalintegrationsources.TokenUpdateServiceOptions{
			Store: store,
			Now:   func() time.Time { return baseTime },
		},
	)
	deleteService := managementexternalintegrationsources.NewDeleteService(store)
	server := httptest.NewServer(w2TokenUpdateRouter(authenticator, service, deleteService))
	defer server.Close()
	client := server.Client()
	client.Timeout = 15 * time.Second

	t.Run("HTTP patch persists a narrow token summary", func(t *testing.T) {
		before := readW2TokenUpdateSnapshot(t, ctx, db, w2TokenUpdateMainID)
		result := requestW2TokenUpdate(ctx, client, server.URL, w2TokenUpdateSourceID, w2TokenUpdateMainID, []byte(`{
			"name":"  W2 PATCH Updated Token  ",
			"status":"revoked",
			"scopes":["juhe_ai_public:group_list:read","juhe_ai_public:api_key_list:read","juhe_ai_public:group_list:read"],
			"expiresAt":"2026-08-03T04:05:06.789Z"
		}`))
		assertW2TokenUpdateResponseHeaders(t, result)
		if result.err != nil || result.status != http.StatusOK {
			t.Fatalf("token update status=%d err=%v", result.status, result.err)
		}
		var envelope struct {
			Data managementexternalintegrationsources.Token `json:"data"`
		}
		if err := json.Unmarshal(result.body, &envelope); err != nil {
			t.Fatalf("decode token update response: %v", err)
		}
		assertW2TokenUpdateNarrowResponse(t, result.body, envelope.Data, before, baseTime)

		after := readW2TokenUpdateSnapshot(t, ctx, db, w2TokenUpdateMainID)
		wantExpiry := time.Date(2026, 8, 3, 4, 5, 6, 789_000_000, time.UTC)
		if after.name != "W2 PATCH Updated Token" || after.status != publicapi.TokenStatusRevoked ||
			after.scopesJSON != `["juhe_ai_public:api_key_list:read","juhe_ai_public:group_list:read"]` ||
			!after.expiresAt.Valid || !after.expiresAt.Time.UTC().Equal(wantExpiry) ||
			!after.updatedAt.UTC().Equal(baseTime) || !after.revokedAt.Valid || !after.revokedAt.Time.UTC().Equal(baseTime) {
			t.Fatal("token update did not persist normalized mutable fields and revoked timestamp")
		}
		if after.sourceID != before.sourceID || after.hash != before.hash ||
			after.secretEncrypted != before.secretEncrypted || after.prefix != before.prefix ||
			after.suffix != before.suffix || after.lastUsedAt != before.lastUsedAt ||
			!after.createdAt.Equal(before.createdAt) {
			t.Fatal("token update changed immutable or sensitive columns")
		}
	})

	t.Run("revoked_at state machine and empty patch", func(t *testing.T) {
		cases := []struct {
			name          string
			tokenID       string
			body          string
			wantStatus    string
			wantRevokedAt sql.NullTime
		}{
			{"revoked omitted preserves timestamp", w2TokenUpdatePreserveID, `{}`, publicapi.TokenStatusRevoked, sql.NullTime{Time: baseTime.Add(-2 * time.Hour), Valid: true}},
			{"revoked status preserves nil", w2TokenUpdateNilRevokedID, `{"status":"revoked"}`, publicapi.TokenStatusRevoked, sql.NullTime{}},
			{"revoked to active clears", w2TokenUpdateActiveID, `{"status":"active"}`, publicapi.TokenStatusActive, sql.NullTime{}},
			{"revoked to disabled clears", w2TokenUpdateDisabledID, `{"status":"disabled"}`, publicapi.TokenStatusDisabled, sql.NullTime{}},
			{"nonrevoked omitted clears residue", w2TokenUpdateResidueID, `{}`, publicapi.TokenStatusActive, sql.NullTime{}},
			{"empty patch refreshes updated at", w2TokenUpdateEmptyID, `{}`, publicapi.TokenStatusDisabled, sql.NullTime{}},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				before := readW2TokenUpdateSnapshot(t, ctx, db, test.tokenID)
				result := requestW2TokenUpdate(ctx, client, server.URL, w2TokenUpdateSourceID, test.tokenID, []byte(test.body))
				assertW2TokenUpdateResponseHeaders(t, result)
				if result.err != nil || result.status != http.StatusOK {
					t.Fatalf("token update status=%d err=%v", result.status, result.err)
				}
				after := readW2TokenUpdateSnapshot(t, ctx, db, test.tokenID)
				if after.status != test.wantStatus || after.revokedAt != test.wantRevokedAt || !after.updatedAt.UTC().Equal(baseTime) {
					t.Fatalf("state transition status=%q revokedAt=%v updatedAt=%s", after.status, after.revokedAt.Valid, after.updatedAt.UTC())
				}
				before.updatedAt = after.updatedAt
				before.status = after.status
				before.revokedAt = after.revokedAt
				if after != before {
					t.Fatal("state transition changed unrelated token columns")
				}
			})
		}
	})

	t.Run("not found and built in rejects have no side effects", func(t *testing.T) {
		protectedBefore := readW2TokenUpdateSnapshot(t, ctx, db, w2TokenUpdateOtherID)
		builtInSourceTokenBefore := readW2TokenUpdateSnapshot(t, ctx, db, w2TokenUpdateBuiltInGuardID)
		builtInTokenBefore := readW2TokenUpdateSnapshot(t, ctx, db, publicapi.BuiltInTestTokenID)
		checks := []struct {
			name       string
			sourceID   string
			tokenID    string
			wantStatus int
		}{
			{"missing source", "extsrc_w2_token_update_missing", w2TokenUpdateMainID, http.StatusNotFound},
			{"missing token", w2TokenUpdateSourceID, "exttok_w2_token_update_missing", http.StatusNotFound},
			{"source mismatch", w2TokenUpdateSourceID, w2TokenUpdateOtherID, http.StatusNotFound},
			{"built in source", publicapi.BuiltInTestSourceID, w2TokenUpdateBuiltInGuardID, http.StatusBadRequest},
			{"built in token", w2TokenUpdateSourceID, publicapi.BuiltInTestTokenID, http.StatusBadRequest},
		}
		for _, check := range checks {
			result := requestW2TokenUpdate(ctx, client, server.URL, check.sourceID, check.tokenID, []byte(`{"name":"must not persist"}`))
			assertW2TokenUpdateResponseHeaders(t, result)
			if result.err != nil || result.status != check.wantStatus {
				t.Fatalf("%s status=%d err=%v", check.name, result.status, result.err)
			}
		}
		if after := readW2TokenUpdateSnapshot(t, ctx, db, w2TokenUpdateOtherID); after != protectedBefore {
			t.Fatal("rejected token updates changed a protected row")
		}
		if after := readW2TokenUpdateSnapshot(t, ctx, db, w2TokenUpdateBuiltInGuardID); after != builtInSourceTokenBefore {
			t.Fatal("built-in source rejection changed its token row")
		}
		if after := readW2TokenUpdateSnapshot(t, ctx, db, publicapi.BuiltInTestTokenID); after != builtInTokenBefore {
			t.Fatal("built-in token rejection changed its token row")
		}
	})

	t.Run("store validation callback rolls back", func(t *testing.T) {
		before := readW2TokenUpdateSnapshot(t, ctx, db, w2TokenUpdateRollbackID)
		validationErr := errors.New("integration validation rejected update")
		_, err := store.UpdateManagementExternalIntegrationSourceToken(ctx, port.ManagementExternalIntegrationSourceTokenUpdateInput{
			SourceID:  w2TokenUpdateSourceID,
			TokenID:   w2TokenUpdateRollbackID,
			HasName:   true,
			Name:      "must roll back",
			UpdatedAt: baseTime,
		}, func(port.ManagementExternalIntegrationSourceTokenUpdateResult) error {
			return validationErr
		})
		if !errors.Is(err, validationErr) {
			t.Fatalf("store validation error=%v", err)
		}
		if after := readW2TokenUpdateSnapshot(t, ctx, db, w2TokenUpdateRollbackID); after != before {
			t.Fatal("store validation callback failure did not roll back all columns")
		}
	})

	t.Run("concurrent patches merge under row locks", func(t *testing.T) {
		blocker, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin concurrent patch blocker: %v", err)
		}
		defer blocker.Rollback()
		if err := blocker.QueryRowContext(ctx, `SELECT id FROM juhe_business.external_integration_sources WHERE id = $1 FOR UPDATE`, w2TokenUpdateSourceID).Scan(new(string)); err != nil {
			t.Fatalf("lock concurrent patch source: %v", err)
		}
		outcomes := make(chan w2TokenUpdateHTTPResult, 2)
		go func() {
			outcomes <- requestW2TokenUpdate(ctx, client, server.URL, w2TokenUpdateSourceID, w2TokenUpdateConcurrentID, []byte(`{"name":"W2 Concurrent Name"}`))
		}()
		go func() {
			outcomes <- requestW2TokenUpdate(ctx, client, server.URL, w2TokenUpdateSourceID, w2TokenUpdateConcurrentID, []byte(`{"scopes":["juhe_ai_public:api_key_list:read"]}`))
		}()
		waitForW2TokenUpdateBlockedQueries(t, ctx, db, 2, "FROM juhe_business.external_integration_sources AS sources")
		if err := blocker.Rollback(); err != nil {
			t.Fatalf("release concurrent patch blocker: %v", err)
		}
		for range 2 {
			outcome := receiveW2TokenUpdateOutcome(t, outcomes, "concurrent patch")
			assertW2TokenUpdateResponseHeaders(t, outcome)
			if outcome.err != nil || outcome.status != http.StatusOK {
				t.Fatalf("concurrent patch status=%d err=%v", outcome.status, outcome.err)
			}
		}
		after := readW2TokenUpdateSnapshot(t, ctx, db, w2TokenUpdateConcurrentID)
		if after.name != "W2 Concurrent Name" || after.scopesJSON != `["juhe_ai_public:api_key_list:read"]` {
			t.Fatalf("concurrent patches lost an update: name=%q scopes=%q", after.name, after.scopesJSON)
		}
	})

	t.Run("patch and Go source delete use compatible lock order", func(t *testing.T) {
		blocker, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin lock-order blocker: %v", err)
		}
		defer blocker.Rollback()
		if err := blocker.QueryRowContext(ctx, `SELECT id FROM juhe_business.external_integration_source_tokens WHERE id = $1 FOR UPDATE`, w2TokenUpdateLockTokenID).Scan(new(string)); err != nil {
			t.Fatalf("lock lock-order token: %v", err)
		}
		patchDone := make(chan w2TokenUpdateHTTPResult, 1)
		go func() {
			patchDone <- requestW2TokenUpdate(ctx, client, server.URL, w2TokenUpdateLockSourceID, w2TokenUpdateLockTokenID, []byte(`{"name":"W2 Lock Order Patched"}`))
		}()
		waitForW2TokenUpdateBlockedQueries(t, ctx, db, 1, "FROM juhe_business.external_integration_source_tokens AS tokens")

		deleteDone := make(chan w2TokenUpdateHTTPResult, 1)
		go func() {
			deleteDone <- requestW2TokenUpdateDelete(ctx, client, server.URL, w2TokenUpdateLockSourceID)
		}()
		waitForW2TokenUpdateBlockedQueries(t, ctx, db, 1, "FindManagementExternalIntegrationSourceForUpdate")
		if err := blocker.Rollback(); err != nil {
			t.Fatalf("release lock-order blocker: %v", err)
		}

		patch := receiveW2TokenUpdateOutcome(t, patchDone, "lock-order patch")
		deleted := receiveW2TokenUpdateOutcome(t, deleteDone, "lock-order delete")
		if patch.err != nil || (patch.status != http.StatusOK && patch.status != http.StatusNotFound) {
			t.Fatalf("lock-order patch status=%d err=%v", patch.status, patch.err)
		}
		if deleted.err != nil || (deleted.status != http.StatusNoContent && deleted.status != http.StatusNotFound) {
			t.Fatalf("lock-order delete status=%d err=%v", deleted.status, deleted.err)
		}
		if bytes.Contains(patch.body, []byte("40P01")) || bytes.Contains(deleted.body, []byte("40P01")) {
			t.Fatal("patch/delete lock ordering exposed a PostgreSQL deadlock")
		}
	})
}

func w2TokenUpdateRouter(
	authenticator *managementauth.Authenticator,
	updateService *managementexternalintegrationsources.TokenUpdateService,
	deleteService *managementexternalintegrationsources.DeleteService,
) http.Handler {
	return httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true, TrustProxy: "false"},
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementExternalSourceTokenUpdateHandler: httpapi.NewManagementExternalIntegrationSourceTokenUpdateHandlerWithOperationLog(
			updateService,
			httpapi.ManagementOperationLogOptions{},
		),
		ManagementExternalIntegrationSourceDeleteHandler: httpapi.NewManagementExternalIntegrationSourceDeleteHandlerWithOperationLog(
			deleteService,
			httpapi.ManagementOperationLogOptions{},
		),
	})
}

func requestW2TokenUpdate(ctx context.Context, client *http.Client, baseURL, sourceID, tokenID string, body []byte) w2TokenUpdateHTTPResult {
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, fmt.Sprintf(
		"%s/__aisys__/api/external-integration-sources/%s/tokens/%s", baseURL, sourceID, tokenID,
	), bytes.NewReader(body))
	if err != nil {
		return w2TokenUpdateHTTPResult{err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", "juhe_ai_session="+w2TokenUpdateSessionToken)
	return doW2TokenUpdateRequest(client, request)
}

func requestW2TokenUpdateDelete(ctx context.Context, client *http.Client, baseURL, sourceID string) w2TokenUpdateHTTPResult {
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+"/__aisys__/api/external-integration-sources/"+sourceID, nil)
	if err != nil {
		return w2TokenUpdateHTTPResult{err: err}
	}
	request.Header.Set("Cookie", "juhe_ai_session="+w2TokenUpdateSessionToken)
	return doW2TokenUpdateRequest(client, request)
}

func doW2TokenUpdateRequest(client *http.Client, request *http.Request) w2TokenUpdateHTTPResult {
	response, err := client.Do(request)
	if err != nil {
		return w2TokenUpdateHTTPResult{err: err}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return w2TokenUpdateHTTPResult{
		status: response.StatusCode, cacheControl: response.Header.Get("Cache-Control"),
		pragma: response.Header.Get("Pragma"), body: body, err: err,
	}
}

func assertW2TokenUpdateResponseHeaders(t *testing.T, result w2TokenUpdateHTTPResult) {
	t.Helper()
	if result.cacheControl != "no-store" || result.pragma != "" {
		t.Fatalf("token update response Cache-Control=%q Pragma=%q", result.cacheControl, result.pragma)
	}
}

func assertW2TokenUpdateNarrowResponse(
	t *testing.T,
	raw []byte,
	token managementexternalintegrationsources.Token,
	before w2TokenUpdateSnapshot,
	now time.Time,
) {
	t.Helper()
	if token.ID != w2TokenUpdateMainID || token.Name != "W2 PATCH Updated Token" ||
		token.Status != publicapi.TokenStatusRevoked ||
		!reflect.DeepEqual(token.Scopes, []string{publicapi.ScopeAPIKeyListRead, publicapi.ScopeGroupListRead}) ||
		token.ExpiresAt == nil || *token.ExpiresAt != "2026-08-03T04:05:06.789Z" ||
		token.UpdatedAt != now.Format("2006-01-02T15:04:05.000Z") ||
		token.RevokedAt == nil || *token.RevokedAt != now.Format("2006-01-02T15:04:05.000Z") || token.IsBuiltIn {
		t.Fatal("token update response summary does not match the persisted update")
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope) != 1 {
		t.Fatal("token update response must contain only data")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope["data"], &fields); err != nil {
		t.Fatalf("decode token update data fields: %v", err)
	}
	allowed := map[string]bool{
		"id": true, "name": true, "tokenPrefix": true, "tokenSuffix": true, "status": true,
		"scopes": true, "expiresAt": true, "lastUsedAt": true, "createdAt": true,
		"updatedAt": true, "revokedAt": true, "isBuiltIn": true,
	}
	for field := range fields {
		if !allowed[field] {
			t.Fatalf("token update response contains unexpected field %q", field)
		}
	}
	for _, forbidden := range []string{"token", "tokenHash", "token_hash", "tokenSecretEncrypted", "token_secret_encrypted", "source", "sourceId", "sourceRefId"} {
		if _, exists := fields[forbidden]; exists {
			t.Fatalf("token update response exposes forbidden field %q", forbidden)
		}
	}
	mainPlainToken := w2TokenUpdatePlainToken(0)
	for _, secretMaterial := range []string{mainPlainToken, before.hash, before.secretEncrypted, before.sourceID} {
		if bytes.Contains(raw, []byte(secretMaterial)) {
			t.Fatal("token update response leaked secret material or source identity")
		}
	}
}

func insertW2TokenUpdateFixtures(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.system_accounts (
			id, username, display_name, role, status, password_hash,
			must_change_password, image_generation_enabled, created_at, updated_at
		) VALUES ($1, 'w2-token-update-admin', 'W2 Token Update Admin', 'admin', 'active', 'hash', false, false, $2, $2)
	`, w2TokenUpdateAdminID, now); err != nil {
		t.Fatalf("insert token update admin: %v", err)
	}
	insertW2ManagementSessionForAccountFixture(t, ctx, db, w2TokenUpdateSessionID, w2TokenUpdateAdminID, w2TokenUpdateSessionToken, now)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.external_integration_sources
			(id, name, status, scopes_json, rate_limits_json, created_at, updated_at)
		VALUES
			($1, 'W2 Token Update Source', 'active', '[]', '[]', $5, $5),
			($2, 'W2 Token Update Other Source', 'active', '[]', '[]', $5, $5),
			($3, 'W2 Token Update Lock Source', 'active', '[]', '[]', $5, $5),
			($4, 'W2 Token Update Built-in Source', 'active', '[]', '[]', $5, $5)
	`, w2TokenUpdateSourceID, w2TokenUpdateOtherSourceID, w2TokenUpdateLockSourceID, publicapi.BuiltInTestSourceID, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("insert token update sources: %v", err)
	}

	type tokenFixture struct {
		id, sourceID, name, status, scopes string
		revokedAt                          *time.Time
	}
	preserved := now.Add(-2 * time.Hour)
	residue := now.Add(-3 * time.Hour)
	fixtures := []tokenFixture{
		{w2TokenUpdateMainID, w2TokenUpdateSourceID, "W2 Main Token", publicapi.TokenStatusActive, `[]`, nil},
		{w2TokenUpdatePreserveID, w2TokenUpdateSourceID, "W2 Preserve Token", publicapi.TokenStatusRevoked, `[]`, &preserved},
		{w2TokenUpdateNilRevokedID, w2TokenUpdateSourceID, "W2 Nil Revoked Token", publicapi.TokenStatusRevoked, `[]`, nil},
		{w2TokenUpdateActiveID, w2TokenUpdateSourceID, "W2 Activate Token", publicapi.TokenStatusRevoked, `[]`, &preserved},
		{w2TokenUpdateDisabledID, w2TokenUpdateSourceID, "W2 Disable Token", publicapi.TokenStatusRevoked, `[]`, &preserved},
		{w2TokenUpdateResidueID, w2TokenUpdateSourceID, "W2 Residue Token", publicapi.TokenStatusActive, `[]`, &residue},
		{w2TokenUpdateEmptyID, w2TokenUpdateSourceID, "W2 Empty Patch Token", publicapi.TokenStatusDisabled, `[]`, nil},
		{w2TokenUpdateOtherID, w2TokenUpdateOtherSourceID, "W2 Other Source Token", publicapi.TokenStatusActive, `[]`, nil},
		{w2TokenUpdateRollbackID, w2TokenUpdateSourceID, "W2 Rollback Token", publicapi.TokenStatusActive, `[]`, nil},
		{w2TokenUpdateConcurrentID, w2TokenUpdateSourceID, "W2 Concurrent Original", publicapi.TokenStatusActive, `[]`, nil},
		{w2TokenUpdateLockTokenID, w2TokenUpdateLockSourceID, "W2 Lock Order Token", publicapi.TokenStatusActive, `[]`, nil},
		{w2TokenUpdateBuiltInGuardID, publicapi.BuiltInTestSourceID, "W2 Built-in Guard Token", publicapi.TokenStatusActive, `[]`, nil},
		{publicapi.BuiltInTestTokenID, w2TokenUpdateSourceID, "W2 Built-in ID Token", publicapi.TokenStatusActive, `[]`, nil},
	}
	codec := secretcrypto.NewJSONCodec(w2TokenUpdateSecret)
	for index, fixture := range fixtures {
		plain := w2TokenUpdatePlainToken(index)
		ciphertext, err := codec.EncryptJSON(map[string]any{"token": plain})
		if err != nil {
			t.Fatalf("encrypt token update fixture %d", index)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO juhe_business.external_integration_source_tokens (
				id, source_ref_id, name, token_hash, token_secret_encrypted,
				token_prefix, token_suffix, status, scopes_json, expires_at,
				last_used_at, created_at, updated_at, revoked_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULL, $10, $11, $12, $13)
		`, fixture.id, fixture.sourceID, fixture.name, publicapiauth.HashExternalSourceToken(plain), ciphertext,
			plain[:8], plain[len(plain)-8:], fixture.status, fixture.scopes, now.Add(-4*time.Hour),
			now.Add(-24*time.Hour), now.Add(-6*time.Hour), fixture.revokedAt); err != nil {
			t.Fatalf("insert token update fixture %s: %v", fixture.id, err)
		}
	}
}

func w2TokenUpdatePlainToken(index int) string {
	return "juis_" + strings.Repeat(string(rune('a'+index)), 43)
}

func readW2TokenUpdateSnapshot(t *testing.T, ctx context.Context, db *sql.DB, tokenID string) w2TokenUpdateSnapshot {
	t.Helper()
	var row w2TokenUpdateSnapshot
	if err := db.QueryRowContext(ctx, `
		SELECT source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix,
		       status, scopes_json, expires_at, last_used_at, created_at, updated_at, revoked_at
		FROM juhe_business.external_integration_source_tokens
		WHERE id = $1
	`, tokenID).Scan(&row.sourceID, &row.name, &row.hash, &row.secretEncrypted, &row.prefix, &row.suffix,
		&row.status, &row.scopesJSON, &row.expiresAt, &row.lastUsedAt, &row.createdAt, &row.updatedAt, &row.revokedAt); err != nil {
		t.Fatalf("read token update snapshot %s: %v", tokenID, err)
	}
	return row
}

func waitForW2TokenUpdateBlockedQueries(t *testing.T, ctx context.Context, db *sql.DB, want int, queryFragment string) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		err := db.QueryRowContext(waitCtx, `
			SELECT COUNT(*)
			FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND state = 'active'
			  AND wait_event_type = 'Lock'
			  AND cardinality(pg_blocking_pids(pid)) > 0
			  AND query LIKE '%' || $1 || '%'
		`, queryFragment).Scan(&count)
		if err != nil {
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				t.Fatalf("timed out waiting for %d blocked queries containing %q", want, queryFragment)
			}
			t.Fatalf("inspect blocked token update queries: %v", err)
		}
		if count >= want {
			return
		}
		select {
		case <-ticker.C:
		case <-waitCtx.Done():
			t.Fatalf("timed out waiting for %d blocked queries containing %q", want, queryFragment)
		}
	}
}

func receiveW2TokenUpdateOutcome(t *testing.T, outcomes <-chan w2TokenUpdateHTTPResult, operation string) w2TokenUpdateHTTPResult {
	t.Helper()
	select {
	case result := <-outcomes:
		return result
	case <-time.After(12 * time.Second):
		t.Fatalf("%s timed out", operation)
		return w2TokenUpdateHTTPResult{}
	}
}
