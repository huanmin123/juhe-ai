// Package realtimetoken 承载 realtime ephemeral token 的签发与校验面
// （实时语音Realtime网关设计 §4，M5b）：
//
//   - 签发：crypto/rand 32 字节 token（base64url 无填充，43 字符，可安全
//     进查询参数）；Redis SET EX 120，value 为 JSON
//     {api_key_id, model, created_at}；键位沿设计字面 realtime_token:<t>，
//     多环境部署经 rediscfg.NamespacedKey 插入命名空间段。
//   - 校验：GET + 模型一致性（token 只能连接其签发时指定的 model，防枚举
//     他人会话）；TTL 内允许多次连接（浏览器重连场景），过期即失效。
//   - 消费方：POST /v1/realtime/client_secrets 管理端点（签发，本批）与
//     GET /v1/realtime WS 升级（?token= 认证，M5b2 WS 桥接 handler 接线）。
//
// Redis 不可用时签发/校验显式失败（ErrStoreUnavailable），不静默降级——
// ephemeral token 面没有无 Redis 的正确行为。
package realtimetoken

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// TTL 是 token 的固定存活窗口（设计 §4：120 秒）。非设置键：TTL 语义是
// token 面契约的一部分（签发侧与展示给客户端的 expires_at 必须同源），
// 不随系统设置漂移。
const TTL = 120 * time.Second

// 键名前缀（设计 §4 字面 realtime_token:<random>）。
const keyPrefix = "realtime_token:"

// Sentinel errors：调用方按错误类别映射 HTTP 语义（invalid/mismatch →
// 401/403 形态，store 不可用 → 503），不依赖错误文本。
var (
	// ErrInvalidToken 报告 token 不存在或已过期（Redis 无该键）。两者对
	// 调用方不可区分也不需区分——都是拒绝连接。
	ErrInvalidToken = errors.New("realtime token 无效或已过期")
	// ErrModelMismatch 报告 token 与请求模型不一致（不记名复用防护）。
	ErrModelMismatch = errors.New("realtime token 与请求模型不一致")
	// ErrStoreUnavailable 报告 Redis 读写失败。
	ErrStoreUnavailable = errors.New("realtime token 存储不可用")
	// ErrInvalidInput 报告签发参数为空。
	ErrInvalidInput = errors.New("realtime token 签发参数无效")
	// ErrMiss 是 RedisClient.Get 的键不存在哨兵（go-redis 的 redis.Nil 由
	// adapter 翻译成它；内存实现直接返回它）。
	ErrMiss = errors.New("realtime token 键不存在")
)

// RedisClient 是本包消费的命令子集。
type RedisClient interface {
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error
	Get(ctx context.Context, key string) (string, error)
}

// GoRedisClient 把 go-redis 客户端适配到 RedisClient（redis.Nil 翻译为
// ErrMiss）。
type GoRedisClient struct{ Client *redis.Client }

// Set 实现 RedisClient。
func (c GoRedisClient) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	return c.Client.Set(ctx, key, value, expiration).Err()
}

// Get 实现 RedisClient（redis.Nil → ErrMiss）。
func (c GoRedisClient) Get(ctx context.Context, key string) (string, error) {
	value, err := c.Client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrMiss
	}
	return value, err
}

// Service 是签发/校验面。
type Service struct {
	client     RedisClient
	namespace  string
	now        func() time.Time
	randomness io.Reader
}

// NewService 构造服务；namespace 为空时键位不加命名空间段（单环境部署）。
func NewService(client RedisClient, namespace string) *Service {
	return &Service{
		client:     client,
		namespace:  rediscfg.CanonicalRedisNamespace(namespace),
		now:        time.Now,
		randomness: rand.Reader,
	}
}

// IssueResult 是签发产物：value 进响应体 {"value","expires_at"}，ExpiresAt
// 是 Unix 秒（OpenAI client_secrets 响应形态）。
type IssueResult struct {
	Value     string
	ExpiresAt int64
}

// tokenPayload 是 Redis value 的 JSON 形态（设计 §4：{api_key_id, model,
// created_at}）。
type tokenPayload struct {
	APIKeyID  string `json:"api_key_id"`
	Model     string `json:"model"`
	CreatedAt int64  `json:"created_at"`
}

// Issue 签发一枚绑定（api_key_id, model）的短期 token。
func (s *Service) Issue(ctx context.Context, apiKeyID, model string) (IssueResult, error) {
	apiKeyID = strings.TrimSpace(apiKeyID)
	model = strings.TrimSpace(model)
	if apiKeyID == "" || model == "" {
		return IssueResult{}, ErrInvalidInput
	}
	if s == nil || s.client == nil {
		return IssueResult{}, ErrStoreUnavailable
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(s.randomness, raw); err != nil {
		return IssueResult{}, fmt.Errorf("realtime token 随机源失败: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	payload, err := json.Marshal(tokenPayload{APIKeyID: apiKeyID, Model: model, CreatedAt: now.Unix()})
	if err != nil {
		return IssueResult{}, fmt.Errorf("realtime token 序列化失败: %w", err)
	}
	if err := s.client.Set(ctx, s.key(token), string(payload), TTL); err != nil {
		return IssueResult{}, fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	return IssueResult{Value: token, ExpiresAt: now.Add(TTL).Unix()}, nil
}

// VerifyResult 是校验产物：签发该 token 的 API Key id（WS 升级时与会话
// 计费归因对齐）。
type VerifyResult struct {
	APIKeyID string
	Model    string
}

// Verify 校验 token 并强制模型一致性：缺失/过期 → ErrInvalidToken；模型
// 不一致 → ErrModelMismatch。
func (s *Service) Verify(ctx context.Context, token, model string) (VerifyResult, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return VerifyResult{}, ErrInvalidToken
	}
	if s == nil || s.client == nil {
		return VerifyResult{}, ErrStoreUnavailable
	}
	value, err := s.client.Get(ctx, s.key(token))
	if err != nil {
		if errors.Is(err, ErrMiss) {
			return VerifyResult{}, ErrInvalidToken
		}
		return VerifyResult{}, fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	var payload tokenPayload
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return VerifyResult{}, fmt.Errorf("realtime token 载荷损坏: %w", err)
	}
	if payload.Model != strings.TrimSpace(model) {
		return VerifyResult{}, ErrModelMismatch
	}
	return VerifyResult{APIKeyID: payload.APIKeyID, Model: payload.Model}, nil
}

// key 构造 Redis 键：命名空间段存在时插入 juhe-ai:<ns>: 前缀。
func (s *Service) key(token string) string {
	return rediscfg.NamespacedKey(keyPrefix+token, s.namespace)
}
