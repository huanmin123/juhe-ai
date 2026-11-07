package oauthrefresh

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/proxyresolve"
)

// CredentialVersionBumper 是 OAuth 凭据版本投影的写入端口：PG 提交成功后把
// 共享版本推进到该次提交的 config_revision（crossprocessstate.
// EntityAccountCredentialsVersion），让 gateway 运行态账号缓存不必等 60s
// revalidate。
//
// 契约（计划 §B2）：只在事务提交成功后调用；bump 失败只记告警，不回滚业务
// 写，也不阻断刷新——读者在版本缺失时按原有 60s 缓存/PG 路径回退。
type CredentialVersionBumper interface {
	Bump(ctx context.Context, accountID string, revision int64) error
}

// AccountStateVersionBumper / APIKeyStateVersionBumper 是运行态版本栅栏的写入
// 端口（B3/B4）：闸门事务提交成功后递增共享序号，gateway runtime 缓存下一次
// 读重载 PG 快照。未接线时保持既有 60s 可见性窗口。
type AccountStateVersionBumper interface {
	BumpAccountState(ctx context.Context, accountID string) error
}

type APIKeyStateVersionBumper interface {
	BumpAPIKeyState(ctx context.Context, apiKeyID string) error
}

// Store is the business database access for the J4 job family. The
// jobs process owns the narrow credential writers (rotation CAS + keepalive
// merge writer) and the maintenance queries; it never routes gateway traffic.
//
// 行以 juhe_business schema 限定，`?` 占位符改写为 $n（PG 唯一形态）。
type Store struct {
	db               *sql.DB
	secret           string
	now              func() time.Time
	credentialBumper CredentialVersionBumper
	accountState     AccountStateVersionBumper
	apiKeyState      APIKeyStateVersionBumper
	// proxyResolver 是代理池有效目标的只读解析器（代理池设计 §5.3）：Writer
	// 恒为 nil——刷新/保活不补租约、不写任何代理池状态；Health 为 nil（PG
	// proxy_profiles.pool_health_status 为事实源，Redis 快照缺失即回退 PG）。
	proxyResolver *proxyresolve.Resolver
}

// OpenStore wraps an opened database handle. secret is the Node
// runtimeConfig.secret material: credentials sealed by Node stay decryptable.
// The handle is owned by the caller (Close only closes via CloseHandle).
func OpenStore(db *sql.DB, secret string) (*Store, error) {
	if db == nil {
		return nil, errors.New("oauthrefresh store requires a database")
	}
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("oauthrefresh store requires the runtime secret")
	}
	proxyStore, err := proxyresolve.NewPostgresStore(db)
	if err != nil {
		return nil, err
	}
	proxyResolver, err := proxyresolve.New(proxyresolve.Options{
		Store:     proxyStore,
		Decrypter: storeProxyPasswordDecrypter{secret: secret},
	})
	if err != nil {
		return nil, err
	}
	return &Store{db: db, secret: secret, now: func() time.Time { return time.Now() }, proxyResolver: proxyResolver}, nil
}

// storeProxyPasswordDecrypter 用与凭据同一套 v1 AES-GCM 封套（DecryptJSON）
// 解开 proxy_profiles.password_encrypted（明文 {"password": "..."}）。
type storeProxyPasswordDecrypter struct {
	secret string
}

func (d storeProxyPasswordDecrypter) DecryptProxyPassword(_ context.Context, encrypted string) (string, error) {
	var envelope struct {
		Password *string `json:"password"`
	}
	if err := DecryptJSON(d.secret, encrypted, &envelope); err != nil {
		return "", err
	}
	if envelope.Password == nil {
		return "", errors.New("代理密码缺失")
	}
	return *envelope.Password, nil
}

// WithClock overrides the store clock (tests).
func (s *Store) WithClock(clock func() time.Time) *Store {
	if clock != nil {
		s.now = clock
	}
	return s
}

// SetCredentialVersionBumper 注入凭据版本投影写入端口。未接线（nil）时保持
// 既有 60s 可见性窗口，不影响刷新正确性。
func (s *Store) SetCredentialVersionBumper(bumper CredentialVersionBumper) {
	s.credentialBumper = bumper
}

// SetStateVersionBumpers 注入账户/API Key 运行态版本栅栏写入端口（B3/B4）。
func (s *Store) SetStateVersionBumpers(account AccountStateVersionBumper, apiKey APIKeyStateVersionBumper) {
	s.accountState = account
	s.apiKeyState = apiKey
}

