// Package statreads ports the Node stats + usage-records read families
// (backend/src/modules/stats/stats.routes.ts and usage-records.routes.ts):
// the admin /stats + /usage-records surfaces and the forceSelfAccessScope
// /my-stats + /my-usage-records surfaces. HTTP parameter mapping, permission
// gates and response envelopes live here; the storage reads stay in this
// package's store helpers mirroring the Node repository SQL
// (usage-stats.repository.ts, usage-stats-ai-performance.repository.ts,
// account-usage.repository.ts, account-health-monitor.repository.ts,
// system-metrics.repository.ts, usage-records.repository.ts).
//
// Access scope semantics mirror storage/access-scope.ts: admins see global
// aggregate rows unless a systemAccountId filter narrows the view; the my-*
// surfaces pin every request to the caller (Node forceSelfAccessScope also
// downgrades the ALS role to "user", which keeps the requireAdmin-gated
// /system-metrics/* routes returning 403 inside /my-stats).
package statreads

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
)

// AccessScope mirrors storage/access-scope.ts for the stats read faces.
type AccessScope struct {
	ViewerID string
	IsAdmin  bool
	FilterID string
}

// scopedID mirrors scopedSystemAccountId: admins pass the filter through,
// non-admins are pinned to themselves.
func (a AccessScope) scopedID() string {
	if !a.IsAdmin {
		return a.ViewerID
	}
	return strings.TrimSpace(a.FilterID)
}

// canAccessAll mirrors canAccessAll.
func (a AccessScope) canAccessAll() bool { return a.IsAdmin }

// currentID mirrors currentSystemAccountId (never empty once authenticated).
func (a AccessScope) currentID() string {
	if id := strings.TrimSpace(a.ViewerID); id != "" {
		return id
	}
	return a.scopedID()
}

// requestScope mirrors getRequestAccessScope(query.systemAccountId): the
// filter only applies to admin roles ("all"/blank clears it).
func requestScope(r *http.Request) AccessScope {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return AccessScope{}
	}
	if auth.Role != "admin" && auth.Role != "super_admin" {
		return AccessScope{ViewerID: auth.SystemAccountID}
	}
	filter := strings.TrimSpace(r.URL.Query().Get("systemAccountId"))
	if filter == "all" {
		filter = ""
	}
	return AccessScope{ViewerID: auth.SystemAccountID, IsAdmin: true, FilterID: filter}
}

// selfScope mirrors forceSelfAccessScope: the scope pins the caller and the
// role is downgraded to "user" (Node re-enters ALS with role "user"), so the
// router-internal requireAdmin gates answer 403 on the my-* surfaces.
func selfScope(r *http.Request) AccessScope {
	auth := authsys.AuthContextFrom(r)
	if auth == nil {
		return AccessScope{}
	}
	return AccessScope{ViewerID: auth.SystemAccountID}
}

// TimezoneSource resolves the usageStatsTimezone system setting
// (Node usageStatsTimezoneAsync). The guard validates against time.LoadLocation
// like Node's Intl.DateTimeFormat probe.
type TimezoneSource func(ctx context.Context) (string, error)

// NewSystemSettingsTimezoneSource reads usageStatsTimezone from the business
// system_settings table (Node usage-stats-helpers.ts usageStatsTimezoneAsync:
// SQLite reads the unqualified table, PostgreSQL qualifies juhe_business).
func NewSystemSettingsTimezoneSource(db DB, postgres bool) TimezoneSource {
	table := "system_settings"
	if postgres {
		table = "juhe_business.system_settings"
	}
	return func(ctx context.Context) (string, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		var raw nullText
		err := db.QueryRowContext(ctx, `SELECT value_json FROM `+table+`
			WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone' LIMIT 1`).Scan(&raw)
		if isNoRows(err) || (err == nil && !raw.Valid) {
			return "", errors.New("系统设置缺少 usageStatsTimezone")
		}
		if err != nil {
			return "", err
		}
		var value string
		if err := raw.unmarshal(&value); err != nil {
			return "", errors.New("系统设置 usageStatsTimezone 无效")
		}
		name := strings.TrimSpace(value)
		if name == "" {
			return "", errors.New("统计时区必须是非空字符串")
		}
		if _, err := time.LoadLocation(name); err != nil {
			return "", errors.New("统计时区不存在：" + name)
		}
		return name, nil
	}
}
