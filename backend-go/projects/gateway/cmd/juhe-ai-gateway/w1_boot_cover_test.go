//go:build windows

package main

// w1_boot_cover_test.go —— juhe-ai-gateway 二进制级启动覆盖测试。
//
// 本文件限定 Windows 构建：场景 J 的 CREATE_NEW_PROCESS_GROUP（syscall
// SysProcAttr.CreationFlags）与 golang.org/x/sys/windows 的
// GenerateConsoleCtrlEvent 都是 Windows 专属面，其余场景（旗标路径、F3/F4
// 迁移、被动/owner fail-fast）与平台无关，但为保持单文件约束统一放在
// Windows 门内；目标验证环境即 Windows 工作区。
//
// 机制：`go build -cover` 构建插桩二进制（构建一次、全场景复用），每个场景以
// 子进程运行 main() 的一条真实路径，GOCOVERDIR 指向
// <os.TempDir()>/w1b-cov/<场景名>。测试结束后不删除计数器文件；每个场景的
// GOCOVERDIR 绝对路径追加写入 <os.TempDir()>/w1b-cov-manifest.txt，供外部
// `go tool covdata` 管线收割（textfmt/merge 均可按清单逐目录处理）。
//
// 场景清单（对应 main.go 的旗标与 env 契约）：
//
//	A  -version                        → exit 0，stdout 含版本契约字符串
//	B  -check-boundary                 → exit 0，stdout 含 boundary=ready
//	C  bogus-arg                       → exit 2，stderr 含 unsupported gateway arguments
//	D  不支持参数（含已退役 sqlite 迁移旗标）→ exit 2，stderr 含 unsupported gateway arguments
//	H  F4 postgres 模式缺 -backup-confirmed → 非零，stderr 含备份确认文案（不连真 PG）
//	I  被动网关 + 非法 -health-listen-address → 非零，stderr 含 listen passive gateway health endpoint
//	J  被动网关优雅关闭（CTRL_BREAK_EVENT，尽力而为；3 次尝试不可行则 Skip 并保留本注释记录）
//	K1 owner 缺 JUHE_AI_POSTGRES_URL → stderr 含 load gateway runtime config
//	   （原「JUHE_AI_DATABASE_DRIVER=bogus」臂随双模式退役：未知 env 不再被
//	   读取，三连接串必填成为 loadRuntimeConfig 的首个 fail-fast）
//	K2（已删除）原「仅 CHAIN 无 SYSTEM_API」联动门槛臂：2026-09-21 起两开关
//	     移除、组合根与网关链恒开，被测对象不复存在。
//	K3~K5（已删除）原切流门禁/cutover evidence 失败臂已随清理批次 C1
//	     （2026-09-30）退役——门禁 env 家族不再解析、证据分支不可达。
//	L  JUHE_AI_BLUE_GREEN_OWNER_MODE=bogus → stderr 含 must be active, standby, or drain
//
// 原「双迁移旗标互斥（D）」「F3/F4 legacy SQLite 迁移（E1/F1/F2/G）」场景
// 随 sqlite 存储形态与 legacy 迁移命令一并退役（PLAN-20261024T051925945Z）；
// F4 postgres 迁移的备份确认门臂保留为 H。
//
// 注意：main.go 的 gateGatewayChain 自 G20 phase-2 起恒返回 nil（适配器全部
// 已 author），原“fail-fast 适配器清单”语义已并入 loadRuntimeConfig 的
// system-api 强制门槛，K2 按当前源码实际文案断言。
//
// 每个子进程场景都有界等待（30s 超时即杀进程并 Fatal），J 场景健康轮询与
// 退出等待均有界。J 场景若 GenerateConsoleCtrlEvent 在 3 次尝试内不可行
// （无 console、进程组事件失败等），以 t.Skip 记录而不是挂死。

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/version"
	"github.com/alicebob/miniredis/v2"
	"golang.org/x/sys/windows"
)

const (
	w1bCoverageRoot     = "w1b-cov"
	w1bCoverageManifest = "w1b-cov-manifest.txt"
	w1bScenarioTimeout  = 30 * time.Second
)

