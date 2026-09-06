package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mutation deduplication mirrors modules/deduplication: an in-memory
// claim/complete store keyed by actor:scope:METHOD:operationKey:fingerprint
// with bounded entries and periodic cleanup.

const (
	dedupDefaultProcessingTTL = 120 * time.Second
	dedupDefaultSucceededTTL  = 60 * time.Second
	dedupDefaultFailedTTL     = 10 * time.Second
	dedupMaxEntries           = 5_000
	dedupCleanupInterval      = 30 * time.Second
	dedupCleanupBatch         = 128
)

type DedupStatus string

const (
	DedupProcessing DedupStatus = "processing"
	DedupSucceeded  DedupStatus = "succeeded"
	DedupFailed     DedupStatus = "failed"
)

type DedupEntry struct {
	Key          string
	OperationKey string
	Status       DedupStatus
	StartedAt    time.Time
	FinishedAt   time.Time
	ExpiresAt    time.Time
}

// HashStableValue hashes a JSON-like value with sorted object keys
// (mirror of hashStableValue).
func HashStableValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		encoded = []byte("null")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

type DeduplicationStore struct {
	mu    sync.Mutex
	entry map[string]*DedupEntry
	clock func() time.Time
	next  time.Time
}

func NewDeduplicationStore(clock func() time.Time) *DeduplicationStore {
	if clock == nil {
		clock = time.Now
	}
	return &DeduplicationStore{entry: make(map[string]*DedupEntry), clock: clock}
}

