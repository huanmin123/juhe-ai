package circuitstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// codex 源探针 fence 结算（background_worker_codex_source_fence_settled 的
// jobs 侧等价物）：Node worker 经 IPC 把 durable outcome 发给网关，由网关
// availability-probe-coordinator 的 source fence CAS 结算；Go 单写者设计下
// jobs 直接读写同一批 Redis 键完成结算（不是第二状态形状）：
//
//	juhe-ai:{ns}:probe:gateway-availability-probe-coordinator:state:{runtimeKey}
//	juhe-ai:{ns}:probe:gateway-availability-probe-coordinator:generation:{runtimeKey}
//	juhe-ai:{ns}:probe:gateway-availability-probe-coordinator:due
//
// 语义对照 modules/gateway/runtime/availability-probe-coordinator.ts 的
// settleDispatchedAvailabilityProbeBySourceFence + settleAvailabilityProbe 与
// shared/runtime-probe-state-store.ts 的 Redis 脚本（本文件只取结算所需
// 子集；acquire/renew/merge 等生产侧脚本仍归网关单实现）。

// ProbeStoreName 与 Node createRuntimeProbeStateStore 名称一致（键空间互通）。
const ProbeStoreName = "gateway-availability-probe-coordinator"

// Probe lease/retention 默认值对齐 availability-probe-coordinator.ts。
const (
	ProbeDefaultLeaseMS     = int64(90_000)
	ProbeDefaultRetentionMS = int64(5 * 60_000)
)

// ProbeSourceFence mirrors AvailabilityProbeSourceFence.
type ProbeSourceFence struct {
	StateKey         string
	AccountID        string
	SourceGeneration int64
	SourceFenceID    string
}

// ProbeOutcome 取值镜像 AvailabilityProbeOutcome。
const (
	ProbeOutcomeSuccess          = "success"
	ProbeOutcomeHealthFailure    = "health_failure"
	ProbeOutcomeUnknown          = "unknown"
	ProbeOutcomeProbeTaskFailure = "probe_task_failure"
	ProbeOutcomeCanceled         = "canceled"
	ProbeOutcomeStale            = "stale"
)

// ProbeStateStore 是结算所需 shared/runtime-probe-state-store.ts Redis 子集。
type ProbeStateStore struct {
	client           redis.Cmdable
	namespace        string
	statePrefix      string
	generationPrefix string
	dueKey           string
}

// NewProbeStateStore 建立 Redis 连接（Client 注入供测试）。
func NewProbeStateStore(url, namespace string, client redis.Cmdable) (*ProbeStateStore, error) {
	var cmdable redis.Cmdable = client
	if cmdable == nil {
		if strings.TrimSpace(url) == "" {
			return nil, errors.New("探针运行态结算缺少 Redis URL")
		}
		options, err := redis.ParseURL(url)
		if err != nil {
			return nil, fmt.Errorf("解析探针运行态 Redis URL: %w", err)
		}
		cmdable = redis.NewClient(options)
	}
	safeName := sanitizeRedisName(ProbeStoreName)
	statePrefix := redisNamespacedKey("juhe-ai:probe:"+safeName+":state:", namespace)
	generationPrefix := redisNamespacedKey("juhe-ai:probe:"+safeName+":generation:", namespace)
	return &ProbeStateStore{
		client:           cmdable,
		namespace:        namespace,
		statePrefix:      statePrefix,
		generationPrefix: generationPrefix,
		dueKey:           redisNamespacedKey("juhe-ai:probe:"+safeName+":due", namespace),
	}, nil
}

// Close 释放自建连接。
func (s *ProbeStateStore) Close() error {
	if client, ok := s.client.(*redis.Client); ok && client != nil {
		return client.Close()
	}
	return nil
}

