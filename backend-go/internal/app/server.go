package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/accountpagedata"
	"juhe-ai/backend-go/internal/modules/announcements"
	"juhe-ai/backend-go/internal/modules/gatewaycache"
	"juhe-ai/backend-go/internal/modules/managementaccountauthorizeddispatch"
	"juhe-ai/backend-go/internal/modules/managementaccountbalance"
	"juhe-ai/backend-go/internal/modules/managementaccountbatchedit"
	"juhe-ai/backend-go/internal/modules/managementaccountcreate"
	"juhe-ai/backend-go/internal/modules/managementaccountdelete"
	"juhe-ai/backend-go/internal/modules/managementaccountdetails"
	"juhe-ai/backend-go/internal/modules/managementaccountexport"
	"juhe-ai/backend-go/internal/modules/managementaccountforceactivate"
	"juhe-ai/backend-go/internal/modules/managementaccountgroupbinding"
	"juhe-ai/backend-go/internal/modules/managementaccountimport"
	"juhe-ai/backend-go/internal/modules/managementaccountlist"
	"juhe-ai/backend-go/internal/modules/managementaccounts"
	"juhe-ai/backend-go/internal/modules/managementaccountstatussnapshot"
	"juhe-ai/backend-go/internal/modules/managementaccounttestdispatch"
	"juhe-ai/backend-go/internal/modules/managementaccounttestoptions"
	"juhe-ai/backend-go/internal/modules/managementaccounttestsession"
	"juhe-ai/backend-go/internal/modules/managementaccountteststatus"
	"juhe-ai/backend-go/internal/modules/managementaccounttrafficmigration"
	"juhe-ai/backend-go/internal/modules/managementaccountupdate"
	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauditlogs"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementauthorizationoptions"
	"juhe-ai/backend-go/internal/modules/managementauthorizations"
	"juhe-ai/backend-go/internal/modules/managementclientippolicies"
	"juhe-ai/backend-go/internal/modules/managementclientipstats"
	"juhe-ai/backend-go/internal/modules/managementexternalintegrationsources"
	"juhe-ai/backend-go/internal/modules/managementgroups"
	"juhe-ai/backend-go/internal/modules/managementoperationlogs"
	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/modules/managementproviders"
	"juhe-ai/backend-go/internal/modules/managementproxies"
	"juhe-ai/backend-go/internal/modules/managementpublicapilogs"
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	"juhe-ai/backend-go/internal/modules/managementruntimeloggrep"
	"juhe-ai/backend-go/internal/modules/managementruntimelogs"
	"juhe-ai/backend-go/internal/modules/managementsettings"
	"juhe-ai/backend-go/internal/modules/managementstats"
	"juhe-ai/backend-go/internal/modules/managementsystemaccounts"
	"juhe-ai/backend-go/internal/modules/managementsystemteams"
	"juhe-ai/backend-go/internal/modules/managementusagerecords"
	"juhe-ai/backend-go/internal/modules/publicaccounts"
	publicapicatalog "juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	publicapiratelimit "juhe-ai/backend-go/internal/modules/publicapi/ratelimit"
	"juhe-ai/backend-go/internal/modules/publicapikeys"
	"juhe-ai/backend-go/internal/modules/publicgroups"
	"juhe-ai/backend-go/internal/modules/publicroutestrategies"
	"juhe-ai/backend-go/internal/modules/publicsettings"
	"juhe-ai/backend-go/internal/ownerlock"
	"juhe-ai/backend-go/internal/platform/accounthealthcheckdispatch"
	"juhe-ai/backend-go/internal/platform/modelcatalogsnapshotrebuild"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/secretcrypto"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
	"juhe-ai/backend-go/internal/version"
)

const gooseSchemaVersionGateTimeout = 5 * time.Second

