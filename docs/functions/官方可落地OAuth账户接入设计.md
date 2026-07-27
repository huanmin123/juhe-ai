# 官方可落地 OAuth 账户接入设计

> 状态：进行中。目标是在 `juhe-ai` 内独立支持当前已验证、可长期维护的 OAuth 账户接入，不复制第三方订阅代理内核。

## 背景

当前仓库已经有 `oauth` 账户类型与 OpenAI 管理式 OAuth 能力，但不同供应商的 OAuth 语义并不相同：

- OpenAI 属于项目内可管理的授权 / 刷新链路。
- Gemini 更接近“用户持有 Google OAuth access token / refresh token 后的导入型账户”。
- Anthropic 当前已独立落地两类能力：
  - 用户已持有官方 OAuth / Claude Code 体系产生的 Bearer token，可直接导入账户；
  - 项目内发起 Anthropic 官方 OAuth / PKCE 浏览器换码，并支持 Refresh Token 创建与重新授权。
  仍不复用第三方 client identity，也不接入订阅代理语义。

本次目标是把这些已可落地的语义统一进现有账户模型、网关鉴权、前端账户表单和测试链路，而不是把 CLIProxyAPI、sub2api_source 或其他项目里的私有订阅代理整体移植进来。

## 目标

- 统一 `oauth` 账户在不同供应商下的能力矩阵和校验边界。
- 保留 OpenAI 现有管理式 OAuth。
- 复用现有 Gemini OAuth 账户模型。
- 新增 Anthropic Bearer Token 型 OAuth 账户。
- 让账户页、账户测试、网关转发、endpoint modes 和长期文档都与真实供应商语义一致。

## 范围边界

### 本次包含

- OpenAI 管理式 OAuth 账户维持现有创建 / 刷新链路。
- Gemini 导入型 OAuth 账户维持现有 access token / refresh token 录入边界。
- Anthropic `oauth` 账户新增直接录入 `access_token` 的独立部署能力。
- 前后端统一按供应商 / 协议档案决定 OAuth 行为，而不是把 `oauth` 全局等同于 OpenAI。

### 本次不包含

- Claude.ai / Claude Code 订阅代理、固定第三方 client ID、client secret、cookie/sessionKey 链路。
- TLS 指纹伪装、bot detection 绕行、浏览器劫持、CLI 签名模拟。
- 新增依赖外部 sidecar 的运行时认证内核。
- xAI、Antigravity、Kimi、Gemini Code Assist 等缺乏稳定独立部署依据的私有 OAuth 链路。

## 供应商 OAuth 矩阵

| 供应商 | 账户语义 | 项目内创建方式 | 必要凭据 | 上游认证方式 | 刷新策略 |
| --- | --- | --- | --- | --- | --- |
| OpenAI / GPT | 官方管理式 OAuth | 项目内发起授权 | `access_token`、`refresh_token` | `Authorization: Bearer` | 继续走现有刷新链路 |
| Gemini | 导入型 Google OAuth | 直接录入凭据 | `access_token`、`refresh_token` | `Authorization: Bearer` | 保持现有逻辑 |
| Anthropic | 官方托管 OAuth + Bearer Token 导入 | 站内授权或直接录入凭据 | `access_token` / `refresh_token` | `Authorization: Bearer` | 支持 Refresh Token 创建、刷新与重新授权 |

Anthropic 这里的 “OAuth” 现在同时覆盖两种入口：

- 官方 OAuth / PKCE 浏览器授权后由项目内换码创建；
- 官方 OAuth / Claude Code 体系产出的 Bearer token 直接导入。

## 统一建模

### 账户类型

- 继续复用 `accounts.type = 'oauth'`。
- 具体是管理式还是导入型，由 `provider_protocol_profile_id` 和供应商能力矩阵决定。
- `oauth` 不再隐含“必然是 OpenAI Responses/Chat 兼容账户”。

### 凭据模型

- OpenAI：继续允许现有 refresh token 生命周期。
- Gemini：继续允许 access token / refresh token 组合。
- Anthropic：最小必填为 `access_token`；允许预留 `refresh_token` 字段，但当前运行时只消费 `access_token`。

