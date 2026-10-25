package aipublic

// Node 对齐契约测试：DB 基础设施失败走全局 handler 语义
//（500 {message}，无 code 字段），nowISO 毫秒精度（nowIso() =
// new Date().toISOString()）。T65 迁移后载具为 pgtestkit 空 schema 隔离库
//（表缺失即存储基础设施失败，与原 SQLite :memory: 空库同一断言语义）。

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// TestGuardDBFailureOmitsCode：token 查询失败（表缺失）时 Node 经
// next(error) 进全局错误处理，返回 500 {"message":"服务器内部错误"}，
// 不带 external_source_internal_error。
func TestGuardDBFailureOmitsCode(t *testing.T) {
	db := pgtestkit.New(t)
	deps := &Deps{DB: db, PGDialect: true, Now: time.Now}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, Prefix+"/group/list", nil)
	request.Header.Set("Authorization", "Bearer some-token")
	// guard 的内层 handler 只在鉴权成功后执行。
	handlerRan := false
	deps.guard(scopeGroupListRead, func(w http.ResponseWriter, r *http.Request) {
		handlerRan = true
	}).ServeHTTP(recorder, request)
	if handlerRan {
		t.Fatalf("handler must not run on auth failure")
	}

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("not 500: %d %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if body != `{"message":"服务器内部错误"}` {
		t.Fatalf("body wrong: %q", body)
	}
}

// TestValidateTokenDBFailureUntyped：ValidateToken 的 500 分支不携带 code。
func TestValidateTokenDBFailureUntyped(t *testing.T) {
	db := pgtestkit.New(t)
	deps := &Deps{DB: db, PGDialect: true, Now: time.Now}
	_, authErr := deps.ValidateToken(t.Context(), "some-token", scopeGroupListRead)
	if authErr == nil {
		t.Fatalf("expected auth error")
	}
	if authErr.StatusCode != http.StatusInternalServerError || authErr.Code != "" || authErr.Message != "服务器内部错误" {
		t.Fatalf("auth error wrong: %#v", authErr)
	}
}

// TestNowISOMillisecondPrecision：nowISO 与 Node nowIso() 同为毫秒精度。
func TestNowISOMillisecondPrecision(t *testing.T) {
	deps := &Deps{Now: func() time.Time {
		return time.Date(2026, 9, 4, 12, 30, 45, 123_456_789, time.UTC)
	}}
	if got := deps.nowISO(); got != "2026-09-04T12:30:45.123Z" {
		t.Fatalf("nowISO wrong: %q", got)
	}
}
