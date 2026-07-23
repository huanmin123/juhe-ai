package port

import (
	"context"
	"time"
)

const (
	GatewayAccountCircuitProjectionKey  = "account_circuit_runtime_v1"
	GatewayAccountCircuitOutboxMaxBatch = 500
)

type GatewayAccountCircuitOutboxEvent struct {
	EventID           string
	ProjectionKey     string
	AccountID         string
	AccountRuntimeKey string
	TransitionID      string
	DispatchRevision  int64
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
	EventID        string
	ProjectionKey  string
	ClaimToken     string
	AcknowledgedAt time.Time
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
}

type GatewayAccountCircuitRevisionProjector interface {
	ProjectGatewayAccountCircuitRevision(context.Context, GatewayAccountCircuitOutboxEvent) (GatewayAccountCircuitRevisionProjection, error)
}
