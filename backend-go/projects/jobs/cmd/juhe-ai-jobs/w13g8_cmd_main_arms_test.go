// 波次 w13g8：main() fail() 出错路径的子进程覆盖批测。
//
// main() 内部 fail() 会 os.Exit(1)，无法进程内覆盖；本文件把测试二进制重新
// 拉起为子进程并设置 GOCOVERDIR（与父进程 -test.gocoverdir 同目录）：go1.21+
// 的 coverage 插桩二进制在 os.Exit 时经 runtime 钩子把累计计数写到
// GOCOVERDIR，go test 结束时统一合并进 -coverprofile。父进程只断言退出码。
//
// 场景只驱动"早失败"分支（配置校验/守卫/监听失败），不依赖 PG/Redis；
// J1/J2/J3a 的 PG 装配成功链归 w14q（PG 门禁）。
//
// 不可达语句归因（main.go:370.23,375.3）：J3b 运行时守卫。生产
// modelcheckruntime.LoadConfig 对任何 enabled 配置无条件 fail closed
// （"J3b 不允许在 juhe-ai-jobs 中启用"），main 的 fail 分支在当前加载器
// 之下不可达；守卫按 Solution A 契约保留，登记为防御性不可达。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proxylatency"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// w13g8ChildMode / w13g8ChildArgs 是子进程协议 env：子进程检测到 mode 后
// 用隔离的 flag.CommandLine 直接驱动生产 main()。
const (
	w13g8ChildMode = "W13G8_MAIN_CHILD"
	w13g8ChildArgs = "W13G8_MAIN_CHILD_ARGS"
)

// w13g8FindGocoverdir 从 os.Args 提取 -test.gocoverdir（go test -coverprofile
// 时传入）；缺失返回空串（子进程计数无法合并，跳过覆盖批测）。
func w13g8FindGocoverdir() string {
	for _, arg := range os.Args {
		if value, ok := strings.CutPrefix(arg, "-test.gocoverdir="); ok {
			return value
		}
	}
	return ""
}

// w13g8RunMainChild 子进程主体：隔离 flag 后同步调用生产 main()。fail() 的
// os.Exit 直接终止进程（runtime 钩子此时写 GOCOVERDIR）；main() 优雅返回时
// 以 0 退出（父进程按场景断言退出码）。
func w13g8RunMainChild(argsJSON string) {
	var args []string
	_ = json.Unmarshal([]byte(argsJSON), &args)
	originalCommandLine := flag.CommandLine
	originalArgs := os.Args
	// main() 的 flag.Parse 只看见子进程自己的 flag（-test.* 属于 testing，
	// 必须隔离）；flag 值经 -flag=value 形式传入。
	flag.CommandLine = flag.NewFlagSet("w13g8-main-child", flag.ContinueOnError)
	os.Args = append([]string{originalArgs[0]}, args...)
	main()
	flag.CommandLine = originalCommandLine
	os.Args = originalArgs
	os.Exit(0)
}

// w13g8EnsurePGMainFixture 自愈共享覆盖库上 main() 装配链需要的 bootstrap
// 面（w14q 预置序列的子集，全部加法幂等）：juhe_jobs schema、w14j 业务/
// 任务形状、sys_admin 种子、F4 operation_logs 审计面（J3a management
// CheckContract 的只读契约）、J3a/jobs 侧预 provision（w16j）。连接串不落
// 日志；失败按各 helper 既有约定降级（记录或 skip）。
func w13g8EnsurePGMainFixture(t *testing.T, pgURL string) {
	t.Helper()
	db, err := sql.Open("pgx", pgURL)
	if err != nil {
		t.Skipf("w13g8: 打开覆盖库失败: %v", err)
	}
	defer func() { _ = db.Close() }()
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Skipf("w13g8: 覆盖库不可达: %v", err)
	}
	if _, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_jobs`); err != nil {
		t.Skipf("w13g8: 建立 juhe_jobs schema 失败: %v", err)
	}
	for _, statement := range w14jPGSeedStatements() {
		if _, err := db.Exec(statement); err != nil {
			if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "multiple primary keys") {
				continue
			}
			t.Logf("w13g8 pg seed 跳过: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts
		(id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES ('sys_admin', 'sys_admin', 'sys_admin', 'admin', 'active', 'w13g8-test-seed-not-a-login-hash', $1, $1)
		ON CONFLICT (id) DO NOTHING`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Skipf("w13g8: sys_admin seed 失败（共享库形状漂移，跳过）: %v", err)
	}
	for _, statement := range strings.Split(w14qOperationLogsSchema, ";") {
		if statement = strings.TrimSpace(statement); statement == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Logf("w13g8 F4 seed 跳过: %v", err)
		}
	}
	w16jEnsurePGJobsFixture(t, pgURL)
}

