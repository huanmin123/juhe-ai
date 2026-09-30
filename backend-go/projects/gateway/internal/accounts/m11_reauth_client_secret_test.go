// m11_reauth_client_secret_test.go pins the oauth-reauthorization-context
// surface 的 clientSecret 投影语义：ai_studio 账户有值时返回真实明文
// （管理面编辑契约），无值保持 omitempty 省略；code_assist 账户不在
// ai_studio 分支，不输出 client 凭据。
package accounts

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// seedM11GeminiAccount 落一行 gemini google_oauth 账户（非授权实例）。
func seedM11GeminiAccount(f *w14lFixture, id string, credentials Credentials) {
	f.t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sealed, err := EncryptJSON(testSecret, credentials)
	if err != nil {
		f.t.Fatal(err)
	}
	f.exec(`INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES (?, ?, 'gemini', 'prof-gemini', 'gemini', 'v1', ?, 'google_oauth', 'active', ?, 'x***', '', ?, ?)`,
		id, f.owner, id, sealed, now, now)
}

func TestM11ReauthContextClientSecretPlaintext(t *testing.T) {
	f := newW14LFaultFixture(t)
	seedM11GeminiAccount(f, "acc-m11-aistudio", Credentials{
		"oauth_type": "ai_studio", "client_id": "cid-m11", "client_secret": "real-secret-m11",
	})
	reauth, err := f.store.FindOAuthReauthorizationContext(context.Background(), "acc-m11-aistudio", f.scope())
	if err != nil {
		t.Fatal(err)
	}
	if reauth == nil || reauth.OAuthType != "ai_studio" {
		t.Fatalf("ai_studio 重授权上下文应存在：%+v", reauth)
	}
	if reauth.ClientID == nil || *reauth.ClientID != "cid-m11" {
		t.Fatalf("clientId 应保持明文：%v", reauth.ClientID)
	}
	if reauth.ClientSecret == nil || *reauth.ClientSecret != "real-secret-m11" {
		t.Fatalf("clientSecret 有值时应返回真实明文：%v", reauth.ClientSecret)
	}
	// 序列化契约：响应体 clientSecret 为明文，clientId 原样。
	encoded, err := json.Marshal(reauth)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "real-secret-m11") {
		t.Fatalf("响应体应包含明文 clientSecret：%s", encoded)
	}
	var rendered map[string]any
	if err := json.Unmarshal(encoded, &rendered); err != nil {
		t.Fatal(err)
	}
	if rendered["clientSecret"] != "real-secret-m11" {
		t.Fatalf("序列化后 clientSecret 应为明文：%v", rendered["clientSecret"])
	}
	if rendered["clientId"] != "cid-m11" {
		t.Fatalf("clientId 应原样序列化：%v", rendered)
	}
}

func TestM11ReauthContextClientSecretOmitted(t *testing.T) {
	f := newW14LFaultFixture(t)
	// ai_studio 且无 client_secret → omitempty 省略。
	seedM11GeminiAccount(f, "acc-m11-nosecret", Credentials{
		"oauth_type": "ai_studio", "client_id": "cid-m11",
	})
	reauth, err := f.store.FindOAuthReauthorizationContext(context.Background(), "acc-m11-nosecret", f.scope())
	if err != nil {
		t.Fatal(err)
	}
	if reauth == nil || reauth.OAuthType != "ai_studio" {
		t.Fatalf("ai_studio 重授权上下文应存在：%+v", reauth)
	}
	if reauth.ClientSecret != nil {
		t.Fatalf("无 client_secret 应保持 nil 省略：%v", reauth.ClientSecret)
	}
	encoded, err := json.Marshal(reauth)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "clientSecret") {
		t.Fatalf("无值时 clientSecret 应整体省略：%s", encoded)
	}
	// code_assist 账户不在 ai_studio 分支，clientSecret/clientId 均不输出。
	seedM11GeminiAccount(f, "acc-m11-codeassist", Credentials{
		"oauth_type": "code_assist", "refresh_token": "rt-m11",
	})
	reauth, err = f.store.FindOAuthReauthorizationContext(context.Background(), "acc-m11-codeassist", f.scope())
	if err != nil {
		t.Fatal(err)
	}
	if reauth == nil || reauth.OAuthType != "code_assist" {
		t.Fatalf("code_assist 重授权上下文应存在：%+v", reauth)
	}
	if reauth.ClientSecret != nil || reauth.ClientID != nil {
		t.Fatalf("code_assist 不应输出 client 凭据：%+v", reauth)
	}
}
