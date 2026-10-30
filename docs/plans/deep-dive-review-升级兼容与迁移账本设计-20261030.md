# 深挖复审台账：升级兼容与迁移账本设计

- 锚定范围：对《升级兼容与迁移账本设计》（`docs/functions/升级兼容与迁移账本设计.md`，2026-10-29 设计稿，未实施）做穷尽质量审查——事实引用核对、契约自洽、与项目既有规则冲突、可实施性。
- 发起：2026-10-30，用户指令「你自己在复查复查吧」；授权：A 类即修（设计文档自身的事实错误、内部矛盾、引用失准直接改文档），B 类过目（改变设计提案契约语义的修正呈报后执行），C 类裁决清单（产品取舍、功能缺口）。
- 需求脉络：开源后升级由第三方使用者执行；目标「任意已发布版本 → 最新，一条 `maintenance upgrade`」；非目标：运行时兼容层、在线迁移、迁移框架、降级。关键契约：三段顺序（ensure 加法相 → 数据步骤 → 破坏相）、`schema_state` 仅由 upgrade 写、账本 `running→applied`、启动门禁 `dbRevision==SchemaRevision 且 stepMax<=dbRevision`、退出码 0/1/2/3、12 auto 步骤 + 3 独立命令、配置注册表与 `check-config`、N-3 升级测试矩阵。
- 涉及文件：`docs/functions/升级兼容与迁移账本设计.md`（被审对象）；`docs/functions/README.md`、`docs/待办与阻塞事项.md`（索引登记，已随交付更新）。
- 关联契约：`docs/architecture/功能开发指导.md` §5.1、`AGENTS.md`（文档先行/部署同步/功能不设开关）、`docs/README.md` 命名规范、`docker/single-server/README.md`、`docs/deploy/部署指南.md` §4、`backend-go/projects/maintenance/cmd/juhe-ai-maintenance/main.go`、`maintenance/internal/schema/pg_schema*.go`、`gateway/cmd/juhe-ai-gateway/{runtime,scheduling_preference_schema,health_route}.go`、`shared/contracts/business_schema.go`。
- 项目裁决出处：`AGENTS.md` 根文档「文档与代码一致性」「部署变更文档同步」「事件导航」；全局 `AGENTS.md`「开发规范 1-9」；`docs/README.md` §4。
- 代码基线：HEAD `e319ed79e`（master）；工作区含并行会话在制品（见下）。
- 工作区边界：并行会话占用——`backend-go/projects/**` 多文件在制品、`docs/functions/接口契约与权限矩阵.md`、`docs/plans/README.md`、`docs/plans/PLAN-20261029T151306819Z-向后兼容代码大清洗.md`。本复审**不读改**这些文件，也不把它们的事实状态作为证据（其中的并行改动可能使某些引用行号漂移，涉及处以 HEAD 基线复核）。共享序号：待办编号已用到 T78（本设计登记占用），plans 无序号体系（UTC 时间戳），无冲突。
- 当前阶段 / 当前轮次：已收口（R3 交互制确认项修复落盘；见终报收口说明）
- 收敛计数：0 / 3（三轮各有确认项即修，未形成连续干净轮——按出口②收口，见终报）

### R1 角色分工与覆盖面登记

| 角色 | 维度 | 覆盖范围 |
| --- | --- | --- |
| A | 数据流与契约一致（文档声明 vs 仓库事实） | §1 七项事实、§4 资产表、§6.2 构建脚本行号、§10 映射表 Satisfied 判定逐条、§7.3 破坏性 DDL 计数、行号引用 |
| B | 状态一致性 / 错误路径（文档内部逻辑） | §5-§9、§11、§14、§18：执行序列、状态机、退出码映射、报告 JSON、门禁三态、adopt 顺序、确认门禁条件、交叉引用 |
| C | 回归与兼容（与项目既有裁决） | AGENTS.md 全局+项目、功能开发指导 §5.1、docs/README 命名、Go三项目架构基线：WARN-only 语义、旗标 vs 开关、semver 衔接、§13 与 §5.1 冲突、§19 同步清单完整性 |
| D | 资源与生命周期 / 外部输入（对码实施） | pg_schema golden 纪律、EnsurePostgres 分相、target 归属真实 schema、CheckBusinessPostgresSchema 强度、step 抽取重构面、advisory lock、/health 双面、configregistry 落点、夹具生成可行性、12 列 × 12 步骤读写面、seed 角色 |
| E | 消费者契约 × 运维部署面（外部视角） | fresh 安装序列、deploy.sh 执行面、verify-release 断言、直跑形态、版本获取路径、备份门槛可达性、停写时长披露、多库边界 |
| F | 数据演化 × 边界态 | 备份恢复再升级、running 残留、checksum 覆盖面、Satisfied 误判、时间列惯例、门禁对旧二进制的盲区、账本表自引用升级 |

## 问题条目表（活跃条目）

