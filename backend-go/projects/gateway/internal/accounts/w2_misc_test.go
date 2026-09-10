package accounts

// W2 杂项纯函数批次：导入执行器错误识别（import.go）、runtime-reset 的
// 时间与绑定判定（runtime_reset.go）、草稿输入解析矩阵
// （test_dispatch_routes.go）、流量迁移 body 解析（m11_traffic_migration.go）
// 与 patch 面小工具（patch.go）。

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestW2ImportDuplicateErrorsAndPlanFlag(t *testing.T) {
	t.Run("duplicateProxyNameError", func(t *testing.T) {
		if duplicateProxyNameError(nil, "x") != nil {
			t.Fatal("nil 错误应返回 nil")
		}
		if duplicateProxyNameError(errors.New("some other failure"), "x") != nil {
			t.Fatal("非唯一约束错误应返回 nil")
		}
		duplicate := duplicateProxyNameError(errors.New("UNIQUE constraint failed: proxy_profiles.name"), "代理一")
		if duplicate == nil || duplicate.Error() != "代理名称已存在：代理一" {
			t.Fatalf("唯一约束识别不一致：%v", duplicate)
		}
	})
	t.Run("duplicateImportGroupNameError", func(t *testing.T) {
		if duplicateImportGroupNameError(nil, "x") != nil {
			t.Fatal("nil 错误应返回 nil")
		}
		if duplicateImportGroupNameError(errors.New("boom"), "x") != nil {
			t.Fatal("非唯一约束错误应返回 nil")
		}
		messages := []string{
			"UNIQUE constraint failed: index 'idx_groups_owner_provider_name_unique'",
			"UNIQUE constraint failed: groups.system_account_id, groups.provider_code, groups.name",
			"UNIQUE constraint failed: juhe_business.groups.system_account_id, juhe_business.groups.provider_code, juhe_business.groups.name",
		}
		for _, message := range messages {
			duplicate := duplicateImportGroupNameError(errors.New(message), "分组一")
			if duplicate == nil || duplicate.Error() != "同一供应商下分组名称已存在：分组一" {
				t.Fatalf("唯一约束识别不一致：%s -> %v", message, duplicate)
			}
		}
	})
	t.Run("planSkipDuplicates", func(t *testing.T) {
		if !planSkipDuplicates(&importPlan{options: ImportOptions{SkipDuplicates: boolPtr(true)}}) {
			t.Fatal("显式 true 应命中")
		}
		if planSkipDuplicates(&importPlan{options: ImportOptions{SkipDuplicates: boolPtr(false)}}) {
			t.Fatal("显式 false 不应命中")
		}
		// 缺省（nil 指针）按 Node 默认开启。
		if !planSkipDuplicates(&importPlan{}) {
			t.Fatal("缺省应视为跳过重复")
		}
	})
}

