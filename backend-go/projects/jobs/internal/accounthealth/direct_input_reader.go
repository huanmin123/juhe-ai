package accounthealth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PostgresDirectInputReader reads only frozen business facts through a
// separately configured read-only connection. It never opens Node SQLite and
// never touches the jobs output schema.
type PostgresDirectInputReader struct {
	db               *sql.DB
	credentialSecret string
	inputTTL         time.Duration
	now              func() time.Time
	suppression      func(context.Context, time.Time) ([]DirectInputSuppression, error)
}

// DirectInputLoadResult separates a candidate-local construction failure from
// a failed read transaction. The runner must persist every Failure before it
// probes Inputs; a SQL/query/scan failure remains a whole-read error.
type DirectInputLoadResult struct {
	Inputs   []Input
	Failures []DirectInputFailure
}

// DirectInputFailure carries only the immutable account generation needed for
// a durable, idempotent task receipt. It deliberately excludes credentials,
// proxy values, and the underlying conversion error.
type DirectInputFailure struct {
	AccountID        string
	InputVersion     int64
	ConfigRevision   int64
	DispatchRevision int64
}

// DirectInputSuppression is jobs-owned retry metadata for a malformed
// business candidate. The business reader never writes this state; it only
// receives a snapshot from the jobs Store and excludes the exact fenced
// generation while its retry window is still active.
type DirectInputSuppression struct {
	AccountID        string
	InputVersion     int64
	ConfigRevision   int64
	DispatchRevision int64
	NextDueAt        time.Time
}

func NewPostgresDirectInputReader(db *sql.DB, credentialSecret string, inputTTL time.Duration, now func() time.Time) (*PostgresDirectInputReader, error) {
	if db == nil || strings.TrimSpace(credentialSecret) == "" || inputTTL < time.Minute || inputTTL > 7*24*time.Hour {
		return nil, fmt.Errorf("PG direct input reader 配置无效")
	}
	if now == nil {
		now = time.Now
	}
	return &PostgresDirectInputReader{db: db, credentialSecret: credentialSecret, inputTTL: inputTTL, now: now}, nil
}

// SetSuppressionProvider wires the jobs-owned retry snapshot without giving
// the business reader any write access to the jobs schema.
func (r *PostgresDirectInputReader) SetSuppressionProvider(provider func(context.Context, time.Time) ([]DirectInputSuppression, error)) {
	if r != nil {
		r.suppression = provider
	}
}

// CheckContract verifies the frozen business-read contract before the runner
// can acquire a J1 lease. It deliberately performs only zero-row reads: jobs
// must fail clearly when Node-owned schema/bootstrap or SELECT grants are
// incomplete, never try to repair business schema themselves.
func (r *PostgresDirectInputReader) CheckContract(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("开始 PG direct input 契约预检事务失败: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL TRANSACTION READ ONLY"); err != nil {
		return fmt.Errorf("设置 PG direct input 契约预检只读事务失败: %w", err)
	}
	for _, relation := range directInputRequiredRelations {
		if _, err := tx.ExecContext(ctx, "SELECT 1 FROM "+relation+" LIMIT 0"); err != nil {
			return fmt.Errorf("PG direct input 缺少业务只读契约 %s: %w", relation, err)
		}
	}
	if _, _, err := loadDirectSchedule(ctx, tx); err != nil {
		return err
	}
	now := r.now().UTC()
	rows, err := tx.QueryContext(ctx, directInputCandidatesSQL, now.Format(time.RFC3339Nano), 0, false, "", 0)
	if err != nil {
		return fmt.Errorf("验证 PG direct input 候选查询失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭 PG direct input 契约预检游标失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 PG direct input 契约预检事务失败: %w", err)
	}
	return nil
}

var directInputRequiredRelations = []string{
	"juhe_business.accounts",
	"juhe_business.account_health_jobs_input_versions",
	"juhe_business.group_accounts",
	"juhe_business.proxy_profiles",
	"juhe_business.resource_authorizations",
	"juhe_business.resource_authorization_grants",
	"juhe_business.system_settings",
	"juhe_stats.usage_stats_totals",
	"juhe_stats.usage_stats_daily",
	"juhe_stats.usage_stats_weekly",
	"juhe_stats.usage_stats_monthly",
	"juhe_stats.usage_quota_hourly_windows",
}

func (r *PostgresDirectInputReader) LoadDue(ctx context.Context, limit int) ([]Input, error) {
	result, err := r.LoadDueWithFailures(ctx, limit)
	if err == nil && len(result.Failures) > 0 {
		return nil, fmt.Errorf("PG direct input 存在 %d 个候选构造失败；请使用 LoadDueWithFailures 处理隔离结果", len(result.Failures))
	}
	return result.Inputs, err
}

