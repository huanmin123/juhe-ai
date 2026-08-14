# Linux 部署流程示例

## 目标

- 路径：`/opt/juhe-ai-lite/current`
- 入口：宿主机 Caddy `https://ai.example.com`
- 常驻：`systemd`
- 上游代理：同机 sing-box `socks5h://127.0.0.1:7890`

## 步骤

```bash
sudo useradd --system --home /opt/juhe-ai-lite --shell /usr/sbin/nologin juhe || true
sudo install -d -o juhe -g juhe /opt/juhe-ai-lite/releases/20260627 /opt/juhe-ai-lite/shared/data /opt/juhe-ai-lite/bin /opt/juhe-ai-lite/logs
sudo -u juhe tar -xzf juhe-ai-release.tar.gz -C /opt/juhe-ai-lite/releases/20260627
cd /opt/juhe-ai-lite/releases/20260627/juhe-ai-release
sudo -u juhe cp -n backend/.env.example /opt/juhe-ai-lite/shared/backend.env
sudo -u juhe ln -sfn /opt/juhe-ai-lite/shared/backend.env backend/.env
sudo -u juhe rm -rf backend/data
sudo -u juhe ln -sfn /opt/juhe-ai-lite/shared/data backend/data
sudo ln -sfn /opt/juhe-ai-lite/releases/20260627/juhe-ai-release /opt/juhe-ai-lite/current
```

`/opt/juhe-ai-lite/shared/backend.env`：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_ALLOWED_ORIGINS=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
JUHE_AI_SECRET=替换为至少32位稳定随机密钥
JUHE_AI_RUNTIME_LOG_INSTANCE_ID=juhe-ai-go-jobs-runtime-log
JUHE_AI_TABLE_MONITOR_INSTANCE_ID=juhe-ai-go-jobs-table-monitor
JUHE_AI_AUDIT_LOG_INSTANCE_ID=juhe-ai-go-gateway-audit-log
JUHE_AI_AUDIT_LOG_STORE=sqlite
JUHE_AI_AUDIT_LOG_DATABASE_PATH=./data/juhe-ai-audit-log.sqlite3
JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=./data/audit-payload-blobs
JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=./data/audit-hot-search
JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS=127.0.0.1:3303
JUHE_AI_AUDIT_LOG_INPUT_URL=http://127.0.0.1:3303
JUHE_AI_AUDIT_LOG_INPUT_SECRET=替换为独立且至少32位的稳定随机密钥
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
curl -i http://127.0.0.1:3000/__aisys__/api/health
curl -i http://127.0.0.1:3303/__aiinternal__/health
sudo journalctl -u juhe-ai -n 100 --no-pager
```

Node 两个 health 应为 `200`，F3 health 应为 `204`。routine upgrade 再检查无 Key gateway `401` 和 sidecar 启动日志；F1/F2 新鲜度和 Node -> F3 -> Node 审计读回只在首次部署、owner/存储变更、故障或回切时执行。示例不构成生产切流授权。

后台代理：

```text
类型：socks5h
Host：127.0.0.1
端口：7890
```
