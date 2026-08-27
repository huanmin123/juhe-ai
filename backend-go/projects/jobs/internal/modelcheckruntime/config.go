package modelcheckruntime

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// RuntimeConfig keeps the historical shape for the reusable J3b packages.
// The jobs executable is fail-closed under solution A and cannot own J3b.
type RuntimeConfig struct {
	Enabled              bool
	InstanceID           string
	StoreMode            string
	JobsDatabasePath     string
	DatasetDatabasePath  string
	BusinessDatabasePath string
	JobsPostgresURL      string
	BusinessPostgresURL  string
	CredentialSecret     string
	IdentitySecret       string
	ManagementAddress    string
	ProbeSetVersion      string
	Deadline             time.Duration
	Heartbeat            time.Duration
	RetryAttempts        int
}

func LoadConfig(getenv func(string) string) (RuntimeConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := RuntimeConfig{Enabled: strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_ENABLED")), "true")}
	if !cfg.Enabled {
		return cfg, nil
	}
	if !strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_JOBS_OWNER")), "go") {
		return RuntimeConfig{}, errors.New("启用 J3b 时 JUHE_AI_MODEL_CHECK_JOBS_OWNER 必须明确为 go")
	}
	cfg.InstanceID = strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_INSTANCE_ID"))
	if cfg.InstanceID == "" {
		return RuntimeConfig{}, errors.New("JUHE_AI_MODEL_CHECK_INSTANCE_ID 是必填配置")
	}
	cfg.StoreMode = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_STORE")))
	if cfg.StoreMode != "sqlite" && cfg.StoreMode != "postgres" {
		return RuntimeConfig{}, errors.New("JUHE_AI_MODEL_CHECK_STORE 必须为 sqlite 或 postgres")
	}
	cfg.JobsDatabasePath = strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_JOBS_DATABASE_PATH"))
	cfg.DatasetDatabasePath = strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_DATASET_DATABASE_PATH"))
	cfg.BusinessDatabasePath = strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_BUSINESS_DATABASE_PATH"))
	cfg.JobsPostgresURL = strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_POSTGRES_URL"))
	cfg.BusinessPostgresURL = strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_BUSINESS_POSTGRES_URL"))
	cfg.CredentialSecret = strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_CREDENTIAL_SECRET"))
	cfg.IdentitySecret = strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_IDENTITY_SECRET"))
	cfg.ManagementAddress = strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_MANAGEMENT_LISTEN_ADDRESS"))
	if cfg.ManagementAddress == "" {
		cfg.ManagementAddress = "127.0.0.1:3308"
	}
	cfg.ProbeSetVersion = strings.TrimSpace(getenv("JUHE_AI_MODEL_CHECK_PROBE_SET_VERSION"))
	if cfg.ProbeSetVersion == "" {
		cfg.ProbeSetVersion = "multi-provider-model-check-v4-gpt56-preview"
	}
	cfg.Deadline = duration(getenv("JUHE_AI_MODEL_CHECK_DEADLINE"), 15*time.Minute)
	cfg.Heartbeat = duration(getenv("JUHE_AI_MODEL_CHECK_HEARTBEAT"), 10*time.Second)
	cfg.RetryAttempts = integer(getenv("JUHE_AI_MODEL_CHECK_RETRY_ATTEMPTS"), 2)
	if cfg.StoreMode == "sqlite" {
		return RuntimeConfig{}, errors.New("J3b 不允许在 juhe-ai-jobs 中启用 sqlite：方案 A 的唯一运行时 owner 是 juhe-ai-gateway")
	}
	if cfg.JobsPostgresURL == "" {
		return RuntimeConfig{}, errors.New("postgres 模式缺少 JUHE_AI_MODEL_CHECK_POSTGRES_URL")
	}
	if cfg.BusinessPostgresURL == "" {
		return RuntimeConfig{}, errors.New("postgres 模式缺少 JUHE_AI_MODEL_CHECK_BUSINESS_POSTGRES_URL")
	}
	if cfg.CredentialSecret == "" {
		return RuntimeConfig{}, errors.New("JUHE_AI_MODEL_CHECK_CREDENTIAL_SECRET 是必填配置")
	}
	if cfg.IdentitySecret == "" {
		return RuntimeConfig{}, errors.New("JUHE_AI_MODEL_CHECK_IDENTITY_SECRET 是必填配置")
	}
	if cfg.Deadline <= 0 {
		return RuntimeConfig{}, errors.New("JUHE_AI_MODEL_CHECK_DEADLINE 必须为正 duration")
	}
	if cfg.Heartbeat <= 0 {
		return RuntimeConfig{}, errors.New("JUHE_AI_MODEL_CHECK_HEARTBEAT 必须为正 duration")
	}
	if cfg.RetryAttempts < 0 || cfg.RetryAttempts > 10 {
		return RuntimeConfig{}, errors.New("JUHE_AI_MODEL_CHECK_RETRY_ATTEMPTS 必须在 0..10")
	}
	return RuntimeConfig{}, errors.New("J3b 不允许在 juhe-ai-jobs 中启用 postgres：方案 A 的唯一运行时 owner 是 juhe-ai-gateway")
}

func duration(value string, fallback time.Duration) time.Duration {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}

func integer(value string, fallback int) int {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return -1
	}
	return parsed
}
