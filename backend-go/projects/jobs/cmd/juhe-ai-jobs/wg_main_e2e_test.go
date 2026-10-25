package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"os/exec"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// wgChildEnvMode / wgChildEnvScenario / wgChildEnvHealthAddr 是 main() 子进程
// 模式的协议 env：子进程（重执行的测试二进制）在 TestMain 里检测到模式标记后
// 直接驱动生产 main()，由父测试通过 CTRL_BREAK 触发优雅停机。main() 内部
// fail() 会 os.Exit，因此只能在子进程运行，父进程内只测 flag 早退路径。
const (
	wgChildEnvMode       = "WG_JOBS_MAIN_CHILD_MODE"
	wgChildEnvScenario   = "WG_JOBS_MAIN_CHILD_SCENARIO"
	wgChildEnvHealthAddr = "WG_JOBS_MAIN_CHILD_HEALTH_ADDR"

	wgScenarioOwner          = "owner"
	wgScenarioPassive        = "passive"
	wgChildReadyTimeout      = 60 * time.Second
	wgChildGracefulTimeout   = 45 * time.Second
	wgHealthPollInterval     = 100 * time.Millisecond
	wgChildFlagSet           = "wg-jobs-main-child"
	wgChildLegacyOutputLine  = "旧运行日志 SQLite 数据迁移和完整性校验完成"
	wgChildVersionOutputLine = "juhe-ai-jobs project="
)

// wgFreePort 分配一个当前空闲的回环端口（监听后立刻释放；存在理论竞态，
// 但仅用于本机测试端口选择）。
func wgFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配空闲端口失败: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("释放探测端口失败: %v", err)
	}
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("解析端口失败: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatalf("端口转换失败: %v", err)
	}
	return port
}

// TestMain 支持 main() 子进程模式：父测试把测试二进制重新拉起（新进程组），
// 子进程在这里直接调用生产 main()；main() 返回后以 -test.run=^$ 复跑
// testing 框架，让已累积的覆盖计数随 -test.gocoverdir 写出并被 go test
// 合并。这是 main()（含 os.Exit 的 fail 路径）唯一可进程内覆盖的方式。
func TestMain(m *testing.M) {
	if os.Getenv(wgChildEnvMode) != "1" {
		os.Exit(m.Run())
	}
	// 子进程：run() 用自己的 FlagSet 解析显式 args（-test.* 属于 testing，
	// 不再依赖全局 flag.CommandLine 替换）；run 优雅返回后以 -test.run=^$
	// 复跑 testing 框架，让已累积的覆盖计数随 -test.gocoverdir 写出并被
	// go test 合并。非零返回码等价原 fail() 的 os.Exit（runtime 钩子此时
	// 写 GOCOVERDIR）。
	childArgs := []string{"-health-listen-address=" + os.Getenv(wgChildEnvHealthAddr)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if code := run(childArgs, os.Stdout, os.Stderr); code != 0 {
			os.Exit(code)
		}
	}()
	<-done
	// run() 已优雅返回：跑零个测试以触发覆盖写出。
	restoredArgs := []string{}
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-test.run=") || strings.HasPrefix(arg, "-test.timeout=") {
			continue
		}
		restoredArgs = append(restoredArgs, arg)
	}
	os.Args = append(restoredArgs, "-test.run=^$", "-test.timeout=120s")
	os.Exit(m.Run())
}

// wgSpawnMainChild 以新进程组拉起测试二进制（子进程模式），返回 cmd 供
// 断言后投递 CTRL_BREAK。
func wgSpawnMainChild(t *testing.T, env map[string]string, scenario string, healthPort int) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试二进制失败: %v", err)
	}
	var gocoverdir string
	for _, arg := range os.Args {
		if value, ok := strings.CutPrefix(arg, "-test.gocoverdir="); ok {
			gocoverdir = value
		}
	}
	childArgs := []string{"-test.run=^$", "-test.timeout=300s"}
	if gocoverdir != "" {
		childArgs = append(childArgs, "-test.gocoverdir="+gocoverdir)
	}
	cmd := exec.Command(executable, childArgs...)
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Env = append(cmd.Env,
		wgChildEnvMode+"=1",
		wgChildEnvScenario+"="+scenario,
		wgChildEnvHealthAddr+"=127.0.0.1:"+fmt.Sprint(healthPort))
	// 独立进程组：CTRL_BREAK 只投递给该子进程（Go runtime 将其映射为
	// os.Interrupt，命中 main() 的 signal.NotifyContext 干净停机路径）。
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 main 子进程失败: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		if t.Failed() {
			dump := output.String()
			if len(dump) > 8192 {
				dump = dump[:8192]
			}
			t.Logf("main 子进程输出（前 8KB）:\n%s", dump)
		}
	})
	return cmd
}