func (r *PostgresDirectInputReader) LoadDueWithFailures(ctx context.Context, limit int) (DirectInputLoadResult, error) {
	return r.load(ctx, limit, false, "")
}

// LoadAccount is only for a signed explicit request. It skips the periodic
// due-time predicate, but retains every account/source/authorization/binding/
// quota eligibility guard from LoadDue.
func (r *PostgresDirectInputReader) LoadAccount(ctx context.Context, accountID string) ([]Input, error) {
	result, err := r.LoadAccountWithFailures(ctx, accountID)
	if err == nil && len(result.Failures) > 0 {
		return nil, fmt.Errorf("PG direct input account=%s 候选构造失败；请使用 LoadAccountWithFailures 处理隔离结果", strings.TrimSpace(accountID))
	}
	return result.Inputs, err
}

func (r *PostgresDirectInputReader) LoadAccountWithFailures(ctx context.Context, accountID string) (DirectInputLoadResult, error) {
	normalizedAccountID := strings.TrimSpace(accountID)
	if normalizedAccountID == "" {
		return DirectInputLoadResult{}, fmt.Errorf("PG direct input account ID 不能为空")
	}
	return r.load(ctx, 1, true, normalizedAccountID)
}

func (r *PostgresDirectInputReader) load(ctx context.Context, limit int, ignoreSchedule bool, accountID string) (DirectInputLoadResult, error) {
	if limit < 1 || limit > maxJ1Capacity {
		return DirectInputLoadResult{}, fmt.Errorf("PG direct input limit 必须在 1..%d", maxJ1Capacity)
	}
	now := r.now().UTC()
	var suppressions []DirectInputSuppression
	if r.suppression != nil {
		var err error
		suppressions, err = r.suppression(ctx, now)
		if err != nil {
			return DirectInputLoadResult{}, fmt.Errorf("读取 PG direct input 重试抑制快照失败: %w", err)
		}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return DirectInputLoadResult{}, fmt.Errorf("开始 PG direct input 只读事务失败: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL TRANSACTION READ ONLY"); err != nil {
		return DirectInputLoadResult{}, fmt.Errorf("设置 PG direct input 只读事务失败: %w", err)
	}
	schedule, timezone, err := loadDirectSchedule(ctx, tx)
	if err != nil {
		return DirectInputLoadResult{}, err
	}
	result := DirectInputLoadResult{Inputs: make([]Input, 0, limit), Failures: make([]DirectInputFailure, 0)}
	err = collectDirectCandidatePages(limit, func(offset int) (int, error) {
		candidateSQL, suppressionArgs := directInputCandidatesQuery(suppressions)
		args := []any{now.Format(time.RFC3339Nano), limit, ignoreSchedule, accountID, offset}
		args = append(args, suppressionArgs...)
		rows, err := tx.QueryContext(ctx, candidateSQL, args...)
		if err != nil {
			return 0, fmt.Errorf("读取 PG direct input 候选失败: %w", err)
		}
		pageCount := 0
		candidates := make([]directCandidate, 0, limit)
		for rows.Next() {
			pageCount++
			candidate, err := scanDirectCandidate(rows)
			if err != nil {
				rows.Close()
				return 0, err
			}
			candidates = append(candidates, candidate)
		}
		if err := rows.Close(); err != nil {
			return 0, fmt.Errorf("关闭 PG direct input 候选游标失败: %w", err)
		}
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("遍历 PG direct input 候选失败: %w", err)
		}
		for _, candidate := range candidates {
			if len(result.Inputs) >= limit {
				break
			}
			if candidate.authorization != nil {
				eligible, err := authorizationQuotaEligible(ctx, tx, candidate, now, timezone)
				if err != nil {
					return 0, err
				}
				// A valid authorization that has exhausted its own or team scope is
				// not a malformed input and must not make unrelated accounts fail to
				// load. Node applies the same fail-closed eligibility filter before
				// dispatch; omit it rather than probing or returning a fallback input.
				if !eligible {
					continue
				}
				candidate.authorization.QuotaEligible = eligible
			}
			direct := DirectInput{Account: candidate.account, Authorization: candidate.authorization, Source: candidate.source, Binding: candidate.binding, Proxy: candidate.proxy, InputVersion: candidate.inputVersion, IssuedAt: now, ExpiresAt: now.Add(r.inputTTL), TLSPolicy: "j1-direct-upstream-v1", Schedule: schedule}
			input, failure, err := buildDirectCandidateInput(candidate, direct, r.credentialSecret, now)
			if err != nil {
				return 0, err
			}
			if failure != nil {
				result.Failures = append(result.Failures, *failure)
				if len(result.Inputs) >= limit {
					break
				}
				continue
			}
			result.Inputs = append(result.Inputs, input)
			if len(result.Inputs) >= limit {
				break
			}
		}
		return pageCount, nil
	}, func() int { return len(result.Inputs) })
	if err != nil {
		return DirectInputLoadResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DirectInputLoadResult{}, fmt.Errorf("提交 PG direct input 只读事务失败: %w", err)
	}
	return result, nil
}

