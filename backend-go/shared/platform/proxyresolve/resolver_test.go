package proxyresolve

import (
	"context"
	"errors"
	"testing"
)

type fakeStore struct {
	bindings map[string]AccountBinding
	profiles map[string]Profile
	pools    map[string]Pool
	members  map[string][]Member
	leases   map[string]Lease
	counts   map[string]map[string]int
}

func (s *fakeStore) LoadBindings(_ context.Context, ids []string) (map[string]AccountBinding, error) {
	out := map[string]AccountBinding{}
	for _, id := range ids {
		if binding, ok := s.bindings[id]; ok {
			out[id] = binding
		}
	}
	return out, nil
}

func (s *fakeStore) LoadProfiles(_ context.Context, ids []string) (map[string]Profile, error) {
	out := map[string]Profile{}
	for _, id := range ids {
		if profile, ok := s.profiles[id]; ok {
			out[id] = profile
		}
	}
	return out, nil
}

func (s *fakeStore) LoadPools(_ context.Context, ids []string) (map[string]Pool, error) {
	out := map[string]Pool{}
	for _, id := range ids {
		if pool, ok := s.pools[id]; ok {
			out[id] = pool
		}
	}
	return out, nil
}

func (s *fakeStore) LoadPoolMembers(_ context.Context, ids []string) (map[string][]Member, error) {
	out := map[string][]Member{}
	for _, id := range ids {
		if members, ok := s.members[id]; ok {
			out[id] = members
		}
	}
	return out, nil
}

func (s *fakeStore) LoadLeases(_ context.Context, keys []string) (map[string]Lease, error) {
	out := map[string]Lease{}
	for _, key := range keys {
		if lease, ok := s.leases[key]; ok {
			out[key] = lease
		}
	}
	return out, nil
}

func (s *fakeStore) CountLeases(_ context.Context, poolIDs []string) (map[string]map[string]int, error) {
	out := map[string]map[string]int{}
	for _, poolID := range poolIDs {
		if counts, ok := s.counts[poolID]; ok {
			out[poolID] = counts
		}
	}
	return out, nil
}

type fakeHealth struct {
	states map[string]HealthState
	err    error
}

func (h *fakeHealth) ReadMemberHealth(_ context.Context, _ []string) (map[string]HealthState, error) {
	return h.states, h.err
}

type fakeDecrypter struct{}

func (fakeDecrypter) DecryptProxyPassword(_ context.Context, encrypted string) (string, error) {
	return "pw-" + encrypted, nil
}

type fakeWriter struct {
	next     string
	err      error
	calls    int
	leaseKey string
	reason   string
	expected *Lease
}

func (w *fakeWriter) AssignLease(_ context.Context, leaseKey, poolID, profileID, reason string, expected *Lease) (Lease, error) {
	w.calls++
	w.leaseKey = leaseKey
	w.reason = reason
	w.expected = expected
	if w.err != nil {
		return Lease{}, w.err
	}
	if w.next != "" {
		profileID = w.next
	}
	return Lease{
		AccountID:        leaseKey,
		PoolID:           poolID,
		ProxyProfileID:   profileID,
		Generation:       1,
		AssignmentReason: reason,
	}, nil
}

func testProfile(id string, enabled bool, health HealthState) Profile {
	return Profile{ID: id, Type: "http", Host: "127.0.0.1", Port: 8080, Enabled: enabled, HealthStatus: health}
}

func testResolver(t *testing.T, store Store, health HealthReader, writer LeaseWriter) *Resolver {
	t.Helper()
	resolver, err := New(Options{Store: store, Health: health, Decrypter: fakeDecrypter{}, Writer: writer})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return resolver
}

func resolveTarget(t *testing.T, resolver *Resolver, accountID string) Target {
	t.Helper()
	targets, err := resolver.ResolveBatch(context.Background(), []string{accountID})
	if err != nil {
		t.Fatalf("ResolveBatch: %v", err)
	}
	target, ok := targets[accountID]
	if !ok {
		t.Fatalf("missing target for %s", accountID)
	}
	return target
}

