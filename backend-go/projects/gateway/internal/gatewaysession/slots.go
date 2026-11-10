package gatewaysession

// 会话名额（soft claim）Redis 实现：每账户一个 ZSET，member=sessionAffinityKey，
// score=leaseUntilMs。名额只控制“账户记住多少会话”，不占并发、不占 RPM、
// 不作为拒绝依据；并发/RPM 仍由 dispatch TryAcquire 决定。
//
// 容量公式（内部常量，不做用户配置）：
//
//	concurrencyBound = C × 4
//	rpmBound         = R > 0 ? floor(R / 2) : +∞
//	slotCap          = clamp(min(concurrencyBound, rpmBound), 32, 10000)
//
// 其中 4 = 每个并发位最多记住 4 个会话，2 = 活跃会话平均每分钟请求数。

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

const (
	sessionAffinitySlotMinCap                       = 32
	sessionAffinitySlotMaxCap                       = 10000
	sessionAffinitySlotSessionsPerConcurrency       = 4
	sessionAffinitySlotRequestsPerMinutePerSession  = 2
	sessionAffinitySlotWaitPollMs                   = int64(200)
	sessionAffinitySlotEvictedBindingCleanupTimeout = 5 * time.Second
)

// SessionSlotCandidate 是名额规划需要的账户容量快照。
type SessionSlotCandidate struct {
	AccountID        string
	ConcurrencyLimit int
	RPMLimit         int
}

// SessionSlotPlan 是准备期的名额规划结果。PreferredAccountID 会被调用方
// 提到候选首位；SlotActive 表示会话原本就持有该账户名额；Fallback 表示首选
// 已不是原 owner（成功后可 CAS 迁移绑定，reason=slot_fallback）。
type SessionSlotPlan struct {
	PreferredAccountID string
	SlotActive         bool
	Fallback           bool
	WaitedMs           int64
}

// SessionSlotCap 返回账户的会话名额软上限。
func SessionSlotCap(concurrencyLimit, rpmLimit int) int {
	concurrency := concurrencyLimit
	if concurrency < 1 {
		concurrency = 1
	}
	bound := concurrency * sessionAffinitySlotSessionsPerConcurrency
	if rpmLimit > 0 {
		rpmBound := rpmLimit / sessionAffinitySlotRequestsPerMinutePerSession
		if rpmBound < bound {
			bound = rpmBound
		}
	}
	if bound < sessionAffinitySlotMinCap {
		bound = sessionAffinitySlotMinCap
	}
	if bound > sessionAffinitySlotMaxCap {
		bound = sessionAffinitySlotMaxCap
	}
	return bound
}

func sessionAffinitySlotKey(namespace, accountID string) (string, error) {
	prefix, err := RedisNamespacedKey(namespace, "juhe-ai:session-affinity:slot:")
	if err != nil {
		return "", err
	}
	return prefix + redisSessionAffinityKeyPart(accountID), nil
}

const redisProbeSessionAffinitySlotsScript = `
local out = {}
for i = 1, #KEYS do
  redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', ARGV[1])
  local score = redis.call('ZSCORE', KEYS[i], ARGV[2])
  out[#out + 1] = redis.call('ZCARD', KEYS[i])
  if score then
    out[#out + 1] = 1
  else
    out[#out + 1] = 0
  end
end
return out
`

const redisAcquireSessionAffinitySlotScript = `
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
local existing = redis.call('ZSCORE', KEYS[1], ARGV[3])
if existing then
  redis.call('ZADD', KEYS[1], ARGV[2], ARGV[3])
  redis.call('PEXPIRE', KEYS[1], ARGV[5])
  return {'refreshed', ''}
end
local evicted = ''
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[4]) then
  local oldest = redis.call('ZPOPMIN', KEYS[1])
  if oldest and oldest[1] then
    evicted = oldest[1]
  end
end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[5])
return {'acquired', evicted}
`

