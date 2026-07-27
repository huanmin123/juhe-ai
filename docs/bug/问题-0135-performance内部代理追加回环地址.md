# BUG-0135：performance 内部代理追加回环地址

## 状态

- 严重级别：P1
- 状态：已修复
- 影响范围：performance 模式下的客户端 IP 注册、统计、查询与封禁

## 现象

IP 管理只显示 `127.0.0.1`，不同真实客户端和不同 API Key 的新请求也无法形成新的 IP 成员。

## 根因

公网边缘与 TLS 入口已正确形成可信 `X-Forwarded-For`，外层 Nginx 也原样透传；内部 performance 槽位再次使用 `$proxy_add_x_forwarded_for`，把其本机 socket 对端 `127.0.0.1` 追加到链尾。生产 Express 使用 `trust proxy=1`，因此把最近一跳回环地址当作客户端。

页面、IP 列表仓储和 `client-ip-stats-aggregation` 均正常；它们接收到的上游事实已经错误。

## 修复

- performance 槽位的管理、公开、内部和网关四类路由统一透传外层来源头。
- 回归门禁固定四类路由的三个头，并禁止 `$remote_addr`、`$proxy_add_x_forwarded_for` 和 `$scheme` 旧写法。
- 生产配置先备份、语法检查，再平滑 reload，并用公网请求、日志和数据库做端到端验证。

## 数据边界

已写为 `127.0.0.1` 的历史明细没有可靠的一对一来源证据，本次不删除、不猜测回填。修复只保证新请求正确；历史重建需另行评估原始边缘访问日志的完整性和关联精度。

## 验证结果

- 生产配置语法检查、平滑 reload、本地 `/__aisys__/health`、`/__aisys__/api/health` 和公网 health 均通过。
- 公网唯一 trace 在 Gateway 请求开始和结束日志中都记录为非回环真实 IPv4。
- 修复后约一分钟内，`client_ip_registry` 从只有 `127.0.0.1` 恢复为新增 4 个不同真实来源。
- `client_ip_stats_aggregation` 持续成功推进，`last_error_message` 为空。
- 管理页面现有浏览器会话为普通用户，未绕过权限直接读取管理员列表；页面事实源已由生产数据库验证恢复。

## 关联

- [执行计划](../plans/计划-20260727T140542094Z-performance真实客户端IP修复.md)
- [IP 统计与封禁设计](../functions/IP统计与封禁设计.md)
- [macOS performance 运维脚本](../deploy/macos/operations/README.md)
