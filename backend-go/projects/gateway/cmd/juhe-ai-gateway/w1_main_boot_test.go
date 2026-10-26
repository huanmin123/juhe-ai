package main

// w1（业务层）：进程级 boot 覆盖收割。按 docs/develop/后端测试分层规则.md，
// main()/信号/监听生命周期属于业务层：go build -cover 构建真进程，pgtestkit
// 隔离 PG 库 + miniredis + 空闲端口启动，健康检查通过后优雅退出
//（CTRL_BREAK），go tool covdata textfmt 收割二进制覆盖率并与单元 profile
// 合并。未设置 JUHE_AI_BUSINESS_BOOT_COVERAGE 时跳过——单元层不跑进程级用例。
//
// 2026-10-24 存储形态收口（PLAN-20261024T051925945Z）：原 SQLite 六库场景
// （maintenance --driver sqlite ensure、J3b sqlite store、六库路径 fail-fast
// 场景五）随生产 sqlite 臂删除，无 PG 对应物的失败臂同批退役；owner/compose/
// J3b 三场景按 PG 唯一形态重写（pgtestkit fresh DDL + seed 等价 maintenance
// --ensure-schema --seed，J3b 专属 schema 复用 w1g2EnsureJ3bSchema 补建）。
// 离线迁移命令失败分支（原 TestW1BusinessBootMigrationFailPaths）随
// --migrate-*-legacy-sqlite 命令物理删除，旗标拒绝臂由 w1_boot_cover_test.go
// 场景 C/D 承载。

