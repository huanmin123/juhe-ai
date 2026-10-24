package legacybusinessimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

// accountRow is one legacy account plus the vendor/preset translation resolved
// from its provider_code and provider_protocol_profile_id.
type accountRow struct {
	ID               string
	ConfigRevision   int
	DispatchRevision int
	CircuitRevision  int
	Owner            string
	ProviderCode     string
	ProfileID        string
	Vendor           sql.NullString
	Driver           string
	Preset           string
	AccountType      string
	ProtocolCode     string
	ProtocolVersion  string
	Name             string
	Status           string
	Credential       string
	CredentialMask   string
	OAuthExpires     sql.NullString
	OAuthRefresh     int
	ProxyProfile     sql.NullString
	ConcurrencyLimit int
	Priority         int
	SuperPriority    int
	FallbackEnabled  int
	Schedulable      int
	AvailabilityJSON sql.NullString
	AvailabilityNext sql.NullString
	Notes            sql.NullString
	AccountExpires   sql.NullString
	LastUsed         sql.NullString
	CooldownUntil    sql.NullString
	LastErrorCode    sql.NullString
	LastErrorMessage sql.NullString
	LastErrorTrace   sql.NullString
	CooldownRetest   int
	CooldownStarted  sql.NullString
	CooldownGen      sql.NullString
	CooldownLastAt   sql.NullString
	CooldownLastCode sql.NullInt64
	TempProbe        int
	Expedited        int
	Domain           string
	HealthModel      sql.NullString
	HealthMode       sql.NullString
	LastHealthAt     sql.NullString
	NextHealthAt     sql.NullString
	LastSuccessAt    sql.NullString
	HealthFailures   int
	HealthFailStart  sql.NullString
	LastHealthCode   sql.NullInt64
	LastHealthErr    sql.NullString
	LastHealthErrMsg sql.NullString
	LastHealthTrace  sql.NullString
	StreamFailures   int
	StreamFailStart  sql.NullString
	BalanceEnabled   int
	BalanceConfig    string
	BalanceNext      sql.NullString
	CreatedAt        string
	UpdatedAt        string
}