func buildDirectCandidateInput(candidate directCandidate, direct DirectInput, secret string, now time.Time) (Input, *DirectInputFailure, error) {
	input, err := direct.ToInput(secret, now)
	if err == nil {
		return input, nil, nil
	}
	failure := DirectInputFailure{AccountID: candidate.account.ID, InputVersion: candidate.inputVersion, ConfigRevision: candidate.account.ConfigRevision, DispatchRevision: candidate.account.DispatchRevision}
	if strings.TrimSpace(failure.AccountID) == "" || failure.InputVersion < 1 || failure.ConfigRevision < 1 || failure.DispatchRevision < 1 {
		return Input{}, nil, fmt.Errorf("PG direct input 坏候选缺少可持久化的 account/revision fence")
	}
	return Input{}, &failure, nil
}

type directCandidate struct {
	account                 DirectAccount
	authorization           *DirectAuthorization
	authorizationLimits     string
	source                  *DirectSource
	binding                 DirectBinding
	proxy                   *DirectProxy
	inputVersion            int64
	systemAccount           string
	authorizationResourceID string
	authorizationOwner      string
	authorizationTeam       string
}

// directInputCandidatesQuery keeps the frozen SQL unchanged when there is no
// retry metadata, and otherwise adds a parameterized fenced suppression CTE.
// Suppression is deliberately applied before LIMIT/OFFSET so malformed rows
// cannot consume the bounded candidate window on every cycle.
func directInputCandidatesQuery(suppressions []DirectInputSuppression) (string, []any) {
	if len(suppressions) == 0 {
		return directInputCandidatesSQL, nil
	}
	values := make([]string, 0, len(suppressions))
	args := make([]any, 0, len(suppressions)*5)
	for index, suppression := range suppressions {
		base := 6 + index*5
		values = append(values, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d)", base, base+1, base+2, base+3, base+4))
		args = append(args, suppression.AccountID, suppression.InputVersion, suppression.ConfigRevision, suppression.DispatchRevision, suppression.NextDueAt.UTC())
	}
	clause := "\n  AND NOT EXISTS (SELECT 1 FROM (VALUES " + strings.Join(values, ",") + ") AS suppressed(account_id, input_version, config_revision, dispatch_revision, next_due_at) WHERE suppressed.account_id = a.id AND suppressed.input_version = iv.current_version AND suppressed.config_revision = a.config_revision AND suppressed.dispatch_revision = a.dispatch_revision AND suppressed.next_due_at > $1)"
	marker := "\n-- Keep activation work first"
	query := strings.Replace(directInputCandidatesSQL, marker, clause+marker, 1)
	return query, args
}

// collectDirectCandidatePages advances through a repeatable-read candidate
// snapshot until quota filtering has produced the requested number of inputs
// or there are no more rows. This keeps SQL page bounds while preventing an
// ineligible full page from starving later eligible candidates. The scan cap
// bounds work when a large backlog consists entirely of malformed candidates;
// callers still receive the failures observed before the cap and can retry
// them through their durable suppression window on a later cycle.
func collectDirectCandidatePages(limit int, loadPage func(offset int) (int, error), accepted func() int) error {
	return collectDirectCandidatePagesWithCap(limit, directInputScanCap(limit), loadPage, accepted)
}

func collectDirectCandidatePagesWithCap(limit, scanCap int, loadPage func(offset int) (int, error), accepted func() int) error {
	if limit < 1 || limit > maxJ1Capacity {
		return fmt.Errorf("PG direct input limit 必须在 1..%d", maxJ1Capacity)
	}
	if scanCap < limit {
		scanCap = limit
	}
	scanned := 0
	for offset := 0; accepted() < limit && scanned < scanCap; {
		pageCount, err := loadPage(offset)
		if err != nil {
			return err
		}
		if pageCount < 0 || pageCount > limit {
			return fmt.Errorf("PG direct input 候选页行数无效: %d", pageCount)
		}
		scanned += pageCount
		if pageCount < limit {
			return nil
		}
		offset += pageCount
	}
	return nil
}

