package modelcheckowner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

type fakeRunService struct{}

type fakeQualityManager struct{}

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

func TestHTTPHandlerJSONAndActiveStopContract(t *testing.T) {
	handler := newTestHTTPHandler()
	run := httptest.NewRecorder()
	handler.ServeHTTP(run, httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if run.Code != http.StatusOK || !strings.Contains(run.Body.String(), `"runId":"run-1"`) {
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
	stream := httptest.NewRecorder()
	handler.ServeHTTP(stream, httptest.NewRequest(http.MethodPost, "/run/stream", strings.NewReader(`{"targetType":"account","targetId":"acct-1","model":"gpt-5.6"}`)))
	if stream.Code != http.StatusOK || !strings.Contains(stream.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(stream.Body.String(), "event: complete") {
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
