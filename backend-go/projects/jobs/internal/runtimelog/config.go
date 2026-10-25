package runtimelog

import (
	"fmt"
	"os"
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
	// STORE 恒 postgres（sqlite store 形态已随存储形态收口物理删除；
	// JUHE_AI_RUNTIME_LOG_STORE env 退役）。PG 连接串无法凭空默认。
	mode := ModePostgres
	// 2026-09-21 起文件日志为常驻能力，JUHE_AI_LOG_FILE_ENABLED 开关移除。
	fileEnabled := true
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
		PostgresURL:      strings.TrimSpace(getenv("JUHE_AI_RUNTIME_LOG_POSTGRES_URL")),
		PostgresMaxConns: postgresMaxConns,
		PostgresMinConns: postgresMinConns,
		LogDirectory:     strings.TrimSpace(getenv("JUHE_AI_LOG_DIR")),
		FileEnabled:      fileEnabled,
		Once:             once,
		PollInterval:      pollInterval,
		RetentionInterval: retentionInterval,
		RetentionDays:     retentionDays,
		LogRetentionDays:  logRetentionDays,
		LogMaxFiles:       logMaxFiles,
		BatchSize:         batchSize,
	}
	if config.LogDirectory == "" {
		// JUHE_AI_LOG_DIR 是路径类 env 但不在固定名表内；为满足空环境可启动，
		// 派生 <DATA_DIR>/logs 并代建目录（索引器/cleanup 直接 ReadDir 该目录）。
		config.LogDirectory = datadir.Path(getenv, "JUHE_AI_LOG_DIR", "logs")
		if err := os.MkdirAll(config.LogDirectory, 0o755); err != nil {
			return Config{}, fmt.Errorf("创建运行日志目录 %s 失败: %w", config.LogDirectory, err)
		}
	}
	// 2026-09-21 起文件日志恒开（JUHE_AI_LOG_FILE_ENABLED 开关移除），
	// FileEnabled 恒为 true，原「索引进程要求开关为 true」守卫不可达已删除。
	if config.PostgresURL == "" {
		return Config{}, fmt.Errorf("postgres 模式缺少 JUHE_AI_RUNTIME_LOG_POSTGRES_URL")
	}
	return config, nil
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
