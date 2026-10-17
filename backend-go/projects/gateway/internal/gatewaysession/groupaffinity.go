package gatewaysession

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 组级会话亲和（策略路由分组会话亲和设计 §3）：会话在策略路由内黏住分组，
// 与账户亲和（含 GroupID 维度）不同源不同名。独立 HMAC purpose、独立记录
// 类型、独立 Redis 键空间与独立端口——不复用账户 SessionBinding 的任何结构
//（§11 数据面阻断项）。

const (
	// GroupAffinityHMACPurpose 是组亲和键的独立 HMAC purpose（§3.1）。
	GroupAffinityHMACPurpose = "group-affinity:v1"

	// 组绑定 TTL 与账户级一致：Redis 1h、进程内 memory 24h。
	groupAffinityRedisTtlMs   = int64(60 * 60 * 1000)
	groupAffinityMemoryTtl    = 24 * time.Hour
	groupAffinityCacheMax     = 5000
	groupAffinityCASMaxRounds = 2

	// redisMissingGroupBindingExpectedValue 与账户级同语义：missing 键的
	// CAS expected 值。
	redisMissingGroupBindingExpectedValue = ""
)

// GroupAffinityKeyInput 是组亲和键的输入（§3.1：canonicalizer 顺序固定，
// 每段长度前缀，不含分组维度——分组选择发生在分组解析之前）。
type GroupAffinityKeyInput struct {
	SystemAccountID string
	APIKeyID        string
	RouteStrategyID string
	ConversationKey string
}

// GroupSessionBinding 是组级会话绑定记录（§3.2）：一会话一策略一条，不含
// 账户 ID、Key 指纹或迁移字段。
type GroupSessionBinding struct {
	RouteStrategyID string
	GroupID         string
}

// GroupAffinityLogger 与 AffinityLogger 同形。
type GroupAffinityLogger interface {
	Warn(fields map[string]any, message string)
}

type noopGroupAffinityLogger struct{}

func (noopGroupAffinityLogger) Warn(fields map[string]any, message string) {}

// GroupAffinityConfig 镜像组绑定所需的配置切片。
type GroupAffinityConfig struct {
	CacheDriver    CacheDriver
	Secret         string
	RedisCacheURL  string
	RedisNamespace string
	// Clock injects time; defaults to time.Now.
	Clock func() time.Time
	// Redis overrides the lazy go-redis dial (test seam).
	Redis RedisClient
	// Logger defaults to a no-op.
	Logger GroupAffinityLogger
}

// GroupAffinityService 承载组级会话绑定状态。
type GroupAffinityService struct {
	cfg    GroupAffinityConfig
	clock  func() time.Time
	logger GroupAffinityLogger
	redis  RedisClient

	mu          sync.Mutex
	bindingByID *ttlCache[GroupSessionBinding]
}

// NewGroupAffinityService 构建组绑定服务；secret 非空判据与账户级一致。
func NewGroupAffinityService(cfg GroupAffinityConfig) (*GroupAffinityService, error) {
	if err := validateIdentitySecret(cfg.Secret); err != nil {
		return nil, err
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = noopGroupAffinityLogger{}
	}
	redisClient := cfg.Redis
	if redisClient == nil && cfg.RedisCacheURL != "" {
		redisClient = newLazyRedisClient(cfg.RedisCacheURL)
	}
	s := &GroupAffinityService{
		cfg:    cfg,
		clock:  clock,
		logger: logger,
		redis:  redisClient,
	}
	s.bindingByID = newTTLCache[GroupSessionBinding](groupAffinityCacheMax, groupAffinityMemoryTtl, false)
	s.bindingByID.readable = func() bool { return s.cfg.CacheDriver != CacheDriverRedis }
	s.bindingByID.now = s.clock
	return s, nil
}

func (s *GroupAffinityService) warn(event string, err error, fields map[string]any, message string) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["event"] = event
	if err != nil {
		fields["error"] = err.Error()
	}
	s.logger.Warn(fields, message)
}

