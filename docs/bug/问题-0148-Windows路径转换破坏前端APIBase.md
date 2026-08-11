# BUG-0148 Windows 路径转换破坏前端 API Base

## 状态

已修复并完成生产浏览器验证；零停机 candidate/handover 流程仍待下一次发布证明。

## 现象

2026-08-12 正式发布后，Node、Go、Nginx 的进程和 health 均正常，静态页面也返回 `200`，但浏览器持续进入“服务正在恢复”，管理后台不可用。

浏览器 console 的关键错误为 Axios 不支持磁盘盘符 scheme。检查正式前端主 bundle 后确认，构建期应为 `/__aisys__/api` 的 `VITE_JUHE_AI_API_BASE_URL` 被改写成了 Windows 文件系统路径。静态资源与 health 不依赖该 Axios base，因此它们会同时误报“服务正常”。

## 根因

正式包由 Windows 侧构建。根相对参数经过 Git Bash/MSYS 到 PowerShell 的 shell 边界时发生路径转换，最终把磁盘路径注入 Vite bundle。发布流程只校验了 source commit、文件存在、静态资源和 health，没有扫描最终 bundle，也没有在 candidate 上执行真实浏览器登录态业务页。

该问题与 recovery 开关、认证 session、Go F3 writer 或 Nginx route 无关。页面进入恢复路由只是前端认证/API 请求失败后的可见结果。

## 生产修复

1. 从同一正式 source commit 重新构建前端，显式确认 `VITE_JUHE_AI_API_BASE_URL=/__aisys__/api`。
2. 检查新 `frontend/dist/build-info.json` 与 source commit 一致，bundle 含根相对 API base 且不含盘符形式。
3. 在目标 Mac 暂存并校验前端 tar，只原子替换正式 release 的 `frontend/dist`；旧错误目录移动到 handover 恢复位置。
4. 未重启 Node、Go、Nginx 或 Caddy。浏览器加载新 hash bundle后，使用现有登录态进入账户页和统计页，业务数据正常渲染。

## 永久门禁

- `scripts/package-release.ps1` 和 `scripts/package-release.sh` 在修改输出目录前只接受严格的 HTTP(S) API base 或精确的根相对值 `/__aisys__/api`（总长度上限 2048、path 上限 1024 字符），并拒绝其它根相对路径、盘符、UNC、协议相对、userinfo、无效端口、query、fragment、空白、反斜杠、authority percent escape 和畸形 percent escape。
- Windows 正式构建从 PowerShell 调用 PowerShell 打包器，默认值不必显式传入；不得从 Git Bash/MSYS 传根相对路径参数。
- 构建后用 `rg` 检查 bundle：必须命中 `/__aisys__/api`，盘符加 `__aisys__/api` 的表达式必须无输出。
- production candidate 必须用真实浏览器现有登录态打开依赖 API 的页面，并确认无 recovery page、scheme/network 错误；`index.html`、health 和任意单个 `200` 都不能替代。
- 高性能发布必须先启动独立 candidate，再用 `performance-handover-controller.sh` 切流。不能先停 active 后在线修包。

## 验证证据

- PowerShell 与 Bash 打包器的动态回归均以事故形态的盘符 API base 执行，并证明在创建输出目录前拒绝。
- release source、release package、Go sidecar launcher 和 macOS operations 回归通过。
- 正式浏览器加载新 bundle，并在保留登录态的情况下成功进入账户页和统计页。

## 关联

- [BUG-0147 mac 候选发布依赖预检缺失](问题-0147-mac候选发布依赖预检缺失.md)
- [跨平台构建文档](../deploy/构建指南.md)
- [AI 部署执行清单](../deploy/AI部署执行清单.md)
