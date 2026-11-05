# 深挖复审台账：近 7 天功能改造（待发布批 v2026.11.03..HEAD）

- 锚定范围：以 `v2026.11.03..HEAD` 待发布候选（13 个提交 / 821 个文件 / +29948 −13753）为主体，覆盖近 7 日（2026-10-30～2026-11-05）已发布但尚无生产验证的功能面作为回归与扩散面：AI 账户 RPM 限频（schemaRevision 14）、桥接 usage 快照与结算供给、AI 账户后台自动探测总开关、account_health_outcomes 保留、my-chat 会话面退役、熔断计数后台队列。
- 发起：2026-11-05，用户显式发起（"最近改动功能特别多 我需要你深度的排查下…最近7天的各种功能的改造这样我才好上线"）；授权模式：**report-only**（未授权修复；A/B/C 仅为建议分级，不改源码、配置、项目测试与文档，本台账与问题记录除外）。
- 问题记录：`docs/plans/deep-dive-review-近7天功能改造-20261105-issues.md`
- 收敛预算：最多 5 轮，单轮并行攻击角色 ≤8、验证角色 ≤6；触顶按中止处理，不得声称收敛。
- 需求脉络：
  - 目标：对最近 7 天的功能改造做上线前穷尽式复审，产出经证伪确认的缺陷 / 回归 / 文档失同步 / 测试缺口清单，并按 A/B/C 给出处置建议。
  - 非目标：不实施修复；不重写架构；不重复处理已登记的运营观察项（W17/W37/W42/W43/W44/W46 等）与产品裁决项（C1 代理池）。
  - 可见行为：`/v1/*` 网关调度与流式行为、管理面账户/API Key/统计/模型目录、jobs 聚合与数据保留、maintenance 升级/清退/退役 CLI、部署与监控产物。
- 涉及文件（初始热点）：`gateway/internal/gatewayrouting/routeselector.go`、`maintenance/cmd/juhe-ai-maintenance/{purge_unsupported_vendor_accounts,drop_retired_account_name_search_index}.go`、`shared/platform/rediscfg/factory.go`、`jobs/internal/oauthrefresh/availability.go`、`gateway/internal/accounts/{schedule,patch,list,write,entry_mappings}.go`、`gateway/internal/apikeys/{schedule,patch,store}.go`、`jobs/internal/statsagg/*`、`jobs/internal/statsverify/*`、`gateway/internal/pricing/billing.go`、`gateway/internal/gateway{openai,anthropic,gemini}/*streaminspection*`、`gateway/internal/accounts/accountstest/options_service.go`、`docker/single-server/compose.yml`、`docker/single-server/monitoring/**`。
- 关联契约：
  - [时间计划闸门与账户状态解耦设计](../functions/时间计划闸门与账户状态解耦设计.md)（§3 数据模型、§4 写入语义、§5 读取语义、§7 存量迁移）
  - [统计指标与分层聚合设计](../functions/统计指标与分层聚合设计.md)（§消费水位，:377-408、:415-425、:436、:455-459、:660-661）
  - [供应商与上游协议面重构设计](../functions/供应商与上游协议面重构设计.md)（§1.1 注册表、§2 账户与连接面、§9 目标 schema）
  - [AI账户每分钟请求上限设计](../functions/AI账户每分钟请求上限设计.md)（§3 字段、§4 计数口径、§5 调度语义、§7 schema）
  - [模型价格与用量统计口径](../functions/模型价格与用量统计口径.md) §3.2；[BUG-0347](../bug/问题-0347-上游缓存写入token与输入token重复计量致成本虚增.md)、[BUG-0345](../bug/问题-0345-晚到写入的用量记录被统计游标越过致日统计永久漏计.md)、[BUG-0344](../bug/问题-0344-无价目录行可绑定可调度致成功流量零成本.md)、[BUG-0343](../bug/问题-0343-成功请求用量记录生效档位与思考级别恒记客户端请求值.md)
  - [AI账户检查模型与人工测试设计](../functions/AI账户检查模型与人工测试设计.md) §5/§8；[BUG-0352](../bug/问题-0352-人工测试目录跨用户个人模型遮蔽致OAuth账户无请求形态.md)、[BUG-0353](../bug/问题-0353-Responses超大终止事件复用上一条事件类型致流误判失败.md)、[BUG-0354](../bug/问题-0354-跨协议超大SSE事件整流跳过致终止失败丢失.md)
  - [接口契约与权限矩阵](../functions/接口契约与权限矩阵.md)、[部署指南](../deploy/部署指南.md)、`docker/single-server/README.md`、[升级兼容与迁移账本设计](../functions/升级兼容与迁移账本设计.md) §6.4/§7.4
- 项目裁决出处：根 `AGENTS.md`（文档优先、证据决策、开发规范）；项目 `AGENTS.md`（文档与代码一致性、部署变更同步、私有环境边界）；`docs/待办与阻塞事项.md`（T114 统一上线批、T105-T124、B4-B6、C1；开工前已通读）。
- 代码基线：`a4c1d07d7`（2026-11-05），工作区 clean，无并行脏文件。上一发布标签 `v2026.11.03`（`contracts.SchemaRevision=14`）；HEAD 为 `17`，本批新增 revision 15/16/17。
- 工作区边界：无并行会话占用；`docs/bug/` 档案编号已至 0354，本台账条目统一 `DR-` 前缀，不占用 B/C/T/W 编号。
- 当前阶段 / 当前轮次：R2 场景制攻击轮与定向验证已完成；R3 交互制未执行（预算触顶）。
- 收敛计数：0/3（R1 确认 17 项，R2 新增 23 项；严重度未下降，不满足任何收敛条件）

