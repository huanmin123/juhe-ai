package main

// G20 phase-3 chat mount test: the my-chat family answers behind the session
// middleware (401 without a session, 200 with the dev auto-login identity);
// 免 Key 直调（W-B）后 provision 不再创建任何 API Key——断言两次 provision 均
// 201 且库中不存在 purpose='chat' 键。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

// composeChatTestSystemAPI composes the system api with the chain + chat
// family; devAutoLogin toggles the dev auto-login identity (with it on, every
// token-less request authenticates as the seeded admin account).
func composeChatTestSystemAPI(t *testing.T, devAutoLogin bool) (*composition, *httptest.Server) {
	t.Helper()
	tdb := composeTestPostgres(t)
	cfg := composeTestConfigForDSN(t, tdb.DSN)
	cfg.ChainEnabled = true
	if devAutoLogin {
		cfg.DevAutoLoginUsername = "admin"
	}
	store := openComposeOperationStore(t, tdb.DSN)
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), mustComposeAuditProducer(t, tdb.DSN), composeTestAuditConfig(t, tdb.DSN), composeTestOwnerHealth())
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

	// POST /conversations 免请求体直进空会话（账户唯一绑定契约）：任何请求体
	// 内容都被服务端忽略；免 Key 直调后不再创建任何 API Key。
	provision, err := client.Post(server.URL+"/__aisys__/api/my-chat/conversations", "application/json",
		strings.NewReader(`{"bindMode":"group","groupId":"legacy-ignored"}`))
	if err != nil {
		t.Fatalf("POST conversations: %v", err)
	}
	defer provision.Body.Close()
	body, _ := readAllString(provision.Body)
	if provision.StatusCode != http.StatusCreated {
		t.Fatalf("provision status=%d body=%s", provision.StatusCode, body)
	}

	// 免 Key 直调：库中不得出现 purpose='chat' 键（供给链已删除）。
	var count int
	if err := composed.DB.QueryRow(
		`SELECT COUNT(*) FROM juhe_business.api_keys WHERE system_account_id = 'sys_admin' AND purpose = 'chat'`,
	).Scan(&count); err != nil {
		t.Fatalf("count chat api keys: %v", err)
	}
	if count != 0 {
		t.Fatalf("chat api key count = %d, want 0（不再创建对话 Key）", count)
	}

	// Second provision 照常 201，且仍无 chat 键。
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
		`SELECT COUNT(*) FROM juhe_business.api_keys WHERE system_account_id = 'sys_admin' AND purpose = 'chat'`,
	).Scan(&count); err != nil {
		t.Fatalf("recount chat api keys: %v", err)
	}
	if count != 0 {
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