func RunServer(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	var runtimeOwnerLock *ownerlock.Lock
	if cfg.OwnerLockEnabled {
		if strings.TrimSpace(cfg.OwnerLockRole) != "server" {
			return fmt.Errorf("Go HTTP server owner lock role must be server")
		}
		lock, err := ownerlock.Acquire(cfg.OwnerLockPath, ownerlock.Metadata{
			DeploymentEpoch: cfg.OwnerLockDeploymentEpoch,
			RouteOwner:      cfg.OwnerLockRole,
			Version:         version.Version,
			PID:             os.Getpid(),
		})
		if err != nil {
			return err
		}
		runtimeOwnerLock = lock
		defer func() {
			if err := runtimeOwnerLock.Release(); err != nil {
				logger.Error("释放 Go owner lock 失败", slog.String("error", err.Error()))
			}
		}()
	}
	if cfg.PostgresURL == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL 不能为空")
	}
	if cfg.RedisStateURL == "" {
		return fmt.Errorf("JUHE_AI_REDIS_STATE_URL 不能为空")
	}
	if cfg.AuthCaptchaDisabled {
		logger.Warn("登录验证码已关闭：仅用于测试或临时排障，账号密码、登录限频、会话和权限校验仍然生效",
			slog.String("event", "auth_captcha_disabled"),
			slog.String("runtime_environment", strings.TrimSpace(cfg.Env)),
		)
	}

	store, err := postgresstore.Open(ctx, cfg.PostgresURL)
	if err != nil {
		return err
	}
	defer store.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := store.Ping(pingCtx); err != nil {
		cancel()
		return err
	}
	cancel()
	if cfg.OwnerLockEnabled {
		schemaCtx, cancel := context.WithTimeout(ctx, gooseSchemaVersionGateTimeout)
		err := store.RequireGooseSchemaVersion(schemaCtx, version.SchemaVersion)
		cancel()
		if err != nil {
			return fmt.Errorf("require goose schema version %d: %w", version.SchemaVersion, err)
		}
	}

	stateRedis, err := redisplatform.NewClient(cfg.RedisStateURL, cfg.RedisNamespace+":state")
	if err != nil {
		return err
	}
	defer func() { _ = stateRedis.Close() }()
	redisPingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := stateRedis.Ping(redisPingCtx); err != nil {
		cancel()
		return err
	}
	cancel()

	systemAccountInvalidator, cacheRedis, closeSystemAccountInvalidator, err := newGatewaySystemAccountInvalidator(ctx, cfg, stateRedis)
	if err != nil {
		return err
	}
	defer closeSystemAccountInvalidator()

	var systemAPIClientIPAllowlistVersionReader httpapi.SystemAPIClientIPAllowlistVersionReader
	var systemAPIRateLimitSettingsVersionReader httpapi.SystemAPIRateLimitSettingsVersionReader
	systemAPIClientIPAllowlistCacheRedis := cacheRedis
	if systemAPIClientIPAllowlistCacheRedis == nil && cfg.RedisCacheURL != "" {
		systemAPIClientIPAllowlistCacheRedis, err = redisplatform.NewClient(cfg.RedisCacheURL, cfg.RedisNamespace+":cache")
		if err != nil {
			return fmt.Errorf("JUHE_AI_REDIS_CACHE_URL 无效: %w", err)
		}
		defer func() { _ = systemAPIClientIPAllowlistCacheRedis.Close() }()
	}
	if systemAPIClientIPAllowlistCacheRedis != nil {
		systemAPIClientIPAllowlistVersionReader, err = httpapi.NewRedisSystemAPIClientIPAllowlistVersionReader(
			systemAPIClientIPAllowlistCacheRedis,
			cfg.RedisNamespace,
		)
		if err != nil {
			return err
		}
		systemAPIRateLimitSettingsVersionReader, err = httpapi.NewRedisSystemAPIRateLimitSettingsVersionReader(
			systemAPIClientIPAllowlistCacheRedis,
			cfg.RedisNamespace,
		)
		if err != nil {
			return err
		}
	}
	systemAPIRateLimitSettingsCache := httpapi.NewSystemAPIRateLimitSettingsCache(
		systemAPIRateLimitSettingsVersionReader,
	)

	var accountsStaticResetPublisher managementPageDataPublisher
	if cfg.ManagementAPIEnabled || cfg.PublicAPIEnabled {
		publisher, closePublisher, err := newRecoveringAccountsStaticResetPublisher(
			ctx, stateRedis, cacheRedis, cfg.RedisNamespace, store, logger,
		)
		if err != nil {
			return err
		}
		defer closePublisher()
		accountsStaticResetPublisher = publisher
	}
	publicAPIHandler, publicAPILogQueue, err := newPublicAPIHandlerWithOptions(
		cfg,
		logger,
		store,
		stateRedis,
		PublicAPIHandlerOptions{
			APIKeyInvalidator: systemAccountInvalidator,
			PageDataPublisher: accountsStaticResetPublisher,
		},
	)
	if err != nil {
		return err
	}
	if publicAPILogQueue != nil {
		defer func() { _ = publicAPILogQueue.Close() }()
	}
	managementOperationLogQueue, err := newManagementOperationLogQueue(cfg)
	if err != nil {
		return err
	}
	if managementOperationLogQueue != nil {
		defer func() { _ = managementOperationLogQueue.Close() }()
	}
	catalogSnapshotBridge, err := newManagementCatalogSnapshotRebuilder(cfg)
	if err != nil {
		return err
	}
	if err := probeManagementCatalogSnapshotBridge(ctx, cfg, catalogSnapshotBridge); err != nil {
		return err
	}

	publicSettingsService := publicsettings.NewService(store)
	var accountConcurrencyReader managementgroups.AccountConcurrencyReader
	if cfg.ManagementAPIEnabled {
		accountConcurrencyReader, err = redisplatform.NewAccountConcurrencyReader(stateRedis, cfg.RedisNamespace)
		if err != nil {
			return fmt.Errorf("初始化账号实时并发读取器失败: %w", err)
		}
	}
	managementHandlers := newManagementAPIHandlerWithCatalogSnapshotRebuilder(
		cfg,
		store,
		stateRedis,
		managementOperationLogQueue,
		logger,
		systemAccountInvalidator,
		accountsStaticResetPublisher,
		accountConcurrencyReader,
		systemAPIRateLimitSettingsCache,
		catalogSnapshotBridge,
	)
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                                            cfg,
		Logger:                                            logger,
		PublicSettingsService:                             &publicSettingsService,
		SystemAPIRateLimitReader:                          store,
		SystemAPIRateLimitSettingsCache:                   systemAPIRateLimitSettingsCache,
		SystemAPIRateLimitSettingsVersionReader:           systemAPIRateLimitSettingsVersionReader,
		SystemAPIClientIPAllowlistReader:                  store,
		SystemAPIClientIPAllowlistVersionReader:           systemAPIClientIPAllowlistVersionReader,
		SystemAPIIPRateLimiter:                            httpapi.NewRedisSystemAPIIPRateLimiter(stateRedis, cfg.RedisNamespace),
		SystemAPIAuthenticatedRateLimiter:                 httpapi.NewRedisSystemAPIAuthenticatedRateLimiter(stateRedis, cfg.RedisNamespace),
		PublicAPIHandler:                                  publicAPIHandler,
		NodeModelCatalogBridgeReadinessProber:             catalogSnapshotBridge,
		ManagementAPIAuthMiddleware:                       managementHandlers.AuthMiddleware,
		ManagementAPIAuthTouchMiddleware:                  managementHandlers.AuthTouchMiddleware,
		ManagementCaptchaHandler:                          managementHandlers.CaptchaHandler,
		ManagementLoginHandler:                            managementHandlers.LoginHandler,
		ManagementCurrentUserHandler:                      managementHandlers.CurrentUserHandler,
		ManagementProfileUpdateHandler:                    managementHandlers.ProfileUpdateHandler,
		ManagementPasswordChangeHandler:                   managementHandlers.PasswordChangeHandler,
		ManagementLogoutHandler:                           managementHandlers.LogoutHandler,
		ManagementProxiesHandler:                          managementHandlers.ProxiesHandler,
		ManagementProxyOptionsHandler:                     managementHandlers.ProxyOptionsHandler,
		ManagementProxyCreateHandler:                      managementHandlers.ProxyCreateHandler,
		ManagementProxyUpdateHandler:                      managementHandlers.ProxyUpdateHandler,
		ManagementProxyDeleteHandler:                      managementHandlers.ProxyDeleteHandler,
		ManagementProxyTestHandler:                        managementHandlers.ProxyTestHandler,
		ManagementSystemAccountsHandler:                   managementHandlers.SystemAccountsHandler,
		ManagementSystemAccountOptionsHandler:             managementHandlers.SystemAccountOptionsHandler,
		ManagementSystemAccountPatchHandler:               managementHandlers.SystemAccountPatchHandler,
		ManagementSystemAccountCreateHandler:              managementHandlers.SystemAccountCreateHandler,
		ManagementSystemTeamsHandler:                      managementHandlers.SystemTeamsHandler,
		ManagementMySystemTeamsHandler:                    managementHandlers.MySystemTeamsHandler,
		ManagementSystemTeamCreateHandler:                 managementHandlers.SystemTeamCreateHandler,
		ManagementSystemTeamPatchHandler:                  managementHandlers.SystemTeamPatchHandler,
		ManagementSystemTeamMembersAddHandler:             managementHandlers.SystemTeamMembersAddHandler,
		ManagementSystemTeamMemberDeleteHandler:           managementHandlers.SystemTeamMemberDeleteHandler,
		ManagementAuthorizationGranteeAccountsHandler:     managementHandlers.AuthorizationGranteeAccountsHandler,
		ManagementMyAuthorizationGranteeAccountsHandler:   managementHandlers.MyAuthorizationGranteeAccountsHandler,
		ManagementAuthorizationGranteeTeamsHandler:        managementHandlers.AuthorizationGranteeTeamsHandler,
		ManagementMyAuthorizationGranteeTeamsHandler:      managementHandlers.MyAuthorizationGranteeTeamsHandler,
		ManagementAuthorizationGranteeGroupsHandler:       managementHandlers.AuthorizationGranteeGroupsHandler,
		ManagementMyAuthorizationGranteeGroupsHandler:     managementHandlers.MyAuthorizationGranteeGroupsHandler,
		ManagementAuthorizationListHandler:                managementHandlers.AuthorizationListHandler,
		ManagementMyAuthorizationListHandler:              managementHandlers.MyAuthorizationListHandler,
		ManagementAuthorizationTeamUsageOverviewHandler:   managementHandlers.AuthorizationTeamUsageOverviewHandler,
		ManagementMyAuthorizationTeamUsageOverviewHandler: managementHandlers.MyAuthorizationTeamUsageOverviewHandler,
		ManagementAuthorizationUserUsageOverviewHandler:   managementHandlers.AuthorizationUserUsageOverviewHandler,
		ManagementMyAuthorizationUserUsageOverviewHandler: managementHandlers.MyAuthorizationUserUsageOverviewHandler,
		ManagementAuthorizationUsageHandler:               managementHandlers.AuthorizationUsageHandler,
		ManagementMyAuthorizationUsageHandler:             managementHandlers.MyAuthorizationUsageHandler,
		ManagementAuthorizationDetailHandler:              managementHandlers.AuthorizationDetailHandler,
		ManagementMyAuthorizationDetailHandler:            managementHandlers.MyAuthorizationDetailHandler,
		ManagementAuthorizationCreateHandler:              managementHandlers.AuthorizationCreateHandler,
		ManagementMyAuthorizationCreateHandler:            managementHandlers.MyAuthorizationCreateHandler,
		ManagementAuthorizationUpdateHandler:              managementHandlers.AuthorizationUpdateHandler,
		ManagementMyAuthorizationUpdateHandler:            managementHandlers.MyAuthorizationUpdateHandler,
		ManagementAuthorizationExpireUpdateHandler:        managementHandlers.AuthorizationExpireUpdateHandler,
		ManagementMyAuthorizationExpireUpdateHandler:      managementHandlers.MyAuthorizationExpireUpdateHandler,
		ManagementAuthorizationReturnHandler:              managementHandlers.AuthorizationReturnHandler,
		ManagementMyAuthorizationReturnHandler:            managementHandlers.MyAuthorizationReturnHandler,
		ManagementAccountAuthorizationReturnHandler:       managementHandlers.AccountAuthorizationReturnHandler,
		ManagementMyAccountAuthorizationReturnHandler:     managementHandlers.MyAccountAuthorizationReturnHandler,
		ManagementGroupAuthorizationReturnHandler:         managementHandlers.GroupAuthorizationReturnHandler,
		ManagementMyGroupAuthorizationReturnHandler:       managementHandlers.MyGroupAuthorizationReturnHandler,
		ManagementAuthorizationRevokeHandler:              managementHandlers.AuthorizationRevokeHandler,
		ManagementMyAuthorizationRevokeHandler:            managementHandlers.MyAuthorizationRevokeHandler,
		ManagementProvidersHandler:                        managementHandlers.ProvidersHandler,
		ManagementProviderOptionsHandler:                  managementHandlers.ProviderOptionsHandler,
		ManagementProviderDefinitionsHandler:              managementHandlers.ProviderDefinitionsHandler,
		ManagementProviderModelOptionsHandler:             managementHandlers.ProviderModelOptionsHandler,
		ManagementProviderModelsHandler:                   managementHandlers.ProviderModelsHandler,
		ManagementProviderModelCapabilitiesHandler:        managementHandlers.ProviderModelCapabilitiesHandler,
		ManagementProviderDefaultHealthCheckModelHandler:  managementHandlers.ProviderDefaultHealthCheckModelHandler,
		ManagementProviderCustomModelCreateHandler:        managementHandlers.ProviderCustomModelCreateHandler,
		ManagementProviderCustomModelUpdateHandler:        managementHandlers.ProviderCustomModelUpdateHandler,
		ManagementProviderCustomModelDeleteHandler:        managementHandlers.ProviderCustomModelDeleteHandler,
		ManagementRouteStrategyListHandler:                managementHandlers.RouteStrategyListHandler,
		ManagementMyRouteStrategyListHandler:              managementHandlers.MyRouteStrategyListHandler,
		ManagementRouteStrategyCreateHandler:              managementHandlers.RouteStrategyCreateHandler,
		ManagementMyRouteStrategyCreateHandler:            managementHandlers.MyRouteStrategyCreateHandler,
		ManagementRouteStrategyUpdateHandler:              managementHandlers.RouteStrategyUpdateHandler,
		ManagementMyRouteStrategyUpdateHandler:            managementHandlers.MyRouteStrategyUpdateHandler,
		ManagementRouteStrategyDeleteHandler:              managementHandlers.RouteStrategyDeleteHandler,
		ManagementMyRouteStrategyDeleteHandler:            managementHandlers.MyRouteStrategyDeleteHandler,
		ManagementRouteStrategyDetailHandler:              managementHandlers.RouteStrategyDetailHandler,
		ManagementMyRouteStrategyDetailHandler:            managementHandlers.MyRouteStrategyDetailHandler,
		ManagementRouteStrategyOptionsHandler:             managementHandlers.RouteStrategyOptionsHandler,
		ManagementMyRouteStrategyOptionsHandler:           managementHandlers.MyRouteStrategyOptionsHandler,
		ManagementAPIKeyListHandler:                       managementHandlers.APIKeyListHandler,
		ManagementMyAPIKeyListHandler:                     managementHandlers.MyAPIKeyListHandler,
		ManagementAPIKeySecretHandler:                     managementHandlers.APIKeySecretHandler,
		ManagementMyAPIKeySecretHandler:                   managementHandlers.MyAPIKeySecretHandler,
		ManagementAPIKeyRefreshHandler:                    managementHandlers.APIKeyRefreshHandler,
		ManagementMyAPIKeyRefreshHandler:                  managementHandlers.MyAPIKeyRefreshHandler,
		ManagementAPIKeyCreateHandler:                     managementHandlers.APIKeyCreateHandler,
		ManagementMyAPIKeyCreateHandler:                   managementHandlers.MyAPIKeyCreateHandler,
		ManagementAPIKeyUpdateHandler:                     managementHandlers.APIKeyUpdateHandler,
		ManagementMyAPIKeyUpdateHandler:                   managementHandlers.MyAPIKeyUpdateHandler,
		ManagementAPIKeyDeleteHandler:                     managementHandlers.APIKeyDeleteHandler,
		ManagementMyAPIKeyDeleteHandler:                   managementHandlers.MyAPIKeyDeleteHandler,
		ManagementGroupListHandler:                        managementHandlers.GroupListHandler,
		ManagementMyGroupListHandler:                      managementHandlers.MyGroupListHandler,
		ManagementGroupDetailHandler:                      managementHandlers.GroupDetailHandler,
		ManagementMyGroupDetailHandler:                    managementHandlers.MyGroupDetailHandler,
		ManagementGroupCreateHandler:                      managementHandlers.GroupCreateHandler,
		ManagementMyGroupCreateHandler:                    managementHandlers.MyGroupCreateHandler,
		ManagementGroupUpdateHandler:                      managementHandlers.GroupUpdateHandler,
		ManagementMyGroupUpdateHandler:                    managementHandlers.MyGroupUpdateHandler,
		ManagementGroupDeleteHandler:                      managementHandlers.GroupDeleteHandler,
		ManagementMyGroupDeleteHandler:                    managementHandlers.MyGroupDeleteHandler,
		ManagementGroupOptionsHandler:                     managementHandlers.GroupOptionsHandler,
		ManagementMyGroupOptionsHandler:                   managementHandlers.MyGroupOptionsHandler,
		ManagementGroupAccountOptionsHandler:              managementHandlers.GroupAccountOptionsHandler,
		ManagementMyGroupAccountOptionsHandler:            managementHandlers.MyGroupAccountOptionsHandler,
		ManagementAccountOptionsHandler:                   managementHandlers.AccountOptionsHandler,
		ManagementMyAccountOptionsHandler:                 managementHandlers.MyAccountOptionsHandler,
		ManagementAccountTestOptionsHandler:               managementHandlers.AccountTestOptionsHandler,
		ManagementMyAccountTestOptionsHandler:             managementHandlers.MyAccountTestOptionsHandler,
		ManagementAccountTagsHandler:                      managementHandlers.AccountTagsHandler,
		ManagementMyAccountTagsHandler:                    managementHandlers.MyAccountTagsHandler,
		ManagementAccountTagDeleteHandler:                 managementHandlers.AccountTagDeleteHandler,
		ManagementMyAccountTagDeleteHandler:               managementHandlers.MyAccountTagDeleteHandler,
		ManagementAccountTagUpdateHandler:                 managementHandlers.AccountTagUpdateHandler,
		ManagementMyAccountTagUpdateHandler:               managementHandlers.MyAccountTagUpdateHandler,
		ManagementAccountDetailHandler:                    managementHandlers.AccountDetailHandler,
		ManagementMyAccountDetailHandler:                  managementHandlers.MyAccountDetailHandler,
		ManagementAccountEditBasicDetailHandler:           managementHandlers.AccountEditBasicDetailHandler,
		ManagementMyAccountEditBasicDetailHandler:         managementHandlers.MyAccountEditBasicDetailHandler,
		ManagementAccountAdvancedDetailHandler:            managementHandlers.AccountAdvancedDetailHandler,
		ManagementMyAccountAdvancedDetailHandler:          managementHandlers.MyAccountAdvancedDetailHandler,
		ManagementAccountAPIKeyRuntimeHandler:             managementHandlers.AccountAPIKeyRuntimeHandler,
		ManagementMyAccountAPIKeyRuntimeHandler:           managementHandlers.MyAccountAPIKeyRuntimeHandler,
		ManagementAccountGroupBindingHandler:              managementHandlers.AccountGroupBindingHandler,
		ManagementMyAccountGroupBindingHandler:            managementHandlers.MyAccountGroupBindingHandler,
		ManagementAccountBatchEditHandler:                 managementHandlers.AccountBatchEditHandler,
		ManagementMyAccountBatchEditHandler:               managementHandlers.MyAccountBatchEditHandler,
		ManagementAccountForceActivateHandler:             managementHandlers.AccountForceActivateHandler,
		ManagementMyAccountForceActivateHandler:           managementHandlers.MyAccountForceActivateHandler,
		ManagementAccountDeleteHandler:                    managementHandlers.AccountDeleteHandler,
		ManagementMyAccountDeleteHandler:                  managementHandlers.MyAccountDeleteHandler,
		ManagementAccountBalanceHandler:                   managementHandlers.AccountBalanceHandler,
		ManagementMyAccountBalanceHandler:                 managementHandlers.MyAccountBalanceHandler,
		ManagementAccountBalanceRefreshHandler:            managementHandlers.AccountBalanceRefreshHandler,
		ManagementMyAccountBalanceRefreshHandler:          managementHandlers.MyAccountBalanceRefreshHandler,
		ManagementAccountStatusSnapshotHandler:            managementHandlers.AccountStatusSnapshotHandler,
		ManagementMyAccountStatusSnapshotHandler:          managementHandlers.MyAccountStatusSnapshotHandler,
		ManagementAccountListHandler:                      managementHandlers.AccountListHandler,
		ManagementMyAccountListHandler:                    managementHandlers.MyAccountListHandler,
		ManagementAccountExportHandler:                    managementHandlers.AccountExportHandler,
		ManagementMyAccountExportHandler:                  managementHandlers.MyAccountExportHandler,
		ManagementAccountCreateHandler:                    managementHandlers.AccountCreateHandler,
		ManagementMyAccountCreateHandler:                  managementHandlers.MyAccountCreateHandler,
		ManagementAccountUpdateHandler:                    managementHandlers.AccountUpdateHandler,
		ManagementMyAccountUpdateHandler:                  managementHandlers.MyAccountUpdateHandler,
		ManagementAccountAuthorizedDispatchHandler:        managementHandlers.AccountAuthorizedDispatchHandler,
		ManagementMyAccountAuthorizedDispatchHandler:      managementHandlers.MyAccountAuthorizedDispatchHandler,
		ManagementAccountImportPreviewHandler:             managementHandlers.AccountImportPreviewHandler,
		ManagementMyAccountImportPreviewHandler:           managementHandlers.MyAccountImportPreviewHandler,
		ManagementAccountImportConfirmHandler:             managementHandlers.AccountImportConfirmHandler,
		ManagementMyAccountImportConfirmHandler:           managementHandlers.MyAccountImportConfirmHandler,
		ManagementAccountTrafficMigrationHandler:          managementHandlers.AccountTrafficMigrationHandler,
		ManagementMyAccountTrafficMigrationHandler:        managementHandlers.MyAccountTrafficMigrationHandler,
		ManagementAccountTestSessionCreateHandler:         managementHandlers.AccountTestSessionCreateHandler,
		ManagementMyAccountTestSessionCreateHandler:       managementHandlers.MyAccountTestSessionCreateHandler,
		ManagementAccountTestSessionHeartbeatHandler:      managementHandlers.AccountTestSessionHeartbeatHandler,
		ManagementMyAccountTestSessionHeartbeatHandler:    managementHandlers.MyAccountTestSessionHeartbeatHandler,
		ManagementAccountTestSessionCompleteHandler:       managementHandlers.AccountTestSessionCompleteHandler,
		ManagementMyAccountTestSessionCompleteHandler:     managementHandlers.MyAccountTestSessionCompleteHandler,
		ManagementAccountTestSessionCancelHandler:         managementHandlers.AccountTestSessionCancelHandler,
		ManagementMyAccountTestSessionCancelHandler:       managementHandlers.MyAccountTestSessionCancelHandler,
		ManagementAccountTestTaskCancelHandler:            managementHandlers.AccountTestTaskCancelHandler,
		ManagementMyAccountTestTaskCancelHandler:          managementHandlers.MyAccountTestTaskCancelHandler,
		ManagementAccountTestTaskListHandler:              managementHandlers.AccountTestTaskListHandler,
		ManagementMyAccountTestTaskListHandler:            managementHandlers.MyAccountTestTaskListHandler,
		ManagementAccountTestSessionStatusHandler:         managementHandlers.AccountTestSessionStatusHandler,
		ManagementMyAccountTestSessionStatusHandler:       managementHandlers.MyAccountTestSessionStatusHandler,
		ManagementAccountTestSessionTasksHandler:          managementHandlers.AccountTestSessionTasksHandler,
		ManagementMyAccountTestSessionTasksHandler:        managementHandlers.MyAccountTestSessionTasksHandler,
		ManagementAccountTestTaskStatusHandler:            managementHandlers.AccountTestTaskStatusHandler,
		ManagementMyAccountTestTaskStatusHandler:          managementHandlers.MyAccountTestTaskStatusHandler,
		ManagementAccountTestDispatchHandler:              managementHandlers.AccountTestDispatchHandler,
		ManagementMyAccountTestDispatchHandler:            managementHandlers.MyAccountTestDispatchHandler,
		ManagementSystemSettingsHandler:                   managementHandlers.SystemSettingsHandler,
		ManagementSystemSettingsUpdateHandler:             managementHandlers.SystemSettingsUpdateHandler,
		ManagementGlobalSettingsHandler:                   managementHandlers.GlobalSettingsHandler,
		ManagementGlobalSettingsUpdateHandler:             managementHandlers.GlobalSettingsUpdateHandler,
		ManagementClientIPStatsHandler:                    managementHandlers.ClientIPStatsHandler,
		ManagementClientIPStatsDetailHandler:              managementHandlers.ClientIPStatsDetailHandler,
		ManagementClientIPAllowlistHandler:                managementHandlers.ClientIPAllowlistHandler,
		ManagementClientIPUnallowlistHandler:              managementHandlers.ClientIPUnallowlistHandler,
		ManagementClientIPBlacklistHandler:                managementHandlers.ClientIPBlacklistHandler,
		ManagementClientIPUnblockHandler:                  managementHandlers.ClientIPUnblockHandler,
		ManagementOperationLogsHandler:                    managementHandlers.OperationLogsHandler,
		ManagementMyOperationLogsHandler:                  managementHandlers.MyOperationLogsHandler,
		ManagementAuditLogsHandler:                        managementHandlers.AuditLogsHandler,
		ManagementAuditErrorGroupsHandler:                 managementHandlers.AuditErrorGroupsHandler,
		ManagementAuditErrorGroupEventsHandler:            managementHandlers.AuditErrorGroupEventsHandler,
		ManagementRuntimeLogsHandler:                      managementHandlers.RuntimeLogsHandler,
		ManagementRuntimeLogGrepHandler:                   managementHandlers.RuntimeLogGrepHandler,
		ManagementExternalIntegrationSourceListHandler:    managementHandlers.ExternalIntegrationSourceListHandler,
		ManagementExternalIntegrationSourceDetailHandler:  managementHandlers.ExternalIntegrationSourceDetailHandler,
		ManagementExternalIntegrationSourceCreateHandler:  managementHandlers.ExternalIntegrationSourceCreateHandler,
		ManagementExternalIntegrationSourceUpdateHandler:  managementHandlers.ExternalIntegrationSourceUpdateHandler,
		ManagementExternalIntegrationSourceDeleteHandler:  managementHandlers.ExternalIntegrationSourceDeleteHandler,
		ManagementExternalSourceBuiltInResetHandler:       managementHandlers.ExternalSourceBuiltInResetHandler,
		ManagementExternalSourceTokenCreateHandler:        managementHandlers.ExternalSourceTokenCreateHandler,
		ManagementExternalSourceTokenUpdateHandler:        managementHandlers.ExternalSourceTokenUpdateHandler,
		ManagementExternalSourceTokenSecretHandler:        managementHandlers.ExternalSourceTokenSecretHandler,
		ManagementExternalIntegrationSourceScopesHandler:  managementHandlers.ExternalIntegrationSourceScopesHandler,
		ManagementExternalIntegrationSourceAPIDocsHandler: managementHandlers.ExternalIntegrationSourceAPIDocsHandler,
		ManagementPublicAPILogsHandler:                    managementHandlers.PublicAPILogsHandler,
		ManagementUsageRecordsHandler:                     managementHandlers.UsageRecordsHandler,
		ManagementMyUsageRecordsHandler:                   managementHandlers.MyUsageRecordsHandler,
		ManagementAnnouncementPublicListHandler:           managementHandlers.AnnouncementPublicListHandler,
		ManagementAnnouncementPublicDetailHandler:         managementHandlers.AnnouncementPublicDetailHandler,
		ManagementAnnouncementPublicReadHandler:           managementHandlers.AnnouncementPublicReadHandler,
		ManagementAnnouncementsHandler:                    managementHandlers.AnnouncementsHandler,
		ManagementSystemMetricsHandler:                    managementHandlers.SystemMetricsHandler,
		ManagementPageDataConfirmHandler:                  managementHandlers.PageDataConfirmHandler,
		ManagementStatsUsageWindowHandler:                 managementHandlers.StatsUsageWindowHandler,
		ManagementMyStatsUsageWindowHandler:               managementHandlers.MyStatsUsageWindowHandler,
	})

	server := &http.Server{
		Addr:              cfg.Address(),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("Go 后端 HTTP 服务启动", slog.String("address", cfg.Address()))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}