// bumpCredentialVersion 在 PG 提交成功后把共享版本推进到该次提交的
// config_revision。失败只告警：读者会在版本键缺失时回退既有缓存窗口，业务
// 写已提交不可回滚。使用 detached ctx：任务取消/进程收尾不应吞掉已提交写
// 的版本投影（最多多花 2s）。
func (s *Store) bumpCredentialVersion(ctx context.Context, accountID string, revision int64) {
	if s.credentialBumper == nil {
		return
	}
	bumpCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := s.credentialBumper.Bump(bumpCtx, accountID, revision); err != nil {
		slog.Warn("OAuth 凭据版本投影写入失败，gateway 将按既有缓存窗口回退",
			"event", "oauth_credential_version_bump_failed",
			"accountId", accountID, "error", err)
	}
}

// bumpAccountStateVersion 在账户状态写提交后递增共享序号（B4）：让 gateway
// runtime 缓存下一次读重载 PG 快照，不必等 60s revalidate。失败只告警。
func (s *Store) bumpAccountStateVersion(ctx context.Context, accountID string) {
	if s.accountState == nil || strings.TrimSpace(accountID) == "" {
		return
	}
	bumpCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := s.accountState.BumpAccountState(bumpCtx, accountID); err != nil {
		slog.Warn("账户运行态版本栅栏写入失败，gateway 按既有缓存窗口回退",
			"event", "account_state_version_bump_failed",
			"accountId", accountID, "error", err)
	}
}

// Close closes the underlying handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) table(name string) string {
	return "juhe_business." + name
}

