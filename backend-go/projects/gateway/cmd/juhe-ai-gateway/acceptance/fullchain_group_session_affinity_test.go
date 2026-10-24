package acceptance

// 组级会话亲和集成验收（策略路由分组会话亲和设计 §7 验收 1/2/3/4/5/6/8）：
// 全链路夹具 + mock 双分组上游，以 fixture 账户 Key 的协议命中序列为黏组
// oracle（设计 §8 明确不依赖审计字段）。会话身份与账户内 Key 亲和同一入口
//（codex 画像 + session-id Header → conversationKey → 组亲和键）。
//
// 分布类断言口径（复审裁定）：轮转/加权命中分布以单测为准（peek 零消费、
// weighted 平滑序），本文件只断言黏组序列与计数器旁证。

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

func groupAffinityHits(f *fullchainFixture, keys ...string) map[string]int {
	out := map[string]int{}
	for _, key := range keys {
		out[key] = len(f.mock.protocolCallsByKey(key))
	}
	return out
}

// postResponsesRaw 是 chatSessionAsCodex 的并发安全变体：不持有 testing.T，
// 传输与读取错误统一折算为非 200 响应带回主 goroutine 断言。
func postResponsesRaw(baseURL, apiKey, sessionID, body string) fullchainChatResponse {
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/responses", strings.NewReader(body))
	if err != nil {
		return fullchainChatResponse{Status: -1, Body: err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("x-juhe-client-profile", "codex")
	request.Header.Set("session-id", sessionID)
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
	return fullchainChatResponse{Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: string(raw)}
}

// TestFullchainGroupSessionAffinity 覆盖设计 §7 验收 1/2（rr 黏组 + 计数器
// 旁证 + 双会话隔离 + 32 并发交错）、验收 3 黏组半边（weighted 黏住不依赖
// 分布断言）、验收 4（failover 组回退后不回切）、验收 5（黏住组账户全部
// 禁用 → 重选重绑 → 恢复后不回切）、验收 6（协议失败随既有响应失败 forget
// 删组绑定，下一请求按模式重选并重绑）、验收 8（merge 不适用组黏性的全链
// 不破坏证据）。
func TestFullchainGroupSessionAffinity(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	// ---- 验收 1/2：round_robin 双分组 ----
	keyA := fullchainUpstreamKey(t, "GroupAffinity-a")
	keyB := fullchainUpstreamKey(t, "GroupAffinity-b")
	route := f.newRoute("GroupAffinity", "round_robin", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{key: keyA}}},
		{accounts: []fullchainAccountSpec{{key: keyB}}},
	}, nil, nil)
	hits := func() map[string]int { return groupAffinityHits(f, keyA, keyB) }

	// 验收 1：SA 首请求按 rr 落组 A 并绑定；随后 20 个协议成功请求全部黏组 A。
	sessionA := "sess-group-affinity-a"
	requireAffinityResponseOK(t, "GA-SA-first", f.chatSessionAsCodex(t, route.apiKey, sessionA, responsesSessionBody("组亲和-A首绑")))
	if got := hits(); got[keyA] != 1 || got[keyB] != 0 {
		t.Fatalf("GA 首请求应 rr 落组 A，命中 %v", got)
	}
	for i := 0; i < 20; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GA-SA-%d", i),
			f.chatSessionAsCodex(t, route.apiKey, sessionA, responsesSessionBody(fmt.Sprintf("组亲和-A%d", i))))
	}
	if got := hits(); got[keyA] != 21 || got[keyB] != 0 {
		t.Fatalf("验收1：同会话后续请求应全部黏组 A，命中 %v", got)
	}

	// 计数器旁证（验收 7 的可观察投影）：无会话控制组从组 B 起轮转——
	// 黏住的 20 次请求没有消费 rr 计数器（计数位仍停在首绑后的 1）。
	for i := 0; i < 2; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GA-anon-%d", i),
			f.chat(route.apiKey, chatBody(acceptanceModel, fmt.Sprintf("组亲和-无会话%d", i))))
	}
	if got := hits(); got[keyA] != 22 || got[keyB] != 1 {
		t.Fatalf("验收1 计数器旁证：无会话控制组应从组 B 起轮转（B,A），命中 %v", got)
	}

	// 验收 2：SB 独立绑定 rr 下一位（组 B），两会话互不影响。
	sessionB := "sess-group-affinity-b"
	requireAffinityResponseOK(t, "GA-SB-first", f.chatSessionAsCodex(t, route.apiKey, sessionB, responsesSessionBody("组亲和-B首绑")))
	if got := hits(); got[keyB] != 2 {
		t.Fatalf("验收2：SB 首请求应 rr 落组 B，命中 %v", got)
	}
	for i := 0; i < 20; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GA-SB-%d", i),
			f.chatSessionAsCodex(t, route.apiKey, sessionB, responsesSessionBody(fmt.Sprintf("组亲和-B%d", i))))
	}
	if got := hits(); got[keyA] != 22 || got[keyB] != 22 {
		t.Fatalf("验收2：SB 应黏组 B 且不影响 SA，命中 %v", got)
	}
	requireAffinityResponseOK(t, "GA-SA-after", f.chatSessionAsCodex(t, route.apiKey, sessionA, responsesSessionBody("组亲和-A回归")))
	requireAffinityResponseOK(t, "GA-SB-after", f.chatSessionAsCodex(t, route.apiKey, sessionB, responsesSessionBody("组亲和-B回归")))
	if got := hits(); got[keyA] != 23 || got[keyB] != 23 {
		t.Fatalf("验收2：交错后各自仍黏原组，命中 %v", got)
	}
	// 验收 2（并发交错）：两会话各 16 个请求同时打满网关，黏组判定在并发下
	// 保持——SA 全部落组 A、SB 全部落组 B（39/39），且全部协议成功。
	const concurrentPerSession = 16
	results := make([]fullchainChatResponse, concurrentPerSession*2)
	var wg sync.WaitGroup
	for slot := 0; slot < concurrentPerSession*2; slot++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			if slot%2 == 0 {
				results[slot] = postResponsesRaw(f.gw.baseURL, route.apiKey, sessionA, responsesSessionBody(fmt.Sprintf("组亲和-A并发%d", slot)))
			} else {
				results[slot] = postResponsesRaw(f.gw.baseURL, route.apiKey, sessionB, responsesSessionBody(fmt.Sprintf("组亲和-B并发%d", slot)))
			}
		}(slot)
	}
	wg.Wait()
	for slot, response := range results {
		if response.Status != http.StatusOK || !strings.Contains(response.Body, "MOCK-OK reply") {
			t.Fatalf("验收2 并发：槽位 %d status=%d body=%s", slot, response.Status, response.Body)
		}
	}
	if got := hits(); got[keyA] != 39 || got[keyB] != 39 {
		t.Fatalf("验收2 并发交错后应保持 39/39，命中 %v", got)
	}

	// ---- 验收 3（黏组半边）：weighted 双分组，会话首绑后连续黏首绑组 ----
	//（配比分布由单测 TestPeekAPIKeyGroupBindingsWeightedDoesNotMutateDebt
	// 与平滑序基线覆盖，本臂只断言黏组序列。）
	keyW1 := fullchainUpstreamKey(t, "GroupAffinity-w1")
	keyW2 := fullchainUpstreamKey(t, "GroupAffinity-w2")
	routeW := f.newRoute("GroupAffinityWeighted", "weighted", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{key: keyW1}}},
		{accounts: []fullchainAccountSpec{{key: keyW2}}},
	}, nil, nil)
	sessionW := "sess-group-affinity-w"
	requireAffinityResponseOK(t, "GW-first", f.chatSessionAsCodex(t, routeW.apiKey, sessionW, responsesSessionBody("组亲和-W首绑")))
	if got := groupAffinityHits(f, keyW1, keyW2); got[keyW1] != 1 || got[keyW2] != 0 {
		t.Fatalf("验收3：weighted 首绑应落高权重组 W1，命中 %v", got)
	}
	for i := 0; i < 20; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GW-%d", i),
			f.chatSessionAsCodex(t, routeW.apiKey, sessionW, responsesSessionBody(fmt.Sprintf("组亲和-W%d", i))))
	}
	if got := groupAffinityHits(f, keyW1, keyW2); got[keyW1] != 21 || got[keyW2] != 0 {
		t.Fatalf("验收3：weighted 会话应连续黏首绑组 W1（sticky hit 不消费加权状态），命中 %v", got)
	}

	// ---- 验收 4：failover 双分组，主组故障组回退重绑后不回切 ----
	keyF1 := fullchainUpstreamKey(t, "GroupAffinity-f1")
	keyF2 := fullchainUpstreamKey(t, "GroupAffinity-f2")
	routeF := f.newRoute("GroupAffinityFailover", "failover", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{key: keyF1}}},
		{accounts: []fullchainAccountSpec{{key: keyF2}}},
	}, nil, nil)
	hitsF := func() map[string]int { return groupAffinityHits(f, keyF1, keyF2) }
	sessionF := "sess-group-affinity-f"
	requireAffinityResponseOK(t, "GF-first", f.chatSessionAsCodex(t, routeF.apiKey, sessionF, responsesSessionBody("组亲和-F首绑")))
	for i := 0; i < 10; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GF-%d", i),
			f.chatSessionAsCodex(t, routeF.apiKey, sessionF, responsesSessionBody(fmt.Sprintf("组亲和-F%d", i))))
	}
	if got := hitsF(); got[keyF1] != 11 || got[keyF2] != 0 {
		t.Fatalf("验收4 前置：failover 会话应黏主组 F1，命中 %v", got)
	}
	// 主组脚本 401：本请求组回退落备组并协议成功重绑。
	f.mock.script(keyF1, platformmock.ScenarioStatus401)
	requireAffinityResponseOK(t, "GF-failover", f.chatSessionAsCodex(t, routeF.apiKey, sessionF, responsesSessionBody("组亲和-F回退")))
	if got := hitsF(); got[keyF1] != 12 || got[keyF2] != 1 {
		t.Fatalf("验收4：主组失败应组回退 F2 并重绑，命中 %v", got)
	}
	// 主组恢复（还原脚本）：连续 20 个成功请求仍黏备组 F2，不回切。
	f.mock.script(keyF1, platformmock.ScenarioChatOK)
	for i := 0; i < 20; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GF-nocut-%d", i),
			f.chatSessionAsCodex(t, routeF.apiKey, sessionF, responsesSessionBody(fmt.Sprintf("组亲和-F不回切%d", i))))
	}
	if got := hitsF(); got[keyF1] != 12 || got[keyF2] != 21 {
		t.Fatalf("验收4：主组恢复后应继续黏备组 F2（不回切），命中 %v", got)
	}

	// ---- 验收 5：黏住组账户全部禁用 → 黏住 peek 无活跃账户按 miss 落下一
	// 组并重绑；账户恢复后继续黏新组（组亲和无故障恢复回切语义）----
	keyD1 := fullchainUpstreamKey(t, "GroupAffinity-d1")
	keyD2 := fullchainUpstreamKey(t, "GroupAffinity-d2")
	routeD := f.newRoute("GroupAffinityDisable", "round_robin", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{key: keyD1}}},
		{accounts: []fullchainAccountSpec{{key: keyD2}}},
	}, nil, nil)
	hitsD := func() map[string]int { return groupAffinityHits(f, keyD1, keyD2) }
	sessionD := "sess-group-affinity-d"
	requireAffinityResponseOK(t, "GD-first", f.chatSessionAsCodex(t, routeD.apiKey, sessionD, responsesSessionBody("组亲和-D首绑")))
	if got := hitsD(); got[keyD1] != 1 || got[keyD2] != 0 {
		t.Fatalf("验收5 前置：会话应黏组 D1，命中 %v", got)
	}
	// 禁用黏住组唯一账户：账户 PATCH 经失效总线同步清运行时缓存，下一请求
	// 黏住 peek 组 D1 无活跃账户按 miss 前进落组 D2。轮询只为兜住失效总线的
	// 异步观察窗口。
	f.patchAccount(routeD.accountIDs[0], map[string]any{"status": "disabled"})
	disabledHitsD1 := hitsD()[keyD1]
	rebound := false
	for attempt := 0; attempt < 10 && !rebound; attempt++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GD-reselect-%d", attempt),
			f.chatSessionAsCodex(t, routeD.apiKey, sessionD, responsesSessionBody(fmt.Sprintf("组亲和-D重选%d", attempt))))
		rebound = hitsD()[keyD2] > 0
		if !rebound {
			time.Sleep(300 * time.Millisecond)
		}
	}
	if !rebound {
		t.Fatalf("验收5：黏住组账户全部禁用后应重选落组 D2，命中 %v", hitsD())
	}
	// 重绑后黏住新组 D2（重选恰好 1 次落 D2，随后 5 次全部黏住）。
	for i := 0; i < 5; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GD-stick-%d", i),
			f.chatSessionAsCodex(t, routeD.apiKey, sessionD, responsesSessionBody(fmt.Sprintf("组亲和-D新黏%d", i))))
	}
	if got := hitsD(); got[keyD1] != disabledHitsD1 || got[keyD2] != 6 {
		t.Fatalf("验收5：重绑后应黏住新组 D2（D1 禁用后不再增长），命中 %v", got)
	}
	// 账户恢复：继续黏 D2 不回切。
	f.patchAccount(routeD.accountIDs[0], map[string]any{"status": "active"})
	for i := 0; i < 5; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GD-nocut-%d", i),
			f.chatSessionAsCodex(t, routeD.apiKey, sessionD, responsesSessionBody(fmt.Sprintf("组亲和-D恢复%d", i))))
	}
	if got := hitsD(); got[keyD1] != disabledHitsD1 || got[keyD2] != 11 {
		t.Fatalf("验收5：账户恢复后应继续黏 D2 不回切，命中 %v", got)
	}

	// ---- 验收 6：2xx 坏载荷协议失败 → 组绑定随既有响应失败 forget 删除 ----
	keyN1 := fullchainUpstreamKey(t, "GroupAffinity-n1")
	keyN2 := fullchainUpstreamKey(t, "GroupAffinity-n2")
	routeN := f.newRoute("GroupAffinityNoRewrite", "round_robin", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{key: keyN1}}},
		{accounts: []fullchainAccountSpec{{key: keyN2}}},
	}, nil, nil)
	hitsN := func() map[string]int { return groupAffinityHits(f, keyN1, keyN2) }
	sessionN := "sess-group-affinity-n"
	requireAffinityResponseOK(t, "GN-first", f.chatSessionAsCodex(t, routeN.apiKey, sessionN, responsesSessionBody("组亲和-N首绑")))
	for i := 0; i < 6; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GN-%d", i),
			f.chatSessionAsCodex(t, routeN.apiKey, sessionN, responsesSessionBody(fmt.Sprintf("组亲和-N%d", i))))
	}
	if got := hitsN(); got[keyN1] != 7 || got[keyN2] != 0 {
		t.Fatalf("验收6 前置：会话应黏组 N1，命中 %v", got)
	}
	// 黏住组 2xx 坏载荷：非流式协议校验失败，网关本地 502 收口（无重试）。
	f.mock.script(keyN1, platformmock.ScenarioMalformedJSON)
	broken := f.chatSessionAsCodex(t, routeN.apiKey, sessionN, responsesSessionBody("组亲和-N协议失败"))
	if broken.Status != http.StatusBadGateway {
		t.Fatalf("验收6：2xx 坏载荷应由网关本地 502 收口，status=%d body=%s", broken.Status, broken.Body)
	}
	if got := hitsN(); got[keyN1] != 8 || got[keyN2] != 0 {
		t.Fatalf("验收6：协议失败只应命中黏住组 N1（本地 502 收口无重试），命中 %v", got)
	}
	// 组绑定已随既有响应失败 forget 三层删除：下一请求按 rr 重新选择（黏住
	// miss 推进一次落 N2）并协议成功重绑，之后黏新组。
	requireAffinityResponseOK(t, "GN-reselect", f.chatSessionAsCodex(t, routeN.apiKey, sessionN, responsesSessionBody("组亲和-N重选")))
	if got := hitsN(); got[keyN1] != 8 || got[keyN2] != 1 {
		t.Fatalf("验收6：协议失败后应按模式重新落组 N2（若绑定被错误保留会再命中 N1），命中 %v", got)
	}
	for i := 0; i < 6; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GN-stick-%d", i),
			f.chatSessionAsCodex(t, routeN.apiKey, sessionN, responsesSessionBody(fmt.Sprintf("组亲和-N新黏%d", i))))
	}
	if got := hitsN(); got[keyN1] != 8 || got[keyN2] != 7 {
		t.Fatalf("验收6：重选后应黏住新组 N2，命中 %v", got)
	}
}

