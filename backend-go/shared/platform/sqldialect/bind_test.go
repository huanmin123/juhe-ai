package sqldialect

import "testing"

func TestBindSQLSQLitePassthrough(t *testing.T) {
	query := "SELECT * FROM t WHERE a = ? AND b = ?"
	if got := BindSQL(false, query); got != query {
		t.Fatalf("SQLite 模式必须原样返回: %s", got)
	}
}

func TestBindSQLPostgresRewrite(t *testing.T) {
	got := BindSQL(true, "INSERT INTO t (a, b) VALUES (?, ?) WHERE c = ?")
	want := "INSERT INTO t (a, b) VALUES ($1, $2) WHERE c = $3"
	if got != want {
		t.Fatalf("got=%s want=%s", got, want)
	}
}

func TestBindSQLNoPlaceholder(t *testing.T) {
	query := "SELECT 1"
	if got := BindSQL(true, query); got != query {
		t.Fatalf("无占位符必须原样返回: %s", got)
	}
}
