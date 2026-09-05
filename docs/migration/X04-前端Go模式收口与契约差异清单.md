# X04 前端 Go 模式收口与契约差异清单

> 状态：已完成（2026-09-04）。前端已具备显式的 Go 后端连接模式（`VITE_JUHE_AI_DEPLOY_MODE`），默认值保持 `node`，默认行为与历史版本零差异。
> 本文是前端视角的收口文档：连接面盘点、go 模式切换方法、J3b UI gate 核对结论，以及前端指向 Go gateway 主入口时的契约差异（降级面）清单。
> Go 侧挂载矩阵的权威事实来源：`backend-go/projects/gateway/cmd/juhe-ai-gateway/compose.go` 顶部挂载矩阵注释与 `Mount` 调用、`main.go` 的 J3b 管理 listener；Node 侧对照 `backend/src/modules/system-api/system-api-app.ts`。

## 1. 结论

1. 前端默认（不设置 `VITE_JUHE_AI_DEPLOY_MODE`）行为零变化：API 基址、dev 代理、J3b 门禁均与历史版本一致。
2. go 模式（`VITE_JUHE_AI_DEPLOY_MODE=go`）当前是**显式声明 + dev 代理辅助**：Go gateway 主入口监听与 Node 相同的端口约定（`JUHE_AI_HOST:JUHE_AI_PORT`，缺省 `127.0.0.1:3000`），因此 dev 代理默认 target 不随模式变化；模式主要影响声明语义、构建日志与后续运行时分支读取点。
3. **go 模式下管理面存在已知的降级面**（第 5 节清单）：审计日志、运行日志、公开 API 日志、授权选项、统计、使用记录、代理配置、表监控、ui-bootstrap、帮助中心等前缀 Go gateway 未挂载，纯 Go 拓扑下返回 Node 404 JSON 契约（`{"message":"资源不存在"}`）。仓库当前没有任何位置配置 `JUHE_AI_LEGACY_BRIDGE_TARGET`，因此不存在"bridge 自动回落 Node"的既成事实；需要零降级的 go 模式演练必须由运维显式配置 bridge 指向 Node origin。
4. J3b UI gate（模型检测入口）核对结论：**无偏差，不需要修改门禁逻辑**（第 4 节）。
5. 顺带修复一个存量问题：`frontend/vite.config.js` 与 `frontend/vite.config.d.ts` 是历史编译产物，vite 加载配置时 `vite.config.js` 优先于 `vite.config.ts`，导致此后 `.ts` 侧任何配置改动（包括本次 go 模式）均不生效。两个产物文件已删除，vite 现在直接加载 `vite.config.ts`。

## 2. 前端连接面盘点

前端指向后端的全部配置点如下：

| 配置点 | 消费位置 | 作用 |
| --- | --- | --- |
| `VITE_JUHE_AI_BACKEND_TARGET` | `frontend/vite.config.ts` dev proxy（缺省 `http://127.0.0.1:3000`） | dev 服务器把 `^/__aisys__/help(/|$)`、`^/__aisys__/api(/|$)`、`/v1` 三条代理转发到该 target |
| `VITE_JUHE_AI_BACKEND_TARGET`（dev 下） | `scripts/dev.mjs` → `scripts/dev-config.mjs` `resolveDevelopmentBackendTarget` | dev 编排从进程环境 / frontend env / backend env 的 `JUHE_AI_HOST:JUHE_AI_PORT` 推导并注入前端子进程 |
| `VITE_JUHE_AI_API_BASE_URL` | `frontend/src/api/http.ts`（axios baseURL，缺省 `/__aisys__/api`）、`frontend/src/api/modelCheckStream.ts`（SSE fetch 基址） | 运行时管理 API 请求基址 |
| `VITE_JUHE_AI_GATEWAY_BASE_URL` | `frontend/src/views/api-keys/ApiKeysView.vue`、`frontend/src/views/external-integration-sources/externalSourceApiDocs.ts` | API Key 页面展示/复制的网关地址（纯展示；后者 dev 下回退 `VITE_JUHE_AI_BACKEND_TARGET`） |
| `VITE_JUHE_AI_J3B_ENABLED` | `frontend/src/router/index.ts` `isJ3bUiEnabled` | 构建期 J3b（模型检测）UI 门禁，正式发布包固定 `false` |
| `VITE_JUHE_AI_BUILD_ID` | `frontend/vite.config.ts`（缺省取 `git rev-parse HEAD`） | 构建版本清单 `build-info.json` |
| `VITE_JUHE_AI_DEPLOY_MODE`（本次新增） | `frontend/vite.config.ts`、`frontend/src/config/deployMode.ts` | 前端后端连接模式：`node`（缺省）/ `go` |
| `VITE_JUHE_AI_J3B_BACKEND_TARGET`（本次新增，可选） | `frontend/vite.config.ts` dev proxy | 把 `/__aisys__/api/model-checks`、`/__aisys__/api/my-model-checks` 代理到 J3b 管理 listener（缺省 `127.0.0.1:3307`）；未设置时不产生代理条目 |

