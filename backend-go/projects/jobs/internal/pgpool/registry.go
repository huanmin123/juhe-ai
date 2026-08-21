package pgpool

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Registry shares database/sql PostgreSQL pools inside one jobs process.
// Pools are keyed by the exact connection URL and logical role.  Different
// URLs or roles intentionally remain isolated because they may have different
// permissions and transaction contracts.
type Registry struct {
	mu      sync.Mutex
	entries map[Key]*entry
}

type Key struct {
	URL  string
	Role string
}

type entry struct {
	db   *sql.DB
	refs int
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

func (r *Registry) Acquire(driver, url, role string, maxOpen, maxIdle int) (*Handle, error) {
	if r == nil {
		return nil, errors.New("postgres pool registry 未初始化")
	}
	if driver == "" || url == "" || role == "" {
		return nil, errors.New("postgres pool driver、URL、role 不能为空")
	}
	if maxOpen < 1 || maxIdle < 1 || maxIdle > maxOpen {
		return nil, fmt.Errorf("postgres pool max open/idle 必须满足 1 <= idle <= open，实际为 %d/%d", maxOpen, maxIdle)
	}
	key := Key{URL: url, Role: role}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[Key]*entry)
	}
	if existing := r.entries[key]; existing != nil {
		// A later consumer may request a larger pool.  Only grow the shared
		// pool; never silently shrink a pool already in use.
		if current := existing.db.Stats().MaxOpenConnections; maxOpen > current {
			existing.db.SetMaxOpenConns(maxOpen)
		}
		existing.db.SetMaxIdleConns(maxIdle)
		existing.refs++
		return &Handle{registry: r, key: key, db: existing.db}, nil
	}
	db, err := sql.Open(driver, url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	r.entries[key] = &entry{db: db, refs: 1}
	return &Handle{registry: r, key: key, db: db}, nil
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
	h.once.Do(func() {
		h.registry.mu.Lock()
		defer h.registry.mu.Unlock()
		current := h.registry.entries[h.key]
		if current == nil {
			return
		}
		current.refs--
		if current.refs > 0 {
			return
		}
		delete(h.registry.entries, h.key)
		err = current.db.Close()
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
			first = fmt.Errorf("关闭 postgres pool %s/%s: %w", key.Role, key.URL, err)
		}
		delete(r.entries, key)
	}
	return first
}
