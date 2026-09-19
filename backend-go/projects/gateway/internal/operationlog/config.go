package operationlog

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/datadir"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlitepath"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
)

type Config struct {
	Enabled              bool
	InstanceID           string
	Mode                 Mode
	DatabasePath         string
	BusinessSettingsPath string
	// BusinessDatabasePath 是业务库本体路径（组合根从 JUHE_AI_BUSINESS_DATABASE_PATH
	// 传入）：SQLite 模式下当镜像文件落后于业务库（运行期新建/改名的系统账户）
	// 时，F4 读模型用它兜底解析 actor 显示名。部署契约通常把
	// JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH 直接指向业务库文件，此时
	// 兜底句柄与镜像句柄同文件，store 会复用而不重复打开。
	BusinessDatabasePath string
	SQLiteIsolationPaths []string
	UsageShardRoot       string
	PostgresURL          string
	PostgresMaxOpenConns int
	PostgresMaxIdleConns int
	PostgresPool         *pgpool.Handle
	OwnerLease           time.Duration
	RetentionInterval    time.Duration
	RetentionDays        int
	RetentionBatchSize   int
}

const (
	defaultOwnerLease           = 30 * time.Second
	defaultRetentionInterval    = time.Minute
	defaultRetentionBatch       = 512
	defaultPostgresPoolSize     = 5096
	defaultPostgresMaxIdleConns = sqlpool.MaxIdleConns
)

func LoadConfig(getenv func(string) string) (Config, error) {
	modeRaw := strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_STORE"))
	if modeRaw == "" {
		// 2026-09-19 零配置默认：F4 store 缺省跟随业务库驱动
		// （JUHE_AI_DATABASE_DRIVER，postgres→postgres，否则 sqlite）——
		// system-api 组合根默认开启后 F4 producer 是管理面审计唯一写入口，
		// 空配置部署也必须有可用 store；显式配置优先。
		if strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_DATABASE_DRIVER")), "postgres") {
			modeRaw = string(ModePostgres)
		} else {
			modeRaw = string(ModeSQLite)
		}
	}
	postgresURL := strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_POSTGRES_URL"))
	ownerLease, err := durationOrDefault("JUHE_AI_OPERATION_LOG_OWNER_LEASE", getenv("JUHE_AI_OPERATION_LOG_OWNER_LEASE"), defaultOwnerLease)
	if err != nil || ownerLease < 5*time.Second {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_OWNER_LEASE must be a duration of at least 5s")
	}
	retentionInterval, err := durationOrDefault("JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL", getenv("JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL"), defaultRetentionInterval)
	if err != nil || retentionInterval < time.Second || retentionInterval > 24*time.Hour {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_RETENTION_INTERVAL must be a duration from 1s to 24h")
	}
	retentionBatch, err := intOrDefault("JUHE_AI_OPERATION_LOG_RETENTION_BATCH_SIZE", getenv("JUHE_AI_OPERATION_LOG_RETENTION_BATCH_SIZE"), defaultRetentionBatch, 1, 5096)
	if err != nil {
		return Config{}, err
	}
	postgresMaxOpen, err := positiveIntOrDefault("JUHE_AI_OPERATION_LOG_POSTGRES_MAX_OPEN_CONNS", getenv("JUHE_AI_OPERATION_LOG_POSTGRES_MAX_OPEN_CONNS"), defaultPostgresPoolSize)
	if err != nil {
		return Config{}, err
	}
	postgresMaxIdle, err := positiveIntOrDefault("JUHE_AI_OPERATION_LOG_POSTGRES_MAX_IDLE_CONNS", getenv("JUHE_AI_OPERATION_LOG_POSTGRES_MAX_IDLE_CONNS"), defaultPostgresMaxIdleConns)
	if err != nil {
		return Config{}, err
	}
	// 路径类 env 派生（2026-09-19 零配置约定，internal/datadir 固定名表）：
	// 未配置时落 <JUHE_AI_DATA_DIR=./data>/<固定名>，显式配置优先。PG 模式
	// OpenStore 不消费 DatabasePath/BusinessSettingsPath，派生值无副作用。
	dataDir := datadir.Dir(getenv)
	cfg := Config{Enabled: true, InstanceID: strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_INSTANCE_ID")), Mode: Mode(strings.ToLower(modeRaw)), DatabasePath: datadir.Path(getenv, dataDir, "JUHE_AI_OPERATION_LOG_DATABASE_PATH", datadir.OperationLogDatabase), BusinessSettingsPath: datadir.Path(getenv, dataDir, "JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH", datadir.BusinessDatabase), PostgresURL: postgresURL, PostgresMaxOpenConns: postgresMaxOpen, PostgresMaxIdleConns: postgresMaxIdle, OwnerLease: ownerLease, RetentionInterval: retentionInterval, RetentionDays: 365, RetentionBatchSize: retentionBatch}
	if cfg.Mode == ModePostgres {
		if err := sqlpool.ValidatePoolLimits(cfg.PostgresMaxOpenConns, cfg.PostgresMaxIdleConns); err != nil {
			return Config{}, fmt.Errorf("F4 PostgreSQL 连接池配置无效: %w", err)
		}
	}
	for _, key := range []string{"JUHE_AI_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_RUNTIME_LOG_DATABASE_PATH", "JUHE_AI_TABLE_MONITOR_DATABASE_PATH", "JUHE_AI_AUDIT_LOG_DATABASE_PATH", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH"} {
		// 未配置的隔离路径按 datadir 固定名表派生后并入隔离清单（2026-09-19）。
		value := getenv(key)
		if strings.TrimSpace(value) == "" {
			value = datadir.Path(getenv, dataDir, key, isolationFixedName(key))
		}
		for _, path := range strings.Split(value, ",") {
			if path = strings.TrimSpace(path); path != "" {
				cfg.SQLiteIsolationPaths = append(cfg.SQLiteIsolationPaths, path)
			}
		}
	}
	cfg.UsageShardRoot = datadir.Path(getenv, dataDir, "JUHE_AI_USAGE_SHARD_ROOT", datadir.UsageShardRoot)
	if cfg.InstanceID == "" {
		// 2026-09-19 零配置默认：os.Hostname()，空/错退化 "juhe-ai-gateway"。
		cfg.InstanceID = datadir.DefaultInstanceID()
	}
	if cfg.Mode != ModeSQLite && cfg.Mode != ModePostgres {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_STORE must be sqlite or postgres")
	}
	if cfg.Mode == ModeSQLite && (cfg.DatabasePath == "" || cfg.BusinessSettingsPath == "") {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_DATABASE_PATH and JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH are required for sqlite")
	}
	if cfg.Mode == ModeSQLite {
		if cfg.UsageShardRoot == "" {
			return Config{}, fmt.Errorf("JUHE_AI_USAGE_SHARD_ROOT is required for F4 SQLite physical isolation")
		}
		if err := validateUsageShardIsolation(cfg.DatabasePath, cfg.UsageShardRoot); err != nil {
			return Config{}, err
		}
	}
	if cfg.Mode == ModePostgres {
		if cfg.PostgresURL == "" {
			// 2026-09-19：PG 模式 F4 store URL 缺省回退主 JUHE_AI_POSTGRES_URL，
			// 仍为空时按原契约报错。
			cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
		}
		if cfg.PostgresURL == "" {
			return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_POSTGRES_URL is required for postgres")
		}
	}
	return cfg, nil
}

