# BUG-0365 账户并发 tracker 时钟方法值冻结——列表并发恒 0、并发/RPM 限流判定失真

## 基本信息

- 编号：BUG-0365
- 状态：已修复已发布（v2026.11.14，2026-11-14；3 天观察期至 2026-11-17，见 #T208）
- 严重程度：P1（限流判定失真 + 可观测面失效；业务请求本身不受影响）
- 发现时间：2026-11-14
- 发现方式：用户反馈（管理页并发列恒 0）+ 生产取证
- 模块：网关（gatewayclientip 账户并发 tracker）
- 关联计划：无
- 关联 bug：BUG-0361（缺陷随其重构激活）、#T206 / #T208
- 责任人：胡安民

## 问题概述

- 现象：生产管理页账户列表「限频」列并发恒显示 `0/N`（如 carborn 系统 鸡毛1/鸡毛c/鸡毛z 全部 `0/5`），而 `/v1` 在途请求持续存在；自 v2026.11.13-2（BUG-0361 根治版）上线起出现。
- 期望：列表并发读数实时反映账户在途请求数；并发上限与 RPM 窗口按真实时间判定。
- 实际：gateway 账户并发 tracker 的时钟冻结在进程启动毫秒——槽 score、清理阈值、RPM 分钟桶全部静止；jobs 以真实时钟读同一批键，把在途槽当过期清空。
- 影响范围：管理列表并发读数恒 0；账户总并发上限间歇失效（窗口性放行）；lane 死槽只增不减 → lane 假满后账户被调度永久跳过；RPM（rpm_limit>0 账户）分钟桶永不滚动，持续流量下跨窗口累计至假性永久拒绝。业务请求链路（占位/释放 ZADD/ZREM）本身存活，不失败。

## 复现步骤

1. 部署 v2026.11.13-2 及之后的 gateway（`chain_runtime.go` 生产组合根未注入 `Clock`，走默认时钟分支）。
2. 持续发送 `/v1` 请求，观察管理页账户列表并发列 → 恒 `0/N`。
3. redis MONITOR 观察 tracker 的 acquire/count/release Lua，`now_ms` 参数恒等于 gateway 启动毫秒；同秒其他子系统（hot-quality/circuit）拿到正确当前 ms。

## 环境信息

- 分支 / 版本：master；缺陷自 v2026.11.13-2（commit `b543edcf1`）上线激活，修复随 v2026.11.14 上线。
- 数据状态：生产 Redis `{prod:concurrency}` 域 `gateway-account-concurrency:account-concurrency:{id}:slots(:<lane>)` 残留 score=启动+30min 的死槽（鸡毛z 4 个/上限 5、d129 3 个/上限 20、超的中转 1 个）。
- 是否稳定复现：是（MONITOR 参数与 ZCARD 行为 100% 复现）。

## 根因分析

- 表象：列表并发恒 0、部分账户 lane 计数虚高。
- 真实根因：`backend-go/projects/gateway/internal/gatewayclientip/accountconcurrency_store_redis.go` `NewRedisAccountConcurrency` 默认时钟分支写成 `clock = time.Now().UnixMilli`——这是**方法值**：`time.Now()` 在构造时求值一次，`.UnixMilli` 绑定到那份 `time.Time` 值接收者副本，此后每次调用恒返回进程启动毫秒（Go method-value 陷阱）。BUG-0361 重构（30m 租约 ZSET 槽）使 clock 成为槽 score/清理阈值/RPM bucket 的核心，潜伏缺陷随即激活。
- 为什么会发生：`437c66077` 引入该行时 clock 仅用于无实害的次要路径；BUG-0361 改造未重审默认分支语义。同仓库正确写法均为闭包（`func() int64 { return time.Now().UnixMilli() }`，如 `gatewayhotquality/redis_store.go:146`），该处是唯一例外。全仓扫描 `clock = time.Now` 形式 7 处，其余 6 处为包函数值（每次调用取当前时间），无同类问题。

## 修复方案

- 修改点：默认时钟分支改为闭包 `clock = func() int64 { return time.Now().UnixMilli() }`，附注释说明方法值陷阱与后果；新增回归 `TestRedisAccountConcurrencyDefaultClockAdvances`（不注入 Clock 时两次采样必须单调推进且贴近真实时刻；缺陷版红灯实测 `before==after==1794648486161`）。
- 行为影响：重启 gateway 后清理阈值恢复真实时钟，存量死槽（score 在真实过去）在首次读/写时被自动清光，lane 自愈，无需手工清 Redis；jobs 零改动（其读取面本就用真实时钟）。
- 发布异常处理：无 schema 变更，常规 `deploy.sh gateway` 滚动发布；如需回滚，旧版二进制恢复冻结时钟（回到缺陷状态，不产生新故障面）。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 回归红灯 | 缺陷版时钟冻结 | stash 修复后 `go test ./internal/gatewayclientip/ -run DefaultClockAdvances` | FAIL（before==after） | `before=1794648486161 after=1794648486161` FAIL | 通过 |
| 回归绿灯 | 修复版时钟推进 | 恢复修复后同命令 | PASS | ok 1.362s | 通过 |
| 功能基线 | 受影响包全量 | `go test ./internal/gatewayclientip/ -count=1` | 全绿 | ok 16.463s | 通过 |
| 装配回归 | cmd 并发定向 | `go test ./cmd/juhe-ai-gateway/ -run 'Concurrency\|ChainAccountConcurrencyReader\|SlotLease\|AccountRPM' -count=1` | 全绿 | ok 3.530s | 通过 |
| 构建/静态 | gateway 模块 | `go build ./... && go vet ./...` | 通过 | 退出码 0 | 通过 |
| 生产验证 | tracker 时钟/列表读数/死槽清理 | 上线后 MONITOR now_ms=当前、在途请求期间列表并发>0 并随结束回落、`:slots:text` 死槽清零 | 见执行记录 | `now_ms=1794648826268`（≈now，发布前恒 `1794548348847`）；d129 在途 ZCARD=1 持续采到；鸡毛z 4/d129 3 个死槽自愈清零；重启后 12 分钟 ERROR 0；rpm_limit>0 配置面 0 | 通过 |

## 复发记录

- 无（首次发现）。

## 下次遇到

- 先查什么：涉及时钟/租约/窗口的 Redis Lua 计数面，先核对 `now_ms` 实参是否随调用推进（MONITOR 抓参数最直接）。
- 重点看什么：`func` 类型字段/参数被赋以 `time.Now().xxx` 方法值、或构造时求值的时间快照被当动态时钟用。
- 如何避免误判：容器/宿主机时钟同步正常 ≠ 进程内时钟正常；「读数恒 0 但流量正常」优先怀疑降级投影或时钟冻结，而非读路径失败（后者有 warn 日志）。

## 完成总结

- 完成时间：2026-11-14（本地落地；上线执行记录回填于 #T208 与运维手册发布记录）
- 结论：一行根治 + 默认时钟回归锁定；同类全仓扫描确认无第二例方法值时钟。
- 后续建议：#T206 观察期在本修复上线后重新起算；上线时顺带核实 rpm_limit>0 账户配置面。
