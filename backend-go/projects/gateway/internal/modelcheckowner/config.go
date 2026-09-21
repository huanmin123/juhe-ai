package modelcheckowner

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/datadir"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
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
	// AutoClaimed marks the 2026-09-20 zero-config standalone arm: the
	// handoff/readiness env family is entirely unconfigured, so the owner
	// facts are claimed in-process (handoff flags true, fixed standalone
	// epoch, no cutover evidence). Explicitly configuring any family member
	// restores the original fail-closed cutover discipline.
	AutoClaimed bool
	// OwnerEpoch identifies the externally coordinated cutover epoch. The
	// value is intentionally opaque here; the deployment evidence verifier
	// remains responsible for proving that the epoch is real and current.
	OwnerEpoch string
	// CutoverEvidencePath points to the externally generated post-cutover
	// evidence consumed before any Business DB or listener is opened.
	CutoverEvidencePath string
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

// j3bHandoffFamilyEnv lists the migration-era handoff/readiness family. The
// zero-config rule mirrors runtime.go's JUHE_AI_BUSINESS_* family (2026-09-19):
// when every member is unconfigured the deployment is treated as a fresh
// standalone install with no Node cutover history and the owner facts are
// auto-claimed; configuring any member keeps the original fail-closed gates.
var j3bHandoffFamilyEnv = []string{
	"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED",
	"JUHE_AI_J3B_NODE_WRITER_STOPPED",
	"JUHE_AI_J3B_OWNER_EPOCH",
	"JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH",
	"JUHE_AI_J3B_SCHEMA_READY",
	"JUHE_AI_J3B_HEALTH_BOUNDARY_READY",
	"JUHE_AI_J3B_RUNTIME_READY",
}

