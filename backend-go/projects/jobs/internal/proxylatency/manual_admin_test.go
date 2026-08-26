package proxylatency

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/operationlogappend"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type fakeManualAdminRunner struct {
	report ProxyTestReport
	err    error
	input  ManualRequest
}

func (r *fakeManualAdminRunner) RunManual(_ context.Context, input ManualRequest) (ProxyTestReport, error) {
	r.input = input
	return r.report, r.err
}

type fakeManualAdminSource struct {
	actor    ManualAdminActor
	authErr  error
	snapshot manualAdminSnapshot
	loadErr  error
	exists   bool
	existErr error
}

func (s *fakeManualAdminSource) Authenticate(_ context.Context, _ string, _ *http.Cookie) (ManualAdminActor, error) {
	return s.actor, s.authErr
}

func (s *fakeManualAdminSource) LoadSnapshot(_ context.Context, _ string, _ time.Duration) (manualAdminSnapshot, error) {
	return s.snapshot, s.loadErr
}

func (s *fakeManualAdminSource) Exists(_ context.Context, _ string) (bool, error) {
	return s.exists, s.existErr
}

type fakeManualAdminAuditAppender struct {
	inputs chan operationlogappend.Input
	err    error
}

func (a *fakeManualAdminAuditAppender) Append(_ context.Context, input operationlogappend.Input) error {
	if a.inputs != nil {
		a.inputs <- input
	}
	return a.err
}

func TestManualAdminSnapshotSQLUsesActiveNodePostgresCatalogFlags(t *testing.T) {
	for _, required := range []string{
		"CASE WHEN ppp.enabled = 1 THEN 0 ELSE 1 END",
		"WHERE p.enabled = 1",
	} {
		if !strings.Contains(manualAdminSnapshotSQL, required) {
			t.Fatalf("management snapshot SQL missing PostgreSQL boolean condition %q: %s", required, manualAdminSnapshotSQL)
		}
	}
	for _, forbidden := range []string{"CASE WHEN ppp.enabled THEN", "WHERE p.enabled\n"} {
		if strings.Contains(manualAdminSnapshotSQL, forbidden) {
			t.Fatalf("management snapshot SQL must use the active Node PostgreSQL integer catalog flags, not %q: %s", forbidden, manualAdminSnapshotSQL)
		}
	}
}

func TestManualAdminSessionTimestampCompatibilityUsesNodeISOText(t *testing.T) {
	if !strings.Contains(manualAdminAuthenticationSQL, "ss.expires_at") || strings.Contains(manualAdminAuthenticationSQL, "clock_timestamp") {
		t.Fatalf("session query must read Node TEXT timestamps without PostgreSQL timestamp comparison: %s", manualAdminAuthenticationSQL)
	}
	instant := time.Date(2026, 8, 26, 12, 34, 56, 987654321, time.FixedZone("UTC+8", 8*60*60))
	if got, want := manualNodeISOTime(instant), "2026-08-26T04:34:56.987Z"; got != want {
		t.Fatalf("Node ISO timestamp=%q want %q", got, want)
	}
}

