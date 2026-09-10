package policyreads

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 外部来源：列表查询校验矩阵与分页窗口
// ---------------------------------------------------------------------------

func TestWeExternalListQueryValidationMatrix(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	cases := []struct {
		name    string
		query   string
		message string
	}{
		// 英文 zod 消息在 HTTP 边界被本地化为状态默认文案。
		{"page 非数字", "?page=abc", localizedBadRequest},
		{"page 小数", "?page=1.5", localizedBadRequest},
		{"page 下界", "?page=0", localizedBadRequest},
		{"pageSize 下界", "?pageSize=0", localizedBadRequest},
		{"pageSize 上界", "?pageSize=101", localizedBadRequest},
		{"pageSize 非数字", "?pageSize=abc", localizedBadRequest},
		{"keyword 重复", "?keyword=a&keyword=b", localizedBadRequest},
		{"status 非法枚举", "?status=bogus", localizedBadRequest},
		{"status 重复", "?status=all&status=active", localizedBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, payload, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources"+tc.query, "")
			if code != http.StatusBadRequest || payload["message"] != tc.message {
				t.Fatalf("%s: %d %v (want %d %s)", tc.name, code, payload, http.StatusBadRequest, tc.message)
			}
		})
	}

	// 合法查询直通。
	code, ok, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources?page=1&pageSize=50&status=all&keyword=x", "")
	if code != 200 || dataMap(t, ok)["page"] != float64(1) || dataMap(t, ok)["pageSize"] != float64(50) {
		t.Fatalf("合法查询: %d %v", code, ok)
	}
}

func TestWeExternalListPaginationWindow(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 造 3 个来源（updated_at 递增保证顺序可预期）。
	for index := 1; index <= 3; index++ {
		code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources",
			fmt.Sprintf(`{"name":"来源%d"}`, index))
		if code != http.StatusCreated {
			t.Fatalf("seed %d: %d %v", index, code, created)
		}
	}

	code, page1, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources?pageSize=1&page=1", "")
	result := dataMap(t, page1)
	items := result["items"].([]any)
	if code != 200 || len(items) != 1 || result["hasMore"] != true || result["page"] != float64(1) {
		t.Fatalf("page1 = %v", result)
	}
	firstName := items[0].(map[string]any)["name"].(string)

	code, page2, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources?pageSize=1&page=2", "")
	page2Items := dataMap(t, page2)["items"].([]any)
	if code != 200 || len(page2Items) != 1 || page2Items[0].(map[string]any)["name"] == firstName {
		t.Fatalf("page2 应翻到下一来源: %v", page2)
	}
	if upper := dataMap(t, page2)["pageUpperBound"].(float64); upper < 2 {
		t.Fatalf("pageUpperBound = %v", dataMap(t, page2))
	}
	code, page3, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources?pageSize=1&page=3", "")
	if code != 200 || dataMap(t, page3)["hasMore"] != false {
		t.Fatalf("page3 应是最后一页: %v", page3)
	}
	// 超出分页窗口被夹到上界（pageSize=1 → 上界 999）。
	code, clamped, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources?pageSize=1&page=99999", "")
	if code != 200 || dataMap(t, clamped)["page"] != float64(999) {
		t.Fatalf("page 应被夹到上界 999: %v", dataMap(t, clamped))
	}
	// keyword 前缀 LIKE 匹配：命中"来源"三个。
	code, keyword, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources?pageSize=100&keyword=来源", "")
	if code != 200 || len(dataMap(t, keyword)["items"].([]any)) != 3 {
		t.Fatalf("keyword = %v", keyword)
	}
	// LIKE 通配符必须被转义：字面 % 不应当通配符。
	code, literal, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources?keyword=%25", "")
	if code != 200 || len(dataMap(t, literal)["items"].([]any)) != 0 {
		t.Fatalf("通配符转义 = %v", literal)
	}
}

// ---------------------------------------------------------------------------
// 外部来源：创建/更新/Token 的 zod 校验矩阵
// ---------------------------------------------------------------------------

