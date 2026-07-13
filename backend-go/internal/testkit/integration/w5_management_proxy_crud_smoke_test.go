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
	"sync"
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
	probe := &w5ProxyTestProbe{}
	service := managementproxies.NewServiceWithOptions(managementproxies.ServiceOptions{
		Store:  store,
		Secret: secret,
		Probe:  probe,
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
		ManagementProxyTestHandler:       httpapi.NewManagementProxyTestHandler(service),
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

	testRec := serveW5ProxyCRUDRequest(router, http.MethodPost, "/__aisys__/api/proxies/"+createBody.Data.ID+"/test", sessionToken, "")
	if testRec.Code != http.StatusOK {
		t.Fatalf("test status = %d, body = %s", testRec.Code, testRec.Body.String())
	}
	var testBody struct {
		Data managementproxies.ProxyTestReport `json:"data"`
	}
	if err := json.NewDecoder(testRec.Body).Decode(&testBody); err != nil {
		t.Fatalf("decode test response: %v", err)
	}
	if testBody.Data.ProxyID != createBody.Data.ID ||
		testBody.Data.Status != "warning" ||
		testBody.Data.OutboundIP == nil ||
		*testBody.Data.OutboundIP != "203.0.113.10" ||
		testBody.Data.BaseLatencyMs == nil {
		t.Fatalf("test response = %+v", testBody.Data)
	}
	if strings.Contains(testRec.Body.String(), initialPassword) || strings.Contains(testRec.Body.String(), "password_encrypted") {
		t.Fatalf("test response leaked sensitive data: %s", testRec.Body.String())
	}
	assertW5ProxyTestState(t, ctx, db, createBody.Data.ID, testBody.Data)
	probe.assertUsedProxyURL(t, initialPassword)

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

type w5ProxyTestProbe struct {
	mu        sync.Mutex
	proxyURLs []string
}

func (p *w5ProxyTestProbe) Probe(_ context.Context, input managementproxies.ProxyProbeInput) (managementproxies.ProxyProbeResult, error) {
	p.mu.Lock()
	p.proxyURLs = append(p.proxyURLs, input.ProxyURL)
	p.mu.Unlock()
	if input.TargetURL == "http://ip-api.com/json/?lang=zh-CN" {
		return managementproxies.ProxyProbeResult{
			StatusCode: http.StatusOK,
			LatencyMs:  10,
			Body:       `{"status":"success","query":"203.0.113.10","countryCode":"US"}`,
		}, nil
	}
	return managementproxies.ProxyProbeResult{
		StatusCode: http.StatusUnauthorized,
		LatencyMs:  25,
	}, nil
}

func (p *w5ProxyTestProbe) assertUsedProxyURL(t *testing.T, forbiddenPlaintext string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.proxyURLs) == 0 {
		t.Fatal("proxy test probe was not called")
	}
	for _, proxyURL := range p.proxyURLs {
		if strings.Contains(proxyURL, forbiddenPlaintext) {
			t.Fatalf("proxy URL leaked raw plaintext credential: %q", proxyURL)
		}
		if !strings.HasPrefix(proxyURL, "socks5h://proxy-user:") {
			t.Fatalf("proxy URL = %q, want socks5h URL with userinfo", proxyURL)
		}
	}
}

func assertW5ProxyTestState(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	proxyID string,
	report managementproxies.ProxyTestReport,
) {
	t.Helper()
	var (
		testStatus      string
		latencyMs       sql.NullInt64
		outboundIP      sql.NullString
		outboundRegion  sql.NullString
		lastTestMessage sql.NullString
		lastTestedAt    sql.NullTime
	)
	if err := db.QueryRowContext(ctx, `
		SELECT test_status, latency_ms, outbound_ip, outbound_region, last_test_message, last_tested_at
		FROM juhe_business.proxy_profiles
		WHERE id = $1
	`, proxyID).Scan(&testStatus, &latencyMs, &outboundIP, &outboundRegion, &lastTestMessage, &lastTestedAt); err != nil {
		t.Fatalf("read W5 proxy test state: %v", err)
	}
	if testStatus != report.Status ||
		!latencyMs.Valid ||
		report.BaseLatencyMs == nil ||
		int(latencyMs.Int64) != *report.BaseLatencyMs ||
		!outboundIP.Valid ||
		report.OutboundIP == nil ||
		outboundIP.String != *report.OutboundIP ||
		!outboundRegion.Valid ||
		report.OutboundRegion == nil ||
		outboundRegion.String != *report.OutboundRegion ||
		!lastTestMessage.Valid ||
		lastTestMessage.String != report.Message ||
		!lastTestedAt.Valid {
		t.Fatalf("persisted test state status=%q latency=%+v outbound=%+v/%+v message=%+v tested=%+v report=%+v",
			testStatus, latencyMs, outboundIP, outboundRegion, lastTestMessage, lastTestedAt, report)
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
			credentials_encrypted, credential_mask, proxy_profile_id, health_check_model, health_check_endpoint_family, created_at, updated_at
		) VALUES (
			'acct_w5_proxy_bound', 'sys_w2_proxy_options', 'gpt', 'profile_gpt_openai_v1',
			'openai', 'v1', 'W5 Bound Account', 'api_key', 'active',
			'encrypted', 'sk-***', $1, 'gpt-5.4-mini', 'responses', $2, $3
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
