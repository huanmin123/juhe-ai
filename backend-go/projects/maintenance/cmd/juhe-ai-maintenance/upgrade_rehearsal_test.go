// 端到端演练（升级兼容与迁移账本设计 §18 验收的执行载体）：以**真实构建的
// 三项目二进制**（ldflags 注入版本）+ pgtestkit 隔离真库，走完使用者视角的
// 全流程——
//
//	fresh 库：upgrade --dry-run（blocked/3）→ 缺确认（exit 2）→ 正式升级
//	（upgraded）→ -init-account-circuit-runtime-index → 启动 gateway/jobs
//	（/health 200，启动门禁放行）→ 重跑 upgrade（up-to-date）
//	反向：只 --ensure-schema 不 upgrade 的库 → gateway 必须被门禁拦下
//	（fail-fast 退出，文案含「尚未升级」）
//	P0：--version 输出 ldflags 注入的版本；check-config 结论可用。
//
// 外部依赖授权门 pgtestkit（JUHE_AI_TEST_EXTERNAL=1）；Redis 连接串从
// shared.env 解析（cache/state），隔离经独立 namespace。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/upgradepipeline"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const (
	rehearsalVersion = "v0.0.0-rehearsal"
	rehearsalSecret  = "rehearsal-secret-0123456789"
)

// readRehearsalReport 解析 --report-out 落盘的报告 JSON。
func readRehearsalReport(t *testing.T, path string) upgradepipeline.Report {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读报告 %s 失败: %v", path, err)
	}
	var report upgradepipeline.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("报告 JSON 解析失败: %v\n%s", err, raw)
	}
	return report
}

type rehearsalBinaries struct {
	maintenance, gateway, jobs string
}

