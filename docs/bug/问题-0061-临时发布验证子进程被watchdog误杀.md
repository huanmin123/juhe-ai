# BUG-0061 临时发布验证子进程被 watchdog 误杀

## 现象

在生产主服务仍运行时，用另一端口启动候选 release 做临时验证。候选主进程能够启动，但 DB service 或 worker 每隔约 15 秒收到 `SIGTERM`，进程拓扑始终不完整，发布预检失败。

## 触发条件

- 同一台主机并行运行生产 release 与临时候选 release。
- 外部 watchdog 除 health 检查外，还会清理 PPID 不等于当前生产 server PID 的 `worker.js` / `db-service.js`。
- 临时验证前没有通过服务管理器正式停止 watchdog。

## 根因

watchdog 的孤儿进程判断只认识生产 server PID。候选 release 的正常子进程对生产 server 来说 PPID 不匹配，因此被误判为旧版本残留并结束。临时服务自身的 supervisor 随后重新拉起子进程，形成反复退出与重启。

## 修复

- 同机临时或蓝绿验证前，先通过 launchd/systemd/Windows Service 正式停止带孤儿清理能力的 watchdog。
- 临时验证结束后先清理候选服务及其子进程，再通过服务管理器恢复 watchdog，并验证只存在一个 watchdog 实例。
- 部署脚本必须使用 `finally` / `trap` 等无条件恢复结构；不能用手工 `nohup` 再启动一个重复 watchdog。
- 在部署指南和 watchdog 指南中增加该门禁，要求恢复后再次检查生产 health、进程拓扑和孤儿进程。

## 验证

- 生产部署中正式 `bootout` watchdog 后，候选 release 的 `1 server + 1 db-service + 3 worker` 拓扑保持稳定。
- 清理候选 release 后重新 `bootstrap` watchdog，launchd 状态为运行中，且只存在一个 watchdog 进程。
- 生产主服务 health、API health、worker 拓扑和孤儿进程检查均正常。

## 下次遇到

- 临时 release 子进程周期性退出时，先对齐退出周期与 watchdog 检查周期。
- 同时检查子进程 PPID、watchdog 日志和 supervisor 重启日志，不要先归因于 worker 自身崩溃。
- 临时验证流程必须先执行 watchdog 暂停门禁，并在任何成功或失败出口恢复。
