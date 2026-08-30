// Package modelcheckauth contains the Gateway-owned management session
// contract used by J3b. It talks directly to the Gateway-owned business
// store and has no Node, IPC, queue, or HTTP-client dependency.
package modelcheckauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Mode uint8

const (
	SQLite Mode = iota + 1
	Postgres
)

const SessionCookieName = "juhe_ai_session"

var (
	ErrInvalidToken   = errors.New("访问令牌无效或已过期")
	ErrLoginRequired  = errors.New("请先登录")
	ErrSessionExpired = errors.New("登录会话已过期")
	ErrMustChange     = errors.New("请先修改初始密码")
	ErrForbidden      = errors.New("需要管理员权限")
)

var temporaryToken = regexp.MustCompile(`^juhe_tmp_[A-Za-z0-9_-]{43}$`)

type Actor struct {
	SystemAccountID    string
	Username           string
	DisplayName        string
	Role               string
	SessionID          string
	MustChangePassword bool
}

type Authenticator struct {
	db   *sql.DB
	mode Mode
	now  func() time.Time
}

func New(db *sql.DB, mode Mode, now func() time.Time) (*Authenticator, error) {
	if db == nil || (mode != SQLite && mode != Postgres) {
		return nil, errors.New("invalid Gateway management authenticator")
	}
	if now == nil {
		now = time.Now
	}
	return &Authenticator{db: db, mode: mode, now: now}, nil
}

// CheckContract is read-only and must pass before binding a management
// listener. The session UPDATE privilege is checked explicitly on PostgreSQL.
func (a *Authenticator) CheckContract(ctx context.Context) error {
	if a == nil || a.db == nil {
		return errors.New("Gateway management authenticator is not initialized")
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("open management auth contract transaction: %w", err)
	}
	defer tx.Rollback()
	for _, relation := range []struct {
		name    string
		columns string
	}{
		{name: a.table("system_sessions"), columns: "id,system_account_id,token_hash,expires_at,created_at,last_seen_at"},
		{name: a.table("system_accounts"), columns: "id,username,display_name,status,role,must_change_password,password_hash,last_login_at,updated_at"},
	} {
		if _, err := tx.ExecContext(ctx, "SELECT "+relation.columns+" FROM "+relation.name+" LIMIT 0"); err != nil {
			return fmt.Errorf("verify management auth relation %s: %w", relation.name, err)
		}
	}
	if a.mode == Postgres {
		var permitted bool
		if err := tx.QueryRowContext(ctx, `SELECT has_table_privilege(current_user, 'juhe_business.system_sessions', 'UPDATE')`).Scan(&permitted); err != nil {
			return fmt.Errorf("read management session update privilege: %w", err)
		}
		if !permitted {
			return errors.New("management role lacks system session touch privilege")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit management auth contract transaction: %w", err)
	}
	return nil
}

func (a *Authenticator) Authenticate(ctx context.Context, authorization, cookieHeader string) (Actor, error) {
	if a == nil || a.db == nil {
		return Actor{}, errors.New("Gateway management authenticator is not initialized")
	}
	token, err := resolveToken(authorization, cookieHeader)
	if err != nil {
		return Actor{}, err
	}
	return a.AuthenticateToken(ctx, token)
}

func (a *Authenticator) AuthenticateToken(ctx context.Context, token string) (Actor, error) {
	return a.authenticateToken(ctx, token, true, true)
}

// AuthenticateTokenForSession returns the active account even when the
// account still has to change its initial password. The Node /auth/me
// contract exposes that state so the client can reach change-password.
func (a *Authenticator) AuthenticateTokenForSession(ctx context.Context, token string) (Actor, error) {
	return a.authenticateToken(ctx, token, false, true)
}

// AuthenticateTokenForSessionNoTouch is for read-only management endpoints
// such as /auth/me, matching Node's read access mode.
func (a *Authenticator) AuthenticateTokenForSessionNoTouch(ctx context.Context, token string) (Actor, error) {
	return a.authenticateToken(ctx, token, false, false)
}

func (a *Authenticator) authenticateToken(ctx context.Context, token string, rejectMustChange, touch bool) (Actor, error) {
	if a == nil || a.db == nil || strings.TrimSpace(token) == "" {
		return Actor{}, errors.New("Gateway management authenticator is not initialized")
	}
	sum := sha256.Sum256([]byte(token))
	var actor Actor
	var expiresRaw, seenRaw any
	var mustChange bool
	query := `SELECT ss.id,ss.expires_at,ss.last_seen_at,sa.id,sa.username,COALESCE(sa.display_name,''),sa.role,sa.must_change_password FROM ` + a.table("system_sessions") + ` ss INNER JOIN ` + a.table("system_accounts") + ` sa ON sa.id=ss.system_account_id WHERE ss.token_hash=? AND sa.status='active'`
	if err := a.db.QueryRowContext(ctx, a.bind(query), hex.EncodeToString(sum[:])).Scan(&actor.SessionID, &expiresRaw, &seenRaw, &actor.SystemAccountID, &actor.Username, &actor.DisplayName, &actor.Role, &mustChange); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Actor{}, ErrSessionExpired
		}
		return Actor{}, fmt.Errorf("read management session: %w", err)
	}
	now := a.now().UTC()
	expiresAt, err := parseTime(expiresRaw)
	if err != nil || !expiresAt.After(now) {
		return Actor{}, ErrSessionExpired
	}
	lastSeen, err := parseTime(seenRaw)
	if err != nil {
		return Actor{}, ErrSessionExpired
	}
	if touch && now.Sub(lastSeen) >= time.Minute {
		if _, err := a.db.ExecContext(ctx, a.bind(`UPDATE `+a.table("system_sessions")+` SET last_seen_at=? WHERE id=? AND last_seen_at<?`), nodeISOTime(now), actor.SessionID, nodeISOTime(now.Add(-time.Minute))); err != nil {
			return Actor{}, fmt.Errorf("touch management session: %w", err)
		}
	}
	actor.MustChangePassword = mustChange
	if rejectMustChange && mustChange {
		return Actor{}, ErrMustChange
	}
	return actor, nil
}

