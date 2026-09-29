# Bug 记录目录

- [BUG-0231](问题-0231-生图资产用量自增upsert在PG下42702.md)：生图资产用量自增 upsert 的 ON CONFLICT DO UPDATE 右值裸列自引用（asset_bytes = asset_bytes + ?）在 PG 下 42702 ambiguous——生产（PG）生图资产提交 100% 失败（子调用上游 200 后事务回滚、chat-assets 残留空分片目录、轮次 image_generation_failed、主模型重试耗尽降级），SQLite 隔离实例容忍裸列故测不出（grok-imagine 生图路径 2026-09-29 首次真实触达，此前生产生图落库从未成功）；已修复（右值表名限定 <table>.asset_bytes，与 quota_hourly_dirty 限定范本同款；生产 psql 直跑限定写法实证通过 + 双方言语句形状测试固化，chatassets 包首个测试；全库排查其余 ON CONFLICT 写入点均 excluded./限定无同类）；已发布（2026-09-29 14:2x）；关联上游边界：/v1/images/edits 只回 url 且 imgen.x.ai 国内不可直连，生产编辑图需生图账户绑出海代理；生图账户探活须 images_json 模式（误配 chat_json 403 打死）。
- [BUG-0231](问题-0231-glm目录快照缺function_calling声明.md)：glm 目录快照全部 16 个模型行（glm-4.5~glm-5.3 系）漏写 SupportedToolsByProtocol——glm 主模型会话 function_calling 判定恒 false，web_search/generate_image 内部工具零注入（连带 toolCapabilities 面板与 binding_required 引导全灭），问实时信息纯文本道歉；生产实证 chat_conv_f548583419ab5df348829d0505e0d0cf（2026-09-29 13:44 glm-5.3-flash 问上海天气，reasoning 自述无任何工具）；已修复（16 行补 chat_completions=[function_calling] 声明，toolsByProtocol 现有口径，deepseek 同款；golden test 补 glm 断言），待发布（静态快照读取链派生，发布即生效无需刷库）；契约依据 §6.4，BUG-0229 同族。**盘点扩展（用户指令全供应商核查）**：deepseek/anthropic/gemini/xai 无缺口（image/embedding 模式不声明属口径），openai 家另发现 8 个旧 chat 模型（gpt-4 系 5 个 + gpt-3.5-turbo 系 3 个）同缺 function_calling——同批补齐（按各自协议列声明，responses 下亦仅 function_calling）；golden test 升级为「全部供应商 chat 模式行必须声明 function_calling」防回归口径。**全列空值盘点扩展（同日）**：反射审计六家快照全部核心列——补全三项不合理空（anthropic 构造器缺 Mode 致生产目录 46 行 mode 列全空；openai 46 行缺 ReleaseDate 按 dated 名字/同系列 dated 行/公开 GA 三级佐证补全，无佐证的 gpt-image-1.5 等 6 行不编造；gpt-4o-2024-05-13 缺缓存价按官方 cached=input×50% 补快照日价），其余空值（MaxTokens 全家不维护、旧模型无缓存、代际性无 effort 档、image/embedding 不声明工具等）逐类判定合理不补；新增 catalog_completeness_test 三门禁（chat 行工具声明/Mode 非空/dated 名字行必带同值日期）固化口径。
- [BUG-0230](问题-0230-主模型生图工具乱传model致派发拒绝.md)：generate_image 工具 schema 的 model 枚举为全集三模型且执行层原样透传主模型选择，会话生图绑定账户支持集为子集时派发被模型门拒绝（503 model_unsupported、attemptCount=0），主模型降级画 SVG 或当轮失败；生产实证（2026-09-29 chat_conv_0a1be842：主模型两次传 gpt-image-2 被拒，传 grok-imagine-image 时审计 200 全通——含 imageBinding 固定派发收敛与 images_json endpoint mode 前置修复均验证正常）；已修复（执行层收敛：constrainChatImageModel 纯函数 + ConstrainImageModel 端口，越界选择回退会话默认生图模型、默认亦不可路由取候选首项，收敛结果贯通子调用/落库/工具结果；契约 §6.2 同步 + 六分支贯通测试，随 2026-09-29 13:5x 发布）；关联事实：api.shenwenai.com 主模型 gpt-5.6-terra 流稳定性一般（30s 无数据看门狗中断，属上游质量）。
- [BUG-0229](问题-0229-custom目录行覆盖内置行后能力空缺.md)：personal/global 自定义模型行在目录合并（scope 优先级整行替换）覆盖内置行后 `supportedTools`/`inputModalities`/`outputModalities` 恒空（custom 表无该三列且两面不兜底；Node `toCustomCatalogItem` 硬编码空数组 + 整行替换，属 Node 既有缺陷延续、非 Go 迁移回归）——web_search/generate_image/诊断工具永不注入、带图输入服务端 400、会话工具面板恒不可用、上下文压缩协议偏好退化；生产实证 `chat_conv_ed0f35e0a639d9eb60e88f1d17a1bd5b`（2026-09-28 17:10 问"北京今天天气"无联网搜索，走 chat_completions 无工具；上游 api.shenwenai.com 已实测支持 responses+web_search 并真实执行 web_search_call 返回实时天气）；已修复（契约裁决口径 b：仅 custom 行与内置目录同 (provider,model) 合并键时三个空能力键继承内置行（含静态兜底）值，仅填空不覆盖；全新自定义模型不继承、禁 pricing 别名/前缀回填；chat 面 `chain_catalog.go` 与管理面 `providers.Store` 经共享 `InheritBuiltinCatalogCapabilities` 同源解析；契约落 `docs/functions/AI问答设计.md` 8.6 新增小节）；已发布（2026-09-28 19:16 gateway+jobs 全量；生产 Redis DB0 空、共享目录缓存键不存在，gateway 重启即带继承能力；隔离+真实上游端到端实证 web_search 真实执行返回当日天气并引用 baidu.weather.com.cn，生产三项人工验证待该场景用户下次对话顺带确认）；核心改动曾以 BUG-0226 编号随 `71bc4a5a6` 提交，编号后被 0226 占用顺延。
- [BUG-0228](问题-0228-F3F4租约启动fail-fast发布窗口重启循环.md)：发布 recreate 窗口旧 gateway 被 SIGKILL 截断 defer 租约释放，DB 单行租约（TTL 默认 30s）存活期内新容器 acquire `ok=false` 走启动 fail-fast，2026-09-28 16:00 发布连续 8 次失败（16:00:25-16:00:37，TTL 过期 16:00:44 自愈；runbook 原记"2 次 Restarting"失真已勘误）；等待机制 `JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT`（F3/F4 共用，main.go 既有、超时仍 fail-fast）此前未配置；已修复（部署配置：compose gateway 显式配 `45s` + healthcheck `start_period` 30s→75s——等待发生在 health server 监听之前；jobs 无启动 fail-fast 租约不配；README/deploy 契约同步、runbook 登记）；已发布（2026-09-28 19:16，compose.yml 已上传生效并 config 校验、旧配置备份；生产实证：启动期等待前任 F3 租约 25s 后一次接管成功、零重启循环、24s 内 healthy）。
- [BUG-0227](问题-0227-usagespooldrain静默失败面与属主队头卡死.md)：16:00 发布（BUG-0225 root→app(UID1000) 过渡）旧 root 容器临终写入 2 个 `root:root 0600` spool 文件居文件名序队头，jobs drain `ReadFile` EACCES 命中「非 ErrNotExist 即终止本轮」分支，叠加 Run 循环吞错零日志——永久 head-of-line 停摆：gateway 流量 `usage_records` 断供 95 分钟、spool 积压 448 文件（含 AI 对话计费），水位函数队头不可读保留旧水位同步卡死统计游标安全门；已修复（读错误记 ERROR+同文件同错误 60s 节流后跳过继续本轮（文件保留待属主修复后自然消费，区别于 .corrupt 隔离）、Run 失败 Warn 留痕、水位跳过不可读候选前进；100ms 节拍/500 批/幂等/损坏隔离语义不变 + 3 个回归测试），生产已止血（chown 1000:1000 后 20 秒内 448 条全部消费入库、`usage_record_spool_drain_summary windowFiles=448` 实证），已随 `71bc4a5a6` 提交并随 2026-09-28 19:16 发布（发布后 drain 实时消费实证 windowFiles=2、零积压）；部署侧配套（chown 时序移至旧容器停止后/发布后复查 spool 属主）入 docker README 与 runbook。
- [BUG-0226](问题-0226-系统指标页jobs健康段不可达.md)：系统指标页 jobs 健康段恒"不可达"——gateway 侧 `JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS` 从未配置（daa90d10d 上线漏部署配置）叠加 jobs 健康监听校验强制 loopback（容器形态 `0.0.0.0` 启动即拒）双重装配缺口；已修复（校验放宽为允许显式 IP/全接口、默认仍 127.0.0.1 + compose 双容器不同值配置），已发布（2026-09-28 17:45 jobs + compose up gateway，容器内实测抓到完整 /health payload）；处置留痕：首次直接配 0.0.0.0 曾致 jobs 重启循环，已即时回滚再走代码路径——改生产配置前必先核对接收端约束。
- [BUG-0225](问题-0225-生产实例事实入库与容器root运行.md)：生产 IP/root 用户名/出海代理 IP 硬编码在 git 追踪的 deploy.sh、compose.yml extra_hosts、Caddyfile 注释与 docs 多处（违反"实例事实进 .local"边界），叠加 Dockerfile.runtime 无 USER 三常驻容器以 root 运行；已修复（deploy 目标与代理 IP 全部 .env 变量化 `:?required` 同 POSTGRES_PASSWORD 模式、镜像固定 UID 1000 非 root、git 追踪文件三 IP 零残留、deploy.sh 兼容 CRLF、deploy 域 4 处活死链顺带清、私有 runbook 同步四项一次性发布前置），待发布（发布前必做四项：服务器 .env 加两个代理 IP 变量 + data/app chown 1000:1000 + 本地 .env 建 JUHE_AI_DEPLOY_SERVER + 上传变更后的 compose.yml/Caddyfile/Dockerfile.runtime——deploy.sh 只传二进制不传配置；真实值与命令见私有 runbook）；已发布（2026-09-28 16:00 全量，四项前置完成、容器 uid=1000(app) 生效、git 追踪文件零实例 IP）；已知残留：AGENTS.md 的 .local 资产文件名引用与 git 历史待用户裁决。
- [BUG-0224](问题-0224-seed默认超管不强制首登改密.md)：seed 默认超管 admin/admin 且 `must_change_password=0`，403 改密 gate 不触发，新部署窗口内管理面可被完全接管（现有生产已改密零影响）；契约裁决：Go seed 声称移植的 Node 归档 seed 文件不存在、Node 运行时 gate 无角色豁免、部署指南现行人步骤即系统意图，裁决置 1；已修复（pg/sqlite 三处 seed 路径置 1 + 裁决依据落常量注释 + 测试同步 + 部署文档更新；dev 自动登录路径硬编码 false 不经 gate，隔离开发流程已核验不受影响），已发布（2026-09-28 16:00 全量；仅影响新 seed 实例，现有生产零影响）。
- [BUG-0223](问题-0223-复审P3防御性小修批次.md)：复审 P3 防御性小修五项——jobsched inBackoff skip 缺 rearm（靠隐式不变量续命的脆弱接缝）、`job.pending` 无置位路径的快照假象、余额探测租约用已取消 ctx 释放、chat invokeModel panic 路径上游 Body 不关闭、`w2_outcome_log_test` 无锁 buffer 竞态（-race 下 5 测试失败，HEAD 基线既有）；已修复（幂等 rearm + 注释固化 + WithoutCancel 5s 收尾 ctx + defer 化 Close + 换 raceBuffer），jobsched -race 清零、chat 全包绿。
- [BUG-0222](问题-0222-gatewaycircuit定时器契约违约与确认补结算无痕.md)：gatewaycircuit 两层——默认 NewTimer 的 stop 只 timer.Stop 不关 done（wait.go:82-87 契约注释逐字预言的违约形态），rebuild 抢跑后 retry goroutine 泄漏到 Bridge.Close，生产装配未注入 NewTimer 恒用违约默认；叠加 releaseAcquiredConfirmation 二次补结算失败 `_, _ =` 无痕；已修复（默认实现对齐 wait.go sync.Once 范式 + ServiceOptions 新增 Logger 注入 + 生产装配传 chainCircuitWaitLogger + 3 个回归测试），已发布（2026-09-28 16:00 全量）。
- [BUG-0221](问题-0221-种子目录行数断言时间炸弹.md)：`TestRunStorageBootstrapSQLiteEndToEnd` 期望值硬编码 118 与被测"按当前 UTC 日期过滤 shutdown 到期行"口径不对称——`gpt-3.5-turbo-1106`（ShutdownDate=2026-09-28）到期后测试自当日起新增恒失败（117≠118），原注释已预告"下一批 2026-09-28 需同步更新此值"；已修复（期望值改同源对称计算：期望库重放同一 ensure+seed 出口后执行同一条 count 查询，未来 shutdown 日期到达自动适应；gateway 侧核实口径对称无同款炸弹），本地当日两次失败复现转绿、包全量无回归。
- [BUG-0220](问题-0220-jobsched每轮泄漏goroutine与ctx节点.md)：jobsched 两层资源泄漏——`contextBoundToStop` 每次调用新建 1 个只等 stopCh 的监听 goroutine（任务结束不释放），叠加 `runOnce` 里 WithTimeout 覆盖 cancel 变量使 WithCancel 层永不取消（Timeout>0 即触发，生产全部任务如此）；每轮净增 1 goroutine + 1 ctx 节点、每天约 24 万轮，长驻 jobs 进程数周量级 OOM；已修复（stopBoundCtx 提升为 Scheduler 级单次构造 + Stop/StopAndDrain stopOnce 内原子取消 + 两层 cancel 拆分并在收尾 defer 幂等调用），负向验证旧代码 51 轮泄漏 106、修复后 leaked=0，jobsched 全包测试通过），已发布（2026-09-28 16:00 全量；goroutines 曲线水平化为长期观察项）。
- [BUG-0219](问题-0219-响应检查策略避让PG占位符方言失效.md)：`newChainListAvailabilityDirtyMarker` 家族展开 SELECT 用 `?` 占位符而 gateway pgpool 无驱动层改写、该文件无 bind 包裹——pgx 原样下发报 42601，dirty 失败向上传播致 Redis 避让同不写入，配置类响应检查策略的账号避让在生产 PG 自 2026-09-09 恒失效（仅 finalize Warn 掩盖）；BUG-0177 同款家族残留，SQLite fixture 模拟 PG 方言对占位符差异天然失明；已修复（`?`→`$1/$2` 一行 + 按 0177 先例补真实 dev PG 门禁回归，红验证精确复现 42601、绿验证通过），已发布（2026-09-28 16:00 全量）。
- [BUG-0218](问题-0218-J1显式探活请求对候选构造失败无隔离.md)：J1 显式探活请求（outbox drain 与 runCycle requests 循环）对候选构造失败的账户无隔离——`LoadAccount` 抛错使毒丸行保持 pending 每周期重试，且 runCycle 整轮 return 中断周期 inputs 探测；生产 2026-09-28 单坏账户（ai.onyxaxis.org-公益）16 行 request_failure 停摆 J1 全账户探活 35 分钟（4733+298 条 ERROR）；已修复（两处改 `LoadAccountWithFailures` 隔离语义，构造失败按 input_stale 终态收敛出队），生产已先行止血（16 行备份 `/opt/juhe-ai/backup-j1-outbox-f6cfe3ad-20260928.csv` 后 consumed，J1 恢复补跑 227 账户），已发布（2026-09-28 12:33 jobs，发布后归零恢复正常节奏）。
- [BUG-0217](问题-0217-AI问答批次3小修四项.md)：绑定模式上线后四项小缺陷——`toolCapabilities` 恒按专用 Key 路由策略计算（group/account 作用域错位）、bind-options 响应无 `Cache-Control: no-store`、新建弹窗分组/账户下拉空时无空态提示、Caddy 对 `/__aisys__/api/my-chat/*` 无显式 `flush_interval -1`（SSE 增量依赖反代自动行为）；已修复（作用域按 bind_mode 收敛 + no-store + 空态"暂无可绑定对象" + 显式 flush 对齐 `/v1/*`），待发布验证。
- [BUG-0216](问题-0216-AI问答绑定调度预检三缺陷.md)：`group`/`account` 会话发送预检三缺陷——chat 调度覆盖注入候选后仍执行 Key 策略 normal 路由判定（专用 Key 路由与绑定对象无关，误拒）、绑定作用域未接入外部 `/v1` 同款可恢复等待（候选冷却立即失败）、停用专用 Key 创建会话 500（与 api_key 模式 400 不对称）；已修复（跳过 normal 路由判定 + 绑定作用域可恢复等待 + 400 `chat_invalid_request` 对称），待发布验证。
- [BUG-0215](问题-0215-AI问答停机丢弃在飞轮次与手动压缩阻塞请求.md)：停机与压缩的生命周期语义违背契约——SIGTERM 时 `GenerationHub.Shutdown` 从未接进组合根 shutdowns（注释宣称 drain first，在飞轮次被直接丢弃），手动压缩 claim 后在请求内同步等待执行完成（连接断开可能中止已受理压缩、长压缩悬挂请求）；已修复（`hub.Shutdown(8s)` 按 LIFO 注册使排空先于链关闭 + claim 受理立即 202、压缩后台脱钩执行），待发布验证。
- [BUG-0214](问题-0214-AI问答长轮次误杀与内容丢失.md)：20 分钟中断阈值按轮次开始计且流式期间不刷新 `active_started_at`（合法长生成可远超 20 分钟被误判 `stream_interrupted`），叠加成功路径 `CompleteChatTurn` 失败不走恢复路径——已流出内容不落库、无终止事件；修复中（流式期间周期刷新 `active_started_at`，阈值语义变为“最后活跃后 20 分钟” + 成功收口失败同型走恢复），待发布验证。
- [BUG-0213](问题-0213-AI问答模型载荷缺step与目录漏未定价模型.md)：`generationParameters` 缺 `step`（滑块步长退化）+ chat 面目录未传 `IncludeUnpriced`（生产 5 个未定价模型从 chat 模型列表消失）；修复中（`step` 透出 + `IncludeUnpriced: true` 对齐 Node `includeUnpriced`），待发布验证。
- [BUG-0212](问题-0212-AI问答读取投影与缓存克隆丢内容块字段.md)：REST 读取投影与前端 IndexedDB 克隆两层丢内容块字段（`output_text`/`reasoning`/`tool_call` 的 `order`、`tool_call` 的 `item`/`blockId`、`output_image` 的 `mimeType`/`width`/`height`/`revisedPrompt`）——刷新/缓存命中后正文块丢失、工具明细与图片元数据消失；修复中（后端投影 + 前端克隆同步补齐），待发布验证。
- [BUG-0211](问题-0211-AI问答流式体验断裂.md)：SSE 收集器 `io.ReadAll` 全量缓冲致页面收不到增量 + runner context 挂请求 context 致客户端断开即取消生成（违背“服务端跑完+重附恢复”契约）；修复中（增量解析 + runner 脱钩：断开后仅停止写响应，生成继续，attach 恢复同轮），待发布验证。
- [BUG-0210](问题-0210-AI问答内置工具能力静态兜底缺失.md)：内置模型的工具/模态能力是读取链派生数据——数据库目录表本无 `supported_tools`/`input_modalities`/`output_modalities` 列，Node 由读取链 `toBuiltInCatalogItem` 用“本地官方能力快照”（静态定价表）兜底填充；Go 迁移只把兜底接到管理面（`ApplyBuiltInStaticDerivedFields`），chat 面目录读取链遗漏，SupportedTools/模态恒空：web_search/generate_image/diagnostic_echo 永不注入、会话工具能力面板恒“不可用”、图片输入被拒、需搜索模型不再偏好 Responses 协议；已修复（chat 面补同源静态兜底 + 真实数据链回归测试），待发布。
- [BUG-0209](问题-0209-jobsched交接自死锁lane永久busy.md)：jobsched `releaseLane` 交接把 `runningJob` 置为队首任务名并唤醒它，但 `acquireLane` 只认空 lane——被唤醒任务把自身重新排队且永不释放，lane 自死锁、全体成员永久 `resource_lane_busy`（跳过仅 Debug 无告警）；生产 08:30 发布后 `external-account-maintenance`/`stats-online` 双双首轮交接即冻结（余额探测补偿与用量统计聚合各只跑一轮），昨晚 23:24 后 recovery 轮次消失同因；已修复（acquireLane 交接再入认领 + 组合回归测试），已发布生产（2026-09-28 09:05 jobs 单独发布，lane 恢复轮转、补偿/聚合任务恢复推进）。
- [BUG-0208](问题-0208-F4租约丢失组件无限重试回放存储错误.md)：F4 `operationlog.LeaseKeeper` 未同步 BUG-0196 给 F3 的语义修订——续租传输失败无 2×TTL 放宽窗口、组件重启不触库重取（回放存储错误的 30s 无限循环，生产 05:37~07:14 约 1.5h 无 F4 owner）、producer 冻结 fence 快照（reacquire 轮换 token 后写入恒被拒，F3 同缺口）；已修复（对齐 F3：放宽窗口 + reacquire + `LeaseSource` 实时读 lease，F3/F4 producer 同改），已随 2026-09-28 08:30 发布上线（发布后 F4 lease 续租正常）。
- [BUG-0207](问题-0207-AI性能监控默认窗口塌缩前天单日.md)：`normalizeStatsDateRange` 把 Node 回退链（`startDate ?? endDate ?? defaultStart` / `endDate ?? startDate ?? defaultEnd`，只引用原始入参）错译为顺序传染——无参数请求的 endDate 被已填充的 startDate=today-2 占据，defaultEnd（今天）成死代码，AI 性能监控默认窗口恒塌缩 `[today-2, today-2]` 单日（days=1），页面首载/重置后显示如 28 日看到 26→26；已修复并发布生产（2026-09-28 gateway `7b6d6af5`：回退改引用原始入参，全空=[today-2,today] 三天窗口、半参数保持单日；隔离环境 API 直调/页面首载/点击重置三重验证修复后 26→28）。
- [BUG-0206](问题-0206-余额首探收口残留J2快照CAS被首探并发写入恒stale.md)：due 等值围栏微秒舍入失配（2026-09-28 根因改判，原「快照 CAS 恒 stale」定性作废）——PG text 列 cast timestamptz 四舍五入（.690411968→.690412）与 pgx 绑定 time.Time 截断（→.690411）恒差 1μs，`EnableDetectedQuery`/`CommitDetectionDue` 对 0204 修复前的 9 位纳秒存量 due 恒 0 行，recovery 恒 stale、12+ 候选永不收口、J2 每 10s 重探；已修复（等值围栏改绑定原文文本+双侧 cast：recovery 围栏/游标 + shared `AdvancePeriodicDue` 同族预防；零数据迁移，存量值首次命中后自然归一毫秒文本），已随 2026-09-28 08:30 发布上线（09:05 jobs 调度修复后 recovery 恢复轮转，候选集 12+→0 全部收口，staleCount 恒 0）。
- [BUG-0205](问题-0205-J3b账户模型映射PG整型列布尔字面量42883.md)：J3b 账户模型映射查询 PG 分支对 integer 列写 `enabled=TRUE`——模型检测页面选账户后选模型即 500，日志 `read J3b account model mapping: operator does not exist: integer = boolean (42883)`；已修复（改整数字面量 `enabled=1` 对齐双方言 schema + 录制驱动锁定 PG SQL 文本契约；全库 `=TRUE` 字面量逐处核对列类型，其余均正确），待发布。
- [BUG-0201](问题-0201-supeai对代理客户端UA做无响应挂起且系统UA过度扩大.md)：生产网关系统请求（手动测试/J1 探针）对 supeai.cc 必超时——实锤 supeai 对"已知代理客户端"UA 的 POST 做无响应挂起（SOCKS/TLS 全通、HTTP 零字节 40~70s；同隧道 A/B 对照 opencode 挂、中性 UA 200），而 BUG-0176 的 OpenCode UA-only 兜底过度扩大把该身份打给了所有 api_key 系统请求；已修复（`upstreamidentity` 改「上游家族→官方客户端身份」统一选择：GLM→ZCode 全套、GPT/Codex→Codex Desktop 静态 UA、Anthropic OAuth 全量/API Key 不注入身份、provider openai 泛化档案不构成家族依据、未知上游一律不注入身份；附带 Anthropic 探测 payload 瘦身 max_tokens 1024），遗留该上游与 UA 无关的间歇性无响应（供应商质量，建议备用渠道）。
- [BUG-0204](问题-0204-余额首探意图围栏失配候选死循环上游429.md)：余额探测意图围栏把 `time.Time` 参数直接与 TEXT 列 `balance_query_next_refresh_at` 等值比较恒不失配——`CommitDetectionDue` 永不命中，16 个首探候选 18 小时无法收口/启用，被 J2 循环按默认 5s 扫描节奏反复重探致上游 429（BUG-0196 due 格式家族）；已修复（PG 围栏改 `::timestamptz` 等值 + 写入统一 RFC3339Nano 毫秒截断文本 + ListDueCandidates 比较 cast），待发布。
- [BUG-0203](问题-0203-relay_balance投影缺失列表与明细读端恒空.md)：Node `account-balance-jobs-projector` 未随 Go 迁移移植，`juhe_stats` relay_balance 行数恒 0——J2 快照只落 `juhe_jobs`，余额列表/明细读端全部落空，多 Key 逐 Key 明细无处可读；已修复（新增 `account-balance-stats-projection` 投影任务：J2→stats UPSERT 幂等投影 + 探测链路接入多 Key 执行与多 Key 快照形状），待发布。
- [BUG-0202](问题-0202-jobsched超时建议性handler卡死lane静默停摆.md)：jobsched Timeout 仅 ctx 取消，超时判定/lane 释放都在 handler 返回之后——handler 卡死即 `external-account-maintenance` lane 被永久占死（生产静默停摆 9 小时+），同 lane 任务只产生 Debug 级 lane_busy 跳过零 WARN；已修复（handler goroutine 化 + 超时强制回收 lane + 迟到结果丢弃 + stuck 指数 Error 留痕），待发布。
- [BUG-0200](问题-0200-管理列表缺失余额字段余额整行不渲染.md)：Go 管理列表投影丢失 Node 的 `balanceQueryEnabled/balanceQueryNextRefreshAt/balanceSnapshot` 三字段，前端余额行 `v-if` 整行不渲染——生产 480 个启用账户余额数据 fresh 却全部不显示；已修复（列表 SQL 增读两列 + ListPage 水合批量叠加 stats relay_balance 快照，匹配围栏 + 剥 keyBalances + authorized 视图隐藏 + 空表降级），待发布。
- [BUG-0199](问题-0199-会话bind名快照列NULL扫描导致存量会话列表详情500.md)：会话绑定模式五列加列交付后，存量会话行的 `bind_group_name_snapshot`/`bind_account_name_snapshot` 为 NULL，扫描结构体声明为普通 `string`——PG 扫描 NULL 直接报错，存量用户的会话列表/详情整体 500；已修复（快照列改 `sql.NullString`，NULL 归一空串、JSON 形状不变）+ 存量 NULL 行回归测试（未修复 FAIL 已验证），生产验收恢复 200。
- [BUG-0198](问题-0198-审计正文捕获装配缺失详情恒未抓取.md)：审计设置适配器从不赋值 `FullBodyCaptureEnabled`（恒 false），审计主开关无法传导到正文捕获——refs 599 条仅 28 个正文 blob，详情页恒"未抓取"；已修复（适配器开关一致 `FullBodyCaptureEnabled=enabled` + 生产采样率显式配 1，成功正文全量长期保留）。
- [BUG-0197](问题-0197-用量统计元数据水合占位符参数多传.md)：用量统计 caller_account 视角（系统账户筛选 / my-stats）元数据水合查询 headParams 多传一个参数——SQLite 静默丢弃 chunk 末位账户（数据悄悄缺失），PG 参数计数不匹配直达 500（「用量统计加载失败」）；已修复（参数收敛为 3 + 测试断言反转为正确契约），statreads 全量方言审计与三方表契约核对无同族残留，待发布。
- [BUG-0196](问题-0196-PG写超时三缺陷J1转正瘫痪F3永久失租J2due不推进.md)：PG 阵发写超时引爆三缺陷——J1 cursor 保存失败丢弃整个探针 outcome 致新账户永久卡 `pending_test`；F3 owner lease 一次续租超时永久 terminal 致审计丢弃+健康 503；J2 周期余额刷新从不回写 due 游标致 475 账户每 5s 重复探测。三修复（cursor 降级 best-effort / 2×TTL 放宽窗口+reacquire / `BusinessDueStore` 按契约写回 due）已完成待发布，终审无 blocker；遗留 producer 旧 fence 快照缺口等 5 项已登记；dev PG 容器缺位致 PG smoke 未跑待补。
- [BUG-0195](问题-0195-运行日志文件写侧缺失读面恒空.md)：go-only 网关 slog 仅挂 stdout，运行日志 JSONL 文件写侧未移植（grep 扫描面/jobs 索引器/保留参数三个消费面齐全但无生产者），运行日志页与 `juhe_dataset.runtime_logs` 恒空；已修复（2026-09-28：FileSink 大小滚动写侧 + `slog.Default()` 接管使访问日志 JSON 进文件 + compose 显式 `JUHE_AI_LOG_DIR` 三方同目录；实施前裁决修正草案「按天滚动」为按大小滚动对齐全部消费面契约；跨端回放测试锁定双端契约；待发布生产）。
- [BUG-0194](问题-0194-J1直读输入基线缺失与健康监控小时表断源.md)：三层断裂——① J1 直读候选 INNER JOIN 输入版本表，该表只被事件路径惰性创建，迁移/全新部署恒空致探针零执行；② go-only 形态探针直连上游不产生 `account_health_check` 使用记录，statsagg 聚合源不存在，`account_health_hourly` 恒空；③ 探针流量不进使用记录（Node 经 /v1 派发链天然产生）。①已修复（生产幂等回填 1517 行 + jobs 启动自动播种）；②已修复（J1 投影面直写小时条带 newest-wins 对齐 statsagg 口径 + 历史结果受控回填 241 行）；③已修复（ProbeUsageRecorder 补记探针使用记录，作用域五元组按业务库解析+归一化安全网，来源筛选/用量统计恢复 Node 语义）。
- [BUG-0193](问题-0193-容器缺WORKDIR用量spool交接断裂统计全空.md)：单机容器镜像未设 WORKDIR 且未配 spool 目录，gateway/jobs 各自把用量 spool 解析到容器私有 `/data`，交接断裂致 `usage_records` 恒 0 行、生产全部统计为空，且积压记录随容器重建静默丢失；已修复（compose 为 gateway/jobs 加 `working_dir: /app/backend` 并同步服务器重建，45 个积压文件抢救补写入库，记录/聚合/文件日志均验证恢复）。
- [BUG-0192](问题-0192-可用性维护事务与逐claim事务跨表死锁.md)：jobs 进程内部两并发任务跨表互锁（`EnqueueAllForRuntimeRecovery` 批量重置 dirty + viewer_health 全量置 stale vs 逐 claim 短事务），复检又实锤第三环（`proxy_profiles` 测试写回经库端触发器隐式写 dirty，应用锁覆盖不到）；四轮修复（拆双短事务、应用侧 dirty 写事务 advisory 锁、触发器汇聚函数 `mark_dirty_accounts` 热修取同一锁键、应用侧锁部署补齐）已闭环，预期 40P01 归零，待晚间高峰后观察确认；修复中（观察期）。
- [BUG-0184](问题-0184-可用性投影维护与网关脏标记路径死锁.md)：`account_list_availability` 投影维护与网关脏标记路径加锁顺序成环，`account-list-availability-projection-maintenance` 反复 40P01 失败（consecFail 18，2h 94 条死锁）；首轮 ApplyClaims 排序+逐 claim 短事务当晚复发，已转入 BUG-0192 续修并完成 advisory 锁闭环；已修复（随 0192 观察期确认归零）。
- [BUG-0183](问题-0183-mockdata占位行毒丸卡死J1调度器.md)：mockdata autofill 向 `account_health_probe_request_outbox` 插入 `source_fence` 非 JSON 的占位行，J1 drain 的 `ClaimPendingProbeRequests` 在 claim 阶段逐行解析裸返回 `*json.SyntaxError`，一行毒丸使整个 claim 中止、owner lease 反复释放、J1 无限失败循环；已修复（autofill 跳过清单登记 + claim 确定性损坏行按行隔离出队并加测试锁定，本地 dev 数据已恢复、租约续约验证通过；重启 dev 后隔离代码生效）。
- [BUG-0182](问题-0182-统计缓存离线重建CLI缺失.md)：统计缓存离线重建 CLI 缺失——Node `rebuild-usage-stats.js` 已随 Node 后端归档删除，Go maintenance 无等价命令，SQLite standalone 与 PG performance 两种模式下统计缓存损坏后无离线重建入口（Node 原语义与建议方案见文档，D5 登记）；待修复。
- [BUG-0181](问题-0181-网关目录源丢失openai兼容供应商聚合语义.md)：网关运行时缓存目录源只按单码查询，丢失 Node 的 openai 兼容供应商源扩展聚合语义（openai/v1 子供应商 + 自己），AI 对话与 `/v1/models` 对 openai/hybrid 分组稳定返回空目录而管理面正常；已修复（补回源扩展/合并/过滤/排序并重写聚合回归锚点，真实库只读验证聚合出 100 个含 glm-5.3 的模型）。
- [BUG-0180](问题-0180-proberepo脏时间戳panic崩溃循环.md)：proberepo reader 对库中非法 `account_expires_at`/`cooldown_until` 曾走 `panic(err)` 而非错误返回，probe worker 读到一条脏行即 panic→supervisor 重启→崩溃循环拖垮全部账户探针；已修复（2026-09-20 提交 `46c367e18`：两处 panic 改错误返回 reader.go:359/382，新增脏时间戳回归 w18_dirty_timestamp_test.go；jobs cmd `worker_probe_jobs.go` 错误传导臂已自然可达；2026-09-20 复核附证据）。
- [BUG-0179](问题-0179-CodexResponses画像无法选中Chat桥账户.md)：端点模式闸对 codex_responses 画像请求先于映射分支无条件要求 `responses_sse`，Codex CLI 客户端无法选中 chat-only 档案桥账户（GLM chat/DeepSeek/hybrid chat），BUG-0178 修复后的桥对主力客户端仍不可达；预存在限制，Node 语义优先顺序待取证；待取证。
- [BUG-0178](问题-0178-Responses到Chat桥执行门控缺失端到端断裂.md)：`responses -> chat_completions` 桥在 Go 网关许可层放行但转换执行门控缺失，Responses 格式请求体原样发往 chat 上游、chat 响应原样回给 Responses 客户端；运行时裁决测试确认（2026-09-19）；已修复（矩阵补组合 + 双侧裁决测试转绿，独立复审通过，组合根全包回归见文档验证记录）。
- [BUG-0177](问题-0177-PG组合根装配与手动测试仓储占位符缺陷.md)：PG 模式 accountbalance.OpenStore 无条件要求 PostgresURL 导致组合根恒失败，manualtestrepo 的 `?` 占位符经 pgx 原样下发致 PG 全部任务 SQL 报语法错误；已修复并以真实 dev PG 门禁化测试回归，待合并。
- [BUG-0176](问题-0176-系统主动请求缺少渠道身份被上游拒绝.md)：系统主动请求缺少统一渠道身份，导致同一 GLM Coding 账户在 OpenCode 可用、项目人工测试/探针被上游拒绝；已补 ZCode 静态身份、无精确身份时的 OpenCode UA-only 兜底，并统一人工测试、J1、J3b 和模型目录调用面，待轮换测试 Key 后真实验证。
- [BUG-0174](问题-0174-账户核心专项深查缺陷清单.md)：账户核心专项深查（调度/状态/探活/协议）——5 blocker + 10 major + 12 minor；2026-09-07 两波清偿（指纹统一/轮转策略/隔离谓词/J1 投影器移植/skip 作用域/busy 轮换/键格式/创建路径链/ENGAGED 锁接线/协议主链认证与错误体）；已修复（B-4 转换器移植待用户裁决）。
- [BUG-0173](问题-0173-Go运行时生产安全门禁缺失与信号错位.md)：Go 运行时缺 5 项生产安全门禁且死配置强制无效队列 URL；首次实现信号错读 JUHE_AI_NODE_ENV 被复审拦截，已统一为 NODE_ENV 并对齐 Node 解析语义（2026-09-06）；已修复。
- [BUG-0171](问题-0171-K2会话接口与多实例状态契约偏离.md)：K2 会话五子项含多实例 Redis 态已全部落地（2026-09-06 复核）；残留 login-guard fail-open 语义裁决与真实多实例回放；已修复（附残留登记）。
- [BUG-0170](问题-0170-K2系统账户创建与管理结果不等价.md)：K2 系统账户创建遗漏默认资源，且 null、日志、缓存和校验结果仍偏离 Node；options 与 PATCH 非 nullable 回执切片已修复；待修复。
- [BUG-0169](问题-0169-K2认证高权限令牌与所有权门禁缺失.md)：临时令牌 allowlist/参数契约、super_admin 治理、OwnerGate、PG 并发不变量四子项全部落地（2026-09-06 复核）；残留启动 CheckContract 未接 compose（有兜底）；已修复（附残留登记）。

