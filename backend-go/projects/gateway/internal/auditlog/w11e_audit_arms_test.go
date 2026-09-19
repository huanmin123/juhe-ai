package auditlog

// w11e auditlog 错误臂：LoadConfig 全链校验、retention 配置与非持久化来源
// 删除、pending blob GC 状态机、hot-search 构建与文件路径边界。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func w11eEnvWith(overrides map[string]string) func(string) string {
	base := sqliteEnv("")
	return func(name string) string {
		if value, ok := overrides[name]; ok {
			return value
		}
		return base[name]
	}
}

func TestW11ELoadConfigValidationArms(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]string
		want      string
	}{
		{"store mode", map[string]string{"JUHE_AI_AUDIT_LOG_STORE": "oracle"}, "JUHE_AI_AUDIT_LOG_STORE"},
		{"owner lease", map[string]string{"JUHE_AI_AUDIT_LOG_OWNER_LEASE": "1s"}, "OWNER_LEASE"},
		{"owner lease parse", map[string]string{"JUHE_AI_AUDIT_LOG_OWNER_LEASE": "x"}, "OWNER_LEASE"},
		{"retention interval", map[string]string{"JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL": "10ms"}, "RETENTION_INTERVAL"},
		{"retention interval parse", map[string]string{"JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL": "-1s"}, "RETENTION_INTERVAL"},
		{"retention batch", map[string]string{"JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE": "0"}, "RETENTION_BATCH_SIZE"},
		{"retention batch float", map[string]string{"JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE": "1.5"}, "RETENTION_BATCH_SIZE"},
		{"hot hours", map[string]string{"JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS": "999"}, "HOT_RETENTION_HOURS"},
		{"sample rate", map[string]string{"JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE": "2"}, "SAMPLE_RATE"},
		{"sample rate decimals", map[string]string{"JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE": "0.12345"}, "SAMPLE_RATE"},
		{"sample rate format", map[string]string{"JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE": "abc"}, "SAMPLE_RATE"},
		{"success days", map[string]string{"JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS": "-1"}, "SUCCESS_RETENTION_DAYS"},
		{"problem days", map[string]string{"JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS": "0"}, "PROBLEM_RETENTION_DAYS"},
		{"sample/days mismatch", map[string]string{"JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE": "0", "JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS": "3"}, "同时为 0"},
		{"success days below hot", map[string]string{"JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS": "72", "JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS": "1"}, "覆盖"},
	}
	// 2026-09-19 零配置默认后，instance id（回落 hostname）、路径族（按
	// datadir 固定名表派生）与 F3 settings 二选一（sqlite 回落业务库文件）
	// 不再是错误臂，见 TestW11EZeroConfigDerivedDefaults。
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadConfig(w11eEnvWith(tc.overrides)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望错误 %q: %v", tc.want, err)
			}
		})
	}
	// postgres 模式缺 URL / 连接池配置（2026-09-19 起 blob 目录派生，不再
	// 是 PG 模式错误臂）。
	pgBase := map[string]string{"JUHE_AI_AUDIT_LOG_STORE": "postgres"}
	if _, err := LoadConfig(w11eEnvWith(pgBase)); err == nil || !strings.Contains(err.Error(), "POSTGRES_URL") {
		t.Fatalf("postgres 缺 URL 必须拒绝: %v", err)
	}
	pgWithURL := map[string]string{"JUHE_AI_AUDIT_LOG_STORE": "postgres", "JUHE_AI_AUDIT_LOG_POSTGRES_URL": "postgres://w11e.invalid/db", "JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL": "postgres://w11e.invalid/settings", "JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY": " "}
	pgCfg, err := LoadConfig(w11eEnvWith(pgWithURL))
	if err != nil || pgCfg.PayloadBlobDirectory != filepath.Join("data", "audit-blob") {
		t.Fatalf("postgres 空 blob 目录必须派生 <DATA_DIR>/audit-blob: %+v err=%v", pgCfg, err)
	}
	// 与业务库共用物理文件必须拒绝。
	sameRoot := t.TempDir()
	sameEnv := sqliteEnv(sameRoot)
	for _, name := range []string{"audit.sqlite3", "business.sqlite3", "dataset.sqlite3", "usage.sqlite3", "stats.sqlite3", "runtime.sqlite3", "table.sqlite3"} {
		if err := os.WriteFile(filepath.Join(sameRoot, name), []byte("w11e"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadConfig(func(name string) string {
		if name == "JUHE_AI_AUDIT_LOG_DATABASE_PATH" {
			return sameEnv["JUHE_AI_DATABASE_PATH"]
		}
		return sameEnv[name]
	}); err == nil || !strings.Contains(err.Error(), "共用") {
		t.Fatalf("共用文件必须拒绝: %v", err)
	}
	pgPool := map[string]string{"JUHE_AI_AUDIT_LOG_STORE": "postgres", "JUHE_AI_AUDIT_LOG_POSTGRES_MAX_OPEN_CONNS": "2", "JUHE_AI_AUDIT_LOG_POSTGRES_MAX_IDLE_CONNS": "4", "JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL": "postgres://w11e.invalid/settings", "JUHE_AI_AUDIT_LOG_POSTGRES_URL": "postgres://w11e.invalid/db"}
	if _, err := LoadConfig(w11eEnvWith(pgPool)); err == nil || !strings.Contains(err.Error(), "连接池") {
		t.Fatalf("非法连接池必须拒绝: %v", err)
	}
	pgMaxIdle := map[string]string{"JUHE_AI_AUDIT_LOG_POSTGRES_MAX_IDLE_CONNS": "x"}
	if _, err := LoadConfig(w11eEnvWith(pgMaxIdle)); err == nil || !strings.Contains(err.Error(), "MAX_IDLE") {
		t.Fatalf("非法 idle 配置必须拒绝: %v", err)
	}
	// codex/usage shard 内放置审计库必须拒绝。
	root := t.TempDir()
	env := sqliteEnv(root)
	within := map[string]string{}
	for key, value := range env {
		within[key] = value
	}
	shardRoot := filepath.Join(root, "codex-shards")
	if err := os.MkdirAll(shardRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	within["JUHE_AI_AUDIT_LOG_DATABASE_PATH"] = filepath.Join(shardRoot, "audit.sqlite3")
	if _, err := LoadConfig(func(name string) string { return within[name] }); err == nil || !strings.Contains(err.Error(), "不得放入") {
		t.Fatalf("shard 内审计库必须拒绝: %v", err)
	}
}

// TestW11EZeroConfigDerivedDefaults 覆盖 2026-09-19 零配置默认：空 env /
// 仅 DATA_DIR 时 LoadConfig 成功，路径落 <DATA_DIR>/<固定名>，实例 ID 回落
// hostname，F3 只读 settings 二选一按模式回退。
func TestW11EZeroConfigDerivedDefaults(t *testing.T) {
	empty, err := LoadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("空 env LoadConfig: %v", err)
	}
	if empty.Mode != ModeSQLite {
		t.Fatalf("空 env mode=%q want sqlite", empty.Mode)
	}
	if empty.InstanceID == "" {
		t.Fatal("空 env 实例 ID 必须回落 hostname 默认值")
	}
	if empty.AuditDatabasePath != filepath.Join("data", "audit-log.sqlite3") || empty.PayloadBlobDirectory != filepath.Join("data", "audit-blob") {
		t.Fatalf("空 env 派生路径=%+v", empty)
	}
	if empty.BusinessSettingsPath != empty.BusinessPath || empty.BusinessPath != filepath.Join("data", "business.sqlite3") {
		t.Fatalf("空 env settings 路径必须回落业务库文件: %+v", empty)
	}
	// DATA_DIR 指向临时目录：派生根随之切换。
	root := t.TempDir()
	directed, err := LoadConfig(func(name string) string {
		if name == "JUHE_AI_DATA_DIR" {
			return root
		}
		return ""
	})
	if err != nil {
		t.Fatalf("DATA_DIR LoadConfig: %v", err)
	}
	if directed.AuditDatabasePath != filepath.Join(root, "audit-log.sqlite3") || directed.StatsPath != filepath.Join(root, "stats.sqlite3") || directed.UsageShardRoot != filepath.Join(root, "usage-shards") || directed.CodexShardRoot != filepath.Join(root, "codex-context", "state-shards") {
		t.Fatalf("DATA_DIR 派生路径=%+v", directed)
	}
	// PG 模式：store/settings URL 均回退主 JUHE_AI_POSTGRES_URL。
	pg, err := LoadConfig(func(name string) string {
		if name == "JUHE_AI_AUDIT_LOG_STORE" {
			return "postgres"
		}
		if name == "JUHE_AI_POSTGRES_URL" {
			return "postgres://w11e.zero/db"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("PG 回退 LoadConfig: %v", err)
	}
	if pg.Mode != ModePostgres || pg.PostgresURL != "postgres://w11e.zero/db" || pg.BusinessSettingsURL != "postgres://w11e.zero/db" {
		t.Fatalf("PG 回退配置=%+v", pg)
	}
}

func TestW11ECodexShardSymlinkRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "state-001.sqlite3")
	if err := os.WriteFile(target, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "nested", "state-002.sqlite3")); err != nil {
		t.Skipf("符号链接不可用（跳过）: %v", err)
	}
	if _, err := listCodexStateShardFiles(root); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("符号链接必须拒绝: %v", err)
	}
	if entries, err := listCodexStateShardFiles(filepath.Join(root, "w11e-missing")); err != nil || len(entries) != 0 {
		t.Fatalf("缺失根目录必须允许: %v %v", entries, err)
	}
}