const (
	maxDirectInputScanCandidates = 5096
	directInputScanMultiplier    = 16
)

func directInputScanCap(limit int) int {
	// A small accepted batch must still be able to pass a durable backlog of
	// due cooldown rows. The SQL ordering deliberately interleaves cooldown
	// and active work, but a 4x window at the production limit (64) only
	// examines 256 rows and can leave a newly due account behind a stuck
	// cooldown backlog. Keep the work bounded while giving each cycle a 16x
	// admission window (1024 rows at the current production limit).
	cap := limit * directInputScanMultiplier
	if cap < limit {
		cap = limit
	}
	if cap > maxDirectInputScanCandidates {
		return maxDirectInputScanCandidates
	}
	return cap
}

const directInputCandidatesSQL = `
SELECT
  a.id, iv.current_version, a.config_revision, a.dispatch_revision, a.provider_code, a.provider_protocol_profile_id, a.protocol_code, a.protocol_version, a.type, a.client_compatibility, a.status, a.schedulable,
  a.health_check_endpoint_mode, a.health_check_model, mapping.upstream_model, mapping.upstream_endpoint_family, a.credentials_encrypted, a.account_expires_at, a.cooldown_until, a.temporary_unavailable_continuous_probe_enabled,
  a.cooldown_retest_observation_started_at, a.cooldown_retest_generation,
  a.system_account_id,
  ra.id, ra.status, ra.expires_at, ra.limits_json, ra.resource_id, ra.resource_owner_system_account_id, ra.effective_source_team_id,
  source.id, source.config_revision, source.provider_code, source.provider_protocol_profile_id, source.protocol_code, source.protocol_version, source.type, source.client_compatibility, source.status, source.schedulable,
  source.account_expires_at, source.cooldown_until, source.last_error_code, source.credentials_encrypted,
  binding.group_id, binding.account_authorization_id,
  proxy.id, proxy.enabled, proxy.type, proxy.host, proxy.port, proxy.username, proxy.password_encrypted
FROM juhe_business.accounts a
JOIN juhe_business.account_health_jobs_input_versions iv ON iv.account_id = a.id
LEFT JOIN juhe_business.resource_authorizations ra ON ra.id = a.authorization_instance_authorization_id
LEFT JOIN juhe_business.accounts source ON source.id = a.authorization_instance_source_account_id AND source.deleted_at IS NULL
LEFT JOIN LATERAL (
  SELECT ga.group_id, ga.account_authorization_id
  FROM juhe_business.group_accounts ga
  WHERE ga.account_id = a.id AND ga.system_account_id = a.system_account_id AND ga.enabled = 1
    AND (a.authorization_instance_authorization_id IS NULL OR ga.account_authorization_id = a.authorization_instance_authorization_id)
  ORDER BY ga.updated_at DESC, ga.group_id ASC, ga.account_id ASC LIMIT 1
) binding ON TRUE
LEFT JOIN juhe_business.proxy_profiles proxy ON proxy.id = CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.proxy_profile_id ELSE source.proxy_profile_id END
LEFT JOIN LATERAL (
  SELECT mm.upstream_model, mm.upstream_endpoint_family
  FROM juhe_business.account_model_mappings mm
  WHERE mm.account_id = CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.id ELSE source.id END
    AND mm.provider_code = CASE WHEN a.authorization_instance_authorization_id IS NULL THEN a.provider_code ELSE source.provider_code END
    AND mm.enabled = 1
    AND mm.source_model = a.health_check_model
    AND (mm.upstream_model <> mm.source_model OR mm.upstream_endpoint_family <> mm.source_endpoint_family)
    AND mm.source_endpoint_family = CASE
      WHEN a.health_check_endpoint_mode IN ('chat_json', 'chat_sse') THEN 'chat_completions'
      WHEN a.health_check_endpoint_mode IN ('responses_json', 'responses_sse') THEN 'responses'
      WHEN a.health_check_endpoint_mode IN ('messages_json', 'messages_sse') THEN 'messages'
      WHEN a.health_check_endpoint_mode = 'generate_content_json' THEN 'generate_content'
      WHEN a.health_check_endpoint_mode = 'generate_content_sse' THEN 'stream_generate_content'
      ELSE 'interactions'
    END
  ORDER BY mm.updated_at DESC, mm.source_model ASC
  LIMIT 1
) mapping ON TRUE
WHERE a.deleted_at IS NULL
  AND a.provider_code IN ('gpt', 'openai', 'xai', 'anthropic', 'deepseek', 'glm', 'gemini', 'hybrid')
  AND a.type IN ('api_key', 'oauth', 'google_oauth')
  AND (a.provider_protocol_profile_id <> 'profile_hybrid_openai_chat_v1' OR mapping.upstream_model IS NOT NULL)
  AND a.health_check_endpoint_mode IN ('chat_json', 'chat_sse', 'responses_json', 'responses_sse', 'images_json', 'messages_json', 'messages_sse', 'generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse')
  AND a.status IN ('active', 'pending_test', 'temporary_unavailable', 'rate_limited')
  AND (a.status = 'pending_test' OR a.schedulable = 1)
  AND (a.account_expires_at IS NULL OR a.account_expires_at > $1)
  AND (a.cooldown_until IS NULL OR a.cooldown_until <= $1)
  AND binding.group_id IS NOT NULL
  AND (a.authorization_instance_authorization_id IS NULL OR (
    ra.id IS NOT NULL AND ra.status = 'active' AND (ra.expires_at IS NULL OR ra.expires_at > $1)
    AND ra.resource_type = 'account' AND ra.resource_id = source.id
    AND ra.resource_owner_system_account_id = source.system_account_id AND ra.grantee_system_account_id = a.system_account_id
    AND source.provider_code IN ('gpt', 'openai', 'xai', 'anthropic', 'deepseek', 'glm', 'gemini', 'hybrid') AND source.type IN ('api_key', 'oauth', 'google_oauth') AND source.status = 'active' AND source.schedulable = 1
    AND source.deleted_at IS NULL AND source.last_error_code IS DISTINCT FROM 'account_expired'
    AND (source.account_expires_at IS NULL OR source.account_expires_at > $1)
    AND (source.cooldown_until IS NULL OR source.cooldown_until <= $1)
  ))
  -- Image generation is too expensive for a healthy periodic check. Keep
  -- image accounts eligible for activation, cooldown recovery, and explicit
  -- account loads (ignoreSchedule=$3), but never schedule an active image
  -- account merely because next_health_check_at is due.
  AND ($3::boolean OR a.status <> 'active' OR a.health_check_endpoint_mode <> 'images_json')
  AND ($3::boolean OR a.status IN ('temporary_unavailable', 'rate_limited') OR a.next_health_check_at IS NULL OR a.next_health_check_at <= $1 OR (a.status = 'pending_test' AND a.last_health_check_at IS NULL))
  AND ($4 = '' OR a.id = $4)
-- Keep activation work first, then interleave due cooldown recovery and
-- periodic active checks. A plain status tier would let a large cooldown (or
-- active) backlog starve the other class under LIMIT; per-class row numbers
-- give each class a slot while still filling from the other class when one is
-- exhausted. Do not order by updated_at: outcome projection changes it.
ORDER BY CASE WHEN a.status = 'pending_test' THEN 0 ELSE 1 END,
  CASE WHEN a.status = 'pending_test' THEN
    ROW_NUMBER() OVER (
      PARTITION BY CASE WHEN a.status = 'pending_test' THEN 0 WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN 1 ELSE 2 END
      ORDER BY a.next_health_check_at ASC NULLS FIRST, a.last_health_check_at ASC NULLS FIRST, a.created_at ASC, a.id ASC)
  ELSE
    ROW_NUMBER() OVER (
      PARTITION BY CASE WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN 0 ELSE 1 END
      ORDER BY CASE WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN a.cooldown_until ELSE a.next_health_check_at END ASC NULLS FIRST, a.last_health_check_at ASC NULLS FIRST, a.created_at ASC, a.id ASC)
  END,
  CASE WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN 0 ELSE 1 END,
  CASE WHEN a.status IN ('temporary_unavailable', 'rate_limited') THEN a.cooldown_until ELSE a.next_health_check_at END ASC NULLS FIRST,
  a.last_health_check_at ASC NULLS FIRST, a.created_at ASC, a.id ASC
LIMIT $2 OFFSET $5`

