# Node 全量清零迁移执行看板（PLAN）

> 唯一进度事实源。每个 WP 状态：pending / in-progress / archived。每片收口（G5）由主 Agent 更新本表并在切片记录目录落证据。
> 权威迁移 worktree：`F:\sub2api-lite-migration`；分支：`node-to-go-final`（共享主目录固定 master 只读）。

## 基线记录（M0，2026-09-04）

- 本地 master：`f9c1fbeac`（== origin/master，无漂移）
- 迁移分支 HEAD：`node-to-go-final` @ `f9c1fbeac`
- 主目录 status：维护者有未提交改动（route-strategies speed-first/rehearsal 相关 + docs/plans 两文件），不属于本迁移，不触碰；本迁移计划文档已复制入 worktree。

## 波次进度

| 波 | 状态 | WP 明细 |
| --- | --- | --- |
| W1 | in-progress | K1 K2 K3 K4 K5 K6 K7 S-PG S-SQ + doc |
| W2 | pending | M01-M07 |
| W3 | pending | M08-M14 |
| W4 | pending | M15-M17 P01-P03 G01-G03 |
| W5 | pending | G04-G08 P04 P05 |
| W6 | pending | G09-G14 C03 |
| W7 | pending | G15-G19 J-A J-B（波末 G20 启动） |
| W8 | pending | J-Ca J-Cb J-Cc J-D J-E J-F C01 |
| W9 | pending | G20(主Agent) C02 X01 X02 X03 |
| W10 | pending | X04 X05 X06 |

## 工作包状态

| WP | 状态 | 提交 | 证据 |
| --- | --- | --- | --- |
| K1 http-kernel | in-progress | — | — |
| K2 session-auth | pending | — | — |
| K3 rate-limit | pending | — | — |
| K4 oplog-producer | pending | — | — |
| K5 invalidation-bus | pending | — | — |
| K6 legacybridge | pending | — | — |
| K7 mockupstream+golden | pending | — | — |
| S-PG ensure-schema PG | pending | — | — |
| S-SQ ensure-schema SQLite | pending | — | — |

（W2+ 的 M/P/G/C/J/X 各行随波次开启时补入。）

## 执行日志

- 2026-09-04 B0.1：基线 M0 记录（如上）；worktree 创建于 `F:\sub2api-lite-migration`；计划文档五件迁入 worktree。B0.2 端点级路由矩阵与逐 job 文件映射在 W1 由主 Agent 生成。
