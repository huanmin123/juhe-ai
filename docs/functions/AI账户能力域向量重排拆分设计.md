# AI 账户能力域向量与重排拆分设计

> 状态：已定稿（2026-11-21 用户裁决）；本文是向量/重排独立能力域的行为契约权威。实施记录随交付追加到 §10。
> 上游契约：[AI账户能力域拆分计划](../plans/计划-20261011T141004000Z-AI账户能力域拆分.md)（D1–D8、三集合模型、M1/M2 迁移先例）、[供应商与上游协议面重构设计](供应商与上游协议面重构设计.md) §2.2、[对外协议面对齐new-api设计](对外协议面对齐new-api设计.md) §6.3。

## 1. 背景与裁决

2026-11-20 协议补齐批（T214）把 `/v1/embeddings`、`/v1/rerank` 落为对话域内文本 lane：`embeddings_json`、`rerank_json` 加入 chat 域锚集（15 值），管理页因此没有两个协议的账户入口，模型广场也没有独立分类。2026-11-21 用户裁决：向量与重排升级为**独立落库能力域**，全部能力面与 video/audio/image 域**完全同级**——账户菜单、模型广场分类、调度偏好占位、策略路由、观测筛选按六域补齐。

## 2. 目标与非目标

**目标**：
1. `accounts.capability_domain` 落库词表四值→六值：新增 `embedding`（向量）、`rerank`（重排）。
2. AI 账户菜单组六项（用户侧 `/my-accounts/embedding|rerank` + 管理侧 `/accounts/embedding|rerank`）。
3. 模型广场分类四类→六类：`text / embedding / rerank / image / audio / video`，向量/重排模型不再归「对话 / 编码」。
4. schema revision 28→29：CHECK 扩值 DO 块 + 数据步骤（阶段记录 ruleVersion 2 升级与存量重推导）入升级账本。
5. 推导规则 v2（`RuleVersion` 1→2），chat 域锚集 15→13。

**非目标**：
1. 调度偏好对新域仍为「无调度偏好」占位（同媒体域模式，不启用成本/速度偏好）。
2. J1 真实模型探针不含两域、无人工测试请求模板（延续 T214 边界，新域恒走连接测试）。
3. 生产发布不在本设计范围（revision 27/28/29 合并基线，须发布窗授权）。
4. Gemini native 向量（`embed_content`）不迁域：仍属对话域账户（T214 口径延续）。

## 3. 域与 lane 定义（决策 1）

`AllowedModes = DomainLane(capability_domain) ∩ PresetVocabulary(...)` 公式不变（协议面设计 §2.2）。域 lane 变更：

| 域 | lane（endpoint modes） | 变更 |
| --- | --- | --- |
| chat | chat_json、chat_sse、responses_json、responses_sse、messages_json、messages_sse、generate_content_json、generate_content_sse、interactions_json、interactions_sse、count_tokens、embed_content、message_token_counting（**13 值**） | 移出 `embeddings_json`、`rerank_json` |
| embedding | embeddings_json（**1 值**） | 新增 |
| rerank | rerank_json（**1 值**） | 新增 |
| video / audio / image | 不变（4 / 7 / 1） | — |

行为含义：`/v1/embeddings` 候选只命中 embedding 域账户；`/v1/rerank` 候选只命中 rerank 域账户；对话账户创建/编辑不再允许声明两 mode。目录 `mode` 白名单：embedding 域→`['embedding']`、rerank 域→`['rerank']`、chat→`['chat','responses']`。

## 4. 推导规则 v2（决策 2）

`RuleVersion` 1→2，`DeriveDomain` 按序命中即停：

1. 声明含 `embeddings_json` 且不含 13 值对话生成锚与媒体触发值、**且不含 `rerank_json`**（两新域互斥）→ `embedding`；
2. 声明含 `rerank_json` 且不含上述值、**且不含 `embeddings_json`** → `rerank`；
3. 其余规则照 ruleVersion=1（对话 modes→chat；video→video；audio→audio；`images_json`→image；缺省 chat），媒体分支同样**排除含任一新域 mode 的声明**（落媒体域的行不会被失败清单标记）。

**混合声明**（向量/重排与对话生成形态、媒体触发值或彼此同账户声明）：一律照 v1 落 `chat`，不自动拆域、不裁剪丢能力，进迁移失败清单人工处理（建议拆成独立账户）。后端 `DeriveDomain` 与前端 `deriveCapabilityDomain` 镜像同构，改动须双侧同步（2026-11-21 复审 A1 修正：此前前端镜像缺少互斥条件与媒体排除，已对齐）。

## 5. 域×供应商写侧矩阵（决策 5）

| 域 | 允许供应商 | 依据 |
| --- | --- | --- |
| embedding | qwen（DashScope `embeddings_json`）、OpenAI 族 gpt（OpenAI 兼容词表含 `embeddings_json`，GLM 除外） | `openAIVocabulary`、`qwenDashScopeVocabulary` |
| rerank | 仅 qwen | 仅 `qwenDashScopeVocabulary` 含 `rerank_json` |