const redisReleaseSessionAffinitySlotScript = `
redis.call('ZREM', KEYS[1], ARGV[1])
return 1
`

// touch 只刷新已存在 member 的 score/TTL：请求开始即续租名额，使 LRU 驱逐
// 选择“最久未请求”的会话，避免把有在途请求的旧会话当成冷会话驱逐。
const redisTouchSessionAffinitySlotScript = `
if redis.call('ZSCORE', KEYS[1], ARGV[2]) then
  redis.call('ZADD', KEYS[1], ARGV[1], ARGV[2])
  redis.call('PEXPIRE', KEYS[1], ARGV[3])
  return 1
end
return 0
`

// PlanSessionSlot 在请求准备期选择名额归属：
//  1. owner 已持有名额或账户名额未满 → 首选 owner；
//  2. owner 未持有名额且是既有会话（claim 前有活跃 lease）：以 200ms 轮询
//     等待 waitBudgetMs，期间释放则仍选 owner；
//  3. owner 未持有名额且是新会话/零 lease 占位：不等待，直接选空闲名额账户；
//  4. 等待耗尽：按 candidates 顺序找第一个有空名额的账户（调用方把 owner
//     同层账户排在其他层之前）；全满则保留 owner，成功结算时再做 LRU。
//
// 新会话没有 owner（ownerAccountID 为空）：直接选第一个有空名额的账户。
func (s *AffinityService) PlanSessionSlot(ctx context.Context, sessionAffinityKey, ownerAccountID string, ownerEstablished bool, candidates []SessionSlotCandidate, waitBudgetMs int64) (SessionSlotPlan, error) {
	if sessionAffinityKey == "" || len(candidates) == 0 || !s.shouldUseRedisSessionAffinity() {
		return SessionSlotPlan{}, nil
	}
	if ownerAccountID != "" {
		found := false
		for _, candidate := range candidates {
			if candidate.AccountID == ownerAccountID {
				found = true
				break
			}
		}
		if !found {
			// owner 已被硬过滤：P0 不在这里改变绑定/顺序。
			return SessionSlotPlan{}, nil
		}
	}
	nowMs := s.clock().UnixMilli()
	counts, active, err := s.probeSessionSlots(ctx, sessionAffinityKey, candidates, nowMs)
	if err != nil {
		gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
		s.warn("redis_session_affinity_slot_probe_failed", err, nil, "会话名额探测失败，已跳过名额规划")
		return SessionSlotPlan{}, nil
	}
	ownerIndex := -1
	if ownerAccountID != "" {
		for index, candidate := range candidates {
			if candidate.AccountID == ownerAccountID {
				ownerIndex = index
				break
			}
		}
	}
	if ownerIndex >= 0 {
		ownerCap := SessionSlotCap(candidates[ownerIndex].ConcurrencyLimit, candidates[ownerIndex].RPMLimit)
		if active[ownerIndex] || counts[ownerIndex] < ownerCap {
			if active[ownerIndex] {
				// 请求开始即续租名额：LRU 按“最近请求”而非仅“最近成功”
				// 排序，降低在途会话被并发 Confirm 驱逐的概率。
				s.touchSessionSlot(ctx, sessionAffinityKey, ownerAccountID, nowMs)
			}
			return SessionSlotPlan{PreferredAccountID: ownerAccountID, SlotActive: active[ownerIndex]}, nil
		}
		if !active[ownerIndex] && !ownerEstablished {
			// 新会话的 claim 只是占位 owner，并没有为它保留名额：不为占位等待，
			// 直接把新会话分到有空名额的账户，避免高优先级账户先被新会话占满。
			for index, candidate := range candidates {
				if index == ownerIndex {
					continue
				}
				cap := SessionSlotCap(candidate.ConcurrencyLimit, candidate.RPMLimit)
				if counts[index] < cap {
					return SessionSlotPlan{PreferredAccountID: candidate.AccountID, Fallback: true}, nil
				}
			}
			return SessionSlotPlan{PreferredAccountID: ownerAccountID}, nil
		}
		waitedMs := int64(0)
		if waitBudgetMs > 0 {
			for waitedMs < waitBudgetMs {
				step := sessionAffinitySlotWaitPollMs
				if remaining := waitBudgetMs - waitedMs; remaining < step {
					step = remaining
				}
				if err := waitSessionSlotPoll(ctx, step); err != nil {
					return SessionSlotPlan{PreferredAccountID: ownerAccountID, WaitedMs: waitedMs}, err
				}
				waitedMs += step
				count, active, probeErr := s.probeSessionSlots(ctx, sessionAffinityKey, candidates[ownerIndex:ownerIndex+1], s.clock().UnixMilli())
				if probeErr != nil {
					gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
					s.warn("redis_session_affinity_slot_wait_probe_failed", probeErr, nil, "会话名额等待探测失败，已停止等待")
					break
				}
				// 并发同会话请求可能已成功并把 member 写回 owner 表；active 为真
				// 说明本会话重新持有名额，无需继续等待。
				if len(count) == 1 && (active[0] || count[0] < ownerCap) {
					gatewayusage.RecordGatewaySessionAffinitySlotWaitMetric(waitedMs)
					return SessionSlotPlan{PreferredAccountID: ownerAccountID, WaitedMs: waitedMs}, nil
				}
			}
		}
		if waitedMs > 0 {
			gatewayusage.RecordGatewaySessionAffinitySlotWaitMetric(waitedMs)
		}
		// 等待结束后重新探测整池，避免用超时前的陈旧计数选回退账户；owner
		// 在这段时间内释放名额时仍优先 owner。
		if freshCounts, freshActive, probeErr := s.probeSessionSlots(ctx, sessionAffinityKey, candidates, s.clock().UnixMilli()); probeErr == nil && len(freshCounts) == len(counts) {
			counts = freshCounts
			ownerCap := SessionSlotCap(candidates[ownerIndex].ConcurrencyLimit, candidates[ownerIndex].RPMLimit)
			if freshActive[ownerIndex] || freshCounts[ownerIndex] < ownerCap {
				return SessionSlotPlan{PreferredAccountID: ownerAccountID, SlotActive: freshActive[ownerIndex], WaitedMs: waitedMs}, nil
			}
		}
		for index, candidate := range candidates {
			if index == ownerIndex {
				continue
			}
			cap := SessionSlotCap(candidate.ConcurrencyLimit, candidate.RPMLimit)
			if counts[index] < cap {
				return SessionSlotPlan{PreferredAccountID: candidate.AccountID, Fallback: true, WaitedMs: waitedMs}, nil
			}
		}
		// 全池名额都满：保留原 owner，成功结算时 LRU 驱逐最久空闲会话。
		return SessionSlotPlan{PreferredAccountID: ownerAccountID, WaitedMs: waitedMs}, nil
	}
	// 新会话没有原 owner：直接选第一个有空名额的账户。index>0 不是“从 owner
	// 回退”，不计 fallback 指标，也不触发 slot_fallback 语义。
	for index, candidate := range candidates {
		cap := SessionSlotCap(candidate.ConcurrencyLimit, candidate.RPMLimit)
		if counts[index] < cap {
			return SessionSlotPlan{PreferredAccountID: candidate.AccountID}, nil
		}
	}
	return SessionSlotPlan{PreferredAccountID: candidates[0].AccountID}, nil
}

