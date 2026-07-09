//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
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
	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW3ManagementProviderModelCRUDPostgresSmoke(t *testing.T) {
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
	runGooseMigrations(t, db)

	now := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	insertW2ProxyOptionsFixture(t, ctx, db, now)
	insertW3ProviderModelCRUDBoundAccountFixture(t, ctx, db, now)
	sessionToken := "w3-management-provider-model-crud-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	service := managementprovidermodels.NewService(store)
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
		},
		Logger:                                     slog.Default(),
		ManagementAPIAuthMiddleware:                httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware:           httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementProviderModelsHandler:            httpapi.NewManagementProviderModelsHandler(service),
		ManagementProviderDefaultTestModelHandler:  httpapi.NewManagementProviderDefaultTestModelHandler(service),
		ManagementProviderCustomModelCreateHandler: httpapi.NewManagementProviderCustomModelCreateHandler(service),
		ManagementProviderCustomModelUpdateHandler: httpapi.NewManagementProviderCustomModelUpdateHandler(service),
		ManagementProviderCustomModelDeleteHandler: httpapi.NewManagementProviderCustomModelDeleteHandler(service),
	})

	createRec := serveW3ProviderModelCRUDRequest(router, http.MethodPost, "/__aisys__/api/providers/gpt/models?systemAccountId=sys_w2_proxy_options", sessionToken, `{
		"model":"w3-crud-model",
		"mode":"text",
		"supportedApiProtocols":["responses","chat_completions"],
		"inputUsdPer1M":1.25,
		"outputUsdPer1M":2.5,
		"pricingNotes":"W3 CRUD 价格说明",
		"notes":"W3 CRUD 备注"
	}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var createBody struct {
		Data managementprovidermodels.ModelCatalogItem `json:"data"`
	}
	if err := json.NewDecoder(createRec.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createBody.Data.ID == "" || createBody.Data.Model != "w3-crud-model" || createBody.Data.Scope != "personal" || createBody.Data.SystemAccountID != "sys_w2_proxy_options" || createBody.Data.PricingNotes != "W3 CRUD 价格说明" {
		t.Fatalf("create response = %+v", createBody.Data)
	}

	listRec := serveW3ProviderModelCRUDRequest(router, http.MethodGet, "/__aisys__/api/providers/gpt/models?systemAccountId=sys_w2_proxy_options&includeInactive=true&includeUnpriced=true", sessionToken, "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var listBody struct {
		Data []managementprovidermodels.ModelCatalogItem `json:"data"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if item := findW2ProviderModel(listBody.Data, "w3-crud-model"); item == nil || item.Notes != "W3 CRUD 备注" {
		t.Fatalf("list response missing created custom model with notes: %+v", listBody.Data)
	}

	defaultRec := serveW3ProviderModelCRUDRequest(router, http.MethodPut, "/__aisys__/api/providers/gpt/default-test-model?systemAccountId=sys_w2_proxy_options", sessionToken, `{"model":"w3-crud-model"}`)
	if defaultRec.Code != http.StatusOK {
		t.Fatalf("set default model status = %d, body = %s", defaultRec.Code, defaultRec.Body.String())
	}

	patchRec := serveW3ProviderModelCRUDRequest(router, http.MethodPatch, "/__aisys__/api/providers/gpt/models/"+createBody.Data.ID, sessionToken, `{"status":"disabled","notes":null}`)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}
	var patchBody struct {
		Data managementprovidermodels.ModelCatalogItem `json:"data"`
	}
	if err := json.NewDecoder(patchRec.Body).Decode(&patchBody); err != nil {
		t.Fatalf("decode patch response: %v", err)
	}
	if patchBody.Data.Status != "disabled" || patchBody.Data.Notes != "" {
		t.Fatalf("patch response = %+v", patchBody.Data)
	}
	assertW3ProviderModelCRUDDefaultPreferenceCleared(t, ctx, db, "w3-crud-model")

	deleteRec := serveW3ProviderModelCRUDRequest(router, http.MethodDelete, "/__aisys__/api/providers/gpt/models/"+createBody.Data.ID, sessionToken, "")
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
	var deleteBody struct {
		Data managementprovidermodels.CustomModelDeleteResult `json:"data"`
	}
	if err := json.NewDecoder(deleteRec.Body).Decode(&deleteBody); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if !deleteBody.Data.Deleted {
		t.Fatalf("delete response = %+v", deleteBody.Data)
	}
	assertW3ProviderModelCRUDCustomModelDeleted(t, ctx, db, createBody.Data.ID)

	boundCreateRec := serveW3ProviderModelCRUDRequest(router, http.MethodPost, "/__aisys__/api/providers/gpt/models?systemAccountId=sys_w2_proxy_options", sessionToken, `{
		"model":"w3-bound-model",
		"inputUsdPer1M":1,
		"outputUsdPer1M":2
	}`)
	if boundCreateRec.Code != http.StatusCreated {
		t.Fatalf("bound create status = %d, body = %s", boundCreateRec.Code, boundCreateRec.Body.String())
	}
	var boundCreateBody struct {
		Data managementprovidermodels.ModelCatalogItem `json:"data"`
	}
	if err := json.NewDecoder(boundCreateRec.Body).Decode(&boundCreateBody); err != nil {
		t.Fatalf("decode bound create response: %v", err)
	}
	insertW3ProviderModelCRUDBindingFixture(t, ctx, db, now, "w3-bound-model")
	boundDeleteRec := serveW3ProviderModelCRUDRequest(router, http.MethodDelete, "/__aisys__/api/providers/gpt/models/"+boundCreateBody.Data.ID, sessionToken, "")
	if boundDeleteRec.Code != http.StatusConflict {
		t.Fatalf("bound delete status = %d, body = %s", boundDeleteRec.Code, boundDeleteRec.Body.String())
	}
	if !strings.Contains(boundDeleteRec.Body.String(), "账户支持模型") || !strings.Contains(boundDeleteRec.Body.String(), "模型映射下游") || !strings.Contains(boundDeleteRec.Body.String(), "模型映射上游") {
		t.Fatalf("bound delete body = %s", boundDeleteRec.Body.String())
	}
}

