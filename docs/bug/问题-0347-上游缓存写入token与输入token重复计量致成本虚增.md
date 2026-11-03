# BUG-0347 上游 cache_write_tokens 与 input_tokens 重复计量，缓存写入成本被虚增

## 基本信息

- 编号：BUG-0347
- 状态：待修复（口径需裁决）
- 严重程度：P2
- 发现时间：2026-11-03
- 发现方式：自查（成本快照行项与 token 列交叉核对）
- 模块：网关（计价行项构造）
- 关联计划：无
- 关联 bug：无
- 责任人：网关维护者

## 问题概述

- 现象：部分成功请求的成本快照同时出现 `input` 行项与 `cache_write` 行项，且 `cache_write` 数量 ≈ `input_tokens`（误差 <1%），导致同一批 token 被按「输入价 + 缓存写入价」各计一次。
- 期望：按 [模型价格与用量统计口径](../functions/模型价格与用量统计口径.md) §3.2，缓存写入是输入的子集时，输入行项应先扣除缓存写入部分（与缓存读取的 `uncachedInputTokens = input - cacheRead` 处理同构）。
- 实际：`uncachedInputTokens` 只扣除了 `cacheReadTokens` 与图片/音频 token，**未扣除 `cacheWriteTokens`**（`internal/pricing/billing.go:499`），因此 `input` 行项按全量输入计价，`cache_write` 行项再计一次。
- 影响范围：所有上游返回 `input_tokens_details.cache_write_tokens` 且其值接近 `input_tokens` 的请求；生产近 30 天 161 条、`cacheWriteCostUsd` 合计 88.45 USD。

## 复现步骤

1. 上游（如 gpt-6.1-sol Responses 面）返回 usage：`{"input_tokens":13196,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":13193},"output_tokens":167}`。
2. 网关解析后 `cache_write_tokens=13193`、`input_tokens=13196`。
3. 计价构造 `input` 行项数量 13196（未扣 13193）、`cache_write` 行项数量 13193。
4. 成本快照：`input 13196 × $2/M + cache_write 13193 × $2.5/M`，同一批 token 计两次。

## 环境信息

- 分支 / 版本：Go 网关（`backend-go/projects/gateway`），生产国内单机 Docker 现行版本。
- 数据状态：近 30 天 161 条命中（`cache_write_tokens > input_tokens*0.9`），涉及 `gpt-6.1-sol`、`gpt-6-astra`、`gpt-6-luna`；`cacheWriteCostUsd` 合计 88.45 USD（同期网关成功流量总成本 4,041.58 USD，约 2.2%）。
- 是否稳定复现：是（上游该字段存在时必现）。

## 根因分析

- 表象：同一请求同时有 input 与 cache_write 两条行项，数量几乎相等。
- 真实根因：`internal/pricing/billing.go` 的 `buildTokenCostBreakdown` 计算未缓存输入时只减了缓存读取：
  ```go
  uncachedInputTokens := math.Max(
      nonNegative(input.InputTokens)-
          boolFloat(options.cacheReadIncludedInInput)*cacheReadTokens-
          inputImageTokens-
          inputAudioTokens,
      0)
  ```
  没有减 `cacheWriteTokens`。而 `cacheReadIncludedInInput` 为 true（openai/gemini/xai 族），说明该族约定「缓存类 token 已包含在 input 内」——缓存写入同样应被扣除。
- 为什么会发生：缓存读取的扣除逻辑先落地（`cacheReadIncludedInInput`），缓存写入（`cache_write_tokens`）是后加的兼容字段（文档 §3.2 提到「OpenAI-compatible 上游如果额外返回明确的 cache_write_tokens…」），新增行项时未同步扩展现有的「已包含在 input 内」扣除集合。
- 佐证：上游原始 usage 中 `input_tokens_details.cache_write_tokens=13193` 与 `input_tokens=13196` 并存，且 `cached_tokens=0`——说明上游把「写入缓存的 prompt token」计入 input 总额，这与 Anthropic 的 `cache_creation_input_tokens`（独立于 input）语义不同。

## 生产证据

