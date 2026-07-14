//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/httpapi"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementclientipstats"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

const (
	w6ManagementClientIPStatsNodeWriterOutputPrefix = "JUHE_AI_NODE_GO_IP_STATS "
	w6ManagementClientIPStatsNodeWriterOutputLimit  = 64 * 1024
	w6ManagementClientIPStatsNodeWriterBodyLimit    = 128 * 1024
	w6ManagementClientIPStatsNodeWriterTimeout      = 90 * time.Second
	w6ManagementClientIPStatsNodeWriterProbeTimeout = 10 * time.Second
	w6ManagementClientIPStatsNodeWriterPassword     = "node_go_ip_stats_password"
)

func TestW6ManagementClientIPStatsNodeWriterGoReaderSmoke(t *testing.T) {
	nodePath, backendDir, helperPath := w6ManagementClientIPStatsNodeWriterPrerequisites(t)
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	t.Setenv("JUHE_AI_USAGE_STATS_TIMEZONE", "UTC")
	postgresContainer, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword(w6ManagementClientIPStatsNodeWriterPassword),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start PostgreSQL container: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		terminateContainer(t, cleanupCtx, postgresContainer)
	}()

	postgresURL, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("build PostgreSQL connection string: %v", err)
	}
	db := openSQLDB(t, postgresURL)
	defer closeSQLDB(t, db)
	runGooseMigrations(t, db)

	fixture := w6ManagementClientIPStatsRunNodeWriterFixture(
		t,
		ctx,
		nodePath,
		backendDir,
		helperPath,
		postgresURL,
	)
	w6ManagementClientIPStatsAssertFixtureContract(t, fixture)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf(
			"open production PostgreSQL store: %s",
			w6ManagementClientIPStatsRedactNodeOutput(err.Error(), postgresURL),
		)
	}
	defer store.Close()

	service := managementclientipstats.NewServiceWithOptions(
		managementclientipstats.ServiceOptions{
			ListReader:               store,
			UsageStatsTimezoneReader: store,
			Now:                      time.Now,
		},
	)
	router := httpapi.NewRouter(httpapi.RouterOptions{
		Config: config.Config{
			Host:                 "127.0.0.1",
			Port:                 3000,
			ManagementAPIEnabled: true,
			TrustProxy:           "false",
		},
		Logger:                            slog.New(slog.NewTextHandler(io.Discard, nil)),
		SystemAPIRateLimitReader:          store,
		SystemAPIIPRateLimiter:            httpapi.NewInMemorySystemAPIIPRateLimiter(),
		SystemAPIAuthenticatedRateLimiter: httpapi.NewInMemorySystemAPIAuthenticatedRateLimiter(),
		ManagementAPIAuthMiddleware: httpapi.NewManagementAPIAuthMiddleware(
			w6ManagementClientIPStatsNodeWriterAuthenticator{},
		),
		ManagementClientIPStatsHandler: httpapi.NewManagementClientIPStatsHandler(service),
	})
	server := httptest.NewServer(router)
	defer server.Close()

	response := w6ManagementClientIPStatsRequestNodeWriterResult(t, ctx, server, fixture)
	w6ManagementClientIPStatsAssertNodeWriterResult(t, response, fixture)
}