外部注入链路（本次未改动）：`scripts/package-release.sh|.ps1` 注入 `VITE_JUHE_AI_API_BASE_URL`、`VITE_JUHE_AI_GATEWAY_BASE_URL`、`VITE_JUHE_AI_BUILD_ID` 并固定 `VITE_JUHE_AI_J3B_ENABLED=false`；`docker/Dockerfile.builder` 与 `Jenkinsfile` 以 build-arg 透传 `VITE_JUHE_AI_BUILD_ID` / `VITE_JUHE_AI_J3B_ENABLED`。

运行时前端只有一个 API 基址（`/__aisys__/api`），所有管理 API 经 `frontend/src/api/http.ts` 的 axios 实例与 `apiUrl()` 发出；公开面 `/__aipublic__` 与网关 `/v1` 不由前端页面直接调用（仅在文案与文档中出现）。

## 3. go 模式实现与切换方法

### 3.1 语义

- `VITE_JUHE_AI_DEPLOY_MODE=node`（缺省）：与历史行为完全一致。
- `VITE_JUHE_AI_DEPLOY_MODE=go`：声明当前构建/开发代理指向 backend-go gateway 主入口（对应部署侧 `JUHE_AI_DEPLOY_MODE=go` 的拓扑，见 `docs/migration/部署go-only双轨开关.md`）。非法值在 vite 启动时直接报错（`VITE_JUHE_AI_DEPLOY_MODE 必须是 node 或 go`）。
- 两种模式下 gateway 主入口监听同一默认端口约定，因此 dev 代理 target 逻辑不变；本地 Go gateway 使用非默认端口时照旧用 `VITE_JUHE_AI_BACKEND_TARGET` 覆盖。

### 3.2 构建期注入

`vite.config.ts` 将模式经 `define` 注入为 `__JUHE_AI_DEPLOY_MODE__`；`frontend/src/config/deployMode.ts` 导出运行时读取点：

- `frontendDeployMode`：`'node' | 'go'`（tsx 回归脚本环境下回退 `import.meta.env`，再回退 `node`）。
- `isGoBackendMode`：是否指向 Go gateway 主入口。

当前没有 UI 分支消费该读取点（降级面通过后端 404 自然呈现，见第 5 节）；它为后续"go 模式下隐藏/降级入口"类小改提供唯一读取位置，避免再散落 `import.meta.env` 判断。

### 3.3 dev 切换步骤

1. `frontend/.env`（参考 `.env.example`）设置 `VITE_JUHE_AI_DEPLOY_MODE=go`，并把 `VITE_JUHE_AI_BACKEND_TARGET` 指向 Go gateway 主入口（缺省端口下即 `http://127.0.0.1:3000`；非缺省端口改端口）。
2. 按拓扑启动 Go gateway（`JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true`；需要 `/v1` 网关链时加 `JUHE_AI_GATEWAY_CHAIN_ENABLED=true`）。
3. 需要联调模型检测 UI 时：先构建时开启 `VITE_JUHE_AI_J3B_ENABLED=true`，再设置 `VITE_JUHE_AI_J3B_BACKEND_TARGET`（如 `http://127.0.0.1:3307`）让 dev 代理把 model-checks 两个前缀转发到 J3b 管理 listener。
4. 已知降级页面见第 5 节；需要它们可用时，要么保持前端指向 Node（`node` 模式），要么给 Go gateway 显式配置 `JUHE_AI_LEGACY_BRIDGE_TARGET` 指向 Node origin。

## 4. J3b UI gate 核对结论

事实核对（均已在源码确认）：

1. 门禁实现：`frontend/src/router/index.ts` 定义 `isJ3bUiEnabled = import.meta.env.VITE_JUHE_AI_J3B_ENABLED === 'true'`；`requiresJ3b: true` 标注在 `/my-model-checks`（self）与 `/model-checks`（admin）两条路由；菜单过滤在 `frontend/src/layouts/AppLayout.vue`（`visibleMenuRoutes`），路由守卫在 `router/index.ts`（`to.meta.requiresJ3b && !isJ3bUiEnabled` 时重定向）。
2. 门禁是**纯构建时开关，与后端连接模式（node/go）完全正交**：前端不存在 goOwner/ownerMode 类运行时判断（`goRuntime` 相关代码只是 Go 运行时指标图表，数据来自 `/stats/system-metrics/*`，属第 5 节 stats 降级面）。
3. 后端事实：Node 主后端（`system-api-app.ts`）**不挂载** model-checks；Go gateway 主入口（`compose.go`）同样**不挂载**。模型检测 API 由 Go gateway 进程内独立的 J3b 管理 listener 独占提供（`main.go`：`JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS`，缺省 `127.0.0.1:3307`，挂载 `/auth/`、`/__aisys__/api/model-checks/`、`/__aisys__/api/my-model-checks/`），生产流量依赖部署层（反代）把这两个前缀路由到该入口。
4. 结论：go 模式下 J3b 门禁展示语义与 node 模式完全一致（开启 `VITE_JUHE_AI_J3B_ENABLED=true` 且部署层路由 model-checks 前缀到 J3b 入口时可用，否则构建期门禁直接隐藏入口，不会出现必然 404 的菜单项）。**门禁逻辑无需修改**；本次补充的 `VITE_JUHE_AI_J3B_BACKEND_TARGET` dev 代理填补了本地联调路径，不改变默认行为。

