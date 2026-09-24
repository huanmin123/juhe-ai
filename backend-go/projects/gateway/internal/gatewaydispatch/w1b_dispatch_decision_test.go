package gatewaydispatch

// W1b：网关调度决策可解释性测试——候选过滤器的 per-account 跳过明细、
// 决策摘要组装（JSON 形状 + 截断 + 确定可回放）、gateway_dispatch_candidates
// 审计标签经现有组装路径写入、决策观察槽事件。全部纯函数/内存 fake，
// 不连数据库。

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w1bCapabilityDriver 在 fakeDriver 之上叠加按账户 ID 的能力否定（Mock：
// 驱动端口只有布尔粒度，测试只需可控的真值表）。
type w1bCapabilityDriver struct {
	*fakeDriver
	unsupportedAccountIDs map[string]struct{}
}

func (d *w1bCapabilityDriver) AccountSupportsGatewayRequest(req *gatewaypreauth.GatewayRequest, account AccountCandidate, requestClientCompatibility string) bool {
	if _, unsupported := d.unsupportedAccountIDs[account.ID]; unsupported {
		return false
	}
	return d.fakeDriver.AccountSupportsGatewayRequest(req, account, requestClientCompatibility)
}

// (a) 混合候选（直接命中/映射命中/无 supportedModels/映射后模型被拒/模型
// 不匹配）→ Skipped 明细逐账户正确，计数语义不变。
func TestW1bModelFilterSkippedDetails(t *testing.T) {
	accounts := []AccountCandidate{
		{ID: "direct", SupportedModels: []string{"gpt-test"}},
		{ID: "mapping", SupportedModels: []string{"mapped-model"},
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel:            "gpt-test",
				SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
				UpstreamModel:          "mapped-model",
				UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
				Enabled:                true,
			}}},
		{ID: "no-constraint"},
		{ID: "mapping-blocked", SupportedModels: []string{"allowed-model"},
			ModelMappings: []gatewayruntimecache.AccountModelMapping{{
				SourceModel:            "gpt-test",
				SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
				UpstreamModel:          "blocked-model",
				UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
				Enabled:                true,
			}}},
		{ID: "mismatch", SupportedModels: []string{"other-model"}},
	}
	result := FilterGatewayAccountsByRequestedModel(accounts, "gpt-test", gatewayrouting.EndpointFamilyChatCompletions)
	if result.SkippedCount != 3 {
		t.Fatalf("skippedCount = %d", result.SkippedCount)
	}
	if result.DirectMatchedCount != 1 || result.MappingMatchedCount != 1 {
		t.Fatalf("matched counts = %d/%d", result.DirectMatchedCount, result.MappingMatchedCount)
	}
	want := []AccountSkip{
		{AccountID: "no-constraint", Reason: ModelSkipReasonUnsupportedConstraint},
		{AccountID: "mapping-blocked", Reason: ModelSkipReasonUnsupportedMapping},
		{AccountID: "mismatch", Reason: ModelSkipReasonModelNotMatched},
	}
	if !reflect.DeepEqual(result.Skipped, want) {
		t.Fatalf("skipped = %#v, want %#v", result.Skipped, want)
	}
}

// (a) 能力过滤：driver 端口布尔粒度下，被跳账户明细记 capability_mismatch。
func TestW1bCapabilityFilterSkippedDetails(t *testing.T) {
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	driver := &w1bCapabilityDriver{
		fakeDriver:            &fakeDriver{},
		unsupportedAccountIDs: map[string]struct{}{"skip-me": {}},
	}
	result := FilterGatewayAccountsByRequestCapability(req, testAccounts("keep-1", "skip-me", "keep-2"), driver, "", "")
	if result.SkippedCount != 1 || len(result.Accounts) != 2 {
		t.Fatalf("skipped/remaining = %d/%d", result.SkippedCount, len(result.Accounts))
	}
	want := []AccountSkip{{AccountID: "skip-me", Reason: CapabilitySkipReasonMismatch}}
	if !reflect.DeepEqual(result.Skipped, want) {
		t.Fatalf("skipped = %#v, want %#v", result.Skipped, want)
	}
}

