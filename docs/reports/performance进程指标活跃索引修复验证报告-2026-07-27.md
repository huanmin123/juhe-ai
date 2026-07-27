# performance 进程指标活跃索引修复验证报告（2026-07-27）

## 结论

生产 Gateway 事件循环指标缺失的根因不是 publisher 未启动，也不是 Nginx 分流失衡，而是 stats reader 在共享 Redis 大键空间中只执行有限页 `SCAN MATCH`。14 个预期进程注册 key 均存在且 TTL 正常，但有限扫描只能随机发现少量 key。现已改为独立有序集合活跃索引，读取开销只随活跃进程数增长，不再随业务 key 总数增长。

修复已完成本地回归、真实 Redis Lua/preflight 和真实 PostgreSQL 最大拓扑验证，尚未部署生产。

## 生产只读证据

- 预期 14 个角色的 sample key 全部存在，剩余 TTL 约 17 至 19 秒，说明各 Gateway、DB service、control 和 worker publisher 正常。
- 旧 reader 使用全局 `SCAN MATCH`，最多 4 页、每页 `COUNT 64`。Redis 的 `COUNT` 不保证返回对应数量的匹配项，在大键空间中不能作为完整发现协议。
- stats worker 的 IPC 只能补采自身管理的子进程，无法访问 launchd 独立托管的 Gateway 与 DB service。
- 内层 Nginx 对三个 Gateway 使用 `least_conn`，请求分布基本均衡。

## 修复边界

- publisher Lua 使用 Redis `TIME`，原子完成 sample `SET EX`、索引 `ZADD`、20 秒过期清理、512 成员基数限制和 60 秒索引 TTL。
- reader 从小型索引查询后一次 `MGET`，不再执行 `SCAN`；Redis score 覆盖本机 payload 时间，消除跨进程时钟偏差。
- publisher 故障只形成监控缺测，不进入用户请求失败路径；连续失败退避为 5、10、10 秒，上限低于 20 秒 lease。
- 拓扑完整性覆盖最大 132 个合法角色，并要求 Gateway/control 与同 ID DB service 配对。
- macOS 安装器按 Gateway-first、control-last 激活。每个服务先 `bootout`，再取 Redis 时间 fence；健康响应给出本次 main、DB service 和 worker PID，部署 gate 必须同时满足 score 严格晚于 fence 且 PID 匹配本次健康拓扑。旧 lease 或旧进程晚到的在途写均无法提前放行。

## 验证结果

本地回归覆盖 14 角色、最大 132 角色、滚动重叠 264 成员、600 次 churn、畸形样本、Redis 时钟偏差、发布错峰、5/10/10 秒失败退避、拓扑完整性和单事务批量持久化。macOS 门禁还直接执行生产 gate 函数，验证三个 Gateway PID 映射最终合并为 control 的 14 角色参数，并验证 PID mapper 非零时部署失败。后端构建、TypeScript 检查及 macOS 静态/dry-run 门禁通过。

授权隔离 Redis smoke 的结果：

```json
{"processCount":14,"sampleTtl":20,"indexTtl":60,"freshnessAndPidFenceVerified":true,"staleMemberRemoved":true,"cappedCardinality":512}
```

该 smoke 先证明旧 lease 在 freshness fence 后不能通过，再用相同稳定 role key 写入新样本，验证本次 PID 成功放行且错误 PID 被拒绝；测试完成后精确删除全部测试 key 并验证零残留。

授权隔离 PostgreSQL smoke 使用一次性随机数据库初始化完整 schema，按最大 132 角色执行真实 repository 写入：266 条 SQL 在一个事务内完成，耗时约 830.6 ms，detail 与 hourly 两类结果均读回 132 个角色。随后终止测试连接、删除临时数据库并确认不存在。

## 容量判断

gateway-3 的 105% 是单次进程 CPU 观测。在多核 macOS 上，单进程 CPU 超过 100% 可以表示使用超过一个逻辑核，不能单独证明持续饱和；当前机器为 4 个物理核、8 个逻辑核。结合 Nginx `least_conn` 与三实例分布基本均衡，目前没有证据支持直接从 3 个扩到 5 个 Gateway。

正确顺序是先部署本次可观测性修复，再采集连续时间窗口内的各 Gateway CPU、事件循环 lag、RSS、入口并发、首字节延迟和 Nginx 分布。只有在持续饱和、排队或延迟退化与业务负载相关时，才评估 4/5 实例压测；扩容本身不会修复注册表漏采。

## 不适用范围

- 本报告不代表生产已部署或线上指标已恢复。
- 本轮不修改 Nginx 算法和 Gateway 副本数。
- 单点瞬时 CPU、单次事件循环峰值或修复前不完整样本都不能作为容量扩展依据。
