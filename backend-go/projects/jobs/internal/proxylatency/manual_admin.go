package proxylatency

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckauth"
	"github.com/huanminabc/juhe-ai/backend-go-platform/operationlogappend"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
)

const manualAdminProxyTestPathPrefix = "/__aisys__/api/proxies/"
const manualAdminProxyTestPathSuffix = "/test"
const manualAdminSessionCookie = "juhe_ai_session"

var temporaryAccessTokenPattern = regexp.MustCompile(`^juhe_tmp_[A-Za-z0-9_-]{43}$`)

var (
	ErrManualAdminInvalidToken   = modelcheckauth.ErrInvalidToken
	ErrManualAdminLoginRequired  = modelcheckauth.ErrLoginRequired
	ErrManualAdminSessionExpired = modelcheckauth.ErrSessionExpired
	ErrManualAdminMustChange     = modelcheckauth.ErrMustChange
	ErrManualAdminForbidden      = modelcheckauth.ErrForbidden
	ErrManualAdminProxyMissing   = errors.New("代理不存在")
)

// ManualAdminConfig is deliberately a separate opt-in from the loopback
// health listener. An external management path is never exposed merely by
// starting the jobs binary.
type ManualAdminConfig struct {
	Enabled         bool
	ListenAddress   string
	PostgresURL     string
	MaxOpenConns    int
	MaxIdleConns    int
	RequestDeadline time.Duration
}