func TestW2RuntimeResetTimeAndBindingHelpers(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

	t.Run("isFutureTimestamp", func(t *testing.T) {
		if !isFutureTimestamp("2030-01-01T00:00:00Z", now) {
			t.Fatal("未来时间应命中")
		}
		if isFutureTimestamp("2020-01-01T00:00:00Z", now) {
			t.Fatal("过去时间不应命中")
		}
		if isFutureTimestamp("   ", now) || isFutureTimestamp("bad", now) {
			t.Fatal("空/非法时间不应命中")
		}
	})
	t.Run("isResourceAuthorizationExpired", func(t *testing.T) {
		if !isResourceAuthorizationExpired("2020-01-01T00:00:00Z", now) {
			t.Fatal("过去时间应过期")
		}
		if isResourceAuthorizationExpired("2030-01-01T00:00:00Z", now) {
			t.Fatal("未来时间不过期")
		}
		if isResourceAuthorizationExpired("", now) || isResourceAuthorizationExpired("bad", now) {
			t.Fatal("空/非法时间视为不过期")
		}
	})
	t.Run("trimSpaces", func(t *testing.T) {
		if got := trimSpaces("  x  "); got != "x" {
			t.Fatalf("trim 不一致：%q", got)
		}
		if got := trimSpaces("\t\n x \v"); got != "x" {
			t.Fatalf("控制符 trim 不一致：%q", got)
		}
	})
	t.Run("bindingIsAuthorizationUnavailable", func(t *testing.T) {
		valid := func(value string) sql.NullString {
			return sql.NullString{Valid: true, String: value}
		}
		if bindingIsAuthorizationUnavailable(&resetSummary{}) {
			t.Fatal("未绑定分组不算不可用")
		}
		if bindingIsAuthorizationUnavailable(&resetSummary{boundGroupID: valid("grp-1")}) {
			t.Fatal("缺组授权 ID 不算不可用")
		}
		if !bindingIsAuthorizationUnavailable(&resetSummary{boundGroupID: valid("grp-1"), boundGroupAuthorizationID: valid("authz-1")}) {
			t.Fatal("账户授权缺失应不可用")
		}
		if !bindingIsAuthorizationUnavailable(&resetSummary{boundGroupID: valid("grp-1"), boundGroupAuthorizationID: valid("authz-1"), authorizationID: valid("authz-2")}) {
			t.Fatal("授权 ID 不一致应不可用")
		}
		if bindingIsAuthorizationUnavailable(&resetSummary{boundGroupID: valid("grp-1"), boundGroupAuthorizationID: valid("authz-1"), authorizationID: valid("authz-1")}) {
			t.Fatal("授权一致应可用")
		}
	})
	t.Run("writeRuntimeResetError 分支", func(t *testing.T) {
		cases := []struct {
			name string
			err  error
			code int
		}{
			{"修订冲突", &RevisionConflictError{}, http.StatusConflict},
			{"账户不存在", errors.New("账户不存在"), http.StatusNotFound},
			{"空消息", errors.New("   "), http.StatusInternalServerError},
			{"普通校验错误", &ValidationError{Message: "无效输入"}, http.StatusBadRequest},
		}
		for _, testCase := range cases {
			recorder := httptest.NewRecorder()
			(&Deps{}).writeRuntimeResetError(recorder, testCase.err)
			if recorder.Code != testCase.code {
				t.Fatalf("%s：期望 %d，实际 %d", testCase.name, testCase.code, recorder.Code)
			}
		}
	})
}

func TestW2ParseTestDraftAccountInputMatrix(t *testing.T) {
	valid := map[string]any{
		"providerCode": "gpt", "providerProtocolProfileId": "prof-gpt", "name": "n",
		"type": "api_key", "healthCheckModel": "m", "healthCheckEndpointMode": "chat_json",
		"groupId": "grp-1",
	}
	parse := func(record map[string]any) string {
		_, message := parseTestDraftAccountInput(record)
		return message
	}

	t.Run("完整合法输入", func(t *testing.T) {
		if message := parse(valid); message != "" {
			t.Fatalf("合法输入应通过：%q", message)
		}
	})
	t.Run("必填缺失", func(t *testing.T) {
		for key := range valid {
			record := map[string]any{}
			for name, value := range valid {
				if name != key {
					record[name] = value
				}
			}
			if message := parse(record); message != "账户测试参数无效" {
				t.Fatalf("缺 %s 应拒绝：%q", key, message)
			}
		}
	})
	t.Run("字段类型矩阵", func(t *testing.T) {
		cases := []struct {
			name   string
			record map[string]any
		}{
			{"未知键", w2Merge(valid, map[string]any{"mystery": 1})},
			{"healthCheckEndpointMode 非法", w2Merge(valid, map[string]any{"healthCheckEndpointMode": "grpc"})},
			{"groupId 空白", w2Merge(valid, map[string]any{"groupId": "  "})},
			{"groupId 非字符串", w2Merge(valid, map[string]any{"groupId": 3})},
			{"credentials 非对象", w2Merge(valid, map[string]any{"credentials": "x"})},
			{"supportedModels 非数组", w2Merge(valid, map[string]any{"supportedModels": "x"})},
			{"supportedModels 空数组", w2Merge(valid, map[string]any{"supportedModels": []any{}})},
			{"supportedModels 空白项", w2Merge(valid, map[string]any{"supportedModels": []any{"  "}})},
			{"modelMappings 非数组", w2Merge(valid, map[string]any{"modelMappings": "x"})},
			{"modelMappings 条目非对象", w2Merge(valid, map[string]any{"modelMappings": []any{"x"}})},
			{"modelMappings 条目非法", w2Merge(valid, map[string]any{"modelMappings": []any{map[string]any{"bogus": 1}}})},
			{"concurrencyLimit 非法", w2Merge(valid, map[string]any{"concurrencyLimit": "x"})},
			{"concurrencyLimit 为零", w2Merge(valid, map[string]any{"concurrencyLimit": float64(0)})},
			{"priority 负数", w2Merge(valid, map[string]any{"priority": float64(-1)})},
			{"superPriorityEnabled 非布尔", w2Merge(valid, map[string]any{"superPriorityEnabled": "yes"})},
			{"fallbackEnabled 非布尔", w2Merge(valid, map[string]any{"fallbackEnabled": 1})},
			{"proxyProfileId 非字符串", w2Merge(valid, map[string]any{"proxyProfileId": 3})},
			{"accountExpiresAt 非字符串", w2Merge(valid, map[string]any{"accountExpiresAt": 3})},
			{"availabilitySchedule 非对象", w2Merge(valid, map[string]any{"availabilitySchedule": "x"})},
			{"notes 非字符串", w2Merge(valid, map[string]any{"notes": 9})},
		}
		for _, testCase := range cases {
			if message := parse(testCase.record); message != "账户测试参数无效" {
				t.Fatalf("%s：期望拒绝，实际 %q", testCase.name, message)
			}
		}
	})
	t.Run("可空与可选值放行", func(t *testing.T) {
		if message := parse(w2Merge(valid, map[string]any{
			"proxyProfileId": nil, "accountExpiresAt": nil,
			"concurrencyLimit": float64(20), "priority": float64(0),
			"superPriorityEnabled": true, "fallbackEnabled": false,
			"supportedModels": []any{"m"}, "modelMappings": []any{},
			"notes": "备注", "credentials": map[string]any{"api_key": "sk-x"},
			"availabilitySchedule": map[string]any{"enabled": true},
		})); message != "" {
			t.Fatalf("可选值组合应通过：%q", message)
		}
	})
}

