package main

// w1（chain_v1.go / chain_chat_mount.go 收尾补测批次，w1v 前缀续 w1_v1_arms*）：
// 对照 %TEMP%/w1pkg-FINAL.out 两文件的零计数块逐块补测。两层驱动：
//
//  1. 进程内 mock 直驱（newV1TestLoop / composeGatewayChain(chainSmokeDeps)
//     的真实链 + 注入桩：电路桩、候选管道桩、决策桩）——覆盖
//     handleOpenAIGatewayRequest 各早退臂、preflight 意外错误、
//     resolveRouteAction 回退错误调用点、speed-first body admission 三臂、
//     handleUpstreamResponse 中止出口、run() 收窄窗口、settle* 各回退臂、
//     决策闭包成功返回、协议成功结算、Retry-After 下限、FinalizeLazy 转换、
//     chatAttachStreamHandler 写失败/marshal 失败/订阅者溢出关闭臂。
//  2. 真实 httptest 上游的全链路 SSE 场景（composeGatewayChain + 真实
//     dispatch + inspection 策略）——覆盖 run() 的 RetryUpstream 续环（593）
//     与剩余候选为空后的跨组回退切换（700）。
//
// 跳过并登记理由（恒不可达守卫 / 当前实现不存在该出口 / 他人范围）：
//
//   - chain_chat_mount.go 39（hub.now 在 chat 包内已无调用点，闭包体为死代码）、
//     41（chat.NewStore 仅 db==nil 报错，上游 30-32 已守卫）、
//     47（newChatTokenCount 仅内嵌 tokenizer 加载失败报错，进程内恒成功）、
//     117/120（writeEvent 的 ended 守卫与 data==nil 兜底：事件循环单线程，
//     每次 end() 后立即 return，闭包不外露）、
//     168（心跳 !active：ended=true 时循环必已退出）、
//     213-219（openChatDatabase 的 pgDialect=true 臂：按任务约定属他人范围）。
//   - chain_v1.go 162-163（HandleParserRejection 对非 nil perr 且头未发送恒
//     返回 true，false 臂不可达）、166-169（Capture 当前实现全部失败路径返回
//     (nil,nil)，不再产生 error）、228-231（newRequestBudgets 失败需空
//     traceID，上游 113-114 CreateTraceID 已兜底）、
//     1495-1498/1547-1549（AttachReservation 成功需协调器 active 态，状态仅
//     引擎内部流转、字段未导出，进程内不可注入）、
//     2569-2575（newRequestBudgets 的 wall/tracker 构造错误需显式非法入参，
//     调用点传入常量合法值）。
//
// 全程确定性：httptest loopback 上游、无真实时钟依赖（chat 溢出场景用
// w1hBlockingWriter 的 channel 同步点），不启动插桩二进制（目标行全部可在
// 进程内确定性触达，w1l 二进制 harness 只会增加重复覆盖）。

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// ---------------------------------------------------------------------------
// 测试替身（w1v 前缀，全部同步、确定性；不与既有 w1v* 重名）
// ---------------------------------------------------------------------------

// w1vCircuitsStub 包装真实 PreAuthCircuits：仅脚本化 InspectClientIPErrorCircuit
// （按 GroupID 拒绝 / 注入错误），其余方法透传。
type w1vCircuitsStub struct {
	inner          gatewaypreauth.PreAuthCircuits
	inspectErr     error
	blockedGroupID string
}

func (s *w1vCircuitsStub) InspectPreAuthCircuit(ctx context.Context, input gatewaypreauth.PreAuthCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	return s.inner.InspectPreAuthCircuit(ctx, input)
}

func (s *w1vCircuitsStub) RecordPreAuthFailure(ctx context.Context, input gatewaypreauth.PreAuthFailureInput) (gatewaypreauth.CircuitDecision, error) {
	return s.inner.RecordPreAuthFailure(ctx, input)
}

func (s *w1vCircuitsStub) InspectClientIPErrorCircuit(ctx context.Context, input gatewaypreauth.ClientIPErrorCircuitInput) (gatewaypreauth.CircuitDecision, error) {
	if s.inspectErr != nil {
		return gatewaypreauth.CircuitDecision{}, s.inspectErr
	}
	if s.blockedGroupID != "" && input.GroupID == s.blockedGroupID {
		return gatewaypreauth.CircuitDecision{Blocked: true, Reason: "w1v_scripted"}, nil
	}
	return s.inner.InspectClientIPErrorCircuit(ctx, input)
}

func (s *w1vCircuitsStub) RecordClientIPErrorCircuitSuccess(ctx context.Context, input gatewaypreauth.ClientIPErrorCircuitInput) error {
	return s.inner.RecordClientIPErrorCircuitSuccess(ctx, input)
}

func (s *w1vCircuitsStub) RecordClientIPErrorCircuitSample(ctx context.Context, input gatewaypreauth.ClientIPErrorCircuitSampleInput) (gatewaypreauth.CircuitDecision, error) {
	return s.inner.RecordClientIPErrorCircuitSample(ctx, input)
}

// w1vResolveStep 是候选解析脚本的一步。accountIDs 非空时随候选携带账户窗口
// （回退预检据此产出 DispatchContext 而非空窗口 RouteAction）；baseURL 为该
// 窗口账户的上游基地址（空 = 无上游可派发）；wrongModel 把窗口账户的支持
// 模型改成 other-model（回退预检模型门排空 → RouteAction）。
type w1vResolveStep struct {
	err        error
	found      bool
	groupID    string
	accountIDs []string
	baseURL    string
	wrongModel bool
}

// w1vChainCandidatePipeline 包装真实 CandidatePipeline：FilterCandidates 可
// 脚本化为空窗口（仅前 filterEmptyCalls 次调用生效，用于把初始预检候选拍成
// RouteAction；回退预检的候选阶段照常透传），ResolveNextGroupFallbackCandidate
// 按脚本逐步返回，脚本耗尽后透传真实管道。
type w1vChainCandidatePipeline struct {
	inner            gatewaypreauth.CandidatePipeline
	filterEmptyCalls int
	filterCalls      int
	resolve          []w1vResolveStep
	resolveCalls     int
	debugT           *testing.T
}

func (s *w1vChainCandidatePipeline) FilterCandidates(ctx context.Context, input gatewaypreauth.CandidateFilterInput) (gatewaypreauth.CandidateFilterResult, error) {
	s.filterCalls++
	if s.filterEmptyCalls > 0 && s.filterCalls <= s.filterEmptyCalls {
		return gatewaypreauth.CandidateFilterResult{}, nil
	}
	return s.inner.FilterCandidates(ctx, input)
}

func (s *w1vChainCandidatePipeline) PrepareDispatchAccounts(ctx context.Context, input gatewaypreauth.DispatchPreparationInput) (gatewaypreauth.DispatchPreparationResult, error) {
	return s.inner.PrepareDispatchAccounts(ctx, input)
}

