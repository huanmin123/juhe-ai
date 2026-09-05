# BUG-0159 K6 legacybridge 前缀翻转缺少并发保护

## 基本信息

- 编号：BUG-0159
- 状态：已关闭（随组件删除失效）
- 严重程度：P1
- 发现时间：2026-09-04
- 发现方式：自查（已提交 Git 历史审计）
- 模块：后端 / 网关 / 迁移
- 关联计划：`docs/migration/Node全量清零迁移总计划-20260904.md`
- 关联 bug：无
- 责任人：待定

> X01 收口（2026-09-05）：`internal/legacybridge` 过渡反代包连同 `kernel.RegisterFallback` 扩展点与 `JUHE_AI_LEGACY_BRIDGE_TARGET` 装配已整体删除，`kernel` 未命中路径固定返回 Node 404 JSON 契约。下列三个子项的修复门槛均以「bridge 继续存在」为前提，组件删除后不再适用；本文保留为历史缺陷证据。

## 问题概述

- 现象：Go `legacybridge.Bridge` 的 `RegisterPrefix`、`RemovePrefix` 与 `ServeHTTP` 读写 prefix slice 没有互斥保护。
- 期望：入口翻转和并发请求期间路由表一致，不发生 data race、越界或请求落到半更新状态。
- 实际：运行时翻转与请求并发时会并发读写 slice，`go test -race` 若覆盖该时序可报竞态；现有测试未做并发 flip。
- 进一步事实：`ReverseProxy` 使用 `Rewrite` 仅 `SetURL`，没有调用 `SetXForwarded`；Rewrite 会先清除出站 `X-Forwarded-*`，Node 收不到原客户端 IP 链，`trust proxy` 下 `req.ip` 可能退化为 bridge 连接地址。
- 影响范围：切片 G4 翻转窗口可能出现请求选择错误、竞态崩溃或 Node/Go owner 短暂重叠。

## 根因与证据

- 文件：`backend-go/projects/gateway/internal/legacybridge/legacybridge.go`。
- `RegisterPrefix`/`RemovePrefix` 修改共享列表，`ServeHTTP` 同时遍历该列表，未使用 mutex 或不可变快照；代理重写也未恢复 X-Forwarded 链。
- 提交测试只覆盖顺序注册、删除和代理，不覆盖并发翻转。

## 已确认子项：方法不匹配时 legacy bridge 不会接管

- 对照事实：K6 的目标是让 Go 成为唯一入口，已迁移的具体路由优先，仍由 Node 持有的同一前缀请求通过 fallback 代理。只要某个 HTTP 方法没有 Go 路由，且前缀仍在 bridge 列表中，该方法仍应到 Node。
- 历史 Go：`kernel.Handler` 先让 Go 1.22 `ServeMux` 匹配具体路径；当路径模式存在但方法不匹配时，`ServeMux` 返回 405，外层 `methodContractWriter` 将其改写为 404。该请求不会落到 `RegisterFallback` 挂载的 `legacybridge.Bridge`，因为 fallback 只处理完全没有匹配模式的路径。
- 可观察结果：例如 Go 只注册 `GET /__aisys__/api/items`、bridge 仍注册 `/__aisys__/api` 时，`POST /__aisys__/api/items` 在 Node 仍可由旧端点处理，Go 却直接返回 404；部分迁移期间同一路径的未迁移方法被错误屏蔽，造成接口不可达。
- 证据范围：历史提交 `21dc58a01` 的 `kernel.go`（`RegisterFallback`、`methodContractWriter`）与 `legacybridge/legacybridge.go`；该提交测试只覆盖完全未注册路径和翻转后的 404，未覆盖“已注册路径 + 未注册方法”。该结论不依赖当前未提交工作区。
- 修复门槛：在方法不匹配且前缀仍由 bridge 持有时继续代理到 Node；仅在 bridge 未命中时才保持 Node 404 语义，并补 GET/POST 同路径的 fallback 对照及翻转后 404 回归。

