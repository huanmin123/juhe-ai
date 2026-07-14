//go:build integration

package integration

import (
	"context"
	"database/sql"
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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
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
	w6ManagementClientIPStatsGooseBaselineVersion   = int64(39)
	w6ManagementClientIPStatsGooseTargetVersion     = int64(40)
)

var w6ManagementClientIPStatsInheritedEnvironmentAllowlist = map[string]struct{}{
	"APPDATA":      {},
	"COMSPEC":      {},
	"HOME":         {},
	"LANG":         {},
	"LC_ALL":       {},
	"LC_CTYPE":     {},
	"LOCALAPPDATA": {},
	"PATH":         {},
	"PATHEXT":      {},
	"SYSTEMROOT":   {},
	"TEMP":         {},
	"TMP":          {},
	"TMPDIR":       {},
	"USERPROFILE":  {},
	"WINDIR":       {},
}

func TestW6ManagementClientIPStatsNodeWriterGoReaderSmoke(t *testing.T) {
	nodePath, backendDir, helperPath := w6ManagementClientIPStatsNodeWriterPrerequisites(t)
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

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

	fixture := w6ManagementClientIPStatsRunNodeWriterFixture(
		t,
		ctx,
		nodePath,
		backendDir,
		helperPath,
		postgresURL,
	)
	w6ManagementClientIPStatsAssertFixtureContract(t, fixture)
	t.Setenv("JUHE_AI_USAGE_STATS_TIMEZONE", fixture.Timezone)
	w6ManagementClientIPStatsBaselineGooseHistory(t, db)
	w6ManagementClientIPStatsRunGooseTargetMigration(t, db)
	w6ManagementClientIPStatsAssertGooseUpgrade(t, db)

	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf(
			"open production PostgreSQL store: %s",
			w6ManagementClientIPStatsRedactSensitiveText(err.Error(), postgresURL),
		)
	}
	defer store.Close()
	storedTimezone, found, err := store.GetManagementUsageStatsTimezone(ctx)
	if err != nil {
		t.Fatalf("read production Go usage statistics timezone: %v", err)
	}
	if !found || storedTimezone != fixture.Timezone {
		t.Fatalf(
			"production Go usage statistics timezone = %q, found %v; want %q",
			storedTimezone,
			found,
			fixture.Timezone,
		)
	}

	service := managementclientipstats.NewServiceWithOptions(
		managementclientipstats.ServiceOptions{
			ListReader:               store,
			RegistryReader:           store,
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
	IPHash                  string                                      `json:"ipHash"`
	AggregateIPKey          string                                      `json:"aggregateIpKey"`
	Timezone                string                                      `json:"timezone"`
	MidnightDistanceMinutes int                                         `json:"midnightDistanceMinutes"`
	StartDate               string                                      `json:"startDate"`
	EndDate                 string                                      `json:"endDate"`
	Expected                w6ManagementClientIPStatsNodeWriterExpected `json:"expected"`
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
	baseEnvironment := w6ManagementClientIPStatsNodeWriterBaseEnvironment()
	w6ManagementClientIPStatsAssertIsolatedBaseEnvironment(t, baseEnvironment)
	probe := exec.CommandContext(probeCtx, nodePath, "--import", "tsx", "--eval", "")
	probe.Dir = backendDir
	probe.Env = baseEnvironment
	probe.Stdout = stdout
	probe.Stderr = stderr
	probe.WaitDelay = 3 * time.Second
	if err := probe.Run(); err != nil {
		w6ManagementClientIPStatsNodeWriterDependencyUnavailable(
			t,
			"tsx loader probe failed: %v; stdout=%q stderr=%q",
			err,
			w6ManagementClientIPStatsBoundedRedactedOutput(stdout, ""),
			w6ManagementClientIPStatsBoundedRedactedOutput(stderr, ""),
		)
		return "", "", ""
	}
	if stdout.Truncated() || stderr.Truncated() {
		w6ManagementClientIPStatsNodeWriterDependencyUnavailable(
			t,
			"tsx loader probe output exceeded bounded capture; stdout=%q stderr=%q",
			w6ManagementClientIPStatsBoundedRedactedOutput(stdout, ""),
			w6ManagementClientIPStatsBoundedRedactedOutput(stderr, ""),
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
	additionalEnvironment ...string,
) w6ManagementClientIPStatsNodeWriterFixture {
	t.Helper()

	nodeCtx, cancel := context.WithTimeout(ctx, w6ManagementClientIPStatsNodeWriterTimeout)
	defer cancel()
	stdout := newW6ManagementClientIPStatsBoundedOutput(w6ManagementClientIPStatsNodeWriterOutputLimit)
	stderr := newW6ManagementClientIPStatsBoundedOutput(w6ManagementClientIPStatsNodeWriterOutputLimit)
	command := exec.CommandContext(nodeCtx, nodePath, "--import", "tsx", helperPath)
	command.Dir = backendDir
	command.Env = append(
		w6ManagementClientIPStatsNodeWriterEnvironment(postgresURL),
		additionalEnvironment...,
	)
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
			w6ManagementClientIPStatsBoundedRedactedOutput(stdout, postgresURL),
			w6ManagementClientIPStatsBoundedRedactedOutput(stderr, postgresURL),
		)
	}
	if waitErr != nil {
		t.Fatalf(
			"Node writer fixture failed: %v; stdout=%q stderr=%q",
			waitErr,
			w6ManagementClientIPStatsBoundedRedactedOutput(stdout, postgresURL),
			w6ManagementClientIPStatsBoundedRedactedOutput(stderr, postgresURL),
		)
	}
	if stdout.Truncated() || stderr.Truncated() {
		t.Fatalf("Node writer fixture output exceeded bounded capture")
	}
	redactedStdout := w6ManagementClientIPStatsBoundedRedactedOutput(stdout, postgresURL)
	redactedStderr := w6ManagementClientIPStatsBoundedRedactedOutput(stderr, postgresURL)

	var payloadLine string
	for _, line := range strings.Split(redactedStdout, "\n") {
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
			redactedStdout,
			redactedStderr,
		)
	}

	var fixture w6ManagementClientIPStatsNodeWriterFixture
	if err := json.Unmarshal([]byte(payloadLine), &fixture); err != nil {
		t.Fatalf("decode Node writer fixture protocol: %v", err)
	}
	return fixture
}

