// Package chatassets carries the chat asset subdomain split out of
// internal/chat (REFACTOR-0006 phase C): the chat_assets /
// chat_user_asset_usage / chat_asset_references / chat_image_generations
// persistence family, the upload/deletion claim lifecycle inputs and the
// asset API metadata mapping. SQL semantics and Chinese error strings mirror
// the Node chat-assets repositories byte for byte.
//
// Dependency direction: facade root (internal/chat) -> chatassets, one way.
// The SQL dialect, clock/id sources, time primitives and the domain error
// factory stay owned by the facade root and are injected here as narrow
// function ports (Ports), so the package never imports internal/chat.
package chatassets

import "database/sql"

// queryer mirrors the facade root's transaction/query abstraction; *sql.DB
// and *sql.Tx satisfy it structurally.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

// Ports is the minimal dependency surface of the asset subdomain on the
// facade root, wired once by chat.Store at construction (no interface
// indirection on any per-request path; every field is a plain function).
type Ports struct {
	// Table qualifies a table name for the active dialect.
	Table func(name string) string
	// Bind rewrites ? placeholders for the active dialect.
	Bind func(query string) string
	// LockSuffix returns the row-lock suffix (" FOR UPDATE" on PostgreSQL).
	LockSuffix func() string
	// NowISO renders the injected clock as millisecond ISO-8601.
	NowISO func() string
	// NewID generates prefixed asset ids.
	NewID func(prefix string) string
	// RequireRFC3339Instant canonicalizes required RFC3339 instants.
	RequireRFC3339Instant func(value, label string) (string, error)
	// AddDays performs UTC calendar-day arithmetic on RFC3339 instants.
	AddDays func(value string, days int, label string) (string, error)
	// RFC3339Millis mirrors rfc3339InstantMilliseconds.
	RFC3339Millis func(value string) (int64, bool)
	// UniqueStrings deduplicates and drops empty strings, order kept.
	UniqueStrings func(values []string) []string
	// Placeholders renders count SQL placeholders.
	Placeholders func(count int) string
	// TrimSpace mirrors the facade's trimSpace helper.
	TrimSpace func(value string) string
	// DomainError builds the facade's *chat.DomainError so error identity is
	// preserved across the package boundary.
	DomainError func(message string) error
}

// AssetStore carries the asset persistence methods split off chat.Store
// (25 of the original 26; the compaction source page loader stayed in the
// facade root with its context.go vocabulary). It holds the db handle
// directly and receives the dialect/clock ports from the facade root.
type AssetStore struct {
	db    *sql.DB
	pg    bool
	ports Ports
}

// NewAssetStore builds the asset subdomain service.
func NewAssetStore(db *sql.DB, postgres bool, ports Ports) *AssetStore {
	return &AssetStore{db: db, pg: postgres, ports: ports}
}
