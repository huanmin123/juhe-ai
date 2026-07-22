package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/accountpagedata"
	"juhe-ai/backend-go/internal/modules/publicaccounts"
	publicapicatalog "juhe-ai/backend-go/internal/modules/publicapi"
	"juhe-ai/backend-go/internal/platform/modelcatalogsnapshotrebuild"
	redisplatform "juhe-ai/backend-go/internal/platform/redis"
)

func TestManagementAPIHandlersDoNotExposeSessionManagement(t *testing.T) {
	typeOfHandlers := reflect.TypeOf(managementAPIHandlers{})
	for _, name := range []string{"SessionListHandler", "SessionRevokeHandler"} {
		if _, ok := typeOfHandlers.FieldByName(name); ok {
			t.Fatalf("managementAPIHandlers still exposes %s", name)
		}
	}
}

func TestNewPublicAPIHandlerDisabledSkipsRuntimeDependencies(t *testing.T) {
	handler, logQueue, err := newPublicAPIHandler(config.Config{}, nil, nil, nil)
	if err != nil {
		t.Fatalf("newPublicAPIHandler() error = %v", err)
	}
	if handler != nil || logQueue != nil {
		t.Fatalf("newPublicAPIHandler() = (%v, %v), want nil handler and queue when disabled", handler, logQueue)
	}
}

func TestRunServerRejectsWorkerOwnerLockRoleBeforeDependencies(t *testing.T) {
	err := RunServer(t.Context(), config.Config{
		Host:                       "127.0.0.1",
		Port:                       3000,
		RedisNamespace:             "owner-lock-test",
		TrustProxy:                 "false",
		NodeInternalRequestTimeout: 2 * time.Second,
		ShutdownTimeout:            time.Second,
		OwnerLockEnabled:           true,
		OwnerLockPath:              filepath.Join(t.TempDir(), "owner.lock"),
		OwnerLockDeploymentEpoch:   "epoch-test",
		OwnerLockRole:              "worker",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "role must be server") {
		t.Fatalf("RunServer() error = %v, want server role error", err)
	}
}

func TestRunServerGooseSchemaGateSourceStructure(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "server.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}

	runServer := findFunction(t, file, "RunServer")
	positions := map[string]token.Pos{
		"ownerlock.Acquire":               token.NoPos,
		"runtimeOwnerLock.Release":        token.NoPos,
		"postgresstore.Open":              token.NoPos,
		"store.Close":                     token.NoPos,
		"store.Ping":                      token.NoPos,
		"store.RequireGooseSchemaVersion": token.NoPos,
		"redisplatform.NewClient":         token.NoPos,
		"newPublicAPIHandlerWithOptions":  token.NoPos,
		"newManagementOperationLogQueue":  token.NoPos,
		"httpapi.NewRouter":               token.NoPos,
		"server.ListenAndServe":           token.NoPos,
	}
	gateCalls := 0
	ast.Inspect(runServer.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callName(call.Fun)
		if _, tracked := positions[name]; tracked && positions[name] == token.NoPos {
			positions[name] = call.Pos()
		}
		if name == "store.RequireGooseSchemaVersion" {
			gateCalls++
		}
		return true
	})

	for _, name := range []string{
		"ownerlock.Acquire",
		"runtimeOwnerLock.Release",
		"postgresstore.Open",
		"store.Close",
		"store.Ping",
		"store.RequireGooseSchemaVersion",
		"redisplatform.NewClient",
		"newPublicAPIHandlerWithOptions",
		"newManagementOperationLogQueue",
		"httpapi.NewRouter",
		"server.ListenAndServe",
	} {
		if positions[name] == token.NoPos {
			t.Fatalf("RunServer missing call %s", name)
		}
	}
	if gateCalls != 1 {
		t.Fatalf("schema gate calls = %d, want 1", gateCalls)
	}
	deferredCalls := map[string]bool{
		"runtimeOwnerLock.Release": false,
		"store.Close":              false,
	}
	ast.Inspect(runServer.Body, func(node ast.Node) bool {
		deferStatement, ok := node.(*ast.DeferStmt)
		if !ok {
			return true
		}
		ast.Inspect(deferStatement.Call, func(deferredNode ast.Node) bool {
			call, ok := deferredNode.(*ast.CallExpr)
			if !ok {
				return true
			}
			if _, tracked := deferredCalls[callName(call.Fun)]; tracked {
				deferredCalls[callName(call.Fun)] = true
			}
			return true
		})
		return true
	})
	for name, deferred := range deferredCalls {
		if !deferred {
			t.Fatalf("RunServer AST must register %s with defer", name)
		}
	}
	gate := positions["store.RequireGooseSchemaVersion"]
	for _, name := range []string{
		"ownerlock.Acquire",
		"runtimeOwnerLock.Release",
		"postgresstore.Open",
		"store.Close",
		"store.Ping",
	} {
		if positions[name] >= gate {
			t.Fatalf("%s must appear before schema gate in RunServer", name)
		}
	}
	for _, name := range []string{
		"redisplatform.NewClient",
		"newPublicAPIHandlerWithOptions",
		"newManagementOperationLogQueue",
		"httpapi.NewRouter",
		"server.ListenAndServe",
	} {
		if positions[name] <= gate {
			t.Fatalf("%s must appear after schema gate in RunServer", name)
		}
	}

	ownerConditionedGate := false
	ast.Inspect(runServer.Body, func(node ast.Node) bool {
		ifStatement, ok := node.(*ast.IfStmt)
		if !ok || !isOwnerLockEnabledSelector(ifStatement.Cond) {
			return true
		}
		ast.Inspect(ifStatement.Body, func(bodyNode ast.Node) bool {
			call, ok := bodyNode.(*ast.CallExpr)
			if ok && callName(call.Fun) == "store.RequireGooseSchemaVersion" {
				ownerConditionedGate = true
			}
			return true
		})
		return true
	})
	if !ownerConditionedGate {
		t.Fatal("schema gate must be inside cfg.OwnerLockEnabled condition")
	}
}

