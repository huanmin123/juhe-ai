//go:build windows

package main

// w1_boot_arms_test.go —— juhe-ai-gateway 二进制级启动深臂场景（w1m 前缀，
// TestW1M 入口）。复用 w1_boot_cover_test.go 的插桩二进制与覆盖收割帮手
// （w1bBuildCoverBinary / w1bCoverageDir / w1bScenarioEnv / w1bRunScenario /
// w1bAppendCoverageManifest / w1bSendCtrlBreak）。
//
// PostgreSQL + Redis 是唯一存储形态（PLAN-20261024T051925945Z）：F3/F4/业务
// 库全部走 PG 连接串（隔离 pgtestkit 库或不可达 URL），文件面只剩 blob/hot
// 搜索目录。（原 SQLite F3/F4 专库租约、垃圾文件打开臂与 STORE=bogus 配置
// 臂随存储形态退役删除；有 PG 对应失败模式的按 PG 臂重写，D9。）
//
// 场景清单（对应 main.go 的剩余快速失败臂与常驻 owner 生命周期）：
//
//	M1 owner + F4 专有租约生命周期（PG 共享库语义不变）：
//		- 进程 1：system api 组合根 + F4 启用 → F4 组件走专有租约分支，
//		  健康端点就绪；
//		- 进程 2：独立 F3 审计库 + 复用进程 1 的 F4 PG 库 + 换实例 ID →
//		  F4 专有租约被持有，supervisor 对组件失败按有界退避无限重试，
//		  stderr 持续出现 "F4 operation log owner lease held by another
//		  owner process"，随后有界终止进程 2 收割覆盖；
//		- 进程 3：复用进程 1 的 F3 审计 PG 库 + 换实例 ID → F3 租约被持有
//		  快速失败；
//		- 进程 1 CTRL_BREAK 优雅关闭。
//	M2 配置快速失败臂：F3 EnsureSchema（不可达 URL）、F4 EnsureSchema（不可达
//		URL，PG store 打开惰性连接后首次连库的确定性失败面）、owner 侧健康
//		监听地址非法。（原 STORE=bogus 配置臂、sqlite
//		垃圾文件臂与 F4 迁移命令臂随存储形态/命令退役删除。）
//	M3 J3b 臂：J3b LoadConfig 失败、J3b postgres 模式业务库连接失败、
//		J3b 电路运行时 Redis 不可达。（原 sqlite 业务库空文件 schema 臂的
//		PG 对应物由「postgres 模式业务库连接失败」臂承载。）
//
// 每个子进程均有界等待（30s 超时即杀并 Fatal）；覆盖目录按场景名追加进
// w1b-cov-manifest.txt 供外部 covdata 管线收割。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// w1mEvidenceEpoch 是 M3 场景共用的 owner epoch；证据文件的 epoch 必须
// 与 env 中声明的 epoch 一致才能通过校验。
const w1mEvidenceEpoch = "epoch-w1m"

// w1mUnreachablePostgresURL 是确定性的不可达 PG URL（端口 1 拒绝连接），
// 顶替原「垃圾 SQLite 文件」打开失败臂的 PG 对应失败模式。
const w1mUnreachablePostgresURL = "postgres://127.0.0.1:1/w1m_unused"

// w1mAuditRoot 为一个 owner 场景准备独立根目录：F3 审计隔离所需的 blob/hot
// 目录与 chat-assets 根。返回根目录路径；调用方把各路径填进 env。
func w1mAuditRoot(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "w1m-"+name)
	for _, dir := range []string{
		filepath.Join(root, "audit-blobs"),
		filepath.Join(root, "audit-hot"),
		filepath.Join(root, "chat-assets"),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("创建场景目录 %s 失败: %v", dir, err)
		}
	}
	return root
}

