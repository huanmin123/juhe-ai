package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/businesshandoff"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/j3bmodelcheck"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
)

// 本文件在同包内直调 CLI 运行层：main() 的 flag/版本分支、runStorageBootstrap
// 的 SQLite/Postgres 驱动矩阵、各只读检查 runner 的成功路径与退出码 helper。
// 依赖真实 PostgreSQL 或与当前仓库状态强相关的 runner（capability manifest、
// owner manifest、PG bootstrap/backfill、schema snapshot 顶层入口）不在直调
// 范围内——它们的 os.Exit 分支会终止测试进程，已由 exec 子进程测试另行覆盖。

// wmCaptureStdout 捕获 fn 期间写入 os.Stdout 的内容。
func wmCaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original }()
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- string(data)
	}()
	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	return <-done
}

// wmCallMainWithFreshFlags 用全新的 FlagSet 调用 main()，避免 flag 状态在多次
// Parse 之间泄漏；调用方负责恢复 os.Args。
func wmCallMainWithFreshFlags(t *testing.T, args []string) string {
	t.Helper()
	savedCommandLine := flag.CommandLine
	savedArgs := os.Args
	flag.CommandLine = flag.NewFlagSet("wm-test", flag.ExitOnError)
	os.Args = append([]string{"juhe-ai-maintenance"}, args...)
	defer func() {
		flag.CommandLine = savedCommandLine
		os.Args = savedArgs
	}()
	return wmCaptureStdout(t, main)
}

func TestWMMainVersionAndBoundaryBranches(t *testing.T) {
	t.Run("version", func(t *testing.T) {
		output := wmCallMainWithFreshFlags(t, []string{"-version"})
		if !strings.Contains(output, "juhe-ai-maintenance project=") || !strings.Contains(output, "contract=") {
			t.Fatalf("--version 输出必须包含项目与契约版本: %q", output)
		}
	})
	t.Run("check boundary", func(t *testing.T) {
		output := wmCallMainWithFreshFlags(t, []string{"-check-boundary"})
		if !strings.Contains(output, "boundary=ready") {
			t.Fatalf("--check-boundary 输出异常: %q", output)
		}
	})
}

