// 波次 w16h：run() 抽取后的进程内臂覆盖。main() 收敛为 os.Exit(run(...)) 后，
// 原 fail() 出口改为返回码，boot/停机序列在测试进程内可直接驱动：错误臂用
// 坏 env/垃圾 SQLite 文件/非法监听地址注入，优雅停机臂用 hooksSignalNotifyContext
// 注入可编程 ctx（等价 SIGTERM，无需真实信号）。stderr 错误文案断言锁定
// 原 fail() 逐字节行为。
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"
)

// w16hApplyEnv 把 env 逐键 t.Setenv（测试结束自动恢复）。
func w16hApplyEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for key, value := range env {
		t.Setenv(key, value)
	}
}

// w16hInjectCancelSignal 覆写 hooksSignalNotifyContext 为可编程取消 ctx：
// run/runPassiveJobs/runRuntimeLegacyMigration 的停机信号源在测试内可控。
func w16hInjectCancelSignal(t *testing.T) context.CancelFunc {
	t.Helper()
	original := hooksSignalNotifyContext
	ctx, cancel := context.WithCancel(context.Background())
	hooksSignalNotifyContext = func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
		return ctx, cancel
	}
	t.Cleanup(func() {
		hooksSignalNotifyContext = original
		cancel()
	})
	return cancel
}

