# BUG-0229：custom 目录行覆盖内置行后能力空缺——custom 行无能力列且两面不兜底，web_search 永不注入

> 编号原派单为 BUG-0226，因并行任务（系统指标页 jobs 健康段，`docs/bug/问题-0226-系统指标页jobs健康段不可达.md`，2026-09-28 17:59 提交）已占用 0226，顺延为 0229；代码内注释与功能文档引用已同步使用 BUG-0229。

## 基本信息

- 编号：BUG-0229
- 状态：已修复（待发布）
- 严重程度：P1
- 发现时间：2026-09-28
- 发现方式：用户反馈（生产 AI 对话 `chat_conv_ed0f35e0a639d9eb60e88f1d17a1bd5b`，2026-09-28 17:10 问天气无搜索）
- 模块：网关 / 后端（目录读取链）
- 关联计划：无
- 关联 bug：BUG-0210（前置：同一读取链内置行静态能力兜底缺失，commit 3a33c9702 只修了内置行一侧）
- 责任人：主代理派单，写入 subagent 执行

## 问题概述

- 现象：生产 AI 对话（供应商 my-chat）中，会话 owner 在 `juhe_business.custom_provider_models` 有 personal 自定义模型行 `gpt-6-sol`（active）。该模型提问实时信息明确回答无法联网，发往上游的请求无 `tools` 字段；带图输入被服务端 400；前端隐藏图片上传按钮；会话 `toolCapabilities` 恒不可用。
- 期望：custom 行覆盖内置行后，模型能力三键 `supportedTools` / `inputModalities` / `outputModalities` 继承被覆盖内置行（经静态兜底后）的值，联网搜索、图片输入、工具面板恢复。
- 实际：三键恒空，能力相关链路全部退化。
- 影响范围：见「影响面清单」。

## 复现步骤

1. `custom_provider_models` 存在与内置 `provider_model_catalog` 同名模型的行（生产为 personal `gpt-6-sol`，scope 优先级 personal=3 > built_in=1）。
2. 会话 owner 在 AI 问答选中该模型提问实时信息（生产复现：`chat_conv_ed0f35e0a639d9eb60e88f1d17a1bd5b`，2026-09-28 17:10）。
3. 回答不联网、请求无 `tools`；粘贴图片发送返回 400；输入框无上传入口。

## 环境信息

- 分支 / 版本：生产国内单机 Docker go-only（103.36.63.105）。
- 数据状态：`juhe_business.custom_provider_models` 含该 owner 的 personal 行（active）。
- 上游：api.shenwenai.com 已实测支持 `/v1/responses` + `tools:[{type:web_search}]` 并真实执行 `web_search_call`（上游能力不是瓶颈）。
- 是否稳定复现：是。

## 根因分析

- 表象：web_search 不注入、带图 400、工具面板不可用。
- 真实根因（目录读取链，证据为 Go 当前源码与 Node 归档）：
  1. custom personal 行按 scope 优先级整行替换内置行：`chainMergeCatalogItems`（`backend-go/projects/gateway/cmd/juhe-ai-gateway/chain_catalog.go:405`，优先级见 `chainCatalogScopePriority` :436）。
  2. `custom_provider_models` 表没有 `supported_tools` / `input_modalities` / `output_modalities` 三列（chat 面列清单 `chainCustomCatalogColumns`，`chain_catalog.go:110` 起无这三键；管理面 `listCustomModelRows` 的 SELECT 同样不含）；`decorateCustomCatalogRow`（`chain_catalog.go:766`）只做 source/缓存/服务等级派生，不做能力兜底。
  3. 管理面同病：`scanCustomCatalogItem` 硬编码空三键（`backend-go/projects/gateway/internal/providers/catalog.go:544-546`）。
  4. 结果：目录里该模型 `SupportedTools` 恒空 → chat 面 `supportsWebSearch=false`（`backend-go/projects/gateway/internal/chat/generation_deps.go:967`）→ `selectChatTransport` 不偏好 responses（`generation_deps.go:968`）→ 走 chat_completions → `web_search` 永不注入（上游仅 Responses 协议执行托管 web_search）。
- Node 语义取证结论：Node 归档 `toCustomCatalogItem` 同样硬编码空能力（`migration-backup/node/final-archive/backend/src/modules/model-pricing/model-catalog.service.ts:757-759`），`mergeModelCatalogItems` 同样整行替换（同文件 :367-368）——**Node 既有缺陷的延续，非 Node→Go 迁移回归**；BUG-0210（commit 3a33c9702）只补了内置行的静态兜底，没有覆盖「custom 行替换内置行」这条路径。
- 为什么会发生：custom 表设计时未承载能力列，而读取链把「行的覆盖语义」与「行的能力声明」耦合在整行替换里——行被 custom 接管后，能力也一并变成空。

### 契约裁决（口径 b）

custom(global/personal) 目录行在目录读取链上，若同 `(provider_code, model)`（按合并键：hybrid 下 provider+model，其余裸 model）存在内置目录行，则其三个空能力键以内置行（经静态兜底后）的值填充（仅填空，不覆盖非空值）；全新自定义模型（无内置对应行）保持空，不得用静态定价表的别名/前缀匹配规则回填（`internal/pricing/lookup.go` 的日期后缀剥离与前缀别名会对 `gpt-5.5-my` 这类自有命名误配）。管理面与 chat 面同源解析（保持 BUG-0210 建立的两面 parity 原则）。契约正文：`docs/functions/AI问答设计.md` 8.6「custom 目录行能力继承」。