func TestWeExternalCreateBodyValidationMatrix(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	cases := []struct {
		name    string
		body    string
		message string
	}{
		{"name 缺失", `{}`, localizedBadRequest},
		{"name 非字符串", `{"name":1}`, localizedBadRequest},
		{"name 空白", `{"name":"  "}`, "来源系统名称不能为空"},
		{"name 超长", `{"name":"` + strings.Repeat("字", 81) + `"}`, "来源系统名称不能超过 80 个字符"},
		{"status 非字符串", `{"name":"a","status":1}`, localizedBadRequest},
		{"status 非法枚举", `{"name":"a","status":"paused"}`, localizedBadRequest},
		{"scopes 非数组", `{"name":"a","scopes":"x"}`, localizedBadRequest},
		{"scopes 项非字符串", `{"name":"a","scopes":[1]}`, localizedBadRequest},
		{"scopes 空白项", `{"name":"a","scopes":[" "]}`, localizedBadRequest},
		{"rateLimits 非数组", `{"name":"a","rateLimits":5}`, localizedBadRequest},
		{"rateLimits 超量", `{"name":"a","rateLimits":[` + strings.TrimSuffix(strings.Repeat(`{"windowSeconds":60,"maxRequests":1},`, 9), ",") + `]}`, "限频规则最多 8 条"},
		{"rateLimit 项非对象", `{"name":"a","rateLimits":[3]}`, localizedBadRequest},
		{"rateLimit 未知字段", `{"name":"a","rateLimits":[{"windowSeconds":60,"maxRequests":1,"extra":1}]}`, localizedBadRequest},
		{"windowSeconds 缺失", `{"name":"a","rateLimits":[{"maxRequests":1}]}`, localizedBadRequest},
		{"windowSeconds 小数", `{"name":"a","rateLimits":[{"windowSeconds":1.5,"maxRequests":1}]}`, localizedBadRequest},
		{"windowSeconds 下界", `{"name":"a","rateLimits":[{"windowSeconds":0,"maxRequests":1}]}`, "限频窗口不能小于 1 秒"},
		{"windowSeconds 上界", `{"name":"a","rateLimits":[{"windowSeconds":86401,"maxRequests":1}]}`, "限频窗口不能超过 86400 秒"},
		{"maxRequests 非数字", `{"name":"a","rateLimits":[{"windowSeconds":60,"maxRequests":"x"}]}`, localizedBadRequest},
		{"maxRequests 小数", `{"name":"a","rateLimits":[{"windowSeconds":60,"maxRequests":1.5}]}`, localizedBadRequest},
		{"maxRequests 下界", `{"name":"a","rateLimits":[{"windowSeconds":60,"maxRequests":0}]}`, "限频次数不能小于 1"},
		{"maxRequests 上界", `{"name":"a","rateLimits":[{"windowSeconds":60,"maxRequests":100001}]}`, "限频次数不能超过 100000"},
		{"expiresAt 非字符串", `{"name":"a","expiresAt":5}`, "过期时间无效"},
		{"expiresAt 非法格式", `{"name":"a","expiresAt":"2026/01/01"}`, "过期时间无效"},
		{"notes 非字符串", `{"name":"a","notes":true}`, localizedBadRequest},
		{"notes 超长", `{"name":"a","notes":"` + strings.Repeat("字", 501) + `"}`, "备注不能超过 500 个字符"},
		{"未知字段", `{"name":"a","bogus":1}`, localizedBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, payload, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources", tc.body)
			if code != http.StatusBadRequest || payload["message"] != tc.message {
				t.Fatalf("%s: %d %v (want %d %s)", tc.name, code, payload, http.StatusBadRequest, tc.message)
			}
		})
	}
}

