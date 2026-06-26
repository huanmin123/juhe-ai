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
| `local_runtime` | 首批用于需要真实执行器的受限运行时入口。MCP real proxy 和 code interpreter 都可以先挂到该闸门；如果对应 executor 尚未接入，必须返回本地 OpenAI 风格 `service_unavailable`，不得继续请求 Anthropic、不得在主 Web 进程执行。 |

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
- `include=code_interpreter_call.outputs`：只在 `mock` 模式放行，`local_runtime` 由执行器决定；默认 `guidance` 和显式 `reject` 模式仍不放行。
- 安全边界：不执行用户代码，不请求 Anthropic，不读取文件，不访问网络，不把用户 prompt 写入工具输出。

#### 5.2.2 Code Interpreter local_runtime gate

`local_runtime` 先作为真实执行器的入口闸门，而不是直接在主 Web 进程执行 Python：

- 只在 `JUHE_AI_HOSTED_TOOL_CODE_INTERPRETER_MODE=local_runtime` 时启用。
- 只支持 Responses 入口；Chat 入口继续 guidance，直到有清晰的 Chat code interpreter 响应契约。
- 如果没有配置 code interpreter executor，必须返回本地 `service_unavailable`，且不请求 Anthropic。
- `include=code_interpreter_call.outputs` 在 `local_runtime` 下不应先被 include 校验拦住，应该交给运行时决定是否产出 outputs。
- 当 executor 接入后，必须保持 worker / 子进程 / 容器隔离、临时目录、默认禁网、输出上限、超时和清理语义，不允许回退到主进程执行。
- 该闸门只证明配置已经进入真实运行时分支，不代表容器、文件产物、stderr 和重试语义已经完整落地。

#### 5.2.3 Code Interpreter worker 沙箱首段

首段 `local_runtime` executor 采用独立 Python 子进程执行，不在 Node Web 进程内 `eval` / `exec` 用户代码。该能力只在 `JUHE_AI_HOSTED_TOOL_CODE_INTERPRETER_MODE=local_runtime` 且 gateway driver 显式接入 executor 时启用：

- Anthropic bridge 会把 OpenAI Responses `code_interpreter` 声明转换成一个 Anthropic client tool，让上游模型生成 `code` 参数。
- 网关执行器只接收模型生成的 `code`，在每次 tool call 独立创建临时目录，写入受控 runner，再用 Python isolated 参数执行。
- 子进程环境使用最小 env，不注入上游 API Key、系统账户凭据、数据库路径、OpenAI / Anthropic token 或服务端完整环境变量。
- 子进程有 `timeoutMs`、`maxCodeBytes`、`maxOutputBytes` 和退出码记录；stdout / stderr 超限必须截断，并在 `code_interpreter_call.metadata.output_truncated=true` 标记。
- 文件产物首段扫描本次临时目录内由代码生成的文件，返回 `metadata.artifacts` 和一段 `outputs[].logs` 摘要；不把文件内容 base64 塞进响应体。
- 当 gateway request 能拿到当前 API Key scope 时，符合上限的文件产物会写入本地 OpenAI 兼容 Files 存储，生成稳定 `file-*`，客户端可用既有 `/v1/files/{file_id}/content` 下载；兼容壳首段同时把这些产物绑定到本次 `container_id`，支持 `/v1/containers/{container_id}/files` 和 `/v1/containers/{container_id}/files/{file_id}/content`。
- 首段网络禁止只能通过 Python runner 层阻断常见 `socket` / `subprocess` / `os.system` 路径，不能宣称等价于 Docker / VM / OS sandbox；生产要达到 OpenAI hosted container 等价能力，仍需容器或受控 VM。
- 工具执行失败、超时或输出超限不标记上游账号失败；Responses 输出仍用 `code_interpreter_call` item 和普通 assistant 收口，并把错误摘要放入 logs / metadata。
- SSE 首段允许缓冲式输出：先聚合 Anthropic `tool_use`，执行代码后回灌 `tool_result`，最终输出 Responses typed SSE terminal snapshot；不宣称逐 token / 逐 stdout 实时流。

首段配置项：

