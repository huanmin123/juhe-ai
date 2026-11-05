package modelcheckowner

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/datadir"
	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// Config is the Gateway-side owner contract for J3b. Loading it only validates
// the explicit readiness gates; listener and scheduler startup remains the
// caller's responsibility after the full dependency graph is assembled.
// PostgreSQL 是唯一存储形态：sqlite 形态（DatabasePath/DatabaseDriver）与
// StoreMode 开关已随 PLAN-20261024T051925945Z 物理删除。
type Config struct {
	Enabled                  bool
	PostgresURL              string
	BusinessPostgresURL      string
	CredentialSecret         string
	IdentitySecret           string
	Owner                    string
	InstanceID               string
	BusinessHandoffConfirmed bool
	// AutoClaimed marks the zero-config auto-claim contract: the owner facts
	// are claimed in-process (handoff flags true, fixed standalone epoch, no
	// cutover evidence). 清理批次 C1（2026-09-30）起恒真。
	AutoClaimed bool
	// OwnerEpoch identifies the externally coordinated cutover epoch. The
	// value is intentionally opaque here; the deployment evidence verifier
	// remains responsible for proving that the epoch is real and current.
	OwnerEpoch string
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
	// 2026-09-21：模型检测是常驻功能，JUHE_AI_J3B_ENABLED 总开关已移除——
	// gateway 进程始终装配 J3b owner；访问控制走管理面权限（Admin/Self
	// 会话鉴权），部署依赖只有存储与可选 Redis。
	cfg := Config{Enabled: true}
	// J3b owner 只有 gateway 一个合法值：Go 迁移完成后 Node 侧写入方已清零，
	// owner 判定不再是部署输入。
	cfg.Owner = "gateway"
	cfg.InstanceID = strings.TrimSpace(getenv("JUHE_AI_J3B_INSTANCE_ID"))
	if cfg.InstanceID == "" {
		cfg.InstanceID = datadir.DefaultInstanceID()
	}
	// PostgreSQL 是唯一存储形态：J3b 与主链共用主库，不再有独立 URL。
	cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_POSTGRES_URL"))
	if cfg.PostgresURL == "" {
		return Config{}, errors.New("缺少 JUHE_AI_POSTGRES_URL")
	}
	cfg.BusinessPostgresURL = cfg.PostgresURL
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
	// 清理批次 C1（2026-09-30）：Node→Go 切流已完成且 Node 进程已于
	// 2026-09-05 清零（不可恢复），J3B 切流门禁 env 家族与 cutover evidence
	// 校验链整体退役——owner 事实一律按零配置自动认领（原显式配置分支在
	// 所有现存部署中从未被配置过，五源核对零引用）。
	cfg.AutoClaimed = true
	cfg.BusinessHandoffConfirmed = true
	cfg.NodeWriterStopped = true
	cfg.OwnerEpoch = "standalone"
	cfg.SchemaReady = true
	cfg.HealthBoundaryReady = true
	cfg.RuntimeReady = true
	// 硬切：J3b 与主链共用唯一 Redis 连接串，键空间由 {namespace:j3b}
	// 域隔离；不再读取独立的 J3B_CIRCUIT_REDIS_URL/NAMESPACE。
	cfg.CircuitRuntimeRedisURL = strings.TrimSpace(getenv("JUHE_AI_REDIS_URL"))
	if cfg.CircuitRuntimeRedisURL == "" {
		if !cfg.AutoClaimed {
			return Config{}, errors.New("J3b account circuit runtime 缺少 Redis URL")
		}
		// 单元测试/零配置内存装配路径；生产 gateway 的 JUHE_AI_REDIS_URL 必填。
	} else {
		baseNamespace := rediscfg.CanonicalRedisNamespace(getenv("JUHE_AI_REDIS_NAMESPACE"))
		if baseNamespace == "" {
			return Config{}, errors.New("J3b account circuit runtime 缺少 Redis namespace")
		}
		cfg.CircuitRuntimeRedisNamespace = rediscfg.DomainNamespace(baseNamespace, rediscfg.DomainJ3B)
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
