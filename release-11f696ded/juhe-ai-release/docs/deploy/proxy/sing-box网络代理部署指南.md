# sing-box 网络代理部署指南

> 本文只说明如何用 sing-box 提供本地 HTTP / SOCKS 混合代理端口，并把该端口接入 juhe-ai。订阅、节点来源、企业出口策略和上游代理凭据由用户自行提供，不写入仓库文档。

## 1. 为什么需要代理

juhe-ai 是上游 AI 账号中转服务。很多部署环境无法直连 OpenAI、Anthropic、Gemini、DeepSeek、GLM 或其他 OpenAI-compatible 上游时，必须让服务器的出站请求走可用代理，否则会出现账号测试失败、OAuth 刷新失败、网关请求超时或上游连接错误。

需要区分两类代理：

| 类型 | 作用 | 配置位置 |
| --- | --- | --- |
| 服务器网络代理 | 拉 Docker 镜像、装 npm 依赖、系统包更新 | Shell 环境变量、Docker daemon、系统代理 |
| juhe-ai 上游账号代理 | 账号测试、OAuth、网关请求上游模型 API | 后台“代理管理”并绑定到 AI 账户 |

## 2. 安装方式

sing-box 支持 Linux、Windows、macOS。具体包名和命令以官方文档为准：

- 官方包管理安装页：`https://sing-box.sagernet.org/installation/package-manager/`
- 官方客户端 / 图形界面说明：`https://sing-box.sagernet.org/clients/`
- 官方 Release：`https://github.com/SagerNet/sing-box/releases`

### Linux

Linux 服务器推荐使用官方软件源或下载对应架构 release 包。Debian / Ubuntu 可按官方 package manager 文档添加 SagerNet 软件源后安装：

```bash
sudo apt update
sudo apt install -y curl ca-certificates
# 按官方 package-manager 页面添加 sing-box 软件源后：
sudo apt install -y sing-box
sing-box version
```

如果发行版没有合适的软件源，下载 `linux-amd64`、`linux-arm64` 等对应架构压缩包，解压后把 `sing-box` 放入 `/usr/local/bin/`，再用 systemd 管理。

### macOS

macOS 可使用官方 macOS 包、图形客户端，或按官方 package manager 文档使用 Homebrew：

```bash
brew search sing-box
brew install sing-box
sing-box version
```

如果使用图形客户端，确保它实际监听本机代理端口，例如 `127.0.0.1:7890`。

### Windows

Windows 可使用官方 Windows release、图形客户端，或按官方 package manager 文档使用 winget / Scoop / Chocolatey。为避免包 ID 变化，先搜索再安装：

```powershell
winget search sing-box
# 根据搜索结果安装官方包 ID
winget install --id <官方包ID>
sing-box version
```

也可以从官方 Release 下载 Windows 压缩包，解压到固定目录，再把该目录加入 `PATH` 或用任务计划程序 / 服务工具托管。

## 3. 本地 mixed 入站

推荐让 sing-box 在本机提供一个 mixed 入站，HTTP 和 SOCKS 客户端都可以连：

```json
{
  "log": {
    "level": "info"
  },
  "inbounds": [
    {
      "type": "mixed",
      "tag": "mixed-in",
      "listen": "127.0.0.1",
      "listen_port": 7890,
      "sniff": true
    }
  ],
  "outbounds": [
    {
      "type": "direct",
      "tag": "direct"
    }
  ],
  "route": {
    "final": "direct"
  }
}
```

上面配置只展示本机入站结构；真实访问上游时，需要把你的订阅、企业代理或自建节点写成 outbound，并把 `route.final` 指向该 outbound。不要把订阅链接、节点密码或 token 写进仓库文档。

裸机同机部署优先监听 `127.0.0.1`。如果 juhe-ai 在 Docker 容器中，而 sing-box 跑在宿主机，需要让容器能访问宿主机代理：

- Windows / macOS Docker Desktop：后台代理 Host 通常填 `host.docker.internal`。
- Linux Docker Engine：给 Compose 增加 `extra_hosts: ["host.docker.internal:host-gateway"]`，或让 sing-box 监听宿主机内网 / bridge 可达地址。
- 监听 `0.0.0.0` 时必须用防火墙限制来源，禁止公网直接访问该代理端口。

## 4. Linux systemd 示例

配置文件建议放在 `/etc/sing-box/config.json`：

```bash
sudo install -d -m 755 /etc/sing-box
sudo nano /etc/sing-box/config.json
sing-box check -c /etc/sing-box/config.json
```

systemd 示例：

