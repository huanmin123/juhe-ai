package openaicompat

import (
	"net/http"
	"strings"
)

// GatewayScope mirrors the apiKey subset of the Node gateway runtime that the
// five modules consume: scope binding for every store query.
type GatewayScope struct {
	SystemAccountID string
	APIKeyID        string
}

// ScopeResolver supplies the gateway runtime scope for a request (the Go
// equivalent of req.gatewayRuntime set by preResolveGatewayRuntime). Returning
// nil mirrors a missing/invalid runtime and renders the 401 contract.
type ScopeResolver func(*http.Request) *GatewayScope

// Deps wires the openaicompat route family and executors.
type Deps struct {
	Store  *Store
	Config Config
	Scope  ScopeResolver

	// IndexAsync schedules vector-store file indexing. Node uses a fire-and-
	// forget promise; the default runs a goroutine. Tests inject a
	// synchronous runner for deterministic assertions.
	IndexAsync func(func())

	// Warn receives indexing failure telemetry (Node logger.warn with the
	// openai_compatible_vector_store_file_indexing_failed event).
	Warn func(err error, fields map[string]any)
}

func (d *Deps) maxFileBytes() int64 { return d.Config.withDefaults().MaxFileBytes }

func (d *Deps) filesRoot() string { return d.Config.withDefaults().FilesRoot }

func (d *Deps) scope(r *http.Request) *GatewayScope {
	if d.Scope == nil {
		return nil
	}
	return d.Scope(r)
}

// requireScope mirrors requireGatewayRuntime: 401 缺少或无效的 API Key.
func (d *Deps) requireScope(w http.ResponseWriter, r *http.Request) *GatewayScope {
	scope := d.scope(r)
	if scope == nil {
		err := badRequest("缺少或无效的 API Key", "invalid_api_key")
		err.StatusCode = http.StatusUnauthorized
		err.write(w)
		return nil
	}
	return scope
}

// handle mirrors handleOpenAICompatibleFilesRoute /
// handleOpenAICompatibleVectorStoresRoute: RequestError renders the gateway
// error payload; everything else falls through to the process-level 500.
func handle(w http.ResponseWriter, run func() error) {
	err := run()
	if err == nil {
		return
	}
	if requestErr, ok := err.(*RequestError); ok {
		requestErr.write(w)
		return
	}
	if indexingErr, ok := err.(*IndexingError); ok {
		indexingErr.write(w)
		return
	}
	writeUnhandledError(w)
}

// Mount registers both route families on the kernel (Node mounts
// openAICompatibleFilesRouter + openAICompatibleVectorStoresRouter in the
// gateway chain ahead of the OpenAI gateway router).
func (d *Deps) Mount(k interface {
	Register(pattern string, handler http.Handler)
}) {
	d.MountFiles(k)
	d.MountVectorStores(k)
}

// isMultipartContentType mirrors /^multipart\/form-data\b/i.
func isMultipartContentType(contentType string) bool {
	lower := strings.ToLower(contentType)
	if !strings.HasPrefix(lower, "multipart/form-data") {
		return false
	}
	rest := lower[len("multipart/form-data"):]
	if rest == "" {
		return true
	}
	// \b: the next character must not be a word character.
	switch rest[0] {
	case 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z',
		'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '_':
		return false
	}
	return true
}