func TestWeExternalSourceUpdateBodyValidationMatrix(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources", `{"name":"矩阵来源"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	sourceID := dataMap(t, created)["item"].(map[string]any)["id"].(string)
	code, detail, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/"+sourceID, "")
	updatedAt := dataMap(t, detail)["updatedAt"].(string)

	cases := []struct {
		name    string
		body    string
		status  int
		message string
	}{
		{"expectedUpdatedAt 缺失", `{"name":"x"}`, http.StatusBadRequest, localizedBadRequest},
		{"expectedUpdatedAt 非字符串", `{"expectedUpdatedAt":1}`, http.StatusBadRequest, localizedBadRequest},
		{"expectedUpdatedAt 非法格式", `{"expectedUpdatedAt":"2026-01-01","name":"x"}`, http.StatusBadRequest, "外部来源配置版本格式不正确"},
		{"无修改字段", `{"expectedUpdatedAt":"2020-01-01T00:00:00.000Z"}`, http.StatusBadRequest, "请提供要修改的来源配置字段"},
		{"未知字段", `{"expectedUpdatedAt":"` + updatedAt + `","name":"x","bogus":1}`, http.StatusBadRequest, localizedBadRequest},
		{"notes 显式置空", `{"expectedUpdatedAt":"` + updatedAt + `","notes":null}`, http.StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, payload, _ := env.do(t, http.MethodPatch, "/__aisys__/api/external-integration-sources/"+sourceID, tc.body)
			if code != tc.status || (tc.message != "" && payload["message"] != tc.message) {
				t.Fatalf("%s: %d %v (want %d %s)", tc.name, code, payload, tc.status, tc.message)
			}
		})
	}

	// 一次性提交全部字段：验证投影分支与操作日志的各字段标签。
	code, detail2, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/"+sourceID, "")
	nextUpdatedAt := dataMap(t, detail2)["updatedAt"].(string)
	code, allFields, _ := env.do(t, http.MethodPatch, "/__aisys__/api/external-integration-sources/"+sourceID,
		`{"expectedUpdatedAt":"`+nextUpdatedAt+`","name":"矩阵来源2","status":"disabled","scopes":["juhe_ai_public:group_list:read"],`+
			`"rateLimits":[{"windowSeconds":120,"maxRequests":5}],"expiresAt":"2031-01-01T00:00:00.000Z","notes":"新备注"}`)
	if code != 200 {
		t.Fatalf("全字段 patch: %d %v", code, allFields)
	}
	code, list, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources?keyword=矩阵来源2", "")
	if code != 200 {
		t.Fatalf("patch 后查询: %d %v", code, list)
	}
	entry := dataMap(t, list)["items"].([]any)[0].(map[string]any)
	if entry["status"] != "disabled" {
		t.Fatalf("status = %v", entry)
	}
	rateLimits := entry["rateLimits"].([]any)
	if len(rateLimits) != 1 || rateLimits[0].(map[string]any)["windowSeconds"] != float64(120) {
		t.Fatalf("rateLimits = %v", rateLimits)
	}
	// 重新启用（tokens 同步分支已在既有用例覆盖，这里补 notes/expires 置空投影）。
	code, after, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/"+sourceID, "")
	afterUpdated := dataMap(t, after)["updatedAt"].(string)
	code, cleared, _ := env.do(t, http.MethodPatch, "/__aisys__/api/external-integration-sources/"+sourceID,
		`{"expectedUpdatedAt":"`+afterUpdated+`","expiresAt":null}`)
	if code != 200 {
		t.Fatalf("expiresAt 置空: %d %v", code, cleared)
	}
}

func TestWeExternalDeleteBodyValidationMatrix(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources", `{"name":"待删来源"}`)
	sourceID := dataMap(t, created)["item"].(map[string]any)["id"].(string)

	cases := []struct {
		name    string
		body    string
		message string
	}{
		{"expectedUpdatedAt 缺失", `{}`, localizedBadRequest},
		{"非字符串", `{"expectedUpdatedAt":1}`, localizedBadRequest},
		{"非法格式", `{"expectedUpdatedAt":"x"}`, "外部来源配置版本格式不正确"},
		{"未知字段", `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z","extra":1}`, localizedBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, payload, _ := env.do(t, http.MethodDelete, "/__aisys__/api/external-integration-sources/"+sourceID, tc.body)
			if code != http.StatusBadRequest || payload["message"] != tc.message {
				t.Fatalf("%s: %d %v (want %d %s)", tc.name, code, payload, http.StatusBadRequest, tc.message)
			}
		})
	}
	// 非冲突的过期版本删除 → 409（版本不匹配）；时间戳独立于失败用例，避免
	// 触发 mutation dedupe 的"刚刚失败"拦截。
	code, conflict, _ := env.do(t, http.MethodDelete, "/__aisys__/api/external-integration-sources/"+sourceID,
		`{"expectedUpdatedAt":"2027-06-01T12:00:00.000Z"}`)
	if code != http.StatusConflict || conflict["message"] != externalConflictMessage {
		t.Fatalf("过期版本删除: %d %v", code, conflict)
	}
	// 正确版本删除成功。
	code, detail, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/"+sourceID, "")
	updatedAt := dataMap(t, detail)["updatedAt"].(string)
	code, deleted, _ := env.do(t, http.MethodDelete, "/__aisys__/api/external-integration-sources/"+sourceID,
		`{"expectedUpdatedAt":"`+updatedAt+`"}`)
	if code != http.StatusNoContent {
		t.Fatalf("删除: %d %v", code, deleted)
	}
}

