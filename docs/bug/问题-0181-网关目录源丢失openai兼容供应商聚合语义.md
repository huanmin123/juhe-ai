# BUG-0181 网关目录源丢失 openai 兼容供应商聚合语义

## 基本信息

- 编号：BUG-0181
- 状态：已修复
- 严重程度：P1
- 发现时间：2026-09-20
- 发现方式：用户反馈（AI 对话模型下拉为空）与源码链路核对
- 模块：网关 / 运行时缓存 / AI 对话
- 关联计划：无
- 关联 bug：BUG-0121（同症状不同根因：0121 是读退场快照，本条是聚合语义缺失）
- 责任人：待定

## 问题概述

- 现象：AI 对话 API Key 绑定「默认 OpenAI 兼容路由」后，AI 对话模型下拉为空（「暂无数据」）。
- 期望：OpenAI 兼容供应商的模型目录是「openai 协议子供应商 + 自己」的聚合（Node `modelCatalogSourceProviderCodesAsync` 语义），AI 对话与认证 `/v1/models` 应展示该聚合目录内、且被分组账户（含模型映射）可达的模型。
- 实际：网关运行时缓存目录源 `chainCatalogSource.ListProviderModelCatalog` 只按单码查询 `provider_code = 'openai'`，丢失源扩展；openai 兼容在内置目录 `provider_model_catalog` 天然没有自身行，聚合结果恒为空数组，AI 对话与 `/v1/models` 对 openai/hybrid 分组稳定返回空目录。
- 影响范围：仅 Go 网关侧运行时缓存目录读取（AI 对话模型列表、认证 `/v1/models`、路由索引、定价估算取数链路）。管理面（供应商页面、账户映射校验）走 `internal/providers` 已移植的聚合读，不受影响，因此「管理页看得到目录、AI 对话为空」形成不一致。

## 复现步骤

1. 系统账户绑定 OpenAI 兼容分组，分组内配置 openai 兼容账户并设置模型映射（如 `glm-5.3` → 上游 `deepseek-v4.1-flash`）。
2. AI 对话 API Key 绑定默认 OpenAI 兼容路由，新建会话展开模型下拉。
3. 下拉为空；同账户同映射在账户编辑页可正常选择。

## 环境信息

- 分支 / 版本：master（Go 三项目迁移后）
- 数据状态：本地 SQLite（`.local/dev/data/business.sqlite3`），`provider_model_catalog` 无 `openai` 行、`custom_provider_models` 0 行、账户映射指向 glm 目录模型
- 是否稳定复现：是（结构性，非数据相关）

## 根因分析

- 表象：会话模型接口返回空数组。
- 真实根因：Node `model-catalog.service.ts` 的 `buildProviderModelCatalogAsync` 先经 `modelCatalogSourceProviderCodesAsync` 做源扩展（openai → openai 协议子供应商 + 自己；hybrid → openai/anthropic/gemini 三协议子供应商），再按扩展码集联合读取并按 scope 优先级合并。Go 迁移把该服务拆成两处：`internal/providers/catalog.go` 完整移植了扩展，而网关链 `cmd/juhe-ai-gateway/chain_catalog.go` 只移植了单码 SQL 查询，源扩展、合并、`isSupportedCatalogModel`/可计价过滤与排序全部丢失。AI 对话与 `/v1/models` 恰好只依赖网关链这条。
- 为什么会发生：迁移时以「built-in + custom 两张表的单码查询」理解了 `listProviderModelCatalog`，未追到目录源扩展语义；旧测试 `TestChainCatalogSourceListsProviderModels` 用 `provider_model_catalog` 里手工插入的 `openai` 行锚定了错误行为（生产中该表根本不会有 openai 行），使缺陷被「验证」通过。

## 修复方案

- 修改点：`cmd/juhe-ai-gateway/chain_catalog.go` `ListProviderModelCatalog` 补回 Node 语义：
  - `sourceProviderCodes`：openai 兼容目标扩展为 openai/v1 协议子供应商（`providers` + `provider_protocol_profiles` 联查，上限 50）+ 自己；hybrid 目标扩展为三协议子供应商；其余供应商保持自身单码。
  - 内置目录查询按扩展码集 `IN` 查询，openai 兼容目标剔除自身（无内置目录）；custom 目录查询覆盖全部源码。
  - 补回按 model 的 scope 优先级合并（hybrid 保留 provider 身份）、`isSupportedCatalogModel` 过滤、非 `includeInactive` 的 active 过滤、非 `includeUnpriced` 的可计价过滤与发布日期排序（模型比较近似 `localeCompare` 的大小写不敏感序）。
- 兼容性：单码供应商（gpt/glm/…）源扩展退化为 `[self]`，行为不变；原先丢失的可计价过滤恢复后与 Node `/v1/models`「可见、启用、目录可见且可计价」契约一致。
- 测试修正：锚定错误语义的种子行从 `provider_model_catalog`（openai 行）改种 `custom_provider_models`（openai 自身目录的真实来源）或改挂 gpt 子供应商 + openai/v1 协议档案（走扩展链路）；`TestChainCatalogSourceListsProviderModels` 重写为聚合语义回归锚点（子供应商内置行进入聚合、openai 自身内置行被剔除）；chain fixture 补真实 DDL 的 `providers` / `provider_protocol_profiles` 表与 custom 表长上下文列。

## 验证记录

- `go build ./...`、`go vet ./cmd/juhe-ai-gateway/` 通过。
- gateway 全项目 `go test ./... -count=1`：除 `cmd/juhe-ai-gateway/acceptance` 的 `TestFullchainStage2OpenItems` 外全部通过；该验收测试在未含本修复的干净基线上同样失败（git stash 对照验证，预存问题，与本修复无关）。
- 本地真实库（`.local/dev/data/business.sqlite3`，只读副本查询）验证：源扩展解析出子供应商 deepseek/gemini/glm/gpt/xai（+hybrid），聚合后 active + 目录可见 + 可计价模型 100 个，包含用户映射入口模型 `glm-5.3` 与 `glm-5.3-flash`。
- 运行中实例尚未重启加载新二进制，浏览器端 AI 对话下拉验证待用户重启 dev 后端后确认。

## 下次遇到

- 对比「管理面目录」与「网关目录」两条读取路径的事实来源，不要只看表里有没有行：同名服务在 Node 里可能带源扩展，迁移拆包时最容易只搬 SQL 不搬扩展。
- 手工往生产不会有行的表里插测试行来锚定行为，等于把当前实现错误固化为契约；种子应模拟真实数据形状。
