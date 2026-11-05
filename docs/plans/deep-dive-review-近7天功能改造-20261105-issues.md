# 问题记录：近 7 天功能改造深挖复审（2026-11-05）

> 对应台账：`docs/plans/deep-dive-review-近7天功能改造-20261105.md`
> 授权模式：report-only。下列 A/B/C 均为建议分级，未执行修复。
> 基线：`a4c1d07d7`（2026-11-05），待发布候选 `v2026.11.03..HEAD`。

## R1 汇总

- 攻击轮：8 个功能角色，产出约 130 条假设。
- 验证批：12 条高价值假设送独立证伪（另 8 条定向验证中）。
- 确认：见下表；证伪：见「证伪项」；待定：见「待定项」。

### 确认项（按发布风险排序）

| ID | 功能面 | 一句话 | 影响 | 建议 | 证据 |
| --- | --- | --- | --- | --- | --- |
| DR-01 | VND | purge 先删 `resource_authorizations`，授权实例/`group_accounts` 外键 NO ACTION 阻断删除 | 高·阻断 | A | `purge_unsupported_vendor_accounts.go:499-517`、`:546-552`；`pg_schema_business_tables.go:663`、`:1201` |
| DR-02 | VND | `liveIDs` 为空时跳过 manifest 漂移校验，按旧 manifest 删除已被修复为真实厂商的账户 | 高·不可逆 | A | `purge_unsupported_vendor_accounts.go:216-225`；`purgeDeleteRows:546-551` 未复用谓词 |
| DR-03 | OPS | T114/部署指南的 `/backup` 在 maintenance 容器内不存在或 root:root 0755，app 用户不可写 | 高·阻断 | A | `docs/deploy/部署指南.md:418-421`；`compose.yml:248-253`；`Dockerfile.runtime:29`；`purge_unsupported_vendor_accounts.go:365-374` |
| DR-04 | RDS | hash-tag namespace 使 J3b 电路索引键全变；`deploy.sh all/upgrade` 无 `-init-account-circuit-runtime-index`，gateway `CheckReady` fail-fast 退出 | 高·阻断 | A（T125 已登记迁移，但 deploy.sh 未接线是回归） | `main.go:318-320`；`circuit_runtime_init.go:17-95`；`deploy.sh:354`；`docs/deploy/config/README.md:106` |
| DR-06 | WM | 自举在 cursor 存在但 0 行 `<= cursor` 时仍返回 `true`，正常选批永不执行；`last_success_at` 每轮刷新 | 高 | A | `statsagg/consumption.go:116-167`；`aggregate.go:107-124`；`clientipagg.go:76-97` |
| DR-09 | WM | `DeadLetterCount` 只增不减，>0 时 ingestgate 永久阻断三个统计任务；死信批移出队列且不落盘，重启只清计数 | 高 | A | `usagewriter/writer.go:545-570`、`:669-682`、`:798-834`；`internal/ingestgate/ingestgate.go:133-135`；`usagespooldrain/drain.go:264-270` |
| DR-12 | SCH | 非法计划行 `next_check_at` 为 `''` 或 NULL，`IS NULL OR <= now` 恒真，永久占用候选；≥500 条时饿死正常计划 | 高·条件 | A | `jobs/internal/oauthrefresh/availability.go:175-183`、`:260-268`；`pg_schema_business_tables.go:618`、`:1317` |
| DR-13 | SCH | 单条非法 Key 计划使列表/详情 500，PATCH 先解析旧计划拒绝，UI 无法清空；空白串语义与 jobs/accounts 不一致 | 高 | A | `apikeys/patch.go:492-498`；`apikeys/store.go:802-808`、`:907-911`；`apikeys/schedule.go:142-153`；`apikeys/routes.go:138-141` |
| DR-16 | OPS | `juhe_app_job_stall` 取最后 `finished_at` 未筛 `status='completed'`，持续失败任务刷新“上次成功”，`AppJobStalled` 永不触发 | 高·观测盲区 | A | `monitoring/collector/queries.yaml:26-32`；`postgres-exporter-queries-additive.yaml:82-88`；`taskruns/store.go:309-319`；`runner.go:369-392` |
| DR-10 | STR | 精确画像 + `Skipped` + `completed` + `DataEventObserved` 时按成功结算，不要求 `TerminalReceived`；客户端截断无重试 | 中高 | A | `gatewayresponse/pipefinal.go:494-496`、`:667-668`；`gatewayopenai/inspector.go:301-358`；`finalize.go:477` |
| DR-05 | RDS | J3b 登录守卫用根 namespace，主管理面用 `{ns:authstate}`；失败计数/锁定窗口分裂（10-31 复审已修，本批回归） | 中·安全 | A | `main.go:552`；`compose.go:673`；`authsys/shared_runtime_state.go:68-75` |
| DR-14 | OPS | 提交 a4c1d07d7 声称删除 `juhe-ai-app-overview.json`，实际 R099 重命名到 stack/dashboards，仍被 provisioning 加载；文档仍称核心面板 | 中·交付不符 | A（提交/文档） | `git show a4c1d07d7 --name-status`；`provider.yaml:5`；`docs/deploy/monitoring/README.md:167` |
| DR-15 | SCH | DST 春季跳变不存在的本地分钟被丢弃，`next_check_at` 直接跳到 04:00；03:00–03:59 有效窗口段 gate 保持 0（默认 Asia/Shanghai 不触发） | 中·条件 | A | `jobs/internal/oauthrefresh/schedule.go:713-717`、`:733-754`；`availability.go:258-261`；`w14b_schedule_family_test.go:53-56` |
| DR-07 | WM | 时间保留 SQL 不检查两个水位列；cursor 已覆盖但水位为 NULL 的晚到行会被删/整日分区 DROP；与设计 :415 不一致，属文档冲突 | 中 | A（先裁决文档） | `cleanuprepo/dataretention.go:754-762`、`:905-906`、`:636-646`；`统计指标与分层聚合设计.md:383` vs `:415` |
| DR-17 | STR | oversized 输出提示正则只判键存在，`response.created` 的 `output:[]`/`text` 被当作可见输出，绕过 W32 空完成检测、提前语义提交 | 中·条件 | A | `gatewayopenai/inspector.go:532`、`:442-445`；`pipefinal.go:596-605`；`precommit.go:58-73`；`客户端稳定性竞品对比.md:81` |

