package modelcheckowner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprobe"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckprofile"
)

type Target struct {
	Endpoint string
	Protocol modelcheckprofile.Protocol
	Headers  http.Header
	Prompt   string
}

type Resolver func(context.Context, RunRequest) (Target, error)

// Runtime is the Gateway-owned basic probe execution path. It deliberately
// requires an injected resolver so credential/source reads remain inside the
// Gateway owner and never become an HTTP or Node dependency.
type Runtime struct {
	Store   *Store
	Resolve Resolver
	OwnerID string
	Now     func() time.Time
	Lease   time.Duration
	OnEvent func(ProgressEvent)
}

func (s *Runtime) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	return s.run(ctx, request, nil)
}

func (s *Runtime) RunStream(ctx context.Context, request RunRequest, onEvent func(ProgressEvent)) (RunResult, error) {
	return s.run(ctx, request, onEvent)
}

func (s *Runtime) run(ctx context.Context, request RunRequest, onEvent func(ProgressEvent)) (RunResult, error) {
	if s == nil || s.Store == nil || s.Resolve == nil {
		return RunResult{}, errors.New("J3b Gateway runtime is not initialized")
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	if request.SystemAccountID == "" || request.ActorSystemAccountID == "" || request.TargetID == "" || request.Model == "" {
		return RunResult{}, errors.New("J3b runtime request is incomplete")
	}
	target, err := s.Resolve(ctx, request)
	if err != nil {
		return RunResult{}, fmt.Errorf("resolve J3b target: %w", err)
	}
	if target.Endpoint == "" || target.Prompt == "" {
		return RunResult{}, errors.New("resolved J3b target is incomplete")
	}
	inputID, runID, outcomeID := newID("input"), newID("run"), newID("outcome")
	probeSet := request.ProbeSetVersion
	if probeSet == "" {
		probeSet = modelcheckprofile.QuickProbeSetVersion
	}
	identity := request.IdentityKey
	if identity == "" {
		identity = request.SystemAccountID + ":" + request.TargetID + ":" + request.Model
	}
	payload, _ := json.Marshal(map[string]any{"targetType": request.TargetType, "targetId": request.TargetID, "model": request.Model, "profile": request.Profile, "probeSetVersion": probeSet})
	input, err := s.Store.IssueInput(ctx, InputRecord{InputID: inputID, IdentityKey: identity, TargetID: request.TargetID, ConfigRevision: request.ConfigRevision, PolicyRevision: request.PolicyRevision, Trigger: "manual", IssuedAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload})
	if err != nil {
		return RunResult{}, err
	}
	if err := s.Store.CreateRun(ctx, RunRecord{ID: runID, SystemAccountID: request.SystemAccountID, ActorSystemAccountID: request.ActorSystemAccountID, ProviderCode: "unknown", TargetType: request.TargetType, TargetID: request.TargetID, Model: request.Model, Profile: request.Profile, TriggerKind: "manual", ProbeSetVersion: probeSet, StartedAt: now, RequestSummary: payload, PolicySnapshot: []byte(`{}`)}); err != nil {
		return RunResult{}, err
	}
	lease := s.Lease
	if lease <= 0 {
		lease = time.Minute
	}
	claim, err := s.Store.ClaimInput(ctx, input.InputID, newID("claim"), outcomeID, ownerOrDefault(s.OwnerID), lease, now)
	if err != nil {
		return RunResult{}, err
	}
	emit := func(event ProgressEvent) {
		if onEvent != nil {
			onEvent(event)
		}
		if s.OnEvent != nil {
			s.OnEvent(event)
		}
	}
	emit(ProgressEvent{Kind: "run_started", Data: map[string]any{"runId": runID}})
	items, probeErr := modelcheckprobe.RunSuite(ctx, modelcheckprobe.Suite{Endpoint: target.Endpoint, Headers: target.Headers, Model: request.Model, Profile: request.Profile, Protocol: target.Protocol}, lease)
	if probeErr != nil {
		_ = s.Store.ReleaseClaim(ctx, claim, now)
		return s.finishFailure(ctx, runID, input, outcomeID, now, probeErr)
	}
	itemRecords := make([]ItemRecord, 0, len(items))
	totalScore, totalMax := 0, 0
	level, message := "success", "probe suite completed"
	for index, evaluation := range items {
		status := ItemStatus(evaluation.Status)
		if status == "" {
			status = ItemSkipped
		}
		if status == ItemFailed {
			level, message = "failure", "probe suite reported a failure"
		}
		if status == ItemWarning && level == "success" {
			level, message = "warning", "probe suite reported a warning"
		}
		evidence, _ := json.Marshal(evaluation.Evidence)
		itemRecords = append(itemRecords, ItemRecord{ID: fmt.Sprintf("%s-item-%04d", runID, index+1), RunID: runID, ItemKey: evaluation.Kind, ItemType: evaluation.Kind, Status: status, Score: evaluation.Score, MaxScore: evaluation.MaxScore, EvidenceSummary: string(evidence)})
		totalScore += evaluation.Score
		totalMax += evaluation.MaxScore
	}
	if totalMax == 0 {
		totalMax = 1
	}
	score := totalScore * 100 / totalMax
	status := RunCompleted
	for _, item := range itemRecords {
		if item.Status == ItemFailed {
			status = RunFailed
			break
		}
	}
	observationStatus, protocolStatus := "partial", "passed"
	if status == RunFailed {
		observationStatus, protocolStatus = "partial", "failed"
	}
	observedModel := request.Model
	for _, item := range items {
		if model, ok := item.Evidence["responseModel"].(string); ok && model != "" {
			observedModel = model
			break
		}
	}
	if err := s.Store.AppendObservation(ctx, ObservationRecord{ID: runID + "-observation-0001", RunID: runID, SystemAccountID: request.SystemAccountID, AccountID: request.TargetID, ProviderCode: "unknown", RequestedModel: request.Model, MappedUpstreamModel: observedModel, ProbeFamily: "core-suite", ObservationStatus: observationStatus, IdentityStatus: "unverified", MappingStatus: "unverified", ProtocolStatus: protocolStatus, EvidenceCoverage: len(items), CreatedAt: now}); err != nil {
		return RunResult{}, err
	}
	resultPayload, _ := json.Marshal(map[string]any{"evaluations": items, "score": score, "maxScore": 100, "level": level})
	evidenceItems := make([]map[string]any, 0, len(items))
	for _, evaluation := range items {
		evidenceItems = append(evidenceItems, map[string]any{"kind": evaluation.Kind, "status": evaluation.Status, "score": evaluation.Score})
	}
	aggregate := AggregateEvidence(evidenceItems)
	qualityDecision, _ := json.Marshal(map[string]any{"evidenceFormed": aggregate.Formed, "trustFormed": aggregate.TrustFormed, "missingFamilies": aggregate.Missing, "partialFamilies": aggregate.Partial})
	if err := s.Store.CommitOutcome(ctx, Outcome{OutcomeID: outcomeID, InputID: input.InputID, InputDigest: input.InputDigest, Payload: resultPayload}, claim, now); err != nil {
		return RunResult{}, err
	}
	if err := s.Store.ProjectOutcome(ctx, OutcomeProjection{RunID: runID, Status: status, Level: level, Score: score, MaxScore: 100, Message: message, FinishedAt: now, Items: itemRecords, ResultSummary: resultPayload, QualityDecision: qualityDecision}); err != nil {
		return RunResult{}, err
	}
	emit(ProgressEvent{Kind: "run_completed", Data: map[string]any{"runId": runID, "status": status}})
	return RunResult{RunID: runID, Status: string(status), Data: map[string]any{"level": level, "score": score, "message": message}}, nil
}

func (s *Runtime) finishFailure(ctx context.Context, runID string, input InputRecord, outcomeID string, now time.Time, cause error) (RunResult, error) {
	message := cause.Error()
	payload, _ := json.Marshal(map[string]any{"success": false, "error": message})
	item := ItemRecord{ID: runID + "-item-0001", RunID: runID, ItemKey: "target.execution", ItemType: "execution", Status: ItemFailed, Score: 0, MaxScore: 100, EvidenceSummary: fmt.Sprintf(`{"message":%q}`, message), ErrorMessage: message}
	if err := s.Store.ProjectOutcome(ctx, OutcomeProjection{RunID: runID, Status: RunFailed, Level: "unavailable", Score: 0, MaxScore: 100, Message: message, FinishedAt: now, Items: []ItemRecord{item}, ResultSummary: payload, QualityDecision: []byte(`{}`)}); err != nil {
		return RunResult{}, err
	}
	return RunResult{RunID: runID, Status: string(RunFailed), Data: map[string]any{"message": message}}, cause
}

func ownerOrDefault(owner string) string {
	if strings.TrimSpace(owner) == "" {
		return "gateway"
	}
	return owner
}

func newID(prefix string) string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return prefix + "-fallback"
	}
	return prefix + "-" + hex.EncodeToString(bytes[:])
}
