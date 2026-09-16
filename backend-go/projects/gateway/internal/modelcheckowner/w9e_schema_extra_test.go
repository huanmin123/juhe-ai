package modelcheckowner

// w9e 覆盖率战役：补 business_schema 漂移检测臂与 PG 辅助纯函数、
// business_source 的凭据类型/来源校验纯分支。基于既有 SQLite fixture。

import (
	"context"
	"strings"
	"testing"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func TestW9ESchemaFixtureMismatchArms(t *testing.T) {
	ctx := context.Background()

	// 缺表。
	db := newBusinessSQLiteSchemaFixture(t, "", "")
	defer db.Close()
	tables := make([]string, 0, len(contracts.BusinessSQLiteSchema))
	for table := range contracts.BusinessSQLiteSchema {
		tables = append(tables, table)
	}
	first := tables[0]
	if _, err := db.Exec("DROP TABLE " + quoteSQLiteIdentifier(first)); err != nil {
		t.Fatal(err)
	}
	if err := CheckBusinessSQLiteSchema(ctx, db); err == nil || !strings.Contains(err.Error(), first) {
		t.Fatalf("缺表应失败: %v", err)
	}

	// 缺索引。
	db2 := newBusinessSQLiteSchemaFixture(t, "", "")
	defer db2.Close()
	var indexName string
	for _, spec := range contracts.BusinessSQLiteSchema {
		if len(spec.IndexDefinitions) == 0 {
			continue
		}
		indexName = spec.IndexDefinitions[0].Name
		break
	}
	if indexName != "" {
		if _, err := db2.Exec("DROP INDEX " + quoteSQLiteIdentifier(indexName)); err != nil {
			t.Fatal(err)
		}
		if err := CheckBusinessSQLiteSchema(ctx, db2); err == nil {
			t.Fatal("缺索引应失败")
		}
	}

	// 缺列。
	db3 := newBusinessSQLiteSchemaFixture(t, "", "")
	defer db3.Close()
	var columnName string
	for table, spec := range contracts.BusinessSQLiteSchema {
		_ = table
		columnName = spec.Columns[len(spec.Columns)-1]
		break
	}
	if columnName != "" {
		if _, err := db3.Exec("ALTER TABLE " + quoteSQLiteIdentifier(first) + " DROP COLUMN " + quoteSQLiteIdentifier(columnName)); err != nil {
			t.Skipf("SQLite 版本不支持 DROP COLUMN: %v", err)
		}
		if err := CheckBusinessSQLiteSchema(ctx, db3); err == nil {
			t.Fatal("缺列应失败")
		}
	}
}

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
