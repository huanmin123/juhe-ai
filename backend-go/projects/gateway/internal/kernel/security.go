package kernel

import (
	"net/http"
)

// Management security headers mirror shared/http-security.ts byte for byte.
var managementHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data: https:; connect-src 'self' https: wss:; worker-src 'self' blob:; media-src 'self' data: blob: https:; manifest-src 'self'",
	"X-Frame-Options":         "DENY",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "strict-origin-when-cross-origin",
}

// CORSPolicy mirrors the httpSecurity runtime config slice consumed by
// isCorsOriginAllowed: an absent Origin header is always allowed.
type CORSPolicy struct {
	AllowAnyOrigin bool
	AllowedOrigins []string
}

func (p CORSPolicy) IsOriginAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	if p.AllowAnyOrigin {
		return true
	}
	for _, allowed := range p.AllowedOrigins {
		if allowed == origin {
			return true
		}
	}
	return false
}

// corsPreflightAllowMethods mirrors the cors package default methods the Node
// createCorsOriginDelegate was designed for (preflightContinue=false,
// optionsSuccessStatus=204).
const corsPreflightAllowMethods = "GET,HEAD,PUT,PATCH,POST,DELETE"

// CORSMiddleware applies the Node http-security origin contract
// (isCorsOriginAllowed + createCorsOriginDelegate, http-security.ts:18-28)
// onto the kernel HTTP surface. Deliberate Go-side addition, NOT a Node
// behavior port: the archived Node production chain defines the delegate but
// never mounts any CORS middleware (system-api-app.ts:112-157), so origin
// enforcement here is a hardening decision (registered in
// docs/reports/Node到Go迁移从头复查报告-20260906.md §7-D12) and production
// startup requires an explicit JUHE_AI_ALLOWED_ORIGINS allowlist:
//   - a request without an Origin header passes through untouched (no CORS
//     headers): same-origin and non-browser clients are never affected;
//   - an allowed Origin is reflected into Access-Control-Allow-Origin with
//     Access-Control-Allow-Credentials: true (the management session cookie
//     is credentialed) and Vary: Origin;
//   - a disallowed Origin continues without any CORS header. Like the cors
//     package delegate (callback(null, false) → next()), the middleware never
//     rejects a request itself; the browser enforces the block;
//   - an OPTIONS request with an allowed Origin is a CORS preflight and
//     terminates with 204 plus the cors package default Allow-Methods and the
//     echoed Access-Control-Request-Headers; a disallowed preflight falls
//     through to the router.
//
// prefixes scope the middleware the way the Express app mounts its layers
// (mountPathMatch semantics: exact prefix or any deeper path); an empty list
// applies to every path.
func CORSMiddleware(policy CORSPolicy, prefixes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(prefixes) > 0 && !matchesAnyMountPrefix(r.URL.EscapedPath(), prefixes) {
				next.ServeHTTP(w, r)
				return
			}
			origin := r.Header.Get("Origin")
			if origin == "" || !policy.IsOriginAllowed(origin) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", corsPreflightAllowMethods)
				if requestHeaders := r.Header.Get("Access-Control-Request-Headers"); requestHeaders != "" {
					w.Header().Set("Access-Control-Allow-Headers", requestHeaders)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func matchesAnyMountPrefix(requestPath string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if mountPathMatch(requestPath, prefix) {
			return true
		}
	}
	return false
}

// ManagementSecurityHeadersMiddleware sets the fixed management headers on
// every response under the management prefix.
func ManagementSecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, value := range managementHeaders {
			w.Header().Set(name, value)
		}
		next.ServeHTTP(w, r)
	})
}

// SessionCookieOptions mirrors sessionCookieOptions from http-security.ts.
type SessionCookieOptions struct {
	SameSite string // "lax" | "strict" | "none"
	Secure   bool
}

func (o SessionCookieOptions) Apply(cookie *http.Cookie) {
	cookie.HttpOnly = true
	cookie.Path = "/"
	switch o.SameSite {
	case "strict":
		cookie.SameSite = http.SameSiteStrictMode
	case "none":
		cookie.SameSite = http.SameSiteNoneMode
	default:
		cookie.SameSite = http.SameSiteLaxMode
	}
	cookie.Secure = o.Secure
}