// w16hSeedSQLite 物理建一个空 SQLite 文件（sql.Open 惰性连接，Ping 落盘）。
func w16hSeedSQLite(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// w16hBaseEnv 构造 F1+F2+J1+worker 恒开装配成功的 owner env 基底（J1 与
// worker 家族恒开后的必填项由基底统一提供，错误臂按需单项覆盖），用于驱动
// J1/mR/J2/J3a/J3b/listener/goMetrics/worker 各加载臂与 listener 臂。
//
// 根目录用自管理目录 + 尽力清理（不使用 t.TempDir）：恒开语义下 J1 store 与
// worker 各库会在多数错误臂返回前打开，而 run() 的组件错误出口不回收它们
// （生产以 os.Exit 收口，进程内测试无此保证）；句柄占用会让 t.TempDir 的
// 严格清理在 Windows 上失败。对齐 model-recovery 守卫臂的既有先例。
func w16hBaseEnv(t *testing.T) map[string]string {
	t.Helper()
	root, err := os.MkdirTemp("", "w16h-base-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	logsDirectory := filepath.Join(root, "logs")
	if err := os.MkdirAll(logsDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	j1Inputs := filepath.Join(root, "j1-inputs")
	if err := os.MkdirAll(j1Inputs, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"business.sqlite3", "dataset.sqlite3", "stats.sqlite3", "usage-catalog.sqlite3"} {
		w16hSeedSQLite(t, filepath.Join(root, name))
	}
	return map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":        "w16h-run",
		"JUHE_AI_RUNTIME_LOG_STORE":              "sqlite",
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":      filepath.Join(root, "runtime-log.sqlite3"),
		"JUHE_AI_LOG_DIR":                        logsDirectory,
		"JUHE_AI_TABLE_MONITOR_INSTANCE_ID":      "w16h-run",
		"JUHE_AI_TABLE_MONITOR_STORE":            "sqlite",
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":    filepath.Join(root, "table-monitor.sqlite3"),
		"JUHE_AI_DATABASE_PATH":                  filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_DATASET_DATABASE_PATH":          filepath.Join(root, "dataset.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH":            filepath.Join(root, "stats.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":    filepath.Join(root, "usage-catalog.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": filepath.Join(root, "codex-state"),
		"JUHE_AI_CHAT_ASSETS_ROOT":               filepath.Join(root, "chat-assets"),
		// worker 恒装配：家族门禁所需的存储路径与密钥。
		"JUHE_AI_TASK_RUNS_DATABASE_PATH": filepath.Join(root, "task-runs.sqlite3"),
		"JUHE_AI_USAGE_SHARD_ROOT":        filepath.Join(root, "usage-shards"),
		"JUHE_AI_CHAT_DATABASE_PATH":      filepath.Join(root, "chat.sqlite3"),
		"JUHE_AI_SECRET":                  "0123456789abcdef0123456789abcdef",
		// J1 恒装配：LoadConfig 必填项（错误臂按需覆盖单项）。
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "w16h-j1",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     filepath.Join(root, "account-health.sqlite3"),
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   j1Inputs,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "0123456789abcdef0123456789abcdef",
	}
}

// w16hRunArms 统一断言：返回码 + stderr 关键片段（锁定原 fail 文案）。
// 注入可编程停机 ctx：若被测臂意外走到 supervisor 循环，立即收敛为 rc=0
// 断言失败可见，而不是挂住测试进程。
func w16hRunArms(t *testing.T, args []string, wantCode int, stderrContains string) (string, string) {
	t.Helper()
	w16hInjectCancelSignal(t)
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	if code != wantCode {
		t.Fatalf("run(%v) 退出码必须为 %d，得到 %d；stderr=%q", args, wantCode, code, stderr.String())
	}
	if stderrContains != "" && !bytes.Contains([]byte(stderr.String()), []byte(stderrContains)) {
		t.Fatalf("stderr 必须包含 %q，得到 %q", stderrContains, stderr.String())
	}
	return stdout.String(), stderr.String()
}

// TestW16HRunFlagArms 覆盖 run() 头部 flag 臂：未知 flag（rc=2）、-h（rc=0）、
// 位置参数（rc=2）、--once 与迁移互斥（rc=2）。FlagSet 的错误与 usage 输出
// 目标是全局 os.Stderr（与原全局 flag.Parse 行为逐字节一致），用 pipe 捕获。
func TestW16HRunFlagArms(t *testing.T) {
	captureOSStderr := func(t *testing.T, invoke func()) string {
		t.Helper()
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		original := os.Stderr
		os.Stderr = writer
		invoke()
		os.Stderr = original
		_ = writer.Close()
		output := make([]byte, 4096)
		read, _ := reader.Read(output)
		_ = reader.Close()
		return string(output[:read])
	}
	t.Run("未知 flag 返回 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"-w16h-no-such-flag"}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("未知 flag 必须返回 2，得到 %d", code)
		}
	})
	t.Run("未知 flag 错误行进全局 stderr", func(t *testing.T) {
		dumped := captureOSStderr(t, func() {
			var stdout, stderr bytes.Buffer
			_ = run([]string{"-w16h-no-such-flag"}, &stdout, &stderr)
		})
		if !bytes.Contains([]byte(dumped), []byte("flag provided but not defined: -w16h-no-such-flag")) {
			t.Fatalf("全局 stderr 必须包含 flag 错误行，得到 %q", dumped)
		}
	})
	t.Run("-h 返回 0 并输出 usage", func(t *testing.T) {
		dumped := captureOSStderr(t, func() {
			var stdout, stderr bytes.Buffer
			if code := run([]string{"-h"}, &stdout, &stderr); code != 0 {
				t.Errorf("-h 必须返回 0，得到 %d", code)
			}
		})
		if !bytes.Contains([]byte(dumped), []byte("Usage of")) {
			t.Fatalf("-h 必须输出 usage 到全局 stderr，得到 %q", dumped)
		}
	})
	t.Run("位置参数返回 2", func(t *testing.T) {
		_, stderr := w16hRunArms(t, []string{"w16h-extra-arg"}, 2, "unsupported jobs arguments")
		if want := "unsupported jobs arguments: [w16h-extra-arg]"; stderr != want+"\n" {
			t.Fatalf("stderr 逐字节断言失败：want %q got %q", want+"\n", stderr)
		}
	})
	t.Run("once 与迁移互斥返回 2", func(t *testing.T) {
		w16hRunArms(t, []string{"-once", "-migrate-runtime-log-legacy-sqlite"}, 2,
			"--once and --migrate-runtime-log-legacy-sqlite are mutually exclusive")
	})
}

