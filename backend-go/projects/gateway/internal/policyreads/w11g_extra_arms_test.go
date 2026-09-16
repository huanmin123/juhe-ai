package policyreads

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func TestW11GExternalHelperArms(t *testing.T) {
	if _, err := NewExternalStore(nil, false, time.Now, nil, nil, ""); err == nil {
		t.Fatal("nil db must fail")
	}
	// 非数组 JSON 的 scopes 解码失败。
	if _, err := decodeExternalScopes("5"); err == nil {
		t.Fatal("non-array scopes json must fail")
	}
	// buildSourceCreateRow 的逐字段错误臂（直调，无需 DB）。
	now := "2026-09-16T08:00:00.000Z"
	newID := func(prefix string) string { return prefix + "_w11g" }
	for name, input := range map[string]externalSourceInput{
		"name":       {Name: 5},
		"status":     {Name: "w11g", Status: 5},
		"statusBad":  {Name: "w11g", Status: "bogus"},
		"scopes":     {Name: "w11g", Scopes: 5},
		"scopesBad":  {Name: "w11g", Scopes: []any{5}},
		"limits":     {Name: "w11g", RateLimits: 5},
		"expiresAt":  {Name: "w11g", ExpiresAt: 5},
		"expiresBad": {Name: "w11g", ExpiresAt: "not-a-time"},
		"notes":      {Name: "w11g", Notes: 5},
	} {
		if _, _, _, err := buildSourceCreateRow(input, now, newID); err == nil {
			t.Fatalf("create row case %s must fail", name)
		}
	}
	row, _, scopes, err := buildSourceCreateRow(externalSourceInput{
		Name: "w11g 来源", Scopes: []any{"juhe_ai_public:api_key_list:read"},
	}, now, newID)
	if err != nil || row == nil || len(scopes) != 1 {
		t.Fatalf("valid row=%v err=%v", row, err)
	}
}

func TestW11GExternalUniqueMappingAndResolveArms(t *testing.T) {
	ctx := context.Background()
	input := externalSourceInput{Name: "w11g 来源", Scopes: []any{"juhe_ai_public:api_key_list:read"}}
	// insertSource 的唯一约束映射。
	s := w11gExternalStore(t, []w11gPStep{
		{},
		{},
		{execErr: errors.New("INSERT failed: UNIQUE constraint failed: external_integration_sources.name")},
	})
	var validation *ValidationError
	if _, _, err := s.CreateAuthorization(ctx, input); !errors.As(err, &validation) {
		t.Fatalf("unique source err=%v", err)
	}
	// insertToken 的唯一约束映射。
	s = w11gExternalStore(t, []w11gPStep{
		{},
		{},
		{},
		{execErr: errors.New("INSERT failed: UNIQUE constraint failed: external_integration_source_tokens.token_hash")},
	})
	if _, _, err := s.CreateAuthorization(ctx, input); !errors.As(err, &validation) {
		t.Fatalf("unique token err=%v", err)
	}
	// resolveSourceForToken：空白 ID 与查询失败。
	s = w11gExternalStore(t, []w11gPStep{{}})
	if _, err := s.CreateToken(ctx, "   ", externalTokenInput{Name: "w11g token"}); !errors.As(err, &validation) {
		t.Fatalf("blank source err=%v", err)
	}
	s = w11gExternalStore(t, []w11gPStep{{}, {rowsErr: errors.New("w11g resolve failed")}})
	if _, err := s.CreateToken(ctx, "w11g-src", externalTokenInput{Name: "w11g token"}); err == nil {
		t.Fatal("resolve error must propagate")
	}
	// ListPage：primary tokens 扫描错误。
	s = w11gExternalStore(t, []w11gPStep{
		{cols: makeCols(10), rows: [][]driver.Value{makeRowStrings(10)}},
		{cols: makeCols(4), rows: [][]driver.Value{{map[string]any{}, "src", "p", "s"}}},
	})
	if _, err := s.ListPage(ctx, nil, nil, "", ""); err == nil {
		t.Fatal("primary tokens scan error must propagate")
	}
	// Postgres 模式的 keyword LIKE 分支。
	pgStore, err := NewExternalStore(w11gOpenScripted(t, []w11gPStep{{}, {}}), true, func() time.Time { return w11gClock }, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgStore.ListPage(ctx, nil, nil, "w11g", "active"); err != nil {
		t.Fatalf("pg keyword list err=%v", err)
	}
}

func TestW11GInspectionListRowArms(t *testing.T) {
	ctx := context.Background()
	// 管理行的 enabled/priority 必须是 int；scope/action 解码错误。
	overviewRow := makeRowStrings(10)
	overviewRow[2] = int64(1)
	overviewRow[3] = int64(10)
	badScope := append([]driver.Value{}, overviewRow...)
	badScope[4] = "bogus-scope"
	s := w11gInspectionStore(t, []w11gPStep{{}, {cols: makeCols(10), rows: [][]driver.Value{badScope}}})
	if _, err := s.ListPage(ctx); err == nil {
		t.Fatal("bad scope_type must fail list")
	}
	badAction := append([]driver.Value{}, overviewRow...)
	badAction[8] = "bogus-action"
	s = w11gInspectionStore(t, []w11gPStep{{}, {cols: makeCols(10), rows: [][]driver.Value{badAction}}})
	if _, err := s.ListPage(ctx); err == nil {
		t.Fatal("bad action must fail list")
	}
	// NewInspectionStore nil db。
	if _, err := NewInspectionStore(nil, false, time.Now, nil, nil); err == nil {
		t.Fatal("nil db must fail")
	}
}