func (s *DeduplicationStore) Claim(key, operationKey string, processingTTL time.Duration) (bool, DedupEntry) {
	now := s.clock()
	s.cleanupIfNeeded(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.entry[key]; ok && existing.ExpiresAt.After(now) {
		return false, *existing
	}
	if processingTTL <= 0 {
		processingTTL = dedupDefaultProcessingTTL
	}
	entry := &DedupEntry{Key: key, OperationKey: operationKey, Status: DedupProcessing, StartedAt: now, ExpiresAt: now.Add(processingTTL)}
	delete(s.entry, key)
	s.entry[key] = entry
	s.trimIfNeeded(now, key)
	return true, *entry
}

// DedupNoRetention 显式声明「完成后不保留去重条目」（Node mutationGuard 的
// succeededTtlMs/failedTtlMs: 0 语义）。0 在 Go 侧表示「用默认 TTL」
// （既有调用方依赖），负值哨兵用于区分两种意图；冻结时钟下正的极小 TTL
// 永不过期，因此不保留必须走删除路径。
const DedupNoRetention time.Duration = -1

func (s *DeduplicationStore) Complete(key string, status DedupStatus, succeededTTL, failedTTL time.Duration) {
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entry[key]
	if !ok || entry.Status != DedupProcessing {
		return
	}
	ttl := failedTTL
	if status == DedupSucceeded {
		ttl = succeededTTL
	}
	if ttl == DedupNoRetention {
		delete(s.entry, key)
		return
	}
	if ttl <= 0 {
		if status == DedupSucceeded {
			ttl = dedupDefaultSucceededTTL
		} else {
			ttl = dedupDefaultFailedTTL
		}
	}
	entry.Status = status
	entry.FinishedAt = now
	entry.ExpiresAt = now.Add(ttl)
}

func (s *DeduplicationStore) cleanupIfNeeded(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Before(s.next) && len(s.entry) <= dedupMaxEntries {
		return
	}
	s.cleanupExpiredLocked(now, dedupCleanupBatch)
	s.next = now.Add(dedupCleanupInterval)
}

func (s *DeduplicationStore) cleanupExpiredLocked(now time.Time, limit int) {
	inspected := 0
	for key, entry := range s.entry {
		if inspected >= limit {
			break
		}
		if !entry.ExpiresAt.After(now) {
			delete(s.entry, key)
		}
		inspected++
	}
}

func (s *DeduplicationStore) trimIfNeeded(now time.Time, protectedKey string) {
	if len(s.entry) <= dedupMaxEntries {
		return
	}
	s.cleanupExpiredLocked(now, dedupCleanupBatch)
	if len(s.entry) <= dedupMaxEntries {
		return
	}
	overflow := len(s.entry) - dedupMaxEntries
	removed := 0
	for key := range s.entry {
		if key == protectedKey {
			continue
		}
		delete(s.entry, key)
		removed++
		if removed >= overflow {
			break
		}
	}
}

// ActorResolver supplies the dedup actor (system account id). The session
// middleware (K2) installs the real resolver; unauthenticated callers are
// "anonymous" exactly like the Node guard.
type ActorResolver func(r *http.Request) string

// FingerprintFunc extracts the dedup fingerprint from a request. The guard
// buffers the JSON body into the request context first, so fingerprints read
// fields via BodyField/ParsedBody like Node closures read req.body. Returning
// an error rejects the request with 400 (mirror of the Node guard's
// fingerprint error path).
type FingerprintFunc func(r *http.Request) (any, error)

// ParsedBody returns the JSON body the guard decoded into context (nil when
// absent).
func ParsedBody(r *http.Request) map[string]any {
	if value, ok := r.Context().Value(bodyKey{}).(map[string]any); ok {
		return value
	}
	return nil
}

// BodyField mirrors bodyField(req, name).
func BodyField(r *http.Request, name string) any {
	body := ParsedBody(r)
	if body == nil {
		return nil
	}
	return body[name]
}

// TextField mirrors textValue: trimmed string or "".
func TextField(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

// SortedTextValues mirrors sortedTextValues.
func SortedTextValues(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	sort.Strings(out)
	return out
}

type bodyKey struct{}

type MutationGuardOptions struct {
	OperationKey  string
	Scope         FingerprintFunc
	Fingerprint   FingerprintFunc
	ProcessingTTL time.Duration
	SucceededTTL  time.Duration
	FailedTTL     time.Duration
	Store         *DeduplicationStore
	Actor         ActorResolver
}

type fingerprintError struct{ message string }

func (e *fingerprintError) Error() string { return e.message }

// MutationGuardMiddleware mirrors mutationGuard: safe methods pass through,
// fingerprint failures 400, duplicate claims 409 with the status-specific
// message, completion follows the response outcome.
func MutationGuardMiddleware(options MutationGuardOptions) func(http.Handler) http.Handler {
	store := options.Store
	if store == nil {
		store = DefaultDeduplicationStore
	}
	actorResolver := options.Actor
	if actorResolver == nil {
		actorResolver = func(*http.Request) string { return "anonymous" }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if options.Fingerprint == nil {
				next.ServeHTTP(w, r)
				return
			}
			// system-api-app.ts mounts express.json + handleJsonBodyError at
			// the prefix level, BEFORE any router (and its mutationGuard).
			// Parser-level failures must therefore answer before the guard
			// can fingerprint or claim: an oversized JSON body answers 413
			// 请求体过大 and malformed JSON answers 400 请求体无效 without
			// ever creating a dedup entry. A body the Node parser would skip
			// (non-JSON media type or no body at all) passes through with an
			// empty parsed body, exactly like req.body = {} in Express.
			parsed, handled := applyJSONBodyParser(w, r)
			if handled {
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), bodyKey{}, parsed))
			fingerprint, err := options.Fingerprint(r)
			if err != nil {
				message := "请求参数无效"
				if ferr, ok := err.(*fingerprintError); ok && ferr.message != "" {
					message = ferr.message
				}
				WriteBadRequest(w, message)
				return
			}
			scope := ""
			if options.Scope != nil {
				scopeValue, err := options.Scope(r)
				if err != nil {
					WriteBadRequest(w, "请求参数无效")
					return
				}
				if text, ok := scopeValue.(string); ok {
					scope = text
				}
			}
			key := stringsJoinNonEmpty([]string{
				actorResolver(r), scope, r.Method, options.OperationKey, HashStableValue(fingerprint),
			}, ":")
			claimed, entry := store.Claim(key, options.OperationKey, options.ProcessingTTL)
			if !claimed {
				WriteJSON(w, http.StatusConflict, map[string]string{"message": duplicateMessage(entry.Status)})
				return
			}
			// Serve through the chain the guard received so guarded
			// responses keep every kernel layer (compression, method
			// contract, ...), then resolve the client-visible status the way
			// Node reads res.statusCode at 'finish'. Inside the kernel chain
			// the compression writer records the status at handler WriteHeader
			// (it defers the forward) and the shared tracking writer observes
			// every writer that forwards immediately; standalone guards fall
			// back to their own tracking wrapper.
			var local *localizeWriter
			if ResponseWriterFromContext(r.Context()) == nil {
				local = newLocalizeWriter(w)
				w = local
			}
			next.ServeHTTP(w, r)
			status := http.StatusOK
			if local != nil {
				status = local.status
			} else {
				lw := ResponseWriterFromContext(r.Context())
				if recorder, ok := w.(interface{ recordedStatus() (int, bool) }); ok {
					if recorded, recordedOK := recorder.recordedStatus(); recordedOK {
						status = recorded
					} else if lw.wroteHeader {
						status = lw.status
					}
				} else if lw.wroteHeader {
					status = lw.status
				}
			}
			if status == 0 {
				status = http.StatusOK
			}
			store.Complete(key, statusFromCode(status), options.SucceededTTL, options.FailedTTL)
		})
	}
}

