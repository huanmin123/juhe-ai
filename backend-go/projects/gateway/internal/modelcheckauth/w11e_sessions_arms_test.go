package modelcheckauth

// w11e modelcheckauth 第三批错误臂：会话签发/撤销/改密/清理的输入与存储
// 失败分支（nil 校验、修订失配、表缺失）。

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW11ESessionInputValidationArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	db := w11eAuthDB(t)
	auth := w11eAuth(t, db, now)
	ctx := context.Background()
	var nilAuth *Authenticator
	// createSession 输入不完整（nil authenticator / 空 account / 空 revision / 非法 TTL）。
	if _, ok, err := nilAuth.createSession(ctx, "a", "r", time.Minute, false); err == nil || ok {
		t.Fatal("nil createSession 必须拒绝")
	}
	if _, ok, err := auth.createSession(ctx, "  ", "r", time.Minute, false); err == nil || ok {
		t.Fatal("空账户必须拒绝")
	}
	if _, ok, err := auth.createSession(ctx, "w11e-acct", "  ", time.Minute, false); err == nil || ok {
		t.Fatal("空修订必须拒绝")
	}
	if _, ok, err := auth.createSession(ctx, "w11e-acct", "r", 0, false); err == nil || ok {
		t.Fatal("非法 TTL 必须拒绝")
	}
	// 账户不存在 → ok=false。
	revision, err := auth.CurrentCredentialRevision(ctx, "w11e-acct")
	if err != nil || revision == "" {
		t.Fatalf("修订=%q err=%v", revision, err)
	}
	if _, ok, err := auth.createSession(ctx, "w11e-missing", revision, time.Minute, false); err != nil || ok {
		t.Fatalf("缺失账户=%t err=%v", ok, err)
	}
	// 修订失配 → ok=false。
	if _, ok, err := auth.createSession(ctx, "w11e-acct", "stale-revision", time.Minute, false); err != nil || ok {
		t.Fatalf("修订失配=%t err=%v", ok, err)
	}
	// canceled createSession。
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := auth.createSession(canceled, "w11e-acct", revision, time.Minute, false); err == nil {
		t.Fatal("canceled createSession 必须失败")
	}
	// RevokeSession 输入与错误。
	if err := nilAuth.RevokeSession(ctx, "s"); err == nil {
		t.Fatal("nil RevokeSession 必须拒绝")
	}
	if err := auth.RevokeSession(ctx, "  "); err == nil {
		t.Fatal("空会话 ID 必须拒绝")
	}
	if err := auth.RevokeSession(canceled, "s"); err == nil {
		t.Fatal("canceled RevokeSession 必须失败")
	}
	// RevokeOtherSessions 输入。
	if err := nilAuth.RevokeOtherSessions(ctx, "a", "keep"); err == nil {
		t.Fatal("nil RevokeOtherSessions 必须拒绝")
	}
	if err := auth.RevokeOtherSessions(ctx, " ", "keep"); err == nil {
		t.Fatal("空账户必须拒绝")
	}
	if err := auth.RevokeOtherSessions(ctx, "a", " "); err == nil {
		t.Fatal("空保留会话必须拒绝")
	}
	// ChangePassword 输入。
	if changed, err := nilAuth.ChangePassword(ctx, "a", "r", "p", "s"); err == nil || changed {
		t.Fatal("nil ChangePassword 必须拒绝")
	}
	for _, tc := range [][4]string{{" ", "r", "p", "s"}, {"a", " ", "p", "s"}, {"a", "r", "", "s"}, {"a", "r", "p", " "}} {
		if changed, err := auth.ChangePassword(ctx, tc[0], tc[1], tc[2], tc[3]); err == nil || changed {
			t.Fatalf("输入 %+v 必须拒绝", tc)
		}
	}
	// UpdateDisplayName 输入。
	if changed, err := nilAuth.UpdateDisplayName(ctx, "a", "n"); err == nil || changed {
		t.Fatal("nil UpdateDisplayName 必须拒绝")
	}
	if changed, err := auth.UpdateDisplayName(ctx, " ", "n"); err == nil || changed {
		t.Fatal("空账户必须拒绝")
	}
	if changed, err := auth.UpdateDisplayName(ctx, "a", " "); err == nil || changed {
		t.Fatal("空名称必须拒绝")
	}
	// CurrentCredentialRevision 输入。
	if revision, err := nilAuth.CurrentCredentialRevision(ctx, "a"); err == nil || revision != "" {
		t.Fatal("nil 修订必须拒绝")
	}
	if revision, err := auth.CurrentCredentialRevision(ctx, " "); err == nil || revision != "" {
		t.Fatal("空账户修订必须拒绝")
	}
	// CleanupExpiredSessions 输入。
	if deleted, err := nilAuth.CleanupExpiredSessions(ctx, now, 10); err == nil || deleted != 0 {
		t.Fatal("nil 清理必须拒绝")
	}
	if deleted, err := auth.CleanupExpiredSessions(ctx, now, 0); err == nil || deleted != 0 {
		t.Fatal("非法 limit 必须拒绝")
	}
	if deleted, err := auth.CleanupExpiredSessions(ctx, now, 10001); err == nil || deleted != 0 {
		t.Fatal("超大 limit 必须拒绝")
	}
	if deleted, err := auth.CleanupExpiredSessions(ctx, time.Time{}, 10); err != nil {
		t.Fatalf("零值时间回退默认时钟=%d err=%v", deleted, err)
	}
}

