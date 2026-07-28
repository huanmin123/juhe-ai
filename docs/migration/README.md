# Go 渐进减法迁移目录

> 面向 AI、维护者和后续迁移执行者。
> 本目录集中维护 `juhe-ai` 从 Node.js + TypeScript 后端迁移到 Go 后端的长期规则、顺序、验收和部署调整。

## 1. 目录目标

- 把迁移目标、迁移顺序、删除规则、Go 技术基线和验证要求固定下来，避免后续只靠对话记忆推进。
- 支持“渐进式 + 减法迁移”：每迁移一个模块，就让该模块只有一个运行时 owner，并删除对应 Node 旧实现。
- 先迁移公开接口和后台管理接口，最后迁移真实中转协议网关。
- 把 Go 能天然简化的 Node 事件循环、阻塞规避、worker thread、SQLite 单写者治理和 IPC 复杂度提前列为删除对象。
- 明确 Go 不是无界并发：PostgreSQL 连接池、Redis 队列、上游账号并发、队列容量、请求体大小和 goroutine 生命周期仍必须有边界。

## 2. 多轮批量迁移执行法

迁移不要求第一轮一次达到最终完美状态，统一按多轮推进，主线优先扩大 Go 覆盖面：

1. 第一轮快速迁移：按模块批量完成 Node -> Go 的接口、路由、数据模型、后台任务和 owner 接线。每个小切片只做必要的编译、静态检查和最小回归；真实依赖验收、全面 Node 对照和细边界修复交给旁支 Agent，不阻塞主线迁移。
2. 第二轮从头复核：按模块重新对照最新 Node，集中处理遗漏接口、字段语义、错误边界、调用链和跨模块副作用；旁支 Agent 可以并行修复，主 Agent 负责合并和冲突裁决。
3. 第三轮统一验收：从头执行跨模块契约、真实 PostgreSQL / Redis / worker、前端真实 Go 后端 smoke、owner manifest、切流和回滚门禁，形成进入减法阶段的证据。
4. 减法阶段：只有用户明确通知开始后才删除 Node。每次删除一个模块前，必须核对 Go 路由、前端调用、数据读写、worker、定时任务、配置、部署入口、owner 和回滚点；删除后再做该模块快速回归与整体验收。

Agent 分工：主 Agent 负责迁移主线、接口整合、冲突、批次提交和推送；旁支 Agent 负责 Node 对照、测试、真实环境验证、问题修复和文档核对。旁支失败不得阻塞第一轮代码迁移，但必须登记到对应轮次清单，不能被描述为已验收。共享文件不并行写入，独立模块优先使用独立 worktree。

### 2.1 共享主目录与迁移 worktree 强制规则

Node -> Go 长期迁移与其他功能会同时修改仓库。为避免维护者在错误分支继续开发、迁移分支吞入无关功能或合回时产生不可审计的大型冲突，所有后续迁移必须遵守以下规则：

