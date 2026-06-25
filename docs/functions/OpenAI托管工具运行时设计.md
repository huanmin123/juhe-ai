# OpenAI 托管工具运行时设计

## 1. 背景

OpenAI 到 Anthropic Messages 的高兼容桥接已经把普通 function tools、Files / File Search、本地图像 provider、thinking、compact 和模型映射约束拆成可验证模块。剩余高风险缺口是 OpenAI hosted tools 中需要真实执行环境的能力：

- `code_interpreter` / container。
- `computer` / computer use。
- `mcp` / remote MCP。
- Codex 类 `shell`、`skills`、`tool_search` 等本地执行工具。

这些能力不能靠字段映射解决。没有执行器时，当前网关返回 OpenAI 形态 agent guidance，并且不请求 Anthropic 上游。后续要做到客户端更无感，必须先补本地或上游原生运行时，再把协议事件、权限、审计和失败语义固定下来。

其中 `tool_search` 需要单独拆开看：OpenAI hosted tool search 会由 OpenAI 服务端加载 deferred tool，并在 Responses 输出里产生 `tool_search_call` / `tool_search_output`，再产生最终 `function_call`。Anthropic Messages 没有这个 hosted loading 生命周期。网关只能在“工具清单已经随请求提供”的场景下做本地展开：把 `namespace` 下的 function 预先转换成 Anthropic client tools，并在回包时恢复 Responses `function_call.namespace`。这属于客户端工具调用兼容，不等价于 OpenAI hosted tool search 事件复刻。

## 2. 目标

- 客户端仍按 OpenAI Chat / Responses 形态声明工具。
- 网关只在明确配置且权限通过时执行 hosted runtime。
- 每类工具都有可诊断的能力等级：上游原生、网关本地运行时、agent guidance 或受控拒绝。
- 工具执行结果、失败、用量和审计都保持 OpenAI 风格，不把 Anthropic 私有事件暴露给 OpenAI 客户端。
- mockai 必须先覆盖协议形态和边界，再做真实账户或真实运行时联调。

## 3. 非目标

- 不在主 Web 进程内直接执行任意代码。
- 不默认开放网络、宿主文件系统、环境变量或系统命令。
- 不把 `computer` 用纯文本假装执行。
- 不把未知 MCP server 自动加入 allowlist。
- 不为了“无感”跳过 API Key、分组、账号、供应商档案和图像 / 文件权限边界。

## 4. 能力分层

| 工具 | 首选承接 | 可选承接 | 无承接时 |
| --- | --- | --- | --- |
| `code_interpreter` | 隔离代码沙箱，本地 worker / 子进程 / 容器执行 | Anthropic code execution 原生能力，需账号 profile 显式声明 | OpenAI 形态 guidance，不请求 Anthropic |
| `computer` | Anthropic computer use 原生能力或本地 computer adapter | 受控浏览器 / 桌面自动化 adapter，需显式人工授权 | OpenAI 形态 guidance，不请求 Anthropic |
| `mcp` | 网关 MCP proxy，按 server allowlist、auth 和 approval 执行 | Anthropic MCP connector，需 beta / profile / 账号能力声明 | OpenAI 形态 guidance，不请求 Anthropic |
| Codex `shell` | 受限本地命令 worker | 无 | OpenAI 形态 guidance，不请求 Anthropic |
| Responses `tool_search` + `namespace` functions | 请求内工具清单本地展开为 Anthropic client tools，回包恢复 `namespace` | 完整 hosted search 事件只能由原生 Responses 或后续本地检索运行时承接 | 无可展开工具时 guidance，不请求 Anthropic |
| Codex `skills` / 本地 `tool_search` | 网关本地工具目录和只读检索 | 无 | OpenAI 形态 guidance，不请求 Anthropic |

默认能力为 L4 guidance。只有配置、权限、执行器和 mock 回归都到位后，单项工具才能升级为 L2 / L3。

### 4.1 无承接能力时的 guidance-first 规则

- 如果上游供应商、当前模型、账号 profile 或本地 registry 没有声明真实执行能力，Bridge 必须把该 tool 判定为 `guidance`，不能通过字段转换伪造执行结果。
- `guidance` 必须按下游协议返回可继续消费的正常消息，说明缺失能力、当前上游事实和可行动建议，例如配置本地 MCP / 工具执行器、换用支持能力的模型或移除 tool。
- 能力缺口不返回 500，不标记账号不可用，不继续请求不支持的上游。请求非法、权限越界、allowlist 未命中、状态链损坏或安全策略命中，才进入 `reject` / 错误路径。
- guidance 文案只面向通用客户端 agent，不写死具体客户端名称；后续恢复策略由客户端 agent 自行决定。

