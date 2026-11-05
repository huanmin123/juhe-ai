# AI 账户导入协议

## 定位

AI 账户导入弹窗支持 5 种来源模式：`native`（本协议自定义 JSON）、`sub2api`（Sub2API sub2api-data / sub2api-bundle v1）、`newapi`（NewAPI Channel JSON）、`oneapi`（One-API Channel JSON）和 `cpa`（CLIProxyAPI config.yaml / Codex auth JSON）。`native` 之外的来源模式只提取 OpenAI 平台条目，并按来源模型名推断真实厂商；无法推断的记录和非核心字段会在预览中跳过或计数忽略；来源模型字段的映射方式和模型目录拦截语义见下文「来源模型映射与目录拦截」。无法直接映射的数据仍可先用 AI、脚本或表格工具整理为 `native` 协议后再导入。

当前协议：

- `type`: `juhe-ai-account-import`
- `version`: `1`
- 单次请求受系统 API `256KB` JSON 请求体上限约束，当前接口按小批量导入设计。
- 单次最多导入 50 个账户、20 个代理，避免 DB service 在一次请求内长时间同步解析和校验大数组。
- 导入弹窗提供可复制 AI 提示词和“导出协议 Markdown”按钮；提示词配合本协议文件交给其他 AI 做格式转换。
- `native` 模式的导入接口只接受合法 JSON，不接受 Markdown、JSONL、CSV、带注释 JSON 或外部系统原始格式；其他来源模式的解析约定见「来源模型映射与目录拦截」。

## 转换硬性规则

- 输出必须是一个 JSON 对象，不能包在 Markdown 代码块里，不能附带解释文字。
- 字段名严格使用本协议定义，不要把 `providerCode` 改成 `provider_code`，也不要把 `api_key` 改成 `apiKey`。
- 顶层 `type` 固定为 `juhe-ai-account-import`，`version` 固定为数字 `1`。
- `accounts` 至少 1 条；每个账户必须显式填写 `name`、`providerCode`、`providerProtocolProfileId`、`type`、`status`、`credentials`，以及 `groupId` 或 `groupName`。
- 每个账户必须显式提交非空 `connections` 数组和健康检查三件套 `healthCheckConnectionPresetCode`、`healthCheckModel`、`healthCheckEndpointMode`；`native` 导入不做缺省补齐，四键缺任一该账户在预览中标记失败（`sub2api` 等非原生来源由适配器按契约补齐三件套，但 `connections` 仍必须由来源包显式携带）。
- `providerCode` 只能是九家真实厂商：`gpt`、`xai`、`anthropic`、`gemini`、`deepseek`、`glm`、`minimax`、`volcengine`、`qwen`。`openai` 兼容、`hybrid` 与 `generic_http` 已于 2026-11-05 退役，提交这些值会返回「不支持的供应商」。`gpt` 支持 `api_key|oauth`；`xai` 支持官方 API Key 与 Responses-only Grok `oauth`；`anthropic` 支持 API Key 与官方 `oauth`；`gemini` native 支持 API Key 与 `google_oauth`，后者必须保存 `code_assist|google_one|ai_studio` 模式，OpenAI Chat 档案只支持 API Key；其余厂商按各自档案与账户类型约束。
- `providerProtocolProfileId` 是协议档案唯一入口；导入协议不接收旧接入类型别名，也不按 `base_url`、供应商默认值或历史字段猜测档案。
- 来源模式没有可信厂商时（Sub2API/NewAPI/One-API/CLIProxyAPI），适配器按来源账户模型名/前缀推断九家厂商：所有模型必须收敛到同一厂商，缺模型、零命中或跨厂商冲突一律跳过该来源记录并写入预览报告；不提供通用连接器兜底。
- 账号导入不填写 `clientCompatibility`。下游客户端画像由网关运行时内部识别；OpenAI v1 普通账户可用 `modelMappings` 显式声明 Responses 到 Chat Completions，其余跨协议映射已随 hybrid 退役，不再支持。
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

## 来源模型映射与目录拦截

来源账户对象中的模型相关字段按来源模式映射：

| 来源模式 | 来源模型字段 | 导入行为 |
| --- | --- | --- |
| `native` | 协议账户对象的 `supportedModels`、`healthCheckModel`、`healthCheckEndpointMode`、`modelMappings` | 按本协议字段直接导入。 |
| `sub2api` | 来源账户对象的 `supportedModels`、`healthCheckModel`、`healthCheckEndpointMode`、`modelMappings` | 透传为账户的支持模型 / 健康检查 / 模型映射配置。 |
| `newapi` / `oneapi` | Channel 的 `models` 字段（逗号 / 分号 / 换行分隔） | 映射为账户 `supportedModels`，去空白、去重、保序；`model_mapping` 字段仍不读取。 |
| `cpa` | 来源模型列表 | 作为厂商推断输入读取，不写入账户 `supportedModels`（合法形态不计入忽略字段）。 |

