// Delegated-local SQL reads/writes over the shared business database:
// the my-chat user profile (system_accounts), the providers precheck for
// group creation, and the authorization-instance source filter for AI
// accounts. These close the contract gaps the P03 tests surfaced (Node
// findSystemAccountByIdAsync / updateSystemAccountAsync in
// storage/system-accounts.repository.ts, findProviderOptionByCodeAsync in
// storage/providers.repository.ts and isOwnedPhysicalAccount's
// authorizationInstanceSourceAccountId probe). Dual-mode SQLite + PostgreSQL
// following the routestrategies/apikeys store style.
package delegated

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// isoMillis mirrors Node toISOString() millisecond precision, the shared
// store timestamp format.
func isoMillis(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000") + "Z"
}

// bind rewrites positional placeholders for PostgreSQL ($1, $2, ...).
func (d *Deps) bind(query string) string {
	if !d.PGDialect {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// table qualifies a business table for PostgreSQL.
func (d *Deps) table(name string) string {
	if d.PGDialect {
		return "juhe_business." + name
	}
	return name
}

func (d *Deps) nowISO() string { return isoMillis(d.clock()) }

// profile mirrors the SystemAccountSummary subset the delegated profile and
// request-limits routes consume (username/displayName + the request-limit
// override JSON for resolveEffectiveUserRequestLimits).
type profile struct {
	ID                string
	Username          string
	DisplayName       string
	RequestLimitsJSON sql.NullString
}

const profileColumns = "id, username, display_name, request_limits_json"

func scanProfileRow(scan func(...any) error) (*profile, error) {
	row := profile{}
	if err := scan(&row.ID, &row.Username, &row.DisplayName, &row.RequestLimitsJSON); err != nil {
		return nil, err
	}
	return &row, nil
}

// findProfileByID mirrors findSystemAccountByIdWithClient (no lock): nil when
// the caller vanished (route renders 404 用户不存在).
func (d *Deps) findProfileByID(ctx context.Context, id string) (*profile, error) {
	if d.DB == nil {
		return nil, nil
	}
	ctx = ensureDelegatedCtx(ctx)
	row, err := scanProfileRow(func(dst ...any) error {
		return d.DB.QueryRowContext(ctx, d.bind(`SELECT `+profileColumns+`
			FROM `+d.table("system_accounts")+` WHERE id = ? LIMIT 1`), id).Scan(dst...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// profileDisplayNameError mirrors normalizeRequiredText(value, '用户名称')
// (system-accounts.repository.ts) for the already zod-trimmed value: blank
// input is rejected by the route schema, only interior whitespace survives.
func profileDisplayNameError(displayName string) error {
	if displayName == "" {
		return errors.New("用户名称不能为空")
	}
	if hasWhitespace(displayName) {
		return errors.New("用户名称不能包含空格")
	}
	return nil
}

// updateProfileDisplayName mirrors updateSystemAccountWithPasswordHashAsync
// restricted to the delegated displayName mutation: existence first (nil →
// 404), then normalizeRequiredText, then the case-insensitive display-name
// uniqueness probe (用户名称已存在), then the two-column UPDATE. Returns the
// updated summary.
func (d *Deps) updateProfileDisplayName(ctx context.Context, id, displayName string) (*profile, error) {
	if d.DB == nil {
		return nil, nil
	}
	ctx = ensureDelegatedCtx(ctx)
	if err := profileDisplayNameError(displayName); err != nil {
		return nil, err
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := scanProfileRow(func(dst ...any) error {
		lock := ""
		if d.PGDialect {
			lock = " FOR UPDATE"
		}
		return tx.QueryRowContext(ctx, d.bind(`SELECT `+profileColumns+`
			FROM `+d.table("system_accounts")+` WHERE id = ? LIMIT 1`+lock), id).Scan(dst...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, d.bind(`SELECT id FROM `+d.table("system_accounts")+`
		WHERE lower(display_name) = lower(?) AND id <> ? LIMIT 1`), displayName, id).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if existing != "" {
		return nil, errors.New("用户名称已存在")
	}
	now := d.nowISO()
	if _, err := tx.ExecContext(ctx, d.bind(`UPDATE `+d.table("system_accounts")+`
		SET display_name = ?, updated_at = ? WHERE id = ?`), displayName, now, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &profile{ID: current.ID, Username: current.Username, DisplayName: displayName, RequestLimitsJSON: current.RequestLimitsJSON}, nil
}

// inheritedSourceAccountIDs reports which of the given account ids are
// authorization instances inherited from another physical account
// (authorization_instance_source_account_id non-null). Node listAccountItems
// surfaces this column and isOwnedPhysicalAccount drops such rows.
func (d *Deps) inheritedSourceAccountIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	inherited := map[string]bool{}
	if d.DB == nil || len(ids) == 0 {
		return inherited, nil
	}
	ctx = ensureDelegatedCtx(ctx)
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := d.DB.QueryContext(ctx, d.bind(`SELECT id FROM `+d.table("accounts")+`
		WHERE id IN (`+strings.Join(placeholders, ",")+`)
		AND authorization_instance_source_account_id IS NOT NULL`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		inherited[id] = true
	}
	return inherited, rows.Err()
}

// providerEnabled mirrors the delegated createGroup precheck
// (findProviderOptionByCodeAsync + !provider.enabled → 400
// 供应商不存在或已停用): false when the code is unknown or disabled.
func (d *Deps) providerEnabled(ctx context.Context, code string) (bool, error) {
	if d.DB == nil {
		return true, nil
	}
	ctx = ensureDelegatedCtx(ctx)
	var enabled int
	err := d.DB.QueryRowContext(ctx, d.bind(`SELECT enabled FROM `+d.table("providers")+`
		WHERE code = ? LIMIT 1`), code).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled == 1, nil
}

func ensureDelegatedCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
