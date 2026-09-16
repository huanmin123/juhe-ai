package policyreads

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
)

// w11gUpdateRow 构造 UpdateSource 的 SELECT 投影行；列序固定为
// id, name, updated_at 再按 SetFields 附加。
func w11gUpdateRow(updated string, extra ...driver.Value) []driver.Value {
	return append([]driver.Value{"w11g-src", "w11g 原名", updated}, extra...)
}

func TestW11GExternalUpdateSourceArms(t *testing.T) {
	ctx := context.Background()
	const at = "2026-09-01T00:00:00.000Z"
	base := func(extra ...driver.Value) []w11gPStep {
		return []w11gPStep{{}, {cols: []string{"id", "name", "updated_at"}, rows: [][]driver.Value{w11gUpdateRow(at, extra...)}}}
	}
	set := func(fields ...string) map[string]bool {
		out := map[string]bool{}
		for _, field := range fields {
			out[field] = true
		}
		return out
	}
	// 版本戳缺失。
	s := w11gExternalStore(t, nil)
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{SetFields: set("name")}); err == nil {
		t.Fatal("missing expectedUpdatedAt must fail")
	}
	// Begin 失败。
	s = w11gExternalStore(t, []w11gPStep{{beginErr: errors.New("w11g begin failed")}})
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name")}); err == nil {
		t.Fatal("begin error must propagate")
	}
	// SELECT 失败。
	s = w11gExternalStore(t, []w11gPStep{{}, {rowsErr: errors.New("w11g select failed")}})
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name")}); err == nil {
		t.Fatal("select error must propagate")
	}
	// 名称规范化失败（>80 字符）。
	longName := strings.Repeat("名", 81)
	s = w11gExternalStore(t, base())
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: longName}); err == nil {
		t.Fatal("long name must fail")
	}
	// 存储侧 status 损坏。
	s = w11gExternalStore(t, base("bogus-status"))
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("status"), Status: "active"}); err == nil {
		t.Fatal("bad stored status must fail")
	}
	// 输入 status 非法。
	s = w11gExternalStore(t, base("active"))
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("status"), Status: "bogus"}); err == nil {
		t.Fatal("bad input status must fail")
	}
	// scopes 非法。
	s = w11gExternalStore(t, base("[]"))
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("scopes"), Scopes: "not-array"}); err == nil {
		t.Fatal("bad scopes must fail")
	}
	// 存储侧 scopes_json 损坏（变更后需要解码旧值）。
	s = w11gExternalStore(t, base("bad-json"))
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("scopes"), Scopes: []any{"juhe_ai_public:api_key_list:read"}}); err == nil {
		t.Fatal("bad stored scopes must fail")
	}
	// rateLimits 非法。
	s = w11gExternalStore(t, base("[]"))
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("rateLimits"), RateLimits: "not-array"}); err == nil {
		t.Fatal("bad rate limits must fail")
	}
	// 存储侧 rate_limits_json 损坏。
	s = w11gExternalStore(t, base("bad-json"))
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("rateLimits"), RateLimits: []any{}}); err == nil {
		t.Fatal("bad stored rate limits must fail")
	}
	// expiresAt 非法。
	s = w11gExternalStore(t, base(""))
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("expiresAt"), ExpiresAt: "not-a-time"}); err == nil {
		t.Fatal("bad expiresAt must fail")
	}
	// notes 非法（>500 字符）。
	longNotes := strings.Repeat("注", 501)
	s = w11gExternalStore(t, base(""))
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("notes"), Notes: longNotes}); err == nil {
		t.Fatal("long notes must fail")
	}
	// 改名时名称可用性检查失败。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: []string{"id", "name", "updated_at"}, rows: [][]driver.Value{w11gUpdateRow(at)}}, {rowsErr: errors.New("w11g ensure failed")}})
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: "w11g 新名"}); err == nil {
		t.Fatal("ensure-name error must propagate")
	}
	// 改名但占用者是自身 → 放行到 UPDATE。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{cols: []string{"id", "name", "updated_at"}, rows: [][]driver.Value{w11gUpdateRow(at)}},
		{cols: []string{"id"}, rows: [][]driver.Value{{"w11g-src"}}},
		{affected: 1},
		{},
	})
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: "w11g 新名"}); err != nil {
		t.Fatalf("self rename err=%v", err)
	}
	// status 变更时 token 最新时间查询失败。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(4), rows: [][]driver.Value{w11gUpdateRow(at, "active")}}, {rowsErr: errors.New("w11g token latest failed")}})
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("status"), Status: "disabled"}); err == nil {
		t.Fatal("token latest error must propagate")
	}
	// status 变更 + token 时间晚于 source → 以 token 时间为基线推进。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{cols: makeCols(4), rows: [][]driver.Value{w11gUpdateRow("2020-01-01T00:00:00.000Z", "active")}},
		{cols: []string{"updated_at"}, rows: [][]driver.Value{{"2021-01-01T00:00:00.000Z"}}},
		{affected: 1},
		{},
		{},
	})
	outcome, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: "2020-01-01T00:00:00.000Z", SetFields: set("status"), Status: "disabled"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Mutation.UpdatedAt != "2026-09-16T08:00:00.000Z" {
		t.Fatalf("updatedAt=%s", outcome.Mutation.UpdatedAt)
	}
	// 存储时间戳非法 → nextRFC3339 失败。
	s = w11gExternalStore(t, []w11gPStep{{}, {cols: makeCols(4), rows: [][]driver.Value{w11gUpdateRow("bad-time", "active")}}, {}})
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: "bad-time", SetFields: set("status"), Status: "disabled"}); err == nil {
		t.Fatal("bad timestamp must fail nextRFC3339")
	}
	// UPDATE 失败。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{cols: []string{"id", "name", "updated_at"}, rows: [][]driver.Value{w11gUpdateRow(at)}},
		{},
		{execErr: errors.New("w11g update failed")},
	})
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: "w11g 新名"}); err == nil {
		t.Fatal("update error must propagate")
	}
	// 影响行数非 1 → 冲突。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{cols: []string{"id", "name", "updated_at"}, rows: [][]driver.Value{w11gUpdateRow(at)}},
		{},
		{affected: 0},
	})
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: "w11g 新名"}); err == nil {
		t.Fatal("affected != 1 must conflict")
	}
	// 联动 token 状态更新失败。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{cols: makeCols(4), rows: [][]driver.Value{w11gUpdateRow(at, "active")}},
		{},
		{affected: 1},
		{execErr: errors.New("w11g token status failed")},
	})
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("status"), Status: "disabled"}); err == nil {
		t.Fatal("token status update error must propagate")
	}
	// Commit 失败。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{cols: []string{"id", "name", "updated_at"}, rows: [][]driver.Value{w11gUpdateRow(at)}},
		{},
		{affected: 1},
		{commitErr: errors.New("w11g commit failed")},
	})
	if _, err := s.UpdateSource(ctx, "w11g-src", externalSourceUpdateInput{ExpectedUpdatedAt: at, SetFields: set("name"), Name: "w11g 新名"}); err == nil {
		t.Fatal("commit error must propagate")
	}
}

