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
	RPMLimit         int
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
	// rpm_limit 是 2026-11-03 新增列（AI账户每分钟请求上限设计 §7.1）：
	// provider-era 旧库普遍没有该列，先探测存在性——有列读
	// COALESCE(rpm_limit, 0)，无列以常量 0 占位同名投影，按 0 回填，
	// SELECT/Scan 形状在两种旧库下保持一致且幂等。
	hasRPMLimit, err := legacyAccountsHasRPMLimitColumn(ctx, source)
	if err != nil {
		return nil, err
	}
	rpmProjection := "0 AS rpm_limit"
	if hasRPMLimit {
		rpmProjection = "COALESCE(rpm_limit, 0) AS rpm_limit"
	}
	rows, err := source.QueryContext(ctx, `
		SELECT id, config_revision, dispatch_revision, circuit_projection_revision,
		       system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
		       name, type, status, credentials_encrypted, credential_mask, oauth_access_token_expires_at,
		       oauth_refresh_token_present, proxy_profile_id, concurrency_limit, `+rpmProjection+`, priority, super_priority_enabled,
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
			&row.OAuthRefresh, &row.ProxyProfile, &row.ConcurrencyLimit, &row.RPMLimit, &row.Priority, &row.SuperPriority,
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

	accountIDs, err := writeAccountRows(ctx, tx, collected, secret, report)
	if err != nil {
		return nil, err
	}

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

// writeAccountRows 把已 resolve 的账户行写成目标形态：账户行、连接、
// endpoint modes 与健康检查连接指针，全部在同一事务内完成。
// 由 importAccounts 在读取/解析源库后调用；独立出来便于 PG 测试直接锁定
// 写账户调用点，防止连接/endpoint mode 写入被未来重构移除。
func writeAccountRows(ctx context.Context, tx *sql.Tx, rows []accountRow, secret string, report *Report) (map[string]struct{}, error) {
	accountIDs := make(map[string]struct{}, len(rows))
	for _, row := range rows {
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
			"concurrency_limit", "rpm_limit", "priority", "super_priority_enabled", "fallback_enabled", "schedulable",
			"availability_schedule_json", "availability_schedule_next_check_at", "notes", "account_expires_at",
			"last_used_at", "cooldown_until", "last_error_code", "last_error_message", "last_error_trace_id",
			"cooldown_retest_failure_count", "cooldown_retest_observation_started_at", "cooldown_retest_generation",
			"cooldown_retest_last_at", "cooldown_retest_last_status_code",
			"background_probe_enabled",
			"temporary_unavailable_continuous_probe_enabled", "expedited_recovery_enabled", "capability_domain",
			"health_check_connection_id", "health_check_model", "health_check_endpoint_mode", "last_health_check_at",
			"next_health_check_at", "last_health_success_at", "health_check_failure_count",
			"health_check_failure_started_at", "last_health_check_status_code", "last_health_check_error_code",
			"last_health_check_error_message", "last_health_check_trace_id", "stream_failure_count",
			"stream_failure_window_started_at", "balance_query_enabled", "balance_query_config_json",
			"balance_query_next_refresh_at", "created_at", "updated_at",
		}
		values := []any{
			// background_probe_enabled 恒写默认 1：legacy 源库没有手动模式概念
			//（源 SELECT 不读该列——2026-10-30 复审 M2，旧库无此列会直接报错）；
			// 「手动导入默认停用」语义只属于现行导入协议 accountstransfer。
			row.ID, maxInt(row.ConfigRevision, 1), maxInt(row.DispatchRevision, 1), row.CircuitRevision, row.Owner,
			row.Vendor, row.Driver, nil, row.ProtocolCode, row.ProtocolVersion,
			row.Name, row.AccountType, row.Status, row.Credential, credentialFingerprint(row.Credential), row.CredentialMask,
			row.OAuthExpires, row.OAuthRefresh, row.ProxyProfile,
			row.ConcurrencyLimit, row.RPMLimit, row.Priority, row.SuperPriority, row.FallbackEnabled, row.Schedulable,
			row.AvailabilityJSON, row.AvailabilityNext, row.Notes, row.AccountExpires,
			row.LastUsed, row.CooldownUntil, row.LastErrorCode, row.LastErrorMessage, row.LastErrorTrace,
			row.CooldownRetest, row.CooldownStarted, row.CooldownGen,
			row.CooldownLastAt, row.CooldownLastCode,
			1, row.TempProbe, row.Expedited, row.Domain,
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
	report.Tables["accounts"] = len(rows)
	report.CreatedAccounts = len(rows)
	return accountIDs, nil
}

// legacyAccountsHasRPMLimitColumn 报告旧库 juhe_business.accounts 是否已有
// rpm_limit 列。该列 2026-11-03 才进入目标形态（AI账户每分钟请求上限设计
// §7.1），provider-era 旧库普遍没有：无列时账户 RPM 按 0（不限制）回填。
func legacyAccountsHasRPMLimitColumn(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_schema = 'juhe_business' AND table_name = 'accounts' AND column_name = 'rpm_limit'`).Scan(&count); err != nil {
		return false, fmt.Errorf("探测旧库 accounts.rpm_limit 列: %w", err)
	}
	return count > 0, nil
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
	skippedUnpriced := 0
	boundAccounts := map[string]struct{}{}
	for _, item := range collected {
		if _, ok := accounts[item.accountID]; !ok {
			continue
		}
		vendor := vendorForLegacyProvider(item.provider)
		entry, ok := models.lookup(vendor, item.model)
		if !ok {
			newID, err := createPersonalModel(ctx, tx, owners[item.accountID], vendor, item.model)
			if err != nil {
				return err
			}
			// 补建行落 draft（无价）：本包不绑定，管理员配置价格并启用后才可绑定。
			models.add(vendor, item.model, newID, false)
			created++
			skippedUnpriced++
			continue
		}
		if !entry.bindable {
			// 目录中已存在但不可绑定（无价 personal 行等）：不再制造无价绑定。
			skippedUnpriced++
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.account_supported_models (account_id, model_id, created_at)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, item.accountID, entry.id, item.createdAt); err != nil {
			return fmt.Errorf("写入账户 %s 模型 %s: %w", item.accountID, entry.id, err)
		}
		boundAccounts[item.accountID] = struct{}{}
		count++
	}
	// 支持模型被跳过可能让某账户最终一条绑定都不剩（旧配置全是无价自定义名）：
	// 这种账户不可调度，必须点名，不能静默交付。
	accountsWithoutModels := 0
	for accountID := range accounts {
		if _, ok := boundAccounts[accountID]; ok {
			continue
		}
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM juhe_business.account_supported_models WHERE account_id = $1 LIMIT 1`,
			accountID).Scan(&exists); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("复查账户 %s 支持模型: %w", accountID, err)
		} else if err == sql.ErrNoRows {
			accountsWithoutModels++
		}
	}
	report.Tables["account_supported_models"] = count
	report.Tables["models_created"] = created
	report.Tables["account_supported_models_skipped_unpriced"] = skippedUnpriced
	report.Tables["accounts_without_supported_models"] = accountsWithoutModels
	if created > 0 {
		report.Notes = append(report.Notes,
			fmt.Sprintf("模型目录：%d 个旧账户模型名（中转站自定义名）在新目录中不存在，已按 personal scope 补建为 draft（未绑定）", created))
	}
	if skippedUnpriced > 0 {
		report.Notes = append(report.Notes,
			fmt.Sprintf("模型目录：%d 条账户支持模型因目标行无价格或未启用未绑定；请为对应 personal 模型配置价格并启用后重新绑定", skippedUnpriced))
	}
	if accountsWithoutModels > 0 {
		report.Notes = append(report.Notes,
			fmt.Sprintf("模型目录：%d 个账户在本次回迁后没有任何支持模型（旧配置全部不可绑定），这些账户不可调度，需先配置模型价格并绑定", accountsWithoutModels))
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
	created := 0
	skippedUnpriced := 0
	for index, item := range collected {
		if _, ok := accounts[item.accountID]; !ok {
			continue
		}
		vendor := vendorForLegacyProvider(item.provider)
		entry, ok := models.lookup(vendor, item.upstreamModel)
		if !ok {
			newID, err := createPersonalModel(ctx, tx, owners[item.accountID], vendor, item.upstreamModel)
			if err != nil {
				return err
			}
			// 补建行落 draft（无价）：映射目标必须先绑定支持模型（复合外键），
			// 无价行不可绑定，故映射一并跳过。
			models.add(vendor, item.upstreamModel, newID, false)
			created++
			skippedUnpriced++
			continue
		}
		if !entry.bindable {
			skippedUnpriced++
			continue
		}
		// 目标模型必须先出现在该账户的 supported_models 中：映射表有
		// (account_id, target_model_id) → account_supported_models 的复合外键。
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.account_supported_models (account_id, model_id, created_at)
			VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, item.accountID, entry.id, item.createdAt); err != nil {
			return fmt.Errorf("映射目标模型补登记 %s/%s: %w", item.accountID, entry.id, err)
		}
		connectionID := "conn_" + item.accountID
		mappingID := fmt.Sprintf("map_%s_%d", item.accountID, index)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO juhe_business.account_entry_mappings
				(id, account_id, source_model, source_endpoint_family, target_connection_id, target_model_id,
				 target_endpoint_family, enabled, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			mappingID, item.accountID, item.sourceModel, item.sourceFamily, connectionID, entry.id,
			item.upstreamFamily, item.enabled, item.createdAt, item.updatedAt); err != nil {
			return fmt.Errorf("写入模型映射 %s: %w", mappingID, err)
		}
		count++
	}
	report.Tables["account_entry_mappings"] = count
	if created > 0 {
		report.Notes = append(report.Notes,
			fmt.Sprintf("模型目录：%d 个映射目标模型名在新目录中不存在，已按 personal scope 补建为 draft（映射未绑定）", created))
	}
	if skippedUnpriced > 0 {
		report.Notes = append(report.Notes,
			fmt.Sprintf("模型目录：%d 条入口映射因目标模型无价格或未启用未迁移；请配置价格并启用后重建映射", skippedUnpriced))
	}
	return nil
}

// modelIndex maps (vendor, model name) to a target models row. Lookup tries the
// upstream name first and falls back to the public name, matching how relays
// name their custom models.
//
// bindable 是「该行能否作为账户支持模型绑定」的判定（BUG-0344 根治）：只有
// active 且能被计价引擎直接消费的行才可绑定。personal 行必须自带价格或档位
// 价（本包补建行落 draft，恒不可绑定）；builtin/global 行由目录种子与 T93-2
// 写侧校验保证口径（媒体单价在网关静态快照层，维护侧不可见），按 active 放行。
type modelIndexEntry struct {
	id       string
	bindable bool
}

type modelIndex struct {
	byUpstream map[string]modelIndexEntry
	byPublic   map[string]modelIndexEntry
}

func modelKey(vendor, name string) string { return vendor + "\x00" + name }

func loadModelIndex(ctx context.Context, tx *sql.Tx) (*modelIndex, error) {
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(vendor_code,''), upstream_model, public_model, id, scope, status,
			CASE WHEN (
				input_usd_per_1m IS NOT NULL OR output_usd_per_1m IS NOT NULL OR
				cached_input_usd_per_1m IS NOT NULL OR cache_write_usd_per_1m IS NOT NULL OR
				cache_write_1h_usd_per_1m IS NOT NULL OR cache_storage_usd_per_1m_per_hour IS NOT NULL OR
				image_input_usd_per_1m IS NOT NULL OR image_output_usd_per_1m IS NOT NULL OR
				audio_input_usd_per_1m IS NOT NULL OR audio_output_usd_per_1m IS NOT NULL OR
				output_usd_per_image IS NOT NULL OR
				COALESCE(service_tier_prices_json,'{}') NOT IN ('{}','null')
			) THEN 1 ELSE 0 END
		FROM juhe_business.models`)
	if err != nil {
		return nil, fmt.Errorf("读取目标模型目录: %w", err)
	}
	defer rows.Close()
	index := &modelIndex{byUpstream: map[string]modelIndexEntry{}, byPublic: map[string]modelIndexEntry{}}
	for rows.Next() {
		var vendor, upstream, public, id, scope, status string
		var priced int
		if err := rows.Scan(&vendor, &upstream, &public, &id, &scope, &status, &priced); err != nil {
			return nil, fmt.Errorf("扫描目标模型目录: %w", err)
		}
		// personal 行只有自带价格才可绑定；builtin/global 由目录种子与写侧
		// 校验保证口径（媒体单价在网关静态层，维护侧不可见）。
		bindable := status == "active" && (scope != "personal" || priced == 1)
		entry := modelIndexEntry{id: id, bindable: bindable}
		index.byUpstream[modelKey(vendor, upstream)] = entry
		if _, exists := index.byPublic[modelKey(vendor, public)]; !exists {
			index.byPublic[modelKey(vendor, public)] = entry
		}
	}
	return index, rows.Err()
}

