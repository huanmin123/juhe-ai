# AI 问答会话绑定模式与动态模型目录设计

> 2026-09-27 修订：AI 问答定位为号池与账户测试工具。新建会话必须显式选择绑定模式与对象（API Key / 分组 / 账户），取消"默认专用 Key 自动绑定"与"默认会话自动选中"；分组、账户模式经网关进程内调度覆盖直达测试目标。本文取代旧的"AI 问答专用 API Key"单 Key 契约。

## 1. 目标与非目标

目标：

- 新建会话必须显式选择一种绑定模式：按 API Key、按分组、按账户；不再存在默认绑定。
- 进入 AI 问答不自动选中任何会话；没有会话时只显示"新建对话"引导，必须显式新建。
- 分组、账户模式让用户直接测试号池中的指定分组或指定账户，不被路由策略和调度间接化。
- 模型列表三种模式复用同一动态目录事实与缓存，成本不随账户数增长。
- 所有模式仍以真实 API Key 走现有 `/v1` 网关入口，鉴权、额度、限流、计费、使用记录、审计、trace 不绕过、不弱化。

非目标：

- 不新增对外 header、查询参数或请求体字段承载调度目标；调度覆盖只存在于进程内。
- 不把探针类直连上游的通道引入聊天；聊天永远不直接请求供应商。
- 不提供会话创建后更换绑定对象的接口；需要更换时新建会话。
- 不迁移历史会话：存量会话一律视为 `api_key` 模式，行为不变。

## 2. 三种绑定模式

| 模式 | `bind_mode` | 绑定对象 | 鉴权主体（Bearer Key） | 调度语义 |
| --- | --- | --- | --- | --- |
| API Key | `api_key` | 当前用户的一个已启用、未过期 API Key | 所选 Key | 现状：Key -> 路由策略 -> 分组 -> 账户 |
| 分组 | `group` | 一个启用中的分组 | 用户专用对话 Key（见第 3 节） | 候选组收敛为指定分组，组内正常调度 |
| 账户 | `account` | 一个启用中的账户 | 用户专用对话 Key（见第 3 节） | 候选账户收敛为指定账户，分组记账按该账户所属分组 |

- 分组模式校验：分组存在且 `enabled`；模型与账户候选来自该分组的可派发账户。
- 账户模式校验：账户存在且启用，并与网关账户候选解析的可用口径一致（不可调度或已禁用的账户不在可选范围）。
- 分组、账户模式下专用对话 Key 只是鉴权与计费主体：其额度、限流、使用记录归属照常生效；会话的绑定展示、模型作用域、调度目标都来自绑定对象，与该 Key 的路由策略无关。

## 3. 专用对话 Key 的新语义

- `api_keys.purpose = 'chat'` 的唯一专用 Key 保留，但不再是新建会话的默认绑定。
- 仅当新建 `group` / `account` 模式会话时，服务端幂等确保专用 Key 存在并作为该会话的鉴权主体（`EnsureChatAPIKey` 既有实现：补齐默认路由、名称"AI 对话 API Key"、唯一索引去重）。
- `api_key` 模式不触碰专用 Key，直接使用用户所选 Key。
- 专用 Key 仍禁止删除、禁止改名；停用或过期后 `group` / `account` 模式会话不能继续发送（与现状 Key 失效语义一致）。
- API Key 页面的专用 Key 标签、路由切换能力不变。

## 4. 会话创建契约

```http
POST /__aisys__/api/my-chat/conversations
{
  "bindMode": "api_key" | "group" | "account",
  "apiKeyId": "...",   // bindMode=api_key 时必填，其余模式不得传
  "groupId": "...",    // bindMode=group 时必填，其余模式不得传
  "accountId": "..."   // bindMode=account 时必填，其余模式不得传
}
```

- `bindMode` 必填；省略或非法值返回 400。旧"省略 `apiKeyId` 自动绑定专用 Key"的路径删除，不再兼容空请求体。
- 创建成功保存：`bind_mode`、对应绑定对象 ID 与名称快照、`api_key_id`（鉴权主体）。`api_key_name_snapshot` 继续保存鉴权 Key 名称。
- 响应仍返回 `defaultModel`（按新模式模型作用域取排序第一项）与完整会话 payload（含绑定模式与对象快照）。
- 会话创建后所有绑定字段不可更换；PATCH 仍只接受 `title`、`isPinned`、`defaultImageModel`。

## 5. 动态模型目录：三种模式的取数链路