func (s *w1vChainCandidatePipeline) ResolveNextGroupFallbackCandidate(ctx context.Context, input gatewaypreauth.GroupFallbackCandidateInput) (gatewaypreauth.GroupFallbackCandidate, bool, error) {
	index := s.resolveCalls
	s.resolveCalls++
	if s.debugT != nil {
		bindingCount := 0
		if input.APIKeyRecord != nil {
			bindingCount = len(input.APIKeyRecord.GroupBindings)
		}
		s.debugT.Logf("w1v resolve #%d group=%s reason=%s excluded=%v bindings=%d snapshotCursor=%d snapshotTargets=%v",
			index, input.GroupID, input.Reason, input.ExcludedAccountIDs, bindingCount,
			input.RoutePlanSnapshot.Cursor, input.RoutePlanSnapshot.OrderedAllowedTargets)
	}
	if index >= len(s.resolve) {
		result, found, err := s.inner.ResolveNextGroupFallbackCandidate(ctx, input)
		if s.debugT != nil {
			s.debugT.Logf("w1v resolve #%d real → found=%v group=%q err=%v", index, found, result.GroupID, err)
		}
		return result, found, err
	}
	step := s.resolve[index]
	if step.err != nil {
		return gatewaypreauth.GroupFallbackCandidate{}, false, step.err
	}
	candidate := gatewaypreauth.GroupFallbackCandidate{GroupID: step.groupID}
	for _, accountID := range step.accountIDs {
		// 脚本候选账户携带支持面与上游指向：回退预检的模型能力门按
		// CandidateAccounts 的 SupportedModels 过滤，派发按 BaseURL 出站。
		models := []string{"gpt-test"}
		if step.wrongModel {
			models = []string{"other-model"}
		}
		candidate.Accounts = append(candidate.Accounts, gatewaypreauth.AccountCandidate{
			ID:              accountID,
			ProviderCode:    "openai",
			ProtocolCode:    "openai",
			Type:            "api_key",
			APIKey:          "sk-upstream-account-key",
			BaseURL:         step.baseURL,
			SupportedModels: models,
		})
	}
	return candidate, step.found, nil
}

// w1vScriptedDecisions 在 w1vDecisionsStub 上按调用次序注入降级查询错误
// （第 N 次 IsAccountLatencyDegradedAsync 报错），其余行为透传。
type w1vScriptedDecisions struct {
	*w1vDecisionsStub
	degradedErrAtCall map[int]error
	degradedCallCount int
}

func (s *w1vScriptedDecisions) IsAccountLatencyDegradedAsync(ctx context.Context, account gatewaydispatch.AccountCandidate, scope *gatewaydispatch.LatencyScopeInput) (bool, error) {
	s.degradedCallCount++
	if err, ok := s.degradedErrAtCall[s.degradedCallCount]; ok {
		return false, err
	}
	return s.w1vDecisionsStub.IsAccountLatencyDegradedAsync(ctx, account, scope)
}

// w1vErrReader 读取即返回注入错误的请求体。
type w1vErrReader struct {
	err error
}

func (r *w1vErrReader) Read([]byte) (int, error) { return 0, r.err }

// w1vWriteErrorWriter 是首次 Write 即报错并放行一次同步信号的
// ResponseWriter+Flusher（驱动 chatAttachStreamHandler writeEvent 写失败臂）。
type w1vWriteErrorWriter struct {
	header http.Header
	code   int
	first  chan struct{}
	once   bool
}

func (w *w1vWriteErrorWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *w1vWriteErrorWriter) WriteHeader(code int) { w.code = code }

func (w *w1vWriteErrorWriter) Write(p []byte) (int, error) {
	if !w.once {
		w.once = true
		close(w.first)
	}
	return 0, errors.New("w1v: 下游写入失败")
}

func (w *w1vWriteErrorWriter) Flush() {}

// w1vSignalWriter 首次 Write 成功落盘并放行同步信号，其后写入交给 recorder
// （驱动 marshal 失败臂：先让订阅快照落地，再注入不可编码数据）。
type w1vSignalWriter struct {
	inner http.ResponseWriter
	first chan struct{}
	once  bool
}

func (w *w1vSignalWriter) Header() http.Header { return w.inner.Header() }

func (w *w1vSignalWriter) WriteHeader(code int) { w.inner.WriteHeader(code) }

func (w *w1vSignalWriter) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	if !w.once {
		w.once = true
		close(w.first)
	}
	return n, err
}

func (w *w1vSignalWriter) Flush() {
	if flusher, ok := w.inner.(http.Flusher); ok {
		flusher.Flush()
	}
}

// w1vEncrypt 用 fixture 同款密钥加密账户凭据。
func w1vEncrypt(t *testing.T, credentials map[string]any) string {
	t.Helper()
	sealed, err := accounts.EncryptJSON("chain-test-secret", credentials)
	if err != nil {
		t.Fatalf("encrypt credentials: %v", err)
	}
	return sealed
}

// w1vServeV1 直接调用 chain.ServeHTTP 投递一次 POST /v1/chat/completions。
func w1vServeV1(t *testing.T, chain *gatewayChain, apiKeySecret, body string, mutate func(*http.Request)) (int, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if apiKeySecret != "" {
		request.Header.Set("Authorization", "Bearer "+apiKeySecret)
	}
	if mutate != nil {
		mutate(request)
	}
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// w1vComposeChain 组装真实链（内存驱动 G13/G11 + sqlite fixture），并补齐
// high_concurrency 分组派发需要的 client-IP 并发槽族（composeSystemAPI 装配、
// chainSmokeDeps 缺省不带的端口）。
func w1vComposeChain(t *testing.T, fixture *chainFixture) (*gatewayChain, func()) {
	t.Helper()
	deps := chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, filepath.Join(t.TempDir(), "spool"))
	slots, err := gatewayclientip.NewClientIPConcurrency(gatewayclientip.ClientIPConcurrencyOptions{Clock: gatewaypreauth.SystemClock{}})
	if err != nil {
		t.Fatalf("construct client-ip concurrency: %v", err)
	}
	t.Cleanup(slots.Close)
	deps.ClientIPSlots = newChainClientIPConcurrency(slots)
	chain, shutdown, err := composeGatewayChain(deps)
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	return chain, shutdown
}

