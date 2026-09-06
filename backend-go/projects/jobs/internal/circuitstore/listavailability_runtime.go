package circuitstore

// 投影 LoadItems 的 Redis 运行态读面：对照归档 Node
//   - shared/runtime-probe-state-store.ts RedisRuntimeProbeStateStore
//     （state `<ns>:juhe-ai:probe:<name>:state:<sanitizedKey>` JSON GET/MGET、
//     due ZSET ZMSCORE scheduledRuntimeKeys）
//   - shared/runtime-state-store.ts（`<ns>:juhe-ai:state:<name>:<key>` JSON）
//   - modules/gateway/runtime/account-side-effects.service.ts
//     loadDistributedGatewayAccountRuntimeAvailability /
//     visibleRuntimeProbePresentation / configuredPolicyAvoidanceAvailability /
//     runtimeProbeStateRunning
//   - domain/account-runtime-availability-public.ts publicAccountRuntimeAvailability
//
// jobs 侧为只读消费（acquire/merge/commit 等生产侧脚本仍归网关单实现）；
// 键空间与 Node/Go 网关互通（gateway-account-recovery /
// gateway-configured-account-policy-avoidance）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// recoveryProbeStoreName 与 Node createRuntimeProbeStateStore 名称一致。
const recoveryProbeStoreName = "gateway-account-recovery"

// policyAvoidanceStoreName 与 Node createRuntimeStateStore 名称一致。
const policyAvoidanceStoreName = "gateway-configured-account-policy-avoidance"

// distributedRecoveryProbeState 是 DistributedRecoveryProbeState 的读取投影
// （camelCase 与 Node 存储一致）。
type distributedRecoveryProbeState struct {
	RuntimeKey             string         `json:"runtimeKey"`
	Phase                  string         `json:"phase"`
	Generation             int64          `json:"generation"`
	StartedAtMs            int64          `json:"startedAtMs"`
	LastObservedAtMs       int64          `json:"lastObservedAtMs"`
	NextProbeAtMs          int64          `json:"nextProbeAtMs"`
	AttemptCount           int            `json:"attemptCount"`
	FailureCount           int            `json:"failureCount"`
	Reason                 string         `json:"reason"`
	DistinctClientIpCount  int            `json:"distinctClientIpCount"`
	DistinctApiKeyCount    int            `json:"distinctApiKeyCount"`
	ProbeRunID             *string        `json:"probeRunId,omitempty"`
	ProbeRunUntilMs        *int64         `json:"probeRunUntilMs,omitempty"`
	HalfOpenLeaseID        *string        `json:"halfOpenLeaseId,omitempty"`
	HalfOpenLeaseUntilMs   *int64         `json:"halfOpenLeaseUntilMs,omitempty"`
	ProbePresentation      map[string]any `json:"probePresentation,omitempty"`
}

// configuredPolicyAvoidanceState 是 ConfiguredPolicyAvoidanceState 的读取投影。
type configuredPolicyAvoidanceState struct {
	RuntimeKey  string `json:"runtimeKey"`
	AccountID   string `json:"accountId"`
	Reason      string `json:"reason"`
	StartedAtMs int64  `json:"startedAtMs"`
	UntilMs     int64  `json:"untilMs"`
}

// RuntimeStateReader 是投影 LoadItems 的运行态可用性只读源。
type RuntimeStateReader struct {
	client             redis.Cmdable
	statePrefix        string
	dueKey             string
	avoidanceKeyPrefix string
	now                func() time.Time
}

// NewRuntimeStateReader 建立 Redis 连接（Client 注入供测试）。
func NewRuntimeStateReader(url, namespace string, client redis.Cmdable) (*RuntimeStateReader, error) {
	var cmdable redis.Cmdable = client
	if cmdable == nil {
		if strings.TrimSpace(url) == "" {
			return nil, errors.New("账户列表投影运行态读取缺少 Redis URL")
		}
		options, err := redis.ParseURL(url)
		if err != nil {
			return nil, fmt.Errorf("解析账户列表投影运行态 Redis URL: %w", err)
		}
		cmdable = redis.NewClient(options)
	}
	safeRecovery := sanitizeRedisName(recoveryProbeStoreName)
	safeAvoidance := sanitizeRedisName(policyAvoidanceStoreName)
	return &RuntimeStateReader{
		client:             cmdable,
		statePrefix:        redisNamespacedKey("juhe-ai:probe:"+safeRecovery+":state:", namespace),
		dueKey:             redisNamespacedKey("juhe-ai:probe:"+safeRecovery+":due", namespace),
		avoidanceKeyPrefix: redisNamespacedKey("juhe-ai:state:"+safeAvoidance+":", namespace),
		now:                func() time.Time { return time.Now() },
	}, nil
}

