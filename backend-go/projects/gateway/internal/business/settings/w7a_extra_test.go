package settings

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 构造函数与方言
// ---------------------------------------------------------------------------

func TestW7AConstructorAndDialectArms(t *testing.T) {
	if _, err := New(nil, SQLite, "", OwnerGate{}); err == nil {
		t.Fatal("nil db must be rejected")
	}
	db, err := sql.Open("sqlite", "file:w7a-settings-ctor?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := New(db, "oracle", "", OwnerGate{}); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
	pg, err := New(db, Postgres, "", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	// settings 包不注入默认 schema：空 schema 时保持不带前缀。
	if got := pg.table("global_settings"); got != "global_settings" {
		t.Fatalf("pg table=%q", got)
	}
	if pg.schema != "" {
		t.Fatalf("schema=%q", pg.schema)
	}
	if got := pg.bind("WHERE key=? AND updated_at=?"); got != "WHERE key=$1 AND updated_at=$2" {
		t.Fatalf("pg bind=%q", got)
	}
	bare := &Store{mode: Postgres}
	if got := bare.table("system_settings"); got != "system_settings" {
		t.Fatalf("empty schema table=%q", got)
	}
	if got := bare.bind("x"); got != "x" {
		t.Fatal("postgres bind changed query")
	}
	sqlite := &Store{mode: SQLite}
	if got := sqlite.bind("a=?"); got != "a=?" {
		t.Fatal("sqlite bind changed query")
	}
}

// ---------------------------------------------------------------------------
// Get / Put System 与 Global 全分支
// ---------------------------------------------------------------------------

func TestW7AGetSystemArms(t *testing.T) {
	store, db := testStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, ok, err := store.GetSystem(ctx, " ", "k"); err == nil || ok {
		t.Fatal("blank system account must fail")
	}
	if _, ok, err := store.GetSystem(ctx, "sys", " "); err == nil || ok {
		t.Fatal("blank key must fail")
	}
	if _, ok, err := store.GetGlobal(ctx, " "); err == nil || ok {
		t.Fatal("blank global key must fail")
	}
	if _, ok, err := store.GetSystem(ctx, "sys", "missing"); err != nil || ok {
		t.Fatalf("missing ok=%v err=%v", ok, err)
	}
	if _, err := store.PutSystem(ctx, "sys", "k", `{"v":1}`, ""); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetSystem(ctx, "sys", "k")
	if err != nil || !ok || got.ValueJSON != `{"v":1}` {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
	// 系统级 CAS 冲突与更新。
	if _, err := store.PutSystem(ctx, "sys", "k", `{"v":2}`, "stale"); !errors.Is(err, ErrCAS) {
		t.Fatalf("stale=%v", err)
	}
	updated, err := store.PutSystem(ctx, "sys", "k", `{"v":2}`, got.UpdatedAt)
	if err != nil || updated.ValueJSON != `{"v":2}` {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	// 系统级新建冲突：已存在键且无期望版本。
	if _, err := store.PutSystem(ctx, "sys", "k", `{"v":3}`, ""); !errors.Is(err, ErrCAS) {
		t.Fatalf("insert conflict=%v", err)
	}
	// 未知系统账户写入后可独立读取。
	if _, err := store.PutSystem(ctx, "sys2", "k", `1`, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.GetSystem(ctx, "sys2", "k"); !ok {
		t.Fatal("sys2 value missing")
	}
	// 空值合法。
	if _, err := store.PutGlobal(ctx, "empty", `null`, ""); err != nil {
		t.Fatal(err)
	}
}

func TestW7APutValidationArms(t *testing.T) {
	store, db := testStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := store.PutGlobal(ctx, " ", `1`, ""); err == nil {
		t.Fatal("blank key must fail")
	}
	if _, err := store.PutSystem(ctx, " ", "k", `1`, ""); err == nil {
		t.Fatal("blank system account must fail")
	}
	if _, err := store.PutGlobal(ctx, "k", `broken`, ""); err == nil {
		t.Fatal("invalid json must fail")
	}
	// 关闭数据库后的查询/事务错误臂。
	broken, err := sql.Open("sqlite", "file:w7a-settings-broken?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	brokenStore, err := New(broken, SQLite, "", OwnerGate{true, true, true})
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := brokenStore.PutGlobal(ctx, "k", `1`, ""); err == nil {
		t.Fatal("closed db put must fail")
	}
	if _, _, err := brokenStore.GetGlobal(ctx, "k"); err == nil {
		t.Fatal("closed db get must fail")
	}
	if _, _, err := brokenStore.get(ctx, "t", "", "k"); err == nil {
		t.Fatal("closed db raw get must fail")
	}
	// 半开门禁。
	store.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	if _, err := store.PutGlobal(ctx, "k", `1`, ""); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("gate=%v", err)
	}
}

// ---------------------------------------------------------------------------
// 模型目录
// ---------------------------------------------------------------------------

func TestW7AListAndReplaceCatalogArms(t *testing.T) {
	store, db := testStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO providers(code,updated_at) VALUES ('openai','r')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProviderModels(ctx, " ", false); err == nil {
		t.Fatal("blank provider must fail")
	}
	if _, err := store.ListProviderModels(ctx, "openai", true); err != nil {
		t.Fatal(err)
	}
	// ReplaceProviderCatalog 校验矩阵。
	order := int64(1)
	negative := int64(-1)
	cases := []struct {
		name  string
		input CatalogReplacement
	}{
		{"blank provider", CatalogReplacement{ExpectedProviderUpdatedAt: "r"}},
		{"blank revision", CatalogReplacement{ProviderCode: "openai"}},
		{"blank model id", CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r", Models: []CatalogModel{{Model: "m", Status: "active", Source: "s", SupportedAPIProtocolsJSON: "[]"}}}},
		{"blank model", CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r", Models: []CatalogModel{{ID: "i", Status: "active", Source: "s", SupportedAPIProtocolsJSON: "[]"}}}},
		{"bad status", CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r", Models: []CatalogModel{{ID: "i", Model: "m", Status: "paused", Source: "s", SupportedAPIProtocolsJSON: "[]"}}}},
		{"bad json", CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r", Models: []CatalogModel{{ID: "i", Model: "m", Status: "active", Source: "s", SupportedAPIProtocolsJSON: "{"}}}},
		{"blank source", CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r", Models: []CatalogModel{{ID: "i", Model: "m", Status: "active", SupportedAPIProtocolsJSON: "[]"}}}},
		{"duplicate", CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r", Models: []CatalogModel{
			{ID: "i1", Model: "m", Status: "active", Source: "s", SupportedAPIProtocolsJSON: "[]"},
			{ID: "i2", Model: "m", Status: "active", Source: "s", SupportedAPIProtocolsJSON: "[]"},
		}}},
		{"order ok", CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r", Models: []CatalogModel{{ID: "i", Model: "m", Status: "active", Source: "s", SupportedAPIProtocolsJSON: "[]", CatalogOrder: &order, CatalogVisible: true}}}},
		{"negative ai limit", CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r", Models: []CatalogModel{{ID: "i", Model: "m2", Status: "active", Source: "s", SupportedAPIProtocolsJSON: "[]", ContextWindowTokens: &negative}}}},
	}
	for _, tc := range cases {
		err := store.ReplaceProviderCatalog(ctx, tc.input)
		if tc.name == "order ok" {
			if err != nil {
				t.Fatalf("%s err=%v", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s must fail", tc.name)
		}
	}
	// 半开门禁。
	store.gate = OwnerGate{Confirmed: true, SchemaReady: true}
	gateErr := store.ReplaceProviderCatalog(ctx, CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r"})
	if !errors.Is(gateErr, ErrOwnerGate) {
		t.Fatalf("gate=%v", gateErr)
	}
	store.gate = OwnerGate{true, true, true}
	// 关闭数据库后的目录替换错误臂。
	broken, err := sql.Open("sqlite", "file:w7a-settings-cat?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	brokenStore, err := New(broken, SQLite, "", OwnerGate{true, true, true})
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Close(); err != nil {
		t.Fatal(err)
	}
	if err := brokenStore.ReplaceProviderCatalog(ctx, CatalogReplacement{ProviderCode: "p", ExpectedProviderUpdatedAt: "r"}); err == nil {
		t.Fatal("closed db replace must fail")
	}
}

func TestW7ANullableStringArms(t *testing.T) {
	if nullableString("") != nil {
		t.Fatal("blank must be nil")
	}
	value := "x"
	if nullableString(value) != "x" {
		t.Fatal("value must pass through")
	}
	if !strings.Contains(ErrCAS.Error(), "stale") {
		t.Fatal("sentinel text")
	}
}
