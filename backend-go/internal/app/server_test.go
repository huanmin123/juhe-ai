package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/modules/publicaccounts"
	publicapicatalog "juhe-ai/backend-go/internal/modules/publicapi"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
	"juhe-ai/backend-go/internal/store/port"
)

func TestNewPublicAPIHandlerDisabledSkipsRuntimeDependencies(t *testing.T) {
	handler, logQueue, err := newPublicAPIHandler(config.Config{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("newPublicAPIHandler() error = %v", err)
	}
	if handler != nil || logQueue != nil {
		t.Fatalf("newPublicAPIHandler() = (%v, %v), want nil handler and queue when disabled", handler, logQueue)
	}
}

func TestNewPublicAPIHandlerRejectsInvalidQueueURLWhenEnabled(t *testing.T) {
	_, _, err := newPublicAPIHandler(config.Config{
		PublicAPIEnabled: true,
		RedisQueueURL:    "http://127.0.0.1:6379/2",
	}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_QUEUE_URL") {
		t.Fatalf("newPublicAPIHandler() error = %v, want redis queue url error", err)
	}
}

func TestNewManagementOperationLogQueueDisabledSkipsRuntimeDependencies(t *testing.T) {
	logQueue, err := newManagementOperationLogQueue(config.Config{})
	if err != nil {
		t.Fatalf("newManagementOperationLogQueue() error = %v", err)
	}
	if logQueue != nil {
		t.Fatal("newManagementOperationLogQueue() returned queue while management API disabled")
	}
}

func TestNewManagementOperationLogQueueRejectsInvalidQueueURLWhenEnabled(t *testing.T) {
	_, err := newManagementOperationLogQueue(config.Config{
		ManagementAPIEnabled: true,
		RedisQueueURL:        "http://127.0.0.1:6379/2",
	})
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_QUEUE_URL") {
		t.Fatalf("newManagementOperationLogQueue() error = %v, want redis queue url error", err)
	}
}

func TestNewPublicAPIHandlersCoversCatalog(t *testing.T) {
	handlers, err := newPublicAPIHandlers(
		nil,
		"12345678901234567890123456789012",
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("newPublicAPIHandlers() error = %v", err)
	}

	endpoints := publicapicatalog.Endpoints()
	if len(handlers) != len(endpoints) {
		t.Fatalf("handlers = %d, want %d", len(handlers), len(endpoints))
	}
	for _, endpoint := range endpoints {
		if handlers[endpoint.ID] == nil {
			t.Fatalf("handler %q is missing", endpoint.ID)
		}
	}
}

func TestNewPublicAccountHealthCheckDispatcherPrefersExplicitInjection(t *testing.T) {
	injected := &appAccountHealthCheckDispatcherRecorder{}

	dispatcher, err := newPublicAccountHealthCheckDispatcher(config.Config{
		NodeInternalBaseURL:        "http://example.com:3000",
		NodeInternalRequestTimeout: 0,
	}, injected)
	if err != nil {
		t.Fatalf("newPublicAccountHealthCheckDispatcher() error = %v", err)
	}
	if dispatcher != injected {
		t.Fatalf("dispatcher = %T, want injected recorder", dispatcher)
	}
}

func TestNewPublicAccountHealthCheckDispatcherFailsFastOnMissingOrInvalidURL(t *testing.T) {
	for _, test := range []struct {
		name    string
		baseURL string
	}{
		{name: "missing URL"},
		{name: "invalid URL", baseURL: "http://example.com:3000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newPublicAccountHealthCheckDispatcher(config.Config{
				Secret:                     "12345678901234567890123456789012",
				NodeInternalBaseURL:        test.baseURL,
				NodeInternalRequestTimeout: 2 * time.Second,
			}, nil)
			if err == nil {
				t.Fatal("newPublicAccountHealthCheckDispatcher() error = nil, want fail-fast error")
			}
			for _, want := range []string{"初始化公开账户健康检查投递器失败", "base URL"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want contains %q", err, want)
				}
			}
		})
	}
}

