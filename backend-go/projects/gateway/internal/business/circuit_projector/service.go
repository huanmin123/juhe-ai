// Package circuitprojector owns the Gateway-local Business outbox consumer.
// It composes SQL and Redis in-process and never talks to Node or jobs.
package circuitprojector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	control "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_control_plane"
	runtime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/circuit_runtime"
)

type Service struct {
	control           *control.Store
	runtime           *runtime.Store
	ownerID           string
	lease, retryDelay time.Duration
}

func New(controlStore *control.Store, runtimeStore *runtime.Store, ownerID string) (*Service, error) {
	if controlStore == nil || runtimeStore == nil {
		return nil, errors.New("circuit projector stores are required")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return nil, errors.New("circuit projector owner id is required")
	}
	return &Service{control: controlStore, runtime: runtimeStore, ownerID: ownerID, lease: 30 * time.Second, retryDelay: time.Second}, nil
}

type RunResult struct{ Claimed, Projected, Acknowledged, Released, Failed int }

// BackfillRuntimeIndex is an explicit maintenance operation. It is never
// called by the steady-state projector loop; callers must provide the full
// writer-drained backfill contract to the runtime owner.
func (s *Service) BackfillRuntimeIndex(ctx context.Context, input runtime.GatewayAccountCircuitRuntimeIndexBackfillInput) (runtime.GatewayAccountCircuitRuntimeIndexBackfillResult, error) {
	if s == nil || s.control == nil || s.runtime == nil {
		return runtime.GatewayAccountCircuitRuntimeIndexBackfillResult{}, errors.New("circuit projector is not initialized")
	}
	return s.runtime.BackfillRuntimeIndex(ctx, input, DispatchRevisionReader{Store: s.control})
}