// w2Merge 合并两个 map（测试输入构造）。
func w2Merge(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func TestW2TrafficMigrationBodyAndHelpers(t *testing.T) {
	t.Run("body 解析", func(t *testing.T) {
		if _, message := parseTrafficMigrationBody(map[string]any{"bogus": 1}); message != "迁移流量参数无效" {
			t.Fatalf("未知键应拒绝：%q", message)
		}
		if _, message := parseTrafficMigrationBody(map[string]any{"targetAccountId": "  "}); message != "迁移流量参数无效" {
			t.Fatalf("空目标应拒绝：%q", message)
		}
		if _, message := parseTrafficMigrationBody(map[string]any{"targetAccountId": "t", "sourceStatus": 3}); message != "迁移流量参数无效" {
			t.Fatalf("状态非字符串应拒绝：%q", message)
		}
		if _, message := parseTrafficMigrationBody(map[string]any{"targetAccountId": "t", "sourceStatus": "weird"}); message != "迁移流量参数无效" {
			t.Fatalf("未知状态应拒绝：%q", message)
		}
		input, message := parseTrafficMigrationBody(map[string]any{"targetAccountId": " t ", "sourceStatus": "unchanged"})
		if message != "" || input.TargetAccountID != "t" || input.SourceStatus != trafficSourceUnchanged {
			t.Fatalf("合法输入不一致：%+v %q", input, message)
		}
	})
	t.Run("时间比较与空值", func(t *testing.T) {
		if !isLaterInstant("2026-01-02T00:00:00Z", "2026-01-01T00:00:00Z") {
			t.Fatal("左侧更晚应命中")
		}
		if isLaterInstant("2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z") {
			t.Fatal("左侧更早不应命中")
		}
		if isLaterInstant("", "2026-01-01T00:00:00Z") || isLaterInstant("bad", "bad") {
			t.Fatal("空/非法输入不应命中")
		}
		if nilToEmpty("") != nil || nilToEmpty("  ") != nil || nilToEmpty("x") != "x" {
			t.Fatal("nilToEmpty 语义不一致")
		}
	})
	t.Run("不可用目标文案", func(t *testing.T) {
		if got := trafficTargetUnavailableMessage(nil); got == "" {
			t.Fatal("nil 目标应有兜底文案")
		}
		if got := trafficTargetUnavailableMessage(&ListItem{Status: "disabled"}); !strings.Contains(got, "已停用") {
			t.Fatalf("disabled 文案不一致：%q", got)
		}
	})
}

func TestW2PatchSmallHelpers(t *testing.T) {
	t.Run("stringSlicesEqual 与 tagListsEqual", func(t *testing.T) {
		if !stringSlicesEqual([]string{"a", "b"}, []string{"a", "b"}) {
			t.Fatal("相等列表应命中")
		}
		if stringSlicesEqual([]string{"a"}, []string{"a", "b"}) || stringSlicesEqual([]string{"a"}, []string{"b"}) {
			t.Fatal("不等列表不应命中")
		}
		if !tagListsEqual([]TagSummary{{Name: "a"}}, []string{"a"}) {
			t.Fatal("等长同名应命中")
		}
		if tagListsEqual([]TagSummary{{Name: "a"}}, []string{"b"}) {
			t.Fatal("名称不同不应命中")
		}
	})
	t.Run("parseStoredBalanceConfig", func(t *testing.T) {
		for _, raw := range []string{"", "  ", "{}"} {
			if parsed, err := parseStoredBalanceConfig(raw); err != nil || parsed != nil {
				t.Fatalf("空配置应归一为 nil：%q %v %v", raw, parsed, err)
			}
		}
		if parsed, err := parseStoredBalanceConfig("{bad"); err != nil || parsed != nil {
			t.Fatalf("坏 JSON 应回退 nil：%v %v", parsed, err)
		}
		parsed, err := parseStoredBalanceConfig(`{"adapter":"builtin"}`)
		if err != nil || parsed == nil || parsed["adapter"] != "builtin" {
			t.Fatalf("有效配置解析不一致：%v %v", parsed, err)
		}
	})
	t.Run("credentialValueJSONText 与 identity 比较", func(t *testing.T) {
		if credentialValueJSONText(nil) != "null" {
			t.Fatalf("nil 序列化不一致：%q", credentialValueJSONText(nil))
		}
		left := map[string]any{"a": 1, "b": []any{"x"}}
		right := map[string]any{"b": []any{"x"}, "a": 1}
		if !balanceIdentityEqual(left, right) {
			t.Fatal("键序无关的深度相等应命中")
		}
		if balanceIdentityEqual(left, map[string]any{"a": 2}) {
			t.Fatal("值不同不应命中")
		}
	})
	t.Run("initialCooldownUntilForStatus", func(t *testing.T) {
		now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
		if got := initialCooldownUntilForStatus("temporary_unavailable", now); got == "" {
			t.Fatal("临时不可用应有 3 秒冷却")
		}
		if got := initialCooldownUntilForStatus("active", now); got != "" {
			t.Fatalf("active 无冷却：%q", got)
		}
	})
	t.Run("pool membership", func(t *testing.T) {
		if !accountApiKeyPoolMembershipEqual(
			Credentials{"api_keys": []any{"k1", "k2"}},
			Credentials{"api_keys": []any{"k2", "k1"}}) {
			t.Fatal("键集合等价应命中")
		}
		if accountApiKeyPoolMembershipEqual(
			Credentials{"api_keys": []any{"k1"}},
			Credentials{"api_keys": []any{"k2"}}) {
			t.Fatal("键不同不应命中")
		}
	})
	t.Run("parseStringJSONColumn", func(t *testing.T) {
		if got := parseStringJSONColumn(sqlNullString(`["a","b"]`)); len(got) != 2 || got[1] != "b" {
			t.Fatalf("数组解析不一致：%v", got)
		}
		if got := parseStringJSONColumn(sql.NullString{}); len(got) != 0 {
			t.Fatalf("空列应为空：%v", got)
		}
		if got := parseStringJSONColumn(sqlNullString("{bad")); len(got) != 0 {
			t.Fatalf("坏 JSON 应为空：%v", got)
		}
	})
	t.Run("scheduleToMap 往返", func(t *testing.T) {
		schedule := &AvailabilitySchedule{Enabled: true, Timezone: "UTC", Mode: "allow_windows",
			Windows: []ScheduleWindow{{DaysOfWeek: []int{1}, Start: "00:00", End: "01:00"}}}
		got := scheduleToMap(schedule)
		if got == nil || got["mode"] != "allow_windows" {
			t.Fatalf("序列化往返不一致：%v", got)
		}
	})
}