- [BUG-0168](问题-0168-S-PGSchema与默认Seed迁移不完整.md)：PG ensure-schema/--seed CLI 入口与默认 seed 补全已确认落地（2026-09-06 复核）；已修复。
- [BUG-0167](问题-0167-S-SQSQLite初始化与Seed未接入.md)：SQLite schema/seed 生产入口（maintenance --ensure-schema/--seed）已确认存在（2026-09-06 复核）；已修复。

- [BUG-0166](问题-0166-M03系统团队迁移权限状态与副作用偏离.md)：2026-09-06 补副作用生产接线与 PG keyword 语义，两子缺陷此前已修；已修复。
- [BUG-0165](问题-0165-M04授权迁移权限与数据副作用偏离.md)：2026-09-06 实现窗口读层+8 条 usage 聚合+管理面 return/usage+revoke owner scope+strict 校验+列表投影并接线；修复中（剩余 rich summary 字段与 PG 集成验证）。

- [BUG-0164](问题-0164-M06路由策略迁移遗漏端点与失效副作用.md)：本包已修复（2026-09-05 审查即修复波次），移交项见文末处置记录。
- [BUG-0163](问题-0163-M05分组迁移遗漏端点统计与约束.md)：已修复（groups 包内，2026-09-06）；4 项跨包主张登记移交（见文末裁决）。