// (b) 决策摘要 JSON 序列化 + 截断 + 确定可回放。
func TestW1bBuildDispatchDecisionSummaryJSONAndTruncation(t *testing.T) {
	rankByAccountID := map[string]int{
		"a-1": ModelPriorityRankDirect,
		"a-2": ModelPriorityRankDirect,
	}
	for index := 0; index < 25; index++ {
		rankByAccountID["s-"+intToStringTest(index)] = ModelPriorityRankUnsupported
	}
	input := DispatchDecisionSummaryInput{
		CandidateTotal: 27,
		Eligible:       testAccounts("a-1", "a-2"),
		ModelPriority:  &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: rankByAccountID},
		QuotaDenied:    []string{"q-1"},
		Busy:           []string{"z-9", "a-2"},
		Suppressed:     []string{"x-2", "x-1", "x-1"},
	}
	summary := BuildDispatchDecisionSummary(input)
	if summary.CandidateTotal != 27 || summary.EligibleCount != 2 || summary.SelectedAccountID != "a-1" {
		t.Fatalf("summary head = %d/%d/%q", summary.CandidateTotal, summary.EligibleCount, summary.SelectedAccountID)
	}
	if !summary.ModelRankAvailable {
		t.Fatal("modelRankAvailable must be true with a rank map")
	}
	if !summary.SkippedTruncated || len(summary.Skipped) != dispatchDecisionSummaryListCap {
		t.Fatalf("skipped truncated/len = %v/%d", summary.SkippedTruncated, len(summary.Skipped))
	}
	// skipped 顺序：配额否决在前、模型秩 unsupported 在后，各按 ID 稳定排序。
	if summary.Skipped[0].AccountID != "q-1" || summary.Skipped[0].Reason != DispatchSkipReasonQuotaDenied {
		t.Fatalf("skipped[0] = %#v", summary.Skipped[0])
	}
	if summary.Skipped[1].AccountID != "s-0" || summary.Skipped[1].Reason != DispatchSkipReasonModelUnsupported {
		t.Fatalf("skipped[1] = %#v", summary.Skipped[1])
	}
	// busy 保持输入顺序（并发快照扫描顺序），suppressed 去重 + 稳定排序。
	if !reflect.DeepEqual(summary.Busy, []string{"z-9", "a-2"}) {
		t.Fatalf("busy = %#v", summary.Busy)
	}
	if !reflect.DeepEqual(summary.Suppressed, []string{"x-1", "x-2"}) {
		t.Fatalf("suppressed = %#v", summary.Suppressed)
	}
	// 同一输入两次组装 JSON 完全一致（截断窗口可回放）。
	first, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	second, err := json.Marshal(BuildDispatchDecisionSummary(input))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("summary not deterministic:\n%s\n%s", first, second)
	}
	var decoded map[string]any
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["candidateTotal"].(float64) != 27 || decoded["eligibleCount"].(float64) != 2 {
		t.Fatalf("decoded head = %#v", decoded)
	}
	if decoded["selectedAccountId"] != "a-1" || decoded["modelRankAvailable"] != true {
		t.Fatalf("decoded selection = %#v/%#v", decoded["selectedAccountId"], decoded["modelRankAvailable"])
	}
	if len(decoded["skipped"].([]any)) != dispatchDecisionSummaryListCap || decoded["skippedTruncated"] != true {
		t.Fatalf("decoded skipped shape = %#v", decoded["skippedTruncated"])
	}
	// 无跳过/空列表：omitempty 键不出场（eligible 非空 → selectedAccountId 在场）。
	empty := BuildDispatchDecisionSummary(DispatchDecisionSummaryInput{Eligible: testAccounts("a-1")})
	encodedEmpty, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decodedEmpty map[string]any
	if err := json.Unmarshal(encodedEmpty, &decodedEmpty); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, absent := range []string{"skipped", "skippedTruncated", "busy", "suppressedAccountIds", "degradedAccountIds", "avoidedAccountIds"} {
		if _, present := decodedEmpty[absent]; present {
			t.Fatalf("empty summary must omit %s: %#v", absent, decodedEmpty)
		}
	}
	if decodedEmpty["selectedAccountId"] != "a-1" {
		t.Fatalf("decoded empty selection = %#v", decodedEmpty["selectedAccountId"])
	}
	if decodedEmpty["candidateTotal"].(float64) != 0 || decodedEmpty["eligibleCount"].(float64) != 1 {
		t.Fatalf("decoded empty head = %#v", decodedEmpty)
	}
}

