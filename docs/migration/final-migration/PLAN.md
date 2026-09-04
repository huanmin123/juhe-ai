# Node 全量清零迁移执行看板（PLAN）

> 唯一进度事实源。每个 WP 状态：pending / in-progress / archived。每片收口（G5）由主 Agent 更新本表并在切片记录目录落证据。
> **工作方式变更（2026-09-04，用户决策）**：放弃独立分支/worktree，全部迁移代码已合并回 master（merge 29b4b99f7，node-to-go-final 分支与 worktree 已删除），此后直接在主目录 master 上继续实现与提交。每片仍保持"实现→测试→提交→登记"节奏与 -race 门禁；主目录上维护者的未提交改动照旧不触碰。

## 合并记录（2026-09-04）

- merge 29b4b99f7：node-to-go-final（基线 f9c1fbeac 上的全部 W1+W2 迁移提交）并入 master，master 当时已前进至 6f9739e96（维护者：账户运行态重置/速度优先修复/排练修复），零文件交集无冲突。合并后主目录 37 个 Go 测试套件全绿。
- 被取消的 M08 accounts 子代理残留（backend-go/.../internal/accounts/ 半成品）已随 worktree 删除，M08 将在 master 重新派发。

## 基线记录（M0，2026-09-04）

- 本地 master：`f9c1fbeac`（== origin/master，无漂移）
- 迁移分支 HEAD：`node-to-go-final` @ `f9c1fbeac`
- 主目录 status：维护者有未提交改动（route-strategies speed-first/rehearsal 相关 + docs/plans 两文件），不属于本迁移，不触碰；本迁移计划文档已复制入 worktree。

## 波次进度

| 波 | 状态 | WP 明细 |
| --- | --- | --- |
| W1 | in-progress | K1 K2 K3 K4 K5 K6 K7 S-PG S-SQ + doc |
| W2+W3 | done | M01-M17 ✓（管理域完成）、C03 pricing ✓、G01-G04 网关协议 ✓、P03 delegated + P04 OIDC 本体 ✓（测试 W5 补齐）；46 套件 -race 绿 |
| W3 | done | M08-M14 |
| W4 | done | M15-M17 P01-P03 G01-G03 |
| W5 | done | G05 preauth + G06 body + G07 quota + G08 routing + G09 hybrid + G10 runtime-cache + P03/P04 测试补齐+契约修复 + P05 publicapilogs；gateway 51 套件绿 |
| W6 | pending | G11-G14 C01（G09/G10 已提前于 W5 完成） |
| W7 | pending | G15-G19 J-A J-B（波末 G20 启动） |
| W8 | pending | J-Ca J-Cb J-Cc J-D J-E J-F C01 |
| W9 | pending | G20(主Agent) C02 X01 X02 X03 |
| W10 | pending | X04 X05 X06 |

## 工作包状态

