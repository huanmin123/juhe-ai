# DeepSeek 账号接入

## 范围

本文记录 DeepSeek 供应商的目标接入方案、账户创建类型、协议档案、网关透传边界、模型目录和后续实现注意事项。本文是实施前的目标方案文档；代码尚未落地时，运行事实仍以当前系统实际内置供应商为准。

官方资料显示，DeepSeek 对外公开的 hosted API 不是一套独立原生协议，而是两套兼容 surface：

- OpenAI-compatible surface：默认地址 `https://api.deepseek.com`
- Anthropic-compatible surface：默认地址 `https://api.deepseek.com/anthropic`
- 部分 OpenAI-compatible beta 能力走 `https://api.deepseek.com/beta`

官方 API 以 API key 为主，不提供本项目需要的 OAuth 账户创建流。对本项目来说，DeepSeek 应作为独立供应商接入，账户凭据仍按 `api_key` 处理，协议层再拆成 OpenAI-compatible 和 Anthropic-compatible 两个档案。

结论：

- 一个供应商：`deepseek`
- 两个协议档案：`profile_deepseek_openai_v1`、`profile_deepseek_anthropic_v1`
- 两类账户底层都保存为 `api_key`
- OpenAI-compatible 面优先复用现有 OpenAI v1 Chat Completions 链路
- Anthropic-compatible 面必须单独做协议适配，不和 OpenAI v1 混用

## 供应商与协议档案

建议新增供应商：

```ts
type ProviderCode = 'deepseek'
```

建议显示名称为 `DeepSeek`。`deepseek` 是独立供应商，不能复用 `openai`、`gpt` 或其账号池、分组和价格目录。`providerCode=openai` 仍表示通用 OpenAI-compatible 聚合目录；DeepSeek 自身账号池、分组、模型价格和测试策略都归属于 `deepseek`。

目标协议档案：

| 档案 | 供应商 | 协议 | 默认 Base URL | 账户创建类型 | 默认能力 |
| --- | --- | --- | --- | --- | --- |
| `profile_deepseek_openai_v1` | `deepseek` | `openai/v1` | `https://api.deepseek.com` | DeepSeek OpenAI API Key | `chat_json`、`chat_sse` |
| `profile_deepseek_anthropic_v1` | `deepseek` | `anthropic/v1` | `https://api.deepseek.com/anthropic` | DeepSeek Anthropic API Key | `messages_json`、`messages_sse` |

OpenAI-compatible 档案当前只应声明 Chat Completions 语义，不把 Responses 当成默认能力。`/beta` 下的 FIM 或其他实验能力可以单独记为 beta 能力，但不要因此扩展成通用 Responses 档案。

Anthropic-compatible 档案必须单独走 Anthropic Messages 形态，不把 `messages` 请求塞进 OpenAI v1 档案，也不通过网关临时把 OpenAI 请求自动翻译成 Anthropic 请求。

## 创建账户类型

前端创建流程仍按“供应商 -> 接入类型 -> 凭据与调度配置”展开。

选择 `DeepSeek` 后展示两个接入类型：

| 页面接入类型 | 底层 `accounts.type` | 协议档案 | 凭据字段 | 默认测试模型 |
| --- | --- | --- | --- | --- |
| DeepSeek OpenAI API Key | `api_key` | `profile_deepseek_openai_v1` | `api_key`、`base_url` | `deepseek-v4-flash` |
| DeepSeek Anthropic API Key | `api_key` | `profile_deepseek_anthropic_v1` | `api_key`、`base_url` | `deepseek-v4-flash` |

这里的“接入类型”是产品表单概念，不是新增 OAuth 类型。后端保存时仍加密保存 `credentials.api_key`，并通过 `provider_protocol_profile_id` 区分 OpenAI-compatible 与 Anthropic-compatible。

保存规则：

- 新建 DeepSeek 账户默认写入 `pending_test`，测试通过后才允许正常调度
- `base_url` 默认按所选接入类型填充，允许用户修改为同协议的代理地址或专属部署地址，但必须继续通过 SSRF 防护和 base URL 校验
- `credentials.supported_endpoint_modes` 省略时，OpenAI-compatible 档案默认启用 `chat_json`、`chat_sse`
- Anthropic-compatible 档案应单独维护自己的 endpoint mode 命名，不复用 OpenAI 的 `chat_*` / `responses_*`
- DeepSeek 账户不显示 GPT OAuth 字段，不显示 Refresh Token、Access Token、Codex Responses 兼容模式
- DeepSeek 账户默认走普通 OpenAI-compatible 透传思路，不引入 GPT 专属的客户端兼容策略

## 网关请求边界

DeepSeek OpenAI v1 档案优先复用现有 OpenAI v1 Chat Completions 协议适配器，但有独立供应商策略。

请求路径：

- 客户端可请求 `/chat/completions` 或 `/v1/chat/completions`
- beta 能力按 `https://api.deepseek.com/beta` 单独拼接
- `GET /models` 和 `GET /v1/models` 继续由本地模型目录返回，不主动请求 DeepSeek 上游模型列表
- 当前不把 `/responses` 作为 DeepSeek OpenAI 档案默认候选；如果后续官方能力和本地适配都确认，再单独放开

请求头：

- OpenAI-compatible surface 统一写入 `Authorization: Bearer <DeepSeek API Key>`
- Anthropic-compatible surface 按 Anthropic 兼容规则写入 `x-api-key` 和必要版本头
- 继续过滤本地认证头、代理链路头、hop-by-hop 头、Cookie、压缩相关头、SDK / tracing / 部署平台噪声头
- 不从账号配置生成 `OpenAI-Organization`、`OpenAI-Project` 或 GPT / Codex 专属 header

## 返回与 usage

