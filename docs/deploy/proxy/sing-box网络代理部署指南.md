# sing-box 网络代理部署指南

> 面向唯一生产形态：国内单台云服务器 + Docker Compose。本文只考虑在云端服务器上部署 sing-box 作为 juhe-ai 的上游代理，不涉及任何其他部署位置。
>
> 本文**不随附实现脚本**：订阅解析、择优控制器、订阅定时更新器三部分只写清思路、策略与验收标准，实现（含 systemd 单元与自动化脚本）由维护者自行完成或交给 AI 按本文生成；订阅链接、节点凭据属于用户资产，不写入仓库。

## 1. 为什么需要代理

juhe-ai 是上游 AI 账号中转服务。云服务器无法直连 OpenAI、Anthropic、Gemini 等上游时，必须让服务器出站请求走可用代理，否则会出现账号测试失败、OAuth 刷新失败、网关请求超时或上游连接错误。

需要区分两类代理：

| 类型 | 作用 | 配置位置 |
| --- | --- | --- |
| 服务器网络代理 | 拉 Docker 镜像、装系统包更新 | Shell 环境变量、Docker daemon、系统代理 |
| juhe-ai 上游账号代理 | 账号测试、OAuth、网关请求上游模型 API | 后台“代理管理”并绑定到 AI 账户 |

## 2. 目标形态与总体结构

一台云端服务器上同时运行 Docker 化的 juhe-ai 与宿主机 sing-box（systemd 常驻）：

```text
juhe-ai 容器（gateway / jobs）
  → socks5h://172.18.0.1:17892          （Docker bridge 网关 = 宿主机）
  → 宿主机 sing-box（systemd 服务）
      ├ mixed 入站：172.18.0.1:17892（容器可达；不监听公网）
      ├ selector 组：聚合订阅节点池，决定当前出口节点
      ├ clash_api：127.0.0.1:19090（供自动化查询与切换）
      └ 出站：订阅节点池 → 上游 AI API
```

关键结构决定：

- sing-box 跑在宿主机而不是容器里：节点池更新要重启代理进程、自动化要读写本机文件与 systemd，宿主机进程最简单可靠。
- 入站监听 Docker bridge 网关地址（如 `172.18.0.1`）而不是 `127.0.0.1`：容器内进程要能访问；同时**绝不监听 `0.0.0.0`**，代理端口一旦暴露公网会被扫成开放代理。防火墙仅放行本机与容器网段。
- 出站用 `selector` 组而不是 `urltest`：原因与替代策略见第 9 节。
- 订阅链接是用户资产：保存在服务器本地文件（约定 `/etc/sing-box/subscription-url`，权限 `600`），不入仓库、不入日志。

文件与目录约定：

| 路径 | 用途 |
| --- | --- |
| `/etc/sing-box/config.json` | sing-box 主配置（由更新器自动重写） |
| `/etc/sing-box/subscription-url` | 订阅链接，一行一个 URL，权限 600 |
| `/etc/sing-box/config.json.bak-*` | 每次变更前的配置备份 |
| `/var/lib/juhe-proxy-switch/state.json` | 择优控制器状态（延迟、失败计数、拉黑、切换历史） |
| `/var/lib/juhe-proxy-switch/lock` | 控制器与更新器共用的互斥锁 |
| `/var/log/juhe-proxy-switch.log` | 控制器决策日志 |
| `/var/log/juhe-sub-update.log` | 订阅更新器日志 |

## 3. 安装 sing-box（Linux）

使用官方软件源或 GitHub Release：

- 官方包管理安装页：`https://sing-box.sagernet.org/installation/package-manager/`
- 官方 Release：`https://github.com/SagerNet/sing-box/releases`

Debian / Ubuntu 按官方 package-manager 页面添加 SagerNet 软件源后安装；发行版无合适软件源时，下载对应架构（`linux-amd64` / `linux-arm64`）压缩包，把 `sing-box` 放入 `/usr/local/bin/`。安装后用 `sing-box version` 确认可执行。