func TestNewPublicAccountServicePassesDispatcherAndLoggerToAdd(t *testing.T) {
	store := &appPublicAccountStoreFake{}
	dispatcher := &appAccountHealthCheckDispatcherRecorder{
		err: errors.New("node dispatch unavailable"),
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	service := newPublicAccountService(
		store,
		nil,
		appProviderModelReaderStub{},
		"12345678901234567890123456789012",
		dispatcher,
		logger,
	)

	response, err := service.Add(context.Background(), publicaccounts.AddInput{
		TargetUsername:            "admin",
		TargetGroupName:           "公开分组",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "profile_gpt_openai_v1",
		Name:                      "生产装配账号",
		Type:                      publicaccounts.AccountTypeAPIKey,
		BaseURL:                   "https://api.openai.com/v1",
		APIKey:                    "sk-production-wiring-0123456789abcdef",
		SupportedModels: publicaccounts.NewStringListValue(
			[]string{"gpt-5.4-mini"},
			true,
		),
	})
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if response.Account == nil {
		t.Fatal("Add() account = nil")
	}
	if len(dispatcher.calls) != 1 {
		t.Fatalf("dispatch calls = %#v, want one call", dispatcher.calls)
	}
	if call := dispatcher.calls[0]; call.accountID != response.Account.ID || call.reason != "activation" {
		t.Fatalf("dispatch call = %#v, want account %q activation", call, response.Account.ID)
	}
	for _, want := range []string{
		`"event":"public_account_health_check_dispatch_failed"`,
		`"account_id":"` + response.Account.ID + `"`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs = %q, want contains %q", logs.String(), want)
		}
	}
}

