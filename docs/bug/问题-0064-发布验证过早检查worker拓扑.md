# BUG-0064 发布验证过早检查 worker 拓扑

## 现象

候选或生产 release 的主 health、API health 已返回成功，但发布验证立即报告只有 2 个 worker，或没有输出明确失败原因便退出。生产切换脚本因此触发自动回滚，旧 release 随后正常恢复。

## 根因

- 主服务开始监听和 DB service health 就绪早于第三个 worker 完成 spawn，health 成功不代表完整 `1 server + 1 db-service + 3 worker` 拓扑已经稳定。
- 验证脚本只读取一次进程树，没有为 supervisor 启动留出收敛窗口。
- 修成轮询后，`set -e` 又会把 `grep -c` 暂时没有匹配时的退出码 `1` 当成脚本失败，导致循环尚未等待就退出。

## 修复

- 临时准备脚本和生产验证脚本都在 health 成功后最多轮询 30 秒，只有完整 `1+1+3` 拓扑出现才通过。
- 进程计数允许暂时为 `0`，`grep -c` 后使用 `|| true`，由显式数量判断决定继续等待或失败。
- 超时后输出 server、DB service、worker 的最终计数；不降低拓扑标准，也不把缺少 worker 当成成功。
- 修正版已部署到生产主机 `juhe-ai-lite/bin`，临时验证与生产切换均使用同一等待规则。

## 验证

- 临时 release 在 3101/3102 通过 health、前端哈希、数据库列和完整拓扑验证，随后清理临时 PostgreSQL、Redis namespace 与进程。
- 前两次生产验证因旧脚本竞态自动回滚，旧 release、health、watchdog 和完整拓扑均恢复。
- 修复后 release `20260712-1043-balance-ui-d0f5e7c87` 通过目标路径、端口、完整拓扑、本机/公网 health、前端哈希、PostgreSQL、Redis 和 90 秒 PID 稳定门禁。

## 下次遇到

- health 成功后仍要等待 supervisor 子进程收敛，不能立即断言拓扑。
- `set -e` 脚本中的计数/探测命令如果允许“暂时未命中”，必须显式吸收该状态，再由业务条件判断。
- 生产切换必须保留 `current` 回滚陷阱；验证失败时先确认自动回滚完成，再修改验证门禁并重试。