import (
	"context"
	"database/sql"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const w1BootGateEnv = "JUHE_AI_BUSINESS_BOOT_COVERAGE"

func TestW1BusinessBootGatewayCoverage(t *testing.T) {
	if os.Getenv(w1BootGateEnv) == "" {
		t.Skipf("未设置 %s；跳过业务层 boot 覆盖（单元层不跑进程级用例）", w1BootGateEnv)
	}
	root := t.TempDir()
	exe := w1bBuildCoverBinary(t)

	// 场景一：被动网关（standby 归属）——健康监听 + 信号优雅退出（不触库）。
	passivePort := w1FreePort(t)
	w1BootRun(t, exe, root, "passive", w1BootScenarioEnv(t, root, "passive",
		"JUHE_AI_BLUE_GREEN_OWNER_MODE=standby",
	), []string{"-health-listen-address", "127.0.0.1:" + passivePort},
		func(body string) bool { return strings.Contains(body, `"ownerMode":"standby"`) })

	// 共享 miniredis：owner/compose 场景的 cache/state 驱动与 J3b circuit
	// runtime 的 Redis 契约面（客户端协议由 miniredis 承载，进程间零共享键）。
	mini := miniredis.NewMiniRedis()
	if err := mini.Start(); err != nil {
		t.Fatalf("miniredis start = %v", err)
	}
	defer mini.Close()
	redisURL := "redis://" + mini.Addr()
	// circuit runtime 的 runtime-index-meta 就绪哈希（CheckReady 契约面）：
	// 主链 namespace 与各场景的 J3b circuit namespace 各预置一份。
	w1g2SeedCircuitRuntimeIndex(t, redisURL, w1BootRedisNamespace)
	w1g2SeedCircuitRuntimeIndex(t, redisURL, w1BootJ3bNamespace)

	// 场景二：全 owner 启动（system-api 关闭，跳过交接证据门）——
	// F3 audit owner 就绪 → /health ready=true → 优雅退出。
	mainPort := w1FreePort(t)
	healthPort := w1FreePort(t)
	ownerEnv := w1BootScenarioEnv(t, root, "owner",
		"NODE_ENV=development",
		"JUHE_AI_POSTGRES_URL="+w1BootOwnerPostgres(t).DSN,
		"JUHE_AI_REDIS_CACHE_URL="+redisURL,
		"JUHE_AI_REDIS_STATE_URL="+redisURL,
		"JUHE_AI_REDIS_NAMESPACE="+w1BootRedisNamespace,
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL="+redisURL,
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE="+w1BootJ3bNamespace,
		"JUHE_AI_SECRET=w1-boot-owner-secret-0123456789abcdef",
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1-boot-instance",
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY="+filepath.Join(root, "blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY="+filepath.Join(root, "hotsearch"),
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1-boot-instance",
		"JUHE_AI_HOST=127.0.0.1",
		"JUHE_AI_PORT="+mainPort,
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:"+healthPort,
	)
	w1BootRun(t, exe, root, "owner", ownerEnv, nil, func(body string) bool {
		return strings.Contains(body, `"ready":true`)
	})

	// 场景三：SystemAPIEnabled + ChainEnabled 全装配 boot——PG 隔离库
	//（w1bScenarioEnv 注入 fresh DDL + seed），/health ready 后经主端口打
	// /v1/models（无 Key → 401 JSON）。
	composeMainPort := w1FreePort(t)
	composeHealthPort := w1FreePort(t)
	composeRoot := filepath.Join(root, "compose-fresh")
	if err := os.MkdirAll(composeRoot, 0o755); err != nil {
		t.Fatalf("compose root = %v", err)
	}
	composeEnv := w1BootScenarioEnv(t, root, "compose",
		"NODE_ENV=development",
		// 独立 F3/F4 实例 ID 与隔离库：上一场景的 audit owner 租约仍在租期
		// 内，不能复用（显式 POSTGRES_URL 阻断 w1bScenarioEnv 的缺省注入）。
		"JUHE_AI_POSTGRES_URL="+w1BootOwnerPostgres(t).DSN,
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1-boot-instance-compose",
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY="+filepath.Join(composeRoot, "blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY="+filepath.Join(composeRoot, "hotsearch"),
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1-boot-operation-compose",
		"JUHE_AI_SECRET=w1-boot-composition-secret-0123456789abcdef",
		"JUHE_AI_REDIS_CACHE_URL="+redisURL,
		"JUHE_AI_REDIS_STATE_URL="+redisURL,
		"JUHE_AI_REDIS_NAMESPACE="+w1BootRedisNamespace,
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL="+redisURL,
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE="+w1BootJ3bNamespace,
		"JUHE_AI_HOST=127.0.0.1",
		"JUHE_AI_PORT="+composeMainPort,
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:"+composeHealthPort,
	)
	composeEnv = append(composeEnv, w1BuildSystemApiEvidenceEnv()...)
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

	// 场景四：J3b owner（SystemAPI 关闭，J3B_ENABLED=true）——miniredis 承载
	// circuit runtime 的 Redis 契约（runtime-index-meta 就绪哈希），隔离库已
	// 随 w1BootOwnerPostgres 补建 juhe_j3b 专属 schema（组合根按契约拒绝自行
	// 建 schema）；健康探针等待 j3bReady=true。
	j3bBusiness := w1BootOwnerPostgres(t)
	j3bPort := w1FreePort(t)
	j3bHealthPort := w1FreePort(t)
	j3bEnv := w1BootScenarioEnv(t, root, "j3b",
		"NODE_ENV=development",
		"JUHE_AI_POSTGRES_URL="+j3bBusiness.DSN,
		"JUHE_AI_BUSINESS_POSTGRES_URL="+j3bBusiness.DSN,
		"JUHE_AI_REDIS_CACHE_URL="+redisURL,
		"JUHE_AI_REDIS_STATE_URL="+redisURL,
		"JUHE_AI_REDIS_NAMESPACE="+w1BootRedisNamespace,
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL="+redisURL,
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE="+w1BootJ3bNamespace,
		"JUHE_AI_SECRET=w1-boot-j3b-secret-0123456789abcdef",
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1-boot-instance-j3b",
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY="+filepath.Join(root, "j3b-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY="+filepath.Join(root, "j3b-hotsearch"),
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1-boot-operation-j3b",
		"JUHE_AI_HOST=127.0.0.1",
		"JUHE_AI_PORT="+w1FreePort(t),
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=127.0.0.1:"+j3bHealthPort,
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=false",
		"JUHE_AI_GATEWAY_CHAIN_ENABLED=false",
		"JUHE_AI_J3B_ENABLED=true",
		"JUHE_AI_J3B_OWNER=gateway",
		"JUHE_AI_J3B_INSTANCE_ID=w1-boot-j3b",
		"JUHE_AI_J3B_POSTGRES_URL="+j3bBusiness.DSN,
		"JUHE_AI_J3B_BUSINESS_POSTGRES_URL="+j3bBusiness.DSN,
		"JUHE_AI_J3B_CREDENTIAL_SECRET=w1-boot-j3b-credential-secret-0123456789",
		"JUHE_AI_J3B_IDENTITY_SECRET=w1-boot-j3b-identity-secret-0123456789",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED=true",
		"JUHE_AI_J3B_SCHEMA_READY=true",
		"JUHE_AI_J3B_HEALTH_BOUNDARY_READY=true",
		"JUHE_AI_J3B_RUNTIME_READY=true",
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL="+redisURL,
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE="+w1BootJ3bNamespace,
		"JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS=127.0.0.1:"+j3bPort,
	)
	// 生产缺陷 #5 已修复（retention mode 按枚举映射）：本场景转正为成功
	// 场景，收割 J3b 装配全段（Business connection/authenticator/
	// retention/circuit control-plane+circuit runtime Redis/key-model/
	// projector/host 装配 + 管理监听 + 组件节拍）。
	w1BootRun(t, exe, root, "j3b", j3bEnv, nil, func(body string) bool {
		return strings.Contains(body, `"j3bReady":true`) && strings.Contains(body, `"ready":true`)
	})
	// 场景六：J3b 装配段 fail fast——circuit runtime Redis ping 失败、
	// runtime-index-meta 未就绪。（原「cutover evidence 缺失/纪元不匹配」
	// 两臂随切流门禁退役删除：VerifyConfiguredCutoverEvidence 已于清理批次
	// C1（2026-09-30）物理删除，证据路径 env 不再被读取，进程不再因此退出。）
	j3bBadRedis := append([]string{}, j3bEnv...)
	j3bBadRedis = append(j3bBadRedis, "JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://127.0.0.1:1")
	w1BootExpectExit(t, exe, filepath.Join(root, "cov-case"), nil, j3bBadRedis, 1, "ping J3b Gateway circuit runtime Redis")
	// runtime-index-meta 未就绪：新 miniredis 不预置就绪哈希 → CheckReady fail。
	miniBare := miniredis.NewMiniRedis()
	if err := miniBare.Start(); err != nil {
		t.Fatalf("miniredis bare start = %v", err)
	}
	defer miniBare.Close()
	j3bNoIndex := append([]string{}, j3bEnv...)
	j3bNoIndex = append(j3bNoIndex, "JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://"+miniBare.Addr())
	w1BootExpectExit(t, exe, filepath.Join(root, "cov-case"), nil, j3bNoIndex, 1, "verify J3b Gateway circuit runtime owner fence")
	// 覆盖率合并：covdata textfmt → 与单元 profile（可选）按块取 max。
	bootProfile := filepath.Join(root, "boot.out")
	// 全部场景的 cov 目录必须全部聚合；此前只收 owner 场景，
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

// w1BootRedisNamespace 是本文件 boot 场景的共享 Redis namespace（miniredis
// 进程内实例本就与其他进程隔离，namespace 仅约束键位形状）。
const w1BootRedisNamespace = "w1-boot"

// w1BootJ3bNamespace 是各场景 J3b circuit runtime 的独立 namespace（与主链
// states hash 键空间分离，符合 J3B_CIRCUIT 共享语义门）。
const w1BootJ3bNamespace = "w1-boot-j3b"

// w1BootOwnerPostgres 提供 owner/compose/J3b 场景的隔离 PG 测试库（fresh
// DDL + seed 等价 maintenance --ensure-schema --seed；J3b owner 常驻后启动
// 即校验 juhe_j3b schema，w1g2EnsureJ3bSchema 补建；测试结束自动删库）。
func w1BootOwnerPostgres(t *testing.T) *pgtestkit.TestDatabase {
	t.Helper()
	db := pgtestkit.NewTestDatabase(t,
		func(ctx context.Context, handle *sql.DB) error {
			_, err := bootstrap.EnsurePostgres(ctx, handle)
			return err
		},
		func(ctx context.Context, handle *sql.DB) error {
			_, err := bootstrap.SeedPostgres(ctx, handle, bootstrap.SeedOptions{Now: time.Now, Secret: "w1-boot-scenario-secret"})
			return err
		})
	j3bDB, err := sql.Open("pgx", db.DSN)
	if err != nil {
		t.Fatalf("open j3b schema handle = %v", err)
	}
	w1g2EnsureJ3bSchema(t, j3bDB)
	_ = j3bDB.Close()
	return db
}

// w1BootScenarioEnv 组装 boot 场景 env：复用 w1bScenarioEnv 的宿主 env 剥离
// 与 PG 缺省注入，GOCOVERDIR 钉到本测试根的 cov/<label> 场景目录（供文件尾
// 的 covdata 合并收割）。
func w1BootScenarioEnv(t *testing.T, root, label string, pairs ...string) []string {
	t.Helper()
	covDir := filepath.Join(root, "cov", label)
	if err := os.MkdirAll(covDir, 0o755); err != nil {
		t.Fatalf("covdir = %v", err)
	}
	return w1bScenarioEnv(t, covDir, pairs...)
}

// w1BootRun 启动网关真进程（GOCOVERDIR 隔离），轮询健康检查直到探针满足，
// 发 CTRL_BREAK 优雅退出，断言退出码为 0。
func w1BootRun(t *testing.T, exe, root, label string, env []string, extraArgs []string, probe func(body string) bool, afterReady ...func()) {
	t.Helper()
	covDir := filepath.Join(root, "cov", label)
	command := exec.Command(exe, extraArgs...)
	command.Env = env
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

// w1BootHealthPort 从 extraArgs 与场景 env 中解析健康监听端口。
func w1BootHealthPort(label string, env []string, extraArgs []string) string {
	for i := 0; i+1 < len(extraArgs); i += 2 {
		if extraArgs[i] == "-health-listen-address" {
			if _, port, err := net.SplitHostPort(extraArgs[i+1]); err == nil {
				return port
			}
		}
	}
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="); ok {
			if _, port, err := net.SplitHostPort(value); err == nil {
				return port
			}
		}
	}
	_ = label
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

// w1BuildSystemApiEvidence 返回系统 API 组合根启动所需的基础 env。
// 清理批次 C1（2026-09-30）：cutover evidence 构造已随切流门禁退役
//（evidencePath 恒空，保留签名兼容调用点）；门禁 env 家族不再解析。
// 2026-10-24 存储形态收口：原 sqlite 库路径键（BUSINESS/CHAT_DATABASE_PATH）
// 随存储形态退役删除，调用方按 PG 唯一形态注入连接串。
func w1BuildSystemApiEvidence(t *testing.T, root string) (evidencePath string, env map[string]string) {
	t.Helper()
	_ = root
	return "", w1BuildSystemApiEvidenceMap()
}

// w1BuildSystemApiEvidenceEnv 是 w1BuildSystemApiEvidence 的 env 片段形态
// （boot 场景直接 append 进场景 env）。
func w1BuildSystemApiEvidenceEnv() []string {
	env := w1BuildSystemApiEvidenceMap()
	pairs := make([]string, 0, len(env))
	for key, value := range env {
		pairs = append(pairs, key+"="+value)
	}
	return pairs
}

func w1BuildSystemApiEvidenceMap() map[string]string {
	return map[string]string{
		"JUHE_AI_BUSINESS_OWNER":             "gateway",
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED": "true",
		"JUHE_AI_GATEWAY_CHAIN_ENABLED":      "true",
	}
}

// w1BootExpectExit 运行网关进程直到自然退出，断言退出码与 stderr 片段。
// J3b fail-fast 分支的 fail()/os.Exit(1) 不属于进程内可测面，在这里以真
// 进程收割；每个用例把 GOCOVERDIR 覆写为独立目录以兜住 os.Exit 的退出钩子
// 刷写（场景 env 携带的 GOCOVERDIR 属成功场景，不复用）。
func w1BootExpectExit(t *testing.T, exe, covDir string, args []string, env []string, wantCode int, wantStderrSubstrings ...string) {
	t.Helper()
	if err := os.MkdirAll(covDir, 0o755); err != nil {
		t.Fatalf("covdir = %v", err)
	}
	stripped := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, "GOCOVERDIR=") {
			continue
		}
		stripped = append(stripped, entry)
	}
	stripped = append(stripped, "GOCOVERDIR="+covDir)
	command := exec.Command(exe, args...)
	command.Env = stripped
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
