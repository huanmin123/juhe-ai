package accountbalance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PostgresDirectInputReader reads a repeatable, read-only snapshot of the
// business facts. It never writes juhe_business or opens Node SQLite.
type PostgresDirectInputReader struct {
	db     *sql.DB
	secret string
	ttl    time.Duration
	now    func() time.Time
}

func NewPostgresDirectInputReader(db *sql.DB, secret string, ttl time.Duration, now func() time.Time) (*PostgresDirectInputReader, error) {
	if db == nil || strings.TrimSpace(secret) == "" || ttl <= 0 || ttl > 15*time.Minute {
		return nil, errors.New("J2 PG direct input 配置无效")
	}
	if now == nil {
		now = time.Now
	}
	return &PostgresDirectInputReader{db: db, secret: secret, ttl: ttl, now: now}, nil
}

func (r *PostgresDirectInputReader) CheckContract(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("J2 PG direct input 开启只读事务失败: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL TRANSACTION READ ONLY"); err != nil {
		return err
	}
	for _, relation := range []string{"juhe_business.accounts", "juhe_business.proxy_profiles"} {
		if _, err := tx.ExecContext(ctx, "SELECT 1 FROM "+relation+" LIMIT 0"); err != nil {
			return fmt.Errorf("J2 PG direct input 缺少 %s: %w", relation, err)
		}
	}
	rows, err := tx.QueryContext(ctx, j2CandidateSQL(candidateReadFirstProbe), r.now().UTC(), 1)
	if err != nil {
		return fmt.Errorf("J2 PG direct input 候选契约预检失败: %w", err)
	}
	_ = rows.Close()
	return tx.Commit()
}

func (r *PostgresDirectInputReader) LoadDue(ctx context.Context, limit int) ([]Candidate, error) {
	return r.load(ctx, limit, candidateReadDue, false)
}

func (r *PostgresDirectInputReader) LoadFirstProbe(ctx context.Context, limit int) ([]Candidate, error) {
	return r.load(ctx, limit, candidateReadFirstProbe, false)
}

// LoadRecovery returns enabled automatic-refresh candidates that lost their
// next_refresh_at schedule. Node treats these as durable recovery work rather
// than leaving them stranded forever after an interrupted write.
func (r *PostgresDirectInputReader) LoadRecovery(ctx context.Context, limit int) ([]Candidate, error) {
	return r.load(ctx, limit, candidateReadRecovery, false)
}

func (r *PostgresDirectInputReader) LoadAccount(ctx context.Context, accountID string) (Candidate, error) {
	if strings.TrimSpace(accountID) == "" {
		return Candidate{}, errors.New("J2 account ID 不能为空")
	}
	rows, err := r.load(ctx, 1, candidateReadDue, true, accountID)
	if err != nil {
		return Candidate{}, err
	}
	if len(rows) != 1 {
		return Candidate{}, sql.ErrNoRows
	}
	return rows[0], nil
}

type candidateReadKind uint8

const (
	candidateReadDue candidateReadKind = iota
	candidateReadFirstProbe
	candidateReadRecovery
)

