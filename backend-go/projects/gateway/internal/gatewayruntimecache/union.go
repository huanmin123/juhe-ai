package gatewayruntimecache

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// ---------------------------------------------------------------------------
// 分组模型并集缓存（权威契约：docs/functions/网关模型列表账户并集设计.md §6.3）
//
// 版本键 = 共享桶名 "gateway:group-model-union:<processStartToken>:<generation>"，
// 桶内键为业务键。启动 token 使重启后的旧 Redis 键不可达；generation 随
// ClearGatewayRuntimeCacheLocal 无条件递增，使失效后的旧键不可达（Redis 删除
// 只是尽力空间回收，失败不影响正确性）。本地发布的代号复核与写入、失效的代号
// 推进与清空全部在 unionPublicationMu 单临界区串行——目录缓存“检查与写入分两
// 次加锁”的窗口不得复现。锁序固定 unionPublicationMu 先、其他锁后，任何锁内
// 不做 Redis I/O；并发保证范围为单 gateway 进程。
// ---------------------------------------------------------------------------

// GroupModelUnionBaseTTL 是分组模型并集的基础 TTL。条目有效期 validUntil 由装
// 载器按 min(装载时刻+基础TTL, 最近未来到期点) 计算；缓存层 TTL 恒取
// min(基础 TTL, 剩余有效期)，回填不重新起算。
const GroupModelUnionBaseTTL = time.Hour

// groupModelUnionReadAttemptLimit 是单次读取在“捕获的代号被并发失效推进”时的
// 有界重试上限；耗尽说明失效持续覆盖整个读取窗口，返回确定性错误而不是无限
// 等待。
const groupModelUnionReadAttemptLimit = 4

// unionSharedCacheWriteTimeout mirrors the catalog shared write budget.
const unionSharedCacheWriteTimeout = 3 * time.Second

// ErrGroupModelUnionLoaderUnavailable 是构造未注入 Union loader 时的读取错误：
// 明确失败，不 panic、不静默空列表。
var ErrGroupModelUnionLoaderUnavailable = errors.New("gateway_group_model_union_loader_unavailable")

var (
	// errGroupModelUnionReadRetryExhausted：读取窗口内代号被连续推进、或装载
	// 结果始终过有效期，有界重试耗尽时上抛的确定性错误。
	errGroupModelUnionReadRetryExhausted = errors.New("gateway_group_model_union_read_retry_exhausted")
	// errGroupModelUnionLoadAborted：后台装载 goroutine 因 panic 提前退出时，
	// 等待者以该错误收尾，不得悬挂。
	errGroupModelUnionLoadAborted = errors.New("gateway_group_model_union_load_aborted")
)

// GroupModelUnionEntry 是分组模型并集的缓存载荷：模型名集合 + 有效期界
// （设计 §6.3）。
type GroupModelUnionEntry struct {
	Models     []string
	ValidUntil time.Time
}

// Clone 深拷贝 Models，缓存读写两侧不共享底层数组。
func (e GroupModelUnionEntry) Clone() GroupModelUnionEntry {
	cloned := e
	if e.Models != nil {
		cloned.Models = append([]string(nil), e.Models...)
	}
	return cloned
}

// GroupModelUnionListOptions 是并集装载输入：调用方（API Key 属主，用于授权
// 门与缓存键隔离）+ 分组 ID 集（方法入口完成去重排序）。
type GroupModelUnionListOptions struct {
	CallerSystemAccountID string
	GroupIDs              []string
}

// GroupModelUnionLoader 是并集数据面装载端口（聚合 SQL + 授权门 + 映射有效性
// 过滤，设计 §6.2）；实现负责按 §6.3 计算 validUntil。
type GroupModelUnionLoader interface {
	ListGroupModelUnion(ctx context.Context, input GroupModelUnionListOptions) (GroupModelUnionEntry, error)
}

// sharedGroupModelUnionEntry 是共享缓存的 JSON 载荷。
type sharedGroupModelUnionEntry struct {
	Models     []string  `json:"models"`
	ValidUntil time.Time `json:"validUntil"`
}

// unionLoad 是并集 singleflight 条目：注册时捕获代号；结果在专用发布锁内落账
// 后关闭 done。等待者各自 select ctx，单个等待者取消不取消共享装载。
type unionLoad struct {
	generation uint64
	done       chan struct{}
	entry      GroupModelUnionEntry
	err        error
	// finished 标记 publishUnionLoad 已落账；panic 臂据此补写哨兵错误。
	finished bool
}