厂商推断语义（适用于 `sub2api`、`newapi`、`oneapi`、`cpa`）：

- 来源记录没有显式真实厂商时，适配器用模型名匹配九家厂商：先精确匹配目标目录的 `public_model`/`upstream_model`，再按最长模型名前缀匹配，最后使用静态前缀表（`gpt-`/`o1`/`o3`/`claude`/`gemini`/`grok`/`deepseek`/`glm`/`qwen`/`doubao`/`minimax` 等）。
- 所有模型必须指向同一厂商；缺模型列表、零命中或跨厂商冲突时跳过该来源记录，报告包含原因与模型样本。
- 推断成功后选择该厂商在 openai_v1 协议面的连接 preset；CLIProxyAPI `codex-api-key` 条目直接按 gpt 处理。

目录拦截语义（适用于 `native`、`sub2api`、`newapi`、`oneapi`）：

- 账户支持模型必须已存在于平台模型目录（含价格）；目录外模型会导致该账户在预览中标记失败，且整次导入无法确认。
- 失败提示原文：`账户支持模型不在供应商模型目录中：…。请先在模型目录创建这些模型并配置价格后再导入`。
- 解决办法：先在「供应商」页（管理员，路由 `/providers`）或「我的模型」页（`/my-models`）的模型目录中创建这些自定义模型并配置价格，再重新导入。
- 模型映射（`modelMappings`）的来源 / 目标模型同样按模型目录校验，失败时使用相同的引导提示。

## 导出流程

`我的 AI 账户` 和 `AI 账户管理` 支持导出 JSON 文件，导出结果直接使用本协议：

