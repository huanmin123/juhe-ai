# AI 问答工具体系与主子模型设计

- 状态：已实施（2026-09-28；阶段 1/2/3 提交 4a5309674 / 195b1cbb6 / 前端改造，候选缺口修复 31a085f97）
- 日期：2026-09-28
- 前置：《AI 问答设计》8.6（已按本设计改写为「工具体系与主子模型」节）——能力数据链（8.5 静态快照兜底、custom 目录行能力继承）继续有效并被本设计复用。
- 验证记录（2026-09-28 隔离实例 + 真实上游）：GPT 纯对话、未绑定 `tool.binding_required` 引导（9 候选 + UserHint 回喂）、GROK 跨账户搜索（grok-4.7 子代理 24 来源实时数据）、GPT 生图（gpt-image-2 真实出图 1370×1148）、非候选绑定 400 + 候选返回、浏览器端免弹窗直进/账户流/绑定弹窗/时间线来源（31 来源 8 链接可展开）全通过。已知限制：GROK 生图账户建模受 xai 供应商 openai 协议档案的目录断言限制（`supportedModels` 不接受 images 协议模型），grok-imagine 生图候选需账户建模层提供 images 档案或映射通道后才能落地；GPT 系生图（gpt vendor 协议直通）不受影响。

## 1. 背景与动机

现状：AI 对话的联网搜索依赖「模型目录声明 `web_search` → 偏好 Responses 协议 → 注入上游 hosted `web_search` 工具」。该路径存在结构性问题：

1. hosted 工具是 OpenAI Responses 独有能力（Chat Completions 只支持 function tools），不同供应商/中转的实际支持差异大；主对话协议被能力绑定，能力不可用拖累协议选择。
2. 主模型必须自己具备该能力才有搜索/生图，模型选择与能力耦合。
3. 能力矩阵（`toolCapabilities`）语义复杂（模型声明 × 协议 × 路由三重判定），用户难以理解。

改造方向（用户裁决）：**主对话恒走 Chat Completions（最通用的协议）；搜索与生图改为「模型工具」——由会话级绑定的子模型执行，主模型通过 function calling 调用，结果回喂主模型。** 能力与主模型彻底解耦。

## 2. 已确认决策

1. 主对话协议恒为 `chat_completions`；不再由工具能力驱动协议偏好。
2. 工具分两类：**普通工具（代码工具）**——后端代码直接执行；**模型工具（受限子代理）**——绑定子模型执行，单一职责（搜索子代理只搜索、生图子代理只生图），不是通用 agent。
3. 模型工具的子模型绑定为**会话级**，绑定粒度是「**AI 账号 + 模型**」二元组（不是裸模型）：同名模型在不同账号上的实际能力差异大，绑定到账号才能把执行目标钉死。搜索、生图各自可绑定一组。
4. 绑定候选 = 会话作用域内可派发账号 × 该账号实际支持且具备对应能力的模型，且必须是「已实现执行器的能力白名单」内的组合：搜索候选限定「账号支持所需端点（`responses_sse`）且其模型在目录声明 `web_search`」；生图候选限定「可路由注册图像模型的账号（当前仅 GPT/Grok 两家：`gpt-image-2`、grok-imagine 系）」。候选条目对用户呈现为「账号名 · 模型名」。
5. 未绑定时工具仍注入；主模型发起调用后，前端弹设置引导（同时工具面板常驻设置入口）。
6. **删除旧 hosted 直连路径，不留尾巴、不留配置开关**（避免误解与维护负担）。
7. 改动范围仅限 AI 对话域（chat 包 + 对话前端）；不改网关链路、不改调度、不改协议转换。
8. 一次完整交付（工具类型系统 + web_search 子代理 + 生图显性化 + 目录「协议 × 工具」矩阵细化 + 旧路径删除）；与《AI 问答会话账户唯一绑定设计》（会话仅账户绑定、免弹窗直进、工具候选放宽到全部授权账户）同批实施。
9. 供应商模型目录细化为「协议 × 工具」矩阵（见 6.4）：候选过滤以「该协议下可用工具」二维数据为准，不再依赖代码内隐式绑定知识。
10. **不做向后兼容**：一维 `supportedTools` 字段直接退场、消费方全量切换，不保留派生兼容层与新旧并存；生产发布即切换（能力数据在静态快照内，随发布全量刷新，无需刷库）。

## 3. 非目标

