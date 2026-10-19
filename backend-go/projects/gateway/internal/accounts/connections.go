package accounts

// 账户连接行写入面（供应商与上游协议面重构设计 §2.1/§8，B1 断点批）：
// 创建/克隆/导入汇入的连接集合校验与 account_connections /
// account_endpoint_modes 写入、health-check 三字段事务闭环。解析与校验只读
// backend-go/shared/contracts 的 connector registry（IsAllowedConnectorCombo /
// AllowedModes / URL 契约），不维护第二份允许集合。
//
// 写入序契约（§2.1 复合外键 + 事务闭环）：accounts（health 三字段暂 NULL）→
// account_connections（确定性行 ID，风格对齐 entryMappingRowID）→
// account_endpoint_modes → account_supported_models → account_entry_mappings
// （此时连接行已可解析，修复 mapping_target_unsupported 断点）→ UPDATE
// accounts 补 health 三字段 → 提交前校验三字段非 NULL。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// resolvedConnection 是校验后的单行连接事实：registry surface（纯媒体为空串，
// 落 SQL NULL）、校验过的 base_url 与 endpoint modes、显式 enabled。
type resolvedConnection struct {
	preset  string
	surface string
	baseURL string
	modes   []string
	enabled bool
}

// accountOAuthTypeForRegistry 按 §1.1 提取 registry 判定所需的 oauth_type：
// 仅 google_oauth 账户携带（credentials.oauth_type ∈ {code_assist, google_one,
// ai_studio}），其余账户 registry 行固定 SQL NULL（空串）。
func accountOAuthTypeForRegistry(accountType string, credentials Credentials) string {
	if accountType != contracts.AccountTypeGoogleOAuth {
		return ""
	}
	if text, ok := credentials["oauth_type"].(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

// connectorComboSurface 返回五元组在 registry 的协议面（纯媒体为空串）；
// 未注册组合 ok=false。
func connectorComboSurface(driver, accountType, oauthType, preset, domain string) (string, bool) {
	for _, combo := range contracts.AllowedConnectorCombos() {
		if combo.Driver == driver && combo.AccountType == accountType &&
			combo.OAuthType == oauthType && combo.Preset == preset && combo.Domain == domain {
			return combo.Surface, true
		}
	}
	return "", false
}

// resolveConnectionSet 执行 §8 连接集合的全量校验并解析为写入形态：
//   - 非空必填（缺失/空数组 400，文案列明缺 connections）；
//   - preset 非空、账户内唯一（重复 preset 400 connection_capability_unsupported）；
//   - preset 必须与账户 (driver, account_type, oauth_type, domain) 组合在
//     registry 允许（IsAllowedConnectorCombo）；
//   - protocolSurfaceCode 回显非空时必须等于 registry surface；
//   - endpointModes 非空、无重复、∈ AllowedModes(domain, preset, account_type,
//     oauth_type)（服务端不补齐、不去重、不裁剪，§2.2）；
//   - baseUrl 走 §2.1 registry 数据驱动 validator（invalid_connection_base_url）。
func resolveConnectionSet(domain, driver, accountType, oauthType string, connections []accountscore.ConnectionInput) ([]resolvedConnection, error) {
	if len(connections) == 0 {
		return nil, &ValidationError{Message: "创建载荷缺少 connections：必须显式提交非空连接集合（connectionPresetCode/baseUrl/endpointModes/enabled）"}
	}
	resolved := make([]resolvedConnection, 0, len(connections))
	seenPresets := map[string]bool{}
	for index, connection := range connections {
		preset := strings.TrimSpace(connection.ConnectionPresetCode)
		if preset == "" {
			return nil, &ValidationError{Message: fmt.Sprintf("connections[%d] 缺少 connectionPresetCode", index)}
		}
		if seenPresets[preset] {
			return nil, &ValidationError{Message: "connection_capability_unsupported: 同一连接预设重复提交：" + preset}
		}
		seenPresets[preset] = true
		if !contracts.IsAllowedConnectorCombo(driver, accountType, oauthType, preset, domain) {
			return nil, &ValidationError{Message: fmt.Sprintf("connection_capability_unsupported: 连接预设 %s 不允许用于账户组合（driver=%s, accountType=%s, oauthType=%s, domain=%s）", preset, driver, accountType, oauthType, domain)}
		}
		surface, _ := connectorComboSurface(driver, accountType, oauthType, preset, domain)
		if echo := strings.TrimSpace(connection.ProtocolSurfaceCode); echo != "" && echo != surface {
			return nil, &ValidationError{Message: fmt.Sprintf("connection_capability_unsupported: connections[%d] protocolSurfaceCode %q 与 registry 预设 %q 的协议面不一致", index, echo, preset)}
		}
		if len(connection.EndpointModes) == 0 {
			return nil, &ValidationError{Message: fmt.Sprintf("connection_capability_unsupported: connections[%d]（%s）endpointModes 不能为空", index, preset)}
		}
		allowed := contracts.AllowedModes(domain, preset, accountType, oauthType)
		allowedSet := map[string]bool{}
		for _, mode := range allowed {
			allowedSet[mode] = true
		}
		seenModes := map[string]bool{}
		modes := make([]string, 0, len(connection.EndpointModes))
		for _, rawMode := range connection.EndpointModes {
			mode := strings.TrimSpace(rawMode)
			if mode == "" || seenModes[mode] {
				return nil, &ValidationError{Message: fmt.Sprintf("connection_capability_unsupported: connections[%d]（%s）endpointModes 存在空值或重复值", index, preset)}
			}
			if !allowedSet[mode] {
				return nil, &ValidationError{Message: fmt.Sprintf("connection_capability_unsupported: connections[%d]（%s）endpoint mode %s 不在该组合允许集合内", index, preset, mode)}
			}
			seenModes[mode] = true
			modes = append(modes, mode)
		}
		baseURL := strings.TrimSpace(connection.BaseURL)
		suffixes, templates, ok := connectionURLContract(driver, accountType, oauthType, preset, domain)
		if !ok {
			return nil, &ValidationError{Message: "connection_capability_unsupported: 连接预设 " + preset + " 缺少 registry URL 契约"}
		}
		if err := validateConnectionBaseURL(suffixes, templates, baseURL); err != nil {
			return nil, err
		}
		resolved = append(resolved, resolvedConnection{
			preset:  preset,
			surface: surface,
			baseURL: baseURL,
			modes:   modes,
			enabled: connection.Enabled,
		})
	}
	return resolved, nil
}

// connectionRowID 按 (账户, preset) 生成确定性行 ID（entryMappingRowID 同款
// sha256 截断先例；(account_id, connection_preset_code) 唯一约束保证同键唯一，
// §8：连接 ID 永不由客户端提交）。
func connectionRowID(accountID, preset string) string {
	summary := sha256.Sum256([]byte(accountID + "\x00" + preset))
	return "conn-" + hex.EncodeToString(summary[:12])
}

// insertAccountConnections 写 account_connections + account_endpoint_modes
// （§2.1 写入序第二、三步；调用方事务内 accounts 行已存在）。protocol_surface_code
// 纯媒体连接落 SQL NULL（registry surface 空串）。
func insertAccountConnections(ctx context.Context, q queryer, s *Store, accountID string, connections []resolvedConnection, nowISO string) error {
	for _, connection := range connections {
		surface := sql.NullString{}
		if connection.surface != "" {
			surface = sql.NullString{String: connection.surface, Valid: true}
		}
		if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_connections")+`
			(id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
			connectionRowID(accountID, connection.preset), accountID, connection.preset, surface,
			connection.baseURL, boolInt(connection.enabled), nowISO, nowISO); err != nil {
			return err
		}
		for _, mode := range connection.modes {
			if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_endpoint_modes")+`
				(connection_id, endpoint_mode) VALUES (?, ?)`),
				connectionRowID(accountID, connection.preset), mode); err != nil {
				return err
			}
		}
	}
	return nil
}

// loadAccountConnectionFacts 读取一个已存在账户的连接事实（克隆综合，§2：
// 连接按 preset/Base URL/endpoint modes/enabled 原样重建、health-check 三字段
// 按 preset 重新解析复制）。源账户没有任何连接行时返回显式错误（不做跨形态
// 兼容层）。
func (s *Store) loadAccountConnectionFacts(ctx context.Context, q queryer, accountID string) ([]accountscore.ConnectionInput, string, string, string, error) {
	rows, err := q.QueryContext(ctx, s.bind(`SELECT connection_preset_code, base_url, enabled
		FROM `+s.table("account_connections")+` WHERE account_id = ? ORDER BY connection_preset_code`), accountID)
	if err != nil {
		return nil, "", "", "", err
	}
	defer rows.Close()
	connections := []accountscore.ConnectionInput{}
	for rows.Next() {
		var preset, baseURL string
		var enabled int64
		if err := rows.Scan(&preset, &baseURL, &enabled); err != nil {
			return nil, "", "", "", err
		}
		connections = append(connections, accountscore.ConnectionInput{
			ConnectionPresetCode: preset,
			BaseURL:              baseURL,
			Enabled:              enabled == 1,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, "", "", "", err
	}
	if len(connections) == 0 {
		return nil, "", "", "", &ValidationError{Message: "克隆源账户没有连接行，无法克隆：" + accountID}
	}
	// 每个连接的 endpoint modes。
	modeRows, err := q.QueryContext(ctx, s.bind(`SELECT account_connections.connection_preset_code, account_endpoint_modes.endpoint_mode
		FROM `+s.table("account_endpoint_modes")+` account_endpoint_modes
		INNER JOIN `+s.table("account_connections")+` account_connections
			ON account_connections.id = account_endpoint_modes.connection_id
		WHERE account_connections.account_id = ?
		ORDER BY account_connections.connection_preset_code, account_endpoint_modes.endpoint_mode`), accountID)
	if err != nil {
		return nil, "", "", "", err
	}
	defer modeRows.Close()
	modesByPreset := map[string][]string{}
	for modeRows.Next() {
		var preset, mode string
		if err := modeRows.Scan(&preset, &mode); err != nil {
			return nil, "", "", "", err
		}
		modesByPreset[preset] = append(modesByPreset[preset], mode)
	}
	if err := modeRows.Err(); err != nil {
		return nil, "", "", "", err
	}
	for index := range connections {
		connections[index].EndpointModes = append([]string(nil), modesByPreset[connections[index].ConnectionPresetCode]...)
	}
	// health-check 三字段反解：connection id → 账户内 preset。
	var healthConnectionID, healthMode, healthModel sql.NullString
	if err := q.QueryRowContext(ctx, s.bind(`SELECT health_check_connection_id, health_check_endpoint_mode, health_check_model
		FROM `+s.table("accounts")+` WHERE id = ?`), accountID).Scan(&healthConnectionID, &healthMode, &healthModel); err != nil {
		return nil, "", "", "", err
	}
	healthPreset := ""
	if healthConnectionID.Valid && strings.TrimSpace(healthConnectionID.String) != "" {
		var preset string
		if err := q.QueryRowContext(ctx, s.bind(`SELECT connection_preset_code FROM `+s.table("account_connections")+`
			WHERE account_id = ? AND id = ?`), accountID, strings.TrimSpace(healthConnectionID.String)).Scan(&preset); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", "", "", &ValidationError{Message: "克隆源账户健康检查连接不存在：" + accountID}
			}
			return nil, "", "", "", err
		}
		healthPreset = preset
	}
	return connections, healthPreset, healthMode.String, healthModel.String, nil
}

// removedConnection 是整替换中待删除的存量连接（删除动作推迟到映射整替换之后
// 终裁，支持 §2.1「同事务改指向其他连接」）。
type removedConnection struct {
	id     string
	preset string
}

// beginAccountConnectionsReplacement 是 §8 整替换的第一阶段（先于映射写入）：
// 保留仍存在 preset 的 DB id（base_url/enabled/modes 差异就地更新）、新 preset
// 服务端生成 id、登记待删除 preset（不执行删除）。禁用连接时引用它的 enabled
// 映射在同一事务内禁用（§5.1，disabled mapping 保留）。返回（待删除集合，
// 是否存在实际变化）。
func (s *Store) beginAccountConnectionsReplacement(ctx context.Context, q queryer, accountID string, resolved []resolvedConnection, nowISO string) ([]removedConnection, bool, error) {
	type existingRow struct {
		id      string
		baseURL string
		enabled int64
	}
	existingRows, err := q.QueryContext(ctx, s.bind(`SELECT id, connection_preset_code, base_url, enabled
		FROM `+s.table("account_connections")+` WHERE account_id = ? ORDER BY id`), accountID)
	if err != nil {
		return nil, false, err
	}
	existing := map[string]existingRow{}
	for existingRows.Next() {
		var row existingRow
		var preset string
		if err := existingRows.Scan(&row.id, &preset, &row.baseURL, &row.enabled); err != nil {
			existingRows.Close()
			return nil, false, err
		}
		existing[preset] = row
	}
	if err := existingRows.Err(); err != nil {
		return nil, false, err
	}
	existingRows.Close()
	// 现有 modes（按 connection id）。
	modeRows, err := q.QueryContext(ctx, s.bind(`SELECT account_endpoint_modes.connection_id, account_endpoint_modes.endpoint_mode
		FROM `+s.table("account_endpoint_modes")+` account_endpoint_modes
		INNER JOIN `+s.table("account_connections")+` account_connections
			ON account_connections.id = account_endpoint_modes.connection_id
		WHERE account_connections.account_id = ?`), accountID)
	if err != nil {
		return nil, false, err
	}
	modesByConnection := map[string]map[string]bool{}
	for modeRows.Next() {
		var connectionID, mode string
		if err := modeRows.Scan(&connectionID, &mode); err != nil {
			modeRows.Close()
			return nil, false, err
		}
		if modesByConnection[connectionID] == nil {
			modesByConnection[connectionID] = map[string]bool{}
		}
		modesByConnection[connectionID][mode] = true
	}
	if err := modeRows.Err(); err != nil {
		return nil, false, err
	}
	modeRows.Close()

	changed := false
	incoming := map[string]bool{}
	removed := []removedConnection{}
	for _, connection := range resolved {
		incoming[connection.preset] = true
		row, ok := existing[connection.preset]
		if !ok {
			if err := insertAccountConnections(ctx, q, s, accountID, []resolvedConnection{connection}, nowISO); err != nil {
				return nil, false, err
			}
			changed = true
			continue
		}
		nextModes := map[string]bool{}
		for _, mode := range connection.modes {
			nextModes[mode] = true
		}
		sameModes := len(nextModes) == len(modesByConnection[row.id])
		if sameModes {
			for mode := range nextModes {
				if !modesByConnection[row.id][mode] {
					sameModes = false
					break
				}
			}
		}
		nextEnabled := int64(boolInt(connection.enabled))
		if connection.baseURL != row.baseURL || nextEnabled != row.enabled || !sameModes {
			if _, err := q.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_connections")+`
				SET base_url = ?, enabled = ?, updated_at = ? WHERE id = ?`),
				connection.baseURL, nextEnabled, nowISO, row.id); err != nil {
				return nil, false, err
			}
			changed = true
		}
		if !sameModes {
			if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_endpoint_modes")+` WHERE connection_id = ?`), row.id); err != nil {
				return nil, false, err
			}
			for _, mode := range connection.modes {
				if _, err := q.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_endpoint_modes")+`
					(connection_id, endpoint_mode) VALUES (?, ?)`), row.id, mode); err != nil {
					return nil, false, err
				}
			}
		}
		// §5.1：禁用连接时引用它的 enabled 映射同事务禁用（disabled 保留）。
		if row.enabled == 1 && nextEnabled == 0 {
			if _, err := q.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_entry_mappings")+`
				SET enabled = 0, updated_at = ? WHERE target_connection_id = ? AND enabled = 1`), nowISO, row.id); err != nil {
				return nil, false, err
			}
		}
	}
	for preset, row := range existing {
		if !incoming[preset] {
			removed = append(removed, removedConnection{id: row.id, preset: preset})
		}
	}
	return removed, changed, nil
}

// finishAccountConnectionsReplacement 是 §8 整替换的第二阶段（映射整替换之后）：
// 待删除连接若仍被入口映射引用（enabled/disabled 均算，§2.1 RESTRICT）返回
// connection_mapping_conflict；仍被 health-check 引用且载荷未把它改选到其他
// preset 时返回 health_check_connection_conflict；否则删除（modes 随
// ON DELETE CASCADE）。
func (s *Store) finishAccountConnectionsReplacement(ctx context.Context, q queryer, accountID string, removed []removedConnection, healthKeepPreset string) error {
	for _, row := range removed {
		var mappingCount int64
		if err := q.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("account_entry_mappings")+`
			WHERE target_connection_id = ?`), row.id).Scan(&mappingCount); err != nil {
			return err
		}
		if mappingCount > 0 {
			return &ValidationError{Message: fmt.Sprintf("connection_mapping_conflict: 连接预设 %s 仍被 %d 条入口映射引用，必须先删除或改指向其他连接", row.preset, mappingCount)}
		}
		var healthReferences int64
		if err := q.QueryRowContext(ctx, s.bind(`SELECT COUNT(*) FROM `+s.table("accounts")+`
			WHERE id = ? AND health_check_connection_id = ?`), accountID, row.id).Scan(&healthReferences); err != nil {
			return err
		}
		if healthReferences > 0 && row.preset != healthKeepPreset {
			return &ValidationError{Message: fmt.Sprintf("health_check_connection_conflict: 连接预设 %s 仍是健康检查连接，必须先改选完整三字段", row.preset)}
		}
		if _, err := q.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_connections")+` WHERE id = ?`), row.id); err != nil {
			return err
		}
	}
	return nil
}

// finalizeAccountHealthCheckTrio 解析并落 health-check 三字段（§2.1 写入序
// 收尾）：preset 必须解析到本账户一行 enabled 连接（health_check_connection_
// conflict 语义），model 必须属于支持模型且其 models 行声明该 mode，mode 必须
// 在该连接已启用 mode 内；同一 UPDATE 写入三字段并在提交前读回校验非 NULL
// （§9 事务级校验硬规则）。
func (s *Store) finalizeAccountHealthCheckTrio(ctx context.Context, q queryer, accountID, vendor, owner, preset, mode, model string, supportedModels []string) error {
	var connectionID string
	err := q.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("account_connections")+`
		WHERE account_id = ? AND connection_preset_code = ? AND enabled = 1`), accountID, preset).Scan(&connectionID)
	if err != nil {
		return &ValidationError{Message: fmt.Sprintf("health_check_connection_conflict: connection preset %q resolves to no enabled account connection", preset)}
	}
	if _, err := normalizedHealthCheckModel(model, supportedModels); err != nil {
		return err
	}
	modelID, err := s.resolveEntryModelID(ctx, q, vendor, owner, model)
	if err != nil {
		return &ValidationError{Message: fmt.Sprintf("health_check_connection_conflict: 检查模型 %s 不在本账户可见支持模型内", model)}
	}
	connectionModes, err := s.entryConnectionEndpointModes(ctx, q, connectionID)
	if err != nil {
		return err
	}
	if !entryModeSet(connectionModes)[mode] {
		return &ValidationError{Message: fmt.Sprintf("health_check_connection_conflict: 检查形态 %s 不在该连接已启用 mode 内", mode)}
	}
	modelModes, err := s.entryModelEndpointModes(ctx, q, modelID)
	if err != nil {
		return err
	}
	if !entryModeSet(modelModes)[mode] {
		return &ValidationError{Message: fmt.Sprintf("health_check_connection_conflict: 检查模型 %s 未声明检查形态 %s", model, mode)}
	}
	if _, err := q.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET
		health_check_connection_id = ?, health_check_endpoint_mode = ?, health_check_model = ?
		WHERE id = ?`), connectionID, mode, model, accountID); err != nil {
		return err
	}
	// 提交前事务级校验（§9 硬规则）：三字段必须全部非 NULL/非空。
	var writtenConnectionID, writtenMode, writtenModel sql.NullString
	if err := q.QueryRowContext(ctx, s.bind(`SELECT health_check_connection_id, health_check_endpoint_mode, health_check_model
		FROM `+s.table("accounts")+` WHERE id = ?`), accountID).Scan(&writtenConnectionID, &writtenMode, &writtenModel); err != nil {
		return err
	}
	if !writtenConnectionID.Valid || strings.TrimSpace(writtenConnectionID.String) == "" ||
		!writtenMode.Valid || strings.TrimSpace(writtenMode.String) == "" ||
		!writtenModel.Valid || strings.TrimSpace(writtenModel.String) == "" {
		return &ValidationError{Message: "health_check_connection_conflict: 健康检查三字段未完整落库"}
	}
	return nil
}
