// Package modelcheckruntime composes the J3b durable input, probe executor,
// and dataset projection inside one Go process. It has no Node or RPC
// dependency; HTTP/SSE and scheduler adapters can be layered on this service.
package modelcheckruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckdurable"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckquality"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckstore"
)

var (
	ErrNotInitialized = errors.New("model check runtime is not initialized")
	ErrInvalidRequest = errors.New("model check runtime request is invalid")
	ErrActiveRun      = errors.New("model check active run already exists")
)

type IDFactory func(prefix string) string

type RunRequest struct {
	InputID              string
	RunID                string
	OutcomeID            string
	OwnerID              string
	ClaimToken           string
	SystemAccountID      string
	ActorSystemAccountID string
	Target               modelcheckinput.AccountSnapshot
	Comparison           *modelcheckinput.AccountSnapshot
	Model                string
	Profile              string
	Trigger              modelcheckinput.Trigger
	ScheduleID           string
	TrustedComparison    bool
	ProbeSetVersion      string
	Policy               modelcheckinput.PolicySnapshot
	StartedAt            time.Time
	DeadlineAt           time.Time
	TargetName           string
	TargetOwnerSystemID  string
	ProviderCode         string
	TargetType           string
	GroupID              string
	APIKeyID             string
	TraceID              string
	ActiveKey            string
	// ActiveLease is acquired by the management transport before it writes an
	// HTTP/SSE response. Scheduler callers leave it nil and the runtime acquires
	// its own lease from Active.
	ActiveLease *modelcheckactive.Handle
}

type Result struct {
	RunID     string                           `json:"runId"`
	InputID   string                           `json:"inputId"`
	OutcomeID string                           `json:"outcomeId"`
	Items     []modelcheckprobe.EvaluationItem `json:"items"`
	Summary   modelcheckprobe.SummaryResult    `json:"summary"`
	RunStatus modelcheckstore.RunStatus        `json:"status"`
}

type ProgressEvent struct {
	Type      string    `json:"type"`
	RunID     string    `json:"runId,omitempty"`
	InputID   string    `json:"inputId,omitempty"`
	Message   string    `json:"message"`
	Status    string    `json:"status,omitempty"`
	StartedAt time.Time `json:"startedAt,omitempty"`
}

type Service struct {
	Durable  *modelcheckdurable.Store
	Dataset  *modelcheckstore.Store
	Resolver modelcheckexecutor.TargetResolver
	Retry    modelcheckprobe.RetryOptions
	Active   *modelcheckactive.Registry
	Now      func() time.Time
	NewID    IDFactory
	sequence atomic.Uint64
}

func (s *Service) Run(ctx context.Context, request RunRequest) (Result, error) {
	return s.run(ctx, request, nil)
}

// RunWithProgress executes one run and delivers best-effort lifecycle events
// to the supplied callback. The callback is scoped to this invocation, so
// concurrent management requests cannot overwrite one another's observer.
func (s *Service) RunWithProgress(ctx context.Context, request RunRequest, progress func(ProgressEvent)) (Result, error) {
	return s.run(ctx, request, progress)
}