// canonicalGroupAffinityKeyInput 以长度前缀编码拼接 canonicalizer 段，防拼接
// 歧义（§3.1：顺序固定 purpose, systemAccountID, apiKeyID, routeStrategyID,
// conversationKey）。
func canonicalGroupAffinityKeyInput(purpose string, input GroupAffinityKeyInput) []byte {
	buf := make([]byte, 0, 160)
	appendField := func(value string) {
		buf = append(buf, strconv.Itoa(len(value))...)
		buf = append(buf, ':')
		buf = append(buf, value...)
	}
	appendField(purpose)
	appendField(input.SystemAccountID)
	appendField(input.APIKeyID)
	appendField(input.RouteStrategyID)
	appendField(input.ConversationKey)
	return buf
}

// DeriveGroupAffinityKey 从会话身份派生组亲和键。无 conversationKey 返回空
// 串（调用方视作黏性不适用）。
func (s *GroupAffinityService) DeriveGroupAffinityKey(input GroupAffinityKeyInput) (string, error) {
	if input.ConversationKey == "" {
		return "", nil
	}
	if s.cfg.Secret == "" {
		return "", errors.New("group-affinity:v1 HMAC secret must not be empty")
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.Secret))
	mac.Write(canonicalGroupAffinityKeyInput(GroupAffinityHMACPurpose, input))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// DeriveGroupAffinityKeyParts 是键派生的平参形式，与
// gatewaypreauth.GroupAffinityPort 的投影签名对齐（gatewaysession 反向依赖
// gatewaypreauth，编排层以端口接口消费本服务，不 import 本包）。
func (s *GroupAffinityService) DeriveGroupAffinityKeyParts(systemAccountID, apiKeyID, routeStrategyID, conversationKey string) (string, error) {
	return s.DeriveGroupAffinityKey(GroupAffinityKeyInput{
		SystemAccountID: systemAccountID,
		APIKeyID:        apiKeyID,
		RouteStrategyID: routeStrategyID,
		ConversationKey: conversationKey,
	})
}

// shouldUseRedisGroupAffinity mirrors shouldUseRedisSessionAffinity.
func (s *GroupAffinityService) shouldUseRedisGroupAffinity() bool {
	return s.cfg.CacheDriver == CacheDriverRedis
}

func (s *GroupAffinityService) redisGroupAffinityClient(ctx context.Context) (RedisClient, error) {
	if s.redis == nil {
		return nil, errors.New("group-affinity:v1 Redis cache driver requires JUHE_AI_REDIS_CACHE_URL")
	}
	_ = ctx
	return s.redis, nil
}

func redisGroupAffinityBindingKey(namespace string, groupAffinityKey string) (string, error) {
	prefix, err := RedisNamespacedKey(namespace, "juhe-ai:session-affinity:group-binding:")
	if err != nil {
		return "", err
	}
	return prefix + redisSessionAffinityKeyPart(groupAffinityKey), nil
}

func marshalGroupSessionBinding(binding GroupSessionBinding) string {
	encoded, err := json.Marshal(groupSessionBindingJSON{
		RouteStrategyID: binding.RouteStrategyID,
		GroupID:         binding.GroupID,
	})
	if err != nil {
		// 结构体载荷不会失败 marshaling。
		return "{}"
	}
	return string(encoded)
}

// groupSessionBindingJSON 与账户级 sessionBindingJSON 同风格：固定字段名，
// 未知字段忽略，解析失败整体丢弃。
type groupSessionBindingJSON struct {
	RouteStrategyID string `json:"routeStrategyId"`
	GroupID         string `json:"groupId"`
}

func parseGroupSessionBinding(rawValue string) *GroupSessionBinding {
	var parsed groupSessionBindingJSON
	if err := json.Unmarshal([]byte(rawValue), &parsed); err != nil {
		return nil
	}
	binding := GroupSessionBinding{
		RouteStrategyID: strings.TrimSpace(parsed.RouteStrategyID),
		GroupID:         strings.TrimSpace(parsed.GroupID),
	}
	if binding.GroupID == "" {
		// 无 GroupID 的记录不可用：删除并视同未命中。
		return nil
	}
	return &binding
}

