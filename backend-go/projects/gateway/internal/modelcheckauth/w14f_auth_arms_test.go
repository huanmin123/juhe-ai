package modelcheckauth

// w14f_auth_arms_test.go 用脚本化 database/sql 驱动补齐 modelcheckauth 的
// 剩余分支：CheckContract 的 PostgreSQL 特权臂、会话签发/改密/清理的行级
// 故障注入、HTTP 处理器的 429/404/409/500 臂、验证码限流与淘汰分支、登录
// 守卫按用户名锁定臂。
//
// 不可达语句登记（进程内确实无法触发的 crypto/rand 失败臂，保持原文不改）：
//   - captcha.go Issue 内 randomCaptchaAnswer / randomCaptchaID 的错误臂及其
//     500 输出（crypto/rand 不失败）；
//   - captcha.go randomCaptchaAnswer 中 rand.Read 的错误臂；
//   - sessions.go createSession 内 randomToken / randomID 的错误臂。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 脚本化驱动 ----

type w14fAuthState struct {
	mu        sync.Mutex
	queryFn   func(query string, args []driver.NamedValue) (driver.Rows, error)
	execFn    func(query string, args []driver.NamedValue) (driver.Result, error)
	beginErr  error
	commitErr error
}

type w14fAuthConnector struct{ state *w14fAuthState }

func (c *w14fAuthConnector) Connect(context.Context) (driver.Conn, error) {
	return &w14fAuthConn{state: c.state}, nil
}
func (c *w14fAuthConnector) Driver() driver.Driver { return w14fAuthDriver{} }

type w14fAuthDriver struct{}

func (w14fAuthDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("w14f fake driver requires a connector")
}

type w14fAuthConn struct{ state *w14fAuthState }

func (c *w14fAuthConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w14f fake driver handles queries directly")
}
func (c *w14fAuthConn) Close() error { return nil }
func (c *w14fAuthConn) Begin() (driver.Tx, error) {
	return c.state.begin()
}
func (c *w14fAuthConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.state.begin()
}

func (s *w14fAuthState) begin() (driver.Tx, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.beginErr != nil {
		return nil, s.beginErr
	}
	return w14fAuthTx{state: s}, nil
}

func (c *w14fAuthConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	execFn := c.state.execFn
	c.state.mu.Unlock()
	if execFn != nil {
		return execFn(query, args)
	}
	return w14fAuthResult{rowsAffected: 1}, nil
}

func (c *w14fAuthConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	queryFn := c.state.queryFn
	c.state.mu.Unlock()
	if queryFn != nil {
		return queryFn(query, args)
	}
	return &w14fAuthRows{}, nil
}

type w14fAuthTx struct{ state *w14fAuthState }

func (t w14fAuthTx) Commit() error {
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	return t.state.commitErr
}
func (t w14fAuthTx) Rollback() error { return nil }

type w14fAuthResult struct {
	rowsAffected int64
	raErr        error
}

func (r w14fAuthResult) LastInsertId() (int64, error) { return 0, errors.New("w14f: unsupported") }
func (r w14fAuthResult) RowsAffected() (int64, error) { return r.rowsAffected, r.raErr }

type w14fAuthRows struct {
	columns []string
	values  [][]driver.Value
	nextErr error
	i       int
}

func (r *w14fAuthRows) Columns() []string { return r.columns }
func (r *w14fAuthRows) Close() error      { return nil }
func (r *w14fAuthRows) Next(dest []driver.Value) error {
	if r.i < len(r.values) {
		copy(dest, r.values[r.i])
		r.i++
		return nil
	}
	if r.nextErr != nil {
		return r.nextErr
	}
	return io.EOF
}

