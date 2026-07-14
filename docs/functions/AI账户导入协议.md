# AI 账户导入协议

## 定位

AI 账户导入只支持项目自定义 JSON 协议，不直接兼容 sub2api、CPA、one-api、new-api 或其他外部格式。外部数据需要由用户自行使用 AI、脚本或表格工具整理为本协议后再导入。

当前协议：

- `type`: `juhe-ai-account-import`
- `version`: `1`
- 单次请求受系统 API `256KB` JSON 请求体上限约束，当前接口按小批量导入设计。
- 单次最多导入 50 个账户、20 个代理，避免 DB service 在一次请求内长时间同步解析和校验大数组。
- 导入弹窗提供可复制 AI 提示词和“导出协议 Markdown”按钮；提示词配合本协议文件交给其他 AI 做格式转换。
- 导入接口只接受合法 JSON，不接受 Markdown、JSONL、CSV、带注释 JSON 或外部系统原始格式。

## 转换硬性规则

- 输出必须是一个 JSON 对象，不能包在 Markdown 代码块里，不能附带解释文字。
- 字段名严格使用本协议定义，不要把 `providerCode` 改成 `provider_code`，也不要把 `api_key` 改成 `apiKey`。
- 顶层 `type` 固定为 `juhe-ai-account-import`，`version` 固定为数字 `1`。
- `accounts` 至少 1 条；每个账户必须显式填写 `name`、`providerCode`、`providerProtocolProfileId`、`type`、`status`、`credentials`，以及 `groupId` 或 `groupName`。
- 当前 `providerCode` 可填 `openai`、`gpt`、`anthropic`、`glm`、`deepseek`、`gemini` 或 `hybrid`；系统按 `providerCode + providerProtocolProfileId` 校验内部协议档案。`openai` 在供应商层表示通用 OpenAI-compatible 供应商，只支持 `api_key`；`gpt` 表示 GPT 专属供应商，支持 `api_key` 和 `oauth`；`anthropic` 表示官方 Anthropic Claude 供应商，当前只支持 API Key；`glm` 表示智谱 GLM 供应商，支持通用 GLM API Key、GLM Coding Plan Key 和 GLM Coding Anthropic Key；`deepseek` 表示 DeepSeek hosted API 供应商，当前支持 OpenAI-compatible API Key 和 DeepSeek Claude Code API Key；`gemini` 表示 Google Gemini 供应商，支持 Gemini 原生 API Key 和 Gemini OpenAI Chat API Key；`hybrid` 表示混合供应商真实上游账户，用于在 AI 账户层配置跨协议入口和模型映射。
- `providerProtocolProfileId` 是协议档案唯一入口；导入协议不接收旧接入类型别名，也不按 `base_url`、供应商默认值或历史字段猜测档案。
- `providerCode = hybrid` 时必须使用 API Key 账户，按真实上游协议显式填写混合供应商档案；真实上游目标协议由账户 `modelMappings.upstreamEndpointFamily` 和 `credentials.supported_endpoint_modes` 表达，不再区分多个混合账户类型。
- 账号导入不填写 `clientCompatibility`。下游客户端画像由网关运行时内部识别；OpenAI v1 普通账户可用 `modelMappings` 显式声明 Responses 到 Chat Completions，Anthropic Messages 或 Gemini native 等其他跨协议转换由混合供应商账户表达真实上游和协议转换能力，不在 API Key 中配置。
- 不确定是否可立即调度时，`status` 填 `pending_test` 或 `disabled`，不要默认填 `active`；即使导入文件写 `active`，导入落库也会转为 `pending_test`，必须由后台激活检查成功后才参与调度，人工测试结果不能作为激活凭证。
- 不要编造缺失的 token、API Key、邮箱、账号 ID、代理密码或模型列表；不确定的信息写入 `notes`。
- 外部来源字段如果没有本协议对应字段，不要塞进 `credentials`，可以整理到 `notes`。

## 导入流程

