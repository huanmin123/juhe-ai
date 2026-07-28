// Package upstreamurlpolicy validates upstream HTTP(S) URLs and prepares a
// connection plan whose addresses cannot change between validation and dial.
package upstreamurlpolicy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const unsafeURLMessage = "上游 Base URL 不能指向本机、内网、链路本地或保留地址；本地联调请显式配置 JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST，只有临时回归才使用 JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS=true"

var (
	absURLPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*):\/\/`)
	hexPattern    = regexp.MustCompile(`^[0-9A-Fa-f]{2}$`)

	blockedPrefixes = mustPrefixes(
		"0.0.0.0/8",
		"10.0.0.0/8",
		"100.64.0.0/10",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"172.16.0.0/12",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"192.88.99.0/24",
		"192.168.0.0/16",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"224.0.0.0/4",
		"240.0.0.0/4",
		"::/128",
		"::1/128",
		"::/96",
		"::ffff:0:0/96",
		"64:ff9b::/96",
		"64:ff9b:1::/48",
		"100::/64",
		"2001::/23",
		"2001:db8::/32",
		"2002::/16",
		"fc00::/7",
		"fe80::/10",
		"ff00::/8",
	)
)

// Resolver is the net.Resolver seam used by PrepareRequestURL. Implementations
// must return every address for the requested host.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialContextFunc matches net.Dialer's context-aware dial method.
type DialContextFunc func(context.Context, string, string) (net.Conn, error)

// Config controls the narrow exceptions to the default public-origin policy.
// The account contract accepts both HTTP and HTTPS public origins; private and
// reserved targets still require an exact allowlist entry.
type Config struct {
	AllowPrivateBaseURLs    bool
	PrivateBaseURLAllowlist []string
	Production              bool
	Resolver                Resolver
	DialContext             DialContextFunc
}

// UnsafeURLError reports a URL that crosses the upstream SSRF boundary.
type UnsafeURLError struct {
	Message string
	Cause   error
}

func (e *UnsafeURLError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return unsafeURLMessage
}

func (e *UnsafeURLError) Unwrap() error { return e.Cause }

// DialPlan is immutable from the caller's perspective. URL returns a copy, and
// DialContext only accepts the original URL's host and effective port.
type DialPlan struct {
	requestURL url.URL
	host       string
	port       string
	addresses  []netip.Addr
	dial       DialContextFunc
}

// URL returns a copy of the validated request URL.
func (p *DialPlan) URL() *url.URL {
	copyURL := p.requestURL
	return &copyURL
}

// Addresses returns a copy of the addresses fixed into this plan.
func (p *DialPlan) Addresses() []netip.Addr {
	return slices.Clone(p.addresses)
}

// DialContext dials only a previously resolved address for the validated
// origin. Attaching this method to http.Transport.DialContext preserves the URL
// hostname for Host/SNI while preventing a second DNS lookup at connect time.
func (p *DialPlan) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if ctx == nil {
		return nil, fmt.Errorf("上游拨号 context 不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("上游拨号网络 %q 不受支持", network)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("上游拨号目标格式无效 %q: %w", address, err)
	}
	canonicalPort, err := normalizePort(port)
	if err != nil || !sameHost(host, p.host) || canonicalPort != p.port {
		return nil, fmt.Errorf("上游拨号目标 %q 与已验证 Origin %s 不一致", address, p.origin())
	}

	var failures []error
	for _, addr := range p.addresses {
		if network == "tcp4" && !addr.Is4() {
			continue
		}
		if network == "tcp6" && !addr.Is6() {
			continue
		}
		connection, dialErr := p.dial(ctx, network, net.JoinHostPort(addr.String(), p.port))
		if dialErr == nil {
			return connection, nil
		}
		if connection != nil {
			_ = connection.Close()
		}
		failures = append(failures, fmt.Errorf("拨号 %s: %w", addr, dialErr))
		if ctx.Err() != nil {
			return nil, errors.Join(append([]error{ctx.Err()}, failures...)...)
		}
	}
	if len(failures) == 0 {
		return nil, fmt.Errorf("已验证 Origin %s 没有适用于网络 %q 的固定地址", p.origin(), network)
	}
	return nil, fmt.Errorf("已验证 Origin %s 的固定地址均拨号失败: %w", p.origin(), errors.Join(failures...))
}

// ValidateOpenAICompatibleBaseURL validates the stricter account credential
// shape without performing DNS resolution. PrepareRequestURL remains the
// mandatory execution-time SSRF boundary.
func ValidateOpenAICompatibleBaseURL(raw string, config Config) (*url.URL, error) {
	prepared, err := prepareConfig(config)
	if err != nil {
		return nil, err
	}
	parsed, rawPath, err := parseURL(raw, true)
	if err != nil {
		return nil, err
	}
	allowedOrigin := prepared.allowlist[originKey(parsed)]
	if err := validateScheme(parsed); err != nil {
		return nil, err
	}
	if err := validateOpenAICompatiblePath(rawPath); err != nil {
		return nil, err
	}
	if err := rejectImmediateUnsafeHost(parsed, prepared.config, allowedOrigin); err != nil {
		return nil, err
	}
	return parsed, nil
}

// PrepareRequestURL validates a complete upstream request URL, resolves every
// DNS address, rejects the whole result if any address is unsafe, and returns a
// dial plan pinned to that result.
func PrepareRequestURL(ctx context.Context, raw string, config Config) (*DialPlan, error) {
	if ctx == nil {
		return nil, fmt.Errorf("上游 URL 准备 context 不能为空")
	}
	prepared, err := prepareConfig(config)
	if err != nil {
		return nil, err
	}
	parsed, _, err := parseURL(raw, false)
	if err != nil {
		return nil, err
	}
	allowedOrigin := prepared.allowlist[originKey(parsed)]
	if err := rejectImmediateUnsafeHost(parsed, prepared.config, allowedOrigin); err != nil {
		return nil, err
	}

	host := normalizeHost(parsed.Hostname())
	addresses, err := resolveAll(ctx, host, prepared.config.Resolver)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if isPrivateOrReserved(address) {
			if !prepared.config.AllowPrivateBaseURLs && !allowedOrigin {
				return nil, &UnsafeURLError{Cause: fmt.Errorf("主机 %q 解析到不安全地址 %s", host, address)}
			}
		}
	}
	if err := validateScheme(parsed); err != nil {
		return nil, err
	}

	return &DialPlan{
		requestURL: *parsed,
		host:       host,
		port:       effectivePort(parsed),
		addresses:  addresses,
		dial:       prepared.config.DialContext,
	}, nil
}

type preparedConfig struct {
	config    Config
	allowlist map[string]bool
}

func prepareConfig(config Config) (preparedConfig, error) {
	if config.Production && config.AllowPrivateBaseURLs {
		return preparedConfig{}, fmt.Errorf("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS 只能用于本地开发或回归测试，生产环境不能启用")
	}
	allowlist := make(map[string]bool, len(config.PrivateBaseURLAllowlist))
	for _, entry := range config.PrivateBaseURLAllowlist {
		origin, err := normalizeAllowlistOrigin(entry)
		if err != nil {
			return preparedConfig{}, err
		}
		allowlist[origin] = true
	}
	if config.Resolver == nil {
		config.Resolver = net.DefaultResolver
	}
	if config.DialContext == nil {
		dialer := &net.Dialer{}
		config.DialContext = dialer.DialContext
	}
	return preparedConfig{config: config, allowlist: allowlist}, nil
}

func normalizeAllowlistOrigin(raw string) (string, error) {
	parsed, _, err := parseURL(raw, false)
	if err != nil {
		return "", fmt.Errorf("JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST 只能逐项填写完整的 http/https 私网 IP Origin %q: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST 只允许 http 或 https Origin: %s", raw)
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", fmt.Errorf("JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST 只能填写 Origin，不要包含路径、查询、片段或用户名密码: %s", raw)
	}
	host := normalizeHost(parsed.Hostname())
	address, err := netip.ParseAddr(host)
	if err != nil || address.Zone() != "" {
		return "", fmt.Errorf("JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST 只允许 IP Origin，不接受域名: %s", raw)
	}
	return originKey(parsed), nil
}

func parseURL(raw string, baseURL bool) (*url.URL, string, error) {
	input := strings.TrimSpace(raw)
	if input == "" {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 不能为空"}
	}
	for _, character := range input {
		if unicode.IsSpace(character) {
			return nil, "", &UnsafeURLError{Message: "上游 Base URL 不能包含空白字符"}
		}
		if unicode.IsControl(character) {
			return nil, "", &UnsafeURLError{Message: "上游 Base URL 不能包含控制字符"}
		}
	}
	if strings.Contains(input, `\`) {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 不能包含反斜杠"}
	}
	match := absURLPattern.FindStringSubmatchIndex(input)
	if match == nil {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 必须是完整绝对地址，例如 https://api.openai.com/v1"}
	}
	afterScheme := input[match[1]:]
	if strings.HasPrefix(afterScheme, "/") {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 协议后只能保留两个斜杠"}
	}
	authorityEnd := strings.IndexAny(afterScheme, "/?#")
	if authorityEnd < 0 {
		authorityEnd = len(afterScheme)
	}
	authority := afterScheme[:authorityEnd]
	if authority == "" {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 必须包含主机名"}
	}
	if strings.Contains(authority, "@") {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 不能包含用户名或密码"}
	}
	rawSuffix := afterScheme[authorityEnd:]
	rawPath := rawSuffix
	if index := strings.IndexAny(rawPath, "?#"); index >= 0 {
		rawPath = rawPath[:index]
	}
	if rawPath == "" {
		rawPath = "/"
	}
	if baseURL && strings.Contains(rawPath, "//") {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 路径不能包含连续斜杠"}
	}
	if err := validateRawPath(rawPath, baseURL); err != nil {
		return nil, "", err
	}
	parsed, err := url.Parse(input)
	if err != nil {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 格式无效", Cause: err}
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Hostname() == "" {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 必须包含主机名"}
	}
	if parsed.User != nil {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 不能包含用户名或密码"}
	}
	if parsed.Fragment != "" || strings.Contains(rawSuffix, "#") {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 不能包含片段标识"}
	}
	if baseURL && (parsed.RawQuery != "" || strings.Contains(rawSuffix, "?")) {
		return nil, "", &UnsafeURLError{Message: "上游 Base URL 不能包含查询参数"}
	}
	if err := validatePort(parsed); err != nil {
		return nil, "", err
	}
	return parsed, rawPath, nil
}

func validateRawPath(rawPath string, rejectEncodedSeparators bool) error {
	for _, segment := range strings.Split(rawPath, "/") {
		if segment == "" {
			continue
		}
		decoded, err := strictPathUnescape(segment)
		if err != nil {
			return &UnsafeURLError{Message: "上游 Base URL 路径编码无效", Cause: err}
		}
		if rejectEncodedSeparators && strings.ContainsAny(decoded, `/\`) {
			return &UnsafeURLError{Message: "上游 Base URL 路径不能包含编码后的斜杠"}
		}
		if decoded == "." || decoded == ".." {
			return &UnsafeURLError{Message: "上游 Base URL 路径不能包含 . 或 .. 段"}
		}
	}
	return nil
}

func strictPathUnescape(value string) (string, error) {
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			continue
		}
		if index+2 >= len(value) || !hexPattern.MatchString(value[index+1:index+3]) {
			return "", url.EscapeError(value[index:])
		}
		index += 2
	}
	return url.PathUnescape(value)
}

func validatePort(parsed *url.URL) error {
	port := parsed.Port()
	if port == "" {
		if strings.HasSuffix(parsed.Host, ":") {
			return &UnsafeURLError{Message: "上游 Base URL 端口无效"}
		}
		return nil
	}
	if _, err := normalizePort(port); err != nil {
		return &UnsafeURLError{Message: "上游 Base URL 端口必须在 1 到 65535 之间", Cause: err}
	}
	return nil
}

func normalizePort(port string) (string, error) {
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		if err == nil {
			err = fmt.Errorf("端口 %q 超出范围", port)
		}
		return "", err
	}
	return strconv.Itoa(number), nil
}

func validateScheme(parsed *url.URL) error {
	switch parsed.Scheme {
	case "http", "https":
		return nil
	default:
		return &UnsafeURLError{Message: "上游 Base URL 只允许 http 或 https 协议"}
	}
}

func validateOpenAICompatiblePath(rawPath string) error {
	segments := make([]string, 0)
	for _, rawSegment := range strings.Split(rawPath, "/") {
		if rawSegment == "" {
			continue
		}
		segment, err := strictPathUnescape(rawSegment)
		if err != nil {
			return &UnsafeURLError{Message: "上游 Base URL 路径编码无效", Cause: err}
		}
		segments = append(segments, strings.ToLower(segment))
	}
	for index, segment := range segments {
		if segment == "v1" && index < len(segments)-1 && matchesOpenAIEndpoint(segments[index+1:]) {
			return &UnsafeURLError{Message: "上游 Base URL 不能包含 /v1 后的具体接口路径"}
		}
	}
	for index := range segments {
		if matchesOpenAIEndpoint(segments[index:]) {
			return &UnsafeURLError{Message: "上游 Base URL 不能填写具体接口路径，请填写服务根地址或 /v1 版本根地址"}
		}
	}
	return nil
}

func matchesOpenAIEndpoint(segments []string) bool {
	prefixes := [][]string{
		{"models"}, {"responses"}, {"chat", "completions"},
		{"images", "generations"}, {"images", "edits"}, {"images", "variations"},
		{"embeddings"}, {"audio", "transcriptions"}, {"audio", "translations"},
		{"audio", "speech"}, {"files"}, {"batches"}, {"fine_tuning", "jobs"},
		{"moderations"}, {"vector_stores"},
	}
	return slices.ContainsFunc(prefixes, func(prefix []string) bool {
		return len(segments) >= len(prefix) && slices.Equal(segments[:len(prefix)], prefix)
	})
}

func rejectImmediateUnsafeHost(parsed *url.URL, config Config, allowedOrigin bool) error {
	if config.AllowPrivateBaseURLs || allowedOrigin {
		return nil
	}
	host := normalizeHost(parsed.Hostname())
	if isLocalhostName(host) {
		return &UnsafeURLError{}
	}
	ipToken := strings.TrimSuffix(host, ".")
	if address, err := netip.ParseAddr(ipToken); err == nil {
		if isPrivateOrReserved(address) {
			return &UnsafeURLError{}
		}
		return nil
	}
	// WHATWG accepts shorthand, octal, hexadecimal, and integer IPv4 forms
	// such as 127.1 and 0x7f000001. Go deliberately does not. Reject these
	// ambiguous numeric hosts before DNS so they cannot bypass the literal-IP
	// check on a platform whose resolver interprets them as loopback.
	if looksLikeLegacyIPv4(ipToken) {
		return &UnsafeURLError{}
	}
	return nil
}

func looksLikeLegacyIPv4(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		base := 10
		digits := part
		if len(part) > 2 && strings.EqualFold(part[:2], "0x") {
			base = 16
			digits = part[2:]
		} else if len(part) > 1 && part[0] == '0' {
			base = 8
			digits = part[1:]
		}
		if digits == "" {
			digits = "0"
		}
		if _, err := strconv.ParseUint(digits, base, 32); err != nil {
			return false
		}
	}
	return true
}

func resolveAll(ctx context.Context, host string, resolver Resolver) ([]netip.Addr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		if literal.Zone() != "" {
			return nil, &UnsafeURLError{Message: "上游 Base URL 不允许带 zone 的 IPv6 地址"}
		}
		return []netip.Addr{literal}, nil
	}
	addresses, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, &UnsafeURLError{Message: fmt.Sprintf("解析上游主机 %q 失败", host), Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unique := make([]netip.Addr, 0, len(addresses))
	seen := make(map[netip.Addr]bool, len(addresses))
	for _, address := range addresses {
		if !address.IsValid() || address.Zone() != "" {
			return nil, &UnsafeURLError{Message: fmt.Sprintf("上游主机 %q 返回无效 DNS 地址 %q", host, address)}
		}
		if !seen[address] {
			seen[address] = true
			unique = append(unique, address)
		}
	}
	if len(unique) == 0 {
		return nil, &UnsafeURLError{Message: fmt.Sprintf("上游主机 %q 没有可用 DNS 地址", host)}
	}
	return unique, nil
}

func isPrivateOrReserved(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return true
	}
	if address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified() || address.IsMulticast() {
		return true
	}
	return slices.ContainsFunc(blockedPrefixes, func(prefix netip.Prefix) bool { return prefix.Contains(address) })
}

func isLocalhostName(host string) bool {
	host = strings.TrimSuffix(host, ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func sameHost(left, right string) bool {
	left = normalizeHost(left)
	right = normalizeHost(right)
	leftAddress, leftErr := netip.ParseAddr(left)
	rightAddress, rightErr := netip.ParseAddr(right)
	if leftErr == nil && rightErr == nil {
		return leftAddress == rightAddress
	}
	return strings.TrimSuffix(left, ".") == strings.TrimSuffix(right, ".")
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]")))
}

func effectivePort(parsed *url.URL) string {
	if parsed.Port() != "" {
		// Port syntax has already been validated. Canonicalizing here keeps
		// exact-origin comparison aligned with WHATWG URL behavior (for example,
		// :080 and the implicit HTTP port are the same origin).
		port, _ := normalizePort(parsed.Port())
		return port
	}
	if parsed.Scheme == "https" {
		return "443"
	}
	return "80"
}

func originKey(parsed *url.URL) string {
	host := normalizeHost(parsed.Hostname())
	if address, err := netip.ParseAddr(host); err == nil && address.Is6() {
		host = "[" + address.String() + "]"
	}
	return parsed.Scheme + "://" + host + ":" + effectivePort(parsed)
}

func (p *DialPlan) origin() string {
	host := p.host
	if address, err := netip.ParseAddr(host); err == nil && address.Is6() {
		host = "[" + address.String() + "]"
	}
	return p.requestURL.Scheme + "://" + host + ":" + p.port
}

func mustPrefixes(values ...string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}
