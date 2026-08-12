# BUG-0148 Windows 路径转换破坏前端 API Base

## 状态

已修复并完成生产浏览器验证；零停机 candidate/handover 流程仍待下一次发布证明。

## 现象

2026-08-12 正式发布后，Node、Go、Nginx 的进程和 health 均正常，静态页面也返回 `200`，但浏览器持续进入“服务正在恢复”，管理后台不可用。

浏览器 console 的关键错误为 Axios 不支持磁盘盘符 scheme。检查正式前端主 bundle 后确认，构建期应为 `/__aisys__/api` 的 `VITE_JUHE_AI_API_BASE_URL` 被改写成了 Windows 文件系统路径。静态资源与 health 不依赖该 Axios base，因此它们会同时误报“服务正常”。

## 根因

正式包由 Windows 侧通过 Git Bash/MSYS 启动 Windows `node`/`pnpm` 构建。MSYS 在进程启动边界把根相对参数和环境变量当作 POSIX 路径转换，最终把磁盘路径注入 Vite bundle。Vite、压缩器、Node 和 pnpm 接收到的已经是错误值；它们不是转换源。

该问题与 recovery 开关、认证 session、Go F3 writer 或 Nginx route 无关。页面进入恢复路由只是前端认证/API 请求失败后的可见结果。

## 生产修复

1. 从同一正式 source commit 重新构建前端，显式确认 `VITE_JUHE_AI_API_BASE_URL=/__aisys__/api`。
2. 检查新 `frontend/dist/build-info.json` 与 source commit 一致，bundle 含根相对 API base 且不含盘符形式。
3. 在目标 Mac 暂存并校验前端 tar，只原子替换正式 release 的 `frontend/dist`；旧错误目录移动到 handover 恢复位置。
4. 未重启 Node、Go、Nginx 或 Caddy。浏览器加载新 hash bundle后，使用现有登录态进入账户页和统计页，业务数据正常渲染。

## 永久门禁

- `scripts/package-release.ps1` 和原生 Unix 下的 `scripts/package-release.sh` 在修改输出目录前只接受严格的 HTTP(S) API base 或精确的根相对值 `/__aisys__/api`。
- Windows 正式构建只从 PowerShell 调用 PowerShell 打包器。Unix 打包器在任何 Windows Bash（包括 `MINGW/MSYS/CYGWIN` 和 w64devkit）中立即失败，不通过环境变量排除规则兼容该 shell。
- 生产 Mac 包在目标 Mac 或受控原生 macOS 构建机生成。项目不再解析压缩后的 JavaScript bundle 来猜测 API base。
- 构建后 `rg` 只用于事故诊断；真正的发布门禁是原生构建环境、固定 commit/buildId、独立 candidate 和真实登录态业务页。
- production candidate 必须用真实浏览器现有登录态打开依赖 API 的页面，并确认无 recovery page、scheme/network 错误；`index.html`、health 和任意单个 `200` 都不能替代。
- 高性能发布必须先启动独立 candidate，再用 `performance-handover-controller.sh` 切流。不能先停 active 后在线修包。

## 验证证据

- PowerShell 打包器会在创建输出目录前拒绝事故形态的盘符 API base；Windows Bash 入口会在任何构建或输出变更前拒绝运行。
- release source、release package、Go sidecar launcher 和 macOS operations 回归通过。
- 正式浏览器加载新 bundle，并在保留登录态的情况下成功进入账户页和统计页。

## 关联

- [BUG-0147 mac 候选发布依赖预检缺失](问题-0147-mac候选发布依赖预检缺失.md)
- [跨平台构建文档](../deploy/构建指南.md)
- [AI 部署执行清单](../deploy/AI部署执行清单.md)
