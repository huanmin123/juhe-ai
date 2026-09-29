package main

import (
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

// 本文件在测试进程内直调 runMaintenance 覆盖其全部出口分支。原版本以“子进程
// 重执行测试二进制”覆盖这些分支；2026-09-19 该哨兵机制一次失配即自复制 658 个
// 进程（fork 炸弹事故），按“测试单进程纪律”（docs/develop/后端测试分层规则.md
// “测试进程纪律（硬性）”章节，守卫脚本 scripts/test-process-isolation-regression.mjs）
// 改写为进程内直调：一次 go test 只允许一个测试进程，测试代码禁止孵化任何子进程。
// runMaintenance 已把 main() 的全部 os.Exit 出口收敛为返回码，分支表格与断言语义
// 继承自原子进程版本，逐分支保留。Node→Go 迁移期命令族退役后，分支表格同步
// 收敛为现行命令的互斥、预检与出口分支（J3a/J3b bootstrap apply 命令为现行
// 生产初始化契约，保留互斥与无 env 拒绝裸跑分支）。
//
// 输出捕获采用最小侵入方案：不改动生产代码的输出面，测试期间进程内替换
// os.Stdout/os.Stderr（runMaintenance 与各 runner 的 fmt/json Encoder 均在调用点
// 读取这两个变量，替换后输出进入管道），调用结束即恢复。

// wmRunMaintenanceCapture 进程内直调 runMaintenance(args)，返回退出码与期间
// 写入的 stdout、stderr。仅供本包测试使用；os.Stdout/os.Stderr 是进程级全局，
// 调用方必须串行使用（本包测试不使用 t.Parallel()）。
func wmRunMaintenanceCapture(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	savedStdout, savedStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutW, stderrW
	stdoutBuf, stderrBuf := &strings.Builder{}, &strings.Builder{}
	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(stdoutBuf, stdoutR)
	}()
	go func() {
		defer copyWG.Done()
		_, _ = io.Copy(stderrBuf, stderrR)
	}()
	code := runMaintenance(args)
	os.Stdout, os.Stderr = savedStdout, savedStderr
	_ = stdoutW.Close()
	_ = stderrW.Close()
	copyWG.Wait()
	_ = stdoutR.Close()
	_ = stderrR.Close()
	return code, stdoutBuf.String(), stderrBuf.String()
}

type wmExitBranch struct {
	name     string
	args     []string
	env      map[string]string
	wantCode int
	wantErr  string // 期望出现在 stderr 的片段；空表示不检查
}

func TestWMMainExitBranches(t *testing.T) {
	branch := func(name string, args []string, wantCode int, wantErr string) wmExitBranch {
		return wmExitBranch{name: name, args: args, wantCode: wantCode, wantErr: wantErr}
	}
	branches := []wmExitBranch{
		// runMaintenance 的互斥分支（全部返回 2）。
		branch("snapshot mutex", []string{"-postgres-schema-snapshot", "-ensure-schema"}, 2, "PostgreSQL schema snapshot flag is mutually exclusive"),
		branch("storage mutex", []string{"-ensure-schema", "-check-boundary"}, 2, "storage bootstrap flags are mutually exclusive"),
		branch("metrics check+apply", []string{"-check-go-runtime-metrics", "-apply-go-runtime-metrics"}, 2, "Go runtime metrics check and apply flags are mutually exclusive"),
		branch("metrics mutex with version", []string{"-check-go-runtime-metrics", "-version"}, 2, "Go runtime metrics flags are mutually exclusive"),
		branch("no command selected", nil, 2, "runtime is not switched yet"),
		// schema snapshot 预检链（env 门控逐级返回 2）。
		branch("snapshot missing target", []string{"-postgres-schema-snapshot"}, 2, "JUHE_AI_SCHEMA_SNAPSHOT_TARGET"),
		branch("snapshot missing url", []string{"-postgres-schema-snapshot"}, 2, "JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL"),
		branch("snapshot missing confirm", []string{"-postgres-schema-snapshot"}, 2, "READ_ONLY"),
		branch("snapshot sqlite url rejected", []string{"-postgres-schema-snapshot"}, 1, "仅支持 PostgreSQL"),
		// runner 用法错误。
		branch("metrics check without url", []string{"-check-go-runtime-metrics"}, 2, "requires --go-runtime-metrics-postgres-url"),
		branch("metrics apply without confirmations", []string{"-apply-go-runtime-metrics", "-go-runtime-metrics-postgres-url", "postgres://m@127.0.0.1:5432/db"}, 2, "--node-stopped --go-stopped --backup-confirmed"),
		// J3a/J3b bootstrap apply 命令（现行生产初始化契约）：与其他命令
		// 互斥、无 env 拒绝裸跑（对照基线 j3a/j3b bootstrap without env 分支）。
		branch("j3 apply mutex with storage bootstrap", []string{"-apply-j3b-model-check-sqlite", "-ensure-schema"}, 2, "storage bootstrap flags are mutually exclusive"),
		branch("j3a apply without env", []string{"-apply-j3a-proxy-latency-postgres"}, 2, "JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL"),
		branch("j3b pg apply without env", []string{"-apply-j3b-model-check-postgres"}, 2, "JUHE_AI_MAINTENANCE_J3B_POSTGRES_URL"),
		branch("j3b sqlite apply without env", []string{"-apply-j3b-model-check-sqlite"}, 2, "JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH"),
	}
	// env 门控分支：原 exec 子进程版本以 cmd.Env 注入；进程内改用 t.Setenv
	// （设置并自动恢复），语义与子进程“继承环境 + 覆盖目标变量”一致。
	for i := range branches {
		switch branches[i].name {
		case "snapshot missing target":
			branches[i].env = map[string]string{"JUHE_AI_SCHEMA_SNAPSHOT_TARGET": ""}
		case "snapshot missing url":
			branches[i].env = map[string]string{"JUHE_AI_SCHEMA_SNAPSHOT_TARGET": "test", "JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL": ""}
		case "snapshot missing confirm":
			branches[i].env = map[string]string{"JUHE_AI_SCHEMA_SNAPSHOT_TARGET": "test", "JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL": "postgres://s@127.0.0.1:5432/db", "JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM": ""}
		case "snapshot sqlite url rejected":
			branches[i].env = map[string]string{"JUHE_AI_SCHEMA_SNAPSHOT_TARGET": "test", "JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL": "sqlite:///x.db", "JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM": "READ_ONLY"}
		case "j3a apply without env":
			branches[i].env = map[string]string{"JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL": ""}
		case "j3b pg apply without env":
			branches[i].env = map[string]string{"JUHE_AI_MAINTENANCE_J3B_POSTGRES_URL": ""}
		case "j3b sqlite apply without env":
			branches[i].env = map[string]string{"JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH": ""}
		}
	}

	for _, item := range branches {
		item := item
		t.Run(item.name, func(t *testing.T) {
			for key, value := range item.env {
				t.Setenv(key, value)
			}
			code, _, stderr := wmRunMaintenanceCapture(t, item.args...)
			if code != item.wantCode {
				t.Fatalf("exit=%d want %d\n%s", code, item.wantCode, stderr)
			}
			if item.wantErr != "" && !strings.Contains(stderr, item.wantErr) {
				t.Fatalf("输出缺少 %q:\n%s", item.wantErr, stderr)
			}
		})
	}
}