| 环境变量 | 默认 | 用途 |
| --- | --- | --- |
| `JUHE_AI_CODE_INTERPRETER_PYTHON_COMMAND` | `python` | Python 子进程命令；留空视为 executor 未配置，`local_runtime` 请求本地返回 `service_unavailable`。 |
| `JUHE_AI_CODE_INTERPRETER_TIMEOUT_MS` | `5000` | 单次代码执行总超时。 |
| `JUHE_AI_CODE_INTERPRETER_MAX_CODE_KB` | `64` | 单次模型生成代码大小上限。 |
| `JUHE_AI_CODE_INTERPRETER_MAX_OUTPUT_KB` | `64` | stdout / stderr 合计输出上限，超限后截断并标记 `metadata.output_truncated=true`。 |
| `JUHE_AI_CODE_INTERPRETER_MAX_ARTIFACTS` | `8` | 单次 tool call 返回元数据摘要的文件产物数量上限，超出后只增加 `metadata.artifacts_omitted_count`。 |
| `JUHE_AI_CODE_INTERPRETER_MAX_ARTIFACT_KB` | `256` | 单个文件产物持久化上限；超过上限的产物只返回元数据并标记 `content_omitted=true` 和 `omit_reason=file_too_large`。 |
| `JUHE_AI_CODE_INTERPRETER_TEMP_ROOT` | `backend/data/code-interpreter-tmp` | 每次 tool call 临时目录根路径。 |
| `JUHE_AI_CODE_INTERPRETER_CLEANUP_TEMP_DIR` | `true` | 执行后是否删除临时目录；排障时可临时关闭，生产应保持开启。 |

#### 5.2.4 Code Interpreter 文件产物首段策略

当前阶段不伪装完整 OpenAI 托管 container VM，但会把可持久化的代码产物接入本地 OpenAI 兼容 Files 存储。执行器在 Python 子进程退出后、清理临时目录前，递归扫描本次工作目录内除根目录 `input.py` / `runner.py` 外的普通文件，并输出以下信息：