1. 用户在 `我的 AI 账户` 直接导入到当前登录用户；管理员在 `AI 账户管理` 选择目标系统账户后可代该用户导入。
2. 打开“导入账户”，可复制右侧 AI 提示词，并点击“导出协议 Markdown”把本协议文件一并交给其他 AI 进行格式转换。
3. 点击“解析预览”，后端校验协议、账户、代理、分组引用和批内重复。
4. 预览无错误且至少有 1 个可创建账户时，允许“确认导入”。
5. 确认导入时重新解析并写入数据，导入结果返回创建、跳过、失败明细。

导入支持这些运行选项：

- 自动创建缺失分组：默认开启；关闭后，未知 `groupName` 会阻止对应账户导入。
- 自动创建缺失代理：默认开启；普通用户只能复用已存在的启用代理，不能新建代理；管理员可在管理侧导入时自动创建缺失代理。关闭后，未知 `proxyRef` 会阻止对应账户导入。
- 导入时跳过重复账户：默认开启；同一用户下已存在同名账户，或导入批内出现同名账户时跳过对应账户。

## 导出流程

`我的 AI 账户` 和 `AI 账户管理` 支持导出 JSON 文件，导出结果直接使用本协议：

- 导出文件顶层仍为 `type: "juhe-ai-account-import"`、`version: 1`。
- 单次最多导出 50 个自有 AI 账户，和单次导入账户上限一致。
- 顶部“导出 JSON”按当前账户列表筛选条件导出，最多处理前 50 条匹配结果；勾选账户后，批量工具栏“导出 JSON”只导出当前勾选账户。
- 导出只包含当前用户或管理员目标作用域内有权查看凭据和编辑的自有账户；授权账户实例不导出。
- 如果账户绑定了可用代理，导出文件会同时写入 `proxies` 并通过账户 `proxyRef` 引用，便于再次导入时自动创建或复用代理。
- 导出会保留账户标签为 `tags` 字符串数组；再次导入时会在目标系统账户维度自动创建缺失标签并绑定到账户。
- 导出会保留账户级 `healthCheckModel` 和精确 `healthCheckEndpointMode`；再次导入时检查模型必须属于同一账户的 `supportedModels`，请求形态必须是账户已启用的 Chat / Responses / Messages / GenerateContent JSON 或 Streaming mode，否则预览失败。导入省略请求形态时按新账户默认规则解析。
- 导出必须为每个账户写出当前 `providerProtocolProfileId`，保证重新导入能还原到同一供应商协议档案；导入不得靠 `credentials.base_url`、供应商默认值或历史接入类型字段猜测档案。
- 账户当前为 `pending_test` 时导出为 `status: "pending_test"`；账户当前为 `active` 且参与调度时导出为 `status: "active"`；其他运行态状态统一导出为 `status: "disabled"`。导入时 `active` 会按安全策略落成 `pending_test`，后台激活检查成功后才转为 `active`。
- 导出的 JSON 文件可以在“导入账户”弹窗中直接粘贴预览，再确认导入。

## JSON 示例