func TestGooseSchemaVersionGateTimeout(t *testing.T) {
	if gooseSchemaVersionGateTimeout != 5*time.Second {
		t.Fatalf("goose schema version gate timeout = %s, want 5s", gooseSchemaVersionGateTimeout)
	}
}

func findFunction(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == name {
			return function
		}
	}
	t.Fatalf("function %s not found", name)
	return nil
}

func callName(expression ast.Expr) string {
	if identifier, ok := expression.(*ast.Ident); ok {
		return identifier.Name
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return receiver.Name + "." + selector.Sel.Name
}

func isOwnerLockEnabledSelector(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "OwnerLockEnabled" {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	return ok && receiver.Name == "cfg"
}

func TestNewManagementAPIHandlerInjectsRuntimeLogIndexEnabled(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "server.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}

	function := findFunction(t, file, "newManagementAPIHandlerWithCatalogSnapshotRebuilder")
	found := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || callName(call.Fun) != "httpapi.NewManagementRuntimeLogsHandler" {
			return true
		}
		if len(call.Args) != 2 {
			t.Fatalf("NewManagementRuntimeLogsHandler args = %d, want 2", len(call.Args))
		}
		selector, ok := call.Args[1].(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "RuntimeLogIndexEnabled" {
			t.Fatalf("runtime log index option = %#v, want cfg.RuntimeLogIndexEnabled", call.Args[1])
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || receiver.Name != "cfg" {
			t.Fatalf("runtime log index option receiver = %#v, want cfg", selector.X)
		}
		found = true
		return true
	})
	if !found {
		t.Fatal("newManagementAPIHandlerWithCatalogSnapshotRebuilder missing runtime log handler assembly")
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

func TestNewManagementCatalogSnapshotRebuilderRequiresSafeNodeBridge(t *testing.T) {
	rebuilder, err := newManagementCatalogSnapshotRebuilder(config.Config{})
	if err != nil || rebuilder != nil {
		t.Fatalf("disabled rebuilder = %T, err = %v", rebuilder, err)
	}
	if _, err := newManagementCatalogSnapshotRebuilder(config.Config{ManagementAPIEnabled: true, Secret: "secret"}); err == nil {
		t.Fatal("enabled rebuilder without Node URL error = nil")
	}
	rebuilder, err = newManagementCatalogSnapshotRebuilder(config.Config{
		ManagementAPIEnabled:       true,
		NodeInternalBaseURL:        "http://127.0.0.1:3001",
		NodeInternalRequestTimeout: 2 * time.Second,
		Secret:                     "secret",
	})
	if err != nil || rebuilder == nil {
		t.Fatalf("enabled rebuilder = %T, err = %v", rebuilder, err)
	}
}

func TestProbeManagementCatalogSnapshotBridge(t *testing.T) {
	if err := probeManagementCatalogSnapshotBridge(t.Context(), config.Config{}, nil); err != nil {
		t.Fatalf("disabled probe error = %v", err)
	}

	success := &managementCatalogSnapshotBridgeStub{}
	if err := probeManagementCatalogSnapshotBridge(t.Context(), config.Config{
		NodeInternalRequestTimeout: time.Second,
	}, success); err != nil {
		t.Fatalf("success probe error = %v", err)
	}
	if success.probeCalls != 1 {
		t.Fatalf("success probe calls = %d, want 1", success.probeCalls)
	}

	typedFailure := &managementCatalogSnapshotBridgeStub{
		probeError: &modelcatalogsnapshotrebuild.ProbeError{Kind: modelcatalogsnapshotrebuild.ProbeFailureUnauthorized},
	}
	err := probeManagementCatalogSnapshotBridge(t.Context(), config.Config{
		NodeInternalRequestTimeout: time.Second,
	}, typedFailure)
	if err == nil || !strings.Contains(err.Error(), string(modelcatalogsnapshotrebuild.ProbeFailureUnauthorized)) {
		t.Fatalf("typed failure error = %v", err)
	}

	genericFailure := &managementCatalogSnapshotBridgeStub{
		probeError: errors.New("http://127.0.0.1:3001 private response body"),
	}
	err = probeManagementCatalogSnapshotBridge(t.Context(), config.Config{
		NodeInternalRequestTimeout: time.Second,
	}, genericFailure)
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "private response body") {
		t.Fatalf("generic failure leaked details: %v", err)
	}
}

func TestProbeManagementCatalogSnapshotBridgeUsesRequestTimeout(t *testing.T) {
	bridge := &managementCatalogSnapshotBridgeStub{waitForContext: true}
	started := time.Now()
	err := probeManagementCatalogSnapshotBridge(t.Context(), config.Config{
		NodeInternalRequestTimeout: 100 * time.Millisecond,
	}, bridge)
	if err == nil {
		t.Fatal("timeout probe error = nil")
	}
	if elapsed := time.Since(started); elapsed < 80*time.Millisecond || elapsed > time.Second {
		t.Fatalf("timeout elapsed = %s", elapsed)
	}
	if bridge.probeCalls != 1 {
		t.Fatalf("timeout probe calls = %d, want 1", bridge.probeCalls)
	}
}

func TestRunServerProbesManagementCatalogBridgeBeforeRouterAndListen(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "server.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse server.go: %v", err)
	}
	runServer := findFunction(t, file, "RunServer")
	positions := map[string]token.Pos{
		"newManagementCatalogSnapshotRebuilder":               token.NoPos,
		"probeManagementCatalogSnapshotBridge":                token.NoPos,
		"newManagementAPIHandlerWithCatalogSnapshotRebuilder": token.NoPos,
		"httpapi.NewRouter":                                   token.NoPos,
		"server.ListenAndServe":                               token.NoPos,
	}
	ast.Inspect(runServer.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callName(call.Fun)
		if _, tracked := positions[name]; tracked && positions[name] == token.NoPos {
			positions[name] = call.Pos()
		}
		return true
	})
	for name, position := range positions {
		if position == token.NoPos {
			t.Fatalf("RunServer missing call %s", name)
		}
	}
	probe := positions["probeManagementCatalogSnapshotBridge"]
	if positions["newManagementCatalogSnapshotRebuilder"] >= probe {
		t.Fatal("bridge construction must precede startup probe")
	}
	for _, name := range []string{
		"newManagementAPIHandlerWithCatalogSnapshotRebuilder",
		"httpapi.NewRouter",
		"server.ListenAndServe",
	} {
		if positions[name] <= probe {
			t.Fatalf("%s must follow startup probe", name)
		}
	}
}

