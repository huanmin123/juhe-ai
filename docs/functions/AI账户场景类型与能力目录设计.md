# AI 账户场景类型与能力目录设计

- 状态：实施中（2026-11-12 B1 底座 + B7 向量/重排/安全场景已落地；B8 Realtime 待做）
- 关联计划：[PLAN-20261111T154901000Z](../plans/计划-20261111T154901000Z-非对话账户内核统一与探针补齐.md)
- 复审记录：[deep-dive-review-非对话账户内核统一与探针补齐-20261112](../plans/deep-dive-review-非对话账户内核统一与探针补齐-20261112.md)
- 裁决：2026-11-12 用户 D13——不做向后兼容；非对话账户上线统一删除，只保留对话账户，删除后按新场景重建。

## 1. 目标

把“账户分类”和“菜单位置”解耦：

- 一个一级菜单「AI 账户」（管理面 `/accounts`、个人面 `/my-accounts`）；
- 创建账户时选「场景类型」；一个账户只属于一个场景；
- 列表按场景类型筛选，列展示场景类型；
- 以后新增场景只加注册表行，不再加菜单。

非目标：不重写 `/v1` 主链；不为每个场景复制账户系统；不做旧非对话账户迁移。

## 2. 场景类型

`backend-go/shared/platform/scenario` 是唯一注册表。当前已开放：

| 场景类型 | 名称 | 调度类 | 人工测试 | 生命周期（B1 现状） |
| --- | --- | --- | --- | --- |
| `chat` | 对话 | conversation | 模型 + 协议 | `pending_test_then_probe` |
| `video` | 视频 | stateless | connectivity | `pending_test_then_probe` |
| `audio` | 音频 | stateless | connectivity | `pending_test_then_probe` |
| `image` | 图像 | stateless | connectivity | `pending_test_then_probe` |
| `embedding` | 向量 | stateless | connectivity | `pending_test_then_probe` |
| `rerank` | 重排 | stateless | connectivity | `pending_test_then_probe` |
| `moderation` | 安全 | stateless | connectivity | `pending_test_then_probe` |

`embedding` / `rerank` / `moderation` 已随 B7 加入注册表；`realtime` 仍在代码中保留类型常量但未注册，B8 只追加 Profile 与 provider 矩阵，不改菜单/表单分支。

## 3. 数据与写侧

- `accounts.capability_domain` 沿用历史列名，语义为场景类型；D13 起**不再带四值 CHECK**，由写侧 `scenario.Valid` 校验。
- `capability_domain` 创建后不可原地修改；要换场景就新建账户。
- 写入口统一按场景校验，包括创建/PATCH/导入/克隆/公开 API/批量编辑/OAuth 托管建户/授权实例置备。
- `health_check_endpoint_mode` 仍保留 DB CHECK；B7 已增补 `rerank_json` / `moderation_json`（27 值）。
- 上线 cutover：`maintenance purge-non-chat-accounts` 删除 `capability_domain <> 'chat'` 的账户（含授权实例、运行态、历史 usage/audit/stats）；不可逆，回滚只能恢复备份。旧二进制不再受支持。

## 4. 能力目录接口

```text
GET /__aisys__/api/accounts/capability-catalog          # 管理面，RequireAdmin
GET /__aisys__/api/my-accounts/capability-catalog       # 个人面，RequireSession
```

返回：

```json
{
  "scenarios": [
    {
      "scenarioType": "image",
      "label": "图像",
      "dispatchClass": "stateless",
      "providers": [
        {
          "providerCode": "xai",
          "profiles": [
            {
              "profileId": "profile_xai_openai_v1",
              "connectionPreset": "xai_openai_v1",
              "accountTypes": ["api_key"],
              "allowedEndpointModes": ["images_json"],
              "probeModes": ["images_json"],
              "testPolicy": "connectivity",
              "statusPolicy": "pending_test_then_probe"
            }
          ]
        }
      ]
    }
  ]
}
```