// wgPollHealthPayload 轮询 /health 直到满足断言或超时；返回最后一次载荷。
func wgPollHealthPayload(t *testing.T, port int, timeout time.Duration, accept func(payload map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last map[string]any
	for time.Now().Before(deadline) {
		response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if err == nil {
			body := response.Body
			payload := map[string]any{}
			decodeErr := json.NewDecoder(body).Decode(&payload)
			_ = body.Close()
			if decodeErr == nil {
				last = payload
				if accept(payload) {
					return payload
				}
			}
		}
		time.Sleep(wgHealthPollInterval)
	}
	t.Fatalf("/health 未在限期内满足就绪条件，最后载荷: %v", last)
	return nil
}

// wgInterruptAndWait 向子进程投递 CTRL_BREAK 并等待干净退出。
func wgInterruptAndWait(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid)); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("投递 CTRL_BREAK 失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("main 子进程未干净退出: %v", err)
		}
	case <-time.After(wgChildGracefulTimeout):
		_ = cmd.Process.Kill()
		t.Fatal("main 子进程未在限期内优雅停机")
	}
}

// wgOwnerModeMainEnv 构造 owner 模式完整 main() 的 env：F1/F2/J1 + 全量
// worker 家族（SQLite + 文件输入源；Redis 缺省 → 电路/速度优先族 disabled）。
func wgOwnerModeMainEnv(t *testing.T, root string) map[string]string {
	t.Helper()
	env := workerSmokeTestEnv(t)
	// 存储路径统一收拢到本次测试的隔离目录。
	env["JUHE_AI_DATABASE_PATH"] = filepath.Join(root, "business.sqlite3")
	env["JUHE_AI_STATS_DATABASE_PATH"] = filepath.Join(root, "stats.sqlite3")
	env["JUHE_AI_TASK_RUNS_DATABASE_PATH"] = filepath.Join(root, "task-runs.sqlite3")
	env["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"] = filepath.Join(root, "usage-catalog.sqlite3")
	env["JUHE_AI_USAGE_SHARD_ROOT"] = filepath.Join(root, "usage-shards")
	env["JUHE_AI_DATASET_DATABASE_PATH"] = filepath.Join(root, "dataset.sqlite3")
	env["JUHE_AI_CHAT_DATABASE_PATH"] = filepath.Join(root, "chat.sqlite3")
	env["JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"] = filepath.Join(root, "codex-state")
	env["JUHE_AI_CHAT_ASSETS_ROOT"] = filepath.Join(root, "chat-assets")
	// F1 runtime-log-indexer。
	env["JUHE_AI_RUNTIME_LOG_INSTANCE_ID"] = "wg-main-e2e"
	env["JUHE_AI_RUNTIME_LOG_STORE"] = "sqlite"
	env["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"] = filepath.Join(root, "runtime-log.sqlite3")
	logsDirectory := filepath.Join(root, "logs")
	if err := os.MkdirAll(logsDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	env["JUHE_AI_LOG_DIR"] = logsDirectory
	// F1 runtime store 会校验只读数据源文件存在，先建全部 SQLite 文件。
	for _, name := range []string{
		"business.sqlite3", "stats.sqlite3", "dataset.sqlite3", "usage-catalog.sqlite3",
		"task-runs.sqlite3", "chat.sqlite3",
	} {
		db, dbErr := sql.Open("sqlite", filepath.Join(root, name))
		if dbErr != nil {
			t.Fatal(dbErr)
		}
		// Ping 触发物理建文件（sql.Open 惰性连接）。
		if dbErr := db.Ping(); dbErr != nil {
			t.Fatal(dbErr)
		}
		if dbErr := db.Close(); dbErr != nil {
			t.Fatal(dbErr)
		}
	}
	// F1 indexer 与 J1 投影面从业务库读 system_settings/投影游标
	// （cursors/receipts 表由 maintenance bootstrap 负责，进程不做 DDL）。
	business, dbErr := sql.Open("sqlite", filepath.Join(root, "business.sqlite3"))
	if dbErr != nil {
		t.Fatal(dbErr)
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS system_settings (
			system_account_id TEXT NOT NULL,
			key TEXT NOT NULL,
			value_json TEXT,
			PRIMARY KEY (system_account_id, key)
		)`,
		`CREATE TABLE IF NOT EXISTS account_health_projection_receipts (
			outcome_id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL,
			input_version INTEGER NOT NULL,
			disposition TEXT NOT NULL,
			reason TEXT,
			applied_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS account_health_projection_cursors (
			consumer_key TEXT PRIMARY KEY,
			observed_at TEXT,
			outcome_id TEXT,
			updated_at TEXT NOT NULL
		)`,
		// accounts.capability_domain 是 jobs 启动的业务库形状契约
		// （capability_domain_schema.go 启动只读校验，2026-10 M1）：fixture 只
		// 提供启动校验消费的最小 accounts 形状（列声明与 maintenance DDL
		// 同构）；任务族所需的完整列集由 wgUnionBusinessSchema 等专项 fixture
		// 承载。
		`CREATE TABLE IF NOT EXISTS accounts (
			id TEXT PRIMARY KEY,
			deleted_at TEXT,
			capability_domain TEXT NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image'))
		)`,
	} {
		if _, err := business.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// M2 启动阶段门禁与调度偏好启动校验夹具已随 sqlite 子进程 e2e 退役：
	// owner 全链装配在 PG 上由 w14q/w16i 覆盖库 e2e 承接（w14j seed + w16j
	// fixture 提供契约形状）。
	if err := business.Close(); err != nil {
		t.Fatal(err)
	}
	// J1 account-health（SQLite store + 显式 files 输入源；目录必须存在且为空 =
	// 无待消费输入。INPUT_SOURCE 自 2026-09 缺省改为 sqlite 直读，但本 fixture
	// 的业务库只有 worker 装配所需表，走直读会契约失败；这些用例验证 worker
	// 装配而非 J1 输入，故显式钉住 files 后备源）。J1 与 worker 均恒装配
	// （2026-09-19 决策），env 不再需要启用开关。
	env["JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER"] = "go"
	env["JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID"] = "wg-main-e2e"
	env["JUHE_AI_ACCOUNT_HEALTH_STORE"] = "sqlite"
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE"] = "files"
	env["JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH"] = filepath.Join(root, "account-health.sqlite3")
	inputDirectory := filepath.Join(root, "account-health-inputs")
	if err := os.MkdirAll(inputDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY"] = inputDirectory
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY"] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA0"
	env["JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"] = "0123456789abcdef0123456789abcdef"
	return env
}

// TestMainOwnerModeRunsAndShutsDownGracefully 在子进程运行完整生产 main()
// （worker + J1 + F1/F2），断言 /health 报告全部组件就绪，CTRL_BREAK 后
// 干净退出（覆盖 main() 组合根主体与 supervisor 停机排空）。
func TestMainPassiveModeServesStandbyHealth(t *testing.T) {
	if testing.Short() {
		t.Skip("main e2e skipped in -short mode")
	}
	root := t.TempDir()
	env := wgOwnerModeMainEnv(t, root)
	env["JUHE_AI_BLUE_GREEN_OWNER_MODE"] = "standby"
	port := wgFreePort(t)
	cmd := wgSpawnMainChild(t, env, wgScenarioPassive, port)
	payload := wgPollHealthPayload(t, port, wgChildReadyTimeout, func(current map[string]any) bool {
		_, hasOwnerMode := current["ownerMode"]
		return hasOwnerMode
	})
	if payload["ready"] != false || payload["ownerReady"] != false || payload["ownerMode"] != "standby" {
		t.Fatalf("passive 健康载荷不得声明 owner 就绪: %v", payload)
	}
	wgInterruptAndWait(t, cmd)
}
