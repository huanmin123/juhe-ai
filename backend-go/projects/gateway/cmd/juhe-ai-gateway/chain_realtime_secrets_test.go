package main

// M5b realtime ephemeral token 签发端点链级测试（Realtime 设计 §2/§4）：
// mergeComposeChain（chainSmokeDeps 先例）+ miniredis 真 go-redis 客户端，
// 覆盖：签发 200 形态与 token 可校验、缺 model 400、非 POST 404、
// 认证失败 401、服务未装配 503。
import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/realtimetoken"
)

// realtimeSecretsCompose 组装带 miniredis token 服务的链，返回链、API Key
// 明文与 token 服务（断言校验往返用）。M5b2 起签发前校验 model 在 realtime
// 目录（chain_realtime_secrets.go），fixture 同步种 gpt-realtime 目录行。
func realtimeSecretsCompose(t *testing.T, withRedis bool) (*gatewayChain, string, *realtimetoken.Service) {
	t.Helper()
	fixture := newChainFixture(t)
	seedRealtimeCatalogRow(t, fixture.db)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	var tokens *realtimetoken.Service
	if withRedis {
		tokens = realtimetoken.NewService(realtimetoken.GoRedisClient{Client: client}, "")
	}
	chain, _, shutdown := mergeComposeChain(t, fixture, func(deps *chainRuntimeDeps) {
		deps.RealtimeTokens = tokens
	})
	t.Cleanup(shutdown)
	return chain, fixture.apiKeySecret, tokens
}

func realtimeSecretsRequest(t *testing.T, server *httptest.Server, method, target, apiKey, body string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, server.URL+target, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	payload, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	return response, string(payload)
}

func TestChainRealtimeClientSecretsIssuesOpenAIShapeToken(t *testing.T) {
	chain, apiKey, tokens := realtimeSecretsCompose(t, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := realtimeSecretsRequest(t, server, http.MethodPost, "/v1/realtime/client_secrets", apiKey, `{"model":"gpt-realtime"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, payload)
	}
	var issued struct {
		Value     string `json:"value"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(payload), &issued); err != nil {
		t.Fatalf("decode response: %v (%s)", err, payload)
	}
	if len(issued.Value) != 43 {
		t.Fatalf("value shape = %q want 43-char base64url", issued.Value)
	}
	if issued.ExpiresAt <= time.Now().Unix() || issued.ExpiresAt > time.Now().Add(realtimetoken.TTL+time.Second).Unix() {
		t.Fatalf("expires_at = %d want now+120s", issued.ExpiresAt)
	}
	// token 可校验且绑定签发身份与模型（M5b2 WS 升级面消费的凭据形态）。
	verified, err := tokens.Verify(context.Background(), issued.Value, "gpt-realtime")
	if err != nil {
		t.Fatalf("verify issued token: %v", err)
	}
	if verified.Model != "gpt-realtime" {
		t.Fatalf("verified model = %q", verified.Model)
	}
}

func TestChainRealtimeClientSecretsRejectsMissingModel(t *testing.T) {
	chain, apiKey, _ := realtimeSecretsCompose(t, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := realtimeSecretsRequest(t, server, http.MethodPost, "/v1/realtime/client_secrets", apiKey, `{"voice":"alloy"}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing model status=%d body=%s", response.StatusCode, payload)
	}
	if !strings.Contains(payload, "invalid_request_body") {
		t.Fatalf("missing model body = %s", payload)
	}
	// 空白 model 同样 400。
	response, payload = realtimeSecretsRequest(t, server, http.MethodPost, "/v1/realtime/client_secrets", apiKey, `{"model":"  "}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("blank model status=%d body=%s", response.StatusCode, payload)
	}
}

func TestChainRealtimeClientSecretsNonPostIs404(t *testing.T) {
	chain, apiKey, _ := realtimeSecretsCompose(t, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		response, payload := realtimeSecretsRequest(t, server, method, "/v1/realtime/client_secrets", apiKey, "")
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", method, response.StatusCode, payload)
		}
	}
}

func TestChainRealtimeClientSecretsRequiresAPIKey(t *testing.T) {
	chain, _, _ := realtimeSecretsCompose(t, true)
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := realtimeSecretsRequest(t, server, http.MethodPost, "/v1/realtime/client_secrets", "sk-wrong-key", `{"model":"gpt-realtime"}`)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad key status=%d body=%s", response.StatusCode, payload)
	}
	response, payload = realtimeSecretsRequest(t, server, http.MethodPost, "/v1/realtime/client_secrets", "", `{"model":"gpt-realtime"}`)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no key status=%d body=%s", response.StatusCode, payload)
	}
}

func TestChainRealtimeClientSecretsDegradesWithoutRedis(t *testing.T) {
	// runtimeStateDriver!=='redis'（deps.RealtimeTokens=nil）：显式 503，
	// 不静默吞请求。
	chain, apiKey, _ := realtimeSecretsCompose(t, false)
	server := httptest.NewServer(chain)
	defer server.Close()

	response, payload := realtimeSecretsRequest(t, server, http.MethodPost, "/v1/realtime/client_secrets", apiKey, `{"model":"gpt-realtime"}`)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no-redis status=%d body=%s", response.StatusCode, payload)
	}
	if !strings.Contains(payload, "service_unavailable") {
		t.Fatalf("no-redis body = %s", payload)
	}
}
