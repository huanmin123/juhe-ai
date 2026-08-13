# macOS 运维脚本

本目录随 `docs/deploy/` 进入发布包，提供不包含真实环境信息的 macOS 运维模板。所有变更脚本默认 `--dry-run`；只有显式传入 `--apply` 才会修改 launchd 或入口路由。

## 文件

- `install-launchd-service.sh`：生成固定 `bin/run.sh`，安装 user LaunchAgent 或 system LaunchDaemon，只守护 juhe-ai 主进程并启用 `KeepAlive`；更新已有服务失败时恢复原 `run.sh`、plist 和原 loaded 状态。
- `install-performance-topology.sh`：仅用于高性能模式，在同一台 macOS 上安装一个 `juhe-ai-go-sidecar` LaunchDaemon/LaunchAgent、一个主 control、默认 3 个 gateway，以及由主 control 看护的 Usage 2 / Log 2 / Stats 1 / Ops 1 worker；`--control-count 2` 时第二个是只读管理副本，不启动 worker，管理写入和内部派发仍固定主 control。单一 sidecar 在同一 PID 中承载 F1、F2、F3、F4，各功能仍拥有独立 Store、schema 与 owner lease。它在 Node `/__aisys__/api/health` 的 DB-service readiness 通过后启动，并连续验证 F3 `GET /__aiinternal__/health`、F4 `GET /__aiinternal__/v1/operation-logs/health` 均为 `204`。高性能候选槽必须明确 `--go-sidecar-mode reuse --audit-input-port <正式 F3 owner 端口> --operation-log-input-port <正式 F4 owner 端口>`，复用唯一 owner，禁止再启动第二个 Go 数据 writer。普通组件错误、lease 暂失、panic 或异常返回只记录并退避重启该组件；进程级故障才由 launchd 重启 sidecar。候选 Node 读取正式 owner 的审计 blob/hot-search 目录。生产切流前仍须通过 Node 只读 API 确认 F1/F2 新鲜度，并以真实可审计请求确认 Node -> F3/F4 -> Node 详情读回。逐节点健康、Redis freshness fence 和 PID 拓扑门禁通过后才原子切换已被 Nginx 主配置 include 的配置文件；失败恢复受本次 owner/reuse 模式管理的 launchd、run script 和 Nginx。历史三程序预演不构成当前单 sidecar 发布证明，正式候选必须从最终冻结 release 重做预演。
- `manage-sing-box.sh`：只接管已证明为 loopback、唯一且由 `sing-box` 持有的监听，并通过实际 SOCKS5 代理探测；也可显式选择 Homebrew service 或 user launchd。
- `diagnose-proxy-dns.sh`：只读检查 DNS、监听端口、launchd 状态和直连/代理连通性。
- `temporary-cutover.sh`：仅用于单进程或非高性能拓扑，在已经准备好的主服务与临时服务之间调用环境私有 switch adapter。高性能多 gateway 生产切流不得使用该脚本。
- `quick-performance-cutover.sh`：高性能生产的默认快速切流入口。它只验证已经启动的 candidate control/API/gateway、单个 Go sidecar health 和启动日志，然后原子替换外层 route fragment、reload Nginx、验证三条公网请求；失败立即恢复原 route。它不启动、停止或重配任何 Node/Go 服务。
- `performance-handover-controller.sh`：针对多 gateway 性能槽的外层 Nginx route-fragment 控制器。它只接受由当前部署控制器私有持有的非秘密 plan；切换前先连续验证两套 slot 内层 Nginx 的 `/__aisys__/health` control 路径、顺序绑定的 `gateway-1` 至 `gateway-3` direct health，以及同一内层监听的 `/v1/models` gateway 路径。两套槽位不得复用 loopback listener、进程 PID 或 DB service PID；随后再验证外层 route header、内层 `X-Juhe-Topology-Install` identity、固定 worker PID 集合和 access-log 增量。`/v1/models` 不携带 Key 时必须精确返回认证性 `401` 并带有该 slot 的 topology header。失败时恢复同一个先前 fragment，且不停止任一槽位。
- `install-redis-role-services.sh`：默认 dry-run，按 cache/state/queue 角色渲染独立 Redis 配置与 system LaunchDaemon；apply 使用 bootout、端口释放、原子替换、bootstrap、kickstart 和失败恢复。
- `verify-redis-role-isolation.sh`：只读验证 main `6379/6380/6381` 或 temporary `16379/16380/16381` 的三个 URL、PID、launchd job、PING、AOF/RDB 和淘汰策略，不输出密码。
- `migrate-wireguard-root-wrappers.sh`：只针对私有 manifest 明确列出的 8 条 system WireGuard job，将已校验 SHA-256、无 `PreUp`/`PostUp`/`PreDown`/`PostDown` hook 的来源配置原子复制到 `/usr/local/libexec/juhe-ai/wireguard-config/<逻辑接口>.conf`。运行时目录为 `root:wheel 0700`、配置为 `root:wheel 0600`；`/usr/local/etc/wireguard` 只保留为迁移输入，绝不修改其父目录所有权。wrapper 固定生成在 root-only libexec，来源 wrapper 只用于哈希、精确 plist 绑定和回滚元数据，绝不复制或执行。任一 bootstrap 失败会回滚已改 job。
- `wireguard-reconciler.sh`：root LaunchDaemon 的单次 WireGuard 数据面恢复器。未传 `--state-dir` 时使用 `<install-dir>/wireguard-reconciler-state`；它不管理 Node、Caddy、Nginx、数据库、DNS 或旧 SSH healer；单 Edge、外部 probe 未知、DNS/Caddy/Node 故障都只记录。
- `install-wireguard-reconciler.sh`：默认 dry-run 的上述迁移与恢复器安装入口。apply 需要私有 root manifest、两个受控脚本 SHA-256 和独立 203 TLS nonce probe adapter；未传 `--state-dir` 时使用 `<install-dir>/wireguard-reconciler-state`，避免 macOS `/var` 符号链接与 root-only 路径链契约冲突；`--remove --apply` 仅移除恢复器，不回退已经收口到 root-only 的 WireGuard job。
- `wireguard-203-tls-nonce-probe-adapter.sh` 与 `install-wireguard-203-tls-nonce-probe-adapter.sh`：通过 root-only 专用 SSH identity、固定 `known_hosts` 指纹和受限 `juhe-tunnel-probe-read-v1` forced-command 读取 203 的回环 collector。私有 mapping 必须和运行 manifest 的 8 个 Edge 精确一致，并以唯一 `node + public_ip` 指标序列绑定每条 Edge；adapter 每次调用前再次比对已安装 runtime manifest。它只返回 `0=已知健康`、`1=已知 Edge 失败`、`75=未知`，不输出 endpoint、私钥或响应正文。canary verify 传入本次重建开始时间，只有同序列成功样本时间不早于该时刻且仍新鲜才通过。
- `legacy-node-postgres-index-bridge.mjs`：历史 Node PostgreSQL 的固定索引桥接。默认只读 inspect；apply 和 cleanup-invalid 使用独立 PG 会话与 advisory lock，只允许固定目录内三条索引，不写 Goose ledger、表或业务行。详细操作见 [遗留 Node PostgreSQL 索引桥接说明](遗留NodePostgreSQL索引桥接说明.md)。
- `cleanup-production-artifacts.sh`：生产残留清理入口。默认 dry-run，只在显式 `--apply` 下清理已证明未引用的历史发布、已过配置热保留窗口的审计热搜索文件和足够陈旧的 `current.next.*` 临时链接；不会删除当前发布、数据库、审计问题归档或业务备份。
- `templates/`：无用户、域名、IP、密钥或生产路径的 plist 模板。

