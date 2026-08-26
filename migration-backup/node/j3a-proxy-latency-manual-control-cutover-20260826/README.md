# J3a Node 手动控制面切换备份

- 归档日期：2026-08-26
- 原始提交：`c5bfcfd9f19ff66c6e39ab5cab568dce80ae82a2`
- 切换目标：删除 Node 的 J3a 手动执行桥与 Node→Go 健康观测；代理检测请求改由 `juhe-ai-jobs` 进程内的 Go 管理入口处理。

## 归档内容

- `backend/src/modules/background/proxy-latency-handover.ts`：完整的 Node→Go loopback 手动执行桥，删除前 SHA-256 为 `8F37BE3A6F46A61E1081B1884BBF1A00C0621C58624F9BBFF9CCB30E79983C86`。
- `backend/src/modules/proxies/proxy-test.contract.ts`：仅供上述 Node 手动桥使用的报告契约，删除前 SHA-256 为 `B8405F04D8C3EF6159753BEBDFF7F003852CA8CB5F3D9FE397230E182ECBEEC4`。
- `proxies-manual-test.route.ts`：从原始提交中 `backend/src/modules/proxies/proxies.routes.ts` 删除的 Node 管理路由片段。
- `system-api-j3a-health-observer.ts`：从原始提交中 `backend/src/modules/system-api/system-api-app.ts` 删除的 Node→Go 健康探测。

本目录只用于回溯，不参与构建或运行。恢复前必须先验证 Go 管理端、审计表契约和入口路由，而不是恢复 Node fallback。

逐文件来源、SHA-256、停用入口和仍未完成的运行时门禁见同目录 `manifest.json`。