// importAccounts is the heart of the rebuild: every account is re-expressed in
// the normalised target shape (vendor + driver + a connection row holding the
// base URL and endpoint modes), and its model capability set is re-pointed at
// target model ids. Accounts whose model list references a model the catalog
// does not know get that model created as a personal row, preserving the
// relay-style custom names production actually uses.
func importAccounts(ctx context.Context, source *sql.DB, tx *sql.Tx, secret string, report *Report) (map[string]struct{}, error) {
	rows, err := source.QueryContext(ctx, `
		SELECT id, config_revision, dispatch_revision, circuit_projection_revision,
		       system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		       name, type, status, credentials_encrypted, credential_mask, oauth_access_token_expires_at,
		       oauth_refresh_token_present, proxy_profile_id, concurrency_limit, priority, super_priority_enabled,
		       fallback_enabled, schedulable, availability_schedule_json, availability_schedule_next_check_at,
		       notes, account_expires_at, last_used_at, cooldown_until, last_error_code, last_error_message,
		       last_error_trace_id, cooldown_retest_failure_count, cooldown_retest_observation_started_at,
		       cooldown_retest_generation, cooldown_retest_last_at, cooldown_retest_last_status_code,
		       temporary_unavailable_continuous_probe_enabled, expedited_recovery_enabled, capability_domain,
		       health_check_model, health_check_endpoint_mode, last_health_check_at, next_health_check_at,
		       last_health_success_at, health_check_failure_count, health_check_failure_started_at,
		       last_health_check_status_code, last_health_check_error_code, last_health_check_error_message,
		       last_health_check_trace_id, stream_failure_count, stream_failure_window_started_at,
		       balance_query_enabled, balance_query_config_json, balance_query_next_refresh_at, created_at, updated_at
		FROM juhe_business.accounts
		WHERE deleted_at IS NULL
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("读取旧账户: %w", err)
	}
	defer rows.Close()

	var collected []accountRow
	for rows.Next() {
		var row accountRow
		if err := rows.Scan(&row.ID, &row.ConfigRevision, &row.DispatchRevision, &row.CircuitRevision,
			&row.Owner, &row.ProviderCode, &row.ProfileID, &row.ProtocolCode, &row.ProtocolVersion,
			&row.Name, &row.AccountType, &row.Status, &row.Credential, &row.CredentialMask, &row.OAuthExpires,
			&row.OAuthRefresh, &row.ProxyProfile, &row.ConcurrencyLimit, &row.Priority, &row.SuperPriority,
			&row.FallbackEnabled, &row.Schedulable, &row.AvailabilityJSON, &row.AvailabilityNext,
			&row.Notes, &row.AccountExpires, &row.LastUsed, &row.CooldownUntil, &row.LastErrorCode,
			&row.LastErrorMessage, &row.LastErrorTrace, &row.CooldownRetest, &row.CooldownStarted,
			&row.CooldownGen, &row.CooldownLastAt, &row.CooldownLastCode, &row.TempProbe, &row.Expedited,
			&row.Domain, &row.HealthModel, &row.HealthMode, &row.LastHealthAt, &row.NextHealthAt,
			&row.LastSuccessAt, &row.HealthFailures, &row.HealthFailStart, &row.LastHealthCode,
			&row.LastHealthErr, &row.LastHealthErrMsg, &row.LastHealthTrace, &row.StreamFailures,
			&row.StreamFailStart, &row.BalanceEnabled, &row.BalanceConfig, &row.BalanceNext,
			&row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("扫描旧账户: %w", err)
		}
		if err := resolveAccountTarget(&row, report); err != nil {
			return nil, err
		}
		collected = append(collected, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历旧账户: %w", err)
	}

	accountIDs := make(map[string]struct{}, len(collected))
	for _, row := range collected {
		credit, err := decodeCredit(secret, row.Credential)
		if err != nil {
			return nil, fmt.Errorf("解密账户 %s 凭据失败（生产 JUHE_AI_SECRET 是否正确）: %w", row.ID, err)
		}
		baseURL := strings.TrimSpace(credit.BaseURL)
		if baseURL == "" {
			baseURL = presetDefaultBaseURL[row.Preset]
		}
		if baseURL == "" {
			// generic_* 没有 registry 默认地址：凭据里也没有时 fail-closed，
			// 绝不生成占位 host（否则账户会静默指向错误上游）。
			return nil, fmt.Errorf("账户 %s（preset %s）凭据缺少 base_url，无法确定上游地址", row.ID, row.Preset)
		}
		modes := effectiveModes(row.Preset, credit.SupportedEndpointModes)
		if len(normalizeModes(row.Preset, credit.SupportedEndpointModes)) == 0 {
			report.UnresolvedEndpointModes = append(report.UnresolvedEndpointModes, row.ID)
		}

		// 逐列构造 INSERT：列名与值同源，避免手写占位符与参数漂移。
		columns := []string{
			"id", "config_revision", "dispatch_revision", "circuit_projection_revision", "system_account_id",
			"vendor_code", "connection_driver_code", "oauth_type", "protocol_code", "protocol_version",
			"name", "type", "status", "credentials_encrypted", "credential_fingerprint", "credential_mask",
			"oauth_access_token_expires_at", "oauth_refresh_token_present", "proxy_profile_id",
			"concurrency_limit", "priority", "super_priority_enabled", "fallback_enabled", "schedulable",
			"availability_schedule_json", "availability_schedule_next_check_at", "notes", "account_expires_at",
			"last_used_at", "cooldown_until", "last_error_code", "last_error_message", "last_error_trace_id",
			"cooldown_retest_failure_count", "cooldown_retest_observation_started_at", "cooldown_retest_generation",
			"cooldown_retest_last_at", "cooldown_retest_last_status_code",
			"temporary_unavailable_continuous_probe_enabled", "expedited_recovery_enabled", "capability_domain",
			"health_check_connection_id", "health_check_model", "health_check_endpoint_mode", "last_health_check_at",
			"next_health_check_at", "last_health_success_at", "health_check_failure_count",
			"health_check_failure_started_at", "last_health_check_status_code", "last_health_check_error_code",
			"last_health_check_error_message", "last_health_check_trace_id", "stream_failure_count",
			"stream_failure_window_started_at", "balance_query_enabled", "balance_query_config_json",
			"balance_query_next_refresh_at", "created_at", "updated_at",
		}
		values := []any{
			row.ID, maxInt(row.ConfigRevision, 1), maxInt(row.DispatchRevision, 1), row.CircuitRevision, row.Owner,
			row.Vendor, row.Driver, nil, row.ProtocolCode, row.ProtocolVersion,
			row.Name, row.AccountType, row.Status, row.Credential, credentialFingerprint(row.Credential), row.CredentialMask,
			row.OAuthExpires, row.OAuthRefresh, row.ProxyProfile,
			row.ConcurrencyLimit, row.Priority, row.SuperPriority, row.FallbackEnabled, row.Schedulable,
			row.AvailabilityJSON, row.AvailabilityNext, row.Notes, row.AccountExpires,
			row.LastUsed, row.CooldownUntil, row.LastErrorCode, row.LastErrorMessage, row.LastErrorTrace,
			row.CooldownRetest, row.CooldownStarted, row.CooldownGen,
			row.CooldownLastAt, row.CooldownLastCode,
			row.TempProbe, row.Expedited, row.Domain,
			nil, row.HealthModel, row.HealthMode, row.LastHealthAt,
			row.NextHealthAt, row.LastSuccessAt, row.HealthFailures,
			row.HealthFailStart, row.LastHealthCode, row.LastHealthErr,
			row.LastHealthErrMsg, row.LastHealthTrace, row.StreamFailures,
			row.StreamFailStart, row.BalanceEnabled, row.BalanceConfig,
			row.BalanceNext, row.CreatedAt, row.UpdatedAt,
		}
		if len(columns) != len(values) {
			return nil, fmt.Errorf("账户 %s 列/值数量不一致: %d vs %d", row.ID, len(columns), len(values))
		}
		placeholders := make([]string, len(values))
		for index := range values {
			placeholders[index] = fmt.Sprintf("$%d", index+1)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO juhe_business.accounts (`+strings.Join(columns, ",")+`) VALUES (`+strings.Join(placeholders, ",")+`)`,
			values...); err != nil {
			return nil, fmt.Errorf("写入账户 %s: %w", row.ID, err)
		}
		connectionID := "conn_" + row.ID
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.account_connections
				(id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,1,$6,$7)`,
			connectionID, row.ID, row.Preset, nullableSurface(row.Preset), baseURL, row.CreatedAt, row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("写入账户 %s 连接: %w", row.ID, err)
		}
		for _, mode := range modes {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO juhe_business.account_endpoint_modes (connection_id, endpoint_mode)
				VALUES ($1,$2) ON CONFLICT DO NOTHING`, connectionID, mode); err != nil {
				return nil, fmt.Errorf("写入账户 %s endpoint mode %s: %w", row.ID, mode, err)
			}
		}
		// health_check_connection_id 指向本轮新建的连接（健康检查三件套必须同源
		// 于一条真实连接，否则启动契约拒绝该账户）。
		if row.HealthMode.Valid && row.HealthModel.Valid {
			if _, err := tx.ExecContext(ctx, `
				UPDATE juhe_business.accounts SET health_check_connection_id = $1 WHERE id = $2`,
				connectionID, row.ID); err != nil {
				return nil, fmt.Errorf("写入账户 %s 健康检查连接: %w", row.ID, err)
			}
		}

		accountIDs[row.ID] = struct{}{}
	}
	report.Tables["accounts"] = len(collected)
	report.CreatedAccounts = len(collected)

	accountOwners := make(map[string]string, len(collected))
	for _, row := range collected {
		accountOwners[row.ID] = row.Owner
	}
	if err := importSupportedModels(ctx, source, tx, accountIDs, accountOwners, report); err != nil {
		return nil, err
	}
	if err := importMappings(ctx, source, tx, accountIDs, accountOwners, report); err != nil {
		return nil, err
	}
	return accountIDs, nil
}

