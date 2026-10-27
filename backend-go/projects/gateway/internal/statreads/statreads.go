package statreads

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// GLOBALStats* mirror usage-stats-types.ts GLOBAL_STATS_SYSTEM_ACCOUNT_ID /
// GLOBAL_STATS_SCOPE_ID.
const (
	globalStatsSystemAccountID = "global"
	globalStatsScopeID         = "global"
)

// Deps bundles the statreads collaborators. Business reaches the business
// database (accounts / system_accounts / groups ...), Stats reaches the stats
// database (usage_stats_* / *_windows / account_health_hourly ...) — both
// alias the shared pool handle with juhe_business.*/juhe_stats.* schema
// qualification.
type Deps struct {
	Business DB
	Stats    DB
	Auth     *authsys.Deps
	Now      func() time.Time
	Timezone TimezoneSource
	// HealthOutcomes optionally points at the J1 account-health outcome
	// store (jobs Postgres juhe_jobs.account_health_outcomes); nil or an
	// empty PostgresURL keeps the merge absent exactly like an unconfigured
	// Node source.
	HealthOutcomes *HealthOutcomeSource
	// RuntimeMode mirrors runtimeConfig.runtimeMode ('standalone' |
	// 'performance'); empty keeps the standalone contract.
	RuntimeMode string
}

// Mount wires the statreads route families:
//
//	GET /__aisys__/api/stats/*            (requireAdmin)
//	GET /__aisys__/api/my-stats/*         (forceSelfAccessScope; the
//	  router-internal requireAdmin routes stay 403 here, mirroring the Node
//	  role downgrade)
//	GET /__aisys__/api/usage-records      (requireAdmin)
//	GET /__aisys__/api/my-usage-records   (forceSelfAccessScope)
func (d *Deps) Mount(k *kernel.Kernel) {
	prefix := "/__aisys__/api"
	admin := d.Auth.RequireAdmin
	self := d.selfScopeSession()

	// stats family surfaces: {prefix} resolves to /stats (admin scope) and
	// /my-stats (self scope).
	for _, surface := range []struct {
		base string
		wrap func(http.Handler) http.Handler
		self bool
	}{
		{prefix + "/stats", admin, false},
		{prefix + "/my-stats", self, true},
	} {
		base := surface.base
		selfOnly := surface.self
		overview := surface.wrap(http.HandlerFunc(d.overviewSectionHandler(selfOnly)))
		k.Register("GET "+base+"/usage-overview/summary", overview)
		k.Register("GET "+base+"/usage-overview/daily-trend", overview)
		k.Register("GET "+base+"/usage-overview/hourly-trend", overview)
		k.Register("GET "+base+"/usage-overview/model-distribution", overview)
		k.Register("GET "+base+"/usage-overview/errors", overview)
		k.Register("GET "+base+"/usage-window", surface.wrap(http.HandlerFunc(d.usageWindowHandler)))
		k.Register("GET "+base+"/ai-performance", surface.wrap(http.HandlerFunc(d.aiPerformanceBaseHandler(selfOnly))))
		k.Register("GET "+base+"/ai-performance/series", surface.wrap(http.HandlerFunc(d.aiPerformanceSeriesHandler(selfOnly))))
		k.Register("GET "+base+"/ai-performance/accounts", surface.wrap(http.HandlerFunc(d.aiPerformanceAccountsHandler(selfOnly))))
		k.Register("GET "+base+"/ai-health", surface.wrap(http.HandlerFunc(d.aiHealthListHandler(selfOnly))))
		k.Register("GET "+base+"/ai-health/hour-detail", surface.wrap(http.HandlerFunc(d.aiHealthHourDetailHandler(selfOnly))))
		k.Register("GET "+base+"/account-usage", surface.wrap(http.HandlerFunc(d.accountUsageHandler(selfOnly))))
		k.Register("GET "+base+"/account-usage/options", surface.wrap(http.HandlerFunc(d.accountUsageOptionsHandler(selfOnly))))
		k.Register("GET "+base+"/account-usage/summary", surface.wrap(http.HandlerFunc(d.accountUsageSummaryHandler(selfOnly))))
		k.Register("GET "+base+"/account-usage/trend", surface.wrap(http.HandlerFunc(d.accountUsageTrendHandler(selfOnly))))
	}

	k.Register("GET "+prefix+"/usage-records", admin(http.HandlerFunc(d.usageRecordsListHandler(false))))
	k.Register("GET "+prefix+"/my-usage-records", self(http.HandlerFunc(d.usageRecordsListHandler(true))))
}

// selfScopeSession mirrors requireSession + forceSelfAccessScope: after the
// session resolves, the auth context re-enters with the role downgraded to
// "user", so router-internal requireAdmin routes answer 403 on the my-*
// surfaces exactly like Node.
func (d *Deps) selfScopeSession() func(http.Handler) http.Handler {
	// Package tests inject the auth context directly without an authsys Deps.
	if d.Auth == nil {
		return downgradeRole
	}
	session := d.Auth.RequireSession(true)
	return func(next http.Handler) http.Handler {
		return session(downgradeRole(next))
	}
}

// downgradeRole re-enters the auth context with the role pinned to "user"
// (forceSelfAccessScope's ALS write).
func downgradeRole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := authsys.AuthContextFrom(r); auth != nil {
			downgraded := *auth
			downgraded.Role = "user"
			next.ServeHTTP(w, r.WithContext(authsys.WithAuthContext(r.Context(), &downgraded)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// timezoneLocation resolves the configured usageStatsTimezone into a
// *time.Location (Node usageStatsTimezoneAsync + time.LoadLocation).
func (d *Deps) timezoneLocation(ctx context.Context) (*time.Location, error) {
	name, err := d.Timezone(ctx)
	if err != nil {
		return nil, err
	}
	return time.LoadLocation(name)
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}