// w1vSeedPlainGroup 追加一个分组 + 一个账户（gpt-test；baseURL 指向上游）。
func w1vSeedPlainGroup(t *testing.T, fixture *chainFixture, groupID, baseURL string) {
	t.Helper()
	now := "2026-09-14T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES (?, ?, 'openai', 1, 'personal')`,
		groupID, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, credentials_encrypted, deleted_at, health_check_model
		) VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', 'W1V 账户', 'api_key', 'active', 1, 4, ?, NULL, 'gpt-test')`,
		"acc_"+groupID, fixture.systemAccount, w1vEncrypt(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": baseURL}))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
		groupID, fixture.systemAccount, "acc_"+groupID, now)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES (?, 'openai', 'gpt-test', ?)`,
		"acc_"+groupID, now)
}

// w1vSeedMultiGroupKey 追加普通路由策略 + 多分组绑定 + API Key，返回 key 明文。
// withAccounts 决定哪些绑定分组实际建账户（未建账户的分组在预检/派发层为空）。
// baseURLs[index] 为该组账户的上游基地址（无账户时忽略）。
func w1vSeedMultiGroupKey(t *testing.T, fixture *chainFixture, groupIDs []string, withAccounts map[int]bool, baseURLs map[int]string) string {
	t.Helper()
	now := "2026-09-14T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	strategyID := "rs_w1v_multi"
	secret := "sk-w1v-multi-key"
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status) VALUES (?, ?, 'W1V 多组', 'normal', NULL, 'active')`,
		strategyID, fixture.systemAccount)
	for index, groupID := range groupIDs {
		seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES (?, ?, 'openai', 1, 'personal')`,
			groupID, fixture.systemAccount)
		if withAccounts[index] {
			accountID := "acc_" + groupID
			baseURL := baseURLs[index]
			seed(`INSERT INTO accounts (
					id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
					name, type, status, schedulable, concurrency_limit, credentials_encrypted, deleted_at, health_check_model
				) VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', 'W1V 账户', 'api_key', 'active', 1, 4, ?, NULL, 'gpt-test')`,
				accountID, fixture.systemAccount, w1vEncrypt(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": baseURL}))
			seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
				groupID, fixture.systemAccount, accountID, now)
			seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES (?, 'openai', 'gpt-test', ?)`,
				accountID, now)
		}
		seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
			VALUES (?, ?, ?, ?, ?, 1, 'active', ?)`,
			"rsg_"+groupID, strategyID, fixture.systemAccount, groupID, index+1, now)
	}
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES (?, ?, ?, 'W1V 多组 Key', ?, 'active', ?)`,
		"key_w1v_multi", fixture.systemAccount, strategyID, gatewayruntimecache.HashSecret(secret), now)
	return secret
}

// ---------------------------------------------------------------------------
// handleOpenAIGatewayRequest 早段臂（103 / 120 / 138 / 158 / 170）
// ---------------------------------------------------------------------------

func TestW1VChainOrchestratorEarlyExits(t *testing.T) {
	t.Run("non_protocol_without_compat_404", func(t *testing.T) {
		// composeGatewayChain 不装配 compat：非协议 /v1 路径落到 404 固定契约。
		fixture := newChainFixture(t)
		chain, shutdown := w1vComposeChain(t, fixture)
		defer shutdown()
		recorder := httptest.NewRecorder()
		chain.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/definitely-not-a-protocol", nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404", recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "资源不存在") {
			t.Fatalf("body=%q", recorder.Body.String())
		}
	})

	t.Run("pre_resolve_runtime_error", func(t *testing.T) {
		// 业务库已关闭 → 运行时解析报错 → 编排器错误出口。
		fixture := newChainFixture(t)
		chain, shutdown := w1vComposeChain(t, fixture)
		defer shutdown()
		if err := fixture.db.Close(); err != nil {
			t.Fatalf("close business db: %v", err)
		}
		status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"gpt-test","messages":[]}`, nil)
		if status != http.StatusInternalServerError && status != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", status, body)
		}
	})

	t.Run("content_length_rejected_before_body", func(t *testing.T) {
		// 声明 Content-Length 超过文本上限（默认 16MiB）→ 读取 body 前拒绝。
		fixture := newChainFixture(t)
		chain, shutdown := w1vComposeChain(t, fixture)
		defer shutdown()
		status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{}`, func(request *http.Request) {
			request.Header.Set("Content-Length", "999999999")
		})
		if status != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if !strings.Contains(body, "请求体过大") {
			t.Fatalf("body=%q", body)
		}
	})

	t.Run("raw_body_read_error", func(t *testing.T) {
		// body 读取中途报错 → parser 拒绝面写 400 并终止链。
		fixture := newChainFixture(t)
		chain, shutdown := w1vComposeChain(t, fixture)
		defer shutdown()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", io.NopCloser(&w1vErrReader{err: errors.New("w1v: 下游读取失败")}))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
		recorder := httptest.NewRecorder()
		chain.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("capture_rejected_body_nil", func(t *testing.T) {
		// in-flight 预算收窄到 8 字节：正常 body 在 Capture 阶段被拒绝
		//（写响应后返回 nil body）→ 编排器静默终止。
		fixture := newChainFixture(t)
		chain, shutdown := w1vComposeChain(t, fixture)
		defer shutdown()
		chain.bodyPipeline = gatewaybody.NewMiddleware(gatewaybody.Config{BodyInFlightMaxBytes: 8})
		status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"gpt-test","messages":[{"role":"user","content":"w1v 超预算请求体"}]}`, nil)
		if status == http.StatusOK {
			t.Fatalf("被拒绝的 body 不应放行: status=%d body=%s", status, body)
		}
	})
}

// ---------------------------------------------------------------------------
// speed-first body admission 三臂（146 / 150 / 153）
// ---------------------------------------------------------------------------

// w1vSeedSpeedFirstStack 种 speed_first 策略 + high_concurrency 分组 + 账户，
// 返回 key 明文。concurrencyLimit 决定 admission 容量（0 = 容量 0 → 429）。
func w1vSeedSpeedFirstStack(t *testing.T, fixture *chainFixture, suffix, schedulingPolicyJSON string, concurrencyLimit int, upstream *httptest.Server) string {
	t.Helper()
	now := "2026-09-14T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	groupID := "w1v_hc_" + suffix
	strategyID := "rs_w1v_speed_" + suffix
	accountID := "acc_w1v_speed_" + suffix
	secret := "sk-w1v-speed-" + suffix
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type, scheduling_policy_json) VALUES (?, ?, 'openai', 1, 'high_concurrency', ?)`,
		groupID, fixture.systemAccount, schedulingPolicyJSON)
	credentials := map[string]any{"api_key": "sk-upstream-account-key"}
	if upstream != nil {
		credentials["base_url"] = upstream.URL
	}
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, credentials_encrypted, deleted_at, health_check_model
		) VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', 'W1V 速度优先账户', 'api_key', 'active', 1, ?, ?, NULL, 'gpt-test')`,
		accountID, fixture.systemAccount, concurrencyLimit, w1vEncrypt(t, credentials))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
		groupID, fixture.systemAccount, accountID, now)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES (?, 'openai', 'gpt-test', ?)`, accountID, now)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status) VALUES (?, ?, 'W1V 速度优先', 'normal', '{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":20000,"speedFirstConfig":{"slowTriggerCount":3}}}', 'active')`,
		strategyID, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES (?, ?, ?, ?, 1, 1, 'active', ?)`, "rsg_"+strategyID, strategyID, fixture.systemAccount, groupID, now)
	seed(`INSERT INTO api_keys (id, system_account_id, route_strategy_id, name, key_hash, status, created_at)
		VALUES (?, ?, ?, 'W1V 速度优先 Key', ?, 'active', ?)`, "key_w1v_speed_"+suffix, fixture.systemAccount, strategyID, gatewayruntimecache.HashSecret(secret), now)
	return secret
}

func TestW1VChainSpeedFirstAdmissionArms(t *testing.T) {
	upstreamHits := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-w1v-admit","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"admitted"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	t.Run("invalid_policy_error_arm", func(t *testing.T) {
		// maxQueueSize 越界 → 队列策略校验失败 → admErr → 编排器 500。
		fixture := newChainFixture(t)
		chain, shutdown := w1vComposeChain(t, fixture)
		defer shutdown()
		secret := w1vSeedSpeedFirstStack(t, fixture, "bad", `{"maxQueueSize":-1}`, 1, upstream)
		status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
		if status != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if !strings.Contains(body, "服务器内部错误") {
			t.Fatalf("body=%q", body)
		}
	})

	t.Run("queue_rejected_429_handled_arm", func(t *testing.T) {
		// 容量 1 + maxQueueWaitMs=1ms：第一个请求经 admission 挂在上游
		//（active=1），第二个请求入队 1ms 即超时 → 429 Handled。
		fixture := newChainFixture(t)
		chain, shutdown := w1vComposeChain(t, fixture)
		defer shutdown()
		var enteredOnce sync.Once
		entered := make(chan struct{})
		releaseUpstream := make(chan struct{})
		hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			enteredOnce.Do(func() { close(entered) })
			<-releaseUpstream
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-w1v-hang","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"hang"},"finish_reason":"stop"}]}`))
		}))
		defer hang.Close()
		secret := w1vSeedSpeedFirstStack(t, fixture, "queue", `{"maxQueueWaitMs":1}`, 1, hang)
		firstDone := make(chan int, 1)
		go func() {
			status, _ := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
			firstDone <- status
		}()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("第一个请求未到达上游")
		}
		status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
		if status != http.StatusTooManyRequests {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if !strings.Contains(body, "当前分组繁忙") {
			t.Fatalf("body=%q", body)
		}
		close(releaseUpstream)
		if first := <-firstDone; first != http.StatusOK {
			t.Fatalf("第一个请求 status=%d want 200", first)
		}
	})

	t.Run("admitted_release_arm", func(t *testing.T) {
		// 容量充足 → admission 放行（Release 挂到 handler 退出，153 臂）。
		// 后续 preflight/dispatch 的结果不影响该臂覆盖。
		fixture := newChainFixture(t)
		chain, shutdown := w1vComposeChain(t, fixture)
		defer shutdown()
		secret := w1vSeedSpeedFirstStack(t, fixture, "open", `{}`, 2, upstream)
		status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
		if status != http.StatusOK && status != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", status, body)
		}
	})
}

