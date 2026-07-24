package gatewaycircuitprojection

import (
	"context"
	"errors"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func TestIncidentProjectorRestoresExactCurrentSnapshot(t *testing.T) {
	incident := projectionIncident("scope-1", 3, 8)
	reader := &incidentReaderStub{load: port.GatewayAccountCircuitIncidentLoad{
		Status: port.GatewayAccountCircuitIncidentCurrent, CurrentDispatchRevision: 3, Incident: incident,
	}}
	restorer := &incidentRestorerStub{result: port.GatewayAccountCircuitRevisionProjection{
		Status: port.GatewayAccountCircuitRevisionApplied, CurrentRevision: 3,
	}}
	projector, _ := NewIncidentProjector(reader, restorer)
	result, err := projector.ProjectGatewayAccountCircuitIncident(context.Background(), projectionEvent("scope-1", 3, 8))
	if err != nil || result.Status != port.GatewayAccountCircuitRevisionApplied || result.IncidentID != incident.IncidentID || result.LedgerRevision != incident.LedgerRevision || restorer.calls != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, restorer.calls, err)
	}
}

func TestIncidentProjectorAcknowledgesObsoleteDispatchWithoutRestore(t *testing.T) {
	reader := &incidentReaderStub{load: port.GatewayAccountCircuitIncidentLoad{
		Status: port.GatewayAccountCircuitIncidentStale, CurrentDispatchRevision: 5,
	}}
	restorer := &incidentRestorerStub{}
	projector, _ := NewIncidentProjector(reader, restorer)
	result, err := projector.ProjectGatewayAccountCircuitIncident(context.Background(), projectionEvent("scope-1", 3, 8))
	if err != nil || result.Status != port.GatewayAccountCircuitRevisionStale || !result.Obsolete || result.CurrentRevision != 5 || restorer.calls != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, restorer.calls, err)
	}
}

func TestIncidentProjectorMissingSnapshotFailsForReplay(t *testing.T) {
	projector, _ := NewIncidentProjector(&incidentReaderStub{load: port.GatewayAccountCircuitIncidentLoad{Status: port.GatewayAccountCircuitIncidentMissing}}, &incidentRestorerStub{})
	if _, err := projector.ProjectGatewayAccountCircuitIncident(context.Background(), projectionEvent("scope-1", 3, 8)); err == nil {
		t.Fatal("missing durable incident must not be acknowledged")
	}
}

func TestIncidentRebuildUsesBoundedKeysetPages(t *testing.T) {
	now := time.Date(2026, 7, 24, 5, 0, 0, 0, time.UTC)
	first := projectionIncident("scope-1", 3, 8)
	first.UpdatedAt = now.Add(-time.Minute)
	second := projectionIncident("scope-2", 3, 9)
	second.UpdatedAt = now
	reader := &incidentReaderStub{pages: []port.GatewayAccountCircuitIncidentRebuildPage{
		{Items: []port.GatewayAccountCircuitIncident{first}, NextCursor: &port.GatewayAccountCircuitIncidentCursor{UpdatedAt: first.UpdatedAt, CircuitScopeKey: first.CircuitScopeKey}},
		{Items: []port.GatewayAccountCircuitIncident{second}},
	}}
	restorer := &incidentRestorerStub{result: port.GatewayAccountCircuitRevisionProjection{Status: port.GatewayAccountCircuitRevisionApplied, CurrentRevision: 3}}
	projector, _ := NewIncidentProjector(reader, restorer)
	result, err := projector.Rebuild(context.Background(), RebuildInput{Now: now, PageSize: 1, MaxPages: 3})
	if err != nil || result.Loaded != 2 || result.Pages != 2 || restorer.calls != 2 || len(reader.inputs) != 2 || reader.inputs[1].After == nil || reader.inputs[1].After.CircuitScopeKey != "scope-1" {
		t.Fatalf("result=%+v inputs=%+v calls=%d err=%v", result, reader.inputs, restorer.calls, err)
	}
}

func TestIncidentRebuildStopsOnRestoreFailure(t *testing.T) {
	want := errors.New("capacity")
	reader := &incidentReaderStub{pages: []port.GatewayAccountCircuitIncidentRebuildPage{{Items: []port.GatewayAccountCircuitIncident{projectionIncident("scope-1", 3, 8)}}}}
	projector, _ := NewIncidentProjector(reader, &incidentRestorerStub{err: want})
	if _, err := projector.Rebuild(context.Background(), RebuildInput{}); !errors.Is(err, want) {
		t.Fatalf("rebuild error=%v, want %v", err, want)
	}
}

func projectionEvent(scope string, revision, ledger int64) port.GatewayAccountCircuitOutboxEvent {
	return port.GatewayAccountCircuitOutboxEvent{
		EventID: "event-1", ProjectionKey: port.GatewayAccountCircuitProjectionKey,
		EventType: port.GatewayAccountCircuitIncidentChanged, AccountID: "account-1",
		AccountRuntimeKey: "account-1", CircuitScopeKey: scope, IncidentID: "incident-1",
		TransitionID: "transition-1", DispatchRevision: revision, Generation: 2, LedgerRevision: ledger,
	}
}

func projectionIncident(scope string, revision, ledger int64) port.GatewayAccountCircuitIncident {
	return port.GatewayAccountCircuitIncident{
		CircuitScopeKey: scope, AccountID: "account-1", AccountRuntimeKey: "account-1",
		ScopeKind: "account", IncidentID: "incident-1", State: "OPEN", Generation: 2,
		DispatchRevision: revision, LedgerRevision: ledger, TransitionID: "transition-1",
		UpdatedAt: time.Date(2026, 7, 24, 5, 0, 0, 0, time.UTC),
	}
}

type incidentReaderStub struct {
	load   port.GatewayAccountCircuitIncidentLoad
	err    error
	pages  []port.GatewayAccountCircuitIncidentRebuildPage
	inputs []port.GatewayAccountCircuitIncidentRebuildInput
}

func (s *incidentReaderStub) LoadGatewayAccountCircuitIncidentForProjection(context.Context, port.GatewayAccountCircuitOutboxEvent) (port.GatewayAccountCircuitIncidentLoad, error) {
	return s.load, s.err
}

func (s *incidentReaderStub) ListGatewayAccountCircuitIncidentsForRebuild(_ context.Context, input port.GatewayAccountCircuitIncidentRebuildInput) (port.GatewayAccountCircuitIncidentRebuildPage, error) {
	s.inputs = append(s.inputs, input)
	index := len(s.inputs) - 1
	if index >= len(s.pages) {
		return port.GatewayAccountCircuitIncidentRebuildPage{}, nil
	}
	return s.pages[index], s.err
}

type incidentRestorerStub struct {
	result port.GatewayAccountCircuitRevisionProjection
	err    error
	calls  int
}

func (s *incidentRestorerStub) RestoreGatewayAccountCircuitIncident(context.Context, port.GatewayAccountCircuitIncident) (port.GatewayAccountCircuitRevisionProjection, error) {
	s.calls++
	return s.result, s.err
}