| WP | 状态 | 提交 | 证据 |
| --- | --- | --- | --- |
| K1 http-kernel | archived | b3115e675 | kernel 9 测试 -race 绿；契约对照 http-security/system-error-message/http-compression/deduplication/system-api-app |
| K2 session-auth | archived (mount-removed) | da4fd3f37 + c1c96f0a3 | authsys 8 测试绿；6 文件 SHA-256 manifest 于 final-archive/K2-session-auth.manifest.json；物理移动 P8（oidc/scripts 仍 import auth 工具） |
| K6 legacybridge | archived | 21dc58a01 | bridge 测试绿：代理/翻转摘除/keep-alive 安全 |
| K3 rate-limit | archived | 下一个提交 | 7 测试绿（IP 分钟/突发、用户分钟、allowlist/health 旁路、Redis Lua 多桶、429 Retry-After 契约） |
| K4 oplog-producer | pending | — | — |
| K5 invalidation-bus | pending | — | — |
| K6 legacybridge | pending | — | — |
| K7 mockupstream+golden | archived (核心) | mockupstream 提交 | OpenAI 协议仿真 + 16 场景矩阵 5 测试绿；golden 录制管线与 Anthropic/Gemini/Grok/Codex 仿真随 G01-G04 扩展 |
| S-PG ensure-schema PG | archived | schema 提交 | 614 语句逐字节等价（tsx 实跑 dump 比对 614/614）；166 表/422 索引/1 DO 块/2 触发器块；种子 164+13 幂等；人工复核 4 项（模型目录 upsert 待定价服务、默认 APIKey/路由策略待 AES 加密切片、外部 token 同、SQLite→PG 转换管线以内嵌终态语句替代）；PG 冒烟 opt-in env 待隔离库实跑 |
| S-SQ ensure-schema SQLite | archived | schema 提交 | 六库 DDL 逐字节等价、幂等、golden 表集断言绿；business 80 表/stats 62（J3b 注释段不计）；发现 4 组 Node 条件 ALTER 迁移需 P8 人工复核（已在 Go 文件头列明） |
| G05 preauth+preflight | archived (本波) | 本波提交 | 28 测试/61 分支 -race 绿；port 契约冻结（G13/G14/G15/G16/G17/G18 seam）；错误文案/状态码矩阵逐字节 |
| G06 body 管线 | archived (本波) | 本波提交 | 39 测试/236 用例 -race×3 绿；worker_threads→有界 goroutine 池（已批准适配）；阈值/413/408/503 文案逐字节 |
| G07 quota | archived (本波) | 本波提交 | 95 用例 -race×3 绿；port+配置轴替代 runtimeConfig 分叉；Redis key 与 Node 迁移期互通 |
| G08 routing | archived (本波) | 本波提交 | 116 用例 -race 绿；smooth-weighted/Redis-modulo 双语义、deadline 恰好边界、中文文案逐字节 |
| G09 hybrid | archived (本波) | 本波提交 | 60 测试/128 用例绿；评分/亲和/质检/修复/探索公平轮转全对齐；clock+rng 注入可回放 |
| G10 runtime-cache | archived (本波) | 本波提交 | 30 测试 -race 绿；inval 接线、单飞、世代重试、负缓存；只读 SQL 双模自建（管理面 store 无 runtime 读接口） |
| P03 delegated 测试+修复 | archived (本波) | 本波提交 | 33 测试绿；A-K 11 项契约偏差修复（HasBindings/name 可选/409 映射/inherited 过滤/profile/api-key PATCH/request-limits 完整实现） |
| P04 oidc 测试+修复 | archived (本波) | 本波提交 | 78 测试绿；7 项修复（ms/ns 混淆致授权码 born-expired、空表密钥引导 503、事务 500→400、毫秒时间戳、device_code 空串、表单体 500、pg LEAST）；空库全链路验收测试 |
| P05 publicapilogs | archived (本波) | 本波提交 | 24 测试 -race×5×count10 绿；Redis Stream 队列消灭→进程内有界 channel（已批准架构差异）；retention/溢出/停机 drain 对齐 |

（W2+ 的 M/P/G/C/J/X 各行随波次开启时补入。）

## 执行日志

