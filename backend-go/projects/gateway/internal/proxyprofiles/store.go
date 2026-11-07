// Package proxyprofiles ports the proxy profile management family (Node
// backend/src/modules/proxies/proxies.routes.ts +
// storage/proxy.repository.ts): the admin list/options reads plus the
// create/patch/delete writes with optimistic updated_at CAS, connection
// change test-state resets, F4 operation log entries and the mutation
// deduplication guard on create.
package proxyprofiles

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

// Test statuses mirror proxyTestStatusValues.
const (
	testStatusUnknown = "unknown"
	// 池健康投影重置值（代理池设计 §6.2）：连接变更后旧检测事实作废。
	poolHealthResetStatus = "unknown"
	poolHealthResetStreak = 0
)

const (
	proxyUsagePreviewLimit = 3
	proxyUsageWindowLimit  = proxyUsagePreviewLimit + 1
)

// Typed failures rendered by the route layer.
var (
	// ErrConflict mirrors ProxyProfileUpdateConflictError.
	ErrConflict = errors.New("代理已被其他操作更新，请刷新后重试")
	// ErrNameRequired mirrors normalizedRequiredProxyName.
	ErrNameRequired = errors.New("代理名称不能为空")
	// ErrHostRequired mirrors normalizedRequiredProxyHost.
	ErrHostRequired = errors.New("代理主机不能为空")
	// ErrTypeInvalid mirrors normalizedProxyType.
	ErrTypeInvalid = errors.New("代理类型无效")
	// ErrPortInvalid mirrors normalizedProxyPort.
	ErrPortInvalid = errors.New("代理端口必须是 1-65535 的整数")
	// ErrDescriptionInvalid mirrors normalizeOptionalText failures.
	ErrDescriptionInvalid = errors.New("代理描述必须是字符串")
	// ErrUsernameInvalid mirrors normalizeOptionalText failures.
	ErrUsernameInvalid = errors.New("代理用户名必须是字符串")
	// ErrEnabledInvalid mirrors normalizeOptionalBoolean failures.
	ErrEnabledInvalid = errors.New("代理启用状态必须是布尔值")
	// ErrPasswordRequired / ErrPasswordString mirror normalizeProxyPassword.
	ErrPasswordRequired = errors.New("代理密码不能为空")
	ErrPasswordString   = errors.New("代理密码必须是字符串")
)

// DuplicateNameError mirrors the isDuplicateProxyNameError branch: 409 with
// the attempted/proxy name.
type DuplicateNameError struct{ Name string }

func (e *DuplicateNameError) Error() string { return "代理名称已存在：" + e.Name }

// InUseError mirrors ProxyInUseError.
type InUseError struct {
	AccountCount             int
	AccountNames             []string
	AccountCountIsLowerBound bool
}

func (e *InUseError) Error() string {
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
	return fmt.Sprintf("这个代理仍被 %s 个账户使用，请先在账户管理中解绑或改绑后再删除%s", countText, names)
}

// PoolMemberReferenceError mirrors the 代理池设计 §8.6
// proxy_pool_member_reference_conflict failure: the profile is still
// referenced by a proxy_pool_members row and cannot be deleted until the
// pool editor removes the membership.
type PoolMemberReferenceError struct{}

// ErrorCode exposes the §8.6 code for the route envelope.
func (e *PoolMemberReferenceError) ErrorCode() string { return "proxy_pool_member_reference_conflict" }

func (e *PoolMemberReferenceError) Error() string {
	return "代理被代理池引用，请先解除成员关系"
}

// RuntimeInvalidator is the K5 gateway runtime cache invalidation port
// (代理池设计 §10.2：代理档案写入同批补上 proxy_profile_updated 失效)。
// *inval.Bus satisfies it structurally; nil keeps the write family
// self-contained with no-op invalidation (the pre-handover Deps state).
type RuntimeInvalidator interface {
	Invalidate(topic, reason string)
}

// MemberHealthResetter is the best-effort Redis member-health projection
// reset port (代理池设计 §6.2/§7.4)：连接变更或档案删除后删除
// juhe-ai:{<namespace>}:proxy-pool:member-health:<id>，让 gateway 回退 PG
// pool_health_*，直到下一轮池健康探测重新写入。删除失败由实现方告警并靠
// TTL 过期兜底；nil keeps the reset off.
type MemberHealthResetter interface {
	ResetMemberHealth(profileID string)
}