// resolveAccountTarget validates the legacy provider/profile pair and fills the
// target vendor, driver and preset. openai/hybrid stay generic on purpose.
func resolveAccountTarget(row *accountRow, report *Report) error {
	preset, ok := legacyProfilePresets[row.ProfileID]
	if !ok {
		return fmt.Errorf("账户 %s 的旧 profile %q 不在固定映射表中，无法确定连接 preset", row.ID, row.ProfileID)
	}
	driver, ok := presetDriver(preset)
	if !ok {
		return fmt.Errorf("账户 %s 的 preset %q 没有目标 driver", row.ID, preset)
	}
	expectedVendor := presetVendor(preset)
	if expectedVendor != "" {
		if row.ProviderCode != expectedVendor {
			return fmt.Errorf("账户 %s 的 provider_code %q 与 profile %q 推导的 vendor %q 不一致",
				row.ID, row.ProviderCode, row.ProfileID, expectedVendor)
		}
		row.Vendor = sql.NullString{String: expectedVendor, Valid: true}
		row.Driver = driver
	} else {
		// generic_http账户：vendor 为 NULL，driver 固定 generic_http。
		row.Vendor = sql.NullString{}
		row.Driver = "generic_http"
	}
	row.Preset = preset
	report.LegacyProfiles[row.ProfileID] = preset
	return nil
}

