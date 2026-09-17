// X05 管理面 smoke 补充场景（计划 §5.3 登记的 smoke fixture 缺口）：三个
// 子测试共用同一 fresh 网关实例（subtests 顺序执行）：external-integration-
// sources 创建→详情回读→PATCH→删除→404/列表摘除；source token 创建与
// secret 揭示；账户手动测试 test-options 载荷结构。复用 X05 公共 harness，
// 不触碰既有场景。
package acceptance

import (
	"net/http"
	"strings"
	"testing"
)

func TestAdminSmokeSurfaces(t *testing.T) {
	fixture := startGateway(t, gatewayEnvOptions{})
	client := &acceptanceClient{t: t, http: fixture.admin, baseURL: fixture.baseURL}
	runTag := randomHex(t, 4)
	externalSourcesPath := "/__aisys__/api/external-integration-sources"

	t.Run("external_source_crud", func(t *testing.T) {
		// 创建：zod 严格 payload（name 必填 ≤80、status 枚举、scopes 白名单），
		// 201 信封同时带 item（含 primaryToken 摘要）与明文 primary token。
		_, created := client.do(http.MethodPost, externalSourcesPath, map[string]any{
			"name":   "验收外部来源" + runTag,
			"status": "active",
			"scopes": []string{"juhe_ai_public:api_key_list:read"},
			"notes":  "X05 external source smoke",
		}, wantStatus(http.StatusCreated))
		item := nestedMap(data(created), "item")
		sourceID := str(item["id"])
		updatedAt := str(item["updatedAt"])
		primaryToken := nestedMap(item, "primaryToken")
		if sourceID == "" || updatedAt == "" || primaryToken == nil || nestedMap(data(created), "token") == nil {
			t.Fatalf("external source create payload wrong: %#v", created)
		}

		// 详情回读：创建字段逐项生效，创建即生成 primary token（tokenCount ≥ 1）。
		_, detail := client.do(http.MethodGet, externalSourcesPath+"/"+sourceID, nil, wantStatus(http.StatusOK))
		detailData := data(detail)
		if str(detailData["name"]) != "验收外部来源"+runTag || str(detailData["status"]) != "active" {
			t.Fatalf("external source detail payload wrong: %#v", detail)
		}
		if tokenCount, _ := detailData["tokenCount"].(float64); tokenCount < 1 {
			t.Fatalf("external source primary token missing: %#v", detail)
		}

		// PATCH：expectedUpdatedAt 乐观锁 + notes 修改；回 MutationResult
		//（id + 新 updatedAt）。
		_, patched := client.do(http.MethodPatch, externalSourcesPath+"/"+sourceID, map[string]any{
			"expectedUpdatedAt": updatedAt,
			"notes":             "X05 external source smoke 更新",
		}, wantStatus(http.StatusOK))
		patchID := str(data(patched)["id"])
		patchUpdatedAt := str(data(patched)["updatedAt"])
		if patchID != sourceID || patchUpdatedAt == "" {
			t.Fatalf("external source patch payload wrong: %#v", patched)
		}

		// 详情验证修改生效（notes 与新版本落库）。
		_, detailAfter := client.do(http.MethodGet, externalSourcesPath+"/"+sourceID, nil, wantStatus(http.StatusOK))
		if str(data(detailAfter)["notes"]) != "X05 external source smoke 更新" || str(data(detailAfter)["updatedAt"]) != patchUpdatedAt {
			t.Fatalf("external source patch not persisted: %#v", detailAfter)
		}

		// 删除：204 空体；随后详情 404、列表摘除。
		client.do(http.MethodDelete, externalSourcesPath+"/"+sourceID,
			map[string]any{"expectedUpdatedAt": patchUpdatedAt}, wantStatus(http.StatusNoContent))
		client.do(http.MethodGet, externalSourcesPath+"/"+sourceID, nil, wantStatus(http.StatusNotFound))
		_, listed := client.do(http.MethodGet, externalSourcesPath, nil, wantStatus(http.StatusOK))
		for _, raw := range anySlice(data(listed), "items") {
			entry, _ := raw.(map[string]any)
			if entry != nil && str(entry["id"]) == sourceID {
				t.Fatalf("deleted external source still listed: %#v", listed)
			}
		}
	})

	t.Run("external_token_secret_reveal", func(t *testing.T) {
		// 独立来源（上一子测试已删除其来源）：创建 source → 追加 token。
		_, created := client.do(http.MethodPost, externalSourcesPath, map[string]any{
			"name": "验收令牌来源" + runTag,
		}, wantStatus(http.StatusCreated))
		sourceID := str(nestedMap(data(created), "item")["id"])
		if sourceID == "" {
			t.Fatalf("token source create payload wrong: %#v", created)
		}

		_, tokenCreated := client.do(http.MethodPost, externalSourcesPath+"/"+sourceID+"/tokens", map[string]any{
			"name":   "验收Token" + runTag,
			"scopes": []string{"juhe_ai_public:api_key_list:read"},
		}, wantStatus(http.StatusCreated))
		token := nestedMap(data(tokenCreated), "token")
		tokenID := str(token["id"])
		fullToken := str(token["token"])
		tokenPrefix := str(token["tokenPrefix"])
		tokenSuffix := str(token["tokenSuffix"])
		if tokenID == "" || fullToken == "" || tokenPrefix == "" || tokenSuffix == "" {
			t.Fatalf("external token create payload wrong: %#v", tokenCreated)
		}
		// prefix/suffix 是完整明文的切片预览（externalTokenSlice 0..8 / -8..）。
		if !strings.HasPrefix(fullToken, tokenPrefix) || !strings.HasSuffix(fullToken, tokenSuffix) {
			t.Fatalf("external token preview mismatch: %#v", token)
		}

		// 详情 tokens 列表包含新 token（primary + 新增 = 2）。
		_, detail := client.do(http.MethodGet, externalSourcesPath+"/"+sourceID, nil, wantStatus(http.StatusOK))
		if count, _ := data(detail)["tokenCount"].(float64); count != 2 {
			t.Fatalf("external source token count wrong: %#v", detail)
		}

		// secret 揭示：第一次返回完整明文，与创建响应一致。
		_, reveal := client.do(http.MethodGet, externalSourcesPath+"/"+sourceID+"/tokens/"+tokenID+"/secret",
			nil, wantStatus(http.StatusOK))
		if str(data(reveal)["token"]) != fullToken {
			t.Fatalf("external token reveal payload wrong: %#v", reveal)
		}
		// 第二次揭示：Go 实现（policyreads/external.go FindTokenSecret）从
		// 持久密文解密，读取不消费密文，因此可重复且同值；文件头 "one-shot"
		// 注释与实现不符，按实现断言（200 + 同值）。
		_, revealAgain := client.do(http.MethodGet, externalSourcesPath+"/"+sourceID+"/tokens/"+tokenID+"/secret",
			nil, wantStatus(http.StatusOK))
		if str(data(revealAgain)["token"]) != fullToken {
			t.Fatalf("external token second reveal payload wrong: %#v", revealAgain)
		}
	})

	t.Run("account_test_options", func(t *testing.T) {
		// 复用管理面建账主干（gpt / api_key / seed 默认分组）；健康检查模型
		// 缺省落 supportedModels[0]（accounts/write.go 默认链）。
		_, created := client.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
			"providerCode":              "gpt",
			"providerProtocolProfileId": "profile_gpt_openai_v1",
			"name":                      "验收测试选项账户" + runTag,
			"type":                      "api_key",
			"credentials":               map[string]any{"api_key": "sk-acceptance-" + runTag, "base_url": "https://api.openai.com/v1"},
			"supportedModels":           []string{"gpt-5.6-sol"},
			"skipInitialHealthCheck":    true,
			"groupId":                   "grp_default_gpt_sys_admin",
		}, wantStatus(http.StatusCreated))
		accountID := dataString(created, "id")
		if accountID == "" {
			t.Fatalf("account create payload wrong: %#v", created)
		}

		// test-options：keyword 钉住受支持模型（seed 目录含 gpt-5.6-sol）；
		// 载荷是 ManualTestOption 数组 {id, name, testEndpointModes}。
		_, options := client.do(http.MethodGet,
			"/__aisys__/api/accounts/"+accountID+"/test-options?keyword=gpt-5.6-sol",
			nil, wantStatus(http.StatusOK))
		rows, ok := options["data"].([]any)
		if !ok {
			t.Fatalf("test-options payload must be an array: %#v", options)
		}
		var target map[string]any
		for _, raw := range rows {
			entry, _ := raw.(map[string]any)
			if entry != nil && str(entry["id"]) == "gpt-5.6-sol" {
				target = entry
				break
			}
		}
		if target == nil {
			t.Fatalf("test-options missing gpt-5.6-sol entry: %#v", options)
		}
		if str(target["name"]) != "gpt-5.6-sol" {
			t.Fatalf("test-options entry payload wrong: %#v", target)
		}
		modes, ok := target["testEndpointModes"].([]any)
		if !ok || len(modes) == 0 {
			t.Fatalf("test-options endpoint modes wrong: %#v", target)
		}
		for _, mode := range modes {
			if strings.TrimSpace(str(mode)) == "" {
				t.Fatalf("test-options empty endpoint mode: %#v", target)
			}
		}
	})
}
