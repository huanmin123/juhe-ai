package proxyresolve

import (
	"context"
	"errors"
	"strings"
)

// Resolver applies the shared account proxy-target contract. Store, health,
// decrypter, and the optional gateway lease writer are injected so the same
// logic is used by gateway and read-only jobs/OAuth/balance callers.
type Resolver struct {
	store     Store
	health    HealthReader
	decrypter PasswordDecrypter
	writer    LeaseWriter
}

type Options struct {
	Store     Store
	Health    HealthReader
	Decrypter PasswordDecrypter
	// Writer is nil for read-only processes. Only the gateway supplies it.
	Writer LeaseWriter
}

func New(options Options) (*Resolver, error) {
	if options.Store == nil {
		return nil, errors.New("proxyresolve store is required")
	}
	return &Resolver{
		store:     options.Store,
		health:    options.Health,
		decrypter: options.Decrypter,
		writer:    options.Writer,
	}, nil
}

// ResolveBatch resolves all requested accounts with bounded batched reads.
// The returned map always has one entry per unique non-empty account ID.
func (r *Resolver) ResolveBatch(ctx context.Context, accountIDs []string) (map[string]Target, error) {
	ids := uniqueNonEmpty(accountIDs)
	out := make(map[string]Target, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	bindings, err := r.store.LoadBindings(ctx, ids)
	if err != nil {
		return nil, err
	}

	directIDs := make([]string, 0, len(ids))
	poolIDs := make([]string, 0, len(ids))
	for _, accountID := range ids {
		binding, ok := bindings[accountID]
		if !ok {
			continue
		}
		switch kind, targetID := binding.Effective(); kind {
		case BindingProfile:
			directIDs = append(directIDs, targetID)
		case BindingPool:
			poolIDs = append(poolIDs, targetID)
		}
	}

	profiles, err := r.store.LoadProfiles(ctx, directIDs)
	if err != nil {
		return nil, err
	}
	pools, err := r.store.LoadPools(ctx, poolIDs)
	if err != nil {
		return nil, err
	}
	membersByPool, err := r.store.LoadPoolMembers(ctx, poolIDs)
	if err != nil {
		return nil, err
	}
	memberProfileIDs := make([]string, 0)
	for _, members := range membersByPool {
		for _, member := range members {
			memberProfileIDs = append(memberProfileIDs, member.ProfileID)
		}
	}
	memberProfiles, err := r.store.LoadProfiles(ctx, memberProfileIDs)
	if err != nil {
		return nil, err
	}
	for id, profile := range memberProfiles {
		if _, exists := profiles[id]; !exists {
			profiles[id] = profile
		}
	}

	leaseKeys := make([]string, 0, len(ids))
	for _, accountID := range ids {
		if binding, ok := bindings[accountID]; ok {
			if kind, _ := binding.Effective(); kind == BindingPool {
				leaseKeys = append(leaseKeys, binding.LeaseKey())
			}
		}
	}
	leases, err := r.store.LoadLeases(ctx, leaseKeys)
	if err != nil {
		return nil, err
	}
	leaseCounts, err := r.store.CountLeases(ctx, poolIDs)
	if err != nil {
		return nil, err
	}

	health, healthErr := r.readHealth(ctx, uniqueNonEmpty(memberProfileIDs))
	if healthErr != nil {
		// Redis is a fast projection; PG profile health remains the fallback.
		health = nil
	}

	for _, accountID := range ids {
		binding, ok := bindings[accountID]
		if !ok {
			out[accountID] = unavailable(CodeProxyUnavailable, "账户代理绑定不存在")
			continue
		}
		target, resolveErr := r.resolveOne(ctx, binding, profiles, pools, membersByPool, leases, leaseCounts, health)
		if resolveErr != nil {
			return nil, resolveErr
		}
		out[accountID] = target
	}
	return out, nil
}

func (r *Resolver) resolveOne(
	ctx context.Context,
	binding AccountBinding,
	profiles map[string]Profile,
	pools map[string]Pool,
	membersByPool map[string][]Member,
	leases map[string]Lease,
	leaseCounts map[string]map[string]int,
	health map[string]HealthState,
) (Target, error) {
	kind, targetID := binding.Effective()
	switch kind {
	case BindingNone:
		return Target{Kind: KindDirect}, nil
	case BindingConflict:
		return unavailable(CodeProxyTargetConflict, "代理与代理池不能同时选择"), nil
	case BindingProfile:
		return r.resolveProfile(ctx, targetID, profiles)
	case BindingPool:
		return r.resolvePool(ctx, binding, targetID, profiles, pools, membersByPool, leases, leaseCounts, health)
	default:
		return unavailable(CodeProxyUnavailable, "未知的代理绑定"), nil
	}
}

func (r *Resolver) resolveProfile(ctx context.Context, profileID string, profiles map[string]Profile) (Target, error) {
	profile, ok := profiles[profileID]
	if !ok || !profile.Enabled {
		return unavailable(CodeProxyUnavailable, "代理不存在或已停用，请选择一个已启用的代理"), nil
	}
	url, err := BuildProfileURL(ctx, profile, r.decrypter)
	if err != nil {
		return unavailable(CodeProxyUnavailable, "代理凭据不可用，请检查代理配置"), nil
	}
	return Target{Kind: KindProfile, ProfileID: profile.ID, URL: url}, nil
}

func (r *Resolver) resolvePool(
	ctx context.Context,
	binding AccountBinding,
	poolID string,
	profiles map[string]Profile,
	pools map[string]Pool,
	membersByPool map[string][]Member,
	leases map[string]Lease,
	leaseCounts map[string]map[string]int,
	health map[string]HealthState,
) (Target, error) {
	pool, ok := pools[poolID]
	if !ok {
		return unavailable(CodeProxyPoolUnavailable, "代理池不存在"), nil
	}
	if !pool.Enabled {
		return unavailable(CodeProxyPoolUnavailable, "代理池已停用"), nil
	}
	members := membersByPool[poolID]
	if len(members) == 0 {
		return unavailable(CodeProxyPoolUnavailable, "代理池没有可用成员"), nil
	}
	leaseKey := binding.LeaseKey()
	lease, hasLease := leases[leaseKey]
	if hasLease && lease.PoolID == poolID {
		if member, ok := findMember(members, lease.ProxyProfileID); ok && member.Enabled {
			if target, ok := r.resolveExistingLease(ctx, lease, profiles, health); ok {
				return target, nil
			}
		}
	}
	if r.writer == nil {
		return unavailable(CodeProxyPoolUnavailable, "池租约不可用，将在下一次 gateway 请求时修复"), nil
	}
	return r.assignMember(ctx, binding, pool, members, profiles, lease, hasLease, leaseCounts[poolID], health)
}

func (r *Resolver) resolveExistingLease(
	ctx context.Context,
	lease Lease,
	profiles map[string]Profile,
	health map[string]HealthState,
) (Target, bool) {
	profile, ok := profiles[lease.ProxyProfileID]
	if !ok || !profileAssignable(profile, health) {
		return Target{}, false
	}
	url, err := BuildProfileURL(ctx, profile, r.decrypter)
	if err != nil {
		return Target{}, false
	}
	return Target{
		Kind:            KindPoolMember,
		ProfileID:       profile.ID,
		PoolID:          lease.PoolID,
		MemberProfileID: profile.ID,
		URL:             url,
		Generation:      lease.Generation,
		HealthSource:    healthSourceOf(profile, health),
	}, true
}

func (r *Resolver) assignMember(
	ctx context.Context,
	binding AccountBinding,
	pool Pool,
	members []Member,
	profiles map[string]Profile,
	lease Lease,
	hasLease bool,
	leaseCounts map[string]int,
	health map[string]HealthState,
) (Target, error) {
	candidates := make([]Candidate, 0, len(members))
	for _, member := range members {
		profile, ok := profiles[member.ProfileID]
		if !ok {
			continue
		}
		candidates = append(candidates, Candidate{Member: member, Profile: profile})
	}
	selected, ok := SelectMember(pool.Strategy, candidates, leaseCounts, health)
	if !ok {
		return unavailable(CodeProxyPoolUnavailable, "代理池没有可分配成员"), nil
	}
	reason := AssignmentInitial
	var expected *Lease
	if hasLease {
		expected = &lease
		reason = AssignmentMemberUnavailable
		if _, found := findMember(members, lease.ProxyProfileID); !found {
			reason = AssignmentMemberRemoved
		}
		if lease.PoolID != pool.ID {
			reason = AssignmentPoolReassigned
		}
	}
	assigned, err := r.writer.AssignLease(ctx, binding.LeaseKey(), pool.ID, selected.Profile.ID, reason, expected)
	if err != nil {
		if errors.Is(err, ErrMemberUnavailable) {
			return unavailable(CodeProxyPoolUnavailable, "池成员状态已变化，将在下一次请求重新选择"), nil
		}
		return Target{}, err
	}
	profile, ok := profiles[assigned.ProxyProfileID]
	if !ok || !profile.Enabled {
		return unavailable(CodeProxyPoolUnavailable, "分配后的池成员不可用"), nil
	}
	url, err := BuildProfileURL(ctx, profile, r.decrypter)
	if err != nil {
		return unavailable(CodeProxyPoolUnavailable, "池成员代理凭据不可用"), nil
	}
	previousProfileID := ""
	if hasLease {
		previousProfileID = lease.ProxyProfileID
	}
	return Target{
		Kind:              KindPoolMember,
		ProfileID:         profile.ID,
		PoolID:            pool.ID,
		MemberProfileID:   profile.ID,
		URL:               url,
		Assigned:          true,
		PreviousProfileID: previousProfileID,
		AssignmentReason:  assigned.AssignmentReason,
		Generation:        assigned.Generation,
		HealthSource:      healthSourceOf(profile, health),
	}, nil
}

// healthSourceOf reports whether the effective health came from the Redis
// projection or the PG profile row. The resolver only consults Redis for pool
// member profiles.
func healthSourceOf(profile Profile, health map[string]HealthState) string {
	if state, ok := health[profile.ID]; ok && state != "" && state != HealthUnknown {
		return "redis"
	}
	return "pg"
}

// CheckMemberHealth returns the effective health of one pool member without
// loading bindings/pools/leases. It reads the Redis projection first; only when
// the key is missing/errors does it read the PG profile health (proxy design
// §7.4). Callers use this on the hot path to decide whether the cached member
// URL can be used as-is.
func (r *Resolver) CheckMemberHealth(ctx context.Context, profileID string) (HealthState, string, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return "", "", errors.New("代理成员 ID 为空")
	}
	if r.health != nil {
		states, err := r.health.ReadMemberHealth(ctx, []string{profileID})
		if err == nil {
			if state, ok := states[profileID]; ok && state != "" && state != HealthUnknown {
				return state, "redis", nil
			}
		}
	}
	profiles, err := r.store.LoadProfiles(ctx, []string{profileID})
	if err != nil {
		return "", "pg", err
	}
	profile, ok := profiles[profileID]
	if !ok {
		// A deleted profile is dead for assignment purposes (the caller then
		// runs the full resolver and fails closed / repairs).
		return HealthDead, "pg", nil
	}
	if profile.HealthFailureStreak >= deadFailureStreak {
		return HealthDead, "pg", nil
	}
	if profile.HealthStatus == "" {
		return HealthUnknown, "pg", nil
	}
	return profile.HealthStatus, "pg", nil
}

