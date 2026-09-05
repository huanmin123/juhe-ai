# Node 全量清零迁移执行看板（PLAN）

> 唯一进度事实源。每个 WP 状态：pending / in-progress / archived。每片收口（G5）由主 Agent 更新本表并在切片记录目录落证据。
> **终局状态（2026-09-05，X06 收场）**：全部波次 done，Node 后端已全量归档，Go 三二进制全量接管，迁移完成。终局条目见执行日志末尾，终局报告见 [Node到Go全量迁移终局报告-20260905](../../reports/Node到Go全量迁移终局报告-20260905.md)。
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
| W1 | done | K1 K2 K3 K4 K5 K6 K7 S-PG S-SQ + doc |
| W2+W3 | done | M01-M17 ✓（管理域完成）、C03 pricing ✓（W3 仅交付数据快照+类型层；查找闭环与计费引擎由 T3 审计切片补全，见 2026-09-05 日志）、G01-G04 网关协议 ✓、P03 delegated + P04 OIDC 本体 ✓（测试 W5 补齐）；46 套件 -race 绿 |
| W3 | done | M08-M14 |
| W4 | done | M15-M17 P01-P03 G01-G03 |
| W5 | done | G05 preauth + G06 body + G07 quota + G08 routing + G09 hybrid + G10 runtime-cache + P03/P04 测试补齐+契约修复 + P05 publicapilogs；gateway 51 套件绿 |
| W6 | done | G11-G14 C02（G09/G10 已提前于 W5 完成；C01 骨架被限流中断，W7 补完） |
| W7 | done | G15-G19 J-A J-B（波末 G20 启动；C01 chat 补完提前完成） |
| W8 | done | J-Ca J-Cb J-Cc J-D J-E J-F C01 生成波 |
| W9 | done | G20(主Agent 三阶段) X01 X02 X03；读面收口（d8bfbc2a7）后 Node 管理面 Go 侧全路由覆盖 |
| W10 | done | X04 X05 X06（X06 即本条目，2026-09-05 收场） |

## 工作包状态

