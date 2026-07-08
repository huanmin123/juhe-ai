package httpapi

import (
	"log/slog"
	"net/http"
	"net/http/pprof"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/modules/publicsettings"
	"juhe-ai/backend-go/internal/store/port"
)

type RouterOptions struct {
	Config                                          config.Config
	Logger                                          *slog.Logger
	PublicSettingsService                           *publicsettings.Service
	SystemAPIIPRateLimitReader                      port.SystemAPIIPRateLimitReader
	SystemAPIIPReadRateLimiter                      SystemAPIIPReadRateLimiter
	PublicAPIHandler                                http.Handler
	ManagementAPIAuthMiddleware                     func(http.Handler) http.Handler
	ManagementCurrentUserHandler                    http.Handler
	ManagementProfileUpdateHandler                  http.Handler
	ManagementLogoutHandler                         http.Handler
	ManagementProxyOptionsHandler                   http.Handler
	ManagementSystemAccountsHandler                 http.Handler
	ManagementSystemAccountOptionsHandler           http.Handler
	ManagementAuthorizationGranteeAccountsHandler   http.Handler
	ManagementMyAuthorizationGranteeAccountsHandler http.Handler
	ManagementAuthorizationGranteeTeamsHandler      http.Handler
	ManagementMyAuthorizationGranteeTeamsHandler    http.Handler
	ManagementAuthorizationGranteeGroupsHandler     http.Handler
	ManagementMyAuthorizationGranteeGroupsHandler   http.Handler
	ManagementProviderOptionsHandler                http.Handler
	ManagementProviderModelOptionsHandler           http.Handler
	ManagementProviderModelsHandler                 http.Handler
	ManagementProviderDefaultTestModelHandler       http.Handler
	ManagementRouteStrategyOptionsHandler           http.Handler
	ManagementMyRouteStrategyOptionsHandler         http.Handler
	ManagementGroupOptionsHandler                   http.Handler
	ManagementMyGroupOptionsHandler                 http.Handler
	ManagementGroupAccountOptionsHandler            http.Handler
	ManagementMyGroupAccountOptionsHandler          http.Handler
	ManagementAccountOptionsHandler                 http.Handler
	ManagementMyAccountOptionsHandler               http.Handler
	ManagementAccountTagsHandler                    http.Handler
	ManagementMyAccountTagsHandler                  http.Handler
	ManagementAccountTagDeleteHandler               http.Handler
	ManagementMyAccountTagDeleteHandler             http.Handler
	ManagementAccountTagUpdateHandler               http.Handler
	ManagementMyAccountTagUpdateHandler             http.Handler
	ManagementOperationLogsHandler                  http.Handler
	ManagementMyOperationLogsHandler                http.Handler
}

