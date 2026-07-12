package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/publicaccounts"
	publicapicatalog "juhe-ai/backend-go/internal/modules/publicapi"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
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
		2*time.Second,
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

func TestNewPublicAccountHealthCheckDispatcherTrimsSecretForNodeSignature(t *testing.T) {
	const trimmedSecret = "node-runtime-trimmed-secret-0123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		if got, want := request.Header.Get("X-Juhe-AI-Signature"), appNodeDispatchSignature(
			trimmedSecret,
			body,
		); got != want {
			http.Error(w, "signature mismatch", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	dispatcher, err := newPublicAccountHealthCheckDispatcher(config.Config{
		Secret:                     " \t" + trimmedSecret + "\r\n ",
		NodeInternalBaseURL:        server.URL,
		NodeInternalRequestTimeout: 2 * time.Second,
	}, nil)
	if err != nil {
		t.Fatalf("newPublicAccountHealthCheckDispatcher() error = %v", err)
	}
	if err := dispatcher.Dispatch(t.Context(), "acc_trimmed_secret", "activation"); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
}

func TestNewPublicAPIHandlersPassesAccountServiceOptionsToFactory(t *testing.T) {
	const credentialSecret = "  public-account-credential-secret  "
	const dispatchTimeout = 5 * time.Second
	dispatcher := &appAccountHealthCheckDispatcherRecorder{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var captured publicaccounts.Options
	factoryCalls := 0

	handlers, err := newPublicAPIHandlers(
		nil,
		credentialSecret,
		nil,
		dispatcher,
		logger,
		dispatchTimeout,
		func(opts publicaccounts.Options) *publicaccounts.Service {
			factoryCalls++
			captured = opts
			return publicaccounts.NewService(opts)
		},
	)
	if err != nil {
		t.Fatalf("newPublicAPIHandlers() error = %v", err)
	}
	if len(handlers) != len(publicapicatalog.Endpoints()) {
		t.Fatalf("handlers = %d, want %d", len(handlers), len(publicapicatalog.Endpoints()))
	}
	if factoryCalls != 1 {
		t.Fatalf("account service factory calls = %d, want 1", factoryCalls)
	}
	if captured.HealthCheckDispatcher != dispatcher {
		t.Fatalf("HealthCheckDispatcher = %T, want injected recorder", captured.HealthCheckDispatcher)
	}
	if captured.Logger != logger {
		t.Fatalf("Logger = %p, want %p", captured.Logger, logger)
	}
	if captured.HealthCheckDispatchTimeout != dispatchTimeout {
		t.Fatalf(
			"HealthCheckDispatchTimeout = %s, want %s",
			captured.HealthCheckDispatchTimeout,
			dispatchTimeout,
		)
	}
	if captured.Secret != credentialSecret {
		t.Fatalf("Secret = %q, want exact credential secret %q", captured.Secret, credentialSecret)
	}
}

func TestNewManagementAPIHandlerDisabledSkipsRuntimeDependencies(t *testing.T) {
	handlers := newManagementAPIHandler(config.Config{}, nil, nil, nil, nil, nil, nil, nil)
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
		handlers.RouteStrategyListHandler != nil ||
		handlers.MyRouteStrategyListHandler != nil ||
		handlers.RouteStrategyDetailHandler != nil ||
		handlers.MyRouteStrategyDetailHandler != nil ||
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
	handlers := newManagementAPIHandler(config.Config{ManagementAuthSessionsEnabled: true}, nil, nil, nil, nil, nil, nil, nil)
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
		handlers.RouteStrategyListHandler != nil ||
		handlers.MyRouteStrategyListHandler != nil ||
		handlers.RouteStrategyDetailHandler != nil ||
		handlers.MyRouteStrategyDetailHandler != nil ||
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
	handlers := newManagementAPIHandler(config.Config{ManagementAPIEnabled: true}, nil, nil, nil, nil, nil, nil, nil)
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
		handlers.RouteStrategyListHandler == nil ||
		handlers.MyRouteStrategyListHandler == nil ||
		handlers.RouteStrategyDetailHandler == nil ||
		handlers.MyRouteStrategyDetailHandler == nil ||
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

func appNodeDispatchSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("juhe-ai:account-health-check-dispatch:v1\n"))
	_, _ = mac.Write(body)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}
