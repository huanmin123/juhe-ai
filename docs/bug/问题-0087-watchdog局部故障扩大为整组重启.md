# BUG-0087 watchdog 局部故障扩大为整组重启

- 状态：已修复并完成生产验证
- 严重程度：P0
- 模块：部署 / watchdog / DB service / supervisor / 生产稳定性
- 发现日期：2026-07-15

## 现象

生产随机出现 502、TLS 连接中断和约 30 秒超时。当天 watchdog 共执行 34 次整组终止，其中 API health 单独失败触发 9 次、双 health 失败触发 19 次、主 health 单独失败触发 6 次。

## 根因

- 外部 watchdog 把主 health 与 DB service 对应的 API health 都当成整组重启条件，DB service 局部卡顿会终止仍可服务静态资源和现有连接的主 Node 进程。
- 连续失败阈值仅 2 次；3 秒采样后不重新确认是否恢复。
- 名义 180 秒启动保护会在首次双 health 成功后提前取消。
- 没有整组重启冷却和时间窗口预算，局部抖动可形成重启风暴。
- 主进程 supervisor 只处理 DB service 退出，不能处理“进程仍在但 health 卡死”。

## 修复

- DB service 由主进程 supervisor 直接健康探测并定向恢复：完整 180 秒宽限、15 秒间隔、5 秒超时、连续失败 3 次、TERM 后 10 秒核对同一 child 再 KILL、60 秒冷却、15 分钟最多 3 次。
- 外部 watchdog 对 API health 只观察；主 health 连续失败 4 次后采样，并再确认 2 次。
- 启动宽限按 PID 墙钟完整保留；整组重启增加 180 秒冷却和 15 分钟最多 1 次的持久化预算。
- TERM/KILL 前同时核对 launchd PID、进程启动时间和 release cwd；预算状态写入失败时禁止终止主服务。

## 验证

- Node：DB service 恢复策略、timer 清理、同 child KILL 竞态回归。
- macOS 脚本：`bash -n`、watchdog regression、`plutil -lint` 和生产 60 秒固定 PID 验收。
- 生产发布后必须确认 API-only 降级只出现 `action=observe-only`，主 PID 不变化。
- 2026-07-16 生产 launchd 配置与单实例状态核对通过；最终 60 秒验收期间主 PID、watchdog PID 未变化，watchdog 无失败、采样或重启动作。

## 防复发

- 外部 watchdog 只负责主服务整体不可用；DB service / worker 局部恢复必须由持有子进程对象的 supervisor 负责。
- 任何自动终止都必须具备连续阈值、动作前复核、身份核对、冷却和窗口预算。
- 发布前固定执行 60 秒验证；故障定位采样默认 1 分钟即可，不再使用 1200 秒连续采样拖长发布窗口。

