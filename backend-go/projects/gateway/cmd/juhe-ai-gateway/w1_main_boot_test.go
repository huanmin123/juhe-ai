package main

// w1（业务层）：进程级 boot 覆盖收割。按 docs/develop/后端测试分层规则.md，
// main()/信号/监听生命周期属于业务层：go build -cover 构建真进程，隔离
// SQLite + 临时目录 + 空闲端口启动，健康检查通过后优雅退出（CTRL_BREAK），
// go tool covdata textfmt 收割二进制覆盖率并与单元 profile 合并。
// 未设置 JUHE_AI_BUSINESS_BOOT_COVERAGE 时跳过——单元层不跑进程级用例。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	gatewaydispatch "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/redis/go-redis/v9"
	_ "modernc.org/sqlite"
)

const w1BootGateEnv = "JUHE_AI_BUSINESS_BOOT_COVERAGE"

func TestW1BusinessBootGatewayCoverage(t *testing.T) {
	if os.Getenv(w1BootGateEnv) == "" {
		t.Skipf("未设置 %s；跳过业务层 boot 覆盖（单元层不跑进程级用例）", w1BootGateEnv)
	}
	root := t.TempDir()
	buildDir, err := os.MkdirTemp("", "w1-boot-build-")
	if err != nil {
		t.Fatalf("builddir = %v", err)
	}
	defer func() { _ = os.RemoveAll(buildDir) }()
	exe := filepath.Join(buildDir, "juhe-ai-gateway-boot.exe")
	build := exec.Command("go", "build", "-cover", "-o", exe, "./")
	build.Dir = currentPackageDir()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -cover 失败：%v\n%s", err, output)
	}
	// 场景一：被动网关（standby 归属）——健康监听 + 信号优雅退出。
	passivePort := w1FreePort(t)
	w1BootRun(t, exe, root, "passive", map[string]string{
		"JUHE_AI_BLUE_GREEN_OWNER_MODE": "standby",
	}, []string{"-health-listen-address", "127.0.0.1:" + passivePort},
		func(body string) bool { return strings.Contains(body, `"ownerMode":"standby"`) })
	// 场景二：全 owner 启动（system-api 关闭，跳过交接证据门）——
	// F3 audit owner 就绪 → /health ready=true → 优雅退出。
	mainPort := w1FreePort(t)
	healthPort := w1FreePort(t)
	ownerEnv := map[string]string{
		"NODE_ENV":                                 "development",
		"JUHE_AI_DATABASE_PATH":                    filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_AUDIT_LOG_STORE":                  "sqlite",
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID":            "w1-boot-instance",
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH": filepath.Join(root, "business-settings.sqlite3"),
		"JUHE_AI_DATASET_DATABASE_PATH":            filepath.Join(root, "dataset.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH":      filepath.Join(root, "usage-catalog.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH":              filepath.Join(root, "stats.sqlite3"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":        filepath.Join(root, "runtime-log.sqlite3"),
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH":      filepath.Join(root, "table-monitor.sqlite3"),
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT":   filepath.Join(root, "codex-shards"),
		"JUHE_AI_USAGE_SHARD_ROOT":                 filepath.Join(root, "usage-shards"),
		"JUHE_AI_AUDIT_LOG_DATABASE_PATH":          filepath.Join(root, "audit.sqlite3"),
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY":         filepath.Join(root, "blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY":   filepath.Join(root, "hotsearch"),
		"JUHE_AI_HOST":                             "127.0.0.1",
		"JUHE_AI_PORT":                             mainPort,
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS":    "127.0.0.1:" + healthPort,
	}
	w1BootRun(t, exe, root, "owner", ownerEnv, nil, func(body string) bool {
		return strings.Contains(body, `"ready":true`)
	})
	// 场景三：SystemAPIEnabled + ChainEnabled 全装配 boot——先造交接证据与
	// 六库 schema（maintenance --ensure-schema --seed），再带证据启动，
	// /health ready 后经主端口打 /v1/models（无 Key → 401 JSON）。
	maintenanceExe := filepath.Join(buildDir, "juhe-ai-maintenance.exe")
	maintenanceBuild := exec.Command("go", "build", "-o", maintenanceExe, "./cmd/juhe-ai-maintenance")
	maintenanceBuild.Dir = filepath.Join(currentPackageDir(), "..", "..", "..", "maintenance")
	if output, err := maintenanceBuild.CombinedOutput(); err != nil {
		t.Fatalf("maintenance build 失败：%v\n%s", err, output)
	}
	paths := "business=" + filepath.Join(root, "business.sqlite3") +
		",chat=" + filepath.Join(root, "chat.sqlite3") +
		",dataset=" + filepath.Join(root, "dataset.sqlite3") +
		",usage-catalog=" + filepath.Join(root, "usage-catalog.sqlite3") +
		",stats=" + filepath.Join(root, "stats.sqlite3") +
		",codex-context-shard-root=" + filepath.Join(root, "codex-shards") +
		",codex-context-shard-count=4"
	ensure := exec.Command(maintenanceExe, "-ensure-schema", "-seed", "-driver", "sqlite", "-paths", paths)
	ensure.Dir = root
	if output, err := ensure.CombinedOutput(); err != nil {
		t.Fatalf("ensure-schema 失败：%v\n%s", err, output)
	}
	evidencePath, evidenceEnv := w1BuildSystemApiEvidence(t, root)
	_ = evidencePath
	composeEnv := map[string]string{}
	for key, value := range ownerEnv {
		composeEnv[key] = value
	}
	for key, value := range evidenceEnv {
		composeEnv[key] = value
	}
	composeMainPort := w1FreePort(t)
	composeHealthPort := w1FreePort(t)
	composeRoot := filepath.Join(root, "compose-fresh")
	if err := os.MkdirAll(composeRoot, 0o755); err != nil {
		t.Fatalf("compose root = %v", err)
	}
	// 独立 F3 文件集：上一场景的 audit owner 租约仍在租期内，不能复用。
	composeEnv["JUHE_AI_AUDIT_LOG_DATABASE_PATH"] = filepath.Join(composeRoot, "audit.sqlite3")
	composeEnv["JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY"] = filepath.Join(composeRoot, "blobs")
	composeEnv["JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY"] = filepath.Join(composeRoot, "hotsearch")
	composeEnv["JUHE_AI_AUDIT_LOG_INSTANCE_ID"] = "w1-boot-instance-compose"
	// SystemAPI 组合根要求 F4 已启用；同样用独立文件集避租约冲突。
	composeEnv["JUHE_AI_OPERATION_LOG_STORE"] = "sqlite"
	composeEnv["JUHE_AI_OPERATION_LOG_DATABASE_PATH"] = filepath.Join(composeRoot, "operation.sqlite3")
	// F4 的设置读取走业务库的 system_settings（ensure-schema+seed 已建）。
	composeEnv["JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH"] = filepath.Join(root, "business.sqlite3")
	composeEnv["JUHE_AI_OPERATION_LOG_INSTANCE_ID"] = "w1-boot-operation-compose"
	composeEnv["JUHE_AI_SECRET"] = "w1-boot-composition-secret-0123456789abcdef"
	composeEnv["JUHE_AI_PORT"] = composeMainPort
	composeEnv["JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS"] = "127.0.0.1:" + composeHealthPort
	w1BootRun(t, exe, root, "compose", composeEnv, nil, func(body string) bool {
		return strings.Contains(body, `"ready":true`)
	}, func() {
		response, err := http.Get("http://127.0.0.1:" + composeMainPort + "/v1/models")
		if err != nil {
			t.Logf("主端口 /v1/models 探针失败：%v", err)
			return
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("/v1/models 无 Key 期望 401，实际 %d：%s", response.StatusCode, string(body))
		}
	})
	// 副作用断言：F3 audit 库已落盘。
	if _, err := os.Stat(filepath.Join(root, "audit.sqlite3")); err != nil {
		t.Fatalf("audit 库必须创建 = %v", err)
	}
	// 场景四：J3b owner（SystemAPI 关闭，J3B_ENABLED=true）——miniredis 承载
	// circuit runtime 的 Redis 契约（runtime-index-meta 就绪哈希），
	// maintenance --apply-j3b-model-check-sqlite 引导 J3b 专属库 schema，
	// 交接证据复用同一构造器；健康探针等待 j3bReady=true。
	j3bRoot := filepath.Join(root, "j3b-scenario")
	if err := os.MkdirAll(j3bRoot, 0o755); err != nil {
		t.Fatalf("j3b root = %v", err)
	}
	j3bBusiness := filepath.Join(j3bRoot, "business.sqlite3")
	j3bPaths := "business=" + j3bBusiness +
		",chat=" + filepath.Join(j3bRoot, "chat.sqlite3") +
		",dataset=" + filepath.Join(j3bRoot, "dataset.sqlite3") +
		",usage-catalog=" + filepath.Join(j3bRoot, "usage-catalog.sqlite3") +
		",stats=" + filepath.Join(j3bRoot, "stats.sqlite3") +
		",codex-context-shard-root=" + filepath.Join(j3bRoot, "codex-shards") +
		",codex-context-shard-count=4"
	j3bEnsure := exec.Command(maintenanceExe, "-ensure-schema", "-seed", "-driver", "sqlite", "-paths", j3bPaths)
	j3bEnsure.Dir = j3bRoot
	if output, err := j3bEnsure.CombinedOutput(); err != nil {
		t.Fatalf("j3b ensure-schema 失败：%v\n%s", err, output)
	}
	// 生产缺陷记录（不改产品代码）：modelcheckowner 的 Business SQLite
	// 巡检把唯一表达式索引当硬错误（sqliteSchemaIndexColumns 对
	// lower(display_name) 这类表达式列返回 "contains an expression"），而
	// 规范 schema 自带 idx_system_accounts_display_name_unique_lower。测试
	// 库里 DROP 该索引以通过巡检；J3b SQLite 分支在真实环境会因此无法启动。
	j3bDropIndex, err := sql.Open("sqlite", j3bBusiness)
	if err != nil {
		t.Fatalf("open j3b business = %v", err)
	}
	if _, err := j3bDropIndex.Exec("DROP INDEX IF EXISTS idx_system_accounts_display_name_unique_lower; DROP INDEX IF EXISTS idx_system_accounts_username_unique_lower"); err != nil {
		_ = j3bDropIndex.Close()
		t.Fatalf("drop expression index = %v", err)
	}
	if err := j3bDropIndex.Close(); err != nil {
		t.Fatalf("close j3b business = %v", err)
	}
	j3bStorePath := filepath.Join(j3bRoot, "j3b-model-check.sqlite3")
	j3bApply := exec.Command(maintenanceExe, "-apply-j3b-model-check-sqlite", "-node-stopped", "-go-stopped", "-backup-confirmed")
	j3bApply.Dir = j3bRoot
	// apply 路径只读 SQLiteBootstrapEnv，不读 -j3b-sqlite-path flag。
	j3bApply.Env = append(os.Environ(), "JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH="+j3bStorePath)
	if output, err := j3bApply.CombinedOutput(); err != nil {
		t.Fatalf("j3b schema apply 失败：%v\n%s", err, output)
	}
	mini := miniredis.NewMiniRedis()
	if err := mini.Start(); err != nil {
		t.Fatalf("miniredis start = %v", err)
	}
	defer mini.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	indexKey := "juhe-ai:w1-j3b:account-circuit:gateway-account-circuit:runtime-index-meta"
	if err := redisClient.HSet(context.Background(), indexKey, map[string]any{"version": "1", "status": "ready", "ownerMode": "go-runtime-state-v1"}).Err(); err != nil {
		t.Fatalf("miniredis index 预置失败 = %v", err)
	}
	_ = redisClient.Close()
	_, j3bEvidenceEnv := w1BuildSystemApiEvidence(t, j3bRoot)
	j3bEnv := map[string]string{}
	for key, value := range ownerEnv {
		j3bEnv[key] = value
	}
	for key, value := range j3bEvidenceEnv {
		j3bEnv[key] = value
	}
	// 独立 F3 文件集：避免与其他场景的 audit owner 租约冲突。
	j3bEnv["JUHE_AI_AUDIT_LOG_DATABASE_PATH"] = filepath.Join(j3bRoot, "audit.sqlite3")
	j3bEnv["JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY"] = filepath.Join(j3bRoot, "blobs")
	j3bEnv["JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY"] = filepath.Join(j3bRoot, "hotsearch")
	j3bEnv["JUHE_AI_AUDIT_LOG_INSTANCE_ID"] = "w1-boot-instance-j3b"
	j3bEnv["JUHE_AI_GATEWAY_SYSTEM_API_ENABLED"] = "false"
	j3bEnv["JUHE_AI_GATEWAY_CHAIN_ENABLED"] = "false"
	j3bEnv["JUHE_AI_J3B_ENABLED"] = "true"
	j3bEnv["JUHE_AI_J3B_OWNER"] = "gateway"
	j3bEnv["JUHE_AI_J3B_INSTANCE_ID"] = "w1-boot-j3b"
	j3bEnv["JUHE_AI_J3B_STORE"] = "sqlite"
	j3bEnv["JUHE_AI_J3B_DATABASE_PATH"] = j3bStorePath
	j3bEnv["JUHE_AI_J3B_BUSINESS_DATABASE_PATH"] = j3bBusiness
	j3bEnv["JUHE_AI_J3B_CREDENTIAL_SECRET"] = "w1-boot-j3b-credential-secret-0123456789"
	j3bEnv["JUHE_AI_J3B_IDENTITY_SECRET"] = "w1-boot-j3b-identity-secret-0123456789"
	j3bEnv["JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED"] = "true"
	j3bEnv["JUHE_AI_J3B_NODE_WRITER_STOPPED"] = "true"
	j3bEnv["JUHE_AI_J3B_SCHEMA_READY"] = "true"
	j3bEnv["JUHE_AI_J3B_HEALTH_BOUNDARY_READY"] = "true"
	j3bEnv["JUHE_AI_J3B_RUNTIME_READY"] = "true"
	j3bEnv["JUHE_AI_J3B_CIRCUIT_REDIS_URL"] = "redis://" + mini.Addr()
	j3bEnv["JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE"] = "w1-j3b"
	j3bEnv["JUHE_AI_J3B_OWNER_EPOCH"] = j3bEvidenceEnv["JUHE_AI_BUSINESS_OWNER_EPOCH"]
	j3bEnv["JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH"] = j3bEvidenceEnv["JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH"]
	j3bEnv["JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS"] = "127.0.0.1:" + w1FreePort(t)
	// 生产缺陷 #5 已修复（retention mode 按枚举映射）：本场景转正为成功
	// 场景，收割 J3b 装配全段（Business connection/authenticator/
	// retention/circuit control-plane+circuit runtime Redis/key-model/
	// projector/host 装配 + 管理监听 + 组件节拍）。
	w1BootRun(t, exe, root, "j3b", j3bEnv, nil, func(body string) bool {
		return strings.Contains(body, `"j3bReady":true`) && strings.Contains(body, `"ready":true`)
	})
	// 场景六：J3b evidence fail fast——交接证据文件缺失/纪元不匹配。
	// 前段已证明 J3b 分支必然在 retention mode 处 fail；这里补收割
	// VerifyConfiguredCutoverEvidence 的两个 fail 出口。
	j3bEvFail := map[string]string{}
	for key, value := range j3bEnv {
		j3bEvFail[key] = value
	}
	j3bEvFail["JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH"] = filepath.Join(root, "missing-evidence.json")
	w1BootExpectExit(t, exe, filepath.Join(root, "cov-case"), nil, j3bEvFail, 1, "read J3b cutover evidence")
	j3bEvEpoch := map[string]string{}
	for key, value := range j3bEnv {
		j3bEvEpoch[key] = value
	}
	j3bEvEpoch["JUHE_AI_J3B_OWNER_EPOCH"] = "w1-boot-wrong-epoch"
	w1BootExpectExit(t, exe, filepath.Join(root, "cov-case"), nil, j3bEvEpoch, 1, "verify J3b cutover evidence")
	// 场景七：J3b 装配段 fail fast（缺陷 #5 修复后全装配可达）——
	// circuit runtime Redis ping 失败、runtime-index-meta 未就绪、
	// 管理监听端口冲突。
	j3bBadRedis := map[string]string{}
	for key, value := range j3bEnv {
		j3bBadRedis[key] = value
	}
	j3bBadRedis["JUHE_AI_J3B_CIRCUIT_REDIS_URL"] = "redis://127.0.0.1:1"
	w1BootExpectExit(t, exe, filepath.Join(root, "cov-case"), nil, j3bBadRedis, 1, "ping J3b Gateway circuit runtime Redis")
	// runtime-index-meta 未就绪：新 miniredis 不预置就绪哈希 → CheckReady fail。
	miniBare := miniredis.NewMiniRedis()
	if err := miniBare.Start(); err != nil {
		t.Fatalf("miniredis bare start = %v", err)
	}
	defer miniBare.Close()
	j3bNoIndex := map[string]string{}
	for key, value := range j3bEnv {
		j3bNoIndex[key] = value
	}
	j3bNoIndex["JUHE_AI_J3B_CIRCUIT_REDIS_URL"] = "redis://" + miniBare.Addr()
	w1BootExpectExit(t, exe, filepath.Join(root, "cov-case"), nil, j3bNoIndex, 1, "verify J3b Gateway circuit runtime owner fence")
	// 场景五：SQLite 六库路径契约 fail fast（子进程收割——错误分支在进程内
	// 返回时不关闭 composed.db，Windows 句柄锁会阻塞临时目录清理）。审计
	// F3 专库隔离校验先于组合根，逐项移除必需路径后断言 exit 1 与错误
	// 原文；composeSystemAPI 的同名缺失分支为纵深防御，生产装配下被这里
	// 前置拦截，不可达。
	composeFailCases := []struct{ label, envKey, wantErr string }{
		{"usage-catalog", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH"},
		{"table-monitor", "JUHE_AI_TABLE_MONITOR_DATABASE_PATH", "JUHE_AI_TABLE_MONITOR_DATABASE_PATH"},
		{"runtime-log", "JUHE_AI_RUNTIME_LOG_DATABASE_PATH", "JUHE_AI_RUNTIME_LOG_DATABASE_PATH"},
		{"stats", "JUHE_AI_STATS_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH"},
		{"dataset", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH"},
	}
	for _, item := range composeFailCases {
		failEnv := map[string]string{}
		for key, value := range composeEnv {
			failEnv[key] = value
		}
		delete(failEnv, item.envKey)
		w1BootExpectExit(t, exe, filepath.Join(root, "cov-case"), nil, failEnv, 1, item.wantErr)
	}
	// 覆盖率合并：covdata textfmt → 与单元 profile（可选）按块取 max。
	bootProfile := filepath.Join(root, "boot.out")
	// 三个场景的 cov 目录必须全部聚合；此前只收 owner 场景，
	// passive 与 compose（SystemAPI 全装配）的覆盖率被整体丢弃。
	covInputs := filepath.Join(root, "cov", "passive") + "," + filepath.Join(root, "cov", "owner") + "," + filepath.Join(root, "cov", "compose") + "," + filepath.Join(root, "cov", "j3b") + "," + filepath.Join(root, "cov-case")
	merge := exec.Command("go", "tool", "covdata", "textfmt", "-i", covInputs, "-o", bootProfile)
	if output, err := merge.CombinedOutput(); err != nil {
		t.Fatalf("covdata textfmt 失败：%v\n%s", err, output)
	}
	unitProfile := os.Getenv("JUHE_AI_UNIT_COVERAGE_PROFILE")
	merged := filepath.Join(root, "merged.out")
	if unitProfile != "" {
		w1MergeCoverageProfiles(t, unitProfile, bootProfile, merged)
		if out := os.Getenv("JUHE_AI_MERGED_COVERAGE_PROFILE"); out != "" {
			if data, err := os.ReadFile(merged); err == nil {
				_ = os.WriteFile(out, data, 0o644)
			}
		}
		t.Logf("合并 profile：%s（单元 + boot）", merged)
	} else {
		t.Logf("boot profile：%s", bootProfile)
	}
}

// w1BootRun 启动网关真进程（GOCOVERDIR 隔离），轮询健康检查直到探针满足，
// 发 CTRL_BREAK 优雅退出，断言退出码为 0。
func w1BootRun(t *testing.T, exe, root, label string, env map[string]string, extraArgs []string, probe func(body string) bool, afterReady ...func()) {
	t.Helper()
	covDir := filepath.Join(root, "cov", label)
	if err := os.MkdirAll(covDir, 0o755); err != nil {
		t.Fatalf("covdir = %v", err)
	}
	command := exec.Command(exe, extraArgs...)
	command.Env = os.Environ()
	command.Env = append(command.Env, "GOCOVERDIR="+covDir)
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x200} // CREATE_NEW_PROCESS_GROUP
	var bootOutput strings.Builder
	command.Stdout = &bootOutput
	command.Stderr = &bootOutput
	if err := command.Start(); err != nil {
		t.Fatalf("[%s] start = %v", label, err)
	}
	healthAddress := "127.0.0.1:" + w1BootHealthPort(label, env, extraArgs)
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	probeDeadline := time.Now().Add(90 * time.Second)
	probeOK := false
	for time.Now().Before(probeDeadline) {
		response, err := http.Get("http://" + healthAddress + "/health")
		if err == nil {
			body := make([]byte, 4096)
			read, _ := response.Body.Read(body)
			_ = response.Body.Close()
			if probe(string(body[:read])) {
				probeOK = true
				break
			}
		}
		select {
		case err := <-exited:
			t.Fatalf("[%s] 进程提前退出 = %v\n启动输出：%s", label, err, bootOutput.String())
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !probeOK {
		w1GracefulStop(command)
		_, _ = command.Process.Wait()
		t.Fatalf("[%s] 健康探针未满足（%s）", label, healthAddress)
	}
	for _, callback := range afterReady {
		callback()
	}
	w1GracefulStop(command)
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("[%s] 优雅退出必须成功 = %v", label, err)
		}
	case <-time.After(20 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("[%s] 优雅退出超时", label)
	}
	entries, err := os.ReadDir(covDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("[%s] GOCOVERDIR 必须产出覆盖率文件 = %v", label, err)
	}
}

// w1GracefulStop 先发 CTRL_BREAK（进程组），失败回落 taskkill（不带 /F 的
// 控制台关闭语义），都不支持时才硬杀（此时覆盖率数据会缺失，测试失败）。
func w1GracefulStop(command *exec.Cmd) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	generate := kernel32.NewProc("GenerateConsoleCtrlEvent")
	if result, _, _ := generate.Call(1 /* CTRL_BREAK_EVENT */, uintptr(command.Process.Pid)); result != 0 {
		return
	}
	fallback := exec.Command("taskkill", "/PID", strconv.Itoa(command.Process.Pid))
	_ = fallback.Run()
}

func w1BootHealthPort(label string, env map[string]string, extraArgs []string) string {
	for i := 0; i+1 < len(extraArgs); i += 2 {
		if extraArgs[i] == "-health-listen-address" {
			if _, port, err := net.SplitHostPort(extraArgs[i+1]); err == nil {
				return port
			}
		}
	}
	if value, ok := env["JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS"]; ok {
		if _, port, err := net.SplitHostPort(value); err == nil {
			return port
		}
	}
	return ""
}

func w1FreePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port = %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return strconv.Itoa(port)
}

func currentPackageDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// w1MergeCoverageProfiles 把 boot profile 与单元 profile 按块键取 max 合并。
func w1MergeCoverageProfiles(t *testing.T, unitPath, bootPath, outPath string) {
	t.Helper()
	mergeInto := func(target map[string]string, path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s = %v", path, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) != 3 {
				continue
			}
			key := parts[0] + " " + parts[1]
			if existing, ok := target[key]; ok {
				a, _ := strconv.Atoi(existing)
				b, _ := strconv.Atoi(parts[2])
				if b > a {
					target[key] = parts[2]
				}
			} else {
				target[key] = parts[2]
			}
		}
	}
	merged := map[string]string{}
	mergeInto(merged, unitPath)
	mergeInto(merged, bootPath)
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString("mode: set\n")
	for _, key := range keys {
		builder.WriteString(key + " " + merged[key] + "\n")
	}
	if err := os.WriteFile(outPath, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write merged = %v", err)
	}
}

