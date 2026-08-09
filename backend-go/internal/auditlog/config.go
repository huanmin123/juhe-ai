package auditlog

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultOwnerLease               = 30 * time.Second
	defaultRetentionInterval        = time.Minute
	defaultRetentionBatchSize       = 100
	defaultSuccessHotRetentionHours = 1
	defaultSuccessSampleRate        = 0.1
	defaultSuccessRetentionDays     = 3
	defaultProblemRetentionDays     = 7
)

type Config struct {
	InstanceID               string
	Mode                     Mode
	OwnerLease               time.Duration
	RetentionInterval        time.Duration
	RetentionBatchSize       int
	SuccessHotRetentionHours int
	SuccessSampleRate        float64
	SuccessRetentionDays     int
	ProblemRetentionDays     int
	AuditDatabasePath        string
	PayloadBlobDirectory     string
	HotSearchDirectory       string
	PostgresURL              string
	BusinessSettingsPath     string
	BusinessSettingsURL      string
	BusinessPath             string
	DatasetPath              string
	UsageCatalogPath         string
	StatsPath                string
	RuntimeLogDatabasePath   string
	TableMonitorDatabasePath string
	CodexShardRoot           string
	UsageShardRoot           string
}

func LoadConfig(getenv func(string) string) (Config, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_STORE"))))
	if mode != ModeSQLite && mode != ModePostgres {
		return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_STORE 必须为 sqlite 或 postgres")
	}
	cfg := Config{
		InstanceID:               strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_INSTANCE_ID")),
		Mode:                     mode,
		AuditDatabasePath:        strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_DATABASE_PATH")),
		PayloadBlobDirectory:     strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY")),
		HotSearchDirectory:       strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY")),
		PostgresURL:              strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL")),
		BusinessSettingsPath:     strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH")),
		BusinessSettingsURL:      strings.TrimSpace(getenv("JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL")),
		BusinessPath:             strings.TrimSpace(getenv("JUHE_AI_DATABASE_PATH")),
		DatasetPath:              strings.TrimSpace(getenv("JUHE_AI_DATASET_DATABASE_PATH")),
		UsageCatalogPath:         strings.TrimSpace(getenv("JUHE_AI_USAGE_CATALOG_DATABASE_PATH")),
		StatsPath:                strings.TrimSpace(getenv("JUHE_AI_STATS_DATABASE_PATH")),
		RuntimeLogDatabasePath:   strings.TrimSpace(getenv("JUHE_AI_RUNTIME_LOG_DATABASE_PATH")),
		TableMonitorDatabasePath: strings.TrimSpace(getenv("JUHE_AI_TABLE_MONITOR_DATABASE_PATH")),
		CodexShardRoot:           strings.TrimSpace(getenv("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")),
		UsageShardRoot:           strings.TrimSpace(getenv("JUHE_AI_USAGE_SHARD_ROOT")),
	}
	if cfg.InstanceID == "" {
		return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_INSTANCE_ID 是稳定实例 ID 的必填配置")
	}
	lease, err := parseDuration("JUHE_AI_AUDIT_LOG_OWNER_LEASE", getenv("JUHE_AI_AUDIT_LOG_OWNER_LEASE"), defaultOwnerLease)
	if err != nil || lease < 5*time.Second {
		return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_OWNER_LEASE 必须是不少于 5s 的正 duration")
	}
	cfg.OwnerLease = lease
	retentionInterval, err := parseDuration("JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL", getenv("JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL"), defaultRetentionInterval)
	if err != nil || retentionInterval < time.Second || retentionInterval > 24*time.Hour {
		return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL 必须为 1s 到 24h 的正 duration")
	}
	cfg.RetentionInterval = retentionInterval
	retentionBatchSize, err := parseBoundedInteger("JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE", getenv("JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE"), defaultRetentionBatchSize, 1, 10000)
	if err != nil {
		return Config{}, err
	}
	cfg.RetentionBatchSize = retentionBatchSize
	successHotRetentionHours, err := parseBoundedInteger("JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS", getenv("JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS"), defaultSuccessHotRetentionHours, 0, 168)
	if err != nil {
		return Config{}, err
	}
	cfg.SuccessHotRetentionHours = successHotRetentionHours
	successSampleRate, err := parseBoundedDecimal("JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE", getenv("JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE"), defaultSuccessSampleRate, 0, 1, 4)
	if err != nil {
		return Config{}, err
	}
	cfg.SuccessSampleRate = successSampleRate
	successRetentionDays, err := parseBoundedInteger("JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS", getenv("JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS"), defaultSuccessRetentionDays, 0, 3650)
	if err != nil {
		return Config{}, err
	}
	cfg.SuccessRetentionDays = successRetentionDays
	problemRetentionDays, err := parseBoundedInteger("JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS", getenv("JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS"), defaultProblemRetentionDays, 1, 3650)
	if err != nil {
		return Config{}, err
	}
	cfg.ProblemRetentionDays = problemRetentionDays
	if err := cfg.validateRetentionPolicy(); err != nil {
		return Config{}, err
	}
	if cfg.BusinessSettingsPath == "" && cfg.BusinessSettingsURL == "" {
		return Config{}, fmt.Errorf("必须设置 JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH 或 JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL；F3 只能只读业务设置")
	}
	if mode == ModePostgres {
		if cfg.PostgresURL == "" {
			return Config{}, fmt.Errorf("postgres 模式缺少 JUHE_AI_POSTGRES_URL")
		}
		if cfg.PayloadBlobDirectory == "" {
			return Config{}, fmt.Errorf("postgres 模式缺少 JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY")
		}
		return cfg, nil
	}
	if cfg.AuditDatabasePath == "" || cfg.PayloadBlobDirectory == "" {
		return Config{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_AUDIT_LOG_DATABASE_PATH 或 JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY")
	}
	for _, candidate := range []struct{ name, path string }{
		{"JUHE_AI_DATABASE_PATH", cfg.BusinessPath},
		{"JUHE_AI_DATASET_DATABASE_PATH", cfg.DatasetPath},
		{"JUHE_AI_USAGE_CATALOG_DATABASE_PATH", cfg.UsageCatalogPath},
		{"JUHE_AI_STATS_DATABASE_PATH", cfg.StatsPath},
		{"JUHE_AI_RUNTIME_LOG_DATABASE_PATH", cfg.RuntimeLogDatabasePath},
		{"JUHE_AI_TABLE_MONITOR_DATABASE_PATH", cfg.TableMonitorDatabasePath},
	} {
		if candidate.path == "" {
			return Config{}, fmt.Errorf("sqlite 模式缺少 %s，无法验证 F3 专库隔离", candidate.name)
		}
		same, err := sameSQLiteFile(cfg.AuditDatabasePath, candidate.path)
		if err != nil {
			return Config{}, fmt.Errorf("校验 JUHE_AI_AUDIT_LOG_DATABASE_PATH 与 %s 的物理隔离失败: %w", candidate.name, err)
		}
		if same {
			return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_DATABASE_PATH 不得与 %s 共用 SQLite 文件", candidate.name)
		}
	}
	if cfg.BusinessSettingsPath != "" {
		same, err := sameSQLiteFile(cfg.AuditDatabasePath, cfg.BusinessSettingsPath)
		if err != nil {
			return Config{}, fmt.Errorf("校验业务设置只读路径与 F3 SQLite 专库隔离失败: %w", err)
		}
		if same {
			return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH 不得指向 F3 SQLite 专库")
		}
	}
	if cfg.CodexShardRoot == "" {
		return Config{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT，无法验证 Node SQLite shard 隔离")
	}
	if err := requirePhysicalShardRoot(cfg.CodexShardRoot, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT"); err != nil {
		return Config{}, err
	}
	within, err := pathWithin(cfg.CodexShardRoot, cfg.AuditDatabasePath)
	if err != nil {
		return Config{}, fmt.Errorf("校验 F3 SQLite 专库与 Codex shard 根目录隔离失败: %w", err)
	}
	if within {
		return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_DATABASE_PATH 不得放入 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")
	}
	codexEntries, err := listCodexStateShardFiles(cfg.CodexShardRoot)
	if err != nil {
		return Config{}, fmt.Errorf("递归枚举 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 失败: %w", err)
	}
	for _, entry := range codexEntries {
		same, err := sameSQLiteFile(cfg.AuditDatabasePath, entry)
		if err != nil {
			return Config{}, fmt.Errorf("校验 F3 SQLite 专库与 Codex state shard %q 的物理隔离失败: %w", entry, err)
		}
		if same {
			return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_DATABASE_PATH 不得与 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 中的 state shard 共用文件")
		}
	}
	if cfg.UsageShardRoot == "" {
		return Config{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_USAGE_SHARD_ROOT，无法验证 usage shard 物理隔离")
	}
	if err := requirePhysicalShardRoot(cfg.UsageShardRoot, "JUHE_AI_USAGE_SHARD_ROOT"); err != nil {
		return Config{}, err
	}
	within, err = pathWithin(cfg.UsageShardRoot, cfg.AuditDatabasePath)
	if err != nil {
		return Config{}, fmt.Errorf("校验 F3 SQLite 专库与 usage shard 根目录隔离失败: %w", err)
	}
	if within {
		return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_DATABASE_PATH 不得放入 JUHE_AI_USAGE_SHARD_ROOT")
	}
	entries, err := listUsageShardFiles(cfg.UsageShardRoot)
	if err != nil {
		return Config{}, fmt.Errorf("递归枚举 JUHE_AI_USAGE_SHARD_ROOT 失败: %w", err)
	}
	for _, entry := range entries {
		same, err := sameSQLiteFile(cfg.AuditDatabasePath, entry)
		if err != nil {
			return Config{}, fmt.Errorf("校验 F3 SQLite 专库与 usage shard %q 隔离失败: %w", entry, err)
		}
		if same {
			return Config{}, fmt.Errorf("JUHE_AI_AUDIT_LOG_DATABASE_PATH 不得与 JUHE_AI_USAGE_SHARD_ROOT 中的 SQLite shard 共用文件")
		}
	}
	return cfg, nil
}

var usageShardRelativePath = regexp.MustCompile(`^(\d{4})[\\/](\d{2})[\\/](\d{2})[\\/]usage-(\d{8})-s\d+\.sqlite3$`)
var codexStateShardFileName = regexp.MustCompile(`^state-\d{3}\.sqlite3$`)

func requirePhysicalShardRoot(root, environmentName string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", environmentName, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s 不允许 symbolic link，无法证明物理隔离", environmentName)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s 必须是目录", environmentName)
	}
	return nil
}

// listCodexStateShardFiles follows Node's state-NNN.sqlite3 files. The root
// currently stores them directly, but recurse to fail closed if a future
// layout nests actual shard files. Any symlink below the root is rejected so
// that a root outside F3 cannot hide a physical alias to the audit database.
func listCodexStateShardFiles(root string) ([]string, error) {
	entries := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && path == root {
				return nil
			}
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Codex state shard 路径不允许 symbolic link: %q", path)
		}
		if entry.IsDir() || !codexStateShardFileName.MatchString(entry.Name()) {
			return nil
		}
		entries = append(entries, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// listUsageShardFiles follows Node's physical YYYY/MM/DD/usage-YYYYMMDD-sN
// layout.  WalkDir does not follow symlink directories; fail closed instead
// of silently skipping an alias that could point at F3's dedicated SQLite.
func listUsageShardFiles(root string) ([]string, error) {
	entries := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && path == root {
				return nil
			}
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("usage shard 路径不允许 symbolic link: %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		matches := usageShardRelativePath.FindStringSubmatch(filepath.ToSlash(relative))
		if matches == nil {
			return nil
		}
		if matches[1]+matches[2]+matches[3] != matches[4] {
			return fmt.Errorf("usage shard 日期目录与文件名不一致: %q", path)
		}
		entries = append(entries, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func parseDuration(name, value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s 必须是正 duration", name)
	}
	return parsed, nil
}

func parseBoundedInteger(name, value string, fallback, min, max int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || math.Trunc(parsed) != parsed || parsed < float64(min) || parsed > float64(max) {
		return 0, fmt.Errorf("%s 必须是 %d 到 %d 之间的整数", name, min, max)
	}
	return int(parsed), nil
}

func parseBoundedDecimal(name, value string, fallback, min, max float64, maxDecimals int) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	message := fmt.Sprintf("%s 必须是 %g 到 %g 之间且最多 %d 位小数的数字", name, min, max, maxDecimals)
	if !regexp.MustCompile(`^(?:\d+|\d*\.\d+)$`).MatchString(value) {
		return 0, fmt.Errorf("%s", message)
	}
	if point := strings.IndexByte(value, '.'); point >= 0 && len(value)-point-1 > maxDecimals {
		return 0, fmt.Errorf("%s", message)
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < min || parsed > max {
		return 0, fmt.Errorf("%s", message)
	}
	return parsed, nil
}

func (cfg Config) validateRetentionPolicy() error {
	if cfg.RetentionInterval < time.Second || cfg.RetentionInterval > 24*time.Hour {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL 必须为 1s 到 24h 的正 duration")
	}
	if cfg.RetentionBatchSize < 1 || cfg.RetentionBatchSize > 10000 {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE 必须是 1 到 10000 之间的整数")
	}
	if cfg.SuccessHotRetentionHours < 0 || cfg.SuccessHotRetentionHours > 168 {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS 必须是 0 到 168 之间的整数")
	}
	if math.IsNaN(cfg.SuccessSampleRate) || math.IsInf(cfg.SuccessSampleRate, 0) || cfg.SuccessSampleRate < 0 || cfg.SuccessSampleRate > 1 {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE 必须是 0 到 1 之间且最多 4 位小数的数字")
	}
	if cfg.SuccessRetentionDays < 0 || cfg.SuccessRetentionDays > 3650 {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS 必须是 0 到 3650 之间的整数")
	}
	if cfg.ProblemRetentionDays < 1 || cfg.ProblemRetentionDays > 3650 {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS 必须是 1 到 3650 之间的整数")
	}
	if (cfg.SuccessSampleRate == 0) != (cfg.SuccessRetentionDays == 0) {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE 和 JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS 必须同时为 0 或同时大于 0")
	}
	if cfg.SuccessRetentionDays > 0 && cfg.SuccessRetentionDays*24 < cfg.SuccessHotRetentionHours {
		return fmt.Errorf("JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS 必须覆盖 JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS")
	}
	return nil
}

// RetentionConfigAt produces the same cutoff policy Node uses for its audit
// maintenance. The zero-success-retention mode intentionally falls back to
// the hot window, matching auditSuccessRetentionCutoffIso in Node.
func (cfg Config) RetentionConfigAt(now time.Time) RetentionConfig {
	now = now.UTC()
	successCutoff := now.Add(-time.Duration(cfg.SuccessHotRetentionHours) * time.Hour)
	if cfg.SuccessRetentionDays > 0 {
		successCutoff = now.Add(-time.Duration(cfg.SuccessRetentionDays) * 24 * time.Hour)
	}
	problemCutoff := now.Add(-time.Duration(cfg.ProblemRetentionDays) * 24 * time.Hour)
	return RetentionConfig{
		SuccessHotCutoff:             now.Add(-time.Duration(cfg.SuccessHotRetentionHours) * time.Hour),
		SuccessCutoff:                successCutoff,
		FailureCutoff:                problemCutoff,
		ErrorGroupCutoff:             problemCutoff,
		SuccessSampleBucketThreshold: int(math.Round(cfg.SuccessSampleRate * 10000)),
		BatchSize:                    cfg.RetentionBatchSize,
	}
}

func sameSQLiteFile(left, right string) (bool, error) {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr != nil && !errors.Is(leftErr, os.ErrNotExist) {
		return false, leftErr
	}
	if rightErr != nil && !errors.Is(rightErr, os.ErrNotExist) {
		return false, rightErr
	}
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo), nil
	}
	leftPath, err := canonicalPath(left)
	if err != nil {
		return false, err
	}
	rightPath, err := canonicalPath(right)
	if err != nil {
		return false, err
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(leftPath, rightPath), nil
	}
	return leftPath == rightPath, nil
}

func canonicalPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("路径不能为空")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if _, err := os.Lstat(abs); err == nil {
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		return filepath.Clean(resolved), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, suffix := filepath.Dir(abs), []string{filepath.Base(abs)}
	for {
		if _, err := os.Lstat(parent); err == nil {
			resolved, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return "", err
			}
			return filepath.Clean(filepath.Join(append([]string{resolved}, suffix...)...)), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("没有可解析的真实父目录")
		}
		suffix = append([]string{filepath.Base(parent)}, suffix...)
		parent = next
	}
}

func pathWithin(root, candidate string) (bool, error) {
	rootPath, err := canonicalPath(root)
	if err != nil {
		return false, err
	}
	candidatePath, err := canonicalPath(candidate)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(rootPath, candidatePath)
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}
