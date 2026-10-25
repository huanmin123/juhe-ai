package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
)

// 内容指纹会话身份（docs/functions/内容指纹会话身份设计.md §3.1/§4.3）：
// sessionIdentityAdapter 的档位矩阵——官方三态与出错臂行为不变、missing 臂
// 进入指纹档（锚点可用性 × 判别器结果）、请求级记忆化恰一次、审计绑定值形状。

// spyContentFingerprintLineage 记录谱系判定调用并可控制产出。
type spyContentFingerprintLineage struct {
	calls     int
	lastHex   string
	lastCount int
	produce   bool
}

func (s *spyContentFingerprintLineage) Record(confirmedHex string, messageCount int) bool {
	s.calls++
	s.lastHex = confirmedHex
	s.lastCount = messageCount
	return s.produce
}

func contentFingerprintGatewayRequest(target string, headers map[string]string, rawBody string) *gatewaypreauth.GatewayRequest {
	raw := httptest.NewRequest(http.MethodPost, target, nil)
	for name, value := range headers {
		raw.Header.Set(name, value)
	}
	req := gatewaypreauth.NewGatewayRequest(raw)
	if rawBody != "" {
		req.Body = &gatewaybody.Request{RawBody: []byte(rawBody)}
	}
	return req
}

const contentFingerprintMultiTurnBody = `{"model":"m","messages":[` +
	`{"role":"system","content":"s"},` +
	`{"role":"user","content":"u1"},` +
	`{"role":"assistant","content":"a1"},` +
	`{"role":"user","content":"u2"}]}`

func contentFingerprintServices(t *testing.T, lineage gatewayContentFingerprintLineage) *sessionIdentityServices {
	t.Helper()
	identityService, err := gatewaysession.NewIdentityService("cfp-unit-secret")
	if err != nil {
		t.Fatalf("NewIdentityService：%v", err)
	}
	return &sessionIdentityServices{
		Identity:                  identityService,
		Affinity:                  nil,
		Secret:                    "cfp-unit-secret",
		ContentFingerprintLineage: lineage,
	}
}