### 证伪项（保留一行，不删）

| ID | 原假设 | 证伪依据 |
| --- | --- | --- |
| DR-R1 | purge 无整段事务导致“manifest 重跑无法恢复” | 分批/可重入是文档明示设计，恢复路径是整库备份；`purge_unsupported_vendor_accounts.go:11`、`:472-473`；`docker/single-server/README.md:28` |
| DR-R2 | purge 的 `account_id IS NULL AND provider_code IN ('openai','hybrid')` 删除范围外历史 | 该分支是退役厂商历史清退路径，真实厂商历史按 manifest ID 保留；`purge_unsupported_vendor_accounts.go:430-435`；`供应商与上游协议面重构设计.md:21`、`:491` |
| DR-R3 | `finalizeParserSkipped` 成功臂未 flush 导致客户端 0 字节 | pre-commit 缓冲不变量保证触发 Skipped 的 >256KB chunk 必先 flush；`precommit.go:58-73`；`pipe.go:332-358` |
| DR-R4 | usage-stats 无 `FOR UPDATE` 导致双实例重复消费 | 生产路径由 `withLease` + `pg_try_advisory_xact_lock` 全局租约包裹；`worker_assembly.go:1009-1019`、`:1061-1064`；`taskruns/store.go:527-613` |
| DR-R5 | 不带 `--profile tool` 的 `build gateway jobs maintenance` 不构建 maintenance | 显式服务名会启用对应 profile；`deploy.sh:69-70`、`:325` |
| DR-R6 | Grafana provisioning 只 upsert，不删除旧面板 | `disableDeletion:false` 时 Grafana 会删除缺失的 provisioned dashboard；`provider.yaml:11` |
| DR-R7 | 坏行被标记后扣减统计形成低计/MAX(0) | `ShouldAggregateUsageStatsRecord` 对同一批坏行返回 false，扣减提前 `continue`；`statssubtractpostgres.go:615-619`；`record.go:183-187`；`w13e_pg_arms_test.go:71-90` |
| DR-R8 | 对账窗口永久排除 pending 小时形成静默盲区 | 正常 pending 消费后窗口前进；真卡住有 `AppStatsCursorStalled`（180m/15m）兜底；`reconcile.go:142-178`；`juhe-ai-alerts.yml:82-88` |
| DR-R9 | 单窗口 `00:00–23:59` 无法表达真正 24h | 两段相邻窗口可表达；仓库有 `alwaysAllowSchedule` 夹具；`m09_test.go:12-17`；`schedule.go:301-318` |
| DR-R10 | NFKC 去索引化是未登记语义回归 | 文档已明确“不做 NFKC 归一化”为契约；`接口契约与权限矩阵.md:206`；`核心功能设计.md:173`；`问题-0349.md:69` |
| DR-R11 | pgx 参数化导致 pg_trgm 抽不出三元组/索引不可用 | pg_trgm 在扫描初始化按实际参数值抽三元组；生产 1211 行 `%林%` 走 index-only scan 约 1.3ms；`问题-0349.md:70` |

