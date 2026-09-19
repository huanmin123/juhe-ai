//go:build windows

package main

// w1_main_tail_test.go —— main.go / chain_runtime.go / chain_ports.go 尾部未
// 覆盖块的专项收敛测试（w1w 标识符前缀，TestW1W 入口）。复用既有插桩二进制
// 与覆盖目录帮手（w1b*）和 PG 门禁帮手（w1g2*），只新增本文件的场景。
//
// 覆盖手法总览：
//   A. main.go owner 全栈启动中段臂：sqlite 组合根 + J3b 全量 env 直接完整
//      启动，让 enforcement/tokenizer/mount/管理监听/采样器/captcha 臂自然
//      覆盖（336-338 captcha、569-574 Go 运行时采样器）。
//   B. main.go fail-fast 臂：在完整 env 基础上做单点破坏（DROP 表 / 删设置
//      行 / 非法 env / 占用端口 / 不 seed meta / 空 J3b 库），每个场景断言
//      stderr 的 fail 特征与 exit 1。
//   C. main.go 运行期臂：启动就绪后从 miniredis 删 runtime-index-meta，让
//      circuit runtime 组件在 CheckReady 处失败 → supervisor fail（243-245、
//      698-700）；部分请求头挂住 main server → Shutdown 超时（676-678）。
//   D. main.go scheduler 闭包（311-323）：向 J3b 专属库预插 due 调度任务，
//      1s 节拍的调度器 claim 后执行 SchedulerFactory 的 build 闭包。
//   E. 迁移结果编码失败（785-787、795-797）：把子进程 stdout 指向读端已关闭
//      的管道，json Encode 写入即 broken pipe → fail。
//   F. chain_runtime.go 进程内：内存驱动组合 + Bus 非空，驱动本地屏蔽存储的
//      并发投影闭包（510-512）；redis 状态驱动组合后关闭共享 state client，
//      驱动认证模型限流 warn 闭包（368-370）与用户请求限制协调器 warn 闭包
//      （376-378）。
//   G. chain_ports.go 进程内：usage 非空的 downstream-closed 分支（896-903）、
//      失败 Store 的来源级避让记录错误臂（747-752）、激活后探针错误臂
//      （760-763、770-774）。
//
// 记录性跳过（构造器防御臂，无触发缝，证据见各块注释）：
//   main.go 117-119（gateGatewayChain 恒 nil）、171-173/187-189/203-205
//   （New 只校验 nil db/非法 mode/非法 schema，main 流程恒合法）、190-192
//   （retention 契约读的 system_sessions 列集与 auth 契约首表相同，auth 先
//   败）、222-225/226-230（keymodel 与 circuitruntime 共用同一 URL/命名空间
//   与 gate，前一级先败）、232-235（projector 仅空 InstanceID 失败，而
//   LoadConfig 拒绝空 InstanceID）、268-290（enforcement/recovery/quality/
//   tokenizer/modelLimits 构造只校验 nil db）、340-348（mount 仅重复模式
//   冲突失败）、388-390/442-444/514-516（租约错误臂需要启动中途 DB 故障注入，
//   无缝）、679-681/689-691/701-706/728-733（Serve/Shutdown 错误臂需要进程外
//   关闭监听或超过 ReadHeaderTimeout 的慢 handler，健康面 handler 恒快返）。
//   chain_runtime.go 252-254/348-350/359-361/393-395/397-407 系（错误臂被
//   同 URL/同命名空间的更早构造或恒真守卫遮蔽）、415-433/443-460 系（quota
//   服务校验在 main 装配恒满足）、466-468/488-490/497-499/521-523/549-551/
//   566-568/628-630/649-658/664-666/681-683（同理，见 w1w 注释与报告）。
//   chain_ports.go 454-456/631-633/690-692/903-905（usage 服务错误臂：队列
//   容量 64MB 恒大于 2MB 估算上限，RecordFailedUpstreamAttempt 无错误路径）、
//   1444-1446（identity secret 经构造校验非空，HMAC 恒成功）、1761-1768
//   （AuditLogInput JSON round-trip 恒可逆）。

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
	"github.com/huanminabc/juhe-ai/backend-go-platform/gometrics"

	_ "modernc.org/sqlite"
)

