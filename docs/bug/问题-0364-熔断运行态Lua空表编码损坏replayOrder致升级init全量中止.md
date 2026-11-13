# BUG-0364：熔断运行态 Lua 空表编码损坏 replayOrder，升级窗口 init 索引重建全量中止

- 级别：P2（运行时读写零影响；唯一爆点是 upgrade 模式的 `-init-account-circuit-runtime-index` 严格解码——每张含坏行的库在该升级模式下必炸，需人工修数据才能拉起）
- 状态：**读侧容忍根治已实施（2026-11-13 复查处方，随下次发布生效；本地回归绿，未单独发布——运行时零影响，无需紧急上线）**；升级窗数据面修复 4 行后被运行进程 15 分钟内复发（写坏路径是活代码，`{}` 会持续产生但被容忍解码无害化），见 §复发实录
- 登记：docs/待办与阻塞事项.md（随 T207 发布观察条目）

## 现象

2026-11-13 v2026.11.13-4（revision 24）停写窗口：`maintenance upgrade` 成功（schema 23→24）后，deploy.sh 的 `gateway -init-account-circuit-runtime-index` 步骤失败：

```
backfill account circuit runtime index: decode account circuit runtime state source:
json: cannot unmarshal object into Go struct field accountCircuitRuntimeIndexStateEntry.replayOrder of type []string
```

gateway 无法启动（fail-fast 循环）。

## 根因

`circuitstate` Lua 脚本（`shared/platform/circuitstate/lua.go` 与 gateway `circuit_runtime/runtime.go` 内嵌同族脚本）以 Redis Lua `cjson.encode` 序列化 states HASH 条目 `{state=..., replayOrder=...}`。Lua 空表 `{}` 被 cjson 编码为 **`{}`（空对象）而非 `[]`（空数组）**（lua-cjson 无法区分空表的数组/对象语义）：

- 初始化分支 `entry = entry or { replayOrder = {} }`、回写分支 `local order = entry['replayOrder'] or {} ... entry['replayOrder'] = order`——凡 `replayOrder` 恰好为空的条目被写回后即损坏为 `"replayOrder":{}`。
- 生产 v2026.11.13-3 运行 10 小时即产生坏行（`{prod:j3b}` states 的 `acc_1787124065051_172f101e` gpt-6.1-sol 条目，updatedAtMs 当日上午）。
- **运行时零影响**：Lua 读路径 `entry['replayOrder'] or {}` 对空表/对象表天然容忍（ipairs 空表 0 迭代）；只有 Go `runtime_index.go` backfill 的 `ReplayOrder []string` 严格解码会失败——而 backfill 只在 upgrade 模式的 init 步骤运行（`deploy.sh all/gateway` 滚动发布不跑 init），所以历史上从未暴露。
- 附带发现：Redis DB2 存在旧 namespace 残留键 `juhe-ai:prod:account-circuit:gateway-account-circuit:*`（无 hash tag 形态，含 3 行同型坏行），非当前读写路径（当前 namespace 为 DB0 `juhe-ai:{prod:circuit}` / `juhe-ai:{prod:j3b}`），已顺带修复其中坏行、键本身待清理。

## 处置（2026-11-13 升级窗口内）

数据面：对生产 4 个坏行（`{prod:j3b}` states 1 行 + DB2 残留键 3 行）原地 HSET 改写 `"replayOrder":{}` → `"replayOrder":[]`（停写窗口内无并发写；语义等价：空回放列表）；`escalation` HASH 的 `scopes` 空数组同型检查为零。修复后 init 成功（`account circuit runtime index is ready`），gateway/jobs 拉起 healthy。

## 复发实录（2026-11-13 上线后终审复查发现）

窗口内数据修复（`{prod:j3b}` states 1 行 `{}`→`[]`）约 15 分钟后，v2026.11.13-4 运行进程把同一行重新写坏（`replayOrder:{}`，`state.updatedAtMs`=12:31:49，晚于 init 重建成功 12:15:07 与容器拉起 12:17:04）——**写坏路径是活代码**（`circuitstate` Lua 的 `entry['replayOrder'] or {}` 回写分支），凡 replayOrder 恰好为空的条目每次写回都会再坏。运行时零影响的结论同时成立（Lua 读路径容忍），故不构成运行事故；但证明数据面修复只是临时解，下次 upgrade 必卡。

## 根治（2026-11-13 实施，随下次发布生效）

读侧容忍（终审复查建议的优先方案）：

1. `runtime_index.go`：`accountCircuitRuntimeIndexStateEntry.ReplayOrder` 与 `accountCircuitRuntimeIndexEvidence.Scopes` 改带 `UnmarshalJSON` 的命名类型（`circuitRuntimeReplayIDs` / `circuitRuntimeEvidenceScopes`）——`{}` 与 `null` 解码为空数组，其余非法形态（非空对象、字符串、数字、坏 JSON）**仍原样报错**，不放宽其他校验（超限/重复/CLOSED 截止等语义不变）。
2. 同文件 finalize 脚本收敛时 `HDEL error`（复查 MINOR-1）：上次失败经 invalidate 臂写入的 error 字段曾与 `status=ready` 并存误导诊断。
3. 回归：`w7x_runtime_index_lenient_test.go` 三用例（空表/null/真数组/非法形态矩阵、escalation scopes 空表、finalize 清 error）；circuit_runtime 全包与 `cmd/juhe-ai-gateway` 全量回归绿。

写侧 Lua 空表编码不动（cjson 空表语义不可靠改；容忍解码后坏行无害化，不再构成升级阻塞）。生产现存坏行不再需要修复。

## 关联

- BUG-0306（init/backfill 语义先例）、T207/BUG-0363（同窗口发现）
- `backend-go/projects/gateway/internal/business/circuit_runtime/runtime_index.go`、`backend-go/shared/platform/circuitstate/lua.go`、`docker/single-server/deploy.sh`（upgrade 模式 init 步骤）