DeepSeek OpenAI-compatible 返回应优先按 OpenAI v1 Chat 语义解析：

- JSON 响应读取 `choices[].message.content`、`choices[].message.tool_calls`、`finish_reason` 和 `usage`
- 流式响应读取 `choices[].delta.content`、`choices[].delta.tool_calls` 和最后 usage chunk
- DeepSeek 的 `thinking`、`reasoning_effort`、`reasoning_content` 这类推理语义应保留为可见或诊断信息，不要写进通用 OpenAI 协议层的固定字段假设

Anthropic-compatible 返回应按 Anthropic Messages 语义抽取可见文本、工具调用、完成状态和 usage，再映射到系统统一响应语义帧。

成本与统计：

- DeepSeek 模型成本按本地模型目录和官方价格口径估算
- DeepSeek hosted API 是 API-key-only，不存在 OAuth 专属额度进度
- 如果后续把 DeepSeek 自托管权重接入为本地 OpenAI-compatible 上游，应作为独立自托管供应商或自定义供应商处理，不和 DeepSeek 云 API 混淆

## 模型目录

DeepSeek 模型目录必须单独维护在 `deepseek` 供应商下，不要混进 GPT 价格文件。

初始目录建议：

- `deepseek-v4-flash`
- `deepseek-v4-pro`

旧别名 `deepseek-chat` 和 `deepseek-reasoner` 已被官方标记为将于 `2026-07-24 15:59 UTC` 退役。当前在目录中可以作为历史兼容别名保留，但不应作为新建账户或新默认测试模型的首选。

模型目录字段要求：

- `providerCode = deepseek`
- `model` 使用 DeepSeek 官方模型 ID，小写和连字符按官方写法保留
- `supportedApiProtocols` 当前只放 OpenAI-compatible 档案对应的 `chat_completions`，Anthropic-compatible 档案单独维护
- `contextWindowTokens` 和 `releaseDate` 以官方模型页或官方文档为准
- 价格只采信 DeepSeek 官方文档或官方 API 文档；第三方价格库和社区表格只能作为线索

DeepSeek 开源模型，例如 `DeepSeek-R1`、`DeepSeek-V3` 以及相关公开仓库和权重，可以作为自托管、vLLM、SGLang 或 Transformers 接入参考，但不等于 DeepSeek 云 API 的模型 ID、上下文窗口或价格。自托管 DeepSeek 如果通过 OpenAI-compatible vLLM / SGLang 暴露，应作为自定义 OpenAI-compatible 上游或独立自托管供应商处理。

## 分组、授权与 API Key 路由

DeepSeek 两个协议档案应创建独立默认分组：

- 默认 DeepSeek OpenAI 分组：绑定 `profile_deepseek_openai_v1`
- 默认 DeepSeek Anthropic 分组：绑定 `profile_deepseek_anthropic_v1`

账户只能加入相同 `provider_protocol_profile_id` 的分组。OpenAI-compatible 账户不能加入 Anthropic 分组，Anthropic 账户也不能加入 OpenAI 分组。

当前 API Key 多分组绑定仍以同一供应商协议档案为硬边界；后续如果模型路由落地，一个本地 API Key 可以同时绑定 DeepSeek 和其他供应商分组，但每次请求必须先用 `model` 命中目标 `provider_protocol_profile_id`，再只在该档案的分组内调度。

## 账号测试

DeepSeek 账户测试必须复用真实网关链路：

- OpenAI-compatible 测试路径使用 `/v1/chat/completions`
- Anthropic-compatible 测试路径使用 Anthropic Messages 形态
- 默认测试模型建议优先使用 `deepseek-v4-flash`
- 测试请求不发送不相关的 OpenAI / GPT 专属字段
- 测试失败不直接把正常账户写成 `temporary_unavailable`，仍遵循当前事前确认和冷却复测规则

## 开源与官方资料

官方来源：

- API 文档：<https://api-docs.deepseek.com/>
- Anthropic 兼容指南：<https://api-docs.deepseek.com/guides/anthropic_api>
- DeepSeek-V3 官方仓库：<https://github.com/deepseek-ai/DeepSeek-V3>
- DeepSeek-R1 官方仓库：<https://github.com/deepseek-ai/DeepSeek-R1>

## 实施清单

- 新增 `deepseek` 供应商种子
- 新增 `profile_deepseek_openai_v1` 和 `profile_deepseek_anthropic_v1`
- 新增两个默认 DeepSeek 分组
- 前端账户创建在选择 `DeepSeek` 后展示 `OpenAI` 与 `Anthropic` 两种接入类型
- 后端账户创建、编辑、导入和公开推送接口按协议档案解析 DeepSeek 接入类型
- DeepSeek OpenAI 档案默认只启用 Chat JSON/SSE
- DeepSeek Anthropic 档案单独维护 Messages 语义和测试路径
- DeepSeek 模型目录和价格目录单独建文件，成本估算按 `providerCode=deepseek` 查找
- 文档、导入协议、接口契约、SQLite 存储说明、模型目录清洗和测试说明同步更新

## 验证要求

当前文档落地后至少覆盖：

- DeepSeek 作为独立供应商，不复用 GPT 供应商编码
- DeepSeek OpenAI 账户保存后落到 `profile_deepseek_openai_v1`
- DeepSeek Anthropic 账户保存后落到 `profile_deepseek_anthropic_v1`
- 两类账户默认分组和协议档案彼此隔离
- OpenAI-compatible 面只声明 Chat Completions
- Anthropic-compatible 面只声明 Messages 语义
- `GET /v1/models` 继续由本地模型目录返回，不请求 DeepSeek 上游
- 旧别名 `deepseek-chat`、`deepseek-reasoner` 不作为新默认模型
