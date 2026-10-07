package accountbalance

// 多 Key 余额执行核心：逐 Key 查询 + 安全合计。
//
// 语义权威是归档 Node 实现 account-balance-query.service.ts 的
// queryMultiKeyAccountBalance（共享 deadline + 有界并发 worker 池逐 Key 查询）
// 与 aggregateMultiKeyBalance（合计规则）；产品契约见
// docs/functions/AI账户上游余额查询设计.md §3.4（多 Key 账户不再自动关闭余额
// 查询，余额按 Key 查询、口径明确时合计，列表/明细展示合并余额与逐 Key 明细）。
//
// 安全规则：只有全部 Key 返回明确 scope=key 且币种/单位/basis 一致时才由
// 服务端用 decimal 精确求和；scope=account 且各 Key 值相同才原样展示共享值；
// 口径不一致、部分失败和未知 scope 都是余额诊断，不改变账户调度事实。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// multiKeyBalanceMaxConcurrent 对齐 Node multiKeyBalanceMaxConcurrent=4：
// 单账户内的逐 Key 上游并发上限（与 Runner.IOConcurrency 的跨账户并发正交）。
const multiKeyBalanceMaxConcurrent = 4

// multiKeyDeadlineMessage 对齐 Node 的共享 deadline 超时文案：deadline 已到而
// 未起跑的 Key 直接记失败，不再发出上游请求。
const multiKeyDeadlineMessage = "上游余额查询超时"

// ExecuteAccountBalanceQuery is the multi-Key aware execution entry: it runs
// one bounded direct balance query for an Input whose credential envelope may
// hold a single API Key ({"api_key": ...}) or a Key pool ({"api_keys": [...]},
// EffectiveAPIKeys 语义). Single-Key inputs delegate to ExecuteBalanceQuery
// unchanged; multi-Key inputs query each Key through the same single-Key path
// (adapter dispatch / proxy / response caps) and merge the results into one
// Snapshot. A returned error still means the input/transport setup failed
// locally; upstream and per-Key diagnostics are represented in QueryResult.
// ExecuteAccountBalanceQuery 是三个写入方（J2 周期/首探、SQLite 自动探测、
// gateway 手动刷新）共用的执行核。BUG-0286：成功返回（含上游失败形态的
// QueryResult）统一在此打点余额输入身份摘要——快照负载携带 inputDigest，
// gateway 读端以账户当前列值现算同一摘要做显示匹配；本地错误（解封失败等）
// 不产生快照，无需打点。
func ExecuteAccountBalanceQuery(ctx context.Context, input Input, options QueryOptions) (QueryResult, error) {
	result, err := executeAccountBalanceQuery(ctx, input, options)
	if err == nil {
		result.Snapshot.InputDigest = BalanceInputDigest(input.Provider, input.CredentialFingerprint, input.ConfigJSON)
	}
	return result, err
}

func executeAccountBalanceQuery(ctx context.Context, input Input, options QueryOptions) (QueryResult, error) {
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	if err := input.Validate(now().UTC()); err != nil {
		return QueryResult{}, err
	}
	if strings.TrimSpace(options.Secret) == "" {
		return QueryResult{}, errors.New("account-balance direct HTTP 缺少 credential secret")
	}
	keys, kind, err := effectiveCredentialKeys(input, options.Secret)
	if err != nil {
		return QueryResult{}, err
	}
	// 单 Key（含零 Key）原样走既有路径，行为零变化：包括单元素 api_keys 数组
	// 继续被单 Key 解封拒绝的既有形态，也不在本入口改变。
	if len(keys) <= 1 {
		return ExecuteBalanceQuery(ctx, input, options)
	}
	return executeMultiKeyBalanceQuery(ctx, input, options, keys, kind)
}

// effectiveCredentialKeys unseals the input credential envelope (same
// selection order and error wrapping as ExecuteBalanceQuery) and returns the
// effective Key pool plus the envelope Kind the per-Key sub-envelopes reuse.
func effectiveCredentialKeys(input Input, secret string) ([]string, string, error) {
	credential := input.APIKey
	if strings.TrimSpace(credential.Ciphertext) == "" {
		credential = input.Credential
	}
	var payload map[string]any
	if err := openCredential(secret, credential, "api_key", &payload); err != nil {
		return nil, "", fmt.Errorf("account-balance API Key envelope 无法安全解封: %w", err)
	}
	return EffectiveAPIKeys(payload), credential.Kind, nil
}

