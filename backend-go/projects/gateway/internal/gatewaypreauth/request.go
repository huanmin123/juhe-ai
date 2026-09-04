package gatewaypreauth

import (
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// GatewayRequest is the gateway-facing request view shared by the pre-auth
// and preflight pipeline. It mirrors the consumed surface of the express
// request in request/pre-auth.ts + request/preflight.ts:
//
//   - HTTP mirrors the raw request (headers, method, URL);
//   - Body mirrors the gateway body pipeline state
//     (request.rawBody / request.body / request.gatewayRequestBody);
//   - ClientIP mirrors express req.ip (trust-proxy resolved upstream);
//   - RemoteAddr mirrors req.socket.remoteAddress.
//
// The gateway server layer populates one GatewayRequest per request; handlers
// never mutate it.
type GatewayRequest struct {
	HTTP       *http.Request
	Body       *gatewaybody.Request
	ClientIP   string
	RemoteAddr string

	// Runtime mirrors req.gatewayRuntime: the runtime snapshot resolved by
	// PreResolveGatewayRuntime and reused by later stages.
	Runtime *gatewayruntimecache.GatewayRuntime

	// AuthFailureErrorMessage / AuthFailureErrorCode mirror
	// res.locals.gatewayAuthFailureErrorMessage / gatewayAuthFailureErrorCode:
	// the audit copy of the auth failure that produced the response.
	AuthFailureErrorMessage string
	AuthFailureErrorCode    string

	// abortSource mirrors the request/abort-attribution.ts marker.
	abortSource GatewayRequestAbortSource

	// InflightQuotaReserved mirrors the Node inflightQuotaReservedRequests
	// WeakSet membership: one in-flight reservation per request.
	InflightQuotaReserved bool
}

// NewGatewayRequest builds the view from a raw request. ClientIP falls back
// to HTTP.RemoteAddr-based extraction only through ExtractClientIP (metadata
// contract); the struct fields stay verbatim inputs.
func NewGatewayRequest(r *http.Request) *GatewayRequest {
	req := &GatewayRequest{HTTP: r}
	if r != nil {
		req.RemoteAddr = r.RemoteAddr
	}
	return req
}

// PathAndQuery mirrors `req.originalUrl || req.path`: the escaped request
// path plus raw query, with a leading slash guaranteed.
func (req *GatewayRequest) PathAndQuery() string {
	if req == nil || req.HTTP == nil {
		return "/"
	}
	if req.HTTP.RequestURI != "" {
		// RequestURI carries the original (escaped) path + query verbatim.
		return req.HTTP.RequestURI
	}
	return gatewayRequestPathAndQuery(req.HTTP)
}

// Path mirrors express req.path: the (still escaped) pathname without query.
func (req *GatewayRequest) Path() string {
	if req == nil || req.HTTP == nil {
		return "/"
	}
	if req.HTTP.URL == nil {
		return "/"
	}
	path := req.HTTP.URL.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

// Header mirrors express req.header(name): case-insensitive lookup with an
// empty string when the header is absent.
func (req *GatewayRequest) Header(name string) string {
	if req == nil || req.HTTP == nil {
		return ""
	}
	return req.HTTP.Header.Get(name)
}

// MethodUpper mirrors req.method.toUpperCase().
func (req *GatewayRequest) MethodUpper() string {
	if req == nil || req.HTTP == nil {
		return ""
	}
	return strings.ToUpper(req.HTTP.Method)
}

// BodyState mirrors getGatewayRequestBodyState(req): the body pipeline state
// or nil before capture.
func (req *GatewayRequest) BodyState() *gatewaybody.BodyState {
	if req == nil || req.Body == nil {
		return nil
	}
	return req.Body.State
}

// ParsedJSONObjectBody mirrors the `isRecord(req.body) ? req.body :
// isRecord(req.gatewayParsedJsonBody) ? ... : undefined` inspection body.
func (req *GatewayRequest) ParsedJSONObjectBody() map[string]any {
	if req == nil || req.Body == nil {
		return nil
	}
	return gatewaybody.GatewayJSONObjectBody(req.Body)
}

// gatewayRequestPathAndQuery mirrors gatewayanthropic.RequestPathAndQuery.
func gatewayRequestPathAndQuery(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "/"
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	if r.URL.RawQuery != "" {
		return path + "?" + r.URL.RawQuery
	}
	return path
}

// splitPathAndQuery mirrors splitPathAndQuery(originalUrl).
func splitPathAndQuery(pathAndQuery string) (path, query string) {
	index := strings.Index(pathAndQuery, "?")
	if index < 0 {
		return pathAndQuery, ""
	}
	return pathAndQuery[:index], pathAndQuery[index+1:]
}

// splitPathAndQueryTwo mirrors the JS `split('?', 2)` destructuring used by
// requestGeminiInteractionStreamQuery: at most two parts.
func splitPathAndQueryTwo(value string) (string, string) {
	parts := strings.SplitN(value, "?", 2)
	path := parts[0]
	query := ""
	if len(parts) > 1 {
		query = parts[1]
	}
	return path, query
}
