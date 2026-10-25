// studio 退场命令 CLI 门禁测试（进程内直调 runMaintenance，测试进程纪律：
// main 是唯一 os.Exit 站点）：用法门禁：缺三确认任一 / 全缺 / 与其他命令互斥 /
// dsn 缺失，退出码 2。端到端清理行为由 internal/studioretirement 的 opt-in
// PG live smoke（JUHE_AI_STUDIO_RETIREMENT_PG_SMOKE_URL）承载；SQLite 端到端
// 臂随 SQLite 存储形态退役删除（PLAN-20261024T051925945Z B4）。

package main

import (
	"testing"
)

func TestStudioRetirementUsageGates(t *testing.T) {
	dsn := "postgres://juhe-ai-test.invalid/testdb"
	withGates := []string{"--apply-studio-retirement", "--dsn", dsn}
	cases := []struct {
		name string
		argv []string
	}{
		{"缺 node-stopped", append(append([]string{}, withGates...), "--go-stopped", "--backup-confirmed")},
		{"缺 go-stopped", append(append([]string{}, withGates...), "--node-stopped", "--backup-confirmed")},
		{"缺 backup-confirmed", append(append([]string{}, withGates...), "--node-stopped", "--go-stopped")},
		{"全缺", withGates},
		{"dsn 缺失", []string{"--apply-studio-retirement", "--node-stopped", "--go-stopped", "--backup-confirmed"}},
		{"与其他命令互斥", append(append([]string{}, withGates...), "--node-stopped", "--go-stopped", "--backup-confirmed", "--ensure-schema")},
		{"与 J3a 互斥", append(append([]string{}, withGates...), "--node-stopped", "--go-stopped", "--backup-confirmed", "--apply-j3a-proxy-latency-postgres")},
		{"与 J3b PG 互斥", append(append([]string{}, withGates...), "--node-stopped", "--go-stopped", "--backup-confirmed", "--apply-j3b-model-check-postgres")},
		{"与 J2 互斥", append(append([]string{}, withGates...), "--node-stopped", "--go-stopped", "--backup-confirmed", "--apply-account-balance-postgres")},
		{"version 互斥", append(append([]string{}, withGates...), "--version")},
		{"check 互斥", append(append([]string{}, withGates...), "--check-boundary")},
		{"dry-run 互斥", append(append([]string{}, withGates...), "--dry-run")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if code := runMaintenance(testCase.argv); code != 2 {
				t.Fatalf("runMaintenance(%v) = %d, want 2", testCase.argv, code)
			}
		})
	}
}
