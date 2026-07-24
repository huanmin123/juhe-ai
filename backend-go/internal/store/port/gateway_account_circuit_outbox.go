package port

import (
	"context"
	"time"
)

const (
	GatewayAccountCircuitProjectionKey           = "account_circuit_runtime_v1"
	GatewayAccountCircuitOutboxMaxBatch          = 500
	GatewayAccountCircuitDispatchRevisionChanged = "dispatch_revision_changed"
	GatewayAccountCircuitIncidentChanged         = "incident_changed"
)

type GatewayAccountCircuitOutboxEvent struct {
	EventID           string
	ProjectionKey     string
	EventType         string
	AccountID         string
	AccountRuntimeKey string
	CircuitScopeKey   string
	IncidentID        string
	TransitionID      string
	DispatchRevision  int64
	Generation        int
	LedgerRevision    int64
	ClaimToken        string
	AttemptCount      int
	CreatedAt         time.Time
}

type GatewayAccountCircuitOutboxClaimInput struct {
	OwnerID string
	Now     time.Time
	Lease   time.Duration
	Limit   int
}

type GatewayAccountCircuitOutboxAcknowledgeInput struct {
	EventID                 string
	ProjectionKey           string
	ClaimToken              string
	AcknowledgedAt          time.Time
	Obsolete                bool
	ProjectedIncidentID     string
	ProjectedLedgerRevision int64
}

type GatewayAccountCircuitOutboxReleaseInput struct {
	EventID    string
	ClaimToken string
	ErrorClass string
	Now        time.Time
	RetryDelay time.Duration
}

type GatewayAccountCircuitOutboxStore interface {
	ClaimGatewayAccountCircuitOutbox(context.Context, GatewayAccountCircuitOutboxClaimInput) ([]GatewayAccountCircuitOutboxEvent, error)
	AcknowledgeGatewayAccountCircuitOutbox(context.Context, GatewayAccountCircuitOutboxAcknowledgeInput) (bool, error)
	ReleaseGatewayAccountCircuitOutbox(context.Context, GatewayAccountCircuitOutboxReleaseInput) (bool, error)
}

type GatewayAccountCircuitRevisionProjectionStatus string

const (
	GatewayAccountCircuitRevisionApplied    GatewayAccountCircuitRevisionProjectionStatus = "applied"
	GatewayAccountCircuitRevisionIdempotent GatewayAccountCircuitRevisionProjectionStatus = "idempotent"
	GatewayAccountCircuitRevisionStale      GatewayAccountCircuitRevisionProjectionStatus = "stale"
)

type GatewayAccountCircuitRevisionProjection struct {
	Status          GatewayAccountCircuitRevisionProjectionStatus
	CurrentRevision int64
	ClosedStates    int
	Obsolete        bool
	IncidentID      string
	LedgerRevision  int64
}

type GatewayAccountCircuitRevisionProjector interface {
	ProjectGatewayAccountCircuitRevision(context.Context, GatewayAccountCircuitOutboxEvent) (GatewayAccountCircuitRevisionProjection, error)
}

type GatewayAccountCircuitIncidentProjector interface {
	ProjectGatewayAccountCircuitIncident(context.Context, GatewayAccountCircuitOutboxEvent) (GatewayAccountCircuitRevisionProjection, error)
}
