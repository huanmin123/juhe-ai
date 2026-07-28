# OAuth 模拟上游 E2E 设计

## 1. 目标

为 Node 后端提供一个进程内、本地监听、每次独立启动的 OAuth 模拟上游，用于稳定验证 OpenAI、Anthropic、Gemini 和 Grok 的授权码交换、Refresh Token 刷新、错误响应、凭据归一化与首次上游请求准备。

模拟器不是宽松桩。它必须校验请求方法、Content-Type、表单或 JSON 编码、PKCE、redirect URI、client ID、scope 和供应商专属 Header；协议漂移时测试应直接失败。

## 2. 边界

- 只服务 `backend/src/scripts/regression/`，监听 `127.0.0.1` 随机端口，测试结束后关闭。
- 生产常量继续指向真实供应商，不新增可在生产运行时覆盖 OAuth endpoint 的环境变量。
- OAuth service 通过可恢复的测试 transport 注入把真实 endpoint 请求转送到本地模拟器；Gemini OAuth 探测和 Code Assist 首请求通过 `AsyncLocalStorage` 测试作用域映射到本地地址；默认生产 endpoint 与 transport 不变。
- 模拟器只生成测试 JWT、授权码和 Refresh Token，不读取或写入真实凭据。
- 不把 xAI CLI 订阅 OAuth 描述为公共开发者 API 的通用 OAuth；测试只固定参考仓库已实现的 CLI 链路。

## 3. 协议基线

| 供应商 | 授权与换票基线 | 模拟器必须拒绝 |
| --- | --- | --- |
| OpenAI | browser authorization code + PKCE；refresh 使用 form，scope 为 `openid profile email`；账号 ID 从 ID/Access Token 按字段回退 | JSON refresh、缺 scope、PKCE 不匹配、缺 `chatgpt_account_id` |
| Anthropic | `platform.claude.com` callback/token 链；JSON token body；支持 URL、query、裸 code 与 `code#state` 粘贴 | redirect 不匹配、state/PKCE 不匹配、错误 token body |
| Gemini | Google OAuth form；PKCE；AI Studio 自建 client；Code Assist/Google One 内置 CLI client；Code Assist `loadCodeAssist`、Resource Manager 与 `onboardUser`；刷新有界重试 | client/client secret/redirect/scope 不匹配、跨 client 使用 Refresh Token、错误编码、无界重试 |
| Grok | xAI PKCE form；CLI proxy 请求只对精确主机增加 xAI CLI 身份头；精确 `Access denied` 403 对可重放请求回退官方 API，且仅采用 2xx | 缺 PKCE、错误 client、对非 CLI host 泄漏 CLI 身份头、其他 403 被错误回退 |

[OpenAI Codex Authentication 官方文档](https://learn.chatgpt.com/docs/auth)当前可确认浏览器登录后回传凭据、本地凭据缓存/存储和会话使用中自动刷新，但未公开浏览器 OAuth 的完整线级 token 契约。线级字段以 sub2api、CLIProxyAPI 和 new-api 的一致实现为回归基线，不能标成 OpenAI 公开 API 承诺。

## 4. 测试场景

每个供应商至少覆盖：

1. 生成授权 URL，模拟 authorize endpoint 校验参数并签发一次性 code。
2. 使用真实 service 网络 transport 换取 access/refresh token。
3. 通过系统 API 建号并绑定分组，激活后从 selector 读取持久化账户，再经过 provider driver 和网关 transport 向本地 inference mock 发出首次请求；校验 token claims、落库 credentials、目标路径及供应商专属 Header。
4. 使用轮换 Refresh Token 刷新，确认旧 token 不覆盖新 token。
5. 注入 `invalid_grant`、`unauthorized_client`、429/5xx 或中断，验证错误分类和有界重试。
6. 验证 session owner、state、PKCE 和重复消费边界。
7. 验证供应商运行时专属 Header 与 base URL 约束。
8. 验证 Gemini 网关刷新使用跨进程共享锁、CAS、旋转 Refresh Token 持久化，并复用 service 的退避重试与旧 client fallback。
9. 验证 Anthropic、Gemini、Grok 手动刷新只更新凭据，不清除限流、临时不可用或其他无匹配 provenance 的业务状态。
10. Gemini AI Studio、Code Assist、Google One 分别完成 authorize、HTTP token exchange、轮换刷新、系统 API 落库与首次请求；Code Assist 新用户必须真实经过本地 `loadCodeAssist -> onboardUser`，Resource Manager 只作为已注册无项目或 onboarding 失败后的 fallback；legacy client fallback 必须先由 mock 拒绝内置 client，再由 Refresh Token 原签发 client 成功。

## 5. 完成标准

- 新增一个统一 `test:provider-oauth-mock-upstream-e2e` 命令，可离线重复运行。
- 四家协议 contract 与模拟上游 E2E 全部通过。
- 生产代码无测试 endpoint 配置、无真实凭据、无后台常驻监听。
- Node 后端和前端 typecheck 全部通过。
- 完成一次独立复查，核对协议、并发、错误边界、文档与测试是否互相迎合。