回归：`pnpm --dir frontend test:model-check-route-availability` 通过（`isJ3bUiEnabled` 源码契约未被破坏）。

## 5. go 模式契约差异清单（降级面）

对照基准：Node `system-api-app.ts` 挂载前缀 vs Go gateway 主入口 `compose.go` 实际 `Mount`/`Register`（截至本文日期的 master 源码）。

前提事实：

- Go kernel 未注册路径的缺省 fallback 是 Node 404 JSON 契约（`kernel.go`：`{"message":"资源不存在"}`）；只有配置 `JUHE_AI_LEGACY_BRIDGE_TARGET` 时才转发 Node origin。仓库内当前无任何位置设置该变量。
- 以下清单描述**前端指向 Go gateway 主入口且未启用 legacy bridge**时的行为；配置 bridge 后下表"降级"列全部变为"由 bridge 回落 Node，功能可用"。

### 5.1 两种模式下均可用（Go 已挂载）

`/auth/*` 与 `/system-accounts`（authsys）、`/announcements` + `/my-announcements`、`/my-accounts` + `/accounts`、`/my-groups` + `/groups`、`/my-route-strategies` + `/route-strategies`、`/my-api-keys` + `/api-keys`、`/my-authorizations` + `/authorizations`、`/my-{anthropic,gemini,grok,openai}-oauth` + 管理端同名前缀（oauthmgmt）、`/providers` + `/my-providers`、`/response-inspection-policies`、`/settings`、`/my-teams` + `/system-teams`、`/my-operation-logs` + `/operation-logs`（logreads/F4）、`/ip-stats`、`/external-integration-sources`、`/oauth`（管理端 OIDC 客户端管理，policyreads.OAuthDeps）、`/__aisys__/api/health`、`/v1`（需 `JUHE_AI_GATEWAY_CHAIN_ENABLED=true`）、`/my-chat`（随 chain 挂载）、`/__aidelegated__/v1`、`/.well-known` + `/oauth` 公开协议面。

### 5.2 Node 挂载、Go 未挂载（纯 go 模式下降级）

| 前缀 | 前端消费 | go 模式降级表现 |
| --- | --- | --- |
| `/__aisys__/api/audit-logs`（含 `/search-hot`、`/:id`、`/:id/payloads/:payloadId`） | `auditLogsApi` → 审计日志页面（`views/audit-logs/`） | 页面打开后列表/检索/详情/payload 请求 404 |
| `/__aisys__/api/runtime-logs`（含 facets、grep 系列） | `runtimeLogsApi` → 运行日志页面（`views/runtime-logs/`） | 列表、facets、grep 全部 404 |
| `/__aisys__/api/public-api-logs` | `publicApiLogsApi` → 公开 API 日志页面（`views/public-api-logs/`） | 列表与详情 404 |
| `/__aisys__/api/authorization-options` + `/my-authorization-options`（grantee-accounts/teams/groups） | `authorizationOptionsApi` / `myAuthorizationOptionsApi` → 授权创建/编辑受托人选项（`views/authorizations/useAuthorizationOptionState.ts`） | 授权表单的受托人（账户/团队/分组）懒加载选项 404；授权列表本身（`/authorizations`，Go 已挂载）仍可用 |
| `/__aisys__/api/stats` + `/my-stats` | `statsApi` / `myStatsApi` → 统计概览、用量统计、AI 性能、系统指标、AI 健康等页面 | 各统计图表数据 404（含 `/stats/system-metrics/go-runtime-*` Go 运行时指标展示） |
| `/__aisys__/api/usage-records` + `/my-usage-records` | `usageRecordsApi` / `myUsageRecordsApi` → 使用记录页面（`views/usage-records/`） | 使用记录列表 404 |
| `/__aisys__/api/proxies` | `proxiesApi` → 代理配置页面（`views/proxies/`）、账户表单代理选项（`views/accounts/useAccountProxyOptions.ts`） | 代理管理不可用；账户编辑的代理下拉选项 404 |
| `/__aisys__/api/table-monitor` | `tableMonitorApi` → 表存储监控页面（`views/table-monitor/`） | 概览/历史/清理 404 |
| `/__aisys__/api/ui-bootstrap` + `/my-ui-bootstrap` | `uiBootstrapApi` / `myUiBootstrapApi` → 用户引用数据（`composables/useUserReferenceData.ts`，多页面共享的选项懒加载） | 引用数据选项 404 |
| `/__aisys__/help/*`（含 `/user/`、`/admin/` 静态帮助站） | `AppLayout.vue` 帮助入口链接、登录后重定向目标（Node `server.ts` Web 层服务） | 帮助中心入口 404；Go gateway 无该 Web 层 |