type managementCatalogSnapshotBridgeStub struct {
	probeCalls     int
	probeError     error
	waitForContext bool
}

func (s *managementCatalogSnapshotBridgeStub) Rebuild(context.Context, string, string) error {
	return nil
}

func (s *managementCatalogSnapshotBridgeStub) Probe(ctx context.Context) error {
	s.probeCalls++
	if s.waitForContext {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.probeError
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
	privateBaseURLAllowlist := []string{"http://192.168.40.199:8317"}
	dispatcher := &appAccountHealthCheckDispatcherRecorder{}
	pageDataPublisher := &appAccountPageDataPublisherStub{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var captured publicaccounts.Options
	factoryCalls := 0

	handlers, err := newPublicAPIHandlers(
		nil,
		credentialSecret,
		privateBaseURLAllowlist,
		nil,
		dispatcher,
		pageDataPublisher,
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
	if captured.PageDataPublisher != pageDataPublisher {
		t.Fatalf("PageDataPublisher = %T, want injected recorder", captured.PageDataPublisher)
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
	if len(captured.PrivateBaseURLAllowlist) != 1 || captured.PrivateBaseURLAllowlist[0] != privateBaseURLAllowlist[0] {
		t.Fatalf("PrivateBaseURLAllowlist = %v, want %v", captured.PrivateBaseURLAllowlist, privateBaseURLAllowlist)
	}
}

type appAccountPageDataPublisherStub struct{}

func (*appAccountPageDataPublisherStub) PublishAccountStaticChange(context.Context, accountpagedata.ChangeInput) error {
	return nil
}

func (*appAccountPageDataPublisherStub) PublishAccountRuntimeChange(context.Context, accountpagedata.ChangeInput) error {
	return nil
}

func TestNewManagementAPIHandlerDisabledSkipsRuntimeDependencies(t *testing.T) {
	handlers := newManagementAPIHandlerWithPageData(config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil)
	if handlers.AuthMiddleware != nil ||
		handlers.AuthTouchMiddleware != nil ||
		handlers.CaptchaHandler != nil ||
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
		handlers.RouteStrategyUpdateHandler != nil ||
		handlers.MyRouteStrategyUpdateHandler != nil ||
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
		handlers.ClientIPStatsHandler != nil ||
		handlers.ClientIPStatsDetailHandler != nil ||
		handlers.ClientIPAllowlistHandler != nil ||
		handlers.ClientIPUnallowlistHandler != nil ||
		handlers.ClientIPBlacklistHandler != nil ||
		handlers.ClientIPUnblockHandler != nil ||
		handlers.OperationLogsHandler != nil ||
		handlers.MyOperationLogsHandler != nil ||
		handlers.TableMonitorHandler != nil ||
		handlers.ExternalIntegrationSourceListHandler != nil ||
		handlers.ExternalIntegrationSourceDetailHandler != nil ||
		handlers.ExternalSourceTokenCreateHandler != nil ||
		handlers.ExternalIntegrationSourceScopesHandler != nil ||
		handlers.ExternalIntegrationSourceAPIDocsHandler != nil ||
		handlers.StatsUsageWindowHandler != nil ||
		handlers.MyStatsUsageWindowHandler != nil {
		t.Fatal("newManagementAPIHandler() returned middleware or handler while disabled")
	}
}

func TestNewManagementAPIHandlerEnabledReturnsAuthAndManagementOptionsHandlers(t *testing.T) {
	handlers := newManagementAPIHandlerWithPageData(config.Config{ManagementAPIEnabled: true}, nil, nil, nil, nil, nil, nil, nil, nil)
	if handlers.AuthMiddleware == nil ||
		handlers.AuthTouchMiddleware == nil ||
		handlers.CaptchaHandler == nil ||
		handlers.LoginHandler == nil ||
		handlers.CurrentUserHandler == nil ||
		handlers.ProfileUpdateHandler == nil ||
		handlers.PasswordChangeHandler == nil ||
		handlers.LogoutHandler == nil ||
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
		handlers.RouteStrategyUpdateHandler == nil ||
		handlers.MyRouteStrategyUpdateHandler == nil ||
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
		handlers.ClientIPStatsHandler == nil ||
		handlers.ClientIPStatsDetailHandler == nil ||
		handlers.ClientIPAllowlistHandler == nil ||
		handlers.ClientIPUnallowlistHandler == nil ||
		handlers.ClientIPBlacklistHandler == nil ||
		handlers.ClientIPUnblockHandler == nil ||
		handlers.OperationLogsHandler == nil ||
		handlers.MyOperationLogsHandler == nil ||
		handlers.TableMonitorHandler == nil ||
		handlers.ExternalIntegrationSourceListHandler == nil ||
		handlers.ExternalIntegrationSourceDetailHandler == nil ||
		handlers.ExternalIntegrationSourceCreateHandler == nil ||
		handlers.ExternalIntegrationSourceUpdateHandler == nil ||
		handlers.ExternalIntegrationSourceDeleteHandler == nil ||
		handlers.ExternalSourceTokenCreateHandler == nil ||
		handlers.ExternalIntegrationSourceScopesHandler == nil ||
		handlers.ExternalIntegrationSourceAPIDocsHandler == nil ||
		handlers.StatsUsageWindowHandler == nil ||
		handlers.MyStatsUsageWindowHandler == nil {
		t.Fatal("newManagementAPIHandler() returned nil middleware or handler while enabled")
	}
}

func TestNewManagementAPIHandlerExternalIntegrationSourceHandlersOptIn(t *testing.T) {
	disabled := newManagementAPIHandlerWithPageData(config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil)
	if disabled.ExternalIntegrationSourceListHandler != nil ||
		disabled.ExternalIntegrationSourceDetailHandler != nil ||
		disabled.ExternalIntegrationSourceCreateHandler != nil ||
		disabled.ExternalIntegrationSourceUpdateHandler != nil ||
		disabled.ExternalIntegrationSourceDeleteHandler != nil ||
		disabled.ExternalSourceTokenCreateHandler != nil ||
		disabled.ExternalSourceTokenSecretHandler != nil ||
		disabled.ExternalIntegrationSourceScopesHandler != nil ||
		disabled.ExternalIntegrationSourceAPIDocsHandler != nil {
		t.Fatal("external integration source handler was created while management API disabled")
	}

	enabled := newManagementAPIHandlerWithPageData(
		config.Config{ManagementAPIEnabled: true},
		nil,
		nil,
		nil,
		nil,
		nil, nil,

		nil,
		nil)

	if enabled.ExternalIntegrationSourceListHandler == nil ||
		enabled.ExternalIntegrationSourceDetailHandler == nil ||
		enabled.ExternalIntegrationSourceCreateHandler == nil ||
		enabled.ExternalIntegrationSourceUpdateHandler == nil ||
		enabled.ExternalIntegrationSourceDeleteHandler == nil ||
		enabled.ExternalSourceTokenCreateHandler == nil ||
		enabled.ExternalSourceTokenSecretHandler == nil ||
		enabled.ExternalIntegrationSourceScopesHandler == nil ||
		enabled.ExternalIntegrationSourceAPIDocsHandler == nil {
		t.Fatal("external integration source handler was not created while management API enabled")
	}

	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := string(source)
	for _, required := range []string{
		"managementexternalintegrationsources.NewServiceWithOptions(",
		"ListReader:   store",
		"DetailReader: store",
		"SecretReader: store",
		"Secret:       cfg.Secret",
		"ManagementExternalIntegrationSourceListHandler:",
		"managementHandlers.ExternalIntegrationSourceListHandler",
		"httpapi.NewManagementExternalIntegrationSourceListHandler(externalIntegrationSourceService)",
		"ManagementExternalIntegrationSourceDetailHandler:",
		"managementHandlers.ExternalIntegrationSourceDetailHandler",
		"httpapi.NewManagementExternalIntegrationSourceDetailHandler(externalIntegrationSourceService)",
		"managementexternalintegrationsources.NewCreateService(store, cfg.Secret)",
		"ManagementExternalIntegrationSourceCreateHandler:",
		"managementHandlers.ExternalIntegrationSourceCreateHandler",
		"httpapi.NewManagementExternalIntegrationSourceCreateHandlerWithOperationLog(externalIntegrationSourceCreateService, operationLogOptions)",
		"managementexternalintegrationsources.NewUpdateService(store)",
		"ManagementExternalIntegrationSourceUpdateHandler:",
		"managementHandlers.ExternalIntegrationSourceUpdateHandler",
		"httpapi.NewManagementExternalIntegrationSourceUpdateHandlerWithOperationLog(externalIntegrationSourceUpdateService, operationLogOptions)",
		"managementexternalintegrationsources.NewDeleteService(store)",
		"ManagementExternalIntegrationSourceDeleteHandler:",
		"managementHandlers.ExternalIntegrationSourceDeleteHandler",
		"httpapi.NewManagementExternalIntegrationSourceDeleteHandlerWithOperationLog(externalIntegrationSourceDeleteService, operationLogOptions)",
		"managementexternalintegrationsources.NewTokenCreateService(store, cfg.Secret)",
		"ManagementExternalSourceTokenCreateHandler:",
		"managementHandlers.ExternalSourceTokenCreateHandler",
		"httpapi.NewManagementExternalIntegrationSourceTokenCreateHandlerWithOperationLog(externalIntegrationSourceTokenCreateService, operationLogOptions)",
		"ManagementExternalSourceTokenSecretHandler:",
		"managementHandlers.ExternalSourceTokenSecretHandler",
		"httpapi.NewManagementExternalIntegrationSourceTokenSecretHandler(externalIntegrationSourceService)",
		"ManagementExternalIntegrationSourceScopesHandler:",
		"managementHandlers.ExternalIntegrationSourceScopesHandler",
		"httpapi.NewManagementExternalIntegrationSourceScopesHandler()",
		"ManagementExternalIntegrationSourceAPIDocsHandler:",
		"managementHandlers.ExternalIntegrationSourceAPIDocsHandler",
		"httpapi.NewManagementExternalIntegrationSourceAPIDocsHandler()",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("server.go missing external integration source wiring %q", required)
		}
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

func TestNewManagementAPIHandlerInjectsProviderModelLogger(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	block := sourceBlockBetween(t, string(source),
		"providerModelService := managementprovidermodels.NewServiceWithOptions",
		"routeStrategyService := managementroutestrategies.NewServiceWithOptions",
	)
	if !strings.Contains(block, "Logger:            logger") {
		t.Fatal("server.go must inject the logger into the provider model service")
	}
}

func TestNewManagementAPIHandlerClientIPPolicyOptInAndSharedServiceWiring(t *testing.T) {
	disabled := newManagementAPIHandlerWithPageData(config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil)
	if disabled.ClientIPAllowlistHandler != nil ||
		disabled.ClientIPUnallowlistHandler != nil ||
		disabled.ClientIPBlacklistHandler != nil ||
		disabled.ClientIPUnblockHandler != nil {
		t.Fatal("client IP policy handlers were created while management API disabled")
	}

	enabled := newManagementAPIHandlerWithPageData(
		config.Config{ManagementAPIEnabled: true},
		nil,
		nil,
		nil,
		nil,
		nil, nil,

		nil,
		nil)

	if enabled.ClientIPAllowlistHandler == nil ||
		enabled.ClientIPUnallowlistHandler == nil ||
		enabled.ClientIPBlacklistHandler == nil ||
		enabled.ClientIPUnblockHandler == nil {
		t.Fatal("client IP policy handlers were not created while management API enabled")
	}

	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := string(source)
	for _, required := range []string{
		"ManagementClientIPAllowlistHandler:",
		"managementHandlers.ClientIPAllowlistHandler",
		"ManagementClientIPUnallowlistHandler:",
		"managementHandlers.ClientIPUnallowlistHandler",
		"ManagementClientIPBlacklistHandler:",
		"managementHandlers.ClientIPBlacklistHandler",
		"ManagementClientIPUnblockHandler:",
		"managementHandlers.ClientIPUnblockHandler",
		"Transactor:  store",
		"Invalidator: systemAccountInvalidator",
		"httpapi.NewManagementClientIPAllowlistHandlerWithOperationLog(clientIPPolicyService, operationLogOptions)",
		"httpapi.NewManagementClientIPUnallowlistHandlerWithOperationLog(clientIPPolicyService, operationLogOptions)",
		"httpapi.NewManagementClientIPBlacklistHandlerWithOperationLog(clientIPPolicyService, operationLogOptions)",
		"httpapi.NewManagementClientIPUnblockHandlerWithOperationLog(clientIPPolicyService, operationLogOptions)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("server.go missing client IP policy wiring %q", required)
		}
	}
}

func TestNewManagementAPIHandlerClientIPStatsReadOptInAndWiring(t *testing.T) {
	disabled := newManagementAPIHandlerWithPageData(config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil)
	if disabled.ClientIPStatsHandler != nil || disabled.ClientIPStatsDetailHandler != nil {
		t.Fatal("client IP stats read handler was created while management API disabled")
	}

	enabled := newManagementAPIHandlerWithPageData(
		config.Config{ManagementAPIEnabled: true},
		nil,
		nil,
		nil,
		nil,
		nil, nil,

		nil,
		nil)

	if enabled.ClientIPStatsHandler == nil || enabled.ClientIPStatsDetailHandler == nil {
		t.Fatal("client IP stats read handler was not created while management API enabled")
	}

	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := string(source)
	for _, required := range []string{
		"ManagementClientIPStatsHandler:",
		"ManagementClientIPStatsDetailHandler:",
		"managementHandlers.ClientIPStatsHandler",
		"managementHandlers.ClientIPStatsDetailHandler",
		"managementclientipstats.NewServiceWithOptions(",
		"ListReader:               store",
		"RegistryReader:           store",
		"DetailReader:             store",
		"UsageStatsTimezoneReader: store",
		"httpapi.NewManagementClientIPStatsHandler(clientIPStatsService)",
		"httpapi.NewManagementClientIPStatsDetailHandler(clientIPStatsService)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("server.go missing client IP stats read wiring %q", required)
		}
	}
}

func TestNewManagementAPIHandlerRouteStrategyDeleteOptInAndSharedServiceWiring(t *testing.T) {
	disabled := newManagementAPIHandlerWithPageData(config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil)
	if disabled.RouteStrategyDeleteHandler != nil ||
		disabled.MyRouteStrategyDeleteHandler != nil {
		t.Fatal("route strategy delete handlers were created while management API disabled")
	}

	enabled := newManagementAPIHandlerWithPageData(
		config.Config{ManagementAPIEnabled: true},
		nil,
		nil,
		nil,
		nil,
		nil, nil,

		nil,
		nil)

	if enabled.RouteStrategyDeleteHandler == nil ||
		enabled.MyRouteStrategyDeleteHandler == nil {
		t.Fatal("route strategy delete handlers were not created while management API enabled")
	}

	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := string(source)
	for _, required := range []string{
		"ManagementRouteStrategyDeleteHandler:",
		"managementHandlers.RouteStrategyDeleteHandler",
		"ManagementMyRouteStrategyDeleteHandler:",
		"managementHandlers.MyRouteStrategyDeleteHandler",
		"RouteStrategyDeleteHandler:",
		"httpapi.NewManagementRouteStrategyDeleteHandlerWithOperationLog(routeStrategyService, operationLogOptions)",
		"MyRouteStrategyDeleteHandler:",
		"httpapi.NewManagementMyRouteStrategyDeleteHandlerWithOperationLog(routeStrategyService, operationLogOptions)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("server.go missing shared route strategy delete wiring %q", required)
		}
	}
}

func TestNewGatewaySystemAccountInvalidatorSkipsOnlyWhenDisabled(t *testing.T) {
	invalidator, _, closeFn, err := newGatewaySystemAccountInvalidator(t.Context(), config.Config{}, nil)
	if err != nil {
		t.Fatalf("newGatewaySystemAccountInvalidator() disabled error = %v", err)
	}
	closeFn()
	if invalidator != nil {
		t.Fatal("newGatewaySystemAccountInvalidator() returned invalidator while management and public APIs were disabled")
	}

	_, _, closeFn, err = newGatewaySystemAccountInvalidator(t.Context(), config.Config{
		ManagementAPIEnabled: true,
		RedisNamespace:       "juhe-ai",
	}, &redisplatform.Client{})
	closeFn()
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_CACHE_URL") {
		t.Fatalf("newGatewaySystemAccountInvalidator() error = %v, want management API cache Redis error", err)
	}
}

func TestNewGatewaySystemAccountInvalidatorRequiresCacheForPublicAPI(t *testing.T) {
	_, _, closeFn, err := newGatewaySystemAccountInvalidator(t.Context(), config.Config{
		PublicAPIEnabled: true,
		RedisNamespace:   "juhe-ai",
	}, &redisplatform.Client{})
	closeFn()
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_CACHE_URL") {
		t.Fatalf("newGatewaySystemAccountInvalidator() error = %v, want public API cache Redis error", err)
	}
}

