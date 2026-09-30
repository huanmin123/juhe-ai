// BUG-0248 契约 3：accounts 包 6 处裸 println（routes.go writeError、
// m11_routes.go 余额刷新未接线与 writeM11ReadError、authorized.go 降级读、
// api_key_runtime_revalidate.go revalidate 未接线、test_dispatch_routes.go
// writeTestError）必须改走注入的 slog.Logger（对齐 apikeys 先例），不再绕过
// JSONL 日志管道。
// 验证方式：注入捕获型 JSON handler，断言日志落点与字段；nil 注入时静默不
// panic。m11_routes.go 余额刷新未接线与 api_key_runtime_revalidate.go
// revalidate 未接线分支依赖完整 handler 装配（DB + auth 上下文），该两分支以
// 同构代码 + 编译验证，未单测。
package accounts

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bug0248CapturingLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

func TestBug0248WriteErrorLogsThroughSlog(t *testing.T) {
	logger, buf := bug0248CapturingLogger()
	deps := &Deps{Log: logger}
	recorder := httptest.NewRecorder()
	deps.writeError(recorder, errors.New("boom-0248-write"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", recorder.Code)
	}
	if !strings.Contains(buf.String(), "accounts slice internal error") || !strings.Contains(buf.String(), "boom-0248-write") {
		t.Fatalf("writeError 必须经 slog 落原始错误，实际日志: %s", buf.String())
	}
	// nil 注入静默不 panic。
	silent := &Deps{}
	silent.writeError(httptest.NewRecorder(), errors.New("boom-silent"))
}

func TestBug0248WriteM11ReadErrorLogsThroughSlog(t *testing.T) {
	logger, buf := bug0248CapturingLogger()
	deps := &Deps{Log: logger}
	recorder := httptest.NewRecorder()
	deps.writeM11ReadError(recorder, errors.New("boom-0248-m11"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", recorder.Code)
	}
	if !strings.Contains(buf.String(), "accounts m11 slice internal error") || !strings.Contains(buf.String(), "boom-0248-m11") {
		t.Fatalf("writeM11ReadError 必须经 slog 落原始错误，实际日志: %s", buf.String())
	}
	silent := &Deps{}
	silent.writeM11ReadError(httptest.NewRecorder(), errors.New("boom-silent"))
}

func TestBug0248WriteTestErrorLogsThroughSlog(t *testing.T) {
	logger, buf := bug0248CapturingLogger()
	deps := &Deps{Log: logger}
	recorder := httptest.NewRecorder()
	deps.writeTestError(recorder, errors.New("boom-0248-test"))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", recorder.Code)
	}
	if !strings.Contains(buf.String(), "accounts test slice internal error") || !strings.Contains(buf.String(), "boom-0248-test") {
		t.Fatalf("writeTestError 必须经 slog 落原始错误，实际日志: %s", buf.String())
	}
	// nil 注入静默不 panic。
	silent := &Deps{}
	silent.writeTestError(httptest.NewRecorder(), errors.New("boom-silent"))
}

// bug0248FailingReader 模拟 authorized 投影读取失败。
type bug0248FailingReader struct{}

func (bug0248FailingReader) AuthorizedReadableAccountIDs(ctx context.Context, viewer string) (map[string]bool, error) {
	return nil, errors.New("boom-0248-authorized")
}

func TestBug0248AuthorizedReadErrorLogsThroughStoreLogger(t *testing.T) {
	logger, buf := bug0248CapturingLogger()
	store := &Store{}
	store.SetAuthorizedReader(bug0248FailingReader{})
	store.SetLogger(logger)
	ids := store.authorizedReadableIDs(context.Background(), AccessScope{ViewerID: "viewer-1"})
	if ids != nil {
		t.Fatalf("读取失败必须降级为 nil（owner 视图），实际: %v", ids)
	}
	if !strings.Contains(buf.String(), "accounts slice authorized read error") ||
		!strings.Contains(buf.String(), "boom-0248-authorized") ||
		!strings.Contains(buf.String(), "viewer-1") {
		t.Fatalf("authorized 降级必须经 Store 注入日志记录，实际日志: %s", buf.String())
	}
	// 未接线日志时保持静默（不 panic、不输出）。
	silentStore := &Store{}
	silentStore.SetAuthorizedReader(bug0248FailingReader{})
	if ids := silentStore.authorizedReadableIDs(context.Background(), AccessScope{ViewerID: "viewer-2"}); ids != nil {
		t.Fatalf("静默降级仍应返回 nil，实际: %v", ids)
	}
}