func TestNewManagementAPIHandlerDisabledSkipsRuntimeDependencies(t *testing.T) {
	handlers := newManagementAPIHandler(config.Config{}, nil, nil, nil, nil, nil, nil)
	if handlers.AuthMiddleware != nil ||
		handlers.AuthTouchMiddleware != nil ||
		handlers.CaptchaHandler != nil ||
		handlers.LoginHandler != nil ||
		handlers.CurrentUserHandler != nil ||
		handlers.ProfileUpdateHandler != nil ||
		handlers.PasswordChangeHandler != nil ||
		handlers.LogoutHandler != nil ||
		handlers.SessionListHandler != nil ||
		handlers.SessionRevokeHandler != nil ||
		handlers.ProxiesHandler != nil ||
		handlers.ProxyOptionsHandler != nil ||
		handlers.ProxyCreateHandler != nil ||
		handlers.ProxyUpdateHandler != nil ||
		handlers.ProxyDeleteHandler != nil ||
		handlers.SystemAccountsHandler != nil ||
		handlers.SystemAccountOptionsHandler != nil ||
		handlers.SystemAccountPatchHandler != nil ||
		handlers.SystemAccountCreateHandler != nil ||
		handlers.SystemTeamsHandler != nil ||
		handlers.MySystemTeamsHandler != nil ||
		handlers.SystemTeamCreateHandler != nil ||
		handlers.AuthorizationGranteeAccountsHandler != nil ||
		handlers.MyAuthorizationGranteeAccountsHandler != nil ||
		handlers.AuthorizationGranteeTeamsHandler != nil ||
		handlers.MyAuthorizationGranteeTeamsHandler != nil ||
		handlers.AuthorizationGranteeGroupsHandler != nil ||
		handlers.MyAuthorizationGranteeGroupsHandler != nil ||
		handlers.AuthorizationListHandler != nil ||
		handlers.MyAuthorizationListHandler != nil ||
		handlers.AuthorizationDetailHandler != nil ||
		handlers.MyAuthorizationDetailHandler != nil ||
		handlers.AuthorizationCreateHandler != nil ||
		handlers.MyAuthorizationCreateHandler != nil ||
		handlers.AuthorizationUpdateHandler != nil ||
		handlers.MyAuthorizationUpdateHandler != nil ||
		handlers.AuthorizationExpireUpdateHandler != nil ||
		handlers.MyAuthorizationExpireUpdateHandler != nil ||
		handlers.AuthorizationReturnHandler != nil ||
		handlers.MyAuthorizationReturnHandler != nil ||
		handlers.AccountAuthorizationReturnHandler != nil ||
		handlers.MyAccountAuthorizationReturnHandler != nil ||
		handlers.GroupAuthorizationReturnHandler != nil ||
		handlers.MyGroupAuthorizationReturnHandler != nil ||
		handlers.AuthorizationRevokeHandler != nil ||
		handlers.MyAuthorizationRevokeHandler != nil ||
		handlers.ProvidersHandler != nil ||
		handlers.ProviderOptionsHandler != nil ||
		handlers.ProviderModelOptionsHandler != nil ||
		handlers.ProviderModelsHandler != nil ||
		handlers.ProviderDefaultHealthCheckModelHandler != nil ||
		handlers.RouteStrategyOptionsHandler != nil ||
		handlers.MyRouteStrategyOptionsHandler != nil ||
		handlers.APIKeyListHandler != nil ||
		handlers.MyAPIKeyListHandler != nil ||
		handlers.APIKeySecretHandler != nil ||
		handlers.MyAPIKeySecretHandler != nil ||
		handlers.APIKeyRefreshHandler != nil ||
		handlers.MyAPIKeyRefreshHandler != nil ||
		handlers.APIKeyCreateHandler != nil ||
		handlers.MyAPIKeyCreateHandler != nil ||
		handlers.APIKeyUpdateHandler != nil ||
		handlers.MyAPIKeyUpdateHandler != nil ||
		handlers.APIKeyDeleteHandler != nil ||
		handlers.MyAPIKeyDeleteHandler != nil ||
		handlers.GroupListHandler != nil ||
		handlers.MyGroupListHandler != nil ||
		handlers.GroupCreateHandler != nil ||
		handlers.MyGroupCreateHandler != nil ||
		handlers.GroupUpdateHandler != nil ||
		handlers.MyGroupUpdateHandler != nil ||
		handlers.GroupDeleteHandler != nil ||
		handlers.MyGroupDeleteHandler != nil ||
		handlers.GroupOptionsHandler != nil ||
		handlers.MyGroupOptionsHandler != nil ||
		handlers.GroupAccountOptionsHandler != nil ||
		handlers.MyGroupAccountOptionsHandler != nil ||
		handlers.AccountOptionsHandler != nil ||
		handlers.MyAccountOptionsHandler != nil ||
		handlers.AccountTagsHandler != nil ||
		handlers.MyAccountTagsHandler != nil ||
		handlers.AccountTagDeleteHandler != nil ||
		handlers.MyAccountTagDeleteHandler != nil ||
		handlers.AccountTagUpdateHandler != nil ||
		handlers.MyAccountTagUpdateHandler != nil ||
		handlers.SystemSettingsHandler != nil ||
		handlers.SystemSettingsUpdateHandler != nil ||
		handlers.GlobalSettingsHandler != nil ||
		handlers.GlobalSettingsUpdateHandler != nil ||
		handlers.OperationLogsHandler != nil ||
		handlers.MyOperationLogsHandler != nil ||
		handlers.StatsUsageWindowHandler != nil ||
		handlers.MyStatsUsageWindowHandler != nil {
		t.Fatal("newManagementAPIHandler() returned middleware or handler while disabled")
	}
}

