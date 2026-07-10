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
	Config                                            config.Config
	Logger                                            *slog.Logger
	PublicSettingsService                             *publicsettings.Service
	SystemAPIRateLimitReader                          port.SystemAPIRateLimitReader
	SystemAPIClientIPAllowlistReader                  port.SystemAPIClientIPAllowlistReader
	SystemAPIClientIPAllowlistVersionReader           SystemAPIClientIPAllowlistVersionReader
	SystemAPIIPRateLimiter                            SystemAPIIPRateLimiter
	SystemAPIAuthenticatedRateLimiter                 SystemAPIAuthenticatedRateLimiter
	PublicAPIHandler                                  http.Handler
	ManagementAPIAuthMiddleware                       func(http.Handler) http.Handler
	ManagementAPIAuthTouchMiddleware                  func(http.Handler) http.Handler
	ManagementCaptchaHandler                          http.Handler
	ManagementLoginHandler                            http.Handler
	ManagementCurrentUserHandler                      http.Handler
	ManagementProfileUpdateHandler                    http.Handler
	ManagementPasswordChangeHandler                   http.Handler
	ManagementLogoutHandler                           http.Handler
	ManagementSessionListHandler                      http.Handler
	ManagementSessionRevokeHandler                    http.Handler
	ManagementProxiesHandler                          http.Handler
	ManagementProxyOptionsHandler                     http.Handler
	ManagementProxyCreateHandler                      http.Handler
	ManagementProxyUpdateHandler                      http.Handler
	ManagementProxyDeleteHandler                      http.Handler
	ManagementProxyTestHandler                        http.Handler
	ManagementSystemAccountsHandler                   http.Handler
	ManagementSystemAccountOptionsHandler             http.Handler
	ManagementSystemAccountPatchHandler               http.Handler
	ManagementSystemAccountCreateHandler              http.Handler
	ManagementSystemTeamsHandler                      http.Handler
	ManagementMySystemTeamsHandler                    http.Handler
	ManagementSystemTeamCreateHandler                 http.Handler
	ManagementSystemTeamPatchHandler                  http.Handler
	ManagementSystemTeamMembersAddHandler             http.Handler
	ManagementSystemTeamMemberDeleteHandler           http.Handler
	ManagementAuthorizationGranteeAccountsHandler     http.Handler
	ManagementMyAuthorizationGranteeAccountsHandler   http.Handler
	ManagementAuthorizationGranteeTeamsHandler        http.Handler
	ManagementMyAuthorizationGranteeTeamsHandler      http.Handler
	ManagementAuthorizationGranteeGroupsHandler       http.Handler
	ManagementMyAuthorizationGranteeGroupsHandler     http.Handler
	ManagementAuthorizationListHandler                http.Handler
	ManagementMyAuthorizationListHandler              http.Handler
	ManagementAuthorizationTeamUsageOverviewHandler   http.Handler
	ManagementMyAuthorizationTeamUsageOverviewHandler http.Handler
	ManagementAuthorizationUserUsageOverviewHandler   http.Handler
	ManagementMyAuthorizationUserUsageOverviewHandler http.Handler
	ManagementAuthorizationUsageHandler               http.Handler
	ManagementMyAuthorizationUsageHandler             http.Handler
	ManagementAuthorizationDetailHandler              http.Handler
	ManagementMyAuthorizationDetailHandler            http.Handler
	ManagementAuthorizationCreateHandler              http.Handler
	ManagementMyAuthorizationCreateHandler            http.Handler
	ManagementAuthorizationUpdateHandler              http.Handler
	ManagementMyAuthorizationUpdateHandler            http.Handler
	ManagementAuthorizationExpireUpdateHandler        http.Handler
	ManagementMyAuthorizationExpireUpdateHandler      http.Handler
	ManagementAuthorizationReturnHandler              http.Handler
	ManagementMyAuthorizationReturnHandler            http.Handler
	ManagementAccountAuthorizationReturnHandler       http.Handler
	ManagementMyAccountAuthorizationReturnHandler     http.Handler
	ManagementGroupAuthorizationReturnHandler         http.Handler
	ManagementMyGroupAuthorizationReturnHandler       http.Handler
	ManagementAuthorizationRevokeHandler              http.Handler
	ManagementMyAuthorizationRevokeHandler            http.Handler
	ManagementProvidersHandler                        http.Handler
	ManagementProviderOptionsHandler                  http.Handler
	ManagementProviderModelOptionsHandler             http.Handler
	ManagementProviderModelsHandler                   http.Handler
	ManagementProviderDefaultHealthCheckModelHandler  http.Handler
	ManagementProviderCustomModelCreateHandler        http.Handler
	ManagementProviderCustomModelUpdateHandler        http.Handler
	ManagementProviderCustomModelDeleteHandler        http.Handler
	ManagementRouteStrategyOptionsHandler             http.Handler
	ManagementMyRouteStrategyOptionsHandler           http.Handler
	ManagementGroupListHandler                        http.Handler
	ManagementMyGroupListHandler                      http.Handler
	ManagementGroupCreateHandler                      http.Handler
	ManagementMyGroupCreateHandler                    http.Handler
	ManagementGroupOptionsHandler                     http.Handler
	ManagementMyGroupOptionsHandler                   http.Handler
	ManagementGroupAccountOptionsHandler              http.Handler
	ManagementMyGroupAccountOptionsHandler            http.Handler
	ManagementAccountOptionsHandler                   http.Handler
	ManagementMyAccountOptionsHandler                 http.Handler
	ManagementAccountTagsHandler                      http.Handler
	ManagementMyAccountTagsHandler                    http.Handler
	ManagementAccountTagDeleteHandler                 http.Handler
	ManagementMyAccountTagDeleteHandler               http.Handler
	ManagementAccountTagUpdateHandler                 http.Handler
	ManagementMyAccountTagUpdateHandler               http.Handler
	ManagementSystemSettingsHandler                   http.Handler
	ManagementSystemSettingsUpdateHandler             http.Handler
	ManagementGlobalSettingsHandler                   http.Handler
	ManagementGlobalSettingsUpdateHandler             http.Handler
	ManagementOperationLogsHandler                    http.Handler
	ManagementMyOperationLogsHandler                  http.Handler
	ManagementStatsUsageWindowHandler                 http.Handler
	ManagementMyStatsUsageWindowHandler               http.Handler
}

