# macOS 运维脚本

本目录随 `docs/deploy/` 进入发布包，提供不包含真实环境信息的 macOS 运维模板。所有变更脚本默认 `--dry-run`；只有显式传入 `--apply` 才会修改 launchd 或入口路由。

## 文件

- `install-launchd-service.sh`：生成固定 `bin/run.sh`，安装 user LaunchAgent 或 system LaunchDaemon，只守护 juhe-ai 主进程并启用 `KeepAlive`；更新已有服务失败时恢复原 `run.sh`、plist 和原 loaded 状态。
- `install-performance-topology.sh`：仅用于高性能模式，在同一台 macOS 上安装一个 control、默认 3 个 gateway，以及由 control 看护的 Usage 2 / Log 2 / Stats 1 / Ops 1 worker；`--release-dir` 可将进程固定到不可变候选 release，`--nginx-config-kind main` 可原子替换并重载现有自定义 Nginx 主配置，`--nginx-main-config` 也支持只切换被主配置 include 的片段，system scope 必须显式提供非 root 的 service user/group。逐节点健康后，使用 mode `0600` API key 文件直探每个候选 Gateway 的 `/v1/models`，切流后再探 loopback Nginx ingress；失败时只有旧 ingress 路由身份和模型 readiness 都重新证明后才 bootout 候选，否则保留候选与恢复文件，以双槽状态 fail-closed。
- `wait-performance-slot-drain.sh`：切流后只读检查 inactive slot 的 control/gateway 端口是否仍有 Nginx 已建立连接；必须明确证明 ingress 正在另一槽、curl 可查询且 lsof 自检成功，`--check` 和 `--wait` 都要求连续 3 个零连接样本。AI 长流未排空或探测能力不确定时保持当前活动槽，禁止升级或清理旧槽。
- `retire-performance-slot.sh`：仅在 slot 已摘流且稳定排空、launchctl domain 与全部 job 状态均可证明后退役该槽；任一 bootout 或状态验证失败都会恢复已停止 job 并保留恢复文件，不删除 release、日志或共享 usage spool。

高性能生产发布固定使用 `main` / `temporary` 双槽：先把候选安装到 inactive slot 并原子切流，等待旧槽长连接排空，再升级旧槽并切回；最后再次排空并退役 temporary。两个槽使用独立端口、launchd label、run script 和实例 ID，3099 始终只把新请求发给一个活动槽。任何排空超时都不强杀 AI 流，保持当前健康槽继续服务并延期后续步骤。

两槽及全部 control/gateway/worker 角色必须共享 `JUHE_AI_AUDIT_BLOB_ROOT=$BASE_DIR/shared/audit/blobs`。审计元数据已经共享 PostgreSQL/队列时，blob 不能落到 release 本地目录，否则切槽后会出现元数据可见但正文文件丢失。

- `manage-sing-box.sh`：只接管已证明为 loopback、唯一且由 `sing-box` 持有的监听，并通过实际 SOCKS5 代理探测；也可显式选择 Homebrew service 或 user launchd。
- `diagnose-proxy-dns.sh`：只读检查 DNS、监听端口、launchd 状态和直连/代理连通性。
- `temporary-cutover.sh`：在已经准备好的主服务与临时服务之间调用环境私有 switch adapter；显式传入新 release 的 readiness runner，并由同一个新 runner 探测 main/temporary 直连、旧入口和新入口全部 URL，因此旧 release 可以没有 runner。切换失败自动复用既有入口回滚。
- `install-redis-role-services.sh`：默认 dry-run，按 cache/state/queue 角色渲染独立 Redis 配置与 system LaunchDaemon；apply 使用 bootout、端口释放、原子替换、bootstrap、kickstart 和失败恢复。
- `verify-redis-role-isolation.sh`：只读验证 main `6379/6380/6381` 或 temporary `16379/16380/16381` 的三个 URL、PID、launchd job、PING、AOF/RDB 和淘汰策略，不输出密码。
- `templates/`：无用户、域名、IP、密钥或生产路径的 plist 模板。

