package gatewaysession

import "strings"

// IdentityRequest mirrors the consumed surface of
// GatewaySessionIdentityRequest: the original URL / path plus multi-value
// header access (the headersDistinct projection).
type IdentityRequest interface {
	// OriginalURL mirrors request.originalUrl.
	OriginalURL() string
	// Path mirrors request.path.
	Path() string
	// HeaderValues mirrors request.headersDistinct[name]: every value sent
	// for the (case-insensitive) header name.
	HeaderValues(name string) []string
}

// NormalizedGatewaySessionRequestPath mirrors normalizedGatewaySessionRequestPath.
//
// The `/^\/v1(?=\/|internal:|$)/` lookahead is implemented manually (RE2 has
// no lookahead): the leading "/v1" is stripped only when followed by "/",
// "internal:" or the end of the string.
func NormalizedGatewaySessionRequestPath(request IdentityRequest) string {
	endpoint := ""
	if request != nil {
		endpoint = request.OriginalURL()
		if endpoint == "" {
			endpoint = request.Path()
		}
	}
	path := endpoint
	if idx := strings.IndexByte(endpoint, '?'); idx >= 0 {
		path = endpoint[:idx]
	}
	path = jsTrimString(strings.ToLower(path))
	if path == "" {
		return "/"
	}
	path = trimTrailingSlashes(path)
	path = stripV1Prefix(path)
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func trimTrailingSlashes(path string) string {
	for strings.HasSuffix(path, "/") {
		path = path[:len(path)-1]
	}
	return path
}

func stripV1Prefix(path string) string {
	const prefix = "/v1"
	if !strings.HasPrefix(path, prefix) {
		return path
	}
	rest := path[len(prefix):]
	if rest == "" || strings.HasPrefix(rest, "/") || strings.HasPrefix(rest, "internal:") {
		return rest
	}
	return path
}

// GatewaySessionHeaderValues mirrors gatewaySessionHeaderValues: distinct
// header values first, then raw header pairs, then single-value headers.
// The IdentityRequest projection collapses the Node fallback chain into the
// headersDistinct accessor; resolvers only need the ordered value list.
func GatewaySessionHeaderValues(request IdentityRequest, name string) []string {
	if request == nil {
		return nil
	}
	values := request.HeaderValues(name)
	if len(values) == 0 {
		return nil
	}
	return values
}