- [BUG-0162](问题-0162-M08账户迁移遗漏端点与运行态副作用.md)：2026-09-06 修复 revalidate/删除失效/导入 SSRF；2026-09-07 两执行端口以共享包进程内接线（无跨进程桥）；已修复。
- [BUG-0161](问题-0161-M07APIKey迁移缺少更新与用量契约.md)：M07 API Key 缺少 PATCH、真实 usage 与必需 validation cache 失效；待修复。

- [BUG-0160](问题-0160-K7Mock上游未记录模型与流式字段.md)：复核确认全部子项已消除（2026-09-06）；已修复。
- [BUG-0159](问题-0159-K6Legacybridge前缀翻转缺少并发保护.md)：K6 legacybridge 前缀注册/删除与请求遍历共享 slice 无并发保护；已关闭（X01 随 legacybridge 包整体删除失效，非代码修复）。
- [BUG-0158](问题-0158-K5缓存失效总线丢通知且注销失效.md)：注销/节流此前已修，2026-09-06 补 handler panic 隔离；已修复。
- [BUG-0157](问题-0157-K4操作日志清洗结果偏离Node.md)：2026-09-06 SafeChange 对齐 Node + sink MaxChanges 校验；已修复（DetailLevel 分布未验证已登记）。
- [BUG-0156](问题-0156-K3内存限流双桶键冲突.md)：2026-09-06 四子项清零（双桶键/JSON 500/Redis 错误映射/只读 POST 规则）；已修复。