func serveW3ProviderModelCRUDRequest(router http.Handler, method string, target string, token string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Cookie", "juhe_ai_session="+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func insertW3ProviderModelCRUDBoundAccountFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id,
			protocol_code, protocol_version, name, type, status,
			credentials_encrypted, credential_mask, created_at, updated_at
		) VALUES (
			'acct_w3_model_crud_bound', 'sys_w2_proxy_options', 'gpt', 'profile_gpt_openai_v1',
			'openai', 'v1', 'W3 Model CRUD Bound Account', 'api_key', 'active',
			'encrypted', 'sk-***', $1, $2
		)
	`, now, now)
	if err != nil {
		t.Fatalf("insert W3 provider model CRUD bound account: %v", err)
	}
}

func insertW3ProviderModelCRUDBindingFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time, model string) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.account_supported_models (
			account_id, provider_code, model, created_at
		) VALUES (
			'acct_w3_model_crud_bound', 'gpt', $1, $2
		)
	`, model, now)
	if err != nil {
		t.Fatalf("insert W3 provider model CRUD supported-model binding for %s: %v", model, err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO juhe_business.account_model_mappings (
			account_id, provider_code, source_model, source_endpoint_family,
			upstream_model, upstream_endpoint_family, enabled, created_at, updated_at
		) VALUES
			('acct_w3_model_crud_bound', 'gpt', $1, 'responses', 'upstream-model-for-source', 'responses', true, $2, $3),
			('acct_w3_model_crud_bound', 'gpt', 'source-model-for-upstream', 'chat_completions', $1, 'chat_completions', true, $2, $3)
	`, model, now, now)
	if err != nil {
		t.Fatalf("insert W3 provider model CRUD model-mapping binding for %s: %v", model, err)
	}
}

func assertW3ProviderModelCRUDDefaultPreferenceCleared(t *testing.T, ctx context.Context, db *sql.DB, model string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM juhe_business.provider_default_test_models
		WHERE system_account_id = 'sys_w2_proxy_options'
		  AND provider_code = 'gpt'
		  AND model = $1
	`, model).Scan(&count); err != nil {
		t.Fatalf("count provider default test model preference for %s: %v", model, err)
	}
	if count != 0 {
		t.Fatalf("default preference for %s count = %d, want 0", model, count)
	}
}

func assertW3ProviderModelCRUDCustomModelDeleted(t *testing.T, ctx context.Context, db *sql.DB, id string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM juhe_business.custom_provider_models
		WHERE id = $1
	`, id).Scan(&count); err != nil {
		t.Fatalf("count custom provider model %s: %v", id, err)
	}
	if count != 0 {
		t.Fatalf("custom provider model %s count = %d, want 0", id, count)
	}
}
