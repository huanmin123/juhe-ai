package accounts

// 账户入口映射写路径（供应商与上游协议面重构设计 §5.1/§5.2/§5.3/§8，B4 W2）：
// account_model_mappings 旧表已随 A 路线撤除（fresh DDL 不再建表），本切片把
// 管理面写路径收敛到 account_entry_mappings 目标形态。解析模式按本包词表
// 字面量维护（唯一可变注册表——矩阵演化只改本处）：
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
	"errors"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pricing"
)

// validationCodeModelNotFound 标记「目标模型在目录中不存在/不可见」这一类
// 校验失败；调用方据此把模型计入 400 样本，其余 ValidationError（如同名跨厂商
// 无法确定计价厂商）原样上抛，避免被折叠成「不在目录中」。
const validationCodeModelNotFound = "model_not_found"

// entryMappingPairTargetModes 是设计 §5.2 固定 endpoint family 矩阵的 18 个
// 允许 (source, target) pair 及其全部可执行 target mode 形态（§5.3 唯一选择
// 函数的写侧输入）。矩阵外的 pair 一律拒绝（§5.2 权威，本包唯一实现）。
var entryMappingPairTargetModes = map[string][]string{
	"chat_completions\x00chat_completions": {"chat_json", "chat_sse"},
	// §5.2 #18：responses→responses 是同族改名映射（Responses 原生上游的
	// 客户端模型别名），不是跨协议 bridge；运行时
	// isOpenAIModelMappingRuntimeConversionSupported 的 source==upstream 分支
	// 已按同族放行，前端下拉与生产存量（144 行）也按同族配置，写侧矩阵曾漏
	// 该项导致此类映射保存恒 400。
	"responses\x00responses":                      {"responses_json", "responses_sse"},
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
//
// 失败文案一律中文：kernel localize 对不含 CJK 的 400 文案会改写为状态默认
// 「请求参数无效」，英文原文从响应侧不可见（BUG-0337 已定的可归因口径）。
func validateEntryMappingFamilyLinkage(sourceFamily, targetFamily string, connectionModes, modelModes []string) error {
	pairModes, ok := entryMappingPairTargetModes[sourceFamily+"\x00"+targetFamily]
	if !ok {
		return &ValidationError{Message: fmt.Sprintf("账号模型别名不支持 %s 到 %s 的上游协议转换，请改用混合供应商账户",
			accountModelMappingEndpointFamilyLabel(sourceFamily), accountModelMappingEndpointFamilyLabel(targetFamily))}
	}
	connectionSet := entryModeSet(connectionModes)
	modelSet := entryModeSet(modelModes)
	if targetFamily == "video_generation" {
		for _, mode := range pairModes {
			if !connectionSet[mode] || !modelSet[mode] {
				return &ValidationError{Message: "账号模型别名的视频上游协议要求连接与模型同时启用 " + mode + "（video_create/video_get/video_content/video_cancel 四个形态缺一不可）"}
			}
		}
		return nil
	}
	for _, mode := range pairModes {
		if connectionSet[mode] && modelSet[mode] {
			return nil
		}
	}
	return &ValidationError{Message: fmt.Sprintf("账号模型别名目标协议 %s 没有可执行的上游形态：目标连接与目标模型的接口能力交集为空，请先启用对应接口能力",
		accountModelMappingEndpointFamilyLabel(targetFamily))}
}

func entryModeSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// resolveEntryConnectionID 按 (账户, preset) 解析 enabled 连接行（§8：preset
// 是账户内自然键）。未知或停用的显式 preset 按显式 400 拒绝；缺省（空）preset
// 回退为该账户唯一的启用连接——B1 断点批前全部连接行的 preset 均为空串，空
// preset 恰好隐式命中（单连接隐式默认的历史行为）；B1 后连接行都携带真实
// preset，隐式命中断裂为恒错。前端账户编辑对话框不暴露连接维度（单连接隐式
// UI），空 preset 回退恢复该 UX；账户有多个启用连接时无法无歧义默认，按映射
// 目标解析失败拒绝并列出候选（中文文案过 localize 直达用户）。
func (s *Store) resolveEntryConnectionID(ctx context.Context, q queryer, accountID, preset string) (string, error) {
	preset = strings.TrimSpace(preset)
	if preset == "" {
		rows, err := q.QueryContext(ctx, s.bind(`SELECT id, COALESCE(connection_preset_code,'') FROM `+s.table("account_connections")+`
			WHERE account_id = ? AND enabled = 1 ORDER BY connection_preset_code, id`), accountID)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		type enabledConnection struct {
			ID     string
			Preset string
		}
		connections := []enabledConnection{}
		for rows.Next() {
			var connection enabledConnection
			if err := rows.Scan(&connection.ID, &connection.Preset); err != nil {
				return "", err
			}
			connections = append(connections, connection)
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
		switch len(connections) {
		case 1:
			return connections[0].ID, nil
		case 0:
			return "", &ValidationError{Message: "账号模型别名未配置上游连接：账户没有启用的上游连接，无法解析映射目标连接"}
		default:
			candidates := make([]string, 0, len(connections))
			for _, connection := range connections {
				candidates = append(candidates, connection.Preset)
			}
			return "", &ValidationError{Message: "账号模型别名必须指定上游连接：账户有多个启用的上游连接，映射行必须指定 targetConnectionPresetCode，候选：" + strings.Join(candidates, "、")}
		}
	}
	var connectionID string
	err := q.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("account_connections")+`
		WHERE account_id = ? AND connection_preset_code = ? AND enabled = 1`), accountID, preset).Scan(&connectionID)
	if err != nil {
		return "", &ValidationError{Message: fmt.Sprintf("上游连接预设 %q 不存在或未启用，请检查账号连接配置", preset)}
	}
	return connectionID, nil
}