func TestNewManagementAPIHandlerSessionSwitchOnlyReturnsSessionHandlers(t *testing.T) {
	handlers := newManagementAPIHandler(config.Config{ManagementAuthSessionsEnabled: true}, nil, nil, nil, nil, nil, nil)
	if handlers.AuthMiddleware == nil ||
		handlers.AuthTouchMiddleware == nil ||
		handlers.SessionListHandler == nil ||
		handlers.SessionRevokeHandler == nil {
		t.Fatal("newManagementAPIHandler() did not return auth/session handlers while session switch enabled")
	}
	if handlers.CaptchaHandler != nil ||
		handlers.LoginHandler != nil ||
		handlers.CurrentUserHandler != nil ||
		handlers.ProfileUpdateHandler != nil ||
		handlers.PasswordChangeHandler != nil ||
		handlers.LogoutHandler != nil ||
		handlers.ProxiesHandler != nil ||
		handlers.ProxyOptionsHandler != nil ||
		handlers.ProxyCreateHandler != nil ||
		handlers.ProxyUpdateHandler != nil ||
		handlers.ProxyDeleteHandler != nil ||
		handlers.SystemAccountsHandler != nil ||
		handlers.SystemAccountOptionsHandler != nil ||
		handlers.SystemAccountPatchHandler != nil ||
		handlers.SystemAccountCreateHandler != nil ||
		handlers.SystemTeamsHandler != nil ||
		handlers.MySystemTeamsHandler != nil ||
		handlers.SystemTeamCreateHandler != nil ||
		handlers.SystemTeamPatchHandler != nil ||
		handlers.SystemTeamMembersAddHandler != nil ||
		handlers.SystemTeamMemberDeleteHandler != nil ||
		handlers.AuthorizationGranteeAccountsHandler != nil ||
		handlers.AuthorizationListHandler != nil ||
		handlers.AuthorizationCreateHandler != nil ||
		handlers.AuthorizationReturnHandler != nil ||
		handlers.AuthorizationRevokeHandler != nil ||
		handlers.ProvidersHandler != nil ||
		handlers.ProviderOptionsHandler != nil ||
		handlers.ProviderModelOptionsHandler != nil ||
		handlers.ProviderModelsHandler != nil ||
		handlers.ProviderDefaultHealthCheckModelHandler != nil ||
		handlers.RouteStrategyOptionsHandler != nil ||
		handlers.APIKeyListHandler != nil ||
		handlers.MyAPIKeyListHandler != nil ||
		handlers.APIKeySecretHandler != nil ||
		handlers.MyAPIKeySecretHandler != nil ||
		handlers.APIKeyRefreshHandler != nil ||
		handlers.MyAPIKeyRefreshHandler != nil ||
		handlers.APIKeyCreateHandler != nil ||
		handlers.MyAPIKeyCreateHandler != nil ||
		handlers.APIKeyUpdateHandler != nil ||
		handlers.MyAPIKeyUpdateHandler != nil ||
		handlers.APIKeyDeleteHandler != nil ||
		handlers.MyAPIKeyDeleteHandler != nil ||
		handlers.GroupListHandler != nil ||
		handlers.MyGroupListHandler != nil ||
		handlers.GroupCreateHandler != nil ||
		handlers.MyGroupCreateHandler != nil ||
		handlers.GroupUpdateHandler != nil ||
		handlers.MyGroupUpdateHandler != nil ||
		handlers.GroupDeleteHandler != nil ||
		handlers.MyGroupDeleteHandler != nil ||
		handlers.GroupOptionsHandler != nil ||
		handlers.AccountOptionsHandler != nil ||
		handlers.AccountTagsHandler != nil ||
		handlers.SystemSettingsHandler != nil ||
		handlers.SystemSettingsUpdateHandler != nil ||
		handlers.GlobalSettingsHandler != nil ||
		handlers.GlobalSettingsUpdateHandler != nil ||
		handlers.OperationLogsHandler != nil ||
		handlers.StatsUsageWindowHandler != nil ||
		handlers.MyStatsUsageWindowHandler != nil {
		t.Fatal("newManagementAPIHandler() returned non-session management handlers while only session switch enabled")
	}
}