type managementAPIHandlers struct {
	AuthMiddleware                          func(http.Handler) http.Handler
	AuthTouchMiddleware                     func(http.Handler) http.Handler
	CaptchaHandler                          http.Handler
	LoginHandler                            http.Handler
	CurrentUserHandler                      http.Handler
	ProfileUpdateHandler                    http.Handler
	PasswordChangeHandler                   http.Handler
	LogoutHandler                           http.Handler
	ProxiesHandler                          http.Handler
	ProxyOptionsHandler                     http.Handler
	ProxyCreateHandler                      http.Handler
	ProxyUpdateHandler                      http.Handler
	ProxyDeleteHandler                      http.Handler
	ProxyTestHandler                        http.Handler
	SystemAccountsHandler                   http.Handler
	SystemAccountOptionsHandler             http.Handler
	SystemAccountPatchHandler               http.Handler
	SystemAccountCreateHandler              http.Handler
	SystemTeamsHandler                      http.Handler
	MySystemTeamsHandler                    http.Handler
	SystemTeamCreateHandler                 http.Handler
	SystemTeamPatchHandler                  http.Handler
	SystemTeamMembersAddHandler             http.Handler
	SystemTeamMemberDeleteHandler           http.Handler
	AuthorizationGranteeAccountsHandler     http.Handler
	MyAuthorizationGranteeAccountsHandler   http.Handler
	AuthorizationGranteeTeamsHandler        http.Handler
	MyAuthorizationGranteeTeamsHandler      http.Handler
	AuthorizationGranteeGroupsHandler       http.Handler
	MyAuthorizationGranteeGroupsHandler     http.Handler
	AuthorizationListHandler                http.Handler
	MyAuthorizationListHandler              http.Handler
	AuthorizationTeamUsageOverviewHandler   http.Handler
	MyAuthorizationTeamUsageOverviewHandler http.Handler
	AuthorizationUserUsageOverviewHandler   http.Handler
	MyAuthorizationUserUsageOverviewHandler http.Handler
	AuthorizationUsageHandler               http.Handler
	MyAuthorizationUsageHandler             http.Handler
	AuthorizationDetailHandler              http.Handler
	MyAuthorizationDetailHandler            http.Handler
	AuthorizationCreateHandler              http.Handler
	MyAuthorizationCreateHandler            http.Handler
	AuthorizationUpdateHandler              http.Handler
	MyAuthorizationUpdateHandler            http.Handler
	AuthorizationExpireUpdateHandler        http.Handler
	MyAuthorizationExpireUpdateHandler      http.Handler
	AuthorizationReturnHandler              http.Handler
	MyAuthorizationReturnHandler            http.Handler
	AccountAuthorizationReturnHandler       http.Handler
	MyAccountAuthorizationReturnHandler     http.Handler
	GroupAuthorizationReturnHandler         http.Handler
	MyGroupAuthorizationReturnHandler       http.Handler
	AuthorizationRevokeHandler              http.Handler
	MyAuthorizationRevokeHandler            http.Handler
	ProvidersHandler                        http.Handler
	ProviderOptionsHandler                  http.Handler
	ProviderDefinitionsHandler              http.Handler
	ProviderModelOptionsHandler             http.Handler
	ProviderModelsHandler                   http.Handler
	ProviderModelCapabilitiesHandler        http.Handler
	ProviderDefaultHealthCheckModelHandler  http.Handler
	ProviderCustomModelCreateHandler        http.Handler
	ProviderCustomModelUpdateHandler        http.Handler
	ProviderCustomModelDeleteHandler        http.Handler
	RouteStrategyListHandler                http.Handler
	MyRouteStrategyListHandler              http.Handler
	RouteStrategyCreateHandler              http.Handler
	MyRouteStrategyCreateHandler            http.Handler
	RouteStrategyUpdateHandler              http.Handler
	MyRouteStrategyUpdateHandler            http.Handler
	RouteStrategyDeleteHandler              http.Handler
	MyRouteStrategyDeleteHandler            http.Handler
	RouteStrategyDetailHandler              http.Handler
	MyRouteStrategyDetailHandler            http.Handler
	RouteStrategyOptionsHandler             http.Handler
	MyRouteStrategyOptionsHandler           http.Handler
	APIKeyListHandler                       http.Handler
	MyAPIKeyListHandler                     http.Handler
	APIKeySecretHandler                     http.Handler
	MyAPIKeySecretHandler                   http.Handler
	APIKeyRefreshHandler                    http.Handler
	MyAPIKeyRefreshHandler                  http.Handler
	APIKeyCreateHandler                     http.Handler
	MyAPIKeyCreateHandler                   http.Handler
	APIKeyUpdateHandler                     http.Handler
	MyAPIKeyUpdateHandler                   http.Handler
	APIKeyDeleteHandler                     http.Handler
	MyAPIKeyDeleteHandler                   http.Handler
	GroupListHandler                        http.Handler
	MyGroupListHandler                      http.Handler
	GroupDetailHandler                      http.Handler
	MyGroupDetailHandler                    http.Handler
	GroupCreateHandler                      http.Handler
	MyGroupCreateHandler                    http.Handler
	GroupUpdateHandler                      http.Handler
	MyGroupUpdateHandler                    http.Handler
	GroupDeleteHandler                      http.Handler
	MyGroupDeleteHandler                    http.Handler
	GroupOptionsHandler                     http.Handler
	MyGroupOptionsHandler                   http.Handler
	GroupAccountOptionsHandler              http.Handler
	MyGroupAccountOptionsHandler            http.Handler
	AccountOptionsHandler                   http.Handler
	MyAccountOptionsHandler                 http.Handler
	AccountTestOptionsHandler               http.Handler
	MyAccountTestOptionsHandler             http.Handler
	AccountTagsHandler                      http.Handler
	MyAccountTagsHandler                    http.Handler
	AccountTagDeleteHandler                 http.Handler
	MyAccountTagDeleteHandler               http.Handler
	AccountTagUpdateHandler                 http.Handler
	MyAccountTagUpdateHandler               http.Handler
	AccountDetailHandler                    http.Handler
	MyAccountDetailHandler                  http.Handler
	AccountEditBasicDetailHandler           http.Handler
	MyAccountEditBasicDetailHandler         http.Handler
	AccountAdvancedDetailHandler            http.Handler
	MyAccountAdvancedDetailHandler          http.Handler
	AccountAPIKeyRuntimeHandler             http.Handler
	MyAccountAPIKeyRuntimeHandler           http.Handler
	AccountGroupBindingHandler              http.Handler
	MyAccountGroupBindingHandler            http.Handler
	AccountBatchEditHandler                 http.Handler
	MyAccountBatchEditHandler               http.Handler
	AccountForceActivateHandler             http.Handler
	MyAccountForceActivateHandler           http.Handler
	AccountDeleteHandler                    http.Handler
	MyAccountDeleteHandler                  http.Handler
	AccountBalanceHandler                   http.Handler
	MyAccountBalanceHandler                 http.Handler
	AccountBalanceRefreshHandler            http.Handler
	MyAccountBalanceRefreshHandler          http.Handler
	AccountStatusSnapshotHandler            http.Handler
	MyAccountStatusSnapshotHandler          http.Handler
	AccountListHandler                      http.Handler
	MyAccountListHandler                    http.Handler
	AccountExportHandler                    http.Handler
	MyAccountExportHandler                  http.Handler
	AccountCreateHandler                    http.Handler
	MyAccountCreateHandler                  http.Handler
	AccountUpdateHandler                    http.Handler
	MyAccountUpdateHandler                  http.Handler
	AccountAuthorizedDispatchHandler        http.Handler
	MyAccountAuthorizedDispatchHandler      http.Handler
	AccountImportPreviewHandler             http.Handler
	MyAccountImportPreviewHandler           http.Handler
	AccountImportConfirmHandler             http.Handler
	MyAccountImportConfirmHandler           http.Handler
	AccountTrafficMigrationHandler          http.Handler
	MyAccountTrafficMigrationHandler        http.Handler
	AccountTestSessionCreateHandler         http.Handler
	MyAccountTestSessionCreateHandler       http.Handler
	AccountTestSessionHeartbeatHandler      http.Handler
	MyAccountTestSessionHeartbeatHandler    http.Handler
	AccountTestSessionCompleteHandler       http.Handler
	MyAccountTestSessionCompleteHandler     http.Handler
	AccountTestSessionCancelHandler         http.Handler
	MyAccountTestSessionCancelHandler       http.Handler
	AccountTestTaskCancelHandler            http.Handler
	MyAccountTestTaskCancelHandler          http.Handler
	AccountTestTaskListHandler              http.Handler
	MyAccountTestTaskListHandler            http.Handler
	AccountTestSessionStatusHandler         http.Handler
	MyAccountTestSessionStatusHandler       http.Handler
	AccountTestSessionTasksHandler          http.Handler
	MyAccountTestSessionTasksHandler        http.Handler
	AccountTestTaskStatusHandler            http.Handler
	MyAccountTestTaskStatusHandler          http.Handler
	AccountTestDispatchHandler              http.Handler
	MyAccountTestDispatchHandler            http.Handler
	SystemSettingsHandler                   http.Handler
	SystemSettingsUpdateHandler             http.Handler
	GlobalSettingsHandler                   http.Handler
	GlobalSettingsUpdateHandler             http.Handler
	ClientIPStatsHandler                    http.Handler
	ClientIPStatsDetailHandler              http.Handler
	ClientIPAllowlistHandler                http.Handler
	ClientIPUnallowlistHandler              http.Handler
	ClientIPBlacklistHandler                http.Handler
	ClientIPUnblockHandler                  http.Handler
	OperationLogsHandler                    http.Handler
	MyOperationLogsHandler                  http.Handler
	AuditLogsHandler                        http.Handler
	AuditErrorGroupsHandler                 http.Handler
	AuditErrorGroupEventsHandler            http.Handler
	RuntimeLogsHandler                      http.Handler
	RuntimeLogGrepHandler                   http.Handler
	ExternalIntegrationSourceListHandler    http.Handler
	ExternalIntegrationSourceDetailHandler  http.Handler
	ExternalIntegrationSourceCreateHandler  http.Handler
	ExternalIntegrationSourceUpdateHandler  http.Handler
	ExternalIntegrationSourceDeleteHandler  http.Handler
	ExternalSourceBuiltInResetHandler       http.Handler
	ExternalSourceTokenCreateHandler        http.Handler
	ExternalSourceTokenUpdateHandler        http.Handler
	ExternalSourceTokenSecretHandler        http.Handler
	ExternalIntegrationSourceScopesHandler  http.Handler
	ExternalIntegrationSourceAPIDocsHandler http.Handler
	PublicAPILogsHandler                    http.Handler
	UsageRecordsHandler                     http.Handler
	MyUsageRecordsHandler                   http.Handler
	AnnouncementPublicListHandler           http.Handler
	AnnouncementPublicDetailHandler         http.Handler
	AnnouncementPublicReadHandler           http.Handler
	AnnouncementsHandler                    http.Handler
	SystemMetricsHandler                    http.Handler
	PageDataConfirmHandler                  http.Handler
	StatsUsageWindowHandler                 http.Handler
	MyStatsUsageWindowHandler               http.Handler
}

