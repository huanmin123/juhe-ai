//go:build windows

package main

// w1_main_tail_test.go —— main.go / chain_runtime.go / chain_ports.go 尾部未
// 覆盖块的专项收敛测试（w1w 标识符前缀，TestW1W 入口）。复用既有插桩二进制
// 与覆盖目录帮手（w1b*）和 PG 门禁帮手（w1g2*），只新增本文件的场景。
//
// PostgreSQL + Redis 是唯一存储形态（PLAN-20261024T051925945Z）：owner 子进
// 程夹具的库角色全部收敛为 pgtestkit 隔离库连接串（业务库 fresh DDL+种子、
// J3b 专属 schema 由 w1g2EnsureJ3bSchema 补建、F3/F4 独立库自 EnsureSchema），
// 文件面只剩 blob/hot 搜索、usage spool 与日志目录。（原 sqlite 六库组合根
// 场景与 legacy sqlite 迁移编码失败场景已随存储形态/命令退役删除，D9。）
//
// 覆盖手法总览：
//   A. main.go owner 全栈启动中段臂：PG 组合根 + J3b 全量 env 直接完整
//      启动，让 enforcement/tokenizer/mount/管理监听/captcha 臂自然覆盖
//      （336-338 captcha；Go 运行时采样器臂已随监控改造删除）。
//   B. main.go fail-fast 臂：在完整 env 基础上做单点破坏（非法 env /
//      非法 Redis URL / 不 seed meta），每个场景断言 stderr 的 fail 特征
//      与 exit 1。
//   B1b. J3b 契约检查失败臂（真实 dev PG 临时子库，不可达即 Skip）：权限
//      收缩使契约检查按列/权限真实失败。
//   B2. F3/F4 EnsureSchema 失败臂：PG store 的 OpenStore 惰性不连接，
//      EnsureSchema 首次连库时对不可达 URL 确定性失败。
//   C. main.go 运行期臂：启动就绪后关停 state miniredis，让 circuit runtime
//      组件在 CheckReady 处失败 → supervisor fail；部分请求头挂住 main
//      server → Shutdown 超时。
//   F. chain_runtime.go 进程内：Bus 非空组合驱动本地屏蔽存储的并发投影闭包
//      （510-512）；redis 状态驱动组合后关闭共享 state client，驱动认证模型
//      限流 warn 闭包（368-370）与用户请求限制协调器 warn 闭包（376-378）。
//   G. chain_ports.go 进程内：usage 非空的 downstream-closed 分支（896-903）、
//      失败 Store 的来源级避让记录错误臂（747-752）、激活后探针错误臂
//      （760-763、770-774）。
//
// 记录性跳过（构造器防御臂，无触发缝，证据见各块注释）：
//   main.go 171-173/187-189/203-205（New 只校验 nil db/非法 mode/非法
//   schema，main 流程恒合法）、190-192（retention 契约读的 system_sessions
//   列集与 auth 契约首表相同，auth 先败）、222-225/226-230（keymodel 与
//   circuitruntime 共用同一 URL/命名空间与 gate，前一级先败）、232-235
//   （projector 仅空 InstanceID 失败，而 LoadConfig 拒绝空 InstanceID）、
//   268-290（enforcement/recovery/quality/tokenizer/modelLimits 构造只校验
//   nil db）、340-348（mount 仅重复模式冲突失败）、388-390/442-444/514-516
//   （租约错误臂需要启动中途 DB 故障注入，无缝）、679-681/689-691/701-706/
//   728-733（Serve/Shutdown 错误臂需要进程外关闭监听或超过 ReadHeaderTimeout
//   的慢 handler，健康面 handler 恒快返）。chain_runtime.go 252-254/348-350/
//   359-361/393-395/397-407 系（错误臂被同 URL/同命名空间的更早构造或恒真
//   守卫遮蔽）、415-433/443-460 系（quota 服务校验在 main 装配恒满足）、
//   466-468/488-490/497-499/521-523/549-551/566-568/628-630/649-658/664-666/
//   681-683（同理，见 w1w 注释与报告）。chain_ports.go 454-456/631-633/
//   690-692/903-905（usage 服务错误臂：队列容量 64MB 恒大于 2MB 估算上限，
//   RecordFailedUpstreamAttempt 无错误路径）、1444-1446（identity secret 经
//   构造校验非空，HMAC 恒成功）、1761-1768（AuditLogInput JSON round-trip
//   恒可逆）。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const (
	w1wOwnerEpoch    = "epoch-w1w"
	w1wJ3bRedisNS    = "w1w"
	w1wRuntimeSecret = "w1w-owner-tail-secret"
)

// ---------------------------------------------------------------------------
// PG owner fixture：业务库 + J3b 专属库 + F3/F4 库 + 双 miniredis + 端口
// ---------------------------------------------------------------------------

type w1wOwnerFixture struct {
	root       string
	pg         *pgtestkit.TestDatabase // 业务库（fresh DDL + seed）
	j3b        *pgtestkit.TestDatabase // J3b 专属库（prepareJ3b=false 时无 schema）
	audit      *pgtestkit.TestDatabase // F3 审计库
	oper       *pgtestkit.TestDatabase // F4 操作日志库
	cache      *miniredis.Miniredis
	state      *miniredis.Miniredis
	healthAddr string
	mainAddr   string
	j3bAddr    string
}

// w1wNewOwnerFixture 组装 owner 二进制场景 fixture。prepareJ3b=false 时 J3b
// 专属库是空 PG 库（合法空库，用于 OpenHost CheckSchema 失败臂）；
// seedMeta=false 时不写 runtime-index-meta（CheckReady 失败臂）。
func w1wNewOwnerFixture(t *testing.T, prepareJ3b, seedMeta bool) *w1wOwnerFixture {
	t.Helper()
	f := &w1wOwnerFixture{root: t.TempDir()}
	f.pg = w1v2PrepareBusinessPostgres(t)
	f.j3b = pgtestkit.NewTestDatabase(t)
	if prepareJ3b {
		w1g2EnsureJ3bSchema(t, f.j3b.DB)
	}
	f.audit = pgtestkit.NewTestDatabase(t)
	f.oper = pgtestkit.NewTestDatabase(t)
	for _, name := range []string{"business-evidence", "j3b-evidence", "usage-spool", "logs"} {
		if err := os.MkdirAll(filepath.Join(f.root, name), 0o750); err != nil {
			t.Fatalf("创建目录 %s 失败: %v", name, err)
		}
	}
	f.cache = miniredis.RunT(t)
	f.state = miniredis.RunT(t)
	if seedMeta {
		w1g2SeedCircuitRuntimeIndex(t, "redis://"+f.state.Addr(), w1wJ3bRedisNS)
	}
	f.healthAddr = fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
	f.mainAddr = fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
	f.j3bAddr = fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
	return f
}

