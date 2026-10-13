// 调度偏好独立实体 P2——派发读取链六态解析收敛（接口契约「调度偏好」章
// 「读取分阶段契约」P2 形态，计划 D4「P2，迁移完成」行；v2 用户裁决修订
// 2026-10-13）。新二进制启动已被完成记录门禁保证处于迁移完成世界：
// purpose=chat 保持现状沿用策略 config（偏好列设计上保持 NULL，不应用偏好）；
// 普通 Key 绑定 NULL 属未迁移/数据不一致，显式错误失败，不回落策略或内置掩
// 盖；绑定内置/自定义偏好经 routestrategies 既有 normalize 合成有效配置
// （enabled 已随 v2 第 4 条退役：创建即启用，无停用回落分支）；引用损坏
// （行缺失/跨 owner）或配置 JSON 损坏/结构非法显式错误上抛。策略 config_json
// 的解析保持宽容（回滚 oracle），但其调度字段不再是普通 Key 的派发配置来源。
package gatewayruntimecache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// schedulingPreferenceFingerprintHexLen 是规范化调度参数 sha256 十六进制指
// 纹截取长度（SP-C2 速度运行态 v2 键的冻结键材料）。
const schedulingPreferenceFingerprintHexLen = 12

// schedulingPreferenceSnapshot 是一次绑定解析通过后的快照增量：config 覆盖
// NormalRoutingConfig，三字段随 GatewayAPIKeyRow 落快照。
type schedulingPreferenceSnapshot struct {
	config *RouteStrategyNormalRoutingConfig
	// normalized 是合成所用的规范化有效配置（fingerprint 的唯一输入；偏好
	// 名称/owner/启用态不参与，同参数不同名偏好 fingerprint 相同）。
	normalized   *routestrategies.NormalRoutingConfig
	preferenceID string
	chatMode     string
}

