package accounts

// W2 路由 CRUD 面测试：list/options/tags/detail/clone-context/create/patch/
// lock/delete 的参数校验矩阵与成功链路，以及 runtime-reset 的 owner 路径
// （routes.go + runtime_reset_routes.go 的 handler 层）。

import (
	"net/http"
	"strings"
	"testing"
)

func TestW2RoutesListOptionsTagsDetail(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("列表账户"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	t.Run("列表与查询参数", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
		if code != http.StatusOK || len(listItems(t, payload)) != 1 {
			t.Fatalf("列表不一致：%d %v", code, payload)
		}
		query := "?sorts=priority:asc&ids=" + id + "&keyword=列表&providerCode=gpt&type=api_key&status=active&schedulable=true&page=1&pageSize=10"
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts"+query, "")
		if code != http.StatusOK || len(listItems(t, payload)) != 1 {
			t.Fatalf("过滤列表不一致：%d %v", code, payload)
		}
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts?keyword=不存在", "")
		if code != http.StatusOK || len(listItems(t, payload)) != 0 {
			t.Fatalf("keyword 过滤不一致：%d %v", code, payload)
		}
	})
	t.Run("options 与 tags", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/options", "")
		if code != http.StatusOK || len(dataArray(t, payload)) != 1 {
			t.Fatalf("options 不一致：%d %v", code, payload)
		}
		// 附加标签后读取标签列表并删除。
		code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id+"/tags",
			`{"expectedConfigRevision":1,"tags":["临时标签"]}`)
		if code != http.StatusOK {
			t.Fatalf("tags patch: %d %v", code, payload)
		}
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/tags", "")
		if code != http.StatusOK {
			t.Fatalf("tags 列表: %d %v", code, payload)
		}
		tagID := ""
		for _, item := range dataArray(t, payload) {
			tag := item.(map[string]any)
			if tag["name"] == "临时标签" {
				tagID = tag["id"].(string)
			}
		}
		if tagID == "" {
			t.Fatalf("标签缺失：%v", payload)
		}
		// 标签仍在账户上：先清空绑定再删除标签。
		code, _ = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id+"/tags",
			`{"expectedConfigRevision":2,"tags":[]}`)
		if code != http.StatusOK {
			t.Fatalf("清空标签: %d %v", code, payload)
		}
		code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/tags/"+tagID, "")
		if code != http.StatusNoContent {
			t.Fatalf("标签删除: %d", code)
		}
		code, payload = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/tags/"+tagID, "")
		if code != http.StatusNotFound || payload["message"] == "" {
			t.Fatalf("重复删除应 404：%d %v", code, payload)
		}
	})
	t.Run("详情与 clone-context", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+id, "")
		if code != http.StatusOK || dataMap(t, payload)["id"] != id {
			t.Fatalf("详情不一致：%d %v", code, payload)
		}
		code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+id+"/edit-basic", "")
		if code != http.StatusOK {
			t.Fatalf("edit-basic: %d", code)
		}
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+id+"/clone-context", "")
		if code != http.StatusOK {
			t.Fatalf("clone-context: %d %v", code, payload)
		}
		code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-missing", "")
		if code != http.StatusNotFound || payload["message"] != "账户不存在" {
			t.Fatalf("详情 404 不一致：%d %v", code, payload)
		}
		code, _ = env.do(t, http.MethodGet, "/__aisys__/api/accounts/acc-missing/clone-context", "")
		if code != http.StatusNotFound {
			t.Fatalf("clone-context 404: %d", code)
		}
	})
	t.Run("范围查询拒绝", func(t *testing.T) {
		code, payload := env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+id+"?systemAccountId=", "")
		if code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
			t.Fatalf("scoped 校验不一致：%d %v", code, payload)
		}
	})
}

func TestW2RoutesCreateValidation(t *testing.T) {
	env := newTestEnv(t)
	adminID := envMustAdmin(t, env)
	env.seedProviderAndDefaultGroup(t, adminID)

	cases := map[string]string{
		"缺 providerCode":  `{"providerProtocolProfileId":"prof-gpt","name":"w2c1","type":"api_key"}`,
		"缺 profile":       `{"providerCode":"gpt","name":"w2c2","type":"api_key"}`,
		"缺 name":          `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","type":"api_key"}`,
		"缺 type":          `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"w2c3"}`,
		"未知键":             `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"w2c4","type":"api_key","bogus":1}`,
		"空支持模型":           `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"w2c5","type":"api_key","supportedModels":[]}`,
		"healthCheckModel 非字符串": `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"w2c6","type":"api_key","healthCheckModel":3}`,
		"并发非法":          `{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"w2c7","type":"api_key","concurrencyLimit":"x"}`,
	}
	for name, body := range cases {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", body)
		if code != http.StatusBadRequest || payload["message"] != "账户参数无效" {
			t.Fatalf("%s：期望 400 账户参数无效，实际 %d %v", name, code, payload)
		}
	}
	// my-accounts 面创建（用户身份）走同一 body 解析。
	env.login(t, "self-user", "self-pass", "user")
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/my-accounts", `{}`)
	if code != http.StatusBadRequest {
		t.Fatalf("my-accounts 创建校验：%d %v", code, payload)
	}
}