- [BUG-0155](问题-0155-M04授权去重ProcessingTTL单位错误.md)：M04 Go 授权去重 `ProcessingTTL` 曾将纳秒常量误作 120ms，现已修正为与 Node 等价的 120s；M04 其他迁移缺口仍待修复。

- [BUG-0154](问题-0154-K1-GoHTTP内核横切契约偏离.md)：K1 Go HTTP 内核在压缩协商/缓冲、trace、安全头、body parser 与 mutation 去重时序上偏离 Node 契约；17 项已按 Node 锁定依赖实测修复并建立 golden；cmd/acceptance 全量回归待并行迁移批次落定后重跑。

- [BUG-0153](问题-0153-M01公告迁移端点与数据契约偏离.md)：2026-09-06 全子项清零（公开面三路由/投影/Create 错位/no-op/严格校验/日志/分页）；已修复。

- [BUG-0152](问题-0152-Go管理迁移未接入唯一入口导致K2路由失效.md)：2026-09-06 复核确认 Go 已挂载唯一入口，前端调用面静态对齐；已修复（残留归档 manifest 簿记）。

- [BUG-0151](问题-0151-Go健康探活关闭HTTP2导致代理链误报上游连接失败.md)：Go J1 在自定义 SOCKS5H 拨号器上关闭 HTTP/2，导致代理链返回的 HTTP/2 SETTINGS 被误判为上游连接失败；现已将 J1/J2/J3a 上游 transport 与 SOCKS5 握手收口到 `shared/platform/upstreamhttp`，完成隔离生产凭据和 Go 全项目复查，待生产发布。

- [BUG-0150](问题-0150-管理API时区改写导致跨时区超时.md)：管理 API 曾将绝对时间改写为 `Asia/Shanghai` 无 offset 字符串，导致跨时区客户端出现超时、到期或排序偏移；现已改为严格 RFC3339 `Z` / 显式 offset 契约，后端和账户测试定向回归通过，前端显示时区回归仍受无关断言阻断，真实浏览器和生产验证待完成。

- [BUG-0149](问题-0149-多轮生产发布停机与流程失控.md)：多轮生产发布把构建、候选准备、切流、验证和清理混在同一窗口，导致硬停机和小时级操作；现收敛为独立 candidate、带时效指纹 preflight、原子 handover 与保留旧槽回切。

- [BUG-0148](问题-0148-Windows路径转换破坏前端APIBase.md)：Windows/MSYS 把根相对前端 API base 转换为磁盘路径，导致 health 和静态页面正常但浏览器管理 API 全部失败；已禁止 Windows Bash 正式构建，生产 Mac 包改为原生 macOS 构建，并保留真实浏览器 candidate 门禁。

- [BUG-0147](问题-0147-mac候选发布依赖预检缺失.md)：macOS temporary 候选发布没有在启动前验证离线依赖 store 与受支持 Node 版本，在线安装会无界等待；待补齐预检门禁。

- [BUG-0146](问题-0146-mac网络切换导致WireGuard假活与root-wrapper权限缺陷.md)：macOS 网络切换后 WireGuard job 假活，并且 root job 执行服务用户可写 wrapper；收口为 root-only manifest、wrapper/config 迁移与受限全 Edge 恢复器。

- [BUG-0145](问题-0145-模型目录预检错误阻断账户可用性.md)：自动账户测试和健康检查把上游模型目录当作强制前置条件，导致目录未开放的可用上游无法激活或使用；现仅保留用户显式目录同步，关联 BUG-0143 / BUG-0144。

- [BUG-0144](问题-0144-多供应商模型目录探针被本地能力过滤拒绝.md)：Gemini、DeepSeek、GLM 的受控账户模型目录探针被本地模型路由或 Chat-only 能力过滤拒绝；现仅对内部标记、精确路径和允许的账户类型放行，客户端模型目录请求与不兼容 OAuth 继续走本地或拒绝。

- [BUG-0143](问题-0143-xAI模型目录探针被能力过滤拒绝.md)：xAI API Key 的受控模型目录探针被 Chat / Responses 能力过滤拒绝；现仅对内部标记的 API Key 检查放行，未标记请求与 Grok OAuth 继续拒绝。

- [BUG-0142](问题-0142-Codex压缩失败终态误判缺少完成事件.md)：Codex Remote Compaction V2 收到精确 `response.failed` 后仍等待 EOF，并错误生成本地 compact 契约 mismatch；现已改为结构失败终态直接进入通用失败路径。

- [BUG-0141](问题-0141-使用记录PostgreSQL批量落库死锁.md)：高性能模式多个 usage worker 以不稳定顺序更新账户，且与 usage 唯一索引锁交错，造成 PostgreSQL `40P01`；统一账户行锁顺序并固定事务边界。

- [BUG-0140](问题-0140-余额自动探测误判不支持结果.md)：余额自动探测把统一查询器正常返回的 `unsupported` 误当能力命中；修复状态分类并补齐真实返回形态回归。

- [BUG-0139](问题-0139-GooseFreshSchema迁移链断裂.md)：W7 fresh PostgreSQL 连续暴露 migration 77 依赖未迁入的 Node Chat 表、migration 80 把 JSON 文本列误当 `jsonb`；修复所有权边界、物理类型和隔离 harness，并从零验证 Goose 92。

- [BUG-0138](问题-0138-CodexResponses双向ID防护失效.md)：Codex Responses 错误 `item_*` 工具 identity 未在响应侧实际修复，并污染下一轮请求；补齐请求双检查点、按 identity 暴露边界修复和严格拦截换号边界。

- [BUG-0137](问题-0137-管理弹窗无交互预加载与全量读写.md)：Node + Vue 管理弹窗在无交互时预取候选，列表 / 编辑 / PATCH 又复用宽摘要并扩大查询、写入和缓存失效范围；Go 不在修复范围。

- [BUG-0136](问题-0136-AI账户测试弹窗打开即加载模型目录.md)：账户列表已返回检查默认值，但测试弹窗打开仍无条件加载完整模型选项，破坏两级按需加载契约。
- [BUG-0135](问题-0135-performance内部代理追加回环地址.md)：performance 内部 Nginx 把本机回环地址追加到可信代理链，导致所有来源被识别为 `127.0.0.1`。
- [BUG-0129](问题-0129-performance进程指标有限SCAN漏采.md)：performance 进程注册项在大 Redis 键空间中被有限页 SCAN 随机漏掉，导致 Gateway 事件循环指标缺失。
- [BUG-0128](问题-0128-动态管理数据缓存遮蔽最新事实.md)：使用记录、表监控与默认页面保活返回旧动态数据，且真实上游错误被统一文案覆盖。
- [BUG-0127](问题-0127-图片请求被普通路由首字截止提前切号.md)：图片 lane 误用普通路由 10 秒首字截止，可用生图账户尚在生成就被提前中止。
- [BUG-0126](问题-0126-AI对话过程错误被通用文案吞没.md)：生成与内部工具错误只保留通用状态，SSE 还漏接工具失败事件，导致前端无法直接诊断。
- [BUG-0125](问题-0125-AI账户模型协议未随选项返回.md)（历史；账户测试批量能力响应已废止）：供应商模型选项裁剪协议能力，曾导致图片模型在账户表单和人工测试中仍显示 Responses。
- [BUG-0124](问题-0124-网关成功终态被连接关闭覆盖.md)：协议成功终止后的连接关闭覆盖成功汇总为 aborted，点号事件在日志搜索未映射。
- [BUG-0123](问题-0123-人工账号测试展示网关改写错误.md)：人工测试混用上游诊断和下游改写响应，导致终端展示网关生成的重试错误。
- [BUG-0122](问题-0122-发布模型快照退场后旧链路仍空转.md)：动态模型目录上线后，旧发布快照生产、预热、dirty rebuild 和测试契约仍在空转。

