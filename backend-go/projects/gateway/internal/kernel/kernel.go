package kernel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Kernel assembles the gateway HTTP boundary in the system-api-app.ts
// middleware order: request context → security headers (management prefix) →
// compression → no-store (API prefix) → routes → API 404 JSON. The
// localizeWriter and the 405→404 JSON conversion apply globally: Express
// falls through to the API 404 JSON when only the method is unmatched.

type Options struct {
	CompressionDisabled bool
	SystemAPIPrefix     string // default "/__aisys__/api"
	PublicAPIPrefix     string // default "/__aipublic__"
	ManagementPrefix    string // default "/__aisys__"
	JSONBodyLimitBytes  int64  // default 256 KiB (systemApiJsonBodyLimit)
	TrustProxyCount     int    // X-Forwarded-For entries to trust
	Readiness           func() (status int, payload any)
}

func (o *Options) fill() {
	if o.SystemAPIPrefix == "" {
		o.SystemAPIPrefix = "/__aisys__/api"
	}
	if o.PublicAPIPrefix == "" {
		o.PublicAPIPrefix = "/__aipublic__"
	}
	if o.ManagementPrefix == "" {
		o.ManagementPrefix = "/__aisys__"
	}
	if o.JSONBodyLimitBytes <= 0 {
		o.JSONBodyLimitBytes = 256 * 1024
	}
}

type Kernel struct {
	opts         Options
	mux          *http.ServeMux
	catchAll     sync.Once
	fallback     http.Handler
	fallbackOnce sync.Once
}

func New(options Options) *Kernel {
	options.fill()
	return &Kernel{opts: options, mux: http.NewServeMux()}
}

// Register mounts a method+pattern handler. Patterns use Go 1.22 ServeMux
// syntax ("GET /__aisys__/api/groups/{id}").
func (k *Kernel) Register(pattern string, handler http.Handler) {
	k.mux.Handle(pattern, handler)
}

func (k *Kernel) RegisterFunc(pattern string, handler http.HandlerFunc) {
	k.mux.HandleFunc(pattern, handler)
}

// RegisterFallback mounts the handler for requests unmatched by any pattern
// (the legacybridge during migration). Only the first registration wins; the
// default fallback is the Node 404 JSON contract.
func (k *Kernel) RegisterFallback(handler http.Handler) {
	k.fallbackOnce.Do(func() {
		k.fallback = handler
	})
}

// Handler returns the fully wrapped root handler.
func (k *Kernel) Handler() http.Handler {
	inner := k.rootHandler()
	// Unmatched paths (mux catch-all) and method mismatches both resolve to
	// the Node 404 JSON contract.
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := &methodContractWriter{ResponseWriter: w}
		inner.ServeHTTP(method, r)
	})
	localized := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lw := newLocalizeWriter(w)
		fallback.ServeHTTP(lw, r.WithContext(WithResponseWriter(r.Context(), lw)))
		if !lw.wroteHeader {
			lw.WriteHeader(http.StatusOK)
		}
	})
	return RequestContextMiddleware(k.opts.TrustProxyCount)(localized)
}

func (k *Kernel) rootHandler() http.Handler {
	// Catch-all: Node's end-of-chain 404 JSON. Registered in New() is not
	// possible (mux not shared); guard with sync.Once for idempotency because
	// Handler() may be invoked multiple times (tests, health snapshots).
	k.catchAll.Do(func() {
		if k.fallback != nil {
			k.mux.Handle("/", k.fallback)
		} else {
			k.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				WriteAPINotFound(w)
			})
		}
	})
	limited := bodyLimitMiddleware(k.opts.SystemAPIPrefix, k.opts.JSONBodyLimitBytes)(k.mux)
	noStore := prefixMiddleware(k.opts.SystemAPIPrefix, noStoreMiddleware)(limited)
	compressed := noStore
	if !k.opts.CompressionDisabled {
		compressed = CompressionMiddleware(noStore)
	}
	return prefixMiddleware(k.opts.ManagementPrefix, ManagementSecurityHeadersMiddleware)(compressed)
}

func prefixMiddleware(prefix string, middleware func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, prefix) {
				middleware(next).ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func noStoreMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// bodyLimitMiddleware wraps the request body with http.MaxBytesReader under
// the API prefixes. Decoding goes through DecodeJSON, which reports the
// handleJsonBodyError contract (413 请求体过大 / 400 请求体无效).
func bodyLimitMiddleware(prefix string, limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, prefix) && r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

var errBodyTooLarge = errors.New("request body too large")

// DecodeJSON parses the request body into target, mirroring express.json()
// plus handleJsonBodyError: an empty body leaves target untouched; an
// oversized body writes 413 {"message":"请求体过大"}; malformed JSON writes
// 400 {"message":"请求体无效"}.
func DecodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	body, err := readAll(r)
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		if errors.Is(err, errBodyTooLarge) {
			WriteError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		} else {
			WriteError(w, http.StatusBadRequest, "请求体无效")
		}
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return true
	}
	if err := json.Unmarshal(body, target); err != nil {
		w.Header().Set("Cache-Control", "no-store")
		WriteError(w, http.StatusBadRequest, "请求体无效")
		return false
	}
	return true
}

func readAll(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errBodyTooLarge
		}
		return nil, err
	}
	return body, nil
}

// methodContractWriter converts the Go mux 405 response into the Node
// contract: Express falls through to the 404 JSON for unmatched methods.
type methodContractWriter struct {
	http.ResponseWriter
	converted bool
}

func (m *methodContractWriter) WriteHeader(status int) {
	if status == http.StatusMethodNotAllowed && !m.converted {
		m.converted = true
		header := m.Header()
		header.Del("Allow")
		header.Set("Content-Type", "application/json; charset=utf-8")
		m.ResponseWriter.WriteHeader(http.StatusNotFound)
		body, _ := json.Marshal(map[string]string{"message": "资源不存在"})
		_, _ = m.ResponseWriter.Write(body)
		return
	}
	m.ResponseWriter.WriteHeader(status)
}

func (m *methodContractWriter) Write(body []byte) (int, error) {
	if m.converted {
		return len(body), nil
	}
	return m.ResponseWriter.Write(body)
}

func (m *methodContractWriter) MarkUpstream() {
	if marker, ok := m.ResponseWriter.(UpstreamMarker); ok {
		marker.MarkUpstream()
	}
}

func (m *methodContractWriter) MarkedUpstream() bool {
	if marker, ok := m.ResponseWriter.(UpstreamMarker); ok {
		return marker.MarkedUpstream()
	}
	return false
}

// NotFoundHandler returns the API 404 JSON handler for catch-all mounting.
func NotFoundHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteAPINotFound(w)
	})
}

// HealthHandler serves the readiness contract (200 ok / 503 degraded).
func HealthHandler(readiness func() (status int, payload any)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, payload := readiness()
		WriteJSON(w, status, payload)
	})
}
