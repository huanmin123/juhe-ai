package circuitstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
)

// 账户并发 overlay 对账（opsjobs.OverlayReconciler 完整实现）：
//   - Redis 半边移植 shared/account-concurrency.ts 的投影 dirty 队列与
//     快照读取（键与 Lua 逐字节一致，jobs 只读不写并发计数）：
//       juhe-ai:{ns}:account-concurrency-v2:{accountId}:{total|text|image|metadata}
//       juhe-ai:{ns}:account-concurrency-projection-dirty       （ZSET）
//       juhe-ai:{ns}:account-concurrency-projection-generation  （HASH）
//   - PG 半边复用 ListAvailabilityRepo（tombstone 存在性 + overlay upsert）。

// OverlayRedisConfig 是 overlay 对账的 Redis 连接约定。
type OverlayRedisConfig struct {
	URL       string
	Namespace string
}

// OverlayRedisStore 是 account-concurrency 投影 dirty/快照的只读 Redis 面。
// pendingGenerations 暂存 ListDirtyEntries 认领到的代次：Node 在 entries 里
// 直接携带 generation（Redis 成对返回值），而 opsjobs.OverlayEntry 只有
// AccountID/NextReconcileAt 两个出口字段；为不改 port 契约，本 store 以实例
// 内 map 保存（对账在单 goroutine 内顺序调用 ListDirtyEntries → Acknowledge）。
type OverlayRedisStore struct {
	client    redis.Cmdable
	namespace string

	generationsMu sync.Mutex
	// generationsMu 保护 generations。
	generations map[string]int64
}

// NewOverlayRedisStore 建立连接（Client 注入供测试）。
func NewOverlayRedisStore(config OverlayRedisConfig, client redis.Cmdable) (*OverlayRedisStore, error) {
	var cmdable redis.Cmdable = client
	if cmdable == nil {
		if strings.TrimSpace(config.URL) == "" {
			return nil, errors.New("账户并发 overlay 对账缺少 Redis URL")
		}
		options, err := redis.ParseURL(config.URL)
		if err != nil {
			return nil, fmt.Errorf("解析账户并发 overlay Redis URL: %w", err)
		}
		cmdable = redis.NewClient(options)
	}
	return &OverlayRedisStore{client: cmdable, namespace: config.Namespace, generations: map[string]int64{}}, nil
}

// Close 释放连接（Client 注入时 no-op 的保守实现：go-redis 的 Cmdable 无法
// 统一关闭；组合根以独立连接构造时负责 Close）。
func (s *OverlayRedisStore) Close() error {
	if client, ok := s.client.(*redis.Client); ok && client != nil {
		return client.Close()
	}
	return nil
}

func (s *OverlayRedisStore) namespaced(key string) string {
	return redisNamespacedKey(key, s.namespace)
}

func (s *OverlayRedisStore) concurrencyKey(accountID, suffix string) string {
	return s.namespaced("juhe-ai:account-concurrency-v2:" + accountID + ":" + suffix)
}

func (s *OverlayRedisStore) dirtyKey() string {
	return s.namespaced("juhe-ai:account-concurrency-projection-dirty")
}

func (s *OverlayRedisStore) generationKey() string {
	return s.namespaced("juhe-ai:account-concurrency-projection-generation")
}

// listDirtyScript 对齐 redisListAccountConcurrencyProjectionDirtyScript。
const listDirtyScript = `
local limit = tonumber(ARGV[1])
local entries = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', '+inf', 'LIMIT', 0, limit)
local results = {}
for _, account_id in ipairs(entries) do
  local generation = redis.call('HGET', KEYS[2], account_id)
  if generation ~= false then
    table.insert(results, account_id)
    table.insert(results, generation)
  else
    redis.call('ZREM', KEYS[1], account_id)
  end
end
return results
`

