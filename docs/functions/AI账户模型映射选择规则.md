# AI 账户模型映射选择规则

> **状态覆盖（2026-10-17）。** 本文的“普通账户 / Hybrid 账户”分支是重构前实现基线。目标版本统一使用 [供应商与上游协议面重构设计](供应商与上游协议面重构设计.md) 的 `account_entry_mappings`、`account_connections` 与其 §5.2 固定矩阵；本文旧的 Hybrid 特判不能继续作为新代码依据。
> **退役标注（2026-11-07）。** ① Hybrid（混合供应商）账户已随 2026-11-05 通用连接器退役一并退役，本文所有 Hybrid 分支均已双重过期，仅作历史基线保留。② 跨协议模型映射（含 `Responses -> Chat Completions`）已随 2026-11-07 用户裁决整体退役：映射协议强制同族（唯一例外 Gemini `stream_generate_content → generate_content` 归一），跨协议转换映射的 rank 1 机制整体删除，协议等价映射（同族改名或 Gemini 归一）与直接支持同权 rank 0。现行矩阵为 10 个协议等价 pair / 16 个请求形态（B7 增补 embedding/rerank/moderation 同族）。
> 状态：重构前实现基线（历史文档，两级退役标注后不再描述现行行为）；现行契约以 [供应商与上游协议面重构设计](供应商与上游协议面重构设计.md) §5 为准。
> 变更记录（2026-10-24，方案B，用户裁决）：同族改名映射（映射行 source 与 target/upstream endpoint family 相同）在网关调度排序中升为与直接支持同权（rank 0），跨协议转换映射（两族不同）保留在直接支持之后（rank 1）。§2.4 与 §5 已同步。（2026-11-07：rank 1 随跨协议映射退役删除。）
> 更新时间：2026-07-27。
> 本文统一 AI 账户单个编辑、批量编辑、导入保存和网关运行时的模型映射语义。协议转换方向曾以 [协议桥接框架设计](协议桥接框架设计.md) 为准（该框架的跨协议方向已随 2026-11-07 退役）；模型目录与价格事实仍以 [自定义模型与模型映射设计](自定义模型与模型映射设计.md) 为准。

## 1. 问题定义

模型映射描述一条精确的一跳规则：

```text
客户端请求协议 + 客户端模型名
  -> 真实上游协议 + 真实上游模型
```

现有实现曾同时存在以下不同口径：

- 单账户编辑左侧加载协议级全局模型池，普通账户保存时却按当前供应商目录校验。
- `Responses -> Chat Completions` 通过右侧协议反向扩大左侧模型候选。
- 批量编辑没有完整复用单账户编辑的模型与协议过滤。
- 网关先按 `supportedModels` 标记直接匹配，再识别当前账户已显式配置的精确映射，导致候选排序分类与最终驱动实际执行的映射不一致。

这些差异会让页面展示可选但保存失败的模型，也会让用户难以区分客户端别名和真实上游能力。

## 2. 核心语义

### 2.1 左侧是客户端匹配键

`sourceModel` 是客户端请求体里的模型名，不代表当前账户真实支持该模型，也不代表上游具备该模型的协议能力。

- 普通供应商账户只允许选择当前供应商模型目录内的模型名，表达当前供应商内的客户端别名。（2026-11-07 起 source 模型候选放宽为跨供应商、按所选协议过滤；协议/目录校验由前端选择器与导入提示承担。）
- （已退役）混合供应商账户允许按左侧来源协议从全局协议模型池选择模型名，表达跨供应商、跨协议入口。Hybrid 已随 2026-11-05 退役。
- 左侧候选只由账户类型、当前供应商和左侧来源协议决定，不读取右侧目标协议或右侧目标模型。

普通账户不按左侧协议过滤当前供应商模型名的原因曾是 OpenAI v1 普通账户需要显式表达 `Responses -> Chat Completions`（DeepSeek、GLM、Gemini OpenAI Chat 等真实上游模型通常只声明 Chat 能力，仍作为 Responses 客户端别名的来源模型）。**该需求已随 2026-11-07 跨协议映射退役消失**：映射协议强制同族，左侧候选按所选协议过滤。

### 2.2 右侧是真实上游能力

`upstreamModel` 是最终发送给当前账户真实 Base URL 的模型名。

右侧模型候选固定为：

```text
账户 supportedModels ∩ 支持右侧 upstreamEndpointFamily 的模型
```

后端保存必须再次执行相同校验。右侧模型不能通过自由输入绕过模型目录和账户支持模型边界。

### 2.3 协议矩阵只约束协议

