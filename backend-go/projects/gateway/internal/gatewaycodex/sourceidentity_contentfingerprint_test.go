package gatewaycodex

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
)

// 来源阶梯 content_fingerprint 档（内容指纹会话身份设计 §3.2/§6.2，验收
// §9.10/§9.11 全行）。

const contentFingerprintSourceAnchoredBody = `{"messages":[` +
	`{"role":"system","content":"s"},` +
	`{"role":"user","content":"u1"},` +
	`{"role":"assistant","content":"a1"},` +
	`{"role":"user","content":"u2"}]}`

const contentFingerprintSourcePlainBody = `{"model":"m"}`

func contentFingerprintSourceRequest(rawBody string) *gatewaypreauth.GatewayRequest {
	raw := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req := gatewaypreauth.NewGatewayRequest(raw)
	if rawBody != "" {
		req.Body = &gatewaybody.Request{RawBody: []byte(rawBody)}
	}
	return req
}

func contentFingerprintSourceInput(profile string, clientIP string) GatewayClientSourceIdentityInput {
	input := GatewayClientSourceIdentityInput{
		ClientProfile:      profile,
		DownstreamProtocol: "chat_completions",
		SystemAccountID:    "sys-1",
		APIKeyID:           "key-1",
		ClientIP:           clientIP,
	}
	if profile == "codex" {
		input.ClientProfileSource = "codex_turn_metadata"
	}
	return input
}

