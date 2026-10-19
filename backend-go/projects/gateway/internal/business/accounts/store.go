// Package accounts contains the Gateway-owned Business account transaction
// boundary. It deliberately depends only on database/sql: no Node, IPC,
// queue, or cross-process bridge is involved.
package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrOwnerGate        = errors.New("Business SQLite owner handoff gate is not satisfied")
	ErrNotFound         = errors.New("account not found")
	ErrRevisionConflict = errors.New("account revision conflict")
)

// OwnerGate is external, auditable evidence. A partial handoff never permits
// a write, even when the schema happens to be present.
type OwnerGate struct {
	Confirmed         bool
	SchemaReady       bool
	NodeWriterStopped bool
}

func (g OwnerGate) Ready() bool { return g.Confirmed && g.SchemaReady && g.NodeWriterStopped }

type Store struct {
	db       *sql.DB
	postgres bool
	gate     OwnerGate
	now      func() time.Time
	// invalidator carries the post-commit gateway runtime invalidation of the
	// committed create/patch/delete writes (see invalidation.go); nil keeps
	// it off.
	invalidator RuntimeInvalidator
}

func NewStore(db *sql.DB, postgres bool, gate OwnerGate) (*Store, error) {
	if db == nil {
		return nil, errors.New("accounts database is required")
	}
	return &Store{db: db, postgres: postgres, gate: gate, now: time.Now}, nil
}

func (s *Store) requireOwner() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}

// CheckContract is read-only and must run before a management listener binds.
func (s *Store) CheckContract(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrOwnerGate
	}
	// account_entry_mappings 是映射聚合的现行载体（供应商与上游协议面重构
	// 设计 §5.1；旧 account_model_mappings 表已随 A 路线撤除，fresh DDL 不再
	// 建表）。account_connections/account_endpoint_modes 是映射 target 解析的
	// 连接事实源（§2.1），与聚合写路径同批纳入契约校验。
	for _, table := range []string{"accounts", "account_supported_models", "account_entry_mappings", "account_connections", "account_endpoint_modes", "account_tags", "account_tag_bindings", "group_accounts", "account_api_key_runtime_states"} {
		if _, err := s.db.ExecContext(ctx, "SELECT 1 FROM "+s.table(table)+" LIMIT 0"); err != nil {
			return fmt.Errorf("verify accounts relation %s: %w", table, err)
		}
	}
	return nil
}

func (s *Store) table(name string) string {
	if s.postgres {
		return "juhe_business." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if !s.postgres {
		return query
	}
	var b strings.Builder
	index := 0
	for _, r := range query {
		if r == '?' {
			index++
			b.WriteString(fmt.Sprintf("$%d", index))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Store) begin(ctx context.Context) (*sql.Tx, error) { return s.db.BeginTx(ctx, nil) }

func (s *Store) stamp() string { return s.now().UTC().Format(time.RFC3339Nano) }