// w1wOwnerEnv 组装 PG owner 完整启动 env；pairs 里的键覆盖同名基线项
// （大小写不敏感、按键全名匹配）。
func w1wOwnerEnv(t *testing.T, coverageDir string, f *w1wOwnerFixture, pairs ...string) []string {
	base := []string{
		"JUHE_AI_POSTGRES_URL=" + f.pg.DSN,
		"JUHE_AI_BUSINESS_POSTGRES_URL=" + f.pg.DSN,
		"JUHE_AI_CACHE_DRIVER=redis",
		"JUHE_AI_REDIS_CACHE_URL=redis://" + f.cache.Addr(),
		"JUHE_AI_RUNTIME_STATE_DRIVER=redis",
		"JUHE_AI_REDIS_STATE_URL=redis://" + f.state.Addr(),
		"JUHE_AI_REDIS_NAMESPACE=juhe-ai:" + w1wJ3bRedisNS,
		"JUHE_AI_SECRET=" + w1wRuntimeSecret,
		"JUHE_AI_HOST=127.0.0.1",
		fmt.Sprintf("JUHE_AI_PORT=%d", func() int {
			port := 0
			_, _ = fmt.Sscanf(f.mainAddr, "127.0.0.1:%d", &port)
			return port
		}()),
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=" + f.healthAddr,
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true",
		"JUHE_AI_AUTH_CAPTCHA_DISABLED=true",
		// business owner handoff（postgres 模式 owner 门槛事实）
		"JUHE_AI_BUSINESS_OWNER=gateway",
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
		"JUHE_AI_BUSINESS_SCHEMA_READY=true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH=" + w1wOwnerEpoch,
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH=" + w1bWriteCutoverEvidence(t, filepath.Join(f.root, "business-evidence"), w1wOwnerEpoch),
		// J3b owner（postgres 专属库 + business 目标库）
		"JUHE_AI_J3B_ENABLED=true",
		"JUHE_AI_J3B_OWNER=gateway",
		"JUHE_AI_J3B_INSTANCE_ID=w1w-owner-boot",
		"JUHE_AI_J3B_STORE=postgres",
		"JUHE_AI_J3B_POSTGRES_URL=" + f.j3b.DSN,
		"JUHE_AI_J3B_BUSINESS_POSTGRES_URL=" + f.pg.DSN,
		"JUHE_AI_J3B_CREDENTIAL_SECRET=w1w-credential-secret-value",
		"JUHE_AI_J3B_IDENTITY_SECRET=w1w-identity-secret-value",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED=true",
		"JUHE_AI_J3B_OWNER_EPOCH=" + w1wOwnerEpoch,
		"JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH=" + w1bWriteCutoverEvidence(t, filepath.Join(f.root, "j3b-evidence"), w1wOwnerEpoch),
		"JUHE_AI_J3B_SCHEMA_READY=true",
		"JUHE_AI_J3B_HEALTH_BOUNDARY_READY=true",
		"JUHE_AI_J3B_RUNTIME_READY=true",
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://" + f.state.Addr(),
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE=" + w1wJ3bRedisNS,
		"JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS=" + f.j3bAddr,
		// F3 审计（postgres 模式）
		"JUHE_AI_AUDIT_LOG_POSTGRES_URL=" + f.audit.DSN,
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL=" + f.pg.DSN,
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=" + filepath.Join(f.root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=" + filepath.Join(f.root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1w-owner-boot",
		// F4 操作日志（postgres 模式）
		"JUHE_AI_OPERATION_LOG_POSTGRES_URL=" + f.oper.DSN,
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1w-owner-boot",
		// chain 协作面（spool / 日志 grep 目录）
		"JUHE_AI_USAGE_SPOOL_DIRECTORY=" + filepath.Join(f.root, "usage-spool"),
		"JUHE_AI_LOG_DIR=" + filepath.Join(f.root, "logs"),
	}
	overridden := map[string]bool{}
	for _, pair := range pairs {
		overridden[strings.SplitN(pair, "=", 2)[0]] = true
	}
	env := make([]string, 0, len(base)+len(pairs)+2)
	for _, entry := range base {
		if overridden[strings.SplitN(entry, "=", 2)[0]] {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, pairs...)
	return w1bScenarioEnv(t, coverageDir, env...)
}

// w1wStartOwner 启动插桩 owner 进程并返回等待通道。
func w1wStartOwner(t *testing.T, exe string, env []string, total time.Duration) (*exec.Cmd, chan error, *bytes.Buffer, *bytes.Buffer, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), total)
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = env
	w1bSetNewProcessGroup(cmd)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("启动 owner 进程失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return cmd, done, stdout, stderr, cancel
}

// w1wPollReady 轮询 /health 就绪（ready=true），失败即杀进程并 Fatal。
func w1wPollReady(t *testing.T, address string, done chan error, cancel context.CancelFunc, stdout, stderr *bytes.Buffer, timeout time.Duration) bool {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + address + "/health")
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				var payload map[string]any
				if json.Unmarshal(body, &payload) == nil && payload["ready"] == true {
					return true
				}
			}
		}
		select {
		case waitErr := <-done:
			cancel()
			t.Fatalf("owner 网关在健康就绪前退出: %v\nstdout=%s\nstderr=%s", waitErr, stdout.String(), stderr.String())
		case <-time.After(200 * time.Millisecond):
		}
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	cancel()
	t.Fatalf("健康端点 %s 在 %s 内未就绪\nstdout=%s\nstderr=%s", address, timeout, stdout.String(), stderr.String())
	return false
}

// w1wShutdownGraceful 发送 CTRL_BREAK 并断言 exit 0 + 无 fail 输出。
func w1wShutdownGraceful(t *testing.T, cmd *exec.Cmd, done chan error, cancel context.CancelFunc, stdout, stderr *bytes.Buffer, stopTimeout time.Duration) {
	t.Helper()
	if err := w1bSendCtrlBreak(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("GenerateConsoleCtrlEvent(CTRL_BREAK) 失败: %v", err)
	}
	select {
	case waitErr := <-done:
		cancel()
		exitCode := 0
		if waitErr != nil {
			exitErr, ok := waitErr.(*exec.ExitError)
			if !ok {
				t.Fatalf("等待进程退出失败: %v", waitErr)
			}
			exitCode = exitErr.ExitCode()
		}
		if exitCode != 0 {
			t.Fatalf("优雅关闭期望 exit 0，实际 %d\nstdout=%s\nstderr=%s", exitCode, stdout.String(), stderr.String())
		}
		combined := stdout.String() + stderr.String()
		for _, marker := range w1v2OwnerFailMarkers {
			if strings.Contains(combined, marker) {
				t.Fatalf("优雅关闭路径出现 fail 输出 %q\nstdout=%s\nstderr=%s", marker, stdout.String(), stderr.String())
			}
		}
	case <-time.After(stopTimeout):
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("CTRL_BREAK 后 %s 内未退出，已强杀\nstdout=%s\nstderr=%s", stopTimeout, stdout.String(), stderr.String())
	}
}

// ---------------------------------------------------------------------------
// 场景 A：完整启动（captcha 臂 + Go 运行时采样器臂）+ 场景 D（scheduler 闭包）
// ---------------------------------------------------------------------------

// w1wInsertSchedulerTask 已随场景 D 改版废弃：scheduled/quality_recovery
// 两种 kind 由业务库 model_quality_schedules / account_quality_enforcements
// 现算 payload，不读 J3b 专属库的任务表。