func maxInt(value, floor int) int {
	if value < floor {
		return floor
	}
	return value
}

func nullableSurface(preset string) sql.NullString {
	if surface := presetSurfaces[preset]; surface != "" {
		return sql.NullString{String: surface, Valid: true}
	}
	return sql.NullString{}
}

// credentialFingerprint mirrors the runtime convention: SHA-256 of the envelope
// bytes. It is a non-sensitive identity, never the credential itself.
func credentialFingerprint(envelope string) string {
	sum := sha256.Sum256([]byte(envelope))
	return hex.EncodeToString(sum[:])
}

// importSupportedModels re-points each account's legacy (provider, model) pairs
// at target model ids, creating a personal model row when the builtin catalog
// has no match (relay deployments routinely use vendor-prefixed custom names).
func importSupportedModels(ctx context.Context, source *sql.DB, tx *sql.Tx, accounts map[string]struct{}, owners map[string]string, report *Report) error {
	models, err := loadModelIndex(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := source.QueryContext(ctx, `
		SELECT account_id, provider_code, model, created_at FROM juhe_business.account_supported_models`)
	if err != nil {
		return fmt.Errorf("读取旧账户模型: %w", err)
	}
	defer rows.Close()
	type pending struct {
		accountID, provider, model, createdAt string
	}
	var collected []pending
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.accountID, &item.provider, &item.model, &item.createdAt); err != nil {
			return fmt.Errorf("扫描旧账户模型: %w", err)
		}
		collected = append(collected, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历旧账户模型: %w", err)
	}

	count := 0
	created := 0
	for _, item := range collected {
		if _, ok := accounts[item.accountID]; !ok {
			continue
		}
		vendor := vendorForLegacyProvider(item.provider)
		modelID, ok := models.lookup(vendor, item.model)
		if !ok {
			newID, err := createPersonalModel(ctx, tx, owners[item.accountID], vendor, item.model)
			if err != nil {
				return err
			}
			models.add(vendor, item.model, newID)
			modelID = newID
			created++
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.account_supported_models (account_id, model_id, created_at)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, item.accountID, modelID, item.createdAt); err != nil {
			return fmt.Errorf("写入账户 %s 模型 %s: %w", item.accountID, modelID, err)
		}
		count++
	}
	report.Tables["account_supported_models"] = count
	report.Tables["models_created"] = created
	if created > 0 {
		report.Notes = append(report.Notes,
			fmt.Sprintf("模型目录：%d 个旧账户模型名（中转站自定义名）在新目录中不存在，已按 personal scope 补建", created))
	}
	return nil
}