func TestNewManagementAPIHandlerEnabledReturnsAuthAndManagementOptionsHandlers(t *testing.T) {
	handlers := newManagementAPIHandler(config.Config{ManagementAPIEnabled: true}, nil, nil, nil, nil, nil, nil)
	if handlers.AuthMiddleware == nil ||
		handlers.AuthTouchMiddleware == nil ||
		handlers.CaptchaHandler == nil ||
		handlers.LoginHandler == nil ||
		handlers.CurrentUserHandler == nil ||
		handlers.ProfileUpdateHandler == nil ||
		handlers.PasswordChangeHandler == nil ||
		handlers.LogoutHandler == nil ||
		handlers.SessionListHandler == nil ||
		handlers.SessionRevokeHandler == nil ||
		handlers.ProxiesHandler == nil ||
		handlers.ProxyOptionsHandler == nil ||
		handlers.ProxyCreateHandler == nil ||
		handlers.ProxyUpdateHandler == nil ||
		handlers.ProxyDeleteHandler == nil ||
		handlers.SystemAccountsHandler == nil ||
		handlers.SystemAccountOptionsHandler == nil ||
		handlers.SystemAccountPatchHandler == nil ||
		handlers.SystemAccountCreateHandler == nil ||
		handlers.SystemTeamsHandler == nil ||
		handlers.MySystemTeamsHandler == nil ||
		handlers.SystemTeamCreateHandler == nil ||
		handlers.AuthorizationGranteeAccountsHandler == nil ||
		handlers.MyAuthorizationGranteeAccountsHandler == nil ||
		handlers.AuthorizationGranteeTeamsHandler == nil ||
		handlers.MyAuthorizationGranteeTeamsHandler == nil ||
		handlers.AuthorizationGranteeGroupsHandler == nil ||
		handlers.MyAuthorizationGranteeGroupsHandler == nil ||
		handlers.AuthorizationListHandler == nil ||
		handlers.MyAuthorizationListHandler == nil ||
		handlers.AuthorizationDetailHandler == nil ||
		handlers.MyAuthorizationDetailHandler == nil ||
		handlers.AuthorizationCreateHandler == nil ||
		handlers.MyAuthorizationCreateHandler == nil ||
		handlers.AuthorizationUpdateHandler == nil ||
		handlers.MyAuthorizationUpdateHandler == nil ||
		handlers.AuthorizationExpireUpdateHandler == nil ||
		handlers.MyAuthorizationExpireUpdateHandler == nil ||
		handlers.AuthorizationReturnHandler == nil ||
		handlers.MyAuthorizationReturnHandler == nil ||
		handlers.AccountAuthorizationReturnHandler == nil ||
		handlers.MyAccountAuthorizationReturnHandler == nil ||
		handlers.GroupAuthorizationReturnHandler == nil ||
		handlers.MyGroupAuthorizationReturnHandler == nil ||
		handlers.AuthorizationRevokeHandler == nil ||
		handlers.MyAuthorizationRevokeHandler == nil ||
		handlers.ProvidersHandler == nil ||
		handlers.ProviderOptionsHandler == nil ||
		handlers.ProviderModelOptionsHandler == nil ||
		handlers.ProviderModelsHandler == nil ||
		handlers.ProviderDefaultHealthCheckModelHandler == nil ||
		handlers.RouteStrategyOptionsHandler == nil ||
		handlers.MyRouteStrategyOptionsHandler == nil ||
		handlers.APIKeyListHandler == nil ||
		handlers.MyAPIKeyListHandler == nil ||
		handlers.APIKeySecretHandler == nil ||
		handlers.MyAPIKeySecretHandler == nil ||
		handlers.APIKeyRefreshHandler == nil ||
		handlers.MyAPIKeyRefreshHandler == nil ||
		handlers.APIKeyCreateHandler == nil ||
		handlers.MyAPIKeyCreateHandler == nil ||
		handlers.APIKeyUpdateHandler == nil ||
		handlers.MyAPIKeyUpdateHandler == nil ||
		handlers.APIKeyDeleteHandler == nil ||
		handlers.MyAPIKeyDeleteHandler == nil ||
		handlers.GroupListHandler == nil ||
		handlers.MyGroupListHandler == nil ||
		handlers.GroupCreateHandler == nil ||
		handlers.MyGroupCreateHandler == nil ||
		handlers.GroupUpdateHandler == nil ||
		handlers.MyGroupUpdateHandler == nil ||
		handlers.GroupDeleteHandler == nil ||
		handlers.MyGroupDeleteHandler == nil ||
		handlers.GroupOptionsHandler == nil ||
		handlers.MyGroupOptionsHandler == nil ||
		handlers.GroupAccountOptionsHandler == nil ||
		handlers.MyGroupAccountOptionsHandler == nil ||
		handlers.AccountOptionsHandler == nil ||
		handlers.MyAccountOptionsHandler == nil ||
		handlers.AccountTagsHandler == nil ||
		handlers.MyAccountTagsHandler == nil ||
		handlers.AccountTagDeleteHandler == nil ||
		handlers.MyAccountTagDeleteHandler == nil ||
		handlers.AccountTagUpdateHandler == nil ||
		handlers.MyAccountTagUpdateHandler == nil ||
		handlers.SystemSettingsHandler == nil ||
		handlers.SystemSettingsUpdateHandler == nil ||
		handlers.GlobalSettingsHandler == nil ||
		handlers.GlobalSettingsUpdateHandler == nil ||
		handlers.OperationLogsHandler == nil ||
		handlers.MyOperationLogsHandler == nil ||
		handlers.StatsUsageWindowHandler == nil ||
		handlers.MyStatsUsageWindowHandler == nil {
		t.Fatal("newManagementAPIHandler() returned nil middleware or handler while enabled")
	}
}

