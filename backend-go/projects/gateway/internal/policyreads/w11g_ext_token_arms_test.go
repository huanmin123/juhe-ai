package policyreads

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

func TestW11GExternalCreateTokenArms(t *testing.T) {
	ctx := context.Background()
	valid := externalTokenInput{Name: "w11g token", Status: "active", Scopes: []any{"juhe_ai_public:api_key_list:read"}}
	// Begin 失败。
	s := w11gExternalStore(t, []w11gPStep{{beginErr: errors.New("w11g begin failed")}})
	if _, err := s.CreateToken(ctx, "w11g-src", valid); err == nil {
		t.Fatal("begin error must propagate")
	}
	// 名称超长。
	longName := strings.Repeat("名", 81)
	s = w11gExternalStore(t, []w11gPStep{{}})
	if _, err := s.CreateToken(ctx, "w11g-src", externalTokenInput{Name: longName}); err == nil {
		t.Fatal("long token name must fail")
	}
	// 名称类型非法。
	s = w11gExternalStore(t, []w11gPStep{{}})
	if _, err := s.CreateToken(ctx, "w11g-src", externalTokenInput{Name: 42}); err == nil {
		t.Fatal("non-string token name must fail")
	}
	// 状态非法。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: []string{"id"}, rows: [][]driver.Value{{"w11g-src"}}}})
	if _, err := s.CreateToken(ctx, "w11g-src", externalTokenInput{Name: "w11g token", Status: "bogus"}); err == nil {
		t.Fatal("bad token status must fail")
	}
	// scopes 非法。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: []string{"id"}, rows: [][]driver.Value{{"w11g-src"}}}})
	if _, err := s.CreateToken(ctx, "w11g-src", externalTokenInput{Name: "w11g token", Scopes: "bad"}); err == nil {
		t.Fatal("bad token scopes must fail")
	}
	// expiresAt 非法。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: []string{"id"}, rows: [][]driver.Value{{"w11g-src"}}}})
	if _, err := s.CreateToken(ctx, "w11g-src", externalTokenInput{Name: "w11g token", ExpiresAt: "not-a-time"}); err == nil {
		t.Fatal("bad token expiresAt must fail")
	}
	// insertToken 失败。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: []string{"id"}, rows: [][]driver.Value{{"w11g-src"}}}, {execErr: errors.New("w11g insert failed")}})
	if _, err := s.CreateToken(ctx, "w11g-src", valid); err == nil {
		t.Fatal("insert error must propagate")
	}
	// Commit 失败。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: []string{"id"}, rows: [][]driver.Value{{"w11g-src"}}}, {}, {commitErr: errors.New("w11g commit failed")}})
	if _, err := s.CreateToken(ctx, "w11g-src", valid); err == nil {
		t.Fatal("commit error must propagate")
	}
}

// w11gTokenRow 构造 UpdateToken 的 SELECT 投影行；基础 5 列再按 SetFields 附加。
func w11gTokenRow(updated string, extra ...driver.Value) []driver.Value {
	return append([]driver.Value{"w11g-tok", "w11g-src", "w11g token", updated, "w11g 来源"}, extra...)
}