```text
bind_mode = api_key : 会话 api_key_id -> Key 视图 active 分组绑定 -> ListAccountsForGroup 聚合 provider_codes
bind_mode = group   : 指定分组 -> ListAccountsForGroup(分组) 聚合 provider_codes
bind_mode = account : 指定账户 -> 该账户 provider_code（单值）-> 与 account_supported_models 取交集
        （三种模式） -> ListCachedProviderModelCatalogAsync（进程内有界 TTL/LRU 缓存）
```

- 列表排序完全复用客户端动态模型目录；第一项即新会话默认模型。
- 目录数据按 provider 缓存，列表响应成本只与 provider 数量相关：`api_key` 模式与现状相同，`group` 模式通常 1 个 provider，`account` 模式恒为 1 个。
- `account_supported_models` 为空集合时表示该账户未配置模型限制，不额外收窄目录；非空时目录与该集合取交集。
- 能力详情接口（同名模型多候选取保守交集）按相同作用域收敛。
- 不通过内部 `/v1/models` 重走网关预检，不读取发布快照；空 provider 作用域返回空列表。

## 6. 网关调度覆盖（进程内）

- 聊天执行器本就进程内直驱 `/v1` chain（无 loopback HTTP hop）；分组、账户模式经未导出的 `context` 键注入调度目标，外部 HTTP 请求无法构造该 context，不存在伪造通道。
- 应用点在鉴权与运行时装配之后、派发循环之前：
  - `group`：候选组收敛为指定分组（校验启用），账户候选按该分组解析；指定分组不可用返回明确错误。
  - `account`：候选账户收敛为指定账户，分组按账户所属分组（`BoundGroupID`）记账，与现有 `applyUsageAccountScope` 语义一致。
- 协议选择（Chat Completions / Responses）按目标作用域内账户的实际端点模式收敛，不再读 Key 视图的全体账户。
- 使用记录天然记录真实命中的 `api_key_id` / `group_id` / `account_id`；审计与 trace 与普通客户端完全一致。

## 7. 前端规则

- 进入页面不自动选中会话；空状态提供"新建对话"。仅待确认提交（页面刷新后的进行中轮次）允许自动回到对应会话。
- 删除当前会话后回到空状态，不自动选中下一项。
- 新建对话打开弹窗：绑定模式（API Key / 分组 / 账户）+ 对应对象下拉；对象未选择时创建按钮禁用；不记忆上次选择。
  - Key 下拉：当前用户可用 Key 列表（`/my-api-keys` self 域）。
  - 分组下拉：管理面分组选项（`/groups/options`）。
  - 账户下拉：管理面账户选项（`/accounts/options`，仅启用账户）。
- 模型下拉保持按需加载：首次展开才请求会话模型列表，并发去重、切会话取消，不引入前端 TTL 缓存或首屏预取。
- 会话详情显示绑定模式与对象名（对象删除后回退名称快照）。

## 8. 数据与迁移

- `chat_conversations` 新增列（PostgreSQL 与 SQLite 同契约）：
  - `bind_mode TEXT NOT NULL DEFAULT 'api_key'`（`api_key | group | account`）
  - `bind_group_id TEXT NULL`、`bind_group_name_snapshot TEXT NULL`
  - `bind_account_id TEXT NULL`、`bind_account_name_snapshot TEXT NULL`
- `api_key_id` 语义扩展：所有模式非空（鉴权主体）；`api_key` 模式为用户所选 Key，`group` / `account` 模式为专用对话 Key。
- 存量数据不加迁移回填：默认 `bind_mode='api_key'` 即表达历史行为。
- `--ensure-schema` 幂等加列（列存在性检查后 `ALTER TABLE ADD COLUMN`），不删除、不改写任何既有列。

## 9. 验收

- 新建会话省略 `bindMode` 返回 400；三种模式各自携带错误对象返回 400；引用的绑定对象（API Key / 分组 / 账户）不存在、已删除或已停用时，创建与发送统一返回 400（`chat_invalid_request`）——这是用户可恢复的输入/状态错误；服务端端口未装配、专用 Key 服务异常等真正的服务端问题保持 5xx，不与用户错误混算。
- `group` 模式会话的模型列表等于该分组可派发账户聚合的动态目录；`account` 模式等于该账户 provider 目录（与 `account_supported_models` 交集）。
- `account` 模式发送命中指定账户，使用记录的 account/group 归属正确；`group` 模式发送始终落在指定分组内。
- 外部 HTTP 请求无法指定调度目标：不带内部 context 的请求调度行为与现状完全一致。
- 进入页面与删除会话后不再自动选中任何会话；待确认提交恢复不受影响。
- 存量会话（`bind_mode='api_key'`）的发送、模型列表、详情展示行为与升级前一致。