// executeMultiKeyBalanceQuery 对齐 Node queryMultiKeyAccountBalance：共享总
// deadline + 有界并发 worker 池逐 Key 查询，再按 aggregateMultiKeyBalance 合并。
//
// deadline 语义（保守取交集）：现有单 Key 路径把 ProbeTimeout 用在每次直接
// 查询的整体上（ExecuteBalanceQuery 内部 context.WithTimeout），多 Key 的总
// deadline 同样取 ProbeTimeout（同 cap：<=0 或超过 defaultBalanceTimeout 时
// 归一为 15s），一次性挂在父 ctx 上；每个单 Key 子查询沿用其内部每请求超时
// （再派生子 ctx），context 派生自动取更保守者（剩余总 deadline 与单请求
// 超时的较小值），因此任何单个 Key 都不可能把整体拖过 ProbeTimeout。
func executeMultiKeyBalanceQuery(ctx context.Context, input Input, options QueryOptions, keys []string, kind string) (QueryResult, error) {
	timeout := options.Timeout
	if timeout <= 0 || timeout > defaultBalanceTimeout {
		timeout = defaultBalanceTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	keyBalances := make([]KeyBalance, len(keys))
	nextIndex := make(chan int, len(keys))
	for index := range keys {
		nextIndex <- index
	}
	close(nextIndex)
	workers := len(keys)
	if workers > multiKeyBalanceMaxConcurrent {
		workers = multiKeyBalanceMaxConcurrent
	}
	var workersWG sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		workersWG.Add(1)
		go func() {
			defer workersWG.Done()
			for index := range nextIndex {
				keyBalances[index] = queryOneKeyBalance(ctx, input, options, kind, keys[index])
			}
		}()
	}
	workersWG.Wait()
	result := QueryResult{Snapshot: aggregateMultiKeyBalance(keyBalances, len(keys))}
	// 聚合诊断文案（部分失败 / 口径不一致 / 全失败）同步到 QueryResult，
	// 使其进入 outcome.ErrorMessage 与运行日志，避免下游出现空文案。
	// Temporary 保持 false：这些是确定性余额诊断，落确定性快照分支。
	result.ErrorMessage = result.Snapshot.ErrorMessage
	// 对齐 Node：只有全部 Key 选中同一适配器时猜测才安全，因此不保留任何
	// 探测出的适配器，仅回带配置里的内置偏好（可能为空）。
	result.Adapter = input.Config.PreferredBuiltinAdapter
	return result, nil
}

// queryOneKeyBalance runs one per-Key single-Key sub-query and projects the
// result into a KeyBalance (Node keySnapshotForFailure / keySnapshotFromResult).
// All upstream and per-Key diagnostics land in the returned entry; only a
// canceled/deadline parent context or a local envelope construction failure is
// reported here as a failed entry too (never as an error that aborts the pool).
func queryOneKeyBalance(ctx context.Context, input Input, options QueryOptions, kind, apiKey string) KeyBalance {
	fingerprint := BalanceAPIKeyFingerprint(options.Secret, apiKey)
	masked := MaskBalanceAPIKey(apiKey)
	// 共享 deadline 已到（或外部取消）：该 Key 不再发起上游请求，直接记失败
	// （对齐 Node 的 deadline 前置检查与超时文案）。
	if err := ctx.Err(); err != nil {
		message := multiKeyDeadlineMessage
		if !errors.Is(err, context.DeadlineExceeded) {
			message = err.Error()
		}
		return keyBalanceForFailure(fingerprint, masked, message, options)
	}
	envelope, err := NewCredentialEnvelope(options.Secret, kind, map[string]string{"api_key": apiKey})
	if err != nil {
		return keyBalanceForFailure(fingerprint, masked, err.Error(), options)
	}
	subInput := input
	subInput.APIKey = envelope
	subInput.Credential = envelope
	result, err := ExecuteBalanceQuery(ctx, subInput, options)
	if err != nil {
		return keyBalanceForFailure(fingerprint, masked, err.Error(), options)
	}
	return keyBalanceFromSnapshot(fingerprint, masked, result, options)
}