// envMustAdmin 确保 admin 账户存在并返回 id（不通过 HTTP 登录）。
func envMustAdmin(t *testing.T, env *testEnv) string {
	t.Helper()
	return env.login(t, "root", "root-pass", "super_admin")
}

func TestW2RoutesPatchValidation(t *testing.T) {
	env := newTestEnv(t)
	adminID := envMustAdmin(t, env)
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("补丁账户"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	cases := map[string]string{
		"未知键":        `{"expectedConfigRevision":1,"bogus":1}`,
		"缺修订号":       `{"name":"新名"}`,
		"修订号非法":      `{"expectedConfigRevision":0}`,
		"name 非字符串":  `{"expectedConfigRevision":1,"name":3}`,
		"status 非字符串": `{"expectedConfigRevision":1,"status":9}`,
		"并发非法":       `{"expectedConfigRevision":1,"concurrencyLimit":"x"}`,
	}
	for name, body := range cases {
		code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id, body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s：期望 400，实际 %d %v", name, code, payload)
		}
	}
	// 标签面校验。
	tagCases := map[string]string{
		"未知键":   `{"expectedConfigRevision":1,"tags":[],"bogus":1}`,
		"缺修订号":  `{"tags":[]}`,
		"tags 缺失": `{"expectedConfigRevision":1}`,
		"超量标签":    `{"expectedConfigRevision":1,"tags":["` + strings.Repeat("t", 1) + `","2","3","4","5","6","7","8","9","10","11","12","13","14","15","16","17","18","19","20","21","22","23","24","25"]}`,
	}
	for name, body := range tagCases {
		code, payload := env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id+"/tags", body)
		if code != http.StatusBadRequest {
			t.Fatalf("tags %s：期望 400，实际 %d %v", name, code, payload)
		}
	}
	// 成功 patch：名称与备注更新 + 修订号递增。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+id,
		`{"expectedConfigRevision":1,"name":"新名称","notes":"新备注"}`)
	if code != http.StatusOK {
		t.Fatalf("patch 成功链路：%d %v", code, payload)
	}
	data := dataMap(t, payload)
	if data["configRevision"] != float64(2) {
		t.Fatalf("修订号未递增：%v", data)
	}
	changed := data["changedFields"].([]any)
	if len(changed) != 2 {
		t.Fatalf("变更字段不一致：%v", changed)
	}
}

func TestW2RoutesLockFamily(t *testing.T) {
	env := newTestEnv(t)
	adminID := envMustAdmin(t, env)
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("锁死账户"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	t.Run("参数校验", func(t *testing.T) {
		// lockBody 纯函数矩阵：guard 失败去重会让连续 400 变 409，
		// 因此 body 校验直接驱动解析函数。
		cases := []struct {
			name    string
			body    map[string]any
			message string
		}{
			{"未知键", map[string]any{"expectedConfigRevision": float64(1), "bogus": 1}, "锁死参数无效"},
			{"缺修订号", map[string]any{"lockDeathTimeoutSeconds": float64(60)}, "锁死参数无效"},
			{"修订号非法", map[string]any{"expectedConfigRevision": "1"}, "锁死参数无效"},
			{"超时非整数", map[string]any{"expectedConfigRevision": float64(1), "lockDeathTimeoutSeconds": "x"}, "锁死死亡窗口必须是 30..3600 的整数"},
			{"间隔非整数", map[string]any{"expectedConfigRevision": float64(1), "lockRetryIntervalSeconds": "x"}, "锁死重试间隔必须是 5..30 的整数"},
		}
		for _, testCase := range cases {
			_, message := lockBody(testCase.body)
			if message != testCase.message {
				t.Fatalf("%s：期望 %q，实际 %q", testCase.name, testCase.message, message)
			}
		}
		// lock-config 空配置。
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/lock-config",
			`{"expectedConfigRevision":1}`)
		if code != http.StatusBadRequest || payload["message"] != "请至少提交一项锁死配置" {
			t.Fatalf("lock-config 空配置：%d %v", code, payload)
		}
	})
	t.Run("锁死与解锁", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/lock",
			`{"expectedConfigRevision":1,"lockDeathTimeoutSeconds":120,"lockRetryIntervalSeconds":10}`)
		if code != http.StatusOK {
			t.Fatalf("lock: %d %v", code, payload)
		}
		if dataMap(t, payload)["lockState"] != "LOCKED_IDLE" {
			t.Fatalf("锁定状态不一致：%v", payload)
		}
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/unlock",
			`{"expectedConfigRevision":2}`)
		if code != http.StatusOK {
			t.Fatalf("unlock: %d %v", code, payload)
		}
		if dataMap(t, payload)["lockState"] != "UNLOCKED" {
			t.Fatalf("解锁状态不一致：%v", payload)
		}
		// lock-config 更新保留配置。
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/lock-config",
			`{"expectedConfigRevision":3,"lockRetryIntervalSeconds":15}`)
		if code != http.StatusOK {
			t.Fatalf("lock-config: %d %v", code, payload)
		}
	})
	t.Run("账户不存在", func(t *testing.T) {
		code, _ := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-missing/lock",
			`{"expectedConfigRevision":1}`)
		if code != http.StatusNotFound {
			t.Fatalf("lock 404: %d", code)
		}
	})
}

