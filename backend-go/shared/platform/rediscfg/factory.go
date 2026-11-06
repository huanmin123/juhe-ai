// Package rediscfg 统一 Redis 连接构造、键命名空间与 Cluster 兼容辅助。
//
// 运行形态只有两种：single（单节点，固定 DB0）与 cluster（Redis Cluster）。
// Cluster 只支持 DB0，因此应用不再使用 DB 编号区分 cache/state/J3b；三类
// 数据由不同的域 namespace + hash tag 隔离，同一域的键固定同一 slot。
package rediscfg

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Mode 是 Redis 部署形态。
type Mode string

const (
	ModeSingle  Mode = "single"
	ModeCluster Mode = "cluster"
)

// 域常量：所有域级键空间都必须从以下常量取值，gateway/jobs 共享的域
// （circuit、keymodel）尤其不能各写一份字面量。
const (
	DomainCache        = "cache"
	DomainCircuit      = "circuit"
	DomainJ3B          = "j3b"
	DomainKeyModel     = "keymodel"
	DomainHotQuality   = "hotquality"
	DomainClientIP     = "clientip"
	DomainSession      = "session"
	DomainRuntimeCache = "runtimecache"
	DomainRateLimit    = "ratelimit"
	DomainCallerTier   = "callertier"
	DomainQuota        = "quota"
	DomainObs          = "obs"
	DomainRouting      = "routing"
	DomainRouteState   = "routestate"
	DomainProxyHealth  = "proxyhealth"
	DomainInval        = "inval"
	DomainRealtime     = "realtime"
	DomainSpeedFirst   = "speedfirst"
	DomainOAuth        = "oauth"
	DomainAuthState    = "authstate"
	DomainSettings     = "settings"
)

// Config 是一次 Redis 客户端构造所需的全部输入。
type Config struct {
	// URL 是基础连接串。single 形态下 DB 只能是 0/空；cluster 形态下
	// 多节点通过 ?addr=host:port 重复追加。
	URL string
	// Mode 为空时按 single 处理；其他值启动失败。
	Mode Mode
	// Password 仅在 URL 未自带密码时注入，供 compose 的 REDIS_PASSWORD 使用。
	Password string
	// ContextTimeoutEnabled 透传 go-redis 的同名选项；默认 false 保持既有
	// 客户端行为。
	ContextTimeoutEnabled bool
}

// processConfig 保存进程级 Redis 运行形态（模式与密码），由各进程 main 在
// 启动最早处经 Configure 写入；二级客户端只持有 URL，NewUniversalClient 用
// 它补齐 Mode/Password。刻意不做隐式 os.Getenv：单元测试里大量临时
// miniredis 连接不应被测试宿主的 REDIS_PASSWORD/JUHE_AI_REDIS_MODE 污染。
const (
	clusterDialTimeout = time.Second
	clusterIOTimeout   = 2 * time.Second
	clusterMaxRetries  = 1
)

var processConfig atomic.Value // Config

// Configure 设置进程级 Redis 缺省配置；零值表示恢复默认（single、无密码）。
func Configure(cfg Config) {
	processConfig.Store(cfg)
}

func configuredProcessConfig() Config {
	cfg, _ := processConfig.Load().(Config)
	return cfg
}

// FromEnv 读取统一 Redis 配置。URL 必填；MODE 空值按 single；
// REDIS_PASSWORD 仅在 URL 未自带密码时注入。
func FromEnv(getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, fmt.Errorf("getenv 不能为空")
	}
	rawURL := strings.TrimSpace(getenv("JUHE_AI_REDIS_URL"))
	if rawURL == "" {
		return Config{}, fmt.Errorf("缺少 JUHE_AI_REDIS_URL（single/cluster 共用基础连接串）")
	}
	mode, err := ParseMode(getenv("JUHE_AI_REDIS_MODE"))
	if err != nil {
		return Config{}, err
	}
	return Config{
		URL:      rawURL,
		Mode:     mode,
		Password: strings.TrimSpace(getenv("REDIS_PASSWORD")),
	}, nil
}

