package accounts

// W2 导出与列表杂项测试：export body 解析与导出链（export.go）、列表排序
// 与可见性 helper（list.go）、test_store 的任务/会话状态辅助函数。

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestW2ExportByIDAndFilters(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	seedImportProxy(t, env, "pp-export", adminID, "导出代理")
	ids := []string{}
	for _, name := range []string{"导出甲", "导出乙"} {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(name))
		if code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, payload)
		}
		ids = append(ids, dataMap(t, payload)["id"].(string))
	}
	env.exec(t, `UPDATE accounts SET proxy_profile_id = 'pp-export' WHERE id = ?`, ids[0])

	t.Run("按 ID 导出", func(t *testing.T) {
		body := `{"accountIds":["` + strings.Join(ids, `","`) + `"]}`
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export", body)
		if code != http.StatusOK {
			t.Fatalf("导出状态码：%d %v", code, payload)
		}
		data := dataMap(t, payload)
		document, ok := data["document"].(map[string]any)
		if !ok {
			t.Fatalf("导出信封缺少 document：%v", data)
		}
		if document["type"] != accountImportProtocolType {
			t.Fatalf("导出文档类型不一致：%v", document["type"])
		}
		accounts, ok := document["accounts"].([]any)
		if !ok || len(accounts) != 2 {
			t.Fatalf("导出账户数量不一致：%v", document["accounts"])
		}
		summary := data["summary"].(map[string]any)
		if summary["accounts"] != float64(2) || summary["proxies"] != float64(1) {
			t.Fatalf("导出汇总不一致：%v", summary)
		}
		first := accounts[0].(map[string]any)
		credentials := first["credentials"].(map[string]any)
		if credentials["api_key"] != "sk-live-secret-1234567890" {
			t.Fatalf("导出应包含明文凭据（导出面契约）：%v", credentials)
		}
		// 代理引用以 proxies 列表 + proxyRef 回显。
		proxies, ok := document["proxies"].([]any)
		if !ok || len(proxies) != 1 {
			t.Fatalf("导出代理数量不一致：%v", document["proxies"])
		}
		if first["proxyRef"] != "proxy-pp-export" {
			t.Fatalf("代理引用回显不一致：%v", first["proxyRef"])
		}
		// 审计。
		found := false
		for _, action := range env.sink.actions() {
			if action == "accounts.export" {
				found = true
			}
		}
		if !found {
			t.Fatalf("审计动作缺失：%v", env.sink.actions())
		}
	})
	t.Run("按筛选导出与空结果", func(t *testing.T) {
		body := `{"filters":{"keyword":"导出","providerCode":"gpt","type":"api_key","status":"active","schedulable":true}}`
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export", body)
		if code != http.StatusOK {
			t.Fatalf("筛选导出：%d %v", code, payload)
		}
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/export",
			`{"filters":{"keyword":"无匹配关键词"}}`)
		if code != http.StatusBadRequest || !strings.Contains(payload["message"].(string), "没有匹配") {
			t.Fatalf("空结果应 400：%d %v", code, payload)
		}
	})
	t.Run("body 校验", func(t *testing.T) {
		for name, body := range map[string]string{
			"未知键":     `{"bogus":1}`,
			"双键互斥":    `{"accountIds":["a"],"filters":{}}`,
			"ID 非字符串": `{"accountIds":[3]}`,
			"ID 空白":   `{"accountIds":["  "]}`,
			"ID 为空数组": `{"accountIds":[]}`,
			"filters 缺失": `{"filters":"x"}`,
			"filters 未知键": `{"filters":{"mystery":1}}`,
		} {
			code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export", body)
			if code != http.StatusBadRequest {
				t.Fatalf("%s：期望 400，实际 %d %v", name, code, payload)
			}
		}
	})
	t.Run("用户导出无代理创建权限差异", func(t *testing.T) {
		env.login(t, "export-user", "export-pass", "user")
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts/export",
			`{"accountIds":["` + strings.Join(ids, `","`) + `"]}`)
		// 他人账户在用户范围不可见 → 导出列表为空或 400。
		if code == http.StatusOK {
			if accounts, ok := dataMap(t, payload)["accounts"].([]any); ok && len(accounts) > 0 {
				t.Fatalf("他人账户不应出现在用户导出：%v", payload)
			}
		}
	})
}

