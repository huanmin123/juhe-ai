package gatewaypreauth

import (
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
)

func newMemoRequest(raw string) *GatewayRequest {
	req := NewGatewayRequest(&http.Request{})
	if raw != "" {
		req.Body = &gatewaybody.Request{RawBody: []byte(raw)}
	}
	return req
}

func TestContentFingerprintMemoFor(t *testing.T) {
	bodyWithAnchors := `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"},{"role":"assistant","content":"a1"},{"role":"user","content":"u2"}]}`
	bodyWithoutMessages := `{"model":"m"}`

	t.Run("nil body and empty raw body have no anchors", func(t *testing.T) {
		for _, req := range []*GatewayRequest{NewGatewayRequest(&http.Request{}), newMemoRequest("")} {
			memo := req.ContentFingerprintMemoFor("generic_openai", "sys", "key")
			if memo.AnchorsOK {
				t.Fatal("anchors must be unavailable")
			}
			if memo.ProvisionalHex != "" || memo.ConfirmedHex != "" || memo.ConfirmedComputable {
				t.Fatalf("unexpected memo payload: %+v", memo)
			}
		}
	})
	t.Run("body without messages has no anchors", func(t *testing.T) {
		memo := newMemoRequest(bodyWithoutMessages).ContentFingerprintMemoFor("generic_openai", "sys", "key")
		if memo.AnchorsOK {
			t.Fatal("anchors must be unavailable")
		}
	})
	t.Run("anchors produce both fingerprints and message count", func(t *testing.T) {
		memo := newMemoRequest(bodyWithAnchors).ContentFingerprintMemoFor("generic_openai", "sys", "key")
		if !memo.AnchorsOK || !memo.ConfirmedComputable {
			t.Fatalf("memo = %+v, want anchors ok and confirmed computable", memo)
		}
		if memo.MessageCount != 4 {
			t.Errorf("messageCount = %d, want 4", memo.MessageCount)
		}
		if len(memo.ProvisionalHex) != 64 || len(memo.ConfirmedHex) != 64 {
			t.Errorf("hex lengths = %d/%d, want 64/64", len(memo.ProvisionalHex), len(memo.ConfirmedHex))
		}
		if memo.Profile != "generic_openai" {
			t.Errorf("profile = %q", memo.Profile)
		}
	})
	t.Run("same profile computes once: later raw body rewrite does not rescan", func(t *testing.T) {
		req := newMemoRequest(bodyWithAnchors)
		first := req.ContentFingerprintMemoFor("generic_openai", "sys", "key")
		// 改写 RawBody：若第二次调用重新扫描，结果会随新 body 变化。
		req.Body = &gatewaybody.Request{RawBody: []byte(`{"messages":[{"role":"user","content":"different"}]}`)}
		second := req.ContentFingerprintMemoFor("generic_openai", "sys", "key")
		if first != second {
			t.Fatalf("memo recomputed: first %+v second %+v", first, second)
		}
	})
	t.Run("different profile computes separately", func(t *testing.T) {
		req := newMemoRequest(bodyWithAnchors)
		first := req.ContentFingerprintMemoFor("generic_openai", "sys", "key")
		second := req.ContentFingerprintMemoFor("cherry_studio", "sys", "key")
		if second.Profile != "cherry_studio" {
			t.Fatalf("profile = %q", second.Profile)
		}
		if first.ProvisionalHex == second.ProvisionalHex {
			t.Fatal("profile is a fingerprint input; codes must differ")
		}
		// 之后再读第一个 profile：命中各自记忆化。
		if again := req.ContentFingerprintMemoFor("generic_openai", "sys", "key"); again != first {
			t.Fatal("first profile memo must stay cached")
		}
	})
	t.Run("truncated body fails closed", func(t *testing.T) {
		memo := newMemoRequest(`{"messages":[{"role":"user","cont`).ContentFingerprintMemoFor("generic_openai", "sys", "key")
		if memo.AnchorsOK {
			t.Fatal("anchors must be unavailable for truncated body")
		}
	})
}