func sanitizeProbeKeyPart(value string) string {
	trimmed := strings.TrimSpace(value)
	var out strings.Builder
	for _, c := range trimmed {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == ':', c == '_', c == '-':
			out.WriteRune(c)
		default:
			out.WriteRune('_')
		}
	}
	if out.Len() == 0 {
		return "default"
	}
	return out.String()
}

func (s *ProbeStateStore) stateKey(runtimeKey string) string {
	return s.statePrefix + sanitizeProbeKeyPart(runtimeKey)
}

func (s *ProbeStateStore) generationKeyOf(runtimeKey string) string {
	return s.generationPrefix + sanitizeProbeKeyPart(runtimeKey)
}

// probeState mirrors AvailabilityProbeState（camelCase 与 Node 存储一致）。
type probeState struct {
	RuntimeKey             string    `json:"runtimeKey"`
	Generation             int64     `json:"generation"`
	NextProbeAtMs          int64     `json:"nextProbeAtMs"`
	AccountRuntimeScope    string    `json:"accountRuntimeScope"`
	ProbeKind              string    `json:"probeKind"`
	ConfigRevision         int64     `json:"configRevision"`
	ProbeRunID             *string   `json:"probeRunId,omitempty"`
	ProbeRunUntilMs        *int64    `json:"probeRunUntilMs,omitempty"`
	DispatchPending        *bool     `json:"dispatchPending,omitempty"`
	DispatchPendingUntilMs *int64    `json:"dispatchPendingUntilMs,omitempty"`
	Outcome                *string   `json:"outcome,omitempty"`
	CompletedAtMs          *int64    `json:"completedAtMs,omitempty"`
	SourceFences           *[]string `json:"sourceFences,omitempty"`
}

func (s *ProbeStateStore) get(ctx context.Context, runtimeKey string) (*probeState, error) {
	raw, err := s.client.Get(ctx, s.stateKey(runtimeKey)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state probeState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		// Node：损坏数据删除。
		_ = s.client.Del(ctx, s.stateKey(runtimeKey)).Err()
		return nil, nil
	}
	return &state, nil
}

const probeAcquireGenerationRunScript = `
local current = redis.call('GET', KEYS[1])
if not current then return '' end
local ok, decoded = pcall(cjson.decode, current)
if not ok or type(decoded) ~= 'table' then return '' end
if tonumber(decoded['generation']) ~= tonumber(ARGV[2]) then return '' end
local lease_id = decoded['halfOpenLeaseId']
local lease_until = tonumber(decoded['halfOpenLeaseUntilMs']) or 0
if lease_id and lease_id ~= cjson.null and lease_until > tonumber(ARGV[5]) then return '' end
local current_run_id = decoded['probeRunId']
local current_run_until = tonumber(decoded['probeRunUntilMs']) or 0
if current_run_id and current_run_id ~= ARGV[3] and current_run_until > tonumber(ARGV[5]) then return '' end
local previous_lease_next = tonumber(decoded['halfOpenPreviousNextProbeAtMs'])
local previous_run_next = tonumber(decoded['probeRunPreviousNextProbeAtMs'])
local previous_next = previous_lease_next or previous_run_next or tonumber(decoded['nextProbeAtMs']) or 0
decoded['nextProbeAtMs'] = math.max(previous_next, tonumber(ARGV[4]))
decoded['probeRunId'] = ARGV[3]
decoded['probeRunUntilMs'] = tonumber(ARGV[4])
decoded['probeRunPreviousNextProbeAtMs'] = previous_next
decoded['halfOpenLeaseId'] = nil
decoded['halfOpenLeaseUntilMs'] = nil
decoded['halfOpenPreviousNextProbeAtMs'] = nil
local encoded = cjson.encode(decoded)
redis.call('SET', KEYS[1], encoded, 'PX', ARGV[6])
redis.call('ZADD', KEYS[2], tonumber(decoded['nextProbeAtMs']) or 0, ARGV[1])
redis.call('PEXPIRE', KEYS[2], ARGV[6])
return encoded
`

