package accounts

// xai_grok 用量快照读取投影测试（AI账户Grok用量快照设计 §5）：镜像
// w13g oauth_usage_snapshot 测试写法，覆盖 grok 行解析（完整/缺字段/坏时间
// 容错）、加载器 kind 过滤与 hydrate 按 provider 注入。
//
// 不可达登记（与 w13g 同一结论，不重复构造）：
// - 加载器 rows.Scan / rows.Err 故障臂在单连接下无故障注入口（纯函数侧已
//   覆盖同一解析矩阵）。
// - 共享头 last_attempt_at/last_success_at/next_refresh_after 坏时间臂由
//   w13g TestW13GOAuthUsageSnapshotFromRowArms 覆盖，grok 侧共享同一实现。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestXAIGrokUsageSnapshotFromRowArms(t *testing.T) {
	// 完整快照：grok_* 字段全部解析，productUsage 原样透传。
	out, err := xaiGrokUsageSnapshotFromRow("xai_grok_billing", `{"grok_credit_used_percent":14,
		"grok_period_type":"USAGE_PERIOD_TYPE_WEEKLY",
		"grok_period_start":"2026-09-21T02:34:03Z","grok_period_end":"2026-09-28T02:34:03Z",
		"grok_subscription_tier":"SuperGrok Heavy",
		"grok_product_usage_json":"[{\"product\":\"GrokBuild\",\"usagePercent\":13}]",
		"grok_on_demand_used_percent":2.5}`, "ok", "2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z",
		"2026-01-04T00:00:00Z", "boom", "2026-01-01T00:00:00Z")
	if err != nil || out == nil {
		t.Fatalf("完整快照：%v %v", out, err)
	}
	if out.Kind != "xai_grok" || out.UsedPercent == nil || *out.UsedPercent != 14 {
		t.Fatalf("用量百分比：%+v", out)
	}
	if out.PeriodType == nil || *out.PeriodType != "USAGE_PERIOD_TYPE_WEEKLY" ||
		out.PeriodStart == nil || *out.PeriodStart != "2026-09-21T02:34:03Z" ||
		out.PeriodEnd == nil || *out.PeriodEnd != "2026-09-28T02:34:03Z" {
		t.Fatalf("周期字段：%+v", out)
	}
	if out.SubscriptionTier == nil || *out.SubscriptionTier != "SuperGrok Heavy" {
		t.Fatalf("套餐名：%+v", out)
	}
	if string(out.ProductUsage) != `[{"product":"GrokBuild","usagePercent":13}]` {
		t.Fatalf("productUsage 应原样透传：%s", out.ProductUsage)
	}
	var products []map[string]any
	if err := json.Unmarshal(out.ProductUsage, &products); err != nil || len(products) != 1 {
		t.Fatalf("productUsage 应为合法 JSON：%s %v", out.ProductUsage, err)
	}
	if out.OnDemandUsedPercent == nil || *out.OnDemandUsedPercent != 2.5 {
		t.Fatalf("按量池百分比：%+v", out)
	}
	if out.Source == nil || *out.Source != "xai_grok_billing" || out.RefreshStatus == nil ||
		out.LastErrorMessage == nil || *out.LastErrorMessage != "boom" {
		t.Fatalf("刷新状态头：%+v", out)
	}

	// 缺 grok_credit_used_percent：不致命，其余字段照常解析。
	out, err = xaiGrokUsageSnapshotFromRow("", `{"grok_period_type":"USAGE_PERIOD_TYPE_MONTHLY",
		"grok_subscription_tier":"SuperGrok"}`, "", "", "", "", "", "2026-01-01T00:00:00Z")
	if err != nil || out == nil {
		t.Fatalf("缺用量百分比：%v %v", out, err)
	}
	if out.UsedPercent != nil || out.PeriodType == nil || out.SubscriptionTier == nil {
		t.Fatalf("缺用量百分比应只省略该字段：%+v", out)
	}

	// 时间字段解析失败：跳过该字段，不致命。
	out, err = xaiGrokUsageSnapshotFromRow("", `{"grok_credit_used_percent":30,
		"grok_period_start":"not-a-time","grok_period_end":123}`, "", "", "", "", "", "2026-01-01T00:00:00Z")
	if err != nil || out == nil {
		t.Fatalf("坏时间应容错：%v %v", out, err)
	}
	if out.UsedPercent == nil || *out.UsedPercent != 30 || out.PeriodStart != nil || out.PeriodEnd != nil {
		t.Fatalf("坏时间字段应跳过：%+v", out)
	}

	// product_usage_json 非法 JSON：整体省略。
	out, err = xaiGrokUsageSnapshotFromRow("", `{"grok_credit_used_percent":1,
		"grok_product_usage_json":"{not-json"}`, "", "", "", "", "", "2026-01-01T00:00:00Z")
	if err != nil || out == nil || out.ProductUsage != nil {
		t.Fatalf("非法 productUsage 应省略：%+v %v", out, err)
	}

	// 共享头容错：snapshot_json 不可解析 → 跳过该行；坏 updated_at → 报错。
	if out, err := xaiGrokUsageSnapshotFromRow("", "{not-json", "", "", "", "", "", "2026-01-01T00:00:00Z"); err != nil || out != nil {
		t.Fatalf("坏 JSON 应跳过：%v %v", out, err)
	}
	if _, err := xaiGrokUsageSnapshotFromRow("", "", "", "", "", "", "", "bad"); err == nil ||
		!strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("坏 updated_at：%v", err)
	}
}

