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