// ParseMode 解析 JUHE_AI_REDIS_MODE；空值按 single。
func ParseMode(raw string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", ModeSingle:
		return ModeSingle, nil
	case ModeCluster:
		return ModeCluster, nil
	default:
		return "", fmt.Errorf("JUHE_AI_REDIS_MODE 只能是 single 或 cluster: %q", raw)
	}
}

// NewUniversalClient 构造单节点或 Cluster 客户端。
//
// Mode 的解析优先级：显式 cfg.Mode（single/cluster，含大小写与空白归一）>
// 进程环境 JUHE_AI_REDIS_MODE > single。二级客户端（会话、限流、client-ip、
// 电路、J3b 等 ~20 个构造点）只持有 URL，没有各自的模式配置；把环境兜底
// 收口在工厂里，保证 cluster 部署下所有客户端形态一致，不会出现主链用
// Cluster、旁路用单节点而误读空数据。
func NewUniversalClient(cfg Config) (redis.UniversalClient, error) {
	process := configuredProcessConfig()
	if strings.TrimSpace(string(cfg.Mode)) == "" {
		cfg.Mode = process.Mode
	}
	if strings.TrimSpace(cfg.Password) == "" {
		cfg.Password = process.Password
	}
	mode := cfg.Mode
	parsedMode, err := ParseMode(string(mode))
	if err != nil {
		return nil, err
	}
	mode = parsedMode
	rawURL := strings.TrimSpace(cfg.URL)
	if rawURL == "" {
		return nil, fmt.Errorf("JUHE_AI_REDIS_URL 不能为空")
	}
	password := cfg.Password
	rawURL, err = injectPassword(rawURL, password)
	if err != nil {
		return nil, err
	}
	if mode == ModeCluster {
		if err := validateClusterURL(rawURL); err != nil {
			return nil, err
		}
		opts, err := redis.ParseClusterURL(rawURL)
		if err != nil {
			return nil, fmt.Errorf("解析 Redis Cluster URL 失败: %w", err)
		}
		// Cluster 节点故障时 go-redis 默认 DialTimeout 5s × MaxRetries 3，
		// 单个请求最坏可被死节点拖住十几秒；网关/探针都跑在请求路径上，
		// 收紧为 1s 拨号 + 1 次重试，让拓扑刷新前的损失以秒计（dev 实测
		// 默认配置下多客户端同时等待死节点可达 ~50s）。
		opts.DialTimeout = clusterDialTimeout
		opts.ReadTimeout = clusterIOTimeout
		opts.WriteTimeout = clusterIOTimeout
		opts.PoolTimeout = clusterIOTimeout
		opts.MaxRetries = clusterMaxRetries
		opts.ContextTimeoutEnabled = cfg.ContextTimeoutEnabled
		return redis.NewClusterClient(opts), nil
	}
	if err := validateSingleURL(rawURL); err != nil {
		return nil, err
	}
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("解析 Redis URL 失败: %w", err)
	}
	opts.DB = 0
	opts.ContextTimeoutEnabled = cfg.ContextTimeoutEnabled
	return redis.NewClient(opts), nil
}

// DomainNamespace 在根 namespace 后追加域段，例如 prod + circuit ->
// prod:circuit。域段非法字符按 namespace 清洗规则折叠。
func DomainNamespace(base, domain string) string {
	base = CanonicalRedisNamespace(base)
	domain = SanitizeRedisNamespacePart(domain)
	if base == "" {
		return domain
	}
	if domain == "" {
		return base
	}
	return base + ":" + domain
}

// TaggedPrefix 返回 Cluster 安全的 namespace 前缀 `juhe-ai:{<ns>}:`。
func TaggedPrefix(namespace string) string {
	ns := CanonicalRedisNamespace(namespace)
	if ns == "" {
		return "juhe-ai:"
	}
	return "juhe-ai:{" + ns + "}:"
}