func (s *Service) run(ctx context.Context, request RunRequest, progress func(ProgressEvent)) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx := ctx
	var activeHandle modelcheckactive.Handle
	if request.ActiveLease != nil {
		activeHandle = *request.ActiveLease
		runCtx = activeHandle.Context()
		defer activeHandle.Finish()
	}
	if s == nil || s.Durable == nil || s.Dataset == nil || s.Resolver == nil {
		return Result{}, ErrNotInitialized
	}
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	now := request.StartedAt.UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	ids := s.NewID
	if ids == nil {
		ids = func(prefix string) string {
			return fmt.Sprintf("%s-%d-%d", prefix, now.UnixNano(), s.sequence.Add(1))
		}
	}
	inputID := request.InputID
	if inputID == "" {
		inputID = ids("model-check-input")
	}
	runID := request.RunID
	if runID == "" {
		runID = ids("model-check-run")
	}
	outcomeID := request.OutcomeID
	if outcomeID == "" {
		outcomeID = ids("model-check-outcome")
	}
	ownerID := request.OwnerID
	if ownerID == "" {
		ownerID = ids("model-check-owner")
	}
	claimToken := request.ClaimToken
	if claimToken == "" {
		claimToken = ids("model-check-claim")
	}
	if request.ActiveLease == nil && s.Active != nil {
		activeKey := request.ActiveKey
		if activeKey == "" {
			activeKey = "system-account:" + request.SystemAccountID
		}
		var acquired bool
		activeHandle, acquired, _ = s.Active.TryStart(ctx, activeKey, modelcheckactive.Summary{RunID: runID, TargetID: request.Target.ID, TargetName: request.TargetName, Model: request.Model, Profile: request.Profile, StartedAt: request.StartedAt.UTC()})
		if !acquired {
			return Result{}, ErrActiveRun
		}
		defer activeHandle.Finish()
		runCtx = activeHandle.Context()
	}
	// A transport may reserve a scope before the runtime has generated the ID.
	// Updating through the handle cannot overwrite a newer run on the same key.
	activeHandle.Update(modelcheckactive.Summary{RunID: runID, TargetID: request.Target.ID, TargetName: request.TargetName, Model: request.Model, Profile: request.Profile, StartedAt: request.StartedAt.UTC()})
	emitProgress(progress, ProgressEvent{Type: "run_started", RunID: runID, InputID: inputID, Message: "模型检测已开始", StartedAt: request.StartedAt.UTC()})

	draft := modelcheckinput.Draft{
		InputID:              inputID,
		SystemAccountID:      request.SystemAccountID,
		ActorSystemAccountID: request.ActorSystemAccountID,
		Target:               request.Target,
		Comparison:           request.Comparison,
		Model:                request.Model,
		Profile:              request.Profile,
		Trigger:              request.Trigger,
		ScheduleID:           request.ScheduleID,
		TrustedComparison:    request.TrustedComparison,
		ProbeSetVersion:      request.ProbeSetVersion,
		Policy:               request.Policy,
		IssuedAt:             request.StartedAt,
		DeadlineAt:           request.DeadlineAt,
	}
	issued, err := s.Durable.Issue(runCtx, draft)
	if err != nil {
		return Result{}, fmt.Errorf("issue model check input: %w", err)
	}
	if err := s.Dataset.CreateRun(runCtx, modelcheckstore.RunInput{
		ID:                         runID,
		SystemAccountID:            request.SystemAccountID,
		ActorSystemAccountID:       request.ActorSystemAccountID,
		ProviderCode:               request.ProviderCode,
		TargetType:                 request.TargetType,
		TargetID:                   request.Target.ID,
		TargetName:                 request.TargetName,
		TargetOwnerSystemAccountID: request.TargetOwnerSystemID,
		AccountID:                  request.Target.ID,
		GroupID:                    request.GroupID,
		APIKeyID:                   request.APIKeyID,
		Model:                      request.Model,
		Profile:                    request.Profile,
		Trigger:                    triggerToStore(request.Trigger),
		ScheduleID:                 request.ScheduleID,
		TrustedComparisonEnabled:   request.TrustedComparison,
		TrustedComparisonAvailable: request.Comparison != nil,
		TraceID:                    request.TraceID,
		ProbeSetVersion:            request.ProbeSetVersion,
		StartedAt:                  request.StartedAt,
		RequestSummary:             marshalRequestSummary(inputID),
		PolicySnapshot:             mustJSONPolicy(request.Policy),
	}); err != nil {
		return Result{}, fmt.Errorf("create model check run: %w", err)
	}
	emitProgress(progress, ProgressEvent{Type: "run_created", RunID: runID, InputID: issued.Input.InputID, Message: "检测记录已创建，开始执行探针", StartedAt: request.StartedAt.UTC()})

	payload, executeErr := modelcheckexecutor.ExecuteInputWithOptions(runCtx, s.Durable, issued.Input.InputID, ownerID, claimToken, outcomeID, now, s.Resolver, s.Retry, modelcheckexecutor.ExecuteOptions{OnItem: func(item modelcheckprobe.EvaluationItem) {
		emitProgress(progress, ProgressEvent{Type: "probe_completed", RunID: runID, InputID: issued.Input.InputID, Message: item.ItemKey, Status: item.Status})
	}})
	if executeErr != nil {
		status := modelcheckstore.RunFailed
		if errors.Is(executeErr, context.Canceled) || errors.Is(executeErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
			status = modelcheckstore.RunCanceled
		}
		item := failureItem(runID, executeErr, ids)
		projectionCtx, cancel := terminalProjectionContext(runCtx)
		defer cancel()
		failureSummary := modelcheckprobe.SummaryResult{Level: "unavailable", Score: 0, MaxScore: 100, Message: executeErr.Error()}
		if projectionErr := s.Dataset.ProjectOutcome(projectionCtx, modelcheckstore.OutcomeProjection{RunID: runID, Items: []modelcheckstore.ItemInput{item}, Status: status, Level: "unavailable", Score: 0, MaxScore: 100, Message: executeErr.Error(), FinishedAt: now, ResultSummary: mustJSONSummary(failureSummary)}); projectionErr != nil {
			return Result{}, errors.Join(executeErr, fmt.Errorf("project model check failure: %w", projectionErr))
		}
		failureOutcome := struct {
			RunID   string                        `json:"runId"`
			Status  modelcheckstore.RunStatus     `json:"status"`
			Summary modelcheckprobe.SummaryResult `json:"summary"`
			Item    modelcheckstore.ItemInput     `json:"item"`
		}{RunID: runID, Status: status, Summary: failureSummary, Item: item}
		if decisionErr := s.Dataset.UpdateQualityDecision(projectionCtx, modelcheckstore.QualityDecisionUpdate{RunID: runID, Status: status, ResultSummary: mustJSONSummary(failureSummary), PolicySnapshot: mustJSONPolicy(request.Policy), Decision: qualityDecisionJSON(request, failureSummary, false, now, failureOutcome)}); decisionErr != nil {
			return Result{}, errors.Join(executeErr, fmt.Errorf("project model check quality decision: %w", decisionErr))
		}
		resultItem := modelcheckprobe.EvaluationItem{ItemKey: item.ItemKey, ItemType: item.ItemType, Status: "failed", Score: item.Score, MaxScore: item.MaxScore, DurationMS: valueOrZero(item.DurationMS), Evidence: map[string]any{"message": executeErr.Error()}, ErrorCode: item.ErrorCode, ErrorMessage: item.ErrorMessage}
		eventType := "error"
		if status == modelcheckstore.RunCanceled {
			eventType = "run_completed"
		}
		emitProgress(progress, ProgressEvent{Type: eventType, RunID: runID, InputID: issued.Input.InputID, Message: executeErr.Error(), Status: string(status)})
		return Result{RunID: runID, InputID: issued.Input.InputID, OutcomeID: outcomeID, Items: []modelcheckprobe.EvaluationItem{resultItem}, Summary: modelcheckprobe.SummaryResult{Level: "unavailable", Score: 0, MaxScore: 100, Message: executeErr.Error()}, RunStatus: status}, executeErr
	}

	items := payload.Items
	if len(items) == 0 {
		items = []modelcheckprobe.EvaluationItem{payload.Item}
	}
	projected := make([]modelcheckstore.ItemInput, 0, len(items))
	for index, item := range items {
		projected = append(projected, itemInput(runID, index, item))
	}
	projectionCtx, cancel := terminalProjectionContext(runCtx)
	defer cancel()
	if err := s.Dataset.ProjectOutcome(projectionCtx, modelcheckstore.OutcomeProjection{RunID: runID, Items: projected, Status: modelcheckstore.RunCompleted, Level: payload.Summary.Level, Score: payload.Summary.Score, MaxScore: payload.Summary.MaxScore, Message: payload.Summary.Message, FinishedAt: now, ResultSummary: mustJSONSummary(payload.Summary)}); err != nil {
		return Result{}, fmt.Errorf("project model check outcome: %w", err)
	}
	if err := s.Dataset.UpdateQualityDecision(projectionCtx, modelcheckstore.QualityDecisionUpdate{RunID: runID, Status: modelcheckstore.RunCompleted, ResultSummary: mustJSONSummary(payload.Summary), PolicySnapshot: mustJSONPolicy(request.Policy), Decision: qualityDecisionJSON(request, payload.Summary, true, now, payload)}); err != nil {
		return Result{}, fmt.Errorf("project model check quality decision: %w", err)
	}
	emitProgress(progress, ProgressEvent{Type: "run_completed", RunID: runID, InputID: issued.Input.InputID, Message: payload.Summary.Message, Status: string(modelcheckstore.RunCompleted)})
	return Result{RunID: runID, InputID: issued.Input.InputID, OutcomeID: outcomeID, Items: items, Summary: payload.Summary, RunStatus: modelcheckstore.RunCompleted}, nil
}

