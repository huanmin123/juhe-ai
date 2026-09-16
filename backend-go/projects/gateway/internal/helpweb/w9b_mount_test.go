package helpweb

// w9b Mount 注册与 redirectRole 分支补充。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

type w9bRecordingKernel struct {
	patterns []string
}

func (k *w9bRecordingKernel) Register(pattern string, handler http.Handler) {
	k.patterns = append(k.patterns, pattern)
}

func TestW9BMountContract(t *testing.T) {
	// DistPath 为空 → 不注册。
	deps := &Deps{}
	recorder := &w9bRecordingKernel{}
	deps.Mount(recorder)
	if len(recorder.patterns) != 0 {
		t.Fatalf("空 DistPath 不应注册：%v", recorder.patterns)
	}
	// DistPath/help 不是目录 → 不注册。
	empty := t.TempDir()
	deps = &Deps{DistPath: empty}
	recorder = &w9bRecordingKernel{}
	deps.Mount(recorder)
	if len(recorder.patterns) != 0 {
		t.Fatalf("缺失 help 目录不应注册：%v", recorder.patterns)
	}
	// 完整 dist → 注册两条 pattern。
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "help", "user"), 0o755); err != nil {
		t.Fatal(err)
	}
	deps = &Deps{DistPath: root}
	recorder = &w9bRecordingKernel{}
	deps.Mount(recorder)
	if len(recorder.patterns) != 2 {
		t.Fatalf("完整 dist 应注册两条：%v", recorder.patterns)
	}
}

func TestW9BRedirectRoleArms(t *testing.T) {
	deps := &Deps{}
	// 管理角色 → admin 面。
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/help/", nil)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{Role: "admin"}))
	deps.redirectRole(recorder, request)
	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/__aisys__/help/admin/" {
		t.Fatalf("admin 跳转=%d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	// 普通角色 → user 面。
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/__aisys__/help/", nil)
	request = request.WithContext(authsys.WithAuthContext(request.Context(), &authsys.AuthContext{Role: "user"}))
	deps.redirectRole(recorder, request)
	if recorder.Header().Get("Location") != "/__aisys__/help/user/" {
		t.Fatalf("user 跳转=%q", recorder.Header().Get("Location"))
	}
	// 无认证上下文 → user 面。
	recorder = httptest.NewRecorder()
	deps.redirectRole(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/help/", nil))
	if recorder.Header().Get("Location") != "/__aisys__/help/user/" {
		t.Fatalf("无认证跳转=%q", recorder.Header().Get("Location"))
	}
}

func TestW9BMountedKernelDispatch(t *testing.T) {
	root := t.TempDir()
	helpDir := filepath.Join(root, "help", "user")
	if err := os.MkdirAll(helpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helpDir, "guide.html"), []byte("<html>guide</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := &Deps{DistPath: root, DevAutoLogin: func(r *http.Request) *authsys.AuthContext {
		return &authsys.AuthContext{SystemAccountID: "sa-1", Role: "user"}
	}}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	deps.Mount(k)
	// 裸前缀 → 302 子树。
	recorder := httptest.NewRecorder()
	k.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/help", nil))
	if recorder.Code != http.StatusFound {
		t.Fatalf("裸前缀=%d", recorder.Code)
	}
	// 子树静态文件。
	recorder = httptest.NewRecorder()
	k.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/help/user/guide.html", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("静态文件=%d", recorder.Code)
	}
}