func statusFromCode(code int) DedupStatus {
	if code >= 200 && code < 400 {
		return DedupSucceeded
	}
	return DedupFailed
}

// applyJSONBodyParser mirrors the express.json() + handleJsonBodyError stage
// that system-api-app.ts mounts ahead of every router: the body is read once,
// parser-level failures answer 413 请求体过大 / 400 请求体无效 before any
// dedup claim, and the raw body is restored for the downstream decoder.
// Non-JSON media types and bodyless requests skip parsing entirely (the
// body-parser default type check via type-is), leaving the parsed map nil
// like Express' req.body = {}. handled=true means the parser error response
// was already written.
func applyJSONBodyParser(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	shouldParse := jsonBodyMediaType(r.Header) && hasRequestBody(r)
	raw, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			if shouldParse {
				w.Header().Set("Cache-Control", "no-store")
				WriteError(w, http.StatusRequestEntityTooLarge, "请求体过大")
				return nil, true
			}
			// The Node parser never reads bodies it would skip, so the body
			// limit never fires for them; restore what arrived.
			r.Body = io.NopCloser(bytes.NewReader(raw))
			return nil, false
		}
		w.Header().Set("Cache-Control", "no-store")
		WriteError(w, http.StatusBadRequest, "请求体无效")
		return nil, true
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var parsed map[string]any
	if shouldParse && len(bytes.TrimSpace(raw)) > 0 {
		if !json.Valid(raw) {
			w.Header().Set("Cache-Control", "no-store")
			WriteError(w, http.StatusBadRequest, "请求体无效")
			return nil, true
		}
		// Arrays and scalars are valid JSON bodies for the parser (req.body
		// carries them); only object-shaped bodies feed the map, mirroring
		// bodyField(req, name) returning undefined otherwise.
		_ = json.Unmarshal(raw, &parsed)
	}
	return parsed, false
}

// mediaTypePattern validates type/subtype tokens the way media-typer does
// (invalid types make type-is return false).
var mediaTypePattern = regexp.MustCompile(`^[\w!#$%&'*+\-.^` + "`" + `|~]+/[\w!#$%&'*+\-.^` + "`" + `|~]+$`)

// jsonBodyMediaType mirrors body-parser json.js's default type check through
// type-is: only the exact media type application/json (parameters stripped,
// case-insensitive) is parsed. application/problem+json and other +json
// suffixes are NOT parsed by the default express.json() — verified against
// body-parser@1.20.5 + type-is@1.6.18.
func jsonBodyMediaType(header http.Header) bool {
	contentType := header.Get("Content-Type")
	if contentType == "" {
		return false
	}
	mediaType := strings.TrimSpace(contentType)
	if index := strings.IndexByte(mediaType, ';'); index >= 0 {
		mediaType = strings.TrimSpace(mediaType[:index])
	}
	mediaType = strings.ToLower(mediaType)
	if !mediaTypePattern.MatchString(mediaType) {
		return false
	}
	return mediaType == "application/json"
}

// hasRequestBody mirrors type-is hasBody: a transfer-encoding header or a
// numeric content-length header.
func hasRequestBody(r *http.Request) bool {
	if r.Header.Get("Transfer-Encoding") != "" {
		return true
	}
	contentLength := r.Header.Get("Content-Length")
	if contentLength == "" {
		return false
	}
	_, err := strconv.ParseInt(strings.TrimSpace(contentLength), 10, 64)
	return err == nil
}

func duplicateMessage(status DedupStatus) string {
	switch status {
	case DedupProcessing:
		return "请求正在处理中，请勿重复提交"
	case DedupFailed:
		return "请求刚刚失败，请稍后重试"
	default:
		return "该操作刚刚已处理，请刷新列表查看结果"
	}
}

func stringsJoinNonEmpty(parts []string, sep string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += sep
		}
		out += part
	}
	return out
}

// DefaultDeduplicationStore backs guards that do not inject their own store.
var DefaultDeduplicationStore = NewDeduplicationStore(nil)