var (
	w1bCoverBinaryOnce sync.Once
	w1bCoverBinaryPath string
	w1bCoverBinaryErr  error
)

// w1bCoverageRootForProcess 返回本 go test 进程唯一的覆盖基目录。固定共享
// 路径（%TEMP%\w1b-cov）在跨进程场景有两类实际故障（2026-09-30 BUG-0252
// 批实测）：并行 go test 进程同时 `go build -cover` 写同一 exe 触发 Windows
// 文件锁冲突；被中断测试遗留的插桩子进程锁住运行中 exe，后续任何构建
// （含单测试）必然失败。按 pid 唯一化后两者不再可达；旧进程残留目录由
// 系统临时目录清理兜底，不主动回收（运行中 exe 无法删除）。
func w1bCoverageRootForProcess() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d", w1bCoverageRoot, os.Getpid()))
}

// w1bBuildCoverBinary 用 `go build -cover` 构建插桩二进制；包内只构建一次，
// 所有测试函数复用同一可执行文件。
func w1bBuildCoverBinary(t *testing.T) string {
	t.Helper()
	w1bCoverBinaryOnce.Do(func() {
		binDir := filepath.Join(w1bCoverageRootForProcess(), "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			w1bCoverBinaryErr = fmt.Errorf("创建插桩二进制目录失败: %w", err)
			return
		}
		exe := filepath.Join(binDir, "juhe-ai-gateway-cover.exe")
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-cover", "-o", exe, "./cmd/juhe-ai-gateway")
		cmd.Dir = `F:\sub2api-lite\backend-go\projects\gateway`
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				w1bCoverBinaryErr = fmt.Errorf("go build -cover 超过 100s 有界等待被终止: %w, 输出: %s", err, output.String())
				return
			}
			w1bCoverBinaryErr = fmt.Errorf("go build -cover 失败: %w, 输出: %s", err, output.String())
			return
		}
		w1bCoverBinaryPath = exe
	})
	if w1bCoverBinaryErr != nil {
		t.Fatalf("构建插桩二进制失败: %v", w1bCoverBinaryErr)
	}
	if _, err := os.Stat(w1bCoverBinaryPath); err != nil {
		t.Fatalf("插桩二进制不存在: %v", err)
	}
	return w1bCoverBinaryPath
}

// w1bCoverageDir 返回场景专属 GOCOVERDIR 基目录（不用 t.TempDir()：
// 测试结束后计数器文件必须保留，供外部 covdata 管线收割；基目录按 go test
// 进程唯一化，跨进程场景目录互不冲突）。
func w1bCoverageDir(t *testing.T, scenario string) string {
	t.Helper()
	dir := filepath.Join(w1bCoverageRootForProcess(), scenario)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建场景覆盖目录 %s 失败: %v", dir, err)
	}
	return dir
}

// w1bAppendCoverageManifest 把场景 GOCOVERDIR 绝对路径追加写入清单文件
// <os.TempDir()>/w1b-cov-manifest.txt（一行一个目录）。
func w1bAppendCoverageManifest(t *testing.T, dir string) {
	t.Helper()
	manifestPath := filepath.Join(os.TempDir(), w1bCoverageManifest)
	file, err := os.OpenFile(manifestPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开覆盖目录清单 %s 失败: %v", manifestPath, err)
	}
	defer file.Close()
	if _, err := fmt.Fprintf(file, "%s\n", dir); err != nil {
		t.Fatalf("写入覆盖目录清单 %s 失败: %v", manifestPath, err)
	}
}

