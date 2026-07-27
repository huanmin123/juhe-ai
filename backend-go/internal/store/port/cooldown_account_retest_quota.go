package port

import (
	"context"
	"time"
)

const (
	CooldownAccountRetestQuotaAccessOwner      = "owner"
	CooldownAccountRetestQuotaAccessAuthorized = "authorized"
	CooldownAccountRetestQuotaAccessInvalid    = "invalid"
)

// CooldownAccountRetestQuotaSubject is the authorization and quota view needed
// to decide whether one due account may issue an upstream recovery probe.
type CooldownAccountRetestQuotaSubject struct {
	AccountID             string
	AccessType            string
	AuthorizationID       string
	SystemAccountID       string
	EffectiveSourceTeamID string
	AuthorizationValid    bool
	DirectLimits          ManagementRequestQuotaLimits
	TeamLimits            ManagementRequestQuotaLimits
}

type CooldownAccountRetestQuotaSubjectReader interface {
	LoadCooldownAccountRetestQuotaSubjects(
		context.Context,
		[]string,
		time.Time,
	) ([]CooldownAccountRetestQuotaSubject, error)
}

// GatewayQuotaCostReader is the narrow aggregate-reader contract shared by
// exact quota checks outside the periodic full gateway snapshot builder.
type GatewayQuotaCostReader interface {
	LoadGatewayQuotaSnapshotCosts(context.Context, []GatewayQuotaCostLookupInput) (map[string]GatewayQuotaCosts, error)
}