```json
{
  "type": "juhe-ai-account-import",
  "version": 1,
  "proxies": [
    {
      "ref": "proxy-hk-1",
      "name": "香港代理 1",
      "type": "http",
      "host": "127.0.0.1",
      "port": 7890,
      "username": "",
      "password": "",
      "enabled": true
    }
  ],
  "accounts": [
    {
      "ref": "openai-key-001",
      "name": "OpenAI 兼容 API Key 账号 1",
      "providerCode": "openai",
      "providerProtocolProfileId": "profile_openai_openai_v1",
      "type": "api_key",
      "status": "active",
      "groupName": "默认 OpenAI 兼容分组",
      "concurrencyLimit": 3,
      "priority": 50,
      "tags": ["主力", "OpenAI 兼容"],
      "proxyRef": "proxy-hk-1",
      "credentials": {
        "api_key": "sk-xxx",
        "base_url": "https://api.openai.com/v1",
        "supported_endpoint_modes": ["chat_json", "chat_sse"]
      },
      "notes": "通用 OpenAI-compatible API Key 账号"
    },
    {
      "ref": "gpt-oauth-001",
      "name": "GPT OAuth 账号 1",
      "providerCode": "gpt",
      "providerProtocolProfileId": "profile_gpt_openai_v1",
      "type": "oauth",
      "status": "active",
      "groupName": "默认 GPT 分组",
      "concurrencyLimit": 3,
      "priority": 50,
      "tags": ["OAuth"],
      "credentials": {
        "refresh_token": "refresh-token-xxx",
        "access_token": "access-token-xxx",
        "id_token": "id-token-xxx",
        "base_url": "https://api.openai.com/v1",
        "supported_endpoint_modes": ["responses_json", "responses_sse"],
        "service_tier_override": "priority",
        "reasoning_effort_override": "high",
        "account_id": "acct_xxx",
        "email": "user@example.com"
      },
      "notes": "OAuth 账号"
    },
    {
      "ref": "anthropic-key-001",
      "name": "Anthropic Claude API Key 账号 1",
      "providerCode": "anthropic",
      "providerProtocolProfileId": "profile_anthropic_anthropic_v1",
      "type": "api_key",
      "status": "active",
      "groupName": "默认 Anthropic 分组",
      "concurrencyLimit": 3,
      "priority": 50,
      "tags": ["Claude"],
      "credentials": {
        "api_key": "sk-ant-api03-xxx",
        "base_url": "https://api.anthropic.com/v1",
        "supported_endpoint_modes": ["messages_json", "messages_sse", "message_token_counting"]
      },
      "notes": "Anthropic 官方 Messages API Key 账号"
    },
    {
      "ref": "glm-coding-001",
      "name": "智谱 GLM Coding 账号 1",
      "providerCode": "glm",
      "providerProtocolProfileId": "profile_glm_coding_openai_v1",
      "type": "api_key",
      "status": "active",
      "groupName": "默认 GLM 分组",
      "concurrencyLimit": 3,
      "priority": 50,
      "credentials": {
        "api_key": "zhipu-api-key-xxx",
        "base_url": "https://open.bigmodel.cn/api/coding/paas/v4",
        "supported_endpoint_modes": ["chat_json", "chat_sse"]
      },
      "notes": "GLM Coding Plan API Key 账号"
    },
    {
      "ref": "glm-coding-anthropic-001",
      "name": "智谱 GLM Coding Anthropic 账号 1",
      "providerCode": "glm",
      "providerProtocolProfileId": "profile_glm_coding_anthropic_v1",
      "type": "api_key",
      "status": "active",
      "groupName": "默认 GLM 分组",
      "concurrencyLimit": 3,
      "priority": 50,
      "credentials": {
        "api_key": "sk-glm-coding-anthropic-xxx",
        "base_url": "https://open.bigmodel.cn/api/anthropic",
        "supported_endpoint_modes": ["messages_json", "messages_sse"]
      },
      "notes": "GLM Coding Anthropic v1 账号"
    },
    {
      "ref": "deepseek-key-001",
      "name": "DeepSeek OpenAI API Key 账号 1",
      "providerCode": "deepseek",
      "type": "api_key",
      "status": "active",
      "providerProtocolProfileId": "profile_deepseek_openai_v1",
      "groupName": "默认 DeepSeek 分组",
      "concurrencyLimit": 3,
      "priority": 50,
      "credentials": {
        "api_key": "sk-xxx",
        "base_url": "https://api.deepseek.com",
        "supported_endpoint_modes": ["chat_json", "chat_sse"]
      },
      "notes": "DeepSeek OpenAI-compatible Chat Completions API Key 账号"
    },
    {
      "ref": "deepseek-claude-code-001",
      "name": "DeepSeek Claude Code API Key 账号 1",
      "providerCode": "deepseek",
      "providerProtocolProfileId": "profile_deepseek_anthropic_v1",
      "type": "api_key",
      "status": "active",
      "groupName": "默认 DeepSeek 分组",
      "concurrencyLimit": 3,
      "priority": 50,
      "credentials": {
        "api_key": "sk-deepseek-claude-code-xxx",
        "base_url": "https://api.deepseek.com/anthropic",
        "supported_endpoint_modes": ["messages_json", "messages_sse"]
      },
      "notes": "DeepSeek Anthropic v1 / Claude Code 账号"
    },
    {
      "ref": "hybrid-glm-chat-001",
      "name": "混合供应商 GLM Chat 上游账号 1",
      "providerCode": "hybrid",
      "providerProtocolProfileId": "profile_hybrid_openai_chat_v1",
      "type": "api_key",
      "status": "active",
      "groupName": "默认混合供应商分组",
      "concurrencyLimit": 3,
      "priority": 50,
      "credentials": {
        "api_key": "sk-glm-coding-xxx",
        "base_url": "https://open.bigmodel.cn/api/coding/paas/v4",
        "supported_endpoint_modes": ["chat_json", "chat_sse"]
      },
      "modelMappings": [
        {
          "sourceModel": "claude-sonnet-4",
          "sourceEndpointFamily": "messages",
          "upstreamModel": "glm-5.2",
          "upstreamEndpointFamily": "chat_completions",
          "enabled": true
        }
      ],
      "notes": "真实上游是 GLM OpenAI Chat；下游 Anthropic Messages 模型通过混合供应商账户映射到 glm-5.2"
    }
  ]
}
```

