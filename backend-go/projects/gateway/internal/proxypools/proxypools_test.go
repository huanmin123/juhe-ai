package proxypools

// 代理池家族契约测试。本文件只承载纯逻辑（输入白名单、成员校验、两阶段
// 替换计划、选项合并排序），离线可跑；真实 PG 契约测试在 pool_store_pg_test.go
// （pgtestkit 授权门，缺 JUHE_AI_TEST_EXTERNAL=1 时跳过）。

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCreateInputStrictWhitelist(t *testing.T) {
	if _, message := parseCreateInput(map[string]any{"name": "池", "strategy": "round_robin", "members": []any{}, "unknown": 1}); message != "代理池参数无效" {
		t.Fatalf("unknown key not rejected: %q", message)
	}
	// members 必填。
	if _, message := parseCreateInput(map[string]any{"name": "池", "strategy": "round_robin"}); message != "代理池参数无效" {
		t.Fatalf("missing members not rejected: %q", message)
	}
	if _, message := parseCreateInput(map[string]any{"name": "池", "strategy": "random", "members": []any{}}); message != "代理池参数无效" {
		t.Fatalf("invalid strategy not rejected: %q", message)
	}
	if _, message := parseCreateInput(map[string]any{"name": "  ", "strategy": "round_robin", "members": []any{}}); message != "代理池参数无效" {
		t.Fatalf("blank name not rejected: %q", message)
	}
	input, message := parseCreateInput(map[string]any{
		"name":     "  池A ",
		"strategy": "weighted",
		"enabled":  false,
		"members": []any{
			map[string]any{"proxyProfileId": "p-1"},
			map[string]any{"proxyProfileId": " p-2 ", "weight": 3.0, "enabled": false},
		},
	})
	if message != "" {
		t.Fatalf("valid body rejected: %q", message)
	}
	if input.Name != "池A" || input.Strategy != StrategyWeighted || input.Enabled {
		t.Fatalf("create input wrong: %#v", input)
	}
	// enabled 缺省 true；weight 缺省 1；enabled 缺省 true。
	if !input.Members[0].Enabled || input.Members[0].Weight != 1 || input.Members[1].Weight != 3 || input.Members[1].Enabled {
		t.Fatalf("member defaults wrong: %#v", input.Members)
	}
}

func TestParseCreateInputMemberShape(t *testing.T) {
	if _, message := parseCreateInput(map[string]any{"name": "池", "strategy": "round_robin", "members": "p-1"}); message != "代理池参数无效" {
		t.Fatalf("non-array members not rejected: %q", message)
	}
	if _, message := parseCreateInput(map[string]any{"name": "池", "strategy": "round_robin", "members": []any{map[string]any{"proxyProfileId": "p-1", "host": "h"}}}); message != "代理池参数无效" {
		t.Fatalf("member unknown key not rejected: %q", message)
	}
	if _, message := parseCreateInput(map[string]any{"name": "池", "strategy": "round_robin", "members": []any{map[string]any{"weight": 2.5}}}); message != "代理池参数无效" {
		t.Fatalf("missing proxyProfileId not rejected: %q", message)
	}
	if _, message := parseCreateInput(map[string]any{"name": "池", "strategy": "round_robin", "members": []any{map[string]any{"proxyProfileId": "p-1", "weight": 0.5}}}); message != "代理池参数无效" {
		t.Fatalf("fractional weight not rejected: %q", message)
	}
}

func TestParsePatchInputRequiresRevisionAndPayload(t *testing.T) {
	if _, message := parsePatchInput(map[string]any{"name": "池"}); message != "代理池参数无效" {
		t.Fatalf("missing expectedUpdatedAt not rejected: %q", message)
	}
	if _, message := parsePatchInput(map[string]any{"expectedUpdatedAt": "2026-01-01T00:00:00Z"}); message != "代理池参数无效" {
		t.Fatalf("payload-only revision not rejected: %q", message)
	}
	input, message := parsePatchInput(map[string]any{
		"expectedUpdatedAt": "2026-01-01T00:00:00.000000Z",
		"members":           []any{},
	})
	if message != "" || !input.HasMembers || len(input.Members) != 0 {
		t.Fatalf("empty members replace rejected: %q %#v", message, input)
	}
	// members 出现即完整替换：null 不是合法数组。
	if _, message := parsePatchInput(map[string]any{"expectedUpdatedAt": "2026-01-01T00:00:00Z", "members": nil}); message != "代理池参数无效" {
		t.Fatalf("null members not rejected: %q", message)
	}
}