func (m *modelIndex) lookup(vendor, name string) (modelIndexEntry, bool) {
	if entry, ok := m.byUpstream[modelKey(vendor, name)]; ok {
		return entry, true
	}
	entry, ok := m.byPublic[modelKey(vendor, name)]
	return entry, ok
}

func (m *modelIndex) add(vendor, name, id string, bindable bool) {
	entry := modelIndexEntry{id: id, bindable: bindable}
	m.byUpstream[modelKey(vendor, name)] = entry
	if _, exists := m.byPublic[modelKey(vendor, name)]; !exists {
		m.byPublic[modelKey(vendor, name)] = entry
	}
}

// createPersonalModel inserts a personal-scope model row for an owner so a
// relay custom model name keeps working after the rebuild.
//
// BUG-0344 根治：补建行一律落 draft 且不写任何价格。旧实现直接落 active 但不写
// 价格，随后被无条件绑进 account_supported_models——该行在计价目录的 priced
// 过滤里被剔除（目录 miss），成功流量 cost_usd 恒 NULL（免费用量）。draft 行
// 既不可被 resolveEntryModelID 解析，也不会被本包绑定；管理员为它配置价格并
// 启用后才进入可绑定/可调度集合。
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
		VALUES ($1,$2,$3,$3,'personal',$4,'draft',false,$5,'[]','[]','[]','{}',false,false,'USD',$6,$6)`,
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
