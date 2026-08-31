package modelcheckowner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckactive"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	_ "modernc.org/sqlite"
)

func TestNewAdminAuthorizeUsesGatewaySessionContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT,display_name TEXT,status TEXT,role TEXT,must_change_password INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT,token_hash TEXT,expires_at TEXT,last_seen_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	token := "juhe_tmp_" + "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLM1234"
	digest := sha256.Sum256([]byte(token))
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO system_accounts VALUES ('sys-1','admin','Admin','active','admin',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_sessions VALUES ('s-1','sys-1',?,?,?)`, hex.EncodeToString(digest[:]), now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	auth, err := modelcheckauth.New(db, modelcheckauth.SQLite, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	authorize := NewAdminAuthorize(auth)
	request := httptest.NewRequest(http.MethodGet, "/run/active", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	got, err := authorize(context.Background(), request)
	if err != nil || got != "sys-1" {
		t.Fatalf("system account=%q err=%v", got, err)
	}
}

func TestNewSelfAuthorizeAllowsAuthenticatedNonAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT,display_name TEXT,status TEXT,role TEXT,must_change_password INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT,token_hash TEXT,expires_at TEXT,last_seen_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	token := "juhe_tmp_" + "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLM1234"
	digest := sha256.Sum256([]byte(token))
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO system_accounts VALUES ('sys-user','user','User','active','user',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_sessions VALUES ('s-user','sys-user',?,?,?)`, hex.EncodeToString(digest[:]), now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	auth, err := modelcheckauth.New(db, modelcheckauth.SQLite, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/run", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	got, err := NewSelfAuthorize(auth)(context.Background(), request)
	if err != nil || got != "sys-user" {
		t.Fatalf("self actor=%q err=%v", got, err)
	}
}

func TestHTTPHandlerMapsForbiddenAdminScopeTo403(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Authorize = func(context.Context, *http.Request) (string, error) {
		return "", modelcheckauth.ErrForbidden
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/run/active", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", response.Code, response.Body.String())
	}
}