// importMappings rewrites legacy account_model_mappings into
// account_entry_mappings: family-only legacy rows lose their upstream model and
// are re-pointed at the target connection plus the resolved target model.
func importMappings(ctx context.Context, source *sql.DB, tx *sql.Tx, accounts map[string]struct{}, owners map[string]string, report *Report) error {
	models, err := loadModelIndex(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := source.QueryContext(ctx, `
		SELECT account_id, provider_code, source_model, source_endpoint_family, upstream_model,
		       upstream_endpoint_family, enabled, created_at, updated_at
		FROM juhe_business.account_model_mappings`)
	if err != nil {
		return fmt.Errorf("读取旧模型映射: %w", err)
	}
	defer rows.Close()
	type pending struct {
		accountID, provider, sourceModel, sourceFamily, upstreamModel, upstreamFamily, createdAt, updatedAt string
		enabled                                                                                             int
	}
	var collected []pending
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.accountID, &item.provider, &item.sourceModel, &item.sourceFamily,
			&item.upstreamModel, &item.upstreamFamily, &item.enabled, &item.createdAt, &item.updatedAt); err != nil {
			return fmt.Errorf("扫描旧模型映射: %w", err)
		}
		collected = append(collected, item)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历旧模型映射: %w", err)
	}

	count := 0
	for index, item := range collected {
		if _, ok := accounts[item.accountID]; !ok {
			continue
		}
		vendor := vendorForLegacyProvider(item.provider)
		modelID, ok := models.lookup(vendor, item.upstreamModel)
		if !ok {
			newID, err := createPersonalModel(ctx, tx, owners[item.accountID], vendor, item.upstreamModel)
			if err != nil {
				return err
			}
			models.add(vendor, item.upstreamModel, newID)
			modelID = newID
		}
		// 目标模型必须先出现在该账户的 supported_models 中：映射表有
		// (account_id, target_model_id) → account_supported_models 的复合外键。
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.account_supported_models (account_id, model_id, created_at)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, item.accountID, modelID, item.createdAt); err != nil {
			return fmt.Errorf("映射目标模型补登记 %s/%s: %w", item.accountID, modelID, err)
		}
		connectionID := "conn_" + item.accountID
		mappingID := fmt.Sprintf("map_%s_%d", item.accountID, index)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.account_entry_mappings
				(id, account_id, source_model, source_endpoint_family, target_connection_id, target_model_id,
				 target_endpoint_family, enabled, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			mappingID, item.accountID, item.sourceModel, item.sourceFamily, connectionID, modelID,
			item.upstreamFamily, item.enabled, item.createdAt, item.updatedAt); err != nil {
			return fmt.Errorf("写入模型映射 %s: %w", mappingID, err)
		}
		count++
	}
	report.Tables["account_entry_mappings"] = count
	return nil
}

// modelIndex maps (vendor, model name) to a target models.id. Lookup tries the
// upstream name first and falls back to the public name, matching how relays
// name their custom models.
type modelIndex struct {
	byUpstream map[string]string
	byPublic   map[string]string
}

func modelKey(vendor, name string) string { return vendor + "\x00" + name }

func loadModelIndex(ctx context.Context, tx *sql.Tx) (*modelIndex, error) {
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(vendor_code,''), upstream_model, public_model, id FROM juhe_business.models`)
	if err != nil {
		return nil, fmt.Errorf("读取目标模型目录: %w", err)
	}
	defer rows.Close()
	index := &modelIndex{byUpstream: map[string]string{}, byPublic: map[string]string{}}
	for rows.Next() {
		var vendor, upstream, public, id string
		if err := rows.Scan(&vendor, &upstream, &public, &id); err != nil {
			return nil, fmt.Errorf("扫描目标模型目录: %w", err)
		}
		index.byUpstream[modelKey(vendor, upstream)] = id
		if _, exists := index.byPublic[modelKey(vendor, public)]; !exists {
			index.byPublic[modelKey(vendor, public)] = id
		}
	}
	return index, rows.Err()
}

func (m *modelIndex) lookup(vendor, name string) (string, bool) {
	if id, ok := m.byUpstream[modelKey(vendor, name)]; ok {
		return id, true
	}
	id, ok := m.byPublic[modelKey(vendor, name)]
	return id, ok
}

func (m *modelIndex) add(vendor, name, id string) {
	m.byUpstream[modelKey(vendor, name)] = id
}

// createPersonalModel inserts a personal-scope model row for an owner so a
// relay custom model name keeps working after the rebuild.
func createPersonalModel(ctx context.Context, tx *sql.Tx, owner, vendor, name string) (string, error) {
	sum := sha256.Sum256([]byte(owner + "\x00" + vendor + "\x00" + name))
	id := "mdl_" + hex.EncodeToString(sum[:10])
	modes := `["chat_json","chat_sse"]`
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO juhe_business.models
			(id, vendor_code, public_model, upstream_model, scope, system_account_id, status, catalog_visible,
			 supported_endpoint_modes_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			 codex_supported_reasoning_levels_json, service_tier_prices_json, long_context_input_token_threshold_inclusive,
			 supports_prompt_caching, currency, created_at, updated_at)
		VALUES ($1,$2,$3,$3,'personal',$4,'active',false,$5,'[]','[]','[]','{}',false,false,'USD',$6,$6)`,
		id, nullableVendor(vendor), name, owner, modes, nowText()); err != nil {
		return "", fmt.Errorf("补建 personal 模型 %s/%s: %w", vendor, name, err)
	}
	return id, nil
}

func nullableVendor(vendor string) sql.NullString {
	if vendor == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: vendor, Valid: true}
}