func w6ManagementClientIPStatsNodeWriterBaseEnvironment() []string {
	environment := make([]string, 0, len(w6ManagementClientIPStatsInheritedEnvironmentAllowlist))
	for _, item := range os.Environ() {
		name, _, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		if _, allowed := w6ManagementClientIPStatsInheritedEnvironmentAllowlist[strings.ToUpper(name)]; !allowed {
			continue
		}
		environment = append(environment, item)
	}
	sort.Strings(environment)
	return environment
}

func w6ManagementClientIPStatsAssertIsolatedBaseEnvironment(t *testing.T, environment []string) {
	t.Helper()

	for _, item := range environment {
		name, _, found := strings.Cut(item, "=")
		if !found {
			t.Fatalf("Node writer inherited environment entry is malformed: %q", item)
		}
		normalizedName := strings.ToUpper(name)
		_, allowed := w6ManagementClientIPStatsInheritedEnvironmentAllowlist[normalizedName]
		forbidden := normalizedName == "NODE_ENV" ||
			normalizedName == "NODE_OPTIONS" ||
			normalizedName == "NODE_PATH" ||
			strings.HasPrefix(normalizedName, "TSX_") ||
			strings.HasPrefix(normalizedName, "PG") ||
			strings.HasPrefix(normalizedName, "JUHE_AI_")
		if !allowed || forbidden {
			t.Fatalf("Node writer must not inherit environment variable %q", name)
		}
	}
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
	if fixture.Timezone == "" || fixture.Timezone == "UTC" {
		t.Fatalf("Node writer timezone = %q, want a non-UTC fixture timezone", fixture.Timezone)
	}
	if fixture.Timezone != "Asia/Shanghai" && fixture.Timezone != "America/New_York" {
		t.Fatalf("Node writer timezone = %q, want a supported fixture timezone", fixture.Timezone)
	}
	if fixture.MidnightDistanceMinutes < 5*60 {
		t.Fatalf(
			"Node writer timezone %q is only %d minutes from local midnight",
			fixture.Timezone,
			fixture.MidnightDistanceMinutes,
		)
	}
	want := w6ManagementClientIPStatsNodeWriterExpected{
		RequestCount:        3,
		SuccessCount:        2,
		ErrorCount:          1,
		ErrorRate:           1.0 / 3.0,
		InputTokens:         606,
		OutputTokens:        66,
		CacheReadTokens:     78,
		CacheReadCost:       0.0078,
		CacheWriteTokens:    102,
		CacheWrite1hTokens:  114,
		CacheWriteCost:      0.0102,
		ThinkingTokens:      138,
		InputImageTokens:    174,
		OutputImageTokens:   186,
		TotalTokens:         672,
		TotalCost:           0.0606,
		ActiveDays:          1,
		AverageDurationMs:   238,
		AverageFirstTokenMs: 89.0 / 3.0,
		MaxDurationMs:       357,
		LastSeenAt:          fixture.Expected.LastSeenAt,
		LastUsedAt:          fixture.Expected.LastUsedAt,
		LastErrorAt:         fixture.Expected.LastErrorAt,
	}
	if fixture.Expected != want || fixture.Expected.LastSeenAt == "" {
		t.Fatalf("Node writer expected contract = %+v, want %+v", fixture.Expected, want)
	}
	lastSeenAt := w6ManagementClientIPStatsParseFixtureTime(t, "lastSeenAt", fixture.Expected.LastSeenAt)
	lastUsedAt := w6ManagementClientIPStatsParseFixtureTime(t, "lastUsedAt", fixture.Expected.LastUsedAt)
	lastErrorAt := w6ManagementClientIPStatsParseFixtureTime(t, "lastErrorAt", fixture.Expected.LastErrorAt)
	if lastSeenAt.Equal(lastUsedAt) || lastSeenAt.Equal(lastErrorAt) || lastUsedAt.Equal(lastErrorAt) {
		t.Fatalf(
			"Node writer lastSeenAt, lastUsedAt, and lastErrorAt must be mutually distinct: %+v",
			fixture.Expected,
		)
	}
	if !lastErrorAt.Before(lastUsedAt) || !lastUsedAt.Before(lastSeenAt) {
		t.Fatalf(
			"Node writer timestamps are not asymmetric: error=%s used=%s seen=%s",
			fixture.Expected.LastErrorAt,
			fixture.Expected.LastUsedAt,
			fixture.Expected.LastSeenAt,
		)
	}
	location, err := time.LoadLocation(fixture.Timezone)
	if err != nil {
		t.Fatalf("load Node writer fixture timezone %q: %v", fixture.Timezone, err)
	}
	if lastErrorAt.In(location).Format("2006-01-02") != fixture.StartDate ||
		lastUsedAt.In(location).Format("2006-01-02") != fixture.StartDate {
		t.Fatalf("Node writer target timestamps are outside %s in %s", fixture.StartDate, fixture.Timezone)
	}
	targetDate, err := time.ParseInLocation("2006-01-02", fixture.StartDate, location)
	if err != nil {
		t.Fatalf("parse Node writer target date %q: %v", fixture.StartDate, err)
	}
	wantNextDate := targetDate.AddDate(0, 0, 1).Format("2006-01-02")
	if lastSeenAt.In(location).Format("2006-01-02") != wantNextDate {
		t.Fatalf("Node writer registry lastSeenAt is not on next local date %s", wantNextDate)
	}
}