// resolveEntryModelID 按 models 目标形态身份键把 target 模型名解析为
// models.id：vendor_code + upstream_model + owner 可见性（personal 行必须归属
// 该 owner，builtin/global 全局可见，owner personal 行优先）+ status=active。
func (s *Store) resolveEntryModelID(ctx context.Context, q queryer, vendor, owner, model string) (string, error) {
	id, _, err := s.resolveEntryModelRow(ctx, q, vendor, owner, model)
	return id, err
}

// resolveEntryModelRow 在 resolveEntryModelID 的身份解析之上带回「该行是否可
// 被计价引擎直接消费」：绑定/映射写入断言据此拒绝无价模型（BUG-0344 根治）。
// 解析谓词与 resolveEntryModelID 完全同键同序（单条 SQL，不重复查询）。
//
// priced 判定委托 pricing.HasCatalogDirectPrice——与网关目录 priced 过滤
// （chainHasDirectCatalogPrice）同一函数、同一入参口径：直价列 / 静态媒体层
// （TTS 字符、音频秒、视频秒、视频按次）/ 档位价三选一。静态媒体层按
// (vendor, public_model) 查（catalogdata 键为目录行 public_model，与链面
// item.Model 一致）。
func (s *Store) resolveEntryModelRow(ctx context.Context, q queryer, vendor, owner, model string) (string, bool, error) {
	var (
		id                                                                  string
		publicModel                                                         string
		rowVendor                                                           string
		inputUsd, outputUsd, cachedInputUsd, cacheWriteUsd, cacheWrite1hUsd sql.NullFloat64
		cacheStorageUsd, imageInputUsd, imageOutputUsd                      sql.NullFloat64
		audioInputUsd, audioOutputUsd, outputUsdPerImage                    sql.NullFloat64
		serviceTierPricesJSON                                               sql.NullString
	)
	// vendor 为空 = generic_http 账户（accounts.vendor_code 为 NULL）。供应商
	// 设计允许 generic_http 绑定任一真实 vendor 的模型；2026-11-03 存量处置后
	// 空 vendor 的 active personal 行已清零，这里按模型名跨 vendor 解析，仍保持
	// owner 可见性与 personal 优先。下方再断言「计价 vendor 唯一且非空」，避免
	// 出现「能绑定但运行期解析不出厂商」的第二形态。
	where := "COALESCE(vendor_code,'') = ? AND "
	args := []any{vendor, model, owner}
	if strings.TrimSpace(vendor) == "" {
		where = "TRUE AND "
		args = []any{model, owner}
	}
	err := q.QueryRowContext(ctx, s.bind(`SELECT id, public_model, COALESCE(vendor_code,''),
			input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m,
			cache_write_usd_per_1m, cache_write_1h_usd_per_1m, cache_storage_usd_per_1m_per_hour,
			image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m,
			audio_output_usd_per_1m, output_usd_per_image, service_tier_prices_json
		FROM `+s.table("models")+`
		WHERE `+where+`upstream_model = ? AND status = 'active'
		  AND (scope IN ('builtin','global') OR (scope = 'personal' AND system_account_id = ?))
		ORDER BY CASE scope WHEN 'personal' THEN 0 WHEN 'global' THEN 1 ELSE 2 END, id
		LIMIT 1`), args...).Scan(
		&id, &publicModel, &rowVendor,
		&inputUsd, &outputUsd, &cachedInputUsd, &cacheWriteUsd, &cacheWrite1hUsd,
		&cacheStorageUsd, &imageInputUsd, &imageOutputUsd, &audioInputUsd,
		&audioOutputUsd, &outputUsdPerImage, &serviceTierPricesJSON,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, &ValidationError{Code: validationCodeModelNotFound, Message: fmt.Sprintf("目标模型 %q 不在当前供应商模型目录中，或已停用/不可见", model)}
		}
		return "", false, err
	}
	if strings.TrimSpace(vendor) == "" && rowVendor == "" {
		// generic_http 账户只能绑定「已归属真实厂商」的行：NULL vendor 行在
		// 运行期无法装载任何厂商目录（sourceVendorCodes("") 返回空源），
		// 绑上就会重现「可调度但算不出成本」。
		return "", false, &ValidationError{Message: fmt.Sprintf("目标模型 %q 未归属任何厂商，无法计价，不能绑定", model)}
	}
	priceSet := pricing.PriceSet{
		InputUsdPer1M:               nullablePriceValue(inputUsd),
		OutputUsdPer1M:              nullablePriceValue(outputUsd),
		CachedInputUsdPer1M:         nullablePriceValue(cachedInputUsd),
		CacheWriteUsdPer1M:          nullablePriceValue(cacheWriteUsd),
		CacheWrite1hUsdPer1M:        nullablePriceValue(cacheWrite1hUsd),
		CacheStorageUsdPer1MPerHour: nullablePriceValue(cacheStorageUsd),
		ImageInputUsdPer1M:          nullablePriceValue(imageInputUsd),
		ImageOutputUsdPer1M:         nullablePriceValue(imageOutputUsd),
		AudioInputUsdPer1M:          nullablePriceValue(audioInputUsd),
		AudioOutputUsdPer1M:         nullablePriceValue(audioOutputUsd),
		OutputUsdPerImage:           nullablePriceValue(outputUsdPerImage),
	}
	return id, pricing.HasCatalogDirectPrice(rowVendor, publicModel, priceSet, serviceTierPriceCount(serviceTierPricesJSON.String)), nil
}

// nullablePriceValue 把可空价格列收敛成 *float64（NULL → nil，0 是真实免费价）。
func nullablePriceValue(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	out := value.Float64
	return &out
}

// serviceTierPriceCount 统计档位价 JSON 的条目数；空/无效/null 均为 0，
// 与链面 chainCatalogTierPriceCount 同语义。
func serviceTierPriceCount(raw string) int {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed == "null" {
		return 0
	}
	var prices map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &prices); err != nil {
		return 0
	}
	return len(prices)
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
