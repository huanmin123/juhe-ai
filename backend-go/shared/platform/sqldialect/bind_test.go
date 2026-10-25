package sqldialect

import "testing"

// 原 TestBindSQLSQLitePassthrough（postgres=false 透传臂）于 2026-10-25 随
// sqlite 方言退役删除：全仓调用方核实恒传 true，改写恒启用。

func TestBindSQLPostgresRewrite(t *testing.T) {
	got := BindSQL("INSERT INTO t (a, b) VALUES (?, ?) WHERE c = ?")
	want := "INSERT INTO t (a, b) VALUES ($1, $2) WHERE c = $3"
	if got != want {
		t.Fatalf("got=%s want=%s", got, want)
	}
}

func TestBindSQLNoPlaceholder(t *testing.T) {
	query := "SELECT 1"
	if got := BindSQL(query); got != query {
		t.Fatalf("无占位符必须原样返回: %s", got)
	}
}