func TestW2RoutesDelete(t *testing.T) {
	env := newTestEnv(t)
	adminID := envMustAdmin(t, env)
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("待删除"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/"+id, "")
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	// 软删后列表不可见、详情 404、重复删除 404。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts", "")
	if code != http.StatusOK || len(listItems(t, payload)) != 0 {
		t.Fatalf("删除后列表不一致：%d %v", code, payload)
	}
	code, _ = env.do(t, http.MethodDelete, "/__aisys__/api/accounts/"+id, "")
	if code != http.StatusNotFound {
		t.Fatalf("重复删除应 404：%d", code)
	}
}

func TestW2RuntimeResetOwnerPath(t *testing.T) {
	env := newTestEnv(t)
	adminID := envMustAdmin(t, env)
	env.seedProviderAndDefaultGroup(t, adminID)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createPayload("重置账户"))
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, payload)
	}
	id := dataMap(t, payload)["id"].(string)

	// 预置冷却/错误痕迹，验证重置清除。
	env.exec(t, `UPDATE accounts SET status = 'cooldown', cooldown_until = '2030-01-01T00:00:00Z',
		last_error_code = 'upstream_5xx', last_error_message = 'boom', schedulable = 0 WHERE id = ?`, id)

	t.Run("参数校验", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset", `{"bogus":1}`)
		if code != http.StatusBadRequest || payload["message"] != "清理运行状态参数无效" {
			t.Fatalf("未知键：%d %v", code, payload)
		}
		code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset", `{"expectedConfigRevision":"1"}`)
		if code != http.StatusBadRequest {
			t.Fatalf("修订号类型：%d %v", code, payload)
		}
	})
	t.Run("重置成功", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset",
			`{"expectedConfigRevision":1}`)
		if code != http.StatusOK {
			t.Fatalf("runtime-reset: %d %v", code, payload)
		}
		data := dataMap(t, payload)
		// 冷却中的账户重置后回到 active 且恢复可调度。
		if data["status"] != "active" || data["schedulable"] != true {
			t.Fatalf("重置后状态不一致：%v", data)
		}
		row := env.queryCell(t, `SELECT COALESCE(cooldown_until,'') || '|' || COALESCE(last_error_code,'') || '|' || schedulable FROM accounts WHERE id = ?`, id)
		if row != "||1" {
			t.Fatalf("冷却痕迹未清除：%s", row)
		}
		// 审计记录存在。
		found := false
		for _, action := range env.sink.actions() {
			if action == "accounts.runtime_reset" {
				found = true
			}
		}
		if !found {
			t.Fatalf("审计动作缺失：%v", env.sink.actions())
		}
	})
	t.Run("修订冲突", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/"+id+"/runtime-reset",
			`{"expectedConfigRevision":1}`)
		if code != http.StatusConflict {
			t.Fatalf("旧修订号应 409：%d %v", code, payload)
		}
	})
	t.Run("账户不存在", func(t *testing.T) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/acc-missing/runtime-reset",
			`{"expectedConfigRevision":1}`)
		if code != http.StatusNotFound || payload["message"] != "账户不存在" {
			t.Fatalf("404 契约：%d %v", code, payload)
		}
	})
}