// TestSessionIdentityAdapterContentFingerprintArms 官方三态回归 + missing 臂
// 指纹档矩阵。
func TestSessionIdentityAdapterContentFingerprintArms(t *testing.T) {
	genericInput := gatewaypreauth.SessionIdentityInput{ClientProfile: "generic_openai", SystemAccountID: "sys-1", APIKeyID: "key-9"}
	claudeInput := gatewaypreauth.SessionIdentityInput{ClientProfile: "claude_code", SystemAccountID: "sys-1", APIKeyID: "key-9"}

	t.Run("official resolved keeps current behavior and skips fingerprint", func(t *testing.T) {
		spy := &spyContentFingerprintLineage{produce: true}
		services := contentFingerprintServices(t, spy)
		adapter := sessionIdentityAdapter{services: services}
		req := contentFingerprintGatewayRequest("/messages", map[string]string{
			"x-claude-code-session-id": "official-session-1",
		}, contentFingerprintMultiTurnBody)
		identity := adapter.ResolveGatewaySessionIdentity(req, claudeInput)
		if identity.SessionID != "official-session-1" || identity.ConversationKey == "" {
			t.Fatalf("resolved 身份 = %+v", identity)
		}
		if !strings.HasPrefix(identity.ConversationKey, "conv_v1_") {
			t.Fatalf("resolved 会话键 = %q", identity.ConversationKey)
		}
		if spy.calls != 0 {
			t.Fatalf("官方 resolved 臂谱系判定调用 = %d，want 0", spy.calls)
		}
	})

	t.Run("official invalid keeps fail-closed empty identity", func(t *testing.T) {
		spy := &spyContentFingerprintLineage{produce: true}
		services := contentFingerprintServices(t, spy)
		adapter := sessionIdentityAdapter{services: services}
		// 控制字符触发 invalid（canonicalizer hasControlCharacter）。
		req := contentFingerprintGatewayRequest("/messages", map[string]string{
			"x-claude-code-session-id": "bad\nsession",
		}, contentFingerprintMultiTurnBody)
		identity := adapter.ResolveGatewaySessionIdentity(req, claudeInput)
		if identity.SessionID != "" || identity.ConversationKey != "" {
			t.Fatalf("invalid 臂身份 = %+v，want 空", identity)
		}
		if spy.calls != 0 {
			t.Fatalf("invalid 臂谱系判定调用 = %d，want 0", spy.calls)
		}
	})

	t.Run("official conflict keeps fail-closed empty identity", func(t *testing.T) {
		spy := &spyContentFingerprintLineage{produce: true}
		services := contentFingerprintServices(t, spy)
		adapter := sessionIdentityAdapter{services: services}
		raw := httptest.NewRequest(http.MethodPost, "/messages", nil)
		raw.Header["X-Claude-Code-Session-Id"] = []string{"session-a", "session-b"}
		req := gatewaypreauth.NewGatewayRequest(raw)
		req.Body = &gatewaybody.Request{RawBody: []byte(contentFingerprintMultiTurnBody)}
		identity := adapter.ResolveGatewaySessionIdentity(req, claudeInput)
		if identity.SessionID != "" || identity.ConversationKey != "" {
			t.Fatalf("conflict 臂身份 = %+v，want 空", identity)
		}
		if spy.calls != 0 {
			t.Fatalf("conflict 臂谱系判定调用 = %d，want 0", spy.calls)
		}
	})

	t.Run("missing without anchors keeps empty identity", func(t *testing.T) {
		spy := &spyContentFingerprintLineage{produce: true}
		services := contentFingerprintServices(t, spy)
		adapter := sessionIdentityAdapter{services: services}
		for name, req := range map[string]*gatewaypreauth.GatewayRequest{
			"nil body":     contentFingerprintGatewayRequest("/v1/chat/completions", nil, ""),
			"no messages":  contentFingerprintGatewayRequest("/v1/chat/completions", nil, `{"model":"m"}`),
			"truncated":    contentFingerprintGatewayRequest("/v1/chat/completions", nil, `{"messages":[{"role":"user"`),
			"no user role": contentFingerprintGatewayRequest("/v1/chat/completions", nil, `{"messages":[{"role":"assistant","content":"a"}]}`),
		} {
			identity := adapter.ResolveGatewaySessionIdentity(req, genericInput)
			if identity.SessionID != "" || identity.ConversationKey != "" {
				t.Fatalf("%s 臂身份 = %+v，want 空", name, identity)
			}
		}
		if spy.calls != 0 {
			t.Fatalf("无锚点臂谱系判定调用 = %d，want 0", spy.calls)
		}
	})

	t.Run("missing with first-request anchors yields audit id only", func(t *testing.T) {
		spy := &spyContentFingerprintLineage{produce: true}
		services := contentFingerprintServices(t, spy)
		adapter := sessionIdentityAdapter{services: services}
		req := contentFingerprintGatewayRequest("/v1/chat/completions", nil,
			`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"u1"}]}`)
		identity := adapter.ResolveGatewaySessionIdentity(req, genericInput)
		if !strings.HasPrefix(identity.SessionID, "cfp1-") {
			t.Fatalf("审计 SessionID = %q，want cfp1- 前缀", identity.SessionID)
		}
		if hex := strings.TrimPrefix(identity.SessionID, "cfp1-"); len(hex) != 64 {
			t.Fatalf("审计 SessionID hex 长度 = %d，want 64", len(hex))
		}
		if identity.ConversationKey != "" {
			t.Fatalf("首请求（无确认锚点）会话键 = %q，want 空", identity.ConversationKey)
		}
		if spy.calls != 0 {
			t.Fatalf("确认档不可算时谱系判定调用 = %d，want 0", spy.calls)
		}
	})

	t.Run("missing with confirmed anchors gated by lineage", func(t *testing.T) {
		produce := &spyContentFingerprintLineage{produce: true}
		services := contentFingerprintServices(t, produce)
		adapter := sessionIdentityAdapter{services: services}
		req := contentFingerprintGatewayRequest("/v1/chat/completions", nil, contentFingerprintMultiTurnBody)
		identity := adapter.ResolveGatewaySessionIdentity(req, genericInput)
		// 零完整物化断言（设计验收 §9.9）：身份接缝只读 RawBody，body 保持
		// 未物化（Body 为 nil），零 ParseRequestJSONBody。
		if req.Body == nil || req.Body.Body != nil {
			t.Fatalf("身份接缝触发了 body 物化：Body=%v", req.Body.Body)
		}
		if produce.calls != 1 {
			t.Fatalf("谱系判定调用 = %d，want 1", produce.calls)
		}
		if produce.lastCount != 4 {
			t.Fatalf("谱系判定消息数 = %d，want 4", produce.lastCount)
		}
		// 三值口径（设计 §11）：审计 SessionID = "cfp1-"+临时指纹，谱系 LRU
		// 键 = 确认指纹（含 A1/U2），两者同请求不同值。
		if !strings.HasPrefix(identity.SessionID, "cfp1-") || len(strings.TrimPrefix(identity.SessionID, "cfp1-")) != 64 {
			t.Fatalf("审计 SessionID = %q", identity.SessionID)
		}
		if len(produce.lastHex) != 64 || produce.lastHex == strings.TrimPrefix(identity.SessionID, "cfp1-") {
			t.Fatalf("谱系键 = %q，want 确认指纹（与临时指纹不同值）", produce.lastHex)
		}
		if identity.ConversationKey == "" || !strings.HasPrefix(identity.ConversationKey, "conv_v1_") {
			t.Fatalf("确认档会话键 = %q", identity.ConversationKey)
		}

		intercept := &spyContentFingerprintLineage{produce: false}
		services = contentFingerprintServices(t, intercept)
		adapter = sessionIdentityAdapter{services: services}
		identity = adapter.ResolveGatewaySessionIdentity(req, genericInput)
		if intercept.calls != 1 {
			t.Fatalf("拦截臂谱系判定调用 = %d，want 1", intercept.calls)
		}
		if identity.SessionID == "" || identity.ConversationKey != "" {
			t.Fatalf("拦截臂身份 = %+v，want 仅审计临时档", identity)
		}
	})

	t.Run("nil lineage degrades to audit-only identity", func(t *testing.T) {
		services := contentFingerprintServices(t, nil)
		adapter := sessionIdentityAdapter{services: services}
		req := contentFingerprintGatewayRequest("/v1/chat/completions", nil, contentFingerprintMultiTurnBody)
		identity := adapter.ResolveGatewaySessionIdentity(req, genericInput)
		if !strings.HasPrefix(identity.SessionID, "cfp1-") || identity.ConversationKey != "" {
			t.Fatalf("nil 判别器身份 = %+v", identity)
		}
	})

	t.Run("self-reported profile without session header enters fingerprint tier", func(t *testing.T) {
		// 设计验收 §9.6 后半：x-juhe-client-profile 自报 + 无会话 Header →
		// 进入指纹档。自报画像在 preauth 侧折算为 ClientProfile 输入；无官方
		// 会话 Header（或自报画像不适用官方会话路径）时 adapter 见到
		// IdentityStatusMissing，唯一入口进指纹档。
		spy := &spyContentFingerprintLineage{produce: true}
		services := contentFingerprintServices(t, spy)
		adapter := sessionIdentityAdapter{services: services}
		for _, profile := range []string{"codex", "claude_code", "cherry_studio"} {
			req := contentFingerprintGatewayRequest("/v1/chat/completions", nil, contentFingerprintMultiTurnBody)
			identity := adapter.ResolveGatewaySessionIdentity(req, gatewaypreauth.SessionIdentityInput{
				ClientProfile: profile, SystemAccountID: "sys-1", APIKeyID: "key-9",
			})
			if !strings.HasPrefix(identity.SessionID, "cfp1-") {
				t.Fatalf("自报画像 %q 臂 SessionID = %q，want cfp1- 前缀", profile, identity.SessionID)
			}
			if !strings.HasPrefix(identity.ConversationKey, "conv_v1_") {
				t.Fatalf("自报画像 %q 臂确认档会话键 = %q", profile, identity.ConversationKey)
			}
		}
		// 三个自报画像各自独立首见（profile 入指纹哈希）。
		if spy.calls != 3 {
			t.Fatalf("谱系判定调用 = %d，want 3", spy.calls)
		}
	})

	t.Run("conversation key isolates accounts", func(t *testing.T) {
		spy := &spyContentFingerprintLineage{produce: true}
		services := contentFingerprintServices(t, spy)
		adapter := sessionIdentityAdapter{services: services}
		// 账户隔离是跨请求语义：记忆化按 profile 键定，同一请求内账户恒定
		// （认证先于 preflight 完成），故用两个独立请求携带同内容 body。
		first := adapter.ResolveGatewaySessionIdentity(
			contentFingerprintGatewayRequest("/v1/chat/completions", nil, contentFingerprintMultiTurnBody), genericInput)
		second := adapter.ResolveGatewaySessionIdentity(
			contentFingerprintGatewayRequest("/v1/chat/completions", nil, contentFingerprintMultiTurnBody),
			gatewaypreauth.SessionIdentityInput{
				ClientProfile: "generic_openai", SystemAccountID: "sys-2", APIKeyID: "key-9",
			})
		if first.ConversationKey == "" || second.ConversationKey == "" {
			t.Fatalf("会话键 = %q / %q", first.ConversationKey, second.ConversationKey)
		}
		if first.ConversationKey == second.ConversationKey {
			t.Fatal("不同系统账户的确认档会话键必须不同")
		}
		if first.SessionID == second.SessionID {
			t.Fatal("不同系统账户的审计 id 必须不同")
		}
		if spy.calls != 2 {
			t.Fatalf("谱系判定调用 = %d，want 2（两请求各一次）", spy.calls)
		}
	})
}

