# AI 账户导入协议

## 定位

AI 账户导入只支持项目自定义 JSON 协议，不直接兼容 sub2api、CPA、one-api、new-api 或其他外部格式。外部数据需要由用户自行使用 AI、脚本或表格工具整理为本协议后再导入。

当前协议：

- `type`: `juhe-ai-account-import`
- `version`: `1`
- 单次请求受系统 API `256KB` JSON 请求体上限约束，当前接口按小批量导入设计。
- 单次最多导入 50 个账户、20 个代理，避免 DB service 在一次管理请求内长时间同步解析和校验大数组。

## 导入流程

1. 用户在 AI 账户管理页选择目标系统账户。
2. 打开“导入账户”，粘贴 `juhe-ai-account-import` JSON。
3. 点击“解析预览”，后端校验协议、账户、代理、分组引用和批内重复。
4. 预览无错误且至少有 1 个可创建账户时，允许“确认导入”。
5. 确认导入时重新解析并写入数据，导入结果返回创建、跳过、失败明细。

导入支持这些运行选项：

- 自动创建缺失分组：默认开启；关闭后，未知 `groupName` 会阻止对应账户导入。
- 自动创建缺失代理：默认开启；关闭后，未知 `proxyRef` 会阻止对应账户导入。
- 导入时跳过重复账户：默认开启；同一用户下已存在同名账户，或导入批内出现同名账户时跳过对应账户。

## 导出流程

AI 账户管理页支持导出 JSON 文件，导出结果直接使用本协议：

- 导出文件顶层仍为 `type: "juhe-ai-account-import"`、`version: 1`。
- 单次最多导出 50 个自有 AI 账户，和单次导入账户上限一致。
- 顶部“导出 JSON”按当前账户列表筛选条件导出，最多处理前 50 条匹配结果；勾选账户后，批量工具栏“导出 JSON”只导出当前勾选账户。
- 导出只包含当前管理员有权查看凭据和编辑的自有账户；授权账户实例不导出。
- 如果账户绑定了可用代理，导出文件会同时写入 `proxies` 并通过账户 `proxyRef` 引用，便于再次导入时自动创建或复用代理。
- 账户当前为 `active` 且参与调度时导出为 `status: "active"`；其他运行态状态统一导出为 `status: "disabled"`，避免重新导入后直接参与调度。
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
      "name": "OpenAI API Key 账号 1",
      "providerCode": "openai",
      "type": "api_key",
      "status": "active",
      "groupName": "默认 OpenAI 分组",
      "concurrencyLimit": 3,
      "priority": 50,
      "proxyRef": "proxy-hk-1",
      "credentials": {
        "api_key": "sk-xxx",
        "base_url": "https://api.openai.com/v1"
      },
      "notes": "API Key 账号"
    },
    {
      "ref": "openai-oauth-001",
      "name": "OpenAI OAuth 账号 1",
      "providerCode": "openai",
      "type": "oauth",
      "status": "active",
      "groupName": "默认 OpenAI 分组",
      "concurrencyLimit": 3,
      "priority": 50,
      "credentials": {
        "refresh_token": "refresh-token-xxx",
        "access_token": "access-token-xxx",
        "id_token": "id-token-xxx",
        "base_url": "https://api.openai.com/v1",
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
| `providerCode` | 是 | 当前支持 `openai`。 |
| `type` | 是 | `api_key` 或 `oauth`。 |
| `status` | 是 | `active` 或 `disabled`。 |
| `groupId` | 二选一 | 绑定已有分组 ID；优先级高于 `groupName`。 |
| `groupName` | 二选一 | 绑定或自动创建同名分组。 |
| `proxyRef` | 否 | 引用 `proxies[].ref` 或已有代理 ID。 |
| `proxyProfileId` | 否 | 直接引用已有代理 ID；不能和 `proxyRef` 同时填写。 |
| `concurrencyLimit` | 否 | 账户并发上限。 |
| `priority` | 否 | 调度优先级。 |
| `supportedModels` | 否 | 支持模型列表。 |
| `accountExpiresAt` | 否 | 账户过期时间。 |
| `availabilitySchedule` | 否 | 自动启停计划；启用时必须包含 `enabled: true`、`mode: "allow_windows"` 和 `windows`。 |
| `credentials` | 是 | 凭据对象。 |
| `notes` | 否 | 备注。 |

## credentials 字段

API Key 账户：

```json
{
  "api_key": "sk-xxx",
  "base_url": "https://api.openai.com/v1"
}
```

OAuth 账户：

```json
{
  "refresh_token": "refresh-token-xxx",
  "access_token": "access-token-xxx",
  "id_token": "id-token-xxx",
  "base_url": "https://api.openai.com/v1",
  "account_id": "acct_xxx",
  "email": "user@example.com"
}
```

规则：

- `api_key` 账户必须有 `credentials.api_key`。
- `oauth` 账户必须有 `credentials.refresh_token` 或 `credentials.access_token`。
- `credentials.base_url` 必须显式填写，不从供应商配置自动补值。
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

## AI 转换提示词

```text
请把我提供的账号数据转换为 juhe-ai-account-import v1 JSON。

要求：
1. 只输出合法 JSON，不要输出解释。
2. 顶层 type 固定为 juhe-ai-account-import，version 固定为 1。
3. 每个账户必须显式填写 providerCode、type、status，以及 groupName 或 groupId。
4. API Key 账号使用 type: api_key，credentials.api_key 和 credentials.base_url 必须显式填写。
5. OAuth 账号使用 type: oauth，credentials.refresh_token / access_token / id_token 按原数据填写，并显式填写 credentials.base_url。
6. 不要补写来源数据里不存在的凭据字段，也不要把字段改成 camelCase。
7. 如果有代理，请放到 proxies 数组，并用账号的 proxyRef 引用。
8. 不确定的信息放到 notes，不要自造凭据字段。
```
