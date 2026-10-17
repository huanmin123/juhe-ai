package acceptance

// 账户内 API Key 会话亲和集成验收（账户内APIKey会话亲和设计 §8 验收 1/2/3/5）：
// 全链路夹具 + mock 上游，以 fixture Key ID 的协议命中序列为黏住 oracle
//（设计明确不依赖审计字段）。会话身份走显式 codex 画像兼容入口
//（x-juhe-client-profile: codex）+ 官方 session-id Header，在 /v1/responses
// 路径下经 CodexSessionHeaderResolver 生成 conversationKey → 会话亲和键。

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// responsesSessionBody 构造 /v1/responses 非流式请求体。
func responsesSessionBody(prompt string) string {
	return fmt.Sprintf(`{"model":%q,"input":%q,"stream":false}`, acceptanceModel, prompt)
}

// chatSessionAsCodex 以显式 codex 画像 + session-id Header 调 /v1/responses。
func (f *fullchainFixture) chatSessionAsCodex(t *testing.T, apiKey, sessionID, body string) fullchainChatResponse {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, f.gw.baseURL+"/v1/responses", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build responses request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("x-juhe-client-profile", "codex")
	request.Header.Set("session-id", sessionID)
	return f.doRawT(t, request)
}

// requireAffinityResponseOK 断言协议成功（2xx 且包含 mock 协议成功内容）。
func requireAffinityResponseOK(t *testing.T, phase string, response fullchainChatResponse) {
	t.Helper()
	if response.Status != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", phase, response.Status, response.Body)
	}
	if !strings.Contains(response.Body, "MOCK-OK reply") {
		t.Fatalf("%s 缺少协议成功内容: %s", phase, response.Body)
	}
}