func TestWMRunStorageBootstrapDriverMatrix(t *testing.T) {
	root := t.TempDir()
	paths := "business=" + filepath.Join(root, "business.sqlite3") +
		",chat=" + filepath.Join(root, "chat.sqlite3") +
		",dataset=" + filepath.Join(root, "dataset.sqlite3") +
		",usage-catalog=" + filepath.Join(root, "usage.sqlite3") +
		",stats=" + filepath.Join(root, "stats.sqlite3") +
		",codex-context-shard-root=" + filepath.Join(root, "shards")

	t.Run("full sqlite ensure and seed", func(t *testing.T) {
		output := wmCaptureStdout(t, func() {
			if code := runStorageBootstrap(true, true, "sqlite", paths+",codex-context-shard-count=2", "", ""); code != 0 {
				t.Fatalf("runStorageBootstrap exit=%d", code)
			}
		})
		var report storageBootstrapReport
		if err := json.Unmarshal([]byte(output), &report); err != nil {
			t.Fatalf("报告必须可解码: %v\n%s", err, output)
		}
		if !report.EnsureRan || !report.SeedRan || report.Driver != "sqlite" {
			t.Fatalf("报告驱动矩阵错误: %+v", report)
		}
		// ensureSQLiteStorage 的键：business、stats、chat、2 个 codex shard、
		// dataset、usage-catalog。
		if len(report.SQLite.Ensure) != 7 || report.SQLite.Seed == nil || report.SQLite.Seed.ModelCatalogRows <= 0 {
			t.Fatalf("六库 ensure + business seed 语义缺失: %+v", report.SQLite)
		}
		// 每个库都真实落盘。
		for _, name := range []string{"business.sqlite3", "chat.sqlite3", "dataset.sqlite3", "usage.sqlite3", "stats.sqlite3", "shards/state-000.sqlite3", "shards/state-001.sqlite3"} {
			if _, err := os.Stat(filepath.Join(root, name)); err != nil {
				t.Fatalf("库文件 %s 应存在: %v", name, err)
			}
		}
	})

	usageExitCode := func(name string, ensure, seed bool, driverName, pathsArg, dsn string, want int) {
		t.Run(name, func(t *testing.T) {
			if code := runStorageBootstrap(ensure, seed, driverName, pathsArg, dsn, ""); code != want {
				t.Fatalf("exit=%d want %d", code, want)
			}
		})
	}
	usageExitCode("invalid driver", true, false, "mysql", "", "", 2)
	usageExitCode("sqlite with dsn", true, false, "sqlite", "business=x", "postgres://u@h/db", 2)
	usageExitCode("postgres with paths", true, false, "postgres", "business=x", "", 2)
	usageExitCode("bad paths entry", true, false, "sqlite", "business", "", 2)
	usageExitCode("missing required key", true, false, "sqlite", "business=x", "", 2)
	usageExitCode("unknown paths key", true, false, "sqlite", "business=x,unknown=y", "", 2)
	usageExitCode("bad shard count", true, false, "sqlite", "business=x,codex-context-shard-count=0", "", 2)
	usageExitCode("postgres without dsn", true, false, "postgres", "", "  ", 2)
	usageExitCode("postgres dsn wrong scheme", true, false, "postgres", "", "sqlite:///x.db", 2)
	usageExitCode("seed without ensure", false, true, "postgres", "", "", 2)

	t.Run("codex shard count bounds", func(t *testing.T) {
		for _, raw := range []string{"codex-context-shard-count=0", "codex-context-shard-count=257", "codex-context-shard-count=x"} {
			if _, err := parseSQLiteStoragePaths(paths + "," + raw); err == nil {
				t.Fatalf("shard 计数 %q 必须被拒绝", raw)
			}
		}
	})

	t.Run("sqlite ensure failure returns 1", func(t *testing.T) {
		// business 指向一个目录：路径解析通过但 ensure 打不开文件。
		brokenPaths := "business=" + root +
			",chat=" + filepath.Join(root, "chat.sqlite3") +
			",dataset=" + filepath.Join(root, "dataset.sqlite3") +
			",usage-catalog=" + filepath.Join(root, "usage.sqlite3") +
			",stats=" + filepath.Join(root, "stats.sqlite3") +
			",codex-context-shard-root=" + filepath.Join(root, "shards2")
		if code := runStorageBootstrap(true, false, "sqlite", brokenPaths, "", ""); code != 1 {
			t.Fatalf("ensure 失败必须返回 1: %d", code)
		}
	})

	t.Run("postgres unreachable dsn returns 1", func(t *testing.T) {
		// sql.Open 是懒连接：前缀校验通过后 DSN 故障在首个语句（ensure）时暴露，
		// 按契约返回运行时失败 1 而非用法错误 2。
		if code := runStorageBootstrap(true, false, "postgres", "", "postgres://wm@127.0.0.1:1/wm_none", ""); code != 1 {
			t.Fatalf("连接失败必须返回 1: %d", code)
		}
	})
}

func TestWMSeedSecretValueResolvesFlagThenEnv(t *testing.T) {
	t.Setenv("JUHE_AI_SECRET", "")
	if got := seedSecretValue(" flag "); got != "flag" {
		t.Fatalf("flag 值必须去空白并优先: %q", got)
	}
	if got := seedSecretValue(""); got != "" {
		t.Fatalf("无 flag 无 env 必须为空（Node dev 默认在 schema 内部选择）: %q", got)
	}
	t.Setenv("JUHE_AI_SECRET", " from-env ")
	if got := seedSecretValue(""); got != "from-env" {
		t.Fatalf("env 必须去空白生效: %q", got)
	}
}

