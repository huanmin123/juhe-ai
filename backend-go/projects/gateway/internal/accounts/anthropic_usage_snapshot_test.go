package accounts

// anthropic_claude 用量快照读取投影测试（AI账户Grok用量快照设计 §8.3）：镜像
// xai_grok_usage_snapshot_test.go 写法，覆盖 claude 行解析（双窗/缺字段/坏
// 时间容错）、加载器 kind 过滤与 hydrate 按 provider 注入（anthropic 命中、
// xai 与 gpt 不命中）。

import (
	"context"
	"testing"
)

func TestAnthropicUsageSnapshotFromRowArms(t *testing.T) {
	// 完整快照：claude_5h/7d 双窗全部解析。reset_at 用远未来时间：窗口折叠
	// 逻辑对照真实当前时钟，近期时间会在过期后把利用率折叠为 0（w13g 惯例）。
	out, err := anthropicUsageSnapshotFromRow("gateway", `{"claude_usage_updated_at":"2026-09-27T00:00:00.000Z",
		"claude_5h_used_percent":14,"claude_5h_reset_at":"2099-09-27T04:00:00.000Z",
		"claude_7d_used_percent":7,"claude_7d_reset_at":"2099-09-28T02:34:03.000Z",
		"claude_unified_status":"allowed"}`, "ok", "2026-01-02T00:00:00Z", "2026-01-03T00:00:00Z",
		"2026-01-04T00:00:00Z", "", "2026-01-01T00:00:00Z")
	if err != nil || out == nil {
		t.Fatalf("完整快照：%v %v", out, err)
	}
	if out.Kind != "anthropic_claude" {
		t.Fatalf("kind：%+v", out)
	}
	if out.FiveHour == nil || out.FiveHour.Utilization != 14 || out.FiveHour.ResetsAt == nil ||
		*out.FiveHour.ResetsAt != "2099-09-27T04:00:00.000Z" {
		t.Fatalf("5h 窗：%+v", out.FiveHour)
	}
	if out.SevenDay == nil || out.SevenDay.Utilization != 7 || out.SevenDay.ResetsAt == nil ||
		*out.SevenDay.ResetsAt != "2099-09-28T02:34:03.000Z" {
		t.Fatalf("7d 窗：%+v", out.SevenDay)
	}
	if out.UsedPercent != nil || out.SubscriptionTier != nil {
		t.Fatalf("claude 快照不应携带 grok 形态字段：%+v", out)
	}

	// 缺 7d 窗与 status：不致命，5h 窗照常解析。
	out, err = anthropicUsageSnapshotFromRow("", `{"claude_5h_used_percent":14}`, "", "", "", "", "", "2026-01-01T00:00:00Z")
	if err != nil || out == nil {
		t.Fatalf("缺 7d 窗：%v %v", out, err)
	}
	if out.FiveHour == nil || out.SevenDay != nil {
		t.Fatalf("缺 7d 窗应整体省略：%+v %+v", out.FiveHour, out.SevenDay)
	}

	// reset_at 坏时间：该窗口报错（reset_at 是网关自写规范化值，坏值即数据错误）。
	if _, err := anthropicUsageSnapshotFromRow("", `{"claude_5h_used_percent":14,
		"claude_5h_reset_at":"not-a-time"}`, "", "", "", "", "", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("坏 reset_at 应报错")
	}

	// 共享头容错：snapshot_json 不可解析 → 跳过该行；坏 updated_at → 报错。
	if out, err := anthropicUsageSnapshotFromRow("", "{not-json", "", "", "", "", "", "2026-01-01T00:00:00Z"); err != nil || out != nil {
		t.Fatalf("坏 JSON 应跳过：%v %v", out, err)
	}
	if _, err := anthropicUsageSnapshotFromRow("", "", "", "", "", "", "", "bad"); err == nil {
		t.Fatal("坏 updated_at 应报错")
	}
}

