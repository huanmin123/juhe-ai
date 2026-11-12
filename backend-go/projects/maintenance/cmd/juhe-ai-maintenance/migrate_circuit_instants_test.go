package main

import "testing"

func TestMigrateCircuitInstantsCommandArgValidation(t *testing.T) {
	if code := runMigrateCircuitInstantsCommand(nil); code != 2 {
		t.Fatalf("缺少 --dsn 必须 exit 2，got %d", code)
	}
	if code := runMigrateCircuitInstantsCommand([]string{"--dsn", "sqlite:///tmp/x.db"}); code != 2 {
		t.Fatalf("非 postgres DSN 必须 exit 2，got %d", code)
	}
}