func TestHTTPHandlerMapsMustChangePasswordTo403WithCode(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Authorize = func(context.Context, *http.Request) (string, error) {
		return "", modelcheckauth.ErrMustChange
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/run/active", nil))
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"must_change_password"`) {
		t.Fatalf("status=%d body=%s, want 403 must_change_password", response.Code, response.Body.String())
	}
}

type fakeRunService struct{}

type fakeQualityManager struct{}

type fakeBaselineActivator struct {
	input TokenInterceptBaselineActivation
	err   error
}

func (f *fakeBaselineActivator) ActivateTokenInterceptBaseline(_ context.Context, input TokenInterceptBaselineActivation) error {
	f.input = input
	return f.err
}

func (fakeQualityManager) Policy(context.Context, string) (QualityPolicyView, error) {
	return QualityPolicyView{SystemAccountID: "sys-1", Revision: 1, Profile: "quick", ManualEnforcementEnabled: true, PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}, nil
}
func (fakeQualityManager) PatchPolicy(_ context.Context, _ string, p QualityPolicyPatch) (QualityPolicyView, error) {
	return QualityPolicyView{SystemAccountID: "sys-1", Revision: p.ExpectedRevision + 1, Profile: "quick", ManualEnforcementEnabled: true, PenaltyThreshold: 70, PenaltyAction: "fallback", RecoveryIntervalMinutes: 10}, nil
}
func (fakeQualityManager) ListSchedules(context.Context, string, int, int) (QualityScheduleList, error) {
	return QualityScheduleList{}, nil
}
func (fakeQualityManager) CreateSchedule(context.Context, string, QualityScheduleInput) (QualityScheduleView, error) {
	return QualityScheduleView{ID: "sch"}, nil
}
func (fakeQualityManager) PatchSchedule(context.Context, string, string, QualitySchedulePatch) (QualityScheduleView, error) {
	return QualityScheduleView{ID: "sch"}, nil
}
func (fakeQualityManager) DeleteSchedule(context.Context, string, string) (bool, error) {
	return true, nil
}

func (fakeRunService) Run(context.Context, RunRequest) (RunResult, error) {
	return RunResult{RunID: "run-1", Status: "completed"}, nil
}
func (fakeRunService) RunStream(ctx context.Context, request RunRequest, progress func(ProgressEvent)) (RunResult, error) {
	progress(ProgressEvent{Kind: "probe"})
	return RunResult{RunID: "run-1", Status: "completed"}, nil
}
func (fakeRunService) ListRuns(context.Context, RunListQuery) (any, error) {
	return []string{"run-1"}, nil
}
func (fakeRunService) GetRun(context.Context, string) (any, bool, error) {
	return RunView{ID: "run-1", SystemAccountID: "sys-1"}, true, nil
}

type scopedRunService struct {
	query RunListQuery
	view  RunView
	list  any
}

type blockingRunService struct{}

func (blockingRunService) Run(context.Context, RunRequest) (RunResult, error) {
	return RunResult{}, nil
}
func (blockingRunService) RunStream(ctx context.Context, _ RunRequest, _ func(ProgressEvent)) (RunResult, error) {
	<-ctx.Done()
	return RunResult{}, ctx.Err()
}
func (blockingRunService) ListRuns(context.Context, RunListQuery) (any, error) { return nil, nil }
func (blockingRunService) GetRun(context.Context, string) (any, bool, error)   { return nil, false, nil }

type contractRunService struct {
	runResult    RunResult
	runErr       error
	streamResult RunResult
	streamErr    error
	detail       any
	found        bool
	detailErr    error
}

func (s contractRunService) Run(context.Context, RunRequest) (RunResult, error) {
	return s.runResult, s.runErr
}

func (s contractRunService) RunStream(_ context.Context, _ RunRequest, progress func(ProgressEvent)) (RunResult, error) {
	progress(ProgressEvent{Kind: "run_started", Data: map[string]any{"runId": s.streamResult.RunID}})
	return s.streamResult, s.streamErr
}

func (s contractRunService) ListRuns(context.Context, RunListQuery) (any, error) { return nil, nil }
func (s contractRunService) GetRun(context.Context, string) (any, bool, error) {
	return s.detail, s.found, s.detailErr
}

type safeStreamRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   bytes.Buffer
	code   int
}

func (r *safeStreamRecorder) Header() http.Header  { return r.header }
func (r *safeStreamRecorder) WriteHeader(code int) { r.mu.Lock(); r.code = code; r.mu.Unlock() }
func (r *safeStreamRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(data)
}
func (r *safeStreamRecorder) Flush() {}
func (r *safeStreamRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func (s *scopedRunService) Run(context.Context, RunRequest) (RunResult, error) {
	return RunResult{}, nil
}
func (s *scopedRunService) RunStream(context.Context, RunRequest, func(ProgressEvent)) (RunResult, error) {
	return RunResult{}, nil
}
func (s *scopedRunService) ListRuns(_ context.Context, query RunListQuery) (any, error) {
	s.query = query
	if s.list != nil {
		return s.list, nil
	}
	return RunListResult{}, nil
}
func (s *scopedRunService) GetRun(context.Context, string) (any, bool, error) {
	return s.view, true, nil
}

func newTestHTTPHandler() *HTTPHandler {
	return &HTTPHandler{
		Service: fakeRunService{}, Active: modelcheckactive.NewRegistry(),
		Quality:   fakeQualityManager{},
		Authorize: func(context.Context, *http.Request) (string, error) { return "sys-1", nil },
		Build: func(context.Context, string, RunCommand) (RunRequest, error) {
			return RunRequest{SystemAccountID: "sys-1", ActorSystemAccountID: "sys-1", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6", Profile: "quick"}, nil
		},
		Heartbeat: 10 * time.Second,
	}
}

func TestHTTPHandlerQualityPolicyAndScheduleRoutes(t *testing.T) {
	handler := newTestHTTPHandler()
	policy := httptest.NewRecorder()
	handler.ServeHTTP(policy, httptest.NewRequest(http.MethodGet, "/quality-policy", nil))
	if policy.Code != http.StatusOK || !strings.Contains(policy.Body.String(), `"revision":1`) {
		t.Fatalf("policy status=%d body=%s", policy.Code, policy.Body.String())
	}
	patch := httptest.NewRecorder()
	handler.ServeHTTP(patch, httptest.NewRequest(http.MethodPatch, "/quality-policy", strings.NewReader(`{"expectedRevision":1,"penaltyThreshold":75}`)))
	if patch.Code != http.StatusOK || !strings.Contains(patch.Body.String(), `"revision":2`) {
		t.Fatalf("patch status=%d body=%s", patch.Code, patch.Body.String())
	}
	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/quality-schedules", strings.NewReader(`{"accountId":"acct","model":"gpt-5.6","intervalMinutes":60,"profile":"quick","penaltyThreshold":70,"penaltyAction":"fallback","recoveryIntervalMinutes":10}`)))
	if create.Code != http.StatusOK || !strings.Contains(create.Body.String(), `"id":"sch"`) {
		t.Fatalf("schedule status=%d body=%s", create.Code, create.Body.String())
	}
}

func TestHTTPHandlerTokenInterceptBaselineActivationContract(t *testing.T) {
	handler := newTestHTTPHandler()
	activator := &fakeBaselineActivator{}
	handler.Baseline = activator
	body := `{"cohortKeyHmac":"hmac-sha256-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","requestedModel":"gpt-5.6","tokenizerVersion":"o200k_base@1","probeSetVersion":"probe-v1","baselineVersion":2,"strongThresholdIntercept":128,"calibrationNote":"calibrated"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/token-intercept-baselines/activate", strings.NewReader(body)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"activated":true`) || activator.input.BaselineVersion != 2 {
		t.Fatalf("activation status=%d body=%s input=%+v", response.Code, response.Body.String(), activator.input)
	}
	for _, invalid := range []string{
		`{"cohortKeyHmac":"bad","requestedModel":"gpt-5.6","tokenizerVersion":"o200k_base@1","probeSetVersion":"probe-v1","baselineVersion":2,"strongThresholdIntercept":128,"calibrationNote":"ok"}`,
		`{"cohortKeyHmac":"hmac-sha256-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","requestedModel":"gpt-5.6","tokenizerVersion":"o200k_base@1","probeSetVersion":"probe-v1","baselineVersion":2,"strongThresholdIntercept":128,"calibrationNote":"ok","unexpected":true}`,
	} {
		invalidResponse := httptest.NewRecorder()
		handler.ServeHTTP(invalidResponse, httptest.NewRequest(http.MethodPost, "/token-intercept-baselines/activate", strings.NewReader(invalid)))
		if invalidResponse.Code != http.StatusBadRequest {
			t.Fatalf("invalid activation status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
		}
	}
}

func TestHTTPHandlerTokenInterceptBaselineMapsConflictAndUnavailable(t *testing.T) {
	validBody := `{"cohortKeyHmac":"hmac-sha256-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","requestedModel":"gpt-5.6","tokenizerVersion":"o200k_base@1","probeSetVersion":"probe-v1","baselineVersion":2,"strongThresholdIntercept":128,"calibrationNote":"calibrated"}`
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "conflict", err: ErrTokenInterceptBaselineConflict, status: http.StatusConflict},
		{name: "unavailable", err: ErrTokenInterceptBaselineUnavailable, status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newTestHTTPHandler()
			handler.Baseline = &fakeBaselineActivator{err: tc.err}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/token-intercept-baselines/activate", strings.NewReader(validBody)))
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s want=%d", response.Code, response.Body.String(), tc.status)
			}
		})
	}
	handler := newTestHTTPHandler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/token-intercept-baselines/activate", strings.NewReader(validBody)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil baseline owner status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPHandlerJSONAndActiveStopContract(t *testing.T) {
	handler := newTestHTTPHandler()
	run := httptest.NewRecorder()
	handler.ServeHTTP(run, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if run.Code != http.StatusServiceUnavailable || !strings.Contains(run.Body.String(), "完整持久化报告尚未就绪") {
		t.Fatalf("run status=%d body=%s", run.Code, run.Body.String())
	}
	active := httptest.NewRecorder()
	_, acquired, _ := handler.Active.TryStart(context.Background(), "system-account:sys-1", modelcheckactive.Summary{RunID: "run-active"})
	if !acquired {
		t.Fatal("failed to seed active run")
	}
	handler.ServeHTTP(active, httptest.NewRequest(http.MethodGet, "/run/active", nil))
	if active.Code != http.StatusOK || !strings.Contains(active.Body.String(), `"runId":"run-active"`) {
		t.Fatalf("active status=%d body=%s", active.Code, active.Body.String())
	}
	stop := httptest.NewRecorder()
	handler.ServeHTTP(stop, httptest.NewRequest(http.MethodPost, "/run/stop", nil))
	if stop.Code != http.StatusOK || !strings.Contains(stop.Body.String(), `"stopped":true`) {
		t.Fatalf("stop status=%d body=%s", stop.Code, stop.Body.String())
	}
}