func TestWeExternalTokenBodyValidationMatrix(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources", `{"name":"Token矩阵来源"}`)
	sourceID := dataMap(t, created)["item"].(map[string]any)["id"].(string)

	cases := []struct {
		name    string
		body    string
		message string
	}{
		{"name 缺失", `{}`, localizedBadRequest},
		{"name 非字符串", `{"name":[]}`, localizedBadRequest},
		{"name 空白", `{"name":" "}`, "Token 名称不能为空"},
		{"name 超长", `{"name":"` + strings.Repeat("字", 81) + `"}`, "Token 名称不能超过 80 个字符"},
		{"status 非法", `{"name":"T","status":"gone"}`, localizedBadRequest},
		{"scopes 非数组", `{"name":"T","scopes":"x"}`, localizedBadRequest},
		{"expiresAt 非法", `{"name":"T","expiresAt":"soon"}`, "过期时间无效"},
		{"未知字段", `{"name":"T","bogus":1}`, localizedBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, payload, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources/"+sourceID+"/tokens", tc.body)
			if code != http.StatusBadRequest || payload["message"] != tc.message {
				t.Fatalf("%s: %d %v (want %d %s)", tc.name, code, payload, http.StatusBadRequest, tc.message)
			}
		})
	}
	// 未知来源 → 400 来源系统不存在。
	code, missing, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources/extsrc_nope/tokens", `{"name":"T"}`)
	if code != http.StatusBadRequest || missing["message"] != "来源系统不存在" {
		t.Fatalf("未知来源: %d %v", code, missing)
	}
}

func TestWeExternalTokenUpdateBodyValidationMatrix(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources", `{"name":"Token补丁来源"}`)
	sourceID := dataMap(t, created)["item"].(map[string]any)["id"].(string)
	code, tokenCreated, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources/"+sourceID+"/tokens", `{"name":"T1"}`)
	if code != http.StatusCreated {
		t.Fatalf("token create: %d %v", code, tokenCreated)
	}
	tokenID := dataMap(t, tokenCreated)["token"].(map[string]any)["id"].(string)
	code, detail, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/"+sourceID, "")
	tokenUpdatedAt := dataMap(t, detail)["tokens"].([]any)[0].(map[string]any)["updatedAt"].(string)

	cases := []struct {
		name    string
		body    string
		status  int
		message string
	}{
		{"expectedUpdatedAt 缺失", `{"name":"x"}`, http.StatusBadRequest, localizedBadRequest},
		{"expectedUpdatedAt 非法格式", `{"expectedUpdatedAt":"x","name":"y"}`, http.StatusBadRequest, "外部来源配置版本格式不正确"},
		{"无修改字段", `{"expectedUpdatedAt":"` + tokenUpdatedAt + `"}`, http.StatusBadRequest, "请提供要修改的 Token 字段"},
		{"name 空白", `{"expectedUpdatedAt":"` + tokenUpdatedAt + `","name":" "}`, http.StatusBadRequest, "Token 名称不能为空"},
		{"scopes 项非字符串", `{"expectedUpdatedAt":"` + tokenUpdatedAt + `","scopes":[3]}`, http.StatusBadRequest, localizedBadRequest},
		{"expiresAt 非法", `{"expectedUpdatedAt":"` + tokenUpdatedAt + `","expiresAt":"tomorrow"}`, http.StatusBadRequest, "过期时间无效"},
		{"未知字段", `{"expectedUpdatedAt":"` + tokenUpdatedAt + `","name":"x","bogus":1}`, http.StatusBadRequest, localizedBadRequest},
		{"token 不存在", `{"expectedUpdatedAt":"` + tokenUpdatedAt + `","name":"x"}`, http.StatusNotFound, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "/__aisys__/api/external-integration-sources/" + sourceID + "/tokens/" + tokenID
			if tc.name == "token 不存在" {
				path = "/__aisys__/api/external-integration-sources/" + sourceID + "/tokens/exttok_nope"
			}
			code, payload, _ := env.do(t, http.MethodPatch, path, tc.body)
			if code != tc.status || (tc.message != "" && payload["message"] != tc.message) {
				t.Fatalf("%s: %d %v (want %d %s)", tc.name, code, payload, tc.status, tc.message)
			}
		})
	}

	// 全字段 token 补丁：name/status/scopes/expiresAt 变更一次提交，覆盖操作日志标签。
	code, allFields, _ := env.do(t, http.MethodPatch,
		"/__aisys__/api/external-integration-sources/"+sourceID+"/tokens/"+tokenID,
		`{"expectedUpdatedAt":"`+tokenUpdatedAt+`","name":"T1改名","status":"revoked","scopes":["juhe_ai_public:group_list:read"],"expiresAt":null}`)
	if code != 200 {
		t.Fatalf("全字段 token patch: %d %v", code, allFields)
	}
	// 内置 token 编辑约束已有用例；这里补 revoked → active 的恢复路径。
	code, restored, _ := env.do(t, http.MethodPatch,
		"/__aisys__/api/external-integration-sources/"+sourceID+"/tokens/"+tokenID,
		`{"expectedUpdatedAt":"`+dataMap(t, allFields)["updatedAt"].(string)+`","status":"active"}`)
	if code != 200 {
		t.Fatalf("恢复 active: %d %v", code, restored)
	}
}

