package policyreads

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// OAuth：创建校验矩阵补全（直接调解析层，锁定 zod 分支语义）
// ---------------------------------------------------------------------------

func TestWeOAuthClientCreateBodyBranches(t *testing.T) {
	validRedirect := `"redirectUris":["https://a.example.com/cb"]`
	validScopes := `"allowedScopes":["openid"]`
	cases := []struct {
		name    string
		body    string
		message string
	}{
		{"displayName 非字符串", `{"displayName":1,"clientType":"public",` + validRedirect + `,` + validScopes + `}`, "Expected string, received number"},
		{"displayName 空白", `{"displayName":" ","clientType":"public",` + validRedirect + `,` + validScopes + `}`, zodStringMin(1)},
		{"displayName 超长", `{"displayName":"` + strings.Repeat("字", 121) + `","clientType":"public",` + validRedirect + `,` + validScopes + `}`, zodStringMax(120)},
		{"clientType 缺失", `{"displayName":"x",` + validRedirect + `,` + validScopes + `}`, zodRequired},
		{"clientType 非字符串", `{"displayName":"x","clientType":1,` + validRedirect + `,` + validScopes + `}`, "Expected string, received number"},
		{"clientType 非法枚举", `{"displayName":"x","clientType":"hybrid",` + validRedirect + `,` + validScopes + `}`, zodEnumMessage([]string{"public", "confidential"}, "hybrid")},
		{"redirectUris 缺失", `{"displayName":"x","clientType":"public",` + validScopes + `}`, zodRequired},
		{"redirectUris 非数组", `{"displayName":"x","clientType":"public","redirectUris":"x",` + validScopes + `}`, "Expected array, received string"},
		{"redirectUris 空数组", `{"displayName":"x","clientType":"public","redirectUris":[],` + validScopes + `}`, zodArrayMin(1)},
		{"redirectUris 超 20", `{"displayName":"x","clientType":"public","redirectUris":[` + strings.TrimSuffix(strings.Repeat(`"https://a.example.com/cb",`, 21), ",") + `],` + validScopes + `}`, zodArrayMax(20)},
		{"redirectUris 项非字符串", `{"displayName":"x","clientType":"public","redirectUris":[1],` + validScopes + `}`, "Expected string, received number"},
		{"redirectUris 非法 URL", `{"displayName":"x","clientType":"public","redirectUris":["not-a-url"],` + validScopes + `}`, "Invalid url"},
		{"allowedScopes 缺失", `{"displayName":"x","clientType":"public",` + validRedirect + `}`, zodRequired},
		{"allowedScopes 非数组", `{"displayName":"x","clientType":"public",` + validRedirect + `,"allowedScopes":"x"}`, "Expected array, received string"},
		{"allowedScopes 空数组", `{"displayName":"x","clientType":"public",` + validRedirect + `,"allowedScopes":[]}`, zodArrayMin(1)},
		{"allowedScopes 超 20", `{"displayName":"x","clientType":"public",` + validRedirect + `,"allowedScopes":[` + strings.TrimSuffix(strings.Repeat(`"openid",`, 21), ",") + `]}`, zodArrayMax(20)},
		{"allowedScopes 空字符串项", `{"displayName":"x","clientType":"public",` + validRedirect + `,"allowedScopes":[""]}`, zodStringMin(1)},
		{"未知字段", `{"displayName":"x","clientType":"public",` + validRedirect + `,` + validScopes + `,"bogus":1}`, zodUnrecognizedKeys([]string{"bogus"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(tc.body), &parsed); err != nil {
				t.Fatalf("测试 JSON 非法: %v", err)
			}
			_, message := parseOAuthClientCreateBody(parsed)
			if message != tc.message {
				t.Fatalf("message = %q, want %q", message, tc.message)
			}
		})
	}
}

func TestWeOAuthScopeAndRedirectValidators(t *testing.T) {
	// scope 组合规则：profile 必须搭配 openid；写权限必须搭配对应读权限。
	if msg := validateOAuthScopes([]string{"profile"}); msg != "Client 参数或 scope 无效" {
		t.Fatalf("profile 无 openid = %s", msg)
	}
	if msg := validateOAuthScopes([]string{"openid", "profile"}); msg != "" {
		t.Fatalf("合法组合 = %s", msg)
	}
	if msg := validateOAuthScopes([]string{"juhe:profile.write"}); msg != "Client 参数或 scope 无效" {
		t.Fatalf("写无读 = %s", msg)
	}
	if msg := validateOAuthScopes([]string{"juhe:ai_accounts.write", "juhe:ai_accounts.read"}); msg != "" {
		t.Fatalf("写读配对 = %s", msg)
	}

	// 回调地址白名单分支。
	if !isAllowedRedirectURI("https://app.example.com/cb", "confidential") {
		t.Fatal("https 回调应放行")
	}
	if isAllowedRedirectURI("https:///no-host", "public") {
		t.Fatal("https 无主机应拒绝")
	}
	if isAllowedRedirectURI("https://app.example.com/cb#frag", "public") {
		t.Fatal("带 fragment 应拒绝")
	}
	if isAllowedRedirectURI("https://user@app.example.com/cb", "public") {
		t.Fatal("带 user info 应拒绝")
	}
	if !isAllowedRedirectURI("http://[::1]:9000/cb", "public") {
		t.Fatal("本机 IPv6 回环应放行")
	}
	if isAllowedRedirectURI("http://10.1.1.1/cb", "confidential") {
		t.Fatal("confidential 的 http 回调应拒绝")
	}
	if !isAllowedRedirectURI("com.example.app://oauth", "public") {
		t.Fatal("反向域名协议应放行")
	}
	if isAllowedRedirectURI("com.example.app://oauth", "confidential") {
		t.Fatal("confidential 的反向域名协议应拒绝")
	}
	if isAllowedRedirectURI("1nvalid.app://oauth", "public") {
		t.Fatal("非法反向域名应拒绝")
	}
	if !isLoopbackHostname("::1") || isLoopbackHostname("example.com") {
		t.Fatal("isLoopbackHostname 语义错误")
	}
	if !isValidURLString("https://a.example.com/x") || isValidURLString("//no-scheme") {
		t.Fatal("isValidURLString 语义错误")
	}
}