## 覆盖矩阵（R1，维度制；列 = 功能面）

R1 将待发布批拆为 8 个可独立验证的功能面：

| 列 | 功能面 | 主要提交 |
| --- | --- | --- |
| SCH | 时间计划闸门（accounts/api_keys availability gate，T112） | `dd432b6a3`（合并提交内含） |
| WM | 统计消费水位（usage_records 双水位 + 自举 + 保留过滤，T108/BUG-0345） | `dd432b6a3` |
| VND | 九家真实厂商收口 + 通用连接器/generic_http 退役（T118，revision 17） | `892cd3de6` |
| NSI | 账户名称搜索去索引化（T119） | `892cd3de6` |
| RDS | Redis 配置统一 `JUHE_AI_REDIS_URL` + DomainNamespace | `892cd3de6` |
| PRC | 计价与用量口径（T106 BUG-0344 / T107 BUG-0347 / T105 BUG-0343 回填 / 桥接 usage） | `dd432b6a3`、`3b4b0d68f`、`f880690a9`、`3d3ab1e32` |
| STR | 跨协议超大 SSE 事件 + 人工测试目录 owner 作用域（T123/T124/BUG-0352） | `b37b7c8e2`、`d0f9ce284` |
| OPS | 部署与监控（compose 挂载、监控栈、告警、env 契约） | `a4c1d07d7`、`ab4c80f7d`、`624e2e113`、`892cd3de6` |

行维度：状态一致性、并发与时序、取消与中断、错误路径与边界、资源与生命周期、数据流与契约一致、外部输入、回归与兼容、性能与放大、测试有效性。

矩阵结论见「问题记录」对应轮次；本轮收口时回填。

## 扩散记录

| 轮次 | 新纳入对象 | 耦合类型 | 理由 |
| --- | --- | --- | --- |

## 修复记录

report-only 模式，不执行修复。

## 裁决清单（C 类，持续积累）

| ID | 一句话 | 来源轮次 | 状态（待裁决/已裁决+结论） |
| --- | --- | --- | --- |

## 终报（收口时写）

## R2 覆盖记录（场景制）

| 入口组 | 覆盖结论 | 主要确认 |
| --- | --- | --- |
| 发布窗口序列（停写→备份→构建→purge→upgrade→drop→up→verify） | 已逐步推演 | DR-19/20/21/22/23；备份/manifest 三项待定（DR-P9/10/11） |
| 账户管理入口矩阵（创建/编辑/克隆/批量/导入/公开 API/授权实例/OAuth/Key） | 已查字段矩阵、RPM、闸门、vendor、连接域、错误面 | DR-18/36/37/40；实例列表/实例计划、公开 API CAS 待定（DR-P13/14） |
| 网关派发 × 统计交互（RPM/闸门/冷却/亲和/重试/桥接/用量/审计/媒体） | 已查主要组合 | DR-27/28/29/30；桥接流式口径 R1 已确认；媒体任务面为设计（证伪） |
| Redis × 启动/停机/故障/集群/限流/登录/监控 | 已查域命名空间、故障面、健康、超时、监控 | DR-31/32/33/34；J3b captcha/namespace 长度待定（DR-P15/16） |
| 前端契约适配（账户/Key/模型广场/导入/帮助） | 已查字段、空态、错误、九家覆盖 | DR-39 与 providerDisplay/帮助文档待定（DR-P12）；基础卡片/typecheck 不在本环 |
| 数据演化与边界（空库/存量/规模/时区/编码/升级/重建/保留） | 已查冷启动、存量形态、306k 行规模、重建、保留 | DR-35；备份口径/索引创建待定（DR-P9/17）；30M 规模前提证伪 |

## 扩散记录（R2）

| 轮次 | 新纳入对象 | 耦合类型 | 理由 |
| --- | --- | --- | --- |
| R2 | `jobs/internal/usagewriter`、`usagespooldrain`、`overflowspool` | 共享文件/跨进程交接 | T114 停写与 purge 后的 spool 回放 |
| R2 | `shared/platform/rediscfg` + gateway/jobs 各域装配 | 共享可变状态 | Redis 单 URL 化后跨进程域命名空间错配 |
| R2 | `gateway/internal/business/circuit_*` 与 `jobs/internal/opsjobs/controlplane` | 共享 outbox 表 | 双消费者 Claim 同一 pending 行 |
| R2 | `jobs/statsrebuild` 与 retention policy | 共享长期统计面 | 重建清空范围与 raw 保留期不匹配 |

## 终止说明

本环**未收敛**：R1/R2 连续两轮均产出新的高/中影响确认项，严重度未下降，未满足 report-only 的“连续三轮无新确认”收敛条件，也未执行 R3 交互制与终态复核轮。按预算触顶中止，终报列明已覆盖面与未覆盖项；不得把本结果表述为“已收敛/无隐藏缺陷”。
