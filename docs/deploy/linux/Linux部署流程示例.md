# Linux 部署流程示例

## 目标

- 路径：`/opt/juhe-ai-lite/current`
- 入口：宿主机 Caddy `https://ai.example.com`
- 常驻：`systemd`
- 存储：外部 PostgreSQL + Redis（唯一存储形态；连接串按部署环境替换）
- 上游代理：同机 sing-box `socks5h://127.0.0.1:7890`

## 步骤

```bash
sudo useradd --system --home /opt/juhe-ai-lite --shell /usr/sbin/nologin juhe || true
sudo install -d -o juhe -g juhe /opt/juhe-ai-lite/releases/20260627 /opt/juhe-ai-lite/shared/data /opt/juhe-ai-lite/bin /opt/juhe-ai-lite/logs
sudo -u juhe tar -xzf juhe-ai-release.tar.gz -C /opt/juhe-ai-lite/releases/20260627
cd /opt/juhe-ai-lite/releases/20260627/juhe-ai-release
sudo -u juhe install -d /opt/juhe-ai-lite/shared
sudo -u juhe cp -n backend/.env /opt/juhe-ai-lite/shared/backend.env
sudo -u juhe ln -sfn /opt/juhe-ai-lite/shared/backend.env backend/.env
sudo -u juhe rm -rf backend/data
sudo -u juhe ln -sfn /opt/juhe-ai-lite/shared/data backend/data
sudo ln -sfn /opt/juhe-ai-lite/releases/20260627/juhe-ai-release /opt/juhe-ai-lite/current

# schema 初始化与默认 seed（juhe_jobs/juhe_j3b schema 预置见 docker/single-server/README.md 拓扑节）
sudo -u juhe env JUHE_AI_POSTGRES_URL="postgres://<user>:<password>@<host>:5432/juhe_ai?sslmode=require" \
  backend/bin/juhe-ai-maintenance --ensure-schema --seed --dsn "$JUHE_AI_POSTGRES_URL"
# J3b 账户电路运行态索引（新库必跑一次）
sudo -u juhe backend/bin/juhe-ai-gateway -init-account-circuit-runtime-index
```

`/opt/juhe-ai-lite/shared/backend.env`（2026-10-24/25 存储收口后：PG + Redis 三连接串必填；模式开关与 `*_DATABASE_PATH` 路径族已退役，不要再写入）：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_ALLOWED_ORIGINS=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
JUHE_AI_SECRET=替换为至少32位稳定随机密钥
JUHE_AI_POSTGRES_URL=postgres://<user>:<password>@<host>:5432/juhe_ai?sslmode=require
JUHE_AI_REDIS_CACHE_URL=redis://:<password>@<host>:6379/0
JUHE_AI_REDIS_STATE_URL=redis://:<password>@<host>:6379/1
JUHE_AI_REDIS_NAMESPACE=juhe-ai:prod
JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://:<password>@<host>:6379/2
JUHE_AI_RUNTIME_LOG_POSTGRES_URL=postgres://<user>:<password>@<host>:5432/juhe_ai?sslmode=require&application_name=juhe-ai-jobs-runtime-log
JUHE_AI_LOG_DIR=./logs
JUHE_AI_DATA_DIR=./data
```

```bash
sudo tee /opt/juhe-ai-lite/bin/run.sh >/dev/null <<'EOF'
#!/usr/bin/env bash
set -e
export NODE_ENV=production
cd /opt/juhe-ai-lite/current
exec bash ./start.sh
EOF
sudo chmod +x /opt/juhe-ai-lite/bin/run.sh

sudo tee /etc/systemd/system/juhe-ai.service >/dev/null <<'EOF'
[Unit]
Description=Juhe AI
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/opt/juhe-ai-lite/current
ExecStart=/usr/bin/env bash /opt/juhe-ai-lite/bin/run.sh
Restart=always
RestartSec=5
User=juhe
Environment=NODE_ENV=production

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now juhe-ai
curl -i http://127.0.0.1:3000/__aisys__/health
curl -i http://127.0.0.1:3306/health
curl -i http://127.0.0.1:3305/health
sudo journalctl -u juhe-ai -n 100 --no-pager
```

gateway 业务健康（3000）与 gateway `3306/health`、jobs `3305/health` 应为正常响应；无 Key 访问 `/v1/models` 应 `401`。统计落库验证：发起一次真实 `/v1` 请求后 `SELECT count(*) FROM juhe_usage.usage_records;` 应 > 0；恒 0 行 = usage-record-spool 交接断裂，按 `docker/single-server/README.md`"运行时数据目录契约"检查 gateway/jobs 两侧目录是否同源。示例不构成生产切流授权。

后台代理：

```text
类型：socks5h
Host：127.0.0.1
端口：7890
```
