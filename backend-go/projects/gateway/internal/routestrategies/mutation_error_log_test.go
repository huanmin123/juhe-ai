// 策略路由写侧兜底错误可观测性（真号验收遗留：策略创建 400 的原始错误被
// localize 中间件改写为「请求参数无效」，服务端必须留有原始文本）。直接锁定
// writeMutationError 兜底分支：Log 非 nil 时以 WARN 落原始错误；nil 时静默。
package routestrategies

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteMutationErrorFallbackLogsOriginalMessage(t *testing.T) {
	var buf bytes.Buffer
	deps := &Deps{Log: slog.New(slog.NewTextHandler(&buf, nil))}
	recorder := httptest.NewRecorder()

	deps.writeMutationError(recorder, errors.New("SQLITE_BUSY: database is locked"))

	if recorder.Code != 400 {
		t.Fatalf("兜底分支 status = %d, want 400", recorder.Code)
	}
	// 契约：WriteBadRequest 内联 localize，非中文 message 响应侧即为
	// 「请求参数无效」——原始文本只能靠 Log 留存（本测试的断言核心）。
	if !strings.Contains(recorder.Body.String(), "请求参数无效") {
		t.Fatalf("兜底分支响应应遵循 localize 契约，got %q", recorder.Body.String())
	}
	if !strings.Contains(buf.String(), "route_strategy_mutation_rejected") || !strings.Contains(buf.String(), "SQLITE_BUSY: database is locked") {
		t.Fatalf("Log 非 nil 时应落 WARN 保留原始错误，got %q", buf.String())
	}
}

func TestWriteMutationErrorNilLogStaysSilent(t *testing.T) {
	deps := &Deps{}
	recorder := httptest.NewRecorder()

	deps.writeMutationError(recorder, errors.New("boom"))

	if recorder.Code != 400 {
		t.Fatalf("nil Log 兜底分支 status = %d, want 400", recorder.Code)
	}
}