// snapshotScript 对齐 redisLoadAccountConcurrencyProjectionSnapshotScript。
const snapshotScript = `
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local results = {}

local function hdel_expired(metadata_key, expired)
  local index = 1
  while index <= #expired do
    local last = math.min(index + 199, #expired)
    redis.call('HDEL', metadata_key, unpack(expired, index, last))
    index = last + 1
  end
end

for key_index = 1, #KEYS, 5 do
  local expired = redis.call('ZRANGEBYSCORE', KEYS[key_index], '-inf', now_ms)
  if #expired > 0 then
    hdel_expired(KEYS[key_index + 4], expired)
  end
  redis.call('ZREMRANGEBYSCORE', KEYS[key_index], '-inf', now_ms)
  redis.call('ZREMRANGEBYSCORE', KEYS[key_index + 1], '-inf', now_ms)
  redis.call('ZREMRANGEBYSCORE', KEYS[key_index + 2], '-inf', now_ms)
  if redis.call('ZCARD', KEYS[key_index]) == 0 then redis.call('DEL', KEYS[key_index]) end
  if redis.call('ZCARD', KEYS[key_index + 1]) == 0 then redis.call('DEL', KEYS[key_index + 1]) end
  if redis.call('ZCARD', KEYS[key_index + 2]) == 0 then redis.call('DEL', KEYS[key_index + 2]) end
  if redis.call('HLEN', KEYS[key_index + 4]) == 0 then redis.call('DEL', KEYS[key_index + 4]) end
  table.insert(results, redis.call('ZCARD', KEYS[key_index + 3]))
  local next_expiry = redis.call('ZRANGE', KEYS[key_index], 0, 0, 'WITHSCORES')
  table.insert(results, tonumber(next_expiry[2]) or 0)
end
return results
`

// acknowledgeScript 对齐 redisAcknowledgeAccountConcurrencyProjectionDirtyScript。
const acknowledgeScript = `
local now_ms = tonumber(ARGV[1])
for arg_index = 2, #ARGV, 3 do
  local account_id = ARGV[arg_index]
  local expected_generation = ARGV[arg_index + 1]
  local next_reconcile_at_ms = tonumber(ARGV[arg_index + 2]) or 0
  local current_generation = redis.call('HGET', KEYS[2], account_id)
  if current_generation ~= false and current_generation == expected_generation then
    if next_reconcile_at_ms > now_ms then
      redis.call('ZADD', KEYS[1], next_reconcile_at_ms, account_id)
    else
      redis.call('ZREM', KEYS[1], account_id)
      redis.call('HDEL', KEYS[2], account_id)
    end
  end
end
return 1
`

// ListDirtyEntries 对齐 listAccountConcurrencyProjectionDirtyEntriesAsync。
func (s *OverlayRedisStore) ListDirtyEntries(ctx context.Context, limit int) ([]opsjobs.OverlayEntry, error) {
	normalized := limit
	if normalized < 1 {
		normalized = 1
	}
	if normalized > 1000 {
		normalized = 1000
	}
	values, err := s.client.Eval(ctx, listDirtyScript, []string{s.dirtyKey(), s.generationKey()}, normalized).Slice()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	entries := []opsjobs.OverlayEntry{}
	generations := map[string]int64{}
	for index := 0; index+1 < len(values); index += 2 {
		accountID := strings.TrimSpace(fmt.Sprintf("%v", values[index]))
		generation, parseErr := strconv.ParseInt(strings.TrimSpace(fmt.Sprintf("%v", values[index+1])), 10, 64)
		if parseErr != nil {
			continue
		}
		if accountID == "" || generation < 1 {
			continue
		}
		entries = append(entries, opsjobs.OverlayEntry{AccountID: accountID})
		generations[accountID] = generation
	}
	s.generationsMu.Lock()
	s.generations = generations
	s.generationsMu.Unlock()
	return entries, nil
}

func (s *OverlayRedisStore) trackedGeneration(accountID string) int64 {
	s.generationsMu.Lock()
	defer s.generationsMu.Unlock()
	return s.generations[accountID]
}

