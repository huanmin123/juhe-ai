# 工作包目录与 ≤10 并发调度（final-migration）

> 本文是《Node全量清零迁移总计划-20260904.md》的调度层：把全部迁移工作拆成 **69 个可独立验收的工作包（WP）**，按 **10 个波次**调度，**任意时刻并发子代理 ≤10**（平台限流实测约束，2026-09-04 二次复查后重排）。生命周期、mock 门禁、git 纪律、归档规则以总计划 §4/§5/§8/§9 为准，本文不重复。

## 1. 并发模型（上限 10）

| 角色 | 数量/波 | 职责 |
| --- | --- | --- |
| 主 Agent（集成者） | 1（不计入并发） | 共享文件独占（main.go 路由挂载、go.mod、legacybridge 规则、owner manifest、看板 G4 翻转提交）；切片收口与 git 提交 |
| 实现 Agent | ≤8 | 每人 1 个 WP：独立 worktree/分支，精确文件 owner，交付 Go 包 + 双模式测试 + golden diff 自证 |
| 测试/Mock Agent | ≤1 | 波内横切：跑 mock 矩阵、golden diff 复核、race、双模式 store 抽查 |
| 文档 Agent | ≤1 | 维护看板 PLAN.md、切片记录、证据链接、docs/functions 同步 |
| 复查 Agent | ≤1 | G4 前独立语义复查（对照 Node oracle 抽查路由/失败路径/权限矩阵），有一票否决权 |

规则：
1. 每个 WP 卡片必须写明：**Node 参考源（只读）→ Go 目标路径（独占）→ 依赖 WP → 验收门禁**。文件 owner 不重叠才可同波并行。
2. `go.mod` 依赖只在 W1 冻结；此后任何 WP 需要新依赖 → 停止并上报主 Agent 裁决。
3. 每个 WP 的路由注册通过包内 `Register(mux, deps)` 暴露，主 Agent 统一挂载——实现 Agent 永不改 main.go / legacybridge。
4. golden 先录制后实现（G0）；测试 Agent 复核 diff（G3）；复查 Agent 抽查（G4 前）；文档 Agent 随波登记（G5）——"编写、测试、文档、复查"四线并行。
5. 波次内 WP 失败不阻塞他人；跨波依赖未就绪则该 WP 顺延到下一波。

## 2. 波次总表（10 波 × ≤10；impl=实现 Agent 数）

| 波 | WP | 组成 |
| --- | --- | --- |
| W1 | K1-K7, S-PG, S-SQ | 9 impl + 1 doc：内核 7 包（K8 已并入 K7）+ schema 双模式；**K2 收口即完成第一次翻转归档（auth+system-accounts）** |
| W2 | M01-M07 | 7 impl + 测试/文档/复查 |
| W3 | M08-M14 | 7 impl + 测试/文档/复查 |
| W4 | M15-M17, P01-P03, G01-G03 | 9 impl + 1 doc：管理域收尾（含 M17 四 OAuth）+ 公开面 + 网关协议驱动开工（只依赖 K7 harness） |
| W5 | G04-G08, P04, P05 | 7 impl + 测试/文档/复查 |
| W6 | G09-G14, C03 | 7 impl + 测试/文档/复查（pricing 先行，J-F 依赖） |
| W7 | G15-G19, J-A(含 J-INF), J-B | 7 impl + 测试/文档/复查；波末主 Agent 启动 G20 整链翻转 |
| W8 | J-Ca, J-Cb, J-Cc, J-D, J-E, J-F, C01 | 7 impl + 测试/文档/复查 |
| W9 | G20(主 Agent), C02, X01, X02, X03 | 网关翻转收口 + 4 impl |
| W10 | X04, X05, X06 | 前端收口、全量验收、文档收场（主 Agent + 文档） |

## 3. 工作包卡片

### K 内核（W1）