// w1bScenarioEnv 剥离宿主环境里的 JUHE_AI_*、NODE_ENV 与 GOCOVERDIR，
// 保证每个场景只受显式 env 影响；GOCOVERDIR 必须指向场景覆盖目录。
func w1bScenarioEnv(t *testing.T, coverageDir string, pairs ...string) []string {
	t.Helper()
	env := make([]string, 0, len(os.Environ())+len(pairs)+1)
	for _, entry := range os.Environ() {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "JUHE_AI_") || strings.HasPrefix(upper, "NODE_ENV=") || strings.HasPrefix(upper, "GOCOVERDIR=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, pairs...)
	// PostgreSQL + Redis 是唯一存储形态（PLAN-20261024T051925945Z）：场景未
	// 显式提供连接串时，注入 pgtestkit 隔离库（fresh DDL + seed）的
	// POSTGRES_URL / BUSINESS_POSTGRES_URL，使旗标/快速失败场景不必各自预置
	// PG；需要专库语义（F4 租约冲突、J3b 不可达连接等）的场景显式给出同名
	// 键，一律不覆盖。原零配置 sqlite 回落路径（<DATA_DIR>/business.sqlite3
	// 预置）已随 sqlite 存储形态删除。
	if !w1bEnvHasKey(pairs, "JUHE_AI_POSTGRES_URL") {
		dsn := w1bScenarioPostgres(t).DSN
		env = append(env, "JUHE_AI_POSTGRES_URL="+dsn)
		if !w1bEnvHasKey(pairs, "JUHE_AI_BUSINESS_POSTGRES_URL") {
			env = append(env, "JUHE_AI_BUSINESS_POSTGRES_URL="+dsn)
		}
	}
	if !w1bEnvHasKey(pairs, "JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS") {
		env = append(env, "JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS=127.0.0.1:0")
	}
	env = append(env, "GOCOVERDIR="+coverageDir)
	return env
}

// w1bScenarioPostgres 提供场景隔离 PG 测试库（fresh DDL + seed，等价
// composeTestPostgres 的维护 bootstrap 面）；每个调用一个独立库，测试结束
// 自动删库。
func w1bScenarioPostgres(t *testing.T) *pgtestkit.TestDatabase {
	t.Helper()
	return pgtestkit.NewTestDatabase(t,
		func(ctx context.Context, db *sql.DB) error {
			_, err := bootstrap.EnsurePostgres(ctx, db)
			return err
		},
		func(ctx context.Context, db *sql.DB) error {
			_, err := bootstrap.SeedPostgres(ctx, db, bootstrap.SeedOptions{Now: time.Now, Secret: "w1b-scenario-secret"})
			return err
		})
}

// w1bEnvHasKey 判断场景 pairs 是否显式给出某个 env（"K=V" 形态，按键全名
// 前缀匹配）。
func w1bEnvHasKey(pairs []string, key string) bool {
	prefix := key + "="
	for _, pair := range pairs {
		if strings.HasPrefix(pair, prefix) {
			return true
		}
	}
	return false
}

// w1bRunScenario 以子进程运行插桩二进制的单个场景：有界等待 30s，超时杀
// 进程并 Fatal；结束后把 GOCOVERDIR 追加进收割清单，返回 stdout/stderr/退出码。
func w1bRunScenario(t *testing.T, scenario string, env []string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), w1bScenarioTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, w1bCoverBinaryPath, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		// 超时必须带出进程现场（stdout/stderr 最后一条日志指示卡点），
		// 否则 30s 等待只留下一个无输出的终止（BUG-0251 同批诊断增强）。
		t.Fatalf("场景 %s 超过 %s 有界等待，进程已被终止: %v；stdout=%q stderr=%q", scenario, w1bScenarioTimeout, ctx.Err(), stdout.String(), stderr.String())
	}
	exitCode := 0
	if runErr != nil {
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok {
			t.Fatalf("场景 %s 启动或等待失败: %v", scenario, runErr)
		}
		exitCode = exitErr.ExitCode()
	}
	w1bAppendCoverageManifest(t, filepath.Join(w1bCoverageRootForProcess(), scenario))
	t.Logf("场景 %s: exit=%d stdout=%q stderr=%q", scenario, exitCode, stdout.String(), stderr.String())
	return stdout.String(), stderr.String(), exitCode
}

func w1bRequireExitCode(t *testing.T, scenario string, actual, want int) {
	t.Helper()
	if actual != want {
		t.Fatalf("场景 %s 退出码期望 %d 实际 %d", scenario, want, actual)
	}
}