gemini / glm / volcengine / anthropic / xai / deepseek / minimax 两域均为 false（词表不含对应 mode）。

## 6. schema revision 29（决策 3）

- **DDL 面**：`accounts.capability_domain` CHECK 四值→六值——建表内联 DDL、存量库守卫 ALTER、新「定位旧 CHECK→DROP→重建」DO 块（照 revision 27 `account-endpoint-mode-pg-check` 先例）。
- **步骤面**：注册 revision 29 步骤 `capability-domain-vector-rerank`（`RequiredBeforeStartup=true`，照 revision 28 `authz-recipient-consent-governance` 模式）：①阶段记录升级——`account_capability_domain_migration` 记录 ruleVersion 1→2、stage 保持 `m2_finalized`；②存量账户按 v2 重推导，混合声明进失败清单。⚠️ `ParseRecord` 强校验 ruleVersion（期望值=包常量），步骤须走专用"记录升级通道"（接受旧值 1 的原始 JSON、校验 stage 后写回），不得走严格 `ReadRecord`。
- **镜像**：gateway/jobs 启动形状校验期望值数组四值→六值；golden 计数（DDL 总数、juhe_business 数）与 `contracts.SchemaRevision=29` 注释同步。
- **存量迁移量**：生产未发布 revision 27/28，CHECK 为 25 值、不可能存在两 mode 声明，重推导预期 0 行；dev 库少量测试账户由步骤裁剪或列失败清单。

## 7. 测试方式与探针边界（决策 4）

- 新域恒连接测试（`TestKindForAccount`：embedding/rerank 域恒 connection，同媒体域模式）。
- 默认健康检查形态：embedding→`embeddings_json`、rerank→`rerank_json`；域内选项=域 lane 单值。
- jobs 连通性候选 SQL 域集合加入 `embedding`、`rerank`（否则新域账户探针无人认领）。
- 不进 J1、无人工测试请求模板；连通性探测规格沿用 T214（embeddings_json：OpenAI/qwen；rerank_json：qwen）。

## 8. 管理面（六域同级）

1. **菜单与路由**：四条新路由 meta `capabilityDomain: 'embedding'|'rerank'`，顺序：对话、向量、重排、视频、音频、图像；图标向量 ClusterOutlined、重排 SortAscendingOutlined。
2. **创建向导**：创建墙按「当前域允许集非空」统一过滤供应商——媒体/向量/重排域按矩阵×词表判型（rerank 域仅 qwen 可选），chat 域按对话锚集合×档案词表求交（纯媒体词表供应商如 minimax 求交为空集，必须隐藏：保留会得到接口能力播种为空、无手动勾选区、保存校验必拦的死路表单，2026-11-21 dev 实测确认）；**域默认端点模式=域 lane 单值 ∩ 词表**（embedding 域=`embeddings_json`、rerank 域=`rerank_json`，与媒体域 `images_json`/`video_create` 默认先例同构，满足新建 ⊆ 允许集）；连接测试三件套校验路径。
3. **批量编辑**：新域裁掉检查模型/检查形态、响应检查策略、请求覆盖（`!mediaDomain` 语义按"媒体+新域"重裁——新域无这些概念）。
4. **导入/导出/转移/克隆**：域过滤参数自动扩六值；克隆域锚定、批量同域断言、导入 DeriveDomain v2 推导照旧机制。
5. **模型广场**：`modelCategoryOrder` 扩六类（text、embedding、rerank、image、audio、video 顺序），标签「向量」「重排」；`categoryFromModeAlias` 两 mode 独立分类（不再归 text）；卡面标签表同步；自定义模型表单分类联动。
6. **调度偏好**：占位表六域（向量/重排=「无调度偏好」只读，不得隐藏——调度偏好计划 §默认偏好占位）。
7. **策略路由 / 观测页**：域 tab、域下拉随域枚举自动扩六项，核对哨兵值与缓存 key。

## 9. 网关派发影响

网关 `/v1` 派发不读域字段的口径**不变**（候选过滤按 endpoint modes 投影与声明集）；域收窄的效果经由"chat 账户不再能声明两 mode + 新域账户声明单 mode"达成，派发链零改动。statreads 域过滤（性能/健康/用量）自动扩六值。

## 10. 验收标准与实施记录

**验收**：
1. AI 账户菜单组六项（用户侧+管理侧），向量/重排列表按域过滤正确。
2. 向量账户创建：qwen/OpenAI 族可选；默认端点模式=`embeddings_json`（域 lane 单值）；健康检查=连接测试（`embeddings_json`）。
3. 重排账户创建：仅 qwen 可选；默认端点模式=`rerank_json`。
4. 对话账户创建/编辑不再出现 Embeddings API/Rerank API 勾选。
5. 模型广场出现「向量」「重排」分类 tab，`text-embedding-v4`、`gte-rerank-v2` 等归位，卡面标签正确。
6. revision 29 在 dev 库 `upgrade --phase=all` 演练通过；golden 测试通过。
7. `/v1/embeddings` 冒烟候选只命中 embedding 域账户。
8. 后端定向包测试 + 前端 typecheck/vitest/回归脚本通过。

