package apikeys

// API Key「非时间段」服务端筛选（《时间计划闸门与账户状态解耦设计》§5.4）：
// status=out_of_window 是虚拟筛选值，等价 status='active' AND
// availability_schedule_gate=0；它与 active / disabled 互斥。此前该筛选只在
// 前端对当前页做本地过滤，跨页会给出假阴性（总数按 active 算、匹配项在其他页）。

import (
	"context"
	"testing"
)

func TestListPageScheduleOutOfWindowFilter(t *testing.T) {
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
		{"非时间段只命中 active+gate=0", "out_of_window", []string{outside.ID}},
		{"active 命中全部启用 Key（含时段外）", "active", []string{active.ID, outside.ID}},
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

func TestAPIKeyStatusQueryValueAcceptsOutOfWindow(t *testing.T) {
	if got := apiKeyStatusQueryValue("out_of_window"); got != "out_of_window" {
		t.Fatalf("out_of_window = %q（必须是服务端虚拟筛选值，不能归一为空）", got)
	}
}