// TestFullchainGroupSessionAffinityRedisDriver 生产数据面的全链黏组证据。
// 主测试夹具是 standalone memory 驱动（进程内 TTL 24h），生产 performance
// 模式为 JUHE_AI_CACHE_DRIVER=redis（TTL 1h）——组亲和驱动与账户级同源
// （chain_runtime.go redisCache 分支）。本测试以 miniredis 起 redis 缓存面
// （网关子进程经 TCP 直连），验证：同一会话经真实 Redis 读写黏住；TTL 过期
// （读不刷 TTL，快进即蒸发）后按模式重选一次并协议成功重绑，之后黏新组。
// 重选落点不断言到具体组（rr 计数器与绑定同库，TTL 归零后轮转位不确定），
// 黏住不变量以「重选后 5 连击全部同组」表达。
func TestFullchainGroupSessionAffinityRedisDriver(t *testing.T) {
	requireFullchainGate(t)
	cacheRedis := miniredis.RunT(t)
	f := startFullchainFixtureExtraEnv(t, func(_ string) map[string]string {
		return map[string]string{
			"JUHE_AI_CACHE_DRIVER":    "redis",
			"JUHE_AI_REDIS_CACHE_URL": "redis://" + cacheRedis.Addr(),
			"JUHE_AI_REDIS_NAMESPACE": "juhe-ai:fullchain-grpaff",
		}
	})

	keyR1 := fullchainUpstreamKey(t, "GroupAffinity-r1")
	keyR2 := fullchainUpstreamKey(t, "GroupAffinity-r2")
	routeR := f.newRoute("GroupAffinityRedis", "round_robin", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{{key: keyR1}}},
		{accounts: []fullchainAccountSpec{{key: keyR2}}},
	}, nil, nil)
	hitsR := func() map[string]int { return groupAffinityHits(f, keyR1, keyR2) }
	sessionR := "sess-group-affinity-redis"
	requireAffinityResponseOK(t, "GR-first", f.chatSessionAsCodex(t, routeR.apiKey, sessionR, responsesSessionBody("组亲和-Redis首绑")))
	if got := hitsR(); got[keyR1]+got[keyR2] != 1 {
		t.Fatalf("Redis 面：首请求应正常落组，命中 %v", got)
	}
	for i := 0; i < 5; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GR-%d", i),
			f.chatSessionAsCodex(t, routeR.apiKey, sessionR, responsesSessionBody(fmt.Sprintf("组亲和-Redis%d", i))))
	}
	firstGroupKey := keyR1
	otherGroupKey := keyR2
	if hitsR()[keyR2] > hitsR()[keyR1] {
		firstGroupKey, otherGroupKey = keyR2, keyR1
	}
	if got := hitsR(); got[firstGroupKey] != 6 || got[otherGroupKey] != 0 {
		t.Fatalf("Redis 面：同会话应经真实 Redis 读写黏住首绑组，命中 %v", got)
	}

	// TTL 过期：快进 2h（> 生产 1h TTL），绑定蒸发 → 下一请求按模式重选
	// 并协议成功重绑。
	cacheRedis.FastForward(2 * time.Hour)
	requireAffinityResponseOK(t, "GR-expired",
		f.chatSessionAsCodex(t, routeR.apiKey, sessionR, responsesSessionBody("组亲和-Redis过期重选")))
	beforeStick := hitsR()
	if beforeStick[keyR1]+beforeStick[keyR2] != 7 {
		t.Fatalf("Redis 面：TTL 过期后应恰好推进一请求（重选），命中 %v", beforeStick)
	}
	// 重选落点：过期前 (6,0)，重选后必为 (7,0) 或 (6,1)。
	reboundGroupKey := keyR1
	if beforeStick[keyR2] > 0 {
		reboundGroupKey = keyR2
	}
	for i := 0; i < 5; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("GR-stick-%d", i),
			f.chatSessionAsCodex(t, routeR.apiKey, sessionR, responsesSessionBody(fmt.Sprintf("组亲和-Redis新黏%d", i))))
	}
	if after := hitsR(); after[reboundGroupKey] != beforeStick[reboundGroupKey]+5 {
		t.Fatalf("Redis 面：过期重绑后 5 连击应全部黏住重绑组 %s，重选后 %v 最终 %v", reboundGroupKey, beforeStick, after)
	}
}
