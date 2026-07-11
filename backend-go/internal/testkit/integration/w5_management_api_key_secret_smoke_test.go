//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/apikeysecret"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/secretcrypto"
)

const w5ManagementAPIKeySecretRuntimeSecret = "w5-management-api-key-secret-runtime-secret"

type w5ManagementAPIKeySecretInvalidator struct {
	events []string
}

func (s *w5ManagementAPIKeySecretInvalidator) InvalidateAPIKeyValidationCache(context.Context) error {
	s.events = append(s.events, "validation")
	return nil
}

func (s *w5ManagementAPIKeySecretInvalidator) InvalidateGatewayRuntime(
	_ context.Context,
	reason string,
) error {
	s.events = append(s.events, "runtime:"+reason)
	return nil
}

func (s *w5ManagementAPIKeySecretInvalidator) InvalidateAPIKeyQuotaChanged(
	_ context.Context,
	apiKeyID string,
	reason string,
) error {
	s.events = append(s.events, "quota:"+apiKeyID+":"+reason)
	return nil
}

func exerciseW5ManagementAPIKeySecretSmoke(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	router http.Handler,
	invalidator *w5ManagementAPIKeySecretInvalidator,
) {
	t.Helper()

	const existingKey = "sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	codec := secretcrypto.NewJSONCodec(w5ManagementAPIKeySecretRuntimeSecret)
	encrypted, err := codec.EncryptJSON(map[string]any{"key": existingKey})
	if err != nil {
		t.Fatalf("encrypt W5 management API Key secret fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.api_keys
		SET key_hash = $1,
		    key_prefix = $2,
		    key_suffix = $3,
		    key_secret_encrypted = $4
		WHERE id = $5
	`, apikeysecret.Hash(existingKey), apikeysecret.Prefix(existingKey), apikeysecret.Suffix(existingKey),
		encrypted, w5ManagementAPIKeyListOwnerDefaultID); err != nil {
		t.Fatalf("prepare W5 management API Key secret fixture: %v", err)
	}

	wrongOwner := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodGet,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListOwnerDefaultID+
			"/secret?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListAdminToken,
		"",
	)
	if wrongOwner.Code != http.StatusNotFound {
		t.Fatalf("wrong-owner reveal status = %d, body = %s", wrongOwner.Code, wrongOwner.Body.String())
	}

	adminReveal := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodGet,
		"/__aisys__/api/api-keys/"+w5ManagementAPIKeyListOwnerDefaultID+"/secret",
		w5ManagementAPIKeyListAdminToken,
		"",
	)
	assertW5ManagementAPIKeySecretReveal(t, adminReveal, existingKey)

	selfReveal := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodGet,
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyListOwnerDefaultID+
			"/secret?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListOwnerToken,
		"",
	)
	assertW5ManagementAPIKeySecretReveal(t, selfReveal, existingKey)

	if _, err := db.ExecContext(ctx, `
		UPDATE juhe_business.api_keys
		SET key_secret_encrypted = NULL
		WHERE id = $1
	`, w5ManagementAPIKeyListEmptyUsageID); err != nil {
		t.Fatalf("set W5 management API Key NULL ciphertext fixture: %v", err)
	}
	nullReveal := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodGet,
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyListEmptyUsageID+"/secret",
		w5ManagementAPIKeyListOwnerToken,
		"",
	)
	if nullReveal.Code != http.StatusInternalServerError {
		t.Fatalf("NULL ciphertext reveal status = %d, body = %s", nullReveal.Code, nullReveal.Body.String())
	}

	refresh := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodPost,
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyListEmptyUsageID+
			"/refresh-key?systemAccountId="+w5ManagementAPIKeyListOtherID,
		w5ManagementAPIKeyListOwnerToken,
		"{}",
	)
	if refresh.Code != http.StatusOK {
		t.Fatalf("self refresh status = %d, body = %s", refresh.Code, refresh.Body.String())
	}
	assertW5ManagementAPIKeySecretNoStore(t, refresh)
	var refreshEnvelope struct {
		Data    map[string]json.RawMessage `json:"data"`
		Message string                     `json:"message"`
	}
	if err := json.NewDecoder(refresh.Body).Decode(&refreshEnvelope); err != nil {
		t.Fatalf("decode self refresh response: %v", err)
	}
	if refreshEnvelope.Message != "API Key 密钥已刷新，请立即复制完整密钥" {
		t.Fatalf("self refresh message = %q", refreshEnvelope.Message)
	}
	for _, forbidden := range []string{
		"systemAccountId",
		"systemAccountName",
		"keyHash",
		"keySecretEncrypted",
	} {
		if _, exists := refreshEnvelope.Data[forbidden]; exists {
			t.Fatalf("self refresh exposed %s: %s", forbidden, refresh.Body.String())
		}
	}
	var refreshedKey string
	if err := json.Unmarshal(refreshEnvelope.Data["key"], &refreshedKey); err != nil || refreshedKey == "" {
		t.Fatalf("decode refreshed key: key=%q err=%v", refreshedKey, err)
	}
	if got, want := invalidator.events, []string{
		"validation",
		"runtime:api_key_secret_refreshed",
		"quota:" + w5ManagementAPIKeyListEmptyUsageID + ":api_key_secret_refreshed",
	}; !equalStrings(got, want) {
		t.Fatalf("secret refresh invalidations = %#v, want %#v", got, want)
	}

	var storedHash string
	var storedPrefix string
	var storedSuffix string
	var storedEncrypted string
	if err := db.QueryRowContext(ctx, `
		SELECT key_hash, key_prefix, key_suffix, key_secret_encrypted
		FROM juhe_business.api_keys
		WHERE id = $1
	`, w5ManagementAPIKeyListEmptyUsageID).Scan(
		&storedHash,
		&storedPrefix,
		&storedSuffix,
		&storedEncrypted,
	); err != nil {
		t.Fatalf("query refreshed W5 management API Key secret: %v", err)
	}
	if storedHash != apikeysecret.Hash(refreshedKey) ||
		storedPrefix != apikeysecret.Prefix(refreshedKey) ||
		storedSuffix != apikeysecret.Suffix(refreshedKey) {
		t.Fatalf(
			"stored refreshed secret markers hash=%q prefix=%q suffix=%q",
			storedHash,
			storedPrefix,
			storedSuffix,
		)
	}
	storedPayload, err := codec.DecryptJSON(storedEncrypted)
	if err != nil || storedPayload["key"] != refreshedKey {
		t.Fatalf("stored refreshed ciphertext payload=%#v err=%v", storedPayload, err)
	}

	repairedReveal := serveW5ManagementAPIKeySecretRequest(
		router,
		http.MethodGet,
		"/__aisys__/api/my-api-keys/"+w5ManagementAPIKeyListEmptyUsageID+"/secret",
		w5ManagementAPIKeyListOwnerToken,
		"",
	)
	assertW5ManagementAPIKeySecretReveal(t, repairedReveal, refreshedKey)
}

func serveW5ManagementAPIKeySecretRequest(
	router http.Handler,
	method string,
	target string,
	sessionToken string,
	body string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if sessionToken != "" {
		req.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	}
	req.Header.Set("User-Agent", "w5-management-api-key-secret-smoke")
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func assertW5ManagementAPIKeySecretReveal(
	t *testing.T,
	rec *httptest.ResponseRecorder,
	wantKey string,
) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("secret reveal status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertW5ManagementAPIKeySecretNoStore(t, rec)
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode secret reveal response: %v", err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("secret reveal fields = %#v", envelope.Data)
	}
	var key string
	if err := json.Unmarshal(envelope.Data["key"], &key); err != nil || key != wantKey {
		t.Fatalf("secret reveal key = %q, want %q, err=%v", key, wantKey, err)
	}
}

func assertW5ManagementAPIKeySecretNoStore(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Header().Get("Cache-Control") != "no-store" ||
		rec.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("secret response cache headers = %#v", rec.Header())
	}
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
