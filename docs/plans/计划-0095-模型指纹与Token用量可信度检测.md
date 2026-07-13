# 模型指纹与 Token 用量可信度检测计划

## 基本信息

- 编号：PLAN-0095
- 状态：进行中（设计评审阶段，尚未开始代码实现）
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

- [ ] 按 2026-07-13 官方模型页同步 GPT-5.6 context / max input / max output 目录事实和对应回归，移除旧 `372000` 运行口径。
- [ ] 按当前账户状态规则修改共享 mock fixture，通过正式激活 / 测试流程获得可调度账号。
- [ ] 重跑现有完整 profile、严格模型匹配、paired mismatch、分布相似度和长上下文回归。
- [ ] 确认测试确实请求 mock 上游，而不是在账号选择阶段提前退出。
- [ ] 固化当前报告 JSON 和评分行为，作为增强前回归基线。

### Task 2：契约与存储设计

- [ ] 定义 `identityStatus`、`mappingStatus`、`usageIntegrityStatus`、`protocolStatus` 和 `evidenceStatus` 契约。
- [ ] 定义 `model_check_observations` 当前 schema、有界 JSON 字段、索引和保留期。
- [ ] 定义指纹、模型配对、Token 诚信和账号最新结果四类统计窗口。
- [ ] 定义上游 origin、probe key 和 `system_fingerprint` 的 HMAC 规则与密钥轮换边界。
- [ ] 同步 SQLite / PostgreSQL 当前 schema、类型、repository、接口和前端 DTO；不增加旧结构兼容分支。

### Task 3：Token 用量可信度

- [ ] 评估并选择支持目标模型 encoding、可版本锁定的精确 tokenizer 实现。
- [ ] 实现 P0 / P1 / P2 精确填充块和实际 outbound 请求 Token 复核。
- [ ] 实现随机轮次、reported / local delta、robust slope / intercept 和置信区间。
- [ ] 实现比例、固定和分桶三类异常原因码。
- [ ] 对隐藏 reasoning、usage 缺失、缓存语义不兼容和 tokenizer 不支持返回 `unsupported` / 证据不足。
- [ ] 保持 usage 事实和计费逻辑不变，只保存诊断 observation 和窗口结果。

### Task 4：GPT-5.6 指纹 observation

- [ ] 把 probe catalog 拆为公开类别和隐藏版本化题面，加入运行时生成式 canary。
- [ ] 为约束、代码、推理、错误恢复、多语言、工具 schema 和知识时间窗输出结构化 feature。
- [ ] 把长上下文从固定三档改为 profile 驱动的低 / 中 / 高 / 可选极限阶梯。
- [ ] 随机交错执行 Sol / Terra / Luna 和 5.5 / 5.4 paired probes。
- [ ] 采集 HMAC 后的 `system_fingerprint` 辅助信号，但不把它作为硬身份凭据。
- [ ] 所有 observation 通过 ingest 队列写入，禁止检测 API 同步聚合。

### Task 5：群体基线与账号结论

- [ ] stats-worker 按游标增量构建 cohort 指纹窗口、paired 相似度窗口和 usage 诚信窗口。
- [ ] 按上游桶限制贡献权重，使用 leave-one-upstream-out、median / MAD、分位数和 bootstrap 区间。
- [ ] 实现 bootstrap / candidate / stable 基线状态和基线版本切换。
- [ ] 实现群体共同漂移保护、异常样本退出稳定基线和单供应商投毒保护。
- [ ] 刷新 `model_account_trust_results`，API 只读预聚合结果，不扫 observation 或 usage 明细。

### Task 6：评分与中文前端

- [ ] 保留现有总览等级，同时展示模型身份、映射、Token 诚信、协议和证据充分度五个分项。
- [ ] 详情页展示 requested / upstream / observed model、probe / baseline version、来源桶数、样本轮次和中文原因码。
- [ ] 显式映射显示“已配置模型映射”，未声明冲突显示“响应模型与请求不一致”。
- [ ] 证据不足、unsupported、网络失败和统计异常使用不同中文状态，禁止统一显示“假模型”。
- [ ] 前端不展示隐藏题面、明文上游 origin、HMAC 输入或敏感响应。

### Task 7：校准、发布与观察