type w6ManagementClientIPStatsNodeWriterExpected struct {
	RequestCount        int64   `json:"requestCount"`
	SuccessCount        int64   `json:"successCount"`
	ErrorCount          int64   `json:"errorCount"`
	ErrorRate           float64 `json:"errorRate"`
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens"`
	CacheReadCost       float64 `json:"cacheReadCost"`
	CacheWriteTokens    int64   `json:"cacheWriteTokens"`
	CacheWrite1hTokens  int64   `json:"cacheWrite1hTokens"`
	CacheWriteCost      float64 `json:"cacheWriteCost"`
	ThinkingTokens      int64   `json:"thinkingTokens"`
	InputImageTokens    int64   `json:"inputImageTokens"`
	OutputImageTokens   int64   `json:"outputImageTokens"`
	TotalTokens         int64   `json:"totalTokens"`
	TotalCost           float64 `json:"totalCost"`
	ActiveDays          int     `json:"activeDays"`
	AverageDurationMs   float64 `json:"averageDurationMs"`
	AverageFirstTokenMs float64 `json:"averageFirstTokenMs"`
	MaxDurationMs       int64   `json:"maxDurationMs"`
	LastSeenAt          string  `json:"lastSeenAt"`
	LastUsedAt          string  `json:"lastUsedAt"`
	LastErrorAt         string  `json:"lastErrorAt"`
}

type w6ManagementClientIPStatsNodeWriterFixture struct {
	IPHash         string                                      `json:"ipHash"`
	AggregateIPKey string                                      `json:"aggregateIpKey"`
	StartDate      string                                      `json:"startDate"`
	EndDate        string                                      `json:"endDate"`
	Expected       w6ManagementClientIPStatsNodeWriterExpected `json:"expected"`
}

type w6ManagementClientIPStatsNodeWriterEnvelope struct {
	Data managementclientipstats.ListResult `json:"data"`
}

type w6ManagementClientIPStatsNodeWriterAuthenticator struct{}

func (w6ManagementClientIPStatsNodeWriterAuthenticator) AuthenticateCookie(
	_ context.Context,
	_ string,
) (managementauth.Context, error) {
	return managementauth.Context{
		SystemAccountID: "sys_node_go_ip_stats_admin",
		Username:        "node-go-ip-stats-admin",
		DisplayName:     "Node Go IP Stats Admin",
		Role:            "admin",
		SessionID:       "sess_node_go_ip_stats_admin",
	}, nil
}

func w6ManagementClientIPStatsNodeWriterPrerequisites(
	t *testing.T,
) (string, string, string) {
	t.Helper()

	nodePath, err := exec.LookPath("node")
	if err != nil {
		w6ManagementClientIPStatsNodeWriterDependencyUnavailable(
			t,
			"node executable is unavailable: %v",
			err,
		)
		return "", "", ""
	}
	nodePath, err = filepath.Abs(nodePath)
	if err != nil {
		t.Fatalf("resolve absolute node executable path: %v", err)
	}

	backendGoDir := repoRoot(t)
	repositoryDir := filepath.Dir(backendGoDir)
	backendDir := filepath.Join(repositoryDir, "backend")
	helperPath := filepath.Join(
		backendDir,
		"src",
		"scripts",
		"regression",
		"client-ip-stats-node-writer-go-reader-fixture.ts",
	)
	if info, statErr := os.Stat(helperPath); statErr != nil {
		t.Fatalf("stat Node writer fixture: %v", statErr)
	} else if info.IsDir() {
		t.Fatal("Node writer fixture path is a directory")
	}

	probeCtx, cancel := context.WithTimeout(t.Context(), w6ManagementClientIPStatsNodeWriterProbeTimeout)
	defer cancel()
	stdout := newW6ManagementClientIPStatsBoundedOutput(w6ManagementClientIPStatsNodeWriterOutputLimit)
	stderr := newW6ManagementClientIPStatsBoundedOutput(w6ManagementClientIPStatsNodeWriterOutputLimit)
	probe := exec.CommandContext(probeCtx, nodePath, "--import", "tsx", "--eval", "")
	probe.Dir = backendDir
	probe.Env = w6ManagementClientIPStatsNodeWriterBaseEnvironment()
	probe.Stdout = stdout
	probe.Stderr = stderr
	probe.WaitDelay = 3 * time.Second
	if err := probe.Run(); err != nil {
		w6ManagementClientIPStatsNodeWriterDependencyUnavailable(
			t,
			"tsx loader probe failed: %v; stdout=%q stderr=%q",
			err,
			stdout.String(),
			stderr.String(),
		)
		return "", "", ""
	}

	return nodePath, backendDir, helperPath
}

