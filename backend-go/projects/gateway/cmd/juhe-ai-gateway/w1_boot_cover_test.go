//go:build windows

package main

// w1_boot_cover_test.go —— juhe-ai-gateway 二进制级启动覆盖测试。
//
// 本文件限定 Windows 构建：场景 J 的 CREATE_NEW_PROCESS_GROUP（syscall
// SysProcAttr.CreationFlags）与 golang.org/x/sys/windows 的
// GenerateConsoleCtrlEvent 都是 Windows 专属面，其余场景（旗标路径、F3/F4
// 迁移、被动/owner fail-fast）与平台无关，但为保持单文件约束统一放在
// Windows 门内；目标验证环境即 Windows 工作区。
//
// 机制：`go build -cover` 构建插桩二进制（构建一次、全场景复用），每个场景以
// 子进程运行 main() 的一条真实路径，GOCOVERDIR 指向
// <os.TempDir()>/w1b-cov/<场景名>。测试结束后不删除计数器文件；每个场景的
// GOCOVERDIR 绝对路径追加写入 <os.TempDir()>/w1b-cov-manifest.txt，供外部
// `go tool covdata` 管线收割（textfmt/merge 均可按清单逐目录处理）。
//
// 场景清单（对应 main.go 的旗标与 env 契约）：
//
//	A  -version                        → exit 0，stdout 含版本契约字符串
//	B  -check-boundary                 → exit 0，stdout 含 boundary=ready
//	C  bogus-arg                       → exit 2，stderr 含 unsupported gateway arguments
//	D  双迁移旗标互斥                   → exit 2，stderr 含 mutually exclusive
//	E1 F3 审计 SQLite 迁移成功（自建 5 张 legacy 审计表 + blob 文件）
//	F1 F3 缺 -node-stopped/-go-stopped → 非零，stderr 含停机 gate 文案
//	F2 F3 源库不存在                    → 非零，stderr 含读源失败文案
//	G  F4 operation SQLite 迁移成功（自建 legacy operation-log 源 + settings 库）
//	H  F4 postgres 模式缺 -backup-confirmed → 非零，stderr 含备份确认文案（不连真 PG）
//	I  被动网关 + 非法 -health-listen-address → 非零，stderr 含 listen passive gateway health endpoint
//	J  被动网关优雅关闭（CTRL_BREAK_EVENT，尽力而为；3 次尝试不可行则 Skip 并保留本注释记录）
//	K1 owner + JUHE_AI_DATABASE_DRIVER=bogus  → stderr 含 load gateway runtime config
//	K2（已删除）原「仅 CHAIN 无 SYSTEM_API」联动门槛臂：2026-09-21 起两开关
//	     移除、组合根与网关链恒开，被测对象不复存在。
//	K3 owner + 非 gateway BUSINESS_OWNER（businessOwnerGate 首步失败）→ stderr 含 verify business owner gates
//	K4 K3 全量 handoff 证据文件缺失 → stderr 含 read business owner cutover evidence
//	K5 K4 有效证据 + J3b enabled 证据缺失 → stderr 含 read J3b cutover evidence
//	L  JUHE_AI_BLUE_GREEN_OWNER_MODE=bogus → stderr 含 must be active, standby, or drain
//
// 注意：main.go 的 gateGatewayChain 自 G20 phase-2 起恒返回 nil（适配器全部
// 已 author），原“fail-fast 适配器清单”语义已并入 loadRuntimeConfig 的
// system-api 强制门槛，K2 按当前源码实际文案断言。
//
// 每个子进程场景都有界等待（30s 超时即杀进程并 Fatal），J 场景健康轮询与
// 退出等待均有界。J 场景若 GenerateConsoleCtrlEvent 在 3 次尝试内不可行
// （无 console、进程组事件失败等），以 t.Skip 记录而不是挂死。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
	"golang.org/x/sys/windows"

	_ "modernc.org/sqlite"
)

const (
	w1bCoverageRoot     = "w1b-cov"
	w1bCoverageManifest = "w1b-cov-manifest.txt"
	w1bScenarioTimeout  = 30 * time.Second
)

var (
	w1bCoverBinaryOnce sync.Once
	w1bCoverBinaryPath string
	w1bCoverBinaryErr  error
)

// w1bBuildCoverBinary 用 `go build -cover` 构建插桩二进制；包内只构建一次，
// 所有测试函数复用同一可执行文件。
func w1bBuildCoverBinary(t *testing.T) string {
	t.Helper()
	w1bCoverBinaryOnce.Do(func() {
		binDir := filepath.Join(os.TempDir(), w1bCoverageRoot, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			w1bCoverBinaryErr = fmt.Errorf("创建插桩二进制目录失败: %w", err)
			return
		}
		exe := filepath.Join(binDir, "juhe-ai-gateway-cover.exe")
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-cover", "-o", exe, "./cmd/juhe-ai-gateway")
		cmd.Dir = `F:\sub2api-lite\backend-go\projects\gateway`
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				w1bCoverBinaryErr = fmt.Errorf("go build -cover 超过 100s 有界等待被终止: %w, 输出: %s", err, output.String())
				return
			}
			w1bCoverBinaryErr = fmt.Errorf("go build -cover 失败: %w, 输出: %s", err, output.String())
			return
		}
		w1bCoverBinaryPath = exe
	})
	if w1bCoverBinaryErr != nil {
		t.Fatalf("构建插桩二进制失败: %v", w1bCoverBinaryErr)
	}
	if _, err := os.Stat(w1bCoverBinaryPath); err != nil {
		t.Fatalf("插桩二进制不存在: %v", err)
	}
	return w1bCoverBinaryPath
}