- [ ] mock 注入诚实 usage、5% / 10% 比例放大、固定增加、64-token 取整和 missing usage。
- [ ] mock 注入显式 Sol -> Luna、未声明 Sol -> Luna、5.5 -> 5.4 和三个模型同源行为。
- [ ] 测试环境收集至少一个完整 baseline window，校验离线重建和在线增量一致。
- [ ] 生产先以小流量、仅诊断、不自动处置方式运行，观察成本、误报和上游限流。
- [ ] 根据真实样本校准阈值并记录版本；未达到独立来源门槛时保持证据不足。
- [ ] 完成构建、类型检查、回归、部署验收和发布后观察，再更新本计划状态。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 现有回归 | 协议 profile | `pnpm test:model-check-protocol-profiles` | 通过 | 已通过 | 2026-07-13 已通过 |
| 现有回归 | 完整 profile 等五条全链路 | 见审计报告命令表 | 全部执行到 mock 上游并通过 | 未通过 | 当前被共享 fixture `pending_test` 阻塞，Phase 0 修复 |
| 模型目录 | GPT-5.6 官方上下文边界 | 模型目录与能力回归 | 三个 5.6 使用当前官方 1,050,000 context / 922,000 max input / 128,000 max output 语义 | 未执行 | 当前运行值仍为旧 `372000` |
| 映射 | 显式与未声明 Sol -> Luna | 新增后端回归 | 两种场景分别为 configured mapping 和硬冲突 | 未执行 | 待实现 |
| 模型指纹 | 三模型同源塌缩 | 新增多轮统计回归 | 命中 `suspected_same_source` | 未执行 | 待实现 |
| 模型指纹 | 相近但正常分布 | 新增抗误报回归 | 不因单题相似误报 | 未执行 | 待实现 |
| Token | 诚实 usage | 新增差分 Token 回归 | slope 置信区间包含 1 | 未执行 | 待实现 |
| Token | 5% / 10% 比例灌水 | 新增差分 Token 回归 | 5% 进入校准边界，10% 稳定异常 | 未执行 | 待实现 |
| Token | 固定与分桶灌水 | 新增差分 Token 回归 | 分别输出 fixed / bucketed 原因码 | 未执行 | 待实现 |
| Token | reasoning 无 breakdown | 新增边界回归 | 输出 unsupported，不误报 | 未执行 | 待实现 |
| 基线 | 单上游大量账号投毒 | stats-worker 回归 | 同一上游桶权重受限 | 未执行 | 待实现 |
| 基线 | 群体模型升级漂移 | baseline version 回归 | 创建新版本并暂停强结论 | 未执行 | 待实现 |
| 存储 | observation 脱敏与大小上限 | sanitizer / repository 回归 | 无题面、凭据、明文 origin 或无界 payload | 未执行 | 待实现 |
| 统计 | 增量与离线重建一致 | worker 回归 / smoke | 窗口结果一致，游标仅在完整处理后推进 | 未执行 | 待实现 |
| 前端 | 中文状态与证据详情 | 前端回归和浏览器验证 | 中文、无敏感字段、状态不混淆 | 未执行 | 待实现 |
| 全量 | 类型、构建、关联回归 | `pnpm typecheck`、`pnpm build`、定向回归 | 全部通过 | 未执行 | 代码实现后执行 |

## 进度记录

- 2026-07-13：完成当前代码、官方能力边界、线上 macOS 样本、显式映射和测试门禁审计。
- 2026-07-13：确认当前只有通用行为指纹，没有 GPT-5.6 子版本统计基线，也没有 Token padding 检测。
- 2026-07-13：确认 Pro / Max / 思考档位等账号权益能力不进入模型身份判据。
- 2026-07-13：完成长期设计、审计报告、架构 / 用量口径和计划索引同步；等待用户复核后再编写文件级实施步骤并修改代码。

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

## 风险与注意事项

- 黑盒行为指纹是统计证据，不是加密证明；高级中转可以针对已知探针适配。
- 无官方对照时群体基线可能集体被污染，必须按独立上游桶限权并保留证据不足状态。
- 如果多数独立上游桶都做相同替换，群体基线可能无法发现；第一版优先输出塌缩 / 离群，不承诺点名物理模型。
- 模型快照和服务端版本更新会引发群体漂移，必须版本化基线，不能把升级当降级。
- 长上下文和多轮探针成本高、持续时间长，需有启动节奏和极限档显式确认。
- 精确 tokenizer 仍不能复现所有服务端消息模板，Token 判断必须以差分斜率为主，固定 intercept 只做 cohort 对照。
- 输出 Token 可能包含隐藏 reasoning；没有 breakdown 时不能据可见文本差值判断灌水。
- 当前现有全链路回归失效是实现前硬门禁，不能跳过。

## 完成总结

当前仅完成设计、现状审计和计划落盘，尚未修改运行代码、数据库 schema、接口或前端。用户复核方案后，再补文件级实施计划并按 Task 1 至 Task 7 执行。