func w6ManagementClientIPStatsNodeWriterDependencyUnavailable(
	t *testing.T,
	format string,
	args ...any,
) {
	t.Helper()

	message := fmt.Sprintf(format, args...)
	required, err := parseIntegrationRequirement(os.Getenv(requireIntegrationEnv))
	if err != nil {
		t.Fatalf("%s configuration is invalid: %v", requireIntegrationEnv, err)
	}
	if required {
		t.Fatalf("%s=1 but Node writer dependency is unavailable: %s", requireIntegrationEnv, message)
	}
	t.Skipf("skip Node writer to Go reader smoke because dependency is unavailable: %s", message)
}

func w6ManagementClientIPStatsRunNodeWriterFixture(
	t *testing.T,
	ctx context.Context,
	nodePath string,
	backendDir string,
	helperPath string,
	postgresURL string,
) w6ManagementClientIPStatsNodeWriterFixture {
	t.Helper()

	nodeCtx, cancel := context.WithTimeout(ctx, w6ManagementClientIPStatsNodeWriterTimeout)
	defer cancel()
	stdout := newW6ManagementClientIPStatsBoundedOutput(w6ManagementClientIPStatsNodeWriterOutputLimit)
	stderr := newW6ManagementClientIPStatsBoundedOutput(w6ManagementClientIPStatsNodeWriterOutputLimit)
	command := exec.CommandContext(nodeCtx, nodePath, "--import", "tsx", helperPath)
	command.Dir = backendDir
	command.Env = w6ManagementClientIPStatsNodeWriterEnvironment(postgresURL)
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = 5 * time.Second

	if err := command.Start(); err != nil {
		t.Fatalf("start Node writer fixture: %v", err)
	}
	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()

	var waitErr error
	select {
	case waitErr = <-wait:
	case <-nodeCtx.Done():
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		select {
		case waitErr = <-wait:
		case <-time.After(5 * time.Second):
			t.Fatalf("Node writer fixture did not exit after timeout")
		}
		t.Fatalf(
			"Node writer fixture timed out: %v; stdout=%q stderr=%q",
			nodeCtx.Err(),
			w6ManagementClientIPStatsRedactNodeOutput(stdout.String(), postgresURL),
			w6ManagementClientIPStatsRedactNodeOutput(stderr.String(), postgresURL),
		)
	}
	if waitErr != nil {
		t.Fatalf(
			"Node writer fixture failed: %v; stdout=%q stderr=%q",
			waitErr,
			w6ManagementClientIPStatsRedactNodeOutput(stdout.String(), postgresURL),
			w6ManagementClientIPStatsRedactNodeOutput(stderr.String(), postgresURL),
		)
	}
	if stdout.Truncated() || stderr.Truncated() {
		t.Fatalf("Node writer fixture output exceeded bounded capture")
	}

	var payloadLine string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, w6ManagementClientIPStatsNodeWriterOutputPrefix) {
			continue
		}
		if payloadLine != "" {
			t.Fatal("Node writer fixture emitted more than one protocol record")
		}
		payloadLine = strings.TrimPrefix(line, w6ManagementClientIPStatsNodeWriterOutputPrefix)
	}
	if payloadLine == "" {
		t.Fatalf(
			"Node writer fixture protocol record is missing; stdout=%q stderr=%q",
			w6ManagementClientIPStatsRedactNodeOutput(stdout.String(), postgresURL),
			w6ManagementClientIPStatsRedactNodeOutput(stderr.String(), postgresURL),
		)
	}

	var fixture w6ManagementClientIPStatsNodeWriterFixture
	if err := json.Unmarshal([]byte(payloadLine), &fixture); err != nil {
		t.Fatalf("decode Node writer fixture protocol: %v", err)
	}
	return fixture
}

