// 验收矩阵开放项补齐（计划 §3 G3，原 Stage2 套件未覆盖；2026-09-18 补；
// R6 hybrid_smart 验收项随 hybrid 路由模式移除一并删除）。
//
//	G3 组内 tier 序：superPriority 恒先（chain_accounts.go 候选 SQL 的
//	local_super_priority_enabled DESC 先于 local_priority）；fallbackEnabled
//	仅在非 fallback 耗尽后派发（local_fallback_enabled ASC 殿后）。
//
// 独立顶层测试：不改动既有 TestFullchainStage2Matrix 的子测试注册表，复用
// 同一 fullchain fixture 与 mock 面（env 门控与 Stage2 一致）。
package acceptance

import (
	"database/sql"
	"net/http"
	"testing"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// G3_own_form_super_priority_via_account_flag（T62 排查矩阵 C 层，2026-10-24）：
// 生产自有账户形态端到端——超级优先写在 accounts.super_priority_enabled、
// 绑定行 local_super_priority_enabled 显式 0。该形态无法经 API 写链构造
// （write.go 创建/编辑会把 accounts.* 镜像同步进 local_*，永远双 1），只能
// 直插业务库构造。断言派发读侧 OR 合并全链生效：绑定 0 + 账户 1 仍先于更低
// priority 的普通账户被命中；双向对照（账户级也清零）回落普通账户。
func testFullchainOwnFormSuperPriorityCase(t *testing.T, f *fullchainFixture) {
	t.Helper()
	superKey := fullchainUpstreamKey(t, "G3O-super")
	normalKey := fullchainUpstreamKey(t, "G3O-normal")
	route := f.newRoute("G3O", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{key: normalKey, defaultScenario: platformmock.ScenarioChatOK,
				extra: map[string]any{"priority": 100}},
			{key: superKey, defaultScenario: platformmock.ScenarioChatOK,
				extra: map[string]any{"priority": 200, "superPriorityEnabled": true}},
		}},
	}, nil, nil)

	db, err := sql.Open("sqlite", f.gw.storage["business"])
	if err != nil {
		t.Fatalf("open business db: %v", err)
	}
	defer db.Close()
	// 生产自有账户形态：账户级 1 保持，绑定级清零（API 写链本会写 1）。
	if _, err := db.Exec(`UPDATE group_accounts SET local_super_priority_enabled = 0
		WHERE account_id IN (SELECT id FROM accounts WHERE super_priority_enabled = 1)`); err != nil {
		t.Fatalf("zero binding local super flag: %v", err)
	}

	response := f.chat(route.apiKey, chatBody(acceptanceModel, "G3-own-form"))
	if response.Status != http.StatusOK {
		t.Fatalf("own-form super status=%d body=%s", response.Status, response.Body)
	}
	if got := len(f.mock.protocolCallsByKeyModel(superKey, acceptanceModel)); got != 1 {
		t.Fatalf("own-form super calls=%d want 1（账户级超级优先 + 绑定 0 必须仍先于普通账户命中）", got)
	}
	if got := len(f.mock.protocolCallsByKeyModel(normalKey, acceptanceModel)); got != 0 {
		t.Fatalf("own-form normal calls=%d want 0", got)
	}

	// 双向对照：账户级也清零后超级优先完全消失，回落普通账户。负对照用独立
	// 路由（新 API Key = 新会话亲和域）——第一个请求已把会话绑定到 super 账
	// 户，同 Key 复用会因亲和继续命中它，污染「标志清零后回落」的断言。
	if _, err := db.Exec(`UPDATE accounts SET super_priority_enabled = 0 WHERE super_priority_enabled = 1`); err != nil {
		t.Fatalf("clear account-level super flag: %v", err)
	}
	clearRoute := f.newRoute("G3O2", "normal", []fullchainGroupSpec{
		{accounts: []fullchainAccountSpec{
			{key: normalKey, defaultScenario: platformmock.ScenarioChatOK},
			{key: superKey, defaultScenario: platformmock.ScenarioChatOK},
		}},
	}, nil, nil)
	response = f.chat(clearRoute.apiKey, chatBody(acceptanceModel, "G3-own-form-cleared"))
	if response.Status != http.StatusOK {
		t.Fatalf("after-clear status=%d body=%s", response.Status, response.Body)
	}
	if got := len(f.mock.protocolCallsByKeyModel(superKey, acceptanceModel)); got != 1 {
		t.Fatalf("cleared super calls=%d want 1（不再新增）", got)
	}
	if got := len(f.mock.protocolCallsByKeyModel(normalKey, acceptanceModel)); got != 1 {
		t.Fatalf("cleared normal calls=%d want 1（无任何超级优先后回落普通账户）", got)
	}
}