- `code_interpreter_call.outputs` 继续以 `logs` 兼容项为主；如果有文件产物，会追加一段 `[artifacts]` logs 摘要，便于现有 Responses 客户端可见。
- `code_interpreter_call.metadata.artifacts` 保存结构化摘要：`filename`、`bytes`、可推断的 `media_type`、`content_omitted`、`omit_reason`、`file_id` 和 `download_path`。
- 可持久化产物用目的 `code_interpreter_output` 写入 `openai_compatible_files`，scope 绑定当前系统账户和 API Key；客户端可以用现有 `/v1/files/{file_id}` 和 `/v1/files/{file_id}/content` 获取元数据和内容。
- `code_interpreter_call.outputs` 的 `[artifacts]` 摘要会显式带出 `file_id`，方便现有客户端从 response payload 直接跳转到下载接口。
- 本地 Files 记录会额外保存 `container_id`。客户端可用 `/v1/containers/{container_id}/files` 列出该 container 下持久化的产物，用 `/v1/containers/{container_id}/files/{file_id}` 获取文件摘要，用 `/v1/containers/{container_id}/files/{file_id}/content` 下载原始字节。
- 当前不把二进制或大文本写入审计、usage record、tool_result 或 Responses payload；模型二次回灌只拿到文件名、大小和可下载 `file_id` 摘要。
- 产物数量超过 `JUHE_AI_CODE_INTERPRETER_MAX_ARTIFACTS` 时，只记录 `metadata.artifacts_omitted_count`；单文件超过 `JUHE_AI_CODE_INTERPRETER_MAX_ARTIFACT_KB` 时不持久化正文，标记 `omit_reason=file_too_large`。
- 符号链接、特殊文件和无法 stat 的路径不作为可用产物暴露；下载必须走受控 Files 存储和授权校验，不能直接暴露临时目录路径。
- 该 container files 兼容壳只覆盖本地 code interpreter 产物的列表、摘要和内容下载；暂不支持创建 container、向 container 上传文件、容器生命周期租约、容器删除、完整 OpenAI `cfile_*` ID 语义或 VM 级隔离。

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
- JSON 非流式和 Responses SSE 免批路径已经完成模型驱动工具循环：仅当 `require_approval=never`，或请求 `allowed_tools` 全部命中 `require_approval.never.tool_names` 时，`tools/list` 结果才会导入 Anthropic client tools；Anthropic 返回 `tool_use` 后，网关执行 MCP `tools/call`，把结果作为 Anthropic `tool_result` 二次回灌，并输出 OpenAI Responses `mcp_list_tools` / `mcp_call` / 最终 assistant message。
- `runtimeConfig.mcpProxy.servers` 命中 allowlist 后，executor 使用 MCP JSON-RPC POST 顺序执行 `initialize`、`notifications/initialized`、`tools/list` 和 `tools/call`。
- allowlist server 字段包含 `label`、`serverUrl`、`enabled`、`allowedTools`、可选 `authorization` 和 `allowRequestAuthorization`；请求里的 `authorization` 只有在 allowlist 明确允许时才发给 MCP server，不写响应。
- `allowed_tools` 会同时受 server allowlist 和请求字段过滤；没有可用工具时返回普通 assistant message，不调用 `tools/call`。
- `require_approval` 省略或为 `always` 时返回 `mcp_approval_request`；`require_approval=never` 或免批工具命中时才执行 `tools/call`。
- 默认审批路径会写入业务库 `pending` 记录，并生成绑定 `server_label`、`server_url`、`tool_name`、`arguments_digest` 和当前 scope 的 `approval_request_id`；收到 `mcp_approval_response` 时必须命中该记录，否则本地 400 / 403，且不请求 Anthropic、不执行 `tools/call`。
- MCP approval 真实路径必须落入业务库状态机，审批记录绑定当前 `system_account_id`、`api_key_id`、`group_id`、`server_label`、`server_url`、`tool_name` 和 `arguments_digest`；跨 API Key / 分组复用 approval id 必须本地拒绝，不执行远程工具。
- approval 状态首段包含 `pending`、`approved`、`rejected`、`expired`、`consumed`：生成 `mcp_approval_request` 时写入 `pending`；收到 `approve=false` 时写入 `rejected` 并返回普通 assistant guidance；收到 `approve=true` 时仅允许从未过期 `pending` 进入 `approved`，执行前标记 `consumed`，防止同一个 approval id 被重复执行。
- 人工审批 API 首段只允许把 `pending` approval 标记为 `approved` 或 `rejected`，用于后台队列管理；该 API 不执行远程 MCP `tools/call`。真正远程执行仍必须由客户端后续请求携带匹配的 OpenAI Responses `mcp_approval_response` 触发，执行前再把 `approved` 标记为 `consumed`。
- 每次进入远程 MCP `tools/call` 都必须写入 execution record，绑定当前 `system_account_id`、`api_key_id`、`group_id`、`trace_id`、`approval_request_id`、server、tool、arguments digest、耗时、状态和错误摘要；成功记录 output digest / bytes / truncated，不保存远程输出正文。
- execution record 必须提供管理侧和用户侧分页查询入口：管理侧可按 `systemAccountId`、`apiKeyId`、`groupId`、`traceId`、`approvalRequestId`、server、tool、status 和时间窗口筛选；用户侧只能查看当前系统账户 scope。查询结果仍只返回摘要、digest、字节数、truncated、错误码和省略元数据，不提供远程输出正文读取接口。
- `tools/call` 输出按 `mcpProxy.maxOutputBytes` 截断，并在 `mcp_call.metadata.output_truncated` 标记省略。
- 当前 JSON 非流式免批路径会把 MCP `tool_result` 回灌给 Anthropic 生成最终 `output_text`；Responses SSE 免批路径会先缓冲首轮 Anthropic SSE，再执行同一套受限多轮工具循环，并以 OpenAI Responses typed SSE 输出完整 terminal snapshot；默认 approval 或非免批工具仍只返回本地 `mcp_approval_request`。
- MCP JSON-RPC transport 已补齐有限重试和重定向拒绝诊断：连接失败、超时和可重试 HTTP 状态可以在当前 MCP server 上重试；3xx 不跟随，返回稳定本地错误码并只暴露状态码和目标摘要。重试次数和延迟由 `JUHE_AI_MCP_PROXY_MAX_RETRIES` / `JUHE_AI_MCP_PROXY_RETRY_DELAY_MS` 控制。
- MCP approval pending 记录默认 5 分钟过期，由 `JUHE_AI_MCP_PROXY_APPROVAL_TTL_SECONDS` 控制；过期 approval response 本地拒绝，不调用远程 MCP。
- mockai 可在显式开启私有上游 allowlist 时使用 loopback HTTP MCP server；生产路径仍要求 HTTPS。

