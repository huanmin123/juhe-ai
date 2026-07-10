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
	SystemAPIIPRateLimitReader                        port.SystemAPIIPRateLimitReader
	SystemAPIIPReadRateLimiter                        SystemAPIIPReadRateLimiter
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
	ManagementProviderDefaultTestModelHandler         http.Handler
	ManagementProviderCustomModelCreateHandler        http.Handler
	ManagementProviderCustomModelUpdateHandler        http.Handler
	ManagementProviderCustomModelDeleteHandler        http.Handler
	ManagementRouteStrategyOptionsHandler             http.Handler
	ManagementMyRouteStrategyOptionsHandler           http.Handler
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
	ManagementOperationLogsHandler                    http.Handler
	ManagementMyOperationLogsHandler                  http.Handler
}

func NewRouter(opts RouterOptions) http.Handler {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware)
	r.Use(recoverMiddleware(opts.Logger))
	clientIPs := newClientIPResolver(opts.Config)
	mutationGuards := newMutationGuardStore()

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
				opts.ManagementProviderDefaultTestModelHandler == nil &&
				opts.ManagementProviderCustomModelCreateHandler == nil &&
				opts.ManagementProviderCustomModelUpdateHandler == nil &&
				opts.ManagementProviderCustomModelDeleteHandler == nil &&
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
				system.With(opts.ManagementAPIAuthMiddleware).Get("/proxies", opts.ManagementProxiesHandler.ServeHTTP)
			}
			if opts.ManagementProxyOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/proxies/options", opts.ManagementProxyOptionsHandler.ServeHTTP)
			}
			if opts.ManagementProxyCreateHandler != nil {
				system.With(
					managementAPIWriteAuthMiddleware,
					mutationGuards.Middleware(managementProxyCreateMutationGuardConfig()),
				).Post("/proxies", opts.ManagementProxyCreateHandler.ServeHTTP)
			}
			if opts.ManagementProxyUpdateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Patch("/proxies/{id}", opts.ManagementProxyUpdateHandler.ServeHTTP)
			}
			if opts.ManagementProxyDeleteHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Delete("/proxies/{id}", opts.ManagementProxyDeleteHandler.ServeHTTP)
			}
			if opts.ManagementProxyTestHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Post("/proxies/{id}/test", opts.ManagementProxyTestHandler.ServeHTTP)
			}
			if opts.ManagementSystemAccountsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/system-accounts", opts.ManagementSystemAccountsHandler.ServeHTTP)
			}
			if opts.ManagementSystemAccountOptionsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/system-accounts/options", opts.ManagementSystemAccountOptionsHandler.ServeHTTP)
			}
			if opts.ManagementSystemAccountPatchHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Patch("/system-accounts/{id}", opts.ManagementSystemAccountPatchHandler.ServeHTTP)
			}
			if opts.ManagementSystemAccountCreateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Post("/system-accounts", opts.ManagementSystemAccountCreateHandler.ServeHTTP)
			}
			if opts.ManagementSystemTeamsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/system-teams", opts.ManagementSystemTeamsHandler.ServeHTTP)
				system.With(opts.ManagementAPIAuthMiddleware).Get("/system-teams/{id}", opts.ManagementSystemTeamsHandler.ServeHTTP)
			}
			if opts.ManagementMySystemTeamsHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-teams", opts.ManagementMySystemTeamsHandler.ServeHTTP)
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-teams/{id}", opts.ManagementMySystemTeamsHandler.ServeHTTP)
			}
			if opts.ManagementSystemTeamCreateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Post("/system-teams", opts.ManagementSystemTeamCreateHandler.ServeHTTP)
			}
			if opts.ManagementSystemTeamPatchHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Patch("/system-teams/{id}", opts.ManagementSystemTeamPatchHandler.ServeHTTP)
			}
			if opts.ManagementSystemTeamMembersAddHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Post("/system-teams/{id}/members", opts.ManagementSystemTeamMembersAddHandler.ServeHTTP)
			}
			if opts.ManagementSystemTeamMemberDeleteHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Delete("/system-teams/{id}/members/{memberId}", opts.ManagementSystemTeamMemberDeleteHandler.ServeHTTP)
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
			if opts.ManagementAuthorizationListHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/authorizations", opts.ManagementAuthorizationListHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationListHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-authorizations", opts.ManagementMyAuthorizationListHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationTeamUsageOverviewHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/authorizations/usage/team-details", opts.ManagementAuthorizationTeamUsageOverviewHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationTeamUsageOverviewHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-authorizations/usage/team-details", opts.ManagementMyAuthorizationTeamUsageOverviewHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationUserUsageOverviewHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/authorizations/usage/user-details", opts.ManagementAuthorizationUserUsageOverviewHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationUserUsageOverviewHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-authorizations/usage/user-details", opts.ManagementMyAuthorizationUserUsageOverviewHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationUsageHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/authorizations/{id}/usage", opts.ManagementAuthorizationUsageHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationUsageHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-authorizations/{id}/usage", opts.ManagementMyAuthorizationUsageHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationDetailHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/authorizations/{id}", opts.ManagementAuthorizationDetailHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationDetailHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/my-authorizations/{id}", opts.ManagementMyAuthorizationDetailHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationCreateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Post("/authorizations", opts.ManagementAuthorizationCreateHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationCreateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Post("/my-authorizations", opts.ManagementMyAuthorizationCreateHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationUpdateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Patch("/authorizations/{id}", opts.ManagementAuthorizationUpdateHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationUpdateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Patch("/my-authorizations/{id}", opts.ManagementMyAuthorizationUpdateHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationExpireUpdateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Patch("/authorizations/{id}/expire", opts.ManagementAuthorizationExpireUpdateHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationExpireUpdateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Patch("/my-authorizations/{id}/expire", opts.ManagementMyAuthorizationExpireUpdateHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Delete("/authorizations/{id}/return", opts.ManagementAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Delete("/my-authorizations/{id}/return", opts.ManagementMyAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementAccountAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Post("/accounts/{id}/return-authorization", opts.ManagementAccountAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Post("/my-accounts/{id}/return-authorization", opts.ManagementMyAccountAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementGroupAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Post("/groups/{id}/return-authorization", opts.ManagementGroupAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementMyGroupAuthorizationReturnHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Post("/my-groups/{id}/return-authorization", opts.ManagementMyGroupAuthorizationReturnHandler.ServeHTTP)
			}
			if opts.ManagementAuthorizationRevokeHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Delete("/authorizations/{id}", opts.ManagementAuthorizationRevokeHandler.ServeHTTP)
			}
			if opts.ManagementMyAuthorizationRevokeHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Delete("/my-authorizations/{id}", opts.ManagementMyAuthorizationRevokeHandler.ServeHTTP)
			}
			if opts.ManagementProvidersHandler != nil {
				system.With(opts.ManagementAPIAuthMiddleware).Get("/providers", opts.ManagementProvidersHandler.ServeHTTP)
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
				system.With(managementAPIWriteAuthMiddleware).Put("/providers/{code}/default-test-model", opts.ManagementProviderDefaultTestModelHandler.ServeHTTP)
			}
			if opts.ManagementProviderCustomModelCreateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Post("/providers/{code}/models", opts.ManagementProviderCustomModelCreateHandler.ServeHTTP)
			}
			if opts.ManagementProviderCustomModelUpdateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Patch("/providers/{code}/models/{id}", opts.ManagementProviderCustomModelUpdateHandler.ServeHTTP)
			}
			if opts.ManagementProviderCustomModelDeleteHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Delete("/providers/{code}/models/{id}", opts.ManagementProviderCustomModelDeleteHandler.ServeHTTP)
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
				system.With(managementAPIWriteAuthMiddleware).Delete("/accounts/tags/{tagId}", opts.ManagementAccountTagDeleteHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountTagDeleteHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Delete("/my-accounts/tags/{tagId}", opts.ManagementMyAccountTagDeleteHandler.ServeHTTP)
			}
			if opts.ManagementAccountTagUpdateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Patch("/accounts/{id}/tags", opts.ManagementAccountTagUpdateHandler.ServeHTTP)
			}
			if opts.ManagementMyAccountTagUpdateHandler != nil {
				system.With(managementAPIWriteAuthMiddleware).Patch("/my-accounts/{id}/tags", opts.ManagementMyAccountTagUpdateHandler.ServeHTTP)
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
		opts.ManagementProviderDefaultTestModelHandler != nil ||
		opts.ManagementProviderCustomModelCreateHandler != nil ||
		opts.ManagementProviderCustomModelUpdateHandler != nil ||
		opts.ManagementProviderCustomModelDeleteHandler != nil ||
		opts.ManagementAccountTagDeleteHandler != nil ||
		opts.ManagementMyAccountTagDeleteHandler != nil ||
		opts.ManagementAccountTagUpdateHandler != nil ||
		opts.ManagementMyAccountTagUpdateHandler != nil
}