type managementAPIInvalidator interface {
	managementsystemaccounts.SystemAccountInvalidator
	managementsystemteams.AuthorizationInvalidator
	managementauthorizations.AuthorizationInvalidator
	managementprovidermodels.CustomProviderModelInvalidator
	managementproxies.ProxyInvalidator
	managementapikeys.APIKeyGatewayCacheInvalidator
	managementsettings.GlobalSettingsCacheInvalidator
	managementsettings.SystemSettingsInvalidator
	managementclientippolicies.ClientIPPolicyCacheInvalidator
}

type gatewayCacheInvalidator interface {
	managementAPIInvalidator
	publicapikeys.APIKeyGatewayCacheInvalidator
}

func newManagementAPIHandlerWithPageData(
	cfg config.Config,
	store *postgresstore.Store,
	stateRedis *redisplatform.Client,
	operationLogQueue operationLogEnqueueClient,
	logger *slog.Logger,
	systemAccountInvalidator managementAPIInvalidator,
	accountsStaticResetPublisher managementPageDataPublisher,
	accountConcurrencyReader managementgroups.AccountConcurrencyReader,
	systemAPIRateLimitSettingsCache managementsettings.SystemAPIRateLimitSettingsCacheInvalidator,
) managementAPIHandlers {
	return newManagementAPIHandlerWithCatalogSnapshotRebuilder(
		cfg,
		store,
		stateRedis,
		operationLogQueue,
		logger,
		systemAccountInvalidator,
		accountsStaticResetPublisher,
		accountConcurrencyReader,
		systemAPIRateLimitSettingsCache,
		nil,
	)
}

