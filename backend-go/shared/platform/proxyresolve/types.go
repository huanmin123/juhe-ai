// Package proxyresolve is the shared account proxy-target resolver for the
// proxy-pool design (docs/functions/代理池设计.md §5.3/§6). It owns the
// direct-profile / pool-member / no-proxy decision, member allocation, and the
// request-time lease-repair contract. Storage, Redis health, and password
// decryption are injected so gateway, jobs, OAuth, balance, and probe callers
// can share the same semantics without importing each other.
package proxyresolve

import "context"

// Kind is the resolved target kind.
type Kind string

const (
	KindDirect     Kind = "direct"
	KindProfile    Kind = "profile"
	KindPoolMember Kind = "pool_member"
)

// UnavailableCode mirrors the internal resolver reason. User-visible proxy
// attempts continue to use proxy_unavailable (pool reason is internal).
const (
	CodeProxyUnavailable     = "proxy_unavailable"
	CodeProxyPoolUnavailable = "proxy_pool_unavailable"
	CodeProxyTargetConflict  = "proxy_target_conflict"
)

// Target is the effective proxy target for one account. URL is empty only for
// KindDirect or an unavailable target.
type Target struct {
	Kind               Kind
	ProfileID          string
	PoolID             string
	MemberProfileID    string
	URL                string
	UnavailableCode    string
	UnavailableMessage string
	// Diagnostics for the gateway request log (proxy design §10.5). Zero values
	// mean the target was not assigned in this request / health came from PG.
	Assigned          bool
	PreviousProfileID string
	AssignmentReason  string
	Generation        int64
	HealthSource      string // "redis" | "pg"
}

func (t Target) Available() bool {
	return t.UnavailableCode == "" && (t.Kind == KindDirect || t.URL != "")
}

// BindingKind is the effective configured binding after source-account
// precedence (proxy design §5.2).
type BindingKind string

const (
	BindingNone     BindingKind = "none"
	BindingProfile  BindingKind = "profile"
	BindingPool     BindingKind = "pool"
	BindingConflict BindingKind = "conflict"
)

// AccountBinding carries the raw configured and source-account proxy columns.
// The store loads it; Effective applies the design precedence:
//   - authorization instance: source pool > source profile > instance profile;
//     the instance's own pool never participates.
//   - ordinary account: configured pool or configured profile (at most one).
type AccountBinding struct {
	AccountID       string
	SourceAccountID string

	ConfiguredProfileID string
	ConfiguredPoolID    string
	SourceProfileID     string
	SourcePoolID        string
}

// LeaseKey is the account that owns the pool lease: the source account for an
// authorization instance, otherwise the account itself.
func (b AccountBinding) LeaseKey() string {
	if b.SourceAccountID != "" {
		return b.SourceAccountID
	}
	return b.AccountID
}

// Effective resolves the configured target for this account.
func (b AccountBinding) Effective() (BindingKind, string) {
	if b.SourceAccountID != "" {
		// 授权实例以 source 绑定为准：source 有绑定（pool 或 profile）时，
		// 实例自身的两列完全不参与，即使实例行是双绑定损坏态也不影响。
		if b.SourcePoolID != "" && b.SourceProfileID != "" {
			return BindingConflict, ""
		}
		if b.SourcePoolID != "" {
			return BindingPool, b.SourcePoolID
		}
		if b.SourceProfileID != "" {
			return BindingProfile, b.SourceProfileID
		}
		// source 无绑定：回落实例自身 direct profile；实例自身 pool 恒不参与。
		// 实例行双绑定是数据损坏（DB CHECK 不可达），不静默选边。
		if b.ConfiguredProfileID != "" && b.ConfiguredPoolID != "" {
			return BindingConflict, ""
		}
		if b.ConfiguredProfileID != "" {
			return BindingProfile, b.ConfiguredProfileID
		}
		return BindingNone, ""
	}
	// The DB CHECK forbids both configured columns on one row. A corrupt row
	// is never silently resolved by precedence.
	if b.ConfiguredProfileID != "" && b.ConfiguredPoolID != "" {
		return BindingConflict, ""
	}
	if b.ConfiguredPoolID != "" {
		return BindingPool, b.ConfiguredPoolID
	}
	if b.ConfiguredProfileID != "" {
		return BindingProfile, b.ConfiguredProfileID
	}
	return BindingNone, ""
}

// Profile is a proxy_profiles row needed by the resolver.
type Profile struct {
	ID                string
	Name              string
	Type              string
	Host              string
	Port              int
	Username          string
	PasswordEncrypted string
	Enabled           bool
	HealthStatus      HealthState
	// HealthFailureStreak is the PG consecutive pool-probe failure count.
	// §6.2 requires both status != dead and streak < 3; the Redis projection
	// only carries the status, so streak is applied only on the PG fallback.
	HealthFailureStreak int
}

// Pool is a proxy_pools row.
type Pool struct {
	ID       string
	Name     string
	Strategy string
	Enabled  bool
}

// Member is a proxy_pool_members row.
type Member struct {
	PoolID    string
	ProfileID string
	Priority  int
	Weight    int
	Enabled   bool
}

// Lease is an account_proxy_leases row.
type Lease struct {
	AccountID        string
	PoolID           string
	ProxyProfileID   string
	Generation       int64
	AssignmentReason string
}

const (
	AssignmentInitial           = "initial"
	AssignmentMemberUnavailable = "member_unavailable"
	AssignmentMemberRemoved     = "member_removed"
	AssignmentPoolReassigned    = "pool_reassigned"
)

// HealthState is the pool member availability projection.
type HealthState string

const (
	HealthHealthy  HealthState = "healthy"
	HealthDegraded HealthState = "degraded"
	HealthDead     HealthState = "dead"
	HealthUnknown  HealthState = "unknown"
)

// HealthReader is the Redis member-health fast projection (C7-R). A missing
// key is not an error; callers fall back to the PG profile health.
type HealthReader interface {
	ReadMemberHealth(ctx context.Context, profileIDs []string) (map[string]HealthState, error)
}

// PasswordDecrypter decrypts a proxy_profiles.password_encrypted envelope.
type PasswordDecrypter interface {
	DecryptProxyPassword(ctx context.Context, encrypted string) (string, error)
}

// Store is the batched PG read contract. Implementations must be safe for one
// resolver call to load all accounts/pools in a bounded number of queries.
type Store interface {
	LoadBindings(ctx context.Context, accountIDs []string) (map[string]AccountBinding, error)
	LoadProfiles(ctx context.Context, profileIDs []string) (map[string]Profile, error)
	LoadPools(ctx context.Context, poolIDs []string) (map[string]Pool, error)
	LoadPoolMembers(ctx context.Context, poolIDs []string) (map[string][]Member, error)
	LoadLeases(ctx context.Context, leaseKeys []string) (map[string]Lease, error)
	CountLeases(ctx context.Context, poolIDs []string) (map[string]map[string]int, error)
}

// LeaseWriter is the gateway-only write contract. Read-only processes pass
// nil: an invalid/missing lease then resolves to proxy_pool_unavailable and is
// repaired by the next gateway request.
type LeaseWriter interface {
	AssignLease(ctx context.Context, leaseKey, poolID, profileID, reason string, expected *Lease) (Lease, error)
}
