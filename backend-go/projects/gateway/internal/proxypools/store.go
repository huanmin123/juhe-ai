// Package proxypools ports the proxy pool management family (代理池设计
// §8.1 管理面代理池 CRUD + §8.2 member-options): the admin paged list,
// pool detail with member/lease/assignable projections, the create/patch/
// delete writes with optimistic updated_at CAS and two-phase priority
// replacement, the mutation deduplication guard on create, the operation log
// entries and the committed-write runtime invalidation
// (proxy_pool_updated, §10.2).
package proxypools

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// Pool strategies mirror the proxy_pools DDL CHECK (代理池设计 §4.1).
const (
	StrategyRoundRobin = "round_robin"
	StrategyWeighted   = "weighted"
)

// memberLimit 是 §8.1 的成员数上限（≤50）。
const memberLimit = 50

// memberCountPreviewLimit / memberCountWindowLimit 沿用代理删除固定窗口口径
// 的 3/4 形态（§4.5：删除池预览最多 4 个账户名，名字展示前 3 个）。
const (
	memberCountPreviewLimit = 3
	memberCountWindowLimit  = memberCountPreviewLimit + 1
)

// priorityTempOffset / memberTempBase are the two-phase replacement offsets:
// priorities normalize into 0..memberLimit-1, existing rows shift by
// priorityTempOffset before the final CASE write, new rows land on
// memberTempBase.. so UNIQUE(pool_id, priority) never sees a transient
// duplicate inside the transaction (§8.1 两阶段更新).
const (
	priorityTempOffset = 10000
	memberTempBase     = 20000
)

// Typed failures rendered by the route layer (代理池设计 §8.6).
var (
	// ErrRevisionConflict mirrors proxy_pool_revision_conflict.
	ErrRevisionConflict = errors.New("代理池已被其他操作更新，请刷新后重试")
	// ErrNameRequired mirrors the create/patch name normalization.
	ErrNameRequired = errors.New("代理池名称不能为空")
	// ErrDescriptionInvalid mirrors the optional text normalization.
	ErrDescriptionInvalid = errors.New("代理池描述必须是字符串")
	// ErrStrategyInvalid mirrors the strategy whitelist.
	ErrStrategyInvalid = errors.New("代理池策略无效")
	// ErrEnabledInvalid mirrors the boolean normalization.
	ErrEnabledInvalid = errors.New("代理池启用状态必须是布尔值")
	// ErrMemberInvalid mirrors proxy_pool_member_invalid (不存在/非单代理/字段非法).
	ErrMemberInvalid = errors.New("代理池成员无效")
	// ErrMemberDuplicate mirrors proxy_pool_member_duplicate.
	ErrMemberDuplicate = errors.New("代理池成员重复")
	// ErrMemberLimit mirrors proxy_pool_member_limit.
	ErrMemberLimit = errors.New("代理池成员数量超出上限")
)

// ErrorCode carrier for §8.6 code-bearing failures (route envelope).
type errorCoder interface {
	ErrorCode() string
}

// PoolNameDuplicateError mirrors the proxyprofiles DuplicateNameError: 409
// with the attempted pool name (idx_proxy_pools_name_unique).
type PoolNameDuplicateError struct{ Name string }

func (e *PoolNameDuplicateError) Error() string { return "代理池名称已存在：" + e.Name }

// PoolBoundAccountsError mirrors proxy_pool_bound_accounts: the pool is still
// bound by non-deleted accounts (fixed preview window, §4.5).
type PoolBoundAccountsError struct {
	AccountCount             int
	AccountNames             []string
	AccountCountIsLowerBound bool
}

func (e *PoolBoundAccountsError) ErrorCode() string { return "proxy_pool_bound_accounts" }

func (e *PoolBoundAccountsError) Error() string {
	names := ""
	if len(e.AccountNames) > 0 {
		names = "：" + strings.Join(e.AccountNames, "、")
		if e.AccountCountIsLowerBound || e.AccountCount > len(e.AccountNames) {
			names += " 等"
		}
	}
	countText := fmt.Sprintf("%d", e.AccountCount)
	if e.AccountCountIsLowerBound {
		countText = fmt.Sprintf("至少 %d", e.AccountCount)
	}
	return fmt.Sprintf("代理池仍被 %s 个账户绑定，请先解除绑定%s", countText, names)
}

// MemberInvalidError carries the offending member id (presence only; the
// message never includes host/port/credentials).
type MemberInvalidError struct{ ProfileID string }

func (e *MemberInvalidError) ErrorCode() string { return "proxy_pool_member_invalid" }

func (e *MemberInvalidError) Error() string { return ErrMemberInvalid.Error() }

// MemberDuplicateError / MemberLimitError carry the §8.6 codes.
type MemberDuplicateError struct{}

func (e *MemberDuplicateError) ErrorCode() string { return "proxy_pool_member_duplicate" }

func (e *MemberDuplicateError) Error() string { return ErrMemberDuplicate.Error() }