func newManagementAPIHandlerWithCatalogSnapshotRebuilder(
	cfg config.Config,
	store *postgresstore.Store,
	stateRedis *redisplatform.Client,
	operationLogQueue operationLogEnqueueClient,
	logger *slog.Logger,
	systemAccountInvalidator managementAPIInvalidator,
	accountsStaticResetPublisher managementPageDataPublisher,
	accountConcurrencyReader managementgroups.AccountConcurrencyReader,
	systemAPIRateLimitSettingsCache managementsettings.SystemAPIRateLimitSettingsCacheInvalidator,
	catalogSnapshotRebuilder managementprovidermodels.CatalogSnapshotRebuilder,
) managementAPIHandlers {
	if !cfg.ManagementAPIEnabled {
		return managementAPIHandlers{}
	}
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{Store: store})
	captchaService := managementauth.NewCaptchaService(stateRedis)
	loginGuardService := managementauth.NewLoginGuardService(stateRedis)
	loginService := managementauth.NewLoginServiceWithOptions(managementauth.LoginServiceOptions{
		Store: store, Captcha: captchaService, Guard: loginGuardService, CaptchaDisabled: cfg.AuthCaptchaDisabled,
	})
	profileService := managementauth.NewProfileService(store)
	passwordService := managementauth.NewPasswordService(store)
	proxyService := managementproxies.NewServiceWithOptions(managementproxies.ServiceOptions{
		Store:       store,
		Secret:      cfg.Secret,
		Invalidator: systemAccountInvalidator,
	})
	providerService := managementproviders.NewService(store)
	providerModelService := managementprovidermodels.NewServiceWithOptions(managementprovidermodels.ServiceOptions{
		Store:             store,
		Invalidator:       systemAccountInvalidator,
		PageDataPublisher: accountsStaticResetPublisher,
		CatalogRebuilder:  catalogSnapshotRebuilder,
		Logger:            logger,
	})
	routeStrategyService := managementroutestrategies.NewServiceWithOptions(
		managementroutestrategies.ServiceOptions{
			OptionReader:      store,
			ListReader:        store,
			DetailReader:      store,
			CreateStore:       store,
			Transactor:        store,
			Invalidator:       systemAccountInvalidator,
			PageDataPublisher: accountsStaticResetPublisher,
			Logger:            logger,
		},
	)
	apiKeyService := managementapikeys.NewServiceWithOptions(managementapikeys.ServiceOptions{
		ListReader:               store,
		Creator:                  store,
		Updater:                  store,
		Deleter:                  store,
		UsageStatsTimezoneReader: store,
		SecretStore:              store,
		SecretTransactor:         store,
		Invalidator:              systemAccountInvalidator,
		Logger:                   logger,
		Secret:                   cfg.Secret,
	})
	groupService := managementgroups.NewServiceWithOptions(managementgroups.ServiceOptions{
		Store:                   store,
		ListStore:               store,
		DetailStore:             store,
		UsageStatsTimezoneStore: store,
		AccountConcurrency:      accountConcurrencyReader,
		Invalidator:             systemAccountInvalidator,
		PageDataPublisher:       accountsStaticResetPublisher,
		Logger:                  logger,
	})
	accountService := managementaccounts.NewServiceWithOptions(managementaccounts.ServiceOptions{
		Store:             store,
		GranteeReader:     store,
		PageDataPublisher: accountsStaticResetPublisher,
		Logger:            logger,
	})
	accountDetailService := managementaccountdetails.NewService(managementaccountdetails.ServiceOptions{
		Reader:            store,
		CredentialCodec:   secretcrypto.NewJSONCodec(cfg.Secret),
		FingerprintSecret: cfg.Secret,
	})
	accountBatchEditService := managementaccountbatchedit.NewService(store, store)
	accountBalanceService := managementaccountbalance.NewService(managementaccountbalance.ServiceOptions{Reader: store, Writer: store})
	accountStatusSnapshotService := managementaccountstatussnapshot.NewServiceWithOptions(managementaccountstatussnapshot.ServiceOptions{
		Reader:             store,
		AccountConcurrency: accountConcurrencyReader,
		APIKeyRuntime:      store,
		APIKeySources:      store,
		CredentialCodec:    secretcrypto.NewJSONCodec(cfg.Secret),
		FingerprintSecret:  cfg.Secret,
		UsageStatsTimezone: store,
	})
	accountListService := managementaccountlist.NewService(store)
	accountExportService := managementaccountexport.NewService(managementaccountexport.ServiceOptions{
		Reader:          store,
		CredentialCodec: secretcrypto.NewJSONCodec(cfg.Secret),
	})
	groupAccountIDsInvalidator, _ := systemAccountInvalidator.(managementaccountgroupbinding.GroupAccountIDsInvalidator)
	accountCreateService := managementaccountcreate.NewService(managementaccountcreate.Options{
		Store: store, CredentialCodec: secretcrypto.NewJSONCodec(cfg.Secret), GranteeReader: store,
		PageDataPublisher: accountsStaticResetPublisher, GroupAccountIDsInvalidator: groupAccountIDsInvalidator,
		GatewayRuntimeInvalidator: systemAccountInvalidator, Logger: logger,
	})
	accountUpdateService := managementaccountupdate.NewService(managementaccountupdate.Options{
		Store: store, CredentialCodec: secretcrypto.NewJSONCodec(cfg.Secret), GranteeReader: store,
		PageDataPublisher: accountsStaticResetPublisher, GroupAccountIDsInvalidator: groupAccountIDsInvalidator,
		GatewayRuntimeInvalidator: systemAccountInvalidator, Logger: logger,
	})
	accountAuthorizedDispatchService := managementaccountauthorizeddispatch.NewService(managementaccountauthorizeddispatch.Options{
		Store: store, GatewayInvalidator: systemAccountInvalidator,
		PageDataPublisher: accountsStaticResetPublisher, Logger: logger,
	})
	accountImportService := managementaccountimport.NewService(managementaccountimport.Options{
		Store: store, CredentialCodec: secretcrypto.NewJSONCodec(cfg.Secret),
	})
	accountTrafficMigrationService := managementaccounttrafficmigration.NewService(managementaccounttrafficmigration.Options{
		Store: store, GatewayInvalidator: systemAccountInvalidator, GranteeReader: store,
		PageDataPublisher: accountsStaticResetPublisher, Logger: logger,
	})
	accountTestOptionsService := managementaccounttestoptions.NewServiceWithOptions(managementaccounttestoptions.ServiceOptions{
		Reader:          store,
		OptionReader:    store,
		ModelCatalog:    providerModelService,
		CredentialCodec: secretcrypto.NewJSONCodec(cfg.Secret),
	})
	accountTestSessionService := managementaccounttestsession.NewService(store, nil)
	accountTestStatusService := managementaccountteststatus.NewService(store)
	accountTestDispatchService := managementaccounttestdispatch.NewService(managementaccounttestdispatch.Options{
		Store:         store,
		EnqueueClient: operationLogQueue,
		Codec:         secretcrypto.NewJSONCodec(cfg.Secret),
		TestOptions:   accountTestOptionsService,
	})
	accountForceActivateService := managementaccountforceactivate.NewService(managementaccountforceactivate.ServiceOptions{
		Store:              store,
		Details:            accountDetailService,
		GranteeReader:      store,
		PageDataPublisher:  accountsStaticResetPublisher,
		GatewayInvalidator: systemAccountInvalidator,
		Logger:             logger,
	})
	accountDeleteService := managementaccountdelete.NewService(managementaccountdelete.Options{
		Store:                      store,
		PageDataPublisher:          accountsStaticResetPublisher,
		GroupAccountIDsInvalidator: groupAccountIDsInvalidator,
		AuthorizationInvalidator:   systemAccountInvalidator,
		GatewayRuntimeInvalidator:  systemAccountInvalidator,
		Logger:                     logger,
	})
	accountGroupBindingService := managementaccountgroupbinding.NewService(managementaccountgroupbinding.Options{
		Store:                      store,
		GranteeReader:              store,
		PageDataPublisher:          accountsStaticResetPublisher,
		RuntimeInvalidator:         systemAccountInvalidator,
		GroupAccountIDsInvalidator: groupAccountIDsInvalidator,
		Logger:                     logger,
	})
	systemAccountService := managementsystemaccounts.NewServiceWithOptions(managementsystemaccounts.ServiceOptions{
		Store:                    store,
		Secret:                   cfg.Secret,
		SystemAccountInvalidator: systemAccountInvalidator,
		PageDataPublisher:        accountsStaticResetPublisher,
		Logger:                   logger,
	})
	systemTeamService := managementsystemteams.NewServiceWithOptions(managementsystemteams.ServiceOptions{
		Store:                    store,
		AuthorizationInvalidator: systemAccountInvalidator,
		Publisher:                accountsStaticResetPublisher,
		Logger:                   logger,
	})
	authorizationService := managementauthorizations.NewServiceWithOptions(managementauthorizations.ServiceOptions{
		Store:                    store,
		Secret:                   cfg.Secret,
		AuthorizationInvalidator: systemAccountInvalidator,
		Publisher:                accountsStaticResetPublisher,
		TeamReader:               store,
		Logger:                   logger,
	})
	authorizationOptionService := managementauthorizationoptions.NewService(store)
	operationLogService := managementoperationlogs.NewService(store)
	auditLogService := managementauditlogs.NewServiceWithOptions(managementauditlogs.ServiceOptions{
		Store: store, HotSearchRoot: cfg.AuditHotSearchDirectory(),
	})
	runtimeLogService := managementruntimelogs.NewService(store)
	runtimeLogGrepService := managementruntimeloggrep.NewService(managementruntimeloggrep.Options{
		Directory:     cfg.RuntimeLogDirectory,
		FileEnabled:   cfg.RuntimeLogFileEnabled,
		MaxFiles:      cfg.RuntimeLogMaxFiles,
		RetentionDays: cfg.RuntimeLogRetentionDays,
		RGPath:        cfg.RGPath,
	})
	externalIntegrationSourceService := managementexternalintegrationsources.NewServiceWithOptions(
		managementexternalintegrationsources.ServiceOptions{
			ListReader:   store,
			DetailReader: store,
			SecretReader: store,
			Secret:       cfg.Secret,
		},
	)
	externalIntegrationSourceUpdateService := managementexternalintegrationsources.NewUpdateService(store)
	externalIntegrationSourceDeleteService := managementexternalintegrationsources.NewDeleteService(store)
	externalIntegrationSourceCreateService := managementexternalintegrationsources.NewCreateService(store, cfg.Secret)
	externalIntegrationSourceBuiltInResetService := managementexternalintegrationsources.NewBuiltInResetService(store, cfg.Secret)
	externalIntegrationSourceTokenCreateService := managementexternalintegrationsources.NewTokenCreateService(store, cfg.Secret)
	externalIntegrationSourceTokenUpdateService := managementexternalintegrationsources.NewTokenUpdateService(store)
	publicAPILogService := managementpublicapilogs.NewService(store)
	usageRecordService := managementusagerecords.NewService(store)
	announcementService := announcements.NewService(store)
	statsService := managementstats.NewService(store)
	globalSettingsService := publicsettings.NewService(store)
	globalSettingsUpdateService := managementsettings.NewServiceWithOptions(managementsettings.ServiceOptions{
		Store:                          store,
		GlobalSettingsCacheInvalidator: systemAccountInvalidator,
	})
	systemSettingsService := managementsettings.NewSystemServiceWithOptions(managementsettings.SystemServiceOptions{
		Store:                             store,
		Invalidator:                       systemAccountInvalidator,
		RateLimitSettingsCacheInvalidator: systemAPIRateLimitSettingsCache,
		Logger:                            logger,
	})
	clientIPPolicyService := managementclientippolicies.NewServiceWithOptions(
		managementclientippolicies.ServiceOptions{
			Transactor:  store,
			Invalidator: systemAccountInvalidator,
			Logger:      logger,
		},
	)
	clientIPStatsService := managementclientipstats.NewServiceWithOptions(
		managementclientipstats.ServiceOptions{
			ListReader:               store,
			RegistryReader:           store,
			DetailReader:             store,
			UsageStatsTimezoneReader: store,
		},
	)
	var pageDataConfirmHandler http.Handler
	if stateRedis != nil {
		pageDataConfirmer, pageDataConfirmErr := redisplatform.NewPageDataChangeConfirmer(stateRedis, cfg.RedisNamespace)
		pageDataConfirmHandler = httpapi.NewPageDataChangeConfirmHandler(pageDataChangeConfirmServiceAdapter{
			confirmer: pageDataConfirmer,
			initErr:   pageDataConfirmErr,
		})
	}
	operationLogOptions := httpapi.ManagementOperationLogOptions{
		Config:         cfg,
		Logger:         logger,
		Client:         operationLogQueue,
		SettingsReader: store,
	}
	return managementAPIHandlers{
		AuthMiddleware:                          httpapi.NewManagementAPIAuthMiddleware(authenticator),
		AuthTouchMiddleware:                     httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		CaptchaHandler:                          httpapi.NewManagementCaptchaHandler(captchaService, cfg),
		LoginHandler:                            httpapi.NewManagementLoginHandler(loginService, cfg),
		CurrentUserHandler:                      httpapi.NewManagementCurrentUserHandler(authenticator),
		ProfileUpdateHandler:                    httpapi.NewManagementProfileUpdateHandlerWithOperationLog(profileService, operationLogOptions),
		PasswordChangeHandler:                   httpapi.NewManagementPasswordChangeHandler(authenticator, passwordService),
		LogoutHandler:                           httpapi.NewManagementLogoutHandler(authenticator, cfg),
		ProxiesHandler:                          httpapi.NewManagementProxiesHandler(proxyService),
		ProxyOptionsHandler:                     httpapi.NewManagementProxyOptionsHandler(proxyService),
		ProxyCreateHandler:                      httpapi.NewManagementProxyCreateHandlerWithOperationLog(proxyService, operationLogOptions),
		ProxyUpdateHandler:                      httpapi.NewManagementProxyUpdateHandlerWithOperationLog(proxyService, operationLogOptions),
		ProxyDeleteHandler:                      httpapi.NewManagementProxyDeleteHandlerWithOperationLog(proxyService, operationLogOptions),
		ProxyTestHandler:                        httpapi.NewManagementProxyTestHandlerWithOperationLog(proxyService, operationLogOptions),
		SystemAccountsHandler:                   httpapi.NewManagementSystemAccountsHandler(systemAccountService),
		SystemAccountOptionsHandler:             httpapi.NewManagementSystemAccountOptionsHandler(systemAccountService),
		SystemAccountPatchHandler:               httpapi.NewManagementSystemAccountPatchHandlerWithOperationLog(systemAccountService, operationLogOptions),
		SystemAccountCreateHandler:              httpapi.NewManagementSystemAccountCreateHandlerWithOperationLog(systemAccountService, operationLogOptions),
		SystemTeamsHandler:                      httpapi.NewManagementSystemTeamsHandler(systemTeamService),
		MySystemTeamsHandler:                    httpapi.NewManagementMySystemTeamsHandler(systemTeamService),
		SystemTeamCreateHandler:                 httpapi.NewManagementSystemTeamCreateHandlerWithOperationLog(systemTeamService, operationLogOptions),
		SystemTeamPatchHandler:                  httpapi.NewManagementSystemTeamPatchHandlerWithOperationLog(systemTeamService, operationLogOptions),
		SystemTeamMembersAddHandler:             httpapi.NewManagementSystemTeamMembersAddHandlerWithOperationLog(systemTeamService, operationLogOptions),
		SystemTeamMemberDeleteHandler:           httpapi.NewManagementSystemTeamMemberDeleteHandlerWithOperationLog(systemTeamService, operationLogOptions),
		AuthorizationGranteeAccountsHandler:     httpapi.NewManagementAuthorizationGranteeAccountsHandler(authorizationOptionService),
		MyAuthorizationGranteeAccountsHandler:   httpapi.NewManagementMyAuthorizationGranteeAccountsHandler(authorizationOptionService),
		AuthorizationGranteeTeamsHandler:        httpapi.NewManagementAuthorizationGranteeTeamsHandler(authorizationOptionService),
		MyAuthorizationGranteeTeamsHandler:      httpapi.NewManagementMyAuthorizationGranteeTeamsHandler(authorizationOptionService),
		AuthorizationGranteeGroupsHandler:       httpapi.NewManagementAuthorizationGranteeGroupsHandler(authorizationOptionService),
		MyAuthorizationGranteeGroupsHandler:     httpapi.NewManagementMyAuthorizationGranteeGroupsHandler(authorizationOptionService),
		AuthorizationListHandler:                httpapi.NewManagementAuthorizationListHandler(authorizationService),
		MyAuthorizationListHandler:              httpapi.NewManagementMyAuthorizationListHandler(authorizationService),
		AuthorizationTeamUsageOverviewHandler:   httpapi.NewManagementAuthorizationTeamUsageOverviewHandler(authorizationService),
		MyAuthorizationTeamUsageOverviewHandler: httpapi.NewManagementMyAuthorizationTeamUsageOverviewHandler(authorizationService),
		AuthorizationUserUsageOverviewHandler:   httpapi.NewManagementAuthorizationUserUsageOverviewHandler(authorizationService),
		MyAuthorizationUserUsageOverviewHandler: httpapi.NewManagementMyAuthorizationUserUsageOverviewHandler(authorizationService),
		AuthorizationUsageHandler:               httpapi.NewManagementAuthorizationUsageHandler(authorizationService),
		MyAuthorizationUsageHandler:             httpapi.NewManagementMyAuthorizationUsageHandler(authorizationService),
		AuthorizationDetailHandler:              httpapi.NewManagementAuthorizationDetailHandler(authorizationService),
		MyAuthorizationDetailHandler:            httpapi.NewManagementMyAuthorizationDetailHandler(authorizationService),
		AuthorizationCreateHandler:              httpapi.NewManagementAuthorizationCreateHandlerWithOperationLog(authorizationService, operationLogOptions),
		MyAuthorizationCreateHandler:            httpapi.NewManagementMyAuthorizationCreateHandlerWithOperationLog(authorizationService, operationLogOptions),
		AuthorizationUpdateHandler:              httpapi.NewManagementAuthorizationUpdateHandlerWithOperationLog(authorizationService, operationLogOptions),
		MyAuthorizationUpdateHandler:            httpapi.NewManagementMyAuthorizationUpdateHandlerWithOperationLog(authorizationService, operationLogOptions),
		AuthorizationExpireUpdateHandler:        httpapi.NewManagementAuthorizationExpireUpdateHandlerWithOperationLog(authorizationService, operationLogOptions),
		MyAuthorizationExpireUpdateHandler:      httpapi.NewManagementMyAuthorizationExpireUpdateHandlerWithOperationLog(authorizationService, operationLogOptions),
		AuthorizationReturnHandler:              httpapi.NewManagementAuthorizationReturnHandlerWithOperationLog(authorizationService, operationLogOptions),
		MyAuthorizationReturnHandler:            httpapi.NewManagementMyAuthorizationReturnHandlerWithOperationLog(authorizationService, operationLogOptions),
		AccountAuthorizationReturnHandler:       httpapi.NewManagementAccountAuthorizationReturnHandlerWithOperationLog(authorizationService, operationLogOptions),
		MyAccountAuthorizationReturnHandler:     httpapi.NewManagementMyAccountAuthorizationReturnHandlerWithOperationLog(authorizationService, operationLogOptions),
		GroupAuthorizationReturnHandler:         httpapi.NewManagementGroupAuthorizationReturnHandlerWithOperationLog(authorizationService, operationLogOptions),
		MyGroupAuthorizationReturnHandler:       httpapi.NewManagementMyGroupAuthorizationReturnHandlerWithOperationLog(authorizationService, operationLogOptions),
		AuthorizationRevokeHandler:              httpapi.NewManagementAuthorizationRevokeHandlerWithOperationLog(authorizationService, operationLogOptions),
		MyAuthorizationRevokeHandler:            httpapi.NewManagementMyAuthorizationRevokeHandlerWithOperationLog(authorizationService, operationLogOptions),
		ProvidersHandler:                        httpapi.NewManagementProvidersHandler(providerService),
		ProviderOptionsHandler:                  httpapi.NewManagementProviderOptionsHandler(providerService),
		ProviderDefinitionsHandler:              httpapi.NewManagementProviderDefinitionsHandler(providerService),
		ProviderModelOptionsHandler:             httpapi.NewManagementProviderModelOptionsHandler(providerModelService),
		ProviderModelsHandler:                   httpapi.NewManagementProviderModelsHandler(providerModelService),
		ProviderModelCapabilitiesHandler:        httpapi.NewManagementProviderModelCapabilitiesHandler(providerModelService),
		ProviderDefaultHealthCheckModelHandler:  httpapi.NewManagementProviderDefaultHealthCheckModelHandler(providerModelService),
		ProviderCustomModelCreateHandler:        httpapi.NewManagementProviderCustomModelCreateHandler(providerModelService),
		ProviderCustomModelUpdateHandler:        httpapi.NewManagementProviderCustomModelUpdateHandlerWithOperationLog(providerModelService, operationLogOptions),
		ProviderCustomModelDeleteHandler:        httpapi.NewManagementProviderCustomModelDeleteHandler(providerModelService),
		RouteStrategyListHandler:                httpapi.NewManagementRouteStrategyListHandler(routeStrategyService),
		MyRouteStrategyListHandler:              httpapi.NewManagementMyRouteStrategyListHandler(routeStrategyService),
		RouteStrategyCreateHandler:              httpapi.NewManagementRouteStrategyCreateHandlerWithOperationLog(routeStrategyService, operationLogOptions),
		MyRouteStrategyCreateHandler:            httpapi.NewManagementMyRouteStrategyCreateHandlerWithOperationLog(routeStrategyService, operationLogOptions),
		RouteStrategyUpdateHandler:              httpapi.NewManagementRouteStrategyUpdateHandlerWithOperationLog(routeStrategyService, operationLogOptions),
		MyRouteStrategyUpdateHandler:            httpapi.NewManagementMyRouteStrategyUpdateHandlerWithOperationLog(routeStrategyService, operationLogOptions),
		RouteStrategyDeleteHandler:              httpapi.NewManagementRouteStrategyDeleteHandlerWithOperationLog(routeStrategyService, operationLogOptions),
		MyRouteStrategyDeleteHandler:            httpapi.NewManagementMyRouteStrategyDeleteHandlerWithOperationLog(routeStrategyService, operationLogOptions),
		RouteStrategyDetailHandler:              httpapi.NewManagementRouteStrategyDetailHandler(routeStrategyService),
		MyRouteStrategyDetailHandler:            httpapi.NewManagementMyRouteStrategyDetailHandler(routeStrategyService),
		RouteStrategyOptionsHandler:             httpapi.NewManagementRouteStrategyOptionsHandler(routeStrategyService),
		MyRouteStrategyOptionsHandler:           httpapi.NewManagementMyRouteStrategyOptionsHandler(routeStrategyService),
		APIKeyListHandler:                       httpapi.NewManagementAPIKeyListHandler(apiKeyService),
		MyAPIKeyListHandler:                     httpapi.NewManagementMyAPIKeyListHandler(apiKeyService),
		APIKeySecretHandler:                     httpapi.NewManagementAPIKeySecretHandlerWithOperationLog(apiKeyService, operationLogOptions),
		MyAPIKeySecretHandler:                   httpapi.NewManagementMyAPIKeySecretHandlerWithOperationLog(apiKeyService, operationLogOptions),
		APIKeyRefreshHandler:                    httpapi.NewManagementAPIKeyRefreshHandlerWithOperationLog(apiKeyService, operationLogOptions),
		MyAPIKeyRefreshHandler:                  httpapi.NewManagementMyAPIKeyRefreshHandlerWithOperationLog(apiKeyService, operationLogOptions),
		APIKeyCreateHandler:                     httpapi.NewManagementAPIKeyCreateHandlerWithOperationLog(apiKeyService, operationLogOptions),
		MyAPIKeyCreateHandler:                   httpapi.NewManagementMyAPIKeyCreateHandlerWithOperationLog(apiKeyService, operationLogOptions),
		APIKeyUpdateHandler:                     httpapi.NewManagementAPIKeyUpdateHandlerWithOperationLog(apiKeyService, operationLogOptions),
		MyAPIKeyUpdateHandler:                   httpapi.NewManagementMyAPIKeyUpdateHandlerWithOperationLog(apiKeyService, operationLogOptions),
		APIKeyDeleteHandler:                     httpapi.NewManagementAPIKeyDeleteHandlerWithOperationLog(apiKeyService, operationLogOptions),
		MyAPIKeyDeleteHandler:                   httpapi.NewManagementMyAPIKeyDeleteHandlerWithOperationLog(apiKeyService, operationLogOptions),
		GroupListHandler:                        httpapi.NewManagementGroupListHandler(groupService),
		MyGroupListHandler:                      httpapi.NewManagementMyGroupListHandler(groupService),
		GroupDetailHandler:                      httpapi.NewManagementGroupDetailHandler(groupService),
		MyGroupDetailHandler:                    httpapi.NewManagementMyGroupDetailHandler(groupService),
		GroupCreateHandler:                      httpapi.NewManagementGroupCreateHandlerWithOperationLog(groupService, operationLogOptions),
		MyGroupCreateHandler:                    httpapi.NewManagementMyGroupCreateHandlerWithOperationLog(groupService, operationLogOptions),
		GroupUpdateHandler:                      httpapi.NewManagementGroupUpdateHandlerWithOperationLog(groupService, operationLogOptions),
		MyGroupUpdateHandler:                    httpapi.NewManagementMyGroupUpdateHandlerWithOperationLog(groupService, operationLogOptions),
		GroupDeleteHandler:                      httpapi.NewManagementGroupDeleteHandlerWithOperationLog(groupService, operationLogOptions),
		MyGroupDeleteHandler:                    httpapi.NewManagementMyGroupDeleteHandlerWithOperationLog(groupService, operationLogOptions),
		GroupOptionsHandler:                     httpapi.NewManagementGroupOptionsHandler(groupService),
		MyGroupOptionsHandler:                   httpapi.NewManagementMyGroupOptionsHandler(groupService),
		GroupAccountOptionsHandler:              httpapi.NewManagementGroupAccountOptionsHandler(groupService),
		MyGroupAccountOptionsHandler:            httpapi.NewManagementMyGroupAccountOptionsHandler(groupService),
		AccountOptionsHandler:                   httpapi.NewManagementAccountOptionsHandler(accountService),
		MyAccountOptionsHandler:                 httpapi.NewManagementMyAccountOptionsHandler(accountService),
		AccountTestOptionsHandler:               httpapi.NewManagementAccountTestOptionsHandler(accountTestOptionsService),
		MyAccountTestOptionsHandler:             httpapi.NewManagementMyAccountTestOptionsHandler(accountTestOptionsService),
		AccountTagsHandler:                      httpapi.NewManagementAccountTagsHandler(accountService),
		MyAccountTagsHandler:                    httpapi.NewManagementMyAccountTagsHandler(accountService),
		AccountTagDeleteHandler:                 httpapi.NewManagementAccountTagDeleteHandler(accountService),
		MyAccountTagDeleteHandler:               httpapi.NewManagementMyAccountTagDeleteHandler(accountService),
		AccountTagUpdateHandler:                 httpapi.NewManagementAccountTagUpdateHandlerWithOperationLog(accountService, operationLogOptions),
		MyAccountTagUpdateHandler:               httpapi.NewManagementMyAccountTagUpdateHandlerWithOperationLog(accountService, operationLogOptions),
		AccountDetailHandler:                    httpapi.NewManagementAccountDetailHandler(accountDetailService),
		MyAccountDetailHandler:                  httpapi.NewManagementMyAccountDetailHandler(accountDetailService),
		AccountEditBasicDetailHandler:           httpapi.NewManagementAccountEditBasicDetailHandler(accountDetailService),
		MyAccountEditBasicDetailHandler:         httpapi.NewManagementMyAccountEditBasicDetailHandler(accountDetailService),
		AccountAdvancedDetailHandler:            httpapi.NewManagementAccountAdvancedDetailHandler(accountDetailService),
		MyAccountAdvancedDetailHandler:          httpapi.NewManagementMyAccountAdvancedDetailHandler(accountDetailService),
		AccountAPIKeyRuntimeHandler:             httpapi.NewManagementAccountAPIKeyRuntimeHandler(accountDetailService),
		MyAccountAPIKeyRuntimeHandler:           httpapi.NewManagementMyAccountAPIKeyRuntimeHandler(accountDetailService),
		AccountGroupBindingHandler:              httpapi.NewManagementAccountGroupBindingHandlerWithOperationLog(accountGroupBindingService, operationLogOptions),
		MyAccountGroupBindingHandler:            httpapi.NewManagementMyAccountGroupBindingHandlerWithOperationLog(accountGroupBindingService, operationLogOptions),
		AccountBatchEditHandler:                 httpapi.NewManagementAccountBatchEditHandler(accountBatchEditService),
		MyAccountBatchEditHandler:               httpapi.NewManagementMyAccountBatchEditHandler(accountBatchEditService),
		AccountForceActivateHandler:             httpapi.NewManagementAccountForceActivateHandlerWithOperationLog(accountForceActivateService, operationLogOptions),
		MyAccountForceActivateHandler:           httpapi.NewManagementMyAccountForceActivateHandlerWithOperationLog(accountForceActivateService, operationLogOptions),
		AccountDeleteHandler:                    httpapi.NewManagementAccountDeleteHandlerWithOperationLog(accountDeleteService, operationLogOptions),
		MyAccountDeleteHandler:                  httpapi.NewManagementMyAccountDeleteHandlerWithOperationLog(accountDeleteService, operationLogOptions),
		AccountBalanceHandler:                   httpapi.NewManagementAccountBalanceHandler(accountBalanceService),
		MyAccountBalanceHandler:                 httpapi.NewManagementMyAccountBalanceHandler(accountBalanceService),
		AccountBalanceRefreshHandler:            httpapi.NewManagementAccountBalanceRefreshHandler(accountBalanceService),
		MyAccountBalanceRefreshHandler:          httpapi.NewManagementMyAccountBalanceRefreshHandler(accountBalanceService),
		AccountStatusSnapshotHandler:            httpapi.NewManagementAccountStatusSnapshotHandler(accountStatusSnapshotService),
		MyAccountStatusSnapshotHandler:          httpapi.NewManagementMyAccountStatusSnapshotHandler(accountStatusSnapshotService),
		AccountListHandler:                      httpapi.NewManagementAccountListHandler(accountListService),
		MyAccountListHandler:                    httpapi.NewManagementMyAccountListHandler(accountListService),
		AccountExportHandler:                    httpapi.NewManagementAccountExportHandler(accountExportService),
		MyAccountExportHandler:                  httpapi.NewManagementMyAccountExportHandler(accountExportService),
		AccountCreateHandler:                    httpapi.NewManagementAccountCreateHandler(accountCreateService),
		MyAccountCreateHandler:                  httpapi.NewManagementMyAccountCreateHandler(accountCreateService),
		AccountUpdateHandler:                    httpapi.NewManagementAccountUpdateHandler(accountUpdateService),
		MyAccountUpdateHandler:                  httpapi.NewManagementMyAccountUpdateHandler(accountUpdateService),
		AccountAuthorizedDispatchHandler:        httpapi.NewManagementAccountAuthorizedDispatchHandler(accountAuthorizedDispatchService),
		MyAccountAuthorizedDispatchHandler:      httpapi.NewManagementMyAccountAuthorizedDispatchHandler(accountAuthorizedDispatchService),
		AccountImportPreviewHandler:             httpapi.NewManagementAccountImportPreviewHandler(accountImportService),
		MyAccountImportPreviewHandler:           httpapi.NewManagementMyAccountImportPreviewHandler(accountImportService),
		AccountImportConfirmHandler:             httpapi.NewManagementAccountImportConfirmHandler(accountImportService),
		MyAccountImportConfirmHandler:           httpapi.NewManagementMyAccountImportConfirmHandler(accountImportService),
		AccountTrafficMigrationHandler:          httpapi.NewManagementAccountTrafficMigrationHandler(accountTrafficMigrationService),
		MyAccountTrafficMigrationHandler:        httpapi.NewManagementMyAccountTrafficMigrationHandler(accountTrafficMigrationService),
		AccountTestSessionCreateHandler:         httpapi.NewManagementAccountTestSessionCreateHandler(accountTestSessionService),
		MyAccountTestSessionCreateHandler:       httpapi.NewManagementMyAccountTestSessionCreateHandler(accountTestSessionService),
		AccountTestSessionHeartbeatHandler:      httpapi.NewManagementAccountTestSessionHeartbeatHandler(accountTestSessionService),
		MyAccountTestSessionHeartbeatHandler:    httpapi.NewManagementMyAccountTestSessionHeartbeatHandler(accountTestSessionService),
		AccountTestSessionCompleteHandler:       httpapi.NewManagementAccountTestSessionCompleteHandler(accountTestSessionService),
		MyAccountTestSessionCompleteHandler:     httpapi.NewManagementMyAccountTestSessionCompleteHandler(accountTestSessionService),
		AccountTestSessionCancelHandler:         httpapi.NewManagementAccountTestSessionCancelHandler(accountTestSessionService),
		MyAccountTestSessionCancelHandler:       httpapi.NewManagementMyAccountTestSessionCancelHandler(accountTestSessionService),
		AccountTestTaskCancelHandler:            httpapi.NewManagementAccountTestTaskCancelHandler(accountTestSessionService),
		MyAccountTestTaskCancelHandler:          httpapi.NewManagementMyAccountTestTaskCancelHandler(accountTestSessionService),
		AccountTestTaskListHandler:              httpapi.NewManagementAccountTestTaskListHandler(accountTestStatusService),
		MyAccountTestTaskListHandler:            httpapi.NewManagementMyAccountTestTaskListHandler(accountTestStatusService),
		AccountTestSessionStatusHandler:         httpapi.NewManagementAccountTestSessionStatusHandler(accountTestStatusService),
		MyAccountTestSessionStatusHandler:       httpapi.NewManagementMyAccountTestSessionStatusHandler(accountTestStatusService),
		AccountTestSessionTasksHandler:          httpapi.NewManagementAccountTestSessionTasksHandler(accountTestStatusService),
		MyAccountTestSessionTasksHandler:        httpapi.NewManagementMyAccountTestSessionTasksHandler(accountTestStatusService),
		AccountTestTaskStatusHandler:            httpapi.NewManagementAccountTestTaskStatusHandler(accountTestStatusService),
		MyAccountTestTaskStatusHandler:          httpapi.NewManagementMyAccountTestTaskStatusHandler(accountTestStatusService),
		AccountTestDispatchHandler:              httpapi.NewManagementAccountTestDispatchHandler(accountTestDispatchService),
		MyAccountTestDispatchHandler:            httpapi.NewManagementMyAccountTestDispatchHandler(accountTestDispatchService),
		SystemSettingsHandler:                   httpapi.NewManagementSystemSettingsHandler(systemSettingsService),
		SystemSettingsUpdateHandler:             httpapi.NewManagementSystemSettingsUpdateHandlerWithOperationLog(systemSettingsService, operationLogOptions),
		GlobalSettingsHandler:                   httpapi.NewManagementGlobalSettingsHandler(&globalSettingsService),
		GlobalSettingsUpdateHandler:             httpapi.NewManagementGlobalSettingsUpdateHandlerWithOperationLog(globalSettingsUpdateService, operationLogOptions),
		ClientIPStatsHandler:                    httpapi.NewManagementClientIPStatsHandler(clientIPStatsService),
		ClientIPStatsDetailHandler:              httpapi.NewManagementClientIPStatsDetailHandler(clientIPStatsService),
		ClientIPAllowlistHandler:                httpapi.NewManagementClientIPAllowlistHandlerWithOperationLog(clientIPPolicyService, operationLogOptions),
		ClientIPUnallowlistHandler:              httpapi.NewManagementClientIPUnallowlistHandlerWithOperationLog(clientIPPolicyService, operationLogOptions),
		ClientIPBlacklistHandler:                httpapi.NewManagementClientIPBlacklistHandlerWithOperationLog(clientIPPolicyService, operationLogOptions),
		ClientIPUnblockHandler:                  httpapi.NewManagementClientIPUnblockHandlerWithOperationLog(clientIPPolicyService, operationLogOptions),
		OperationLogsHandler:                    httpapi.NewManagementOperationLogsHandler(operationLogService),
		MyOperationLogsHandler:                  httpapi.NewManagementMyOperationLogsHandler(operationLogService),
		AuditLogsHandler:                        httpapi.NewManagementAuditLogsHandler(auditLogService),
		AuditErrorGroupsHandler:                 httpapi.NewManagementAuditErrorGroupsHandler(auditLogService),
		AuditErrorGroupEventsHandler:            httpapi.NewManagementAuditErrorGroupEventsHandler(auditLogService),
		RuntimeLogsHandler:                      httpapi.NewManagementRuntimeLogsHandler(runtimeLogService, cfg.RuntimeLogIndexEnabled),
		RuntimeLogGrepHandler:                   httpapi.NewManagementRuntimeLogGrepHandler(runtimeLogGrepService),
		ExternalIntegrationSourceListHandler:    httpapi.NewManagementExternalIntegrationSourceListHandler(externalIntegrationSourceService),
		ExternalIntegrationSourceDetailHandler:  httpapi.NewManagementExternalIntegrationSourceDetailHandler(externalIntegrationSourceService),
		ExternalIntegrationSourceCreateHandler:  httpapi.NewManagementExternalIntegrationSourceCreateHandlerWithOperationLog(externalIntegrationSourceCreateService, operationLogOptions),
		ExternalIntegrationSourceUpdateHandler:  httpapi.NewManagementExternalIntegrationSourceUpdateHandlerWithOperationLog(externalIntegrationSourceUpdateService, operationLogOptions),
		ExternalIntegrationSourceDeleteHandler:  httpapi.NewManagementExternalIntegrationSourceDeleteHandlerWithOperationLog(externalIntegrationSourceDeleteService, operationLogOptions),
		ExternalSourceBuiltInResetHandler:       httpapi.NewManagementExternalIntegrationSourceBuiltInResetHandlerWithOperationLog(externalIntegrationSourceBuiltInResetService, operationLogOptions),
		ExternalSourceTokenCreateHandler:        httpapi.NewManagementExternalIntegrationSourceTokenCreateHandlerWithOperationLog(externalIntegrationSourceTokenCreateService, operationLogOptions),
		ExternalSourceTokenUpdateHandler:        httpapi.NewManagementExternalIntegrationSourceTokenUpdateHandlerWithOperationLog(externalIntegrationSourceTokenUpdateService, operationLogOptions),
		ExternalSourceTokenSecretHandler:        httpapi.NewManagementExternalIntegrationSourceTokenSecretHandler(externalIntegrationSourceService),
		ExternalIntegrationSourceScopesHandler:  httpapi.NewManagementExternalIntegrationSourceScopesHandler(),
		ExternalIntegrationSourceAPIDocsHandler: httpapi.NewManagementExternalIntegrationSourceAPIDocsHandler(),
		PublicAPILogsHandler:                    httpapi.NewManagementPublicAPILogsHandler(publicAPILogService),
		UsageRecordsHandler:                     httpapi.NewManagementUsageRecordsHandler(usageRecordService),
		MyUsageRecordsHandler:                   httpapi.NewManagementMyUsageRecordsHandler(usageRecordService),
		AnnouncementPublicListHandler:           httpapi.NewAnnouncementPublicListHandler(announcementService),
		AnnouncementPublicDetailHandler:         httpapi.NewAnnouncementPublicDetailHandler(announcementService),
		AnnouncementPublicReadHandler:           httpapi.NewAnnouncementPublicReadHandler(announcementService),
		AnnouncementsHandler:                    httpapi.NewAnnouncementManagementHandlerWithOptions(announcementService, operationLogOptions, accountsStaticResetPublisher, logger),
		SystemMetricsHandler:                    httpapi.NewManagementSystemMetricsHandler(statsService),
		PageDataConfirmHandler:                  pageDataConfirmHandler,
		StatsUsageWindowHandler:                 httpapi.NewManagementStatsUsageWindowHandler(statsService),
		MyStatsUsageWindowHandler:               httpapi.NewManagementMyStatsUsageWindowHandler(statsService),
	}
}

