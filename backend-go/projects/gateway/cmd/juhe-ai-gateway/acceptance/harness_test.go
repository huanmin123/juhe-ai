// X05 验收夹具：隔离环境构建、J3b cutover evidence 生成、maintenance
// ensure+seed、被测进程启停（带诊断日志尾部）、HTTP 客户端助手。
package acceptance

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
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
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
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

func mustTouchFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("touch %s: %v", path, err)
	}
}

// acceptanceAdminPassword 是 seed 管理员密码（Node seedDefaults 与 Go
// maintenance seed 一致：admin/admin，pbkdf2 封套按 Node crypto.ts 的
// base64url salt 文本语义派生，Node 与 Go 双侧均可验证）。
const acceptanceAdminPassword = "admin"

// provisionOperationLogBusinessSettingsMirror 按 F4 读模型约定播种业务设置
// 镜像：actor 显示名与 operationLogRetentionDays 都从该只读镜像解析。空
// 镜像文件会让带条目的 operation-logs 列表 500（system_accounts 表不存在）。
func provisionOperationLogBusinessSettingsMirror(t *testing.T, mirrorPath, businessPath string) {
	t.Helper()
	business, err := sql.Open("sqlite", "file:"+businessPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open business db for F4 mirror: %v", err)
	}
	rows, err := business.Query(`SELECT id, username, COALESCE(display_name, '') FROM system_accounts`)
	if err != nil {
		business.Close()
		t.Fatalf("read business system_accounts for F4 mirror: %v", err)
	}
	type accountRow struct{ id, username, displayName string }
	accounts := []accountRow{}
	for rows.Next() {
		var row accountRow
		if err := rows.Scan(&row.id, &row.username, &row.displayName); err != nil {
			rows.Close()
			business.Close()
			t.Fatalf("scan system_accounts: %v", err)
		}
		accounts = append(accounts, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		business.Close()
		t.Fatalf("iterate system_accounts: %v", err)
	}
	if err := business.Close(); err != nil {
		t.Fatalf("close business db: %v", err)
	}

	mirror, err := sql.Open("sqlite", "file:"+mirrorPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open F4 business settings mirror: %v", err)
	}
	defer mirror.Close()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`,
		`CREATE TABLE IF NOT EXISTS system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL, display_name TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := mirror.Exec(statement); err != nil {
			t.Fatalf("provision F4 mirror schema: %v: %v", statement, err)
		}
	}
	for _, row := range accounts {
		if _, err := mirror.Exec(`INSERT OR IGNORE INTO system_accounts (id, username, display_name) VALUES (?, ?, ?)`, row.id, row.username, row.displayName); err != nil {
			t.Fatalf("seed F4 mirror account %s: %v", row.id, err)
		}
	}
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
// J3b cutover evidence（gateway 启动门禁要求的交接证据）
// ---------------------------------------------------------------------------

// writeCutoverEvidence 生成 gateway 启动门禁（VerifyConfiguredCutoverEvidence）
// 可接受的最小证据集：备份工件 + readback manifest + evidence JSON。真实
// 生产证据由迁移流程生成；验收环境只证明「门禁在位且可被合法证据满足」。
func writeCutoverEvidence(t *testing.T, root, ownerEpoch string) string {
	t.Helper()
	now := time.Now().UTC()

	// 备份工件：任意稳定内容 + SHA-256。
	artifactPath := filepath.Join(root, "acceptance-backup.bin")
	artifactBody := []byte("juhe-ai acceptance backup artifact " + ownerEpoch)
	if err := os.WriteFile(artifactPath, artifactBody, 0o644); err != nil {
		t.Fatalf("write backup artifact: %v", err)
	}
	artifactSum := sha256.Sum256(artifactBody)

	tables := make([]contracts.J3bReadbackTableDigest, 0, 9)
	emptySum := sha256.Sum256(nil)
	emptyDigest := hex.EncodeToString(emptySum[:])
	for _, name := range []string{
		"account_quality_health_hourly",
		"model_check_items",
		"model_check_observations",
		"model_check_runs",
		"model_account_trust_results",
		"model_token_intercept_baseline_versions",
		"model_trust_aggregation_state",
		"model_trust_latest_dirty_accounts",
		"model_trust_observation_receipts",
	} {
		tables = append(tables, contracts.J3bReadbackTableDigest{
			Name: name, SourceRows: 0, TargetRows: 0,
			SourceDigest: emptyDigest, TargetDigest: emptyDigest,
		})
	}
	manifest := contracts.J3bReadbackManifest{
		FormatVersion:          contracts.J3bReadbackManifestFormatVersion,
		Scope:                  contracts.J3bReadbackManifestScope,
		Producer:               "acceptance-harness",
		SourceSnapshotIdentity: "acceptance-snapshot",
		SourceSchema:           "legacy-sqlite-dataset+stats",
		TargetSchema:           "juhe-j3b-sqlite",
		ProjectionComplete:     true,
		VerifiedAt:             now.Format(time.RFC3339),
		Tables:                 tables,
	}
	manifestHash, err := contracts.ComputeJ3bReadbackManifestHash(manifest)
	if err != nil {
		t.Fatalf("compute readback manifest hash: %v", err)
	}
	manifest.ManifestHash = manifestHash
	manifestPath := filepath.Join(root, "acceptance-readback.json")
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal readback manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, manifestBytes, 0o644); err != nil {
		t.Fatalf("write readback manifest: %v", err)
	}
	manifestSum := sha256.Sum256(manifestBytes)

	evidence := map[string]any{
		"oldOwner":       "node",
		"newOwner":       "go-gateway",
		"ownerEpoch":     ownerEpoch,
		"drainCompleted": true,
		"inFlight":       0,
		"activePathZero": true,
		"backupArtifact": map[string]any{
			"path": artifactPath,
			"hash": hex.EncodeToString(artifactSum[:]),
		},
		"rollbackReplayCursor": "acceptance-cursor-0",
		"freshness": map[string]any{
			"capturedAt":    now.Format(time.RFC3339),
			"maxAgeSeconds": 24 * 60 * 60,
		},
		"sourceDigest": "",
		"targetDigest": "",
		"readbackManifest": map[string]any{
			"path":                   manifestPath,
			"hash":                   hex.EncodeToString(manifestSum[:]),
			"formatVersion":          contracts.J3bReadbackManifestFormatVersion,
			"scope":                  contracts.J3bReadbackManifestScope,
			"sourceSnapshotIdentity": "acceptance-snapshot",
			"sourceSchema":           "legacy-sqlite-dataset+stats",
			"targetSchema":           "juhe-j3b-sqlite",
		},
		"blockedFindings": 0,
	}
	evidencePath := filepath.Join(root, "acceptance-cutover-evidence.json")
	evidenceBytes, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("marshal cutover evidence: %v", err)
	}
	if err := os.WriteFile(evidencePath, evidenceBytes, 0o644); err != nil {
		t.Fatalf("write cutover evidence: %v", err)
	}
	return evidencePath
}

