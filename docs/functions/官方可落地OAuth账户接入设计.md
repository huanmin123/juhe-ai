# 官方可落地 OAuth 账户接入设计

> 状态：进行中。目标是在 `juhe-ai` 内独立支持当前已验证、可长期维护的 OAuth 账户接入，不复制第三方订阅代理内核。

## 背景

当前仓库已经有管理式 OAuth 能力，但不同供应商的账户类型、授权客户端和上游 runtime 并不相同：

- OpenAI 属于项目内可管理的授权 / 刷新链路。
- Gemini 使用 `google_oauth`，支持 `code_assist`、`google_one`、`ai_studio` 三种托管授权模式；前两者使用 Code Assist runtime，后者使用 Gemini Developer API。
- Anthropic 当前已独立落地两类能力：
  - 用户已持有官方 OAuth / Claude Code 体系产生的 Bearer token，可直接导入账户；
  - 项目内发起 Anthropic 官方 OAuth / PKCE 浏览器换码，并支持 Refresh Token 创建与重新授权。
  仍不复用第三方 client identity，也不接入订阅代理语义。
- xAI / Grok 使用 `oauth`，支持 xAI PKCE 授权、Refresh Token、直接 Access Token，以及 Grok Web SSO Cookie 通过 device flow 转换成可刷新的 OAuth 凭据；Grok OAuth 运行时只承接 Responses。

本次目标是把这些已可落地的语义统一进现有账户模型、网关鉴权、前端账户表单和测试链路，而不是把 CLIProxyAPI、sub2api_source 或其他项目里的私有订阅代理整体移植进来。

## 目标

- 统一 `oauth` 账户在不同供应商下的能力矩阵和校验边界。
- 保留 OpenAI 现有管理式 OAuth。
- 补齐 Gemini 三种托管 OAuth 模式和 Code Assist runtime。
- 新增 Anthropic Bearer Token 型 OAuth 账户。
- 补齐 Grok OAuth、请求前刷新和 SSO device flow。
- 让账户页、账户测试、网关转发、endpoint modes 和长期文档都与真实供应商语义一致。

## 范围边界

### 本次包含

- OpenAI 管理式 OAuth 账户维持现有创建 / 刷新链路。
- Gemini Google OAuth 支持站内 PKCE、Refresh Token 与 Access Token 创建，按 `oauth_type` 选择 AI Studio 或 Code Assist runtime。
- Anthropic `oauth` 账户新增直接录入 `access_token` 的独立部署能力。
- Grok `oauth` 支持授权回调、Refresh Token、Access Token 和 SSO Cookie 批量转换，固定 Responses-only。
- 前后端统一按供应商 / 协议档案决定 OAuth 行为，而不是把 `oauth` 全局等同于 OpenAI。

### 本次不包含

- Claude.ai / Claude Code 订阅代理、固定第三方 client ID、client secret、cookie/sessionKey 链路。
- TLS 指纹伪装、bot detection 绕行、浏览器劫持、CLI 签名模拟。
- 新增依赖外部 sidecar 的运行时认证内核。
- Vertex AI、Google Workspace 管理授权、Google ADC / Service Account JWT。
- Grok OAuth / SSO device flow 之外的 X 会话代理、浏览器自动化和任意 Cookie 长期网关认证。
- Antigravity、Kimi 等尚未建立当前 Node driver 与凭据闭环的私有 OAuth 链路。

## 供应商 OAuth 矩阵

| 供应商 | 账户语义 | 项目内创建方式 | 必要凭据 | 上游认证方式 | 刷新策略 |
| --- | --- | --- | --- | --- | --- |
| OpenAI / GPT | 官方管理式 OAuth | 项目内发起授权 | `access_token`、`refresh_token` | `Authorization: Bearer` | 继续走现有刷新链路 |
| Gemini | 三模式托管 Google OAuth | 站内 PKCE、Refresh Token 或 Access Token | token、`oauth_type`、模式 client、project / tier | AI Studio Bearer；Code Assist Bearer + request wrapper | 按模式 client 刷新 |
| Anthropic | 官方托管 OAuth + Bearer Token 导入 | 站内授权或直接录入凭据 | `access_token` / `refresh_token` | `Authorization: Bearer` | 支持 Refresh Token 创建、刷新与重新授权 |
| xAI / Grok | 官方 CLI OAuth + SSO device flow | PKCE、Refresh Token、Access Token 或 SSO Cookie | `access_token`、`refresh_token` 及公开 claims | `Authorization: Bearer` + Grok CLI headers | 请求前单飞刷新 |

