package port

import (
	"context"
	"time"
)

type GatewayAccountPolicyAction string

const (
	GatewayAccountPolicyCooldown GatewayAccountPolicyAction = "cooldown"
	GatewayAccountPolicyDisable  GatewayAccountPolicyAction = "disable"
)

type GatewayAccountPolicyCooldownStatus string

const (
	GatewayAccountPolicyRateLimited          GatewayAccountPolicyCooldownStatus = "rate_limited"
	GatewayAccountPolicyTemporaryUnavailable GatewayAccountPolicyCooldownStatus = "temporary_unavailable"
)

// GatewayAccountPolicyRevisionFence freezes the account facts used by the
// upstream attempt. A writer must compare both revisions in the same
// transaction that applies the policy mutation.
type GatewayAccountPolicyRevisionFence struct {
	AccountID                string
	ExpectedConfigRevision   int
	ExpectedDispatchRevision int64
}

// GatewayAccountPolicyTarget identifies the local dispatch instance. For an
// authorized account this is deliberately distinct from Source: policy state
// belongs to the local authorization instance and must not poison every
// consumer of the physical credential source.
type GatewayAccountPolicyTarget struct {
	GatewayAccountPolicyRevisionFence
	SystemAccountID                   string
	GroupID                           string
	AccountAuthorizationID            string
	AuthorizationSourceAccountID      string
	AuthorizationOwnerSystemAccountID string
	AccountRuntimeKey                 string
	ExpectedStatus                    string
}

type GatewayAccountPolicyWriteInput struct {
	TransitionID   string
	Target         GatewayAccountPolicyTarget
	Source         GatewayAccountPolicyRevisionFence
	Action         GatewayAccountPolicyAction
	CooldownStatus GatewayAccountPolicyCooldownStatus
	CooldownUntil  *time.Time
	Reason         string
	TraceID        string
	AppliedAt      time.Time
}

type GatewayAccountPolicyWriteStatus string

const (
	GatewayAccountPolicyWriteApplied     GatewayAccountPolicyWriteStatus = "applied"
	GatewayAccountPolicyWriteIdempotent  GatewayAccountPolicyWriteStatus = "idempotent"
	GatewayAccountPolicyWriteStaleTarget GatewayAccountPolicyWriteStatus = "stale_target"
	GatewayAccountPolicyWriteStaleSource GatewayAccountPolicyWriteStatus = "stale_source"
	GatewayAccountPolicyWriteIneligible  GatewayAccountPolicyWriteStatus = "ineligible"
)

type GatewayAccountPolicyWriteResult struct {
	Status                 GatewayAccountPolicyWriteStatus
	TransitionID           string
	TargetDispatchRevision int64
	OutboxEventID          string
}

type GatewayAccountPolicyWriter interface {
	ApplyGatewayAccountPolicy(context.Context, GatewayAccountPolicyWriteInput) (GatewayAccountPolicyWriteResult, error)
}