// ErrPoolUnavailable / ErrPoolEmpty are the write-path structural failures
// (proxy design §8.3/§6.1). A pool that has enabled members but none currently
// assignable by health is not an error: the account may be created without a
// lease and the first gateway request repairs it.
var (
	ErrPoolUnavailable = errors.New("proxy pool not found or disabled")
	ErrPoolEmpty       = errors.New("proxy pool has no enabled member")
)

// SelectPoolMember chooses the member for a new/rebound binding without writing
// a lease. The caller writes the account row and the lease in the same
// transaction (proxy design §6.1), so this method only reads through the
// injected Store (which may be a transaction-scoped adapter).
func (r *Resolver) SelectPoolMember(ctx context.Context, poolID string) (string, error) {
	poolID = strings.TrimSpace(poolID)
	if poolID == "" {
		return "", ErrPoolUnavailable
	}
	pools, err := r.store.LoadPools(ctx, []string{poolID})
	if err != nil {
		return "", err
	}
	pool, ok := pools[poolID]
	if !ok || !pool.Enabled {
		return "", ErrPoolUnavailable
	}
	members, err := r.store.LoadPoolMembers(ctx, []string{poolID})
	if err != nil {
		return "", err
	}
	profileIDs := make([]string, 0, len(members[poolID]))
	for _, member := range members[poolID] {
		profileIDs = append(profileIDs, member.ProfileID)
	}
	profiles, err := r.store.LoadProfiles(ctx, profileIDs)
	if err != nil {
		return "", err
	}
	structural := 0
	for _, member := range members[poolID] {
		profile, ok := profiles[member.ProfileID]
		if member.Enabled && ok && profile.Enabled {
			structural++
		}
	}
	if structural == 0 {
		return "", ErrPoolEmpty
	}
	counts, err := r.store.CountLeases(ctx, []string{poolID})
	if err != nil {
		return "", err
	}
	var health map[string]HealthState
	if r.health != nil && len(profileIDs) > 0 {
		if states, healthErr := r.health.ReadMemberHealth(ctx, profileIDs); healthErr == nil {
			health = states
		}
	}
	candidates := make([]Candidate, 0, len(members[poolID]))
	for _, member := range members[poolID] {
		if profile, ok := profiles[member.ProfileID]; ok {
			candidates = append(candidates, Candidate{Member: member, Profile: profile})
		}
	}
	selected, ok := SelectMember(pool.Strategy, candidates, counts[poolID], health)
	if !ok {
		return "", nil
	}
	return selected.Profile.ID, nil
}

func (r *Resolver) readHealth(ctx context.Context, profileIDs []string) (map[string]HealthState, error) {
	if r.health == nil || len(profileIDs) == 0 {
		return nil, nil
	}
	return r.health.ReadMemberHealth(ctx, profileIDs)
}

func unavailable(code, message string) Target {
	return Target{UnavailableCode: code, UnavailableMessage: message}
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func findMember(members []Member, profileID string) (Member, bool) {
	for _, member := range members {
		if member.ProfileID == profileID {
			return member, true
		}
	}
	return Member{}, false
}