Anthropic 这里的 “OAuth” 现在同时覆盖两种入口：

- 官方 OAuth / PKCE 浏览器授权后由项目内换码创建；
- 官方 OAuth / Claude Code 体系产出的 Bearer token 直接导入。

Gemini 的 “Google OAuth” 必须继续区分三种模式：

- `code_assist`：内置 Gemini CLI client，使用 `cloudcode-pa.googleapis.com`，Project 必填。
- `google_one`：内置 Gemini CLI client，使用 Code Assist runtime，并增加 Drive metadata scope 识别订阅档位。
- `ai_studio`：自定义 Google OAuth client，使用 `generativelanguage.googleapis.com`。

## 统一建模

### 账户类型

- OpenAI、Anthropic、Grok 复用 `accounts.type = 'oauth'`；Gemini 保持 `accounts.type = 'google_oauth'`。
- 具体是管理式还是导入型，由 `provider_protocol_profile_id` 和供应商能力矩阵决定。
- `oauth` 不再隐含“必然是 OpenAI Responses/Chat 兼容账户”。

### 凭据模型

- OpenAI：继续允许现有 refresh token 生命周期。
- Gemini：保存 token、模式 client、`oauth_type`、project、tier、base URL；Google One 可附带 Drive 配额快照。
- Anthropic：最小必填为 `access_token`；允许预留 `refresh_token` 字段，但当前运行时只消费 `access_token`。
- Grok：保存 access / refresh / ID token、client、scope、到期时间及可取得的用户、团队和订阅 claims；SSO Cookie 不落入账户凭据。

### endpoint modes

- endpoint modes 由供应商协议档案决定。
- OpenAI OAuth 继续对应 Responses / Chat 能力。
- Gemini AI Studio 按 native 档案能力；Code Assist / Google One 只对应 `generate_content_json`、`generate_content_sse`。
- Anthropic OAuth 对应 `messages_json`、`messages_sse`、`message_token_counting`。
- Grok OAuth 只对应 `responses_json`、`responses_sse`；xAI API Key 仍可按档案使用 Chat / Responses。
- 不再把所有 `oauth` 账户统一压成 OpenAI Responses 能力集合。

## 后端设计

### 存储与默认档案

- 在供应商协议档案中声明哪些 profile 支持 `oauth`。
- Anthropic 官方档案开放 `api_key` 与 `oauth` 两种账户类型。
- Gemini native 档案开放 `api_key` 与 `google_oauth`；xAI 档案开放 `api_key` 与 `oauth`。

### 凭据归一化与校验

- `oauth` 凭据归一化改为按 profile 做最小必填校验。
- Anthropic OAuth 缺少 `access_token` 时直接拒绝保存或测试。
- Gemini Code Assist 缺少 `project_id` 时拒绝创建；Grok OAuth 缺少 `access_token` 时拒绝派发。
- 不能沿用“有 refresh token 即视为可用 OAuth”这类 OpenAI 假设。

### 网关鉴权

- 上游认证头由供应商驱动决定。
- OpenAI、Gemini、Anthropic、Grok 都可能使用 Bearer token，但 endpoint mode、协议路径、请求包装、headers 和失败语义保持各自独立。
- Anthropic 驱动与路由辅助逻辑必须允许 `oauth` 账户参与候选和转发。
- Gemini driver 按 `oauth_type` 分流 AI Studio / Code Assist；xAI driver 让 Grok OAuth 只参与 Responses 候选。

### 账户测试与运行时