- 样本 trace `9da21540-a1fc-4b71-b03f-00b03d818bd8`（2026-11-03 10:34，`gpt-6.1-sol`）：
  - 上游原始 usage（审计载荷 `25b2841a…`）：`{"input_tokens":13196,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":13193},"output_tokens":167,"total_tokens":13363}`。
  - 用量记录：`input_tokens=13196`、`cache_write_tokens=13193`、`output_tokens=167`、`cost_usd=0.0610445`。
  - 成本快照行项：`input 13196 × $2/M = 0.026392`、`output 167 × $10/M = 0.00167`、`cache_write 13193 × $2.5/M = 0.0329825`，合计 0.0610445。
  - 若按「缓存写入已含在 input 内」正确扣除：`input 3 × $2/M + cache_write 13193 × $2.5/M + output 167 × $10/M ≈ 0.03467`，虚增约 **76%**。
- 规模：近 30 天 161 条命中、`cacheWriteCostUsd` 88.45 USD；全量 `cache_write_tokens>0` 共 2,045 条、成本 250.13 USD（其中异常 161 条占 35%）。
- 另一形态：部分行 `input_tokens` 与 `cache_write_tokens` 完全相等（如 `usage_20261103_s08_…` 的 13196/13193），也有 `cache_write_tokens` 远小于 input 的正常形态（如 4215/18993），说明只有「接近相等」这批是重复计量。

## 修复方案（待裁决）

- **A（推荐）**：`buildTokenCostBreakdown` 的 `uncachedInputTokens` 同步扣除 `cacheWriteTokens`（与 `cacheReadTokens` 同权），并在 `hasUnpricedUsage` 判定中同步。前提：确认 `cache_write_tokens` 在 OpenAI 兼容族确实包含于 `input_tokens`（上游样本已佐证）。
- **B**：若某些上游的 `cache_write_tokens` 是**独立于** input 的（Anthropic 语义），则需按 provider 分族处理：openai/gemini/xai 族扣除，anthropic 族不扣。当前 `cacheReadIncludedInInput` 已是分族开关，可扩为 `cacheWriteIncludedInInput`。
- 裁决要点：A 改动最小但假设所有族同语义；B 更严谨但需逐族确认上游口径。建议先按 A 修 openai 族，并对 anthropic 族显式标注为不扣（其 `cache_creation_input_tokens` 不包含在 `input_tokens` 内）。

- 行为影响：修复后这些请求的成本下降（回到真实单价），`success_cost_usd` 与额度消耗同步下降；历史行不回填。
- 发布异常处理：无 schema 变更。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 生产取证 | 上游 usage 与成本行项对照 | 审计载荷解析 + 快照 | 确认重复计量 | 确认（13196 vs 13193） | 已执行 |
| 生产取证 | 规模统计 | 快照 lineItems 扫描 | 量化影响 | 161 条 / 88.45 USD / 2.2% | 已执行 |
| 单元验证 | 缓存写入扣除后成本正确 | 待实施后补 | 通过 | 待实施 | 未执行 |
| 生产验证 | 发布后同类请求成本下降且无负值 | 发布后抽样 | 符合预期 | 待发布 | 未执行 |

## 复发记录

- 时间：
- 环境：
- 现象：
- 关联处理：

## 下次遇到

- 先查什么：成本异常先看 `cost_breakdown_snapshot_json` 的 `lineItems`，逐项核对数量与上游 usage 字段是否一一对应、是否有重复覆盖。
- 重点看什么：`cache_read` / `cache_write` 的数量与 `input` 行项数量的关系。若 `input` 数量没有扣除缓存类 token，且上游把缓存 token 计入 input，就是重复计量。
- 如何避免误判：不同厂商的缓存语义不同——Anthropic 的 `cache_creation_input_tokens` 独立于 `input_tokens`，OpenAI 兼容族的 `input_tokens_details.cache_write_tokens` 包含在 `input_tokens` 内；不能一套扣除规则套所有厂商。

## 完成总结

- 完成时间：待修复
- 结论：待裁决修复方向（建议 A + anthropic 族显式豁免）
- 后续建议：修复后核对同期 `success_cost_usd` 变化，评估历史 161 行是否需要重算（需授权）
