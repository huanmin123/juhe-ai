# Claude Code 客户端画像兼容设计

## 范围

本文记录 Claude Code 作为下游客户端工具接入本项目 Anthropic native 网关时的兼容边界。Claude Code 在这里是客户端画像，不是供应商、协议档案或账户类型。

本设计只处理 Claude Code 使用本地 API Key 调用本项目，再由本项目调度 Anthropic API Key 账号直连 Anthropic Messages 的路径。Claude Code OAuth、Setup Token、Claude 订阅账号、token exchange、token refresh、TLS 指纹伪装、5h 窗口和会话额度都不纳入本文范围。

## 结论

- Claude Code 兼容应落在 `client-profiles/` 客户端画像层。
- Anthropic 账号类型仍只有 `api_key`，不新增 `claude_code` 账号类型。
- 本地认证继续支持 `Authorization: Bearer <本地 API Key>` 和 `x-api-key: <本地 API Key>`。
- 上游认证仍由 Anthropic API Key adapter 替换为命中账号的 `x-api-key`，不能把本地认证或客户端画像 header 透传给上游。
- Claude Code 不使用 OpenAI `response.failed` / `upstream_retryable_error` 协议，不能复用 Codex SSE 兜底语义。
- 画像识别必须来自明确客户端信号。可以使用真实 Claude Code 的多信号请求特征，但不能只靠单个 User-Agent、状态码、Anthropic 错误类型、模型名或某个错误文案临时推断。

## 客户端配置

Claude Code 可配置 Anthropic API base URL 指向本项目网关：

```powershell
$env:ANTHROPIC_BASE_URL = "http://127.0.0.1:3000"
$env:ANTHROPIC_API_KEY = "<本项目本地 API Key>"
$env:CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY = "1"
```

`ANTHROPIC_BASE_URL` 使用不带 `/v1` 的根地址更贴近官方网关示例；本项目同时容忍根地址和 `/v1` 前缀。官方 Claude Code 网关文档要求 Anthropic Messages 格式网关至少暴露 `/v1/messages` 和 `/v1/messages/count_tokens`，模型发现依赖 `/v1/models` 且需要 `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`。

认证优先级按官方 Claude Code 行为处理：`ANTHROPIC_AUTH_TOKEN` 会作为 Bearer 发送，`ANTHROPIC_API_KEY` 会在没有 auth token 时作为 `x-api-key` 发送，本项目两种本地认证都可以承接。为了复现 API Key 模式并避免用户级 Claude Code 设置覆盖当前环境，自动抓包脚本使用 `npx @anthropic-ai/claude-code@latest --print --bare --setting-sources local --no-session-persistence`，并只设置 `ANTHROPIC_API_KEY`。

`x-juhe-client-profile: claude_code` 仍可作为本项目内部显式画像声明：

```text
x-juhe-client-profile: claude_code
```

该 header 只服务网关识别、审计和后续客户端专属策略。真实官方 Claude Code 不会发送这个本地 header；上游请求必须过滤该 header。

抓包或验证模型发现时不要设置会关闭必要请求的粗粒度禁用项；如需降低非必要联网，优先使用 `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`、`DISABLE_TELEMETRY=1`、`DISABLE_ERROR_REPORTING=1` 等明确开关。

## 模型发现与别名

Claude Code 的模型配置支持官方模型 ID，也支持客户端别名。当前本地 Anthropic 模型发现目录只补充官方 Claude Code 配置别名：

- `best`、`fable`、`opus`、`opus[1m]`、`opusplan`、`sonnet`、`sonnet[1m]`、`haiku`

`default` 不进入模型目录，因为它在 Claude Code 中表示清除模型覆盖并回到账号推荐模型，不是可直接调度的模型别名。Antigravity 相关名称是兼容网关 / 客户端侧别名，不代表 Anthropic 官方直连模型 ID；当前仅保留隐藏计价能力，不进入官方 Anthropic 模型发现目录。如需对某个兼容代理正式暴露这些名称，应通过自定义模型或后续独立供应商目录承接。

`-low`、`-medium`、`-high`、`-max` 等 effort 变体后缀不单独列入模型发现目录，但成本解析会按基础 thinking 模型回落，例如 `google/antigravity-claude-opus-4-6-thinking-high` 计入 `google/antigravity-claude-opus-4-6-thinking`。旧的 `claude-opus-4-5-thinking` / `google/antigravity-claude-opus-4-5-thinking` 不收录。

