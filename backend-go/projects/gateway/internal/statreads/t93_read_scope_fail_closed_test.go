package statreads

// T93-8（R7-C C1-C18 裁决「修」）：统计读面非管理员空作用域 fail-closed。
// 认证上下文缺 SystemAccountID 时 scopedID 为空，读面 SQL 会省略
// system_account_id 条件退化成跨租户全量读——handler 一律 403，SQL 组装层
// 追加 `1 = 0` 作为第二道保险。管理员 all-scope（空过滤 = 全站视图）与正常
// 用户（钉到自身）行为不变。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// t93SeedUsageRecord 插入一条指定租户的用量记录（默认窗口 = fixture now 当天）。
func t93SeedUsageRecord(t *testing.T, fixture *testFixture, id, systemAccountID string) {
	t.Helper()
	if _, err := fixture.db.Exec(`CREATE TABLE IF NOT EXISTS juhe_usage.usage_records_default
		PARTITION OF juhe_usage.usage_records DEFAULT`); err != nil &&
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("创建 usage_records 默认分区: %v", err)
	}
	if _, err := fixture.db.Exec(`INSERT INTO juhe_usage.usage_records
		(id, system_account_id, trace_id, model, success, created_at)
		VALUES ($1, $2, $1 || '-trace', 'gpt-4o-mini', 1, '2026-09-04T10:00:00.000Z')`,
		id, systemAccountID); err != nil {
		t.Fatalf("seed usage record %s: %v", id, err)
	}
}

func TestT93StatReadsFailClosedWithoutTenantScope(t *testing.T) {
	fixture := newFixture(t)
	t93SeedUsageRecord(t, fixture, "ur-t93-a", "sys-t93-a")
	t93SeedUsageRecord(t, fixture, "ur-t93-b", "sys-t93-b")

	// 认证上下文缺 SystemAccountID 的非管理员：所有读面 403，不返回任何行。
	missingIdentity := &authsys.AuthContext{Username: "ghost", Role: "user"}
	handlers := []struct {
		name    string
		handler http.HandlerFunc
		target  string
	}{
		{"usage-records", fixture.deps.usageRecordsListHandler(false), "/__aisys__/api/usage-records"},
		{"my-usage-records", fixture.deps.usageRecordsListHandler(true), "/__aisys__/api/my-usage-records"},
		{"usage-overview", fixture.deps.overviewSectionHandler(false), "/__aisys__/api/stats/usage-overview/summary"},
		{"ai-performance", fixture.deps.aiPerformanceBaseHandler(false), "/__aisys__/api/stats/ai-performance"},
		{"ai-health", fixture.deps.aiHealthListHandler(false), "/__aisys__/api/stats/ai-health"},
		{"account-usage", fixture.deps.accountUsageHandler(false), "/__aisys__/api/stats/account-usage"},
	}
	for _, item := range handlers {
		recorder := invoke(t, item.handler, http.MethodGet, item.target, missingIdentity)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s 空作用域应 403：%d %s", item.name, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), statReadsMissingIdentityMessage) {
			t.Fatalf("%s 403 文案不符：%s", item.name, recorder.Body.String())
		}
	}

	// 管理员 all-scope（不传 systemAccountId）不受影响：跨租户全站可读。
	adminRecorder := invoke(t, fixture.deps.usageRecordsListHandler(false), http.MethodGet,
		"/__aisys__/api/usage-records", adminAuth("super_admin"))
	if adminRecorder.Code != http.StatusOK {
		t.Fatalf("管理员全站读应 200：%d %s", adminRecorder.Code, adminRecorder.Body.String())
	}
	if total := dataMap(t, decodeBody(t, adminRecorder))["total"]; total != float64(2) {
		t.Fatalf("管理员应看到全部 2 条：%v", total)
	}

	// 正常用户仍钉到自身：只看到自己的 1 条。
	userRecorder := invoke(t, fixture.deps.usageRecordsListHandler(false), http.MethodGet,
		"/__aisys__/api/usage-records", &authsys.AuthContext{SystemAccountID: "sys-t93-a", Role: "user"})
	if userRecorder.Code != http.StatusOK {
		t.Fatalf("普通用户读应 200：%d %s", userRecorder.Code, userRecorder.Body.String())
	}
	if total := dataMap(t, decodeBody(t, userRecorder))["total"]; total != float64(1) {
		t.Fatalf("普通用户只应看到自己 1 条：%v", total)
	}
}

// TestT93UsageRecordFiltersNeverDropTenantScope 钉住 SQL 组装层的第二道保险：
// 非管理员空作用域不得省略 system_account_id 条件（退化为全租户读），管理员
// 空过滤保持全站语义。
func TestT93UsageRecordFiltersNeverDropTenantScope(t *testing.T) {
	fixture := newFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/usage-records", nil)

	empty, err := fixture.deps.usageRecordFilters(request, AccessScope{}, usageRecordListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty.clause, "1 = 0") {
		t.Fatalf("非管理员空作用域必须收窄为空集：%q", empty.clause)
	}

	user, err := fixture.deps.usageRecordFilters(request, AccessScope{ViewerID: "sys-t93-a"}, usageRecordListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(user.clause, "ur.system_account_id = ?") || len(user.params) != 1 {
		t.Fatalf("普通用户必须带租户条件：%q %v", user.clause, user.params)
	}

	admin, err := fixture.deps.usageRecordFilters(request,
		AccessScope{ViewerID: "sys-admin-1", IsAdmin: true}, usageRecordListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(admin.clause, "system_account_id") || strings.Contains(admin.clause, "1 = 0") {
		t.Fatalf("管理员全站视图不得带租户条件：%q", admin.clause)
	}
}