// multiKeyScopeFor 按命中的适配器推导余额口径，逐条对齐 Node
// account-balance-adapters.ts 各 parse 函数写入 snapshot.scope 的规则：
// sub2api 的 quota_limited 是独立 Key 配额（scope=key），钱包/订阅是账户级
// 共享额度（scope=account）；newapi 成功响应是独立 Key 配额（key）；
// openai_billing 的账单端点在多数部署里是账户/组织级预算，上游未明确 Key
// 口径前保持 unknown（禁止相加）；litellm/user_balance 是账户级（account）；
// custom 与适配器未命中（unsupported 诊断，Adapter 为空）回退 unknown。
// Go 的单 Key Snapshot 不携带 scope（保持既有 JSON 形状），因此这里在多 Key
// 合并时按子查询结果推导，等价于 Node 的逐响应 scope。
func multiKeyScopeFor(result QueryResult) string {
	if result.Snapshot.Status == StatusUnlimited {
		// Node 的 unlimited 快照不携带 scope（sub2api 订阅 unlimited）。
		return ScopeUnknown
	}
	switch result.Adapter {
	case AdapterSub2API:
		if result.Snapshot.Basis == BasisAPIKeyQuota {
			return ScopeKey
		}
		return ScopeAccount
	case AdapterNewAPI:
		if result.Snapshot.Status == StatusFresh {
			return ScopeKey
		}
		return ScopeUnknown
	case AdapterLiteLLM, AdapterUserBalance:
		return ScopeAccount
	default:
		// openai_billing 与 custom（无独立 Key 口径）以及适配器未命中的
		// unsupported 诊断都禁止按 Key 相加。
		return ScopeUnknown
	}
}

// keyBalanceFromSnapshot 对齐 Node keySnapshotFromResult：保留子快照的
// status/amount/unit/basis/error 与逐 Key 时间戳；scope 由 multiKeyScopeFor
// 推导，缺省记 unknown（禁止合计）。
func keyBalanceFromSnapshot(fingerprint, masked string, result QueryResult, options QueryOptions) KeyBalance {
	snapshot := result.Snapshot
	entry := KeyBalance{
		KeyFingerprint: fingerprint,
		MaskedKey:      masked,
		Status:         snapshot.Status,
		RemainingUSD:   snapshot.RemainingUSD,
		RawUnit:        snapshot.RawUnit,
		Scope:          multiKeyScopeFor(result),
		Basis:          snapshot.Basis,
		ErrorMessage:   snapshot.ErrorMessage,
		LastAttemptAt:  balanceTimestamp(options),
	}
	if snapshot.Status == StatusFresh || snapshot.Status == StatusUnlimited {
		entry.LastSuccessAt = entry.LastAttemptAt
	}
	return entry
}

func keyBalanceForFailure(fingerprint, masked, message string, options QueryOptions) KeyBalance {
	return KeyBalance{
		KeyFingerprint: fingerprint,
		MaskedKey:      masked,
		Status:         StatusFailed,
		ErrorMessage:   message,
		LastAttemptAt:  balanceTimestamp(options),
	}
}

func balanceTimestamp(options QueryOptions) string {
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	return now().UTC().Format(time.RFC3339Nano)
}

// aggregateMultiKeyBalance 对齐 Node aggregateMultiKeyBalance：
//   - 全部成功、scope=key、status=fresh 且币种/单位/basis 一致 → fresh/sum
//     （decimal 精确求和，禁 float）；
//   - 全部成功、scope=account、status=fresh、单位/basis 一致且各值相同 →
//     fresh/shared（取共享值）；
//   - 其余情况：全部成功 → unsupported（口径不一致）；有失败 → failed
//     （部分失败带 x/y 文案；全失败取首个 Key 的代表性错误）。
//
// 任何情况都携带 keyCount/queriedKeyCount/keyBalances。与 Node 的唯一偏差是
// 部分失败文案的分子：任务与设计文档明确 x=失败数（Node 代码用成功数），按
// 当前契约实现。
func aggregateMultiKeyBalance(keyBalances []KeyBalance, keyCount int) Snapshot {
	successful := make([]KeyBalance, 0, len(keyBalances))
	for _, entry := range keyBalances {
		if entry.Status == StatusFresh || entry.Status == StatusUnlimited {
			successful = append(successful, entry)
		}
	}
	queried := len(successful)
	allSuccessful := queried == keyCount && keyCount > 0
	result := Snapshot{KeyCount: keyCount, QueriedKeyCount: queried, KeyBalances: keyBalances}

	// sum：只有全部 Key 明确属于独立 Key 配额且口径一致才安全相加。
	if allSuccessful && multiKeyAllSummable(successful) {
		total, err := sumKeyBalanceAmounts(successful)
		if err == nil {
			result.Status = StatusFresh
			result.RemainingUSD = total
			result.Scope = ScopeKey
			result.Aggregation = AggregationSum
			return result
		}
		// 金额串不是合法 decimal（现实不可达：RemainingUSD 均由 decimal 工具
		// 生成）时按口径不一致处理，绝不回退为 float 相加。
	}
	// shared：账户级共享额度只在全部 Key 值完全一致时原样展示。
	if allSuccessful && multiKeyAllShared(successful) {
		sharedValues := make([]string, 0, len(successful))
		for _, entry := range successful {
			if entry.RemainingUSD != "" {
				sharedValues = append(sharedValues, entry.RemainingUSD)
			}
		}
		if len(sharedValues) == keyCount && multiKeyAllEqual(sharedValues) {
			result.Status = StatusFresh
			result.RemainingUSD = sharedValues[0]
			result.Scope = ScopeAccount
			result.Aggregation = AggregationShared
			return result
		}
	}
	// 兜底：不伪造总额，按"口径不一致 / 部分失败 / 全失败"给出诊断。
	if anyAccountScope(successful) {
		result.Scope = ScopeAccount
		result.Aggregation = AggregationShared
	} else {
		result.Scope = ScopeUnknown
		result.Aggregation = AggregationUnknown
	}
	if allSuccessful {
		result.Status = StatusUnsupported
		result.ErrorMessage = "多 Key 余额口径不一致，无法安全合计"
		return result
	}
	if queried == 0 {
		result.Status = StatusFailed
		for _, entry := range keyBalances {
			if entry.ErrorMessage != "" {
				result.ErrorMessage = entry.ErrorMessage
				break
			}
		}
		if result.ErrorMessage == "" {
			result.ErrorMessage = "多 Key 余额查询失败"
		}
		return result
	}
	failedCount := keyCount - queried
	result.Status = StatusFailed
	result.ErrorMessage = fmt.Sprintf("多 Key 余额查询部分失败（%d/%d）", failedCount, keyCount)
	return result
}

