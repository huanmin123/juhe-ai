# Anthropic 官方 OAuth 托管接入设计

> 状态：实施中。目标是在现有账户和系统 API 架构内，复用已验证的 OpenAI OAuth 骨架，为 Anthropic 官方 OAuth 提供托管接入能力。

## 背景

当前项目的 OpenAI OAuth 已经形成一套完整的托管实现：

- 站内生成授权链接。
- 临时保存 OAuth 会话。
- 浏览器授权后用 code + state 换取 token。
- 以统一账户创建逻辑落库。
- 支持基于 Refresh Token 的创建与重新授权。

Anthropic 目前仅支持把已有 Bearer Token 当作 `oauth` 账户手工导入，这与用户希望“直接参考已有开源实现拿来用”的目标不一致，也导致前端和账户维护逻辑在 OpenAI / Anthropic 之间分裂。

本次把 Anthropic 接入方向收敛为：

- 复用当前仓库现有 OpenAI OAuth 模块的结构与调用面。
- 只承接标准 OAuth / PKCE / token exchange 这类可以独立落地、与当前项目账户模型兼容的能力。
- 明确排除与订阅代理、浏览器劫持、cookie 链路耦合的非通用实现。

## 目标

- 让 Anthropic 官方档案支持托管 OAuth 创建。
- 让 Anthropic 与 OpenAI 在前端都走统一的授权面板和保存流。
- 让托管 OAuth 的供应商分流收敛到能力判定和 API 选择层。

## 范围边界

### 本次包含

- Anthropic 官方 OAuth `auth-url` 生成。
- Anthropic OAuth code callback 换 token。
- Anthropic Refresh Token 直接建号与重新授权。
- 账户创建、操作日志、分组校验、探针派发复用现有通用逻辑。

### 本次不包含

- Claude Web / Claude Code 订阅站点 cookie、sessionKey、设备指纹和 WebSocket 会话迁移。
- 非官方 client identity 伪装、TLS 指纹伪装和 bot detection 绕行。
- 多账号共享订阅代理语义。

## 架构策略

### 后端

- 复制 `modules/openai-oauth` 的模块边界，新建 `modules/anthropic-oauth`。
- 保持系统 API 路由风格一致：
  - `my-anthropic-oauth`
  - `anthropic-oauth`
- 继续复用 `createAccountAsync`、`updateAccountAsync`、`dispatchPendingAccountHealthCheck`、`mutationGuard`、操作日志和权限校验。

### 前端

- 复用 `AccountOAuthAuthorizePanel` 作为 Anthropic 托管 OAuth 的统一授权面板。
- 把“是否是托管 OAuth”从 `canCreateOAuthAccount` 的 OpenAI 专属判断，收敛为按供应商识别的托管能力。
- 保存流和重新授权流按供应商选择 API client，避免写死到 `openaiOAuth`。

## 能力矩阵

| 供应商 | OAuth 账户类型 | 托管 OAuth | 重新授权 | Access Token 自动刷新 |
| --- | --- | --- | --- | --- |
| GPT / OpenAI v1 | 支持 | 支持 | 支持 | 继续走现有逻辑 |
| Anthropic v1 | 支持 | 本次新增 | 本次新增 | 取决于 Refresh Token 可用性 |
| Gemini | `google_oauth` | 不在本次范围 | 不变 | 保持现有逻辑 |

## 关键约束

- `oauth` 只是账户类型，不代表具体供应商实现。
- OpenAI 专属字段如 `id_token`、`account_id`、`chatgpt_user_id` 不能泄漏进 Anthropic 账户凭据语义。
- 网关 Anthropic 驱动已经支持 `oauth` 账户的 Bearer Token 分发，本次只补创建与维护闭环。

## 验证

- 后端 typecheck / 定向回归。
- 前端 typecheck / 保存流编译验证。
- 复查能力矩阵、重新授权按钮、系统 API 挂载点和凭据归一化边界。

## 关联计划

- [PLAN-20260727T154225823Z](../plans/计划-20260727T154225823Z-Anthropic官方OAuth托管接入复用实现.md)
- [官方可落地 OAuth 账户接入设计](官方可落地OAuth账户接入设计.md)
