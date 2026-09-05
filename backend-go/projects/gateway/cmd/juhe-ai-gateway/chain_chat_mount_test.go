package main

// G20 phase-3 chat mount test: the my-chat family answers behind the session
// middleware (401 without a session, 200 with the dev auto-login identity)
// and provisions the purpose='chat' API key on first provision (POST
// /conversations).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// composeChatTestSystemAPI composes the system api with the chain + chat
// family; devAutoLogin toggles the dev auto-login identity (with it on, every
// token-less request authenticates as the seeded admin account).
func composeChatTestSystemAPI(t *testing.T, devAutoLogin bool) (*composition, *httptest.Server) {
	t.Helper()
	cfg := composeTestConfig(t)
	cfg.ChainEnabled = true
	if devAutoLogin {
		cfg.DevAutoLoginUsername = "admin"
	}
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), openComposeOperationStore(t))
	if err != nil {
		t.Fatalf("compose system api with chat family: %v", err)
	}
	t.Cleanup(composed.Shutdown)
	seedSystemSettings(t, composed.DB)
	server := httptest.NewServer(composed.Kernel)
	t.Cleanup(server.Close)
	return composed, server
}

// TestComposeSystemAPIMountsChatFamily: 401 without a session, 200 with the
// dev auto-login identity, and the chat key lifecycle provisions exactly one
// purpose='chat' key.
func TestComposeSystemAPIMountsChatFamily(t *testing.T) {
	t.Run("401 without a session", func(t *testing.T) {
		_, server := composeChatTestSystemAPI(t, false)
		client := &http.Client{Timeout: 10 * time.Second}
		response, err := client.Get(server.URL + "/__aisys__/api/my-chat/image-policy")
		if err != nil {
			t.Fatalf("GET image-policy: %v", err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated image-policy status=%d want 401", response.StatusCode)
		}
	})

	composed, server := composeChatTestSystemAPI(t, true)
	client := &http.Client{Timeout: 10 * time.Second}

	// 200 with the dev auto-login session.
	authed, err := client.Get(server.URL + "/__aisys__/api/my-chat/image-policy")
	if err != nil {
		t.Fatalf("authenticated GET image-policy: %v", err)
	}
	defer authed.Body.Close()
	if authed.StatusCode != http.StatusOK {
		body, _ := readAllString(authed.Body)
		t.Fatalf("authenticated image-policy status=%d body=%s", authed.StatusCode, body)
	}
	var policy struct {
		Data struct {
			Input struct {
				MimeType string `json:"mimeType"`
				MaxEdge  int    `json:"maxEdge"`
			} `json:"input"`
		} `json:"data"`
	}
	if err := json.NewDecoder(authed.Body).Decode(&policy); err != nil {
		t.Fatalf("decode image-policy: %v", err)
	}
	if policy.Data.Input.MimeType != "image/webp" || policy.Data.Input.MaxEdge != 1024 {
		t.Fatalf("image policy = %+v", policy.Data.Input)
	}

	// POST /conversations provisions the purpose='chat' API key.
	provision, err := client.Post(server.URL+"/__aisys__/api/my-chat/conversations", "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST conversations: %v", err)
	}
	defer provision.Body.Close()
	body, _ := readAllString(provision.Body)
	if provision.StatusCode != http.StatusCreated {
		t.Fatalf("provision status=%d body=%s", provision.StatusCode, body)
	}

	// The chat key provider inserted exactly one purpose='chat' key with a
	// sealed plaintext (Node ensureChatApiKeyForSystemAccountAsync).
	var keyID string
	if err := composed.DB.QueryRow(
		`SELECT id FROM api_keys WHERE system_account_id = 'sys_admin' AND purpose = 'chat' LIMIT 1`,
	).Scan(&keyID); err != nil {
		t.Fatalf("query chat api key: %v", err)
	}
	var count int
	if err := composed.DB.QueryRow(
		`SELECT COUNT(*) FROM api_keys WHERE system_account_id = 'sys_admin' AND purpose = 'chat'`,
	).Scan(&count); err != nil {
		t.Fatalf("count chat api keys: %v", err)
	}
	if count != 1 {
		t.Fatalf("chat api key count = %d", count)
	}
	var sealed string
	if err := composed.DB.QueryRow(`SELECT key_secret_encrypted FROM api_keys WHERE id = ?`, keyID).Scan(&sealed); err != nil {
		t.Fatalf("query sealed secret: %v", err)
	}
	var envelope struct {
		Key string `json:"key"`
	}
	if err := apikeys.DecryptJSON("compose-test-secret", sealed, &envelope); err != nil || !strings.HasPrefix(envelope.Key, "sk-") {
		t.Fatalf("sealed chat key round-trip failed: %v %q", err, envelope.Key)
	}

	// Second provision reuses the same key (Node chatApiKeyIdForSystemAccount).
	second, err := client.Post(server.URL+"/__aisys__/api/my-chat/conversations", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusCreated {
		body, _ := readAllString(second.Body)
		t.Fatalf("second provision status=%d body=%s", second.StatusCode, body)
	}
	if err := composed.DB.QueryRow(
		`SELECT COUNT(*) FROM api_keys WHERE system_account_id = 'sys_admin' AND purpose = 'chat'`,
	).Scan(&count); err != nil {
		t.Fatalf("recount chat api keys: %v", err)
	}
	if count != 1 {
		t.Fatalf("chat api key count after second provision = %d", count)
	}
	_ = context.Background
}

// readAllString drains a response body (small helper for error paths).
func readAllString(body interface{ Read([]byte) (int, error) }) (string, error) {
	builder := &strings.Builder{}
	buf := make([]byte, 4096)
	for {
		n, err := body.Read(buf)
		builder.Write(buf[:n])
		if err != nil {
			return builder.String(), nil
		}
	}
}
