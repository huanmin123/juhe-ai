package runtimelog

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPollInterval      = time.Second
	defaultRetentionInterval = time.Hour
	defaultRetentionDays     = 14
	defaultLogRetentionDays  = 30
	defaultLogMaxFiles       = 500
	defaultBatchSize         = 500
	defaultOwnerLease        = 30 * time.Second
	defaultPostgresMaxConns  = 1000
	defaultPostgresMinConns  = 0
)

func LoadConfig(getenv func(string) string) (Config, error) {
	ownerInstance := strings.TrimSpace(getenv("JUHE_AI_RUNTIME_LOG_INSTANCE_ID"))
	if ownerInstance == "" {
		return Config{}, fmt.Errorf("JUHE_AI_RUNTIME_LOG_INSTANCE_ID 是运行日志索引实例的必填配置")
	}
	ownerLease, err := durationOrDefault("JUHE_AI_RUNTIME_LOG_OWNER_LEASE", getenv("JUHE_AI_RUNTIME_LOG_OWNER_LEASE"), defaultOwnerLease)
	if err != nil {
		return Config{}, err
	}
	mode, err := parseMode(firstNonEmpty(getenv("JUHE_AI_RUNTIME_LOG_STORE"), getenv("JUHE_AI_DATABASE_DRIVER")))
	if err != nil {
		return Config{}, err
	}
	fileEnabled, err := parseBool("JUHE_AI_LOG_FILE_ENABLED", getenv("JUHE_AI_LOG_FILE_ENABLED"), true)
	if err != nil {
		return Config{}, err
	}
	once, err := parseBool("JUHE_AI_RUNTIME_LOG_ONCE", getenv("JUHE_AI_RUNTIME_LOG_ONCE"), false)
	if err != nil {
		return Config{}, err
	}
	pollInterval, err := durationOrDefault("JUHE_AI_RUNTIME_LOG_POLL_INTERVAL", getenv("JUHE_AI_RUNTIME_LOG_POLL_INTERVAL"), defaultPollInterval)
	if err != nil {
		return Config{}, err
	}
	retentionInterval, err := durationOrDefault("JUHE_AI_RUNTIME_LOG_RETENTION_INTERVAL", getenv("JUHE_AI_RUNTIME_LOG_RETENTION_INTERVAL"), defaultRetentionInterval)
	if err != nil {
		return Config{}, err
	}
	retentionDays, err := intOrDefault("JUHE_AI_RUNTIME_LOG_RETENTION_DAYS", getenv("JUHE_AI_RUNTIME_LOG_RETENTION_DAYS"), defaultRetentionDays, 1, 90)
	if err != nil {
		return Config{}, err
	}
	logRetentionDays, err := intOrDefault("JUHE_AI_LOG_RETENTION_DAYS", getenv("JUHE_AI_LOG_RETENTION_DAYS"), defaultLogRetentionDays, 1, 30)
	if err != nil {
		return Config{}, err
	}
	logMaxFiles, err := intOrDefault("JUHE_AI_LOG_MAX_FILES", getenv("JUHE_AI_LOG_MAX_FILES"), defaultLogMaxFiles, 1, 500)
	if err != nil {
		return Config{}, err
	}
	batchSize, err := intOrDefault("JUHE_AI_RUNTIME_LOG_BATCH_SIZE", getenv("JUHE_AI_RUNTIME_LOG_BATCH_SIZE"), defaultBatchSize, 1, 100000)
	if err != nil {
		return Config{}, err
	}
	postgresMaxConns, err := positiveIntOrDefault("JUHE_AI_RUNTIME_LOG_POSTGRES_MAX_CONNS", getenv("JUHE_AI_RUNTIME_LOG_POSTGRES_MAX_CONNS"), defaultPostgresMaxConns)
	if err != nil {
		return Config{}, err
	}
	postgresMinConns, err := positiveIntOrDefault("JUHE_AI_RUNTIME_LOG_POSTGRES_MIN_CONNS", getenv("JUHE_AI_RUNTIME_LOG_POSTGRES_MIN_CONNS"), defaultPostgresMinConns)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		OwnerID:                fmt.Sprintf("%s:%d", ownerInstance, os.Getpid()),
		OwnerLease:             ownerLease,
		Mode:                   mode,
		DatasetPath:            strings.TrimSpace(getenv("JUHE_AI_DATASET_DATABASE_PATH")),
		RuntimeLogDatabasePath: strings.TrimSpace(getenv("JUHE_AI_RUNTIME_LOG_DATABASE_PATH")),
		BusinessPath:           strings.TrimSpace(getenv("JUHE_AI_DATABASE_PATH")),
		UsageCatalogPath:       strings.TrimSpace(getenv("JUHE_AI_USAGE_CATALOG_DATABASE_PATH")),
		StatsPath:              strings.TrimSpace(getenv("JUHE_AI_STATS_DATABASE_PATH")),
		CodexShardRoot:         strings.TrimSpace(getenv("JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")),
		PostgresURL:            firstNonEmpty(getenv("JUHE_AI_RUNTIME_LOG_POSTGRES_URL"), getenv("JUHE_AI_POSTGRES_URL")),
		PostgresMaxConns:       postgresMaxConns,
		PostgresMinConns:       postgresMinConns,
		LogDirectory:           strings.TrimSpace(getenv("JUHE_AI_LOG_DIR")),
		FileEnabled:            fileEnabled,
		Once:                   once,
		PollInterval:           pollInterval,
		RetentionInterval:      retentionInterval,
		RetentionDays:          retentionDays,
		LogRetentionDays:       logRetentionDays,
		LogMaxFiles:            logMaxFiles,
		BatchSize:              batchSize,
	}
	if config.LogDirectory == "" {
		return Config{}, fmt.Errorf("JUHE_AI_LOG_DIR 是运行日志索引 Go owner 的必填配置")
	}
	if !config.FileEnabled {
		return Config{}, fmt.Errorf("运行日志索引进程要求 JUHE_AI_LOG_FILE_ENABLED=true")
	}
	switch config.Mode {
	case ModeSQLite:
		if config.RuntimeLogDatabasePath == "" {
			return Config{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_RUNTIME_LOG_DATABASE_PATH")
		}
		if err := validateSQLiteIsolation(config, strings.TrimSpace(getenv("JUHE_AI_TABLE_MONITOR_DATABASE_PATH"))); err != nil {
			return Config{}, err
		}
	case ModePostgres:
		if config.PostgresURL == "" {
			return Config{}, fmt.Errorf("postgres 模式缺少 JUHE_AI_RUNTIME_LOG_POSTGRES_URL 或 JUHE_AI_POSTGRES_URL")
		}
	}
	return config, nil
}