> 面向 AI 和维护者。
> 这里是项目 bug 的历史记录入口，用来沉淀“发生了什么、为什么发生、怎么修、以后怎么避免”。
> 通用问题排查和修复流程见 [问题修复指导](../architecture/问题修复指导.md)。
> 新建 bug 记录时，优先复制 [问题模板](问题模板.md)。

## 1. 目录目标

- 记录有复用价值的 bug：容易复发、容易误判、定位成本高、影响面较大、涉及共享层或真实交互。
- 用稳定的 bug 号作为主键，避免标题变化、路径变化后丢失关联。
- 保留“复发关联”和“修复引入新问题”的历史，方便后续快速回溯。

## 2. 编号规则

- 编号格式统一为 `BUG-0001`、`BUG-0002`、`BUG-0003`……按创建顺序递增。
- 编号只增不改，不重复使用，不回收。
- 标题可以调整，文件名可以优化，但 bug 号必须保持不变。
- 建议文件名格式：`问题-0001-简短标题.md`；正文内继续保留稳定编号，如 `BUG-0001`。

## 3. 关联规则

- **同根因复发**：继续使用原 bug 号，在原文档里追加“复发记录”。
- **现象相同但根因不同**：新建 bug 号，并在“关联 bug”里指向旧记录。
- **修复引入新问题**：新建 bug 号，并标注“由 `BUG-xxxx` 的修复引入”。
- **一对多关联**：一个 bug 可以同时关联多个旧 bug，保留最相关的主关联。

## 4. 推荐目录结构

```text
docs/bug/
  README.md
  问题模板.md
  问题-0001-xxx.md
  问题-0002-xxx.md
  问题-0003-xxx.md
```

- 如果后续某个模块 bug 很多，也可以再按模块分子目录，但优先保持路径简单。

## 5. 单个 bug 文档模板

可直接复制 [问题模板](问题模板.md) 作为新记录起点。

模板字段只维护在 `问题模板.md`，本 README 不再内嵌模板正文，避免两个模板版本漂移。

## 6. 新增记录流程

- 先查本目录是否已有相似 bug 号。
- 如果是同根因复发，优先复用原 bug 号，而不是新建相似记录。
- 如果是新根因，创建新 bug 号并补充关联说明。
- 如果修复后再次出现，优先在原文档追加复发信息，再决定是否拆新 bug。

## 7. 记录要求

- 必须写清楚：现象、触发条件、根因、修复方式、验证方式、关联 bug。
- 不只记录“怎么修”，也要记录“为什么会错”和“下次怎么快速定位”。
- 尽量避免只写临时补丁结论；要沉淀可复用的排查经验。

## 8. 建议索引方式

- 新增 bug 文档后，优先在这里补一条索引。
- 索引建议按编号倒序或模块分组维护，保证能快速从编号定位到问题。

## 9. 关系到其他文档时

- 修 bug 前优先参考 [问题修复指导](../architecture/问题修复指导.md)。
- 如果 bug 影响架构、阶段范围、运行流程或存储策略，再同步更新对应文档。

## 10. 问题索引