// w1bRecordingAudit 记录标签与完整 metadata 值（frozenAudit 只记标签，
// 断言摘要值需要值级 fake；Mock 优先，不依赖真实审计实现）。
type w1bRecordingAudit struct {
	metadata map[string][]map[string]any
	order    []string
}

func (c *w1bRecordingAudit) BindContext(gatewaypreauth.AuditGatewayContext) {}

func (c *w1bRecordingAudit) AddGatewayMetadata(label string, m map[string]any) {
	if c.metadata == nil {
		c.metadata = map[string][]map[string]any{}
	}
	c.metadata[label] = append(c.metadata[label], m)
	c.order = append(c.order, label)
}

func (c *w1bRecordingAudit) Finalize(gatewaypreauth.AuditFinalizeInput) {}

func (c *w1bRecordingAudit) single(t *testing.T, label string) map[string]any {
	t.Helper()
	if len(c.metadata[label]) != 1 {
		t.Fatalf("label %s entries = %d", label, len(c.metadata[label]))
	}
	return c.metadata[label][0]
}

// (c) 决策摘要经现有准备路径写入 gateway_dispatch_candidates 审计标签。
func TestW1bPrepareDispatchAccountsEmitsGatewayDispatchCandidatesMetadata(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	engine.Quota = &fakeQuota{denied: map[string]struct{}{"a-2": {}}}
	capture := &w1bRecordingAudit{}
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	input := gatewaypreauth.DispatchPreparationInput{
		Req:               req,
		AuditCapture:      capture,
		UsageContext:      testUsageContext(),
		StartedAt:         1_000,
		CandidateAccounts: testAccounts("a-1", "a-2"),
		ModelPriority: &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{
			"a-1": ModelPriorityRankDirect, "a-2": ModelPriorityRankDirect,
		}},
		GroupAccess:        gatewayruntimecache.GroupUsageAccessMetadata{},
		SystemAccountID:    "system-1",
		APIKeyID:           "apikey-1",
		GroupID:            "group-1",
		SessionAffinityKey: "session-key",
		ClientStrategy:     gatewaypreauth.ClientStrategyContext{},
		RequestLane:        "text",
		ServerRetryBudget:  gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{}),
		RouteCoordinator:   &capturingCoordinator{},
		Signal:             context.Background(),
	}
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s (reason %q)", result.Outcome, result.Reason)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ID != "a-1" {
		t.Fatalf("eligible = %#v", accountIDs(result.Accounts))
	}
	metadata := capture.single(t, "gateway_dispatch_candidates")
	if metadata["candidateTotal"].(float64) != 2 {
		t.Fatalf("candidateTotal = %#v", metadata["candidateTotal"])
	}
	if metadata["eligibleCount"].(float64) != 1 {
		t.Fatalf("eligibleCount = %#v", metadata["eligibleCount"])
	}
	if metadata["selectedAccountId"] != "a-1" {
		t.Fatalf("selectedAccountId = %#v", metadata["selectedAccountId"])
	}
	if metadata["modelRankAvailable"] != true {
		t.Fatalf("modelRankAvailable = %#v", metadata["modelRankAvailable"])
	}
	skipped, ok := metadata["skipped"].([]any)
	if !ok || len(skipped) != 1 {
		t.Fatalf("skipped = %#v", metadata["skipped"])
	}
	entry := skipped[0].(map[string]any)
	if entry["id"] != "a-2" || entry["reason"] != DispatchSkipReasonQuotaDenied {
		t.Fatalf("skip entry = %#v", entry)
	}
	// 新标签是追加：既有标签照常在场。
	if len(capture.metadata["session_affinity_claim"]) != 1 {
		t.Fatalf("session_affinity_claim entries = %d", len(capture.metadata["session_affinity_claim"]))
	}
}

