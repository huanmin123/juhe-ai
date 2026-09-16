package uibootstrap

// w9b Mount 注册：真实 authsys 链上的 admin/self 两个 bootstrap 面。

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	businessauth "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

var mustChangeFalseW9B = false

func TestW9BMountRegistersBothSurfaces(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// authsys 依赖的账户表来自 bootstrapSchema（同库）。
	if _, err := db.Exec(bootstrapSchema); err != nil {
		t.Fatal(err)
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)
	if err != nil {
		t.Fatal(err)
	}
	authDeps := &authsys.Deps{
		Port: service, Accounts: accounts, Captcha: modelcheckauth.NewCaptchaService(nil),
		LoginGuard: modelcheckauth.NewLoginGuard(nil), CaptchaDisabled: true,
	}
	deps := &Deps{DB: db, PGDialect: false, Auth: authDeps}
	k := kernel.New(kernel.Options{CompressionDisabled: true})
	authDeps.MountAuth(k, "lax", false)
	deps.Mount(k)
	// 未登录访问 admin 面 → 401/403 而不是 404（路由已注册）。
	recorder := httptest.NewRecorder()
	k.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/ui-bootstrap/options", nil))
	if recorder.Code == http.StatusNotFound {
		t.Fatalf("admin 面未注册：%d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	k.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/__aisys__/api/my-ui-bootstrap/options", nil))
	if recorder.Code == http.StatusNotFound {
		t.Fatalf("self 面未注册：%d", recorder.Code)
	}
	_ = mustChangeFalseW9B
}