func TestW11GExternalUpdateTokenArms(t *testing.T) {
	ctx := context.Background()
	const at = "2026-09-01T00:00:00.000Z"
	set := func(fields ...string) map[string]bool {
		out := map[string]bool{}
		for _, field := range fields {
			out[field] = true
		}
		return out
	}
	// 内置 token 不可编辑。
	s := w11gExternalStore(t, nil)
	if _, err := s.UpdateToken(ctx, "w11g-src", builtInExternalTestTokenID, externalTokenUpdateInput{ExpectedUpdatedAt: at}); err == nil {
		t.Fatal("builtin token edit must fail")
	}
	// 版本戳缺失。
	s = w11gExternalStore(t, nil)
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{SetFields: set("name")}); err == nil {
		t.Fatal("missing expectedUpdatedAt must fail")
	}
	// Begin 失败。
	s = w11gExternalStore(t, []w11gPStep{{beginErr: errors.New("w11g begin failed")}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at}); err == nil {
		t.Fatal("begin error must propagate")
	}
	// SELECT 失败。
	s = w11gExternalStore(t, []w11gPStep{{}, {rowsErr: errors.New("w11g select failed")}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at}); err == nil {
		t.Fatal("select error must propagate")
	}
	// 版本冲突：ConflictError、outcome 为空。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(5), rows: [][]driver.Value{w11gTokenRow("2020-01-01T00:00:00.000Z")}}})
	outcome, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: "w11g 新"})
	var conflict *ConflictError
	if outcome != nil || !errors.As(err, &conflict) {
		t.Fatalf("conflict outcome=%v err=%v", outcome, err)
	}
	// 时间戳非法 → nextRFC3339 失败。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(5), rows: [][]driver.Value{w11gTokenRow("bad-time")}}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: "bad-time", SetFields: set("name"), Name: "w11g 新"}); err == nil {
		t.Fatal("bad timestamp must fail nextRFC3339")
	}
	// 名称超长与类型非法。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(5), rows: [][]driver.Value{w11gTokenRow(at)}}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: strings.Repeat("名", 81)}); err == nil {
		t.Fatal("long name must fail")
	}
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(5), rows: [][]driver.Value{w11gTokenRow(at)}}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: 7}); err == nil {
		t.Fatal("non-string name must fail")
	}
	// 存储侧 status 损坏与输入 status 非法。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(6), rows: [][]driver.Value{w11gTokenRow(at, "bogus")}}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at, SetFields: set("status"), Status: "active"}); err == nil {
		t.Fatal("bad stored status must fail")
	}
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(6), rows: [][]driver.Value{w11gTokenRow(at, "active")}}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at, SetFields: set("status"), Status: "bogus"}); err == nil {
		t.Fatal("bad input status must fail")
	}
	// scopes 非法与存储侧 scopes_json 损坏。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(6), rows: [][]driver.Value{w11gTokenRow(at, "[]")}}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at, SetFields: set("scopes"), Scopes: "bad"}); err == nil {
		t.Fatal("bad scopes must fail")
	}
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(6), rows: [][]driver.Value{w11gTokenRow(at, "bad-json")}}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at, SetFields: set("scopes"), Scopes: []any{"juhe_ai_public:api_key_list:read"}}); err == nil {
		t.Fatal("bad stored scopes must fail")
	}
	// expiresAt 非法。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(6), rows: [][]driver.Value{w11gTokenRow(at, "")}}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at, SetFields: set("expiresAt"), ExpiresAt: "not-a-time"}); err == nil {
		t.Fatal("bad expiresAt must fail")
	}
	// UPDATE 失败、影响行数非 1、Commit 失败。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(5), rows: [][]driver.Value{w11gTokenRow(at)}}, {execErr: errors.New("w11g update failed")}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: "w11g 新"}); err == nil {
		t.Fatal("update error must propagate")
	}
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(5), rows: [][]driver.Value{w11gTokenRow(at)}}, {affected: 0}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: "w11g 新"}); err == nil {
		t.Fatal("affected != 1 must conflict")
	}
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(5), rows: [][]driver.Value{w11gTokenRow(at)}}, {affected: 1}, {commitErr: errors.New("w11g commit failed")}})
	if _, err := s.UpdateToken(ctx, "w11g-src", "w11g-tok", externalTokenUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: "w11g 新"}); err == nil {
		t.Fatal("commit error must propagate")
	}
}

func TestW11GExternalResetBuiltInTestTokenArms(t *testing.T) {
	ctx := context.Background()
	// reset 全链步骤布局：0=Begin, 1=scopes 查询, 2=token 名查询,
	// 3=UPDATE token, 4=UPDATE source, 5=Commit。
	resetSteps := func() []w11gPStep {
		return []w11gPStep{
			{},
			{cols: []string{"scopes_json"}, rows: [][]driver.Value{{"[]"}}},
			{cols: []string{"name"}, rows: [][]driver.Value{{"内置测试 Token"}}},
			{},
			{},
			{},
		}
	}
	s := w11gExternalStore(t, []w11gPStep{{beginErr: errors.New("w11g begin failed")}})
	if _, err := s.ResetBuiltInTestToken(ctx); err == nil {
		t.Fatal("begin error must propagate")
	}
	s = w11gExternalStore(t, []w11gPStep{{}, {rowsErr: errors.New("w11g scopes failed")}})
	if _, err := s.ResetBuiltInTestToken(ctx); err == nil {
		t.Fatal("scopes query error must propagate")
	}
	s = w11gExternalStore(t, []w11gPStep{{}, {}, {rowsErr: errors.New("w11g name failed")}})
	if _, err := s.ResetBuiltInTestToken(ctx); err == nil {
		t.Fatal("token name query error must propagate")
	}
	steps := resetSteps()
	steps[3].execErr = errors.New("w11g update token failed")
	s = w11gExternalStore(t, steps)
	if _, err := s.ResetBuiltInTestToken(ctx); err == nil {
		t.Fatal("token update error must propagate")
	}
	steps = resetSteps()
	steps[4].execErr = errors.New("w11g update source failed")
	s = w11gExternalStore(t, steps)
	if _, err := s.ResetBuiltInTestToken(ctx); err == nil {
		t.Fatal("source update error must propagate")
	}
	steps = resetSteps()
	steps[5].commitErr = errors.New("w11g commit failed")
	s = w11gExternalStore(t, steps)
	if _, err := s.ResetBuiltInTestToken(ctx); err == nil {
		t.Fatal("commit error must propagate")
	}
	// Commit 后 scopes 解码失败。
	steps = resetSteps()
	steps[1].rows = [][]driver.Value{{"bad-json"}}
	s = w11gExternalStore(t, steps)
	if _, err := s.ResetBuiltInTestToken(ctx); err == nil {
		t.Fatal("bad stored scopes must fail after commit")
	}
}