// ---------------------------------------------------------------------------
// preflight 意外错误臂（216）与 resolveRouteAction 错误调用点（259）
// ---------------------------------------------------------------------------

func TestW1VChainPreflightUnexpectedError(t *testing.T) {
	fixture := newChainFixture(t)
	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()
	chain.preauth.Circuits = &w1vCircuitsStub{inner: chain.preauth.Circuits, inspectErr: errors.New("w1v: 客户端电路检查失败")}
	status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"gpt-test","messages":[]}`, nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(body, "服务器内部错误") {
		t.Fatalf("body=%q", body)
	}
}

func TestW1VChainResolveRouteActionFallbackErrorAtCallSite(t *testing.T) {
	// 首组候选被排空（FilterCandidates 空窗口 → RouteAction）→ 回退候选解析
	// 报错 → resolveRouteAction 错误传播到编排器错误出口（routes.ts:518-520）。
	fixture := newChainFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()
	secret := w1vSeedMultiGroupKey(t, fixture, []string{"w1v_group_a", "w1v_group_b"},
		map[int]bool{0: true, 1: true}, map[int]string{0: upstream.URL, 1: upstream.URL})
	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()
	chain.preauth.Candidates = &w1vChainCandidatePipeline{
		inner:       chain.preauth.Candidates,
		filterEmptyCalls: 1,
		resolve:     []w1vResolveStep{{err: errors.New("w1v: 回退候选解析失败")}},
	}
	status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(body, "服务器内部错误") {
		t.Fatalf("body=%q", body)
	}
}

// TestW1VChainResolveRouteActionFallbackAttemptedArms：编排器侧
// resolveRouteAction 的 fallback.Attempted 双臂——回退预检产出 DispatchContext
// → result=上下文续环（984-987）；回退预检渲染后返回空 → Attempted+空 →
// return nil,nil（991）。
func TestW1VChainResolveRouteActionFallbackAttemptedArms(t *testing.T) {
	goodUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-w1v-attempted","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"回退分组内容"},"finish_reason":"stop"}]}`))
	}))
	defer goodUpstream.Close()

	newCase := func(t *testing.T, resolve []w1vResolveStep, blockedGroupID string) (*gatewayChain, func(), string) {
		fixture := newChainFixture(t)
		secret := w1vSeedMultiGroupKey(t, fixture, []string{"w1v_group_a", "w1v_group_b"},
			map[int]bool{0: true}, map[int]string{0: ""})
		chain, shutdown := w1vComposeChain(t, fixture)
		chain.preauth.Candidates = &w1vChainCandidatePipeline{inner: chain.preauth.Candidates, filterEmptyCalls: 1, resolve: resolve}
		chain.preauth.Circuits = &w1vCircuitsStub{inner: chain.preauth.Circuits, blockedGroupID: blockedGroupID}
		return chain, shutdown, secret
	}

	t.Run("attempted_with_context_continues", func(t *testing.T) {
		// 首组候选排空 → RouteAction(A) → 回退候选 B（携带 gpt-test 账户窗口）
		// → 回退预检 DispatchContext → result=上下文续环（985-987）→ B 派发成功。
		chain, shutdown, secret := newCase(t, []w1vResolveStep{
			{found: true, groupID: "w1v_group_b", accountIDs: []string{"acc_w1v_b"}, baseURL: goodUpstream.URL},
		}, "")
		defer shutdown()
		status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
		if status != http.StatusOK {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if !strings.Contains(body, "回退分组内容") {
			t.Fatalf("body=%q", body)
		}
	})

	t.Run("attempted_with_empty_context_settles", func(t *testing.T) {
		// 回退分组预检被客户端电路拒绝（渲染 429）→ Attempted+空 →
		// return nil,nil（991）→ 编排器 preflight.rejected 终态。
		chain, shutdown, secret := newCase(t, []w1vResolveStep{
			{found: true, groupID: "w1v_group_b"},
		}, "w1v_group_b")
		defer shutdown()
		status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
		if status != http.StatusTooManyRequests {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if !strings.Contains(body, "当前来源短时间错误过多") {
			t.Fatalf("body=%q", body)
		}
	})
}

// TestW1VResetSpeedFirstStateReleasesPendingReservation：组切换重置把未消费的
// 切号预留确定性释放（1149-1152）。
func TestW1VResetSpeedFirstStateReleasesPendingReservation(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	reservation := w1vReserveCutoverReservation(t, "acc_target")
	loop.speedFirstCutoverReservation = reservation

	loop.resetSpeedFirstState()
	if loop.speedFirstCutoverReservation != nil {
		t.Fatal("重置后预留应清空")
	}
	if loop.speedFirstByteRetryCount != 0 || loop.speedFirstRetryCandidateAccountIds != nil {
		t.Fatalf("计数/窗口未重置: %d %v", loop.speedFirstByteRetryCount, loop.speedFirstRetryCandidateAccountIds)
	}
}

// ---------------------------------------------------------------------------
// handleUpstreamResponse：commitState 缺省 + 中止信号错误出口（326 / 401 / 407 / 412）
// ---------------------------------------------------------------------------

func TestW1VHandleUpstreamResponseAbortsAndRenders503(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 响应处理前信号已中止
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	req := gatewaypreauth.NewGatewayRequest(request)
	res := gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())
	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account: gatewaydispatch.AccountCandidate{ID: "acc_w1v"},
		Response: &gatewaydispatch.GatewayUpstreamResponse{
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader(`{}`)),
		},
	}
	handling := loop.c.handleUpstreamResponse(req, res, loop.auditCapture, loop.current, dispatched, loop.startedAt,
		gatewayruntimecache.GatewaySettings{}, requestBudgets{}, nil)
	if handling.RetryUpstream || handling.ErrorCode != "" {
		t.Fatalf("中止出口必须返回零值结果: %+v", handling)
	}
	if res.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503（头未发送且未提交）", res.StatusCode())
	}
}

// ---------------------------------------------------------------------------
// run()：speed-first 收窄窗口（539-546）+ 空候选派发耗尽结算
// ---------------------------------------------------------------------------

func TestW1VDispatchLoopRunNarrowsSpeedFirstWindow(t *testing.T) {
	fixture := newChainFixture(t)
	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.c = chain // 真实引擎：候选窗口收窄后派发失败 → 耗尽结算
	w1vLoopBudgets(t, loop) // 引擎协调上下文需要真实预算组
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
	loop.speedFirstRetryCandidateAccountIds = map[string]struct{}{"acc_1": {}}
	loop.run(context.Background())
	if loop.res.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503（耗尽契约经真实 Responses 渲染）", loop.res.StatusCode())
	}
}

// ---------------------------------------------------------------------------
// settleResponseStreamServerRetry：可写流已结束（619）与回退错误（694）
// ---------------------------------------------------------------------------

func TestW1VSettleResponseStreamServerRetryEndedAndFallbackError(t *testing.T) {
	t.Run("writable_ended_settles", func(t *testing.T) {
		sink := &recordingFailureSink{}
		loop := newV1TestLoop(t, sink)
		loop.res.End()
		settled := loop.settleResponseStreamServerRetry(context.Background(),
			gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
			inspectionRetryHandling(true))
		if !settled {
			t.Fatal("可写流已结束必须结算")
		}
		if len(sink.inputs) != 0 {
			t.Fatalf("已结束的流不应再渲染: %+v", sink.inputs)
		}
	})

	t.Run("fallback_candidate_error_renders_unexpected", func(t *testing.T) {
		sink := &recordingFailureSink{}
		loop := newV1TestLoop(t, sink)
		loop.c.preauth.Candidates = &w1vChainCandidatePipeline{inner: chainCandidatePipelineNone(), resolve: []w1vResolveStep{{err: errors.New("w1v: 候选存储不可用")}}}
		loop.current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{
			GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
				{GroupID: "group_main"}, {GroupID: "group_fb"},
			},
		}
		// 携带路由计划快照：零值快照会让 canAttemptAPIKeyGroupFallback 恒不可回退。
		loop.current.RoutePlanSnapshot = gatewayrouting.RoutePlanSnapshot[string]{
			OrderedAllowedTargets: []string{"group_main", "group_fb"},
			Cursor:                0,
		}
		loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}}
		settled := loop.settleResponseStreamServerRetry(context.Background(),
			gatewaydispatch.UpstreamDispatchResult{Account: gatewaydispatch.AccountCandidate{ID: "acc_1"}},
			inspectionRetryHandling(true))
		if !settled {
			t.Fatal("回退错误必须结算")
		}
		if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("inputs=%+v", sink.inputs)
		}
	})
}

