package gatewaydispatch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 调度内核通用化设计 5.1 逐段状态机 Mock 回归矩阵（批次 1，文档 6.1/7）。
// 全部以明确尝试序列断言：scriptedUpstream 记录全局尝试顺序，不以"最终成功"
// 粗粒度断言代替。

// testAccountWithPriority 构造指定优先级的候选账户（四元 tier 的 Priority 位）。
func testAccountWithPriority(id string, priority int) AccountCandidate {
	account := testAccounts(id)[0]
	account.Priority = priority
	return account
}

// matrixAudit 记录审计标签与元数据值（释放事件需要字段级断言）。
type matrixAuditEvent struct {
	label    string
	metadata map[string]any
}

type matrixAudit struct {
	events []matrixAuditEvent
	sink   *fakeAuditSink
}

func (f *matrixAudit) BindContext(gatewaypreauth.AuditGatewayContext) {}

func (f *matrixAudit) AddGatewayMetadata(label string, metadata map[string]any) {
	copied := make(map[string]any, len(metadata))
	for key, value := range metadata {
		copied[key] = value
	}
	f.events = append(f.events, matrixAuditEvent{label: label, metadata: copied})
}

func (f *matrixAudit) Finalize(gatewaypreauth.AuditFinalizeInput) {}

func (f *matrixAudit) releaseEvents() []matrixAuditEvent {
	out := make([]matrixAuditEvent, 0, len(f.events))
	for _, event := range f.events {
		if event.label == "gateway_dispatch_exclusion_release" {
			out = append(out, event)
		}
	}
	return out
}

// scriptedUpstream 单上游服务器：按 Authorization（fakeDriver 固定形状
// Bearer key-<id>）识别账户，记录全局尝试序列，并按脚本决定成败。脚本返回
// 0 = 成功；>0 = 以该状态码失败（500 等瞬态会触发既有同账户重试，400 等
// 非瞬态单次失败即耗尽）。
type scriptedUpstream struct {
	mu     sync.Mutex
	calls  map[string]int
	order  []string
	script func(accountID string, call int) int
}

func newScriptedUpstream(script func(accountID string, call int) int) *scriptedUpstream {
	return &scriptedUpstream{calls: map[string]int{}, script: script}
}