1. **共享主目录固定在 `master`。** 执行迁移的 Agent 不得在维护者正在使用的主目录执行 `git switch`、`git checkout`、`git rebase`、`git reset`、`git stash`、`git update-ref` 或临时 detached HEAD，也不得用底层 ref 操作绕过已检出的 `master`。主目录出现未提交改动时一律视为维护者所有，只允许只读检查，不得替维护者提交、暂存、还原或搬运。
2. **迁移分支只在独立 worktree 使用。** 当前权威迁移分支与实际 worktree 绝对路径记录在长期计划中；被审计为混入其他任务的旧分支只能作为抽取来源，不能继续集成或直接合回。Agent、测试脚本和子 Agent 必须显式指定权威迁移 worktree 作为工作目录，不能依赖终端上一次所在目录。新增并行实现应继续使用独立分支 / worktree，且不得并行编辑同一共享文件。不得对仓库中其他任务的 worktree 执行 `prune` / `remove`；确需清理本任务 worktree 时，必须先核对绝对路径、分支、状态和归属并取得明确授权。
3. **每批开始先记录三方基线。** 至少记录本地 `master`、`origin/master`、迁移分支 HEAD 和 merge-base；同时分别执行主目录与迁移 worktree 的 `git status --short --branch`。远端跟踪只允许先 `fetch`，不得在共享主目录自动 `pull`。若本地 `master` 与 `origin/master` 分叉，必须先审计双方提交；最终迁移分支必须同时包含待发布的本地 `master` 和最新 `origin/master`，不得擅自选择一侧或覆盖另一侧。本地 `master` 含维护者尚未提交的文件时，这些内容不会也不能被迁移分支读取或复制。
4. **主线同步统一使用 merge。** 在迁移 worktree 执行“`master` / `origin/master` -> 迁移分支”的普通 merge，不使用 rebase，不改写已有迁移提交，不 force-push。冲突只在迁移 worktree 解决，并逐文件判断应保留最新 Node 业务语义、Go 已迁移语义还是删除已撤销能力；不能用整侧覆盖解决业务冲突。无法安全裁决时只允许在迁移 worktree 执行 `git merge --abort`，随后重新验证两个 worktree 的分支与状态；禁止到共享主目录解冲突。
5. **持续跟踪 `master` 漂移。** 至少在每批任务分配前、代码完成后、长时间真实依赖测试后、中心集成前和推送前重新读取 `master`。若基线前进，先审计 `git log <旧基线>..<新基线>` 及受影响文件，再把新 `master` 合入迁移分支并重跑受影响测试。Node 新增业务语义必须同步到对应 Go owner；纯 Node runtime 规避机制不机械复制。
6. **提交前做范围审计。** 用 `git diff --name-status master...<迁移分支>`、逐提交 diff 和迁移计划清单证明变更属于迁移。账户页面、Node 功能、报告或其他并行工作即使测试通过，也不能因共享工作区自动提交而混入迁移批次；发现混入时必须先拆分或重建干净迁移提交。
7. **合回前执行双重稳定性检查。** 记录验证开始时的 `master` 为 M0；全部测试结束后再次读取为 M1。只有 M0=M1，或已把 M1 合入迁移分支并重跑受影响验证，才能准备合回。合回前还必须证明迁移 worktree 干净、迁移提交范围可审计、必需测试通过、文档与回滚证据齐全。合回 `master` 只允许 fast-forward；若 `git merge --ff-only <迁移分支>` 失败，说明主线再次漂移，必须返回迁移 worktree 合入最新主线并复验，禁止在共享主目录创建合并提交或处理冲突。
8. **维护者主目录不干净时禁止合回。** 不得通过 stash、临时提交、reset 或覆盖维护者文件来腾出 `master`。等待维护者收口后再合并，或使用经明确批准且不改变共享主目录分支的集成入口。合并后必须从 `master` 重新执行关键门禁；未经验证不得推送。
9. **回滚只使用可审计提交。** 禁止用 reset 或 force-push 回滚共享主线。代码回滚使用明确的 revert / 回滚提交；生产 owner 回滚还必须使用已验证的 owner manifest、drain 和启动顺序，不能把“Git 已回滚”当作运行时 owner 已恢复。
10. **每次同步都要留证。** 在 `PLAN-20260706T071505000Z` 或对应波次记录同步时间、旧 / 新 `master` SHA、迁移分支 SHA、冲突与裁决、执行测试、未完成门禁和回滚点。没有这些证据，只能描述为代码在迁移分支存在，不能描述为已合入、已接管或已完成迁移。
11. **共享主目录发生意外分支或历史变更时立即停写审计。** 先只读记录 `status`、`HEAD`、本地 / 远端 `master`、reflog 和所有 worktree，再确认维护者文件归属；不得用 reset、stash、checkout 或删除文件“恢复现场”。如果污染提交已经进入本地 `master`，仍以该 `master` 为待同步事实合入 clean 迁移分支，同时用 merge-base、逐提交和三点 diff 证明迁移提交边界；未推送的污染历史不等于可以忽略，也不授权迁移任务改写共享历史。
12. **迁移测试不得借 Node 初始化掩盖 Go schema 缺口。** `juhe-ai-maintenance schema-up` 必须能在隔离 fresh PostgreSQL 上独立完成当前 Goose catalog；遇到仍由 Node 拥有、尚未进入 Go catalog 的表时，只允许让加法 migration 在对象不存在时显式 no-op，或把完整对象迁入 Goose 并通过 owner 评审。禁止在 Go 验收脚本中先调用 Node schema 初始化、伪造 Goose ledger 或复用已有业务库来制造通过结果。

推荐顺序固定为：只读检查共享 `master` -> 在独立 worktree 审计范围 -> 将最新 `master` merge 到迁移分支 -> 实现与验证 -> 再次检查 `master` 漂移 -> 必要时再次 merge 与复验 -> 在共享主目录干净且基线稳定时合回 `master` -> 从 `master` 复验并记录证据。任何一步发现异常都回到只读基线审计，不凭记忆继续执行。