func TestManualAdminHandlerExecutesDirectlyAndAppendsCompatibleAuditRecord(t *testing.T) {
	report := ProxyTestReport{
		ProxyID: "proxy-1", ProxyName: "东京", Score: 100, Grade: "A", Status: OverallPassed,
		PassedCount: 1, TestedAt: "2026-08-26T00:00:00.000000Z", Message: "代理质量优秀",
		Items: []ProxyTestItem{{Name: "OpenAI", Status: ItemPassed, Message: "HTTP 204"}},
	}
	runner := &fakeManualAdminRunner{report: report}
	source := &fakeManualAdminSource{
		actor: ManualAdminActor{SystemAccountID: "admin-1", Username: "admin", DisplayName: "管理员", Role: "admin"},
		snapshot: manualAdminSnapshot{
			Request: ManualRequest{SchemaVersion: 1, ProxyID: "proxy-1", ProxyName: "东京", ConfigRevision: "2026-08-26T00:00:00.000000Z", ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080, DeadlineMS: 25_000},
			before:  manualAdminTestState{status: "unknown"},
		},
		exists: true,
	}
	audit := &fakeManualAdminAuditAppender{inputs: make(chan operationlogappend.Input, 1)}
	handler := NewManualAdminHandler(runner, source, audit, 25*time.Second, nil)
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/proxies/proxy-1/test", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: manualAdminSessionCookie, Value: "session-token"})
	request.RemoteAddr = "203.0.113.8:443"
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data ProxyTestReport `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.ProxyID != report.ProxyID || runner.input.ProxyID != "proxy-1" {
		t.Fatalf("direct report/input mismatch: %#v / %#v", envelope.Data, runner.input)
	}
	select {
	case input := <-audit.inputs:
		if input.Module != "proxies" || input.Action != "test" || input.OperationKey != "proxies.test" || input.ResourceID != "proxy-1" {
			t.Fatalf("unexpected audit identity: %#v", input)
		}
		if input.ActorSystemAccountID != "admin-1" || input.VisibilityScope != "admin_only" || input.Method != http.MethodPost || input.Path != request.URL.Path {
			t.Fatalf("unexpected audit actor/request metadata: %#v", input)
		}
		if len(input.Changes) == 0 || input.Changes[0].Field != "testStatus" {
			t.Fatalf("expected proxy test changes, got %#v", input.Changes)
		}
	case <-time.After(time.Second):
		t.Fatal("audit append was not scheduled")
	}
}

func TestManualAdminHandlerPreservesLegacyErrorMatrix(t *testing.T) {
	base := func() (*fakeManualAdminRunner, *fakeManualAdminSource, *fakeManualAdminAuditAppender) {
		return &fakeManualAdminRunner{}, &fakeManualAdminSource{
			snapshot: manualAdminSnapshot{Request: ManualRequest{SchemaVersion: 1, ProxyID: "proxy-1", ProxyName: "东京", ConfigRevision: "2026-08-26T00:00:00.000000Z", ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 8080}},
			exists:   true,
		}, &fakeManualAdminAuditAppender{}
	}
	for _, test := range []struct {
		name       string
		configure  func(*fakeManualAdminRunner, *fakeManualAdminSource)
		method     string
		path       string
		wantStatus int
		wantCode   string
		wantRetry  string
	}{
		{name: "invalid token", configure: func(_ *fakeManualAdminRunner, source *fakeManualAdminSource) {
			source.authErr = ErrManualAdminInvalidToken
		}, method: http.MethodPost, path: "/__aisys__/api/proxies/proxy-1/test", wantStatus: http.StatusUnauthorized},
		{name: "must change password", configure: func(_ *fakeManualAdminRunner, source *fakeManualAdminSource) {
			source.authErr = ErrManualAdminMustChange
		}, method: http.MethodPost, path: "/__aisys__/api/proxies/proxy-1/test", wantStatus: http.StatusForbidden, wantCode: "must_change_password"},
		{name: "ordinary user", configure: func(_ *fakeManualAdminRunner, source *fakeManualAdminSource) {
			source.authErr = ErrManualAdminForbidden
		}, method: http.MethodPost, path: "/__aisys__/api/proxies/proxy-1/test", wantStatus: http.StatusForbidden},
		{name: "missing proxy", configure: func(_ *fakeManualAdminRunner, source *fakeManualAdminSource) {
			source.loadErr = ErrManualAdminProxyMissing
		}, method: http.MethodPost, path: "/__aisys__/api/proxies/proxy-1/test", wantStatus: http.StatusNotFound},
		{name: "busy owner", configure: func(runner *fakeManualAdminRunner, _ *fakeManualAdminSource) { runner.err = ErrOwnerLeaseHeld }, method: http.MethodPost, path: "/__aisys__/api/proxies/proxy-1/test", wantStatus: http.StatusServiceUnavailable, wantRetry: "1"},
		{name: "wrong method", configure: func(_ *fakeManualAdminRunner, _ *fakeManualAdminSource) {}, method: http.MethodGet, path: "/__aisys__/api/proxies/proxy-1/test", wantStatus: http.StatusNotFound},
		{name: "wrong path", configure: func(_ *fakeManualAdminRunner, _ *fakeManualAdminSource) {}, method: http.MethodPost, path: "/__aisys__/api/proxies/proxy-1/test/extra", wantStatus: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, source, audit := base()
			test.configure(runner, source)
			handler := NewManualAdminHandler(runner, source, audit, 25*time.Second, nil)
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(`{}`))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if response.Header().Get("Retry-After") != test.wantRetry {
				t.Fatalf("retry-after=%q want=%q", response.Header().Get("Retry-After"), test.wantRetry)
			}
			if test.wantCode != "" && !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("missing code %q in %s", test.wantCode, response.Body.String())
			}
		})
	}
}

func TestManualAdminTokenResolutionMatchesNodeContract(t *testing.T) {
	temporary := "juhe_tmp_" + strings.Repeat("A", 43)
	if token, err := resolveManualAdminToken("Bearer "+temporary, nil); err != nil || token != temporary {
		t.Fatalf("temporary token=%q err=%v", token, err)
	}
	if _, err := resolveManualAdminToken("Bearer ordinary-session", nil); !errors.Is(err, ErrManualAdminInvalidToken) {
		t.Fatalf("wrong bearer error=%v", err)
	}
	if token, err := resolveManualAdminToken("", &http.Cookie{Name: manualAdminSessionCookie, Value: "session-token"}); err != nil || token != "session-token" {
		t.Fatalf("cookie token=%q err=%v", token, err)
	}
	if _, err := resolveManualAdminToken("", nil); !errors.Is(err, ErrManualAdminLoginRequired) {
		t.Fatalf("missing token error=%v", err)
	}
}

func TestManualAdminProxyIDMatchesDecodedExpressPathParam(t *testing.T) {
	if id, ok := manualAdminProxyID("/__aisys__/api/proxies/proxy%2D1/test"); !ok || id != "proxy-1" {
		t.Fatalf("decoded proxy id=%q ok=%v", id, ok)
	}
	if _, ok := manualAdminProxyID("/__aisys__/api/proxies/proxy%2Fother/test"); ok {
		t.Fatal("decoded slash must not be accepted as one proxy ID")
	}
}

func TestLoadManualAdminConfigIsExplicitAndBounded(t *testing.T) {
	if cfg, err := LoadManualAdminConfig(func(string) string { return "" }); err != nil || cfg.Enabled {
		t.Fatalf("disabled config=%#v err=%v", cfg, err)
	}
	env := map[string]string{
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED":                 "true",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS":          "0.0.0.0:3405",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL":            "postgres://example",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_MAX_OPEN_CONNS": "1000",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_MAX_IDLE_CONNS": "1000",
		"JUHE_AI_PROXY_LATENCY_MANAGEMENT_DEADLINE":                "25s",
	}
	cfg, err := LoadManualAdminConfig(func(key string) string { return env[key] })
	if err != nil || !cfg.Enabled || cfg.RequestDeadline != 25*time.Second || cfg.MaxOpenConns != 1000 {
		t.Fatalf("enabled config=%#v err=%v", cfg, err)
	}
	env["JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS"] = "0.0.0.0:not-a-port"
	if _, err := LoadManualAdminConfig(func(key string) string { return env[key] }); err == nil {
		t.Fatal("expected invalid port rejection")
	}
}

func TestManualAdminAuditValueKeepsNodeSafeChangeBound(t *testing.T) {
	if got := manualAdminAuditValue(strings.Repeat("测", 201)); got != strings.Repeat("测", 200)+"…" {
		t.Fatalf("bounded audit value=%#v", got)
	}
}

func TestPostgresManualAdminContractSmoke(t *testing.T) {
	if os.Getenv("J3A_MANUAL_ADMIN_POSTGRES_SMOKE") != "1" {
		t.Skip("set J3A_MANUAL_ADMIN_POSTGRES_SMOKE=1 to verify the configured development PostgreSQL role")
	}
	postgresURL := os.Getenv("JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL")
	if postgresURL == "" {
		t.Fatal("JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL is required for the opt-in smoke")
	}
	db, err := sql.Open("pgx", postgresURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping J3a management PostgreSQL: %v", err)
	}
	source, err := NewPostgresManualAdminSource(db, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.CheckContract(ctx); err != nil {
		t.Fatalf("J3a management PostgreSQL contract: %v", err)
	}
}

func TestPostgresManualAdminSnapshotQuerySmoke(t *testing.T) {
	if os.Getenv("J3A_MANUAL_ADMIN_POSTGRES_SMOKE") != "1" {
		t.Skip("set J3A_MANUAL_ADMIN_POSTGRES_SMOKE=1 to verify the configured development PostgreSQL query")
	}
	postgresURL := os.Getenv("JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL")
	if postgresURL == "" {
		t.Fatal("JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL is required for the opt-in smoke")
	}
	db, err := sql.Open("pgx", postgresURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, manualAdminSnapshotSQL, "__j3a_management_query_probe__")
	if err != nil {
		t.Fatalf("J3a management snapshot query: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close J3a management snapshot query: %v", err)
	}
}