// 观察槽：ready 出口恰好发出一次决策事件，身份与摘要字段正确。
func TestW1bDispatchDecisionObserverReceivesReadyEvent(t *testing.T) {
	pipeline, _, _, _ := newPipeline(t)
	var received []DispatchDecisionEvent
	SetDispatchDecisionObserver(func(event DispatchDecisionEvent) {
		received = append(received, event)
	})
	t.Cleanup(func() { SetDispatchDecisionObserver(nil) })
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	input := gatewaypreauth.DispatchPreparationInput{
		Req:               req,
		AuditCapture:      &frozenAudit{sink: &fakeAuditSink{}},
		UsageContext:      testUsageContext(),
		StartedAt:         1_000,
		CandidateAccounts: testAccounts("a-1", "a-2"),
		ModelPriority: &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{
			"a-1": ModelPriorityRankDirect, "a-2": ModelPriorityRankDirect,
		}},
		GroupAccess:       gatewayruntimecache.GroupUsageAccessMetadata{},
		SystemAccountID:   "system-1",
		APIKeyID:          "apikey-1",
		GroupID:           "group-1",
		ClientStrategy:    gatewaypreauth.ClientStrategyContext{},
		RequestLane:       "text",
		ServerRetryBudget: gatewaypreauth.NewServerRetryBudget(5_000, gatewaypreauth.SystemClock{}),
		RouteCoordinator:  &capturingCoordinator{},
		Signal:            context.Background(),
	}
	result, err := pipeline.PrepareDispatchAccounts(context.Background(), input)
	if err != nil {
		t.Fatalf("PrepareDispatchAccounts: %v", err)
	}
	if result.Outcome != gatewaypreauth.CandidateOutcomeAccounts {
		t.Fatalf("outcome = %s", result.Outcome)
	}
	if len(received) != 1 {
		t.Fatalf("events = %d", len(received))
	}
	event := received[0]
	if event.TraceID != "trace-test" || event.GroupID != "group-1" || event.APIKeyID != "apikey-1" {
		t.Fatalf("identity = %q/%q/%q", event.TraceID, event.GroupID, event.APIKeyID)
	}
	if event.TrafficSource != "gateway" || event.DurationMs < 0 {
		t.Fatalf("traffic/duration = %q/%d", event.TrafficSource, event.DurationMs)
	}
	if event.Summary.EligibleCount != 2 || event.Summary.SelectedAccountID != "a-1" || event.Summary.CandidateTotal != 2 {
		t.Fatalf("summary = %#v", event.Summary)
	}
}

// w1bConcurrencyStub 提供可控的当前并发快照（只读）。
type w1bConcurrencyStub struct {
	current map[string]int
}

func (s *w1bConcurrencyStub) LoadCurrentAsync(ctx context.Context, accountIDs []string) (map[string]int, error) {
	return s.current, nil
}