// Acknowledge 对齐 acknowledgeAccountConcurrencyProjectionDirtyEntriesAsync：
// 仅当 generation 未变化时确认（future expiry 保留为重对账计划）。
func (s *OverlayRedisStore) Acknowledge(ctx context.Context, entries []opsjobs.OverlayEntry) error {
	if len(entries) == 0 {
		return nil
	}
	args := []any{time.Now().UnixMilli()}
	for _, entry := range entries {
		nextReconcileAtMS := int64(0)
		if entry.NextReconcileAt != nil && strings.TrimSpace(*entry.NextReconcileAt) != "" {
			parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*entry.NextReconcileAt))
			if err != nil {
				return errors.New("账户并发 nextReconcileAt 必须是带 offset 的 RFC3339 时间")
			}
			nextReconcileAtMS = parsed.UnixMilli()
		}
		args = append(args, entry.AccountID, s.trackedGeneration(entry.AccountID), nextReconcileAtMS)
	}
	return s.client.Eval(ctx, acknowledgeScript, []string{s.dirtyKey(), s.generationKey()}, args...).Err()
}

// LoadSnapshots 对齐 loadAccountConcurrencyProjectionSnapshotsAsync（分批
// 100、Redis TIME 清理过期槽、返回当前并发与最早槽到期）。
func (s *OverlayRedisStore) LoadSnapshots(ctx context.Context, accountIDs []string) ([]opsjobs.OverlaySnapshot, error) {
	ids, err := normalizedIDList(accountIDs)
	if err != nil {
		return nil, err
	}
	output := []opsjobs.OverlaySnapshot{}
	for index := 0; index < len(ids); index += 100 {
		end := index + 100
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[index:end]
		var keys []string
		for _, accountID := range chunk {
			keys = append(keys,
				s.concurrencyKey(accountID, "total"),
				s.concurrencyKey(accountID, "text"),
				s.concurrencyKey(accountID, "image"),
				s.concurrencyKey(accountID, "total"),
				s.concurrencyKey(accountID, "metadata"))
		}
		raw, err := s.client.Eval(ctx, snapshotScript, keys).Result()
		if err != nil {
			return nil, err
		}
		values, err := redisInt64Slice(raw)
		if err != nil {
			return nil, err
		}
		for chunkIndex, accountID := range chunk {
			valueOffset := chunkIndex * 2
			current := int64(0)
			nextExpiry := int64(0)
			if valueOffset < len(values) {
				current = values[valueOffset]
			}
			if valueOffset+1 < len(values) {
				nextExpiry = values[valueOffset+1]
			}
			snapshot := opsjobs.OverlaySnapshot{AccountID: accountID, CurrentConcurrency: int(current)}
			if nextExpiry > time.Now().UnixMilli() {
				value := time.UnixMilli(nextExpiry).UTC().Format(time.RFC3339Nano)
				snapshot.NextReconcileAt = &value
			}
			output = append(output, snapshot)
		}
	}
	return output, nil
}

func redisInt64Slice(raw any) ([]int64, error) {
	switch typed := raw.(type) {
	case []any:
		values := make([]int64, 0, len(typed))
		for _, item := range typed {
			switch value := item.(type) {
			case int64:
				values = append(values, value)
			case string:
				var parsed int64
				if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &parsed); err != nil {
					return nil, errors.New("Redis 并发快照数值返回无效")
				}
				values = append(values, parsed)
			default:
				return nil, errors.New("Redis 并发快照数值返回无效")
			}
		}
		return values, nil
	case []int64:
		return typed, nil
	default:
		return nil, errors.New("Redis 并发快照数值返回无效")
	}
}

// OverlayReconciler 组合实现 opsjobs.OverlayReconciler。
type OverlayReconciler struct {
	redis *OverlayRedisStore
	repo  *ListAvailabilityRepo
}

