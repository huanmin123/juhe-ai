package oauthmgmt

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// sessionBackend 是会话存储的远端实现（生产为 Redis）；nil 时走进程内 map。
// 方法返回错误，调用方必须把基础设施失败与“会话不存在”区分开。
type sessionBackend interface {
	Set(ctx context.Context, key string, value json.RawMessage, ttl time.Duration) error
	Get(ctx context.Context, key string) (json.RawMessage, error)
	CompareDelete(ctx context.Context, key string, expected json.RawMessage) (bool, error)
}

// sessionStore mirrors the per-provider RuntimeStateStore session contract
// (getJson/setJson/compareDeleteJson). Production uses Redis so an authorize
// request on gateway A and its callback on gateway B share the same session;
// tests and chain-disabled compositions keep the TTL-bounded in-process map.
// Every provider namespace (openai-oauth:sessions, anthropic-oauth:sessions,
// gemini-oauth:sessions, grok-oauth:sessions) shares this store with a
// "{namespace}:{sessionId}" key; compareDeleteJson keeps the single-consumption
// guarantee for OAuth code exchanges.
type sessionStore struct {
	mu      sync.Mutex
	entries map[string]sessionEntry
	now     func() time.Time
	remote  sessionBackend
}

type sessionEntry struct {
	value     json.RawMessage
	expiresAt time.Time
}

const (
	oauthSessionTTL = 30 * time.Minute
	// oauthSessionMaxEntries bounds the in-process fallback: abandoned
	// authorization flows only ever delete their own key (get/compareDelete
	// 的惰性删除)，未完成授权会单调积累内存，所以对齐
	// kernel.DeduplicationStore 的 maxEntries+trim 先例——写入时顺带清扫
	// 过期项并按上限裁剪。Redis 路径由 TTL 约束，不需要该上限。
	oauthSessionMaxEntries = 5_000
)

func newSessionStore(now func() time.Time) *sessionStore {
	if now == nil {
		now = time.Now
	}
	return &sessionStore{entries: map[string]sessionEntry{}, now: now}
}

func (s *sessionStore) setBackend(backend sessionBackend) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remote = backend
}

func (s *sessionStore) key(namespace, sessionID string) string {
	return namespace + ":" + sessionID
}

// set keeps the historical best-effort in-process contract for tests and
// static fixtures; production write paths use setJSON and surface errors.
func (s *sessionStore) set(namespace, sessionID string, value any, ttl time.Duration) {
	_ = s.setJSON(context.Background(), namespace, sessionID, value, ttl)
}

// setJSON stores the session in the configured backend. Redis writes use the
// canonical JSON encoding so compareDelete can do an exact byte comparison
// inside its Lua script.
func (s *sessionStore) setJSON(ctx context.Context, namespace, sessionID string, value any, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = time.Millisecond
	}
	key := s.key(namespace, sessionID)
	s.mu.Lock()
	remote := s.remote
	s.mu.Unlock()
	if remote != nil {
		raw, err := canonicalSessionJSON(value)
		if err != nil {
			return err
		}
		return remote.Set(ctx, key, raw, ttl)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.entries[key] = sessionEntry{
		value:     raw,
		expiresAt: now.Add(ttl),
	}
	s.evictLocked(key, now)
	return nil
}

// evictLocked keeps the store bounded (caller holds mu): first sweep the
// expired entries, then trim the overflow below maxEntries, protecting the
// just-written key (mirror of DeduplicationStore cleanupExpiredLocked +
// trimIfNeeded; map iteration order is unspecified, so overflow eviction is
// arbitrary-but-bounded exactly like the precedent).
func (s *sessionStore) evictLocked(protectedKey string, now time.Time) {
	for key, entry := range s.entries {
		if !entry.expiresAt.After(now) {
			delete(s.entries, key)
		}
	}
	if len(s.entries) <= oauthSessionMaxEntries {
		return
	}
	overflow := len(s.entries) - oauthSessionMaxEntries
	removed := 0
	for key := range s.entries {
		if key == protectedKey {
			continue
		}
		delete(s.entries, key)
		removed++
		if removed >= overflow {
			break
		}
	}
}

// get keeps the historical best-effort contract for tests and static
// fixtures; production read paths use getJSON and surface errors.
func (s *sessionStore) get(namespace, sessionID string) json.RawMessage {
	raw, _ := s.getJSON(context.Background(), namespace, sessionID)
	return raw
}

// getJSON returns the live session JSON, or nil when missing/expired. A
// non-nil error is an infrastructure failure, never a cache miss.
func (s *sessionStore) getJSON(ctx context.Context, namespace, sessionID string) (json.RawMessage, error) {
	key := s.key(namespace, sessionID)
	s.mu.Lock()
	remote := s.remote
	s.mu.Unlock()
	if remote != nil {
		return remote.Get(ctx, key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return nil, nil
	}
	if !entry.expiresAt.After(s.now()) {
		delete(s.entries, key)
		return nil, nil
	}
	return entry.value, nil
}

// compareDelete keeps the historical bool-only contract for tests and static
// fixtures; production consume paths use compareDeleteJSON.
func (s *sessionStore) compareDelete(namespace, sessionID string, expected any) bool {
	ok, _ := s.compareDeleteJSON(context.Background(), namespace, sessionID, expected)
	return ok
}

// compareDeleteJSON deletes the session only when its stored JSON matches the
// expected value (Node compareDeleteJson single-consumption semantics).
func (s *sessionStore) compareDeleteJSON(ctx context.Context, namespace, sessionID string, expected any) (bool, error) {
	key := s.key(namespace, sessionID)
	s.mu.Lock()
	remote := s.remote
	s.mu.Unlock()
	if remote != nil {
		rawExpected, err := canonicalSessionJSON(expected)
		if err != nil {
			return false, nil
		}
		return remote.CompareDelete(ctx, key, rawExpected)
	}
	rawExpected, err := json.Marshal(expected)
	if err != nil {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return false, nil
	}
	if !entry.expiresAt.After(s.now()) {
		delete(s.entries, key)
		return false, nil
	}
	var current, wanted any
	if json.Unmarshal(entry.value, &current) != nil || json.Unmarshal(rawExpected, &wanted) != nil {
		return false, nil
	}
	// Mirror JSON.stringify equality: canonical re-encode both sides.
	encodedCurrent, errCurrent := json.Marshal(current)
	encodedWanted, errWanted := json.Marshal(wanted)
	if errCurrent != nil || errWanted != nil || string(encodedCurrent) != string(encodedWanted) {
		return false, nil
	}
	delete(s.entries, key)
	return true, nil
}

// canonicalSessionJSON normalizes a value to the same map-key-sorted encoding
// that json.Unmarshal + json.Marshal produces, so Redis compareDelete can
// compare raw bytes without a round trip.
func canonicalSessionJSON(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}
