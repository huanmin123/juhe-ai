// URL security primitives ported from the archived Node
// shared/upstream-url-policy.ts (+ upstream-base-url-validator.ts) blocked
// range tables: the static private/reserved-IP assertion and the
// resolve-all + validated-dial guard (D-192/D-146, BUG-0175). The package
// stays transport-only: it knows IP ranges and DNS, never accounts or
// providers.
package upstreamhttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// UnsafeUpstreamURLError mirrors UnsafeUpstreamUrlError: the static URL
// contract (scheme / private or reserved literal / localhost name) failed.
type UnsafeUpstreamURLError struct{ Message string }

// Error implements error.
func (e *UnsafeUpstreamURLError) Error() string { return e.Message }

// UnsafeResolvedUpstreamURLError mirrors UnsafeResolvedUpstreamUrlError: a
// DNS resolution result hit a blocked range. Distinguished from the static
// error so the dispatch failure policy can tell "配置就非法" from "解析后被
// 重绑定/劫持到内网".
type UnsafeResolvedUpstreamURLError struct{ Message string }

// Error implements error.
func (e *UnsafeResolvedUpstreamURLError) Error() string { return e.Message }

// UnsafeUpstreamURLMessage mirrors the archived Node default message byte for
// byte so operator-facing failures stay identical.
const UnsafeUpstreamURLMessage = "上游 Base URL 不能指向本机、内网、链路本地或保留地址；本地联调请显式配置 JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST，只有临时回归才使用 JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS=true"

// blockedIpv4Range / blockedIpv6Range mirror the archived range tables.
type blockedIpv4Range struct {
	parts        [4]byte
	prefixLength int
}

type blockedIpv6Range struct {
	groups       [8]uint16
	prefixLength int
}

type blockedRangeInput struct {
	address      string
	prefixLength int
}

var blockedIpv4Ranges = mustBlockedIpv4Ranges([]blockedRangeInput{
	{"0.0.0.0", 8},
	{"10.0.0.0", 8},
	{"100.64.0.0", 10},
	{"127.0.0.0", 8},
	{"169.254.0.0", 16},
	{"172.16.0.0", 12},
	{"192.0.0.0", 24},
	{"192.0.2.0", 24},
	{"192.88.99.0", 24},
	{"192.168.0.0", 16},
	{"198.18.0.0", 15},
	{"198.51.100.0", 24},
	{"203.0.113.0", 24},
	{"224.0.0.0", 4},
	{"240.0.0.0", 4},
})

var blockedIpv6Ranges = mustBlockedIpv6Ranges([]blockedRangeInput{
	{"::", 128},
	{"::1", 128},
	{"::", 96},
	{"::ffff:0:0", 96},
	{"64:ff9b::", 96},
	{"64:ff9b:1::", 48},
	{"100::", 64},
	{"2001::", 23},
	{"2001:db8::", 32},
	{"2002::", 16},
	{"fc00::", 7},
	{"fe80::", 10},
	{"ff00::", 8},
})

func mustBlockedIpv4Ranges(inputs []blockedRangeInput) []blockedIpv4Range {
	ranges := make([]blockedIpv4Range, 0, len(inputs))
	for _, input := range inputs {
		ip := net.ParseIP(input.address)
		if ip == nil || ip.To4() == nil {
			panic(fmt.Sprintf("upstreamhttp: invalid blocked ipv4 range %q", input.address))
		}
		parts := ip.To4()
		ranges = append(ranges, blockedIpv4Range{parts: [4]byte(parts), prefixLength: input.prefixLength})
	}
	return ranges
}

func mustBlockedIpv6Ranges(inputs []blockedRangeInput) []blockedIpv6Range {
	ranges := make([]blockedIpv6Range, 0, len(inputs))
	for _, input := range inputs {
		ip := net.ParseIP(input.address)
		if ip == nil || ip.To16() == nil {
			panic(fmt.Sprintf("upstreamhttp: invalid blocked ipv6 range %q", input.address))
		}
		var groups [8]uint16
		sixteen := ip.To16()
		for i := 0; i < 8; i++ {
			groups[i] = uint16(sixteen[2*i])<<8 | uint16(sixteen[2*i+1])
		}
		ranges = append(ranges, blockedIpv6Range{groups: groups, prefixLength: input.prefixLength})
	}
	return ranges
}

// NormalizeHostToken mirrors normalizeHostToken.
func NormalizeHostToken(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Trim(value, "[]")))
}

// IsLocalhostName mirrors isLocalhostName.
func IsLocalhostName(hostname string) bool {
	return hostname == "localhost" || strings.HasSuffix(hostname, ".localhost")
}