| 编号 | 标题 | 状态 | 严重程度 | 模块 | 文档 |
| --- | --- | --- | --- | --- | --- |
| BUG-0151 | Go 健康探活关闭 HTTP/2 导致代理链误报上游连接失败 | 已修复（Go 测试与隔离生产凭据验证通过，待生产发布） | P1 | backend-go / J1 / SOCKS5H / HTTP/2 | [问题-0151-Go健康探活关闭HTTP2导致代理链误报上游连接失败.md](问题-0151-Go健康探活关闭HTTP2导致代理链误报上游连接失败.md) |
| BUG-0150 | 管理 API 时区改写导致跨时区超时 | 已修复（后端 / 账户测试通过；前端显示时区回归受无关断言阻断；真实浏览器 / 生产验证待完成） | P1 | 管理 API / 前端 / 时间契约 | [问题-0150-管理API时区改写导致跨时区超时.md](问题-0150-管理API时区改写导致跨时区超时.md) |
| BUG-0149 | 多轮生产发布停机与流程失控 | 整改中，待下一次正式无感发布证明 | P0 | 发布 / macOS / candidate / handover / 回切 | [问题-0149-多轮生产发布停机与流程失控.md](问题-0149-多轮生产发布停机与流程失控.md) |
| BUG-0148 | Windows 路径转换破坏前端 API Base | 已修复并完成生产浏览器验证 | P0 | 发布包 / Vite / Windows / macOS / 生产切流 | [问题-0148-Windows路径转换破坏前端APIBase.md](问题-0148-Windows路径转换破坏前端APIBase.md) |
| BUG-0147 | mac 候选发布依赖预检缺失 | 待修复，阻断 temporary 候选启动 | P1 | macOS / 发布包 / pnpm / Node | [问题-0147-mac候选发布依赖预检缺失.md](问题-0147-mac候选发布依赖预检缺失.md) |
| BUG-0146 | macOS 网络切换导致 WireGuard 假活与 root wrapper 权限缺陷 | 已实现，待生产验证 | P0 | macOS / WireGuard / launchd / 生产稳定性 | [问题-0146-mac网络切换导致WireGuard假活与root-wrapper权限缺陷.md](问题-0146-mac网络切换导致WireGuard假活与root-wrapper权限缺陷.md) |
| BUG-0145 | 模型目录预检错误阻断账户可用性 | 已修复（本地回归通过） | P1 | Node 后端 / Vue 前端 / AI 账户测试与健康检查 | [问题-0145-模型目录预检错误阻断账户可用性.md](问题-0145-模型目录预检错误阻断账户可用性.md) |
| BUG-0144 | 多供应商模型目录探针被本地能力过滤拒绝 | 已修复（仅用户显式同步） | P1 | Node 后端 / 网关 / 供应商驱动 | [问题-0144-多供应商模型目录探针被本地能力过滤拒绝.md](问题-0144-多供应商模型目录探针被本地能力过滤拒绝.md) |
| BUG-0143 | xAI 模型目录探针被能力过滤拒绝 | 本地修复完成，待真实上游验证 | P1 | Node 后端 / xAI provider / 账户模型同步 / 人工测试 | [问题-0143-xAI模型目录探针被能力过滤拒绝.md](问题-0143-xAI模型目录探针被能力过滤拒绝.md) |
| BUG-0142 | Codex 压缩失败终态误判缺少完成事件 | 本地修复完成，待统一上线/生产验证 | P1 | Node 后端 / 网关 / Codex Responses / SSE | [问题-0142-Codex压缩失败终态误判缺少完成事件.md](问题-0142-Codex压缩失败终态误判缺少完成事件.md) |
| BUG-0141 | 使用记录 PostgreSQL 批量落库死锁 | 待发布验证 | P1 | Node 后端 / Usage worker / Redis Stream / PostgreSQL / accounts | [问题-0141-使用记录PostgreSQL批量落库死锁.md](问题-0141-使用记录PostgreSQL批量落库死锁.md) |
| BUG-0140 | 余额自动探测误判不支持结果 | 已修复（本地验证完成） | P1 | 后端 / AI 账户 / ops-worker / 余额查询 / PostgreSQL / 测试 | [问题-0140-余额自动探测误判不支持结果.md](问题-0140-余额自动探测误判不支持结果.md) |
| BUG-0138 | Codex Responses 双向 ID 防护失效 | 已修复（未部署） | P1 | Node 网关 / Codex Responses / 账户派发 / 响应守卫 | [问题-0138-CodexResponses双向ID防护失效.md](问题-0138-CodexResponses双向ID防护失效.md) |
| BUG-0137 | 管理弹窗无交互预加载与全量读写 | 已修复（Node / Vue 类型检查、生产构建、定向矩阵与核心 Browser 矩阵已通过；真实 PostgreSQL smoke 未执行） | P1 | Vue 前端 / Node 后端 / 存储 / 管理接口 | [问题-0137-管理弹窗无交互预加载与全量读写.md](问题-0137-管理弹窗无交互预加载与全量读写.md) |
| BUG-0136 | AI 账户测试弹窗打开即加载模型目录 | 已修复 / 最终回归随 BUG-0137 收口 | P2 | 前端 / AI 账户 / 人工测试 / 按需加载 | [问题-0136-AI账户测试弹窗打开即加载模型目录.md](问题-0136-AI账户测试弹窗打开即加载模型目录.md) |
| BUG-0128 | 动态管理数据缓存遮蔽最新事实 | 已修复（自动化、构建与隔离 Browser 验证） | P1 | 前后端 / 使用记录 / 表监控 / 页面生命周期 / 上游错误诊断 | [问题-0128-动态管理数据缓存遮蔽最新事实.md](问题-0128-动态管理数据缓存遮蔽最新事实.md) |
| BUG-0127 | 图片请求被普通路由首字截止提前切号 | 已修复 | P1 | 后端 / 网关 / 图片生成 / 账户切换 | [问题-0127-图片请求被普通路由首字截止提前切号.md](问题-0127-图片请求被普通路由首字截止提前切号.md) |
| BUG-0126 | AI 对话过程错误被通用文案吞没 | 已修复 | P1 | 后端 / 前端 / AI 对话 / 内部工具 / SSE | [问题-0126-AI对话过程错误被通用文案吞没.md](问题-0126-AI对话过程错误被通用文案吞没.md) |
| BUG-0125 | AI 账户模型协议未随选项返回 | 已修复（定向回归、类型检查、构建与浏览器验证） | P1 | 前后端 / AI 账户 / 模型目录 / 人工测试 | [问题-0125-AI账户模型协议未随选项返回.md](问题-0125-AI账户模型协议未随选项返回.md) |
| BUG-0124 | 网关成功终态被连接关闭覆盖 | 已修复 | P2 | 后端 / 网关 / 运行日志 / 前端日志搜索 | [问题-0124-网关成功终态被连接关闭覆盖.md](问题-0124-网关成功终态被连接关闭覆盖.md) |
| BUG-0123 | 人工账号测试展示网关改写错误 | 已修复（定向回归与类型检查） | P2 | 后端 / AI 账户 / 网关 / 人工测试 | [问题-0123-人工账号测试展示网关改写错误.md](问题-0123-人工账号测试展示网关改写错误.md) |
| BUG-0121 | AI 问答仍读取退场快照导致模型为空 | 修复中 | P1 | AI 问答 / API Key / 动态模型目录 / 前后端 | [问题-0121-AI问答仍读取退场快照导致模型为空.md](问题-0121-AI问答仍读取退场快照导致模型为空.md) |
| BUG-0120 | AI 账户支持模型空缓存长期续期 | 已修复（定向回归、类型检查与浏览器验证） | P1 | 前端 / AI 账户 / 模型目录 / 页面数据缓存 | [问题-0120-AI账户支持模型空缓存长期续期.md](问题-0120-AI账户支持模型空缓存长期续期.md) |
| BUG-0119 | Node 页面数据删除残留与网关可靠性回归 | 修复中 | P0 | Node 后端 / 前端 / 网关 / Redis | [问题-0119-Node页面数据删除残留与网关可靠性回归.md](问题-0119-Node页面数据删除残留与网关可靠性回归.md) |
| BUG-0118 | AI 问答快照约束未迁移导致模型列表为空 | 已修复并完成生产验证 | P0 | AI 问答 / PostgreSQL / 模型目录 / 迁移 / 发布 | [问题-0118-AI问答快照约束未迁移导致模型列表为空.md](问题-0118-AI问答快照约束未迁移导致模型列表为空.md) |
| BUG-0117 | 旁路数据写入放大导致事件循环与后台任务停滞 | 修复中（代码完成，待生产拓扑验证） | P0 | 后端 / Gateway / Usage / Log / Stats worker / Redis / PostgreSQL | [问题-0117-旁路数据写入放大导致事件循环与后台任务停滞.md](问题-0117-旁路数据写入放大导致事件循环与后台任务停滞.md) |
| BUG-0116 | 候选缺设置被误报为网关请求体无效 | 已修复并完成生产验证 | P0 | 后端 / 网关 / PostgreSQL / 发布 / macOS | [问题-0116-候选缺设置被误报为网关请求体无效.md](问题-0116-候选缺设置被误报为网关请求体无效.md) |
| BUG-0115 | AI 问答模型列表重复扫描账户 | 已修复（定向回归与类型检查，待生产验证） | P1 | 前后端 / AI 问答 / 模型目录 / API Key / 性能 | [问题-0115-AI问答模型列表重复扫描账户.md](问题-0115-AI问答模型列表重复扫描账户.md) |
| BUG-0114 | 统计快捷范围空值触发组件告警 | 已修复（回归、类型检查与浏览器验证） | P3 | 前端 / 统计概览 / 系统指标 / Ant Design Vue | [问题-0114-统计快捷范围空值触发组件告警.md](问题-0114-统计快捷范围空值触发组件告警.md) |
| BUG-0113 | 同值保存误清余额快照并丢失刷新计划 | 已修复（待生产验证） | P1 | AI 账户 / 余额查询 / ops-worker / 前端 | [问题-0113-同值保存误清余额快照并丢失刷新计划.md](问题-0113-同值保存误清余额快照并丢失刷新计划.md) |
| BUG-0112 | 统计安全快照超时误报失败 | 已修复（回归与开发运行监控验证） | P2 | 后端 / stats-worker / ingest-worker / 使用记录 / 系统监控 | [问题-0112-统计安全快照超时误报失败.md](问题-0112-统计安全快照超时误报失败.md) |
| BUG-0111 | AI 问答破坏上游前缀缓存 | 已修复（完整回归与真实上游命中验证） | P1 | 后端 / AI 问答 / 网关 / 账户亲和 / Prompt Cache | [问题-0111-AI问答破坏上游前缀缓存.md](问题-0111-AI问答破坏上游前缀缓存.md) |
| BUG-0110 | AI 问答断流提交状态无法权威确权 | 已修复（完整回归、真实模型、断网恢复与浏览器验证） | P1 | 前后端 / AI 问答 / 幂等 / 断流恢复 | [问题-0110-AI问答断流提交状态无法权威确权.md](问题-0110-AI问答断流提交状态无法权威确权.md) |
| BUG-0109 | AI 问答压缩与资产生命周期复查缺陷 | 已修复（完整回归、真实依赖与浏览器验证） | P1 | 前后端 / AI 问答 / 上下文 / 图片资产 / 编辑器 | [问题-0109-AI问答压缩与资产生命周期复查缺陷.md](问题-0109-AI问答压缩与资产生命周期复查缺陷.md) |
| BUG-0108 | AI 问答长会话初始停在最早消息 | 已修复（浏览器验证） | P2 | 前端 / AI 问答 / 长会话 / 虚拟列表 / 滚动 | [问题-0108-AI问答长会话初始停在最早消息.md](问题-0108-AI问答长会话初始停在最早消息.md) |
| BUG-0107 | AI 问答上下文恢复边界断链 | 已修复（Mock、repository 与真实模型验证） | P1 | 后端 / AI 问答 / 上下文 / 主动压缩 / 图片记忆 | [问题-0107-AI问答上下文恢复边界断链.md](问题-0107-AI问答上下文恢复边界断链.md) |
| BUG-0106 | AI 问答默认文本块为空导致模型历史丢字 | 已修复（repository 与压缩回归） | P1 | 后端 / AI 问答 / 内容块 / 上下文 | [问题-0106-AI问答默认文本块为空导致模型历史丢字.md](问题-0106-AI问答默认文本块为空导致模型历史丢字.md) |
| BUG-0105 | AI 问答上游响应伪有界读取 | 已修复（回归验证） | P1 | 后端 / AI 问答 / SSE / JSON / 内存边界 | [问题-0105-AI问答上游响应伪有界读取.md](问题-0105-AI问答上游响应伪有界读取.md) |
| BUG-0104 | AI 问答模型未就绪时草稿丢失 | 已修复（前端回归） | P1 | 前端 / AI 问答 / 编辑器 / 模型加载 | [问题-0104-AI问答模型未就绪时草稿丢失.md](问题-0104-AI问答模型未就绪时草稿丢失.md) |
| BUG-0103 | AI 问答置顶会话游标漏页 | 已修复（repository 与前端回归） | P1 | 前后端 / AI 问答 / 会话列表 / 游标 | [问题-0103-AI问答置顶会话游标漏页.md](问题-0103-AI问答置顶会话游标漏页.md) |
| BUG-0102 | AI 问答沉浸页移动端失去系统入口 | 已修复（浏览器验证） | P1 | 前端 / AI 问答 / 移动端 / 导航 | [问题-0102-AI问答沉浸页移动端失去系统入口.md](问题-0102-AI问答沉浸页移动端失去系统入口.md) |
| BUG-0101 | AI 问答旧流误删新流停止句柄 | 已修复（回归验证） | P1 | 后端 / AI 问答 / SSE / 并发 | [问题-0101-AI问答旧流误删新流停止句柄.md](问题-0101-AI问答旧流误删新流停止句柄.md) |
| BUG-0100 | AI 问答 PostgreSQL 跨会话配额竞态 | 已修复（真实 PostgreSQL 并发验证） | P1 | 后端 / AI 问答 / PostgreSQL / 配额 | [问题-0100-AI问答PostgreSQL跨会话配额竞态.md](问题-0100-AI问答PostgreSQL跨会话配额竞态.md) |
| BUG-0099 | AI 问答内部网关 trace 断链 | 已修复（Mock 验证） | P1 | 后端 / AI 问答 / 网关 / trace | [问题-0099-AI问答内部网关trace断链.md](问题-0099-AI问答内部网关trace断链.md) |
| BUG-0098 | AI 问答图片数量与请求体边界失配 | 已修复（HTTP 与前端回归） | P1 | 前后端 / AI 问答 / 图片 / 请求体安全 | [问题-0098-AI问答图片数量与请求体边界失配.md](问题-0098-AI问答图片数量与请求体边界失配.md) |
| BUG-0097 | AI 问答会话切换被旧响应覆盖 | 已修复（回归验证） | P1 | 前端 / AI 问答 / 异步竞态 | [问题-0097-AI问答会话切换被旧响应覆盖.md](问题-0097-AI问答会话切换被旧响应覆盖.md) |
| BUG-0096 | AI 问答 Responses 截断流误记完成 | 已修复（回归验证） | P1 | 后端 / AI 问答 / Responses / SSE | [问题-0096-AI问答Responses截断流误记完成.md](问题-0096-AI问答Responses截断流误记完成.md) |
| BUG-0095 | AI 问答 PostgreSQL 消息游标越界 | 已修复（PostgreSQL 回归） | P1 | 后端 / AI 问答 / PostgreSQL / 游标分页 | [问题-0095-AI问答PostgreSQL消息游标越界.md](问题-0095-AI问答PostgreSQL消息游标越界.md) |
| BUG-0094 | AI 问答初始化失败遗留活动轮次 | 已修复（回归验证） | P1 | 后端 / AI 问答 / 事务 / SSE | [问题-0094-AI问答初始化失败遗留活动轮次.md](问题-0094-AI问答初始化失败遗留活动轮次.md) |
| BUG-0093 | AI 问答复制只依赖 Clipboard API | 已修复（开发验证） | P2 | 前端 / AI 问答 / 剪贴板 | [问题-0093-AI问答复制只依赖ClipboardAPI.md](问题-0093-AI问答复制只依赖ClipboardAPI.md) |
| BUG-0092 | AI 问答编辑原文未显示 | 已修复（真实模型验证） | P1 | 前端 / AI 问答 / Tiptap / UndoRedo | [问题-0092-AI问答编辑原文未显示.md](问题-0092-AI问答编辑原文未显示.md) |
| BUG-0091 | AI 问答协议能力候选误判 | 已修复（Mock 与浏览器验证） | P1 | 后端 / AI 问答 / 网关 / 协议能力 | [问题-0091-AI问答协议能力候选误判.md](问题-0091-AI问答协议能力候选误判.md) |
| BUG-0090 | AI 问答原生图片绕过 HTTPS 限制 | 已修复（开发验证） | P2 | 前端 / AI 问答 / Markdown / 安全 | [问题-0090-AI问答原生图片绕过HTTPS限制.md](问题-0090-AI问答原生图片绕过HTTPS限制.md) |
| BUG-0089 | Codex 兼容头版本落后 | 已修复（待生产验证） | P1 | 网关 / Codex Responses / OpenAI OAuth | [问题-0089-Codex兼容头版本落后.md](问题-0089-Codex兼容头版本落后.md) |
| BUG-0088 | 模型目录配置字段与视图不完整 | 已修复（待生产验证） | P1 | 模型目录 / 前端 / Node / Go / PostgreSQL / SQLite | [问题-0088-模型目录配置字段与视图不完整.md](问题-0088-模型目录配置字段与视图不完整.md) |
| BUG-0087 | watchdog 局部故障扩大为整组重启 | 已修复（待生产验证） | P0 | 部署 / watchdog / DB service / supervisor / 生产稳定性 | [问题-0087-watchdog局部故障扩大为整组重启.md](问题-0087-watchdog局部故障扩大为整组重启.md) |
| BUG-0086 | 账户配置变更被旧成功信号延后检查 | 已修复（待生产验证） | P1 | AI 账户 / 健康检查 / PostgreSQL / SQLite / Go 公开账户 | [问题-0086-账户配置变更被旧成功信号延后检查.md](问题-0086-账户配置变更被旧成功信号延后检查.md) |
| BUG-0085 | AI 账户批量编辑分组与文案漂移 | 已修复（待生产验证） | P2 | 前端 / AI 账户 / 批量编辑 / 文档 | [问题-0085-AI账户批量编辑分组与文案漂移.md](问题-0085-AI账户批量编辑分组与文案漂移.md) |
| BUG-0084 | GPT cyber_policy 作用域误限于 Codex | 已修复（待生产验证） | P1 | GPT / 响应检查 / 客户端画像 / 网关 | [问题-0084-GPT-cyber-policy作用域误限于Codex.md](问题-0084-GPT-cyber-policy作用域误限于Codex.md) |
| BUG-0083 | GPT 覆盖与 Bridge 字段保护偏离 | 已修复（待生产验证） | P1 | GPT / 账户覆盖 / 模型目录 / 协议 bridge / 网关 | [问题-0083-GPT覆盖与Bridge字段保护偏离.md](问题-0083-GPT覆盖与Bridge字段保护偏离.md) |
| BUG-0082 | 运维时间与队列状态语义混用 | 已修复（待生产验证） | P2 | 前端 / AI 账户 / 使用记录 / 后台任务 / 队列 | [问题-0082-运维时间与队列状态语义混用.md](问题-0082-运维时间与队列状态语义混用.md) |
| BUG-0081 | 账户检查成功证据与协议组合缺口 | 已修复（待生产验证） | P1 | AI 账户 / 健康检查 / 人工测试 / 协议 | [问题-0081-账户检查成功证据与协议组合缺口.md](问题-0081-账户检查成功证据与协议组合缺口.md) |
| BUG-0080 | 冷却复测并发与多协议候选缺口 | 已修复（待生产验证） | P1 | AI 账户 / PostgreSQL / SQLite / ops-worker | [问题-0080-冷却复测并发与多协议候选缺口.md](问题-0080-冷却复测并发与多协议候选缺口.md) |
| BUG-0079 | 审计配置与大正文边界偏离 | 已修复（待生产验证） | P1 | 网关 / 原始审计 / 配置 / worker / 存储 | [问题-0079-审计配置与大正文边界偏离.md](问题-0079-审计配置与大正文边界偏离.md) |
| BUG-0078 | 临时维护任务状态与过期租约未回收 | 已修复（待生产验证） | P2 | 后端 / stats-worker / 临时维护任务 / PostgreSQL / SQLite | [问题-0078-临时维护任务状态与过期租约未回收.md](问题-0078-临时维护任务状态与过期租约未回收.md) |
| BUG-0077 | 模型配对窗口目标模型互相覆盖 | 已修复（待生产验证） | P2 | 后端 / 模型检测 / stats-worker / paired window | [问题-0077-模型配对窗口目标模型互相覆盖.md](问题-0077-模型配对窗口目标模型互相覆盖.md) |
| BUG-0076 | 模型身份来源更新未刷新群体 peer | 已修复（待生产验证） | P1 | 后端 / 模型检测 / stats-worker / LOO 基线 | [问题-0076-模型身份来源更新未刷新群体peer.md](问题-0076-模型身份来源更新未刷新群体peer.md) |
| BUG-0075 | 余额快照清理失败后旧值回显 | 已发布并验证 | P1 | 后端 / AI 账户 / 多 Key / 余额快照 / stats-writer | [问题-0075-余额快照清理失败后旧值回显.md](问题-0075-余额快照清理失败后旧值回显.md) |
| BUG-0074 | 运行日志 Redis 入队失败终止主进程 | 已修复（待生产验证） | P1 | 后端 / 运行日志 / Redis Stream / 进程稳定性 | [问题-0074-运行日志Redis入队失败终止主进程.md](问题-0074-运行日志Redis入队失败终止主进程.md) |
| BUG-0073 | 多 Key 账户被余额查询阻断 | 已发布并验证 | P1 | 前端 / 后端 / AI 账户 / 多 Key / 余额查询 | [问题-0073-多Key账户被余额查询阻断.md](问题-0073-多Key账户被余额查询阻断.md) |
| BUG-0072 | 网关客户端时延与审计主线程阻塞 | 已发布并验证 | P1 | 后端 / 网关 / 原始审计 / 使用记录 / 部署 | [问题-0072-网关客户端时延与审计主线程阻塞.md](问题-0072-网关客户端时延与审计主线程阻塞.md) |
| BUG-0071 | GPT 覆盖字段运行时丢失与使用记录计价事实缺口 | 已修复（待生产数据库同步与页面验证） | P1 | 后端 / 网关 / 使用记录 / PostgreSQL / SQLite / 前端 | [问题-0071-GPT覆盖字段运行时丢失与使用记录计价事实缺口.md](问题-0071-GPT覆盖字段运行时丢失与使用记录计价事实缺口.md) |
| BUG-0070 | Codex 跨协议上下文污染 | 已修复（生产已发布，专项自动化已验证） | P1 | 后端 / 网关 / Codex Responses / 协议转换 / 上下文压缩 | [问题-0070-Codex跨协议上下文污染.md](问题-0070-Codex跨协议上下文污染.md) |
| BUG-0069 | 余额临时失败重试等待过长 | 已修复（生产已验证） | P1 | 后端 / AI 账户 / 余额查询 / ops-worker | [问题-0069-余额临时失败重试等待过长.md](问题-0069-余额临时失败重试等待过长.md) |
| BUG-0068 | 记录维护回归夹具缺少账户检查模型 | 已修复 | P3 | 测试 / 数据维护 / AI 账户 schema | [问题-0068-记录维护回归夹具缺少账户检查模型.md](问题-0068-记录维护回归夹具缺少账户检查模型.md) |
| BUG-0067 | 表监控展示不存在的 PostgreSQL 归档库 | 已修复（待生产验证） | P2 | PostgreSQL / 使用记录保留 / 表监控 / 前端 | [问题-0067-表监控展示不存在的PostgreSQL归档库.md](问题-0067-表监控展示不存在的PostgreSQL归档库.md) |
| BUG-0066 | NewAPI 令牌不限额误判为账户余额无限 | 已修复（待生产验证） | P1 | 后端 / 前端 / AI 账户 / 余额适配器 / 后台调度 | [问题-0066-NewAPI令牌不限额误判为账户余额无限.md](问题-0066-NewAPI令牌不限额误判为账户余额无限.md) |
| BUG-0065 | 普通发布无条件重建非业务数据库 | 已修复（生产已验证） | P1 | 部署 / PostgreSQL / Redis / 数据保留 | [问题-0065-普通发布无条件重建非业务数据库.md](问题-0065-普通发布无条件重建非业务数据库.md) |
| BUG-0064 | 发布验证过早检查 worker 拓扑 | 已修复（生产已验证） | P1 | 部署 / supervisor / worker / 自动回滚 | [问题-0064-发布验证过早检查worker拓扑.md](问题-0064-发布验证过早检查worker拓扑.md) |
| BUG-0063 | 弹窗余额验证意外保存配置和快照 | 已修复（生产已发布） | P1 | 前端 / 后端 / AI 账户 / 余额查询 / API 契约 | [问题-0063-弹窗余额验证意外保存配置和快照.md](问题-0063-弹窗余额验证意外保存配置和快照.md) |
| BUG-0062 | 余额刷新后列表金额不立即更新 | 已修复（生产已发布） | P2 | 前端 / Vue / AI 账户 / 余额查询 | [问题-0062-余额刷新后列表金额不立即更新.md](问题-0062-余额刷新后列表金额不立即更新.md) |
| BUG-0061 | 临时发布验证子进程被 watchdog 误杀 | 已修复（生产已验证） | P1 | 部署 / watchdog / worker / DB service | [问题-0061-临时发布验证子进程被watchdog误杀.md](问题-0061-临时发布验证子进程被watchdog误杀.md) |
| BUG-0060 | Codex 压缩响应被大小限制拦截 | 已修复（生产已验证） | P1 | 后端 / 网关 / Codex Responses / SSE / 响应检查 | [问题-0060-Codex压缩响应被大小限制拦截.md](问题-0060-Codex压缩响应被大小限制拦截.md) |
| BUG-0059 | PostgreSQL 健康检查结果无法写回 | 已修复（生产已验证） | P1 | 后端 / worker / AI 账户 / PostgreSQL / 健康检查 | [问题-0059-PostgreSQL健康检查结果无法写回.md](问题-0059-PostgreSQL健康检查结果无法写回.md) |
| BUG-0058 | 健康激活未刷新分组统计 | 已修复（待真实 PostgreSQL 验证） | P1 | 后端 / Node / AI 账户 / 健康检查 / 分组统计 | [问题-0058-健康激活未刷新分组统计.md](问题-0058-健康激活未刷新分组统计.md) |
| BUG-0057 | Go 全局自定义模型权限错误优先级 | 已修复 | P2 | 后端 / Go / 供应商 / 自定义模型 / 权限 | [问题-0057-Go全局自定义模型权限错误优先级.md](问题-0057-Go全局自定义模型权限错误优先级.md) |
| BUG-0056 | 管理员遗留个人检查模型偏好 | 已修复（待真实 PostgreSQL 验证） | P1 | 后端 / Node / AI 账户 / 供应商 / 健康检查模型 | [问题-0056-管理员遗留个人检查模型偏好.md](问题-0056-管理员遗留个人检查模型偏好.md) |
| BUG-0055 | 自定义模型请求能力范围漂移 | 已修复 | P1 | 后端 / Node / 供应商 / 自定义模型 / API 契约 | [问题-0055-自定义模型请求能力范围漂移.md](问题-0055-自定义模型请求能力范围漂移.md) |
| BUG-0054 | Node 模型选项遗漏请求能力 | 已修复 | P1 | 后端 / Node / 供应商 / 模型目录 / API 契约 / 前端账户配置 | [问题-0054-Node模型选项遗漏请求能力.md](问题-0054-Node模型选项遗漏请求能力.md) |
| BUG-0053 | Go 分组列表并发快照误报可用 | 已修复（待真实环境验证） | P2 | 后端 / Go / 管理接口 / 分组 / Redis / 运行态 | [问题-0053-Go分组列表并发快照误报可用.md](问题-0053-Go分组列表并发快照误报可用.md) |
| BUG-0052 | Go Redis namespace 默认值偏离 Node | 已修复（待真实环境验证） | P1 | 后端 / Go / Redis / 配置 / 管理接口 / 运行态 | [问题-0052-GoRedisNamespace默认值偏离Node.md](问题-0052-GoRedisNamespace默认值偏离Node.md) |
| BUG-0051 | Go 账户标签更新未刷新修改时间 | 已修复（待真实环境验证） | P2 | 后端 / Go / 管理接口 / AI 账户 / 标签 / PostgreSQL | [问题-0051-Go账户标签更新未刷新修改时间.md](问题-0051-Go账户标签更新未刷新修改时间.md) |
| BUG-0050 | Go 公开账户字段出现误判连接变更 | 已修复（待真实环境验证） | P1 | 后端 / Go / 公开接口 / AI 账户 / 健康检查 / PostgreSQL | [问题-0050-Go公开账户字段出现误判连接变更.md](问题-0050-Go公开账户字段出现误判连接变更.md) |
| BUG-0049 | Go 管理 API 缺少 Redis cache 仍启动 | 已修复（待真实环境验证） | P1 | 后端 / Go / 管理接口 / Redis cache / 网关缓存失效 / 部署 | [问题-0049-Go管理API缺少RedisCache仍启动.md](问题-0049-Go管理API缺少RedisCache仍启动.md) |
| BUG-0048 | Go 健康检查模型契约漂移 | 已修复（待真实环境验证） | P1 | 后端 / Go / 供应商 / 公开接口 / AI 账户 / PostgreSQL | [问题-0048-Go健康检查模型契约漂移.md](问题-0048-Go健康检查模型契约漂移.md) |
| BUG-0047 | Go 管理设置 JSON 解析顺序偏离 Node | 已修复 | P2 | 后端 / Go / System API / 鉴权 / 限流 / 请求体解析 | [问题-0047-Go管理设置JSON解析顺序偏离Node.md](问题-0047-Go管理设置JSON解析顺序偏离Node.md) |
| BUG-0046 | Go 管理设置请求体上限偏离 Node | 已修复 | P2 | 后端 / Go / 管理接口 / 请求体校验 | [问题-0046-Go管理设置请求体上限偏离Node.md](问题-0046-Go管理设置请求体上限偏离Node.md) |
| BUG-0045 | 服务端与客户端重试预算叠加 | 已修复 | P1 | 后端 / 网关 / 账号调度 / 客户端画像 / 部署 | [问题-0045-服务端与客户端重试预算叠加.md](问题-0045-服务端与客户端重试预算叠加.md) |
| BUG-0044 | Go 管理操作日志未统一执行 Node 清洗规则 | 已修复（待真实环境验证） | P1 | 后端 / Go / 管理接口 / 操作日志 / Asynq | [问题-0044-Go管理操作日志未统一执行Node清洗规则.md](问题-0044-Go管理操作日志未统一执行Node清洗规则.md) |
| BUG-0043 | Go 公开账户支持模型更新未清理默认测试模型 | 已修复（待真实环境验证） | P1 | 后端 / Go / 公开接口 / AI 账户 / 默认测试模型 / PostgreSQL | [问题-0043-Go公开账户支持模型更新未清理默认测试模型.md](问题-0043-Go公开账户支持模型更新未清理默认测试模型.md) |
| BUG-0042 | Go 供应商默认测试模型覆盖协议档案 | 已修复（待真实环境验证） | P1 | 后端 / Go / 供应商 / 默认测试模型 / 协议档案 / API 契约 | [问题-0042-Go供应商默认测试模型覆盖协议档案.md](问题-0042-Go供应商默认测试模型覆盖协议档案.md) |
| BUG-0041 | Go 团队与授权写后缓存失效误报失败 | 已修复（待真实环境验证） | P1 | 后端 / Go / 团队 / 统一授权 / Redis runtime state / 网关缓存 | [问题-0041-Go团队与授权写后缓存失效误报失败.md](问题-0041-Go团队与授权写后缓存失效误报失败.md) |
| BUG-0040 | Go 公开 API Key 写入未触发网关缓存失效 | 已修复（待真实环境验证） | P1 | 后端 / Go / 公开接口 / API Key / Redis cache / Redis runtime state / 网关额度 | [问题-0040-Go公开APIKey写入未触发网关缓存失效.md](问题-0040-Go公开APIKey写入未触发网关缓存失效.md) |
| BUG-0039 | Go 额度快照生成时间早于构建完成 | 已修复（待真实环境验证） | P2 | 后端 / Go / worker / 网关 / 授权额度 / Redis runtime state | [问题-0039-Go额度快照生成时间早于构建完成.md](问题-0039-Go额度快照生成时间早于构建完成.md) |
| BUG-0038 | Go 系统 API 限流未共享 Node Redis 窗口 | 已修复（待真实环境验证） | P1 | 后端 / Go / System API / Redis / 限流 / 灰度切流 | [问题-0038-Go系统API限流未共享NodeRedis窗口.md](问题-0038-Go系统API限流未共享NodeRedis窗口.md) |
| BUG-0037 | Go 自定义模型缓存失效错误误报写入失败 | 已修复（待真实环境验证） | P1 | 后端 / Go / 管理接口 / 自定义模型 / Redis / 网关缓存 | [问题-0037-Go自定义模型缓存失效错误误报写入失败.md](问题-0037-Go自定义模型缓存失效错误误报写入失败.md) |
| BUG-0036 | Go 公开账户模型目录校验缺失 | 已修复（待真实环境验证） | P2 | 后端 / Go / 公开接口 / AI 账户 / 模型目录 / PostgreSQL | [问题-0036-Go公开账户模型目录校验缺失.md](问题-0036-Go公开账户模型目录校验缺失.md) |
| BUG-0035 | Go 公开账户默认模型语义漂移 | 已修复（待真实环境验证） | P1 | 后端 / Go / 公开接口 / AI 账户 / PostgreSQL / 网关 | [问题-0035-Go公开账户默认模型语义漂移.md](问题-0035-Go公开账户默认模型语义漂移.md) |
| BUG-0034 | 审计保留清理单批删除长尾 | 已修复（待上线） | P2 | 后端 / worker / 存储 / 审计 | [问题-0034-审计保留清理单批删除长尾.md](问题-0034-审计保留清理单批删除长尾.md) |
| BUG-0033 | 管理端读接口生产延迟 | 已修复 | P1 | 前端 / 后端 / 存储 / 部署 / DNS | [问题-0033-管理端读接口生产延迟.md](问题-0033-管理端读接口生产延迟.md) |
| BUG-0032 | compact 摘要子请求被误改写为流式 | 已修复 | P1 | 后端 / 网关 / Codex Responses / DeepSeek bridge / 模型映射 | [问题-0032-compact摘要子请求被误改写为流式.md](问题-0032-compact摘要子请求被误改写为流式.md) |
| BUG-0031 | 响应检查 errorType 策略未切号 | 已修复 | P1 | 后端 / 网关 / 响应检查 / 账号调度 | [问题-0031-响应检查errorType策略未切号.md](问题-0031-响应检查errorType策略未切号.md) |
| BUG-0030 | 流式心跳无有效输出未切号 | 已修复 | P1 | 后端 / 网关 / 流式转发 / 账号调度 / SSE 协议适配 | [问题-0030-流式心跳无有效输出未切号.md](问题-0030-流式心跳无有效输出未切号.md) |
| BUG-0029 | 高性能账号并发读数长期为零 | 已修复 | P1 | 前端 / 后端 / 网关 / Redis 运行态 | [问题-0029-高性能账号并发读数长期为零.md](问题-0029-高性能账号并发读数长期为零.md) |
| BUG-0028 | 响应检查无切号无限重试 | 已修复 | P1 | 后端 / 网关 / 响应检查 / 调度 | [问题-0028-响应检查无切号无限重试.md](问题-0028-响应检查无切号无限重试.md) |
| BUG-0027 | 运行日志成功链路刷屏 | 已修复 | P2 | 后端 / 网关 / 运行日志 / SQLite | [问题-0027-运行日志成功链路刷屏.md](问题-0027-运行日志成功链路刷屏.md) |
| BUG-0026 | 统计聚合被用量队列锁失败阻塞 | 已修复 | P1 | 后端 / 存储 / worker / SQLite / 统计 | [问题-0026-统计聚合被用量队列锁失败阻塞.md](问题-0026-统计聚合被用量队列锁失败阻塞.md) |
| BUG-0025 | API Key 时间计划提前启用无效 | 已修复 | P1 | 前端 / 后端 / 存储 / 网关 / API Key | [问题-0025-APIKey时间计划提前启用无效.md](问题-0025-APIKey时间计划提前启用无效.md) |
| BUG-0024 | API Key 时间计划错过边界未停用 | 已修复 | P1 | 后端 / 网关 / worker / API Key | [问题-0024-APIKey时间计划错过边界未停用.md](问题-0024-APIKey时间计划错过边界未停用.md) |
| BUG-0023 | 分散失败未升级临时不可调用 | 已修复 | P1 | 后端 / 网关 / worker / 账号质量 | [问题-0023-分散失败未升级临时不可调用.md](问题-0023-分散失败未升级临时不可调用.md) |
| BUG-0022 | 审计详情有 hash 无正文 | 已修复 | P1 | 前端 / 后端 / 存储 / 网关 / 部署 | [问题-0022-审计详情有hash无正文.md](问题-0022-审计详情有hash无正文.md) |
| BUG-0021 | 分组保存协议档案字段被路由拒绝 | 已修复 | P1 | 前端 / 后端 / 分组管理 / 部署 | [问题-0021-分组保存协议档案字段被路由拒绝.md](问题-0021-分组保存协议档案字段被路由拒绝.md) |
| BUG-0020 | 网关上游错误未写账号状态 | 已修复 | P1 | 后端 / 网关 / 账号错误策略 / worker | [问题-0020-网关余额不足未写账号状态.md](问题-0020-网关余额不足未写账号状态.md) |
| BUG-0019 | 授权配额快照跨进程失效缺失 | 已修复 | P1 | 后端 / 网关 / DB service / 授权配额 | [问题-0019-授权配额快照跨进程失效缺失.md](问题-0019-授权配额快照跨进程失效缺失.md) |
| BUG-0018 | 已删除记录后台清理跨库锁冲突 | 已修复 | P1 | 后端 / 存储 / worker / SQLite | [问题-0018-已删除记录后台清理跨库锁冲突.md](问题-0018-已删除记录后台清理跨库锁冲突.md) |
| BUG-0017 | 分组统计脏标记写入统计库锁冲突 | 已修复 | P1 | 后端 / 存储 / worker / SQLite | [问题-0017-分组统计脏标记写入统计库锁冲突.md](问题-0017-分组统计脏标记写入统计库锁冲突.md) |
| BUG-0016 | 授权恢复运行态仍暂停 | 已修复 | P1 | 后端 / 存储 / 授权 / 网关调度 | [问题-0016-授权恢复运行态仍暂停.md](问题-0016-授权恢复运行态仍暂停.md) |
| BUG-0015 | 账户测试状态码白名单漏标失败 | 已修复 | P1 | 后端 / 账号测试 / 授权账号隔离 / 网关错误策略 / 前端错误策略 | [问题-0015-账户测试状态码白名单漏标失败.md](问题-0015-账户测试状态码白名单漏标失败.md) |
| BUG-0014 | 图像流大 SSE 事件解析误跳过 | 已修复 | P1 | 后端 / 网关 / 流式转发 / 使用记录 | [问题-0014-图像流大SSE事件解析误跳过.md](问题-0014-图像流大SSE事件解析误跳过.md) |
| BUG-0013 | 高并发下后台复测与记录链路放大 | 部分修复，持续监控 | P1 | 后端 / 网关 / worker / 审计 / 运行日志 / 使用记录 | [问题-0013-高并发下后台复测与记录链路放大.md](问题-0013-高并发下后台复测与记录链路放大.md) |
| BUG-0012 | 流式完成后客户端关闭误记取消 | 已修复 | P1 | 后端 / 网关 / 流式转发 / 审计与使用记录 | [问题-0012-流式完成后客户端关闭误记取消.md](问题-0012-流式完成后客户端关闭误记取消.md) |
| BUG-0011 | 非 LTS 内置 SQLite 能力不完整 | 已修复 | P2 | 后端 / 脚本 / 文档 / 部署 | [问题-0011-非LTS内置SQLite能力不完整.md](问题-0011-非LTS内置SQLite能力不完整.md) |
| BUG-0010 | 流式失败过早屏蔽账号 | 已修复 | P1 | 后端 / 网关 / 账号调度 / 流式转发 | [问题-0010-流式失败过早屏蔽账号.md](问题-0010-流式失败过早屏蔽账号.md) |
| BUG-0009 | 菜单切换短暂卡顿 | 待验证 | P2 | 前端 / 菜单 / 统计 / 表格 / 图表 | [问题-0009-菜单切换短暂卡顿.md](问题-0009-菜单切换短暂卡顿.md) |
| BUG-0008 | Node 23 启动缺少 node:sqlite 提示 | 已修复 | P2 | 后端 / 脚本 / 文档 | [问题-0008-Node23启动缺少node-sqlite提示.md](问题-0008-Node23启动缺少node-sqlite提示.md) |
| BUG-0007 | 分库后分组统计错库 | 已修复 | P1 | 后端 / 存储 / 统计 | [问题-0007-分库后分组统计错库.md](问题-0007-分库后分组统计错库.md) |
| BUG-0006 | Mac 整库在线备份卡住 | 已修复 | P1 | 部署 / 运维 / SQLite | [问题-0006-Mac整库在线备份卡住.md](问题-0006-Mac整库在线备份卡住.md) |
| BUG-0005 | 账号质量刷新字段名错误 | 已修复 | P1 | 后端 / 存储 / worker | [问题-0005-账号质量刷新字段名错误.md](问题-0005-账号质量刷新字段名错误.md) |
| BUG-0004 | macOS 内存监控口径误报 | 已修复 | P2 | 后端 / 存储 / 文档 | [问题-0004-macOS内存监控口径误报.md](问题-0004-macOS内存监控口径误报.md) |
| BUG-0003 | 管理面并发请求触发 SQLite 写锁 | 已修复 | P1 | 后端 / 存储 / 文档 | [问题-0003-管理面并发请求触发SQLite写锁.md](问题-0003-管理面并发请求触发SQLite写锁.md) |
| BUG-0002 | 统计口径待复查缺陷 | 部分修复，待端到端验证 | P1 | 前端 / 后端 / 存储 / 网关 / 文档 | [问题-0002-统计口径待复查缺陷.md](问题-0002-统计口径待复查缺陷.md) |
| BUG-0001 | 授权管理待复查缺陷 | 待验证 | P1 | 前端 / 后端 / 存储 / 网关 / 文档 | [问题-0001-授权管理待复查缺陷.md](问题-0001-授权管理待复查缺陷.md) |
