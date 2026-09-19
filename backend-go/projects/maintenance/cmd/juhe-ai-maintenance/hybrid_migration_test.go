package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/routestrategymigration"
)

// 本文件覆盖 --migrate-hybrid-smart-strategies 的进程内结果函数与 main() 派发
// 分支：os.Exit 出口已由 runMaintenance 收敛为返回码，派发分支经
// wmRunMaintenanceCapture 进程内直调断言（2026-09-19 fork 炸弹事故后按
// “测试单进程纪律”由子进程重执行范式改写，见 docs/develop/后端测试分层规则.md）。

func TestHybridSmartMigrationResultUsageErrors(t *testing.T) {
	if code := hybridSmartStrategyMigrationResult("", false); code != 2 {
		t.Fatalf("missing --dsn exit code = %d, want 2", code)
	}
	if code := hybridSmartStrategyMigrationResult("   ", false); code != 2 {
		t.Fatalf("blank --dsn exit code = %d, want 2", code)
	}
	// Open 拒绝非 PostgreSQL URL：SQLite 文件路径绝不能被误当作目标。
	if code := hybridSmartStrategyMigrationResult("sqlite://business.sqlite3", true); code != 2 {
		t.Fatalf("non-postgres dsn exit code = %d, want 2", code)
	}
}

func TestHybridSmartMigrationOutcomeExitCode(t *testing.T) {
	if code := hybridSmartMigrationOutcomeExitCode(routestrategymigration.Report{}, errors.New("boom")); code != 1 {
		t.Fatalf("runtime failure exit code = %d, want 1", code)
	}
	ready := routestrategymigration.Report{Confirm: true, RemainingHybridSmart: 0}
	if code := hybridSmartMigrationOutcomeExitCode(ready, nil); code != 0 {
		t.Fatalf("ready report exit code = %d, want 0", code)
	}
	notReady := routestrategymigration.Report{Confirm: true, RemainingHybridSmart: 2}
	if code := hybridSmartMigrationOutcomeExitCode(notReady, nil); code != 3 {
		t.Fatalf("not-ready report exit code = %d, want 3", code)
	}
}

func TestHybridSmartMigrationMainDispatch(t *testing.T) {
	branches := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{
			name:     "mutex with storage bootstrap",
			args:     []string{"-migrate-hybrid-smart-strategies", "-ensure-schema"},
			wantCode: 2,
			wantErr:  "hybrid_smart strategy migration flag is mutually exclusive with other maintenance commands",
		},
		{
			name:     "mutex with version",
			args:     []string{"-migrate-hybrid-smart-strategies", "-version"},
			wantCode: 2,
			wantErr:  "hybrid_smart strategy migration flag is mutually exclusive with other maintenance commands",
		},
		{
			name:     "missing dsn",
			args:     []string{"-migrate-hybrid-smart-strategies"},
			wantCode: 2,
			wantErr:  "requires --dsn with an explicit maintenance-scoped PostgreSQL URL",
		},
		{
			name:     "sqlite dsn rejected",
			args:     []string{"-migrate-hybrid-smart-strategies", "-confirm", "-dsn", "sqlite://business.sqlite3"},
			wantCode: 2,
			wantErr:  "open hybrid_smart strategy migration connection",
		},
	}
	for _, tc := range branches {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := wmRunMaintenanceCapture(t, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr: %s)", code, tc.wantCode, stderr)
			}
			if !strings.Contains(stderr, tc.wantErr) {
				t.Fatalf("stderr %q does not contain %q", stderr, tc.wantErr)
			}
		})
	}
}