func (s *scriptedUpstream) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer key-")
		s.mu.Lock()
		s.calls[id]++
		call := s.calls[id]
		s.order = append(s.order, id)
		s.mu.Unlock()
		status := 0
		if s.script != nil {
			status = s.script(id, call)
		}
		if status == 0 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-ok"}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down","type":"server_error","code":"upstream_error"}}`))
	}))
}

func (s *scriptedUpstream) hits(accountID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[accountID]
}

func (s *scriptedUpstream) attemptOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

// firstAppearanceOrder 投影尝试序列的首现顺序（同账户重试不改变序）。
func firstAppearanceOrder(order []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(order))
	for _, id := range order {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// newMatrixEngine 装配矩阵测试引擎：R4 注入全快退避（容量重试 / 可恢复等待 /
// 同账户重试均 1ms），场景确定且快速。
func newMatrixEngine(t *testing.T) (*Engine, *fakeDriver) {
	t.Helper()
	engine, driver, _ := newTestEngine(t)
	engine.Config.UpstreamRetryBackoffDelaysMs = []int64{1, 1, 1, 1}
	return engine, driver
}

// matrixURLs 为全部账户指到同一脚本服务器。
func matrixURLs(server *httptest.Server, ids ...string) map[string][]string {
	urls := make(map[string][]string, len(ids))
	for _, id := range ids {
		urls[id] = []string{server.URL + "/v1/chat/completions"}
	}
	return urls
}

func matrixDispatchArgs(t *testing.T, req *gatewaypreauth.GatewayRequest, accounts []AccountCandidate, audit *matrixAudit, excluded []string, segments []DispatchSegment) FetchFirstAvailableUpstreamArgs {
	t.Helper()
	args := dispatchArgs(t, req, accounts)
	args.AuditCapture = AuditCapture{Context: audit, Sink: audit.sink}
	if excluded != nil {
		args.SchedulingExclusions = &SchedulingExclusions{ExcludedAccountIDs: excluded}
	}
	args.DispatchSegments = segments
	return args
}

func matrixSegment(id string, accounts ...AccountCandidate) DispatchSegment {
	return DispatchSegment{
		OpaqueSegmentID: id,
		Tier:            dispatchPriorityTierOf(accounts[0], nil),
		Accounts:        accounts,
	}
}

// registerDispatchAttemptForTest 把账户预登记进请求尝试追踪器：引擎入口准入
// 注册（CanAttemptAccount）随后拒绝该账户（矩阵 b/d 的硬门钩子）。
func registerDispatchAttemptForTest(t *testing.T, args *FetchFirstAvailableUpstreamArgs, account AccountCandidate) {
	t.Helper()
	runtimeKey, err := gatewayAccountRuntimeKey(account)
	if err != nil {
		t.Fatalf("gatewayAccountRuntimeKey: %v", err)
	}
	if _, err := args.RequestCoordination.RequestAttemptTracker.TryRecordDispatchAttempt(gatewayrouting.GatewayDispatchAttemptRecordInput{
		GatewayDispatchAttemptIdentity: gatewayrouting.GatewayDispatchAttemptIdentity{
			AccountRuntimeKey:     runtimeKey,
			PhysicalCredentialKey: accountPhysicalCredentialKey(account),
			ProtocolModelKey:      "matrix-preregister:" + account.ID,
		},
	}); err != nil {
		t.Fatalf("TryRecordDispatchAttempt: %v", err)
	}
}

func assertReleaseEvent(t *testing.T, audit *matrixAudit, wantCount int, segmentID string, reason string) []matrixAuditEvent {
	t.Helper()
	events := audit.releaseEvents()
	if len(events) != wantCount {
		t.Fatalf("释放事件数 = %d, 期望 %d（全部事件: %v）", len(events), wantCount, audit.events)
	}
	if wantCount == 0 {
		return events
	}
	// 段 ID 按前缀匹配：等待/reload 重排后按设计重新生成为 "<来源ID>#rN"
	//（设计 5.1：新顺序不得复用旧段 ID）。
	if segmentID != "" {
		id, _ := events[0].metadata["opaqueSegmentId"].(string)
		if !strings.HasPrefix(id, segmentID) {
			t.Fatalf("opaqueSegmentId = %v, 期望前缀 %s", events[0].metadata["opaqueSegmentId"], segmentID)
		}
	}
	if reason != "" && events[0].metadata["reason"] != reason {
		t.Fatalf("释放原因 = %v, 期望 %s", events[0].metadata["reason"], reason)
	}
	tier, ok := events[0].metadata["tier"].(string)
	if !ok || tier == "" {
		t.Fatalf("释放事件必须携带四元 tier: %v", events[0].metadata)
	}
	released, ok := events[0].metadata["releasedAccountIds"].([]string)
	if !ok || len(released) == 0 {
		t.Fatalf("释放事件必须携带非空 releasedAccountIds: %v", events[0].metadata)
	}
	if _, ok := events[0].metadata["candidateAccountIds"].([]string); !ok {
		t.Fatalf("释放事件必须携带 candidateAccountIds: %v", events[0].metadata)
	}
	return events
}

// 矩阵 a（事故场景）：高优段唯一候选在排除集 → 入口立即释放尝试，低优段零
// 尝试（不再跨层降级）。
func TestMatrixAHighTierExcludedReleasesImmediately(t *testing.T) {
	upstream := newScriptedUpstream(func(accountID string, call int) int { return 0 })
	server := upstream.server()
	defer server.Close()
	engine, driver := newMatrixEngine(t)
	driver.urlByAccount = matrixURLs(server, "a-high", "a-low")
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &matrixAudit{sink: &fakeAuditSink{}}
	accounts := []AccountCandidate{testAccountWithPriority("a-high", 0), testAccountWithPriority("a-low", 10)}
	segments := []DispatchSegment{
		matrixSegment("seg-high", accounts[0]),
		matrixSegment("seg-low", accounts[1]),
	}
	args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-high"}, segments)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-high" {
		t.Fatalf("高优段唯一候选在排除集必须立即释放并服务, got %s", result.Account.ID)
	}
	if order := upstream.attemptOrder(); len(order) != 1 || order[0] != "a-high" {
		t.Fatalf("尝试序列 = %v（低优段必须零尝试）", order)
	}
	assertReleaseEvent(t, audit, 1, "seg-high", "segment_entry_no_ordinary_candidate")
}

// 矩阵 b（入口快照变体）：高优段排除账号 A + 同段候选 B，B 被入口硬门（准入
// 注册）跳过 → 动态释放 A，不先打低段。
func TestMatrixBEntryHardGateDynamicRelease(t *testing.T) {
	upstream := newScriptedUpstream(func(accountID string, call int) int { return 0 })
	server := upstream.server()
	defer server.Close()
	engine, driver := newMatrixEngine(t)
	driver.urlByAccount = matrixURLs(server, "a-1", "a-2", "a-3")
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &matrixAudit{sink: &fakeAuditSink{}}
	accounts := []AccountCandidate{
		testAccountWithPriority("a-1", 0),
		testAccountWithPriority("a-2", 0),
		testAccountWithPriority("a-3", 10),
	}
	segments := []DispatchSegment{
		matrixSegment("seg-high", accounts[0], accounts[1]),
		matrixSegment("seg-low", accounts[2]),
	}
	args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
	registerDispatchAttemptForTest(t, &args, accounts[1])
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("同段普通候选被硬门跳过后必须动态释放 A, got %s", result.Account.ID)
	}
	if order := upstream.attemptOrder(); len(order) != 1 || order[0] != "a-1" {
		t.Fatalf("尝试序列 = %v（B 零上游尝试，低段不得先打）", order)
	}
	assertReleaseEvent(t, audit, 1, "seg-high", "segment_entry_no_ordinary_candidate")
}

// 矩阵 c（多段资格）：高段 A 释放尝试失败 → 低段排除账号 B 独立释放
// （per-account 释放资格独立）。
func TestMatrixCMultiSegmentIndependentRelease(t *testing.T) {
	upstream := newScriptedUpstream(func(accountID string, call int) int {
		if accountID == "a-2" {
			return 0
		}
		return 400
	})
	server := upstream.server()
	defer server.Close()
	engine, driver := newMatrixEngine(t)
	driver.urlByAccount = matrixURLs(server, "a-1", "a-2")
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &matrixAudit{sink: &fakeAuditSink{}}
	accounts := []AccountCandidate{testAccountWithPriority("a-1", 0), testAccountWithPriority("a-2", 10)}
	segments := []DispatchSegment{
		matrixSegment("seg-high", accounts[0]),
		matrixSegment("seg-low", accounts[1]),
	}
	args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1", "a-2"}, segments)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("低段排除账号必须独立释放并服务, got %s", result.Account.ID)
	}
	if order := firstAppearanceOrder(upstream.attemptOrder()); len(order) != 2 || order[0] != "a-1" || order[1] != "a-2" {
		t.Fatalf("首现顺序 = %v, 期望 [a-1 a-2]", order)
	}
	if upstream.hits("a-2") != 1 {
		t.Fatalf("低段释放账号应恰好尝试一次, hits = %d", upstream.hits("a-2"))
	}
	events := audit.releaseEvents()
	if len(events) != 2 {
		t.Fatalf("两段各自释放, 事件数 = %d", len(events))
	}
	if events[0].metadata["opaqueSegmentId"] != "seg-high" || events[1].metadata["opaqueSegmentId"] != "seg-low" {
		t.Fatalf("释放事件段归属 = %v / %v", events[0].metadata["opaqueSegmentId"], events[1].metadata["opaqueSegmentId"])
	}
}

// 矩阵 d（硬门边界）：释放账号被准入拦截 → 视同段空跳段，不绕过硬门。
func TestMatrixDReleasedAccountHardGateSkipsSegment(t *testing.T) {
	upstream := newScriptedUpstream(func(accountID string, call int) int { return 0 })
	server := upstream.server()
	defer server.Close()
	engine, driver := newMatrixEngine(t)
	driver.urlByAccount = matrixURLs(server, "a-1", "a-2")
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &matrixAudit{sink: &fakeAuditSink{}}
	accounts := []AccountCandidate{testAccountWithPriority("a-1", 0), testAccountWithPriority("a-2", 0)}
	segments := []DispatchSegment{
		matrixSegment("seg-1", accounts[0]),
		matrixSegment("seg-2", accounts[1]),
	}
	args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
	registerDispatchAttemptForTest(t, &args, accounts[0])
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("释放账号被硬门拦截后应跳段, got %s", result.Account.ID)
	}
	if order := upstream.attemptOrder(); len(order) != 1 || order[0] != "a-2" {
		t.Fatalf("尝试序列 = %v（被拦截账号必须零上游尝试）", order)
	}
	assertReleaseEvent(t, audit, 1, "seg-1", "segment_entry_no_ordinary_candidate")
}

// 矩阵 e（健康分段顺序，事故反例固定序列）：候选 [B T0，C T1，A T0] 仅 B 排除
// → 失败/硬阻路径序列 B → C → A；B 或 C 成功立即结束。
func TestMatrixEHealthySegmentOrder(t *testing.T) {
	accounts := []AccountCandidate{
		testAccountWithPriority("a-1", 0),  // B：健康 T0，排除
		testAccountWithPriority("a-2", 10), // C：健康 T1
		testAccountWithPriority("a-3", 0),  // A：T0
	}
	segments := []DispatchSegment{
		matrixSegment("seg-b", accounts[0]),
		matrixSegment("seg-c", accounts[1]),
		matrixSegment("seg-a", accounts[2]),
	}

	t.Run("失败路径序列 B 到 C 到 A", func(t *testing.T) {
		upstream := newScriptedUpstream(func(accountID string, call int) int { return 500 })
		server := upstream.server()
		defer server.Close()
		engine, driver := newMatrixEngine(t)
		driver.urlByAccount = matrixURLs(server, "a-1", "a-2", "a-3")
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		audit := &matrixAudit{sink: &fakeAuditSink{}}
		args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
		_, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
		var attemptErr *UpstreamAttemptError
		if !errorsAs(err, &attemptErr) {
			t.Fatalf("全失败应返回 UpstreamAttemptError, got %v", err)
		}
		if order := firstAppearanceOrder(upstream.attemptOrder()); len(order) != 3 || order[0] != "a-1" || order[1] != "a-2" || order[2] != "a-3" {
			t.Fatalf("首现顺序 = %v, 期望 [a-1 a-2 a-3]（事故反例固定序列 B→C→A）", order)
		}
		assertReleaseEvent(t, audit, 1, "seg-b", "segment_entry_no_ordinary_candidate")
	})
	t.Run("B 成功立即结束", func(t *testing.T) {
		upstream := newScriptedUpstream(func(accountID string, call int) int { return 0 })
		server := upstream.server()
		defer server.Close()
		engine, driver := newMatrixEngine(t)
		driver.urlByAccount = matrixURLs(server, "a-1", "a-2", "a-3")
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		audit := &matrixAudit{sink: &fakeAuditSink{}}
		args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
		result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
		if err != nil {
			t.Fatalf("FetchFirstAvailableUpstream: %v", err)
		}
		if result.Account.ID != "a-1" {
			t.Fatalf("B 释放后成功必须立即结束, got %s", result.Account.ID)
		}
		if order := upstream.attemptOrder(); len(order) != 1 || order[0] != "a-1" {
			t.Fatalf("尝试序列 = %v（成功后不得继续后续账号）", order)
		}
	})
}

// postCycleMultiSuppress 编排 post-cycle 抑制：仅当输入为多账户（post-cycle
// 全量复查）且包含目标账户时抑制该账户（可恢复）；单账户输入（dispatchSingle
// 的每账户复查与可恢复子集复查）恒放行——镜像 phasedSuppression 的可恢复等
// 待驱动方式，且不受每账户复查调用次数影响。
type postCycleMultiSuppress struct {
	suppressID string
}

func (p *postCycleMultiSuppress) FilterAsync(_ context.Context, accounts []AccountCandidate, _ SuppressionFilterOptions) (SuppressionFilterResult, error) {
	result := SuppressionFilterResult{
		Accounts:               append([]AccountCandidate{}, accounts...),
		AcquiredHalfOpenLeases: []HalfOpenLease{},
	}
	if len(accounts) > 1 {
		for _, account := range accounts {
			if account.ID == p.suppressID {
				result.SuppressedAccountIDs = append(result.SuppressedAccountIDs, account.ID)
				result.SuppressedCount++
			}
		}
	}
	return result, nil
}

func (p *postCycleMultiSuppress) ResolveLocalSuppressionFilter(_ context.Context, input LocalSuppressionPreflightInput) (*SuppressionFilterResult, bool, error) {
	result := localSuppressionBypassResult(input.Accounts)
	return &result, false, nil
}

// queueGatedConcurrencyStore 在队列等待发生前拒绝获取（容量满），队列等待
// 触发后放行——驱动既有"外环容量等待 → 重排 → 重试"恢复路径。
type queueGatedConcurrencyStore struct {
	queueHit atomic.Bool
	calls    atomic.Int64
	acquired atomic.Int64
}

func (s *queueGatedConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *queueGatedConcurrencyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *queueGatedConcurrencyStore) TryAcquireAsync(_ context.Context, _ string, concurrencyLimit int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	s.calls.Add(1)
	if !s.queueHit.Load() {
		return ConcurrencySlot{Acquired: false, Current: concurrencyLimit, Limit: concurrencyLimit, Lane: options.Lane}, nil
	}
	s.acquired.Add(1)
	return ConcurrencySlot{
		Acquired:        true,
		Current:         1,
		Limit:           concurrencyLimit,
		Lane:            options.Lane,
		Release:         func() {},
		MarkFirstOutput: func() {},
	}, nil
}

// queueHitFlipper 在容量队列等待时翻转存储闸门（等待即恢复信号）。
type queueHitFlipper struct {
	store *queueGatedConcurrencyStore
}

func (q *queueHitFlipper) WaitForCapacity(_ context.Context, _ HighConcurrencyWaitInput) (QueueWaitResult, error) {
	q.store.queueHit.Store(true)
	return QueueWaitResult{Ready: true, Reason: "drained"}, nil
}

// 矩阵 f（真实失败后释放 + 可恢复等待）：段内普通候选真实耗尽既有重试 → 释放
// 排除账号 → 释放账号命中可恢复容量失败，按既有外环容量等待恢复后继续重试，
// 无需二次释放。
func TestMatrixFReleaseThenRecoverableWait(t *testing.T) {
	upstream := newScriptedUpstream(func(accountID string, call int) int {
		if accountID == "a-1" {
			return 0
		}
		return 500 // b 恒败，真实耗尽既有重试
	})
	server := upstream.server()
	defer server.Close()
	engine, driver := newMatrixEngine(t)
	engine.Config.AccountConcurrencyRetryBudgetMs = 1
	store := &queueGatedConcurrencyStore{}
	engine.Concurrency = store
	engine.HighConcurrencyQueue = &queueHitFlipper{store: store}
	driver.urlByAccount = matrixURLs(server, "a-1", "b-1")
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &matrixAudit{sink: &fakeAuditSink{}}
	accounts := []AccountCandidate{
		testAccountWithPriority("b-1", 0),
		testAccountWithPriority("a-1", 0),
	}
	segments := []DispatchSegment{matrixSegment("seg-1", accounts...)}
	args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
	args.GroupSchedulingPolicy = &gatewayruntimecache.GroupSchedulingPolicy{}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("容量等待恢复后 a-1 应服务, got %s", result.Account.ID)
	}
	order := upstream.attemptOrder()
	if firstAppearanceOrder(order)[0] != "b-1" {
		t.Fatalf("普通候选必须先真实耗尽: %v", order)
	}
	if upstream.hits("a-1") != 1 {
		t.Fatalf("a-1 应在容量恢复后恰好尝试一次, hits = %d（order=%v）", upstream.hits("a-1"), order)
	}
	if store.calls.Load() <= store.acquired.Load() || store.acquired.Load() != 2 {
		t.Fatalf("容量门应先拒后放且每账户恰取得一次: calls=%d acquired=%d", store.calls.Load(), store.acquired.Load())
	}
	// 同一次引擎调用内至多一次释放：容量等待/reload 后重试不重新消耗释放资格。
	assertReleaseEvent(t, audit, 1, "seg-1", "segment_ordinary_candidates_exhausted")
}

// 矩阵 g（钉住豁免）：SameAccountRetry 钉住账号在排除集时照常钉住尝试，不因
// 排除集被去重，也不触发释放。
func TestMatrixGSameAccountRetryPinningExempt(t *testing.T) {
	upstream := newScriptedUpstream(func(accountID string, call int) int { return 0 })
	server := upstream.server()
	defer server.Close()
	engine, driver := newMatrixEngine(t)
	driver.urlByAccount = matrixURLs(server, "a-1", "a-2")
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &matrixAudit{sink: &fakeAuditSink{}}
	accounts := []AccountCandidate{testAccountWithPriority("a-1", 0), testAccountWithPriority("a-2", 10)}
	segments := []DispatchSegment{matrixSegment("seg-1", accounts...)}
	args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
	// RetryID 留空：同账户重试注册通道要求引擎内预留（与 BUG-0289 钉住契约
	// 一致），钉住豁免语义只依赖 SameAccountRetry != nil。
	args.RequestCoordination.SameAccountRetry = &SameAccountRetry{RetryID: "", Account: accounts[0]}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("钉住目标在排除集必须照常尝试, got %s", result.Account.ID)
	}
	if order := upstream.attemptOrder(); len(order) != 1 || order[0] != "a-1" {
		t.Fatalf("尝试序列 = %v（钉住窗口塌缩到单账户）", order)
	}
	assertReleaseEvent(t, audit, 0, "", "")
}

// 矩阵 h（重复释放禁止 + 释放状态跨等待/reload 持续）：同段两个排除账号一次
// 释放（单事件、各一次），经外环容量等待与 reload 重排后继续，不产生第二次
// 释放；已释放状态跨等待持续。
func TestMatrixHReleaseOnceAcrossReload(t *testing.T) {
	upstream := newScriptedUpstream(func(accountID string, call int) int {
		if accountID == "c-1" {
			return 400 // 非瞬态，单次即耗尽
		}
		return 0 // b-1 恢复后成功
	})
	server := upstream.server()
	defer server.Close()
	engine, driver := newMatrixEngine(t)
	engine.Config.AccountConcurrencyRetryBudgetMs = 1
	store := &queueGatedConcurrencyStore{}
	engine.Concurrency = store
	engine.HighConcurrencyQueue = &queueHitFlipper{store: store}
	driver.urlByAccount = matrixURLs(server, "c-1", "b-1", "b-2")
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &matrixAudit{sink: &fakeAuditSink{}}
	accounts := []AccountCandidate{
		testAccountWithPriority("c-1", 0),
		testAccountWithPriority("b-1", 0),
		testAccountWithPriority("b-2", 0),
	}
	segments := []DispatchSegment{matrixSegment("seg-1", accounts...)}
	args := matrixDispatchArgs(t, req, accounts, audit, []string{"b-1", "b-2"}, segments)
	args.GroupSchedulingPolicy = &gatewayruntimecache.GroupSchedulingPolicy{}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "b-1" {
		t.Fatalf("容量等待恢复后 b-1 应服务, got %s", result.Account.ID)
	}
	if order := firstAppearanceOrder(upstream.attemptOrder()); len(order) != 2 || order[0] != "c-1" || order[1] != "b-1" {
		t.Fatalf("首现顺序 = %v, 期望 [c-1 b-1]", order)
	}
	if upstream.hits("b-1") != 1 {
		t.Fatalf("b-1 hits = %d", upstream.hits("b-1"))
	}
	// 两个排除账号在同一次释放中各解除一次；容量等待/reload 后无第二次释放。
	events := assertReleaseEvent(t, audit, 1, "seg-1", "segment_ordinary_candidates_exhausted")
	released := events[0].metadata["releasedAccountIds"].([]string)
	if strings.Join(released, ",") != "b-1,b-2" {
		t.Fatalf("releasedAccountIds = %v, 期望 [b-1 b-2]", released)
	}
}

// 矩阵 i（分派段元数据，准备层）：最终候选 [T0，T1，T0] 生成三个连续段；
// 相同 tier 非连续不合并、不数值排序；健康/降级外层段界切断同 tier run。
func TestMatrixIDispatchSegmentMetadata(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	accounts := []AccountCandidate{
		testAccountWithPriority("a-1", 0),
		testAccountWithPriority("a-2", 10),
		testAccountWithPriority("a-3", 20),
	}
	input := dispatchPreparationInput(t, accounts)
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if len(result.DispatchSegments) != 3 {
		t.Fatalf("三个互异 tier 应生成三个连续段: %d", len(result.DispatchSegments))
	}
	seen := map[string]struct{}{}
	concatenated := make([]string, 0, len(result.Accounts))
	for _, segment := range result.DispatchSegments {
		if _, dup := seen[segment.OpaqueSegmentID]; dup {
			t.Fatalf("OpaqueSegmentID 必须逐段唯一: %s", segment.OpaqueSegmentID)
		}
		seen[segment.OpaqueSegmentID] = struct{}{}
		concatenated = append(concatenated, accountIDs(segment.Accounts)...)
	}
	if strings.Join(concatenated, ",") != "a-1,a-2,a-3" {
		t.Fatalf("段内候选必须保持最终原顺序（不数值重排）: %v", concatenated)
	}
	if result.DispatchSegments[0].Tier == result.DispatchSegments[1].Tier ||
		result.DispatchSegments[1].Tier == result.DispatchSegments[2].Tier {
		t.Fatal("不同 tier 段不得合并")
	}
	// 非连续相同 tier 不合并在切段纯函数锁定（TestBuildDispatchSegmentsRuns）；
	// 此处锁定健康/降级外层段界：速度优先 + a-3 延迟降级（tier 与 a-1 相同）
	// → a-1 与 a-3 分属不同外层段，不得合并。
	engine.Latency = &flagLatencyPort{applied: true, degraded: []string{"a-3"}}
	accounts = []AccountCandidate{
		testAccountWithPriority("a-1", 0),
		testAccountWithPriority("a-2", 10),
		testAccountWithPriority("a-3", 0),
	}
	input = dispatchPreparationInput(t, accounts)
	input.NormalRouteSpeedFirstConfig = &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{}
	result, err = pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts(speed-first): %v", err)
	}
	if len(result.DispatchSegments) != 3 {
		t.Fatalf("健康/降级外层段界必须切断同 tier run: %d", len(result.DispatchSegments))
	}
	if len(result.DispatchSegments[2].Accounts) != 1 || result.DispatchSegments[2].Accounts[0].ID != "a-3" {
		t.Fatalf("a-3 应独立成段: %v", accountIDs(result.DispatchSegments[2].Accounts))
	}
}

// 矩阵 j（硬门副作用隔离）：段产出纯函数不改输入；真实尝试路径硬门（并发槽）
// 每账户只执行一次，无重复租约。
func TestMatrixJHardGateSideEffectIsolation(t *testing.T) {
	accounts := []AccountCandidate{testAccountWithPriority("a-1", 0), testAccountWithPriority("a-2", 10)}
	snapshot := append([]AccountCandidate(nil), accounts...)
	_ = buildDispatchSegments(accounts, nil, nil)
	for i := range accounts {
		if accounts[i].ID != snapshot[i].ID || accounts[i].Priority != snapshot[i].Priority {
			t.Fatal("buildDispatchSegments 不得改写输入候选")
		}
	}

	upstream := newScriptedUpstream(func(accountID string, call int) int {
		if accountID == "a-1" {
			return 400
		}
		return 0
	})
	server := upstream.server()
	defer server.Close()
	engine, driver := newMatrixEngine(t)
	store := &fakeConcurrencyStore{}
	engine.Concurrency = store
	driver.urlByAccount = matrixURLs(server, "a-1", "a-2")
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &matrixAudit{sink: &fakeAuditSink{}}
	segments := []DispatchSegment{
		matrixSegment("seg-1", accounts[0]),
		matrixSegment("seg-2", accounts[1]),
	}
	args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("got %s", result.Account.ID)
	}
	// 每个真实尝试路径账户恰取得一次并发槽（a-1 一轮 + a-2 一轮 = 2），无重复租约。
	if acquired := store.acquired.Load(); acquired != 2 {
		t.Fatalf("并发槽取得次数 = %d, 期望 2（硬门只执行一次）", acquired)
	}
}

// 矩阵 k（释放状态与终态）：临时硬阻按既有等待恢复且保留释放状态（矩阵 f/h
// 覆盖）；此处锁定硬终止/终态不复活——硬失败账号跨段不重新进入候选。
func TestMatrixKHardTerminalNotRevived(t *testing.T) {
	upstream := newScriptedUpstream(func(accountID string, call int) int {
		if accountID == "a-2" {
			return 0
		}
		return 400
	})
	server := upstream.server()
	defer server.Close()
	engine, driver := newMatrixEngine(t)
	driver.urlByAccount = matrixURLs(server, "a-1", "a-2")
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &matrixAudit{sink: &fakeAuditSink{}}
	accounts := []AccountCandidate{testAccountWithPriority("a-1", 0), testAccountWithPriority("a-2", 0)}
	segments := []DispatchSegment{
		matrixSegment("seg-1", accounts[0]),
		matrixSegment("seg-2", accounts[1]),
	}
	args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-2" {
		t.Fatalf("got %s", result.Account.ID)
	}
	if upstream.hits("a-1") != 1 {
		t.Fatalf("硬终止账号不得复活, a-1 hits = %d", upstream.hits("a-1"))
	}
	assertReleaseEvent(t, audit, 1, "seg-1", "segment_entry_no_ordinary_candidate")
}

// 矩阵 l（合法预约预算）：KeyRotation / SameAccountRetry / 有效锁租约保留预约
// 上下文，不被账户 ID 去重误挡，不增加翻回或重试预算。钉住豁免语义见矩阵 g；
// 此处锁定：重复 ID 候选切段不去重 + 有效租约与钉住预约组合照常尝试、零释放。
func TestMatrixLReservationsNotDeduplicated(t *testing.T) {
	duplicated := []AccountCandidate{testAccountWithPriority("a-1", 0), testAccountWithPriority("a-1", 0)}
	segments := buildDispatchSegments(duplicated, nil, nil)
	if len(segments) != 1 || len(segments[0].Accounts) != 2 {
		t.Fatalf("重复 ID 候选同段不得去重: %#v", segments)
	}

	upstream := newScriptedUpstream(func(accountID string, call int) int { return 0 })
	server := upstream.server()
	defer server.Close()
	engine, driver := newMatrixEngine(t)
	driver.urlByAccount = matrixURLs(server, "a-1")
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &matrixAudit{sink: &fakeAuditSink{}}
	accounts := []AccountCandidate{testAccountWithPriority("a-1", 0)}
	segmentsPinned := []DispatchSegment{matrixSegment("seg-1", accounts[0])}
	args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segmentsPinned)
	args.RequestCoordination.AccountLockRetryLease = &AccountLockRetryLease{AccountID: "a-1", LeaseID: "lease-1"}
	args.RequestCoordination.SameAccountRetry = &SameAccountRetry{RetryID: "", Account: accounts[0], AccountLockLeaseID: "lease-1"}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "a-1" {
		t.Fatalf("有效租约/钉住预约不得被排除集误挡, got %s", result.Account.ID)
	}
	if upstream.hits("a-1") != 1 {
		t.Fatalf("预约路径不得叠加额外尝试预算, hits = %d", upstream.hits("a-1"))
	}
	assertReleaseEvent(t, audit, 0, "", "")
}

// 矩阵 m（相邻同 tier 的外层分段）：[B T0，A T0] 仅 B 排除 → 两段不合并；
// B 成功立即结束（A 零尝试），B 耗尽后才进降级段尝试 A。
func TestMatrixMAdjacentSameTierSegments(t *testing.T) {
	accounts := []AccountCandidate{
		testAccountWithPriority("a-1", 0), // B：健康 T0，排除
		testAccountWithPriority("a-2", 0), // A：降级 T0
	}
	segments := []DispatchSegment{
		matrixSegment("seg-b", accounts[0]),
		matrixSegment("seg-a", accounts[1]),
	}

	t.Run("B 成功立即结束", func(t *testing.T) {
		upstream := newScriptedUpstream(func(accountID string, call int) int { return 0 })
		server := upstream.server()
		defer server.Close()
		engine, driver := newMatrixEngine(t)
		driver.urlByAccount = matrixURLs(server, "a-1", "a-2")
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		audit := &matrixAudit{sink: &fakeAuditSink{}}
		args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
		result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
		if err != nil {
			t.Fatalf("FetchFirstAvailableUpstream: %v", err)
		}
		if result.Account.ID != "a-1" {
			t.Fatalf("got %s", result.Account.ID)
		}
		if order := upstream.attemptOrder(); len(order) != 1 || order[0] != "a-1" {
			t.Fatalf("尝试序列 = %v（两段不得合并——合并则 a-2 作为普通候选先打）", order)
		}
	})
	t.Run("B 耗尽后进 A", func(t *testing.T) {
		upstream := newScriptedUpstream(func(accountID string, call int) int {
			if accountID == "a-2" {
				return 0
			}
			return 400
		})
		server := upstream.server()
		defer server.Close()
		engine, driver := newMatrixEngine(t)
		driver.urlByAccount = matrixURLs(server, "a-1", "a-2")
		req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
		audit := &matrixAudit{sink: &fakeAuditSink{}}
		args := matrixDispatchArgs(t, req, accounts, audit, []string{"a-1"}, segments)
		result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
		if err != nil {
			t.Fatalf("FetchFirstAvailableUpstream: %v", err)
		}
		if result.Account.ID != "a-2" {
			t.Fatalf("got %s", result.Account.ID)
		}
		if order := firstAppearanceOrder(upstream.attemptOrder()); len(order) != 2 || order[0] != "a-1" || order[1] != "a-2" {
			t.Fatalf("首现顺序 = %v, 期望 [a-1 a-2]", order)
		}
	})
}

// accountGatedConcurrencyStore 只对指定账户在容量队列等待发生前拒绝获取（段 1
// 候选容量受限），其余账户照常放行（段 2 候选空闲）——驱动"段 1 容量失败、
// 段 2 空闲"的整池等待域场景。
type accountGatedConcurrencyStore struct {
	blockedID string
	queueHit  atomic.Bool
	calls     atomic.Int64
	acquired  atomic.Int64
}

func (s *accountGatedConcurrencyStore) LoadCurrentAsync(context.Context, []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *accountGatedConcurrencyStore) LoadCurrentByLaneAsync(context.Context, []string, string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *accountGatedConcurrencyStore) TryAcquireAsync(_ context.Context, accountID string, concurrencyLimit int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	s.calls.Add(1)
	if accountID == s.blockedID && !s.queueHit.Load() {
		return ConcurrencySlot{Acquired: false, Current: concurrencyLimit, Limit: concurrencyLimit, Lane: options.Lane}, nil
	}
	s.acquired.Add(1)
	return ConcurrencySlot{
		Acquired:        true,
		Current:         1,
		Limit:           concurrencyLimit,
		Lane:            options.Lane,
		Release:         func() {},
		MarkFirstOutput: func() {},
	}, nil
}

// poolRecordingQueue 记录每次容量队列等待的整池入参（等待域断言载体），并在
// 等待时翻转并发闸门（等待即恢复信号）。
type poolRecordingQueue struct {
	store  *accountGatedConcurrencyStore
	mu     sync.Mutex
	inputs []HighConcurrencyWaitInput
}

func (q *poolRecordingQueue) WaitForCapacity(_ context.Context, input HighConcurrencyWaitInput) (QueueWaitResult, error) {
	q.mu.Lock()
	q.inputs = append(q.inputs, input)
	q.mu.Unlock()
	q.store.queueHit.Store(true)
	return QueueWaitResult{Ready: true, Reason: "drained"}, nil
}

func (q *poolRecordingQueue) recordedInputs() []HighConcurrencyWaitInput {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]HighConcurrencyWaitInput(nil), q.inputs...)
}

// 回炉回归（批次 1 复审裁决：整池等待域，锁定无排除场景行为等价）：无排除
// 集、两个段——段 1（高优）候选容量受限、段 2（低优）候选空闲。旧行为（整池）
// 在单周期内段 1 容量失败后段 2 立即被真实尝试，整池周期走完后容量等待才触发
// 且等待对象包含两段候选；等待域若被收窄到当前活动段，段 2 首试会被段 1 的
// 容量等待推迟（可达数十秒），且等待对象只含段 1 候选。
func TestWholePoolCapacityWaitAcrossSegments(t *testing.T) {
	upstream := newScriptedUpstream(func(accountID string, call int) int {
		if accountID == "p-1" {
			return 0 // 段 1 候选容量恢复后成功
		}
		return 500 // 段 2 候选空闲但上游瞬态失败，保证本轮整池周期走完才等待
	})
	server := upstream.server()
	defer server.Close()
	engine, driver := newMatrixEngine(t)
	engine.Config.AccountConcurrencyRetryBudgetMs = 1
	store := &accountGatedConcurrencyStore{blockedID: "p-1"}
	queue := &poolRecordingQueue{store: store}
	engine.Concurrency = store
	engine.HighConcurrencyQueue = queue
	driver.urlByAccount = matrixURLs(server, "p-1", "p-2")
	req := newTestRequest(t, `{"model":"gpt-test","stream":false}`)
	audit := &matrixAudit{sink: &fakeAuditSink{}}
	accounts := []AccountCandidate{
		testAccountWithPriority("p-1", 0),  // 段 1（高优）：容量受限
		testAccountWithPriority("p-2", 10), // 段 2（低优）：空闲
	}
	segments := []DispatchSegment{
		matrixSegment("seg-1", accounts[0]),
		matrixSegment("seg-2", accounts[1]),
	}
	args := matrixDispatchArgs(t, req, accounts, audit, nil, segments)
	args.GroupSchedulingPolicy = &gatewayruntimecache.GroupSchedulingPolicy{}
	result, err := engine.FetchFirstAvailableUpstream(context.Background(), args)
	if err != nil {
		t.Fatalf("FetchFirstAvailableUpstream: %v", err)
	}
	if result.Account.ID != "p-1" {
		t.Fatalf("容量恢复后段 1 候选应服务, got %s", result.Account.ID)
	}
	// 断言一：段 2 候选在段 1 容量失败的同一周期内即被真实尝试（首现先于段 1
	// 的任何上游尝试），不被段 1 容量等待推迟。
	order := firstAppearanceOrder(upstream.attemptOrder())
	if len(order) != 2 || order[0] != "p-2" || order[1] != "p-1" {
		t.Fatalf("首现顺序 = %v, 期望 [p-2 p-1]（段 2 不被段 1 容量等待推迟）", order)
	}
	// 断言二：整池容量等待恰好一次，且等待对象包含两段候选（整池域，而非段 1
	// 单独的收窄域）。
	inputs := queue.recordedInputs()
	if len(inputs) != 1 {
		t.Fatalf("容量队列等待次数 = %d, 期望 1", len(inputs))
	}
	if got := strings.Join(inputs[0].AccountIDs, ","); got != "p-1,p-2" {
		t.Fatalf("容量等待 AccountIDs = %q, 期望整池 %q", got, "p-1,p-2")
	}
	// 两段候选各发生真实上游尝试（段 1 恰一次成功，段 2 至少一次瞬态失败）。
	if upstream.hits("p-1") != 1 {
		t.Fatalf("p-1 hits = %d, 期望 1", upstream.hits("p-1"))
	}
	if upstream.hits("p-2") < 1 {
		t.Fatalf("p-2 hits = %d, 期望 >= 1", upstream.hits("p-2"))
	}
}