func TestNewGatewaySystemAccountInvalidatorRequiresStateRedis(t *testing.T) {
	_, _, closeFn, err := newGatewaySystemAccountInvalidator(t.Context(), config.Config{
		ManagementAPIEnabled: true,
		RedisNamespace:       "juhe-ai",
	}, nil)
	closeFn()
	if err == nil || !strings.Contains(err.Error(), "state redis") {
		t.Fatalf("newGatewaySystemAccountInvalidator() error = %v, want state redis error", err)
	}
}

func TestNewGatewaySystemAccountInvalidatorRejectsInvalidCacheURL(t *testing.T) {
	_, _, closeFn, err := newGatewaySystemAccountInvalidator(t.Context(), config.Config{
		ManagementAPIEnabled: true,
		RedisCacheURL:        "http://127.0.0.1:6379/0",
		RedisNamespace:       "juhe-ai",
	}, &redisplatform.Client{})
	closeFn()
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_REDIS_CACHE_URL") {
		t.Fatalf("newGatewaySystemAccountInvalidator() error = %v, want Redis cache URL error", err)
	}
}

func TestRunServerBuildsSystemAPICacheReadersWhenCacheRedisIsReused(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("read server.go: %v", err)
	}
	text := strings.ReplaceAll(string(source), "\r\n", "\n")
	clientCreation := strings.Index(text, "if systemAPIClientIPAllowlistCacheRedis == nil && cfg.RedisCacheURL != \"\"")
	if clientCreation < 0 {
		t.Fatal("server must retain the optional cache client creation branch")
	}
	allowlistReader := strings.Index(text, "httpapi.NewRedisSystemAPIClientIPAllowlistVersionReader(")
	rateLimitReader := strings.Index(text, "httpapi.NewRedisSystemAPIRateLimitSettingsVersionReader(")
	if allowlistReader < 0 || rateLimitReader < 0 {
		t.Fatal("server must construct both system API cache readers")
	}
	branchEnd := strings.Index(text[clientCreation:], "\n\t}\n")
	if branchEnd < 0 {
		t.Fatal("cache client creation branch must have a clear boundary")
	}
	branchEnd += clientCreation
	if allowlistReader < branchEnd || rateLimitReader < branchEnd {
		t.Fatal("system API cache readers must be constructed after the optional client creation branch")
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