当前首批 executor 仍不是完整 hosted MCP 等价实现：

- JSON 非流式和 Responses SSE 免批路径已完成“模型选择工具 + 网关执行工具 + Anthropic tool_result 回灌 + OpenAI Responses 工具轨迹”第一段；默认 approval 或非免批工具仍返回本地 `mcp_approval_request`，不请求 Anthropic，也不执行 `tools/call`。
- 当前 MCP 工具循环支持受限多轮，默认上限为 4 轮；达到上限后会受控收口，不继续无限递归调用工具。
- Responses SSE 当前采用缓冲式受限多轮工具循环：首轮 Anthropic SSE 被聚合成完整 `tool_use` 后再执行 MCP 和二次回灌，最终一次性输出 OpenAI typed SSE。普通 function call 已按 Anthropic `input_json_delta.partial_json` 输出 `response.function_call_arguments.delta/done`；MCP `mcp_call` 在完整参数已知后输出单片段 `response.mcp_call_arguments.delta`、`response.mcp_call_arguments.done` 和 `response.mcp_call.in_progress`，成功时输出最终 `response.output_item.done`，失败时输出 `response.mcp_call.failed` 和带 `error` 的最终 `mcp_call` item，再用 `response.completed` 收口。这不是 OpenAI 原生远程 MCP 的实时逐片段执行流；`response.mcp_call.failed` 只表示单次工具调用失败，不能被响应检查策略当作整个 Responses 流失败事件。
- approval request 当前已完成业务库状态机和人工审批 API 首段：`pending` / `approved` / `rejected` / `expired` / `consumed` 与当前 API Key / 分组 scope 绑定；后台 API 只改审批状态，不执行远程 MCP。execution record 首段已写入业务库并提供管理侧 / 用户侧摘要查询 API，后续再补 UI 审批队列、完整查询页面和长期审计视图。
- 远程 Streamable HTTP JSON 响应、Streamable HTTP POST 返回 `text/event-stream` 的 JSON-RPC result frame、legacy HTTP+SSE 双端点长连接状态机、有限重试和重定向拒绝诊断已有 mock 回归；真实第三方 HTTP-SSE server 联调仍需继续补齐。
- server allowlist 首段已进入业务库并提供管理侧 / 用户侧 API；运行时先读取当前系统账户 scope 下的 DB enabled server，再合并 `JUHE_AI_MCP_PROXY_SERVERS_JSON` bootstrap / 应急来源。环境变量不再作为唯一生产配置面。
- server 可用性诊断和工具 schema 缓存必须是显式人工 / 后台动作，不能由列表页或普通详情页隐式触发远程 `tools/list`。诊断入口只执行 `initialize`、`notifications/initialized` 和 `tools/list`，复用 MCP proxy 的超时、重试、重定向拒绝、响应体上限和输出省略规则；诊断结果只保存状态、工具数量、错误码、错误摘要、耗时和工具 schema 摘要，不保存明文 authorization、远程输出正文或请求中的临时授权。

OpenAI Responses 的 MCP 契约决定了真实 proxy 需要同时处理四类状态：

- server 定义：`type=mcp`、`server_label`、`server_url` 或 `connector_id`、可选 `server_description`、`authorization`、`allowed_tools`、`defer_loading` 和 `require_approval`。
- 工具导入：成功时输出 `mcp_list_tools`，并把工具定义保留在后续上下文；失败时输出可诊断的失败 item 或本地协议错误。
- 工具审批：默认应生成 `mcp_approval_request`；只有 `require_approval=never` 或策略明确免批的工具才允许直接调用。
- 工具调用：成功时输出 `mcp_call`，失败时在 `mcp_call.error` 中保留 MCP 协议、执行或 transport 错误摘要。

首批 real proxy 的长期 schema 边界：