- 导出文件顶层仍为 `type: "juhe-ai-account-import"`、`version: 1`。
- 单次最多导出 50 个自有 AI 账户，和单次导入账户上限一致。
- 顶部“导出 JSON”按当前账户列表筛选条件导出，最多处理前 50 条匹配结果；勾选账户后，批量工具栏“导出 JSON”只导出当前勾选账户。
- 导出只包含当前用户或管理员目标作用域内有权查看凭据和编辑的自有账户；授权账户实例不导出。
- 如果账户绑定了可用代理，导出文件会同时写入 `proxies` 并通过账户 `proxyRef` 引用，便于再次导入时自动创建或复用代理。
- 导出会保留账户标签为 `tags` 字符串数组；再次导入时会在目标系统账户维度自动创建缺失标签并绑定到账户。
- 导出会保留账户级 `healthCheckModel` 和精确 `healthCheckEndpointMode`；再次导入时检查模型必须属于同一账户的 `supportedModels`。文本请求形态必须是账户已启用的 Chat / Responses / Messages / GenerateContent JSON 或 Streaming mode；`images_json` 只允许模型目录证明检查模型支持 `images` 时使用，否则预览失败。导入省略请求形态时按新账户默认规则解析。
- 导出会为每个账户写出完整 `connections` 数组和健康检查三件套 `healthCheckConnectionPresetCode`、`healthCheckModel`、`healthCheckEndpointMode`，与导入必填键一一对应，导出文件可直接回导。
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
      "ref": "gpt-oauth-001",
      "name": "GPT OAuth 账号 1",
      "providerCode": "gpt",
      "providerProtocolProfileId": "profile_gpt_openai_v1",
      "type": "oauth",
      "status": "active",
      "groupName": "默认 GPT 分组",
      "concurrencyLimit": 3,
      "rpmLimit": 0,
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
      "supportedModels": ["gpt-5.2"],
      "connections": [
        {
          "connectionPresetCode": "gpt_openai_v1",
          "baseUrl": "https://api.openai.com/v1",
          "endpointModes": ["responses_json", "responses_sse"],
          "enabled": true
        }
      ],
      "healthCheckConnectionPresetCode": "gpt_openai_v1",
      "healthCheckModel": "gpt-5.2",
      "healthCheckEndpointMode": "responses_sse",
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
      "rpmLimit": 0,
      "priority": 50,
      "tags": ["Claude"],
      "credentials": {
        "api_key": "sk-ant-api03-xxx",
        "base_url": "https://api.anthropic.com/v1",
        "supported_endpoint_modes": ["messages_json", "messages_sse", "message_token_counting"]
      },
      "supportedModels": ["claude-sonnet-4-6"],
      "connections": [
        {
          "connectionPresetCode": "anthropic_v1",
          "baseUrl": "https://api.anthropic.com/v1",
          "endpointModes": ["messages_json", "messages_sse"],
          "enabled": true
        }
      ],
      "healthCheckConnectionPresetCode": "anthropic_v1",
      "healthCheckModel": "claude-sonnet-4-6",
      "healthCheckEndpointMode": "messages_sse",
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
      "rpmLimit": 0,
      "priority": 50,
      "credentials": {
        "api_key": "zhipu-api-key-xxx",
        "base_url": "https://open.bigmodel.cn/api/coding/paas/v4",
        "supported_endpoint_modes": ["chat_json", "chat_sse"]
      },
      "supportedModels": ["glm-5.2"],
      "connections": [
        {
          "connectionPresetCode": "glm_coding_openai_v1",
          "baseUrl": "https://open.bigmodel.cn/api/coding/paas/v4",
          "endpointModes": ["chat_json", "chat_sse"],
          "enabled": true
        }
      ],
      "healthCheckConnectionPresetCode": "glm_coding_openai_v1",
      "healthCheckModel": "glm-5.2",
      "healthCheckEndpointMode": "chat_sse",
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
      "rpmLimit": 0,
      "priority": 50,
      "credentials": {
        "api_key": "sk-glm-coding-anthropic-xxx",
        "base_url": "https://open.bigmodel.cn/api/anthropic/v1",
        "supported_endpoint_modes": ["messages_json", "messages_sse"]
      },
      "supportedModels": ["glm-5.2"],
      "connections": [
        {
          "connectionPresetCode": "glm_coding_anthropic_v1",
          "baseUrl": "https://open.bigmodel.cn/api/anthropic/v1",
          "endpointModes": ["messages_json", "messages_sse"],
          "enabled": true
        }
      ],
      "healthCheckConnectionPresetCode": "glm_coding_anthropic_v1",
      "healthCheckModel": "glm-5.2",
      "healthCheckEndpointMode": "messages_sse",
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
      "rpmLimit": 0,
      "priority": 50,
      "credentials": {
        "api_key": "sk-xxx",
        "base_url": "https://api.deepseek.com/v1",
        "supported_endpoint_modes": ["chat_json", "chat_sse", "responses_json", "responses_sse"]
      },
      "supportedModels": ["deepseek-v4.1-flash"],
      "connections": [
        {
          "connectionPresetCode": "deepseek_openai_v1",
          "baseUrl": "https://api.deepseek.com/v1",
          "endpointModes": ["chat_json", "chat_sse", "responses_json", "responses_sse"],
          "enabled": true
        }
      ],
      "healthCheckConnectionPresetCode": "deepseek_openai_v1",
      "healthCheckModel": "deepseek-v4.1-flash",
      "healthCheckEndpointMode": "chat_sse",
      "notes": "DeepSeek OpenAI-compatible Chat Completions / Responses API Key 账号"
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
      "rpmLimit": 0,
      "priority": 50,
      "credentials": {
        "api_key": "sk-deepseek-claude-code-xxx",
        "base_url": "https://api.deepseek.com/anthropic/v1",
        "supported_endpoint_modes": ["messages_json", "messages_sse"]
      },
      "supportedModels": ["deepseek-v4.1-flash"],
      "connections": [
        {
          "connectionPresetCode": "deepseek_anthropic_v1",
          "baseUrl": "https://api.deepseek.com/anthropic/v1",
          "endpointModes": ["messages_json", "messages_sse"],
          "enabled": true
        }
      ],
      "healthCheckConnectionPresetCode": "deepseek_anthropic_v1",
      "healthCheckModel": "deepseek-v4.1-flash",
      "healthCheckEndpointMode": "messages_sse",
      "notes": "DeepSeek Anthropic v1 / Claude Code 账号"
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
| `providerCode` | 是 | 只能是九家真实厂商：`gpt`、`xai`、`anthropic`、`gemini`、`deepseek`、`glm`、`minimax`、`volcengine`、`qwen`。`openai`、`hybrid` 与 `generic_http` 已退役，提交即失败。 |
| `providerProtocolProfileId` | 是 | 显式供应商协议档案。可用值：`profile_gpt_openai_v1`、`profile_xai_openai_v1`、`profile_anthropic_anthropic_v1`、`profile_glm_general_openai_v1`、`profile_glm_coding_openai_v1`、`profile_glm_coding_anthropic_v1`、`profile_deepseek_openai_v1`、`profile_deepseek_anthropic_v1`、`profile_gemini_native_v1beta`、`profile_gemini_openai_chat_v1beta`、`profile_minimax_openai_v1`、`profile_volcengine_openai_v1`、`profile_qwen_openai_v1`。 |
| `type` | 是 | `api_key`、`oauth` 或 `google_oauth`，具体可用值受供应商协议档案约束。 |
| `status` | 是 | `active`、`pending_test` 或 `disabled`；导入创建时 `active` 会转为 `pending_test`。 |
| `groupId` | 二选一 | 绑定已有分组 ID；优先级高于 `groupName`。 |
| `groupName` | 二选一 | 绑定或自动创建同名分组。 |
| `proxyRef` | 否 | 引用 `proxies[].ref` 或已有代理 ID。 |
| `proxyProfileId` | 否 | 直接引用已有代理 ID；不能和 `proxyRef` 同时填写。 |
| `concurrencyLimit` | 否 | 账户并发上限。 |
| `rpmLimit` | 否 | 每分钟请求上限（RPM）；非负整数，`0` 或缺省表示不限制。按自然分钟固定窗口统计真实向上游派发的次数（含 Key 轮换重试与失败尝试），与并发上限同时生效。 |
| `priority` | 否 | 调度优先级。 |
| `superPriorityEnabled` | 否 | 超级优先开关。 |
| `fallbackEnabled` | 否 | 降级备用开关。 |
| `supportedModels` | 否 | 支持模型列表；省略时按供应商默认支持模型回填，最终必须非空。填写 `healthCheckModel` 时建议同时显式给出本字段，便于核对成员关系。 |
| `connections` | 是 | 连接集合，非空必填；每个元素描述一条上游连接（见下文「connections 条目字段」）。元素 `connectionPresetCode` 在同一账户内不能重复。 |
| `healthCheckConnectionPresetCode` | 是 | 健康检查三件套之一；必须等于本账户 `connections` 中一条 `enabled: true` 连接的 `connectionPresetCode`，落库时同事务解析为 `health_check_connection_id`。 |
| `healthCheckModel` | 是 | 健康检查三件套之一；账户级检查模型，必须属于账户 `supportedModels`（含供应商默认回填后的集合）。管理面手动创建时才允许省略并由创建事务在最终支持模型集内解析；`native` 导入必须显式提交。 |
| `healthCheckEndpointMode` | 是 | 健康检查三件套之一；后台健康检查精确请求形态。允许 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`、`messages_json`、`messages_sse`、`generate_content_json`、`generate_content_sse`、`interactions_json`、`interactions_sse`、`images_json`。`images_json` 只允许模型目录证明检查模型支持 `images` 时使用。必须属于 `healthCheckConnectionPresetCode` 指向连接的 `endpointModes`，且检查模型须在模型目录声明该形态。非原生来源省略时按流式优先解析（[AI账户检查模型与人工测试设计](AI账户检查模型与人工测试设计.md) §健康检查请求形态 2026-10-19 定案）：先从连接 `endpointModes` 取首个可检查 `_sse` 形态，无 `_sse` 才按声明顺序回退；`native` 导入必须显式提交，不做缺省解析。 |
| `modelMappings` | 否 | 账号模型映射列表；只允许当前账号供应商模型目录内的同族模型名改写，以及 OpenAI v1 的 Responses 到 Chat Completions 显式 bridge。条目包含 `sourceModel`、`sourceEndpointFamily`、`upstreamModel`、`upstreamEndpointFamily`、`enabled`。 |
| `tags` | 否 | 账户标签字符串数组；导入时按目标系统账户自动创建缺失标签并绑定到当前账户。 |
| `accountExpiresAt` | 否 | 账户过期时间。 |
| `availabilitySchedule` | 否 | 时间计划；启用时必须包含 `enabled: true`、`mode: "allow_windows"` 和 `windows`。 |
| `credentials` | 是 | 凭据对象。 |
| `notes` | 否 | 备注。 |

字段规则：

- `groupId` 和 `groupName` 同时存在时优先使用 `groupId`。
- `proxyRef` 和 `proxyProfileId` 不能同时填写。
- `concurrencyLimit` 必须是正整数；`rpmLimit` 必须是非负整数（`0` 表示不限制）；`priority` 必须是非负整数。
- `supportedModels` 只填明确支持的模型名称；不确定时省略，系统会按供应商默认支持模型回填。
- 健康检查三件套 `healthCheckConnectionPresetCode`、`healthCheckModel`、`healthCheckEndpointMode` 在 `native` 导入中必须全部显式提交，缺一整账户失败；三键共同约束为：preset 指向本账户一条 `enabled: true` 连接，检查形态属于该连接 `endpointModes`，检查模型属于 `supportedModels`。
- `healthCheckModel` 只在账户创建阶段使用默认链补齐；账户落库后后台系统检查严格读取该字段，不动态跟随供应商默认值。
- 需要承接 OpenAI Responses 的 GPT 账户，请在 `credentials.supported_endpoint_modes` 中包含 `responses_json` 或 `responses_sse`。
- `modelMappings` 只做账号模型别名和 OpenAI v1 bridge：Chat Completions → Chat Completions，Responses → Responses，Responses → Chat Completions（仅 stream=true），Messages → Messages，GenerateContent / 流式 GenerateContent → GenerateContent，video_generation → video_generation，tts → tts。其他跨协议方向会被拒绝。
- `modelMappings` 右侧 `upstreamEndpointFamily: "responses"` 只允许 `sourceEndpointFamily: "responses"` 的原生 Responses 别名，账号真实能力必须包含 `responses_json` 或 `responses_sse`。
- 跨协议（如 OpenAI 到 Anthropic Messages）已无账户类型承载；需要对应协议时请直接创建该厂商的原生账户，不要尝试写入跨协议映射。
- `tags` 用于账户快速分类，单个账户最多 24 个标签，单个标签最长 40 个字符；空白标签会被忽略，同一账户内完全相同的标签会去重，大小写不同的标签会按不同标签保留。
- `modelMappings` 的 `sourceModel` 是客户端请求模型，`upstreamModel` 是该账户实际转发模型；两者都必须来自当前账户供应商模型目录。OpenAI v1 `responses -> chat_completions` bridge 的左侧 `sourceModel` 是下游 Responses 别名，可选择当前供应商目录内的 Chat-only 模型；右侧 `upstreamModel` 仍必须支持 Chat Completions。
- `supportedModels` 最终必须非空，限制的是映射右侧 `upstreamModel`，不是左侧 `sourceModel`；映射右侧只能选择账户支持模型。
- `accountExpiresAt` 使用 ISO 时间字符串，例如 `2027-12-31T00:00:00.000Z`。
- `pending_test` 表示账户等待后台激活检查，检查成功后才参与调度；这是新建和导入账户的默认安全状态，人工测试不能改变它。

## connections 条目字段

每个账户的 `connections` 必须是非空对象数组，元素键固定如下；出现未知键、缺少任一必填键或 `endpointModes` 为空都会导致该账户失败（与创建链共享 `ParseConnectionInput` 严格解析，不造兼容层）：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `connectionPresetCode` | 是 | 连接器注册表预设标识，当前恰好 14 个值：`gpt_openai_v1`、`xai_openai_v1`、`deepseek_openai_v1`、`deepseek_anthropic_v1`、`anthropic_v1`、`gemini_openai_chat_v1beta`、`gemini_native_v1beta`、`glm_general_openai_v1`、`glm_coding_openai_v1`、`glm_coding_anthropic_v1`、`minimax_media`、`volcengine_ark`、`volcengine_openspeech`、`qwen_dashscope`。必须与账户 `providerCode` + `providerProtocolProfileId` 的能力组合匹配，同一账户内不能重复。 |
| `baseUrl` | 是 | 包含版本前缀的根地址（如 `/v1`、`/api/v3`），不能带 `/chat/completions`、`/messages` 等具体接口路径，必须通过 registry URL 契约校验；通常与 `credentials.base_url` 相同。 |
| `endpointModes` | 是 | 非空字符串数组，无重复；取值必须在该预设与账户组合允许的 mode 集合内。常用组合：`gpt_openai_v1` API Key 允许 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`；`gpt_openai_v1` 与 `xai_openai_v1` 的 OAuth 收窄为 `responses_json`、`responses_sse`；`glm_general_openai_v1`、`glm_coding_openai_v1` 允许 `chat_json`、`chat_sse`；`deepseek_anthropic_v1`、`glm_coding_anthropic_v1` 允许 `messages_json`、`messages_sse`；`anthropic_v1` 允许 `messages_json`、`messages_sse`、`message_token_counting`。 |
| `enabled` | 是 | 是否启用；必须显式填写，省略或非布尔即失败。健康检查只能指向 `enabled: true` 的连接。 |
| `protocolSurfaceCode` | 否 | 协议面回显；省略即可，填写时必须与预设注册的协议面一致（`openai_v1`、`anthropic_v1`、`gemini_v1beta`）。 |

