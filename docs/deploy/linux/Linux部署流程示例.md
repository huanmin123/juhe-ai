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
```

后台代理：

```text
类型：socks5h
Host：127.0.0.1
端口：7890
```
