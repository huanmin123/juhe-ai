//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"juhe-ai/backend-go/internal/app"
	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/platform/modelcatalogsnapshotrebuild"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w3NodeBridgeReadyPrefix = "JUHE_AI_MODEL_CATALOG_BRIDGE_READY "
	w3NodeBridgeSecret      = "w3-node-bridge-secret-0123456789abcdef"
	w3NodeBridgeOutputLimit = 64 * 1024
)

func TestW3ManagementProviderModelSnapshotNodeBridgePostgresRedisSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	nodePath, backendDir, helperPath := w3ModelCatalogNodeBridgePrerequisites(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	postgresContainer, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateW3NodeBridgeContainer(t, postgresContainer)
	postgresURL, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}

	redisCacheContainer := startW3NodeBridgeRedis(t, ctx, "cache")
	defer terminateW3NodeBridgeContainer(t, redisCacheContainer)
	redisStateContainer := startW3NodeBridgeRedis(t, ctx, "state")
	defer terminateW3NodeBridgeContainer(t, redisStateContainer)
	redisQueueContainer := startW3NodeBridgeRedis(t, ctx, "queue")
	defer terminateW3NodeBridgeContainer(t, redisQueueContainer)
	redisCacheURL, err := redisCacheContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis cache connection string: %v", err)
	}
	redisStateURL, err := redisStateContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis state connection string: %v", err)
	}
	redisQueueURL, err := redisQueueContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("redis queue connection string: %v", err)
	}

	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)
	now := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	insertW2ProxyOptionsFixture(t, ctx, db, now)
	sessionToken := "w3-model-catalog-node-bridge-session-token"
	insertW2ManagementSessionFixture(t, ctx, db, sessionToken, now)

	bridge := startW3ModelCatalogNodeBridge(t, ctx, nodePath, backendDir, helperPath, postgresURL, redisCacheURL, redisStateURL, redisQueueURL)
	defer bridge.Close(t)
	client, err := modelcatalogsnapshotrebuild.NewClientWithTimeouts(bridge.BaseURL(), w3NodeBridgeSecret, 60*time.Second, 2*time.Second)
	if err != nil {
		t.Fatalf("create Go model catalog snapshot client: %v", err)
	}
	if err := client.Probe(ctx); err != nil {
		t.Fatalf("probe real Node model catalog bridge: %v", err)
	}
	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	defer store.Close()
	service := managementprovidermodels.NewServiceWithOptions(managementprovidermodels.ServiceOptions{
		Store:            store,
		CatalogRebuilder: client,
	})
	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{
		Store: store,
		Now:   func() time.Time { return now },
	})
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config:                           config.Config{Host: "127.0.0.1", Port: 3000, ManagementAPIEnabled: true},
		Logger:                           slog.Default(),
		ManagementAPIAuthMiddleware:      httpapi.NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: httpapi.NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementProviderCustomModelCreateHandler: httpapi.NewManagementProviderCustomModelCreateHandler(service),
	})

	rec := serveW3ProviderModelCRUDRequest(router, http.MethodPost, "/__aisys__/api/providers/gpt/models?systemAccountId=sys_w2_proxy_options", sessionToken, `{
		"model":"w3-node-bridge-model",
		"scope":"global",
		"catalogVisible":true,
		"mode":"text",
		"supportedApiProtocols":["responses","chat_completions"],
		"inputUsdPer1M":1,
		"outputUsdPer1M":2
	}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("global create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	assertW3NodeBridgeDirtyAcknowledged(t, ctx, db)
	assertW3NodeBridgeGlobalSnapshots(t, ctx, db, "w3-node-bridge-model")
	assertW3RealGoServerBridgeReadiness(t, ctx, bridge.BaseURL(), postgresURL, redisCacheURL, redisStateURL, redisQueueURL)
}

type w3ModelCatalogNodeBridgeProcess struct {
	command  *exec.Cmd
	stdin    io.WriteCloser
	wait     <-chan error
	port     int
	stdout   *w6ManagementClientIPStatsBoundedOutput
	stderr   *w6ManagementClientIPStatsBoundedOutput
	redacted []string
}

func w3ModelCatalogNodeBridgePrerequisites(t *testing.T) (string, string, string) {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		w6ManagementClientIPStatsNodeWriterDependencyUnavailable(t, "node executable is unavailable: %v", err)
		return "", "", ""
	}
	nodePath, err = filepath.Abs(nodePath)
	if err != nil {
		t.Fatalf("resolve node path: %v", err)
	}
	backendDir := filepath.Join(filepath.Dir(repoRoot(t)), "backend")
	helperPath := filepath.Join(backendDir, "src", "scripts", "regression", "model-catalog-snapshot-real-node-server.ts")
	if info, statErr := os.Stat(helperPath); statErr != nil {
		t.Fatalf("stat Node model catalog bridge helper: %v", statErr)
	} else if info.IsDir() {
		t.Fatal("Node model catalog bridge helper path is a directory")
	}
	probeCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	probe := exec.CommandContext(probeCtx, nodePath, "--import", "tsx", "--eval", "")
	probe.Dir = backendDir
	probe.Env = w6ManagementClientIPStatsNodeWriterBaseEnvironment()
	probeStdout := newW6ManagementClientIPStatsBoundedOutput(w3NodeBridgeOutputLimit)
	probeStderr := newW6ManagementClientIPStatsBoundedOutput(w3NodeBridgeOutputLimit)
	probe.Stdout = probeStdout
	probe.Stderr = probeStderr
	if probeErr := probe.Run(); probeErr != nil {
		w6ManagementClientIPStatsNodeWriterDependencyUnavailable(t, "tsx loader unavailable: %v; stdout=%q stderr=%q", probeErr, probeStdout.String(), probeStderr.String())
		return "", "", ""
	}
	if probeStdout.Truncated() || probeStderr.Truncated() {
		w6ManagementClientIPStatsNodeWriterDependencyUnavailable(t, "tsx loader probe output exceeded bounded capture")
		return "", "", ""
	}
	return nodePath, backendDir, helperPath
}

func startW3ModelCatalogNodeBridge(
	t *testing.T,
	ctx context.Context,
	nodePath string,
	backendDir string,
	helperPath string,
	postgresURL string,
	redisCacheURL string,
	redisStateURL string,
	redisQueueURL string,
) *w3ModelCatalogNodeBridgeProcess {
	t.Helper()
	port := w3ReserveLoopbackPort(t)
	command := exec.CommandContext(ctx, nodePath, "--import", "tsx", helperPath)
	command.Dir = backendDir
	command.Env = append(w6ManagementClientIPStatsNodeWriterBaseEnvironment(),
		"NODE_ENV=test",
		"JUHE_AI_RUNTIME_MODE=performance",
		"JUHE_AI_DATABASE_DRIVER=postgres",
		"JUHE_AI_CACHE_DRIVER=redis",
		"JUHE_AI_RUNTIME_STATE_DRIVER=redis",
		"JUHE_AI_QUEUE_DRIVER=redis_stream",
		"JUHE_AI_HOST=127.0.0.1",
		"JUHE_AI_PORT="+strconv.Itoa(port),
		"JUHE_AI_POSTGRES_URL="+postgresURL,
		"JUHE_AI_REDIS_CACHE_URL="+redisCacheURL,
		"JUHE_AI_REDIS_STATE_URL="+redisStateURL,
		"JUHE_AI_REDIS_QUEUE_URL="+redisQueueURL,
		"JUHE_AI_PROCESS_ROLE=worker",
		"JUHE_AI_WORKER_ROLE=ingest-worker",
		"JUHE_AI_OWNER_LOCK_ENABLED=true",
		"JUHE_AI_SECRET="+w3NodeBridgeSecret,
		"JUHE_AI_LOG_FILE_ENABLED=false",
		"JUHE_AI_LOG_CONSOLE_ENABLED=false",
	)
	stdout := newW6ManagementClientIPStatsBoundedOutput(w3NodeBridgeOutputLimit)
	stderr := newW6ManagementClientIPStatsBoundedOutput(w3NodeBridgeOutputLimit)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("create Node bridge stdin: %v", err)
	}
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = 5 * time.Second
	if err := command.Start(); err != nil {
		t.Fatalf("start Node model catalog bridge: %v", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	process := &w3ModelCatalogNodeBridgeProcess{
		command: command, stdin: stdin, wait: wait, stdout: stdout, stderr: stderr,
		port: port, redacted: []string{postgresURL, redisCacheURL, redisStateURL, redisQueueURL},
	}
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if readyPort, ok := w3NodeBridgeReadyPort(stdout.String()); ok && readyPort == port {
			client, clientErr := modelcatalogsnapshotrebuild.NewClientWithTimeouts(process.BaseURL(), w3NodeBridgeSecret, 60*time.Second, 2*time.Second)
			if clientErr != nil {
				t.Fatalf("create Node readiness client: %v", clientErr)
			}
			if probeErr := client.Probe(ctx); probeErr == nil {
				return process
			}
		}
		select {
		case err := <-wait:
			t.Fatalf("Node model catalog bridge exited before ready: %v; stdout=%q stderr=%q", err, process.safeOutput(stdout.String()), process.safeOutput(stderr.String()))
		case <-deadline.C:
			_ = command.Process.Kill()
			select {
			case <-wait:
			case <-time.After(5 * time.Second):
			}
			t.Fatalf("Node model catalog bridge ready timeout; stdout=%q stderr=%q", process.safeOutput(stdout.String()), process.safeOutput(stderr.String()))
		case <-ticker.C:
		}
	}
}

func w3ReserveLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback port reservation: %v", err)
	}
	return port
}

func assertW3RealGoServerBridgeReadiness(
	t *testing.T,
	ctx context.Context,
	nodeBaseURL string,
	postgresURL string,
	redisCacheURL string,
	redisStateURL string,
	redisQueueURL string,
) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	goPort := w3ReserveLoopbackPort(t)
	cfg := w3RealGoServerConfig(goPort, nodeBaseURL, postgresURL, redisCacheURL, redisStateURL, redisQueueURL, w3NodeBridgeSecret)
	serverCtx, cancelServer := context.WithCancel(ctx)
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = app.RunServer(serverCtx, cfg, logger)
		close(done)
	}()
	var stopServerOnce sync.Once
	var serverStopErr error
	stopServer := func() {
		stopServerOnce.Do(func() {
			cancelServer()
			select {
			case <-done:
				serverStopErr = runErr
			case <-time.After(10 * time.Second):
				serverStopErr = errors.New("shutdown timeout")
			}
		})
	}
	t.Cleanup(stopServer)
	w3WaitForGoReadiness(t, done, &runErr, goPort)
	stopServer()
	if serverStopErr != nil {
		t.Fatalf("real Go server shutdown: %v", serverStopErr)
	}

	wrongListener := w3ListenLoopback(t)
	t.Cleanup(func() { _ = wrongListener.Close() })
	wrongPort := wrongListener.Addr().(*net.TCPAddr).Port
	wrongCfg := w3RealGoServerConfig(wrongPort, nodeBaseURL, postgresURL, redisCacheURL, redisStateURL, redisQueueURL, "wrong-node-bridge-secret-0123456789")
	wrongCtx, cancelWrong := context.WithTimeout(ctx, 15*time.Second)
	defer cancelWrong()
	wrongResult := make(chan error, 1)
	go func() {
		wrongResult <- app.RunServer(wrongCtx, wrongCfg, logger)
	}()
	select {
	case err := <-wrongResult:
		if err == nil || !strings.Contains(err.Error(), string(modelcatalogsnapshotrebuild.ProbeFailureUnauthorized)) {
			t.Fatalf("wrong-secret Go startup error = %v, want unauthorized", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wrong-secret Go startup did not fail fast")
	}
	if err := wrongListener.Close(); err != nil {
		t.Fatalf("close wrong-secret listener: %v", err)
	}
}

func w3ListenLoopback(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback port: %v", err)
	}
	return listener
}

func w3RealGoServerConfig(
	port int,
	nodeBaseURL string,
	postgresURL string,
	redisCacheURL string,
	redisStateURL string,
	redisQueueURL string,
	secret string,
) config.Config {
	return config.Config{
		Host:                               "127.0.0.1",
		Port:                               port,
		Env:                                "test",
		LogLevel:                           "error",
		PostgresURL:                        postgresURL,
		RedisCacheURL:                      redisCacheURL,
		RedisStateURL:                      redisStateURL,
		RedisQueueURL:                      redisQueueURL,
		RedisNamespace:                     "w3-real-server-bridge",
		Secret:                             secret,
		NodeInternalBaseURL:                nodeBaseURL,
		NodeInternalRequestTimeout:         2 * time.Second,
		NodeInternalSnapshotRebuildTimeout: 60 * time.Second,
		ManagementAPIEnabled:               true,
		TrustProxy:                         "false",
		CookieSameSite:                     "lax",
		ShutdownTimeout:                    5 * time.Second,
	}
}

func w3WaitForGoReadiness(t *testing.T, done <-chan struct{}, runErr *error, port int) {
	t.Helper()
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/__aisys__/readyz"
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			t.Fatalf("real Go server exited before ready: %v", *runErr)
		case <-deadline.C:
			t.Fatalf("real Go server readiness timeout: %s", url)
		case <-ticker.C:
			requestCtx, cancel := context.WithTimeout(t.Context(), time.Second)
			request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				cancel()
				continue
			}
			var body httpapi.HealthResponse
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&body)
			_ = response.Body.Close()
			cancel()
			if response.StatusCode == http.StatusOK && decodeErr == nil && body.Dependencies["nodeModelCatalogBridge"].Status == "ok" {
				return
			}
		}
	}
}

func (p *w3ModelCatalogNodeBridgeProcess) BaseURL() string {
	return "http://127.0.0.1:" + strconv.Itoa(p.port)
}

func (p *w3ModelCatalogNodeBridgeProcess) Close(t *testing.T) {
	t.Helper()
	_ = p.stdin.Close()
	select {
	case err := <-p.wait:
		if err != nil {
			t.Errorf("Node model catalog bridge shutdown: %v; stdout=%q stderr=%q", err, p.safeOutput(p.stdout.String()), p.safeOutput(p.stderr.String()))
		}
	case <-time.After(10 * time.Second):
		_ = p.command.Process.Kill()
		select {
		case <-p.wait:
			t.Errorf("Node model catalog bridge required force kill after stdin close; stdout=%q stderr=%q", p.safeOutput(p.stdout.String()), p.safeOutput(p.stderr.String()))
		case <-time.After(5 * time.Second):
			t.Errorf("Node model catalog bridge did not reap after force kill; stdout=%q stderr=%q", p.safeOutput(p.stdout.String()), p.safeOutput(p.stderr.String()))
		}
	}
}

func (p *w3ModelCatalogNodeBridgeProcess) safeOutput(output string) string {
	for _, secret := range p.redacted {
		output = strings.ReplaceAll(output, secret, "<redacted>")
	}
	return output
}

func w3NodeBridgeReadyPort(output string) (int, bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, w3NodeBridgeReadyPrefix) {
			continue
		}
		var ready struct {
			Port int `json:"port"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, w3NodeBridgeReadyPrefix)), &ready); err == nil && ready.Port > 0 && ready.Port <= 65535 {
			return ready.Port, true
		}
	}
	return 0, false
}

