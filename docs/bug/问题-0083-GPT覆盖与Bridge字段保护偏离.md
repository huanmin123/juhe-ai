# BUG-0083 GPT 覆盖与 Bridge 字段保护偏离

## 基本信息

- 编号：BUG-0083
- 状态：已修复（待生产验证）
- 严重程度：P1
- 发现时间：2026-07-14
- 发现方式：会话设计对照
- 模块：GPT / 账户覆盖 / 模型目录 / 协议 bridge / 网关
- 关联计划：PLAN-0096
- 关联 bug：BUG-0071

## 问题概述

- GPT 多模型账户按能力并集 / 任一模型校验，运行时还会向下选择或静默清空覆盖，与“全部模型共同支持且精确生效”冲突。
- 新能力错误发生在网关归一化之外时可能成为通用 500。
- 重建型 Gemini bridge 会静默丢服务等级 / 思考控制，初版保护又误拒显式 null。

## 根因与修复

- 前后端统一使用完整目录能力交集；缺目录或目标模型不精确支持时返回 account-scoped `account_request_override_unsupported`，不降级。
- API Key 和 OAuth driver 共用同一错误归一化入口。
- 无法保真映射的非 null 控制字段明确返回 400；null 作为 no-op 放行，不静默删除有效值。

## 验证记录

- `test:account-gpt-request-overrides`（前后端）
- `test:openai-api-key-passthrough`
- `test:protected-request-control-bridges`
- `test:service-tier-billing`
- `test:gemini-gateway-mock-ai`

## 下次遇到

- 账户覆盖必须按全部支持模型取交集并精确应用，不能用排序等级向下选择。
- Bridge 重建 body 时，受保护字段只能明确转换、明确拒绝或无操作放行 null，不能静默丢失。