// vendorForLegacyProvider maps a legacy provider_code to the target vendor. The
// generic pseudo-vendors (openai/hybrid) map to the empty (NULL) vendor.
func vendorForLegacyProvider(provider string) string {
	if vendor, ok := realVendors[provider]; ok {
		return vendor
	}
	return ""
}

// importGroupAccounts binds accounts to groups, skipping bindings whose account
// was soft-deleted in the legacy database.
func importGroupAccounts(ctx context.Context, source *sql.DB, tx *sql.Tx, groups, accounts, owners map[string]struct{}, report *Report) error {
	rows, err := source.QueryContext(ctx, `
		SELECT system_account_id, group_id, account_id, account_authorization_id, local_priority,
		       local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at
		FROM juhe_business.group_accounts`)
	if err != nil {
		return fmt.Errorf("读取旧分组绑定: %w", err)
	}
	defer rows.Close()
	count := 0
	skipped := 0
	for rows.Next() {
		var owner, groupID, accountID, createdAt, updatedAt string
		var authorization sql.NullString
		var localPriority, superPriority, fallback, enabled int
		if err := rows.Scan(&owner, &groupID, &accountID, &authorization, &localPriority, &superPriority,
			&fallback, &enabled, &createdAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧分组绑定: %w", err)
		}
		if _, ok := accounts[accountID]; !ok {
			skipped++
			continue
		}
		if _, ok := groups[groupID]; !ok {
			return fmt.Errorf("分组绑定引用了未迁移的分组 %s", groupID)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.group_accounts
				(system_account_id, group_id, account_id, account_authorization_id, local_priority,
				 local_super_priority_enabled, local_fallback_enabled, enabled, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			owner, groupID, accountID, authorization, localPriority, superPriority, fallback, enabled,
			createdAt, updatedAt); err != nil {
			return fmt.Errorf("写入分组绑定 %s/%s: %w", groupID, accountID, err)
		}
		count++
	}
	report.Tables["group_accounts"] = count
	if skipped > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf("分组绑定：%d 条指向已软删账户，已跳过", skipped))
	}
	return rows.Err()
}