- 不支持 Claude messages / Gemini generateContent 作为主对话协议。
- 不实现通用子 agent（子代理只服务单一功能）。
- 不在本期实现纯搜索 API 后端（博查/Tavily 等）；架构留插拔位，将来可作为 web_search 工具的另一种执行后端接入。
- 不改动 `/v1` 网关链路、账户调度、模型映射与协议转换。
- 不改动管理面模型目录（能力数据链保持现状，作为绑定候选过滤的数据源）。

## 4. 总体架构

```
AI 对话（chat 包 + 对话前端）
├─ 主模型：用户选择的对话模型，恒 chat_completions
├─ 工具体系（function calling 注入，对主模型透明）
│   ├─ 普通工具（code）：代码直接执行（现有 diagnostic_echo 等）
│   └─ 模型工具（model-backed）：会话级绑定子模型
│       ├─ web_search      → 绑定「目录声明 web_search 的模型」
│       └─ generate_image  → 绑定图像模型（现有 default_image_model 升级）
├─ 子代理执行：经进程内 /v1 链固定派发到绑定的「账号+模型」（复用账户/调度/协议，网关零改动）
└─ 结果回喂：工具结果作为 tool result 回到主模型，继续本轮生成
```

职责边界：主模型只负责对话与判断何时用工具；子代理只负责执行单一能力并把结构化结果交回；两者通过工具调用协议（function calling）通信，互不感知对方实现。

## 5. 工具类型系统

- 工具注册器（现有 `generation_tools.go`）扩展类型元数据：`kind: "code" | "model"`。
- 对主模型的注入完全一致：两者都是 function 工具定义（名称、描述、参数 schema），主模型不感知后端执行方式。
- 执行路由：`code` 工具走现有注册函数直接执行；`model` 工具由子代理执行器处理（见第 6 节）。
- 扩展预留：将来「时间查询、URL 读取」等走 `code`；「深度研究、长文摘要」等走 `model`（绑定擅长该任务的模型），体系不再变化。

## 6. 模型工具契约

### 6.1 web_search（搜索子代理）

- 工具定义：`web_search`，参数为搜索意图（由主模型根据用户问题生成搜索词，schema 简单：`{ query: string }`）。
- 执行：后端用会话绑定的「账号 + 模型」发起一次**流式**子调用（`stream: true`），固定派发到该账号（经 chat 面现有调度覆盖机制注入，不做常规调度漂移）；子调用**按绑定模型与账号的实际能力走它自己的协议**（例如绑定的模型声明 `web_search` 且账号支持 `responses_sse`，则子调用为 Responses + hosted `web_search`；该判定只作用于子调用，不影响主对话协议）。子调用请求携带 `reasoning: {summary: "auto"}` 请求子代理思考摘要（推理模型返回 reasoning summary 增量；上游不支持时按失败重试链路处理，不降级协议）。
- **执行过程可视化（§10.3 契约的数据面）**：子代理流式增量（思考摘要、搜索动作、回答摘要）经内容块投影通道（`content_block.updated` patch，item 携带 `progress` 字段 `{stage: "reasoning"|"searching"|"answering", reasoning, actions[], answer}`）渐进下发——增量节流合并，默认 400ms，阶段切换即时。思考摘要按 `reasoning_summary_part.added`（或 `reasoning_text_part.added`）分段：新段开始前在已有摘要后补空行分隔，避免上游各段自带的 `**强调**` 标记连成 `**a****b**`；分段后的摘要由前端按 Markdown 渲染。搜索动作在 `output_item.added` 与 `output_item.done` 两个时机提取并去重（官方流 added 带 action；部分中转上游仅 done 携带完整 query/queries）。`progress` 为瞬态过程数据：流内驱动前端实时展示，不落库（落库前剥离，tool_call item 仅终态 query/sourceCount/sources）。生图无流式过程，`started` 即带 `progress {stage:"generating"}`。最终文本与来源以 `response.completed` 事件内的完整响应为权威（复用非流式提取逻辑），增量仅作展示。
- 子调用经进程内 `/v1` 链派发（与现有生图工具执行路径同构），权限上限为会话作用域（bindMode 对应的分组/账户/Key 范围），绑定的账号必须在该作用域内。
- 结果回喂：子调用的回答与来源列表裁剪、格式化为 tool result（文本 + 来源 URL 清单，带字节上限），回喂主模型；主模型基于结果作答并在正文中引用来源。
- 超时与错误：子调用整体超时（默认 120s，可配）；失败时返回明确的工具错误消息（不中断主对话轮次，主模型可向用户说明搜索暂不可用）。
- 审计：子调用产生独立的 trace 与用量记录（经 `/v1` 链天然具备）。