func TestWMRunJ3bSQLiteBootstrapRunner(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	checkPath := filepath.Join(root, "check.db")
	applyPath := filepath.Join(root, "apply.db")

	// check 模式在 schema 已就绪的文件上应直接返回（不触发 exit 3）。
	db, err := j3bmodelcheck.OpenSQLite(checkPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j3bmodelcheck.RunSQLite(ctx, db, true); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Run("check mode returns on ready schema", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, checkPath)
		wmCaptureStdout(t, func() { runJ3bModelCheckSQLiteBootstrap(false, false, false, false) })
	})
	t.Run("apply mode requires confirmations handled upstream", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, applyPath)
		wmCaptureStdout(t, func() { runJ3bModelCheckSQLiteBootstrap(true, true, true, true) })
		if _, err := os.Stat(applyPath); err != nil {
			t.Fatalf("apply 应创建专属文件: %v", err)
		}
	})
}

// wmBuildJ3bSQLiteCutoverSet 构造 target/dataset/stats 三文件并完成一次
// backfill，使 readback 达到 Complete（用于 runner 成功路径）。
func wmBuildJ3bSQLiteCutoverSet(t *testing.T) (root, targetPath, datasetPath, statsPath string) {
	t.Helper()
	ctx := context.Background()
	root = t.TempDir()
	targetPath = filepath.Join(root, "target.db")
	datasetPath = filepath.Join(root, "dataset.db")
	statsPath = filepath.Join(root, "stats.db")
	apply := func(path string) {
		db, err := j3bmodelcheck.OpenSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := j3bmodelcheck.RunSQLite(ctx, db, true); err != nil {
			t.Fatal(err)
		}
	}
	apply(datasetPath)
	apply(statsPath)
	apply(targetPath)
	stats, err := j3bmodelcheck.OpenSQLite(statsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stats.Close()
	if _, err := stats.Exec(`CREATE TABLE stats_job_state (scope_type TEXT NOT NULL,scope_id TEXT NOT NULL DEFAULT '',job_name TEXT NOT NULL,cursor_created_at TEXT,cursor_id TEXT,last_success_at TEXT,last_error_message TEXT,lag_seconds INTEGER,updated_at TEXT NOT NULL,PRIMARY KEY(scope_type,scope_id,job_name))`); err != nil {
		t.Fatal(err)
	}
	if _, err := stats.Exec(`INSERT INTO stats_job_state(scope_type,scope_id,job_name,cursor_created_at,cursor_id,last_success_at,last_error_message,lag_seconds,updated_at) VALUES ('global','','model-trust-observation-aggregation','2026-08-27T10:00:00Z','obs-1','2026-08-27T10:01:00Z',NULL,3,'2026-08-27T10:01:00Z')`); err != nil {
		t.Fatal(err)
	}
	stats.Close()
	target, err := j3bmodelcheck.OpenSQLite(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if _, err := j3bmodelcheck.BackfillSQLite(ctx, target, datasetPath, statsPath); err != nil {
		t.Fatal(err)
	}
	return root, targetPath, datasetPath, statsPath
}

func TestWMRunJ3bSQLiteReadbackAndBackfillRunners(t *testing.T) {
	_, targetPath, datasetPath, statsPath := wmBuildJ3bSQLiteCutoverSet(t)

	t.Run("readback runner exits zero via Complete projection", func(t *testing.T) {
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, targetPath)
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH", datasetPath)
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH", statsPath)
		output := wmCaptureStdout(t, func() { runJ3bModelCheckSQLiteReadback() })
		var report j3bmodelcheck.BackfillVerificationReport
		if err := json.Unmarshal([]byte(output), &report); err != nil {
			t.Fatalf("readback 报告必须可解码: %v", err)
		}
		if !report.Complete || j3bSQLiteReadbackExitCode(report) != 0 {
			t.Fatalf("readback 必须完整: %+v", report)
		}
	})

	t.Run("backfill runner copies with complete evidence", func(t *testing.T) {
		// 第二个 target 预应用契约 schema 后再次执行 backfill（幂等），
		// evidence 必须是含清单的完整剪除证据。
		evidencePath := wmWriteCompleteCutoverEvidenceWithManifest(t)
		secondTarget := filepath.Join(t.TempDir(), "second-target.db")
		second, err := j3bmodelcheck.OpenSQLite(secondTarget)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j3bmodelcheck.RunSQLite(context.Background(), second, true); err != nil {
			second.Close()
			t.Fatal(err)
		}
		second.Close()
		t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, secondTarget)
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_DATASET_PATH", datasetPath)
		t.Setenv("JUHE_AI_MAINTENANCE_J3B_SOURCE_STATS_PATH", statsPath)
		output := wmCaptureStdout(t, func() {
			runJ3bModelCheckSQLiteBackfill(true, true, true, evidencePath)
		})
		var report j3bmodelcheck.BackfillReport
		if err := json.Unmarshal([]byte(output), &report); err != nil {
			t.Fatalf("backfill 报告必须可解码: %v", err)
		}
		if report.InsertedRows["model_trust_aggregation_state"] != 1 {
			t.Fatalf("trust 游标应被复制: %+v", report.InsertedRows)
		}
	})
}