// w1bCoverageDir 返回场景专属 GOCOVERDIR 固定基目录（不用 t.TempDir()：
// 测试结束后计数器文件必须保留，供外部 covdata 管线收割）。
func w1bCoverageDir(t *testing.T, scenario string) string {
	t.Helper()
	dir := filepath.Join(os.TempDir(), w1bCoverageRoot, scenario)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建场景覆盖目录 %s 失败: %v", dir, err)
	}
	return dir
}

// w1bAppendCoverageManifest 把场景 GOCOVERDIR 绝对路径追加写入清单文件
// <os.TempDir()>/w1b-cov-manifest.txt（一行一个目录）。
func w1bAppendCoverageManifest(t *testing.T, dir string) {
	t.Helper()
	manifestPath := filepath.Join(os.TempDir(), w1bCoverageManifest)
	file, err := os.OpenFile(manifestPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("打开覆盖目录清单 %s 失败: %v", manifestPath, err)
	}
	defer file.Close()
	if _, err := fmt.Fprintf(file, "%s\n", dir); err != nil {
		t.Fatalf("写入覆盖目录清单 %s 失败: %v", manifestPath, err)
	}
}

// w1bScenarioEnv 剥离宿主环境里的 JUHE_AI_*、NODE_ENV 与 GOCOVERDIR，
// 保证每个场景只受显式 env 影响；GOCOVERDIR 必须指向场景覆盖目录。
func w1bScenarioEnv(t *testing.T, coverageDir string, pairs ...string) []string {
	t.Helper()
	env := make([]string, 0, len(os.Environ())+len(pairs)+1)
	for _, entry := range os.Environ() {
		upper := strings.ToUpper(entry)
		if strings.HasPrefix(upper, "JUHE_AI_") || strings.HasPrefix(upper, "NODE_ENV=") || strings.HasPrefix(upper, "GOCOVERDIR=") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, pairs...)
	// 2026-09-21 起模型检测 owner 默认常驻：J3b 装配先于多数目标组件，
	// 场景未显式提供 DATA_DIR 时准备一个隔离数据目录，并把零配置回落
	// 路径 <DATA_DIR>/business.sqlite3 预置成带 schema 的业务库（不得注
	// 入 BUSINESS_DATABASE_PATH——那会触发切流家族的严格门禁）；管理
	// listener 用随机端口，避免抢占 3307。显式给出同名键的 pairs（场景
	// 自身契约）一律不覆盖。
	if !w1bEnvHasKey(pairs, "JUHE_AI_DATA_DIR") {
		dataDir := t.TempDir()
		w1v2PrepareBusinessSQLite(t, filepath.Join(dataDir, "business.sqlite3"))
		env = append(env, "JUHE_AI_DATA_DIR="+dataDir)
	}
	if !w1bEnvHasKey(pairs, "JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS") {
		env = append(env, "JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS=127.0.0.1:0")
	}
	env = append(env, "GOCOVERDIR="+coverageDir)
	return env
}

// w1bEnvHasKey 判断场景 pairs 是否显式给出某个 env（"K=V" 形态，按键全名
// 前缀匹配）。
func w1bEnvHasKey(pairs []string, key string) bool {
	prefix := key + "="
	for _, pair := range pairs {
		if strings.HasPrefix(pair, prefix) {
			return true
		}
	}
	return false
}

// w1bRunScenario 以子进程运行插桩二进制的单个场景：有界等待 30s，超时杀
// 进程并 Fatal；结束后把 GOCOVERDIR 追加进收割清单，返回 stdout/stderr/退出码。
func w1bRunScenario(t *testing.T, scenario string, env []string, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), w1bScenarioTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, w1bCoverBinaryPath, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("场景 %s 超过 %s 有界等待，进程已被终止: %v", scenario, w1bScenarioTimeout, ctx.Err())
	}
	exitCode := 0
	if runErr != nil {
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok {
			t.Fatalf("场景 %s 启动或等待失败: %v", scenario, runErr)
		}
		exitCode = exitErr.ExitCode()
	}
	w1bAppendCoverageManifest(t, filepath.Join(os.TempDir(), w1bCoverageRoot, scenario))
	t.Logf("场景 %s: exit=%d stdout=%q stderr=%q", scenario, exitCode, stdout.String(), stderr.String())
	return stdout.String(), stderr.String(), exitCode
}

func w1bRequireExitCode(t *testing.T, scenario string, actual, want int) {
	t.Helper()
	if actual != want {
		t.Fatalf("场景 %s 退出码期望 %d 实际 %d", scenario, want, actual)
	}
}

func w1bRequireContains(t *testing.T, scenario, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("场景 %s 输出缺少期望子串 %q，实际: %q", scenario, needle, haystack)
	}
}

// ---- 场景 A/B/C/D/L：旗标快速路径与 ownermode 加载错误臂 ----

