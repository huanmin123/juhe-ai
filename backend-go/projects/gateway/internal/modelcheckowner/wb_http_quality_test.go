package modelcheckowner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// wbQualityStub 允许逐方法注入返回值与错误，用于验证 HTTP 错误语义映射。
type wbQualityStub struct {
	policy        QualityPolicyView
	policyErr     error
	patchPolicy   QualityPolicyView
	patchPolicyEr error
	schedules     QualityScheduleList
	listErr       error
	created       QualityScheduleView
	createErr     error
	patched       QualityScheduleView
	patchErr      error
	deleted       bool
	deleteErr     error
}

func (s *wbQualityStub) Policy(context.Context, string) (QualityPolicyView, error) {
	return s.policy, s.policyErr
}
func (s *wbQualityStub) PatchPolicy(context.Context, string, QualityPolicyPatch) (QualityPolicyView, error) {
	return s.patchPolicy, s.patchPolicyEr
}
func (s *wbQualityStub) ListSchedules(context.Context, string, int, int) (QualityScheduleList, error) {
	return s.schedules, s.listErr
}
func (s *wbQualityStub) CreateSchedule(context.Context, string, QualityScheduleInput) (QualityScheduleView, error) {
	return s.created, s.createErr
}
func (s *wbQualityStub) PatchSchedule(context.Context, string, string, QualitySchedulePatch) (QualityScheduleView, error) {
	return s.patched, s.patchErr
}
func (s *wbQualityStub) DeleteSchedule(context.Context, string, string) (bool, error) {
	return s.deleted, s.deleteErr
}

// wbAccountOptionsStub 提供只读的账户选项 owner。
type wbAccountOptionsStub struct {
	items  []AccountOption
	listEr error
}

func (s *wbAccountOptionsStub) ListAccountOptions(context.Context, AccountOptionsQuery) ([]AccountOption, error) {
	return s.items, s.listEr
}
func (s *wbAccountOptionsStub) ModelCheckOptions() ModelCheckOptions {
	return ModelCheckOptions{DefaultModel: "gpt-5.6-sol", DefaultProfile: "quick", SupportedModels: []ModelCheckSupportedOption{{Value: "gpt-5.6-sol", Label: "GPT"}}}
}