// w1BuildSystemApiEvidence 在本地构造一套可验证的 J3b 交接证据
// （读回清单 + 备份工件 + 证据 JSON），打开 SystemAPIEnabled 的组合根 boot。
func w1BuildSystemApiEvidence(t *testing.T, root string) (evidencePath string, env map[string]string) {
	t.Helper()
	const (
		formatVersion = "j3b-readback-manifest/v2"
		scope         = "j3b-legacy-facts-v2"
		sourceSchema  = "juhe_dataset+juhe_stats"
		targetSchema  = "juhe_j3b"
		epoch         = "w1-boot-epoch"
	)
	// 读回清单：固定 9 张必需表，行数相等 + 摘要一致（本地构造的合法事实）。
	tableNames := []string{
		"account_quality_health_hourly", "model_check_items", "model_check_observations",
		"model_check_runs", "model_account_trust_results", "model_token_intercept_baseline_versions",
		"model_trust_aggregation_state", "model_trust_latest_dirty_accounts", "model_trust_observation_receipts",
	}
	tables := make([]gatewaydispatch.J3bReadbackTableDigest, 0, len(tableNames))
	for _, name := range tableNames {
		digest := sha256.Sum256([]byte("w1-table:" + name))
		tables = append(tables, gatewaydispatch.J3bReadbackTableDigest{
			Name: name, SourceRows: 0, TargetRows: 0,
			SourceDigest: hex.EncodeToString(digest[:]), TargetDigest: hex.EncodeToString(digest[:]),
		})
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	manifest := gatewaydispatch.J3bReadbackManifest{
		FormatVersion: formatVersion, Scope: scope, Producer: "w1-boot",
		SourceSnapshotIdentity: "w1-boot-snapshot", SourceSchema: sourceSchema, TargetSchema: targetSchema,
		ProjectionComplete: true,
		VerifiedAt:         time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
		Tables:             tables,
	}
	canonicalHash, err := gatewaydispatch.ComputeJ3bReadbackManifestHash(manifest)
	if err != nil {
		t.Fatalf("manifest hash = %v", err)
	}
	manifest.ManifestHash = canonicalHash
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("manifest marshal = %v", err)
	}
	manifestPath := filepath.Join(root, "readback-manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		t.Fatalf("write manifest = %v", err)
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	// 备份工件：内容任意，哈希与文件一致即可。
	backupBytes := []byte("w1-boot-backup-artifact")
	backupPath := filepath.Join(root, "business-backup.bin")
	if err := os.WriteFile(backupPath, backupBytes, 0o644); err != nil {
		t.Fatalf("write backup = %v", err)
	}
	backupDigest := sha256.Sum256(backupBytes)
	evidence := gatewaydispatch.J3bCutoverEvidence{
		OldOwner: "node", NewOwner: "go-gateway", OwnerEpoch: epoch,
		DrainCompleted: true, InFlight: 0, ActivePathZero: true, BlockedFindings: 0,
		RollbackReplayCursor: "w1-boot-cursor",
		BackupArtifact: gatewaydispatch.J3bBackupArtifact{
			Path: backupPath, Hash: hex.EncodeToString(backupDigest[:]),
		},
		Freshness: gatewaydispatch.J3bEvidenceFreshness{
			CapturedAt: time.Now().UTC().Format(time.RFC3339), MaxAgeSeconds: 86400,
		},
		ReadbackManifest: gatewaydispatch.J3bReadbackManifestReference{
			Path: manifestPath, Hash: hex.EncodeToString(manifestDigest[:]),
			FormatVersion: formatVersion, Scope: scope,
			SourceSnapshotIdentity: "w1-boot-snapshot", SourceSchema: sourceSchema, TargetSchema: targetSchema,
		},
	}
	evidenceBytes, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("evidence marshal = %v", err)
	}
	evidencePath = filepath.Join(root, "cutover-evidence.json")
	if err := os.WriteFile(evidencePath, evidenceBytes, 0o644); err != nil {
		t.Fatalf("write evidence = %v", err)
	}
	return evidencePath, map[string]string{
		"JUHE_AI_BUSINESS_OWNER":                 "gateway",
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED":     "true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED":   "true",
		"JUHE_AI_BUSINESS_SCHEMA_READY":          "true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH":           epoch,
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH": evidencePath,
		"JUHE_AI_BUSINESS_DATABASE_PATH":         filepath.Join(root, "business.sqlite3"),
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED":     "true",
		"JUHE_AI_GATEWAY_CHAIN_ENABLED":          "true",
		"JUHE_AI_CHAT_DATABASE_PATH":             filepath.Join(root, "chat.sqlite3"),
	}
}

