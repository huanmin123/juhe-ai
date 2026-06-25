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
- `capabilityMode`：`native_upstream`、`local_runtime`、`guidance`、`reject`。
- `enabled`：系统级、账号级和 API Key / 分组级共同决定。
- `limits`：超时、输出大小、文件大小、并发、网络策略、审计级别。

Bridge 层只读取 registry 决策，不直接判断具体运行时实现细节。

当前首批 registry 已落地为运行时配置骨架，只支持两种保守模式：

| 模式 | 行为 |
| --- | --- |
| `guidance` | 默认值。返回 OpenAI 形态 agent guidance，不请求 Anthropic，不执行本地工具。 |
| `reject` | 返回本地 OpenAI 风格错误，不请求 Anthropic，不执行本地工具。 |

真实 `native_upstream` / `local_runtime` 模式必须等对应执行器、权限、审计和 mock 回归齐全后再加入配置枚举，避免通过环境变量提前宣称能力可用。

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

### 5.3 MCP Proxy

MCP 只允许访问 allowlist 中的 server：

- server label 和 URL 必须匹配 allowlist。
- auth token 只能来自加密配置，不从用户 prompt 注入。
- `require_approval` 需要映射为本地 approval policy；没有 approval 实现时必须 guidance 或拒绝。
- 每次 tool call 记录 server、tool name、arguments 摘要、耗时、状态和错误码。
- MCP 返回的大内容必须按大小上限截断或转文件引用。

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
| code interpreter | 成功、stderr、超时、输出超限、文件产物上限、网络默认禁止 |
| MCP | allowlist 命中、未授权 server、auth 缺失、approval required、tool 输出超限 |
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