// IsPrivateOrReservedIP mirrors isPrivateOrReservedIp: true for IPv4/IPv6
// literals inside the blocked tables; non-IP input is never blocked here.
func IsPrivateOrReservedIP(value string) bool {
	ip := net.ParseIP(NormalizeHostToken(value))
	if ip == nil {
		return false
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		var parts [4]byte
		copy(parts[:], ipv4)
		for _, blocked := range blockedIpv4Ranges {
			if ipv4MatchesPrefix(parts, blocked.parts, blocked.prefixLength) {
				return true
			}
		}
		return false
	}
	var groups [8]uint16
	sixteen := ip.To16()
	for i := 0; i < 8; i++ {
		groups[i] = uint16(sixteen[2*i])<<8 | uint16(sixteen[2*i+1])
	}
	for _, blocked := range blockedIpv6Ranges {
		if ipv6MatchesPrefix(groups, blocked.groups, blocked.prefixLength) {
			return true
		}
	}
	return false
}

func ipv4MatchesPrefix(parts, rangeParts [4]byte, prefixLength int) bool {
	remaining := prefixLength
	for i := 0; i < 4; i++ {
		if remaining <= 0 {
			return true
		}
		bits := min(8, remaining)
		mask := byte(^uint8(0) << (8 - bits))
		if parts[i]&mask != rangeParts[i]&mask {
			return false
		}
		remaining -= bits
	}
	return true
}

func ipv6MatchesPrefix(groups, rangeGroups [8]uint16, prefixLength int) bool {
	remaining := prefixLength
	for i := 0; i < 8; i++ {
		if remaining <= 0 {
			return true
		}
		bits := min(16, remaining)
		mask := uint16(^uint16(0) << (16 - bits))
		if groups[i]&mask != rangeGroups[i]&mask {
			return false
		}
		remaining -= bits
	}
	return true
}

// URLSecurityConfig mirrors runtimeConfig.upstreamUrlSecurity:
//   - AllowPrivateBaseUrls is JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS
//     semantics (the gateway runtime refuses it under the production signal);
//   - PrivateOriginAllowlist carries the normalized origin keys of
//     JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST (IP-only origins).
type URLSecurityConfig struct {
	AllowPrivateBaseUrls   bool
	PrivateOriginAllowlist map[string]bool
}

// IsAllowedPrivateOrigin mirrors isAllowedPrivateOrigin.
func (c URLSecurityConfig) IsAllowedPrivateOrigin(originKey string) bool {
	return c.PrivateOriginAllowlist[originKey]
}

// UpstreamOriginKey mirrors upstreamOriginKey: scheme://host:port with the
// scheme-default port made explicit.
func UpstreamOriginKey(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return u.Scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// NormalizePrivateUpstreamOrigin mirrors normalizePrivateUpstreamOrigin: the
// configured allowlist entry must be an http/https origin whose host is a
// bare IP (v4 or v6); the normalized origin key lowercases the host and makes
// the default port explicit.
func NormalizePrivateUpstreamOrigin(name, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%s 只能逐项填写完整的 http/https 私网 IP Origin：%s", name, value)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%s 只允许 http 或 https Origin：%s", name, value)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("%s 只能填写 Origin，不要包含路径、查询、片段或用户名密码：%s", name, value)
	}
	host := NormalizeHostToken(parsed.Hostname())
	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("%s 只允许 IP Origin，不接受域名：%s", name, value)
	}
	port := parsed.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return scheme + "://" + host + ":" + port, nil
}

// dialGuardSequence disambiguates guard instances inside client-pool keys.
var dialGuardSequence atomic.Uint64

// DialGuard is the request-time half of the DNS rebinding defense: every new
// upstream connection resolves the connect target itself, rejects the dial
// when any resolved address falls into a blocked range, and dials one of the
// validated addresses directly (pinning: no second resolution between check
// and connect). The archived Node implementation pinned the lookup result at
// request-prepare time via the http request `lookup` option; Go has no
// per-request dial hook, so the guard validates at the connection boundary
// instead — every socket is established against a validated address, which is
// the same guarantee applied per dial.
//
// With an HTTP(S) proxy the guarded transport dials the proxy host, so the
// guard validates the proxy target (the actual socket peer), matching the
// Node semantics of validating the connect target. SOCKS dialers keep their
// own resolution (socks5h is remote resolution by design).
//
// Allowlisted origins (JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST) skip the
// blocked-range rejection exactly like the archived implementation, while
// hostname targets still resolve-and-dial through the same validated path.
type DialGuard struct {
	config   URLSecurityConfig
	resolver Resolver
	dialer   *net.Dialer
	id       uint64

	// allowedHosts / allowedHostPorts are the scheme-agnostic projection of
	// the origin allowlist (entries are IP-only origins, so host and
	// host:port matching is unambiguous).
	allowedHosts     map[string]bool
	allowedHostPorts map[string]bool
}

