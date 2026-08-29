package circuitruntime

import (
	"context"
	"time"
)

const GatewayAccountCircuitIncidentMaxPage = 500

type GatewayAccountCircuitIncident struct {
	CircuitScopeKey           string
	AccountID                 string
	AccountRuntimeKey         string
	ScopeKind                 string
	KeyFingerprint            string
	ProtocolCode              string
	RequestLane               string
	ModelFamily               string
	ClientModel               string
	CapabilityHash            string
	CredentialSourceAccountID string
	ClientEndpointFamily      string
	FinalUpstreamModel        string
	UpstreamEndpointMode      string
	IncidentID                string
	State                     string
	Generation                int
	DispatchRevision          int64
	LedgerRevision            int64
	TransitionID              string
	OpenUntil                 *time.Time
	NextTransitionAt          *time.Time
	LeaseID                   string
	LeasePurpose              string
	LeaseUntil                *time.Time
	BackoffLevel              int
	RecoveringSuccesses       int
	RetainedUntil             *time.Time
	UpdatedAt                 time.Time
}

type GatewayAccountCircuitIncidentLoadStatus string

const (
	GatewayAccountCircuitIncidentCurrent GatewayAccountCircuitIncidentLoadStatus = "current"
	GatewayAccountCircuitIncidentStale   GatewayAccountCircuitIncidentLoadStatus = "stale"
	GatewayAccountCircuitIncidentMissing GatewayAccountCircuitIncidentLoadStatus = "missing"
)

type GatewayAccountCircuitIncidentLoad struct {
	Status                  GatewayAccountCircuitIncidentLoadStatus
	CurrentDispatchRevision int64
	Incident                GatewayAccountCircuitIncident
}

type GatewayAccountCircuitIncidentCursor struct {
	UpdatedAt       time.Time
	CircuitScopeKey string
}

type GatewayAccountCircuitIncidentRebuildInput struct {
	Now   time.Time
	After *GatewayAccountCircuitIncidentCursor
	Limit int
}

type GatewayAccountCircuitIncidentRebuildPage struct {
	Items      []GatewayAccountCircuitIncident
	NextCursor *GatewayAccountCircuitIncidentCursor
}

type GatewayAccountCircuitIncidentReader interface {
	LoadGatewayAccountCircuitIncidentForProjection(context.Context, GatewayAccountCircuitOutboxEvent) (GatewayAccountCircuitIncidentLoad, error)
	ListGatewayAccountCircuitIncidentsForRebuild(context.Context, GatewayAccountCircuitIncidentRebuildInput) (GatewayAccountCircuitIncidentRebuildPage, error)
}

type GatewayAccountCircuitIncidentRestorer interface {
	RestoreGatewayAccountCircuitIncident(context.Context, GatewayAccountCircuitIncident) (GatewayAccountCircuitRevisionProjection, error)
}