type MemberLimitError struct{}

func (e *MemberLimitError) ErrorCode() string { return "proxy_pool_member_limit" }

func (e *MemberLimitError) Error() string { return ErrMemberLimit.Error() }

// RuntimeInvalidator is the K5 gateway runtime cache invalidation port
// (代理池设计 §10.2：池/成员写入提交后发布 proxy_pool_updated)。
// *inval.Bus satisfies it structurally; nil tolerated.
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// MemberHealthResetter is the best-effort Redis member-health key reset port
// (代理池设计 §7.4：成员/池删除时清理
// juhe-ai:{<namespace>}:proxy-pool:member-health:<id>，失败告警靠 TTL)。
// nil tolerated.
type MemberHealthResetter interface {
	ResetMemberHealth(profileID string)
}

// Deps bundles the pool family collaborators.
type Deps struct {
	DB     *sql.DB
	Now    func() time.Time
	NewID  func(prefix string) string
	Inval  RuntimeInvalidator
	Health MemberHealthResetter
}

// Store is the pool persistence half.
type Store struct {
	db     *sql.DB
	now    func() time.Time
	newID  func(string) string
	inval  RuntimeInvalidator
	health MemberHealthResetter
}

// NewStore builds the pool store.
func NewStore(deps Deps) (*Store, error) {
	if deps.DB == nil {
		return nil, errors.New("proxy pool store requires a database")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.NewID == nil {
		return nil, errors.New("proxy pool store requires an id generator")
	}
	return &Store{db: deps.DB, now: deps.Now, newID: deps.NewID, inval: deps.Inval, health: deps.Health}, nil
}

// notifyRuntimeUpdated sits strictly on commit-success paths (§10.2).
func (s *Store) notifyRuntimeUpdated() {
	if s.inval == nil {
		return
	}
	s.inval.Invalidate(inval.TopicGatewayRuntime, "proxy_pool_updated")
}

// resetMemberHealth deletes the shared Redis member-health keys best-effort;
// failures are logged by the adapter and the keys age out via TTL.
func (s *Store) resetMemberHealth(profileIDs []string) {
	if s.health == nil {
		return
	}
	seen := map[string]bool{}
	for _, id := range profileIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		s.health.ResetMemberHealth(id)
	}
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

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// isPoolNameDuplicate mirrors isProxyNameDuplicate: PG surfaces the unique
// index name on idx_proxy_pools_name_unique.
func isPoolNameDuplicate(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "idx_proxy_pools_name_unique")
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullIfEmpty(value *string) any {
	if value == nil {
		return nil
	}
	if strings.TrimSpace(*value) == "" {
		return nil
	}
	return *value
}

func derefOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func placeholders(count int) string {
	if count < 1 {
		count = 1
	}
	pieces := make([]string, count)
	for index := range pieces {
		pieces[index] = "?"
	}
	return strings.Join(pieces, ",")
}

func idsToAny(ids []string) []any {
	values := make([]any, 0, len(ids))
	for _, id := range ids {
		values = append(values, id)
	}
	return values
}

func boolFromValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case int64:
		return typed == 1
	case float64:
		return typed != 0
	case []byte:
		return string(typed) == "1"
	default:
		return false
	}
}

func intFromValue(value any) int {
	switch typed := value.(type) {
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case []byte:
		var parsed int
		_, _ = fmt.Sscanf(string(typed), "%d", &parsed)
		return parsed
	default:
		return 0
	}
}

func toTextValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case time.Time:
		return typed.UTC().Format(revisionLayout)
	default:
		return ""
	}
}

const revisionLayout = "2006-01-02T15:04:05.000000Z"

// revisionSelectExpression textifies updated_at on PG so the revision scans
// back as text (same to_char US pattern as proxyprofiles).
func (s *Store) revisionSelectExpression() string {
	return `to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS updated_at`
}

// revisionCastPlaceholder wraps the CAS parameter on PG.
func (s *Store) revisionCastPlaceholder() string {
	return "CAST(? AS timestamptz)"
}

func normalizeRevision(value string) string {
	return strings.TrimSpace(value)
}

// keywordFilter renders the name prefix window (§8.1 关键词只按池名精确/
// 前缀)：PG pins byte collation and adds starts_with, mirroring the
// proxyprofiles predicate byte for byte.
func (s *Store) keywordFilter() string {
	return `(name COLLATE "C" >= ? AND name COLLATE "C" < ? AND starts_with(name, ?))`
}

func (s *Store) keywordFilterParams(keyword string) []any {
	upper := textPrefixUpperBound(keyword)
	return []any{keyword, upper, keyword}
}

func textPrefixUpperBound(value string) string {
	chars := []rune(value)
	for index := len(chars) - 1; index >= 0; index-- {
		codePoint := chars[index]
		if codePoint >= 0x10ffff {
			continue
		}
		return string(chars[:index]) + string(codePoint+1)
	}
	return value + "\U0010ffff"
}
