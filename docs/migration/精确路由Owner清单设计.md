# 精确路由 Owner 清单设计

## 1. 目标与边界

本设计把 `deploy/owner-manifest.json` 从四大域粗粒度声明扩展为可选的 `HTTP method + path template` 精确路由声明和 `job` 精确 worker 声明，使 HTTP 纵切面与后台任务都可以按已验证单元登记 owner，而不必等待整个域同时切换。

HTTP `routeAllowlist` 仍只提供声明、严格校验、解析和回滚候选生成能力，尚未接入 Caddy、Nginx 或其他生产代理 dispatch。worker `workerAllowlist` 已接入 Go worker 启动门禁和 Node cooldown scheduler 注册门禁，但当前仓库两类 allowlist 都为空，四大域仍全部由 Node 持有，不改变生产流量。

worker 不是 HTTP route，禁止用 method/path 规则拆分 worker owner。schema v3 使用独立 `workerAllowlist`；未列出的 job 回退 `routeOwners.worker`。

## 2. Schema v3

`routeOwners` 保留并作为 HTTP route 或 worker job 没有精确命中时的默认 owner。`rollbackRouteOwners` 保存四大域的上一个可恢复 owner；`routeAllowlist` 保存 HTTP 路由，`workerAllowlist` 保存后台任务：

```json
{
  "schemaVersion": 3,
  "deploymentEpoch": "go-management-slice-2026-07-22-001",
  "release": {
    "nodeVersion": "0.1.0",
    "goVersion": "0.1.0-w0",
    "schemaVersion": 85
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
  ],
  "workerAllowlist": [
    {
      "job": "cooldown-account-retest",
      "owner": "go",
      "rollbackOwner": "node"
    }
  ]
}
```

schema v1/v2 仍可被校验器识别，Go/Node worker gate 兼容 v2 的全局 worker owner；当前发布格式是 v3。发布 schema 必须等于当前 Goose catalog；自动回滚要求 v2 或 v3，因为 v1 没有足够的前一 owner 证据。`deploy/owner-manifest.schema.json` 描述当前 v3 结构和基础枚举，`scripts/validate-owner-manifest.mjs` 额外承担模板相交、重复 job 和回滚语义校验。

## 3. 精确 allowlist 规则

- `surface` 只允许 `management`、`public`、`gateway`。management 必须位于 `/__aisys__/api`，public 必须位于 `/__aipublic__`，gateway 禁止占用这两个保留前缀，且首段必须是 literal，防止 `/{surface}/...` 模板覆盖保留前缀。
- method 必须是明确的大写 HTTP method；不接受 `ANY`、小写或 `*`。owner 解析严格匹配 method，`GET` 不会隐式接管 `HEAD`；两者需要分别切流时必须分别登记。
- path 区分大小写，禁止 query、fragment、百分号编码、反斜杠、空段、尾斜杠和 `.` / `..` 段。
- 动态参数默认只允许占据完整 segment 的 `{name}`，名称必须以字母开头且单条模板内不重复。为覆盖 Gemini native action，额外允许受限的 `{model}:literalAction`；例如 `/v1beta/models/{model}:generateContent`。仍禁止 `*`、`**`、Express `:id`、`{id}.json`、多个参数拼接和正则表达式。
- 任意两条相同 method 的 path template 存在共同匹配输入时，manifest 整体拒绝。例如 `/accounts/{id}` 与 `/accounts/current` 不能同时声明；应合并为一个模板 owner，或先调整路由契约使其不相交。
- `owner` 与 `rollbackOwner` 必须都是 `node` 或 `go` 且互不相同。清单最多 2048 条，避免发布预检出现无界二次比较。
- schema 和每层对象都拒绝未知字段，拼写错误不能被静默忽略。

精确解析函数先查 allowlist，再回退 `routeOwners[surface]`。不规范或未列出的请求路径只回退粗粒度 owner，不能模糊匹配到 Go。该函数目前仅供校验和测试，不得被误写为已完成生产 dispatch。

## 4. 精确 worker job 规则

- `job` 必须是小写 kebab-case，最多 256 项且禁止重复；`owner` 与 `rollbackOwner` 必须是互不相同的 `node` / `go`。
- 解析时先精确匹配 `workerAllowlist[].job`，未命中再回退 `routeOwners.worker`。不允许前缀、通配符、角色名或 CLI alias 模糊匹配。
- Go CLI 把当前 Cobra 子命令名注入 runtime gate。schema v3 只有解析后的当前 job owner 为 Go 才能继续；schema v2 继续要求全局 `routeOwners.worker=go`。
- Node 仅在 owner lock 显式开启时读取同一 manifest 和 deployment epoch；当前 job 解析为 Go 时不注册对应 scheduler。配置、文件、JSON、schema 或 epoch 无法确认时，该 job fail-closed，不得因解析失败退回 Node 双开。
- `JUHE_AI_GO_WORKER_EXCLUSIVE_OWNER` 与 `JUHE_AI_LEGACY_NODE_WORKER_DRAINED` 是当前 job 的发布证据，不代表所有 Node worker 都已排空。每个独立 Go job 进程必须使用独立的 release 外绝对 owner lock path，避免不同 Go job 互相争用同一 OS lock。
- job 级切换必须先停止 Node 对应 scheduler、排空或使旧任务 fence 失效，再开启 Go job；仅修改 manifest 不能证明 drain、consumer 单 owner 或 outcome writer 单 owner。

## 5. 切换与回滚流程

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

命令只向标准输出打印一个新 manifest：交换粗粒度 current/rollback owner，并交换每条精确路由和 worker job 的 `owner/rollbackOwner`。它不写仓库、不发布、不修改代理或 scheduler。新 epoch 是必填项且不能与当前 epoch 相同，防止 owner lock 把回滚误认成同一代发布。

生产回滚仍须走发布预检、目标进程 readiness、反向代理原子切换和稳定性观察；生成 manifest 不是回滚成功证据。

worker job 切换还必须执行 Node scheduler drain、Go readiness/真实依赖 smoke、单 consumer 与单 writer 观察；回滚时先停止 Go job并确认不再 claim，再发布新 epoch 并恢复 Node scheduler。不能只交换 JSON 后同时运行两端。

## 6. 后续接入门禁

后续代理 dispatch 接入必须单独设计并满足：

1. 代理使用与本校验器一致的 method、path 解码和模板匹配语义，不能自行发明宽松 wildcard。
2. 启动前一次性验证 manifest、release、schema 和 deployment epoch，并从已验证快照构建不可变路由索引；不能在每个请求上重复执行模板相交的二次校验。任何解析差异 fail-closed。
3. 同一 method/path 只有一个 owner，写接口不能镜像双写；影子验证只能发送明确无副作用的读请求。
4. 切流和回滚各自有独立 epoch、readiness、请求探针、错误率观察和旧 owner 保留窗口。
5. Node 删除只能发生在精确路由已经稳定归属 Go、回滚演练完成且对应减法清单证据齐全之后。

## 7. 验证覆盖

`scripts/validate-owner-manifest.test.mjs` 覆盖 v1/v2 兼容、v3 两类 allowlist、当前 Goose schema、粗粒度回退、路由模板与 worker job 精确命中、非法/重复 job、全 Node 断言和自动回滚往返。Go runtime gate 与 Node worker-owner regression 分别覆盖命令名注入、job fallback、epoch fail-closed 和 scheduler drain；真实双进程 handoff/rollback 仍属于 W7 验收门禁。