### 待定项

| ID | 原假设 | 待补证 |
| --- | --- | --- |
| DR-P1 | PRC 桥接流式 usage 与原生不一致（Gemini→Chat thinking 丢失；Chat/Anthropic→Gemini 重复计） | 待最终四态；已有代码证据指向 confirmed（仅流式） |
| DR-P2 | 映射后 `upstream_model` 使 gateway 计价 miss，jobs 是否回填 | 待 spool→jobs 路径与 `models.upstream_model` 数据验证 |
| DR-P3 | 授权实例测试目录 owner 回落 grantee | 待最终四态 |
| DR-P4 | BUG-0353/0354 验收证据虚高（测试名不存在/字节断言缺失） | 待最终四态 |
| DR-P5 | CHECK 允许空串/非九家 equal pair 与未知 vendor 仍可派发 | 待最终四态 |
| DR-P6 | 统一 Redis URL 取消三实例隔离、迁移清零运行态 | 待最终四态 |
| DR-P7 | PRC tier-only/cache-only 目录行可绑定导致成功流量零成本 | 待最终四态 |
| DR-P8 | OPS 告警 `absent()` 缺失、慢日志 `SLOWLOG GET` 默认 10 条 | 未送验（预算） |

## 覆盖结论（R1）

- SCH：已查非法计划边界/写面覆盖/CAS/读取面/列表/迁移/并发/测试；确认 DR-12/DR-13/DR-15；H5 证伪（两段窗口可表达 24h）。
- WM：已查自举/事务/双聚合器/保留/安全截止/死信/重建/对账/索引；确认 DR-06/DR-07/DR-09；重复消费与坏行扣减证伪。
- VND：已查谓词/删除完整性/manifest/停写/vendorinfer/残留/前端/CHECK；确认 DR-01/DR-02；非事务与 null-account 删除证伪。
- NSI：已查转义/语义/性能/退役命令/残留/测试；NFKC 为已登记 B 类契约变化，性能指控证伪；跨页关键字归一化差异保留为低影响 B 类。
- RDS：已查 key 迁移/DomainNamespace/UniversalClient/env/三实例/集群/可观测/测试；确认 DR-04/DR-05；三实例隔离与运行态清零待定。
- PRC：已查 cache_write 族判定/桥接 output/无价判据/回填/档位；桥接流式口径待最终判定，tier-only 待定。
- STR：已查 oversized 状态/前缀分类/终止/字节完整性/finalizeSkipped/owner/测试；确认 DR-10/DR-17；flush 与 ParseErrorEventCount 证伪/重复。
- OPS：已查 compose 挂载/env/监控悬空/采集/安装/健康检查/文档；确认 DR-03/DR-14/DR-16。