func w14fAuthOpenDB(t *testing.T, state *w14fAuthState) *sql.DB {
	t.Helper()
	db := sql.OpenDB(&w14fAuthConnector{state: state})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// w14fAuthScripted 以脚本化驱动组装 Authenticator（mode 切换方言臂）。
func w14fAuthScripted(t *testing.T, mode Mode, state *w14fAuthState) *Authenticator {
	t.Helper()
	auth, err := New(w14fAuthOpenDB(t, state), mode, func() time.Time { return time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func w14fErrRows(err error) driver.Rows { return &w14fAuthRows{nextErr: err} }

func w14fResult(rows int64) driver.Result { return w14fAuthResult{rowsAffected: rows} }

func w14fStaticRows(columns int, values ...driver.Value) driver.Rows {
	return &w14fAuthRows{columns: make([]string, columns), values: [][]driver.Value{values}}
}

// w14fSessionValues 返回 authenticateToken 会话连接查询的 8 列成功行
// （expires 在注入 now 之后、last_seen 在 10 分钟前以触发 touch）。
func w14fSessionValues(role string) []driver.Value {
	return []driver.Value{
		"w14f-session", "2026-09-16T11:00:00.000Z", "2026-09-16T09:50:00.000Z",
		"w14f-acct", "w14f-admin", "管理员", role, 0,
	}
}

// ---- CheckContract PostgreSQL 特权臂 ----

func TestW14fCheckContractPostgresPrivilegeArms(t *testing.T) {
	// 特权查询返回 true → 契约通过。
	permitted := true
	auth := w14fAuthScripted(t, Postgres, &w14fAuthState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "has_table_privilege") {
			return w14fStaticRows(1, permitted), nil
		}
		return &w14fAuthRows{}, nil
	}})
	if err := auth.CheckContract(context.Background()); err != nil {
		t.Fatalf("特权查询通过后契约不应报错: %v", err)
	}

	// 特权查询 Scan 失败。
	auth = w14fAuthScripted(t, Postgres, &w14fAuthState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "has_table_privilege") {
			return nil, errors.New("w14f privilege read failure")
		}
		return &w14fAuthRows{}, nil
	}})
	if err := auth.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "read management session update privilege") {
		t.Fatalf("特权读取失败应报错: %v", err)
	}

	// 特权为 false。
	auth = w14fAuthScripted(t, Postgres, &w14fAuthState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "has_table_privilege") {
			return w14fStaticRows(1, false), nil
		}
		return &w14fAuthRows{}, nil
	}})
	if err := auth.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "lacks system session touch privilege") {
		t.Fatalf("缺少 UPDATE 特权应报错: %v", err)
	}

	// 提交失败（同臂覆盖 SQLite 事务提交）。
	auth = w14fAuthScripted(t, SQLite, &w14fAuthState{commitErr: errors.New("w14f commit failure")})
	if err := auth.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "commit management auth contract transaction") {
		t.Fatalf("契约提交失败应报错: %v", err)
	}
}

// ---- authenticateToken touch 失败与 RequireAdmin ----

func TestW14fTouchUpdateFailureAndRequireAdminForbidden(t *testing.T) {
	// 会话有效、last_seen 超过 1 分钟 → touch UPDATE 失败。
	auth := w14fAuthScripted(t, SQLite, &w14fAuthState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "SELECT ss.id,ss.expires_at") {
			return w14fStaticRows(8, w14fSessionValues("admin")...), nil
		}
		_ = args
		return &w14fAuthRows{}, nil
	}, execFn: func(query string, args []driver.NamedValue) (driver.Result, error) {
		if strings.Contains(query, "SET last_seen_at=") {
			return nil, errors.New("w14f touch failure")
		}
		_ = args
		return w14fResult(1), nil
	}})
	if _, err := auth.AuthenticateTokenForSession(context.Background(), w14fTokenValue(t)); err == nil ||
		!strings.Contains(err.Error(), "touch management session") {
		t.Fatalf("touch 失败应报错: %v", err)
	}

	// RequireAdmin：非管理员角色 → ErrForbidden。
	auth = w14fAuthScripted(t, SQLite, &w14fAuthState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "SELECT ss.id,ss.expires_at") {
			return w14fStaticRows(8, w14fSessionValues("viewer")...), nil
		}
		_ = args
		return &w14fAuthRows{}, nil
	}})
	if _, err := auth.RequireAdmin(context.Background(), "Bearer "+w14fTokenValue(t), ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer 角色应被拒绝: %v", err)
	}
}

// w14fTokenValue 返回满足临时令牌形状的固定令牌。
func w14fTokenValue(t *testing.T) string {
	t.Helper()
	token := "juhe_tmp_" + strings.Repeat("a", 43)
	if !temporaryToken.MatchString(token) {
		t.Fatalf("w14f token shape invalid")
	}
	return token
}

func TestW14fParseTimeBytesArm(t *testing.T) {
	parsed, err := parseTime([]byte("2026-09-16T10:00:00Z"))
	if err != nil || parsed.IsZero() {
		t.Fatalf("[]byte 时间解析: %v %v", parsed, err)
	}
}

