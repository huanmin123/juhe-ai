package authsys

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

func TestWlStoreErrorTypesAndCreateValidation(t *testing.T) {
	t.Run("错误类型消息", func(t *testing.T) {
		if (&ConflictError{Message: "c"}).Error() != "c" {
			t.Fatal("ConflictError.Error 必须透传消息")
		}
		if (&ValidationError{Message: "v"}).Error() != "v" {
			t.Fatal("ValidationError.Error 必须透传消息")
		}
		if (&BadRequestError{Message: "b"}).Error() != "b" {
			t.Fatal("BadRequestError.Error 必须透传消息")
		}
	})
	t.Run("noop sink 经接口调用", func(t *testing.T) {
		var sink OperationLogSink = noopSink{}
		sink.Record(OperationLogEntry{Module: "m"}, httptest.NewRequest(http.MethodGet, "/", nil))
	})
	db := newContractTestDB(t)
	store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tests := []struct {
		name  string
		input CreateInput
		want  string
	}{
		{"空用户名", CreateInput{DisplayName: "d", Password: "pass-123"}, "用户账户不能为空"},
		{"空显示名", CreateInput{Username: "u", Password: "pass-123"}, "用户名称不能为空"},
		{"用户名空格", CreateInput{Username: "a b", DisplayName: "d", Password: "pass-123"}, "用户账户不能包含空格"},
		{"显示名空格", CreateInput{Username: "ab", DisplayName: "a b", Password: "pass-123"}, "用户名称不能包含空格"},
		{"短密码", CreateInput{Username: "ab", DisplayName: "d", Password: "abc"}, "登录密码不能少于 4 个字符"},
		{"密码空格", CreateInput{Username: "ab", DisplayName: "d", Password: "a b c"}, "登录密码不能包含空格"},
		{"角色无效", CreateInput{Username: "ab", DisplayName: "d", Password: "pass-123", Role: "root"}, "系统账户角色无效"},
		{"状态无效", CreateInput{Username: "ab", DisplayName: "d", Password: "pass-123", Status: "paused"}, "系统账户状态无效"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.Create(ctx, test.input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v want=%q", err, test.want)
			}
		})
	}
	t.Run("AI 账户上限越界", func(t *testing.T) {
		limit := 1_000_001
		_, err := store.Create(ctx, CreateInput{Username: "ab", DisplayName: "d", Password: "pass-123", AIAccountLimit: &limit})
		if err == nil || !strings.Contains(err.Error(), "0 到 1000000") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("重名冲突", func(t *testing.T) {
		if _, err := store.Create(ctx, CreateInput{Username: "wlconflict", DisplayName: "冲突名", Password: "pass-123"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Create(ctx, CreateInput{Username: "wlconflict", DisplayName: "另一个名字", Password: "pass-123"}); err == nil || !strings.Contains(err.Error(), "用户账户已存在") {
			t.Fatalf("err=%v", err)
		}
		if _, err := store.Create(ctx, CreateInput{Username: "另一个用户", DisplayName: "冲突名", Password: "pass-123"}); err == nil || !strings.Contains(err.Error(), "用户名称已存在") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestWlVerifyNodePasswordMalformedHash(t *testing.T) {
	if _, err := verifyNodePassword("pw", "not-a-node-hash"); err == nil || !strings.Contains(err.Error(), "format is invalid") {
		t.Fatalf("err=%v", err)
	}
	// 分段匹配但 digest 不是合法 base64。
	if _, err := verifyNodePassword("pw", "scrypt$!!!bad-base64!!!"); err == nil {
		t.Fatal("非法 digest 必须报错")
	}
}

func TestWlEnsureDefaultResourcesIdempotentInSameTx(t *testing.T) {
	ctx := context.Background()
	db := newContractTestDB(t)
	store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ensurer := NewSQLDefaultResources(store, wlStringSealer{})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	nowText := time.Now().UTC().Format(time.RFC3339Nano)
	if err := ensurer.EnsureDefaultResources(ctx, tx, "wl-idem", nowText); err != nil {
		t.Fatalf("第一次执行失败: %v", err)
	}
	// 同一事务内重复执行必须全部命中"已存在"分支且不产生新行。
	if err := ensurer.EnsureDefaultResources(ctx, tx, "wl-idem", nowText); err != nil {
		t.Fatalf("第二次执行失败: %v", err)
	}
	for _, table := range []string{"groups", "route_strategies", "route_strategy_groups"} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE system_account_id='wl-idem'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatalf("%s 不应有零行", table)
		}
	}
}

func TestWlNextDefaultResourceNameSkipsTaken(t *testing.T) {
	ctx := context.Background()
	db := newContractTestDB(t)
	store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ensurer := NewSQLDefaultResources(store, wlStringSealer{})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	name, err := ensurer.nextDefaultResourceName(ctx, tx, "groups", "wl-name", "基名")
	if err != nil || name != "基名" {
		t.Fatalf("name=%q err=%v", name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO groups (id, system_account_id, name, provider_code, created_at, updated_at) VALUES ('g-n1','wl-name','基名','openai','t','t')`); err != nil {
		t.Fatal(err)
	}
	name, err = ensurer.nextDefaultResourceName(ctx, tx, "groups", "wl-name", "基名")
	if err != nil || name != "基名 2" {
		t.Fatalf("name=%q err=%v", name, err)
	}
}

func TestWlGetProfileSettingsFailure(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	account := seedAccount(t, deps, "wlprofilefail", "pass-1234", "admin")
	deps.Settings = &wlFakeSettings{err: errors.New("settings down")}
	request := withWlAuth(httptest.NewRequest(http.MethodGet, "/profile", nil), account.ID, "wlprofilefail", "admin", false)
	recorder := httptest.NewRecorder()
	deps.getProfile(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWlDBFailurePathsReturn500(t *testing.T) {
	// 关闭数据库后触发查询失败分支：路由层必须映射为 500 而不是 panic。
	db := newContractTestDB(t)
	store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	deps := &Deps{Accounts: store, Settings: &wlFakeSettings{values: wlLimitSettings()}, Now: time.Now}
	auth := func(request *http.Request) *http.Request {
		return request.WithContext(WithAuthContext(request.Context(), &AuthContext{SystemAccountID: "acc", Username: "u", Role: "admin"}))
	}
	t.Run("listAccounts", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		deps.listAccounts(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", recorder.Code)
		}
	})
	t.Run("listAccountOptions", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		deps.listAccountOptions(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", recorder.Code)
		}
	})
	t.Run("getProfile", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		deps.getProfile(recorder, auth(httptest.NewRequest(http.MethodGet, "/profile", nil)))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", recorder.Code)
		}
	})
	t.Run("patchMe 查询失败", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := auth(wlJSONRequest(http.MethodPatch, "/me", `{"displayName":"x"}`))
		deps.patchMe(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", recorder.Code)
		}
	})
}

func TestWlSmallHelpersExtraBranches(t *testing.T) {
	t.Run("normalizeRFC3339", func(t *testing.T) {
		normalized, err := normalizeRFC3339("2026-01-02T03:04:05.999+08:00")
		if err != nil || !strings.HasSuffix(normalized, "Z") {
			t.Fatalf("normalized=%q err=%v", normalized, err)
		}
		if _, err := normalizeRFC3339("bad-time"); err == nil || !strings.Contains(err.Error(), "格式不正确") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("normalizeRFC3339Text", func(t *testing.T) {
		if got := normalizeRFC3339Text("not-rfc3339"); got != "not-rfc3339" {
			t.Fatalf("got=%q", got)
		}
	})
	t.Run("mustMarshalJSON", func(t *testing.T) {
		if string(mustMarshalJSON(map[string]any{"a": float64(1)})) != `{"a":1}` {
			t.Fatalf("got=%s", mustMarshalJSON(map[string]any{"a": float64(1)}))
		}
	})
	t.Run("NewAccountStore 校验", func(t *testing.T) {
		if _, err := NewAccountStore(nil, modelcheckauth.SQLite, nil); err == nil {
			t.Fatal("nil db 必须拒绝")
		}
		if _, err := NewAccountStore(newContractTestDB(t), modelcheckauth.Mode(99), nil); err == nil {
			t.Fatal("未知模式必须拒绝")
		}
	})
	t.Run("Postgres 模式锁在 SQLite 上报错", func(t *testing.T) {
		db := newContractTestDB(t)
		pgStore, err := NewAccountStore(db, modelcheckauth.Postgres, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		// PG 方言锁语句在 SQLite 无法执行：必须返回错误而不是 panic。
		if err := pgStore.lockSuperAdminInvariant(context.Background(), tx); err == nil {
			t.Fatal("PG 锁语句在 SQLite 必须报错")
		}
	})
	t.Run("EnsureDefaultResources 缺 store", func(t *testing.T) {
		ensurer := NewSQLDefaultResources(nil, wlStringSealer{})
		tx, err := newContractTestDB(t).BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := ensurer.EnsureDefaultResources(context.Background(), tx, "acc", "now"); err == nil {
			t.Fatal("缺 store 必须拒绝")
		}
	})
	t.Run("Redis 构造器错误分支", func(t *testing.T) {
		if _, _, err := NewRedisCaptchaService("://bad", "ns", nil); err == nil {
			t.Fatal("非法 URL 必须拒绝")
		}
		if _, _, err := NewRedisLoginGuard("://bad", "ns", nil); err == nil {
			t.Fatal("非法 URL 必须拒绝")
		}
	})
	t.Run("loginClientIP 回退", func(t *testing.T) {
		if got := loginClientIP(httptest.NewRequest(http.MethodGet, "/", nil)); got != "unknown" {
			t.Fatalf("got=%q", got)
		}
	})
	t.Run("mustMarshalJSON 非法值回退 null", func(t *testing.T) {
		if got := mustMarshalJSON(math.NaN()); string(got) != "null" {
			t.Fatalf("got=%s", got)
		}
	})
}

func TestWlPatchAccountRouteValidation(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "wlpatchadmin", "pass-1234", "super_admin")
	seedAccount(t, deps, "wlpatchuser", "pass-1234", "user")
	cookie := login(t, server, "wlpatchadmin", "pass-1234")
	patch := func(id, body string) int {
		request, err := http.NewRequest(http.MethodPatch, server.URL+"/__aisys__/api/system-accounts/"+id, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Cookie", cookie)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return response.StatusCode
	}
	t.Run("username 不可修改", func(t *testing.T) {
		if got := patch("whatever", `{"expectedUpdatedAt":"2026-01-01T00:00:00Z","username":"new"}`); got != http.StatusBadRequest {
			t.Fatalf("status=%d", got)
		}
	})
	t.Run("账户不存在返回 404", func(t *testing.T) {
		if got := patch("sysacc_missing", `{"expectedUpdatedAt":"2026-01-01T00:00:00Z","displayName":"x"}`); got != http.StatusNotFound {
			t.Fatalf("status=%d", got)
		}
	})
}

func TestWlRoutesRemainingErrorBranches(t *testing.T) {
	deps, _, server := newTestEnv(t)
	originalPort := deps.Port
	seedAccount(t, deps, "wlbranch", "admin-pass-1", "admin")
	seedAccount(t, deps, "wlbranchuser", "user-pass-1", "user")
	issue := func(username, password string) (string, int) {
		response, payload := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens",
			`{"username":"`+username+`","password":"`+password+`"}`, "")
		if response.StatusCode != http.StatusOK {
			return "", response.StatusCode
		}
		token, _ := payload["data"].(map[string]any)["token"].(string)
		return token, response.StatusCode
	}
	t.Run("临时令牌 password 非字符串", func(t *testing.T) {
		response, _ := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens", `{"password":true}`, "")
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d", response.StatusCode)
		}
	})
	t.Run("临时令牌创建失败返回 500", func(t *testing.T) {
		deps.Port = temporaryTokenCreationFailingPort{Port: originalPort, err: errors.New("boom")}
		response, _ := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens", `{"username":"wlbranch","password":"admin-pass-1"}`, "")
		if response.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.Port = originalPort
	})
	t.Run("修改密码时凭据校验失败返回 500", func(t *testing.T) {
		deps.Port = verifyCredentialsFailingPort{Port: originalPort, err: errors.New("boom")}
		request := wlJSONRequest(http.MethodPost, "/change-password", `{"oldPassword":"x","newPassword":"new-pass-1"}`)
		request = request.WithContext(WithAuthContext(request.Context(), &AuthContext{SystemAccountID: "acc", Username: "wlbranch", Role: "admin", SessionID: "s"}))
		recorder := httptest.NewRecorder()
		deps.postChangePassword(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", recorder.Code)
		}
		deps.Port = originalPort
	})
	t.Run("临时令牌撤销缺少会话上下文返回 401", func(t *testing.T) {
		token, _ := issue("wlbranch", "admin-pass-1")
		request := httptest.NewRequest(http.MethodPost, "/revoke", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		deps.postTemporaryAccessTokenRevoke(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", recorder.Code)
		}
	})
	t.Run("会话创建被拒且登录守卫已锁返回 429", func(t *testing.T) {
		store := newWlFakeStateStore()
		store.preset("login:ip:127.0.0.1:lock", time.Now().Add(time.Minute).UnixMilli())
		deps.LoginGuard = NewSharedLoginGuard(store, deps.Now)
		deps.Port = sessionCreationRejectedPort{Port: originalPort}
		response, _ := postJSON(t, server, "/__aisys__/api/auth/login", `{"username":"wlbranch","password":"admin-pass-1"}`, "")
		if response.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.Port = originalPort
		deps.LoginGuard = modelcheckauth.NewLoginGuard(deps.Now)
	})
}

func mustFindID(t *testing.T, deps *Deps, username string) string {
	t.Helper()
	summary, err := deps.Accounts.FindByUsername(context.Background(), username)
	if err != nil || summary.ID == "" {
		t.Fatalf("账户缺失: %v", err)
	}
	return summary.ID
}

// ---- 补充端口桩：精确命中剩余错误分支 ----

type wlSessionCreateFailingPort struct {
	businessauth.Port
	err error
}

func (p wlSessionCreateFailingPort) CreateSession(context.Context, string, string, int) (modelcheckauth.IssuedSession, bool, error) {
	return modelcheckauth.IssuedSession{}, false, p.err
}

type wlSessionCreateUnavailablePort struct {
	businessauth.Port
}

func (wlSessionCreateUnavailablePort) CreateSession(context.Context, string, string, int) (modelcheckauth.IssuedSession, bool, error) {
	return modelcheckauth.IssuedSession{}, false, nil
}

type wlRevokeOtherFailingPort struct {
	businessauth.Port
}

func (wlRevokeOtherFailingPort) RevokeOtherSessions(context.Context, string, string) error {
	return errors.New("revoke-others boom")
}

type wlTempTokenUnavailablePort struct {
	businessauth.Port
}

func (wlTempTokenUnavailablePort) CreateTemporaryToken(context.Context, string, string, int) (modelcheckauth.IssuedSession, bool, error) {
	return modelcheckauth.IssuedSession{}, false, nil
}

type wlMustChangeVerifyPort struct {
	businessauth.Port
	accountID string
}

func (p wlMustChangeVerifyPort) VerifyCredentials(context.Context, string, string) (modelcheckauth.VerifiedCredentials, bool, error) {
	return modelcheckauth.VerifiedCredentials{SystemAccountID: p.accountID, Username: "wlbranch", Role: "admin", MustChangePassword: true}, true, nil
}

func TestWlRemainingRoute500And401Branches(t *testing.T) {
	deps, _, server := newTestEnv(t)
	originalPort := deps.Port
	seedAccount(t, deps, "wlbranch", "admin-pass-1", "admin")
	accountID := mustFindID(t, deps, "wlbranch")
	loginAndExpect := func(body string, want int) {
		t.Helper()
		response, _ := postJSON(t, server, "/__aisys__/api/auth/login", body, "")
		if response.StatusCode != want {
			t.Fatalf("status=%d want=%d", response.StatusCode, want)
		}
	}
	t.Run("CreateSession 失败返回 500", func(t *testing.T) {
		deps.Port = wlSessionCreateFailingPort{Port: originalPort, err: errors.New("boom")}
		loginAndExpect(`{"username":"wlbranch","password":"admin-pass-1"}`, http.StatusInternalServerError)
		deps.Port = originalPort
	})
	t.Run("会话创建拒绝且失败计数达阈值返回 429", func(t *testing.T) {
		store := newWlFakeStateStore()
		store.preset("login:ip:127.0.0.1:count", int64(sharedLoginLimit-1))
		deps.LoginGuard = NewSharedLoginGuard(store, deps.Now)
		deps.Port = wlSessionCreateUnavailablePort{Port: originalPort}
		loginAndExpect(`{"username":"wlbranch","password":"admin-pass-1"}`, http.StatusTooManyRequests)
		deps.Port = originalPort
		deps.LoginGuard = modelcheckauth.NewLoginGuard(deps.Now)
	})
	t.Run("撤销其他会话失败返回 500", func(t *testing.T) {
		deps.Port = wlRevokeOtherFailingPort{Port: originalPort}
		request := wlJSONRequest(http.MethodPost, "/change-password", `{"oldPassword":"admin-pass-1","newPassword":"new-pass-1"}`)
		request = request.WithContext(WithAuthContext(request.Context(), &AuthContext{SystemAccountID: accountID, Username: "wlbranch", Role: "admin", SessionID: "s"}))
		recorder := httptest.NewRecorder()
		deps.postChangePassword(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", recorder.Code)
		}
		deps.Port = originalPort
	})
	t.Run("临时令牌不可用返回 401", func(t *testing.T) {
		deps.Port = wlTempTokenUnavailablePort{Port: originalPort}
		response, _ := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens", `{"username":"wlbranch","password":"admin-pass-1"}`, "")
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.Port = originalPort
	})
	t.Run("强制改密管理员申请临时令牌返回 403", func(t *testing.T) {
		deps.Port = wlMustChangeVerifyPort{Port: originalPort, accountID: accountID}
		response, _ := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens", `{"username":"wlbranch","password":"whatever"}`, "")
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.Port = originalPort
	})
	t.Run("Redis namespace 为空时构造失败", func(t *testing.T) {
		server := miniredisRun(t)
		if _, _, err := NewRedisNamespacedStateStore("redis://"+server.Addr(), "  ", "name"); err == nil {
			t.Fatal("空 namespace 必须拒绝")
		}
	})
	t.Run("数据库关闭后 patchMe 返回 500", func(t *testing.T) {
		db := newContractTestDB(t)
		store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		closedDeps := &Deps{Accounts: store, Settings: &wlFakeSettings{values: wlLimitSettings()}, Now: time.Now}
		request := wlJSONRequest(http.MethodPatch, "/me", `{"displayName":"x"}`)
		request = request.WithContext(WithAuthContext(request.Context(), &AuthContext{SystemAccountID: "acc", Username: "u", Role: "admin"}))
		recorder := httptest.NewRecorder()
		closedDeps.patchMe(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d", recorder.Code)
		}
	})
}

// miniredisRun 启动一个测试专用 miniredis 实例。
func miniredisRun(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	return miniredis.RunT(t)
}