---

## R2 场景制攻击轮（2026-11-05）

### 新增确认项（按发布风险排序）

| ID | 功能面 | 一句话 | 影响 | 建议 | 证据 |
| --- | --- | --- | --- | --- | --- |
| DR-18 | 账户入口 | **已在生产**：前端 OAuth 公共载荷恒发 `rpmLimit`，后端 oauthmgmt strict-body 白名单与 Store 无该字段；四个 plan 的托管建号/克隆/SSO 导入全部 400 | 极高·生产已断 | A（热修） | `accountSavePayload.ts:460`；`oauthmgmt/routes.go:304-312`、`:628`；`oauthmgmt/store.go:516-538`；发布记录 v2026.11.03 含前端该行且 oauthmgmt 未改 |
| DR-19 | 发布序列 | compose 无 `stop_grace_period`，Docker 10s 后 SIGKILL；gateway 串行 shutdown 10s+5s+5s（最坏 20s），在途 SSE 的 usage/审计未构造即丢 | 高·发布窗口 | A | `compose.yml:89-232`；`main.go:1056-1088`；`deploy.sh:329` |
| DR-20 | 发布序列 | purge 不清理 usage spool/overflow spool；停写残留文件在 `up -d` 后被 jobs 回放，已清退账户 usage 重新入库（usage_records 无 account FK） | 高·清退失真 | A | `purge_unsupported_vendor_accounts.go` 全文无 spool；`usagespooldrain/drain.go:173-285`；`usagewriter/store.go:188,212-215`；`pg_schema_usage.go:82-152` |
| DR-21 | 发布序列 | `upgrade --dry-run` 在 dbRevision < SchemaRevision 时恒返回 blocked/3；T114“确认无 blocked”不可满足；dry-run 不执行 EnsureAdditive，看不到 CHECK 冲突 | 中·流程阻断 | A（文档/指令） | `upgradepipeline/pipeline.go:343-349`；`upgrade_pipeline_integration_test.go:41-49`；T114 第 5 步 |
| DR-22 | 发布序列 | revision 17 CHECK 在加法相执行、`schema_state` 最后写；后续步骤失败留下“CHECK 已在库、state 仍旧值”，旧二进制通过门禁但 generic 写入撞 CHECK | 中 | A | `pipeline.go:376`、`:459`；`pg_schema_business_tables.go:2429-2444`；`schemagate.go:71-79` |
| DR-23 | 发布序列 | T114 手工构建路径不经 deploy.sh 前端漏发门禁/产物校验；`deploy.sh maintenance` 只上传 maintenance；README 声称的 `JUHE_AI_IMAGE_TAG` 注入不存在（T84-10 已登记） | 中高·可静默旧前端 | A | `deploy.sh:69-71`、`:221-230`、`:314-325`；`Dockerfile.runtime:20-21`；`README.md:334` |
| DR-27 | 派发/RPM | RPM 只在成功 TryAcquire 时计数；同账户瞬态重试、普通 Key 轮换、OAuth 401 刷新、多 URL 轮询都不重新计数，单请求可多次真实出站 | 中 | A（口径裁决后） | `dispatchsingle.go:141-166`、`:234-267`、`:430`、`:475-477`；`accountconcurrency.go:115-148`；设计 `核心功能设计.md:138` 与 `AI账户每分钟请求上限设计.md:58-63` 口径冲突 |
| DR-28 | 计量 | gateway_summary_compact 成功只 finalize 审计、不写 usage；摘要 token 零计量，审计无 token 字段无法回补（失败面反而写 usage） | 中 | A | `compactpreflight.go:365-375`、`:478-507`；`audit_capture.go:910-935`；`chain_ports.go:2558-2583`；`Responses上下文压缩落地方案.md:66,205,329` |
| DR-29 | 审计 | 媒体创建 2xx/参数 4xx 透传/2xx 解析失败三类走 AlreadyFinalized，不 finalize 审计；创建成功不写 usage（usage 推迟到终态轮询，属设计） | 中 | A（审计面） | `chain_v1.go:655-660`、`:905-911`；`chain_media_video.go:1101`、`:1110-1128`、`:1045-1054`；`原始审计日志设计.md:56` |
| DR-30 | 闸门 | jobs 电路恢复探针 `LoadAccountForGroup` SQL 无 `availability_schedule_gate`，时段外仍发真实上游诊断；speed-first 恢复探针同族 | 低 | A | `worker_circuit_jobs.go:196,219`；`proberepo/reader.go:527-556`；`speedfirstprobe.go:148-167` |
| DR-31 | Redis | gateway 写 latency degradation 到 `{ns:proxyhealth}`，jobs 读 `{ns:speedfirst}`；jobs 恢复候选恒 0，降级只能自然恢复，且 jobs 持续写无人读域 | 中高 | A | `chain_runtime.go:685`；`worker_probe_jobs.go:149`；`speedfirstrepo/speedfirst.go:33`；`接口契约与权限矩阵.md:409` |
| DR-32 | Redis | gateway 写 policy avoidance 到 `{ns:keymodel}`，jobs 读 `{ns:circuit}`；列表投影把避让账户标为 available 并清 dirty | 中 | A | `chain_runtime.go:745`；`worker_projection_jobs.go:71`；`listavailability_runtime.go:97`；`listavailability_effective.go:182-203` |
| DR-33 | Redis | gateway J3b projector 与 jobs control-plane maintenance 同表 Claim outbox，无 projection/shard 过滤；先到者 ack 后另一侧漏事件（BUG-0295 已见 J3b 漏收） | 中高 | A | `main.go:332`；`circuit_projector/service.go:58,74`；`opsjobs/controlplane.go:347,359`；`circuit_control_plane/store.go:588` |
| DR-34 | Redis | Redis 故障时核心守卫 fail-closed 无统一 breaker；single 客户端保留 5s dial/3s IO/3 retries 且 ContextTimeoutEnabled=false，挂起可把 preflight 的多个串行 GET 放大到 15s+ | 中高 | A（评估降级层） | `preauth.go:151-160`；`preflight.go:409-415`；`upstreamdispatch.go:1032-1038`；`rediscfg/factory.go:159-180`、`:61-63` |
| DR-35 | 统计 | `--rebuild-usage-stats` 清空 totals/daily/weekly/monthly，只回放 raw（默认 30 天）；同 cap 重跑再次清空且不推进；命令自身无备份/续跑 | 高（人工执行时） | A（文档/安全阀） | `statsrebuild.go:80-134,275-307,336-371`；`policy.go:48-57`；`rebuild_usage_stats.go:91-96` |
| DR-36 | 账户入口 | 前端编辑/克隆只提交健康检查那一条 connections，后端按集合整替换；多连接账户（deepseek/glm/gemini 同域多 preset）未提交连接被删或因映射/健康检查引用 400 | 中·条件 | B（多连接尚未上线） | `accountSavePayload.ts:355-365`；`accountEditPatch.ts:40-50`；`connections.go:281-392,485-507`；生产 0 个多连接账户 |
| DR-37 | 账户入口 | rpmLimit 只校验非负整数，不限制 int4；超 2147483647 在 create/PATCH/批量路径 500；前端硬限 9999、公开 API 上限 1,000,000 三方不一致 | 中低 | A | `routes.go:1138-1145`；`patch.go:614-617`；`batch.go:1874-1878`；`pg_schema_business_tables.go:612`；`AccountEditModal.vue:319` |
| DR-38 | 部署监控 | 提交 a4c1d07d7 声称删除应用总览面板，实际 R099 重命名；文件仍被 provisioning 加载，文档仍称核心面板（同 DR-14，补充证据） | 中 | A | 同上 |
| DR-39 | 导入 | 导入弹窗四个来源示例均无 models/supportedModels，后端因来源模型列表缺失逐条跳过；复制示例 0 条可导入 | 中 | A | `AccountImportModal.vue:294-331`；`import_source.go:124-128,802-805` |
| DR-40 | 克隆 | 克隆媒体账户只回探针表可派生 profile，openspeech/ark 不在表内，前端回落 vendor 默认 ark → 组合校验 400 | 中·条件 | A | `clone.go:243-247,372-375`；`accountEditFormLoaders.ts:193-204`；`accountOptions.ts:379-393` |