func scanDirectCandidate(rows *sql.Rows) (directCandidate, error) {
	var result directCandidate
	var schedulable, sourceSchedulable sql.NullInt64
	var continuousProbe sql.NullBool
	var accountExpires, cooldownUntil, observationStarted, cooldownGeneration sql.NullString
	var authorizationID, authorizationStatus, authorizationExpires, authorizationLimits sql.NullString
	var authorizationResourceID, authorizationOwner, authorizationTeam sql.NullString
	var accountProfile, accountProtocol, accountProtocolVersion, accountClientCompatibility sql.NullString
	var mappedUpstreamModel, mappedUpstreamFamily sql.NullString
	var sourceID, sourceProvider, sourceProfile, sourceProtocol, sourceProtocolVersion, sourceType, sourceClientCompatibility, sourceStatus, sourceExpires, sourceCooldown, sourceError, sourceCredentials sql.NullString
	var sourceRevision sql.NullInt64
	var groupID, bindingAuthorizationID sql.NullString
	var proxyID, proxyType, proxyHost, proxyUsername, proxyPassword sql.NullString
	var proxyEnabled sql.NullBool
	var proxyPort sql.NullInt64
	if err := rows.Scan(
		&result.account.ID, &result.inputVersion, &result.account.ConfigRevision, &result.account.DispatchRevision, &result.account.Provider, &accountProfile, &accountProtocol, &accountProtocolVersion, &result.account.Type, &accountClientCompatibility, &result.account.Status, &schedulable,
		&result.account.EndpointMode, &result.account.HealthModel, &mappedUpstreamModel, &mappedUpstreamFamily, &result.account.CredentialsEncrypted, &accountExpires, &cooldownUntil, &continuousProbe,
		&observationStarted, &cooldownGeneration, &result.systemAccount,
		&authorizationID, &authorizationStatus, &authorizationExpires, &authorizationLimits, &authorizationResourceID, &authorizationOwner, &authorizationTeam,
		&sourceID, &sourceRevision, &sourceProvider, &sourceProfile, &sourceProtocol, &sourceProtocolVersion, &sourceType, &sourceClientCompatibility, &sourceStatus, &sourceSchedulable, &sourceExpires, &sourceCooldown, &sourceError, &sourceCredentials,
		&groupID, &bindingAuthorizationID,
		&proxyID, &proxyEnabled, &proxyType, &proxyHost, &proxyPort, &proxyUsername, &proxyPassword,
	); err != nil {
		return directCandidate{}, fmt.Errorf("解码 PG direct input 候选失败: %w", err)
	}
	result.account.ProtocolProfileID = accountProfile.String
	result.account.ProtocolCode = accountProtocol.String
	result.account.ProtocolVersion = accountProtocolVersion.String
	result.account.ClientCompatibility = accountClientCompatibility.String
	result.account.MappedUpstreamModel = mappedUpstreamModel.String
	result.account.MappedUpstreamEndpointFamily = mappedUpstreamFamily.String
	result.account.Schedulable = schedulable.Valid && schedulable.Int64 == 1
	result.account.TemporaryUnavailableContinuousProbeEnabled = continuousProbe.Valid && continuousProbe.Bool
	var err error
	if result.account.AccountExpiresAt, err = parseNullableDirectTime(accountExpires); err != nil {
		return directCandidate{}, err
	}
	if result.account.CooldownUntil, err = parseNullableDirectTime(cooldownUntil); err != nil {
		return directCandidate{}, err
	}
	if observationStarted.Valid || cooldownGeneration.Valid {
		observed, err := parseRequiredDirectTime(observationStarted, "cooldown observation")
		if err != nil || !cooldownGeneration.Valid || strings.TrimSpace(cooldownGeneration.String) == "" {
			return directCandidate{}, fmt.Errorf("PG direct input 的 cooldown fence 无效")
		}
		result.account.Cooldown = &CooldownFence{ObservationStartedAt: observed, Generation: cooldownGeneration.String}
	}
	result.binding = DirectBinding{GroupID: groupID.String, Enabled: groupID.Valid, AuthorizationBindingID: bindingAuthorizationID.String}
	if authorizationID.Valid {
		expiresAt, err := parseNullableDirectTime(authorizationExpires)
		if err != nil {
			return directCandidate{}, err
		}
		result.authorization = &DirectAuthorization{ID: authorizationID.String, Status: authorizationStatus.String, ExpiresAt: expiresAt}
		result.authorizationLimits = authorizationLimits.String
		result.authorizationResourceID = authorizationResourceID.String
		result.authorizationOwner = authorizationOwner.String
		result.authorizationTeam = authorizationTeam.String
	}
	if sourceID.Valid {
		expiresAt, err := parseNullableDirectTime(sourceExpires)
		if err != nil {
			return directCandidate{}, err
		}
		cooldownAt, err := parseNullableDirectTime(sourceCooldown)
		if err != nil {
			return directCandidate{}, err
		}
		result.source = &DirectSource{ID: sourceID.String, ConfigRevision: sourceRevision.Int64, Provider: sourceProvider.String, ProtocolProfileID: sourceProfile.String, ProtocolCode: sourceProtocol.String, ProtocolVersion: sourceProtocolVersion.String, Type: sourceType.String, ClientCompatibility: sourceClientCompatibility.String, Status: sourceStatus.String, Schedulable: sourceSchedulable.Valid && sourceSchedulable.Int64 == 1, AccountExpiresAt: expiresAt, CooldownUntil: cooldownAt, LastErrorCode: sourceError.String, CredentialsEncrypted: sourceCredentials.String}
		if result.account.Cooldown != nil {
			value := result.source.ConfigRevision
			result.account.Cooldown.SourceConfigRevision = &value
		}
	}
	if proxyID.Valid {
		result.proxy = &DirectProxy{ID: proxyID.String, Enabled: proxyEnabled.Bool, Type: proxyType.String, Host: proxyHost.String, Port: int(proxyPort.Int64), Username: proxyUsername.String, PasswordEncrypted: proxyPassword.String}
	}
	return result, nil
}