func TestAnthropicUsageSnapshotLoaderKindFilter(t *testing.T) {
	env := newTestEnv(t)
	now := "2026-01-01T00:00:00Z"
	claudeJSON := `{"claude_5h_used_percent":14,"claude_5h_reset_at":"2099-09-27T04:00:00.000Z",
		"claude_7d_used_percent":7,"claude_unified_status":"allowed"}`
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json,
		refresh_status, created_at, updated_at) VALUES ('sys', 'acc-claude-1', 'anthropic_claude', 'gateway', ?, 'fresh', ?, ?)`,
		claudeJSON, now, now)
	// 同账户的 openai_codex 行不得串入，另一账户的 anthropic_claude 行应命中。
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json,
		refresh_status, created_at, updated_at) VALUES ('sys', 'acc-claude-1', 'openai_codex', 'codex', '{}', 'ok', ?, ?)`,
		now, now)
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json,
		refresh_status, created_at, updated_at) VALUES ('sys', 'acc-claude-2', 'anthropic_claude', 'gateway', ?, 'fresh', ?, ?)`,
		claudeJSON, now, now)

	// kind 过滤：只返回 anthropic_claude 行；空串与重复 id 去重。
	snapshots, err := env.store.loadAnthropicUsageSnapshots(context.Background(),
		[]string{"acc-claude-1", "acc-claude-1", "", "acc-claude-2"})
	if err != nil {
		t.Fatalf("加载失败：%v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("应命中两行：%+v", snapshots)
	}
	snapshot := snapshots["acc-claude-1"]
	if snapshot == nil || snapshot.Kind != "anthropic_claude" || snapshot.FiveHour == nil ||
		snapshot.FiveHour.Utilization != 14 {
		t.Fatalf("claude 快照字段：%+v", snapshot)
	}
	if snapshot.UsedPercent != nil {
		t.Fatalf("claude 快照不应命中 grok 形态字段：%+v", snapshot)
	}
	// 空入参 → 空映射。
	if snapshots, err := env.store.loadAnthropicUsageSnapshots(context.Background(), nil); err != nil || len(snapshots) != 0 {
		t.Fatalf("空入参：%v %v", snapshots, err)
	}
}

func TestAnthropicUsageHydrateArms(t *testing.T) {
	env := newTestEnv(t)
	now := "2026-01-01T00:00:00Z"
	env.exec(t, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind, source, snapshot_json,
		refresh_status, created_at, updated_at) VALUES ('sys', 'acc-claude-h', 'anthropic_claude', 'gateway', '{}', 'fresh', ?, ?)`,
		now, now)

	// 命中：anthropic + oauth 账户读取 claude 快照。
	items := []ListItem{{ID: "acc-claude-h", ProviderCode: "anthropic", Type: "oauth"}}
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), items); err != nil {
		t.Fatalf("hydrate 失败：%v", err)
	}
	if items[0].OAuthUsage == nil || items[0].OAuthUsage.Kind != "anthropic_claude" {
		t.Fatalf("anthropic 账户未回填 claude 快照：%+v", items[0].OAuthUsage)
	}

	// anthropic api_key 不回填；xai / gpt oauth 不命中 claude 快照（按 provider 分流）。
	roster := []ListItem{
		{ID: "acc-claude-h", ProviderCode: "anthropic", Type: "api_key"},
		{ID: "acc-claude-h", ProviderCode: "xai", Type: "oauth"},
		{ID: "acc-claude-h", ProviderCode: "gpt", Type: "oauth"},
	}
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), roster); err != nil {
		t.Fatalf("roster hydrate 失败：%v", err)
	}
	for index, label := range []string{"api_key", "xai", "gpt"} {
		if roster[index].OAuthUsage != nil {
			t.Fatalf("%s 账户不应命中 claude 快照：%+v", label, roster[index].OAuthUsage)
		}
	}

	// 实例账户：fact id 用源账户。
	sourceID := "acc-claude-h"
	instances := []ListItem{{ID: "acc-claude-inst", ProviderCode: "anthropic", Type: "oauth",
		AuthorizationInstanceSourceAccountID: &sourceID}}
	if err := env.store.hydrateOAuthUsageSnapshots(context.Background(), instances); err != nil {
		t.Fatalf("实例 hydrate 失败：%v", err)
	}
	if instances[0].OAuthUsage == nil || instances[0].OAuthUsage.Kind != "anthropic_claude" {
		t.Fatalf("实例应回填源账户快照：%+v", instances[0].OAuthUsage)
	}
}