// chainCandidatePipelineNone 返回一个不可达兜底管道（脚本内错误已拦截）。
func chainCandidatePipelineNone() gatewaypreauth.CandidatePipeline {
	return &w1vCandidatePipelineStub{}
}

// ---------------------------------------------------------------------------
// settleDispatchError：cutover 续环 false（735）、已知错误中止（746）、
// agent guidance 原因（825）
// ---------------------------------------------------------------------------

func TestW1VSettleDispatchErrorCutoverFalseKnownErrorGuidance(t *testing.T) {
	t.Run("cutover_confirmed_returns_false", func(t *testing.T) {
		sink := &recordingFailureSink{}
		loop := newV1TestLoop(t, sink)
		loop.current.UsageContext.TrafficSource = gatewayTrafficSource
		loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(800, 3)
		loop.c.engine = &gatewaydispatch.Engine{Locks: &w1vLocksStub{}}
		reservation := w1vReserveCutoverReservation(t, "acc_target")
		view := speedFirstReservationViewOf(reservation)
		loop.recordAttachedSpeedFirstReservation(view, reservation)
		settled := loop.settleDispatchError(context.Background(), &gatewaydispatch.NormalRouteFirstByteCutoverError{
			AccountID:          "acc_slow",
			Message:            "首字截止已到",
			CutoverReservation: view,
		})
		if settled {
			t.Fatal("确认切换应继续循环（false）")
		}
		loop.releasePendingSpeedFirstReservation()
	})

	t.Run("aborted_context_known_error_settles", func(t *testing.T) {
		sink := &recordingFailureSink{}
		loop := newV1TestLoop(t, sink)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		settled := loop.settleDispatchError(ctx, errors.New("w1v: 派发中下游断开"))
		if !settled {
			t.Fatal("已中止信号的已知错误面必须结算")
		}
		if len(sink.inputs) != 0 {
			t.Fatalf("中止路径不应渲染失败响应: %+v", sink.inputs)
		}
	})

	t.Run("agent_guidance_reason_exhausted", func(t *testing.T) {
		sink := &recordingFailureSink{}
		loop := newV1TestLoop(t, sink)
		settled := loop.settleDispatchError(context.Background(), &gatewaydispatch.UpstreamAttemptError{
			Message:               "账户级代理指引耗尽",
			AgentGuidanceResponse: &gatewaypreauth.GatewayAgentGuidanceResponse{},
			FailedAccountIDs:      []string{"acc_1"},
		})
		if !settled {
			t.Fatal("agent guidance 耗尽必须结算")
		}
		if len(sink.inputs) != 1 || sink.inputs[0].StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("inputs=%+v", sink.inputs)
		}
	})
}

// ---------------------------------------------------------------------------
// 全链路 SSE：inspection 策略组内换号续环（593）与跨组回退切换（700）
// ---------------------------------------------------------------------------

// w1vSeedInspectionRetry 建命中 inspection 策略的错误 SSE 上游并替换
// fixture 主账户凭据；fallbackGroup 非空时另建回退分组并把 acc_good 挪入。
func w1vSeedInspectionRetry(t *testing.T, fixture *chainFixture, fallbackGroup string, goodUpstream *httptest.Server) {
	t.Helper()
	newInspectionRetryChain(t, fixture,
		"data: {\"error\":{\"code\":\"w1v_inspected_overloaded\",\"message\":\"上游过载\"}}\n\n",
		nil)
	seedInspectionRetryPolicy(t, fixture, "pol_w1v_retry", "w1v_inspected_overloaded")
	if fallbackGroup == "" {
		return
	}
	now := "2026-09-14T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES (?, ?, 'openai', 1, 'personal')`,
		fallbackGroup, fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_w1v_fb', 'rs_1', ?, ?, 2, 1, 'active', ?)`, fixture.systemAccount, fallbackGroup, now)
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, 'acc_good', 1, ?)`,
		fallbackGroup, fixture.systemAccount, now)
	seed(`DELETE FROM group_accounts WHERE group_id = ? AND account_id = 'acc_good'`, fixture.groupID)
}

func TestW1VResponseInspectionRetryRotatesWithinGroup(t *testing.T) {
	// 组内双账户：错误 SSE 命中策略 → 排除当前账户续环（593）→ 第二账户成功。
	fixture := newChainFixture(t)
	goodUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-w1v-rotate\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"换号续环内容\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
	}))
	defer goodUpstream.Close()
	w1vSeedInspectionRetry(t, fixture, "", goodUpstream)
	now := "2026-09-14T00:00:00.000Z"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, concurrency_limit, credentials_encrypted, deleted_at, health_check_model
		) VALUES ('acc_w1v_second', ?, 'openai', 'prof_1', 'openai', 'v1', '组内第二账户', 'api_key', 'active', 1, 4, ?, NULL, 'gpt-test')`,
		fixture.systemAccount, w1vEncrypt(t, map[string]any{"api_key": "sk-upstream-account-key", "base_url": goodUpstream.URL}))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, 'acc_w1v_second', 1, ?)`,
		fixture.groupID, fixture.systemAccount, now)
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, created_at) VALUES ('acc_w1v_second', 'openai', 'gpt-test', ?)`, now)

	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()
	server := httptest.NewServer(chain)
	defer server.Close()
	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200 after in-group rotation: %s", status, raw)
	}
	if !strings.Contains(raw, "换号续环内容") {
		t.Fatalf("rotated content missing: %s", raw)
	}
}