```ini
[Unit]
Description=sing-box
After=network.target

[Service]
Type=simple
ExecStart=/usr/bin/env sing-box run -c /etc/sing-box/config.json
Restart=always
RestartSec=5
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
```

启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now sing-box
sudo systemctl status sing-box
```

验证本机端口：

```bash
ss -lntp | grep ':7890 ' || true
curl -x socks5h://127.0.0.1:7890 https://api.openai.com/v1/models -I
```

## 5. macOS 常驻示例

如果用 Homebrew 安装，可以用 brew services 托管：

```bash
mkdir -p ~/.config/sing-box
nano ~/.config/sing-box/config.json
sing-box check -c ~/.config/sing-box/config.json
brew services start sing-box
brew services list | grep sing-box
```

如果使用图形客户端，确认它开机自启，并实际监听本机端口：

```bash
lsof -iTCP:7890 -sTCP:LISTEN || true
curl -x socks5h://127.0.0.1:7890 https://api.openai.com/v1/models -I
```

macOS 发布包部署时，juhe-ai 后台代理 Host 使用 `127.0.0.1`；Docker Desktop 容器访问宿主机 sing-box 时使用 `host.docker.internal`。

## 6. Windows 常驻示例

Windows 可以用图形客户端开机自启，也可以把官方 `sing-box.exe` 固定到目录后用任务计划程序或 NSSM 托管。

配置文件示例路径：

```powershell
New-Item -ItemType Directory -Force C:\sing-box | Out-Null
notepad C:\sing-box\config.json
sing-box check -c C:\sing-box\config.json
```

NSSM 示例：

```powershell
nssm install sing-box C:\sing-box\sing-box.exe "run -c C:\sing-box\config.json"
nssm set sing-box AppDirectory C:\sing-box
nssm set sing-box Start SERVICE_AUTO_START
nssm start sing-box
```

验证：

```powershell
netstat -ano | Select-String ':7890'
curl.exe -x socks5h://127.0.0.1:7890 https://api.openai.com/v1/models -I
```

Windows 发布包部署时，juhe-ai 后台代理 Host 使用 `127.0.0.1`；Docker Desktop 容器访问宿主机 sing-box 时使用 `host.docker.internal`。

## 7. 接入 juhe-ai

在 juhe-ai 后台进入“代理管理”，新增代理：

```text
名称：sing-box 本机代理
类型：socks5h
Host：127.0.0.1
端口：7890
用户名：留空，除非 sing-box 入站启用了认证
密码：留空，除非 sing-box 入站启用了认证
状态：启用
```

不同部署形态下 Host 选择：

| juhe-ai 运行位置 | sing-box 运行位置 | Host |
| --- | --- | --- |
| 同一台机器发布包运行 | 同一台机器 | `127.0.0.1` |
| Windows / macOS Docker Desktop 容器 | 宿主机 | `host.docker.internal` |
| Linux Docker 容器 | 宿主机 | `host.docker.internal` 加 `host-gateway`，或宿主机内网 / bridge IP |
| 应用服务器 | 独立代理服务器 | 代理服务器内网 IP |

保存后执行“测试代理”。测试通过后，把该代理绑定到需要走代理的 AI 账户。账号测试、OAuth 刷新和网关请求会按账号代理走对应出口。

## 8. OAuth 兜底代理

如果只有 OpenAI OAuth token 换取 / 刷新需要兜底代理，可以在 `backend/.env` 或 Docker `.env` 中配置：

```env
JUHE_AI_OAUTH_PROXY_URL=socks5h://127.0.0.1:7890
```

Docker 容器访问宿主机 sing-box 时示例：

```env
JUHE_AI_OAUTH_PROXY_URL=socks5h://host.docker.internal:7890
```

注意：该变量不是所有上游请求的全局代理。普通上游模型请求仍应通过后台代理绑定到账号。

## 9. 排障

- 后台代理测试失败：先在服务器上用 `curl -x socks5h://...` 验证代理端口是否可用。
- Docker 容器访问失败：确认容器里能解析并访问 Host；Linux 需要 `host-gateway` 或可达宿主机 IP。
- OAuth 可以刷新但网关请求仍失败：检查 AI 账户是否绑定代理，不能只配置 `JUHE_AI_OAUTH_PROXY_URL`。
- 代理端口误暴露公网：立即关闭监听或加防火墙，仅允许应用服务器访问。
- 上游仍超时：确认 sing-box outbound 真正走可用节点，且 DNS、IPv6、TLS 拦截和企业防火墙策略没有阻断。
