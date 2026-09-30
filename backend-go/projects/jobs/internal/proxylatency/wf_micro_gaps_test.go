package proxylatency

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestWFLoadManualAdminConfigDeadlineAndPools(t *testing.T) {
	base := map[string]string{
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED":        "true",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS": "127.0.0.1:18433",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL":   "postgres://m:x@127.0.0.1:5432/b",
	}
	// 清理批次 C4（2026-09-30）：管理面连接池 env 已收编为常量（原"上限
	// 不兼容"臂退役），本测试保留 deadline 与合法配置断言。
	// deadline 超上限。
	env2 := map[string]string{}
	for key, value := range base {
		env2[key] = value
	}
	env2["JUHE_AI_PROXY_LATENCY_MANAGEMENT_DEADLINE"] = "30s"
	if _, err := LoadManualAdminConfig(wfEnv(env2)); err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("deadline err=%v", err)
	}
	// 显式合法值。
	env3 := map[string]string{}
	for key, value := range base {
		env3[key] = value
	}
	env3["JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_MAX_OPEN_CONNS"] = "4"
	env3["JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_MAX_IDLE_CONNS"] = "2"
	env3["JUHE_AI_PROXY_LATENCY_MANAGEMENT_DEADLINE"] = "10s"
	cfg, err := LoadManualAdminConfig(wfEnv(env3))
	// 清理批次 C4：连接池 env 收编常量，仅断言 deadline 显式配置生效。
	if err != nil || cfg.MaxOpenConns != 5096 || cfg.MaxIdleConns != 10 || cfg.RequestDeadline != 10*time.Second {
		t.Fatalf("显式配置=%+v err=%v", cfg, err)
	}
}

// 清理批次 C4（2026-09-30）：原连接池上限不兼容臂已随池 env 收编退役
// （常量组合恒有效）。
func TestWFLoadRuntimeConfigInputPoolAndLimits(t *testing.T) {
	env := map[string]string{
		"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":          "go",
		"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":         "wf",
		"JUHE_AI_PROXY_LATENCY_STORE":               "postgres",
		"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":        "postgres://j:x@127.0.0.1:5432/j",
		"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":  "postgres://b:x@127.0.0.1:5432/b",
		"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": "postgres://r:x@127.0.0.1:5432/b",
		"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":   "s",
	}
	if _, err := LoadRuntimeConfig(wfEnv(env)); err != nil {
		t.Fatalf("合法配置必须通过: %v", err)
	}
}

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