## credentials 字段

API Key 账户：

```json
{
  "api_key": "sk-xxx",
  "base_url": "https://api.openai.com/v1",
  "supported_endpoint_modes": ["chat_json", "chat_sse", "responses_json", "responses_sse"]
}
```

DeepSeek OpenAI-compatible API Key 账户：

```json
{
  "api_key": "sk-xxx",
  "base_url": "https://api.deepseek.com/v1",
  "supported_endpoint_modes": ["chat_json", "chat_sse", "responses_json", "responses_sse"]
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

Gemini Google OAuth 账户：

```json
{
  "refresh_token": "google-refresh-token-xxx",
  "client_id": "google-client-id-xxx",
  "client_secret": "google-client-secret-xxx",
  "oauth_type": "ai_studio",
  "tier_id": "aistudio_free",
  "quota_project_id": "project-id",
  "base_url": "https://generativelanguage.googleapis.com/v1beta",
  "supported_endpoint_modes": ["generate_content_json", "generate_content_sse", "interactions_json", "interactions_sse", "count_tokens", "embed_content"]
}
```

Gemini Code Assist / Google One OAuth 账户必须把 `oauth_type` 改为 `code_assist` 或 `google_one`，填写 `project_id`，`base_url` 使用 `https://cloudcode-pa.googleapis.com`，`supported_endpoint_modes` 只能是 `generate_content_json`、`generate_content_sse`。内置 client 的真实 `client_id` / `client_secret` 由托管授权流程写入；导入时必须和 Refresh Token 的签发 client 一致。

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