// ---------------------------------------------------------------------------
// 外部 Token 密文读取：明文缺失与密文损坏
// ---------------------------------------------------------------------------

func TestWeExternalFindTokenSecretGuards(t *testing.T) {
	env := newPolicyTestEnv(t)
	store := env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	_, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/external-integration-sources", `{"name":"密文来源"}`)
	sourceID := dataMap(t, created)["item"].(map[string]any)["id"].(string)

	// 精确验证：seed 一个无密文 token 再读取。
	env.exec(t, `INSERT INTO external_integration_source_tokens (id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, last_used_at, created_at, updated_at, revoked_at)
		VALUES ('exttok_nosecret', ?, '无密文', 'hash', NULL, 'prefix123', 'suffix456', 'active', '[]', NULL, NULL, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', NULL)`, sourceID)
	if _, err := store.FindTokenSecret(nil, sourceID, "exttok_nosecret"); err == nil || !strings.Contains(err.Error(), "缺少完整 Token") {
		t.Fatalf("缺密文应报错: %v", err)
	}
	// 密文损坏：解密失败同样报缺少完整 Token。
	env.exec(t, `UPDATE external_integration_source_tokens SET token_secret_encrypted = 'garbage' WHERE id = 'exttok_nosecret'`)
	if _, err := store.FindTokenSecret(nil, sourceID, "exttok_nosecret"); err == nil || !strings.Contains(err.Error(), "缺少完整 Token") {
		t.Fatalf("损坏密文应报错: %v", err)
	}
	// 正常路径已有用例覆盖；这里确认未知 token 返回 nil, nil。
	secret, err := store.FindTokenSecret(nil, sourceID, "exttok_missing")
	if err != nil || secret != nil {
		t.Fatalf("未知 token = %v err = %v", secret, err)
	}
}

// ---------------------------------------------------------------------------
// 响应检查策略：列表行扫描、补丁校验矩阵与删除未命中
// ---------------------------------------------------------------------------

func TestWeInspectionListWithRowsScansOverview(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountInspection(t)
	seedInspectionProviders(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	for index := 0; index < 3; index++ {
		code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies",
			fmt.Sprintf(`{"name":"策略%d","scopeType":"protocol","protocolCode":"openai","priority":%d,"match":{"errorCodes":["x"]},"action":"observe"}`, index, index+1))
		if code != http.StatusCreated {
			t.Fatalf("create %d: %d %v", index, code, created)
		}
	}
	code, list, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies", "")
	if code != 200 {
		t.Fatalf("list: %d %v", code, list)
	}
	policies := dataMap(t, list)["policies"].([]any)
	if len(policies) != 3 {
		t.Fatalf("policies = %d, want 3", len(policies))
	}
	// 优先级升序；行扫描投影 enabled/editable/updatedAt。
	first := policies[0].(map[string]any)
	if first["editable"] != true || first["defaultRule"] != false || first["enabled"] != true || first["updatedAt"] == "" {
		t.Fatalf("first = %v", first)
	}
	// 详情走补丁行投影（LEFT JOIN provider name 为 NULL 分支）。
	policyID := first["id"].(string)
	code, rowDetail, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/"+policyID, "")
	if code != 200 || dataMap(t, rowDetail)["name"] != first["name"] {
		t.Fatalf("detail: %d %v", code, rowDetail)
	}
}

