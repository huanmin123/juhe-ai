# 智谱 GLM 账号接入

## 范围

本文记录智谱 GLM 供应商的接入结论、账户创建类型、协议档案、网关透传边界、模型目录和后续实现注意事项。本文是实施前的目标方案文档；代码尚未落地时，运行事实仍以当前系统实际内置供应商为准。

官方资料显示，智谱当前面向本项目最相关的接入形态有三类：

- 通用 GLM API：支持 OpenAI API 兼容调用，OpenAI SDK 只需要替换 API Key 和 `base_url`，默认地址为 `https://open.bigmodel.cn/api/paas/v4/`。参考 [OpenAI API 兼容](https://docs.bigmodel.cn/cn/guide/develop/openai/introduction) 和 [HTTP API](https://docs.bigmodel.cn/cn/guide/develop/http/introduction)。
- GLM Coding Plan 的 OpenAI Chat Completions 端点：官方文档明确给出 `OpenAI Chat Completions` 协议端点 `https://open.bigmodel.cn/api/coding/paas/v4`，用于支持 OpenAI Compatible 的编程工具。参考 [GLM Coding Plan 接入工具](https://docs.bigmodel.cn/cn/coding-plan/tool/others)。
- GLM Coding Plan 的 Anthropic Messages 端点：官方文档给出 `Anthropic Messages` 协议端点 `https://open.bigmodel.cn/api/anthropic`。该端点不是 OpenAI v1，后续必须新增 Anthropic 协议档案和 adapter 后再接入，第一阶段不混入 OpenAI v1 档案。

结论：

- 智谱支持 OpenAI 兼容协议，但当前稳定落地范围应限定为 OpenAI Chat Completions，不声明 OpenAI Responses 能力。
- 通用 GLM API 和 GLM Coding Plan 都用 API Key 鉴权，不是 OAuth；创建账户时应像 GPT 一样先区分接入类型，但底层 `accounts.type` 仍保存为 `api_key`。
- 通用 GLM API 和 GLM Coding Plan 必须使用不同 `provider_protocol_profile_id`，不能只靠同一个 `glm` 供应商和不同 `base_url` 混用，否则分组、额度、模型目录、账号测试和排障都会混在一起。

## 供应商与协议档案

新增供应商：

```ts
type ProviderCode = 'glm'
```

建议显示名称为 `智谱 GLM`。`glm` 是独立供应商，不能复用 `gpt` 供应商、`profile_gpt_openai_v1` 或 GPT / Codex 客户端策略。`providerCode=openai` 仍作为 OpenAI v1 聚合目录；GLM 的 OpenAI v1 模型可以进入 `openai` 聚合目录，但 GLM 自身账号池、分组、模型价格和响应策略归属于 `glm`。

目标协议档案：

| 档案 | 供应商 | 协议 | 默认 Base URL | 账户创建类型 | 默认能力 |
| --- | --- | --- | --- | --- | --- |
| `profile_glm_general_openai_v1` | `glm` | `openai/v1` | `https://open.bigmodel.cn/api/paas/v4/` | 通用 GLM API Key | `chat_json`、`chat_sse` |
| `profile_glm_coding_openai_v1` | `glm` | `openai/v1` | `https://open.bigmodel.cn/api/coding/paas/v4` | GLM Coding Plan Key | `chat_json`、`chat_sse` |

两套档案都只声明 `chat_completions` 端点族。不要默认写入 `responses_json` 或 `responses_sse`，除非官方后续明确提供 OpenAI Responses 兼容接口并完成真实验证。

后续如果接入 `https://open.bigmodel.cn/api/anthropic`：

- 必须新增 `protocolCode=anthropic`、对应协议版本、`messages` 端点族和 Anthropic adapter。
- 不能把 Anthropic Messages 请求塞进 OpenAI v1 Chat Completions 档案。
- 不能通过网关临时把 OpenAI Chat Completions 自动翻译为 Anthropic Messages，除非单独立项、定义字段映射、流式事件、错误语义和验证矩阵。

## 创建账户类型

前端创建流程仍按“供应商 -> 接入类型 -> 凭据与调度配置”展开。

选择 `智谱 GLM` 后展示两个接入类型：

| 页面接入类型 | 底层 `accounts.type` | 协议档案 | 凭据字段 | 默认测试模型 |
| --- | --- | --- | --- | --- |
| 通用 GLM API Key | `api_key` | `profile_glm_general_openai_v1` | `api_key`、`base_url` | `glm-5.2` |
| GLM Coding Plan Key | `api_key` | `profile_glm_coding_openai_v1` | `api_key`、`base_url` | `glm-5.2` |

这里的“接入类型”是产品表单概念，不是新增 OAuth 类型。后端保存时仍加密保存 `credentials.api_key`，并通过 `provider_protocol_profile_id` 区分通用 GLM API 与 GLM Coding Plan。

保存规则：

- 新建 GLM 账户默认写入 `pending_test`，测试通过后才允许正常调度。
- `base_url` 默认按所选接入类型填充，允许用户修改为同协议的代理地址或专属部署地址，但必须继续通过 SSRF 防护和 OpenAI-compatible base URL 校验。
- `credentials.supported_endpoint_modes` 省略时，GLM 两种接入类型都默认 `['chat_json', 'chat_sse']`。
- GLM 账户不显示 GPT OAuth 字段，不显示 Refresh Token、Access Token、ChatGPT Account ID 或 Codex Responses 兼容模式。
- GLM 账户默认 `client_compatibility = openai_standard`。不要套用 GPT API Key 默认的 `codex_responses`。
- Coding Plan Key 与平台其他 API Key 不通用；如果用户把通用 API Key 填到 Coding Plan 档案，可能无法使用 Coding 套餐额度，应通过表单提示和测试结果提示区分。

## 网关请求边界

GLM OpenAI v1 档案复用 OpenAI v1 Chat Completions 协议适配器，但有独立供应商策略。

请求路径：

- 客户端可请求 `/chat/completions` 或 `/v1/chat/completions`。
- 网关按当前上游 URL 归一化规则把请求发往所选档案的 `base_url + /chat/completions`。
- `GET /models` 和 `GET /v1/models` 继续由本地模型目录返回，不主动请求智谱上游模型列表。
- `/responses` 和 `/v1/responses` 第一阶段不进入 GLM 候选账号；如果当前 API Key 只绑定 GLM Chat 档案分组，应返回本地“没有支持该端点的上游账户”类错误，而不是自动改写为 Chat Completions。

请求体：

- 默认保持 raw body passthrough，保留智谱扩展字段，例如 `thinking`、`reasoning_effort`、`tool_stream`、`do_sample` 等官方支持字段。
- 账户测试请求必须使用 Chat Completions 形态，避免沿用 GPT OAuth / Responses 测试路径。
- GLM 对 Chat Completions `messages[].role` 的支持应以官方文档和真实测试为准。若客户端传入 OpenAI 新式 `developer` role，GLM 可能返回角色错误；第一阶段应在 GLM 供应商兼容层中把 `developer` 限定性降级为 `system`，或在本地提前返回清晰错误，不能把该规则写进全局 OpenAI v1 协议层。
- 一些 OpenAI 客户端允许 `temperature` 大于 `1`，而 GLM 可能只接受更窄范围。默认透传时让上游返回真实错误；如果后续需要本地纠偏，应作为 GLM 供应商兼容规则配置，不能影响 GPT 或其他 OpenAI-compatible 供应商。

请求头：

- 上游鉴权统一写入 `Authorization: Bearer <GLM API Key>`。
- 继续过滤本地认证头、代理链路头、hop-by-hop 头、Cookie、压缩相关头、SDK / tracing / 部署平台噪声头。
- 不从账号配置生成 `OpenAI-Organization`、`OpenAI-Project` 或 GPT / Codex 专属 header。
- Coding Plan 面向指定工具和产品环境；网关不应无故覆盖客户端 `User-Agent`。在安全过滤之后，尽量保留工具侧用于官方识别的普通请求形态，避免影响套餐额度识别。

## 返回与 usage

GLM Chat Completions 返回应优先按 OpenAI v1 Chat 语义解析：

- JSON 响应读取 `choices[].message.content`、`choices[].message.tool_calls`、`finish_reason` 和 `usage`。
- 流式响应读取 `choices[].delta.content`、`choices[].delta.tool_calls` 和最后 usage chunk；如果上游没有返回 usage，只在已有可见输出时按现有估算兜底。
- 如果 GLM 返回 `reasoning_content` 或类似推理文本字段，响应语义检查和诊断展示应把它作为可见 / 诊断语义来源之一，但是否写给下游要遵循客户端协议兼容策略。

成本与统计：

- 通用 GLM API 的 token 成本按 GLM 模型目录价格估算。
- GLM Coding Plan 是套餐权益和 prompt / 周期额度语义，不等同于普通 API 余额扣费。本地仍可记录 token、请求数、错误数和成本估算，但不能把本地美元成本当作 Coding Plan 官方额度。
- Coding Plan 官方文档提到 5 小时和每周使用额度、不同套餐 prompts 额度和模型倍率；这些属于 Coding Plan 专属展示信息。第一阶段不主动抓取或猜测套餐额度，只记录真实上游响应和本地使用统计。
- 不要臆造智谱 rate-limit header。只有真实请求或官方文档确认的响应头才能进入额度快照设计。

## 模型目录

GLM 模型目录必须单独维护在 `glm` 供应商下，不要混进 GPT 价格文件。

初始目录建议按两个接入类型区分可见范围：

- 通用 GLM API：以智谱开放平台模型文档和价格页为准，优先收录当前官方可调用、可计价的 GLM 文本 / 编码模型。
- GLM Coding Plan：以 Coding Plan 套餐权益和接入工具文档为准，初始重点覆盖 `glm-5.2`、`glm-5-turbo`、`glm-4.7`、`glm-4.5-air`。官方文档说明调用历史模型 `glm-5.1` / `glm-5` 会自动切换到 `glm-5.2`，本地目录应谨慎处理旧别名，避免把它们误当成独立计价模型。

模型目录字段要求：

- `providerCode = glm`
- `model` 使用智谱官方模型 ID，小写和连字符按官方写法保留。
- `supportedApiProtocols` 第一阶段填 `chat_completions`。
- `contextWindowTokens` 以官方模型页或 Coding Plan 工具文档为准；例如 Coding Plan 文档提示 `glm-5.2` 可按 `1000000` 上下文配置，其他模型常见为 `200000`，实际落库前仍需以官方模型页复核。
- `releaseDate` 用官方发布日期或官方文档可确认的上线日期，不确定时留空，不为了排序编造。
- 价格只采信智谱官方价格页或官方模型页；第三方价格库和社区表格只能作为线索。

智谱开源模型，例如 `GLM-4.5`、`GLM-5` 仓库和 Hugging Face / ModelScope 权重，可以作为自托管、vLLM、SGLang 或 Transformers 接入参考，但不等于智谱云 API 的模型 ID、上下文窗口或价格。自托管 GLM 如果通过 OpenAI-compatible vLLM / SGLang 暴露，应作为自定义 OpenAI-compatible 上游或独立自托管供应商处理，不直接套用智谱云价格。

## 分组、授权与 API Key 路由

GLM 两个 OpenAI v1 档案应创建独立默认分组：

- 默认 GLM 通用分组：绑定 `profile_glm_general_openai_v1`。
- 默认 GLM Coding 分组：绑定 `profile_glm_coding_openai_v1`。

账户只能加入相同 `provider_protocol_profile_id` 的分组。通用 GLM API 账户不能加入 Coding Plan 分组，Coding Plan Key 也不能加入通用 GLM 分组。

当前 API Key 多分组绑定仍以同一供应商协议档案为硬边界；模型路由方案落地后，一个本地 API Key 可以同时绑定 GPT、GLM 通用和 GLM Coding 分组，但每次请求必须先用 `model` 命中目标 `provider_protocol_profile_id`，再只在该档案的分组内调度。跨供应商路由详见 [自定义模型与模型映射设计](自定义模型与模型映射设计.md) 和 [API Key 多分组路由设计](APIKey多分组路由设计.md)。

## 账号测试

GLM 账户测试必须复用真实网关链路：

- 测试路径使用 `/v1/chat/completions`。
- 默认测试模型使用档案 `default_test_model = glm-5.2`。
- 测试请求不发送 Responses 字段，例如 `input`、`instructions`、`max_output_tokens`、`store` 或 Codex 专属 metadata。
- 测试失败不直接把正常账户写成 `temporary_unavailable`，仍遵循当前事前确认和冷却复测规则。
- Coding Plan 测试会消耗套餐资源，应保持最小 prompt、低输出上限和明确的用户提示，不做批量额度探测。

## 导入导出协议

账户导入协议新增 GLM 示例时，应使用项目自定义 JSON，不直接兼容智谱控制台导出格式。

推荐写法：

```json
{
  "name": "智谱 GLM Coding 账号 1",
  "providerCode": "glm",
  "connectionType": "coding_api_key",
  "type": "api_key",
  "status": "pending_test",
  "groupName": "默认 GLM Coding 分组",
  "credentials": {
    "api_key": "zhipu-api-key",
    "base_url": "https://open.bigmodel.cn/api/coding/paas/v4",
    "supported_endpoint_modes": ["chat_json", "chat_sse"]
  }
}
```

`connectionType` 是导入协议层用于 disambiguate 同供应商多 API Key 接入类型的字段。后端落库时仍以 `providerCode + connectionType` 解析 `provider_protocol_profile_id`，再保存 `accounts.type = api_key`。如果实现时不新增 `connectionType` 字段，也必须提供等价字段，不能只靠用户手填 `base_url` 推断档案。

## 实施清单

- 新增 `glm` 供应商种子。
- 新增 `profile_glm_general_openai_v1` 和 `profile_glm_coding_openai_v1`。
- 新增两个默认 GLM 分组。
- 前端账户创建在选择 `智谱 GLM` 后展示“通用 GLM API Key”和“GLM Coding Plan Key”。
- 后端账户创建、编辑、导入和公开推送接口按 `connectionType` 或等价字段解析 GLM 协议档案。
- GLM 默认 `supported_endpoint_modes` 只启用 Chat JSON/SSE。
- 账号测试默认走 Chat Completions，模型 `glm-5.2`。
- GLM 模型目录和价格目录单独建文件，成本估算按 `providerCode=glm` 查找。
- 响应语义检查支持 GLM Chat 的 `reasoning_content` 等局部字段，但规则落在 `providerCode=glm` 的供应商层。
- 文档、导入协议、接口契约、SQLite 存储说明、模型目录清洗和测试说明同步更新。

## 验证要求

第一阶段代码落地后至少覆盖：

- 创建通用 GLM API Key 账户，保存后落到 `profile_glm_general_openai_v1`。
- 创建 GLM Coding Plan Key 账户，保存后落到 `profile_glm_coding_openai_v1`。
- 两类账户默认 `supported_endpoint_modes` 均为 `chat_json/chat_sse`，不参与 `/v1/responses` 调度。
- 两类账户分别绑定同档案分组，跨档案绑定被拒绝。
- 账户测试使用 `/v1/chat/completions` 和 `glm-5.2`。
- 网关请求通用 GLM API 时命中 `https://open.bigmodel.cn/api/paas/v4/chat/completions`。
- 网关请求 GLM Coding Plan 时命中 `https://open.bigmodel.cn/api/coding/paas/v4/chat/completions`。
- `GET /v1/models` 返回本地可见 GLM 模型，不请求智谱上游。
- GLM 响应 usage 能写入使用记录；无 usage 的流式可见输出按现有估算兜底。
- Coding Plan Key 错填到通用档案或通用 Key 错填到 Coding 档案时，测试失败信息能让用户区分接入类型错误。