type managementCatalogSnapshotBridge interface {
	managementprovidermodels.CatalogSnapshotRebuilder
	httpapi.ReadinessProber
}

func newManagementCatalogSnapshotRebuilder(cfg config.Config) (managementCatalogSnapshotBridge, error) {
	if !cfg.ManagementAPIEnabled {
		return nil, nil
	}
	timeout := cfg.NodeInternalSnapshotRebuildTimeout
	if timeout == 0 {
		timeout = time.Minute
	}
	return modelcatalogsnapshotrebuild.NewClientWithTimeouts(
		cfg.NodeInternalBaseURL,
		cfg.Secret,
		timeout,
		cfg.NodeInternalRequestTimeout,
	)
}

func probeManagementCatalogSnapshotBridge(
	ctx context.Context,
	cfg config.Config,
	bridge managementCatalogSnapshotBridge,
) error {
	if bridge == nil {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, cfg.NodeInternalRequestTimeout)
	defer cancel()
	if err := bridge.Probe(probeCtx); err != nil {
		var probeError *modelcatalogsnapshotrebuild.ProbeError
		if errors.As(err, &probeError) {
			return fmt.Errorf("Node 模型目录快照 bridge readiness 启动门禁失败: %s", probeError.Kind)
		}
		return errors.New("Node 模型目录快照 bridge readiness 启动门禁失败")
	}
	return nil
}