| 数据对象 | 用途 | 关键字段 |
| --- | --- | --- |
| MCP server allowlist | 管理允许访问的远程 MCP server | `id`、`system_account_id`、`label`、`server_url`、`description`、`enabled`、`allowed_tool_names`、`default_approval_policy`、`timeout_ms`、`max_retries`、`max_body_bytes`、`output_limit_bytes`、`allow_request_authorization`、`authorization_ref`、`created_at`、`updated_at` |
| MCP auth reference | 保存或引用访问远程 MCP 的凭据 | `server_id`、`credential_ref`、`auth_type`、`expires_at`、`scope_summary`；不保存明文 token 到普通业务字段 |
| MCP tool cache | 缓存 `mcp_list_tools` 结果，降低每轮导入成本 | `server_id`、`tool_name`、`input_schema`、`description`、`annotations`、`etag`、`expires_at`、`last_checked_at` |
| MCP server diagnostic | 记录显式诊断结果 | `id`、`server_id`、`system_account_id`、`status`、`tool_count`、`error_code`、`error_message`、`started_at`、`finished_at`、`duration_ms`、`omission_metadata` |
| MCP approval request | 记录待审批调用 | `approval_request_id`、`server_id`、`tool_name`、`arguments_digest`、`arguments_preview`、`status`、`created_by_api_key_id`、`expires_at` |
| MCP execution record | 审计真实调用 | `call_id`、`system_account_id`、`api_key_id`、`group_id`、`trace_id`、`approval_request_id`、`server_label`、`server_url`、`tool_name`、`arguments_digest`、`output_digest`、`output_bytes`、`duration_ms`、`status`、`error_code`、`omission_metadata`；只提供摘要查询，不提供远程输出正文读取 |

首批 real proxy 的执行流程：

1. Bridge 发现 Responses `tools[].type=mcp` 且 Runtime Registry 判定为 `local_runtime`。
2. 校验 `server_label` / `server_url` 命中 allowlist；同一请求内 `server_label` 不能重复，`server_url` 和 `connector_id` 不能同时存在。长期路径先读业务库 allowlist，再合并环境 bootstrap allowlist；冲突时业务库明确禁用优先。
3. 从加密凭据引用或本次请求 `authorization` 构造 MCP transport header；`authorization` 只进入远程请求，不写入响应、审计正文或普通日志。
4. 如果上下文已有可信 `mcp_list_tools` 且未过期，可复用；否则向远程 server 执行 tools/list，再按 `allowed_tools` 过滤。
5. 根据 `require_approval`、server 默认策略和工具风险等级决定是否生成 `mcp_approval_request`。
6. 后台人工审批 API 可把匹配当前系统账户 scope 的 `pending` approval 标记为 `approved` 或 `rejected`；该 API 只改变状态，不调用远程 MCP。
7. 收到匹配当前 server / tool / arguments、当前 API Key / 分组 scope 且未过期的 `mcp_approval_response` 后再执行 tools/call；`approval_request_id` 不存在、scope 不匹配、状态不是 `approved` 或 `pending`、已过期或 arguments digest 不匹配时本地拒绝，拒绝审批时写入 `rejected` 并生成普通 assistant guidance，不调用远程 MCP。
8. 执行 `tools/call` 时写入 execution record；成功时记录 output digest / bytes / truncation，失败时记录错误码和错误摘要，但不保存远程输出正文。
9. 审计查询通过 `/__aisys__/api/mcp-execution-records` 和 `/__aisys__/api/my-mcp-execution-records` 分页读取摘要，按系统账户权限裁剪 scope。
10. 将远程工具输出转成 Responses `mcp_call.output` 字符串；超限内容截断或转文件引用，并在审计里记录 omission metadata。
11. SSE 路径按 Responses 事件输出 `response.output_item.added`、`response.output_item.done`、必要的 text delta 和最终 `response.completed`；不使用 Chat `[DONE]`。

server 诊断与工具缓存流程：