// unionClosedDone 是已完成装载共享的 done 通道（registerUnionLoad 双检命中的
// 合成结果），只读关闭态。
var unionClosedDone = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// newProcessStartToken 生成进程启动 token（16 字节随机 hex）。go1.24+ 的
// crypto/rand.Read 恒填满且不返回错误。
func newProcessStartToken() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

// normalizeGroupModelUnionGroupIDs 对分组 ID 去重并排序（设计 §6.3 业务键输入，
// 方法入口完成）。
func normalizeGroupModelUnionGroupIDs(groupIDs []string) []string {
	seen := make(map[string]bool, len(groupIDs))
	out := make([]string, 0, len(groupIDs))
	for _, id := range groupIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sortStrings(out)
	return out
}

// groupModelUnionCacheKey 组合并集业务键：调用方 + 分组集确定性编码的 sha256
// 指纹（设计 §6.3）。分组集相同且调用方相同的不同 Key 共享条目；groupIDs 必
// 须先经 normalizeGroupModelUnionGroupIDs。
func groupModelUnionCacheKey(callerSystemAccountID string, groupIDs []string) string {
	digest := sha256.Sum256([]byte(strings.Join(groupIDs, "\n")))
	return callerSystemAccountID + ":" + hex.EncodeToString(digest[:])
}

// unionVersionKeyLocked 返回当前代共享桶名（调用方须持有 unionPublicationMu）。
func (s *Service) unionVersionKeyLocked() string {
	return groupModelUnionCacheName + ":" + s.processStartToken + ":" + strconv.FormatUint(s.unionGeneration, 10)
}

// captureUnionVersion 捕获当前版本键与代号：读取与装载全程使用捕获值；失效只
// 推进服务内代号，不影响在途 Redis 键（旧键因此不可达）。
func (s *Service) captureUnionVersion() (string, uint64) {
	s.unionPublicationMu.Lock()
	defer s.unionPublicationMu.Unlock()
	return s.unionVersionKeyLocked(), s.unionGeneration
}

// isUnionGenerationCurrent 报告捕获的代号是否仍是当前代。
func (s *Service) isUnionGenerationCurrent(generation uint64) bool {
	s.unionPublicationMu.Lock()
	defer s.unionPublicationMu.Unlock()
	return s.unionGeneration == generation
}

// ListCachedGroupModelUnionAsync 返回分组模型并集的缓存读取（设计 §6.3）：
// redis 模式共享优先读（命中进专用锁复核代号与有效期后回填本地），memory 模
// 式仅本地；未命中进入按代合并的 singleflight 后台装载。缓存面失败按既有容错
// 降级（读=未命中、写=WARN），loader 错误原样上抛；旧代结果作废并重算，不返
// 回、不回填。
func (s *Service) ListCachedGroupModelUnionAsync(ctx context.Context, input GroupModelUnionListOptions) (GroupModelUnionEntry, error) {
	if s.unionLoader == nil {
		return GroupModelUnionEntry{}, ErrGroupModelUnionLoaderUnavailable
	}
	s.syncInvalidationsBestEffort(ctx)
	callerID := input.CallerSystemAccountID
	groupIDs := normalizeGroupModelUnionGroupIDs(input.GroupIDs)
	cacheKey := groupModelUnionCacheKey(callerID, groupIDs)
	loaderInput := GroupModelUnionListOptions{CallerSystemAccountID: callerID, GroupIDs: groupIDs}

	for attempt := 0; attempt < groupModelUnionReadAttemptLimit; attempt++ {
		versionKey, generation := s.captureUnionVersion()

		// redis 模式：用捕获的当前版本键在锁外 GET 共享缓存（读失败按未命中）。
		if s.opts.Shared != nil {
			entry, ok, err := getSharedGroupModelUnion(ctx, s.opts.Shared.Cache(versionKey), cacheKey)
			if err != nil {
				s.logSharedFailure("gateway_group_model_union_shared_cache_read_failed", err)
			}
			if ok {
				if promoted, promotedOK := s.promoteUnionSharedEntry(generation, cacheKey, entry); promotedOK {
					return promoted, nil
				}
				// 复核失败（代号已推进/条目已到期）按未命中继续。
			}
		}

		// 本地 entryCache：memory 模式是唯一事实源，redis 模式是共享未命中后的
		// 回填层；命中同样复核代号与 ValidUntil。
		if entry, ok := s.getUnionLocalEntry(generation, cacheKey); ok {
			return entry, nil
		}

		// singleflight 注册（每代合并；同代并发未命中只装载一次）。
		pending, start, retry := s.registerUnionLoad(cacheKey, generation)
		if retry {
			// 捕获与注册之间代已被失效推进：以当前代重启读取，不登记跨代装载。
			continue
		}
		if start {
			go s.runUnionLoad(pending, cacheKey, versionKey, loaderInput)
		}
		select {
		case <-pending.done:
		case <-ctx.Done():
			// 等待者取消只退出自己；共享装载持独立有界 context 继续。
			return GroupModelUnionEntry{}, ctx.Err()
		}
		if !s.isUnionGenerationCurrent(pending.generation) {
			// 旧代结果（含错误）作废：不得回填本地、不得直接交给等待者，
			// 进入当前代重算。
			continue
		}
		if pending.err != nil {
			// loader 错误原样返回，不被缓存面失败掩盖。
			return GroupModelUnionEntry{}, pending.err
		}
		if s.clock.Now().Before(pending.entry.ValidUntil) {
			return pending.entry.Clone(), nil
		}
		// loader 返回前已过 validUntil：视为未命中，落入下一轮重算（设计 §6.3）。
	}
	return GroupModelUnionEntry{}, errGroupModelUnionReadRetryExhausted
}