// w13g8FullOwnerEnv 构造 owner 模式中深度臂基座：PG 唯一形态下 F1 与
// worker/J1（store/input URL 缺省回退 JUHE_AI_POSTGRES_URL）全部指向共享
// 覆盖库，使 main() 越过 F1 装配前置、命中各场景的目标失败分支（原 SQLite
// 路径族 env 已退役不再被读取）。覆盖库门禁：连接串不可用即 skip。
func w13g8FullOwnerEnv(t *testing.T) map[string]string {
	t.Helper()
	pgURL := w12cPGOverrideURL(t)
	if pgURL == "" {
		t.Skip("w13g8: 覆盖库连接串不可用")
	}
	w13g8EnsurePGMainFixture(t, pgURL)
	env := wgOwnerModeMainEnv(t, t.TempDir())
	env["JUHE_AI_RUNTIME_LOG_POSTGRES_URL"] = pgURL
	env["JUHE_AI_POSTGRES_URL"] = pgURL
	// J1 postgres 直连输入（PG 唯一形态必填，原 files/sqlite 形状退役）。
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL"] = pgURL
	return env
}

// w13g8NoPostgresURLEnv 构造 worker 配置 fail closed 臂：F1 独立 URL 可加载，
// JUHE_AI_POSTGRES_URL 缺失 → loadWorkerConfig fail-fast（PG 唯一形态下的
// worker 配置守卫）。
func w13g8NoPostgresURLEnv(t *testing.T) map[string]string {
	env := w13g8FullOwnerEnv(t)
	delete(env, "JUHE_AI_POSTGRES_URL")
	return env
}

// w13g8WorkerAssemblyFailEnv 构造 worker 装配失败臂的 env：PG 连接串指向
// 不可达端口（业务/usage 句柄 Acquire 失败 → buildWorkerAssembly 错误传播；
// F1 走独立 URL 不受影响）。
func w13g8WorkerAssemblyFailEnv(t *testing.T) map[string]string {
	env := w13g8FullOwnerEnv(t)
	env["JUHE_AI_POSTGRES_URL"] = w16dClosedPortURL()
	return env
}

// w13g8GarbageRuntimeEnv 构造 F1 OpenStore 失败臂的 env：F1 运行日志 PG
// 连接串指向不可达端口（OpenStore/Ping 失败）。
func w13g8GarbageRuntimeEnv(t *testing.T) map[string]string {
	env := w13g8FullOwnerEnv(t)
	env["JUHE_AI_RUNTIME_LOG_POSTGRES_URL"] = w16dClosedPortURL()
	return env
}

// w13g8PassiveEnv 构造 standby passive 模式 env（passive 路径在任何装配前返回）。
func w13g8PassiveEnv(t *testing.T) map[string]string {
	root := t.TempDir()
	env := wgOwnerModeMainEnv(t, root)
	env["JUHE_AI_BLUE_GREEN_OWNER_MODE"] = "standby"
	return env
}

