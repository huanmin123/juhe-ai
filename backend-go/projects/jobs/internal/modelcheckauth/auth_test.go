package modelcheckauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestAuthenticateMatchesNodeCookieBearerAndSessionTouch(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	db := newAuthDB(t)
	insertSession(t, db, "session-token", "admin", "admin", false, now.Add(time.Hour), now.Add(-2*time.Minute))
	auth, err := New(db, Postgres, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.CheckContract(context.Background()); err != nil {
		t.Fatal(err)
	}
	actor, err := auth.RequireAdmin(context.Background(), "", "other=value; juhe_ai_session=session-token")
	if err != nil || actor.SystemAccountID != "admin" || actor.SessionID == "" {
		t.Fatalf("actor=%+v err=%v", actor, err)
	}
	var lastSeen string
	if err := db.QueryRow(`SELECT last_seen_at FROM juhe_business.system_sessions`).Scan(&lastSeen); err != nil || lastSeen != "2026-08-27T12:00:00.000Z" {
		t.Fatalf("lastSeen=%q err=%v", lastSeen, err)
	}
	temporary := "juhe_tmp_" + strings.Repeat("A", 43)
	insertSession(t, db, temporary, "super", "super_admin", false, now.Add(time.Hour), now)
	actor, err = auth.RequireAdmin(context.Background(), "Bearer "+temporary, "juhe_ai_session=session-token")
	if err != nil || actor.SystemAccountID != "super" {
		t.Fatalf("bearer precedence actor=%+v err=%v", actor, err)
	}
	if _, err = auth.Authenticate(context.Background(), "Bearer ordinary-session", "juhe_ai_session=session-token"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("ordinary bearer err=%v", err)
	}
}

func TestAuthenticateRejectsExpiredPasswordChangeAndUser(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	db := newAuthDB(t)
	insertSession(t, db, "expired", "admin", "admin", false, now.Add(-time.Second), now)
	insertSession(t, db, "change", "admin2", "admin", true, now.Add(time.Hour), now)
	insertSession(t, db, "user", "user", "user", false, now.Add(time.Hour), now)
	auth, _ := New(db, Postgres, func() time.Time { return now })
	if _, err := auth.Authenticate(context.Background(), "", "juhe_ai_session=expired"); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expired err=%v", err)
	}
	if _, err := auth.RequireAdmin(context.Background(), "", "juhe_ai_session=change"); !errors.Is(err, ErrMustChange) {
		t.Fatalf("must-change err=%v", err)
	}
	if _, err := auth.RequireAdmin(context.Background(), "", "juhe_ai_session=user"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("user err=%v", err)
	}
	if _, err := auth.Authenticate(context.Background(), "", ""); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("missing err=%v", err)
	}
}

func TestCookieValueMatchesNodePercentDecoding(t *testing.T) {
	if got := cookieValue("juhe_ai_session=a%2Bb%3D%3D", SessionCookieName); got != "a+b==" {
		t.Fatalf("decoded=%q", got)
	}
	if got := cookieValue("juhe_ai_session=first; juhe_ai_session=last", SessionCookieName); got != "last" {
		t.Fatalf("duplicate cookie=%q", got)
	}
	if got := cookieValue("juhe_ai_session=bad%ZZ", SessionCookieName); got != "" {
		t.Fatalf("bad cookie=%q", got)
	}
}

// newAuthDB 打开 pgtestkit 隔离 PG 测试库，并按生产 schema 前缀与生产列形
// 建 juhe_business.system_accounts / juhe_business.system_sessions（text
// 时间列，与生产 maintenance DDL 对齐；生产表另有 password_hash 等管理面
// 列，非认证链路读写面，夹具不复刻）。库生命周期由 pgtestkit 承接。
func newAuthDB(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `CREATE SCHEMA juhe_business`); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE juhe_business.system_accounts (id text PRIMARY KEY, username text NOT NULL, display_name text NOT NULL, role text NOT NULL, status text NOT NULL, must_change_password integer NOT NULL)`); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `CREATE TABLE juhe_business.system_sessions (id text PRIMARY KEY, system_account_id text NOT NULL, token_hash text NOT NULL UNIQUE, expires_at text NOT NULL, last_seen_at text NOT NULL)`)
		return err
	})
}

// insertSession 以 $n 占位符写入账户与会话行；时间戳用 nodeISOTime（生产
// timeValue 的毫秒 ISO 格式）写入 text 列，保证与生产写入格式逐字节一致，
// 触碰比较（text 字典序）与读回断言不受夹具格式影响。
func insertSession(t *testing.T, db *sql.DB, token, account, role string, mustChange bool, expiresAt, lastSeen time.Time) {
	t.Helper()
	sum := sha256.Sum256([]byte(token))
	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts (id, username, display_name, role, status, must_change_password) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`, account, account, account, role, "active", boolToInt(mustChange)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO juhe_business.system_sessions (id, system_account_id, token_hash, expires_at, last_seen_at) VALUES ($1,$2,$3,$4,$5)`, "session-"+account+"-"+token[:min(4, len(token))], account, hex.EncodeToString(sum[:]), nodeISOTime(expiresAt), nodeISOTime(lastSeen)); err != nil {
		t.Fatal(err)
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