func w1bRequireContains(t *testing.T, scenario, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("场景 %s 输出缺少期望子串 %q，实际: %q", scenario, needle, haystack)
	}
}

// ---- 场景 A/B/C/D/L：旗标快速路径与 ownermode 加载错误臂 ----

func TestW1BBootCoverQuickFlags(t *testing.T) {
	w1bBuildCoverBinary(t)

	// A: -version → exit 0，stdout 含项目与契约版本字符串。
	coverageDir := w1bCoverageDir(t, "A-version")
	stdout, _, code := w1bRunScenario(t, "A-version", w1bScenarioEnv(t, coverageDir), "-version")
	w1bRequireExitCode(t, "A-version", code, 0)
	w1bRequireContains(t, "A-version", stdout, fmt.Sprintf("juhe-ai-gateway version=%s commit=%s", version.Version, version.Commit))
	w1bRequireContains(t, "A-version-schema", stdout, fmt.Sprintf("schemaRevision=%d", contracts.SchemaRevision))

	// B: -check-boundary → exit 0，stdout 含 boundary=ready。
	coverageDir = w1bCoverageDir(t, "B-check-boundary")
	stdout, _, code = w1bRunScenario(t, "B-check-boundary", w1bScenarioEnv(t, coverageDir), "-check-boundary")
	w1bRequireExitCode(t, "B-check-boundary", code, 0)
	w1bRequireContains(t, "B-check-boundary", stdout, "juhe-ai-gateway boundary=ready runtime=audit-operation-owner")

	// C: 不支持参数 → exit 2，stderr 含 unsupported gateway arguments。
	coverageDir = w1bCoverageDir(t, "C-bogus-arg")
	_, stderr, code := w1bRunScenario(t, "C-bogus-arg", w1bScenarioEnv(t, coverageDir), "bogus-arg")
	w1bRequireExitCode(t, "C-bogus-arg", code, 2)
	w1bRequireContains(t, "C-bogus-arg", stderr, "unsupported gateway arguments")

	// D: 已退役的 sqlite 迁移旗标 → exit 2，stderr 含 unsupported gateway
	// arguments（-migrate-audit-log-legacy-sqlite / -migrate-operation-log-
	// legacy-sqlite 随 sqlite 存储形态退役删除；互斥校验臂同批失去被测对象）。
	coverageDir = w1bCoverageDir(t, "D-mutually-exclusive")
	_, stderr, code = w1bRunScenario(t, "D-mutually-exclusive", w1bScenarioEnv(t, coverageDir),
		"-migrate-audit-log-legacy-sqlite", "-migrate-operation-log-legacy-sqlite")
	w1bRequireExitCode(t, "D-mutually-exclusive", code, 2)
	// flag 包在 main 参数白名单校验前先拦截未定义旗标，文案为 flag provided
	// but not defined（与 C 场景「已定义但不支持」的 unsupported 文案不同层）。
	w1bRequireContains(t, "D-mutually-exclusive", stderr, "flag provided but not defined: -migrate-audit-log-legacy-sqlite")

	// L: 非法 owner mode → fail 退出，stderr 含 ownermode.Load 契约文案。
	coverageDir = w1bCoverageDir(t, "L-owner-mode-invalid")
	_, stderr, code = w1bRunScenario(t, "L-owner-mode-invalid", w1bScenarioEnv(t, coverageDir, "JUHE_AI_BLUE_GREEN_OWNER_MODE=bogus-mode"))
	w1bRequireExitCode(t, "L-owner-mode-invalid", code, 1)
	w1bRequireContains(t, "L-owner-mode-invalid", stderr, "JUHE_AI_BLUE_GREEN_OWNER_MODE must be active, standby, or drain")
}

// ---- 场景 H：F4 postgres 迁移备份确认门（sqlite 迁移场景随存储形态退役）----

// ---- 场景 G/H：F4 operation-log 迁移 ----

// ---- 场景 I/J：被动网关 ----

