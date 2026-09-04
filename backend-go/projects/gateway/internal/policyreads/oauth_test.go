package policyreads

import (
	"net/http"
	"strings"
	"testing"
)

func TestOAuthClientLifecycle(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountOAuth(t, true, "https://id.example.com")
	env.login(t, "root", "root-pass", "super_admin")

	// Empty list first.
	code, empty, _ := env.do(t, http.MethodGet, "/__aisys__/api/oauth/clients", "")
	if code != 200 || len(dataSlice(t, empty)) != 0 {
		t.Fatalf("initial clients: %d %v", code, empty)
	}

	// Public client: no secret material in the response.
	code, publicCreated, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients",
		`{"displayName":"CLI","clientType":"public","redirectUris":["https://app.example.com/cb"],`+
			`"allowedScopes":["openid","profile","juhe:groups.read"]}`)
	if code != http.StatusCreated {
		t.Fatalf("public create: %d %v", code, publicCreated)
	}
	publicClient := dataMap(t, publicCreated)
	if !strings.HasPrefix(publicClient["clientId"].(string), "juhe_") {
		t.Fatalf("public client: %v", publicClient)
	}
	if _, hasSecret := publicClient["clientSecret"]; hasSecret {
		t.Fatalf("public client must not carry a secret: %v", publicClient)
	}
	if _, hasHash := publicClient["clientSecretHash"]; hasHash {
		t.Fatalf("clientSecretHash must be scrubbed: %v", publicClient)
	}

	// Confidential client carries a one-time secret.
	code, confidentialCreated, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients",
		`{"displayName":"Backend","clientType":"confidential","redirectUris":["https://backend.example.com/cb"],`+
			`"allowedScopes":["juhe:api_keys.read"]}`)
	if code != http.StatusCreated {
		t.Fatalf("confidential create: %d %v", code, confidentialCreated)
	}
	confidential := dataMap(t, confidentialCreated)
	confidentialID := confidential["clientId"].(string)
	confidentialSecret := confidential["clientSecret"].(string)
	if !strings.HasPrefix(confidentialSecret, "jcs_") {
		t.Fatalf("confidential secret: %v", confidential)
	}

	// Validation matrix.
	code, missing, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients", `{"clientType":"public"}`)
	if code != http.StatusBadRequest || missing["message"] != localizedBadRequest {
		t.Fatalf("missing displayName: %d %v", code, missing)
	}
	code, badRedirect, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients",
		`{"displayName":"x","clientType":"public","redirectUris":["http://10.0.0.1:8080/cb"],`+
			`"allowedScopes":["openid"]}`)
	if code != http.StatusBadRequest || badRedirect["message"] != "回调地址必须是精确 HTTPS、反向域名协议或本机回环地址" {
		t.Fatalf("bad redirect: %d %v", code, badRedirect)
	}
	code, loopback, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients",
		`{"displayName":"Local","clientType":"public","redirectUris":["http://127.0.0.1:8321/cb"],`+
			`"allowedScopes":["juhe:groups.write","juhe:groups.read"]}`)
	if code != http.StatusCreated {
		t.Fatalf("loopback redirect: %d %v", code, loopback)
	}
	code, badScope, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients",
		`{"displayName":"x","clientType":"public","redirectUris":["https://a.example.com/cb"],`+
			`"allowedScopes":["juhe:groups.write"]}`)
	if code != http.StatusBadRequest || badScope["message"] != "Client 参数或 scope 无效" {
		t.Fatalf("write without read: %d %v", code, badScope)
	}
	code, unknownField, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients",
		`{"displayName":"x","clientType":"public","redirectUris":["https://a.example.com/cb"],`+
			`"allowedScopes":["openid"],"extra":1}`)
	if code != http.StatusBadRequest || unknownField["message"] != localizedBadRequest {
		t.Fatalf("unknown field: %d %v", code, unknownField)
	}

	// List.
	code, list, _ := env.do(t, http.MethodGet, "/__aisys__/api/oauth/clients", "")
	clients := dataSlice(t, list)
	if code != 200 || len(clients) != 3 {
		t.Fatalf("clients: %d %v", code, list)
	}
	for _, entry := range clients {
		if _, hasHash := entry.(map[string]any)["clientSecretHash"]; hasHash {
			t.Fatalf("hash must be scrubbed in list: %v", entry)
		}
	}

	// Status patch.
	code, disabled, _ := env.do(t, http.MethodPatch, "/__aisys__/api/oauth/clients/"+publicClient["clientId"].(string),
		`{"status":"disabled"}`)
	if code != 200 || dataMap(t, disabled)["status"] != "disabled" {
		t.Fatalf("disable: %d %v", code, disabled)
	}
	code, badStatus, _ := env.do(t, http.MethodPatch, "/__aisys__/api/oauth/clients/"+publicClient["clientId"].(string),
		`{"status":"gone"}`)
	if code != http.StatusBadRequest || badStatus["message"] != "Client 状态参数无效" {
		t.Fatalf("bad status: %d %v", code, badStatus)
	}
	code, unknown, _ := env.do(t, http.MethodPatch, "/__aisys__/api/oauth/clients/juhe_missing", `{"status":"active"}`)
	if code != http.StatusNotFound || unknown["message"] != "Client 不存在" {
		t.Fatalf("unknown patch: %d %v", code, unknown)
	}

	// Reissue.
	code, reissued, _ := env.do(t, http.MethodPost,
		"/__aisys__/api/oauth/clients/"+confidentialID+"/secret/reissue", "")
	if code != 200 {
		t.Fatalf("reissue: %d %v", code, reissued)
	}
	newSecret := dataMap(t, reissued)["clientSecret"].(string)
	if newSecret == confidentialSecret {
		t.Fatal("reissue must mint a new secret")
	}
	publicID := publicClient["clientId"].(string)
	code, publicReissue, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients/"+publicID+"/secret/reissue", "")
	if code != http.StatusBadRequest || publicReissue["message"] != "公开 Client 不使用 Client Secret" {
		t.Fatalf("public reissue: %d %v", code, publicReissue)
	}
	code, unknownReissue, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients/juhe_missing/secret/reissue", "")
	if code != http.StatusNotFound || unknownReissue["message"] != "Client 不存在" {
		t.Fatalf("unknown reissue: %d %v", code, unknownReissue)
	}

	// Integration package: confidential returns the decryptable secret.
	code, packageData, _ := env.do(t, http.MethodGet,
		"/__aisys__/api/oauth/clients/"+confidentialID+"/integration-package", "")
	pkg := dataMap(t, packageData)
	if code != 200 || pkg["clientSecret"] != newSecret {
		t.Fatalf("package secret: %d %v", code, packageData)
	}
	if pkg["client"].(map[string]any)["clientId"] != confidentialID {
		t.Fatalf("package client: %v", pkg)
	}
	code, publicPackage, _ := env.do(t, http.MethodGet,
		"/__aisys__/api/oauth/clients/"+publicID+"/integration-package", "")
	publicPkg := dataMap(t, publicPackage)
	if code != 200 {
		t.Fatalf("public package: %d %v", code, publicPackage)
	}
	if _, hasSecret := publicPkg["clientSecret"]; hasSecret {
		t.Fatalf("public package must omit the secret: %v", publicPkg)
	}
	code, missingPackage, _ := env.do(t, http.MethodGet, "/__aisys__/api/oauth/clients/juhe_missing/integration-package", "")
	if code != http.StatusNotFound || missingPackage["message"] != "Client 不存在" {
		t.Fatalf("missing package: %d %v", code, missingPackage)
	}

	// Integration info.
	code, info, _ := env.do(t, http.MethodGet, "/__aisys__/api/oauth/integration-info", "")
	infoData := dataMap(t, info)
	if code != 200 || infoData["issuer"] != "https://id.example.com" ||
		infoData["discoveryUrl"] != "https://id.example.com/.well-known/openid-configuration" ||
		infoData["idTokenSigningAlgorithm"] != "RS256" {
		t.Fatalf("integration info: %v", infoData)
	}
}

