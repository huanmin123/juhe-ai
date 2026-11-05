# Docker 部署

**当前唯一生产形态：国内单机 Docker Compose（go-only）**，配置与构建/更新流程见 `single-server/README.md` 与 `docs/deploy/部署指南.md`：PostgreSQL + Redis + gateway/jobs/maintenance + Caddy（自动 HTTPS），出站代理为宿主机侧常驻代理（当前主用本机 sing-box + 择优控制器 + 每小时订阅更新，常驻隧道为备用，见 `docs/deploy/部署指南.md` 第 5 节）。

旧的 K3s/Harbor 拓扑（`compose.yml`、`compose.performance.yml`、`Dockerfile.go-project` 构建链、`Dockerfile.go-jobs-hotfix`）已于 2026-09-26 废弃删除；本目录保留 `single-server/` 与其运行时 `Dockerfile.runtime`，以及内网**生产演练**变体 `dev-server/`：复用外部 PostgreSQL/Redis，只跑 gateway/jobs/maintenance；按生产语义运行（`NODE_ENV=production`、验证码开启、强制改密门禁开启），仅内网 HTTP 使用 `COOKIE_SECURE=false`，不是生产实例。配置、启动、备份与边界统一见 `dev-server/README.md`。
