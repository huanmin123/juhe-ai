package httpapi

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementaccountteststatus"
	"juhe-ai/backend-go/internal/store/port"
)

type accountTestStatusService interface {
	ListTasks(*http.Request, []string, port.ManagementAccountTestAccess) ([]port.ManagementAccountTestTask, error)
	GetSession(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error)
	ListSessionTasks(*http.Request, string, port.ManagementAccountTestAccess) ([]port.ManagementAccountTestTask, bool, error)
	GetTask(*http.Request, string, port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error)
}
type accountTestStatusAdapter struct {
	service *managementaccountteststatus.Service
}

func (a accountTestStatusAdapter) ListTasks(r *http.Request, ids []string, x port.ManagementAccountTestAccess) ([]port.ManagementAccountTestTask, error) {
	return a.service.ListTasks(r.Context(), ids, x)
}
func (a accountTestStatusAdapter) GetSession(r *http.Request, id string, x port.ManagementAccountTestAccess) (port.ManagementAccountTestSession, bool, error) {
	return a.service.GetSession(r.Context(), id, x)
}
func (a accountTestStatusAdapter) ListSessionTasks(r *http.Request, id string, x port.ManagementAccountTestAccess) ([]port.ManagementAccountTestTask, bool, error) {
	return a.service.ListSessionTasks(r.Context(), id, x)
}
func (a accountTestStatusAdapter) GetTask(r *http.Request, id string, x port.ManagementAccountTestAccess) (port.ManagementAccountTestTask, bool, error) {
	return a.service.GetTask(r.Context(), id, x)
}

func NewManagementAccountTestTaskListHandler(s *managementaccountteststatus.Service) http.Handler {
	return newAccountTestTaskListHandler(accountTestStatusAdapter{s}, managementAccountTestScopeAdmin)
}
func NewManagementMyAccountTestTaskListHandler(s *managementaccountteststatus.Service) http.Handler {
	return newAccountTestTaskListHandler(accountTestStatusAdapter{s}, managementAccountTestScopeSelf)
}
func NewManagementAccountTestSessionStatusHandler(s *managementaccountteststatus.Service) http.Handler {
	return newAccountTestSessionStatusHandler(accountTestStatusAdapter{s}, managementAccountTestScopeAdmin)
}
func NewManagementMyAccountTestSessionStatusHandler(s *managementaccountteststatus.Service) http.Handler {
	return newAccountTestSessionStatusHandler(accountTestStatusAdapter{s}, managementAccountTestScopeSelf)
}
func NewManagementAccountTestSessionTasksHandler(s *managementaccountteststatus.Service) http.Handler {
	return newAccountTestSessionTasksHandler(accountTestStatusAdapter{s}, managementAccountTestScopeAdmin)
}
func NewManagementMyAccountTestSessionTasksHandler(s *managementaccountteststatus.Service) http.Handler {
	return newAccountTestSessionTasksHandler(accountTestStatusAdapter{s}, managementAccountTestScopeSelf)
}
func NewManagementAccountTestTaskStatusHandler(s *managementaccountteststatus.Service) http.Handler {
	return newAccountTestTaskStatusHandler(accountTestStatusAdapter{s}, managementAccountTestScopeAdmin)
}
func NewManagementMyAccountTestTaskStatusHandler(s *managementaccountteststatus.Service) http.Handler {
	return newAccountTestTaskStatusHandler(accountTestStatusAdapter{s}, managementAccountTestScopeSelf)
}

func newAccountTestTaskListHandler(s accountTestStatusService, scope managementAccountTestScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := accountTestAccess(w, r, scope)
		if !ok {
			return
		}
		if len(r.URL.RawQuery) > 8192 {
			writeMessageError(w, 400, "查询参数过长")
			return
		}
		tasks, err := s.ListTasks(r, accountTestTaskIDs(r), a)
		if err != nil {
			writeMessageError(w, 400, err.Error())
			return
		}
		writeData(w, 200, tasks)
	})
}
func newAccountTestSessionStatusHandler(s accountTestStatusService, scope managementAccountTestScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := accountTestAccess(w, r, scope)
		if !ok {
			return
		}
		v, found, err := s.GetSession(r, chi.URLParam(r, "sessionId"), a)
		writeAccountTestStatusResult(w, v, found, err, "账户测试会话不存在")
	})
}
func newAccountTestSessionTasksHandler(s accountTestStatusService, scope managementAccountTestScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := accountTestAccess(w, r, scope)
		if !ok {
			return
		}
		v, found, err := s.ListSessionTasks(r, chi.URLParam(r, "sessionId"), a)
		writeAccountTestStatusResult(w, v, found, err, "账户测试会话不存在")
	})
}
func newAccountTestTaskStatusHandler(s accountTestStatusService, scope managementAccountTestScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := accountTestAccess(w, r, scope)
		if !ok {
			return
		}
		v, found, err := s.GetTask(r, chi.URLParam(r, "taskId"), a)
		writeAccountTestStatusResult(w, v, found, err, "账户测试任务不存在")
	})
}
func writeAccountTestStatusResult(w http.ResponseWriter, v any, found bool, err error, notFound string) {
	if err != nil {
		writeMessageError(w, 400, err.Error())
		return
	}
	if !found {
		writeMessageError(w, 404, notFound)
		return
	}
	writeData(w, 200, v)
}
func accountTestTaskIDs(r *http.Request) []string {
	var ids []string
	for _, value := range r.URL.Query()["ids"] {
		ids = append(ids, strings.Split(value, ",")...)
	}
	return ids
}