// TestW16HRunStartupFailArms 覆盖 logger/owner/F1 配置链错误臂。
func TestW16HRunStartupFailArms(t *testing.T) {
	t.Run("非法日志级别返回 1", func(t *testing.T) {
		w16hApplyEnv(t, map[string]string{"JUHE_AI_LOG_LEVEL": "w16h-not-a-level"})
		w16hRunArms(t, nil, 1, "JUHE_AI_LOG_LEVEL")
	})
	t.Run("非法 owner 模式返回 1", func(t *testing.T) {
		w16hApplyEnv(t, map[string]string{"JUHE_AI_BLUE_GREEN_OWNER_MODE": "w16h-garbage"})
		w16hRunArms(t, nil, 1, "must be active, standby, or drain")
	})
	t.Run("standby 监听地址非法返回 1", func(t *testing.T) {
		w16hApplyEnv(t, map[string]string{"JUHE_AI_BLUE_GREEN_OWNER_MODE": "standby"})
		w16hRunArms(t, []string{"-health-listen-address=10.0.0.8:3305"}, 1,
			"listen passive jobs health endpoint")
	})
	t.Run("F1 配置默认化后空 env 在 store 打开处失败返回 1", func(t *testing.T) {
		// 2026-09-19 零配置决策：F1 配置不再有必填缺失臂（INSTANCE_ID/STORE/
		// 路径全部派生默认）；空 env 装配推进到 store 打开，因派生的共享
		// SQLite 数据源尚不存在而 fail-fast（只读数据源校验保留）。DATA_DIR
		// 指向自管理目录，避免在包目录创建 ./data。
		root, mkdirErr := os.MkdirTemp("", "w16h-empty-env-")
		if mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		w16hApplyEnv(t, map[string]string{"JUHE_AI_DATA_DIR": root})
		w16hRunArms(t, nil, 1, "open F1 runtime-log-indexer store")
	})
	t.Run("F1 ONCE 不支持返回 1", func(t *testing.T) {
		w16hApplyEnv(t, w16hBaseEnv(t))
		w16hApplyEnv(t, map[string]string{"JUHE_AI_RUNTIME_LOG_ONCE": "true"})
		w16hRunArms(t, nil, 1, "JUHE_AI_RUNTIME_LOG_ONCE=true is not supported by juhe-ai-jobs")
	})
	t.Run("F1 store 打开失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		garbage := filepath.Join(t.TempDir(), "w16h-garbage-runtime.sqlite3")
		if err := os.WriteFile(garbage, []byte("w16h not a sqlite database"), 0o644); err != nil {
			t.Fatal(err)
		}
		env["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"] = garbage
		w16hApplyEnv(t, env)
		w16hRunArms(t, nil, 1, "open F1 runtime-log-indexer store")
	})
	t.Run("迁移分发臂 open store 失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		garbage := filepath.Join(t.TempDir(), "w16h-garbage-migrate.sqlite3")
		if err := os.WriteFile(garbage, []byte("w16h not a sqlite database"), 0o644); err != nil {
			t.Fatal(err)
		}
		env["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"] = garbage
		w16hApplyEnv(t, env)
		w16hRunArms(t, []string{"-migrate-runtime-log-legacy-sqlite"}, 1,
			"open F1 runtime-log-indexer store")
	})
}

// TestW16HRunTableMonitorArms 覆盖 F2 链错误臂与 --once 单轮分支。
func TestW16HRunTableMonitorArms(t *testing.T) {
	t.Run("F2 配置非法返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		// 零配置决策下 TABLE_MONITOR_INSTANCE_ID 缺省取 hostname，「配置缺失」
		// 不再 fail-fast；保留 F2 配置链的确定性失败臂：store 模式非法在
		// LoadConfig 处拒绝。
		env["JUHE_AI_TABLE_MONITOR_STORE"] = "w16h-bogus"
		w16hApplyEnv(t, env)
		w16hRunArms(t, nil, 1, "load F2 table-monitor config")
	})
	t.Run("F2 store 打开失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		garbage := filepath.Join(t.TempDir(), "w16h-garbage-tablemonitor.sqlite3")
		if err := os.WriteFile(garbage, []byte("w16h not a sqlite database"), 0o644); err != nil {
			t.Fatal(err)
		}
		env["JUHE_AI_TABLE_MONITOR_DATABASE_PATH"] = garbage
		w16hApplyEnv(t, env)
		w16hRunArms(t, nil, 1, "open F2 table-monitor store")
	})
	t.Run("once 单轮采样后返回 0", func(t *testing.T) {
		w16hApplyEnv(t, w16hBaseEnv(t))
		stdout, _ := w16hRunArms(t, []string{"-once"}, 0, "")
		var payload map[string]any
		if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
			t.Fatalf("--once 必须向 stdout 输出单个 JSON 结果: %v stdout=%q", err, stdout)
		}
	})
}

