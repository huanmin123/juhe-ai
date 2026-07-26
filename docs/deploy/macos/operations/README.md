# macOS 运维脚本

本目录随 `docs/deploy/` 进入发布包，提供不包含真实环境信息的 macOS 运维模板。所有变更脚本默认 `--dry-run`；只有显式传入 `--apply` 才会修改 launchd 或入口路由。

## 文件

- `install-launchd-service.sh`：生成固定 `bin/run.sh`，安装 user LaunchAgent 或 system LaunchDaemon，只守护 juhe-ai 主进程并启用 `KeepAlive`；更新已有服务失败时恢复原 `run.sh`、plist 和原 loaded 状态。
- `install-performance-topology.sh`：仅用于高性能模式，在同一台 macOS 上安装一个 control、默认 3 个 gateway，以及由 control 看护的 Usage 2 / Log 2 / Stats 1 / Ops 1 worker；逐节点健康后才原子切换已被 Nginx 主配置 include 的配置文件，失败恢复 launchd 和 Nginx。
- `verify-performance-topology.sh`：跨 launchd、PID/cwd、端口、进程树、worker 健康载荷和候选入口响应头连续采样，确认临时槽没有进程漂移或角色串线。
- `switch-performance-slot.sh`：本次大版本升级使用的前向切换器；切换前严格验证候选拓扑和当前入口标签，Nginx reload 提交后不回切旧槽，后置验证失败会保留候选入口并输出 `FORWARD_FIX_REQUIRED`。
- `manage-sing-box.sh`：只接管已证明为 loopback、唯一且由 `sing-box` 持有的监听，并通过实际 SOCKS5 代理探测；也可显式选择 Homebrew service 或 user launchd。
- `diagnose-proxy-dns.sh`：只读检查 DNS、监听端口、launchd 状态和直连/代理连通性。
- `temporary-cutover.sh`：在已经准备好的主服务与临时服务之间调用环境私有 switch adapter，切换失败自动回滚入口。
- `install-redis-role-services.sh`：默认 dry-run，按 cache/state/queue 角色渲染独立 Redis 配置与 system LaunchDaemon；apply 使用 bootout、端口释放、原子替换、bootstrap、kickstart 和失败恢复。
- `verify-redis-role-isolation.sh`：只读验证 main `6379/6380/6381` 或 temporary `16379/16380/16381` 的三个 URL、PID、launchd job、PING、AOF/RDB 和淘汰策略，不输出密码。
- `templates/`：无用户、域名、IP、密钥或生产路径的 plist 模板。

## 安全边界

- 外部 HTTP watchdog 已退役，不提供安装、恢复或启动脚本。主进程退出由 launchd `KeepAlive` 拉起，DB service 和 worker 继续由主进程 supervisor 管理。
- 真实路径、label、用户、入口域名、端口、代理订阅和凭据由部署人员通过参数或服务器私有配置提供，不写入仓库。
- 高性能拓扑脚本不会安装 Nginx，也不会修改 Nginx 主配置；`--nginx-config` 必须是主配置已 include 的绝对路径。默认仅 dry-run，`--apply` 要求构建产物、共享 `backend/.env`、Node、launchd、Nginx 和所有目标端口均可用。
- 高性能拓扑生成的运行脚本会显式设置 `JUHE_AI_AUDIT_LOG_ENABLED=true` 和 `JUHE_AI_RUNTIME_LOG_INDEX_ENABLED=true`。临时槽和正式入口都必须分别证明审计日志有新写入、日志搜索的索引查询能命中刚生成的 traceId。
- `switch-performance-slot.sh` 不创建完整数据库快照，也不保留旧数据库回滚点。业务数据应先单独导出并在新库完成向前兼容升级和逐表校验；日志、用量、统计等非业务表在候选库重建。
- `install-launchd-service.sh --apply` 必须显式传 `--health-port` 或 loopback `--health-base-url`；加载后在有界窗口内连续确认 `/__aisys__/health` 与 `/__aisys__/api/health`，失败会恢复旧定义和 loaded 状态。
- `manage-sing-box.sh` 的 `launchd` 更新在 bootstrap、kickstart、监听身份或代理探测失败时恢复旧 plist 与原 loaded 状态；`existing` 不会因为任意进程占用端口就接管。
- `temporary-cutover.sh` 不复制数据库、不生成临时 env、不停止主服务或临时服务。环境私有流程先完成资源隔离与候选启动，再使用本脚本切流；切流成功后也保留源服务，确认稳定后才显式清理。
- Redis 角色安装器不接受共享 host:port，不执行运行时参数热改；持久化和角色变化必须通过配置文件与 launchd 有界替换。temporary 必须使用独立三实例，不能只换 namespace 后复用生产 PID。
- 旧的 `temporary-cutover.sh` 仍是通用双槽回切模板，但不用于本次前向数据库升级。生产执行必须调用 `switch-performance-slot.sh`；在 Nginx reload 成功前只允许恢复本次尚未提交的配置文件，在 reload 成功后禁止切回旧 release 或旧数据库，异常直接修复候选槽。

