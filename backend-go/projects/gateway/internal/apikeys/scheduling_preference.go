// 调度偏好独立实体 P1——API Key 绑定域（接口契约「调度偏好」章「API Key
// 绑定契约（五写入口矩阵）」）：内置固定 id 常量、统一 400 文案与五写入口
// 共用的绑定校验。偏好实体自身的 CRUD 在 /__aisys__/api 调度偏好接口（SP-B
// 并行区 internal/schedulingpreferences），本文件只做绑定侧的引用校验。
package apikeys

import (
	"context"
	"database/sql"
	"strings"
)

// BuiltinSchedulingPreferenceID 是内置调度偏好行「默认调度偏好」的固定 id
// （契约固定值，保留 P1 不透明标识）。gateway 不能 import maintenance
// internal/schema（独立模块），与 maintenance 的 schema.BuiltinSchedulingPreferenceID
// 及本仓 cmd/juhe-ai-gateway/scheduling_preference_schema.go 的
// schedulingPreferenceBuiltinID 同源锚点，改动即三处同改。
const BuiltinSchedulingPreferenceID = "schpref-builtin-chat-cost-first"

// BuiltinSchedulingPreferenceName 是内置行权威名称「默认调度偏好」（v2 用户
// 裁决修订第 2 条通用「默认」定位更名；与 maintenance
// schema.BuiltinSchedulingPreferenceName 同源锚点）：内置行业务写入口一律
// 403、种子 upsert 恢复权威值，读取投影直接用常量，不必为回默认写路径补查。
const BuiltinSchedulingPreferenceName = "默认调度偏好"

// SchedulingPreferenceUnavailableMessage 是绑定校验失败的统一 400 文案（契
// 约：非法类型、空字符串、不存在、跨 owner 不区分提示，避免偏好枚举；停用
// 目标已随 v2 用户裁决修订第 4 条退役，不设停用态）。
const SchedulingPreferenceUnavailableMessage = "调度偏好不存在或不可用"

// schedulingPreferenceExemptPurpose 报告 Key purpose 是否豁免调度偏好绑定
// （契约「API Key 绑定契约」：只豁免 chat 内部用途 Key——purpose='chat' 的
// 绑定列恒 NULL、不建立绑定，经普通 HTTP 写入口提交 schedulingPreferenceId
// （非空 ID 或显式 null）一律统一同型 400，不走普通 Key 的「显式 null 回默认
// 内置 ID」矩阵。chat 裸 INSERT 载体（EnsureChatAPIKey）已随 AI 问答免 Key
// 直调退场（W-B），新写路径不再产生 chat Key，本豁免仅覆盖存量行；studio 已
// 退役，不再识别/豁免，存量 studio 行由 maintenance
// `--apply-studio-retirement` 清理（D06）。公开面 add 与内面 Create 的
// purpose 恒 'general' 不涉及本分支。词表锚点与 gatewayruntimecache 读取链
// 旁路（schedulingPurposeChat）同源，改动即两处同改）。
func schedulingPreferenceExemptPurpose(purpose sql.NullString) bool {
	return purpose.Valid && purpose.String == "chat"
}

// schedulingPreferenceBinding 是一次绑定校验通过后的权威值（id 落列，name/
// builtIn 进 rowPatch 投影）。
type schedulingPreferenceBinding struct {
	id      string
	name    string
	builtIn bool
}

// builtinSchedulingPreferenceBinding 是缺省 / 显式 null 的持久化目标（契约：
// 缺省或 null → 内置固定 ID，常量权威值，不做库查询）。
func builtinSchedulingPreferenceBinding() schedulingPreferenceBinding {
	return schedulingPreferenceBinding{id: BuiltinSchedulingPreferenceID, name: BuiltinSchedulingPreferenceName, builtIn: true}
}

// resolveSchedulingPreferenceBinding 是创建 / PATCH 共用的绑定解析（公开面
// add/update 经同一 Create/Patch 入口收敛）：
//   - requested == nil：缺省或显式 null → 内置固定 ID，不查库；
//   - 非空 ID：同 owner 自定义偏好或全局内置行才可绑定，否则统一
//     ValidationError（HTTP 层 400「调度偏好不存在或不可用」）；enabled 已随
//     v2 用户裁决修订第 4 条退役（创建即启用），无停用目标分支；
//   - 空字符串（trim 后）：同口径 400（契约把空字符串列在统一拒绝集）。
//
// q 收 *sql.Tx / *sql.DB，校验与外层写入同事务。
func (s *Store) resolveSchedulingPreferenceBinding(ctx context.Context, q queryer, ownerID string, requested *string) (schedulingPreferenceBinding, error) {
	if requested == nil {
		return builtinSchedulingPreferenceBinding(), nil
	}
	candidate := strings.TrimSpace(*requested)
	if candidate == "" {
		return schedulingPreferenceBinding{}, &ValidationError{Message: SchedulingPreferenceUnavailableMessage}
	}
	var binding schedulingPreferenceBinding
	var builtIn int
	err := q.QueryRowContext(ctx, s.bind(`SELECT scheduling_preferences.id, scheduling_preferences.name, scheduling_preferences.built_in
		FROM `+s.table("scheduling_preferences")+` scheduling_preferences
		WHERE scheduling_preferences.id = ?
			AND (scheduling_preferences.system_account_id = ? OR scheduling_preferences.built_in = 1)
		LIMIT 1`), candidate, ownerID).
		Scan(&binding.id, &binding.name, &builtIn)
	if err == sql.ErrNoRows {
		return schedulingPreferenceBinding{}, &ValidationError{Message: SchedulingPreferenceUnavailableMessage}
	}
	if err != nil {
		return schedulingPreferenceBinding{}, err
	}
	binding.builtIn = builtIn == 1
	return binding, nil
}
