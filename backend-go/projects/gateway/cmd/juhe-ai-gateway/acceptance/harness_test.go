// X05 验收夹具：隔离环境构建、maintenance ensure+seed、被测进程启停（带诊断
// 日志尾部）、HTTP 客户端助手。PostgreSQL + Redis 是唯一存储形态
// （PLAN-20261024T051925945Z）：显式 PGDSN 缺省时夹具经 pgtestkit 自建每测试
// 隔离库（fresh DDL + seed，等价部署 runbook 的受控预置步骤）；Redis 连接串
// 自仓库根 .local dev env 解析（与 pgtestkit 同源），缺失时显式失败。
package acceptance

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// ---------------------------------------------------------------------------
// 通用助手
// ---------------------------------------------------------------------------

func randomHex(t *testing.T, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("read random bytes: %v", err)
	}
	return hex.EncodeToString(buf)
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("grab free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if port <= 0 {
		t.Fatalf("free port invalid: %d", port)
	}
	return port
}

func mustMkdirAll(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
}

// acceptanceAdminPassword 是 seed 管理员密码（Node seedDefaults 与 Go
// maintenance seed 一致：admin/admin，pbkdf2 封套按 Node crypto.ts 的
// base64url salt 文本语义派生，Node 与 Go 双侧均可验证）。
const acceptanceAdminPassword = "admin"

// acceptanceSharedEnvRelative 是 dev env 事实文件相对仓库根的固定位置（与
// pgtestkit 同源；Redis 连接串只从这里或显式 env 读取，内容不进入任何
// 文件、日志或断言）。
const acceptanceSharedEnvRelative = ".local/project-resources/dev/env/shared.env"

// acceptanceRedisEnvKey 是显式 Redis 连接串 env（值相同时 cache/state 可共用，
// 验收隔离面以独立 namespace 隔离键空间）。
const acceptanceRedisEnvOverride = "JUHE_AI_TEST_REDIS_URL"

// acceptanceRedisURL 解析验收进程族的 Redis 连接串：显式 env 优先，缺失时回退
// 仓库根 .local dev env 的 JUHE_AI_REDIS_CACHE_URL。未配置即显式失败（验收
// boot 的 Redis 契约是必填面，不静默跳过）。
func acceptanceRedisURL(t *testing.T) string {
	t.Helper()
	if raw := strings.TrimSpace(os.Getenv(acceptanceRedisEnvOverride)); raw != "" {
		return raw
	}
	for _, key := range []string{"JUHE_AI_REDIS_CACHE_URL", "JUHE_AI_REDIS_STATE_URL"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			return raw
		}
	}
	values := acceptanceSharedEnv(t)
	raw := strings.TrimSpace(values["JUHE_AI_REDIS_CACHE_URL"])
	if raw == "" {
		t.Fatalf("acceptance: Redis 连接串不可用（设 %s 或在仓库根 %s 配置 JUHE_AI_REDIS_CACHE_URL）",
			acceptanceRedisEnvOverride, acceptanceSharedEnvRelative)
	}
	return raw
}

// acceptanceSharedEnv 自当前目录向上查找并解析 shared.env 的 KEY=VALUE。
func acceptanceSharedEnv(t *testing.T) map[string]string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("acceptance: 读取工作目录失败: %v", err)
	}
	var envPath string
	for i := 0; i < 32; i++ {
		candidate := filepath.Join(dir, filepath.FromSlash(acceptanceSharedEnvRelative))
		if _, statErr := os.Stat(candidate); statErr == nil {
			envPath = candidate
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if envPath == "" {
		t.Fatalf("acceptance: 未找到 %s", acceptanceSharedEnvRelative)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("acceptance: 读取 dev env 失败: %v", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

// ---------------------------------------------------------------------------
// 被测进程管理
// ---------------------------------------------------------------------------

type managedProcess struct {
	cmd     *exec.Cmd
	logPath string
	stopped bool
}

// startProcess 启动一个被测二进制并接管 stdout/stderr 到独立日志文件。
func startProcess(t *testing.T, name string, binary string, env []string) *managedProcess {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), name+"-output.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create %s log: %v", name, err)
	}
	cmd := exec.Command(binary)
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if runtime.GOOS == "windows" {
		// 独立进程组：让 CTRL_BREAK 只投递给该子进程（干净停机用）。
		cmd.SysProcAttr = windowsNewProcessGroupAttr()
	}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = logFile.Close()
		stopProcess(t, name, cmd)
		if t.Failed() {
			t.Logf("%s log tail:\n%s", name, logTail(logPath, 4096))
		}
	})
	return &managedProcess{cmd: cmd, logPath: logPath}
}

