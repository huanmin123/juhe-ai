# BUG-0342 账号模型别名 Responses→Responses 同族映射恒 400「请求参数无效」

## 基本信息

- 编号：BUG-0342
- 状态：已修复（待发布与生产验证）
- 严重程度：P1
- 发现时间：2026-11-02
- 发现方式：用户反馈（生产截图）
- 模块：后端（`gateway/internal/accounts` 映射写入校验）+ 文档
- 关联计划：无
- 关联 bug：[BUG-0337](问题-0337-入口映射空preset隐式连接默认随B1重建断裂致编辑保存400.md)（同款 localize 吞错放大器）

## 问题概述

- 现象：生产编辑 AI 账户，在「账号模型别名」把上游协议选成 Responses（或新增别名默认落在 Responses 上游）后保存，弹出多个「请求参数无效」，保存不成功。
- 期望：账户连接与目标模型都具备 Responses 能力时，`responses -> responses` 同族改名映射可以保存。
- 实际：写侧校验返回 400，`kernel.localize` 把英文 `ValidationError` 改写为「请求参数无效」，原文从响应侧不可见。
- 影响范围：所有需要 Responses 模型别名的账户。生产库 `juhe_business.account_entry_mappings` 现有 144 行 `responses -> responses` 存量映射（53 个账户，其中 4 个 active），这些账户的任何一次映射集合变更（新增/删除/改名一条 chat 别名）都会被整批拒绝——因为映射是整集合替换写。

## 复现步骤

1. 打开任意 gpt 账户编辑弹窗（例如 `acc_d53626b7e75dc015` / `api.zeekai.cc-v44hst`），连接含 `responses_json`/`responses_sse`。
2. 在「账号模型别名」新增一行：客户端协议 Responses、上游协议 Responses、模型改名（如 `gpt-5.6-sol` → `gpt-6-luna`）。
3. 保存。

## 环境信息

- 分支 / 版本：master，发布基线 `v2026.10.31-4`
- 数据状态：生产库实测（只读）
- 是否稳定复现：是（100%）

## 根因分析

- 表象：前端提示「请求参数无效」，无任何字段级说明，用户无法判断是模型、连接还是协议组合的问题。
- 真实根因：两层实现不一致。
  - 写侧矩阵 `backend-go/projects/gateway/internal/accounts/entry_mappings.go` 的 `entryMappingPairTargetModes` 只有设计 §5.2 的 17 个 pair，**没有 `responses -> responses`**，命中即返回 `mapping_target_unsupported: family pair responses->responses is not in the §5.2 matrix`。
  - 但前端 `accountModelMappingProtocolMatrix.ts` 把它当同族改名放行（`requiresNativeResponses`），新增别名的默认值也会落到 Responses→Responses（`preferredUpstreamFamilies`）；运行时 `gatewayopenai.IsAdmissibleModelMapping` 的 `source == upstream` 分支同样放行。
  - 设计文档 §5.2 正文写着「responses→responses 是 OpenAI Responses 原生直连……不通过映射 bridge 合成」，但没把它列入 17 对表格，实现按表格写就漏了。两处文档口径本身互相矛盾。
- 为什么会发生：映射写侧矩阵与前端矩阵、运行时判定、设计正文四方演化不同步；写侧漏项后，唯一暴露方式是保存失败，而失败文案又被 localize 吞掉。

## 修复方案

- 修改点：
  1. `entry_mappings.go` 的 `entryMappingPairTargetModes` 增加 `"responses\x00responses": {"responses_json", "responses_sse"}`（§5.2 #18），矩阵 17 → 18 对、28 → 30 个请求形态。
  2. 同文件全部写侧拒绝文案改中文并点名冲突对象（协议族 / 连接 preset / 模型名）：含 CJK 的文案不会被 `LocalizeSystemErrorMessage` 改写，用户可直接归因（BUG-0337 已确立的口径）。
  3. 文档同步：[供应商与上游协议面重构设计](../functions/供应商与上游协议面重构设计.md) §5.2 表格新增 #18、§5.3 补 Responses 同族改名行、新增 §5.4「写侧拒绝文案的可归因要求」、验收计数 17/28 → 18/30；[自定义模型与模型映射设计](../functions/自定义模型与模型映射设计.md) 矩阵行改为「支持（同族改名）」；[AI账户模型映射选择规则](../functions/AI账户模型映射选择规则.md) 与 [functions/README](../functions/README.md) 计数同步。
