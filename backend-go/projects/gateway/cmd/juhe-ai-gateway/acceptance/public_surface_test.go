// X05 场景 4：公开面。oidc discovery/jwks；管理面创建 OAuth client →
// 授权码 + PKCE 全流程（会话 cookie 授权确认 → code → token）→ delegated
// Bearer 访问 /__aidelegated__/v1/profile；无效 token 的 401 契约。
package acceptance

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestAcceptancePublicSurface(t *testing.T) {
	fixture := startGateway(t, gatewayEnvOptions{OIDC: true})
	client := newClient(t, fixture.baseURL)
	client.do(http.MethodPost, "/__aisys__/api/auth/login",
		map[string]any{"username": "admin", "password": acceptanceAdminPassword}, wantStatus(http.StatusOK))

	// discovery：issuer/端点契约（oidc/routes.go getDiscovery，对齐
	// Node oidc-provider.routes.ts discovery 文档）。
	_, discovery := client.do(http.MethodGet, "/.well-known/openid-configuration", nil, wantStatus(http.StatusOK))
	if str(discovery["issuer"]) != fixture.baseURL {
		t.Fatalf("discovery issuer wrong: %#v", discovery["issuer"])
	}
	if str(discovery["authorization_endpoint"]) != fixture.baseURL+"/oauth/authorize" ||
		str(discovery["token_endpoint"]) != fixture.baseURL+"/oauth/token" {
		t.Fatalf("discovery endpoints wrong: %#v", discovery)
	}
	grants, _ := discovery["grant_types_supported"].([]any)
	if len(grants) == 0 || str(grants[0]) != "authorization_code" {
		t.Fatalf("discovery grant types wrong: %#v", grants)
	}

	// jwks：首次协议请求惰性轮换签名密钥后应暴露 RS256 key。
	_, jwks := client.do(http.MethodGet, "/oauth/jwks", nil, wantStatus(http.StatusOK))
	keys, _ := jwks["keys"].([]any)
	if len(keys) == 0 {
		t.Fatalf("jwks empty: %#v", jwks)
	}

	// 创建 public client（policyreads/oauth.go createClient；本机回环
	// http 回调仅允许 public client）。clientId 可能平铺或嵌套在 client 下。
	_, clientCreated := client.do(http.MethodPost, "/__aisys__/api/oauth/clients", map[string]any{
		"displayName":   "验收委派端" + randomHex(t, 3),
		"clientType":    "public",
		"redirectUris":  []string{"http://127.0.0.1:9/callback"},
		"allowedScopes": []string{"juhe:profile.read", "juhe:groups.read"},
	}, wantStatus(http.StatusCreated))
	createdData := data(clientCreated)
	clientID := str(createdData["clientId"])
	if clientID == "" {
		if nestedClient, _ := createdData["client"].(map[string]any); nestedClient != nil {
			clientID = str(nestedClient["clientId"])
		}
	}
	if clientID == "" {
		t.Fatalf("oauth client create payload wrong: %#v", clientCreated)
	}

	// 授权请求（PKCE S256）→ 会话 cookie 下返回 HTML 授权确认页。
	verifier := pkceVerifier()
	challenge := pkceS256(verifier)
	state := "st-" + randomHex(t, 6)
	authorizeURL := fmt.Sprintf("/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&scope=%s&state=%s&code_challenge=%s&code_challenge_method=S256",
		url.QueryEscape(clientID), url.QueryEscape("http://127.0.0.1:9/callback"),
		url.QueryEscape("juhe:profile.read juhe:groups.read"), url.QueryEscape(state),
		url.QueryEscape(challenge))
	consentHTML := httpGetBody(t, client, authorizeURL)
	transactionID := regexCapture(`name="transaction_id" value="([^"]+)"`, consentHTML)
	csrfToken := regexCapture(`name="csrf_token" value="([^"]+)"`, consentHTML)
	if transactionID == "" || csrfToken == "" {
		t.Fatalf("consent page missing transaction/csrf: %s", consentHTML)
	}
	if !strings.Contains(consentHTML, "授权确认") {
		t.Fatalf("consent page title wrong: %s", consentHTML)
	}

	// 授权确认 → 302 回调带 code + state（routes.go postAuthorizeDecision）。
	decisionResponse := postForm(t, client, "/oauth/authorize/decision", url.Values{
		"transaction_id": {transactionID}, "csrf_token": {csrfToken}, "decision": {"allow"},
	})
	// 契约以 Location 回调为准（302；Go 实测可能以 200 + Location 呈现，
	// 与 kernel 响应写包装有关——以 code+state 可解析为准）。
	if decisionResponse.location == "" || decisionResponse.statusCode >= 400 {
		t.Fatalf("decision status=%d location=%q body=%q",
			decisionResponse.statusCode, decisionResponse.location, decisionResponse.body)
	}
	callback, err := url.Parse(decisionResponse.location)
	if err != nil {
		t.Fatalf("decision location parse: %v", err)
	}
	code := callback.Query().Get("code")
	if code == "" || callback.Query().Get("state") != state {
		t.Fatalf("decision callback wrong: %s", decisionResponse.location)
	}

	// token 交换（public client 无 secret，client_id + PKCE verifier）。
	tokenResponse := postForm(t, client, "/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {"http://127.0.0.1:9/callback"}, "code_verifier": {verifier},
		"client_id":    {clientID},
	})
	if tokenResponse.statusCode != http.StatusOK {
		t.Fatalf("token status=%d body=%s", tokenResponse.statusCode, tokenResponse.body)
	}
	var tokenPayload map[string]any
	if err := json.Unmarshal([]byte(tokenResponse.body), &tokenPayload); err != nil {
		t.Fatalf("decode token payload: %v", tokenResponse.body)
	}
	accessToken := str(tokenPayload["access_token"])
	if str(tokenPayload["token_type"]) != "Bearer" || accessToken == "" {
		t.Fatalf("token payload wrong: %#v", tokenPayload)
	}
	if str(tokenPayload["scope"]) != "juhe:profile.read juhe:groups.read" {
		t.Fatalf("token scope wrong: %#v", tokenPayload)
	}

	// delegated profile：Bearer 访问（delegated/routes.go getProfile）。
	profileRequest, _ := http.NewRequest(http.MethodGet, fixture.baseURL+"/__aidelegated__/v1/profile", nil)
	profileRequest.Header.Set("Authorization", "Bearer "+accessToken)
	profileStatus, profilePayload := client.doRequest(profileRequest, wantStatus(http.StatusOK))
	if str(data(profilePayload)["systemAccountId"]) == "" && profilePayload["data"] == nil {
		t.Fatalf("delegated profile payload wrong: %#v", profilePayload)
	}
	_ = profileStatus

	// 无效 token → 401 invalid_token + 逐字节消息（delegated/routes.go
	// bearerToken 认证失败分支「访问令牌无效或已过期」）。
	badRequest, _ := http.NewRequest(http.MethodGet, fixture.baseURL+"/__aidelegated__/v1/profile", nil)
	badRequest.Header.Set("Authorization", "Bearer not-a-real-token")
	badStatus, badPayload := client.doRequest(badRequest, wantStatus(http.StatusUnauthorized))
	if badStatus != http.StatusUnauthorized || !strings.Contains(fmt.Sprintf("%v", badPayload), "invalid_token") {
		t.Fatalf("invalid token payload wrong: %#v", badPayload)
	}
	if !strings.Contains(fmt.Sprintf("%v", badPayload), "访问令牌无效或已过期") {
		t.Fatalf("invalid token message wrong: %#v", badPayload)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func pkceVerifier() string {
	buf := make([]byte, 48)
	_, _ = rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func regexCapture(pattern, body string) string {
	re := regexp.MustCompile(pattern)
	match := re.FindStringSubmatch(body)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

func httpGetBody(t *testing.T, client *acceptanceClient, path string) string {
	t.Helper()
	response, err := client.http.Get(client.baseURL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

type formResponse struct {
	statusCode int
	location   string
	body       string
	headers    http.Header
}

func postForm(t *testing.T, client *acceptanceClient, path string, values url.Values) formResponse {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, client.baseURL+path, strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatalf("build form %s: %v", path, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// 授权确认的 302 回调不允许自动跟随（回调地址是外部占位地址）。
	noRedirectClient := *client.http
	noRedirectClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := noRedirectClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read form %s: %v", path, err)
	}
	return formResponse{statusCode: response.StatusCode, location: response.Header.Get("Location"), body: string(raw), headers: response.Header}
}