func NewRouter(opts RouterOptions) http.Handler {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware)
	r.Use(recoverMiddleware(opts.Logger))
	clientIPs := newClientIPResolver(opts.Config)
	systemAPIClientIPAllowlist := newSystemAPIClientIPAllowlistInspector(
		opts.SystemAPIClientIPAllowlistReader,
		opts.SystemAPIClientIPAllowlistVersionReader,
	)
	mutationGuards := newMutationGuardStore()

	health := NewHealthHandler(opts.Config, opts.Logger)
	r.Get("/__aisys__/health", health.ServeHTTP)
	r.Route("/__aisys__/api", func(system chi.Router) {
		system.Use(noStoreMiddleware)
		if opts.SystemAPIRateLimitReader != nil {
			system.Use(newSystemAPIIPRateLimitMiddleware(
				opts.SystemAPIRateLimitReader,
				opts.SystemAPIIPRateLimiter,
				clientIPs,
				systemAPIClientIPAllowlist,
				opts.Logger,
			))
		}
		system.Get("/health", health.ServeHTTP)
		if opts.PublicSettingsService != nil {
			publicSettingsHandler := NewPublicSettingsHandler(*opts.PublicSettingsService, opts.Logger)
			system.Get("/settings/public", publicSettingsHandler.ServeHTTP)
		}
		if opts.Config.ManagementAPIEnabled || opts.Config.ManagementAuthSessionsEnabled {
			if opts.ManagementAPIAuthMiddleware == nil {
				panic("ManagementAPIAuthMiddleware is required when Go management routes are enabled")
			}
			writeRoutesConfigured := managementWriteRoutesConfigured(opts)
			if !opts.Config.ManagementAPIEnabled {
				writeRoutesConfigured = opts.ManagementSessionRevokeHandler != nil
			}
			if writeRoutesConfigured && opts.ManagementAPIAuthTouchMiddleware == nil {
				panic("ManagementAPIAuthTouchMiddleware is required for Go management write routes")
			}
			managementAPIWriteAuthMiddleware := opts.ManagementAPIAuthTouchMiddleware
			managementAPIReadRateLimitMiddleware := opts.ManagementAPIAuthMiddleware
			managementAPIWriteRateLimitMiddleware := managementAPIWriteAuthMiddleware
			if opts.Config.ManagementAPIEnabled &&
				opts.SystemAPIRateLimitReader != nil &&
				managementBusinessRoutesConfigured(opts) {
				if opts.SystemAPIAuthenticatedRateLimiter == nil {
					panic("SystemAPIAuthenticatedRateLimiter is required when system API rate limiting covers Go management business routes")
				}
				managementAPIReadRateLimitMiddleware = chainHTTPMiddleware(
					opts.ManagementAPIAuthMiddleware,
					newSystemAPIAuthenticatedRateLimitMiddleware(
						opts.SystemAPIRateLimitReader,
						opts.SystemAPIAuthenticatedRateLimiter,
						systemAPIMethodRead,
						clientIPs,
						systemAPIClientIPAllowlist,
						opts.Logger,
					),
				)
				if managementAPIWriteAuthMiddleware != nil {
					managementAPIWriteRateLimitMiddleware = chainHTTPMiddleware(
						managementAPIWriteAuthMiddleware,
						newSystemAPIAuthenticatedRateLimitMiddleware(
							opts.SystemAPIRateLimitReader,
							opts.SystemAPIAuthenticatedRateLimiter,
							systemAPIMethodWrite,
							clientIPs,
							systemAPIClientIPAllowlist,
							opts.Logger,
						),
					)
				}
			}
			if !opts.Config.ManagementAPIEnabled &&
				opts.ManagementSessionListHandler == nil &&
				opts.ManagementSessionRevokeHandler == nil {
				panic("at least one management auth session handler is required when JUHE_AI_MANAGEMENT_AUTH_SESSIONS_ENABLED is true")
			}
			if opts.Config.ManagementAPIEnabled &&
				opts.ManagementCaptchaHandler == nil &&
				opts.ManagementLoginHandler == nil &&
				opts.ManagementCurrentUserHandler == nil &&
				opts.ManagementProfileUpdateHandler == nil &&
				opts.ManagementPasswordChangeHandler == nil &&
				opts.ManagementLogoutHandler == nil &&
				opts.ManagementSessionListHandler == nil &&
				opts.ManagementSessionRevokeHandler == nil &&
				opts.ManagementProxiesHandler == nil &&
				opts.ManagementProxyOptionsHandler == nil &&
				opts.ManagementProxyCreateHandler == nil &&
				opts.ManagementProxyUpdateHandler == nil &&
				opts.ManagementProxyDeleteHandler == nil &&
				opts.ManagementProxyTestHandler == nil &&
				opts.ManagementSystemAccountsHandler == nil &&
				opts.ManagementSystemAccountOptionsHandler == nil &&
				opts.ManagementSystemAccountPatchHandler == nil &&
				opts.ManagementSystemAccountCreateHandler == nil &&
				opts.ManagementSystemTeamsHandler == nil &&
				opts.ManagementMySystemTeamsHandler == nil &&
				opts.ManagementSystemTeamCreateHandler == nil &&
				opts.ManagementSystemTeamPatchHandler == nil &&
				opts.ManagementSystemTeamMembersAddHandler == nil &&
				opts.ManagementSystemTeamMemberDeleteHandler == nil &&
				opts.ManagementAuthorizationGranteeAccountsHandler == nil &&
				opts.ManagementMyAuthorizationGranteeAccountsHandler == nil &&
				opts.ManagementAuthorizationGranteeTeamsHandler == nil &&
				opts.ManagementMyAuthorizationGranteeTeamsHandler == nil &&
				opts.ManagementAuthorizationGranteeGroupsHandler == nil &&
				opts.ManagementMyAuthorizationGranteeGroupsHandler == nil &&
				opts.ManagementAuthorizationListHandler == nil &&
				opts.ManagementMyAuthorizationListHandler == nil &&
				opts.ManagementAuthorizationTeamUsageOverviewHandler == nil &&
				opts.ManagementMyAuthorizationTeamUsageOverviewHandler == nil &&
				opts.ManagementAuthorizationUserUsageOverviewHandler == nil &&
				opts.ManagementMyAuthorizationUserUsageOverviewHandler == nil &&
				opts.ManagementAuthorizationUsageHandler == nil &&
				opts.ManagementMyAuthorizationUsageHandler == nil &&
				opts.ManagementAuthorizationDetailHandler == nil &&
				opts.ManagementMyAuthorizationDetailHandler == nil &&
				opts.ManagementAuthorizationCreateHandler == nil &&
				opts.ManagementMyAuthorizationCreateHandler == nil &&
				opts.ManagementAuthorizationUpdateHandler == nil &&
				opts.ManagementMyAuthorizationUpdateHandler == nil &&
				opts.ManagementAuthorizationExpireUpdateHandler == nil &&
				opts.ManagementMyAuthorizationExpireUpdateHandler == nil &&
				opts.ManagementAuthorizationReturnHandler == nil &&
				opts.ManagementMyAuthorizationReturnHandler == nil &&
				opts.ManagementAccountAuthorizationReturnHandler == nil &&
				opts.ManagementMyAccountAuthorizationReturnHandler == nil &&
				opts.ManagementGroupAuthorizationReturnHandler == nil &&
				opts.ManagementMyGroupAuthorizationReturnHandler == nil &&
				opts.ManagementAuthorizationRevokeHandler == nil &&
				opts.ManagementMyAuthorizationRevokeHandler == nil &&
				opts.ManagementProvidersHandler == nil &&
				opts.ManagementProviderOptionsHandler == nil &&
				opts.ManagementProviderModelOptionsHandler == nil &&
				opts.ManagementProviderModelsHandler == nil &&
				opts.ManagementProviderDefaultHealthCheckModelHandler == nil &&
				opts.ManagementProviderCustomModelCreateHandler == nil &&
				opts.ManagementProviderCustomModelUpdateHandler == nil &&
				opts.ManagementProviderCustomModelDeleteHandler == nil &&
				opts.ManagementRouteStrategyOptionsHandler == nil &&
				opts.ManagementMyRouteStrategyOptionsHandler == nil &&
				opts.ManagementGroupListHandler == nil &&
				opts.ManagementMyGroupListHandler == nil &&
				opts.ManagementGroupCreateHandler == nil &&
				opts.ManagementMyGroupCreateHandler == nil &&
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
				opts.ManagementSystemSettingsHandler == nil &&
				opts.ManagementSystemSettingsUpdateHandler == nil &&
				opts.ManagementGlobalSettingsHandler == nil &&
				opts.ManagementGlobalSettingsUpdateHandler == nil &&
				opts.ManagementOperationLogsHandler == nil &&
				opts.ManagementMyOperationLogsHandler == nil &&
				opts.ManagementStatsUsageWindowHandler == nil &&
				opts.ManagementMyStatsUsageWindowHandler == nil {
				panic("at least one management API handler is required when JUHE_AI_MANAGEMENT_API_ENABLED is true")
			}
			if opts.Config.ManagementAPIEnabled {
				if opts.ManagementCaptchaHandler != nil {
					system.Get("/auth/captcha", opts.ManagementCaptchaHandler.ServeHTTP)
				}
				if opts.ManagementLoginHandler != nil {
					system.Post("/auth/login", opts.ManagementLoginHandler.ServeHTTP)
				}
				if opts.ManagementCurrentUserHandler != nil {
					system.Get("/auth/me", opts.ManagementCurrentUserHandler.ServeHTTP)
				}
				if opts.ManagementProfileUpdateHandler != nil {
					system.With(managementAPIWriteAuthMiddleware).Patch("/auth/me", opts.ManagementProfileUpdateHandler.ServeHTTP)
				}
				if opts.ManagementPasswordChangeHandler != nil {
					system.Post("/auth/change-password", opts.ManagementPasswordChangeHandler.ServeHTTP)
				}
				if opts.ManagementLogoutHandler != nil {
					system.Post("/auth/logout", opts.ManagementLogoutHandler.ServeHTTP)
				}
			}
			if opts.ManagementSessionListHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/auth/sessions", opts.ManagementSessionListHandler.ServeHTTP)
			}
			if opts.ManagementSessionRevokeHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Delete("/auth/sessions/{id}", opts.ManagementSessionRevokeHandler.ServeHTTP)
			}
			if !opts.Config.ManagementAPIEnabled {
				return
			}
			if opts.ManagementProxiesHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/proxies", opts.ManagementProxiesHandler.ServeHTTP)
			}
			if opts.ManagementProxyOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/proxies/options", opts.ManagementProxyOptionsHandler.ServeHTTP)
			}
			if opts.ManagementProxyCreateHandler != nil {
				system.With(
					managementAPIWriteRateLimitMiddleware,
					mutationGuards.Middleware(managementProxyCreateMutationGuardConfig()),
				).Post("/proxies", opts.ManagementProxyCreateHandler.ServeHTTP)
			}
			if opts.ManagementProxyUpdateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Patch("/proxies/{id}", opts.ManagementProxyUpdateHandler.ServeHTTP)
			}
			if opts.ManagementProxyDeleteHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Delete("/proxies/{id}", opts.ManagementProxyDeleteHandler.ServeHTTP)
			}
			if opts.ManagementProxyTestHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Post("/proxies/{id}/test", opts.ManagementProxyTestHandler.ServeHTTP)
			}
			if opts.ManagementSystemAccountsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/system-accounts", opts.ManagementSystemAccountsHandler.ServeHTTP)
			}
			if opts.ManagementSystemAccountOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/system-accounts/options", opts.ManagementSystemAccountOptionsHandler.ServeHTTP)
			}
			if opts.ManagementSystemAccountPatchHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Patch("/system-accounts/{id}", opts.ManagementSystemAccountPatchHandler.ServeHTTP)
			}
			if opts.ManagementSystemAccountCreateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Post("/system-accounts", opts.ManagementSystemAccountCreateHandler.ServeHTTP)
			}
			if opts.ManagementSystemTeamsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/system-teams", opts.ManagementSystemTeamsHandler.ServeHTTP)
				system.With(managementAPIReadRateLimitMiddleware).Get("/system-teams/{id}", opts.ManagementSystemTeamsHandler.ServeHTTP)
			}
			if opts.ManagementMySystemTeamsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-teams", opts.ManagementMySystemTeamsHandler.ServeHTTP)
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-teams/{id}", opts.ManagementMySystemTeamsHandler.ServeHTTP)
			}
			if opts.ManagementSystemTeamCreateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Post("/system-teams", opts.ManagementSystemTeamCreateHandler.ServeHTTP)
			}
			if opts.ManagementSystemTeamPatchHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Patch("/system-teams/{id}", opts.ManagementSystemTeamPatchHandler.ServeHTTP)
			}
			if opts.ManagementSystemTeamMembersAddHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Post("/system-teams/{id}/members", opts.ManagementSystemTeamMembersAddHandler.ServeHTTP)
			}
			if opts.ManagementSystemTeamMemberDeleteHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Delete("/system-teams/{id}/members/{memberId}", opts.ManagementSystemTeamMemberDeleteHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationGranteeAccountsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/authorization-options/grantee-accounts", opts.ManagementAuthorizationGranteeAccountsHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationGranteeAccountsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-authorization-options/grantee-accounts", opts.ManagementMyAuthorizationGranteeAccountsHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationGranteeTeamsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/authorization-options/grantee-teams", opts.ManagementAuthorizationGranteeTeamsHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationGranteeTeamsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-authorization-options/grantee-teams", opts.ManagementMyAuthorizationGranteeTeamsHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationGranteeGroupsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/authorization-options/grantee-groups", opts.ManagementAuthorizationGranteeGroupsHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationGranteeGroupsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-authorization-options/grantee-groups", opts.ManagementMyAuthorizationGranteeGroupsHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationListHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/authorizations", opts.ManagementAuthorizationListHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationListHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-authorizations", opts.ManagementMyAuthorizationListHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationTeamUsageOverviewHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/authorizations/usage/team-details", opts.ManagementAuthorizationTeamUsageOverviewHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationTeamUsageOverviewHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-authorizations/usage/team-details", opts.ManagementMyAuthorizationTeamUsageOverviewHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationUserUsageOverviewHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/authorizations/usage/user-details", opts.ManagementAuthorizationUserUsageOverviewHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationUserUsageOverviewHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-authorizations/usage/user-details", opts.ManagementMyAuthorizationUserUsageOverviewHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationUsageHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/authorizations/{id}/usage", opts.ManagementAuthorizationUsageHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationUsageHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-authorizations/{id}/usage", opts.ManagementMyAuthorizationUsageHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationDetailHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/authorizations/{id}", opts.ManagementAuthorizationDetailHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationDetailHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-authorizations/{id}", opts.ManagementMyAuthorizationDetailHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationCreateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Post("/authorizations", opts.ManagementAuthorizationCreateHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationCreateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Post("/my-authorizations", opts.ManagementMyAuthorizationCreateHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationUpdateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Patch("/authorizations/{id}", opts.ManagementAuthorizationUpdateHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationUpdateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Patch("/my-authorizations/{id}", opts.ManagementMyAuthorizationUpdateHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationExpireUpdateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Patch("/authorizations/{id}/expire", opts.ManagementAuthorizationExpireUpdateHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationExpireUpdateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Patch("/my-authorizations/{id}/expire", opts.ManagementMyAuthorizationExpireUpdateHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Delete("/authorizations/{id}/return", opts.ManagementAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Delete("/my-authorizations/{id}/return", opts.ManagementMyAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementAccountAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Post("/accounts/{id}/return-authorization", opts.ManagementAccountAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Post("/my-accounts/{id}/return-authorization", opts.ManagementMyAccountAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementGroupAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Post("/groups/{id}/return-authorization", opts.ManagementGroupAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementMyGroupAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Post("/my-groups/{id}/return-authorization", opts.ManagementMyGroupAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationRevokeHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Delete("/authorizations/{id}", opts.ManagementAuthorizationRevokeHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationRevokeHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Delete("/my-authorizations/{id}", opts.ManagementMyAuthorizationRevokeHandler.ServeHTTP)
			}
			if opts.ManagementProvidersHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/providers", opts.ManagementProvidersHandler.ServeHTTP)
			}
			if opts.ManagementProviderOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/providers/options", opts.ManagementProviderOptionsHandler.ServeHTTP)
			}
			if opts.ManagementProviderModelOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/providers/models/options", opts.ManagementProviderModelOptionsHandler.ServeHTTP)
			}
			if opts.ManagementProviderModelsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/providers/{code}/models", opts.ManagementProviderModelsHandler.ServeHTTP)
			}
			if opts.ManagementProviderDefaultHealthCheckModelHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Put("/providers/{code}/default-health-check-model", opts.ManagementProviderDefaultHealthCheckModelHandler.ServeHTTP)
			}
			if opts.ManagementProviderCustomModelCreateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Post("/providers/{code}/models", opts.ManagementProviderCustomModelCreateHandler.ServeHTTP)
			}
			if opts.ManagementProviderCustomModelUpdateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Patch("/providers/{code}/models/{id}", opts.ManagementProviderCustomModelUpdateHandler.ServeHTTP)
			}
			if opts.ManagementProviderCustomModelDeleteHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Delete("/providers/{code}/models/{id}", opts.ManagementProviderCustomModelDeleteHandler.ServeHTTP)
			}
			if opts.ManagementRouteStrategyOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/route-strategies/options", opts.ManagementRouteStrategyOptionsHandler.ServeHTTP)
			}
			if opts.ManagementMyRouteStrategyOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-route-strategies/options", opts.ManagementMyRouteStrategyOptionsHandler.ServeHTTP)
			}
			if opts.ManagementGroupListHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/groups", opts.ManagementGroupListHandler.ServeHTTP)
			}
			if opts.ManagementMyGroupListHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-groups", opts.ManagementMyGroupListHandler.ServeHTTP)
			}
			if opts.ManagementGroupCreateHandler != nil {
				system.With(
					managementGroupCreateJSONBodyMiddleware,
					managementAPIWriteRateLimitMiddleware,
					managementGroupAdminRoleMiddleware,
					mutationGuards.Middleware(managementGroupCreateMutationGuardConfig(managementGroupScopeAdmin)),
				).Post("/groups", opts.ManagementGroupCreateHandler.ServeHTTP)
			}
			if opts.ManagementMyGroupCreateHandler != nil {
				system.With(
					managementGroupCreateJSONBodyMiddleware,
					managementAPIWriteRateLimitMiddleware,
					mutationGuards.Middleware(managementGroupCreateMutationGuardConfig(managementGroupScopeSelf)),
				).Post("/my-groups", opts.ManagementMyGroupCreateHandler.ServeHTTP)
			}
			if opts.ManagementGroupOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/groups/options", opts.ManagementGroupOptionsHandler.ServeHTTP)
			}
			if opts.ManagementMyGroupOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-groups/options", opts.ManagementMyGroupOptionsHandler.ServeHTTP)
			}
			if opts.ManagementGroupAccountOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/groups/account-options", opts.ManagementGroupAccountOptionsHandler.ServeHTTP)
			}
			if opts.ManagementMyGroupAccountOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-groups/account-options", opts.ManagementMyGroupAccountOptionsHandler.ServeHTTP)
			}
			if opts.ManagementAccountOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/accounts/options", opts.ManagementAccountOptionsHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountOptionsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-accounts/options", opts.ManagementMyAccountOptionsHandler.ServeHTTP)
			}
			if opts.ManagementAccountTagsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/accounts/tags", opts.ManagementAccountTagsHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountTagsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-accounts/tags", opts.ManagementMyAccountTagsHandler.ServeHTTP)
			}
			if opts.ManagementAccountTagDeleteHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Delete("/accounts/tags/{tagId}", opts.ManagementAccountTagDeleteHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountTagDeleteHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Delete("/my-accounts/tags/{tagId}", opts.ManagementMyAccountTagDeleteHandler.ServeHTTP)
			}
			if opts.ManagementAccountTagUpdateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Patch("/accounts/{id}/tags", opts.ManagementAccountTagUpdateHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountTagUpdateHandler != nil {
				system.With(managementAPIWriteRateLimitMiddleware).Patch("/my-accounts/{id}/tags", opts.ManagementMyAccountTagUpdateHandler.ServeHTTP)
			}
			if opts.ManagementSystemSettingsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/settings", opts.ManagementSystemSettingsHandler.ServeHTTP)
			}
			if opts.ManagementSystemSettingsUpdateHandler != nil {
				system.With(managementSettingsJSONBodyMiddleware, managementAPIWriteRateLimitMiddleware).
					Patch("/settings", opts.ManagementSystemSettingsUpdateHandler.ServeHTTP)
			}
			if opts.ManagementGlobalSettingsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/settings/global", opts.ManagementGlobalSettingsHandler.ServeHTTP)
			}
			if opts.ManagementGlobalSettingsUpdateHandler != nil {
				system.With(managementSettingsJSONBodyMiddleware, managementAPIWriteRateLimitMiddleware).
					Patch("/settings/global", opts.ManagementGlobalSettingsUpdateHandler.ServeHTTP)
			}
			if opts.ManagementOperationLogsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/operation-logs", opts.ManagementOperationLogsHandler.ServeHTTP)
				system.With(managementAPIReadRateLimitMiddleware).Get("/operation-logs/{id}", opts.ManagementOperationLogsHandler.ServeHTTP)
			}
			if opts.ManagementMyOperationLogsHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-operation-logs", opts.ManagementMyOperationLogsHandler.ServeHTTP)
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-operation-logs/{id}", opts.ManagementMyOperationLogsHandler.ServeHTTP)
			}
			if opts.ManagementStatsUsageWindowHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/stats/usage-window", opts.ManagementStatsUsageWindowHandler.ServeHTTP)
			}
			if opts.ManagementMyStatsUsageWindowHandler != nil {
				system.With(managementAPIReadRateLimitMiddleware).Get("/my-stats/usage-window", opts.ManagementMyStatsUsageWindowHandler.ServeHTTP)
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

func chainHTTPMiddleware(first func(http.Handler) http.Handler, second func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return first(second(next))
	}
}