// registerUnionLoad 在专用发布锁内注册或复用同代 singleflight 装载。注册前
// 双检本地缓存，闭合并发发布间隙；代号已推进时要求调用方以当前代重启。
// 返回 (装载, 是否新建需启动 goroutine, 是否需重试)。
func (s *Service) registerUnionLoad(cacheKey string, generation uint64) (*unionLoad, bool, bool) {
	s.unionPublicationMu.Lock()
	defer s.unionPublicationMu.Unlock()
	if s.unionGeneration != generation {
		return nil, false, true
	}
	if cached, ok := s.unionCache.get(cacheKey); ok && s.clock.Now().Before(cached.ValidUntil) {
		// 双检命中：并发装载已在本地检查之后发布，以已完成的合成装载返回。
		return &unionLoad{generation: generation, done: unionClosedDone, entry: cached.Clone(), finished: true}, false, false
	}
	if pending, ok := s.pendingUnionLoads[cacheKey]; ok {
		return pending, false, false
	}
	load := &unionLoad{generation: generation, done: make(chan struct{})}
	s.pendingUnionLoads[cacheKey] = load
	return load, true, false
}

// runUnionLoad 执行共享后台装载：独立有界 context（GatewayRuntimeLoadTimeout），
// 单个等待者取消不传播；发布在专用发布锁临界区内串行，Redis SET 在锁外用捕获
// 的版本键执行——完成在失效之后也只产生不可达旧键。
func (s *Service) runUnionLoad(load *unionLoad, cacheKey, versionKey string, input GroupModelUnionListOptions) {
	defer safego.Recover("gatewayruntimecache.union.background_load")
	defer func() {
		s.unionPublicationMu.Lock()
		if s.pendingUnionLoads[cacheKey] == load {
			delete(s.pendingUnionLoads, cacheKey)
		}
		if !load.finished {
			// loader panic / 提前退出臂：等待者不得悬挂。
			load.err = errGroupModelUnionLoadAborted
		}
		close(load.done)
		s.unionPublicationMu.Unlock()
	}()
	loadCtx, cancel := context.WithTimeout(context.Background(), GatewayRuntimeLoadTimeout)
	defer cancel()
	entry, err := s.unionLoader.ListGroupModelUnion(loadCtx, input)
	s.publishUnionLoad(load, cacheKey, versionKey, entry, err)
}

