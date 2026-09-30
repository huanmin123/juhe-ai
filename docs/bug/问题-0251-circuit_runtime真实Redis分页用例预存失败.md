# BUG-0251 circuit_runtime W14G 真实 Redis 分页用例预存失败（readHash 页数上限未触发）

## 基本信息

- 编号：BUG-0251
- 状态：已修复（2026-09-30，纯测试改造，不改变生产二进制行为）
- 严重程度：P3（单测门禁红灯；被测域生产逻辑无缺陷）
- 发现时间：2026-09-30
- 发现方式：复审发版前 gateway 全模块测试
- 模块：backend-go gateway `internal/business/circuit_runtime`（W14G 运行态索引回填）
- 关联 bug：无直接关联；与 BUG-0250 同属「已提交基线上的既有失败」形态

## 问题概述

- 现象：`TestW14GRealRedisScanPaginationBounds` 稳定失败于 `w14g_runtime_branches_test.go:980: readHash 页数上限 必须失败`——用例以 `MaxPages=1 / ScanCount=1` 扫描 seed 后的 states hash，期望 readHash 触发页数上限错误，实际调用成功返回。
- 该用例连远端 dev 共享 Redis（`.local/project-resources/dev/env/shared.env` 的 `JUHE_AI_REDIS_CACHE_URL`；`JUHE_AI_W1COVER_REDIS_URL` 可覆盖）。

## 复现步骤

1. 修复前：`cd backend-go/projects/gateway && go test ./internal/business/circuit_runtime/ -run TestW14GRealRedisScanPaginationBounds -count=1` 稳定失败。

## 环境信息

- 分支 / 版本：master（c3922e706 引入用例，2026-09-18；046a1a17c 父提交同样失败）
- dev Redis：**8.2.7**（探针实证）
- 是否稳定复现：是

## 根因分析（探针实证闭环）

用例缺陷（环境不可满足的前提断言），**非产品代码缺陷**。`readHash`/`scanAndApply` 的页数上限逻辑本身正确。

- HSCAN 的 `COUNT` 只是服务器迭代提示而非返回数上限——**listpack 编码的小 hash 一页即全量返回且游标归零**，`cursor==0` 短路成功返回，页数上限分支不可达。
- 探针实证（dev Redis 8.2.7）：2-field、50-field hash + `COUNT 1` 均一页全回（`nextCursor=0`）；**1000-field hash 才按 COUNT 逐 entry 分页**（每页约 1 个，游标推进）。分水岭是 hash 编码：默认 `hash-max-listpack-entries=128`，超阈值 hash 转 hashtable 才有 COUNT 分页语义。
- 原用例只 seed 2 个 state（恒在 listpack 区间），**从 c3922e706 引入起在任何环境都不可能通过**；miniredis 亦无法承载该分支（探针实证 miniredis 的 HSCAN 无视 COUNT、1000-field 也一页全回）。

## 修复方案

纯用例改造（`w14g_runtime_branches_test.go`），生产代码零改动：

1. seed 从 2 个 state 改为 **200 个**（单次批量 HSet，超过默认 listpack 阈值 128，hash 转 hashtable 后 `COUNT 1` 逐 entry 分页，实测每页约 1 个）；
2. 新增**分页可行性探测**：首轮 `HSCAN COUNT 1` 游标归零（环境把 `hash-max-listpack-entries` 调得更高等）时按「环境不可构造分页」`t.Skip` 而非红灯——环境行为漂移时优雅降级；
3. 双断言（readHash 页数上限 + scanAndApply 页数上限）保持不变；
4. 注释写明 BUG-0251 机制与 listpack/hashtable 分水岭，防止回退到小 hash seed。

## 验证结果

- `TestW14GRealRedisScanPaginationBounds` 修复后通过（0.08s，dev Redis 8.2.7 实际分页触发上限）。
- `circuit_runtime` 整包 `-count=1` 全绿（12.6s）。
- `w14gSeedTwoStates` 仍被其余 4 处用例引用，保留；探针文件已删除。

## 防回归与观测性备注

- 「在真实 Redis 上断言分页行为」的用例必须 seed 超过 listpack 阈值的数据量，并对「一页全回」做 Skip 降级——COUNT 语义无契约保证。
- 该修复为测试文件改造，不进入生产二进制，无需随批发版（下次任意 gateway 发布自然携带）。
