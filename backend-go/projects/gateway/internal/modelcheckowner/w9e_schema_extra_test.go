package modelcheckowner

// w9e 覆盖率战役：business_schema PG 辅助纯函数与 business_source 凭据
// 纯分支（原 SQLite 漂移臂随 CheckBusinessSQLiteSchema 退役，D17）。

import (
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func TestW9EPostgresSchemaHelpers(t *testing.T) {
	// postgresReferentialAction 全码表。
	codes := map[string]string{"a": "NO ACTION", " A ": "NO ACTION", "r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT", "x": "", "NO ACTION": ""}
	for code, want := range codes {
		if got := postgresReferentialAction(code); got != want {
			t.Fatalf("postgresReferentialAction(%q) = %q, want %q", code, got, want)
		}
	}
	// postgresSchemaIndexMismatch：谓词不符。
	actual := postgresSchemaIndex{columns: []string{"a", "b"}, predicate: "a > 0"}
	required := contracts.SQLiteIndexDefinition{Name: "idx", Columns: []string{"a", "b"}, Unique: true, Predicate: "a > 1"}
	if postgresSchemaIndexMismatch(actual, required) == "" {
		t.Fatal("谓词漂移应报不匹配")
	}
	match := postgresSchemaIndex{columns: []string{"a", "b"}, unique: true, predicate: "a > 1"}
	if postgresSchemaIndexMismatch(match, required) != "" {
		t.Fatal("完全一致不应报不匹配")
	}
	// hasPostgresConstraint。
	constraints := map[int64]*postgresSchemaConstraint{
		1: {kind: "u", columns: []string{"a", "b"}},
	}
	if !hasPostgresConstraint(constraints, "u", []string{"a", "b"}) {
		t.Fatal("匹配约束应命中")
	}
	if hasPostgresConstraint(constraints, "u", []string{"b", "a"}) {
		t.Fatal("列顺序不同不应命中")
	}
	// hasPostgresForeignKey。
	foreignKeys := map[int64]*postgresSchemaForeignKey{
		7: {refSchema: "public", refTable: "parent", columns: []string{"pid"}, refColumns: []string{"id"}, onUpdate: "a", onDelete: "c"},
	}
	spec := contracts.SQLiteForeignKeySpec{Columns: []string{"pid"}, RefTable: "parent", RefColumns: []string{"id"}, OnUpdate: "a", OnDelete: "c"}
	if !hasPostgresForeignKey(foreignKeys, "public", spec) {
		t.Fatal("外键应命中")
	}
	wrong := spec
	wrong.OnDelete = "r"
	if hasPostgresForeignKey(foreignKeys, "public", wrong) {
		t.Fatal("onDelete 漂移不应命中")
	}
}