| WP | 范围 | Node 参考 | Go 目标 | 依赖 | 验收 |
| --- | --- | --- | --- | --- | --- |
| K1 | HTTP 内核：ServeMux、中间件链（顺序=system-api-app.ts:117-216）、压缩、安全头、错误本地化、413/no-store/404/405、优雅停机、静态托管、/metrics、**mutation 去重守卫**（modules/deduplication 的 operationKey+fingerprint 幂等中间件，管理写接口在用）、db-service 准入控制等价（max in-flight → 进程内有界并发） | server.ts、shared/http-security|request-context|system-error-message|http-compression、modules/deduplication/(275) | gateway/internal/kernel, httpsrv | — | 中间件顺序单测 + echo 路由 e2e + 去重守卫并发测试 |
| K2 | 会话鉴权纵切片（含翻转归档 auth+system-accounts） | modules/auth/(1,392)、system-accounts/(227)、storage/system-account*、system_team* 表 | gateway/internal/session, sysaccounts | K1 | W3 契约 + 双模式 + captcha 开关 + 会话撤销 |
| K3 | 两级限流 | modules/system-api/system-api-rate-limit.middleware.ts(476) | gateway/internal/ratelimit | K1 | 429 语义/allowlist bypass/30s 缓存版本失效 |
| K4 | 操作日志 producer API | modules/operation-logs/ 写侧、F4 契约 | gateway/internal/operationlog 扩展 | — | 32 producer 语义对照 |
| K5 | 缓存失效总线 | shared/gateway-cache-invalidation.ts(494)、cache.ts(487)：4 topic + settings:system/global | gateway/internal/inval | — | Redis 版本 bump 断言 + 内存模式 |
| K6 | legacybridge 过渡代理 | —（新造） | gateway/internal/legacybridge | K1 | 透传登录/SSE/429 三链路 |
| K7 | Mock AI 上游 harness + golden 录制管线（原 K8 并入）：OpenAI 协议仿真 + 场景脚本 + 对 Node 录制 请求/响应/流 三元组的 fixture 规范 | protocols/openai-v1 语义、backend 全栈（录制源） | shared/platform/mockupstream（test-only）、final-migration/golden/ | — | 场景矩阵可脚本化回放；对 Node 网关录通 1 条完整链 |
| S-PG | applyPostgresSchema → maintenance --ensure-schema（PG） | storage/postgres-schema.ts、postgres-seed-defaults.ts、postgres-*-gate.ts | maintenance/internal/schema | — | fresh PG 与 Node 建库逐对象 diff=0 |
| S-SQ | SQLite 六库 ensure-schema + seeds | storage/schema/*.ts、database.ts | maintenance/internal/schema | — | fresh SQLite diff=0；dev 库幂等升级 |

### M 管理域（W2/W3，每包=读+写+副作用+翻转归档）

| WP | 功能 | Node 参考（storage 域文件数） | 依赖 | 复杂度 |
| --- | --- | --- | --- | --- |
| M01 | announcements | modules/announcements + 2 repo | K1-K5 | 小 |
| M02 | ui-bootstrap + authorization-options + diagnostics + **`/__aisys__/help` 会话与帮助静态** | 38+75+27 行模块 + server.ts helpPrefix/requireHelpSession | K1-K5 | 小 |
| M03 | system-teams + my-teams | modules/system-teams + system_team* repo | K2 | 小 |
| M04 | authorizations + my-authorizations（含 usage 明细/summary） | modules/authorizations + 17 repo 域 | K2,K4 | 中 |
| M05 | groups + my-groups | modules/groups + 8 repo 域 | K2,K4,K5 | 中 |
| M06 | route-strategies + my-route-strategies | 3 repo 域 | M05 | 中 |
| M07 | api-keys + my-api-keys（secret 生命周期） | 8 repo 域 | K2,K4,K5 | 中 |
| M08 | accounts CRUD/详情/运行态/锁定（读+写） | accounts 域 62 repo 中的管理面子集 | K2,K4,K5 | 大 |
| M09 | accounts 批量/导入导出/clone-context/模型同步 | accounts 域批量侧 | M08 | 大 |
| M10 | accounts 选项/标签/列表投影/搜索 | accounts 域 projection 侧 | M08 | 中 |
| M11 | providers + proxies | 7+1 repo | K2,K4 | 中 |
| M12 | settings（53 key 读+写、在线禁改时区、双失效）+ **免登录 GET /api/settings/public** | settings.repository + settings:system/global 缓存 + system-api-app.ts:19 | K5 | 小 |
| M13 | table-monitor 管理读（挂 F2 Go store） | modules/table-monitor | S-SQ | 小 |
| M14 | usage-records/my-usage-records 读 + stats/my-stats 读 | usage-stats 域 21 repo 的读侧 + loaders | S-PG/S-SQ | 中 |
| M15 | operation-logs/my-operation-logs、audit-logs、runtime-logs、public-api-logs 读 + ip-stats 读+写 | 514+804+1,178+1,007+307 行 + 各 1-6 repo | K2 | 中 |
| M16 | response-inspection-policies + external-integration-sources + oauth-management | 307 + external-integrations 域 8 repo | K2,K4 | 中 |
| M17 | **四供应商 OAuth 账户授权纵切片**（复查补充）：admin + my-\* 双挂载的 auth-url、create-from-code、create-from-refresh-token、accounts/:id/refresh-token、reauthorize-from-code/-from-refresh-token；grok 另有 sso-to-oauth 与 SSO 设备流；gemini capabilities；凭据 AES-GCM 加密与 oauth-credential-rotation | modules/openai-oauth(2,472)/anthropic-oauth(1,047)/gemini-oauth(1,869)/grok-oauth(1,869)、storage/oauth-credential-rotation.repository.ts | K2,K4,M08（写 accounts） | 中 |

### P 公开面（W3/W6）

| WP | 功能 | Node 参考 | 依赖 |
| --- | --- | --- | --- |
| P01 | /__aipublic__ 外部维护 + external-integrations 公开读 | W1b 契约 + 8 repo 域 | K2 |
| P02 | /oauth 回调 + /.well-known | 四 oauth 模块公开侧 | K2 |
| P03 | delegated-api（/__aidelegated__/v1） | modules/delegated-api(818) | M04 |
| P04 | OIDC provider | modules/oidc-provider(2,210) + oauth_* 8 表 | K2 |
| P05 | F5 公开接口日志 writer：capture→有界 channel→直接异步写+retention | modules/public-api-logs(1,007) + Redis Stream public-api-logs 队列（消灭） | K1,K4；M15 已读 |

### G 网关链（W4/W5，采用网关分解报告 18 包；G20=翻转归档）

| WP | 包名 | 主要文件（modules/gateway/ 下） | 复杂度 |
| --- | --- | --- | --- |
| G01 | 协议驱动注册 | protocols/_shared、registry、三 driver | 小 |
| G02 | OpenAI 协议 | protocols/openai-v1/*（3.9k，除 driver） | 大 |
| G03 | Anthropic 协议 | protocols/anthropic-v1/* | 中 |
| G04 | Gemini 协议（含 interaction affinity） | protocols/gemini-v1beta/* | 中 |
| G05 | 鉴权+preflight 编排 | request/pre-auth、preflight(1,926)、authorization-preflight、metadata | 大 |
| G06 | body 管线（worker 线程解析→进程内有界解析） | request/body*、json-* | 大 |
| G07 | 配额 | quota/*（2.2k）+ request-quota-limit.schema.ts | 中 |
| G08 | 普通路由策略 | routing/*、policy/timeout-profile | 中 |
| G09 | hybrid 智能路由 | hybrid/*(2.7k)、hot-quality-candidate | 中 |
| G10 | runtime-cache 只读缓存 | runtime/runtime-cache(1.7k)、snapshot、registry | 中 |
| G11 | 账户熔断 | runtime/account-circuit*(5)、suppression、recoverable-wait、probe-coordinator | 大 |
| G12 | 账户 key 池与副作用 | runtime/account-api-key-*、account-side-effects(2,945)、key-model-* | 大 |
| G13 | client-ip/热质量/速度优先（可拆 13a/13b） | runtime/client-ip-*(4)、hot-quality-*(8)、speed-first、latency-degradation、proxy-health、并发队列、**client-ip-policy-hit-buffer 队列** | 大 |
| G14 | 会话身份与亲和 | session-identity/*、session-affinity | 小 |
| G15 | 派发引擎+上游传输+codex 适配 | dispatch/*(5.3k)、upstream/*(2.8k)、adapters/gpt-codex(1.2k) | 大 |
| G16 | 响应流+终态 | response/*(8.3k：stream 2,002、finalization 2,306) | 大 |
| G17 | usage+audit 交接 | usage/*(2.7k)、audit/*(1.1k)（F3 已有） | 中 |
| G18 | codex 桥 | codex-responses/*(1.8k)、client-profiles/codex-*、strategy、codex_source_fence_settled 结算语义 | 大 |
| G19 | 观测+诊断 | observability(797)、diagnostics(283) | 小 |
| G20 | 全场景 record-replay 回归归零 + 前端 e2e + **翻转 /v1 归档整个 gateway 模块** | 主 Agent 执行 | — |

依赖：G01-G04 依赖 K7；G05/G15 依赖 M07/M08/M05（key/账户/分组读）、K5；G11-G13 依赖 G10；G16 依赖 G02-G04；G17 依赖 F3（已有）与 J-F 接口；G18 依赖 G02 与 codex-context store（C02 前置）；J-B 依赖 M17；J-F 依赖 C03；G20 依赖 G01-G19 全部。

### C 附属（W6）

| WP | 功能 | Node 参考 |
| --- | --- | --- |
| C01 | my-chat | modules/chat(8,328) + chat 域 8 repo + chat_* 10 表 |
| C02 | openai-compatible 五件套 | files/vector-stores/images/computer/code-interpreter（~2.3k）+ 2 repo |
| C03 | model-pricing | modules/model-pricing(4,920)（必须先于 J-F） |

### J 后台任务（W6/W7，jobs 项目）

| WP | 任务族 | 门禁 |
| --- | --- | --- |
| J-A | J3c：account-quality-refresh、failure-precheck、cooldown-retest(+queue)。**前置 J-INF**：jobs 运行记录/租约基建对齐 stats 库 `background_task_runs`、`background_job_leases` 表语义（task-run-reconcile 的对账对象） | fence/CAS/恢复 |
| J-B | J4：openai-oauth 刷新（复用 M17 刷新 service 语义）、三供应商保活、授权到期 sweep、availability 同步 ×2 | 加密刷新 golden；依赖 M17 |
| J-C | J5 统计 12 job（可拆 a: usage 聚合 5 / b: 窗口 4 / c: client-ip+group+一致性 3） | 双实现对账 diff=0 |
| J-D | J6 retention：data/chat/expired-account/codex-context/record-maintenance 5/cleanup retry 2 | 各域独立 owner |
| J-E | 运行维稳：circuit-recovery、control-plane、key-model-memory(复用现有包)、speed-first-probe、list-availability、manual-test-queue、task-run-reconcile、balance-auto-detect。**复查补充**：internal-api 派发接口 `POST /__aiinternal__/v1/account-test/dispatch`（HMAC+loopback，签名域 `juhe-ai:account-test-dispatch:v1`）与 account-health-check-dispatch 一并接管；Go 形态（gateway 进程内直调 or jobs loopback 端点）在该片 G0 冻结 | kill-restart 恢复 |
| J-F | F6 usage writer：直接异步写 + shard + 失败终态 + pricing freeze + usage-semantics 契约（modules/usage-semantics, 15 行） | W7 契约；依赖 C03 |

### X 终局（W7/W8）

| WP | 内容 |
| --- | --- |
| X01 | db-service/worker/IPC 退役 + legacybridge 删除 + SQLite 单写者终验 |
| X02 | storage/shared/domain/config/入口归档 + Node 指标字段删除清单执行 |
| X03 | 部署 go-only：start.sh|ps1、docker、Jenkins、package/validate-release、owner-manifest 全 go |
| X04 | 前端收口：env/代理、J3b UI gate |
| X05 | 全量验收（总计划 §1 判据 3-5，双模式 fresh） |
| X06 | 文档收场：看板全 archived、架构/functions 文档同步、终局报告入 docs/reports/ |

## 4. 波次内角色分配示例（以 W4 为例）

```text
主Agent：集成/挂载/基线
impl×9：M15 M16 M17 P01 P02 P03 G01 G02 G03   （文件 owner 互不重叠）
doc ×1：看板+切片记录
test/rev：由主 Agent 在波末统一调度（golden diff 对 G01-G03 协议驱动、M17 OAuth 加密封套抽查）
```

## 5. 与总计划的关系

- 总计划 §4 生命周期（G0-G5）、§5 mock 门禁、§7 入口翻转、§8 git 纪律、§9 文档体系**不变**，WP 是其在 ≤10 并发下的实例化调度单元。
- 每完成一个 WP = 总计划里的一个或半个切片（M 包=1 切片；G 包为网关整链的内部分片，对外翻转仍只有 G20 一次）。
- 看板 `PLAN.md` 按 WP 记录状态/提交 SHA/证据；本目录其余 inventory-* 文件是 W1 的对照事实源。