func emitProgress(progress func(ProgressEvent), event ProgressEvent) {
	if progress == nil {
		return
	}
	defer func() { _ = recover() }()
	progress(event)
}

func validateRequest(request RunRequest) error {
	if request.SystemAccountID == "" || request.ActorSystemAccountID == "" || request.Target.ID == "" || request.Target.ConfigRevision == "" || request.Target.ProtocolProfileID == "" || request.Target.ProtocolProfileRevision == "" || request.Target.EndpointFingerprint == "" || request.Target.MappedUpstreamModel == "" || request.Target.CredentialEnvelopeRef == "" || request.Target.ProxyConfigurationVersion == "" || request.Model == "" || request.Profile == "" || request.ProbeSetVersion == "" || request.Policy.Revision == "" || request.Policy.Digest == "" || request.StartedAt.IsZero() || request.DeadlineAt.IsZero() || !request.DeadlineAt.After(request.StartedAt) {
		return ErrInvalidRequest
	}
	if request.TrustedComparison != (request.Comparison != nil) {
		return ErrInvalidRequest
	}
	return nil
}

func triggerToStore(trigger modelcheckinput.Trigger) modelcheckstore.Trigger {
	switch trigger {
	case modelcheckinput.TriggerScheduled:
		return modelcheckstore.TriggerScheduled
	case modelcheckinput.TriggerQualityRecovery:
		return modelcheckstore.TriggerQualityRecovery
	default:
		return modelcheckstore.TriggerManual
	}
}

