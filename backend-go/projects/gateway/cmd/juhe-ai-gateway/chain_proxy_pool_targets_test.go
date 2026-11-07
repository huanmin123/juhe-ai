package main

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-platform/proxyresolve"
)

type fakeProxyBatchResolver struct {
	targets map[string]proxyresolve.Target
	err     error
	calls   int
	ids     []string

	health       proxyresolve.HealthState
	healthSource string
	healthErr    error
	healthCalls  int
}

func (f *fakeProxyBatchResolver) ResolveBatch(_ context.Context, accountIDs []string) (map[string]proxyresolve.Target, error) {
	f.calls++
	f.ids = append([]string{}, accountIDs...)
	return f.targets, f.err
}

func (f *fakeProxyBatchResolver) CheckMemberHealth(_ context.Context, _ string) (proxyresolve.HealthState, string, error) {
	f.healthCalls++
	if f.health == "" {
		return proxyresolve.HealthDead, "redis", f.healthErr
	}
	return f.health, f.healthSource, f.healthErr
}

func poolCandidate() gatewaydispatch.AccountCandidate {
	poolID := "pool-1"
	profileID := "old-member"
	memberID := "old-member"
	url := "http://old.example:8080"
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                   "acc-1",
		ProxyPoolID:          &poolID,
		ProxyProfileID:       &profileID,
		ProxyMemberProfileID: &memberID,
		ProxyURL:             &url,
	}
}

func TestChainProxyPoolTargetResolverDirectPassThrough(t *testing.T) {
	batch := &fakeProxyBatchResolver{}
	resolver := newChainProxyPoolTargetResolver(batch, nil)
	account := gatewaydispatch.AccountCandidate{ID: "acc-direct"}
	got, err := resolver.ResolveAccountProxyTarget(context.Background(), account)
	if err != nil {
		t.Fatalf("direct pass-through error: %v", err)
	}
	if got.ProxyProfileID != nil || got.ProxyPoolID != nil || batch.calls != 0 {
		t.Fatalf("direct account must not be resolved: %+v calls=%d", got, batch.calls)
	}
}

func TestChainProxyPoolTargetResolverOverridesMember(t *testing.T) {
	batch := &fakeProxyBatchResolver{targets: map[string]proxyresolve.Target{
		"acc-1": {
			Kind:            proxyresolve.KindPoolMember,
			ProfileID:       "member-2",
			PoolID:          "pool-1",
			MemberProfileID: "member-2",
			URL:             "socks5h://user:pass@10.0.0.2:1080",
		},
	}}
	resolver := newChainProxyPoolTargetResolver(batch, nil)
	account := poolCandidate()
	unavailable := true
	account.ProxyProfileUnavailable = &unavailable
	account.ProxyProfileErrorMessage = stringPtr("旧的不可用")
	got, err := resolver.ResolveAccountProxyTarget(context.Background(), account)
	if err != nil {
		t.Fatalf("resolve pool target: %v", err)
	}
	if got.ProxyProfileID == nil || *got.ProxyProfileID != "member-2" ||
		got.ProxyMemberProfileID == nil || *got.ProxyMemberProfileID != "member-2" ||
		got.ProxyURL == nil || *got.ProxyURL != "socks5h://user:pass@10.0.0.2:1080" ||
		got.ProxyPoolID == nil || *got.ProxyPoolID != "pool-1" {
		t.Fatalf("target override mismatch: %+v", got)
	}
	if got.ProxyProfileUnavailable != nil || got.ProxyProfileErrorMessage != nil {
		t.Fatalf("healthy target must clear unavailable: %+v", got)
	}
	if len(batch.ids) != 1 || batch.ids[0] != "acc-1" {
		t.Fatalf("resolver ids = %v", batch.ids)
	}
}

func TestChainProxyPoolTargetResolverUnavailableAndError(t *testing.T) {
	cases := []struct {
		name   string
		batch  *fakeProxyBatchResolver
		reason string
	}{
		{
			name: "dead member",
			batch: &fakeProxyBatchResolver{targets: map[string]proxyresolve.Target{
				"acc-1": {UnavailableCode: proxyresolve.CodeProxyPoolUnavailable, UnavailableMessage: "代理池「P」无可用成员"},
			}},
			reason: "代理池「P」无可用成员",
		},
		{
			name:   "store failure",
			batch:  &fakeProxyBatchResolver{err: errors.New("db down")},
			reason: "代理池当前不可用，将在下一次请求重新分配",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := newChainProxyPoolTargetResolver(tc.batch, nil)
			got, err := resolver.ResolveAccountProxyTarget(context.Background(), poolCandidate())
			if err != nil {
				t.Fatalf("pool failure must become an unavailable candidate, not a request error: %v", err)
			}
			if got.ProxyProfileUnavailable == nil || !*got.ProxyProfileUnavailable {
				t.Fatalf("unavailable flag missing: %+v", got)
			}
			if got.ProxyProfileErrorMessage == nil || *got.ProxyProfileErrorMessage != tc.reason {
				t.Fatalf("message = %v, want %q", got.ProxyProfileErrorMessage, tc.reason)
			}
			if got.ProxyURL != nil || got.ProxyProfileID != nil || got.ProxyMemberProfileID != nil {
				t.Fatalf("unavailable target must not keep stale member/url: %+v", got)
			}
			if got.ProxyPoolID == nil || *got.ProxyPoolID != "pool-1" {
				t.Fatalf("pool id must stay for retry/diagnostics: %+v", got)
			}
		})
	}
}

