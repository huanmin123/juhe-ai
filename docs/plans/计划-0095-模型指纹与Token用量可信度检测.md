# 模型指纹与 Token 用量可信度检测计划

## 基本信息

- 编号：PLAN-0095
- 状态：进行中（Phase 0、输入 Token 差分、增量结果和身份群体基线闭环已完成；固定 intercept cohort、全类别结构化特征、profile 驱动长上下文和真实样本校准待完成）
- 创建时间：2026-07-13
- 需求来源：当前 Codex 会话；用户要求重点提高 GPT-5.6 真伪检测准确率，并确认 Token 灌水检测现状
- 执行者：Codex；方案与阈值由用户复核后进入实现
- 关联模块：模型检测 / AI 账户 / 网关协议 / usage / ingest-worker / stats-worker / 统计结果 / 前端
- 数据库变更：预计需要当前 schema 的 observation 事实表和基线窗口表；不做旧结构兼容或运行时迁移

## 需求目标

- 提高发现 `gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna`、`gpt-5.5` 和 `gpt-5.4` 未声明替换的概率。
- 在没有官方直连对照账户时，通过独立上游桶群体基线和同账号跨模型塌缩检测形成可解释的统计证据。
- 使用差分 Token 探针检测比例灌水、固定灌水和分桶取整异常。
- 把模型身份、显式映射、Token 诚信、协议一致性和证据充分度拆开输出。
- 保持探针有界、脱敏、可版本化、可复算，不采集普通用户请求正文作为训练数据。

## 范围边界

本期包含：

- GPT / OpenAI-compatible Responses 首个可比 cohort。
- GPT-5.6 三个子版本和 GPT-5.5 / GPT-5.4 配对。
- 受控行为 observation、生成式 canary、动态长上下文阶梯和跨模型相似度。
- 精确 tokenizer、输入 Token 差分斜率、固定 intercept 和分桶异常。
- 数据集事实、stats-worker 基线窗口、账号最新可信结果和前端分项报告。
- mock 注入、统计校准、测试环境和生产小流量验证。

本期不包含：

- 不承诺 100% 证明物理模型身份。
- 不依赖 Pro、Max、Ultra、service tier、思考档位、多智能体或其他账号权益能力。
- 不使用普通用户请求正文训练模型身份分类器。
- 不自动修改上游 usage、成本、额度或账单，不自动处罚疑似账号。
- 不把 OAuth / Codex、Chat bridge、Anthropic bridge 与 API Key Responses cohort 混合建模；这些链路先保持协议检测和独立证据不足状态。
- 不引入重型外部 eval 平台、向量数据库、分布式训练服务或第三方模型检测网站。

## 关联文档

- `docs/architecture/架构总览.md`
- `docs/functions/模型检测设计.md`
- `docs/functions/模型价格与用量统计口径.md`
- `docs/functions/统计指标与分层聚合设计.md`
- `docs/functions/统计数据集与结果库拆分设计.md`
- `docs/functions/SQLite存储说明.md`
- `docs/functions/接口契约与权限矩阵.md`
- `docs/plans/计划-0025-模型检测与可信度诊断.md`
- `docs/plans/计划-0072-多供应商模型检测扩展.md`
- `docs/reports/GPT-5.6模型检测现状与样本审计报告-2026-07-13.md`

## 决策记录

### 2026-07-13：不用账号权益能力做模型指纹

Pro / Max / Ultra、思考档位、多智能体和 service tier 由账号套餐、组织权限、客户端和灰度共同决定。缺少这些能力不能证明模型被替换，因此它们只能作为干扰变量或诊断信息，不能进入身份硬判定。

### 2026-07-13：没有官方对照时使用群体异常检测

不训练没有真标签的真假二分类器。只使用受控探针 observation，按独立上游桶限权，构建 leave-one-upstream-out 稳健分布；样本不足时明确输出证据不足。

### 2026-07-13：Token 灌水使用差分斜率

不把单次本地估算与上游 usage 的差值直接视为灌水。使用 P0 / P1 / P2 精确填充、随机顺序和多轮线性拟合，分别识别比例 slope、固定 intercept 和分桶阶梯。

### 2026-07-13：诊断不直接改账

模型身份和 Token 诚信结果先用于告警、筛选和排障。没有完成真实样本校准、账单对照和误报评估前，不自动改写 usage、成本或账户状态。

## 执行拆解

### Task 0：文档与现状基线

