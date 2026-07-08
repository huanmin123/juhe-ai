package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/managementaccounts"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementauthorizationoptions"
	"juhe-ai/backend-go/internal/modules/managementgroups"
	"juhe-ai/backend-go/internal/modules/managementoperationlogs"
	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/modules/managementproviders"
	"juhe-ai/backend-go/internal/modules/managementproxies"
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	"juhe-ai/backend-go/internal/modules/managementsystemaccounts"
	"juhe-ai/backend-go/internal/modules/publicaccounts"
	publicapicatalog "juhe-ai/backend-go/internal/modules/publicapi"
	publicapiauth "juhe-ai/backend-go/internal/modules/publicapi/auth"
	publicapiratelimit "juhe-ai/backend-go/internal/modules/publicapi/ratelimit"
	"juhe-ai/backend-go/internal/modules/publicapikeys"
	"juhe-ai/backend-go/internal/modules/publicgroups"
	"juhe-ai/backend-go/internal/modules/publicroutestrategies"
	"juhe-ai/backend-go/internal/modules/publicsettings"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func RunServer(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.PostgresURL == "" {
		return fmt.Errorf("JUHE_AI_POSTGRES_URL 不能为空")
	}
	if cfg.RedisStateURL == "" {
		return fmt.Errorf("JUHE_AI_REDIS_STATE_URL 不能为空")
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

	publicAPIHandler, publicAPILogQueue, err := newPublicAPIHandler(cfg, logger, store, stateRedis)
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

	publicSettingsService := publicsettings.NewService(store)
	managementHandlers := newManagementAPIHandler(cfg, store, managementOperationLogQueue, logger)
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                                          cfg,
		Logger:                                          logger,
		PublicSettingsService:                           &publicSettingsService,
		SystemAPIIPRateLimitReader:                      store,
		SystemAPIIPReadRateLimiter:                      httpapi.NewRedisSystemAPIIPReadRateLimiter(stateRedis),
		PublicAPIHandler:                                publicAPIHandler,
		ManagementAPIAuthMiddleware:                     managementHandlers.AuthMiddleware,
		ManagementCurrentUserHandler:                    managementHandlers.CurrentUserHandler,
		ManagementProxyOptionsHandler:                   managementHandlers.ProxyOptionsHandler,
		ManagementSystemAccountsHandler:                 managementHandlers.SystemAccountsHandler,
		ManagementSystemAccountOptionsHandler:           managementHandlers.SystemAccountOptionsHandler,
		ManagementAuthorizationGranteeAccountsHandler:   managementHandlers.AuthorizationGranteeAccountsHandler,
		ManagementMyAuthorizationGranteeAccountsHandler: managementHandlers.MyAuthorizationGranteeAccountsHandler,
		ManagementAuthorizationGranteeTeamsHandler:      managementHandlers.AuthorizationGranteeTeamsHandler,
		ManagementMyAuthorizationGranteeTeamsHandler:    managementHandlers.MyAuthorizationGranteeTeamsHandler,
		ManagementAuthorizationGranteeGroupsHandler:     managementHandlers.AuthorizationGranteeGroupsHandler,
		ManagementMyAuthorizationGranteeGroupsHandler:   managementHandlers.MyAuthorizationGranteeGroupsHandler,
		ManagementProviderOptionsHandler:                managementHandlers.ProviderOptionsHandler,
		ManagementProviderModelOptionsHandler:           managementHandlers.ProviderModelOptionsHandler,
		ManagementProviderModelsHandler:                 managementHandlers.ProviderModelsHandler,
		ManagementProviderDefaultTestModelHandler:       managementHandlers.ProviderDefaultTestModelHandler,
		ManagementRouteStrategyOptionsHandler:           managementHandlers.RouteStrategyOptionsHandler,
		ManagementMyRouteStrategyOptionsHandler:         managementHandlers.MyRouteStrategyOptionsHandler,
		ManagementGroupOptionsHandler:                   managementHandlers.GroupOptionsHandler,
		ManagementMyGroupOptionsHandler:                 managementHandlers.MyGroupOptionsHandler,
		ManagementGroupAccountOptionsHandler:            managementHandlers.GroupAccountOptionsHandler,
		ManagementMyGroupAccountOptionsHandler:          managementHandlers.MyGroupAccountOptionsHandler,
		ManagementAccountOptionsHandler:                 managementHandlers.AccountOptionsHandler,
		ManagementMyAccountOptionsHandler:               managementHandlers.MyAccountOptionsHandler,
		ManagementAccountTagsHandler:                    managementHandlers.AccountTagsHandler,
		ManagementMyAccountTagsHandler:                  managementHandlers.MyAccountTagsHandler,
		ManagementAccountTagDeleteHandler:               managementHandlers.AccountTagDeleteHandler,
		ManagementMyAccountTagDeleteHandler:             managementHandlers.MyAccountTagDeleteHandler,
		ManagementAccountTagUpdateHandler:               managementHandlers.AccountTagUpdateHandler,
		ManagementMyAccountTagUpdateHandler:             managementHandlers.MyAccountTagUpdateHandler,
		ManagementOperationLogsHandler:                  managementHandlers.OperationLogsHandler,
		ManagementMyOperationLogsHandler:                managementHandlers.MyOperationLogsHandler,
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
	AuthMiddleware                        func(http.Handler) http.Handler
	CurrentUserHandler                    http.Handler
	ProxyOptionsHandler                   http.Handler
	SystemAccountsHandler                 http.Handler
	SystemAccountOptionsHandler           http.Handler
	AuthorizationGranteeAccountsHandler   http.Handler
	MyAuthorizationGranteeAccountsHandler http.Handler
	AuthorizationGranteeTeamsHandler      http.Handler
	MyAuthorizationGranteeTeamsHandler    http.Handler
	AuthorizationGranteeGroupsHandler     http.Handler
	MyAuthorizationGranteeGroupsHandler   http.Handler
	ProviderOptionsHandler                http.Handler
	ProviderModelOptionsHandler           http.Handler
	ProviderModelsHandler                 http.Handler
	ProviderDefaultTestModelHandler       http.Handler
	RouteStrategyOptionsHandler           http.Handler
	MyRouteStrategyOptionsHandler         http.Handler
	GroupOptionsHandler                   http.Handler
	MyGroupOptionsHandler                 http.Handler
	GroupAccountOptionsHandler            http.Handler
	MyGroupAccountOptionsHandler          http.Handler
	AccountOptionsHandler                 http.Handler
	MyAccountOptionsHandler               http.Handler
	AccountTagsHandler                    http.Handler
	MyAccountTagsHandler                  http.Handler
	AccountTagDeleteHandler               http.Handler
	MyAccountTagDeleteHandler             http.Handler
	AccountTagUpdateHandler               http.Handler
	MyAccountTagUpdateHandler             http.Handler
	OperationLogsHandler                  http.Handler
	MyOperationLogsHandler                http.Handler
}

func newManagementAPIHandler(
	cfg config.Config,
	store *postgresstore.Store,
	operationLogQueue operationLogEnqueueClient,
	logger *slog.Logger,
) managementAPIHandlers {
	if !cfg.ManagementAPIEnabled {
		return managementAPIHandlers{}
	}
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{Store: store})
	proxyService := managementproxies.NewService(store)
	providerService := managementproviders.NewService(store)
	providerModelService := managementprovidermodels.NewService(store)
	routeStrategyService := managementroutestrategies.NewService(store)
	groupService := managementgroups.NewService(store)
	accountService := managementaccounts.NewService(store)
	systemAccountService := managementsystemaccounts.NewService(store)
	authorizationOptionService := managementauthorizationoptions.NewService(store)
	operationLogService := managementoperationlogs.NewService(store)
	operationLogOptions := httpapi.ManagementOperationLogOptions{
		Config: cfg,
		Logger: logger,
		Client: operationLogQueue,
	}
	return managementAPIHandlers{
		AuthMiddleware:                        httpapi.NewManagementAPIAuthMiddleware(authenticator),
		CurrentUserHandler:                    httpapi.NewManagementCurrentUserHandler(authenticator),
		ProxyOptionsHandler:                   httpapi.NewManagementProxyOptionsHandler(proxyService),
		SystemAccountsHandler:                 httpapi.NewManagementSystemAccountsHandler(systemAccountService),
		SystemAccountOptionsHandler:           httpapi.NewManagementSystemAccountOptionsHandler(systemAccountService),
		AuthorizationGranteeAccountsHandler:   httpapi.NewManagementAuthorizationGranteeAccountsHandler(authorizationOptionService),
		MyAuthorizationGranteeAccountsHandler: httpapi.NewManagementMyAuthorizationGranteeAccountsHandler(authorizationOptionService),
		AuthorizationGranteeTeamsHandler:      httpapi.NewManagementAuthorizationGranteeTeamsHandler(authorizationOptionService),
		MyAuthorizationGranteeTeamsHandler:    httpapi.NewManagementMyAuthorizationGranteeTeamsHandler(authorizationOptionService),
		AuthorizationGranteeGroupsHandler:     httpapi.NewManagementAuthorizationGranteeGroupsHandler(authorizationOptionService),
		MyAuthorizationGranteeGroupsHandler:   httpapi.NewManagementMyAuthorizationGranteeGroupsHandler(authorizationOptionService),
		ProviderOptionsHandler:                httpapi.NewManagementProviderOptionsHandler(providerService),
		ProviderModelOptionsHandler:           httpapi.NewManagementProviderModelOptionsHandler(providerModelService),
		ProviderModelsHandler:                 httpapi.NewManagementProviderModelsHandler(providerModelService),
		ProviderDefaultTestModelHandler:       httpapi.NewManagementProviderDefaultTestModelHandler(providerModelService),
		RouteStrategyOptionsHandler:           httpapi.NewManagementRouteStrategyOptionsHandler(routeStrategyService),
		MyRouteStrategyOptionsHandler:         httpapi.NewManagementMyRouteStrategyOptionsHandler(routeStrategyService),
		GroupOptionsHandler:                   httpapi.NewManagementGroupOptionsHandler(groupService),
		MyGroupOptionsHandler:                 httpapi.NewManagementMyGroupOptionsHandler(groupService),
		GroupAccountOptionsHandler:            httpapi.NewManagementGroupAccountOptionsHandler(groupService),
		MyGroupAccountOptionsHandler:          httpapi.NewManagementMyGroupAccountOptionsHandler(groupService),
		AccountOptionsHandler:                 httpapi.NewManagementAccountOptionsHandler(accountService),
		MyAccountOptionsHandler:               httpapi.NewManagementMyAccountOptionsHandler(accountService),
		AccountTagsHandler:                    httpapi.NewManagementAccountTagsHandler(accountService),
		MyAccountTagsHandler:                  httpapi.NewManagementMyAccountTagsHandler(accountService),
		AccountTagDeleteHandler:               httpapi.NewManagementAccountTagDeleteHandler(accountService),
		MyAccountTagDeleteHandler:             httpapi.NewManagementMyAccountTagDeleteHandler(accountService),
		AccountTagUpdateHandler:               httpapi.NewManagementAccountTagUpdateHandlerWithOperationLog(accountService, operationLogOptions),
		MyAccountTagUpdateHandler:             httpapi.NewManagementMyAccountTagUpdateHandlerWithOperationLog(accountService, operationLogOptions),
		OperationLogsHandler:                  httpapi.NewManagementOperationLogsHandler(operationLogService),
		MyOperationLogsHandler:                httpapi.NewManagementMyOperationLogsHandler(operationLogService),
	}
}

type operationLogEnqueueClient interface {
	Enqueue(ctx context.Context, taskType string, payload []byte, opts queue.EnqueueOptions) (queue.TaskInfo, error)
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
	Now      func() time.Time
	NewLogID func() string
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

	handlers, err := newPublicAPIHandlers(store, cfg.Secret)
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

func newPublicAPIHandlers(store *postgresstore.Store, credentialSecret string) (map[string]http.Handler, error) {
	groupService := publicgroups.NewService(publicgroups.Options{Store: store, Transactor: store})
	routeStrategyService := publicroutestrategies.NewService(publicroutestrategies.Options{Store: store, Transactor: store})
	apiKeyService := publicapikeys.NewService(publicapikeys.Options{Store: store, Transactor: store})
	accountService := publicaccounts.NewService(publicaccounts.Options{Store: store, Transactor: store, Secret: credentialSecret})

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