// ---- 验证码分支 ----

func w14fCaptchaNow(t *testing.T) (*CaptchaService, *time.Time) {
	t.Helper()
	current := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	return NewCaptchaService(func() time.Time { return current }), &current
}

func TestW14fCaptchaBlockedAndHandlerArms(t *testing.T) {
	service, current := w14fCaptchaNow(t)
	ip := "192.0.2.10"
	// 预置 60 次窗口内签发 → Issue 直接 blocked。
	stamps := make([]time.Time, 0, captchaIssueLimit)
	for i := 0; i < captchaIssueLimit; i++ {
		stamps = append(stamps, current.Add(-time.Duration(i)*time.Second))
	}
	service.issues[ip] = stamps

	handler := &HTTPHandler{Captcha: service}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/captcha", nil)
	request.RemoteAddr = ip + ":5555"
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked captcha=%d %s", recorder.Code, recorder.Body.String())
	}
	if retry := recorder.Header().Get("Retry-After"); retry == "" {
		t.Fatalf("Retry-After 缺失: %s", recorder.Body.String())
	}
	// RetryAfter 计算为正数（窗口 60s）。
	if result, err := service.Issue(ip); err != nil || !result.Blocked || result.RetryAfter <= 0 {
		t.Fatalf("issue blocked: %+v %v", result, err)
	}
	_ = current
}

func TestW14fCaptchaIssueEvictionArms(t *testing.T) {
	service, current := w14fCaptchaNow(t)

	// 过期挑战清理臂：Issue 时先删除过期记录。
	service.chall["w14f-stale"] = captchaRecord{answer: "AAAAA", expiresAt: current.Add(-time.Second)}
	result, err := service.Issue("192.0.2.20")
	if err != nil || result.Blocked {
		t.Fatalf("issue after stale cleanup: %+v %v", result, err)
	}
	if _, ok := service.chall["w14f-stale"]; ok {
		t.Fatalf("过期挑战应被清理")
	}

	// issues 键上限：预置 captchaMaxIssueKeys 个近期键（当前 ip 不在册）→
	// 强制淘汰循环执行后新键落位。
	fresh := "192.0.2.21"
	service.issues = map[string][]time.Time{}
	for i := 0; i < captchaMaxIssueKeys; i++ {
		service.issues["w14f-ip-"+strconv.Itoa(i)] = []time.Time{*current}
	}
	if _, err := service.Issue(fresh); err != nil {
		t.Fatalf("issue under key eviction: %v", err)
	}
	if _, ok := service.issues[fresh]; !ok {
		t.Fatalf("新键应已落位")
	}

	// 全部过期键：惰性淘汰循环执行。
	service.issues = map[string][]time.Time{}
	for i := 0; i < captchaMaxIssueKeys; i++ {
		service.issues["w14f-old-"+strconv.Itoa(i)] = []time.Time{current.Add(-captchaIssueWindow - time.Minute)}
	}
	if _, err := service.Issue(fresh); err != nil {
		t.Fatalf("issue under stale key eviction: %v", err)
	}
}

func TestW14fCaptchaVerifyAndAnswerMissArms(t *testing.T) {
	service, _ := w14fCaptchaNow(t)
	if service.Verify("w14f-missing", "AAAAA") {
		t.Fatalf("未知验证码 ID 必须失败")
	}
	if service.AnswerForTest("w14f-missing") != "" {
		t.Fatalf("未知验证码 ID 的答案必须为空")
	}
}

// ---- 登录守卫按用户名锁定臂 ----

func TestW14fLoginGuardUserLockArm(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	guard := NewLoginGuard(func() time.Time { return now })
	// 前 9 次不同来源 IP 对同一用户名失败：按用户名未达上限、按 IP 不锁定。
	for i := 0; i < loginGuardLimit-1; i++ {
		blocked, _, _ := guard.Failed("192.0.2."+strconv.Itoa(100+i), "w14f-user")
		if blocked {
			t.Fatalf("第 %d 次失败不应锁定", i+1)
		}
	}
	// 第 10 次（最后一次）触达上限 → 按用户名锁定成立。
	if blocked, _, _ := guard.Failed("192.0.2.109", "w14f-user"); !blocked {
		t.Fatalf("第 10 次失败应触发锁定")
	}
	blocked, retry, message := guard.Check("192.0.2.200", "w14f-user")
	if !blocked || retry <= 0 || message != "账号暂时锁定，请稍后再试" {
		t.Fatalf("user lock: %v %d %q", blocked, retry, message)
	}
	// 未触发用户 → 通过。
	if blocked, _, _ := guard.Check("192.0.2.200", "w14f-other"); blocked {
		t.Fatalf("其他用户不应被锁定")
	}
}

