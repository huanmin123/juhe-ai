package operationlog

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Enabled              bool
	InstanceID           string
	Mode                 Mode
	DatabasePath         string
	BusinessSettingsPath string
	SQLiteIsolationPaths []string
	PostgresURL          string
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

func LoadConfig(getenv func(string) string) (Config, error) {
	modeRaw := strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_STORE"))
	listen := strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS"))
	if modeRaw == "" && listen == "" {
		return Config{}, nil
	}
	if modeRaw == "" || listen == "" {
		return Config{}, fmt.Errorf("F4 operation log store and input listener must be configured together")
	}
	cfg := Config{Enabled: true, InstanceID: strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_INSTANCE_ID")), Mode: Mode(strings.ToLower(modeRaw)), DatabasePath: strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_DATABASE_PATH")), BusinessSettingsPath: strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH")), PostgresURL: strings.TrimSpace(getenv("JUHE_AI_OPERATION_LOG_POSTGRES_URL")), OwnerLease: 30 * time.Second, RetentionInterval: time.Minute, RetentionDays: 365, RetentionBatchSize: 1000}
	for _, key := range []string{"JUHE_AI_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_RUNTIME_LOG_DATABASE_PATH", "JUHE_AI_TABLE_MONITOR_DATABASE_PATH", "JUHE_AI_AUDIT_LOG_DATABASE_PATH", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH", "JUHE_AI_USAGE_SHARD_PATHS"} {
		for _, path := range strings.Split(getenv(key), ",") {
			if path = strings.TrimSpace(path); path != "" {
				cfg.SQLiteIsolationPaths = append(cfg.SQLiteIsolationPaths, path)
			}
		}
	}
	if cfg.InstanceID == "" {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_INSTANCE_ID is required")
	}
	if cfg.Mode != ModeSQLite && cfg.Mode != ModePostgres {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_STORE must be sqlite or postgres")
	}
	if cfg.Mode == ModeSQLite && (cfg.DatabasePath == "" || cfg.BusinessSettingsPath == "") {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_DATABASE_PATH and JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH are required for sqlite")
	}
	if cfg.Mode == ModePostgres && cfg.PostgresURL == "" {
		return Config{}, fmt.Errorf("JUHE_AI_OPERATION_LOG_POSTGRES_URL is required for postgres")
	}
	return cfg, nil
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
