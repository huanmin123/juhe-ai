# Docker 部署

**当前唯一生产形态：国内单机 Docker Compose（go-only）**，配置与构建/更新流程见 `single-server/README.md` 与 `docs/deploy/部署指南.md`：PostgreSQL + Redis + gateway/jobs/maintenance + Caddy（自动 HTTPS），出站代理为宿主机 sing-box。

旧的 K3s/Harbor 拓扑（`compose.yml`、`compose.performance.yml`、`Dockerfile.go-project` 构建链、`Dockerfile.go-jobs-hotfix`）已于 2026-09-26 废弃删除；本目录只保留 `single-server/` 与其运行时 `Dockerfile.runtime`。