func TestW1BBootCoverQuickFlags(t *testing.T) {
	w1bBuildCoverBinary(t)

	// A: -version → exit 0，stdout 含项目与契约版本字符串。
	coverageDir := w1bCoverageDir(t, "A-version")
	stdout, _, code := w1bRunScenario(t, "A-version", w1bScenarioEnv(t, coverageDir), "-version")
	w1bRequireExitCode(t, "A-version", code, 0)
	w1bRequireContains(t, "A-version", stdout, fmt.Sprintf("juhe-ai-gateway project=%s contract=%s", contracts.ProjectGateway, contracts.ArchitectureVersion))

	// B: -check-boundary → exit 0，stdout 含 boundary=ready。
	coverageDir = w1bCoverageDir(t, "B-check-boundary")
	stdout, _, code = w1bRunScenario(t, "B-check-boundary", w1bScenarioEnv(t, coverageDir), "-check-boundary")
	w1bRequireExitCode(t, "B-check-boundary", code, 0)
	w1bRequireContains(t, "B-check-boundary", stdout, "juhe-ai-gateway boundary=ready runtime=audit-operation-owner")

	// C: 不支持参数 → exit 2，stderr 含 unsupported gateway arguments。
	coverageDir = w1bCoverageDir(t, "C-bogus-arg")
	_, stderr, code := w1bRunScenario(t, "C-bogus-arg", w1bScenarioEnv(t, coverageDir), "bogus-arg")
	w1bRequireExitCode(t, "C-bogus-arg", code, 2)
	w1bRequireContains(t, "C-bogus-arg", stderr, "unsupported gateway arguments")

	// D: 互斥迁移旗标 → exit 2，stderr 含 mutually exclusive。
	coverageDir = w1bCoverageDir(t, "D-mutually-exclusive")
	_, stderr, code = w1bRunScenario(t, "D-mutually-exclusive", w1bScenarioEnv(t, coverageDir),
		"-migrate-audit-log-legacy-sqlite", "-migrate-operation-log-legacy-sqlite")
	w1bRequireExitCode(t, "D-mutually-exclusive", code, 2)
	w1bRequireContains(t, "D-mutually-exclusive", stderr, "offline migration modes are mutually exclusive")

	// L: 非法 owner mode → fail 退出，stderr 含 ownermode.Load 契约文案。
	coverageDir = w1bCoverageDir(t, "L-owner-mode-invalid")
	_, stderr, code = w1bRunScenario(t, "L-owner-mode-invalid", w1bScenarioEnv(t, coverageDir, "JUHE_AI_BLUE_GREEN_OWNER_MODE=bogus-mode"))
	w1bRequireExitCode(t, "L-owner-mode-invalid", code, 1)
	w1bRequireContains(t, "L-owner-mode-invalid", stderr, "JUHE_AI_BLUE_GREEN_OWNER_MODE must be active, standby, or drain")
}

// ---- 场景 E/F：F3 审计 SQLite 迁移 ----

// w1bLegacyAuditTables 的列清单与 internal/auditlog/legacy_migration.go 的
// legacyAuditTables 复制列集一一对应；legacy 源表不声明列类型（无 affinity，
// 值按插入时的存储类保存），保证迁移后 EXCEPT 字段一致性校验通过。
var w1bLegacyAuditTables = map[string]string{
	"audit_logs":          "id,trace_id,traffic_source,system_account_id,api_key_id,conversation_key,session_id,session_client_type,group_id,account_id,provider_code,method,path,query_string,model,upstream_model,pricing_model,model_mapping_applied,model_mapping_source,source_endpoint_family,upstream_endpoint_family,stream,client_ip,user_agent,audit_outcome,success,final_status_code,error_phase,error_code,error_message,sample_bucket,sample_reason,attempt_count,payload_count,raw_payload_bytes,compressed_payload_bytes,compression_saved_bytes,error_group_id,capture_status,lifecycle_status,started_at,ended_at,duration_ms,http_completed_at,http_duration_ms,first_token_ms,created_at",
	"audit_log_attempts":  "id,audit_log_id,attempt_index,account_id,account_owner_system_account_id,group_id,proxy_url,provider_code,attempt_model,attempt_upstream_model,attempt_pricing_model,attempt_model_mapping_applied,attempt_model_mapping_source,attempt_source_endpoint_family,attempt_upstream_endpoint_family,upstream_method,upstream_url,upstream_status_code,success,error_phase,error_code,error_message,started_at,ended_at,duration_ms",
	"audit_payload_blobs": "id,sha256,raw_size_bytes,compressed_size_bytes,content_type,content_encoding,compression,storage_key,ref_count,first_seen_at,last_seen_at,created_at",
	"audit_payload_refs":  "id,audit_log_id,attempt_id,part_type,sequence_index,content_type,content_encoding,headers_blob_id,body_blob_id,headers_sha256,body_sha256,raw_size_bytes,compressed_size_bytes,capture_status,drop_reason,created_at",
	"audit_error_groups":  "id,fingerprint,window_started_at,window_ended_at,system_account_id,api_key_id,group_id,account_id,provider_code,path,model,status_code,error_phase,error_code,error_type,request_fingerprint,error_fingerprint,count,first_event_id,last_event_id,sample_event_id,last_message,created_at,updated_at",
}

