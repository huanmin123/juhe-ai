package main

// API Key 安全策略请求链接入的链级测试（安全策略设计 §2/§12 验收 1/2，批B2）：
//  - 绑定含 placeholder 规则策略的 Key 发 chat 请求：mock 上游收到的 body 含
//    [[PHONE_1]] 不含原文手机号（请求侧断言；响应还原归下一批），usage 记录
//    携带 securityPolicyApplied=true 与按 label 的 piiHits 计数；
//  - 未绑定 Key 同请求：上游收到的 body 与客户端原文逐字节一致（零开销路径，
//    UpstreamBodyCache 为 nil 路径）；
//  - 读物化对象分支（codex OAuth 读 req.Body.Body 物化对象，不走
//    clientUpstreamBody）：sanitized 对象重建 + RawBody 保持原文（单元级）；
//  - endpoint family 判定与 builtin/未绑定零开销裁决（单元级）。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayoauthcodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// seedSecurityPolicyChainWorld 在链级夹具上种一条自定义安全策略（phone 占位
// 规则），可选绑定到 fixture 的 key_1。
func seedSecurityPolicyChainWorld(t *testing.T, fixture *chainFixture, bindKey bool) {
	t.Helper()
	now := "2026-09-04T00:00:00.000Z"
	if _, err := fixture.db.Exec(fixtureBind(`INSERT INTO security_policies (id, system_account_id, name, rules_json, built_in, created_at, updated_at)
		VALUES ('secpol_chain_phone', ?, '链级手机号占位', '{"rules":[{"id":"r1","type":"phone","action":"placeholder"}]}', 0, ?, ?)`),
		fixture.systemAccount, now, now); err != nil {
		t.Fatalf("seed security policy: %v", err)
	}
	if bindKey {
		if _, err := fixture.db.Exec(fixtureBind(`UPDATE api_keys SET security_policy_id = 'secpol_chain_phone' WHERE id = 'key_1'`)); err != nil {
			t.Fatalf("bind security policy: %v", err)
		}
	}
}

// securityPolicyBodyCaptureUpstream 是捕获上游请求体的 mock 上游。
func securityPolicyBodyCaptureUpstream(t *testing.T, body string) (*httptest.Server, func() string) {
	t.Helper()
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured = raw
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, func() string { return string(captured) }
}

const securityPolicyChatBody = `{"id":"chatcmpl-secpol","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"CONTENT"},"finish_reason":"stop"}]}`

// securityPolicyChatRequest 发一次带手机号的 /v1/chat/completions。
func securityPolicyChatRequest(t *testing.T, chain http.Handler, fixture *chainFixture, content string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"`+content+`"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// TestChainSecurityPolicySanitizesUpstreamBodyFullChain：绑定 placeholder 策略
// 的 Key 出站脱敏生效——上游收到 [[PHONE_1]] 与 §6.3 注入文案、不含原文手机
// 号；usage 记录 securityPolicyApplied=true、piiHits={"PHONE":1}（设计 §8）。
func TestChainSecurityPolicySanitizesUpstreamBodyFullChain(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	seedSecurityPolicyChainWorld(t, fixture, true)
	upstream, capturedBody := securityPolicyBodyCaptureUpstream(t, securityPolicyChatBody)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL)
	chain, spoolDir := composeChatDispatchChain(t, fixture)

	const rawNumber = "13812345678"
	status, body := securityPolicyChatRequest(t, chain, fixture, "打"+rawNumber+"求助")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	sent := capturedBody()
	if !strings.Contains(sent, "[[PHONE_1]]") {
		t.Fatalf("上游 body 应包含占位符：\n%s", sent)
	}
	if strings.Contains(sent, rawNumber) {
		t.Fatalf("上游 body 不得包含原文手机号：\n%s", sent)
	}
	if !strings.Contains(sent, "【隐私保护说明】") {
		t.Fatalf("placeholder 策略应注入 §6.3 固定文案：\n%s", sent)
	}
	// usage 记录：applied=绑定事实（与命中无关），piiHits 按 label 计数无原文。
	records := waitForSpoolRecords(t, spoolDir)
	var successRecord map[string]any
	for _, record := range records {
		if success, _ := record["success"].(bool); success {
			successRecord = record
			break
		}
	}
	if successRecord == nil {
		t.Fatalf("spool 缺成功记录: %v", records)
	}
	if applied, ok := successRecord["securityPolicyApplied"].(bool); !ok || !applied {
		t.Fatalf("usage securityPolicyApplied 应为 true: %v", successRecord["securityPolicyApplied"])
	}
	hits, ok := successRecord["piiHits"].(map[string]any)
	if !ok || hits["PHONE"].(float64) != 1 {
		t.Fatalf("usage piiHits 应为 {\"PHONE\":1}: %v", successRecord["piiHits"])
	}
}