// TestW16HRunComponentConfigArms 覆盖 J1/model-recovery/J2/J3a/管理接口守卫/
// J3b/worker/gometrics 各加载错误臂（基底 F1+F2 成功，组件其余全禁用）。
func TestW16HRunComponentConfigArms(t *testing.T) {
	t.Run("J1 配置错误返回 1", func(t *testing.T) {
		w16hApplyEnv(t, w16hBaseEnv(t))
		w16hApplyEnv(t, map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER": "node",
		})
		w16hRunArms(t, nil, 1, "load J1 account-health config")
	})
	t.Run("model-recovery 配置错误返回 1", func(t *testing.T) {
		w16hApplyEnv(t, w16hBaseEnv(t))
		// JUHE_AI_REDIS_STATE_URL 非空即启用分布式 profile；缺合法 namespace
		// 时 LoadRedisConfig 报错。
		w16hApplyEnv(t, map[string]string{"JUHE_AI_REDIS_STATE_URL": "redis://w16h-invalid"})
		w16hRunArms(t, nil, 1, "load model-recovery config")
	})
	t.Run("J2 配置错误返回 1", func(t *testing.T) {
		w16hApplyEnv(t, w16hBaseEnv(t))
		w16hApplyEnv(t, map[string]string{
			"JUHE_AI_ACCOUNT_BALANCE_ENABLED":    "true",
			"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER": "node",
		})
		w16hRunArms(t, nil, 1, "load J2 account-balance config")
	})
	t.Run("J3a 配置错误返回 1", func(t *testing.T) {
		w16hApplyEnv(t, w16hBaseEnv(t))
		w16hApplyEnv(t, map[string]string{
			"JUHE_AI_PROXY_LATENCY_ENABLED":    "true",
			"JUHE_AI_PROXY_LATENCY_JOBS_OWNER": "node",
		})
		w16hRunArms(t, nil, 1, "load J3a proxy-latency config")
	})
	t.Run("J3a 管理守卫（J3a 未启用）返回 1", func(t *testing.T) {
		w16hApplyEnv(t, w16hBaseEnv(t))
		w16hApplyEnv(t, map[string]string{
			"JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED":        "true",
			"JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS": "127.0.0.1:3311",
			"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL":   "postgres://w16h:w16h@127.0.0.1:1/w16h",
		})
		w16hRunArms(t, nil, 1, "启用 J3a 管理接口前必须启用 J3a Go owner")
	})
	t.Run("J3b 加载即拒绝返回 1", func(t *testing.T) {
		w16hApplyEnv(t, w16hBaseEnv(t))
		w16hApplyEnv(t, map[string]string{"JUHE_AI_MODEL_CHECK_ENABLED": "true"})
		w16hRunArms(t, nil, 1, "load J3b model-check config")
	})
	t.Run("worker 配置错误返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		env["JUHE_AI_TASK_RUNS_DATABASE_PATH"] = filepath.Join(t.TempDir(), "task-runs.sqlite3")
		env["JUHE_AI_USAGE_SHARD_ROOT"] = filepath.Join(t.TempDir(), "usage-shards")
		env["JUHE_AI_SECRET"] = "0123456789abcdef0123456789abcdef"
		// drain 超时低于 100ms 时 loadWorkerConfig fail closed。
		env["JUHE_AI_JOBS_DRAIN_TIMEOUT_MS"] = "1"
		w16hApplyEnv(t, env)
		w16hRunArms(t, nil, 1, "load jobs worker config")
	})
	t.Run("worker 装配失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		garbage := filepath.Join(t.TempDir(), "w16h-garbage-catalog.sqlite3")
		if err := os.WriteFile(garbage, []byte("w16h not a sqlite database"), 0o644); err != nil {
			t.Fatal(err)
		}
		env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"] = garbage
		env["JUHE_AI_INSTANCE_ID"] = "w16h-worker-fail"
		env["JUHE_AI_WORKER_ROLE"] = "stats-worker"
		w16hApplyEnv(t, env)
		w16hRunArms(t, nil, 1, "assemble jobs worker")
	})
	t.Run("owner 健康监听地址非法返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		w16hApplyEnv(t, env)
		w16hRunArms(t, []string{"-health-listen-address=10.0.0.8:3305"}, 1,
			"listen jobs health endpoint")
	})
	t.Run("goMetrics 配置错误返回 1", func(t *testing.T) {
		w16hApplyEnv(t, w16hBaseEnv(t))
		w16hApplyEnv(t, map[string]string{"JUHE_AI_GO_RUNTIME_METRICS_STORE": "w16h-garbage"})
		port := wgFreePort(t)
		w16hRunArms(t, []string{"-health-listen-address=127.0.0.1:" + itoa(port)}, 1,
			"load Go runtime metrics config")
	})
}

// TestW16HRunOwnerGracefulShutdown 在完整 owner env（worker 全家族 SQLite +
// J1 SQLite 文件输入）内驱动 run() 到 supervisor 循环，轮询 /health 就绪后
// 注入取消，断言优雅停机返回 0。
func TestW16HRunOwnerGracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("owner 停机链路 skipped in -short mode")
	}
	w16hApplyEnv(t, wgOwnerModeMainEnv(t, t.TempDir()))
	cancel := w16hInjectCancelSignal(t)
	port := wgFreePort(t)
	runDone := make(chan int, 1)
	go func() {
		runDone <- run([]string{"-health-listen-address=127.0.0.1:" + itoa(port)}, io.Discard, io.Discard)
	}()
	wgPollHealthPayload(t, port, 60*time.Second, func(payload map[string]any) bool {
		ready, _ := payload["ready"].(bool)
		return ready
	})
	cancel()
	select {
	case code := <-runDone:
		if code != 0 {
			t.Fatalf("优雅停机后 run 必须返回 0，得到 %d", code)
		}
	case <-time.After(wgChildGracefulTimeout):
		t.Fatal("run 未在限期内优雅返回")
	}
}