判断与风险：上述前缀中，日志三件套（audit/runtime/public-api-logs）与授权选项是 Go 侧已登记的"仍由 Node 拥有"项（`compose.go` 挂载矩阵注释：F1 dataset reader 未迁移、authorization-options 顺延 W3→W3 final-migration）；stats/usage-records/proxies/table-monitor/ui-bootstrap 同样未出现在 Go 挂载矩阵中，属于尚未迁移的读面。go 模式正式翻转（X03/G20）前，这些页面必须在"Go 挂载补齐"与"bridge 回落"之间给出明确归宿，不能默认纯 go 拓扑可用。

### 5.3 与模式无关的说明

- `/model-checks` + `/my-model-checks`：两种后端模式下都不在主入口上，均依赖部署层路由到 J3b 管理 listener（第 4 节），不属于 go 模式新增差异。
- `/__aipublic__` external-integrations legacy family：Go 无对应包（`compose.go` 矩阵注明），但前端页面不直接调用该面，仅外部集成方使用；对前端 UI 无感。
- `/v1` 网关链与 `/my-chat`：Go 侧要求 `JUHE_AI_GATEWAY_CHAIN_ENABLED=true` 才挂载；纯 go 拓扑若未开启 chain，`/v1` 与 AI 问答页面随 bridge 归宿决定可用性。

## 6. 验证记录

| 验证 | 命令 | 结果 |
| --- | --- | --- |
| 默认模式构建（零行为基线） | `pnpm --dir frontend build`（`vue-tsc -b` + `vite build`） | 通过（18.33s，无新增告警） |
| go 模式构建 + J3b 代理注入 | `VITE_JUHE_AI_DEPLOY_MODE=go VITE_JUHE_AI_J3B_BACKEND_TARGET=http://127.0.0.1:3307 vite build` | 通过；启动日志输出 go 模式与 J3b 代理 target 两行提示 |
| 非法模式拒绝 | `VITE_JUHE_AI_DEPLOY_MODE=hybrid vite build` | 失败并报错 `VITE_JUHE_AI_DEPLOY_MODE 必须是 node 或 go`（预期） |
| J3b 路由源码契约 | `pnpm --dir frontend test:model-check-route-availability` | 通过 |
| Build ID / 发布注入契约 | `pnpm --dir frontend test:frontend-build-info` | 通过 |

未做（按任务约束）：浏览器手工验证与截图（AGENTS.md 红线）；Go 后端实际请求联调（属 X03/部署工作包验证范围，本文第 5 节基于源码挂载矩阵核对，未做运行时请求验证）。

## 7. 改动文件清单

| 文件 | 改动 |
| --- | --- |
| `frontend/vite.config.ts` | 新增 `VITE_JUHE_AI_DEPLOY_MODE` 解析与校验、`__JUHE_AI_DEPLOY_MODE__` define 注入、go 模式启动日志、可选 `VITE_JUHE_AI_J3B_BACKEND_TARGET` dev 代理条目（置于 `^/__aisys__/api` 之前保证优先匹配）；默认 proxy 集合与历史一致 |
| `frontend/src/env.d.ts` | 新增 `__JUHE_AI_DEPLOY_MODE__` 全局常量声明 |
| `frontend/src/config/deployMode.ts` | 新增：运行时模式读取点（`frontendDeployMode` / `isGoBackendMode`） |
| `frontend/.env.example` | 补充 `VITE_JUHE_AI_DEPLOY_MODE`、`VITE_JUHE_AI_J3B_BACKEND_TARGET` 注释化说明（默认不启用）；`VITE_JUHE_AI_BACKEND_TARGET` 注释补充 Go gateway 端口约定 |
| `frontend/vite.config.js`、`frontend/vite.config.d.ts` | 删除：历史编译产物，且 `vite.config.js` 会优先于 `.ts` 被加载，导致 `.ts` 配置改动失效 |
| `docs/migration/X04-前端Go模式收口与契约差异清单.md` | 新增本文 |
| `docs/migration/README.md` | 导航新增本文条目 |
