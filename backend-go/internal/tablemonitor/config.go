package tablemonitor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultInterval             = time.Minute
	defaultOwnerLease           = 5 * time.Minute
	defaultRetentionDays        = 30
	defaultMaxTables            = 256
	defaultMaxConcurrentSources = 8
	defaultRetentionBatchSize   = 1000
	defaultRetentionMaxBatches  = 1000
)

func LoadConfig(getenv func(string) string) (Config, error) {
	instanceID := strings.TrimSpace(getenv("JUHE_AI_TABLE_MONITOR_INSTANCE_ID"))
	if instanceID == "" {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_INSTANCE_ID 是必填配置")
	}
	mode := Mode(strings.ToLower(strings.TrimSpace(firstNonEmpty(getenv("JUHE_AI_TABLE_MONITOR_STORE"), getenv("JUHE_AI_DATABASE_DRIVER"), string(ModeSQLite)))))
	if mode != ModeSQLite && mode != ModePostgres {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_STORE 必须为 sqlite 或 postgres")
	}
	interval, err := durationOrDefault("JUHE_AI_TABLE_MONITOR_INTERVAL", getenv("JUHE_AI_TABLE_MONITOR_INTERVAL"), defaultInterval)
	if err != nil {
		return Config{}, err
	}
	ownerLease, err := durationOrDefault("JUHE_AI_TABLE_MONITOR_OWNER_LEASE", getenv("JUHE_AI_TABLE_MONITOR_OWNER_LEASE"), defaultOwnerLease)
	if err != nil || ownerLease < 5*time.Second {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_OWNER_LEASE 必须是不少于 5s 的正 duration")
	}
	retentionDays, err := intOrDefault(getenv("JUHE_AI_TABLE_MONITOR_RETENTION_DAYS"), defaultRetentionDays, 1, 3650)
	if err != nil {
		return Config{}, err
	}
	maxTables, err := intOrDefault(getenv("JUHE_AI_TABLE_MONITOR_MAX_TABLES"), defaultMaxTables, 1, 10000)
	if err != nil {
		return Config{}, err
	}
	maxConcurrentSources, err := intOrDefault(getenv("JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES"), defaultMaxConcurrentSources, 1, 256)
	if err != nil {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES 无效: %w", err)
	}
	retentionBatchSize, err := intOrDefault(getenv("JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE"), defaultRetentionBatchSize, 1, 10000)
	if err != nil {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE 无效: %w", err)
	}
	retentionMaxBatches, err := intOrDefault(getenv("JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES"), defaultRetentionMaxBatches, 1, 100000)
	if err != nil {
		return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES 无效: %w", err)
	}
	cfg := Config{
		InstanceID:           instanceID,
		OwnerLease:           ownerLease,
		Mode:                 mode,
		OutputPath:           strings.TrimSpace(getenv("JUHE_AI_TABLE_MONITOR_DATABASE_PATH")),
		BusinessPath:         strings.TrimSpace(getenv("JUHE_AI_DATABASE_PATH")),
		DatasetPath:          strings.TrimSpace(getenv("JUHE_AI_DATASET_DATABASE_PATH")),
		UsageCatalogPath:     strings.TrimSpace(getenv("JUHE_AI_USAGE_CATALOG_DATABASE_PATH")),
		StatsPath:            strings.TrimSpace(getenv("JUHE_AI_STATS_DATABASE_PATH")),
		CodexShardRoot:       strings.TrimSpace(getenv("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")),
		PostgresURL:          strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL")),
		Interval:             interval,
		RetentionDays:        retentionDays,
		MaxTables:            maxTables,
		MaxConcurrentSources: maxConcurrentSources,
		RetentionBatchSize:   retentionBatchSize,
		RetentionMaxBatches:  retentionMaxBatches,
	}
	if mode == ModeSQLite {
		if cfg.OutputPath == "" {
			return Config{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_TABLE_MONITOR_DATABASE_PATH")
		}
		for role, path := range cfg.sourcePaths() {
			if path == "" {
				return Config{}, fmt.Errorf("sqlite 模式缺少 %s 数据库路径", role)
			}
			if sameSQLiteFile(cfg.OutputPath, path) {
				return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_DATABASE_PATH 不得与 %s 共用 SQLite 文件", role)
			}
		}
		if cfg.CodexShardRoot == "" {
			return Config{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")
		}
		entries, err := filepath.Glob(filepath.Join(cfg.CodexShardRoot, "*.sqlite3"))
		if err != nil {
			return Config{}, fmt.Errorf("枚举 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 失败: %w", err)
		}
		shardKeys := make(map[string]string, len(entries))
		for _, entry := range entries {
			if sameSQLiteFile(cfg.OutputPath, entry) {
				return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_DATABASE_PATH 不得与 Codex context SQLite shard 共用")
			}
			key := filepath.Base(entry)
			if prior, exists := shardKeys[key]; exists && !sameSQLiteFile(prior, entry) {
				return Config{}, fmt.Errorf("Codex context SQLite shard 文件名重复，无法形成稳定 table identity: %s", key)
			}
			shardKeys[key] = entry
		}
		if pathWithin(cfg.CodexShardRoot, cfg.OutputPath) {
			return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_DATABASE_PATH 不得放入 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")
		}
	} else if cfg.PostgresURL == "" {
		return Config{}, fmt.Errorf("postgres 模式缺少 JUHE_AI_POSTGRES_URL")
	}
	return cfg, nil
}

func (c Config) sourcePaths() map[string]string {
	return map[string]string{
		"JUHE_AI_DATABASE_PATH":               c.BusinessPath,
		"JUHE_AI_DATASET_DATABASE_PATH":       c.DatasetPath,
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH": c.UsageCatalogPath,
		"JUHE_AI_STATS_DATABASE_PATH":         c.StatsPath,
	}
}

func durationOrDefault(name, value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s 必须是正 duration", name)
	}
	return parsed, nil
}

func intOrDefault(value string, fallback, min, max int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("表监控数值必须在 %d..%d", min, max)
	}
	return parsed, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sameSQLiteFile(left, right string) bool {
	leftPath, leftErr := canonicalPath(left)
	rightPath, rightErr := canonicalPath(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if equalFilesystemPath(leftPath, rightPath) {
		return true
	}
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func pathWithin(root, candidate string) bool {
	rootAbs, rootErr := canonicalPath(root)
	candidateAbs, candidateErr := canonicalPath(candidate)
	if rootErr != nil || candidateErr != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	parent := filepath.Dir(abs)
	suffix := []string{filepath.Base(abs)}
	for parent != filepath.Dir(parent) {
		if resolved, err := filepath.EvalSymlinks(parent); err == nil {
			parts := append([]string{resolved}, suffix...)
			return filepath.Join(parts...), nil
		}
		suffix = append([]string{filepath.Base(parent)}, suffix...)
		parent = filepath.Dir(parent)
	}
	return abs, nil
}

func equalFilesystemPath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}
