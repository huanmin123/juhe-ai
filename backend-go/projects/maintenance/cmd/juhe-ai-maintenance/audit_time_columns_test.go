package main

import "testing"

func TestAuditTimeColumnsRequiresPostgresDSN(t *testing.T) {
	if code := runMaintenance([]string{"audit-time-columns"}); code != 2 {
		t.Fatalf("缺 --dsn 应 exit 2，实际 %d", code)
	}
	if code := runMaintenance([]string{"audit-time-columns", "--dsn", "sqlite:///tmp/x.db"}); code != 2 {
		t.Fatalf("非 PostgreSQL DSN 应 exit 2，实际 %d", code)
	}
}

func TestSplitSchemaList(t *testing.T) {
	got := splitSchemaList(" juhe_business, juhe_usage ,, juhe_business ")
	if len(got) != 2 || got[0] != "juhe_business" || got[1] != "juhe_usage" {
		t.Fatalf("splitSchemaList = %#v", got)
	}
	if len(splitSchemaList("")) != 0 {
		t.Fatal("空 schemas 应返回空切片")
	}
}