版本要求：需支持 `selector` 出站、`clash_api` 与 `cache_file` 持久化（1.13.x 已验证可用）。注意 1.13 的 `cache_file` 无 `store_selected` 字段，启用 `enabled` 即持久化所选节点。

## 4. sing-box 配置结构

配置由四个部分组成；自动化（第 10 节）只重写“订阅节点出站 + selector 组”这两块，其余保持稳定。

### 4.1 mixed 入站

```jsonc
{
  "type": "mixed",
  "tag": "mixed-in",
  "listen": "172.18.0.1",        // Docker bridge 网关，容器经此访问；禁用 0.0.0.0
  "listen_port": 17892,
  "sniff": true
}
```

HTTP 与 SOCKS 客户端都可连该端口。

### 4.2 订阅节点出站

每个订阅节点对应一个 outbound（tag = 稳定唯一的节点名），结构由订阅解析器生成（见第 8 节）。节点数量随订阅变化，这是配置中唯一“可变”的部分。

### 4.3 selector 组

```jsonc
{
  "type": "selector",
  "tag": "auto",
  "outbounds": ["<节点tag1>", "<节点tag2>", "/* ...全部节点... */"],
  "default": "<初始/兜底节点tag>",
  "interrupt_exist_connections": false
}
```

`route.final` 指向该组 tag。组内节点列表随节点池同步重写，组 tag 保持不变，入站与路由零改动。

### 4.4 experimental（自动化依赖）

```jsonc
"experimental": {
  "clash_api": { "external_controller": "127.0.0.1:19090" },
  "cache_file": { "enabled": true }
}
```

`clash_api` 是择优控制器与更新器的操作面（查询当前选择、切换节点、健康校验）；`cache_file` 持久化所选节点，重启不丢。

## 5. systemd 常驻与基础验证

配置文件放 `/etc/sing-box/config.json`，每次修改后先 `sing-box check -c /etc/sing-box/config.json` 再重启。systemd 单元要点：

```ini
[Service]
Type=simple
ExecStart=/usr/bin/env sing-box run -c /etc/sing-box/config.json
Restart=always
RestartSec=5
LimitNOFILE=1048576
```

`Restart=always` 必须保留：更新器会周期性重启进程，失败后要能自动拉起。

基础验证：

```sh
ss -lntp | grep ':17892 '                                          # 入站已监听
curl -x socks5h://172.18.0.1:17892 https://api.openai.com/v1/models -I   # 出口可达主上游
curl http://127.0.0.1:19090/proxies/auto                           # selector 组存在且 type=Selector
```

## 6. 接入 juhe-ai

在后台“代理管理”新增代理：类型 `socks5h`，地址与端口即第 2 节的入站（当前生产为 `172.18.0.1:17892`），无认证则用户名密码留空。保存后执行“测试代理”，通过后把该代理绑定到需要走代理的 AI 账户。账号测试、OAuth 刷新和网关请求按账号代理走对应出口。

| juhe-ai 运行位置 | sing-box 运行位置 | 代理 Host |
| --- | --- | --- |
| 同机 Docker 容器（当前形态） | 同机宿主机 | Docker bridge 网关 IP（`172.18.0.1`）；或 `host.docker.internal` 加 `host-gateway` 映射 |
| 同机发布包直跑 | 同机 | `127.0.0.1` |
| 应用服务器 | 独立代理服务器 | 代理服务器内网 IP |

## 7. OAuth 兜底代理

`JUHE_AI_OAUTH_PROXY_URL` 只作为 OpenAI OAuth token 换取 / 刷新的兜底代理（Docker 形态写入 compose 使用的 env 文件，契约见 `docker/single-server/README.md`）：

```env
JUHE_AI_OAUTH_PROXY_URL=socks5h://172.18.0.1:17892
```

注意：该变量不是所有上游请求的全局代理。普通上游模型请求仍应通过后台“代理管理”绑定到账号；不能只配置本变量。

