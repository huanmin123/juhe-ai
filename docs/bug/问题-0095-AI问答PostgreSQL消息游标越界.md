# BUG-0095 AI 问答 PostgreSQL 消息游标越界

## 基本信息

- 状态：已修复（PostgreSQL 回归）
- 严重程度：P1
- 模块：后端 / AI 问答 / PostgreSQL / 游标分页
- 发现时间：2026-07-13
- 关联计划：PLAN-0103

## 现象与触发条件

消息列表不传 `beforeSequenceNo` 时，repository 使用 `Number.MAX_SAFE_INTEGER` 作为 SQL 哨兵。SQLite 可接受该值，但 PostgreSQL `sequence_no integer` 无法绑定，导致首次加载消息失败。

## 根因

共享 SQL 为省略可选条件引入了超大数哨兵，却没有遵守 PostgreSQL `int4` 参数范围；SQLite 回归无法暴露驱动契约差异。

## 修复与验证

无游标时直接省略 `sequence_no < ?` 条件；有游标时只接受 PostgreSQL integer 范围内的安全整数，超范围输入同样不绑定。PostgreSQL SQL 形态回归和真实 PostgreSQL/Redis smoke 均通过。

## 防复发

跨 SQLite/PostgreSQL repository 不使用超出目标列类型的哨兵值表达“无条件”；可选过滤应生成可选 SQL 片段，并为参数类型边界单独写 PostgreSQL 回归。