// TestSessionIdentityAdapterContentFingerprintMemoization 同请求同 profile 的
// 重复解析命中记忆化：改写 RawBody 后身份不变（未重新扫描），判别器拿到的
// 消息数仍是首次计算值。
func TestSessionIdentityAdapterContentFingerprintMemoization(t *testing.T) {
	spy := &spyContentFingerprintLineage{produce: true}
	services := contentFingerprintServices(t, spy)
	adapter := sessionIdentityAdapter{services: services}
	genericInput := gatewaypreauth.SessionIdentityInput{ClientProfile: "generic_openai", SystemAccountID: "sys-1", APIKeyID: "key-9"}

	req := contentFingerprintGatewayRequest("/v1/chat/completions", nil, contentFingerprintMultiTurnBody)
	first := adapter.ResolveGatewaySessionIdentity(req, genericInput)
	// 改写 RawBody 模拟 body 变化：若第二档重扫描，审计 id 与判别器输入都会变。
	req.Body = &gatewaybody.Request{RawBody: []byte(`{"messages":[{"role":"user","content":"changed"},{"role":"assistant","content":"x"},{"role":"user","content":"y"}]}`)}
	second := adapter.ResolveGatewaySessionIdentity(req, genericInput)
	if first.SessionID != second.SessionID || first.ConversationKey != second.ConversationKey {
		t.Fatalf("记忆化失效：first %+v second %+v", first, second)
	}
	if spy.lastCount != 4 {
		t.Fatalf("谱系判定消息数 = %d，want 首次计算的 4", spy.lastCount)
	}
	// 不同 profile 走独立记忆化（指纹随 profile 变化）。
	otherProfile := adapter.ResolveGatewaySessionIdentity(req, gatewaypreauth.SessionIdentityInput{
		ClientProfile: "cherry_studio", SystemAccountID: "sys-1", APIKeyID: "key-9",
	})
	if otherProfile.SessionID == second.SessionID {
		t.Fatal("不同 profile 的指纹必须不同")
	}
}