func TestW2ExportHelpers(t *testing.T) {
	t.Run("textValueOrJoin", func(t *testing.T) {
		if got := textValueOrJoin("  a  "); got != "  a  " {
			t.Fatalf("字符串应原样返回：%q", got)
		}
		if got := textValueOrJoin([]any{"  ", "b"}); got != "b" {
			t.Fatalf("数组项应 trim 且丢弃空白：%q", got)
		}
		if got := textValueOrJoin([]any{"a", "b"}); got != "a,b" {
			t.Fatalf("数组拼接不一致：%q", got)
		}
		if got := textValueOrJoin(nil); got != "" {
			t.Fatalf("nil 应为空：%q", got)
		}
		if got := textValueOrJoin(3); got != "" {
			t.Fatalf("其他类型应为空：%q", got)
		}
	})
	t.Run("textListOrSingle", func(t *testing.T) {
		if got := textListOrSingle("a"); len(got) != 1 || got[0] != "a" {
			t.Fatalf("单值包装不一致：%v", got)
		}
		if got := textListOrSingle([]any{"a", " b "}); len(got) != 2 || got[1] != "b" {
			t.Fatalf("列表拆分不一致：%v", got)
		}
		if got := textListOrSingle(nil); got != nil {
			t.Fatalf("nil 应为 nil：%v", got)
		}
	})
	t.Run("strPtrOrNil", func(t *testing.T) {
		if strPtrOrNil("") != nil {
			t.Fatal("空串应为 nil")
		}
		if got := strPtrOrNil("x"); got == nil || *got != "x" {
			t.Fatalf("非空应包装：%v", got)
		}
	})
	t.Run("exportAccountStatus", func(t *testing.T) {
		// active 需同时 schedulable；pending_test 透传；其余 disabled。
		if got := exportAccountStatus(&exportAccountRow{status: "active", schedulable: true}); got != "active" {
			t.Fatalf("active 判定不一致：%s", got)
		}
		if got := exportAccountStatus(&exportAccountRow{status: "active", schedulable: false}); got != "disabled" {
			t.Fatalf("不可调度导出为 disabled：%s", got)
		}
		if got := exportAccountStatus(&exportAccountRow{status: "pending_test"}); got != "pending_test" {
			t.Fatalf("pending_test 透传不一致：%s", got)
		}
		if got := exportAccountStatus(&exportAccountRow{status: "cooldown"}); got != "disabled" {
			t.Fatalf("cooldown 导出为 disabled：%s", got)
		}
	})
	t.Run("credentials orderedKeys", func(t *testing.T) {
		credentials := Credentials{"b": 1, "a": 2, "c": 3}
		got := credentials.orderedKeys()
		// 键按字典序稳定输出（导出 JSON 的确定性契约）。
		if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
			t.Fatalf("键序不一致：%v", got)
		}
	})
}

func TestW2TestStoreHelpers(t *testing.T) {
	t.Run("任务与会话状态", func(t *testing.T) {
		if got := testTaskStatus("weird"); got != "failed" {
			t.Fatalf("未知任务状态回退不一致：%q", got)
		}
		if got := testTaskStatus("queued"); got != "queued" {
			t.Fatalf("queued 透传不一致：%q", got)
		}
		if got := testSessionStatus("weird"); got != "expired" {
			t.Fatalf("未知会话状态回退不一致：%q", got)
		}
		if got := testSessionStatus("running"); got != "running" {
			t.Fatalf("running 透传不一致：%q", got)
		}
		deadline := testQueuedDeadlineAt("2026-01-01T00:00:00Z")
		if deadline == "" || deadline == "2026-01-01T00:00:00Z" {
			t.Fatalf("排队截止时间应推进：%q", deadline)
		}
		if got := testQueuedDeadlineAt("bad-time"); got != "bad-time" {
			t.Fatalf("非法时间原样返回：%q", got)
		}
	})
	t.Run("读写权限", func(t *testing.T) {
		// 读取权以请求者本人为界：ViewerID 必须等于请求者，且过滤 ID 匹配。
		owner := &AccessScope{ViewerID: "owner1"}
		ownerFiltered := &AccessScope{ViewerID: "owner1", IsAdmin: true, FilterID: "owner1"}
		other := &AccessScope{ViewerID: "other"}
		if !canReadTestTask("owner1", sql.NullString{}, owner) {
			t.Fatal("请求者本人应可读")
		}
		if !canReadTestTask("owner1", sqlNullString("owner1"), ownerFiltered) {
			t.Fatal("过滤命中的范围管理者应可读")
		}
		if canReadTestTask("owner1", sql.NullString{}, other) {
			t.Fatal("无关用户不可读")
		}
		if canReadTestTask("owner1", sqlNullString("other"), ownerFiltered) {
			t.Fatal("过滤不一致不可读")
		}
		if canReadTestSession("owner1", sql.NullString{}, other) {
			t.Fatal("无关用户不可读会话")
		}
		if !canReadTestSession("owner1", sql.NullString{}, owner) {
			t.Fatal("请求者本人可读会话")
		}
		if !canReadTestSession("owner1", sqlNullString("owner1"), ownerFiltered) {
			t.Fatal("过滤命中应可读")
		}
		if canReadTestSession("owner1", sqlNullString("nope"), ownerFiltered) {
			t.Fatal("过滤不一致不可读")
		}
	})
	t.Run("布尔字面量与可选时间戳", func(t *testing.T) {
		env := newTestEnv(t)
		if env.store.boolTrueLiteral() != "1" || env.store.boolFalseLiteral() != "0" {
			t.Fatal("SQLite 布尔字面量不一致")
		}
		if testEndpointModeOrNull(sql.NullString{}) != nil {
			t.Fatal("空端点形态应为 nil")
		}
		if testOptionalTimestamp(sql.NullString{}) != nil {
			t.Fatal("空时间戳应为 nil")
		}
		if got := testOptionalTimestamp(sqlNullString("2026-01-01T00:00:00Z")); got == nil || *got == "" {
			t.Fatal("有效时间戳应包装")
		}
		if got := testTrimmedOrNull("  x  "); got != "x" {
			t.Fatalf("trim 包装不一致：%v", got)
		}
		if got := testTrimmedOrNull("  "); got != nil {
			t.Fatalf("空白应为 nil：%v", got)
		}
	})
	t.Run("testRequestRole", func(t *testing.T) {
		if testRequestRole(AccessScope{ViewerID: "a", IsAdmin: true}) != "admin" {
			t.Fatal("admin 角色标注不一致")
		}
		if testRequestRole(AccessScope{ViewerID: "a"}) != "user" {
			t.Fatal("user 角色标注不一致")
		}
	})
	_ = context.Background()
	_ = time.Now
}
