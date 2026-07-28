# BUG-0139 Goose fresh schema 迁移链断裂

## 基本信息

- 编号：BUG-0139
- 状态：已修复（迁移分支，未合入 / 未部署）
- 严重程度：P1
- 发现时间：2026-07-28
- 发现方式：W7 Windows 隔离 PostgreSQL / Redis / Asynq 真实依赖验收
- 模块：Goose / PostgreSQL / Go 迁移 / W7 worker
- 关联计划：PLAN-20260706T071505000Z
- 责任人：Codex

## 问题概述

- 现象：W7 harness 在全新 PostgreSQL 上执行 Go-only `schema-up` 时，先在 migration 77 因 `juhe_chat.chat_messages` 不存在失败；修复后又在 migration 80 因 `system_settings.value_json` 的 `text = jsonb` 比较失败。
- 期望：当前 Goose catalog 能从空数据库连续迁移到 schema 92；尚未迁入 Goose 的 Node-owned 表不能阻断 fresh Go schema，共存列必须遵守 Node / Go 当前一致的物理类型。
- 实际：77 号 migration 无条件修改 Node-owned Chat 表；80 号 migration 把实际为 `text` 的 JSON 文本列臆测为 `jsonb`。

## 根因分析

1. migration 契约测试只检查目标 SQL 片段，没有在 fresh PostgreSQL 上连续执行整个 catalog。
2. 迁移实现把“历史 Node 数据库中存在该表”错误外推为“Goose fresh schema 已创建该表”。
3. 80 号 migration 的测试把错误的 `::jsonb` 写法也冻结下来，测试证明了源码一致性，却没有证明数据库类型兼容。
4. 初版 W7 Windows harness 又因 `pg_ctl` 后台子进程继承捕获管道而挂起，导致 schema 错误没有及时暴露。

## 修复方案

- migration 77 的 Up / Down 都在 Goose `StatementBegin/StatementEnd` 内用 `to_regclass()` 检查目标表；已有 Node Chat 表时继续执行加法升级，fresh Go schema 中不存在时显式 no-op。
- migration 80 按权威物理契约直接比较和写入 JSON 文本字面量；只有需要 JSON 运算时才允许显式转换，不改变 `value_json text`。
- W7 Windows harness 使用不捕获后台句柄的进程启动方式，继续只调用 Go `schema-up`；禁止切换到 Node schema 初始化、已有业务库或伪造 Goose ledger。
- 新增 migration 77 所有权边界测试，并同步 80 号契约测试。

## 验证记录

| 验证类型 | 覆盖 | 状态 |
| --- | --- | --- |
| migration 单元 | 77 号 Up / Down 存在性保护、80 号 text 契约、catalog 连续性、schema-up | 通过 |
| fresh PostgreSQL | PostgreSQL 18.4，Goose 0 -> 92，最终 `targetVersion=92/currentVersion=92` | 通过 |
| 真实 Redis / Asynq | 三套独立 Redis 8.8.1、W0、W7 gate / OAuth lock / consume / unique lease | 通过 |
| race | `internal/testkit/w7real` | 通过 |
| 清理 | 退出后 W7 临时目录与所属 PostgreSQL / Redis 进程计数均为 0 | 通过 |

以上只证明 fresh catalog 和当前 W7 harness 子集，不证明历史无 ledger Node 库可原地升级，也不代表生产 worker owner 已切换。

## 下次遇到

- 每次新增或修改 migration 后，除了源码契约测试，还要定期从空 PostgreSQL 连续执行完整 catalog。
- Node-owned 表尚未迁入 Goose 时，加法 migration 必须显式处理对象不存在；不能在验收脚本里调用 Node 初始化掩盖所有权缺口。
- JSON 内容不等于 PostgreSQL `jsonb` 物理类型，先从权威 DDL 和真实数据库确认列类型。
- harness 挂起先检查后台进程继承的 stdout/stderr 句柄；真实依赖脚本必须验证只清理自己创建的进程和目录。