// TestChainSecurityPolicyUnboundKeepsBodyByteIdenticalFullChain：未绑定 Key
// 的请求走零开销路径——上游收到的 body 与客户端原文逐字节一致（验收 1，
// UpstreamBodyCache 为 nil 路径回落 RawBody）。
func TestChainSecurityPolicyUnboundKeepsBodyByteIdenticalFullChain(t *testing.T) {
	fixture := newChainFixture(t)
	shortenChainWaitBudgets(t, fixture)
	// 策略行存在但未绑定：未绑定语义（NULL 列）必须零改动。
	seedSecurityPolicyChainWorld(t, fixture, false)
	upstream, capturedBody := securityPolicyBodyCaptureUpstream(t, securityPolicyChatBody)
	chainPointAccountAtUpstream(t, fixture.db, fixture.accountID, upstream.URL)
	chain, _ := composeChatDispatchChain(t, fixture)

	const requestRawBody = `{"model":"gpt-test","messages":[{"role":"user","content":"打13812345678求助"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestRawBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := capturedBody(); got != requestRawBody {
		t.Fatalf("未绑定 Key 上游 body 必须与客户端原文逐字节一致：\nsent:  %s\nclient: %s", got, requestRawBody)
	}
}

// TestApplySecurityPolicyRebuildsMaterializedObject：读物化对象分支——已物化
// Body 的请求经出站脱敏后 req.Body.Body 换成 sanitized 对象（codex OAuth 读
// 物化对象，验收 c 的可测形态），RawBody 保持客户端原文（审计/指纹契约），
// UpstreamBodyCache 携带 sanitized 字节，执行事实挂 req.SecurityPolicy。
func TestApplySecurityPolicyRebuildsMaterializedObject(t *testing.T) {
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	row := &gatewayruntimecache.GatewayAPIKeyRow{
		ID:                      "key_secpol",
		SecurityPolicyID:        "secpol-x",
		SecurityPolicyRulesJSON: `{"rules":[{"id":"r1","type":"phone","action":"placeholder"}]}`,
	}
	rawBody := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"打13812345678"}]}`)
	var originalObject map[string]any
	if err := json.Unmarshal(rawBody, &originalObject); err != nil {
		t.Fatalf("seed parsed object: %v", err)
	}
	bodyReq := &gatewaybody.Request{RawBody: append([]byte(nil), rawBody...), Body: originalObject}
	req := &gatewaypreauth.GatewayRequest{
		HTTP:    httpRequest,
		Runtime: &gatewayruntimecache.GatewayRuntime{APIKey: row},
		Body:    bodyReq,
	}

	if err := applySecurityPolicyIfNeeded(req, "trace-b2-materialized"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if req.SecurityPolicy == nil || req.SecurityPolicy.PolicyID != "secpol-x" {
		t.Fatalf("执行事实未挂载: %+v", req.SecurityPolicy)
	}
	if !req.SecurityPolicy.Applied {
		t.Fatalf("Applied 应为 true")
	}
	if req.Body.UpstreamBodyCache == nil || !bytes.Contains(req.Body.UpstreamBodyCache.PassthroughBody, []byte("[[PHONE_1]]")) {
		t.Fatalf("UpstreamBodyCache 应携带 sanitized 字节: %+v", req.Body.UpstreamBodyCache)
	}
	if !bytes.Equal(req.Body.RawBody, rawBody) {
		t.Fatalf("RawBody 必须保持客户端原文: %s", req.Body.RawBody)
	}
	object, ok := req.Body.Body.(map[string]any)
	if !ok {
		t.Fatalf("读物化对象应重建为 map: %T", req.Body.Body)
	}
	// HasPlaceholder 策略注入 §6.3 系统提示：首位插入一条 system 消息（设计
	// §6.2），原 user 消息后移。
	messages, _ := object["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("读物化对象应含注入 system + 原 user 两条消息: %v", object)
	}
	message := messages[1].(map[string]any)
	if message["content"] != "打[[PHONE_1]]" {
		t.Fatalf("读物化对象 content 应为 sanitized: %v", message["content"])
	}
	// 验收 c：codex OAuth 读物化对象读取口（gatewayoauthcodex.
	// ParseOpenAIOAuthCodexJsonObjectBody 读 req.Body.Body，不走
	// clientUpstreamBody）消费到 sanitized 内容。
	codexBody, parseErr := gatewayoauthcodex.ParseOpenAIOAuthCodexJsonObjectBody(req)
	if parseErr != nil {
		t.Fatalf("oauthadapter 读取物化对象: %v", parseErr)
	}
	codexObject, ok := codexBody.(map[string]any)
	if !ok {
		t.Fatalf("oauthadapter 读取结果应为对象: %T", codexBody)
	}
	codexMessages, _ := codexObject["messages"].([]any)
	if len(codexMessages) != 2 {
		t.Fatalf("oauthadapter 视角消息数不符: %v", codexObject)
	}
	if codexMessages[1].(map[string]any)["content"] != "打[[PHONE_1]]" {
		t.Fatalf("oauthadapter 应读到 sanitized 内容: %v", codexObject)
	}
	// 原始解析对象不得被原地改写（重建而非共享 mutation）。
	if originalObject["messages"].([]any)[0].(map[string]any)["content"] != "打13812345678" {
		t.Fatalf("原始解析对象应保持原文")
	}
}

