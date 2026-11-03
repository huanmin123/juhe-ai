# BUG-0344 跨供应商账户计价按账户 vendor 查目录，个人无价行遮蔽内置有价行致免费用量

## 基本信息

- 编号：BUG-0344
- 状态：待修复（口径需裁决）
- 严重程度：P1
- 发现时间：2026-11-03
- 发现方式：自查（生产用量记录成本字段横向体检）
- 模块：网关（同步计价目录查找）
- 关联计划：无
- 关联 bug：无
- 责任人：网关维护者

## 问题概述

- 现象：`gpt` 供应商账户承接 `deepseek-v4.1-flash` 等其它供应商模型时，成功请求的 `cost_usd` 恒为 `NULL`（不计费），而同模型由 `deepseek` 账户承接时正常计费。
- 期望：按 [模型价格与用量统计口径](../functions/模型价格与用量统计口径.md) §2.2「计价身份按目标 `models.vendor_code` 选择计费驱动」，应命中 `deepseek` 内置有价行（输入 0.28571429 / 输出 1.14285714 USD/1M）。
- 实际：计价目录按**账户 vendor**（`gpt`）查找；`gpt` 目录下同名模型只有个人（`personal`）无价行，命中后整单无价 → `cost_usd` 保持 NULL。
- 影响范围：仅影响跨供应商接单（账户 vendor ≠ 模型真实 vendor）的用量计价；同 vendor 流量不受影响。

## 复现步骤

1. 某系统账户下创建 `gpt` 供应商账户，绑定个人模型行 `deepseek-v4.1-flash`（`vendor_code='gpt'`、`scope='personal'`、价格列全空）。
2. 该账户配置入口映射 `deepseek-v4.1-flash → deepseek-v4.1-flash-1017` 并参与调度。
3. 经该账户发起 chat 请求并成功。
4. 查用量记录：`provider_code='gpt'`、`pricing_model='deepseek-v4.1-flash'`、`input_tokens>0`、`cost_usd IS NULL`。

## 环境信息

- 分支 / 版本：Go 网关（`backend-go/projects/gateway`），生产国内单机 Docker 现行版本。
- 数据状态：生产全时段 4,879 条成功行命中该形态（2026-10-22 ~ 2026-11-03），涉及 8 个账户、1 个 API Key（`key_1780640534323_d535c2c5`「统一入口」，`quota_limits_json` 为空 = 无额度上限）。
- 浏览器 / 系统 / Node 版本：不适用（纯后端计价路径）。
- 是否稳定复现：是（该形态 100% 命中）。

## 根因分析

- 表象：`cost_usd` 恒 NULL，`cost_breakdown_snapshot_json` 只有 `{"thinkingTokens":N,"multiplier":1,"serviceTierPricingSource":"unknown"}`，无单价与行项。
- 真实根因：计价目录查找以账户 vendor 为键，且目录合并让 personal 行覆盖同名 builtin 行——
  - `cmd/juhe-ai-gateway/chain_usage.go:497` 的 `resolveModelVendor` 分支只在**账户 vendor 为空**时触发（`generic_http` 专用）；账户 vendor 非空时直接使用账户 vendor 查目录。
  - `cmd/juhe-ai-gateway/chain_pricing.go` 的 `findItem` 以 `(providerCode=账户vendor, systemAccountID, model)` 调 `ListCachedProviderModelCatalogAsync`。
  - `cmd/juhe-ai-gateway/chain_catalog.go:435` 的 `chainMergeCatalogItems` 按 `chainCatalogScopePriority` 合并同名行，`personal(3) > global(2) > builtin(1)`；`gpt` 目录下 `deepseek-v4.1-flash` 只有 personal 行（无价），无 builtin 行可覆盖。
  - `internal/pricing/billing.go` 的 `buildTokenCostBreakdown` 在 `hasAnyRate(rates.PriceSet)` 为 false 时返回 nil → `EstimateCost` 返回 nil → `cost_usd` 保持 NULL（符合「目录未命中不写 0」契约，但此处是**命中了一条空价行**）。
- 为什么会发生：文档 §2.2 要求「按目标 `models.vendor_code` 选择计费驱动」，实现把该规则只落在 vendor 为空的 `generic_http` 账户上；账户 vendor 非空但承接了其它 vendor 的模型时，没有回落到目标模型行的真实 vendor。

### 生产证据