func managementBusinessRoutesConfigured(opts RouterOptions) bool {
	return opts.ManagementProxiesHandler != nil ||
		opts.ManagementProxyOptionsHandler != nil ||
		opts.ManagementProxyCreateHandler != nil ||
		opts.ManagementProxyUpdateHandler != nil ||
		opts.ManagementProxyDeleteHandler != nil ||
		opts.ManagementProxyTestHandler != nil ||
		opts.ManagementSystemAccountsHandler != nil ||
		opts.ManagementSystemAccountOptionsHandler != nil ||
		opts.ManagementSystemAccountPatchHandler != nil ||
		opts.ManagementSystemAccountCreateHandler != nil ||
		opts.ManagementSystemTeamsHandler != nil ||
		opts.ManagementMySystemTeamsHandler != nil ||
		opts.ManagementSystemTeamCreateHandler != nil ||
		opts.ManagementSystemTeamPatchHandler != nil ||
		opts.ManagementSystemTeamMembersAddHandler != nil ||
		opts.ManagementSystemTeamMemberDeleteHandler != nil ||
		opts.ManagementAuthorizationGranteeAccountsHandler != nil ||
		opts.ManagementMyAuthorizationGranteeAccountsHandler != nil ||
		opts.ManagementAuthorizationGranteeTeamsHandler != nil ||
		opts.ManagementMyAuthorizationGranteeTeamsHandler != nil ||
		opts.ManagementAuthorizationGranteeGroupsHandler != nil ||
		opts.ManagementMyAuthorizationGranteeGroupsHandler != nil ||
		opts.ManagementAuthorizationListHandler != nil ||
		opts.ManagementMyAuthorizationListHandler != nil ||
		opts.ManagementAuthorizationTeamUsageOverviewHandler != nil ||
		opts.ManagementMyAuthorizationTeamUsageOverviewHandler != nil ||
		opts.ManagementAuthorizationUserUsageOverviewHandler != nil ||
		opts.ManagementMyAuthorizationUserUsageOverviewHandler != nil ||
		opts.ManagementAuthorizationUsageHandler != nil ||
		opts.ManagementMyAuthorizationUsageHandler != nil ||
		opts.ManagementAuthorizationDetailHandler != nil ||
		opts.ManagementMyAuthorizationDetailHandler != nil ||
		opts.ManagementAuthorizationCreateHandler != nil ||
		opts.ManagementMyAuthorizationCreateHandler != nil ||
		opts.ManagementAuthorizationUpdateHandler != nil ||
		opts.ManagementMyAuthorizationUpdateHandler != nil ||
		opts.ManagementAuthorizationExpireUpdateHandler != nil ||
		opts.ManagementMyAuthorizationExpireUpdateHandler != nil ||
		opts.ManagementAuthorizationReturnHandler != nil ||
		opts.ManagementMyAuthorizationReturnHandler != nil ||
		opts.ManagementAccountAuthorizationReturnHandler != nil ||
		opts.ManagementMyAccountAuthorizationReturnHandler != nil ||
		opts.ManagementGroupAuthorizationReturnHandler != nil ||
		opts.ManagementMyGroupAuthorizationReturnHandler != nil ||
		opts.ManagementAuthorizationRevokeHandler != nil ||
		opts.ManagementMyAuthorizationRevokeHandler != nil ||
		opts.ManagementProvidersHandler != nil ||
		opts.ManagementProviderOptionsHandler != nil ||
		opts.ManagementProviderModelOptionsHandler != nil ||
		opts.ManagementProviderModelsHandler != nil ||
		opts.ManagementProviderDefaultHealthCheckModelHandler != nil ||
		opts.ManagementProviderCustomModelCreateHandler != nil ||
		opts.ManagementProviderCustomModelUpdateHandler != nil ||
		opts.ManagementProviderCustomModelDeleteHandler != nil ||
		opts.ManagementRouteStrategyOptionsHandler != nil ||
		opts.ManagementMyRouteStrategyOptionsHandler != nil ||
		opts.ManagementGroupListHandler != nil ||
		opts.ManagementMyGroupListHandler != nil ||
		opts.ManagementGroupCreateHandler != nil ||
		opts.ManagementMyGroupCreateHandler != nil ||
		opts.ManagementGroupOptionsHandler != nil ||
		opts.ManagementMyGroupOptionsHandler != nil ||
		opts.ManagementGroupAccountOptionsHandler != nil ||
		opts.ManagementMyGroupAccountOptionsHandler != nil ||
		opts.ManagementAccountOptionsHandler != nil ||
		opts.ManagementMyAccountOptionsHandler != nil ||
		opts.ManagementAccountTagsHandler != nil ||
		opts.ManagementMyAccountTagsHandler != nil ||
		opts.ManagementAccountTagDeleteHandler != nil ||
		opts.ManagementMyAccountTagDeleteHandler != nil ||
		opts.ManagementAccountTagUpdateHandler != nil ||
		opts.ManagementMyAccountTagUpdateHandler != nil ||
		opts.ManagementSystemSettingsHandler != nil ||
		opts.ManagementSystemSettingsUpdateHandler != nil ||
		opts.ManagementGlobalSettingsHandler != nil ||
		opts.ManagementGlobalSettingsUpdateHandler != nil ||
		opts.ManagementOperationLogsHandler != nil ||
		opts.ManagementMyOperationLogsHandler != nil ||
		opts.ManagementStatsUsageWindowHandler != nil ||
		opts.ManagementMyStatsUsageWindowHandler != nil
}

