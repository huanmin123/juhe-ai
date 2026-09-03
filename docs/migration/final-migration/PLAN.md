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
| K1 http-kernel | archived | 见执行日志 kernel 提交 | `go test -race ./internal/kernel/` 9 项绿；契约对照 shared/http-security.ts、system-error-message.ts、http-compression.ts、deduplication、system-api-app.ts |
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
- 2026-09-04 K1：`gateway/internal/kernel` 完成——响应封装（{data,message}/{message}）、CJK 错误本地化（含裸字符串 payload、上游标记保留）、管理安全头（逐字节对照）、压缩（1024B 阈值/事件流跳过/缓冲语义）、no-store、256KiB JSON 限制（413/400 契约）、404/405 JSON 化、mutation 去重守卫（TTL/清理/键序全对照）、trace/client-ip 上下文。`go test -race` 9 项全绿。
- 2026-09-04 已知基线问题：`maintenance/internal/ownermanifest/TestVerifyRepositoryBusinessOwnerManifest` 在基线 f9c1fbeac 即失败（`account_api_key_pool_probe_cursor type line 844 is stale`；主目录存在维护者未提交的 route-strategies 改动，Go 校验器与已提交 Node 状态不同步）。本迁移不掩盖：该断言涉及的 Node 文件将在 M08-M10 归档时随迁移消失，届时此失败自然解除；W1-W3 每次全量回归将此失败记为 KNOWN-BASELINE-FAIL，不计入迁移回归门。