func w6ManagementClientIPStatsNodeWriterBaseEnvironment() []string {
	environment := make([]string, 0, len(os.Environ()))
	for _, item := range os.Environ() {
		name, _, found := strings.Cut(item, "=")
		normalizedName := strings.ToUpper(name)
		if found && (strings.HasPrefix(normalizedName, "JUHE_AI_") || normalizedName == "NODE_ENV") {
			continue
		}
		environment = append(environment, item)
	}
	return environment
}

func w6ManagementClientIPStatsNodeWriterEnvironment(postgresURL string) []string {
	return append(w6ManagementClientIPStatsNodeWriterBaseEnvironment(),
		"NODE_ENV=test",
		"JUHE_AI_RUNTIME_MODE=performance",
		"JUHE_AI_DATABASE_DRIVER=postgres",
		"JUHE_AI_CACHE_DRIVER=redis",
		"JUHE_AI_RUNTIME_STATE_DRIVER=redis",
		"JUHE_AI_QUEUE_DRIVER=redis_stream",
		"JUHE_AI_POSTGRES_URL="+postgresURL,
		"JUHE_AI_REDIS_CACHE_URL=redis://127.0.0.1:1/0",
		"JUHE_AI_REDIS_STATE_URL=redis://127.0.0.1:1/1",
		"JUHE_AI_REDIS_QUEUE_URL=redis://127.0.0.1:1/2",
		"JUHE_AI_PROCESS_ROLE=worker",
		"JUHE_AI_WORKER_ROLE=stats-worker",
		"JUHE_AI_USAGE_STATS_TIMEZONE=UTC",
		"JUHE_AI_LOG_FILE_ENABLED=false",
		"JUHE_AI_LOG_CONSOLE_ENABLED=false",
		"JUHE_AI_NODE_GO_IP_STATS_FIXTURE=1",
	)
}

func w6ManagementClientIPStatsRequestNodeWriterResult(
	t *testing.T,
	ctx context.Context,
	server *httptest.Server,
	fixture w6ManagementClientIPStatsNodeWriterFixture,
) managementclientipstats.ListResult {
	t.Helper()

	query := url.Values{
		"page":      {"1"},
		"pageSize":  {"20"},
		"keyword":   {fixture.AggregateIPKey},
		"startDate": {fixture.StartDate},
		"endDate":   {fixture.EndDate},
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		server.URL+"/__aisys__/api/ip-stats?"+query.Encode(),
		nil,
	)
	if err != nil {
		t.Fatalf("build Go client IP stats request: %v", err)
	}
	request.Header.Set("Cookie", "juhe_ai_session=node-go-ip-stats-session")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("request production Go client IP stats route: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, w6ManagementClientIPStatsNodeWriterBodyLimit+1))
	if err != nil {
		t.Fatalf("read Go client IP stats response: %v", err)
	}
	if len(body) > w6ManagementClientIPStatsNodeWriterBodyLimit {
		t.Fatalf("Go client IP stats response exceeded %d bytes", w6ManagementClientIPStatsNodeWriterBodyLimit)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Go client IP stats status = %d; body = %s", response.StatusCode, body)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Go client IP stats Cache-Control = %q, want no-store", response.Header.Get("Cache-Control"))
	}

	var envelope w6ManagementClientIPStatsNodeWriterEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode Go client IP stats response: %v; body = %s", err, body)
	}
	return envelope.Data
}