func TestResolveGatewayClientSourceIdentityContentFingerprint(t *testing.T) {
	t.Run("generic profile with anchors produces content_fingerprint without client ip", func(t *testing.T) {
		resolver := newSourceResolver(&fakeSessionResolver{})
		anchoredReq := contentFingerprintSourceRequest(contentFingerprintSourceAnchoredBody)
		source := resolver.ResolveGatewayClientSourceIdentity(anchoredReq,
			contentFingerprintSourceInput("generic_openai", "203.0.113.7"))
		// 零完整物化断言（设计验收 §9.9）：来源阶梯只读 RawBody，body 保持
		// 未物化（Body 为 nil），零 ParseRequestJSONBody。
		if anchoredReq.Body == nil || anchoredReq.Body.Body != nil {
			t.Fatalf("来源阶梯触发了 body 物化：Body=%v", anchoredReq.Body.Body)
		}
		if source.Status != SourceStatusResolved || source.Kind != SourceKindContentFingerprint {
			t.Fatalf("source = %+v", source)
		}
		if !strings.HasPrefix(source.SourceKey, "src_v1_") {
			t.Fatalf("sourceKey = %q，want src_v1_ 前缀（复用既有 HMAC 布局）", source.SourceKey)
		}
		if source.SemanticNamespace != gatewaybody.ContentFingerprintNamespace {
			t.Fatalf("semanticNamespace = %q", source.SemanticNamespace)
		}
		if source.AffinityKey != "" {
			t.Fatalf("affinityKey = %q，want 空（亲和确认档由 adapter 侧产出，不经来源档）", source.AffinityKey)
		}
		// 输入不含 clientIp：同锚点不同出口 IP → 同来源键（跨 NAT 天然隔离）。
		otherIP := resolver.ResolveGatewayClientSourceIdentity(
			contentFingerprintSourceRequest(contentFingerprintSourceAnchoredBody),
			contentFingerprintSourceInput("generic_openai", "198.51.100.9"))
		if otherIP.SourceKey != source.SourceKey {
			t.Fatalf("sourceKey = %q vs %q，want 同锚点同键（clientIp 不入输入）", otherIP.SourceKey, source.SourceKey)
		}
	})

	t.Run("official session evidence suppresses the fingerprint tier", func(t *testing.T) {
		resolver := newSourceResolver(&fakeSessionResolver{identity: gatewaysession.GatewaySessionIdentity{
			Status:            gatewaysession.IdentityStatusResolved,
			SessionID:         "sess-1",
			ConversationKey:   "conv-1",
			SemanticNamespace: "com.openai.codex.session",
		}})
		source := resolver.ResolveGatewayClientSourceIdentity(
			contentFingerprintSourceRequest(contentFingerprintSourceAnchoredBody),
			contentFingerprintSourceInput("codex", "203.0.113.7"))
		if source.Kind != SourceKindOfficialSession {
			t.Fatalf("kind = %q，want official_session", source.Kind)
		}
	})

	t.Run("protocol resource evidence suppresses the fingerprint tier", func(t *testing.T) {
		resolver := newSourceResolver(&fakeSessionResolver{})
		resolver.GeminiInteractionResourceID = func(*gatewaypreauth.GatewayRequest) string { return "interaction-1" }
		input := contentFingerprintSourceInput("gemini_cli", "203.0.113.7")
		input.DownstreamProtocol = "gemini_interactions"
		source := resolver.ResolveGatewayClientSourceIdentity(
			contentFingerprintSourceRequest(contentFingerprintSourceAnchoredBody), input)
		if source.Kind != SourceKindProtocolResource {
			t.Fatalf("kind = %q，want protocol_resource", source.Kind)
		}
	})

	t.Run("official resolver missing falls through to the fingerprint tier", func(t *testing.T) {
		resolver := newSourceResolver(&fakeSessionResolver{identity: gatewaysession.GatewaySessionIdentity{
			Status: gatewaysession.IdentityStatusMissing,
		}})
		source := resolver.ResolveGatewayClientSourceIdentity(
			contentFingerprintSourceRequest(contentFingerprintSourceAnchoredBody),
			contentFingerprintSourceInput("codex", "203.0.113.7"))
		if source.Kind != SourceKindContentFingerprint {
			t.Fatalf("kind = %q，want content_fingerprint", source.Kind)
		}
	})

	t.Run("invalid and conflict stop all fine-grained sources", func(t *testing.T) {
		for name, status := range map[string]gatewaysession.IdentityStatus{
			"invalid":  gatewaysession.IdentityStatusInvalid,
			"conflict": gatewaysession.IdentityStatusConflict,
		} {
			resolver := newSourceResolver(&fakeSessionResolver{identity: gatewaysession.GatewaySessionIdentity{Status: gatewaysession.IdentityStatus(status)}})
			source := resolver.ResolveGatewayClientSourceIdentity(
				contentFingerprintSourceRequest(contentFingerprintSourceAnchoredBody),
				contentFingerprintSourceInput("codex", "203.0.113.7"))
			if source.Status != GatewayClientSourceStatus(status) || source.SourceKey != "" {
				t.Fatalf("%s 臂 source = %+v，want 无任何来源键", name, source)
			}
			if source.Kind == SourceKindContentFingerprint || source.Kind == SourceKindIPAPIKeyFallback {
				t.Fatalf("%s 臂 kind = %q，want 无来源", name, source.Kind)
			}
		}
	})

	t.Run("no anchors keeps the ip fallback", func(t *testing.T) {
		resolver := newSourceResolver(&fakeSessionResolver{})
		for name, tc := range map[string]struct {
			req  *gatewaypreauth.GatewayRequest
			want string
		}{
			"no messages": {contentFingerprintSourceRequest(contentFingerprintSourcePlainBody), SourceKindIPAPIKeyFallback},
			"nil body":    {contentFingerprintSourceRequest(""), SourceKindIPAPIKeyFallback},
		} {
			source := resolver.ResolveGatewayClientSourceIdentity(tc.req, contentFingerprintSourceInput("generic_openai", "203.0.113.7"))
			if source.Kind != tc.want {
				t.Fatalf("%s 臂 kind = %q，want %q", name, source.Kind, tc.want)
			}
		}
	})

	t.Run("no anchors and unknown client ip stays missing", func(t *testing.T) {
		resolver := newSourceResolver(&fakeSessionResolver{})
		source := resolver.ResolveGatewayClientSourceIdentity(
			contentFingerprintSourceRequest(contentFingerprintSourcePlainBody),
			contentFingerprintSourceInput("generic_openai", ""))
		if source.Status != SourceStatusMissing || source.SourceKey != "" {
			t.Fatalf("source = %+v，want missing", source)
		}
	})
}