func (s *Store) bind(query string) string {
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + itoa(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// nowISO renders the store clock as Node nowIso().
func (s *Store) nowISO() string { return isoMillis(s.now()) }

// ---------------------------------------------------------------------------
// OpenAI refresh candidates
// ---------------------------------------------------------------------------

// RefreshCandidate mirrors OpenAIOAuthRefreshCandidateResult: either a
// decryptable account or a decrypt-failure marker (the local_configuration
// path).
type RefreshCandidate struct {
	Account *RotationAccount // nil for decrypt failures
	// decrypt failure payload
	AccountID                     string
	AccountName                   string
	AccountStatus                 string
	ConfigRevision                int64
	ErrorCode                     string
	ErrorMessage                  string
	AccountLocalEvidenceConfirmed bool
	// ObservedFailure carries the failure state read during batch selection so
	// the success path clears exactly the record it saw.
	ObservedFailure *RefreshFailureState
}

// IsDecryptFailure reports whether the candidate failed credential decryption.
func (c RefreshCandidate) IsDecryptFailure() bool { return c.Account == nil }

// openAIProfileIDs mirrors openAIProtocolProfileIdsForQuery: enabled OpenAI
// protocol profiles, falling back to the pinned GPT profile constant.
// 供应商解耦后（设计 §9.3/§9.11）profile 表退役，OpenAI 刷新候选身份收敛为
// (vendor_code='gpt', type='oauth')——该组合在 §1.1 registry 中恰好绑定唯一
// gpt_openai_v1 预设，不再需要 profile ID 列表门。
//
// ListDueOpenAIRefreshAccounts mirrors
// listOpenAIOAuthAccountsDueForAccessTokenRefreshAsync. leadSeconds/dueBefore
// semantics and the ordering contract stay byte-identical.
func (s *Store) ListDueOpenAIRefreshAccounts(ctx context.Context, leadSeconds int, limit int, stoppedErrorCode string, now time.Time) ([]RefreshCandidate, error) {
	dueBefore := isoMillis(now.Add(time.Duration(leadSeconds) * time.Second))
	limit = clampInt(limit, 1, 500)
	query := `SELECT a.id, a.system_account_id, COALESCE(a.vendor_code, ''), a.protocol_code, a.protocol_version, a.name, a.type, a.status, a.credentials_encrypted,
		a.proxy_profile_id, COALESCE(a.proxy_pool_id, ''),
		a.authorization_instance_source_account_id,
		COALESCE(source_account.proxy_profile_id, ''), COALESCE(source_account.proxy_pool_id, ''),
		a.concurrency_limit, a.priority,
		a.super_priority_enabled, a.fallback_enabled, a.schedulable, a.account_expires_at, a.cooldown_until,
		a.last_error_code, a.last_error_message, a.health_check_model, a.health_check_endpoint_mode, a.config_revision
	FROM ` + s.table("accounts") + ` a
	LEFT JOIN ` + s.table("accounts") + ` source_account
	  ON source_account.id = a.authorization_instance_source_account_id
	 AND source_account.deleted_at IS NULL
	WHERE a.authorization_instance_authorization_id IS NULL
		AND a.deleted_at IS NULL
		AND COALESCE(a.vendor_code, '') = ?
		AND a.type = 'oauth'
		AND (a.status <> 'error' OR a.last_error_code IS NULL OR a.last_error_code <> ?)
		AND (a.oauth_access_token_expires_at IS NULL OR a.oauth_access_token_expires_at <= ?)
	ORDER BY a.oauth_refresh_token_present ASC,
		(a.oauth_access_token_expires_at IS NOT NULL) ASC,
		a.oauth_access_token_expires_at ASC,
		a.updated_at ASC,
		a.id ASC
	LIMIT ?`
	args := []any{ProviderGPT, stoppedErrorCode, dueBefore, limit}
	return s.listRefreshCandidates(ctx, query, args...)
}

func (s *Store) listRefreshCandidates(ctx context.Context, query string, args ...any) ([]RefreshCandidate, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []RefreshCandidate{}
	for rows.Next() {
		var (
			id               string
			systemAccountID  string
			providerCode     string
			protocolCode     string
			protocolVersion  string
			name             string
			accountType      string
			status           string
			encrypted        string
			proxyProfileID   sql.NullString
			proxyPoolID      sql.NullString
			sourceAccountID  sql.NullString
			sourceProfileID  sql.NullString
			sourcePoolID     sql.NullString
			accountExpiresAt sql.NullString
			lastErrorCode    sql.NullString
			lastErrorMessage sql.NullString
			configRevision   int64
		)
		var concurrencyLimit, priority, superPriority, fallbackEnabled, schedulable, cooldownUntil any
		var healthCheckModel, healthCheckEndpointMode any
		if err := rows.Scan(&id, &systemAccountID, &providerCode, &protocolCode, &protocolVersion, &name, &accountType, &status, &encrypted,
			&proxyProfileID, &proxyPoolID, &sourceAccountID, &sourceProfileID, &sourcePoolID,
			&concurrencyLimit, &priority,
			&superPriority, &fallbackEnabled, &schedulable, &accountExpiresAt, &cooldownUntil,
			&lastErrorCode, &lastErrorMessage, &healthCheckModel, &healthCheckEndpointMode, &configRevision); err != nil {
			return nil, err
		}
		if configRevision < 1 {
			configRevision = 1
		}
		credentials := map[string]any{}
		if err := DecryptJSON(s.secret, encrypted, &credentials); err != nil {
			candidates = append(candidates, RefreshCandidate{
				AccountID:      id,
				AccountName:    name,
				AccountStatus:  status,
				ConfigRevision: configRevision,
				ErrorCode:      "oauth_credentials_decrypt_failed",
				ErrorMessage:   "本地 OpenAI OAuth 账户凭据无法解密，请重新授权或更新凭据",
			})
			continue
		}
		candidates = append(candidates, RefreshCandidate{Account: &RotationAccount{
			ID: id, SystemAccountID: systemAccountID,
			ProviderCode: providerCode,
			ProtocolCode: protocolCode, ProtocolVersion: protocolVersion,
			Name: name, Type: accountType, Status: status,
			LastErrorCode:    lastErrorCode.String,
			ProxyProfileID:   proxyProfileID.String,
			ProxyPoolID:      proxyPoolID.String,
			SourceAccountID:  sourceAccountID.String,
			SourceProfileID:  sourceProfileID.String,
			SourcePoolID:     sourcePoolID.String,
			Credentials:      credentials,
			AccountExpiresAt: accountExpiresAt.String,
			ConfigRevision:   configRevision,
		}})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// ---------------------------------------------------------------------------
// Rotation account + CAS credential writers
// ---------------------------------------------------------------------------

// RotationAccount mirrors OAuthCredentialRotationAccount. ProviderCode 承载
// 目标 schema 的 vendor_code 身份值（§2.1：真实厂商 vendor_code ==
// provider_code；§9.11 目标列集无 provider_code / provider_protocol_profile_id，
// profile 维度已删除）。
type RotationAccount struct {
	ID              string
	SystemAccountID string
	ProviderCode    string
	ProtocolCode    string
	ProtocolVersion string
	Name            string
	Type            string
	Status          string
	LastErrorCode   string
	ProxyProfileID  string
	// ProxyPoolID / Source* 四列承载代理池有效目标的行事实（代理池设计
	// §5.2）：ConfiguredPoolID 参与普通账户解析；授权实例（本族候选查询恒
	// authorization_instance_authorization_id IS NULL，防御性带上）按
	// source pool > source profile > 自身直接代理解析，实例自身池绑定不参与。
	ProxyPoolID      string
	SourceAccountID  string
	SourceProfileID  string
	SourcePoolID     string
	Credentials      map[string]any
	AccountExpiresAt string
	ConfigRevision   int64
	UpdatedAt        string
}

// CredentialsUnavailableError marks an account whose sealed credentials are
// absent or undecryptable (Node decryptJson throw path).
type CredentialsUnavailableError struct{}

func (e *CredentialsUnavailableError) Error() string { return "OAuth 凭据读取失败" }

// RevisionConflictError maps to AccountConfigRevisionConflictError.
type RevisionConflictError struct{ Message string }

func (e *RevisionConflictError) Error() string { return e.Message }

// FindRotationAccount mirrors findOAuthCredentialRotationAccountAsync (jobs
// internal access crosses owners, so no system_account scope is applied — the
// Node internal access sys_admin/super_admin is owner-free).
func (s *Store) FindRotationAccount(ctx context.Context, accountID string) (*RotationAccount, error) {
	var (
		id               string
		systemAccountID  string
		providerCode     string
		protocolCode     string
		protocolVersion  string
		name             string
		accountType      string
		status           string
		lastErrorCode    sql.NullString
		proxyProfileID   sql.NullString
		proxyPoolID      sql.NullString
		sourceAccountID  sql.NullString
		sourceProfileID  sql.NullString
		sourcePoolID     sql.NullString
		encrypted        string
		accountExpiresAt sql.NullString
		configRevision   int64
		updatedAt        string
	)
	// 身份读键 = COALESCE(vendor_code,'')（§9.11 目标列集无 provider_code /
	// provider_protocol_profile_id；与 gateway oauthmgmt T30 轮换读面同口径）。
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT a.id, a.system_account_id, COALESCE(a.vendor_code, ''),
			a.protocol_code, a.protocol_version, a.name, a.type, a.status,
			a.last_error_code, a.proxy_profile_id, COALESCE(a.proxy_pool_id, ''),
			a.authorization_instance_source_account_id,
			COALESCE(source_account.proxy_profile_id, ''), COALESCE(source_account.proxy_pool_id, ''),
			a.credentials_encrypted, a.account_expires_at, a.config_revision, a.updated_at
		FROM `+s.table("accounts")+` a
		LEFT JOIN `+s.table("accounts")+` source_account
		  ON source_account.id = a.authorization_instance_source_account_id
		 AND source_account.deleted_at IS NULL
		WHERE a.id = ?
			AND a.deleted_at IS NULL
			AND a.authorization_instance_authorization_id IS NULL
		LIMIT 1`), accountID).Scan(
		&id, &systemAccountID, &providerCode,
		&protocolCode, &protocolVersion, &name, &accountType, &status,
		&lastErrorCode, &proxyProfileID, &proxyPoolID, &sourceAccountID, &sourceProfileID, &sourcePoolID,
		&encrypted, &accountExpiresAt, &configRevision, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	credentials := map[string]any{}
	if err := DecryptJSON(s.secret, encrypted, &credentials); err != nil {
		return nil, &CredentialsUnavailableError{}
	}
	if credentials == nil {
		credentials = map[string]any{}
	}
	if configRevision < 1 {
		configRevision = 1
	}
	return &RotationAccount{
		ID: id, SystemAccountID: systemAccountID,
		ProviderCode: providerCode,
		ProtocolCode: protocolCode, ProtocolVersion: protocolVersion,
		Name: name, Type: accountType, Status: status,
		LastErrorCode:    lastErrorCode.String,
		ProxyProfileID:   proxyProfileID.String,
		ProxyPoolID:      proxyPoolID.String,
		SourceAccountID:  sourceAccountID.String,
		SourceProfileID:  sourceProfileID.String,
		SourcePoolID:     sourcePoolID.String,
		Credentials:      credentials,
		AccountExpiresAt: accountExpiresAt.String,
		ConfigRevision:   configRevision, UpdatedAt: updatedAt,
	}, nil
}

// ProxyTargetUnavailableError 标记池绑定账户的有效代理目标在本地解析阶段不可用
// （池不存在/停用、无有效租约、租约成员不可分配、配置双列冲突；代理池设计
// §5.4/§7.5）。它刻意不是 LocalConfigurationError：两个刷新族按既有“非本地
// 配置失败”分支处理——记录 300s 退避重试，不计入 OAuth 本地终态停刷；同时
// 任何分支都不回落直连。
type ProxyTargetUnavailableError struct{ Message string }

func (e *ProxyTargetUnavailableError) Error() string { return e.Message }

// proxyRequestURLForAccount 解析账户的有效代理出站 URL（代理池设计 §5.2/§5.3
// 只读入口）：source pool > source profile > 实例自身直接代理绑定（实例自身
// 池绑定不参与）；普通账户用自身池或直接代理绑定。池绑定走共享解析器只读
// 解析当前租约成员（Writer=nil，不补租约、不在刷新中途换出口）；无有效租约/
// 池不可用返回 ProxyTargetUnavailableError，绝不直连。直接代理与无绑定保持
// 既有 proxyProfileRequestURL 语义（含 LocalConfigurationError 分类）。
func (s *Store) proxyRequestURLForAccount(ctx context.Context, account *RotationAccount) (string, error) {
	ctx = ensureCtx(ctx)
	if account == nil {
		return "", errors.New("OAuth 账户为空，无法解析代理目标")
	}
	binding := proxyresolve.AccountBinding{
		AccountID:           account.ID,
		SourceAccountID:     account.SourceAccountID,
		ConfiguredProfileID: account.ProxyProfileID,
		ConfiguredPoolID:    account.ProxyPoolID,
		SourceProfileID:     account.SourceProfileID,
		SourcePoolID:        account.SourcePoolID,
	}
	kind, targetID := binding.Effective()
	switch kind {
	case proxyresolve.BindingPool:
		targets, err := s.proxyResolver.ResolveBatch(ctx, []string{account.ID})
		if err != nil {
			return "", err
		}
		target := targets[account.ID]
		if target.Available() {
			return target.URL, nil
		}
		return "", &ProxyTargetUnavailableError{Message: "账户代理池租约不可用（" + target.UnavailableCode + "）：" + target.UnavailableMessage}
	case proxyresolve.BindingConflict:
		return "", &ProxyTargetUnavailableError{Message: "账户代理与代理池同时绑定（数据损坏），请先修复账户代理配置"}
	case proxyresolve.BindingProfile:
		return s.proxyProfileRequestURL(ctx, targetID)
	default:
		return s.proxyProfileRequestURL(ctx, "")
	}
}

// RotateCredentialsInput mirrors rotateOAuthCredentialsAsync input (jobs
// internal access). 身份 CAS 键 = vendor_code（§2.1 行为键，真实厂商 ==
// provider_code）+ account type + config revision；profile 维度已随目标列集
// 删除（§9.11）。
type RotateCredentialsInput struct {
	AccountID              string
	ExpectedConfigRevision int64
	ExpectedProviderCode   string
	ExpectedAccountType    string
	Credentials            map[string]any
}

// RotationResult mirrors OAuthCredentialRotationResult.
type RotationResult struct {
	ID             string
	ConfigRevision int64
	UpdatedAt      string
	Changed        bool
	Credentials    map[string]any
}

// RotateCredentials mirrors rotateOAuthCredentialsAsync: revision CAS,
// credential column swap (sealed envelope + fingerprint + mask + the derived
// oauth_access_token_expires_at / oauth_refresh_token_present columns),
// config_revision + 1 and the unchanged no-op receipt.
func (s *Store) RotateCredentials(ctx context.Context, input RotateCredentialsInput) (*RotationResult, error) {
	ctx = ensureCtx(ctx)
	if input.ExpectedConfigRevision < 1 {
		return nil, errors.New("账户配置版本无效")
	}
	if expiresAt, exists := input.Credentials["expires_at"]; exists {
		if _, ok := canonicalRFC3339(normalizeText(expiresAt)); !ok {
			return nil, errors.New("OAuth 凭据 expires_at 必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var (
		id             string
		providerCode   string
		accountType    string
		encrypted      string
		configRevision int64
	)
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, COALESCE(vendor_code, ''), type, credentials_encrypted, config_revision
		FROM `+s.table("accounts")+`
		WHERE id = ?
			AND deleted_at IS NULL
			AND authorization_instance_authorization_id IS NULL
		LIMIT 1`), input.AccountID).Scan(&id, &providerCode, &accountType, &encrypted, &configRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if providerCode != input.ExpectedProviderCode ||
		accountType != input.ExpectedAccountType {
		return nil, nil
	}
	if configRevision != input.ExpectedConfigRevision {
		return nil, &RevisionConflictError{Message: "账户配置版本冲突"}
	}
	current := map[string]any{}
	if err := DecryptJSON(s.secret, encrypted, &current); err != nil {
		return nil, &CredentialsUnavailableError{}
	}
	if credentialsEqual(current, input.Credentials) {
		return &RotationResult{
			ID: id, ConfigRevision: configRevision,
			UpdatedAt: s.nowISO(), Changed: false, Credentials: input.Credentials,
		}, nil
	}
	source, err := requiredCredentialSource(input.ExpectedAccountType, input.Credentials)
	if err != nil {
		return nil, err
	}
	sealed, err := EncryptJSON(s.secret, map[string]any(input.Credentials))
	if err != nil {
		return nil, err
	}
	expiresAt := sql.NullString{}
	if value, exists := input.Credentials["expires_at"]; exists {
		if canonical, ok := canonicalRFC3339(normalizeText(value)); ok {
			expiresAt = sql.NullString{String: canonical, Valid: true}
		}
	}
	refreshPresent := 0
	if normalizeText(input.Credentials["refresh_token"]) != "" {
		refreshPresent = 1
	}
	updatedAt := s.nowISO()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
			oauth_access_token_expires_at = ?, oauth_refresh_token_present = ?,
			config_revision = config_revision + 1, updated_at = ?
		WHERE id = ? AND COALESCE(vendor_code, '') = ? AND type = ?
			AND config_revision = ?
			AND deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`),
		sealed, hashSecret(source), maskSecret(source), expiresAt, refreshPresent, updatedAt,
		id, providerCode, accountType, configRevision)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: "账户配置版本冲突"}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.bumpCredentialVersion(ctx, id, configRevision+1)
	return &RotationResult{
		ID: id, ConfigRevision: configRevision + 1,
		UpdatedAt: updatedAt, Changed: true, Credentials: input.Credentials,
	}, nil
}

// UpdateAccountCredentials mirrors updateAccountAsync({credentials}, …,
// {expectedConfigRevision}) as used by the three-provider dispatch
// preparation: a merge writer that verifies the expected revision and bumps
// config_revision + 1. The caller supplies the fully merged credentials; the
// derived columns refresh exactly like the rotation path.
func (s *Store) UpdateAccountCredentials(ctx context.Context, accountID string, credentials map[string]any, expectedConfigRevision int64) (*RotationAccount, error) {
	ctx = ensureCtx(ctx)
	if expectedConfigRevision < 1 {
		return nil, errors.New("账户配置版本无效")
	}
	if expiresAt, exists := credentials["expires_at"]; exists {
		if _, ok := canonicalRFC3339(normalizeText(expiresAt)); !ok {
			return nil, errors.New("OAuth 凭据 expires_at 必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var (
		id             string
		configRevision int64
	)
	err = tx.QueryRowContext(ctx, s.bind(`SELECT id, config_revision FROM `+s.table("accounts")+`
		WHERE id = ? AND deleted_at IS NULL LIMIT 1`), accountID).Scan(&id, &configRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if configRevision != expectedConfigRevision {
		return nil, &RevisionConflictError{Message: "账户已被其他请求修改，请重试"}
	}
	accountType := "oauth"
	source, err := requiredCredentialSource(accountType, credentials)
	if err != nil {
		return nil, err
	}
	sealed, err := EncryptJSON(s.secret, map[string]any(credentials))
	if err != nil {
		return nil, err
	}
	expiresAt := sql.NullString{}
	if value, exists := credentials["expires_at"]; exists {
		if canonical, ok := canonicalRFC3339(normalizeText(value)); ok {
			expiresAt = sql.NullString{String: canonical, Valid: true}
		}
	}
	refreshPresent := 0
	if normalizeText(credentials["refresh_token"]) != "" {
		refreshPresent = 1
	}
	updatedAt := s.nowISO()
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET credentials_encrypted = ?, credential_fingerprint = ?, credential_mask = ?,
			oauth_access_token_expires_at = ?, oauth_refresh_token_present = ?,
			config_revision = config_revision + 1, updated_at = ?
		WHERE id = ? AND config_revision = ? AND deleted_at IS NULL`),
		sealed, hashSecret(source), maskSecret(source), expiresAt, refreshPresent, updatedAt, id, configRevision)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, &RevisionConflictError{Message: "账户已被其他请求修改，请重试"}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.bumpCredentialVersion(ctx, accountID, configRevision+1)
	return s.FindRotationAccount(ctx, accountID)
}

// MarkAccountFailureState mirrors mark_openai_oauth_local_configuration_exception:
// flips an active account into the terminal refresh-failure error state guarded
// by the config revision and the current status. updated=false when the guard
// misses.
func (s *Store) MarkAccountFailureState(ctx context.Context, accountID, errorCode, reason string, expectedConfigRevision int64, expectedStatus string) (bool, error) {
	ctx = ensureCtx(ctx)
	updatedAt := s.nowISO()
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET status = 'error', last_error_code = ?, last_error_message = ?,
			config_revision = config_revision + 1, updated_at = ?
		WHERE id = ? AND config_revision = ? AND status = ? AND deleted_at IS NULL`),
		errorCode, reason, updatedAt, accountID, expectedConfigRevision, expectedStatus)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, nil
	}
	s.bumpAccountStateVersion(ctx, accountID)
	return true, nil
}

// ClearAccountFailureState mirrors clear_account_failure_state with the
// expectedLastErrorCodes guard: the account returns to active when the current
// last_error_code is one of the managed codes. changed reports the CAS guard.
// 守卫谓词为 status = 'error' AND last_error_code IN (managed codes)；空码集
// 时 IN 守卫恒不可满足（PG 不支持空 IN 列表），按 CAS 未命中返回，不发
// UPDATE。占位符数（updated_at + id + n 个码）必须与参数数严格一致——pgx
// 严格校验，多余参数即 mismatched param and argument count。
func (s *Store) ClearAccountFailureState(ctx context.Context, accountID string, expectedLastErrorCodes []string) (changed bool, accountStatus string, err error) {
	ctx = ensureCtx(ctx)
	if len(expectedLastErrorCodes) == 0 {
		return false, "", nil
	}
	guards := make([]string, len(expectedLastErrorCodes))
	args := []any{s.nowISO(), accountID}
	for i, code := range expectedLastErrorCodes {
		guards[i] = "?"
		args = append(args, code)
	}
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+`
		SET status = 'active', last_error_code = NULL, last_error_message = NULL,
			config_revision = config_revision + 1, updated_at = ?
		WHERE id = ? AND status = 'error' AND last_error_code IN (`+strings.Join(guards, ", ")+`)
			AND deleted_at IS NULL`), args...)
	if err != nil {
		return false, "", err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, "", err
	}
	if affected != 1 {
		return false, "", nil
	}
	s.bumpAccountStateVersion(ctx, accountID)
	var status string
	if err := s.db.QueryRowContext(ctx, s.bind(`SELECT status FROM `+s.table("accounts")+` WHERE id = ?`), accountID).Scan(&status); err != nil {
		return true, "", err
	}
	return true, status, nil
}

// ---------------------------------------------------------------------------
// Keepalive due list (anthropic / gemini / grok)
// ---------------------------------------------------------------------------

// KeepaliveCandidate is one due keepalive account.
type KeepaliveCandidate struct {
	Account *RotationAccount
}

// ListDueKeepaliveAccounts mirrors the dispatch-preparation refresh window as a
// batch query: oauth accounts of the vendor whose derived
// oauth_access_token_expires_at is missing or within the lead window. gemini
// accounts use type google_oauth；身份键为 COALESCE 后的 vendor_code（§2.1
// 行为键；§9.11 目标列集无 provider_code / provider_protocol_profile_id，
// profile 维度门已删除——vendor+type 在 §1.1 registry 已唯一绑定预设）。
func (s *Store) ListDueKeepaliveAccounts(ctx context.Context, providerCode, accountType string, lead time.Duration, limit int, now time.Time) ([]KeepaliveCandidate, error) {
	dueBefore := isoMillis(now.Add(lead))
	limit = clampInt(limit, 1, 500)
	query := `SELECT a.id, a.system_account_id, COALESCE(a.vendor_code, ''), a.protocol_code, a.protocol_version, a.name, a.type, a.status, a.credentials_encrypted,
		a.proxy_profile_id, COALESCE(a.proxy_pool_id, ''),
		a.authorization_instance_source_account_id,
		COALESCE(source_account.proxy_profile_id, ''), COALESCE(source_account.proxy_pool_id, ''),
		a.account_expires_at, a.last_error_code, a.config_revision, a.updated_at
	FROM ` + s.table("accounts") + ` a
	LEFT JOIN ` + s.table("accounts") + ` source_account
	  ON source_account.id = a.authorization_instance_source_account_id
	 AND source_account.deleted_at IS NULL
	WHERE a.authorization_instance_authorization_id IS NULL
		AND a.deleted_at IS NULL
		AND COALESCE(a.vendor_code, '') = ?
		AND a.type = ?
		AND (a.status <> 'error' OR a.last_error_code IS NULL OR a.last_error_code NOT IN ('oauth_token_refresh_failed', 'oauth_token_refresh_local_configuration_invalid'))
		AND (a.oauth_access_token_expires_at IS NULL OR a.oauth_access_token_expires_at <= ?)
		AND a.oauth_refresh_token_present = 1`
	args := []any{providerCode, accountType, dueBefore}
	query += `
	ORDER BY a.oauth_access_token_expires_at IS NOT NULL ASC, a.oauth_access_token_expires_at ASC, a.updated_at ASC, a.id ASC
	LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []KeepaliveCandidate{}
	for rows.Next() {
		var (
			id              string
			systemAccountID string
			rowProviderCode string
			protocolCode    string
			protocolVersion string
			name            string
			rowAccountType  string
			status          string
			encrypted       string
			proxyProfileID  sql.NullString
			proxyPoolID     sql.NullString
			sourceAccountID sql.NullString
			sourceProfileID sql.NullString
			sourcePoolID    sql.NullString
			accountExpires  sql.NullString
			lastErrorCode   sql.NullString
			configRevision  int64
			updatedAt       string
		)
		if err := rows.Scan(&id, &systemAccountID, &rowProviderCode, &protocolCode, &protocolVersion, &name, &rowAccountType, &status, &encrypted,
			&proxyProfileID, &proxyPoolID, &sourceAccountID, &sourceProfileID, &sourcePoolID,
			&accountExpires, &lastErrorCode, &configRevision, &updatedAt); err != nil {
			return nil, err
		}
		if configRevision < 1 {
			configRevision = 1
		}
		credentials := map[string]any{}
		if err := DecryptJSON(s.secret, encrypted, &credentials); err != nil {
			// Keepalive skips undecryptable rows; the OpenAI refresh family owns
			// the local-configuration terminal path and the sweep stays readable.
			continue
		}
		candidates = append(candidates, KeepaliveCandidate{Account: &RotationAccount{
			ID: id, SystemAccountID: systemAccountID,
			ProviderCode: rowProviderCode,
			ProtocolCode: protocolCode, ProtocolVersion: protocolVersion,
			Name: name, Type: rowAccountType, Status: status,
			LastErrorCode:    lastErrorCode.String,
			ProxyProfileID:   proxyProfileID.String,
			ProxyPoolID:      proxyPoolID.String,
			SourceAccountID:  sourceAccountID.String,
			SourceProfileID:  sourceProfileID.String,
			SourcePoolID:     sourcePoolID.String,
			Credentials:      credentials,
			AccountExpiresAt: accountExpires.String,
			ConfigRevision:   configRevision, UpdatedAt: updatedAt,
		}})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// credentialsEqual mirrors isDeepStrictEqual over the JSON-normalized
// credential maps (canonical re-encode comparison).
func credentialsEqual(current, next map[string]any) bool {
	encode := func(value map[string]any) (string, bool) {
		raw, err := json.Marshal(value)
		return string(raw), err == nil
	}
	left, leftOK := encode(current)
	right, rightOK := encode(next)
	return leftOK && rightOK && left == right
}

// requiredCredentialSource mirrors requiredAccountCredentialSource: the secret
// field that backs the fingerprint/mask columns per account type.
func requiredCredentialSource(accountType string, credentials map[string]any) (string, error) {
	pick := func(keys ...string) string {
		for _, key := range keys {
			if value := normalizeText(credentials[key]); value != "" {
				return value
			}
		}
		return ""
	}
	switch accountType {
	case "oauth":
		if value := pick("refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", errors.New("OAuth 凭据不能为空")
	case "api_key":
		if value := normalizeText(credentials["api_key"]); value != "" {
			return value, nil
		}
		return "", errors.New("API Key 不能为空")
	case "google_oauth":
		if value := pick("refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", errors.New("Google OAuth 凭据不能为空")
	default:
		if value := pick("api_key", "refresh_token", "access_token"); value != "" {
			return value, nil
		}
		return "", errors.New("账户凭据不能为空")
	}
}

// stringCredential mirrors stringCredential: trimmed string credential or "".
func stringCredential(credentials map[string]any, key string) string {
	return normalizeText(credentials[key])
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func ensureCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
