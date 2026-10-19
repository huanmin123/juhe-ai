package accounts

// 账户入口映射写路径（供应商与上游协议面重构设计 §5.1/§5.2/§5.3/§8，B4 W2）：
// account_model_mappings 旧表已随 A 路线撤除（fresh DDL 不再建表），本切片把
// 管理面写路径收敛到 account_entry_mappings 目标形态。解析模式与
// internal/business/accounts（资源账户聚合，W1 产物）同构、按本包词表字面量
// 维护（两包互不依赖，禁造第二份可变注册表——矩阵演化时两处同批改）：
//
//  1. targetConnectionPresetCode 按 (账户, preset) 解析 enabled
//     account_connections 行（连接 ID 永不由客户端提交，§8）；
//  2. target 模型名按 models.upstream_model 身份键 + owner 可见性 + active
//     解析 models.id（与 account_supported_models 目标形态同一键）；
//  3. §5.2 固定 family 矩阵 + §5.3 connection∩model endpoint modes 联动校验。
//
// 任一步失败返回 ValidationError（400 族），不静默跳过、不造兼容层；导入/克隆
// 上下文缺 preset 的存量行按同一显式错误拒绝（迁移窗口裁决归主代理）。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// entryMappingPairTargetModes 是设计 §5.2 固定 endpoint family 矩阵的 17 个
// 允许 (source, target) pair 及其全部可执行 target mode 形态（§5.3 唯一选择
// 函数的写侧输入）。矩阵外的 pair 一律拒绝。与
// internal/business/accounts.mappingPairTargetModes 同表同语义（§5.2 权威）。
var entryMappingPairTargetModes = map[string][]string{
	"chat_completions\x00chat_completions":        {"chat_json", "chat_sse"},
	"responses\x00chat_completions":               {"chat_sse"},
	"messages\x00chat_completions":                {"chat_json", "chat_sse"},
	"generate_content\x00chat_completions":        {"chat_json"},
	"stream_generate_content\x00chat_completions": {"chat_sse"},
	"messages\x00messages":                        {"messages_json", "messages_sse"},
	"chat_completions\x00messages":                {"messages_json", "messages_sse"},
	"responses\x00messages":                       {"messages_json", "messages_sse"},
	"generate_content\x00messages":                {"messages_json"},
	"stream_generate_content\x00messages":         {"messages_sse"},
	"generate_content\x00generate_content":        {"generate_content_json"},
	"stream_generate_content\x00generate_content": {"generate_content_sse"},
	"chat_completions\x00generate_content":        {"generate_content_json", "generate_content_sse"},
	"responses\x00generate_content":               {"generate_content_json", "generate_content_sse"},
	"messages\x00generate_content":                {"generate_content_json", "generate_content_sse"},
	// §5.2 #16：视频映射 target 必须同时声明并启用全部四个 video mode——
	// 不能把单一 video_create 当作完整可轮询的视频连接。
	"video_generation\x00video_generation": {"video_create", "video_get", "video_content", "video_cancel"},
	// §5.2 #17：TTS 只有一个同步语音形态。
	"tts\x00tts": {"audio_speech"},
}

// validateEntryMappingFamilyLinkage 校验一条映射在目标连接/模型的 endpoint
// modes 交集上可执行（§5.3）：文本目标要求至少一个 source shape 的 target mode
// 同时落在连接 account_endpoint_modes 与模型 supported_endpoint_modes_json 交
// 集内；视频目标要求四个 mode 全部落入交集；TTS 目标必须有 audio_speech。
func validateEntryMappingFamilyLinkage(sourceFamily, targetFamily string, connectionModes, modelModes []string) error {
	pairModes, ok := entryMappingPairTargetModes[sourceFamily+"\x00"+targetFamily]
	if !ok {
		return &ValidationError{Message: fmt.Sprintf("mapping_target_unsupported: family pair %s->%s is not in the §5.2 matrix", sourceFamily, targetFamily)}
	}
	connectionSet := entryModeSet(connectionModes)
	modelSet := entryModeSet(modelModes)
	if targetFamily == "video_generation" {
		for _, mode := range pairModes {
			if !connectionSet[mode] || !modelSet[mode] {
				return &ValidationError{Message: "mapping_target_unsupported: video target requires all four modes declared and enabled, missing " + mode}
			}
		}
		return nil
	}
	for _, mode := range pairModes {
		if connectionSet[mode] && modelSet[mode] {
			return nil
		}
	}
	return &ValidationError{Message: fmt.Sprintf("mapping_target_unsupported: no source shape of %s->%s is executable in connection∩model endpoint modes", sourceFamily, targetFamily)}
}

func entryModeSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// resolveEntryConnectionID 按 (账户, preset) 解析 enabled 连接行（§8：preset
// 是账户内自然键）。缺省、未知或停用 preset 都按显式 400 拒绝。
func (s *Store) resolveEntryConnectionID(ctx context.Context, q queryer, accountID, preset string) (string, error) {
	preset = strings.TrimSpace(preset)
	var connectionID string
	err := q.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("account_connections")+`
		WHERE account_id = ? AND connection_preset_code = ? AND enabled = 1`), accountID, preset).Scan(&connectionID)
	if err != nil {
		return "", &ValidationError{Message: fmt.Sprintf("mapping_target_unsupported: connection preset %q resolves to no enabled account connection", preset)}
	}
	return connectionID, nil
}

// resolveEntryModelID 按 models 目标形态身份键把 target 模型名解析为
// models.id：vendor_code + upstream_model + owner 可见性（personal 行必须归属
// 该 owner，builtin/global 全局可见，owner personal 行优先）+ status=active。
// 与 internal/business/accounts.resolveSupportedModelID 同键同序。
func (s *Store) resolveEntryModelID(ctx context.Context, q queryer, vendor, owner, model string) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("models")+`
		WHERE COALESCE(vendor_code,'') = ? AND upstream_model = ? AND status = 'active'
		  AND (scope IN ('builtin','global') OR (scope = 'personal' AND system_account_id = ?))
		ORDER BY CASE scope WHEN 'personal' THEN 0 WHEN 'global' THEN 1 ELSE 2 END, id
		LIMIT 1`), vendor, model, owner).Scan(&id)
	if err != nil {
		return "", &ValidationError{Message: fmt.Sprintf("mapping_target_unsupported: target model %q resolves to no active owner-visible models row", model)}
	}
	return id, nil
}

// entryConnectionEndpointModes 读目标连接的 account_endpoint_modes 实际集合
// （§5.3 联动校验的连接侧输入）。
func (s *Store) entryConnectionEndpointModes(ctx context.Context, q queryer, connectionID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, s.bind(`SELECT endpoint_mode FROM `+s.table("account_endpoint_modes")+` WHERE connection_id = ? ORDER BY endpoint_mode`), connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var modes []string
	for rows.Next() {
		var mode string
		if err := rows.Scan(&mode); err != nil {
			return nil, err
		}
		modes = append(modes, mode)
	}
	return modes, rows.Err()
}

// entryModelEndpointModes 读 target 模型的 supported_endpoint_modes_json（§3：
// JSON 字符串数组；NULL/空数组按空集参与交集）。
func (s *Store) entryModelEndpointModes(ctx context.Context, q queryer, modelID string) ([]string, error) {
	var raw sql.NullString
	if err := q.QueryRowContext(ctx, s.bind(`SELECT supported_endpoint_modes_json FROM `+s.table("models")+` WHERE id = ?`), modelID).Scan(&raw); err != nil {
		return nil, err
	}
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, nil
	}
	var modes []string
	if err := json.Unmarshal([]byte(raw.String), &modes); err != nil {
		return nil, &ValidationError{Message: fmt.Sprintf("model %s supported_endpoint_modes_json: %v", modelID, err)}
	}
	return modes, nil
}

// resolveEntryMappingTargets 执行一条映射的双解析 + §5.3 联动校验，返回
// (connection_id, model_id)。
func (s *Store) resolveEntryMappingTargets(ctx context.Context, q queryer, accountID, vendor, owner string, mapping ModelMapping) (string, string, error) {
	connectionID, err := s.resolveEntryConnectionID(ctx, q, accountID, mapping.TargetConnectionPresetCode)
	if err != nil {
		return "", "", err
	}
	modelID, err := s.resolveEntryModelID(ctx, q, vendor, owner, mapping.UpstreamModel)
	if err != nil {
		return "", "", err
	}
	connectionModes, err := s.entryConnectionEndpointModes(ctx, q, connectionID)
	if err != nil {
		return "", "", err
	}
	modelModes, err := s.entryModelEndpointModes(ctx, q, modelID)
	if err != nil {
		return "", "", err
	}
	if err := validateEntryMappingFamilyLinkage(mapping.SourceEndpointFamily, mapping.UpstreamEndpointFamily, connectionModes, modelModes); err != nil {
		return "", "", err
	}
	return connectionID, modelID, nil
}

// entryMappingRowID 按 (账户, source_model, source_endpoint_family) 生成确定性
// 行 ID（与 account_tags 同款 sha256 截断先例；UNIQUE 约束保证同键唯一）。
func entryMappingRowID(accountID, sourceModel, sourceFamily string) string {
	summary := sha256.Sum256([]byte(accountID + "\x00" + sourceModel + "\x00" + sourceFamily))
	return "entmap-" + hex.EncodeToString(summary[:12])
}
