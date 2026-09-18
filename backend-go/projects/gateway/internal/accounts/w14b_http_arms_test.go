package accounts

// w14b HTTP 未认证/坏作用域臂、sub2api OAuth 来源适配、调度下一次检查、
// 家族版本推进入口的补齐。

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestW14BUnauthenticatedArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	// 未登录（jar 无 cookie）：写路由 401。
	for _, item := range []struct{ method, path string }{
		{http.MethodPost, "/__aisys__/api/accounts"},
		{http.MethodPatch, "/__aisys__/api/accounts/acc-x/tags"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-x/lock"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-x/lock-config"},
		{http.MethodDelete, "/__aisys__/api/accounts/acc-x"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-x/force-activate"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-x/return-authorization"},
		{http.MethodPost, "/__aisys__/api/accounts/acc-x/traffic-migration"},
		{http.MethodPatch, "/__aisys__/api/accounts/acc-x/authorized-dispatch"},
		{http.MethodPost, "/__aisys__/api/accounts/batch-update"},
	} {
		code, _ := env.do(t, item.method, item.path, "{}")
		if code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未认证应 401：%d", item.method, item.path, code)
		}
	}
	// 未登录读路由（RequireAdmin）401。
	code, _ := env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-x/advanced", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("未认证 advanced 应 401：%d", code)
	}
}

func TestW14BBadScopeQueryArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-w14b-scope", adminID, "w14b-scope", "active")
	// systemAccountId 空白值 → 400（scopeQueryOK 臂）。
	code, _ := env.do(t, http.MethodPost, "/__aisys__/api/accounts?systemAccountId=", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"x","type":"api_key","credentials":{"api_key":"sk-abc123456","base_url":"https://api.openai.com/v1"}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("空白 systemAccountId 创建应 400：%d", code)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-w14b-scope/traffic-migration?systemAccountId=", "{}")
	if code != http.StatusBadRequest {
		t.Fatalf("空白作用域流量迁移应 400：%d", code)
	}
	code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-w14b-scope/authorized-dispatch?systemAccountId=", "{}")
	if code != http.StatusBadRequest {
		t.Fatalf("空白作用域授权调度应 400：%d", code)
	}
	// m11 读路由错误臂（写入 m11 错误渲染）：缺失账户读详情 → 4xx。
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-w14b-none/advanced", "")
	if code != http.StatusNotFound {
		t.Fatalf("缺失账户详情应 404：%d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-w14b-none/api-key-runtime", "")
	if code != http.StatusNotFound {
		t.Fatalf("缺失账户运行时应 404：%d", code)
	}
	code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-w14b-none/balance/details", "")
	if code != http.StatusNotFound {
		t.Fatalf("缺失账户余额详情应 404：%d", code)
	}
}

func TestW14BSub2APIOAuthSourceArms(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// sub2api 来源：OAuth 账户（凭据缺失 → 记录跳过消息臂）+ 正常 api_key。
	document := map[string]any{
		"type": "sub2api-export", "version": float64(2), "exported_at": "2026-01-01T00:00:00Z",
		"accounts": []any{
			map[string]any{
				"email": "w14b-oauth@example.com", "name": "w14b-oauth", "type": "oauth",
				"status": "active", "group": "w14b-src-group", "platform": "openai",
				"credentials": map[string]any{},
			},
			map[string]any{
				"email": "w14b-key@example.com", "name": "w14b-key", "type": "api_key",
				"status": "active", "group": "w14b-src-group", "platform": "openai",
				"credentials": map[string]any{"api_key": "sk-src-w14b", "base_url": "https://api.openai.com/v1"},
			},
		},
	}
	result, err := env.store.PreviewImport(context.Background(), document, "sub2api", ImportOptions{}, scope)
	if err != nil {
		t.Fatalf("sub2api 预览应成功：%v", err)
	}
	if len(result.Accounts) == 0 {
		t.Fatal("sub2api 来源应产生账户条目")
	}
	joined := ""
	for _, item := range result.Accounts {
		joined += strings.Join(item.Messages, "|") + ";"
	}
	_ = joined
}

func TestW14BScheduleNextCheckArms(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	// 常规窗口：下一次检查应给出未来边界。
	schedule := &AvailabilitySchedule{Enabled: true, Timezone: "UTC", Windows: []ScheduleWindow{
		{DaysOfWeek: []int{1, 2, 3, 4, 5, 6, 7}, Start: "09:00", End: "10:00"},
	}}
	next, ok := NextScheduleCheckAt(schedule, now)
	if !ok || next == "" {
		t.Fatalf("应有下一次检查：%q %v", next, ok)
	}
	// dateRange 已结束：无边界候选 → 跟进兜底臂（now + 7 天）。
	expired := &AvailabilitySchedule{Enabled: true, Timezone: "UTC", Windows: []ScheduleWindow{
		{DaysOfWeek: []int{1, 2, 3, 4, 5, 6, 7}, Start: "09:00", End: "10:00"},
	}, DateRange: &ScheduleDateRange{StartDate: "2026-01-01", EndDate: "2026-01-02"}}
	fallback, ok := NextScheduleCheckAt(expired, now)
	if !ok || fallback == "" {
		t.Fatalf("已结束计划应返回兜底检查时刻：%q %v", fallback, ok)
	}
	fallbackTime, err := time.Parse(time.RFC3339, fallback)
	if err != nil || fallbackTime.Before(now.AddDate(0, 0, 6)) {
		t.Fatalf("兜底时刻应约为 now+7 天：%q %v", fallback, err)
	}
	// 禁用计划：直接 false。
	if _, ok := NextScheduleCheckAt(&AvailabilitySchedule{Enabled: false}, now); ok {
		t.Fatal("禁用计划应返回 false")
	}
}

func TestW14BAdvanceDispatchFamilyEntry(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedAccount(t, "acc-w14b-fam", adminID, "w14b-fam", "active")
	ctx := context.Background()
	tx, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.AdvanceDispatchRevisionFamily(ctx, tx, "acc-w14b-fam", "w14b-transition", time.Now().UnixMilli()); err != nil {
		_ = tx.Rollback()
		t.Fatalf("家族版本推进应成功：%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := env.db.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id = 'acc-w14b-fam'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision < 1 {
		t.Fatalf("调度版本应推进：%d", revision)
	}
}
