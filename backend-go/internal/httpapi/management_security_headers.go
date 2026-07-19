package httpapi

import (
	"net/http"
	"strings"
)

var managementSecurityHeaders = map[string]string{
	"Content-Security-Policy": "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data: https:; connect-src 'self' https: wss:; worker-src 'self' blob:; media-src 'self' data: blob: https:; manifest-src 'self'",
	"X-Frame-Options":         "DENY",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "strict-origin-when-cross-origin",
}

func managementSecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__aisys__" || strings.HasPrefix(r.URL.Path, "/__aisys__/") {
			for name, value := range managementSecurityHeaders {
				w.Header().Set(name, value)
			}
		}
		next.ServeHTTP(w, r)
	})
}