## 静态验证

在仓库根目录执行：

```powershell
pnpm test:macos-operations
pnpm test:redis-role-operations
```

该门禁会检查敏感生产常量、只读诊断边界、代理监听/进程/探测身份、PID/cwd/端口/双 health 身份、适配器部分失败回滚、launchd 安装失败恢复、plist `KeepAlive`、shell 语法和 dry-run；在 Darwin/Linux + gcc 环境还会执行临时原生命令桩的 bootstrap/kickstart/监听/探测失败回滚模拟。

## 示例

先查看计划：

```bash
bash ./install-launchd-service.sh --dry-run \
  --scope user \
  --base-dir "$HOME/juhe-ai-lite" \
  --label com.example.juhe-ai \
  --health-port 3000

bash ./install-performance-topology.sh --dry-run \
  --scope user \
  --base-dir "$HOME/juhe-ai-lite" \
  --label-prefix com.example.juhe-ai.performance \
  --control-port 3200 \
  --gateway-base-port 3101 \
  --gateway-count 3 \
  --usage-workers 2 \
  --log-workers 2 \
  --ingress-port 3000 \
  --nginx-config /opt/homebrew/etc/nginx/servers/juhe-ai-performance.conf \
  --nginx-bin /opt/homebrew/bin/nginx \
  --nginx-main-config /opt/homebrew/etc/nginx/nginx.conf

bash ./verify-performance-topology.sh --apply \
  --release "$HOME/juhe-ai-lite/releases/<commit>/juhe-ai-release" \
  --scope user \
  --label-prefix com.example.juhe-ai.performance \
  --control-port 3200 --gateway-base-port 3101 --gateway-count 3 \
  --usage-workers 2 --log-workers 2 --ingress-port 3000 --samples 3

bash ./manage-sing-box.sh existing --dry-run \
  --config "$HOME/.config/sing-box/config.json" \
  --probe-url 'https://api.openai.com/'

bash ./diagnose-proxy-dns.sh \
  --proxy-url socks5h://127.0.0.1:7890 \
  --target-host api.openai.com
```

临时接管前，主服务和临时服务都必须已经独立健康，且 switch adapter 接受 `main` / `temporary` 参数：

```bash
bash ./temporary-cutover.sh --action takeover --dry-run \
  --switch-script "$HOME/juhe-ai-lite/bin/switch-upstream.sh" \
  --main-release "$HOME/juhe-ai-lite/current" --main-pid '<主PID>' --main-port '<主端口>' \
  --temporary-release "$HOME/juhe-ai-lite/temporary/current" --temporary-pid '<临时PID>' --temporary-port '<临时端口>' \
  --ingress-health-url 'https://你的入口域名/__aisys__/health'
```

审核 dry-run 后，把 `--dry-run` 改为 `--apply`。回切使用相同参数，只把 `--action` 改为 `switchback`。任何切换失败时脚本会调用相反目标回滚；如果回滚入口无法证明，脚本保留两套服务并要求人工处理，不自动杀进程。