## 安全边界

- 外部 HTTP watchdog 已退役，不提供安装、恢复或启动脚本。主进程退出由 launchd `KeepAlive` 拉起，DB service 和 worker 继续由主进程 supervisor 管理。
- WireGuard reconciler 是唯一的窄例外：它只可对私有 manifest 的全部 Edge 同时满足两次陈旧握手、稳定默认路由、sleep/wake 宽限结束、无维护/发布锁和预算允许时，先处理一个 canary。canary 必须在旧映射已清理后重新观察到 `utun` 映射、连续新握手、传输增量以及独立 203 TLS nonce probe 成功，才会串行处理余下 Edge；macOS 可复用 `utun` 编号。任一步失败立即停止。它不会把公网 HTTP 失败直接当成 WireGuard 故障。
- 私有 manifest、203 mapping、SSH private key 和 `known_hosts` 必须由 root 持有且不可由服务用户、组或其他用户写入。203 collector 保持仅回环监听，不为恢复器临时开放 Prometheus；专用 SSH 公钥在 203 上必须设为无转发、无 PTY、无 shell 的固定 read-only forced-command。manifest 用于构成精确 allowlist，不得以通配 label、Shell 片段、环境变量或网络返回值扩展动作目标；wrapper 和配置的源 SHA-256 由 apply 前私有预检写入，不得写入仓库。
- 203 接入前置：先在 203 为专用公钥安装固定 `juhe-tunnel-probe-read-v1` forced-command，使其只读取 collector 文本；Mac 侧再以 root 创建 `0400/0600` identity 与 `0400/0600` 的仅该 host key 的 `known_hosts`。adapter 安装时传入的 6 字段 runtime manifest 必须就是恢复器随后安装的 manifest；任一文件尚未就绪时 adapter 返回 `75`，不得跳过校验或临时开放 Prometheus。
- 发布流程在替换任何 WireGuard plist、wrapper 或配置前必须创建 root-owned release lock；恢复器看到该标记只记录。发布完成、回滚完成或人工确认停止恢复后才可删除标记。
- 真实路径、label、用户、入口域名、端口、代理订阅和凭据由部署人员通过参数或服务器私有配置提供，不写入仓库。
- 高性能拓扑脚本不会安装 Nginx，也不会修改 Nginx 主配置；`--nginx-config` 必须是主配置已 include 的槽位配置绝对路径，绝不能等于 `--nginx-main-config`。脚本在任何目录创建、ownership 调整或配置覆盖前解析两者的物理父路径，拒绝同一规范路径、现有同 inode 文件和链接别名，并通过实际 Nginx `-T -c <main config>` 确认槽位文件已被活动主配置 include。system scope 必须显式传入两者并通过 `--nginx-bin` 绑定实际运行实例。生产 apply 还应通过 `--release-dir` 绑定不可变发布目录，脚本会解析物理路径后再生成运行脚本，避免 `current` 并发切换造成进程版本混用。`--dry-run` 不是流程模拟：它只读门禁不可变发布输入，要求真实发布目录、构建产物、两个 Node 预检脚本、`backend/.env` 与唯一 `backend-go/juhe-ai-go-sidecar` 常规可执行文件存在；任一缺失必定失败。F3/F4 都要求后续 apply 提供一致的 loopback input URL/listen address、独立 input secret 和 PostgreSQL；F3 另要求专用 blob/hot-search 目录，缺失绝不回退。`--apply` 才要求 Node、launchd、Nginx 和所有目标端口可用，并执行运行目录创建或服务、Nginx 状态变更。
- 默认单槽路径保持 `bin/performance`、`logs` 与 `shared/usage-spool`。主槽与临时槽并存时，临时槽必须额外提供唯一的 `--runtime-dir` 和 `--nginx-upstream-suffix`：运行根目录必须是 base 物理目录内的真实子目录，运行脚本、launchd 日志和 usage spool 都从该根目录派生；suffix 只能是 `A-Za-z0-9_` 且长度为 1 到 48，生成的 gateway/control Nginx upstream 名称会带上该 suffix。调用方仍须同时使用独立的 release、label prefix、端口、Nginx include 文件、数据库与 Redis 身份；脚本不会替这些外部隔离资源做推断。F1、F2、F3、F4 的实例 ID 都由 `--instance-id-prefix` 与固定组件名组成，必须稳定且唯一；发布输入门禁和 apply 都 fail-fast 检查唯一 Go sidecar 是发布包内可执行的常规文件，apply 还会在 system scope 下验证服务用户可读/执行。F1 运行脚本优先读取 `JUHE_AI_RUNTIME_LOG_POSTGRES_URL`，F2 优先读取 `JUHE_AI_TABLE_MONITOR_POSTGRES_URL`，F3 优先读取 `JUHE_AI_AUDIT_LOG_POSTGRES_URL`，F4 优先读取 `JUHE_AI_OPERATION_LOG_POSTGRES_URL`；均只在专用 URL 缺失时回退 `JUHE_AI_POSTGRES_URL`。F3/F4 显式设置独立 input secret 和 loopback listener，F3 还设置业务设置 URL 与 blob/hot-search，缺失即失败，不会切回 SQLite、Redis、Node worker 或旧队列。F2/F3/F4 达到有界 launchd 存活不等同于业务完成；不读取凭据、不做直接数据库探测，必须在 Node `/__aisys__/api/health` 已就绪后由部署人员通过 Node 只读 API 检查 F1/F2 新鲜度和 F3/F4 详情读回。发布/部署脚本已完成目标 Mac isolated temporary `--apply`、listener、F3 restart 和 rollback 预演；开发 PostgreSQL 闭环和这次临时预演都不能替代正式 release 的重复验证、Nginx/Caddy/Edge 切换或真实流量验收。
- 高性能槽位 Nginx 是外层可信反向代理与 Node 之间的本机路由层，必须原样传递外层写入的 `X-Real-IP`、`X-Forwarded-For` 和 `X-Forwarded-Proto`。不得用 `$remote_addr` 或 `$proxy_add_x_forwarded_for` 重建来源链，否则 Express 在 `trust proxy=1` 下会把本机回环地址识别为客户端 IP。
- `install-launchd-service.sh --apply` 必须显式传 `--health-port` 或 loopback `--health-base-url`；加载后在有界窗口内连续确认 `/__aisys__/health` 与 `/__aisys__/api/health`，失败会恢复旧定义和 loaded 状态。
- `manage-sing-box.sh` 的 `launchd` 更新在 bootstrap、kickstart、监听身份或代理探测失败时恢复旧 plist 与原 loaded 状态；`existing` 不会因为任意进程占用端口就接管。
- `temporary-cutover.sh` 不复制数据库、不生成临时 env、不停止主服务或临时服务。环境私有流程先完成资源隔离与候选启动，再使用本脚本切流；切流成功后也保留源服务，确认稳定后才显式清理。
- Redis 角色安装器不接受共享 host:port，不执行运行时参数热改；持久化和角色变化必须通过配置文件与 launchd 有界替换。temporary 必须使用独立三实例，不能只换 namespace 后复用生产 PID。
- apply 前必须保留当前可用入口和回滚目标，并先证明回滚目标当前确实是入口。主服务和临时服务必须使用不同 PID、端口和实际 release 目录，并同时通过 `/__aisys__/health`、`/__aisys__/api/health`。入口证明依赖 switch adapter 写入的响应头，不能只凭某个 health 返回 200 放行。
- 调用 switch adapter 前就会启用失败 trap；即使适配器已经部分改动入口后以非零状态退出，也会调用相反目标执行反向回滚并重新证明入口。
- 高性能多槽位不得把应用 upstream、DB upstream 与 active label 分别替换。外层 Nginx 必须先迁移到单个已 include 的 route fragment，再由 `performance-handover-controller.sh` 原子替换、`nginx -t`、reload 和路由证明。controller plan 仅允许路径、标签、Node 路径、入口 URL，以及两套 slot 的 control instance ID、gateway instance ID 前缀、`X-Juhe-Topology-Install` identity、每槽内层 Nginx 的 loopback `/__aisys__/health` control URL、每套三个 direct gateway health URL 和同一内层 listener 的精确 `/v1/models` gateway ingress URL，不接受密码、认证 token、数据库或 Redis URL。临时槽必须通过 `--instance-id-prefix` 生成和主槽不重合的 control、gateway、DB service 与 worker instance ID，避免共享 Redis 指标注册或网关发现键。所有 loopback URL 必须使用规范的 `127.0.0.1` 十进制端口；不接受 `localhost` 或前导零端口，从而不能把同一 listener 伪装成不同目标。`/v1/models` 探测不携带 Key，且只能返回 `401`；这证明请求进入真实 gateway 协议路由，不能使用裸 `/`，也不能放行 `404`，因为 control 节点的错误路由会返回 `404`。apply 会拒绝任何跨 slot 的 loopback listener 复用、gateway 顺序/身份错误，以及控制、gateway、DB service 或 worker PID 的交集；plan、route、fragment、Nginx 配置和 access log 都必须归当前控制器所有、不是链接且不允许组或其他用户写入。一次状态转换由目录锁串行化。失败或中断时保留两个槽位和 journal，`rollback-proven` 可再次预检，`recover` 只接受未完成状态，避免陈旧 rollback fragment 重写已提交路由。
- `install-performance-topology.sh --apply` 会在回滚快照建立前创建运行目录、audit blob 路径并可能调整服务用户 ownership；现有失败回滚只覆盖 launchd/run script/Nginx，不会自动删除这些目录或恢复 ownership。失败记录必须列出这些持久副作用并人工复核；候选槽被放弃后只能按精确路径清理，不能递归清理共享 base。

