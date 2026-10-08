// 本文件由 jobs internal/accounthealth/w12d_units_test.go 拆分而来（成对关系见 probe.go 包注释）：
// 承载随探针执行器闭包下沉的被移函数测试；同源文件的留守测试仍在 jobs 包内，
// 两侧不重复、不丢失。
package exactkeyprobe

import (
	"strings"
	"testing"
	"time"
)

// TestW12dDecryptV1EnvelopeBadBase64Arms 覆盖 envelope tag/ciphertext 解码失败臂。
func TestW12dDecryptV1EnvelopeBadBase64Arms(t *testing.T) {
	if _, err := DecryptV1Envelope("s", "v1:!!!:AAAA:AAAA"); err == nil || !strings.Contains(err.Error(), "解码凭据 envelope 失败") {
		t.Fatalf("bad iv: %v", err)
	}
	if _, err := DecryptV1Envelope("s", "v1:AAAAAAAAAAAAAAAA:!!!:AAAA"); err == nil || !strings.Contains(err.Error(), "解码凭据 envelope 失败") {
		t.Fatalf("bad tag: %v", err)
	}
	if _, err := DecryptV1Envelope("s", "v1:AAAAAAAAAAAAAAAA:AAAAAAAAAAAAAAAA:!!!"); err == nil || !strings.Contains(err.Error(), "解码凭据 envelope 失败") {
		t.Fatalf("bad ciphertext: %v", err)
	}
	if _, err := DecryptV1Envelope("s", "v1:AAAAAAAAAAAAAAAA:AAAAAAAAAAAAAAAA:AAAAAAAAAAAAAAAA"); err == nil || !strings.Contains(err.Error(), "IV 或 tag 长度无效") {
		t.Fatalf("bad lengths: %v", err)
	}
}

// TestW12dDirectInputToInputArms 表驱动覆盖 DirectInput.ToInput 的错误臂。
func TestW12dDirectInputToInputArms(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	secret := "w12d-direct-secret"
	envelope := testEnvelope(t, secret, `{"api_key":"sk-w12d"}`)
	valid := DirectInput{
		Account: DirectAccount{
			ID: "w12d-din", ConfigRevision: 5, DispatchRevision: 7, Provider: "deepseek",
			ProtocolProfileID: "profile_deepseek_openai_v1", ProtocolCode: "openai", ProtocolVersion: "v1",
			Type: "api_key", Status: "active", Schedulable: true, EndpointMode: "chat_json",
			HealthModel: "w12d-model", CredentialsEncrypted: envelope,
		},
		Binding:      DirectBinding{GroupID: "w12d-group", Enabled: true},
		InputVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1",
	}
	input, err := valid.ToInput(secret, now)
	if err != nil {
		t.Fatalf("valid: %v", err)
	}
	if input.AccountID != "w12d-din" || len(input.APIKeys) != 1 {
		t.Fatalf("input=%+v", input)
	}
	cases := []struct {
		name   string
		mutate func(*DirectInput)
		frag   string
	}{
		{"missing secret", func(d *DirectInput) {}, "缺少业务凭据 secret"},
		{"binding disabled", func(d *DirectInput) { d.Binding = DirectBinding{} }, "group binding"},
		{"bad epoch", func(d *DirectInput) { d.InputVersion = 0 }, "epoch 或有效期无效"},
		{"expired", func(d *DirectInput) { d.ExpiresAt = now.Add(-time.Minute) }, "epoch 或有效期无效"},
		{"missing tls policy", func(d *DirectInput) { d.TLSPolicy = " " }, "TLS policy"},
		{"unsupported protocol metadata", func(d *DirectInput) { d.Account.ProtocolCode = "gopher" }, "protocol_code 与 profile 不一致"},
		{"empty key pool", func(d *DirectInput) {
			d.Account.CredentialsEncrypted = testEnvelope(t, secret, `{"api_key":""}`)
		}, "API Key pool 为空"},
		{"oauth token missing", func(d *DirectInput) {
			d.Account.Type = "oauth"
			d.Account.Provider = "gpt"
			d.Account.ProtocolProfileID = "profile_gpt_openai_v1"
			d.Account.EndpointMode = "responses_json"
			d.Account.CredentialsEncrypted = testEnvelope(t, secret, `{"nope":1}`)
		}, "OAuth access token 缺失"},
		{"oauth token expired", func(d *DirectInput) {
			d.Account.Type = "oauth"
			d.Account.Provider = "gpt"
			d.Account.ProtocolProfileID = "profile_gpt_openai_v1"
			d.Account.EndpointMode = "responses_json"
			d.Account.CredentialsEncrypted = testEnvelope(t, secret, `{"access_token":"at","expires_at":"2026-09-06T12:00:30Z"}`)
		}, "已到期或接近到期"},
		{"undecryptable credentials", func(d *DirectInput) {
			d.Account.CredentialsEncrypted = "not-an-envelope"
		}, "凭据"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subject := valid
			if tc.name == "missing secret" {
				if _, err := subject.ToInput("  ", now); err == nil || !strings.Contains(err.Error(), tc.frag) {
					t.Fatalf("err=%v want=%q", err, tc.frag)
				}
				return
			}
			tc.mutate(&subject)
			if _, err := subject.ToInput(secret, now); err == nil || !strings.Contains(err.Error(), tc.frag) {
				t.Fatalf("err=%v want frag=%q", err, tc.frag)
			}
		})
	}
	// OAuth happy path（access token + account id 提取）。
	oauth := valid
	oauth.Account.Type = "oauth"
	oauth.Account.Provider = "gpt"
	oauth.Account.ProtocolProfileID = "profile_gpt_openai_v1"
	oauth.Account.EndpointMode = "responses_json"
	oauth.Account.CredentialsEncrypted = testEnvelope(t, secret, `{"access_token":"at-w12d","expires_at":"2027-01-01T00:00:00Z","account_id":"acc-w12d"}`)
	input, err = oauth.ToInput(secret, now)
	if err != nil || input.OAuthAccess == nil || input.OAuthAccountID != "acc-w12d" {
		t.Fatalf("oauth input=%+v err=%v", input, err)
	}
	// Proxy envelope。
	withProxy := valid
	withProxy.Proxy = &DirectProxy{ID: "w12d-proxy", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 8080, PasswordEncrypted: testEnvelope(t, secret, "pw")}
	input, err = withProxy.ToInput(secret, now)
	if err != nil || input.Proxy == nil {
		t.Fatalf("proxy input=%+v err=%v", input, err)
	}
}