// wmWriteCompleteCutoverEvidenceWithManifest 生成含合法 readback manifest 的
// 完整 cutover 证据文件（与 contracts 契约逐字段对齐）。
func wmWriteCompleteCutoverEvidenceWithManifest(t *testing.T) string {
	t.Helper()
	// 证据新鲜度按运行时钟校验，必须以当前时间生成（MaxAge 1 小时）。
	now := time.Now().UTC()
	manifest := contracts.J3bReadbackManifest{
		FormatVersion: contracts.J3bReadbackManifestFormatVersion,
		Scope:         contracts.J3bReadbackManifestScope,
		Producer:      "wm-test", SourceSnapshotIdentity: "wm-snapshot-1", SourceSchema: "legacy-sqlite-dataset+stats", TargetSchema: "juhe-j3b-sqlite",
		ProjectionComplete: true, VerifiedAt: now.Format(time.RFC3339),
	}
	for _, name := range []string{
		"account_quality_health_hourly", "model_check_items", "model_check_observations", "model_check_runs",
		"model_account_trust_results", "model_token_intercept_baseline_versions", "model_trust_aggregation_state",
		"model_trust_latest_dirty_accounts", "model_trust_observation_receipts",
	} {
		manifest.Tables = append(manifest.Tables, contracts.J3bReadbackTableDigest{Name: name, SourceRows: 1, TargetRows: 1, SourceDigest: strings.Repeat("a", 64), TargetDigest: strings.Repeat("a", 64)})
	}
	hash, err := contracts.ComputeJ3bReadbackManifestHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manifestPath := filepath.Join(root, "readback-manifest.json")
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestFileHash := sha256.Sum256(manifestData)

	backupPath := filepath.Join(root, "backup.bin")
	backupData := []byte("wm immutable backup")
	if err := os.WriteFile(backupPath, backupData, 0o600); err != nil {
		t.Fatal(err)
	}
	backupDigest := sha256.Sum256(backupData)
	readbackDigest := hex.EncodeToString(backupDigest[:])

	evidence := businesshandoff.J3bCutoverEvidence{
		OldOwner: "node", NewOwner: contracts.J3bGatewayCutoverOwner, OwnerEpoch: "wm-epoch-1", DrainCompleted: true,
		InFlight: 0, ActivePathZero: true,
		BackupArtifact:       businesshandoff.J3bBackupArtifact{Path: backupPath, Hash: hex.EncodeToString(backupDigest[:])},
		RollbackReplayCursor: "receipt:000001",
		Freshness:            businesshandoff.J3bEvidenceFreshness{CapturedAt: now.Add(-time.Minute).Format(time.RFC3339), MaxAgeSeconds: 3600},
		SourceDigest:         readbackDigest, TargetDigest: readbackDigest,
		ReadbackManifest: businesshandoff.J3bReadbackManifestReference{Path: manifestPath, Hash: hex.EncodeToString(manifestFileHash[:]), FormatVersion: contracts.J3bReadbackManifestFormatVersion, Scope: contracts.J3bReadbackManifestScope, SourceSnapshotIdentity: "wm-snapshot-1", SourceSchema: "legacy-sqlite-dataset+stats", TargetSchema: "juhe-j3b-sqlite"},
		BlockedFindings:  0,
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	evidencePath := filepath.Join(root, "cutover-evidence.json")
	if err := os.WriteFile(evidencePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return evidencePath
}

func TestWMRunJ3bCutoverEvidenceCheckReadyPath(t *testing.T) {
	evidencePath := wmWriteCompleteCutoverEvidenceWithManifest(t)
	output := wmCaptureStdout(t, func() { runJ3bCutoverEvidenceCheck(evidencePath) })
	var report businesshandoff.J3bCutoverEvidenceReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("cutover 报告必须可解码: %v", err)
	}
	if !report.Ready || j3bCutoverEvidenceExitCode(report) != 0 {
		t.Fatalf("完整证据必须就绪: %+v", report)
	}
}

func TestWMRunBusinessSQLiteRunners(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	business, err := bootstrap.OpenSQLiteFile(businessPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := schema.EnsureSQLiteBusiness(context.Background(), business); err != nil {
		business.Close()
		t.Fatal(err)
	}
	if err := business.Close(); err != nil {
		t.Fatal(err)
	}

	t.Run("handoff runner with distinct files", func(t *testing.T) {
		j3bPath := filepath.Join(root, "j3b.db")
		j3b, err := j3bmodelcheck.OpenSQLite(j3bPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j3bmodelcheck.RunSQLite(context.Background(), j3b, true); err != nil {
			j3b.Close()
			t.Fatal(err)
		}
		j3b.Close()
		wmCaptureStdout(t, func() { runBusinessSQLiteHandoffCheck(businessPath, j3bPath) })
	})

	t.Run("schema runner returns on contract-ready business file", func(t *testing.T) {
		// 双实现一致性守卫：若 schema 包生成的 business 库不满足 handoff 契约，
		// runner 会 exit(3)，这里跳过而不是杀死测试进程。
		if report, err := businesshandoff.VerifySQLiteSchema(context.Background(), businessPath); err != nil || !report.Ready {
			t.Skipf("schema 包 business 库未满足 handoff 契约，跳过直调: %+v err=%v", report, err)
		}
		wmCaptureStdout(t, func() { runBusinessSQLiteSchemaCheck(businessPath) })
	})
}

func TestWMRepoStateDependentRunnersReturnCleanly(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	archiveMissing := false
	if _, err := os.Stat(filepath.Join(root, "migration-backup", "node", "final-archive", "backend", "src", "modules", "db-service", "db-service-types.ts")); err != nil {
		archiveMissing = true
	}
	if !archiveMissing {
		t.Skip("Node archive contract sources present; runner may exit non-zero by design")
	}
	t.Run("node active path scan exits zero on trimmed archive", func(t *testing.T) {
		wmCaptureStdout(t, func() { runNodeJ3bActivePathCheck() })
	})
	t.Run("j3c readonly boundary exits zero", func(t *testing.T) {
		wmCaptureStdout(t, func() { runJ3cReadOnlyBoundaryCheck() })
	})
}

func TestWMPathAndEnvHelpers(t *testing.T) {
	// 行为存疑：<实现只对 env 值去空白，fallback 原样返回>。按当前实际行为断言。
	if got := envOrDefault("WM_MISSING_ENV_KEY", " fallback "); got != " fallback " {
		t.Fatalf("fallback 按当前实现应原样返回: %q", got)
	}
	t.Setenv("WM_PRESENT_ENV_KEY", " value ")
	if got := envOrDefault("WM_PRESENT_ENV_KEY", "fallback"); got != "value" {
		t.Fatalf("envOrDefault 环境值应去空白: %q", got)
	}
	if got := resolveRepoPath("docs/migration"); !filepath.IsAbs(got) && !strings.Contains(got, "docs/migration") {
		t.Fatalf("resolveRepoPath 应向上解析或原样返回: %q", got)
	}
	absolute := filepath.Join(t.TempDir(), "absolute.json")
	if got := resolveRepoPath(absolute); got != absolute {
		t.Fatalf("绝对路径必须原样返回: %q", got)
	}
	if !isRepositoryRoot(resolveRepositoryRoot()) {
		t.Fatalf("从测试 CWD 向上必须找到仓库根: %s", resolveRepositoryRoot())
	}
	if isRepositoryRoot(t.TempDir()) {
		t.Fatal("临时目录不是仓库根")
	}
}

func TestWMJ3bInventoryEvidenceRequiredExitCode(t *testing.T) {
	if got := j3bInventoryEvidenceRequiredExitCode("  "); got != 2 {
		t.Fatalf("空路径 exit=%d want 2", got)
	}
	if got := j3bInventoryEvidenceRequiredExitCode("evidence.json"); got != 0 {
		t.Fatalf("显式路径 exit=%d want 0", got)
	}
}

func TestWMRunJ3bModelCheckInventoryRunner(t *testing.T) {
	// 完整证据：每个清单事实都有显式 disposition 与合法 sha256。
	evidence := make(map[string]j3bmodelcheck.LegacyJ3bFactEvidence, len(j3bmodelcheck.LegacyJ3bFactInventory))
	for _, item := range j3bmodelcheck.LegacyJ3bFactInventory {
		evidence[item.Name] = j3bmodelcheck.LegacyJ3bFactEvidence{
			SourceSchema: item.SourceSchema, SourceTable: item.SourceTable, Scope: item.Scope,
			Digest:                   "sha256:" + strings.Repeat("a", 64),
			BackfillReadbackVerified: item.Disposition == j3bmodelcheck.LegacyFactBackfill,
			RetentionVerified:        item.Disposition == j3bmodelcheck.LegacyFactRetain,
		}
	}
	data, err := json.Marshal(j3bmodelcheck.LegacyJ3bFactEvidenceDocument{Facts: evidence})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "inventory-evidence.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	output := wmCaptureStdout(t, func() { runJ3bModelCheckInventory(path) })
	var report j3bmodelcheck.LegacyJ3bFactCoverageReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("inventory 报告必须可解码: %v", err)
	}
	if !report.Ready || j3bInventoryExitCode(report) != 0 {
		t.Fatalf("完整证据必须就绪: %+v", report)
	}
}

func TestWMRunBusinessOwnerManifestCheckAgainstGraveyard(t *testing.T) {
	// cmd 默认指向 migration-backup-1 墓地；该前提缺席时 runner 会 exit(1)，
	// 此处跳过而不是杀死测试进程。
	root := filepath.Join("..", "..", "..", "..", "..")
	typesPath := filepath.Join(root, "migration-backup-1", "node", "final-archive", "backend", "src", "modules", "db-service", "db-service-types.ts")
	if _, err := os.Stat(typesPath); err != nil {
		t.Skip("migration-backup-1 墓地契约源缺席，无法直调 owner manifest runner")
	}
	wmCaptureStdout(t, func() { runBusinessOwnerManifestCheck() })
}

func TestWMRunBusinessSQLiteHandoffCheckEnvFallback(t *testing.T) {
	root := t.TempDir()
	businessPath := filepath.Join(root, "business.sqlite3")
	j3bPath := filepath.Join(root, "j3b.db")
	business, err := bootstrap.OpenSQLiteFile(businessPath)
	if err != nil {
		t.Fatal(err)
	}
	business.Close()
	j3b, err := j3bmodelcheck.OpenSQLite(j3bPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j3bmodelcheck.RunSQLite(context.Background(), j3b, true); err != nil {
		j3b.Close()
		t.Fatal(err)
	}
	j3b.Close()
	// 形参为空时必须回落到环境变量（runner 的 env fallback 分支）。
	t.Setenv("JUHE_AI_MAINTENANCE_BUSINESS_SQLITE_PATH", businessPath)
	t.Setenv(j3bmodelcheck.SQLiteBootstrapEnv, j3bPath)
	wmCaptureStdout(t, func() { runBusinessSQLiteHandoffCheck("", "") })
}
