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
- `accounts` 至少 1 条；每个账户必须显式填写 `name`、`providerCode`、`type`、`status`、`credentials`，以及 `groupId` 或 `groupName`。
- 当前 `providerCode` 可填 `openai` 或 `gpt`；系统会按供应商自动匹配内部协议配置。`openai` 在供应商层表示通用 OpenAI-compatible 供应商，只支持 `api_key`；`gpt` 表示 GPT 专属供应商，支持 `api_key` 和 `oauth`。
- 不确定是否可立即调度时，`status` 填 `pending_test` 或 `disabled`，不要默认填 `active`；即使导入文件写 `active`，导入落库也会转为 `pending_test`，必须在本系统测试通过后才参与调度。
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
- 账户当前为 `pending_test` 时导出为 `status: "pending_test"`；账户当前为 `active` 且参与调度时导出为 `status: "active"`；其他运行态状态统一导出为 `status: "disabled"`。导入时 `active` 会按安全策略落成 `pending_test`，避免重新导入后直接参与调度。
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
      "clientCompatibility": "openai_standard",
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
      "clientCompatibility": "codex_responses",
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
        "account_id": "acct_xxx",
        "email": "user@example.com"
      },
      "notes": "OAuth 账号"
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
| `providerCode` | 是 | 当前支持 `openai` 和 `gpt`。`openai` 表示通用 OpenAI-compatible 供应商，`gpt` 表示 GPT 专属供应商。 |
| `clientCompatibility` | 否 | 客户端兼容模式，支持 `openai_standard` 和 `codex_responses`；省略时按供应商和账户类型默认。 |
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
| `supportedModels` | 否 | 支持模型列表。 |
| `modelMappings` | 否 | 模型映射列表，条目包含 `sourceModel`、`upstreamModel`、`enabled`。 |
| `tags` | 否 | 账户标签字符串数组；导入时按目标系统账户自动创建缺失标签并绑定到当前账户。 |
| `accountExpiresAt` | 否 | 账户过期时间。 |
| `availabilitySchedule` | 否 | 时间计划；启用时必须包含 `enabled: true`、`mode: "allow_windows"` 和 `windows`。 |
| `credentials` | 是 | 凭据对象。 |
| `notes` | 否 | 备注。 |

字段规则：

- `groupId` 和 `groupName` 同时存在时优先使用 `groupId`。
- `proxyRef` 和 `proxyProfileId` 不能同时填写。
- `concurrencyLimit` 必须是正整数；`priority` 必须是非负整数。
- `supportedModels` 只填明确支持的模型名称；不确定时省略。
- 通用 OpenAI-compatible 上游如果要支持 Codex Responses，请把 `clientCompatibility` 填为 `codex_responses`，并在 `credentials.supported_endpoint_modes` 中包含 `responses_sse`。
- `tags` 用于账户快速分类，单个账户最多 24 个标签，单个标签最长 40 个字符；空白标签会被忽略，同一账户内大小写重复标签会去重。
- `modelMappings` 的 sourceModel 是下游请求模型，upstreamModel 是该账户实际转发模型。
- `accountExpiresAt` 使用 ISO 时间字符串，例如 `2027-12-31T00:00:00.000Z`。
- `pending_test` 表示账户需要在本系统手动测试通过后才参与调度；这是新建和导入账户的推荐默认状态。

## credentials 字段

API Key 账户：

```json
{
  "api_key": "sk-xxx",
  "base_url": "https://api.openai.com/v1",
  "supported_endpoint_modes": ["chat_json", "chat_sse"]
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

规则：

- `providerCode = openai` 时只允许 `type = api_key`，且必须有 `credentials.api_key`。
- `providerCode = gpt` 且 `type = api_key` 时必须有 `credentials.api_key`。
- `providerCode = gpt` 且 `type = oauth` 时必须有 `credentials.refresh_token` 或 `credentials.access_token`。
- `credentials.base_url` 必须显式填写，不从供应商配置自动补值。
- `credentials.supported_endpoint_modes` 可限制 OpenAI v1 接口能力，枚举值为 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`；省略时通用 `openai` API Key 默认 Chat JSON/SSE，GPT API Key 默认四项全开，GPT OAuth 默认 Responses JSON/SSE。
- `clientCompatibility = codex_responses` 时必须启用 `credentials.supported_endpoint_modes` 中的 `responses_sse`。
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