func loadDirectSchedule(ctx context.Context, tx *sql.Tx) (Schedule, *time.Location, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key, value_json FROM juhe_business.system_settings WHERE system_account_id = 'sys_admin' AND key IN ('accountHealthCheckIntervalHours', 'accountHealthCheckJitterMinutes', 'accountHealthCheckFailureThreshold', 'defaultTemporaryUnschedulableMinutes', 'cooldownAccountRetestMaxBackoffHours', 'usageStatsTimezone')`)
	if err != nil {
		return Schedule{}, nil, fmt.Errorf("读取 PG direct input settings 失败: %w", err)
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return Schedule{}, nil, err
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return Schedule{}, nil, err
	}
	intervalHours, err := directSettingInt(values, "accountHealthCheckIntervalHours", 1, 168)
	if err != nil {
		return Schedule{}, nil, err
	}
	jitterMinutes, err := directSettingInt(values, "accountHealthCheckJitterMinutes", 0, 1440)
	if err != nil {
		return Schedule{}, nil, err
	}
	threshold, err := directSettingInt(values, "accountHealthCheckFailureThreshold", 1, 10)
	if err != nil {
		return Schedule{}, nil, err
	}
	maxPauseMinutes, err := directSettingInt(values, "defaultTemporaryUnschedulableMinutes", 1, 1440)
	if err != nil {
		return Schedule{}, nil, err
	}
	maxRecoveryHours, err := directSettingInt(values, "cooldownAccountRetestMaxBackoffHours", 1, 24*30)
	if err != nil {
		return Schedule{}, nil, err
	}
	var timezone string
	if raw, found := values["usageStatsTimezone"]; !found || json.Unmarshal([]byte(raw), &timezone) != nil || strings.TrimSpace(timezone) == "" {
		return Schedule{}, nil, fmt.Errorf("PG direct input 缺少有效 usageStatsTimezone")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Schedule{}, nil, fmt.Errorf("PG direct input usageStatsTimezone 无效: %w", err)
	}
	return Schedule{HealthIntervalMS: int64(intervalHours) * int64(time.Hour/time.Millisecond), HealthJitterMS: int64(jitterMinutes) * int64(time.Minute/time.Millisecond), FailureThreshold: threshold, FailureRetryMS: int64(5 * time.Minute / time.Millisecond), CooldownNeutralBaseMS: int64(30 * time.Second / time.Millisecond), CooldownNeutralMaxMS: int64(15 * time.Minute / time.Millisecond), CooldownFailureBackoffMS: int64(3 * time.Second / time.Millisecond), MaxPauseMinutes: maxPauseMinutes, MaxRecoveryHours: maxRecoveryHours}, location, nil
}

func directSettingInt(values map[string]string, key string, minimum, maximum int) (int, error) {
	raw, found := values[key]
	if !found {
		return 0, fmt.Errorf("PG direct input 缺少系统设置 %s", key)
	}
	var value int
	if err := json.Unmarshal([]byte(raw), &value); err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("PG direct input 系统设置 %s 无效", key)
	}
	return value, nil
}

func authorizationQuotaEligible(ctx context.Context, tx *sql.Tx, candidate directCandidate, now time.Time, location *time.Location) (bool, error) {
	if candidate.authorization == nil {
		return true, nil
	}
	limits, err := ParseDirectQuotaLimits(candidate.authorizationLimits)
	if err != nil {
		return false, err
	}
	costs, err := loadDirectQuotaCosts(ctx, tx, candidate.systemAccount, "account_authorization", candidate.authorization.ID, limits, now, location)
	if err != nil {
		return false, err
	}
	if DirectQuotaExceeded(limits, costs) {
		return false, nil
	}
	if strings.TrimSpace(candidate.authorizationTeam) == "" {
		return true, nil
	}
	var grantLimitsRaw sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT limits_json FROM juhe_business.resource_authorization_grants
WHERE resource_type = 'account' AND resource_id = $1 AND resource_owner_system_account_id = $2
  AND grantee_type = 'team' AND grantee_team_id = $3 AND status = 'active'
  AND (expires_at IS NULL OR expires_at > $4)
ORDER BY updated_at DESC LIMIT 1`, candidate.authorizationResourceID, candidate.authorizationOwner, candidate.authorizationTeam, now.Format(time.RFC3339Nano)).Scan(&grantLimitsRaw)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("读取 PG direct input authorization team grant 失败: %w", err)
	}
	grantLimits, err := ParseDirectQuotaLimits(grantLimitsRaw.String)
	if err != nil {
		return false, err
	}
	teamCosts, err := loadDirectQuotaCosts(ctx, tx, candidate.systemAccount, "account_authorization_team", candidate.authorization.ID+":"+candidate.authorizationTeam, grantLimits, now, location)
	if err != nil {
		return false, err
	}
	return !DirectQuotaExceeded(grantLimits, teamCosts), nil
}

