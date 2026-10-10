# BUG-0296：TransportOptions.DialGuard 装配条件恒假，/v1 链连接层 SSRF 纵深自引入起从未生效

状态：已修复（2026-10-10）——DialGuard 装配、生图直连下载、生图代理下载最终目标校验（方案 A）全部收口，待发布验证。

## 缺陷

`backend-go/shared/platform/upstreamhttp/transport.go` 的 `NewTransport` 对 `TransportOptions.DialGuard` 的安装条件为：

```go
if options.DialGuard != nil && transport.DialContext == nil {
    transport.DialContext = options.DialGuard.DialContext
}
```

两个原因使该条件恒假、安装从未发生：

1. `transport` 克隆自 `http.DefaultTransport`，其 `DialContext` 恒非 nil；
2. 直连路径（`rawProxyURL` 为空）在该安装段之前已提前 `return`。

因此 `DialGuard`（私网/保留段校验 + DNS 解析-校验-固定拨号防 rebinding，D-192 的请求期 DNS 半边）自 `37ec59f0f` 引入起从未被任何 transport 实际装载。`/v1` 链唯一生产装配 `gatewayupstream/transport_urlpolicy.go:57`（`NewResolvedUpstreamURLPolicy` → `TransportDeps.DialGuard`）的连接层纵深空转——问题-0175 档案 D-146/D-192 预判的「SSRF 纵深缺失」实底即此。

边界澄清：URL 级静态校验（保存层校验、严格模式 prepare 校验）一直生效，缺陷面是连接层纵深（含 DNS rebinding 窗口），非完全无防护。

## 发现路径

外部复审问题 1（生图下载路径无上游 URL 安全校验）实施「复用既有 DialGuard」时，实证装配点从未生效——不修复装配点，生图与 /v1 两侧的接入都是无效装配。

## 修复

1. `transport.go`：直连分支 return 前与 HTTP(S) 代理分支显式安装 `options.DialGuard.DialContext`（guard 校验实际 socket 对端：直连为上游主机、代理为代理主机）；SOCKS5 分支保持自身拨号器（`socks5h` 为远端解析，guard 的解析-校验-固定拨号语义不可达，按契约忽略 guard）。
2. `urlguard.go`：`NewDialGuard` 回退 dialer 从 `10s` 对齐 `http.DefaultTransport` 拨号预算（30s 超时 + 30s keep-alive），避免防护启用本身附带收紧连接超时预算（guard 此前从未生效，10s 从未真正服务过调用方）。
3. 生图下载两条路径同批接入：主路径 `chain_chat_image_proxy.go` 以部署级 `URLSecurityConfig` 构造 guard（与 /v1 链同源，私网代理是否放行由部署 env 裁决）；回退客户端 `generation_images.go` 改为零值最严 guard + 禁跟随重定向（对齐主路径语义）。

## 行为影响

- 生产默认（`JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS` 未设置 = 放行私网，2026-09-19 决策）下 /v1 链连接行为不变；严格模式仅增加纵深，可达性结论不变（prepare 校验先行）。
- 回退直连生图下载从「无校验、跟随重定向」变为「拒绝私网/保留地址、不跟随重定向」，属外部复审问题 1 的修复目标语义。

## 验证

- `upstreamhttp` 包：直连拨私网与 HTTP 代理路径拨代理主机均被 guard 以 `UnsafeResolvedUpstreamURLError` 拒绝；无 guard 时同址正常拨号（对照证明拒绝来自 guard）。
- `chat` 包：回退客户端对 `127.0.0.1`/`10.255.255.1`/`169.254.169.254`/`[::1]` 拒绝且错误可定位；302 重定向到私网地址不跟随、收敛为下载失败；客户端契约钉（60s 超时、`ErrUseLastResponse`、DialContext 经 guard）。
- `/v1` 链真实派发冒烟（allow 配置）通过；`go build` 三模块通过。

## 关联

- 问题-0175 档案 D-146（SSRF 半边已随本档案收口，Governor/连接治理面仍开放）、D-192（已收口）。
- 外部复审问题 1（生图下载 SSRF）为同批加固；直连与代理的修复覆盖不同，代理最终目标残留见下节。完整复审状态见 [全面审查复核与残留问题报告](../reports/全面审查复核与残留问题报告-2026-10-09.md)。

## 2026-10-09 复审：代理最终图片 URL 校验残留

### 已修复与仍缺失的边界

`dd11b92a5` 已修复 transport 的 guard 装配及生图回退直连路径。不能由此推断所有代理下载的最终目标都已受保护：

- `chat/generation_images.go` 的 `downloadGeneratedImage` 在发送前没有检查最终图片 URL 的主机。上游 JSON 中的 `url` 被直接送入该方法。
- `cmd/juhe-ai-gateway/chain_chat_image_proxy.go` 用部署级配置创建 `DialGuard` 后交给 `upstreamhttp.SharedClient`。HTTP(S) 代理分支中，guard 检查的是代理 socket 主机；图片目标由 HTTP 代理处理。
- `shared/platform/upstreamhttp/transport.go` 的 SOCKS 分支使用独立拨号器，`socks5h` 将目标域名交给代理解析，不执行该 guard 的本地解析与固定拨号。私网目标字面量也未在发送前拦截。
- 两条下载路径当前均禁止跟随重定向；残留是首次目标校验，不是重定向回归。

