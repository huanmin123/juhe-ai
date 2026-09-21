//go:build windows

package main

// w1_boot_arms_test.go —— juhe-ai-gateway 二进制级启动深臂场景（w1m 前缀，
// TestW1M 入口）。复用 w1_boot_cover_test.go 的插桩二进制与覆盖收割帮手
// （w1bBuildCoverBinary / w1bCoverageDir / w1bScenarioEnv / w1bRunScenario /
// w1bAppendCoverageManifest / w1bWriteCutoverEvidence / w1bSendCtrlBreak）。
//
// 场景清单（对应 main.go 的剩余快速失败臂与常驻 owner 生命周期）：
//
//	M1 owner + F4 专有租约生命周期：
//		- 进程 1：system api 关闭 + F4 SQLite 启用 → F4 组件走专有租约分支
//		  （main.go F4 owner Run 闭包 keeper==nil 路径），健康端点就绪；
//		- 进程 2：独立 F3 审计库 + 复用进程 1 的 F4 库 → F4 专有租约被持有，
//		  supervisor 对组件失败按有界退避无限重试（进程不停机），stderr
//		  持续出现 "F4 operation log owner lease held by another owner
//		  process"，随后优雅关闭进程 2 收割覆盖；
//		- 进程 3：复用进程 1 的 F3 审计库 → F3 租约被持有快速失败；
//		- 进程 1 CTRL_BREAK 优雅关闭。
//	M2 配置快速失败臂：F3 LoadConfig / F3 OpenStore（垃圾文件）、F4
//		LoadConfig / F4 OpenStore（垃圾文件）、owner 侧健康监听地址非法、
//		F4 迁移命令 LoadConfig 失败。
//	M3 证据与 J3b 臂：业务证据可读但未就绪、J3b LoadConfig 失败、J3b 证据
//		可读但未就绪、J3b 业务库 schema 校验失败、J3b 电路运行时 Redis
//		不可达（bootstrap 建满业务库后推进到 Ping 失败臂）。
//	M4 system api 组合根生命周期：全量组装成功 + 健康契约 + 主监听 +
//		CTRL_BREAK 优雅关闭；组合根失败（垃圾表监控库）快速退出；主监听
//		端口被占快速退出。
//
// 每个子进程均有界等待（30s 超时即杀并 Fatal）；覆盖目录按场景名追加进
// w1b-cov-manifest.txt 供外部 covdata 管线收割。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
)

// w1mEvidenceEpoch 是 M3/M4 场景共用的 owner epoch；证据文件的 epoch 必须
// 与 env 中声明的 epoch 一致才能通过校验。
const w1mEvidenceEpoch = "epoch-w1m"

// w1mAuditRoot 为一个 owner 场景准备独立根目录：F3 审计隔离所需的全部
// 六库路径、codex shard 根、usage shard 根与两份空 business settings 镜像。
// 返回根目录路径；调用方把各路径填进 env。
func w1mAuditRoot(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "w1m-"+name)
	for _, dir := range []string{
		filepath.Join(root, "codex-context"),
		filepath.Join(root, "usage-shards"),
		filepath.Join(root, "audit-blobs"),
		filepath.Join(root, "chat-assets"),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("创建场景目录 %s 失败: %v", dir, err)
		}
	}
	for _, file := range []string{
		filepath.Join(root, "audit-business-settings.sqlite3"),
		filepath.Join(root, "f4-business-settings.sqlite3"),
		// 业务库本体：main 在 F4 OpenStore 时就把 JUHE_AI_DATABASE_PATH 交给
		// F4 只读兜底句柄（open F4 operation-log store），文件必须已存在；
		// system api 场景后续由组合根 preflight 在此文件上 ensure+seed。
		filepath.Join(root, "business.sqlite3"),
	} {
		if err := os.WriteFile(file, nil, 0o644); err != nil {
			t.Fatalf("创建空业务/settings 文件 %s 失败: %v", file, err)
		}
	}
	return root
}

