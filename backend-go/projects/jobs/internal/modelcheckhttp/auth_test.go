package modelcheckhttp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckcommand"
	_ "modernc.org/sqlite"
)

func TestNewAdminAuthorizeFuncDirectlyChecksManagementSession(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	db := newManagementAuthDB(t)
	defer db.Close()
	insertManagementSession(t, db, "session", "admin", "admin", false, now.Add(time.Hour))
	auth, err := modelcheckauth.New(db, modelcheckauth.SQLite, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/run", nil)
	request.Header.Set("Cookie", "juhe_ai_session=session")
	scope, err := NewAdminAuthorizeFunc(auth)(context.Background(), request)
	if err != nil || scope.SystemAccountID != "admin" || scope.ActorSystemAccountID != "admin" || scope.ActorRole != "admin" {
		t.Fatalf("scope=%+v err=%v", scope, err)
	}
	insertManagementSession(t, db, "change", "change", "admin", true, now.Add(time.Hour))
	request = httptest.NewRequest(http.MethodPost, "/run", nil)
	request.Header.Set("Cookie", "juhe_ai_session=change")
	_, err = NewAdminAuthorizeFunc(auth)(context.Background(), request)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusForbidden || httpErr.Code != "must_change_password" {
		t.Fatalf("error=%v http=%+v", err, httpErr)
	}
}

func TestBuildRequestCarriesDistinctActorScope(t *testing.T) {
	builder, err := modelcheckcommand.New(modelcheckcommand.Config{Freezer: httpFakeFreezer{}, PolicyLoader: httpFakePolicyLoader{}, ProbeSetVersion: "probe-v1", Deadline: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewBuildRequestFunc(builder)(context.Background(), Scope{SystemAccountID: "managed", ActorSystemAccountID: "admin", ActorRole: "super_admin"}, Command{TargetType: "account", TargetID: "target-1", Model: "gpt-5.6-sol", Profile: "quick"})
	if err != nil || request.SystemAccountID != "managed" || request.ActorSystemAccountID != "admin" {
		t.Fatalf("request=%+v err=%v", request, err)
	}
}

func TestAdminTargetScopeResolverAndActiveKeyKeepActorSeparate(t *testing.T) {
	resolver := NewAdminTargetScopeResolver(fakeManagementTargetScopeReader{systemAccountID: "target-owner"})
	scope, err := resolver(context.Background(), Scope{SystemAccountID: "admin", ActorSystemAccountID: "admin", ActorRole: "super_admin"}, Command{TargetID: "target"})
	if err != nil || scope.SystemAccountID != "target-owner" || activeKey(scope) != "system-account:admin" {
		t.Fatalf("scope=%+v err=%v key=%q", scope, err, activeKey(scope))
	}
	scope, err = resolver(context.Background(), Scope{SystemAccountID: "filtered", ActorSystemAccountID: "admin", SystemAccountFilterID: "filtered"}, Command{TargetID: "target"})
	if err != nil || scope.SystemAccountID != "filtered" {
		t.Fatalf("explicit filter scope=%+v err=%v", scope, err)
	}
}

func TestAdminAuthorizeScopeFilterMatchesNodeQueryContract(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	db := newManagementAuthDB(t)
	defer db.Close()
	insertManagementSession(t, db, "session", "admin", "admin", false, now.Add(time.Hour))
	auth, _ := modelcheckauth.New(db, modelcheckauth.SQLite, func() time.Time { return now })
	request := httptest.NewRequest(http.MethodPost, "/run?systemAccountId=managed", nil)
	request.Header.Set("Cookie", "juhe_ai_session=session")
	scope, err := NewAdminAuthorizeFunc(auth)(context.Background(), request)
	if err != nil || scope.SystemAccountID != "managed" || scope.SystemAccountFilterID != "managed" {
		t.Fatalf("scope=%+v err=%v", scope, err)
	}
	request = httptest.NewRequest(http.MethodPost, "/run?systemAccountId=&systemAccountId=other", nil)
	request.Header.Set("Cookie", "juhe_ai_session=session")
	_, err = NewAdminAuthorizeFunc(auth)(context.Background(), request)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadRequest {
		t.Fatalf("filter error=%v", err)
	}
}

type fakeManagementTargetScopeReader struct {
	systemAccountID string
	err             error
}

func (r fakeManagementTargetScopeReader) ResolveManagementSystemAccount(context.Context, string) (string, error) {
	return r.systemAccountID, r.err
}

func newManagementAuthDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE system_accounts(id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT NOT NULL,role TEXT NOT NULL,status TEXT NOT NULL,must_change_password INTEGER NOT NULL); CREATE TABLE system_sessions(id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,token_hash TEXT NOT NULL UNIQUE,expires_at TEXT NOT NULL,last_seen_at TEXT NOT NULL)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}
func insertManagementSession(t *testing.T, db *sql.DB, token, account, role string, mustChange bool, expires time.Time) {
	t.Helper()
	sum := sha256.Sum256([]byte(token))
	changed := 0
	if mustChange {
		changed = 1
	}
	if _, err := db.Exec(`INSERT INTO system_accounts VALUES(?,?,?,?,?,?)`, account, account, account, role, "active", changed); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO system_sessions VALUES(?,?,?,?,?)`, "session-"+account, account, hex.EncodeToString(sum[:]), expires.Format(time.RFC3339Nano), expires.Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}
