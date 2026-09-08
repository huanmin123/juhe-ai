# AI 账户上游接口能力设计

## 1. 目标

本文固定 AI 账户 `credentials.supported_endpoint_modes`、账号模型别名、客户端画像和人工测试请求形态之间的边界，避免把“真实上游支持什么”和“客户端以什么形态请求”混成同一层。

页面统一使用名称“上游接口能力”。不再展示或保存“可承接请求”“客户端请求限制”或泛化客户端兼容配置。

## 2. 单一事实来源

`credentials.supported_endpoint_modes` 是账户真实上游接口能力的最终配置来源：

- OpenAI v1：`chat_json`、`chat_sse`、`responses_json`、`responses_sse`。图片模型健康检查和人工测试专用的 `images_json` 不写入此字段；只有模型目录证明模型支持 `images` 时，才可将它作为 `healthCheckEndpointMode` 使用。
- Anthropic v1：`messages_json`、`messages_sse`、`message_token_counting`。
- Gemini native：`generate_content_json`、`generate_content_sse`、`interactions_json`、`interactions_sse`、`count_tokens`、`embed_content`。
- xAI OpenAI v1：`chat_json`、`chat_sse`、`responses_json`、`responses_sse`。

用户显式启用某项能力后，网关按该声明尝试请求上游。模型目录的 `supportedApiProtocols` 只用于模型候选、默认选择和风险提示，不覆盖账户显式能力；上游最终返回不支持路径、模型或参数时，按真实上游失败进入诊断和切号流程。

## 3. 网关判定顺序

请求进入候选账户筛选后，固定按以下顺序判断：

1. 识别下游请求路径、JSON / SSE、模型和客户端画像。
2. 按 `sourceModel + sourceEndpointFamily` 查找启用的账号模型映射。
3. 命中映射时，以映射右侧确定真实上游模型和协议；未命中时，下游协议就是上游协议。
4. 使用“上游接口能力”检查最终上游 endpoint mode。
5. 能力满足时保留账户，能力不满足时跳过账户。

客户端画像只负责请求整理、上下文恢复、失败事件、重试、响应检查和审计，不再作为账户候选硬门槛。OpenAI-compatible API Key 账户只要显式启用 Responses，就可以承接 Codex `/responses`；Codex 请求整理仍按请求画像执行。GPT OAuth 等专用链路继续由账户类型、协议驱动和专用 adapter 约束。

## 4. 模型映射联动

接口能力约束映射右侧的真实上游协议，不约束映射左侧的客户端入口协议：

- `Responses -> Chat Completions` 只要求账户启用至少一种 Chat Completions 上游能力；关闭原生 Responses 不影响该映射。
- `Responses -> Responses` 要求账户启用至少一种 Responses 上游能力。
- Messages 或 Gemini native 映射同样按右侧协议族检查真实上游能力。
- Gemini Interactions 是原生 endpoint family，不参与普通 OpenAI v1 模型映射；模型目录声明 `interactions` 且账户启用对应 JSON / SSE mode 时才进入人工测试和网关候选。
- 新建或编辑映射时，右侧协议只能选择当前上游能力允许的协议族。
- 修改上游能力后，如果启用映射的右侧协议失去能力，保存必须失败并指出冲突；不能静默删除映射。
- 已停用映射可以保留，之后重新启用能力时再恢复。
- 运行时只使用已启用且右侧上游能力仍满足的映射。

## 5. 切号时的有效上游目标

本节只约束已发生切号的请求，不改变初始候选筛选和显式 mapping 的允许矩阵。初始请求可以使用 `sourceModel + sourceEndpointFamily` 解析映射；一旦某个账号完成状态恢复、协议转换和上游请求构造，调度器必须冻结本次请求的有效上游目标，再用该目标筛选后续账号。

冻结目标至少包含：

```text
providerCode
providerProtocolProfileID
upstreamModel
upstreamEndpointFamily
upstreamEndpointMode     # 例如 chat_json、chat_sse、responses_json、responses_sse
contextContract          # 例如 chat_messages_v1、native_responses_v1
```