历史计划中记录的 rebase、共享主目录切分支或临时集成方式只作为当时事实保留，不构成现行操作依据；本节规则自 2026-07-28 起优先适用于后续迁移批次。

## 3. 首次阅读顺序

1. [迁移规划总览](迁移规划总览.md)：迁移原则、阶段、减法规则和整体边界。
2. [Go 后端架构基线](Go后端架构基线.md)：目标目录、进程模型、并发模型和线程安全规则。
3. [Go 技术选型与依赖基线](Go技术选型与依赖基线.md)：Go 框架、日志、配置、DB、SQL、job、测试、观测和安全扫描的默认依赖。
4. [存储目标与 SQLite 移除](存储目标与SQLite移除.md)：PostgreSQL + Redis 单模式目标、SQLite 删除范围和数据边界。
5. [Go 迁移指标与观测规划](Go迁移指标与观测规划.md)：系统指标从 Node 事件循环口径切换到 Go runtime、PG/Redis/Asynq 和网关观测口径的规划。
6. [Go 系统指标字段迁移清单](Go系统指标字段迁移清单.md)：W6 / W7 / W8 执行系统监控迁移时逐项删除 Node 字段、替换 Go 字段和验证前端契约的清单。
7. [模块迁移顺序与减法清单](模块迁移顺序与减法清单.md)：模块迁移波次、删除条件和验收门禁。
8. [W1b 外部维护公开接口迁移记录](W1b-外部维护公开接口迁移记录.md)：`/__aipublic__` 外部维护接口的当前契约、Node 对照命令、Go 目标边界和删除门禁。
9. [W2 管理端只读辅助接口迁移记录](W2-管理端只读辅助接口迁移记录.md)：后台 options / catalog 接口和账号标签切片的当前契约、已迁移路径、系统账户轻量下拉、authorization grantee accounts / grantee teams / grantee groups、分组授权组只读 union、账户授权账户只读 union、账号标签 owner-only 只读 / 未绑定删除 / 独立 PATCH opt-in、主账户标签写路径和 operation log 缺口、接管门禁。
10. [W3 登录与系统账户迁移记录](W3-登录与系统账户迁移记录.md)：登录、当前用户、会话、登出、改密、验证码和系统账户写接口的分块迁移记录；当前覆盖 `GET /auth/captcha` 验证码发放 / 校验基础、`POST /auth/login` 登录 / session 创建小切片、`GET /auth/me` 读切片、`PATCH /auth/me` 当前用户资料更新切片、`POST /auth/change-password` 当前用户改密切片、`POST /auth/logout` 当前令牌退出切片、`POST /system-accounts` 创建切片，以及 `PATCH /system-accounts/{id}` 完整 mixed partial update；登录会话列表 / 按 ID 撤销已撤销，不属于当前迁移范围。全部仍为 Go opt-in 灰度路径，不代表 W3、Node `/auth` 或 Node `/system-accounts` 已接管。
11. [W4 团队与统一授权迁移记录](W4-团队与统一授权迁移记录.md)：系统团队、成员、授权 grant、授权来源展开和最终用户授权的分块迁移记录；当前覆盖团队、授权 CRUD / 归还 / 回收、授权详情用量，以及 admin/self 授权用量 rows-only details + 独立 `team-summary` / `user-summary` 的 Go opt-in 灰度能力，并包含授权来源 / grant / 额度窗口 / 统计脏标记 / usage window PostgreSQL schema 基线和授权缓存失效；不代表 W4、Node `/system-teams` 或 Node `/authorizations` 已接管。
12. [W5 管理端全局品牌设置读取记录](W5-管理端全局品牌设置读取记录.md)：`GET/PATCH /__aisys__/api/settings/global` 的 Go opt-in 契约、`publicsettings` / store 复用、管理员权限、读写 session、两层限流、精确品牌 DTO、验证记录和删除门禁。
13. [W5 管理端系统运行设置迁移记录](W5-管理端系统运行设置迁移记录.md)：已进入 Go opt-in 的 `GET/PATCH /__aisys__/api/settings`，固定 53 key，GPT Priority / Flex 使用模型目录精确档位价格且不提供通用倍率，并覆盖 `256 KiB` / `413`、parser 与鉴权 / 限流顺序、PostgreSQL 有界事务、`000024` 初始设置 seed、`000043` 删除历史倍率设置、双缓存失效、操作日志和删除门禁；真实依赖因 Docker 不可用输出 `SKIP` 时不计通过。
14. [W5 管理端分组创建迁移记录](W5-管理端分组创建迁移记录.md)：已进入 Go opt-in 的 `POST /groups` 与 `POST /my-groups` 创建契约、作用域、完整高并发策略、唯一约束、写后副作用、验证记录和删除门禁。
15. [W5 管理端分组列表迁移记录](W5-管理端分组列表迁移记录.md)：`GET /groups` 与 `GET /my-groups` 的权限、分页、progressive DTO、预聚合读取、共存期 Node 单 writer 和最终 Go stats worker 门禁。
16. [W5 管理端分组详情迁移记录](W5-管理端分组详情迁移记录.md)：`GET /groups/{id}` 与 `GET /my-groups/{id}` 的 owner / authorized 详情 DTO、实时账户并发、授权来源、权限和真实依赖门禁。
17. [W5 管理端分组更新迁移记录](W5-管理端分组更新迁移记录.md)：`PATCH /groups/{id}` 与 `PATCH /my-groups/{id}` 的 owner / authorized 字段边界、事务保护、路由绑定保护、缓存与运行态失效、操作日志和真实依赖门禁。
18. [W5 管理端分组删除迁移记录](W5-管理端分组删除迁移记录.md)：`DELETE /groups/{id}` 与 `DELETE /my-groups/{id}` 的 owner-only 权限、默认分组和路由策略保护、硬删除级联、统计脏标记、缓存与运行态失效、操作日志和真实依赖门禁。
19. [W5 管理端策略路由列表与详情迁移记录](W5-管理端策略路由列表与详情迁移记录.md)：管理 / 个人四条 GET 的 admin global / owner narrowing、self 强制本人、渐进分页、大小写敏感名称前缀、轻量列表、完整详情和真实依赖门禁；Go opt-in 已实现，真实 PostgreSQL smoke 因 Docker 不可用待复跑，不代表生产接管。
20. [W5 管理端策略路由创建迁移记录](W5-管理端策略路由创建迁移记录.md)：管理 / 个人两条 POST 的 strict JSON、五模式、授权分组事务锁、运行态失效、操作日志、前端 request-capture 和真实依赖门禁。
21. [W5 管理端策略路由更新迁移记录](W5-管理端策略路由更新迁移记录.md)：管理 / 个人两条 PATCH 的 strict partial JSON、事务锁定、绑定整体替换、错误优先级、运行态失效、操作日志、前端 request-capture 和真实依赖门禁。
22. [W5 管理端策略路由删除迁移记录](W5-管理端策略路由删除迁移记录.md)：管理 / 个人两条 DELETE 的 admin global / owner narrowing、self actor、默认与 API Key 引用保护、事务锁读、204 空响应、运行态失效、操作日志、前端 request-capture 和真实依赖门禁。
23. [W5 管理端 API Key 密钥生命周期迁移记录](W5-管理端APIKey密钥生命周期迁移记录.md)：管理端 / 个人端 API Key 创建、完整密钥查看与刷新、加密存储、权限、缓存失效、操作日志和真实依赖门禁。
24. [W5 管理端 API Key 删除迁移记录](W5-管理端APIKey删除迁移记录.md)：`DELETE /api-keys/{id}` 与 `DELETE /my-api-keys/{id}` 的作用域、204 空响应、原子硬删除、cleanup target、提交后失效、操作日志、残余安全风险和真实依赖门禁。
25. [W6 记录与统计读接口迁移记录](W6-记录与统计读接口迁移记录.md)：记录、日志和统计只读接口迁移记录；当前覆盖管理侧 / 个人侧 `usage-window`、账户用量 list/summary/trend、AI 性能 base/series/accounts、usage overview combined 与五段 progressive、系统指标 trend、使用记录列表、运行日志列表 / 详情 / facets / runtime、公开接口日志列表 / 详情和审计日志轻量列表 Go opt-in；`000088` / `000089` 补齐本轮统计 reader 与 Node writer 共存所需 fresh Goose catalog，但 Node stats worker 仍是唯一 writer。
26. [W6 System API 限流对齐记录](W6-System-API限流对齐记录.md)：system API IP read / write、已认证用户 read / write、client IP allowlist bypass、缓存失效、验证和剩余 Node 差异。
27. [W6 管理端客户端 IP 统计与策略迁移记录](W6-管理端客户端IP策略迁移记录.md)：`GET /ip-stats` 只读列表与 `allowlist`、`unallowlist`、`blacklist`、`unblock` 四条管理写接口的 Go opt-in 契约、Node writer 边界、预聚合读取、查询计划、前端证据和删除门禁。
28. [W6 管理端表监控只读 Schema 共存记录](W6-管理端表监控只读Schema共存记录.md)：表监控三条 GET 的 Go reader、Node 单 writer、schema capability gate、已发布 `000073` 后的连续版本规则和删除门禁。
29. [W7 模型检测写入与任务契约迁移记录](W7-模型检测写入与任务契约迁移记录.md)：模型检测 durable job payload、幂等写阶段、终态 CAS、停止 / SSE 语义和 Node 专用复杂度删除边界。
30. [测试与验收策略](测试与验收策略.md)：契约测试、回归矩阵、性能验证和网关专项验收。
31. [W7 公开接口日志写入与保留契约](W7-公开接口日志写入与保留契约.md)：冻结 Node 单 writer、队列容量、payload 捕获、保留清理和 Go reader 反向约束，供后续 Go-native writer / retention 接管使用；当前不改变生产 owner。
32. [W7 使用记录写入队列 Node 契约基线](W7-使用记录写入队列Node契约基线.md)：冻结 Node 使用记录 writer / queue 的 owner、可靠性边界、已确认丢失缺陷和 Go 原生接管门禁。
33. [W7 账户健康探针状态机契约](W7-账户健康探针状态机契约.md)：自动探针归因、周期健康 / 冷却复测边界、master 权威五元 fence、授权 quota、payload v3、schema 91 generation/index、neutral defer、原生十模式 API Key/OAuth Probe、发送前撤销栅栏与 PostgreSQL outcomes；真实依赖、job owner handoff 和生产切换仍未完成。
34. [开发构建部署调整](开发构建部署调整.md)：本地开发、构建、发布包、Docker 和常驻运行的迁移安排。
35. [W10 网关上游请求与流式中转核心迁移记录](W10-网关上游请求与流式中转核心迁移记录.md)：Go-native 上游请求构造、HTTP transport dispatch seam、凭据与 header 隔离、有界 body、流式背压、超时、取消、终态和 usage/audit handoff；当前不接生产 listener、upstream policy 或 owner 切换。
36. [迁移文档示例](迁移文档示例.md)：后续单模块迁移记录的写法示例。
37. [精确路由 Owner 清单设计](精确路由Owner清单设计.md)：四大域默认 owner、method + path template 精确 allowlist、回滚 manifest 和未来代理接入门禁。
38. [Goose 与 Node 初始化边界复审记录](Goose与Node初始化边界复审记录.md)：schema 92 的 Go-only Goose 执行命令、Node 补充 DDL 仍保留的原因、未追踪 schema 拒绝门禁，以及 fresh schema 77/80 号 migration 的失败反证与修复证据。