func TestValidateMembersContract(t *testing.T) {
	if err := validateMembers([]memberInput{{ProxyProfileID: "a", Weight: 1}, {ProxyProfileID: "a", Weight: 1}}); err == nil || err.Error() != ErrMemberDuplicate.Error() {
		t.Fatalf("duplicate member not rejected: %v", err)
	}
	var duplicate *MemberDuplicateError
	if err := validateMembers([]memberInput{{ProxyProfileID: "a", Weight: 1}, {ProxyProfileID: "a", Weight: 1}}); err == nil || !asType(err, &duplicate) {
		t.Fatalf("duplicate error type wrong: %v", err)
	}
	many := make([]memberInput, memberLimit+1)
	for index := range many {
		many[index] = memberInput{ProxyProfileID: "p-" + itoa(index)}
	}
	var limit *MemberLimitError
	if err := validateMembers(many); err == nil || !asType(err, &limit) {
		t.Fatalf("member limit not enforced: %v", err)
	}
	if err := validateMembers([]memberInput{{ProxyProfileID: "a", Weight: 0}, {ProxyProfileID: "b", Weight: 101}}); err == nil || err.Error() != ErrMemberInvalid.Error() {
		t.Fatalf("weight range not enforced: %v", err)
	}
	if err := validateMembers([]memberInput{{ProxyProfileID: "  "}}); err == nil || err.Error() != ErrMemberInvalid.Error() {
		t.Fatalf("blank profile id not rejected: %v", err)
	}
	if err := validateMembers(nil); err != nil {
		t.Fatalf("empty members should pass: %v", err)
	}
}

func asType(err error, target any) bool {
	switch target.(type) {
	case **MemberDuplicateError:
		_, ok := err.(*MemberDuplicateError)
		return ok
	case **MemberLimitError:
		_, ok := err.(*MemberLimitError)
		return ok
	}
	return false
}

func TestPlanMemberReplaceKeepsIntersectionAndReorders(t *testing.T) {
	current := []memberState{
		{profileID: "a", priority: 0, weight: 1, enabled: true},
		{profileID: "b", priority: 1, weight: 2, enabled: true},
	}
	// [C, A(weight 5)]：B 移除、C 新增、A 原地改权重、priority 按新顺序。
	plan := planMemberReplace(current, []memberInput{
		{ProxyProfileID: "c", Weight: 1, Enabled: true},
		{ProxyProfileID: "a", Weight: 5, Enabled: true},
	})
	if len(plan.toAdd) != 1 || plan.toAdd[0].ProxyProfileID != "c" {
		t.Fatalf("toAdd wrong: %#v", plan.toAdd)
	}
	if len(plan.toRemove) != 1 || plan.toRemove[0] != "b" || len(plan.removedProfileIDs) != 1 {
		t.Fatalf("toRemove wrong: %#v", plan.toRemove)
	}
	if len(plan.kept) != 1 || plan.kept[0].ProxyProfileID != "a" || plan.kept[0].Weight != 5 {
		t.Fatalf("kept wrong: %#v", plan.kept)
	}
	if !plan.changed {
		t.Fatal("replace should be marked changed")
	}
	// 完全一致（含 priority 位置）→ 不变更。
	identical := planMemberReplace(current, []memberInput{
		{ProxyProfileID: "a", Weight: 1, Enabled: true},
		{ProxyProfileID: "b", Weight: 2, Enabled: true},
	})
	if identical.changed {
		t.Fatalf("identical plan marked changed: %#v", identical)
	}
	// 纯重排也是变更（priority 由列表位置决定）。
	reordered := planMemberReplace(current, []memberInput{
		{ProxyProfileID: "b", Weight: 2, Enabled: true},
		{ProxyProfileID: "a", Weight: 1, Enabled: true},
	})
	if !reordered.changed || len(reordered.toAdd) != 0 || len(reordered.toRemove) != 0 {
		t.Fatalf("reorder plan wrong: %#v", reordered)
	}
}