func (r *PostgresDirectInputReader) load(ctx context.Context, limit int, kind candidateReadKind, byID bool, ids ...string) ([]Candidate, error) {
	if limit < 1 || limit > 1024 {
		return nil, errors.New("J2 direct input limit 必须在 1..1024")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL TRANSACTION READ ONLY"); err != nil {
		return nil, err
	}
	now := r.now().UTC()
	query := j2CandidateSQL(kind)
	// Keep scanning past malformed rows. The legacy Node reader scans bounded
	// pages, so a bad credential/config/proxy cannot starve later due work.
	scanLimit := limit * 4
	if scanLimit < 40 {
		scanLimit = 40
	}
	if scanLimit > 1024 {
		scanLimit = 1024
	}
	args := []any{now, scanLimit}
	if byID {
		query = j2CandidateByIDSQL(kind)
		args = []any{now, ids[0]}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取 J2 PG 候选失败: %w", err)
	}
	defer rows.Close()
	result := make([]Candidate, 0, limit)
	for rows.Next() {
		candidate, err := r.scanCandidate(rows, now)
		if err != nil {
			// Keep a malformed account from starving every later due row.  The
			// rejection is observable and never converted into a direct request.
			slog.Default().Warn("J2 跳过无效 PG 候选", "error", err)
			continue
		}
		result = append(result, candidate)
		if len(result) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *PostgresDirectInputReader) scanCandidate(rows *sql.Rows, now time.Time) (Candidate, error) {
	var id, systemID, provider, typ, status, credentials, configJSON sql.NullString
	var revision, dispatch int64
	var schedulable, enabled sql.NullInt64
	var next sql.NullString
	var proxyRequired, proxyID, proxyType, proxyHost, proxyUser, proxyPassword sql.NullString
	var proxyPort sql.NullInt64
	var proxyEnabled sql.NullBool
	if err := rows.Scan(&id, &systemID, &revision, &dispatch, &provider, &typ, &status, &schedulable, &enabled, &configJSON, &next, &credentials, &proxyRequired, &proxyID, &proxyType, &proxyHost, &proxyPort, &proxyUser, &proxyPassword, &proxyEnabled); err != nil {
		return Candidate{}, fmt.Errorf("解码 J2 PG 候选失败: %w", err)
	}
	if !id.Valid || !systemID.Valid || !credentials.Valid {
		return Candidate{}, errors.New("J2 PG 候选缺少账户、system account 或凭据")
	}
	var credential map[string]any
	plain, err := DecryptV1Envelope(r.secret, credentials.String)
	if err != nil {
		return Candidate{}, fmt.Errorf("J2 account=%s 凭据解封失败: %w", id.String, err)
	}
	if err := json.Unmarshal(plain, &credential); err != nil {
		return Candidate{}, fmt.Errorf("J2 account=%s 凭据 JSON 无效: %w", id.String, err)
	}
	keys := EffectiveAPIKeys(credential)
	if len(keys) != 1 {
		return Candidate{}, fmt.Errorf("J2 account=%s 必须恰好一个 API Key", id.String)
	}
	baseURL, ok := credential["base_url"].(string)
	if !ok || strings.TrimSpace(baseURL) == "" {
		return Candidate{}, fmt.Errorf("J2 account=%s 缺少 Base URL", id.String)
	}
	var config QueryConfig
	if enabled.Valid && enabled.Int64 == 1 {
		var raw map[string]any
		if err := json.Unmarshal([]byte(configJSON.String), &raw); err != nil {
			return Candidate{}, fmt.Errorf("J2 account=%s 余额配置 JSON 无效: %w", id.String, err)
		}
		config, err = NormalizeConfig(raw)
		if err != nil {
			return Candidate{}, fmt.Errorf("J2 account=%s 余额配置无效: %w", id.String, err)
		}
	} else {
		config = QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5}
	}
	candidate := Candidate{AccountID: id.String, SystemAccountID: systemID.String, InputVersion: dispatch, ConfigRevision: revision, Provider: provider.String, Type: typ.String, Status: status.String, Schedulable: schedulable.Valid && schedulable.Int64 == 1, BalanceEnabled: enabled.Valid && enabled.Int64 == 1, FirstProbe: !enabled.Valid || enabled.Int64 == 0, Recovery: !next.Valid && enabled.Valid && enabled.Int64 == 1, APIKeyCount: len(keys), APIKey: CredentialEnvelope{Kind: "api_key", Ciphertext: credentials.String}, BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), Config: config, IssuedAt: now, ExpiresAt: now.Add(r.ttl)}
	if next.Valid {
		value, err := time.Parse(time.RFC3339Nano, next.String)
		if err != nil {
			return Candidate{}, fmt.Errorf("J2 account=%s next_refresh_at 非 RFC3339: %w", id.String, err)
		}
		value = value.UTC()
		candidate.NextRefreshAt = &value
	}
	if proxyRequired.Valid && (!proxyID.Valid || !proxyEnabled.Valid || !proxyEnabled.Bool) {
		return Candidate{}, fmt.Errorf("J2 account=%s 代理配置不存在或已禁用", id.String)
	}
	if proxyID.Valid && proxyEnabled.Valid && proxyEnabled.Bool {
		proxy, err := r.makeProxy(proxyType.String, proxyHost.String, proxyPort.Int64, proxyUser.String, proxyPassword.String)
		if err != nil {
			return Candidate{}, fmt.Errorf("J2 account=%s 代理无效: %w", id.String, err)
		}
		candidate.Proxy = proxy
	}
	return candidate, nil
}