## 8. 订阅链接解析与节点池生成

订阅服务商随时轮换节点，静态节点列表会静默失效；订阅解析是两项自动化（第 9、10 节）的共同基础。本节定义“从订阅 URL 到合法 sing-box 配置”的完整思路。

### 8.1 拉取策略

- **链路兜底**：依次尝试直连 → 本机 sing-box 自身出口（`socks5h://<入站地址>`），第一个成功者生效。订阅面板域名在国内常直连超时，必须有代理兜底。
- **限频识别**：订阅面板对同 token 高频抓取会返回 403 或 200 + HTML 限频页而非订阅内容。解析前必须做订阅形态校验（见 8.2），非订阅内容视为本次失败：换下一条链路，整链等 30 秒后重试一轮；仍失败则本轮放弃，等下个周期。
- **错峰**：多台部署共用同一订阅时，定时器需加随机延迟（如 0-5 分钟抖动），避免同 token 同时触发限频。
- **频率**：每小时一轮足够；订阅池本身存在分钟级漂移，更高频收益低且易限频。

### 8.2 返回体识别与解析

订阅返回体常见三种形态，按序识别：

1. **base64 分享链接列表**：整体 base64 解码后逐行得到 `ss://`、`vmess://`、`vless://`、`trojan://`、`hysteria2://` 等分享链接——最常见的机场形态。
2. **Clash YAML**：`proxies:` 列表，逐项映射字段。
3. **sing-box JSON**：直接含 `outbounds` 数组，取非 `direct`/`block`/`dns` 项。

识别方法：先尝试 base64 解码且解码结果含已知分享链接 scheme 则走形态 1；否则尝试 YAML 解析含 `proxies` 键则走形态 2；否则尝试 JSON 解析含 `outbounds` 键则走形态 3；都不满足即判定“非订阅内容”（进入 8.1 的限频/失败处理）。

### 8.3 分享链接 → sing-box outbound 映射要点

每种协议的映射由其 URI 规范决定，实现时逐协议处理；共性要点：

| 要点 | 说明 |
| --- | --- |
| 必填字段 | 服务器地址、端口、协议专属凭据（ss 的加密方法+密码、vmess/vless 的 uuid、trojan 的密码等） |
| TLS | `tls=true` 时填 `tls.enabled`、`server_name`（SNI）；`allowInsecure` 对应 `insecure`，默认不开启 |
| 传输层 | ws 需换算 `path` / `Host` 头到 `transport`；grpc 需 `service_name`；tcp 直接省略 |
| 混淆/插件 | ss 的 simple-obfs 等插件参数需换算为 sing-box 对应字段，无法表达的节点丢弃并记日志 |
| query 参数 | `encryption`、`flow`、`sni`、`fp`（uTLS 指纹）、`type`/`path`/`host` 等按各协议 URI 规范读取 |

Clash YAML 形态同理：`name/type/server/port/uuid/password/cipher/tls/servername/network/ws-opts` 等字段逐项换算。无法映射的节点跳过并记日志，不让单个坏节点阻塞整池。

### 8.4 规范化：稳定 tag 与信息条目过滤

- **tag 稳定且唯一**：tag 由节点名生成；重名节点追加序号去重。同一节点输入必须产生同一 tag——tag 漂移会让无序比较误判“全部变化”，也会让控制器按 tag 记账失真。节点名可能含表情与空白，保留原样（UTF-8）但做 trim。
- **剔除数值型信息条目**：机场常在节点列表里混入“剩余流量：xx GB”“套餐到期：xxxx”“官网：xxx”等伪节点，且数值每次抓取都变。解析阶段按特征（无法解析为合法节点 / 名称匹配流量、到期、官网等模式）剔除，保证变更检测的比较稳定。
- **空池保护**：解析得到 0 个有效节点视为失败，绝不应用。

### 8.5 生成配置

