# BUG-0100 AI 问答 PostgreSQL 跨会话配额竞态

## 基本信息

- 状态：已修复（真实 PostgreSQL 并发验证）
- 严重程度：P1
- 模块：后端 / AI 问答 / PostgreSQL / 配额
- 发现时间：2026-07-13
- 关联计划：PLAN-0103

## 现象与根因

同一用户在两个不同会话并发发送时，两笔 PostgreSQL `READ COMMITTED` 事务分别锁住不同会话行，可能同时读到旧容量窗口并都通过校验，最终合计超过 7 天存储配额。日桶 upsert 只保证计数不丢失，不能让“读取、判断、占用”原子化。

## 修复与验证

`acceptChatTurn` 在分区准备后、会话锁前按 `systemAccountId` 获取事务级 PostgreSQL advisory lock，锁覆盖容量读取、替换扣减、消息和日桶写入；SQLite 继续依赖 `BEGIN IMMEDIATE`。真实 PG 回归用两个不同会话并发提交，修复前 2 个都成功，修复后严格为 1 个成功、1 个 `chat_storage_quota_exceeded`，最终窗口不超限。

## 防复发

用户级配额必须使用用户级事务互斥，不能用会话级锁或日桶原子累加替代检查与占用的事务边界。
