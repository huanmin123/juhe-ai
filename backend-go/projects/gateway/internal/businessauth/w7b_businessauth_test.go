package businessauth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
	_ "modernc.org/sqlite"
)

// w7b：Business SQLite owner 门禁适配层的全接口覆盖（进程内 sqlite 直驱）。
func TestW7BBusinessAuthFullSurface(t *testing.T) {
	db := openBusinessAuthDB(t)
	defer db.Close()
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	const passwordHash = "pbkdf2$sha512$120000$MDEyMzQ1Njc4OWFiY2RlZg$MB16ie0MUIkgM1Xio7iCM8x9uCDqJJf5rkQ297w84fg"
	if _, err := db.Exec(`INSERT INTO system_accounts(id,username,display_name,role,status,password_hash,must_change_password) VALUES ('acct','admin','Admin','admin','active',?,0)`, passwordHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_accounts(id,username,display_name,role,status,password_hash,must_change_password) VALUES ('acct2','mustchange','Must','admin','active',?,1)`, passwordHash); err != nil {
		t.Fatal(err)
	}
	// 构造参数校验。
	if _, err := New(nil, modelcheckauth.SQLite, time.Now, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}); err == nil {
		t.Fatal("nil db 必须报错")
	}
	service, err := New(db, modelcheckauth.SQLite, func() time.Time { return now }, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// 契约校验（只读事务 + 关系列校验）。
	if err := service.CheckContract(ctx); err != nil {
		t.Fatalf("CheckContract = %v", err)
	}

	// CreateSession / CreateTemporaryToken。
	revision, err := service.CurrentCredentialRevision(ctx, "acct")
	if err != nil {
		t.Fatal(err)
	}
	issued, ok, err := service.CreateSession(ctx, "acct", revision, 1)
	if err != nil || !ok || issued.Token == "" {
		t.Fatalf("CreateSession = %+v ok=%v err=%v", issued, ok, err)
	}
	tmp, ok, err := service.CreateTemporaryToken(ctx, "acct", revision, 300)
	if err != nil || !ok || !strings.HasPrefix(tmp.Token, "juhe_tmp_") {
		t.Fatalf("CreateTemporaryToken = %+v ok=%v err=%v", tmp, ok, err)
	}

	// Authenticate 四种标志组合 + Touch。
	for _, combo := range [][2]bool{{false, false}, {false, true}, {true, true}, {true, false}} {
		actor, err := service.Authenticate(ctx, issued.Token, combo[0], combo[1])
		if err != nil || actor.SessionID != issued.SessionID {
			t.Fatalf("Authenticate(%v) = %+v err=%v", combo, actor, err)
		}
	}
	if actor, err := service.Authenticate(ctx, tmp.Token, false, false); err != nil || actor.SessionID == "" {
		t.Fatalf("临时令牌认证 = %+v err=%v", actor, err)
	}

	// 必改密码账户：no-touch 读取必须拒绝，touch 路径放行。
	mustIssued, _, ok, err := service.Login(ctx, "mustchange", "correct horse battery staple", 1)
	if err != nil || !ok {
		t.Fatalf("must-change 登录 ok=%v err=%v", ok, err)
	}
	if _, err := service.Authenticate(ctx, mustIssued.Token, true, false); !errors.Is(err, modelcheckauth.ErrMustChange) {
		t.Fatalf("must-change 拒绝 = %v", err)
	}
	// rejectMustChange=true 的 touch 路径同样围栏必改密码账户。
	if _, err := service.Authenticate(ctx, mustIssued.Token, true, true); !errors.Is(err, modelcheckauth.ErrMustChange) {
		t.Fatalf("touch 围栏 = %v", err)
	}
	// reject=false + touch：仍可用于触达（行为与 AuthenticateTokenForSession 一致）。
	if actor, err := service.Authenticate(ctx, mustIssued.Token, false, true); err != nil || actor.SessionID == "" {
		t.Fatalf("reject=false touch = %+v err=%v", actor, err)
	}

	// VerifyCredentials：大小写归一 + 错误密码。
	if creds, ok, err := service.VerifyCredentials(ctx, " ADMIN ", "correct horse battery staple"); err != nil || !ok || creds.SystemAccountID != "acct" {
		t.Fatalf("VerifyCredentials = %+v ok=%v err=%v", creds, ok, err)
	}
	if _, ok, err := service.VerifyCredentials(ctx, "admin", "wrong"); err != nil || ok {
		t.Fatalf("错误密码 ok=%v err=%v", ok, err)
	}

	// RevokeSession / RevokeOtherSessions。
	extra, ok, err := service.CreateSession(ctx, "acct", revision, 1)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := service.RevokeOtherSessions(ctx, "acct", issued.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, extra.Token, false, true); !errors.Is(err, modelcheckauth.ErrSessionExpired) {
		t.Fatalf("其余会话必须被吊销: %v", err)
	}
	if err := service.RevokeSession(ctx, issued.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, issued.Token, false, true); !errors.Is(err, modelcheckauth.ErrSessionExpired) {
		t.Fatalf("定向吊销后必须失效: %v", err)
	}

	// CleanupExpiredSessions：预置一条过期会话后清理。
	expireAt := now.Add(-time.Hour).UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
	if _, err := db.Exec(`INSERT INTO system_sessions(id,system_account_id,token_hash,expires_at,created_at,last_seen_at) VALUES ('w7b-expired','acct','w7b-expired-hash',?,?,?)`, expireAt, expireAt, expireAt); err != nil {
		t.Fatal(err)
	}
	cleaned, err := service.CleanupExpiredSessions(ctx, now, 10)
	if err != nil || cleaned < 1 {
		t.Fatalf("清理 = %d err=%v", cleaned, err)
	}
}

func TestW7BBusinessAuthGateFailsClosed(t *testing.T) {
	db := openBusinessAuthDB(t)
	defer db.Close()
	partial, err := New(db, modelcheckauth.SQLite, time.Now, OwnerGate{Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, _, err := partial.Login(ctx, "u", "p", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Login = %v", err)
	}
	if _, _, err := partial.CreateSession(ctx, "a", "r", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("CreateSession = %v", err)
	}
	if _, _, err := partial.CreateTemporaryToken(ctx, "a", "r", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("CreateTemporaryToken = %v", err)
	}
	if _, err := partial.Authenticate(ctx, "token", false, true); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Authenticate = %v", err)
	}
	if _, err := partial.Touch(ctx, "token"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Touch = %v", err)
	}
	if err := partial.RevokeToken(ctx, "token"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("RevokeToken = %v", err)
	}
	if err := partial.Logout(ctx, "token"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("Logout = %v", err)
	}
	if err := partial.RevokeSession(ctx, "sid"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("RevokeSession = %v", err)
	}
	if err := partial.RevokeOtherSessions(ctx, "a", "sid"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("RevokeOtherSessions = %v", err)
	}
	if _, err := partial.ChangePassword(ctx, "a", "r", "n", "sid"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("ChangePassword = %v", err)
	}
	if _, err := partial.CleanupExpiredSessions(ctx, time.Now(), 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("CleanupExpiredSessions = %v", err)
	}
	if _, _, err := partial.VerifyCredentials(ctx, "u", "p"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("VerifyCredentials = %v", err)
	}
	if _, err := partial.CurrentCredentialRevision(ctx, "a"); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("CurrentCredentialRevision = %v", err)
	}

	// nil 接收者 fail-closed。
	var nilService *Service
	if _, _, _, err := nilService.Login(ctx, "u", "p", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil Login = %v", err)
	}
	if err := nilService.CheckContract(ctx); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil CheckContract = %v", err)
	}

	// 无效 mode 的构造错误分支。
	badDB, err := sql.Open("sqlite", "file:w7b-bad?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer badDB.Close()
	if _, err := New(badDB, modelcheckauth.Mode(42), time.Now, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}); err == nil {
		t.Fatal("非法 mode 必须报错")
	}
}