## 5. 运行时边界

### 5.1 Runtime Registry

新增工具运行时前，先抽象统一 registry：

- `toolType`：`code_interpreter`、`computer`、`mcp`、`shell`、`skills`、`tool_search`。
- `sourceEndpointFamily`：Chat 或 Responses。
- `upstreamProfileId`：当前上游供应商协议档案。
- `capabilityMode`：`native_upstream`、`local_runtime`、`guidance`、`reject`、`mock`。
- `enabled`：系统级、账号级和 API Key / 分组级共同决定。
- `limits`：超时、输出大小、文件大小、并发、网络策略、审计级别。

Bridge 层只读取 registry 决策，不直接判断具体运行时实现细节。

当前 registry 已落地为运行时配置骨架，默认只支持保守模式：

| 模式 | 行为 |
| --- | --- |
| `guidance` | 默认值。返回 OpenAI 形态 agent guidance，不请求 Anthropic，不执行本地工具。 |
| `reject` | 返回本地 OpenAI 风格错误，不请求 Anthropic，不执行本地工具。 |
| `mock` | 仅用于 mockai / 回归验证的本地模拟输出。不得执行外部命令、不得访问网络、不得宣称生产可用。首批允许 Responses `code_interpreter` 输出模拟的 `code_interpreter_call` 生命周期，并允许 Responses `mcp` 走固定 allowlist 的本地 mock proxy。 |
| `local_runtime` | 首批只允许 MCP real proxy 入口使用。如果 MCP proxy 执行器尚未接入，必须返回本地 OpenAI 风格 `service_unavailable`，不得继续请求 Anthropic 或远程 MCP。 |

除 MCP 外，真实 `native_upstream` / `local_runtime` 模式必须等对应执行器、权限、审计和 mock 回归齐全后再加入配置枚举，避免通过环境变量提前宣称能力可用。MCP 的 `local_runtime` 只是 real proxy 的显式闸门：没有 executor hook、server allowlist、transport 和 approval 时，必须本地失败。`mock` 不属于真实执行器，只用于固定协议形态、审计边界和不请求上游的回归。

| 环境变量 | 工具 | 默认 |
| --- | --- | --- |
| `JUHE_AI_HOSTED_TOOL_CODE_INTERPRETER_MODE` | `code_interpreter` / `container` | `guidance` |
| `JUHE_AI_HOSTED_TOOL_COMPUTER_MODE` | `computer` | `guidance` |
| `JUHE_AI_HOSTED_TOOL_MCP_MODE` | `mcp` | `guidance` |
| `JUHE_AI_HOSTED_TOOL_SHELL_MODE` | `shell` | `guidance` |
| `JUHE_AI_HOSTED_TOOL_SKILLS_MODE` | `skills` | `guidance` |
| `JUHE_AI_HOSTED_TOOL_TOOL_SEARCH_MODE` | `tool_search` | `guidance` |

### 5.2 执行隔离

`code_interpreter` 和 `shell` 不能在 server 进程内执行。最低要求：

- 独立 child process 或 worker，后续可替换为容器。
- 临时工作目录按请求创建，完成后清理。
- 默认禁止外网，允许网络必须显式配置。
- 默认不注入上游账号凭据、系统环境变量和服务端配置。
- stdout / stderr / 文件产物有大小上限。
- 执行超时、空闲超时和并发上限必须可配置。

#### 5.2.1 Code Interpreter mock runtime

首批 code interpreter runtime 只做 mockai：

- 只在 `JUHE_AI_HOSTED_TOOL_CODE_INTERPRETER_MODE=mock` 时启用。
- 只支持 Responses 入口；Chat 入口继续 guidance，直到有清晰的 Chat code interpreter 响应契约。
- 不请求 Anthropic 上游。
- 不运行 Python，不执行 shell，不读取或写入用户文件，不访问网络。
- 返回 OpenAI Responses 形态的 `code_interpreter_call` item 和一条普通 assistant message。
- SSE 模式必须输出 `response.created`、`response.in_progress`、`response.output_item.added`、`response.output_item.done` 和 `response.completed`。
- `include=code_interpreter_call.outputs` 在 mock 模式下允许，返回固定 `logs` 输出；非 mock 模式仍本地拒绝。
- mock 输出必须带稳定 marker，方便 mock 回归断言；不得把用户 prompt、真实代码、文件正文或密钥写入工具输出。

