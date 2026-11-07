package accounts

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestConcurrencyAndPriorityInt4Bounds 锁定 T173：concurrencyLimit / priority
// 与 rpmLimit 同属 PostgreSQL int4，管理面 create/PATCH/批量/导入必须在写面
// 按 0..2147483647（并发 1..2147483647）返回 400，不能落到数据库变成 500。
func TestConcurrencyAndPriorityInt4Bounds(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	scope := AccessScope{ViewerID: adminID, IsAdmin: true}

	// 创建：int4 上界落库。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		rpmCreatePayload("int4-上界", `"concurrencyLimit":2147483647,"priority":2147483647,`))
	if code != http.StatusCreated {
		t.Fatalf("int4 上界创建应成功：%d %v", code, payload)
	}
	maxID := dataMap(t, payload)["id"].(string)
	if got := env.queryCell(t, `SELECT concurrency_limit || '|' || priority FROM accounts WHERE id = ?`, maxID); got != "2147483647|2147483647" {
		t.Fatalf("int4 上界应落库，got %s", got)
	}

	// 创建：超 int4 拒绝。
	for _, testCase := range []struct {
		name      string
		fragment  string
		wantField string
	}{
		{"并发超上界", `"concurrencyLimit":2147483648,`, "并发限制"},
		{"优先级超上界", `"priority":2147483648,`, "优先级"},
	} {
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
			rpmCreatePayload("int4-"+testCase.name, testCase.fragment))
		if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), testCase.wantField) {
			t.Fatalf("%s应 400：%d %v", testCase.name, code, payload)
		}
	}

	// PATCH：超 int4 拒绝，上界可写。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("int4-patch"))
	if code != http.StatusCreated {
		t.Fatalf("PATCH 夹具创建应成功：%d %v", code, payload)
	}
	patchID := dataMap(t, payload)["id"].(string)
	for _, testCase := range []struct {
		body      string
		wantField string
	}{
		{`{"expectedConfigRevision":1,"concurrencyLimit":2147483648}`, "并发限制"},
		{`{"expectedConfigRevision":1,"priority":2147483648}`, "优先级"},
	} {
		code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+patchID, testCase.body)
		if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), testCase.wantField) {
			t.Fatalf("PATCH 超 int4 应 400：%d %v", code, payload)
		}
	}
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+patchID,
		`{"expectedConfigRevision":1,"concurrencyLimit":2147483647,"priority":2147483647}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH int4 上界应成功：%d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT concurrency_limit || '|' || priority FROM accounts WHERE id = ?`, patchID); got != "2147483647|2147483647" {
		t.Fatalf("PATCH int4 上界应落库，got %s", got)
	}

	// 批量：两字段均按 int4 上界校验。
	if message := validateBatchUpdateValue("concurrencyLimit", float64(2147483648)); message == "" {
		t.Fatal("批量 concurrencyLimit 超 int4 应拒绝")
	}
	if message := validateBatchUpdateValue("priority", float64(2147483648)); message == "" {
		t.Fatal("批量 priority 超 int4 应拒绝")
	}
	for _, valid := range []struct {
		field string
		value any
	}{
		{"concurrencyLimit", float64(2147483647)},
		{"priority", float64(2147483647)},
	} {
		if message := validateBatchUpdateValue(valid.field, valid.value); message != "" {
			t.Fatalf("批量 %s int4 上界应合法：%q", valid.field, message)
		}
	}

	// 导入：两个字段超 int4 均在预览期按 per-account 失败，不进入落库。
	overInt4, err := env.store.ExecuteImport(context.Background(), w2NativeDoc(
		[]map[string]any{
			withAPIKeyAccountOverrides(w2APIKeyAccount("int4-imp-concurrency", "int4-g-concurrency"), map[string]any{"concurrencyLimit": float64(2147483648)}),
			withAPIKeyAccountOverrides(w2APIKeyAccount("int4-imp-priority", "int4-g-priority"), map[string]any{"priority": float64(2147483648)}),
		},
		nil,
	), "", ImportOptions{}, scope)
	if err != nil {
		t.Fatal(err)
	}
	if overInt4.Imported || overInt4.CanImport {
		t.Fatalf("超 int4 导入应停在预览不执行：imported=%v canImport=%v", overInt4.Imported, overInt4.CanImport)
	}
	for _, item := range overInt4.Accounts {
		if item.Action != importActionFailed {
			t.Fatalf("超 int4 条目应 failed：%+v", item)
		}
		if joined := strings.Join(item.Messages, ";"); !strings.Contains(joined, "2147483647") {
			t.Fatalf("失败消息应说明 int4 上界：%v", item.Messages)
		}
	}
}