func TestNewManagementAPIHandlerExplicitlyInjectsAPIKeyMutationDependencies(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := string(source)
	for _, required := range []string{
		"Creator:                  store",
		"Updater:                  store",
		"Deleter:                  store",
		"UsageStatsTimezoneReader: store",
		"Logger:                   logger",
		"APIKeyCreateHandler:",
		"MyAPIKeyCreateHandler:",
		"APIKeyUpdateHandler:",
		"MyAPIKeyUpdateHandler:",
		"APIKeyDeleteHandler:",
		"MyAPIKeyDeleteHandler:",
		"ManagementAPIKeyDeleteHandler:",
		"ManagementMyAPIKeyDeleteHandler:",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("server.go missing explicit API Key mutation wiring %q", required)
		}
	}
}

func TestNewGatewaySystemAccountInvalidatorSkipsOnlyWhenDisabled(t *testing.T) {
	invalidator, closeFn, err := newGatewaySystemAccountInvalidator(t.Context(), config.Config{}, nil)
	if err != nil {
		t.Fatalf("newGatewaySystemAccountInvalidator() disabled error = %v", err)
	}
	closeFn()
	if invalidator != nil {
		t.Fatal("newGatewaySystemAccountInvalidator() returned invalidator while management and public APIs were disabled")
	}

	_, closeFn, err = newGatewaySystemAccountInvalidator(t.Context(), config.Config{
		ManagementAPIEnabled: true,
		RedisNamespace:       "juhe-ai",
	}, &redisplatform.Client{})
	closeFn()
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_CACHE_URL") {
		t.Fatalf("newGatewaySystemAccountInvalidator() error = %v, want management API cache Redis error", err)
	}
}

func TestNewGatewaySystemAccountInvalidatorRequiresCacheForPublicAPI(t *testing.T) {
	_, closeFn, err := newGatewaySystemAccountInvalidator(t.Context(), config.Config{
		PublicAPIEnabled: true,
		RedisNamespace:   "juhe-ai",
	}, &redisplatform.Client{})
	closeFn()
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_CACHE_URL") {
		t.Fatalf("newGatewaySystemAccountInvalidator() error = %v, want public API cache Redis error", err)
	}
}

func TestNewGatewaySystemAccountInvalidatorRequiresStateRedis(t *testing.T) {
	_, closeFn, err := newGatewaySystemAccountInvalidator(t.Context(), config.Config{
		ManagementAPIEnabled: true,
		RedisNamespace:       "juhe-ai",
	}, nil)
	closeFn()
	if err == nil || !strings.Contains(err.Error(), "state redis") {
		t.Fatalf("newGatewaySystemAccountInvalidator() error = %v, want state redis error", err)
	}
}

func TestNewGatewaySystemAccountInvalidatorRejectsInvalidCacheURL(t *testing.T) {
	_, closeFn, err := newGatewaySystemAccountInvalidator(t.Context(), config.Config{
		ManagementAPIEnabled: true,
		RedisCacheURL:        "http://127.0.0.1:6379/0",
		RedisNamespace:       "juhe-ai",
	}, &redisplatform.Client{})
	closeFn()
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_CACHE_URL") {
		t.Fatalf("newGatewaySystemAccountInvalidator() error = %v, want Redis cache URL error", err)
	}
}

type appAccountHealthCheckDispatchCall struct {
	accountID string
	reason    string
}