func (s *AffinityService) probeSessionSlots(ctx context.Context, sessionAffinityKey string, candidates []SessionSlotCandidate, nowMs int64) ([]int, []bool, error) {
	client, err := s.redisSessionAffinityClient(ctx)
	if err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		key, keyErr := sessionAffinitySlotKey(s.cfg.RedisNamespace, candidate.AccountID)
		if keyErr != nil {
			return nil, nil, keyErr
		}
		keys = append(keys, key)
	}
	raw, err := client.Eval(ctx, redisProbeSessionAffinitySlotsScript, keys, strconv.FormatInt(nowMs, 10), sessionAffinityKey)
	if err != nil {
		return nil, nil, err
	}
	values, ok := raw.([]any)
	if !ok || len(values) != 2*len(candidates) {
		return nil, nil, errors.New("会话名额探测返回值格式无效")
	}
	counts := make([]int, len(candidates))
	active := make([]bool, len(candidates))
	for index := range candidates {
		counts[index] = int(evalInt64(values[index*2]))
		active[index] = evalInt64(values[index*2+1]) == 1
	}
	return counts, active, nil
}

func evalInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case string:
		parsed, _ := strconv.ParseInt(typed, 10, 64)
		return parsed
	default:
		return 0
	}
}

func waitSessionSlotPoll(ctx context.Context, delayMs int64) error {
	if delayMs <= 0 {
		return nil
	}
	timer := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ConfirmSessionSlot 在最终协议成功后写入/刷新新 owner 名额，并在 owner 变化
// 时释放旧名额；名额满时 LRU 弹出一个最久空闲会话（score 最小）。
func (s *AffinityService) ConfirmSessionSlot(ctx context.Context, sessionAffinityKey, expectedOwner, newOwner string, cap int) bool {
	if sessionAffinityKey == "" || newOwner == "" || !s.shouldUseRedisSessionAffinity() {
		return false
	}
	cap = sessionSlotCapClamp(cap)
	client, err := s.redisSessionAffinityClient(ctx)
	if err != nil {
		gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
		return false
	}
	// 名额跟随 binding 权威：只有最终成功账户已经是当前 binding owner 时才
	// 写入/迁移名额。跨层成功未重绑、rebind CAS 冲突或 binding 已删除时，
	// 保留原 owner 名额，避免在成功账户上积累永不回访的 stale 名额。
	record, err := s.getRedisSessionAffinityRecord(ctx, sessionAffinityKey, false)
	if err != nil {
		gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
		s.warn("redis_session_affinity_slot_binding_read_failed", err, map[string]any{"accountId": newOwner}, "会话名额结算读取绑定失败，已跳过本次名额写入")
		return false
	}
	if record == nil || record.binding.AccountID != newOwner {
		return false
	}
	slotKey, err := sessionAffinitySlotKey(s.cfg.RedisNamespace, newOwner)
	if err != nil {
		gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
		return false
	}
	nowMs := s.clock().UnixMilli()
	raw, err := client.Eval(ctx, redisAcquireSessionAffinitySlotScript, []string{slotKey},
		strconv.FormatInt(nowMs, 10),
		strconv.FormatInt(nowMs+sessionAffinityLeaseTtlMs, 10),
		sessionAffinityKey,
		strconv.Itoa(cap),
		strconv.FormatInt(sessionAffinityTtlMs+redisSessionAffinityIndexTtlPaddingMs, 10),
	)
	if err != nil {
		gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
		s.warn("redis_session_affinity_slot_acquire_failed", err, map[string]any{"accountId": newOwner}, "会话名额写入失败，请求结果不受影响")
		return false
	}
	evictedKey := ""
	if values, ok := raw.([]any); ok && len(values) >= 2 {
		evictedKey = stringValueFromEval(values[1])
	}
	if evictedKey != "" && evictedKey != sessionAffinityKey {
		gatewayusage.RecordGatewaySessionAffinitySlotEvictionMetric()
		s.cleanupEvictedSessionSlotBindingAsync(ctx, evictedKey, newOwner)
	}
	if expectedOwner != "" && expectedOwner != newOwner {
		s.releaseSessionSlot(ctx, sessionAffinityKey, expectedOwner)
	}
	return true
}

func sessionSlotCapClamp(cap int) int {
	if cap < sessionAffinitySlotMinCap {
		return sessionAffinitySlotMinCap
	}
	if cap > sessionAffinitySlotMaxCap {
		return sessionAffinitySlotMaxCap
	}
	return cap
}

func stringValueFromEval(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

func (s *AffinityService) cleanupEvictedSessionSlotBindingAsync(ctx context.Context, sessionAffinityKey, evictedAccountID string) {
	cleanupCtx := context.WithoutCancel(ctx)
	go func() {
		defer safego.Recover("gatewaysession.slot.evicted_binding_cleanup")
		timeoutCtx, cancel := context.WithTimeout(cleanupCtx, sessionAffinitySlotEvictedBindingCleanupTimeout)
		defer cancel()
		record, err := s.getRedisSessionAffinityRecord(timeoutCtx, sessionAffinityKey, false)
		if err != nil || record == nil {
			return
		}
		// 驱逐只释放该账户的名额；若会话已被并发重绑到其他账户，不得删除
		// 新 owner 的活跃绑定（binding CAS 是权威，slot 只是 advisory）。
		if record.binding.AccountID != evictedAccountID {
			return
		}
		client, err := s.redisSessionAffinityClient(timeoutCtx)
		if err != nil {
			s.warn("redis_session_affinity_slot_cleanup_client_failed", err, map[string]any{"accountId": evictedAccountID}, "被驱逐会话绑定清理失败，已跳过")
			return
		}
		if _, err := s.deleteRedisSessionAffinityBinding(timeoutCtx, client, sessionAffinityKey, record); err != nil {
			s.warn("redis_session_affinity_slot_cleanup_failed", err, map[string]any{"accountId": evictedAccountID}, "被驱逐会话绑定清理失败，已跳过")
		}
	}()
}

// touchSessionSlot 在请求开始时刷新既有名额的 score/TTL，作为 LRU 的在途
// 保护：有请求正在处理的旧会话不会因为“上次成功很久以前”被优先驱逐。
func (s *AffinityService) touchSessionSlot(ctx context.Context, sessionAffinityKey, accountID string, nowMs int64) {
	if sessionAffinityKey == "" || accountID == "" || !s.shouldUseRedisSessionAffinity() {
		return
	}
	client, err := s.redisSessionAffinityClient(ctx)
	if err != nil {
		gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
		return
	}
	slotKey, err := sessionAffinitySlotKey(s.cfg.RedisNamespace, accountID)
	if err != nil {
		gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
		return
	}
	if _, err := client.Eval(ctx, redisTouchSessionAffinitySlotScript, []string{slotKey},
		strconv.FormatInt(nowMs+sessionAffinityLeaseTtlMs, 10),
		sessionAffinityKey,
		strconv.FormatInt(sessionAffinityTtlMs+redisSessionAffinityIndexTtlPaddingMs, 10),
	); err != nil {
		gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
		s.warn("redis_session_affinity_slot_touch_failed", err, map[string]any{"accountId": accountID}, "会话名额续租失败，已跳过")
	}
}

// releaseSessionSlot 清理指定账户上的会话名额（绑定删除/迁移时调用）。名额是
// advisory：失败只告警，不影响绑定清理结果。
func (s *AffinityService) releaseSessionSlot(ctx context.Context, sessionAffinityKey, accountID string) {
	if sessionAffinityKey == "" || accountID == "" || !s.shouldUseRedisSessionAffinity() {
		return
	}
	client, err := s.redisSessionAffinityClient(ctx)
	if err != nil {
		gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
		return
	}
	slotKey, err := sessionAffinitySlotKey(s.cfg.RedisNamespace, accountID)
	if err != nil {
		gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
		return
	}
	if _, err := client.Eval(ctx, redisReleaseSessionAffinitySlotScript, []string{slotKey}, sessionAffinityKey); err != nil {
		gatewayusage.RecordGatewaySessionAffinitySlotFailureMetric()
		s.warn("redis_session_affinity_slot_release_failed", err, map[string]any{"accountId": accountID}, "会话名额释放失败，已跳过本次清理")
	}
}
