# BUG-0296：TransportOptions.DialGuard 装配条件恒假，/v1 链连接层 SSRF 纵深自引入起从未生效

状态：已修复（2026-10-08，随外部复审生图 SSRF 加固批次实施，待发布验证）。

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
- 外部复审问题 1（生图下载 SSRF）为同批修复；问题 2/3/4/5/6/7 同批复审清单见会话交付说明。