`upstreamEndpointMode` 是实际上游采用的精确 JSON / SSE 形态，不是客户端形态的别名。`Responses -> Chat` bridge 当前以 `chat_sse` 作为上游载体；只有 bridge 明确选择并构造 `chat_json` 时，才可形成 `chat_json` 目标。`chat_json` 和 `chat_sse`、`responses_json` 和 `responses_sse` 不能在切号时互换。

切号候选只检查两类账号事实：

1. `supported_endpoint_modes` 是否包含冻结的精确 mode；
2. `supportedModels` 是否直接支持冻结的 `upstreamModel`，或启用且有效的账号模型别名 RHS 是否同时匹配冻结的 `upstreamModel + upstreamEndpointFamily`。

mapping 左侧 `SourceModel`、`SourceEndpointFamily`、客户端协议和客户端画像不参与切号资格判断；不能复用只按 source 查找的 `ResolveAccountModelMapping`。候选被选中后，provider driver 必须使用已经物化的 `contextContract` 或冻结的 bridge 执行计划重建请求，不能再把原始客户端 body 交给候选账号。

供应商边界固定为：冻结目标是 `chat_completions` 时允许跨供应商，但模型、Chat 协议族、精确 mode 和上下文形态必须一致；冻结目标是 `responses` 或其他非 Chat 协议时必须保持同一供应商，并保持兼容的 provider profile、模型、协议族和 mode。目标缺失或无法证明时不放宽为客户端协议，停止跨账号切换。

`Responses -> Chat` 只有在 `previous_response_id`、网关 compact envelope 和其他可转换状态已由网关状态层消费，并且不存在仍需原上游解开的必需 opaque `encrypted_content`，形成自包含的 Chat / canonical 上下文后，才可以按 Chat 目标跨供应商切号。外部原生 `previous_response_id` 或仍需原上游解开的 opaque `encrypted_content` 不能交给 Chat 候选；只能保持原生 Responses 的同供应商约束，否则受控失败。

完整契约见 [切号时有效上游目标与上下文迁移设计](切号时有效上游目标与上下文迁移设计.md)。

## 6. 人工测试契约

账户列表保持轻量，不返回凭据或完整模型目录。用户打开单账户测试时，前端立即读取当前账户作用域内的 `test-options`，运行按钮在该请求完成前保持禁用；加载失败时必须展示原始错误并阻止提交，不能使用账户中可能过期的请求形态静默测试。

`test-options` 对每个候选模型返回人工测试所需的最小数据 `{ id, name, testEndpointModes }`。其中 `testEndpointModes` 由账户已启用的上游能力和该模型目录 `supportedApiProtocols` 的交集计算，不返回原始凭据或无关模型的完整协议详情。目录确认只支持图片生成的模型，其唯一测试形态为 `images_json`；前端必须同步草稿 `healthCheckEndpointMode` 并显示 `Images API`。切换模型时必须重新取得该模型的可测试形态。新增 / 编辑表单可使用供应商模型选项携带的协议与当前草稿账户能力计算交集，但提交测试时后端仍重新以模型目录验证，不能相信客户端传值。

## 7. 非目标

- 不新增独立的图片账户能力字段；`images_json` 仅是 `healthCheckEndpointMode` 的精确检查形态，不替代 `credentials.supported_endpoint_modes`。
- 不新增客户端请求限制配置。
- 不展示派生的“可承接请求”列表。
- 不让客户端画像创造账户没有声明的上游协议能力。
- 不把模型目录协议标签改成账户运行时能力事实。
- 不自动删除与能力冲突的模型映射。

## 8. 验证

- 普通 OpenAI-compatible API Key 显式启用 `responses_sse` 后，可以进入 Codex `/responses` 候选并执行 Codex 请求整理。
- 未启用目标上游 endpoint mode 的账户仍被候选过滤。
- 人工测试首次打开时完成 `test-options` 加载；每项返回 `{ id, name, testEndpointModes }`，加载中或加载失败时不得提交测试。纯图片模型只提供 `images_json`，并在界面显示为 `Images API`。
- 关闭映射右侧上游协议能力时，启用映射阻止保存，停用映射允许保留。
- 页面、批量编辑和导入说明统一使用“上游接口能力”。
