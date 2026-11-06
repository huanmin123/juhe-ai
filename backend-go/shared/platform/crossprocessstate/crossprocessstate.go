// Package crossprocessstate 是 gateway 与 jobs 共享的跨进程状态契约：
// 键命名、按账户锁、单调版本号与批量读取原语。
//
// 边界（docs/plans/计划-20261106T024618000Z-网关与jobs跨进程共享状态改造.md
// §B0）：
//   - 只放键 shape、锁/版本原语、超时与错误分类，不放任何业务判定；
//   - 不 import gateway/jobs 任何内部包，两个项目只依赖本包 + rediscfg；
//   - 值只允许锁 token 与整型版本号，禁止写入 token/密码/完整凭据；
//   - PG 仍是事实源：版本号只表达“PG 已提交的凭据发生过变化”，读者在
//     Redis 缺失/错误时必须回落到原有 PG/缓存路径，不得用旧值放行。
//
// 键统一经 rediscfg.TaggedNamespacedKey 生成，同一 namespace 下的所有键
// 固定同一 Cluster slot，因此 Versions 的批量 MGET 在 Cluster 形态合法。
// 不同域（例如 oauth 与 runtimecache）由调用方经
// rediscfg.DomainNamespace(root, domain) 派生不同 namespace 隔离。
package crossprocessstate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

const (
	// DefaultLockTTL 是跨进程锁的默认租期：OAuth 刷新单次上游换发的预算上限
	// 为 60s（transport 侧 25s/60s 独立超时），租期与预算对齐；即使持有者
	// 崩溃，最迟 60s 后锁自然释放。
	DefaultLockTTL = 60 * time.Second
	// DefaultVersionTTL 是版本键的保留期。版本只用于“是否变化”比较，过期后
	// 读者会观察到一次回落（0），触发一次保守重载后重新记录，不会放行旧凭据。
	DefaultVersionTTL = 30 * 24 * time.Hour
	// DefaultReadTimeout 是请求路径上批量读版本的预算上限（计划 §5 硬约束 7：
	// 每请求最多一次批量读、50–100ms 超时）。Redis 变慢时按错误处理回退原有
	// 缓存/PG 路径，不阻塞请求。
	DefaultReadTimeout = 50 * time.Millisecond
)

// 共享实体名。键 shape 固定为
//
//	juhe-ai:{<namespace>}:<entity>:<id>
//
// entity 自带类型前缀（lock/version），避免不同原语共用一个键。
const (
	// EntityOAuthRefreshLock 是 OAuth 账户级刷新互斥锁（gateway 手动/401 自动
	// 刷新与 jobs 定时 refresh/keepalive 共用）。
	EntityOAuthRefreshLock = "lock:oauth-refresh"
	// EntityAccountCredentialsVersion 是账户 OAuth 凭据版本：每次 PG 凭据提交
	// 后递增，gateway 运行态账号缓存据此提前重载（不必等 60s revalidate）。
	EntityAccountCredentialsVersion = "version:account-credentials"
)

const unlockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`

const bumpVersionScript = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
local next = tonumber(ARGV[1])
local ttl = ARGV[2]
if next > current then
	redis.call('SET', KEYS[1], ARGV[1], 'PX', ttl)
	return next
end
redis.call('PEXPIRE', KEYS[1], ttl)
return current
`

// Executor 是原语依赖的最小 Redis 面。go-redis 的 UniversalClient 经
// NewRedis 适配；测试注入内存 fake，无需 miniredis 或真实 Redis。
type Executor interface {
	SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)
	MGet(ctx context.Context, keys ...string) ([]any, error)
}

// Client 把共享契约绑定到一个已派生的域 namespace。零值不可用；namespace
// 为空时沿用 rediscfg.TaggedNamespacedKey 的既有空 namespace 语义（返回原键），
// 生产调用方必须传入非空 namespace。
type Client struct {
	exec      Executor
	namespace string
}

// New 用最小 Executor 构造（测试或已有适配层）。
func New(exec Executor, namespace string) (*Client, error) {
	if exec == nil {
		return nil, fmt.Errorf("crossprocessstate 需要 Redis Executor")
	}
	return &Client{exec: exec, namespace: rediscfg.CanonicalRedisNamespace(namespace)}, nil
}

// NewRedis 用 go-redis 客户端构造。
func NewRedis(client redis.UniversalClient, namespace string) (*Client, error) {
	if client == nil {
		return nil, fmt.Errorf("crossprocessstate 需要 Redis 客户端")
	}
	return New(redisExecutor{client: client}, namespace)
}

// Namespace 返回派生根 namespace（例如 prod:oauth）。
func (c *Client) Namespace() string { return c.namespace }

// Key 生成共享键：juhe-ai:{<namespace>}:<entity>:<id>。
func (c *Client) Key(entity, id string) string {
	entity = strings.TrimSpace(entity)
	id = strings.TrimSpace(id)
	if entity == "" || id == "" {
		panic("crossprocessstate 键的 entity/id 不能为空")
	}
	return rediscfg.TaggedNamespacedKey(entity+":"+id, c.namespace)
}