const (
	w1wOwnerEpoch    = "epoch-w1w"
	w1wJ3bRedisNS    = "w1w"
	w1wRuntimeSecret = "w1w-owner-tail-secret"
)

// ---------------------------------------------------------------------------
// sqlite owner fixture：业务库 + J3b 专属库 + 双 miniredis + 端口
// ---------------------------------------------------------------------------

type w1wOwnerFixture struct {
	root         string
	businessPath string
	j3bPath      string
	cache        *miniredis.Miniredis
	state        *miniredis.Miniredis
	healthAddr   string
	mainAddr     string
	j3bAddr      string
}

// w1wNewOwnerFixture 组装 owner 二进制场景 fixture。prepareJ3b=false 时 J3b
// 专属库是零字节空文件（合法空 SQLite，用于 OpenHost CheckSchema 失败臂）；
// seedMeta=false 时不写 runtime-index-meta（CheckReady 失败臂）。
func w1wNewOwnerFixture(t *testing.T, prepareJ3b, seedMeta bool) *w1wOwnerFixture {
	t.Helper()
	f := &w1wOwnerFixture{root: t.TempDir()}
	f.businessPath = filepath.Join(f.root, "business.sqlite")
	w1v2PrepareBusinessSQLite(t, f.businessPath)
	f.j3bPath = filepath.Join(f.root, "j3b-dedicated.sqlite")
	if prepareJ3b {
		w1v2PrepareJ3bSQLite(t, f.j3bPath)
	} else {
		if err := os.WriteFile(f.j3bPath, nil, 0o644); err != nil {
			t.Fatalf("预建空 J3b 库失败: %v", err)
		}
	}
	for _, name := range []string{"business-evidence", "j3b-evidence", "codex-shards", "usage-shards", "audit-blobs", "audit-hot", "usage-spool", "logs"} {
		if err := os.MkdirAll(filepath.Join(f.root, name), 0o750); err != nil {
			t.Fatalf("创建目录 %s 失败: %v", name, err)
		}
	}
	f.cache = miniredis.RunT(t)
	f.state = miniredis.RunT(t)
	if seedMeta {
		w1v2SeedCircuitRuntimeIndex(t, f.state.Addr(), w1wJ3bRedisNS)
	}
	f.healthAddr = fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
	f.mainAddr = fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
	f.j3bAddr = fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
	return f
}

