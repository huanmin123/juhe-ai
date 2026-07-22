package managementmodelchecks

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestOptionsMatchesNodeAndFrontendContract(t *testing.T) {
	got := Options()
	wantModels := []string{
		"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4",
		"deepseek-v4-flash", "deepseek-v4-pro", "glm-5.2", "glm-5.1",
		"claude-opus-4-8", "claude-opus-4-7", "gemini-3.5-flash", "gemini-3.1-pro-preview",
	}
	if len(got.SupportedModels) != len(wantModels) {
		t.Fatalf("supported models = %d, want %d: %+v", len(got.SupportedModels), len(wantModels), got.SupportedModels)
	}
	for index, want := range wantModels {
		if got.SupportedModels[index].Value != want || got.SupportedModels[index].Label != want {
			t.Fatalf("supportedModels[%d] = %+v, want value/label %q", index, got.SupportedModels[index], want)
		}
	}
	if got.DefaultModel != "gpt-5.6-sol" || got.DefaultProfile != "full" {
		t.Fatalf("defaults = %q/%q", got.DefaultModel, got.DefaultProfile)
	}
	if !reflect.DeepEqual(got.SupportedProfiles, []Option{{
		Value:       "full",
		Label:       "强诊断完整检测",
		Description: "准确优先，不以成本和耗时为约束，执行多轮协议、行为指纹、长上下文、稳定性和可信对比探针",
	}}) {
		t.Fatalf("supported profiles = %+v", got.SupportedProfiles)
	}
	if got.TrustedComparison.EnabledByDefault || !got.TrustedComparison.Available || got.TrustedComparison.UnavailableReason != "" {
		t.Fatalf("trusted comparison = %+v", got.TrustedComparison)
	}
	wantMessage := "可信对比默认关闭；选择一个你信任的可用 OpenAI Responses / OpenAI Chat Completions / Anthropic Messages / Gemini native 协议账户后，会额外消耗该账户额度"
	if got.TrustedComparison.Message != wantMessage {
		t.Fatalf("trusted comparison message = %q", got.TrustedComparison.Message)
	}
	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"enabledByDefault":false`) {
		t.Fatalf("options JSON omitted explicit Node default: %s", payload)
	}
}

func TestServiceListNormalizesBoundedProgressivePageAndScope(t *testing.T) {
	reader := &modelCheckReaderStub{
		listResult: port.ManagementModelCheckRunListResult{
			Items:   []port.ManagementModelCheckRun{baseRunFact()},
			HasMore: true,
		},
	}
	service := NewService(reader)

	result, err := service.List(context.Background(), ListInput{
		SystemAccountID:            " sys_target ",
		IncludeSystemAccountFields: true,
		Page:                       999,
		PageSize:                   500,
		PageSizeProvided:           true,
		TargetType:                 " account ",
		TargetID:                   " acct_1 ",
		Model:                      " gpt-5.6-sol ",
		Level:                      " likely ",
		Status:                     " completed ",
		StartAt:                    " 2026-07-01T00:00:00.000Z ",
		EndAt:                      " 2026-07-22T00:00:00.000Z ",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantStoreInput := port.ManagementModelCheckRunListInput{
		SystemAccountID: "sys_target",
		TargetType:      "account",
		TargetID:        "acct_1",
		Model:           "gpt-5.6-sol",
		Level:           "likely",
		Status:          "completed",
		StartAt:         "2026-07-01T00:00:00.000Z",
		EndAt:           "2026-07-22T00:00:00.000Z",
		Limit:           100,
		Offset:          900,
	}
	if !reflect.DeepEqual(reader.listInput, wantStoreInput) {
		t.Fatalf("store input = %+v, want %+v", reader.listInput, wantStoreInput)
	}
	if result.Page != 10 || result.PageSize != 100 || !result.HasMore || result.Total != 902 {
		t.Fatalf("page result = %+v", result)
	}
	if len(result.Items) != 1 || result.Items[0].SystemAccountID != "sys_owner" || result.Items[0].ActorSystemAccountID != "sys_actor" || result.Items[0].TargetOwnerSystemAccountID != "sys_owner" {
		t.Fatalf("admin summary = %+v", result.Items)
	}
}

func TestServiceListDefaultsAndIgnoresInvalidFilters(t *testing.T) {
	reader := &modelCheckReaderStub{listResult: port.ManagementModelCheckRunListResult{Items: []port.ManagementModelCheckRun{baseRunFact()}}}
	service := NewService(reader)

	result, err := service.List(context.Background(), ListInput{
		TargetType: "group",
		Model:      "unknown-model",
		Level:      "other",
		Status:     "queued",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reader.listInput, port.ManagementModelCheckRunListInput{TargetType: "account", Limit: 20}) {
		t.Fatalf("store input = %+v", reader.listInput)
	}
	if result.Page != 1 || result.PageSize != 20 || result.Total != 1 || result.HasMore {
		t.Fatalf("page result = %+v", result)
	}
	item := result.Items[0]
	if item.SystemAccountID != "" || item.ActorSystemAccountID != "" || item.TargetOwnerSystemAccountID != "" {
		t.Fatalf("self summary leaked management fields: %+v", item)
	}
}

func TestServiceActiveUsesActorOwnedFactAndReturnsFrontendDTO(t *testing.T) {
	reader := &modelCheckReaderStub{active: baseRunFact(), activeFound: true}
	service := NewService(reader)

	result, found, err := service.Active(context.Background(), " sys_actor ")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if reader.activeActorID != "sys_actor" {
		t.Fatalf("active actor id = %q", reader.activeActorID)
	}
	if result.RunID != "mcr_1" || result.TraceID != "trace_1" || result.TargetID != "acct_1" || result.TargetName != "目标账户" || result.Model != "gpt-5.6-sol" || result.StartedAt != "2026-07-22T01:00:00.000Z" || result.StopRequested {
		t.Fatalf("active result = %+v", result)
	}
}

func TestServiceDetailMapsExactDTOAndSafeJSON(t *testing.T) {
	run := baseRunFact()
	run.RequestSummaryJSON = `{"prompt":"bounded"}`
	run.ResultSummaryJSON = `{broken`
	reader := &modelCheckReaderStub{
		detail: run,
		checks: []port.ManagementModelCheckItem{{
			ID: "mci_1", RunID: "mcr_1", ItemKey: "protocol", ItemType: "protocol",
			Status: "passed", Score: 10, MaxScore: 10, DurationMs: intPtr(25), TraceID: stringPtr("trace_item"),
			EvidenceSummaryJSON: `{"safe":true}`, CreatedAt: "2026-07-22T01:00:01.000Z", UpdatedAt: "2026-07-22T01:00:02.000Z",
		}},
		detailFound: true,
	}
	service := NewService(reader)

	detail, found, err := service.Detail(context.Background(), DetailInput{ID: " mcr_1 ", SystemAccountID: " sys_owner ", IncludeSystemAccountFields: true})
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if reader.detailID != "mcr_1" || reader.detailSystemAccountID != "sys_owner" {
		t.Fatalf("detail input = %q/%q", reader.detailID, reader.detailSystemAccountID)
	}
	if detail.RequestSummary["prompt"] != "bounded" || len(detail.ResultSummary) != 0 {
		t.Fatalf("summaries = request=%v result=%v", detail.RequestSummary, detail.ResultSummary)
	}
	if len(detail.Checks) != 1 || detail.Checks[0].EvidenceSummary["safe"] != true || detail.Checks[0].DurationMs == nil || *detail.Checks[0].DurationMs != 25 {
		t.Fatalf("checks = %+v", detail.Checks)
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) == "" {
		t.Fatal("empty detail JSON")
	}
}

func TestServiceDetailMergesLatestTrustReportWithoutScanningObservations(t *testing.T) {
	run := baseRunFact()
	run.AccountID = stringPtr("acct_1")
	run.ResultSummaryJSON = `{"trustReport":{"requestedModel":"gpt-5.6-sol","observedModel":"gpt-5.6-sol","reasonCodes":["protocol_ok"],"probeSetVersion":"run-probe"}}`
	reader := &modelCheckReaderStub{
		detail: run,
		checks: []port.ManagementModelCheckItem{},
		detailFound: true,
		trust: port.ManagementModelAccountTrustResult{
			IdentityStatus: "consistent", MappingStatus: "direct", UsageIntegrityStatus: "consistent",
			ProtocolStatus: "consistent", EvidenceStatus: "stable", EvidenceCoverage: 0.9,
			ObservationCount: 12, RoundCount: 3, IndependentSourceCount: 2, IdentityObservationCount: 4,
			PairedProbeCount: 2, Slope: floatPtr(1.02), Intercept: floatPtr(0.5), InterceptStrongGateEnabled: true,
			ProbeSetVersion: "trust-probe", ReasonCodes: []string{"token_slope_ok"}, LastObservedAt: stringPtr("2026-07-22T02:00:00.000Z"),
		},
		trustFound: true,
	}
	service := NewService(reader)

	detail, found, err := service.Detail(context.Background(), DetailInput{ID: "mcr_1", SystemAccountID: "sys_owner", IncludeSystemAccountFields: true})
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if reader.trustSystemAccountID != "sys_owner" || reader.trustAccountID != "acct_1" || reader.trustModel != "gpt-5.6-sol" {
		t.Fatalf("trust lookup = %q/%q/%q", reader.trustSystemAccountID, reader.trustAccountID, reader.trustModel)
	}
	report := detail.ResultSummary["trustReport"].(map[string]any)
	if report["requestedModel"] != "gpt-5.6-sol" || report["identityStatus"] != "consistent" || report["probeSetVersion"] != "trust-probe" {
		t.Fatalf("merged trust report = %#v", report)
	}
	reasonCodes, ok := report["reasonCodes"].([]string)
	if !ok || !reflect.DeepEqual(reasonCodes, []string{"token_slope_ok"}) {
		t.Fatalf("reason codes = %#v", report["reasonCodes"])
	}
}

func TestServiceDetailSkipsTrustMergeForUnavailableEvidence(t *testing.T) {
	run := baseRunFact()
	run.Level = "unavailable"
	run.ResultSummaryJSON = `{"trustReport":{"reasonCodes":["model_response_evidence_unavailable"]}}`
	reader := &modelCheckReaderStub{detail: run, detailFound: true, trustFound: true, trust: port.ManagementModelAccountTrustResult{IdentityStatus: "consistent"}}
	service := NewService(reader)

	detail, found, err := service.Detail(context.Background(), DetailInput{ID: "mcr_1", SystemAccountID: "sys_owner"})
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if reader.trustCalls != 0 {
		t.Fatalf("trust lookup should be skipped, calls=%d", reader.trustCalls)
	}
	report := detail.ResultSummary["trustReport"].(map[string]any)
	if report["identityStatus"] != nil {
		t.Fatalf("unavailable report was overwritten: %#v", report)
	}
}

func TestServicePropagatesReaderErrors(t *testing.T) {
	want := errors.New("database unavailable")
	service := NewService(&modelCheckReaderStub{err: want})
	if _, err := service.List(context.Background(), ListInput{}); !errors.Is(err, want) {
		t.Fatalf("list error = %v", err)
	}
	if _, _, err := service.Active(context.Background(), "sys_actor"); !errors.Is(err, want) {
		t.Fatalf("active error = %v", err)
	}
	if _, _, err := service.Detail(context.Background(), DetailInput{ID: "mcr_1"}); !errors.Is(err, want) {
		t.Fatalf("detail error = %v", err)
	}
}

func baseRunFact() port.ManagementModelCheckRun {
	return port.ManagementModelCheckRun{
		ID: "mcr_1", SystemAccountID: "sys_owner", ActorSystemAccountID: "sys_actor", ProviderCode: "gpt",
		TargetType: "account", TargetID: "acct_1", TargetName: stringPtr("目标账户"), TargetOwnerSystemAccountID: stringPtr("sys_owner"),
		AccountID: stringPtr("acct_1"), Model: "gpt-5.6-sol", Profile: "full", TrustedComparison: true,
		TrustedComparisonAvailable: true, Level: "likely", Score: 88, MaxScore: 100, Status: "completed",
		Message: "模型检测完成", TraceID: stringPtr("trace_1"), ProbeSetVersion: "multi-provider-model-check-v4-gpt56-preview",
		StartedAt: "2026-07-22T01:00:00.000Z", FinishedAt: stringPtr("2026-07-22T01:01:00.000Z"), DurationMs: intPtr(60_000),
		RequestSummaryJSON: `{}`, ResultSummaryJSON: `{}`, CreatedAt: "2026-07-22T01:00:00.000Z", UpdatedAt: "2026-07-22T01:01:00.000Z",
	}
}

type modelCheckReaderStub struct {
	active                port.ManagementModelCheckRun
	activeFound           bool
	listResult            port.ManagementModelCheckRunListResult
	detail                port.ManagementModelCheckRun
	checks                []port.ManagementModelCheckItem
	detailFound           bool
	err                   error
	activeActorID         string
	listInput             port.ManagementModelCheckRunListInput
	detailID              string
	detailSystemAccountID string
	trust                port.ManagementModelAccountTrustResult
	trustFound           bool
	trustCalls           int
	trustSystemAccountID string
	trustAccountID       string
	trustModel           string
}

func (s *modelCheckReaderStub) FindManagementModelCheckActive(_ context.Context, actorSystemAccountID string) (port.ManagementModelCheckRun, bool, error) {
	s.activeActorID = actorSystemAccountID
	return s.active, s.activeFound, s.err
}

func (s *modelCheckReaderStub) ListManagementModelCheckRuns(_ context.Context, input port.ManagementModelCheckRunListInput) (port.ManagementModelCheckRunListResult, error) {
	s.listInput = input
	return s.listResult, s.err
}

func (s *modelCheckReaderStub) GetManagementModelCheckRun(_ context.Context, id string, systemAccountID string) (port.ManagementModelCheckRun, []port.ManagementModelCheckItem, bool, error) {
	s.detailID = id
	s.detailSystemAccountID = systemAccountID
	return s.detail, s.checks, s.detailFound, s.err
}

func (s *modelCheckReaderStub) FindManagementModelAccountTrustResult(_ context.Context, systemAccountID string, accountID string, requestedModel string) (port.ManagementModelAccountTrustResult, bool, error) {
	s.trustCalls++
	s.trustSystemAccountID = systemAccountID
	s.trustAccountID = accountID
	s.trustModel = requestedModel
	return s.trust, s.trustFound, s.err
}

func stringPtr(value string) *string { return &value }
func intPtr(value int) *int          { return &value }
func floatPtr(value float64) *float64 { return &value }

var _ port.ManagementModelCheckReader = (*modelCheckReaderStub)(nil)
