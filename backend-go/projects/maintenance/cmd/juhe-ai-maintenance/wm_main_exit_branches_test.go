package main

import (
	"io"
	"os"
	"path/filepath"
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
// 继承自原子进程版本，逐分支保留。
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
	// 供互斥/证据分支使用的临时文件。
	root := t.TempDir()
	evidencePath := filepath.Join(root, "cutover.json")
	if err := os.WriteFile(evidencePath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	malformedPath := filepath.Join(root, "malformed.json")
	if err := os.WriteFile(malformedPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 解码成功但清单为空：inventory 门保持关闭（exit 3）。
	emptyFactsPath := filepath.Join(root, "empty-facts.json")
	if err := os.WriteFile(emptyFactsPath, []byte(`{"facts":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	completeEvidence := wmWriteCompleteCutoverEvidenceWithManifest(t)

	branch := func(name string, args []string, wantCode int, wantErr string) wmExitBranch {
		return wmExitBranch{name: name, args: args, wantCode: wantCode, wantErr: wantErr}
	}
	branches := []wmExitBranch{
		// runMaintenance 的互斥分支（全部返回 2）。
		branch("snapshot mutex", []string{"-postgres-schema-snapshot", "-ensure-schema"}, 2, "PostgreSQL schema snapshot flag is mutually exclusive"),
		branch("storage mutex", []string{"-ensure-schema", "-check-boundary"}, 2, "storage bootstrap flags are mutually exclusive"),
		branch("metrics check+apply", []string{"-check-go-runtime-metrics", "-apply-go-runtime-metrics"}, 2, "Go runtime metrics check and apply flags are mutually exclusive"),
		branch("metrics mutex with version", []string{"-check-go-runtime-metrics", "-version"}, 2, "Go runtime metrics flags are mutually exclusive"),
		// -verify-j3b-cutover-evidence 与 -j3b-backfill-evidence 均为带值
		// string flag（main.go 的 flag 定义与 usage），必须用 = 形式赋值；
		// 空格分隔会让第一个 flag 吞掉第二个 flag 名。
		branch("cutover evidence mutex", []string{"-verify-j3b-cutover-evidence=" + evidencePath, "-j3b-backfill-evidence=" + evidencePath}, 2, "J3b cutover evidence verification is mutually exclusive"),
		branch("inventory mutex", []string{"-verify-j3b-model-check-inventory", "-check-j3a-proxy-latency-postgres"}, 2, "J3b inventory verification flag is mutually exclusive"),
		branch("j3a check+apply", []string{"-check-j3a-proxy-latency-postgres", "-apply-j3a-proxy-latency-postgres"}, 2, "J3a PostgreSQL bootstrap flags are mutually exclusive"),
		branch("j3b check+apply", []string{"-check-j3b-model-check-postgres", "-apply-j3b-model-check-postgres"}, 2, "J3b PostgreSQL bootstrap flags are mutually exclusive"),
		branch("j3b sqlite check+apply", []string{"-check-j3b-model-check-sqlite", "-apply-j3b-model-check-sqlite"}, 2, "J3b SQLite bootstrap flags are mutually exclusive"),
		branch("j3b sqlite backfill+readback", []string{"-backfill-j3b-model-check-sqlite", "-verify-j3b-model-check-sqlite-backfill"}, 2, "J3b SQLite backfill and readback flags are mutually exclusive"),
		branch("j3b pg readback+backfill", []string{"-verify-j3b-model-check-postgres-backfill", "-backfill-j3b-model-check-postgres"}, 2, "J3b PostgreSQL backfill/readback flags are mutually exclusive"),
		branch("no command selected", nil, 2, "runtime is not switched yet"),
		// schema snapshot 预检链（env 门控逐级返回 2）。
		branch("snapshot missing target", []string{"-postgres-schema-snapshot"}, 2, "JUHE_AI_SCHEMA_SNAPSHOT_TARGET"),
		branch("snapshot missing url", []string{"-postgres-schema-snapshot"}, 2, "JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL"),
		branch("snapshot missing confirm", []string{"-postgres-schema-snapshot"}, 2, "READ_ONLY"),
		branch("snapshot sqlite url rejected", []string{"-postgres-schema-snapshot"}, 1, "仅支持 PostgreSQL"),
		// runner 用法错误。
		branch("metrics check without url", []string{"-check-go-runtime-metrics"}, 2, "requires --go-runtime-metrics-postgres-url"),
		branch("metrics apply without confirmations", []string{"-apply-go-runtime-metrics", "-go-runtime-metrics-postgres-url", "postgres://m@127.0.0.1:5432/db"}, 2, "--node-stopped --go-stopped --backup-confirmed"),
		branch("inventory without evidence", []string{"-verify-j3b-model-check-inventory"}, 2, "requires --j3b-inventory-evidence"),
		branch("inventory evidence missing file", []string{"-verify-j3b-model-check-inventory", "-j3b-inventory-evidence", filepath.Join(root, "missing.json")}, 2, "input failed"),
		// inventory 证据契约（LoadLegacyJ3bFactEvidence）：文件缺失或解码失败
		// 是 input error（exit 2）；解码成功但覆盖清单为空才进入未就绪门
		// （exit 3），因此这里用 emptyFactsPath 而非 malformedPath。
		branch("inventory evidence unready", []string{"-verify-j3b-model-check-inventory", "-j3b-inventory-evidence", emptyFactsPath}, 3, ""),
		branch("cutover evidence missing file", []string{"-verify-j3b-cutover-evidence", filepath.Join(root, "missing.json")}, 2, "input failed"),
		// cutover/backfill 证据共用 verifyJ3bEvidence：可读但解码失败会进入
		// 结构化报告（ready=false）并以 exit 3 fail-closed，只有打不开文件才
		// 是 "input failed"（exit 2）。
		branch("cutover evidence malformed", []string{"-verify-j3b-cutover-evidence", malformedPath}, 3, ""),
		branch("business schema check without path", []string{"-verify-business-sqlite-schema"}, 2, "requires --business-sqlite-path"),
		branch("business handoff without paths", []string{"-verify-business-sqlite-handoff"}, 2, "JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH"),
		branch("j3b sqlite bootstrap without env", []string{"-check-j3b-model-check-sqlite"}, 2, "JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH"),
		branch("j3b sqlite apply without confirmations", []string{"-apply-j3b-model-check-sqlite"}, 2, "--node-stopped --go-stopped --backup-confirmed"),
		branch("j3b sqlite backfill without confirmations", []string{"-backfill-j3b-model-check-sqlite", "-node-stopped", "-go-stopped", "-backup-confirmed"}, 2, "requires --j3b-backfill-evidence"),
		branch("j3b sqlite backfill without evidence", []string{"-backfill-j3b-model-check-sqlite", "-node-stopped", "-go-stopped", "-backup-confirmed", "-j3b-backfill-evidence", filepath.Join(root, "missing.json")}, 2, "input failed"),
		branch("j3b sqlite backfill evidence unready", []string{"-backfill-j3b-model-check-sqlite", "-node-stopped", "-go-stopped", "-backup-confirmed", "-j3b-backfill-evidence", malformedPath}, 3, ""),
		branch("j3b sqlite readback without env", []string{"-verify-j3b-model-check-sqlite-backfill"}, 2, "JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH"),
		branch("j3b pg readback without url", []string{"-verify-j3b-model-check-postgres-backfill"}, 2, "requires --j3b-postgres-readback-url"),
		branch("j3b pg backfill without url", []string{"-backfill-j3b-model-check-postgres"}, 2, "requires --j3b-postgres-backfill-url"),
		branch("j3b pg backfill without confirmations", []string{"-backfill-j3b-model-check-postgres", "-j3b-postgres-backfill-url", "postgres://r@127.0.0.1:5432/db"}, 2, "--node-stopped --go-stopped --backup-confirmed"),
		branch("j3b pg backfill without evidence", []string{"-backfill-j3b-model-check-postgres", "-j3b-postgres-backfill-url", "postgres://r@127.0.0.1:5432/db", "-node-stopped", "-go-stopped", "-backup-confirmed"}, 2, "requires --j3b-backfill-evidence"),
		branch("j3b pg bootstrap without env", []string{"-check-j3b-model-check-postgres"}, 2, "JUHE_AI_MAINTENANCE_J3B_POSTGRES_URL"),
		branch("j3a bootstrap without env", []string{"-apply-j3a-proxy-latency-postgres"}, 2, "JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL"),
		// 仓库状态相关的只读审计（当前状态 exit 3 / exit 0 都是真实分支）。
		branch("gateway route manifest gate", []string{"-verify-gateway-route-owner-manifest"}, 3, ""),
		branch("capability manifest gate", []string{"-verify-business-capability-manifest"}, 3, ""),
		branch("owner manifest verify", []string{"-verify-business-owner-manifest"}, 0, ""),
		branch("node active path scan", []string{"-scan-node-j3b-active-path"}, 0, ""),
		branch("j3c readonly boundary", []string{"-verify-j3c-readonly-boundary"}, 0, ""),
		// 运行时失败（SQLite 路径真实执行失败）。
		branch("j3b sqlite bootstrap on directory", []string{"-check-j3b-model-check-sqlite"}, 1, ""),
		branch("j3b sqlite readback on missing files", []string{"-verify-j3b-model-check-sqlite-backfill"}, 1, ""),
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
		case "j3b sqlite apply without confirmations":
			// SQLite bootstrap 的预检顺序是先 env 后确认 flag
			// （runJ3bModelCheckSQLiteBootstrap），必须提供非空 env 才能
			// 到达确认分支；路径在确认分支返回前不会被打开。
			branches[i].env = map[string]string{"JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH": filepath.Join(root, "apply-target.db")}
		case "j3b sqlite bootstrap on directory":
			branches[i].env = map[string]string{"JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH": root}
		case "j3b sqlite readback on missing files":
			branches[i].env = map[string]string{
				"JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH":         filepath.Join(root, "missing-target.db"),
				"JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH": filepath.Join(root, "missing-dataset.db"),
				"JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH":   filepath.Join(root, "missing-stats.db"),
			}
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

	// 成功路径的完整证据校验（exit 0，覆盖 runJ3bCutoverEvidenceCheck 成功
	// 与 backfill 证据门在完整清单下放行的出口）。
	t.Run("complete evidence accepted", func(t *testing.T) {
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_BACKFILL_EVIDENCE", "")
		code, stdout, stderr := wmRunMaintenanceCapture(t, "-verify-j3b-cutover-evidence", completeEvidence)
		if code != 0 {
			t.Fatalf("完整证据应 exit 0: %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, `"ready":true`) {
			t.Fatalf("输出应包含就绪报告:\n%s", stdout)
		}
	})
}
