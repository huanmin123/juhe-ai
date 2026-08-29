package circuitruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	IndexVersion    = "1"
	IndexOwnerMode  = "go-runtime-state-v1"
	DefaultCapacity = 100000
)

type OwnerGate struct{ Confirmed, SchemaReady, NodeWriterStopped bool }

func (g OwnerGate) Ready() bool { return g.Confirmed && g.SchemaReady && g.NodeWriterStopped }

type Config struct {
	URL, Namespace string
	Capacity       int
	Retention      time.Duration
}

type Store struct {
	client    *Client
	runtime   *AccountCircuitRuntimeStore
	gate      OwnerGate
	capacity  int
	retention time.Duration
}

func New(cfg Config, gate OwnerGate) (*Store, error) {
	if strings.TrimSpace(cfg.URL) == "" || strings.TrimSpace(cfg.Namespace) == "" {
		return nil, errors.New("account circuit runtime Redis URL and namespace are required")
	}
	if cfg.Capacity == 0 {
		cfg.Capacity = DefaultCapacity
	}
	if cfg.Retention == 0 {
		cfg.Retention = 5 * time.Minute
	}
	client, err := NewClient(cfg.URL, cfg.Namespace)
	if err != nil {
		return nil, err
	}
	runtime, err := NewAccountCircuitRuntimeStore(client, cfg.Retention, cfg.Capacity)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &Store{client: client, runtime: runtime, gate: gate, capacity: cfg.Capacity, retention: cfg.Retention}, nil
}

func (s *Store) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.client == nil {
		return errors.New("account circuit runtime Redis client is required")
	}
	return s.client.Ping(ctx)
}
func (s *Store) CheckReady(ctx context.Context) error {
	if s == nil || s.client == nil || s.runtime == nil || !s.gate.Ready() {
		return errors.New("account circuit runtime owner gate is not satisfied")
	}
	if err := s.client.Ping(ctx); err != nil {
		return err
	}
	meta, err := s.client.client.HMGet(ctx, s.runtime.keys.indexMeta, "version", "status", "ownerMode").Result()
	if err != nil {
		return err
	}
	if len(meta) != 3 || fmt.Sprint(meta[0]) != IndexVersion || fmt.Sprint(meta[1]) != "ready" || fmt.Sprint(meta[2]) != IndexOwnerMode {
		return errors.New("account circuit runtime index is not ready")
	}
	return nil
}

// Runtime exposes the complete Redis Lua state machine only after CheckReady
// has passed. Callers must remain inside the Gateway process.
func (s *Store) Runtime() (*AccountCircuitRuntimeStore, error) {
	if s == nil || s.runtime == nil {
		return nil, errors.New("account circuit runtime store is required")
	}
	if !s.gate.Ready() {
		return nil, errors.New("account circuit runtime owner gate is not satisfied")
	}
	return s.runtime, nil
}

func (s *Store) runtimeReady(ctx context.Context) (*AccountCircuitRuntimeStore, error) {
	if err := s.CheckReady(ctx); err != nil {
		return nil, err
	}
	return s.runtime, nil
}