### endpoint modes

- endpoint modes 由供应商协议档案决定。
- OpenAI OAuth 继续对应 Responses / Chat 能力。
- Anthropic OAuth 对应 `messages_json`、`messages_sse`、`message_token_counting`。
- 不再把所有 `oauth` 账户统一压成 OpenAI Responses 能力集合。

## 后端设计

### 存储与默认档案

- 在供应商协议档案中声明哪些 profile 支持 `oauth`。
- Anthropic 官方档案开放 `api_key` 与 `oauth` 两种账户类型。

### 凭据归一化与校验

- `oauth` 凭据归一化改为按 profile 做最小必填校验。
- Anthropic OAuth 缺少 `access_token` 时直接拒绝保存或测试。
- 不能沿用“有 refresh token 即视为可用 OAuth”这类 OpenAI 假设。

### 网关鉴权

- 上游认证头由供应商驱动决定。
- OpenAI / Gemini / Anthropic 均使用 Bearer token，但 endpoint mode、协议路径和失败语义保持各自独立。
- Anthropic 驱动与路由辅助逻辑必须允许 `oauth` 账户参与候选和转发。

### 账户测试与运行时

- 账户测试取密逻辑按供应商区分。
- 仅 OpenAI 管理式 OAuth 继续走刷新 / 重新授权相关逻辑。
- Anthropic 托管 OAuth 支持项目内换码、Refresh Token 创建、手动刷新和重新授权；直接导入型 Bearer Token 继续走“直接验证 access token 是否可调用”的路径。

## 前端设计

### 能力矩阵

- UI 区分“支持 OAuth 账户类型”和“支持管理式 OAuth 授权按钮”两个概念。
- OpenAI：支持管理式授权按钮。
- Gemini：支持 OAuth 账户类型，但默认走直接录入凭据。
- Anthropic：支持 OAuth 账户类型，同时支持托管授权按钮、Refresh Token 和直接录入凭据。

### 表单交互

- 新建 Anthropic OAuth 账户时，展示三种入口：官方 OAuth 回调 URL、Refresh Token、Access Token。
- OpenAI 保持现有授权按钮与回填逻辑。
- endpoint modes、文案说明、可选协议能力说明都按供应商 profile 切换。

### 编辑与维护

- Anthropic OAuth 暴露独立的“刷新令牌”和“重新授权”操作，但语义绑定到 Anthropic 路由而不是复用 OpenAI 文案。
- 编辑时仍允许直接替换 access token；若仅替换裸 access token，继续通过编辑保存而不是“重新授权”接口。

## 测试策略

### 自动化回归

- 后端 typecheck
- 前端 typecheck
- 账户能力 / endpoint mode 纯函数回归
- Anthropic 网关 mock 回归
- 凭据归一化与账户测试相关回归

### 真实联调

- PostgreSQL / Redis 使用测试环境 `192.168.1.203`
- 真实模型密钥按需从 `.local/project-resources/` 引用的外部文件读取
- 不在仓库保存真实 OAuth token

## 风险与注意事项

- `oauth` 是账户类型，不是供应商协议；任何“按类型推断供应商能力”的旧逻辑都要收口。
- Anthropic 当前既支持项目内托管的官方 OAuth 浏览器授权，也支持 token 导入型 OAuth。
- 后续若要新增其他供应商 OAuth，必须先补充独立的供应商矩阵、凭据语义、刷新方式和真实 E2E 证据。

## 关联文档

- [供应商订阅认证接入安全设计](供应商订阅认证接入安全设计.md)
- [OpenAI 账号接入](OpenAI账号接入.md)
- [Gemini 账号接入](Gemini账号接入.md)
- [Anthropic 账号接入](Anthropic账号接入.md)
- [PLAN-20260722T145435019Z 供应商订阅认证安全扩展](../plans/计划-20260722T145435019Z-供应商订阅认证安全扩展.md)
- [PLAN-20260722T155636479Z CLIProxyAPI 本地 Sidecar 接入](../plans/计划-20260722T155636479Z-CLIProxyAPI本地Sidecar接入.md)
