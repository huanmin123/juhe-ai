# BUG-0084 GPT cyber_policy 作用域误限于 Codex

## 基本信息

- 编号：BUG-0084
- 状态：已修复（待生产验证）
- 严重程度：P1
- 发现时间：2026-07-14
- 发现方式：Codex 会话 `019f613c-d454-7173-90f5-2b46a9d91048` 需求复查
- 模块：GPT / 响应检查策略 / 客户端画像 / 网关
- 关联计划：PLAN-0096
- 关联 bug：无

## 问题概述

- 系统默认 `GPT cyber_policy` 已限定 GPT 供应商，却又限定 `clientProfiles = codex`。
- 普通 OpenAI 下游客户端通过真实 GPT 供应商账户请求时不会命中该稳定错误码规则，服务端响应检查行为与真实供应商语义不一致。
- 直接放开所有无客户端画像的 `errorCodes` 又会让协议级规则跨供应商误匹配。

## 根因与修复

- 默认 GPT 规则移除 Codex 客户端限制，保留 OpenAI v1 协议与 GPT provider 双重作用域。
- 运行时仅允许“明确 provider scope 且 providerCode 非空”的错误码规则省略客户端画像；协议级错误码仍必须声明 `clientProfiles`。
- Codex `response.incomplete`、compact 输出契约和专用 SSE 失败渲染保持 Codex 专属，不随 `cyber_policy` 放宽。

## 验证记录

- `test:response-inspection-policy`
- 后端 `typecheck`
- 回归覆盖普通 OpenAI 客户端 + GPT provider 命中，以及普通 OpenAI-compatible provider 不命中 GPT 专属规则。

## 下次遇到

- 供应商稳定错误码与客户端响应格式是两个维度；放宽供应商事实不能同时扩散客户端专属渲染。
- 新增无客户端画像的错误码规则时，必须同时验证协议级拒绝、目标 provider 命中和其他 provider 隔离。