- 2026-09-04 B0.1：基线 M0 记录（如上）；worktree 创建于 `F:\sub2api-lite-migration`；计划文档五件迁入 worktree。B0.2 端点级路由矩阵与逐 job 文件映射在 W1 由主 Agent 生成。
- 2026-09-04 K1：`gateway/internal/kernel` 完成——响应封装（{data,message}/{message}）、CJK 错误本地化（含裸字符串 payload、上游标记保留）、管理安全头（逐字节对照）、压缩（1024B 阈值/事件流跳过/缓冲语义）、no-store、256KiB JSON 限制（413/400 契约）、404/405 JSON 化、mutation 去重守卫（TTL/清理/键序全对照）、trace/client-ip 上下文。`go test -race` 9 项全绿。
- 2026-09-04 K2: 实现 authsys 包（会话/凭据复用 modelcheckauth+businessauth，captcha/login-guard/token 三态/系统账户 CRUD+乐观并发），翻转摘除 /auth 与 /system-accounts 挂载。调试期发现并修复两个内核缺陷：① compressionWriter 未拦截 WriteHeader 导致 gzip 头在快照后丢失（客户端解压为空）；② guard 自建 localizeWriter 使外层 auto-200 覆写内层 500（改为 context 共享 lw）。Node tsc --noEmit 通过（摘挂载后仍编译）。
- 2026-09-04 K6: legacybridge 前缀代理 + RegisterFallback 扩展点（sync.Once 幂等）。
- 2026-09-04 M01: announcements 双模式 store + 全路由（create/patch/publish/unpublish/delete/public/read-tracking/revision 冲突 409/currentRevision）+ mutation guard + 操作日志挂接。发现并修复 Patch 未递增 updated_at 的移植缺陷（Node 语义 revision=max(now,prev+1ms)）。挂载摘除推迟到 P8：scripts/ 与后续切片仍引用该模块（不变量：剩余 Node 必须可编译）。
- 2026-09-04 M02 顺延: ui-bootstrap /options 依赖 findUserReferenceData（accounts 域读模型），authorization-options grantee-* 依赖 authorizations 域 repository——按依赖规则移至 W3（M04/M08/M10 之后）。波次内顺延不阻塞其他片。
- 2026-09-04 M03→M04 顺序调整: G0 发现 system-teams 的 PATCH status=disabled/active 级联调用 resource-authorization-write 域的 revokeAllTeamSourcesAsync/reactivateTeamGrantSourcesAsync（含 refreshResourceAuthorizationEffectiveSource 重算），属 M04 核心写引擎。依赖优先：先 M04 再 M03，M03 状态变更届时接线。M03 其余语义（CRUD/成员/历史/访问域过滤）契约已读取完毕存于会话证据。
- 2026-09-04 M04+M03: authz 包（grant/source/runtime 状态机、effective source 四分支刷新、乐观并发、到期扫描、团队级联 RevokeAllTeamSources/ReactivateTeamGrants/RevokeTeamSourcesForMember）+ systemteams 包（CRUD/成员 20 上限/历史/访问域过滤/去重守卫）+ 双前缀路由。调试期间发现并修复两个移植缺陷：①手写 sprintf 逐字节 string(byte) 导致中文双重编码（改 fmt.Sprintf）；②authz id 生成用冻结时钟导致唯一冲突（改随机后缀，对齐 Node newId）。-race 全绿。
- 2026-09-04 M05（子代理交付，主 agent 复验通过）: groups 双挂载 CRUD/乐观锁/默认分组守卫/路由绑定守卫(100 上限)/级联删除+脏标记/去重守卫/操作日志，5 测试 -race 绿。顺延登记：①authorized 视角读分支（access_type=authorized，依赖 M04 runtime 查询，下一轮补挂）②group_account_stats 投影与 todayUsage 水合（J5）③refresh worker（J5）④网关缓存失效已接 inval 接口占位 ⑤options/edit-basic 等低价值面待 M08 后补。子代理报告 Node 函数→Go 方法对照完整（group-read/write/summary/limits 七文件全覆盖）。
- 2026-09-04 M06（子代理交付，主 agent -race 复验通过）: route-strategies 双挂载全 CRUD + 五模式（normal 速度优先六旋钮 10000-60000ms 等阈值 / hybrid_smart 评分配置+levelRoutes ≤5 档约束 / weighted·failover·round_robin 禁 config）+ 绑定 1-20 不重复 active 优先级唯一 + failover 首位主用校验 + 默认策略与 API Key 引用删除保护 + 乐观 409 currentUpdatedAt。顺延：速度优先运行态清理/K5 失效（RuntimeInvalidator 接口已注入）、options/edit-basic/speed-first-runtime 读端点、授权分组绑定分支（M04）。
- 2026-09-04 M07（子代理交付，主 agent -race 复验通过）: api-keys 双挂载切片，AES-256-GCM v1 信封与 Node crypto.ts 逐字节兼容（存量密文可解，密钥=sha256(runtimeSecret)），明文仅创建/refresh/secret 三处一次性返回，删除原子硬删+cleanup-target 同事务 upsert，可用性排程全量移植（跨午夜/DST）。顺延：PATCH /{id} 乐观锁更新、usage 渲染（J5）、schedule 时区读系统设置（暂进程时区）。
- 2026-09-04 M08（子代理交付，主 agent -race 复验通过）: accounts CRUD 核心切片（11 文件）：列表分页+八种过滤、详情/edit-basic、options、创建（revision 初始化+凭据封存）、编辑乐观锁（config_revision 递增）、锁定族（generation CAS）、软删+卫星清理、tags 三端点、NFKC 名称搜索。凭据 AES v1 信封与 Node crypto.ts 双向兼容。登记的 Go 加固偏差：编辑详情凭据返回掩码而非 Node 明文。顺延：授权实例视角 UNION 读、批量/导入导出/clone（M09）、runtime-reset（维护者 6f9739e96 新增，需对照）、余额健康探针读、circuit outbox 推进、创建量上限。
- 2026-09-04 M09（子代理交付，主 agent -race 复验通过）: accounts 批量/导入/导出切片。批量：2-100 账户同 owner 校验、16 字段 enabled-union 覆盖、逐账户 revision CAS 409、batchId+逐账户日志。导入：五 sourceMode（native/sub2api/newapi/cpa/oneapi）preview/confirm 两阶段、账户经 M08 Store.Create 落库（凭据密封/代理密码密封/分组自动创建）。导出：byIds/byFilters（500 上限）。顺延登记：5 个凭据配置字段批量覆盖（待凭据配置切片）、导入侧凭据归一化与 pending 健康投递、CPA YAML、日志 targets 字段（authsys sink 扩展）、导出 filters 严格 400 语义。
- 2026-09-04 检查点: master 直推模式运行正常（合并 29b4b99f7 后 6 个迁移提交）。M09 剩余补挂：clone-context（30+ 列投影，account-interaction-context.repository）与 tags/编辑明细已就绪。下一片 M10（授权实例视角读）→ M11-M17。全量回归 69 套件 ok，唯一失败仍是已知基线问题（BusinessOwnerManifest probe_cursor 断言）。
- 2026-09-04 M10+M11: M10 授权实例视角——authz.Store.AuthorizedReadableInstanceAccounts（直接授权+团队授权两条路径，active+未过期过滤），accounts 包 AuthorizedAccountReader 窄接口注入，my-accounts 列表对实例账户放行（3 个授权视角测试绿）。M11 providers 管理读（列表分页+keyword、详情 byCode/byID；写端点依赖定价服务顺延 C03）。另加固：临时令牌 ttlSeconds 严格整数校验（NaN/Inf/非整数拒绝）。providers 包为被配额中断的子代理遗留成品，主 agent -race 复验通过后补登记。
- 2026-09-04 M12-M17 六包并发归档（用户授权不限并发）：六子代理并行、包目录互不重叠、主 agent 统一 -race 复验（44 gateway 套件全绿零失败）后逐包提交。各包顺延项已在包注释与子代理报告登记：M12 settings 实为 60 key（53 为过期口径）、authsys OperationLogEntry 缺 visibilityScope/detailLevel/metadata 字段（K4 sink 扩展）、M14 hot-search/grep 文件面、M15 detail 端点、M16c OIDC 签名、M17 gemini 富化/刷新退避/openai 刷新状态机。W2 管理域主体完成，剩余补挂项与 W3 波次（M09 导入归一化、providers 写、M02 依赖域读模型）待续。
- 2026-09-04 W3 wave: 四子代理并行交付 C03 pricing（模型定价目录+计算器）、G01 gatewayproto（驱动注册接口）、G02 gatewayopenai（OpenAI 协议+8 测试绿，含 cacheWrite 链/tier 正则/seen map 三 bug 修复）、G03 gatewayanthropic + G04 gatewaygemini（协议本体）、P03 delegated + P04 oidc（半成品待补齐）。中断残留修复：pricing providerCode/IsInf、gemini urlPathUnescape/utf16→utf8、delegated 11 处编译错误（Profile/ApiKey 接口缺失/类型断言/method 调用）。
- 2026-09-04 W5 wave: 九子代理并发交付（G05 首派遇平台限流后补派成功）。G05 gatewaypreauth（preflight 编排+pre-auth+metadata+authorization-preflight+错误响应，9,351 行/28 文件，28 测试绿；对 G13/G14/G15/G16/G17/G18 冻结 port 契约）；G06 gatewaybody（有界 goroutine 池替代 worker_threads，39 测试/236 用例绿）；G07 gatewayquota（快照缓存/三域配额/inflight，95 用例绿）；G08 gatewayrouting（普通路由+超时档+协调预算，116 用例绿）；G09 gatewayhybrid（评分/亲和/质检/修复/热质量候选，60 测试绿）；G10 gatewayruntimecache（五域缓存+快照+内部注册表，30 测试绿）；P03 delegated 测试补齐（24 测试）并发现 A-K 11 项契约偏差→第二代理全部修复（33 测试绿）；P04 oidc 测试补齐（78 测试）发现 7 项偏差（含 ms/ns 混淆与空表引导两处严重缺陷）→第三代理全部修复+空库全链路验收；P05 publicapilogs（队列消灭架构，24 测试绿）。
- 2026-09-04 W5 过程纠偏: 复查发现子代理向 5 个 Node 参考文件（preflight.ts/session-affinity/session-identity）写入 173 行未在真实系统存在的 provider-anchor 实验代码，违反 Node 只读纪律；核实 Go 产物零污染后 `git checkout --` 全部回滚。后续波次提示词已强调该红线。
- 已知基线问题：`maintenance/internal/ownermanifest/TestVerifyRepositoryBusinessOwnerManifest` 在基线 f9c1fbeac 即失败（`account_api_key_pool_probe_cursor type line 844 is stale`；主目录存在维护者未提交的 route-strategies 改动，Go 校验器与已提交 Node 状态不同步）。本迁移不掩盖：该断言涉及的 Node 文件将在 M08-M10 归档时随迁移消失，届时此失败自然解除；W1-W3 每次全量回归将此失败记为 KNOWN-BASELINE-FAIL，不计入迁移回归门。