func TestHTTPHandlerJSONRejectsConcurrentActiveRun(t *testing.T) {
	handler := newTestHTTPHandler()
	_, acquired, _ := handler.Active.TryStart(context.Background(), "system-account:sys-1", modelcheckactive.Summary{RunID: "existing"})
	if !acquired {
		t.Fatal("failed to seed active run")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusConflict || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestHTTPHandlerRunReturnsDurableCompleteDetailWithoutFabricatingFields(t *testing.T) {
	handler := newTestHTTPHandler()
	detail := map[string]any{
		"id":              "run-detail",
		"systemAccountId": "sys-1",
		"requestSummary":  map[string]any{"targetId": "acct-1"},
		"resultSummary":   map[string]any{"score": 100},
		"checks":          []any{map[string]any{"id": "check-1"}},
	}
	handler.Service = contractRunService{
		runResult: RunResult{RunID: "run-detail", Status: "completed"},
		detail:    detail,
		found:     true,
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusOK || response.Header().Get("X-Juhe-Model-Check-Detail") != "" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"id", "requestSummary", "resultSummary", "checks"} {
		if _, ok := body.Data[field]; !ok {
			t.Fatalf("durable detail missing %q: %s", field, response.Body.String())
		}
	}
}

func TestHTTPHandlerRunFailsClosedWhenDurableDetailIsUnavailable(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = contractRunService{runResult: RunResult{RunID: "run-missing", Status: "completed"}}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Message == "" || body.Message != body.Error.Message {
		t.Fatalf("Node root envelope / legacy compatibility missing: %#v", body)
	}
}

func TestHTTPHandlerRunFailsClosedWhenOnlySummaryIsAvailable(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = contractRunService{
		runResult: RunResult{RunID: "run-summary", Status: "completed"},
		detail:    RunView{ID: "run-summary"},
		found:     true,
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "完整持久化报告尚未就绪") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPHandlerRunPreservesRequestErrorStatusAndEnvelope(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = contractRunService{runErr: &RequestError{StatusCode: http.StatusNotFound, Message: "账户不存在或无权检测"}}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"message":"账户不存在或无权检测"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPHandlerBuildPreservesRequestErrorStatusAndEnvelope(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Build = func(context.Context, string, RunCommand) (RunRequest, error) {
		return RunRequest{}, &RequestError{StatusCode: http.StatusNotFound, Message: "账户不存在或无权检测"}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-missing","model":"gpt-5.6"}`)))
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"message":"账户不存在或无权检测"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPHandlerRejectsBuildScopeDrift(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Build = func(context.Context, string, RunCommand) (RunRequest, error) {
		return RunRequest{SystemAccountID: "other", ActorSystemAccountID: "actor", TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6", Profile: "quick"}, nil
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("scope drift status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDecodeRunCommandRejectsClientOwnedAndTrailingFields(t *testing.T) {
	for _, body := range []string{
		`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6","providerCode":"openai"}`,
		`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}{"targetType":"account"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
		if _, err := decodeRunCommand(request, 1<<20); err == nil {
			t.Fatalf("request %q must be rejected", body)
		}
	}
}

func TestDecodeRunCommandValidatesTrustedComparisonContract(t *testing.T) {
	valid := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6","trustedComparison":true,"trustedComparisonAccountId":"acct-2"}`))
	command, err := decodeRunCommand(valid, 1<<20)
	if err != nil || !command.TrustedComparison || command.TrustedComparisonID != "acct-2" {
		t.Fatalf("valid trusted comparison command=%+v err=%v", command, err)
	}
	for _, body := range []string{
		`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6","trustedComparison":true}`,
		`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6","trustedComparisonAccountId":"acct-2"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
		if _, err := decodeRunCommand(request, 1<<20); err == nil {
			t.Fatalf("invalid trusted comparison request %q must be rejected", body)
		}
	}
}

func TestHTTPHandlerSSEAndConflictContract(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = contractRunService{
		streamResult: RunResult{RunID: "run-1", Status: "completed"},
		detail: map[string]any{
			"id":              "run-1",
			"systemAccountId": "sys-1",
			"requestSummary":  map[string]any{"targetId": "acct-1"},
			"resultSummary":   map[string]any{"score": 100},
			"checks":          []any{map[string]any{"id": "check-1"}},
		},
		found: true,
	}
	stream := httptest.NewRecorder()
	handler.ServeHTTP(stream, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if stream.Code != http.StatusOK || !strings.Contains(stream.Header().Get("Content-Type"), "text/event-stream") || stream.Header().Get("Cache-Control") != "no-cache, no-transform" || stream.Header().Get("Connection") != "keep-alive" || stream.Header().Get("X-Accel-Buffering") != "no" || !strings.Contains(stream.Body.String(), "event: complete") {
		t.Fatalf("stream status=%d headers=%v body=%s", stream.Code, stream.Header(), stream.Body.String())
	}
	_, acquired, _ := handler.Active.TryStart(context.Background(), "system-account:sys-1", modelcheckactive.Summary{RunID: "existing"})
	if !acquired {
		t.Fatal("failed to seed conflict run")
	}
	conflict := httptest.NewRecorder()
	handler.ServeHTTP(conflict, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if conflict.Code != http.StatusConflict || conflict.Header().Get("Retry-After") != "1" {
		t.Fatalf("conflict status=%d headers=%v body=%s", conflict.Code, conflict.Header(), conflict.Body.String())
	}
}

func TestHTTPHandlerSSERequestErrorIncludesNodeStatusCode(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = contractRunService{
		streamResult: RunResult{RunID: "run-stream"},
		streamErr:    &RequestError{StatusCode: http.StatusBadRequest, Message: "检测目标不能为空"},
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: error") || !strings.Contains(response.Body.String(), `"statusCode":400`) || !strings.Contains(response.Body.String(), `"message":"检测目标不能为空"`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestHTTPHandlerSSEGenericErrorDoesNotInventStatusCode(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = contractRunService{
		streamResult: RunResult{RunID: "run-stream"},
		streamErr:    errors.New("上游探针连接失败"),
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: error") || strings.Contains(response.Body.String(), `"statusCode"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPHandlerSSEFailsClosedWhenOnlySummaryIsAvailable(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = contractRunService{
		streamResult: RunResult{RunID: "run-summary", Status: "completed"},
		detail:       RunView{ID: "run-summary"},
		found:        true,
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: error") || strings.Contains(response.Body.String(), "event: complete") || !strings.Contains(response.Body.String(), "完整持久化报告尚未就绪") || !strings.Contains(response.Body.String(), `"statusCode":503`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPHandlerSSEDetailReadErrorIncludesNodeStatusCode(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = contractRunService{
		streamResult: RunResult{RunID: "run-stream", Status: "completed"},
		detailErr:    &RequestError{StatusCode: http.StatusServiceUnavailable, Message: "持久化报告读取失败"},
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: error") || strings.Contains(response.Body.String(), "event: complete") || !strings.Contains(response.Body.String(), `"statusCode":503`) || !strings.Contains(response.Body.String(), `"message":"持久化报告读取失败"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHTTPHandlerScopesDetailAndParsesPagination(t *testing.T) {
	service := &scopedRunService{view: RunView{ID: "run-other", SystemAccountID: "sys-2"}}
	handler := newTestHTTPHandler()
	handler.Service = service
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/runs?page=3&pageSize=27", nil))
	if list.Code != http.StatusOK || service.query.SystemAccountID != "sys-1" || service.query.Page != 3 || service.query.PageSize != 27 {
		t.Fatalf("list status=%d query=%#v", list.Code, service.query)
	}
	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/runs/run-other", nil))
	if detail.Code != http.StatusNotFound {
		t.Fatalf("cross-account detail status=%d body=%s", detail.Code, detail.Body.String())
	}
	for _, query := range []string{"?page=0", "?page=not-a-number", "?pageSize=0", "?pageSize=1001", "?pageSize=bad"} {
		invalid := httptest.NewRecorder()
		handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/runs"+query, nil))
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid pagination %s status=%d body=%s", query, invalid.Code, invalid.Body.String())
		}
	}
}

func TestHTTPHandlerSelfScopeRedactsTenantFieldsFromListAndDetail(t *testing.T) {
	service := &scopedRunService{
		view: RunView{ID: "run-1", SystemAccountID: "sys-1"},
		list: RunListResult{Items: []RunView{{ID: "run-1", SystemAccountID: "sys-1"}}},
	}
	handler := newTestHTTPHandler()
	handler.Service = service
	handler.ForceActorScope = true
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/runs", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "systemAccountId") {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/runs/run-1", nil))
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), "systemAccountId") {
		t.Fatalf("detail status=%d body=%s", detail.Code, detail.Body.String())
	}
}

func TestHTTPHandlerSelfScopeRedactsNestedTenantFieldsFromCompletedRun(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.ForceActorScope = true
	handler.Service = contractRunService{
		runResult: RunResult{RunID: "run-1", Status: "completed"},
		detail: map[string]any{
			"id":                   "run-1",
			"systemAccountId":      "sys-1",
			"actorSystemAccountId": "sys-1",
			"requestSummary":       map[string]any{"systemAccountId": "sys-1"},
			"resultSummary":        map[string]any{"targetOwnerSystemAccountId": "sys-owner"},
			"checks":               []any{map[string]any{"id": "check-1"}},
		},
		found: true,
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "systemAccountId") || strings.Contains(response.Body.String(), "actorSystemAccountId") || strings.Contains(response.Body.String(), "targetOwnerSystemAccountId") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestResolveManagementScopeSeparatesActorAndSelectedTenant(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/runs", nil)
	scope, err := resolveManagementScope(request, "sys-1", false, false)
	if err != nil || scope.ActorSystemAccountID != "sys-1" || scope.SelectedSystemAccountID != "sys-1" || scope.AllSystemAccounts {
		t.Fatalf("default scope=%+v err=%v", scope, err)
	}
	request = httptest.NewRequest(http.MethodGet, "/runs?systemAccountId=all", nil)
	scope, err = resolveManagementScope(request, "sys-1", true, false)
	if err != nil || scope.ActorSystemAccountID != "sys-1" || scope.SelectedSystemAccountID != "" || !scope.AllSystemAccounts {
		t.Fatalf("global scope=%+v err=%v", scope, err)
	}
	request = httptest.NewRequest(http.MethodGet, "/runs?systemAccountId=sys-2", nil)
	scope, err = resolveManagementScope(request, "sys-1", true, false)
	if err != nil || scope.ActorSystemAccountID != "sys-1" || scope.SelectedSystemAccountID != "sys-2" || scope.AllSystemAccounts {
		t.Fatalf("selected scope=%+v err=%v", scope, err)
	}
	if _, err := resolveManagementScope(request, "sys-1", false, false); err == nil {
		t.Fatal("self mount foreign scope must remain rejected")
	}
	request = httptest.NewRequest(http.MethodGet, "/runs?systemAccountId=sys-1&systemAccountId=sys-1", nil)
	if _, err := resolveManagementScope(request, "sys-1", true, false); err == nil {
		t.Fatal("duplicate system-account scope must be rejected")
	}
}

func TestHTTPHandlerAdminScopeForwardsGlobalAndForeignReadSelection(t *testing.T) {
	for _, requested := range []string{"all", "sys-2"} {
		t.Run(requested, func(t *testing.T) {
			handler := newTestHTTPHandler()
			handler.AllowCrossAccount = true
			service := &scopedRunService{}
			handler.Service = service
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/runs?systemAccountId="+requested, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if requested == "all" && (!service.query.AllSystemAccounts || service.query.SystemAccountID != "") {
				t.Fatalf("global query=%+v", service.query)
			}
			if requested == "sys-2" && (service.query.AllSystemAccounts || service.query.SystemAccountID != "sys-2") {
				t.Fatalf("selected query=%+v", service.query)
			}
		})
	}
}

func TestHTTPHandlerGlobalScopeBuildsTargetTenantAndKeepsQualitySpecific(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.AllowCrossAccount = true
	var builtScope ManagementScope
	handler.BuildScoped = func(_ context.Context, scope ManagementScope, command RunCommand) (RunRequest, error) {
		builtScope = scope
		return RunRequest{SystemAccountID: "sys-2", ActorSystemAccountID: "sys-1", TargetType: command.TargetType, TargetID: command.TargetID, Model: command.Model, Profile: "quick"}, nil
	}
	handler.Service = contractRunService{
		runResult: RunResult{RunID: "run-global", Status: "completed"},
		detail:    RunDetail{RunView: RunView{ID: "run-global", SystemAccountID: "sys-2"}, RequestSummary: json.RawMessage(`{}`), ResultSummary: json.RawMessage(`{}`), Checks: []RunCheck{}},
		found:     true,
	}
	run := httptest.NewRecorder()
	handler.ServeHTTP(run, httptest.NewRequest(http.MethodPost, "/run?systemAccountId=all", strings.NewReader(`{"targetType":"account","targetId":"acct-2","model":"gpt-5.6"}`)))
	if run.Code != http.StatusOK || !builtScope.AllSystemAccounts || builtScope.ActorSystemAccountID != "sys-1" {
		t.Fatalf("run status=%d scope=%+v body=%s", run.Code, builtScope, run.Body.String())
	}
	quality := httptest.NewRecorder()
	handler.ServeHTTP(quality, httptest.NewRequest(http.MethodGet, "/quality-policy?systemAccountId=all", nil))
	if quality.Code != http.StatusBadRequest || !strings.Contains(quality.Body.String(), "请先选择具体系统账户") {
		t.Fatalf("quality status=%d body=%s", quality.Code, quality.Body.String())
	}
}

func TestHTTPHandlerSSEEmitsHeartbeatWhileRuntimeIsRunning(t *testing.T) {
	handler := newTestHTTPHandler()
	handler.Service = blockingRunService{}
	handler.Heartbeat = time.Millisecond
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)).WithContext(requestContext)
	response := &safeStreamRecorder{header: make(http.Header)}
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	deadline := time.After(250 * time.Millisecond)
	for !strings.Contains(response.String(), ": heartbeat") {
		select {
		case <-deadline:
			t.Fatalf("SSE body=%q, heartbeat missing", response.String())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop after request cancellation")
	}
}