在既有配置骨架（第 4 节）上只重写两块：订阅节点 outbounds（全量替换）与 selector 组的 `outbounds` 列表（组 tag、入站、route、experimental 均不动）。selector 的 `default` 取变更前线上实际选择（经 clash_api 读出），保持出口语义无损；首次部署无线上状态时取解析列表第一个节点。

## 9. 择优控制器：思路与策略

### 9.1 为什么不用 urltest

订阅节点池用默认 `urltest` 组会按“到测速 URL 的延迟”自动换节点，生产实测有四个致命问题：

1. 只测延迟不测真实上游连通性：能通 gstatic 的节点可能对真实目标返回 503。
2. 排名洗牌导致节点乒乓切换，连接频繁中断。
3. 坏节点没有记忆，过几分钟又当选。
4. **目的地相关劣化**：同一节点对测速站正常、对部分真实站点 503；`api.ipify.org` 这类 IP 回显站会被部分节点阻断，不能当探针。

### 9.2 控制器思路

用 `selector` 组 + 独立的择优控制器进程（一个仅用标准库的脚本即可实现）替代 urltest：控制器按周期经 `clash_api` 评估节点、决定是否切换。控制器故障时退化为“selector 停留在当前节点”，不乱切、不影响 sing-box 本身运行——这是安全底线。

### 9.3 四条切换策略

1. **先测通再切**：任何切换前，候选节点必须通过对真实上游（主业务上游 API，如 `https://api.openai.com/v1/models`，收到任意 HTTP 响应即视为传输可达）的实时确认测试。
2. **不频繁切**：性能切换设最小间隔（参考值 10 分钟）；评估周期 2 分钟——评估 ≠ 切换。
3. **差异不大不切**：候选必须比当前节点快超过容差（参考值 100ms）才允许切换，避免无意义抖动。
4. **差异过大或当前节点坏了才切**：当前节点连续 2 次健康检查失败触发救援切换（不受最小间隔限制）；连续失败 3 次的节点拉黑冷却 30 分钟。

### 9.4 参数参考

| 参数 | 参考值 | 含义 |
| --- | --- | --- |
| 评估周期 | 2 分钟 | 每轮对全池做一次健康/延迟评估 |
| 性能切换容差 | 100ms | 候选须比当前快超过该值 |
| 性能切换最小间隔 | 600s | 救援切换不受限 |
| 救援阈值 | 连续 2 次失败 | 当前节点连续失败该次数触发救援切换 |
| 拉黑阈值 | 连续 3 次失败 | 连续失败该次数拉黑 |
| 拉黑冷却 | 1800s | 冷却后重新参与评估 |
| 测试 URL | 主业务上游 | 真实业务目标；主要流量换供应商时同步调整 |
| 慢扫描批量 / 过期 | 15 个 / 900s | 全池较大时分批测，避免单轮超时；过期结果作废 |

### 9.5 状态、日志与互斥

- **状态文件**（`/var/lib/juhe-proxy-switch/state.json`）：各节点延迟记录、连续失败计数、拉黑表、切换历史。重启控制器不丢记忆。
- **决策日志**（`/var/log/juhe-proxy-switch.log`）：每轮一行（时间、当前节点、候选与延迟、动作），journald 无持久化的机器上这是唯一可追溯记录。
- **互斥**：控制器与更新器可能同时操作 clash_api / 重启 sing-box，必须共用一个文件锁（`/var/lib/juhe-proxy-switch/lock`）串行化。

### 9.6 边界说明

- 控制器只解决“节点池里选哪个”；**若整池分钟级漂移劣化（几乎所有节点反复通/断），任何选节点机制都救不了**，此时应切换到其他代理链路。节点自身的每连接级丢包由网关跨账户重试兜底。
- 后台“代理管理”的 test_status 是展示态（jobs 周期探测回写），IP 回显类目标在部分节点上必失败，可能显示 failed/warning；**派单只看代理是否启用，不受 test_status 影响**。

## 10. 订阅定时更新器：思路与策略

