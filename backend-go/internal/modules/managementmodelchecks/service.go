package managementmodelchecks

import (
	"context"
	"encoding/json"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	defaultPageSize   = 20
	maxPageSize       = 100
	maxListWindowRows = 1001
)

var supportedModels = []string{
	"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4",
	"deepseek-v4-flash", "deepseek-v4-pro", "glm-5.2", "glm-5.1",
	"claude-opus-4-8", "claude-opus-4-7", "gemini-3.5-flash", "gemini-3.1-pro-preview",
}

var supportedModelSet = func() map[string]struct{} {
	result := make(map[string]struct{}, len(supportedModels))
	for _, model := range supportedModels {
		result[model] = struct{}{}
	}
	return result
}()

type Service struct {
	reader port.ManagementModelCheckReader
}

type Option struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type TrustedComparisonOptions struct {
	EnabledByDefault  bool   `json:"enabledByDefault"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
	Message           string `json:"message,omitempty"`
}

type OptionsResult struct {
	SupportedModels   []Option                 `json:"supportedModels"`
	SupportedProfiles []Option                 `json:"supportedProfiles"`
	TrustedComparison TrustedComparisonOptions `json:"trustedComparison"`
	DefaultModel      string                   `json:"defaultModel"`
	DefaultProfile    string                   `json:"defaultProfile"`
}

type ListInput struct {
	SystemAccountID            string
	IncludeSystemAccountFields bool
	Page                       int
	PageSize                   int
	PageSizeProvided           bool
	TargetType                 string
	TargetID                   string
	Model                      string
	Level                      string
	Status                     string
	StartAt                    string
	EndAt                      string
}

type DetailInput struct {
	ID                         string
	SystemAccountID            string
	IncludeSystemAccountFields bool
}

type RunSummary struct {
	ID                         string         `json:"id"`
	SystemAccountID            string         `json:"systemAccountId,omitempty"`
	ActorSystemAccountID       string         `json:"actorSystemAccountId,omitempty"`
	ProviderCode               string         `json:"providerCode"`
	TargetType                 string         `json:"targetType"`
	TargetID                   string         `json:"targetId"`
	TargetName                 string         `json:"targetName,omitempty"`
	TargetOwnerSystemAccountID string         `json:"targetOwnerSystemAccountId,omitempty"`
	AccountID                  string         `json:"accountId,omitempty"`
	GroupID                    string         `json:"groupId,omitempty"`
	APIKeyID                   string         `json:"apiKeyId,omitempty"`
	Model                      string         `json:"model"`
	Profile                    string         `json:"profile"`
	TrustedComparison          bool           `json:"trustedComparison"`
	TrustedComparisonAvailable bool           `json:"trustedComparisonAvailable"`
	Level                      string         `json:"level"`
	Score                      int            `json:"score"`
	MaxScore                   int            `json:"maxScore"`
	Status                     string         `json:"status"`
	Message                    string         `json:"message"`
	TraceID                    string         `json:"traceId,omitempty"`
	ProbeSetVersion            string         `json:"probeSetVersion"`
	StartedAt                  string         `json:"startedAt"`
	FinishedAt                 string         `json:"finishedAt,omitempty"`
	DurationMs                 *int           `json:"durationMs,omitempty"`
	RequestSummary             map[string]any `json:"requestSummary,omitempty"`
	ResultSummary              map[string]any `json:"resultSummary,omitempty"`
	ErrorCode                  string         `json:"errorCode,omitempty"`
	ErrorMessage               string         `json:"errorMessage,omitempty"`
	CreatedAt                  string         `json:"createdAt"`
	UpdatedAt                  string         `json:"updatedAt"`
}

type CheckResult struct {
	ID              string         `json:"id"`
	RunID           string         `json:"runId"`
	ItemKey         string         `json:"itemKey"`
	ItemType        string         `json:"itemType"`
	Status          string         `json:"status"`
	Score           int            `json:"score"`
	MaxScore        int            `json:"maxScore"`
	DurationMs      *int           `json:"durationMs,omitempty"`
	TraceID         string         `json:"traceId,omitempty"`
	EvidenceSummary map[string]any `json:"evidenceSummary"`
	ErrorCode       string         `json:"errorCode,omitempty"`
	ErrorMessage    string         `json:"errorMessage,omitempty"`
	CreatedAt       string         `json:"createdAt"`
	UpdatedAt       string         `json:"updatedAt"`
}

type RunDetail struct {
	RunSummary
	RequestSummary map[string]any `json:"requestSummary"`
	ResultSummary  map[string]any `json:"resultSummary"`
	Checks         []CheckResult  `json:"checks"`
}

type ListResult struct {
	Items    []RunSummary `json:"items"`
	Page     int          `json:"page"`
	PageSize int          `json:"pageSize"`
	Total    int          `json:"total"`
	HasMore  bool         `json:"hasMore"`
}

type ActiveRunSummary struct {
	RunID         string `json:"runId,omitempty"`
	TraceID       string `json:"traceId,omitempty"`
	TargetID      string `json:"targetId,omitempty"`
	TargetName    string `json:"targetName,omitempty"`
	Model         string `json:"model,omitempty"`
	StartedAt     string `json:"startedAt"`
	StopRequested bool   `json:"stopRequested"`
}

func NewService(reader port.ManagementModelCheckReader) *Service {
	return &Service{reader: reader}
}

func Options() OptionsResult {
	models := make([]Option, 0, len(supportedModels))
	for _, model := range supportedModels {
		models = append(models, Option{Value: model, Label: model})
	}
	return OptionsResult{
		SupportedModels: models,
		SupportedProfiles: []Option{{
			Value:       "full",
			Label:       "强诊断完整检测",
			Description: "准确优先，不以成本和耗时为约束，执行多轮协议、行为指纹、长上下文、稳定性和可信对比探针",
		}},
		TrustedComparison: TrustedComparisonOptions{
			Available: true,
			Message:   "可信对比默认关闭；选择一个你信任的可用 OpenAI Responses / OpenAI Chat Completions / Anthropic Messages / Gemini native 协议账户后，会额外消耗该账户额度",
		},
		DefaultModel:   supportedModels[0],
		DefaultProfile: "full",
	}
}

func (s *Service) Active(ctx context.Context, actorSystemAccountID string) (ActiveRunSummary, bool, error) {
	run, found, err := s.reader.FindManagementModelCheckActive(ctx, trim(actorSystemAccountID))
	if err != nil || !found {
		return ActiveRunSummary{}, found, err
	}
	return ActiveRunSummary{
		RunID: run.ID, TraceID: text(run.TraceID), TargetID: run.TargetID, TargetName: text(run.TargetName),
		Model: run.Model, StartedAt: run.StartedAt, StopRequested: false,
	}, true, nil
}

func (s *Service) List(ctx context.Context, input ListInput) (ListResult, error) {
	pageSize := defaultPageSize
	if input.PageSizeProvided {
		pageSize = min(max(input.PageSize, 1), maxPageSize)
	}
	maxPage := max(1, (maxListWindowRows-1)/pageSize)
	page := min(max(input.Page, 1), maxPage)
	storeResult, err := s.reader.ListManagementModelCheckRuns(ctx, port.ManagementModelCheckRunListInput{
		SystemAccountID: trim(input.SystemAccountID),
		TargetType:      "account",
		TargetID:        trim(input.TargetID),
		Model:           normalizedModel(input.Model),
		Level:           normalizedLevel(input.Level),
		Status:          normalizedStatus(input.Status),
		StartAt:         trim(input.StartAt),
		EndAt:           trim(input.EndAt),
		Limit:           pageSize,
		Offset:          (page - 1) * pageSize,
	})
	if err != nil {
		return ListResult{}, err
	}
	items := make([]RunSummary, 0, len(storeResult.Items))
	for _, run := range storeResult.Items {
		items = append(items, runSummary(run, input.IncludeSystemAccountFields, false))
	}
	total := (page-1)*pageSize + len(items)
	if storeResult.HasMore {
		total++
	}
	return ListResult{Items: items, Page: page, PageSize: pageSize, Total: total, HasMore: storeResult.HasMore}, nil
}

func (s *Service) Detail(ctx context.Context, input DetailInput) (RunDetail, bool, error) {
	run, checks, found, err := s.reader.GetManagementModelCheckRun(ctx, trim(input.ID), trim(input.SystemAccountID))
	if err != nil || !found {
		return RunDetail{}, found, err
	}
	summary := runSummary(run, input.IncludeSystemAccountFields, false)
	result := RunDetail{
		RunSummary:     summary,
		RequestSummary: jsonObject(run.RequestSummaryJSON),
		ResultSummary:  jsonObject(run.ResultSummaryJSON),
		Checks:         make([]CheckResult, 0, len(checks)),
	}
	for _, check := range checks {
		result.Checks = append(result.Checks, CheckResult{
			ID: check.ID, RunID: check.RunID, ItemKey: check.ItemKey, ItemType: check.ItemType,
			Status: check.Status, Score: check.Score, MaxScore: check.MaxScore, DurationMs: check.DurationMs,
			TraceID: text(check.TraceID), EvidenceSummary: jsonObject(check.EvidenceSummaryJSON),
			ErrorCode: text(check.ErrorCode), ErrorMessage: text(check.ErrorMessage),
			CreatedAt: check.CreatedAt, UpdatedAt: check.UpdatedAt,
		})
	}
	if shouldMergeLatestTrustResult(result, run) {
		trust, trustFound, trustErr := s.reader.FindManagementModelAccountTrustResult(
			ctx,
			trustSystemAccountID(run, input.SystemAccountID),
			text(run.AccountID),
			run.Model,
		)
		if trustErr != nil {
			return RunDetail{}, false, trustErr
		}
		if trustFound {
			mergeLatestTrustResult(result.ResultSummary, run.Model, trust)
		}
	}
	return result, true, nil
}

func shouldMergeLatestTrustResult(detail RunDetail, run port.ManagementModelCheckRun) bool {
	if run.AccountID == nil || trim(*run.AccountID) == "" {
		return false
	}
	report, _ := detail.ResultSummary["trustReport"].(map[string]any)
	if containsString(report["reasonCodes"], "model_response_evidence_unavailable") {
		return false
	}
	if run.Level == "unavailable" && trim(anyString(report["observedModel"])) == "" {
		return false
	}
	return trustSystemAccountID(run, "") != ""
}

func trustSystemAccountID(run port.ManagementModelCheckRun, fallback string) string {
	if value := trim(run.SystemAccountID); value != "" {
		return value
	}
	return trim(fallback)
}

func mergeLatestTrustResult(resultSummary map[string]any, requestedModel string, trust port.ManagementModelAccountTrustResult) {
	report, _ := resultSummary["trustReport"].(map[string]any)
	if report == nil {
		report = map[string]any{}
	}
	latest := map[string]any{
		"identityStatus": trust.IdentityStatus, "mappingStatus": trust.MappingStatus,
		"usageIntegrityStatus": trust.UsageIntegrityStatus, "protocolStatus": trust.ProtocolStatus,
		"evidenceStatus": trust.EvidenceStatus, "evidenceCoverage": trust.EvidenceCoverage,
		"observationCount": trust.ObservationCount, "roundCount": trust.RoundCount,
		"independentSourceCount": trust.IndependentSourceCount, "identityObservationCount": trust.IdentityObservationCount,
		"pairedProbeCount": trust.PairedProbeCount, "interceptStrongGateEnabled": trust.InterceptStrongGateEnabled,
		"reasonCodes": append([]string(nil), trust.ReasonCodes...),
	}
	putOptional(latest, "slope", trust.Slope)
	putOptional(latest, "intercept", trust.Intercept)
	putOptional(latest, "interceptBaselineMedian", trust.InterceptBaselineMedian)
	putOptional(latest, "interceptBaselineMad", trust.InterceptBaselineMAD)
	putOptional(latest, "interceptBaselineVersion", trust.InterceptBaselineVersion)
	putOptional(latest, "interceptBaselineStatus", trust.InterceptBaselineStatus)
	putOptional(latest, "identityDistance", trust.IdentityDistance)
	putOptional(latest, "pairedDistance", trust.PairedDistance)
	putOptional(latest, "pairedBaselineMedian", trust.PairedBaselineMedian)
	putOptional(latest, "pairedBaselineMad", trust.PairedBaselineMAD)
	putOptional(latest, "baselineVersion", trust.BaselineVersion)
	putOptional(latest, "baselineVersionStatus", trust.BaselineVersionStatus)
	putOptional(latest, "featureVersion", trust.FeatureVersion)
	putOptional(latest, "tokenizerVersion", trust.TokenizerVersion)
	if trust.ProbeSetVersion != "" {
		latest["probeSetVersion"] = trust.ProbeSetVersion
	}
	putOptional(latest, "lastObservedAt", trust.LastObservedAt)
	for key, value := range latest {
		report[key] = value
	}
	if _, exists := report["requestedModel"]; !exists {
		report["requestedModel"] = requestedModel
	}
	resultSummary["trustReport"] = report
}

func putOptional[T any](target map[string]any, key string, value *T) {
	if value != nil {
		target[key] = *value
	}
}

func containsString(value any, wanted string) bool {
	items, ok := value.([]any)
	if !ok {
		if strings, stringOK := value.([]string); stringOK {
			for _, item := range strings {
				if item == wanted {
					return true
				}
			}
		}
		return false
	}
	for _, item := range items {
		if anyString(item) == wanted {
			return true
		}
	}
	return false
}

func anyString(value any) string {
	result, _ := value.(string)
	return result
}

func runSummary(run port.ManagementModelCheckRun, includeSystemAccountFields bool, includeSummaries bool) RunSummary {
	result := RunSummary{
		ID: run.ID, ProviderCode: run.ProviderCode, TargetType: run.TargetType, TargetID: run.TargetID,
		TargetName: text(run.TargetName), AccountID: text(run.AccountID), GroupID: text(run.GroupID), APIKeyID: text(run.APIKeyID),
		Model: run.Model, Profile: run.Profile, TrustedComparison: run.TrustedComparison,
		TrustedComparisonAvailable: run.TrustedComparisonAvailable, Level: run.Level, Score: run.Score, MaxScore: run.MaxScore,
		Status: run.Status, Message: run.Message, TraceID: text(run.TraceID), ProbeSetVersion: run.ProbeSetVersion,
		StartedAt: run.StartedAt, FinishedAt: text(run.FinishedAt), DurationMs: run.DurationMs,
		ErrorCode: text(run.ErrorCode), ErrorMessage: text(run.ErrorMessage), CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
	}
	if includeSystemAccountFields {
		result.SystemAccountID = run.SystemAccountID
		result.ActorSystemAccountID = run.ActorSystemAccountID
		result.TargetOwnerSystemAccountID = text(run.TargetOwnerSystemAccountID)
	}
	if includeSummaries {
		result.RequestSummary = jsonObject(run.RequestSummaryJSON)
		result.ResultSummary = jsonObject(run.ResultSummaryJSON)
	}
	return result
}

func jsonObject(raw string) map[string]any {
	result := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &result); err != nil || result == nil {
		return map[string]any{}
	}
	return result
}

func normalizedModel(value string) string {
	value = trim(value)
	if _, ok := supportedModelSet[value]; ok {
		return value
	}
	return ""
}

func normalizedLevel(value string) string {
	value = trim(value)
	switch value {
	case "high_confidence", "likely", "uncertain", "suspicious", "unavailable":
		return value
	default:
		return ""
	}
}

func normalizedStatus(value string) string {
	value = trim(value)
	switch value {
	case "running", "completed", "failed", "canceled":
		return value
	default:
		return ""
	}
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func trim(value string) string {
	return strings.TrimSpace(value)
}