// multiKeyAllSummable 对齐 Node allKeyQuota：全部 fresh、scope=key、金额与
// 单位齐全且 basis/unit 与首个成功 Key 一致。
func multiKeyAllSummable(successful []KeyBalance) bool {
	if len(successful) == 0 {
		return false
	}
	first := successful[0]
	for _, entry := range successful {
		if entry.Scope != ScopeKey || entry.Status != StatusFresh ||
			entry.RemainingUSD == "" || entry.RawUnit == "" ||
			entry.Basis != first.Basis || entry.RawUnit != first.RawUnit {
			return false
		}
	}
	return true
}

// multiKeyAllShared 对齐 Node allShared 的口径前提：全部 fresh、scope=account、
// 单位齐全且 basis/unit 与首个成功 Key 一致（值是否相同由调用方再验）。
func multiKeyAllShared(successful []KeyBalance) bool {
	if len(successful) == 0 {
		return false
	}
	first := successful[0]
	for _, entry := range successful {
		if entry.Scope != ScopeAccount || entry.Status != StatusFresh ||
			entry.RawUnit == "" || entry.Basis != first.Basis || entry.RawUnit != first.RawUnit {
			return false
		}
	}
	return true
}

func multiKeyAllEqual(values []string) bool {
	for _, value := range values {
		if value != values[0] {
			return false
		}
	}
	return true
}

func anyAccountScope(successful []KeyBalance) bool {
	for _, entry := range successful {
		if entry.Scope == ScopeAccount {
			return true
		}
	}
	return false
}

// sumKeyBalanceAmounts 用包内 decimal 工具精确求和（对齐 Node addDecimalStrings
// 的整数系数语义），任一金额非法即失败，绝不退化为浮点相加。
func sumKeyBalanceAmounts(successful []KeyBalance) (string, error) {
	total, err := parseDecimal(successful[0].RemainingUSD, "remainingUsd")
	if err != nil {
		return "", err
	}
	for _, entry := range successful[1:] {
		value, err := parseDecimal(entry.RemainingUSD, "remainingUsd")
		if err != nil {
			return "", err
		}
		total = decimalAdd(total, value)
	}
	return decimalText(total), nil
}

// BalanceAPIKeyFingerprint mirrors gateway accountsbalance.BalanceAPIKeyFingerprint
// (Node accountBalanceApiKeyFingerprint): a stable HMAC-SHA256 identity keyed
// by the same credential secret, so per-Key entries stored by jobs can be
// joined by the gateway without the raw Key ever leaving the backend.
func BalanceAPIKeyFingerprint(secret, value string) string {
	key := strings.TrimSpace(value)
	if key == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

// MaskBalanceAPIKey mirrors gateway accountsbalance.MaskBalanceAPIKey (Node
// maskAccountBalanceApiKey): the UI-safe representation used by balance
// details and never by upstream requests.
func MaskBalanceAPIKey(value string) string {
	key := strings.TrimSpace(value)
	runes := []rune(key)
	if len(runes) <= 8 {
		head := len(runes)
		if head > 2 {
			head = 2
		}
		tailStart := len(runes) - 2
		if tailStart < 0 {
			tailStart = 0
		}
		return string(runes[:head]) + "…" + string(runes[tailStart:])
	}
	return string(runes[:4]) + "…" + string(runes[len(runes)-4:])
}
