package accounthealth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultScanInterval     = time.Minute
	defaultOwnerLease       = 90 * time.Second
	defaultProbeTimeout     = 65 * time.Second
	defaultMaxResponseBytes = int64(256 * 1024)
)

// Config is deliberately opt-in.  A release cannot accidentally claim J1
// ownership merely because the jobs binary was upgraded.
type Config struct {
	Enabled             bool
	InstanceID          string
	Store               StoreConfig
	InputDirectory      string
	InputKeys           map[string][]byte
	InputSource         string
	BusinessPostgresURL string
	DirectInputLimit    int
	CredentialSecret    string
	ScanInterval        time.Duration
	OwnerLease          time.Duration
	ProbeTimeout        time.Duration
	MaxResponseBytes    int64
	MaxConcurrency      int
	Now                 func() time.Time
}

func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Config{Enabled: strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_ENABLED")), "true"), Now: time.Now}
	var err error
	if !cfg.Enabled {
		return cfg, nil
	}
	if !strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER")), "go") {
		return Config{}, errors.New("启用 J1 时 JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER 必须明确为 go")
	}
	cfg.InstanceID = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID"))
	if cfg.InstanceID == "" {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID 是必填配置")
	}
	mode := StoreMode(strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_STORE"))))
	if mode != StoreSQLite && mode != StorePostgres {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_STORE 必须为 sqlite 或 postgres")
	}
	cfg.Store = StoreConfig{Mode: mode, DatabasePath: strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH")), PostgresURL: strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL"))}
	if mode == StoreSQLite && cfg.Store.DatabasePath == "" {
		return Config{}, errors.New("sqlite 模式缺少 JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH")
	}
	if mode == StorePostgres && cfg.Store.PostgresURL == "" {
		return Config{}, errors.New("postgres 模式缺少 JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL")
	}
	cfg.InputDirectory = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY"))
	if cfg.InputDirectory == "" {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY 是必填配置")
	}
	if mode == StoreSQLite {
		if err := validateSQLiteIsolation(cfg.Store.DatabasePath, cfg.InputDirectory, getenv); err != nil {
			return Config{}, err
		}
	}
	cfg.InputSource = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE")))
	if cfg.InputSource == "" {
		cfg.InputSource = "files"
	}
	if cfg.InputSource != "files" && cfg.InputSource != "postgres" {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE 必须为 files 或 postgres")
	}
	if cfg.InputSource == "postgres" {
		if mode != StorePostgres {
			return Config{}, errors.New("PG direct input 只允许与 postgres jobs store 一起启用")
		}
		cfg.BusinessPostgresURL = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL"))
		if cfg.BusinessPostgresURL == "" {
			return Config{}, errors.New("postgres direct input 缺少 JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL")
		}
		if cfg.DirectInputLimit, err = configInt(getenv, "JUHE_AI_ACCOUNT_HEALTH_DIRECT_INPUT_LIMIT", 256, 1, 1024); err != nil {
			return Config{}, err
		}
	}
	keyText := strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY"))
	if keyText == "" {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY 是必填配置")
	}
	key, err := base64.RawURLEncoding.DecodeString(keyText)
	if err != nil || len(key) < 32 {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY 必须是至少 32 字节的 base64url")
	}
	keyID := strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY_ID"))
	if keyID == "" {
		keyID = "runtime-v1"
	}
	cfg.InputKeys = map[string][]byte{keyID: key}
	cfg.CredentialSecret = strings.TrimSpace(getenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"))
	if cfg.CredentialSecret == "" {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET 是必填配置")
	}
	if cfg.ScanInterval, err = configDuration(getenv, "JUHE_AI_ACCOUNT_HEALTH_SCAN_INTERVAL", defaultScanInterval, 5*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.OwnerLease, err = configDuration(getenv, "JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE", defaultOwnerLease, 15*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.ProbeTimeout, err = configDuration(getenv, "JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT", defaultProbeTimeout, time.Second); err != nil {
		return Config{}, err
	}
	if cfg.OwnerLease <= cfg.ProbeTimeout {
		return Config{}, errors.New("JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE 必须大于 JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT")
	}
	if cfg.MaxResponseBytes, err = configInt64(getenv, "JUHE_AI_ACCOUNT_HEALTH_MAX_RESPONSE_BYTES", defaultMaxResponseBytes, 1, 16*1024*1024); err != nil {
		return Config{}, err
	}
	if cfg.MaxConcurrency, err = configInt(getenv, "JUHE_AI_ACCOUNT_HEALTH_MAX_CONCURRENCY", 4, 1, 64); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateSQLiteIsolation(storePath, inputDirectory string, getenv func(string) string) error {
	store, err := canonicalPath(storePath)
	if err != nil {
		return fmt.Errorf("解析 J1 SQLite store 路径失败: %w", err)
	}
	for _, name := range []string{"JUHE_AI_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH", "JUHE_AI_RUNTIME_LOG_DATABASE_PATH", "JUHE_AI_TABLE_MONITOR_DATABASE_PATH"} {
		other := strings.TrimSpace(getenv(name))
		if other == "" {
			continue
		}
		candidate, err := canonicalPath(other)
		if err != nil {
			return fmt.Errorf("解析 %s 失败: %w", name, err)
		}
		if equalPath(store, candidate) {
			return fmt.Errorf("JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH 不得与 %s 共用 SQLite 文件", name)
		}
	}
	input, err := canonicalPath(inputDirectory)
	if err != nil {
		return fmt.Errorf("解析 J1 input 目录失败: %w", err)
	}
	relative, err := filepath.Rel(input, store)
	if err != nil {
		return err
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return errors.New("JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH 不得放入 input 目录")
	}
	return nil
}

func canonicalPath(value string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func equalPath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func configDuration(getenv func(string) string, name string, fallback, minimum time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < minimum {
		return 0, fmt.Errorf("%s 必须是不少于 %s 的 duration", name, minimum)
	}
	return parsed, nil
}

func configInt(getenv func(string) string, name string, fallback, minimum, maximum int) (int, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s 必须在 %d..%d", name, minimum, maximum)
	}
	return parsed, nil
}

func configInt64(getenv func(string) string, name string, fallback, minimum, maximum int64) (int64, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s 必须在 %d..%d", name, minimum, maximum)
	}
	return parsed, nil
}
