package operationlog

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlitepath"
)

type Config struct {
	Enabled              bool
	InstanceID           string
	Mode                 Mode
	DatabasePath         string
	BusinessSettingsPath string
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
type InputServerConfig struct {
	ListenAddress  string
	SharedSecret   string
	MaxBytes       int64
	RequestTimeout time.Duration
	ReplayWindow   time.Duration
}

const defaultInputMaxBytes int64 = 4 << 20

const (
	defaultOwnerLease        = 30 * time.Second
	defaultRetentionInterval = time.Minute
	defaultRetentionBatch    = 512
	defaultPostgresPoolSize  = 5096
)

func LoadConfig(getenv func(string) string) (Config, error) {
	modeRaw := strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_STORE"))
	listen := strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS"))
	if modeRaw == "" && listen == "" {
		return Config{}, nil
	}
	if modeRaw == "" || listen == "" {
		return Config{}, fmt.Errorf("F4 operation log store and input listener must be configured together")
	}
	postgresURL := strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_POSTGRES_URL"))
	if postgresURL == "" {
		postgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
	}
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
	postgresMaxIdle, err := positiveIntOrDefault("JUHE_AI_OPERATION_LOG_POSTGRES_MAX_IDLE_CONNS", getenv("JUHE_AI_OPERATION_LOG_POSTGRES_MAX_IDLE_CONNS"), defaultPostgresPoolSize)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{Enabled: true, InstanceID: strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_INSTANCE_ID")), Mode: Mode(strings.ToLower(modeRaw)), DatabasePath: strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_DATABASE_PATH")), BusinessSettingsPath: strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH")), PostgresURL: postgresURL, PostgresMaxOpenConns: postgresMaxOpen, PostgresMaxIdleConns: postgresMaxIdle, OwnerLease: ownerLease, RetentionInterval: retentionInterval, RetentionDays: 365, RetentionBatchSize: retentionBatch}
	for _, key := range []string{"JUHE_AI_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_RUNTIME_LOG_DATABASE_PATH", "JUHE_AI_TABLE_MONITOR_DATABASE_PATH", "JUHE_AI_AUDIT_LOG_DATABASE_PATH", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH"} {
		for _, path := range strings.Split(getenv(key), ",") {
			if path = strings.TrimSpace(path); path != "" {
				cfg.SQLiteIsolationPaths = append(cfg.SQLiteIsolationPaths, path)
			}
		}
	}
	cfg.UsageShardRoot = strings.TrimSpace(getenv("JUHE_AI_USAGE_SHARD_ROOT"))
	if cfg.InstanceID == "" {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_INSTANCE_ID is required")
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
	if cfg.Mode == ModePostgres && cfg.PostgresURL == "" {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_POSTGRES_URL or JUHE_AI_POSTGRES_URL is required for postgres")
	}
	return cfg, nil
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
func LoadInputServerConfig(getenv func(string) string) (InputServerConfig, error) {
	cfg := InputServerConfig{ListenAddress: strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS")), SharedSecret: strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_INPUT_SECRET")), MaxBytes: defaultInputMaxBytes, RequestTimeout: 5 * time.Second, ReplayWindow: 5 * time.Minute}
	if err := validateLoopbackAddress(cfg.ListenAddress); err != nil {
		return InputServerConfig{}, err
	}
	if cfg.SharedSecret == "" {
		return InputServerConfig{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_INPUT_SECRET is required")
	}
	if strings.EqualFold(getenv("NODE_ENV"), "production") && len(cfg.SharedSecret) < 32 {
		return InputServerConfig{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_INPUT_SECRET must be at least 32 characters in production")
	}
	return cfg, nil
}
func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return fmt.Errorf("input listener must be loopback IP:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("input listener port invalid")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("input listener must be loopback")
	}
	return nil
}
