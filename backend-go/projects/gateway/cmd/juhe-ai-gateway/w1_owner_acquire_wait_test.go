//go:build windows

package main

// w1_owner_acquire_wait_test.go —— JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT 有界等待
// 接管臂的二进制级验证（TestW1MOwnerLeaseAcquireWaitArms）。复用
// w1_boot_arms_test.go 与 w1_boot_cover_test.go 的插桩二进制、owner 进程与
// 覆盖收割帮手（w1bBuildCoverBinary / w1mAuditRoot / w1mAuditEnvPairs /
// w1mF4EnvPairs / w1mSystemAPIEnvPairs / w1mStartOwnerProcess /
// w1mWaitHealthReady / w1mWaitOutputContains / w1mGracefulShutdownOwner /
// w1bRunScenario / w1bFreePort）。
//
// 根因背景：dev 启动器在 Windows 上用 taskkill /t /f 强杀 gateway，进程
// defer 链不执行，F3/F4 owner 租约（SQLite lease 表一行，lease_until =
// 最后续租 + TTL 30s）未释放；TTL 过期前重启 gateway 会在 StartLeaseKeeper
// 的 ok=false 分支 fail-fast。修复契约：启动获取点外围新增有界等待重试，env
// 显式开启（默认空 / 0 保持既有 fail-fast 契约）；AcquireOwnerLease 的
// WHERE lease_until <= now 保证过期接管不会与活 owner 并存。
//
// 子场景（对应 main.go F3/F4 两个启动获取点的 wait 包裹路径）：
//
//	A F3 过期租约接管成功：进程 1（system api 组合根 + business evidence
//	  门禁，M4 同款构造，同时持有 F3+F4 启动获取的租约）就绪后
//	  cmd.Process.Kill() 强杀（taskkill /f 同款语义，defer 释放不执行）；
//	  sqlite 把 F3 lease 行 lease_until 直改为明确过去；F4 lease 行同改
//	  （进程 2 与进程 1 同 env 共用 F4 库，生产 taskkill 场景两份租约同时
//	  处于过期态，F4 行不改则进程 2 的 F4 启动获取在 10s 预算内等不到
//	  30s TTL 过期）；进程 2 同 env（仅监听端口换新）+ wait=10s 启动 →
//	  health ready → 优雅关闭收割覆盖。
//	B F3 未过期租约等待后超时 fail：进程 1（M1 同款构造，system api 关闭）
//	  就绪后强杀，不改 lease 行（lease_until 仍在未来）；进程 2 带 wait=2s
//	  同步运行 → exit 1 且 stderr 含 F3 快速失败原文案（默认契约不变）。
//	C F4 过期租约接管成功：进程 1（M1 同款"同时持有 F3+F4"构造，F4 走
//	  组件专有租约分支）就绪后强杀；sqlite 把 F4 lease 行置为明确过去；
//	  进程 2（独立 F3 审计根 + 共享 F4 库 + system api 组合根走 main 启动
//	  获取点）带 wait=10s 启动 → health ready → 优雅关闭收割覆盖。
//
// lease 行 UPDATE 的时间格式必须与 store 写入格式一致（SQLite 字符串比较
// 语义）：F3 是 internal/auditlog dbTime 的 UTC RFC3339Nano；F4 是
// internal/operationlog storageTime 的 UTC 定长 9 位小数
// （storageTimeLayout = "2006-01-02T15:04:05.000000000Z"）。
//
// 强杀进程的 GOCOVERDIR 不追加进收割清单：TerminateProcess 跳过 cover 运行
// 时的计数器落盘，目录内没有可收割的计数文件；优雅关闭的进程 2 照常追加。

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// w1mLeaseWaitPastF3 / w1mLeaseWaitPastF4 是明确的过去时刻（2000-01-01），
// 分别按 F3 dbTime（RFC3339Nano）与 F4 storageTime（定长 9 位小数）的
// SQLite 写入格式排版，保证与 AcquireOwnerLease 的 WHERE lease_until <= now
// 字符串比较语义一致。
var (
	w1mLeaseWaitPastF3 = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	w1mLeaseWaitPastF4 = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000000000Z")
)

// w1mExpireOwnerLeaseRow 直改 lease 行的 lease_until（模拟 TTL 已过期的安全
// 接管窗口）。表由进程 1 的 EnsureSchema 建好，这里只 UPDATE 不复制 DDL；
// updated_at 仅为元数据列，同写过去时刻不影响比较语义。
func w1mExpireOwnerLeaseRow(t *testing.T, dbPath, table, leaseKey, until string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("打开 lease 库 %s 失败: %v", dbPath, err)
	}
	defer db.Close()
	result, err := db.Exec(`UPDATE `+table+` SET lease_until=?, updated_at=? WHERE lease_key=?`, until, until, leaseKey)
	if err != nil {
		t.Fatalf("置过期 %s 的 %s lease 行失败: %v", dbPath, table, err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		t.Fatalf("置过期 %s 的 %s lease 行影响行数 = %d（err=%v），want 1", dbPath, table, affected, err)
	}
}