func (s *w1bConcurrencyStub) LoadCurrentByLaneAsync(ctx context.Context, accountIDs []string, lane string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (s *w1bConcurrencyStub) TryAcquireAsync(ctx context.Context, accountID string, concurrencyLimit int, options AccountConcurrencyAcquireOptions) (ConcurrencySlot, error) {
	return ConcurrencySlot{Acquired: true, Limit: concurrencyLimit, Lane: options.Lane}, nil
}

// busy 明细带出：lane 容量排序同时给出排序前快照的 busy 候选 ID，且排序
// 行为与原函数逐位一致（busy 降位、稳定序）。
func TestW1bLaneCapacityOrderingBringsBusyIDs(t *testing.T) {
	accounts := testAccounts("b-1", "b-2")
	store := &w1bConcurrencyStub{current: map[string]int{
		gatewaySessionConcurrencyID(accounts[1]): 99, // limit 4 → busy
	}}
	ordered, busy, err := OrderGatewayAccountsByLaneCapacityAvailabilityWithBusyAsync(
		context.Background(), store, accounts, gatewayproto.LaneText, nil, nil)
	if err != nil {
		t.Fatalf("ordering: %v", err)
	}
	if len(busy) != 1 || busy[0] != "b-2" {
		t.Fatalf("busy = %#v", busy)
	}
	if ordered[0].ID != "b-1" || ordered[1].ID != "b-2" {
		t.Fatalf("ordered = %#v", accountIDs(ordered))
	}
	parity, err := OrderGatewayAccountsByLaneCapacityAvailabilityAsync(
		context.Background(), store, accounts, gatewayproto.LaneText, nil, nil)
	if err != nil {
		t.Fatalf("parity ordering: %v", err)
	}
	if !reflect.DeepEqual(accountIDs(parity), accountIDs(ordered)) {
		t.Fatalf("parity order = %#v, want %#v", accountIDs(parity), accountIDs(ordered))
	}
}

// TestW1bPreFilterSkippedInDecisionSummary 验证 W1b 续：预过滤层（能力/
// 模型）跳过明细进入决策摘要（计数 + 截断 + omitempty 形状），且不干扰
// 既有 skipped 列表的排序与截断语义。
func TestW1bPreFilterSkippedInDecisionSummary(t *testing.T) {
	input := DispatchDecisionSummaryInput{
		CandidateTotal: 3,
		Eligible:       testAccounts("a-1"),
		ModelPriority: &gatewayrouting.GatewayAccountModelPriority{RankByAccountID: map[string]int{
			"a-1": ModelPriorityRankDirect,
		}},
		PreFilterSkipped: []AccountSkip{
			{AccountID: "pre-1", Reason: CapabilitySkipReasonMismatch},
			{AccountID: "pre-2", Reason: ModelSkipReasonUnsupportedConstraint},
		},
		QuotaDenied: []string{"q-1"},
	}
	summary := BuildDispatchDecisionSummary(input)
	if summary.PreFilterSkippedCount != 2 {
		t.Fatalf("preFilterSkippedCount = %d", summary.PreFilterSkippedCount)
	}
	if len(summary.PreFilterSkipped) != 2 || summary.PreFilterSkipped[0].AccountID != "pre-1" ||
		summary.PreFilterSkipped[0].Reason != CapabilitySkipReasonMismatch ||
		summary.PreFilterSkipped[1].AccountID != "pre-2" {
		t.Fatalf("preFilterSkipped order = %#v", summary.PreFilterSkipped)
	}
	if summary.PreFilterSkippedTrunc {
		t.Fatal("preFilterSkipped must not truncate under the cap")
	}
	// 既有 skipped 语义不受影响（quota 在前 + 模型 unsupported 在后）。
	if len(summary.Skipped) != 1 || summary.Skipped[0].AccountID != "q-1" {
		t.Fatalf("skipped = %#v", summary.Skipped)
	}
	// JSON 形状：preFilterSkipped/preFilterSkippedCount 在场；键名契约。
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["preFilterSkippedCount"].(float64) != 2 {
		t.Fatalf("decoded count = %#v", decoded["preFilterSkippedCount"])
	}
	preFilter, ok := decoded["preFilterSkipped"].([]any)
	if !ok || len(preFilter) != 2 {
		t.Fatalf("decoded preFilterSkipped = %#v", decoded["preFilterSkipped"])
	}
	first := preFilter[0].(map[string]any)
	if first["id"] != "pre-1" || first["reason"] != CapabilitySkipReasonMismatch {
		t.Fatalf("decoded preFilterSkipped[0] = %#v", first)
	}
	// 空预过滤输入：三个键全部 omitempty 不出场。
	empty := BuildDispatchDecisionSummary(DispatchDecisionSummaryInput{Eligible: testAccounts("a-1")})
	encodedEmpty, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if strings.Contains(string(encodedEmpty), "preFilterSkipped") {
		t.Fatalf("empty preFilterSkipped keys must be omitted: %s", encodedEmpty)
	}
	// 截断：超过 cap 时 truncated 置位且保留前 cap 条。
	overflow := DispatchDecisionSummaryInput{Eligible: testAccounts("a-1")}
	for index := 0; index < dispatchDecisionSummaryListCap+5; index++ {
		overflow.PreFilterSkipped = append(overflow.PreFilterSkipped, AccountSkip{AccountID: "p-" + intToStringTest(index), Reason: CapabilitySkipReasonMismatch})
	}
	truncated := BuildDispatchDecisionSummary(overflow)
	if !truncated.PreFilterSkippedTrunc || len(truncated.PreFilterSkipped) != dispatchDecisionSummaryListCap || truncated.PreFilterSkippedCount != dispatchDecisionSummaryListCap+5 {
		t.Fatalf("truncated shape = %v/%d/%d", truncated.PreFilterSkippedTrunc, len(truncated.PreFilterSkipped), truncated.PreFilterSkippedCount)
	}
}