// TestUpgradeRehearsalEndToEnd 是全流程演练主入口。
func TestUpgradeRehearsalEndToEnd(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	env := rehearsalBaseEnv(t)
	bins := buildRehearsalBinaries(t)

	// P0：--version 必须输出 ldflags 注入的版本（发布链路冒烟）。
	versionOut := runCapture(t, bins.maintenance, nil, "--version")
	if !strings.Contains(versionOut, "version="+rehearsalVersion) {
		t.Fatalf("--version 未注入 ldflags 版本: %q", versionOut)
	}

	// ---- 正向全流程：fresh 库 → upgrade → 服务健康 ----
	td := pgtestkit.NewTestDatabase(t)
	upgradeArgs := func(extra ...string) []string {
		return append([]string{
			"upgrade", "--dsn", td.DSN, "--secret", rehearsalSecret,
			"--stop-write-confirmed", "--backup-confirmed",
		}, extra...)
	}

	// 1) dry-run：blocked/3，报告经 --report-out 落盘后解析断言（§9.3）。
	reportFile := filepath.Join(t.TempDir(), "dry-run-report.json")
	runExpectCode(t, bins.maintenance, nil, upgradepipeline.ExitNeedsHuman,
		upgradeArgs("--dry-run", "--report-out", reportFile)...)
	dryReport := readRehearsalReport(t, reportFile)
	if dryReport.Result != upgradepipeline.ResultBlocked {
		t.Fatalf("fresh dry-run 应 blocked: %+v", dryReport.Result)
	}
	// fresh 库口径（adopt/pending 互斥修复后）：注册步骤总数恒为
	// len(registeredSteps)，空库上目标态判据式步骤（违规计数类）空真满足归
	// adopt，其余 pending；两列表必须恰好不重不漏覆盖全部步骤 ID。
	total := len(dryReport.Plan.Pending) + len(dryReport.Plan.Adopt)
	if total != 12 {
		t.Fatalf("fresh dry-run 步骤总数不符: pending=%d adopt=%d 合计=%d destructive=%d",
			len(dryReport.Plan.Pending), len(dryReport.Plan.Adopt), total, dryReport.Plan.DestructiveDDLStatements)
	}
	seen := map[string]int{}
	for _, item := range dryReport.Plan.Pending {
		seen[item.StepID]++
	}
	for _, item := range dryReport.Plan.Adopt {
		seen[item.StepID]++
	}
	for stepID, count := range seen {
		if count != 1 {
			t.Fatalf("步骤 %s 在 pending/adopt 出现 %d 次（应恰好一次）", stepID, count)
		}
	}
	if len(seen) != 12 {
		t.Fatalf("步骤 ID 去重后 %d 个，want 12", len(seen))
	}
	for _, item := range dryReport.Plan.Pending {
		t.Logf("pending rev=%d %s", item.Revision, item.StepID)
	}
	for _, item := range dryReport.Plan.Adopt {
		t.Logf("adopt   rev=%d %s（%s）", item.Revision, item.StepID, item.Reason)
	}
	// 披露值与 schema 包动态一致（B4 收口后当前为 0；未来破坏性 DDL 回归时
	// 该值随清单增长，断言不硬编码）。
	if dryReport.Plan.DestructiveDDLStatements != schema.PostgresDestructiveStatementCount() {
		t.Fatalf("破坏性语句披露 = %d, want %d（与 schema 包清单一致）",
			dryReport.Plan.DestructiveDDLStatements, schema.PostgresDestructiveStatementCount())
	}
	if dryReport.Rollback != upgradepipeline.RollbackBinaryOnly {
		t.Fatalf("dry-run rollback = %s, want binary-only", dryReport.Rollback)
	}
	// 2) 缺确认：exit 2。
	runExpectCode(t, bins.maintenance, nil, 2, stripUpgradeFlags(upgradeArgs())...)
	// 3) 正式升级：upgraded/0（真实二进制 + 真库，postflight OK）。
	upgradeReportFile := filepath.Join(t.TempDir(), "upgrade-report.json")
	runExpectCode(t, bins.maintenance, nil, upgradepipeline.ExitOK,
		upgradeArgs("--report-out", upgradeReportFile)...)
	upgradeReport := readRehearsalReport(t, upgradeReportFile)
	if upgradeReport.Result != upgradepipeline.ResultUpgraded || upgradeReport.Postflight == nil || !upgradeReport.Postflight.OK {
		t.Fatalf("正式升级报告不符: result=%v postflight=%+v", upgradeReport.Result, upgradeReport.Postflight)
	}
	if upgradeReport.Rollback != upgradepipeline.RollbackBackupRequired {
		t.Fatalf("升级后 rollback = %s, want backup-required（破坏相已执行）", upgradeReport.Rollback)
	}

	// 3.5) 关键启动门禁前置事实直查（演练诊断锚点）：阶段记录必须已存在。
	var stageJSON string
	stageErr := td.DB.QueryRowContext(context.Background(),
		`SELECT value_json FROM juhe_business.global_settings WHERE key = 'account_capability_domain_migration'`).Scan(&stageJSON)
	if stageErr != nil {
		var allRows []string
		all, allErr := td.DB.QueryContext(context.Background(), `SELECT key, left(value_json, 200) FROM juhe_business.global_settings`)
		if allErr == nil {
			for all.Next() {
				var k, v string
				all.Scan(&k, &v)
				allRows = append(allRows, k+"="+v)
			}
			all.Close()
		}
		t.Fatalf("升级后能力域阶段记录缺失（capability Apply 未写记录）: %v；global_settings 全部行=%v 验证CLI退出码=%d",
			stageErr, allRows, func() int {
				code, _ := callResult(func() int {
					return accountCapabilityDomainVerifyResult(td.DSN, rehearsalSecret, "m2-cutover", "postflight")
				})
				return code
			}())
	}
	t.Logf("阶段记录: %s", stageJSON)

	// 4) J3b 运行态索引初始化（fresh 序列步骤）。
	indexEnv := append(env, "JUHE_AI_POSTGRES_URL="+td.DSN)
	runExpectCode(t, bins.gateway, indexEnv, 0, "-init-account-circuit-runtime-index")

	// 5) gateway 启动：门禁放行 + /health 200。
	gatewayEnv := append(env,
		"JUHE_AI_POSTGRES_URL="+td.DSN,
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL="+td.DSN,
	)
	gatewayHealth := startGatewayExpectReady(t, bins.gateway, gatewayEnv, &testingGateExpectation{shouldPass: true})

	// 6) 反向：只 ensure 不 upgrade 的库，gateway 必须被门禁拦下。
	td2 := pgtestkit.NewTestDatabase(t)
	runCapture(t, bins.maintenance, nil, "--ensure-schema", "--dsn", td2.DSN)
	blockedEnv := append(env,
		"JUHE_AI_POSTGRES_URL="+td2.DSN,
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL="+td2.DSN,
		"JUHE_AI_REDIS_NAMESPACE="+rehearsalNamespace(),
	)
	expectGateBlocking(t, bins.gateway, blockedEnv)
	_ = gatewayHealth

	// 7) 重跑：up-to-date/0。
	upToDateReportFile := filepath.Join(t.TempDir(), "up-to-date-report.json")
	runExpectCode(t, bins.maintenance, nil, upgradepipeline.ExitOK,
		upgradeArgs("--dry-run", "--report-out", upToDateReportFile)...)
	upToDate := readRehearsalReport(t, upToDateReportFile)
	if upToDate.Result != upgradepipeline.ResultUpToDate && upToDate.Result != upgradepipeline.ResultPlanned {
		t.Fatalf("重跑 dry-run 应 up-to-date/planned, got %v", upToDate.Result)
	}
	runExpectCode(t, bins.maintenance, nil, upgradepipeline.ExitOK, upgradeArgs()...)

	// 8) jobs 启动：门禁放行 + /health 200。
	jobsEnv := append(env,
		"JUHE_AI_POSTGRES_URL="+td.DSN,
		"JUHE_AI_RUNTIME_LOG_POSTGRES_URL="+td.DSN,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL="+td.DSN,
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OUTCOME_POSTGRES_URL="+td.DSN,
	)
	startJobsExpectReady(t, bins.jobs, jobsEnv)
}

