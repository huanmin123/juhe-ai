package port

import (
	"context"
	"time"
)

const GatewayAccountCandidateScanLimit = 512

type GatewayGroupAccessType string

const (
	GatewayGroupAccessOwner      GatewayGroupAccessType = "owner"
	GatewayGroupAccessAuthorized GatewayGroupAccessType = "authorized"
)

type GatewayGroupAccessInput struct {
	GroupID         string
	SystemAccountID string
	Now             time.Time
}

type GatewayGroupAccess struct {
	GroupID                        string
	CallerSystemAccountID          string
	GroupOwnerSystemAccountID      string
	ProviderCode                   string
	AccessType                     GatewayGroupAccessType
	GroupType                      string
	SchedulingPolicyJSON           string
	GroupAuthorizationID           string
	GroupAuthorizationExpiresAt    *time.Time
	GroupAuthorizationLimitsJSON   string
	GroupAuthorizationSourceType   string
	GroupAuthorizationSourceTeamID string
}

type GatewayAccountCandidateListInput struct {
	Access             GatewayGroupAccess
	AccountID          string
	Now                time.Time
	IncludeUnavailable bool
	RequestedModel     string
	EndpointFamily     string
	Limit              int
}

// GatewayAccountCandidate is the bounded database projection used before
// credential/model/proxy hydration and runtime dispatch state are applied.
// A projection is not a dispatch lease: authorization must be revalidated at
// the final dispatch/claim boundary before credentials are sent upstream.
type GatewayAccountCandidate struct {
	AccountID                 string
	SystemAccountID           string
	GroupID                   string
	AccountAuthorizationID    string
	LocalPriority             int
	LocalSuperPriorityEnabled bool
	LocalFallbackEnabled      bool
	BindingCreatedAt          time.Time

	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Name                      string
	Type                      string
	Status                    string
	Schedulable               bool
	ConcurrencyLimit          int
	Priority                  int
	SuperPriorityEnabled      bool
	FallbackEnabled           bool
	ClientCompatibility       string
	CredentialsEncrypted      string
	ProxyProfileID            string
	AvailabilityScheduleJSON  string
	CooldownUntil             *time.Time
	AccountExpiresAt          *time.Time
	ConfigRevision            int
	DispatchRevision          int64

	AuthorizationSourceAccountID      string
	AuthorizationID                   string
	AuthorizationOwnerSystemAccountID string
	AuthorizationExpiresAt            *time.Time
	AuthorizationLimitsJSON           string
	AuthorizationSourceType           string
	AuthorizationSourceTeamID         string

	ResourceAccountID                 string
	ResourceProviderCode              string
	ResourceProviderProtocolProfileID string
	ResourceProtocolCode              string
	ResourceProtocolVersion           string
	ResourceType                      string
	ResourceStatus                    string
	ResourceSchedulable               bool
	ResourceCredentialsEncrypted      string
	ResourceProxyProfileID            string
	ResourceCooldownUntil             *time.Time
	ResourceAccountExpiresAt          *time.Time
	ResourceConcurrencyLimit          int
	ResourceClientCompatibility       string
	ResourceConfigRevision            int
	ResourceDispatchRevision          int64
	ModelRank                         int
}

type GatewayAccountCandidateReader interface {
	ResolveGatewayGroupAccess(context.Context, GatewayGroupAccessInput) (GatewayGroupAccess, bool, error)
	ListGatewayAccountCandidates(context.Context, GatewayAccountCandidateListInput) ([]GatewayAccountCandidate, error)
}
