# macOS 部署指南

## 1. 推荐路径

| 场景 | 推荐方式 | 入口 |
| --- | --- | --- |
| 本机轻量部署 | 发布包 + `start.sh` | [部署指南](../部署指南.md) |
| 长期运行 | release 目录 + user `launchd` | 本文第 4 节 |
| Docker 部署 | Docker Desktop + Compose | [Docker 部署指南](../Docker部署指南.md) |
| 公网 HTTPS | 宿主机 Caddy | [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md) |
| 公网 Edge / 高并发隧道 | WireGuard + Caddy + launchd | [反向代理与高并发隧道部署指南](../反向代理与高并发隧道部署指南.md) |
| 常驻恢复 | `launchd`；外部探针默认只告警 | 本文第 7 节 |
| 上游代理 | sing-box + 后台代理绑定 | [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md) |

## 2. 部署前检查

```bash
sw_vers
uname -a
df -h .
node -v
node -p "process.version + ' LTS=' + (process.release.lts || '非LTS')"
corepack --version || true
pnpm -v || true
lsof -iTCP:3000 -sTCP:LISTEN || true
```

要求：

- Node.js 使用官方 LTS，当前支持 `22.x >= 22.13.0` 或 `24.x >= 24.11.0`。
- 生产只暴露 Caddy `80/443` 或 WireGuard 回源 listener；不要暴露 juhe-ai `3000`、sing-box `7890`、PostgreSQL 或 Redis。
- 远端 SSH、launchd 和手工预检必须使用同一条 Node LTS PATH。
- 发布包必须包含目标 macOS 架构的唯一 Go 二进制 `juhe-ai-go-sidecar`。历史 temporary release 曾验证三二进制拓扑，现已归档，不能外推到当前部署。当前候选槽只启动独立 Node 拓扑，并强制复用承流槽 sidecar；它不得创建第二个 F1/F2/F3 owner。任何候选记录都不替代 `current` 切换、反向代理/Edge、生产数据或真实流量验证。

## 3. 发布包目录

```text
~/juhe-ai-lite/
  current -> releases/某次发布/juhe-ai-release
  releases/
  shared/backend.env
  shared/data/
  backups/
  logs/
  bin/run.sh
  caddy-edge/
```

首次发布：

```bash
mkdir -p ~/juhe-ai-lite/releases/20260627 ~/juhe-ai-lite/shared/data ~/juhe-ai-lite/bin ~/juhe-ai-lite/logs
tar -xzf juhe-ai-release.tar.gz -C ~/juhe-ai-lite/releases/20260627
cd ~/juhe-ai-lite/releases/20260627/juhe-ai-release
cp -n backend/.env.example ~/juhe-ai-lite/shared/backend.env
rm -f backend/.env
ln -s "$HOME/juhe-ai-lite/shared/backend.env" backend/.env
rm -rf backend/data
ln -s "$HOME/juhe-ai-lite/shared/data" backend/data
ln -sfn "$HOME/juhe-ai-lite/releases/20260627/juhe-ai-release" "$HOME/juhe-ai-lite/current"
```