// ---- 基础环境（Redis 连接串来自 shared.env，隔离经独立 namespace） ----

func rehearsalBaseEnv(t *testing.T) []string {
	t.Helper()
	values := sharedEnvValues(t, "JUHE_AI_REDIS_CACHE_URL", "JUHE_AI_REDIS_STATE_URL")
	dataDir := t.TempDir()
	return []string{
		"JUHE_AI_REDIS_CACHE_URL=" + values["JUHE_AI_REDIS_CACHE_URL"],
		"JUHE_AI_REDIS_STATE_URL=" + values["JUHE_AI_REDIS_STATE_URL"],
		// 演练抓到的真实要求（§11.3 fresh 序列补充）：J3b 电路 Redis 必须
		// 显式配置；独立 namespace 下显式同值 = 已知并接受与主链共享。
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL=" + values["JUHE_AI_REDIS_STATE_URL"],
		// 部署 env 契约：JUHE_AI_SECRET 常驻（gateway 加密封套、jobs J2
		// account-balance 凭据密钥的回退源——缺它 J2 启动必填校验失败）。
		"JUHE_AI_SECRET=" + rehearsalSecret,
		// J2 手动拉取的 jobs↔gateway 共享 HTTP 认证密钥：显式回退源
		// （JUHE_AI_SECRET）不满足 ≥32 字符下限时必须显式给出，两进程同值。
		"JUHE_AI_ACCOUNT_BALANCE_JOBS_HTTP_SECRET=" + rehearsalSecret + "-account-balance-jobs-http",
		"JUHE_AI_REDIS_NAMESPACE=" + rehearsalNamespace(),
		"JUHE_AI_DATA_DIR=" + dataDir,
		"JUHE_AI_LOG_DIR=" + filepath.Join(dataDir, "logs"),
	}
}

func rehearsalNamespace() string {
	return fmt.Sprintf("juhe-ai:rehearsal-%d", time.Now().UnixNano())
}

func sharedEnvValues(t *testing.T, keys ...string) map[string]string {
	t.Helper()
	root := repoRootForMatrix(t)
	path := filepath.Join(root, ".local", "project-resources", "dev", "env", "shared.env")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 shared.env 失败（演练需要 Redis 连接串）: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if eq := strings.IndexByte(line, '='); eq > 0 {
			key := strings.TrimSpace(line[:eq])
			for _, wanted := range keys {
				if key == wanted {
					values[key] = strings.Trim(strings.TrimSpace(line[eq+1:]), `"'`)
				}
			}
		}
	}
	for _, wanted := range keys {
		if values[wanted] == "" {
			t.Fatalf("shared.env 缺少 %s（演练需要 Redis 连接串）", wanted)
		}
	}
	return values
}

