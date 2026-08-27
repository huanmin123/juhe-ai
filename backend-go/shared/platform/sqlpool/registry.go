// Package sqlpool contains the process-local lifecycle and reference-counting
// boundary for database/sql pools. It owns no schema, role, transaction, or
// business policy; callers provide the driver-specific opener.
package sqlpool

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	// MaxIdleConns prevents a high concurrency ceiling from becoming a high
	// number of permanently reserved PostgreSQL sessions.
	MaxIdleConns = 10
	// MaxConnIdleTime releases unused database/sql connections after the
	// platform-wide idle window.
	MaxConnIdleTime = 10 * time.Minute
)

type Registry struct {
	mu       sync.Mutex
	entries  map[Key]*entry
	observer func(PoolEvent)
}

// PoolEvent is emitted after a pool lifecycle operation. URL is deliberately
// excluded so a DSN password can never leak through an instrumentation hook.
type PoolEvent struct {
	Kind    string
	Role    string
	MaxOpen int
	MaxIdle int
	Refs    int
	DBStats sql.DBStats
}

const (
	PoolEventOpen    = "open"
	PoolEventReuse   = "reuse"
	PoolEventRelease = "release"
	PoolEventClose   = "close"
)

// PoolSnapshot is a point-in-time view of every pool owned by the registry.
// It is safe to expose to metrics/logging code; it contains no credentials.
type PoolSnapshot struct {
	Role    string
	MaxOpen int
	MaxIdle int
	Refs    int
	DBStats sql.DBStats
}

type Key struct {
	URL  string
	Role string
}

type entry struct {
	db      *sql.DB
	refs    int
	maxIdle int
}

type Handle struct {
	registry *Registry
	key      Key
	db       *sql.DB
	once     sync.Once
}

func NewRegistry() *Registry {
	return &Registry{entries: make(map[Key]*entry)}
}

// SetObserver installs an optional lifecycle hook. The hook is invoked after
// the registry lock is released and must be non-blocking; callers can bridge
// it to slog or Prometheus without changing pool acquisition call sites.
func (r *Registry) SetObserver(observer func(PoolEvent)) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.observer = observer
	r.mu.Unlock()
}

// Stats returns credential-free snapshots for all pools in this registry.
func (r *Registry) Stats() []PoolSnapshot {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshots := make([]PoolSnapshot, 0, len(r.entries))
	for key, current := range r.entries {
		if current == nil || current.db == nil {
			continue
		}
		stats := current.db.Stats()
		snapshots = append(snapshots, PoolSnapshot{Role: key.Role, MaxOpen: stats.MaxOpenConnections, MaxIdle: current.maxIdle, Refs: current.refs, DBStats: stats})
	}
	return snapshots
}

// Acquire opens or reuses a pool identified by the exact URL and logical
// role. The opener stays in the project adapter so this shared package does
// not import a database driver.
func (r *Registry) Acquire(open func() (*sql.DB, error), url, role string, maxOpen, maxIdle int) (*Handle, error) {
	if r == nil {
		return nil, errors.New("sql pool registry 未初始化")
	}
	if open == nil || url == "" || role == "" {
		return nil, errors.New("sql pool opener、URL、role 不能为空")
	}
	if err := ValidatePoolLimits(maxOpen, maxIdle); err != nil {
		return nil, err
	}
	key := Key{URL: url, Role: role}
	r.mu.Lock()
	if r.entries == nil {
		r.entries = make(map[Key]*entry)
	}
	if current := r.entries[key]; current != nil {
		if maxOpen > current.db.Stats().MaxOpenConnections {
			current.db.SetMaxOpenConns(maxOpen)
		}
		configurePool(current.db, maxIdle)
		current.maxIdle = maxIdle
		current.refs++
		handle := &Handle{registry: r, key: key, db: current.db}
		event := r.eventLocked(PoolEventReuse, key, current)
		observer := r.observer
		r.mu.Unlock()
		emit(observer, event)
		return handle, nil
	}
	db, err := open()
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if db == nil {
		r.mu.Unlock()
		return nil, errors.New("sql pool opener 返回空数据库连接")
	}
	db.SetMaxOpenConns(maxOpen)
	configurePool(db, maxIdle)
	r.entries[key] = &entry{db: db, refs: 1, maxIdle: maxIdle}
	entry := r.entries[key]
	event := r.eventLocked(PoolEventOpen, key, entry)
	observer := r.observer
	r.mu.Unlock()
	emit(observer, event)
	return &Handle{registry: r, key: key, db: db}, nil
}

func (r *Registry) eventLocked(kind string, key Key, current *entry) PoolEvent {
	stats := current.db.Stats()
	return PoolEvent{Kind: kind, Role: key.Role, MaxOpen: stats.MaxOpenConnections, MaxIdle: current.maxIdle, Refs: current.refs, DBStats: stats}
}

func emit(observer func(PoolEvent), event PoolEvent) {
	if observer != nil {
		observer(event)
	}
}

// ValidatePoolLimits keeps the high connection ceiling independent from the
// small, bounded cache of idle sessions. Callers should validate at config
// load time so a stale deployment value fails before a pool is opened.
func ValidatePoolLimits(maxOpen, maxIdle int) error {
	if maxOpen < 1 || maxIdle < 1 || maxIdle > maxOpen || maxIdle > MaxIdleConns {
		return fmt.Errorf("sql pool max open/idle 必须满足 1 <= idle <= min(open, %d)，实际为 %d/%d", MaxIdleConns, maxOpen, maxIdle)
	}
	return nil
}

func configurePool(db *sql.DB, maxIdle int) {
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxIdleTime(MaxConnIdleTime)
}

func (h *Handle) DB() *sql.DB {
	if h == nil {
		return nil
	}
	return h.db
}

func (h *Handle) Close() error {
	if h == nil || h.registry == nil {
		return nil
	}
	var err error
	var event PoolEvent
	var observer func(PoolEvent)
	h.once.Do(func() {
		h.registry.mu.Lock()
		current := h.registry.entries[h.key]
		if current == nil {
			h.registry.mu.Unlock()
			return
		}
		current.refs--
		if current.refs > 0 {
			event = h.registry.eventLocked(PoolEventRelease, h.key, current)
			observer = h.registry.observer
			h.registry.mu.Unlock()
			emit(observer, event)
			return
		}
		delete(h.registry.entries, h.key)
		err = current.db.Close()
		event = PoolEvent{Kind: PoolEventClose, Role: h.key.Role, MaxOpen: current.db.Stats().MaxOpenConnections, MaxIdle: current.maxIdle, Refs: 0, DBStats: current.db.Stats()}
		observer = h.registry.observer
		h.registry.mu.Unlock()
		emit(observer, event)
	})
	return err
}

func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var first error
	for key, current := range r.entries {
		if err := current.db.Close(); err != nil && first == nil {
			first = fmt.Errorf("关闭 sql pool %s/%s: %w", key.Role, key.URL, err)
		}
		delete(r.entries, key)
	}
	return first
}
