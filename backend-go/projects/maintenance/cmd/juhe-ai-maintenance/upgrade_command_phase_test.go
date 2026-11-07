package main

import "testing"

// 分相旗标校验必须在触库前完成：非法 phase、错配的 --online /
// --old-writers-drained 一律 exit 2，不产生报告。
func TestRunUpgradeCommandRejectsInvalidPhaseFlags(t *testing.T) {
	cases := [][]string{
		{"--dsn", "postgres://example", "--phase", "switch"},
		{"--dsn", "postgres://example", "--online"},
		{"--dsn", "postgres://example", "--old-writers-drained"},
		{"--dsn", "postgres://example", "--phase", "contract", "--online"},
		{"--dsn", "postgres://example", "--phase", "expand", "--old-writers-drained"},
	}
	for _, argv := range cases {
		if code := runUpgradeCommand(argv); code != ExitUsageCode {
			t.Fatalf("argv=%v exit=%d, want %d", argv, code, ExitUsageCode)
		}
	}
}