func TestW1WOwnerFullBootTailArms(t *testing.T) {
	exe := w1bBuildCoverBinary(t)
	f := w1wNewOwnerFixture(t, true, true)

	// 场景 D fixture：scheduled/quality_recovery 两种 kind 不读
	// model_check_scheduler_tasks，而是从业务库 model_quality_schedules JOIN
	// accounts（scheduled）与 account_quality_enforcements JOIN accounts
	// （quality_recovery）现算 payload。插入真实可认领的行，使 main.go
	// SchedulerFactory 的 build 闭包在 1s 节拍内被执行：
	//   - w1w-acc（active）+ 到期 schedule（模型 gpt-4o）→ resolve 通过、
	//     revision 与账户一致（323 最终返回）；
	//   - w1w-acc + 未知模型 schedule → resolve 失败（316-317）；
	//   - w1w-acc-iso（quality_isolated）+ 到期 enforcement → 313-315
	//     （EnforcementID 非空走 quality_recovery trigger）。
	nowText := time.Now().UTC().Format(time.RFC3339Nano)
	pastText := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	seedStatements := []string{
		`INSERT INTO juhe_business.accounts (id,system_account_id,vendor_code,connection_driver_code,protocol_code,protocol_version,name,type,status,credentials_encrypted,health_check_model,health_check_endpoint_mode,config_revision,dispatch_revision,created_at,updated_at)
			VALUES ('w1w-acc','sys_admin',NULL,'generic_http','openai','v1','w1w-account','api_key','active','w1w-encrypted','gpt-4o','chat_json',42,7,'` + nowText + `','` + nowText + `')`,
		`INSERT INTO juhe_business.accounts (id,system_account_id,vendor_code,connection_driver_code,protocol_code,protocol_version,name,type,status,credentials_encrypted,health_check_model,health_check_endpoint_mode,config_revision,dispatch_revision,created_at,updated_at)
			VALUES ('w1w-acc-iso','sys_admin',NULL,'generic_http','openai','v1','w1w-account-isolated','api_key','quality_isolated','w1w-encrypted','gpt-4o','chat_json',11,3,'` + nowText + `','` + nowText + `')`,
		`INSERT INTO juhe_business.model_quality_schedules (id,system_account_id,account_id,model,enabled,revision,next_run_at,created_at,updated_at)
			VALUES ('w1w-sched-ok','sys_admin','w1w-acc','gpt-4o',1,1,'` + pastText + `','` + nowText + `','` + nowText + `')`,
		`INSERT INTO juhe_business.accounts (id,system_account_id,vendor_code,connection_driver_code,protocol_code,protocol_version,name,type,status,credentials_encrypted,health_check_model,health_check_endpoint_mode,config_revision,dispatch_revision,created_at,updated_at)
			VALUES ('w1w-acc-bad','sys_admin',NULL,'generic_http','openai','v1','w1w-account-bad','api_key','active','w1w-encrypted','gpt-4o','chat_json',13,2,'` + nowText + `','` + nowText + `')`,
		`INSERT INTO juhe_business.model_quality_schedules (id,system_account_id,account_id,model,enabled,revision,next_run_at,created_at,updated_at)
			VALUES ('w1w-sched-bad-model','sys_admin','w1w-acc-bad','w1w-nonexistent-model',1,1,'` + pastText + `','` + nowText + `','` + nowText + `')`,
		`INSERT INTO juhe_business.account_quality_enforcements (account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,policy_revision,profile,account_config_revision,before_status,after_status,started_at,recovery_due_at,recovery_model,created_at,updated_at)
			VALUES ('w1w-acc-iso','sys_admin','w1w-enforcement-1',1,'active','quality_isolate','w1w-trigger-run',0,'quick',11,'active','quality_isolated','` + nowText + `','` + pastText + `','gpt-4o','` + nowText + `','` + nowText + `')`,
	}
	for _, statement := range seedStatements {
		if _, execErr := f.pg.DB.Exec(statement); execErr != nil {
			t.Fatalf("执行调度 fixture DML 失败: %v\nstatement=%s", execErr, statement)
		}
	}

	// 场景 A：不设 JUHE_AI_AUTH_CAPTCHA_DISABLED（336-338 captcha 臂）。
	// （Go 运行时指标采样器组件臂已随监控改造删除：gometrics 采样与
	// JUHE_AI_GO_RUNTIME_METRICS_* env 家族整体退役，promhttp /metrics 取代。）
	coverageDir := w1bCoverageDir(t, "W1W-owner-tail-boot")
	env := w1wOwnerEnv(t, coverageDir, f,
		// 置空覆盖基线的 captcha 关闭项：envBool("") = false → 子进程装配
		// captcha 服务（main.go 336-338）。
		"JUHE_AI_AUTH_CAPTCHA_DISABLED=",
	)
	cmd, done, stdout, stderr, cancel := w1wStartOwner(t, exe, env, 120*time.Second)
	if !w1wPollReady(t, f.healthAddr, done, cancel, stdout, stderr, 45*time.Second) {
		return
	}
	// 给 1s 节拍的调度器留出 claim + 执行 build 闭包的时间。
	time.Sleep(4 * time.Second)
	w1bAppendCoverageManifest(t, coverageDir)
	w1wShutdownGraceful(t, cmd, done, cancel, stdout, stderr, 30*time.Second)
	// 诊断：回读调度计划执行状态，确认调度器确实 claim 并执行了 build 闭包。
	rows, queryErr := f.pg.DB.Query(`SELECT id,last_run_id,last_run_status FROM juhe_business.model_quality_schedules ORDER BY id`)
	if queryErr == nil {
		for rows.Next() {
			var id string
			var lastRunID, lastRunStatus sql.NullString
			_ = rows.Scan(&id, &lastRunID, &lastRunStatus)
			t.Logf("调度计划 %s last_run_id=%v last_run_status=%v", id, lastRunID, lastRunStatus)
		}
		rows.Close()
	} else {
		t.Logf("回读调度计划失败: %v", queryErr)
	}
	t.Logf("场景 A/D 完成: captcha 臂 + 调度闭包（health=%s main=%s j3b=%s）", f.healthAddr, f.mainAddr, f.j3bAddr)
}

// ---------------------------------------------------------------------------
// 场景 B：J3b 装配段 fail-fast 臂（表驱动单点破坏）
// ---------------------------------------------------------------------------