// Resolver is the DNS surface used by the guard (mockable in tests).
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// NewDialGuard builds the guard. A nil resolver falls back to
// net.DefaultResolver; a nil dialer falls back to a 10s-timeout dialer (the
// transport's own response-header budget stays independent).
func NewDialGuard(config URLSecurityConfig, resolver Resolver, dialer *net.Dialer) *DialGuard {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if dialer == nil {
		dialer = &net.Dialer{Timeout: 10 * time.Second}
	}
	guard := &DialGuard{
		config:           config,
		resolver:         resolver,
		dialer:           dialer,
		id:               dialGuardSequence.Add(1),
		allowedHosts:     map[string]bool{},
		allowedHostPorts: map[string]bool{},
	}
	for originKey := range config.PrivateOriginAllowlist {
		parsed, err := url.Parse(originKey)
		if err != nil || parsed.Host == "" {
			continue
		}
		host := NormalizeHostToken(parsed.Hostname())
		port := parsed.Port()
		guard.allowedHosts[host] = true
		if port != "" {
			guard.allowedHostPorts[host+":"+port] = true
		}
	}
	return guard
}

// Key identifies the guard inside transport pool keys.
func (g *DialGuard) Key() string {
	if g == nil {
		return ""
	}
	return strconv.FormatUint(g.id, 10)
}

func (g *DialGuard) hostAllowlisted(host, port string) bool {
	if g.allowedHosts[host] {
		return true
	}
	if port != "" && g.allowedHostPorts[host+":"+port] {
		return true
	}
	// An allowlisted default port (80/443) also matches the portless form.
	if port == "" {
		return g.allowedHostPorts[host+":80"] || g.allowedHostPorts[host+":443"]
	}
	return false
}

func (g *DialGuard) rangeCheckAllowed(host, port string) bool {
	if g.config.AllowPrivateBaseUrls || g.hostAllowlisted(host, port) {
		return true
	}
	return false
}

// ValidateHost resolves host and rejects it when any resolved address is
// private or reserved (unless the deployment allowlisted the host or
// explicitly allowed private base URLs). IP-literal hosts are validated
// directly without DNS.
func (g *DialGuard) ValidateHost(ctx context.Context, host, port string) error {
	normalized := NormalizeHostToken(host)
	if g.rangeCheckAllowed(normalized, port) {
		return nil
	}
	if ip := net.ParseIP(normalized); ip != nil {
		if IsPrivateOrReservedIP(normalized) {
			return &UnsafeResolvedUpstreamURLError{Message: UnsafeUpstreamURLMessage}
		}
		return nil
	}
	addresses, err := g.resolver.LookupIPAddr(ctx, normalized)
	if err != nil {
		return err
	}
	for _, addr := range addresses {
		if addr.IP == nil {
			continue
		}
		if IsPrivateOrReservedIP(addr.IP.String()) {
			return &UnsafeResolvedUpstreamURLError{Message: UnsafeUpstreamURLMessage}
		}
	}
	return nil
}

// DialContext implements the transport dial hook: resolve the address,
// validate every candidate, then dial the validated IP directly so the
// checked address is the connected address.
func (g *DialGuard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("upstream dial guard: invalid address %q", address)
	}
	normalized := NormalizeHostToken(host)
	if g.rangeCheckAllowed(normalized, port) {
		return g.dialer.DialContext(ctx, network, address)
	}
	if ip := net.ParseIP(normalized); ip != nil {
		// Not allowlisted and a blocked literal: refuse the socket.
		if IsPrivateOrReservedIP(normalized) {
			return nil, &UnsafeResolvedUpstreamURLError{Message: UnsafeUpstreamURLMessage}
		}
		return g.dialer.DialContext(ctx, network, address)
	}
	addresses, err := g.resolver.LookupIPAddr(ctx, normalized)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: normalized, IsNotFound: true}
	}
	var lastErr error
	for _, addr := range addresses {
		if addr.IP == nil {
			continue
		}
		if IsPrivateOrReservedIP(addr.IP.String()) {
			return nil, &UnsafeResolvedUpstreamURLError{Message: UnsafeUpstreamURLMessage}
		}
		conn, dialErr := g.dialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = errors.New("upstream dial guard: no dialable validated address")
	}
	return nil, lastErr
}