func w6ManagementClientIPStatsParseFixtureTime(t *testing.T, name string, value string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parse Node writer %s %q: %v", name, value, err)
	}
	return parsed
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

func w6ManagementClientIPStatsBaselineGooseHistory(t *testing.T, db *sql.DB) {
	t.Helper()

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect for Node schema baseline: %v", err)
	}
	version, err := goose.EnsureDBVersion(db)
	if err != nil {
		t.Fatalf("create goose history after Node schema and data: %v", err)
	}
	if version != 0 {
		t.Fatalf("initial goose version after Node schema and data = %d, want 0", version)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin goose history baseline: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for migrationVersion := int64(1); migrationVersion <= w6ManagementClientIPStatsGooseBaselineVersion; migrationVersion++ {
		if _, err := tx.Exec(
			"INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, TRUE)",
			migrationVersion,
		); err != nil {
			t.Fatalf("baseline goose migration %06d over Node schema: %v", migrationVersion, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit goose history baseline: %v", err)
	}
}

func w6ManagementClientIPStatsRunGooseTargetMigration(t *testing.T, db *sql.DB) {
	t.Helper()

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect for target migration: %v", err)
	}
	migrationDir := filepath.Join(repoRoot(t), "db", "migrations")
	if err := goose.UpTo(db, migrationDir, w6ManagementClientIPStatsGooseTargetVersion); err != nil {
		t.Fatalf(
			"goose up to %06d over Node schema: %v",
			w6ManagementClientIPStatsGooseTargetVersion,
			err,
		)
	}
}

func w6ManagementClientIPStatsAssertGooseUpgrade(t *testing.T, db *sql.DB) {
	t.Helper()

	version, err := goose.GetDBVersion(db)
	if err != nil {
		t.Fatalf("inspect current goose version after Node writer: %v", err)
	}
	if version != w6ManagementClientIPStatsGooseTargetVersion {
		t.Fatalf(
			"current goose version after Node writer = %d, want exactly %d",
			version,
			w6ManagementClientIPStatsGooseTargetVersion,
		)
	}

	var applied bool
	if err := db.QueryRow(`
SELECT is_applied
FROM goose_db_version
WHERE version_id = $1
ORDER BY id DESC
LIMIT 1
`, w6ManagementClientIPStatsGooseTargetVersion).Scan(&applied); err != nil {
		t.Fatalf("inspect goose migration %06d after Node writer: %v", w6ManagementClientIPStatsGooseTargetVersion, err)
	}
	if !applied {
		t.Fatalf("goose migration %06d is not applied after Node writer", w6ManagementClientIPStatsGooseTargetVersion)
	}
	var newerApplied int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM goose_db_version WHERE version_id > $1 AND is_applied = TRUE",
		w6ManagementClientIPStatsGooseTargetVersion,
	).Scan(&newerApplied); err != nil {
		t.Fatalf("inspect goose versions newer than %06d: %v", w6ManagementClientIPStatsGooseTargetVersion, err)
	}
	if newerApplied != 0 {
		t.Fatalf(
			"goose applied %d migration(s) newer than explicitly locked target %06d",
			newerApplied,
			w6ManagementClientIPStatsGooseTargetVersion,
		)
	}
}