- [x] 审计当前模型检测 profile、行为题、usage 检查、模型映射和结果分级。
- [x] 只读检查线上 macOS 部署资料、模型检测历史、用量聚合和映射事实。
- [x] 记录当前回归通过 / 失败状态及 `pending_test` fixture 根因。
- [x] 更新模型检测长期设计、架构边界、用量口径和文档索引。
- [x] 落盘生产样本与当前能力审计报告。
- [ ] 用户复核设计、结果枚举、群体基线门槛和 Token 初始阈值。

### Task 1：恢复现有回归门禁

- [x] 按 2026-07-13 官方模型页同步 GPT-5.6 context / max input / max output 目录事实和对应回归，移除旧 `372000` 运行口径。
- [x] 按当前账户状态规则修改共享 mock fixture，通过正式健康检查成功路径获得可调度账号。
- [x] 重跑现有完整 profile、严格模型匹配、paired mismatch、分布相似度和长上下文回归。
- [x] 确认测试确实请求 mock 上游，而不是在账号选择阶段提前退出。
- [x] 固化当前报告 JSON 和评分行为，作为增强前回归基线。

### Task 2：契约与存储设计

- [x] 定义 `identityStatus`、`mappingStatus`、`usageIntegrityStatus`、`protocolStatus` 和 `evidenceStatus` 契约，并在现有报告 JSON 中形成最小闭环。
- [x] 定义 `model_check_observations` 当前 schema、有界字段、索引和保留期。
- [x] 定义并实现身份来源特征 / 版本基线、模型配对、Token 诚信和账号最新结果四类统计窗口。
- [x] 定义上游 origin、probe key 和 `system_fingerprint` 的 HMAC 规则；当前随系统 secret 轮换，不保存 HMAC 输入。
- [x] 同步 SQLite / PostgreSQL 当前 schema、类型、repository、接口和前端 DTO；不增加旧结构兼容分支。

### Task 3：Token 用量可信度

- [x] 评估并选择支持目标模型 encoding、可版本锁定的精确 tokenizer 实现：`js-tiktoken@1.0.21:o200k_base`。
- [x] 实现 P0 / P1 / P2 精确填充块和实际 outbound 受控请求 Token 复核。
- [x] 精确填充限制为 2048 Token，使用线性构造并投递到有界单 worker 任务池，避免阻塞服务事件循环。
- [x] 实现三轮交错顺序、reported / local 差分、slope / intercept 和 95% 置信区间。
- [ ] 比例和分桶异常原因码已实现；固定 intercept 已保存，但固定灌水强判等待 cohort 固定开销基线。
- [x] usage 缺失或总输入口径不兼容返回 `unsupported`；当前不做 output 灌水结论，因此不会把隐藏 reasoning 误判为灌水。
- [x] 保持 usage 事实和计费逻辑不变，只保存诊断 observation 和窗口结果。

### Task 4：GPT-5.6 指纹 observation

- [x] 保留公开行为类别，并把版本化运行时生成式 canary 独立到身份 observation 模块；题面和回答正文均不落库。
- [ ] 为约束、代码、推理、错误恢复、多语言、工具 schema 和知识时间窗输出结构化 feature。
- [ ] 把长上下文从固定三档改为 profile 驱动的低 / 中 / 高 / 可选极限阶梯。
- [x] 随机交错执行 Sol / Terra / Luna 和 5.5 / 5.4 paired probes。
- [x] 采集 HMAC 后的 `system_fingerprint` 辅助信号，但不把它作为硬身份凭据。
- [x] 所有当前 Token observation 通过 dataset-writer / ingest-worker 写入，检测 API 不同步聚合。

### Task 5：群体基线与账号结论

- [x] stats-worker 按同一确认游标增量构建 usage 诚信、身份来源特征、版本基线、paired 相似度和账号 latest。
- [x] 按上游桶限制贡献权重，候选账号使用 leave-one-upstream-out median / MAD / q10 / q90；bootstrap 重采样区间保留为真实样本校准增强，不阻塞当前稳健基线。
- [x] 按来源桶、样本数和时间跨度输出 bootstrap / candidate / stable，并实现 active / drift_protected / retired 基线版本切换机制。
- [x] 仅 `observed + observed_model` 有效样本进入聚合；Token 样本还要求 reported usage，失败和缺字段样本不增加轮次、覆盖率、来源数、时间跨度或证据阶段。
- [x] 实现群体共同漂移保护、MAD 极端样本退出和同一上游桶多账号塌缩限权。
- [x] 刷新 `model_account_trust_results`，详情 API 只读预聚合结果，不扫 observation 或 usage 明细。

