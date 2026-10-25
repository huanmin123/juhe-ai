package acceptance

// 内容指纹会话身份集成验收（内容指纹会话身份设计 §10，聚焦 §9.1/§9.4/§9.5）：
// 无官方会话 Header 的 generic 客户端以 chat completions 全量重发上下文派生
// 内容指纹身份，以 mock 上游账户命中数为黏住 oracle（设计口径：不依赖审计
// 字段）：
//  1. 三轮会话：首请求无确认锚点 → 亲和查询为零（rr 计数位可证）；第二请求
//     起产出确认档身份、协议成功写回绑定；第三请求起黏住同账户；
//  2. 恒定长度重放：LRU 记录生命周期内除首见外不建立、不消费亲和（按 rr
//     轮转，不黏）；
//  3. 改写锚点（模拟压缩）：身份更换 → 新会话语义重绑，旧绑定不被消费。
//
// 会话身份走默认检测画像（无 x-juhe-client-profile、无会话 Header），请求体
// 为逐轮全量重发的 messages 序列（锚点 [system?, U1, A1, U2] 冻结集 + 增长
// 尾部）。

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fingerprintMessagesBody 构造多轮 chat completions 请求体（全量重发语义）。
func fingerprintMessagesBody(model string, messages ...string) string {
	return fmt.Sprintf(`{"model":%q,"messages":[%s],"stream":false}`, model, strings.Join(messages, ","))
}

func fingerprintMsg(role, content string) string {
	return fmt.Sprintf(`{"role":%q,"content":%q}`, role, content)
}

func fingerprintTurn(userIndex int) []string {
	return []string{
		fingerprintMsg("user", fmt.Sprintf("cfp-question-%d", userIndex)),
		fingerprintMsg("assistant", fmt.Sprintf("cfp-reply-%d", userIndex)),
	}
}

func fingerprintHitCounts(f *fullchainFixture, keys ...string) map[string]int {
	out := map[string]int{}
	for _, key := range keys {
		out[key] = len(f.mock.protocolCallsByKey(key))
	}
	return out
}

// postChatRaw 是 chat 的并发安全变体（§9.2 并发交错臂）：不持有 testing.T，
// 传输与读取错误统一折算为非 200 响应带回主 goroutine 断言。
func postChatRaw(baseURL, apiKey, body string) fullchainChatResponse {
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		return fullchainChatResponse{Status: -1, Body: err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: 120 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fullchainChatResponse{Status: -1, Body: err.Error()}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return fullchainChatResponse{Status: -1, Body: err.Error()}
	}
	return fullchainChatResponse{Status: response.StatusCode, Body: string(raw)}
}

// waitFingerprintAuditLogs 轮询该 API Key 的审计列表直到出现 want 条记录
// （审计落库异步），返回列表行（含 sessionId 字段）。
func waitFingerprintAuditLogs(f *fullchainFixture, apiKeyID string, want int) []map[string]any {
	f.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		items := f.findAuditLogs(apiKeyID)
		if len(items) >= want {
			return items
		}
		time.Sleep(400 * time.Millisecond)
	}
	f.t.Fatalf("audit logs not in time: want %d items", want)
	return nil
}