func w6ManagementClientIPStatsBoundedRedactedOutput(
	output *w6ManagementClientIPStatsBoundedOutput,
	postgresURL string,
) string {
	return w6ManagementClientIPStatsRedactSensitiveText(output.String(), postgresURL)
}

func w6ManagementClientIPStatsRedactSensitiveText(output string, postgresURL string) string {
	secrets := make(map[string]struct{})
	w6ManagementClientIPStatsAddSecretVariants(secrets, postgresURL)
	w6ManagementClientIPStatsAddSecretVariants(secrets, w6ManagementClientIPStatsNodeWriterPassword)
	if parsed, err := url.Parse(postgresURL); err == nil && parsed != nil {
		w6ManagementClientIPStatsAddSecretVariants(secrets, parsed.String())
		if parsed.User != nil {
			w6ManagementClientIPStatsAddSecretVariants(secrets, parsed.User.String())
			w6ManagementClientIPStatsAddSecretVariants(secrets, parsed.User.Username())
			if password, ok := parsed.User.Password(); ok {
				w6ManagementClientIPStatsAddSecretVariants(secrets, password)
			}
		}
	}
	ordered := make([]string, 0, len(secrets))
	for secret := range secrets {
		ordered = append(ordered, secret)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return len(ordered[left]) > len(ordered[right])
	})
	redacted := output
	for _, secret := range ordered {
		redacted = strings.ReplaceAll(redacted, secret, "[redacted]")
	}
	return redacted
}

func w6ManagementClientIPStatsAddSecretVariants(secrets map[string]struct{}, value string) {
	if value == "" {
		return
	}
	for _, variant := range []string{
		value,
		url.PathEscape(value),
		url.QueryEscape(value),
	} {
		if variant == "" {
			continue
		}
		secrets[variant] = struct{}{}
		secrets[w6ManagementClientIPStatsNormalizePercentHexCase(variant, true)] = struct{}{}
		secrets[w6ManagementClientIPStatsNormalizePercentHexCase(variant, false)] = struct{}{}
	}
}

func w6ManagementClientIPStatsNormalizePercentHexCase(value string, upper bool) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '%' || index+2 >= len(value) {
			builder.WriteByte(value[index])
			continue
		}
		builder.WriteByte('%')
		hex := value[index+1 : index+3]
		if upper {
			builder.WriteString(strings.ToUpper(hex))
		} else {
			builder.WriteString(strings.ToLower(hex))
		}
		index += 2
	}
	return builder.String()
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
