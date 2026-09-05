package accounts

import (
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
)

// Upstream Base URL security policy: the write-side port of
// backend/src/shared/upstream-base-url-validator.ts (openAICompatibleBaseUrlPolicy)
// plus the static (non-DNS) half of upstream-url-policy.ts
// assertSafeUpstreamBaseUrl. The async DNS half (prepareSafeUpstreamRequestUrl)
// belongs to the gateway upstream request slices and stays unported here.

// unsafeUpstreamBaseURLMessage mirrors UnsafeUpstreamUrlError's default copy.
const unsafeUpstreamBaseURLMessage = "上游 Base URL 不能指向本机、内网、链路本地或保留地址；本地联调请显式配置 JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST，只有临时回归才使用 JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS=true"

// upstreamURLSecurity mirrors RuntimeConfig['upstreamUrlSecurity'] (config
// runtime.ts:1679-1689): env-driven once at process start in Node, lazily
// cached here so tests can point the env before the first call.
type upstreamURLSecurity struct {
	allowPrivateBaseUrls    bool
	privateBaseUrlAllowlist []string
}

var (
	upstreamSecurityOnce sync.Once
	upstreamSecurity     upstreamURLSecurity
)

func upstreamURLSecurityConfig() upstreamURLSecurity {
	upstreamSecurityOnce.Do(func() {
		allow := false
		switch strings.TrimSpace(os.Getenv("JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS")) {
		case "true", "1":
			allow = true
		}
		allowlist := []string{}
		raw := os.Getenv("JUHE_AI_UPSTREAM_BASE_URL_PRIVATE_ALLOWLIST")
		if strings.TrimSpace(raw) != "" {
			seen := map[string]bool{}
			for _, item := range strings.Split(raw, ",") {
				origin, ok := normalizePrivateUpstreamOrigin(strings.TrimSpace(item))
				if ok && !seen[origin] {
					seen[origin] = true
					allowlist = append(allowlist, origin)
				}
			}
		}
		upstreamSecurity = upstreamURLSecurity{allowPrivateBaseUrls: allow, privateBaseUrlAllowlist: allowlist}
	})
	return upstreamSecurity
}

// normalizePrivateUpstreamOrigin mirrors normalizePrivateUpstreamOrigin
// (config/runtime.ts:1695+): entries must parse as absolute URLs and render as
// protocol//host:port with the default port elided.
func normalizePrivateUpstreamOrigin(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.Trim(host, "[]")
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return parsed.Scheme + "://" + host + ":" + port, true
}

type upstreamBaseURLValidationError struct{ message string }

func (e *upstreamBaseURLValidationError) Error() string { return e.message }

func upstreamURLInvalid(message string) error {
	return &upstreamBaseURLValidationError{message: message}
}

// validateOpenAICompatibleBaseURLPath mirrors
// validateOpenAICompatibleBaseUrlPath: a /v1 root or the service root only —
// concrete endpoint paths are rejected.
func validateOpenAICompatibleBaseURLPath(path string) string {
	segments := []string{}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			continue
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return "上游 Base URL 路径编码无效"
		}
		segments = append(segments, strings.ToLower(decoded))
	}
	if len(segments) == 0 {
		return ""
	}
	finalV1Index := -1
	for index, segment := range segments {
		if segment == "v1" {
			finalV1Index = index
		}
	}
	if finalV1Index >= 0 && finalV1Index < len(segments)-1 {
		suffix := segments[finalV1Index+1:]
		if matchesOpenAIEndpointPath(suffix) {
			return "上游 Base URL 不能包含 /v1 后的具体接口路径"
		}
	}
	for index := range segments {
		if matchesOpenAIEndpointPath(segments[index:]) {
			return "上游 Base URL 不能填写具体接口路径，请填写服务根地址或 /v1 版本根地址"
		}
	}
	return ""
}

var openAIEndpointPathPrefixes = [][]string{
	{"models"}, {"responses"}, {"chat", "completions"}, {"images", "generations"},
	{"images", "edits"}, {"images", "variations"}, {"embeddings"},
	{"audio", "transcriptions"}, {"audio", "translations"}, {"audio", "speech"},
	{"files"}, {"batches"}, {"fine_tuning", "jobs"}, {"moderations"}, {"vector_stores"},
}