// importAuthorizations carries resource authorizations, their source rows and
// the per-group override settings.
func importAuthorizations(ctx context.Context, source *sql.DB, tx *sql.Tx, groups, accounts, owners map[string]struct{}, report *Report) error {
	rows, err := source.QueryContext(ctx, `
		SELECT id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
		       scope, status, effective_source_type, effective_source_team_id, activated_at,
		       last_source_changed_at, remark, expires_at, limits_json, created_by, created_at,
		       revoked_by, revoked_at, revoked_reason, updated_at
		FROM juhe_business.resource_authorizations`)
	if err != nil {
		return fmt.Errorf("读取旧授权: %w", err)
	}
	defer rows.Close()
	type kept struct{ id, resourceType, resourceID string }
	var keptRows []kept
	for rows.Next() {
		var id, resourceType, resourceID, owner, grantee, scope, status, createdBy, createdAt, updatedAt string
		var effectiveType, effectiveTeam, activatedAt, lastSourceChanged, remark, expiresAt, limits, revokedBy,
			revokedAt, revokedReason sql.NullString
		if err := rows.Scan(&id, &resourceType, &resourceID, &owner, &grantee, &scope, &status,
			&effectiveType, &effectiveTeam, &activatedAt, &lastSourceChanged, &remark, &expiresAt, &limits,
			&createdBy, &createdAt, &revokedBy, &revokedAt, &revokedReason, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧授权: %w", err)
		}
		switch resourceType {
		case "group":
			if _, ok := groups[resourceID]; !ok {
				continue
			}
		case "account":
			if _, ok := accounts[resourceID]; !ok {
				continue
			}
		default:
			return fmt.Errorf("授权 %s 使用了未知资源类型 %q", id, resourceType)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.resource_authorizations
				(id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
				 scope, status, effective_source_type, effective_source_team_id, activated_at,
				 last_source_changed_at, remark, expires_at, limits_json, created_by, created_at,
				 revoked_by, revoked_at, revoked_reason, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
			id, resourceType, resourceID, owner, grantee, scope, status, effectiveType, effectiveTeam,
			activatedAt, lastSourceChanged, remark, expiresAt, limits, createdBy, createdAt,
			revokedBy, revokedAt, revokedReason, updatedAt); err != nil {
			return fmt.Errorf("写入授权 %s: %w", id, err)
		}
		keptRows = append(keptRows, kept{id: id, resourceType: resourceType, resourceID: resourceID})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历旧授权: %w", err)
	}
	report.Tables["resource_authorizations"] = len(keptRows)

	keptIDs := make(map[string]struct{}, len(keptRows))
	for _, row := range keptRows {
		keptIDs[row.id] = struct{}{}
	}

	sourceRows, err := source.QueryContext(ctx, `
		SELECT id, authorization_id, source_type, source_team_id, status, activated_at, ended_at, ended_reason,
		       created_by, created_at, revoked_by, revoked_at, updated_at
		FROM juhe_business.resource_authorization_sources`)
	if err != nil {
		return fmt.Errorf("读取旧授权来源: %w", err)
	}
	defer sourceRows.Close()
	sources := 0
	for sourceRows.Next() {
		var id, authorizationID, sourceType, status, createdBy, createdAt, updatedAt string
		var teamID, activatedAt, endedAt, endedReason, revokedBy, revokedAt sql.NullString
		if err := sourceRows.Scan(&id, &authorizationID, &sourceType, &teamID, &status, &activatedAt,
			&endedAt, &endedReason, &createdBy, &createdAt, &revokedBy, &revokedAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧授权来源: %w", err)
		}
		if _, ok := keptIDs[authorizationID]; !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.resource_authorization_sources
				(id, authorization_id, source_type, source_team_id, status, activated_at, ended_at,
				 ended_reason, created_by, created_at, revoked_by, revoked_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			id, authorizationID, sourceType, teamID, status, activatedAt, endedAt, endedReason,
			createdBy, createdAt, revokedBy, revokedAt, updatedAt); err != nil {
			return fmt.Errorf("写入授权来源 %s: %w", id, err)
		}
		sources++
	}
	report.Tables["resource_authorization_sources"] = sources
	if err := sourceRows.Err(); err != nil {
		return fmt.Errorf("遍历旧授权来源: %w", err)
	}
	return importAuthorizationGrants(ctx, source, tx, groups, accounts, report)
}

// importAuthorizationGrants carries the authorization business master rows the
// 授权操作 list reads and the team cascades drive off. The resource filter
// mirrors the runtime rows: a grant whose resource did not survive the
// groups/accounts import is dropped alongside its runtime row, while grants of
// kept resources carry over even when their runtime row is already gone (a
// terminal grant is business history worth keeping).
func importAuthorizationGrants(ctx context.Context, source *sql.DB, tx *sql.Tx, groups, accounts map[string]struct{}, report *Report) error {
	rows, err := source.QueryContext(ctx, `
		SELECT id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
		       grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
		       limits_json, created_by, created_at, revoked_by, revoked_at, updated_at
		FROM juhe_business.resource_authorization_grants`)
	if err != nil {
		return fmt.Errorf("读取旧授权记录: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, resourceType, resourceID, owner, granteeType, scope, status, createdBy, createdAt, updatedAt string
		var granteeUser, granteeTeam, remark, expiresAt, limits, revokedBy, revokedAt sql.NullString
		if err := rows.Scan(&id, &resourceType, &resourceID, &owner, &granteeType, &granteeUser, &granteeTeam,
			&scope, &status, &remark, &expiresAt, &limits, &createdBy, &createdAt, &revokedBy, &revokedAt, &updatedAt); err != nil {
			return fmt.Errorf("扫描旧授权记录: %w", err)
		}
		switch resourceType {
		case "group":
			if _, ok := groups[resourceID]; !ok {
				continue
			}
		case "account":
			if _, ok := accounts[resourceID]; !ok {
				continue
			}
		default:
			return fmt.Errorf("授权记录 %s 使用了未知资源类型 %q", id, resourceType)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.resource_authorization_grants
				(id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
				 grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
				 limits_json, created_by, created_at, revoked_by, revoked_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			id, resourceType, resourceID, owner, granteeType, granteeUser, granteeTeam, scope, status,
			remark, expiresAt, limits, createdBy, createdAt, revokedBy, revokedAt, updatedAt); err != nil {
			return fmt.Errorf("写入授权记录 %s: %w", id, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历旧授权记录: %w", err)
	}
	report.Tables["resource_authorization_grants"] = count
	return nil
}

func nowText() string { return timeNowUTC() }