func TestXAIGrokUsageSnapshotLoaderKindFilter(t *testing.T) {
	env := newTestEnv(t)
	now := "2026-01-01T00:00:00Z"
	grokJSON := `{"grok_credit_used_percent":14,"grok_period_type":"USAGE_PERIOD_TYPE_WEEKLY",
		"grok_period_start":"2026-09-21T02:34:03Z","grok_period_end":"2026-09-28T02:34:03Z",
		"grok_subscription_tier":"SuperGrok Heavy"}`
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json,
		refresh_status, created_at, updated_at) VALUES ('sys', 'acc-grok-1', 'xai_grok', 'xai_grok_billing', ?, 'ok', ?, ?)`,
		grokJSON, now, now)
	// 同账户的 openai_codex 行不得串入，另一账户的 xai_grok 行应命中。
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json,
		refresh_status, created_at, updated_at) VALUES ('sys', 'acc-grok-1', 'openai_codex', 'codex', '{}', 'ok', ?, ?)`,
		now, now)
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json,
		refresh_status, created_at, updated_at) VALUES ('sys', 'acc-grok-2', 'xai_grok', 'xai_grok_billing', ?, 'ok', ?, ?)`,
		grokJSON, now, now)

	// kind 过滤：只返回 xai_grok 行；空串与重复 id 去重。
	snapshots, err := env.store.loadXAIGrokUsageSnapshots(context.Background(),
		[]string{"acc-grok-1", "acc-grok-1", "", "acc-grok-2"})
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("应命中两行：%+v", snapshots)
	}
	snapshot := snapshots["acc-grok-1"]
	if snapshot == nil || snapshot.Kind != "xai_grok" || snapshot.UsedPercent == nil ||
		*snapshot.UsedPercent != 14 || snapshot.SubscriptionTier == nil ||
		*snapshot.SubscriptionTier != "SuperGrok Heavy" {
		t.Fatalf("grok 快照字段：%+v", snapshot)
	}
	if snapshot.FiveHour != nil || snapshot.SevenDay != nil {
		t.Fatalf("grok 快照不应有 codex 窗口：%+v", snapshot)
	}
	// 空入参 → 空映射。
	if snapshots, err := env.store.loadXAIGrokUsageSnapshots(context.Background(), nil); err != nil || len(snapshots) != 0 {
		t.Fatalf("空入参：%v %v", snapshots, err)
	}
}

func TestXAIGrokUsageHydrateArms(t *testing.T) {
	env := newTestEnv(t)
	now := "2026-01-01T00:00:00Z"
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json,
		refresh_status, created_at, updated_at) VALUES ('sys', 'acc-grok-h', 'xai_grok', 'xai_grok_billing', '{}', 'ok', ?, ?)`,
		now, now)

	// 命中：xai + oauth 账户读取 grok 快照。
	items := []ListItem{{ID: "acc-grok-h", ProviderCode: "xai", Type: "oauth"}}
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), items); err != nil {
		t.Fatalf("hydrate 失败：%v", err)
	}
	if items[0].OAuthUsage == nil || items[0].OAuthUsage.Kind != "xai_grok" {
		t.Fatalf("xai 账户未回填 grok 快照：%+v", items[0].OAuthUsage)
	}

	// xai api_key 不回填；gpt oauth 不命中 grok 快照（按 provider 分流）。
	roster := []ListItem{
		{ID: "acc-grok-h", ProviderCode: "xai", Type: "api_key"},
		{ID: "acc-grok-h", ProviderCode: "gpt", Type: "oauth"},
	}
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), roster); err != nil {
		t.Fatalf("roster hydrate 失败：%v", err)
	}
	if roster[0].OAuthUsage != nil {
		t.Fatalf("api_key 不应回填：%+v", roster[0].OAuthUsage)
	}
	if roster[1].OAuthUsage != nil {
		t.Fatalf("gpt 账户不应命中 grok 快照：%+v", roster[1].OAuthUsage)
	}

	// 实例账户：fact id 用源账户。
	sourceID := "acc-grok-h"
	instances := []ListItem{{ID: "acc-grok-inst", ProviderCode: "xai", Type: "oauth",
		AuthorizationInstanceSourceAccountID: &sourceID}}
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), instances); err != nil {
		t.Fatalf("实例 hydrate 失败：%v", err)
	}
	if instances[0].OAuthUsage == nil || instances[0].OAuthUsage.Kind != "xai_grok" {
		t.Fatalf("实例应回填源账户快照：%+v", instances[0].OAuthUsage)
	}

	// 空列表与无命中。
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), nil); err != nil {
		t.Fatalf("空列表：%v", err)
	}
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(),
		[]ListItem{{ID: "x", ProviderCode: "anthropic", Type: "oauth"}}); err != nil {
		t.Fatalf("无命中：%v", err)
	}

	// 快照表缺失 → 传播。
	env.exec(t, `DROP TABLE account_usage_snapshots`)
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), items); err == nil {
		t.Fatal("快照表缺失应报错")
	}
}