### R2 证伪/重复

| ID | 原假设 | 结论 |
| --- | --- | --- |
| DR-R12 | 媒体 poll/content/cancel 绕过 gate/RPM 是缺陷 | refuted：任务面按设计“受理后账户亲和直连”，不进派发循环；`音频视频模型接入与统一媒体网关设计.md:86,137` |
| DR-R13 | playground chat-pinned 绕 gate 是新问题 | duplicate：T112 已知边界② |
| DR-R14 | 仅 credentials.base_url 的 PATCH 不同步连接是新问题 | duplicate：BUG-0341/T96 同族；受支持前端与公开 API 已提交 connections |
| DR-R15 | gateway /health 不探测 Redis | refuted：J3b circuit owner 每秒 CheckReady（PING+HMGET），硬停时 /health 会 503；但挂起形态仍可能长期 healthy |
| DR-R16 | 30M 行导致自举/标记超时 | refuted 规模前提：生产 306,548 行、单日分区 25,205 行；30M 需约 100 倍流量。自举 UPDATE 无 LIMIT 机制成立，20s 是否足够未实测（undetermined） |
| DR-R17 | 毒行 created_at / 空 id / 重复 id 致统计永久停摆 | refuted：写入面强制 RFC3339、id 随机 UUID 且 ON CONFLICT，生产 0 重复/100% 规范；仅库外损坏可触发 |
| DR-R18 | gateway 映射模型计价 miss 致 cost_usd NULL | refuted：spool→jobs FreezePricing 以 requestedModel / models.upstream_model 回填；仅两侧都 miss 时保持 NULL |
| DR-R19 | 后端白名单允许 rpmLimit 但 Store 丢弃（仅静默） | refuted 作为独立项：白名单也拒绝，先 400；Store 丢弃是修复时必须同时补的第二处 |