### 6.2 generate_image（生图子代理）

- 现有 `generate_image` 内部工具升级为 `model` 类型：执行器绑定会话的「图像账号 + 图像模型」（现有 `default_image_model` 保留为模型绑定并补 `image_account_id` 账号绑定），沿用现有图像路由、参数校验、artifact 资产写入与消息块呈现，逻辑不变；绑定账号后固定派发到该账号。
- 存量会话未绑定账号时与搜索一致：触发即引导绑定，不做旧路由派发兼容。

### 6.3 绑定候选与作用域

- 候选过滤总原则：**候选 = 「已实现执行器的能力白名单」∩ 账号实际可派发能力 ∩ 模型目录能力声明**。目录声明只是必要条件——执行器未实现的能力（GPT/Grok 之外的图像模型、hosted web_search 之外的搜索方式）一律不进候选，避免用户绑定到跑不通的组合。
- 候选范围 = **用户授权范围内的全部可派发账户**（2026-09-28 随《AI 问答会话账户唯一绑定设计》修订：会话绑定收敛为仅账户后，工具候选放宽到全部授权账户，支持「grok 对话会话 + gpt 生图工具」等跨账户组合；详见该文档第 7 节）。
- 绑定粒度为「账号 + 模型」：候选条目 = `{ accountId, accountName, modelId, modelName }`。
- 搜索候选过滤（当前已实现的执行方式仅为 Responses + hosted `web_search`）：账号可派发且端点能力含 `responses_sse` × 账号实际支持的模型中、目录矩阵（经 6.4 细化、8.5 兜底与 custom 继承后）的 `responses` 协议工具集含 `web_search` 的条目。将来接入纯搜索 API 后端时另行扩展候选语义。
- 生图候选过滤（当前已实现的图像模型仅 GPT 与 Grok 两家的注册枚举：`gpt-image-2`、`grok-imagine-image`、`grok-imagine-quality`）：账号可派发且可路由上述注册枚举模型（GPT 系账号 × `gpt-image-2`、Grok 系账号 × grok-imagine 系）；目录声明 `image_generation` 的其他模型不进候选。
- 子代理执行时固定派发到绑定的账号+模型：经 chat 面现有的调度覆盖机制（与 account 绑定会话同款入口）注入指定账号，不做常规调度漂移。

### 6.4 模型目录「协议 × 工具」矩阵细化（候选过滤的数据基础）

现状缺口：目录的 `supportedTools` 与 `supportedApiProtocols` 是模型级一维列表，「`web_search` 仅在 `responses` 协议下可用、`chat_completions` 只有 `function_calling`」这类**协议级工具绑定关系只存在于代码逻辑里，目录数据表达不了**。绑定候选过滤要判定「账号的模型经其可走协议能否执行该工具」，一维数据不充分，目录必须细化。

- **数据形状**：目录行新增 `supported_tools_by_protocol_json`，二维映射——`{"responses": ["function_calling","web_search","file_search",...], "chat_completions": ["function_calling"]}`；键为现有协议枚举（与 `supported_api_protocols_json` 同源），值为该协议下可用的工具集。
- **静态快照升级**：`internal/pricing/data_*.go` 各模型行按协议声明工具集（口径 = 各供应商官方文档的协议级能力），读取链兜底（8.5）与 custom 覆盖行继承（BUG-0229）同步适配二维字段。
- **不做兼容层（用户裁决）**：一维 `supportedTools` 字段从目录投影中**直接退场**，不保留并集派生、不做新旧并存；管理面能力过滤、模型能力展示、chat 面判定、`toolCapabilities` 等全部消费方一次切换到二维读取。`inputModalities` / `outputModalities` 维持一维不动（模态与协议无实质绑定差异）。
- **数据落点与生产切换**：能力数据继续按 BUG-0210 确立的模式由**代码内静态快照读取链派生**，不加 DB 列；生产发布新版本即全量刷新能力数据（无需刷库），发布后失效目录缓存即可生效。
- **管理面展示**：模型目录详情按矩阵呈现（每协议一行、该协议下工具一列），与静态兜底/继承后的值同源。
- **范围界定**：本细化只动目录数据层（静态快照、读取链、管理面展示），不动 `/v1` 请求链路、调度与协议转换——目录数据链是 AI 对话依赖的数据层，非网关行为（BUG-0210/0229 同款边界）。

## 7. 数据模型