func TestResolveDirectProfile(t *testing.T) {
	store := &fakeStore{
		bindings: map[string]AccountBinding{"a": {AccountID: "a", ConfiguredProfileID: "p1"}},
		profiles: map[string]Profile{"p1": testProfile("p1", true, HealthHealthy)},
	}
	target := resolveTarget(t, testResolver(t, store, nil, nil), "a")
	if target.Kind != KindProfile || target.ProfileID != "p1" || target.URL != "http://127.0.0.1:8080" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolveDirectProfileSocks5Auth(t *testing.T) {
	profile := testProfile("p1", true, HealthHealthy)
	profile.Type = "socks5"
	profile.Port = 1080
	profile.Username = "u"
	profile.PasswordEncrypted = "enc"
	store := &fakeStore{
		bindings: map[string]AccountBinding{"a": {AccountID: "a", ConfiguredProfileID: "p1"}},
		profiles: map[string]Profile{"p1": profile},
	}
	target := resolveTarget(t, testResolver(t, store, nil, nil), "a")
	if target.URL != "socks5h://u:pw-enc@127.0.0.1:1080" {
		t.Fatalf("unexpected URL: %q", target.URL)
	}
}

func TestResolvePoolExistingLease(t *testing.T) {
	store := &fakeStore{
		bindings: map[string]AccountBinding{"a": {AccountID: "a", ConfiguredPoolID: "pool1"}},
		pools:    map[string]Pool{"pool1": {ID: "pool1", Enabled: true, Strategy: "round_robin"}},
		members: map[string][]Member{"pool1": {
			{PoolID: "pool1", ProfileID: "m1", Priority: 0, Weight: 1, Enabled: true},
			{PoolID: "pool1", ProfileID: "m2", Priority: 1, Weight: 1, Enabled: true},
		}},
		profiles: map[string]Profile{
			"m1": testProfile("m1", true, HealthHealthy),
			"m2": testProfile("m2", true, HealthHealthy),
		},
		leases: map[string]Lease{"a": {AccountID: "a", PoolID: "pool1", ProxyProfileID: "m1"}},
	}
	target := resolveTarget(t, testResolver(t, store, nil, nil), "a")
	if target.Kind != KindPoolMember || target.PoolID != "pool1" || target.MemberProfileID != "m1" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolvePoolDeadLeaseReassigns(t *testing.T) {
	writer := &fakeWriter{next: "m2"}
	store := &fakeStore{
		bindings: map[string]AccountBinding{"a": {AccountID: "a", ConfiguredPoolID: "pool1"}},
		pools:    map[string]Pool{"pool1": {ID: "pool1", Enabled: true, Strategy: "round_robin"}},
		members: map[string][]Member{"pool1": {
			{PoolID: "pool1", ProfileID: "m1", Priority: 0, Weight: 1, Enabled: true},
			{PoolID: "pool1", ProfileID: "m2", Priority: 1, Weight: 1, Enabled: true},
		}},
		profiles: map[string]Profile{
			"m1": testProfile("m1", true, HealthHealthy),
			"m2": testProfile("m2", true, HealthHealthy),
		},
		leases: map[string]Lease{"a": {AccountID: "a", PoolID: "pool1", ProxyProfileID: "m1", Generation: 3}},
		counts: map[string]map[string]int{"pool1": map[string]int{"m1": 1, "m2": 0}},
	}
	health := &fakeHealth{states: map[string]HealthState{"m1": HealthDead, "m2": HealthHealthy}}
	target := resolveTarget(t, testResolver(t, store, health, writer), "a")
	if target.Kind != KindPoolMember || target.MemberProfileID != "m2" {
		t.Fatalf("unexpected target: %+v", target)
	}
	if writer.calls != 1 || writer.reason != AssignmentMemberUnavailable || writer.expected == nil {
		t.Fatalf("unexpected writer call: %+v", writer)
	}
}

func TestResolvePoolReadOnlyDeadLeaseUnavailable(t *testing.T) {
	store := &fakeStore{
		bindings: map[string]AccountBinding{"a": {AccountID: "a", ConfiguredPoolID: "pool1"}},
		pools:    map[string]Pool{"pool1": {ID: "pool1", Enabled: true, Strategy: "round_robin"}},
		members: map[string][]Member{"pool1": {
			{PoolID: "pool1", ProfileID: "m1", Priority: 0, Weight: 1, Enabled: true},
		}},
		profiles: map[string]Profile{"m1": testProfile("m1", true, HealthHealthy)},
		leases:   map[string]Lease{"a": {AccountID: "a", PoolID: "pool1", ProxyProfileID: "m1"}},
	}
	health := &fakeHealth{states: map[string]HealthState{"m1": HealthDead}}
	target := resolveTarget(t, testResolver(t, store, health, nil), "a")
	if target.UnavailableCode != CodeProxyPoolUnavailable {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolvePoolNoLeaseWriterInitial(t *testing.T) {
	writer := &fakeWriter{}
	store := &fakeStore{
		bindings: map[string]AccountBinding{"a": {AccountID: "a", ConfiguredPoolID: "pool1"}},
		pools:    map[string]Pool{"pool1": {ID: "pool1", Enabled: true, Strategy: "round_robin"}},
		members: map[string][]Member{"pool1": {
			{PoolID: "pool1", ProfileID: "m1", Priority: 0, Weight: 1, Enabled: true},
			{PoolID: "pool1", ProfileID: "m2", Priority: 1, Weight: 1, Enabled: true},
		}},
		profiles: map[string]Profile{
			"m1": testProfile("m1", true, HealthHealthy),
			"m2": testProfile("m2", true, HealthHealthy),
		},
		counts: map[string]map[string]int{"pool1": map[string]int{"m1": 1, "m2": 0}},
	}
	target := resolveTarget(t, testResolver(t, store, nil, writer), "a")
	if target.Kind != KindPoolMember || target.MemberProfileID != "m2" {
		t.Fatalf("unexpected target: %+v", target)
	}
	if writer.calls != 1 || writer.reason != AssignmentInitial || writer.expected != nil {
		t.Fatalf("unexpected writer call: %+v", writer)
	}
}

func TestResolvePoolNoAssignable(t *testing.T) {
	writer := &fakeWriter{}
	store := &fakeStore{
		bindings: map[string]AccountBinding{"a": {AccountID: "a", ConfiguredPoolID: "pool1"}},
		pools:    map[string]Pool{"pool1": {ID: "pool1", Enabled: true, Strategy: "round_robin"}},
		members: map[string][]Member{"pool1": {
			{PoolID: "pool1", ProfileID: "m1", Priority: 0, Weight: 1, Enabled: true},
		}},
		profiles: map[string]Profile{
			"m1": testProfile("m1", true, HealthDead),
		},
	}
	target := resolveTarget(t, testResolver(t, store, nil, writer), "a")
	if target.UnavailableCode != CodeProxyPoolUnavailable {
		t.Fatalf("unexpected target: %+v", target)
	}
	if writer.calls != 0 {
		t.Fatalf("writer must not be called without assignable members: %+v", writer)
	}
}

func TestResolveConflict(t *testing.T) {
	store := &fakeStore{
		bindings: map[string]AccountBinding{"a": {AccountID: "a", ConfiguredProfileID: "p1", ConfiguredPoolID: "pool1"}},
		profiles: map[string]Profile{"p1": testProfile("p1", true, HealthHealthy)},
		pools:    map[string]Pool{"pool1": {ID: "pool1", Enabled: true}},
	}
	target := resolveTarget(t, testResolver(t, store, nil, nil), "a")
	if target.UnavailableCode != CodeProxyTargetConflict {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolveSourcePoolOverridesInstanceProfile(t *testing.T) {
	store := &fakeStore{
		bindings: map[string]AccountBinding{"inst": {
			AccountID: "inst", SourceAccountID: "src", SourcePoolID: "pool1", ConfiguredProfileID: "instance-p1",
		}},
		pools: map[string]Pool{"pool1": {ID: "pool1", Enabled: true, Strategy: "round_robin"}},
		members: map[string][]Member{"pool1": {
			{PoolID: "pool1", ProfileID: "m1", Priority: 0, Weight: 1, Enabled: true},
		}},
		profiles: map[string]Profile{
			"m1":          testProfile("m1", true, HealthHealthy),
			"instance-p1": testProfile("instance-p1", true, HealthHealthy),
		},
		leases: map[string]Lease{"src": {AccountID: "src", PoolID: "pool1", ProxyProfileID: "m1"}},
	}
	target := resolveTarget(t, testResolver(t, store, nil, nil), "inst")
	if target.Kind != KindPoolMember || target.PoolID != "pool1" || target.MemberProfileID != "m1" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolveInstancePoolIgnored(t *testing.T) {
	store := &fakeStore{
		bindings: map[string]AccountBinding{"inst": {
			AccountID: "inst", SourceAccountID: "src", SourceProfileID: "src-p1", ConfiguredPoolID: "pool1",
		}},
		profiles: map[string]Profile{
			"src-p1": testProfile("src-p1", true, HealthHealthy),
		},
	}
	target := resolveTarget(t, testResolver(t, store, nil, nil), "inst")
	if target.Kind != KindProfile || target.ProfileID != "src-p1" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolveRedisHealthWinsOverPG(t *testing.T) {
	store := &fakeStore{
		bindings: map[string]AccountBinding{"a": {AccountID: "a", ConfiguredPoolID: "pool1"}},
		pools:    map[string]Pool{"pool1": {ID: "pool1", Enabled: true, Strategy: "round_robin"}},
		members: map[string][]Member{"pool1": {
			{PoolID: "pool1", ProfileID: "m1", Priority: 0, Weight: 1, Enabled: true},
		}},
		profiles: map[string]Profile{"m1": testProfile("m1", true, HealthDead)},
		leases:   map[string]Lease{"a": {AccountID: "a", PoolID: "pool1", ProxyProfileID: "m1"}},
	}
	health := &fakeHealth{states: map[string]HealthState{"m1": HealthHealthy}}
	target := resolveTarget(t, testResolver(t, store, health, nil), "a")
	if target.Kind != KindPoolMember || target.MemberProfileID != "m1" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestResolveHealthReadErrorFallsBackPG(t *testing.T) {
	writer := &fakeWriter{next: "m2"}
	store := &fakeStore{
		bindings: map[string]AccountBinding{"a": {AccountID: "a", ConfiguredPoolID: "pool1"}},
		pools:    map[string]Pool{"pool1": {ID: "pool1", Enabled: true, Strategy: "round_robin"}},
		members: map[string][]Member{"pool1": {
			{PoolID: "pool1", ProfileID: "m1", Priority: 0, Weight: 1, Enabled: true},
			{PoolID: "pool1", ProfileID: "m2", Priority: 1, Weight: 1, Enabled: true},
		}},
		profiles: map[string]Profile{
			"m1": testProfile("m1", true, HealthDead),
			"m2": testProfile("m2", true, HealthHealthy),
		},
		leases: map[string]Lease{"a": {AccountID: "a", PoolID: "pool1", ProxyProfileID: "m1"}},
	}
	health := &fakeHealth{err: errors.New("redis down")}
	target := resolveTarget(t, testResolver(t, store, health, writer), "a")
	if target.Kind != KindPoolMember || target.MemberProfileID != "m2" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestCheckMemberHealthFastPath(t *testing.T) {
	store := &fakeStore{profiles: map[string]Profile{
		"m1": testProfile("m1", true, HealthDegraded),
		"m2": testProfile("m2", true, HealthDead),
	}}
	resolver := testResolver(t, store, &fakeHealth{states: map[string]HealthState{"m1": HealthHealthy}}, nil)
	state, source, err := resolver.CheckMemberHealth(context.Background(), "m1")
	if err != nil || state != HealthHealthy || source != "redis" {
		t.Fatalf("redis health = (%v, %q, %v)", state, source, err)
	}
	// Missing Redis key falls back to the PG profile row.
	state, source, err = resolver.CheckMemberHealth(context.Background(), "m2")
	if err != nil || state != HealthDead || source != "pg" {
		t.Fatalf("pg fallback = (%v, %q, %v)", state, source, err)
	}
	// Deleted profile is dead (the full resolver then fail-closes).
	state, source, err = resolver.CheckMemberHealth(context.Background(), "missing")
	if err != nil || state != HealthDead || source != "pg" {
		t.Fatalf("missing profile = (%v, %q, %v)", state, source, err)
	}
}

func TestCheckMemberHealthRedisErrorFallsBackPG(t *testing.T) {
	store := &fakeStore{profiles: map[string]Profile{"m1": testProfile("m1", true, HealthHealthy)}}
	resolver := testResolver(t, store, &fakeHealth{err: errors.New("redis down")}, nil)
	state, source, err := resolver.CheckMemberHealth(context.Background(), "m1")
	if err != nil || state != HealthHealthy || source != "pg" {
		t.Fatalf("fallback = (%v, %q, %v)", state, source, err)
	}
}

func TestSelectPoolMemberForWritePath(t *testing.T) {
	store := &fakeStore{
		pools: map[string]Pool{
			"pool1": {ID: "pool1", Enabled: true, Strategy: "round_robin"},
			"off":   {ID: "off", Enabled: false, Strategy: "round_robin"},
		},
		members: map[string][]Member{
			"pool1": {
				{PoolID: "pool1", ProfileID: "m1", Priority: 0, Weight: 1, Enabled: true},
				{PoolID: "pool1", ProfileID: "m2", Priority: 1, Weight: 1, Enabled: true},
			},
			"off": {{PoolID: "off", ProfileID: "m1", Priority: 0, Weight: 1, Enabled: true}},
		},
		profiles: map[string]Profile{
			"m1": testProfile("m1", true, HealthDead),
			"m2": testProfile("m2", true, HealthHealthy),
		},
		counts: map[string]map[string]int{"pool1": {"m2": 3}},
	}
	resolver := testResolver(t, store, nil, nil)
	selected, err := resolver.SelectPoolMember(context.Background(), "pool1")
	if err != nil || selected != "m2" {
		t.Fatalf("selected = (%q, %v), want m2", selected, err)
	}
	if _, err := resolver.SelectPoolMember(context.Background(), "off"); !errors.Is(err, ErrPoolUnavailable) {
		t.Fatalf("disabled pool error = %v", err)
	}
	emptyStore := &fakeStore{
		pools:    map[string]Pool{"empty": {ID: "empty", Enabled: true}},
		members:  map[string][]Member{"empty": {{PoolID: "empty", ProfileID: "m1", Enabled: true}}},
		profiles: map[string]Profile{"m1": testProfile("m1", false, HealthHealthy)},
	}
	emptyResolver := testResolver(t, emptyStore, nil, nil)
	if _, err := emptyResolver.SelectPoolMember(context.Background(), "empty"); !errors.Is(err, ErrPoolEmpty) {
		t.Fatalf("structural empty error = %v", err)
	}
	allDeadStore := &fakeStore{
		pools:    map[string]Pool{"dead": {ID: "dead", Enabled: true}},
		members:  map[string][]Member{"dead": {{PoolID: "dead", ProfileID: "m1", Enabled: true}}},
		profiles: map[string]Profile{"m1": testProfile("m1", true, HealthDead)},
	}
	allDeadResolver := testResolver(t, allDeadStore, nil, nil)
	selected, err = allDeadResolver.SelectPoolMember(context.Background(), "dead")
	if err != nil || selected != "" {
		t.Fatalf("all-dead selection = (%q, %v), want empty/no error (create without lease)", selected, err)
	}
}
