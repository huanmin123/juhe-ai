package modelcheckauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// 2026-10-22 dev 联调开关 JUHE_AI_AUTH_MUST_CHANGE_PASSWORD_GATE_DISABLED 的
// 归一契约:开启后 actor 与 credentials 构造处一律观察 mustChangePassword=false,
// rejectMustChange 面不再拒绝;存储列不写,默认(未开启)行为保持 BUG-0224 门禁。
func TestInitialPasswordGateDisabledNormalizesActorsAndCredentials(t *testing.T) {
	const passwordHash = "pbkdf2$sha512$120000$MDEyMzQ1Njc4OWFiY2RlZg$MB16ie0MUIkgM1Xio7iCM8x9uCDqJJf5rkQ297w84fg"
	db := newModelCheckAuthPGDatabase(t)
	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts (id,username,display_name,role,status,password_hash,must_change_password,created_at,updated_at) VALUES ('sys_admin','admin','超级管理员','super_admin','active',$1,1,'','')`, passwordHash); err != nil {
		t.Fatal(err)
	}
	token := "juhe_tmp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLM1234"
	if len(token) != len("juhe_tmp_")+43 {
		t.Fatalf("test token length=%d", len(token))
	}
	digest := sha256.Sum256([]byte(token))
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO juhe_business.system_sessions (id,system_account_id,token_hash,expires_at,created_at,last_seen_at) VALUES ('session-1','sys_admin',$1,$2,$3,$4)`, hex.EncodeToString(digest[:]), now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Add(-2*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	auth, err := New(db, Postgres, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	// 默认臂:BUG-0224 门禁生效——session 面可见 mustChangePassword=true,
	// reject 面拒绝,credentials 也带 true。
	sessionActor, err := auth.AuthenticateTokenForSession(context.Background(), token)
	if err != nil || !sessionActor.MustChangePassword {
		t.Fatalf("default session actor=%+v err=%v, want MustChangePassword=true", sessionActor, err)
	}
	if _, err := auth.AuthenticateToken(context.Background(), token); !errors.Is(err, ErrMustChange) {
		t.Fatalf("default reject err=%v, want ErrMustChange", err)
	}
	verified, ok, err := auth.VerifySystemAccountCredentials(context.Background(), "admin", "correct horse battery staple")
	if err != nil || !ok || !verified.MustChangePassword {
		t.Fatalf("default verified=%+v ok=%v err=%v, want MustChangePassword=true", verified, ok, err)
	}

	// 开关臂:归一为 false——session 面、reject 面、credentials 面全部放行,
	// 存储列保持 1。
	auth.SetInitialPasswordGateDisabled(true)
	gatedActor, err := auth.AuthenticateTokenForSession(context.Background(), token)
	if err != nil || gatedActor.MustChangePassword {
		t.Fatalf("gated session actor=%+v err=%v, want MustChangePassword=false", gatedActor, err)
	}
	rejectActor, err := auth.AuthenticateToken(context.Background(), token)
	if err != nil || rejectActor.MustChangePassword {
		t.Fatalf("gated reject actor=%+v err=%v, want no ErrMustChange", rejectActor, err)
	}
	gatedVerified, ok, err := auth.VerifySystemAccountCredentials(context.Background(), "admin", "correct horse battery staple")
	if err != nil || !ok || gatedVerified.MustChangePassword {
		t.Fatalf("gated verified=%+v ok=%v err=%v, want MustChangePassword=false", gatedVerified, ok, err)
	}
	var stored int
	if err := db.QueryRow(`SELECT must_change_password FROM juhe_business.system_accounts WHERE id='sys_admin'`).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("stored must_change_password=%d err=%v, want untouched 1", stored, err)
	}
}