func TestW1VResponseInspectionRetrySwitchesToFallbackGroup(t *testing.T) {
	// 单账户组排除后无剩余候选（合并请求级耗尽集）→ switchToFallbackGroup
	// 切换（700: Switched → false 续环）→ 回退分组返回 200 流内容。
	fixture := newChainFixture(t)
	goodHits := 0
	goodUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		goodHits++
		t.Logf("w1v good upstream hit #%d path=%s", goodHits, r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-w1v-fbswitch\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"回退分组流内容\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
	}))
	defer goodUpstream.Close()
	w1vSeedInspectionRetry(t, fixture, "w1v_group_fb_stream", goodUpstream)
	shortenChainWaitBudgets(t, fixture)

	chain, shutdown := w1vComposeChain(t, fixture)
	defer shutdown()
	// 回退候选走脚本桩（acc_good 随候选窗口移交）：本测试的目标是
	// settleResponseStreamServerRetry 剩余候选为空后的 Switched 续环臂
	//（chain_v1.go:700），真实管道的回退解析由既有的 dead-upstream 全链路
	// 回归覆盖。
	chain.preauth.Candidates = &w1vChainCandidatePipeline{inner: chain.preauth.Candidates, resolve: []w1vResolveStep{
		{found: true, groupID: "w1v_group_fb_stream", accountIDs: []string{"acc_good"}, baseURL: goodUpstream.URL},
	}}
	server := httptest.NewServer(chain)
	defer server.Close()
	status, raw := chainV1ChatRequest(t, server.URL, fixture.apiKeySecret,
		`{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"你好"}]}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d want 200 after fallback switch: %s", status, raw)
	}
	if !strings.Contains(raw, "回退分组流内容") {
		t.Fatalf("fallback stream content missing: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// switchToFallbackGroup 续环臂（833 / 1101 / 1105 / 1108 / 1114）
// ---------------------------------------------------------------------------

func TestW1VSwitchToFallbackGroupContinuations(t *testing.T) {
	deadUpstream := newDeadUpstreamAddr(t)

	newCase := func(t *testing.T, resolve []w1vResolveStep, blockedGroupID string, secondGroupHasAccount bool) (*gatewayChain, func(), string) {
		fixture := newChainFixture(t)
		secret := w1vSeedMultiGroupKey(t, fixture, []string{"w1v_group_a", "w1v_group_b"},
			map[int]bool{0: true, 1: secondGroupHasAccount}, map[int]string{0: deadUpstream, 1: ""})
		chain, shutdown := w1vComposeChain(t, fixture)
		chain.preauth.Candidates = &w1vChainCandidatePipeline{inner: chain.preauth.Candidates, resolve: resolve}
		chain.preauth.Circuits = &w1vCircuitsStub{inner: chain.preauth.Circuits, blockedGroupID: blockedGroupID}
		shortenChainWaitBudgets(t, fixture)
		return chain, shutdown, secret
	}

	t.Run("fallback_preflight_renders_completed", func(t *testing.T) {
		// 回退分组预检被客户端电路拒绝（渲染 429）→ Attempted+空 →
		// switchToFallbackGroup Completed（1101）→ attempt 臂结算（833）。
		chain, shutdown, secret := newCase(t, []w1vResolveStep{{found: true, groupID: "w1v_group_b"}}, "w1v_group_b", true)
		defer shutdown()
		status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
		if status != http.StatusTooManyRequests {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if !strings.Contains(body, "当前来源短时间错误过多") {
			t.Fatalf("body=%q", body)
		}
	})

	t.Run("fallback_route_action_finalized_completed", func(t *testing.T) {
		// 回退分组为空 → 预检 RouteAction → resolveRouteAction 二次回退
		// Attempted=false → finalize 渲染 → next==nil → Completed（1108）。
		chain, shutdown, secret := newCase(t, []w1vResolveStep{
			{found: true, groupID: "w1v_group_b"},
			{found: false},
		}, "", false)
		defer shutdown()
		status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", status, body)
		}
		// finalizeRouteAction 渲染回退 RouteAction 的 Failure 原文
		//（空分组候选 = no_available_upstream_account / 没有可用的上游账户）。
		if !strings.Contains(body, "没有可用的上游账户") {
			t.Fatalf("body=%q", body)
		}
	})

	t.Run("fallback_route_action_resolve_error", func(t *testing.T) {
		// 回退预检的候选窗口被模型门排空（wrongModel）→ RouteAction(B) →
		// resolveRouteAction 二次候选解析报错 → switchToFallbackGroup 原样
		// 上抛（1105）→ 顶层 503 意外契约。
		chain, shutdown, secret := newCase(t, []w1vResolveStep{
			{found: true, groupID: "w1v_group_b", accountIDs: []string{"acc_w1v_ghost_b"}, wrongModel: true},
			{err: errors.New("w1v: 二次候选解析失败")},
		}, "", false)
		defer shutdown()
		status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if !strings.Contains(body, "上游暂时不可用，请重试") {
			t.Fatalf("body=%q", body)
		}
	})

	t.Run("repeated_entered_group_skips_switch", func(t *testing.T) {
		// 回退候选又指向已进入的首组（携带真实账户窗口 → DispatchContext）→
		// next 分组命中 enteredGroups → v1FallbackNone（1114）→ 耗尽出口渲染。
		chain, shutdown, secret := newCase(t, []w1vResolveStep{
			{found: true, groupID: "w1v_group_a", accountIDs: []string{"acc_w1v_group_a"}},
		}, "", true)
		defer shutdown()
		status, body := w1vServeV1(t, chain, secret, `{"model":"gpt-test","messages":[]}`, nil)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if !strings.Contains(body, "上游暂时不可用，请重试") {
			t.Fatalf("body=%q", body)
		}
	})
}

// ---------------------------------------------------------------------------
// settleSpeedFirstCutoverError 的回退臂（1215 / 1218 / 1220）
// ---------------------------------------------------------------------------

func TestW1VSettleSpeedFirstCutoverFallbackArms(t *testing.T) {
	newLoop := func(t *testing.T, resolve []w1vResolveStep, blockedGroupID string) (*v1DispatchLoop, *recordingFailureSink, func()) {
		fixture := newChainFixture(t)
		w1vSeedPlainGroup(t, fixture, "w1v_group_fb", newDeadUpstreamAddr(t))
		chain, shutdown := w1vComposeChain(t, fixture)
		chain.preauth.Candidates = &w1vChainCandidatePipeline{inner: chain.preauth.Candidates, resolve: resolve}
		chain.preauth.Circuits = &w1vCircuitsStub{inner: chain.preauth.Circuits, blockedGroupID: blockedGroupID}
		sink := &recordingFailureSink{}
		loop := newV1TestLoop(t, sink)
		loop.c = chain
		loop.current.UsageContext.TrafficSource = gatewayTrafficSource
		loop.current.UsageContext.SystemAccountID = fixture.systemAccount
		loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(800, 3)
		// 真实流程里 DispatchContext 携带路由计划快照；手写 loop 必须补齐，
		// 否则 canAttemptAPIKeyGroupFallback 对零值快照恒返回不可回退。
		loop.current.RoutePlanSnapshot = gatewayrouting.RoutePlanSnapshot[string]{
			OrderedAllowedTargets: []string{"group_main", "w1v_group_fb"},
			Cursor:                0,
		}
		loop.current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{
			GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
				{GroupID: "group_main"}, {GroupID: "w1v_group_fb"},
			},
		}
		// 回退预检的模型能力门需要请求携带 model（RequestModel 读已解析的
		// body state；手写 loop 未经 body 管道，直接装配）。
		modeledRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"w1v"}]}`))
		modeledRequest.Header.Set("Content-Type", "application/json")
		loop.req = gatewaypreauth.NewGatewayRequest(modeledRequest)
		model := "gpt-test"
		loop.req.Body = &gatewaybody.Request{
			RawBody:           []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"w1v"}]}`),
			ContentTypeHeader: "application/json",
			State:             &gatewaybody.BodyState{Model: &model},
		}
		loop.releases = &clientIPSlotReleaseList{} // switchToFallbackGroup 追加释放闭包
		return loop, sink, shutdown
	}
	cutover := func() *gatewaydispatch.NormalRouteFirstByteCutoverError {
		return &gatewaydispatch.NormalRouteFirstByteCutoverError{
			AccountID: "acc_slow",
			Message:   "首字截止已到",
		}
	}

	t.Run("fallback_error_propagates", func(t *testing.T) {
		loop, sink, shutdown := newLoop(t, []w1vResolveStep{{err: errors.New("w1v: 候选存储故障")}}, "")
		defer shutdown()
		if settled := loop.settleSpeedFirstCutoverError(context.Background(), cutover()); !settled {
			t.Fatal("回退错误必须结算")
		}
		// loop.c 是组合链：渲染走真实 Responses 写到 loop.res。
		if loop.res.StatusCode() != http.StatusServiceUnavailable {
			t.Fatalf("res status=%d body=%q", loop.res.StatusCode(), "")
		}
		if len(sink.inputs) != 0 {
			t.Fatalf("组合链下 sink 不应捕获: %+v", sink.inputs)
		}
	})

	t.Run("fallback_completed_via_blocked_preflight", func(t *testing.T) {
		loop, sink, shutdown := newLoop(t, []w1vResolveStep{{found: true, groupID: "w1v_group_fb"}}, "w1v_group_fb")
		defer shutdown()
		if settled := loop.settleSpeedFirstCutoverError(context.Background(), cutover()); !settled {
			t.Fatal("回退预检已渲染（Completed）必须结算")
		}
		if loop.res.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("res status=%d want 429（回退预检渲染）", loop.res.StatusCode())
		}
		if len(sink.inputs) != 0 {
			t.Fatalf("Completed 由回退预检渲染，sink 不应重复: %+v", sink.inputs)
		}
	})

	t.Run("fallback_switched_continues_loop", func(t *testing.T) {
		loop, sink, shutdown := newLoop(t, []w1vResolveStep{
			{found: true, groupID: "w1v_group_fb", accountIDs: []string{"acc_w1v_group_fb"}},
		}, "")
		defer shutdown()
		settled := loop.settleSpeedFirstCutoverError(context.Background(), cutover())
		if settled {
			t.Fatalf("Switched 应返回 false 续环（res status=%d）", loop.res.StatusCode())
		}
		if len(sink.inputs) != 0 {
			t.Fatalf("续环路径不应渲染: %+v", sink.inputs)
		}
		if loop.fallbackSwitches != 1 || loop.current.UsageContext.GroupID != "w1v_group_fb" {
			t.Fatalf("switches=%d current=%s", loop.fallbackSwitches, loop.current.UsageContext.GroupID)
		}
	})
}

// ---------------------------------------------------------------------------
// speed-first 决策闭包成功返回（1419）、排除集拷贝（1458）、
// 候选评估错误（1463）、预留获取错误（1488）
// ---------------------------------------------------------------------------

func TestW1VCutoverDecisionClosureReturnAndErrorArms(t *testing.T) {
	account := gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "慢账户"}
	deadline := gatewayrouting.NormalRouteAttemptFirstByteDeadline{EffectiveDeadlineMs: 800, LimitingFactor: gatewayrouting.FirstByteLimitingFactorConfigured}

	newCase := func(t *testing.T) (*v1DispatchLoop, *recordingMetadataCapture, *w1vScriptedDecisions) {
		sink := &recordingFailureSink{}
		capture := &recordingMetadataCapture{}
		stub := &w1vScriptedDecisions{w1vDecisionsStub: &w1vDecisionsStub{}}
		loop, _ := w1vLoopWithDecisions(t, sink, capture, stub)
		loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(800, 3)
		loop.current.Accounts = []gatewaydispatch.AccountCandidate{{ID: "acc_1"}, {ID: "acc_2"}}
		return loop, capture, stub
	}

	t.Run("closure_returns_decision_action", func(t *testing.T) {
		// 决策成功（未降级 → Continue）→ 闭包原样返回 action（1419）。
		loop, _, _ := newCase(t)
		stub := &w1vScriptedDecisions{w1vDecisionsStub: &w1vDecisionsStub{slowResult: &gatewayproxyhealth.LatencySlowResult{SlowCount: 1}}}
		loop.c.engine = &gatewaydispatch.Engine{Latency: stub}
		loop.current.NormalRouteSpeedFirstConfig = w1vSpeedFirstConfig(800, 3)
		closure := loop.onNormalRouteFirstByteDeadline(context.Background(), loop.current)
		if got := closure(gatewaydispatch.FirstByteDeadlineDecisionInput{}, account, deadline, &gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}); got != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("action=%q want continue", got)
		}
	})

	t.Run("excluded_set_copied_into_next_window", func(t *testing.T) {
		// streamRetryExcludedAccounts 非空 → 拷贝循环执行（1458）。
		loop, capture, stub := newCase(t)
		loop.streamRetryExcludedAccounts = map[string]struct{}{"acc_9": {}}
		stub.slowResult = &gatewayproxyhealth.LatencySlowResult{SlowCount: 1}
		if _, err := loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
			&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, loop.speedFirstLatencyScopeOf(loop.current), loop.current.NormalRouteSpeedFirstConfig); err != nil {
			t.Fatalf("decision err=%v", err)
		}
		metadata := capture.byLabel("normal_route_speed_first_slow_observed")
		if metadata == nil || metadata["retryBlockedReason"] != "slow_observation_not_degraded" {
			t.Fatalf("metadata=%+v", metadata)
		}
	})

	t.Run("eligible_accounts_error_propagates", func(t *testing.T) {
		// 第二次降级查询（候选窗口评估）报错 → 1463。
		loop, _, stub := newCase(t)
		stub.degradedErrAtCall = map[int]error{2: errors.New("w1v: 候选降级评估失败")}
		stub.slowResult = &gatewayproxyhealth.LatencySlowResult{SlowCount: 1, Degraded: true}
		if _, err := loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
			&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, loop.speedFirstLatencyScopeOf(loop.current), loop.current.NormalRouteSpeedFirstConfig); err == nil {
			t.Fatal("候选评估错误必须上抛")
		}
	})

	t.Run("reserve_cutover_error_propagates", func(t *testing.T) {
		// 前置条件满足但并发槽获取器报错 → 预留失败（1488）。
		loop, _, stub := newCase(t)
		loop.current.UsageContext.TrafficSource = gatewayTrafficSource
		loop.c.engine.Concurrency = &w1vConcurrencyStub{acquireErr: errors.New("w1v: 并发存储故障")}
		stub.degradedByAccount = map[string]bool{"acc_1": true}
		stub.slowResult = &gatewayproxyhealth.LatencySlowResult{SlowCount: 2, Degraded: true}
		if _, err := loop.speedFirstDeadlineDecision(context.Background(), loop.current, account, deadline,
			&gatewaydispatch.NormalRouteFirstByteAttemptCoordinator{}, stub, loop.speedFirstLatencyScopeOf(loop.current), loop.current.NormalRouteSpeedFirstConfig); err == nil {
			t.Fatal("预留错误必须上抛")
		}
	})
}

// ---------------------------------------------------------------------------
// confirmProtocolSuccessSideEffects（1791 / 1795 / 1804）
// ---------------------------------------------------------------------------

func TestW1VConfirmProtocolSuccessArms(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)

	// 协议未验证成功：直接返回（1791）。
	loop.confirmProtocolSuccessSideEffects(context.Background(), gatewaydispatch.UpstreamDispatchResult{}, gatewayresponse.UpstreamResponseHandlingResult{})

	// 两个结算闭包都报错：各写一条 warn 日志（1795 / 1804），不改写响应。
	loop.confirmProtocolSuccessSideEffects(context.Background(),
		gatewaydispatch.UpstreamDispatchResult{
			Account:                          gatewaydispatch.AccountCandidate{ID: "acc_w1v"},
			ConfirmSameAccountApiKeyFailures: func() error { return errors.New("w1v: 轮转失败结算未完成") },
			ConfirmAccountAPIKeySuccess:      func() error { return errors.New("w1v: 成功结算未完成") },
		},
		gatewayresponse.UpstreamResponseHandlingResult{ProtocolValidatedSuccess: true})
	if len(sink.inputs) != 0 {
		t.Fatalf("结算失败不应渲染响应: %+v", sink.inputs)
	}
}

// ---------------------------------------------------------------------------
// confirmClientIPAccountAvoidanceAfterFinalFailure 的确认失败臂（1959）
// ---------------------------------------------------------------------------

// w1vFailingStateStore 是恒报错的 RuntimeStateStore（驱动 redis 驱动的
// 回避状态确认失败臂）。
type w1vFailingStateStore struct{}

func (w1vFailingStateStore) GetJSON(context.Context, string, any) (bool, error) {
	return false, errors.New("w1v: 回避状态存储读取失败")
}

func (w1vFailingStateStore) SetJSON(context.Context, string, any, int64) error {
	return errors.New("w1v: 回避状态存储写入失败")
}

func (w1vFailingStateStore) Delete(context.Context, string) error {
	return errors.New("w1v: 回避状态存储删除失败")
}

func TestW1VClientIPAvoidanceConfirmError(t *testing.T) {
	avoidance, err := gatewayclientip.NewAvoidance(gatewayclientip.AvoidanceOptions{
		Clock:              gatewaypreauth.SystemClock{},
		RuntimeStateDriver: gatewayclientip.RuntimeStateDriverRedis,
		StateStore:         w1vFailingStateStore{},
		StateStoreClose:    func() {},
	})
	if err != nil {
		t.Fatalf("construct avoidance: %v", err)
	}
	t.Cleanup(avoidance.Close)
	tracker := avoidance.CreateAvoidanceTracker(gatewayclientip.AvoidanceScopeInput{
		SystemAccountID: "sys_w1v", GroupID: "group_w1v", APIKeyID: "key_w1v", ClientIP: "203.0.113.9",
	})
	avoidance.RememberPendingFailure(tracker, "acc_w1v", "W1V 账户", gatewayclientip.AccountFailure{ErrorPhase: "upstream_request"})

	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.c.preauth.AccountAvoidance = avoidance
	loop.current.ClientIPAccountAvoidance = tracker

	// 终态确认失败：只写 warn 日志，不改写终态渲染（1959-1969）。
	loop.confirmClientIPAccountAvoidanceAfterFinalFailure(context.Background(), loop.current, "w1v_gateway_failure_response")
	if len(sink.inputs) != 0 {
		t.Fatalf("确认失败不应渲染: %+v", sink.inputs)
	}
}

// ---------------------------------------------------------------------------
// finalizeRouteAction 的 Retry-After 下限臂（1999）
// ---------------------------------------------------------------------------

func TestW1VFinalizeRouteActionRetryAfterFloor(t *testing.T) {
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	retryAfter := int64(0) // (0+999)/1000 = 0 → 下限抬到 1 秒
	loop.finalizeRouteAction(&gatewaypreauth.RouteAction{
		UsageContext: loop.current.UsageContext,
		Failure: &gatewayrouting.GatewayRouteFinalFailure{
			StatusCode: http.StatusTooManyRequests, Message: "限流", ErrorType: "rate_limited",
			ErrorCode: "rate_limited", RetryAfterMs: &retryAfter,
		},
	})
	if got := loop.res.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q want 1", got)
	}
}

// ---------------------------------------------------------------------------
// chainResponseAuditCapture.FinalizeLazy 的启用转换体（2371-2390）
// ---------------------------------------------------------------------------

func TestW1VResponseAuditCaptureFinalizeLazyEnabled(t *testing.T) {
	capture := gatewayusage.NewAuditCaptureContext(gatewayusage.AuditCaptureInput{
		TraceID:       "trace_w1v_lazy",
		StartedAtMs:   1728000000000,
		TrafficSource: "gateway",
		Method:        http.MethodPost,
		Path:          "/v1/chat/completions",
		Settings: gatewayusage.FixedAuditLogSettingsSource{Settings: gatewayusage.AuditLogSettings{
			Enabled: true,
		}},
	})
	t.Cleanup(func() { capture.Cancel() })
	typed := chainResponseAuditCapture{capture: capture}
	// 启用状态下 FinalizeLazy 立即执行转换闭包并结算（无 dispatcher 也不 panic）。
	typed.FinalizeLazy(func() gatewaypreauth.AuditFinalizeInput {
		return gatewaypreauth.AuditFinalizeInput{
			Success:          false,
			Outcome:          gatewaypreauth.AuditOutcomeUpstreamFailed,
			StatusCode:       http.StatusServiceUnavailable,
			ResponseBody:     "w1v 失败响应体",
			ResponsePartType: string(gatewaypreauth.AuditPartGatewayError),
			ErrorPhase:       "dispatch",
			ErrorCode:        "upstream_retryable_error",
			ErrorMessage:     "上游失败",
		}
	})
}

// ---------------------------------------------------------------------------
// chatAttachStreamHandler：写失败（129 + 157）、marshal 失败（124 + 157）、
// 订阅者溢出关闭（148）
// ---------------------------------------------------------------------------

func TestW1VChatAttachStreamWriteAndMarshalFailures(t *testing.T) {
	t.Run("downstream_write_error", func(t *testing.T) {
		// 订阅快照的首次 Write 即失败 → writeEvent 写失败臂（129）→
		// 事件循环收束（157）。
		hub := chat.NewGenerationHub(func() string { return w1hNow })
		identity := chat.GenerationIdentity{OwnerID: "owner-w1v", ConversationID: "conv-w1v-werr", TurnID: "turn-w1v-werr"}
		runner, _, release := w1hLaunchBlockedRunner(t, hub, identity)
		writer := &w1vWriteErrorWriter{first: make(chan struct{})}
		done := make(chan bool, 1)
		go func() {
			done <- chatAttachStreamHandler(hub)(writer, httptest.NewRequest(http.MethodGet, "/attach", nil), identity)
		}()
		<-writer.first
		select {
		case ok := <-done:
			if !ok {
				t.Fatal("handler = false, want true")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("写失败后 handler 未收束")
		}
		close(release)
		<-runner.Completion()
	})

	t.Run("marshal_error", func(t *testing.T) {
		// 快照落地后注入 NaN（不可 JSON 编码）→ writeEvent marshal 失败
		//（124）→ 事件循环收束（157）。
		hub := chat.NewGenerationHub(func() string { return w1hNow })
		identity := chat.GenerationIdentity{OwnerID: "owner-w1v", ConversationID: "conv-w1v-marshal", TurnID: "turn-w1v-marshal"}
		runner, execCh, release := w1hLaunchBlockedRunner(t, hub, identity)
		recorder := httptest.NewRecorder()
		writer := &w1vSignalWriter{inner: recorder, first: make(chan struct{})}
		done := make(chan bool, 1)
		go func() {
			done <- chatAttachStreamHandler(hub)(writer, httptest.NewRequest(http.MethodGet, "/attach", nil), identity)
		}()
		exec := <-execCh
		<-writer.first // 订阅快照已写出，handler 阻塞在事件循环
		if !exec.Publish("message.delta", map[string]any{"delta": math.NaN()}, chat.ChatGenerationProjectionUpdate{}) {
			t.Fatal("publish NaN 事件失败")
		}
		select {
		case ok := <-done:
			if !ok {
				t.Fatal("handler = false, want true")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("marshal 失败后 handler 未收束")
		}
		close(release)
		<-runner.Completion()
	})
}

func TestW1VChatAttachStreamDroppedSubscriberClosesEvents(t *testing.T) {
	// 首次写入阻塞期间灌 300 条非终结事件：缓冲 256 塞满 → TrySend 丢弃并
	// 关闭 events → 释放写入后 handler 处理完缓冲读到关闭态（148）→ 收束。
	hub := chat.NewGenerationHub(func() string { return w1hNow })
	identity := chat.GenerationIdentity{OwnerID: "owner-w1v", ConversationID: "conv-w1v-drop", TurnID: "turn-w1v-drop"}
	runner, execCh, release := w1hLaunchBlockedRunner(t, hub, identity)
	writer := &w1hBlockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan bool, 1)
	go func() {
		done <- chatAttachStreamHandler(hub)(writer, httptest.NewRequest(http.MethodGet, "/attach", nil), identity)
	}()
	exec := <-execCh
	<-writer.entered
	for index := 0; index < 300; index++ {
		exec.Publish("message.delta", map[string]any{"index": index}, chat.ChatGenerationProjectionUpdate{})
	}
	close(writer.release)
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("handler = false, want true（通道关闭收束）")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("通道关闭后 handler 未收束")
	}
	body := writer.body()
	if !strings.Contains(body, "event: message.snapshot") {
		truncated := body
		if len(truncated) > 200 {
			truncated = truncated[:200]
		}
		t.Fatalf("快照事件应已写出: %q", truncated)
	}
	close(release)
	<-runner.Completion()
}