func stopProcess(t *testing.T, name string, cmd *exec.Cmd) {
	t.Helper()
	if cmd.Process == nil || cmd.ProcessState != nil {
		return
	}
	_ = interruptProcess(cmd)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Logf("%s did not exit after interrupt; killing", name)
		_ = cmd.Process.Kill()
		<-done
	}
}

func logTail(path string, limit int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(read log %s: %v)", path, err)
	}
	if len(data) > limit {
		return string(data[len(data)-limit:])
	}
	return string(data)
}

// ---------------------------------------------------------------------------
// maintenance storage bootstrap
// ---------------------------------------------------------------------------

// provisionJ3bPostgresSchema 按部署 runbook 的受控预置步骤创建 juhe_j3b
// schema：gateway 的 J3b bootstrap 明确拒绝自行建 schema，验收 harness 作为
// 受控数据库流程必须先建 schema，再执行 --apply-j3b-model-check-postgres。
func provisionJ3bPostgresSchema(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres for j3b schema: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS juhe_j3b"); err != nil {
		t.Fatalf("create juhe_j3b schema: %v", err)
	}
}

// provisionJobsPostgresSchema 按部署 runbook 预置 juhe_jobs schema：jobs 的
// J1 account-health / J2 account-balance / J3a proxy-latency store 都要求
// schema 由外部受控流程创建且 owner 为当前 jobs role，进程自身拒绝建 schema。
func provisionJobsPostgresSchema(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres for juhe_jobs schema: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS juhe_jobs"); err != nil {
		t.Fatalf("create juhe_jobs schema: %v", err)
	}
}