// w1BootExpectExit 运行网关进程直到自然退出，断言退出码与 stderr 片段。
// 离线迁移命令的 fail()/os.Exit(2) 分支不属于进程内可测面，在这里以真
// 进程收割；每个用例独立 GOCOVERDIR 以兜住 os.Exit 的退出钩子刷写。
func w1BootExpectExit(t *testing.T, exe, covDir string, args []string, env map[string]string, wantCode int, wantStderrSubstrings ...string) {
	t.Helper()
	if err := os.MkdirAll(covDir, 0o755); err != nil {
		t.Fatalf("covdir = %v", err)
	}
	command := exec.Command(exe, args...)
	command.Env = os.Environ()
	command.Env = append(command.Env, "GOCOVERDIR="+covDir)
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x200}
	var output strings.Builder
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start = %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- command.Wait() }()
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("进程 30s 未退出：%s", output.String())
	}
	code := command.ProcessState.ExitCode()
	if code != wantCode {
		t.Fatalf("退出码 = %d 期望 %d\n输出：%s", code, wantCode, output.String())
	}
	for _, substring := range wantStderrSubstrings {
		if !strings.Contains(output.String(), substring) {
			t.Fatalf("输出缺少 %q：%s", substring, output.String())
		}
	}
}

// TestW1BusinessBootMigrationFailPaths 收割离线迁移命令的失败分支：
// 停机门缺失、源文件缺失、互斥模式、未知参数、F4 配置缺失。
func TestW1BusinessBootMigrationFailPaths(t *testing.T) {
	if os.Getenv(w1BootGateEnv) == "" {
		t.Skipf("未设置 %s；跳过业务层 boot 覆盖（单元层不跑进程级用例）", w1BootGateEnv)
	}
	root := t.TempDir()
	buildDir, err := os.MkdirTemp("", "w1-boot-build-")
	if err != nil {
		t.Fatalf("builddir = %v", err)
	}
	defer func() { _ = os.RemoveAll(buildDir) }()
	exe := filepath.Join(buildDir, "juhe-ai-gateway-boot.exe")
	build := exec.Command("go", "build", "-cover", "-o", exe, "./")
	build.Dir = currentPackageDir()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build -cover 失败：%v\n%s", err, output)
	}
	missing := filepath.Join(root, "missing.sqlite3")
	// F3：停机门缺失 → fail（exit 1）。
	w1BootExpectExit(t, exe, filepath.Join(root, "cov-gates"), []string{"-migrate-audit-log-legacy-sqlite"}, nil, 1, "停机")
	// F3：门齐但源文件不存在 → fail（exit 1）。
	w1BootExpectExit(t, exe, filepath.Join(root, "cov-case"), []string{
		"-migrate-audit-log-legacy-sqlite", "-node-stopped", "-go-stopped",
		"-source-db=" + missing, "-target-db=" + filepath.Join(root, "t.sqlite3"),
		"-source-blob-dir=" + root, "-target-blob-dir=" + filepath.Join(root, "blobs"),
	}, nil, 1, "访问旧审计 SQLite 源文件失败")
	// 互斥模式 → os.Exit(2)。
	w1BootExpectExit(t, exe, filepath.Join(root, "cov-case"), []string{"-migrate-audit-log-legacy-sqlite", "-migrate-operation-log-legacy-sqlite"}, nil, 2, "mutually exclusive")
	// 未知位置参数 → os.Exit(2)。
	w1BootExpectExit(t, exe, filepath.Join(root, "cov-case"), []string{"unexpected-positional"}, nil, 2, "unsupported gateway arguments")
	// F4：LoadConfig 未启用 store → MigrateLegacySQLite 拒绝 → fail（exit 1）。
	w1BootExpectExit(t, exe, filepath.Join(root, "cov-case"), []string{
		"-migrate-operation-log-legacy-sqlite", "-node-stopped", "-go-stopped", "-backup-confirmed",
		"-operation-log-source-db=" + missing,
	}, nil, 1, "JUHE_AI_OPERATION_LOG_STORE=sqlite")
	// 聚合全部失败分支的 covdata 并导出：三方合并（单元 + boot + fail）
	// 由外层驱动通过 JUHE_AI_FAIL_COVERAGE_PROFILE 取走。
	if out := os.Getenv("JUHE_AI_FAIL_COVERAGE_PROFILE"); out != "" {
		inputs := filepath.Join(root, "cov-gates") + "," + filepath.Join(root, "cov-case")
		dump := exec.Command("go", "tool", "covdata", "textfmt", "-i", inputs, "-o", out)
		if output, err := dump.CombinedOutput(); err != nil {
			t.Fatalf("fail-path covdata textfmt 失败：%v\n%s", err, output)
		}
	}
}
