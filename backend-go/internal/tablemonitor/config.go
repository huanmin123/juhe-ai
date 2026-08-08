package tablemonitor

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultInterval      = time.Minute
	defaultOwnerLease    = 5 * time.Minute
	defaultRetentionDays = 30
	defaultMaxTables     = 256
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
	interval, err := durationOrDefault(getenv("JUHE_AI_TABLE_MONITOR_INTERVAL"), defaultInterval)
	if err != nil {
		return Config{}, err
	}
	ownerLease, err := durationOrDefault(getenv("JUHE_AI_TABLE_MONITOR_OWNER_LEASE"), defaultOwnerLease)
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
	cfg := Config{
		InstanceID:       instanceID,
		OwnerLease:       ownerLease,
		Mode:             mode,
		OutputPath:       strings.TrimSpace(getenv("JUHE_AI_TABLE_MONITOR_DATABASE_PATH")),
		BusinessPath:     strings.TrimSpace(getenv("JUHE_AI_DATABASE_PATH")),
		DatasetPath:      strings.TrimSpace(getenv("JUHE_AI_DATASET_DATABASE_PATH")),
		UsageCatalogPath: strings.TrimSpace(getenv("JUHE_AI_USAGE_CATALOG_DATABASE_PATH")),
		StatsPath:        strings.TrimSpace(getenv("JUHE_AI_STATS_DATABASE_PATH")),
		CodexShardRoot:   strings.TrimSpace(getenv("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")),
		PostgresURL:      strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL")),
		Interval:         interval,
		RetentionDays:    retentionDays,
		MaxTables:        maxTables,
	}
	if mode == ModeSQLite {
		if cfg.OutputPath == "" {
			return Config{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_TABLE_MONITOR_DATABASE_PATH")
		}
		for role, path := range cfg.sourcePaths() {
			if path == "" {
				return Config{}, fmt.Errorf("sqlite 模式缺少 %s 数据库路径", role)
			}
			if samePath(cfg.OutputPath, path) {
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
		for _, entry := range entries {
			if samePath(cfg.OutputPath, entry) {
				return Config{}, fmt.Errorf("JUHE_AI_TABLE_MONITOR_DATABASE_PATH 不得与 Codex context SQLite shard 共用")
			}
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

func durationOrDefault(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("JUHE_AI_TABLE_MONITOR_INTERVAL 必须是正 duration")
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

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
}

func pathWithin(root, candidate string) bool {
	rootAbs, rootErr := filepath.Abs(root)
	candidateAbs, candidateErr := filepath.Abs(candidate)
	if rootErr != nil || candidateErr != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}