func (s *Service) RunOnce(ctx context.Context, now time.Time, limit int) (RunResult, error) {
	if s == nil || s.control == nil || s.runtime == nil {
		return RunResult{}, errors.New("circuit projector is not initialized")
	}
	if err := s.runtime.CheckReady(ctx); err != nil {
		return RunResult{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	events, err := s.control.ClaimOutbox(ctx, s.ownerID, now.UnixMilli(), s.lease.Milliseconds(), limit)
	if err != nil {
		return RunResult{}, fmt.Errorf("claim account circuit outbox: %w", err)
	}
	result := RunResult{Claimed: len(events)}
	for _, event := range events {
		projection, projectErr := s.project(ctx, event)
		if projectErr != nil {
			result.Failed++
			if _, releaseErr := s.control.ReleaseOutboxForReplay(ctx, event.EventID, value(event.ClaimToken), "projection_failed", now.UnixMilli(), s.retryDelay.Milliseconds()); releaseErr != nil {
				return result, errors.Join(projectErr, releaseErr)
			}
			result.Released++
			continue
		}
		result.Projected++
		ok, ackErr := s.control.AcknowledgeOutbox(ctx, event.EventID, event.ProjectionKey, value(event.ClaimToken), now.UnixMilli())
		if ackErr != nil {
			return result, ackErr
		}
		if ok {
			result.Acknowledged++
		} else {
			result.Failed++
		}
		_ = projection
	}
	return result, nil
}

func (s *Service) project(ctx context.Context, event control.Outbox) (runtime.GatewayAccountCircuitRevisionProjection, error) {
	converted := runtime.GatewayAccountCircuitOutboxEvent{EventID: event.EventID, ProjectionKey: event.ProjectionKey, EventType: event.EventType, AccountID: event.AccountID, AccountRuntimeKey: event.AccountRuntimeKey, TransitionID: event.TransitionID, DispatchRevision: event.DispatchRevision, AttemptCount: int(event.AttemptCount), CreatedAt: time.UnixMilli(event.CreatedAtMS).UTC()}
	if event.CircuitScopeKey != nil {
		converted.CircuitScopeKey = *event.CircuitScopeKey
	}
	if event.IncidentID != nil {
		converted.IncidentID = *event.IncidentID
	}
	if event.Generation != nil {
		converted.Generation = int(*event.Generation)
	}
	if event.LedgerRevision != nil {
		converted.LedgerRevision = int64(*event.LedgerRevision)
	}
	if event.ClaimToken != nil {
		converted.ClaimToken = *event.ClaimToken
	}
	if event.EventType == runtime.GatewayAccountCircuitDispatchRevisionChanged {
		return s.runtime.ProjectRevision(ctx, converted)
	}
	if event.EventType != runtime.GatewayAccountCircuitIncidentChanged {
		return runtime.GatewayAccountCircuitRevisionProjection{}, errors.New("unsupported circuit outbox event type")
	}
	loaded, err := s.control.LoadIncidentForProjection(ctx, event)
	if err != nil {
		return runtime.GatewayAccountCircuitRevisionProjection{}, err
	}
	if loaded.Status == "stale" {
		return runtime.GatewayAccountCircuitRevisionProjection{Status: runtime.GatewayAccountCircuitRevisionStale, CurrentRevision: loaded.CurrentDispatchRevision, Obsolete: true}, nil
	}
	if loaded.Status != "current" {
		return runtime.GatewayAccountCircuitRevisionProjection{}, errors.New("account circuit incident ledger snapshot is missing")
	}
	incident, err := convertIncident(loaded.Incident)
	if err != nil {
		return runtime.GatewayAccountCircuitRevisionProjection{}, err
	}
	projection, err := s.runtime.RestoreIncident(ctx, incident)
	if err != nil {
		return runtime.GatewayAccountCircuitRevisionProjection{}, err
	}
	if projection.Status != runtime.GatewayAccountCircuitRevisionStale {
		projection.IncidentID = incident.IncidentID
		projection.LedgerRevision = incident.LedgerRevision
	}
	return projection, nil
}

func convertIncident(v control.Incident) (runtime.GatewayAccountCircuitIncident, error) {
	if v.IncidentID == nil {
		return runtime.GatewayAccountCircuitIncident{}, errors.New("incident id is required")
	}
	return runtime.GatewayAccountCircuitIncident{
		CircuitScopeKey: v.CircuitScopeKey, AccountID: v.AccountID, AccountRuntimeKey: v.AccountRuntimeKey, ScopeKind: v.ScopeKind,
		KeyFingerprint: value(v.KeyFingerprint), ProtocolCode: value(v.ProtocolCode), RequestLane: value(v.RequestLane), ModelFamily: value(v.ModelFamily),
		ClientModel: value(v.ClientModel), CapabilityHash: value(v.CapabilityHash), CredentialSourceAccountID: value(v.CredentialSourceAccountID),
		ClientEndpointFamily: value(v.ClientEndpointFamily), FinalUpstreamModel: value(v.FinalUpstreamModel), UpstreamEndpointMode: value(v.UpstreamEndpointMode),
		IncidentID: value(v.IncidentID), State: v.State,
		Generation: int(v.Generation), DispatchRevision: v.DispatchRevision, LedgerRevision: v.LedgerRevision, TransitionID: v.TransitionID,
		OpenUntil: timePointer(v.OpenUntilMS), NextTransitionAt: timePointer(v.NextTransitionAtMS), LeaseID: value(v.LeaseID), LeasePurpose: value(v.LeasePurpose), LeaseUntil: timePointer(v.LeaseUntilMS),
		BackoffLevel: int(v.BackoffLevel), RecoveringSuccesses: int(v.RecoveringSuccesses), RetainedUntil: timePointer(v.RetainedUntilMS), UpdatedAt: time.UnixMilli(v.UpdatedAtMS).UTC(),
	}, nil
}

func timePointer(value *int64) *time.Time {
	if value == nil {
		return nil
	}
	parsed := time.UnixMilli(*value).UTC()
	return &parsed
}

func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
