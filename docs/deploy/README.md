# 部署文档目录

> 以**当前唯一生产形态**为主线：国内单台云服务器 + Docker Compose（PostgreSQL + Redis + gateway/jobs/maintenance + Caddy，go-only）；另附发布包构建与 Linux 直跑差异。文档已按 2026-11-04 现行状态整理，不保留历史形态的部署说明。

## 文档索引

| 内容 | 入口 |
| --- | --- |
| **从零手动部署（主文档）** | [部署指南](部署指南.md)：前置条件、主机初始化、`.env`/Caddy 配置、本地构建、首次部署、验证、更新升级、回滚、备份、监控、排障、迁移 |
| 构建发布包（Windows/macOS/Linux） | [构建指南](构建指南.md)：跨平台构建 Go 三项目 + 前端，生成 go-only 发布包 |
| 非 Docker 直跑（systemd/裸进程） | [linux/README.md](linux/README.md)：与 Docker 形态的差异、cwd/数据目录契约、升级等价操作 |
| HTTPS / 反向代理（外部 TLS 终止） | [https/README.md](https/README.md)：默认 compose Caddy；外部 Caddy/Nginx 终止 TLS 的要求（SSE、真实客户端 IP、HTTP/1.1 upstream） |
| 出海网络代理（AI 上游） | [proxy/README.md](proxy/README.md) → [sing-box 指南](proxy/sing-box网络代理部署指南.md) |

## 权威位置

- 部署配置源文件：仓库 [`docker/single-server/`](../../docker/single-server/)（`compose.yml` / `Caddyfile` / `Dockerfile.runtime` / `deploy.sh` / `verify-release.sh` / `host-init.sh`）。
- 配置契约（`.env` 变量、运行时目录、更新发布、监控端口、schema 升级门禁）：[`docker/single-server/README.md`](../../docker/single-server/README.md)。
- 生产环境的服务器地址、密钥、备份账号等实例事实：私有目录 `.local/project-resources/prod/`，不进入本目录。

## 适用边界

- 本文档面向“拿到仓库即可部署”的维护者：按 [部署指南](部署指南.md) 顺序执行即可完成新机部署与日常发布。
- 不覆盖已退役形态：K3s/K8s、Harbor/Jenkins、家庭宽带反代、Edge/WireGuard 回源、macOS/Windows 本机部署、SQLite/内存存储模式、Node 后端。看到旧文档或旧命令时以本目录为准。
- 修改部署行为（compose / Caddyfile / Dockerfile / 环境变量契约 / 运行时目录 / 初始化与升级序列）时，必须同批更新本目录与 `docker/single-server/README.md`。