// ---- 会话签发 / 改密 / 清理错误臂（脚本化驱动） ----

func TestW14fSessionIssueArms(t *testing.T) {
	const passwordHash = "pbkdf2$sha512$120000$MDEyMzQ1Njc4OWFiY2RlZg$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	revision := hashString(passwordHash)

	build := func(t *testing.T, mutate func(*w14fAuthState)) *Authenticator {
		t.Helper()
		state := &w14fAuthState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(query, "SELECT status,password_hash FROM"):
				return w14fStaticRows(2, "active", passwordHash), nil
			}
			_ = args
			return &w14fAuthRows{}, nil
		}}
		mutate(state)
		return w14fAuthScripted(t, Postgres, state)
	}

	// PG FOR UPDATE 锁子句 + 成功签发（覆盖 Postgres 分支渲染）。
	auth := build(t, func(*w14fAuthState) {})
	issued, ok, err := auth.CreateTemporaryAccessToken(context.Background(), "w14f-acct", revision, 900)
	if err != nil || !ok || !strings.HasPrefix(issued.Token, "juhe_tmp_") {
		t.Fatalf("pg issue: %+v %v %v", issued, ok, err)
	}

	// 账户行读取失败。
	auth = build(t, func(state *w14fAuthState) {
		state.queryFn = func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "SELECT status,password_hash FROM") {
				return nil, errors.New("w14f account read failure")
			}
			_ = args
			return &w14fAuthRows{}, nil
		}
	})
	if _, _, err := auth.CreateTemporaryAccessToken(context.Background(), "w14f-acct", revision, 900); err == nil ||
		!strings.Contains(err.Error(), "read session account") {
		t.Fatalf("账户读取失败应报错: %v", err)
	}

	// last_login_at 更新失败。
	auth = build(t, func(state *w14fAuthState) {
		state.execFn = func(query string, args []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(query, "SET last_login_at=") {
				return nil, errors.New("w14f last login failure")
			}
			_ = args
			return w14fResult(1), nil
		}
	})
	if _, _, err := auth.CreateTemporaryAccessToken(context.Background(), "w14f-acct", revision, 900); err == nil ||
		!strings.Contains(err.Error(), "update session account login") {
		t.Fatalf("last_login 更新失败应报错: %v", err)
	}

	// 提交失败。
	auth = build(t, func(state *w14fAuthState) { state.commitErr = errors.New("w14f issue commit failure") })
	if _, _, err := auth.CreateTemporaryAccessToken(context.Background(), "w14f-acct", revision, 900); err == nil ||
		!strings.Contains(err.Error(), "commit session issue") {
		t.Fatalf("签发提交失败应报错: %v", err)
	}
}

