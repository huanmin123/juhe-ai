# 问题-0306：电路运行时孤儿 closed 墓碑无法 GC 致模型检测全部永久秒拒

- **日期**：生产故障暴露于 2026-10-10 21:3x（用户报告），根因遗留自 2026-10-09 Node→Go 键空间切换；修复于 2026-10-11
- **严重级**：P1（模型检测/J3b 探针面全量不可用，任何账户、任何重试均秒拒；/v1 真实流量不受影响；数据零损失）
- **状态**：已修复（gateway 单项目；修复自愈，无需生产手工清键）

## 现象

生产模型检测（手动触发，任意账户如 api.shenwenai.com-神影-克隆，gpt-6.1-sol，快速档）恒定失败：约 4 秒返回「不可检测 0/100」，attempt 证据全部为

```text
J3b upstream request failed: prepare account circuit attempt: mutate account circuit runtime: invalid account circuit runtime index
attemptStatusCodes = [0, 0, 0]
```

三次 attempt 均 0–2ms 内被拒（纯 Redis Lua 即时拒绝，从未发出上游请求）。10-10 21:44 按 runbook 执行 `-init-account-circuit-runtime-index` 重建索引（stop gateway → init → start，发布 "ready"）后，用户 02:52 复测**依然同错**——重建不能治愈。同时段 /v1 真实流量与 jobs 侧账户健康探针（200 正常）完全不受影响。

## 取证（生产 Redis DB 2，键空间 `juhe-ai:prod:account-circuit:gateway-account-circuit:*`）

| 键 | 事实 |
| --- | --- |
| `runtime-index-meta` | `status=ready`，`stateCount=1`，`revisionCount=1693`，`auditAtMs=1791639893037`（= 2026-10-10 21:44:53 +08，即最后一次重建） |
| `states` | 1 条（当前活跃 scope，与索引一致） |
| `scope-runtime` / `runtime-scopes` / `account-runtimes` / `runtime-accounts` | 各 1 条，与 states 一致 |
| **`closed`（zset）** | **5 条成员，score 1791396~1791399 ×10⁶ ms ≈ 2026-10-08 上午——早于 10-09 Go 切流，Node 时代遗留的已过期 CLOSED 墓碑，且在 `scope-runtime` 中无任何索引映射（孤儿）** |
| `due` / `escalation` | 不存在（空） |

## 根因（双重缺陷，第二处使第一处永久化）

1. **GC 活性缺陷（直接根因）**：mutation 与 escalation 两个 Lua 脚本的 `cleanup_closed()` 在**每次 mutation 入口无条件执行**，对"closed zset 有过期成员、但 `scope-runtime` 无索引映射"的形态校验失败返回 false → 全部 mutation 秒拒 `invalid account circuit runtime index`。更致命的是**该失败永久化**：要删除这些过期墓碑必须先通过索引校验，而索引映射已不存在——墓碑永远无法被 GC，中毒无法自愈。
2. **重建形成路径缺陷（使其跨重建稳定复现）**：`begin`（重建脚本）只 `DEL` 四个索引 hash 并从 `states`/`escalation` 重建，**不触碰也不清扫 `closed`/`due`**；audit 也不检查 closed/due。因此凡"`closed` 里有比 `states` 更早的墓碑"的键空间，重建后必然中毒，且重建显示"成功 ready"（audit 通过）——10-10 21:44 重建"成功但无效"即此机理。

形成路径：5 条墓碑产生于 Node 时代（score 均 10-08，早于 10-09 切流）；Go 侧键空间切换/索引重建后其索引映射消失，`cleanup_closed` 从此恒假。影响面仅限装配 `probeCircuit`（`JUHE_AI_J3B_CIRCUIT_REDIS_URL`，独立 DB 2）的模型检测/J3b 探针路径；/v1 主链不经过该 mutation。

## 修复（全部在 `internal/business/circuit_runtime`）

1. **mutation 脚本与 escalation 脚本的 `cleanup_closed()`**：索引映射缺失的过期墓碑按不可投影垃圾直接回收（`HDEL states` + `ZREM due` + `ZREM closed`），不再返回 false；**索引映射存在但内容不一致仍保持响亮失败**（真实损坏不吞）。`HGET` 缺失返回 Lua `false`，语义安全。
2. **两脚本的 `reserve_capacity()` evict 路径**：最旧 closed 条目为孤儿墓碑时同样回收，避免容量压力下同类卡死。
3. **`begin`（重建）脚本**：以 `states` 为准清扫 `closed`/`due` 中无来源成员（`HEXISTS … == 0` 显式比较——Lua 中 `HEXISTS` 返回数字 0/1，**0 是真值**，`not 0` 恒假），消除形成路径；重建后键空间与投影自洽。
4. `w15a_orphan_tombstone_test.go`：5 用例钉死——孤儿自愈 GC、映射不完整仍响亮失败、容量驱逐可回收孤儿、重建清扫 closed/due、未过期墓碑保留至过期。红相在 miniredis 上逐字复现生产错误后转绿。

## 真实 dev e2e（用户指令：本地测真实账号，把生产的拿下来）

隔离实例（SQLite 业务库 + dev Redis 独立 DB5/DB6 + namespace `dev:j3be2e`，`JUHE_AI_SECRET` 仅进程 env 注入、不入任何文件），拉取生产账号 `acc_1789614837673_2dc29a61`（api.shenwenai.com-神影-克隆，含凭据密文）及其分组绑定/支持模型，播种生产同款 5 条孤儿墓碑：

| 阶段 | 二进制 | 结果 |
| --- | --- | --- |
| 红 | HEAD（无修复） | `mutate account circuit runtime: invalid account circuit runtime index` ×3 attempt、httpStatus 全 0、score 0/100——与生产逐字一致 |
| 绿 | 修复版 | 首次 mutation 自愈 GC（closed 5→0），真实上游探测 28.9s，**score 85/100「快速检测未发现明显异常」** |

证明：① 根因成立且修复有效；② 上线后**无需 runbook 停机重建**——生产那 5 条孤儿墓碑会被修复版第一次 mutation 自动回收。

## 教训（固化）

1. **墓碑 GC 不得以"索引一致性校验通过"为删除前置**：过期 CLOSED 墓碑是等待回收的垃圾，删除动作本身不能要求垃圾先满足不变式，否则任何投影/索引重建留下的孤儿都会把 GC 永久卡死（活性优先，真实不一致另行走响亮失败）。
2. **重建/迁移脚本的"来源唯一性"必须覆盖全部派生结构**：本例索引从 `states`/`escalation` 重建，但 `closed`/`due` 也是派生键——凡有 states 之外的残留成员即是孤儿，重建时应一并清扫并在 audit 中覆盖。
3. **排障必须先核对键空间分片事实**（state=DB1 / J3b circuit=DB2 是启动强校验的隔离契约）：本次最初对 DB1 采样得出"索引三 hash 全空"的假象，導向多条错误假设；换 DB2 后一次采样即定位。

## 关联

- 键空间隔离契约：`docker/single-server/README.md`（Redis /1=state、/2=J3b circuit runtime）。
- 探针结算契约：BUG-0262（探针失败不处罚被测账户）。
- e2e 运行留档：`.local/project-resources/dev/runtime/modelcheck-circuit-e2e-20261011/`（含 run-red.json / run-green.json，凭据密文不出该目录）。