## 4. 目录职责

| 文档 | 职责 |
| --- | --- |
| `README.md` | 迁移目录入口、阅读顺序和维护规则 |
| `迁移规划总览.md` | 长期迁移策略、阶段、边界和不做事项 |
| `Go后端架构基线.md` | Go 目标架构、依赖选择、并发、线程安全和内存治理 |
| `Go技术选型与依赖基线.md` | Go 框架、日志、配置、DB、SQL、job、测试、观测和安全扫描的默认依赖与禁用依赖 |
| `存储目标与SQLite移除.md` | PostgreSQL + Redis 单模式、SQLite 删除清单、离线数据处理和验证要求 |
| `Go迁移指标与观测规划.md` | Go 目标系统指标、Prometheus、pprof、PG/Redis/Asynq、worker 和网关观测口径 |
| `Go系统指标字段迁移清单.md` | Node 系统指标字段删除、Go 字段替换、前端页面迁移和 W6 / W7 / W8 验收清单 |
| `模块迁移顺序与减法清单.md` | 模块优先级、迁移状态、Node 删除证据和测试门禁 |
| `Goose与Node初始化边界复审记录.md` | Goose schema-up 的单一账本边界、fresh / upgrade 数据库规则、Node DDL 缺口和 seed boolean 修复证据 |
| `W1b-外部维护公开接口迁移记录.md` | `/__aipublic__` 外部维护接口契约、Go 迁移范围、Node 对照命令和删除门禁 |
| `W2-管理端只读辅助接口迁移记录.md` | 管理端只读辅助接口与账号标签只读 / 未绑定删除 / 独立 PATCH 契约、Go 当前实现范围、权限边界、系统账户轻量下拉、authorization grantee accounts / grantee teams / grantee groups、分组授权组只读 union、账户授权账户只读 union、主账户标签写路径 / 完整 summary / operation log 缺口和删除门禁 |
| `W3-登录与系统账户迁移记录.md` | 登录、当前用户、会话、登出、改密、验证码和系统账户写接口迁移记录；当前固定 `GET /auth/captcha` 验证码发放 / 校验基础、`POST /auth/login` 登录 / session 创建小切片、`GET /auth/me` 只读切片、`PATCH /auth/me` 当前用户资料更新切片、`POST /auth/change-password` 当前用户改密切片、`POST /auth/logout` 当前令牌退出切片、`POST /system-accounts` 创建切片、`PATCH /system-accounts/{id}` 完整 mixed partial update 和后续拆分门禁；登录会话列表 / 按 ID 撤销已撤销，不得作为恢复项 |
| `W4-团队与统一授权迁移记录.md` | 团队与统一授权迁移记录；当前固定团队与授权 CRUD / 归还 / 回收、授权 `:id/usage` 明细、admin/self `team-details` / `user-details` rows-only 分页及独立 `team-summary` / `user-summary` 汇总契约、授权来源 / grant / 额度窗口 / 统计脏标记 / usage window PostgreSQL schema、usage window / 到期扫描 / quota snapshot worker Go opt-in；真实依赖、生产 worker、浏览器真实后端、owner 切流和 Node 删除门禁仍保留 |
| `W5-管理端全局品牌设置读取记录.md` | W5 `GET/PATCH /settings/global` 管理端品牌设置读写切片；固定管理 API 默认关闭、管理员权限、读写 session、两层 read/write 限流、精确品牌 DTO、PostgreSQL 事务、`settings:global` shared cache version 和 `settings.update_global` operation log；与系统运行设置纵切面分开登记，明确排除生产切流和 Node 删除 |
| `W5-管理端系统运行设置迁移记录.md` | W5 `GET/PATCH /settings` 已进入 Go opt-in 的系统运行设置纵切面；固定 53 key，GPT Priority / Flex 只使用模型目录精确档位价格，并覆盖 `256 KiB` / `413`、IP limiter 后且 auth / touch / user limiter 前的 PATCH parser、GET read auth 不 touch、PATCH touch、PostgreSQL 固定有界读取 / `FOR UPDATE` / 稳定 key / 完整 snapshot、migration `000024` 的初始设置与统计时区 seed、migration `000043` 删除历史倍率设置、PostgreSQL 在线禁改时区、`settings:system` / `settings_updated` 双失效和 `settings.update` operation log；真实依赖 smoke 因本机无 Docker 输出 `SKIP` 时不计通过，不代表生产接管 |
| `W5-管理端分组创建迁移记录.md` | W5 `POST /groups` 与 `POST /my-groups` 已进入 Go opt-in；固定 admin / self owner 作用域、strict body、`256 KiB`、个人 / 高并发默认值、完整 16 字段策略 JSON、数据库唯一约束、`201` 基础摘要、gateway runtime 失效与 `groups.create` operation log best-effort；integration 代码已补，真实依赖仍待健康 Docker 环境复跑；明确排除列表、详情、更新、删除和生产接管 |
| `W5-管理端分组列表迁移记录.md` | W5 `GET /groups` 与 `GET /my-groups` 设计；固定 admin / self 作用域、1000 行 progressive pagination、owner / authorized union、稳定排序、轻量 DTO、预聚合 stats / usage 批量读取、共存期 Node 单 writer 和最终 Go stats worker 删除门禁 |
| `W5-管理端分组详情迁移记录.md` | W5 `GET /groups/{id}` 与 `GET /my-groups/{id}` 已进入 Go opt-in；固定 admin / self 作用域、owner / authorized 可见性、owner `accountIds` 与 Redis v2 实时并发、authorized 账户 ID 隐藏与预聚合统计、完整授权来源、两层 read limiter、真实 PostgreSQL / Redis smoke 和删除门禁 |
| `W5-管理端分组更新迁移记录.md` | W5 `PATCH /groups/{id}` 与 `PATCH /my-groups/{id}` 已进入 Go opt-in；固定 owner / authorized 字段边界、strict partial JSON、PostgreSQL 事务与路由绑定保护、授权本地设置、完整详情回读、shared cache / runtime 失效、`groups.update` operation log、真实 PostgreSQL / Redis smoke 和删除门禁 |
| `W5-管理端分组删除迁移记录.md` | W5 `DELETE /groups/{id}` 与 `DELETE /my-groups/{id}` 已进入 Go opt-in；固定 owner-only、authorized 404、默认分组和全作用域路由策略保护、硬删除级联、事务内统计脏标记、双 shared cache / runtime 失效、`groups.delete` operation log、真实 PostgreSQL / Redis smoke 和删除门禁 |
| `W5-管理端策略路由列表与详情迁移记录.md` | W5 策略路由管理 / 个人四条 GET 迁移记录；固定 admin global / owner narrowing、self 强制本人、`pageSize=50` 默认与 `1..200`、无最大页、大小写敏感名称字面前缀、非法 mode/status 忽略、`pageSize+1` progressive total、列表最多 3 条绑定预览与计数、详情完整 bindings/config，以及真实 PostgreSQL / HTTP / 前端 / 切流 / 回滚 / Node 删除门禁；Go opt-in 已实现，真实 PostgreSQL smoke 待健康 Docker 环境复跑 |
| `W5-管理端策略路由创建迁移记录.md` | W5 策略路由管理 / 个人两条 POST 迁移记录；固定 strict JSON / presence、五种模式、停用目标账户、授权分组事务锁、重复名称、提交后运行态失效、operation log、前端 request-capture 和真实 PostgreSQL / 前端 / 切流 / Node 删除门禁 |
| `W5-管理端策略路由更新迁移记录.md` | W5 策略路由管理 / 个人两条 PATCH 迁移记录；固定 strict partial JSON、admin global / owner narrowing、self actor scope、事务内锁定快照、绑定整体替换、完整独立配置校验、错误优先级、`route_strategy_updated` 运行态失效、operation log、前端 request-capture 和真实 PostgreSQL / Redis 互操作 / 前端 / 切流 / Node 删除门禁 |
| `W5-管理端策略路由删除迁移记录.md` | W5 策略路由管理 / 个人两条 DELETE 迁移记录；固定 admin global / owner narrowing、self actor scope、无 body parser / mutation guard、默认和 API Key 引用 400 保护、事务 `FOR UPDATE`、204 空 body、`route_strategy_deleted` 失效、marker-only operation log、前端 request-capture，以及真实 PostgreSQL / Redis 互操作 / 前端 / 切流 / Node 删除门禁 |
| `W5-管理端APIKey密钥生命周期迁移记录.md` | W5 `POST /api-keys` / `POST /my-api-keys`、`GET .../secret` 与 `POST .../refresh-key` 管理 / 个人双路由已进入 Go opt-in；固定 owner scope、strict create body、AES-GCM 密文、一次性明文返回、create runtime/quota 与 refresh validation/runtime/quota 失效、session touch、operation log marker 和真实 PostgreSQL / Redis / Asynq 门禁；删除由独立迁移记录维护，不代表 API Key 生产接管 |
| `W5-管理端APIKey删除迁移记录.md` | W5 `DELETE /api-keys/{id}` 与 `DELETE /my-api-keys/{id}` 已进入 Go opt-in；固定 admin global / owner narrowing、self actor scope、写鉴权与限流、204 空 body / no-store、默认 Key 保护、PostgreSQL 原子硬删除与 cleanup-target upsert、validation 必需失效、`api_keys.delete` 操作日志、残余失效重试风险和真实依赖门禁 |
| `W6-记录与统计读接口迁移记录.md` | W6 记录、日志和统计只读接口迁移记录；固定管理侧 / 个人侧 `usage-window`、账户用量 list/summary/trend、AI 性能 base/series/accounts、usage overview 五段渐进读取的窄契约、31 天窗口、预聚合读取、schema catalog / writer owner 分离和删除门禁 |
| `W6-System-API限流对齐记录.md` | system API 两层 read / write 限流记录；固定六项设置默认值、鉴权前 IP 层、鉴权后已注册业务路由用户层、Redis / 内存实现、client IP allowlist 两层 bypass、30 秒缓存 / shared version 失效、429 语义，以及已认证未知路径 / 错误 method 尚未对齐的删除门禁 |
| `W6-管理端客户端IP策略迁移记录.md` | W6 `GET /ip-stats` 与四条 `POST /ip-stats/{ipHash}/{action}` Go opt-in 记录；列表固定只读 Node 预聚合结果、query/date/status/sort/progressive pagination、默认静态请求数排序和 Node writer / detail 边界，写接口固定 strict JSON、事务、shared cache version、operation log、前端证据和真实依赖门禁 |
| `W7-账户健康探针状态机契约.md` | W7 自动探针归因、周期健康检查与冷却复测边界、五元陈旧任务 fence、授权 quota、payload v3、schema 91 generation/index、neutral defer、`cooldown_retest` 统计排除和 Go 原生 worker 接线门禁；不代表 Probe / Outcomes 或生产 owner 已接管 |
| `W6-管理端表监控只读Schema共存记录.md` | W6 表监控三条 GET 的 PostgreSQL 只读迁移；固定 Node 单 writer、schema capability gate、缺表不伪造空数据、并行 migration 版本协调和删除门禁 |
| `W7-模型检测写入与任务契约迁移记录.md` | W7 模型检测 writer/job 契约、Go 主动修复、后续 schema/worker/executor/HTTP 顺序和 Node 删除门禁 |
| `测试与验收策略.md` | 单模块、系统、网关、性能、安全和发布验收 |
| `W7-公开接口日志写入与保留契约.md` | Node 公开接口日志单 writer、队列容量、payload 捕获、保留清理、Go reader 反向约束和 Go-native 接管顺序；当前不改生产 owner |
| `开发构建部署调整.md` | 开发环境、命令、包结构、部署脚本和平台差异 |
| `迁移文档示例.md` | 后续新增单模块迁移记录时的参考格式 |
| `精确路由Owner清单设计.md` | 路由级 owner 声明、严格匹配、回滚清单和生产 dispatch 接入门禁 |