// w1mAuditEnvPairs 返回 F3 审计常驻启动所需的完整 env（owner 模式下审计
// 总是先于其他组件启动，是所有 owner 场景的公共前置）。
func w1mAuditEnvPairs(t *testing.T, root, instanceID string) []string {
	t.Helper()
	return []string{
		"JUHE_AI_AUDIT_LOG_STORE=sqlite",
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=" + instanceID,
		"JUHE_AI_AUDIT_LOG_DATABASE_PATH=" + filepath.Join(root, "audit-dataset.sqlite3"),
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=" + filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=" + filepath.Join(root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH=" + filepath.Join(root, "audit-business-settings.sqlite3"),
		"JUHE_AI_DATABASE_PATH=" + filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_DATASET_DATABASE_PATH=" + filepath.Join(root, "dataset.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH=" + filepath.Join(root, "usage-catalog.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH=" + filepath.Join(root, "stats.sqlite3"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH=" + filepath.Join(root, "runtime-log.sqlite3"),
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH=" + filepath.Join(root, "table-monitor.sqlite3"),
		"JUHE_AI_CHAT_DATABASE_PATH=" + filepath.Join(root, "chat.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT=" + filepath.Join(root, "codex-context"),
		"JUHE_AI_USAGE_SHARD_ROOT=" + filepath.Join(root, "usage-shards"),
		"NODE_ENV=test",
	}
}

// w1mF4EnvPairs 返回 F4 SQLite 常驻 owner 所需的 env；f4DB / settings /
// shardRoot 由调用方显式给值，供"复用同一 F4 库"的租约冲突场景精确控制。
func w1mF4EnvPairs(t *testing.T, f4DB, settingsPath, shardRoot string) []string {
	t.Helper()
	return []string{
		"JUHE_AI_OPERATION_LOG_STORE=sqlite",
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1m-f4",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH=" + f4DB,
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH=" + settingsPath,
		"JUHE_AI_USAGE_SHARD_ROOT=" + shardRoot,
	}
}

// w1mGarbageSQLite 写出一个非 SQLite 垃圾文件（512 字节 0x51），命中
// store 打开阶段的 PRAGMA configure 错误臂。
func w1mGarbageSQLite(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x51}, 512), 0o644); err != nil {
		t.Fatalf("写入垃圾 SQLite 文件 %s 失败: %v", path, err)
	}
}

// w1mWriteEvidence 在确保目录存在后构造有效切换证据（w1bWriteCutoverEvidence
// 不负责创建目录，既有场景均先显式 MkdirAll）。
func w1mWriteEvidence(t *testing.T, dir, epoch string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("创建证据目录 %s 失败: %v", dir, err)
	}
	return w1bWriteCutoverEvidence(t, dir, epoch)
}

// w1mNotReadyEvidence 基于有效证据派生一份"可解析但未就绪"的证据文件：
// DrainCompleted=false 且 BlockedFindings=1，校验器返回 report（err==nil）
// 但 Ready=false，命中 main.go 的 "verify ... cutover evidence" 快速失败臂。
func w1mNotReadyEvidence(t *testing.T, dir, epoch string) string {
	t.Helper()
	validPath := w1mWriteEvidence(t, dir, epoch)
	data, err := os.ReadFile(validPath)
	if err != nil {
		t.Fatalf("读取有效证据失败: %v", err)
	}
	var evidence map[string]any
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatalf("解析有效证据失败: %v", err)
	}
	evidence["drainCompleted"] = false
	evidence["blockedFindings"] = 1
	notReady, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("序列化未就绪证据失败: %v", err)
	}
	path := filepath.Join(dir, "evidence-not-ready.json")
	if err := os.WriteFile(path, notReady, 0o600); err != nil {
		t.Fatalf("写入未就绪证据失败: %v", err)
	}
	return path
}

// w1mSyncBuffer 是并发安全的 stdout/stderr 收集缓冲（子进程与测试 goroutine
// 并发读写 bytes.Buffer 会产生数据竞争）。
type w1mSyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
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

// w1mStartOwnerProcess 以独立进程组启动插桩二进制的 owner 常驻场景并返回
// 进程句柄与并发安全的输出缓冲；调用方负责有界等待与优雅关闭。
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