- `chat_conversations` 新增两列（均可空 TEXT；空 = 未绑定）：
  - `search_account_id` / `search_model_id`——搜索子代理的账号+模型绑定；
  - `image_account_id`——生图子代理的账号绑定；现有 `default_image_model` 保留为生图模型绑定（语义升级，列名不变）。
- 存量会话：搜索与生图绑定列均为 NULL，首次触发对应工具时统一进入引导流程（不保留旧图像路由派发的兼容路径，两个模型工具行为一致）。
- 绑定完整性约束：账号+模型必须仍在会话作用域且可派发，失效时（账号停用/删除、模型下架）绑定状态返回「已失效，请重设」，不静默漂移到其他账号。
- 消息内容块：子代理执行以现有 `tool_call` / `tool_result` 内容块落库与呈现；搜索来源列表作为 `tool_result` 的结构化字段（前端时间线渲染「已搜索 + 来源」）。不新增块类型。

## 8. API 契约

1. `GET /my-chat/conversations/{id}/tool-bindings`：返回两类模型工具的绑定状态与候选列表。
   - 形状：`{ tools: [ { id: "web_search", kind: "model", bound: bool, binding: {accountId, accountName, modelId, modelName}|null, valid: bool, candidates: [{accountId, accountName, modelId, modelName}] }, { id: "generate_image", kind: "model", bound, binding, valid, candidates }, { id: "diagnostic_echo", kind: "code", ... } ] }`（`code` 工具无绑定概念，仅列出）。
2. `PATCH /my-chat/conversations/{id}`：新增可修改键 `searchBinding: {accountId, modelId}`（传 null/空解绑；二元组必须在候选列表内，否则 400 并返回候选）。生图绑定沿用图像模型设置入口并补账号选择（`imageBinding`）。
3. `toolCapabilities`（会话详情内嵌字段）：**形状重写**为上述绑定状态（旧「可用性矩阵 + 不可用原因」语义废弃）；前端同步改。
4. SSE：新增事件 `tool.binding_required`（工具调用触发但未绑定时下发，携带 `toolId` 与候选摘要），前端据此弹引导；主模型本轮收到「工具未配置」的 tool result，可自然告知用户。

## 9. 执行流程

```
用户提问
→ 主模型（chat_completions）带工具定义生成
→ tool_call: web_search(query)
→ 后端：会话已绑定（账号+模型）？
   ├─ 未绑定 → SSE tool.binding_required + tool result「工具未配置」→ 主模型告知用户
   └─ 已绑定 → 子代理经 /v1 链固定派发到绑定账号+模型（其自身协议，如 responses+hosted web_search）
        → 获得结果与来源 → tool result 回喂
→ 主模型继续生成最终回答（正文含来源引用）
→ 轮次完成，内容块落库（tool_call/tool_result 块已含子代理过程）
```

并发与重入：子代理调用计入主轮次的总超时；用户停止轮次时子调用同步取消（复用现有停止链路）。

## 10. 交互设计

1. 会话设置面板：新增「搜索模型」「生图模型」两行绑定选择器，选择器按「账号 · 模型」组合呈现候选（如「神影-克隆 · gpt-6-sol」）；未绑定显示「未设置」，绑定失效（账号停用/模型下架）显示「已失效，请重设」。
2. 引导：`tool.binding_required` 触发时前端弹轻量引导（「该会话支持联网搜索，选择账号和模型」+「账号 · 模型」组合候选下拉）；也可忽略继续纯对话。
3. 消息时间线：搜索工具执行展示为现有 tool_call 块样式，**执行中默认展开为「子代理过程区」**（与思考块同款的渐进展示，非 JSON）：依次呈现子代理思考摘要（流式追加、自动滚动、按 Markdown 渲染）、搜索动作（「搜索「query」」逐条追加）与回答摘要头部；**轮次全部结束后自动折叠**为一行（「联网搜索 已完成 · N 个来源」），可手动展开回看摘要与来源 URL（可点击）。生图执行中在图片块位置展示生成进行态，完成后折叠为图片块本身。历史回看（刷新后）只有终态摘要与来源，过程区不落库。

## 11. 删除项清单（不留尾巴）