目录由 `contracts.AllowedConnectorCombos()` / `AllowedEndpointModeCombos()` + `scenario` 注册表派生，只读、不查库、不含凭据。**B1.3 已落地**：前端 `AccountsView` 打开时拉取管理面/个人面 `capability-catalog`，`accountCapabilityDomains.ts` 的账户类型与允许 modes 优先使用目录，目录不可用时才回退本地常量矩阵（测试/离线 fallback）；本地矩阵保留为兜底，不再作为正常运行时的第二事实源。

## 5. 前端

- `/accounts` 与 `/my-accounts` 是唯一路由；旧 `/accounts/{chat|video|audio|image}` 与 `/my-accounts/{...}` 不做兼容。
- 页面顶部「场景类型」筛选：全部 / 对话 / 视频 / 音频 / 图像 / 向量 / 重排 / 安全；query `?scenario=<type>` 是唯一来源。
- 列表新增「场景」列；所有场景共用同一列集，不再按域隐藏优先级/时间计划。
- 创建弹窗当前沿用页面选中的场景；全部场景时默认 chat。场景选择已移入创建向导第一步，可在创建时切换。

## 6. 验收

- 只有一个一级「AI 账户」菜单，管理面/个人面各一个入口；
- 创建时可选场景类型，列表可按场景筛选并显示场景列；
- 现有 chat/video/audio/image 与 B7 向量/重排/安全账户全部可见可建；
- `capability_domain` 无 CHECK，gateway/jobs 启动门禁要求旧 CHECK 已删除；
- `purge-non-chat-accounts` dry-run/apply 门禁与 manifest 漂移拒绝可用；
- 旧非对话账户不迁移，按 D13 删除重建。

## 7. 向量 / 重排 / 安全工具场景（B7）

### 7.1 对外契约

| 场景 | 客户端端点 | 请求形态 | 响应形态 |
| --- | --- | --- | --- |
| 向量 `embedding` | `POST /v1/embeddings` | OpenAI Embeddings：`{model,input}`；`input` 支持字符串或字符串数组 | `{object,data:[{object,index,embedding}],model,usage}` 原样透传 |
| 重排 `rerank` | `POST /v1/rerank` | Cohere/Jina 兼容：`{model,query,documents,top_n?,return_documents?}`；`documents` 支持字符串数组或 `{text}` 对象数组 | `{results:[{index,relevance_score,document?}],model?,usage?}` |
| 安全 `moderation` | `POST /v1/moderations` | OpenAI Moderations：`{model,input}`；`input` 支持字符串或数组 | OpenAI Moderations 响应原样透传 |

- 三个端点都在网关根注册（`/embeddings`、`/rerank`、`/moderations`，含 `/v1` 前缀和子路径），由 `/v1` 主链统一认证、权限、并发、代理、错误策略与审计。
- 请求 lane 统一为 `utility`（`gatewayproto.LaneUtility`），调度域为 `stateless`（`DispatchDomainStateless`，历史值 `stateless_media` 已在 B7 收敛为 `stateless`）；不产生会话亲和、组亲和、Key 亲和，不进入热质量/缓存率/速度优先/流式重试/恢复等待，不派发请求失败健康检查。
- endpoint family：`embed_content` / `rerank` / `moderation`；模型映射只允许同族改名（B7 新增 §5.2 #8-#10 三对），不会落到 `chat_completions` 误命中对话映射。
- API Key 安全策略出站脱敏按 `tool` 族扫描文本输入（embedding input、rerank query/documents、moderation input），不注入系统提示（工具请求没有 system 位置，响应不回显输入）。

### 7.2 首批供应商与不支持原因