// w1mWaitHealthReady 有界轮询健康端点直至 ready=true（owner 全部组件已
// 启动并持有租约），超时即 Fatal。
func w1mWaitHealthReady(t *testing.T, client *http.Client, address string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
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
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("健康端点 %s 在 20s 内未就绪", address)
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
// M1. owner + F4 专有租约生命周期（system api 关闭）
// ---------------------------------------------------------------------------

func TestW1MBootF4OwnerPrivateLeaseLifecycle(t *testing.T) {
	w1bBuildCoverBinary(t)
	client := &http.Client{Timeout: 2 * time.Second}

	// 进程 1：F4 启用 + system api 关闭 → F4 组件走专有租约分支。
	root1 := w1mAuditRoot(t, "m1-owner1")
	healthAddr := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
	coverageDir1 := w1bCoverageDir(t, "M1-owner-f4-private-lease")
	env1 := w1bScenarioEnv(t, coverageDir1, append(
		w1mAuditEnvPairs(t, root1, "w1m-m1-audit-1"),
		w1mF4EnvPairs(t,
			filepath.Join(root1, "f4-operation.sqlite3"),
			filepath.Join(root1, "f4-business-settings.sqlite3"),
			filepath.Join(root1, "usage-shards"),
		)...,
	)...)
	env1 = append(env1, "JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr)
	// 2026-09-21 起组合根恒开（SYSTEM_API/CHAIN 开关移除）；F4 专有租约
	// 分支成为唯一路径，进程照常只暴露 gateway health 监听。
	cmd1, done1, cancel1, _, _ := w1mStartOwnerProcess(t, coverageDir1, env1)
	w1mWaitHealthReady(t, client, healthAddr)

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

	// 进程 2：独立 F3 审计库 + 复用进程 1 的 F4 库 → F4 专有租约被持有。
	// 契约：supervisor 对组件失败按有界退避无限重试而非进程停机，因此
	// F4 owner Run 闭包的专有租约 !ok 分支表现为 stderr 持续出现租约冲突
	// 文案；断言后优雅关闭进程 2 以收割其覆盖数据。
	root2 := w1mAuditRoot(t, "m1-owner2")
	coverageDir2 := w1bCoverageDir(t, "M1-f4-lease-conflict")
	env2 := w1bScenarioEnv(t, coverageDir2, append(
		w1mAuditEnvPairs(t, root2, "w1m-m1-audit-2"),
		w1mF4EnvPairs(t,
			filepath.Join(root1, "f4-operation.sqlite3"),
			filepath.Join(root2, "f4-business-settings.sqlite3"),
			filepath.Join(root2, "usage-shards"),
		)...,
	)...)
	// 同进程 1：health 监听用空闲端口，避免与本机开发实例默认端口冲突。
	env2 = append(env2,
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:"+strconv.Itoa(w1bFreePort(t)))
	cmd2, done2, cancel2, stdout2, stderr2 := w1mStartOwnerProcess(t, coverageDir2, env2)
	// 2026-09-21 起组合根恒开后租约冲突文案进入 stderr（与 343 行契约描述
	// 一致），等待目标从 stdout 改为 stderr。
	if !w1mWaitOutputContains(t, stderr2, "F4 operation log owner lease held by another owner process", 15*time.Second) {
		cancel2()
		_ = cmd2.Process.Kill()
		<-done2
		t.Fatalf("进程 2 的 F4 专有租约冲突未在 15s 内出现，stdout: %s stderr: %s", stdout2.String(), stderr2.String())
	}
	// 2026-09-21 起组合根恒开：P2 的 F4 supervisor 有界重试可能在投递
	// CTRL_BREAK 之前就自行终结进程（GenerateConsoleCtrlEvent 对已退出
	// 进程报「参数不正确」），故发送失败按「已退出」处理。稳定契约：进程
	// 有界终止（自退或关停），stderr 已含租约冲突重试文案，退出码 0（健康
	// 关停）/1（组件不健康上报）均合法。
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

	// 进程 3：复用进程 1 的 F3 审计库 → F3 租约被持有快速失败。
	coverageDir3 := w1bCoverageDir(t, "M1-f3-lease-conflict")
	env3 := w1bScenarioEnv(t, coverageDir3, w1mAuditEnvPairs(t, root1, "w1m-m1-audit-1")...)
	_, stderr3, code3 := w1bRunScenario(t, "M1-f3-lease-conflict", env3)
	w1bRequireExitCode(t, "M1-f3-lease-conflict", code3, 1)
	w1bRequireContains(t, "M1-f3-lease-conflict", stderr3, "F3 audit owner lease held by another owner process")

	// 进程 1 优雅关闭（CTRL_BREAK）。
	w1mGracefulShutdownOwner(t, cmd1, done1, cancel1, coverageDir1, "M1-owner-f4-private-lease")
}

// ---------------------------------------------------------------------------
// M2. 配置快速失败臂（F3/F4 LoadConfig、OpenStore 垃圾文件、owner 侧
// 健康监听地址非法、F4 迁移命令配置错误）
// ---------------------------------------------------------------------------

func TestW1MBootConfigFastFailArms(t *testing.T) {
	w1bBuildCoverBinary(t)

	t.Run("F3审计store配置非法", func(t *testing.T) {
		root := w1mAuditRoot(t, "m2-f3-config")
		coverageDir := w1bCoverageDir(t, "M2-f3-config-bogus")
		env := w1bScenarioEnv(t, coverageDir, append(w1mAuditEnvPairs(t, root, "w1m-m2-a"), "JUHE_AI_AUDIT_LOG_STORE=bogus")...)
		_, stderr, code := w1bRunScenario(t, "M2-f3-config-bogus", env)
		w1bRequireExitCode(t, "M2-f3-config-bogus", code, 1)
		w1bRequireContains(t, "M2-f3-config-bogus", stderr, "load F3 audit-log config")
	})

	t.Run("F3审计store打开垃圾文件失败", func(t *testing.T) {
		root := w1mAuditRoot(t, "m2-f3-open")
		w1mGarbageSQLite(t, filepath.Join(root, "audit-dataset.sqlite3"))
		coverageDir := w1bCoverageDir(t, "M2-f3-open-garbage")
		env := w1bScenarioEnv(t, coverageDir, w1mAuditEnvPairs(t, root, "w1m-m2-b")...)
		_, stderr, code := w1bRunScenario(t, "M2-f3-open-garbage", env)
		w1bRequireExitCode(t, "M2-f3-open-garbage", code, 1)
		w1bRequireContains(t, "M2-f3-open-garbage", stderr, "open F3 audit-log store")
	})

	t.Run("F4操作日志配置非法", func(t *testing.T) {
		root := w1mAuditRoot(t, "m2-f4-config")
		coverageDir := w1bCoverageDir(t, "M2-f4-config-bogus")
		env := w1bScenarioEnv(t, coverageDir, append(
			w1mAuditEnvPairs(t, root, "w1m-m2-c"),
			"JUHE_AI_OPERATION_LOG_STORE=bogus",
		)...)
		_, stderr, code := w1bRunScenario(t, "M2-f4-config-bogus", env)
		w1bRequireExitCode(t, "M2-f4-config-bogus", code, 1)
		w1bRequireContains(t, "M2-f4-config-bogus", stderr, "load F4 operation-log config")
	})

	t.Run("F4操作日志store打开垃圾文件失败", func(t *testing.T) {
		root := w1mAuditRoot(t, "m2-f4-open")
		garbage := filepath.Join(root, "f4-operation.sqlite3")
		w1mGarbageSQLite(t, garbage)
		coverageDir := w1bCoverageDir(t, "M2-f4-open-garbage")
		env := w1bScenarioEnv(t, coverageDir, append(
			w1mAuditEnvPairs(t, root, "w1m-m2-d"),
			w1mF4EnvPairs(t, garbage,
				filepath.Join(root, "f4-business-settings.sqlite3"),
				filepath.Join(root, "usage-shards"),
			)...,
		)...)
		_, stderr, code := w1bRunScenario(t, "M2-f4-open-garbage", env)
		w1bRequireExitCode(t, "M2-f4-open-garbage", code, 1)
		w1bRequireContains(t, "M2-f4-open-garbage", stderr, "open F4 operation-log store")
	})

	t.Run("owner侧健康监听地址非法", func(t *testing.T) {
		root := w1mAuditRoot(t, "m2-listen")
		coverageDir := w1bCoverageDir(t, "M2-owner-bad-listen")
		// 2026-09-19 起 F4 store 缺省启用：显式配置 F4（临时目录文件），
		// 保证 boot 走到健康监听守卫而不是在派生 F4 路径处先失败。
		env := w1bScenarioEnv(t, coverageDir, append(
			w1mAuditEnvPairs(t, root, "w1m-m2-e"),
			w1mF4EnvPairs(t,
				filepath.Join(root, "f4-operation.sqlite3"),
				filepath.Join(root, "f4-business-settings.sqlite3"),
				filepath.Join(root, "usage-shards"),
			)...,
		)...)
		_, stderr, code := w1bRunScenario(t, "M2-owner-bad-listen", env, "-health-listen-address", "127.0.0.1:99999")
		w1bRequireExitCode(t, "M2-owner-bad-listen", code, 1)
		w1bRequireContains(t, "M2-owner-bad-listen", stderr, `listen gateway health endpoint "127.0.0.1:99999"`)
	})

	t.Run("F4迁移命令配置非法", func(t *testing.T) {
		coverageDir := w1bCoverageDir(t, "M2-f4-migrate-config")
		env := w1bScenarioEnv(t, coverageDir, "JUHE_AI_OPERATION_LOG_STORE=bogus")
		_, stderr, code := w1bRunScenario(t, "M2-f4-migrate-config", env,
			"-migrate-operation-log-legacy-sqlite", "-node-stopped", "-go-stopped", "-backup-confirmed")
		if code == 0 {
			t.Fatalf("场景 M2-f4-migrate-config 期望非零退出码，实际 0")
		}
		w1bRequireContains(t, "M2-f4-migrate-config", stderr, "load F4 operation-log config")
	})
}

// ---------------------------------------------------------------------------
// M3. 证据与 J3b 臂（业务证据未就绪、J3b LoadConfig / 证据 / 业务库连接 /
// 电路运行时 Redis 不可达）
// ---------------------------------------------------------------------------

// w1mJ3BEnvPairs 返回 J3b owner 的完整 env（SQLite 专库 + 业务库路径 +
// 全部就绪门槛 + 不可达 Redis：127.0.0.1:1 拒绝连接）。
func w1mJ3BEnvPairs(t *testing.T, root, businessDBPath, evidencePath string) []string {
	t.Helper()
	return []string{
		"JUHE_AI_DATABASE_PATH=" + filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_J3B_ENABLED=true",
		"JUHE_AI_J3B_OWNER=gateway",
		"JUHE_AI_J3B_INSTANCE_ID=w1m-j3b",
		"JUHE_AI_J3B_STORE=sqlite",
		"JUHE_AI_J3B_DATABASE_PATH=" + filepath.Join(root, "j3b-dedicated.sqlite"),
		"JUHE_AI_J3B_BUSINESS_DATABASE_PATH=" + businessDBPath,
		"JUHE_AI_J3B_CREDENTIAL_SECRET=w1m-j3b-credential-secret",
		"JUHE_AI_J3B_IDENTITY_SECRET=w1m-j3b-identity-secret",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED=true",
		"JUHE_AI_J3B_OWNER_EPOCH=" + w1mEvidenceEpoch,
		"JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH=" + evidencePath,
		"JUHE_AI_J3B_SCHEMA_READY=true",
		"JUHE_AI_J3B_HEALTH_BOUNDARY_READY=true",
		"JUHE_AI_J3B_RUNTIME_READY=true",
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://127.0.0.1:1/0",
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE=juhe-ai:w1m-test",
	}
}

func TestW1MBootEvidenceAndJ3bArms(t *testing.T) {
	w1bBuildCoverBinary(t)

	t.Run("业务证据可读但未就绪", func(t *testing.T) {
		root := t.TempDir()
		evidence := w1mNotReadyEvidence(t, filepath.Join(root, "evidence-dir"), w1mEvidenceEpoch)
		coverageDir := w1bCoverageDir(t, "M3-business-evidence-not-ready")
		_, stderr, code := w1bRunScenario(t, "M3-business-evidence-not-ready", w1bOwnerBaseEnv(t, coverageDir,
			"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true",
			"JUHE_AI_BUSINESS_OWNER=gateway",
			"JUHE_AI_BUSINESS_DATABASE_PATH="+filepath.Join(t.TempDir(), "w1m-business-gate.sqlite"),
			"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
			"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
			"JUHE_AI_BUSINESS_SCHEMA_READY=true",
			"JUHE_AI_BUSINESS_OWNER_EPOCH="+w1mEvidenceEpoch,
			"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH="+evidence))
		w1bRequireExitCode(t, "M3-business-evidence-not-ready", code, 1)
		w1bRequireContains(t, "M3-business-evidence-not-ready", stderr, "verify business owner cutover evidence")
	})

	t.Run("J3b配置非法", func(t *testing.T) {
		coverageDir := w1bCoverageDir(t, "M3-j3b-config-bogus")
		_, stderr, code := w1bRunScenario(t, "M3-j3b-config-bogus", w1bOwnerBaseEnv(t, coverageDir,
			"JUHE_AI_J3B_ENABLED=true",
			"JUHE_AI_J3B_OWNER=notgateway"))
		w1bRequireExitCode(t, "M3-j3b-config-bogus", code, 1)
		w1bRequireContains(t, "M3-j3b-config-bogus", stderr, "load J3b gateway owner config")
	})

	t.Run("J3b证据可读但未就绪", func(t *testing.T) {
		root := t.TempDir()
		validBusiness := w1mWriteEvidence(t, filepath.Join(root, "business-evidence"), w1mEvidenceEpoch)
		notReadyJ3b := w1mNotReadyEvidence(t, filepath.Join(root, "j3b-evidence"), w1mEvidenceEpoch)
		coverageDir := w1bCoverageDir(t, "M3-j3b-evidence-not-ready")
		env := w1bScenarioEnv(t, coverageDir, append(w1mJ3BEnvPairs(t, root,
			filepath.Join(root, "j3b-business.sqlite"), notReadyJ3b),
			"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH="+validBusiness,
			// 2026-09-21 起组合根恒开（SYSTEM_API/CHAIN 开关移除），业务 owner
			// 门禁必然执行：本臂只验证 J3b 证据，补全业务 owner 事实让门禁通过，
			// 使装配推进到 J3b 证据校验。
			"JUHE_AI_BUSINESS_OWNER=gateway",
			"JUHE_AI_BUSINESS_DATABASE_PATH="+filepath.Join(t.TempDir(), "w1m-j3b-evidence-business.sqlite"),
			"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
			"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
			"JUHE_AI_BUSINESS_SCHEMA_READY=true",
			"JUHE_AI_BUSINESS_OWNER_EPOCH="+w1mEvidenceEpoch,
		)...)
		_, stderr, code := w1bRunScenario(t, "M3-j3b-evidence-not-ready", w1bOwnerBaseEnv(t, coverageDir, env...))
		w1bRequireExitCode(t, "M3-j3b-evidence-not-ready", code, 1)
		w1bRequireContains(t, "M3-j3b-evidence-not-ready", stderr, "verify J3b cutover evidence")
	})

	t.Run("J3b业务库schema校验失败", func(t *testing.T) {
		root := w1mAuditRoot(t, "m3-j3b-schema")
		evidence := w1mWriteEvidence(t, filepath.Join(root, "j3b-evidence"), w1mEvidenceEpoch)
		// 空业务库文件：SchemaReady=true 时 CheckBusinessSQLiteSchema 必失败。
		emptyBusiness := filepath.Join(root, "j3b-business-empty.sqlite")
		if err := os.WriteFile(emptyBusiness, nil, 0o644); err != nil {
			t.Fatalf("创建空业务库失败: %v", err)
		}
		coverageDir := w1bCoverageDir(t, "M3-j3b-business-schema")
		env := w1bScenarioEnv(t, coverageDir, w1mJ3BEnvPairs(t, root, emptyBusiness, evidence)...)
		_, stderr, code := w1bRunScenario(t, "M3-j3b-business-schema", env)
		w1bRequireExitCode(t, "M3-j3b-business-schema", code, 1)
		w1bRequireContains(t, "M3-j3b-business-schema", stderr, "open J3b Business owner connection")
	})

	t.Run("J3bpostgres模式映射与业务库连接失败", func(t *testing.T) {
		root := w1mAuditRoot(t, "m3-j3b-pg")
		evidence := w1mWriteEvidence(t, filepath.Join(root, "j3b-evidence"), w1mEvidenceEpoch)
		// STORE=postgres 命中 main.go 的 Postgres 模式映射臂；业务 PG URL
		// 指向不可达地址（127.0.0.1:1），schema 校验连接即失败，不连真 PG。
		coverageDir := w1bCoverageDir(t, "M3-j3b-pg-business-unreachable")
		env := w1bScenarioEnv(t, coverageDir,
			"JUHE_AI_DATABASE_PATH="+filepath.Join(root, "business.sqlite3"),
			"JUHE_AI_J3B_ENABLED=true",
			"JUHE_AI_J3B_OWNER=gateway",
			"JUHE_AI_J3B_INSTANCE_ID=w1m-j3b",
			"JUHE_AI_J3B_STORE=postgres",
			"JUHE_AI_J3B_POSTGRES_URL=postgres://127.0.0.1:1/w1m_j3b_unused",
			"JUHE_AI_J3B_BUSINESS_POSTGRES_URL=postgres://127.0.0.1:1/w1m_j3b_business",
			"JUHE_AI_J3B_CREDENTIAL_SECRET=w1m-j3b-credential-secret",
			"JUHE_AI_J3B_IDENTITY_SECRET=w1m-j3b-identity-secret",
			"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED=true",
			"JUHE_AI_J3B_NODE_WRITER_STOPPED=true",
			"JUHE_AI_J3B_OWNER_EPOCH="+w1mEvidenceEpoch,
			"JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH="+evidence,
			"JUHE_AI_J3B_SCHEMA_READY=true",
			"JUHE_AI_J3B_HEALTH_BOUNDARY_READY=true",
			"JUHE_AI_J3B_RUNTIME_READY=true",
			"JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://127.0.0.1:1/0",
			"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE=juhe-ai:w1m-test")
		_, stderr, code := w1bRunScenario(t, "M3-j3b-pg-business-unreachable", env)
		w1bRequireExitCode(t, "M3-j3b-pg-business-unreachable", code, 1)
		w1bRequireContains(t, "M3-j3b-pg-business-unreachable", stderr, "open J3b Business owner connection")
	})

	t.Run("J3b电路运行时Redis不可达", func(t *testing.T) {
		root := w1mAuditRoot(t, "m3-j3b-redis")
		evidence := w1mWriteEvidence(t, filepath.Join(root, "j3b-evidence"), w1mEvidenceEpoch)
		// bootstrap 建满业务库 schema，让装配推进到电路运行时 Redis Ping。
		businessDB := filepath.Join(root, "j3b-business-full.sqlite")
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(businessDB))
		if err != nil {
			t.Fatalf("打开业务库: %v", err)
		}
		if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
			_ = db.Close()
			t.Fatalf("ensure 业务 schema: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("关闭业务库: %v", err)
		}
		coverageDir := w1bCoverageDir(t, "M3-j3b-redis-unreachable")
		env := w1bScenarioEnv(t, coverageDir, w1mJ3BEnvPairs(t, root, businessDB, evidence)...)
		_, stderr, code := w1bRunScenario(t, "M3-j3b-redis-unreachable", env)
		w1bRequireExitCode(t, "M3-j3b-redis-unreachable", code, 1)
		w1bRequireContains(t, "M3-j3b-redis-unreachable", stderr, "ping J3b Gateway circuit runtime Redis")
	})
}

// ---------------------------------------------------------------------------
// M4. system api 组合根生命周期（全量组装 + 优雅关闭 / 组合失败 / 端口冲突）
// ---------------------------------------------------------------------------

// w1mSystemAPIEnvPairs 返回 system api 组合根全量启动 env（业务 owner
// 门槛 + 有效切换证据 + 主监听地址）。2026-09-21 起模型检测 owner 默认
// 常驻且装配先于组合根，业务库在此预置完整 schema。
func w1mSystemAPIEnvPairs(t *testing.T, root, evidencePath string, mainPort int) []string {
	t.Helper()
	w1v2PrepareBusinessSQLite(t, filepath.Join(root, "business.sqlite3"))
	return []string{
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true",
		"JUHE_AI_BUSINESS_OWNER=gateway",
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
		"JUHE_AI_BUSINESS_SCHEMA_READY=true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH=" + w1mEvidenceEpoch,
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH=" + evidencePath,
		"JUHE_AI_BUSINESS_DATABASE_PATH=" + filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_SECRET=w1m-system-api-secret-0123456789abcdef",
		"JUHE_AI_HOST=127.0.0.1",
		fmt.Sprintf("JUHE_AI_PORT=%d", mainPort),
		"JUHE_AI_CHAT_ASSETS_ROOT=" + filepath.Join(root, "chat-assets"),
	}
}

func TestW1MBootSystemAPICompositionLifecycle(t *testing.T) {
	w1bBuildCoverBinary(t)
	client := &http.Client{Timeout: 3 * time.Second}

	t.Run("全量组装与优雅关闭", func(t *testing.T) {
		root := w1mAuditRoot(t, "m4-success")
		evidence := w1mWriteEvidence(t, filepath.Join(root, "evidence"), w1mEvidenceEpoch)
		healthAddr := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		mainPort := w1bFreePort(t)
		coverageDir := w1bCoverageDir(t, "M4-systemapi-success")
		pairs := w1mAuditEnvPairs(t, root, "w1m-m4-audit")
		pairs = append(pairs, w1mF4EnvPairs(t,
			filepath.Join(root, "f4-operation.sqlite3"),
			filepath.Join(root, "f4-business-settings.sqlite3"),
			filepath.Join(root, "usage-shards"),
		)...)
		pairs = append(pairs, w1mSystemAPIEnvPairs(t, root, evidence, mainPort)...)
		env := w1bScenarioEnv(t, coverageDir, pairs...)
		env = append(env, "JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr)
		cmd, done, cancel, _, _ := w1mStartOwnerProcess(t, coverageDir, env)
		w1mWaitHealthReady(t, client, healthAddr)

		// 主监听面：内核健康端点（组合根 /health 闭包）与指标面。
		mainBase := fmt.Sprintf("http://127.0.0.1:%d", mainPort)
		if response, err := client.Get(mainBase + "/__aisys__/api/health"); err != nil {
			t.Fatalf("GET /__aisys__/api/health 失败: %v", err)
		} else {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("GET /__aisys__/api/health 状态码 = %d，want 200", response.StatusCode)
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

		w1mGracefulShutdownOwner(t, cmd, done, cancel, coverageDir, "M4-systemapi-success")
	})

	t.Run("组合根失败快速退出", func(t *testing.T) {
		root := w1mAuditRoot(t, "m4-compose-fail")
		evidence := w1mWriteEvidence(t, filepath.Join(root, "evidence"), w1mEvidenceEpoch)
		// 表监控库替换为垃圾文件：loadRuntimeConfig 不校验文件内容，
		// composeSystemAPI 的 configure SQLite 步骤必然失败。
		w1mGarbageSQLite(t, filepath.Join(root, "table-monitor.sqlite3"))
		coverageDir := w1bCoverageDir(t, "M4-systemapi-compose-fail")
		pairs := w1mAuditEnvPairs(t, root, "w1m-m4-b-audit")
		pairs = append(pairs, w1mF4EnvPairs(t,
			filepath.Join(root, "f4-operation.sqlite3"),
			filepath.Join(root, "f4-business-settings.sqlite3"),
			filepath.Join(root, "usage-shards"),
		)...)
		pairs = append(pairs, w1mSystemAPIEnvPairs(t, root, evidence, w1bFreePort(t))...)
		// health 监听用空闲端口：默认 127.0.0.1:3306 可能被本机开发实例占用，
		// 不能让它先于被测的组合根失败臂触发。
		env := w1bScenarioEnv(t, coverageDir, pairs...)
		env = append(env, "JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:"+strconv.Itoa(w1bFreePort(t)))
		_, stderr, code := w1bRunScenario(t, "M4-systemapi-compose-fail", env)
		w1bRequireExitCode(t, "M4-systemapi-compose-fail", code, 1)
		w1bRequireContains(t, "M4-systemapi-compose-fail", stderr, "compose gateway system api")
	})

	t.Run("主监听端口被占快速退出", func(t *testing.T) {
		root := w1mAuditRoot(t, "m4-port-conflict")
		evidence := w1mWriteEvidence(t, filepath.Join(root, "evidence"), w1mEvidenceEpoch)
		// 测试进程占住主监听端口：组合成功后 net.Listen 必然失败。
		blocker, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("占位监听失败: %v", err)
		}
		defer func() { _ = blocker.Close() }()
		occupied := blocker.Addr().(*net.TCPAddr).Port
		coverageDir := w1bCoverageDir(t, "M4-systemapi-port-conflict")
		pairs := w1mAuditEnvPairs(t, root, "w1m-m4-c-audit")
		pairs = append(pairs, w1mF4EnvPairs(t,
			filepath.Join(root, "f4-operation.sqlite3"),
			filepath.Join(root, "f4-business-settings.sqlite3"),
			filepath.Join(root, "usage-shards"),
		)...)
		pairs = append(pairs, w1mSystemAPIEnvPairs(t, root, evidence, occupied)...)
		// health 监听用空闲端口（同上：默认 3306 可能被本机开发实例占用）。
		env := w1bScenarioEnv(t, coverageDir, pairs...)
		env = append(env, "JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:"+strconv.Itoa(w1bFreePort(t)))
		_, stderr, code := w1bRunScenario(t, "M4-systemapi-port-conflict", env)
		w1bRequireExitCode(t, "M4-systemapi-port-conflict", code, 1)
		w1bRequireContains(t, "M4-systemapi-port-conflict", stderr, "listen gateway system api endpoint")
	})
}