1. `stream_route` 的工具驱动协议偏好：`selectChatTransport(..., supportsWebSearch || imageCount > 0)` 改为恒 `chat_completions`。
2. chat 面对 hosted 工具的注入：`effectiveTools` / `mapChatHostedToolsToResponses` 及 Responses 分支的 `tools` 注入，连同 `web_search` hosted 判定一并删除（`generation_tools` 内部工具照常注入 function tools）。
3. `buildChatTransportRequest` 的 Responses 分支与 Responses 事件解析：主对话不再产生 Responses 流；上下文压缩调用同步恒 `chat_completions`。图片输入改为 Chat Completions 多模态格式（`content` 数组 `image_url` 块），服务端与前端校验口径同步（`inputModalities` 含 `image` 的模型才允许带图）。
4. `toolCapabilities` 旧矩阵计算（`chain_chat_tool_capabilities.go` 的模型×协议判定）重写为绑定状态聚合。
5. 上述路径的既有测试同步删除或改写；`AI问答设计.md` 8.6 同步改写并指向本文档。
6. 保留：模型目录能力数据链（静态兜底、custom 继承、`supportedToolsByProtocol` 二维矩阵）——服务于绑定候选过滤与模型能力展示，不再驱动协议与注入；一维 `supportedTools` 字段按 §2.10 / §6.4 直接退场，不保留。

## 12. 兼容与迁移

- 存量会话：不迁移；搜索未绑定 → 引导；生图模型保持现值继续可用。
- 前端 IndexedDB 缓存消息：`tool_call`/`tool_result` 块形状不变，旧消息渲染不受影响。
- 旧 `toolCapabilities` 消费方只有对话前端，同仓同步改，无外部契约面。
- 子代理子调用产生的用量记录与现有 `/v1` 计费口径一致，统计无特殊处理。

## 13. 边界与风险

1. 搜索延迟与成本：一次搜索 = 一次子模型完整调用（约 10~30s、数千 token），计入会话成本；质量优于纯搜索 API（子模型自带查询改写与多轮搜索），但单价更高。将来可用插拔位接入纯搜索 API 降本。
2. 触发可靠性依赖主模型的 function calling 质量（主流模型可靠；弱模型可能漏调用，用工具描述与系统提示引导）。
3. 搜索结果占用主对话上下文（tool result 有字节上限，默认 4KB，可配）。
4. 图片输入从 Responses 迁到 Chat Completions 多模态：依赖上游账户对 `image_url` 的支持（主流兼容端点标配）；个别不支持的账户会在该轮收到上游错误，错误面与现状一致。

## 14. 验收标准（可观察行为）

1. 主对话请求体恒为 `chat_completions` 形状，不出现 Responses 主流。
2. 绑定搜索「账号+模型」（如 神影-克隆 · gpt-6-sol）后问实时信息：时间线出现 web_search 工具块、正文含实时信息与来源 URL；消息落库含 `tool_call`/`tool_result` 块。
3. 未绑定时问实时信息（同上场景不绑定）：弹出设置引导，主模型回复中自然说明「搜索未配置」；完成绑定后同轮或下轮可正常搜索。
4. 生图：现有生图行为不回归（显性化为会话设置项）。
5. 带图输入：`inputModalities` 含 image 的模型可正常带图对话（Chat Completions 多模态）。
6. 旧路径删除验证：代码中不再存在 chat 面 hosted 工具注入与工具驱动协议偏好的任何分支；相关测试清零或改写。
7. 网关与调度域零改动（diff 不涉及 gatewaydispatch/调度/转换器）。

## 15. 验证方式

- 单元/链路：工具类型注册、绑定候选过滤、未绑定引导事件、恒 chat 协议的传输构造、图片多模态构造。
- 端到端（隔离环境 + 真实上游，复用 2026-09-28 的验证方法）：建 personal 会话绑定「神影-克隆账号 + `gpt-6-sol`」为搜索子代理，主模型选任一 chat 模型，问「北京今天天气」→ 验收标准 2/3 逐项核对。
- 回归：生图链路、停止轮次、上下文压缩、消息落库与前端渲染。

## 16. 实施顺序

1. 工具类型系统与注册器改造。
2. 目录「协议 × 工具」矩阵细化（6.4：静态快照二维化、读取链兜底/继承适配、一维字段退场与消费方全量切换、管理面矩阵展示）——候选过滤的数据基础，先行。
3. 数据模型（会话绑定三列）与 API（tool-bindings / PATCH / SSE 引导事件）。
4. web_search 子代理执行器（/v1 子调用 + 结果回喂）。
5. 主对话恒 chat 与图片多模态改造；删除项清单逐条执行。
6. 前端：设置面板、引导、时间线、模型目录矩阵展示。
7. 文档同步（`AI问答设计.md` 8.6 改写）与测试清理。