mock runtime 的目的只是先固定协议外形和运行时分支，不代表生产级 code interpreter。真实 runtime 仍需要 worker / 容器、文件隔离、网络策略、超时、输出上限、审计省略和失败语义。

当前已实现的 mock 子集：

- Responses JSON：返回 `status=completed`，`output` 包含 `code_interpreter_call` 和 assistant message，`usage` 为 0。
- Responses SSE：返回 OpenAI Responses 事件序列，不使用 Chat `[DONE]`，并在 `response.completed` snapshot 中带完整输出。
- `include=code_interpreter_call.outputs`：只在 `mock` 模式放行，输出固定 `logs` marker；默认 `guidance` 和显式 `reject` 模式仍不放行。
- 安全边界：不执行用户代码，不请求 Anthropic，不读取文件，不访问网络，不把用户 prompt 写入工具输出。

### 5.3 MCP Proxy

MCP 只允许访问 allowlist 中的 server：

- server label 和 URL 必须匹配 allowlist。
- auth token 只能来自加密配置，不从用户 prompt 注入。
- `require_approval` 需要映射为本地 approval policy；没有 approval 实现时必须 guidance 或拒绝。
- 每次 tool call 记录 server、tool name、arguments 摘要、耗时、状态和错误码。
- MCP 返回的大内容必须按大小上限截断或转文件引用。

#### 5.3.1 MCP mock proxy

首批 MCP proxy 只做 mockai，不连接真实远程 MCP server：

- 只在 `JUHE_AI_HOSTED_TOOL_MCP_MODE=mock` 时启用。
- 只支持 Responses 入口；Chat 入口继续 guidance。
- 固定 allowlist：`server_label=mock-mcp` 且 `server_url=https://mock.mcp.local/mcp`。其他 label / URL 视为未授权 server，本地拒绝。
- 不请求 Anthropic，不请求远程 MCP，不访问网络，不使用或回显请求中的 `authorization`。
- 返回 OpenAI Responses 形态的 `mcp_list_tools` item；`allowed_tools` 只过滤 mock 工具清单，不加载外部工具。
- `require_approval` 省略或为 `always` 时返回 `mcp_approval_request`，不执行 mock tool call。
- `require_approval=never`，或 `require_approval.never.tool_names` 命中 mock 工具时，返回 `mcp_call` 和一条普通 assistant message。
- SSE 模式只输出 OpenAI Responses `response.output_item.*` 和 `response.completed` 事件，不使用 Chat `[DONE]`。
- mock 输出必须带稳定 marker，方便回归断言；不得把用户 prompt、OAuth token、远程 URL 响应或真实第三方数据写入工具输出。

mock proxy 的目的只是固定 MCP 输出 item、approval 和 allowlist 边界。真实 MCP proxy 仍需要 server allowlist 存储、认证引用、approval 状态机、远程 MCP transport、输出大小限制、审计省略和失败语义。

当前已实现的 mock 子集：

- Responses JSON：返回 `status=completed`，`output` 包含 `mcp_list_tools`，并按 approval 决策返回 `mcp_approval_request` 或 `mcp_call` 和 assistant message，`usage` 为 0。
- Responses SSE：返回 OpenAI Responses 事件序列，不使用 Chat `[DONE]`，并在 `response.completed` snapshot 中带完整输出。
- `allowed_tools`：只过滤固定 mock 工具清单；没有可用 mock tool 时返回普通 assistant message，不访问外部 registry。
- 安全边界：只允许固定 `mock-mcp` server，不连接远程 MCP，不请求 Anthropic，不使用或回显 `authorization`，不把用户 prompt 写入工具输出。

#### 5.3.2 MCP real proxy

真实 MCP proxy 是网关本地运行时，不是 Anthropic Messages 字段映射。首批只承接 `server_url` 形式的远程 MCP server；OpenAI `connector_id` 属于 OpenAI 维护连接器，需要单独 connector adapter，不得用 remote MCP proxy 伪装支持。

当前首批代码已启用 runtime gate、allowlist executor hook 和 mock-server transport 子集：

