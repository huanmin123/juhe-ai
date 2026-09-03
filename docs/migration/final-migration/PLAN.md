# Node 全量清零迁移执行看板（PLAN）

> 唯一进度事实源。每个 WP 状态：pending / in-progress / archived。每片收口（G5）由主 Agent 更新本表并在切片记录目录落证据。
> 权威迁移 worktree：`F:\sub2api-lite-migration`；分支：`node-to-go-final`（共享主目录固定 master 只读）。

## 基线记录（M0，2026-09-04）

- 本地 master：`f9c1fbeac`（== origin/master，无漂移）
- 迁移分支 HEAD：`node-to-go-final` @ `f9c1fbeac`
- 主目录 status：维护者有未提交改动（route-strategies speed-first/rehearsal 相关 + docs/plans 两文件），不属于本迁移，不触碰；本迁移计划文档已复制入 worktree。

## 波次进度

| 波 | 状态 | WP 明细 |
| --- | --- | --- |
| W1 | in-progress | K1 K2 K3 K4 K5 K6 K7 S-PG S-SQ + doc |
| W2 | in-progress | M01 ✓、M04 ✓（usage 待 J5）、M03 ✓、M05 ✓、**M06 ✓**（五模式 config 校验/绑定 reconcile/删除保护，-race 绿；速度优先运行态清理接 K5 总线）；下一片 M07 api-keys |
| W3 | pending | M08-M14 |
| W4 | pending | M15-M17 P01-P03 G01-G03 |
| W5 | pending | G04-G08 P04 P05 |
| W6 | pending | G09-G14 C03 |
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
- 已知基线问题：`maintenance/internal/ownermanifest/TestVerifyRepositoryBusinessOwnerManifest` 在基线 f9c1fbeac 即失败（`account_api_key_pool_probe_cursor type line 844 is stale`；主目录存在维护者未提交的 route-strategies 改动，Go 校验器与已提交 Node 状态不同步）。本迁移不掩盖：该断言涉及的 Node 文件将在 M08-M10 归档时随迁移消失，届时此失败自然解除；W1-W3 每次全量回归将此失败记为 KNOWN-BASELINE-FAIL，不计入迁移回归门。