// TestW16HRunPassiveGracefulShutdown 进程内覆盖 runPassiveJobs 全链：
// standby 模式健康端点服务 → 注入取消 → 优雅停机返回 0。
func TestW16HRunPassiveGracefulShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("passive 停机链路 skipped in -short mode")
	}
	w16hApplyEnv(t, map[string]string{"JUHE_AI_BLUE_GREEN_OWNER_MODE": "standby"})
	cancel := w16hInjectCancelSignal(t)
	port := wgFreePort(t)
	runDone := make(chan int, 1)
	go func() {
		runDone <- run([]string{"-health-listen-address=127.0.0.1:" + itoa(port)}, io.Discard, io.Discard)
	}()
	wgPollHealthPayload(t, port, 60*time.Second, func(payload map[string]any) bool {
		ownerReady, _ := payload["ownerReady"].(bool)
		return ownerReady == false
	})
	response, err := http.Get("http://127.0.0.1:" + itoa(port) + "/health")
	if err == nil {
		_ = response.Body.Close()
	}
	cancel()
	select {
	case code := <-runDone:
		if code != 0 {
			t.Fatalf("passive 优雅停机后 run 必须返回 0，得到 %d", code)
		}
	case <-time.After(wgChildGracefulTimeout):
		t.Fatal("passive run 未在限期内优雅返回")
	}
}

// TestW16HRunFlagOutputs 进程内断言 --version 与 --check-boundary 的 stdout
// 输出与退出码（run 抽取后 stdout 经参数注入）。
func TestW16HRunFlagOutputs(t *testing.T) {
	t.Run("--version", func(t *testing.T) {
		stdout, _ := w16hRunArms(t, []string{"--version"}, 0, "")
		want := "juhe-ai-jobs project=" + string(contracts.ProjectJobs) + " contract=" + contracts.ArchitectureVersion + "\n"
		if stdout != want {
			t.Fatalf("--version stdout 逐字节断言失败：want %q got %q", want, stdout)
		}
	})
	t.Run("--check-boundary", func(t *testing.T) {
		stdout, _ := w16hRunArms(t, []string{"--check-boundary"}, 0, "")
		want := "juhe-ai-jobs boundary=ready runtime=table-monitor-owner\n"
		if stdout != want {
			t.Fatalf("--check-boundary stdout 逐字节断言失败：want %q got %q", want, stdout)
		}
	})
}