func (s *Store) GetGatewayAccountCircuit(ctx context.Context, in GatewayAccountCircuitGetInput) (GatewayAccountCircuitState, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return GatewayAccountCircuitState{}, err
	}
	return r.GetGatewayAccountCircuit(ctx, in)
}
func (s *Store) SuspectGatewayAccountCircuit(ctx context.Context, in GatewayAccountCircuitSuspectInput) (GatewayAccountCircuitMutationResult, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	return r.SuspectGatewayAccountCircuit(ctx, in)
}
func (s *Store) AcquireGatewayAccountCircuitConfirmationLease(ctx context.Context, in GatewayAccountCircuitAcquireConfirmationLeaseInput) (GatewayAccountCircuitMutationResult, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	return r.AcquireGatewayAccountCircuitConfirmationLease(ctx, in)
}
func (s *Store) CompleteGatewayAccountCircuitConfirmation(ctx context.Context, in GatewayAccountCircuitCompleteConfirmationInput) (GatewayAccountCircuitMutationResult, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	return r.CompleteGatewayAccountCircuitConfirmation(ctx, in)
}
func (s *Store) AcquireGatewayAccountCircuitCanaryLease(ctx context.Context, in GatewayAccountCircuitAcquireCanaryLeaseInput) (GatewayAccountCircuitMutationResult, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	return r.AcquireGatewayAccountCircuitCanaryLease(ctx, in)
}
func (s *Store) CompleteGatewayAccountCircuitCanary(ctx context.Context, in GatewayAccountCircuitCompleteCanaryInput) (GatewayAccountCircuitMutationResult, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	return r.CompleteGatewayAccountCircuitCanary(ctx, in)
}
func (s *Store) ReplaceGatewayAccountCircuitDispatchRevision(ctx context.Context, in GatewayAccountCircuitReplaceDispatchRevisionInput) (GatewayAccountCircuitMutationResult, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	return r.ReplaceGatewayAccountCircuitDispatchRevision(ctx, in)
}
func (s *Store) RestoreGatewayAccountCircuit(ctx context.Context, in GatewayAccountCircuitRestoreInput) (GatewayAccountCircuitMutationResult, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	return r.RestoreGatewayAccountCircuit(ctx, in)
}
func (s *Store) ListDueGatewayAccountCircuits(ctx context.Context, in GatewayAccountCircuitListDueInput) ([]GatewayAccountCircuitState, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return nil, err
	}
	return r.ListDueGatewayAccountCircuits(ctx, in)
}
func (s *Store) RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx context.Context, in GatewayAccountCircuitProtocolModelOpenEvidenceInput) (GatewayAccountCircuitEscalationResult, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return GatewayAccountCircuitEscalationResult{}, err
	}
	return r.RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx, in)
}
func (s *Store) ClearGatewayAccountCircuitEscalationEvidence(ctx context.Context, in GatewayAccountCircuitClearAccountEscalationEvidenceInput) (bool, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return false, err
	}
	return r.ClearGatewayAccountCircuitEscalationEvidence(ctx, in)
}
func (s *Store) ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx context.Context, in GatewayAccountCircuitReplaceAccountDispatchRevisionInput) (GatewayAccountCircuitAccountRevisionResult, error) {
	r, err := s.runtimeReady(ctx)
	if err != nil {
		return GatewayAccountCircuitAccountRevisionResult{}, err
	}
	return r.ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx, in)
}

func (s *Store) BackfillRuntimeIndex(ctx context.Context, input GatewayAccountCircuitRuntimeIndexBackfillInput, reader GatewayAccountCircuitDispatchRevisionReader) (GatewayAccountCircuitRuntimeIndexBackfillResult, error) {
	if s == nil || s.client == nil || s.runtime == nil || !s.gate.Ready() {
		return GatewayAccountCircuitRuntimeIndexBackfillResult{}, errors.New("account circuit runtime owner gate is not satisfied")
	}
	if reader == nil {
		return GatewayAccountCircuitRuntimeIndexBackfillResult{}, errors.New("account circuit runtime revision reader is required")
	}
	backfiller, err := NewAccountCircuitRuntimeIndexBackfiller(s.client)
	if err != nil {
		return GatewayAccountCircuitRuntimeIndexBackfillResult{}, err
	}
	backfiller.WithDispatchRevisionReader(reader)
	return backfiller.BackfillGatewayAccountCircuitRuntimeIndex(ctx, input)
}

func (s *Store) ProjectRevision(ctx context.Context, event GatewayAccountCircuitOutboxEvent) (GatewayAccountCircuitRevisionProjection, error) {
	if err := s.CheckReady(ctx); err != nil {
		return GatewayAccountCircuitRevisionProjection{}, err
	}
	p, err := NewAccountCircuitRevisionProjector(s.client, s.retention)
	if err != nil {
		return GatewayAccountCircuitRevisionProjection{}, err
	}
	return p.ProjectGatewayAccountCircuitRevision(ctx, event)
}

func (s *Store) RestoreIncident(ctx context.Context, incident GatewayAccountCircuitIncident) (GatewayAccountCircuitRevisionProjection, error) {
	if err := s.CheckReady(ctx); err != nil {
		return GatewayAccountCircuitRevisionProjection{}, err
	}
	r, err := NewAccountCircuitRuntimeOwnerIncidentRestorer(s.client, s.retention, s.capacity)
	if err != nil {
		return GatewayAccountCircuitRevisionProjection{}, err
	}
	return r.RestoreGatewayAccountCircuitIncident(ctx, incident)
}