func LoadManualAdminConfig(getenv func(string) string) (ManualAdminConfig, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := ManualAdminConfig{Enabled: strings.EqualFold(strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_MANAGEMENT_ENABLED")), "true")}
	if !cfg.Enabled {
		return cfg, nil
	}
	cfg.ListenAddress = strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS"))
	if _, port, err := net.SplitHostPort(cfg.ListenAddress); err != nil || strings.TrimSpace(port) == "" {
		return ManualAdminConfig{}, errors.New("JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS 必须是 host:port")
	} else if portNumber, numberErr := strconv.Atoi(port); numberErr != nil || portNumber < 1 || portNumber > 65535 {
		return ManualAdminConfig{}, errors.New("JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS 端口必须在 1..65535")
	}
	cfg.PostgresURL = strings.TrimSpace(getenv("JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL"))
	if cfg.PostgresURL == "" {
		return ManualAdminConfig{}, errors.New("启用 J3a 管理接口时缺少 JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_URL")
	}
	var err error
	if cfg.MaxOpenConns, err = positiveInt(getenv, "JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_MAX_OPEN_CONNS", 5096); err != nil {
		return ManualAdminConfig{}, err
	}
	if cfg.MaxIdleConns, err = positiveInt(getenv, "JUHE_AI_PROXY_LATENCY_MANAGEMENT_POSTGRES_MAX_IDLE_CONNS", sqlpool.MaxIdleConns); err != nil {
		return ManualAdminConfig{}, err
	}
	if err := sqlpool.ValidatePoolLimits(cfg.MaxOpenConns, cfg.MaxIdleConns); err != nil {
		return ManualAdminConfig{}, fmt.Errorf("J3a 管理 PostgreSQL 连接池配置无效: %w", err)
	}
	if cfg.RequestDeadline, err = runtimeDuration(getenv, "JUHE_AI_PROXY_LATENCY_MANAGEMENT_DEADLINE", 25*time.Second, time.Second, 25*time.Second); err != nil {
		return ManualAdminConfig{}, err
	}
	return cfg, nil
}

// ManualAdminActor is the authenticated system account whose audit identity
// is carried to F4. No credential token is retained after Authenticate.
type ManualAdminActor struct {
	SystemAccountID string
	Username        string
	DisplayName     string
	Role            string
}

type manualAdminSnapshot struct {
	Request ManualRequest
	before  manualAdminTestState
}

type manualAdminTestState struct {
	status         string
	latencyMS      *int64
	outboundIP     *string
	outboundRegion *string
	message        *string
	testedAt       *string
}

// ManualAdminRunner executes the already-authenticated direct request in the
// same jobs process.
type ManualAdminRunner interface {
	RunManual(context.Context, ManualRequest) (ProxyTestReport, error)
}

// ManualAdminSource supplies authentication and a coherent proxy snapshot.
type ManualAdminSource interface {
	Authenticate(context.Context, string, *http.Cookie) (ManualAdminActor, error)
	LoadSnapshot(context.Context, string, time.Duration) (manualAdminSnapshot, error)
	Exists(context.Context, string) (bool, error)
}

// ManualAdminAuditAppender appends a completed management action to F4's
// durable audit schema. Failure is observed but never retroactively changes a
// completed proxy probe response, matching the prior Node audit semantics.
type ManualAdminAuditAppender interface {
	Append(context.Context, operationlogappend.Input) error
}

type postgresManualAdminAuditAppender struct {
	db *sql.DB
}

// NewPostgresManualAdminAuditAppender creates the append-only F4 producer for
// the J3a endpoint. It does not acquire the F4 owner lease because it neither
// runs F4's listener nor its retention worker.
func NewPostgresManualAdminAuditAppender(db *sql.DB) ManualAdminAuditAppender {
	return postgresManualAdminAuditAppender{db: db}
}

func (a postgresManualAdminAuditAppender) Append(ctx context.Context, input operationlogappend.Input) error {
	return operationlogappend.AppendPostgres(ctx, a.db, input)
}

// PostgresManualAdminSource owns only the direct, bounded business reads and
// auth session touch required by the J3a management endpoint. It never opens
// Node storage or calls another process.
type PostgresManualAdminSource struct {
	db   *sql.DB
	auth *modelcheckauth.Authenticator
}

func NewPostgresManualAdminSource(db *sql.DB, now func() time.Time) (*PostgresManualAdminSource, error) {
	if db == nil {
		return nil, errors.New("J3a management PostgreSQL database is required")
	}
	auth, err := modelcheckauth.New(db, modelcheckauth.Postgres, now)
	if err != nil {
		return nil, err
	}
	return &PostgresManualAdminSource{db: db, auth: auth}, nil
}

// CheckContract verifies the least-privilege schema and grants before the
// public listener is bound. It does not repair schemas or make writes.
func (s *PostgresManualAdminSource) CheckContract(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("J3a management source 未初始化")
	}
	if err := s.auth.CheckContract(ctx); err != nil {
		return fmt.Errorf("验证 J3a management 会话认证契约失败: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("开始 J3a management contract 事务失败: %w", err)
	}
	defer tx.Rollback()
	for _, relation := range []string{
		"juhe_business.system_sessions", "juhe_business.system_accounts", "juhe_business.proxy_profiles",
		"juhe_business.providers", "juhe_business.provider_protocol_profiles",
	} {
		if _, err := tx.ExecContext(ctx, "SELECT 1 FROM "+relation+" LIMIT 0"); err != nil {
			return fmt.Errorf("J3a management 缺少关系读取权限 %s: %w", relation, err)
		}
	}
	rows, err := tx.QueryContext(ctx, manualAdminSnapshotSQL, "__j3a_management_contract_probe__")
	if err != nil {
		return fmt.Errorf("验证 J3a management proxy 快照查询失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭 J3a management proxy 快照契约游标失败: %w", err)
	}
	for _, statement := range manualAdminAuditContractSQL {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("验证 J3a management F4 审计表契约失败: %w", err)
		}
	}
	var granted bool
	err = tx.QueryRowContext(ctx, `
SELECT has_table_privilege(current_user, 'juhe_business.system_sessions', 'UPDATE')
   AND has_table_privilege(current_user, 'juhe_dataset.operation_logs', 'INSERT')
   AND has_table_privilege(current_user, 'juhe_dataset.operation_log_targets', 'INSERT')
   AND has_table_privilege(current_user, 'juhe_dataset.operation_log_viewers', 'INSERT')
   AND has_table_privilege(current_user, 'juhe_dataset.operation_log_summary_search_terms', 'INSERT')`).Scan(&granted)
	if err != nil {
		return fmt.Errorf("读取 J3a management 写权限失败: %w", err)
	}
	if !granted {
		return errors.New("J3a management PostgreSQL 角色缺少会话 touch 或 F4 审计追加权限")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 J3a management contract 事务失败: %w", err)
	}
	return nil
}

func (s *PostgresManualAdminSource) Authenticate(ctx context.Context, authorization string, cookie *http.Cookie) (ManualAdminActor, error) {
	token, err := resolveManualAdminToken(authorization, cookie)
	if err != nil {
		return ManualAdminActor{}, err
	}
	actor, err := s.auth.RequireAdminToken(ctx, token)
	if err != nil {
		return ManualAdminActor{}, err
	}
	return ManualAdminActor{SystemAccountID: actor.SystemAccountID, Username: actor.Username, DisplayName: actor.DisplayName, Role: actor.Role}, nil
}

func resolveManualAdminToken(authorization string, cookie *http.Cookie) (string, error) {
	if authorization != "" {
		matched := regexp.MustCompile(`(?i)^Bearer\s+(.+)$`).FindStringSubmatch(strings.TrimSpace(authorization))
		if len(matched) != 2 || !temporaryAccessTokenPattern.MatchString(matched[1]) {
			return "", ErrManualAdminInvalidToken
		}
		return matched[1], nil
	}
	if cookie == nil || strings.TrimSpace(cookie.Value) == "" {
		return "", ErrManualAdminLoginRequired
	}
	return cookie.Value, nil
}

func (s *PostgresManualAdminSource) LoadSnapshot(ctx context.Context, proxyID string, deadline time.Duration) (manualAdminSnapshot, error) {
	proxyID = strings.TrimSpace(proxyID)
	if proxyID == "" {
		return manualAdminSnapshot{}, ErrManualAdminProxyMissing
	}
	rows, err := s.db.QueryContext(ctx, manualAdminSnapshotSQL, proxyID)
	if err != nil {
		return manualAdminSnapshot{}, fmt.Errorf("读取 J3a management proxy 快照失败: %w", err)
	}
	defer rows.Close()
	var snapshot *manualAdminSnapshot
	seen := map[string]bool{}
	for rows.Next() {
		row, err := scanManualAdminSnapshotRow(rows)
		if err != nil {
			return manualAdminSnapshot{}, err
		}
		if snapshot == nil {
			request := ManualRequest{
				SchemaVersion: 1, ProxyID: row.proxyID, ProxyName: row.proxyName, ConfigRevision: row.configRevision,
				ProxyType: row.proxyType, ProxyHost: row.proxyHost, ProxyPort: int(row.proxyPort), ProxyUsername: row.proxyUsername,
				DeadlineMS: int(deadline / time.Millisecond),
			}
			if row.passwordEncrypted != "" {
				if !validProxyLatencyEnvelope(row.passwordEncrypted) {
					return manualAdminSnapshot{}, errors.New("J3a management proxy password envelope 无效")
				}
				request.ProxyPassword = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: row.passwordEncrypted}
			}
			snapshot = &manualAdminSnapshot{Request: request, before: row.before}
		}
		if row.provider == "" && row.profileID == "" && row.targetURL == "" && row.providerName == "" {
			continue
		}
		if row.provider == "" || row.profileID == "" || row.providerName == "" {
			return manualAdminSnapshot{}, errors.New("J3a management provider target 标识无效")
		}
		if seen[row.provider] {
			return manualAdminSnapshot{}, errors.New("J3a management provider target 重复")
		}
		seen[row.provider] = true
		snapshot.Request.Targets = append(snapshot.Request.Targets, ManualTarget{Provider: row.provider, ProfileID: row.profileID, Name: row.providerName, URL: row.targetURL})
	}
	if err := rows.Err(); err != nil {
		return manualAdminSnapshot{}, fmt.Errorf("遍历 J3a management proxy 快照失败: %w", err)
	}
	if snapshot == nil {
		return manualAdminSnapshot{}, ErrManualAdminProxyMissing
	}
	return *snapshot, nil
}