// TaggedNamespacedKey 生成 Cluster 安全的键：
//
//	juhe-ai:{<namespace>}:<key>
//
// 同一 namespace 下的所有键固定同一 slot；不同域通过 DomainNamespace 得到
// 不同 namespace，从而可以落到不同 slot。namespace 为空时按原键返回
// （调用方应保证命名空间非空；保留该分支仅为既有 panic/空值语义兼容）。
func TaggedNamespacedKey(key, namespace string) string {
	normalized := strings.TrimSpace(key)
	if normalized == "" {
		panic("Redis key 不能为空")
	}
	ns := CanonicalRedisNamespace(namespace)
	if ns == "" {
		return normalized
	}
	namespacePrefix := "juhe-ai:{" + ns + "}:"
	if strings.HasPrefix(normalized, namespacePrefix) {
		return normalized
	}
	if strings.HasPrefix(normalized, "juhe-ai:") {
		rest := normalized[len("juhe-ai:"):]
		// 兼容调用方传入已带旧 namespace 段的键：同一 namespace 只保留
		// 一份（例如 juhe-ai:dev:state + dev -> juhe-ai:{dev}:state）。
		if strings.HasPrefix(rest, ns+":") {
			rest = rest[len(ns)+1:]
		}
		return namespacePrefix + rest
	}
	return namespacePrefix + normalized
}

// CloseClient 关闭单节点或 Cluster 客户端；显式处理 typed-nil，避免
// `(*redis.Client)(nil)` 满足接口后调用 Close 触发 nil panic。
func CloseClient(client any) error {
	switch c := client.(type) {
	case nil:
		return nil
	case *redis.Client:
		if c == nil {
			return nil
		}
		return c.Close()
	case *redis.ClusterClient:
		if c == nil {
			return nil
		}
		return c.Close()
	case interface{ Close() error }:
		return c.Close()
	default:
		return nil
	}
}

// ScanAll 对单节点或 Cluster 执行 pattern 扫描，并把每批 key 交给 fn。
// ClusterClient.Scan 只作用于单个连接，必须逐 master 扫描。
func ScanAll(ctx context.Context, client redis.UniversalClient, pattern string, fn func(keys []string) error) error {
	if client == nil {
		return fmt.Errorf("Redis client 不能为空")
	}
	if cluster, ok := client.(*redis.ClusterClient); ok {
		return cluster.ForEachMaster(ctx, func(ctx context.Context, master *redis.Client) error {
			return scanClient(ctx, master, pattern, fn)
		})
	}
	return scanClient(ctx, client, pattern, fn)
}

func scanClient(ctx context.Context, client redis.Cmdable, pattern string, fn func(keys []string) error) error {
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := fn(keys); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

func injectPassword(rawURL, password string) (string, error) {
	if strings.TrimSpace(password) == "" {
		return rawURL, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("解析 Redis URL 失败: %w", err)
	}
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return rawURL, nil
		}
	}
	username := ""
	if parsed.User != nil {
		username = parsed.User.Username()
	}
	parsed.User = url.UserPassword(username, password)
	return parsed.String(), nil
}

func validateSingleURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("解析 Redis URL 失败: %w", err)
	}
	switch parsed.Path {
	case "", "/", "/0":
		return nil
	default:
		return fmt.Errorf("JUHE_AI_REDIS_URL 在 single 模式只能使用 DB0（path 为空、/ 或 /0）: %q", parsed.Path)
	}
}

func validateClusterURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("解析 Redis Cluster URL 失败: %w", err)
	}
	switch parsed.Path {
	case "", "/", "/0":
		return nil
	default:
		return fmt.Errorf("JUHE_AI_REDIS_URL 在 cluster 模式只能使用 DB0（path 为空、/ 或 /0）: %q", parsed.Path)
	}
}