`~/juhe-ai-lite/shared/backend.env`：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_ALLOWED_ORIGINS=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
JUHE_AI_SECRET=替换为至少32位稳定随机密钥
# performance 模式下每个 Node control/gateway/worker 都要使用稳定且唯一的值。
JUHE_AI_INSTANCE_ID=juhe-ai-control-1
JUHE_AI_RUNTIME_LOG_INSTANCE_ID=juhe-ai-go-sidecar-runtime-log
JUHE_AI_TABLE_MONITOR_INSTANCE_ID=juhe-ai-go-sidecar-table-monitor
JUHE_AI_AUDIT_LOG_INSTANCE_ID=juhe-ai-go-sidecar-audit-log
JUHE_AI_AUDIT_LOG_STORE=sqlite
JUHE_AI_AUDIT_LOG_DATABASE_PATH=./data/juhe-ai-audit-log.sqlite3
JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=./data/audit-payload-blobs
JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=./data/audit-hot-search
JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS=127.0.0.1:3303
JUHE_AI_AUDIT_LOG_INPUT_URL=http://127.0.0.1:3303
JUHE_AI_AUDIT_LOG_INPUT_SECRET=替换为独立且至少32位的稳定随机密钥
```

`JUHE_AI_AUDIT_LOG_INPUT_SECRET` 不能复用或回退 `JUHE_AI_SECRET`。`start.sh` 启动唯一 Go sidecar，但不会生成 F1/F2/F3 owner ID 或 F3 secret；任一项缺失时必须失败，不得跳过 F3 继续运行。PostgreSQL performance 模式改用 `JUHE_AI_AUDIT_LOG_STORE=postgres`、优先的 `JUHE_AI_AUDIT_LOG_POSTGRES_URL`（未设置才回退 `JUHE_AI_POSTGRES_URL`）和独立 blob/hot-search 目录；仅 F3 需要的连接选项，例如服务端未启用 TLS 时的 `sslmode=disable`，必须写在前者，不能修改 Node 通用 URL。Node 对 F3 的一次性输入确认默认等待 `7000ms`，可用 `JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT_MS` 在 `1000..60000` 毫秒内调整，且不阻塞已完成的业务响应。

## 4. launchd 常驻

写入固定启动脚本：

```bash
cat > ~/juhe-ai-lite/bin/run.sh <<'EOF'
#!/usr/bin/env bash
set -e
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export NODE_ENV=production
cd "$HOME/juhe-ai-lite/current"
exec bash ./start.sh
EOF
chmod +x ~/juhe-ai-lite/bin/run.sh
```

写入用户级 LaunchAgent：

```bash
mkdir -p ~/Library/LaunchAgents
cat > ~/Library/LaunchAgents/com.juhe-ai.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.juhe-ai</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>$HOME/juhe-ai-lite/bin/run.sh</string>
  </array>
  <key>WorkingDirectory</key>
  <string>$HOME/juhe-ai-lite/current</string>
  <key>KeepAlive</key>
  <true/>
  <key>RunAtLoad</key>
  <true/>
  <key>SoftResourceLimits</key>
  <dict>
    <key>NumberOfFiles</key>
    <integer>65536</integer>
  </dict>
  <key>HardResourceLimits</key>
  <dict>
    <key>NumberOfFiles</key>
    <integer>131072</integer>
  </dict>
  <key>StandardOutPath</key>
  <string>$HOME/juhe-ai-lite/logs/launchd.out.log</string>
  <key>StandardErrorPath</key>
  <string>$HOME/juhe-ai-lite/logs/launchd.err.log</string>
</dict>
</plist>
EOF

