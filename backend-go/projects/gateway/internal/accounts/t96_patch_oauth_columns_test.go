package accounts

import (
	"net/http"
	"strings"
	"testing"
)

// T96-③：accounts PATCH 凭据重封必须同步 OAuth 派生列
// （oauth_access_token_expires_at / oauth_refresh_token_present）——刷新调度按
// 列扫描（jobs store.go:115-118），缺列使编辑后的新过期时间对调度不可见。
func TestT96PatchResealSyncsOAuthDerivedColumns(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		`{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"t96-oauth","type":"oauth",
		"credentials":{"access_token":"at-t96","refresh_token":"rt-t96","expires_at":"2030-01-01T00:00:00Z","account_id":"acc-t96","base_url":"https://api.openai.com/v1"},
		"supportedModels":["gpt-4o-mini"],"connections":[{"connectionPresetCode":"gpt_openai_v1","baseUrl":"https://api.openai.com/v1","endpointModes":["responses_json","responses_sse"],"enabled":true}],"healthCheckConnectionPresetCode":"gpt_openai_v1","healthCheckEndpointMode":"responses_json","healthCheckModel":"gpt-4o-mini","status":"active"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)
	revision := dataMap(t, created)["configRevision"].(float64)

	if got := env.queryCell(t, `SELECT oauth_refresh_token_present FROM accounts WHERE id = ?`, id); got != "1" {
		t.Fatalf("create refresh present: %q", got)
	}

	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":`+floatText(revision)+`,"credentialsPatch":{"expires_at":"2031-06-01T00:00:00Z"}}`)
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, patched)
	}

	expires := env.queryCell(t, `SELECT oauth_access_token_expires_at FROM accounts WHERE id = ?`, id)
	if !strings.Contains(expires, "2031-06-01") {
		t.Fatalf("patched expiry must reach the scheduling column, got %q", expires)
	}
	if got := env.queryCell(t, `SELECT oauth_refresh_token_present FROM accounts WHERE id = ?`, id); got != "1" {
		t.Fatalf("patched refresh present: %q", got)
	}
}