// w1mKillOwnerProcess 强杀 owner 进程并回收 wait（taskkill /f 同款语义：
// TerminateProcess 不执行 defer 链，F3/F4 租约留在表内不释放）。
func w1mKillOwnerProcess(t *testing.T, cmd *exec.Cmd, done <-chan error, cancel context.CancelFunc, scenario string) {
	t.Helper()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("场景 %s 强杀 owner 进程失败: %v", scenario, err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("场景 %s 强杀后 15s 内未退出", scenario)
	}
	cancel()
}

func TestW1MOwnerLeaseAcquireWaitArms(t *testing.T) {
	w1bBuildCoverBinary(t)
	client := &http.Client{Timeout: 2 * time.Second}

	t.Run("A-F3过期租约接管成功", func(t *testing.T) {
		root := w1mAuditRoot(t, "lease-wait-a")
		evidence := w1mWriteEvidence(t, filepath.Join(root, "evidence"), w1mEvidenceEpoch)
		f4DB := filepath.Join(root, "f4-operation.sqlite3")

		// 进程 1：system api 组合根 + business evidence 门禁（M4 同款构造），
		// F3 与 F4 的启动获取租约都由本进程持有。
		healthAddr1 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir1 := w1bCoverageDir(t, "M1-lease-wait-a-owner1")
		env1 := w1bScenarioEnv(t, coverageDir1, append(
			w1mAuditEnvPairs(t, root, "w1m-wait-a-audit"),
			w1mF4EnvPairs(t, f4DB,
				filepath.Join(root, "f4-business-settings.sqlite3"),
				filepath.Join(root, "usage-shards"),
			)...)...)
		env1 = append(env1, w1mSystemAPIEnvPairs(t, root, evidence, w1bFreePort(t))...)
		env1 = append(env1, "JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr1)
		cmd1, done1, cancel1, _, _ := w1mStartOwnerProcess(t, coverageDir1, env1)
		w1mWaitHealthReady(t, client, healthAddr1)

		// 强杀（taskkill /f 同款语义）：defer 释放链不执行，租约留在表内。
		w1mKillOwnerProcess(t, cmd1, done1, cancel1, "M1-lease-wait-a-owner1")

		// F3 lease 行置为明确过去（被测接管窗口）；F4 行同改（同 env 共用
		// F4 库的前置条件，见文件头说明）。
		w1mExpireOwnerLeaseRow(t, filepath.Join(root, "audit-dataset.sqlite3"), "audit_log_owner_leases", "f3-audit-log-persistence", w1mLeaseWaitPastF3)
		w1mExpireOwnerLeaseRow(t, f4DB, "operation_log_owner_leases", "f4-operation-log-persistence", w1mLeaseWaitPastF4)

		// 进程 2：同 env（仅监听端口换新，进程 1 已退出）+ wait=10s →
		// F3/F4 过期租约启动获取接管成功，健康端点就绪。
		healthAddr2 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir2 := w1bCoverageDir(t, "M1-lease-wait-a-owner2")
		env2 := w1bScenarioEnv(t, coverageDir2, append(
			w1mAuditEnvPairs(t, root, "w1m-wait-a-audit"),
			w1mF4EnvPairs(t, f4DB,
				filepath.Join(root, "f4-business-settings.sqlite3"),
				filepath.Join(root, "usage-shards"),
			)...)...)
		env2 = append(env2, w1mSystemAPIEnvPairs(t, root, evidence, w1bFreePort(t))...)
		env2 = append(env2,
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr2,
			"JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=10s")
		cmd2, done2, cancel2, stdout2, stderr2 := w1mStartOwnerProcess(t, coverageDir2, env2)
		w1mWaitHealthReady(t, client, healthAddr2)
		if !w1mWaitOutputContains(t, stdout2, "juhe-ai-gateway started", 10*time.Second) {
			cancel2()
			_ = cmd2.Process.Kill()
			<-done2
			t.Fatalf("进程 2 就绪后未见启动完成日志，stdout: %s stderr: %s", stdout2.String(), stderr2.String())
		}
		w1mGracefulShutdownOwner(t, cmd2, done2, cancel2, coverageDir2, "M1-lease-wait-a-owner2")
	})

	t.Run("B-F3未过期租约等待超时fail", func(t *testing.T) {
		root := w1mAuditRoot(t, "lease-wait-b")

		// 进程 1：M1 同款构造（system api 关闭 → F3 启动获取 + F4 专有租约）。
		healthAddr1 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir1 := w1bCoverageDir(t, "M1-lease-wait-b-owner1")
		env1 := w1bScenarioEnv(t, coverageDir1, append(
			w1mAuditEnvPairs(t, root, "w1m-wait-b-audit"),
			w1mF4EnvPairs(t,
				filepath.Join(root, "f4-operation.sqlite3"),
				filepath.Join(root, "f4-business-settings.sqlite3"),
				filepath.Join(root, "usage-shards"),
			)...)...)
		env1 = append(env1,
			"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=false",
			"JUHE_AI_GATEWAY_CHAIN_ENABLED=false",
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr1)
		cmd1, done1, cancel1, _, _ := w1mStartOwnerProcess(t, coverageDir1, env1)
		w1mWaitHealthReady(t, client, healthAddr1)
		w1mKillOwnerProcess(t, cmd1, done1, cancel1, "M1-lease-wait-b-owner1")

		// 进程 2：不改 lease 行（lease_until 仍在未来）+ wait=2s → 等待预算
		// 耗尽仍被持有，走既有 fail-fast 原文案（默认契约不变）。
		coverageDir2 := w1bCoverageDir(t, "M1-lease-wait-b-timeout")
		env2 := w1bScenarioEnv(t, coverageDir2, w1mAuditEnvPairs(t, root, "w1m-wait-b-audit")...)
		env2 = append(env2,
			"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=false",
			"JUHE_AI_GATEWAY_CHAIN_ENABLED=false",
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:"+strconv.Itoa(w1bFreePort(t)),
			"JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=2s")
		_, stderr2, code2 := w1bRunScenario(t, "M1-lease-wait-b-timeout", env2)
		w1bRequireExitCode(t, "M1-lease-wait-b-timeout", code2, 1)
		w1bRequireContains(t, "M1-lease-wait-b-timeout", stderr2, "F3 audit owner lease held by another owner process")
	})

	t.Run("C-F4过期租约接管成功", func(t *testing.T) {
		root1 := w1mAuditRoot(t, "lease-wait-c1")
		root2 := w1mAuditRoot(t, "lease-wait-c2")
		f4DB := filepath.Join(root1, "f4-operation.sqlite3")

		// 进程 1：M1 同款"同时持有 F3+F4"构造（system api 关闭 → F4 走
		// 组件专有租约分支）。
		healthAddr1 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir1 := w1bCoverageDir(t, "M1-lease-wait-c-owner1")
		env1 := w1bScenarioEnv(t, coverageDir1, append(
			w1mAuditEnvPairs(t, root1, "w1m-wait-c-audit-1"),
			w1mF4EnvPairs(t, f4DB,
				filepath.Join(root1, "f4-business-settings.sqlite3"),
				filepath.Join(root1, "usage-shards"),
			)...)...)
		env1 = append(env1,
			"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=false",
			"JUHE_AI_GATEWAY_CHAIN_ENABLED=false",
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr1)
		cmd1, done1, cancel1, _, _ := w1mStartOwnerProcess(t, coverageDir1, env1)
		w1mWaitHealthReady(t, client, healthAddr1)
		w1mKillOwnerProcess(t, cmd1, done1, cancel1, "M1-lease-wait-c-owner1")

		// F4 lease 行置为明确过去（被测接管窗口）；F3 行不动（进程 2 换
		// 独立审计根，无 F3 接管前置）。
		w1mExpireOwnerLeaseRow(t, f4DB, "operation_log_owner_leases", "f4-operation-log-persistence", w1mLeaseWaitPastF4)

		// 进程 2：独立 F3 审计根 + 共享 F4 库 + system api 组合根（business
		// evidence 门禁）→ F4 启动获取点带 wait=10s 接管过期租约。
		evidence2 := w1mWriteEvidence(t, filepath.Join(root2, "evidence"), w1mEvidenceEpoch)
		healthAddr2 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir2 := w1bCoverageDir(t, "M1-lease-wait-c-owner2")
		env2 := w1bScenarioEnv(t, coverageDir2, append(
			w1mAuditEnvPairs(t, root2, "w1m-wait-c-audit-2"),
			w1mF4EnvPairs(t, f4DB,
				filepath.Join(root2, "f4-business-settings.sqlite3"),
				filepath.Join(root2, "usage-shards"),
			)...)...)
		env2 = append(env2, w1mSystemAPIEnvPairs(t, root2, evidence2, w1bFreePort(t))...)
		env2 = append(env2,
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr2,
			"JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=10s")
		cmd2, done2, cancel2, _, _ := w1mStartOwnerProcess(t, coverageDir2, env2)
		w1mWaitHealthReady(t, client, healthAddr2)
		w1mGracefulShutdownOwner(t, cmd2, done2, cancel2, coverageDir2, "M1-lease-wait-c-owner2")
	})
}