// PATCH /quality-schedules/{id} 契约：空 ID 404、请求体无效 400、
// 质量错误按冲突/不存在/参数错误映射 409/404/400。
func TestWBHTTPPatchQualityScheduleRouteMapsErrorSemantics(t *testing.T) {
	validBody := strings.NewReader(`{"expectedRevision":1,"intervalMinutes":60}`)
	cases := []struct {
		name   string
		path   string
		body   string
		stub   wbQualityStub
		status int
		need   string
	}{
		{name: "success", path: "/quality-schedules/sch-1", body: `{"expectedRevision":1,"intervalMinutes":60}`, stub: wbQualityStub{patched: QualityScheduleView{ID: "sch-1"}}, status: http.StatusOK, need: `"id":"sch-1"`},
		{name: "empty id", path: "/quality-schedules/", body: `{}`, status: http.StatusNotFound},
		{name: "invalid body", path: "/quality-schedules/sch-1", body: `{"unexpected":true}`, status: http.StatusBadRequest},
		{name: "conflict", path: "/quality-schedules/sch-1", body: `{"expectedRevision":1}`, stub: wbQualityStub{patchErr: errors.New("定时检查配置已被其他操作修改")}, status: http.StatusConflict, need: "已被其他操作修改"},
		{name: "missing schedule", path: "/quality-schedules/sch-1", body: `{"expectedRevision":1}`, stub: wbQualityStub{patchErr: errors.New("定时检查配置不存在")}, status: http.StatusNotFound, need: "不存在"},
		{name: "invalid values", path: "/quality-schedules/sch-1", body: `{"expectedRevision":1,"intervalMinutes":1}`, stub: wbQualityStub{patchErr: errors.New("定时检查间隔必须是 10 到 10080 的整数分钟")}, status: http.StatusBadRequest, need: "10 到 10080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := newTestHTTPHandler()
			handler.Quality = &tc.stub
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, tc.path, strings.NewReader(tc.body)))
			if response.Code != tc.status || (tc.need != "" && !strings.Contains(response.Body.String(), tc.need)) {
				t.Fatalf("status=%d body=%s want=%d need=%q", response.Code, response.Body.String(), tc.status, tc.need)
			}
		})
	}
	t.Run("quality owner missing", func(t *testing.T) {
		handler := newTestHTTPHandler()
		handler.Quality = nil
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/quality-schedules/sch-1", validBody))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "质量管理 owner 未完成接线") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

// DELETE /quality-schedules/{id} 契约：成功返回 deleted:true，
// 删除未命中返回 404，存储错误返回 500。
func TestWBHTTPDeleteQualityScheduleRouteMapsErrorSemantics(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		stub   wbQualityStub
		status int
		need   string
	}{
		{name: "success", path: "/quality-schedules/sch-1", stub: wbQualityStub{deleted: true}, status: http.StatusOK, need: `"deleted":true`},
		{name: "empty id", path: "/quality-schedules/", status: http.StatusNotFound},
		{name: "not deleted", path: "/quality-schedules/sch-1", stub: wbQualityStub{deleted: false}, status: http.StatusNotFound},
		{name: "storage error", path: "/quality-schedules/sch-1", stub: wbQualityStub{deleteErr: errors.New("存储不可用")}, status: http.StatusInternalServerError, need: "存储不可用"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := newTestHTTPHandler()
			handler.Quality = &tc.stub
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, tc.path, nil))
			if response.Code != tc.status || (tc.need != "" && !strings.Contains(response.Body.String(), tc.need)) {
				t.Fatalf("status=%d body=%s want=%d need=%q", response.Code, response.Body.String(), tc.status, tc.need)
			}
		})
	}
}

// 全局 scope 契约：质量定时任务的 PATCH/DELETE 必须落在具体系统账户上，
// 管理员全局 scope 一律拒绝。
func TestWBHTTPScopedQualityScheduleRoutesRejectGlobalScope(t *testing.T) {
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPatch, "/quality-schedules/sch-1?systemAccountId=all"},
		{http.MethodDelete, "/quality-schedules/sch-1?systemAccountId=all"},
	} {
		t.Run(route.method, func(t *testing.T) {
			handler := newTestHTTPHandler()
			handler.AllowCrossAccount = true
			handler.Quality = &wbQualityStub{deleted: true, patched: QualityScheduleView{ID: "sch-1"}}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, strings.NewReader(`{"expectedRevision":1}`)))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "请先选择具体系统账户") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

// PATCH /quality-policy 的错误语义同样经过 writeQualityError 映射。
func TestWBHTTPPatchQualityPolicyMapsQualityErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{name: "conflict", err: errors.New("配置已变化"), status: http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := newTestHTTPHandler()
			handler.Quality = &wbQualityStub{patchPolicyEr: tc.err}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/quality-policy", strings.NewReader(`{"expectedRevision":1,"penaltyThreshold":80}`)))
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s want=%d", response.Code, response.Body.String(), tc.status)
			}
		})
	}
	t.Run("nil error direct", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		writeQualityError(recorder, nil)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "质量管理操作失败") {
			t.Fatalf("writeQualityError(nil) status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
}

// GET /options 契约：owner 未接线 503；已接线返回静态目录选项。
func TestWBHTTPOptionsRouteServesModelCheckCatalog(t *testing.T) {
	t.Run("owner missing", func(t *testing.T) {
		handler := newTestHTTPHandler()
		handler.AccountOptions = nil
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/options", nil))
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "账户选项 owner 未完成接线") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
	t.Run("serves catalog", func(t *testing.T) {
		handler := newTestHTTPHandler()
		handler.AccountOptions = &wbAccountOptionsStub{}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/options", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"defaultModel":"gpt-5.6-sol"`) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}