func startW3NodeBridgeRedis(t *testing.T, ctx context.Context, role string) *tcredis.RedisContainer {
	t.Helper()
	container, err := tcredis.Run(ctx, redisImage)
	if err != nil {
		t.Fatalf("start redis %s container: %v", role, err)
	}
	return container
}

func terminateW3NodeBridgeContainer(t *testing.T, container testcontainers.Container) {
	t.Helper()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	terminateContainer(t, cleanupCtx, container)
}

func assertW3NodeBridgeDirtyAcknowledged(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM juhe_business.model_catalog_snapshot_rebuild_requests`).Scan(&count); err != nil {
		t.Fatalf("count model catalog dirty requests: %v", err)
	}
	if count != 0 {
		t.Fatalf("model catalog dirty request count = %d, want 0 after Node generation ack", count)
	}
}

func assertW3NodeBridgeGlobalSnapshots(t *testing.T, ctx context.Context, db *sql.DB, model string) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT protocol, variant
		FROM juhe_business.gateway_model_catalog_snapshots
		WHERE system_account_id = ''
		ORDER BY protocol, variant
	`)
	if err != nil {
		t.Fatalf("query global model catalog snapshot variants: %v", err)
	}
	defer rows.Close()
	var variants []string
	for rows.Next() {
		var protocol string
		var variant string
		if err := rows.Scan(&protocol, &variant); err != nil {
			t.Fatalf("scan global model catalog snapshot variant: %v", err)
		}
		variants = append(variants, protocol+"/"+variant)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate global model catalog snapshot variants: %v", err)
	}
	wantVariants := []string{"anthropic/default", "gemini/default", "openai/chat", "openai/codex", "openai/default"}
	if !slices.Equal(variants, wantVariants) {
		t.Fatalf("global model catalog snapshot variants = %v, want %v", variants, wantVariants)
	}
	var payload string
	if err := db.QueryRowContext(ctx, `
		SELECT payload_json
		FROM juhe_business.gateway_model_catalog_snapshots
		WHERE system_account_id = '' AND protocol = 'openai' AND variant = 'default'
	`).Scan(&payload); err != nil {
		t.Fatalf("query global OpenAI model catalog snapshot: %v", err)
	}
	if !strings.Contains(payload, `"id":"`+model+`"`) {
		t.Fatalf("global OpenAI model catalog snapshot missing %q", model)
	}
}