### Task 6：评分与中文前端

- [x] 保留现有总览等级，同时展示模型身份、映射、Token 诚信、协议和证据充分度五个分项。
- [x] 详情页展示 requested / upstream / observed model、probe / tokenizer version、来源桶数、样本轮次、差分斜率 / 截距和中文原因码。
- [x] 显式映射显示“已配置模型映射”，未声明冲突显示“响应模型与请求不一致”。
- [x] 证据不足、unsupported、网络失败和统计异常使用不同中文状态，禁止统一显示“假模型”。
- [x] 前端不展示隐藏题面、明文上游 origin、HMAC 输入或敏感响应。

### Task 7：校准、发布与观察

- [x] 回归覆盖诚实 usage、5% / 10% 比例放大、固定增加、64-token 取整、missing usage 和不兼容 usage。
- [x] mock 覆盖既有显式映射，并新增未声明 Sol -> Luna、5.5 -> 5.4 行为降级和三个模型同源塌缩。
- [ ] 测试环境收集至少一个完整 baseline window，校验离线重建和在线增量一致。
- [ ] 生产先以小流量、仅诊断、不自动处置方式运行，观察成本、误报和上游限流。
- [ ] 根据真实样本校准阈值并记录版本；未达到独立来源门槛时保持证据不足。
- [x] 完成构建、类型检查和定向回归。
- [ ] 完成部署验收和发布后观察，再更新本计划状态。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 现有回归 | 协议 profile | `pnpm test:model-check-protocol-profiles` | 通过 | 已通过 | 2026-07-13 已通过 |
| 现有回归 | 完整 profile 等五条全链路 | 见审计报告命令表 | 全部执行到 mock 上游并通过 | 已通过 | 2026-07-14 共享 fixture 修复后全部到达 mock 上游并通过 |
| 模型目录 | GPT-5.6 官方上下文边界 | 模型目录与能力回归 | 三个 5.6 使用当前官方 1,050,000 context / 922,000 max input / 128,000 max output 语义 | 已通过 | 2026-07-14 模型目录与生成快照回归通过 |
| 映射 | 显式与未声明模型冲突 | 严格模型匹配回归 | 两种场景分别为 configured mapping 和硬冲突 | 已通过 | 2026-07-14 五维报告断言通过 |
| 模型指纹 | 三模型同源塌缩 | `pnpm test:model-trust-identity-baseline` | 命中 `suspected_same_source` | 已通过 | Sol / Terra / Luna 相同 paired 特征命中同源原因码 |
| 模型指纹 | 相近但正常分布 | `pnpm test:model-trust-identity-baseline` | 不因单题或局部特征相似误报 | 已通过 | 2026-07-14 增加正常 cohort 成员不得进入同源、降级或群体离群状态的明确断言 |
| Token | 诚实 usage | `pnpm test:model-check-token-integrity` | slope 置信区间包含 1 | 已通过 | slope=1，固定正常开销保留为 intercept |
| Token | 5% / 10% 比例灌水 | `pnpm test:model-check-token-integrity` | 5% 进入校准边界，10% 稳定异常 | 已通过 | 5% warning，10% suspected_padding |
| Token | 固定与分桶灌水 | `pnpm test:model-check-token-integrity` | 固定值不脱离 cohort 强判，分桶输出原因码 | 已通过 | intercept 可复算，64-token 取整 warning |
| Token | reasoning 无 breakdown | 输入差分边界 | 不形成 output 灌水误报 | 已通过 | 当前仅判 input；usage 缺失 / 不兼容为 unsupported |
| 基线 | 单上游大量账号投毒 | `pnpm test:model-trust-identity-baseline` | 同一上游桶权重受限 | 已通过 | 先按 upstream HMAC 桶塌缩再进入 LOO |
| 基线 | 群体模型升级漂移 | `pnpm test:model-trust-identity-baseline` | 创建新版本并暂停强结论；恢复、失稳或超期后不再永久优先候选 | 已通过 | `drift_protected` 期间身份降为证据不足；固定 observation 时间覆盖 `recovered` / `rejected` / `expired` |
| 基线 | 累计来源向量与硬冲突门禁 | `pnpm test:model-trust-identity-baseline` | 使用累计均值和约束通过率；模型 / 协议硬冲突不进入来源或基线 | 已通过 | latest feature 与累计均值相反的 paired 样本距离为 0；冲突只保留 dataset / latest 诊断 |
| 存储 | observation 脱敏与大小上限 | `pnpm test:model-trust-observation-aggregation` | 无题面、凭据、明文 origin 或无界 payload | 已通过 | SQLite 事实与 PG schema 转译已覆盖 |
| 统计 | 增量游标与 latest | `pnpm test:model-trust-observation-aggregation` | 游标仅在完整处理后推进，API 只读 latest | 已通过 | 4+5 分批后不重复，结果 slope=1 / intercept=10 |
| 前端 | 中文状态与证据详情 | 前端专项回归和浏览器验证 | 中文、无敏感字段、状态不混淆 | 部分完成 | 五维中文详情代码已实现；专门自动化契约和登录态浏览器验收尚未完成，不能由构建通过替代 |
| 全量 | 类型、构建、关联回归 | `pnpm typecheck`、`pnpm build`、定向回归 | 全部通过 | 已通过 | 2026-07-14 类型、构建和定向回归通过；浏览器插件当前无可用实例 |

