package accounts

// T91-3（R7-B B1-10 裁决「修」）：schedulable 改为账户状态派生。
//   - Create / PATCH 请求体不再接受 schedulable：任何显式提交（true / false /
//     null）都按项目 unknown-field 口径回 400，文案固定；
//   - 用户状态写入派生：active → schedulable=1，disabled / pending_test /
//     temporary_unavailable / error / quality_isolated → 0；
//   - 只改名称等非状态字段时不触碰 schedulable；
//   - 系统写面（错误策略、冷却复测、迁移、锁死、到期）保持各自既有语义。
// 存量 active+schedulable=0 的归一在 maintenance 升级链第 13 步
// （cmd/juhe-ai-maintenance 的 account-schedulable-normalization）。

import (
	"net/http"
	"testing"
)

func TestT91SchedulableRejectedOnCreateAndPatch(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// Create：三种取值形态（true / false / null）一律 400 + 固定文案。
	// 名字逐个不同：mutation guard 以 owner/provider/name/credential 做创建
	// 指纹，同键立即重放会被去重成 409。
	bodies := []struct{ name, body string }{
		{"t91-create-0", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"t91-create-0","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"],"schedulable":true,` + b1GPTFragment + `}`},
		{"t91-create-1", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"t91-create-1","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"],"schedulable":false,` + b1GPTFragment + `}`},
		{"t91-create-2", `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"t91-create-2","type":"api_key","credentials":{"api_key":"sk-1","base_url":"https://api.openai.com/v1"},"supportedModels":["gpt-4o-mini"],"schedulable":null,` + b1GPTFragment + `}`},
	}
	for _, item := range bodies {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", item.body)
		if code != http.StatusBadRequest || payload["message"] != schedulableReadOnlyMessage {
			t.Fatalf("create 携带 schedulable 应 400（%s）：%d %v", item.name, code, payload)
		}
		if rows := env.count(t, `SELECT COUNT(*) FROM accounts WHERE name = ?`, item.name); rows != 0 {
			t.Fatalf("被拒创建不得落库（%s）：%d", item.name, rows)
		}
	}

	// PATCH：同上；编辑守卫以账户 id 做指纹，每种形态用独立账户。
	patches := []struct{ name, body string }{
		{"t91-patch-0", `{"expectedConfigRevision":1,"schedulable":false}`},
		{"t91-patch-1", `{"expectedConfigRevision":1,"status":"active","schedulable":false}`},
		{"t91-patch-2", `{"expectedConfigRevision":1,"schedulable":null}`},
	}
	for _, item := range patches {
		code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload(item.name))
		if code != http.StatusCreated {
			t.Fatalf("create（%s）：%d %v", item.name, code, created)
		}
		id := dataMap(t, created)["id"].(string)
		code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, item.body)
		if code != http.StatusBadRequest || payload["message"] != schedulableReadOnlyMessage {
			t.Fatalf("patch 携带 schedulable 应 400（%s）：%d %v", item.body, code, payload)
		}
		if got := env.queryCell(t, `SELECT status || '|' || schedulable || '|' || config_revision FROM accounts WHERE id = ?`, id); got != "active|1|1" {
			t.Fatalf("被拒编辑不得改写账户（%s）：%s", item.name, got)
		}
	}
}

func TestT91SchedulableDerivedFromStatusWrites(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("t91-derived"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	id := dataMap(t, created)["id"].(string)
	if got := env.queryCell(t, `SELECT status || '|' || schedulable FROM accounts WHERE id = ?`, id); got != "active|1" {
		t.Fatalf("创建 active 应派生可调度：%s", got)
	}

	// 只改名称：不触碰 schedulable（也不出现在 changedFields）。
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"name":"t91-derived-renamed"}`)
	if code != http.StatusOK {
		t.Fatalf("rename: %d %v", code, patched)
	}
	if changedFieldSet(t, patched)["schedulable"] {
		t.Fatalf("只改名称不应改写 schedulable：%v", dataMap(t, patched)["changedFields"])
	}
	if got := env.queryCell(t, `SELECT name || '|' || schedulable FROM accounts WHERE id = ?`, id); got != "t91-derived-renamed|1" {
		t.Fatalf("改名后行：%s", got)
	}

	// disabled：派生 0。
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":2,"status":"disabled"}`)
	if code != http.StatusOK {
		t.Fatalf("disable: %d %v", code, patched)
	}
	if !changedFieldSet(t, patched)["schedulable"] {
		t.Fatalf("disabled 必须披露派生 schedulable：%v", dataMap(t, patched)["changedFields"])
	}
	if got := env.queryCell(t, `SELECT status || '|' || schedulable FROM accounts WHERE id = ?`, id); got != "disabled|0" {
		t.Fatalf("disabled 应派生不可调度：%s", got)
	}

	// active：派生回 1（显式启用语义不变）。
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":3,"status":"active"}`)
	if code != http.StatusOK {
		t.Fatalf("enable: %d %v", code, patched)
	}
	if got := env.queryCell(t, `SELECT status || '|' || schedulable FROM accounts WHERE id = ?`, id); got != "active|1" {
		t.Fatalf("active 应派生可调度：%s", got)
	}

	// temporary_unavailable（人工隔离）：派生 0。
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":4,"status":"temporary_unavailable"}`)
	if code != http.StatusOK {
		t.Fatalf("isolate: %d %v", code, patched)
	}
	if got := env.queryCell(t, `SELECT status || '|' || schedulable FROM accounts WHERE id = ?`, id); got != "temporary_unavailable|0" {
		t.Fatalf("temporary_unavailable 应派生不可调度：%s", got)
	}

	// pending_test：派生 0。
	env.exec(t, `UPDATE accounts SET schedulable = 1 WHERE id = ?`, id)
	code, patched = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":5,"status":"pending_test"}`)
	if code != http.StatusOK {
		t.Fatalf("pending: %d %v", code, patched)
	}
	if got := env.queryCell(t, `SELECT status || '|' || schedulable FROM accounts WHERE id = ?`, id); got != "pending_test|0" {
		t.Fatalf("pending_test 应派生不可调度：%s", got)
	}

	// 系统写面不被派生规则改写：错误策略把账户写成 error+0 与 cooling+1 的既有
	// 语义由错误策略单测钉住（patch 只覆盖用户状态写入），这里只确认用户面
	// 不再提供入口——任何 body 携带该键都被拒绝。
	code, rejected := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":6,"status":"active","schedulable":true}`)
	if code != http.StatusBadRequest || rejected["message"] != schedulableReadOnlyMessage {
		t.Fatalf("用户面不得再提交 schedulable：%d %v", code, rejected)
	}
	if schedulableReadOnlyMessage != "schedulable 由账户状态派生，不支持手动设置" {
		t.Fatalf("裁决文案不匹配：%q", schedulableReadOnlyMessage)
	}
}