// TestW16HRunDeepFailArms 覆盖 F1/F2 PG 池、J1 store/input/ping、J2/J3a 池
// 与 store、goMetrics store 等深层错误臂（坏 DSN / 合法不可达 DSN / 垃圾
// SQLite 文件注入，全部无需真实 PG）。
func TestW16HRunDeepFailArms(t *testing.T) {
	const garbageDSN = "w16h-not-a-postgres-url"
	const unreachableDSN = "postgres://w16h:w16h@127.0.0.1:1/w16h"
	t.Run("F1 缺只读源文件 store 打开失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		// 不预建 business.sqlite3：F1 在 OpenStore 即校验只读数据源文件存在，
		// CheckSchema 的同名检查成为恒过前置（错误出口收敛为 open store 臂）。
		if err := os.Remove(env["JUHE_AI_DATABASE_PATH"]); err != nil {
			t.Fatal(err)
		}
		w16hApplyEnv(t, env)
		w16hRunArms(t, nil, 1, "open F1 runtime-log-indexer store")
	})
	t.Run("F2 PG 模式 schema 校验失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		env["JUHE_AI_TABLE_MONITOR_STORE"] = "postgres"
		env["JUHE_AI_TABLE_MONITOR_POSTGRES_URL"] = garbageDSN
		w16hApplyEnv(t, env)
		// pgx sql.Open 惰性解析：坏 DSN 在 schema 初始化时才暴露。
		w16hRunArms(t, nil, 1, "initialize F2 table-monitor schema")
	})
	t.Run("J1 PG store schema 初始化失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		w16hApplyEnv(t, env)
		w16hApplyEnv(t, map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
			"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "w16h-j1",
			"JUHE_AI_ACCOUNT_HEALTH_STORE":             "postgres",
			"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":      garbageDSN,
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":      "files",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   t.TempDir(),
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
			"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "0123456789abcdef0123456789abcdef",
		})
		w16hRunArms(t, nil, 1, "initialize J1 account-health schema")
	})
	t.Run("J1 store 打开失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		garbage := filepath.Join(t.TempDir(), "w16h-garbage-j1.sqlite3")
		if err := os.WriteFile(garbage, []byte("w16h not a sqlite database"), 0o644); err != nil {
			t.Fatal(err)
		}
		w16hApplyEnv(t, env)
		w16hApplyEnv(t, map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
			"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "w16h-j1",
			"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
			"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     garbage,
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":      "files",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   t.TempDir(),
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
			"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "0123456789abcdef0123456789abcdef",
		})
		w16hRunArms(t, nil, 1, "open J1 account-health store")
	})
	t.Run("J1 input PG 池打开失败返回 1", func(t *testing.T) {
		// J1 契约：input=postgres 强制 jobs store=postgres；store schema 初始
		// 化需要真实 PG，本臂与 ping 臂移至 w16i PG 门禁组。
		t.Skip("J1 input PG 臂依赖真实 PG（store=postgres 前置），归 w16i PG 门禁组")
	})
	t.Run("model-recovery 缺 PG reader 守卫返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		// 既有行为：J1 store 打开后的 mR 守卫失败出口不关 J1 SQLite（与原
		// main() 的 fail() os.Exit 语义一致）；句柄占用会卡 TempDir 清理，
		// 故用自管理目录并在 cleanup 时忽略错误。
		root := filepath.Join(os.TempDir(), "w16h-mr-guard")
		_ = os.RemoveAll(root)
		if err := os.MkdirAll(filepath.Join(root, "store"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "inputs"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		w16hApplyEnv(t, env)
		w16hApplyEnv(t, map[string]string{
			"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
			"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "w16h-j1",
			"JUHE_AI_ACCOUNT_HEALTH_STORE":             "sqlite",
			"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     filepath.Join(root, "store", "j1.sqlite3"),
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE":      "files",
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   filepath.Join(root, "inputs"),
			"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0",
			"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "0123456789abcdef0123456789abcdef",
			"JUHE_AI_REDIS_STATE_URL":                  "redis://127.0.0.1:1",
			"JUHE_AI_REDIS_NAMESPACE":                  "juhe-ai:w16h",
		})
		w16hRunArms(t, nil, 1, "启用 model-recovery 必须同时启用 PostgreSQL J1 direct input reader")
	})
	t.Run("J2 service 初始化失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		w16hApplyEnv(t, env)
		w16hApplyEnv(t, map[string]string{
			"JUHE_AI_ACCOUNT_BALANCE_ENABLED":            "true",
			"JUHE_AI_ACCOUNT_BALANCE_JOBS_OWNER":         "go",
			"JUHE_AI_ACCOUNT_BALANCE_OWNER_ID":           "w16h",
			"JUHE_AI_ACCOUNT_BALANCE_STORE":              "postgres",
			"JUHE_AI_ACCOUNT_BALANCE_POSTGRES_URL":       garbageDSN,
			"JUHE_AI_ACCOUNT_BALANCE_INPUT_POSTGRES_URL": unreachableDSN,
			"JUHE_AI_ACCOUNT_BALANCE_CREDENTIAL_SECRET":  "0123456789abcdef0123456789abcdef",
			"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET":   "0123456789abcdef0123456789abcdef",
		})
		// pgx 惰性解析下 jobs/input 池 acquire 均成功，坏 DSN 由 J2 service
		// 初始化（NewService → OpenStore）暴露——J2 装配链唯一可达错误出口。
		w16hRunArms(t, nil, 1, "initialize J2 account-balance service")
	})
	t.Run("J3a jobs 池后 store 打开失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		w16hApplyEnv(t, env)
		w16hApplyEnv(t, map[string]string{
			"JUHE_AI_PROXY_LATENCY_ENABLED":             "true",
			"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":          "go",
			"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":         "w16h-j3a",
			"JUHE_AI_PROXY_LATENCY_STORE":               "postgres",
			"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":        garbageDSN,
			"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":  unreachableDSN,
			"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":   "0123456789abcdef0123456789abcdef",
			"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": unreachableDSN,
		})
		// pgx 惰性解析下 acquire 成功，坏 DSN 由 OpenStore/CheckSchema 暴露；
		// 断言 J3a 装配链内任意失败文案。
		var stdout, stderr bytes.Buffer
		w16hInjectCancelSignal(t)
		code := run(nil, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("J3a 坏 store 必须返回 1，得到 %d stderr=%q", code, stderr.String())
		}
		if !bytes.Contains(stderr.Bytes(), []byte("open J3a proxy-latency jobs store")) &&
			!bytes.Contains(stderr.Bytes(), []byte("verify pre-provisioned J3a proxy-latency jobs schema")) {
			t.Fatalf("stderr 必须含 J3a store/schema 失败文案，得到 %q", stderr.String())
		}
	})
	t.Run("J3a store schema 校验失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		w16hApplyEnv(t, env)
		w16hApplyEnv(t, map[string]string{
			"JUHE_AI_PROXY_LATENCY_ENABLED":             "true",
			"JUHE_AI_PROXY_LATENCY_JOBS_OWNER":          "go",
			"JUHE_AI_PROXY_LATENCY_INSTANCE_ID":         "w16h-j3a",
			"JUHE_AI_PROXY_LATENCY_STORE":               "postgres",
			"JUHE_AI_PROXY_LATENCY_POSTGRES_URL":        unreachableDSN,
			"JUHE_AI_PROXY_LATENCY_INPUT_POSTGRES_URL":  unreachableDSN,
			"JUHE_AI_PROXY_LATENCY_CREDENTIAL_SECRET":   "0123456789abcdef0123456789abcdef",
			"JUHE_AI_PROXY_LATENCY_RESULT_POSTGRES_URL": unreachableDSN,
		})
		var stdout, stderr bytes.Buffer
		w16hInjectCancelSignal(t)
		code := run(nil, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("J3a 不可达 store 必须返回 1，得到 %d stderr=%q", code, stderr.String())
		}
		if !bytes.Contains(stderr.Bytes(), []byte("J3a proxy-latency jobs store")) &&
			!bytes.Contains(stderr.Bytes(), []byte("J3a proxy-latency jobs schema")) {
			t.Fatalf("stderr 必须含 J3a store/schema 失败文案，得到 %q", stderr.String())
		}
	})
	t.Run("goMetrics schema 校验失败返回 1", func(t *testing.T) {
		env := w16hBaseEnv(t)
		garbage := filepath.Join(t.TempDir(), "w16h-garbage-metrics.sqlite3")
		if err := os.WriteFile(garbage, []byte("w16h not a sqlite database"), 0o644); err != nil {
			t.Fatal(err)
		}
		w16hApplyEnv(t, env)
		w16hApplyEnv(t, map[string]string{
			"JUHE_AI_GO_RUNTIME_METRICS_ENABLED":       "true",
			"JUHE_AI_GO_RUNTIME_METRICS_STORE":         "sqlite",
			"JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH": garbage,
		})
		port := wgFreePort(t)
		// sql.Open 惰性：垃圾文件由 EnsureReady（schema 校验）暴露。
		w16hRunArms(t, []string{"-health-listen-address=127.0.0.1:" + itoa(port)}, 1,
			"verify Go runtime metrics schema")
	})
}