type operationLogEnqueueClient interface {
	Enqueue(ctx context.Context, taskType string, payload []byte, opts queue.EnqueueOptions) (queue.TaskInfo, error)
}

func newGatewaySystemAccountInvalidator(
	ctx context.Context,
	cfg config.Config,
	stateRedis *redisplatform.Client,
) (gatewayCacheInvalidator, *redisplatform.Client, func(), error) {
	closeFn := func() {}
	if !cfg.ManagementAPIEnabled && !cfg.PublicAPIEnabled {
		return nil, nil, closeFn, nil
	}
	if stateRedis == nil {
		return nil, nil, closeFn, fmt.Errorf("gateway system account invalidator requires state redis")
	}
	var cacheRedis *redisplatform.Client
	if cfg.RedisCacheURL == "" {
		return nil, nil, closeFn, fmt.Errorf("gateway invalidator requires JUHE_AI_REDIS_CACHE_URL when management or public API is enabled")
	} else {
		var err error
		cacheRedis, err = redisplatform.NewClient(cfg.RedisCacheURL, cfg.RedisNamespace+":cache")
		if err != nil {
			return nil, nil, closeFn, fmt.Errorf("JUHE_AI_REDIS_CACHE_URL 无效: %w", err)
		}
		closeFn = func() { _ = cacheRedis.Close() }
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := cacheRedis.Ping(pingCtx); err != nil {
			closeFn()
			return nil, nil, func() {}, fmt.Errorf("网关缓存 Redis 不可用: %w", err)
		}
	}
	invalidator, err := gatewaycache.NewSystemAccountInvalidator(gatewaycache.SystemAccountInvalidatorOptions{
		Cache:     cacheRedis,
		State:     stateRedis,
		Namespace: cfg.RedisNamespace,
	})
	if err != nil {
		closeFn()
		return nil, nil, func() {}, err
	}
	return invalidator, cacheRedis, closeFn, nil
}