func TestParseSelectedMemberOptionIds(t *testing.T) {
	if _, message := parseSelectedMemberOptionIds(urlValuesOf(map[string][]string{"selectedIds": {"a,b"}})); message != "代理池成员选项 selectedIds 无效" {
		t.Fatalf("csv selectedIds not rejected: %q", message)
	}
	if _, message := parseSelectedMemberOptionIds(urlValuesOf(map[string][]string{"selectedIds[0]": {"a"}})); message != "代理池成员选项 selectedIds 无效" {
		t.Fatalf("indexed selectedIds not rejected: %q", message)
	}
	many := urlValuesOf(map[string][]string{})
	for index := 0; index < 21; index++ {
		many["selectedIds[]"] = append(many["selectedIds[]"], "id-"+itoa(index))
	}
	if _, message := parseSelectedMemberOptionIds(many); message != "代理池成员选项 selectedIds 最多 20 个" {
		t.Fatalf("selectedIds cap not enforced: %q", message)
	}
	ids, message := parseSelectedMemberOptionIds(urlValuesOf(map[string][]string{"selectedIds": {"b", " a "}, "selectedIds[]": {"b"}}))
	if message != "" || len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("selectedIds merge wrong: %#v %q", ids, message)
	}
}

func urlValuesOf(raw map[string][]string) map[string][]string { return raw }

func TestMemberOptionSummaryShape(t *testing.T) {
	// §8.1：member-options 只暴露 id/name/type/enabled/testStatus。
	encoded, err := json.Marshal(MemberOptionSummary{ID: "p-1", Name: "代理", Type: "http", Enabled: true, TestStatus: "passed"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(encoded)
	for _, key := range []string{`"id"`, `"name"`, `"type"`, `"enabled"`, `"testStatus"`} {
		if !strings.Contains(text, key) {
			t.Fatalf("member option missing %s: %s", key, text)
		}
	}
	for _, forbidden := range []string{"host", "port", "username", "password", "outboundIp"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("member option leaks %s: %s", forbidden, text)
		}
	}
}

func TestMemberOptionMergeOrder(t *testing.T) {
	merged := mergeMemberOptionRows(
		[]memberOptionRow{
			{id: "w2", name: "Beta", typeCode: "http", enabled: true, testStatus: "unknown", updatedAt: "2026-01-02"},
			{id: "w1", name: "Alpha", typeCode: "socks5", enabled: false, testStatus: "failed", updatedAt: "2026-01-03"},
		},
		[]memberOptionRow{
			{id: "s1", name: "Alpha", typeCode: "http", enabled: true, testStatus: "passed", updatedAt: "2026-01-01"},
		})
	// name ASC；同名按 updated_at DESC（窗口行 01-03 胜出），再去 id ASC。
	if len(merged) != 3 || merged[0].ID != "w1" || merged[0].Enabled || merged[0].TestStatus != "failed" ||
		merged[1].ID != "s1" || !merged[1].Enabled || merged[2].ID != "w2" {
		t.Fatalf("member options merge wrong: %#v", merged)
	}
}

func TestParseInvalidQueries(t *testing.T) {
	if _, present := integerQueryValue("abc"); present {
		t.Fatal("non-integer limit should be absent")
	}
	if value, present := integerQueryValue(" 7 "); !present || value != 7 {
		t.Fatalf("trimmed integer wrong: %v %v", value, present)
	}
	if optionLimitValue(0, true) != 1 || optionLimitValue(999, true) != 50 || optionLimitValue(0, false) != 50 {
		t.Fatal("option limit clamp wrong")
	}
	if intFromQuery(1e300, true, 20) != int(^uint(0)>>1) {
		t.Fatal("intFromQuery overflow clamp wrong")
	}
}

func TestWriteMutationErrorEnvelopes(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeMutationError(recorder, ErrRevisionConflict, "fallback")
	if recorder.Code != 409 || !strings.Contains(recorder.Body.String(), "proxy_pool_revision_conflict") {
		t.Fatalf("revision conflict envelope wrong: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	writeMutationError(recorder, &PoolBoundAccountsError{AccountCount: 1, AccountNames: []string{"账户甲"}}, "fallback")
	if recorder.Code != 409 || !strings.Contains(recorder.Body.String(), "proxy_pool_bound_accounts") || !strings.Contains(recorder.Body.String(), "账户甲") {
		t.Fatalf("bound accounts envelope wrong: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	writeMutationError(recorder, &MemberInvalidError{ProfileID: "p-x"}, "fallback")
	if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), "proxy_pool_member_invalid") {
		t.Fatalf("member invalid envelope wrong: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	writeMutationError(recorder, &PoolNameDuplicateError{Name: "池"}, "fallback")
	if recorder.Code != 409 || !strings.Contains(recorder.Body.String(), "代理池名称已存在：池") {
		t.Fatalf("duplicate name envelope wrong: %d %s", recorder.Code, recorder.Body.String())
	}
}
