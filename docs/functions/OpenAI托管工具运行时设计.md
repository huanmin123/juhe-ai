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
| `mcp` | 客户端本地 MCP 或原生支持 MCP 的上游 | 不由网关服务端承接 | OpenAI 形态 guidance，不请求 Anthropic、不连接 MCP server |
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
| `mock` | 仅用于 mockai / 回归验证的本地模拟输出。不得执行外部命令、不得访问网络、不得宣称生产可用。首批允许 Responses `code_interpreter` 输出模拟的 `code_interpreter_call` 生命周期。MCP 不提供 mock/proxy runtime。 |
| `local_runtime` | 用于需要真实执行器的受限运行时入口，例如 code interpreter 和 computer adapter；如果对应 executor 尚未接入，必须返回本地 OpenAI 风格 `service_unavailable`，不得继续请求 Anthropic、不得在主 Web 进程执行。MCP 不进入该模式。 |

真实 `native_upstream` / `local_runtime` 模式必须等对应执行器、权限、审计和 mock 回归齐全后再加入配置枚举，避免通过环境变量提前宣称能力可用。MCP 固定为 guidance，不提供服务端 runtime、mock、proxy、approval、execution record 或管理 UI。

| 环境变量 | 工具 | 默认 |
| --- | --- | --- |
| `JUHE_AI_HOSTED_TOOL_CODE_INTERPRETER_MODE` | `code_interpreter` / `container` | `guidance` |
| `JUHE_AI_HOSTED_TOOL_COMPUTER_MODE` | `computer` | `guidance` |
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

### 5.3 MCP 边界

MCP 不在网关服务端执行。OpenAI Responses `tools[].type=mcp`、Anthropic server tool 中的 MCP 语义，以及客户端本地 MCP 能力，都不由本项目中转层扩展协议或代理执行。

当前固定策略：

- Runtime Registry 对 `mcp` 固定返回 `guidance`，不读取 `JUHE_AI_HOSTED_TOOL_MCP_MODE`。
- Bridge 不校验、登记、连接或代理 `server_label`、`server_url`、`connector_id`、`authorization`、`allowed_tools`、`require_approval` 等 MCP 执行字段。
- 网关不输出 `mcp_list_tools`、`mcp_call`、`mcp_approval_request` 或 `mcp_approval_response` 的服务端执行结果；如果上游原生返回这些 OpenAI Responses item，只按普通协议事件处理，不代表网关执行过 MCP。
- 不新增 MCP server allowlist、auth reference、approval 状态机、execution record、诊断接口、工具缓存、管理 API 或前端 UI。
- 命中 MCP 能力缺口时返回 OpenAI 形态 agent guidance，提示客户端使用本地 MCP、调用方 agent 自行执行，或切换到原生支持该 MCP 能力的上游；本轮不请求 Anthropic、不连接远程 MCP server、不伪造工具结果。

这条边界是产品决策，不是暂缺实现。后续如需 MCP，应优先引导客户端本地 MCP 或选择原生支持该能力的供应商，而不是在中转服务端扩展 MCP 协议。
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
- `mcp`、远程 registry、权限化动态工具发现不纳入本地展开；MCP 统一返回客户端 / 本地 agent guidance。

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
| MCP | Responses `tools[].type=mcp` 返回正常 guidance，文案指向客户端本地 MCP / 原生上游；不请求 Anthropic、不连接远程 MCP、不输出网关生成的 `mcp_list_tools` / `mcp_call` / `mcp_approval_request` |
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