Grok OAuth 账户：

```json
{
  "refresh_token": "grok-refresh-token-xxx",
  "access_token": "grok-access-token-xxx",
  "id_token": "grok-id-token-xxx",
  "client_id": "b1a00492-073a-47ea-816f-4c329264a828",
  "token_type": "Bearer",
  "scope": "openid profile email offline_access grok-cli:access api:access",
  "base_url": "https://cli-chat-proxy.grok.com/v1",
  "supported_endpoint_modes": ["responses_json", "responses_sse"]
}
```

规则：

- `providerCode = gpt` 且 `type = api_key` 时必须有 `credentials.api_key`。
- `providerCode = gpt` 且 `type = oauth` 时必须有 `credentials.refresh_token` 或 `credentials.access_token`。
- `providerCode = xai` 允许 `type = api_key|oauth`，并必须使用 `profile_xai_openai_v1`。API Key 必须有 `credentials.api_key`，默认地址 `https://api.x.ai/v1`；OAuth 必须有 `credentials.access_token`，可选 Refresh / ID Token 和公开 claims，默认地址 `https://cli-chat-proxy.grok.com/v1`，endpoint modes 只能是 `responses_json/responses_sse`。SSO Cookie 不属于通用导入凭据，必须通过 Grok 专属 `/sso-to-oauth` 接口转换，导入 JSON 不接受 `sso`、`sso-rw` 或 Cookie 字段。
- 文本模型账户凭据可选包含字符串 `service_tier_override` 与 `reasoning_effort_override`；空值表示不覆盖客户端请求。导入校验只做 token 形状与协议族全集校验：openai 族（gpt/openai/deepseek/glm/xai 等 OpenAI 兼容供应商）思考级别为 `none|minimal|low|medium|high|xhigh|max`、服务等级为 `default|priority|flex`；anthropic 族思考级别为 `low|medium|high|xhigh|max`、不提供服务等级选项；gemini 族思考级别为 `minimal|low|medium|high`、服务等级为 `default|priority|flex`。导入不再要求已选模型在当前供应商目录声明该值，`service_tier_override=default` 恒可保存；`gpt` 仍只接受既有 OpenAI 值（`ultra`/`fast`/`auto` 被拒），API Key 与 OAuth 使用同一规则。Anthropic 原生 `service_tier=auto|standard_only` 与 `speed` 不进入本站通用服务等级控件，思考级别覆盖仍映射到 `output_config.effort`；Gemini 按原生 `service_tier` / thinking level 映射。thinking budget 等原生字段只做同协议保护，不属于这两个账户控件。
- `service_tier_override` 与 `reasoning_effort_override` 在模型映射和协议桥接确定实际上游模型后逐字段判断；思考级别按档位序向下兼容降级到最终模型声明值中不高于配置值的最高档，无可用档位时保留客户端原值；服务等级不降级，`default` 恒删除客户端 `service_tier`，`priority`/`flex` 仅在最终模型声明支持时写入，否则保留客户端原值；不报账户覆盖错误。OpenAI Responses Multi-agent Beta 不属于本期导入字段。
- `providerCode = anthropic` 允许 `type = api_key` 或 `type = oauth`。`type = api_key` 时必须有 `credentials.api_key`；`type = oauth` 时必须有 `credentials.access_token`，可选附带 `credentials.refresh_token` 作为补充保存字段。当前导入协议不接受 `workload_identity`、IdP JWT、Claude.ai / Claude Code 订阅网页登录 OAuth、Setup Token 或 Cookie / sessionKey 私有代理链路；官方 WIF 只有在后续接入能够自动获取和轮换可信 assertion 的服务端 provider 后才可重新设计导入契约。
- `providerCode = anthropic` 不接受 `credentials.anthropic_version` 或 `credentials.anthropic_beta`。`anthropic-version` 是客户端请求头，缺省时由网关按协议默认 `2023-06-01` 补齐；`anthropic-beta` 只透传客户端显式 header。
- `providerCode = glm` 时只允许 `type = api_key`，且必须有 `credentials.api_key`。
- `providerCode = glm` 必须显式填写 `profile_glm_general_openai_v1`、`profile_glm_coding_openai_v1` 或 `profile_glm_coding_anthropic_v1`，分别表示通用 GLM API、GLM Coding OpenAI Chat 和 GLM Coding Anthropic。预览、确认导入和导出都按该字段 round-trip。
- 智谱官网现另列 OpenAI Response 端点 `https://open.bigmodel.cn/api/v1`；对应目标档案 `profile_glm_coding_openai_responses_v1` 已进入适配方案，但在代码、seed、模型白名单和探针完成前不属于当前可提交导入枚举。实施后必须作为独立 `providerProtocolProfileId` 接受，不能复用 Coding Chat 档案或依赖 `base_url` 猜测。
- `providerCode = glm` 且 `providerProtocolProfileId = profile_glm_coding_openai_v1` 时，账号只保存真实上游能力 `chat_json`、`chat_sse`；Responses / Codex Responses 桥接由普通账号显式 `responses -> chat_completions` 模型别名处理。
- `providerCode = glm` 且 `providerProtocolProfileId = profile_glm_coding_openai_responses_v1`（实施后）时，账号只保存经验证的 `responses_json`、`responses_sse`，默认 Base URL 为 `https://open.bigmodel.cn/api/v1`，模型必须在目录中声明 `responses`；不得静默改投 Chat 或把 bridge 当成原生 Responses。
- `providerCode = glm` 且 `providerProtocolProfileId = profile_glm_coding_anthropic_v1` 时，`credentials.supported_endpoint_modes` 只保存 `messages_json`、`messages_sse`。
- `providerCode = glm` 的通用 GLM API、GLM Coding OpenAI Chat 和 GLM Coding Anthropic 都要求 `credentials.base_url` 显式填写到对应协议档案的版本根（GLM `/api/paas/v4`、`/api/coding/paas/v4`、`/api/anthropic/v1`），不能依赖后端猜测。
- NewAPI / OpenAI-compatible 聚合入口如果只给代理根地址，例如 `https://vsllm.com`，先按来源模型名推断真实厂商；`credentials.base_url` 填该代理的 OpenAI v1 版本根（如 `https://vsllm.com/v1`）——上游 URL 为版本根 + 不含版本前缀的操作路径（`/chat/completions`），不能只填站点根地址（运行时不再自动补 `/v1`，存量裸根由迁移命令回填）。推断失败时该来源记录跳过，不再按通用 `providerCode = openai` 导入。
- `providerCode = deepseek` 时只允许 `type = api_key`，且必须有 `credentials.api_key`。DeepSeek OpenAI-compatible 必须显式填写 `providerProtocolProfileId = profile_deepseek_openai_v1`；DeepSeek Claude Code 必须显式填写 `providerProtocolProfileId = profile_deepseek_anthropic_v1`。当前不接受 DeepSeek OAuth、Refresh Token、Setup Token 或 Claude Code token；Anthropic-compatible 只通过 API Key + 协议档案导入。
- `providerCode = deepseek` 且 `providerProtocolProfileId = profile_deepseek_openai_v1` 时，`credentials.base_url` 默认建议填写 `https://api.deepseek.com/v1`（版本根）；如使用代理地址或专属部署地址，填该代理的版本根，仍必须通过 SSRF 防护和 OpenAI-compatible base URL 校验。
- `providerCode = deepseek` 且 `providerProtocolProfileId = profile_deepseek_anthropic_v1` 时，`credentials.base_url` 默认建议填写 `https://api.deepseek.com/anthropic/v1`（版本根）；使用 NewAPI 类代理时填该代理的 anthropic 版本根（含 `/v1`），例如 `https://vsllm.com/v1`。
- `providerCode = deepseek` 且 `providerProtocolProfileId = profile_deepseek_openai_v1` 时，账号可保存 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`。官方当前列出的原生 Responses 模型是 `deepseek-v4-flash`，产品预兼容 `deepseek-v4-pro`；Pro 的真实可用性仍以实际上游响应为准。Responses / Codex Responses 需要改写到 Chat 时仍由普通账号显式 `responses -> chat_completions` 模型别名处理。DeepSeek Claude Code 档案不使用这条规则。
- DeepSeek beta 能力不能只靠导入 `base_url` 猜测启用，必须由后续明确 endpoint mode / 能力开关控制。
- `providerCode = gemini` 且 `type = api_key` 时必须有 `credentials.api_key`；`type = google_oauth` 时必须有 Access Token，或 Refresh Token + 与签发令牌一致的 Client ID / Secret，并显式保存 `credentials.oauth_type = code_assist|google_one|ai_studio`。没有 Refresh Token 的 access-only 账户按静态 token 使用。Google OAuth 必须落到 `profile_gemini_native_v1beta`；Gemini OpenAI Chat 档案只接受 API Key。
- `oauth_type = code_assist|google_one` 时 `credentials.base_url` 使用 `https://cloudcode-pa.googleapis.com`、必须有 `project_id`，endpoint modes 只能是 `generate_content_json/generate_content_sse`；`code_assist` 默认 `tier_id=gcp_standard`，`google_one` 默认 `tier_id=google_one_free`。`oauth_type = ai_studio` 时使用 `https://generativelanguage.googleapis.com/v1beta`（Gemini 原生版本根）、自定义 Google OAuth client 和 native 档案 endpoint modes，默认 `tier_id=aistudio_free`。
- `providerCode = gemini` 且 `providerProtocolProfileId = profile_gemini_native_v1beta` 时不配置普通账户 OpenAI / Anthropic 跨协议模型映射；Code Assist / Google One 也不能通过导入的 endpoint modes 放开 Interactions、Count Tokens 或 Embedding。
- `providerCode = gemini` 且 `providerProtocolProfileId = profile_gemini_openai_chat_v1beta` 时，`credentials.base_url` 默认建议填写 `https://generativelanguage.googleapis.com/v1beta/openai`，`credentials.supported_endpoint_modes` 使用 `chat_json`、`chat_sse`；如需 Codex / Responses 使用 Gemini OpenAI Chat，可在普通账号中配置 `responses -> chat_completions` 模型别名。该档案不接受 Anthropic Messages 来源的账户模型别名。
- `credentials.base_url` 必须显式填写，不从供应商配置自动补值。
- `credentials.supported_endpoint_modes` 可限制协议端点能力。xAI API Key 可用 Chat / Responses，Grok OAuth 固定 Responses；Anthropic 使用 Messages / Count Tokens；Gemini native 枚举 GenerateContent、Interactions、Count Tokens、Embedding，但 Code Assist / Google One 固定只有 GenerateContent JSON / SSE。省略时按供应商、协议档案和认证模式默认值回填，不能只按协议大类放宽。
- Codex Responses 请求的客户端画像由网关自动识别；目标账号必须具备对应真实上游能力。GPT / 通用 OpenAI 原生 Responses 账号必须具备 `responses_sse`；DeepSeek `deepseek-v4-flash` 与预兼容的 `deepseek-v4-pro` 原生 Responses 账号必须具备 `responses_sse`，其他需要改写到 Chat 的显式 Chat-only DeepSeek、GLM Coding 和 Gemini OpenAI Chat 账号必须配置 `responses -> chat_completions` 模型别名并具备 `chat_sse`。
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
- 账户支持模型不在供应商模型目录中：先在「供应商」页或「我的模型」页的模型目录创建这些自定义模型并配置价格，再重新导入。
- 账号模型别名（`modelMappings`）的来源 / 目标模型不在当前供应商模型目录中：处理方式与支持模型相同，先在模型目录创建并配置价格，再重新导入。
- 账户 `connections` 不能为空：导入包必须显式提交非空 connections 数组（connectionPresetCode/baseUrl/endpointModes/enabled）。
- `healthCheckConnectionPresetCode` 不能为空：健康检查三件套必须显式提交；即使填写，指向的也必须是本账户一条 `enabled: true` 连接（否则执行阶段报 `health_check_connection_conflict`）。
- `healthCheckModel` 不能为空：健康检查三件套必须显式提交；填写后仍必须属于 `supportedModels`。
- `healthCheckEndpointMode` 不能为空：健康检查三件套必须显式提交；填写后必须属于 `healthCheckConnectionPresetCode` 指向连接的 `endpointModes`，且检查模型须在模型目录声明该形态。