// w1wOwnerEnv 组装 w1v2 同款 sqlite owner 完整启动 env；pairs 里的键覆盖
// 同名基线项（大小写不敏感、按键全名匹配）。
func w1wOwnerEnv(t *testing.T, coverageDir string, f *w1wOwnerFixture, pairs ...string) []string {
	runtimeLog := filepath.Join(f.root, "runtime-log.sqlite")
	if err := os.WriteFile(runtimeLog, nil, 0o644); err != nil {
		t.Fatalf("预建 runtime-log 失败: %v", err)
	}
	base := []string{
		"JUHE_AI_RUNTIME_MODE=performance",
		"JUHE_AI_DATABASE_DRIVER=sqlite",
		"JUHE_AI_CACHE_DRIVER=redis",
		"JUHE_AI_RUNTIME_STATE_DRIVER=redis",
		"JUHE_AI_REDIS_CACHE_URL=redis://" + f.cache.Addr(),
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
		"JUHE_AI_DATABASE_PATH=" + f.businessPath,
		"JUHE_AI_CHAT_DATABASE_PATH=" + filepath.Join(f.root, "chat.sqlite"),
		"JUHE_AI_DATASET_DATABASE_PATH=" + filepath.Join(f.root, "dataset.sqlite"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH=" + filepath.Join(f.root, "usage-catalog.sqlite"),
		"JUHE_AI_STATS_DATABASE_PATH=" + filepath.Join(f.root, "stats.sqlite"),
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH=" + filepath.Join(f.root, "table-monitor.sqlite"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH=" + runtimeLog,
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT=" + filepath.Join(f.root, "codex-shards"),
		"JUHE_AI_BUSINESS_OWNER=gateway",
		"JUHE_AI_BUSINESS_DATABASE_PATH=" + f.businessPath,
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
		"JUHE_AI_BUSINESS_SCHEMA_READY=true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH=" + w1wOwnerEpoch,
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH=" + w1bWriteCutoverEvidence(t, filepath.Join(f.root, "business-evidence"), w1wOwnerEpoch),
		"JUHE_AI_J3B_ENABLED=true",
		"JUHE_AI_J3B_OWNER=gateway",
		"JUHE_AI_J3B_INSTANCE_ID=w1w-owner-boot",
		"JUHE_AI_J3B_STORE=sqlite",
		"JUHE_AI_J3B_DATABASE_PATH=" + f.j3bPath,
		"JUHE_AI_J3B_BUSINESS_DATABASE_PATH=" + f.businessPath,
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
		"JUHE_AI_AUDIT_LOG_STORE=sqlite",
		"JUHE_AI_AUDIT_LOG_DATABASE_PATH=" + filepath.Join(f.root, "audit.sqlite"),
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=" + filepath.Join(f.root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=" + filepath.Join(f.root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH=" + f.businessPath,
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1w-owner-boot",
		"JUHE_AI_OPERATION_LOG_STORE=sqlite",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH=" + filepath.Join(f.root, "operation.sqlite"),
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH=" + f.businessPath,
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1w-owner-boot",
		"JUHE_AI_USAGE_SHARD_ROOT=" + filepath.Join(f.root, "usage-shards"),
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
	businessDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(f.businessPath)+"?mode=rw&_pragma=foreign_keys(OFF)")
	if err != nil {
		t.Fatalf("打开业务库失败: %v", err)
	}
	seedStatements := []string{
		`INSERT INTO accounts (id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,protocol_version,name,type,status,credentials_encrypted,health_check_model,health_check_endpoint_mode,config_revision,dispatch_revision,created_at,updated_at)
			VALUES ('w1w-acc','w1w-sys','openai','default','openai','v1','w1w-account','api_key','active','w1w-encrypted','gpt-4o','chat_json',42,7,'` + nowText + `','` + nowText + `')`,
		`INSERT INTO accounts (id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,protocol_version,name,type,status,credentials_encrypted,health_check_model,health_check_endpoint_mode,config_revision,dispatch_revision,created_at,updated_at)
			VALUES ('w1w-acc-iso','w1w-sys','openai','default','openai','v1','w1w-account-isolated','api_key','quality_isolated','w1w-encrypted','gpt-4o','chat_json',11,3,'` + nowText + `','` + nowText + `')`,
		`INSERT INTO model_quality_schedules (id,system_account_id,account_id,model,enabled,revision,next_run_at,created_at,updated_at)
			VALUES ('w1w-sched-ok','w1w-sys','w1w-acc','gpt-4o',1,1,'` + pastText + `','` + nowText + `','` + nowText + `')`,
		`INSERT INTO accounts (id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,protocol_version,name,type,status,credentials_encrypted,health_check_model,health_check_endpoint_mode,config_revision,dispatch_revision,created_at,updated_at)
			VALUES ('w1w-acc-bad','w1w-sys','openai','default','openai','v1','w1w-account-bad','api_key','active','w1w-encrypted','gpt-4o','chat_json',13,2,'` + nowText + `','` + nowText + `')`,
		`INSERT INTO model_quality_schedules (id,system_account_id,account_id,model,enabled,revision,next_run_at,created_at,updated_at)
			VALUES ('w1w-sched-bad-model','w1w-sys','w1w-acc-bad','w1w-nonexistent-model',1,1,'` + pastText + `','` + nowText + `','` + nowText + `')`,
		`INSERT INTO account_quality_enforcements (account_id,system_account_id,enforcement_id,generation,state,action,trigger_run_id,policy_revision,profile,account_config_revision,before_status,after_status,started_at,recovery_due_at,recovery_model,created_at,updated_at)
			VALUES ('w1w-acc-iso','w1w-sys','w1w-enforcement-1',1,'active','quality_isolate','w1w-trigger-run',0,'quick',11,'active','quality_isolated','` + nowText + `','` + pastText + `','gpt-4o','` + nowText + `','` + nowText + `')`,
	}
	for _, statement := range seedStatements {
		if _, execErr := businessDB.Exec(statement); execErr != nil {
			_ = businessDB.Close()
			t.Fatalf("执行调度 fixture DML 失败: %v\nstatement=%s", execErr, statement)
		}
	}
	if err := businessDB.Close(); err != nil {
		t.Fatalf("关闭业务库失败: %v", err)
	}

	// 场景 A：不设 JUHE_AI_AUTH_CAPTCHA_DISABLED（336-338 captcha 臂）+
	// sqlite Go 运行时指标 store（569-574 采样器组件臂）。采样器只做
	// CheckSchema（schema 归 maintenance 预建），组合根要求库已就绪，
	// 因此先按 gometrics.OpenStore 的 DDL 形态补建 schema。
	metricsPath := filepath.Join(f.root, "go-metrics.sqlite")
	metricsDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(metricsPath)+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("打开 Go 运行时指标库失败: %v", err)
	}
	metricsStore, err := gometrics.NewStore(metricsDB, gometrics.DialectSQLite)
	if err != nil {
		t.Fatalf("创建 Go 运行时指标 store 失败: %v", err)
	}
	if err := metricsStore.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("补建 Go 运行时指标 schema 失败: %v", err)
	}
	if err := metricsDB.Close(); err != nil {
		t.Fatalf("关闭 Go 运行时指标库失败: %v", err)
	}
	coverageDir := w1bCoverageDir(t, "W1W-owner-tail-boot")
	env := w1wOwnerEnv(t, coverageDir, f,
		// 置空覆盖基线的 captcha 关闭项：envBool("") = false → 子进程装配
		// captcha 服务（main.go 336-338）。
		"JUHE_AI_AUTH_CAPTCHA_DISABLED=",
		"JUHE_AI_GO_RUNTIME_METRICS_STORE=sqlite",
		"JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH="+metricsPath,
		"JUHE_AI_GO_RUNTIME_METRICS_INTERVAL=1s",
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
	taskDB, taskErr := sql.Open("sqlite", "file:"+filepath.ToSlash(f.businessPath)+"?mode=ro")
	if taskErr == nil {
		rows, queryErr := taskDB.Query(`SELECT id,last_run_id,last_run_status FROM model_quality_schedules ORDER BY id`)
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
		_ = taskDB.Close()
	}
	t.Logf("场景 A/D 完成: captcha 臂 + 采样器组件 + 调度闭包（health=%s main=%s j3b=%s）", f.healthAddr, f.mainAddr, f.j3bAddr)
}

// ---------------------------------------------------------------------------
// 场景 B：J3b 装配段 fail-fast 臂（表驱动单点破坏）
// ---------------------------------------------------------------------------

func TestW1WOwnerJ3bFailFastArms(t *testing.T) {
	w1bBuildCoverBinary(t)

	scenarios := []struct {
		name       string
		prepareJ3b bool
		seedMeta   bool
		mutate     func(t *testing.T, f *w1wOwnerFixture) []string
		wantStderr string
	}{
		{
			name: "timezone-missing", prepareJ3b: true, seedMeta: true,
			mutate: func(t *testing.T, f *w1wOwnerFixture) []string {
				db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(f.businessPath)+"?mode=rw")
				if err != nil {
					t.Fatalf("打开业务库失败: %v", err)
				}
				defer db.Close()
				if _, err := db.Exec(`DELETE FROM system_settings WHERE key='usageStatsTimezone'`); err != nil {
					t.Fatalf("DELETE usageStatsTimezone 失败: %v", err)
				}
				return nil
			},
			wantStderr: "load J3b Gateway usage stats timezone",
		},
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
		{
			name: "j3b-host-schema", prepareJ3b: false, seedMeta: true,
			mutate:     func(t *testing.T, f *w1wOwnerFixture) []string { return nil },
			wantStderr: "open J3b Gateway owner host",
		},
		{
			name: "management-port-occupied", prepareJ3b: true, seedMeta: true,
			mutate: func(t *testing.T, f *w1wOwnerFixture) []string {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatalf("占用端口失败: %v", err)
				}
				t.Cleanup(func() { _ = listener.Close() })
				return []string{"JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS=" + listener.Addr().String()}
			},
			wantStderr: "listen J3b Gateway management endpoint",
		},
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
// 装配 env（系统 API 关闭，失败发生在 compose 之前）。
func w1wJ3bPostgresContractEnv(t *testing.T, coverageDir, tempAppURL, evidencePath, stateRedisAddr string) []string {
	t.Helper()
	return w1bScenarioEnv(t, coverageDir,
		"JUHE_AI_RUNTIME_MODE=performance",
		"JUHE_AI_DATABASE_DRIVER=postgres",
		"JUHE_AI_POSTGRES_URL="+tempAppURL,
		"JUHE_AI_BUSINESS_POSTGRES_URL="+tempAppURL,
		// 2026-09-19 起 system api 默认开启；本臂只验证 J3b 契约，显式关闭
		// 组合根与网关链。
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=false",
		"JUHE_AI_GATEWAY_CHAIN_ENABLED=false",
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

// ---------------------------------------------------------------------------
// 场景 B2：F3/F4 EnsureSchema 失败臂（378-380、425-427）——PG store 的
// OpenStore 惰性不连接，EnsureSchema 首次连库时对不可达 URL 确定性失败。
// ---------------------------------------------------------------------------

func TestW1WOwnerF3F4EnsureSchemaArms(t *testing.T) {
	w1bBuildCoverBinary(t)
	root := t.TempDir()
	for _, name := range []string{"audit-blobs", "audit-hot", "usage-shards", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o750); err != nil {
			t.Fatalf("创建目录 %s 失败: %v", name, err)
		}
	}
	businessSettings := filepath.Join(root, "business-settings.sqlite3")
	w1bCreateBusinessSettingsSQLite(t, businessSettings)
	args := []string{"-health-listen-address", fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))}
	rolePaths := []string{
		"JUHE_AI_DATABASE_PATH=" + filepath.Join(root, "business.sqlite"),
		"JUHE_AI_CHAT_DATABASE_PATH=" + filepath.Join(root, "chat.sqlite"),
		"JUHE_AI_DATASET_DATABASE_PATH=" + filepath.Join(root, "dataset.sqlite"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH=" + filepath.Join(root, "usage-catalog.sqlite"),
		"JUHE_AI_STATS_DATABASE_PATH=" + filepath.Join(root, "stats.sqlite"),
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH=" + filepath.Join(root, "table-monitor.sqlite"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH=" + filepath.Join(root, "runtime-log.sqlite"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT=" + filepath.Join(root, "codex-shards"),
		"JUHE_AI_USAGE_SHARD_ROOT=" + filepath.Join(root, "usage-shards"),
		"JUHE_AI_USAGE_SPOOL_DIRECTORY=" + filepath.Join(root, "usage-spool"),
		"JUHE_AI_LOG_DIR=" + filepath.Join(root, "logs"),
	}
	auditBase := []string{
		"JUHE_AI_AUDIT_LOG_STORE=postgres",
		"JUHE_AI_AUDIT_LOG_POSTGRES_URL=postgres://127.0.0.1:1/w1w_unused",
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL=postgres://127.0.0.1:1/w1w_unused",
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=" + filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=" + filepath.Join(root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1w-f3-schema",
	}

	// F3 PG EnsureSchema 连接失败臂（378-380）。
	coverageDir := w1bCoverageDir(t, "W1W-f3-schema-fail")
	env := w1bScenarioEnv(t, coverageDir, append(append([]string{}, rolePaths...), auditBase...)...)
	_, stderr, code := w1bRunScenario(t, "W1W-f3-schema-fail", env, args...)
	w1bRequireExitCode(t, "f3-schema-fail", code, 1)
	w1bRequireContains(t, "f3-schema-fail", stderr, "initialize F3 audit-log schema")

	// F4 PG EnsureSchema 连接失败臂（425-427）。
	coverageDir = w1bCoverageDir(t, "W1W-f4-schema-fail")
	env = w1bScenarioEnv(t, coverageDir, append(append([]string{}, rolePaths...),
		"JUHE_AI_AUDIT_LOG_STORE=sqlite",
		"JUHE_AI_AUDIT_LOG_DATABASE_PATH="+filepath.Join(root, "audit-ok.sqlite"),
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY="+filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY="+filepath.Join(root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH="+businessSettings,
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1w-f4-schema",
		"JUHE_AI_OPERATION_LOG_STORE=postgres",
		"JUHE_AI_OPERATION_LOG_POSTGRES_URL=postgres://127.0.0.1:1/w1w_unused",
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1w-f4-schema",
	)...)
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

// ---------------------------------------------------------------------------
// 场景 E：迁移结果 JSON 编码失败（785-787、795-797）——stdout 读端关闭
// ---------------------------------------------------------------------------

func TestW1WMigrationResultEncodeFailArms(t *testing.T) {
	exe := w1bBuildCoverBinary(t)
	root := t.TempDir()

	// F3：合法 legacy 审计源库，迁移成功但 stdout broken pipe → encode 失败。
	sourcePath := filepath.Join(root, "audit-source.sqlite")
	targetPath := filepath.Join(root, "audit-target.sqlite")
	sourceBlobs := filepath.Join(root, "audit-source-blobs")
	targetBlobs := filepath.Join(root, "audit-target-blobs")
	w1bCreateLegacyAuditSQLite(t, sourcePath, sourceBlobs)

	coverageDir := w1bCoverageDir(t, "W1W-audit-encode-fail")
	env := w1bScenarioEnv(t, coverageDir)
	pr, pw, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("创建管道失败: %v", pipeErr)
	}
	if closeErr := pr.Close(); closeErr != nil {
		t.Fatalf("关闭管道读端失败: %v", closeErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), w1bScenarioTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe,
		"-migrate-audit-log-legacy-sqlite",
		"-source-db", sourcePath,
		"-target-db", targetPath,
		"-source-blob-dir", sourceBlobs,
		"-target-blob-dir", targetBlobs,
		"-node-stopped", "-go-stopped")
	cmd.Env = env
	cmd.Stdout = pw
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	_ = pw.Close()
	if ctx.Err() != nil {
		t.Fatalf("F3 encode 失败场景超过 %s 有界等待", w1bScenarioTimeout)
	}
	exitCode := 0
	if runErr != nil {
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok {
			t.Fatalf("F3 encode 场景启动失败: %v", runErr)
		}
		exitCode = exitErr.ExitCode()
	}
	w1bRequireExitCode(t, "audit-encode-fail", exitCode, 1)
	w1bRequireContains(t, "audit-encode-fail", stderr.String(), "encode F3 audit migration result")
	w1bAppendCoverageManifest(t, coverageDir)

	// F4：合法 legacy operation 源库，同样以 broken stdout 驱动 encode 失败。
	opSource := filepath.Join(root, "node-dataset.sqlite3")
	opTarget := filepath.Join(root, "f4-operation.sqlite3")
	usageShardRoot := filepath.Join(root, "usage-shards")
	w1bCreateLegacyOperationLogSQLite(t, opSource)
	w1bCreateBusinessSettingsSQLite(t, filepath.Join(root, "business-settings.sqlite3"))
	coverageDir = w1bCoverageDir(t, "W1W-oplog-encode-fail")
	env = w1bScenarioEnv(t, coverageDir,
		"JUHE_AI_OPERATION_LOG_STORE=sqlite",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH="+opTarget,
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH="+filepath.Join(root, "business-settings.sqlite3"),
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1w-f4-encode",
		"JUHE_AI_USAGE_SHARD_ROOT="+usageShardRoot)
	pr2, pw2, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("创建 F4 管道失败: %v", pipeErr)
	}
	if closeErr := pr2.Close(); closeErr != nil {
		t.Fatalf("关闭 F4 管道读端失败: %v", closeErr)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), w1bScenarioTimeout)
	defer cancel2()
	cmd2 := exec.CommandContext(ctx2, exe,
		"-migrate-operation-log-legacy-sqlite",
		"-operation-log-source-db", opSource,
		"-node-stopped", "-go-stopped", "-backup-confirmed")
	cmd2.Env = env
	cmd2.Stdout = pw2
	var stderr2 bytes.Buffer
	cmd2.Stderr = &stderr2
	runErr2 := cmd2.Run()
	_ = pw2.Close()
	if ctx2.Err() != nil {
		t.Fatalf("F4 encode 失败场景超过 %s 有界等待", w1bScenarioTimeout)
	}
	exitCode2 := 0
	if runErr2 != nil {
		exitErr, ok := runErr2.(*exec.ExitError)
		if !ok {
			t.Fatalf("F4 encode 场景启动失败: %v", runErr2)
		}
		exitCode2 = exitErr.ExitCode()
	}
	w1bRequireExitCode(t, "oplog-encode-fail", exitCode2, 1)
	w1bRequireContains(t, "oplog-encode-fail", stderr2.String(), "encode F4 operation-log migration result")
	w1bAppendCoverageManifest(t, coverageDir)
	t.Logf("场景 E 完成: F3/F4 迁移结果编码失败臂覆盖")
}

// ---------------------------------------------------------------------------
// 场景 B4：F4 PG 租约被占（445-447）——真实 dev PG 临时子库，不可达即 Skip
// ---------------------------------------------------------------------------

func TestW1WOwnerF4LeaseHeldPostgres(t *testing.T) {
	tempAppURL := w1g2CoverPostgres(t)
	// 先确保 F4 PG schema 就绪，再预插一条未过期的他者租约。
	fixtureStore, err := operationlog.OpenStore(operationlog.Config{
		Enabled:              true,
		Mode:                 operationlog.ModePostgres,
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
	if _, err := db.Exec(`INSERT INTO juhe_dataset.operation_log_owner_leases (lease_key,owner_id,fence_token,lease_until,updated_at)
		VALUES ('f4-operation-log-persistence','w1w-other-owner',1, clock_timestamp() + INTERVAL '1 hour', clock_timestamp())
		ON CONFLICT (lease_key) DO UPDATE SET owner_id='w1w-other-owner', lease_until = clock_timestamp() + INTERVAL '1 hour', updated_at = clock_timestamp()`); err != nil {
		t.Fatalf("预插 F4 他者租约失败: %v", err)
	}

	w1bBuildCoverBinary(t)
	root := t.TempDir()
	for _, name := range []string{"audit-blobs", "audit-hot", "logs"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o750); err != nil {
			t.Fatalf("创建目录 %s 失败: %v", name, err)
		}
	}
	businessPath := filepath.Join(root, "business.sqlite")
	w1v2PrepareBusinessSQLite(t, businessPath)
	if err := os.MkdirAll(filepath.Join(root, "evidence"), 0o750); err != nil {
		t.Fatalf("创建 evidence 目录失败: %v", err)
	}
	businessEvidence := w1bWriteCutoverEvidence(t, filepath.Join(root, "evidence"), w1wOwnerEpoch)
	healthPort := w1bFreePort(t)
	coverageDir := w1bCoverageDir(t, "W1W-f4-lease-held")
	env := w1bScenarioEnv(t, coverageDir,
		"JUHE_AI_RUNTIME_MODE=standalone",
		"JUHE_AI_DATABASE_DRIVER=postgres",
		"JUHE_AI_POSTGRES_URL="+tempAppURL,
		"JUHE_AI_BUSINESS_POSTGRES_URL="+tempAppURL,
		"JUHE_AI_SECRET=w1w-f4-lease-secret",
		"JUHE_AI_DATABASE_PATH="+filepath.Join(root, "business.sqlite"),
		"JUHE_AI_CHAT_DATABASE_PATH="+filepath.Join(root, "chat.sqlite"),
		"JUHE_AI_DATASET_DATABASE_PATH="+filepath.Join(root, "dataset.sqlite"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH="+filepath.Join(root, "usage-catalog.sqlite"),
		"JUHE_AI_STATS_DATABASE_PATH="+filepath.Join(root, "stats.sqlite"),
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH="+filepath.Join(root, "table-monitor.sqlite"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH="+filepath.Join(root, "runtime-log.sqlite"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT="+filepath.Join(root, "codex-shards"),
		"JUHE_AI_USAGE_SHARD_ROOT="+filepath.Join(root, "usage-shards"),
		"JUHE_AI_USAGE_SPOOL_DIRECTORY="+filepath.Join(root, "usage-spool"),
		"JUHE_AI_LOG_DIR="+filepath.Join(root, "logs"),
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true",
		"JUHE_AI_BUSINESS_OWNER=gateway",
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
		"JUHE_AI_BUSINESS_SCHEMA_READY=true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH="+w1wOwnerEpoch,
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH="+businessEvidence,
		"JUHE_AI_AUDIT_LOG_STORE=sqlite",
		"JUHE_AI_AUDIT_LOG_DATABASE_PATH="+filepath.Join(root, "audit.sqlite"),
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY="+filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY="+filepath.Join(root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH="+businessPath,
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1w-f4-lease",
		"JUHE_AI_OPERATION_LOG_STORE=postgres",
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
	composed := &composition{
		db:        w1uOpenSeededBusinessDB(t),
		statsDB:   w1uOpenPlainSQLite(t, "w1w-sup-stats.sqlite3"),
		pgDialect: false,
		Bus:       inval.New(time.Now),
	}
	cfg := runtimeConfig{
		RuntimeMode:                   "standalone",
		DatabaseDriver:                "sqlite",
		CacheDriver:                   "memory",
		RuntimeStateDriver:            "memory",
		Secret:                        "w1w-chain-runtime-secret",
		DispatchAccountCandidateLimit: 5000,
		ConcurrencyGlobalMax:          5000,
	}
	services, err := composeChainRuntimeServices(composed, cfg, w1tSettingValue)
	if err != nil {
		t.Fatalf("内存驱动组合失败: %v", err)
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
		db:        w1uOpenSeededBusinessDB(t),
		statsDB:   w1uOpenPlainSQLite(t, "w1w-warn-stats.sqlite3"),
		pgDialect: false,
		Bus:       inval.New(time.Now),
	}
	stateRedis := miniredis.RunT(t)
	cfg := runtimeConfig{
		RuntimeMode:                   "performance",
		DatabaseDriver:                "sqlite",
		CacheDriver:                   "memory",
		RuntimeStateDriver:            "redis",
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

// TestW1WChainRuntimeEmptySecretArm 覆盖 accountkeystates 构造失败臂
// （chain_runtime.go 606-608）：空 Secret 走完内存驱动全部装配后在
// accountkeystates.NewStore 处 fail-fast。
func TestW1WChainRuntimeEmptySecretArm(t *testing.T) {
	composed := &composition{
		db:        w1uOpenSeededBusinessDB(t),
		statsDB:   w1uOpenPlainSQLite(t, "w1w-empty-secret-stats.sqlite3"),
		pgDialect: false,
		Bus:       inval.New(time.Now),
	}
	cfg := runtimeConfig{
		RuntimeMode:                   "standalone",
		DatabaseDriver:                "sqlite",
		CacheDriver:                   "memory",
		RuntimeStateDriver:            "memory",
		Secret:                        "   ",
		DispatchAccountCandidateLimit: 5000,
		ConcurrencyGlobalMax:          5000,
	}
	services, err := composeChainRuntimeServices(composed, cfg, w1tSettingValue)
	if err == nil {
		services.Close()
		t.Fatalf("空 Secret 的组合必须 fail-fast")
	}
	if !strings.Contains(err.Error(), "create account api-key effects key states store") {
		t.Fatalf("期望 accountkeystates 构造失败，实际: %v", err)
	}
	t.Logf("chain_runtime 空 Secret 失败臂（606-608）已驱动")
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
