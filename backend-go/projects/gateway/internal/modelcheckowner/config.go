package modelcheckowner

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the Gateway-side owner contract for J3b. Loading it only validates
// the explicit readiness gates; listener and scheduler startup remains the
// caller's responsibility after the full dependency graph is assembled.
type Config struct {
	Enabled                  bool
	StoreMode                string
	DatabasePath             string
	PostgresURL              string
	BusinessDatabasePath     string
	BusinessPostgresURL      string
	CredentialSecret         string
	IdentitySecret           string
	Owner                    string
	InstanceID               string
	BusinessHandoffConfirmed bool
	// NodeWriterStopped is an explicit cutover fence. It must be true before
	// Gateway can enable a confirmed Business handoff; otherwise a stale Node
	// writer could race the new owner and silently corrupt Business state.
	NodeWriterStopped            bool
	SchemaReady                  bool
	RuntimeReady                 bool
	HealthBoundaryReady          bool
	CircuitRuntimeRedisURL       string
	CircuitRuntimeRedisNamespace string
	CircuitRuntimeCapacity       int
	CircuitRuntimeRetention      time.Duration
}

func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Config{Enabled: strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_J3B_ENABLED")), "true")}
	if !cfg.Enabled {
		return cfg, nil
	}
	cfg.Owner = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_J3B_OWNER")))
	if cfg.Owner != "gateway" {
		return Config{}, errors.New("启用 J3b 时 JUHE_AI_J3B_OWNER 必须为 gateway")
	}
	cfg.InstanceID = strings.TrimSpace(getenv("JUHE_AI_J3B_INSTANCE_ID"))
	if cfg.InstanceID == "" {
		return Config{}, errors.New("JUHE_AI_J3B_INSTANCE_ID 是必填配置")
	}
	cfg.StoreMode = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_J3B_STORE")))
	if cfg.StoreMode != "sqlite" && cfg.StoreMode != "postgres" {
		return Config{}, errors.New("JUHE_AI_J3B_STORE 必须为 sqlite 或 postgres")
	}
	if cfg.StoreMode == "sqlite" {
		cfg.DatabasePath = strings.TrimSpace(getenv("JUHE_AI_J3B_DATABASE_PATH"))
		if cfg.DatabasePath == "" {
			return Config{}, errors.New("sqlite 模式缺少 JUHE_AI_J3B_DATABASE_PATH")
		}
		cfg.BusinessDatabasePath = strings.TrimSpace(getenv("JUHE_AI_J3B_BUSINESS_DATABASE_PATH"))
		if cfg.BusinessDatabasePath == "" {
			return Config{}, errors.New("sqlite 模式缺少 JUHE_AI_J3B_BUSINESS_DATABASE_PATH")
		}
		if strings.EqualFold(cfg.DatabasePath, cfg.BusinessDatabasePath) {
			return Config{}, errors.New("J3b 专属 SQLite 文件必须与 Business SQLite 文件分离")
		}
	} else {
		cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_J3B_POSTGRES_URL"))
		if cfg.PostgresURL == "" {
			return Config{}, errors.New("postgres 模式缺少 JUHE_AI_J3B_POSTGRES_URL")
		}
		cfg.BusinessPostgresURL = strings.TrimSpace(getenv("JUHE_AI_J3B_BUSINESS_POSTGRES_URL"))
		if cfg.BusinessPostgresURL == "" {
			return Config{}, errors.New("postgres 模式缺少 JUHE_AI_J3B_BUSINESS_POSTGRES_URL")
		}
	}
	cfg.CredentialSecret = strings.TrimSpace(getenv("JUHE_AI_J3B_CREDENTIAL_SECRET"))
	if cfg.CredentialSecret == "" {
		return Config{}, errors.New("JUHE_AI_J3B_CREDENTIAL_SECRET 是必填配置")
	}
	cfg.IdentitySecret = strings.TrimSpace(getenv("JUHE_AI_J3B_IDENTITY_SECRET"))
	if cfg.IdentitySecret == "" {
		return Config{}, errors.New("JUHE_AI_J3B_IDENTITY_SECRET 是必填配置")
	}
	cfg.BusinessHandoffConfirmed = trueValue(getenv("JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED"))
	if !cfg.BusinessHandoffConfirmed {
		return Config{}, errors.New("J3b Business owner handoff 未确认，必须保持关闭")
	}
	cfg.NodeWriterStopped = trueValue(getenv("JUHE_AI_J3B_NODE_WRITER_STOPPED"))
	if !cfg.NodeWriterStopped {
		return Config{}, errors.New("J3b Business owner handoff 已确认但 Node writer 未停止，必须保持关闭")
	}
	cfg.SchemaReady = trueValue(getenv("JUHE_AI_J3B_SCHEMA_READY"))
	if !cfg.SchemaReady {
		return Config{}, errors.New("J3b schema readiness 未确认，必须保持关闭")
	}
	cfg.HealthBoundaryReady = trueValue(getenv("JUHE_AI_J3B_HEALTH_BOUNDARY_READY"))
	if !cfg.HealthBoundaryReady {
		return Config{}, errors.New("J3b/J3c health boundary 未确认，必须保持关闭")
	}
	cfg.RuntimeReady = trueValue(getenv("JUHE_AI_J3B_RUNTIME_READY"))
	if !cfg.RuntimeReady {
		return Config{}, errors.New("J3b runtime readiness 未确认，必须保持关闭")
	}
	cfg.CircuitRuntimeRedisURL = strings.TrimSpace(getenv("JUHE_AI_J3B_CIRCUIT_REDIS_URL"))
	if cfg.CircuitRuntimeRedisURL == "" {
		cfg.CircuitRuntimeRedisURL = strings.TrimSpace(getenv("JUHE_AI_REDIS_STATE_URL"))
	}
	if cfg.CircuitRuntimeRedisURL == "" {
		return Config{}, errors.New("J3b account circuit runtime 缺少 Redis state URL")
	}
	cfg.CircuitRuntimeRedisNamespace = strings.TrimSpace(getenv("JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE"))
	if cfg.CircuitRuntimeRedisNamespace == "" {
		cfg.CircuitRuntimeRedisNamespace = strings.TrimSpace(getenv("JUHE_AI_REDIS_NAMESPACE"))
	}
	if cfg.CircuitRuntimeRedisNamespace == "" {
		return Config{}, errors.New("J3b account circuit runtime 缺少 Redis namespace")
	}
	cfg.CircuitRuntimeCapacity = 100000
	if raw := strings.TrimSpace(getenv("JUHE_AI_J3B_CIRCUIT_RUNTIME_CAPACITY")); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 1 || value > 10000000 {
			return Config{}, errors.New("JUHE_AI_J3B_CIRCUIT_RUNTIME_CAPACITY 无效")
		}
		cfg.CircuitRuntimeCapacity = value
	}
	cfg.CircuitRuntimeRetention = 5 * time.Minute
	if raw := strings.TrimSpace(getenv("JUHE_AI_J3B_CIRCUIT_RUNTIME_RETENTION")); raw != "" {
		value, parseErr := time.ParseDuration(raw)
		if parseErr != nil || value <= 0 || value > 7*24*time.Hour {
			return Config{}, errors.New("JUHE_AI_J3B_CIRCUIT_RUNTIME_RETENTION 无效")
		}
		cfg.CircuitRuntimeRetention = value
	}
	return cfg, nil
}

func trueValue(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}