func itemInput(runID string, index int, item modelcheckprobe.EvaluationItem) modelcheckstore.ItemInput {
	duration := item.DurationMS
	return modelcheckstore.ItemInput{ID: fmt.Sprintf("%s-item-%04d", runID, index), RunID: runID, ItemKey: item.ItemKey, ItemType: item.ItemType, Status: itemStatus(item.Status), Score: item.Score, MaxScore: item.MaxScore, DurationMS: &duration, TraceID: item.TraceID, EvidenceSummary: marshalEvidence(item.Evidence), ErrorCode: item.ErrorCode, ErrorMessage: item.ErrorMessage}
}

func failureItem(runID string, err error, ids IDFactory) modelcheckstore.ItemInput {
	id := fmt.Sprintf("%s-item-execution", runID)
	if ids != nil {
		id = ids("model-check-failure-item")
	}
	message := err.Error()
	duration := int64(0)
	return modelcheckstore.ItemInput{ID: id, RunID: runID, ItemKey: "target.execution", ItemType: "execution", Status: modelcheckstore.ItemFailed, Score: 0, MaxScore: 100, DurationMS: &duration, EvidenceSummary: []byte(`{"message":` + quoteJSON(message) + `}`), ErrorCode: "model_check_execution_failed", ErrorMessage: message}
}

func itemStatus(status string) modelcheckstore.ItemStatus {
	switch status {
	case "passed":
		return modelcheckstore.ItemPassed
	case "warning":
		return modelcheckstore.ItemWarning
	case "skipped":
		return modelcheckstore.ItemSkipped
	default:
		return modelcheckstore.ItemFailed
	}
}

func marshalEvidence(value map[string]any) []byte {
	if len(value) == 0 {
		return []byte(`{}`)
	}
	data, err := jsonMarshal(value)
	if err != nil {
		return []byte(`{"evidenceEncoding":"failed"}`)
	}
	return data
}

func marshalRequestSummary(inputID string) []byte {
	data, err := json.Marshal(map[string]string{"inputId": inputID})
	if err != nil {
		return []byte(`{"inputId":""}`)
	}
	return data
}

func terminalProjectionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, 5*time.Second)
}

func valueOrZero(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func mustJSONSummary(summary modelcheckprobe.SummaryResult) []byte {
	data, _ := jsonMarshal(summary)
	return data
}

func mustJSONPolicy(policy modelcheckinput.PolicySnapshot) []byte {
	data, _ := jsonMarshal(policy)
	return data
}

func qualityDecisionJSON(request RunRequest, summary modelcheckprobe.SummaryResult, completed bool, decidedAt time.Time, outcome any) []byte {
	decision := modelcheckquality.Decide(request.Trigger, request.Policy, summary, completed, modelcheckquality.Evidence{}, decidedAt)
	fact := modelcheckquality.NewFact(decision, digestJSON(outcome), request.Policy.Digest, digestJSON(modelcheckquality.Evidence{}))
	data, err := jsonMarshal(fact)
	if err != nil {
		return []byte(`{"result":"not_triggered","reasonCodes":["quality_decision_encoding_failed"]}`)
	}
	return data
}

func digestJSON(value any) string {
	data, err := jsonMarshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Small local wrappers keep the runtime package free of mutable JSON state.
var jsonMarshal = func(value any) ([]byte, error) { return json.Marshal(value) }

func quoteJSON(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
