// bug0238_oauth_reauth_context_test.go pins the BUG-0238 契约延伸 on the
// oauth-reauthorization-context surface：ai_studio 账户的 clientSecret 不再
// 返回明文，有值渲染统一密文占位，无值保持 omitempty 省略。
package accounts

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// seedBug0238GeminiAccount 落一行 gemini google_oauth 账户（非授权实例）。
func seedBug0238GeminiAccount(f *w14lFixture, id string, credentials Credentials) {
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

func TestBug0238OAuthReauthContextClientSecretMasked(t *testing.T) {
	f := newW14LFaultFixture(t)
	seedBug0238GeminiAccount(f, "acc-b0238-aistudio", Credentials{
		"oauth_type": "ai_studio", "client_id": "cid-b0238", "client_secret": "real-secret-b0238",
	})
	reauth, err := f.store.FindOAuthReauthorizationContext(context.Background(), "acc-b0238-aistudio", f.scope())
	if err != nil {
		t.Fatal(err)
	}
	if reauth == nil || reauth.OAuthType != "ai_studio" {
		t.Fatalf("ai_studio 重授权上下文应存在：%+v", reauth)
	}
	if reauth.ClientID == nil || *reauth.ClientID != "cid-b0238" {
		t.Fatalf("clientId 非敏感键应保持明文：%v", reauth.ClientID)
	}
	if reauth.ClientSecret == nil || *reauth.ClientSecret != CredentialCipherPlaceholder {
		t.Fatalf("clientSecret 应渲染统一密文占位：%v", reauth.ClientSecret)
	}
	// 序列化契约：响应体 clientSecret 为占位字面量，clientId 原样。
	encoded, err := json.Marshal(reauth)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "real-secret-b0238") {
		t.Fatalf("响应体不得包含明文 clientSecret：%s", encoded)
	}
	var rendered map[string]any
	if err := json.Unmarshal(encoded, &rendered); err != nil {
		t.Fatal(err)
	}
	if rendered["clientSecret"] != CredentialCipherPlaceholder {
		t.Fatalf("序列化后 clientSecret 应为占位：%v", rendered["clientSecret"])
	}
	if rendered["clientId"] != "cid-b0238" {
		t.Fatalf("clientId 应原样序列化：%v", rendered)
	}
}

func TestBug0238OAuthReauthContextClientSecretOmitted(t *testing.T) {
	f := newW14LFaultFixture(t)
	// ai_studio 且无 client_secret → omitempty 省略。
	seedBug0238GeminiAccount(f, "acc-b0238-nosecret", Credentials{
		"oauth_type": "ai_studio", "client_id": "cid-b0238",
	})
	reauth, err := f.store.FindOAuthReauthorizationContext(context.Background(), "acc-b0238-nosecret", f.scope())
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
	seedBug0238GeminiAccount(f, "acc-b0238-codeassist", Credentials{
		"oauth_type": "code_assist", "refresh_token": "rt-b0238",
	})
	reauth, err = f.store.FindOAuthReauthorizationContext(context.Background(), "acc-b0238-codeassist", f.scope())
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