// StickyGroupAsync 读取组绑定（读不刷 TTL，§3.2）。未命中或记录非法返回
// 空串；Redis 读失败降级为未命中并告警。
func (s *GroupAffinityService) StickyGroupAsync(ctx context.Context, groupAffinityKey string) (string, bool) {
	if groupAffinityKey == "" {
		return "", false
	}
	if !s.shouldUseRedisGroupAffinity() {
		s.mu.Lock()
		defer s.mu.Unlock()
		binding, ok := s.bindingByID.Get(groupAffinityKey)
		if !ok {
			return "", false
		}
		return binding.GroupID, true
	}
	client, err := s.redisGroupAffinityClient(ctx)
	if err != nil {
		s.warn("group_affinity_redis_unavailable", err, nil, "组亲和 Redis 客户端不可用，按未命中处理")
		return "", false
	}
	key, err := redisGroupAffinityBindingKey(s.cfg.RedisNamespace, groupAffinityKey)
	if err != nil {
		return "", false
	}
	rawValue, err := client.Get(ctx, key)
	if err != nil {
		s.warn("redis_group_affinity_read_failed", err, nil, "组亲和绑定读取失败，按未命中处理")
		return "", false
	}
	if rawValue == nil {
		return "", false
	}
	binding := parseGroupSessionBinding(*rawValue)
	if binding == nil {
		_ = client.Del(ctx, key)
		return "", false
	}
	return binding.GroupID, true
}

// RememberGroupAsync 写回组绑定（§5：写入即建即更，CAS ≤2 轮，冲突保留现值
// 并告警）。非法 groupID 拒绝写入。
func (s *GroupAffinityService) RememberGroupAsync(ctx context.Context, groupAffinityKey string, routeStrategyID string, groupID string) {
	if groupAffinityKey == "" || groupID == "" {
		return
	}
	binding := GroupSessionBinding{RouteStrategyID: routeStrategyID, GroupID: groupID}
	if !s.shouldUseRedisGroupAffinity() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.bindingByID.Set(groupAffinityKey, binding)
		return
	}
	client, err := s.redisGroupAffinityClient(ctx)
	if err != nil {
		s.warn("group_affinity_redis_unavailable", err, nil, "组亲和 Redis 客户端不可用，跳过写回")
		return
	}
	key, err := redisGroupAffinityBindingKey(s.cfg.RedisNamespace, groupAffinityKey)
	if err != nil {
		return
	}
	newValue := marshalGroupSessionBinding(binding)
	for round := 0; round < groupAffinityCASMaxRounds; round++ {
		priorRaw, err := client.Get(ctx, key)
		if err != nil {
			s.warn("redis_group_affinity_read_failed", err, nil, "组亲和写回前置读取失败，跳过写回")
			return
		}
		expected := redisMissingGroupBindingExpectedValue
		if priorRaw != nil {
			expected = *priorRaw
		}
		result, err := client.Eval(ctx, redisSetGroupAffinityBindingScript, []string{key},
			expected,
			newValue,
			strconv.FormatInt(groupAffinityRedisTtlMs, 10),
		)
		if err != nil {
			s.warn("redis_group_affinity_write_failed", err, nil, "组亲和绑定写回失败")
			return
		}
		if redisBooleanResult(result) {
			return
		}
	}
	s.warn("redis_group_affinity_cas_conflict", nil, nil, "组亲和绑定 CAS 两轮冲突，保留现值")
}

// ForgetGroupAsync 删除组绑定（三层删除的组层，§3.3/§5.2）。
func (s *GroupAffinityService) ForgetGroupAsync(ctx context.Context, groupAffinityKey string) {
	if groupAffinityKey == "" {
		return
	}
	if !s.shouldUseRedisGroupAffinity() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.bindingByID.Delete(groupAffinityKey)
		return
	}
	client, err := s.redisGroupAffinityClient(ctx)
	if err != nil {
		return
	}
	key, err := redisGroupAffinityBindingKey(s.cfg.RedisNamespace, groupAffinityKey)
	if err != nil {
		return
	}
	_ = client.Del(ctx, key)
}

// redisSetGroupAffinityBindingScript 与账户级 set 脚本同语义的简化版：
// expected（missing 为空串）匹配才 SET PX，否则返回 0（调用方重读重试）。
const redisSetGroupAffinityBindingScript = `
      local current = redis.call('GET', KEYS[1])
      if type(current) ~= 'string' then
        current = ''
      end
      if current ~= ARGV[1] then
        return 0
      end
      redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
      return 1
    `