// TestW13G8MainFailFastArms 覆盖 main() 的早失败分支（flag/配置校验/守卫/
// 装配失败/监听失败）。
func TestW13G8MainFailFastArms(t *testing.T) {
	if os.Getenv(w13g8ChildMode) == "1" {
		w13g8RunMainChild(os.Getenv(w13g8ChildArgs))
		return
	}
	if w13g8FindGocoverdir() == "" {
		t.Skip("w13g8 fail-fast 子进程计数需要 -test.gocoverdir（go test -coverprofile 时生效）")
	}
	scenarios := []struct {
		name       string
		args       []string
		envBuilder func(*testing.T) map[string]string
		patch      map[string]string
		anyCode    bool // true 时仅要求限时退出（不限具体退出码）。
		expectCode int
	}{
		// flag 早失败臂（os.Exit(2)）。
		{"多余位置参数", []string{"w13g8-positional"}, workerSmokeTestEnv, nil, false, 2},
		// 配置加载失败臂（fail → exit 1）。
		{"无效日志级别", nil, workerSmokeTestEnv, map[string]string{"JUHE_AI_LOG_LEVEL": "w13g8-bogus"}, false, 1},
		{"owner 模式非法", nil, workerSmokeTestEnv, map[string]string{"JUHE_AI_BLUE_GREEN_OWNER_MODE": "w13g8"}, false, 1},
		{"runtime log 配置缺失", nil, workerSmokeTestEnv, nil, false, 1},
		// 中深度臂：以 w13g8FullOwnerEnv 为基座（F1/J1 指向共享覆盖库、
		// fixture 自愈通过），fail 前置点之后才命中目标分支。
		{"runtime log ONCE 不支持", nil, w13g8FullOwnerEnv, map[string]string{"JUHE_AI_RUNTIME_LOG_ONCE": "true"}, false, 1},
		{"J1 owner 非 go", nil, w13g8FullOwnerEnv, map[string]string{"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER": "w13g8-not-go"}, false, 1},
		{"model-recovery 非法 namespace", nil, w13g8FullOwnerEnv, map[string]string{
			"JUHE_AI_REDIS_STATE_URL": "redis://127.0.0.1:6379/9",
			"JUHE_AI_REDIS_NAMESPACE": "w13g8 bad namespace!",
		}, false, 1},
		{"model-recovery Redis URL 非法", nil, w13g8FullOwnerEnv, map[string]string{
			"JUHE_AI_REDIS_STATE_URL": "redis://w13g8-invalid-url",
			"JUHE_AI_REDIS_NAMESPACE": "w13g8",
		}, false, 1},
		{"J3a 管理接口守卫", nil, w13g8FullOwnerEnv, map[string]string{"JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED": "true"}, false, 1},
		{"F1 OpenStore 失败", nil, w13g8GarbageRuntimeEnv, nil, false, 1},
		{"J3b 由网关持有", nil, w13g8FullOwnerEnv, map[string]string{"JUHE_AI_MODEL_CHECK_ENABLED": "true"}, false, 1},
		{"worker 配置 fail closed", nil, w13g8NoPostgresURLEnv, nil, false, 1},
		{"worker 装配失败", nil, w13g8WorkerAssemblyFailEnv, nil, false, 1},
		// 监听失败臂（装配面全部成功后 listen 校验失败）。
		{"健康监听地址非法", []string{"-health-listen-address=w13g8-not-an-address"}, w13g8FullOwnerEnv, map[string]string{}, false, 1},
		// passive standby 模式的监听失败臂（runPassiveJobs listen fail）。
		{"passive 监听地址非法", []string{"-health-listen-address=w13g8-not-an-address"}, w13g8PassiveEnv, nil, false, 1},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			env := scenario.envBuilder(t)
			for key, value := range scenario.patch {
				env[key] = value
			}
			code := w13g8SpawnAndAssertExit(t, env, scenario.args)
			if scenario.anyCode {
				return
			}
			if code != scenario.expectCode {
				t.Fatalf("场景 %s 退出码=%d want %d", scenario.name, code, scenario.expectCode)
			}
		})
	}
}

// w13g8SpawnAndAssertExit 拉起子进程并返回其退出码。
func w13g8SpawnAndAssertExit(t *testing.T, env map[string]string, args []string) int {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("定位测试二进制失败: %v", err)
	}
	gocoverdir := w13g8FindGocoverdir()
	childArgs := []string{"-test.run=^TestW13G8MainFailFastArms$", "-test.timeout=120s"}
	if gocoverdir != "" {
		childArgs = append(childArgs, "-test.gocoverdir="+gocoverdir)
	}
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, childArgs...)
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Env = append(cmd.Env, w13g8ChildMode+"=1", w13g8ChildArgs+"="+string(argsJSON))
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		t.Fatalf("子进程异常退出: %v\n%s", err, output.String())
		return -1
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("子进程未在限期内退出（场景未命中 fail 分支）:\n%s", output.String())
		return -1
	}
}

// TestW13G8HealthHandlerPartialDeps 覆盖 healthHandler 未显式提供 worker
// 依赖时的回落分支（worker 恒开，未提供的探针回落恒就绪）。
func TestW13G8HealthHandlerPartialDeps(t *testing.T) {
	runtimeRunning := &atomic.Bool{}
	runtimeRunning.Store(true)
	handler := healthHandler(healthDeps{
		OwnerMode:            ownermode.Active,
		RuntimeRunning:       runtimeRunning,
		AccountBalanceReady:  func() bool { return true },
		ProxyLatencyEnabled:  true,
		ProxyLatencyReady:    func() bool { return true },
		ProxyLatencyStatus:   func() proxylatency.RunnerStatus { return proxylatency.RunnerStatus{} },
		ProxyLatencySnapshot: func() (proxylatency.RunnerStatus, bool) { return proxylatency.RunnerStatus{}, true },
	})
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("/health 必须 200: %d", response.Code)
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	// worker 依赖缺失时 readiness 仍由恒开语义收口（workerEnabled=true 且
	// 默认探针就绪）。
	for key, want := range map[string]any{
		"workerEnabled": true,
		"ready":         true,
	} {
		if payload[key] != want {
			t.Fatalf("%s 必须为 %v: %v", key, want, payload[key])
		}
	}
}