func TestW11ERetentionConfigArms(t *testing.T) {
	if _, err := normalizeRetentionConfig(RetentionConfig{}); err == nil || !strings.Contains(err.Error(), "cutoff") {
		t.Fatalf("零值 cutoff 必须拒绝: %v", err)
	}
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	base := RetentionConfig{SuccessHotCutoff: now, SuccessCutoff: now.Add(-time.Hour), FailureCutoff: now.Add(-time.Hour), ErrorGroupCutoff: now.Add(-time.Hour)}
	hotBefore := base
	hotBefore.SuccessHotCutoff = base.SuccessCutoff.Add(-time.Minute)
	if _, err := normalizeRetentionConfig(hotBefore); err == nil || !strings.Contains(err.Error(), "不得早于") {
		t.Fatalf("hot 早于 success 必须拒绝: %v", err)
	}
	normalized, err := normalizeRetentionConfig(RetentionConfig{SuccessHotCutoff: now, SuccessCutoff: now, FailureCutoff: now, ErrorGroupCutoff: now, BatchSize: -5, SuccessSampleBucketThreshold: -3})
	if err != nil || normalized.BatchSize != 1000 || normalized.SuccessSampleBucketThreshold != 0 {
		t.Fatalf("归一化=%+v err=%v", normalized, err)
	}
	big := base
	big.BatchSize = 9999
	big.SuccessSampleBucketThreshold = 99999
	normalized, err = normalizeRetentionConfig(big)
	if err != nil || normalized.BatchSize != 5096 || normalized.SuccessSampleBucketThreshold != 10000 {
		t.Fatalf("上限归一化=%+v err=%v", normalized, err)
	}
	// RetentionConfigAt 两种模式。
	cfg := Config{SuccessHotRetentionHours: 2, SuccessRetentionDays: 3, ProblemRetentionDays: 7, RetentionBatchSize: 64, SuccessSampleRate: 0.25}
	derived := cfg.RetentionConfigAt(now)
	if derived.SuccessCutoff != now.Add(-72*time.Hour) || derived.FailureCutoff != now.Add(-168*time.Hour) || derived.SuccessHotCutoff != now.Add(-2*time.Hour) || derived.SuccessSampleBucketThreshold != 2500 || derived.BatchSize != 64 {
		t.Fatalf("派生配置=%+v", derived)
	}
	zero := Config{SuccessRetentionDays: 0, ProblemRetentionDays: 7, RetentionBatchSize: 8}
	derived = zero.RetentionConfigAt(now)
	if derived.SuccessCutoff != derived.SuccessHotCutoff {
		t.Fatalf("零保留模式必须回落热窗: %+v", derived)
	}
}