- `JUHE_AI_HOSTED_TOOL_MCP_MODE=local_runtime` 时，Responses `tools[].type=mcp` 不再进入普通 guidance。
- 请求仍先执行 MCP 定义校验；重复 `server_label`、`server_url` / `connector_id` 冲突、缺少 server 标识、非 HTTPS `server_url`、凭据 header 冲突等错误本地拒绝。
- `connector_id` 在 local runtime 下固定返回本地错误，直到 connector adapter 独立实现。
- 如果未配置 MCP proxy executor，返回本地 OpenAI 风格 `service_unavailable` / `openai_anthropic_bridge_mcp_proxy_unavailable`，并且不请求 Anthropic、不连接远程 MCP。
- JSON 非流式免批路径已经完成模型驱动工具循环：仅当 `require_approval=never`，或请求 `allowed_tools` 全部命中 `require_approval.never.tool_names` 时，`tools/list` 结果才会导入 Anthropic client tools；Anthropic 返回 `tool_use` 后，网关执行 MCP `tools/call`，把结果作为 Anthropic `tool_result` 二次回灌，并输出 OpenAI Responses `mcp_list_tools` / `mcp_call` / 最终 assistant message。
- `runtimeConfig.mcpProxy.servers` 命中 allowlist 后，executor 使用 MCP JSON-RPC POST 顺序执行 `initialize`、`notifications/initialized`、`tools/list` 和 `tools/call`。
- allowlist server 字段包含 `label`、`serverUrl`、`enabled`、`allowedTools`、可选 `authorization` 和 `allowRequestAuthorization`；请求里的 `authorization` 只有在 allowlist 明确允许时才发给 MCP server，不写响应。
- `allowed_tools` 会同时受 server allowlist 和请求字段过滤；没有可用工具时返回普通 assistant message，不调用 `tools/call`。
- `require_approval` 省略或为 `always` 时返回 `mcp_approval_request`；`require_approval=never` 或免批工具命中时才执行 `tools/call`。
- `tools/call` 输出按 `mcpProxy.maxOutputBytes` 截断，并在 `mcp_call.metadata.output_truncated` 标记省略。
- 当前 JSON 非流式免批路径会把 MCP `tool_result` 回灌给 Anthropic 生成最终 `output_text`；默认 approval 或非免批工具仍只返回本地 `mcp_approval_request`。
- mockai 可在显式开启私有上游 allowlist 时使用 loopback HTTP MCP server；生产路径仍要求 HTTPS。

当前首批 executor 仍不是完整 hosted MCP 等价实现：

- JSON 非流式免批路径已完成“模型选择工具 + 网关执行工具 + Anthropic tool_result 回灌 + OpenAI Responses 工具轨迹”第一段；默认 approval 或非免批工具仍返回本地 `mcp_approval_request`，不请求 Anthropic，也不执行 `tools/call`。
- JSON 非流式当前只做单轮 MCP 工具循环；如果第二轮 Anthropic 仍要求继续调用工具，暂不继续递归执行。
- 流式路径暂时仍采用本地 Responses 直返 runtime；SSE 中异步执行 MCP `tools/call`、输出 `mcp_call` 增量和 terminal snapshot 需要单独实现。
- approval request 尚未持久化，不做跨 API Key / 分组边界校验。
- 远程 Streamable HTTP / HTTP-SSE 已按响应解析预留，但 mock 回归当前只覆盖 JSON-RPC JSON 响应。
- server allowlist 目前来自 runtime 配置，后续需要进入账号 / 分组 / API Key 可管理的存储和 UI。

OpenAI Responses 的 MCP 契约决定了真实 proxy 需要同时处理四类状态：

- server 定义：`type=mcp`、`server_label`、`server_url` 或 `connector_id`、可选 `server_description`、`authorization`、`allowed_tools`、`defer_loading` 和 `require_approval`。
- 工具导入：成功时输出 `mcp_list_tools`，并把工具定义保留在后续上下文；失败时输出可诊断的失败 item 或本地协议错误。
- 工具审批：默认应生成 `mcp_approval_request`；只有 `require_approval=never` 或策略明确免批的工具才允许直接调用。
- 工具调用：成功时输出 `mcp_call`，失败时在 `mcp_call.error` 中保留 MCP 协议、执行或 transport 错误摘要。

首批 real proxy 的长期 schema 边界：