func (s *ProbeStateStore) acquireGenerationRun(ctx context.Context, runtimeKey string, generation int64, runID string, runUntilMS, ttlMS int64) (*probeState, error) {
	raw, err := s.client.Eval(ctx, probeAcquireGenerationRunScript,
		[]string{s.stateKey(runtimeKey), s.dueKey},
		sanitizeProbeKeyPart(runtimeKey), generation, runID, runUntilMS, time.Now().UnixMilli(), normalizedTTLMS(ttlMS)).Result()
	if err != nil {
		return nil, err
	}
	encoded := redisRawString(raw)
	if encoded == "" {
		return nil, nil
	}
	var state probeState
	if err := json.Unmarshal([]byte(encoded), &state); err != nil {
		return nil, nil
	}
	return &state, nil
}

const probeCommitGenerationRunScript = `
local current = redis.call('GET', KEYS[1])
if not current then return 0 end
local ok, decoded = pcall(cjson.decode, current)
if not ok or type(decoded) ~= 'table' then return 0 end
if tonumber(decoded['generation']) ~= tonumber(ARGV[5]) then return 0 end
if decoded['probeRunId'] ~= ARGV[6] then return 0 end
local incoming_ok, incoming = pcall(cjson.decode, ARGV[1])
if not incoming_ok or type(incoming) ~= 'table' then return 0 end
if tonumber(incoming['generation']) ~= tonumber(ARGV[5]) then return 0 end
local source_fences = {}
local source_fence_seen = {}
local function append_source_fences(fence_set)
  if type(fence_set) == 'table' then
    for _, fence in ipairs(fence_set) do
      if type(fence) == 'string' and fence ~= '' and not source_fence_seen[fence] then
        source_fence_seen[fence] = true
        table.insert(source_fences, fence)
        if #source_fences >= 64 then return end
      end
    end
  end
end
append_source_fences(decoded['sourceFences'])
append_source_fences(incoming['sourceFences'])
if #source_fences > 0 then incoming['sourceFences'] = source_fences end
incoming['probeRunId'] = nil
incoming['probeRunUntilMs'] = nil
incoming['probeRunPreviousNextProbeAtMs'] = nil
local encoded = cjson.encode(incoming)
redis.call('SET', KEYS[1], encoded, 'PX', ARGV[2])
redis.call('ZADD', KEYS[2], tonumber(incoming['nextProbeAtMs']) or tonumber(ARGV[3]) or 0, ARGV[4])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return 1
`