// Deps bundles the proxy family collaborators.
type Deps struct {
	DB     *sql.DB
	Secret string
	Now    func() time.Time
	NewID  func(prefix string) string
	// Inval publishes the committed-write runtime invalidation; nil tolerated.
	Inval RuntimeInvalidator
	// MemberHealth resets the shared Redis member-health key; nil tolerated.
	MemberHealth MemberHealthResetter
}

// Store is the proxy persistence half.
type Store struct {
	db           *sql.DB
	secret       string
	now          func() time.Time
	newID        func(string) string
	inval        RuntimeInvalidator
	memberHealth MemberHealthResetter
}

// NewStore builds the proxy store.
func NewStore(deps Deps) (*Store, error) {
	if deps.DB == nil {
		return nil, errors.New("proxy store requires a database")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.NewID == nil {
		return nil, errors.New("proxy store requires an id generator")
	}
	return &Store{db: deps.DB, secret: deps.Secret, now: deps.Now, newID: deps.NewID,
		inval: deps.Inval, memberHealth: deps.MemberHealth}, nil
}

// notifyRuntimeUpdated mirrors notifyCommittedModelCacheInvalidation: call
// sites sit strictly on commit-success paths, before the response renders
// (§10.2 proxy_profile_updated reason).
func (s *Store) notifyRuntimeUpdated() {
	if s.inval == nil {
		return
	}
	s.inval.Invalidate(inval.TopicGatewayRuntime, "proxy_profile_updated")
}

// resetMemberHealth deletes the shared Redis member-health key best-effort
// (§6.2)：失败只告警（实现方负责），键最多保留到 TTL 过期，gateway 期间回退
// PG pool_health_* 事实源。
func (s *Store) resetMemberHealth(profileID string) {
	if s.memberHealth == nil {
		return
	}
	s.memberHealth.ResetMemberHealth(profileID)
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

// ProfileSummary mirrors ProxyProfileSummary.
type ProfileSummary struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Description     *string `json:"description,omitempty"`
	Type            string  `json:"type"`
	Host            string  `json:"host"`
	Port            int     `json:"port"`
	Username        *string `json:"username,omitempty"`
	Enabled         bool    `json:"enabled"`
	TestStatus      string  `json:"testStatus"`
	LatencyMs       *int64  `json:"latencyMs,omitempty"`
	OutboundIp      *string `json:"outboundIp,omitempty"`
	OutboundRegion  *string `json:"outboundRegion,omitempty"`
	LastTestMessage *string `json:"lastTestMessage,omitempty"`
	LastTestedAt    *string `json:"lastTestedAt,omitempty"`
	UpdatedAt       string  `json:"updatedAt"`
}

// ListResult mirrors ProxyProfileListResult.
type ListResult struct {
	Items    []ProfileSummary `json:"items"`
	Total    int              `json:"total"`
	HasMore  bool             `json:"hasMore"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

// OptionSummary mirrors ProxyProfileOptionSummary plus the 代理池设计 §8.2
// kind discriminator. Profile items keep the legacy id/name/type/enabled
// shape (kind added additively); pool items omit type and carry the pool
// counts — never members, host, port or credentials.
type OptionSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Type    string `json:"type,omitempty"`
	Enabled bool   `json:"enabled"`
	// Pool-only fields (nil/omitted on profile items).
	MemberCount           *int `json:"memberCount,omitempty"`
	AssignableMemberCount *int `json:"assignableMemberCount,omitempty"`
}

// Option kinds for the shared account-form dropdown (§8.2).
const (
	OptionKindProfile = "profile"
	OptionKindPool    = "pool"
)

// isProxyNameDuplicate mirrors isDuplicateProxyNameError: Node matches the
// unique index names surfaced by better-sqlite3/pg; the Go drivers report
// either the index name (pg) or the column form (modernc), so both shapes map
// onto the same 409.
func isProxyNameDuplicate(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	if strings.Contains(message, "idx_proxy_profiles_name_unique") ||
		strings.Contains(message, "idx_proxy_profiles_name_unique_lower") {
		return true
	}
	return strings.Contains(message, "UNIQUE constraint failed") &&
		strings.Contains(message, "proxy_profiles.name")
}

// normalizeTestStatus mirrors normalizeProxyTestStatus.
func normalizeTestStatus(value string) string {
	switch value {
	case "unknown", "passed", "warning", "failed":
		return value
	default:
		// Node throws; the store keeps unknown for forward compatibility of
		// freshly migrated rows (the read path throws in Node, so this branch
		// is only reachable for manually corrupted rows).
		return testStatusUnknown
	}
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

var _ = context.Background