// w1mRuntimeBaseEnvPairs 返回 owner 子进程启动必需的 runtime 基线 env：
// 三个连接串中的 Redis 两个由 miniredis 承载（JUHE_AI_POSTGRES_URL 由
// w1bScenarioEnv 在场景未显式给出时自动注入 pgtestkit 隔离库），加 SECRET。
func w1mRuntimeBaseEnvPairs(t *testing.T) []string {
	t.Helper()
	cacheRedis := miniredis.RunT(t)
	stateRedis := miniredis.RunT(t)
	// J3b 账户电路运行时要求显式声明 JUHE_AI_J3B_CIRCUIT_REDIS_URL（回退自
	// 主链 state URL 会被 ValidateCircuitRuntimeRedisIsolation fail-fast）；
	// 测试里用第三个独立 miniredis 承载，语义与生产「独立 URL」一致，实例
	// 由 t.Cleanup 关闭（晚于子进程 kill 的清理顺序）。
	circuitRedis := miniredis.RunT(t)
	// CheckReady 契约要求 circuit Redis 预置 runtime-index-meta（网关不自建，
	// 见 w1wNewOwnerFixture 的 seedMeta 语义），就绪路径场景必须先种子。
	w1g2SeedCircuitRuntimeIndex(t, "redis://"+circuitRedis.Addr(), "juhe-ai:w1m")
	return []string{
		"JUHE_AI_CACHE_DRIVER=redis",
		"JUHE_AI_REDIS_CACHE_URL=redis://" + cacheRedis.Addr(),
		"JUHE_AI_RUNTIME_STATE_DRIVER=redis",
		"JUHE_AI_REDIS_STATE_URL=redis://" + stateRedis.Addr(),
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://" + circuitRedis.Addr(),
		"JUHE_AI_REDIS_NAMESPACE=juhe-ai:w1m",
		"JUHE_AI_SECRET=w1m-owner-arms-secret-0123456789abcdef",
		"NODE_ENV=test",
	}
}

// w1mMainChainRedisPairs 返回主链 cache/state 两条 miniredis 连接串（2026-10-24
// 存储收口后 loadRuntimeConfig 对三连接串无条件必填；J3b 电路连接由场景按自身
// 失败臂语义显式给值，不由本帮手注入）。实例由 t.Cleanup 关闭。
func w1mMainChainRedisPairs(t *testing.T) []string {
	t.Helper()
	cacheRedis := miniredis.RunT(t)
	stateRedis := miniredis.RunT(t)
	return []string{
		"JUHE_AI_REDIS_CACHE_URL=redis://" + cacheRedis.Addr(),
		"JUHE_AI_REDIS_STATE_URL=redis://" + stateRedis.Addr(),
		"JUHE_AI_REDIS_NAMESPACE=juhe-ai:w1m",
	}
}