func w6ManagementClientIPStatsAssertFixtureContract(
	t *testing.T,
	fixture w6ManagementClientIPStatsNodeWriterFixture,
) {
	t.Helper()

	if fixture.IPHash == "" || fixture.AggregateIPKey != "198.18.250.42" {
		t.Fatalf("Node writer identity = hash %q aggregate %q", fixture.IPHash, fixture.AggregateIPKey)
	}
	if fixture.StartDate == "" || fixture.StartDate != fixture.EndDate {
		t.Fatalf("Node writer date range = %q..%q", fixture.StartDate, fixture.EndDate)
	}
	want := w6ManagementClientIPStatsNodeWriterExpected{
		RequestCount:        2,
		SuccessCount:        1,
		ErrorCount:          1,
		ErrorRate:           0.5,
		InputTokens:         140,
		OutputTokens:        25,
		CacheReadTokens:     12,
		CacheReadCost:       0.00013,
		CacheWriteTokens:    9,
		CacheWrite1hTokens:  5,
		CacheWriteCost:      0.00027,
		ThinkingTokens:      18,
		InputImageTokens:    3,
		OutputImageTokens:   7,
		TotalTokens:         165,
		TotalCost:           0.002,
		ActiveDays:          1,
		AverageDurationMs:   180,
		AverageFirstTokenMs: 40,
		MaxDurationMs:       240,
		LastSeenAt:          fixture.Expected.LastSeenAt,
		LastUsedAt:          fixture.Expected.LastSeenAt,
		LastErrorAt:         fixture.Expected.LastSeenAt,
	}
	if fixture.Expected != want || fixture.Expected.LastSeenAt == "" {
		t.Fatalf("Node writer expected contract = %+v, want %+v", fixture.Expected, want)
	}
}

func w6ManagementClientIPStatsAssertNodeWriterResult(
	t *testing.T,
	result managementclientipstats.ListResult,
	fixture w6ManagementClientIPStatsNodeWriterFixture,
) {
	t.Helper()

	if !result.RangeReady {
		t.Fatal("Go client IP stats rangeReady = false after Node production refresh")
	}
	if result.Page != 1 || result.PageSize != 20 || result.PageUpperBound != 1 || result.HasMore {
		t.Fatalf("Go client IP stats pagination = %+v", result)
	}
	if result.Range.StartDate != fixture.StartDate ||
		result.Range.EndDate != fixture.EndDate ||
		result.Range.Days != 1 || result.Range.MaxDays != 31 {
		t.Fatalf("Go client IP stats range = %+v", result.Range)
	}
	if len(result.Items) != 1 {
		t.Fatalf("Go client IP stats items = %d, want 1: %+v", len(result.Items), result.Items)
	}
	item := result.Items[0]
	if item.IPHash != fixture.IPHash ||
		item.AggregateIPKey != fixture.AggregateIPKey ||
		item.Status != "normal" {
		t.Fatalf("Go client IP stats identity/status = %+v", item)
	}
	w6ManagementClientIPStatsAssertStringPointer(t, "lastSeenAt", item.LastSeenAt, fixture.Expected.LastSeenAt)

	usage := item.RangeUsage
	if usage.RequestCount != fixture.Expected.RequestCount ||
		usage.SuccessCount != fixture.Expected.SuccessCount ||
		usage.ErrorCount != fixture.Expected.ErrorCount ||
		usage.InputTokens != fixture.Expected.InputTokens ||
		usage.OutputTokens != fixture.Expected.OutputTokens ||
		usage.CacheReadTokens != fixture.Expected.CacheReadTokens ||
		usage.CacheWriteTokens != fixture.Expected.CacheWriteTokens ||
		usage.CacheWrite1hTokens != fixture.Expected.CacheWrite1hTokens ||
		usage.ThinkingTokens != fixture.Expected.ThinkingTokens ||
		usage.InputImageTokens != fixture.Expected.InputImageTokens ||
		usage.OutputImageTokens != fixture.Expected.OutputImageTokens ||
		usage.TotalTokens != fixture.Expected.TotalTokens ||
		usage.ActiveDays != fixture.Expected.ActiveDays {
		t.Fatalf("Go client IP stats integer metrics = %+v", usage)
	}
	w6ManagementClientIPStatsAssertFloat(t, "errorRate", usage.ErrorRate, fixture.Expected.ErrorRate)
	w6ManagementClientIPStatsAssertFloat(t, "cacheReadCost", usage.CacheReadCost, fixture.Expected.CacheReadCost)
	w6ManagementClientIPStatsAssertFloat(t, "cacheWriteCost", usage.CacheWriteCost, fixture.Expected.CacheWriteCost)
	w6ManagementClientIPStatsAssertFloat(t, "totalCost", usage.TotalCost, fixture.Expected.TotalCost)
	w6ManagementClientIPStatsAssertFloatPointer(
		t,
		"averageDurationMs",
		usage.AverageDurationMs,
		fixture.Expected.AverageDurationMs,
	)
	w6ManagementClientIPStatsAssertFloatPointer(
		t,
		"averageFirstTokenMs",
		usage.AverageFirstTokenMs,
		fixture.Expected.AverageFirstTokenMs,
	)
	if usage.MaxDurationMs == nil || *usage.MaxDurationMs != fixture.Expected.MaxDurationMs {
		t.Fatalf("Go client IP stats maxDurationMs = %v, want %d", usage.MaxDurationMs, fixture.Expected.MaxDurationMs)
	}
	w6ManagementClientIPStatsAssertStringPointer(t, "lastUsedAt", usage.LastUsedAt, fixture.Expected.LastUsedAt)
	w6ManagementClientIPStatsAssertStringPointer(t, "lastErrorAt", usage.LastErrorAt, fixture.Expected.LastErrorAt)
}

