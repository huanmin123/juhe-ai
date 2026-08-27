// Package modelcheckauth verifies the existing management-session contract
// directly against the business database. It has no Node, IPC, or HTTP-client
// dependency and is shared by future Go-owned J3b management routes.
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
	SystemAccountID string
	Username        string
	DisplayName     string
	Role            string
	SessionID       string
}

type Authenticator struct {
	db   *sql.DB
	mode Mode
	now  func() time.Time
}

func New(db *sql.DB, mode Mode, now func() time.Time) (*Authenticator, error) {
	if db == nil || (mode != SQLite && mode != Postgres) {
		return nil, errors.New("invalid model check management authenticator")
	}
	if now == nil {
		now = time.Now
	}
	return &Authenticator{db: db, mode: mode, now: now}, nil
}

// CheckContract is read-only. The caller must run it before binding a public
// management listener; schema/grant drift therefore fails closed at startup.
func (a *Authenticator) CheckContract(ctx context.Context) error {
	if a == nil || a.db == nil {
		return errors.New("model check management authenticator is not initialized")
	}
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("open management auth contract transaction: %w", err)
	}
	defer tx.Rollback()
	for _, relation := range []string{a.table("system_sessions"), a.table("system_accounts")} {
		if _, err := tx.ExecContext(ctx, "SELECT 1 FROM "+relation+" LIMIT 0"); err != nil {
			return fmt.Errorf("verify management auth relation %s: %w", relation, err)
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

// Authenticate enforces the same bearer-versus-cookie precedence as Node's
// resolveSystemAccessToken. It returns every active session actor; callers
// decide whether the particular management route requires an administrator.
func (a *Authenticator) Authenticate(ctx context.Context, authorization, cookieHeader string) (Actor, error) {
	if a == nil || a.db == nil {
		return Actor{}, errors.New("model check management authenticator is not initialized")
	}
	token, err := resolveToken(authorization, cookieHeader)
	if err != nil {
		return Actor{}, err
	}
	return a.AuthenticateToken(ctx, token)
}

// AuthenticateToken verifies a token whose HTTP transport precedence has
// already been resolved by a compatible management adapter.
func (a *Authenticator) AuthenticateToken(ctx context.Context, token string) (Actor, error) {
	if a == nil || a.db == nil || strings.TrimSpace(token) == "" {
		return Actor{}, errors.New("model check management authenticator is not initialized")
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
	if now.Sub(lastSeen) >= time.Minute {
		if _, err := a.db.ExecContext(ctx, a.bind(`UPDATE `+a.table("system_sessions")+` SET last_seen_at=? WHERE id=? AND last_seen_at<?`), a.timeValue(now), actor.SessionID, a.timeValue(now.Add(-time.Minute))); err != nil {
			return Actor{}, fmt.Errorf("touch management session: %w", err)
		}
	}
	if mustChange {
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

func (a *Authenticator) RequireAdminToken(ctx context.Context, token string) (Actor, error) {
	actor, err := a.AuthenticateToken(ctx, token)
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
	token := cookieValue(cookieHeader, SessionCookieName)
	if strings.TrimSpace(token) == "" {
		return "", ErrLoginRequired
	}
	return token, nil
}

// cookieValue intentionally mirrors the Node parser: first matching cookie,
// split at the first equals sign, and percent decoding without '+' conversion.
func cookieValue(header, name string) string {
	result := ""
	for _, part := range strings.Split(header, ";") {
		rawName, rawValue, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found || rawName != name {
			continue
		}
		decoded, err := url.PathUnescape(rawValue)
		if err != nil {
			return ""
		}
		result = decoded
	}
	return result
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
func (a *Authenticator) timeValue(value time.Time) any {
	return nodeISOTime(value)
}
func parseTime(raw any) (time.Time, error) {
	switch value := raw.(type) {
	case time.Time:
		return value.UTC(), nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return time.Time{}, err
		}
		return parsed.UTC(), nil
	case []byte:
		return parseTime(string(value))
	default:
		return time.Time{}, errors.New("invalid session timestamp")
	}
}

func nodeISOTime(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}
