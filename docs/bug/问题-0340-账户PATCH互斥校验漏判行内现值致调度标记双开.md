# BUG-0340 账户 PATCH 互斥校验漏判行内现值致超级优先 / 降级备用双开

## 基本信息

- 编号：BUG-0340
- 状态：已修复待验证（2026-10-31；已随 v2026.10.31 发布，页面验证待 owner 实操确认）
- 严重程度：P2
- 发现时间：2026-10-31（管理页面实操）
- 发现方式：用户反馈
- 模块：后端 / Go / AI 账户 / 管理接口 / 前端 / 账户菜单
- 关联：无

## 现象

用户先给某 AI 账户开启「超级优先」，之后仍能在同一账户的「更多」菜单里开启「降级备用」并成功保存；账户列表状态列同时展示「超级优先」和「降级备用」两个标签。契约（[核心功能设计](../functions/核心功能设计.md)「AI 账户管理」降级备用条目）要求两者不能同时开启。

## 根因

1. **通用 PATCH 的互斥校验只比请求体，不并入库内现值**：`Store.Patch`
   （`backend-go/projects/gateway/internal/accounts/patch.go`）对
   `superPriorityEnabled` / `fallbackEnabled` 各自内嵌校验「本次翻转开启时，请求体里另一字段是否也显式为 true」。请求只带单个字段时（列表菜单「超级优先 / 降级备用」的 PATCH 通道正是 `{fallbackEnabled: true}` 单字段载荷），另一字段为 nil，校验恒通过——行内已开的另一标记完全没参与判定，双开落库。
2. **同域写入面均已按终值校验，唯独此入口漏了**：创建（`write.go`，从零起步只需比双输入）、授权实例专用接口（`m11_authorized_dispatch.go:478-488`，行内现值并入后判终值）、批量更新 / 导入（`accountstransfer/batch.go:1204-1220`，终值校验 + 让位归一化）均正确；编辑弹窗走单选 `privilege` 映射、保存时显式双发布尔，同样安全。漏判面收敛在通用 PATCH。
3. **前端菜单两个入口独立渲染、提交前不互查**：`accountRules.ts pushDispatchFlagItems` 按 `canEnable* || 已开` 独立 push 两个菜单项，`useAccountMenuActions.ts` 对应分支只校验状态可用面，不检查另一标记；后端漏判因此从页面直达落库。
4. **存量测试固化了双开行为**：`w13a_patch_arms_test.go TestW13APatchValidationArms` 在"单开超级优先 → 200"后断言"单开降级 `{fallbackEnabled:true}` 应 200"且 `super_priority_enabled + fallback_enabled = 2`「两个开关都应落库」——正是漏判路径被写成了期望行为，本次一并按契约修正（单开降级 400、行值只保留超级优先）。

## 修复

- `backend-go/projects/gateway/internal/accounts/patch.go`：互斥校验改为终值判定——请求未携带的字段并入行内现值后再判，双真即拒绝（与 `m11_authorized_dispatch` 同款语义）；原两处内嵌校验删除（被终值校验覆盖）。历史双开行在取消其一（终值单真）时放行，保留自愈通道。
- `frontend/src/views/accounts/accountRules.ts` `pushDispatchFlagItems`：新设入口只在另一标记未开启时显示；已开标记保留「取消」入口，历史双开数据可经菜单自愈。
- `patch_super_fallback_mutex_test.go`（新增）：四个真实 PG 回归用例——行内已开降级备用、请求单开超级优先 → 拒绝且行值不变；行内已开超级优先、请求单开降级备用 → 拒绝且行值不变；双显式真 → 拒绝；历史双开行取消其一 → 放行。
- `w13a_patch_arms_test.go`：修正固化双开行为的存量断言（见根因 4），后续 `expectedConfigRevision` 序列同步减一。

## 验证

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 红验证 | 校验停用时用例必须失败 | `JUHE_AI_TEST_EXTERNAL=1 go test ./projects/gateway/internal/accounts -run 'TestPatch(SuperPriorityRejected\|FallbackRejected\|BothFlagsExplicitTrue\|DirtyBothOnRow)' -count=1`（临时停用终值校验） | 前 3 例失败（行内已开 A、请求单开 B 与双显式真均 `got <nil>` 写入成功，复现用户现象），自愈例通过 | 与预期一致 | 通过 |
| 回归验证 | 恢复校验后用例全过 | 同上（恢复校验） | 4 例全过 | 4 例全过 | 通过 |
| 存量修正 | W13A 校验臂按新契约修正后通过 | `JUHE_AI_TEST_EXTERNAL=1 go test ./projects/gateway/internal/accounts -run 'TestW13APatchValidationArms' -count=1` | 全过 | 通过（修正前该例在单开降级臂失败，证实其固化了双开行为） | 通过 |
| 包级回归 | Patch 邻域（Patch / Bug0258 / M09 / M11 / BatchUpdate / Mutex） | `JUHE_AI_TEST_EXTERNAL=1 go test ./projects/gateway/internal/accounts -run '(Patch\|Bug0258\|M09\|M11\|BatchUpdate\|Mutex)' -count=1 -timeout 20m` | 全过 | `ok ... 161.358s` | 通过 |
| 前端回归 | 菜单入口互斥不破坏既有状态可用面断言 | `pnpm run test:account-status-formatters` | 回归通过 | 通过 | 通过 |

注意：该包真实 PG 用例需 `JUHE_AI_TEST_EXTERNAL=1`，缺省全 SKIP；不带标志的绿色结果是假阴性。包全量在 10 分钟默认超时内跑不完（`panic: test timed out`），需放宽 `-timeout`。

## 下次遇到

- 「两个互斥标记同时被设置成功」类缺陷，先查校验比较的是「请求体字段两两」还是「写入终值（输入并入行内现值）」；PATCH 单字段通道是这类漏判的高发入口。
- 同一不变式在多个写入面（创建 / PATCH / 批量 / 专用接口 / 导入）各自实现时，逐面核对，不因一处正确推断其余正确。
- 真实 PG 测试默认 SKIP 的包，验证结论必须带 `JUHE_AI_TEST_EXTERNAL=1` 才算数；绿了先确认不是 SKIP。