func validateSQLiteIsolation(config Config, tableMonitorPath string) error {
	candidates := []struct {
		name string
		path string
	}{
		{name: "JUHE_AI_TABLE_MONITOR_DATABASE_PATH", path: tableMonitorPath},
		{name: "JUHE_AI_DATABASE_PATH", path: config.BusinessPath},
		{name: "JUHE_AI_DATASET_DATABASE_PATH", path: config.DatasetPath},
		{name: "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", path: config.UsageCatalogPath},
		{name: "JUHE_AI_STATS_DATABASE_PATH", path: config.StatsPath},
	}
	for _, candidate := range candidates {
		if candidate.path == "" {
			return fmt.Errorf("sqlite 模式缺少 %s，无法验证运行日志专库隔离", candidate.name)
		}
		same, err := sameSQLitePath(candidate.path, config.RuntimeLogDatabasePath)
		if err != nil {
			return fmt.Errorf("校验 JUHE_AI_RUNTIME_LOG_DATABASE_PATH 与 %s 的 SQLite 隔离失败: %w", candidate.name, err)
		}
		if same {
			return fmt.Errorf("JUHE_AI_RUNTIME_LOG_DATABASE_PATH 不得与 %s 指向同一个 SQLite 文件", candidate.name)
		}
	}
	if config.CodexShardRoot == "" {
		return fmt.Errorf("sqlite 模式缺少 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT，无法验证运行日志专库隔离")
	}
	within, err := sqlitePathWithin(config.CodexShardRoot, config.RuntimeLogDatabasePath)
	if err != nil {
		return fmt.Errorf("校验 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 与运行日志 SQLite 隔离失败: %w", err)
	}
	if within {
		return fmt.Errorf("JUHE_AI_RUNTIME_LOG_DATABASE_PATH 不得放入 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT")
	}
	entries, err := filepath.Glob(filepath.Join(config.CodexShardRoot, "*.sqlite3"))
	if err != nil {
		return fmt.Errorf("枚举 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 失败: %w", err)
	}
	for _, entry := range entries {
		same, err := sameSQLitePath(entry, config.RuntimeLogDatabasePath)
		if err != nil {
			return fmt.Errorf("校验 Codex context SQLite shard %q 与运行日志 SQLite 隔离失败: %w", entry, err)
		}
		if same {
			return fmt.Errorf("JUHE_AI_RUNTIME_LOG_DATABASE_PATH 不得与 JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT 中的 SQLite shard 指向同一个文件")
		}
	}
	return nil
}

func parseMode(value string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(value))) {
	case ModeSQLite:
		return ModeSQLite, nil
	case ModePostgres:
		return ModePostgres, nil
	default:
		return "", fmt.Errorf("JUHE_AI_RUNTIME_LOG_STORE 或 JUHE_AI_DATABASE_DRIVER 必须为 sqlite 或 postgres")
	}
}

func parseBool(name string, value string, defaultValue bool) (bool, error) {
	if strings.TrimSpace(value) == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s 必须是布尔值: %w", name, err)
	}
	return parsed, nil
}

func durationOrDefault(name string, value string, defaultValue time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return defaultValue, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s 必须是正 duration", name)
	}
	return parsed, nil
}

func intOrDefault(name string, value string, defaultValue int, min int, max int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("%s 必须在 %d..%d", name, min, max)
	}
	return parsed, nil
}

func positiveIntOrDefault(name string, value string, defaultValue int) (int, error) {
	if strings.TrimSpace(value) == "" {
		return defaultValue, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("%s 必须是非负整数", name)
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