| ID | 来源 | 假设（一句话） | 关键依据 | 状态 |
| --- | --- | --- | --- | --- |
| S1 | R2 场景制 | primary DSN 连不上的退出码二义（exit 2 vs exit 1） | L279/L327/L331 vs L366 | 已确认(中)（R2-VA）→批3修复 |
| S2 | R2 场景制 | --step-timeout 无下界校验、适用面未定义，estimate 失真 | L294/L309/L353/L361 | 已确认(中)（R2-VA）→批3修复 |
| S3 | R2 场景制 | --secret 错误值被 seed 静默接受，坏凭据无失败痕 | L307/L375、pg_seed.go:661/710 | 已确认(中)（R2-VA）→批3修复 |
| S4 | R2 场景制 | --report-out 落盘失败行为未定义（翻码或静默丢诊断） | L203/L308/L331 | 已确认(低)（R2-VA）→批3修复 |
| S5 | R2 场景制 | §11.3 fresh 序列裸 upgrade 与确认门禁冲突（截面回归） | L316/L445-451 | 已确认(中)（R2-VA）→批3修复 |
| S6 | R2 场景制 | deploy.sh all 插入 upgrade 无停写步，确认变虚假声明 | L453、deploy.sh:262-276 | 已确认(中)（R2-VA）→批3修复 |
| S7 | R2 场景制 | upgrade 模式镜像重建时点未定义，旧镜像假升级 | L453、deploy.sh:20-24 | 已确认(中)（R2-VA）→批3修复 |
| S8 | R2 场景制 | compose run 缺 --profile tool，照抄报 no such service | compose.yml:252、L448/L453 | 已确认(低)（R2-VA）→批3修复 |
| S9 | R2 场景制 | 门禁四行表条件独立可达性/死子句复核 | L418-427 | 已证伪（R2-VA） |
| S10 | R2 场景制 | state 有、账本空的反向不一致无门禁互证 | L418-427/L429/L373 | 已确认（R2-VB）→批3修复 |
| S11 | R2 场景制 | 持锁期间 dry-run 2s 快失败的报告形态未定义 | L367/L383 | 已确认（R2-VB）→批3修复 |
| S12 | R2 场景制 | exit 2 用法错误路径「任何退出路径都输出报告」不可满足 | L331/L334 | 已确认（R2-VB）→批3修复 |
| S13 | R2 场景制 | adopt 行写入时点两读：确认门前写账本则保护语义漏 | L76/L252-256/L370/L372 | 已确认（R2-VB）→批3修复 |
| S14 | R2 场景制 | 矩阵起点缺口：首个 tag 时 P4 夹具/账本尚不存在 | L536-541/L546/L581-587 | 已确认（R2-VB）→批3修复 |
| S15 | R2 场景制 | 「MINOR 只增不删」与破坏相常驻 DROP、§13 的「删」不同义 | L92/L229/L517 | 已确认（R2-VB）→批3修复 |
| S16 | R2 场景制 | target 连接串「存在但不可达」失败时点/退出码二义；部分 target 失败归因 | L283/L366/L331 | 已确认（R2-VB）→批3修复 |
| S17 | R2 场景制 | dev 构建把 version=dev 写入账本/state，可辨识性失效 | L116/L165/L183 | 已确认（R2-VB）→批3修复 |
| S18 | R2 场景制 | check-config --list / 子命令 --version 旗标面漂移 | L490-493/L502/L312 | 已确认（R2-VB）→批3修复 |
| B1 | R1-B | dry-run 退出点（第 1 步）早于其依赖的账本读取（第 3 步），拿不拿锁未定义 | §9.4 L319-321、L273 | 已确认(中)（V1） |
| B2 | R1-B | 仅加 DDL 无新步骤也强制 +1，且 dbRevision 只由 upgrade 写 → 「只跑 ensure-schema 后启动」被恒拦，与 L356「flag 行为不变」承诺冲突 | L128-133、L365-377、L356 | 已证伪（V2） |
| B3 | R1-B | 升序遍历 + 独立 Satisfied 允许「adopt 40 后 apply 39」，未禁止前向依赖 | L324、L262 | 已证伪（V2） |
| B4 | R1-B | Verify 失败的退出码：§8.1 注释 exit 3 vs §9.4.6 exit 1 矛盾；「库新于二进制 exit 3」超出 §9.2 中 3 的定义 | L227、L324、L322 | 已确认(中)（V1） |
| B5 | R1-B | 报告示例数字自相矛盾（dbRevision=39 下步骤 35 不应仍在 adopt；38/39 无处可寻）；executed/postflight/rollback 三场景存在性未定义 | L303-308、L313 | 已确认(中)（V2） |
| B6 | R1-B | pending 谓词未定义（running 行算不算），断点续跑承诺悬空 | L175-181、L324 | 已证伪（V1） |
| B7 | R1-B | adopt/applied 先于 schema_state 写入，中断时三条件同时命中、分支优先级未排 | L365-377、L324 | 已确认(低)（V1） |
| B8 | R1-B | 「破坏性语句非零」按常量解释恒为 12 → 免确认分支不可达；按生效解释判定方法未定义 | L282-283、L201 | 已确认(中)（V2） |
| B9 | R1-B | 既有 DDL 语句文本修改（含注释）不触发 +1，golden 与 statementCount 两道防线都漏过 | L128-133、L206-207 | 已确认(中)（V2） |
| B10 | R1-B | 目标 DSN 缺失检查时序未定义（第 1 步还是第 6 步）；晚查则加法相已先行执行 | L252、L319-324 | 已确认(低)（V1） |
| B11 | R1-B | 验收 7 用 xact_commit 差断言零写入不可行；P1「重复执行零写入」与每次写 schema_state 矛盾，三处口径不一 | L540、L525、L535 | 已确认(中)（V2） |
| B12 | R1-B | §7.4 golden 称「§17 P1 交付项」但 P1 行未列；§8.3 自述依赖 §20.3 裁决却不在 L9 状态行 | L207、L524-525、L9 | 已确认(低)（V2） |
| B13 | R1-B | L37/L464 裸「§5.1」无文档限定词，易误读为本文 §5 | L13、L37、L464 | 已确认(低)（V2） |
| B14 | R1-B | 「全部已最新时只读核验 exit 0」与执行序列无条件跑两相+postflight+写 state 矛盾，短路分支未定义 | L283、L323-328 | 已确认(中)（V1） |
| B15 | R1-B | rollback 字段在执行完才输出，中途失败时是否存在/取值未定义；「换回旧二进制重跑旧库」与已 applied 账本行的交互未说明 | L474、L328、L470-472 | 已确认(低)（V1） |
| B16 | R1-B | 失败原因正是连接/写账本失败时 detail_json 写入失败的行为未定义 | L324、L178 | 已证伪（V1） |
| B17 | R1-B | 3 个 destructive=是 的步骤把删列/删表放进第 6 步数据相，推翻「破坏性窗口缩到最后」承诺；rollback 翻转时机未定义 | L346、L349-350、L199、L470 | 已确认(中)（V1） |
| E1 | R1-E | upgrade 执行序列不含 seed，存量升级后内置题库等种子数据断供（§9.4 无 seed vs §10 称「seed…upgrade 内部调用」自相矛盾） | §9.4 :319-328、§10 :336、docker/single-server/README.md:34 | 已确认(中)（V3） |
| E2 | R1-E | §11.3 全新库序列缺 J3b 运行态索引初始化，照做 gateway 仍 fail-fast | §11.3 :396、docker/single-server/README.md:42、部署指南:30 | 已确认(中)（V3） |
| E3 | R1-E | backfill 步骤 Satisfied「六表 done」在全新库为假（ensure 只建空表不写行），「全部步骤 adopt 零执行」断言不成立 | §10 :348、§11.3 :396、pg_schema_stats.go:1971-1978 | 已确认(中)（V3） |
| E4 | R1-E | Satisfied 先于 Apply 求值：juhe_j3b schema 缺失时 Satisfied 查表报错，到不了「Apply 前建 schema」 | §8.3 :254、§8.1 :223-225 | 已证伪（V3） |
| E5 | R1-E | deploy.sh 正式 upgrade 的执行步骤未设计（只有 dry-run 前置核验），[4/6] up -d 新版会 fail-fast 循环；§19 清单不含 deploy.sh | §11.3 :398、§17 :526、deploy.sh:262-276 | 已确认(中)（V4） |
| E6 | R1-E | 「镜像 tag 一一对应」在单机 compose（本地 build 无 registry）无处落地，§6.2 改动点不含 compose.yml tag 参数化 | §6.1 :87、compose.yml:96/190/250 | 已确认(中)（V4） |
| E7 | R1-E | Windows 发布包脚本（package-release.ps1:288-296）无 ldflags 注入，包内二进制恒 version=dev；§19 无 Windows 直跑升级文档 | §6.2 :105-110 | 已确认(中)（V4） |
| E8 | R1-E | `--backup-confirmed` 纯人工声明对无备份习惯用户无最低保障，属功能缺口未进 §20 | §9.1 :274、§20 :554-579 | 已确认(中)（V7） |
| E9 | R1-E | `--dry-run` 报告无预计时长/数据量，停写窗口不可估算 | §9.1 :273、§9.3 | 已确认(低)（V5） |
| E10 | R1-E | 「停写时长可预期」「分钟级」无产生数值的机制或实测口径 | §2.2 :38、§7.3 :198 | 已确认(低)（V4，收窄：:198 半句不成立） |
| E11 | R1-E | 中途失败的用户呈现形态未定义（JSON 报告/stderr/可读报告） | §9.4 :324 | 已确认(低)（V5） |
| E12 | R1-E | §4 流水账收敛后，历次批次行为变更披露在 CHANGELOG 三节结构中无归属 | §16.1 :512-516、§16.2 :517 | 已确认(中)（V4） |
| E13 | R1-E | §6.3 对 verify-release.sh 断言面描述失实（实际三项断言不含 ready） | §6.3 :121、verify-release.sh:43-99 | 已确认(低)（主代理直读 verify-release.sh 证实：三条断言无 ready） |
| E14 | R1-E | 多库/多实例边界未声明（账本随库、需逐库跑；多 gateway 实例是 README 认可形态） | §7.1 :169、§8.3 | 已证伪（V4，事实前提不成立） |
| E15 | R1-E | 直跑形态回滚依赖旧 release 目录保留，§14 未提该差异（弱信号） | §14 :468-473、§19 :548 | 已证伪（V4） |
| C1 | R1-C | §12.2 未知变量 WARN-only 的判据与呈现面未成文，可能被判为「静默降级」新形态、验收 6 不可测 | 设:425-430,539 vs 全·规范1 | 已证伪（V5） |
| C2 | R1-C | 旗标面（dry-run/confirmed/report-out/step-timeout）未对照「功能不设开关」做显式归类声明 | 设:267-283 vs 全·规范2 | 已证伪（V5） |
| C3 | R1-C | 「镜像 tag 一一对应」与 compose 无版本镜像名、无 registry 本地构建形态无衔接，§6.2/§19 均不含改造项 | 设:87 vs compose.yml:96,190,250 | 待定 |
| C4 | R1-C | CI/发布流水线在仓库不存在（无 .github/workflows），「CI 全绿」「发布流水线生成夹具」「漂移测试」三类完成判定不可达，基建前置未交代 | 设:33,443-446,528 | 已确认(中)（V4；漂移测试可本地单测的半句被驳回） |
| C5 | R1-C | 「git tag 为唯一来源」未交代本地工作树构建下 VERSION/Commit 派生与脏工作树防线（前端有 buildId==HEAD 门禁先例，后端无） | 设:87,93-110 vs 署:131-134 | 已确认(低)（V4） |
| C6 | R1-C | semver/CHANGELOG 与「计划用 UTC 时间戳」两套标识体系未声明互不替代；按日期历史挂 semver 版本头的迁移成本未交代 | 设:87-89,512-519 vs docs/README.md:49 | 已证伪（V4，后半并入 E12） |
| C7 | R1-C | §13「不删旧列留一个 MINOR」与 §5.1「不做向下兼容/同批切换/结构直接调整到最优」存在 schema 级字面冲突，§20.1 单句豁免不了 schema 保留，裁决 A 后应直接修 §5.1 | 设:458-464,556-562 vs 功:118-126 | 已确认(中)（V5） |
| C8 | R1-C | §19 同步清单漏 `.local/project-resources/prod/runbooks/国内单机Docker部署与运维.md` 去向与 CHANGELOG.md 新建文件 | 设:543-552 vs AGENTS.md 部署同步节 | 已确认(中)（V5，CHANGELOG 半点被驳回） |
| C9 | R1-C | functions/README 双登记状态文本需手工保持一致，状态推进时可能漂移（低危） | 函:7,46 | 已证伪（V5） |
| C10 | R1-C | configregistry 放 shared/platform 的跨项目边界（谁可改、gateway 内嵌他项目 env 声明、check-config --process all 的校验边界）未表达 | 设:404-421 vs 基:37,112 | 已证伪（V5） |
| C11 | R1-C | 目标 1「任意已发布版本→最新」绝对承诺与 §16.3「仅最近 3 版背书」不一致，应挂「以版本兼容矩阵为准」限定 | 设:29,33,518 | 已证伪（V2） |
| F1 | R1-F | §14 缺「升级成功后恢复旧备份→重跑 upgrade」路径；§8.4 幂等契约未定义回退重放语义 | 设:468-472、258-263 | 已证伪（V1） |
| F2 | R1-F | 恢复备份会抹掉迁移后人工热修（urlv2 7 行人工修正先例），重升终态 ≠ 首升终态，无提示 | 设:347、待办:248 | 已确认(低)（V1） |
| F3 | R1-F | running 行 detail_json 多次失败间覆盖/追加语义未定义，与 append-only 表述未对齐 | 设:178,181,324 | 已确认(低)（V7） |
| F4 | R1-F | dry-run 与报告对 running 行无呈现定义，plan 二分不完备 | 设:273,306-307 | 已确认(低)（V1） |
| F5 | R1-F | 旧二进制注册表无新步骤，无法消费 running 行；门禁「请重跑 upgrade」不指明版本，死循环引导 | 设:375,232 | 已确认(中)（V3） |
| F6 | R1-F | checksum/golden 钉不住 Apply 实现体，「只能新增补偿步骤」是无检测机制的裸契约，弱于 schedprefdomain ruleVersion 先例 | 设:205-207、schedprefdomain/record.go | 已确认(中)（V6） |
| F7 | R1-F | merge 步骤谓词只扫 route_strategies 不看产物表，手工清理库误 adopt，数据丢失静默 | 设:345、mergesupermigration | 已证伪（V3，adopt≡no-op 判据同源） |
| F8 | R1-F | schedpref 谓词看记录不看偏好数据，与 capability domain 双判据不同源 | 设:344,381-386 | 已确认(低)（V3） |
| F9 | R1-F | 账本 timestamptz 与业务表 text 时间惯例分裂（jobs 有 timestamptz 先例，属惯例分裂非硬伤） | 设:147,159-160、pg_schema_business_tables.go:32-33 | 已确认(低)（V7） |
| F10 | R1-F | advisory lock 只覆盖 upgrade 与 ensure-schema，15 个旧 flag 命令不加锁，可与之并发 | 设:332,356 | 已确认(中)（V6） |
| F11 | R1-F | 「沿用 pg_lock_retry.go 的有界等待」证据引用错位：真先例是 shared/platform/advisorylock（键空间不可变更契约也未登记） | 设:320、pg_lock_retry.go:1-35、advisorylock.go | 待定 |
| F12 | R1-F | schema_state 被手工调高 revision 后只有 exit 3，无修复命令或自愈路径 | 设:142-148,373-374 | 已证伪（V7） |
| F13 | R1-F | 「全新库全部步骤 adopt 零执行」不成立：seed 只封存 schedpref，capability domain 谓词 false 会照跑；adopted 来源无法区分三种 | 设:396、pg_seed.go:138-139 | 已证伪（V3，ensure 臂封存 capability domain） |
| F14 | R1-F | 夹具 ledger.json 未声明按 fresh（adopted）还是升级（applied）路径生成，断言口径漂移 | 设:484 | 已证伪（V7） |
| F15 | R1-F | 门禁对 pre-gate 旧二进制的永久盲区未声明（旧二进制无门禁代码，对新库启动不被拦） | 设:389-393 | 已证伪（V7） |
| F16 | R1-F | dry-run 在方向校验（第 4 步）前退出，对「库比二进制新」要么空 plan exit 0 要么被拦，两读都有害 | 设:319 vs 322 | 已确认(中)（V1） |
| F17 | R1-F | 账本表自身 DDL 变更的自引用引导问题：纯 DDL revision 在 golden 无落点；读账本先于 EnsureAdditive 的旧形状容忍未声明 | 设:130,207,321 | 已确认(低)（V2） |
| F18 | R1-F | 「破坏性语句非零」常量/动态二义（与 B8 同源，交叉印证），report 示例写死 12 佐证常量解释 | 设:282-283,201,308 | 待定 |
| D1 | R1-D | 账本表加入 pg_schema_business_tables.go 与文件头「Node 逐字移植、不得手改」纪律冲突，golden（693 语句/per-schema 计数）必拦 | pg_schema_business_tables.go:1-6、pg_schema_test.go:105-182 | 已确认(中)（V6） |
| D2 | R1-D | 分相过滤使跨相执行序不再等于「concatenation order = execution order」的 Node 单遍序契约，且该契约无测试保护 | pg_schema.go:72-79,115-137 | 已确认(低)（V6） |
| D3 | R1-D | postflight statementCount 若指 PGResult.StatementCount 则恒真（成功=全建），封不死漏建表；若指库内对象计数则算法未定义 | pg_schema.go:136、设:326 | 已确认(低)（V2） |
| D4 | R1-D | CheckBusinessPostgresSchema 只断言 juhe_business 最小集，不覆盖 chat/j3b，postflight 结构断言能力被夸大 | business_schema.go:4-6,44-72 | 已确认(低)（V6） |
| D5 | R1-D | SchemaRevision 初值=1 与首版 12 步骤序号数学矛盾：stepMax≥12 > dbRevision=1 → 永久 fail-fast；首版 revision 分配未定义 | 设:130-133,215-232,364-376 | 已确认(高)（V2） |
| D6 | R1-D | 「沿用 pg_lock_retry 的有界等待」「与 ensure-schema 共用同一 key」均无现存对应物（pg_lock_retry 是语句级重试；EnsurePostgres 不取锁）；新 key 需避开既有键空间 | pg_lock_retry.go:1-17、advisorylock.go:26 | 已确认(中)（V6） |
| D7 | R1-D | upgrade 旗标表缺 --secret/JUHE_AI_SECRET 契约：upstream base URL step 与 postflight 强制要密钥，缺失即必败无指引；静默回落开发默认有错封风险 | upstream_base_url_migration.go:105-106、main.go:443 | 待定 |
| D8 | R1-D | §10 称 seed 由 upgrade 内部调用 vs §9.4 无 seed，自相矛盾（与 E1 交叉印证）；题库/目录行仅存在于 SeedPostgresDefaults | 设:319-328 与 :336、pg_seed.go:112-127 | 待定 |
| D9 | R1-D | capability domain 是 m1/m2 两阶段显式状态机，映射为单 auto step 丢掉中间态表达，「同一内部函数」复用面被低估 | account_capability_domain.go:51-81 | 已证伪（V3） |
| D10 | R1-D | accountbalance.BootstrapPostgres 无只读模式，dry-run/Satisfied 复用会直接建表，违反「不写任何表」承诺 | accountbalance/bootstrap.go:53,68 | 已证伪（V3） |
| D11 | R1-D | 三个 apply-* bootstrap 强制显式角色校验且「不回落应用 URL」，§8.3 的 --dsn 回落路径与真实校验冲突，报错指向不符 | main.go:388-396 | 已证伪（V3） |
| D12 | R1-D | account_balance 表实际落 juhe_jobs schema，§8.3 的 CREATE SCHEMA `<schema>` 映射含糊，可能建出幻影 schema | main.go:54、设:241-254 | 已确认(低)（V3） |
| D13 | R1-D | target=primary 掩盖跨 schema 写入（stats/chat），schema 拆库部署下写入落空但账本记成功 | success_cost_backfill.go:73 | 已证伪（V3） |
| D14 | R1-D | schemasnapshot 输出是 JSON 快照非 SQL，逆向无法还原函数/触发器/DO 块，夹具「导出并转 SQL」被低估；直接替代是 vN 二进制跑 ensure 或 pg_dump | schemasnapshot.go:1-13 | 已确认(中)（V7） |
| D15 | R1-D | mockdata 编译期绑定当前 schema，无法为 vN-3 旧形状库造数，§15.1 数据夹具来源不成立 | main.go:67 | 已证伪（V7） |
| D16 | R1-D | /health 增键只写 gateway 面，jobs 有独立实现的 loopback /health 同受门禁约束，设计遗漏 | 设:112-121、jobs main.go:59 | 已证伪（V4） |
| D17 | R1-D | getenv 是注入式闭包且 jobs/maintenance 同模式，§12.1 只点名 runtime.go；全量替换后漂移测试「扫描字面量」恒过，需改方向 | runtime.go:276-289、worker_config.go:114 | 已证伪（V5） |
| D18 | R1-D | `upgrade` 子命令语法与既有单层 FlagSet CLI 不兼容（Go flag 首个非 flag 参数停止解析，--dsn 会被吞）；--dry-run 已被其他命令占用 | main.go:49,77,94 | 已确认(中)（V5） |
| A1 | R1-A | env 计数 214/123/103/16 用任何常规口径都无法复现（字面量去重 206/144/37 并集 387；getenv 调用 67/45/0；README 表 90），P3 验收基线无口径依据 | 设:22 + rg 复算 | 已证伪（V5：214 并集可复现，jobs 分项 103→102 差 1） |
| A2 | R1-A | j3a Satisfied「6 表 + 2 索引」索引数错误：实际 3 条索引且就绪判定要求 3 条，照抄会误判 adopted | j3aproxylatency/bootstrap.go:309-311、bootstrap_test.go:72 | 已确认(中)（V6） |
| A3 | R1-A | 「除两处门禁外没有机制发现库落后」过强：还有 J3b 电路运行态索引 CheckReady 门禁与 codex_context schema 启动探针 | circuit_runtime_init.go:22、main.go:248,274、codex_context_store.go:27 | 已确认(中)（V6） |
| A4 | R1-A | 「verify-release.sh 只断言 ready 与内存限制」失实：实际三条断言为 spool 同源/drain 接线/GOMEMLIMIT，无 ready（与 E13 交叉印证） | verify-release.sh:9-16,41-63 | 已确认(低)（主代理直读 verify-release.sh 证实，与 E13 同源） |
| A5 | R1-A | README「更新发布」节手动流程段同样保留「回滚 = 上传上一版 build」通用句，冲突不只是「部署指南 vs README」两侧对立 | 部署指南:53、README:195,207 | 已确认(中)（V6） |
| A6 | R1-A | capability domain Satisfied「域列无空值」恒真（列 NOT NULL DEFAULT 'chat'，无 null 检查臂），写入契约制造错觉 | capabilitydomain/stage.go:25、capability_domain_schema.go | 已确认(低)（V6） |

