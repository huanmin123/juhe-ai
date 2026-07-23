# AI 问答专用 API Key 与动态模型目录设计

## 1. 目标

- AI 问答使用每个系统账户唯一的专用 API Key，不再借用某条 GPT 默认路由下的默认 API Key。
- 专用 Key 首次创建时绑定当前用户的 GPT 默认普通路由，之后允许用户在 API Key 页面切换到自己的任意启用策略路由。
- AI 问答模型列表与公开 `/v1/models` 使用同一动态目录事实：按 Key 的路由策略全部 active 分组绑定汇总供应商，再聚合当前用户可见、启用、目录可见且可计价的模型。
- 新会话默认选择动态模型列表第一项；后续会话恢复用户最近一次实际使用的模型。

## 2. Key 身份与生命周期

- `api_keys.purpose` 表达 Key 的产品用途，当前取值为 `general | chat`，默认 `general`。
- 每个 `system_account_id` 最多存在一个 `purpose = chat` 的 Key；唯一性由数据库部分唯一索引保证，不能用名称、描述或路由反推。
- 新系统账户创建默认资源时同时创建 AI 问答专用 Key。
- 已有系统账户在首次创建 AI 问答会话时幂等补齐专用 Key；补齐只创建缺失资源，不覆盖已有专用 Key 的路由、状态、名称、额度或时间计划。
- 专用 Key 默认名称为“AI 对话 API Key”，默认绑定 GPT 默认普通路由，`is_default = 0`。它不是某条路由的默认 Key，因此允许更换策略路由。
- 专用 Key 在 API Key 列表显示“AI 对话”用途标签，并禁止删除；允许编辑、启停、刷新密钥、调整额度和时间计划。停用或过期后，历史会话仍可读，但新建会话和继续发送应明确失败。

## 3. 会话绑定

- `POST /my-chat/conversations` 未显式传入 `apiKeyId` 时，只使用当前用户的 `purpose = chat` Key。
- 兼容已有会话：会话继续固定使用创建时保存的 `api_key_id`，不因专用 Key 后续切换路由而改绑其他 Key；专用 Key 自身修改路由后，绑定该 Key 的会话自然使用新路由。
- 不再通过“默认 Key + GPT 分组”查询决定 AI 问答身份。

## 4. 动态模型目录

模型列表链路固定为：

```text
会话 api_key_id
  -> 校验 Key 与路由策略
  -> 收集全部 active 分组绑定的 provider_code
  -> listClientModelCatalogAsync(systemAccountId, providerCodes)
  -> ChatModelListOption[]
```

- 列表排序完全复用客户端动态模型目录；第一项就是新会话默认模型。
- 空 provider 绑定必须返回空列表，不回退公开全量目录。
- 模型能力详情按相同 provider 集合读取当前供应商目录，并对同名模型取保守交集；不读取 `chat_list:*` 或 `chat_model:*` 发布快照。
- Chat 模块不通过 loopback HTTP 再请求 `/v1/models`，避免重复鉴权、限流和网络往返；只复用同一目录服务。

## 5. 前端规则

- 创建接口返回 `defaultModel`，新会话无需先展开下拉即可显示模型。
- 如果创建响应因并发目录变化没有默认模型，首次展开下拉后仍用返回列表第一项回填。
- 下拉为空时保留明确空态，不伪造模型；接口错误使用现有中文错误提示。
- `lastModel` 优先于 `defaultModel`，只要它仍存在于本次动态列表；失效时回落到列表第一项。
- 当前模型支持思考级别或服务等级时，对应控件默认选择能力列表第一项；不支持时不显示该控件。

## 6. 数据与迁移

- SQLite 当前 schema 增加 `purpose TEXT NOT NULL DEFAULT 'general'` 和合法值约束。
- PostgreSQL 使用新的 Goose 前向迁移增加字段、约束和 `purpose = 'chat'` 的账户级唯一索引。
- 历史 Key 全部保持 `general`；不在 SQL migration 中生成密钥，也不根据名称猜测历史 Key。
- 旧发布快照表可暂留给其他未迁移调用方，但 AI 问答运行路径必须移除对其读取依赖。

## 7. 验收

- 新用户和已有用户都能得到唯一 AI 对话专用 Key，重复补齐不产生第二条。
- 专用 Key 默认绑定 GPT 路由，API Key 页面可改到其他启用路由，并显示“AI 对话”标签。
- 新建会话的模型列表等于该专用 Key 对外 `/v1/models` 的动态作用域，列表非空时默认选第一项。
- 用户发送过其他模型后重新进入会话，恢复 `lastModel`；该模型失效时回落首项。
- 删除接口拒绝专用 Key，普通 Key 行为不变。