func TestW11ERetentionDeletesNonPersistedTrafficSources(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	fresh := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	health := fixture("w11e-health", LifecycleFinalized)
	health.CreatedAt, health.StartedAt, health.EndedAt = fresh, fresh, fresh
	if _, err := store.Persist(ctx, lease, health); err != nil {
		t.Fatal(err)
	}
	implementation := store.(*sqlStore)
	// 旧写入器遗留的非持久化来源行必须被无条件清除。
	if _, err := implementation.db.Exec(`UPDATE audit_logs SET traffic_source='account_health_check' WHERE id='w11e-health'`); err != nil {
		t.Fatal(err)
	}
	result, err := store.CleanupRetention(ctx, lease, RetentionConfig{SuccessHotCutoff: time.Now().UTC(), SuccessCutoff: time.Now().UTC(), FailureCutoff: time.Now().UTC(), ErrorGroupCutoff: time.Now().UTC(), BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedNonPersistedLogs != 1 {
		t.Fatalf("非持久化来源必须无条件删除: %+v", result)
	}
	var count int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE id LIKE 'w11e-%'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("残留行=%d err=%v", count, err)
	}
}

func TestW11ERetentionCleansErrorGroups(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	old := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	failed := fixture("w11e-egc", LifecycleFinalized)
	failed.AuditOutcome = AuditOutcomeGatewayFailed
	failed.Success = false
	failed.ErrorMessage = "w11e failure"
	failed.CreatedAt, failed.StartedAt, failed.EndedAt = old, old, old
	failed.Attempts = []AuditLogAttemptInput{{AttemptIndex: 0, ProviderCode: "openai", UpstreamMethod: "POST", UpstreamURL: "https://w11e.invalid", StartedAt: old, EndedAt: old, Success: boolPtr(false), UpstreamStatusCode: intPointer(500), ErrorPhase: "upstream", ErrorCode: "w11e_code", ErrorMessage: "w11e message"}}
	if _, err := store.Persist(ctx, lease, failed); err != nil {
		t.Fatal(err)
	}
	result, err := store.CleanupRetention(ctx, lease, RetentionConfig{SuccessHotCutoff: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), SuccessCutoff: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), FailureCutoff: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), ErrorGroupCutoff: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedLogs != 1 || result.DeletedErrorGroups != 1 {
		t.Fatalf("retention=%+v", result)
	}
}