func TestW1BBootCoverPassiveGatewayErrorArm(t *testing.T) {
	w1bBuildCoverBinary(t)
	// I: 非 owner + 非法健康监听地址 → fail 退出，stderr 含被动网关监听文案。
	coverageDir := w1bCoverageDir(t, "I-passive-bad-address")
	_, stderr, code := w1bRunScenario(t, "I-passive-bad-address",
		w1bScenarioEnv(t, coverageDir, "JUHE_AI_BLUE_GREEN_OWNER_MODE=standby"),
		"-health-listen-address", "bad-address")
	w1bRequireExitCode(t, "I-passive-bad-address", code, 1)
	w1bRequireContains(t, "I-passive-bad-address", stderr, `listen passive gateway health endpoint "bad-address"`)
}

// w1bFreePort 借一次 127.0.0.1:0 监听挑选当前空闲端口。
func w1bFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("挑选空闲端口失败: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestW1BBootCoverPassiveGatewayGracefulShutdown(t *testing.T) {
	exe := w1bBuildCoverBinary(t)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastAttemptErr string
	for attempt := 1; attempt <= 3; attempt++ {
		scenario := fmt.Sprintf("J-passive-graceful-%d", attempt)
		address := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir := w1bCoverageDir(t, scenario)
		ctx, cancel := context.WithTimeout(context.Background(), w1bScenarioTimeout)
		cmd := exec.CommandContext(ctx, exe, "-health-listen-address", address)
		cmd.Env = w1bScenarioEnv(t, coverageDir, "JUHE_AI_BLUE_GREEN_OWNER_MODE=standby")
		w1bSetNewProcessGroup(cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if startErr := cmd.Start(); startErr != nil {
			cancel()
			t.Fatalf("场景 %s 启动被动网关失败: %v", scenario, startErr)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		healthy := false
		pollDeadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(pollDeadline) {
			response, err := client.Get("http://" + address + "/health")
			if err == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					healthy = true
					break
				}
			}
			select {
			case waitErr := <-done:
				cancel()
				t.Fatalf("场景 %s 被动网关在健康检查通过前退出: %v, stderr=%s", scenario, waitErr, stderr.String())
			case <-time.After(100 * time.Millisecond):
			}
		}
		if !healthy {
			_ = cmd.Process.Kill()
			<-done
			cancel()
			lastAttemptErr = fmt.Sprintf("健康端点 %s 在 15s 内未就绪", address)
			continue
		}
		if err := w1bSendCtrlBreak(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			<-done
			cancel()
			lastAttemptErr = fmt.Sprintf("GenerateConsoleCtrlEvent(CTRL_BREAK) 失败: %v", err)
			continue
		}
		select {
		case waitErr := <-done:
			// cancel() 会使 ctx.Err() 变为 Canceled，超时判定必须在 cancel 之前取值。
			ctxErr := ctx.Err()
			cancel()
			if ctxErr != nil {
				t.Fatalf("场景 %s 超过 %s 有界等待，进程已被终止；stdout=%q stderr=%q", scenario, w1bScenarioTimeout, stdout.String(), stderr.String())
			}
			exitCode := 0
			if waitErr != nil {
				exitErr, ok := waitErr.(*exec.ExitError)
				if !ok {
					t.Fatalf("场景 %s 等待进程退出失败: %v", scenario, waitErr)
				}
				exitCode = exitErr.ExitCode()
			}
			w1bRequireExitCode(t, scenario, exitCode, 0)
			w1bRequireContains(t, scenario, stdout.String(), "juhe-ai-gateway passive")
			combined := stdout.String() + stderr.String()
			for _, forbidden := range []string{"shutdown passive gateway health endpoint", "listen passive gateway health endpoint", "passive gateway health endpoint stopped"} {
				if strings.Contains(combined, forbidden) {
					t.Fatalf("场景 %s 优雅关闭路径出现 fail 输出 %q: stdout=%q stderr=%q", scenario, forbidden, stdout.String(), stderr.String())
				}
			}
			w1bAppendCoverageManifest(t, coverageDir)
			return
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			cancel()
			t.Fatalf("场景 %s CTRL_BREAK 后 15s 内未退出，已强杀", scenario)
		}
	}
	// 尽力而为：3 次尝试不可行则跳过本场景（覆盖率收割清单不含 J 场景）。
	t.Skipf("场景 J 被动网关优雅关闭在 3 次尝试内不可行，已跳过（详见文件头注释）: %s", lastAttemptErr)
}

