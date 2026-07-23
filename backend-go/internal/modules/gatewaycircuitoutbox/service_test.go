package gatewaycircuitoutbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestRunOnceProjectsAcknowledgesAndReleasesBoundedBatch(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	failedAt := now.Add(5 * time.Second)
	store := &outboxStoreStub{events: []port.GatewayAccountCircuitOutboxEvent{
		event("event-1", "claim-1", 2), event("event-2", "claim-2", 3),
	}}
	projector := &revisionProjectorStub{results: map[string]port.GatewayAccountCircuitRevisionProjection{
		"event-1": {Status: port.GatewayAccountCircuitRevisionApplied, CurrentRevision: 2, ClosedStates: 1},
	}, errByID: map[string]error{"event-2": errors.New("redis unavailable")}}
	service, _ := NewService(store, projector)
	service.WithNow(func() time.Time { return failedAt })
	result, err := service.RunOnce(context.Background(), RunOnceInput{OwnerID: "worker-1", Now: now, Limit: 2})
	if err == nil || result.Claimed != 2 || result.Projected != 1 || result.Applied != 1 || result.Acknowledged != 1 || result.Released != 1 || result.Failed != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(store.acks) != 1 || store.acks[0].EventID != "event-1" || len(store.releases) != 1 || store.releases[0].ErrorClass != "projection_failed" {
		t.Fatalf("acks=%+v releases=%+v", store.acks, store.releases)
	}
	if !store.releases[0].Now.Equal(failedAt) || !store.releases[0].Now.Add(store.releases[0].RetryDelay).After(failedAt) {
		t.Fatalf("release=%+v, want retry measured from actual failure time", store.releases[0])
	}
}

func TestRunOnceAcknowledgesStaleMonotonicProjection(t *testing.T) {
	store := &outboxStoreStub{events: []port.GatewayAccountCircuitOutboxEvent{event("event-1", "claim-1", 2)}}
	projector := &revisionProjectorStub{results: map[string]port.GatewayAccountCircuitRevisionProjection{
		"event-1": {Status: port.GatewayAccountCircuitRevisionStale, CurrentRevision: 4},
	}}
	service, _ := NewService(store, projector)
	result, err := service.RunOnce(context.Background(), RunOnceInput{OwnerID: "worker-1"})
	if err != nil || result.Stale != 1 || result.Acknowledged != 1 || len(store.acks) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunOnceTreatsLostLeaseAsNonFatalAndDoesNotReleaseAfterProjection(t *testing.T) {
	store := &outboxStoreStub{events: []port.GatewayAccountCircuitOutboxEvent{event("event-1", "claim-1", 2)}, loseAck: true}
	projector := &revisionProjectorStub{results: map[string]port.GatewayAccountCircuitRevisionProjection{
		"event-1": {Status: port.GatewayAccountCircuitRevisionIdempotent, CurrentRevision: 2},
	}}
	service, _ := NewService(store, projector)
	result, err := service.RunOnce(context.Background(), RunOnceInput{OwnerID: "worker-1"})
	if err != nil || result.LeaseLost != 1 || result.Acknowledged != 0 || len(store.releases) != 0 {
		t.Fatalf("result=%+v err=%v releases=%+v", result, err, store.releases)
	}
}

func TestRunOnceRejectsUnsafeBoundsBeforeClaim(t *testing.T) {
	service, _ := NewService(&outboxStoreStub{}, &revisionProjectorStub{})
	for _, input := range []RunOnceInput{{}, {OwnerID: "worker", Lease: time.Hour + 1}, {OwnerID: "worker", RetryDelay: 24*time.Hour + 1}, {OwnerID: "worker", Limit: 501}} {
		if _, err := service.RunOnce(context.Background(), input); err == nil {
			t.Fatalf("input=%+v error=nil", input)
		}
	}
}

func event(id, claim string, revision int64) port.GatewayAccountCircuitOutboxEvent {
	return port.GatewayAccountCircuitOutboxEvent{
		EventID: id, ProjectionKey: port.GatewayAccountCircuitProjectionKey,
		AccountID: "account-1", AccountRuntimeKey: "account-1", TransitionID: "transition-1",
		DispatchRevision: revision, ClaimToken: claim, AttemptCount: 1,
	}
}

type outboxStoreStub struct {
	events   []port.GatewayAccountCircuitOutboxEvent
	claimErr error
	acks     []port.GatewayAccountCircuitOutboxAcknowledgeInput
	releases []port.GatewayAccountCircuitOutboxReleaseInput
	loseAck  bool
}

func (s *outboxStoreStub) ClaimGatewayAccountCircuitOutbox(context.Context, port.GatewayAccountCircuitOutboxClaimInput) ([]port.GatewayAccountCircuitOutboxEvent, error) {
	return append([]port.GatewayAccountCircuitOutboxEvent(nil), s.events...), s.claimErr
}
func (s *outboxStoreStub) AcknowledgeGatewayAccountCircuitOutbox(_ context.Context, input port.GatewayAccountCircuitOutboxAcknowledgeInput) (bool, error) {
	s.acks = append(s.acks, input)
	if s.loseAck {
		return false, nil
	}
	return true, nil
}
func (s *outboxStoreStub) ReleaseGatewayAccountCircuitOutbox(_ context.Context, input port.GatewayAccountCircuitOutboxReleaseInput) (bool, error) {
	s.releases = append(s.releases, input)
	return true, nil
}

type revisionProjectorStub struct {
	results map[string]port.GatewayAccountCircuitRevisionProjection
	errByID map[string]error
}

func (s *revisionProjectorStub) ProjectGatewayAccountCircuitRevision(_ context.Context, event port.GatewayAccountCircuitOutboxEvent) (port.GatewayAccountCircuitRevisionProjection, error) {
	if err := s.errByID[event.EventID]; err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, err
	}
	return s.results[event.EventID], nil
}