// TestApplySecurityPolicyZeroPaths：未绑定 / 内置默认 / 空规则集 / 无 Key 快照
// 恒零开销返回（不挂执行事实、不写 UpstreamBodyCache），引用损坏错误上抛。
func TestApplySecurityPolicyZeroPaths(t *testing.T) {
	newRequest := func(row *gatewayruntimecache.GatewayAPIKeyRow) *gatewaypreauth.GatewayRequest {
		rawBody := []byte(`{"messages":[{"role":"user","content":"打13812345678"}]}`)
		cloned := gatewayruntimecache.CloneGatewayAPIKeyRow(*row)
		return &gatewaypreauth.GatewayRequest{
			HTTP:    httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
			Runtime: &gatewayruntimecache.GatewayRuntime{APIKey: &cloned},
			Body:    &gatewaybody.Request{RawBody: append([]byte(nil), rawBody...)},
		}
	}
	base := func() *gatewayruntimecache.GatewayAPIKeyRow {
		return &gatewayruntimecache.GatewayAPIKeyRow{ID: "key"}
	}

	t.Run("无 Key 快照", func(t *testing.T) {
		req := newRequest(base())
		req.Runtime.APIKey = nil
		if err := applySecurityPolicyIfNeeded(req, "t"); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if req.SecurityPolicy != nil || req.Body.UpstreamBodyCache != nil {
			t.Fatalf("无 Key 快照必须零开销: %+v", req.SecurityPolicy)
		}
	})
	t.Run("未绑定", func(t *testing.T) {
		req := newRequest(base())
		if err := applySecurityPolicyIfNeeded(req, "t"); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if req.SecurityPolicy != nil || req.Body.UpstreamBodyCache != nil {
			t.Fatalf("未绑定必须零开销: %+v", req.SecurityPolicy)
		}
	})
	t.Run("内置默认行", func(t *testing.T) {
		row := base()
		row.SecurityPolicyID = "secpol-builtin-default"
		row.SecurityPolicyRulesJSON = `{"rules":[]}`
		req := newRequest(row)
		if err := applySecurityPolicyIfNeeded(req, "t"); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if req.SecurityPolicy != nil {
			t.Fatalf("内置默认必须零开销: %+v", req.SecurityPolicy)
		}
	})
	t.Run("空规则集自定义策略", func(t *testing.T) {
		row := base()
		row.SecurityPolicyID = "secpol-empty"
		row.SecurityPolicyRulesJSON = `{"rules":[]}`
		req := newRequest(row)
		if err := applySecurityPolicyIfNeeded(req, "t"); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if req.SecurityPolicy != nil {
			t.Fatalf("空规则集必须零开销: %+v", req.SecurityPolicy)
		}
	})
	t.Run("规则集损坏显式上抛", func(t *testing.T) {
		row := base()
		row.SecurityPolicyID = "secpol-bad"
		row.SecurityPolicyRulesJSON = `{"rules":[{"id":"r1","type":"no_such_type","action":"mask"}]}`
		req := newRequest(row)
		if err := applySecurityPolicyIfNeeded(req, "t"); err == nil {
			t.Fatalf("损坏规则集必须显式上抛")
		}
	})
}

// TestSecurityPolicyFamilyForRequest：endpoint family 判定词表（五族 + 无法
// 判定空串）。
func TestSecurityPolicyFamilyForRequest(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodPost, "/v1/chat/completions", "openai_chat"},
		{http.MethodPost, "/v1/responses", "openai_responses"},
		{http.MethodPost, "/v1/messages", "anthropic"},
		{http.MethodPost, "/v1beta/models/gemini-pro:generateContent", "gemini"},
		{http.MethodPost, "/v1beta/models/gemini-pro:streamGenerateContent", "gemini"},
		{http.MethodPost, "/v1/videos", "media"},
		{http.MethodPost, "/v1/video/generations", "media"},
		{http.MethodPost, "/v1/tasks/audio", "media"},
		{http.MethodPost, "/v1/audio/speech", "media"},
		{http.MethodPost, "/v1/audio/jobs", "media"},
		{http.MethodPost, "/v1/images/generations", "media"},
		{http.MethodPost, "/v1/images/edits", "media"},
		{http.MethodGet, "/v1/models", ""},
		{http.MethodPost, "/v1/embeddings", "tool"},
		{http.MethodPost, "/v1/rerank", "tool"},
		{http.MethodPost, "/v1/moderations", "tool"},
		{http.MethodPost, "/v1/audio/transcriptions", "media"},
	}
	for _, c := range cases {
		req := &gatewaypreauth.GatewayRequest{HTTP: httptest.NewRequest(c.method, c.path, nil)}
		if got := securityPolicyFamilyForRequest(req); got != c.want {
			t.Fatalf("%s %s family = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}