// runMaintenanceAccountBalancePostgresApply 按部署 runbook 应用 J2 账户余额
// 四表（幂等）：jobs 的 J2 service 只读取已预置表，不自行建表。
func runMaintenanceAccountBalancePostgresApply(t *testing.T, dsn string) {
	t.Helper()
	cmd := exec.Command(maintenanceBinary, "--apply-account-balance-postgres")
	cmd.Env = append(os.Environ(), "JUHE_AI_MAINTENANCE_ACCOUNT_BALANCE_POSTGRES_URL="+dsn)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("maintenance account-balance postgres apply failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
}

// runMaintenanceProxyLatencyPostgresApply 按部署 runbook 应用 J3a 代理延迟
// jobs 表（幂等）：jobs 启动只校验已预置表，不自行建表。
func runMaintenanceProxyLatencyPostgresApply(t *testing.T, dsn string) {
	t.Helper()
	cmd := exec.Command(maintenanceBinary, "--apply-j3a-proxy-latency-postgres")
	cmd.Env = append(os.Environ(), "JUHE_AI_MAINTENANCE_J3A_POSTGRES_URL="+dsn)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("maintenance j3a proxy-latency postgres apply failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
}

// runMaintenanceJ3bPostgresApply 应用 juhe_j3b schema（幂等）：模型检测
// owner 默认常驻后，juhe_j3b 专属 schema 按契约由 maintenance 显式补齐。
func runMaintenanceJ3bPostgresApply(t *testing.T, dsn string) {
	t.Helper()
	cmd := exec.Command(maintenanceBinary, "--apply-j3b-model-check-postgres")
	cmd.Env = append(os.Environ(), "JUHE_AI_MAINTENANCE_J3B_POSTGRES_URL="+dsn)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("maintenance j3b postgres apply failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
}

// runMaintenanceEnsureSeed 调用 juhe-ai-maintenance --ensure-schema --seed
// （PostgreSQL 是唯一形态：--dsn 是唯一目标形态）。
func runMaintenanceEnsureSeed(t *testing.T, dsn string, secret string) map[string]any {
	t.Helper()
	args := []string{"--ensure-schema", "--seed", "--secret", secret, "--dsn", dsn}
	cmd := exec.Command(maintenanceBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("maintenance ensure+seed failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	var report map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode maintenance report: %v\nstdout=%s", err, stdout.String())
	}
	if report["ensureRan"] != true || report["seedRan"] != true {
		t.Fatalf("maintenance report must record ensure+seed: %#v", report)
	}
	return report
}

// ---------------------------------------------------------------------------
// gateway 隔离环境
// ---------------------------------------------------------------------------

type gatewayEnvOptions struct {
	// ChainEnabled 打开 /v1 网关链 + my-chat 家族。
	ChainEnabled bool
	// PGDSN 非空时使用该连接串；为空时夹具经 pgtestkit 自建每测试隔离库
	//（fresh DDL + seed）。
	PGDSN string
	// J3bPinnedListener 固定模型检测管理 listener 地址并暴露到 fixture.j3bURL
	//（模型检测已默认常驻，此项只为断言提供已知端口）。
	J3bPinnedListener bool
	// ExtraEnv 追加到网关子进程的 env（M6 起火山语音 E2E 用：把
	// JUHE_AI_GATEWAY_VOLCENGINE_SPEECH_BASE_URL 测试 seam 指向本夹具
	// mock 上游）。
	ExtraEnv map[string]string
}

type gatewayFixture struct {
	baseURL        string
	healthURL      string
	j3bURL         string
	root           string
	secret         string
	dsn            string
	redisURL       string
	redisNamespace string
	spoolDir       string
	assetsRoot     string
	// storage 暴露共享目录面（logs / codex-context 根 / usage-shards；jobs
	// 场景复用同一隔离目录布局）。数据库面统一走 dsn。
	storage map[string]string
	process *managedProcess
	admin   *http.Client
	t       *testing.T
}

// startGateway 以 fresh 隔离环境组装并启动 juhe-ai-gateway。PostgreSQL +
// Redis 唯一存储形态：schema/seed 由外部 maintenance 执行（与部署 runbook
// 一致；显式 PGDSN 缺省时经 pgtestkit 自建每测试隔离库），juhe_j3b schema
// 按契约由受控数据库流程预置。
func startGateway(t *testing.T, opts gatewayEnvOptions) *gatewayFixture {
	t.Helper()
	// 外部依赖授权门：未授权时 SKIP，绝不构建/启动子进程网关、绝不连 PG/Redis。
	pgtestkit.SkipUnlessExternalAuthorized(t)
	root := t.TempDir()
	fixture := &gatewayFixture{root: root, t: t}

	mainPort := freePort(t)
	healthPort := freePort(t)
	fixture.baseURL = fmt.Sprintf("http://127.0.0.1:%d", mainPort)
	fixture.healthURL = fmt.Sprintf("http://127.0.0.1:%d", healthPort)
	secret := randomHex(t, 16)
	fixture.secret = secret

	// 隔离 PG 库：显式 DSN 优先，缺省时 pgtestkit 自建（fresh DDL + seed）。
	dsn := opts.PGDSN
	if dsn == "" {
		testDB := pgtestkit.NewTestDatabase(t,
			func(ctx context.Context, db *sql.DB) error {
				_, err := bootstrap.EnsurePostgres(ctx, db)
				return err
			},
			func(ctx context.Context, db *sql.DB) error {
				_, err := bootstrap.SeedPostgres(ctx, db, bootstrap.SeedOptions{Now: time.Now, Secret: secret})
				return err
			})
		dsn = testDB.DSN
	} else {
		// 外部库同样走受控 ensure+seed（幂等）。
		runMaintenanceEnsureSeed(t, dsn, secret)
	}
	fixture.dsn = dsn

	codexRoot := filepath.Join(root, "storage", "codex-context")
	usageShardRoot := filepath.Join(root, "storage", "usage-shards")
	mustMkdirAll(t, codexRoot, usageShardRoot,
		filepath.Join(root, "storage", "audit-blobs"),
		filepath.Join(root, "chat-assets"),
		filepath.Join(root, "openai-files"),
		filepath.Join(root, "logs"),
	)

	spoolDir := filepath.Join(root, "storage", "usage-record-spool")
	fixture.spoolDir = spoolDir
	fixture.assetsRoot = filepath.Join(root, "chat-assets")

	// PG 唯一形态下 gateway↔jobs 经 spool 目录与 PG 交接；Redis 键空间以
	// 每测试唯一 namespace 隔离，避免并发验收互踩。
	redisURL := acceptanceRedisURL(t)
	namespace := "acc-" + randomHex(t, 6)
	fixture.redisURL = redisURL
	fixture.redisNamespace = namespace

	env := map[string]string{
		"JUHE_AI_HOST":                          "127.0.0.1",
		"JUHE_AI_PORT":                          fmt.Sprint(mainPort),
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS": fmt.Sprintf("127.0.0.1:%d", healthPort),
		// 2026-09-21 起模型检测 owner 默认常驻：数据目录收敛进隔离 storage，
		// 管理 listener 用随机端口避免并发场景抢占 3307。
		"JUHE_AI_DATA_DIR":                      filepath.Join(root, "storage"),
		"JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS": "127.0.0.1:0",
		"JUHE_AI_SECRET":                        secret,
		// PostgreSQL + Redis 唯一存储形态（2026-10-24 存储形态收口）：三个
		// 连接串必填；F3/F4 store URL 缺省回落主连接串，实例 ID 显式钉死。
		"JUHE_AI_POSTGRES_URL":          dsn,
		"JUHE_AI_BUSINESS_POSTGRES_URL": dsn,
		"JUHE_AI_REDIS_CACHE_URL":       redisURL,
		"JUHE_AI_REDIS_STATE_URL":       redisURL,
		"JUHE_AI_REDIS_NAMESPACE":       namespace,
		// J3b 常驻（2026-09-21）后 circuit runtime 要求显式连接串（与主链
		// 共享同一 Redis 实例时必须显式声明并隔离键空间：独立 namespace）。
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL":       redisURL,
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE": namespace + "-j3b",
		"JUHE_AI_BUSINESS_OWNER":              "gateway",
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED":  "true",
		"JUHE_AI_AUTH_CAPTCHA_DISABLED":       "true",
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID":       "acceptance-gateway",
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY":    filepath.Join(root, "storage", "audit-blobs"),
		// E2E-FINDING #10：验收链路要求每次成功请求的 attempts 归因都保留，
		// 采样率 1 = 全采样（成功信封 retention days 默认 3，满足 LoadConfig
		// 「rate 与 retention days 同时为 0 或同时 > 0」联动校验）。
		"JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE": "1",
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID":     "acceptance-gateway",
		"JUHE_AI_CHAT_ASSETS_ROOT":              fixture.assetsRoot,
		"JUHE_AI_OPENAI_COMPATIBLE_FILES_ROOT":  filepath.Join(root, "openai-files"),
		"JUHE_AI_USAGE_SPOOL_DIRECTORY":         spoolDir,
	}
	fixture.storage = map[string]string{
		"dsn":          dsn,
		"codex-root":   codexRoot,
		"usage-shards": usageShardRoot,
		"logs":         filepath.Join(root, "logs"),
	}
	if opts.ChainEnabled {
		env["JUHE_AI_GATEWAY_CHAIN_ENABLED"] = "true"
	}
	for key, value := range opts.ExtraEnv {
		env[key] = value
	}
	if opts.J3bPinnedListener {
		j3bPort := freePort(t)
		fixture.j3bURL = fmt.Sprintf("http://127.0.0.1:%d", j3bPort)
		env["JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS"] = fmt.Sprintf("127.0.0.1:%d", j3bPort)
	}
	// 模型检测 owner 默认常驻（2026-09-21）：juhe_j3b schema 按契约由受控
	// 数据库流程预置，harness 与部署 runbook 保持一致。
	provisionJ3bPostgresSchema(t, dsn)
	runMaintenanceJ3bPostgresApply(t, dsn)
	// jobs 侧 J1/J2/J3a 三族 store 都只校验 juhe_jobs schema 存在且 owner
	// 为当前 role，表由各 store 的 EnsureSchema 自建（与部署 runbook 的
	// 「先 CREATE SCHEMA AUTHORIZATION 再 apply」序列一致）。
	provisionJobsPostgresSchema(t, dsn)
	runMaintenanceAccountBalancePostgresApply(t, dsn)
	runMaintenanceProxyLatencyPostgresApply(t, dsn)
	// J3b circuit runtime owner fence（与部署 jobs 预置 runtime-index-meta
	// 同一契约）：fresh 验收环境的 state Redis 无历史就绪哈希，harness 按
	// circuitruntime.Store.CheckReady 的键位契约预置。
	provisionCircuitRuntimeIndex(t, redisURL, namespace+"-j3b")
	// X04 logreads：F1 runtime-log 数据集表由 jobs 进程自建（gateway 只读）；
	// fresh 验收环境由夹具按生产 DDL 预置读面（helper 在 log_faces_test.go）。
	provisionRuntimeLogDataset(t, dsn)

	fixture.process = startProcess(t, "gateway", gatewayBinary, envMapToSlice(env))
	waitForSystemAPIReady(t, fixture)
	fixture.admin = fixture.loginClient(t, "admin", acceptanceAdminPassword)
	return fixture
}

// provisionCircuitRuntimeIndex 按 jobs 部署侧的 runtime-index-meta 预置契约
// 写入就绪哈希（键位与 circuitruntime.Store.CheckReady 一致；namespace 走
// 加载层同款 canonical 短形式）。
func provisionCircuitRuntimeIndex(t *testing.T, redisURL, namespace string) {
	t.Helper()
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse acceptance redis url: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := "juhe-ai:" + namespace + ":account-circuit:gateway-account-circuit:runtime-index-meta"
	if err := client.HSet(ctx, key,
		"version", "1", "status", "ready", "ownerMode", "go-runtime-state-v1").Err(); err != nil {
		t.Fatalf("seed circuit runtime index: %v", err)
	}
}

// waitForSystemAPIReady 等待业务系统 API 面（/__aisys__/api/health 200 +
// Node db-service 契约字段）。所有场景的公共就绪门。F4 producer 与
// input server 共享同一进程级 owner lease（LeaseKeeper），无租约争用，
// 就绪不应滞后。
func waitForSystemAPIReady(t *testing.T, fixture *gatewayFixture) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(fixture.baseURL + "/__aisys__/api/health")
		if err == nil {
			var payload map[string]any
			err = json.NewDecoder(response.Body).Decode(&payload)
			_ = response.Body.Close()
			if err == nil && response.StatusCode == http.StatusOK && payload["service"] == "juhe-ai-db-service" {
				return
			}
		}
		if fixture.process.cmd.ProcessState != nil {
			t.Fatalf("gateway exited during startup:\n%s", logTail(fixture.process.logPath, 4096))
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("gateway system api not ready in time:\n%s", logTail(fixture.process.logPath, 4096))
}

// waitForProcessHealthReady 等待进程级 /health ready==true（owner/worker
// 契约面）。
func waitForProcessHealthReady(t *testing.T, fixture *gatewayFixture) map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(fixture.healthURL + "/health")
		if err == nil {
			var payload map[string]any
			err = json.NewDecoder(response.Body).Decode(&payload)
			_ = response.Body.Close()
			if err == nil && response.StatusCode == http.StatusOK && payload["ready"] == true {
				return payload
			}
		}
		if fixture.process.cmd.ProcessState != nil {
			t.Fatalf("gateway exited while waiting process health:\n%s", logTail(fixture.process.logPath, 4096))
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("gateway process /health never became ready:\n%s", logTail(fixture.process.logPath, 4096))
	return nil
}

func envMapToSlice(env map[string]string) []string {
	slice := make([]string, 0, len(env)+8)
	for key, value := range env {
		slice = append(slice, key+"="+value)
	}
	return append(slice, os.Environ()...)
}

// ---------------------------------------------------------------------------
// HTTP 客户端助手
// ---------------------------------------------------------------------------

type acceptanceClient struct {
	t       *testing.T
	http    *http.Client
	baseURL string
}

func newClient(t *testing.T, baseURL string) *acceptanceClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &acceptanceClient{t: t, http: &http.Client{Timeout: 30 * time.Second, Jar: jar}, baseURL: baseURL}
}

func (f *gatewayFixture) loginClient(t *testing.T, username, password string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &acceptanceClient{t: t, http: &http.Client{Timeout: 30 * time.Second, Jar: jar}, baseURL: f.baseURL}
	client.do(http.MethodPost, "/__aisys__/api/auth/login", map[string]any{"username": username, "password": password}, wantStatus(http.StatusOK))
	// BUG-0224 起 seed 账户首登命中全局强制改密 gate（403 must_change_password
	// 拦截管理面；/auth/me 挂 auth 路由族不受 gate，可读 mustChangePassword）。
	// 为 true 时双改密解除：must 态改密免验旧密（先改到临时密码），再以临时
	// 密码验旧密改回原密码——净效果密码不变、gate 解除、当前会话保留
	// （RevokeOtherSessions 只踢其他会话），后续断言语义不变。
	status, me := client.do(http.MethodGet, "/__aisys__/api/auth/me", nil, wantStatus(http.StatusOK))
	summary, _ := me["data"].(map[string]any)
	if status == http.StatusOK && summary != nil && summary["mustChangePassword"] == true {
		tempPassword := "juhe-ai-acceptance-tmp-2026"
		client.do(http.MethodPost, "/__aisys__/api/auth/change-password", map[string]any{"newPassword": tempPassword}, wantStatus(http.StatusOK))
		client.do(http.MethodPost, "/__aisys__/api/auth/change-password", map[string]any{"oldPassword": tempPassword, "newPassword": password}, wantStatus(http.StatusOK))
	}
	return client.http
}

type wantStatusOption int

func wantStatus(status int) wantStatusOption { return wantStatusOption(status) }

// do 发送 JSON 请求并返回解码后的响应体；status>0 时强断言。
func (c *acceptanceClient) do(method, path string, body any, expected ...wantStatusOption) (int, map[string]any) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal %s %s body: %v", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		c.t.Fatalf("build %s %s: %v", method, path, err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return c.doRequest(request, expected...)
}

func (c *acceptanceClient) doRequest(request *http.Request, expected ...wantStatusOption) (int, map[string]any) {
	c.t.Helper()
	response, err := c.http.Do(request)
	if err != nil {
		c.t.Fatalf("%s %s: %v", request.Method, request.URL.Path, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		c.t.Fatalf("read %s %s body: %v", request.Method, request.URL.Path, err)
	}
	payload := map[string]any{}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &payload); err != nil {
			payload = nil
		}
	}
	if len(expected) > 0 && int(expected[0]) != 0 && int(expected[0]) != response.StatusCode {
		c.t.Fatalf("%s %s status=%d want %d body=%s",
			request.Method, request.URL.Path, response.StatusCode, int(expected[0]), string(raw))
	}
	return response.StatusCode, payload
}

// data 返回 Node ok() 信封的 data 字段。
func data(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	nested, _ := payload["data"].(map[string]any)
	return nested
}

func dataString(payload map[string]any, key string) string {
	nested := data(payload)
	if nested == nil {
		return ""
	}
	text, _ := nested[key].(string)
	return text
}