func TestW11EPendingBlobGCArms(t *testing.T) {
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	implementation := store.(*sqlStore)
	// GC 行不存在 → false, nil。
	missing, err := implementation.cleanupScheduledBlobFile(ctx, lease, pendingBlobGCRow{blobID: "w11e-missing", storageKey: ""})
	if err != nil || missing {
		t.Fatalf("缺失 GC 行 removed=%t err=%v", missing, err)
	}
	// 无效租约必须拒绝。
	if _, err := implementation.cleanupScheduledBlobFile(ctx, OwnerLease{}, pendingBlobGCRow{blobID: "w11e-x", storageKey: "sha256/x"}); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("无效租约必须拒绝: %v", err)
	}
	// blob 元数据缺失（exists=false）→ 物理文件清理，返回 false。
	blobDir := implementation.blobDir
	storageKey := "sha256/w11e-gone"
	path := filepath.Join(blobDir, filepath.FromSlash(storageKey))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("w11e"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.db.Exec(`INSERT INTO audit_payload_blob_gc (blob_id,storage_key,scheduled_at) VALUES ('w11e-no-meta',?,?)`, storageKey, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	removed, err := implementation.cleanupScheduledBlobFile(ctx, lease, pendingBlobGCRow{blobID: "w11e-no-meta", storageKey: storageKey})
	if err != nil || removed {
		t.Fatalf("无元数据 removed=%t err=%v", removed, err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("物理文件必须删除: %v", statErr)
	}
	var gcRows int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_payload_blob_gc WHERE blob_id='w11e-no-meta'`).Scan(&gcRows); err != nil || gcRows != 0 {
		t.Fatalf("GC 行必须清理: %d %v", gcRows, err)
	}
	// 越界 storage key → 文件删除失败，GC 行保留可重试。
	if _, err := implementation.db.Exec(`INSERT INTO audit_payload_blob_gc (blob_id,storage_key,scheduled_at) VALUES ('w11e-escape','../../escape.bin',?)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.cleanupScheduledBlobFile(ctx, lease, pendingBlobGCRow{blobID: "w11e-escape", storageKey: "../../escape.bin"}); err == nil || !strings.Contains(err.Error(), "越界") {
		t.Fatalf("越界 key 必须失败: %v", err)
	}
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM audit_payload_blob_gc WHERE blob_id='w11e-escape'`).Scan(&gcRows); err != nil || gcRows != 1 {
		t.Fatalf("失败后 GC 行必须保留: %d %v", gcRows, err)
	}
}

func TestW11EBlobFilePathArms(t *testing.T) {
	root := t.TempDir()
	if _, err := blobFilePath(root, "  "); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空 key 必须拒绝: %v", err)
	}
	if _, err := blobFilePath(root, "../escape.bin"); err == nil || !strings.Contains(err.Error(), "越界") {
		t.Fatalf("越界 key 必须拒绝: %v", err)
	}
	valid, err := blobFilePath(root, "sha256/abc")
	if err != nil || !strings.HasSuffix(valid, filepath.Join("sha256", "abc")) {
		t.Fatalf("合法路径=%q err=%v", valid, err)
	}
	// removeBlobFile：空 key 直接成功；缺失文件视为已删除。
	if err := removeBlobFile(root, ""); err != nil {
		t.Fatalf("空 key 必须成功: %v", err)
	}
	if err := removeBlobFile(root, "sha256/missing"); err != nil {
		t.Fatalf("缺失文件必须成功: %v", err)
	}
	// blobFileMatchesMetadata：缺失 false、大小不一致报错、一致 true。
	matches, err := blobFileMatchesMetadata(root, "sha256/missing", 4)
	if err != nil || matches {
		t.Fatalf("缺失文件 matches=%t err=%v", matches, err)
	}
	path := filepath.Join(root, "sha256", "sized")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("12345"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := blobFileMatchesMetadata(root, "sha256/sized", 4); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("大小不一致必须报错: %v", err)
	}
	matches, err = blobFileMatchesMetadata(root, "sha256/sized", 5)
	if err != nil || !matches {
		t.Fatalf("一致文件 matches=%t err=%v", matches, err)
	}
}

func TestW11EHotSearchBuildArms(t *testing.T) {
	root := t.TempDir()
	// 空 ID 与非持久化来源跳过；空文本跳过。
	lines, err := buildHotSearchLines(root, []AuditLogInput{{ID: " "}, {ID: "w11e-health", TrafficSource: TrafficSource("runtime_recovery_probe")}})
	if err != nil || len(lines) != 0 {
		t.Fatalf("跳过逻辑 lines=%v err=%v", lines, err)
	}
	// 非法 createdAt。
	bad := fixture("w11e-bad-time", LifecycleFinalized)
	bad.CreatedAt, bad.EndedAt = "not-a-time", "not-a-time"
	if _, err := buildHotSearchLines(root, []AuditLogInput{bad}); err == nil || !strings.Contains(err.Error(), "createdAt 非法") {
		t.Fatalf("非法时间必须拒绝: %v", err)
	}
	// 空文本（无任何字段）跳过。
	empty := AuditLogInput{ID: "w11e-empty", LifecycleStatus: LifecycleFinalized, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	lines, err = buildHotSearchLines(root, []AuditLogInput{empty})
	if err != nil || len(lines) != 0 {
		t.Fatalf("空文本 lines=%v err=%v", lines, err)
	}
	// 长文本分片与 body 包含逻辑。
	long := fixture("w11e-long", LifecycleFinalized)
	long.Success = false
	long.ErrorMessage = strings.Repeat("w11e-error ", 9000)
	lines, err = buildHotSearchLines(root, []AuditLogInput{long})
	if err != nil || len(lines) != 1 {
		t.Fatalf("长文本 lines=%v err=%v", lines, err)
	}
	var total int
	for _, chunk := range lines {
		total += len(chunk)
	}
	if total < 2 {
		t.Fatalf("长文本必须分片: %d", total)
	}
	// appendHotSearchFile 目录被文件占用必须失败。
	blocker := filepath.Join(root, "w11e-hour")
	if err := os.WriteFile(blocker, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := appendHotSearchFile(filepath.Join(blocker, "hot.jsonl"), lines[filepath.Join(root, "w11e-hour", "hot.jsonl")]); err == nil {
		t.Fatal("被占路径必须失败")
	}
	// CleanupHotSearch 零值 cutoff。
	store := openSQLiteStore(t, sqliteConfig(t, t.TempDir()))
	defer store.Close()
	if _, err := store.CleanupHotSearch(context.Background(), OwnerLease{}, time.Time{}, 1); err == nil || !strings.Contains(err.Error(), "cutoff") {
		t.Fatalf("零值 cutoff 必须拒绝: %v", err)
	}
}

func TestW11ESmallHelperArms(t *testing.T) {
	if placeholders(0) != "NULL" || placeholders(2) != "?,?" {
		t.Fatal("placeholder 生成错误")
	}
	unique := uniqueStrings([]string{"a", "", "a", "b"})
	if len(unique) != 2 || unique[0] != "a" || unique[1] != "b" {
		t.Fatalf("去重=%v", unique)
	}
	if len(stringArgs([]string{"x"})) != 1 || len(stringAny([]string{"x", "y"})) != 2 {
		t.Fatal("参数转换错误")
	}
	if !isNonPersistedTrafficSource("cooldown_retest") || isNonPersistedTrafficSource("gateway") {
		t.Fatal("非持久化来源判定错误")
	}
}