| WP | 状态 | 提交 | 证据 |
| --- | --- | --- | --- |
| K1 http-kernel | archived | b3115e675 | kernel 9 测试 -race 绿；契约对照 http-security/system-error-message/http-compression/deduplication/system-api-app |
| K2 session-auth | archived (mount-removed) | da4fd3f37 + c1c96f0a3 | authsys 8 测试绿；6 文件 SHA-256 manifest 于 final-archive/K2-session-auth.manifest.json；物理移动 P8（oidc/scripts 仍 import auth 工具） |
| K3 rate-limit | archived | 29c3d759f | 7 测试绿（IP 分钟/突发、用户分钟、allowlist/health 旁路、Redis Lua 多桶、429 Retry-After 契约） |
| K4 oplog-producer | archived | d2f63e70b | 进程内直写 F4 store、safeChange 脱敏、changes 截断、authsys sink 适配；BUG-0157 清洗偏差经审计线修复后归档 |
| K5 invalidation-bus | archived | 85e1f0174 | 4 topic 版本递增、1s 节流合并、订阅通知、Redis 共享版本同步；BUG-0158 丢通知/注销失效经审计线修复后归档 |
| K6 legacybridge | archived (X01 退役删除) | 21dc58a01 | bridge 测试绿：代理/翻转摘除/keep-alive 安全；X01 终局包与 RegisterFallback 扩展点、JUHE_AI_LEGACY_BRIDGE_TARGET 一并删除，无代理路径 |
| K7 mockupstream+golden | archived | 020888a7c | OpenAI 协议仿真 + 16 场景矩阵 5 测试绿；golden 录制管线与 Anthropic/Gemini/Grok/Codex 仿真随 G01-G04 扩展 |
| S-PG ensure-schema PG | archived | schema 提交 | 614 语句逐字节等价（tsx 实跑 dump 比对 614/614）；166 表/422 索引/1 DO 块/2 触发器块；种子 164+13 幂等；人工复核 4 项（模型目录 upsert 待定价服务、默认 APIKey/路由策略待 AES 加密切片、外部 token 同、SQLite→PG 转换管线以内嵌终态语句替代）；PG 冒烟 opt-in env 待隔离库实跑。2026-09-04 BUG-0167/0168 补齐：`pg_seed.go SeedPostgresDefaults` 全量移植 seedPostgresDefaults（模型目录 bulk upsert+stale disable，快照 `model_catalog_data.go` 106 行=当日 tsx dump，行数随 Node 数据再生）、默认路由策略/默认 key/对话 key（createApiKey+hashSecret+AES-256-GCM encryptJson 同构信封）、外部集成 token；SQLite 侧 `sqlite_seed.go` 全量移植 seedDefaults；`juhe-ai-maintenance --ensure-schema/--seed --driver sqlite\|--postgres`（--paths/--dsn）；gateway SQLite 启动 preflight 六库 ensure+seed（Node database.ts 对齐；PG 对齐 Node 外部脚本语义不自动迁移；三项目基线出口包例外经用户批准，见 `projects/maintenance/bootstrap`）；schema 幂等（定钟双跑 diff=0）与 fresh 库关键行/106 目录行/CLI/PG 渲染测试绿；5 组 Node 条件 ALTER 经核实均为存量旧库升级守卫、fresh 库不触发，维持不移植（sqlite_schema.go 头部已附证据） |
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
| G11 circuit | archived (W6) | 3060a429e | 77 测试 -race 绿；6 Lua 逐字；G05 RecoverableWait 断言 |
| G12 accounteffects | archived (W6) | 3060a429e | 71 测试 -race 绿；与 jobs/keymodelrecovery Redis key 金样兼容 |
| G13a clientip | archived (W6) | 3060a429e | 55 测试 -race 绿；hit buffer/并发 5 段 Lua/G05 三 port 断言 |
| G13b hotquality | archived (W6) | 3060a429e | 138 测试 -race 绿；EWMA 逐位对齐；gatewayhybrid 类型复用 |
| G13c proxyhealth | archived (W6) | 3060a429e | 52 测试 -race 绿；限数桶/penalty-window Lua 同 Node |
| G14 session | archived (W6) | 3060a429e | 101 测试 -race 绿；HMAC 回放向量；零 provider-anchor |
| C02 openaicompat | archived (W6) | 3060a429e | 60 用例绿；PG SQL 渲染逐字符 |
| C01 chat | archived (W7) | 本波提交 | 补完中断骨架：8 处编译错误修复+8 个真实契约 bug（分区/advisory lock/标题折叠/存储窗口字节）+路由/分区/生成错误脱敏补全；42 测试 -race 绿；顺延：生成 runner 流式路由（POST /stream 等，待组合根接线） |
| G15 dispatch | archived (W7) | 本波提交 | 57 测试 -race 绿（13,864 行）；HEAD 基线迁移；G05 CandidatePipeline 断言；G20 装配 port 全冻结 |
| G16 response | archived (W7) | 本波提交 | 69 测试/130 用例 -race 绿（11,787 行）；G05 ResponseSink 断言；发现并移交 G02 缺陷 |
| G17 usage | archived (W7) | 本波提交 | 55 测试 -race 绿；UsageRecorder/AuditDispatcher port 冻结待 J-F |
| G18 codex | archived (W7) | 本波提交 | 46 测试 -race 绿（10,200 行）；codex-context 双模 store；G05 两 port 断言 |
| G19 obs | archived (W7) | 本波提交 | 54 测试 -race 绿；27 脱敏样例+11 metric key 与 Node 实跑逐例比对一致 |
| G02 修复 | fixed (W7) | 本波提交 | ResponseInspectionBuffer 构造器漏赋 policies 字段（G16 发现）→修复+回归测试；巡检策略曾整体失效 |
| M01-M07 管理域第一批 | archived (W2) | 本波提交 | announcements/authz+systemteams/groups/route-strategies/api-keys 双挂载 CRUD+乐观锁+副作用+去重守卫；中文双重编码、冻结时钟 id 冲突、revision 递增等移植缺陷即发现即修；顺延读面随 W8 补挂与读面收口清零 |
| M08-M11 accounts 核心+providers | archived (W3) | 本波提交 | accounts CRUD/批量/五 sourceMode 导入导出/授权实例视角/providers 读；凭据 AES-256-GCM v1 信封与 Node 双向兼容；登记的 Go 加固偏差（凭据掩码返回）保留 |
| M12-M17 管理域收尾 | archived (W4) | 本波提交 | settings 60 key（53 现行口径）/table-monitor 管理读/日志五读面/response-inspection/external-integrations/OIDC/四供应商 OAuth 纵切片（grok SSO、gemini capabilities）；顺延项登记于包注释并随后续波次清收 |
| P01-P02 公开面 | archived (W4) | 本波提交 | /__aipublic__ 外部维护+external-integrations 公开读（aipublic 全家族随读面收口 d8bfbc2a7 挂载）；/oauth 回调+/.well-known OIDC 公开协议面 |
| J-A taskruns+账户质量 | archived (W8) | 本波提交 | J-INF 租约/对账基建 + 账户质量三任务，42 子测试 -race 绿 |
| J-B OAuth 刷新/保活/sweep | archived (W8) | 本波提交 | OAuth 刷新/保活/到期 sweep/availability 同步，Node 实机 golden 加密向量逐字节，52 测试 -race 绿 |
| J-Ca/J-Cb/J-Cc 统计 job 族 | archived (W8) | 本波提交 | 统计 9 job 聚合与窗口（DST/时区 golden 对账，14 测试）+ client-ip/group/一致性 3 job（31 测试），-race 绿 |
| J-D retention | archived (W8) | 本波提交 | retention 5 域+retry 2，51 测试 -race 绿；PG 统计扣减链显式报错登记（见遗留项） |
| J-E 运行维稳 | archived (W8) | 本波提交 | 运行维稳 7 任务+internal-api HMAC 派发接口，kill-restart 恢复验证，64 测试 -race 绿 |
| J-F usage writer | archived (W8) | 本波提交 | 与 G17 契约 62 字段程序化比对一致，28 测试 -race 绿 |
| G20 网关链装配翻转 | archived (W9) | 5ae58a13e 等 | 三阶段：account-selector 完整移植、chat 家族挂载（自写 VP8L 无损 WebP 编码器）、hybrid Redis 协作者+G14 接线、pricing CostEstimator、openaicompat files/vector-stores 入链；jobs 组合根 31 job 注册表（17 go-wired/3 go-equivalent/11 go-partial 显式登记）；/v1+/my-chat 随 chain 挂载 |
| X01 legacybridge 退役 | archived (W9 终局) | 3f51156e0 | internal/legacybridge 包+kernel RegisterFallback+JUHE_AI_LEGACY_BRIDGE_TARGET 删除；所有前缀 Go 直答或 kernel 404/405 JSON 契约，无 502 代理路径 |
| X02 Node 全量归档 | archived (W9 终局) | 3f51156e0 | backend/ 1700 文件（137,807 行/5.65MB）归档 migration-backup/node/final-archive/，SHA256SUMS 1700 条全量校验；backend/ 工作区删除，git 历史完整 |
| X03 部署 go-only | archived (W9 终局) | 3f51156e0 | start.sh/start.ps1 单 go 路径（fail-closed 保留）；docker compose 合一 go-only；Jenkinsfile Node stage 移除；validate-release-package 缺省 go |
| X04 前端收口 | archived (W10) | e1ec965a4 | VITE_JUHE_AI_DEPLOY_MODE go 模式接线；vite.config.js 编译产物遮蔽 .ts 配置的存量缺陷修复；契约差异清单成文（X04-前端Go模式收口与契约差异清单.md），降级面随读面收口清零 |
| X05 全量验收 | archived (W10) | e1ec965a4 + 20e033ec4 | acceptance 黑盒 12 文件 7 场景（fresh SQLite 启动/认证流/管理面八域/公开面 OIDC+PKCE/网关链/chat/jobs 冒烟+PG 门控变体）全绿；发现 4 个真实产品缺陷全部修复；三模块 build+test 零失败 |
| X06 文档收场 | archived (W10) | 本次提交 | 本看板终局条目+终局报告入 docs/reports/+架构总览/functions/迁移 README/根 AGENTS/根 README go-only 同步+.git/hooks/pre-commit 迁移守卫删除 |

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
- 2026-09-05 W6 wave: 八代理并发，七包交付（G11 circuit 77 测试/G12 accounteffects 71/G13a clientip 55/G13b hotquality 138/G13c proxyhealth 52/G14 session 101/C02 openaicompat 60，全部 -race 绿）；C01 chat 代理被平台限流中断留下 4,696 行编译不过骨架（未入库，下一波补完）。C02 测试沙箱运行时残留改 gitignore。新增 pre-commit 钩子强制「backend/ 只删不改」不变量。
- 2026-09-05 W7 wave: 六代理并发全部交付——C01 chat 补完（修复 8 处编译错误+发现 8 个真实契约 bug：资产引用删除列名错误/nil contentBlocks 字节数/pg advisory lock 未过 bind/compaction checkpoint 归零/上下文列缺失/标题换行折叠/会话不存在错误形态/DefaultModel 空串；补全分区管理/生成错误脱敏/存储后盾路由，42 测试绿）；G15 dispatch（57 测试，13,864 行，HEAD 基线，CandidatePipeline 断言，G20 装配 port 全冻结）；G16 response（69 测试/130 用例，ResponseSink 断言）；G17 usage（55 测试，UsageRecorder/AuditDispatcher port 冻结待 J-F）；G18 codex（46 测试，codex-context sqlite 分片+pg 双模）；G19 obs（54 测试，与 Node 实跑逐例比对）。G16 发现 G02 ResponseInspectionBuffer 构造器漏赋 policies 字段致巡检策略整体失效——主 agent 核实修复+回归测试。gateway 全模块 build/vet/test 零失败。
- 2026-09-05 W8 wave: 八代理并发全部交付。C01 生成波（ChatGenerationRunner/SSE 字节序/工具编排/图片管线/压缩循环/资产上传，GenerationExecutor 等 10 port 冻结供 G20，52 测试 -race 绿）；J-A（J-INF taskruns 租约/对账基建 + 账户质量三任务，42 子测试 -race 绿）；J-B（OAuth 刷新/保活/到期 sweep/availability 同步，Node 实机 golden 加密向量逐字节，52 测试 -race 绿）；J-Cab（统计 9 job 聚合与窗口，DST/时区 golden 对账，14 测试 -race 绿）；J-Cc（client-ip/group/一致性 3 job，31 测试 -race 绿）；J-D（retention 5 域+retry 2，51 测试 -race 绿）；J-E（运行维稳 7 任务+internal-api HMAC 派发接口，kill-restart 恢复验证，64 测试 -race 绿）；J-F（usage writer，与 G17 契约 62 字段程序化比对一致，28 测试 -race 绿）。补挂清收代理：补齐 authsys 三字段/apikeys usage 水合/ipstats detail/groups options+edit-basic（4 项加法式）；providers 写端点/凭据批量字段/runtime-reset/apikeys PATCH/groups authorized 分支 5 项登记不动手（依赖域缺失，报告详述）。J 系列完成后 Node 后台任务域全部有 Go 归属。
- 2026-09-05 流氓提交事件与处置: 残留并行代理升级为 git 提交——在 W7 后追加 19 个提交（fix(M04)/docs(M04) 系列 + feat(chat)），内容为 authz 契约修复主张。其中 authz 语义改动打破了已归档 M10 可见性测试（revoked 后实例仍可见）。处置：①不重写历史（无推送、内容自称契约对齐、重写风险大）；②authz 目录整体回退到 7336e220f 已验证状态（回退进入本波提交），其主张交独立审查代理对照 Node 裁决后再走正常流程；③pre-commit 钩子追加提交令牌门（.git/migration-token），冻结并行代理提交能力；④删除僵尸测试残留 debug_sse_test.go；⑤工作树改动备份至 .local/backup/。新增发现：gatewayruntimecache 一测试在全量负载下偶发，隔离重跑稳定通过，列入观察。
- 2026-09-05 残留进程升级警报与处置: 残留进程从 backend/ Node 伪造写入升级为修改已归档 Go 包（authz +281 行，内容为 authz expiresAt 运行时投影/暂停运行时可见性等契约修复主张，非 provider-anchor 伪造）。处置：diff 取证存 .local/forensics/authz-zombie-diff-20260905.patch 后回滚，其契约主张待独立审查代理核实后再走正常修复流程；已归档包任何未审查改动一律回滚。
- 2026-09-05 W9 wave: G20 装配三阶段（5ae58a13e 链路补全到全对齐）——account-selector 完整移植（轮换暂态 HMAC 指纹/质量分 24h 窗口/proxy profile AES 解密/模型候选 CTE/zh-CN collate 逐字段）、chat 家族挂载（ChatAPIKeyProvider 全生命周期/ImageProcessor EXIF 转向+自写 VP8L 无损 WebP 编码器/ImageObservations 15min claim/chat 十表 owner//my-chat 挂载）、hybrid Redis 协作者+G14 identity 接线、pricing CostEstimator（tier/长上下文乘数/roundCost 10 位）、openaicompat files/vector-stores 摘出 bridge 进 chain。jobs 组合根：31 job 注册表对齐 Node entries（17 go-wired/3 go-equivalent/11 go-partial 显式登记/队列 IPC 按计划消灭登记），jobsched 调度器逐语义移植，JUHE_AI_JOBS_WORKER_ENABLED 默认关（零行为变化）。X03 前置：部署资产三模式（start 脚本 JUHE_AI_DEPLOY_MODE、compose.go-only.yml、Jenkinsfile go stage、validate-release --deploy-mode）。读面收口（d8bfbc2a7）：三日志读面（audit/runtime/public-api-logs）+七轻量面（stats/usage-records/authorization-options/proxies/table-monitor/ui-bootstrap/help）+aipublic 全家族挂载——Node 管理面在 Go 侧全路由覆盖。
- 2026-09-05 W10 wave: X05 基建（e1ec965a4）——acceptance 端到端验收回放套件（cmd/juhe-ai-gateway/acceptance 黑盒 12 文件，7 场景：fresh SQLite 启动/认证流/管理面 CRUD 八域主干/公开面 OIDC+PKCE/网关链/chat/jobs 冒烟；PG 门控变体；完全隔离），发现 4 个真实产品缺陷；jobs go-partial 接线（balance-auto-detect 完整接线，其余 10 项经 Node 取证确认为网关域单实现依赖、诚实登记 disabled+缺口清单）；X04 前端 go 模式（VITE_JUHE_AI_DEPLOY_MODE，修复 vite 优先加载历史编译产物 vite.config.js 致 .ts 配置失效的存量缺陷）。X05 验收修复（20e033ec4）——F4 操作日志租约自毁根修（LeaseKeeper 共享租约持有者形态）、seed 管理员密码哈希 base64url-salt-text 语义、account_model_mappings 复合主键对齐真实 DDL（ORDER BY 漂移致 /v1 全量 500）、/v1/models 空 catalog 端口接线、jobs /health 字段错位修复；探针栈移植（jobs/internal/accountprobe+proberepo，全协议诊断探针/分级超时/limited 脱敏/六种完成证据）；retention/cleanup 仓储移植（cleanuprepo：data-retention 40+6 表/codex-context 分片/chat 分区+资产链/逻辑删除物理清理/api-key+account retry 扣减链，PG 统计扣减链显式报错登记）；acceptance 全套 66s 全绿（0 skip 除 PG 门控）。
- 2026-09-05 X02+X01+X03 终局提交（3f51156e0，Node 时代收场）: X02 Node 全量归档——backend/ 1700 文件（137,807 行/5.65MB）原样归档至 migration-backup/node/final-archive/，SHA256SUMS 1700 条全量校验通过，backend/ 从工作区删除（git 历史完整保留），node_modules/dist 构建产物清除，根 package.json 157 条死脚本/pnpm-workspace/发布回归脚本改读归档路径，dev.mjs go-only 化。X01 legacybridge 退役——internal/legacybridge 包删除，kernel RegisterFallback 扩展点移除（唯一调用方即 bridge），JUHE_AI_LEGACY_BRIDGE_TARGET 删除；所有前缀 Go 直答或 kernel 404/405 JSON 契约，无 502 代理路径。X03 部署 go-only 终态——start.sh/start.ps1 单 go 路径（fail-closed 校验保留），docker compose 两文件合一 go-only 拓扑、Node 镜像链删除，Jenkinsfile Node stage 移除（发布状态机 digest 链保持），validate-release-package 缺省 go，launcher 零第三方依赖。五项清障——authsys producer FIFO flake 根修（归档取证证实 Node 本无顺序契约）+测试隔离修复；ownermanifest KNOWN-BASELINE-FAIL 终解（91/91 type_line +36 漂移数据驱动批量修正，未删断言）；validate-macos-operations/dev-go-project-env 回归对齐归档路径；frontend 166 条死脚本入口清理+9 个回归脚本归档路径改造。终验：gateway/jobs/maintenance 三模块 build+test 零失败（含 acceptance）；12 项 node 回归测试全绿；frontend build 通过。
- 2026-09-05 X06 文档收场（本条目）: 看板波次表/工作包表全部 archived；终局报告落 docs/reports/Node到Go全量迁移终局报告-20260905.md；docs/architecture/架构总览.md 改写为 Go-native 现状；docs/functions/README.md 加实现归属总说明；docs/migration/README.md 状态改「已完成的终局迁移」；根 AGENTS.md/根 README.md 去 Node 后端表述；.git/hooks/pre-commit 迁移守卫按钩子注释既定收场动作删除（backend/ 已不存在，守卫成死代码）。docs/bug/问题-01NN-*.md 与 docs/plans/ 审计计划文档作为审计记录原样保留。
- 终局已知遗留项清单（详见终局报告 §6，均有代码/文档证据）: ① hybrid 混合智能路由的 AuxiliaryDispatcher 协作者组装根未装配（chain_compose.go 条件分支依赖三个协作者端口，组装根仅装配 Redis 模式的 SharedJSONCache/RuntimeStateStore）；② cleanuprepo 的 PG 统计扣减链显式报错登记（api-key/account retry 扣减链仅 SQLite 生效）；③ 限流沿用与 Node 相同的共享单实例 Redis Lua 多桶键空间等价语义（无集群化扩展）；④ chat 图片管线 WebP 编码为自写 VP8L 无损编码器（chain_chat_images.go），有损编码待决策；⑤ table-monitor 非业务数据 cleanup POST 未移植（W6 记录约定 Node-owned，Node 归档后该操作 404）；⑥ 平台侧待办：k8s-juhe overlay 仍声明 Node 容器需切 go-only 双容器拓扑、owner lock 无 Go 等价包装（启动脚本显式拒绝）、docker/compose.performance.yml 的 go-only 高性能变体未落地、主机侧 PM2/systemd 生态文件需改直拉两 Go 二进制（见 docs/migration/部署go-only双轨开关.md §6）；⑦ jobs 31 job 注册表中 11 go-partial 项按缺口清单显式登记（worker_partial_jobs.go：3 个 scheduled job 缺 Redis CircuitStore 等跨模块依赖未接线，其余为队列 IPC 按计划消灭/网关域单实现依赖），JUHE_AI_JOBS_WORKER_ENABLED 默认关；⑧ 问题-0172 所列 T2（OAuth 轮换/账户写路径 inval 未注入）等登记项按审计记录跟踪。
- 2026-09-05 T3 审计切片（pricing 计费引擎与查找闭环）: 审计发现 C03 交付失真——`internal/pricing` 无导出函数，`canonicalOpenAIModelAlias` 零调用、零测试；G20 的 chain_pricing.go CostEstimator 只是估算输入的窄实现。本切片对照归档 Node `model-pricing.service.ts`（findProviderModelPricing candidates 循环 + canonical alias 双回退、shutdown 过滤、目录排序）与 `provider-billing.{shared,policies,registry,service}.ts`（六 provider policy、tier 精确价 priority/flex/batch、cache split input/cache_write/cache_write_1h/cache_read、长上下文乘数、image/audio 行项目、costUsd override）补全：`lookup.go` 导出 List/FindProviderModelPricing(AsOf)，`billing.go` 导出 BuildCostBreakdown + EstimateProviderCostUsd/CacheWrite/CacheRead，供组合根装配 gatewayusage.PricingCatalog / jobs usagewriter.CatalogPricing / gatewayquota CostEstimator（三方 port 隔离，同构适配在组合根进行）。golden 测试 16 例从归档 Node 源逐字段推导（含 gpt-5.6→gpt-5.6-sol alias、日期后缀 candidates、shutdown 回退、tier_specific/mixed、长上下文含 gemini flex 保留 source、image/audio 合成行），-race 绿。Catalog display 渲染（presentation-only）与账户自定义目录合并（providers slice）仍登记顺延。
- 已知基线问题（已终解）：`maintenance/internal/ownermanifest/TestVerifyRepositoryBusinessOwnerManifest` 在基线 f9c1fbeac 即失败（`account_api_key_pool_probe_cursor type line 844 is stale`）。W1-W8 全量回归曾按 KNOWN-BASELINE-FAIL 记录、不计入迁移回归门；2026-09-05 终局提交（3f51156e0）按 91/91 type_line +36 漂移完成数据驱动批量修正（未删断言），该失败正式解除，三模块 build+test 零失败。