func (s *PostgresManualAdminSource) Exists(ctx context.Context, proxyID string) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM juhe_business.proxy_profiles WHERE id=$1)`, proxyID).Scan(&exists); err != nil {
		return false, fmt.Errorf("确认 J3a management proxy 存在失败: %w", err)
	}
	return exists, nil
}

type manualAdminSnapshotRow struct {
	proxyID, proxyName, proxyType, proxyHost, proxyUsername, passwordEncrypted, configRevision string
	proxyPort                                                                                  int64
	provider, providerName, profileID, targetURL                                               string
	before                                                                                     manualAdminTestState
}

func scanManualAdminSnapshotRow(rows *sql.Rows) (manualAdminSnapshotRow, error) {
	var row manualAdminSnapshotRow
	var latency sql.NullInt64
	var outboundIP, outboundRegion, message, testedAt sql.NullString
	var provider, providerName, profileID, targetURL sql.NullString
	if err := rows.Scan(&row.proxyID, &row.proxyName, &row.proxyType, &row.proxyHost, &row.proxyPort, &row.proxyUsername, &row.passwordEncrypted, &row.configRevision, &row.before.status, &latency, &outboundIP, &outboundRegion, &message, &testedAt, &provider, &providerName, &profileID, &targetURL); err != nil {
		return manualAdminSnapshotRow{}, fmt.Errorf("解码 J3a management proxy 快照失败: %w", err)
	}
	if latency.Valid {
		value := latency.Int64
		row.before.latencyMS = &value
	}
	for _, field := range []struct {
		source sql.NullString
		target **string
	}{{outboundIP, &row.before.outboundIP}, {outboundRegion, &row.before.outboundRegion}, {message, &row.before.message}, {testedAt, &row.before.testedAt}} {
		if field.source.Valid {
			value := field.source.String
			*field.target = &value
		}
	}
	if provider.Valid {
		row.provider = provider.String
	}
	if providerName.Valid {
		row.providerName = providerName.String
	}
	if profileID.Valid {
		row.profileID = profileID.String
	}
	if targetURL.Valid {
		row.targetURL = targetURL.String
	}
	return row, nil
}

const manualAdminSnapshotSQL = `
WITH selected_targets AS (
  SELECT provider,provider_name,profile_id,target_url
  FROM (
    SELECT p.code AS provider,p.name AS provider_name,ppp.id AS profile_id,ppp.base_url AS target_url,
      row_number() OVER (
        PARTITION BY p.code
        ORDER BY CASE WHEN ppp.enabled = 1 THEN 0 ELSE 1 END,
          CASE WHEN (p.code = 'gemini' AND ppp.id = 'profile_gemini_native_v1beta')
                    OR (p.code = 'glm' AND ppp.id = 'profile_glm_coding_openai_v1') THEN 0 ELSE 1 END,
          ppp.updated_at DESC,ppp.id ASC
      ) AS profile_rank
    FROM juhe_business.providers p
    JOIN juhe_business.provider_protocol_profiles ppp ON ppp.provider_code = p.code
    WHERE p.enabled = 1
  ) ranked_profiles
  WHERE profile_rank = 1
)
SELECT p.id,p.name,p.type,p.host,p.port,COALESCE(p.username,''),COALESCE(p.password_encrypted,''),
  to_char(p.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS config_revision,
  p.test_status,p.latency_ms,p.outbound_ip,p.outbound_region,p.last_test_message,
  CASE WHEN p.last_tested_at IS NULL THEN NULL ELSE to_char(p.last_tested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END AS last_tested_at,
  target.provider,target.provider_name,target.profile_id,target.target_url
FROM juhe_business.proxy_profiles p
LEFT JOIN selected_targets target ON TRUE
WHERE p.id=$1
ORDER BY target.provider_name ASC,target.provider ASC`

// EXPLAIN validates the exact append statements without writing an audit row.
// It intentionally includes each child table and ON CONFLICT clause so a
// partial, legacy, or incompatible F4 schema prevents the public listener
// from binding.
var manualAdminAuditContractSQL = []string{
	`EXPLAIN INSERT INTO juhe_dataset.operation_logs (
  id,trace_id,actor_system_account_id,actor_username,actor_display_name,actor_role,
  operation_scope_system_account_id,mode,module,action,operation_key,resource_type,
  resource_id,resource_name,summary,detail_level,visibility_scope,changes_json,
  metadata_json,method,path,status_code,client_ip,user_agent,created_at
) VALUES (
  '__j3a_management_contract_probe__',NULL,'__j3a_management_contract_probe__',NULL,NULL,'admin',
  NULL,'admin','proxies','test','proxies.test','proxy',NULL,NULL,'probe','full','admin_only','[]'::jsonb,
  '{}'::jsonb,NULL,NULL,200,NULL,NULL,clock_timestamp()
) ON CONFLICT (id) DO NOTHING`,
	`EXPLAIN INSERT INTO juhe_dataset.operation_log_targets (
  id,operation_log_id,target_type,target_id,target_name,target_owner_system_account_id,relation,created_at
) VALUES (
  '__j3a_management_contract_probe__','__j3a_management_contract_probe__','proxy',NULL,NULL,NULL,'primary',clock_timestamp()
)`,
	`EXPLAIN INSERT INTO juhe_dataset.operation_log_viewers (
  operation_log_id,system_account_id,visibility_reason,detail_level,created_at
) VALUES (
  '__j3a_management_contract_probe__','__j3a_management_contract_probe__','actor_self','full',clock_timestamp()
) ON CONFLICT DO NOTHING`,
	`EXPLAIN INSERT INTO juhe_dataset.operation_log_summary_search_terms (
  operation_log_id,term,created_at
) VALUES (
  '__j3a_management_contract_probe__','probe',clock_timestamp()
) ON CONFLICT DO NOTHING`,
}

// NewManualAdminHandler exposes exactly the legacy proxy-test resource path.
// The request is authenticated and executed in this process; the handler has
// no Node fallback and no call to another Go process.
func NewManualAdminHandler(runner ManualAdminRunner, source ManualAdminSource, audit ManualAdminAuditAppender, deadline time.Duration, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		proxyID, matched := manualAdminProxyID(request.URL.Path)
		if !matched || request.Method != http.MethodPost {
			http.NotFound(response, request)
			return
		}
		if runner == nil || source == nil || audit == nil {
			writeManualAdminError(response, http.StatusServiceUnavailable, "J3a proxy management unavailable", "")
			return
		}
		if err := validateManualAdminBody(response, request); err != nil {
			writeManualAdminError(response, http.StatusBadRequest, "请求体无效", "")
			return
		}
		actor, err := source.Authenticate(request.Context(), request.Header.Get("Authorization"), requestCookie(request, manualAdminSessionCookie))
		if err != nil {
			writeManualAdminAuthError(response, err)
			return
		}
		snapshot, err := source.LoadSnapshot(request.Context(), proxyID, deadline)
		if err != nil {
			writeManualAdminExecutionError(response, err)
			return
		}
		report, err := runner.RunManual(request.Context(), snapshot.Request)
		if err != nil {
			writeManualAdminExecutionError(response, err)
			return
		}
		exists, err := source.Exists(request.Context(), proxyID)
		if err != nil {
			writeManualAdminExecutionError(response, err)
			return
		}
		if !exists {
			writeManualAdminError(response, http.StatusNotFound, ErrManualAdminProxyMissing.Error(), "")
			return
		}
		response.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(response).Encode(map[string]any{"data": report}); err != nil {
			logger.Warn("encode J3a management report failed", "error", err, "proxyID", proxyID)
			return
		}
		requestMetadata := manualAdminRequestMetadata(request)
		go appendManualAdminOperationLog(audit, logger, actor, snapshot, report, requestMetadata)
	})
}

func manualAdminProxyID(path string) (string, bool) {
	if !strings.HasPrefix(path, manualAdminProxyTestPathPrefix) || !strings.HasSuffix(path, manualAdminProxyTestPathSuffix) {
		return "", false
	}
	value, err := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(path, manualAdminProxyTestPathPrefix), manualAdminProxyTestPathSuffix))
	if err != nil {
		return "", false
	}
	if value == "" || strings.Contains(value, "/") {
		return "", false
	}
	return value, true
}

func validateManualAdminBody(response http.ResponseWriter, request *http.Request) error {
	if request.Body == nil || request.ContentLength == 0 {
		return nil
	}
	request.Body = http.MaxBytesReader(response, request.Body, 64<<10)
	decoder := json.NewDecoder(request.Body)
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing request body")
	}
	return nil
}

func requestCookie(request *http.Request, name string) *http.Cookie {
	cookie, err := request.Cookie(name)
	if err != nil {
		return nil
	}
	return cookie
}

func writeManualAdminAuthError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrManualAdminInvalidToken):
		writeManualAdminError(response, http.StatusUnauthorized, err.Error(), "")
	case errors.Is(err, ErrManualAdminLoginRequired):
		writeManualAdminError(response, http.StatusUnauthorized, err.Error(), "")
	case errors.Is(err, ErrManualAdminSessionExpired):
		writeManualAdminError(response, http.StatusUnauthorized, err.Error(), "")
	case errors.Is(err, ErrManualAdminMustChange):
		writeManualAdminError(response, http.StatusForbidden, err.Error(), "must_change_password")
	case errors.Is(err, ErrManualAdminForbidden):
		writeManualAdminError(response, http.StatusForbidden, err.Error(), "")
	default:
		writeManualAdminError(response, http.StatusBadGateway, err.Error(), "")
	}
}

func writeManualAdminExecutionError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrManualAdminProxyMissing), errors.Is(err, ErrManualProxyMissing):
		writeManualAdminError(response, http.StatusNotFound, ErrManualAdminProxyMissing.Error(), "")
	case errors.Is(err, ErrOwnerLeaseHeld), errors.Is(err, ErrProxyLeaseHeld):
		response.Header().Set("Retry-After", "1")
		writeManualAdminError(response, http.StatusServiceUnavailable, "代理检测暂时繁忙", "")
	default:
		writeManualAdminError(response, http.StatusBadGateway, err.Error(), "")
	}
}

func writeManualAdminError(response http.ResponseWriter, status int, message, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	payload := map[string]string{"message": message}
	if code != "" {
		payload["code"] = code
	}
	_ = json.NewEncoder(response).Encode(payload)
}

type manualAdminRequestInfo struct {
	method, path, clientIP, userAgent string
}

func manualAdminRequestMetadata(request *http.Request) manualAdminRequestInfo {
	clientIP, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		clientIP = ""
	}
	return manualAdminRequestInfo{method: request.Method, path: request.URL.Path, clientIP: clientIP, userAgent: request.UserAgent()}
}

func appendManualAdminOperationLog(audit ManualAdminAuditAppender, logger *slog.Logger, actor ManualAdminActor, snapshot manualAdminSnapshot, report ProxyTestReport, request manualAdminRequestInfo) {
	id, err := operationlogappend.NewID("oplog")
	if err != nil {
		logger.Warn("generate J3a management operation log id failed", "error", err, "proxyID", report.ProxyID)
		return
	}
	statusCode := http.StatusOK
	input := operationlogappend.Input{
		ID: id, ActorSystemAccountID: actor.SystemAccountID, ActorUsername: actor.Username, ActorDisplayName: actor.DisplayName, ActorRole: actor.Role,
		Mode: "admin", Module: "proxies", Action: "test", OperationKey: "proxies.test", ResourceType: "proxy", ResourceID: report.ProxyID,
		ResourceName: report.ProxyName, Summary: "检测代理：" + report.ProxyName, DetailLevel: "full", VisibilityScope: "admin_only",
		Changes: manualAdminOperationChanges(snapshot.before, report), Metadata: json.RawMessage("{}"), Method: request.method, Path: request.path,
		StatusCode: &statusCode, ClientIP: request.clientIP, UserAgent: request.userAgent, CreatedAt: time.Now().UTC(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := audit.Append(ctx, input); err != nil {
		logger.Warn("append J3a management operation log failed", "error", err, "operationLogID", id, "proxyID", report.ProxyID)
	}
}

func manualAdminOperationChanges(before manualAdminTestState, report ProxyTestReport) []operationlogappend.Change {
	changes := make([]operationlogappend.Change, 0, 6)
	appendIfChanged := func(field, label string, left, right any) {
		if !manualAdminComparable(left, right) {
			changes = append(changes, operationlogappend.Change{Field: field, Label: label, Before: manualAdminAuditValue(left), After: manualAdminAuditValue(right)})
		}
	}
	appendIfChanged("testStatus", "检测状态", before.status, string(report.Status))
	appendIfChanged("latencyMs", "延迟", manualAdminIntValue(before.latencyMS), manualAdminIntValue(report.BaseLatencyMS))
	appendIfChanged("outboundIp", "出口 IP", manualAdminStringValue(before.outboundIP), emptyToNil(report.OutboundIP))
	appendIfChanged("outboundRegion", "出口地区", manualAdminStringValue(before.outboundRegion), emptyToNil(report.OutboundRegion))
	appendIfChanged("lastTestMessage", "检测消息", manualAdminStringValue(before.message), report.Message)
	appendIfChanged("lastTestedAt", "检测时间", manualAdminStringValue(before.testedAt), report.TestedAt)
	return changes
}

// Node's safeChange contract kept operation-log field values bounded. The Go
// producer has only scalar proxy-test values, so preserve the same observable
// truncation rule locally instead of carrying an unbounded transport message
// into the F4 audit database.
func manualAdminAuditValue(value any) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	characters := []rune(text)
	if len(characters) <= 200 {
		return text
	}
	return string(characters[:200]) + "…"
}

func manualAdminComparable(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func manualAdminIntValue(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func manualAdminStringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func emptyToNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}