// w16hRunUntilReadyAndCancel 在 goroutine 启动 run()，轮询 /health 至就绪；
// run 提前退出即失败并附退出码与 stderr。就绪后注入停机并断言优雅返回 0。
func w16hRunUntilReadyAndCancel(t *testing.T, env map[string]string, ready func(map[string]any) bool, label string, onReady ...func(port int)) {
	t.Helper()
	w16hApplyEnv(t, env)
	cancel := w16hInjectCancelSignal(t)
	port := wgFreePort(t)
	var stderr bytes.Buffer
	runDone := make(chan int, 1)
	go func() {
		runDone <- run([]string{"-health-listen-address=127.0.0.1:" + itoa(port)}, io.Discard, &stderr)
	}()
	ready2 := false
	var lastPayload map[string]any
	// 全包负载下 F1 lease 与 table-monitor 首轮可能显著变慢：supervisor 对
	// 首轮失败的组件按 1s→30s 抖动重试，重载机器上 F1 偶发需多轮重试才能
	// 持有 lease。就绪窗口取 240s（spawn 常量 60s 的四倍，120s 曾在负载下
	// 不足）。
	deadline := time.Now().Add(240 * time.Second)
	for time.Now().Before(deadline) && !ready2 {
		select {
		case code := <-runDone:
			t.Fatalf("%s：run 在就绪前退出（码=%d）stderr=%s", label, code, stderr.String())
			return
		default:
		}
		response, err := http.Get("http://127.0.0.1:" + itoa(port) + "/health")
		if err == nil {
			var payload map[string]any
			decodeErr := json.NewDecoder(response.Body).Decode(&payload)
			_ = response.Body.Close()
			if decodeErr == nil {
				lastPayload = payload
				if ready(payload) {
					ready2 = true
				}
			}
		}
		time.Sleep(wgHealthPollInterval)
	}
	if !ready2 {
		t.Fatalf("%s：健康端点未在限期内就绪，最后载荷=%v stderr=%s", label, lastPayload, stderr.String())
	}
	if len(onReady) > 0 && onReady[0] != nil {
		onReady[0](port)
	}
	cancel()
	select {
	case code := <-runDone:
		if code != 0 {
			t.Fatalf("%s：优雅停机后 run 必须返回 0，得到 %d", label, code)
		}
	case <-time.After(wgChildGracefulTimeout):
		t.Fatal(label + "：run 未在限期内优雅返回")
	}
}