// ---------------------------------------------------------------------------
// maintenance storage bootstrap
// ---------------------------------------------------------------------------

// runMaintenanceEnsureSeed 调用 juhe-ai-maintenance --ensure-schema --seed。
func runMaintenanceEnsureSeed(t *testing.T, driver string, paths string, dsn string, secret string) map[string]any {
	t.Helper()
	args := []string{"--ensure-schema", "--seed", "--driver", driver, "--secret", secret}
	if driver == "sqlite" {
		args = append(args, "--paths", paths)
	} else {
		args = append(args, "--dsn", dsn)
	}
	cmd := exec.Command(maintenanceBinary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("maintenance ensure+seed (%s) failed: %v\nstdout=%s\nstderr=%s", driver, err, stdout.String(), stderr.String())
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
	// OIDC 启用公开协议面（issuer 指向本实例）。
	OIDC bool
	// PGDSN 非空时以 postgres 模式组装（其余路径仍指向隔离临时目录的
	// 审计/操作日志等专用存储）。
	PGDSN string
}

type gatewayFixture struct {
	baseURL    string
	healthURL  string
	root       string
	secret     string
	spoolDir   string
	assetsRoot string
	// storage 暴露六库路径与专用目录（jobs 场景复用同一隔离存储布局）。
	storage map[string]string
	process *managedProcess
	admin   *http.Client
	t       *testing.T
}

// startGateway 以 fresh 隔离环境组装并启动 juhe-ai-gateway。SQLite 模式先
// 执行 maintenance ensure+seed（与部署 runbook 一致）；gateway 启动时的
// SQLite preflight 会幂等地再跑一遍六库 ensure+seed（Node db-service 语义）。
func startGateway(t *testing.T, opts gatewayEnvOptions) *gatewayFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &gatewayFixture{root: root, t: t}

	mainPort := freePort(t)
	healthPort := freePort(t)
	f4Port := freePort(t)
	f3Port := freePort(t)
	fixture.baseURL = fmt.Sprintf("http://127.0.0.1:%d", mainPort)
	fixture.healthURL = fmt.Sprintf("http://127.0.0.1:%d", healthPort)
	secret := randomHex(t, 16)
	fixture.secret = secret
	ownerEpoch := "acceptance-epoch-" + randomHex(t, 4)

	pg := opts.PGDSN != ""
	sixPaths := map[string]string{
		"JUHE_AI_DATABASE_PATH":               filepath.Join(root, "storage", "business.sqlite3"),
		"JUHE_AI_STATS_DATABASE_PATH":         filepath.Join(root, "storage", "stats.sqlite3"),
		"JUHE_AI_CHAT_DATABASE_PATH":          filepath.Join(root, "storage", "chat.sqlite3"),
		"JUHE_AI_DATASET_DATABASE_PATH":       filepath.Join(root, "storage", "dataset.sqlite3"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH": filepath.Join(root, "storage", "usage-catalog.sqlite3"),
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH": filepath.Join(root, "storage", "table-monitor.sqlite3"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH":   filepath.Join(root, "storage", "runtime-log.sqlite3"),
	}
	codexRoot := filepath.Join(root, "storage", "codex-context")
	usageShardRoot := filepath.Join(root, "storage", "usage-shards")
	mustMkdirAll(t, codexRoot, usageShardRoot,
		filepath.Join(root, "storage", "audit-blobs"),
		filepath.Join(root, "chat-assets"),
		filepath.Join(root, "openai-files"),
		filepath.Join(root, "logs"),
	)
	mustTouchFile(t, filepath.Join(root, "storage", "audit-business-settings.sqlite3"))
	mustTouchFile(t, filepath.Join(root, "storage", "oplog-business-settings.sqlite3"))
	// X04 logreads: the runtime-log file is F1-jobs-owned (the gateway
	// only reads it); fresh boots provision the minimal dataset schema
	// like a deployed F1 indexer would (helper in log_faces_test.go).
	provisionRuntimeLogDataset(t, sixPaths["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"])

	spoolDir := filepath.Join(root, "storage", "usage-record-spool")
	fixture.spoolDir = spoolDir
	fixture.assetsRoot = filepath.Join(root, "chat-assets")

	env := map[string]string{
		"JUHE_AI_HOST":                                 "127.0.0.1",
		"JUHE_AI_PORT":                                 fmt.Sprint(mainPort),
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS":        fmt.Sprintf("127.0.0.1:%d", healthPort),
		"JUHE_AI_RUNTIME_MODE":                         "standalone",
		"JUHE_AI_SECRET":                               secret,
		"JUHE_AI_BUSINESS_OWNER":                       "gateway",
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED":           "true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED":         "true",
		"JUHE_AI_BUSINESS_SCHEMA_READY":                "true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH":                 ownerEpoch,
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH":       writeCutoverEvidence(t, root, ownerEpoch),
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED":           "true",
		"JUHE_AI_AUTH_CAPTCHA_DISABLED":                "true",
		"JUHE_AI_AUDIT_LOG_STORE":                      "sqlite",
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID":                "acceptance-gateway",
		"JUHE_AI_AUDIT_LOG_DATABASE_PATH":              filepath.Join(root, "storage", "audit.sqlite3"),
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY":             filepath.Join(root, "storage", "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH":     filepath.Join(root, "storage", "audit-business-settings.sqlite3"),
		"JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS":       fmt.Sprintf("127.0.0.1:%d", f3Port),
		"JUHE_AI_AUDIT_LOG_INPUT_SECRET":               randomHex(t, 16),
		"JUHE_AI_OPERATION_LOG_STORE":                  "sqlite",
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID":            "acceptance-gateway",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH":          filepath.Join(root, "storage", "operation-log.sqlite3"),
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH": filepath.Join(root, "storage", "oplog-business-settings.sqlite3"),
		"JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS":   fmt.Sprintf("127.0.0.1:%d", f4Port),
		"JUHE_AI_OPERATION_LOG_INPUT_SECRET":           randomHex(t, 16),
		"JUHE_AI_USAGE_SHARD_ROOT":                     usageShardRoot,
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT":       codexRoot,
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT":      "1",
		"JUHE_AI_CHAT_ASSETS_ROOT":                     fixture.assetsRoot,
		"JUHE_AI_OPENAI_COMPATIBLE_FILES_ROOT":         filepath.Join(root, "openai-files"),
		"JUHE_AI_USAGE_SPOOL_DIRECTORY":                spoolDir,
	}
	for key, value := range sixPaths {
		env[key] = value
	}
	// business owner 门禁要求显式的 Business 数据库路径（sqlite 模式，
	// runtime.go businessOwnerGate）。
	env["JUHE_AI_BUSINESS_DATABASE_PATH"] = sixPaths["JUHE_AI_DATABASE_PATH"]
	// 验收场景把 AI 账户指向本机 mock 上游（gateway_chain / chat_flow），
	// 属于 accounts upstream_base_url.go 明确允许的临时回归通道。
	env["JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS"] = "true"
	fixture.storage = map[string]string{
		"business":      sixPaths["JUHE_AI_DATABASE_PATH"],
		"stats":         sixPaths["JUHE_AI_STATS_DATABASE_PATH"],
		"chat":          sixPaths["JUHE_AI_CHAT_DATABASE_PATH"],
		"dataset":       sixPaths["JUHE_AI_DATASET_DATABASE_PATH"],
		"usage-catalog": sixPaths["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"],
		"runtime-log":   sixPaths["JUHE_AI_RUNTIME_LOG_DATABASE_PATH"],
		"codex-root":    codexRoot,
		"usage-shards":  usageShardRoot,
		"logs":          filepath.Join(root, "logs"),
	}
	if opts.ChainEnabled {
		env["JUHE_AI_GATEWAY_CHAIN_ENABLED"] = "true"
	}
	if opts.OIDC {
		env["JUHE_AI_OIDC_ENABLED"] = "true"
		env["JUHE_AI_OIDC_ISSUER"] = fixture.baseURL
		env["JUHE_AI_OIDC_KEY_ENCRYPTION_SECRET"] = randomHex(t, 16)
	}
	if pg {
		env["JUHE_AI_DATABASE_DRIVER"] = "postgres"
		env["JUHE_AI_POSTGRES_URL"] = opts.PGDSN
		env["JUHE_AI_BUSINESS_POSTGRES_URL"] = opts.PGDSN
		env["JUHE_AI_AUDIT_LOG_STORE"] = "postgres"
		env["JUHE_AI_AUDIT_LOG_POSTGRES_URL"] = opts.PGDSN
		env["JUHE_AI_OPERATION_LOG_STORE"] = "postgres"
		env["JUHE_AI_OPERATION_LOG_POSTGRES_URL"] = opts.PGDSN
		// PG 模式没有启动期 ensure：schema/seed 由外部 maintenance 执行。
		runMaintenanceEnsureSeed(t, "postgres", "", opts.PGDSN, secret)
	} else {
		env["JUHE_AI_DATABASE_DRIVER"] = "sqlite"
		pathsValue := fmt.Sprintf(
			"business=%s,chat=%s,dataset=%s,usage-catalog=%s,stats=%s,codex-context-shard-root=%s,codex-context-shard-count=1",
			sixPaths["JUHE_AI_DATABASE_PATH"], sixPaths["JUHE_AI_CHAT_DATABASE_PATH"], sixPaths["JUHE_AI_DATASET_DATABASE_PATH"],
			sixPaths["JUHE_AI_USAGE_CATALOG_DATABASE_PATH"], sixPaths["JUHE_AI_STATS_DATABASE_PATH"], codexRoot,
		)
		runMaintenanceEnsureSeed(t, "sqlite", pathsValue, "", secret)
		provisionOperationLogBusinessSettingsMirror(t,
			filepath.Join(root, "storage", "oplog-business-settings.sqlite3"),
			sixPaths["JUHE_AI_DATABASE_PATH"])
	}

	fixture.process = startProcess(t, "gateway", gatewayBinary, envMapToSlice(env))
	waitForSystemAPIReady(t, fixture)
	fixture.admin = fixture.loginClient(t, "admin", acceptanceAdminPassword)
	return fixture
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