func (s *ProbeStateStore) commitGenerationRun(ctx context.Context, state probeState, runID string, ttlMS int64) (bool, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return false, err
	}
	result, err := s.client.Eval(ctx, probeCommitGenerationRunScript,
		[]string{s.stateKey(state.RuntimeKey), s.dueKey},
		string(encoded), normalizedTTLMS(ttlMS), maxInt64(0, state.NextProbeAtMs),
		sanitizeProbeKeyPart(state.RuntimeKey), state.Generation, runID).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func normalizedTTLMS(ttlMS int64) int64 {
	if ttlMS < 1 {
		return 1
	}
	return ttlMS
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func redisRawString(raw any) string {
	switch typed := raw.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

// AvailabilityProbeRuntimeKey 对齐 availabilityProbeRuntimeKey。
func AvailabilityProbeRuntimeKey(accountRuntimeScope, probeKind string, configRevision int64) string {
	if configRevision < 1 {
		configRevision = 1
	}
	return fmt.Sprintf("availability:%s:%s:r%d", strings.TrimSpace(accountRuntimeScope), probeKind, configRevision)
}

// encodeSourceFence mirrors availability-probe-coordinator 的编码
// （JSON [stateKey, accountId, sourceGeneration, sourceFenceId]）。
func encodeSourceFence(fence ProbeSourceFence) string {
	encoded, _ := json.Marshal([]any{fence.StateKey, fence.AccountID, fence.SourceGeneration, fence.SourceFenceID})
	return string(encoded)
}

var probeFenceUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// NormalizeSourceFence 校验 worker IPC 传入的 fence 形状（对齐
// background-ipc.ts normalizeCodexSourceProbeFence 的有界校验）。
func NormalizeSourceFence(fence ProbeSourceFence) (ProbeSourceFence, error) {
	normalized := ProbeSourceFence{
		StateKey:         strings.TrimSpace(fence.StateKey),
		AccountID:        strings.TrimSpace(fence.AccountID),
		SourceFenceID:    strings.ToLower(strings.TrimSpace(fence.SourceFenceID)),
		SourceGeneration: fence.SourceGeneration,
	}
	if normalized.StateKey == "" || normalized.AccountID == "" {
		return ProbeSourceFence{}, errors.New("codex 源探针 fence 缺少 stateKey/accountId")
	}
	if normalized.SourceGeneration < 0 {
		return ProbeSourceFence{}, errors.New("codex 源探针 fence sourceGeneration 无效")
	}
	if !probeFenceUUIDPattern.MatchString(normalized.SourceFenceID) {
		return ProbeSourceFence{}, errors.New("codex 源探针 fence sourceFenceId 必须是 UUID")
	}
	return normalized, nil
}

// ValidProbeOutcome 校验结算 outcome 取值。
func ValidProbeOutcome(outcome string) bool {
	switch outcome {
	case ProbeOutcomeSuccess, ProbeOutcomeHealthFailure, ProbeOutcomeUnknown,
		ProbeOutcomeProbeTaskFailure, ProbeOutcomeCanceled, ProbeOutcomeStale:
		return true
	}
	return false
}

// SettleDispatchedBySourceFence 对齐 settleDispatchedAvailabilityProbeBySourceFence：
// fence CAS 防止旧结果结算新一轮 generation；成功取得 run 后以该 outcome 提交。
func (s *ProbeStateStore) SettleDispatchedBySourceFence(ctx context.Context, runtimeKey string, generation int64, fence ProbeSourceFence, outcome string, nowMS *int64) (bool, error) {
	if !ValidProbeOutcome(outcome) {
		return false, errors.New("账户可用性探针结算 outcome 无效")
	}
	current, err := s.get(ctx, runtimeKey)
	if err != nil {
		return false, err
	}
	fenceEncoded := encodeSourceFence(fence)
	if current == nil || current.Generation != generation || current.Outcome != nil ||
		current.DispatchPending == nil || !*current.DispatchPending ||
		!containsFence(current.SourceFences, fenceEncoded) {
		return false, nil
	}
	now := time.Now().UnixMilli()
	if nowMS != nil {
		now = *nowMS
	}
	ownerToken := newRandomUUID()
	taken, err := s.acquireGenerationRun(ctx, runtimeKey, generation, ownerToken, now+ProbeDefaultLeaseMS, ProbeDefaultRetentionMS)
	if err != nil {
		return false, err
	}
	if taken == nil || taken.ProbeRunID == nil || *taken.ProbeRunID != ownerToken {
		return false, nil
	}
	// settleAvailabilityProbe：outcome 提交是 fencing 点。
	next := *current
	nextProbeAt := now
	next.NextProbeAtMs = nextProbeAt
	next.ProbeRunID = nil
	next.ProbeRunUntilMs = nil
	next.DispatchPending = nil
	next.DispatchPendingUntilMs = nil
	outcomeValue := outcome
	next.Outcome = &outcomeValue
	next.CompletedAtMs = &now
	return s.commitGenerationRun(ctx, next, ownerToken, ProbeDefaultRetentionMS)
}

func containsFence(fences *[]string, target string) bool {
	if fences == nil {
		return false
	}
	for _, fence := range *fences {
		if fence == target {
			return true
		}
	}
	return false
}
