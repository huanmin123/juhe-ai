// Package routestrategies owns the M06 vertical slice: route-strategy
// persistence (SQLite + PostgreSQL) and the /route-strategies + /
// my-route-strategies route family ported from
// backend/src/modules/route-strategies/route-strategies.routes.ts plus
// storage/route-strategy.repository.ts and
// route-strategy-group-binding-limits.ts. The slice covers the paginated list
// with binding snapshots (bindingCount/apiKeyCount/3-item preview),
// owner-scoped detail, create with five-mode config validation and
// transactional group-binding writes, strict partial PATCH with
// expectedUpdatedAt optimistic locking (409 策略路由已被其他操作更新，请刷新后重试)
// and whole-set binding replacement, plus delete protection for default
// strategies and API-Key-referenced strategies, with operation logging and
// the gateway runtime invalidation hook. The /options, /:id/edit-basic and
// /:id/speed-first-runtime reads, the authorized-grantee binding branch and
// the API-Key validation-cache + normal-route speed-first runtime cleanup
// hooks belong to their own slices.
package routestrategies

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// ConflictError maps to the duplicate strategy name errors — 409 in the route
// family (message contains 已存在, mirroring the Node includes('已存在') probe).
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// VersionConflictError maps to RouteStrategyVersionConflictError — 409 with
// the current row version so clients can refresh and retry.
type VersionConflictError struct {
	Message          string
	CurrentUpdatedAt string
}

func (e *VersionConflictError) Error() string { return e.Message }

// ValidationError maps to Node throw-Error paths rendered as 400 (config
// normalization, binding boundary, mode binding rules, delete guards).
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// RuntimeInvalidator is the K5 gateway runtime cache invalidation port
// (Node notifyGatewayRuntimeCacheInvalidation). *inval.Bus satisfies it; nil
// keeps the slice self-contained with no-op invalidation.
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// TopicGatewayRuntime mirrors the Node gateway runtime cache topic constant.
const TopicGatewayRuntime = "topic:gateway_runtime_cache"

// maxRouteStrategyGroupBindings mirrors route-strategy-group-binding-limits.ts.
const maxRouteStrategyGroupBindings = 20

// routeStrategyGroupBoundaryError mirrors ROUTE_STRATEGY_GROUP_BOUNDARY_ERROR.
const routeStrategyGroupBoundaryError = "策略路由只能绑定自己的分组或有效授权给自己的分组"

// AccessScope mirrors storage/access-scope.ts for the owner-view subset this
// slice implements: admins see everything unless a filter is set; users are
// pinned to their own rows (forceSelfAccessScope).
type AccessScope struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

// manageableID mirrors manageableSystemAccountId: empty result + admin means
// unscoped (all rows); non-admins always scope to themselves.
func (a AccessScope) manageableID() string {
	if a.IsAdmin {
		return a.FilterID
	}
	return a.ViewerID
}

func (a AccessScope) canAccessAll() bool { return a.IsAdmin }

// writeSystemAccountID mirrors writeSystemAccountId: the owner stamped on
// newly created rows (manageable ?? current viewer).
func (a AccessScope) writeSystemAccountID() (string, error) {
	if id := a.manageableID(); id != "" {
		return id, nil
	}
	if a.ViewerID != "" {
		return a.ViewerID, nil
	}
	return "", &ValidationError{Message: "缺少系统账户上下文"}
}

// canManageOwner mirrors canManageApiKeyOwner: scoped ids must match;
// unscoped admin manages everything.
func (a AccessScope) canManageOwner(ownerID string) bool {
	if scoped := a.manageableID(); scoped != "" {
		return scoped == ownerID
	}
	return a.canAccessAll()
}

// Store is the dual-mode route-strategy persistence.
type Store struct {
	db    *sql.DB
	pg    bool
	now   func() time.Time
	newI  func(prefix string) string
	inval RuntimeInvalidator
}

// NewStore builds the store; inval may be nil (no-op invalidation until K5
// wires the bus).
func NewStore(db *sql.DB, postgres bool, now func() time.Time, newID func(string) string, inval RuntimeInvalidator) (*Store, error) {
	if db == nil {
		return nil, errors.New("routestrategies store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return randomID(prefix) }
	}
	return &Store{db: db, pg: postgres, now: now, newI: newID, inval: inval}, nil
}

// randomID mirrors Node newId('route_strategy') / newId('rsg').
func randomID(prefix string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return prefix + "_" + hex.EncodeToString(buf)
}

func (s *Store) table(name string) string {
	if s.pg {
		return "juhe_business." + name
	}
	return name
}

func (s *Store) bind(query string) string {
	if !s.pg {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// isoMillis mirrors Node nowIso()/toISOString() millisecond precision.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func (s *Store) nowISO() string { return isoMillis(s.now()) }

// invalidateRuntime mirrors notifyGatewayRuntimeCacheInvalidation.
func (s *Store) invalidateRuntime(reason string) {
	if s.inval != nil {
		s.inval.Invalidate(TopicGatewayRuntime, reason)
	}
}