// TestW16HRunOwnerShutdownWithMetrics 覆盖 F1+F2+J1+worker+goMetrics 的完整
// owner 装配（恒开终态）：默认 ready 闭包、goMetrics 采样组件注册与 Close、
// 全默认健康槽位与优雅停机。
func TestW16HRunOwnerShutdownWithMetrics(t *testing.T) {
	if testing.Short() {
		t.Skip("最小 owner 停机链路 skipped in -short mode")
	}
	env := w16hBaseEnv(t)
	env["JUHE_AI_GO_RUNTIME_METRICS_ENABLED"] = "true"
	env["JUHE_AI_GO_RUNTIME_METRICS_STORE"] = "sqlite"
	metricsPath := filepath.Join(t.TempDir(), "go-metrics.sqlite3")
	env["JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH"] = metricsPath
	// gometrics.OpenStore 不做 DDL：表由 maintenance/运维预 provision（生产
	// 契约）。fixture 按同一 DDL 形态预建，EnsureReady 才能通过。
	if db, err := sql.Open("sqlite", metricsPath); err != nil {
		t.Fatal(err)
	} else {
		store, err := gometrics.NewStore(db, gometrics.DialectSQLite)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.EnsureSchema(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	w16hRunUntilReadyAndCancel(t, env, func(payload map[string]any) bool {
		ready, _ := payload["ready"].(bool)
		return ready
	}, "完整 owner")
}

// TestW16HRunLegacyMigrationFailArms 覆盖 runRuntimeLegacyMigration 的迁移
// 执行失败臂：dataset 旧库缺 runtime-log schema 时 MigrateLegacySQLite 报错，
// RunWithOwnerLease 传播为退出码 1。
func TestW16HRunLegacyMigrationFailArms(t *testing.T) {
	if testing.Short() {
		t.Skip("legacy migration 失败臂 skipped in -short mode")
	}
	root := t.TempDir()
	logsDirectory := filepath.Join(root, "logs")
	if err := os.MkdirAll(logsDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"business.sqlite3", "dataset.sqlite3", "stats.sqlite3", "usage-catalog.sqlite3"} {
		w16hSeedSQLite(t, filepath.Join(root, name))
	}
	// dataset 旧库保持空 schema（不 seed runtime-log 表）→ 迁移失败。
	env := map[string]string{
		"JUHE_AI_RUNTIME_LOG_INSTANCE_ID":        "w16h-legacy-fail",
		"JUHE_AI_RUNTIME_LOG_STORE":              "sqlite",
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":      filepath.Join(root, "runtime-log.sqlite3"),
		"JUHE_AI_RUNTIME_LOG_OWNER_LEASE":        "15s",
		"JUHE_AI_LOG_DIR":                        logsDirectory,
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":    filepath.Join(root, "table-monitor.sqlite3"),
		"JUHE_AI_DATABASE_PATH":                  filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_DATASET_DATABASE_PATH":          filepath.Join(root, "dataset.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH":            filepath.Join(root, "stats.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":    filepath.Join(root, "usage-catalog.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT": filepath.Join(root, "codex-state"),
		"JUHE_AI_CHAT_ASSETS_ROOT":               filepath.Join(root, "chat-assets"),
	}
	w16hApplyEnv(t, env)
	w16hInjectCancelSignal(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"-migrate-runtime-log-legacy-sqlite"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("旧库缺 schema 的迁移必须返回 1，得到 %d stderr=%q", code, stderr.String())
	}
}

// itoa 供测试内拼接端口号。
func itoa(value int) string {
	return strconv.Itoa(value)
}
