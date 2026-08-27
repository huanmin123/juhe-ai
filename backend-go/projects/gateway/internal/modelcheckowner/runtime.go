package modelcheckowner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
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
	Endpoint, ProviderCode, ConfigRevision, UpstreamModel string
	Protocol                                              modelcheckprofile.Protocol
	Headers                                               http.Header
	Prompt                                                string
}

type Resolver func(context.Context, RunRequest) (Target, error)

// Runtime is the Gateway-owned basic probe execution path. It deliberately
// requires an injected resolver so credential/source reads remain inside the
// Gateway owner and never become an HTTP or Node dependency.
type Runtime struct {
	Store             *Store
	Resolve           Resolver
	ResolveComparison Resolver
	Tokenizer         modelcheckprobe.Tokenizer
	ModelLimits       modelcheckprobe.ModelLimitSnapshot
	Projector         *QualityProjector
	OwnerID           string
	Now               func() time.Time
	Lease             time.Duration
	OnEvent           func(ProgressEvent)
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
	if request.SystemAccountID == "" || request.ActorSystemAccountID == "" {
		return RunResult{}, errors.New("J3b runtime scope is incomplete")
	}
	if request.TargetType != "account" || request.TargetID == "" || request.Model == "" {
		return RunResult{}, errors.New("J3b runtime request is incomplete")
	}
	if request.Profile != "quick" && request.Profile != "full" {
		return RunResult{}, errors.New("J3b runtime profile is invalid")
	}
	target, err := s.Resolve(ctx, request)
	if err != nil {
		return RunResult{}, fmt.Errorf("resolve J3b target: %w", err)
	}
	if target.Endpoint == "" || target.Prompt == "" {
		return RunResult{}, errors.New("resolved J3b target is incomplete")
	}
	var comparisonTarget Target
	if request.TrustedComparison {
		if request.Profile != "full" || strings.TrimSpace(request.TrustedComparisonAccountID) == "" || s.ResolveComparison == nil {
			return RunResult{}, errors.New("resolved J3b trusted comparison contract is incomplete")
		}
		comparisonTarget, err = s.ResolveComparison(ctx, request)
		if err != nil {
			return RunResult{}, fmt.Errorf("resolve J3b trusted comparison: %w", err)
		}
		if comparisonTarget.Endpoint == "" || comparisonTarget.Prompt == "" || comparisonTarget.UpstreamModel == "" {
			return RunResult{}, errors.New("resolved J3b trusted comparison target is incomplete")
		}
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
	// Keep the frozen revisions in the durable request summary. Health retry
	// must reuse the original CAS inputs instead of consulting mutable state.
	// Freeze both the requested identity and the resolved upstream identity.
	// Mapping rules are mutable configuration; retries and audit reads must never
	// silently substitute a later mapping for the model actually probed.
	upstreamModel := target.UpstreamModel
	if upstreamModel == "" {
		upstreamModel = request.Model
	}
	payloadSnapshot := map[string]any{"targetType": request.TargetType, "targetId": request.TargetID, "model": request.Model, "upstreamModel": upstreamModel, "profile": request.Profile, "protocol": target.Protocol, "endpointFingerprint": endpointFingerprint(target.Endpoint), "probeSetVersion": probeSet, "configRevision": request.ConfigRevision, "policyRevision": request.PolicyRevision}
	if request.TrustedComparison {
		payloadSnapshot["trustedComparison"] = map[string]any{"accountId": request.TrustedComparisonAccountID, "configRevision": request.TrustedComparisonConfigRevision, "upstreamModel": comparisonTarget.UpstreamModel, "protocol": comparisonTarget.Protocol, "endpointFingerprint": endpointFingerprint(comparisonTarget.Endpoint)}
	}
	payload, _ := json.Marshal(payloadSnapshot)
	penaltyAction := request.PenaltyAction
	if penaltyAction == "" {
		penaltyAction = "quality_isolate"
	}
	if penaltyAction != "disable" && penaltyAction != "fallback" && penaltyAction != "quality_isolate" {
		return RunResult{}, errors.New("J3b runtime penalty action is invalid")
	}
	recoveryInterval := request.RecoveryIntervalMinutes
	if recoveryInterval == 0 {
		recoveryInterval = 10
	}
	if recoveryInterval < 10 || recoveryInterval > 10080 {
		return RunResult{}, errors.New("J3b runtime recovery interval is invalid")
	}
	policySnapshot, _ := json.Marshal(map[string]any{"revision": request.PolicyRevision, "threshold": request.Threshold, "action": penaltyAction, "recoveryIntervalMinutes": recoveryInterval})
	providerCode := request.ProviderCode
	if providerCode == "" {
		providerCode = "unknown"
	}
	triggerKind := request.TriggerKind
	if triggerKind == "" {
		triggerKind = "manual"
	}
	if triggerKind != "manual" && triggerKind != "scheduled" && triggerKind != "quality_recovery" {
		return RunResult{}, errors.New("J3b runtime trigger is invalid")
	}
	input, err := s.Store.IssueInput(ctx, InputRecord{InputID: inputID, IdentityKey: identity, TargetID: request.TargetID, ConfigRevision: request.ConfigRevision, PolicyRevision: request.PolicyRevision, Trigger: triggerKind, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Payload: payload})
	if err != nil {
		return RunResult{}, err
	}
	accountID := ""
	if request.TargetType == "account" {
		accountID = request.TargetID
	}
	if err := s.Store.CreateRun(ctx, RunRecord{ID: runID, SystemAccountID: request.SystemAccountID, ActorSystemAccountID: request.ActorSystemAccountID, ProviderCode: providerCode, TargetType: request.TargetType, TargetID: request.TargetID, AccountID: accountID, Model: request.Model, Profile: request.Profile, TriggerKind: triggerKind, ScheduleID: request.ScheduleID, ProbeSetVersion: probeSet, StartedAt: now, RequestSummary: payload, PolicySnapshot: policySnapshot}); err != nil {
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
	probeModel := upstreamModel
	probeSuite := modelcheckprobe.Suite{Endpoint: target.Endpoint, ProviderCode: target.ProviderCode, Headers: target.Headers, Model: probeModel, Profile: request.Profile, Protocol: target.Protocol, Tokenizer: s.Tokenizer, ModelLimits: s.ModelLimits}
	if request.TrustedComparison {
		probeSuite.Comparison = &modelcheckprobe.Suite{Endpoint: comparisonTarget.Endpoint, Headers: comparisonTarget.Headers, Model: comparisonTarget.UpstreamModel, Profile: request.Profile, Protocol: comparisonTarget.Protocol}
	}
	items, probeErr := modelcheckprobe.RunSuite(ctx, probeSuite, lease)
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
	mappingStatus := "unmapped"
	if probeModel != request.Model {
		mappingStatus = "mapped"
	}
	resultPayload, _ := json.Marshal(map[string]any{"evaluations": items, "score": score, "maxScore": 100, "level": level})
	evidenceItems := make([]map[string]any, 0, len(items))
	for _, evaluation := range items {
		evidenceItems = append(evidenceItems, map[string]any{"kind": evaluation.Kind, "status": evaluation.Status, "score": evaluation.Score})
	}
	aggregate := AggregateEvidence(evidenceItems)
	trustReport := BuildTrustReport(aggregate, evidenceItems)
	observationStatus, protocolStatus := "partial", "passed"
	if aggregate.Formed {
		observationStatus = "complete"
	}
	if status == RunFailed {
		protocolStatus = "failed"
	}
	identityStatus := observationIdentityStatus(evidenceItems)
	if err := appendEvaluationObservations(ctx, s.Store, runID, request.SystemAccountID, request.TargetID, providerCode, request.Model, probeModel, mappingStatus, protocolStatus, identityStatus, len(aggregate.Families), items, now); err != nil {
		return RunResult{}, err
	}
	if request.TrustedComparison {
		comparisonMappingStatus := "unmapped"
		if comparisonTarget.UpstreamModel != request.Model {
			comparisonMappingStatus = "mapped"
		}
		if err := appendObservationIdempotent(ctx, s.Store, ObservationRecord{ID: runID + "-observation-0002", RunID: runID, SystemAccountID: request.SystemAccountID, AccountID: request.TrustedComparisonAccountID, ProviderCode: comparisonTarget.ProviderCode, RequestedModel: request.Model, MappedUpstreamModel: comparisonTarget.UpstreamModel, ProbeFamily: "trusted-comparison", ObservationStatus: observationStatus, IdentityStatus: identityStatus, MappingStatus: comparisonMappingStatus, ProtocolStatus: protocolStatus, EvidenceCoverage: len(aggregate.Families), CreatedAt: now}); err != nil {
			return RunResult{}, err
		}
	}
	qualityDecision, _ := json.Marshal(map[string]any{"evidenceFormed": aggregate.Formed, "trustFormed": aggregate.TrustFormed, "missingFamilies": aggregate.Missing, "partialFamilies": aggregate.Partial, "trust": trustReport})
	if err := s.Store.CommitOutcome(ctx, Outcome{OutcomeID: outcomeID, InputID: input.InputID, InputDigest: input.InputDigest, Payload: resultPayload}, claim, now); err != nil {
		return RunResult{}, err
	}
	if err := s.Store.ProjectOutcome(ctx, OutcomeProjection{RunID: runID, Status: status, Level: level, Score: score, MaxScore: 100, Message: message, FinishedAt: now, Items: itemRecords, ResultSummary: resultPayload, QualityDecision: qualityDecision}); err != nil {
		return RunResult{}, err
	}
	if aggregate.Formed && aggregate.TrustFormed && s.Projector != nil && request.Threshold > 0 && request.ProviderCode != "" {
		fact := HealthFact{AccountID: request.TargetID, SystemAccountID: request.SystemAccountID, StatHour: now.Truncate(time.Hour).Format(time.RFC3339Nano), RunID: runID, ProviderCode: request.ProviderCode, Model: request.Model, Profile: request.Profile, ScheduleID: request.ScheduleID, PolicyRevision: request.PolicyRevision, AccountConfigRevision: request.ConfigRevision, PenaltyAction: penaltyAction, RecoveryIntervalMinutes: recoveryInterval, ObservedAt: now, Score: score, Threshold: request.Threshold, Level: level}
		if err := s.Projector.Project(ctx, runID, aggregate, fact); err != nil {
			// The run/outcome is already durable. Keep the health publication
			// retryable instead of reporting a false applied state.
			emit(ProgressEvent{Kind: "health_sync_failed", Data: map[string]any{"runId": runID, "message": err.Error()}})
		}
	}
	emit(ProgressEvent{Kind: "run_completed", Data: map[string]any{"runId": runID, "status": status}})
	return RunResult{RunID: runID, Status: string(status), Data: map[string]any{
		"level":   level,
		"score":   score,
		"message": message,
		// Scheduler quality recovery consumes these explicit durable quality
		// gates. Omitting either flag must remain fail-closed in the executor.
		"evidenceFormed": aggregate.Formed,
		"trustFormed":    aggregate.TrustFormed,
	}}, nil
}

// appendEvaluationObservations writes one bounded, durable receipt for every
// evaluation emitted by modelcheckprobe. The receipt intentionally contains
// no evaluation evidence payload: item evidence remains in the run projection
// after its own sanitizer, while observations carry only scope, mapping,
// protocol and formed/partial facts needed by downstream aggregation.
//
// IDs are derived from the run and stable evaluation position. A retry of the
// same projection therefore addresses the same primary keys; the idempotent
// append below treats an exact existing row as success and rejects drift.
func appendEvaluationObservations(ctx context.Context, store *Store, runID, systemAccountID, accountID, providerCode, requestedModel, mappedModel, mappingStatus, protocolStatus, identityStatus string, evidenceCoverage int, evaluations []modelcheckprobe.Evaluation, now time.Time) error {
	if len(evaluations) == 0 {
		return errors.New("J3b evaluation observations are empty")
	}
	for index, evaluation := range evaluations {
		family := strings.TrimSpace(evaluation.Kind)
		if family == "" {
			return fmt.Errorf("J3b evaluation %d family is empty", index)
		}
		if err := appendObservationIdempotent(ctx, store, ObservationRecord{
			ID:                  fmt.Sprintf("%s-observation-family-%04d", runID, index+1),
			RunID:               runID,
			SystemAccountID:     systemAccountID,
			AccountID:           accountID,
			ProviderCode:        providerCode,
			RequestedModel:      requestedModel,
			MappedUpstreamModel: mappedModel,
			ProbeFamily:         family,
			ObservationStatus:   evaluationObservationStatus(evaluation.Status),
			IdentityStatus:      identityStatus,
			MappingStatus:       mappingStatus,
			ProtocolStatus:      protocolStatus,
			EvidenceCoverage:    evidenceCoverage,
			CreatedAt:           now,
		}); err != nil {
			return fmt.Errorf("append J3b %s observation: %w", family, err)
		}
	}
	return nil
}

func appendObservationIdempotent(ctx context.Context, store *Store, observation ObservationRecord) error {
	if store == nil || store.db == nil {
		return errors.New("J3b store is not open")
	}
	if err := validateObservation(observation); err != nil {
		return err
	}
	if found, err := observationMatches(ctx, store, observation); err != nil {
		return err
	} else if found {
		return nil
	}
	if err := store.AppendObservation(ctx, observation); err == nil {
		return nil
	} else if found, lookupErr := observationMatches(ctx, store, observation); lookupErr == nil && found {
		// Another owner of the same durable run may have won the insert race.
		// Exact row equality makes this a safe replay; any drift remains the
		// original append error and is never silently accepted.
		return nil
	} else {
		return err
	}
}

func observationMatches(ctx context.Context, store *Store, observation ObservationRecord) (bool, error) {
	var existing ObservationRecord
	var created string
	err := store.db.QueryRowContext(ctx, store.bind(`SELECT id,run_id,system_account_id,account_id,provider_code,requested_model,mapped_upstream_model,probe_family,observation_status,identity_status,mapping_status,protocol_status,evidence_coverage,created_at FROM `+store.table("model_check_observations")+` WHERE id=?`), observation.ID).Scan(
		&existing.ID, &existing.RunID, &existing.SystemAccountID, &existing.AccountID, &existing.ProviderCode,
		&existing.RequestedModel, &existing.MappedUpstreamModel, &existing.ProbeFamily, &existing.ObservationStatus,
		&existing.IdentityStatus, &existing.MappingStatus, &existing.ProtocolStatus, &existing.EvidenceCoverage, &created,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read J3b observation idempotency key: %w", err)
	}
	existing.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return false, fmt.Errorf("parse J3b observation created_at: %w", err)
	}
	if existing.RunID != observation.RunID || existing.SystemAccountID != observation.SystemAccountID || existing.AccountID != observation.AccountID || existing.ProviderCode != observation.ProviderCode || existing.RequestedModel != observation.RequestedModel || existing.MappedUpstreamModel != observation.MappedUpstreamModel || existing.ProbeFamily != observation.ProbeFamily || existing.ObservationStatus != observation.ObservationStatus || existing.IdentityStatus != observation.IdentityStatus || existing.MappingStatus != observation.MappingStatus || existing.ProtocolStatus != observation.ProtocolStatus || existing.EvidenceCoverage != observation.EvidenceCoverage || !existing.CreatedAt.UTC().Equal(observation.CreatedAt.UTC()) {
		return false, fmt.Errorf("J3b observation %s conflicts with existing row", observation.ID)
	}
	return true, nil
}

func evaluationObservationStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "passed", "failed", "warning":
		return "complete"
	default:
		// skipped and unknown statuses are retained as partial evidence. They
		// must never be promoted to a formed quality fact by persistence.
		return "partial"
	}
}

func observationIdentityStatus(items []map[string]any) string {
	for _, item := range items {
		if kind, _ := item["kind"].(string); kind != "identity_observation" {
			continue
		}
		switch status, _ := item["status"].(string); status {
		case "passed":
			return "verified"
		case "failed":
			return "suspected_downgrade"
		default:
			return "unknown"
		}
	}
	return "unknown"
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

func endpointFingerprint(endpoint string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(endpoint)))
	return hex.EncodeToString(sum[:])
}