func newManagementOperationLogQueue(cfg config.Config) (*queue.Client, error) {
	if !cfg.ManagementAPIEnabled {
		return nil, nil
	}
	redisOpts, err := queue.ParseRedisURL(cfg.RedisQueueURL)
	if err != nil {
		return nil, fmt.Errorf("JUHE_AI_REDIS_QUEUE_URL 无效: %w", err)
	}
	logQueue := queue.NewClient(redisOpts)
	if err := logQueue.Ping(); err != nil {
		_ = logQueue.Close()
		return nil, fmt.Errorf("操作日志队列不可用: %w", err)
	}
	return logQueue, nil
}

func newPublicAPIHandler(
	cfg config.Config,
	logger *slog.Logger,
	store *postgresstore.Store,
	stateRedis *redisplatform.Client,
) (http.Handler, *queue.Client, error) {
	return newPublicAPIHandlerWithOptions(cfg, logger, store, stateRedis, PublicAPIHandlerOptions{})
}

type PublicAPIHandlerOptions struct {
	Now                          func() time.Time
	NewLogID                     func() string
	APIKeyInvalidator            publicapikeys.APIKeyGatewayCacheInvalidator
	AccountHealthCheckDispatcher publicaccounts.AccountHealthCheckDispatcher
	PageDataPublisher            accountpagedata.Publisher
}

func NewPublicAPIHandler(
	cfg config.Config,
	logger *slog.Logger,
	store *postgresstore.Store,
	stateRedis *redisplatform.Client,
) (http.Handler, *queue.Client, error) {
	return newPublicAPIHandlerWithOptions(cfg, logger, store, stateRedis, PublicAPIHandlerOptions{})
}

func NewPublicAPIHandlerWithOptions(
	cfg config.Config,
	logger *slog.Logger,
	store *postgresstore.Store,
	stateRedis *redisplatform.Client,
	opts PublicAPIHandlerOptions,
) (http.Handler, *queue.Client, error) {
	return newPublicAPIHandlerWithOptions(cfg, logger, store, stateRedis, opts)
}

func newPublicAPIHandlerWithOptions(
	cfg config.Config,
	logger *slog.Logger,
	store *postgresstore.Store,
	stateRedis *redisplatform.Client,
	opts PublicAPIHandlerOptions,
) (http.Handler, *queue.Client, error) {
	if !cfg.PublicAPIEnabled {
		return nil, nil, nil
	}

	redisOpts, err := queue.ParseRedisURL(cfg.RedisQueueURL)
	if err != nil {
		return nil, nil, fmt.Errorf("JUHE_AI_REDIS_QUEUE_URL 无效: %w", err)
	}
	logQueue := queue.NewClient(redisOpts)
	if err := logQueue.Ping(); err != nil {
		_ = logQueue.Close()
		return nil, nil, fmt.Errorf("公开接口日志队列不可用: %w", err)
	}

	limiter, err := publicapiratelimit.NewLimiter(publicapiratelimit.Options{Client: stateRedis})
	if err != nil {
		_ = logQueue.Close()
		return nil, nil, err
	}

	accountHealthCheckDispatcher, err := newPublicAccountHealthCheckDispatcher(
		cfg,
		opts.AccountHealthCheckDispatcher,
	)
	if err != nil {
		_ = logQueue.Close()
		return nil, nil, err
	}

	handlers, err := newPublicAPIHandlers(
		store,
		cfg.Secret,
		cfg.UpstreamBaseURLPrivateAllowlist,
		opts.APIKeyInvalidator,
		accountHealthCheckDispatcher,
		opts.PageDataPublisher,
		logger,
		cfg.NodeInternalRequestTimeout,
		nil,
	)
	if err != nil {
		_ = logQueue.Close()
		return nil, nil, err
	}

	handler := httpapi.NewPublicAPIShell(httpapi.PublicAPIShellOptions{
		Config:                  cfg,
		Logger:                  logger,
		Authenticator:           publicapiauth.NewAuthenticator(publicapiauth.AuthenticatorOptions{Store: store}),
		RateLimiter:             limiter,
		LogClient:               logQueue,
		EndpointHandlers:        handlers,
		Now:                     opts.Now,
		NewLogID:                opts.NewLogID,
		SkipRequestIDMiddleware: true,
	})

	return handler, logQueue, nil
}

func newPublicAPIHandlers(
	store *postgresstore.Store,
	credentialSecret string,
	privateBaseURLAllowlist []string,
	apiKeyInvalidator publicapikeys.APIKeyGatewayCacheInvalidator,
	accountHealthCheckDispatcher publicaccounts.AccountHealthCheckDispatcher,
	pageDataPublisher accountpagedata.Publisher,
	logger *slog.Logger,
	healthCheckDispatchTimeout time.Duration,
	accountServiceFactory publicAccountServiceFactory,
) (map[string]http.Handler, error) {
	groupService := publicgroups.NewService(publicgroups.Options{Store: store, Transactor: store})
	routeStrategyService := publicroutestrategies.NewService(publicroutestrategies.Options{Store: store, Transactor: store})
	apiKeyService := publicapikeys.NewService(publicapikeys.Options{
		Store:       store,
		Transactor:  store,
		Invalidator: apiKeyInvalidator,
	})
	providerModelService := managementprovidermodels.NewService(store)
	if accountServiceFactory == nil {
		accountServiceFactory = publicaccounts.NewService
	}
	accountService := accountServiceFactory(publicaccounts.Options{
		Store:                      store,
		Transactor:                 store,
		ProviderModels:             providerModelService,
		HealthCheckDispatcher:      accountHealthCheckDispatcher,
		GranteeReader:              store,
		PageDataPublisher:          pageDataPublisher,
		HealthCheckDispatchTimeout: healthCheckDispatchTimeout,
		Logger:                     logger,
		Secret:                     credentialSecret,
		PrivateBaseURLAllowlist:    privateBaseURLAllowlist,
	})

	handlers := map[string]http.Handler{}
	for _, part := range []map[string]http.Handler{
		httpapi.NewPublicGroupHandlers(groupService),
		httpapi.NewPublicRouteStrategyHandlers(routeStrategyService),
		httpapi.NewPublicAPIKeyHandlers(apiKeyService),
		httpapi.NewPublicAccountHandlers(accountService),
	} {
		for id, handler := range part {
			if _, exists := handlers[id]; exists {
				return nil, fmt.Errorf("公开接口 handler 重复: %s", id)
			}
			handlers[id] = handler
		}
	}

	for _, endpoint := range publicapicatalog.Endpoints() {
		if handlers[endpoint.ID] == nil {
			return nil, fmt.Errorf("公开接口 handler 未实现: %s", endpoint.ID)
		}
	}
	return handlers, nil
}

func newPublicAccountHealthCheckDispatcher(
	cfg config.Config,
	injected publicaccounts.AccountHealthCheckDispatcher,
) (publicaccounts.AccountHealthCheckDispatcher, error) {
	if injected != nil {
		return injected, nil
	}
	dispatcher, err := accounthealthcheckdispatch.NewClientWithTimeout(
		cfg.NodeInternalBaseURL,
		strings.TrimSpace(cfg.Secret),
		cfg.NodeInternalRequestTimeout,
	)
	if err != nil {
		return nil, fmt.Errorf("初始化公开账户健康检查投递器失败: %w", err)
	}
	return dispatcher, nil
}

type publicAccountServiceFactory func(publicaccounts.Options) *publicaccounts.Service