// fingerprint 计算规范化调度参数 sha256 前 12 位：json.Marshal 的 struct 编
// 码按字段声明序稳定，规范化后数值全部落定，与 JSON 键顺序/原始字节无关。
func (s schedulingPreferenceSnapshot) fingerprint() (string, error) {
	encoded, err := json.Marshal(s.normalized)
	if err != nil {
		return "", fmt.Errorf("调度偏好配置指纹计算失败: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])[:schedulingPreferenceFingerprintHexLen], nil
}

// resolveSchedulingPreferenceSnapshot 按读取分阶段契约 P2 形态解析 api_keys
// 行的调度偏好绑定（输入为 loadGatewayAPIKeyRow 的 JOIN 扫描值）：
//   - purpose='chat'：完全沿用策略 config（现状），返回 nil（快照三字段保持
//     空，运行时不消费偏好配置；会话路径保持现有覆盖/lane 行为）；
//   - scheduling_preference_id IS NULL（普通 Key）：迁移完成后不允许存在，
//     属未迁移或数据不一致，显式错误（不回落策略、不回落内置）；
//   - 绑定内置或自定义行：合成完整配置（enabled 已随 v2 用户裁决修订第 4 条
//     退役：创建即启用，停用回落分支不存在）；
//   - 引用不存在/跨 owner（绑定列非空但 JOIN 未命中）：显式错误。
//
// 偏好行的合成细节由 synthesizeSchedulingPreferenceSnapshot 完成，配置损坏
// （JSON/结构/非法 chat_mode）在其内部显式上抛。
func resolveSchedulingPreferenceSnapshot(purpose, bindingID, joinedID, chatMode, configJSON sql.NullString) (*schedulingPreferenceSnapshot, error) {
	if purpose.Valid && purpose.String == "chat" {
		return nil, nil
	}
	if !bindingID.Valid || bindingID.String == "" {
		// 契约（D4「P2，迁移完成」行）：普通 Key 的 NULL 是未迁移/数据不一致，
		// 请求失败；禁止回落策略调度配置或内置偏好掩盖。
		return nil, fmt.Errorf("API Key 调度偏好绑定缺失（scheduling_preference_id 为空）：调度偏好迁移已完成，普通 Key 必须绑定调度偏好，该状态属于未迁移或数据不一致，不能回落策略路由调度配置")
	}
	if !joinedID.Valid || joinedID.String == "" {
		// 契约：禁止把引用损坏的 Key 当作无绑定或回落策略/内置，读快照按
		// 显式错误失败。
		return nil, fmt.Errorf("API Key 调度偏好绑定引用不存在或不属于该 owner（scheduling_preference_id=%s）", bindingID.String)
	}
	return synthesizeSchedulingPreferenceSnapshot(bindingID.String, chatMode.String, configJSON.String)
}

// synthesizeSchedulingPreferenceSnapshot 把偏好行 chat_mode +
// chat_speed_first_config_json 经 routestrategies 既有 normalize（导出复用，
// 不另写默认值）合成为完整 NormalRoutingConfig：cost_first 塌缩为裸偏好形
// （Raw nil，既有 clone/消费规则不变）；speed_first 附完整 speedFirstConfig
// （Raw 携带规范化后的完整对象）。损坏 JSON、结构非法与非法 chat_mode 显式
// 错误上抛。
func synthesizeSchedulingPreferenceSnapshot(preferenceID, chatMode, speedConfigJSON string) (*schedulingPreferenceSnapshot, error) {
	record := map[string]any{"schedulingPreference": chatMode}
	if chatMode == "speed_first" {
		speedConfigValue := any(nil)
		if speedConfigJSON != "" {
			var decoded any
			if err := json.Unmarshal([]byte(speedConfigJSON), &decoded); err != nil {
				return nil, fmt.Errorf("调度偏好速度优先配置 JSON 损坏: %w", err)
			}
			speedConfigValue = decoded
		}
		record["speedFirstConfig"] = speedConfigValue
	}
	normalized, err := routestrategies.NormalizeNormalRoutingConfig(record)
	if err != nil {
		return nil, fmt.Errorf("调度偏好配置无效: %w", err)
	}
	snapshot := &schedulingPreferenceSnapshot{
		normalized:   normalized,
		preferenceID: preferenceID,
		chatMode:     normalized.SchedulingPreference,
	}
	if normalized.SchedulingPreference == "speed_first" {
		// speed_first 保留 Raw 的既有规则不变：Raw 携带规范化后的完整对象
		// （含缺省回填的 firstByteDeadlineMs 与 speedFirstConfig 全字段）。
		encoded, err := json.Marshal(normalized)
		if err != nil {
			return nil, fmt.Errorf("调度偏好速度优先配置编码失败: %w", err)
		}
		snapshot.config = &RouteStrategyNormalRoutingConfig{SchedulingPreference: "speed_first", Raw: encoded}
		return snapshot, nil
	}
	// cost_first 塌缩规则不变：裸偏好形（Raw nil）。
	snapshot.config = &RouteStrategyNormalRoutingConfig{SchedulingPreference: normalized.SchedulingPreference}
	return snapshot, nil
}

// applySchedulingPreferenceSnapshot 把解析结果写进 GatewayAPIKeyRow（合成路
// 径）：快照三字段 + NormalRoutingConfig 覆盖；nil snapshot（chat 态）原样
// 返回，三字段保持空（v1 运行态语义）。preferenceID 透传 apikeys 的内置行常
// 量锚点，不为绑定列引入第二处字面量。
func applySchedulingPreferenceSnapshot(row *GatewayAPIKeyRow, snapshot *schedulingPreferenceSnapshot) error {
	if snapshot == nil {
		return nil
	}
	fingerprint, err := snapshot.fingerprint()
	if err != nil {
		return err
	}
	row.NormalRoutingConfig = snapshot.config
	row.SchedulingPreferenceID = snapshot.preferenceID
	row.SchedulingPreferenceFingerprint = fingerprint
	row.SchedulingPreferenceChatMode = snapshot.chatMode
	return nil
}