// TestFullchainStage2OpenItemsOwnForm 与 TestFullchainStage2OpenItems 同门控、
// 独立顶层测试（G3 自有形态端到端，见 testFullchainOwnFormSuperPriorityCase）。
func TestFullchainStage2OpenItemsOwnForm(t *testing.T) {
	f := startFullchainFixture(t)
	testFullchainOwnFormSuperPriorityCase(t, f)
}

// TestFullchainStage2OpenItems G3（计划 §3 开放项验收门）。
func TestFullchainStage2OpenItems(t *testing.T) {
	f := startFullchainFixture(t)

	t.Run("G3_super_priority_first", func(t *testing.T) {
		normalKey := fullchainUpstreamKey(t, "G3S-normal")
		superKey := fullchainUpstreamKey(t, "G3S-super")
		route := f.newRoute("G3S", "normal", []fullchainGroupSpec{
			{accounts: []fullchainAccountSpec{
				// 非 super 高优先级：若 tier 序不生效，它会先被命中。
				{key: normalKey, defaultScenario: platformmock.ScenarioChatOK,
					extra: map[string]any{"priority": 100}},
				{key: superKey, defaultScenario: platformmock.ScenarioChatOK,
					extra: map[string]any{"priority": 200, "superPriorityEnabled": true}},
			}},
		}, nil, nil)

		response := f.chat(route.apiKey, chatBody(acceptanceModel, "G3-super"))
		if response.Status != http.StatusOK {
			t.Fatalf("G3 super status=%d body=%s", response.Status, response.Body)
		}
		// 按 key 维度断言（全局 protocolCalls 序列跨子测试累积，不可用）。
		if got := len(f.mock.protocolCallsByKeyModel(superKey, acceptanceModel)); got != 1 {
			t.Fatalf("G3 superPriority calls=%d want 1（superPriority 必须先于更低 priority 的普通账户被命中）", got)
		}
		if got := len(f.mock.protocolCallsByKeyModel(normalKey, acceptanceModel)); got != 0 {
			t.Fatalf("G3 normal calls=%d want 0（superPriority 存在时普通账户不应先被派发）", got)
		}
	})

	t.Run("G3_fallback_after_non_fallback_exhausted", func(t *testing.T) {
		fallbackKey := fullchainUpstreamKey(t, "G3F-fallback")
		normalKey := fullchainUpstreamKey(t, "G3F-normal")
		route := f.newRoute("G3F", "normal", []fullchainGroupSpec{
			{accounts: []fullchainAccountSpec{
				// fallback 账户虽然 priority 更高（100 < 200），tier 序要求
				// 它殿后：只有非 fallback 候选耗尽后才可派发。
				{key: fallbackKey, defaultScenario: platformmock.ScenarioChatOK,
					extra: map[string]any{"priority": 100, "fallbackEnabled": true}},
				{key: normalKey, defaultScenario: platformmock.ScenarioStatus500,
					extra: map[string]any{"priority": 200}},
			}},
		}, nil, nil)

		response := f.chat(route.apiKey, chatBody(acceptanceModel, "G3-fallback"))
		if response.Status != http.StatusOK {
			t.Fatalf("G3 fallback status=%d body=%s", response.Status, response.Body)
		}
		// 同账户瞬态重试（F1 语义）允许 normal 被尝试多次，但 fallback 必须
		// 恰好一次且晚于全部 normal 尝试（非 fallback 耗尽后才派发）。
		normalCalls := f.mock.protocolCallsByKeyModel(normalKey, acceptanceModel)
		fallbackCalls := f.mock.protocolCallsByKeyModel(fallbackKey, acceptanceModel)
		if len(normalCalls) < 1 {
			t.Fatalf("G3 normal calls=%d want >=1（非 fallback 候选应先被派发）", len(normalCalls))
		}
		if len(fallbackCalls) != 1 {
			t.Fatalf("G3 fallback calls=%d want 1（fallback 只能在非 fallback 耗尽后派发一次）", len(fallbackCalls))
		}
		if !fallbackCalls[0].At.After(normalCalls[len(normalCalls)-1].At) {
			t.Fatalf("G3 时序错误：fallback 必须晚于全部非 fallback 尝试（normal 末次=%v fallback=%v）",
				normalCalls[len(normalCalls)-1].At, fallbackCalls[0].At)
		}
	})
}