**R1-A 已证实无误项（不立案）**：15 个迁移 flag 精确成立（main.go 44 flag 中迁移/清理恰 15）；12 条破坏性 DDL 精确成立；§6.2 四处行号全部存在且含 -ldflags；project.go:5、README.md:144、compose.yml:6 行号成立；§8.3 三个 env 名逐字一致；§10 其余各行判定与代码一致（j3b CHECK/余额 4 表/schedpref completed/merge 残留/chat 三列/upstream 复核桶/六表 done/studio/12 表/默认资源家族）；pg_lock_retry.go 存在；§19 ps1 指向确认；git tag 为空；三连接串文案；别名唯一；三个 verify 命令与资产存在。

## 覆盖矩阵（R1 维度制）

| 格 | 结论 |
| --- | --- |
| A 事实核对（10 攻击点） | 12 项证实无误（15 flag、12 DDL、4 处 ldflags 行号、env 名逐字一致、§10 其余判定、git tag 空、别名唯一等）；6 项发现 → A1-A6 |
| B 契约自洽（12 攻击点） | 无落空；17 条发现 → B1-B17 |
| C 规则冲突（10 攻击点） | 2 项未发现（「待裁决」与文档先行自洽、T78 六字段齐备）；11 条发现 → C1-C11 |
| D 可实施性（12 攻击点） | 攻击点 3/10 部分未发现（12 表归属单库一致；12 条 DROP 列与 upstream 迁移读写面无交集）；18 条发现 → D1-D18 |
| E 外部使用者（10 攻击点） | 3 项核实（.env 模板不存在、contract= 无消费方、ps1 指向确认）、§19 行号全部准确；15 条发现 → E1-E15 |
| F 边界态（10 攻击点） | 攻击点 1/6 部分被既有契约覆盖（F1/F12 剩余面收窄）；18 条发现 → F1-F18 |