## 生产零停机发布契约

- 零停机只适用于“独立 candidate 槽已完整运行，再由外层 handover 原子切流”。对 active 槽使用同端口、同 label 的原地 `install-performance-topology.sh --apply` 会逐项重启服务，不得称为零停机。
- candidate 必须使用不可变 release、独立 Node runtime、label、端口、Nginx slot include、upstream suffix、Node instance ID 与 Redis 身份；业务槽连接同一权威业务库，不能把预演 clone 当成生产权威库。candidate 必须显式使用 `--go-sidecar-mode reuse --audit-input-port <正式 F3 owner 端口> --operation-log-input-port <正式 F4 owner 端口>`，共享正式槽唯一 Go sidecar 的 F1/F2/F3/F4 owner、Store、lease、审计 blob/hot-search 目录与 loopback 输入端口，不能创建第二个 Go owner。reuse apply 会检查候选 label 对应的 Go sidecar launchd job、plist 与 run script；任一残留即拒绝，必须先人工确认并清理，绝不自动停止未知 owner。
- 常规发布只需 candidate control health、API health、无 Key 的 gateway `401`、F3/F4 Go sidecar health 和启动日志无 `panic`/`fatal`；随后由 `quick-performance-cutover.sh` 原子切 route，并验证同样三条公网请求。静态 HTML、单个 `200` 或任一 Go health `204` 仍不能单独放行。
- `performance-handover-controller.sh` 的长 preflight、PID/worker 集合、access-log 稳定窗口和 journal 只保留给故障调查、异常回切或需要深入证明的变更，不是常规发布前置。快速切换失败时脚本自动恢复原 route，两个槽均保留；下一次 routine release 前再清理旧槽即可。

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
  --release-dir "$HOME/juhe-ai-lite/releases/<commit>/juhe-ai-release" \
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