func LoadConfig(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	// 2026-09-21：模型检测是常驻功能，JUHE_AI_J3B_ENABLED 总开关已移除——
	// gateway 进程始终装配 J3b owner；访问控制走管理面权限（Admin/Self
	// 会话鉴权），部署依赖只有存储与可选 Redis。
	cfg := Config{Enabled: true}
	cfg.Owner = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_J3B_OWNER")))
	if cfg.Owner == "" {
		cfg.Owner = "gateway"
	}
	if cfg.Owner != "gateway" {
		return Config{}, errors.New("启用 J3b 时 JUHE_AI_J3B_OWNER 必须为 gateway")
	}
	cfg.InstanceID = strings.TrimSpace(getenv("JUHE_AI_J3B_INSTANCE_ID"))
	if cfg.InstanceID == "" {
		cfg.InstanceID = datadir.DefaultInstanceID()
	}
	cfg.StoreMode = strings.ToLower(strings.TrimSpace(getenv("JUHE_AI_J3B_STORE")))
	if cfg.StoreMode == "" {
		if strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_DATABASE_DRIVER")), "postgres") {
			cfg.StoreMode = "postgres"
		} else {
			cfg.StoreMode = "sqlite"
		}
	}
	if cfg.StoreMode != "sqlite" && cfg.StoreMode != "postgres" {
		return Config{}, errors.New("JUHE_AI_J3B_STORE 必须为 sqlite 或 postgres")
	}
	dataDir := datadir.Dir(getenv)
	if cfg.StoreMode == "sqlite" {
		cfg.DatabasePath = datadir.Path(getenv, dataDir, "JUHE_AI_J3B_DATABASE_PATH", datadir.ModelCheckDatabase)
		cfg.BusinessDatabasePath = strings.TrimSpace(getenv("JUHE_AI_J3B_BUSINESS_DATABASE_PATH"))
		if cfg.BusinessDatabasePath == "" {
			// 未显式配置时跟随主网关的 Business 库（同一部署的 owner 事实
			// 源必须同库），再回落 datadir 固定名。
			cfg.BusinessDatabasePath = strings.TrimSpace(getenv("JUHE_AI_BUSINESS_DATABASE_PATH"))
		}
		if cfg.BusinessDatabasePath == "" {
			cfg.BusinessDatabasePath = filepath.Join(dataDir, datadir.BusinessDatabase)
		}
		if cfg.DatabasePath == cfg.BusinessDatabasePath {
			return Config{}, errors.New("J3b 专属 SQLite 文件必须与 Business SQLite 文件分离")
		}
	} else {
		cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_J3B_POSTGRES_URL"))
		if cfg.PostgresURL == "" {
			cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
		}
		if cfg.PostgresURL == "" {
			return Config{}, errors.New("postgres 模式缺少 JUHE_AI_J3B_POSTGRES_URL（或回退 JUHE_AI_POSTGRES_URL）")
		}
		cfg.BusinessPostgresURL = strings.TrimSpace(getenv("JUHE_AI_J3B_BUSINESS_POSTGRES_URL"))
		if cfg.BusinessPostgresURL == "" {
			cfg.BusinessPostgresURL = strings.TrimSpace(getenv("JUHE_AI_BUSINESS_POSTGRES_URL"))
		}
		if cfg.BusinessPostgresURL == "" {
			cfg.BusinessPostgresURL = cfg.PostgresURL
		}
	}
	cfg.CredentialSecret = strings.TrimSpace(getenv("JUHE_AI_J3B_CREDENTIAL_SECRET"))
	if cfg.CredentialSecret == "" {
		// 与主配置同源：未显式配置时回落 JUHE_AI_SECRET；仍为空时由组合根
		// 按零配置约定填内置开发密钥（生产环境主配置已拒绝空/短密钥）。
		cfg.CredentialSecret = strings.TrimSpace(getenv("JUHE_AI_SECRET"))
	}
	cfg.IdentitySecret = strings.TrimSpace(getenv("JUHE_AI_J3B_IDENTITY_SECRET"))
	if cfg.IdentitySecret == "" {
		cfg.IdentitySecret = cfg.CredentialSecret
	}
	if hasAnyRawConfig(getenv, j3bHandoffFamilyEnv...) {
		cfg.BusinessHandoffConfirmed = trueValue(getenv("JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED"))
		if !cfg.BusinessHandoffConfirmed {
			return Config{}, errors.New("J3b Business owner handoff 未确认，必须保持关闭")
		}
		cfg.NodeWriterStopped = trueValue(getenv("JUHE_AI_J3B_NODE_WRITER_STOPPED"))
		if !cfg.NodeWriterStopped {
			return Config{}, errors.New("J3b Business owner handoff 已确认但 Node writer 未停止，必须保持关闭")
		}
		cfg.OwnerEpoch = strings.TrimSpace(getenv("JUHE_AI_J3B_OWNER_EPOCH"))
		if cfg.OwnerEpoch == "" {
			return Config{}, errors.New("J3b Business owner handoff 已确认但 JUHE_AI_J3B_OWNER_EPOCH 未提供，必须保持关闭")
		}
		cfg.CutoverEvidencePath = strings.TrimSpace(getenv("JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH"))
		if cfg.CutoverEvidencePath == "" {
			return Config{}, errors.New("J3b Business owner handoff 已确认但 JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH 未提供，必须保持关闭")
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
	} else {
		cfg.AutoClaimed = true
		cfg.BusinessHandoffConfirmed = true
		cfg.NodeWriterStopped = true
		cfg.OwnerEpoch = "standalone"
		cfg.SchemaReady = true
		cfg.HealthBoundaryReady = true
		cfg.RuntimeReady = true
	}
	cfg.CircuitRuntimeRedisURL = strings.TrimSpace(getenv("JUHE_AI_J3B_CIRCUIT_REDIS_URL"))
	if cfg.CircuitRuntimeRedisURL == "" {
		cfg.CircuitRuntimeRedisURL = strings.TrimSpace(getenv("JUHE_AI_REDIS_STATE_URL"))
	}
	if cfg.CircuitRuntimeRedisURL == "" {
		if !cfg.AutoClaimed {
			return Config{}, errors.New("J3b account circuit runtime 缺少 Redis state URL")
		}
		// 自动认领零配置：无 Redis 时组合根回退进程内 memory 准入，
		// namespace 不参与装配，不校验。
	} else {
		// 2026-09-22 namespace canonical 化：J3b 组合根把同一 namespace 喂给
		// circuit_runtime（直接拼接型）与 key_model_runtime（双格式去重型），
		// 加载层统一剥除 `juhe-ai:` 根前缀，短名输入逐字节不变，键空间不再
		// 因全前缀配置分裂。
		cfg.CircuitRuntimeRedisNamespace = rediscfg.CanonicalRedisNamespace(getenv("JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE"))
		if cfg.CircuitRuntimeRedisNamespace == "" {
			cfg.CircuitRuntimeRedisNamespace = rediscfg.CanonicalRedisNamespace(getenv("JUHE_AI_REDIS_NAMESPACE"))
		}
		if cfg.CircuitRuntimeRedisNamespace == "" {
			return Config{}, errors.New("J3b account circuit runtime 缺少 Redis namespace")
		}
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

func hasAnyRawConfig(getenv func(string) string, keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(getenv(key)) != "" {
			return true
		}
	}
	return false
}

func trueValue(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}