- 形态规模（2026-10-22 ~ 2026-11-03）：成功行 `cost_usd IS NULL` 且 `input_tokens+output_tokens>0` 共 **4,879 条**，输入 **718.38M** token、输出 **3.77M** token。
- 按同模型内置目录价折算应计成本约 **209.57 USD**（`deepseek-v4.1-flash` 209.48、`grok-4.6` 0.06、`glm-5.3-flash` 0.02、其余 <0.01）；同期网关成功流量总成本 4,034.30 USD，**漏计约 5.2%**。
- 归属账户（均属 `sysacc_1780629399485_5c62baf2`）：`acc_1790252484968_c33055d4`（3,414 行 / 139.34 USD）、`acc_3aa8b897629cdcb0`（786 行 / 43.20）、`acc_96fd8e31ff6aae1f`（451 行 / 19.13）、`acc_e3ea7ae3b9b9bb68`（148 行 / 4.53）、`acc_caa0e180d60ec8c3`（43 行 / 2.29）、`acc_365a5e8addca5d32`（25 行 / 0.98）。
- 绑定事实：上述账户的 `account_supported_models` 指向 `mdl_44ed8e2a979d3be6d1d7`（`vendor_code='gpt'`、`scope='personal'`、`system_account_id='sysacc_1780629399485_5c62baf2'`、价格列全空、`status='active'`）。
- 同模型内置行：`provider_model_deepseek_deepseek_v4_1_flash_9a65d894c4d1`（`vendor_code='deepseek'`、`scope='builtin'`、0.28571429 / 1.14285714）。

## 修复方案（待裁决，三个候选）

- **A（推荐，与文档一致）**：计价身份改为「目标模型行的真实 vendor」——`resolveModelVendor` 从 `generic_http` 专用改为所有账户通用：账户 vendor 非空但目标模型行解析出唯一真实 vendor 且与账户 vendor 不同时，按真实 vendor 计价；解析不唯一保持现状（NULL，不猜测）。改动点：`chain_usage.go` 的 `resolveModelVendor` 触发条件 + 单测覆盖跨 vendor 场景。
- **B**：目录合并时禁止 personal 空价行覆盖同名的其它 scope 有价行（价格维度的独立优先级），改动点 `chainMergeCatalogItems`；风险是改变「个人模型覆盖全局/内置」的既有语义。
- **C**：账户绑定模型时校验价格完整性（无价行不得绑定为支持模型），改动点 `accounts` 写侧校验；会阻断现有 76 条无价 active personal 行的存量绑定。

裁决要点：A 与文档现文一致、改动面最小、不影响 personal 覆盖语义；B/C 会改变配置面契约。建议 A，并同批补一条「跨 vendor 同名 personal 行的计价归属」文档口径。

- 行为影响：修复后跨 vendor 流量按真实模型 vendor 计价，`cost_usd` 从 NULL 变为实际值；历史行不回填（不伪造）。
- 发布异常处理：无 schema 变更；发布后新记录即生效。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 生产取证 | 零成本形态规模与应计成本 | 见上文「生产证据」SQL 口径 | 量化影响 | 4,879 行 / 209.57 USD / 5.2% | 已执行 |
| 生产取证 | 目录合并与绑定事实 | 查 `models` / `account_supported_models` | 确认 personal 无价行遮蔽 | 确认（`mdl_44ed8e2a979d3be6d1d7` 无价、gpt 目录无同名 builtin） | 已执行 |
| 单元验证 | 跨 vendor 计价命中真实 vendor 行 | 待实施后补 | 通过 | 待实施 | 未执行 |
| 生产验证 | 发布后跨 vendor 成功行 `cost_usd` 非 NULL | 发布后抽样同形态 | 命中 deepseek 内置价 | 待发布 | 未执行 |

## 复发记录

- 时间：
- 环境：
- 现象：
- 关联处理：

## 下次遇到

- 先查什么：`cost_usd IS NULL` 先分三类——(1) 目录未命中（模型名不在目录）、(2) 命中空价行（personal/global 无价行遮蔽）、(3) 计量维度缺失（无 token/无 duration）。三类的快照形态不同：未命中是空快照、空价行是 `{"thinkingTokens":N,"multiplier":1,"serviceTierPricingSource":"unknown"}`、维度缺失是零 token。
- 重点看什么：`provider_code`（账户 vendor）与 `pricing_model` 的真实 vendor 是否一致；不一致即为跨 vendor 接单，必须核对计价目录查找键。
- 如何避免误判：不要以「有 `pricing_model` 就说明计价链路通」推断已计费——`ResolvePricingModel` 只解析模型名，价格是否为空由 `EstimateCost` 的 `hasAnyRate` 独立判定。

## 完成总结

- 完成时间：待修复
- 结论：待裁决修复方向（建议 A）
- 后续建议：修复后评估历史 4,879 行是否需要按新口径重算（当前口径不回填、不伪造，需用户另行授权）