## 已确认子项：legacy bridge 前缀匹配缺少路径段边界

- 对照事实：Node 通过 Express `app.use(systemPrefix, ...)` / `app.use(systemApiPrefix, ...)` 挂载前缀；Express 的 mount path 只匹配该路径本身或其后续 `/` 段，不会把 `/__aisys__/api2` 当作 `/__aisys__/api` 子路径（已用 Node 运行时边界请求复核）。
- 历史 Go：`Bridge.ServeHTTP` 对每个注册前缀直接调用 `strings.HasPrefix(r.URL.Path, p)`，未要求路径等于前缀或以 `p + "/"` 开始。注册 `/__aisys__/api` 后，`/__aisys__/api2`、`/__aisys__/apix/anything` 都会命中代理。
- 可观察结果：拼写错误、相邻资源名或未迁移的相似前缀请求会被错误转发到 Node，Node/Go owner 边界扩大；错误请求可能返回旧系统的 200/鉴权结果，而正确语义应由 Go 404 或其他独立路由处理，造成安全与功能路由漂移。
- 证据范围：Node 历史 `backend/src/server.ts` 第 208–210、265–269 行的 Express 前缀挂载；Go 迁移提交 `21dc58a01` 的 `legacybridge.go` 第 64–68 行。结论不依赖当前未提交工作区。
- 修复门槛：使用路径段边界匹配（`path == prefix || strings.HasPrefix(path, prefix+"/")`，根前缀单独处理），并补相邻前缀、尾斜杠和 query 请求的代理/404 对照测试。

## 已确认子项：legacy bridge 丢失 `X-Forwarded-*` 客户端链

- 对照事实：Node server 开启 `trust proxy`；Node 的代理请求构造会保留既有 `X-Forwarded-For` 并追加当前客户端地址，同时传递 `X-Forwarded-Host` 与 `X-Forwarded-Proto`。未迁移路由在 Node 看到的 `req.ip`、协议和原始 Host 依赖这组头。
- 历史 Go：`legacybridge.New` 使用 `httputil.ReverseProxy.Rewrite` 调用 `SetURL`，但没有调用 `ProxyRequest.SetXForwarded`，也没有复制入站 `X-Forwarded-*`。Go 标准库在进入 `Rewrite` 前会删除 `Forwarded`、`X-Forwarded-For`、`X-Forwarded-Host`、`X-Forwarded-Proto`，因此出站请求只剩 bridge 到 Node 的连接信息。
- 可观察结果：经 Go bridge 到 Node 的请求会把客户端 IP 退化为 bridge 地址，Node 的基于 IP 限流、审计来源、访问日志和协议判断可能与 Node 直连结果不同；同一客户端在翻转期间还可能被按两个不同 IP 计数。该差异不是 header 排版问题，而是安全与业务结果变化。
- 证据范围：Node 历史 `backend/src/server.ts` 第 201–202 行启用 `trust proxy`，`backend/src/modules/db-service/db-service-http-proxy.ts` 第 146–168 行的 `appendForwardedFor`/三组 forwarded headers；Go 迁移提交 `21dc58a01` 的 `legacybridge.go` 第 23–34 行；Go 标准库 `net/http/httputil/reverseproxy.go` 在 `Rewrite` 前删除四组 forwarding headers。结论不依赖当前未提交工作区。
- 修复门槛：明确 bridge 的可信代理边界后，复制并追加入站 forwarding 链，再设置 host/proto；补真实 HTTP 请求的多级 `X-Forwarded-For`、无头和伪造头用例，断言 Node 侧 `req.ip`/协议/Host 与直连等价且不接受未受信任的客户端伪造。

## 修复与验证

- 修改点：使用读写锁或原子不可变路由快照，显式恢复受信任的 X-Forwarded 链，并补并发 register/remove/serve 的 race 与真实 client-IP 测试。
- 当前验证：未执行并发 race；目标 gateway 入口尚未完成正式切换。
- 结论：K6 翻转组件存在已确认的并发安全缺口，待修复。
