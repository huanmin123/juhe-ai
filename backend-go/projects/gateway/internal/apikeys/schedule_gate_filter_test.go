package apikeys

// API Key 状态筛选与时间计划闸门的收敛（《时间计划闸门与账户状态解耦设计》
// §5.4，2026-11-07 修正）：虚拟筛选值 status=out_of_window 已退役，
// status='active' 天然包含时段外（gate=0）行；非时间段子状态由列表展示层
// 表达，不再有独立筛选臂。

import (
	"context"
	"testing"
)

func TestListPageStatusFilterCoversScheduleGate(t *testing.T) {
	env := newTestEnv(t)
	ownerID := env.login(t, "root", "root-pass", "super_admin")
	env.seedDefaultRouteStrategy(t, ownerID, "rs-gate")

	// 三个 Key：正常（gate NULL）、时段外（active + gate=0）、人工停用。
	active, _, err := env.store.Create(context.Background(), CreateInput{Name: "gate-active"}, AccessScope{ViewerID: ownerID})
	if err != nil {
		t.Fatal(err)
	}
	outside, _, err := env.store.Create(context.Background(), CreateInput{Name: "gate-outside"}, AccessScope{ViewerID: ownerID})
	if err != nil {
		t.Fatal(err)
	}
	disabled, _, err := env.store.Create(context.Background(), CreateInput{Name: "gate-disabled"}, AccessScope{ViewerID: ownerID})
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `UPDATE api_keys SET availability_schedule_gate = 0 WHERE id = ?`, outside.ID)
	env.exec(t, `UPDATE api_keys SET status = 'disabled' WHERE id = ?`, disabled.ID)

	cases := []struct {
		name   string
		status string
		want   []string
	}{
		{"active 命中全部启用 Key（天然包含时段外）", "active", []string{active.ID, outside.ID}},
		{"out_of_window 已退役：按不过滤处理返回全量", "out_of_window", []string{active.ID, outside.ID, disabled.ID}},
		{"disabled 只命中人工停用", "disabled", []string{disabled.ID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := env.store.ListPage(context.Background(), AccessScope{IsAdmin: true}, ListOptions{Status: tc.status})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != len(tc.want) {
				t.Fatalf("items=%d want %d（筛选 %q）: %+v", len(page.Items), len(tc.want), tc.status, page.Items)
			}
			got := map[string]bool{}
			for _, item := range page.Items {
				got[item.ID] = true
			}
			for _, id := range tc.want {
				if !got[id] {
					t.Fatalf("筛选 %q 缺 %s: %+v", tc.status, id, page.Items)
				}
			}
		})
	}
}

func TestAPIKeyStatusQueryValueRetiresOutOfWindow(t *testing.T) {
	if got := apiKeyStatusQueryValue("out_of_window"); got != "" {
		t.Fatalf("out_of_window 已退役，应归一为空（不过滤），得到 %q", got)
	}
}
