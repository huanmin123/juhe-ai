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
// defer 链不执行，F3/F4 owner 租约（PG lease 表一行，lease_until =
// 最后续租 + TTL 30s）未释放；TTL 过期前重启 gateway 会在 StartLeaseKeeper
// 的 ok=false 分支 fail-fast。修复契约：启动获取点外围新增有界等待重试，env
// 显式开启（默认空 / 0 保持既有 fail-fast 契约）。
//
// 获取冲突分支的两条路径（store.go AcquireOwnerLease）：
//   - 跨 owner：必须等 lease_until 过期，过期前 acquire ok=false，等待预算
//     耗尽维持 fail-fast 原文案（BUG-0228 发布窗口契约，子场景 B）；
//   - 同 owner_id（进程身份，默认主机名）：未过期租约直接接管并自增
//     fence_token——同 owner 的再获取只可能来自本实例自己的重启/失租恢复，
//     旧世代写入由 fence 挡在外面，无需等 TTL 到期（子场景 D）。
//
// 子场景（对应 main.go F3/F4 两个启动获取点的 wait 包裹路径；PG 租约语义
// 与原 SQLite 等价，PLAN-20261024T051925945Z：lease 行落在共享 pgtestkit
// 库的 juhe_dataset.*_owner_leases 表）：
//
//	A F3 过期租约接管成功：进程 1（system api 组合根，M4 同款构造，同时
//	  持有 F3+F4 启动获取的租约）就绪后 cmd.Process.Kill() 强杀（taskkill /f
//	  同款语义，defer 释放不执行）；F3 lease 行 lease_until 直改为明确过去；
//	  F4 lease 行同改（进程 2 与进程 1 同 env 共用 F4 库，生产 taskkill 场景
//	  两份租约同时处于过期态，F4 行不改则进程 2 的 F4 启动获取在 10s 预算内
//	  等不到 30s TTL 过期）；进程 2 同 env（仅监听端口换新）+ wait=10s 启动
//	  → health ready → 优雅关闭收割覆盖。
//	B F3 跨 owner 未过期租约等待后超时 fail：进程 1（M1 同款构造）就绪后
//	  强杀，不改 lease 行（lease_until 仍在未来）；进程 2 换一个 audit 实例
//	  ID（跨 owner——同 owner_id 会被直接接管，见下）带 wait=2s 同步运行 →
//	  exit 1 且 stderr 含 F3 快速失败原文案（默认契约不变）。
//	C F4 过期租约接管成功：进程 1（M1 同款"同时持有 F3+F4"构造，F4 走
//	  组件专有租约分支）就绪后强杀；F4 lease 行置为明确过去；进程 2（独立
//	  F3 审计库 + 共享 F4 库 + system api 组合根走 main 启动获取点）带
//	  wait=10s 启动 → health ready → 优雅关闭收割覆盖。
//	D F3 同 owner 未过期租约直接接管：进程 1（M1 同款构造）就绪后强杀，不改
//	  lease 行（lease_until 仍在未来）；进程 2 沿用同实例 ID + wait=10s 启动
//	  → 同 owner 分支立即接管（不消耗等待预算、不报租约冲突），health ready
//	  → 优雅关闭收割覆盖。
//	E F4 同 owner 未过期租约直接接管：进程 1（M1 同款"同时持有 F3+F4"构造）
//	  就绪后强杀，不改 lease 行；进程 2（独立 F3 审计库 + 共享 F4 库 + system
//	  api 组合根走 main 启动获取点）带 wait=10s 启动 → F4 同 owner 分支立即
//	  接管，health ready → 优雅关闭收割覆盖。F4 获取 SQL 与 F3 同键同语义
//	  （operationlog/store.go 的 owner_id = excluded.owner_id 分支）。
//
// 强杀进程的 GOCOVERDIR 不追加进收割清单：TerminateProcess 跳过 cover 运行
// 时的计数器落盘，目录内没有可收割的计数文件；优雅关闭的进程 2 照常追加。

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// w1mExpireOwnerLeaseRow 直改 lease 行的 lease_until（模拟 TTL 已过期的安全
// 接管窗口）。表由进程 1 的 EnsureSchema 建好，这里只 UPDATE 不复制 DDL；
// updated_at 同写过去时刻（元数据列，不影响比较语义）。
func w1mExpireOwnerLeaseRow(t *testing.T, db *sql.DB, qualifiedTable, leaseKey string) {
	t.Helper()
	result, err := db.Exec(`UPDATE `+qualifiedTable+`
		SET lease_until = '2000-01-01T00:00:00Z'::timestamptz, updated_at = '2000-01-01T00:00:00Z'::timestamptz
		WHERE lease_key = $1`, leaseKey)
	if err != nil {
		t.Fatalf("置过期 %s 的 lease 行失败: %v", qualifiedTable, err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		t.Fatalf("置过期 %s 的 lease 行影响行数 = %d（err=%v），want 1", qualifiedTable, affected, err)
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
		business := w1mBusinessPostgres(t)
		audit := pgtestkit.NewTestDatabase(t)
		f4 := pgtestkit.NewTestDatabase(t)

		// 进程 1：system api 组合根（M4 同款构造），
		// F3 与 F4 的启动获取租约都由本进程持有。
		healthAddr1 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir1 := w1bCoverageDir(t, "M1-lease-wait-a-owner1")
		env1 := w1bScenarioEnv(t, coverageDir1, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root, "w1m-wait-a-audit", audit.DSN, business.DSN)...,
		), w1mF4EnvPairs(t, f4.DSN, "w1m-f4")...)...)
		env1 = append(env1, w1mSystemAPIEnvPairs(t, root, business.DSN, w1bFreePort(t))...)
		env1 = append(env1, "JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr1)
		cmd1, done1, cancel1, _, _ := w1mStartOwnerProcess(t, coverageDir1, env1)
		w1mWaitHealthReady(t, client, healthAddr1)

		// 强杀（taskkill /f 同款语义）：defer 释放链不执行，租约留在表内。
		w1mKillOwnerProcess(t, cmd1, done1, cancel1, "M1-lease-wait-a-owner1")

		// F3 lease 行置为明确过去（被测接管窗口）；F4 行同改（同 env 共用
		// F4 库的前置条件，见文件头说明）。
		w1mExpireOwnerLeaseRow(t, audit.DB, "juhe_dataset.audit_log_owner_leases", "f3-audit-log-persistence")
		w1mExpireOwnerLeaseRow(t, f4.DB, "juhe_dataset.operation_log_owner_leases", "f4-operation-log-persistence")

		// 进程 2：同 env（仅监听端口换新，进程 1 已退出）+ wait=10s →
		// F3/F4 过期租约启动获取接管成功，健康端点就绪。
		healthAddr2 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir2 := w1bCoverageDir(t, "M1-lease-wait-a-owner2")
		env2 := w1bScenarioEnv(t, coverageDir2, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root, "w1m-wait-a-audit", audit.DSN, business.DSN)...,
		), w1mF4EnvPairs(t, f4.DSN, "w1m-f4")...)...)
		env2 = append(env2, w1mSystemAPIEnvPairs(t, root, business.DSN, w1bFreePort(t))...)
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
		business := w1mBusinessPostgres(t)
		audit := pgtestkit.NewTestDatabase(t)
		f4 := pgtestkit.NewTestDatabase(t)

		// 进程 1：M1 同款构造（F3 启动获取 + F4 专有租约）。
		healthAddr1 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir1 := w1bCoverageDir(t, "M1-lease-wait-b-owner1")
		env1 := w1bScenarioEnv(t, coverageDir1, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root, "w1m-wait-b-audit", audit.DSN, business.DSN)...,
		), w1mF4EnvPairs(t, f4.DSN, "w1m-f4")...)...)
		env1 = append(env1,
			"JUHE_AI_POSTGRES_URL="+business.DSN,
			"JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN,
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr1)
		cmd1, done1, cancel1, _, _ := w1mStartOwnerProcess(t, coverageDir1, env1)
		w1mWaitHealthReady(t, client, healthAddr1)
		w1mKillOwnerProcess(t, cmd1, done1, cancel1, "M1-lease-wait-b-owner1")

		// 进程 2：换一个 audit 实例 ID（跨 owner——同 owner_id 的未过期租约
		// 会被直接接管，见文件头契约）+ 不改 lease 行（lease_until 仍在未来）
		// + wait=2s → 等待预算耗尽仍被持有，走既有 fail-fast 原文案。
		coverageDir2 := w1bCoverageDir(t, "M1-lease-wait-b-timeout")
		env2 := w1bScenarioEnv(t, coverageDir2, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root, "w1m-wait-b-rival", audit.DSN, business.DSN)...,
		), "JUHE_AI_POSTGRES_URL="+business.DSN, "JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN)...)
		env2 = append(env2,
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:"+strconv.Itoa(w1bFreePort(t)),
			"JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=2s")
		_, stderr2, code2 := w1bRunScenario(t, "M1-lease-wait-b-timeout", env2)
		w1bRequireExitCode(t, "M1-lease-wait-b-timeout", code2, 1)
		w1bRequireContains(t, "M1-lease-wait-b-timeout", stderr2, "F3 audit owner lease held by another owner process")
	})

	t.Run("C-F4过期租约接管成功", func(t *testing.T) {
		root1 := w1mAuditRoot(t, "lease-wait-c1")
		root2 := w1mAuditRoot(t, "lease-wait-c2")
		business := w1mBusinessPostgres(t)
		audit1 := pgtestkit.NewTestDatabase(t)
		audit2 := pgtestkit.NewTestDatabase(t)
		f4 := pgtestkit.NewTestDatabase(t)

		// 进程 1：M1 同款"同时持有 F3+F4"构造（F4 走组件专有租约分支）。
		healthAddr1 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir1 := w1bCoverageDir(t, "M1-lease-wait-c-owner1")
		env1 := w1bScenarioEnv(t, coverageDir1, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root1, "w1m-wait-c-audit-1", audit1.DSN, business.DSN)...,
		), w1mF4EnvPairs(t, f4.DSN, "w1m-f4")...)...)
		env1 = append(env1,
			"JUHE_AI_POSTGRES_URL="+business.DSN,
			"JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN,
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr1)
		cmd1, done1, cancel1, _, _ := w1mStartOwnerProcess(t, coverageDir1, env1)
		w1mWaitHealthReady(t, client, healthAddr1)
		w1mKillOwnerProcess(t, cmd1, done1, cancel1, "M1-lease-wait-c-owner1")

		// F4 lease 行置为明确过去（被测接管窗口）；F3 行不动（进程 2 换
		// 独立审计库，无 F3 接管前置）。
		w1mExpireOwnerLeaseRow(t, f4.DB, "juhe_dataset.operation_log_owner_leases", "f4-operation-log-persistence")

		// 进程 2：独立 F3 审计库 + 共享 F4 库 + system api 组合根 → F4 启动
		// 获取点带 wait=10s 接管过期租约。
		healthAddr2 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir2 := w1bCoverageDir(t, "M1-lease-wait-c-owner2")
		env2 := w1bScenarioEnv(t, coverageDir2, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root2, "w1m-wait-c-audit-2", audit2.DSN, business.DSN)...,
		), w1mF4EnvPairs(t, f4.DSN, "w1m-f4")...)...)
		env2 = append(env2, w1mSystemAPIEnvPairs(t, root2, business.DSN, w1bFreePort(t))...)
		env2 = append(env2,
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr2,
			"JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=10s")
		cmd2, done2, cancel2, _, _ := w1mStartOwnerProcess(t, coverageDir2, env2)
		w1mWaitHealthReady(t, client, healthAddr2)
		w1mGracefulShutdownOwner(t, cmd2, done2, cancel2, coverageDir2, "M1-lease-wait-c-owner2")
	})

	t.Run("D-F3同owner未过期租约直接接管", func(t *testing.T) {
		root := w1mAuditRoot(t, "lease-wait-d")
		business := w1mBusinessPostgres(t)
		audit := pgtestkit.NewTestDatabase(t)
		f4 := pgtestkit.NewTestDatabase(t)

		// 进程 1：M1 同款构造（F3 启动获取由本进程持有）。
		healthAddr1 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir1 := w1bCoverageDir(t, "M1-lease-wait-d-owner1")
		env1 := w1bScenarioEnv(t, coverageDir1, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root, "w1m-wait-d-audit", audit.DSN, business.DSN)...,
		), w1mF4EnvPairs(t, f4.DSN, "w1m-f4")...)...)
		env1 = append(env1,
			"JUHE_AI_POSTGRES_URL="+business.DSN,
			"JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN,
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr1)
		cmd1, done1, cancel1, _, _ := w1mStartOwnerProcess(t, coverageDir1, env1)
		w1mWaitHealthReady(t, client, healthAddr1)
		w1mKillOwnerProcess(t, cmd1, done1, cancel1, "M1-lease-wait-d-owner1")

		// 进程 2：沿用同实例 ID（同 owner_id）+ 不改 lease 行（lease_until
		// 仍在未来）+ wait=10s → 同 owner 分支立即接管，等待预算不被消耗、
		// 不报租约冲突，health ready 后优雅关闭。
		healthAddr2 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir2 := w1bCoverageDir(t, "M1-lease-wait-d-owner2")
		env2 := w1bScenarioEnv(t, coverageDir2, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root, "w1m-wait-d-audit", audit.DSN, business.DSN)...,
		), w1mF4EnvPairs(t, f4.DSN, "w1m-f4")...)...)
		env2 = append(env2,
			"JUHE_AI_POSTGRES_URL="+business.DSN,
			"JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN,
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr2,
			"JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=10s")
		cmd2, done2, cancel2, stdout2, stderr2 := w1mStartOwnerProcess(t, coverageDir2, env2)
		if w1mWaitOutputContains(t, stderr2, "lease held by another owner process", 3*time.Second) {
			cancel2()
			_ = cmd2.Process.Kill()
			<-done2
			t.Fatalf("进程 2 同 owner 未过期租约必须直接接管，实际报租约冲突: %s", stderr2.String())
		}
		w1mWaitHealthReady(t, client, healthAddr2)
		if !w1mWaitOutputContains(t, stdout2, "juhe-ai-gateway started", 10*time.Second) {
			cancel2()
			_ = cmd2.Process.Kill()
			<-done2
			t.Fatalf("进程 2 同 owner 未过期租约接管后未见启动完成日志，stdout: %s stderr: %s", stdout2.String(), stderr2.String())
		}
		w1mGracefulShutdownOwner(t, cmd2, done2, cancel2, coverageDir2, "M1-lease-wait-d-owner2")
	})

	t.Run("E-F4同owner未过期租约直接接管", func(t *testing.T) {
		root1 := w1mAuditRoot(t, "lease-wait-e1")
		root2 := w1mAuditRoot(t, "lease-wait-e2")
		business := w1mBusinessPostgres(t)
		audit1 := pgtestkit.NewTestDatabase(t)
		audit2 := pgtestkit.NewTestDatabase(t)
		f4 := pgtestkit.NewTestDatabase(t)

		// 进程 1：M1 同款"同时持有 F3+F4"构造（F4 走组件专有租约分支，
		// owner_id 固定 w1m-f4，与进程 2 的 F4 实例 ID 相同）。
		healthAddr1 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir1 := w1bCoverageDir(t, "M1-lease-wait-e-owner1")
		env1 := w1bScenarioEnv(t, coverageDir1, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root1, "w1m-wait-e-audit-1", audit1.DSN, business.DSN)...,
		), w1mF4EnvPairs(t, f4.DSN, "w1m-f4")...)...)
		env1 = append(env1, w1mSystemAPIEnvPairs(t, root1, business.DSN, w1bFreePort(t))...)
		env1 = append(env1, "JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr1)
		cmd1, done1, cancel1, stdout1, stderr1 := w1mStartOwnerProcess(t, coverageDir1, env1)
		if !w1mTryWaitHealthReady(t, client, healthAddr1, 20*time.Second) {
			cancel1()
			_ = cmd1.Process.Kill()
			<-done1
			t.Fatalf("进程 1 健康端点 %s 在 20s 内未就绪\n--- stdout ---\n%s\n--- stderr ---\n%s", healthAddr1, stdout1.String(), stderr1.String())
		}
		w1mKillOwnerProcess(t, cmd1, done1, cancel1, "M1-lease-wait-e-owner1")

		// 进程 2：独立 F3 审计库（F3 无前置冲突）+ 共享 F4 库 + system api
		// 组合根走 main 启动获取点。不改 F4 lease 行（lease_until 仍在未来）
		// + wait=10s → F4 同 owner 分支立即接管，不报租约冲突。
		healthAddr2 := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir2 := w1bCoverageDir(t, "M1-lease-wait-e-owner2")
		env2 := w1bScenarioEnv(t, coverageDir2, append(append(
			w1mRuntimeBaseEnvPairs(t),
			w1mAuditEnvPairs(t, root2, "w1m-wait-e-audit-2", audit2.DSN, business.DSN)...,
		), w1mF4EnvPairs(t, f4.DSN, "w1m-f4")...)...)
		env2 = append(env2, w1mSystemAPIEnvPairs(t, root2, business.DSN, w1bFreePort(t))...)
		env2 = append(env2,
			"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddr2,
			"JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=10s")
		cmd2, done2, cancel2, stdout2, stderr2 := w1mStartOwnerProcess(t, coverageDir2, env2)
		if w1mWaitOutputContains(t, stderr2, "lease held by another owner process", 3*time.Second) {
			cancel2()
			_ = cmd2.Process.Kill()
			<-done2
			t.Fatalf("进程 2 的 F4 同 owner 未过期租约必须直接接管，实际报租约冲突: %s", stderr2.String())
		}
		if !w1mTryWaitHealthReady(t, client, healthAddr2, 20*time.Second) {
			cancel2()
			_ = cmd2.Process.Kill()
			<-done2
			t.Fatalf("进程 2 健康端点 %s 在 20s 内未就绪\n--- stdout ---\n%s\n--- stderr ---\n%s", healthAddr2, stdout2.String(), stderr2.String())
		}
		if !w1mWaitOutputContains(t, stdout2, "juhe-ai-gateway started", 10*time.Second) {
			cancel2()
			_ = cmd2.Process.Kill()
			<-done2
			t.Fatalf("进程 2 的 F4 同 owner 未过期租约接管后未见启动完成日志，stdout: %s stderr: %s", stdout2.String(), stderr2.String())
		}
		w1mGracefulShutdownOwner(t, cmd2, done2, cancel2, coverageDir2, "M1-lease-wait-e-owner2")
	})
}