来源协议和目标协议的组合由普通账户矩阵或混合供应商矩阵决定。协议矩阵可以：

- 限制来源协议下拉。
- 限制目标协议下拉。
- 根据右侧目标协议检查账户上游接口能力。

协议矩阵不得改变左侧或右侧另一端的模型目录范围。

### 2.4 显式映射优先识别

对于单个账户，如果请求精确命中启用的 `sourceModel + sourceEndpointFamily` 映射，即使 `sourceModel` 同时存在于该账户 `supportedModels`，该账户仍应按“映射匹配”分类，最终执行该映射。

不同账户之间的排序（2026-10-24 方案B 修订，2026-11-07 收敛）：

```text
直接支持账户 + 协议等价映射账户（同族改名或 Gemini stream 归一，rank 0，同权）
```

同族改名映射（映射行 `source_endpoint_family` 与 `target_endpoint_family` 相同，仅模型名改写）与 Gemini 同族归一映射（`stream_generate_content → generate_content`）是管理员声明的客户端别名，与直接支持账户**同权 rank 0** 参与调度排序。跨协议转换映射（两族不同）的 rank 1 机制已随 2026-11-07 退役整体删除：矩阵不允许保存跨族行，运行时不存在 rank 1 候选。

判定口径：协议等价按 `(source = target) OR (source = 'stream_generate_content' AND target = 'generate_content')` 判定（运行时 `ResolvedModelMapping.UpstreamEndpointFamily`）；恒等映射（同名同族）仍按既有规则拒绝（不作为映射命中），同名请求继续走直接匹配路径。

模型命中方式（本节 rank 口径）只是网关派发排序的第一维；完整维度顺序（模型命中方式 → 降级备用 → 超级优先 → `priority` → 质量分）与特权阶梯的作用边界见 [核心功能设计](核心功能设计.md)「AI 账户管理」超级优先适用范围条目，此处不重复。

## 3. 供应商规则

> 2026-11-07 退役标注：下表所有 `Responses -> Chat`（显式 bridge）方向均已退役；现行允许方向只有同族改名 + Gemini `stream_generate_content → generate_content` 归一（B7 后 10 个协议等价 pair，含 embedding/rerank/moderation 同族）。Hybrid 行已随 2026-11-05 退役，“通用 OpenAI-compatible”档案已随 2026-11-05 供应商退役消失。下表仅作历史基线保留。

| 供应商 / 档案 | 左侧模型范围 | 允许的协议方向 |
| --- | --- | --- |
| GPT、通用 OpenAI-compatible（已退役）、xAI OpenAI v1 | 当前供应商目录 | Chat -> Chat；Responses -> Responses；（已退役）Responses -> Chat |
| DeepSeek OpenAI v1 | DeepSeek 目录 | Chat -> Chat；Responses -> Responses；（已退役）Responses -> Chat |
| GLM OpenAI Chat | GLM 目录 | Chat -> Chat；（已随 2026-11-07 退役）Responses -> Chat 显式 bridge |
| GLM OpenAI Responses（方案目标，未落地） | 通过 `responses` 能力白名单的 GLM 目录 | Responses -> Responses；不允许跨供应商/跨 profile 原生换号 |
| Gemini OpenAI Chat | Gemini 目录 | Chat -> Chat；（已随 2026-11-07 退役）Responses -> Chat；目标只能是 Chat |
| Anthropic、DeepSeek Anthropic、GLM Anthropic | 当前供应商目录 | Messages -> Messages |
| Gemini native | Gemini 目录 | GenerateContent -> GenerateContent；StreamGenerateContent -> GenerateContent（同族归一，现行） |
| openai 协议档案（媒体映射，M4b） | 当前供应商目录的媒体行（协议 token `video` / `audio_speech`） | Video Generation -> Video Generation；TTS -> TTS（同族模型名改写；账户需含 `video_create` / `audio_speech` 端点模式） |
| Hybrid（已随 2026-11-05 退役） | 左侧协议对应的全局协议模型池 | 历史截面：按混合供应商白名单转到 Chat、Messages 或 GenerateContent；媒体族同族改写；不合成 Responses 上游 |

（历史说明）通用 `openai` 供应商的“当前供应商目录”曾是 OpenAI-compatible 聚合目录；该供应商已随 2026-11-05 退役，专用供应商不能引用其他供应商目录模型，映射 source 候选改为跨供应商、按协议过滤。

## 4. 页面规则

### 4.1 单账户编辑