## 进度记录

- 2026-07-13：完成当前代码、官方能力边界、线上 macOS 样本、显式映射和测试门禁审计。
- 2026-07-13：确认当前只有通用行为指纹，没有 GPT-5.6 子版本统计基线，也没有 Token padding 检测。
- 2026-07-13：确认 Pro / Max / 思考档位等账号权益能力不进入模型身份判据。
- 2026-07-13：完成长期设计、审计报告、架构 / 用量口径和计划索引同步；等待用户复核后再编写文件级实施步骤并修改代码。
- 2026-07-14：完成 Phase 0：同步 GPT-5.6 三模型目录边界，通过健康检查成功路径修复共享 fixture，恢复完整 profile 等全链路回归。
- 2026-07-14：完成五维报告最小闭环和中文详情展示。由于仓库尚无目标模型精确 tokenizer，Token 诚信与群体证据保持证据不足；observation、HMAC 上游桶、stats-worker 窗口和生产校准尚未实现。
- 2026-07-14：锁定 `js-tiktoken@1.0.21:o200k_base`，落地 P0 / P1 / P2 三轮输入差分、HMAC observation、ingest 写入、stats-worker 游标窗口、账号 latest 和中文证据覆盖。身份群体指纹、同源塌缩、LOO 稳健基线与漂移版本仍待实现。
- 2026-07-14：落地静态行为与运行时生成式 canary 的 8 维身份 observation、五模型 paired 随机交错、独立上游桶塌缩、LOO median / MAD / 分位基线、paired 同源 / 降级判断和基线漂移保护版本；生产阈值与版本切换时间窗仍需小流量观察校准。
- 2026-07-14：修正可信度证据资格：精确 Token 填充移入有界 worker 并改为线性构造；无 response model 的当前报告保持不可用；失败、usage 缺失和模型字段缺失 observation 不再放大样本、轮次、来源、覆盖率或证据阶段。
- 2026-07-14：复查修正身份来源累计语义：特征使用 `sum/sample_count`，约束使用通过率；`undeclared_mismatch` / `protocol failed` observation 只保留事实和 latest 诊断，不进入 Token / 身份来源及基线。漂移候选按 observation 时间补齐恢复、拒绝、7 天过期和 3 天稳定晋升，历史候选不再永久优先 active。
- 2026-07-14：为 cohort 独立来源 `COUNT(DISTINCT upstream_bucket_hmac)` 增加 `idx_model_trust_window_sources_cohort(cohort_key_hmac, upstream_bucket_hmac)`，SQLite 查询计划和生成 PostgreSQL DDL 同步。生产本批首次创建可信度 8 表 / 11 索引，不执行旧表清空；曾试跑旧聚合的非生产环境需停 worker 后离线重建派生结果。

## 验收标准

- 显式模型映射与未声明模型替换在接口和页面上严格区分。
- GPT-5.6 身份结论来自多探针、多轮、独立上游桶和版本化基线，不来自单题、自报身份或账号权益。
- 同账号多模型异常同质可以形成 `suspected_same_source`，但证据不足时不强判。
- input Token 比例、固定和分桶灌水都有可重复 mock 验证；隐藏 reasoning 不可拆分时不误报 output 灌水。
- API 和前端只读预聚合结果，不实时扫描模型检测明细、observation、审计或 usage shard。
- observation 有大小上限且不含隐藏题面、完整输出、明文 Base URL、凭据或用户自然流量正文。
- 完整回归、类型检查、构建、SQLite / PostgreSQL smoke、worker 增量 / 重建一致性和中文前端验证全部通过。
- 生产先诊断后处置；没有阈值校准和观察记录前不自动改账或停号。