func w6ManagementClientIPStatsAssertFloat(t *testing.T, name string, got float64, want float64) {
	t.Helper()

	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("Go client IP stats %s = %.15g, want %.15g", name, got, want)
	}
}

func w6ManagementClientIPStatsAssertFloatPointer(
	t *testing.T,
	name string,
	got *float64,
	want float64,
) {
	t.Helper()

	if got == nil {
		t.Fatalf("Go client IP stats %s is nil, want %.15g", name, want)
	}
	w6ManagementClientIPStatsAssertFloat(t, name, *got, want)
}

func w6ManagementClientIPStatsAssertStringPointer(
	t *testing.T,
	name string,
	got *string,
	want string,
) {
	t.Helper()

	if got == nil || *got != want {
		t.Fatalf("Go client IP stats %s = %v, want %q", name, got, want)
	}
}

func w6ManagementClientIPStatsRedactNodeOutput(output string, postgresURL string) string {
	redacted := strings.ReplaceAll(output, postgresURL, "[redacted-postgres-url]")
	redacted = strings.ReplaceAll(redacted, w6ManagementClientIPStatsNodeWriterPassword, "[redacted-password]")
	return redacted
}

type w6ManagementClientIPStatsBoundedOutput struct {
	mu        sync.Mutex
	limit     int
	data      []byte
	truncated bool
}

func newW6ManagementClientIPStatsBoundedOutput(limit int) *w6ManagementClientIPStatsBoundedOutput {
	return &w6ManagementClientIPStatsBoundedOutput{limit: limit}
}

func (output *w6ManagementClientIPStatsBoundedOutput) Write(value []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()

	written := len(value)
	if output.limit <= 0 {
		output.truncated = output.truncated || written > 0
		return written, nil
	}
	if len(value) >= output.limit {
		output.data = append(output.data[:0], value[len(value)-output.limit:]...)
		output.truncated = true
		return written, nil
	}
	if overflow := len(output.data) + len(value) - output.limit; overflow > 0 {
		copy(output.data, output.data[overflow:])
		output.data = output.data[:len(output.data)-overflow]
		output.truncated = true
	}
	output.data = append(output.data, value...)
	return written, nil
}

func (output *w6ManagementClientIPStatsBoundedOutput) String() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return string(append([]byte(nil), output.data...))
}

func (output *w6ManagementClientIPStatsBoundedOutput) Truncated() bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.truncated
}
