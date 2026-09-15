package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// wmInstallChildFlags 在子进程内替换 flag.CommandLine 与 os.Args，让 main()
// 在干净的 flag 集上解析指定参数（避免 -test.* 残留与多次 Parse 泄漏）。
func wmInstallChildFlags(args []string) func() {
	savedCommandLine := flag.CommandLine
	savedArgs := os.Args
	flag.CommandLine = flag.NewFlagSet("wm-child", flag.ExitOnError)
	os.Args = append([]string{"juhe-ai-maintenance"}, args...)
	return func() {
		flag.CommandLine = savedCommandLine
		os.Args = savedArgs
	}
}

// 本文件以“子进程重执行测试二进制”的方式覆盖 main() 与各 runner 的 os.Exit
// 出口分支：这些分支在进程内直调会终止测试进程，因此只能在子进程中执行。
// 覆盖率集成：父进程在 GOCOVERDIR 环境下运行时把它传给子进程，子进程
// os.Exit 时由 runtime 覆盖钩子落盘 covdata，最终可用 `go tool covdata
// textfmt` 与各包计数合并（Go 1.20+ 二进制覆盖机制）。
// 各互斥/预检分支的退出码与错误文本同时是行为断言，非单纯刷覆盖率。

type wmExitBranch struct {
	name     string
	args     string // \x1f 分隔的 CLI 参数
	wantCode int
	wantErr  string // 期望出现在 stderr 的片段；空表示不检查
}

