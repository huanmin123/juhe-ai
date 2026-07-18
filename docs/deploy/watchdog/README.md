# 状态检测与恢复

本目录说明进程级恢复和只读健康观察。juhe-ai 不再使用外部 HTTP watchdog 主动终止或重启服务。

- 主进程异常退出由 launchd、systemd、Windows Service 或容器 restart policy 拉起。
- DB service 和 worker 由主进程 supervisor 独立看护；显式退出后按 `1s / 5s / 15s / 30s / 1m / 2m / 5m / 10m` 退避，连续稳定 10 分钟后才清零。
- DB service 进程存活但 health 异常时，只由主进程在持续失败和恢复预算保护下定向处理 DB service。
- 公网域名只用于观察和告警，失败不能单独触发重启、代理切换、回滚或清理子进程。
- 发布流程不得安装、恢复、enable 或 bootstrap 旧 watchdog。

详细边界见 [状态检测与自动恢复指南](状态检测与自动恢复指南.md)。旧平台 watchdog 示例已经退役，见 [自动恢复部署示例](自动恢复部署示例.md)。
