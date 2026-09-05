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
)

// Test statuses mirror proxyTestStatusValues.
const (
	testStatusUnknown = "unknown"
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

// Deps bundles the proxy family collaborators.
type Deps struct {
	DB        *sql.DB
	PGDialect bool
	Secret    string
	Now       func() time.Time
	NewID     func(prefix string) string
}

// Store is the proxy persistence half.
type Store struct {
	db     *sql.DB
	pg     bool
	secret string
	now    func() time.Time
	newID  func(string) string
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
	return &Store{db: deps.DB, pg: deps.PGDialect, secret: deps.Secret, now: deps.Now, newID: deps.NewID}, nil
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

// OptionSummary mirrors ProxyProfileOptionSummary.
type OptionSummary struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

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
