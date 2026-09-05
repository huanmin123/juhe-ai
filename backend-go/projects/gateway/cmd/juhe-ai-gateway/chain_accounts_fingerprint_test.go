package main

// chainFingerprintAPIKey 空 secret 契约：Node createHmac('sha256', '') 对空
// 密钥照常计算（部署未配置 JUHE_AI_SECRET 时 weighted entry id 仍是 HMAC
// 摘要，不是空串）。

import "testing"

func TestChainFingerprintAPIKeyEmptySecret(t *testing.T) {
	// 参考值：hmac.new(b'', b'sk-weighted-entry', sha256)（openssl/python 独立计算）。
	const want = "04722e2734f4c9aa0605ec90ba521c50a376eb4ff1e2cba521635b06ba3fff2d"
	if got := chainFingerprintAPIKey("", "sk-weighted-entry"); got != want {
		t.Fatalf("empty secret fingerprint = %q, want %q", got, want)
	}
	if got := chainFingerprintAPIKey("", "sk-other-key"); got == want || got == "" {
		t.Fatalf("empty secret fingerprint must stay key-specific and non-empty: %q", got)
	}
	// 非 empty secret 行为不变。
	if chainFingerprintAPIKey("secret", "key") != chainFingerprintAPIKey("secret", "key") {
		t.Fatalf("non-empty secret fingerprint not deterministic")
	}
	if chainFingerprintAPIKey("s1", "key") == chainFingerprintAPIKey("s2", "key") {
		t.Fatalf("different secrets must collide-proof fingerprints")
	}
}