func TestW14fChangePasswordArms(t *testing.T) {
	const passwordHash = "pbkdf2$sha512$120000$MDEyMzQ1Njc4OWFiY2RlZg$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	revision := hashString(passwordHash)
	ctx := context.Background()

	build := func(t *testing.T, mutate func(*w14fAuthState)) *Authenticator {
		t.Helper()
		state := &w14fAuthState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "SELECT password_hash FROM") && strings.Contains(query, "LIMIT 1") {
				return w14fStaticRows(1, passwordHash), nil
			}
			_ = args
			return &w14fAuthRows{}, nil
		}}
		mutate(state)
		return w14fAuthScripted(t, Postgres, state)
	}

	// PG 锁子句 + 成功改密。
	auth := build(t, func(*w14fAuthState) {})
	changed, err := auth.ChangePassword(ctx, "w14f-acct", revision, "w14f-new-password", "w14f-session")
	if err != nil || !changed {
		t.Fatalf("pg change password: %v %v", changed, err)
	}

	// 开启事务失败。
	auth = build(t, func(state *w14fAuthState) { state.beginErr = errors.New("w14f begin failure") })
	if _, err := auth.ChangePassword(ctx, "w14f-acct", revision, "w14f-new-password", "w14f-session"); err == nil ||
		!strings.Contains(err.Error(), "begin password change") {
		t.Fatalf("改密开事务失败应报错: %v", err)
	}

	// 版本读取失败。
	auth = build(t, func(state *w14fAuthState) {
		state.queryFn = func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "SELECT password_hash FROM") && strings.Contains(query, "LIMIT 1") {
				return nil, errors.New("w14f revision read failure")
			}
			_ = args
			return &w14fAuthRows{}, nil
		}
	})
	if _, err := auth.ChangePassword(ctx, "w14f-acct", revision, "w14f-new-password", "w14f-session"); err == nil ||
		!strings.Contains(err.Error(), "read password revision") {
		t.Fatalf("版本读取失败应报错: %v", err)
	}

	// 新哈希写入失败。
	auth = build(t, func(state *w14fAuthState) {
		state.execFn = func(query string, args []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(query, "SET password_hash=") {
				return nil, errors.New("w14f persist failure")
			}
			_ = args
			return w14fResult(1), nil
		}
	})
	if _, err := auth.ChangePassword(ctx, "w14f-acct", revision, "w14f-new-password", "w14f-session"); err == nil ||
		!strings.Contains(err.Error(), "persist password change") {
		t.Fatalf("密码写入失败应报错: %v", err)
	}

	// 撤销其他会话失败。
	auth = build(t, func(state *w14fAuthState) {
		state.execFn = func(query string, args []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(query, "DELETE FROM") && strings.Contains(query, "id<>") {
				return nil, errors.New("w14f revoke failure")
			}
			_ = args
			return w14fResult(1), nil
		}
	})
	if _, err := auth.ChangePassword(ctx, "w14f-acct", revision, "w14f-new-password", "w14f-session"); err == nil ||
		!strings.Contains(err.Error(), "revoke sessions after password change") {
		t.Fatalf("改密后撤销会话失败应报错: %v", err)
	}

	// 提交失败。
	auth = build(t, func(state *w14fAuthState) { state.commitErr = errors.New("w14f change commit failure") })
	if _, err := auth.ChangePassword(ctx, "w14f-acct", revision, "w14f-new-password", "w14f-session"); err == nil ||
		!strings.Contains(err.Error(), "commit password change") {
		t.Fatalf("改密提交失败应报错: %v", err)
	}
}

func TestW14fDisplayNameAndCleanupArms(t *testing.T) {
	ctx := context.Background()

	// UpdateDisplayName 执行失败。
	auth := w14fAuthScripted(t, SQLite, &w14fAuthState{execFn: func(query string, args []driver.NamedValue) (driver.Result, error) {
		if strings.Contains(query, "SET display_name=") {
			return nil, errors.New("w14f display name failure")
		}
		_ = args
		return w14fResult(1), nil
	}})
	if _, err := auth.UpdateDisplayName(ctx, "w14f-acct", "w14f-name"); err == nil ||
		!strings.Contains(err.Error(), "update display name") {
		t.Fatalf("显示名更新失败应报错: %v", err)
	}

	// UpdateDisplayName 未命中行。
	auth = w14fAuthScripted(t, SQLite, &w14fAuthState{execFn: func(query string, args []driver.NamedValue) (driver.Result, error) {
		_ = args
		return w14fResult(0), nil
	}})
	changed, err := auth.UpdateDisplayName(ctx, "w14f-acct", "w14f-name")
	if err != nil || changed {
		t.Fatalf("未命中行应返回 false: %v %v", changed, err)
	}

	// CleanupExpiredSessions 执行失败。
	auth = w14fAuthScripted(t, SQLite, &w14fAuthState{execFn: func(query string, args []driver.NamedValue) (driver.Result, error) {
		if strings.Contains(query, "DELETE FROM") && strings.Contains(query, "expires_at<=") {
			return nil, errors.New("w14f cleanup failure")
		}
		_ = args
		return w14fResult(1), nil
	}})
	if _, err := auth.CleanupExpiredSessions(ctx, time.Unix(1_000, 0), 10); err == nil ||
		!strings.Contains(err.Error(), "cleanup expired sessions") {
		t.Fatalf("清理执行失败应报错: %v", err)
	}

	// CleanupExpiredSessions RowsAffected 失败。
	auth = w14fAuthScripted(t, SQLite, &w14fAuthState{execFn: func(query string, args []driver.NamedValue) (driver.Result, error) {
		_ = args
		return w14fAuthResult{rowsAffected: 0, raErr: errors.New("w14f cleanup count failure")}, nil
	}})
	if _, err := auth.CleanupExpiredSessions(ctx, time.Unix(1_000, 0), 10); err == nil ||
		!strings.Contains(err.Error(), "read expired session cleanup count") {
		t.Fatalf("清理计数失败应报错: %v", err)
	}
}