| 数据对象 | 用途 | 关键字段 |
| --- | --- | --- |
| MCP server allowlist | 管理允许访问的远程 MCP server | `id`、`label`、`server_url`、`description`、`enabled`、`owner_scope`、`allowed_tool_names`、`default_approval_policy`、`timeout_ms`、`output_limit_bytes` |
| MCP auth reference | 保存或引用访问远程 MCP 的凭据 | `server_id`、`credential_ref`、`auth_type`、`expires_at`、`scope_summary`；不保存明文 token 到普通业务字段 |
| MCP tool cache | 缓存 `mcp_list_tools` 结果，降低每轮导入成本 | `server_id`、`tool_name`、`input_schema`、`description`、`annotations`、`etag`、`expires_at`、`last_checked_at` |
| MCP approval request | 记录待审批调用 | `approval_request_id`、`server_id`、`tool_name`、`arguments_digest`、`arguments_preview`、`status`、`created_by_api_key_id`、`expires_at` |
| MCP execution record | 审计真实调用 | `call_id`、`server_id`、`tool_name`、`arguments_digest`、`output_digest`、`duration_ms`、`status`、`error_code`、`omission_metadata` |

首批 real proxy 的执行流程：

1. Bridge 发现 Responses `tools[].type=mcp` 且 Runtime Registry 判定为 `local_runtime`。
2. 校验 `server_label` / `server_url` 命中 allowlist；同一请求内 `server_label` 不能重复，`server_url` 和 `connector_id` 不能同时存在。
3. 从加密凭据引用或本次请求 `authorization` 构造 MCP transport header；`authorization` 只进入远程请求，不写入响应、审计正文或普通日志。
4. 如果上下文已有可信 `mcp_list_tools` 且未过期，可复用；否则向远程 server 执行 tools/list，再按 `allowed_tools` 过滤。
5. 根据 `require_approval`、server 默认策略和工具风险等级决定是否生成 `mcp_approval_request`。
6. 收到匹配的 `mcp_approval_response` 后再执行 tools/call；拒绝审批时生成普通 assistant guidance，不调用远程 MCP。
7. 将远程工具输出转成 Responses `mcp_call.output` 字符串；超限内容截断或转文件引用，并在审计里记录 omission metadata。
8. SSE 路径按 Responses 事件输出 `response.output_item.added`、`response.output_item.done`、必要的 text delta 和最终 `response.completed`；不使用 Chat `[DONE]`。

首批 real proxy 的拒绝边界：

- `connector_id`：返回 guidance 或本地错误，直到 connector adapter 独立实现。
- 未命中 allowlist 的 `server_label` / `server_url`：本地 OpenAI 错误，不连接远程 server。
- 请求同时提供 `server_url` 和 `connector_id`、同请求重复 `server_label`、没有任何 server 标识：本地 OpenAI 错误。
- `authorization` 与自定义 `headers.Authorization` 同时出现：本地 OpenAI 错误，避免凭据来源不清。
- `allowed_tools` 指定不存在的工具：工具导入失败或返回空工具集，不把未允许工具暴露给模型。
- approval request 过期、跨 API Key / 分组 / 授权边界、approval id 不匹配：本地 OpenAI 错误，不调用远程 MCP。
- 远程输出超限、非 UTF-8 文本、大二进制、URL 回传、图片 URL 回传：按输出策略截断、引用或拒绝；不能把远程大 payload 原样写入审计正文。

首批 real proxy 的 transport 策略：

- 仅支持 HTTPS `server_url`。
- 支持 MCP Streamable HTTP 和 HTTP/SSE transport。
- 每次 tools/list 和 tools/call 都有独立超时、总字节上限和重试上限。
- 默认不跟随跨域重定向；如后续允许，必须在 allowlist 中记录最终域名。
- 远程错误只影响当前工具运行，不把上游模型账号标记为不可用。

真实 MCP proxy 启用前必须新增专门 mockai：本机 mock MCP server 覆盖 tools/list 成功、tools/list 失败、tools/call 成功、tools/call 错误、SSE transport、auth 缺失、output 超限、approval required、approval reject、approval expired、allowlist reject 和敏感字段扫描。

### 5.4 Computer Adapter

`computer` 需要状态和动作协议，不能只给模型一段说明：

- adapter 必须提供屏幕截图 / DOM / 可操作目标。
- 鼠标、键盘、导航和文件上传等动作必须按权限分级。
- 默认禁止访问宿主桌面；首批更适合只接受受控浏览器环境。
- 操作轨迹必须审计，图片 / 截图正文按图像 payload 省略策略处理。

### 5.5 Tool Search / Namespace 本地展开

OpenAI 官方 hosted tool search 支持把 function、namespace 或 MCP server 作为可搜索目录，模型决定加载 deferred tool 后会输出 `tool_search_call` 和 `tool_search_output`。Anthropic Messages 不能原生产生这两个 Responses item，因此 bridge 只实现安全的本地展开子集：