func TestW11GExternalDeleteSourceArms(t *testing.T) {
	ctx := context.Background()
	const at = "2026-09-01T00:00:00.000Z"
	// 内置测试来源不可删除。
	s := w11gExternalStore(t, nil)
	if _, err := s.DeleteSource(ctx, builtInExternalTestSourceID, at); err == nil {
		t.Fatal("builtin source delete must fail")
	}
	s = w11gExternalStore(t, []w11gPStep{{beginErr: errors.New("w11g begin failed")}})
	if _, err := s.DeleteSource(ctx, "w11g-src", at); err == nil {
		t.Fatal("begin error must propagate")
	}
	s = w11gExternalStore(t, []w11gPStep{{}, {rowsErr: errors.New("w11g select failed")}})
	if _, err := s.DeleteSource(ctx, "w11g-src", at); err == nil {
		t.Fatal("select error must propagate")
	}
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{cols: []string{"id", "name", "updated_at"}, rows: [][]driver.Value{{"w11g-src", "w11g 名", at}}},
		{execErr: errors.New("w11g delete tokens failed")},
	})
	if _, err := s.DeleteSource(ctx, "w11g-src", at); err == nil {
		t.Fatal("delete tokens error must propagate")
	}
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{cols: []string{"id", "name", "updated_at"}, rows: [][]driver.Value{{"w11g-src", "w11g 名", at}}},
		{},
		{execErr: errors.New("w11g delete source failed")},
	})
	if _, err := s.DeleteSource(ctx, "w11g-src", at); err == nil {
		t.Fatal("delete source error must propagate")
	}
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{cols: []string{"id", "name", "updated_at"}, rows: [][]driver.Value{{"w11g-src", "w11g 名", at}}},
		{},
		{affected: 0},
	})
	if _, err := s.DeleteSource(ctx, "w11g-src", at); err == nil {
		t.Fatal("affected != 1 must conflict")
	}
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{cols: []string{"id", "name", "updated_at"}, rows: [][]driver.Value{{"w11g-src", "w11g 名", at}}},
		{},
		{affected: 1},
		{commitErr: errors.New("w11g commit failed")},
	})
	if _, err := s.DeleteSource(ctx, "w11g-src", at); err == nil {
		t.Fatal("commit error must propagate")
	}
}
