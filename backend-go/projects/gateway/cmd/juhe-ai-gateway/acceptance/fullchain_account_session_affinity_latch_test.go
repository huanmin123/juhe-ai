package acceptance

// 账户亲和同层单向粘性 fullchain 验收（账户亲和同层单向粘性与会话名额设计
// §10.1 验收 1）：
//  1. 会话首请求在同层账户 A 上协议成功并建立绑定；
//  2. A 硬并发满（无会话请求持流占用唯一并发槽）时，会话请求走同层 B 并
//     最终协议成功，绑定 CAS 重绑 A→B；
//  3. 放行 A 后连续 20 次会话请求仍全部落 B，不因 A 恢复空闲回切。
//
// 该用例专门覆盖“准备期容量排序把硬满 owner 降位、请求直接成功在 B 上而
// 从未尝试 owner”的路径：若只依赖派发期容量失败，重绑不会发生，A 空闲后
// 下一次请求会回切 A。

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 本用例走 fullchain 夹具的 localSessionAffinity 降级适配器（夹具不注入
// Identity 依赖）；生产 Redis 适配器的同层重绑与软阈值排序由 cmd 包
// TestChainSessionAffinityDispatchPortRebind / SoftThresholdOrder 覆盖。

func TestFullchainAccountSessionAffinityLatch(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	keyA := fullchainUpstreamKey(t, "AcctLatch-a")
	keyB := fullchainUpstreamKey(t, "AcctLatch-b")
	route := f.newRoute("AcctLatch", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{key: keyA, extra: map[string]any{"concurrencyLimit": 1}},
			{key: keyB},
		}},
	}, nil, nil)

	hits := func() (int, int) {
		return len(f.mock.protocolCallsByKey(keyA)), len(f.mock.protocolCallsByKey(keyB))
	}

	// —— 1. 会话首请求按同层顺序落 A 并绑定。
	session := "sess-acct-latch"
	requireAffinityResponseOK(t, "latch-first",
		f.chatSessionAsCodex(t, route.apiKey, session, responsesSessionBody("账户latch-首绑")))
	if a, b := hits(); a != 1 || b != 0 {
		t.Fatalf("首请求应落 A，命中 A=%d B=%d", a, b)
	}

	// —— 2. 无会话请求持流占满 A 的唯一并发槽。
	f.mock.script(keyA, fullchainHoldScenario)
	t.Cleanup(func() { f.mock.releaseHold(keyA) })
	holdDone := make(chan fullchainChatResponse, 1)
	go func() {
		holdDone <- f.chat(route.apiKey, chatBody(acceptanceModel, "账户latch-持流"))
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if a, _ := hits(); a >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("持流请求未命中 A：A 命中数=%d", len(f.mock.protocolCallsByKey(keyA)))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 会话 owner A 硬满：准备期容量排序把 A 降位，请求直接成功在 B；
	// 准备期 busy 标志驱动最终结算重绑 A→B。
	requireAffinityResponseOK(t, "latch-failover",
		f.chatSessionAsCodex(t, route.apiKey, session, responsesSessionBody("账户latch-切换")))
	if a, b := hits(); a != 2 || b != 1 {
		t.Fatalf("A 硬满时应由 B 承接且重绑，命中 A=%d B=%d", a, b)
	}

	// —— 3. 放行 A，确认持流请求成功；随后 20 次会话请求必须全部落 B。
	f.mock.releaseHold(keyA)
	hold := <-holdDone
	requireAffinityResponseOK(t, "latch-hold", hold)
	for i := 0; i < 20; i++ {
		requireAffinityResponseOK(t, fmt.Sprintf("latch-stick-%d", i),
			f.chatSessionAsCodex(t, route.apiKey, session, responsesSessionBody(fmt.Sprintf("账户latch-粘住%d", i))))
	}
	if a, b := hits(); a != 2 || b != 21 {
		t.Fatalf("A 恢复空闲后不得回切：期望 A=2 B=21，实际 A=%d B=%d", a, b)
	}

	// —— 4. 运行中网关的 Prometheus 面必须已暴露 latch 指标（不是只在单测注册表里）。
	metrics := scrapeGatewayMetrics(t, f)
	for _, name := range []string{
		"juhe_ai_gateway_session_affinity_rebind_total",
		"juhe_ai_gateway_session_affinity_cross_account_switch_total",
		"juhe_ai_gateway_session_affinity_lease_hit_total",
		"juhe_ai_gateway_session_affinity_back_switch_prevented_total",
	} {
		requirePositiveMetric(t, metrics, name)
	}
	if !strings.Contains(metrics, `reason="capacity"`) {
		t.Fatalf("rebind 指标缺少 capacity reason 标签:\n%s", metrics)
	}
}

func scrapeGatewayMetrics(t *testing.T, f *fullchainFixture) string {
	t.Helper()
	response, err := http.Get(f.gw.healthURL + "/__aisys__/metrics")
	if err != nil {
		t.Fatalf("scrape gateway metrics: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read gateway metrics: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("gateway metrics status=%d body=%s", response.StatusCode, body)
	}
	return string(body)
}

func requirePositiveMetric(t *testing.T, metrics, name string) {
	t.Helper()
	for _, line := range strings.Split(metrics, "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		if strings.HasSuffix(line, " 0") {
			continue
		}
		return
	}
	t.Fatalf("metric %s not positive/rendered:\n%s", name, metrics)
}