# 临时性能槽必须使用独立运行目录、Nginx upstream 名称和实例 ID 前缀，避免与主槽碰撞。
bash ./install-performance-topology.sh --dry-run \
  --scope system \
  --service-user '<运行用户>' \
  --base-dir "$HOME/juhe-ai-lite" \
  --release-dir "$HOME/juhe-ai-lite/temporary/releases/<commit>/juhe-ai-release" \
  --label-prefix com.example.juhe-ai.temporary \
  --runtime-dir "$HOME/juhe-ai-lite/temporary-runtime-<stamp>" \
  --nginx-upstream-suffix "temporary_<stamp>" \
  --instance-id-prefix "temporary" \
  --go-sidecar-mode reuse \
  --audit-input-port '<正式 owner F3 loopback端口>' \
  --control-port '<临时control端口>' \
  --gateway-base-port '<临时gateway起始端口>' \
  --gateway-count 3 \
  --ingress-port '<临时ingress端口>' \
  --nginx-config /opt/homebrew/etc/nginx/servers/juhe-ai-temporary.conf \
  --nginx-bin /opt/homebrew/bin/nginx \
  --nginx-main-config /opt/homebrew/etc/nginx/nginx.conf

bash ./manage-sing-box.sh existing --dry-run \
  --config "$HOME/.config/sing-box/config.json" \
  --probe-url 'https://api.openai.com/'

