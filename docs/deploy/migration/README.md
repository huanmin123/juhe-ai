# 迁移到新服务器

> 适用：更换云服务器、迁移机房或重建主机。迁移期间旧机保持可回退，DNS 切流放在最后一步；整体部署流程见[部署指南](../部署指南.md)。

## 1. 迁移前确认

- 新机已按[主机初始化](../host/README.md)完成 Docker、swap/sysctl、防火墙与目录创建。
- 新机规格不低于旧机（4C8G / 60G 起），出站代理已能访问 AI 上游。
- 已确认停写窗口；迁移期间不要同时向新旧两套写数据。
- 旧机完成一次可恢复备份并验证 `pg_restore -l` 可读。

## 2. 搬迁内容

| 搬 | 不搬 |
| --- | --- |
| `/opt/juhe-ai/{compose.yml,Caddyfile,Dockerfile.runtime,verify-release.sh,host-init.sh,.env}` | `build/`（本地重新编译上传） |
| `/opt/juhe-ai/data/` 全部（PG/Redis 数据 + app 文件） | Docker 镜像（新机重新 build） |
| 外部代理配置（sing-box/隧道，按 [proxy/](../proxy/README.md)） | Caddy 命名卷（新机自动重签证书） |
| 监控采集端/监控栈（如独立部署，按 [监控接入](../monitoring/README.md)） | 旧机 Docker 网络与临时容器 |

```sh
# 旧机打包（停写后）
cd /opt/juhe-ai
docker compose stop gateway jobs
tar czf /tmp/juhe-ai-config-data.tgz compose.yml Caddyfile Dockerfile.runtime verify-release.sh host-init.sh .env data
# 传输到新机 /opt/juhe-ai 后解包
```

`.env` 含密钥，传输与落盘按 600 权限管理；不要经过公开网盘或聊天工具。

## 3. 新机启动

```sh
cd /opt/juhe-ai
chmod 600 .env
chown -R 1000:1000 data/app
docker compose --profile tool build
docker compose up -d
docker compose ps
```

- 整体搬 `data/` 时，PG/Redis 数据都在，不需要重跑 schema；Redis 数据也在时不需要重建电路索引。
- 只恢复 PG dump、未搬 Redis 时，必须在 gateway 首次启动前执行：

```sh
docker compose run --rm gateway -init-account-circuit-runtime-index
```

gateway 已启动时先 `docker compose stop gateway`，跑完再 `start`（一次性容器占用固定 IP）。

- 使用 Nginx 入口时，按 [Nginx 部署](../nginx/README.md) 在新机重新签发/拷贝证书，并停用 compose caddy。
- 使用 Caddy 时，确认新机 80/443 可访问，Caddy 会自动申请新证书。

## 4. 验证（切 DNS 前）

按[部署指南 §8](../部署指南.md) 全量执行：

```sh
docker compose ps
curl -fsS -o /dev/null -w '%{http_code}\n' https://<新机域名或临时host>/__aisys__/health
bash verify-release.sh
```

再用临时 `--resolve` 或 hosts 绑定域名到新机 IP，验证登录、`/v1/models` 401、一次真实流式请求与 `usage_records` 增长。

## 5. 切流

1. 降低 DNS TTL（建议提前一天改到 60s）。
2. 旧机停写：`docker compose stop gateway jobs`。
3. 把 DNS A 记录切到新机公网 IP。
4. 等待 TTL 生效，观察新机入口、错误日志、用量与监控。
5. 稳定后停旧机全部容器；保留旧机数据与备份至少一个观察期，确认无回退需求后再释放。

## 6. 回退

- DNS 切回旧机 IP；旧机执行 `docker compose start gateway jobs`。
- 如果新机已经产生写入，回退前先评估数据差异；不要把新机数据直接覆盖旧机。
- 旧机保留期间不要升级旧机 schema/二进制，避免回退时版本不匹配。

## 7. 迁移后清理

- 更新监控采集端/监控栈里的目标地址与隧道（如果 IP 变化）。
- 更新发布脚本 `deploy.sh` 的 `JUHE_AI_DEPLOY_SERVER`、SSH 私钥路径与 `HEALTH_URL`（如使用）。
- 更新备份机的拉取目标与保留策略。
- 旧机上的 `.env`、证书私钥与数据按安全流程销毁，不要留在可访问位置。

## 8. 关联文档

- [部署指南](../部署指南.md)：首次部署、升级、验证。
- [主机初始化](../host/README.md)、[备份与恢复](../backup/README.md)。
- [Nginx 部署](../nginx/README.md)、[HTTPS / 反向代理](../https/README.md)。
- [监控接入](../monitoring/README.md)。