func (a *Authenticator) RequireAdmin(ctx context.Context, authorization, cookieHeader string) (Actor, error) {
	actor, err := a.Authenticate(ctx, authorization, cookieHeader)
	if err != nil {
		return Actor{}, err
	}
	if actor.Role != "admin" && actor.Role != "super_admin" {
		return Actor{}, ErrForbidden
	}
	return actor, nil
}

func resolveToken(authorization, cookieHeader string) (string, error) {
	if authorization != "" {
		matched := regexp.MustCompile(`(?i)^Bearer\s+(.+)$`).FindStringSubmatch(strings.TrimSpace(authorization))
		if len(matched) != 2 || !temporaryToken.MatchString(matched[1]) {
			return "", ErrInvalidToken
		}
		return matched[1], nil
	}
	for _, part := range strings.Split(cookieHeader, ";") {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found || name != SessionCookieName {
			continue
		}
		decoded, err := url.PathUnescape(value)
		if err != nil || decoded == "" {
			return "", ErrLoginRequired
		}
		return decoded, nil
	}
	return "", ErrLoginRequired
}

func (a *Authenticator) table(name string) string {
	if a.mode == Postgres {
		return "juhe_business." + name
	}
	return name
}

func (a *Authenticator) bind(query string) string {
	if a.mode != Postgres {
		return query
	}
	for index := 1; strings.Contains(query, "?"); index++ {
		query = strings.Replace(query, "?", fmt.Sprintf("$%d", index), 1)
	}
	return query
}

func parseTime(raw any) (time.Time, error) {
	switch value := raw.(type) {
	case time.Time:
		return value.UTC(), nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, value)
		return parsed.UTC(), err
	case []byte:
		return parseTime(string(value))
	default:
		return time.Time{}, errors.New("invalid session timestamp")
	}
}

func nodeISOTime(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}