// TestFullchainApiKeySessionAffinity 覆盖设计 §8 验收 1/2/3/5（验收 3 含 401
// 与冷却两变体，冷却变体兼作验收 6 冷却边界）：
//  1. rr 三 Key 账户，会话 SA 首个协议成功请求按策略落 keyA 并绑定，后续请求全部黏住 keyA；
//  2. 会话 SB 独立绑定 rr 下一位 keyB，两会话互不影响；无会话键控制组按 rr 均匀轮转
//     （黏住不消费轮转计数器的可观察证据：控制组从 keyC 起各 Key +2，且不打乱 A/B 黏住）；
//  3. 黏住 Key 失配两变体：401（本请求内救回下一把 Key 并在协议成功后重绑）与
//     冷却抑制（失败确认冲刷后黏性失配，按策略走健康候选并重绑）；
//  5. 独立账户黏住 Key 返回 2xx 坏载荷（截断 body 协议校验失败，网关本地
//     502 收口无重试，不轮换 Key、不写 Key 抑制）→ 失败不写回指纹；既有响应
//     失败语义整删绑定后，下一请求按策略重新落位并黏新 Key。
//
// 验收 4（全部 Key 冷却按 recovery 池序首项兜底）在本测试的新鲜隔离 fixture
// 且仅由串行网关请求驱动时不可达：全失败请求不冲刷确认失败，最后救回成功的
// Key 必然健康；由单测
// TestSelectAccountAPIKeyStickyIgnoredWhenOnlyRecovery 覆盖。
func TestFullchainApiKeySessionAffinity(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	keyA := fullchainUpstreamKey(t, "KeyAffinity-a")
	keyB := fullchainUpstreamKey(t, "KeyAffinity-b")
	keyC := fullchainUpstreamKey(t, "KeyAffinity-c")
	route := f.newRoute("KeyAffinity", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{
			key: keyA,
			extra: map[string]any{
				"credentials": map[string]any{
					"api_key":          keyA,
					"api_keys":         []any{keyA, keyB, keyC},
					"api_key_strategy": "round_robin",
				},
			},
		}}},
	}, nil, nil)
	hits := func() map[string]int {
		return map[string]int{
			keyA: len(f.mock.protocolCallsByKey(keyA)),
			keyB: len(f.mock.protocolCallsByKey(keyB)),
			keyC: len(f.mock.protocolCallsByKey(keyC)),
		}
	}

	// —— 验收 1：SA 首请求 rr 落 keyA（新账户轮转计数从 0 开始）并绑定。
	sessionA := "sess-affinity-a"
	requireAffinityResponseOK(t, "SA-first", f.chatSessionAsCodex(t, route.apiKey, sessionA, responsesSessionBody("亲和-A首绑")))
	if got := hits(); got[keyA] != 1 || got[keyB] != 0 || got[keyC] != 0 {
		t.Fatalf("SA 首请求应 rr 选 keyA，命中 %v", got)
	}
	for i := 0; i < 20; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("SA-%d", i),
			f.chatSessionAsCodex(t, route.apiKey, sessionA, responsesSessionBody(fmt.Sprintf("亲和-A%d", i))))
	}
	if got := hits(); got[keyA] != 21 || got[keyB] != 0 || got[keyC] != 0 {
		t.Fatalf("验收1：同会话后续请求应全部黏住 keyA，命中 %v", got)
	}

	// —— 验收 2：SB 独立绑定 rr 下一位 keyB；A/B 互不影响。
	sessionB := "sess-affinity-b"
	requireAffinityResponseOK(t, "SB-first", f.chatSessionAsCodex(t, route.apiKey, sessionB, responsesSessionBody("亲和-B首绑")))
	if got := hits(); got[keyB] != 1 {
		t.Fatalf("SB 首请求应 rr 选 keyB，命中 %v", got)
	}
	for i := 0; i < 20; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("SB-%d", i),
			f.chatSessionAsCodex(t, route.apiKey, sessionB, responsesSessionBody(fmt.Sprintf("亲和-B%d", i))))
	}
	if got := hits(); got[keyA] != 21 || got[keyB] != 21 || got[keyC] != 0 {
		t.Fatalf("验收2：SB 应黏住 keyB 且不影响 SA，命中 %v", got)
	}
	// 无会话键控制组：轮转计数已消耗 2（A/B 首选），6 个请求按 rr 取序
	// keyC,keyA,keyB,keyC,keyA,keyB（各 Key +2）。20 次黏性请求不是 3 的倍数，
	// 因而若黏性错误地消费计数器，SB 首选或该控制组起始 Key 都会偏移。
	for i := 0; i < 6; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("anon-%d", i),
			f.chat(route.apiKey, chatBody(acceptanceModel, fmt.Sprintf("亲和-无会话%d", i))))
	}
	if got := hits(); got[keyA] != 23 || got[keyB] != 23 || got[keyC] != 2 {
		t.Fatalf("验收2 控制组：无会话请求应按 rr 从 keyC 起各 Key +2，命中 %v", got)
	}
	requireAffinityResponseOK(t, "SA-after-anon", f.chatSessionAsCodex(t, route.apiKey, sessionA, responsesSessionBody("亲和-A回归")))
	if got := hits(); got[keyA] != 24 || got[keyB] != 23 || got[keyC] != 2 {
		t.Fatalf("无会话轮转后 SA 仍应黏住 keyA，命中 %v", got)
	}

	// —— 验收 3：黏住 keyA 脚本一次 401 → 本请求内救回 keyB 并协议成功重绑。
	f.mock.script(keyA, platformmock.ScenarioStatus401)
	requireAffinityResponseOK(t, "SA-failover", f.chatSessionAsCodex(t, route.apiKey, sessionA, responsesSessionBody("亲和-失配重绑")))
	if got := hits(); got[keyA] != 25 || got[keyB] != 24 {
		t.Fatalf("验收3：失败请求应 keyA +1（401）并救回 keyB +1，命中 %v", got)
	}
	for i := 0; i < 6; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("SA-rebound-%d", i),
			f.chatSessionAsCodex(t, route.apiKey, sessionA, responsesSessionBody(fmt.Sprintf("亲和-A重绑%d", i))))
	}
	if got := hits(); got[keyA] != 25 || got[keyB] != 30 || got[keyC] != 2 {
		t.Fatalf("验收3：重绑后 SA 应黏住 keyB，命中 %v", got)
	}
	// SB 不受 SA 重绑影响。
	requireAffinityResponseOK(t, "SB-after-rebind", f.chatSessionAsCodex(t, route.apiKey, sessionB, responsesSessionBody("亲和-B回归")))
	if got := hits(); got[keyB] != 31 || got[keyC] != 2 {
		t.Fatalf("SB 应仍黏住 keyB，命中 %v", got)
	}

	// —— 验收 3 冷却变体（兼验收 6 冷却边界）：黏住 Key 进入失败抑制（确认
	// 失败冲刷，3s 退避）后黏性失配：会话不再命中旧指纹，按策略走健康候选，
	// 协议成功后绑定新 Key。账户用 failover 策略（恒选健康池首）消除 rr 计数
	// 算术对断言的干扰；抑制用无会话请求制造（脚本池首 keyD 401 → 救回 keyE
	// 成功 → keyD 失败确认入抑制），不影响 SC 既有绑定。「全部 Key 冷却按
	// recovery 池序首项兜底」在本测试的新鲜隔离 fixture 且仅由串行网关请求驱动时
	// 不可达（全失败请求不冲刷确认失败，最后救回成功的 Key 必然健康），由单测
	// TestSelectAccountAPIKeyStickyIgnoredWhenOnlyRecovery 覆盖。
	keyD := fullchainUpstreamKey(t, "KeyAffinityCooldown-d")
	keyE := fullchainUpstreamKey(t, "KeyAffinityCooldown-e")
	keyF := fullchainUpstreamKey(t, "KeyAffinityCooldown-f")
	routeCD := f.newRoute("KeyAffinityCooldown", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{
			key: keyD,
			extra: map[string]any{
				"credentials": map[string]any{
					"api_key":          keyD,
					"api_keys":         []any{keyD, keyE, keyF},
					"api_key_strategy": "failover",
				},
			},
		}}},
	}, nil, nil)
	requireAffinityResponseOK(t, "CD-warmup", f.chat(routeCD.apiKey, chatBody(acceptanceModel, "亲和-冷却预热")))
	sessionCD := "sess-affinity-cd"
	requireAffinityResponseOK(t, "CD-first", f.chatSessionAsCodex(t, routeCD.apiKey, sessionCD, responsesSessionBody("亲和-冷却首绑")))
	cdHits := func() map[string]int {
		return map[string]int{
			keyD: len(f.mock.protocolCallsByKey(keyD)),
			keyE: len(f.mock.protocolCallsByKey(keyE)),
			keyF: len(f.mock.protocolCallsByKey(keyF)),
		}
	}
	if got := cdHits(); got[keyD] != 2 || got[keyE] != 0 || got[keyF] != 0 {
		t.Fatalf("验收3 冷却变体前置：failover 下预热与 SC 首绑都应落池首 keyD，命中 %v", got)
	}
	// 无会话请求脚本池首 keyD 401：failover 首选 keyD → 401 → 救回 keyE 成功；
	// keyD 失败随本请求成功被确认入抑制（3s 窗口）。
	f.mock.script(keyD, platformmock.ScenarioStatus401)
	requireAffinityResponseOK(t, "CD-suppress", f.chat(routeCD.apiKey, chatBody(acceptanceModel, "亲和-冷却抑制")))
	if got := cdHits(); got[keyD] != 3 || got[keyE] != 1 || got[keyF] != 0 {
		t.Fatalf("验收3 冷却变体：抑制请求应 keyD 401 并救回 keyE，命中 %v", got)
	}
	// 抑制窗口内 SC 请求：黏住 keyD 失配（冷却 Key 不在健康候选集）→ failover
	// 走健康池首 keyE → 协议成功重绑 SC → keyE；旧指纹 keyD 与池尾 keyF 均不
	// 再命中。
	requireAffinityResponseOK(t, "CD-reselect", f.chatSessionAsCodex(t, routeCD.apiKey, sessionCD, responsesSessionBody("亲和-冷却重选")))
	if got := cdHits(); got[keyD] != 3 || got[keyE] != 2 || got[keyF] != 0 {
		t.Fatalf("验收3 冷却变体：黏住 Key 冷却时应按策略走 keyE 并重绑，不得再命中 keyD/keyF，命中 %v", got)
	}
	// SC 后续请求黏重绑后的 keyE。
	requireAffinityResponseOK(t, "CD-rebound", f.chatSessionAsCodex(t, routeCD.apiKey, sessionCD, responsesSessionBody("亲和-冷却重绑")))
	if got := cdHits(); got[keyD] != 3 || got[keyE] != 3 || got[keyF] != 0 {
		t.Fatalf("验收3 冷却变体：重绑后 SC 应黏住 keyE，命中 %v", got)
	}

	// —— 验收 5：黏住 Key 返回 2xx 坏载荷（malformed_json → 截断 body 协议
	// 校验失败，网关本地 502 收口（AlreadyFinalized，无重试），不写 Key 抑制、
	// 不触发 Key 轮换；既有响应失败语义 forget 整删绑定）。
	keyG := fullchainUpstreamKey(t, "KeyAffinityNoRewrite-g")
	keyH := fullchainUpstreamKey(t, "KeyAffinityNoRewrite-h")
	keyI := fullchainUpstreamKey(t, "KeyAffinityNoRewrite-i")
	routeNR := f.newRoute("KeyAffinityNoRewrite", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{
			key: keyG,
			extra: map[string]any{
				"credentials": map[string]any{
					"api_key":          keyG,
					"api_keys":         []any{keyG, keyH, keyI},
					"api_key_strategy": "round_robin",
				},
			},
		}}},
	}, nil, nil)
	sessionNR := "sess-affinity-nr"
	requireAffinityResponseOK(t, "NR-first", f.chatSessionAsCodex(t, routeNR.apiKey, sessionNR, responsesSessionBody("亲和-不换绑首绑")))
	nrHits := func() map[string]int {
		return map[string]int{
			keyG: len(f.mock.protocolCallsByKey(keyG)),
			keyH: len(f.mock.protocolCallsByKey(keyH)),
			keyI: len(f.mock.protocolCallsByKey(keyI)),
		}
	}
	if got := nrHits(); got[keyG] != 1 || got[keyH] != 0 || got[keyI] != 0 {
		t.Fatalf("验收5 前置：SD 首绑应落 keyG，命中 %v", got)
	}
	f.mock.script(keyG, platformmock.ScenarioMalformedJSON)
	broken := f.chatSessionAsCodex(t, routeNR.apiKey, sessionNR, responsesSessionBody("亲和-协议失败"))
	if broken.Status != http.StatusBadGateway {
		t.Fatalf("验收5：2xx 坏载荷应由网关本地 502 收口，status=%d body=%s", broken.Status, broken.Body)
	}
	if got := nrHits(); got[keyG] != 2 || got[keyH] != 0 || got[keyI] != 0 {
		t.Fatalf("验收5：协议失败只应命中黏住 keyG（本地 502 收口不轮换 Key、不写抑制），命中 %v", got)
	}
	// 非流式协议校验失败走既有响应失败语义：ForgetSessionAffinity 整条删除绑定
	//（Key 指纹与账户绑定同删，符合验收 5「指纹不得新增或替换」+「既有 forget
	// 整删」边界）。下一请求按策略重新落位（rr 计数位 keyH）并协议成功新绑，
	// 之后黏住新 Key。
	requireAffinityResponseOK(t, "NR-after-broken", f.chatSessionAsCodex(t, routeNR.apiKey, sessionNR, responsesSessionBody("亲和-不换绑回归")))
	if got := nrHits(); got[keyG] != 2 || got[keyH] != 1 || got[keyI] != 0 {
		t.Fatalf("验收5：协议失败后应按策略重新落位 keyH（若指纹被错误保留会命中 keyG），命中 %v", got)
	}
	requireAffinityResponseOK(t, "NR-stick-new", f.chatSessionAsCodex(t, routeNR.apiKey, sessionNR, responsesSessionBody("亲和-不换绑新黏")))
	if got := nrHits(); got[keyG] != 2 || got[keyH] != 2 || got[keyI] != 0 {
		t.Fatalf("验收5：重新落位后应黏住 keyH，命中 %v", got)
	}
}