func TestWMMainExitBranches(t *testing.T) {
	const childEnv = "WM_MAIN_CHILD_ARGS"
	const childSentinel = "WM_MAIN_CHILD=1"
	if os.Getenv("WM_MAIN_CHILD") == "1" {
		// 子进程：换上干净的 flag 集与 os.Args 后调用 main()。哨兵与参数
		// 分离，使“无参数”分支不会与父进程混淆而递归孵化。
		restore := wmInstallChildFlags(strings.Split(os.Getenv(childEnv), "\x1f"))
		defer restore()
		main()
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	coverDir := os.Getenv("GOCOVERDIR")

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

	branch := func(name, args string, wantCode int, wantErr string) wmExitBranch {
		return wmExitBranch{name: name, args: args, wantCode: wantCode, wantErr: wantErr}
	}
	branches := []wmExitBranch{
		// main() 的互斥分支（全部 exit 2）。
		branch("snapshot mutex", "-postgres-schema-snapshot\x1f-ensure-schema", 2, "PostgreSQL schema snapshot flag is mutually exclusive"),
		branch("storage mutex", "-ensure-schema\x1f-check-boundary", 2, "storage bootstrap flags are mutually exclusive"),
		branch("metrics check+apply", "-check-go-runtime-metrics\x1f-apply-go-runtime-metrics", 2, "Go runtime metrics check and apply flags are mutually exclusive"),
		branch("metrics mutex with version", "-check-go-runtime-metrics\x1f-version", 2, "Go runtime metrics flags are mutually exclusive"),
		// -verify-j3b-cutover-evidence 与 -j3b-backfill-evidence 均为带值
		// string flag（main.go 的 flag 定义与 usage），必须用 = 形式赋值；
		// 空格分隔会让第一个 flag 吞掉第二个 flag 名。
		branch("cutover evidence mutex", "-verify-j3b-cutover-evidence="+evidencePath+"\x1f-j3b-backfill-evidence="+evidencePath, 2, "J3b cutover evidence verification is mutually exclusive"),
		branch("inventory mutex", "-verify-j3b-model-check-inventory\x1f-check-j3a-proxy-latency-postgres", 2, "J3b inventory verification flag is mutually exclusive"),
		branch("j3a check+apply", "-check-j3a-proxy-latency-postgres\x1f-apply-j3a-proxy-latency-postgres", 2, "J3a PostgreSQL bootstrap flags are mutually exclusive"),
		branch("j3b check+apply", "-check-j3b-model-check-postgres\x1f-apply-j3b-model-check-postgres", 2, "J3b PostgreSQL bootstrap flags are mutually exclusive"),
		branch("j3b sqlite check+apply", "-check-j3b-model-check-sqlite\x1f-apply-j3b-model-check-sqlite", 2, "J3b SQLite bootstrap flags are mutually exclusive"),
		branch("j3b sqlite backfill+readback", "-backfill-j3b-model-check-sqlite\x1f-verify-j3b-model-check-sqlite-backfill", 2, "J3b SQLite backfill and readback flags are mutually exclusive"),
		branch("j3b pg readback+backfill", "-verify-j3b-model-check-postgres-backfill\x1f-backfill-j3b-model-check-postgres", 2, "J3b PostgreSQL backfill/readback flags are mutually exclusive"),
		branch("no command selected", "", 2, "runtime is not switched yet"),
		// schema snapshot 预检链（env 门控逐级 exit）。
		branch("snapshot missing target", "-postgres-schema-snapshot", 2, "JUHE_AI_SCHEMA_SNAPSHOT_TARGET"),
		branch("snapshot missing url", "-postgres-schema-snapshot", 2, "JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL"),
		branch("snapshot missing confirm", "-postgres-schema-snapshot", 2, "READ_ONLY"),
		branch("snapshot sqlite url rejected", "-postgres-schema-snapshot", 1, "仅支持 PostgreSQL"),
		// runner 用法错误。
		branch("metrics check without url", "-check-go-runtime-metrics", 2, "requires --go-runtime-metrics-postgres-url"),
		branch("metrics apply without confirmations", "-apply-go-runtime-metrics\x1f-go-runtime-metrics-postgres-url\x1fpostgres://m@127.0.0.1:5432/db", 2, "--node-stopped --go-stopped --backup-confirmed"),
		branch("inventory without evidence", "-verify-j3b-model-check-inventory", 2, "requires --j3b-inventory-evidence"),
		branch("inventory evidence missing file", "-verify-j3b-model-check-inventory\x1f-j3b-inventory-evidence\x1f"+filepath.Join(root, "missing.json"), 2, "input failed"),
		// inventory 证据契约（LoadLegacyJ3bFactEvidence）：文件缺失或解码失败
		// 是 input error（exit 2）；解码成功但覆盖清单为空才进入未就绪门
		// （exit 3），因此这里用 emptyFactsPath 而非 malformedPath。
		branch("inventory evidence unready", "-verify-j3b-model-check-inventory\x1f-j3b-inventory-evidence\x1f"+emptyFactsPath, 3, ""),
		branch("cutover evidence missing file", "-verify-j3b-cutover-evidence\x1f"+filepath.Join(root, "missing.json"), 2, "input failed"),
		// cutover/backfill 证据共用 verifyJ3bEvidence：可读但解码失败会进入
		// 结构化报告（ready=false）并以 exit 3 fail-closed，只有打不开文件才
		// 是 "input failed"（exit 2）。
		branch("cutover evidence malformed", "-verify-j3b-cutover-evidence\x1f"+malformedPath, 3, ""),
		branch("business schema check without path", "-verify-business-sqlite-schema", 2, "requires --business-sqlite-path"),
		branch("business handoff without paths", "-verify-business-sqlite-handoff", 2, "JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH"),
		branch("j3b sqlite bootstrap without env", "-check-j3b-model-check-sqlite", 2, "JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH"),
		branch("j3b sqlite apply without confirmations", "-apply-j3b-model-check-sqlite", 2, "--node-stopped --go-stopped --backup-confirmed"),
		branch("j3b sqlite backfill without confirmations", "-backfill-j3b-model-check-sqlite\x1f-node-stopped\x1f-go-stopped\x1f-backup-confirmed", 2, "requires --j3b-backfill-evidence"),
		branch("j3b sqlite backfill without evidence", "-backfill-j3b-model-check-sqlite\x1f-node-stopped\x1f-go-stopped\x1f-backup-confirmed\x1f-j3b-backfill-evidence\x1f"+filepath.Join(root, "missing.json"), 2, "input failed"),
		branch("j3b sqlite backfill evidence unready", "-backfill-j3b-model-check-sqlite\x1f-node-stopped\x1f-go-stopped\x1f-backup-confirmed\x1f-j3b-backfill-evidence\x1f"+malformedPath, 3, ""),
		branch("j3b sqlite readback without env", "-verify-j3b-model-check-sqlite-backfill", 2, "JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH"),
		branch("j3b pg readback without url", "-verify-j3b-model-check-postgres-backfill", 2, "requires --j3b-postgres-readback-url"),
		branch("j3b pg backfill without url", "-backfill-j3b-model-check-postgres", 2, "requires --j3b-postgres-backfill-url"),
		branch("j3b pg backfill without confirmations", "-backfill-j3b-model-check-postgres\x1f-j3b-postgres-backfill-url\x1fpostgres://r@127.0.0.1:5432/db", 2, "--node-stopped --go-stopped --backup-confirmed"),
		branch("j3b pg backfill without evidence", "-backfill-j3b-model-check-postgres\x1f-j3b-postgres-backfill-url\x1fpostgres://r@127.0.0.1:5432/db\x1f-node-stopped\x1f-go-stopped\x1f-backup-confirmed", 2, "requires --j3b-backfill-evidence"),
		branch("j3b pg bootstrap without env", "-check-j3b-model-check-postgres", 2, "JUHE_AI_MAINTENANCE_J3B_POSTGRES_URL"),
		branch("j3a bootstrap without env", "-apply-j3a-proxy-latency-postgres", 2, "JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL"),
		// 仓库状态相关的只读审计（当前状态 exit 3 / exit 0 都是真实分支）。
		branch("gateway route manifest gate", "-verify-gateway-route-owner-manifest", 3, ""),
		branch("capability manifest gate", "-verify-business-capability-manifest", 3, ""),
		branch("owner manifest verify", "-verify-business-owner-manifest", 0, ""),
		branch("node active path scan", "-scan-node-j3b-active-path", 0, ""),
		branch("j3c readonly boundary", "-verify-j3c-readonly-boundary", 0, ""),
		// 运行时失败（SQLite 路径真实执行失败）。
		branch("j3b sqlite bootstrap on directory", "-check-j3b-model-check-sqlite", 1, ""),
		branch("j3b sqlite readback on missing files", "-verify-j3b-model-check-sqlite-backfill", 1, ""),
	}

	for _, item := range branches {
		item := item
		t.Run(item.name, func(t *testing.T) {
			cmd := exec.Command(self, "-test.run=TestWMMainExitBranches", "-test.v=false")
			cmd.Env = append(os.Environ(), childSentinel, childEnv+"="+item.args)
			if coverDir != "" {
				cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverDir)
			}
			switch item.name {
			case "snapshot missing target":
				cmd.Env = append(cmd.Env, "JUHE_AI_SCHEMA_SNAPSHOT_TARGET=")
			case "snapshot missing url":
				cmd.Env = append(cmd.Env, "JUHE_AI_SCHEMA_SNAPSHOT_TARGET=test", "JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL=")
			case "snapshot missing confirm":
				cmd.Env = append(cmd.Env, "JUHE_AI_SCHEMA_SNAPSHOT_TARGET=test", "JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL=postgres://s@127.0.0.1:5432/db", "JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM=")
			case "snapshot sqlite url rejected":
				cmd.Env = append(cmd.Env, "JUHE_AI_SCHEMA_SNAPSHOT_TARGET=test", "JUHE_AI_SCHEMA_SNAPSHOT_POSTGRES_URL=sqlite:///x.db", "JUHE_AI_SCHEMA_SNAPSHOT_READ_ONLY_CONFIRM=READ_ONLY")
			case "j3b sqlite apply without confirmations":
				// SQLite bootstrap 的预检顺序是先 env 后确认 flag
				// （runJ3bModelCheckSQLiteBootstrap），必须提供非空 env 才能
				// 到达确认分支；路径在确认分支返回前不会被打开。
				cmd.Env = append(cmd.Env, "JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH="+filepath.Join(root, "apply-target.db"))
			case "j3b sqlite bootstrap on directory":
				cmd.Env = append(cmd.Env, "JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH="+root)
			case "j3b sqlite readback on missing files":
				cmd.Env = append(cmd.Env,
					"JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH="+filepath.Join(root, "missing-target.db"),
					"JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH="+filepath.Join(root, "missing-dataset.db"),
					"JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH="+filepath.Join(root, "missing-stats.db"))
			}
			output, err := cmd.CombinedOutput()
			code := 0
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatalf("运行子进程失败: %v\n%s", err, output)
			}
			if code != item.wantCode {
				t.Fatalf("exit=%d want %d\n%s", code, item.wantCode, output)
			}
			if item.wantErr != "" && !strings.Contains(string(output), item.wantErr) {
				t.Fatalf("输出缺少 %q:\n%s", item.wantErr, output)
			}
		})
	}

	// 成功路径的完整证据校验（exit 0，覆盖 runJ3bCutoverEvidenceCheck 成功
	// 与 backfill 证据门在完整清单下放行的出口）。
	t.Run("complete evidence accepted", func(t *testing.T) {
		cmd := exec.Command(self, "-test.run=TestWMMainExitBranches", "-test.v=false")
		// 与上方表格 case 一致必须携带 childSentinel：缺它子进程走父分支
		// 再次孵化自身，形成无限递归，CombinedOutput 永不返回。
		cmd.Env = append(os.Environ(), childSentinel,
			childEnv+"=-verify-j3b-cutover-evidence\x1f"+completeEvidence,
			"JUHE_AI_MAINTENANCE_J3B_BACKFILL_EVIDENCE=")
		if coverDir != "" {
			cmd.Env = append(cmd.Env, "GOCOVERDIR="+coverDir)
		}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("完整证据应 exit 0: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), `"ready":true`) {
			t.Fatalf("输出应包含就绪报告:\n%s", output)
		}
	})
}