## 本机抓包结果（2026-06-18）

本机全局 `claude` 为旧版本 `2.1.62`，不支持 `--bare`，且用户级 Claude settings 会覆盖临时环境变量。使用 `npx @anthropic-ai/claude-code@latest` 拉取到 `2.1.181` 后，必须带 `--setting-sources local` 才能稳定命中临时本地网关。

抓包到的官方 CLI 请求形态：

- CLI 先发 `HEAD /` 探测根地址，随后发 `POST /v1/messages`；mock 回归里还观察到 `POST /v1/messages?beta=true`。
- 本地认证使用 `x-api-key`，不带 `Authorization`。
- 请求头包含 `anthropic-version: 2023-06-01`、`user-agent: claude-cli/2.1.181 (external, sdk-cli)`、`x-claude-code-session-id`，以及多个 `anthropic-beta`，其中包含 `claude-code-20250219`。
- 请求体不是最小 Messages：`system` 是数组，`tools` 默认有 3 个，`thinking` 可为 `{ "type": "adaptive" }`，并带 `context_management`、`output_config`、`metadata.user_id`、`max_tokens: 32000` 和大段系统提示。
- CLI 会发流式请求，也可能补一个非流式请求；本项目应原样透传 Anthropic native JSON / SSE，而不是转换成 OpenAI Responses。

对比本项目早期合成画像测试：早期测试只覆盖 `x-juhe-client-profile: claude_code` + 最小 JSON body，不能代表官方 CLI 的完整请求形态。当前回归已经把官方 CLI mock 捕获和多信号画像识别纳入验证。

## 当前落地行为

### 画像枚举

运行时客户端画像扩展为：

```ts
type ClientProfile =
  | 'generic_openai'
  | 'codex'
  | 'generic_anthropic'
  | 'claude_code'
```

账号侧兼容能力由供应商、账户类型和协议档案派生。OpenAI 存储字段仍只作为 OpenAI 账号能力输入：

```ts
type AccountClientCompatibility = 'openai_standard' | 'codex_responses'
```

Anthropic 账户不展示 Codex Responses 兼容选项。Claude Code 不写入 `accounts.client_compatibility`，也不影响 Anthropic API Key 账号的创建、导入、测试和调度边界；运行时请求侧 `requestClientCompatibility = claude_code` 只用于账号能力筛选和 Anthropic native 请求策略。

### 识别条件

只有满足以下条件时进入 `claude_code` 画像：

- 当前 API Key 所选路由策略命中的分组使用 `protocolCode = anthropic`、`protocolVersion = v1`。
- 请求是 Anthropic native 支持路径，当前主要是 `POST /v1/messages` 或 `/messages`。
- 满足以下任一识别方式：
  - 请求显式带 `x-juhe-client-profile: claude_code`，大小写和连字符 / 下划线可归一化。
  - 真实 Claude Code 多信号命中：`user-agent` 中的 `claude-cli/`、`anthropic-beta` 中的 `claude-code-*`、`x-claude-code-session-id` / `x-claude-code-agent-id`、`?beta=true` 查询参数中至少命中两个。

不满足时默认为：

- Anthropic 协议：`generic_anthropic`
- OpenAI 协议：`generic_openai` 或已有 Codex 精确命中逻辑

`x-juhe-client-profile: claude_code` 和 Claude Code 多信号都只在 Anthropic Messages 协议下生效，对 OpenAI / GPT / Codex 请求无效，不能把 OpenAI 请求升级为 Claude Code。单个 `User-Agent` 或单个 beta header 不足以升级画像，避免把普通 Anthropic SDK 或兼容客户端误判。

### 请求侧影响

Claude Code 画像当前只影响：

- 审计元数据中的 `clientProfile`、`clientProfileSource`、`downstreamProtocol` 和 `upstreamAdapter`。
- 后续客户端专属策略的门控条件。
- 本地 header 过滤：`x-juhe-client-profile` 不透传上游。

它当前不影响：

- Anthropic API Key 上游认证方式。
- `anthropic-version` 默认值。
- `anthropic-beta` 注入逻辑。
- 模型映射、账号候选筛选、并发、代理、分组授权或 API Key 额度。
- 账号持久状态写入。

### 返回侧影响

Claude Code 当前沿用 Anthropic native 响应透传：

- 非流式 JSON 原样转发 Anthropic Messages JSON。
- 流式 SSE 原样转发 Anthropic Messages SSE 事件。
- usage 仍按 Anthropic usage 字段解析。