func TestChainProxyPoolTargetResolverHealthyCacheFastPath(t *testing.T) {
	batch := &fakeProxyBatchResolver{health: proxyresolve.HealthDegraded, healthSource: "redis"}
	resolver := newChainProxyPoolTargetResolver(batch, nil)
	account := poolCandidate()
	got, err := resolver.ResolveAccountProxyTarget(context.Background(), account)
	if err != nil {
		t.Fatalf("fast path error: %v", err)
	}
	if batch.healthCalls != 1 || batch.calls != 0 {
		t.Fatalf("healthy cached member must only MGET health: health=%d full=%d", batch.healthCalls, batch.calls)
	}
	if got.ProxyURL == nil || *got.ProxyURL != "http://old.example:8080" ||
		got.ProxyProfileID == nil || *got.ProxyProfileID != "old-member" {
		t.Fatalf("fast path must keep cached member: %+v", got)
	}
}

func TestChainProxyPoolTargetResolverHealthErrorFailsClosed(t *testing.T) {
	batch := &fakeProxyBatchResolver{healthErr: errors.New("pg down")}
	resolver := newChainProxyPoolTargetResolver(batch, nil)
	got, err := resolver.ResolveAccountProxyTarget(context.Background(), poolCandidate())
	if err != nil {
		t.Fatalf("health error must fail closed, not error the request: %v", err)
	}
	if got.ProxyProfileUnavailable == nil || !*got.ProxyProfileUnavailable || got.ProxyURL != nil {
		t.Fatalf("health error must mark unavailable: %+v", got)
	}
	if batch.calls != 0 {
		t.Fatalf("must not full-resolve after health read failure: %d", batch.calls)
	}
}

func TestChainProxyPoolTargetResolverKeepsRepairedMemberLocally(t *testing.T) {
	batch := &fakeProxyBatchResolver{
		health: proxyresolve.HealthDead,
		targets: map[string]proxyresolve.Target{
			"acc-1": {Kind: proxyresolve.KindPoolMember, ProfileID: "member-2", PoolID: "pool-1",
				MemberProfileID: "member-2", URL: "http://new.example:8080", Assigned: true},
		},
	}
	resolver := newChainProxyPoolTargetResolver(batch, nil)
	first, err := resolver.ResolveAccountProxyTarget(context.Background(), poolCandidate())
	if err != nil || first.ProxyMemberProfileID == nil || *first.ProxyMemberProfileID != "member-2" {
		t.Fatalf("first resolution = %+v, %v", first, err)
	}
	// The cache still holds the old member (stale). Even if that old member
	// looks healthy again, the local override must keep the repaired member.
	batch.health = proxyresolve.HealthHealthy
	batch.calls = 0
	batch.healthCalls = 0
	second, err := resolver.ResolveAccountProxyTarget(context.Background(), poolCandidate())
	if err != nil {
		t.Fatalf("second resolution: %v", err)
	}
	if second.ProxyMemberProfileID == nil || *second.ProxyMemberProfileID != "member-2" {
		t.Fatalf("override member mismatch: %+v", second)
	}
	if batch.calls != 0 {
		t.Fatalf("override must avoid full resolve: %d", batch.calls)
	}
	// A refreshed cache with a different member is authoritative.
	refreshed := poolCandidate()
	newMember := "member-2"
	refreshed.ProxyMemberProfileID = &newMember
	batch.health = proxyresolve.HealthDead
	third, err := resolver.ResolveAccountProxyTarget(context.Background(), refreshed)
	if err != nil {
		t.Fatalf("third resolution: %v", err)
	}
	if batch.calls != 1 {
		t.Fatalf("refreshed cache must full-resolve once: %d", batch.calls)
	}
	if third.ProxyMemberProfileID == nil || *third.ProxyMemberProfileID != "member-2" {
		t.Fatalf("refreshed resolution = %+v", third)
	}
}

func TestChainCandidateEffectiveProxyBinding(t *testing.T) {
	null := func(value string) sql.NullString {
		if value == "" {
			return sql.NullString{}
		}
		return sql.NullString{String: value, Valid: true}
	}
	cases := []struct {
		name      string
		row       chainCandidateRow
		profileID string
		poolID    string
	}{
		{
			name: "source pool wins",
			row: chainCandidateRow{
				AuthorizationInstanceSourceAccountID: null("src"), ResourceProxyPoolID: null("pool-src"),
				ResourceProxyProfileID: null("src-profile"), ProxyProfileID: null("own-profile"), ProxyPoolID: null("own-pool"),
			},
			poolID: "pool-src",
		},
		{
			name: "source direct profile wins",
			row: chainCandidateRow{
				AuthorizationInstanceSourceAccountID: null("src"), ResourceProxyProfileID: null("src-profile"),
				ProxyProfileID: null("own-profile"), ProxyPoolID: null("ignored-pool"),
			},
			profileID: "src-profile",
		},
		{
			name: "instance own pool ignored",
			row: chainCandidateRow{
				AuthorizationInstanceSourceAccountID: null("src"), ProxyPoolID: null("own-pool"),
			},
		},
		{
			name: "instance falls back to own direct profile",
			row: chainCandidateRow{
				AuthorizationInstanceSourceAccountID: null("src"), ProxyProfileID: null("own-profile"),
			},
			profileID: "own-profile",
		},
		{
			name:   "ordinary pool",
			row:    chainCandidateRow{ProxyPoolID: null("pool-own")},
			poolID: "pool-own",
		},
		{
			name:      "ordinary direct profile",
			row:       chainCandidateRow{ProxyProfileID: null("own-profile")},
			profileID: "own-profile",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profileID, poolID := tc.row.effectiveProxyBinding()
			if profileID != tc.profileID || poolID != tc.poolID {
				t.Fatalf("binding = (%q, %q), want (%q, %q)", profileID, poolID, tc.profileID, tc.poolID)
			}
		})
	}
}