// TestFullchainContentFingerprintSessionIdentity 三条臂共享一个 fixture、各用
// 独立路由（rr 计数算术互不干扰；不同路由的 API Key 使指纹键天然隔离）。
func TestFullchainContentFingerprintSessionIdentity(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	newTwoGroupRoute := func(tag string) (fullchainRoute, func() map[string]int) {
		keyOne := fullchainUpstreamKey(t, tag+"-1")
		keyTwo := fullchainUpstreamKey(t, tag+"-2")
		route := f.newRoute(tag, "round_robin", []fullchainGroupSpec{
			{accounts: []fullchainAccountSpec{{key: keyOne}}},
			{accounts: []fullchainAccountSpec{{key: keyTwo}}},
		}, nil, nil)
		return route, func() map[string]int { return fingerprintHitCounts(f, keyOne, keyTwo) }
	}

	// ---- 臂 1：三轮会话（设计 §9.1）----
	routeCF, cfHits := newTwoGroupRoute("CFAffinity")
	// r1：单 user 消息，无 A1/U2 → 仅临时档 → 无亲和查询，按 rr 落组 1。
	requireAffinityResponseOK(t, "CF-r1", f.chat(routeCF.apiKey,
		fingerprintMessagesBody(acceptanceModel, fingerprintMsg("user", "cfp-question-1"))))
	if got := cfHits(); got[routeCF.upstreamKeys[0]] != 1 || got[routeCF.upstreamKeys[1]] != 0 {
		t.Fatalf("CF r1 应按 rr 落账户 1，命中 %v", got)
	}
	// r2：全量重发 + 首条回复 + 第二条提问 → 确认档首见产出 → 查未命中按 rr
	// 落账户 2 并写回绑定（若 r1 错误绑定，r2 会黏账户 1）。
	r2Messages := []string{fingerprintMsg("user", "cfp-question-1")}
	r2Messages = append(r2Messages, fingerprintTurn(1)...)
	requireAffinityResponseOK(t, "CF-r2", f.chat(routeCF.apiKey,
		fingerprintMessagesBody(acceptanceModel, r2Messages...)))
	if got := cfHits(); got[routeCF.upstreamKeys[0]] != 1 || got[routeCF.upstreamKeys[1]] != 1 {
		t.Fatalf("CF r2 应按 rr 落账户 2 并写回绑定（r1 不得有绑定），命中 %v", got)
	}
	// r3：上下文继续增长 → 确认档演化产出 → 黏住 r2 绑定的账户 2。
	r3Messages := append(append([]string{}, r2Messages...), fingerprintTurn(2)...)
	requireAffinityResponseOK(t, "CF-r3", f.chat(routeCF.apiKey,
		fingerprintMessagesBody(acceptanceModel, r3Messages...)))
	if got := cfHits(); got[routeCF.upstreamKeys[0]] != 1 || got[routeCF.upstreamKeys[1]] != 2 {
		t.Fatalf("CF r3 应黏住账户 2，命中 %v", got)
	}
	// r4：继续增长仍黏账户 2。
	r4Messages := append(append([]string{}, r3Messages...), fingerprintTurn(3)...)
	requireAffinityResponseOK(t, "CF-r4", f.chat(routeCF.apiKey,
		fingerprintMessagesBody(acceptanceModel, r4Messages...)))
	if got := cfHits(); got[routeCF.upstreamKeys[0]] != 1 || got[routeCF.upstreamKeys[1]] != 3 {
		t.Fatalf("CF r4 应仍黏住账户 2，命中 %v", got)
	}

	// ---- 臂 2：恒定长度重放不黏（设计 §9.4）----
	routeCR, crHits := newTwoGroupRoute("CFReplay")
	replayBody := fingerprintMessagesBody(acceptanceModel, append(
		[]string{fingerprintMsg("system", "cfp-canned-system")},
		append(fingerprintTurn(1), fingerprintMsg("user", "cfp-canned-u2"))...)...)
	// rp1：首见产出一次（照常建绑），rr 落账户 1。
	requireAffinityResponseOK(t, "CF-rp1", f.chat(routeCR.apiKey, replayBody))
	if got := crHits(); got[routeCR.upstreamKeys[0]] != 1 {
		t.Fatalf("CF rp1 应按 rr 落账户 1，命中 %v", got)
	}
	// rp2..rp4：恒定长度 → 判别器拦截 → 无亲和 → 按 rr 轮转（若错误黏住，
	// 会全部命中账户 1）。
	for i := 2; i <= 4; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("CF-rp%d", i), f.chat(routeCR.apiKey, replayBody))
	}
	if got := crHits(); got[routeCR.upstreamKeys[0]] != 2 || got[routeCR.upstreamKeys[1]] != 2 {
		t.Fatalf("CF 重放臂：除首见外应按 rr 轮转（C,D 各 2），命中 %v", got)
	}

	// ---- 臂 3：改写锚点重绑（设计 §9.5）----
	routeCW, cwHits := newTwoGroupRoute("CFRewrite")
	// q1：仅临时档 → rr 落账户 1。
	requireAffinityResponseOK(t, "CF-q1", f.chat(routeCW.apiKey,
		fingerprintMessagesBody(acceptanceModel,
			fingerprintMsg("system", "cfp-orig-system"), fingerprintMsg("user", "cfp-rewrite-u1"))))
	if got := cwHits(); got[routeCW.upstreamKeys[0]] != 1 {
		t.Fatalf("CF q1 应按 rr 落账户 1，命中 %v", got)
	}
	// q2：确认档产出 K1 → rr 落账户 2 并绑定。
	requireAffinityResponseOK(t, "CF-q2", f.chat(routeCW.apiKey,
		fingerprintMessagesBody(acceptanceModel,
			fingerprintMsg("system", "cfp-orig-system"), fingerprintMsg("user", "cfp-rewrite-u1"),
			fingerprintMsg("assistant", "cfp-rewrite-a1"), fingerprintMsg("user", "cfp-rewrite-u2"))))
	if got := cwHits(); got[routeCW.upstreamKeys[1]] != 1 {
		t.Fatalf("CF q2 应按 rr 落账户 2 并绑定，命中 %v", got)
	}
	// q3：改写 system（模拟压缩）→ 新确认档 K2（首见）→ 查旧绑定不命中 →
	// 按 rr 重新落位账户 1 并重绑（不得消费 K1 的旧绑定）。
	requireAffinityResponseOK(t, "CF-q3", f.chat(routeCW.apiKey,
		fingerprintMessagesBody(acceptanceModel,
			fingerprintMsg("system", "cfp-compacted-system"), fingerprintMsg("user", "cfp-rewrite-u1"),
			fingerprintMsg("assistant", "cfp-rewrite-a1"), fingerprintMsg("user", "cfp-rewrite-u2"))))
	if got := cwHits(); got[routeCW.upstreamKeys[0]] != 2 || got[routeCW.upstreamKeys[1]] != 1 {
		t.Fatalf("CF q3 应按新会话语义 rr 落账户 1 重绑（不得黏旧绑定账户 2），命中 %v", got)
	}
	// q4：新会话继续增长 → 黏住 q3 重绑的账户 1。
	requireAffinityResponseOK(t, "CF-q4", f.chat(routeCW.apiKey,
		fingerprintMessagesBody(acceptanceModel,
			fingerprintMsg("system", "cfp-compacted-system"), fingerprintMsg("user", "cfp-rewrite-u1"),
			fingerprintMsg("assistant", "cfp-rewrite-a1"), fingerprintMsg("user", "cfp-rewrite-u2"),
			fingerprintMsg("assistant", "cfp-rewrite-a2"), fingerprintMsg("user", "cfp-rewrite-u3"))))
	if got := cwHits(); got[routeCW.upstreamKeys[0]] != 3 || got[routeCW.upstreamKeys[1]] != 1 {
		t.Fatalf("CF q4 应黏住重绑后的账户 1，命中 %v", got)
	}

	// ---- 臂 4：§9.2 并发交错（同 S+U1+A1 异 U2 双会话写回互不覆盖）----
	routeCI, ciHits := newTwoGroupRoute("CFInterleave")
	interleaveBase := []string{
		fingerprintMsg("system", "cfp-il-system"),
		fingerprintMsg("user", "cfp-il-u1"),
		fingerprintMsg("assistant", "cfp-il-a1"),
	}
	// 同 S+U1+A1、异 U2 → 确认档异码；首绑串行确定会话→账户映射。
	bodyOf := func(u2 string, extraTurns int) string {
		msgs := append([]string{}, interleaveBase...)
		msgs = append(msgs, fingerprintMsg("user", u2))
		for i := 0; i < extraTurns; i++ {
			msgs = append(msgs,
				fingerprintMsg("assistant", fmt.Sprintf("cfp-il-%s-a%d", u2, i+2)),
				fingerprintMsg("user", fmt.Sprintf("cfp-il-%s-u%d", u2, i+3)))
		}
		return fingerprintMessagesBody(acceptanceModel, msgs...)
	}
	requireAffinityResponseOK(t, "CF-P1-first", f.chat(routeCI.apiKey, bodyOf("cfp-il-p1", 0)))
	requireAffinityResponseOK(t, "CF-P2-first", f.chat(routeCI.apiKey, bodyOf("cfp-il-p2", 0)))
	if got := ciHits(); got[routeCI.upstreamKeys[0]] != 1 || got[routeCI.upstreamKeys[1]] != 1 {
		t.Fatalf("CF 交错臂前置：两会话首绑应各落一组，命中 %v", got)
	}
	// 并发交错增长：两会话各 4 个增长请求同时打满网关。确认档异码，绑定
	// 写回互不覆盖的可观察证据：各自全程黏住首绑组（若被覆盖，对应请求落
	// 到对方组，两侧计数同时偏移）。
	const interleaveGrowth = 4
	var wg sync.WaitGroup
	interleaveResponses := make([]fullchainChatResponse, interleaveGrowth*2)
	for i := 0; i < interleaveGrowth; i++ {
		wg.Add(2)
		go func(seq int) {
			defer wg.Done()
			interleaveResponses[seq] = postChatRaw(f.gw.baseURL, routeCI.apiKey, bodyOf("cfp-il-p1", seq+1))
		}(i)
		go func(seq int) {
			defer wg.Done()
			interleaveResponses[interleaveGrowth+seq] = postChatRaw(f.gw.baseURL, routeCI.apiKey, bodyOf("cfp-il-p2", seq+1))
		}(i)
	}
	wg.Wait()
	for i, response := range interleaveResponses {
		if response.Status != http.StatusOK || !strings.Contains(response.Body, "MOCK-OK reply") {
			t.Fatalf("CF 交错臂响应 %d = %d %s", i, response.Status, response.Body)
		}
	}
	if got := ciHits(); got[routeCI.upstreamKeys[0]] != 1+interleaveGrowth || got[routeCI.upstreamKeys[1]] != 1+interleaveGrowth {
		t.Fatalf("CF 交错臂：两会话应各自黏住首绑组（异码绑定互不覆盖），命中 %v", got)
	}

	// ---- 臂 5：§9.3 审计串联（session_id 完整值精确查询含全部请求）----
	routeCA, _ := newTwoGroupRoute("CFAudit")
	requireAffinityResponseOK(t, "CF-a1", f.chat(routeCA.apiKey,
		fingerprintMessagesBody(acceptanceModel, fingerprintMsg("user", "cfp-audit-u1"))))
	requireAffinityResponseOK(t, "CF-a2", f.chat(routeCA.apiKey,
		fingerprintMessagesBody(acceptanceModel,
			fingerprintMsg("user", "cfp-audit-u1"),
			fingerprintMsg("assistant", "cfp-audit-a1"),
			fingerprintMsg("user", "cfp-audit-u2"))))
	requireAffinityResponseOK(t, "CF-a3", f.chat(routeCA.apiKey,
		fingerprintMessagesBody(acceptanceModel,
			fingerprintMsg("user", "cfp-audit-u1"),
			fingerprintMsg("assistant", "cfp-audit-a1"),
			fingerprintMsg("user", "cfp-audit-u2"),
			fingerprintMsg("assistant", "cfp-audit-a2"),
			fingerprintMsg("user", "cfp-audit-u3"))))
	auditItems := waitFingerprintAuditLogs(f, routeCA.apiKeyID, 3)
	if len(auditItems) != 3 {
		t.Fatalf("CF 审计臂：审计条数 = %d，want 3", len(auditItems))
	}
	sessionID := str(auditItems[0]["sessionId"])
	if !strings.HasPrefix(sessionID, "cfp1-") || len(sessionID) != len("cfp1-")+64 {
		t.Fatalf("CF 审计臂：session_id = %q，want cfp1-+64hex", sessionID)
	}
	sessionClientType := str(auditItems[0]["sessionClientType"])
	if sessionClientType == "" {
		t.Fatal("CF 审计臂：session_client_type 为空，want 检测画像")
	}
	for _, item := range auditItems[1:] {
		if str(item["sessionId"]) != sessionID {
			t.Fatalf("CF 审计臂：同一会话 session_id 不一致：%q vs %q", sessionID, str(item["sessionId"]))
		}
		if str(item["sessionClientType"]) != sessionClientType {
			t.Fatalf("CF 审计臂：session_client_type 不一致：%q vs %q", sessionClientType, str(item["sessionClientType"]))
		}
	}
	// 按完整值精确查询：命中且仅命中本会话全部请求（统一会话身份 §5 主查询
	// 口径；session_id 含账户/画像隔离，其他路由的会话不同码）。
	_, queried := f.admin.do(http.MethodGet,
		"/__aisys__/api/audit-logs?systemAccountId=sys_admin&sessionId="+url.QueryEscape(sessionID)+"&page=1&pageSize=50",
		nil, wantStatus(http.StatusOK))
	queriedItems, _ := data(queried)["items"].([]any)
	if len(queriedItems) != 3 {
		t.Fatalf("CF 审计臂：按 session_id 完整值查询 = %d 条，want 3", len(queriedItems))
	}

	// ---- 臂 6：§9.12 预填确认档锚点的首请求（few-shot / 续写历史）----
	routePF, pfHits := newTwoGroupRoute("CFPrefill")
	prefillMsgs := []string{
		fingerprintMsg("system", "cfp-pf-system"),
		fingerprintMsg("user", "cfp-pf-u1"),
		fingerprintMsg("assistant", "cfp-pf-a1"),
		fingerprintMsg("user", "cfp-pf-u2"),
	}
	// 首请求 S/U1/A1/U2 齐备、末位 user 提问 → 首见即产出确认档 → 恰一次
	// 亲和查询（未命中，按 rr 落组 1）→ 协议成功恰一次写回。
	requireAffinityResponseOK(t, "CF-pf1", f.chat(routePF.apiKey,
		fingerprintMessagesBody(acceptanceModel, prefillMsgs...)))
	if got := pfHits(); got[routePF.upstreamKeys[0]] != 1 || got[routePF.upstreamKeys[1]] != 0 {
		t.Fatalf("CF 预填臂：首请求应按 rr 落组 1，命中 %v", got)
	}
	// 第二请求增长 → 判别器放行 → 黏住首请求写回的组 1（若首请求未写回，
	// 会按 rr 落组 2，计数偏移可证）。
	prefillGrow := append(append([]string{}, prefillMsgs...),
		fingerprintMsg("assistant", "cfp-pf-a2"), fingerprintMsg("user", "cfp-pf-u3"))
	requireAffinityResponseOK(t, "CF-pf2", f.chat(routePF.apiKey,
		fingerprintMessagesBody(acceptanceModel, prefillGrow...)))
	if got := pfHits(); got[routePF.upstreamKeys[0]] != 2 || got[routePF.upstreamKeys[1]] != 0 {
		t.Fatalf("CF 预填臂：第二请求应黏住首请求写回的组 1，命中 %v", got)
	}

	// ---- 臂 7：§9.13 反向断言（同 S+U1+A1+U2 双会话同码共享绑定）----
	routeSH, shHits := newTwoGroupRoute("CFSharedCanned")
	cannedMsgs := []string{
		fingerprintMsg("system", "cfp-sh-system"),
		fingerprintMsg("user", "cfp-sh-u1"),
		fingerprintMsg("assistant", "cfp-sh-a1"),
		fingerprintMsg("user", "cfp-sh-u2"),
	}
	// 会话 A：罐头开场首绑（首见产出，rr 落组 1 并写回）。
	requireAffinityResponseOK(t, "CF-shA", f.chat(routeSH.apiKey,
		fingerprintMessagesBody(acceptanceModel, cannedMsgs...)))
	if got := shHits(); got[routeSH.upstreamKeys[0]] != 1 {
		t.Fatalf("CF 共享臂：会话 A 应 rr 落组 1，命中 %v", got)
	}
	// 会话 B：同冻结锚点（同码）。其首个等长重放会被判别器拦截（§4.3 恒定
	// 长度语义），首个增长请求产出同一确认键 → 查询命中 A 的绑定 → 共享黏住。
	sharedGrow := append(append([]string{}, cannedMsgs...),
		fingerprintMsg("assistant", "cfp-sh-a2"), fingerprintMsg("user", "cfp-sh-b3"))
	requireAffinityResponseOK(t, "CF-shB", f.chat(routeSH.apiKey,
		fingerprintMessagesBody(acceptanceModel, sharedGrow...)))
	sharedGrow2 := append(append([]string{}, sharedGrow...),
		fingerprintMsg("assistant", "cfp-sh-a3"), fingerprintMsg("user", "cfp-sh-b4"))
	requireAffinityResponseOK(t, "CF-shB2", f.chat(routeSH.apiKey,
		fingerprintMessagesBody(acceptanceModel, sharedGrow2...)))
	if got := shHits(); got[routeSH.upstreamKeys[0]] != 3 || got[routeSH.upstreamKeys[1]] != 0 {
		t.Fatalf("CF 共享臂：会话 B 应与会话 A 共享绑定（同码黏组 1），命中 %v", got)
	}
	// 审计共享：两会话全部请求同一 cfp1- 审计 id（§8.1 反向断言）。
	sharedItems := waitFingerprintAuditLogs(f, routeSH.apiKeyID, 3)
	if len(sharedItems) != 3 {
		t.Fatalf("CF 共享臂：审计条数 = %d，want 3", len(sharedItems))
	}
	sharedID := str(sharedItems[0]["sessionId"])
	for _, item := range sharedItems {
		if str(item["sessionId"]) != sharedID {
			t.Fatalf("CF 共享臂：同码会话审计 id 应共享：%q vs %q", sharedID, str(item["sessionId"]))
		}
	}
}