func TestW11ESessionStorageFailureArms(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)
	db := w11eAuthDB(t)
	auth := w11eAuth(t, db, now)
	ctx := context.Background()
	revision, err := auth.CurrentCredentialRevision(ctx, "w11e-acct")
	if err != nil || revision == "" {
		t.Fatalf("修订=%q err=%v", revision, err)
	}
	// 会话表缺失：createSession persist 失败。
	if _, err := db.Exec(`DROP TABLE system_sessions`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.createSession(ctx, "w11e-acct", revision, time.Minute, false); err == nil || !strings.Contains(err.Error(), "persist session") {
		t.Fatalf("缺会话表=%v", err)
	}
	if err := auth.RevokeSession(ctx, "w11e-s"); err == nil || !strings.Contains(err.Error(), "revoke session") {
		t.Fatalf("缺会话表撤销=%v", err)
	}
	if err := auth.RevokeOtherSessions(ctx, "w11e-acct", "keep"); err == nil || !strings.Contains(err.Error(), "revoke other sessions") {
		t.Fatalf("缺会话表撤销其他=%v", err)
	}
	if deleted, err := auth.CleanupExpiredSessions(ctx, now, 10); err == nil || !strings.Contains(err.Error(), "cleanup expired sessions") {
		t.Fatalf("缺会话表清理=%d %v", deleted, err)
	}
	// 改密链失败（会话表缺失）。
	if changed, err := auth.ChangePassword(ctx, "w11e-acct", revision, "w11e-new-pass", "keep-session"); err == nil || changed || !strings.Contains(err.Error(), "revoke sessions after password change") {
		t.Fatalf("缺会话表改密=%t %v", changed, err)
	}
	// 账户表缺失：改密读取失败。
	if _, err := db.Exec(`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,token_hash TEXT UNIQUE,expires_at TEXT,created_at TEXT,last_seen_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE system_accounts`); err != nil {
		t.Fatal(err)
	}
	if changed, err := auth.ChangePassword(ctx, "w11e-acct", revision, "w11e-new-pass", "keep-session"); err == nil || changed {
		t.Fatalf("缺账户表改密=%t %v", changed, err)
	}
	if changed, err := auth.UpdateDisplayName(ctx, "w11e-acct", "n"); err == nil || changed {
		t.Fatalf("缺账户表资料=%t %v", changed, err)
	}
	if revision, err := auth.CurrentCredentialRevision(ctx, "w11e-acct"); err == nil || revision != "" {
		t.Fatalf("缺账户表修订=%q %v", revision, err)
	}
}
