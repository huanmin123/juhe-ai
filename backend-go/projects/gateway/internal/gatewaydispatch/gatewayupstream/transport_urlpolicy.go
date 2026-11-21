package gatewayupstream

// Request-time upstream URL security ported from the archived Node
// shared/upstream-url-policy.ts prepareSafeUpstreamRequestUrl (D-192/D-146,
// BUG-0175): the Go composition previously left TransportDeps.URLPolicy on
// the PassthroughUpstreamURLPolicy, so UnsafeResolvedUpstreamURLError had
// consumers but no producer and the DNS-rebinding half of the SSRF defense
// was missing (the static check only ran in the account-save path).
//
// Semantics mirrored from the archive:
//   - http/https URLs only; the URL must carry a host;
//   - a localhost name or a private/reserved IP literal fails with the
//     static UnsafeUpstreamURLError unless the origin is allowlisted or the
//     deployment explicitly allowed private base URLs;
//   - in strict mode, a hostname target resolves and is accepted when at
//     least one resolved address clears the blocked-range tables (mixed
//     public/blocked answers pass); only an all-blocked resolution produces
//     the resolved variant of the error;
//   - in strict mode, the dial side pins through sharedupstreamhttp.DialGuard
//     so every locally selected socket address is established against a
//     validated address; local-resolution SOCKS5 only sends a validated
//     CONNECT target, while socks5h keeps remote resolution.
//
// Permissive targets (allowlisted origin or AllowPrivateBaseUrls) keep the
// archive's historical behavior: prepare-time validation short-circuits
// without DNS and the direct/HTTP(S) connection boundary uses the plain
// dialer (local-resolution SOCKS5 keeps its historical local resolution), so
// a deployment that explicitly allows private upstreams does not acquire a
// new resolver dependency. Strict targets are resolved at prepare time with
// allow-mixed semantics and re-resolved/pinned by DialGuard at dial time.

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"

	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

// UnsafeUpstreamURLError mirrors shared/upstream-url-policy.ts
// UnsafeUpstreamUrlError (the static, pre-DNS failure).
type UnsafeUpstreamURLError struct{ Message string }

// Error implements error.
func (e *UnsafeUpstreamURLError) Error() string { return e.Message }

// ResolvedUpstreamURLPolicy implements UpstreamURLPolicy over the shared
// platform URL-security facts.
type ResolvedUpstreamURLPolicy struct {
	config sharedupstreamhttp.URLSecurityConfig
	guard  *sharedupstreamhttp.DialGuard
}

// NewResolvedUpstreamURLPolicy builds the resolve-all policy. The guard is
// shared with TransportDeps.DialGuard so prepare-time validation and
// dial-time pinning use the same configuration.
func NewResolvedUpstreamURLPolicy(config sharedupstreamhttp.URLSecurityConfig) *ResolvedUpstreamURLPolicy {
	return &ResolvedUpstreamURLPolicy{
		config: config,
		guard:  sharedupstreamhttp.NewDialGuard(config, nil, nil),
	}
}

// Guard exposes the dial guard to wire into TransportDeps.
func (p *ResolvedUpstreamURLPolicy) Guard() *sharedupstreamhttp.DialGuard {
	if p == nil {
		return nil
	}
	return p.guard
}

// PrepareSafeUpstreamRequestURL implements UpstreamURLPolicy.
func (p *ResolvedUpstreamURLPolicy) PrepareSafeUpstreamRequestURL(ctx context.Context, rawURL string) (*url.URL, error) {
	if p == nil || p.guard == nil {
		return PassthroughUpstreamURLPolicy{}.PrepareSafeUpstreamRequestURL(ctx, rawURL)
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return nil, &UnsafeUpstreamURLError{Message: "上游 Base URL 格式无效"}
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, &UnsafeUpstreamURLError{Message: "上游 Base URL 只允许 http 或 https 协议"}
	}
	host := sharedupstreamhttp.NormalizeHostToken(parsed.Hostname())
	allowlistedOrigin := p.config.IsAllowedPrivateOrigin(sharedupstreamhttp.UpstreamOriginKey(parsed))
	// Static half (assertSafeUpstreamUrl): the check returns early for
	// allowlisted origins and is skipped entirely with allowPrivateBaseUrls.
	if !p.config.AllowPrivateBaseUrls && !allowlistedOrigin {
		if sharedupstreamhttp.IsLocalhostName(host) || sharedupstreamhttp.IsPrivateOrReservedIP(host) {
			return nil, &UnsafeUpstreamURLError{Message: sharedupstreamhttp.UnsafeUpstreamURLMessage}
		}
	}
	// Resolve half (archive: lookup(host, {all: true, verbatim: true}) with
	// the fixed-lookup pin): IP-literal URLs skip DNS; strict hostname targets
	// go through the guard, which accepts them when at least one resolved
	// address is dialable (mixed answers pass; all-blocked answers are
	// rejected). Permissive targets (allowlisted host / AllowPrivateBaseUrls)
	// short-circuit without resolution; their plain dial behavior is unchanged.
	if net.ParseIP(host) == nil {
		if err := p.guard.ValidateHost(ctx, host, parsed.Port()); err != nil {
			var resolvedErr *sharedupstreamhttp.UnsafeResolvedUpstreamURLError
			if errors.As(err, &resolvedErr) {
				return nil, &UnsafeResolvedUpstreamURLError{Message: resolvedErr.Message}
			}
			return nil, err
		}
	}
	return parsed, nil
}

// BoundedConcurrencyGovernor is the process-local ConcurrencyGovernor port of
// the archived shared/concurrency-governor.ts acquireGlobalConcurrencySlot
// memory-driver branch (pLimit(runtimeConfig.concurrency.globalMax)): at most
// capacity upstream requests are in flight; the rest block until a slot is
// released or the request context is done.
//
// Residual (registered): the Node performance topology shares one capacity
// across replicas through a Redis ZSET lease (redis runtime-state driver);
// this port enforces the per-process slot only. The composition sizes it
// with JUHE_AI_CONCURRENCY_GLOBAL_MAX so single-process deployments get the
// exact Node budget and multi-process deployments keep at least the same
// per-process bound.
type BoundedConcurrencyGovernor struct {
	semaphore chan struct{}
}

// NewBoundedConcurrencyGovernor builds the governor; a capacity below one
// returns nil so degenerate configs keep the Nop semantics instead of
// deadlocking the dispatch loop (Acquire on a nil governor is unlimited).
func NewBoundedConcurrencyGovernor(capacity int) *BoundedConcurrencyGovernor {
	if capacity < 1 {
		return nil
	}
	return &BoundedConcurrencyGovernor{semaphore: make(chan struct{}, capacity)}
}

// Acquire implements ConcurrencyGovernor.
func (g *BoundedConcurrencyGovernor) Acquire(ctx context.Context) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	select {
	case <-ctx.Done():
		return nil, &UpstreamRequestAbortedError{Message: "全局并发槽获取已取消"}
	case g.semaphore <- struct{}{}:
		acquired := g.semaphore
		released := false
		return func() {
			if released {
				return
			}
			released = true
			<-acquired
		}, nil
	}
}