不补发 OpenAI `response.failed`，也不把 Codex `upstream_retryable_error` 暴露给 Claude Code。Claude Code 自身会对 transient failure 进行客户端重试；如果后续发现需要为 Claude Code 追加 Anthropic 原生错误事件或 HTTP 错误 shape，必须在 Claude Code 画像下单独实现，不能改 OpenAI / Codex / 通用 Anthropic 行为。

## 分层落点

| 能力 | 落点 | 说明 |
| --- | --- | --- |
| 画像识别 | `backend/src/modules/gateway/client-profiles/strategy.ts` | 通过协议档案、显式 header 或真实 Claude Code 多信号识别 `claude_code` |
| 本地认证 | `request/pre-auth.ts` | 继续支持 Bearer 和 `x-api-key` 本地 API Key |
| 上游认证 | `upstream/request.ts` | Anthropic API Key adapter 写入账号 `x-api-key` |
| 本地画像 header 过滤 | `upstream/request.ts` | `x-juhe-client-profile` 不透传上游 |
| 前端账号策略 | `AccountStrategySection.vue` | Anthropic 账户显示 Anthropic 原生，不显示 Codex Responses |
| 前端响应检查策略 | `ResponseInspectionPolicyFormModal.vue`、`responseInspectionPolicyForm.ts` | 可选择 Anthropic v1 协议和 `claude_code` / `generic_anthropic` 作为策略范围；Anthropic 默认 error object 规则只作为响应语义输入，不直接写账号状态 |
| 回归验证 | `claude-code-client-strategy-regression.ts`、`anthropic-gateway-mock-ai-regression.ts` | 覆盖画像识别、协议隔离和 header 不透传 |

## 禁止项

- 禁止新增 `accounts.type = claude_code`。
- 禁止把 Claude Code OAuth access token 当作 Anthropic API Key 保存。
- 禁止把 Claude Code 画像写入 `clientCompatibility`。
- 禁止根据 `authentication_error`、`rate_limit_error`、HTTP 429 / 5xx 或错误文案临时切到 Claude Code 行为。
- 禁止对所有 Anthropic API Key 请求默认追加 Claude Code 专属 `anthropic-beta`、User-Agent 或伪装 header。
- 禁止把 Codex 的 `response.failed`、turn 级避让或 Responses 请求体归一化复用给 Claude Code。
- 禁止在普通 Anthropic native 路径里实现 Claude Code OAuth token refresh。

## 验证要求

- `x-juhe-client-profile: claude_code` + Anthropic `/v1/messages` 能识别为 `claude_code`。
- 官方 Claude Code 真实请求信号中至少两个命中时能识别为 `claude_code`，`clientProfileSource = claude_code_request_signature`。
- 只有单个 Claude Code 信号时仍识别为 `generic_anthropic`。
- 未带画像 header 的 Anthropic `/v1/messages` 仍识别为 `generic_anthropic`。
- OpenAI `/v1/responses` 即使带 `x-juhe-client-profile: claude_code` 也不能识别为 `claude_code`。
- Claude Code 画像请求上游仍只收到账号 `x-api-key`，不能收到本地 Bearer、下游 `x-api-key` 或 `x-juhe-client-profile`。
- Anthropic API Key 请求不默认注入 Claude Code 专属 `anthropic-beta` 或 User-Agent。
- 账号异常处理仍走统一上游失败、确认、半开、冷却复测和账户错误策略；不得按状态码或错误类型直接判死账号。
- 官方 Claude Code CLI 抓包作为可选验证项；抓包时必须隔离用户级 Claude settings，否则可能被已有 `ANTHROPIC_BASE_URL` / token 设置覆盖而命中其他上游。
- 开启模型发现时，本地 `/v1/models` 应返回 Anthropic 形态模型目录；`/v1/messages/count_tokens` 应能通过同一套本地认证和账号调度链路转发。

## 参考资料

- Claude API overview：<https://platform.claude.com/docs/en/api/overview>
- Claude Code authentication：<https://code.claude.com/docs/en/authentication>
- Claude Code LLM gateway：<https://code.claude.com/docs/en/llm-gateway>
- Claude Code settings：<https://docs.anthropic.com/en/docs/claude-code/settings>
- Claude Code errors：<https://code.claude.com/docs/en/errors>
- Messages API：<https://platform.claude.com/docs/en/api/messages>
- OpenAI SDK compatibility：<https://platform.claude.com/docs/en/api/openai-sdk>
