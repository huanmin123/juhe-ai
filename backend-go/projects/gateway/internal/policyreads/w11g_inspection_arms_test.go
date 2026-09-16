package policyreads

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func w11gInspectionStore(t *testing.T, steps []w11gPStep) *InspectionStore {
	t.Helper()
	store, err := NewInspectionStore(w11gOpenScripted(t, steps), false, func() time.Time { return w11gClock }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestW11GInspectionListPageErrorArms(t *testing.T) {
	ctx := context.Background()
	// 默认规则的供应商名查询失败。
	s := w11gInspectionStore(t, []w11gPStep{{rowsErr: errors.New("w11g providers failed")}})
	if _, err := s.ListPage(ctx); err == nil {
		t.Fatal("provider names error must propagate")
	}
	// 管理行查询失败。
	s = w11gInspectionStore(t, []w11gPStep{{}, {rowsErr: errors.New("w11g policies failed")}})
	if _, err := s.ListPage(ctx); err == nil {
		t.Fatal("policies query error must propagate")
	}
	// 管理行扫描失败（map 值无法进 string 目标）。
	s = w11gInspectionStore(t, []w11gPStep{{}, {cols: makeCols(10), rows: [][]driver.Value{makeRowBad(10)}}})
	if _, err := s.ListPage(ctx); err == nil {
		t.Fatal("policies scan error must propagate")
	}
	// 存储行 scope_type 损坏。
	row := makeRowStrings(10)
	row[4] = "bogus-scope"
	s = w11gInspectionStore(t, []w11gPStep{{}, {cols: makeCols(10), rows: [][]driver.Value{row}}})
	if _, err := s.ListPage(ctx); err == nil {
		t.Fatal("bad scope_type must fail")
	}
	// 存储行 action 损坏。
	row = makeRowStrings(10)
	row[8] = "bogus-action"
	s = w11gInspectionStore(t, []w11gPStep{{}, {cols: makeCols(10), rows: [][]driver.Value{row}}})
	if _, err := s.ListPage(ctx); err == nil {
		t.Fatal("bad action must fail")
	}
	// rows.Next 中途失败。
	s = w11gInspectionStore(t, []w11gPStep{{}, {cols: makeCols(10), rows: [][]driver.Value{makeRowStrings(10)}, nextErr: errors.New("w11g rows failed")}})
	if _, err := s.ListPage(ctx); err == nil {
		t.Fatal("rows error must propagate")
	}
	// providers 查询行扫描失败（默认规则路径）。
	s = w11gInspectionStore(t, []w11gPStep{{cols: makeCols(2), rows: [][]driver.Value{{map[string]any{}, "x"}}}})
	if _, err := s.ListPage(ctx); err == nil {
		t.Fatal("provider scan error must propagate")
	}
}

func TestW11GInspectionFindDetailErrorArms(t *testing.T) {
	ctx := context.Background()
	// 空白 ID → nil。
	s := w11gInspectionStore(t, nil)
	if detail, err := s.FindDetail(ctx, "  "); detail != nil || err != nil {
		t.Fatalf("blank id detail=%v err=%v", detail, err)
	}
	// 默认规则供应商名查询失败：用带 provider 的默认规则（default_gpt_cyber_policy）。
	s = w11gInspectionStore(t, []w11gPStep{{rowsErr: errors.New("w11g provider failed")}})
	if _, err := s.FindDetail(ctx, "default_gpt_cyber_policy"); err == nil {
		t.Fatal("default rule provider error must propagate")
	}
	// 管理行查询失败。
	s = w11gInspectionStore(t, []w11gPStep{{rowsErr: errors.New("w11g detail failed")}})
	if _, err := s.FindDetail(ctx, "rip_w11g"); err == nil {
		t.Fatal("detail query error must propagate")
	}
	// 存储行损坏：scope/action/match 非法逐个触发。
	for i, mod := range []func(row []driver.Value){
		func(row []driver.Value) { row[4] = "bogus-scope" },
		func(row []driver.Value) { row[8] = "bogus-action" },
		func(row []driver.Value) { row[7] = "bad-json" },
	} {
		row := w11gPatchRow()
		mod(row)
		s = w11gInspectionStore(t, []w11gPStep{{cols: makeCols(12), rows: [][]driver.Value{append(row, "provider")}}})
		if _, err := s.FindDetail(ctx, "rip_w11g"); err == nil {
			t.Fatalf("case %d must fail", i)
		}
	}
	// providerName 查询失败（管理行有效 + provider 非空 → normalizedFromPatchRow 后 detail 不查 name；
	// 通过 Create 的 providerName 错误臂覆盖，见下）。
}

func TestW11GInspectionProviderOptionsArms(t *testing.T) {
	ctx := context.Background()
	s := w11gInspectionStore(t, nil)
	// 不支持的协议 / 非 provider 层级 → 空选项。
	options, err := s.ProviderOptions(ctx, "bogus", "provider", "")
	if err != nil || len(options) != 0 {
		t.Fatalf("unsupported protocol options=%v err=%v", options, err)
	}
	options, err = s.ProviderOptions(ctx, "openai", "protocol", "")
	if err != nil || len(options) != 0 {
		t.Fatalf("non-provider scope options=%v err=%v", options, err)
	}
	s = w11gInspectionStore(t, []w11gPStep{{rowsErr: errors.New("w11g options failed")}})
	if _, err := s.ProviderOptions(ctx, "openai", "provider", ""); err == nil {
		t.Fatal("options query error must propagate")
	}
	s = w11gInspectionStore(t, []w11gPStep{{cols: makeCols(2), rows: [][]driver.Value{{map[string]any{}, "x"}}}})
	if _, err := s.ProviderOptions(ctx, "openai", "provider", "w11g"); err == nil {
		t.Fatal("options scan error must propagate")
	}
}

func TestW11GInspectionNormalizeArms(t *testing.T) {
	ctx := context.Background()
	s := w11gInspectionStore(t, nil)
	validMatch := map[string]any{"jsonPathsExists": []any{"error"}}
	cases := []struct {
		name  string
		input inspectionMergedInput
	}{
		{"scopeType", inspectionMergedInput{ScopeType: "bogus", ProtocolCode: "openai", Name: "w11g", Match: validMatch, Action: "retry_no_avoidance"}},
		{"protocol", inspectionMergedInput{ScopeType: "protocol", ProtocolCode: "bogus", Name: "w11g", Match: validMatch, Action: "retry_no_avoidance"}},
		{"protocolUnsupported", inspectionMergedInput{ScopeType: "protocol", ProtocolCode: "bogus", Name: "w11g", Match: validMatch, Action: "retry_no_avoidance"}},
		{"providerMissing", inspectionMergedInput{ScopeType: "provider", ProtocolCode: "openai", Name: "w11g", Match: validMatch, Action: "retry_no_avoidance"}},
		{"name", inspectionMergedInput{ScopeType: "protocol", ProtocolCode: "openai", Name: " ", Match: validMatch, Action: "retry_no_avoidance"}},
		{"priority", inspectionMergedInput{ScopeType: "protocol", ProtocolCode: "openai", Name: "w11g", Priority: 10000, Match: validMatch, Action: "retry_no_avoidance"}},
		{"matchEmpty", inspectionMergedInput{ScopeType: "protocol", ProtocolCode: "openai", Name: "w11g", Match: map[string]any{}, Action: "retry_no_avoidance"}},
		{"action", inspectionMergedInput{ScopeType: "protocol", ProtocolCode: "openai", Name: "w11g", Match: validMatch, Action: "bogus"}},
		{"notes", inspectionMergedInput{ScopeType: "protocol", ProtocolCode: "openai", Name: "w11g", Match: validMatch, Action: "retry_no_avoidance", Notes: "  "}},
		{"protocolBoundProvider", inspectionMergedInput{ScopeType: "protocol", ProtocolCode: "openai", ProviderCode: "w11g-provider", Name: "w11g", Match: validMatch, Action: "retry_no_avoidance"}},
	}
	for _, testCase := range cases {
		if _, err := s.normalizeMerged(ctx, s.db, testCase.input, false); err == nil {
			t.Fatalf("case %s must fail", testCase.name)
		}
	}
	// membership 校验：查询失败与供应商未启用档案。
	s = w11gInspectionStore(t, []w11gPStep{{rowsErr: errors.New("w11g membership failed")}})
	if _, err := s.normalizeMerged(ctx, s.db, inspectionMergedInput{ScopeType: "provider", ProtocolCode: "openai", ProviderCode: "w11g-provider", Name: "w11g", Match: validMatch, Action: "retry_no_avoidance"}, true); err == nil {
		t.Fatal("membership query error must propagate")
	}
	s = w11gInspectionStore(t, []w11gPStep{{}})
	if _, err := s.normalizeMerged(ctx, s.db, inspectionMergedInput{ScopeType: "provider", ProtocolCode: "openai", ProviderCode: "w11g-provider", Name: "w11g", Match: validMatch, Action: "retry_no_avoidance"}, true); err == nil {
		t.Fatal("membership miss must fail")
	}
	// match 规范化错误细节：clientProfiles 不支持、列表超 50 项、元素空。
	bigList := make([]any, 51)
	for i := range bigList {
		bigList[i] = "x"
	}
	for name, match := range map[string]map[string]any{
		"clientProfiles": {"clientProfiles": []any{"bogus"}, "jsonPathsExists": []any{"error"}},
		"tooMany":        {"jsonPathsExists": bigList},
		"emptyItem":      {"jsonPathsExists": []any{" "}},
		"longItem":       {"jsonPathsExists": []any{string(make([]byte, 201))}},
		"notList":        {"jsonPathsExists": "not-list"},
		"longItem200":    {"jsonPathsExists": []any{string(make([]byte, 200)), "x"}, "jsonPathsMissing": []any{"y"}},
	} {
		if _, err := s.normalizeMerged(ctx, s.db, inspectionMergedInput{ScopeType: "protocol", ProtocolCode: "openai", Name: "w11g", Match: match, Action: "retry_no_avoidance"}, false); err == nil {
			t.Fatalf("match case %s must fail", name)
		}
	}
}

func TestW11GInspectionCreateAndPatchArms(t *testing.T) {
	ctx := context.Background()
	validInput := &InspectionCreateInput{Name: "w11g 规则", ScopeType: "protocol", ProtocolCode: "openai", Match: map[string]any{"jsonPathsExists": []any{"error"}}, Action: "retry_no_avoidance"}
	// 容量检查查询失败。
	s := w11gInspectionStore(t, []w11gPStep{{rowsErr: errors.New("w11g capacity failed")}})
	if _, err := s.Create(ctx, validInput); err == nil {
		t.Fatal("capacity query error must propagate")
	}
	// 容量检查 rows 中途失败。
	s = w11gInspectionStore(t, []w11gPStep{{cols: []string{"id"}, nextErr: errors.New("w11g rows failed")}})
	if _, err := s.Create(ctx, validInput); err == nil {
		t.Fatal("capacity rows error must propagate")
	}
	// 规范化失败（provider 层级缺供应商编码）。
	s = w11gInspectionStore(t, []w11gPStep{{}})
	if _, err := s.Create(ctx, &InspectionCreateInput{Name: "w11g", ScopeType: "provider", ProtocolCode: "openai", Match: validInput.Match, Action: "retry_no_avoidance"}); err == nil {
		t.Fatal("normalize error must propagate")
	}
	// INSERT 失败。
	s = w11gInspectionStore(t, []w11gPStep{{}, {execErr: errors.New("w11g insert failed")}})
	if _, err := s.Create(ctx, validInput); err == nil {
		t.Fatal("insert error must propagate")
	}
	// providerName 查询失败（无 provider 的规则不触发，需 provider_code 非空）。
	providerInput := &InspectionCreateInput{Name: "w11g 规则", ScopeType: "provider", ProtocolCode: "openai", ProviderCode: ptrString("w11g-provider"), Match: validInput.Match, Action: "retry_no_avoidance"}
	s = w11gInspectionStore(t, []w11gPStep{
		{},
		{cols: []string{"c"}, rows: [][]driver.Value{{int64(1)}}},
		{},
		{rowsErr: errors.New("w11g provider name failed")},
	})
	if _, err := s.Create(ctx, providerInput); err == nil {
		t.Fatal("provider name error must propagate")
	}
	// Patch：查询失败。
	s = w11gInspectionStore(t, []w11gPStep{{rowsErr: errors.New("w11g patch select failed")}})
	if _, err := s.Patch(ctx, "rip_w11g", &InspectionPatch{ExpectedAt: "2026-09-01T00:00:00.000Z", SetFields: map[string]bool{}}); err == nil {
		t.Fatal("patch select error must propagate")
	}
	// Patch：时间戳非法。
	row := w11gPatchRow()
	row[10] = "bad-time"
	s = w11gInspectionStore(t, []w11gPStep{{cols: makeCols(11), rows: [][]driver.Value{row}}})
	if _, err := s.Patch(ctx, "rip_w11g", &InspectionPatch{ExpectedAt: "bad-time", SetFields: map[string]bool{"name": true}, Name: ptrString("w11g 新")}); err == nil {
		t.Fatal("bad timestamp must fail patch")
	}
	// Patch：current providerName 查询失败（行带 provider_code）。
	row = w11gPatchRow()
	row[6] = "w11g-provider"
	s = w11gInspectionStore(t, []w11gPStep{{cols: makeCols(11), rows: [][]driver.Value{row}}, {rowsErr: errors.New("w11g current provider failed")}})
	if _, err := s.Patch(ctx, "rip_w11g", &InspectionPatch{ExpectedAt: "2026-09-01T00:00:00.000Z", SetFields: map[string]bool{}}); err == nil {
		t.Fatal("current provider name error must propagate")
	}
	// Patch：UPDATE 失败与影响行数 0。
	row = w11gPatchRow()
	s = w11gInspectionStore(t, []w11gPStep{{cols: makeCols(11), rows: [][]driver.Value{row}}, {execErr: errors.New("w11g update failed")}})
	if _, err := s.Patch(ctx, "rip_w11g", &InspectionPatch{ExpectedAt: "2026-09-01T00:00:00.000Z", SetFields: map[string]bool{"name": true}, Name: ptrString("w11g 新")}); err == nil {
		t.Fatal("update error must propagate")
	}
	s = w11gInspectionStore(t, []w11gPStep{{cols: makeCols(11), rows: [][]driver.Value{row}}, {affected: 0}})
	outcome, err := s.Patch(ctx, "rip_w11g", &InspectionPatch{ExpectedAt: "2026-09-01T00:00:00.000Z", SetFields: map[string]bool{"name": true}, Name: ptrString("w11g 新")})
	if err != nil || outcome.Status != "conflict" {
		t.Fatalf("affected=0 outcome=%+v err=%v", outcome, err)
	}
	// Patch：下一状态 providerName 查询失败（providerCode 变更触发 membership 与名称查询）。
	s = w11gInspectionStore(t, []w11gPStep{
		{cols: makeCols(11), rows: [][]driver.Value{row}},
		{cols: []string{"c"}, rows: [][]driver.Value{{int64(1)}}},
		{affected: 1},
		{rowsErr: errors.New("w11g next provider failed")},
	})
	if _, err := s.Patch(ctx, "rip_w11g", &InspectionPatch{ExpectedAt: "2026-09-01T00:00:00.000Z", SetFields: map[string]bool{"providerCode": true}, ProviderCode: "w11g-other", ScopeType: ptrString("provider"), ProtocolCode: ptrString("openai"), Match: validInput.Match, Action: ptrString("retry_no_avoidance")}); err == nil {
		t.Fatal("next provider name error must propagate")
	}
}

// w11gPatchRow 构造 Patch/FindDetail 的 11 列管理行（enabled/priority 为 int）。
func w11gPatchRow() []driver.Value {
	row := makeRowStrings(11)
	row[2] = int64(0)
	row[3] = int64(100)
	row[4] = "protocol"
	row[5] = "openai"
	row[6] = nil
	row[7] = `{"jsonPathsExists":["error"]}`
	row[8] = "retry_no_avoidance"
	row[10] = "2026-09-01T00:00:00.000Z"
	return row
}
