# 部署文档目录

> 面向 AI 与维护者。**当前唯一生产部署形态：国内单台云服务器 + Docker Compose（go-only）**。家庭宽带/内网回源反向代理、公网 Edge 隧道、macOS/Windows 本机部署、K8s/K3s 等形态已于 2026-09-26 全部废弃并删除文档，不要再按旧方式部署。

## 权威入口

| 内容 | 位置 |
| --- | --- |
| 生产部署（唯一形态） | [部署指南](部署指南.md) |
| 部署配置源文件（compose / Caddyfile / 运行时镜像） | 仓库 `docker/single-server/`（含 README，构建与更新流程以它为准） |
| Linux 服务器差异（防火墙、systemd、发布包直跑） | [linux/](linux/README.md) |
| HTTPS 证书（Caddy 自动 ACME + 续期） | [https/](https/README.md) |
| 出站网络代理（sing-box，AI 上游出海） | [proxy/](proxy/README.md) |
| 跨平台构建发布包 | [构建指南](构建指南.md) |

## 已废弃（2026-09-26 清理，文档已删除）

- 家庭宽带反向代理 / 公网 Edge / WireGuard 回源隧道（`scenarios/家庭宽带反向代理方案`、`反向代理与高并发隧道部署指南`）——家庭部署不稳定，生产已全部上云。
- K3s/K8s 混合拓扑（`高性能模式部署指南`、`docker/compose.performance.yml`、Harbor/Jenkins 发布链）——不再使用。
- macOS / Windows / watchdog 等本机部署形态与旧单容器 Docker 指南。