## 顶层字段

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `type` | 是 | 固定为 `juhe-ai-account-import`。 |
| `version` | 是 | 当前固定为 `1`。 |
| `proxies` | 否 | 代理数组；账户通过 `proxyRef` 引用代理 `ref`。 |
| `accounts` | 是 | 账户数组，至少 1 条。 |

## accounts 字段

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `ref` | 否 | 导入预览和错误定位用，不写入数据库。 |
| `name` | 是 | 账户名称，同一系统账户下不能重复。 |
| `providerCode` | 是 | 当前支持 `openai`、`gpt`、`anthropic`、`glm`、`deepseek`、`gemini` 和 `hybrid`。`openai` 表示通用 OpenAI-compatible 供应商，`gpt` 表示 GPT 专属供应商，`anthropic` 表示官方 Anthropic Claude 供应商，`glm` 表示智谱 GLM 供应商，`deepseek` 表示 DeepSeek hosted API 供应商，`gemini` 表示 Google Gemini 供应商，`hybrid` 表示混合供应商真实上游账户。 |
| `providerProtocolProfileId` | 是 | 显式供应商协议档案。常用值包括 `profile_openai_openai_v1`、`profile_gpt_openai_v1`、`profile_anthropic_anthropic_v1`、`profile_glm_general_openai_v1`、`profile_glm_coding_openai_v1`、`profile_glm_coding_anthropic_v1`、`profile_deepseek_openai_v1`、`profile_deepseek_anthropic_v1`、`profile_gemini_native_v1beta`、`profile_gemini_openai_chat_v1beta` 和混合供应商档案。 |
| `type` | 是 | `api_key` 或 `oauth`。 |
| `status` | 是 | `active`、`pending_test` 或 `disabled`；导入创建时 `active` 会转为 `pending_test`。 |
| `groupId` | 二选一 | 绑定已有分组 ID；优先级高于 `groupName`。 |
| `groupName` | 二选一 | 绑定或自动创建同名分组。 |
| `proxyRef` | 否 | 引用 `proxies[].ref` 或已有代理 ID。 |
| `proxyProfileId` | 否 | 直接引用已有代理 ID；不能和 `proxyRef` 同时填写。 |
| `concurrencyLimit` | 否 | 账户并发上限。 |
| `priority` | 否 | 调度优先级。 |
| `superPriorityEnabled` | 否 | 超级优先开关。 |
| `fallbackEnabled` | 否 | 降级备用开关。 |
| `supportedModels` | 否 | 支持模型列表；省略时按供应商默认支持模型回填，最终必须非空。 |
| `healthCheckModel` | 否 | 账户级检查模型；省略时按“个人默认 > 管理员系统默认 > 协议档案默认”初始化，最终必须持久化且属于账户 `supportedModels`。 |
| `healthCheckEndpointMode` | 否 | 后台健康检查精确请求形态；允许 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`、`messages_json`、`messages_sse`、`generate_content_json`、`generate_content_sse`。省略时 GPT 官方默认 `responses_sse`，OpenAI-compatible 默认 `chat_json`，Anthropic 默认 `messages_json`，Gemini Native 默认 `generate_content_json`。 |
| `modelMappings` | 否 | 账号模型映射列表；普通供应商账户只允许当前账号供应商模型目录内的同协议模型名改写，以及 OpenAI v1 的 Responses 到 Chat Completions 显式 bridge；混合供应商账户允许用该字段声明其他下游模型 / 协议入口到真实上游模型 / 协议的跨协议映射。条目包含 `sourceModel`、`sourceEndpointFamily`、`upstreamModel`、`upstreamEndpointFamily`、`enabled`。 |
| `tags` | 否 | 账户标签字符串数组；导入时按目标系统账户自动创建缺失标签并绑定到当前账户。 |
| `accountExpiresAt` | 否 | 账户过期时间。 |
| `availabilitySchedule` | 否 | 时间计划；启用时必须包含 `enabled: true`、`mode: "allow_windows"` 和 `windows`。 |
| `credentials` | 是 | 凭据对象。 |
| `notes` | 否 | 备注。 |

字段规则：

- `groupId` 和 `groupName` 同时存在时优先使用 `groupId`。
- `proxyRef` 和 `proxyProfileId` 不能同时填写。
- `concurrencyLimit` 必须是正整数；`priority` 必须是非负整数。
- `supportedModels` 只填明确支持的模型名称；不确定时省略，系统会按供应商默认支持模型回填。
- `healthCheckModel` 只在账户创建阶段使用默认链补齐；账户落库后后台系统检查严格读取该字段，不动态跟随供应商默认值。
- 通用 OpenAI-compatible 上游如需承接 OpenAI Responses 透传，请在 `credentials.supported_endpoint_modes` 中包含 `responses_json` 或 `responses_sse`。
- 普通供应商账户的 `modelMappings` 只做账号模型别名和 OpenAI v1 内部 bridge：OpenAI Chat 只能映射到 Chat Completions，OpenAI Responses 可映射到 Responses 或 Chat Completions，Anthropic Messages 只能映射到 Messages，Gemini native `streamGenerateContent` 可映射到 `generateContent`。其他跨协议方向不要写入普通账户导入数据。
- 混合供应商账户的 `modelMappings` 用于声明跨协议入口，`upstreamEndpointFamily` 直接表示真实上游目标协议，可选 `chat_completions`、`messages` 或 `generate_content`；未列出的转换方向会被拒绝。
- `modelMappings` 右侧 `upstreamEndpointFamily: "responses"` 只允许 `sourceEndpointFamily: "responses"` 的原生 Responses 别名，账号真实能力必须包含 `responses_json` 或 `responses_sse`。
- 需要把 OpenAI / Gemini 转到 Anthropic Messages、Anthropic / OpenAI 转到 Gemini native，或 Gemini native 转到 OpenAI / Anthropic 时，请使用 `providerCode: "hybrid"` 的混合供应商账户配置真实上游和协议转换；普通账户 `modelMappings` 只额外承接 OpenAI v1 Responses -> Chat Completions。
- `tags` 用于账户快速分类，单个账户最多 24 个标签，单个标签最长 40 个字符；空白标签会被忽略，同一账户内完全相同的标签会去重，大小写不同的标签会按不同标签保留。
- `modelMappings` 的 `sourceModel` 是客户端请求模型，`upstreamModel` 是该账户实际转发模型；普通供应商账户两者都必须来自当前账户供应商模型目录。OpenAI v1 `responses -> chat_completions` bridge 的左侧 `sourceModel` 是下游 Responses 别名，可选择当前供应商目录内的 Chat-only 模型；右侧 `upstreamModel` 仍必须支持 Chat Completions。通用 OpenAI-compatible 账号按 OpenAI-compatible 协议模型池处理，专用供应商不能引用其他供应商目录模型。混合供应商账户允许任意填写明确的下游模型名和真实上游模型名。
- `supportedModels` 最终必须非空，限制的是映射右侧 `upstreamModel`，不是左侧 `sourceModel`；映射右侧只能选择账户支持模型。
- `accountExpiresAt` 使用 ISO 时间字符串，例如 `2027-12-31T00:00:00.000Z`。
- `pending_test` 表示账户等待后台激活检查，检查成功后才参与调度；这是新建和导入账户的默认安全状态，人工测试不能改变它。

## credentials 字段

API Key 账户：

```json
{
  "api_key": "sk-xxx",
  "base_url": "https://api.openai.com/v1",
  "supported_endpoint_modes": ["chat_json", "chat_sse"]
}
```

DeepSeek OpenAI-compatible API Key 账户：

```json
{
  "api_key": "sk-xxx",
  "base_url": "https://api.deepseek.com",
  "supported_endpoint_modes": ["chat_json", "chat_sse"]
}
```

Anthropic API Key 账户：

```json
{
  "api_key": "sk-ant-api03-xxx",
  "base_url": "https://api.anthropic.com/v1",
  "supported_endpoint_modes": ["messages_json", "messages_sse", "message_token_counting"]
}
```

OAuth 账户：

```json
{
  "refresh_token": "refresh-token-xxx",
  "access_token": "access-token-xxx",
  "id_token": "id-token-xxx",
  "base_url": "https://api.openai.com/v1",
  "supported_endpoint_modes": ["responses_json", "responses_sse"],
  "account_id": "acct_xxx",
  "email": "user@example.com"
}
```

混合供应商账户：

```json
{
  "providerCode": "hybrid",
  "providerProtocolProfileId": "profile_hybrid_openai_chat_v1",
  "type": "api_key",
  "credentials": {
    "api_key": "sk-glm-coding-xxx",
    "base_url": "https://open.bigmodel.cn/api/coding/paas/v4",
    "supported_endpoint_modes": ["chat_json", "chat_sse"]
  },
  "modelMappings": [
    {
      "sourceModel": "claude-sonnet-4",
      "sourceEndpointFamily": "messages",
      "upstreamModel": "glm-5.2",
      "upstreamEndpointFamily": "chat_completions",
      "enabled": true
    }
  ]
}
```

规则：

- `providerCode = openai` 时只允许 `type = api_key`，且必须有 `credentials.api_key`。
- `providerCode = gpt` 且 `type = api_key` 时必须有 `credentials.api_key`。
- `providerCode = gpt` 且 `type = oauth` 时必须有 `credentials.refresh_token` 或 `credentials.access_token`。
- `providerCode = gpt` 的 API Key 和 OAuth 凭据可选包含 `service_tier_override=default|priority|flex` 与 `reasoning_effort_override=none|minimal|low|medium|high|xhigh|max`；空值表示不覆盖客户端请求。后端按最终支持模型的精确能力数组和适配器能力校验，拒绝 `fast`、`auto`、`ultra`，非 GPT 账户不得提交这两个字段。
- `service_tier_override` 与 `reasoning_effort_override` 会在模型映射和协议桥接确定实际上游模型后覆盖最终上游请求；OpenAI Responses Multi-agent Beta 不属于本期导入字段。
- `providerCode = anthropic` 时只允许 `type = api_key`，且必须有 `credentials.api_key`。当前导入协议不接受 Anthropic OAuth、Setup Token、Claude Code token、`refresh_token` 或 `access_token`。
- `providerCode = anthropic` 不接受 `credentials.anthropic_version` 或 `credentials.anthropic_beta`。`anthropic-version` 是客户端请求头，缺省时由网关按协议默认 `2023-06-01` 补齐；`anthropic-beta` 只透传客户端显式 header。
- `providerCode = glm` 时只允许 `type = api_key`，且必须有 `credentials.api_key`。
- `providerCode = glm` 必须显式填写 `profile_glm_general_openai_v1`、`profile_glm_coding_openai_v1` 或 `profile_glm_coding_anthropic_v1`，分别表示通用 GLM API、GLM Coding OpenAI Chat 和 GLM Coding Anthropic。预览、确认导入和导出都按该字段 round-trip。
- `providerCode = glm` 且 `providerProtocolProfileId = profile_glm_coding_openai_v1` 时，账号只保存真实上游能力 `chat_json`、`chat_sse`；Responses / Codex Responses 桥接由普通账号显式 `responses -> chat_completions` 模型别名处理。
- `providerCode = glm` 且 `providerProtocolProfileId = profile_glm_coding_anthropic_v1` 时，`credentials.supported_endpoint_modes` 只保存 `messages_json`、`messages_sse`。
- `providerCode = glm` 的通用 GLM API、GLM Coding OpenAI Chat 和 GLM Coding Anthropic 都要求 `credentials.base_url` 显式填写到对应协议档案可接受的根地址，不能依赖后端猜测。
- NewAPI / OpenAI-compatible 聚合入口如果只给代理根地址，例如 `https://vsllm.com`，优先按通用 `providerCode = openai` 导入，OpenAI-compatible 档案会拼接 `/v1/chat/completions`；如果按 `providerCode = glm` 专用 OpenAI v1 档案导入，`credentials.base_url` 必须填写到该代理的 OpenAI v1 根，例如 `https://vsllm.com/v1`，不能只填站点根地址。
- `providerCode = deepseek` 时只允许 `type = api_key`，且必须有 `credentials.api_key`。DeepSeek OpenAI-compatible 必须显式填写 `providerProtocolProfileId = profile_deepseek_openai_v1`；DeepSeek Claude Code 必须显式填写 `providerProtocolProfileId = profile_deepseek_anthropic_v1`。当前不接受 DeepSeek OAuth、Refresh Token、Setup Token 或 Claude Code token；Anthropic-compatible 只通过 API Key + 协议档案导入。
- `providerCode = deepseek` 且 `providerProtocolProfileId = profile_deepseek_openai_v1` 时，`credentials.base_url` 默认建议填写 `https://api.deepseek.com`；如使用代理地址或专属部署地址，仍必须通过 SSRF 防护和 OpenAI-compatible base URL 校验。
- `providerCode = deepseek` 且 `providerProtocolProfileId = profile_deepseek_anthropic_v1` 时，`credentials.base_url` 默认建议填写 `https://api.deepseek.com/anthropic`；使用 NewAPI 类代理时可以填写代理根地址，例如 `https://vsllm.com`。
- `providerCode = deepseek` 且 `providerProtocolProfileId = profile_deepseek_openai_v1` 时，账号只保存真实上游能力 `chat_json`、`chat_sse`；Responses / Codex Responses 桥接由普通账号显式 `responses -> chat_completions` 模型别名处理。DeepSeek Claude Code 档案不使用这条规则。
- DeepSeek beta 能力不能只靠导入 `base_url` 猜测启用，必须由后续明确 endpoint mode / 能力开关控制。
- `providerCode = gemini` 时只允许 `type = api_key`，且必须有 `credentials.api_key`。Gemini 原生必须显式填写 `providerProtocolProfileId = profile_gemini_native_v1beta`；如需 OpenAI Chat / Codex Responses 使用 Gemini，必须填写 `providerProtocolProfileId = profile_gemini_openai_chat_v1beta`。Gemini native 下游如需使用 Gemini OpenAI Chat，请使用混合供应商账户，不要写入账户 `modelMappings`。
- `providerCode = gemini` 且 `providerProtocolProfileId = profile_gemini_native_v1beta` 时，`credentials.base_url` 默认建议填写 `https://generativelanguage.googleapis.com`，`credentials.supported_endpoint_modes` 使用 `generate_content_json`、`generate_content_sse`、`count_tokens`、`embed_content`，不配置 OpenAI / Anthropic 跨协议模型映射。
- `providerCode = gemini` 且 `providerProtocolProfileId = profile_gemini_openai_chat_v1beta` 时，`credentials.base_url` 默认建议填写 `https://generativelanguage.googleapis.com/v1beta/openai`，`credentials.supported_endpoint_modes` 使用 `chat_json`、`chat_sse`；如需 Codex / Responses 使用 Gemini OpenAI Chat，可在普通账号中配置 `responses -> chat_completions` 模型别名；Gemini native 下游使用 Gemini OpenAI Chat 时请使用混合供应商账户。该档案不接受 Anthropic Messages 来源的账户模型别名。
- `providerCode = hybrid` 时只允许 `type = api_key`，且必须有 `credentials.api_key`。混合供应商账户是真实 AI 账户，不能填写目标账户 ID、目标分组 ID 或 API Key 级路由规则。
- `providerCode = hybrid` 时，`credentials.base_url` 填真实上游根地址；可按真实上游能力在 OpenAI、Anthropic 和 Gemini endpoint modes 中选择 `credentials.supported_endpoint_modes`。`modelMappings.upstreamEndpointFamily` 决定本条映射实际请求 Chat Completions、Messages 还是 Gemini GenerateContent 上游。
- `credentials.base_url` 必须显式填写，不从供应商配置自动补值。
- `credentials.supported_endpoint_modes` 可限制协议端点能力。OpenAI v1 枚举值为 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`；Anthropic 枚举值为 `messages_json`、`messages_sse`、`message_token_counting`；Gemini 原生枚举值为 `generate_content_json`、`generate_content_sse`、`count_tokens`、`embed_content`。省略时通用 `openai` API Key 默认 Chat JSON/SSE，GPT API Key 默认四项全开，GPT OAuth 默认 Responses JSON/SSE，官方 Anthropic 默认 Messages JSON/SSE/Count Tokens，GLM OpenAI 档案默认 Chat JSON/SSE，GLM Coding Anthropic 默认 Messages JSON/SSE，DeepSeek OpenAI-compatible 默认 Chat JSON/SSE，DeepSeek Claude Code 默认 Messages JSON/SSE，Gemini 原生默认 generateContent JSON/SSE/Count Tokens，Gemini OpenAI Chat 默认 Chat JSON/SSE，混合供应商默认全量 endpoint modes。
- Codex Responses 请求的客户端画像由网关自动识别；目标账号必须具备对应真实上游能力。GPT / 通用 OpenAI 原生 Responses 账号必须具备 `responses_sse`；GLM Coding、DeepSeek 和 Gemini OpenAI Chat 账号必须配置 `responses -> chat_completions` 模型别名并具备 `chat_sse`。Gemini native `streamGenerateContent` 通过混合供应商账户桥接到 Chat 上游时要求真实上游具备 `chat_sse`，桥接到 Anthropic Messages 上游时要求真实上游具备 `messages_sse`。
- `credentials` 只接受当前账户类型支持的字段；未知字段会在预览阶段标记为失败。
- 凭据属于敏感数据，只在受控账户凭据路径保存和展示。

## proxies 字段

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `ref` | 是 | 代理引用标识，供账户 `proxyRef` 使用。 |
| `name` | 是 | 代理名称；已有同名代理时复用。 |
| `type` | 是 | `http`、`https`、`socks5`、`socks5h`。 |
| `host` | 是 | 代理主机。 |
| `port` | 是 | 代理端口，1 到 65535。 |
| `username` | 否 | 代理用户名。 |
| `password` | 否 | 代理密码。 |
| `enabled` | 否 | 是否启用，默认 `true`。 |

## 常见失败原因

- 顶层不是 JSON 对象，或者带了 Markdown 代码块。
- `type` / `version` 不匹配当前协议。
- `accounts` 为空、超过 50 条，或账户名称在同一系统账户下重复。
- 账户缺少 `groupId` / `groupName`，或分组供应商和账户供应商不一致。
- API Key 账户缺少 `credentials.api_key`。
- OAuth 账户同时缺少 `credentials.refresh_token` 和 `credentials.access_token`。
- `proxyRef` 指向的代理不存在、未在 `proxies` 中声明，或普通用户尝试创建新代理。
- 未知字段写入了错误层级，例如把外部系统字段直接放进 `credentials`。