func matchesOpenAIEndpointPath(segments []string) bool {
	for _, prefix := range openAIEndpointPathPrefixes {
		if len(segments) < len(prefix) {
			continue
		}
		matched := true
		for index, part := range prefix {
			if segments[index] != part {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// validateOpenAICompatibleBaseURL mirrors parseAndValidateUpstreamUrl under
// openAICompatibleBaseUrlPolicy: raw-string checks, authority/query/hash/path
// checks, then the URL-level checks. Returns the parsed URL.
func validateOpenAICompatibleBaseURL(value string) (*url.URL, error) {
	input, err := validateRawUpstreamURLString(value)
	if err != nil {
		return nil, err
	}
	parts, err := parseRawAbsoluteURLParts(input)
	if err != nil {
		return nil, err
	}
	if strings.Contains(parts.authority, "@") {
		return nil, upstreamURLInvalid("上游 Base URL 不能包含用户名或密码")
	}
	if parts.query != "" {
		return nil, upstreamURLInvalid("上游 Base URL 不能包含查询参数")
	}
	if parts.hash != "" {
		return nil, upstreamURLInvalid("上游 Base URL 不能包含片段标识")
	}
	if parts.path != "" && !strings.HasPrefix(parts.path, "/") {
		return nil, upstreamURLInvalid("上游 Base URL 路径必须以 / 开头")
	}
	if matchConsecutiveSlashes(parts.path) {
		return nil, upstreamURLInvalid("上游 Base URL 路径不能包含连续斜杠")
	}
	if err := assertUpstreamPathSegments(parts.path); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(input)
	if err != nil {
		return nil, upstreamURLInvalid("上游 Base URL 格式无效")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, upstreamURLInvalid("上游 Base URL 只允许 http 或 https 协议")
	}
	if parsed.Hostname() == "" {
		return nil, upstreamURLInvalid("上游 Base URL 必须包含主机名")
	}
	if parsed.User != nil {
		return nil, upstreamURLInvalid("上游 Base URL 不能包含用户名或密码")
	}
	if parsed.RawQuery != "" {
		return nil, upstreamURLInvalid("上游 Base URL 不能包含查询参数")
	}
	if parsed.Fragment != "" || parsed.ForceQuery {
		return nil, upstreamURLInvalid("上游 Base URL 不能包含片段标识")
	}
	if message := validateOpenAICompatibleBaseURLPath(parts.path); message != "" {
		return nil, upstreamURLInvalid(message)
	}
	return parsed, nil
}

func matchConsecutiveSlashes(path string) bool {
	return strings.Contains(path, "//")
}

func validateRawUpstreamURLString(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", upstreamURLInvalid("上游 Base URL 不能为空")
	}
	input := strings.TrimSpace(value)
	for _, r := range input {
		// /[^\\S ]|\s/ — every whitespace except the plain space, plus the
		// general \s class (which includes the space).
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' ||
			r == 0x00a0 || r == 0x1680 || (r >= 0x2000 && r <= 0x200a) ||
			r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff {
			return "", upstreamURLInvalid("上游 Base URL 不能包含空白字符")
		}
	}
	for _, r := range input {
		if r <= 0x1f || r == 0x7f {
			return "", upstreamURLInvalid("上游 Base URL 不能包含控制字符")
		}
	}
	if strings.Contains(input, "\\") {
		return "", upstreamURLInvalid("上游 Base URL 不能包含反斜杠")
	}
	return input, nil
}

type rawURLParts struct {
	protocol  string
	authority string
	path      string
	query     string
	hash      string
}

// parseRawAbsoluteURLParts mirrors parseRawAbsoluteUrlParts: the manual split
// runs before the URL parser so malformed shapes fail with Node's copy.
func parseRawAbsoluteURLParts(input string) (rawURLParts, error) {
	schemeEnd := strings.Index(input, "://")
	if schemeEnd <= 0 {
		return rawURLParts{}, upstreamURLInvalid("上游 Base URL 必须是完整绝对地址，例如 https://api.openai.com/v1")
	}
	scheme := input[:schemeEnd]
	if !isAlphaByte(scheme[0]) {
		return rawURLParts{}, upstreamURLInvalid("上游 Base URL 必须是完整绝对地址，例如 https://api.openai.com/v1")
	}
	for index := 0; index < len(scheme); index++ {
		c := scheme[index]
		if !isAlphaByte(c) && !isDigitByte(c) && c != '+' && c != '-' && c != '.' {
			return rawURLParts{}, upstreamURLInvalid("上游 Base URL 必须是完整绝对地址，例如 https://api.openai.com/v1")
		}
	}
	protocol := strings.ToLower(scheme) + ":"
	afterScheme := input[schemeEnd+3:]
	if strings.HasPrefix(afterScheme, "/") {
		return rawURLParts{}, upstreamURLInvalid("上游 Base URL 协议后只能保留两个斜杠")
	}
	authorityEnd := strings.IndexAny(afterScheme, "/?#")
	authority := afterScheme
	pathAndSuffix := ""
	if authorityEnd >= 0 {
		authority = afterScheme[:authorityEnd]
		pathAndSuffix = afterScheme[authorityEnd:]
	}
	if authority == "" {
		return rawURLParts{}, upstreamURLInvalid("上游 Base URL 必须包含主机名")
	}
	hashIndex := strings.Index(pathAndSuffix, "#")
	beforeHash := pathAndSuffix
	hash := ""
	if hashIndex >= 0 {
		beforeHash = pathAndSuffix[:hashIndex]
		hash = pathAndSuffix[hashIndex:]
	}
	queryIndex := strings.Index(beforeHash, "?")
	path := beforeHash
	query := ""
	if queryIndex >= 0 {
		path = beforeHash[:queryIndex]
		query = beforeHash[queryIndex:]
	}
	if path == "" {
		path = "/"
	}
	return rawURLParts{protocol: protocol, authority: authority, path: path, query: query, hash: hash}, nil
}

func assertUpstreamPathSegments(path string) error {
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			continue
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return upstreamURLInvalid("上游 Base URL 路径编码无效")
		}
		if strings.ContainsAny(decoded, "/\\") {
			return upstreamURLInvalid("上游 Base URL 路径不能包含编码后的斜杠")
		}
		if decoded == "." || decoded == ".." {
			return upstreamURLInvalid("上游 Base URL 路径不能包含 . 或 .. 段")
		}
	}
	return nil
}

func isAlphaByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// assertSafeUpstreamBaseURL mirrors assertSafeUpstreamBaseUrl: parse under the
// openai-compatible policy, then reject loopback/private/reserved targets
// unless the runtime config allows or allowlists the exact origin.
func assertSafeUpstreamBaseURL(value string) error {
	parsed, err := validateOpenAICompatibleBaseURL(value)
	if err != nil {
		return &unsafeUpstreamError{message: validationMessageOrPolicy(err)}
	}
	config := upstreamURLSecurityConfig()
	if config.allowPrivateBaseUrls {
		return nil
	}
	if upstreamOriginAllowlisted(parsed, config) {
		return nil
	}
	if isLocalhostHostName(parsed.Hostname()) || isPrivateOrReservedIP(parsed.Hostname()) {
		return &unsafeUpstreamError{message: unsafeUpstreamBaseURLMessage}
	}
	return nil
}

func validationMessageOrPolicy(err error) string {
	var validation *upstreamBaseURLValidationError
	if ok := asUpstreamValidation(err, &validation); ok && validation.message != "" {
		return validation.message
	}
	return "上游 Base URL 格式无效"
}

func asUpstreamValidation(err error, target **upstreamBaseURLValidationError) bool {
	if cast, ok := err.(*upstreamBaseURLValidationError); ok {
		*target = cast
		return true
	}
	return false
}

// unsafeUpstreamError renders the UnsafeUpstreamUrlError branch of the
// credentials write path: the message is the 400 body verbatim.
type unsafeUpstreamError struct{ message string }

func (e *unsafeUpstreamError) Error() string { return e.message }

func isLocalhostHostName(hostName string) bool {
	return hostName == "localhost" || strings.HasSuffix(hostName, ".localhost")
}

func upstreamOriginAllowlisted(parsed *url.URL, config upstreamURLSecurity) bool {
	if len(config.privateBaseUrlAllowlist) == 0 {
		return false
	}
	origin, ok := normalizePrivateUpstreamOrigin(parsed.String())
	if !ok {
		return false
	}
	for _, allowed := range config.privateBaseUrlAllowlist {
		if allowed == origin {
			return true
		}
	}
	return false
}

// isPrivateOrReservedIP mirrors isPrivateOrReservedIp: literal addresses only
// (hostnames resolve at request time, not here). Node's isIP() picks the table
// from the literal form — any ':' means the IPv6 table, including the
// ::ffff:a.b.c.d mapped form.
func isPrivateOrReservedIP(value string) bool {
	normalized := strings.Trim(strings.ToLower(strings.TrimSpace(value)), "[]")
	if normalized == "" || (!strings.Contains(normalized, ":") && !strings.Contains(normalized, ".")) {
		return false
	}
	if strings.Contains(normalized, ":") {
		ip := net.ParseIP(normalized)
		if ip == nil {
			return false
		}
		return isBlockedIPv6(ip.To16())
	}
	ip := net.ParseIP(normalized)
	if ip == nil {
		return false
	}
	ipv4 := ip.To4()
	if ipv4 == nil {
		return false
	}
	return isBlockedIPv4(ipv4)
}

type blockedIPv4Range struct {
	network [4]byte
	prefix  int
}

var blockedIPv4Ranges = []blockedIPv4Range{
	{[4]byte{0, 0, 0, 0}, 8}, {[4]byte{10, 0, 0, 0}, 8}, {[4]byte{100, 64, 0, 0}, 10},
	{[4]byte{127, 0, 0, 0}, 8}, {[4]byte{169, 254, 0, 0}, 16}, {[4]byte{172, 16, 0, 0}, 12},
	{[4]byte{192, 0, 0, 0}, 24}, {[4]byte{192, 0, 2, 0}, 24}, {[4]byte{192, 88, 99, 0}, 24},
	{[4]byte{192, 168, 0, 0}, 16}, {[4]byte{198, 18, 0, 0}, 15}, {[4]byte{198, 51, 100, 0}, 24},
	{[4]byte{203, 0, 113, 0}, 24}, {[4]byte{224, 0, 0, 0}, 4}, {[4]byte{240, 0, 0, 0}, 4},
}

func isBlockedIPv4(ip net.IP) bool {
	parts := [4]byte{ip[0], ip[1], ip[2], ip[3]}
	for _, blocked := range blockedIPv4Ranges {
		if ipv4MatchesPrefix(parts, blocked.network, blocked.prefix) {
			return true
		}
	}
	return false
}

func ipv4MatchesPrefix(parts, network [4]byte, prefix int) bool {
	remaining := prefix
	for index := 0; index < 4; index++ {
		if remaining <= 0 {
			return true
		}
		bits := remaining
		if bits > 8 {
			bits = 8
		}
		mask := byte(0xff << (8 - bits))
		if parts[index]&mask != network[index]&mask {
			return false
		}
		remaining -= bits
	}
	return true
}

type blockedIPv6Range struct {
	network [16]byte
	prefix  int
}

func mustIPv6Network(text string) [16]byte {
	ip := net.ParseIP(text)
	if ip == nil {
		panic("invalid blocked IPv6 range " + text)
	}
	return [16]byte(ip.To16())
}

var blockedIPv6Ranges = []blockedIPv6Range{
	{mustIPv6Network("::"), 128}, {mustIPv6Network("::1"), 128}, {mustIPv6Network("::"), 96},
	{mustIPv6Network("::ffff:0:0"), 96}, {mustIPv6Network("64:ff9b::"), 96},
	{mustIPv6Network("64:ff9b:1::"), 48}, {mustIPv6Network("100::"), 64},
	{mustIPv6Network("2001::"), 23}, {mustIPv6Network("2001:db8::"), 32},
	{mustIPv6Network("2002::"), 16}, {mustIPv6Network("fc00::"), 7},
	{mustIPv6Network("fe80::"), 10}, {mustIPv6Network("ff00::"), 8},
}

func isBlockedIPv6(ip net.IP) bool {
	groups := ip.To16()
	if groups == nil {
		return true
	}
	var parts [16]byte
	copy(parts[:], groups)
	for _, blocked := range blockedIPv6Ranges {
		if ipv6MatchesPrefix(parts, blocked.network, blocked.prefix) {
			return true
		}
	}
	return false
}

func ipv6MatchesPrefix(parts, network [16]byte, prefix int) bool {
	remaining := prefix
	for index := 0; index < 16; index++ {
		if remaining <= 0 {
			return true
		}
		bits := remaining
		if bits > 8 {
			bits = 8
		}
		mask := byte(0xff << (8 - bits))
		if parts[index]&mask != network[index]&mask {
			return false
		}
		remaining -= bits
	}
	return true
}