1. 管理侧或用户侧对自己 scope 内的 enabled MCP server 显式调用诊断接口；列表页、详情页和普通筛选不触发远程访问。
2. 诊断接口按 allowlist 记录构造 runtime server，只允许使用已保存的安全字段；如需要一次性请求 authorization，只能在 server 开启 `allow_request_authorization` 时由本次诊断请求携带，且不保存、不回显。
3. 诊断复用 MCP proxy transport 执行 `initialize`、可选 `notifications/initialized` 和 `tools/list`，按 server 级 timeout / retry / max body bytes 限制收口。
4. 成功时用 `tools/list` 结果替换当前 server 的工具 schema cache：按工具名唯一保存 `description`、`input_schema`、`annotations`、`last_checked_at` 和 `expires_at`；返回给前端的仍是本地缓存摘要。
5. 失败时写入 diagnostic record，保留稳定错误码和短错误摘要，不清空已有成功缓存，避免一次网络波动导致运行期失去历史工具定义参考。
6. 诊断接口不执行 `tools/call`，不生成 approval request，不写 execution record；它只证明远程 server 可连接、工具可导入和 schema 可缓存。

首批 real proxy 的拒绝边界：

- `connector_id`：返回 guidance 或本地错误，直到 connector adapter 独立实现。
- 未命中 allowlist 的 `server_label` / `server_url`：本地 OpenAI 错误，不连接远程 server。
- 请求同时提供 `server_url` 和 `connector_id`、同请求重复 `server_label`、没有任何 server 标识：本地 OpenAI 错误。
- `authorization` 与自定义 `headers.Authorization` 同时出现：本地 OpenAI 错误，避免凭据来源不清。
- `allowed_tools` 指定不存在的工具：工具导入失败或返回空工具集，不把未允许工具暴露给模型。
- 诊断请求未命中 scope、server disabled、server URL 非 HTTPS、请求 authorization 未被 allowlist 允许：本地拒绝，不访问远程 server。
- approval request 过期、跨 API Key / 分组 / 授权边界、approval id 不匹配：本地 OpenAI 错误，不调用远程 MCP。
- 远程输出超限、非 UTF-8 文本、大二进制、URL 回传、图片 URL 回传：按输出策略截断、引用或拒绝；不能把远程大 payload 原样写入审计正文。

server allowlist 存储化首段：

- 新增业务库 `openai_compatible_mcp_servers`，按 `system_account_id` 归属；管理侧可按系统账户查看 / 管理，用户侧只能管理自身 scope。
- 唯一性以 `system_account_id + label` 为准，同一系统账户下 label 不能重复；请求执行时必须同时匹配 label 和 `server_url`，避免 label 被重定向到其他 server。
- 字段只保存安全摘要和引用：`authorization_ref` 只引用凭据，不把明文 token 放入普通字段、响应、审计正文或前端表格。
- `allowed_tool_names` 为空表示允许远程 `tools/list` 返回的所有工具；非空时还要继续受请求 `allowed_tools` 过滤。
- `default_approval_policy` 首段只允许 `always` / `never`；默认 `always`。请求声明 `require_approval=never` 时仍不能覆盖 server 上更严格的 `always` 策略。
- 运行时合并规则：业务库 enabled server 优先；环境变量 server 可作为 bootstrap，仅在没有同 scope 同 label 禁用记录时参与；生产 UI 应提示环境变量项不可在数据库内直接编辑。
- 管理 API 不触发远程 `tools/list`。server 可用性检测、工具缓存和 schema 校验后续单独做诊断任务，避免列表页请求链路访问外部 MCP。

首批 real proxy 的 transport 策略：