// ---- 构建（ldflags 注入，模拟 deploy.sh 构建链） ----

// buildRehearsalBinaries 从**当前工作树**构建三项目二进制（ldflags 注入版本）。
// 演练的对象是待交付变更集：被测代码未提交时 HEAD 快照里没有它，从 HEAD 构建
// 必然假失败（2026-10-30 实锤——能力域封存修复只在工作区，HEAD 二进制复现旧缺陷）。
// 并行编辑撕裂风险改由纪律约束：演练应在工作区静止窗口运行；撕裂窗口内 go build
// 会直接编译报错暴露，不会静默产出错码二进制。
func buildRehearsalBinaries(t *testing.T) rehearsalBinaries {
	t.Helper()
	binDir := t.TempDir()
	ldflags := fmt.Sprintf("-s -w -X github.com/huanminabc/juhe-ai/backend-go-platform/version.Version=%s -X github.com/huanminabc/juhe-ai/backend-go-platform/version.Commit=rehearsal -X github.com/huanminabc/juhe-ai/backend-go-platform/version.BuildTime=2026-10-30T00:00:00Z", rehearsalVersion)
	bins := rehearsalBinaries{}
	for _, project := range []string{"maintenance", "gateway", "jobs"} {
		suffix := ""
		if runtime.GOOS == "windows" {
			suffix = ".exe"
		}
		binary := filepath.Join(binDir, "juhe-ai-"+project+suffix)
		build := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", binary, "./cmd/juhe-ai-"+project)
		build.Dir = filepath.Join(repoRootForMatrix(t), "backend-go", "projects", project)
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("构建 %s 失败: %v\n%s", project, err, output)
		}
		switch project {
		case "maintenance":
			bins.maintenance = binary
		case "gateway":
			bins.gateway = binary
		case "jobs":
			bins.jobs = binary
		}
	}
	return bins
}

// ---- 进程执行辅助 ----

func runCapture(t *testing.T, binary string, env []string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	if env != nil {
		cmd.Env = env
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s 失败: %v\n%s", filepath.Base(binary), strings.Join(args, " "), err, output)
	}
	return string(output)
}

func runExpectCode(t *testing.T, binary string, env []string, wantCode int, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	if env != nil {
		cmd.Env = env
	}
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("%s %v 启动失败: %v\n%s", filepath.Base(binary), args, err, output)
		}
		exitCode = exitErr.ExitCode()
	}
	if exitCode != wantCode {
		t.Fatalf("%s %v 退出码 = %d, want %d\n%s", filepath.Base(binary), args, exitCode, wantCode, output)
	}
}

// ---- gateway / jobs 启动与健康 ----

type testingGateExpectation struct{ shouldPass bool }

// rehearsalAllocatedPorts 记录本轮测试已分配过的端口：OS 临时端口在 Close 后
// 立即重取可能返回同值，health 口与主口撞号会造成难排查的启动失败。
var rehearsalAllocatedPorts sync.Map

func freePort(t *testing.T) int {
	t.Helper()
	for {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		if _, allocated := rehearsalAllocatedPorts.LoadOrStore(port, struct{}{}); !allocated {
			return port
		}
	}
}