- 区域名称继续使用“账号模型别名”。
- 左侧字段文案使用“客户端协议”“客户端模型”；2026-11-07 起两个协议下拉合并为单协议选择（协议自动决定 source/target 两字段，仍按现有 API 契约持久化）。
- 右侧字段文案使用“上游协议”“上游模型”。
- 普通账户左侧曾读取当前供应商完整模型目录；2026-11-07 起左侧候选改为跨供应商模型目录并按所选协议过滤。
- （已退役）Hybrid 左侧按客户端协议加载 OpenAI、Anthropic 或 Gemini 全局协议模型池。
- 右侧只显示账户支持模型中支持所选上游协议的模型。
- 改变上游协议不得清空或改变左侧模型；异步目录刷新也不得静默清空已选值。
- 模型目录中不存在的历史值允许保留显示，但保存时由后端按当前完整模型拒绝。

### 4.2 批量编辑

- 只允许同供应商、同协议档案账户批量修改模型映射。
- 普通账户和 Hybrid（已退役）使用与单账户编辑相同的左侧模型策略；2026-11-07 起批量同样使用单协议下拉与跨供应商 source 选择。
- 右侧使用批量最终支持模型交集，并按上游协议过滤。
- 不维护第二套独立选择规则。

## 5. 保存与运行时校验

保存时固定校验（2026-11-07 收敛后口径，Hybrid 相关条目随 Hybrid 退役失效）：

1. `sourceModel`、`sourceEndpointFamily`、`upstreamModel`、`upstreamEndpointFamily` 非空。
2. 同一 `sourceModel + sourceEndpointFamily` 只能存在一条映射。
3. 协议方向命中现行矩阵（10 个协议等价 pair）；跨族组合返回中文 400「客户端协议与上游协议必须一致」。
4. 启用映射的右侧协议具备对应账户上游接口能力；停用映射可以保留。
5. 普通账户左侧模型存在于当前供应商目录（2026-11-07 起放宽为跨供应商、按所选协议过滤，协议校验由前端选择器与导入提示承担）；（已退役）Hybrid 左侧模型存在于来源协议全局模型池。
6. 右侧模型存在于当前供应商目录（或 Hybrid 目标协议池，已退役）。
7. 右侧模型存在于账户最终 `supportedModels` 且支持目标协议。
8. 右侧模型声明支持目标上游协议。

运行时固定顺序（2026-11-07 收敛后）：

1. 对每个账户先检查精确启用映射。
2. 映射存在且右侧模型仍属于账户支持模型时，将该账户分类为映射匹配；协议等价映射（同族改名或 Gemini `stream_generate_content → generate_content` 归一）记 rank 0；跨协议转换映射的 rank 1 已随退役删除，不存在 rank 1 候选，判定口径见 §2.4。
3. 没有映射时再检查请求模型是否由账户直接支持（rank 0）。
4. 候选汇总时协议等价映射与直接匹配账户同秩同权参与调度排序。
5. 驱动按同一精确映射改写模型和上游协议。

## 6. 非目标

- 不允许普通供应商账户跨供应商选择左侧模型。（2026-11-07 起放宽：source 模型候选跨供应商，但协议必须一致；`upstreamModel` 仍限当前账户供应商目录与支持模型。）
- 不把模型映射迁移到 API Key、路由策略或分组。
- 不允许映射右侧自由输入未入模型目录的真实上游模型。
- 不新增自动猜测协议或相似模型回退。
- 不改变当前协议桥接白名单。（2026-11-07 起跨协议白名单仅剩同族 + Gemini 归一，其余方向随退役删除。）
- 不新增数据库字段或历史运行时兼容分支。

## 7. 验收标准

> 2026-11-07 退役标注：涉及 `Responses -> Chat Completions` 与 Hybrid 的验收项随退役作废，对应行为改为“保存 400 / 无候选”。

- 普通 DeepSeek OpenAI 账户左侧只展示 DeepSeek 模型，不展示 GPT、GLM 或 Gemini 模型。（2026-11-07 起放宽为跨供应商候选按协议过滤。）
- （已随 2026-11-07 退役）DeepSeek / GLM / Gemini OpenAI Chat 账户配置 `Responses -> Chat Completions`：现保存返回 400。
- 修改右侧上游协议不会改变左侧模型候选。
- （已随 2026-11-05 退役）Hybrid 切换客户端协议时按对应全局协议池切换左侧模型。
- 右侧模型始终来自账户支持模型并符合目标协议。
- 单账户编辑、批量编辑、前端保存和后端保存使用同一规则。
- （已随 2026-11-07 退役）同模型名的 `Responses -> Chat Completions` 显式映射不再可保存，也不存在网关映射分类。
