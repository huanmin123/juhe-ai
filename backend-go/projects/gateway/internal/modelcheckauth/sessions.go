package modelcheckauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type IssuedSession struct {
	Token, SessionID string
	ExpiresAt        time.Time
}

// CreateAuthenticatedSession creates a durable session and advances
// last_login_at in one owner transaction. credentialRevision is the same
// password-derived revision checked by the Node oracle.
func (a *Authenticator) CreateAuthenticatedSession(ctx context.Context, systemAccountID, credentialRevision string, ttlDays int) (IssuedSession, bool, error) {
	return a.createSession(ctx, systemAccountID, credentialRevision, time.Duration(maxInt(ttlDays, 1))*24*time.Hour, false)
}

// CreateTemporaryAccessToken creates a short-lived token with the Node token
// prefix and the same account/password revision fence as normal login.
func (a *Authenticator) CreateTemporaryAccessToken(ctx context.Context, systemAccountID, credentialRevision string, ttlSeconds int) (IssuedSession, bool, error) {
	return a.createSession(ctx, systemAccountID, credentialRevision, time.Duration(maxInt(ttlSeconds, 1))*time.Second, true)
}

func (a *Authenticator) createSession(ctx context.Context, accountID, credentialRevision string, ttl time.Duration, temporary bool) (IssuedSession, bool, error) {
	if a == nil || a.db == nil || strings.TrimSpace(accountID) == "" || strings.TrimSpace(credentialRevision) == "" || ttl <= 0 {
		return IssuedSession{}, false, errors.New("session issue input is incomplete")
	}
	if ttl > 14*24*time.Hour && !temporary {
		ttl = 14 * 24 * time.Hour
	}
	token, err := randomToken(temporary)
	if err != nil {
		return IssuedSession{}, false, err
	}
	sessionID, err := randomID("tmp_sess", temporary)
	if err != nil {
		return IssuedSession{}, false, err
	}
	now := a.now().UTC()
	expires := now.Add(ttl)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return IssuedSession{}, false, fmt.Errorf("begin session issue: %w", err)
	}
	defer tx.Rollback()
	lock := ""
	if a.mode == Postgres {
		lock = " FOR UPDATE"
	}
	var status, passwordHash string
	if err := tx.QueryRowContext(ctx, a.bind(`SELECT status,password_hash FROM `+a.table("system_accounts")+` WHERE id=? LIMIT 1`+lock), accountID).Scan(&status, &passwordHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IssuedSession{}, false, nil
		}
		return IssuedSession{}, false, fmt.Errorf("read session account: %w", err)
	}
	if status != "active" || hashString(passwordHash) != credentialRevision {
		return IssuedSession{}, false, nil
	}
	nowText, expiresText := nodeISOTime(now), nodeISOTime(expires)
	if _, err := tx.ExecContext(ctx, a.bind(`INSERT INTO `+a.table("system_sessions")+` (id,system_account_id,token_hash,expires_at,created_at,last_seen_at) VALUES (?,?,?,?,?,?)`), sessionID, accountID, hashString(token), expiresText, nowText, nowText); err != nil {
		return IssuedSession{}, false, fmt.Errorf("persist session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, a.bind(`UPDATE `+a.table("system_accounts")+` SET last_login_at=? WHERE id=?`), nowText, accountID); err != nil {
		return IssuedSession{}, false, fmt.Errorf("update session account login: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return IssuedSession{}, false, fmt.Errorf("commit session issue: %w", err)
	}
	return IssuedSession{Token: token, SessionID: sessionID, ExpiresAt: expires}, true, nil
}

func (a *Authenticator) RevokeToken(ctx context.Context, token string) error {
	if a == nil || a.db == nil || strings.TrimSpace(token) == "" {
		return errors.New("session revoke token is required")
	}
	_, err := a.db.ExecContext(ctx, a.bind(`DELETE FROM `+a.table("system_sessions")+` WHERE token_hash=?`), hashString(token))
	if err != nil {
		return fmt.Errorf("revoke session token: %w", err)
	}
	return nil
}

func (a *Authenticator) RevokeSession(ctx context.Context, sessionID string) error {
	if a == nil || a.db == nil || strings.TrimSpace(sessionID) == "" {
		return errors.New("session ID is required")
	}
	_, err := a.db.ExecContext(ctx, a.bind(`DELETE FROM `+a.table("system_sessions")+` WHERE id=?`), sessionID)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func (a *Authenticator) RevokeOtherSessions(ctx context.Context, systemAccountID, keepSessionID string) error {
	if a == nil || a.db == nil || strings.TrimSpace(systemAccountID) == "" || strings.TrimSpace(keepSessionID) == "" {
		return errors.New("session account and keep ID are required")
	}
	_, err := a.db.ExecContext(ctx, a.bind(`DELETE FROM `+a.table("system_sessions")+` WHERE system_account_id=? AND id<>?`), systemAccountID, keepSessionID)
	if err != nil {
		return fmt.Errorf("revoke other sessions: %w", err)
	}
	return nil
}

func (a *Authenticator) CleanupExpiredSessions(ctx context.Context, now time.Time, limit int) (int64, error) {
	if a == nil || a.db == nil || limit <= 0 || limit > 10000 {
		return 0, errors.New("expired session cleanup input is invalid")
	}
	if now.IsZero() {
		now = a.now()
	}
	query := `DELETE FROM ` + a.table("system_sessions") + ` WHERE id IN (SELECT id FROM ` + a.table("system_sessions") + ` WHERE expires_at<=? ORDER BY expires_at,id LIMIT ?)`
	result, err := a.db.ExecContext(ctx, a.bind(query), nodeISOTime(now.UTC()), limit)
	if err != nil {
		return 0, fmt.Errorf("cleanup expired sessions: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read expired session cleanup count: %w", err)
	}
	return count, nil
}

func hashString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func randomToken(temporary bool) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	if temporary {
		return "juhe_tmp_" + token, nil
	}
	return token, nil
}

func randomID(prefix string, temporary bool) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	if temporary {
		return prefix + "-" + hex.EncodeToString(raw[:]), nil
	}
	return "sess-" + hex.EncodeToString(raw[:]), nil
}

func maxInt(value, minimum int) int {
	if value < minimum {
		return minimum
	}
	return value
}