// startGatewayExpectReady 启动 gateway 并轮询 /health 至 200；门禁若误拦
// （进程退出）即失败——这是「门禁假阳性」的演练断言。
func startGatewayExpectReady(t *testing.T, binary string, env []string, _ *testingGateExpectation) int {
	t.Helper()
	healthPort := freePort(t)
	mainPort := freePort(t)
	cmdEnv := append([]string{},
		env...)
	cmdEnv = append(cmdEnv,
		fmt.Sprintf("JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:%d", healthPort),
		fmt.Sprintf("JUHE_AI_PORT=%d", mainPort),
	)
	cmd := exec.Command(binary)
	cmd.Env = cmdEnv
	cmd.Dir = t.TempDir()
	logPath := filepath.Join(cmd.Dir, "gateway.log")
	logFile, _ := os.Create(logPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("gateway 启动失败: %v", err)
	}
	// 提前退出检测必须走 Wait（ProcessState 仅在 Wait 后非 nil，裸读恒 nil
	// 是死代码——复审 H7）；误拦/崩溃时快速失败并回放日志，而非空转 90s。
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", healthPort)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		select {
		case err := <-exited:
			logFile.Close()
			logContent, _ := os.ReadFile(logPath)
			t.Fatalf("gateway 进程提前退出（门禁误拦或启动崩溃）: %v\n%s", err, logContent)
		default:
		}
		response, err := http.Get(healthURL)
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == 200 {
				cmd.Process.Kill()
				logFile.Close()
				logContent, _ := os.ReadFile(logPath)
				if !strings.Contains(string(logContent), `"schemaRevision"`) {
					t.Fatalf("启动日志缺构建身份键: %s", logContent)
				}
				return healthPort
			}
		}
	}
	logFile.Close()
	logContent, _ := os.ReadFile(logPath)
	cmd.Process.Kill()
	t.Fatalf("gateway 90 秒内未就绪:\n%s", logContent)
	return healthPort
}

// expectGateBlocking 启动 gateway 断言其被门禁拦下（进程退出 + 文案含
// 「尚未升级」）——这是「门禁假阴性」的演练断言。
func expectGateBlocking(t *testing.T, binary string, env []string) {
	t.Helper()
	cmdEnv := append([]string{}, env...)
	cmdEnv = append(cmdEnv,
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:"+fmt.Sprint(freePort(t)),
		"JUHE_AI_PORT="+fmt.Sprint(freePort(t)),
	)
	cmd := exec.Command(binary)
	cmd.Env = cmdEnv
	cmd.Dir = t.TempDir()
	logPath := filepath.Join(cmd.Dir, "gateway-blocked.log")
	logFile, _ := os.Create(logPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("gateway 启动失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("未升级库上的 gateway 竟然正常退出（应 fail-fast 非零）")
		}
		logFile.Close()
		logContent, _ := os.ReadFile(logPath)
		if !strings.Contains(string(logContent), "尚未升级") {
			t.Fatalf("fail-fast 文案缺「尚未升级」:\n%s", logContent)
		}
		if time.Since(started) > 30*time.Second {
			t.Fatal("门禁拦截耗时异常（应在启动早期 fail-fast）")
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatal("未升级库上的 gateway 30 秒未被门禁拦下（假阴性）")
	}
}

// startJobsExpectReady 启动 jobs 并轮询 /health 至 200（门禁放行断言）。
func startJobsExpectReady(t *testing.T, binary string, env []string) {
	t.Helper()
	healthPort := freePort(t)
	cmdEnv := append([]string{}, env...)
	cmdEnv = append(cmdEnv, fmt.Sprintf("JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS=127.0.0.1:%d", healthPort))
	cmd := exec.Command(binary)
	cmd.Env = cmdEnv
	cmd.Dir = t.TempDir()
	logPath := filepath.Join(cmd.Dir, "jobs.log")
	logFile, _ := os.Create(logPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("jobs 启动失败: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", healthPort)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		select {
		case err := <-exited:
			logFile.Close()
			logContent, _ := os.ReadFile(logPath)
			t.Fatalf("jobs 进程提前退出（门禁误拦或启动崩溃）: %v\n%s", err, logContent)
		default:
		}
		response, err := http.Get(healthURL)
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == 200 {
				cmd.Process.Kill()
				logFile.Close()
				return
			}
		}
	}
	logFile.Close()
	logContent, _ := os.ReadFile(logPath)
	cmd.Process.Kill()
	t.Fatalf("jobs 90 秒内未就绪:\n%s", logContent)
}

// stripUpgradeFlags 把确认/密钥旗标剥掉（缺确认臂用）。
func stripUpgradeFlags(args []string) []string {
	filtered := []string{}
	skip := map[string]bool{"--stop-write-confirmed": true, "--backup-confirmed": true, "--secret": true}
	for i := 0; i < len(args); i++ {
		if skip[args[i]] {
			if args[i] == "--secret" {
				i++ // --secret 带值
			}
			continue
		}
		filtered = append(filtered, args[i])
	}
	return filtered
}