func NewRouter(opts RouterOptions) http.Handler {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware)
	r.Use(recoverMiddleware(opts.Logger))
	clientIPs := newClientIPResolver(opts.Config)

	health := NewHealthHandler(opts.Config, opts.Logger)
	r.Get("/__aisys__/health", health.ServeHTTP)
	r.Route("/__aisys__/api", func(system chi.Router) {
		system.Use(noStoreMiddleware)
		if opts.SystemAPIIPRateLimitReader != nil {
			system.Use(newSystemAPIIPReadRateLimitMiddleware(
				opts.SystemAPIIPRateLimitReader,
				opts.SystemAPIIPReadRateLimiter,
				clientIPs,
				opts.Logger,
			))
		}
		system.Get("/health", health.ServeHTTP)
		if opts.PublicSettingsService != nil {
			publicSettingsHandler := NewPublicSettingsHandler(*opts.PublicSettingsService, opts.Logger)
			system.Get("/settings/public", publicSettingsHandler.ServeHTTP)
		}
		if opts.Config.ManagementAPIEnabled {
			if opts.ManagementAPIAuthMiddleware == nil {
				panic("ManagementAPIAuthMiddleware is required when JUHE_AI_MANAGEMENT_API_ENABLED is true")
			}
			if opts.ManagementCurrentUserHandler == nil &&
				opts.ManagementProfileUpdateHandler == nil &&
				opts.ManagementLogoutHandler == nil &&
				opts.ManagementProxyOptionsHandler == nil &&
				opts.ManagementSystemAccountsHandler == nil &&
				opts.ManagementSystemAccountOptionsHandler == nil &&
				opts.ManagementAuthorizationGranteeAccountsHandler == nil &&
				opts.ManagementMyAuthorizationGranteeAccountsHandler == nil &&
				opts.ManagementAuthorizationGranteeTeamsHandler == nil &&
				opts.ManagementMyAuthorizationGranteeTeamsHandler == nil &&
				opts.ManagementAuthorizationGranteeGroupsHandler == nil &&
				opts.ManagementMyAuthorizationGranteeGroupsHandler == nil &&
				opts.ManagementProviderOptionsHandler == nil &&
				opts.ManagementProviderModelOptionsHandler == nil &&
				opts.ManagementProviderModelsHandler == nil &&
				opts.ManagementProviderDefaultTestModelHandler == nil &&
				opts.ManagementRouteStrategyOptionsHandler == nil &&
				opts.ManagementMyRouteStrategyOptionsHandler == nil &&
				opts.ManagementGroupOptionsHandler == nil &&
				opts.ManagementMyGroupOptionsHandler == nil &&
				opts.ManagementGroupAccountOptionsHandler == nil &&
				opts.ManagementMyGroupAccountOptionsHandler == nil &&
				opts.ManagementAccountOptionsHandler == nil &&
				opts.ManagementMyAccountOptionsHandler == nil &&
				opts.ManagementAccountTagsHandler == nil &&
				opts.ManagementMyAccountTagsHandler == nil &&
				opts.ManagementAccountTagDeleteHandler == nil &&
				opts.ManagementMyAccountTagDeleteHandler == nil &&
				opts.ManagementAccountTagUpdateHandler == nil &&
				opts.ManagementMyAccountTagUpdateHandler == nil &&
				opts.ManagementOperationLogsHandler == nil &&
				opts.ManagementMyOperationLogsHandler == nil {
				panic("at least one management API handler is required when JUHE_AI_MANAGEMENT_API_ENABLED is true")
			}
			if opts.ManagementCurrentUserHandler != nil {
				system.Get("/auth/me", opts.ManagementCurrentUserHandler.ServeHTTP)
			}
			if opts.ManagementProfileUpdateHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Patch("/auth/me", opts.ManagementProfileUpdateHandler.ServeHTTP)
			}
			if opts.ManagementLogoutHandler != nil {
				system.Post("/auth/logout", opts.ManagementLogoutHandler.ServeHTTP)
			}
			if opts.ManagementProxyOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/proxies/options", opts.ManagementProxyOptionsHandler.ServeHTTP)
			}
			if opts.ManagementSystemAccountsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/system-accounts", opts.ManagementSystemAccountsHandler.ServeHTTP)
			}
			if opts.ManagementSystemAccountOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/system-accounts/options", opts.ManagementSystemAccountOptionsHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationGranteeAccountsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/authorization-options/grantee-accounts", opts.ManagementAuthorizationGranteeAccountsHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationGranteeAccountsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-authorization-options/grantee-accounts", opts.ManagementMyAuthorizationGranteeAccountsHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationGranteeTeamsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/authorization-options/grantee-teams", opts.ManagementAuthorizationGranteeTeamsHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationGranteeTeamsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-authorization-options/grantee-teams", opts.ManagementMyAuthorizationGranteeTeamsHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationGranteeGroupsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/authorization-options/grantee-groups", opts.ManagementAuthorizationGranteeGroupsHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationGranteeGroupsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-authorization-options/grantee-groups", opts.ManagementMyAuthorizationGranteeGroupsHandler.ServeHTTP)
			}
			if opts.ManagementProviderOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/providers/options", opts.ManagementProviderOptionsHandler.ServeHTTP)
			}
			if opts.ManagementProviderModelOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/providers/models/options", opts.ManagementProviderModelOptionsHandler.ServeHTTP)
			}
			if opts.ManagementProviderModelsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/providers/{code}/models", opts.ManagementProviderModelsHandler.ServeHTTP)
			}
			if opts.ManagementProviderDefaultTestModelHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Put("/providers/{code}/default-test-model", opts.ManagementProviderDefaultTestModelHandler.ServeHTTP)
			}
			if opts.ManagementRouteStrategyOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/route-strategies/options", opts.ManagementRouteStrategyOptionsHandler.ServeHTTP)
			}
			if opts.ManagementMyRouteStrategyOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-route-strategies/options", opts.ManagementMyRouteStrategyOptionsHandler.ServeHTTP)
			}
			if opts.ManagementGroupOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/groups/options", opts.ManagementGroupOptionsHandler.ServeHTTP)
			}
			if opts.ManagementMyGroupOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-groups/options", opts.ManagementMyGroupOptionsHandler.ServeHTTP)
			}
			if opts.ManagementGroupAccountOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/groups/account-options", opts.ManagementGroupAccountOptionsHandler.ServeHTTP)
			}
			if opts.ManagementMyGroupAccountOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-groups/account-options", opts.ManagementMyGroupAccountOptionsHandler.ServeHTTP)
			}
			if opts.ManagementAccountOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/accounts/options", opts.ManagementAccountOptionsHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-accounts/options", opts.ManagementMyAccountOptionsHandler.ServeHTTP)
			}
			if opts.ManagementAccountTagsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/accounts/tags", opts.ManagementAccountTagsHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountTagsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-accounts/tags", opts.ManagementMyAccountTagsHandler.ServeHTTP)
			}
			if opts.ManagementAccountTagDeleteHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Delete("/accounts/tags/{tagId}", opts.ManagementAccountTagDeleteHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountTagDeleteHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Delete("/my-accounts/tags/{tagId}", opts.ManagementMyAccountTagDeleteHandler.ServeHTTP)
			}
			if opts.ManagementAccountTagUpdateHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Patch("/accounts/{id}/tags", opts.ManagementAccountTagUpdateHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountTagUpdateHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Patch("/my-accounts/{id}/tags", opts.ManagementMyAccountTagUpdateHandler.ServeHTTP)
			}
			if opts.ManagementOperationLogsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/operation-logs", opts.ManagementOperationLogsHandler.ServeHTTP)
				system.With(opts.ManagementAPIAuthMiddleware).Get("/operation-logs/{id}", opts.ManagementOperationLogsHandler.ServeHTTP)
			}
			if opts.ManagementMyOperationLogsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-operation-logs", opts.ManagementMyOperationLogsHandler.ServeHTTP)
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-operation-logs/{id}", opts.ManagementMyOperationLogsHandler.ServeHTTP)
			}
		}
	})

	if opts.Config.MetricsEnabled {
		r.With(loopbackOnlyMiddleware(clientIPs)).Handle("/__aisys__/metrics", promhttp.Handler())
	}

	if opts.Config.PprofEnabled {
		r.Group(func(debug chi.Router) {
			debug.Use(loopbackOnlyMiddleware(clientIPs))
			debug.Get("/__debug/pprof", pprof.Index)
			debug.Get("/__debug/pprof/*", pprof.Index)
			debug.Get("/__debug/pprof/cmdline", pprof.Cmdline)
			debug.Get("/__debug/pprof/profile", pprof.Profile)
			debug.Get("/__debug/pprof/symbol", pprof.Symbol)
			debug.Get("/__debug/pprof/trace", pprof.Trace)
		})
	}

	if opts.Config.PublicAPIEnabled {
		if opts.PublicAPIHandler == nil {
			panic("PublicAPIHandler is required when JUHE_AI_PUBLIC_API_ENABLED is true")
		}
		r.Handle(publicapi.Prefix, opts.PublicAPIHandler)
		r.Handle(publicapi.Prefix+"/*", opts.PublicAPIHandler)
	}

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "接口不存在")
	})

	return r
}