// w1mAuditEnvPairs 返回 F3 审计常驻启动所需的完整 env（owner 模式下审计
// 总是先于其他组件启动，是所有 owner 场景的公共前置）。auditURL 是 F3 的
// PG 连接串；businessURL 是 F3 只读业务设置镜像的业务库（部署契约允许镜像
// 指向业务库本体）。
func w1mAuditEnvPairs(t *testing.T, root, instanceID, auditURL, businessURL string) []string {
	t.Helper()
	return []string{
		"JUHE_AI_AUDIT_LOG_POSTGRES_URL=" + auditURL,
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL=" + businessURL,
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=" + filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=" + filepath.Join(root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=" + instanceID,
	}
}

// w1mF4EnvPairs 返回 F4 常驻 owner 所需的 env；f4URL / instanceID 由调用方
// 显式给值，供「复用同一 F4 PG 库」的租约冲突场景精确控制。
func w1mF4EnvPairs(t *testing.T, f4URL, instanceID string) []string {
	t.Helper()
	return []string{
		"JUHE_AI_OPERATION_LOG_POSTGRES_URL=" + f4URL,
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=" + instanceID,
	}
}

// w1mBusinessPostgres 提供 owner 场景的业务 PG 库（fresh DDL + seed：compose
// preflight 的能力域形状校验、阶段记录门禁与调度偏好四臂全部需要种子面）。
// J3B_POSTGRES_URL 未显式配置时回落业务库，J3b 装配的 schema 门禁要求
// juhe_j3b 面就绪；maintenance bootstrap 不含 j3b schema（独立
// j3bmodelcheck 面，gateway 从不自建），这里按同源 DDL 幂等补建。
func w1mBusinessPostgres(t *testing.T) *pgtestkit.TestDatabase {
	t.Helper()
	db := w1v2PrepareBusinessPostgres(t)
	w1g2EnsureJ3bSchema(t, db.DB)
	return db
}

// w1mSyncBuffer 是并发安全的 stdout/stderr 收集缓冲（子进程与测试 goroutine
// 并发读写 bytes.Buffer 会产生数据竞争）。
type w1mSyncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *w1mSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *w1mSyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func w1mStartOwnerProcess(t *testing.T, coverageDir string, env []string, args ...string) (*exec.Cmd, <-chan error, context.CancelFunc, *w1mSyncBuffer, *w1mSyncBuffer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	cmd := exec.CommandContext(ctx, w1bBuildCoverBinary(t), args...)
	cmd.Env = env
	stdout := &w1mSyncBuffer{}
	stderr := &w1mSyncBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	w1bSetNewProcessGroup(cmd)
	if startErr := cmd.Start(); startErr != nil {
		cancel()
		t.Fatalf("启动 owner 进程失败: %v", startErr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		cancel()
	})
	return cmd, done, cancel, stdout, stderr
}

// w1mWaitOutputContains 有界等待子进程输出出现目标子串（main 的 slog
// JSON 日志走 stdout，supervisor 组件失败重试的 cause 字段也在其中）。
func w1mWaitOutputContains(t *testing.T, buffer *w1mSyncBuffer, needle string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(buffer.String(), needle) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// w1mTryWaitHealthReady 有界轮询健康端点直至 ready=true（owner 全部组件已
// 启动并持有租约），超时返回 false。
func w1mTryWaitHealthReady(t *testing.T, client *http.Client, address string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + address + "/health")
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				var payload struct {
					Ready bool `json:"ready"`
				}
				if json.Unmarshal(body, &payload) == nil && payload.Ready {
					return true
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func w1mWaitHealthReady(t *testing.T, client *http.Client, address string) {
	if !w1mTryWaitHealthReady(t, client, address, 20*time.Second) {
		t.Fatalf("健康端点 %s 在 20s 内未就绪", address)
	}
}

// w1mGracefulShutdownOwner 投递 CTRL_BREAK 并有界等待进程 0 退出；覆盖
// 目录追加进收割清单。
func w1mGracefulShutdownOwner(t *testing.T, cmd *exec.Cmd, done <-chan error, cancel context.CancelFunc, coverageDir, scenario string) {
	t.Helper()
	if cmd.Process == nil {
		t.Fatalf("场景 %s 进程句柄缺失", scenario)
	}
	if err := w1bSendCtrlBreak(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("场景 %s GenerateConsoleCtrlEvent(CTRL_BREAK) 失败: %v", scenario, err)
	}
	select {
	case waitErr := <-done:
		cancel()
		exitCode := 0
		if waitErr != nil {
			exitErr, ok := waitErr.(*exec.ExitError)
			if !ok {
				t.Fatalf("场景 %s 等待进程退出失败: %v", scenario, waitErr)
			}
			exitCode = exitErr.ExitCode()
		}
		w1bRequireExitCode(t, scenario, exitCode, 0)
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		cancel()
		t.Fatalf("场景 %s CTRL_BREAK 后 15s 内未退出，已强杀", scenario)
	}
	w1bAppendCoverageManifest(t, coverageDir)
}

// ---------------------------------------------------------------------------
// M1. owner + F4 专有租约生命周期（system api 组合根恒开）
// ---------------------------------------------------------------------------

func TestW1MBootF4OwnerPrivateLeaseLifecycle(t *testing.T) {
	w1bBuildCoverBinary(t)
	client := &http.Client{Timeout: 2 * time.Second}

	// 共享 PG 库：业务库（settings/compose preflight 消费）与 F4 专有租约库。
	business := w1mBusinessPostgres(t)
	f4 := pgtestkit.NewTestDatabase(t)

	// 进程 1：F4 启用 + 独立 F3 审计库 → F4 组件走专有租约分支。
	root1 := w1mAuditRoot(t, "m1-owner1")
	audit1 := pgtestkit.NewTestDatabase(t)
	healthAddr := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
	coverageDir1 := w1bCoverageDir(t, "M1-owner-f4-private-lease")
	env1 := w1bScenarioEnv(t, coverageDir1, append(append(
		w1mRuntimeBaseEnvPairs(t),
		w1mAuditEnvPairs(t, root1, "w1m-m1-audit-1", audit1.DSN, business.DSN)...,
	), w1mF4EnvPairs(t, f4.DSN, "w1m-f4")...)...)
	env1 = append(env1,
		"JUHE_AI_POSTGRES_URL="+business.DSN,
		"JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN,
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr,
	)
	// F4 专有租约分支成为唯一路径，进程照常只暴露 gateway health 监听。
	cmd1, done1, cancel1, bootStdout1, bootStderr1 := w1mStartOwnerProcess(t, coverageDir1, env1)
	if !w1mTryWaitHealthReady(t, client, healthAddr, 20*time.Second) {
		t.Fatalf("健康端点 %s 在 20s 内未就绪\n--- 子进程 stdout ---\n%s\n--- 子进程 stderr ---\n%s",
			healthAddr, bootStdout1.String(), bootStderr1.String())
	}

	// 健康端点契约：GET /health 就绪、POST /health 404、
	// GET /__aisys__/metrics 命中指标分支。
	if response, err := client.Get("http://" + healthAddr + "/health"); err != nil {
		t.Fatalf("GET /health 失败: %v", err)
	} else {
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET /health 状态码 = %d，want 200", response.StatusCode)
		}
	}
	if response, err := client.Post("http://"+healthAddr+"/health", "application/json", strings.NewReader("{}")); err != nil {
		t.Fatalf("POST /health 失败: %v", err)
	} else {
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("POST /health 状态码 = %d，want 404", response.StatusCode)
		}
	}
	if response, err := client.Get("http://" + healthAddr + "/__aisys__/metrics"); err != nil {
		t.Fatalf("GET /__aisys__/metrics 失败: %v", err)
	} else {
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET /__aisys__/metrics 状态码 = %d，want 200", response.StatusCode)
		}
	}

	// 进程 2：独立 F3 审计库 + 复用进程 1 的 F4 PG 库，但换一个 F4 实例 ID →
	// F4 专有租约被跨 owner 持有。同 owner_id 的未过期行会被直接接管
	// （operationlog store 的 owner_id = excluded.owner_id 分支），只有跨
	// owner 才走冲突文案。契约：supervisor 对组件失败按有界退避无限重试
	// 而非进程停机，因此 F4 owner Run 闭包的专有租约 !ok 分支表现为
	// stderr 持续出现租约冲突文案；断言后终止进程 2 以收割其覆盖数据。
	root2 := w1mAuditRoot(t, "m1-owner2")
	audit2 := pgtestkit.NewTestDatabase(t)
	coverageDir2 := w1bCoverageDir(t, "M1-f4-lease-conflict")
	env2 := w1bScenarioEnv(t, coverageDir2, append(append(
		w1mRuntimeBaseEnvPairs(t),
		w1mAuditEnvPairs(t, root2, "w1m-m1-audit-2", audit2.DSN, business.DSN)...,
	), w1mF4EnvPairs(t, f4.DSN, "w1m-f4-rival")...)...)
	env2 = append(env2,
		"JUHE_AI_POSTGRES_URL="+business.DSN,
		"JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN,
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:"+strconv.Itoa(w1bFreePort(t)),
	)
	cmd2, done2, cancel2, stdout2, stderr2 := w1mStartOwnerProcess(t, coverageDir2, env2)
	// 组合根恒开后租约冲突文案进入 stderr，等待目标从 stdout 改为 stderr。
	if !w1mWaitOutputContains(t, stderr2, "F4 operation log owner lease held by another owner process", 15*time.Second) {
		cancel2()
		_ = cmd2.Process.Kill()
		<-done2
		t.Fatalf("进程 2 的 F4 专有租约冲突未在 15s 内出现，stdout: %s stderr: %s", stdout2.String(), stderr2.String())
	}
	// P2 的 F4 supervisor 有界重试可能在投递 CTRL_BREAK 之前就自行终结进程
	// （GenerateConsoleCtrlEvent 对已退出进程报「参数不正确」），故发送失败
	// 按「已退出」处理。稳定契约：进程有界终止（自退或关停），stderr 已含
	// 租约冲突重试文案，退出码 0（健康关停）/1（组件不健康上报）均合法。
	_ = w1bSendCtrlBreak(cmd2.Process.Pid)
	select {
	case waitErr := <-done2:
		cancel2()
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); !ok {
				t.Fatalf("场景 M1-f4-lease-conflict 等待进程退出失败: %v", waitErr)
			} else if code := exitErr.ExitCode(); code != 0 && code != 1 {
				t.Fatalf("场景 M1-f4-lease-conflict 退出码 %d 超出健康关停（0）与组件不健康上报（1）范围", code)
			}
		}
	case <-time.After(15 * time.Second):
		_ = cmd2.Process.Kill()
		cancel2()
		t.Fatalf("场景 M1-f4-lease-conflict 15s 内未终止，已强杀")
	}
	w1bAppendCoverageManifest(t, coverageDir2)

	// 进程 3：复用进程 1 的 F3 审计 PG 库 + 换一个 audit 实例 ID → F3 租约被
	// 跨 owner 持有快速失败。实例 ID 必须与进程 1 不同：F3 获取的冲突分支
	// 对同 owner_id（进程身份）的未过期租约直接接管（store.go
	// AcquireOwnerLease 的 owner_id = excluded.owner_id 分支），只有跨 owner
	// 才走 fail-fast 原文案。health 监听显式用空闲端口。
	coverageDir3 := w1bCoverageDir(t, "M1-f3-lease-conflict")
	env3 := w1bScenarioEnv(t, coverageDir3, append(append(
		w1mRuntimeBaseEnvPairs(t),
		w1mAuditEnvPairs(t, root1, "w1m-m1-audit-1-rival", audit1.DSN, business.DSN)...,
	), "JUHE_AI_POSTGRES_URL="+business.DSN, "JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN)...)
	env3 = append(env3, "JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:"+strconv.Itoa(w1bFreePort(t)))
	_, stderr3, code3 := w1bRunScenario(t, "M1-f3-lease-conflict", env3)
	w1bRequireExitCode(t, "M1-f3-lease-conflict", code3, 1)
	w1bRequireContains(t, "M1-f3-lease-conflict", stderr3, "F3 audit owner lease held by another owner process")

	// 进程 1 优雅关闭（CTRL_BREAK）。
	w1mGracefulShutdownOwner(t, cmd1, done1, cancel1, coverageDir1, "M1-owner-f4-private-lease")
}

// ---------------------------------------------------------------------------
// M2. 配置快速失败臂（F3/F4 OpenStore 不可达 URL、owner 侧健康监听地址非法；
// 原 STORE=bogus 配置臂、sqlite 垃圾文件臂与 F4 迁移命令臂随存储形态/命令
// 退役删除）
// ---------------------------------------------------------------------------

func TestW1MBootConfigFastFailArms(t *testing.T) {
	w1bBuildCoverBinary(t)

	t.Run("F3审计store打开不可达URL失败", func(t *testing.T) {
		// PG 形态下 OpenStore/池 Acquire 均惰性连接，不可达 F3 URL 的确定性
		// 失败面是 EnsureSchema 首次连库（与 W1W-f3-schema-fail 同语义）。
		// 业务库显式给值：J3b 装配先于 F3 打开且回落业务库校验 juhe_j3b 面。
		business := w1mBusinessPostgres(t)
		root := w1mAuditRoot(t, "m2-f3-open")
		coverageDir := w1bCoverageDir(t, "M2-f3-open-unreachable")
		env := w1bScenarioEnv(t, coverageDir, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root, "w1m-m2-b", w1mUnreachablePostgresURL, w1mUnreachablePostgresURL)...,
		), "JUHE_AI_POSTGRES_URL="+business.DSN, "JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN)...)
		_, stderr, code := w1bRunScenario(t, "M2-f3-open-unreachable", env)
		w1bRequireExitCode(t, "M2-f3-open-unreachable", code, 1)
		w1bRequireContains(t, "M2-f3-open-unreachable", stderr, "initialize F3 audit-log schema")
	})

	t.Run("F4操作日志store打开不可达URL失败", func(t *testing.T) {
		root := w1mAuditRoot(t, "m2-f4-open")
		audit := pgtestkit.NewTestDatabase(t)
		business := w1mBusinessPostgres(t)
		coverageDir := w1bCoverageDir(t, "M2-f4-open-unreachable")
		env := w1bScenarioEnv(t, coverageDir, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root, "w1m-m2-d", audit.DSN, business.DSN)...,
		), w1mF4EnvPairs(t, w1mUnreachablePostgresURL, "w1m-f4")...)...)
		env = append(env,
			"JUHE_AI_POSTGRES_URL="+business.DSN,
			"JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN,
		)
		_, stderr, code := w1bRunScenario(t, "M2-f4-open-unreachable", env)
		w1bRequireExitCode(t, "M2-f4-open-unreachable", code, 1)
		// OpenStore 惰性连接后，不可达 F4 URL 的确定性失败面是 EnsureSchema。
		w1bRequireContains(t, "M2-f4-open-unreachable", stderr, "initialize F4 operation-log schema")
	})

	t.Run("owner侧健康监听地址非法", func(t *testing.T) {
		root := w1mAuditRoot(t, "m2-listen")
		audit := pgtestkit.NewTestDatabase(t)
		f4 := pgtestkit.NewTestDatabase(t)
		business := w1mBusinessPostgres(t)
		coverageDir := w1bCoverageDir(t, "M2-owner-bad-listen")
		env := w1bScenarioEnv(t, coverageDir, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root, "w1m-m2-e", audit.DSN, business.DSN)...,
		), w1mF4EnvPairs(t, f4.DSN, "w1m-f4")...)...)
		env = append(env,
			"JUHE_AI_POSTGRES_URL="+business.DSN,
			"JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN,
		)
		_, stderr, code := w1bRunScenario(t, "M2-owner-bad-listen", env, "-health-listen-address", "127.0.0.1:99999")
		w1bRequireExitCode(t, "M2-owner-bad-listen", code, 1)
		w1bRequireContains(t, "M2-owner-bad-listen", stderr, `listen gateway health endpoint "127.0.0.1:99999"`)
	})
}

