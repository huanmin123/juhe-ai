package main

import "testing"

func TestMigrateUsageRecordsCommandArgValidation(t *testing.T) {
	if code := runMigrateUsageRecordsCommand(nil); code != 2 {
		t.Fatalf("缺少 --dsn 必须 exit 2，got %d", code)
	}
	if code := runMigrateUsageRecordsCommand([]string{"--dsn", "sqlite:///tmp/x.db"}); code != 2 {
		t.Fatalf("非 postgres DSN 必须 exit 2，got %d", code)
	}
}