// Close 释放自建连接。
func (r *RuntimeStateReader) Close() error {
	if client, ok := r.client.(*redis.Client); ok && client != nil {
		return client.Close()
	}
	return nil
}

// LoadRuntimeAvailability 对齐 loadDistributedGatewayAccountRuntimeAvailability，
// 输出为 publicAccountRuntimeAvailability 投影后的 payload 形状。
func (r *RuntimeStateReader) LoadRuntimeAvailability(ctx context.Context, runtimeKeys []string) (map[string]AccountRuntimeAvailability, error) {
	keys := uniqueRuntimeKeys(runtimeKeys)
	result := map[string]AccountRuntimeAvailability{}
	if len(keys) == 0 {
		return result, nil
	}
	states, err := r.getStates(ctx, keys)
	if err != nil {
		return nil, err
	}
	scheduled, err := r.scheduledRuntimeKeys(ctx, keys)
	if err != nil {
		return nil, err
	}
	avoidances, err := r.getPolicyAvoidances(ctx, keys)
	if err != nil {
		return nil, err
	}
	nowMS := r.now().UnixMilli()
	for _, key := range keys {
		state, found := states[key]
		if !found {
			continue
		}
		if state.Phase == "recovery_wait" && state.AttemptCount == 0 {
			continue
		}
		status := "degraded"
		if state.Phase == "precheck_pending" {
			status = "precheck_pending"
		}
		availability := AccountRuntimeAvailability{
			Status: status,
			Reason: state.Reason,
			Since:  millisToISO(state.StartedAtMs),
		}
		presentation := visibleRuntimeProbePresentation(state.ProbePresentation, visibleProbeInput{
			taskScheduled: scheduled[key],
			running:       runtimeProbeStateRunning(state, nowMS),
		}, nowMS)
		if presentation != nil {
			availability.ProbePresentation = presentation
		}
		result[key] = availability
	}
	for index, key := range keys {
		state := avoidances[index]
		if state == nil {
			continue
		}
		recoveryAt := millisToISO(state.UntilMs)
		result[key] = AccountRuntimeAvailability{
			Status: "local_suppressed",
			Reason: state.Reason,
			Since:  millisToISO(state.StartedAtMs),
			ProbePresentation: map[string]any{
				"schedule":       map[string]any{"state": "none"},
				"recoveryAt":     recoveryAt,
				"recoveryAtKind": "policy_ttl_expiry",
			},
		}
	}
	return result, nil
}

type visibleProbeInput struct {
	taskScheduled bool
	running       bool
}

// visibleRuntimeProbePresentation 对齐 visibleRuntimeProbePresentation（输出
// 保持 Node probePresentation JSON 形状；lastObservation 原样透传）。
func visibleRuntimeProbePresentation(presentation map[string]any, input visibleProbeInput, nowMS int64) map[string]any {
	var lastObservation any
	if presentation != nil {
		lastObservation = presentation["lastObservation"]
	}
	payload := map[string]any{}
	if lastObservation != nil {
		payload["lastObservation"] = lastObservation
	}
	if input.running {
		payload["schedule"] = map[string]any{"state": "running"}
		return payload
	}
	var nextAttemptAt string
	if presentation != nil {
		if schedule, ok := presentation["schedule"].(map[string]any); ok {
			if text, ok := schedule["nextAttemptAt"].(string); ok {
				nextAttemptAt = text
			}
		}
	}
	if !input.taskScheduled || nextAttemptAt == "" {
		payload["schedule"] = map[string]any{"state": "none"}
		return payload
	}
	timestamp, ok := rfc3339Millis(nextAttemptAt)
	if !ok {
		return payload
	}
	state := "scheduled"
	if timestamp <= nowMS {
		state = "due_waiting"
	}
	payload["schedule"] = map[string]any{"state": state, "nextAttemptAt": nextAttemptAt}
	return payload
}

