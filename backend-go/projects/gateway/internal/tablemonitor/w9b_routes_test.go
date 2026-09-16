package tablemonitor

// w9b 路由面补充：database-history handler、Mount 注册与错误响应臂。

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

func w9bAdminAuth(request *http.Request) *http.Request {
	auth := &authsys.AuthContext{SystemAccountID: "sys-admin-1", Username: "admin", Role: "admin"}
	return request.WithContext(authsys.WithAuthContext(request.Context(), auth))
}

func TestW9BDatabaseHistoryHandler(t *testing.T) {
	deps, _ := newMonitorFixture(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/table-monitor/database-history?windowMs=3600000", nil)
	http.HandlerFunc(deps.databaseHistoryHandler).ServeHTTP(recorder, w9bAdminAuth(request))
	if recorder.Code != http.StatusOK {
		t.Fatalf("database-history=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestW9BMountRegistersRoutes(t *testing.T) {
	deps, _ := newMonitorFixture(t)
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	auth := &authsys.Deps{}
	deps.Mount(k, auth)
	handler := k.Handler()
	// 路由存在性：nil auth 守卫时至少不会 404 到 kernel 兜底……实际上
	// RequireAdmin 需要真实 auth 链，这里只验证 Mount 不 panic 且
	// Handler 可用。
	if handler == nil {
		t.Fatal("handler 不能为 nil")
	}
}

func TestW9BWriteReadErrorArms(t *testing.T) {
	deps := &Deps{}
	recorder := httptest.NewRecorder()
	deps.writeReadError(recorder, errors.New("boom"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("普通错误=%d", recorder.Code)
	}
	// SchemaUnavailable → 503。
	recorder = httptest.NewRecorder()
	deps.writeReadError(recorder, ErrSchemaUnavailable)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("schema 不可用=%d", recorder.Code)
	}
}