交叉印证（≥2 独立角色命中）：B8=F18（确认门禁二义）、E1=D8（seed 缺失）、E6=C3（镜像 tag 无落地）、E13=A4（verify-release 断言面）、D6=F11（锁证据引用错位）。

**R1 对抗确认轮汇总**：78 行验证覆盖 85 条假设（5 组同源合并）。confirmed 51（高 1 / 中 24 / 低 26）、refuted 27、undetermined 0、duplicate 0。A4/E13 由主代理直读取证补验。被证伪代表：B2/B3/C11（设计原文已有依据）、F7/F13/D9/D10/D11/D13（对代码现状转述失实）、A1（214 口径可复现，jobs 分项 103→102）、C1/C2/C10/D17（规则引用错位）。

## 问题条目表（R3 交互制，I 系列）

| ID | 来源 | 假设（一句话） | 关键依据 | 状态 |
| --- | --- | --- | --- | --- |
| I1 | R3 交互制 | secret KCV 第三态未定义（存量库无可试解密载体） | §9.4-11 | 已确认(中)→批5修复 |
| I2 | R3 交互制 | KCV 时序晚于结构写入，保护强度不对称，可前移第 8 步 | §9.4-8/11 | 已确认(低)→批5修复 |
| I3 | R3 交互制 | estimate 口径二义：示例未含结构相上界；结构相无旗标可调 | §8.4-4 vs §9.3 | 已确认(低)→批5修复 |
| I4 | R3 交互制 | §11.3 upgrade 命令缺 --dsn 必填旗标；脚本 exit 2/3 分支未定义 | §11.3 vs §9.1 | 已确认(中)→批5修复 |
| I5 | R3 交互制 | deploy.sh 对 upgrade 非零退出分支未定义，停写窗口悬挂 | §11.3 | 重复（并入 I4） |
| I6 | R3 交互制 | 门禁顺序 3 文案未覆盖「pending 空 + state 未推进」（空号形态） | §11.1 vs §7.3 | 已确认(低)→批5修复 |
| I7 | R3 交互制 | §14「只能恢复备份」为假（可同版本续跑）；rollback 相级/语句级两读 | §14 vs §2.1-2 | 已确认(中)→批5修复（「只能恢复备份为假」半句被驳回，前向续跑入 §14） |
| I8 | R3 交互制 | ImplVersion 漂移重求值的落库语义（UPDATE/INSERT、标志翻转）未定义 | §7.2/§7.4 | 已证伪（当前文本已定义 UPDATE 既有行） |
| I9 | R3 交互制 | 漂移测试只比行数，内容漂移不可见；生成时机未定义 | §12.4 | 已确认(低)→批5修复 |
| I10 | R3 交互制 | executed 字段语义未定义（攻击者取证先于 T4 修复，按当前文本核） | §9.3 | 已证伪（T4 修复后示例与矩阵一致） |