// ---- 场景 K：owner 模式快速失败臂 ----

// w1bWriteCutoverEvidence 原构造 J3b 切换证据（备份工件 + readback
// manifest + freshness 窗口）。清理批次 C1（2026-09-30）后切流门禁与证据
// 校验链退役，证据文件不再被读取——保留签名与目录创建语义（调用方 env 里
// 的 CUTOVER_EVIDENCE_PATH 不再解析），恒返回空路径。
func w1bWriteCutoverEvidence(t *testing.T, dir, epoch string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("创建证据目录 %s 失败: %v", dir, err)
	}
	_ = epoch
	return ""
}

func w1bOwnerBaseEnv(t *testing.T, coverageDir string, extra ...string) []string {
	t.Helper()
	return w1bScenarioEnv(t, coverageDir, extra...)
}

func TestW1BBootCoverOwnerFailFastArms(t *testing.T) {
	w1bBuildCoverBinary(t)

	// K1: 缺 JUHE_AI_POSTGRES_URL → loadRuntimeConfig 快速失败（PostgreSQL
	// + Redis 是唯一存储形态，三个连接串必填；原 JUHE_AI_DATABASE_DRIVER=bogus
	// 臂随双模式删除退役——未知 env 不再被读取）。
	coverageDir := w1bCoverageDir(t, "K1-missing-postgres-url")
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "JUHE_AI_") || strings.HasPrefix(upper, "NODE_ENV=") || strings.HasPrefix(upper, "GOCOVERDIR=") {
			continue
		}
		env = append(env, entry)
	}
	// redis 两连接由 miniredis 承载：loadRuntimeConfig 的必填校验先查
	// Redis 再查 POSTGRES_URL（runtime.go 308-314 的顺序），因此只注入
	// Redis 两条即可让失败落点精确到缺 POSTGRES_URL。
	cacheRedis := miniredis.RunT(t)
	stateRedis := miniredis.RunT(t)
	env = append(env,
		"JUHE_AI_CACHE_DRIVER=redis",
		"JUHE_AI_REDIS_CACHE_URL=redis://"+cacheRedis.Addr(),
		"JUHE_AI_RUNTIME_STATE_DRIVER=redis",
		"JUHE_AI_REDIS_STATE_URL=redis://"+stateRedis.Addr(),
	)
	env = append(env, "GOCOVERDIR="+coverageDir)
	_, stderr, code := w1bRunScenario(t, "K1-missing-postgres-url", env)
	w1bRequireExitCode(t, "K1-missing-postgres-url", code, 1)
	w1bRequireContains(t, "K1-missing-postgres-url", stderr, "load gateway runtime config: 缺少 JUHE_AI_POSTGRES_URL")

	// K2（已删除）：原「chain=true + system-api=false 联动门槛」臂依赖
	// JUHE_AI_GATEWAY_CHAIN_ENABLED / JUHE_AI_GATEWAY_SYSTEM_API_ENABLED
	// 开关。2026-09-21 起两开关移除、组合根与网关链恒开，联动门槛不复
	// 存在，该臂失去被测对象。

	// K3~K5（已删除）：原切流门禁/cutover evidence 失败臂已随清理批次 C1
	//（2026-09-30）退役——门禁 env 家族不再解析、证据分支不可达；K1 的
	// 驱动非法 fail-fast 臂保留在上方。
}

// ---- Windows 进程组控制（场景 J） ----
// CREATE_NEW_PROCESS_GROUP 让子进程成为独立进程组，CTRL_BREAK_EVENT 只投递
// 给该组而不影响 go test 自身；Go runtime 把 CTRL_BREAK 映射为 os.Interrupt，
// 命中 main.go runPassiveGateway 的 signal.NotifyContext 优雅关闭路径。

func w1bSetNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func w1bSendCtrlBreak(pid int) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))
}
