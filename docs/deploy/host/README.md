# 主机初始化与前置条件

> 本文说明部署前对服务器、Docker、防火墙和内核参数的要求；整体部署顺序见[部署指南](../部署指南.md)。主机层治理脚本是仓库内的 [`docker/single-server/host-init.sh`](../../../docker/single-server/host-init.sh)，幂等可重复执行。

## 1. 服务器规格

| 项 | 要求 | 说明 |
| --- | --- | --- |
| CPU / 内存 | 4 核 8G 起步 | 本项目生产实测规格；gateway、PG、Redis、jobs、Caddy 共享 |
| 系统盘 | ≥ 60G | PG 数据 + 备份 + 日志；审计 blob 会持续增长 |
| 系统 | Debian 12/13 或 Ubuntu 22.04+，x86_64 | 其他发行版需自行确认 Docker 与内核参数路径 |
| 公网 | 固定公网 IP | 域名 A 记录指向它 |
| 安全组/防火墙 | 放行 `22`、`80`、`443`（TCP），可选 `443/udp` | PostgreSQL/Redis/指标端口一律不放行 |

## 2. 安装 Docker

要求 Docker Engine 24+ 与 Docker Compose v2，`docker compose version` 可用。

```sh
# Debian/Ubuntu 官方脚本
curl -fsSL https://get.docker.com | sh
systemctl enable --now docker
docker version
docker compose version
```

国内网络拉取镜像失败时，改用云厂商镜像源或 Docker 官方 apt 仓库；`compose.yml` 的构建基础镜像已指向 `docker.m.daocloud.io`。如果 BuildKit 解析基础镜像 HEAD 返回 502，先手动 `docker pull docker.m.daocloud.io/library/alpine:3.20` 预热再重试。

## 3. 域名与 DNS

- 准备一个域名（示例 `<domain>`），A 记录指向服务器公网 IP。
- 默认由 compose 内置 Caddy 自动申请/续期证书；使用 Nginx 时先按 [Nginx 部署](../nginx/README.md) 完成证书引导。
- 首次验证可以临时用服务器 IP + 80 端口，但需要单独改 Caddyfile 与 compose healthcheck，不属于标准流程；正式入口必须 HTTPS。

## 4. 主机层治理（`host-init.sh`）

在仓库根目录执行：

```sh
scp docker/single-server/host-init.sh <user>@<server>:/tmp/host-init.sh
ssh <user>@<server> 'sudo bash /tmp/host-init.sh'   # 已是 root 可去掉 sudo
```

脚本完成三项治理：

1. **4G swap**：`/swapfile`，fstab 持久，`vm.swappiness=10`；仅作内存耗尽兜底。
2. **防内存冻结 sysctl**：`vm.min_free_kbytes=262144`、`vm.watermark_scale_factor=125`、`vm.overcommit_memory=1`，写入 `/etc/sysctl.d/99-juhe-ai.conf`。
3. **journald 日志上限 500M**。

验证：

```sh
swapon --show
sysctl vm.min_free_kbytes vm.watermark_scale_factor vm.overcommit_memory
journalctl --disk-usage
```

`deploy.sh` 每次发布都会自动自检这三项，单项失败只告警、不阻塞发布。新机建议先手工确认一次。

## 5. 目录与属主

```sh
mkdir -p /opt/juhe-ai/{backup,data/app/{data,logs}}
chmod 600 /opt/juhe-ai/.env
chown -R 1000:1000 /opt/juhe-ai/data/app
```

- 容器以 UID/GID `1000` 的 `app` 用户运行；`data/app` 属主必须匹配，否则 usage spool 与日志写不进去。
- `data/postgres`、`data/redis` 由容器自身管理，不需要 chown 给 app。
- 目录结构完整说明见[部署指南 §4](../部署指南.md)。

## 6. 出站代理（可选）

AI 上游需要出海时，先在服务器准备出站代理（sing-box 或常驻隧道），见 [proxy/](../proxy/README.md)。没有代理且上游可直连时可跳过。

## 7. 验证清单

```sh
# Docker
docker run --rm hello-world

# 端口
ss -lntp | grep -E ':(80|443)\b' || echo 'ports free'

# 主机治理
swapon --show
sysctl vm.min_free_kbytes vm.watermark_scale_factor vm.overcommit_memory

# DNS（在本地或服务器执行）
getent hosts <domain>
```

全部符合预期后，回到[部署指南](../部署指南.md)继续上传与首次部署。