// ---------------------------------------------------------------------------
// OAuth：OIDC 启用下的 integration-package 密文守卫分支
// ---------------------------------------------------------------------------

func TestWeOAuthIntegrationPackageCiphertextGuards(t *testing.T) {
	env := newPolicyTestEnv(t)
	store := env.mountOAuth(t, true, "https://id.example.com")
	env.login(t, "root", "root-pass", "super_admin")

	// 手工播种两个 confidential client：一个密文损坏、一个密文缺失。
	now := "2026-01-01T00:00:00.000Z"
	env.exec(t, `INSERT INTO oauth_clients (id, client_id, display_name, client_type, client_secret_hash, client_secret_ciphertext, redirect_uris_json, allowed_scopes_json, status, created_at, updated_at)
		VALUES ('id-corrupt', 'juhe_corrupt', '损坏密文', 'confidential', 'hash', 'garbage', '[]', '[]', 'active', ?, ?)`, now, now)
	env.exec(t, `INSERT INTO oauth_clients (id, client_id, display_name, client_type, client_secret_hash, client_secret_ciphertext, redirect_uris_json, allowed_scopes_json, status, created_at, updated_at)
		VALUES ('id-nosecret', 'juhe_nosecret', '缺密文', 'confidential', 'hash', NULL, '[]', '[]', 'active', ?, ?)`, now, now)

	// 密文损坏 → 409 提示重新签发。
	code, corrupt, _ := env.do(t, http.MethodGet, "/__aisys__/api/oauth/clients/juhe_corrupt/integration-package", "")
	if code != http.StatusConflict || corrupt["message"] != "该 Client 的当前 Client Secret 无法读取，请重新签发后再下载对接文档" {
		t.Fatalf("corrupt: %d %v", code, corrupt)
	}
	// 密文缺失 → 409 提示先签发。
	code, noSecret, _ := env.do(t, http.MethodGet, "/__aisys__/api/oauth/clients/juhe_nosecret/integration-package", "")
	if code != http.StatusConflict || noSecret["message"] != "该 Client 没有可下载的当前 Client Secret，请先重新签发密钥后再下载对接文档" {
		t.Fatalf("no secret: %d %v", code, noSecret)
	}

	// 仓库层：缺失行返回 nil。
	if secret, err := store.FindClientSecret(nil, "juhe_missing"); err != nil || secret != nil {
		t.Fatalf("missing = %v err = %v", secret, err)
	}
	// 仓库层：损坏密文 → OidcCiphertextError。
	if _, err := store.FindClientSecret(nil, "juhe_corrupt"); err == nil {
		t.Fatal("损坏密文应报错")
	}
	// 公开 client 的 secret 读取返回 nil（FindClientSecret 的类型分支）。
	code, publicCreated, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients",
		`{"displayName":"P","clientType":"public","redirectUris":["https://p.example.com/cb"],"allowedScopes":["openid"]}`)
	publicID := dataMap(t, publicCreated)["clientId"].(string)
	if secret, err := store.FindClientSecret(nil, publicID); err != nil || secret != nil {
		t.Fatalf("public = %v err = %v", secret, err)
	}
}

// ---------------------------------------------------------------------------
// OIDC 密文编解码：格式、base64、篡改与空密钥分支
// ---------------------------------------------------------------------------

func TestWeOidcValueCryptoBranches(t *testing.T) {
	var target struct {
		ClientSecret string `json:"clientSecret"`
	}
	// 空密钥。
	if err := decryptOidcValue("", "a.b.c", &target); err == nil || !strings.Contains(err.Error(), "密钥未配置") {
		t.Fatalf("空密钥 = %v", err)
	}
	// 格式无效（段数不对或空段）。
	for _, envelope := range []string{"", "a.b", "a..c", ".b.c", "a.b.", "x.y"} {
		if err := decryptOidcValue("secret", envelope, &target); err == nil || !strings.Contains(err.Error(), "格式无效") {
			t.Fatalf("envelope %q = %v", envelope, err)
		}
	}
	// base64 非法字符。
	if err := decryptOidcValue("secret", "!!!.$$.###", &target); err == nil || !strings.Contains(err.Error(), "格式无效") {
		t.Fatalf("非法 base64 = %v", err)
	}
	// 合法 envelope 但密钥不匹配 → 无法读取。
	envelope, err := encryptOidcValue("secret-a", map[string]string{"clientSecret": "jcs_x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := decryptOidcValue("secret-b", envelope, &target); err == nil || !strings.Contains(err.Error(), "无法读取") {
		t.Fatalf("密钥不匹配 = %v", err)
	}
	// 正常往返。
	if err := decryptOidcValue("secret-a", envelope, &target); err != nil || target.ClientSecret != "jcs_x" {
		t.Fatalf("roundtrip = %+v err = %v", target, err)
	}
}