## 修复方案

- 修改点：
  - 文档先行：`docs/functions/AI问答设计.md` 8.6 新增「custom 目录行能力继承」小节（目标、可见行为、边界），并在 web_search 注入判定条目加交叉引用。
  - 共用原语：`backend-go/projects/gateway/internal/providers/custom_capability_inherit.go` 新增 `InheritBuiltinCatalogCapabilities`（仅填空三键，输入 custom 行能力三键 + 内置行，输出回填后三键）。
  - chat 面：`chain_catalog.go` `ListProviderModelCatalog` 在 merge 之后、过滤排序与缓存写入之前回填（`chain_catalog.go:204` 调用，`chainInheritCustomCatalogCapabilities` 实现于 :455）；回填 map 键与 `chainMergeCatalogItems` 的合并键一致（hybrid 下 provider+model，其余裸 model），内置扫描结果建 map，同优先级后行胜出与 merge 决胜语义一致。
  - 管理面：`providers/catalog.go` `listProviderModelCatalog` 在 merge 后同规则回填（`catalog.go:184` 调用，`inheritCustomCatalogCapabilities` 实现于 :602），内置行数据来自同请求已加载的 `builtIn` 切片（已过 `ApplyBuiltInStaticDerivedFields` 静态兜底），无新增查询。
  - 注释同步：`chain_catalog.go` 头部修复记录与 `decorateCustomCatalogRow` / `decorateBuiltinStaticDerivedCapabilities` 注释中「custom 行不做兜底」的旧表述改为新契约。
- 行为影响：覆盖内置行的 custom 行恢复能力三键；全新自定义模型（无内置对应行）不变（保持空）；非空键不覆盖；`generationParameterCapabilities` 不在继承范围，仍按 custom 行自身 provider+model 生成。
- 发布异常处理：见「发布注意」。

## 影响面清单（修复后恢复项）

- `web_search` / `generate_image` / 诊断工具（`function_calling` 门控）永不注入 → 恢复按目录声明注入。
- 带图输入被服务端 400：`backend-go/projects/gateway/internal/chat/stream_route.go:492`（`inputModalities` 不含 image）。
- 前端隐藏上传按钮：`frontend/src/views/chat/ChatView.vue:67`（`image-input-supported` 同时看 `inputModalities` 与 responses 协议）。
- `toolCapabilities` 接口恒不可用（能力矩阵按目录能力聚合）。
- 上下文压缩协议偏好退化：`backend-go/projects/gateway/internal/chat/generation_deps.go:967`（supportsWebSearch=false → 不偏好 responses）。
- 管理面 custom 行能力恒空，且模型表能力关键词过滤永不命中：`frontend/src/views/providers/providerModelTableState.ts:76-78`（关键词对 `inputModalities`/`outputModalities`/`supportedTools` 匹配）。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 单元/链路回归 | chat 面 custom 覆盖继承 + 全新模型保持空 + 合并键语义 | `cd backend-go && go test ./projects/gateway/cmd/juhe-ai-gateway/ -run 'Catalog|StaticDerived' -count=1` | 全部通过 | ok（26s） | 通过 |
| 管理面回归 | providers 包全量（含新回填助手与原语单测） | `cd backend-go && go test ./projects/gateway/internal/providers/ -count=1` | 全部通过 | ok（12s） | 通过 |
| 两面 parity | custom 覆盖场景 chat 面与 providers.Store 真实读取链同值（含全新模型两侧保持空） | `TestChainCatalogCustomCapabilityInheritAdminFaceParity` | 三键 DeepEqual（nil 与空切片视为等价） | 通过 | 通过 |
| 继承值正确性 | 覆盖行三键与 `ApplyBuiltInStaticDerivedFields` 同键样本完全同值（含 web_search） | `TestChainCatalogCustomRowInheritsBuiltinCapabilities` | 完全同值 | 通过 | 通过 |
| 生产功能验证 | 生产会话重问实时信息、带图发送、工具面板 | 发布后按「发布注意」清缓存后人工验证 | web_search 注入、图片可发、面板可用 | 待发布后执行 | 未执行 |

## 复发记录

- 时间：无。
- 环境：—
- 现象：—
- 关联处理：—

## 下次遇到

- 先查什么：模型能力类「恒空/恒不可用」先比对该模型在目录合并里的胜出行 scope（custom 覆盖还是内置行），再看胜出行来源表有没有能力列。
- 重点看什么：BUG-0210（内置行静态兜底）与本条（custom 行继承）是同一读取链的两段；新目录来源接入时，能力三键的取值口径要在两面（chain_catalog.go / providers/catalog.go）同时落地。
- 如何避免误判：不要把「上游不支持」当第一归因——先在目录面确认声明口径（本例上游实际支持 web_search，是本地目录恒空）。

## 完成总结

- 完成时间：2026-09-28
- 结论：已修复（待发布）。custom 目录行能力继承按口径 b 落地，chat 面与管理面同源回填，测试全绿。
- 后续建议：发布后清共享目录缓存键；生产功能验证三项（联网/带图/工具面板）完成后回填本记录。

## 发布注意

- 生产 cacheDriver 为 redis 时，共享目录缓存键 `gateway:provider-model-catalog` TTL 24h 且发布重启不清缓存（同 BUG-0210 的发布注意）；发布后需清理该键（或触发一次任意目录保存），否则最长 24h 内 custom 覆盖行的旧空能力条目继续命中、修复看似无效。
