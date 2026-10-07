# 问题-0286：余额快照显示守卫用全局 config_revision，任何编辑都打断余额显示

## 基本信息

- 编号：BUG-0286
- 状态：已修复（2026-10-07，生产用户报障「开启余额查询前端长时间显示待查询」引出，用户升级定性「任何修改都会打掉缓存是全局性缺陷，要求全局排查」）
- 严重程度：P1（用户可感知的显示缺陷 + 全局性判据设计问题）
- 发现时间：2026-10-07
- 发现方式：用户报障 + 生产数据取证
- 模块：前端账户列表余额列 / gateway hydrate / shared J2 执行核 / jobs 投影与自动探测
- 关联 bug：BUG-0009（无自动刷新取向，同源用户体感）、问题-0196 遗留 #4（历史非 RFC3339 next_refresh_at 存量）

## 问题概述

- 现象：开启余额查询的账户在前端长期显示「待查询」；后端 J2 实际每 5 分钟无间断查询且快照 fresh（生产实证：某账户 09-26 起累计 7351 次 periodic outcome、最近成功在分钟级）。
- 期望：余额输入（凭据/适配器配置/provider）未变化时，已查询快照持续显示；输入变化时立即失效并在下一轮查询自愈。
- 实际：显示守卫 `BalanceSnapshotMatchesConfiguration` 用全账户级 `config_revision` 相等 + `next_refresh_at` 毫秒相等双判定——① 任何编辑（改名/备注/状态等非余额字段）都推进 config_revision，打断显示直到下一轮 J2 写入（≤5 分钟）；② next_refresh 毫秒相等把每个刷新周期边界的投影滞后（≤1 分钟）变成随机显示空洞。两者叠加造成「长时间待查询」。
- 影响范围：账户列表余额列、余额详情视图（同匹配函数）；398 个暂停账户的「待查询」属设计内（J2 只刷 active+可调度，tooltip 已解释），与本缺陷叠加放大了体感。

## 根因分析

- 表象：待查询驻留。
- 真实根因：显示有效性判据误用全局 revision。config_revision 承担两类语义——运行态并发围栏（派发 CAS/probe fencing/冷却守卫，全局推进正确）与显示缓存有效性（本缺陷，需要的是余额输入身份而非全局版本）。
- 为什么会发生：判据移植自 Node `accountBalanceSnapshotMatchesConfiguration`（同为全局 revision + 毫秒相等），Node 时代即有的设计缺陷，go-only 终态下用户裁决按输入身份重造。

## 修复方案

- 判据重造：`BalanceSnapshotMatchesConfiguration(record, currentDigest)` 只匹配余额输入身份摘要。摘要 = `BalanceInputDigest(provider_code, credential_fingerprint, balance_query_config_json 列原文)`（SHA-256 截 16 hex，shared/platform/accountbalance 唯一算法源）。
- 写端：唯一执行入口 `ExecuteAccountBalanceQuery` 薄壳统一打点 `Snapshot.InputDigest`（覆盖 J2 周期/首探、SQLite 自动探测、gateway 手动刷新全部写入方）；PG 投影白名单透传、SQLite `buildSnapshotJSON` 透传。
- 读端：`LoadBalanceInputDigests` 按页现算当前摘要（900 分块，列值 COALESCE 与 J2 直读 reader 同源），匹配相等才携带 balanceSnapshot。
- 显式不修（定性非缺陷）：自动状态翻转（markCooldown 等）不推进 config_revision（仅 CAS 守卫），不触发本缺陷；暂停账户不自动查询为设计口径；T7 六条授权实例列表投影属另一域的对齐裁决项。

## 验证记录

| 验证类型 | 验证内容 | 结果 |
| --- | --- | --- |
| 单元 | BalanceInputDigest 契约（确定性/逐输入区分/trim 稳定） | 通过 |
| 单元 | 匹配臂重写（nil/空负载/缺摘要/摘要不一致不匹配；摘要一致且 revision 已推进仍匹配） | 通过 |
| 集成 | accounts 包全量（overlay/self/m11 details/refresh contract/chunking） | 通过 |
| 集成 | shared accountbalance 全量 + jobs balance 用例 | 通过 |
| 生产取证 | J2 周期 outcome 按天 2.1-2.8 万条连续、目标账户 5 分钟节拍无空洞、active 账户快照 0 缺失 | 见问题描述 |

## 遗留与边界

- 发布后存量旧形状快照（无 inputDigest）在下一轮 J2 写入（≤5 分钟）内自愈为匹配形态，无需数据修复。
- 多机部署时本摘要不含时钟/实例语义，无跨机约束；`credential_fingerprint` 由凭据写路径维护（patch/rotation），若某写入方漏维护指纹会导致换 Key 不失效——全局排查确认现行写路径均已维护。