// ---- HTTP 处理器错误臂（脚本化驱动 + 预置守卫） ----

func w14fHandlerDispatch(t *testing.T, mutate func(*w14fAuthState)) (*HTTPHandler, *w14fAuthState) {
	t.Helper()
	passwordHash := w14fHandlerPasswordHash
	state := &w14fAuthState{queryFn: func(query string, args []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(query, "SELECT ss.id,ss.expires_at"):
			return w14fStaticRows(8, w14fSessionValues("admin")...), nil
		case strings.Contains(query, "lower(username)=lower("):
			return w14fStaticRows(7, "w14f-acct", "w14f-admin", "管理员", "admin", "active", passwordHash, 0), nil
		case strings.Contains(query, "SELECT status,password_hash FROM"):
			return w14fStaticRows(2, "active", passwordHash), nil
		case strings.Contains(query, "SELECT password_hash FROM") && strings.Contains(query, "LIMIT 1"):
			return w14fStaticRows(1, passwordHash), nil
		case strings.Contains(query, "SELECT password_hash FROM"):
			return w14fStaticRows(1, passwordHash), nil
		}
		_ = args
		return &w14fAuthRows{}, nil
	}}
	mutate(state)
	auth := w14fAuthScripted(t, SQLite, state)
	handler := &HTTPHandler{
		Auth:                       auth,
		TemporaryAccessIPAllowlist: []string{"192.0.2.1"},
	}
	return handler, state
}

func w14fPost(handler *HTTPHandler, path, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.RemoteAddr = "192.0.2.1:1234"
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestW14fTemporaryAccessTokenErrorArms(t *testing.T) {
	body := `{"username":"w14f-admin","password":"w14f-password","ttlSeconds":900}`
	verifyError := func(state *w14fAuthState) {
		state.queryFn = func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "lower(username)=lower(") {
				return nil, errors.New("w14f verify failure")
			}
			_ = args
			return &w14fAuthRows{}, nil
		}
	}
	handler, _ := w14fHandlerDispatch(t, verifyError)
	if recorder := w14fPost(handler, "/temporary-access-tokens", body); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("verify 失败应 500: %d %s", recorder.Code, recorder.Body.String())
	}

	// 验证通过后签发开启事务失败 → 500。
	handler, _ = w14fHandlerDispatch(t, func(state *w14fAuthState) { state.beginErr = errors.New("w14f begin failure") })
	if recorder := w14fPost(handler, "/temporary-access-tokens", body); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("签发失败应 500: %d %s", recorder.Code, recorder.Body.String())
	}

	// 签发返回 issuedOK=false（账户行缺失）+ 守卫未锁定 → 401。
	handler, _ = w14fHandlerDispatch(t, func(state *w14fAuthState) {
		state.queryFn = func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "SELECT status,password_hash FROM") {
				return &w14fAuthRows{columns: make([]string, 2)}, nil
			}
			return w14fHandlerBaseQuery(query, args)
		}
	})
	if recorder := w14fPost(handler, "/temporary-access-tokens", body); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("issuedOK=false 应 401: %d %s", recorder.Code, recorder.Body.String())
	}

	// issuedOK=false + 守卫锁定（预置 9 次失败，本次为第 10 次）→ 429 + Retry-After。
	handler, _ = w14fHandlerDispatch(t, func(state *w14fAuthState) {
		state.queryFn = func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "SELECT status,password_hash FROM") {
				return &w14fAuthRows{columns: make([]string, 2)}, nil
			}
			return w14fHandlerBaseQuery(query, args)
		}
	})
	guard := handler.loginGuard()
	for i := 0; i < loginGuardLimit-1; i++ {
		guard.Failed("192.0.2.1", "w14f-admin")
	}
	recorder := w14fPost(handler, "/temporary-access-tokens", body)
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") == "" {
		t.Fatalf("签发失败+锁定应 429: %d %s", recorder.Code, recorder.Body.String())
	}
}

