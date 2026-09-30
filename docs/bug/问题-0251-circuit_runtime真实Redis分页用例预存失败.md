# BUG-0251 circuit_runtime W14G 真实 Redis 分页用例预存失败（readHash 页数上限未触发）

## 基本信息

- 编号：BUG-0251
- 状态：已立案，待诊断（预存缺陷，非 2026-09-30 复审批次引入）
- 严重程度：P3（单测门禁红灯；被测域不在近期发版改动范围，生产运行未见对应异常）
- 发现时间：2026-09-30
- 发现方式：复审发版前 gateway 全模块测试
- 模块：backend-go gateway `internal/business/circuit_runtime`（W14G 运行态索引回填）
- 关联 bug：无直接关联；与 BUG-0250 同属「已提交基线上的既有失败」形态

## 问题概述

- 现象：`TestW14GRealRedisScanPaginationBounds` 稳定失败于 `w14g_runtime_branches_test.go:980: readHash 页数上限 必须失败`——用例以 `MaxPages=1 / ScanCount=1` 扫描 seed 后的 states hash，期望 readHash 触发页数上限错误，实际调用成功返回。
- 该用例连**远端 dev 共享 Redis**（`.local/project-resources/dev/env/shared.env` 的 `JUHE_AI_REDIS_CACHE_URL`，192.168.1.203 cache 实例 DB9；`JUHE_AI_W1COVER_REDIS_URL` 可覆盖）；Redis 不可达时用例自行 Skip，当前未 Skip 说明实例可达且读写正常。

## 复现步骤

1. `cd backend-go/projects/gateway && go test ./internal/business/circuit_runtime/ -run TestW14GRealRedisScanPaginationBounds -count=1`；
2. 稳定失败（约 0.5-2.5s，无拨号重试）。

## 环境信息

- 分支 / 版本：master；stash 二分证实 046a1a17c（Lua 空数组编码修复）父提交同样失败，回归早于该提交，未继续向前追溯
- 是否稳定复现：是（单跑/全量跑均失败）
- 外部依赖：dev 共享 Redis（实例行为不受本仓库控制）

## 根因分析

- 未定。已核实的事实边界：
  - circuit_runtime 包在 2026-09-30 13:3x 发布点（e020a6d90）之后的全部待发布提交（24544f44c 及工作区批次）中**零改动**，本失败不随 2026-09-30 晚间发版扩散或修复；
  - 失败不在 Redis 拨号（未 Skip、读写均成功），而在分页上限语义：MaxPages=1/ScanCount=1 时 readHash 未报错——可能方向：① 某历史提交改变了 HSCAN 分页计数/上限判定（需向前二分定位引入点）；② dev 共享 Redis 环境变化（版本、hash 元素布局、共享 DB 干扰）导致页数判定永不触达。
- 排查建议：向前二分（c3922e706「修复多项缺陷并改进测试稳定性」为先验嫌疑）；或在隔离本机 Redis（docker 临时实例）上复跑对照，剥离共享环境变量。

## 修复方案

- 待诊断后定。

## 验证结果

- 立案待修。复审批次处置结论：不阻塞 2026-09-30 晚间发版（该域零变更随批）。

## 防回归与观测性备注

- 本失败依赖远端共享 Redis，属「环境敏感型」用例：诊断时须区分代码回归与环境漂移，优先用隔离 Redis 实例对照。