订阅服务商随时轮换节点，静态配置会静默失效。更新器以 systemd timer 每小时运行一轮（带 8.1 的错峰抖动），与择优控制器组成完整自动化。

### 10.1 单轮流程

1. **拉取与解析**：按第 8 节完成拉取、识别、解析、规范化，得到本次节点集合。
2. **变更检测**：与线上生效节点做**无序集合比较**（顺序无关；比较基于 8.4 规范化后的稳定 tag 与关键连接参数）。无变化 → 本轮结束，零动作、不打断在途连接。
3. **备份**：有变化才继续；备份当前 `/etc/sing-box/config.json`，保留最近 5 份。
4. **重写配置**：按 8.5 生成新配置，写入前先落盘到临时文件。
5. **check 门禁**：`sing-box check` 不过 → 放弃本次变更，线上配置分毫不动，记日志告警。特别要拦住“urltest 特有字段（`url`/`interval`/`tolerance`/`idle_timeout`）残留导致 check 失败”这类结构性错误。
6. **重启**：`systemctl restart` sing-box（依赖单元的 `Restart=always` 兜底）。
7. **健康校验**：Clash API 可用、selector 组存在；`JUHE_SB_API` 类 API 不可用的部署可退化为经 socks 入站实测主上游。
8. **重置控制器状态**：清空控制器的延迟记录与拉黑表（节点池已变，旧记账作废）；写 selector `default` 为变更前线上选择（语义无损迁移）。
9. **抽样预选**：抽样实测（默认 10 个）节点，把最快可达者设为当前出口，避免重启后停留在随机节点上。
10. **失败安全**：步骤 6-9 任一失败 → 自动回滚到最近备份并重启，保持代理可用；回滚动作本身也记日志。

### 10.2 与其他组件的协作

- 与择优控制器共用互斥锁（9.5），不会并发操作 Clash API。
- 订阅 URL 从 `/etc/sing-box/subscription-url` 读取（600），不落日志、不进报告。
- 手动立即更新：`systemctl start <更新器 service>`；暂停自动化：`systemctl stop <更新器 timer>`。

## 11. 排障

- 后台代理测试失败：先在服务器上用 `curl -x socks5h://<入站>:<端口>` 验证代理端口可用性，再查账号绑定。
- 容器访问失败：确认容器内能路由到入站地址（bridge 网关 / host-gateway）；`host.docker.internal` 在 Linux Engine 上必须显式加 `host-gateway`。
- OAuth 可以刷新但网关请求仍失败：AI 账户未绑定代理，只配了 `JUHE_AI_OAUTH_PROXY_URL`。
- 上游仍超时：确认 selector 当前节点真实可达（9.3 的确认测试）、DNS / IPv6 / TLS 拦截未阻断；查看控制器决策日志确认切换行为。
- 节点池大面积通/断翻转：整池漂移劣化（9.6），控制器无解，切备用链路或等订阅服务商恢复。
- 更新后全部节点不可用：检查 check 门禁日志与回滚记录；常见根因是订阅返回了限频页被误解析，确认 8.2 的形态校验已启用。

## 12. 验收清单

- [ ] `sing-box check` 通过；入站仅监听 bridge 网关地址，公网无法访问代理端口。
- [ ] `curl -x socks5h://172.18.0.1:17892 https://api.openai.com/v1/models -I` 有响应。
- [ ] `curl http://127.0.0.1:19090/proxies/auto` 返回 `type: Selector`，组内包含全部订阅节点。
- [ ] 后台“代理管理”测试通过；绑定账号后账号测试、OAuth 刷新、网关请求均走代理成功。
- [ ] 控制器：手工把 selector 切到一个坏节点，2 个周期内自动救援切换到可达节点；决策日志与状态文件正常增长。
- [ ] 更新器：订阅无变化时一轮零动作；构造变化（或等真实变更）后走完 备份→check→重启→重置→预选 全流程；check 注入坏配置时拒绝应用并保持线上可用。
- [ ] 订阅 URL 权限 600，不出现在任何日志与文档中。
