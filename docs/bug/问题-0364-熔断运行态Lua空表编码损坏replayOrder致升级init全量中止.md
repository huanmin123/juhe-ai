# BUG-0364：熔断运行态 Lua 空表编码损坏 replayOrder，升级窗口 init 索引重建全量中止

- 级别：P2（运行时读写零影响；唯一爆点是 upgrade 模式的 `-init-account-circuit-runtime-index` 严格解码——每张含坏行的库在该升级模式下必炸，需人工修数据才能拉起）
- 状态：2026-11-13 revision 24 升级窗口发现，数据面已修复（生产 4 行坏行 `{}`→`[]` 原地改写，服务已恢复）；**代码根治未做**，见文末待办
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

## 根治方案（待立项）

优先「读侧容忍」：`runtime_index.go` 的 `accountCircuitRuntimeIndexStateEntry.ReplayOrder` 改自定义 UnmarshalJSON（`{}` 视为空数组）或 `json.RawMessage` 宽松解码——单文件、消灭「任何一次 upgrade 都可能被历史坏行卡死」的雷；写侧 Lua 空表编码不动（cjson 语义不可靠改）。备选：写侧在 `#order == 0` 时用字符串拼接写 `'[]'`（脏，不推荐）。登记 #T207 后续批次。

## 关联

- BUG-0306（init/backfill 语义先例）、T207/BUG-0363（同窗口发现）
- `backend-go/projects/gateway/internal/business/circuit_runtime/runtime_index.go`、`backend-go/shared/platform/circuitstate/lua.go`、`docker/single-server/deploy.sh`（upgrade 模式 init 步骤）