### R2 待定项

| ID | 原假设 | 待补证 |
| --- | --- | --- |
| DR-P9 | 生产备份 pg_dump 的 `--exclude-table-data` 口径与 T118 需要的 audit/stats 回滚点冲突；`pg_restore -l` 只验 TOC 不验数据 | 需核对窗口实际备份命令与备份机脚本 |
| DR-P10 | purge apply 在部分删除后 manifest 哈希必不相等，文档“同一 manifest 续跑”不成立 | 需确认是否已接受“重新 dry-run”为唯一续跑方式 |
| DR-P11 | `real_account_count` 把授权实例计入，清退后与 T114 验收基线不一致 | 需确认授权实例是否在清退集合内为设计 |
| DR-P12 | 前端 providerDisplay 缺 minimax/volcengine/qwen；帮助文档仍写 OpenAI only；导入协议三家缺示例 | 影响面已知，未逐页验证 |
| DR-P13 | 授权实例列表按实例 gate(NULL) 显示可用、不读来源 gate；实例 PATCH 允许写计划 | 未送验 |
| DR-P14 | 公开 API update 多连接整替换、CAS 重试旧快照覆盖 | 未送验 |
| DR-P15 | J3b `/auth` 验证码用进程内 store，与主面 Redis captcha 分裂 | 未送验 |
| DR-P16 | namespace 长度 56-64 通过 raw 校验、拼 domain 后超 64 导致启动失败 | 未送验 |
| DR-P17 | 新增部分索引在存量分区表非 CONCURRENT 创建，停写窗口/磁盘风险 | 未送验 |