bash ./diagnose-proxy-dns.sh \
  --proxy-url socks5h://127.0.0.1:7890 \
  --target-host api.openai.com
```

生产使用 system LaunchDaemon 时必须额外传 `--service-user`。Node、DB service 和 worker 都以该非 root 用户运行；脚本只把日志与用量 spool 目录交给该用户，不改变 release 或共享配置的所有权，并在变更 launchd 前以目标用户验证 release/Node 的访问权限和运行目录写权限。system base 必须可遍历但不能由该服务用户写入；运行目录必须是物理 base 目录内的真实目录，脚本会拒绝符号链接或解析后越界的目录，并在 `chown` 时禁止跟随符号链接。

## 生产残留清理

先执行 dry-run，确认候选项后才执行 apply。发布清理默认保留当前发布及至少一个按更新时间选择的回滚发布；每个候选还会检查当前/回滚符号链接、运行进程、LaunchDaemon plist 和 base 内运行脚本引用。任一引用无法核实时该发布只会显示为 `SKIPPED_RELEASE`，不会被删除。存在新于保留期的 `current.next.*` 链接时，apply 会直接失败，避免与正在进行的发布并发。

```bash
bash ./cleanup-production-artifacts.sh --dry-run \
  --base-dir "$HOME/juhe-ai-lite" \
  --prune-stale-links --prune-releases --keep-release-count 2 \
  --prune-audit-hot \
  --audit-success-hot-retention-hours '<运行中的 JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS>' \
  --audit-success-sample-rate '<运行中的 JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE>'