type appAccountHealthCheckDispatcherRecorder struct {
	calls []appAccountHealthCheckDispatchCall
	err   error
}

func (r *appAccountHealthCheckDispatcherRecorder) Dispatch(
	_ context.Context,
	accountID string,
	reason string,
) error {
	r.calls = append(r.calls, appAccountHealthCheckDispatchCall{
		accountID: accountID,
		reason:    reason,
	})
	return r.err
}

type appProviderModelReaderStub struct{}

func (appProviderModelReaderStub) Models(
	_ context.Context,
	_ managementprovidermodels.ModelListInput,
) ([]managementprovidermodels.ModelCatalogItem, error) {
	return []managementprovidermodels.ModelCatalogItem{{
		ProviderCode: "gpt",
		Model:        "gpt-5.4-mini",
		Status:       "active",
	}}, nil
}

type appPublicAccountStoreFake struct {
	port.PublicAccountStore
}

func (*appPublicAccountStoreFake) FindPublicAccountTargetByUsername(
	_ context.Context,
	username string,
) (port.PublicGroupTarget, bool, error) {
	return port.PublicGroupTarget{
		ID:          "sys_app_wiring",
		Username:    username,
		DisplayName: "App Wiring",
		Status:      "active",
	}, true, nil
}

func (*appPublicAccountStoreFake) FindPublicAccountProviderProfile(
	_ context.Context,
	_ string,
	_ string,
	_ string,
) (port.PublicAccountProviderProfile, bool, error) {
	return port.PublicAccountProviderProfile{
		ID:                      "profile_gpt_openai_v1",
		ProviderCode:            "gpt",
		Enabled:                 true,
		ProviderEnabled:         true,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		AccountTypesJSON:        `["api_key"]`,
		DefaultSupportedModels:  []string{"gpt-5.4-mini"},
		DefaultHealthCheckModel: "gpt-5.4-mini",
	}, true, nil
}

func (*appPublicAccountStoreFake) FindExistingPublicAccountGroupByName(
	_ context.Context,
	_ string,
	_ string,
	name string,
) (port.PublicAccountGroupRef, bool, error) {
	return port.PublicAccountGroupRef{
		ID:              "grp_app_wiring",
		SystemAccountID: "sys_app_wiring",
		Name:            name,
		ProviderCode:    "gpt",
		Enabled:         true,
		GroupType:       "personal",
	}, true, nil
}

func (*appPublicAccountStoreFake) FindExistingPublicAccountByNameInGroup(
	context.Context,
	port.PublicAccountNameLookupInput,
) (port.PublicAccountSummary, bool, error) {
	return port.PublicAccountSummary{}, false, nil
}

func (*appPublicAccountStoreFake) CreatePublicAccount(
	_ context.Context,
	input port.PublicAccountCreateInput,
) (port.PublicAccountSummary, error) {
	groupID := input.GroupID
	groupName := "公开分组"
	return port.PublicAccountSummary{
		ID:                        input.ID,
		SystemAccountID:           input.SystemAccountID,
		Name:                      input.Name,
		ProviderCode:              input.ProviderCode,
		ProviderProtocolProfileID: input.ProviderProtocolProfileID,
		ProtocolCode:              input.ProtocolCode,
		ProtocolVersion:           input.ProtocolVersion,
		Type:                      input.Type,
		Status:                    input.Status,
		CredentialsEncrypted:      input.CredentialsEncrypted,
		CredentialFingerprint:     input.CredentialFingerprint,
		CredentialMask:            input.CredentialMask,
		ClientCompatibility:       input.ClientCompatibility,
		SupportedModels:           input.SupportedModels,
		HealthCheckModel:          input.HealthCheckModel,
		BoundGroupID:              &groupID,
		BoundGroupName:            &groupName,
		Schedulable:               input.Schedulable,
		AvailabilityScheduleJSON:  input.AvailabilityScheduleJSON,
		ConcurrencyLimit:          input.ConcurrencyLimit,
		Priority:                  input.Priority,
		Notes:                     input.Notes,
		CreatedAt:                 input.Now,
		UpdatedAt:                 input.Now,
	}, nil
}
