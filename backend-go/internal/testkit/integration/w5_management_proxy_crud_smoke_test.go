//go:build integration

package integration

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
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
	"juhe-ai/backend-go/internal/modules/managementproxies"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestW5ManagementProxyCRUDPostgresSmoke(t *testing.T) {
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

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	insertW2ProxyOptionsFixture(t, ctx, db, now)
	sessionToken := "w5-management-proxy-crud-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()

	secret := "w5-management-proxy-crud-secret-0001"
	service := managementproxies.NewServiceWithOptions(managementproxies.ServiceOptions{
		Store:  store,
		Secret: secret,
	})
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
		Logger:                           slog.Default(),
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementProxyCreateHandler:     httpapi.NewManagementProxyCreateHandler(service),
		ManagementProxyUpdateHandler:     httpapi.NewManagementProxyUpdateHandler(service),
		ManagementProxyDeleteHandler:     httpapi.NewManagementProxyDeleteHandler(service),
	})

	initialPassword := " proxy secret with spaces "
	createRec := serveW5ProxyCRUDRequest(router, http.MethodPost, "/__aisys__/api/proxies", sessionToken, `{
		"name":"W5 CRUD Proxy",
		"description":"W5 proxy create",
		"type":"socks5h",
		"host":"proxy.example.com",
		"port":1080,
		"username":"proxy-user",
		"password":"`+initialPassword+`",
		"enabled":true
	}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var createBody struct {
		Data managementproxies.Summary `json:"data"`
	}
	if err := json.NewDecoder(createRec.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createBody.Data.ID == "" || createBody.Data.Name != "W5 CRUD Proxy" || createBody.Data.TestStatus != "unknown" {
		t.Fatalf("create response = %+v", createBody.Data)
	}
	assertW5ProxyEncryptedPassword(t, ctx, db, createBody.Data.ID, secret, initialPassword)

	_, err = db.ExecContext(ctx, `
		UPDATE juhe_business.proxy_profiles
		SET test_status = 'passed',
		    latency_ms = 42,
		    outbound_ip = '203.0.113.10',
		    outbound_region = 'US',
		    last_test_message = 'ok',
		    last_tested_at = $1
		WHERE id = $2
	`, now, createBody.Data.ID)
	if err != nil {
		t.Fatalf("seed W5 proxy test state: %v", err)
	}

	updatedPassword := " updated proxy secret "
	updateRec := serveW5ProxyCRUDRequest(router, http.MethodPatch, "/__aisys__/api/proxies/"+createBody.Data.ID, sessionToken, `{
		"username":"proxy-user-updated",
		"password":"`+updatedPassword+`"
	}`)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateRec.Code, updateRec.Body.String())
	}
	var updateBody struct {
		Data managementproxies.Summary `json:"data"`
	}
	if err := json.NewDecoder(updateRec.Body).Decode(&updateBody); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updateBody.Data.Username == nil || *updateBody.Data.Username != "proxy-user-updated" ||
		updateBody.Data.TestStatus != "unknown" ||
		updateBody.Data.LatencyMs != nil ||
		updateBody.Data.OutboundIP != nil ||
		updateBody.Data.LastTestMessage != nil ||
		updateBody.Data.LastTestedAt != nil {
		t.Fatalf("update response did not reset test state: %+v", updateBody.Data)
	}
	assertW5ProxyEncryptedPassword(t, ctx, db, createBody.Data.ID, secret, updatedPassword)

	insertW5ProxyBindingFixture(t, ctx, db, now, createBody.Data.ID)
	conflictRec := serveW5ProxyCRUDRequest(router, http.MethodDelete, "/__aisys__/api/proxies/"+createBody.Data.ID, sessionToken, "")
	if conflictRec.Code != http.StatusConflict || !strings.Contains(conflictRec.Body.String(), "W5 Bound Account") {
		t.Fatalf("bound delete status = %d, body = %s", conflictRec.Code, conflictRec.Body.String())
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM juhe_business.accounts WHERE id = 'acct_w5_proxy_bound'`); err != nil {
		t.Fatalf("delete W5 proxy binding fixture: %v", err)
	}
	deleteRec := serveW5ProxyCRUDRequest(router, http.MethodDelete, "/__aisys__/api/proxies/"+createBody.Data.ID, sessionToken, "")
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
	var remaining int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_business.proxy_profiles WHERE id = $1`, createBody.Data.ID).Scan(&remaining); err != nil {
		t.Fatalf("count deleted W5 proxy: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("deleted W5 proxy count = %d, want 0", remaining)
	}
}

func serveW5ProxyCRUDRequest(router http.Handler, method string, target string, token string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Cookie", "juhe_ai_session="+token)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func insertW5ProxyBindingFixture(t *testing.T, ctx context.Context, db *sql.DB, now time.Time, proxyID string) {
	t.Helper()
	_, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id,
			protocol_code, protocol_version, name, type, status,
			credentials_encrypted, credential_mask, proxy_profile_id, created_at, updated_at
		) VALUES (
			'acct_w5_proxy_bound', 'sys_w2_proxy_options', 'gpt', 'profile_gpt_openai_v1',
			'openai', 'v1', 'W5 Bound Account', 'api_key', 'active',
			'encrypted', 'sk-***', $1, $2, $3
		)
	`, proxyID, now, now)
	if err != nil {
		t.Fatalf("insert W5 proxy binding fixture: %v", err)
	}
}

func assertW5ProxyEncryptedPassword(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	proxyID string,
	secret string,
	wantPassword string,
) {
	t.Helper()
	var encrypted string
	if err := db.QueryRowContext(ctx, `
		SELECT password_encrypted
		FROM juhe_business.proxy_profiles
		WHERE id = $1
	`, proxyID).Scan(&encrypted); err != nil {
		t.Fatalf("read W5 proxy encrypted password: %v", err)
	}
	if strings.Contains(encrypted, wantPassword) {
		t.Fatalf("encrypted proxy password contains plaintext")
	}
	var payload struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(decryptW5NodeCompatibleJSON(t, secret, encrypted), &payload); err != nil {
		t.Fatalf("decode W5 proxy encrypted payload: %v", err)
	}
	if payload.Password != wantPassword {
		t.Fatalf("decrypted proxy password = %q, want %q", payload.Password, wantPassword)
	}
}

func decryptW5NodeCompatibleJSON(t *testing.T, secret string, encrypted string) []byte {
	t.Helper()
	parts := strings.Split(encrypted, ":")
	if len(parts) != 4 || parts[0] != "v1" {
		t.Fatalf("encrypted payload format = %q", encrypted)
	}
	decode := func(value string) []byte {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			t.Fatalf("decode encrypted payload component: %v", err)
		}
		return decoded
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("create AES cipher: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("create AES-GCM cipher: %v", err)
	}
	nonce := decode(parts[1])
	tag := decode(parts[2])
	ciphertext := decode(parts[3])
	plain, err := aead.Open(nil, nonce, append(ciphertext, tag...), nil)
	if err != nil {
		t.Fatalf("decrypt Node-compatible payload: %v", err)
	}
	return plain
}