```

只有检查输出符合预期时，才将 `--dry-run` 改为 `--apply`。`--audit-success-hot-retention-hours` 和 `--audit-success-sample-rate` 必须抄自当前正在运行的配置，脚本按这个热保留窗口清理 `audit-hot-YYYYMMDDHH.ndjson`，不删除 `audit/blobs`、问题审计或数据库记录。

临时接管前，主服务和临时服务都必须已经独立健康，且 switch adapter 接受 `main` / `temporary` 参数：

```bash
bash ./temporary-cutover.sh --action takeover --dry-run \
  --switch-script "$HOME/juhe-ai-lite/bin/switch-upstream.sh" \
  --main-release "$HOME/juhe-ai-lite/current" --main-pid '<主PID>' --main-port '<主端口>' \
  --temporary-release "$HOME/juhe-ai-lite/temporary/current" --temporary-pid '<临时PID>' --temporary-port '<临时端口>' \
  --ingress-health-url 'https://你的入口域名/__aisys__/health'
```

审核 dry-run 后，把 `--dry-run` 改为 `--apply`。回切使用相同参数，只把 `--action` 改为 `switchback`。任何切换失败时脚本会调用相反目标回滚；如果回滚入口无法证明，脚本保留两套服务并要求人工处理，不自动杀进程。
