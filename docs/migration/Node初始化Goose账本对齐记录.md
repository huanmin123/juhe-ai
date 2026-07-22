# Node 初始化 Goose 账本对齐记录

## 问题与结论

旧 `postgres:init-schema` 直接执行 Node 从 SQLite 定义生成的当前 PostgreSQL DDL。它能创建业务对象，但不会产生 `public.goose_db_version`，因此 Go/Node 的精确 schema gate 无法证明 migration lineage。

不能用“表和列看起来齐全”回填 Goose 版本。Node 完整 DDL 与 69 个 Goose migration 是两个事实源：前者描述 Node 当前仍需的对象，后者还包含按顺序执行的转换、约束、索引和 seed。没有逐项等价证明时直接插入版本属于伪造 migration。

本批采用 Goose-first：

1. 在专用 PostgreSQL session 使用 `pg_try_advisory_lock` 获取固定锁；已有初始化时快速拒绝，不无界等待。
2. 无 ledger 时确认 `juhe_*` schema 内没有业务对象；发现未知对象立即拒绝。
3. 调用 Go `juhe-ai-maintenance schema-up`，由 Goose 真实执行当前完整 catalog 并写入 ledger。
4. 复用 Node 精确 schema gate 验证当前版本且不存在更高已应用版本。
5. 执行 Node 当前幂等 DDL，补充迁移期仍由 Node 使用、但尚未进入 Go catalog 的对象。
6. 非 `--schema-only` 时写入默认数据，最后释放 advisory lock。

## 失败恢复

| 失败位置 | 数据库状态 | 重跑行为 |
| --- | --- | --- |
| 来源预检前或预检失败 | 无新业务 DDL | 修正目标库或配置后重跑 |
| Goose 中途失败 | 只有 Goose 已成功提交的真实版本 | Goose 从 ledger 继续执行 |
| 精确 gate 失败 | ledger 保持真实状态 | 不执行 Node DDL；检查 catalog/版本漂移 |
| Node DDL 失败 | Goose 已到当前版本，Node 补充 DDL 部分成功 | Goose no-op 后重跑幂等 Node DDL |
| seed 失败 | schema 与 ledger 已完成，seed 部分成功 | 重跑幂等 DDL 和 seed |
| advisory unlock 失败 | 初始化结果可能已完成 | 命令失败并销毁专用连接，由 PostgreSQL 释放 session lock |

已有 ledger 且版本低于当前 catalog 可以从真实进度继续；高于当前 catalog、ledger 表为空、或无 ledger 但已有 `juhe_*` 对象均 fail-closed。本命令不自动 Down，也不用于把来源未知的生产旧库升级或登记为当前版本。

## 本批证据与边界

- Node 状态机回归覆盖 fresh、未知旧库、低版本续跑、高版本拒绝、Goose-first 顺序、`--schema-only`、migration 失败不执行 Node DDL、锁释放和子进程连接串边界。
- Go 定向 test/vet 覆盖 catalog、目标版本、migration 失败、最终版本和无直接 ledger mutation。
- 本批没有连接真实 PostgreSQL；fresh catalog、故障注入和双进程竞争必须在后续统一真实依赖验收中复跑，未完成前不构成生产初始化或旧库迁移证据。