// publishUnionLoad 在专用发布锁临界区内完成装载结果落账：复核代号未变与
// now < ValidUntil 后写进程内 entryCache（TTL = min(基础 TTL, 剩余有效期)）；
// Redis SET 用捕获的版本键在锁外执行，失败记 WARN 不失败请求。
func (s *Service) publishUnionLoad(load *unionLoad, cacheKey, versionKey string, entry GroupModelUnionEntry, err error) {
	sharedSet := false
	var sharedTTL time.Duration
	s.unionPublicationMu.Lock()
	now := s.clock.Now()
	if err == nil && now.Before(entry.ValidUntil) {
		sharedTTL = entry.ValidUntil.Sub(now)
		if sharedTTL > GroupModelUnionBaseTTL {
			sharedTTL = GroupModelUnionBaseTTL
		}
		if load.generation == s.unionGeneration {
			s.unionCache.set(cacheKey, entry.Clone(), sharedTTL)
		}
		// 共享 SET 不以代号为前提：捕获的版本键保证失效后的写入只落在不可达
		// 旧桶（设计 §6.3），正确性不受影响。
		sharedSet = s.opts.Shared != nil
	}
	load.entry = entry
	load.err = err
	load.finished = true
	s.unionPublicationMu.Unlock()

	if sharedSet {
		ctx, cancel := context.WithTimeout(context.Background(), unionSharedCacheWriteTimeout)
		defer cancel()
		payload := sharedGroupModelUnionEntry{Models: entry.Models, ValidUntil: entry.ValidUntil}
		if setErr := s.opts.Shared.Cache(versionKey).Set(ctx, cacheKey, payload, sharedTTL); setErr != nil {
			s.logSharedFailure("gateway_group_model_union_shared_cache_write_failed", setErr)
		}
	}
}

// promoteUnionSharedEntry 是共享命中后的提升路径：专用锁内复核代号与有效期，
// 通过后按剩余有效期（非正值不回填）回填进程内 entryCache 并返回克隆；复核
// 失败按未命中处理。
func (s *Service) promoteUnionSharedEntry(generation uint64, cacheKey string, entry GroupModelUnionEntry) (GroupModelUnionEntry, bool) {
	s.unionPublicationMu.Lock()
	defer s.unionPublicationMu.Unlock()
	if generation != s.unionGeneration {
		return GroupModelUnionEntry{}, false
	}
	now := s.clock.Now()
	if !now.Before(entry.ValidUntil) {
		return GroupModelUnionEntry{}, false
	}
	ttl := entry.ValidUntil.Sub(now)
	if ttl > GroupModelUnionBaseTTL {
		ttl = GroupModelUnionBaseTTL
	}
	s.unionCache.set(cacheKey, entry.Clone(), ttl)
	return entry.Clone(), true
}

// getUnionLocalEntry 在专用发布锁内读取本地条目并复核代号与有效期。
func (s *Service) getUnionLocalEntry(generation uint64, cacheKey string) (GroupModelUnionEntry, bool) {
	s.unionPublicationMu.Lock()
	defer s.unionPublicationMu.Unlock()
	if generation != s.unionGeneration {
		return GroupModelUnionEntry{}, false
	}
	entry, ok := s.unionCache.get(cacheKey)
	if !ok || !s.clock.Now().Before(entry.ValidUntil) {
		return GroupModelUnionEntry{}, false
	}
	return entry.Clone(), true
}

// getSharedGroupModelUnion 解码共享载荷；键缺失按未命中（ok=false, err=nil），
// 读取/解码错误原样返回交由调用方记 WARN 后按未命中降级。
func getSharedGroupModelUnion(ctx context.Context, shared SharedCache, cacheKey string) (GroupModelUnionEntry, bool, error) {
	var decoded sharedGroupModelUnionEntry
	ok, err := shared.Get(ctx, cacheKey, &decoded)
	if err != nil || !ok {
		return GroupModelUnionEntry{}, false, err
	}
	return GroupModelUnionEntry{Models: decoded.Models, ValidUntil: decoded.ValidUntil}, true, nil
}

// cleanupUnionSharedBucket 异步尽力回收旧代共享桶：桶名含旧 token/generation，
// 当前代桶名不同、天然不会被误删（设计 §6.3）。失败记 WARN——旧代键已因版本
// 键不可达，删除只负责空间回收，不影响正确性。
func (s *Service) cleanupUnionSharedBucket(versionKey string) {
	shared := s.opts.Shared.Cache(versionKey)
	ctx, cancel := context.WithTimeout(context.Background(), unionSharedCacheWriteTimeout)
	defer cancel()
	if err := shared.Clear(ctx); err != nil {
		s.logSharedFailure("gateway_group_model_union_shared_cache_cleanup_failed", err)
	}
}