- 仅适用于 Responses 入口。
- 仅处理请求 `tools` 中已经声明的 `namespace` / `function`，不访问外部工具目录，不调用 MCP server，不执行本地命令。
- 当请求包含 `{"type":"tool_search"}` 且同时包含 `namespace` 时，把 namespace 内的 function tool 展开成 Anthropic client tool。
- Anthropic 工具名使用 `namespace__function` 形式并做字符清洗和去重，避免不同 namespace 下同名 function 冲突。
- 回包时把 Anthropic `tool_use.name` 映射回 Responses `function_call.name`，并在有 namespace 时补 `function_call.namespace`。
- 不输出伪造的 `tool_search_call` / `tool_search_output`。需要这些精确事件的客户端应直连原生 OpenAI Responses，或等待后续本地 tool search runtime。
- `tool_choice.type=allowed_tools` 仍按展开后的函数集合过滤；强制指定 namespace function 时必须能映射到唯一 Anthropic 工具名。
- `mcp`、远程 registry、权限化动态工具发现仍按 MCP Proxy / Runtime Registry 计划推进，不纳入本地展开。

## 6. 协议输出

Responses 入口优先保持 OpenAI hosted tool 生命周期：

- tool item added / in_progress。
- tool delta 或 action event。
- tool completed / failed。
- response completed / failed。

Chat 入口没有完整 hosted tool item 结构时，只输出合法 Chat Completion / Chat SSE。不能把 Responses 私有 item 塞进 Chat；必要时使用 `message.content` guidance、`tool_calls` 或 `annotations` 的受控子集。

任何本地运行时失败都必须返回 OpenAI 风格错误对象或工具失败 item：

- 权限 / allowlist：`invalid_request_error` 或 `permission_error`。
- 沙箱超时：`upstream_error` / `tool_execution_timeout`。
- 运行时不可用：`service_unavailable` / `tool_runtime_unavailable`。
- 输出超限：`tool_output_too_large`。

## 7. 审计与用量

- 请求审计记录 tool type、tool choice、runtime mode、allowlist 命中和限制配置摘要。
- 响应审计记录 tool status、错误码、耗时和输出大小。
- 大文本、文件、截图、图像和二进制输出不直接写入普通 payload body。
- 本地工具成本需要单独计入 usage metadata；不要混入 Anthropic token usage。
- 失败后是否允许模型继续回答必须按工具类型配置，不能静默吞掉工具失败。

## 8. Mock 优先测试矩阵

| 类别 | 必测场景 |
| --- | --- |
| guidance | 未配置执行器时 Responses / Chat 返回 OpenAI 形态 guidance 且不请求上游 |
| 权限 | API Key / 分组 / 账号未授权时拒绝，不执行本地工具 |
| code interpreter | mock 模式 JSON / SSE 生命周期、`include=code_interpreter_call.outputs` 固定 logs、不执行代码、不请求 Anthropic；真实沙箱后再补成功、stderr、超时、输出超限、文件产物上限、网络默认禁止 |
| MCP | mock 模式 allowlist 命中、未授权 server、authorization 不回显、approval request、allowed_tools 过滤、JSON / SSE `mcp_list_tools` / `mcp_call` 生命周期；真实 proxy 后再补 auth 缺失、远程 transport、tool 输出超限 |
| computer | adapter 未配置、动作成功、动作拒绝、截图省略、操作超时 |
| SSE | tool in_progress、delta / action、completed、failed、response terminal |
| 审计 | 工具大输出和截图正文省略，metadata 保留 |
| 回归 | OpenAI -> Anthropic bridge、Responses -> Chat bridge、混合路由不回归 |

真实联调只能在 mock 全过后做小批量抽样。

## 9. 启用条件

单个 hosted runtime 从 L4 guidance 升级为可执行前，必须同时满足：

- 有设计文档和计划项。
- 有执行器实现和禁用开关。
- 有权限、allowlist、超时、大小、并发和审计策略。
- 有 JSON / SSE mock 回归。
- 有失败语义和不请求上游的边界回归。
- 有凭据扫描和敏感输出不落盘检查。

不满足任一条件时，继续保持 guidance。

## 10. 官方依据

- [OpenAI MCP and Connectors](https://developers.openai.com/api/docs/guides/tools-connectors-mcp)
- [OpenAI Responses API](https://developers.openai.com/api/docs/api-reference/responses/create)
- [Model Context Protocol](https://modelcontextprotocol.io/introduction)