## 安全边界

- 外部 HTTP watchdog 已退役，不提供安装、恢复或启动脚本。主进程退出由 launchd `KeepAlive` 拉起，DB service 和 worker 继续由主进程 supervisor 管理。
- 真实路径、label、用户、入口域名、端口、代理订阅和凭据由部署人员通过参数或服务器私有配置提供，不写入仓库。
- `install-launchd-service.sh --apply` 必须显式传 `--health-port` 或 loopback `--health-base-url`；加载后在有界窗口内连续确认 `/__aisys__/health` 与 `/__aisys__/api/health`，失败会恢复旧定义和 loaded 状态。
- `manage-sing-box.sh` 的 `launchd` 更新在 bootstrap、kickstart、监听身份或代理探测失败时恢复旧 plist 与原 loaded 状态；`existing` 不会因为任意进程占用端口就接管。
- `temporary-cutover.sh` 不复制数据库、不生成临时 env、不停止主服务或临时服务。环境私有流程先完成资源隔离与候选启动，再使用本脚本切流；切流成功后也保留源服务，确认稳定后才显式清理。
- Redis 角色安装器不接受共享 host:port，不执行运行时参数热改；持久化和角色变化必须通过配置文件与 launchd 有界替换。temporary 必须使用独立三实例，不能只换 namespace 后复用生产 PID。
- apply 前必须保留当前可用入口和回滚目标，并先证明回滚目标当前确实是入口。主服务和临时服务必须使用不同 PID、端口和实际 release 目录，并同时通过 `/__aisys__/health`、`/__aisys__/api/health`。入口证明依赖 switch adapter 写入的响应头，不能只凭某个 health 返回 200 放行。
- 调用 switch adapter 前就会启用失败 trap；即使适配器已经部分改动入口后以非零状态退出，也会调用相反目标执行反向回滚并重新证明入口。
- installer 自动回滚不会把“旧 Nginx 配置文件已放回”当成恢复成功。只有 reload 成功、响应头明确指向另一槽且该 ingress 的 `/v1/models` readiness 成功，才允许 bootout 候选；任何一步无法证明都必须保留候选进程、plist/run script 和备份，人工确认入口后再处理。
- 业务发布必须显式传入 `--model-readiness-key-file`。文件只保存一行可调用 `/v1/models` 的本地 API key，权限必须为 `0600`；脚本和 runner 不输出 key。`--skip-model-readiness-for-non-business-test` 只允许无业务数据的脚本测试，不能用于生产候选或真实切流。
- 模型目录只在首次空库初始化或真实模型事实产生 dirty generation 时重建。首次部署执行 `pnpm --filter juhe-ai-backend maintenance:ensure-published-model-catalog`：已有全局 OpenAI 持久快照时严格 no-op，缺失时才初始化一次。普通滚动发布不得调用无条件 rebuild 命令，也不得增加周期调用。

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
  --ingress-health-url 'https://你的入口域名/__aisys__/health' \
  --main-header-value performance-main --temporary-header-value performance-temporary \
  --model-readiness-key-file "$HOME/juhe-ai-lite/secrets/readiness-api-key" \
  --model-readiness-runner "$HOME/juhe-ai-lite/temporary/current/backend/dist/scripts/operations/model-catalog-readiness.js" \
  --main-model-base-url 'http://127.0.0.1:3211' \
  --temporary-model-base-url 'http://127.0.0.1:3311' \
  --model-readiness-ingress-base-url 'http://127.0.0.1:3099'
```

审核 dry-run 后，把 `--dry-run` 改为 `--apply`。回切时把 `--action` 改为 `switchback`，并把 `--model-readiness-runner` 更新为已经升级完成的 main release runner；三类 readiness base URL 必须是 `http://127.0.0.1:PORT`。任何切换失败时脚本会调用相反目标回滚；如果回滚入口无法证明，脚本保留两套服务并要求人工处理，不自动杀进程。
