// Package accountsbalance is the balance & probe subdomain of the accounts
// slice (REFACTOR-0005 阶段 B): the M11 balance family projections (balance
// details, manual refresh candidate, force-activate CAS), the balance query
// config normalization/validation, the superseded-snapshot cleanup port, the
// OpenAI Codex OAuth usage snapshot read projection and the quota recovery
// policy normalization. The HTTP surface (m11_routes.go) and the route-side
// draft preparation (prepareBalanceDraft, whose output row the routes read
// field-by-field and whose inputs are all facade write-path helpers) stay in
// the accounts facade, which consumes this package through the forwarded
// Store methods.
//
// 依赖方向约束：accountsbalance 只 import accountscore（中立类型层），不
// import accounts 门面；Store 能力经 accountscore.StoreBase 窄端口由门面
// 装配注入，跨域读面（授权可见集合、批量派发修订推进、汇总行重读、可用性
// 调度门）经 Deps 函数端口注入（门面每次调用以活字段构造 Service，兼容测试
// 克隆 Store 后替换字段的语义）。
package accountsbalance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// Deps bundles the cross-domain function ports the balance family needs into
// the facade (each adapter closes over the live Store, so composition-root
// tests that clone a Store and swap fields keep observing the clone).
type Deps struct {
	// StatsDatabase is the dedicated juhe_stats handle behind the
	// account_usage_snapshots reads. Production SQLite opens the stats
	// database as a separate file; PostgreSQL and the single-file tests leave
	// it nil and the service falls back to the shared store handle.
	StatsDatabase *sql.DB
	// AuthorizedReadableIDs mirrors Store.authorizedReadableIDs: the
	// authorized-instance account id set for the scope viewer (authorized.go).
	AuthorizedReadableIDs func(ctx context.Context, access accountscore.AccessScope) map[string]bool
	// AdvanceBatchDispatchRevision mirrors Store.advanceBatchDispatchRevision
	// (batch_effects.go): the dispatch-revision family advance inside the
	// caller's transaction.
	AdvanceBatchDispatchRevision func(ctx context.Context, q accountscore.Queryer, accountID, transitionID string, nowMS int64) error
	// FindAccountSummary mirrors Store.findForceActivateSummary: the sanitized
	// account summary re-read (facade *ListItem behind any, nil when the row
	// is gone).
	FindAccountSummary func(ctx context.Context, accountID, ownerID string) (any, error)
	// ScheduleGate mirrors m11ScheduleAllowed: the availability-schedule gate
	// at an instant (schedule.go stays a facade surface; parse errors degrade
	// to always-allowed).
	ScheduleGate func(scheduleJSON string, now time.Time) bool
	// NewDispatchID mirrors the facade free function newID("dispatch")
	// (crypto.go): the original call site bypassed the store-injected newID
	// clock, so the port closes over the free function instead of StoreBase.
	NewDispatchID func() string
}

// Service carries the balance & probe subdomain state: the injected Store
// capability port plus the cross-domain function ports.
type Service struct {
	store accountscore.StoreBase
	deps  Deps
}

// New builds the subdomain service over the injected Store capability port.
func New(store accountscore.StoreBase, deps Deps) *Service {
	return &Service{store: store, deps: deps}
}

// StatsTable qualifies a juhe_stats table (PostgreSQL schema-qualified, bare
// on SQLite — the Go test database keeps one file).
func (s *Service) StatsTable(name string) string {
	if s.store.PG() {
		return "juhe_stats." + name
	}
	return name
}

// statsDB resolves the handle stats-table statements must run on: the
// attached stats database when present, else the shared store handle.
func (s *Service) statsDB() *sql.DB {
	if s.deps.StatsDatabase != nil {
		return s.deps.StatsDatabase
	}
	return s.store.DB()
}

// BalanceAPIKeyFingerprint mirrors accountBalanceApiKeyFingerprint: a stable
// server-side HMAC identity for one Key; the raw credential never leaves the
// backend. The HMAC key material is the store secret (runtimeConfig.secret).
func (s *Service) BalanceAPIKeyFingerprint(value string) string {
	if value == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(s.store.Secret()))
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