## 扩散记录

| 轮次 | 新纳入对象 | 耦合类型 | 理由 |
| --- | --- | --- | --- |

排除项：并行会话在制品域（见工作区边界）；`frontend/**`（设计明示 API 面不在范围，§12.5）。

## 修复记录

| 批 | 内容 | 验证证据 | 落点复核轮次 |
| --- | --- | --- | --- |
| 1 | 设计文档 v2 全文修订（`docs/functions/升级兼容与迁移账本设计.md`），处置 R1 全部 51 条 confirmed；B 类清单为空（被审对象是未实施设计文档，修复不产生用户可见行为变化）。逐条映射：**事实修正** A2（j3a 6 表+3 索引）、A3（门禁盘点进 §1.7/§11.2）、A4=E13（verify-release 三断言）、A5（README:138 同款冲突句入 §19）、A6（删恒真判定）；**契约修正** B1/B10/F16（执行序列重排：DSN 前置、方向校验含 dry-run）、B4（退出码统一：Verify→3 / Apply→1 / 超账本→3）、B5+E9+E11+F4（报告字段存在性矩阵 + estimate + 失败呈现 + running 呈现）、B8（确认门禁改「pending 或库落后」，破坏语句数仅披露）、B14（已最新短路）、B15+B17（rollback 按「已发生的破坏性写」判定，§7.3 边界如实声明）、B7+F5（门禁四行求值顺序表 + 最低版本指引）、F2（人工热修重放提示）；**设计级** D5（SchemaRevision=序号空间最高 revision，初值 12）、B9（递增条件含语句文本修改 + 清单 digest golden）、D1（账本独立 upgrade_ledger.go + 专属 golden，不进 Node 生成文件）、F9（时间列改 text 跟随惯例）、F3（detail_json 覆盖写 + 成功清空）、F6（ImplVersion 检测实现漂移，对齐 ruleVersion 先例）、D2（相间顺序静态测试）、D6（advisorylock 新键 UpgradeOrchestration，ensure/seed 接入为声明行为改造）、D3+D4（postflight 语义收窄：语句计数=执行侧完整性、形状契约仅覆盖 business）、D7（--secret 旗标，禁静默回落）、D18（显式子命令分发）、E1=D8（seed 入序列第 11 步）、E3（fresh 空跑如实披露）、F8（schedpref 三重判据同源）、D12（target→schema 映射表）；**部署/发布** E5（deploy.sh upgrade 模式 + 核验用新镜像）、E6=C3（compose 镜像 tag 参数化 + 可核验性以二进制为准）、E7（ps1 ldflags + Windows 文档）、C4（矩阵先本地脚本，CI 入 §20.6）、C5（脏树守卫）、E12（CHANGELOG Pre-1.0 behavior notes 节）、C8（.local runbook 入 §19）、E2（J3b 索引入 fresh 序列）、E10（去「分钟级」改 estimate）、C7（§20.1 明确裁决 A 须改写 §5.1 条文）、E8（新增 §20.5 备份最低保障）。证伪 27 条不改。 |
| 2 | 回归修复批（R2 落点复核发现，最高优先级即修）：REG1 门禁四行表重写（按 stepMax>dbRevision 中断 / dbRevision>SR 落后 / dbRevision<SR 待升级 / 其余通过分流，附三场景代数验证——修复完成态被顺序 1 误判 + 验收 4 不可满足）；REG2+REG3 报告示例重做（自洽口径：36→40、37/39/40 pending、38 adopt、status 枚举 absent|running，删除「39 absent 而 40 running」矛盾形态）；REG4 §5 总览图步号与 §9.4 同步（7 dry-run/8 门禁/9 短路/10-14/15）；REG5 §1.7 与 §11.2 改「各管一段，覆盖面之外的迁移漏跑无感知」；REG6 顺序 4 自相矛盾括注删除；另落点复核两条小项：§10 capability domain 括注措辞、§6.4 纯 DDL 变更占空号规则；交互对 (21)(9) 建议：secret 缺失检查提前到 §9.4-8（先于任何写入）。落点结论：34 条修复 33 条落点正确。 | R2 落点复核轮 | R3 |
| 3 | R2 场景制确认 17 条修复（S1-S8、S10-S18；S9 证伪）：S1/S16 连接类失败分流（试连前置第 2 步，env 缺失 exit 2 / 不可达 exit 1）+ 退出码表重写；S11/S12 报告存在性改「第 1 步用法错误无 JSON、第 2 步起均输出」+ 矩阵加前置失败行；S13 adopt 写入时点定于第 12 步（第 6 步只读求值）；S2 --step-timeout 钳制 1m..4h + 结构相/postflight 独立上界；S3 secret KCV（存量库试解密，fresh 披露 secretVerified:false）；S4 --report-out 落盘失败不翻码；S5 fresh 序列补两确认（无豁免口）；S6/S7/S8 deploy.sh 顺序固定 build→停写→校验→upgrade→up + --profile tool；S10 门禁加顺序 4（账本空反向不一致）；S14 矩阵冷启动口径；S15 「MINOR 只增不删」限定用户可见结构、收敛 DROP 不算违约；S17 dev 构建账本记 dev-<sha> + WARN；S18 check-config --list 契约 + 子命令 --version | R3 落点复核（并入终态复核轮） | R3 |
| 4 | R3 终态复核 T1-T4 定点修复：T1 §5 图与 §9.3 报告存在性口径同步 + 矩阵列拆分（driver/binary 与 database 分列，前置失败行 database 标「连接失败子场景缺位」）；T2 exit 3 清单补 secret KCV 失败；T3 §12.1 maintenance 现状改「散点 os.Getenv，P3 先建装载边界」（实现断言抽核 15 条中唯一失实项）；T4 报告示例 executed 补全 37/39/40 三行与 estimate.steps=3 对齐。终态复核结论：7 对交叉 6 对一致（T1 已修）、15 条实现断言 14 条成立（T3 已修）、4 条升级路径全部走通。 | R3 终态复核轮 | 收口 |
| 5 | R3 交互制确认修复（I1-I7、I9；I8/I10 被当前文本证伪、I5 并入 I4）：I1 KCV 第三态（存量库无载体→同 fresh 披露 secretVerified:false）；I2 KCV 前移第 8 步（只读、先于任何写入，第 11 步只消费）；I3 estimate 分列（applySteps/applyUpperBound/structuralUpperBound/total，两类上界不合并）；I4+I5 deploy.sh 命令显式 --dsn + upgrade 非零退出固定分支（中止不 up -d、停写窗口不悬挂、二选一指引）；I6 门禁顺序 3 补「无待执行步骤（空号+结构相中断）」文案；I7 rollback 语句级判定（破坏相部分执行即 backup-required）+ §14 行 2 补前向续跑选项；I9 漂移测试改逐行内容比对 + 生成物同批提交。主代理自查 15 项不变量全过、§9.4 步号 1-15 连续。 | 批 5 未经独立复核轮（见终报收口说明） | — |
| 6 | 用户问询补强（收口后追加）：§9.4-6 preflight 增加结构 digest 与 `schema_state.schema_digest` 比对（不一致 WARN 不阻断）；新增 §9.6「用户自改的检测边界」——改坏官方结构由收敛自愈/形状门禁拦截、迁移前提坏由 preflight/Verify 零写入拦截、私加对象刻意保留（命名冲突边界披露：`ADD COLUMN IF NOT EXISTS` 接管语义 + 私加前缀要求）、代码自改靠 `dev-<sha>` 留痕。digest 字段自此有消费语义。 | 主代理自查 | — |
| 8 | 裁决落地 + P0 实施（2026-10-30，用户「继续做」指令）：①§20.1/§20.2/§20.5 按推荐项 A/A/① 落地——`docs/architecture/功能开发指导.md` §5.1 增边界澄清条文（运行时代码域 + 存储收敛两版走例外 + 升级编排属交付能力）、设计文档状态头记裁决与实施状态、§20.5 补备份维度方案；②P0 交付：`shared/platform/version` 包、`contracts.SchemaRevision`（暂 0，P1 置 12）、三项目 `--version`（version/commit/buildTime/schemaRevision）+ gateway readiness 双面增键 + jobs health payload 增键 + 两常驻进程启动身份首行、deploy.sh/package-release.sh/ps1 ldflags 注入 + tag 脏树守卫（ALLOW_DIRTY_BUILD 豁免）、Dockerfile.go-project ARG 注入、compose `JUHE_AI_IMAGE_TAG` 参数化、CHANGELOG 骨架、部署指南构建示例与 single-server README env 契约同步。验证：三项目 `go build ./...` + `go vet ./cmd/...` + 三 cmd 包测试全绿；`go run -version` 冒烟自报 `version=dev commit=none buildTime=unknown schemaRevision=0`；脚本 `bash -n` 过。已知边界：并行会话同窗口删除 provider-connection 迁移壳（main.go 同文件两批改动共存、构建测试均过），设计 §10「15 个命令」计数将在 P1 实施时以届时现状为准；首个 tag 由维护者打。 | 三项目构建/vet/定向测试 + 冒烟 | — |
| 7 | 用户问询补强：§2.2 新增非目标 5「不做自动升级」——升级仅由用户显式执行 `upgrade`（deploy.sh upgrade 为同一命令的脚本封装）；拉代码/重建/up/重启不得触发结构收敛、迁移或 seed；启动门禁只读检查，与 Grafana 式启动自动迁移刻意相反。 | 主代理自查 | — |

