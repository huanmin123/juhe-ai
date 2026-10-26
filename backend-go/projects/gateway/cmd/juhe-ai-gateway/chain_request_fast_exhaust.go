package main

// 请求级提前耗尽（L1）的请求内计数状态（请求级提前耗尽与负面缓存设计 §4）。
//
// 判定对象是单次客户端调用的 dispatch 循环：计数器挂在请求 ctx 上（链入口
// 安装、请求结束随 ctx 销毁），纯循环内内存计数——零缓存、零 TTL、零 hash。
// 计数输入只有"完整 HTTP 非 2xx 的候选拒绝"这一结构事实（失败派发器
// gateway 流量 skip_account 分支），不解析状态码 / 错误码语义；传输类失败、
// 语义提交后失败、配置截止取消、媒体与诊断透传均不经过计数点（结构性排除
// 由分支形状保证）。计数桶键 = attempt 的上游派发模型 upstream_model（与
// usage 写库同源解析），直连与映射到不同上游模型的候选天然分桶、互不烧断。
//
// 本机制不写任何账户 / Key / 供应商状态，不改变切号准入；达到合取阈值后由
// 失败派发器返回 UpstreamAttemptError，引擎原样上抛、链面走既有统一耗尽出口
// （settleDispatchError → renderDispatchExhausted），响应契约不变。

import (
	"context"
	"sync"
)

// L1 合取阈值（设计 §7 初值）：同一计数桶内 ≥3 只不同账户且合计 ≥5 次完整
// 非 2xx 候选拒绝。settings 常量（代码内固定，不做运行时开关）。
const (
	requestFastExhaustMinDistinctAccounts = 3
	requestFastExhaustMinTotalAttempts    = 5
)

// requestFastExhaustContextKey 是 L1 请求内状态的 ctx 键（包私有，外部无法
// 伪造）。
type requestFastExhaustContextKey struct{}

// requestFastExhaustBucket 是单个上游模型桶的计数状态。
type requestFastExhaustBucket struct {
	accounts  map[string]struct{}
	attempts  int
	triggered bool
}

// requestFastExhaustState 是一次客户端调用的请求内提前耗尽计数器：按上游
// 模型分桶计数，并按首次失败顺序累积全部被完整非 2xx 拒绝的账户（触发时
// 携带进既有耗尽出口的 FailedAccountIDs，供链面耗尽集与诊断消费）。同时承
// 载 L2 负面缓存的请求内记忆化（请求级提前耗尽与负面缓存设计 §5 懒计算）：
// 第一次候选拒绝后才解析一次"来源键 + 请求指纹"缓存键，本请求内续用。
type requestFastExhaustState struct {
	mu               sync.Mutex
	buckets          map[string]*requestFastExhaustBucket
	failedAccountIDs []string
	failedAccountSet map[string]struct{}

	negativeCacheDone     bool
	negativeCacheEligible bool
	negativeCacheKeyMemo  string
}

// requestFastExhaustBucketSnapshot 是触发桶的只读事实快照（审计标记消费）。
type requestFastExhaustBucketSnapshot struct {
	UpstreamModel        string
	DistinctAccountCount int
	AttemptCount         int
	FailedAccountIDs     []string
}

// withRequestFastExhaustState 把请求内计数器安装到请求 ctx（链入口一次）。
func withRequestFastExhaustState(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, requestFastExhaustContextKey{}, &requestFastExhaustState{
		buckets:          map[string]*requestFastExhaustBucket{},
		failedAccountSet: map[string]struct{}{},
	})
}

// requestFastExhaustStateFromContext 读取请求内计数器；未安装（非派发链调用
// 方）返回 nil，调用方按无计数处理。
func requestFastExhaustStateFromContext(ctx context.Context) *requestFastExhaustState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(requestFastExhaustContextKey{}).(*requestFastExhaustState)
	return state
}

// observe 记一次完整非 2xx 候选拒绝：账户只计一次（兄弟 Key 轮换 / 同账户
// 瞬态重试按 attempt 合计次数、不虚增账户数），桶内合取阈值达到后保持触发
// 态（后续同桶拒绝立即再触发，服务链面窗口展开后的快速终止）。返回桶快照
// 与是否触发。
func (s *requestFastExhaustState) observe(upstreamModel, accountID string) (requestFastExhaustBucketSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.failedAccountSet[accountID]; !seen {
		s.failedAccountSet[accountID] = struct{}{}
		s.failedAccountIDs = append(s.failedAccountIDs, accountID)
	}
	bucket := s.buckets[upstreamModel]
	if bucket == nil {
		bucket = &requestFastExhaustBucket{accounts: map[string]struct{}{}}
		s.buckets[upstreamModel] = bucket
	}
	bucket.accounts[accountID] = struct{}{}
	bucket.attempts++
	if !bucket.triggered &&
		len(bucket.accounts) >= requestFastExhaustMinDistinctAccounts &&
		bucket.attempts >= requestFastExhaustMinTotalAttempts {
		bucket.triggered = true
	}
	failedIDs := append([]string(nil), s.failedAccountIDs...)
	return requestFastExhaustBucketSnapshot{
		UpstreamModel:        upstreamModel,
		DistinctAccountCount: len(bucket.accounts),
		AttemptCount:         bucket.attempts,
		FailedAccountIDs:     failedIDs,
	}, bucket.triggered
}

// negativeCacheKey 返回 L2 缓存键的请求内记忆化结果（键、是否合格参与 L2）。
// 首次调用时执行 resolve 计算并记忆，后续调用直接复用（成功请求零解析、
// 零 hash；解析只发生在失败路径）。resolve 在持锁内执行，不得回调本状态。
func (s *requestFastExhaustState) negativeCacheKey(resolve func() (string, bool)) (key string, eligible bool, first bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.negativeCacheDone {
		return s.negativeCacheKeyMemo, s.negativeCacheEligible, false
	}
	s.negativeCacheDone = true
	s.negativeCacheKeyMemo, s.negativeCacheEligible = resolve()
	return s.negativeCacheKeyMemo, s.negativeCacheEligible, true
}

// failedAccountIDsSnapshot 返回请求内按首次失败顺序累积的失败账户集快照。
func (s *requestFastExhaustState) failedAccountIDsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.failedAccountIDs...)
}
