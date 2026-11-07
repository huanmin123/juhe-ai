package gatewaydispatch

// dateline 隐写清洗出站门控单测：钉住 normalizeDatelineForAttempt 的
// anthropic 家族 + oauth 判定口径（与 chain_driver.go anthropic 出站分支
// 同镜像，NormalizeProviderToken 大小写归一化），以及 API Key 账户不清洗、
// 非 anthropic 协议不清洗的边界。

import (
	"bytes"
	"testing"
)

func TestNormalizeDatelineForAttemptGate(t *testing.T) {
	watermarked := `{"system":"Today` + "\u2019" + `s date is 2026-07-01."}`
	canonical := `{"system":"Today's date is 2026-07-01."}`

	t.Run("anthropic+oauth 清洗发生", func(t *testing.T) {
		out := normalizeDatelineForAttempt(
			AccountCandidate{ID: "acc-1", Type: "oauth", ProtocolCode: "anthropic"},
			[]byte(watermarked))
		if !bytes.Equal(out, []byte(canonical)) {
			t.Fatalf("出站 body 未归一化: %s", out)
		}
	})

	t.Run("anthropic+api_key 不清洗", func(t *testing.T) {
		body := []byte(watermarked)
		out := normalizeDatelineForAttempt(
			AccountCandidate{ID: "acc-2", Type: "api_key", ProtocolCode: "anthropic"},
			body)
		if !bytes.Equal(out, body) {
			t.Fatalf("API Key 账户 body 被改写: %s", out)
		}
	})

	t.Run("非 anthropic+oauth 不清洗", func(t *testing.T) {
		body := []byte(watermarked)
		out := normalizeDatelineForAttempt(
			AccountCandidate{ID: "acc-3", Type: "oauth", ProtocolCode: "gemini"},
			body)
		if !bytes.Equal(out, body) {
			t.Fatalf("gemini oauth body 被改写: %s", out)
		}
	})

	t.Run("protocol 大小写混合仍命中", func(t *testing.T) {
		out := normalizeDatelineForAttempt(
			AccountCandidate{ID: "acc-4", Type: "oauth", ProtocolCode: "Anthropic"},
			[]byte(watermarked))
		if !bytes.Equal(out, []byte(canonical)) {
			t.Fatalf("大小写混合 protocol 未命中清洗: %s", out)
		}
	})
}