// TestResolveGatewayClientSourceIdentityContentFingerprintIsolation 验收
// §9.11：同 clientIp + apiKeyId 下不同锚点会话互不连带；跨系统账户同内容键
// 不同；同会话跨请求键稳定。
func TestResolveGatewayClientSourceIdentityContentFingerprintIsolation(t *testing.T) {
	resolver := newSourceResolver(&fakeSessionResolver{})
	// 临时档锚点是 {S,U1}：两条会话的 U1 必须真不同（§8.1 显式接受同 S+U1
	// 会话在来源档过共享——那是正确行为而非连带）。
	sessionOne := `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"},{"role":"assistant","content":"a1"},{"role":"user","content":"u2"}]}`
	sessionTwo := `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"completely different first question"},{"role":"assistant","content":"a1"},{"role":"user","content":"u2"}]}`

	first := resolver.ResolveGatewayClientSourceIdentity(
		contentFingerprintSourceRequest(sessionOne), contentFingerprintSourceInput("generic_openai", "203.0.113.7"))
	second := resolver.ResolveGatewayClientSourceIdentity(
		contentFingerprintSourceRequest(sessionTwo), contentFingerprintSourceInput("generic_openai", "203.0.113.7"))
	if first.Kind != SourceKindContentFingerprint || second.Kind != SourceKindContentFingerprint {
		t.Fatalf("kinds = %q / %q", first.Kind, second.Kind)
	}
	if first.SourceKey == second.SourceKey {
		t.Fatal("同 IP 同 Key 下不同锚点会话的来源键必须互不连带")
	}

	// 同会话跨请求（含中间穿插的重发）键稳定。
	again := resolver.ResolveGatewayClientSourceIdentity(
		contentFingerprintSourceRequest(sessionOne), contentFingerprintSourceInput("generic_openai", "203.0.113.7"))
	if again.SourceKey != first.SourceKey {
		t.Fatal("同会话来源键必须跨请求稳定")
	}

	// 跨系统账户同内容 → 不同键。
	otherAccount := contentFingerprintSourceInput("generic_openai", "203.0.113.7")
	otherAccount.SystemAccountID = "sys-2"
	other := resolver.ResolveGatewayClientSourceIdentity(contentFingerprintSourceRequest(sessionOne), otherAccount)
	if other.SourceKey == first.SourceKey {
		t.Fatal("跨系统账户同内容会话的来源键必须不同")
	}
}

// TestResolveGatewayClientSourceIdentityContentFingerprintMemoization 阶梯与
// 会话身份接缝共享请求级记忆化：第二次解析不重复扫描。
func TestResolveGatewayClientSourceIdentityContentFingerprintMemoization(t *testing.T) {
	resolver := newSourceResolver(&fakeSessionResolver{})
	req := contentFingerprintSourceRequest(contentFingerprintSourceAnchoredBody)
	first := resolver.ResolveGatewayClientSourceIdentity(req, contentFingerprintSourceInput("generic_openai", "203.0.113.7"))
	// 改写 RawBody：若重复扫描，来源键会随新锚点变化。
	req.Body = &gatewaybody.Request{RawBody: []byte(`{"messages":[{"role":"user","content":"rewritten"}]}`)}
	second := resolver.ResolveGatewayClientSourceIdentity(req, contentFingerprintSourceInput("generic_openai", "203.0.113.7"))
	if first.SourceKey != second.SourceKey || first.Kind != second.Kind {
		t.Fatalf("记忆化失效：first %+v second %+v", first, second)
	}
}
