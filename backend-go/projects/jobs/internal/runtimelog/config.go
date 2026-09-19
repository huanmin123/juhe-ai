package runtimelog

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/datadir"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
)

const (
	defaultPollInterval      = time.Second
	defaultRetentionInterval = time.Hour
	defaultRetentionDays     = 14
	defaultLogRetentionDays  = 30
	defaultLogMaxFiles       = 500
	defaultBatchSize         = 512
	defaultOwnerLease        = 30 * time.Second
	defaultPostgresMaxConns  = 16
	// Do not prewarm PostgreSQL connections during process startup. Pools
	// create connections on demand and recycle idle ones via MaxConnIdleTime.
	defaultPostgresMinConns = 0
)

func LoadConfig(getenv func(string) string) (Config, error) {
	// INSTANCE_ID 缺省取 os.Hostname()（2026-09-19 零配置决策；hostname 不可
	// 用或为空时回落固定名，与 accounthealth 同约定）。
	ownerInstance := strings.TrimSpace(getenv("JUHE_AI_RUNTIME_LOG_INSTANCE_ID"))
	if ownerInstance == "" {
		hostname, hostErr := os.Hostname()
		if hostErr != nil || strings.TrimSpace(hostname) == "" {
			hostname = "juhe-ai-jobs"
		}
		ownerInstance = hostname
	}
	ownerLease, err := durationOrDefault("JUHE_AI_RUNTIME_LOG_OWNER_LEASE", getenv("JUHE_AI_RUNTIME_LOG_OWNER_LEASE"), defaultOwnerLease)
	if err != nil {
		return Config{}, err
	}
	// STORE 缺省跟随 JUHE_AI_DATABASE_DRIVER（与 jobs 组合根 loadWorkerConfig
	// 同一依据，2026-09-19 零配置决策）：postgres → ModePostgres，否则
	// ModeSQLite；显式配置仍必须为 sqlite|postgres。
	mode, err := parseStoreMode(getenv)
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
	batchSize, err := intOrDefault("JUHE_AI_RUNTIME_LOG_BATCH_SIZE", getenv("JUHE_AI_RUNTIME_LOG_BATCH_SIZE"), defaultBatchSize, 1, 5096)
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
	if postgresMinConns > sqlpool.MaxIdleConns {
		return Config{}, fmt.Errorf("JUHE_AI_RUNTIME_LOG_POSTGRES_MIN_CONNS 不得大于 %d", sqlpool.MaxIdleConns)
	}
	config := Config{
		OwnerID:    fmt.Sprintf("%s:%d", ownerInstance, os.Getpid()),
		OwnerLease: ownerLease,
		Mode:       mode,
		// 路径类 env 按 DATA_DIR 约定派生（2026-09-19 零配置决策，
		// internal/datadir）：显式配置优先，固定名与 gateway/worker_config 一致。
		DatasetPath:            datadir.Path(getenv, "JUHE_AI_DATASET_DATABASE_PATH", "dataset.sqlite3"),
		RuntimeLogDatabasePath: datadir.Path(getenv, "JUHE_AI_RUNTIME_LOG_DATABASE_PATH", "runtime-log.sqlite3"),
		BusinessPath:           datadir.Path(getenv, "JUHE_AI_DATABASE_PATH", "business.sqlite3"),
		UsageCatalogPath:       datadir.Path(getenv, "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "usage-catalog.sqlite3"),
		StatsPath:              datadir.Path(getenv, "JUHE_AI_STATS_DATABASE_PATH", "stats.sqlite3"),
		CodexShardRoot:         datadir.Path(getenv, "JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT", "codex-context/state-shards"),
		PostgresURL:            strings.TrimSpace(getenv("JUHE_AI_RUNTIME_LOG_POSTGRES_URL")),
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
		// JUHE_AI_LOG_DIR 是路径类 env 但不在固定名表内；为满足空环境可启动，
		// 派生 <DATA_DIR>/logs 并代建目录（索引器/cleanup 直接 ReadDir 该目录）。
		config.LogDirectory = datadir.Path(getenv, "JUHE_AI_LOG_DIR", "logs")
		if err := os.MkdirAll(config.LogDirectory, 0o755); err != nil {
			return Config{}, fmt.Errorf("创建运行日志目录 %s 失败: %w", config.LogDirectory, err)
		}
	}
	if !config.FileEnabled {
		return Config{}, fmt.Errorf("运行日志索引进程要求 JUHE_AI_LOG_FILE_ENABLED=true")
	}
	switch config.Mode {
	case ModeSQLite:
		if config.RuntimeLogDatabasePath == "" {
			return Config{}, fmt.Errorf("sqlite 模式缺少 JUHE_AI_RUNTIME_LOG_DATABASE_PATH")
		}
		if err := validateSQLiteIsolation(config, datadir.Path(getenv, "JUHE_AI_TABLE_MONITOR_DATABASE_PATH", "table-monitor.sqlite3")); err != nil {
			return Config{}, err
		}
	case ModePostgres:
		if config.PostgresURL == "" {
			return Config{}, fmt.Errorf("postgres 模式缺少 JUHE_AI_RUNTIME_LOG_POSTGRES_URL")
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

// parseStoreMode 解析 JUHE_AI_RUNTIME_LOG_STORE：缺省跟随
// JUHE_AI_DATABASE_DRIVER（postgres → ModePostgres，否则 ModeSQLite）；显式
// 配置必须为 sqlite|postgres。
func parseStoreMode(getenv func(string) string) (Mode, error) {
	value := strings.TrimSpace(getenv("JUHE_AI_RUNTIME_LOG_STORE"))
	if value == "" {
		if strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_DATABASE_DRIVER")), "postgres") {
			return ModePostgres, nil
		}
		return ModeSQLite, nil
	}
	switch Mode(strings.ToLower(value)) {
	case ModeSQLite:
		return ModeSQLite, nil
	case ModePostgres:
		return ModePostgres, nil
	default:
		return "", fmt.Errorf("JUHE_AI_RUNTIME_LOG_STORE 必须为 sqlite 或 postgres")
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
