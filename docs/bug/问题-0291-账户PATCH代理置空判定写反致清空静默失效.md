# BUG-0291 账户 PATCH 代理置空判定写反致清空静默失效

## 基本信息

- 编号：BUG-0291
- 状态：已修复（待发布验证）
- 严重程度：P2
- 发现时间：2026-10-07
- 发现方式：用户反馈
- 模块：后端 / 网关（accounts PATCH）
- 关联计划：无
- 关联 bug：无

## 问题概述

- 现象：用户在 AI 账户编辑弹窗「策略与代理」把已绑定的代理点 × 清空后保存（接口 200、提示"账户已更新"），重新打开编辑弹窗代理仍在，代理解绑永远不生效。
- 期望：编辑保存携带 `proxyProfileId: null`（nullable id 三态语义的"清除"臂）时应移除账户行内 `proxy_profile_id` 绑定，`changedFields` 记入 `proxyProfileId` 并推进 `config_revision`。
- 实际：PATCH 返回 200 且 `changedFields: []`，行内代理绑定保持不变；反向副作用：对未绑定代理的账户提交清空会虚记一次变更并空转推进 `config_revision`。
- 影响范围：`PATCH /__aisys__/api/accounts/{id}` 与 `/my-accounts/{id}`（同一 handler，管理面与自助面同时受影响）的代理置空臂；代理新绑定与切换臂不受影响。自 Node→Go 迁移起清空代理从未生效过。

## 复现步骤

1. 给任一 AI 账户绑定一个启用代理（列表可见代理标识）。
2. 打开编辑弹窗 →「策略与代理」→ 代理下拉点 × 清空 → 确定；请求为 `PATCH /accounts/{id}`，body 含 `"proxyProfileId":null`，返回 200。
3. 重新打开编辑弹窗：代理仍显示原值；高级详情 `GET /accounts/{id}/advanced` 的 `proxyProfileId` 不变。

## 环境信息

- 分支 / 版本：master 工作区（Node→Go 迁移后全部已发布版本均受影响），2026-10-07。
- 数据状态：不受影响（纯写路径判定缺陷，无脏数据；错误方向多推的 revision 无消费方依赖其"必须无变化"语义）。
- 浏览器 / 系统 / Node 版本：不适用（后端缺陷，任意客户端表现一致）。
- 是否稳定复现：是（隔离 SQLite 实例 API 级稳定复现）。

## 根因分析

- 表象：清空保存"成功"但绑定原样保留，无任何报错。
- 真实根因：`internal/accounts/patch.go` 代理段的变更判定把方向写反了——

  ```go
  changed := (requested == nil) != current.Valid || ...
  ```

  清空臂（`requested == nil`，行内已绑定 `current.Valid == true`）算出 `true != true = false`，被误判"无变化"静默跳过；反向地，对未绑定账户清空（`true != false = true`）反而虚记变更、空转 bump revision。正确语义是"有无代理"对比：`(requested != nil) != current.Valid`。
- 为什么会发生：Node 归档迁移带入（`656f81e47`/`e62f82240` 波次）。`w2_patch_full_test.go` 迁移期即发现该行为反常，按《后端测试分层规则》以「行为存疑」注释按现状断言固化，但对应问题档案一直未建（本次补 BUG-0291）；既有 `TestW13GPatchProxyArms` 的"清空"用例只断言不报错、且账户本就未绑定代理，红不了。

### 前端链路核实（排除前端嫌疑）

编辑弹窗清空 × → `form.proxyProfileId = undefined` → `saveProxyProfileId(undefined, editing=true)` 返回 `null` → `buildAccountAdvancedUpdatePatch` 差量含 `proxyProfileId: null`（`hasDefinedOwn` 视 null 为存在值）→ PATCH body。全链正确，前端无需改动；断点唯一在后端判定。

## 修复方案

- 修改点：
  - `patch.go` 代理段 `changed` 判定反转为 `(requested != nil) != current.Valid`（一处，四种组合全表正确：清空已绑定→变更；清空未绑定→无变化；新绑定→变更；换绑同 id→无变化）。
  - `w13g_patch_arms_test.go` 新增「绑定启用代理 → 清空 → 断言行内绑定移除 + `changedFields` 含 `proxyProfileId`」红绿回归（修复前在该断言必红）。
  - `w2_patch_full_test.go`「行为存疑」按现状断言反转为正确语义（置空后行内移除 + 列入变更）。
- 行为影响：置空代理现在真实生效，并按既有契约计为连接变更（触发保存后配置探针派发、`config_revision` 推进、网关运行时缓存失效）；对未绑定代理重复清空不再虚增 revision。无 env/schema 变化。
- 发布异常处理：无；存量"想解绑但解不掉"的账户发布后在页面重新清空保存一次即可。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 红测试 | 清空已绑定代理（修复前） | `go test ./internal/accounts/ -run TestW13GPatchProxyArms` | 「清空后行内代理绑定应移除」断言红 | 红（绑定残留） | 通过 |
| 回归验证 | accounts 包全量（修复后） | `go test ./internal/accounts/ -count=1` | 全绿 | ok 43s | 通过 |
| 静态检查 | go vet | `go vet ./internal/accounts/` | 无告警 | 无告警 | 通过 |
| 端到端 | 隔离 SQLite 实例 API 复现（修复后二进制） | 建 proxy + 绑定账户 → `PATCH {"proxyProfileId":null}` → 读回 | `changedFields:["proxyProfileId"]`、revision 1→2、advanced 读回为空 | 一致 | 通过 |
| 幂等验证 | 重复清空不再虚增版本 | 同上再 PATCH 一次 | `changedFields:[]`、revision 保持 2 | 一致 | 通过 |

前端差量构造单验（临时 vitest，验证后已删）：清空表单态 delta 含 `proxyProfileId: null`，通过。

## 复发记录

- 无（首次登记）。

## 下次遇到

- 先查什么：三态（present + nullable value）写字段的"变化判定"布尔方向，优先对四种组合画真值表。
- 重点看什么：`*Present` 标志与 `*string` nil 的组合语义；"接口 200 但 changedFields 空"是判定写反的典型指纹。
- 如何避免误判：「行为存疑」按现状断言必须在当时建问题档案，防止缺陷被测试永久固化（本次 w2 注释固化近一个月无人跟进）。

## 完成总结

- 完成时间：2026-10-07
- 结论：一处布尔方向写反，修复后管理面/自助面账户编辑清空代理真实生效；accounts 包全量 + 隔离实例端到端红绿闭环。
- 后续建议：发布无需数据修复；同类「按现状断言」存疑注释全库清点已完成，20 条无档案线索入册 BUG-0294，同型升级实例见 BUG-0293。