func TestOAuthDisabledAndPermissions(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountOAuth(t, false, "")
	env.login(t, "root", "root-pass", "super_admin")

	code, info, _ := env.do(t, http.MethodGet, "/__aisys__/api/oauth/integration-info", "")
	if code != http.StatusNotFound || info["message"] != "OIDC Provider 未启用" {
		t.Fatalf("disabled info: %d %v", code, info)
	}
	code, pkg, _ := env.do(t, http.MethodGet, "/__aisys__/api/oauth/clients/x/integration-package", "")
	if code != http.StatusConflict || pkg["message"] != "OIDC Provider 未启用，不能下载对接文档" {
		t.Fatalf("disabled package: %d %v", code, pkg)
	}
	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients",
		`{"displayName":"x","clientType":"public","redirectUris":["https://a.example.com/cb"],"allowedScopes":["openid"]}`)
	if code != http.StatusConflict || created["message"] != "OIDC Provider 未启用，不能创建 Client" {
		t.Fatalf("disabled create: %d %v", code, created)
	}
	code, reissue, _ := env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients/x/secret/reissue", "")
	if code != http.StatusConflict || reissue["message"] != "OIDC Provider 未启用，不能重新签发 Client Secret" {
		t.Fatalf("disabled reissue: %d %v", code, reissue)
	}
	// List/status patch keep working while disabled.
	code, list, _ := env.do(t, http.MethodGet, "/__aisys__/api/oauth/clients", "")
	if code != 200 || len(dataSlice(t, list)) != 0 {
		t.Fatalf("disabled list: %d %v", code, list)
	}
}

func TestOAuthAnonymousAndNonAdmin(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountOAuth(t, true, "https://id.example.com")

	code, anonymous, _ := env.do(t, http.MethodGet, "/__aisys__/api/oauth/clients", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous clients: %d %v", code, anonymous)
	}

	env.login(t, "alice", "alice-pass", "user")
	code, forbidden, _ := env.do(t, http.MethodGet, "/__aisys__/api/oauth/clients", "")
	if code != http.StatusForbidden || forbidden["message"] != "需要管理员权限" {
		t.Fatalf("user clients: %d %v", code, forbidden)
	}
	code, _, _ = env.do(t, http.MethodPost, "/__aisys__/api/oauth/clients",
		`{"displayName":"x","clientType":"public","redirectUris":["https://a.example.com/cb"],"allowedScopes":["openid"]}`)
	if code != http.StatusForbidden {
		t.Fatalf("user create: %d", code)
	}
}