func TestWeInspectionPatchBodyValidationMatrix(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountInspection(t)
	seedInspectionProviders(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies",
		`{"name":"补丁矩阵","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe","notes":"原始备注"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	policyID := dataMap(t, created)["id"].(string)
	code, detail, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/"+policyID, "")
	updatedAt := dataMap(t, detail)["updatedAt"].(string)

	cases := []struct {
		name    string
		body    string
		message string
	}{
		{"expectedUpdatedAt 缺失", `{"name":"x"}`, localizedBadRequest},
		{"expectedUpdatedAt 非字符串", `{"expectedUpdatedAt":9}`, localizedBadRequest},
		{"name 非字符串", `{"expectedUpdatedAt":"` + updatedAt + `","name":1}`, localizedBadRequest},
		{"name 空白", `{"expectedUpdatedAt":"` + updatedAt + `","name":" "}`, "规则名称不能为空"},
		{"name 超长", `{"expectedUpdatedAt":"` + updatedAt + `","name":"` + strings.Repeat("字", 101) + `"}`, "规则名称不能超过 100 个字符"},
		{"enabled 非布尔", `{"expectedUpdatedAt":"` + updatedAt + `","enabled":"yes"}`, localizedBadRequest},
		{"priority 小数", `{"expectedUpdatedAt":"` + updatedAt + `","priority":1.5}`, localizedBadRequest},
		{"priority 下界", `{"expectedUpdatedAt":"` + updatedAt + `","priority":0}`, localizedBadRequest},
		{"priority 上界", `{"expectedUpdatedAt":"` + updatedAt + `","priority":10000}`, localizedBadRequest},
		{"scopeType 非法", `{"expectedUpdatedAt":"` + updatedAt + `","scopeType":"world"}`, localizedBadRequest},
		{"protocolCode 非法", `{"expectedUpdatedAt":"` + updatedAt + `","protocolCode":"ollama"}`, localizedBadRequest},
		{"providerCode 空白", `{"expectedUpdatedAt":"` + updatedAt + `","scopeType":"provider","providerCode":" "}`, "请选择供应商"},
		{"match 非对象", `{"expectedUpdatedAt":"` + updatedAt + `","match":[]}`, localizedBadRequest},
		{"match 未知键", `{"expectedUpdatedAt":"` + updatedAt + `","match":{"bogus":["x"]}}`, localizedBadRequest},
		{"match 无条件", `{"expectedUpdatedAt":"` + updatedAt + `","match":{}}`, "响应检查策略至少需要一个匹配条件"},
		{"match 项非字符串", `{"expectedUpdatedAt":"` + updatedAt + `","match":{"errorCodes":[3]}}`, localizedBadRequest},
		{"action 非法", `{"expectedUpdatedAt":"` + updatedAt + `","action":"explode"}`, localizedBadRequest},
		{"notes 空白", `{"expectedUpdatedAt":"` + updatedAt + `","notes":" "}`, "备注不能为空"},
		{"notes 非字符串", `{"expectedUpdatedAt":"` + updatedAt + `","notes":7}`, localizedBadRequest},
		{"未知字段", `{"expectedUpdatedAt":"` + updatedAt + `","bogus":1}`, localizedBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, payload, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID, tc.body)
			if code != http.StatusBadRequest || payload["message"] != tc.message {
				t.Fatalf("%s: %d %v (want %d %s)", tc.name, code, payload, http.StatusBadRequest, tc.message)
			}
		})
	}

	// 合法补丁：启用状态翻转 + match 变更 + notes 置空，覆盖操作日志字段投影。
	code, patched, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","enabled":false,"match":{"errorCodes":["x","y"]},"notes":null,"action":"retry_next_account"}`)
	if code != 200 {
		t.Fatalf("合法补丁: %d %v", code, patched)
	}
}

func TestWeInspectionDeleteUnknownReturns404(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountInspection(t)
	seedInspectionProviders(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	// 删除不存在的策略：guardedDelete 的未命中分支。
	code, missing, _ := env.do(t, http.MethodDelete, "/__aisys__/api/response-inspection-policies/rip_missing", "")
	if code != http.StatusNotFound || missing["message"] != "响应检查策略不存在" {
		t.Fatalf("delete unknown: %d %v", code, missing)
	}
}

func TestWeInspectionProviderOptionsKeywordTooLong(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountInspection(t)
	seedInspectionProviders(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	long := strings.Repeat("字", 81)
	code, payload, _ := env.do(t, http.MethodGet,
		"/__aisys__/api/response-inspection-policies/provider-options?protocolCode=openai&scopeType=provider&keyword="+long, "")
	if code != http.StatusBadRequest || payload["message"] != localizedBadRequest {
		t.Fatalf("超长 keyword: %d %v", code, payload)
	}
	code, unknownKey, _ := env.do(t, http.MethodGet,
		"/__aisys__/api/response-inspection-policies/provider-options?protocolCode=openai&scopeType=provider&extra=1", "")
	if code != http.StatusBadRequest || unknownKey["message"] != localizedBadRequest {
		t.Fatalf("未知查询键: %d %v", code, unknownKey)
	}
}

func TestWeInspectionPatchNoChangeFields(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountInspection(t)
	seedInspectionProviders(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies",
		`{"name":"无变化","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	policyID := dataMap(t, created)["id"].(string)
	// 只提交版本号：refine 拒绝（消息与创建路径的措辞不同）。
	code, payload, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`)
	if code != http.StatusBadRequest || payload["message"] != "至少需要提交一个变化字段" {
		t.Fatalf("无变化字段: %d %v", code, payload)
	}
	// 版本格式错误使用领域专属消息（区别于外部来源的版本错误文案）。
	code, badVersion, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"nope","name":"x"}`)
	if code != http.StatusBadRequest || badVersion["message"] != "响应检查策略版本无效" {
		t.Fatalf("版本无效: %d %v", code, badVersion)
	}
	// providerCode 显式置空在 protocol 层策略下应被拒绝（协议层不能绑定供应商）。
	code, detail, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/"+policyID, "")
	updatedAt := dataMap(t, detail)["updatedAt"].(string)
	code, providerNull, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","providerCode":"gpt"}`)
	if code != http.StatusBadRequest || providerNull["message"] != "协议层响应检查策略不能绑定供应商" {
		t.Fatalf("协议层绑定供应商: %d %v", code, providerNull)
	}
}