| 供应商 | 向量 | 重排 | 安全 | 说明 |
| --- | --- | --- | --- | --- |
| gpt | ✅ `/v1/embeddings` | ❌ | ✅ `/v1/moderations` | OpenAI 无官方 rerank 端点 |
| qwen | ✅ `/compatible-mode/v1/embeddings` | ✅ DashScope `text-rerank` | ❌ | DashScope 无 moderations 端点；重排由网关把外部兼容报文翻译为 `{model,input:{query,documents},parameters}` |
| gemini | ❌ 暂缓 | ❌ | ❌ | 官方 `embedContent` 需要 Gemini 原生协议桥；本批未交付 adapter，注册表不加入组合，避免造出必然失败的账户。后续随 Gemini embedding adapter 单独立项。 |
| anthropic | ❌ | ❌ | ❌ | 官方无 embedding/rerank/moderation 端点 |
| xai | ❌ | ❌ | ❌ | 官方无 embedding/rerank/moderation 端点 |
| deepseek | ❌ | ❌ | ❌ | 本批未确认可用的官方工具端点，不放开 |
| glm | ❌ | ❌ | ❌ | 本批未确认可用的官方工具端点，不放开 |
| minimax | ❌ | ❌ | ❌ | 无对应工具端点 |
| volcengine | ❌ | ❌ | ❌ | Ark 多模态向量面与 OpenAI `/embeddings` 形态不一致，本批不做厂商私有 adapter |

不支持不是探针缺失：只有已实现上游 adapter、写侧校验与安全探测三者齐备的组合才加入连接注册表；没有官方端点的厂商不硬造，也不提供“先放开再修”。

### 7.3 账户、探测与生命周期

- 一个工具账户只属于一个场景、一条 Base URL/凭据；不与对话或媒体账户混用。
- 创建后统一 `pending_test`，J1 首次激活探测通过后转 `active`；周期检查、冷却恢复、锁死、特供、时间计划、并发、优先级与其它场景同一套内核。
- 工具域没有用户可配置的“上游接口能力开关”：创建/编辑时 `supported_endpoint_modes` 恒等于该场景允许集全量（各只有一个 mode）；`health_check_endpoint_mode` 默认 `embed_content` / `rerank_json` / `moderation_json`。
- J1 安全探测：POST 空输入到对应端点，2xx 或上游参数校验拒绝（400/422）视为 Key 可达；401/403 为凭据失败；5xx/传输错误按既有分类。探测不产生真实向量/重排/审核结果（上游拒绝空输入；若上游接受空输入，也只是一次极小结果）。
- 不支持能力（与其它非对话场景一致）：会话亲和、缓存率排序、热质量、速度优先、同账户流式重放、对话响应检查、客户端画像、GPT 服务等级/思考级别、余额查询（供应商无接口时不显示）。

### 7.4 模型目录、计费与前端

- 模型目录 `supported_api_protocols` 新增 `rerank` / `moderation`，沿用 `embed_content`；目录 mode 词表新增 `rerank` / `moderation`（`ProviderCatalogModeValues` 八值），自定义模型 UI mode 新增 `embedding` / `rerank` / `moderation`（gpt：向量/安全；qwen：向量/重排）。
- 计费沿用模型目录输入 token 单价：embedding 与 moderation 按输入 token（OpenAI moderation 目录价为 0）；qwen rerank 的 `usage.total_tokens` 在响应归一为一层补 `input_tokens`，避免被当成全零 usage 丢弃。没有独立“搜索单元”价格的模型不进入目录，不静默套用对话 output token 口径。
- 前端「AI 账户」单菜单场景筛选与创建选项自动包含三个场景；模型选择器按 `DOMAIN_MODEL_MODES` 只显示本场景目录模型；测试入口只做连通性测试，不选模型/协议；`/v1/models` 账户并集沿用账户支持模型装载，工具账户模型对客户端可发现。

### 7.5 B7 验收补充

- 三个场景可独立创建账户并完成 `pending_test → active`；
- `/v1/embeddings`、`/v1/rerank`、`/v1/moderations` 走 `stateless` 调度，不产生会话亲和；
- qwen 重排请求/响应翻译正确，gpt/qwen 向量与 gpt 安全透传正确；
- 工具账户登录后只显示本场景模型与连通性测试入口；
- 对话/媒体场景既有行为零回归；无官方端点的厂商在创建供应商矩阵中不出现。