**实施记录**（2026-11-21）：
1. 全链路已交付：六域枚举/lane/推导 v2（`RuleVersion=2`）、revision 29（CHECK 六值 DO 块 + `capability-domain-vector-rerank` 数据步骤入账本）、网关默认集修正（新域默认=域 lane 单值∩词表，修 §8.2 旧措辞导致的播种越界矛盾）、jobs 候选 SQL 五域、前端六域（菜单/路由/模型广场六分类/调度偏好占位/域参数化词表）。
2. dev 演练：revision 29 upgrade 全阶段收敛（2 个 2026-11-08 安全审计残留坏行经用户授权备份后删除，重跑 upgraded，postflight 18 项全 ok）；浏览器 E2E：菜单六项、向量/重排创建落库（`dev-vector-check`=embedding/`embeddings_json`、`dev-rerank-check`=rerank/`rerank_json`）、重排墙仅 qwen（模型目录唯一 `gte-rerank-v2`）、对话检查形态仅 Chat/Responses 四项、模型广场六分类。
3. E2E 发现并修正：对话域创建墙原按「不过滤」实现，纯媒体词表供应商（minimax，后端注册表仅 video+audio 域）进入对话向导后接口能力播种为空、表单无手动勾选区、保存校验必拦成死路。修正为创建墙按域允许集统一过滤（§8.2），同步 §8.2 措辞；GPT 路径第 1 步回归通过，accounts 目录 215 项 vitest + typecheck 全绿。
4. 测试账户 `dev-vector-check`（假 Key 连接失败，status=error）与 `dev-rerank-check`（pending_test）为探测数据，留存 dev 列表，可随时删除。
5. 二轮复查（用户发起）：①终审级独立复审确认 A1/A2 修复语义等价无新缺陷，B/C 级均为既有测试缺口（B1=revision 29 执行器 pg 集成测试缺失，登记 T222 发布窗前补）；②发现并修复**模型广场对话模型混入向量分类**——目录 DTO `mode` 由 `supported_endpoint_modes_json` 经 `DeriveLegacyModeAndProtocols` 派生，计数辅助 mode（`message_token_counting`/`count_tokens`，对话族附属接口）投影为旧 embedding mode 且 rank 高于 chat，claude/gemini 对话模型因此被派生成 embedding（旧四分类下归 text 展示不可见，六域独立分类后显性化）。修复：两处镜像派生（gateway `internal/providers/endpoint_modes.go` + `cmd/juhe-ai-gateway/chain_catalog.go`）将计数辅助 mode 排除出主 mode 竞争（协议投影保持全量），纯辅助行回落 embedding 保持旧行为；定向单测 + 两包回归 + dev 重启实测（claude-opus-5/gemini-3.8-flash→chat，`gemini-embedding-2`/`text-embedding-v4`/`doubao-embedding-vision`→embedding，`gte-rerank-v2`→rerank，向量分类 30→3 行全为真向量模型）。③前后端 DeriveDomain 测试互补钉值补齐（后端补 `embeddings_json+audio_speech` 与三向混合，前端补 `rerank_json+video_create` 与三向混合）。

## 11. 2026-11-21 五域声明退役修订

同日用户裁决「非对话域彻底去掉上游接口能力」（[AI账户上游接口能力设计](AI账户上游接口能力设计.md) §3）对本设计的承接：embedding / rerank 两域（连同 image / audio / video）彻底去除上游接口能力声明面——前端无配置、写侧不校验、凭据不落 `supported_endpoint_modes` 键、连接行不落 `account_endpoint_modes` 行、克隆不透传、批量编辑无此字段；本设计 §8.2 依赖的「域 lane 集 ∩ 供应商词表」声明交集仅保留 chat 域语义。`embeddings_json` / `rerank_json` 及各域 lane 自该批起语义转为**域探测形态集**（`LaneModesForDomain`）：不再参与连接组合校验（registry `AllowedEndpointModeCombos` 收缩为 chat 域组合 60），只服务健康检查 / 连通性探测的规格选择（域默认形态集 × preset 规格优先级）；两域默认检查形态落值不变（embedding→`embeddings_json`、rerank→`rerank_json`）。`/v1/embeddings`、`/v1/rerank` 候选隔离改由网关域闸门（`capability_domain_mismatch`）承接（上游接口能力设计 §4）。存量清洗由 schema revision 30 `capability-domain-declaration-retirement` 承担（含五域凭据键与连接行清理）。

**2026-11-21 dev 演练收口补充**：revision 30 清洗删键后，revision 29 v2 步骤的 Apply/Verify（`VerifyRuleVersionV2` / `UpgradeRuleVersionV2`）对「落库五域 + 凭据无键」按退役终态放行（`retiredDeclarationTerminal`，域以 capability_domain 列为权威，五域源授权实例继承源落库域），不再判 `modes_missing` / `instance_source_failed`；chat 行凭据缺键仍判失败（上游接口能力设计 §3.6）。dev 库 revision 29→30 演练复跑 postflight 19/19 全绿、`schema_state` 收敛 30。