launchctl bootout "gui/$UID" ~/Library/LaunchAgents/com.juhe-ai.plist 2>/dev/null || true
launchctl bootstrap "gui/$UID" ~/Library/LaunchAgents/com.juhe-ai.plist
launchctl kickstart -k "gui/$UID/com.juhe-ai"
```

执行 `bootout` 前先用 `plutil -lint` 验证新 plist，并准备一次性、有超时的恢复助手：旧 job 退出后由助手 bootstrap 已验证的 plist；发布脚本中断或新服务 health 失败时恢复旧 plist。恢复助手成功后立即清理，不作为长期 watchdog。Caddy 等长连接服务的 `bootout` 可能等待优雅退出，发布脚本不得无限等待。完整流程见 [反向代理与高并发隧道部署指南](../反向代理与高并发隧道部署指南.md)。

验证：

```bash
launchctl print "gui/$UID/com.juhe-ai" | head
curl -i http://127.0.0.1:3000/__aisys__/health
curl -i http://127.0.0.1:3000/__aisys__/api/health
curl -i http://127.0.0.1:3303/__aiinternal__/health
pgrep -af 'juhe-ai-go-sidecar' || true
```

前三项的预期是 Node health 为 `200`、F3 health 为 `204`、唯一 Go sidecar 来自当前 release。仍须通过 Node 只读 `/runtime-logs`、`/table-monitor` 确认 F1/F2 新鲜度，并通过一次真实审计输入后的管理端详情读回确认 Node -> F3 -> Node；不能只凭 launchd loaded 或 Node health 继续切流。

升级只切 `current` 并重启：

```bash
ln -sfn "$HOME/juhe-ai-lite/releases/新版本/juhe-ai-release" "$HOME/juhe-ai-lite/current"
launchctl kickstart -k "gui/$UID/com.juhe-ai"
sleep 3
launchctl print "gui/$UID/com.juhe-ai" | head
lsof -nP -iTCP:3000 -sTCP:LISTEN
pgrep -af 'node|juhe-ai' | grep 'juhe-ai-lite' || true
```

主进程会看护 DB service 和 worker；不要把 `worker.js` 或 `db-service.js` 单独注册为 launchd 服务。升级后必须确认只有当前 release 的主进程监听 `3000`，没有旧 release 路径下的 node 子进程残留。持续 SQLite `database is locked`、PG 连接数异常或管理接口忽快忽慢时，先查旧子进程残留和端口监听，再查数据库。

### 4.1 高性能模式 Redis 三角色

macOS 裸机高性能模式固定使用三个物理进程：

| 范围 | cache | state | queue |
| --- | --- | --- | --- |
| main | `127.0.0.1:6379`，无 AOF/RDB，`allkeys-lru` | `127.0.0.1:6380`，无 AOF/RDB，`noeviction` | `127.0.0.1:6381`，AOF everysec、无 RDB、`noeviction` |
| temporary | `127.0.0.1:16379` | `127.0.0.1:16380` | `127.0.0.1:16381` |

使用 [macOS 运维脚本](operations/README.md) 中的 `install-redis-role-services.sh` 和 `verify-redis-role-isolation.sh`。安装器默认 dry-run；apply 只能在临时服务接管、已留存 plist/config 哈希与恢复副本后执行。temporary 的三个 PID、PostgreSQL 数据库和 namespace 必须都与 main 不同，不能复制 main env 后只换 namespace。

queue 迁移前先建立 token fence，再用 `backend/dist/scripts/operations/drain-redis-streams.js` 独立排空 usage、audit、operation log、public API log 和 record maintenance 五条 Stream。普通运行日志不进入 Redis，必须单独确认角色 JSONL 文件 backlog 已由唯一 Go sidecar 内 F1 追平并且 cursor/freshness 正常。切换到 `6381` 后它成为新的队列事实源，失败时不得直接把 URL 改回旧 `6380`；state 改为无持久化必须在 queue 连续性验证之后单独执行。

### 4.2 高性能模式单一 Go sidecar

`install-performance-topology.sh` 只安装一个 `juhe-ai-go-sidecar` launchd 服务，服务内承载 F1、F2、F3。dry-run/apply 都要求 release 内唯一 Go 二进制是可执行常规文件；system scope 也只验证该二进制的读取与执行权限。F1/F2/F3 仍保留独立 Store、schema 和 owner lease，稳定 ID 由 `--instance-id-prefix` 加固定组件名生成；它们不是三个 launchd 服务。F2 固定 PostgreSQL 模式，优先使用 `JUHE_AI_TABLE_MONITOR_POSTGRES_URL`，否则只继承 `JUHE_AI_POSTGRES_URL`，两者均缺失即失败，不会回退 SQLite、Redis、Node worker 或 queue。

发布包内的运维脚本按文档一律用 `bash docs/deploy/macos/operations/<脚本>.sh ...` 调用；不要假设这些文档脚本本身带可执行位并直接以 `./<脚本>.sh` 运行。`start.sh` 与唯一 Go sidecar 二进制才是发布包必须具备执行权限的运行入口。

system scope 的 `--apply` 必须由 `sudo` 执行；Node 与唯一 Go sidecar 均以 `--service-user` 指定的非 root 用户运行。目标机完成 `pnpm install --prod` 后，release 必须对服务用户递归可读/可执行、但不可写；安装器会拒绝服务用户可写的 release。运行目录、日志和 spool 才可以交给服务用户写入，不能用递归 `chown` 或放宽 release 权限来解决启动失败。

F3 同样固定 PostgreSQL 模式，稳定实例 ID 为 `<instance-id-prefix>-audit-log`。它从 release 的 `backend/.env`（或 launchd 明确环境）读取优先的 `JUHE_AI_AUDIT_LOG_POSTGRES_URL`（未设置才读取 `JUHE_AI_POSTGRES_URL`）、可选的 `JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL`、`JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS`、同一地址的 `JUHE_AI_AUDIT_LOG_INPUT_URL`、独立的 `JUHE_AI_AUDIT_LOG_INPUT_SECRET` 和可选的 `JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT_MS`；缺失、非 loopback、URL 与监听地址不一致或短密钥都会失败。F3 不会回退 `JUHE_AI_SECRET`、SQLite、Redis、Node worker 或旧审计队列。Node service 与 sidecar 统一使用运行槽位的 `$DATA_DIR/audit/blobs` 和 `$DATA_DIR/audit/hot-search`，避免 payload/hot-search 写读目录漂移。

脚本先连续通过 gateway/control 的 `/__aisys__/health` 和 `/__aisys__/api/health`（后者是 Node DB-service readiness），再启动并验证唯一 Go sidecar；F3 必须连续返回 loopback `GET /__aiinternal__/health` 的 `204`。失败时恢复 Go sidecar、Node 服务、Nginx 的原 plist、run script 和 loaded 状态。F2/F3 的 liveness 都不等价于数据完成：生产 cutover 前必须通过 Node 只读 API 人工核对 F1 日志新鲜度、F2 owner lease/snapshot freshness，并以真实可审计请求完成 Node -> F3 -> Node 详情读回。不得在脚本中读取 PostgreSQL 凭据、直接查询数据库或把存活误报为数据完成。

2026-08-11 已完成目标 Mac 上隔离 temporary release 的直接 `start.sh` 预演：空的可销毁 PostgreSQL 库先安装 release 生产依赖并执行 `node ./backend/dist/scripts/maintenance/init-postgres-schema.js`，随后 Node 两个 health、F3 `204`、F1/F2 Node readback、F3 lifecycle/payload Node readback、真实网关审计捕获和无效 HMAC `401` 均通过。预演未改动 `current`、launchd、Nginx、Caddy、Edge、生产库或 Redis，结束后已清理临时目录、进程、监听和测试库。Mac 默认 Node 路径若不是受支持的 22.x/24.x LTS，必须在 launchd 和手工命令中显式使用同一条受支持 PATH。

2026-08-11 的隔离 temporary 服务空间曾验证旧三 sidecar 拓扑；它已经归档，不能外推到当前单一 sidecar。当前候选槽必须复用承流槽的 Go sidecar，不能创建第二个 F1/F2/F3 data owner。候选 Node 只完成业务验证，随后通过 handover controller 原子切流；它仍不是 `current` 切换、Nginx/Caddy/Edge 变更、生产数据验证或真实流量演练。

旧归档中的三二进制 dry-run 证据仅代表历史输入，不能作为当前版本的构建或部署证明。正式候选必须从最终冻结 commit 生成一个 `juhe-ai-go-sidecar`、记录 archive SHA-256，并重新完成候选 Node 验证、Node -> F3 -> Node 读回、稳定观察和 handover rollback 演练。

### 4.3 生产升级与无感切流

生产升级不再通过“切换 `current` 后重启 active 服务”完成。该做法会在同端口、同 label 上停止现有进程，无法提供零停机证明。

1. 从冻结 commit 构建不可变 release，核对 archive SHA-256、`RELEASE_SOURCE_COMMIT`、前端 `buildId` 和目标架构。
2. 使用独立 candidate Node runtime、label、端口、Nginx slot include、upstream suffix、Node instance ID 与 Redis 身份执行 system `--apply`，同时传入 `--go-sidecar-mode reuse --audit-input-port <正式 owner F3 loopback端口>` 复用正式槽唯一 sidecar；candidate 不得创建 F1/F2/F3 的第二个 owner。reuse apply 若发现该 candidate label 的 Go sidecar job、plist 或 run script 残留会直接拒绝，必须人工确认后清理，禁止自动停止未知 owner。system scope 必须同时传入 `--nginx-config <slot include>` 与 `--nginx-main-config <main config>`，两者不得相同。
3. 在 candidate 直连入口完成 Node 双 health、三个 gateway、worker/PID、F1/F2 freshness、F3 HMAC/读回、网关请求、登录态管理页和业务 API 验证。浏览器必须实际进入至少一个依赖 API 的页面；只加载 `index.html` 或得到 health `200` 不算通过。
4. 高性能拓扑只使用 `performance-handover-controller.sh` 执行 `preflight -> takeover`。先在旧槽承流时完成 candidate 全部慢验证；`preflight` 将两槽与当前入口合并观察并生成默认 300 秒有效的文件指纹凭证，`takeover` 复用该证据，只做一次实时身份复核、原子 route 切换和一轮合并稳定观察。保存 committed journal、route identity、access-log 增量和稳定窗口证据。`temporary-cutover.sh` 不适用于多 gateway 生产；完整顺序见 [生产发布快速流程](../生产发布快速流程.md)。
5. 切流后保留旧槽。稳定观察通过后才更新 `current` 指针并清理旧槽。若 takeover 尚未 committed 而中断，按 journal 执行 `recover`；若已经 committed 后业务页或稳定窗口失败，重新对旧槽执行反向 `preflight`，再执行 `switchback`。新槽已宕机时使用显式 `preflight --degraded-source`，它只证明旧槽回切目标健康且当前 route 仍指向 committed 新槽，不要求故障源恢复；随后执行 `switchback`。`stable` 不是可执行命令。不得在未知状态下杀进程或原地重装。

2026-08-12 的首次 F3 正式上线发生过硬停机，且前端构建把根相对 API base 错误转换成 Windows 磁盘路径，导致静态页面和 health 正常但浏览器 API 全部失败。该事故证明“HTTP `200`”不足以验收发布；后续必须同时执行构建产物 API-base 扫描和真实浏览器登录态业务页验证。此记录不表示零停机流程已经在生产证明，下一次发布必须先完成完整 candidate/handover 演练。

## 5. HTTPS 和端口边界

macOS 生产建议宿主机 Caddy 监听 `80/443`，反向代理到 `127.0.0.1:3000`。完整 Caddyfile 见 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。

边界：

- 家庭路由器或云安全组只转发 `80/443` 到 Caddy。
- juhe-ai 绑定 `127.0.0.1:3000`。
- sing-box mixed / socks 端口只监听本机或受信内网。
- macOS Application Firewall 不是精确端口防火墙；端口边界优先靠绑定地址、路由器映射、Caddy 和 WireGuard peer 白名单。

真实客户端 IP 需要 `JUHE_AI_TRUST_PROXY=true`，且后端端口不能被客户端绕过反代直连。多层 Edge / WireGuard 见 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md) 的真实 IP 段落。

公网 Edge + WireGuard 的高并发模式不直接把 Caddy 固定到 loopback：Caddy 的回源 listener 绑定精确 WireGuard 地址，juhe-ai 仍绑定 `127.0.0.1`。模块、PROXY v2 和切换方式见 [反向代理与高并发隧道部署指南](../反向代理与高并发隧道部署指南.md)。

高并发长连接的 macOS 内核起步值是 `kern.ipc.somaxconn=4096`、`kern.maxfiles=524288`、`kern.maxfilesperproc=131072`。使用 root LaunchDaemon 持久设置并在重启后复核；Caddy、Nginx、Node 和 WireGuard 的 plist 还要分别设置 `65536/131072` 文件句柄软硬上限。plist 和 sysctl 只代表目标配置，必须受控重启后验证 live PID 实际值。

macOS Application Firewall 必须保持开启。自定义或 xcaddy 构建的 Caddy 二进制替换后，先备份并执行本机 ad-hoc 签名，再将同一绝对路径加入防火墙允许列表：

```bash
sudo codesign --force --deep --sign - /usr/local/bin/caddy-l4
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --add /usr/local/bin/caddy-l4
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --unblockapp /usr/local/bin/caddy-l4
sudo /usr/libexec/ApplicationFirewall/socketfilterfw --getappblocked /usr/local/bin/caddy-l4
```

签名或替换二进制后必须重新验证版本、模块、SHA-256、每个 WireGuard listener、TLS、PROXY v2 和长连接；不允许用关闭 Application Firewall 规避 RST。

## 6. Docker Desktop 差异

Docker 容器访问宿主机 sing-box：

```text
类型：socks5h
Host：host.docker.internal
端口：7890
```

宿主机 Caddy 反代 Docker 容器时，`docker/.env`：

```env
JUHE_AI_PUBLIC_BIND=127.0.0.1
JUHE_AI_PUBLIC_PORT=3000
JUHE_AI_PUBLIC_ORIGIN=https://ai.example.com
JUHE_AI_COOKIE_SECURE=true
JUHE_AI_TRUST_PROXY=true
```

## 7. 状态检测和恢复

长期运行默认只由 launchd `KeepAlive` 维护主服务；主进程继续看护 DB service 和 worker。默认不部署会终止 server 的外部 watchdog，避免与发布和受控切换冲突。

外部 health 探针可以只告警。公网失败但本机 health 正常时，先检查 Edge、WireGuard、Caddy、证书和 Nginx，不重启主服务。确有无人值守自动恢复需求时，再按 [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md) 单独评审。

## 8. 上游网络代理

临时接管发布时，候选服务必须与正式服务保持相同 API 行为；不得设置或依赖 `JUHE_AI_SYSTEM_API_READ_ONLY`，也不得通过 HTTP 方法拦截 System、Public、网关或内部接口。临时数据库的数据同步属于后续独立方案，不能以接口禁写替代。

裸机同机 sing-box：

```text
类型：socks5h
Host：127.0.0.1
端口：7890
```

`JUHE_AI_OAUTH_PROXY_URL` 只作为 OpenAI OAuth token 换取 / 刷新的兜底代理；普通上游请求优先使用账号绑定代理。