- 仅支持 HTTPS `server_url`。
- 支持 MCP Streamable HTTP 和 legacy HTTP+SSE transport。按 MCP 2025-06-18 规范，客户端先向用户配置的 `server_url` POST `initialize`；如果成功，视为 Streamable HTTP；如果返回 4xx，例如 404 / 405，再 GET 同一个 `server_url`，等待 SSE 首个 `endpoint` 事件，并把后续 JSON-RPC 通过该 endpoint POST。
- Streamable HTTP 路径必须继续支持两种响应：`application/json` 单个 JSON-RPC 对象，以及 POST 响应直接升级为 `text/event-stream` 并在 `message` 帧里返回匹配 `id` 的 JSON-RPC response。
- legacy HTTP+SSE 路径必须保持 GET SSE 连接，等待 `event: endpoint` 后解析 endpoint URI；endpoint 必须按 `server_url` 相对解析且与原 `server_url` 同 origin，禁止跨域 endpoint、禁止跟随重定向、禁止把 endpoint query 中的敏感内容写入错误正文。
- legacy HTTP+SSE 后续请求必须向 endpoint POST JSON-RPC；请求型 JSON-RPC 要从 GET SSE 流的 `message` 事件里读取匹配 `id` 的 response，忽略无关 notification / request；notification 型 JSON-RPC 只要求 endpoint POST 成功，不等待 response。
- legacy HTTP+SSE 连接必须受 server 级 timeout、SSE 累计字节上限和请求 abort signal 控制；连接 EOF、超时、endpoint 缺失、endpoint 跨域、未收到匹配 `id` 都返回稳定本地 transport 错误码。
- 每次 initialize、tools/list 和 tools/call 都有独立超时、总字节上限和重试上限；重试只针对连接失败、超时和可重试 HTTP 状态，不重放 JSON-RPC 协议错误。legacy HTTP+SSE 只有在尚未向 endpoint 成功 POST 具体 JSON-RPC 前允许按初始化探测重试；已经发送的 `tools/call` 不自动重放，避免远程副作用重复执行。
- 默认不跟随任何重定向；收到 3xx 时本地拒绝并返回 `openai_anthropic_bridge_mcp_proxy_redirect_blocked`，如后续允许，必须在 allowlist 中记录最终域名。
- 远程错误只影响当前工具运行，不把上游模型账号标记为不可用。
- 参考官方规范：[MCP 2025-06-18 Transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) 和 [MCP 2024-11-05 HTTP with SSE](https://modelcontextprotocol.io/specification/2024-11-05/basic/transports)。

真实 MCP proxy 启用前必须新增专门 mockai：本机 mock MCP server 覆盖 tools/list 成功、tools/list 失败、tools/call 成功、tools/call 错误、SSE transport、auth 缺失、output 超限、approval required、approval reject、approval expired、allowlist reject 和敏感字段扫描。

### 5.4 Computer Adapter

`computer` 需要状态和动作协议，不能只给模型一段说明：

- OpenAI GA 形态是 Responses `computer` tool 输出 `computer_call`，其中 `actions[]` 由外部 harness 执行，再把新截图作为 `computer_call_output` 回传。网关不能把它简化成普通文本回答，也不能假装已经看见或操作了屏幕。
- 承接路径分两类：
  - `native_bridge`：上游账号 profile 明确支持 Anthropic computer use 或其他原生 UI action 能力时，才允许把 OpenAI `computer` 映射为上游原生工具；上游返回的动作必须再映射回 Responses `computer_call`，由下游客户端或后续 adapter 回传 `computer_call_output`。
  - `local_runtime`：网关持有受控浏览器 / VM adapter，由 adapter 捕获屏幕状态、执行动作并返回新状态；该模式必须有显式环境隔离、域名 allowlist、动作 allowlist、审批策略和审计记录。未配置 adapter 时继续 guidance / reject，不请求 Anthropic。
- 首批只允许受控浏览器环境，不允许宿主桌面。推荐 Playwright / Selenium / container browser；浏览器启动时必须清空宿主环境变量，禁用扩展和本地文件访问，默认只开放 allowlist 域名。
- 会话状态必须独立建模：`session_id`、`environment`、`viewport`、当前 URL 摘要、允许域名、动作计数、最后一次截图摘要、截图省略引用、创建时间、最近活动时间、TTL 和 trace id。不能把截图正文、DOM 全量、cookie、localStorage 或页面 HTML 写入普通响应 / 审计正文。
- 动作协议首批只接受 OpenAI GA 中可归一化的安全动作：`screenshot`、`wait`、`move`、`click`、`double_click`、`scroll`、`type`、`keypress`、`drag`。每个动作都必须做坐标、按键、文本长度、drag path 长度和动作数上限校验。
- 高风险动作必须分级：
  - 默认拒绝：宿主文件访问、本地应用、剪贴板读取、下载后执行、浏览器扩展、跨域 endpoint、系统设置和 OS 快捷键。
  - 必须人工确认：登录后提交、发帖 / 发信 / 下单、付款、删除、权限变更、敏感信息输入、验证码、绕过安全提示。
  - 可预授权：用户在原始 prompt 中明确允许的站点登录、上传指定文件、接受常规站点确认，但仍需记录授权来源。
- `computer_call_output` 输入必须校验 call id、session id、截图 MIME、尺寸和大小；`include=computer_call_output.output.image_url` 在没有受控截图引用时继续本地拒绝，不能返回空字段伪装成功。
- 输出语义：
  - 只请求截图时返回 `computer_call`，`actions=[{type:"screenshot"}]`，`status=completed`。
  - 动作可执行但需要下游执行时返回 `computer_call`，不由网关代执行。
  - 本地 adapter 代执行时，Responses SSE 必须至少输出 `response.output_item.added`、`response.output_item.done` 和 `response.completed`；动作拒绝输出 `computer_call` 失败 metadata 或正常 guidance message，不能抛 500。
  - adapter 失败、截图超限、动作越权和超时必须使用稳定错误码，并且只影响当前工具运行，不把模型账号标记为不可用。
- mockai 先行：`computer=mock` 已返回固定 `computer_call` / `computer_call_output` 收口外形，不启动浏览器、不访问网页、不请求 Anthropic。
- `computer=local_runtime` 首段先落 adapter 接口和受控执行闸门：未配置 adapter 时本地返回 `service_unavailable`，不上游；测试 adapter 只允许输出结构化 `computer_call` / 普通 message、会话摘要、动作摘要和截图省略引用，不保存或回显截图正文。
- 真实浏览器运行时首段采用 HTTP sandbox adapter 桥，而不是把 Playwright 直接放进主网关进程。只有同时配置 `JUHE_AI_HOSTED_TOOL_COMPUTER_MODE=local_runtime`、`JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENABLED=true` 和 `JUHE_AI_COMPUTER_BROWSER_ADAPTER_ENDPOINT` 时才启用。该 endpoint 应指向本机或内网容器浏览器服务，由外部 sandbox 负责 Playwright / Selenium / browser container、域名 allowlist、动作执行、截图持久化和人工确认；网关只负责转发当前 Responses `computer` 请求、限制超时 / 响应大小、还原 OpenAI `computer_call` / message 外形，并继续脱敏 `image_url`、base64、prompt、token、cookie、DOM 和 HTML。
- HTTP sandbox adapter endpoint 不能包含用户名密码、query 或 fragment；本地 HTTP 只允许 loopback，远程必须使用 HTTPS。adapter 响应只接受 JSON：`message` 必填，`call` 可选，`call.actions[]` 继续经过动作字段白名单和文本省略；超时、非 2xx、非 JSON、响应体超限或 schema 异常都返回稳定本地错误，不把模型账号标记为不可用。
- 后续完整真实 adapter 再补内置 Playwright / container browser 管理、会话 TTL、截图引用持久化、域名 allowlist、动作 allowlist、人工确认和审计摘要。

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
| code interpreter | mock 模式 JSON / SSE 生命周期、`include=code_interpreter_call.outputs` 固定 logs、不执行代码、不请求 Anthropic；local_runtime worker 覆盖成功、stderr、超时、输出超限、安全 env 不泄漏、文件产物元数据摘要、本地 Files `file_id` 下载和 `/v1/containers/{container_id}/files` / content 兼容壳；真实容器沙箱后再补 create/upload/delete container、生命周期和更强网络隔离 |
| MCP | mock 模式 allowlist 命中、未授权 server、authorization 不回显、approval request、allowed_tools 过滤、JSON / SSE `mcp_list_tools` / `mcp_call` 生命周期；local_runtime 覆盖 approval 状态机、scope、reject、expired、replay、execution record、Streamable HTTP / POST-SSE / legacy HTTP+SSE mock transport 和 tool 输出超限；后续再补真实第三方 HTTP-SSE 联调、人工审批 UI 和 execution record 查询页面 |
| computer | adapter 未配置、`computer=mock` JSON / SSE 固定 `computer_call`、`computer_call_output` 收口且不回显截图正文、不请求上游；`computer=local_runtime` 首段覆盖 adapter gate、测试 adapter `computer_call` 输出、动作/会话 metadata、截图正文省略和不上游；HTTP sandbox adapter 首段覆盖显式配置、adapter HTTP 调用、响应大小限制、JSON schema 归一化、动作/metadata 脱敏和不上游；后续完整 Playwright / container adapter 再补动作执行、域名 allowlist、截图引用持久化、人工确认和审计摘要 |
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