func (r *PostgresDirectInputReader) makeProxy(kind, host string, port int64, user, encryptedPassword string) (*CredentialEnvelope, error) {
	// Node stores legacy socks5 profiles but resolves them through the
	// socks5h path. Keep direct PG readers on the same remote-DNS contract.
	if kind == "socks5" {
		kind = "socks5h"
	}
	if kind != "http" && kind != "https" && kind != "socks5" && kind != "socks5h" {
		return nil, fmt.Errorf("不支持的 proxy 类型=%s", kind)
	}
	if strings.TrimSpace(host) == "" || port < 1 || port > 65535 {
		return nil, errors.New("代理地址无效")
	}
	password := ""
	if encryptedPassword != "" {
		plain, err := DecryptV1Envelope(r.secret, encryptedPassword)
		if err != nil {
			return nil, err
		}
		var value map[string]any
		if err := json.Unmarshal(plain, &value); err != nil {
			return nil, err
		}
		if text, ok := value["password"].(string); ok {
			password = text
		}
	}
	u := &url.URL{Scheme: kind, Host: host + ":" + strconv.FormatInt(port, 10)}
	if user != "" {
		u.User = url.UserPassword(user, password)
	}
	ciphertext, err := NewCredentialEnvelope(r.secret, "proxy_url", map[string]string{"url": u.String()})
	if err != nil {
		return nil, err
	}
	return &ciphertext, nil
}

func j2CandidateSQL(kind candidateReadKind) string {
	predicate := "a.balance_query_enabled = 1 AND a.balance_query_next_refresh_at IS NOT NULL AND a.balance_query_next_refresh_at::timestamptz <= $1"
	switch kind {
	case candidateReadFirstProbe:
		predicate = "a.balance_query_enabled = 0 AND a.balance_query_config_json = '{}' AND a.balance_query_next_refresh_at IS NOT NULL AND a.balance_query_next_refresh_at::timestamptz <= $1"
	case candidateReadRecovery:
		// Keep the prepared-query parameter shape stable while ensuring the
		// recovery scan remains independent of wall-clock ordering.
		predicate = "a.balance_query_enabled = 1 AND a.balance_query_next_refresh_at IS NULL AND $1::timestamptz IS NOT NULL"
	}
	orderBy := "a.balance_query_next_refresh_at ASC,a.id ASC"
	if kind == candidateReadRecovery {
		orderBy = "a.id ASC"
	}
	return `SELECT a.id,a.system_account_id,a.config_revision,a.dispatch_revision,a.provider_code,a.type,a.status,a.schedulable,a.balance_query_enabled,a.balance_query_config_json,a.balance_query_next_refresh_at,a.credentials_encrypted,a.proxy_profile_id,p.id,p.type,p.host,p.port,p.username,p.password_encrypted,p.enabled FROM juhe_business.accounts a LEFT JOIN juhe_business.proxy_profiles p ON p.id=a.proxy_profile_id WHERE a.deleted_at IS NULL AND a.authorization_instance_authorization_id IS NULL AND a.type='api_key' AND a.status='active' AND a.schedulable=1 AND ` + predicate + ` ORDER BY ` + orderBy + ` LIMIT $2`
}

func j2CandidateByIDSQL(kind candidateReadKind) string {
	query := j2CandidateSQL(kind)
	if kind == candidateReadDue {
		query = strings.Replace(query, "a.balance_query_enabled = 1 AND a.balance_query_next_refresh_at IS NOT NULL AND a.balance_query_next_refresh_at::timestamptz <= $1", "a.balance_query_enabled = 1", 1)
	}
	query = strings.Replace(query, " ORDER BY ", " AND a.id=$2 ORDER BY ", 1)
	return strings.Replace(query, " LIMIT $2", " LIMIT 1", 1)
}
