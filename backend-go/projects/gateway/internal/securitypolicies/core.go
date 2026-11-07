// Package securitypolicies owns the security-policy entity slice (API Key 安
// 全策略与出站内容脱敏设计 §3 规则模型 / §4 实体绑定与菜单，批A 实体 CRUD)：
// the dual-surface CRUD route family — /__aisys__/api/security-policies
// (admin) and /__aisys__/api/my-security-policies (self) — over the
// security_policies table delivered by maintenance (schema + builtin row
// seed). The slice mirrors the internal/schedulingpreferences template:
// owner-scoped list/options/detail with the builtin-row global read exception,
// guarded create with strict body whitelist, optimistic-lock PATCH
// (expectedUpdatedAt CAS, equal-value no-op) and always-success delete that
// first resets bound API keys to the builtin default in the same transaction,
// with operation logging and the global gateway-runtime invalidation hook.
// 规则集（rules_json）的校验与规范化在本包 rules.go 单点完成；请求/响应链路
// 的脱敏执行、占位符还原与系统提示注入属后续批次（internal/gatewaysecpol），
// 不在本包。
package securitypolicies

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

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// BuiltinSecurityPolicyID 是内置安全策略行「默认安全策略」的固定 id（API Key
// 安全策略与出站内容脱敏设计 §4.1 契约固定值）。与 maintenance
// internal/schema 的 schema.BuiltinSecurityPolicyID、internal/apikeys 绑定侧
// 常量及前端 api 常量同源锚点，改动即多处同改。
const BuiltinSecurityPolicyID = "secpol-builtin-default"

// BuiltinSecurityPolicyName 是内置行权威名称「默认安全策略」（与 maintenance
// schema.BuiltinSecurityPolicyName 同源锚点）：内置行业务写入口一律 403、种
// 子 upsert 恢复权威值，读取投影直接用存储行回显。
const BuiltinSecurityPolicyName = "默认安全策略"

// BuiltinWriteError marks a write against the builtin row — the route renders
// 403 系统内置安全策略不可修改或删除（内置行任何业务写入口一律 403）.
type BuiltinWriteError struct{ Message string }

func (e *BuiltinWriteError) Error() string { return e.Message }

// AccessScope mirrors the schedulingpreferences owner-view subset: admins see
// every owner unless a filter narrows the view; the self surface is pinned to
// the authenticated caller. The builtin row (system_account_id NULL) is a
// global read-only exception visible through every scope.
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
// newly created rows. The admin route enforces the required target owner
// (400 系统账号 ID 不能为空) before calling Create, so this is the fallback.
func (a AccessScope) writeSystemAccountID() (string, error) {
	if id := a.manageableID(); id != "" {
		return id, nil
	}
	if a.ViewerID != "" {
		return a.ViewerID, nil
	}
	return "", &routestrategies.ValidationError{Message: "缺少系统账户上下文"}
}

// canManageOwner mirrors canManageApiKeyOwner: scoped ids must match;
// unscoped admin manages every custom row.
func (a AccessScope) canManageOwner(ownerID string) bool {
	if scoped := a.manageableID(); scoped != "" {
		return scoped == ownerID
	}
	return a.canAccessAll()
}

// Store is the PostgreSQL security-policy persistence.
type Store struct {
	db    *sql.DB
	now   func() time.Time
	newI  func(prefix string) string
	inval routestrategies.RuntimeInvalidator
}

// NewStore builds the store; inval may be nil (no-op invalidation). inval is
// the same port the scheduling-preference slice uses
// (routestrategies.RuntimeInvalidator, satisfied by *inval.Bus) so the
// composition wires one bus for both slices.
func NewStore(db *sql.DB, now func() time.Time, newID func(string) string, inval routestrategies.RuntimeInvalidator) (*Store, error) {
	if db == nil {
		return nil, errors.New("securitypolicies store requires a database")
	}
	if now == nil {
		now = time.Now
	}
	if newID == nil {
		newID = func(prefix string) string { return randomID(prefix) }
	}
	return &Store{db: db, now: now, newI: newID, inval: inval}, nil
}

func randomID(prefix string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return prefix + "_" + hex.EncodeToString(buf)
}

func (s *Store) table(name string) string {
	return "juhe_business." + name
}

func (s *Store) bind(query string) string {
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

// isoMillis mirrors Node nowIso()/toISOString() millisecond precision — the
// stored updated_at shape the CAS canonicalizes against.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func (s *Store) nowISO() string { return isoMillis(s.now()) }

// canonicalRFC3339Instant mirrors the schedulingpreferences helper: RFC3339
// with a mandatory Z or numeric offset, canonicalized to UTC millisecond
// precision (ok=false renders the caller's 400 安全策略配置版本格式不正确).
func canonicalRFC3339Instant(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return isoMillis(parsed.Truncate(time.Millisecond)), true
}

// intQueryValue mirrors the schedulingpreferences integer query parser:
// JavaScript Number(trimmed) semantics over integer results.
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

// invalidateRuntime mirrors the scheduling-preference invalidation hook: the
// global gateway-runtime topic with a slice-specific reason. 绑定 Key 的出站
// 脱敏行为依赖策略内容（rules 变更、删除解绑回默认），提交成功后必须全清运
// 行态缓存，让全部绑定 Key 下次加载重建快照（设计 §4.3：全局 topic，不做逐
// Key N+1 失效）。
func (s *Store) invalidateRuntime(reason string) {
	if s.inval != nil {
		s.inval.Invalidate(routestrategies.TopicGatewayRuntime, reason)
	}
}

func ptrString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func nullPtrString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return &value.String
}

func nullString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func sameNullString(left, right sql.NullString) bool {
	return left.Valid == right.Valid && left.String == right.String
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