func managementWriteRoutesConfigured(opts RouterOptions) bool {
	return opts.ManagementProfileUpdateHandler != nil ||
		opts.ManagementSessionRevokeHandler != nil ||
		opts.ManagementProxyCreateHandler != nil ||
		opts.ManagementProxyUpdateHandler != nil ||
		opts.ManagementProxyDeleteHandler != nil ||
		opts.ManagementProxyTestHandler != nil ||
		opts.ManagementSystemAccountPatchHandler != nil ||
		opts.ManagementSystemAccountCreateHandler != nil ||
		opts.ManagementSystemTeamCreateHandler != nil ||
		opts.ManagementSystemTeamPatchHandler != nil ||
		opts.ManagementSystemTeamMembersAddHandler != nil ||
		opts.ManagementSystemTeamMemberDeleteHandler != nil ||
		opts.ManagementAuthorizationCreateHandler != nil ||
		opts.ManagementMyAuthorizationCreateHandler != nil ||
		opts.ManagementAuthorizationUpdateHandler != nil ||
		opts.ManagementMyAuthorizationUpdateHandler != nil ||
		opts.ManagementAuthorizationExpireUpdateHandler != nil ||
		opts.ManagementMyAuthorizationExpireUpdateHandler != nil ||
		opts.ManagementAuthorizationReturnHandler != nil ||
		opts.ManagementMyAuthorizationReturnHandler != nil ||
		opts.ManagementAccountAuthorizationReturnHandler != nil ||
		opts.ManagementMyAccountAuthorizationReturnHandler != nil ||
		opts.ManagementGroupAuthorizationReturnHandler != nil ||
		opts.ManagementMyGroupAuthorizationReturnHandler != nil ||
		opts.ManagementAuthorizationRevokeHandler != nil ||
		opts.ManagementMyAuthorizationRevokeHandler != nil ||
		opts.ManagementProviderDefaultHealthCheckModelHandler != nil ||
		opts.ManagementProviderCustomModelCreateHandler != nil ||
		opts.ManagementProviderCustomModelUpdateHandler != nil ||
		opts.ManagementProviderCustomModelDeleteHandler != nil ||
		opts.ManagementGroupCreateHandler != nil ||
		opts.ManagementMyGroupCreateHandler != nil ||
		opts.ManagementAccountTagDeleteHandler != nil ||
		opts.ManagementMyAccountTagDeleteHandler != nil ||
		opts.ManagementAccountTagUpdateHandler != nil ||
		opts.ManagementMyAccountTagUpdateHandler != nil ||
		opts.ManagementSystemSettingsUpdateHandler != nil ||
		opts.ManagementGlobalSettingsUpdateHandler != nil
}