func loadDirectQuotaCosts(ctx context.Context, tx *sql.Tx, systemAccount, scopeType, scopeID string, limits DirectQuotaLimits, now time.Time, location *time.Location) (DirectQuotaCosts, error) {
	local := now.In(location)
	dateKey := local.Format("2006-01-02")
	monthKey := local.Format("2006-01")
	weekday := int(local.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	weekKey := local.AddDate(0, 0, 1-weekday).Format("2006-01-02")
	result := DirectQuotaCosts{}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(total_cost_usd, 0) FROM juhe_stats.usage_stats_totals WHERE system_account_id=$1 AND scope_type=$2 AND scope_id=$3`, systemAccount, scopeType, scopeID).Scan(&result.Total); err != nil && err != sql.ErrNoRows {
		return DirectQuotaCosts{}, fmt.Errorf("读取 PG direct input total quota cost 失败: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(total_cost_usd, 0) FROM juhe_stats.usage_stats_daily WHERE system_account_id=$1 AND scope_type=$2 AND scope_id=$3 AND stat_date=$4`, systemAccount, scopeType, scopeID, dateKey).Scan(&result.Daily); err != nil && err != sql.ErrNoRows {
		return DirectQuotaCosts{}, fmt.Errorf("读取 PG direct input daily quota cost 失败: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(total_cost_usd, 0) FROM juhe_stats.usage_stats_weekly WHERE system_account_id=$1 AND scope_type=$2 AND scope_id=$3 AND stat_week=$4`, systemAccount, scopeType, scopeID, weekKey).Scan(&result.Weekly); err != nil && err != sql.ErrNoRows {
		return DirectQuotaCosts{}, fmt.Errorf("读取 PG direct input weekly quota cost 失败: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(total_cost_usd, 0) FROM juhe_stats.usage_stats_monthly WHERE system_account_id=$1 AND scope_type=$2 AND scope_id=$3 AND stat_month=$4`, systemAccount, scopeType, scopeID, monthKey).Scan(&result.Monthly); err != nil && err != sql.ErrNoRows {
		return DirectQuotaCosts{}, fmt.Errorf("读取 PG direct input monthly quota cost 失败: %w", err)
	}
	if limits.Hourly != nil && limits.Hourly.Enabled {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(total_cost_usd, 0) FROM juhe_stats.usage_quota_hourly_windows WHERE system_account_id=$1 AND scope_type=$2 AND scope_id=$3 AND window_hours=$4`, systemAccount, scopeType, scopeID, limits.Hourly.Hours).Scan(&result.Hourly); err != nil && err != sql.ErrNoRows {
			return DirectQuotaCosts{}, fmt.Errorf("读取 PG direct input hourly quota cost 失败: %w", err)
		}
	}
	return result, nil
}

func parseNullableDirectTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, fmt.Errorf("解析 PG direct input 时间失败")
	}
	return &parsed, nil
}

func parseRequiredDirectTime(value sql.NullString, field string) (time.Time, error) {
	parsed, err := parseNullableDirectTime(value)
	if err != nil || parsed == nil {
		return time.Time{}, fmt.Errorf("PG direct input 缺少有效 %s", field)
	}
	return *parsed, nil
}

func parseDirectBool(value sql.NullString) (bool, error) {
	if !value.Valid {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value.String)
	if err != nil {
		return false, err
	}
	return parsed, nil
}