// w14fHandlerPasswordHash 是处理器脚本行使用的真实 PBKDF2 哈希（对应口令
// "w14f-password"），保证 VerifySystemAccountCredentials 的真实验证路径可通过。
var w14fHandlerPasswordHash = w11ePasswordHash("w14f-password")

// w14fHandlerBaseQuery 是处理器分发的默认查询面（供局部覆写时复用）。
func w14fHandlerBaseQuery(query string, args []driver.NamedValue) (driver.Rows, error) {
	passwordHash := w14fHandlerPasswordHash
	switch {
	case strings.Contains(query, "SELECT ss.id,ss.expires_at"):
		return w14fStaticRows(8, w14fSessionValues("admin")...), nil
	case strings.Contains(query, "lower(username)=lower("):
		return w14fStaticRows(7, "w14f-acct", "w14f-admin", "管理员", "admin", "active", passwordHash, 0), nil
	case strings.Contains(query, "SELECT status,password_hash FROM"):
		return w14fStaticRows(2, "active", passwordHash), nil
	case strings.Contains(query, "SELECT password_hash FROM"):
		return w14fStaticRows(1, passwordHash), nil
	}
	_ = args
	return &w14fAuthRows{}, nil
}

func TestW14fRevokeTokenErrorArm(t *testing.T) {
	handler, _ := w14fHandlerDispatch(t, func(state *w14fAuthState) {
		state.execFn = func(query string, args []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(query, "DELETE FROM") && strings.Contains(query, "token_hash=") {
				return nil, errors.New("w14f revoke failure")
			}
			_ = args
			return w14fResult(1), nil
		}
	})
	recorder := w14fPost(handler, "/temporary-access-tokens/revoke", "")
	request := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens/revoke", nil)
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Authorization", "Bearer "+w14fTokenValue(t))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("撤销失败应 500: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestW14fLoginGuardBlockedArms(t *testing.T) {
	// 预置守卫锁定 → /login 直接 429（Retry-After 头）。
	handler, _ := w14fHandlerDispatch(t, func(*w14fAuthState) {})
	guard := handler.loginGuard()
	for i := 0; i < loginGuardLimit; i++ {
		guard.Failed("192.0.2.1", "w14f-admin")
	}
	recorder := w14fPost(handler, "/login", `{"username":"w14f-admin","password":"w14f-password"}`)
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") == "" {
		t.Fatalf("登录锁定应 429: %d %s", recorder.Code, recorder.Body.String())
	}

	// 登录失败 + 第 10 次 Failed 触发锁定 → 429。
	handler, _ = w14fHandlerDispatch(t, func(*w14fAuthState) {})
	guard = handler.loginGuard()
	for i := 0; i < loginGuardLimit-1; i++ {
		guard.Failed("192.0.2.1", "w14f-admin")
	}
	badPassword := `{"username":"w14f-admin","password":"w14f-wrong"}`
	recorder = w14fPost(handler, "/login", badPassword)
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") == "" {
		t.Fatalf("登录失败锁定应 429: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestW14fProfileArms(t *testing.T) {
	authenticated := func(state *w14fAuthState) {
		state.queryFn = func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "SELECT ss.id,ss.expires_at") {
				return w14fStaticRows(8, w14fSessionValues("admin")...), nil
			}
			return w14fHandlerBaseQuery(query, args)
		}
	}
	// 显示名更新失败 → 500。
	handler, _ := w14fHandlerDispatch(t, func(state *w14fAuthState) {
		authenticated(state)
		state.execFn = func(query string, args []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(query, "SET display_name=") {
				return nil, errors.New("w14f display failure")
			}
			_ = args
			return w14fResult(1), nil
		}
	})
	request := httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(`{"displayName":"w14f-name"}`))
	request.RemoteAddr = "192.0.2.1:1234"
	request.Header.Set("Authorization", "Bearer "+w14fTokenValue(t))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("显示名失败应 500: %d %s", recorder.Code, recorder.Body.String())
	}

	// 显示名未命中行 → 404。
	handler, _ = w14fHandlerDispatch(t, func(state *w14fAuthState) {
		authenticated(state)
		state.execFn = func(query string, args []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(query, "SET display_name=") {
				return w14fResult(0), nil
			}
			_ = args
			return w14fResult(1), nil
		}
	})
	request = httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(`{"displayName":"w14f-name"}`))
	request.Header.Set("Authorization", "Bearer "+w14fTokenValue(t))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("显示名未命中应 404: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestW14fChangePasswordHandlerArms(t *testing.T) {
	const passwordHash = "pbkdf2$sha512$120000$MDEyMzQ1Njc4OWFiY2RlZg$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	body := `{"oldPassword":"w14f-password","newPassword":"w14f-new-password"}`

	withAuth := func(mutate func(*w14fAuthState)) *HTTPHandler {
		handler, _ := w14fHandlerDispatch(t, func(inner *w14fAuthState) {
			inner.queryFn = func(query string, args []driver.NamedValue) (driver.Rows, error) {
				if strings.Contains(query, "SELECT ss.id,ss.expires_at") {
					return w14fStaticRows(8, w14fSessionValues("admin")...), nil
				}
				return w14fHandlerBaseQuery(query, args)
			}
			mutate(inner)
		})
		return handler
	}
	doRequest := func(handler *HTTPHandler) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+w14fTokenValue(t))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	// 无令牌 → 401（actor requestToken 错误臂）。
	handler, _ := w14fHandlerDispatch(t, func(*w14fAuthState) {})
	if recorder := w14fPost(handler, "/change-password", body); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("无令牌改密应 401: %d %s", recorder.Code, recorder.Body.String())
	}

	// 当前版本读取失败 → 404。
	handler = withAuth(func(state *w14fAuthState) {
		state.queryFn = func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "SELECT password_hash FROM") {
				return nil, errors.New("w14f revision failure")
			}
			if strings.Contains(query, "SELECT ss.id,ss.expires_at") {
				return w14fStaticRows(8, w14fSessionValues("admin")...), nil
			}
			_ = args
			return &w14fAuthRows{}, nil
		}
	})
	if recorder := doRequest(handler); recorder.Code != http.StatusNotFound {
		t.Fatalf("版本读取失败应 404: %d %s", recorder.Code, recorder.Body.String())
	}

	// 旧密码验证查询失败 → 500。
	handler = withAuth(func(state *w14fAuthState) {
		state.queryFn = func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "lower(username)=lower(") {
				return nil, errors.New("w14f verify failure")
			}
			if strings.Contains(query, "SELECT ss.id,ss.expires_at") {
				return w14fStaticRows(8, w14fSessionValues("admin")...), nil
			}
			return w14fHandlerBaseQuery(query, args)
		}
	})
	if recorder := doRequest(handler); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("旧密码验证失败应 500: %d %s", recorder.Code, recorder.Body.String())
	}

	// ChangePassword 开事务失败 → 500。
	handler = withAuth(func(state *w14fAuthState) { state.beginErr = errors.New("w14f begin failure") })
	if recorder := doRequest(handler); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("改密开事务失败应 500: %d %s", recorder.Code, recorder.Body.String())
	}

	// ChangePassword 版本围栏未命中 → 409。
	handler = withAuth(func(state *w14fAuthState) {
		state.queryFn = func(query string, args []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "SELECT password_hash FROM") && strings.Contains(query, "LIMIT 1") {
				return w14fStaticRows(1, "pbkdf2$sha512$120000$MDEyMzQ1Njc4OWFiY2RlZg$BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"), nil
			}
			if strings.Contains(query, "SELECT ss.id,ss.expires_at") {
				return w14fStaticRows(8, w14fSessionValues("admin")...), nil
			}
			return w14fHandlerBaseQuery(query, args)
		}
	})
	if recorder := doRequest(handler); recorder.Code != http.StatusConflict {
		t.Fatalf("版本围栏未命中应 409: %d %s", recorder.Code, recorder.Body.String())
	}
	_ = passwordHash
}

func TestW14fDecodeJSONDefaultBodyLimitArm(t *testing.T) {
	// MaxBody 未设置 → decodeJSON/maxBody 走默认 64KiB 臂；非法 JSON → 400。
	handler, _ := w14fHandlerDispatch(t, func(*w14fAuthState) {})
	recorder := w14fPost(handler, "/login", `{invalid`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400: %d %s", recorder.Code, recorder.Body.String())
	}
}
