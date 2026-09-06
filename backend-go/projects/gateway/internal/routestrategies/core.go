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
// the gateway runtime invalidation hook. This slice also owns the
// /options, /:id/edit-basic and /:id/speed-first-runtime reads, the
// authorized-grantee binding branch (resource_authorizations +
// group_authorization_settings joins), the PATCH API-Key validation-cache
// invalidation and the best-effort normal-route speed-first runtime cleanup
// hooks (BUG-0164). The facade port (SpeedFirstRuntimeFacade) and the
// validation-cache invalidator stay nil-safe until composition wires them.
package routestrategies

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
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

// ValidationCacheInvalidator mirrors notifyGatewayApiKeyValidationCacheInvalidation:
// Node fires it after a runtime-relevant PATCH and surfaces a failure as
// GatewayApiKeyValidationCacheInvalidationError (route renders 500). Nil keeps
// the invalidation best-effort skipped.
type ValidationCacheInvalidator interface {
	InvalidateValidationCache(reason string) error
}

// ValidationCacheInvalidationError maps to
// GatewayApiKeyValidationCacheInvalidationError (PATCH → 500
// 策略路由已更新，但 API Key validation cache 失效失败).
type ValidationCacheInvalidationError struct{ Message string }

func (e *ValidationCacheInvalidationError) Error() string { return e.Message }

// SpeedFirstRuntimeSummary mirrors RouteStrategySpeedFirstLatencyRuntimeSummary.
type SpeedFirstRuntimeSummary struct {
	RuntimeAvailable bool `json:"runtimeAvailable"`
	DegradedCount    int  `json:"degradedCount"`
}

// SpeedFirstRuntimeItem mirrors NormalRouteLatencyDegradedRuntimeItem — the
// admin-facing degraded runtime row rendered by the speed-first-runtime
// endpoint (JSON tags are the wire contract).
type SpeedFirstRuntimeItem struct {
	AccountID                      string       `json:"accountId"`
	AccountName                    *string      `json:"accountName,omitempty"`
	Scope                          runtimeScope `json:"scope"`
	SlowCount                      int64        `json:"slowCount"`
	SlowTriggerCount               int64        `json:"slowTriggerCount"`
	SlowWindowSeconds              int64        `json:"slowWindowSeconds"`
	DegradedUntil                  string       `json:"degradedUntil"`
	NextProbeAt                    *string      `json:"nextProbeAt,omitempty"`
	RecoverySuccessCount           int64        `json:"recoverySuccessCount"`
	RequiredRecoverySuccessCount   int64        `json:"requiredRecoverySuccessCount"`
	RecoveryProbeRoundAttemptCount int64        `json:"recoveryProbeRoundAttemptCount"`
	RecoveryProbeRoundSuccessCount int64        `json:"recoveryProbeRoundSuccessCount"`
	Reason                         string       `json:"reason"`
}

type runtimeScope struct {
	RouteStrategyID string `json:"routeStrategyId"`
	GroupID         string `json:"groupId"`
}

// SpeedFirstRuntimeFacade mirrors
// modules/route-strategies/route-strategy-speed-first-runtime.facade.ts: the
// raw degraded-runtime reader plus the per-strategy cleanup. The facade-level
// account dedupe / degraded counting lives in this package; the adapter
// (composition side, typically gatewayproxyhealth.LatencyDegradationService)
// only performs the store read. Nil keeps the reads offline: the runtime
// endpoint is not mounted and the cleanup hooks no-op.
type SpeedFirstRuntimeFacade interface {
	// ListDegradedRuntime mirrors listNormalRouteLatencyDegradedRuntimeAsync.
	// available=false means the runtime store is unavailable (Node renders
	// runtimeAvailable:false instead of failing the request).
	ListDegradedRuntime(ctx context.Context, systemAccountID *string, routeStrategyIDs []string) (items []SpeedFirstRuntimeItem, available bool, err error)
	// ClearDegradedRuntime mirrors
	// clearNormalRouteLatencyDegradationForRouteStrategyAsync (cleared count).
	ClearDegradedRuntime(ctx context.Context, routeStrategyID string) (int, error)
}

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

	validationInval   ValidationCacheInvalidator
	speedFirst        SpeedFirstRuntimeFacade
}

// SetValidationCacheInvalidator wires the API-Key validation-cache
// invalidation port (nil = skipped).
func (s *Store) SetValidationCacheInvalidator(inval ValidationCacheInvalidator) {
	s.validationInval = inval
}

// SetSpeedFirstRuntimeFacade wires the speed-first runtime facade used by the
// runtime read endpoint, the list enrichment and the mutation cleanup hooks
// (Node serves reads through the facade and cleans through
// clearNormalRouteLatencyDegradationForRouteStrategyAsync). Nil keeps the
// endpoint unmounted and the cleanup no-op.
func (s *Store) SetSpeedFirstRuntimeFacade(facade SpeedFirstRuntimeFacade) {
	s.speedFirst = facade
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

// invalidateValidationCache mirrors notifyGatewayApiKeyValidationCacheInvalidation;
// a failure surfaces as ValidationCacheInvalidationError so the PATCH route can
// render Node's 500 策略路由已更新，但 API Key validation cache 失效失败.
func (s *Store) invalidateValidationCache(reason string) error {
	if s.validationInval == nil {
		return nil
	}
	if err := s.validationInval.InvalidateValidationCache(reason); err != nil {
		return &ValidationCacheInvalidationError{Message: "策略路由已更新，但 API Key validation cache 失效失败"}
	}
	return nil
}

// normalizeStoredMode mirrors normalizeRouteStrategyMode over stored rows:
// empty/NULL falls back to normal, unknown values fail the read (Node throws
// into the route error handler).
func normalizeStoredMode(value string) (string, error) {
	if value == "" {
		return ModeNormal, nil
	}
	if IsRouteStrategyMode(value) {
		return value, nil
	}
	return "", &ValidationError{Message: "路由策略模式无效"}
}

// normalizeStoredStatus mirrors normalizeRouteStrategyStatus(row.status,
// fallback) over stored rows.
func normalizeStoredStatus(value, fallback string) (string, error) {
	if value == "" {
		return fallback, nil
	}
	if value == "active" || value == "disabled" {
		return value, nil
	}
	return "", &ValidationError{Message: "策略路由状态无效"}
}

// utf16CodeUnits counts JavaScript String.length units: astral runes (outside
// the BMP) take two code units, matching the zod max(200) description bound.
func utf16CodeUnits(value string) int {
	units := 0
	for _, r := range value {
		if r > 0xFFFF {
			units += 2
			continue
		}
		units++
	}
	return units
}

// canonicalRFC3339Instant mirrors canonicalizeRfc3339Instant: RFC3339 with a
// mandatory Z or numeric offset, canonicalized to UTC millisecond precision
// (ok=false renders the caller's 400).
func canonicalRFC3339Instant(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return isoMillis(parsed.Truncate(time.Millisecond)), true
}

// intQueryValue mirrors integerQueryValue: JavaScript Number(trimmed) semantics
// over integer results — "1e2" → 100, "1.0" → 1, "abc"/"12.5"/"" → not a
// queryable integer.
func intQueryValue(raw string) (int, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, false
	}
	number, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	if number != math.Trunc(number) || number < math.MinInt32 || number > math.MaxInt32 {
		return 0, false
	}
	return int(number), true
}