// LockKey 是 Key 的语义别名（账户级互斥锁）。
func (c *Client) LockKey(entity, id string) string { return c.Key(entity, id) }

// VersionKey 是 Key 的语义别名（实体版本号）。
func (c *Client) VersionKey(entity, id string) string { return c.Key(entity, id) }

// TryLock 以 SET NX PX 抢占锁。acquired=false 表示锁被其他持有者占用；
// token 仅在该次调用成功时非空，释放时必须原样传回 Unlock 以做 compare-delete。
func (c *Client) TryLock(ctx context.Context, entity, id string, ttl time.Duration) (token string, acquired bool, err error) {
	if ttl <= 0 {
		ttl = DefaultLockTTL
	}
	token, err = randomToken()
	if err != nil {
		return "", false, err
	}
	acquired, err = c.exec.SetNX(ctx, c.Key(entity, id), token, ttl)
	if err != nil {
		return "", false, err
	}
	if !acquired {
		return "", false, nil
	}
	return token, true, nil
}

// Unlock 做 compare-delete：只有 token 仍匹配（未被 TTL 过期后他人重新持有）
// 才删除。token 为空是 no-op（调用方未拿到锁）。
func (c *Client) Unlock(ctx context.Context, entity, id, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	_, err := c.exec.Eval(ctx, unlockScript, []string{c.Key(entity, id)}, token)
	return err
}

// BumpVersion 把实体版本单调推进到 revision（通常为 PG 的 config_revision），
// 并续期。只有 revision 大于当前值才写入，乱序回写不会让版本后退；键不存在
// 时直接写入。
//
// 写序要求：调用方必须在本实体对应的 PG 事务提交成功之后调用；失败只记录
// 告警并保留原有可见性窗口，不得回滚已提交的业务写。
//
// 版本号与 PG revision 同值的设计让读者可以在装载数据后检测“读到旧数据、
// 读到新版本”的竞态：当版本大于数据行的 config_revision 时，说明数据读取
// 早于最近一次提交，应按需重读一次。
func (c *Client) BumpVersion(ctx context.Context, entity, id string, revision int64) (int64, error) {
	if revision <= 0 {
		return 0, fmt.Errorf("crossprocessstate 版本号必须为正整数: %d", revision)
	}
	raw, err := c.exec.Eval(ctx, bumpVersionScript, []string{c.Key(entity, id)}, revision, DefaultVersionTTL.Milliseconds())
	if err != nil {
		return 0, err
	}
	value, ok := toInt64(raw)
	if !ok {
		return 0, fmt.Errorf("crossprocessstate 版本返回值不是整数: %v", raw)
	}
	return value, nil
}

// Versions 批量读取实体版本。返回值覆盖全部入参 id：键缺失、值为空或非法
// 一律记 0（“无已记录版本”），调用方把 0 与缓存条目记录的快照比较即可。
// 只有 Redis 调用失败才返回 error；调用方必须按错误回退原有缓存/PG 路径。
//
// 调用方应通过 ReadContext 限制本次读取预算；同一 namespace 的键同 slot，
// Cluster 形态下单个 MGET 合法。
func (c *Client) Versions(ctx context.Context, entity string, ids []string) (map[string]int64, error) {
	out := make(map[string]int64, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	keys := make([]string, 0, len(ids))
	keyIDs := make([]string, 0, len(ids))
	unique := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := unique[id]; exists {
			continue
		}
		unique[id] = struct{}{}
		out[id] = 0
		keys = append(keys, c.Key(entity, id))
		keyIDs = append(keyIDs, id)
	}
	if len(keys) == 0 {
		return out, nil
	}
	values, err := c.exec.MGet(ctx, keys...)
	if err != nil {
		return nil, err
	}
	for index, id := range keyIDs {
		if index >= len(values) {
			break
		}
		if value, ok := toInt64(values[index]); ok {
			out[id] = value
		}
	}
	return out, nil
}

// ReadContext 给批量读套上默认 50ms 预算；调用方已有更短 deadline 时以更短者
// 为准（context.WithTimeout 的既有语义）。
func ReadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, DefaultReadTimeout)
}

func randomToken() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("crossprocessstate 生成锁 token 失败: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func toInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case nil:
		return 0, false
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	case []byte:
		parsed, err := strconv.ParseInt(strings.TrimSpace(string(typed)), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

type redisExecutor struct {
	client redis.UniversalClient
}

func (e redisExecutor) SetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	return e.client.SetNX(ctx, key, value, ttl).Result()
}

func (e redisExecutor) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return e.client.Eval(ctx, script, keys, args...).Result()
}

func (e redisExecutor) MGet(ctx context.Context, keys ...string) ([]any, error) {
	return e.client.MGet(ctx, keys...).Result()
}
