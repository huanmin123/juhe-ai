package modelcheckauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestAuthenticateBearerTouchesSessionAndRequiresAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "business.db")
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT,status TEXT NOT NULL,role TEXT NOT NULL,must_change_password INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,token_hash TEXT NOT NULL,expires_at TEXT NOT NULL,last_seen_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	token := "juhe_tmp_" + "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLM1234"
	if len(token) != len("juhe_tmp_")+43 {
		t.Fatalf("test token length=%d", len(token))
	}
	digest := sha256.Sum256([]byte(token))
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO system_accounts VALUES ('sys-1','admin','Admin','active','admin',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_sessions VALUES ('session-1','sys-1',?,?,?)`, hex.EncodeToString(digest[:]), now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(-2*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	auth, err := New(db, SQLite, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	actor, err := auth.RequireAdmin(context.Background(), "Bearer "+token, "")
	if err != nil || actor.SystemAccountID != "sys-1" || actor.Role != "admin" {
		t.Fatalf("actor=%+v err=%v", actor, err)
	}
	var seen string
	if err := db.QueryRow(`SELECT last_seen_at FROM system_sessions WHERE id='session-1'`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != "2026-08-27T12:00:00.000Z" {
		t.Fatalf("last_seen_at=%q", seen)
	}
}

func TestResolveTokenRejectsInvalidBearer(t *testing.T) {
	if _, err := resolveToken("Bearer invalid", ""); err != ErrInvalidToken {
		t.Fatalf("err=%v, want ErrInvalidToken", err)
	}
}

// TestResolveTokenPicksLastSessionCookie 锁定 last-wins：重名
// juhe_ai_session cookie 与 authsys.ParseCookie 的 map 覆盖语义一致，
// 取最后一个同名段（BUG-0256，两面不得分叉）。
func TestResolveTokenPicksLastSessionCookie(t *testing.T) {
	token, err := resolveToken("", "theme=dark; "+SessionCookieName+"=first-token; "+SessionCookieName+"=second-token")
	if err != nil || token != "second-token" {
		t.Fatalf("token=%q err=%v, want second-token", token, err)
	}
}

func TestResolveTokenKeepsSingleCookieAndBearerPrecedence(t *testing.T) {
	token, err := resolveToken("", "other=1; "+SessionCookieName+"=single-token")
	if err != nil || token != "single-token" {
		t.Fatalf("token=%q err=%v, want single-token", token, err)
	}
	valid := "juhe_tmp_" + "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLM1234"
	token, err = resolveToken("Bearer "+valid, SessionCookieName+"=cookie-token")
	if err != nil || token != valid {
		t.Fatalf("token=%q err=%v, want bearer precedence over cookie", token, err)
	}
}

// TestResolveTokenFailsClosedOnEmptySessionCookie 锁定 fail-closed：
// 任一同名段值空（含尾部重名空段）或完全无 cookie 都必须是
// ErrLoginRequired，不得静默跳过后继续取前一个值。
func TestResolveTokenFailsClosedOnEmptySessionCookie(t *testing.T) {
	for _, header := range []string{
		"",
		SessionCookieName + "=",
		"other=1; " + SessionCookieName + "=",
		SessionCookieName + "=valid-token; " + SessionCookieName + "=",
	} {
		if _, err := resolveToken("", header); err != ErrLoginRequired {
			t.Fatalf("header %q err=%v, want ErrLoginRequired", header, err)
		}
	}
}

func TestCheckContractRejectsMissingAuthRuntimeColumn(t *testing.T) {
	for _, omitted := range []string{"display_name", "role", "must_change_password", "last_login_at", "updated_at"} {
		t.Run(omitted, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "business.db")+"?mode=rwc")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			accountColumns := []string{"id TEXT PRIMARY KEY", "username TEXT NOT NULL", "display_name TEXT", "status TEXT NOT NULL", "role TEXT NOT NULL", "must_change_password INTEGER NOT NULL", "password_hash TEXT NOT NULL", "last_login_at TEXT", "updated_at TEXT NOT NULL"}
			filtered := make([]string, 0, len(accountColumns)-1)
			for _, definition := range accountColumns {
				if len(definition) >= len(omitted) && definition[:len(omitted)] == omitted {
					continue
				}
				filtered = append(filtered, definition)
			}
			if _, err := db.Exec(`CREATE TABLE system_accounts (` + joinDefinitions(filtered) + `)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE system_sessions (id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,token_hash TEXT NOT NULL,expires_at TEXT NOT NULL,created_at TEXT NOT NULL,last_seen_at TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}
			auth, err := New(db, SQLite, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			if err := auth.CheckContract(context.Background()); err == nil {
				t.Fatalf("missing %s must fail the auth contract", omitted)
			}
		})
	}
}

func joinDefinitions(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += ","
		}
		result += value
	}
	return result
}