func TestW1WOwnerJ3bFailFastArms(t *testing.T) {
	w1bBuildCoverBinary(t)

	// 场景表说明（2026-09-30 C1 切流门禁退役后）：
	//   - timezone-missing / j3b-host-schema 两臂已删除——它们断言的是
	//     严格切流模式（J3b 七证显式配置、专属库外部预置、SCHEMA_READY
	//     门禁）的 fail-fast；C1 后 AutoClaimed 恒真，专属库由组合根
	//     ensureJ3bDedicatedSQLiteBootstrap 幂等自举（main.go），装配对
	//     自举产物必然成功，两臂从设计上不可达（实测进程完整启动）。
	//   - 保留的三个臂失败点不被自举修复：非法 env 值（retention）、
	//     非法 Redis URL（circuit-url）、fence 未 ready（circuit-meta）。
	scenarios := []struct {
		name       string
		prepareJ3b bool
		seedMeta   bool
		mutate     func(t *testing.T, f *w1wOwnerFixture) []string
		wantStderr string
	}{
		{
			name: "retention-interval", prepareJ3b: true, seedMeta: true,
			mutate: func(t *testing.T, f *w1wOwnerFixture) []string {
				return []string{"JUHE_AI_SESSION_RETENTION_INTERVAL=not-a-duration"}
			},
			wantStderr: "load J3b Gateway session retention config",
		},
		{
			// 命名空间留空时 LoadConfig 回落 JUHE_AI_REDIS_NAMESPACE，因此改用
			// 非法 URL：circuitruntime.New 的 NewClient 解析失败（210-212）。
			name: "circuit-url-invalid", prepareJ3b: true, seedMeta: true,
			mutate: func(t *testing.T, f *w1wOwnerFixture) []string {
				return []string{"JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://[::bad-w1w]:9/0"}
			},
			wantStderr: "create J3b Gateway circuit runtime owner",
		},
		{
			name: "circuit-meta-missing", prepareJ3b: true, seedMeta: false,
			mutate:     func(t *testing.T, f *w1wOwnerFixture) []string { return nil },
			wantStderr: "verify J3b Gateway circuit runtime owner fence",
		},
		// management-port-occupied 臂已删除（2026-09-21 起管理面同权挂主端口，
		// 别名端口被占只告警不阻断启动）——降级语义由下方的
		// TestW1WManagementAliasDemotesWhenPortTaken 承载。
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			f := w1wNewOwnerFixture(t, scenario.prepareJ3b, scenario.seedMeta)
			pairs := scenario.mutate(t, f)
			coverageDir := w1bCoverageDir(t, "W1W-j3b-arm-"+scenario.name)
			env := w1wOwnerEnv(t, coverageDir, f, pairs...)
			_, stderr, code := w1bRunScenario(t, "W1W-j3b-arm-"+scenario.name, env)
			w1bRequireExitCode(t, scenario.name, code, 1)
			w1bRequireContains(t, scenario.name, stderr, scenario.wantStderr)
		})
	}
}

// ---------------------------------------------------------------------------
// 场景 B1b：J3b 契约检查失败臂（真实 dev PG 临时子库，不可达即 Skip）。
// SQLite 变体无法区分“连接打开的 schema 门禁”与后续契约检查（表缺失在
// OpenBusinessTargetConnection 就被拦下），改用 PG 权限收缩：schema 门禁读
// 目录不受影响，契约检查按列/权限真实失败。
// ---------------------------------------------------------------------------

// w1wJ3bPostgresContractEnv 组装 PG 业务模式 + J3b postgres 专属库的 J3b
// 装配 env。2026-09-21 起组合根恒开（SYSTEM_API/CHAIN 开关移除），业务
// POSTGRES_URL 触发严格 owner 门禁：补全业务 owner 事实通过门禁，让失败
// 仍发生在 J3b 契约校验（compose 之前的装配段）。
func w1wJ3bPostgresContractEnv(t *testing.T, coverageDir, tempAppURL, evidencePath, stateRedisAddr string) []string {
	t.Helper()
	return w1bScenarioEnv(t, coverageDir,
		"JUHE_AI_RUNTIME_MODE=performance",
		"JUHE_AI_DATABASE_DRIVER=postgres",
		"JUHE_AI_POSTGRES_URL="+tempAppURL,
		"JUHE_AI_BUSINESS_POSTGRES_URL="+tempAppURL,
		"JUHE_AI_BUSINESS_OWNER=gateway",
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
		"JUHE_AI_BUSINESS_SCHEMA_READY=true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH="+w1wOwnerEpoch,
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH="+evidencePath,
		"JUHE_AI_CACHE_DRIVER=memory",
		"JUHE_AI_RUNTIME_STATE_DRIVER=memory",
		"JUHE_AI_SECRET=w1w-pg-contract-secret",
		"JUHE_AI_J3B_ENABLED=true",
		"JUHE_AI_J3B_OWNER=gateway",
		"JUHE_AI_J3B_INSTANCE_ID=w1w-pg-contract",
		"JUHE_AI_J3B_STORE=postgres",
		"JUHE_AI_J3B_POSTGRES_URL="+tempAppURL,
		"JUHE_AI_J3B_BUSINESS_POSTGRES_URL="+tempAppURL,
		"JUHE_AI_J3B_CREDENTIAL_SECRET=w1w-credential-secret-value",
		"JUHE_AI_J3B_IDENTITY_SECRET=w1w-identity-secret-value",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED=true",
		"JUHE_AI_J3B_OWNER_EPOCH="+w1wOwnerEpoch,
		"JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH="+evidencePath,
		"JUHE_AI_J3B_SCHEMA_READY=true",
		"JUHE_AI_J3B_HEALTH_BOUNDARY_READY=true",
		"JUHE_AI_J3B_RUNTIME_READY=true",
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://"+stateRedisAddr,
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE="+w1wJ3bRedisNS,
	)
}

func TestW1WOwnerJ3bPostgresContractArms(t *testing.T) {
	w1bBuildCoverBinary(t)
	tempAppURL := w1g2CoverPostgres(t)
	envFile := w1pgEnvFile(t)
	parsed, err := url.Parse(tempAppURL)
	if err != nil {
		t.Fatalf("解析临时子库 URL 失败: %v", err)
	}
	appRole := parsed.User.Username()
	if appRole == "" {
		t.Skipf("临时子库 URL 未携带角色（跳过权限收缩臂）")
	}
	adminDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		envFile["DEV_POSTGRES_ADMIN_USERNAME"], envFile["DEV_POSTGRES_ADMIN_PASSWORD"],
		envFile["DEV_POSTGRES_HOST"], envFile["DEV_POSTGRES_DIRECT_PORT"], w1coverDB)
	adminDB, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("打开 dev PG 管理连接失败: %v", err)
	}
	defer adminDB.Close()
	if err := adminDB.Ping(); err != nil {
		t.Skipf("dev PG 管理口不可达（跳过权限收缩臂）: %v", err)
	}
	db := w1g2OpenApp(t, tempAppURL)
	w1g2EnsureJ3bSchema(t, db)

	scenarios := []struct {
		name       string
		revoke     string
		restore    string
		wantStderr string
	}{
		{
			// 174-176：auth 契约的 PG UPDATE 表权限检查失败。
			name:       "auth-contract",
			revoke:     `REVOKE UPDATE ON TABLE juhe_business.system_sessions FROM ` + appRole,
			restore:    `GRANT UPDATE ON TABLE juhe_business.system_sessions TO ` + appRole,
			wantStderr: "verify J3b Gateway auth contract",
		},
		{
			// 206-208：circuit 契约读 account_circuit_incidents 被拒。
			name:       "circuit-contract",
			revoke:     `REVOKE SELECT ON TABLE juhe_business.account_circuit_incidents FROM ` + appRole,
			restore:    `GRANT SELECT ON TABLE juhe_business.account_circuit_incidents TO ` + appRole,
			wantStderr: "verify J3b Gateway circuit control-plane contract",
		},
		{
			// 280-282：scheduler 契约读 model_quality_schedules 被拒。
			name:       "scheduler-contract",
			revoke:     `REVOKE SELECT ON TABLE juhe_business.model_quality_schedules FROM ` + appRole,
			restore:    `GRANT SELECT ON TABLE juhe_business.model_quality_schedules TO ` + appRole,
			wantStderr: "verify J3b Gateway scheduler contract",
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			if _, err := adminDB.Exec(scenario.revoke); err != nil {
				t.Skipf("权限收缩不可执行（%s）: %v", scenario.revoke, err)
			}
			t.Cleanup(func() {
				if _, err := adminDB.Exec(scenario.restore); err != nil {
					t.Errorf("恢复权限失败（%s）: %v", scenario.restore, err)
				}
			})
			f := w1wNewOwnerFixture(t, true, true)
			coverageDir := w1bCoverageDir(t, "W1W-pg-contract-"+scenario.name)
			env := w1wJ3bPostgresContractEnv(t, coverageDir, tempAppURL,
				w1bWriteCutoverEvidence(t, filepath.Join(f.root, "j3b-evidence"), w1wOwnerEpoch), f.state.Addr())
			_, stderr, code := w1bRunScenario(t, "W1W-pg-contract-"+scenario.name, env)
			w1bRequireExitCode(t, scenario.name, code, 1)
			w1bRequireContains(t, scenario.name, stderr, scenario.wantStderr)
		})
	}
}