- 行为影响：`responses -> responses` 由必失败转为可保存（受 §5.3 联动约束：连接与目标模型都必须有 `responses_json` 或 `responses_sse`）。矩阵外组合仍拒绝，只是文案变为中文。运行时零改动——`IsAdmissibleModelMapping` 本就放行同族映射，无需发布新逻辑才能生效。
- 发布异常处理：纯代码 + 文档变更，无 schema/env 变更；回滚仅回退提交。生产已有 144 行存量映射可正常读写，不需要数据迁移。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 复现 | 修复前同载荷保存 | 临时 PATCH 探针（真实 dev PG，跑完即删） | 400「请求参数无效」 | 400 `{"message":"请求参数无效"}`；store 层原始错误 `mapping_target_unsupported: family pair responses->responses is not in the §5.2 matrix` | 已确认 |
| 反证 | 去掉 responses 行、只留 chat 行 | 同上 | 200 | 200，`changedFields:[supportedModels modelMappings]` | 已确认 |
| 存量行 | 已存在的 responses 行原样回存 | 直插存量行后 PATCH | 200（diff 为空，不触发校验） | 200；但新增任意 chat 行即整批 400 | 已确认 |
| 功能验证 | 修复后同一份混合载荷 | `TestResponsesIdentityMappingCoexistsWithChatAliases` | 200 且两行落库 | PASS | 通过 |
| 功能验证 | Responses-only 账户（唯一可用方向） | `TestResponsesIdentityMappingSavesWithResponsesOnlyConnection` | 200 且落库 | PASS | 通过 |
| 边界验证 | 矩阵外组合仍拒绝且中文可归因 | `TestResponsesIdentityMappingStillRejectsUnsupportedPairs` | 拒绝且文案含中文与协议名 | PASS | 通过 |
| 联动验证 | 连接/模型缺 Responses mode 时拒绝 | `TestResponsesIdentityMappingRequiresResponsesModes` | 拒绝且中文点名 | PASS | 通过 |
| 构建验证 | gateway 全量 build / vet | `go build ./...`、`go vet ./internal/accounts/` | 通过 | 通过 | 通过 |
| 回归验证 | accounts 包全量 | `JUHE_AI_TEST_EXTERNAL=1 go test ./internal/accounts -count=1 -timeout 40m` | 全绿 | 见完成总结 | 待填 |

## 复发记录

- 时间：2026-11-02 14:09:25 / 14:09:25 / 14:09:25 / 14:12:52（生产 gateway 日志，同一账户 `acc_d53626b7e75dc015` 连续 4 次 PATCH 400，与用户截图一致）
- 环境：生产 103.36.63.105，发布基线 `v2026.10.31-4`
- 现象：PATCH `/__aisys__/api/my-accounts/acc_d53626b7e75dc015` 返回 400，无字段级提示
- 关联处理：本条修复

## 下次遇到

- 先查什么：管理面 400 且文案是「请求参数无效」时，先确认是不是 localize 改写——中文原文不会被动，英文一律被吞。直接对 store 层调用或看 `writeError` 前的 `ValidationError.Message` 能拿到原文。
- 重点看什么：`account_entry_mappings` 的 family pair 是否落在 `entryMappingPairTargetModes`；前端下拉是否放行了写侧没有的 pair。两侧矩阵必须逐项对表。
- 如何避免误判：不要按前端「保存成功」推断落库成功；也不要因为「账户详情能回显」就认为写路径正常（回显读的是已落库的行，与本次 PATCH 是否被拒无关）。

## 完成总结

- 完成时间：2026-11-02
- 结论：写侧 §5.2 矩阵漏 `responses -> responses` 同族改名项，与前端 / 运行时 / 设计正文四方不一致；补项 + 中文可归因文案后本地全绿。
- 后续建议：把「前端可选项集合」与「写侧矩阵」的一致性做成编译期或测试期断言（例如前端矩阵导出的 pair 列表与 Go 侧 `entryMappingPairTargetModes` 对表），避免下次再靠用户报障发现。该建议登记在待办事项。
