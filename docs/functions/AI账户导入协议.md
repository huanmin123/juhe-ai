# AI 账户导入协议

## 定位

AI 账户导入只支持项目自定义 JSON 协议，不直接兼容 sub2api、CPA、one-api、new-api 或其他外部格式。外部数据需要由用户自行使用 AI、脚本或表格工具整理为本协议后再导入。

当前协议：

- `type`: `juhe-ai-account-import`
- `version`: `1`
- 单次请求受系统 API JSON 请求体上限约束，当前接口按小批量导入设计。
- 单次最多导入 500 个账户、200 个代理。

## 导入流程

1. 用户在 AI 账户管理页选择目标系统账户。
2. 打开“导入账户”，粘贴 `juhe-ai-account-import` JSON。
3. 点击“解析预览”，后端校验协议、账户、代理、分组引用和批内重复。
4. 预览无错误且至少有 1 个可创建账户时，允许“确认导入”。
5. 确认导入时重新解析并写入数据，导入结果返回创建、跳过、失败明细。

导入支持这些运行选项：

- 自动创建缺失分组：默认开启；关闭后，未知 `groupName` 会阻止对应账户导入。
- 自动创建缺失代理：默认开启；关闭后，未知 `proxyRef` 会阻止对应账户导入。
- 导入时跳过重复账户：默认开启；已存在同名或同凭据账户时跳过对应账户。

## JSON 示例

```json
{
  "type": "juhe-ai-account-import",
  "version": 1,
  "metadata": {
    "source": "用户自定义",
    "generatedAt": "2026-05-22T12:00:00+08:00"
  },
  "defaults": {
    "providerCode": "openai",
    "type": "api_key",
    "status": "active",
    "baseUrl": "https://api.openai.com/v1",
    "groupName": "默认 OpenAI 分组",
    "concurrencyLimit": 3,
    "priority": 50
  },
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
      "type": "api_key",
      "groupName": "默认 OpenAI 分组",
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
      "type": "oauth",
      "groupName": "默认 OpenAI 分组",
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
| `metadata` | 否 | 来源、生成时间、备注等说明信息，仅用于人工识别。 |
| `defaults` | 否 | 账户默认值；单个账户未填写时继承。 |
| `proxies` | 否 | 代理数组；账户通过 `proxyRef` 引用代理 `ref`。 |
| `accounts` | 是 | 账户数组，至少 1 条。 |

## defaults 字段

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `providerCode` | `openai` | 当前主要使用 OpenAI 供应商。 |
| `type` | `api_key` | 账户类型，支持 `api_key`、`oauth`。 |
| `status` | `active` | 账户状态，导入协议仅支持 `active`、`disabled`。 |
| `baseUrl` | 供应商默认 Base URL | 写入 `credentials.base_url` 的默认值。 |
| `groupId` | 无 | 目标分组 ID；优先级高于 `groupName`。 |
| `groupName` | 无 | 目标分组名称；可按导入选项自动创建。 |
| `proxyRef` | 无 | 默认代理引用。 |
| `proxyProfileId` | 无 | 已存在的代理配置 ID。 |
| `concurrencyLimit` | 系统默认 | 账户并发上限，必须大于 0。 |
| `priority` | `0` | 调度优先级。 |
| `accountExpiresAt` | 无 | 账户过期时间，使用可解析时间字符串。 |
| `availabilitySchedule` | 无 | 自动启停计划；结构同账户接口，未填写表示不限制时段。 |

## accounts 字段

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `ref` | 否 | 导入预览和错误定位用，不写入数据库。 |
| `name` | 是 | 账户名称，同一系统账户、供应商下不能重复。 |
| `providerCode` | 否 | 未填时继承 `defaults.providerCode`。 |
| `type` | 否 | 未填时继承 `defaults.type`。 |
| `status` | 否 | `active` 或 `disabled`。 |
| `groupId` | 否 | 绑定已有分组 ID。 |
| `groupName` | 否 | 绑定或自动创建同名分组。 |
| `proxyRef` | 否 | 引用 `proxies[].ref` 或已有代理 ID。 |
| `proxyProfileId` | 否 | 直接引用已有代理 ID；不能和 `proxyRef` 同时填写。 |
| `concurrencyLimit` | 否 | 账户并发上限。 |
| `priority` | 否 | 调度优先级。 |
| `supportedModels` | 否 | 支持模型列表。 |
| `accountExpiresAt` | 否 | 账户过期时间。 |
| `availabilitySchedule` | 否 | 自动启停计划；`null` 表示不继承默认计划。 |
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
- `base_url` 未填时使用 `defaults.baseUrl` 或供应商默认 Base URL。
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
3. providerCode 默认 openai。
4. API Key 账号使用 type: api_key，credentials.api_key 保存密钥。
5. OAuth 账号使用 type: oauth，credentials.refresh_token / access_token / id_token 按原数据填写。
6. 如果没有 base_url，使用 https://api.openai.com/v1。
7. 如果有代理，请放到 proxies 数组，并用账号的 proxyRef 引用。
8. 不确定的信息放到 notes，不要自造凭据字段。
```
