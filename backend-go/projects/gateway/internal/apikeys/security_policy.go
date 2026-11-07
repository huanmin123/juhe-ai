// 安全策略批A——API Key 绑定域（API Key 安全策略与出站内容脱敏设计 §4.2
// 「API Key 绑定契约（五写入口矩阵）」）：统一 400 文案与五写入口共用的绑定
// 校验，镜像 scheduling_preference.go（调度偏好）文件形态。策略实体自身的
// CRUD 在 /__aisys__/api security-policies 接口（internal/securitypolicies），
// 本文件只做绑定侧的引用校验；内置常量引用 securitypolicies 包（单向依赖：
// apikeys → securitypolicies，反向由 securitypolicies 删除解绑直接 SQL 重置，
// 不 import 本包）。
package apikeys

import (
	"context"
	"database/sql"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/securitypolicies"
)

// BuiltinSecurityPolicyID 是内置安全策略行「默认安全策略」的固定 id（契约固
// 定值）。gateway 不能 import maintenance internal/schema（独立模块），与
// maintenance 的 schema.BuiltinSecurityPolicyID、securitypolicies 包常量及
// 前端 api 常量同源锚点，改动即多处同改。
const BuiltinSecurityPolicyID = securitypolicies.BuiltinSecurityPolicyID

// BuiltinSecurityPolicyName 是内置行权威名称「默认安全策略」（与 maintenance
// schema.BuiltinSecurityPolicyName / securitypolicies 包常量同源锚点）：内置
// 行业务写入口一律 403、种子 upsert 恢复权威值，读取投影 join 存储行回显。
const BuiltinSecurityPolicyName = securitypolicies.BuiltinSecurityPolicyName

// SecurityPolicyUnavailableMessage 是绑定校验失败的统一 400 文案（契约 §4.2：
// 非法类型、空字符串、不存在、跨 owner 不区分提示，避免策略枚举；安全策略
// 无停用态）。
const SecurityPolicyUnavailableMessage = "安全策略不存在或不可用"

// securityPolicyBinding 是一次绑定校验通过后的权威值（id 落列，name/
// builtIn 进 rowPatch 投影）。
type securityPolicyBinding struct {
	id      string
	name    string
	builtIn bool
}

// builtinSecurityPolicyBinding 是缺省 / 显式 null 的持久化目标（契约：缺省
// 或 null → 内置固定 ID，常量权威值，不做库查询）。
func builtinSecurityPolicyBinding() securityPolicyBinding {
	return securityPolicyBinding{id: BuiltinSecurityPolicyID, name: BuiltinSecurityPolicyName, builtIn: true}
}

// resolveSecurityPolicyBinding 是创建 / PATCH 共用的绑定解析（公开面
// add/update 经同一 Create/Patch 入口收敛）：
//   - requested == nil：缺省或显式 null → 内置固定 ID，不查库；
//   - 非空 ID：同 owner 自定义策略或全局内置行才可绑定，否则统一
//     ValidationError（HTTP 层 400「安全策略不存在或不可用」）；
//   - 空字符串（trim 后）：同口径 400（契约把空字符串列在统一拒绝集）。
//
// 与调度偏好绑定的差异（契约 §4.2「无 purpose 豁免」）：安全策略不设 chat
// purpose 豁免——所有 purpose 的 Key 均可绑定，无「列恒 NULL 强不变量」。
//
// q 收 *sql.Tx / *sql.DB，校验与外层写入同事务。
func (s *Store) resolveSecurityPolicyBinding(ctx context.Context, q queryer, ownerID string, requested *string) (securityPolicyBinding, error) {
	if requested == nil {
		return builtinSecurityPolicyBinding(), nil
	}
	candidate := strings.TrimSpace(*requested)
	if candidate == "" {
		return securityPolicyBinding{}, &ValidationError{Message: SecurityPolicyUnavailableMessage}
	}
	var binding securityPolicyBinding
	var builtIn int
	err := q.QueryRowContext(ctx, s.bind(`SELECT security_policies.id, security_policies.name, security_policies.built_in
		FROM `+s.table("security_policies")+` security_policies
		WHERE security_policies.id = ?
			AND (security_policies.system_account_id = ? OR security_policies.built_in = 1)
		LIMIT 1`), candidate, ownerID).
		Scan(&binding.id, &binding.name, &builtIn)
	if err == sql.ErrNoRows {
		return securityPolicyBinding{}, &ValidationError{Message: SecurityPolicyUnavailableMessage}
	}
	if err != nil {
		return securityPolicyBinding{}, err
	}
	binding.builtIn = builtIn == 1
	return binding, nil
}
