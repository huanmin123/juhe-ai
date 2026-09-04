package gatewaypreauth

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Port of request/metadata.ts. Every function mirrors the Node source field
// by field (trimming, fallback chains and IPv4-only normalization included).

var bearerTokenPattern = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)

// ExtractBearerToken mirrors extractBearerToken: case-insensitive Bearer
// scheme, trimmed token, empty token counts as missing.
func ExtractBearerToken(authorization string) (string, bool) {
	if authorization == "" {
		return "", false
	}
	match := bearerTokenPattern.FindStringSubmatch(authorization)
	if match == nil {
		return "", false
	}
	token := strings.TrimSpace(match[1])
	if token == "" {
		return "", false
	}
	return token, true
}

// ExtractClientIP mirrors extractClientIp: normalizeClientIp(req.ip) ??
// normalizeClientIp(req.socket.remoteAddress).
func ExtractClientIP(req *GatewayRequest) (string, bool) {
	if ip, ok := normalizeClientIP(req.ClientIP); ok {
		return ip, true
	}
	return normalizeClientIP(req.RemoteAddr)
}

// normalizeClientIP mirrors the Node helper: trim, strip [..] brackets,
// strip a ":port" suffix from dotted-quad addresses, strip the "::ffff:"
// mapped-address prefix, and keep only IPv4 results.
func normalizeClientIP(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	ip := strings.TrimSpace(value)
	if ip == "" {
		return "", false
	}
	if strings.HasPrefix(ip, "[") {
		end := strings.Index(ip, "]")
		if end > 0 {
			ip = ip[1:end]
		}
	}
	if ipv4WithPortPattern.MatchString(ip) {
		ip = ip[:strings.LastIndex(ip, ":")]
	}
	if strings.HasPrefix(ip, "::ffff:") {
		ip = ip[len("::ffff:"):]
	}
	return ip, isIPv4Text(ip)
}

var ipv4WithPortPattern = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}:\d+$`)

// isIPv4Text mirrors isIP(ip) === 4: dotted-quad only.
func isIPv4Text(value string) bool {
	parsed := net.ParseIP(value)
	return parsed != nil && parsed.To4() != nil && !strings.Contains(value, ":")
}

// RequestModel mirrors requestModel: Gemini path model, then the captured
// body state model, then the raw parsed body model string.
func RequestModel(req *GatewayRequest) (string, bool) {
	if model, ok := requestModelFromGeminiPath(req.PathAndQuery(), req.Path()); ok {
		return model, true
	}
	if state := req.BodyState(); state != nil && state.Model != nil {
		// Node `bodyState?.model ?? ...`: empty strings are kept (only
		// nullish values fall through).
		return *state.Model, true
	}
	if object := req.ParsedJSONObjectBody(); object != nil {
		if model, ok := object["model"].(string); ok {
			return model, true
		}
	}
	return "", false
}

var geminiPathModelPattern = regexp.MustCompile(`(?i)/models/([^/:?#]+):(?:generateContent|streamGenerateContent|countTokens|embedContent)$`)

// requestModelFromGeminiPath mirrors requestModelFromGeminiPath: the path is
// `originalUrl.split('?', 1)[0] || req.path || ''`.
func requestModelFromGeminiPath(pathAndQuery, fallbackPath string) (string, bool) {
	path := strings.SplitN(pathAndQuery, "?", 2)[0]
	if path == "" {
		path = fallbackPath
	}
	match := geminiPathModelPattern.FindStringSubmatch(path)
	if match == nil || match[1] == "" {
		return "", false
	}
	decoded, err := url.PathUnescape(match[1])
	if err != nil {
		return match[1], true
	}
	return decoded, true
}

// RequestStream mirrors requestStream: captured body state stream flag, then
// the parsed body stream === true, then the Gemini interaction ?stream=true
// query.
func RequestStream(req *GatewayRequest) bool {
	if state := req.BodyState(); state != nil && state.Stream != nil {
		return *state.Stream
	}
	if object := req.ParsedJSONObjectBody(); object != nil {
		if stream, ok := object["stream"].(bool); ok && stream {
			return true
		}
	}
	return requestGeminiInteractionStreamQuery(req)
}

var geminiInteractionStreamPathPattern = regexp.MustCompile(`(?i)^/(?:v1beta/)?interactions/[^/]+$`)

// requestGeminiInteractionStreamQuery mirrors the Node helper.
func requestGeminiInteractionStreamQuery(req *GatewayRequest) bool {
	if req.MethodUpper() != "GET" {
		return false
	}
	pathAndQuery := req.PathAndQuery()
	if pathAndQuery == "" {
		pathAndQuery = req.Path()
	}
	path, query := splitPathAndQueryTwo(pathAndQuery)
	if !geminiInteractionStreamPathPattern.MatchString(path) {
		return false
	}
	value, ok := queryParamFirstValue(query, "stream")
	if !ok {
		return false
	}
	return strings.ToLower(value) == "true"
}

// RequestEndpoint mirrors requestEndpoint: `METHOD path-without-query`.
func RequestEndpoint(req *GatewayRequest) string {
	path := strings.SplitN(req.PathAndQuery(), "?", 2)[0]
	if path == "" {
		path = req.Path()
	}
	return req.MethodUpper() + " " + path
}

// queryParamFirstValue mirrors new URLSearchParams(query).get(name): the
// first value of the key with form-urlencoded decoding ('+' -> space).
func queryParamFirstValue(query, name string) (string, bool) {
	query = strings.TrimPrefix(query, "?")
	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}
		key, value := pair, ""
		if index := strings.Index(pair, "="); index >= 0 {
			key, value = pair[:index], pair[index+1:]
		}
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			decodedKey = key
		}
		if decodedKey != name {
			continue
		}
		decodedValue, err := url.QueryUnescape(value)
		if err != nil {
			decodedValue = value
		}
		return decodedValue, true
	}
	return "", false
}