func TestWeExternalCorruptedRowSurfaces500(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")

	// 存量行 scopes_json 损坏：读取面按内部错误处理（500），映射 decode 失败分支。
	env.exec(t, `INSERT INTO external_integration_sources (id, name, status, scopes_json, rate_limits_json, expires_at, notes, last_used_at, created_at, updated_at)
		VALUES ('extsrc_broken', '损坏来源', 'active', '{bad json', NULL, NULL, NULL, NULL, '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`)

	code, list, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources", "")
	if code != http.StatusInternalServerError || list["message"] != "服务器内部错误" {
		t.Fatalf("损坏列表: %d %v", code, list)
	}
	code, detail, _ := env.do(t, http.MethodGet, "/__aisys__/api/external-integration-sources/extsrc_broken", "")
	if code != http.StatusInternalServerError || detail["message"] != "服务器内部错误" {
		t.Fatalf("损坏详情: %d %v", code, detail)
	}
}

func TestWeInspectionCreateBodyExtraBranches(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountInspection(t)
	seedInspectionProviders(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	base := `"scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"`
	cases := []struct {
		name string
		body string
	}{
		{"enabled 非布尔", `{"name":"x","enabled":"yes",` + base + `}`},
		{"priority 非数字", `{"name":"x","priority":"high",` + base + `}`},
		{"priority 小数", `{"name":"x","priority":1.5,` + base + `}`},
		{"priority 下界", `{"name":"x","priority":0,` + base + `}`},
		{"priority 上界", `{"name":"x","priority":10000,` + base + `}`},
		{"match 非对象", `{"name":"x","scopeType":"protocol","protocolCode":"openai","match":"x","action":"observe"}`},
	}
	// 英文 zod 消息在 HTTP 边界被本地化为状态默认文案。
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, payload, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies", tc.body)
			if code != http.StatusBadRequest || payload["message"] != localizedBadRequest {
				t.Fatalf("%s: %d %v", tc.name, code, payload)
			}
		})
	}
	// 中文领域消息原样透传。
	chinese := []struct {
		name    string
		body    string
		message string
	}{
		{"notes 超长", `{"name":"x","notes":"` + strings.Repeat("字", 1001) + `",` + base + `}`, "备注不能超过 1000 个字符"},
		{"protocolCode 非字符串", `{"name":"x","scopeType":"protocol","protocolCode":1,"match":{"errorCodes":["x"]},"action":"observe"}`, "响应检查策略协议无效"},
	}
	for _, tc := range chinese {
		t.Run(tc.name, func(t *testing.T) {
			code, payload, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies", tc.body)
			if code != http.StatusBadRequest || payload["message"] != tc.message {
				t.Fatalf("%s: %d %v (want %s)", tc.name, code, payload, tc.message)
			}
		})
	}
}