- 账户测试取密逻辑按供应商区分。
- 四个供应商都通过各自独立路由执行刷新和重新授权，不能互相复用 client identity 或 token endpoint。
- Anthropic 托管 OAuth 支持项目内换码、Refresh Token 创建、手动刷新和重新授权；直接导入型 Bearer Token 继续走“直接验证 access token 是否可调用”的路径。
- Gemini Code Assist / Google One 请求前使用保存的 client 刷新并调用 Cloud Code wrapper；AI Studio 继续调用 Developer API。
- Grok SSO 批量转换最多 3 路并发、逐项返回成功 / 失败；成功后只使用标准 OAuth 凭据运行。

## 前端设计

### 能力矩阵

- UI 区分“支持 OAuth 账户类型”和“支持管理式 OAuth 授权按钮”两个概念。
- OpenAI：支持管理式授权按钮。
- Gemini：支持三模式选择、capabilities、授权回调、Refresh Token 和 Access Token，默认模式为 Code Assist。
- Anthropic：支持 OAuth 账户类型，同时支持托管授权按钮、Refresh Token 和直接录入凭据。
- Grok：支持授权回调、Refresh Token、Access Token 和 SSO Cookie 批量导入。

### 表单交互

- 新建 Anthropic / Gemini / Grok OAuth 账户时按供应商能力展示授权回调 URL、Refresh Token、Access Token；Grok 额外展示 SSO Cookie，Gemini 额外展示模式、Project、Tier 和 AI Studio client 字段。
- OpenAI 保持现有授权按钮与回填逻辑。
- endpoint modes、文案说明、可选协议能力说明都按供应商 profile 切换。

### 编辑与维护

- Anthropic OAuth 暴露独立的“刷新令牌”和“重新授权”操作，但语义绑定到 Anthropic 路由而不是复用 OpenAI 文案。
- Gemini / Grok 同样调用各自路由；重新授权保留账户名称、分组、代理、并发和自定义 Base URL。
- 编辑时仍允许直接替换 access token；若仅替换裸 access token，继续通过编辑保存而不是“重新授权”接口。

## 测试策略

### 自动化回归

- 后端 typecheck
- 前端 typecheck
- 账户能力 / endpoint mode 纯函数回归
- Anthropic 网关 mock 回归
- Gemini OAuth 协议契约、Code Assist runtime 与 token refresh 回归
- Grok OAuth 协议契约、Responses-only、请求前刷新与 SSO device flow 回归
- 凭据归一化与账户测试相关回归

### 真实联调

- PostgreSQL / Redis 使用测试环境 `192.168.1.203`
- 真实模型密钥按需从 `.local/project-resources/` 引用的外部文件读取
- 不在仓库保存真实 OAuth token

## 风险与注意事项

- `oauth` 是账户类型，不是供应商协议；任何“按类型推断供应商能力”的旧逻辑都要收口。
- Gemini 的 `google_oauth` 也不能只按账户类型推断 runtime，必须读取 `oauth_type`。
- Anthropic 当前既支持项目内托管的官方 OAuth 浏览器授权，也支持 token 导入型 OAuth。
- 后续若要新增其他供应商 OAuth，必须先补充独立的供应商矩阵、凭据语义、刷新方式和真实 E2E 证据。

## 关联文档

- [供应商订阅认证接入安全设计](供应商订阅认证接入安全设计.md)
- [OpenAI 账号接入](OpenAI账号接入.md)
- [Gemini 账号接入](Gemini账号接入.md)
- [Anthropic 账号接入](Anthropic账号接入.md)
- [xAI / Grok 账号接入](xAI账号接入.md)
- [PLAN-20260727T174540632Z Gemini 与 Grok OAuth 完整补全](../plans/计划-20260727T174540632Z-Gemini与GrokOAuth完整补全.md)
- [PLAN-20260722T145435019Z 供应商订阅认证安全扩展](../plans/计划-20260722T145435019Z-供应商订阅认证安全扩展.md)
- [PLAN-20260722T155636479Z CLIProxyAPI 本地 Sidecar 接入](../plans/计划-20260722T155636479Z-CLIProxyAPI本地Sidecar接入.md)
