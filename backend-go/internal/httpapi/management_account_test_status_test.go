package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

func TestAccountTestTaskListBoundedIDs(t *testing.T) {
	stub := &statusHTTPStub{}
	ids := make([]string, 201)
	for i := range ids {
		ids[i] = "task"
	}
	req := testRouteRequest(http.MethodGet, "/test-tasks?ids="+strings.Join(ids, ","), "unused", "")
	rec := httptest.NewRecorder()
	newAccountTestTaskListHandler(stub, managementAccountTestScopeAdmin).ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
func TestAccountTestTaskStatusNotFound(t *testing.T) {
	stub := &statusHTTPStub{}
	req := testRouteRequest(http.MethodGet, "/test-tasks/missing", "taskId", "missing")
	rec := httptest.NewRecorder()
	newAccountTestTaskStatusHandler(stub, managementAccountTestScopeAdmin).ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type statusHTTPStub struct{}

func (*statusHTTPStub) ListTasks(_ *http.Request, ids []string, _ port.ManagementAccountTestAccess) ([]port.ManagementAccountTestTask, error) {
	if len(ids) > 200 {
		return nil, &boundedHTTPError{}
	}
	return nil, nil
}
func (*statusHTTPStub) GetSession(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return port.ManagementAccountTestSession{}, false, nil
}
func (*statusHTTPStub) ListSessionTasks(*http.Request, string, port.ManagementAccountTestAccess) ([]port.ManagementAccountTestTask, bool, error) {
	return nil, false, nil
}
func (*statusHTTPStub) GetTask(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	return port.ManagementAccountTestTask{}, false, nil
}

type boundedHTTPError struct{}

func (*boundedHTTPError) Error() string { return "账户测试任务最多查询 200 项" }