## 验证记录

- 2026-07-13：`pnpm test:model-check-protocol-profiles` 通过。
- 2026-07-13：五条模型检测全链路回归被共享 fixture 账号状态阻塞，失败发生在探针前；详细结果见审计报告。
- 2026-07-13：线上 macOS 只读检查完成，没有改配置、写库、重启服务或追加真实模型消耗。
- 2026-07-13：8 份关联文档本地链接检查、尾随空白检查、PLAN-0095 唯一性、`git diff --check` 和仅文档变更检查通过；仅有 Git 提示未来可能按工作区配置把 LF 转为 CRLF，没有格式错误。
- 2026-07-14：`pnpm test:model-trust-identity-baseline`、Token / observation 聚合、完整 profile、严格模型匹配、paired mismatch、存储脱敏、SQLite writer、后台 registry、账户删除清理、`pnpm typecheck` 和 `pnpm build` 通过。Browser 运行时无可用实例，中文详情的 DOM / 交互 / 截图验证仍保留为部署验收项。
- 2026-07-14：可信度证据资格加固后，完整非 PG 模型检测套件、`test:deleted-account-related-cleanup`、`test:postgres-schema-sql`、`test:server-audit-shutdown`、全工作区 `pnpm typecheck` / `pnpm build` 通过；源码 `tsx` 和构建产物 `.js` 两种 Token worker 入口均验证可启动且退出后无残留。另在 `192.168.1.203` 创建一次性隔离数据库执行 `postgres:init-schema-only`、`test:model-checks-postgres-smoke` 和实际 Token source / window / round 聚合，确认 `padding_mask = 7`、完整轮次与 latest 结果；通过后已终止连接并删除临时数据库，未修改共享测试库 schema。
- 2026-07-14：基线复查修复后，累计均值 / 约束通过率、映射与协议硬冲突门禁、`recovered` / `rejected` / `expired` 固定时间状态和 SQLite cohort COUNT 查询计划均通过；完整非 PG 模型检测套件、账户删除清理、`test:postgres-schema-sql`、全工作区 `pnpm typecheck` / `pnpm build` 再次通过。新索引的真实 PostgreSQL 建表与 explain 随本批 8 表 / 11 索引迁移演练执行，本提交不连接或修改生产库。

## 风险与注意事项

- 黑盒行为指纹是统计证据，不是加密证明；高级中转可以针对已知探针适配。
- 无官方对照时群体基线可能集体被污染，必须按独立上游桶限权并保留证据不足状态。
- 如果多数独立上游桶都做相同替换，群体基线可能无法发现；第一版优先输出塌缩 / 离群，不承诺点名物理模型。
- 模型快照和服务端版本更新会引发群体漂移，必须版本化基线，不能把升级当降级。
- 漂移候选只按 observation 时间判断，不依赖 server 当前时间；群体恢复记为 `recovered`，候选分布失稳记为 `rejected`，证据不足超过 7 天记为 `expired`，三类历史状态均不再覆盖 active 基线。
- 长上下文和多轮探针成本高、持续时间长，需有启动节奏和极限档显式确认。
- 精确 tokenizer 仍不能复现所有服务端消息模板，Token 判断必须以差分斜率为主，固定 intercept 只做 cohort 对照。
- 输出 Token 可能包含隐藏 reasoning；没有 breakdown 时不能据可见文本差值判断灌水。
- 当前现有全链路回归失效是实现前硬门禁，不能跳过。

## 完成总结

当前完成 Phase 0、五维报告、输入 Token 差分、生成式身份 observation 和增量群体基线闭环：五模型 paired 探针、脱敏有界特征、独立上游桶限权、leave-one-upstream-out 稳健分布、同源 / 降级诊断、群体漂移保护、账号 latest 及中文证据已经落地。尚未完成全类别结构化特征、profile 驱动动态长上下文、测试环境离线重建对照和生产真实样本阈值校准；当前机制始终仅诊断，不会自动停号、改账或改写 usage。