func TestWeInspectionPatchProtocolChangeAndProviderRename(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.mountInspection(t)
	seedInspectionProviders(t, env)
	env.login(t, "root", "root-pass", "super_admin")

	// 合法 protocolCode 变更（openai → anthropic）。
	code, created, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies",
		`{"name":"协议切换","scopeType":"protocol","protocolCode":"openai","match":{"errorCodes":["x"]},"action":"observe"}`)
	policyID := dataMap(t, created)["id"].(string)
	code, detail, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/"+policyID, "")
	updatedAt := dataMap(t, detail)["updatedAt"].(string)
	code, switched, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+policyID,
		`{"expectedUpdatedAt":"`+updatedAt+`","protocolCode":"anthropic"}`)
	if code != 200 || dataMap(t, switched)["protocolCode"] != "anthropic" {
		t.Fatalf("协议切换: %d %v", code, switched)
	}

	// 供应商层策略：providerCode 变更到同协议的另一启用供应商，触发 providerName 重查。
	code, providerCreated, _ := env.do(t, http.MethodPost, "/__aisys__/api/response-inspection-policies",
		`{"name":"供应商策略","scopeType":"provider","protocolCode":"openai","providerCode":"gpt","match":{"errorCodes":["x"]},"action":"observe"}`)
	if code != http.StatusCreated {
		t.Fatalf("provider create: %d %v", code, providerCreated)
	}
	providerID := dataMap(t, providerCreated)["id"].(string)
	code, providerDetail, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/"+providerID, "")
	if dataMap(t, providerDetail)["providerName"] != "GPT 官方" {
		t.Fatalf("providerName: %v", providerDetail)
	}
	providerUpdated := dataMap(t, providerDetail)["updatedAt"].(string)
	code, renamed, _ := env.do(t, http.MethodPatch, "/__aisys__/api/response-inspection-policies/"+providerID,
		`{"expectedUpdatedAt":"`+providerUpdated+`","providerCode":"openai-res"}`)
	if code != 200 || dataMap(t, renamed)["providerCode"] != "openai-res" {
		t.Fatalf("供应商切换: %d %v", code, renamed)
	}
	code, afterRename, _ := env.do(t, http.MethodGet, "/__aisys__/api/response-inspection-policies/"+providerID, "")
	if dataMap(t, afterRename)["providerName"] != "OpenAI Res" {
		t.Fatalf("切换后 providerName: %v", afterRename)
	}
}

func TestWeExternalHandlerEmptyPathValueGuards(t *testing.T) {
	env := newPolicyTestEnv(t)
	store := env.mountExternal(t)
	env.login(t, "root", "root-pass", "super_admin")
	deps := &ExternalDeps{Store: store, Auth: env.deps, Sink: env.sink}

	call := func(handler http.Handler, method, target, body string, pathValues map[string]string) int {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request := httptest.NewRequest(method, target, reader)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		for key, value := range pathValues {
			request.SetPathValue(key, value)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}

	// 空路径参数的守卫分支（路由层通常不会产生空值，这里白盒直调 handler）。
	if code := call(http.HandlerFunc(deps.detail), http.MethodGet, "/x", "", map[string]string{"id": ""}); code != http.StatusBadRequest {
		t.Fatalf("detail 空 id = %d", code)
	}
	if code := call(http.HandlerFunc(deps.tokenSecret), http.MethodGet, "/x", "", map[string]string{"id": ""}); code != http.StatusBadRequest {
		t.Fatalf("tokenSecret 空 id = %d", code)
	}
	if code := call(http.HandlerFunc(deps.tokenSecret), http.MethodGet, "/x", "", map[string]string{"id": "extsrc_1", "tokenId": ""}); code != http.StatusBadRequest {
		t.Fatalf("tokenSecret 空 tokenId = %d", code)
	}
	if code := call(deps.guardedPatchSource(), http.MethodPatch, "/x", `{"name":"x"}`, map[string]string{"id": ""}); code != http.StatusBadRequest {
		t.Fatalf("patch source 空 id = %d", code)
	}
	if code := call(deps.guardedDeleteSource(), http.MethodDelete, "/x", `{"expectedUpdatedAt":"2026-01-01T00:00:00.000Z"}`, map[string]string{"id": ""}); code != http.StatusBadRequest {
		t.Fatalf("delete source 空 id = %d", code)
	}
	if code := call(deps.guardedCreateToken(), http.MethodPost, "/x", `{"name":"T"}`, map[string]string{"id": ""}); code != http.StatusBadRequest {
		t.Fatalf("create token 空 id = %d", code)
	}
	if code := call(deps.guardedPatchToken(), http.MethodPatch, "/x", `{"name":"x"}`, map[string]string{"id": ""}); code != http.StatusBadRequest {
		t.Fatalf("patch token 空 id = %d", code)
	}
	if code := call(deps.guardedPatchToken(), http.MethodPatch, "/x", `{"name":"x"}`, map[string]string{"id": "extsrc_1", "tokenId": ""}); code != http.StatusBadRequest {
		t.Fatalf("patch token 空 tokenId = %d", code)
	}
}