// isolationFixedName maps the shared business-family isolation env names onto
// their datadir fixed names (2026-09-19 zero-config table).
func isolationFixedName(envName string) string {
	switch envName {
	case "JUHE_AI_DATABASE_PATH":
		return datadir.BusinessDatabase
	case "JUHE_AI_DATASET_DATABASE_PATH":
		return datadir.DatasetDatabase
	case "JUHE_AI_RUNTIME_LOG_DATABASE_PATH":
		return datadir.RuntimeLogDatabase
	case "JUHE_AI_TABLE_MONITOR_DATABASE_PATH":
		return datadir.TableMonitorDatabase
	case "JUHE_AI_AUDIT_LOG_DATABASE_PATH":
		return datadir.AuditLogDatabase
	case "JUHE_AI_USAGE_CATALOG_DATABASE_PATH":
		return datadir.UsageCatalogDatabase
	case "JUHE_AI_STATS_DATABASE_PATH":
		return datadir.StatsDatabase
	}
	return ""
}

func durationOrDefault(name, value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func intOrDefault(name, value string, fallback, min, max int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", name, min, max)
	}
	return parsed, nil
}

func positiveIntOrDefault(name, value string, fallback int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return parsed, nil
}

func validateUsageShardIsolation(operationDatabasePath, usageShardRoot string) error {
	if err := sqlitepath.RequirePhysicalRoot(usageShardRoot, "JUHE_AI_USAGE_SHARD_ROOT"); err != nil {
		return err
	}
	within, err := sqlitepath.PathWithin(usageShardRoot, operationDatabasePath)
	if err != nil {
		return fmt.Errorf("validate F4 SQLite database against usage shard root: %w", err)
	}
	if within {
		return fmt.Errorf("JUHE_AI_OPERATION_LOG_DATABASE_PATH must not be inside JUHE_AI_USAGE_SHARD_ROOT")
	}
	entries, err := sqlitepath.ListUsageShardFiles(usageShardRoot)
	if err != nil {
		return fmt.Errorf("enumerate JUHE_AI_USAGE_SHARD_ROOT: %w", err)
	}
	for _, entry := range entries {
		same, err := sqlitepath.SameFile(operationDatabasePath, entry)
		if err != nil {
			return fmt.Errorf("validate F4 SQLite database against usage shard %q: %w", entry, err)
		}
		if same {
			return fmt.Errorf("JUHE_AI_OPERATION_LOG_DATABASE_PATH must not share a SQLite file with JUHE_AI_USAGE_SHARD_ROOT")
		}
	}
	return nil
}
