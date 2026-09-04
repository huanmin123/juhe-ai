package openaicompat

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Test scope constants (越权 checks use a second scope).
const (
	testScopeA = "sys-a"
	testKeyA   = "key-a"
	testScopeB = "sys-b"
	testKeyB   = "key-b"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:openaicompat-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range openaicompatTestSchema() {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func openaicompatTestSchema() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS openai_compatible_files (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			api_key_id TEXT NOT NULL,
			purpose TEXT NOT NULL,
			container_id TEXT,
			filename TEXT NOT NULL,
			bytes INTEGER NOT NULL,
			media_type TEXT,
			storage_key TEXT NOT NULL,
			sha256 TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'processed',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			expires_at TEXT,
			deleted_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS openai_compatible_vector_stores (
			id TEXT PRIMARY KEY,
			system_account_id TEXT NOT NULL,
			api_key_id TEXT NOT NULL,
			name TEXT,
			description TEXT,
			metadata_json TEXT NOT NULL DEFAULT '{}',
			bytes INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			expires_after_anchor TEXT,
			expires_after_days INTEGER,
			expires_at TEXT,
			deleted_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS openai_compatible_vector_store_files (
			vector_store_id TEXT NOT NULL,
			file_id TEXT NOT NULL,
			system_account_id TEXT NOT NULL,
			api_key_id TEXT NOT NULL,
			attributes_json TEXT NOT NULL DEFAULT '{}',
			chunking_strategy_json TEXT NOT NULL DEFAULT '{}',
			status TEXT NOT NULL,
			usage_bytes INTEGER NOT NULL DEFAULT 0,
			last_error_json TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			deleted_at TEXT,
			PRIMARY KEY (vector_store_id, file_id)
		)`,
		`CREATE TABLE IF NOT EXISTS openai_compatible_vector_store_chunks (
			id TEXT PRIMARY KEY,
			vector_store_id TEXT NOT NULL,
			file_id TEXT NOT NULL,
			system_account_id TEXT NOT NULL,
			api_key_id TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			content_text TEXT NOT NULL,
			content_preview TEXT NOT NULL,
			token_estimate INTEGER NOT NULL,
			keyword_index_text TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
	}
}

// fixedClock keeps timestamps deterministic across store and route layers.
func fixedClock(t *testing.T) func() time.Time {
	t.Helper()
	base := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	ticks := 0
	var mu sync.Mutex
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		ticks++
		return base.Add(time.Duration(ticks) * time.Millisecond)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(newTestDB(t), false, WithNow(fixedClock(t)))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// routeEnv mounts the Deps against a kernel with header-based scopes:
// requests carrying X-Test-Scope: sys-a|sys-b resolve to the matching scope;
// other requests have no runtime (401 contract).
type routeEnv struct {
	Deps      *Deps
	Server    *httptest.Server
	FilesRoot string
}

func newRouteEnv(t *testing.T, mutate func(*Deps)) *routeEnv {
	t.Helper()
	store := newTestStore(t)
	root := t.TempDir()
	deps := &Deps{
		Store: store,
		Config: Config{
			FilesRoot:    root,
			Port:         3111,
			MaxFileBytes: DefaultMaxFileBytes,
		},
		Scope: func(r *http.Request) *GatewayScope {
			switch r.Header.Get("X-Test-Scope") {
			case testScopeA:
				return &GatewayScope{SystemAccountID: testScopeA, APIKeyID: testKeyA}
			case testScopeB:
				return &GatewayScope{SystemAccountID: testScopeB, APIKeyID: testKeyB}
			default:
				return nil
			}
		},
		// Deterministic indexing for tests (Node fires this as a detached
		// promise; the default Deps behavior is a goroutine).
		IndexAsync: func(task func()) { task() },
	}
	if mutate != nil {
		mutate(deps)
	}
	kernel := &testKernel{}
	deps.Mount(kernel)
	server := httptest.NewServer(kernel.mux)
	t.Cleanup(server.Close)
	return &routeEnv{Deps: deps, Server: server, FilesRoot: root}
}

// do performs a request and returns status + raw body.
func (e *routeEnv) do(t *testing.T, method, path, scope, contentType string, body io.Reader) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, e.Server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if scope != "" {
		request.Header.Set("X-Test-Scope", scope)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(raw)
}

func (e *routeEnv) doJSON(t *testing.T, method, path, scope, body string) (int, map[string]any) {
	t.Helper()
	status, raw := e.do(t, method, path, scope, "application/json", strings.NewReader(body))
	return status, decodeJSON(t, raw)
}

func decodeJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return decoded
}

// testKernel adapts the Deps.Mount registration interface to a plain mux.
type testKernel struct {
	mux *http.ServeMux
}

func (k *testKernel) Register(pattern string, handler http.Handler) {
	if k.mux == nil {
		k.mux = http.NewServeMux()
	}
	k.mux.Handle(pattern, handler)
}

// createTestFile seeds a file record + physical object.
func createTestFile(t *testing.T, store *Store, root, id, purpose string, content []byte, mediaType string, containerID *string) FileRecord {
	t.Helper()
	storageKey := StorageKeyForFile(id)
	path, err := EnsureFileObjectParent(root, storageKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	mediaTypePtr := (*string)(nil)
	if mediaType != "" {
		mediaTypePtr = &mediaType
	}
	sha := sha256Hex(content)
	record, err := store.CreateFile(t.Context(), FileCreateInput{
		ID:              id,
		SystemAccountID: testScopeA,
		APIKeyID:        testKeyA,
		Purpose:         purpose,
		ContainerID:     containerID,
		Filename:        id + ".txt",
		Bytes:           int64(len(content)),
		MediaType:       mediaTypePtr,
		StorageKey:      storageKey,
		SHA256:          sha,
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// execSQL runs raw SQL against the store's DB for fixture setup.
func execSQL(t *testing.T, store *Store, query string, args ...any) {
	t.Helper()
	if _, err := store.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
