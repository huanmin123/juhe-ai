package proxylatency

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestWFDecryptProxyPasswordSubCases(t *testing.T) {
	secret := "wf-micro"
	valid := wfSealPassword(t, secret, "pw")
	parts := strings.Split(valid, ":")
	// ciphertext 为空（空串 base64 解码为零字节 → GCM open 失败）。
	emptyCT := "v1:" + parts[1] + ":" + parts[2] + ":"
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: emptyCT}); err == nil {
		t.Fatal("空密文必须拒绝")
	}
	// 非法 JSON 载荷（用合法信封封装非 JSON 明文）。
	raw := []byte("not-json")
	sealed := wfSealRaw(t, secret, raw)
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: sealed}); err == nil {
		t.Fatal("非 JSON 载荷必须拒绝")
	}
	// JSON 但缺 password 字段。
	sealed2 := wfSealRaw(t, secret, []byte(`{"other":1}`))
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: sealed2}); err == nil {
		t.Fatal("缺 password 字段必须拒绝")
	}
	// tag 解码非法。
	badTag := "v1:" + base64.RawURLEncoding.EncodeToString([]byte("123456789012")) + ":###:" + base64.RawURLEncoding.EncodeToString([]byte("ct"))
	if _, err := decryptProxyPasswordV1(secret, CredentialEnvelope{Kind: "proxy_password", Ciphertext: badTag}); err == nil {
		t.Fatal("非法 tag 必须拒绝")
	}
}

// wfSealRaw 以 AES-256-GCM 信封加密任意明文（测试辅助）。
func wfSealRaw(t *testing.T, secret string, plaintext []byte) string {
	t.Helper()
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatalf("创建 cipher 失败: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("创建 GCM 失败: %v", err)
	}
	nonce := []byte("abcdef123456")
	sealed := gcm.Seal(nil, nonce, plaintext, nil)
	ciphertext, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	return "v1:" + base64.RawURLEncoding.EncodeToString(nonce) + ":" + base64.RawURLEncoding.EncodeToString(tag) + ":" + base64.RawURLEncoding.EncodeToString(ciphertext)
}
