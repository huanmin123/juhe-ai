package oauthmgmt

import (
	"encoding/json"
	"sync"
	"time"
)

// sessionStore mirrors the per-provider RuntimeStateStore session contract
// (getJson/setJson/compareDeleteJson) with an in-process, TTL-bounded map.
// Every provider namespace (openai-oauth:sessions, anthropic-oauth:sessions,
// gemini-oauth:sessions, grok-oauth:sessions) shares this store with a
// "{namespace}:{sessionId}" key; compareDeleteJson keeps the single-consumption
// guarantee for OAuth code exchanges.
type sessionStore struct {
	mu      sync.Mutex
	entries map[string]sessionEntry
	now     func() time.Time
}

type sessionEntry struct {
	value     json.RawMessage
	expiresAt time.Time
}

const oauthSessionTTL = 30 * time.Minute

func newSessionStore(now func() time.Time) *sessionStore {
	if now == nil {
		now = time.Now
	}
	return &sessionStore{entries: map[string]sessionEntry{}, now: now}
}

func (s *sessionStore) key(namespace, sessionID string) string {
	return namespace + ":" + sessionID
}

func (s *sessionStore) set(namespace, sessionID string, value any, ttl time.Duration) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	if ttl <= 0 {
		ttl = time.Millisecond
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[s.key(namespace, sessionID)] = sessionEntry{
		value:     raw,
		expiresAt: s.now().Add(ttl),
	}
}

// get returns the live session JSON, or nil when missing/expired.
func (s *sessionStore) get(namespace, sessionID string) json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.key(namespace, sessionID)
	entry, ok := s.entries[key]
	if !ok {
		return nil
	}
	if !entry.expiresAt.After(s.now()) {
		delete(s.entries, key)
		return nil
	}
	return entry.value
}

// compareDelete deletes the session only when its stored JSON matches the
// expected value (Node compareDeleteJson single-consumption semantics).
func (s *sessionStore) compareDelete(namespace, sessionID string, expected any) bool {
	rawExpected, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := s.key(namespace, sessionID)
	entry, ok := s.entries[key]
	if !ok {
		return false
	}
	if !entry.expiresAt.After(s.now()) {
		delete(s.entries, key)
		return false
	}
	var current, wanted any
	if json.Unmarshal(entry.value, &current) != nil || json.Unmarshal(rawExpected, &wanted) != nil {
		return false
	}
	// Mirror JSON.stringify equality: canonical re-encode both sides.
	encodedCurrent, errCurrent := json.Marshal(current)
	encodedWanted, errWanted := json.Marshal(wanted)
	if errCurrent != nil || errWanted != nil || string(encodedCurrent) != string(encodedWanted) {
		return false
	}
	delete(s.entries, key)
	return true
}