// runtimeProbeStateRunning 对齐 runtimeProbeStateRunning。
func runtimeProbeStateRunning(state distributedRecoveryProbeState, nowMS int64) bool {
	if state.ProbeRunID != nil && state.ProbeRunUntilMs != nil && *state.ProbeRunUntilMs > nowMS {
		return true
	}
	if state.HalfOpenLeaseID != nil && state.HalfOpenLeaseUntilMs != nil && *state.HalfOpenLeaseUntilMs > nowMS {
		return true
	}
	return false
}

func (r *RuntimeStateReader) getStates(ctx context.Context, keys []string) (map[string]distributedRecoveryProbeState, error) {
	stateKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		stateKeys = append(stateKeys, r.statePrefix+sanitizeProbeKeyPart(key))
	}
	rawValues, err := r.client.MGet(ctx, stateKeys...).Result()
	if err != nil {
		return nil, err
	}
	result := map[string]distributedRecoveryProbeState{}
	for index, raw := range rawValues {
		text, ok := raw.(string)
		if !ok || text == "" {
			continue
		}
		var state distributedRecoveryProbeState
		if err := json.Unmarshal([]byte(text), &state); err != nil {
			// Node：损坏数据删除并抛错（fail closed）。
			_ = r.client.Del(ctx, stateKeys[index]).Err()
			return nil, fmt.Errorf("Redis probe state 内容损坏：%s", keys[index])
		}
		result[keys[index]] = state
	}
	return result, nil
}

func (r *RuntimeStateReader) scheduledRuntimeKeys(ctx context.Context, keys []string) (map[string]bool, error) {
	// go-redis ZMScore 的 []float64 无法区分缺失成员与 score=0（RESP2 归零），
	// 改用 pipeline ZScore 逐键判定：redis.Nil 即未调度（Node ZMSCORE null）。
	pipeline := r.client.Pipeline()
	cmds := make([]*redis.FloatCmd, len(keys))
	for index, key := range keys {
		cmds[index] = pipeline.ZScore(ctx, r.dueKey, key)
	}
	if _, err := pipeline.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	scheduled := map[string]bool{}
	for index, cmd := range cmds {
		if err := cmd.Err(); err == nil {
			scheduled[keys[index]] = true
		} else if !errors.Is(err, redis.Nil) {
			return nil, err
		}
	}
	return scheduled, nil
}

func (r *RuntimeStateReader) getPolicyAvoidances(ctx context.Context, keys []string) ([]*configuredPolicyAvoidanceState, error) {
	avoidanceKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		avoidanceKeys = append(avoidanceKeys, r.avoidanceKeyPrefix+sanitizeProbeKeyPart(key))
	}
	rawValues, err := r.client.MGet(ctx, avoidanceKeys...).Result()
	if err != nil {
		return nil, err
	}
	output := make([]*configuredPolicyAvoidanceState, len(keys))
	for index, raw := range rawValues {
		text, ok := raw.(string)
		if !ok || text == "" {
			continue
		}
		var state configuredPolicyAvoidanceState
		if err := json.Unmarshal([]byte(text), &state); err != nil {
			return nil, fmt.Errorf("Redis 运行态 policy avoidance 内容损坏：%s", keys[index])
		}
		if state.UntilMs <= r.now().UnixMilli() {
			continue
		}
		output[index] = &state
	}
	return output, nil
}

func uniqueRuntimeKeys(keys []string) []string {
	seen := map[string]struct{}{}
	output := make([]string, 0, len(keys))
	for _, key := range keys {
		normalized := strings.TrimSpace(key)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		output = append(output, normalized)
		if len(output) >= 100 {
			break
		}
	}
	return output
}

// OverlayConcurrencySource 把 OverlayRedisStore（account-concurrency-v2 同键
// 快照 Lua）适配为投影 LoadItems 的并发读。
type OverlayConcurrencySource struct {
	overlay *OverlayRedisStore
}

// NewOverlayConcurrencySource 组装并发读源。
func NewOverlayConcurrencySource(overlay *OverlayRedisStore) *OverlayConcurrencySource {
	return &OverlayConcurrencySource{overlay: overlay}
}

// LoadConcurrency 返回账户当前并发（current_concurrency 槽位同键同值）。
func (s *OverlayConcurrencySource) LoadConcurrency(ctx context.Context, accountIDs []string) (map[string]int, error) {
	snapshots, err := s.overlay.LoadSnapshots(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	output := make(map[string]int, len(snapshots))
	for _, snapshot := range snapshots {
		output[snapshot.AccountID] = snapshot.CurrentConcurrency
	}
	return output, nil
}