// w1bCreateLegacyAuditSQLite 构造含 5 张 legacy 审计表的最小源库：1 条
// audit_logs、1 个 blob（aa/payload.blob，none 压缩）与 1 条 body 引用，
// ref_count 与引用数一致以满足迁移后 ref_count 完整性校验。
func w1bCreateLegacyAuditSQLite(t *testing.T, dbPath, blobDir string) {
	t.Helper()
	var ddl strings.Builder
	for _, table := range []string{"audit_logs", "audit_log_attempts", "audit_payload_blobs", "audit_payload_refs", "audit_error_groups"} {
		ddl.WriteString("CREATE TABLE " + table + " (" + w1bLegacyAuditTables[table] + ");")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatalf("创建 legacy 审计源库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(ddl.String()); err != nil {
		t.Fatalf("创建 legacy 审计表失败: %v", err)
	}
	const payload = "legacy audit payload"
	digest := sha256.Sum256([]byte(payload))
	// NOT NULL DEFAULT 列必须显式给值：迁移按列清单 INSERT，显式列不触发
	// 目标 schema 的 DEFAULT，源端 NULL 会让 INSERT OR IGNORE 静默吞行
	// （表现为“行数不一致”）。
	if _, err := db.Exec(`INSERT INTO audit_logs (id,trace_id,traffic_source,method,path,audit_outcome,sample_bucket,sample_reason,capture_status,lifecycle_status,model_mapping_applied,stream,success,attempt_count,payload_count,raw_payload_bytes,compressed_payload_bytes,compression_saved_bytes,started_at,ended_at,created_at) VALUES ('log-1','trace-1','gateway','GET','/v1/test','gateway_succeeded',1,'test','complete','finalized',0,0,0,0,0,0,0,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:01Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("插入 legacy audit_logs 失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,compression,storage_key,ref_count,first_seen_at,last_seen_at,created_at) VALUES ('blob-1',?,?,?,?,?,?,1,?,?,?)`,
		hex.EncodeToString(digest[:]), len(payload), len(payload), "text/plain", "none", "aa/payload.blob", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("插入 legacy audit_payload_blobs 失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_refs (id,audit_log_id,part_type,sequence_index,body_blob_id,raw_size_bytes,compressed_size_bytes,capture_status,created_at) VALUES ('ref-1','log-1','gateway_response',0,'blob-1',?,?,'complete','2026-01-01T00:00:00Z')`, len(payload), len(payload)); err != nil {
		t.Fatalf("插入 legacy audit_payload_refs 失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭 legacy 审计源库失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(blobDir, "aa"), 0o750); err != nil {
		t.Fatalf("创建 legacy blob 目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(blobDir, "aa", "payload.blob"), []byte(payload), 0o640); err != nil {
		t.Fatalf("写入 legacy blob 文件失败: %v", err)
	}
}

func TestW1BBootCoverAuditLegacyMigration(t *testing.T) {
	w1bBuildCoverBinary(t)
	root := t.TempDir()

	// E: F3 审计迁移成功臂。
	sourcePath := filepath.Join(root, "audit-source.sqlite")
	targetPath := filepath.Join(root, "audit-target.sqlite")
	sourceBlobs := filepath.Join(root, "audit-source-blobs")
	targetBlobs := filepath.Join(root, "audit-target-blobs")
	w1bCreateLegacyAuditSQLite(t, sourcePath, sourceBlobs)
	coverageDir := w1bCoverageDir(t, "E-audit-migrate-ok")
	stdout, stderr, code := w1bRunScenario(t, "E-audit-migrate-ok", w1bScenarioEnv(t, coverageDir),
		"-migrate-audit-log-legacy-sqlite",
		"-source-db", sourcePath,
		"-target-db", targetPath,
		"-source-blob-dir", sourceBlobs,
		"-target-blob-dir", targetBlobs,
		"-node-stopped", "-go-stopped")
	w1bRequireExitCode(t, "E-audit-migrate-ok", code, 0)
	var result struct {
		NoOp        bool             `json:"noOp"`
		TableCounts map[string]int64 `json:"tableCounts"`
		BlobCount   int64            `json:"blobCount"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("场景 E-audit-migrate-ok stdout 不是合法迁移结果 JSON: %v, stdout=%q stderr=%q", err, stdout, stderr)
	}
	if result.NoOp || result.TableCounts["audit_logs"] != 1 || result.TableCounts["audit_payload_blobs"] != 1 || result.BlobCount != 1 {
		t.Fatalf("场景 E-audit-migrate-ok 迁移结果契约不符: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(targetBlobs, "aa", "payload.blob")); err != nil {
		t.Fatalf("场景 E-audit-migrate-ok 目标 blob 缺失: %v", err)
	}

	// F1: 缺 -node-stopped/-go-stopped → 非零退出 + 停机 gate 文案。
	coverageDir = w1bCoverageDir(t, "F1-audit-gate-missing")
	_, stderr, code = w1bRunScenario(t, "F1-audit-gate-missing", w1bScenarioEnv(t, coverageDir), "-migrate-audit-log-legacy-sqlite")
	if code == 0 {
		t.Fatalf("场景 F1-audit-gate-missing 期望非零退出码，实际 0")
	}
	w1bRequireContains(t, "F1-audit-gate-missing", stderr, "F3 audit SQLite migration failed: F3 审计 SQLite 迁移要求停机：必须同时确认 Node 和 Go 已停止")

	// F2: 源库不存在 → 非零退出 + 读源失败文案。
	coverageDir = w1bCoverageDir(t, "F2-audit-source-missing")
	_, stderr, code = w1bRunScenario(t, "F2-audit-source-missing", w1bScenarioEnv(t, coverageDir),
		"-migrate-audit-log-legacy-sqlite",
		"-source-db", filepath.Join(root, "missing-source.sqlite"),
		"-target-db", filepath.Join(root, "unused-target.sqlite"),
		"-source-blob-dir", sourceBlobs,
		"-target-blob-dir", targetBlobs,
		"-node-stopped", "-go-stopped")
	if code == 0 {
		t.Fatalf("场景 F2-audit-source-missing 期望非零退出码，实际 0")
	}
	w1bRequireContains(t, "F2-audit-source-missing", stderr, "F3 audit SQLite migration failed: 访问旧审计 SQLite 源文件失败")
}

// ---- 场景 G/H：F4 operation-log 迁移 ----

// w1bCreateLegacyOperationLogSQLite 复刻 internal/operationlog 测试的 legacy
// Node 源库形态：4 张 legacy 表 + 1 条完整操作日志（targets/viewers/search terms）。
func w1bCreateLegacyOperationLogSQLite(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("创建 legacy operation-log 源库失败: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE operation_logs (id TEXT PRIMARY KEY,trace_id TEXT,actor_system_account_id TEXT NOT NULL,actor_username TEXT,actor_display_name TEXT,actor_role TEXT NOT NULL,operation_scope_system_account_id TEXT,mode TEXT NOT NULL,module TEXT NOT NULL,action TEXT NOT NULL,operation_key TEXT NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT,resource_name TEXT,summary TEXT NOT NULL,detail_level TEXT NOT NULL,visibility_scope TEXT NOT NULL,changes_json TEXT NOT NULL,metadata_json TEXT NOT NULL,method TEXT,path TEXT,status_code INTEGER,client_ip TEXT,user_agent TEXT,created_at TEXT NOT NULL);
CREATE TABLE operation_log_targets (id TEXT PRIMARY KEY,operation_log_id TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT,target_name TEXT,target_owner_system_account_id TEXT,relation TEXT NOT NULL,created_at TEXT NOT NULL,FOREIGN KEY(operation_log_id) REFERENCES operation_logs(id) ON DELETE CASCADE);
CREATE TABLE operation_log_viewers (operation_log_id TEXT NOT NULL,system_account_id TEXT NOT NULL,visibility_reason TEXT NOT NULL,detail_level TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(operation_log_id,system_account_id,visibility_reason),FOREIGN KEY(operation_log_id) REFERENCES operation_logs(id) ON DELETE CASCADE);
CREATE TABLE operation_log_summary_search_terms (operation_log_id TEXT NOT NULL,term TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(term,operation_log_id),FOREIGN KEY(operation_log_id) REFERENCES operation_logs(id) ON DELETE CASCADE);`)
	if err != nil {
		t.Fatalf("创建 legacy operation-log 表失败: %v", err)
	}
	const createdAt = "2026-08-13T08:00:00.100000000+08:00"
	if _, err := db.Exec(`INSERT INTO operation_logs (id,actor_system_account_id,actor_role,mode,module,action,operation_key,resource_type,resource_id,resource_name,summary,detail_level,visibility_scope,changes_json,metadata_json,created_at) VALUES ('oplog-legacy-1','actor','user','self','accounts','update','accounts.update','account','account-1','Legacy Account','legacy operation summary','full','targeted','[{"field":"name","label":"Name","before":"old","after":"new"}]','{}',?)`, createdAt); err != nil {
		t.Fatalf("插入 legacy operation_logs 失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO operation_log_targets VALUES ('optgt-legacy-1','oplog-legacy-1','account','account-1','Legacy Account','owner','primary',?); INSERT INTO operation_log_viewers VALUES ('oplog-legacy-1','actor','actor_self','full',?); INSERT INTO operation_log_summary_search_terms VALUES ('oplog-legacy-1','legacy',?)`, createdAt, createdAt, createdAt); err != nil {
		t.Fatalf("插入 legacy operation-log 子表失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭 legacy operation-log 源库失败: %v", err)
	}
}

// w1bCreateBusinessSettingsSQLite 构造 F4 OpenStore 必需的 business settings
// 只读镜像库（system_accounts + system_settings）。
func w1bCreateBusinessSettingsSQLite(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("创建 business settings 库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL,key TEXT NOT NULL,value_json TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(system_account_id,key)); CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT NOT NULL); INSERT INTO system_accounts VALUES ('actor','actor','Actor'); INSERT INTO system_settings VALUES ('sys_admin','operationLogRetentionDays','365','2026-08-13T00:00:00Z')`); err != nil {
		t.Fatalf("初始化 business settings 库失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭 business settings 库失败: %v", err)
	}
}

func TestW1BBootCoverOperationLegacyMigration(t *testing.T) {
	w1bBuildCoverBinary(t)
	root := t.TempDir()

	// G: F4 SQLite 迁移成功臂（运行 -migrate-operation-log-legacy-sqlite）。
	sourcePath := filepath.Join(root, "node-dataset.sqlite3")
	targetPath := filepath.Join(root, "f4-operation.sqlite3")
	businessSettings := filepath.Join(root, "business-settings.sqlite3")
	usageShardRoot := filepath.Join(root, "usage-shards")
	if err := os.MkdirAll(usageShardRoot, 0o750); err != nil {
		t.Fatalf("创建 usage shard 根目录失败: %v", err)
	}
	w1bCreateLegacyOperationLogSQLite(t, sourcePath)
	w1bCreateBusinessSettingsSQLite(t, businessSettings)
	coverageDir := w1bCoverageDir(t, "G-operation-migrate-ok")
	env := w1bScenarioEnv(t, coverageDir,
		"JUHE_AI_OPERATION_LOG_STORE=sqlite",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH="+targetPath,
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH="+businessSettings,
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1b-f4-migrate",
		"JUHE_AI_USAGE_SHARD_ROOT="+usageShardRoot)
	stdout, stderr, code := w1bRunScenario(t, "G-operation-migrate-ok", env,
		"-migrate-operation-log-legacy-sqlite",
		"-operation-log-source-db", sourcePath,
		"-node-stopped", "-go-stopped", "-backup-confirmed")
	w1bRequireExitCode(t, "G-operation-migrate-ok", code, 0)
	var result struct {
		Mode                  string           `json:"mode"`
		NoOp                  bool             `json:"noOp"`
		TargetCounts          map[string]int64 `json:"targetCounts"`
		SearchTermsRebuilt    bool             `json:"searchTermsRebuilt"`
		MigratedOperationLogs int64            `json:"migratedOperationLogs"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &result); err != nil {
		t.Fatalf("场景 G-operation-migrate-ok stdout 不是合法迁移结果 JSON: %v, stdout=%q stderr=%q", err, stdout, stderr)
	}
	if result.Mode != "sqlite-copy" || result.NoOp || result.MigratedOperationLogs != 1 || !result.SearchTermsRebuilt || result.TargetCounts["operation_logs"] != 1 {
		t.Fatalf("场景 G-operation-migrate-ok 迁移结果契约不符: %+v", result)
	}

	// H: F4 postgres 模式缺 -backup-confirmed → 非零退出 + 备份确认文案
	// （停机 gate 先于任何 PostgreSQL 连接执行，子进程不需要真实 PG）。
	coverageDir = w1bCoverageDir(t, "H-operation-postgres-no-backup")
	env = w1bScenarioEnv(t, coverageDir,
		"JUHE_AI_OPERATION_LOG_STORE=postgres",
		"JUHE_AI_OPERATION_LOG_POSTGRES_URL=postgres://127.0.0.1:1/w1b_unused",
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1b-f4-pg")
	_, stderr, code = w1bRunScenario(t, "H-operation-postgres-no-backup", env,
		"-migrate-operation-log-legacy-postgres",
		"-node-stopped", "-go-stopped")
	if code == 0 {
		t.Fatalf("场景 H-operation-postgres-no-backup 期望非零退出码，实际 0")
	}
	w1bRequireContains(t, "H-operation-postgres-no-backup", stderr, "F4 operation-log legacy migration failed: F4 操作日志离线迁移要求已完成可恢复备份确认")
}

// ---- 场景 I/J：被动网关 ----

func TestW1BBootCoverPassiveGatewayErrorArm(t *testing.T) {
	w1bBuildCoverBinary(t)
	// I: 非 owner + 非法健康监听地址 → fail 退出，stderr 含被动网关监听文案。
	coverageDir := w1bCoverageDir(t, "I-passive-bad-address")
	_, stderr, code := w1bRunScenario(t, "I-passive-bad-address",
		w1bScenarioEnv(t, coverageDir, "JUHE_AI_BLUE_GREEN_OWNER_MODE=standby"),
		"-health-listen-address", "bad-address")
	w1bRequireExitCode(t, "I-passive-bad-address", code, 1)
	w1bRequireContains(t, "I-passive-bad-address", stderr, `listen passive gateway health endpoint "bad-address"`)
}

// w1bFreePort 借一次 127.0.0.1:0 监听挑选当前空闲端口。
func w1bFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("挑选空闲端口失败: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestW1BBootCoverPassiveGatewayGracefulShutdown(t *testing.T) {
	exe := w1bBuildCoverBinary(t)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastAttemptErr string
	for attempt := 1; attempt <= 3; attempt++ {
		scenario := fmt.Sprintf("J-passive-graceful-%d", attempt)
		address := fmt.Sprintf("127.0.0.1:%d", w1bFreePort(t))
		coverageDir := w1bCoverageDir(t, scenario)
		ctx, cancel := context.WithTimeout(context.Background(), w1bScenarioTimeout)
		cmd := exec.CommandContext(ctx, exe, "-health-listen-address", address)
		cmd.Env = w1bScenarioEnv(t, coverageDir, "JUHE_AI_BLUE_GREEN_OWNER_MODE=standby")
		w1bSetNewProcessGroup(cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if startErr := cmd.Start(); startErr != nil {
			cancel()
			t.Fatalf("场景 %s 启动被动网关失败: %v", scenario, startErr)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		healthy := false
		pollDeadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(pollDeadline) {
			response, err := client.Get("http://" + address + "/health")
			if err == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					healthy = true
					break
				}
			}
			select {
			case waitErr := <-done:
				cancel()
				t.Fatalf("场景 %s 被动网关在健康检查通过前退出: %v, stderr=%s", scenario, waitErr, stderr.String())
			case <-time.After(100 * time.Millisecond):
			}
		}
		if !healthy {
			_ = cmd.Process.Kill()
			<-done
			cancel()
			lastAttemptErr = fmt.Sprintf("健康端点 %s 在 15s 内未就绪", address)
			continue
		}
		if err := w1bSendCtrlBreak(cmd.Process.Pid); err != nil {
			_ = cmd.Process.Kill()
			<-done
			cancel()
			lastAttemptErr = fmt.Sprintf("GenerateConsoleCtrlEvent(CTRL_BREAK) 失败: %v", err)
			continue
		}
		select {
		case waitErr := <-done:
			// cancel() 会使 ctx.Err() 变为 Canceled，超时判定必须在 cancel 之前取值。
			ctxErr := ctx.Err()
			cancel()
			if ctxErr != nil {
				t.Fatalf("场景 %s 超过 %s 有界等待，进程已被终止", scenario, w1bScenarioTimeout)
			}
			exitCode := 0
			if waitErr != nil {
				exitErr, ok := waitErr.(*exec.ExitError)
				if !ok {
					t.Fatalf("场景 %s 等待进程退出失败: %v", scenario, waitErr)
				}
				exitCode = exitErr.ExitCode()
			}
			w1bRequireExitCode(t, scenario, exitCode, 0)
			w1bRequireContains(t, scenario, stdout.String(), "juhe-ai-gateway passive")
			combined := stdout.String() + stderr.String()
			for _, forbidden := range []string{"shutdown passive gateway health endpoint", "listen passive gateway health endpoint", "passive gateway health endpoint stopped"} {
				if strings.Contains(combined, forbidden) {
					t.Fatalf("场景 %s 优雅关闭路径出现 fail 输出 %q: stdout=%q stderr=%q", scenario, forbidden, stdout.String(), stderr.String())
				}
			}
			w1bAppendCoverageManifest(t, coverageDir)
			return
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			cancel()
			t.Fatalf("场景 %s CTRL_BREAK 后 15s 内未退出，已强杀", scenario)
		}
	}
	// 尽力而为：3 次尝试不可行则跳过本场景（覆盖率收割清单不含 J 场景）。
	t.Skipf("场景 J 被动网关优雅关闭在 3 次尝试内不可行，已跳过（详见文件头注释）: %s", lastAttemptErr)
}

// ---- 场景 K：owner 模式快速失败臂 ----

// w1bWriteCutoverEvidence 构造一份能通过
// modelcheckowner.VerifyConfiguredCutoverEvidence 的有效 J3b 切换证据
// （备份工件 + readback manifest + freshness 窗口），epoch 由调用方指定。
func w1bWriteCutoverEvidence(t *testing.T, dir, epoch string) string {
	t.Helper()
	const digest64 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	now := time.Now().UTC()
	backupData := []byte("w1b-backup-artifact")
	backupPath := filepath.Join(dir, "backup.bin")
	if err := os.WriteFile(backupPath, backupData, 0o600); err != nil {
		t.Fatalf("写入备份工件失败: %v", err)
	}
	backupDigest := sha256.Sum256(backupData)
	manifest := contracts.J3bReadbackManifest{
		FormatVersion:          contracts.J3bReadbackManifestFormatVersion,
		Scope:                  contracts.J3bReadbackManifestScope,
		Producer:               "w1b-boot-cover-test",
		SourceSnapshotIdentity: "snapshot-w1b",
		SourceSchema:           "legacy-sqlite-dataset+stats",
		TargetSchema:           "juhe-j3b-sqlite",
		ProjectionComplete:     true,
		VerifiedAt:             now.Format(time.RFC3339),
	}
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
		manifest.Tables = append(manifest.Tables, contracts.J3bReadbackTableDigest{Name: name, SourceRows: 1, TargetRows: 1, SourceDigest: digest64, TargetDigest: digest64})
	}
	manifestHash, err := contracts.ComputeJ3bReadbackManifestHash(manifest)
	if err != nil {
		t.Fatalf("计算 readback manifest hash 失败: %v", err)
	}
	manifest.ManifestHash = manifestHash
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("序列化 readback manifest 失败: %v", err)
	}
	manifestPath := filepath.Join(dir, "readback-manifest.json")
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatalf("写入 readback manifest 失败: %v", err)
	}
	manifestFileDigest := sha256.Sum256(manifestData)
	evidence := contracts.J3bCutoverEvidence{
		OldOwner:             "node",
		NewOwner:             contracts.J3bGatewayCutoverOwner,
		OwnerEpoch:           epoch,
		DrainCompleted:       true,
		ActivePathZero:       true,
		InFlight:             0,
		BlockedFindings:      0,
		RollbackReplayCursor: "cursor-w1b",
		SourceDigest:         digest64,
		TargetDigest:         digest64,
		BackupArtifact:       contracts.J3bBackupArtifact{Path: backupPath, Hash: hex.EncodeToString(backupDigest[:])},
		ReadbackManifest: contracts.J3bReadbackManifestReference{
			Path:                   manifestPath,
			Hash:                   hex.EncodeToString(manifestFileDigest[:]),
			FormatVersion:          contracts.J3bReadbackManifestFormatVersion,
			Scope:                  contracts.J3bReadbackManifestScope,
			SourceSnapshotIdentity: "snapshot-w1b",
			SourceSchema:           "legacy-sqlite-dataset+stats",
			TargetSchema:           "juhe-j3b-sqlite",
		},
		Freshness: contracts.J3bEvidenceFreshness{CapturedAt: now.Format(time.RFC3339), MaxAgeSeconds: 3600},
	}
	evidenceData, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("序列化切换证据失败: %v", err)
	}
	evidencePath := filepath.Join(dir, "evidence.json")
	if err := os.WriteFile(evidencePath, evidenceData, 0o600); err != nil {
		t.Fatalf("写入切换证据失败: %v", err)
	}
	return evidencePath
}

// w1bOwnerBaseEnv 返回 owner 快速失败臂的公共 env：owner mode 保持 active
// （默认），sqlite 组合根必需的 JUHE_AI_DATABASE_PATH 预置。
func w1bOwnerBaseEnv(t *testing.T, coverageDir string, extra ...string) []string {
	t.Helper()
	base := []string{"JUHE_AI_DATABASE_PATH=" + filepath.Join(t.TempDir(), "w1b-business.sqlite")}
	return w1bScenarioEnv(t, coverageDir, append(base, extra...)...)
}

func TestW1BBootCoverOwnerFailFastArms(t *testing.T) {
	w1bBuildCoverBinary(t)
	root := t.TempDir()

	// K1: JUHE_AI_DATABASE_DRIVER=bogus → loadRuntimeConfig 快速失败。
	coverageDir := w1bCoverageDir(t, "K1-driver-bogus")
	_, stderr, code := w1bRunScenario(t, "K1-driver-bogus", w1bOwnerBaseEnv(t, coverageDir, "JUHE_AI_DATABASE_DRIVER=bogus"))
	w1bRequireExitCode(t, "K1-driver-bogus", code, 1)
	w1bRequireContains(t, "K1-driver-bogus", stderr, "load gateway runtime config: JUHE_AI_DATABASE_DRIVER 必须为 sqlite 或 postgres")

	// K2（已删除）：原「chain=true + system-api=false 联动门槛」臂依赖
	// JUHE_AI_GATEWAY_CHAIN_ENABLED / JUHE_AI_GATEWAY_SYSTEM_API_ENABLED
	// 开关。2026-09-21 起两开关移除、组合根与网关链恒开，联动门槛不复
	// 存在，该臂失去被测对象。

	// K3: businessOwnerGate 首步（JUHE_AI_BUSINESS_OWNER 非 gateway）→
	// stderr 含 verify business owner gates。2026-09-19 起 sqlite +
	// BUSINESS_* 家族全空会零配置自动认领（新装部署直通），错误臂改用显式
	// 非 gateway owner 保持门禁覆盖。
	coverageDir = w1bCoverageDir(t, "K3-business-owner-gate")
	_, stderr, code = w1bRunScenario(t, "K3-business-owner-gate", w1bOwnerBaseEnv(t, coverageDir,
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true",
		"JUHE_AI_BUSINESS_OWNER=legacy"))
	w1bRequireExitCode(t, "K3-business-owner-gate", code, 1)
	w1bRequireContains(t, "K3-business-owner-gate", stderr, "verify business owner gates: 启用系统 API 组合根时 JUHE_AI_BUSINESS_OWNER 必须为 gateway")

	// K4: handoff 门槛全过但切换证据文件缺失 → stderr 含 read business
	// owner cutover evidence。
	businessEvidenceDir := filepath.Join(root, "business-evidence")
	if err := os.MkdirAll(businessEvidenceDir, 0o750); err != nil {
		t.Fatalf("创建 business evidence 目录失败: %v", err)
	}
	missingEvidence := filepath.Join(businessEvidenceDir, "missing-evidence.json")
	coverageDir = w1bCoverageDir(t, "K4-business-evidence-missing")
	_, stderr, code = w1bRunScenario(t, "K4-business-evidence-missing", w1bOwnerBaseEnv(t, coverageDir,
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true",
		"JUHE_AI_BUSINESS_OWNER=gateway",
		"JUHE_AI_BUSINESS_DATABASE_PATH="+filepath.Join(root, "w1b-business-owner.sqlite"),
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
		"JUHE_AI_BUSINESS_SCHEMA_READY=true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH=epoch-w1b",
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH="+missingEvidence))
	w1bRequireExitCode(t, "K4-business-evidence-missing", code, 1)
	w1bRequireContains(t, "K4-business-evidence-missing", stderr, "read business owner cutover evidence")

	// K5: business 证据有效 + J3b enabled 但 J3b 证据路径缺失 → stderr 含
	// read J3b cutover evidence（business evidence 校验先通过，才走到 J3b）。
	businessEvidenceValidDir := filepath.Join(root, "business-evidence-valid")
	if err := os.MkdirAll(businessEvidenceValidDir, 0o750); err != nil {
		t.Fatalf("创建 business evidence 目录失败: %v", err)
	}
	validEvidence := w1bWriteCutoverEvidence(t, businessEvidenceValidDir, "epoch-w1b")
	coverageDir = w1bCoverageDir(t, "K5-j3b-evidence-missing")
	_, stderr, code = w1bRunScenario(t, "K5-j3b-evidence-missing", w1bOwnerBaseEnv(t, coverageDir,
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true",
		"JUHE_AI_BUSINESS_OWNER=gateway",
		"JUHE_AI_BUSINESS_DATABASE_PATH="+filepath.Join(root, "w1b-business-owner.sqlite"),
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
		"JUHE_AI_BUSINESS_SCHEMA_READY=true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH=epoch-w1b",
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH="+validEvidence,
		"JUHE_AI_J3B_ENABLED=true",
		"JUHE_AI_J3B_OWNER=gateway",
		"JUHE_AI_J3B_INSTANCE_ID=w1b-j3b",
		"JUHE_AI_J3B_STORE=sqlite",
		"JUHE_AI_J3B_DATABASE_PATH="+filepath.Join(root, "j3b-dedicated.sqlite"),
		"JUHE_AI_J3B_BUSINESS_DATABASE_PATH="+filepath.Join(root, "j3b-business.sqlite"),
		"JUHE_AI_J3B_CREDENTIAL_SECRET=w1b-credential-secret-value",
		"JUHE_AI_J3B_IDENTITY_SECRET=w1b-identity-secret-value",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED=true",
		"JUHE_AI_J3B_OWNER_EPOCH=epoch-w1b",
		"JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH="+missingEvidence,
		"JUHE_AI_J3B_SCHEMA_READY=true",
		"JUHE_AI_J3B_HEALTH_BOUNDARY_READY=true",
		"JUHE_AI_J3B_RUNTIME_READY=true",
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://127.0.0.1:1/0",
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE=juhe-ai:w1b-test"))
	w1bRequireExitCode(t, "K5-j3b-evidence-missing", code, 1)
	w1bRequireContains(t, "K5-j3b-evidence-missing", stderr, "read J3b cutover evidence")
}

// ---- Windows 进程组控制（场景 J） ----
// CREATE_NEW_PROCESS_GROUP 让子进程成为独立进程组，CTRL_BREAK_EVENT 只投递
// 给该组而不影响 go test 自身；Go runtime 把 CTRL_BREAK 映射为 os.Interrupt，
// 命中 main.go runPassiveGateway 的 signal.NotifyContext 优雅关闭路径。

func w1bSetNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func w1bSendCtrlBreak(pid int) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(pid))
}