## 5. 维护规则

- 任何 Go 迁移任务开始前，先确认本目录和 `../plans/计划-20260706T071505000Z-Node转Go渐进减法迁移.md`。
- 每迁移一个模块，必须更新 [模块迁移顺序与减法清单](模块迁移顺序与减法清单.md) 的状态、Node 删除证据和测试结果。
- 影响 Go 目录结构、并发模型、进程模型或存储 owner 时，更新 [Go 后端架构基线](Go后端架构基线.md)。
- 影响 Go 框架、日志、配置、SQL、job、测试、观测、安全扫描或外部依赖时，先更新 [Go 技术选型与依赖基线](Go技术选型与依赖基线.md)，再改代码。
- 影响系统指标、Prometheus、pprof、worker lag、队列状态、PG/Redis/Asynq 观测或前端系统监控契约时，先更新 [Go 迁移指标与观测规划](Go迁移指标与观测规划.md) 和 [Go 系统指标字段迁移清单](Go系统指标字段迁移清单.md)，不能把 Node `eventLoopLagMs` / `db-service` 字段模拟成 Go 指标。
- 影响 PostgreSQL、Redis、SQLite 移除、数据导入导出或存储部署时，更新 [存储目标与 SQLite 移除](存储目标与SQLite移除.md)。
- 影响本地启动、安装、构建、发布包、Docker、服务化或回滚方式时，更新 [开发构建部署调整](开发构建部署调整.md)，并同步 `../develop/` 或 `../deploy/` 对应当前手册。
- 影响公开 API、管理 API、权限、安全、统计、存储或网关语义时，同步更新 `../functions/` 下对应功能文档。
- 影响当前真实架构事实时，同步更新 `../architecture/架构总览.md` 和 `../architecture/backend/README.md`。

## 6. 边界说明

- 迁移期间，前端仍按 Vue 3 + TypeScript + Ant Design Vue 维护。
- Go 迁移优先覆盖后端运行时；前端 API 调用契约要通过测试证明未缺失，但不因迁移重做前端信息架构。
- “不向下兼容”指不为旧 Node 内部结构、旧 schema、旧 repository 或旧 IPC 保留运行时兼容分支；对当前产品公开契约和用户可见行为，迁移必须做到等价或在文档中明确记录新契约。
- 迁移目标不再保留 SQLite standalone / PostgreSQL performance 两套模式。Go 后端目标只有 PostgreSQL + Redis；SQLite 只作为当前 Node 旧实现和离线导出来源存在。
- 迁移中允许测试环境短期存在 Node 与 Go 两个服务按路径分流，但同一接口、同一模块或同一后台任务不能长期双 owner。