// NewOverlayReconciler 组合 Redis 只读半边与 PG 持久化半边。
func NewOverlayReconciler(overlay *OverlayRedisStore, repo *ListAvailabilityRepo) *OverlayReconciler {
	if overlay == nil || repo == nil {
		return nil
	}
	return &OverlayReconciler{redis: overlay, repo: repo}
}

// ListDirtyEntries 实现 port。
func (o *OverlayReconciler) ListDirtyEntries(ctx context.Context, limit int) ([]opsjobs.OverlayEntry, error) {
	return o.redis.ListDirtyEntries(ctx, limit)
}

// Acknowledge 实现 port。
func (o *OverlayReconciler) Acknowledge(ctx context.Context, entries []opsjobs.OverlayEntry) error {
	return o.redis.Acknowledge(ctx, entries)
}

// LoadSnapshots 实现 port。
func (o *OverlayReconciler) LoadSnapshots(ctx context.Context, accountIDs []string) ([]opsjobs.OverlaySnapshot, error) {
	return o.redis.LoadSnapshots(ctx, accountIDs)
}

// ExistingAccountIDs 实现 port（tombstone 安全确认的存在性半边）。
func (o *OverlayReconciler) ExistingAccountIDs(ctx context.Context, accountIDs []string) (map[string]struct{}, error) {
	ids, err := normalizedIDList(accountIDs)
	if err != nil {
		return nil, err
	}
	existing := map[string]struct{}{}
	if len(ids) == 0 {
		return existing, nil
	}
	placeholders := placeholdersFor(len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := o.repo.db.QueryContext(ctx, `
    SELECT id FROM `+o.repo.table("accounts")+`
    WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		existing[id] = struct{}{}
	}
	return existing, rows.Err()
}

// UpsertOverlays 对齐 upsertAccountListAvailabilityRuntimeOverlaysInClient。
func (o *OverlayReconciler) UpsertOverlays(ctx context.Context, overlays []opsjobs.OverlayUpsert) error {
	byAccountID := map[string]opsjobs.OverlayUpsert{}
	ordered := make([]opsjobs.OverlayUpsert, 0, len(overlays))
	for _, entry := range overlays {
		accountID := strings.TrimSpace(entry.AccountID)
		if accountID == "" || len(accountID) > 256 {
			return errors.New("accountId 长度必须为 1..256")
		}
		if entry.CurrentConcurrency < 0 {
			return errors.New("currentConcurrency 必须是非负整数")
		}
		observedAt := strings.TrimSpace(entry.ObservedAt)
		if observedAt == "" || len(observedAt) > 64 {
			return errors.New("observedAt 长度必须为 1..64")
		}
		if _, exists := byAccountID[accountID]; !exists {
			ordered = append(ordered, entry)
		}
		byAccountID[accountID] = entry
	}
	if len(ordered) == 0 {
		return nil
	}
	valueClauses := make([]string, 0, len(ordered))
	args := make([]any, 0, len(ordered)*4)
	for _, entry := range ordered {
		valueClauses = append(valueClauses, "(?, ?, ?, ?)")
		args = append(args,
			strings.TrimSpace(entry.AccountID),
			entry.CurrentConcurrency,
			strings.TrimSpace(entry.ObservedAt),
			textPtrOrEmpty(entry.NextReconcileAt))
	}
	query := `
    INSERT INTO ` + o.repo.table("account_list_availability_runtime_overlays") + ` (
      account_id, current_concurrency, observed_at, next_reconcile_at
    ) VALUES ` + strings.Join(valueClauses, ", ") + `
    ON CONFLICT(account_id) DO UPDATE SET
      current_concurrency = excluded.current_concurrency,
      observed_at = excluded.observed_at,
      next_reconcile_at = excluded.next_reconcile_at`
	_, err := o.repo.db.ExecContext(ctx, query, args...)
	return err
}

func textPtrOrEmpty(value *string) any {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return normalized
}
