package modelcheckowner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckactive"
)

type fakeRunService struct{}

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
		Authorize: func(context.Context, *http.Request) (string, error) { return "sys-1", nil },
		Build: func(context.Context, string, RunCommand) (RunRequest, error) {
			return RunRequest{TargetType: "account", TargetID: "acct-1", Model: "gpt-5.6", Profile: "quick"}, nil
		},
		Heartbeat: 10 * time.Second,
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