// TestW1WManagementAliasDemotesWhenPortTaken 覆盖 2026-09-21 起的别名降级
// 语义：模型检测管理面同权挂主端口后，3307 兼容别名端口被占只告警不阻断
// 启动。断言：别名告警出现在 stdout（slog）、进程保持存活、主端口实际服务
// 模型检测管理面（未认证 /model-checks/run/active 得 401/403 而非 404）。
func TestW1WManagementAliasDemotesWhenPortTaken(t *testing.T) {
	w1bBuildCoverBinary(t)
	alias, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用别名端口失败: %v", err)
	}
	defer alias.Close()
	mainPort := w1bFreePort(t)
	healthPort := w1bFreePort(t)
	coverageDir := w1bCoverageDir(t, "W1W-alias-demoted")
	env := w1bScenarioEnv(t, coverageDir,
		"JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS="+alias.Addr().String(),
		"JUHE_AI_HOST=127.0.0.1",
		fmt.Sprintf("JUHE_AI_PORT=%d", mainPort),
		fmt.Sprintf("JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:%d", healthPort))
	cmd, done, cancel, stdout, stderr := w1mStartOwnerProcess(t, coverageDir, env)
	if !w1mWaitOutputContains(t, stdout, "J3b management alias endpoint", 30*time.Second) {
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("别名降级告警未在 30s 内出现，stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for {
		response, probeErr := client.Get(fmt.Sprintf("http://127.0.0.1:%d/model-checks/run/active", mainPort))
		if probeErr == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
				break
			}
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			<-done
			t.Fatalf("主端口未在 30s 内服务模型检测管理面（最后一次探测 err=%v，stdout=%s stderr=%s）", probeErr, stdout.String(), stderr.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	w1mGracefulShutdownOwner(t, cmd, done, cancel, coverageDir, "W1W-alias-demoted")
}

// ---------------------------------------------------------------------------
// 场景 B2：F3/F4 EnsureSchema 失败臂（378-380、425-427）——PG store 的
// OpenStore 惰性不连接，EnsureSchema 首次连库时对不可达 URL 确定性失败。
// ---------------------------------------------------------------------------

func TestW1WOwnerF3F4EnsureSchemaArms(t *testing.T) {
	w1bBuildCoverBinary(t)
	root := t.TempDir()
	for _, name := range []string{"audit-blobs", "audit-hot", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o750); err != nil {
			t.Fatalf("创建目录 %s 失败: %v", name, err)
		}
	}
	business := w1mBusinessPostgres(t)
	args := []string{"-health-listen-address", fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))}
	auditBase := []string{
		"JUHE_AI_AUDIT_LOG_POSTGRES_URL=postgres://127.0.0.1:1/w1w_unused",
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL=postgres://127.0.0.1:1/w1w_unused",
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=" + filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=" + filepath.Join(root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1w-f3-schema",
	}

	// F3 PG EnsureSchema 连接失败臂（378-380）。
	coverageDir := w1bCoverageDir(t, "W1W-f3-schema-fail")
	env := w1bScenarioEnv(t, coverageDir, append(append([]string{}, auditBase...),
		"JUHE_AI_POSTGRES_URL="+business.DSN,
		"JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN)...)
	_, stderr, code := w1bRunScenario(t, "W1W-f3-schema-fail", env, args...)
	w1bRequireExitCode(t, "f3-schema-fail", code, 1)
	w1bRequireContains(t, "f3-schema-fail", stderr, "initialize F3 audit-log schema")

	// F4 PG EnsureSchema 连接失败臂（425-427）：F3 用可用库让启动推进到
	// F4 EnsureSchema，再以不可达 URL 命中失败。
	coverageDir = w1bCoverageDir(t, "W1W-f4-schema-fail")
	auditOK := pgtestkit.NewTestDatabase(t)
	env = w1bScenarioEnv(t, coverageDir,
		"JUHE_AI_AUDIT_LOG_POSTGRES_URL="+auditOK.DSN,
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL="+business.DSN,
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY="+filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY="+filepath.Join(root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1w-f4-schema",
		"JUHE_AI_OPERATION_LOG_POSTGRES_URL=postgres://127.0.0.1:1/w1w_unused",
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1w-f4-schema",
		"JUHE_AI_POSTGRES_URL="+business.DSN,
		"JUHE_AI_BUSINESS_POSTGRES_URL="+business.DSN)
	_, stderr, code = w1bRunScenario(t, "W1W-f4-schema-fail", env, args...)
	w1bRequireExitCode(t, "f4-schema-fail", code, 1)
	w1bRequireContains(t, "f4-schema-fail", stderr, "initialize F4 operation-log schema")
}

// 场景 B2 补充（369-371 / 408-410 / 388-390 / 442-444 / 514-516）记录性跳过：
//   - 369-371 / 408-410（共享 PG 池打开失败）：pgpool.Acquire 的错误臂是空
//     URL/role、池上限非法与 open() 同步失败；前两者被 auditlog/operationlog
//     LoadConfig 内的 sqlpool.ValidatePoolLimits 先行拦截，sql.Open("pgx")
//     惰性解析使不可达 DSN 拖到 EnsureSchema 才失败（上一场景正是利用该
//     语义覆盖 378-380 / 425-427）。
//   - 388-390 / 442-444（租约获取错误臂）：租约表由 EnsureSchema 刚建好，
//     错误需要启动中途的 DB 故障注入，无确定性缝（held 臂 445-447 已由
//     TestW1WOwnerF4LeaseHeldPostgres 覆盖）。
//   - 514-516（F4 组件私有租约获取错误臂）：同上，需要组件运行中途的
//     DB 故障注入。无可达触发缝。
// 场景 B3（620-622 内部网关注册表错误臂）记录性跳过：NewRegistry 的三个
// 错误臂（空 URL / 空 secret / URL 解析失败）在 main 装配序里都被更早的
// 门禁拦截——loadRuntimeConfig 强制 redis state driver 下 URL 非空、secret
// 非空，而 composeSystemAPI 的 shared login guard 在注册表之前就用同一
// cfg.RedisStateURL 做 ParseURL，URL 解析失败会先以 "create shared login
// guard" 失败（已在 W1W-registry-error 试跑中实证）。无可达触发缝。

// ---------------------------------------------------------------------------
// 场景 C1：关停 state Redis → circuit 组件 CheckReady 错误返回（243-245）；
// supervisor 对组件错误按退避无限重试（Run 恒返回 nil，698-700 为防御臂）
// ---------------------------------------------------------------------------

func TestW1WOwnerCircuitComponentErrorArm(t *testing.T) {
	exe := w1bBuildCoverBinary(t)
	f := w1wNewOwnerFixture(t, true, true)
	coverageDir := w1bCoverageDir(t, "W1W-meta-deleted-supervisor-fail")
	env := w1wOwnerEnv(t, coverageDir, f)
	cmd, done, stdout, stderr, cancel := w1wStartOwner(t, exe, env, 90*time.Second)
	if !w1wPollReady(t, f.healthAddr, done, cancel, stdout, stderr, 45*time.Second) {
		return
	}
	// 关停 state Redis：circuit runtime 组件 1s 节拍的 CheckReady/Ping 失败，
	// 组件闭包里的错误返回（243-245）被覆盖；supervisor 对组件错误按退避
	// 无限重试并保持进程存活，因此这里只等待节拍触达失败，再走优雅关闭。
	f.state.Close()
	time.Sleep(3500 * time.Millisecond)
	// 组件错误已被 1s 节拍触达（243-245 覆盖）；CTRL_BREAK 走优雅关闭。
	if err := w1bSendCtrlBreak(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("GenerateConsoleCtrlEvent(CTRL_BREAK) 失败: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case wErr := <-done:
			exitCode := 0
			if wErr != nil {
				exitErr, ok := wErr.(*exec.ExitError)
				if !ok {
					cancel()
					t.Fatalf("等待进程退出失败: %v", wErr)
				}
				exitCode = exitErr.ExitCode()
			}
			cancel()
			if exitCode != 0 {
				t.Fatalf("组件错误被 supervisor 重试，优雅关闭期望 exit 0，实际 %d\nstdout=%s\nstderr=%s", exitCode, stdout.String(), stderr.String())
			}
			// supervisor 的组件日志走 main 的 JSON slog（stdout）。
			w1bRequireContains(t, "meta-deleted", stdout.String(), "sidecar component failed; retrying")
			w1bAppendCoverageManifest(t, coverageDir)
			t.Logf("场景 C1 完成: state Redis 关停后组件 CheckReady 失败返回（243-245），supervisor 重试后优雅关闭")
			return
		case <-time.After(500 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			<-done
			cancel()
			t.Fatalf("state Redis 关停后 30s 内进程未退出\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
		}
	}
}

// ---------------------------------------------------------------------------
// 场景 C2：部分请求头挂住 main server → Shutdown 超时错误臂（676-678）
// ---------------------------------------------------------------------------

func TestW1WOwnerMainServerShutdownTimeout(t *testing.T) {
	exe := w1bBuildCoverBinary(t)
	f := w1wNewOwnerFixture(t, true, true)
	coverageDir := w1bCoverageDir(t, "W1W-main-shutdown-timeout")
	env := w1wOwnerEnv(t, coverageDir, f)
	cmd, done, stdout, stderr, cancel := w1wStartOwner(t, exe, env, 120*time.Second)
	if !w1wPollReady(t, f.healthAddr, done, cancel, stdout, stderr, 45*time.Second) {
		return
	}
	// 完整请求头 + 声明 512 字节 body 但只发 10 字节：auth login handler
	// 阻塞在 body 读取（main server 无 body 超时），请求保持 active；
	// CTRL_BREAK 后 mainServer.Shutdown 等 10s 必然超时报错 → fail（676-678）。
	conn, dialErr := net.DialTimeout("tcp", f.mainAddr, 3*time.Second)
	if dialErr != nil {
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("连接 main 端口失败: %v", dialErr)
	}
	request := "POST /__aisys__/api/auth/login HTTP/1.1\r\n" +
		"Host: w1w\r\n" +
		"Content-Type: application/json\r\n" +
		"Content-Length: 512\r\n\r\n" +
		`{"username":"w`
	if _, writeErr := conn.Write([]byte(request)); writeErr != nil {
		_ = conn.Close()
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("写部分请求头失败: %v", writeErr)
	}
	defer func() { _ = conn.Close() }()
	time.Sleep(300 * time.Millisecond)
	if err := w1bSendCtrlBreak(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("CTRL_BREAK 失败: %v", err)
	}
	select {
	case waitErr := <-done:
		cancel()
		exitCode := 0
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				t.Fatalf("等待进程退出失败: %v", waitErr)
			}
		}
		if exitCode != 1 {
			t.Fatalf("期望 Shutdown 超时 fail（exit 1），实际 %d\nstdout=%s\nstderr=%s", exitCode, stdout.String(), stderr.String())
		}
		w1bRequireContains(t, "main-shutdown-timeout", stderr.String(), "shutdown gateway system api endpoint")
		w1bAppendCoverageManifest(t, coverageDir)
		t.Logf("场景 C2 完成: main server Shutdown 超时错误臂覆盖")
	case <-time.After(45 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("Shutdown 超时场景 45s 内未退出\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
	}
}

// 场景 E（原 F3/F4 legacy sqlite 迁移结果编码失败臂）已随迁移命令与
// sqlite 存储形态退役删除（PLAN-20261024T051925945Z D9：无 PG 对应物）。

// ---------------------------------------------------------------------------
// 场景 B4：F4 PG 租约被占（445-447）——真实 dev PG 临时子库，不可达即 Skip
// ---------------------------------------------------------------------------

func TestW1WOwnerF4LeaseHeldPostgres(t *testing.T) {
	tempAppURL := w1g2CoverPostgres(t)
	// 先确保 F4 PG schema 就绪，再预插一条未过期的他者租约。
	fixtureStore, err := operationlog.OpenStore(operationlog.Config{
		Enabled:              true,
		InstanceID:           "w1w-f4-lease-fixture",
		PostgresURL:          tempAppURL,
		PostgresMaxOpenConns: 4,
		PostgresMaxIdleConns: 2,
		OwnerLease:           30 * time.Second,
	})
	if err != nil {
		t.Fatalf("打开 F4 fixture store 失败: %v", err)
	}
	if err := fixtureStore.EnsureSchema(context.Background()); err != nil {
		_ = fixtureStore.Close()
		t.Fatalf("确保 F4 fixture schema 失败: %v", err)
	}
	if err := fixtureStore.Close(); err != nil {
		t.Fatalf("关闭 F4 fixture store 失败: %v", err)
	}
	db := w1g2OpenApp(t, tempAppURL)
	// 持久子库的 juhe_j3b schema 会因漂移重灌而缺表（本测试的子进程走零配置
	// PG J3b，OpenHost 处 CheckSchema 只校验不自建），显式补建保证场景自洽。
	w1g2EnsureJ3bSchema(t, db)
	if _, err := db.Exec(`INSERT INTO juhe_dataset.operation_log_owner_leases (lease_key,owner_id,fence_token,lease_until,updated_at)
		VALUES ('f4-operation-log-persistence','w1w-other-owner',1, clock_timestamp() + INTERVAL '1 hour', clock_timestamp())
		ON CONFLICT (lease_key) DO UPDATE SET owner_id='w1w-other-owner', lease_until = clock_timestamp() + INTERVAL '1 hour', updated_at = clock_timestamp()`); err != nil {
		t.Fatalf("预插 F4 他者租约失败: %v", err)
	}
	// 预插租约带 1 小时有效期：不清理会污染同一持久子库上后续运行的
	// TestW1G2ComposeSystemAPIPostgresSuccess（同键租约在有效期内不可获取）。
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM juhe_dataset.operation_log_owner_leases WHERE lease_key = 'f4-operation-log-persistence'`); err != nil {
			t.Errorf("清理预插 F4 租约失败: %v", err)
		}
		_ = db.Close()
	})

	w1bBuildCoverBinary(t)
	root := t.TempDir()
	for _, name := range []string{"audit-blobs", "audit-hot", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o750); err != nil {
			t.Fatalf("创建目录 %s 失败: %v", name, err)
		}
	}
	// F3 审计走独立 pgtestkit 库；业务 settings 镜像指向 fresh 种子业务库。
	business := w1mBusinessPostgres(t)
	audit := pgtestkit.NewTestDatabase(t)
	healthPort := w1bFreePort(t)
	coverageDir := w1bCoverageDir(t, "W1W-f4-lease-held")
	env := w1bScenarioEnv(t, coverageDir,
		"JUHE_AI_POSTGRES_URL="+tempAppURL,
		"JUHE_AI_BUSINESS_POSTGRES_URL="+tempAppURL,
		"JUHE_AI_SECRET=w1w-f4-lease-secret",
		"JUHE_AI_LOG_DIR="+filepath.Join(root, "logs"),
		"JUHE_AI_BUSINESS_OWNER=gateway",
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
		"JUHE_AI_BUSINESS_SCHEMA_READY=true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH="+w1wOwnerEpoch,
		"JUHE_AI_AUDIT_LOG_POSTGRES_URL="+audit.DSN,
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL="+business.DSN,
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY="+filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY="+filepath.Join(root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1w-f4-lease",
		"JUHE_AI_OPERATION_LOG_POSTGRES_URL="+tempAppURL,
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1w-f4-lease-holder",
	)
	_, stderr, code := w1bRunScenario(t, "W1W-f4-lease-held", env,
		"-health-listen-address", fmt.Sprintf("127.0.0.1:%d", healthPort))
	w1bRequireExitCode(t, "f4-lease-held", code, 1)
	w1bRequireContains(t, "f4-lease-held", stderr, "F4 operation log owner lease held by another owner process")
}

// ---------------------------------------------------------------------------
// F：chain_runtime.go 进程内——本地屏蔽并发投影闭包（510-512）
// ---------------------------------------------------------------------------

func TestW1WChainRuntimeSuppressionConcurrencyClosure(t *testing.T) {
	stateRedis := miniredis.RunT(t)
	composed := &composition{
		db:        w1uOpenSeededBusinessPG(t),
		statsDB:   nil,
		pgDialect: true,
		Bus:       inval.New(time.Now),
	}
	composed.statsDB = composed.db
	cfg := runtimeConfig{
		// PG+Redis 恒定形态（存储形态收口）：redis 端点由 miniredis 承载；
		// db 句柄走 pgtestkit fresh DDL 夹具（PG 唯一路径）。
		RedisCacheURL:                 "redis://" + stateRedis.Addr(),
		RedisStateURL:                 "redis://" + stateRedis.Addr(),
		RedisNamespace:                "w1w-chain-ns",
		Secret:                        "w1w-chain-runtime-secret",
		DispatchAccountCandidateLimit: 5000,
		ConcurrencyGlobalMax:          5000,
	}
	services, err := composeChainRuntimeServices(composed, cfg, w1tSettingValue)
	if err != nil {
		t.Fatalf("组合失败: %v", err)
	}
	defer services.Close()
	if services.SuppressionStore == nil || services.AccountAPIKeyEffects == nil {
		t.Fatalf("组合根关键字段缺失: suppression=%v effects=%v", services.SuppressionStore != nil, services.AccountAPIKeyEffects != nil)
	}

	key := "acc-w1w-suppression"
	// 1ms 时长的本地屏蔽（立即过期）→ 过滤时取得半开租约（状态转为
	// half-open，租约 180s 内有效）→ 再次过滤时 cleanup 与 blocking 评估路径
	// 都会调用并发投影闭包（chain_runtime.go 510-512，内存 tracker 并发恒 0）。
	services.SuppressionStore.Suppress(key, 1, "w1w-reason", gatewaycircuit.AvailabilityStatusLocalSuppressed, nil)
	time.Sleep(20 * time.Millisecond)
	accounts := []gatewaycircuit.SuppressibleAccount{{SuppressibleGatewayAccount: gatewaycircuit.SuppressibleGatewayAccount{ID: key}}}
	options := gatewaycircuit.SuppressionFilterOptions{AcquireHalfOpenLease: true}
	first := services.SuppressionStore.FilterSuppressions(accounts, nil, options)
	if len(first.AcquiredHalfOpenLeases) != 1 {
		t.Fatalf("首次过滤应取得半开租约: %+v", first)
	}
	second := services.SuppressionStore.FilterSuppressions(accounts, nil, options)
	// 半开租约（180s）仍然有效：blocking 评估先看租约未过期（短路），并发
	// 投影闭包由 cleanup 的 HalfOpen 分支调用；账户按屏蔽计数。
	if second.SuppressedCount != 1 {
		t.Fatalf("半开租约有效期内账户应按屏蔽计数: %+v", second)
	}
	t.Logf("chain_runtime 并发投影闭包（510-512）已驱动: first=%+v second=%+v", first.SuppressedCount, second.SuppressedCount)
}

// ---------------------------------------------------------------------------
// F：chain_runtime.go 进程内——限流/协调器 warn 闭包（368-370、376-378）
// ---------------------------------------------------------------------------

func TestW1WChainRuntimeWarnClosures(t *testing.T) {
	composed := &composition{
		db:        w1uOpenSeededBusinessPG(t),
		statsDB:   nil,
		pgDialect: true,
		Bus:       inval.New(time.Now),
	}
	composed.statsDB = composed.db
	stateRedis := miniredis.RunT(t)
	cfg := runtimeConfig{
		RedisCacheURL:                 "redis://" + stateRedis.Addr(),
		RedisStateURL:                 "redis://" + stateRedis.Addr(),
		RedisNamespace:                "juhe-ai:" + w1wJ3bRedisNS,
		Secret:                        "w1w-chain-runtime-secret",
		DispatchAccountCandidateLimit: 5000,
		ConcurrencyGlobalMax:          5000,
	}
	services, err := composeChainRuntimeServices(composed, cfg, w1tSettingValue)
	if err != nil {
		t.Fatalf("redis 状态驱动组合失败: %v", err)
	}
	defer services.Close()
	if services.StateClient == nil || services.ModelsRateLimit == nil || services.UserLimits == nil {
		t.Fatalf("redis 状态驱动协作组件缺失: state=%v models=%v userLimits=%v", services.StateClient != nil, services.ModelsRateLimit != nil, services.UserLimits != nil)
	}
	// 关闭共享 state client，使限流与协调器的 redis 操作全部失败，
	// 进而触发 composeChainRuntimeServices 内注册的 warn 闭包。
	if err := services.StateClient.Close(); err != nil {
		t.Fatalf("关闭 state client 失败: %v", err)
	}

	// 认证模型限流 redis 不可用 → warn 闭包（368-370）。
	decision, consumeErr := services.ModelsRateLimit.Consume(context.Background(), gatewaypreauth.AuthenticatedModelsRateLimitInput{
		APIKeyID: "key-w1w",
		ClientIP: "127.0.0.1",
	})
	if consumeErr != nil {
		t.Fatalf("Consume 不应返回错误（fail-closed 语义）: %v", consumeErr)
	}
	if !decision.Unavailable {
		t.Fatalf("state client 关闭后期望 Unavailable 决策: %+v", decision)
	}

	// 用户请求限制：计数器记脏 → 协调器后台同步失败 → warn 闭包（376-378）。
	// UserLimits 面是 gatewaypreauth.UserRequestLimits 接口（Consume +
	// StartCoordinator）；compose 已 StartCoordinator，后台循环按节拍同步
	// 脏桶，state client 关闭后同步必败并触发 compose 注册的 Log 闭包。
	limit := int64(100)
	_ = services.UserLimits.Consume(gatewaypreauth.UserRequestLimitConsumeInput{
		SystemAccountID: "sys-w1w",
		Settings:        gatewayruntimecache.GatewaySettings{GatewayUserRequestLimitPerMinute: &limit},
	})
	time.Sleep(5 * time.Second) // 等待协调器后台同步节拍触达失败日志。
	t.Logf("chain_runtime warn 闭包（368-370、376-378）已驱动")
}

// TestW1WChainRuntimeEmptySecretArm 覆盖空 Secret 组合 fail-fast 臂：
// 2026-10-24 存储形态收口删除 memory 电路恢复驱动后，空 Secret 的最早失败点
// 回到 accountkeyStates.NewStore 的空密钥校验（D-111 效果链 Key 指纹与凭据
// 解密强依赖 JUHE_AI_SECRET）；accountkeystates 自身的空 Secret 校验臂由
// internal/accountkeystates 覆盖。
func TestW1WChainRuntimeEmptySecretArm(t *testing.T) {
	stateRedis := miniredis.RunT(t)
	composed := &composition{
		db:        w1uOpenSeededBusinessPG(t),
		statsDB:   nil,
		pgDialect: true,
		Bus:       inval.New(time.Now),
	}
	composed.statsDB = composed.db
	cfg := runtimeConfig{
		RedisCacheURL:                 "redis://" + stateRedis.Addr(),
		RedisStateURL:                 "redis://" + stateRedis.Addr(),
		RedisNamespace:                "w1w-chain-ns",
		Secret:                        "   ",
		DispatchAccountCandidateLimit: 5000,
		ConcurrencyGlobalMax:          5000,
	}
	services, err := composeChainRuntimeServices(composed, cfg, w1tSettingValue)
	if err == nil {
		services.Close()
		t.Fatalf("空 Secret 的组合必须 fail-fast")
	}
	if !strings.Contains(err.Error(), "account api-key effects key states store") {
		t.Fatalf("期望 accountkeystates store 构造失败，实际: %v", err)
	}
	t.Logf("chain_runtime 空 Secret 失败臂（accountkeystates store）已驱动")
}

// ---------------------------------------------------------------------------
// G：chain_ports.go——downstream-closed usage 臂 + 来源级避让错误/探针臂
// ---------------------------------------------------------------------------

// w1wFailingTurnRetryStore 是 TurnRetryStateStore 的恒错实现（驱动 747-752）。
type w1wFailingTurnRetryStore struct{}

func (w1wFailingTurnRetryStore) GetJSON(context.Context, string) (json.RawMessage, error) {
	return nil, fmt.Errorf("w1w turn retry store 下线")
}
func (w1wFailingTurnRetryStore) CompareSetJSON(context.Context, string, json.RawMessage, any, int64) (bool, error) {
	return false, fmt.Errorf("w1w turn retry store 下线")
}
func (w1wFailingTurnRetryStore) Incr(context.Context, string, int64) (int64, error) {
	return 0, fmt.Errorf("w1w turn retry store 下线")
}

func TestW1WChainPortsFailureDispatcherArms(t *testing.T) {
	// 896-903：downstream-closed 分支在 usage 非空时的记录调用
	// （usage 无 dispatch → RecordFailedUpstreamAttempt 成功返回）。
	sink := &failureDispatchAuditSink{}
	dispatcher := &chainFailureDispatcher{
		affinity: &failureDispatchAffinity{},
		usage:    gatewayusage.NewService(nil, gatewayusage.ServiceConfig{}),
	}
	input := upstreamRequestErrorInput(
		&gatewaydispatch.UpstreamRequestAbortedError{Message: "请求已取消", UpstreamRequestStarted: true},
		sink,
		&gatewaydispatch.UpstreamAttempt{AccountID: "acc_1", UpstreamURL: "https://upstream.example/chat", Status: 200, HasStatus: true})
	result, err := dispatcher.HandleUpstreamRequestError(context.Background(), input)
	if err != nil {
		t.Fatalf("downstream-closed usage 记录不应失败: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("action=%s want skip_account", result.Action)
	}

	// 747-752：turnRetry 记录返回错误（失败 Store）→ warn + 短避让保持。
	strategyDeps := &gatewaycodex.ClientStrategyDeps{Source: &gatewaycodex.SourceIdentityResolver{Secret: "w1w-secret"}}
	failingTurnRetry := &gatewaycodex.TurnRetryService{Secret: "w1w-secret", Store: w1wFailingTurnRetryStore{}}
	avoidDispatcher := &chainFailureDispatcher{
		clientStrategy: strategyDeps,
		turnRetry:      failingTurnRetry,
	}
	avoidInput := avoidanceRecordRequestInput(t, &failureDispatchAuditSink{}, "gateway")
	avoidInput.AuditAttemptID = "w1w-attempt-1"
	if _, err := avoidDispatcher.HandleUpstreamRequestError(context.Background(), avoidInput); err != nil {
		t.Fatalf("避让记录失败不应上抛: %v", err)
	}

	// 760-763 + 770-774：两次失败激活避让 + 非空探针（探针恒错）→
	// goroutine 投递探活并在错误臂记录 warn。
	probe := &gatewaycodex.TurnAvoidanceProbeService{
		AccountRuntimeKey: func(gatewayruntimecache.OpenAIAccountSecret) (string, error) {
			return "", fmt.Errorf("w1w 探针运行键不可用")
		},
	}
	activatedTurnRetry := &gatewaycodex.TurnRetryService{Secret: "w1w-secret"}
	activatedDispatcher := &chainFailureDispatcher{
		clientStrategy: strategyDeps,
		turnRetry:      activatedTurnRetry,
		avoidanceProbe: probe,
	}
	for _, attempt := range []string{"w1w-attempt-a", "w1w-attempt-b"} {
		activatedInput := avoidanceRecordRequestInput(t, &failureDispatchAuditSink{}, "gateway")
		activatedInput.AuditAttemptID = attempt
		if _, err := activatedDispatcher.HandleUpstreamRequestError(context.Background(), activatedInput); err != nil {
			t.Fatalf("避让激活流程失败: %v", err)
		}
	}
	time.Sleep(300 * time.Millisecond) // 等待探针 goroutine 的错误臂执行。
	t.Logf("chain_ports failure dispatcher 臂（896-903、747-752、760-763、770-774）已驱动")
}