// ---------------------------------------------------------------------------
// M3. J3b 臂（LoadConfig / postgres 业务库连接 / 电路运行时 Redis 不可达；
// 原 sqlite 业务库空文件 schema 臂由 postgres 连接失败臂承载，D9）
// ---------------------------------------------------------------------------

func TestW1MBootEvidenceAndJ3bArms(t *testing.T) {
	w1bBuildCoverBinary(t)

	t.Run("J3b配置非法", func(t *testing.T) {
		coverageDir := w1bCoverageDir(t, "M3-j3b-config-bogus")
		// 主链三连接串必填（PG+Redis 唯一形态）；补齐后失败落点精确到
		// J3b LoadConfig 的 owner 校验臂。
		_, stderr, code := w1bRunScenario(t, "M3-j3b-config-bogus", w1bOwnerBaseEnv(t, coverageDir,
			append(w1mMainChainRedisPairs(t),
				"JUHE_AI_J3B_ENABLED=true",
				"JUHE_AI_J3B_OWNER=notgateway")...))
		w1bRequireExitCode(t, "M3-j3b-config-bogus", code, 1)
		w1bRequireContains(t, "M3-j3b-config-bogus", stderr, "load J3b gateway owner config")
	})

	t.Run("J3bpostgres模式映射与业务库连接失败", func(t *testing.T) {
		// STORE=postgres 命中 main.go 的 Postgres 模式映射臂；业务 PG URL
		// 指向不可达地址（127.0.0.1:1），schema 校验连接即失败，不连真 PG。
		coverageDir := w1bCoverageDir(t, "M3-j3b-pg-business-unreachable")
		env := w1bScenarioEnv(t, coverageDir,
			append(w1mMainChainRedisPairs(t),
				"JUHE_AI_J3B_ENABLED=true",
				"JUHE_AI_J3B_OWNER=gateway",
				"JUHE_AI_J3B_INSTANCE_ID=w1m-j3b",
				"JUHE_AI_J3B_STORE=postgres",
				"JUHE_AI_J3B_POSTGRES_URL="+w1mUnreachablePostgresURL,
				"JUHE_AI_J3B_BUSINESS_POSTGRES_URL="+w1mUnreachablePostgresURL,
				"JUHE_AI_J3B_CREDENTIAL_SECRET=w1m-j3b-credential-secret",
				"JUHE_AI_J3B_IDENTITY_SECRET=w1m-j3b-identity-secret",
				"JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://127.0.0.1:1/0",
				"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE=juhe-ai:w1m-test")...)
		_, stderr, code := w1bRunScenario(t, "M3-j3b-pg-business-unreachable", env)
		w1bRequireExitCode(t, "M3-j3b-pg-business-unreachable", code, 1)
		w1bRequireContains(t, "M3-j3b-pg-business-unreachable", stderr, "open J3b Business owner connection")
	})

	t.Run("J3b电路运行时Redis不可达", func(t *testing.T) {
		// 业务库用 fresh DDL 种子库，让 J3b 业务库契约校验推进到电路运行时
		// Redis Ping 失败臂。J3b 专属库 schema 由 w1g2EnsureJ3bSchema 补建。
		business := w1mBusinessPostgres(t)
		j3bDB := pgtestkit.NewTestDatabase(t)
		w1g2EnsureJ3bSchema(t, j3bDB.DB)
		coverageDir := w1bCoverageDir(t, "M3-j3b-redis-unreachable")
		env := w1bScenarioEnv(t, coverageDir,
			append(w1mMainChainRedisPairs(t),
				"JUHE_AI_POSTGRES_URL="+business.DSN,
				"JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN,
				"JUHE_AI_J3B_ENABLED=true",
				"JUHE_AI_J3B_OWNER=gateway",
				"JUHE_AI_J3B_INSTANCE_ID=w1m-j3b",
				"JUHE_AI_J3B_STORE=postgres",
				"JUHE_AI_J3B_POSTGRES_URL="+j3bDB.DSN,
				"JUHE_AI_J3B_BUSINESS_POSTGRES_URL="+business.DSN,
				"JUHE_AI_J3B_CREDENTIAL_SECRET=w1m-j3b-credential-secret",
				"JUHE_AI_J3B_IDENTITY_SECRET=w1m-j3b-identity-secret",
				"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED=true",
				"JUHE_AI_J3B_NODE_WRITER_STOPPED=true",
				"JUHE_AI_J3B_SCHEMA_READY=true",
				"JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://127.0.0.1:1/0",
				"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE=juhe-ai:w1m-test")...)
		_, stderr, code := w1bRunScenario(t, "M3-j3b-redis-unreachable", env)
		w1bRequireExitCode(t, "M3-j3b-redis-unreachable", code, 1)
		w1bRequireContains(t, "M3-j3b-redis-unreachable", stderr, "ping J3b Gateway circuit runtime Redis")
	})
}

// ---------------------------------------------------------------------------
// M4. system api 组合根 env（全量组装 + 优雅关闭 / 组合失败 / 端口冲突场景
// 与 w1_owner_acquire_wait 共用）
// ---------------------------------------------------------------------------

// w1mSystemAPIEnvPairs 返回 system api 组合根全量启动 env（PG 业务库 +
// owner 门槛事实）。模型检测 owner 常驻且装配先于组合根，业务库必须带
// fresh DDL + 种子（能力域/调度偏好 preflight）。
func w1mSystemAPIEnvPairs(t *testing.T, root, businessPostgresURL string, mainPort int) []string {
	t.Helper()
	return []string{
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true",
		"JUHE_AI_POSTGRES_URL=" + businessPostgresURL,
		"JUHE_AI_BUSINESS_POSTGRES_URL=" + businessPostgresURL,
		"JUHE_AI_SECRET=w1m-system-api-secret-0123456789abcdef",
		"JUHE_AI_HOST=127.0.0.1",
		fmt.Sprintf("JUHE_AI_PORT=%d", mainPort),
		"JUHE_AI_CHAT_ASSETS_ROOT=" + filepath.Join(root, "chat-assets"),
	}
}
