# 精确路由 Owner 清单设计

## 1. 目标与边界

本设计把 `deploy/owner-manifest.json` 从四大域粗粒度声明扩展为可选的 `HTTP method + path template` 精确声明，使管理接口、公开接口和网关接口可以按已验证纵切面逐条登记 owner，而不必等待整个域同时切换。

本批只交付声明格式、严格校验、解析函数和回滚 manifest 生成能力，不接入 Caddy、Nginx、Node、Go 或任何代理 dispatch，不改变当前监听、启动、owner lock、数据库 writer 和生产流量。当前仓库的 `routeAllowlist` 为空，四大域仍全部由 Node 持有。

worker 不是 HTTP route，继续只使用 `routeOwners.worker`；禁止伪造 method/path 规则拆分 worker owner。

## 2. Schema v2

`routeOwners` 保留并作为没有精确命中时的默认 owner。`rollbackRouteOwners` 保存四大域的上一个可恢复 owner。`routeAllowlist` 只保存明确列出的 HTTP 路由：

```json
{
  "schemaVersion": 2,
  "deploymentEpoch": "go-management-slice-2026-07-22-001",
  "release": {
    "nodeVersion": "0.1.0",
    "goVersion": "0.1.0-w0",
    "schemaVersion": 67
  },
  "routeOwners": {
    "management": "node",
    "public": "node",
    "gateway": "node",
    "worker": "node"
  },
  "rollbackRouteOwners": {
    "management": "node",
    "public": "node",
    "gateway": "node",
    "worker": "node"
  },
  "routeAllowlist": [
    {
      "surface": "management",
      "method": "GET",
      "path": "/__aisys__/api/accounts/{accountId}",
      "owner": "go",
      "rollbackOwner": "node"
    }
  ]
}
```

旧 release 的 schema v1 仍可被校验器读取，便于审计和人工回退；自动回滚要求 v2，因为 v1 没有足够的前一 owner 证据。`deploy/owner-manifest.schema.json` 描述结构和基础枚举，`scripts/validate-owner-manifest.mjs` 额外承担 JSON Schema 无法直接表达的模板相交、surface 前缀和回滚语义校验。

## 3. 精确 allowlist 规则

- `surface` 只允许 `management`、`public`、`gateway`。management 必须位于 `/__aisys__/api`，public 必须位于 `/__aipublic__`，gateway 禁止占用这两个保留前缀，且首段必须是 literal，防止 `/{surface}/...` 模板覆盖保留前缀。
- method 必须是明确的大写 HTTP method；不接受 `ANY`、小写或 `*`。`GET` 与 `HEAD` 按常见路由器的隐式 HEAD 行为视为可能重叠。
- path 区分大小写，禁止 query、fragment、百分号编码、反斜杠、空段、尾斜杠和 `.` / `..` 段。
- 动态参数只允许占据完整 segment 的 `{name}`，名称必须以字母开头且单条模板内不重复。禁止 `*`、`**`、`:id`、`{id}.json` 和正则表达式。
- 任意两条 method 可能相交且 path template 存在共同匹配输入时，manifest 整体拒绝。例如 `/accounts/{id}` 与 `/accounts/current` 不能同时声明；应合并为一个模板 owner，或先调整路由契约使其不相交。
- `owner` 与 `rollbackOwner` 必须都是 `node` 或 `go` 且互不相同。清单最多 2048 条，避免发布预检出现无界二次比较。
- schema 和每层对象都拒绝未知字段，拼写错误不能被静默忽略。

精确解析函数先查 allowlist，再回退 `routeOwners[surface]`。不规范或未列出的请求路径只回退粗粒度 owner，不能模糊匹配到 Go。该函数目前仅供校验和测试，不得被误写为已完成生产 dispatch。

## 4. 切换与回滚流程

精确切换前必须先证明目标 Go route 的契约、依赖、writer、前端调用和最小 smoke，并确保 Node 与 Go 对同一路由不会同时接收生产写流量。然后在新 `deploymentEpoch` 中增加 allowlist 项，`owner=go`、`rollbackOwner=node`，执行：

```powershell
pnpm test:owner-manifest
pnpm validate:owner-manifest
```

全 Node 发布预检使用 `pnpm validate:owner-manifest:node`，它同时检查四大粗粒度 owner 和所有精确项，不能被精确 Go 项绕过。

生成只读回滚候选：

```powershell
node scripts/validate-owner-manifest.mjs --print-rollback=rollback-2026-07-22-001 deploy/owner-manifest.json
```

命令只向标准输出打印一个新 manifest：交换粗粒度 current/rollback owner，并交换每条精确路由的 `owner/rollbackOwner`。它不写仓库、不发布、不修改代理。新 epoch 是必填项且不能与当前 epoch 相同，防止 owner lock 把回滚误认成同一代发布。

生产回滚仍须走发布预检、目标进程 readiness、反向代理原子切换和稳定性观察；生成 manifest 不是回滚成功证据。

## 5. 后续接入门禁

后续代理 dispatch 接入必须单独设计并满足：

1. 代理使用与本校验器一致的 method、path 解码和模板匹配语义，不能自行发明宽松 wildcard。
2. 启动前验证 manifest、release、schema 和 deployment epoch；任何解析差异 fail-closed。
3. 同一 method/path 只有一个 owner，写接口不能镜像双写；影子验证只能发送明确无副作用的读请求。
4. 切流和回滚各自有独立 epoch、readiness、请求探针、错误率观察和旧 owner 保留窗口。
5. Node 删除只能发生在精确路由已经稳定归属 Go、回滚演练完成且对应减法清单证据齐全之后。

## 6. 验证覆盖

`scripts/validate-owner-manifest.test.mjs` 覆盖 v1 兼容、v2 空/非空 allowlist、粗粒度回退、模板命中、未知 method、unsafe wildcard、编码/非规范路径、surface 越界、owner 非法、重复/相交模板、GET/HEAD 语义、全 Node 断言和自动回滚往返。