上游若能控制返回的图片 URL，仍可诱导已绑定代理尝试访问私网目标。实际可达范围取决于代理所在网络和目标 ACL，不能据此声称可访问 gateway 本机 Docker 内的 PostgreSQL/Redis，更不能声称已验证访问生产内网。

### 本机 Mock 实证

2026-10-09 在 gateway 模块下运行临时 Go 程序，使用现有 `upstreamhttp.NewDialGuard` 与 `NewClient`（与 `SharedClient` 共用 `NewTransport` 和 `NewClientWithTransport` 构造逻辑）：

1. 创建本机 `httptest` HTTP 代理；handler 只记录请求并返回固定响应，不向任何目标转发。
2. guard 使用严格配置，仅将该 Mock 代理 origin 加入私网 allowlist。
3. 经代理请求 `http://10.255.255.1:8080/private-image.png`。
4. 实际退出码为 `0`，输出如下：

```text
proxy_observed=GET http://10.255.255.1:8080/private-image.png
response_status=200
response_body=mock-only-no-forward
```

这证明私网最终 URL 被送到了代理。该实验未访问私网目标、真实代理或生产环境。SOCKS 路径本轮仅完成源码核验，未运行协议级 Mock。

### 待处理与验收

明确生图最终目标的安全策略，并在代理请求发出前执行目标校验；代理地址的合法性不能替代最终目标校验。远端 DNS 解析模式还需明确代理侧解析约束，不能把一次本地 DNS 预检当成已消除远端 rebinding。

回归至少覆盖 HTTP(S)/SOCKS 代理对私网字面量、localhost、允许的公网目标及重定向的处理，同时保留合法私网代理的可用性。现有直连测试 `TestChatImageURLDownload*` 本轮通过，但不覆盖代理最终目标拒绝。此残留未修复前，外部问题 1 只能记为部分修复。

## 2026-10-10 修复记录：代理最终目标校验（方案 A）

- 实施裁决：用户 2026-10-10 选定方案 A（与 `/v1` 链同源的部署级 `URLSecurityConfig` 在发送前校验最终目标）。落点在组合根 `cmd/juhe-ai-gateway/chain_chat_image_proxy.go`：新增 `targetGuardRoundTripper`——发送前对 `request.URL` 最终目标主机经同一部署级 guard 的 `ValidateHost` 做本地解析校验（两条生图下载路径均禁跟随重定向，`req.URL` 即最终目标首跳；scheme 限 http/https、端口按 scheme 补默认；拒绝错误为 `UnsafeResolvedUpstreamURLError`，与直连路径可 `errors.As` 一致）；新增纯装配 helper `newTargetGuardProxyClient`（`SharedClient` 浅拷贝 + 替换 Transport，保留池 client 的 `CheckRedirect=ErrUseLastResponse` 禁跟随语义，池条目零改写）。三条路径的最终目标防护：直连=拨号边界 guard（原样不变）；HTTP(S) 代理=拨号边界校验代理主机（原样）+ 发送前目标预检（新增）；SOCKS5H=发送前本地解析预检（新增）。chat 包与 `ImageDownloadProxy` 端口形状零改动。
- 残留登记（不宣称消除）：SOCKS5H 实际连接仍由代理解析，发送前本地预检不消除远端解析偏移窗口；预检与拨号之间亦无钉扎传递（与 `/v1` 链 prepare 同一语义，见 gatewayupstream/transport_urlpolicy.go 头注释）。
- 验证：新增 9 用例（`chain_chat_image_proxy_targetguard_test.go`，`-race` 绿）——严格配置私网字面量在代理收到请求前拒绝（stub 代理计数 0，即 2026-10-09 Mock 实证形态的反面）、allowlist 私网放行（合法私网代理可用性）、`AllowPrivateBaseUrls=true`（生产 env 默认形态）放行、公网字面量放行、302 不跟随且计数不增、SOCKS5H 预检先于拨号（listener 连接计数 0）、非 http(s) 拒绝、localhost 域名拒绝、池条目不被改写；`internal/chat` 零改动且既有 `TestChatImageURLDownload*`/`TestGenerateChatImage*` 全绿。等价类归并说明：localhost/公网/302 为 wrapper 层代理协议无关行为，SOCKS 协议级实测仅私网字面量+先于拨号一臂；allowlist 无端口投影分支为静态核验。
- 文档同步：`docs/functions/核心功能设计.md:195`（生图 url 下载发送前最终目标解析校验 + socks5h 残留窗口）；`docs/develop/安装指南.md:119`（配置面消费说明 + 残留窗口）。同段"全局开关…生产仍禁止"旧句与 runtime 默认放行决策（2026-09-19）的措辞张力为既有文档漂移，本批未处理、在此登记。
- 复审：独立只读复审无 blocker（2026-10-10）。外部复审问题 1 自本记录起由"部分修复"改判"已修复"。
