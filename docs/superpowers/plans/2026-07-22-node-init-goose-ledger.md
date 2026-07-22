# Node PostgreSQL 初始化与 Goose 账本对齐实施计划

**目标：** 修复 `postgres:init-schema` 在新库只创建业务对象、却没有真实 Goose ledger 的 P1；不从对象存在推断 migration 已应用，不直接写 `goose_db_version`。

**已批准方案：** 初始化在独占 PostgreSQL session advisory lock 内按 `来源预检 -> Goose up -> 精确版本 gate -> Node 幂等补充 DDL -> 可选 seed` 执行。Goose 是 migration 和 ledger 的唯一写入者。Node 当前完整 DDL 不被声明为 Goose catalog 的等价物，也不用于回填版本。

## 完成项

- [x] 新库无 ledger 且无 `juhe_*` 对象时允许 Goose 从零执行。
- [x] 无 ledger 但已有 `juhe_*` 对象时 fail-closed，拒绝伪造 lineage。
- [x] 已有较低 Goose 版本时允许 Goose 从真实 ledger 续跑；高于当前 catalog 时拒绝。
- [x] Go `schema-up` 先校验 migration catalog，再使用 `goose.UpToContext` 执行并精确验证最终版本。
- [x] Node 通过无 shell 子进程调用维护命令，连接串只放环境变量，不进入 argv。
- [x] 同一 Node PostgreSQL session 持有 advisory lock；并发初始化快速拒绝，失败后尝试解锁并销毁专用连接。
- [x] 定向 Node 回归、Go test/vet、无直接 ledger 写入检查。
- [x] 迁移记录和验证说明同步。

## 后置验证

- [ ] 在可销毁 PostgreSQL 上运行 fresh、Goose 中途失败续跑、Node DDL 失败续跑和双进程竞争演练。
- [ ] 在统一验收轮运行 backend typecheck 和更大范围回归；隔离 worktree 当前没有本地 Node 依赖链接。
- [ ] 生产旧库只允许按独立离线迁移计划处理，本命令不提供 ledger backfill 或自动 Down。