## 裁决清单（C 类，持续积累）

| ID | 一句话 | 来源轮次 | 状态 |
| --- | --- | --- | --- |

## 终报（收口时写）

- **终止原因**：出口②——烈度单调走低后的提前收口（收敛协议未走满 3 个连续干净轮，如实声明）。R1 确认 51 项（含设计级 D5 序号矛盾、D1 golden 冲突）、R2 确认 17+回归 6、R3 确认 7+终态复核 4，全部为文档同步/契约完备类；R3 终态复核判定「一轮定点修复后可收口，无需升级终审档」，批 5（I 系列）修复后未再派独立复核轮——最终态经主代理 15 项不变量自查 + 步号连续性核验，未做独立终验，属本收口的已知边界。
- **扫描范围与轮次**：锚定《升级兼容与迁移账本设计》（docs/functions/升级兼容与迁移账本设计.md）+ 两个索引文件。R1 维度制（6 攻击 → 85 假设 → 7 验证 → confirmed 51/refuted 27）；R2 场景制（10 入口攻击 → 18 假设 → 2 验证 → confirmed 17/refuted 1）+ 落点复核轮（34 修复条目，33 落点正确 + 回归 REG1-6）；R3 交互制（10 机制对 → 10 假设 → 1 验证 → confirmed 7/refuted 2/dup 1）+ 终态复核轮（7 交叉对/15 实现断言/4 路径活性）。视角三轮互不相同。
- **处置统计**：A 类即修 5 批共 79 项（批 1=51、批 2=6 回归、批 3=17、批 4=4、批 5=7[去重 6]）；B 类 0（被审对象为未实施设计文档）；C 类裁决清单 6 项（§20.1-20.6）；证伪累计 30 项（含攻击方对代码现状的 13 处转述失实被驳回——S9/A1/F7/F13/D9/D10/D11/D13 等）；交叉印证 5 组。
- **修复与验证证据**：批 1-4 经独立落点复核/终态复核背书；批 5 主代理自查（15 不变量 + 步号连续）。设计文档现为 v3，待办 #T78 与 functions/README 索引同步。
- **移交清单**：① §20.1（§5.1 边界澄清+条文改写）与 §20.2（破坏性变更延后一版）为实施前置裁决；② §20.3（schema 创建并入 Apply）、§20.5（备份最低保障）、§20.6（CI 载体）随分期裁决；③ C8/E7 的 .local runbook 与 Windows 直跑文档在实施批交付；④ `scripts/assert-migration-version-compatibility.ps1` 随账本 golden 落地删除；⑤ 批 5 未经独立复核——实施 P1 前建议对 v3 契约做一次轻量终验。
- **未提交改动清单**：本会话产物 = `docs/functions/升级兼容与迁移账本设计.md`（新文件，v3）、`docs/plans/deep-dive-review-升级兼容与迁移账本设计-20261030.md`（新文件）、`docs/functions/README.md` 与 `docs/待办与阻塞事项.md`（索引/T78 登记，混有并行会话指针行）；工作区其余脏文件属并行会话在制品，未触碰。
