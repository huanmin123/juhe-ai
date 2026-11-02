# 深挖复审台账：网关（backend-go/projects/gateway）

- 锚定范围：对 `backend-go/projects/gateway`（cmd/juhe-ai-gateway 组合根 + internal 全部包）做深挖质量复审，重点 `/v1` 网关主链（入口保护、认证、调度派发、流式管道、用量/审计、OAuth 凭据生命周期），随扩散规则向耦合面扩一跳。
- 发起：2026-10-31，用户显式发起（"帮我把项目的问题找找吧 从网关开始"）；授权：deep-dive-review 全语义——A 类即修权已声明（修复后行为与契约一致或合法用户无感知，即修即登记），B 类过目后修（每轮收口呈报，批后执行），C 类裁决清单（不进修复流、不计收敛）。
- 需求脉络：
  - 目标：对网关域做穷尽式深挖，产出经证伪确认的缺陷/回归/文档失同步/测试缺口清单，A 类即修、B/C 类移交裁决。
  - 非目标：不重写架构、不做供应商重构（以《供应商与上游协议面重构设计》目标契约为准但不在本环实施）、不处理已登记的运营面观察项（W17/W11/W32/W37 等外部渠道质量问题）。
  - 可见行为：网关所有对外行为（`/v1/*`、`/__aisys__/api/*`、`/__aipublic__/*`、chat、metrics）的正确性、稳定性与契约一致性。
- 涉及文件（初始清单）：`backend-go/projects/gateway/cmd/juhe-ai-gateway/**`（组合根与链路接线，4.07 万行）、`internal/{gatewaypreauth,gatewayclientip,ratelimit,gatewaycacherate,gatewaycallertier,gatewaydispatch,gatewayrouting,gatewayruntimecache,gatewayhotquality,gatewaycircuit,gatewayproxyhealth,gatewayresponse,gatewayopenai,gatewayproto,openaicompat,gatewayusage,auditlog,operationlog,statreads,publicapilogs,oauthmgmt,accountkeystates,gatewayaccounteffects,gatewaybody,gatewaycodex,gatewaygemini,gatewayanthropic,apikeys,authsys}` 等。
- 关联契约：
  - [架构总览](../architecture/架构总览.md)：路由层级 `API Key → 路由策略 → 分组（普通/超级）→ AI 账户 → 供应商/协议`；请求侧/返回侧分层；易失运行态边界（Redis state 不承载事实）；数据集域异步写入边界。
  - [请求处理分层设计](../functions/请求处理分层设计.md)、[网关错误处理完整链路](../functions/网关错误处理完整链路.md)、[网关异常重试与兜底策略](../functions/网关异常重试与兜底策略.md)、[响应语义检查管线设计](../functions/响应语义检查管线设计.md)。
  - [流式提交边界受限重放与异常终局处置设计](../functions/流式提交边界受限重放与异常终局处置设计.md) v2.3：T58 受限重放窗口（B=8KB/T=2000ms）、ParseError 字节隔离、generic EOF 收敛 `upstream_stream_abnormal_eof`、W32 L1 空完成终局 `upstream_stream_empty_completion` + L2 协议形状护栏、T53 提交后真·网络级断开、§6.5 合法失败帧拦截。
  - [请求级提前耗尽与负面缓存设计](../functions/请求级提前耗尽与负面缓存设计.md)：L1 3 账户/5 次合取阈值、L2 `clientSourceKey+"\x1f"+requestFingerprint` 10min TTL / 10000 上限 / body ≤1MB。
  - [官方可落地OAuth账户接入设计](../functions/官方可落地OAuth账户接入设计.md) + T65：401 自动刷新 singleflight + 60s 最小间隔 + 请求级 once；上游换发失败 5 分钟静默回落。
  - [审计日志保全策略设计](../functions/审计日志保全策略设计.md)：成功 1h 热窗口 + 10% 采样、正文 512KB/2MB 上限、队列 256MB 字节预算、W13 空壳三重防线（Cancel 撤回/persist 失败撤回/2h 兜底清扫）。
  - [AI账户错误语义与状态变更边界](../functions/AI账户错误语义与状态变更边界.md)：`error` 硬状态不参与调度；`degraded/precheck_pending/half_open` 等只属运行态；上游响应不写账户状态原则。
  - [接口契约与权限矩阵](../functions/接口契约与权限矩阵.md)：管理 API / 网关 API 响应结构与错误语义。
- 项目裁决出处：根 `AGENTS.md`（全局行为规范、开发规范）；项目 `AGENTS.md`（文档优先、待办登记、部署同步）；`docs/待办与阻塞事项.md`（B4/B5/T63/T65/T82/T85/W13/W32/W39/W45 等，开工前已通读）。
- 代码基线：master @ `36d05a01a`（2026-10-31），工作区 clean；最近功能面提交：`312ffc301`(W40 retention)、`8c21f958c`(accounts 样式)。v2026.10.31 发布基线 `4c5cff40a` 含 BUG-0339 修复与 T82 能力声明三列。
- 工作区边界：无并行脏文件；共享序号占用——`docs/待办与阻塞事项.md` 编号 B/C/T/W/P 已用至 B5/T85/W47，本台账条目编号用 `DD-` 前缀避免冲突；`docs/bug/` 问题档案编号已至 0339，新增档案顺延 0340+。
- 已知问题史（回归先验，攻击角色输入）：
  - BUG-0339：失败健康检查去重表以 `*GatewayRequest` 强键驻留请求体（进程级强键 map 移植自 per-request WeakMap）——修复为 per-request 指针标记（2026-10-31 已发布）。**同族风险：进程级强键 map / 全局注册表钉住大对象**。
  - W13 审计 in_progress 空壳：三轮修复（Cancel 撤回 → persistOne 失败/panic 分支撤回 → 饱和内联撤回 + 2h 兜底清扫），仍有生产残留产生面在取证。
  - W39 电路持久化：`ledgerRevisions` 缓存在 `CASConflict && Incident==nil` 不清理 → 恒冲突重试耗尽（已修，观察中）。
  - W32 空壳成功流、W22 半截 chunk 粘连、T58 EOF 尾字节次序：流式管道边界多轮加固，均为复发高危面。
  - B5 托管 OAuth 建号 400（strict-body 未知键拒绝）；BUG-0333/0332 克隆/编辑协议身份占位；BUG-0334/0335/0336 列退役与写序外键族。
  - W45 chat 面目录读链畸形能力矩阵硬失败 vs 管理面 lenient（已登记观察，不扩大）。
  - T85 chat 工具编排触顶错误掩盖（待用户裁决，属 chat 域在办项，本环不重复登记）。
- 当前阶段 / 当前轮次：终态复核轮已完成并出终报；**未提交批最终落点复核已完成**（代码 26/26 核点正确，3 处修正已闭环：2 文档残留 + 1 测试强度）。本环全部轮次结束。
- 当前工作区状态：R6 批10 已由用户提交为 `98b0e210b`（此前 R1–R5 主体已入 `e0d313642`/`d56bc2829` 等）；未提交仅剩台账 + 落点复核的 3 处修正（README.md:11、postgres-exporter yaml:23、dd6_s1 测试强度）。

## 扩散记录（续）

| 轮次 | 新纳入对象 | 耦合类型 | 理由 |
| --- | --- | --- | --- |
| R4 | jobs `usagewriter/statsagg` 游标门（DD3-B1）与 `circuitstore` dedupe/清理（DD3-B5） | 跨进程共享状态（PG/Redis/文件交接） | R3 跨进程对产出的两条高价值候选，gateway 侧无法单边修复 |

## 覆盖矩阵（R1 维度制，大对象以"复核通过面"结论承载）

R1 划分 7 个聚焦面（A 入口保护与认证 / B 调度派发 / C 流式管道与响应 / D 用量审计统计 / E OAuth 凭据生命周期 / F 请求体与协议准备 / G cmd 组合根与装配），每面产出"已查面清单 + 发现编号"结论，细节见问题记录。

## 扩散记录

| 轮次 | 新纳入对象 | 耦合类型 | 理由 |
| --- | --- | --- | --- |
| R1 | （排除）`internal/chat/**` 与 `docs/functions/AI问答*.md` | 并行会话在制品 | R1 攻击/验证期间该域出现另一会话的活动改动（T85 工具编排触顶收尾方向），本环不触碰、不重复产出（T85 已登记待办在办） |

工作区边界更新（2026-10-31 攻击轮进行中核实）：HEAD 前进至 `1da3ec3ca`（仅 docs/todo，不影响 R1 结论的代码面）；脏文件=并行会话 chat 域 9 个 Go 文件 + 4 个 AI问答 docs，均为并行会话在制品，本环修复批全部避开 `internal/chat/**`。

## 问题记录

### R1 攻击轮（维度制，2026-10-31，7 角色并行）

攻击角色：A 入口认证（gatewaypreauth/gatewayclientip/ratelimit/gatewaycacherate/gatewaycallertier/apikeys/authsys）｜B 调度派发（gatewaydispatch/routing/runtimecache/hotquality/circuit/proxyhealth + T63/BUG-0339/W39 新代码）｜C 流式管道（gatewayresponse/openai/proto/openaicompat/桥接）｜D 用量审计（gatewayusage/auditlog/operationlog/statreads/publicapilogs/spool）｜E OAuth 凭据（oauthmgmt/accountkeystates/自动刷新）｜F 请求体协议（gatewaybody/codex/gemini/anthropic/requestoverride/指纹）｜G cmd 组合根（main/compose/runtime/挂载/停机）。假设编号 DD-<角色><序号>。

| ID | 来源 | 假设（一句话） | 关键依据 | 状态 |
| --- | --- | --- | --- | --- |
| DD-A1 | A | preauth 失败计数 Redis GET-then-SET 非原子，并发打点互覆，认证前熔断对并发 spray 失效 | gatewayclientip/circuit.go:299-321 | 假设 |
| DD-A2 | A | /v1 files 族（openaicompat）在协议门前应答，绕过 preauth 熔断与 IP 黑名单 | chain_v1.go:137-146、chain_openaicompat.go:73-91 | 假设 |
| DD-A3 | A | /__aipublic__ 无效 token 401 不进惩罚窗口且无 IP 级限流；/v1/realtime ?token= 同构 | aipublic/aipublic.go:178-208、kernel.go:103-107、chain_realtime.go:746-767 | 假设 |
| DD-A4 | A | aipublic DecodeJSON 无界 io.ReadAll，单请求 OOM DoS | kernel.go:103/195-207、aipublic/groups.go:281 | 假设 |
| DD-A5 | A | aipublic Redis 惩罚窗口失败回退进程内 memory 继续放行（fail-open），违「缓存不可用显式失败」 | aipublic/ratelimit.go:94-115 | 假设 |
| DD-A6 | A | x-api-key / gemini ?key= 无效凭证落 :missing 桶而非 :token 桶，spray 阈值退化 | gatewaypreauth/preauth.go:386-400、circuit.go:593-608 | 假设 |
| DD-A7 | A | 额度后置记账+5s 决策缓存+进程内在途估算，并发可超额（Node 同构取舍） | gatewayquota/inflight.go:56-80、apikeyquota.go:15 | 假设 |
| DD-A8 | A | 无效 Key 10min 负缓存占 LRU 10000 容量可挤出有效快照（Node 同构低危 DoS） | gatewayruntimecache/runtime.go:192-198、service.go:339 | 假设 |
| DD-A9 | A | stale-while-revalidate 下禁用 Key 失效事件丢失最长 10min 陈旧窗口 | gatewayruntimecache/runtime.go:38-47 | 假设 |
| DD-A10 | A | preflight 授权配额检查 account 参数恒 nil，账户级授权额度疑似从不检查（猜测，需 parity） | gatewaypreauth/preflight.go:820-824、gatewayquota/authzquota.go:164-176 | 假设 |
| DD-A11 | A | api_keys 时间计划网关路径不即时判定，依赖 jobs 周期翻 status | gatewayruntimecache/sqlruntime.go:226-274 | 假设 |
| DD-B1 | B | T63-L1 FailedAccountIDs 跨 upstream_model 桶全量聚合且不带 RecoverableAccountIDs，可恢复账户被写进请求级耗尽集 | chain_request_fast_exhaust.go:108-114、attemptoutcomes.go:222-227 | 假设 |
| DD-B2 | B | T63-L1 5xx 瞬态计入请求级失败，抖动上游可在第 5 次失败截断本可命中候选（设计 §4 边界） | chain_ports.go:773-783 | 假设 |
| DD-B3 | B | T63-L2 无上游成功后提前清除路径，TTL 内原样重试秒拒 | chain_request_negative_cache.go:43-73 | 假设 |
| DD-B4 | B | rawBodySnapshotOf nil 时 bodySha 回落空串摘要，同源请求可互撞 L2 键（猜测） | chain_ports.go:872-896 | 假设 |
| DD-B5 | B | L2 ip_api_key_fallback 档 NAT 共享出口同 Key 多用户互撞（低危） | gatewaycodex/sourceidentity.go:158-170 | 假设 |
| DD-B6 | B | L2 order 字段无读消费者，O(n) 线性扫描微 perf 债 | chain_request_negative_cache.go:32/69/82-90 | 假设 |
| DD-B7 | B | BUG-0339 后 Rejected 不消耗标记 → 每个候选拒绝重跑 backgroundProbeDisabled DB SELECT（热路径放大） | chain_request_failure_health.go:217-222/287-295 | 假设 |
| DD-B8 | B | W39 delete 无条件整键，与 rebuild/reconcile 回填竞争可短暂复活 cas_conflict 循环（自愈） | gatewaycircuit/bridge.go:1151-1163 | 假设 |
| DD-B9 | B | ledgerRevisions/dispatchRevisions 无淘汰，账户删除后条目常驻（低危） | gatewaycircuit/bridge.go:392/564/1161 | 假设 |
| DD-B10 | B | latency_degraded withLatencyStateMutationLock 未获锁时慢样本静默丢弃，触发降级延后 | gatewayproxyhealth/latencydegradation.go:522-536 | 假设 |
| DD-B11 | B | latency 读写键 checked/unchecked 两变体在 runtime key 出错时失配（猜测，可达性未验证） | latencydegradation.go:1784-1798 | 假设 |
| DD-B12 | B | 55P03 快失败使本次本地避让不落 Redis——问题-0313 已裁决口径 | chain_dispatch.go:985-991 | 假设 |
| DD-C1 | C | shiftSseEvent 不识别 CRLF 空行 \r\n\r\n（注释声称支持），CRLF 上游全流粘连 | gatewayresponse/interceptor.go:186-205 对照 streamusageinject.go:113-131 | 假设 |
| DD-C2 | C | OpenAIStreamInterceptor.pending 无容量上限，提交后无 stream_lifetime，单连接内存无界 | interceptor.go:53-54/292 | 假设 |
| DD-C3 | C | 隔离器 256KB 越限按任意字节偏移整段放行，帧结构破坏 + 好事件一并放行，可蓄意绕过 | interceptor.go:290-296、gatewayopenai/inspector.go:14/176 | 假设 |
| DD-C4 | C | W32 对 responses 域 reasoning-only 完成回合（无文本/工具/encrypted_content/summary）误判 empty_completion | pipefinal.go:596-605、gatewayopenai/sseparse.go:185-192 | 假设 |
| DD-C5 | C | T53 Hijack 失败降级后连接不关，net/http 补发 chunked 终止帧，契约明令禁止形态且仅 Warn 无度量 | chain_downstream_interrupt.go:44-49、gatewaypreauth/responses.go:284-293 | 假设 |
| DD-C6 | C | 桥接流 usage 记账按账户（上游）协议解析却消费桥接后客户端协议字节，跨协议桥 usage 丢失/丢缓存粒度 | gatewayresponse/finalize.go:151-168、gatewayopenai/usage.go:119-126 | 假设 |
| DD-C7 | C | chat→anthropic 桥 Fail 臂只发 error 事件无 message_delta usage 回填，窄窗口 input_tokens 丢失 | openaicompatbridge/bridge_response.go:978-996 | 假设 |
| DD-C8 | C | chat→gemini / anthropic→gemini 桥上游 EOF 中断臂无条件合成 finishReason:STOP 成功收尾，失败反转为成功 | chain_bridge_response.go:197-199/231-233、bridge_gemini_response.go:327-340 | 假设 |
| DD-C9 | C | IndexBridgeSseBoundary 只匹配 \n\n/\n\r\n 不匹配 \r\n\r\n；pending 全量拷贝 O(n²) | openaicompatbridge/bridge_stream.go:16-33/54-66 | 假设 |
| DD-C10 | C | （核验通过）T58 窗口缓冲硬上限 8KB、释放幂等、timer 竞速无泄漏 | precommit.go:58-104 | 已查未发现 |
| DD-C11 | C | （核验通过）§6.5 拦截状态机 error 帧后内容不可达/多帧首帧胜/跨 chunk 拆分持有，无绕过 | pipe.go:274-357、pipefinal.go:333-357 | 已查未发现 |
| DD-C12 | C | （核验通过）EOF 尾字节二分修复覆盖混合形态，字节序与上游一致 | pipefinal.go:41-57/134-139/211-223 | 已查未发现 |
| DD-C13 | C | （核验通过）T58 窗口排除面（媒体/桥接/hosted tool）在装配点判定且桥接流天然不开窗 | chain_v1_loop.go:369-378 | 已查未发现 |
| DD-D1 | D | 撤回命令与 in_progress 信封落库无全序无墓碑：4 worker FIFO + retractBestEffort 内联旁路，retract 可先于信封落库执行成 no-op → 空壳第四条产生面（W13 残留归因最高嫌疑） | auditlog/producer.go:262-283、retract.go:38、audit_capture.go:971-975 | 假设 |
| DD-D2 | D | flushFinalizedAudit 对 gateway_failed 且 totalAttempts==0 丢弃 finalize 不撤回，与 StartAttempt 锁间隙可命中 | audit_capture.go:1064-1072/690/716-717/1242 | 假设 |
| DD-D3 | D | （核验通过）blob 错误形态=首发错误+Rollback ErrTxDone 拼接，已收敛（失败分支有撤回） | auditlog/store.go:894/289-291 | 已查未发现 |
| DD-D4 | D | DispatchAuditLog 调用方 goroutine json.Marshal 全量 base64（1.33x），违背「大审计后台编码」契约字面；unmarshal 失败静默吞 | chain_ports.go:2555-2565、audit_types.go:144-161 | 假设 |
| DD-D5 | D | StartAttempt 内联 sha256 body（≤64MB resident 门），Node 1MB 内联上限语义疑似不一致 | audit_capture.go:728/1336/24/580 | 假设 |
| DD-D6 | D | Cancel() 撤回队列满时请求 goroutine 阻塞重试 ≤300ms 仍失败仅 warn（已知面确认） | auditlog/producer.go:235-243 | 假设 |
| DD-D7 | D | spool .corrupt 隔离文件全仓无删除方且计入容量配额，慢性侵占 MaxItems/MaxBytes 致 usage 丢失 | gatewayusage/spool.go:132-138/215-230、jobs drain.go:259 | 假设 |
| DD-D8 | D | FinalizationDispatch DroppedCount 硬编码恒 0 死指标 | gatewayusage/finalization.go:366 | 假设 |
| DD-D9 | D | chainBodyRejectionRecorder 不填 CompactionExpected——复核后仍为唯一已知缺口 | chain_compose.go:1106-1136 | 假设 |
| DD-D10 | D | （核验通过）spool gateway↔jobs JSON tag 逐字段一致，T49 失配未复发 | gatewayusage/records.go vs usagewriter/records.go | 已查未发现 |
| DD-D11 | D | （核验通过）审计队列准入 TOCTOU 无泄漏、预算释放闭合、>256MB 不可达 | auditlog/producer.go:166-189/341-355 | 已查未发现 |
| DD-D12 | D | （核验通过）retractOne 失败靠 jobs reaper 2h 兜底，空壳存活 ≤2h | producer.go:293-335、cleanuprepo/auditshells.go | 已查未发现 |
| DD-D13 | D | traffic 门对信封/finalize 各跑一次的不对称休眠缝（当前接线恒 gateway 不触发） | audit_capture.go:1297-1305 | 假设 |
| DD-D14 | D | reaper 按 created_at<2h 删信封，>2h 超长流 finalize 走 INSERT 恢复无残留（行为边界） | auditlog/store.go:977-979 | 假设 |
| DD-D15 | D | （核验通过）F4 producer fire-and-forget+panic 隔离契约符合 | operationlog/producer.go:97-163 | 已查未发现 |
| DD-E1 | E | T65① 超级分组路由下 UsageContext.GroupID=超级组 id，loader 不展开成员组 → rehydrate 恒空，刷新成功也 SkipAccount | chain_oauth_refresh.go:176-189、preflight.go:1858-1866 | 假设 |
| DD-E2 | E | gateway CAS 冲突分支弃本进程已换发令牌且无 race-retry（jobs 有），最坏库内凭据被作废 → 持续 401 | oauthmgmt/auto_refresh.go:88-106 对照 jobs refresh.go:600-610 | 假设 |
| DD-E3 | E | token 端点错误 detail 用原始响应体 verbatim 进 502 与自动刷新日志，违脱敏契约 | oauthmgmt/anthropic.go:204-212、errors.go:100-108 | 假设 |
| DD-E4 | E | 四家 credentialsPatch 解析器不收 error_handling_rule_overrides 而前端会发 → OAuth 建户 400 | oauthmgmt/providers.go:195-199、frontend accountSavePayload.ts:470-481 | 假设 |
| DD-E5 | E | gateway 水合解密失败静默 nil,nil 无日志无兄弟对照（对照只在 jobs 批） | chain_accounts_secret.go:76-81 | 假设 |
| DD-E6 | E | singleflight 闭包持代表方 ctx，代表断开时等待者共享 nil 候选且 60s 内不再刷新 | chain_oauth_refresh.go:120-127/155/162/180 | 假设 |
| DD-E7 | E | JUHE_AI_SECRET 轮换中断留混合密文时，可解密兄弟为旧密文账户背书 → 全池误杀变体（推断） | jobs refresh.go:353-371/442-449 | 假设 |
| DD-E8 | E | 重新水合用缓存读而非 Fresh 读，失效事件同步前以 401 旧凭据重试必再 401 且污染缓存 | chain_oauth_refresh.go:180、gatewayruntimecache/accounts.go:47-59 | 假设 |
| DD-E9 | E | chainAccountAPIKeyRotationKey 硬编码前缀绕过 NamespacedKey，多环境共享 Redis 会串号 | chain_apikey_rotation_redis.go:82-85 | 假设 |
| DD-E10 | E | accountkeystates 包内无任何 DELETE，secret 轮换/Key 删除后孤儿 state 行永存 | accountkeystates/（rg DELETE 零命中） | 假设 |
| DD-E11 | E | Redis 轮换计数器出错时多 Key 账户准备整体报错不降级，池收窄放大（fail closed） | gatewaydispatch/accountpreparation.go:204-206 | 假设 |
| DD-E12 | E | singleflight 完成后无二次节流复查，等待者拿同一候选各自重试放大上游压力（低置信） | chain_oauth_refresh.go:107-127 | 假设 |
| DD-F1 | F | override 降级链查请求模型目录而非映射右侧真实上游模型，违背「以映射右侧确定真实上游模型」契约 | chain_driver.go:616-622/560-590、AI账户上游接口能力设计.md:26 | 假设 |
| DD-F2 | F | service_tier 覆盖写不区分 endpoint family，跨协议映射到 anthropic_messages 时 priority 进入上游 body → 400 | oauthnormalizer_overrides.go:211-216、requestoverride.go:81-85 | 假设 |
| DD-F3 | F | /v1/responses/compact 无界 wall budget 挂 raw 判定而非 codexTurn 门，任意客户端可拿无墙钟预算占槽 | gatewaycodex/compactioncontract.go:22-31、preflight.go:285/306/342-344 | 假设 |
| DD-F4 | F | override 应用的 compact 门 path-only 与压缩期望（含 trigger 项）不同源（低置信） | gatewayupstream/helpers.go:84-90、chain_driver.go:629 | 假设 |
| DD-F5 | F | Gemini :generateContent + body stream:true 判 SSE 下游但上游 URL 不带 alt=sse，协议错帧（猜测） | gatewaycodex/strategy.go:408-413、gatewaygemini/routehelpers.go:247-250 | 假设 |
| DD-F6 | F | 多值响应头被 Set 覆盖仅存末值，Node 保数组语义（观测失真非安全） | gatewayupstream/transport.go:966-976、finalize.go:749-752 | 假设 |
| DD-F7 | F | in-flight 预算只计 RawBody，物化 JSON map 放大不计量（BUG-0339 家族残余） | gatewaybody/body.go:385-400、request.go:86-135 | 假设 |
| DD-F8 | F | 内容指纹第二轮才补 system 前缀时锚点漂移，审计 SessionID 与亲和中途换键 | gatewaybody/contentfingerprint.go:144-183/251-277 | 假设 |
| DD-F9 | F | （边界记录）内容指纹四锚齐备前任一元素缺 role 即整档失败——fail-closed 按设计 | contentfingerprint.go:167-175 | 边界记录 |
| DD-G1 | G | supervisor 停机顺序倒置：先关 owner store/释放 F4 租约才进 HTTP Shutdown，在途请求审计/操作日志丢失，与 main.go:953 注释宣称相反 | supervisor.go:68-77、main.go:952/957/721/769-776 | 假设 |
| DD-G2 | G | Shutdown 超时或 Serve 错误走 fail()→os.Exit(1) 跳过全部 defer，composed 排空永不执行 | main.go:961-966/979-988/1224-1232 | 假设 |
| DD-G3 | G | F3/F4 producer 无 Close/drain 接口，优雅退出也丢队内记录；compose.go:1239 注释与代码不符（文档-代码矛盾） | auditlog/producer.go、operationlog/producer.go、compose.go:1238-1242 | 假设 |
| DD-G4 | G | http.Server.Shutdown 不等 hijacked 连接即返回，realtime 会话截断无收尾 | chain_realtime.go:350、main.go:957-963 | 假设 |
| DD-G5 | G | J3b 四族路由（/auth/、/model-checks/ 等）在 rootMux 先于 kernel 栈命中，绕过 IP 限流/安全头/recover | main.go:864-870/540-551、kernel.go:88-114 | 假设 |
| DD-G6 | G | 临时 IP 白名单 J3b 面 commaList 原样 vs 主面严格解析，::ffff: 形态条目只在主面生效 | main.go:541、runtime.go:801-820 | 假设 |
| DD-G7 | G | main.go 对 runtimecache registry error 用 _ 丢弃，未来前置排除失效即 nil-deref panic（潜伏） | main.go:903-914 | 假设 |
| DD-G8 | G | codex/anthropic 用量头与健康派发 fire-and-forget goroutine（WithoutCancel 无 deadline），PG 池饱和时无界堆积 | compose_codex_usage_headers.go:84-105、recordmaintenance/dispatch.go:207-230 | 假设 |
| DD-G9 | G | realtime 每 Key 连接计数 sync.Map key 永不删除（release 只减值） | chain_realtime.go:95/105-127 | 假设 |
| DD-G10 | G | usageStatsTimezone 双源：statreads 直查 DB 每读一次 vs ipstats 60s TTL 快照 | compose.go:825/604/1248-1252、statreads/scope.go:92-115 | 假设 |
| DD-G11 | G | publicapilogs capture store 初始化失败仅 WARN 静默降级，公开面访问日志恒缺 | compose.go:1194-1202 | 假设 |
| DD-G12 | G | （文档性防线）健康面绑 0.0.0.0+ALLOW_NON_LOOPBACK，容器网内 metrics/pprof 匿名可达；宿主发布绑 127.0.0.1 核对无误 | main.go:79-98/1033-1036、compose.yml | 文档性防线 |
| DD-G13 | G | chat 未绑账户会话回落裸执行器 → 401（猜测：设计上未选账户不可发送，疑似不可达） | chain_chat.go:70-80/144-148 | 假设 |
| DD-G14 | G | owner RunOwner 失败→503→docker 重启循环边界紧（租约等待 45s vs start_period） | main.go:339-350/704-722、health_route.go:44-53 | 假设 |
| DD-G15 | G | gateway 请求路径 fire-and-forget ensureSchema 与「maintenance 预置、组合根只读」口径并存（加法幂等，文档偏差） | chain_request_failure_health.go:158-163、recordmaintenance/dispatch.go:133-136 | 假设 |
| DD-G16 | G | 启动期 spool 目录不可写仅 ERROR 继续启动，用量交接断链运行期才暴露 | chain_spool_diagnostics.go:20-33、compose.go:868-871 | 假设 |

R1 攻击轮核验性结论（不改行为、直接入覆盖矩阵）：DD-C10~C13、DD-D3、DD-D10、DD-D11、DD-D12、DD-D15；边界/取舍直接归类：DD-F9（fail-closed 按设计）、DD-G12（文档性防线）。A 角色核验通过面：/v1 全入口同认证门无旁路、内部身份通道未导出 key 无外部伪造、ratelimit 系统 API Lua 原子 fail-closed、caller tier 不可拔高、captcha GETDEL 一次性、密码恒时比较、IP 黑名单单 IP 粒度无区域封禁。B 角色核验通过面：普通/超级分组同一 loader 资格闸、重试预算值域封顶、L1 计数器按请求 ctx 安装、跨进程无 memory 回退、电路双写顺序正确。E 角色核验通过面：清扫 vs 单飞无竞态（TTL 10min>两窗）、retry 预算互斥类型门成立、clone 域断言成立、restoreIfRecovered 双守卫。

### R1 验证轮（2026-10-31，7 审核档角色独立证伪）

送验 63 条：**confirmed 30 / refuted·duplicate 32 / undetermined 1（DD-F5，native gemini 消费路径与上游 stream 字段行为缺证，待补证）**。要点修正：DD-A1「熔断失效」实为灵敏度退化（计数单调不减仍达阈值）；DD-E2 升级指控被反证（CAS 冲突=本进程写未落地，无令牌作废），归 T65 已登记跨进程双打同源；DD-B1 RecoverableAccountIDs 全仓结构性恒空，无可破坏语义；DD-E11 实际行为比报告更早终止（整请求失败而非池收窄）。

**确认项 A/B/C 裁决**（判据基准=契约/文档承诺语义）：

- **A 类即修 22 条**：DD-D1（W13 空壳第四产生面：撤回与信封落库无全序，慢窗下撤回 no-op 先行→信封后提交成永久空壳；生产 ~2.9/h 残留形态吻合，列 W13 取证首要假设）、DD-G1（停机顺序倒置：组件 Close/租约释放先于 HTTP Shutdown，在途审计/操作日志续租失败被丢，违架构总览:268）、DD-G2（fail→os.Exit 跳过全部 defer 排空）、DD-G3/G15（注释与代码矛盾）、DD-E1（超级分组路由下 401 自动刷新重试臂恒失效）、DD-E4（credentialsPatch 拒 error_handling_rule_overrides 与写入面矛盾→OAuth 建户 400）、DD-F1（override 降级判定查请求模型而非映射右侧真实上游模型，违 AI账户上游接口能力设计:26）、DD-F2（service_tier 跨协议写进 anthropic body）、DD-F6（多值响应头 Set 覆盖）、DD-A1（熔断计数 Redis RMW 非原子）、DD-A2（/v1 files 族绕过 preauth 熔断与 IP 黑名单）、DD-A3（aipublic/realtime 无效 token 401 无惩罚限流）、DD-A4（aipublic 无界 body 读）、DD-A6 相关修正不涉修复、DD-C1（SSE 拦截器/桥泵 CRLF 空行不识别，注释声称支持）、DD-C2（拦截器 pending 无上限）、DD-C6（跨协议桥接 usage 记账丢失：anthropic→chat、chat→gemini 两方向结构证实）、DD-B7（Rejected 不消耗标记→每失败候选重跑 DB SELECT）、DD-D4（请求路径全量 Marshal+base64 违「大审计后台编码」契约字面）、DD-D5（内联 sha256 无 1MB 门，与 Node 镜像语义背离）、DD-D8（DroppedCount 死指标）、DD-G9（realtime 计数 key 永不删）。
- **B 类过目 2 条**：DD-F3（/v1/responses/compact 预算豁免无身份门——任意 API Key 可领无界 wall budget；修复=加 keepalive 同款身份门会挡住以前成功的请求）、DD-D7（spool .corrupt 隔离文件保留现场但全仓无回收路径且计入容量配额，慢性侵占可致溢出补偿永久失效；修复=加保留 TTL 属数据保留策略）。
- **C 类裁决清单**：DD-F7（in-flight 物化放大不计量，Node 同构容量决策）、DD-G4（Shutdown 不等 hijacked，realtime 停机截断，需自 track 机制）、DD-G8（fire-and-forget goroutine 无并发上限，镜像 Node 已裁决契约）、DD-G10（时区双源，继承 Node 语义）、DD-G5 残余（/__aisys__/api/{,my-}model-checks/ 落 system API 前缀却无限流/安全头，差异未登记）、DD-G12（健康面绑 0.0.0.0 文档性防线）、DD-G14（owner 失败→重启循环边界紧）、DD-G16（spool 目录不可写不阻断启动）、DD-B2/B3（T63 设计已裁决边界）、DD-C4/C8（设计登记+测试钉住的既定取舍）、DD-C5（Hijack 降级当前部署不可达，已登记）、DD-D2 理论缺口（gateway_failed 零尝试丢弃分支不查 inProgressAuditEnqueued，无可达触发路径）、DD-D6（Cancel 撤回 3 次重试为 2026-10-29 收口有意设计，已裁决接受）、DD-D13（traffic 门不对称休眠缝）、DD-D14（>2h 超长流 reaper 边界）、DD-B5/B6/B9（低危微债）、DD-A7/A8/A9/A11（Node 同构取舍/观察）、DD-F5（待补证）。
- **refuted 32 条**：DD-A5/A6/A10、B1/B2/B3/B4/B8/B10/B11、C3/C4/C5/C7/C8、D2、E2/E3/E5/E6/E7/E8/E9/E10/E11/E12、F4/F8、G6/G11/G13（C9 归 C1 同源）。已裁决接受指针：DD-B12→问题-0313；DD-A7/A8→Node 同构。

**修复批计划**：批 1（11 条，中高影响）=DD-D1/D4/D5、E1/E4、F1/F2、G1/G2/G3/G15（审计域+停机域+OAuth 域+override 域 4 写码档并行）；批 2（11 条，低影响）=DD-A1/A2/A3/A4/G9、C1/C2/C6/F6、B7、D8（入口防护域+链路流式域 3 写码档并行）。每条钉住方式见派发规格；B 类 2 条随本轮收口呈报。

### R1 落点复核轮（2026-10-31，3 审核档并行；三动作：落点逐条核/修复间交互/回归攻击）

22 条修复中 19 条落点确认正确（含文档同步逐句核对一致、负向场景攻击无回归）。登记 8 项 DD-RC：

| ID | 落点 | 一句话 | 处置 |
| --- | --- | --- | --- |
| DD-RC-A1 | auditlog/producer.go:447-451 | **回归**：CaptureDeferred Decode 失败臂在 persistOne 前 return，在途信封集合泄漏、该 id 后续撤回空转 15s | ✅ 即修闭环（envelopeID 随队列项携带+失败臂注销；TestDD1DeferredDecodeFailureUnregistersEnvelope 钉住，含注销后撤回立即执行的行为证据） |
| DD-RC-A2 | main.go failRuntime 路径 | failRuntime 在 Close#1 内 exit 跳过 F4 lease 释放（继任等 TTL）；G2 注释不准确 | ✅ 即修闭环（runtimeFailureCleanup 排空后释放 lease，TestDDG2_RuntimeFailureCleanupReleasesLease；注释回正） |
| DD-RC-B1 | gatewayclientip/circuit.go:334/:488 | Lua blocked 早退 failureCount 恒 0（展示字段偏差，封禁语义无损） | ✅ 即修闭环（`#(entry['samples'] or {})`，memory 臂同值对照测试） |
| DD-RC-C1 | chain_bridge_response.go:182-188 | **回归**：桥接流上游未发 usage 时 state.Usage=nil 仍写 EmptyUsage 快照→finalize 跳过估算兜底计费 0 | ✅ 即修闭环（nil 不写快照回落估算；「发 usage 全 0」保留 ok=true；负向验证撤回修复测试转红后恢复；8 新测试） |
| DD-RC-C3 | chain_v1.go:1184-1188 | compat 入口熔断服务 err fail-open 与主链 fail-closed 口径差 | ✅ 即修闭环（复用 handleOrchestratorError 同一渲染出口，零新错误契约；2 新测试） |
| DD-RC-C2 | gatewayresponse/finalize.go:770 | 空值头 Get=="" 误判不存在（终值与修复前一致，不回归） | 登记（低，观察） |
| DD-RC-C4 | bridge_stream.go vs interceptor.go | `\n\r\n` 边界仅桥泵识别（既有形态非本批回归），三处分帧词表未完全取齐 | 登记（C 类观察，后续对齐） |
| （附带） | main.go:646 | F3 auditLease 在 failRuntime 路径同样被跳过（与 RC-A2 同族，继任等 TTL 有界） | 登记（低，观察） |

修复间交互审查结论：D1×D4（Decode 臂=RC-A1 已闭环）、G1×G2（RC-A2 已闭环）外全部「一致」；跨域对（E1×E4/A1×A3/A3×A4/F1×F2/A2×C6/C1×C2/C6×F6）逐对无冲突。回归攻击结论：producer 并发 5 场景、停机顺序 4 场景、C6 槽位并发/C1 跨 chunk 拆分/C2 越限边界攻击无新发现。

### R2 验证轮（2026-10-31，4 审核档角色；22 条送验）

**confirmed 14 / refuted 8**。两处细节修正：DD2-A5/A6 越限阈值实为 256KB（StreamPreCommitBufferMaxBytes）非 8KB；DD2-C7 的「已删 store 孤儿 chunks」半句被 FindVectorStore 守卫证伪（file 级复活成立）。附带发现：`docs/functions/AI问答设计.md:729` 仍描述已退役的 8s 生成排空契约（chat 域并行会话在制品，登记不动）。

- **修复批回归/缺口 confirmed（A 类即修 6）**：DD2-A1（F3 门自证式架空，高——B 类呈报裁决方向）、DD2-A5（C2 越限放行先于清点，完整事件绕过响应检查策略——唯一策略消费面在 interceptor.go:129，越限分支 :78-93 先 return）、DD2-A6（gatewayresponse 拦截器/usage 注入/gatewayopenai 缓冲三处词表缺 \n\r\n，直连流该形态 missing_terminal 失败结算）、DD2-B1/B2（C6 截断流/半程 usage 快照 veto 估算：pump err==nil 即写快照、finalize 不看 Completed；message_start 即赋 Usage、空对象渲染三键非 nil）、DD2-C1（compat 守卫只 Inspect 不 Record，compat 401 枚举零成本，A2 修复计数面缺失）。
- **移植缺口 confirmed（A 类即修 7）**：DD2-C4（multipart purpose 字段 io.ReadAll 无上限 OOM，高；busboy 1MiB fieldSize 未移植）、DD2-C7（vector_stores 索引完成 upsert `deleted_at=NULL` 无守卫复活已删行）、DD2-D1（Grok sso-to-oauth 收 credentialsPatch 但 exchangeGrokSSOToken 不应用，前端恒发静默丢策略）、DD2-D2（授权实例高级详情 EffectiveErrorHandlingRules 用空 ownerCredentials，同函数 quota 用 effectivePolicySource 口径相悖）、DD2-D3（overrides 读面循环无条件 break 只取第一条，写面接受任意长度）、DD2-D4（replace 越界 rule_index 无上界→系统额度规则被无声抑制）、DD2-D8（克隆 CloneCredentialOptions 投影缺 overrides 键）。
- **B 类呈报 2**：DD2-A1（F3 豁免锚定方向需裁决：服务端锚选择属行为语义）、DD2-E8（改密面无暴破防护：120/分钟全局限速 vs 登录面 10/10min，加守卫会挡正常改密重试）。
- **refuted 8**：DD2-G2/G3/G5/G6（内联撤回=W13 既有取舍；尾部排空只写 spool 文件不走 PG；defer stop() LIFO 恢复信号默认行为；当前码无 8s 注释）、DD2-F1+F2+F3（媒体计费轮询驱动=设计文档明文裁决「不做后台全量轮询」）、DD2-F5（modes 过滤是闸且写侧保证非空，派发不读域=架构裁决；残余观察：存量缺 modes 历史行待运维只读核查）、DD2-A3（压缩分档是专用判读口径，设计明文）、DD2-B4（realtime 熔断封锁键=IP+token 指纹二元组，无 NAT 连坐）。
- **未送验（保持假设，R3/后续轮处理）**：DD2-A3/A4/A7/A8/A9/A11、B3/B5/B6/B9、C2/C3/C5/C6/C8/C9/C10/C11、D5/D6/D7/D9、E1/E2/E4/E9/E10/E11/E12/E13/E15、F4/F6/F7/F8/F9/F11/F12/F13/F14、G1 修复方向补强、G7/G8/G9/G12。

**R2 处置**：A 类即修 13 条（流式回归 4+compat 计数 1+移植缺口 7+D1 边界补强 1）派写码档；B 类 2 条呈报；其余假设保持登记。R2 存在 B 类确认与修复回归，不计干净轮，收敛计数 0/3。

**R2 过程观察登记**：①`TestOAuthManagedDefaultsCreateWritesConnectionRows/gemini_google_oauth` HEAD 既有失败（commit e38c6e9f2 上游 Base URL 版本根 v2 引入：期望凭据 base_url 原样、实际写 /v1beta 注册默认）——独立问题待办候选，非本环改动；②accounts 门控全包随机挂起（dev PG 多会话并行争用，pgx peekMessage 形态）——环境面；③`docs/functions/AI问答设计.md:729` 仍描述已退役的 8s 生成排空（chat 域并行会话在制品，登记不动）；④DD2-G1 修复后残余 µs~20ms 级缝隙（settle 恰落 deadline 前最后轮询窗内且撤回意图未及登记→走 W13 既有「重试耗尽」终态告警，远小于修复前 15~20s 带内必漏，是否另立事项待裁决）。

### R1 同源发现与遗留（攻击/修复过程中产出，未进修复流）

- audit_summary.go `SummarizeAuditPayloadForLimit`（:57-75）对超限 body 无门重算 sha——Node 镜像有门（sha256BufferIfSmall）；给它加门会改变 payload-summary JSON 的 originalSha256 契约，需先裁决字段形态 → C 类。
- DD-C6 剩余缺口已登记待办 **T86**（非流式桥接臂与其余四族流式桥方向未接 usage 快照供给，机制已就绪逐族接线；R2-DD2-B10 确认非流式维度在 T86 覆盖内）。
- DD-F5（undetermined）：native gemini `:generateContent`+body stream:true 的下游消费路径与上游字段行为缺证，待补证后裁决。

### R3 攻击轮（交互制，2026-10-31，6 角色分批）

**R3-E 修复间交互审查（强制项）已完**：43 处修复按 8 文件域逐对 36 对——**一致 35 / 冲突 0 / 需登记 1**。跨轮真实回归（前轮修被后轮修坏）未再现。需登记：DD3-RC1（低·理论缺口：D4 把 decode 移入 worker 后 persistOne 的 panic 隔离与 D1「settle 三臂都到达」不变量均未覆盖 decode panic 臂——当前 decode 纯 JSON 往返不可达，建议补 panic 隔离或收窄注释）。台账既有登记项（RC-C2/RC-C4/F3 auditLease）复核未被后续批削弱。

**R3-C 审计管线交错已完**：16 条假设（H15/H16 为正面确认：同 id 信封-终态乱序与 Decode 失败混流均有全序保证）。要点：

| ID | 假设 | 依据 | 状态 |
| --- | --- | --- | --- |
| DD3-C1 | 队列字节预算预检×Add 非原子，并发软突破（4096 硬限兜底） | producer.go:337/344-345/373/386-387 | 假设（低） |
| DD3-C2 | **finalize Deferred 因预算/队列满被丢弃×信封已落库→终态审计缺失且无意图登记，壳行滞留至 failure retention（outcome=gateway_succeeded 落 `<> 'success'` 谓词）** | producer.go:373-378、audit_capture.go:1248-1252 | 假设（高优先验证） |
| DD3-C3 | 撤回风暴 worker 独占（与 R2-G2 证伪同源，新增「20ms 轮询×settle 注销同锁放大延迟」微元素） | producer.go:64/541-542/586-593 | duplicate(R2-G2) |
| DD3-C4 | **撤回命令 3 次入队全失败被丢弃时不登记意图→信封 settle 成功无从补派**（DD2-G1 相邻缝隙） | producer.go:439-447/287-298 | 假设（高优先验证） |
| DD3-C5 | registerInFlightEnvelope 无条件置 false 会抹掉已置位意图（无二次登记路径，理论） | producer.go:256-263 | 观察（猜测） |
| DD3-C6 | D5 门×body 省略→requestFingerprint 丢 body 分量（与 L2 在 1MB 边界语义分裂） | audit_capture.go:736/1399、store.go:1324-1336 | 假设（中低） |
| DD3-C7 | spool 满 overflow 失败静默丢 usage+OverflowSpoolCount/PersistFailureCount 无 Prometheus 接线（与 D8 死指标同族） | finalization.go:257-262/291-294 | 假设（中） |
| DD3-C8 | Dispatch 无 overflow 工厂时 cond.Wait 永久阻塞（SpoolDirectory 显式配空才触发，窗口窄） | finalization.go:257-268 | 假设（低） |
| DD3-C9 | 单条 usage 超 64MB 错误被 sink 静默吞无日志 | service.go:253-255、sink.go:267 | 假设（低） |
| DD3-C10 | 停机在途 persistOne×store Close（「已接受边界」注释只声明队列残余未覆盖在途半途+撤回连锁） | compose.go:1233-1243、main.go:729 | 假设（中低） |
| DD3-C11 | failRuntime 只释放 F4 lease 的 F3 不对称（已登记观察的量化补强：同机重启 fence+1 可直取，仅跨机等 TTL 30s） | main.go:700/646/1310-1318 | duplicate(观察) |
| DD3-C12 | auditSettingsSourceAdapter 恒只传 3 字段，resident/正文上限旋钮在 gateway 链路不可表达（恒默认值） | chain_compose.go:800-804、audit_settings.go:58-83 | 设计确认项 |
| DD3-C13 | offload 恒 true→512KB/2MB 正文上限从不截断（64MB capture 门是唯一实体上限）——设计确认项 | chain_v1_audit.go:66、audit_capture.go:1372-1374 | 设计确认项 |
| DD3-C14 | 估算口径不计 payload.Headers（大 header 驻留低估，decode 同口径无漂移） | producer.go:214-228 | 假设（低） |

**R3-B 跨进程对已完**：18 条（B4 写半截×消费读不可达、B13 jobs restoreIfRecovered 收敛、B8/B14/B18 为核对点非缺陷）。要点：DD3-B1（高：ingestDrainProbe 两段快照被 drain 末批交错→statsagg 游标永久跳过→统计永久缺口，worker_ingestgate.go:46-56）；DD3-B5（高：circuit dedupe 行永生——Store.Cleanup 全仓无生产 caller，generation 复位复用 transitionID→CAS dedupe 命中+incident 行已缺=硬错误不走 W39 清缓存臂→persist 循环恒败，controlplane.go:566-597）；DD3-B2（中：持续损坏流×.corrupt 占配额 7 天→usage 断供；两份 NormalizeUsageRecordInput 镜像副本 562/499 行漂移）；DD3-B9/B10（OAuth ctx 取消×rotation 型凭据永久失效无自愈 / 宽限 rotation 双成功双方报成功——供应商语义需实测）；DD3-B11（手动重授权×jobs 批 CAS 409→单次 code 报废，T65⑤ 新后果）；DD3-B6/B7（W39 清缓存×回填 account 不匹配硬错误窗口 / CASAccountNotFound 静默 return）；DD3-B3/B12（低）；B15（Clock 注入瑕疵极低）；B16/B17（reaper×撤回复活窄窗 / 2h 前提依赖流无硬截止）。

**R3-D 入口保护三面已完**：15 条（键隔离/封锁联动主体健康复核通过；DD2-E1 触发源静态不可达降级观察）。要点：DD3-D1（中·唯一新引入故障放大面：compat 401 补计不区分基础设施 err——runtime cache 故障窗内 ≥8 个 compat 请求→故障恢复后合法用户被误封 30s+，与未送验的 R2-DD2-C3「DB err 折叠 401」耦合）；DD3-D2（x-api-key 主链 8 次 vs compat missing 桶 40 次阈值 5 倍漂移）；DD3-D3（compat/realtime 429 缺 Connection: close——注释声称逐字段对齐但代码缺，A 类小修）；DD3-D5~D15（低危观察/设计继承：spray 无 Inspect 短路面、混合桶截断提前封锁方向安全、403 免计 oracle、Redis 故障两面 fail 方向不一致=既有裁决、models Success 清键 fail-closed 方向安全等）。

**R3-A 缓存×调度已完**：15 条（H4 将 R2-DD2-C11 收口为「设计覆盖表+契约测试闭合无新缺陷」；H8/H9 修复批新代码核对通过）。要点：DD3-A1（中：runtime 单飞首载持发起方请求 ctx——同包 union.go:275 已改 Background 双标，首发断开→全体等待者 500，runtime.go:146-176）；DD3-A2（低中：失效×runtime 装载回填 check-then-act 窗口，旧快照回填 ≤60s，union 单临界区为项目自定标准被复现）；DD3-A3（低：memory 模式 accountsCache 同步读回填无代守卫，redis 模式不可达）；DD3-A5（低猜测：E1×H3 窗口同账户重试旧凭据）；其余为有界观察（H6 topic 正则未来风险、H10/H11/H12/H13/H14/H15）。

**R3-F 停机×owner 生命周期已完**：15 条（H5/H9/H12/H15 确认面；H6 修正 G14：compose 无 autoheal 不会重启循环，实为组件永久降级存活）。要点：**DD3-F1（高置信：正常停机 F3 租约必不释放——F3 组件 Close=auditStore.Close 先归零池 refs，main defer 的 auditLease.Close 落已关池仅 Warn；F4 对照成立因 lease 释放在组件 Close 内先于 store Close（main.go:777-784），F3 缺同款）**；DD3-F3（启动 fail 分支（F4 open/listen/compose 失败）os.Exit 跳过 defer→F3/F4 租约滞留 TTL，TTL 内重启无 ACQUIRE_WAIT 即 fail-fast——RC-A2 未覆盖的启动分支）；DD3-F4（RC-A2×docker 10s 宽限交错：长流 Shutdown 超时场景总预算 ~20s>10s 被 SIGKILL 截断，cleanup 不可达）；DD3-F7（组件重启窗不对称：F3 窗内审计静默缺失 vs circuit 窗内 503）；DD3-F8（retention markReady 时序→每次重启 ≈清理时长 503 窗）；DD3-F10（drain Close panic→退出码 0 假成功，猜测）；DD3-F13（J3b probe 无 Timeout 占槽，猜测低）。

| R4-批7（2026-10-31，B 类批复 1 条族 + T87，2 写码档） | DD2-E8+E9（改密 LoginGuard 镜像登录强度+操作日志，4 钉住测试含门控 2 例实跑 PASS，文档 3 处）；T87（gateway 写侧 circuit 同构缺口：outboxByDedupe 排除 dispatched 仅 incident 臂——dispatch 臂保留 dispatched 行为幂等证据有测试钉住、insertIncidentChangedOutbox ON CONFLICT 冲突重置+ErrIdentityReplay identity 守卫逐字对齐 jobs 形态；顺带修正 w9e fixture 为生产复合 UNIQUE；真实 PG 4 例绿+gatewaycircuit 全包绿；Cleanup 不接线已裁决→T87r 观察项，T87 条目按「解决即删」移除） | authsys 46 PASS/0 FAIL（门控实跑）；circuit_control_plane 门控全包 24.9s 绿；gatewaycircuit 13.4s 绿 | 落点由写码档自证（jobs 模板对照+门控实跑）；随下轮复核 |

| R4-批8（2026-11-01，C 类裁决落地 DD2-A1） | compact 预算豁免锚改服务端账户：chain_v1_loop 装配点闸（applyCompactionExemptionAccountGate），判定=Type==oauth && CodexOAuthVendorEligible(vendor)（复用既有 gpt vendor 口径）；触发条件=wall.Unbounded 且窗口含非 codex OAuth 账户→覆盖常规 270s+TimeoutsDisabled=false；TotalTimeLane/l.budgets.wall/preflight/T49 刻意不动（注释写明）；9 测试钉住 ①-⑥ + OAuth 重放轮混窗；文档 3 处口径收窄 | preauth/gatewaycodex/cmd 全包绿（cmd 21.9s）；vet 干净 | 落点由写码档自证+①-⑥ 验收面逐项测试；随下轮复核 |

### R6（运维与部署面 + 操作序列×降级组合，2026-11-01）

**R6-O 运维与部署面**：8 条（配置矩阵核查：本环 64 修复零新增 env，硬编码常量符合「功能不设开关」；健康语义未变；无新增 DDL）。要点：DD6-O1（中·**初始化文档失真**：本环把 juhe_chat 组从 ensure-schema 移除（现 5 组）但部署文档仍写 6 组/8 schema——README:9/:29/:31 与 compose.yml:4-5）；DD6-O2（低·**派生目录契约残留**：chat-assets 已无写侧/清理方，文档仍列为现行派生目录——README:360、部署指南:173）；DD6-O3（中·**env 退役承诺与 jobs 行为不符**：文档称退役 CHAT 变量会得未知变量 WARN，但 configregistry 仅 gateway 接入、jobs 零信号）；DD6-O4（低·回滚面：rev7 删步+chatbindingmigration 物理删除+唯一 rollbackSql 记载删除，实际回滚目标已晚于 rev7 风险低）；DD6-O5（低中·**droppedRetractIntents 无硬上界无 gauge**，长故障窗按请求率线性增长 20-30MB 量级）；DD6-O6（低中·新计数无抓取：PendingOverflowCount 零消费、意图表/在途集合无 gauge，与 DD3-C7 同族）；DD6-O7（低·发布窗 /health 503 尖峰与告警窗需核对）；DD6-O8（已登记 DD3-F4 终态确认）。

**R6-S 操作序列×降级组合**：15 条（H6/H9/H10/H11/H12 闭合核对通过；H2 设计接受；H13/H14/H15 待核实/未知）。要点：DD6-S1（中·**审计饱和自延长**：字节预算饱和窗内每个被丢 finalize 生成免预算撤回命令（bytes=0 绕过预算），慢 PG 上每条撤回占 worker 至 15s→4 worker 全被撤回占满→数据条目停消费→queueBytes 不释放→预算持续满→更多丢弃，与 PG 恢复后自愈）；DD6-S3（低猜测·同 tid 迟到重放×retention 清理窗→已关闭 incident 复活；触发方未定位）；DD6-S4（低·**Cleanup 注释与 SQL 矛盾**：outbox 删除无 event_type 过滤（dispatched+acked 行同删）而注释宣称「dispatched 行永生」，行为由 dispatch_revision CAS stale 臂兜住但注释会误导后人）；DD6-S5（中·compat 计数 WithoutCancel+3s 在 Redis 黑洞时每请求驻留 3s、401 已写出无背压→goroutine≈RPS×3s 放大）；DD6-S7（中·**vectorstores 删→重加×旧索引任务在途**：IndexCallback=true 命中新行以旧 attributes/chunking 覆盖，last-writer-wins 无代次守卫）；DD6-S8（中·**vectorstores 索引任务×进程重启→绑定永久 in_progress 无清扫无超时回收**，DD2-C7 只拦已删行复活不解决任务消失）。

**R6 验证轮**：7 条送验 → **6 confirmed / 1 refuted**（DD6-O3 证伪：compose 三服务共享同一 env_file，残留 CHAT 变量 gateway 启动即打 WARN，文档承诺在部署层面兑现）。

| R6-批10（2026-11-01，6 confirmed，3 写码档） | 内容 |
| --- | --- |
| DD6-O1+O2（B/C·部署文档失真） | 8 处同步：README:9/:29/:31/:327（8→7 schema、6→5 组、chat 库加表条改写为退役标注）、compose.yml:4-5 头注释、部署指南:59/:173、README:360 chat-assets 移除+退役括注。**代码事实修正**：juhe_jobs/juhe_j3b 非「bootstrap 建 schema」而是手工 CREATE SCHEMA 预置后 --apply-* 建表（已按代码改）；deploy/README.md:51 落点不存在（已核实） |
| DD6-S7（中·数据正确性） | vectorstores 索引代次守卫：回调 upsert WHERE 追加 `updated_at = ?`（方案①，`updated_at` 是每次绑定写点都推进的单调列；`created_at` 在重加 upsert 不更新无区分度），superseded 分类不写 failed；与仓内 authsys/authz 乐观锁惯例同型。已知局限：毫秒精度同毫秒碰撞窗口，无 DDL 无法根除 |
| DD6-S8（中低） | vectorstores 启动清扫：`sweepStaleVectorStoreFileIndexing` 后台一次性，超龄 10min in_progress 标 failed；**偏离任务原文**：条件用 `updated_at` 而非 `created_at`（重加不推进 created_at 会误判合法在途行），且不能同步触库（既有 `TestCreateFileSurfacesDBErrors` 钉住 NewStore 不触库契约）→ 改 `safego.Go` 后台执行；两修复互洽（清扫推进 updated_at 后旧代次回调必被 S7 守卫拒绝） |
| DD6-S1（中·饱和自延长） | 审计撤回结构隔离：新增专用有界撤回队列（256）+ 2 撤回 worker，数据 worker 不再消费撤回命令→正反馈打断（数据条目持续消费、queueBytes 释放）；`retractWaitBudget` 测试缝 atomic 化消除 -race 竞争；w13h 内联臂语义保持 |
| DD6-S5（中低） | compat 计数改有界异步：单 worker+有界 channel（256），超容量丢弃计数并 Warn（`gateway_pre_auth_failure_count_queue_full`）；请求对象字段在 handler 返回前提取，仅计数 IO 入后台；WithoutCancel 语义保留；注释记录与主链/realtime 写出前计数的语义差异 |

验证：auditlog 23.5s/cmd 35.6s 全绿（含 -race 两轮）、openaicompatstorage 全绿+DD2-C7 三例回归、部署文档 rg 失真模式零命中；新增钉住测试 6 条（DD6-S1×2、DD6-S5×2、DD6-S7×2+DD6-S8×2）。

### R5（数据演化×边界态 + 消费者契约×外部视角 + 批7/8 落点复核，2026-11-01）

**落点复核（批7-1 E8/E9、批7-2 T87、批8 A1）**：**3/3 组确认正确、0 问题**（守卫同源/mustChange 短路/日志脱敏/fail-closed；T87 两臂传参/identity 守卫 IS NOT DISTINCT FROM/重置列完整性/schema 三方一致；A1 授予点唯一/空值保守/传导链/不动面/重放轮）。回归攻击 10 个场景全通过（含 E8 第 10/11 次边界、成功后清零、并发 INCR、mustChange 非爆破面；T87 同 tid 并发双写、identity 不匹配显式错误；A1 混合窗口每轮独立无跨轮污染）。

**R5 数据演化×边界态**：18 条（H10/H11/H12/H13/H14/H15/H16/H17/H18 闭合核对通过；H6 低危边界差异；H8 设计意图；H9 观察）。待验证 4 条：DD5-E1（H1 规模×审计饱和窗：丢弃臂补派撤回在队列槽满时转 caller 协程内联 retractOne 最长 15s→请求准入面阻塞，修复前为瞬时返回；注释「最坏 10s」与 retractWaitBudget=15s 不同步）、DD5-E2（H2 存量 dispatched 行×新排除读面：「事件已 dispatched 且 incident 未清理」窗口内同 tid 重试不再命中幂等臂→CAS 插入臂重置 dispatched 行→重复投影一次，下游幂等则无害）、DD5-E3（H5 D4 写面闸×部分更新：规则缺席即 accountRuleCount=0→任何 replace 一律 400，API 直调只提交 overrides 的存量配置无法更新）、DD5-E4（H7 E8 共享守卫自锁：合法用户改密连错达阈值把登录也锁 15min——有意防劫持取舍但用户面无提示）。猜测 3 条：DD5-E5（H3 Unbounded 基座 acceptedAt<=0 边界，触发面未证实）、DD5-E6（H4 存量 gpt OAuth vendor 空值→豁免被收）、DD5-E7（H9 spool 25 万条 30s 全量扫描 IO 峰）。

**R5 验证轮**：9 条送验 → **7 confirmed / 2 refuted**。refuted：DD5-E2（incident 行未清理时首应用已推进 revision，同 tid 重试必命中 cas_conflict 到不了插入臂）、DD5-E3（PATCH 先与存量 credentials 合并，accountRuleCount=len(存量)>0，合法部分更新不被 400）；DD5-C6 主体亦 refuted（实例行 credentials 是明文占位 `"{}"` 致 PATCH DecryptJSON 失败零写入，不存在死写）——残留仅「实例提交 credentials 得 500 而非 400」的守卫不对称，属移植期既有行为，登记观察。DD5-E4 confirmed 但属 DD2-E8 有意设计（共享守卫防劫持），仅「无提示文案」子断言被证伪（429 带锁定文案），降为观察。

**R5 消费者契约×外部视角**：7 条（H2 文档代码数字级脱钩最高置信；H4/H6 跨面防护不对称）。待验证：DD5-C1（H1 操作日志前端缺 change_password 标签+筛选选项——能看不能筛）、DD5-C2（H2 connector_registry xai api_key 新增 image 域：代码组合表 49→50/mode 162→163 已改测试但权威文档 §2 仍 49/`chat、video`，外部可见能力扩展无文档依据）、DD5-C3（H3 DATA_DIR 派生目录 playground-artifacts 未进部署文档 README 目录清单，违反部署同步先例）、DD5-C4（H4 compat 401 计数在响应写出后且用请求 ctx——客户端读 401 后立即断连→ctx 取消→计数失败仅 Warn→系统性规避 preauth 计数，与主链/realtime 早于写出的强度不等）、DD5-C5（H5 熔断 429 响应头清单文档滞后：Connection: close 三面统一但 IP 熔断设计/Realtime 设计响应头清单未列）、DD5-C6（H6 授权实例错误策略读写口径三分：写面仍接受 error_handling_* 写实例行但读面+运行时均取来源→死写；调度字段有 400 拒绝臂而 error_handling 键无同款守卫）。观察：DD5-C7（H7 accounts 列表信封多 generatedAt vs logreads 无，Node parity 产物低危）。

| R5-批9（2026-11-01，R5 验证 confirmed 6 条，2 写码档） | DD5-E1（丢弃臂去内联：非阻塞 tryEnqueue 失败则登记外挂意图表，worker 侧 drainDroppedRetractIntents 补派——零新 goroutine、闭环论证槽满前提=worker 在场；retractBestEffort 注释修正并声明仅限 worker 协程；**变异验证**：还原内联后钉住测试死锁，证实生产等价形态比报告 15s 更严重）；DD5-C4（compat 401 计数改 `context.WithoutCancel`+3s 超时，断连不再规避计数）；DD5-C1（前端 actionTextMap+actionOptions 补 change_password，vue-tsc/eslint 零错误）；DD5-C2（连接器组合契约文档同步 7 处：xai api_key 组合表+49→50/xai=4/162→163/mode 分布/验收清单/拒绝句订正；契约测试绿、旧值 rg 零命中）；DD5-C3（README 派生目录补 playground-artifacts 及语义句）；DD5-C5（IP 熔断设计+Realtime 设计 429 响应头补 Connection: close） | auditlog 23s/cmd 36s 全绿；契约测试 0.28s；前端 typecheck+lint exit 0 | 待复核（随下轮） |

### R4（扩展视角 + 落点复核 + jobs 扩散，2026-10-31）

- **落点复核（R2 批4 13 条 + R3 批5 9 条）**：**22/22 确认正确**，文档契约同步抽查 4 处一致；R2×R3 同域交叉协作序核验正确（C1 补计×D1 err 区分、C2/C4×G1 意图链、F1/F3×G1/G2/RC-A2 停机链终态）。发现 1 低：DD4-RC1（C2 单条超限臂×finalize 类壳行窄缺口）→ 已修复。
- **jobs 扩散验证**：DD3-B1 confirmed（高——游标永久缺口，读序两段非原子）、DD3-B5 confirmed（高——dedupe 行永生+行缺失硬错误；且 dispatched 行本身无界泄漏）。
- **R4 修复批（批6，3 条）**：DD3-B1（jobs worker_ingestgate 读序调换，单向迁移论证入注释，纯函数级三测试）；DD3-B5（**方案偏移已验证**：验证者给的 status 过滤单独不充分——真实 PG 实测插入臂撞 UNIQUE 23505，配套插入臂 ON CONFLICT DO UPDATE 冲突重置（status 回 pending/attempt 清零/identity 守卫防 transition_id 复用），真实 PG 三测试绿）；DD4-RC1（超限臂对齐 retract 口径，AuditLogID 回落）。**连带登记 T87**（gateway 写侧同构缺口：circuit_control_plane outboxByDedupe 无 status 过滤+insertOutbox 纯 INSERT+Cleanup 无 caller——待后续批处理）。

### R3 验证轮与修复批（2026-10-31）

**R3 验证**：首批 6 条最高优先候选全部 confirmed（DD3-F1 中高/F3 中/C2 中/C4 低中/D1 中/A1 中低）。剩余候选（B1 游标缺口、B5 dedupe 永生、B9/B10 供应商语义、C7 指标、D2 阈值漂移、RC1 decode panic 理论缺口等）保持假设/观察登记待后续轮。

**R3 修复批（批5，A 类 9 条，3 写码档，全绿含 -race）**：DD3-F1（F3 组件 Close 改 f3OwnerClose 先租约后 store，对齐 F4 结构；wiring guard 防退回）+DD3-F3（startupLeaseRelease 钩子，fail() exit 前尽力释放已获取租约，nil 安全）；DD3-C2（丢弃臂派 retractBestEffort——retractID=EnvelopeID 为空回落 AuditLogID，因 finalize 与信封共用同一 auditLogID；单条超限臂在登记前不派撤回为钉住契约）+DD3-C4（Retract 入队 3 次耗尽臂 registerAbandonedRetract 登记意图，settle 补派）；DD3-D1（compat scope resolver 区分 err 源：基础设施 err 经 handleOrchestratorError 渲染 503/500 不计数——主链同窗同错口径；仅 key 无效 401+计数；预解析+请求内 memo 不增读）+DD3-D3（compat/realtime 429 补 Connection: close 对齐注释与主链）+DD3-A1（runtime 单飞装载改 Background 对齐 union 先例，首发取消不再传染同 key 等待者）。文档同步：OpenAI兼容Files设计 §7 追加 scope 错误源区分与 429 close 契约。

**R3 收口统计**：攻击 6 角色 ~80 条假设（含核验通过面：union 成员变更闭合收口 DD2-C11、G9/T63/E1/G8 修复批新代码交错核对通过、spool 写半截不可达、jobs restoreIfRecovered 收敛、键空间六族零交叉、compose 无 autoheal 修正 G14 框架）；送验 6/6 confirmed；A 类修复 9 条全绿。R3 存在修复回归风险待落点复核；收敛计数 0/3 维持（B 类 2 条仍待批）。**遗留移交**：jobs 侧两条高价值（B1 游标永久缺口、B5 dedupe 永生毒化——jobs 不在锚定范围，建议另立 jobs 深挖或随本环扩散轮处理）；B9/B10 需供应商 rotation 语义实测；B11 重授权 409；C7 指标接线；D2 阈值漂移；C12/C13 设计确认项；DD3-RC1 decode panic 理论缺口；单条超限臂×已落库信封壳残留（低）。

### R2 攻击轮（场景制，2026-10-31，7 入口组角色并行）

假设编号 DD2-<入口><序号>。核验性通过（直接入覆盖矩阵）：DD2-A10/A12/A13/A14、DD2-B复核组（C6 槽换代隔离/G9 全场景/bridgeTakeCompleteSseEvents 无调用者）、DD2-E3/E6/E7/H16、DD2-G核验组（renewLoop stopCh、panic LIFO、估算对称、D7 救场可达、30s 缓存自洽、supervisor Close 不阻断、Handle.Close 幂等）、DD2-F证伪组（SSRF/越权）、DD2-D排除复核（E4 本体方向正确、patch 合成方向无碰撞）。

| ID | 来源 | 假设（一句话） | 关键依据 | 状态 |
| --- | --- | --- | --- | --- |
| DD2-A1 | A | F3 身份门自证式：CodexTurnPresent 仅由请求自带 x-codex-turn-metadata 头（任意含 turn_id 的 JSON）+路径判定，无服务端资格校验——任意 API Key 附头即领无界预算，修复被架空 | strategy.go:185-198/536-554、preflight.go:320-323/350-361 | 假设（高优先验证） |
| DD2-A2 | A | 压缩标记可被字符串内容翻转：raw 边缘正则命中 JSON 字符串值内文 `"type":"compaction_trigger"`，标记失真+配合 A1 在普通 /responses 解锁预算 | compactioncontract.go:19/93-124/127-177 | 假设（高优先验证） |
| DD2-A3 | A | 非 codex compact 首字超时击杀×compactionExpected=true 分档错位（设计取舍一致性观察） | preflight.go:286/323、preflighthelpers.go:230-248 | 观察 |
| DD2-A4 | A | codex 非流式 responses 拿不到 codexTurn（DownstreamResponsesSSE 门），真实 codex 压缩非流式形态被墙钟截断（猜测） | strategy.go:186-187 | 假设（低） |
| DD2-A5 | A | **C2 越限放行在事件清点之前**：pending>8KB 时整段（含其中已完整事件）原样透传，完整事件不再过响应检查策略——注释「仅放弃未完成事件」与行为不符，恶意上游可投递本应被拦内容 | interceptor.go:78-92/82-86 | 假设（高优先验证，修复回归） |
| DD2-A6 | A | \n\r\n（LF 数据行+CRLF 空行）gatewayresponse 三处词表不识别而 bridge 识别（C1 取齐未完成的具体化）：直连流事件滞留→越限透传+missing_terminal 失败结算/重复计费 | interceptor.go:229-253、streamusageinject.go:113-131、gatewayopenai/buffer.go:555-557 | 假设（高优先验证，RC-C4 具体化） |
| DD2-A7 | A | B7 memo 注册表仅 ≥1024 插入时惰性清扫，高峰在飞请求数即峰值（低危容量面非泄漏） | chain_request_failure_health.go:139/152-166 | 观察 |
| DD2-A8 | A | C6 请求级 carrier 单槽：若同请求两个并发上游响应面调用，后换槽覆盖前槽致 usage 串账（猜测，并行可达性未证实） | chain_bridge_response.go:63-64/753-774 | 假设（低） |
| DD2-A9 | A | F1 恒取 UpstreamModel 后空串 UpstreamModel 不再回落 RequestModel，与桥 model 选择空回落口径分裂（写侧校验未验证） | chain_driver.go:623-633、gatewayopenai/mapping.go:106-119 | 假设（低） |
| DD2-A11 | A | D1 撤回在 worker 协程内轮询等待 settle 占死 worker ≤15s：批量取消×慢窗下管线吞吐停摆+放弃撤回空壳回归（reaper 兜底） | producer.go:485-521/537-547 | 假设（与 G2 同源） |
| DD2-B1 | B | **C6 截断流部分 usage veto 估算**：上游 clean EOF 无 message_stop 时 pump 判 Failed 但快照照写（{N,0}），finalize 不看 pipeResult.Completed→截断流按 0 output 计费（修复回归） | chain_bridge_response.go:320-335、finalize.go:400-410 | 假设（高优先验证） |
| DD2-B2 | B | C6 gemini 半程 usage（仅 prompt_tokens 或空对象渲染三键）快照 veto 估算（同族回归） | bridge_response.go:1345/1154-1174 | 假设（高优先验证） |
| DD2-B3 | B | 桥泵混合行尾（data 行 CRLF+空行单个 CR 的 \r\n\r 形）不识别→两事件融合→JSON 解析失败整流断 | bridge_stream.go:18-41、bridgejson.go:196-206 | 假设（中） |
| DD2-B4 | B | **realtime 401 风暴连坐**：token 过期/Redis 丢失后 WS 客户端无退避重连，8 次/分打满 invalid 桶→同 IP 全部 Bearer 流量被封 30s 起步（A3 修复引入的连坐面，设计权衡） | chain_realtime.go:783-787、circuit.go:20-24 | 假设（裁决/缓解设计） |
| DD2-B5 | B | realtime token 有效但绑定 Key 已禁用的 401 不写 preauth 计数（与 Bearer 面无效 key 同型计数不对称） | chain_realtime.go:807-810 | 假设（低） |
| DD2-B6 | B | gemini interactions ID 晚到×重放窗先关→BeforeDownstreamCommit 空 ID 调 Remember no-op，真 ID 永不补写→亲和缺失（事件序猜测成分） | pipehelpers.go:31-37/99-113、gatewaygemini/streaminspection.go:343-349 | 假设（低） |
| DD2-B7 | B | C2 越限放行段内 usage/终态帧绕过 inspector（T9b 未接线方向计费回落估算；C6 钉住方向不受影响） | interceptor.go:40/86-103 | 假设（并入 A5 验证） |
| DD2-B9 | B | F6 已存在则 Add：链上前序已写同名头（如默认 Content-Type）时上游头叠加双值（猜测） | finalize.go:766-773 | 假设（低） |
| DD2-B10 | B | C6 非流式桥接槽永不写（buffered transform 路径）→T86 覆盖面确认 | chain_bridge_response.go:113-137 | 已登记（T86 内） |
| DD2-C1 | C | **compat 认证失败永不进熔断计数**：A2 守卫只 Inspect 不 Record，compat 族 401 枚举零成本，熔断对 compat 族仅读主链喂的状态（A2 修复缺口） | chain_v1.go:1186-1190、preauth.go:352-357 | 假设（高优先验证，修复缺口） |
| DD2-C2 | C | compat 黑名单/熔断拦截路径无审计（AuthFailure 字段死存储，compat 路径未装 sink） | chain_v1.go:1166-1167/1214-1215 | 假设（中） |
| DD2-C3 | C | compat scope 解析把 DB 存储错误折叠成 401（主链同窗口 503），口径不一致 | chain_openaicompat.go:82-84 | 假设（中） |
| DD2-C4 | C | **multipart purpose 字段 io.ReadAll 无上限**（busbus 1MiB fieldSize 未移植）→数 GB 字段 OOM | openaicompatstorage/filesroutes.go:450 | 假设（高优先验证） |
| DD2-C5 | C | compat 分支无 body 预检/读超时（bodyPipeline 只挂协议链），慢速滴流占连接（进程级读超时未验证） | chain_v1.go:148-153 | 假设（中） |
| DD2-C6 | C | T82 畸形×chain 面 /v1/models+计价扩散（W45 只登记 chat 面）：item JSON 形状不符整目录硬错误+该账户 cost NULL | chain_catalog.go:889-892、chain_usage.go:1017-1020 | 假设（W45 扩散面） |
| DD2-C7 | C | **vector_stores 删除×后台索引复活竞态**：先插 in_progress 行异步索引，完成时 upsert deleted_at=NULL 复活已删行 | vectorstoresroutes.go:383-404/519、vectorstoresstore.go:505-541 | 假设（高优先验证） |
| DD2-C8 | C | files 无 per-Key 配额/无清理任务/删除错误忽略→磁盘 DoS+Key 删除残留 | filesroutes.go:363/380-414、store.go:311-332 | 假设（裁决面） |
| DD2-C9 | C | vector_stores expires_at 只渲染不强制（过期仍可列/搜） | vectorstoresstore.go:172/234 | 假设（低） |
| DD2-C10 | C | 账户目录只滤 status=active 不过滤 shutdown_date/catalog_visible | chain_catalog.go:224-233 | 假设（低） |
| DD2-C11 | C | union 缓存 1h TTL×超分组成员变更是否触发失效未取证（/v1/models 最长 1h 陈旧） | gatewayruntimecache/union.go:31 | 待补证 |
| DD2-D1 | D | **Grok sso-to-oauth 收 credentialsPatch 但兑换链从不应用**（exchangeGrokSSOToken 仅 buildGrokOAuthCredentials 无 safePatch），前端恒发→SSO 建户静默丢策略（B5/E4 同族） | oauthmgmt/routes.go:1009/1094-1099/1143-1153 | 假设（高优先验证） |
| DD2-D2 | D | **授权实例高级详情 EffectiveErrorHandlingRules 用空 ownerCredentials**（stamped 行 credentials 列 SELECT 为 NULL）→来源规则与 overrides 全部不可见，与同函数 quota policy 口径相悖 | accounts/m11_reads.go:394-395 对照 370-384 | 假设（高优先验证） |
| DD2-D3 | D | overrides 多条目读面只取第一条（循环无条件 break），写面接受任意长度——写读不对称 | m11_reads.go:94-99、error_policy.go:347-384 | 假设（高优先验证） |
| DD2-D4 | D | replace 越界 rule_index 无上界校验→系统规则被无声抑制且无替换体 | error_policy.go:368-372、m11_reads.go:122-137 | 假设（高优先验证） |
| DD2-D5 | D | batch-update 未挂 MutationGuard（同族均挂），双击落 CAS 409 而非防重复文案（操作日志语义） | accounts/routes.go:68 | 假设（低） |
| DD2-D6 | D | oauth 建号防重复指纹不含 credentialsPatch——400 后修正重提被 409 误拦（failed TTL 窗内） | oauthmgmt/routes.go:84-120 | 假设（中） |
| DD2-D7 | D | 并发同 state 建号（指纹不含 groupId）双账户风险（上游单次码则收敛）（猜测） | openai.go:110-143 | 假设（低） |
| DD2-D8 | D | 克隆凭据投影缺 error_handling_rule_overrides 键→克隆体静默丢策略（E4 键族旁路缺失） | accounts/clone.go:33-35/441-449 | 假设（高优先验证） |
| DD2-D9 | D | 轮换链对存量畸形 overrides 零校验，畸形值存续致 FindAdvancedDetail 500（猜测） | oauthmgmt/store.go:885-963 | 假设（低） |
| DD2-E1 | E | A1 Lua 对「Go 可解析但 Lua 敌对」JSON（samples:null 等）无 pcall→运行时报错→该键后续每次打点 500 直至 TTL，键不自愈（触发源未证实） | circuit.go:321-323/334/488/509-511 | 假设（中，触发源待证） |
| DD2-E2 | E | 毒键 Go 读自删×Lua 覆盖写并发交错可复活毒键一个 TTL（低危） | circuit.go:319-324/362-363 | 观察 |
| DD2-E4 | E | IPv6 不聚合→惩罚窗按源地址数无限稀释（preauth spray 同病，既有语义继承） | aipublic.go:253-257 | 观察（既有） |
| DD2-E8 | E | **改密面无暴破防护**：postChangePassword 对非 mustChange 账户逐次 Verify 凭据，失败仅 400，无 LoginGuard/验证码（仅全局用户写限速）——与登录守卫强度不对称，会话劫持下可在线暴破当前口令 | authsys/routes.go:314-328 对照 119-133 | 假设（高优先验证） |
| DD2-E9 | E | 改密全程无操作日志（patchMe 有）——审计链缺失（Node parity 未核） | authsys/routes.go:293-343 | 假设（中） |
| DD2-E10 | E | 改密 Verify 与 Update 无版本 fence，并发改密互删会话（可用性非提权） | authsys/routes.go:319-338 | 假设（低） |
| DD2-E11 | E | mustChangePassword admin 双读不一致（会话臂 403 门 vs store 臂豁免展示）——D-214 语义需裁决 | modelcheckauth/auth.go:154-175 对照 store_accounts.go:1122-1125 | 假设（裁决） |
| DD2-E12 | E | 临时令牌面对密码正确但非 admin 记登录失败→白名单内自锁+第三方可触发（攻击条件强） | authsys/routes.go:382-397 | 假设（低） |
| DD2-E13 | E | realtime ?token= 经 query 可能进访问日志/审计 OriginalURL 明文落盘（120s 短效缓解，日志写点未逐一核实） | chain_realtime.go:769 | 假设（低） |
| DD2-E15 | E | RecordClientIPErrorCircuitSuccess（GET+DEL）是 A1 后残留的最后一处非原子读改写（少计方向安全） | circuit.go:607-620 | 观察 |
| DD2-F1 | F | **媒体任务计费只由客户端轮询驱动**：客户端放弃/断开→上游实际完成的成本永不入账，7 天后被 retention 静默删除（平台成本泄漏，设计级） | chain_media_video.go:1446-1531、jobs retention/mediajobs.go:70-104 | 假设（高优先验证，设计裁决） |
| DD2-F2 | F | Key 删除/失效后存量 in_progress 任务永久无法推进终态（同族计费缺口） | jobsrepo.go:233-242、chain_media_video.go:1331 | 假设（并入 F1） |
| DD2-F3 | F | 取消/上游已开始生成→上游成本无 usage 记录（本地 cancelled 短路轮询） | chain_media_video.go:1615-1655/1962 | 假设（并入 F1） |
| DD2-F4 | F | 媒体创建响应短路绕过 Finalize→创建请求（含失败尝试）零审计痕迹（任务面豁免有注释，创建面无文档依据） | chain_v1.go:625-629、audit_capture.go:1246-1252 | 假设（中） |
| DD2-F5 | F | **capability_domain 无派发闸**：候选链无域过滤，gemini chat 域账户（空 modes/models）可提交视频任务真实派发——「域创建时确定」只是记录字段不构成闸（文档↔实现差距） | chain_driver.go:914-1004/2026-2044、chain_accounts_secret.go:487/522-523 | 假设（高优先验证） |
| DD2-F6 | F | n>1 多段任务按 1 段计费（PerCall 不乘 N/秒价单值）——少计 ×N（部分需目录数据实证） | videoadapter.go:128/152、chain_media_video.go:1844-1879 | 假设（中） |
| DD2-F7 | F | 同步付费 lane（/v1/images、TTS）5xx 换候选重试→上游双份成本（与「图片诊断不重试」裁决面不对称） | chain_ports.go:522-569、chain_media_video.go:396-436 | 假设（裁决面） |
| DD2-F8 | F | audio 任务在途×账户被探针置非 active→任务面 503 结果丢失（video 有恢复态准入，audio 要求恒 active） | chain_media_video.go:1358-1395 | 假设（中） |
| DD2-F9 | F | 账户 vendor 被改写后历史任务 adapter 解析失配→永久不可达 | chain_media_video.go:1362-1365/342-353 | 假设（中） |
| DD2-F10 | F | 上游 2xx 受理×本地落库失败→重试双份成本+孤儿上游任务（代码注释自认） | chain_media_video.go:1107-1116/1040-1047 | 已裁决接受（自认风险） |
| DD2-F11 | F | 终态任务×SupportsCancel=true 转发上游 DELETE 误 502/204 语义混淆 | chain_media_video.go:1615-1655 | 假设（低） |
| DD2-F12 | F | audio 探针结论被任务 completed CAS 翻回 active（探测准确性受损） | chain_media_video.go:974-1008/1514-1516 | 假设（低） |
| DD2-F13 | F | 媒体创建零预扣，负余额窗口最长 7 天（preauth 成本估计面未验证） | chain_media_video.go:1492-1516 | 待补证 |
| DD2-F14 | F | content 转发先 Set 上游 Content-Length 再 io.Copy 无完整性校验（谎报/截断） | chain_media_jobs_center.go:110-119、chain_media_video.go:1591-1603 | 假设（低） |
| DD2-G1 | G | **D1 修复在其目标慢窗内自身失效**：15s 撤回预算 < persistOne 最坏 20s（renew 10s+persist 10s），settle 落在 t≥14.9s 时撤回无剩余 DELETE 预算必败→空壳滞留至 success retention 3 天（修复效力趋零边界） | producer.go:492/503-510/537-544/575/584 | 假设（高优先验证，修复边界） |
| DD2-G2 | G | **存储故障×撤回放大×饱和三重叠加**：每条失败 persist 派撤回，撤回内联占死 worker 至 15s，4 worker 全内联期间队列只进不出→审计吞吐坍缩每 15s≤4 条+丢弃激增（业务请求不受影响） | producer.go:54/426-432/599-603 | 假设（高优先验证） |
| DD2-G3 | G | **正常停机 composed.Shutdown 在 F3/F4 store 关闭之后执行**——尾部排空窗口的审计写全落空（G1 修复未覆盖 composed 排空时序；唯一活口是 hijacked realtime） | main.go:646/826/990 | 假设（高优先验证） |
| DD2-G4 | G | 进程退出丢队列的二阶后果：已落库信封的 finalize/retract 命令随队列丢失→每次停机新增 ≤4096 空壳、按 success retention 3 天延迟回收（有界契约成立，半径写明） | compose.go:185-191、audit_capture.go:1246-1302 | 观察（契约内） |
| DD2-G5 | G | 双重 SIGTERM 不强杀（NotifyContext 未 Stop）+recorder.Close 无预算 wg.Wait（慢盘挂起时 SIGTERM 后不死；failRuntime 有 10s 硬预算而正常路径无全局预算） | main.go:960-961、chain_usage.go:224-234 | 假设（中） |
| DD2-G6 | G | **「chat 生成 8s 排空」注释能力不存在**（全仓无该实现），composed.Shutdown 实际无带预算的生成排空，failRuntime 10s 预算推演建立在不存在的能力上（注释失真） | main.go:1304-1308/1322-1325、chain_compose.go:854-863 | 假设（高优先验证，注释失真） |
| DD2-G7 | G | F3 keeper 关闭窗口×宽限失败：Close 窗口含 composed.Shutdown，>60s 挂起时 renewLoop lost→Close 跳过释放（低概率组合） | auditlog/owner.go:124/157-173/199-208 | 假设（低） |
| DD2-G8 | G | .corrupt 7 天 TTL 从原文件落盘时刻起算（jobs 改名继承 mtime）——积压数天后隔离的现场保留期被压缩（低） | spool.go:34/239-240、drain.go:259 | 观察 |
| DD2-G9 | G | 近期 .corrupt 洪峰未过期时单次 scan 无法恢复配额，补偿失败持续至 7 天（低概率） | spool.go:141-150/239-256 | 观察 |
| DD2-G12 | G | failRuntime 只释放 F4 lease 不释放 F3 lease（与注释「lease 是错误退出唯一跨进程语义」的口径不对称，未明示理由） | main.go:700/1310-1315/1327-1344 | 假设（与 F3-auditLease 观察同源） |

## 修复记录

| 批 | 内容（指向问题记录文档） | 验证证据 | 落点复核轮次 |
| --- | --- | --- | --- |
| R1-批1+批2（2026-10-31，A 类 22 条，7 写码档并行，未 commit） | DD-D1（producer 在途信封集合+撤回延迟生效；dd_d1_inflight_retract_test.go 5 例，w13g/w13h 保持绿）；DD-D4（Marshal 移入 worker：DeferredCapture/CaptureDeferred+估算记账；等价回归 3MB body 逐字节一致+记账测试）；DD-D5（1MB 内联门；L2 指纹独立门槛已核实不退化）；DD-D8（DroppedCount 死字段删除，主代理直修）；DD-G1（HTTP 排空包装为最后注册组件先关，supervisor 未动；shutdown_order_test 5 例）；DD-G2（failRuntime 有界排空 10s 后 exit(1)）；DD-G3/G15（注释回正）；DD-E1（rehydrate 按账户 BoundGroupID 直读候选；supergroup_test 链级 2 例）；DD-E4（四家 patch 收 error_handling_rule_overrides 与写面对齐）；DD-F1（覆盖入参改映射右侧 UpstreamModel）；DD-F2（service_tier 按最终 family 分支）；DD-A1（熔断计数 EvalLua 原子化）；DD-A2（compat 分支补入口保护）；DD-A3（aipublic 惩罚窗+realtime 失败计数）；DD-A4（aipublic 256KiB body 上限）；DD-G9（realtime 计数归零删键）；DD-C1（SSE CRLF 边界补齐三处词表）；DD-C2（pending 上限+越限放行）；DD-C6（桥接 usage 快照通道，剩余缺口=T86）；DD-F6（多值头保留）；DD-B7（weak memo 免重复 DB 查询） | 触及 11 包聚合测试全绿+五模块构建通过；文档同步 6 处+待办 T86 | R1-落点复核（19/22 确认，4 RC 即修闭环，见下） |
| R1-批3（2026-10-31，B 类批复 2 条 + 同族新发现 1 条，主代理与 2 写码档） | DD-F3（compact 预算豁免收 codexTurn 身份门，标记/预算面拆分，文档 4 处同步）；DD-D7（.corrupt 7 天保留 TTL 进 scanCapacity 清理周期）；**DD-D10 新发现即修**：spool 两镜像常量无类型整数按纳秒比较——容量缓存「30s」实际 30µs、.tmp「1h」实际 3.6ms（DD-D7 写码档初版同型错误暴露），恢复镜像契约语义（30*time.Second/time.Hour，注释立规）；.corrupt 修复初版曾把 err 作用域缺陷引入（空指针 panic，测试暴露已修） | gatewayusage 全包 2.2s 绿（含 w13d/.tmp 回归与 DDD 三例）、gatewaypreauth/gatewaycodex/cmd 定向与全包绿 | R1-落点复核（批3 为收口批复项，落点由各写码档自证+负向场景钉住；下轮复核覆盖） |
| R2-批4（2026-10-31，R2 验证 confirmed 13 条 A 类，3 写码档+1 后台补差） | **流式回归**：DD2-A5（越限检查移到清点循环后，完整事件先过策略仅放行无边界尾段）、DD2-A6（shiftSseEvent/sseEventEnd/pendingSseEventBuffer 三处补 \n\r\n 与 bridge 词表取齐，优先序逐字节核对无截断）、DD2-B1/B2（快照消费加 pipeResult.Completed 条件，截断/半程 usage 回落估算，完成流快照保持权威）；**compat 计数**：DD2-C1（compat 401 经 TrackingWriter 落定后按状态码补记 RecordPreAuthFailure，fire-and-forget）；**D1 边界**：DD2-G1（在途集合值改 retractRequested 标志，撤回放弃时原子登记、成功 settle 臂 needsRetract\|\|retractRequested 补派幂等撤回；负向验证：回退修复→主臂测试转红→恢复）；**移植缺口**：DD2-C4（multipart 字段 1MiB LimitReader 对齐 busboy fieldSize）、DD2-C7（索引回调 upsert 加 deleted_at IS NULL 条件 RETURNING 判空回滚，用户面重加复活契约保留）、DD2-D1（sso-to-oauth 补 safePatch+mergePatchLast 应用 credentialsPatch）、DD2-D2（读面规则改用 effectivePolicySource 与 quota 对齐，文档契约同步修订 :143）、DD2-D3（读面处理全部条目末条生效，Node 亲代遗留有意收口）；**停手裁决**：DD2-D8（取证证明缺键=文档明文契约「克隆不复制覆盖」非移植缺口→已裁决接受）、DD2-D4（已补差落地：error_policy.go ForWrite 入参 accountRuleCount+credentials_normalize.go 漏斗 4 行扩展，越界 ValidationError；读面/运行时对历史存量保持宽容；连带修正 R1 DD-E4 钉住测试夹具「replace(0) 无同批 rules」为退化形态——补 rules 使 replace(0) 合法、断言不变；文档 :133 追加上界契约） | gatewayresponse/gatewayopenai/auditlog/openaicompat/oauthmgmt/accounts 定向与全包测试绿（含真实 PG 门控用例 + DD2-G1 负向验证转红）；cmd 全包 37.8s 绿；文档同步 3 处（上游额度不足继承错误策略:143、OpenAI兼容Files:52/:88、官方可落地OAuth:124） | 待复核（R2 落点复核随下轮） |

## 裁决清单（C 类，持续积累）

| ID | 一句话 | 来源轮次 | 状态（待裁决/已裁决+结论） |
| --- | --- | --- | --- |
| DD-F7 | in-flight 请求物化 JSON map 放大不计量（256MB raw 预算外峰值），Node 同构容量决策 | R1 | 待裁决 |
| DD-G4 | http.Server.Shutdown 不等 hijacked 连接，realtime 会话停机截断（修复需自 track 机制） | R1 | 待裁决 |
| DD-G5 残余 | /__aisys__/api/{,my-}model-checks/ 路径落 system API 前缀却无限流/安全头（J3b 绕 kernel 主体已裁决登记，此差异未单独登记） | R1 | 待裁决（登记差异或补齐防护） |
| DD-G12 | 健康面容器内绑 0.0.0.0+ALLOW_NON_LOOPBACK，metrics/pprof 同网容器匿名可达（宿主发布绑 127.0.0.1 核对无误，文档性防线） | R1 | 待裁决（是否加技术围栏） |
| DD-G16 | 启动期 spool 目录不可写仅 ERROR 继续启动，用量交接断链运行期才暴露 | R1 | 待裁决（是否 fail-fast） |
| DD-B5/B6/B9 | L2 NAT 粒度互撞（低危）/L2 order 死代码 O(n) 扫描/ledger 缓存无淘汰（缓慢无界，量级小） | R1 | 待裁决（择机清理） |
| audit_summary sha 无门 | SummarizeAuditPayloadForLimit 对超限 body 无门重算 sha（Node 镜像有门）；加门会改 payload-summary JSON 契约，需先裁决字段形态 | R1 | 待裁决 |
| DD-G8 | codex/anthropic 用量头等 fire-and-forget goroutine 无并发上限（镜像 Node priority:low 契约，池饱和事故态才堆积） | R1 | 已裁决接受（Node 镜像语义） |
| DD-G10 | usageStatsTimezone 双源（statreads 直查 vs ipstats 60s TTL），继承 Node 每读直查契约 | R1 | 已裁决接受（继承语义） |
| DD-B2/B3 | T63 L1 5xx 瞬态计入/L2 无成功清除——设计 §4/§7/§12.2 明文裁决（含残余误伤登记） | R1 | 已裁决接受（设计 §4/§7） |
| DD-C4/C8 | W32 reasoning-only 空完成（设计 §5.1 残余 3 登记）/gemini 桥 EOF 合成 STOP 收尾（w14a 测试钉住+流式中断调研明文豁免） | R1 | 已裁决接受（设计登记+测试钉住） |
| DD-C5 | T53 Hijack 降级分支当前部署不可达（Caddy 明文 HTTP/1.1 恒可 Hijack），文件头已登记 | R1 | 已裁决接受 |
| DD-D6 | Cancel 撤回队列满 3 次重试阻塞 ≤300ms——2026-10-29 W13 两步收口有意设计 | R1 | 已裁决接受 |
| DD-D14 | >2h 超长流 reaper 先删信封、finalize 走 INSERT 恢复终态（行为边界无残留） | R1 | 已裁决接受 |
| DD-F9 | 内容指纹四锚严格性 fail-closed 按设计（内容指纹会话身份设计 §明文） | R1 | 已裁决接受 |
| DD-A7/A8/A9/A11 | 额度后置记账+5s 缓存（Node 同构）/无效 Key LRU 挤出（Node 同构低危）/禁用传播 10min 有界陈旧/时间计划依赖 jobs 翻状态（设计依赖） | R1 | 已裁决接受（Node 同构）+A11 观察 |
| DD-D2 理论缺口 | flushFinalizedAudit gateway_failed 零尝试丢弃分支不查 inProgressAuditEnqueued（无可达并发触发路径） | R1 | 观察 |
| DD-D13 | DispatchAuditLogToGo traffic 门信封/finalize 各跑一次不对称（当前接线恒 gateway，休眠缝） | R1 | 观察 |
| DD-G14 | owner RunOwner 失败→503→docker 重启循环边界紧（JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT=45s vs start_period 75s 内可自愈） | R1 | 观察（到期复核） |
| DD-RC-C2 / DD-RC-C4 / F3 auditLease | 落点复核登记项（空值头边界/桥泵 \n\r\n 词表残余不一致/failRuntime 路径 F3 租约跳过） | R1-落点复核 | 观察（低） |
| DD-F5 | native gemini :generateContent+body stream:true 的下游消费与上游字段行为缺证 | R1 | 待补证 |

## B 类过目清单（终态）

| ID | 处置 |
| --- | --- |
| DD-F3 / DD-D7 | ✅ 已批复修复（见修复记录批3） |
| DD2-E8（改密暴破防护）+DD2-E9（改密无操作日志） | ✅ 已修复（2026-10-31，按用户常设指令「是真的有问题那么就修复吧」执行）：postChangePassword 非 mustChange 分支接入登录面同款 LoginGuard（同键空间 10 次/10min、429+Retry-After 登录面同文案、fail-closed、成功 Success 清计数）+成功写 auth.change_password 操作日志（不含密码值）；mustChange 首改不经守卫。文档同步 3 处（核心功能设计:75/:80、接口契约:143；操作日志设计已有条目）。4 钉住测试全绿（含 JUHE_AI_TEST_EXTERNAL 门控 2 例实跑 PASS） |
| DD2-A1（F3 门自证式架空） | ⬆️ **转 C 类裁决**：修复方向属真设计裁决（预算跟随真实上游账户=服务端锚但需重构 preflight→dispatch 预算传递 vs 有限硬上限=需改 BUG-0266 冻结契约 vs 接受现状+登记=滥用面为持有效 Key 者自伤式占用），不擅定，见裁决清单 |

## 裁决清单补充（R4 后）

| ID | 一句话 | 状态 |
| --- | --- | --- |
| DD2-A1 | compact 预算豁免锚定方向三选 | ✅ **已裁决选①并实施（2026-11-01，用户裁决「不影响功能可改造」）**：attempt 装配点闸——仅 wall.Unbounded 且候选窗口含非真实 codex OAuth 账户（Type==oauth && CodexOAuthVendorEligible(vendor)，复用既有 gpt vendor 口径）时覆盖为常规 270s 预算+TimeoutsDisabled=false；纯 codex OAuth 窗口豁免完全保持（真 Codex Desktop 零变化）；混合窗口偏保守取「伪造必掐断」；T49 标记/keepalive/T58/媒体桥接排除面零改动（①-⑥ 九测试钉住+cmd 全包回归绿）；文档同步 3 处。混合窗口逐 attempt 精确化需改 gatewaydispatch，另行裁决 |
| T87r | dispatched outbox 行无界累积（量级小，正确性已无害化），清理接线归属待裁决 | 观察（3 个月观察期） |

| ID | 一句话 | 修复结果 | 验证证据 |
| --- | --- | --- | --- |
| DD-F3 | /v1/responses/compact 预算豁免无身份门 | ✅ 已修：识别面（T49 标记）与预算面拆分，预算豁免收敛到 codexTurn 身份门（ClientStrategyContext.CodexTurnPresent 投影，与 keepalive 同判定源）；非 codex 客户端 compact 请求恢复常规墙钟/attempt 预算。文档同步 4 处（使用记录延迟健康分档:85、调度内核通用化 §5.2、策略路由设计:239、速度优先延迟切换 8 处） | 复核证据链四环节成立；TestWhPreflightCompactionTimeoutsDisabled 4 臂（codexTurn 豁免保持/非 codex 常规预算+标记仍 true/非压缩不豁免/body trigger 同口径）+ T49 标记回归 + preauth/gatewaycodex/cmd 全包绿 |
| DD-D7 | spool .corrupt 无回收路径且计入容量配额 | ✅ 已修：spoolCorruptFileRetention=7 天，scanCapacity 同一清理周期顺带删除（复用 .tmp 模式）；未到期保留+计入配额不变（w13d 不回归）；清理失败仅 Warn 照常计入不阻断。无文档记载处，不新增文档 | 复核证据链七环节成立；TestDDDSpoolCorrupt 3 例（保留+计入/过期清理+配额回收/清理失败不阻断 Windows 占句柄注入）+ w13d 回归 + gatewayusage 全包绿 |

## R1 轮报（2026-10-31 收口呈报）

- 轮次视角：维度制（7 攻击面：入口认证/调度派发/流式管道/用量审计/OAuth 凭据/请求体协议/cmd 组合根）。
- 统计：攻击假设 84（含核验性结论与边界记录）→ 送验 63 → confirmed 30 / refuted·duplicate 32 / undetermined 1（DD-F5）。
- 处置：A 类即修 22 条（7 写码档并行，全部带钉住测试）+ 落点复核登记 8 项（其中修复回归 2 条最高优先即修闭环、口径差 2 条即修闭环、观察 4 项）；B 类 2 条呈报待批；C 类裁决清单 22 行（已裁决接受 10、待裁决 7、观察 5）。证伪 32 条保留一行于问题记录（终报复盘用）。
- 验证证据：触及包聚合门禁全绿（auditlog/gatewayusage/gatewayresponse/openaicompatbridge/gatewayopenai/gatewayanthropic/gatewayclientip/aipublic/oauthmgmt/gatewayoauthcodex/cmd 全包，模块构建 5/5）；修复落点复核 19/22 确认正确，回归负向验证（撤回修复→测试转红→恢复）通过。
- 文档同步 6 处：官方可落地OAuth账户接入设计、GPT请求服务等级与思考级别覆盖设计 §8.3、模型价格与用量统计口径 §2.8、OpenAI兼容Files设计 §7、外部来源系统鉴权设计、实时语音Realtime网关设计；待办新增 T86（桥接 usage 剩余缺口）。
- 未提交改动：本环修复批 36 文件（backend-go 32 + docs 6，与并行会话 chat 域在制品混存工作区，未 commit）；台账与 T86 为过程记录。
- 收敛状态：收敛协议未走满——R2（场景制）、R3（交互制）未执行；R1 存在 B 类确认与修复回归，不计干净轮。出口待用户裁决：继续 R2 / 用户裁决提前收口（出口②）。

### 遗留项收口（T86 / T87r / 活性验证，2026-11-01）

**T86 跨协议桥接 usage 全方向收口（已完成，条目已删）**：流式五方向接线（gemini native→chat、anthropic→gemini、gemini native→responses/messages 回转、chat→responses codex 回转、anthropic→responses）+ 非流式缓冲臂（chain_bridge_response.go storeChainBridgeNonStreamUsageSnapshot + nonstream.go 消费入口）；桥核心修正一处（anthropic→responses message_start 的 usage 加 nil 守卫，防全 0 快照否决估算）。17 个新测试（5 方向快照值/5 nil-usage 回落/全 0 快照/非流式三族/回退形态/2 转换口径）+ 非流式结算 3 例；gatewayresponse 6.45s、openaicompatbridge 1.93s、cmd 全包 39.5s 全绿。文档：模型价格与用量统计口径 §2.8 方向清单全量。**已知口径观察**：gemini→chat 的 thinking 渲染进 prompt_tokens_details（Node parity），结算不读该键——§2.8 已如实记录。

**T87r outbox 清理接线（已完成，条目已删，转 jobs 侧三臂 + gateway 死代码删除）**：判定结论——jobs 侧 Cleanup 只删 incident 行不删 dispatched outbox（注释自证「清理由 gateway 写侧维护」而 gateway Store.Cleanup 全仓无 caller=死代码）。方案：jobs `internal/circuitstore` Cleanup 追加第三臂（dispatched outbox，ack 超 24h 保留期、单批 500、同事务先于 incident 两臂）+ `CleanupResult` 三计数进任务日志；gateway 删除 Cleanup/CleanupResult/deleteLimited 死代码并回正「dispatched 行永生」注释为「ack 保留期内仍在」。真实 PG 7 例绿（超龄删/边界/未超龄留/pending+processing 留/两类 event_type 同删/幂等/incident 臂不干扰），circuitstore 15.1s、circuit_control_plane 31.2s 全绿。文档：AI账户短窗口热质量设计 :652 三臂契约。

**活性验证（已执行）**：①PG 臂三包实测——openaicompatstorage 33.0s、circuit_control_plane 31.2s、circuitstore 15.1s 全绿（含 DD6-S7/S8、T87/T87r 真实 PG 用例）；②upgrade 活性——发现 **2 个测试文件 3 处过时期望**（rev7 chat-account-only-binding 退役后：pipeline 集成测试 pending「全量 11」实为 7（11 步 − rev8/10/11/12 四步空库 fresh-adopt，rev12 注释自证设计）、applied「12 行」实为 11；演练测试「步骤 12 个」实为 11）→ 已修正并按设计语义注释；`TestUpgradeFreshDatabasePipeline` 14.9s 绿、`TestUpgradeRehearsalEndToEnd` 32.4s 绿（fresh→dry-run blocked→缺确认 exit2→upgraded→postflight OK→能力域阶段记录探针→gateway/jobs health 门禁→负路径被拦全链路）。**该 3 处为既有测试期望滞后（非本环改动引入），修正后升级链活性实证成立。**

### 未提交批最终落点复核（2026-11-01，2 审核档并行）

**批次口径核实（重要）**：任务称「27 项未提交」，实际复核时用户已把 R6 批10 提交为 `98b0e210b`（工作区仅剩台账等 2 处未提交）。复核以提交内容 + 当前工作区为准。

**结果：A/B 代码面 26/26 核点全正确、零产品缺陷；C 组 2 处文档残留 + 1 处测试强度问题（已修）。**

| 组 | 结论 |
| --- | --- |
| 审计 producer 撤回队列隔离（7 核点） | 6 正确 + 1 测试强度问题。两池 worker/panic 隔离/退出保证、意图表 6 并发 drain 互斥不双取、撤回完全不进 queueBytes（比原 bytes=0 更彻底）、atomic retractWaitBudget 读写点全查（生产默认 15s 不变、包内无 t.Parallel）、**w13h 内联臂实际跑在数据 worker（撤回 worker 从不调 retractBestEffort）**、既有 DD-D1/G1/C2/C4/RC1/w13g/w13h 契约全回归绿（21.8s + -race 24.1s） |
| compat 异步计数 + vectorstores（19 核点） | 17 正确 + 2 文档残留。队列有界/Once/panic 隔离/字段前置提取全对；DD2-C1 与 DD3-D1 门仍在同步路径（503/500 零计数为强断言）；vectorstores 三分类完备、timestamptz cast 方向正确（UTC Z 毫秒、`< cutoff`=更早即超龄）、NewStore 不触库契约保持（后台 safego.Go）、S7×S8 互洽、ExpectedUpdatedAt 空=不启用兼容。**真实 PG 全量实跑 40.7s 绿**（含 S7/S8 四例） |

**修复（3 处，已全部落地）**：
1. `docs/functions/README.md:11` 49→50 组合 / 162→163 mode（唯一实质文档残留）。
2. `docker/single-server/monitoring/postgres-exporter-queries-additive.yaml:23` 8→7 schema（注释级）。
3. **DD8-RC1 测试强度**：`dd6_s1_retract_isolation_test.go` 原仅派 2 条撤回（=撤回 worker 数），修复前共享池形态下仍剩 2 个数据 worker → 断言无法证伪隔离契约（假阴性）。改为撤回命令数取 `producerWorkers`（≥2），尾部断言同步为「全数落账」；全包 20.8s 绿。

**观察项登记（2 条，不阻塞）**：O1 撤回队列满 256 × persistOne 失败叠加时数据 worker 仍可内联 15s（4 worker 理论可全占，前提比修复前窄约两个量级，注释已声明，无测试覆盖叠加态）；O2 DD6-O5 意图表登记前提从「数据队列满 4096」改为「撤回队列满 256」，触发门槛降约 16 倍，20-30MB 量级估计需按新阈值重估。

**判定：本批可交付**（代码零缺陷、测试全绿、文档残留清零）。

### 终态复核轮（2026-11-01，3 审核档并行，收敛判定前强制轮）

**①已修行为组合终态（6 组全核）**：审计 producer（数据 worker 4/撤回 worker 2/意图表/在途集合/预算记账/三丢弃臂/panic 臂全交互闭合，三满叠加无「满」态，正反馈已打断）、流式管道（四处词表对齐+优先序逐形态推演一致、A5 越限在清点后、B1/B2 Completed 门+RC-C1 双重守卫）、停机链（正常/错误/启动失败三路径 × F3/F4 lease/store 释放保证全闭合）、入口保护（四面计数与封锁语义一致）、配置/校验（写读契约+守卫一致）、jobs（读序/dedupe/T87 同构）。**新发现仅 2 条低危台账级（DD7-RC1 注释与 Cleanup SQL 矛盾、DD7-RC2 DD3-RC1 登记未落地），无跨修复组行为回归。**

**②文档-实现终态一致（27 条逐句核）**：一致 24，失同步 3（DD7-DC1 Files 缺 S7/S8、DD7-DC2 用量口径缺 Completed 门、DD7-DC3 策略路由缺 DD2-A1 收窄句）→ **已全部修正**；另修正 2 处代码注释瑕疵（pg_schema EnsurePostgres 组清单、connector_registry_test 162→163）与主代理直修的 connector_registry.go/test 9 处旧值注释（49→50、162→163，契约测试绿）。**代码本身未见与文档相悖的错误实现。**

**③测试与门禁盘点（11 项全通过）**：钉住测试均钉修复后行为（异步语义改轮询到账断言，无钉旧行为残留）；时间断言仅 1 处 200ms 门余量约 200 倍属低风险；无并行在制品依赖；**变异抽检 5/5 还原必红**（DD-D1/DD6-S1/DD2-A1/DD6-S7/DD3-B1）；新包已入日常链路（circuit_control_plane/openaicompatstorage/circuitstore/ingestgate 均不在 exclude 表）；全量抽查 9 包+2 jobs 包全绿（auditlog 29s、accounts、authsys、gatewayclientip、runtimecache、circuit_control_plane 等）。

**④部署/升级活性（8 项全通过）**：SchemaRevision 恒 12、本环零 DDL（S7 用既有 updated_at、S8 是运行时 UPDATE）→ upgrade 重跑收敛 no-op；旧数据 5 类（.corrupt/.tmp、dispatched outbox、in_progress vectorstore、>1MB 审计 body、越界 overrides）升级后行为逐类确认；回滚双向可跑（无结构变更，compose diff 仅头注释）；启动顺序（schemagate/ACQUIRE_WAIT 45s/F3-F4 租约释放）通过。

**建议（未执行，需授权窗口）**：交付前补跑一次 openaicompatstorage/circuit_control_plane/circuitstore 的 `JUHE_AI_TEST_EXTERNAL=1` PG 臂与 maintenance upgrade 重跑，作为 S7/S8/T87 的活性实证。

## 终报

- **终止原因**：用户裁决提前收口（出口②）——用户要求「转入终态复核轮」，终态复核已执行（①6 组全核 ②27 条文档逐句 ③11 项测试门禁 ④8 项部署活性），**但收敛协议的「连续 3 轮零 B 类确认」未走满**：R1–R6 每轮均有新确认与修复（R1 30、R2 14、R3 6、R4 2+jobs2、R5 7、R6 6），收敛计数 0/3。终态复核轮结论为「零行为回归、零 B 类新确认、零 A 类未被处置项」，据此收口并如实标注协议未走满。
- **扫描范围与轮次**：锚定 `backend-go/projects/gateway`（cmd 4.07 万行 + internal 20 万行，含 jobs 扩散 2 处）。R1 维度制（7 攻击面/84 假设→30 confirmed）｜R2 场景制（7 入口组/~70 假设→14 confirmed）｜R3 交互制（6 模块对/~80 假设→6 confirmed，修复间 36 对一致 35/冲突 0）｜R4 扩展+落点复核（批4 13 条 + 批5 9 条全复核通过；jobs 扩散 2 confirmed）｜R5 数据演化+消费者契约（9 送验→7 confirmed；批7/8 落点 3/3 确认）｜R6 运维部署+序列降级（7 送验→6 confirmed）。
- **处置统计**：**A 类即修 76 条**（R1 22 + 修复回归 5 + B 类批复 3 + R2 13 + R3 9 + R4 3 + R5 6 + R6 6 + 终态 5 + 注释 9 处合并计）；**B 类 3 组**（DD-F3/DD-D7 已批复修复；DD2-E8 按常设指令修复；DD2-A1 经用户三选裁决后按①实施）；**C 类裁决清单**：DD2-A1 已裁决实施、其余为观察/已裁决接受；**证伪 50+ 条**保留于各轮记录。
- **修复与验证证据**：68 个钉住测试新增/适配（含变异验证 5/5、负向验证 2 处、真实 PG 门控 10+ 例、-race 多轮）；全量回归 9 包+2 jobs 包绿；契约测试绿；前端 typecheck/lint 绿。
- **移交清单**：
  - 功能补齐/产品取舍类：**T86**（桥接 usage 剩余方向）、**T87r**（dispatched outbox 清理接线）、DD2-A1 混合窗口逐 attempt 精确化（需改 gatewaydispatch）、S8 清扫周期化（需文档裁决）。
  - 运维与部署义务：交付前在授权窗口补跑 S7/S8/T87 的 PG 臂与 upgrade 重跑；`AI问答设计.md:729` 仍描述已退役的 8s 生成排空（chat 域并行会话负责）。
  - 测试缺口登记：auditlog（>10s）与 cmd 钉住测试按分层规则不入日常链路（文档化预算取舍）。
  - 观察项：DD3-RC1 decode panic 理论缺口、DD7-RC1 注释与 Cleanup SQL 矛盾、DD5-E4 锁定文案粒度、DD5-C6 实例 credentials 500/400 不对称、DD6-S8 重启间断清扫、HEAD 既有 `TestOAuthManagedDefaultsCreateWritesConnectionRows/gemini_google_oauth` 失败（e38c6e9f2 引入，独立问题）、dev PG 并行争用。
- **提交与未提交分界（收口时核实）**：本环所有角色遵守「禁止 git commit」约束、均未主动提交。**用户已自行分批入库 R1–R5 主体修复**（`e0d313642` 与 `d56bc2829`「fix(gateway): 修复认证前熔断与停机启动顺序」，2026-11-01 10:26，含 chain_v1_loop 账户闸、chain_v1/chain_openaicompat/chain_realtime/compose/main/runtime、dd2_a1/dd5_c4/dd2_c1/dd3_compat/dd_g1_g2 等钉住测试与 accounts options_service 等）。**当前未提交 27 项 = R6 批10（auditlog 撤回队列隔离+compat 异步计数+vectorstores 代次守卫/启动清扫+部署文档 3 处）+ 终态复核批（模板文档 3 处+注释 2 处+connector_registry 注释 9 处）+ 台账 + 待办**：`internal/auditlog/{producer.go,+3 测试}`、`cmd/juhe-ai-gateway/{chain_v1.go,+3 测试,+dd6_s5}`、`internal/openaicompat/openaicompatstorage/{store.go,vectorstoresroutes.go,vectorstoresstore.go,vectorstoresstalesweep.go(新),+1 测试}`、`maintenance/internal/schema/pg_schema.go`（注释）、`shared/contracts/{connector_registry.go,connector_registry_test.go}`（注释）、`docker/single-server/{README.md,compose.yml}`、`docs/deploy/部署指南.md`、`docs/functions/{OpenAI兼容Files...,模型价格与用量统计口径,策略路由设计}.md`、`docs/plans/deep-dive-review-网关-20261031.md`、`docs/待办与阻塞事项.md`。
- **与并行会话的混合署名**：`internal/chat/**` 与 `docs/functions/AI问答*.md` 的改动属并行会话在制品（T85/my-chat 退役），本环全程未触碰；`docs/待办与阻塞事项.md` 为本环与并行会话共同修改（本环新增 T86/T87r，T87 已按「解决即删」移除）。

---

# R7 独立域深挖（新锚定：模型检测 / 状态流转 / 统计）

定位：R1–R6 锚定 `/v1` 主链及周边；R7 起对**未覆盖的功能域**分别立轮（不混轮，防假干净）。各域单独写锚定范围与结论，共用本台账。

## R7-A 模型检测域

- 锚定范围：`backend-go/projects/gateway/internal/modelcheck*`（owner 12.4k + probe 6.3k + profile 0.2k + questionbank 1.9k + active 0.1k + auth 1.5k ≈ 22.5k 行非测试代码）——探针执行、检测调度与持久化、结论写回、题目库、J3b 认证面。
- 关联契约：
  - [模型检测设计](../functions/模型检测设计.md)、架构总览「模型检测」模块：快速检测 = 2 次单轮轻量探针、最高"较可信"、不写模型指纹/Token observation；深度检测 = 完整探针 + 分钟级失败重试 + 可选可信对比 + 隐藏分布相似度；报告只保存脱敏有界摘要；**指纹与 Token 差分只使用深度检测主动发起的受控探针，不读取普通用户请求正文**。
  - 检测复用本地网关链路，产生的模型请求按既有使用记录/额度/审计规则处理。
  - 人工测试隔离硬契约：人工测试只产诊断任务/使用记录/审计，**不改**账户、授权实例、API Key 状态、`schedulable`、冷却、运行态、健康事实或最近错误。
  - 探针分级超时（BUG-0287：探针超时流中途截断不得晋级为可信）、W11（非流式空 choices 判定）、T65②（Gemini Code Assist 探测/onboarding 遗留）、W45（畸形能力矩阵 chat 面）。
- 已排除：`internal/chat/**`、`internal/playground/`（并行会话在制品）；R1–R6 已登记项。
- 状态：攻击两批完成（5 角色 ~86 条假设）；送验 16 条 → 15 confirmed / 1 refuted；A 类修复 14 条 + 夹具债 3 处已落，四包（含真 PG 门控）全绿。剩余假设登记见下，R7-A 尚余落点复核轮。

### R7-A 攻击轮（第一批 3 角色：探针执行 / 调度持久化 / 结论写回）

**角色1 探针执行（modelcheckprobe 16 文件 + profile）**：H1（高·**非 Responses 系 SSE 无完成证据校验**——chat_sse/messages_sse/generate_content_sse 的 [DONE]/message_stop/finishReason 均不校验，干净 EOF 截断与完整流不可区分→BUG-0287 家族复发面，probe.go:624-625/991-1063/1516-1522）；H2（高·reasoning_content/reasoning_summary delta 并入 Output 参与判分→「只推理晋级」probe.go:1447-1458/evaluation.go:75/155）；H3（高·responses 通用解析器不校验 status=incomplete，Codex 解析器有守卫 probe.go:1043-1050 对照 :1153-1166）；H4（**契约冲突需裁决**：探针是否应写 usage/额度——设计文档 §69/260/499 要求按真实调用方计入 vs §321「Diagnostic 探针不写 usage」现状，probe_adapter.go:56-66 无 usage 写入）；H5（quiz 两跳判分：作答跳非终态失败无门→空答案照送裁判，请求失败痕迹不可见 quiz.go:87-107/210-222）；H6（evidence["reason"] 原文无截断入 EvidenceSummary，500rune 脱敏仅覆盖 message/error quiz.go:218-221/runtime.go:440-442）；H7（long_context 窗口按「每单元≈4token」估算超调不上修→小窗模型实际超窗误判 longcontext.go:38-93）；H8（body>maxBytes 不置 Incomplete→按定论 200 不重试转计分失败，BUG-0292 家族 probe.go:559-563/retry.go:95）；H9（RunSuite timeout 参数是死参，无墙钟上限仅心跳续租 retry.go:39-53）；H10（可信对比 paired 探针绕过 familyRunner 终态门 suite.go:807-864）；H11（同账户跨 profile/actor 可并发无账户级互斥）；H12（quick token_integrity <6 样本一律 reported_usage_missing 归因失真）；H13（heartbeatCtx 无 defer cancel→panic 时永久续租，猜测）；H14（重试节奏固定 2s 与文档「随机 1–3s」漂移）。**已核清**：探针 prompt 全部来自受控池/题库，未发现读取普通用户正文路径。
**角色2 调度持久化（owner 调度侧）**：H1（高·**错误路径绕过 finishFailure→run 永久 running**，唯一回收是启动期 sweep，运行期无周期清扫 runtime.go:566-575/stale_runs.go:36-41）；H2（schedule 完成写失败仅靠 6min 租约重认领无限重跑无帽 business_scheduler.go:109-147）；H3（跨触发源无互斥：manual/run-now 占槽但 scheduled/recovery 执行器不查注册表 http.go:913-927）；H4（ProjectTrust 游标推进跨过并发在途 run 更早 observation，DD3-B1 同构但当前无 cursor 消费者→前瞻）；H5（高·**ProjectTrust 失败→health 重试只认 'failed' 而 status 留空→健康事实永久静默丢失** runtime.go:584-587/scheduler_executor.go:228-236）；H6（高·**死信窗口饿死**：MaxAttempts=10 死信 run updated_at 冻结霸占 LIMIT=16 窗口，新健康失败永不进重试且每秒空插 store.go:507-511/health.go:115）；H7（run-now lease 读判与 claim 写非同事务 TOCTOU）；H8（启动 30min sweep 可误杀另一存活进程 >30min 合法长 run）；H9（探针成功但 ctx 恰在 finalize 前过期→结果丢弃记 failed）；H10（心跳失租后 CommitOutcome 必败→H1 僵尸+H10 双跑链）；H11（recovery Build 失败等租约周期，期间占住 lease 拒绝新请求）；H12（调度参数无 env 装配面，固定 DB 轮询底噪）；H13（健康重试每任务全窗扫 1000 行只为找自己 runID）；H14-H15（ListRuns 时间参数未校验/字节截断 UTF-8，低危）；H16（jobs 家族与检测域两套探针无协调，需产品裁决）；H17（RunNowBatch 无服务层上限，猜测）；H18（越权/围栏/幂等/query 无实时扫明细=核对通过）。
**角色3 结论写回（结论/信任/处罚/恢复）**：H1（高·**对比账户自身 modelMismatch 未被排除出 suspicious 短路循环→把目标 run 整轮拉 suspicious 且可放大为处罚**——probe 层 summary.go:76-81 无前缀过滤，owner 层 trust.go:57-59 显式跳过，两层口径分裂）；H2（高·**超时处罚主场景被 Apply 输入校验拒绝→处罚永不生效**，health_test 用不做分数校验的 fake 掩盖，business_enforcement.go:36 vs health.go:162-171）；H3（trust latest-wins：identity/protocol/evidence 可被单次 formed run 反向洗白，仅 usage_integrity 有只升不降 trust_store.go:176-179）；H4（对比账户仅校验 vendor/protocol/profile 不校验 upstream_model 映射一致）；H5（model_limits 查询不按 system_account_id 过滤→可命中他租户同名自定义行 model_limits.go:40）；H6（manual 检测处罚属**文档化通道**非缺陷，但需文档口径守卫「人工测试≠手动检测」）；H7（quick 处罚免 formed 门，证据门槛低于 full，Node 兼容判断项）；H8（custom_quiz 失败全额扣分进处罚分母，设计使然）；H9-H11（恢复通道落 disabled 属既有语义/禁用绑定不同步/恢复 lease 竞争静默 no-op 延迟）；H12-H15（围栏/CAS/error 写入面/权益不入证/证据脱敏=核对通过）；H16（BUG-0287 单 choke point 各臂成立=核对通过）；H17（ListCommittedOutcomes 无租户过滤但无消费方，观察）；H18（熔断中性结算成立，诊断链是否零 usage 未逐行核实=待补）。
- R7-A 尾批待发：input/http/query/quiz 面 + modelcheckauth 认证面。

**R7-A 攻击第二批（输入契约 / J3b 认证）**：
- 输入/HTTP/题库角色：I-H1（owner 面大量 500/400 直透 `err.Error()`（含 PG 错误与内部包装前缀），writeBuildError 把 DB 故障标 400）、I-H2（run-now 只占 target 槽不占 actor 槽→`/run/stop` 与 `/run/active` 查不到，10 分钟预算内不可取消且同账户手动检测被 409）、I-H3（`/question-bank/test` 不限频次、不占 active 槽、同步两跳上游）、I-H4（**题库写操作审计 sink 组合根装配为 nil→全链无审计留痕**，与包注释不符）、I-H5（相似度查重长度预过滤可被填充文本绕过）、I-H6（parsePage page 无上界→OFFSET 溢出）、I-H7（created_at 为 TEXT，startAt/endAt 不校验格式按字典序比较）、I-H8（J3B_CREDENTIAL_SECRET 无长度校验、IdentitySecret 无消费点）、I-H9（错误码按中文子串映射，脆弱）、I-H10（SSE 进度帧不经脱敏适配，含 upstreamModel/responsePreview）、I-H11（quizRequestFailurePhrase 保留上游错误原文进报告）、I-H12（Store.Review 层对 pending 允许任意 actor，靠 handler adminMode 兜底）、I-H13（dev 自动登录仅靠生产 fail-fast 阻断）、I-H14（定点模型选项需显式 limit=1 否则 400）、I-H15（admin all-scope 选项与全局历史集合不一致，猜测）。**已核清**：三挂载 scope 强制与角色矩阵无越权读、无实时扫明细、active registry 并发安全、business_schema fail-closed、profile 无注入面。
- J3b 认证角色：A-H1（**J3b guard 与主面完全独立**：纯内存驱动、无 fail-closed 臂、重启清零——同一凭据获两份 10 次/10min 预算；任务描述的 Redis GETDEL 契约只在 authsys 面成立）、A-H2（j3bManagementMount 调两次各建一个 HTTPHandler/guard——别名端可达时多一份预算）、A-H3（**J3b 会话 cookie 无 Secure 属性**，HTTPS 部署下与主面带 Secure 的同名 cookie 分叉可被覆盖）、A-H4（跨面共享 system_sessions 与 cookie 名，会话互通但属性分叉）、A-H5（mustChange 两面分叉：主面 admin 豁免、J3b 不豁免）、A-H6（`/auth/` 族 500 直透 err.Error() 含 PG 细节）、A-H7（停用账户短路跳过 dummy hash→时序可枚举账户存在性）、A-H8（**临时令牌白名单基于 RemoteAddr，Caddy 反代后全网塌缩**；guard/captcha byIP 桶全员共享→一次爆破全员锁死）、A-H9（allowlist 双解析器漂移同 DD-G6 家族）、A-H10（guard 超容驱逐可洗掉受害者活跃锁/未消费验证码）、A-H11（临时令牌面无验证码）、A-H12（mustChange 分支跳过旧密码+≥4 位即可，与主面策略一致性未核）、A-H13（DD-G5 登记仍精确，无新增漏挂认证=核对）、A-H14（logout 不消费 Authorization，Bearer 持有者静默无效）、A-H15（用户名 lower()+LIMIT 1 无 ORDER BY）、A-H16（会话数无上限）、A-H17（非生产默认关 mustChange 门=已登记设计）、A-H18（captcha 内存一次性 vs Redis GETDEL，多实例前必须换 SharedCaptcha）。

### R7-A 验证轮（完成）

送验 16 条（3 审核档）：V1=探针核心 5（P-H1/H2/H3/H5/H4 契约冲突）；V2=调度写回 5（S-H1/S-H5/S-H6/W-H1/W-H2）；V3=输入认证 6（I-H1/I-H4/A-H1/A-H3/A-H5/A-H8）。

- 结果：**15 confirmed / 1 refuted**。refuted=A-H5（mustChange 两面分叉）——核实为已登记 DD2-E11 的既有裁决面，非新缺陷，验证者按重复/已登记驳回。
- P-H4 验证结论：**确认为文档间契约冲突**（模型检测设计 §321「Diagnostic 探针不写 usage」 vs 通用用量文档 §69/§260/§499「按真实调用方计入」），现状实现取 §321；不是代码缺陷，待用户裁决口径。

### R7-A 修复批（A 类 14 条 + 夹具债 3 处，3 写码档并行，全绿）

| 编号 | 修复内容 | 钉住测试 |
| --- | --- | --- |
| P-H1 | SSE 终止证据校验：chat `[DONE]`、messages `message_stop`（含纯 event 行形态）、gemini 任一 `finishReason`；干净 EOF 无终止事件 → `Incomplete=true` + `J3b probe upstream stream ended without terminal event`，不晋级可信（BUG-0287 家族收口） | `r7a_probe_fixes_test.go` 5 例；`w1_envelope_integration_test.go` 夹具补终止帧 |
| P-H2 | `parseSSEEventOutput` chat 分支不再把 `reasoning_content` 并入 Output（防"只推理晋级"），refusal 保留 | 同上 |
| P-H3 | `parseResponseDetailed` 增加 `status!="" && status!="completed"` 守卫（与 Codex 解析器口径取齐） | 同上 |
| P-H5 | quiz 两跳门 `quizAnswerHopUsable = Success && !Incomplete && Output!=""`：作答跳非终态失败不再空答案送裁判，记请求失败证据且不扣分 | 同上 + `quizdiagnosis.go` 同步 |
| W-H1 | summary 层 `modelMismatch` 短路循环跳过 `trusted_comparison.*`（与 owner trust 层口径取齐，防对比账户自身错配拉整轮 suspicious） | `r7a_probe_fixes_test.go`；`wl_probe_extra_test.go` 期望 `mrfd`→`mfd` |
| S-H1 | 新增 `Store.MarkRunFailedTerminal`（仅 `status='running'` 可写 failed）：ClaimInput/CommitOutcome/ProjectOutcome 失败臂全部收口；ErrStaleFence 只记日志不强写；新增 `StaleRunSweeper`（默认 10min 扫描、30min 阈值）挂入 Scheduler.Run，运行期不再依赖启动期 sweep | `r7a_fixes_test.go`；`w1_envelope_source_test.go:522` 旧断言（钉住"commit 失败仍 running"缺陷态）改为期望 failed |
| S-H5 | ProjectTrust 失败先 `MarkHealthSync(...,"failed")` 再返回，健康重试不再永久静默丢事实 | 同上 |
| S-H6 | 健康窗口查询排除已达 `MaxAttempts` 死信任务（`NOT EXISTS` 子查询），`rotateSkipped` 11 个跳过分支推进 `updated_at`，防死信霸占 LIMIT 窗口饿死新失败 | 同上（夹具 DDL 补 `model_check_scheduler_tasks`） |
| W-H2 | `QualityEnforcement.TimeoutPenalty` 传入处罚校验并豁免分数门，超时处罚主场景可达（原输入校验永久拒绝） | 同上；`health_test` fake 不再掩盖 |
| I-H1 | 新增 `writeOwnerFailure`：RequestError 保 4xx，其余统一 500 + 固定文案「服务器内部错误」+ slog，约 15 处 `err.Error()` 直透收口（含 SSE 错误帧） | `w11e` 系列更新；`TestW11EStreamErrorPayloadHelpers` |
| I-H4 | 题库写操作审计 sink 组合根注入（`SetAuditSink` nil 安全 + Warn once），main.go 在 composeSystemAPI 后接 `composed.authDeps.Sink` | `TestSetAuditSinkRecordsWriteOperations` |
| A-H1 | 新增 `SharedLoginGuardDriver` 注入口，优先使用共享 Redis guard；驱动错误 fail-closed（500+slog）；`j3bAuthHandler` 单例供两个挂载点复用，消除双预算 | `TestJ3bSharedGuardInjectionAndSharedMountInstance` / `TestJ3bSharedGuardFailClosed` / `TestJ3bLoginGuardDriverSelection` |
| A-H3 | J3b 会话 cookie 按注入配置补 `Secure`（登录/登出两臂） | `TestJ3bSessionCookieSecureFollowsInjection` |
| A-H8 | `HTTPHandler.TrustProxyCount` + `clientIP` 走 `kernel.ExtractClientIP`，反代后临时令牌白名单/限流桶不再按 RemoteAddr 塌缩；4 处调用点收口 | `TestJ3bClientIPTrustProxyInjection` |
| 夹具债（非本轮引入） | URL v2 版本根语义（e38c6e9f2，2026-10-29）迁移漏改：`runtime_test.go` `TestRuntimeExecutesAndPersistsBasicProbe`、`runtime_catalog_external_test.go` 两处 endpoint 裸 `server.URL` → `server.URL+"/v1"`（断言 `/v1/responses`、`/v1/chat/completions` 自 e793cf61b 2026-09-21 起未变） | 三测试真 PG 实跑 PASS；modelcheckowner 全包 244s 绿 |

验证证据：`modelcheckprobe` 0.709s、`modelcheckowner` 244.475s（`JUHE_AI_TEST_EXTERNAL=1` 全包）、`modelcheckquestionbank` 21.195s、`modelcheckauth` 16.550s 全绿；`go build ./...` 网关模块通过。

### R7-A 剩余假设登记（未送验，留待后续轮或观察）

- 探针执行：P-H6（evidence reason 原文未截断）、P-H7（long_context 窗口 4token/单元估算超调）、P-H8（body 超 maxBytes 不置 Incomplete）、P-H9（RunSuite timeout 死参无墙钟上限）、P-H10（可信对比 paired 探针绕过终态门）、P-H11（同账户跨 profile 并发无互斥）、P-H12（quick token_integrity 样本不足归因失真）、P-H13（heartbeatCtx 无 defer cancel）、P-H14（重试节奏 2s 与文档 1–3s 漂移）。
- 调度持久化：S-H2（schedule 完成写失败靠租约无限重跑）、S-H3（跨触发源无互斥）、S-H4（ProjectTrust 游标前瞻）、S-H7（run-now TOCTOU）、S-H8（启动 sweep 误杀长 run）、S-H9（finalize 前 ctx 过期丢结果）、S-H10（心跳失租双跑链）、S-H11（recovery Build 失败占租约）、S-H12（调度参数无 env 面）、S-H13（健康重试全窗扫描）、S-H14/S-H15（ListRuns 参数/UTF-8 截断）、S-H16（jobs 探针族协调需产品裁决）、S-H17（RunNowBatch 无上限）。
- 结论写回：P-H4 之外 W-H3（trust latest-wins 洗白）、W-H4（对比账户不校验 upstream_model 映射）、W-H5（model_limits 未按 system_account_id 过滤）、W-H7（quick 处罚免 formed 门）、W-H18（诊断链零 usage 未逐行核实）。
- 输入/认证：I-H2（run-now 不占 actor 槽不可取消）、I-H3（题库 test 不限频）、I-H5（相似度预过滤可绕）、I-H6（page 无上界）、I-H7（created_at TEXT 字典序）、I-H8（凭据 secret 无长度校验）、I-H9（错误码中文子串映射）、I-H10（SSE 进度帧不脱敏）、I-H11（quiz 失败原文进报告）、I-H12（Store.Review actor 门靠 handler）、I-H13（dev 自动登录仅生产 fail-fast）、I-H14（limit=1 隐含要求）、A-H2（双挂载双 handler 已随 A-H1 收口，待复核）、A-H4（跨面会话属性分叉）、A-H6（/auth/ 500 直透）、A-H7（停用账户时序枚举）、A-H9（allowlist 双解析器）、A-H10（guard 超容驱逐）、A-H11（临时令牌面无验证码）、A-H12（mustChange 旧密码策略）、A-H14（logout 不消费 Bearer）、A-H15（用户名 LIMIT 无 ORDER）、A-H16（会话数无上限）、A-H18（captcha 多实例）。
- 移交裁决：P-H4 探针 usage/额度口径（建议以 §321 为准并回写通用用量文档，需用户确认）。

### R7-A 落点复核（独立复核员，2026-11-01）与第二批修复（A 类 15 条）

复核结论：需修 6 条（高 1、中 5），另有中风险裁决/加固 5 条、低风险观察 6 条。以上全部为**第一批修复本身或同族缺口**，非新域缺陷。

| 编号 | 复核发现 | 处置 |
| --- | --- | --- |
| RC-A1（高） | 非 Codex 的 Responses SSE 无 `response.completed` 仍判成功（`sseStreamTerminalEventSeen` default 直接 true，`parseResponseDetailed` 无终态失败判定） | 已修：Responses 纳入终态校验，缺 `response.completed`/`response.done`/`status=completed` 置 Incomplete |
| RC-A2（中） | 零事件 SSE（空 body/仅心跳/截断 JSON）直接 return true | 已修：仅完整可解析 JSON 豁免，SSE 零事件判不完整 |
| RC-A3（中） | chat 只认 `[DONE]`，`finish_reason` 收尾被误判 Incomplete（与《响应语义检查管线设计》冲突） | 已修：接受非空 `choices[].finish_reason` 为终态证据 |
| RC-A4（中） | read-error 臂不认 `[DONE]`/`message_stop`/`finishReason`，终态后尾部读错误覆盖语义成功（违反 §1.2） | 已修：`responseBodyIsSemanticallyComplete` 增加 protocol 参数并复用统一终态判定 |
| RC-A5（中） | quiz 300 字空作答/200 错误信封被归请求失败（不扣分、不入分母），是全跳过仍达高分的逃逸面（与 §1.2「200 内容质量失败进正常评分」冲突） | **未改，B 类待批**：改判会改变扣分与处罚面；文档对「作答跳 200 空输出」无明文，需裁决 |
| RC-A6（中） | recovery 写复用已过期 finalizeCtx，终态落盘仍失败 | 已修：统一后台 10s ctx |
| RC-A7（中） | 周期 sweep 误杀 >30min 合法手动 full run（心跳只推进 claim 表） | 已修：心跳同步 `TouchRun` 推进 run.updated_at |
| RC-A8（中） | 死信子查询 `(...->>'attempts')::int` 遇非数字 payload 整表扫描报错 | 已修：`jsonb_typeof='number'` + CASE + COALESCE 守卫 |
| RC-A9（中） | builder 用户输入错误被 writeOwnerFailure 统一升 500 | 已修：3 处输入校验返回 `*RequestError{400}`，文案不变 |
| RC-A10（中） | `errors.Join(cause, storageErr)` 让 errors.As 命中 RequestError，终态写失败被降级为 4xx/503 | 已修：终态写失败只暴露存储错误（cause 仅日志），HTTP 500 |
| RC-A11（中） | 健康重试绕过 trust 回执门，ProjectTrust 失败后仍可能发布健康事实并处罚 | 已修：重试前从 result_summary_json 重放 ProjectTrust（幂等）；无可重放 report 时保持 failed |
| RC-A12（低） | 非流式 Responses JSON `status=incomplete` 未校验 | 已修：直接 JSON 分支补 status 守卫 |
| RC-A13（低） | Responses reasoning delta 仍并入 Output | 已修：仅 `response.output_text.*`/无 type 拼接，reasoning 返回空 |
| RC-A14（低） | SetAuditSink 在 listener Serve 后写接口字段（data race + 启动窗无审计） | 已修：RWMutex 保护，注入窗口按 nil no-op 契约注释说明 |
| RC-A15（低） | sweep 不写 finished_at；rotateSkipped 吞错误 | 已修：补 finished_at；轮转失败 slog.Warn |

验证证据（合并三批改动后）：`modelcheckprobe` 0.725s、`modelcheckowner` 255.563s（`JUHE_AI_TEST_EXTERNAL=1`）、`modelcheckquestionbank` 23.504s、`modelcheckauth` 19.107s、`cmd/juhe-ai-gateway` 22.609s 全绿。新增测试：`r7a2_terminal_evidence_test.go`、`r7a2_owner_runtime_test.go`、`TestBusinessSourceInputValidationReturnsRequestError`、`TestHTTPHandlerRunInputValidationIsBadRequest`、`TestSetAuditSinkConcurrentWithWriteOperations`。

剩余观察（不修）：baseline 激活未知存储错误 503→500（属存储错误，500 可接受）；健康重试全窗 1000 行扫描（S-H13 既有登记）。

## R7-B 状态流转域

- 锚定范围：账户状态机写入面（`internal/accounts`、`accountkeystates`、`gatewayaccounteffects`、`cmd/.../chain_error_policy_effects.go`）；运行态与调度消费面（`gatewayhotquality`、`gatewayruntimecache`、`gatewaydispatch`、`gatewayquota`、`jobs/accounthealth`）；授权实例与 API Key 生命周期（`internal/authz`、`apikeys`、`gatewaypreauth`、`jobs/keymodelrecovery`、`business/key_model_runtime`）。
- 关联契约：AI账户错误语义与状态变更边界、AI账户健康状态收敛设计、AI账户运行态探针恢复设计、AI账户短窗口热质量与精准切号设计、系统团队与统一授权设计、核心功能设计（API Key 段）、AI账户主探测与Key模型短屏蔽修复设计。
- 状态：攻击轮完成（3 角色，B1 18 条 / B2 19 条 / B3 32 条）；验证轮进行中。

### R7-B 攻击轮要点

**B1 账户状态写入（18 条）**：
- B1-1（高·事实）流量迁移写面无前置状态/CAS/缓存失效：`accounts/m11_traffic_migration.go:243-260/262-280/370-395` 仅 `WHERE id=? AND system_account_id=?`，可把 disabled/error/pending_test/锁死账户改写为 temporary_unavailable/disabled；temp 分支不写 schedulable、不 bump config_revision、全文件无 invalidate。
- B1-2（高）删除/人工停用不释放锁死与质量隔离：`accounts/delete.go:88-103` 只写 status/schedulable/deleted_at，`account_lock_states`/`account_quality_enforcements`/Key 运行态无清理；对比后台探测关闭臂 `patch.go:1727-1789` 有完整清理。
- B1-3（中）自动禁用覆盖 error/pending_test/quality_isolated：`chain_error_policy_effects.go:617` 非绑定臂只短路 error/disabled，绑定臂 `:584-603` 仅 `status <> 'disabled'`。
- B1-4（中）Key 级 `error` 终态被失败写降级为 rate_limited：`accountkeystates/probe.go:440/509/574` 无 `status NOT IN ('disabled','error')`；后续成功探针可恢复 active。
- B1-5（中）同一 temporary_unavailable 三套 schedulable 语义（人工=0、错误策略=1、迁移=继承旧值）。
- B1-7（中）运行时重置把 quality_isolated 恢复为 active 且 schedulable=1，不清 enforcement（`accountsreset/runtime_reset.go:701-712/751`）。
- B1-8（中）DeferProbe 可把 next_probe_at 从数小时后回退到 +3s（`probe.go:723/729-740` 不与既有值取 max）。
- B1-10（中）PATCH 激活强制 schedulable=true，覆盖显式 false（`patch.go:1288-1293`）。
- 其余：B1-6/9/11-18（迁移无失效/自动状态无审计/毫秒栅栏/锁死可施加非 active/过去 cooldown 不递增退避/Key 错误文案未脱敏/提交后失效失败重复计数/批量状态集合漂移/删除保留旧文案/运行时重置不清 Key 状态）。

**B2 运行态与调度消费（19 条）**：
- B2-H1（高·事实）探针运行态 degraded/precheck_pending/half_open 生产零写入：`gatewaycircuit/suppression.go:186-190` 注释明示写面退场，`chain_runtime.go:607-614` 无 writer，`NewProbeCoordinator` 无 caller；文档所述软阻断链路未落地（C 类功能缺口候选）。
- B2-H2（高）降级排序端口恒 disabled：`chain_compose.go:730-733` disabledDegradation，`compose.go:1060-1111` 未注入 Degradation；`runtime_degraded` 排序规则不生效（同 C 类）。
- B2-H3（高）运行缓存 stale 窗口最长 10min 可继续调度已 error/disabled 账户：`gatewayruntimecache/service.go:19-20`、`runtime.go:31-43/282-302`；jobs 翻状态不发布 gateway 失效。
- B2-M1（中·文档冲突）探针 cooldown_success 可清除用户显式 temporary_unavailable/rate_limited，无 provenance 区分（`patch_runtime_state.go:172-183`、`jobs/accounthealth/projection.go:912-916`）。
- B2-M2（中）探针 owner lease 90s 小于冷却探针 10min 上限，终态补投影先写业务库后验租约（`jobs/accounthealth/config.go:20/165-171`、`scheduler.go:116`、`store.go:195-240`）。
- B2-M3（中）热质量 key 容量 10000 耗尽后回落 protocol scope，跨账户污染分数（`gatewayhotquality/redis_store.go:843-875`）。
- B2-M5（中）同账户重试绕过电路 PrepareAttempt（`gatewaydispatch/upstreamdispatch.go:999`）。
- B2-M6（中）热质量共享 Redis 使用调用方 now_ms，多实例时钟偏移破坏窗口。
- 其余 B2-L1-L6（precheck 清理依赖外部谓词、状态生命周期不对称、重试时间覆盖、死分支、整分钟桶边界、固定惩罚窗）。

**B3 授权实例与 API Key（32 条）**：
- B3-H1（高）团队停用可被普通授权 PATCH 绕过：`authz/mutations.go:423-457` Patch 不查团队状态，`sync.go:148-192` 仅按成员表展开，`systemteams/mutations.go:308-315` 停用只撤 sources。
- B3-H11（高）计划激活的小时额度 Key 永久缺失小时窗口绑定：`apikeys/store.go:1175/1468-1495` 仅 active 时建绑定，`jobs/statsagg/stages_quota.go` 只按绑定表生成窗口。
- B3-H12（中）计划边界整分钟错过则 Key 状态整天不纠正：`jobs/oauthrefresh/schedule.go:615-628`。
- B3-H13（中）jobs 翻 Key 状态不发布 gateway 运行缓存失效（60s fresh / 10min stale 窗口）。
- B3-H14（高）runtime 缓存 invalidation 与在途 load 竞态，旧快照可写回（`gatewayruntimecache/runtime.go:174-189`、`invalidate.go:86-119`）。
- B3-H23（高）keymodelrecovery HALF_OPEN 租约无回收，永久卡死并饿死 due 窗口（`jobs/keymodelrecovery/state.go:208-224`、`redis_store.go:109-147`）。
- B3-H24（中）LoadAccount 无匹配时永久 10s 重试，无终态迁移。
- B3-H25（中）恢复探针使用人工 disabled 的 Key 发真实请求（`accounttest/exactkeyprobe/direct_input.go:213-227` 不读 Key 运行态）。
- B3-H26（中）continuation 保留槽在权威 Lua 未落实（`redis_store.go:198-204`）。
- B3-H27（中）单条脏 state 让整个 recovery 扫描失败（`redis_store.go:126-129`）。
- B3-H31（中）`business/key_model_runtime` ForegroundLimit=100000 与主链 2 的固定契约漂移（`contract.go:131-134`）。
- 其余 B3-H2-H10/H15-H22/H28-H30/H32（终态复活/成员上限/实例状态不归一/expired→paused 分叉/returned 回收/团队详情 404/过期 sweep TOCTOU/脏 schedule Key 500/过期 Key active/结构缺陷/chat Key 不可删/文档 last_used_at 无写方等）。

### R7-B 验证轮（完成）

送验 22 条（B1 8 / B2 7 / B3 11）→ **20 confirmed / 3 refuted / 2 重复**（重复项计在 confirmed 内：B3-H13、B3-H14 分别并入 DD-A11/DD-A9、DD3-A2）。

| 编号 | 裁决 | 分类 | 处置 |
| --- | --- | --- | --- |
| B1-1 流量迁移无 CAS/失效/前置状态 | confirmed | A（revision+失效）+ B（可改写特殊状态） | A 部分即修；B 部分呈报 |
| B1-2 删除/停用不释放锁死与质量隔离 | confirmed（删除残留仅数据卫生） | B（停用臂违反锁死契约） | A 类修停用臂清理；删除残留观察 |
| B1-3 自动禁用覆盖 error/pending_test/quality_isolated | confirmed | B（状态机语义） | 按"error 为人工终态"修复 |
| B1-4 Key error 被失败写降级 | confirmed（现役实现在 shared proberepo） | A | 即修 |
| B1-5 temporary_unavailable schedulable 三套语义 | confirmed（后果降级） | C | 登记裁决 |
| B1-7 运行时重置 quality_isolated→active 不清 enforcement | confirmed | B | 呈报 |
| B1-8 DeferProbe 提前 next_probe_at | **refuted**（gateway DeferProbe 无生产调用方，现役 shared 实现带 fence） | C 死代码 | 观察 |
| B1-10 PATCH 激活覆盖 schedulable=false | confirmed | B（API 语义） | 呈报 |
| B2-H1 运行态软阻断零写入 | confirmed | C（功能缺口） | 登记裁决 |
| B2-H2 降级排序端口恒 disabled | confirmed（H1 下游） | C | 并入 H1 |
| B2-H3 运行缓存 10min stale 调度已 error 账户 | confirmed | B | 呈报（与 DD-A9 同族需合并裁决） |
| B2-M1 cooldown_success 清用户显式状态 | confirmed（文档条款冲突） | B | 呈报 |
| B2-M2 owner lease 90s vs 10min | **refuted**（续约独立 goroutine，双探针窗口不成立） | — | 不登记 |
| B2-M3 热质量容量回落跨账户污染 | **refuted**（回落保留 AccountRuntimeKey，仅跨模型族） | — | 不登记（指标告警可选） |
| B2-M5 同账户重试绕过电路 | confirmed | B | 呈报（与 W17 并列） |
| B3-H1 团队停用可被授权 PATCH 绕过 | confirmed | A（授权面防御） | 即修 |
| B3-H11 计划激活 Key 缺小时窗口绑定 | confirmed（静默 fail-open） | A | 即修 |
| B3-H12 计划边界错过不纠正 | confirmed | B（DD-A11 延伸） | 呈报 |
| B3-H13 jobs 翻 Key 状态不发布失效 | confirmed | 重复 DD-A11/DD-A9/B2-H3 | 不重复登记 |
| B3-H14 runtime 缓存回填竞态 | confirmed（≤60s） | 重复 DD3-A2 | 不重复登记 |
| B3-H23 HALF_OPEN 租约无回收 | confirmed（卡死+due 饥饿） | A | 即修 |
| B3-H24 Unknown 永久重试 | confirmed | B（终态语义需定） | 呈报 |
| B3-H25 恢复探针使用 disabled Key | confirmed | A（明确契约） | 即修 |
| B3-H26 continuation 保留槽 | confirmed（软 SLO） | C | 登记裁决 |
| B3-H27 单条脏 state 阻塞全扫描 | confirmed | A | 即修 |
| B3-H31 ForegroundLimit=100000 契约漂移 | confirmed | B | 呈报 |

**R7-B A 类修复批（批11，即修）**：B1-1a（迁移写 bump config_revision + 运行缓存失效）、B1-2a（停用臂结束锁死/质量隔离）、B1-3（自动禁用 hard-unavailable 集合）、B1-4（Key error 不被失败写降级）、B3-H1（PATCH 补团队 status 门）、B3-H11（计划激活补建小时窗口绑定）、B3-H23（HALF_OPEN 过期租约接管）、B3-H25（恢复探针跳过 disabled Key）、B3-H27（脏 state 逐项隔离）。

### R7-B 批11 交付记录（状态写面 + key-model 恢复四件，写码档，2026-11-01）

本小节只登记批11 中由本次写码交付的四条（B1-4 / B3-H23 / B3-H25 / B3-H27）；其余五条以各自交付记录为准。

| 编号 | 修复内容（落点） | 钉住测试 |
| --- | --- | --- |
| B1-4 | Key 级 `error` 视为人工终态，被动失败写不得降级。shared `accounttest/proberepo/mutation.go`：`RecordKeyFailure` 入口新增 `Expected.Status=="error"` 早退 `manual_restore_required`、existing 行 `error` 早退同码、UPDATE 臂 `status <> 'disabled'` → `status NOT IN ('disabled','error')`、INSERT 臂补 `ON CONFLICT(account_id,key_fingerprint) DO UPDATE ... WHERE status NOT IN ('disabled','error')` 冲突守卫（identity/status/observed_at 围栏语义保留，恢复窗口写本次计算值）。gateway `accountkeystates/probe.go` 同型三处（`RecordFailure` 早退两处 + fenced UPDATE + 无 fence upsert 的 `current_state.status NOT IN ('disabled','error')`）。 | `proberepo/b1_4_error_terminal_test.go::TestB14RecordKeyFailurePreservesTerminalStatuses`、`accountkeystates/b1_4_error_terminal_test.go::TestB14RecordFailurePreservesKeyTerminalStates` / `TestB14RecordFailureSkipsWhenExpectedError`（error/disabled 均 no-op 且计数不变，rate_limited 正常 +1） |
| B3-H23 | HALF_OPEN 过期租约回收。`keymodelrecovery/redis_store.go` acquire Lua：阶段门放行 `HALF_OPEN`，该分支以 `EXISTS leaseKey` 判定租约是否已随 45s TTL 消失（存在即 `not_due`），接管沿用脚本内 `SET NX PX` 原子领取；`state.go` 新增 `recoveryDue`（HALF_OPEN 仅租约过期/缺失算 due），`Acquire`/`PrioritizeDue`/`SelectDue` 与 `ListDue` 同源使用。generation/dispatchRevision 不变，旧 owner 由 lease ID/过期 fence 拒绝。 | `keymodelrecovery/b3_h23_half_open_takeover_test.go::TestB3H23HalfOpenExpiredLeaseTakeover`（真实 Lua + miniredis TTL 过期：未过期拒绝接管/不进 due，过期可接管、旧 worker Settle=Stale 且 Commit=stale）、`TestB3H23SelectDueAndPureAcquireTakeover` |
| B3-H25 | 恢复/显式探针排除人工 disabled Key。`accounthealth/direct_input_reader.go` 在显式账户装载（`LoadAccount` 路径）内 join 业务库 `account_api_key_runtime_states` 读 `status='disabled'` 指纹注入 `exactkeyprobe.DirectInput.DisabledKeyFingerprints`；`exactkeyprobe/direct_input.go` `ToInput` 构建 Key 池时按指纹过滤，全部禁用返回可识别 `ErrNoAvailableAPIKey`（`errors.Is` 可判）；`LoadAccount` 将该 cause 原样透出。周期 J1 扫描面语义不变。 | `exactkeyprobe/b3_h25_disabled_key_test.go::TestB3H25DirectInputExcludesDisabledKeys`（disabled 指纹本地 `key_model_key_unavailable` 且上游 0 命中、active 对照 1 命中、全部 disabled 返回 ErrNoAvailableAPIKey）、`accounthealth/b3_h25_disabled_key_reader_test.go::TestB3H25LoadAccountExcludesDisabledKeys`（真实 PG：disabled+active → 只留 active；全部 disabled → 错误） |
| B3-H27 | 单条脏 state 隔离。`keymodelrecovery/redis_store.go` `ListDue`：state 缺失 / JSON 解析失败 / capabilityHash 不匹配时对该 due 成员 `ZREM` 后继续，Warn 仅输出 `capabilityHashPrefix`（12 位）+ `reason`，不输出原文；只有 Redis 基础设施错误（ZRANGEBYSCORE/GET/ZREM 失败）才返回错误。`RedisStore` 增 `SetLogger`（`NewRunner` 自动接线组合根 logger）。 | `keymodelrecovery/b3_h27_due_isolation_test.go::TestB3H27ListDueIsolatesCorruptStates`（1 条损坏 + 1 条哈希不符 + 1 条缺失混入 → 其余 2 条正常返回且坏成员被 ZREM）、`b3_h27_warn_redaction_test.go::TestB3H27RejectedDueStateWarnIsRedacted` / `TestB3H27CapabilityHashLogPrefix` |

验证：gateway `go test -count=1 ./internal/accountkeystates/`（含 `JUHE_AI_TEST_EXTERNAL=1` 全包 63.9s）、jobs `go test -count=1 ./internal/keymodelrecovery/ ./internal/accounthealth/`（含外部 PG 全包）、shared `go test -count=1 ./accounttest/...`（含外部 PG `proberepo` 全包）全绿；三模块 `go build ./...` 与目标包 `go vet` 通过。

文档同步：[AI账户主探测与Key模型短屏蔽修复设计](../functions/AI账户主探测与Key模型短屏蔽修复设计.md) §8.3 补第 8-10 条（HALF_OPEN 接管/脏 state 隔离/disabled Key 排除）、§15 收窄 disabled 口径；[AI账户错误语义与状态变更边界](../functions/AI账户错误语义与状态变更边界.md) §2 补 Key 级 error/disabled 终态写入守卫契约。

### R7-B 批11 交付记录（账户状态写面 + 授权 + 小时额度，写码档，2026-11-01）

| 编号 | 修复内容（落点） | 钉住测试 |
| --- | --- | --- |
| B1-1a | 流量迁移写面补 `config_revision = COALESCE(config_revision,0)+1`（owner disabled/temp 两分支 + 授权实例分支），提交后经既有 `CacheInvalidator.InvalidateGatewayRuntime`（reason `account_traffic_migration`）失效；失效失败只 Warn 不回滚。未改可迁移源状态白名单（B 类呈报）。`accounts/m11_traffic_migration.go` | `TestW13gTrafficMigrationRevisionInvalidationAndCAS`（revision 递增、并发 PATCH CAS 冲突、失效端口被调用） |
| B1-2a | 人工 PATCH `status=disabled` 与后台探测关闭臂共用 `releaseAccountLockAndQualityEnforcement`：结束锁死（UNLOCKED/终态列清零/generation+1）、清 `account_quality_enforcements` active 行；Key 运行态清理端口调用面扩到手动停用。`accounts/patch.go` | `TestPatchManualDisableReleasesLockAndQualityEnforcement`（ENGAGED 锁+quality active → 全部结束；active 无副作用） |
| B1-3 | 自动禁用两臂统一 `isHardUnavailableAccountStatus` 短路（含 pending_test/quality_isolated/error）；绑定臂补预读 + `status=? AND config_revision=?` CAS，error→error 不再重写原因。`chain_error_policy_effects.go` | `TestW1EEffectsDisableHardUnavailableGuard`（三种特殊状态不覆盖、active 正常写 error） |
| B3-H1 | 团队停用防御：`patchForOwner` 在 sync 前校验 team source 的 `system_teams.status='active'`，否则返回创建路径同文案「团队不存在或已停用」（400），任意字段 PATCH 均不可复活；`activeTeamMemberIDs` 展开 join 团队状态。`authz/mutations.go`、`sync.go` | `TestTeamDisabledBlocksGrantPatchAndActiveResync`（仅改 limits 被拒/不 provision/active 团队不变） |
| B3-H11 | 计划激活重建小时额度绑定。新增 jobs `internal/quotahourly`（DELETE+upsert，列集与 gateway 写侧逐字段一致）+ `oauthrefresh.APIKeyScheduleActivationHook`，在翻 active 的同一事务内重建、提交后打脏；无小时额度/停用不写；坏额度文档逐项隔离不阻塞同批。gateway 零改动；`jobregistry` Writes 追加绑定/脏表。 | `TestSyncApiKeyScheduleActivationRebuildsHourlyWindowBinding`（8 字段断言+脏 generation=1）、`...BindingIdempotent`、`...DeactivationWritesNoBinding`、`...WithoutHourlyQuotaWritesNoBinding`、`...InvalidQuotaJSONSkipsRow`、`TestQuotaHourlyWindowFromScheduleActivationBinding`（statsagg 产出窗口）、`TestAPIKeyScheduleSyncNeverNilActivationHook` |

验证（合并三批独立复跑）：gateway `accounts 4.111s`、`authz 0.323s`、`accountkeystates 0.263s`、`cmd 22.513s`、`apikeys 0.297s`、`gatewayquota 11.051s`；jobs `keymodelrecovery 12.115s`、`accounthealth 30.934s`、`oauthrefresh 58.951s`、`statsagg 59.893s`、`quotahourly 0.234s`、`jobregistry 0.228s`（均 `JUHE_AI_TEST_EXTERNAL=1`）；shared `accounttest/...` 全绿（proberepo 17.656s、manualtestrepo 15.684s 实跑）；三模块 `go build ./...` 通过。

### R7-B B 类待裁决清单（需用户确认后实施）

| 编号 | 一句话 | 建议 |
| --- | --- | --- |
| B1-1b | 流量迁移允许把 error/pending_test/quality_isolated/锁死账户直接改写为 disabled/temp，且 temp 臂继承旧 schedulable | 拒绝迁移非 active 源，或显式确认迁移=强制接管 |
| B1-7 | 「清理运行状态」把 quality_isolated 恢复 active 但不清质量处罚 | 重置时一并清 enforcement（推荐）或禁对隔离账户重置 |
| B1-10 | PATCH `{status:active, schedulable:false}` 静默改为可调度 | 尊重显式 false，或拒绝该组合并报错 |
| B2-H3 | gateway 运行缓存 stale 最长 10min 仍调度已 error/disabled 账户 | 缩短 retain 或状态类失效强制回源（影响性能契约） |
| B2-M1 | 冷却复测成功可清除用户显式 temporary_unavailable/rate_limited（无 provenance） | 补来源列/标记，人工状态仅人工恢复；同步修订两份文档冲突条款 |
| B2-M5 | 同账户重试跳过电路 PrepareAttempt | 重试臂也过电路门（推荐）或文档明示豁免 |
| B3-H12 | 计划边界整分钟错过则 Key 状态整天不纠正 | 增加窗口内纠偏（按当前时刻判 in-window） |
| B3-H24 | keymodelrecovery Unknown 无终态，永久 10s 重试 | 定义放弃终态（如 N 次后 CLOSED/废弃）并清理 |
| B3-H31 | J3b Key-模型 ForegroundLimit=100000，与设计固定 2 漂移 | 统一回 2（推荐，恢复风暴保护）或修订设计契约 |

### R7-B C 类裁决清单（功能缺口/容量）

| 编号 | 一句话 | 建议 |
| --- | --- | --- |
| B2-H1/H2 | 账户运行态软阻断（degraded/precheck_pending/half_open）与降级排序整体未落地：无 writer、无 half-open 执行器、端口恒 disabled | 立项实现或从文档移除该链路；当前文档与运行事实冲突 |
| B1-5 | temporary_unavailable 的 schedulable 三套语义（人工 0/策略 1/迁移继承） | 统一语义并写轴契约 |
| B3-H26 | continuation 保留槽仅在「全局+本来源」双标志时生效，与纯函数 SelectDue 口径不一致 | 对齐 Lua 与设计 §8.2 |
| B1-8 | gateway `accountkeystates.DeferProbe` 无生产调用方（死代码） | 删除或标注退役 |

## R7-C 统计口径域

- 锚定范围：读面（`gateway/internal/statreads`、`ipstats`、`logreads`、`publicapilogs`）；写入与聚合（`jobs/internal/usagewriter`、`ingestgate`、`statsagg`、`statsverify`、`retention`）；定价/额度/结算交叉面（`gateway/internal/pricing`、`gatewayquota`、`gatewayusage`、`chain_pricing.go`、`chain_catalog.go`、`chain_usage.go`）。
- 关联契约：模型价格与用量统计口径、统计指标与分层聚合设计、接口契约与权限矩阵、额度与计费相关设计。
- 状态：攻击轮 3 视角 41 条 → 送验 20 条 → **18 confirmed / 2 refuted / 2 既有裁决重复**；A 类 8 条已修复并验证，其余 B/C 登记。

### R7-C 验证与处置

| 编号 | 裁决 | 分类 | 处置 |
| --- | --- | --- | --- |
| C1-H1 AI 健康页把非质量终态计失败 | confirmed | A（文档明文） | 已修：neutral 集合不计入健康率 |
| C1-C2 概览今日走 daily、跨天走 5min 窗口且缺行静默全 0 | confirmed | B | 呈报 |
| C1-C3/C5 非法日期静默丢过滤/回落今天 | confirmed | A | 已修：统一 400 |
| C1-C6 授权账户在 options 不可见 | confirmed | A | 已修：复用可见性子句 |
| C1-C7 缺 metadata 静默丢行但 total 不变 | confirmed | A | 已修：占位行 + total 自洽 |
| C1-C18 空 SystemAccountID 跨租户 | **refuted**（HTTP 面不可达） | C 防御 | 观察 |
| C2-H1 迟到记录永久不聚合 | confirmed | B | 呈报（gateway 内存 buffer 残余窗口） |
| C2-M1 聚合期重定价 | **refuted**（生产未注入 estimator） | C 潜在 | 观察 |
| C2-M6 单行缺 access_type 卡死全部聚合 | confirmed | A | 已修：行级隔离 + 游标推进 |
| C2-H3 分区备忘过期 + 批级毒丸 | confirmed | A | 已修：存在性复核 + 23514 清备忘重试 |
| C2-H2 死信对门控/对账不可见 | confirmed | A | 已修：DeadLetterCount 阻断聚合，DroppedCount 只观测 |
| C3-H1 在途额度预占恒 no-op | confirmed | B（需设计选择） | 呈报 |
| C3-H2 自定义模型部分价格→整单 NULL | confirmed | B（需产品口径） | 呈报 |
| C3-H3 冻结快照空 billed tier 错价 | confirmed | A | 已修：serviceTierForWrite 回退链 |
| C3-H4 客户端 service_tier 任意 token→NULL | confirmed | B | 呈报 |
| C3-H5 个人模型价作用于被授权调用方 | confirmed | 重复（目标态差异） | 不重复登记 |
| C3-H7 取消/断流规避额度 | confirmed | 重复（既有裁决） | 不重复登记 |
| C3-H8 授权/团队额度无在途预留 | confirmed | B | 呈报 |
| C1-C9 额度读投影欠计 | confirmed | B | 呈报（与 C3-H1 同链） |
| C3-H13 字符串参数绑整数列 | **refuted**（pgx 文本参数推断） | — | 不登记 |

### R7-C A 类修复（批12）

| 编号 | 修复内容（落点） | 钉住测试 |
| --- | --- | --- |
| C1-H1 | `statreads/aihealth.go` 新增 `j1NonQualityOutcomes`（stale/framing_complete_neutral/opaque_upstream/gateway_capacity/gateway_policy/downstream_closed）返回空串不计入；真实账号失败仍 failure | `TestJ1OutcomeHealthStatusExcludesNonQualityOutcomes`、`TestJ1NeutralOutcomesStayOutOfHealthRate` |
| C1-C3/C5 | `statreads/usagerecords.go` 新增 `strictDateQueryValue` + `statReadsBadRequest`（handler 400）；`usageoverview.go` 日历校验 400，不再回落 today | `TestUsageRecordsInvalidDatesRejectBadRequest`、`TestUsageOverviewInvalidCalendarRejectsBadRequest`、`TestUsageRecordDateRangeKeepsValidContract` |
| C1-C6 | `accountusage.go` options/selectedIds 复用 `accountUsageCallerVisibilityClause` | `TestWdAccountUsageOptionsIncludesAccountAuthorizations`（真 PG） |
| C1-C7 | `accountusage.go` metadata 缺失行用占位名保留，total 与实际行自洽 | `TestWdAccountUsageKeepsRowsForDeletedAccounts`（真 PG） |
| C2-M6 | `statsagg/aggregate.go` 缺 access_type 行 Warn 后跳过，游标正常推进；skipped 计数留痕 | `TestC2M6BadAccessTypeRowIsolatedAndCursorAdvances`、`TestC2M6OnlyBadRowBatchStillAdvancesCursor`、`TestC2M6RowSkipReasonMatchesErrorArm` |
| C2-H3 | `usagewriter/partitions.go` 备忘可失效（`forget`/`markEnsured`/存在性查询）+ `isBenignPartitionDDLConflict`；`store.go` 每批按 `pg_class.relispartition` 复核，INSERT 撞 23514 清批内日期键交上游重试 | `partitions_c2h3_test.go` 4 例 |
| C2-H2 | `ingestgate` DeadLetterCount 阻断聚合（可观测降级，writer 恢复后解除）、DroppedCount 只观测；`usagewriter` 暴露计数并接入 DrainStatus | `TestC2H2DeadLetterBlocksAggregationGate`、`TestC2H2DeadLetterClearedReopensGate`、`TestC2H2DroppedCountIsVisibleButNotHardGate`、`TestC2H2DeadLetterTakesPrecedenceOverBacklog` |
| C3-H3 | `usagewriter/freeze.go` 改用 `serviceTierForWrite(enriched)` 回退链，快照档位价与网关 cost_usd 同源 | `TestC3H3FreezeServiceTierFallbackChain`、`TestC3H3FreezeSnapshotUsesTierPricing` |

验证（真实输出）：`statreads` 默认 0.290s、真 PG 全包 17.909s；jobs `usagewriter` 13.435s、`statsagg` 48.661s、`ingestgate` 0.238s（均 `JUHE_AI_TEST_EXTERNAL=1`）；gateway `go build ./...`、jobs 目标包 `go build` 通过。文档同步：模型价格与用量统计口径 §2.9、统计指标与分层聚合设计「分区与灌入守卫」、接口契约与权限矩阵「统计读面参数与可见性」。

### R7-C B 类待裁决清单

| 编号 | 一句话 | 建议 |
| --- | --- | --- |
| C3-H1 | API Key 在途成本预占因 provider 恒空而 no-op | 预检后移到账户选定或按模型跨 vendor 保守估价 |
| C3-H2 | 自定义模型任一价格即可 active，未定价维度使整单 cost NULL | 文本模型强制 input+output 齐备；或禁止个人行定价（更彻底） |
| C3-H4 | 客户端任意 service_tier 使成本 NULL | 未知档位回退 standard 计价或入口拒绝 |
| C3-H8 | 授权/团队额度无在途预留 | 复用 API Key 预留机制或明确文档接受 |
| C1-C2 | 概览双源（daily vs 5min 窗口）且缺行静默 0 | 统一走 daily 或窗口缺行时回退实时聚合/标记 partial |
| C2-H1 | 迟到记录（gateway 内存 buffer 窗口）永久不聚合 | 登记为已知缺口并在 statsverify 增对账 |
| C1-C9 | 额度读投影欠计（聚合延迟+5s 缓存） | 与 C3-H1 一并解决或文档明示 |
| C1-C18 | 认证上下文空 SystemAccountID 时 usage-records 无租户条件 | 加 fail-closed 断言（防御加固，当前不可达） |

# R7 终报（独立域深挖收口，2026-11-01）

## 覆盖矩阵

| 域 | 锚定 | 攻击 | 送验 | confirmed | A 类修复 | 剩余 |
| --- | --- | --- | --- | --- | --- | --- |
| R7-A 模型检测 | modelcheck* ~22.5k 行 | 5 角色 ~86 条 | 16 | 15（1 refuted=已登记 DD2-E11） | 14 + 夹具债 3 + 落点复核第二批 15 | 剩余假设 ~60 条登记；P-H4 契约冲突=T88 |
| R7-B 状态流转 | accounts/accountkeystates/authz/apikeys/keymodelrecovery/gatewayquota 等 | 3 角色 69 条 | 22 | 20（3 refuted，2 与 DD-A11/DD-A9/DD3-A2 重复） | 9 | B 类 9 条=T91；C 类 4 条入清单；T89/T90 派生 |
| R7-C 统计口径 | statreads/ipstats/logreads/usagewriter/statsagg/pricing/gatewayquota | 3 角色 41 条 | 20 | 18（2 refuted，2 既有裁决重复） | 8 | B 类 8 条=T93；低危登记 |

合计：攻击 ~196 条假设 → 送验 58 → confirmed 53 → **A 类代码修复 46 条**（R7-A 14+15、R7-B 9、R7-C 8）+ 测试夹具债 3 处，全部带钉住测试，全部真 PG / 真环境实跑绿；三模块 `go build ./...` 通过。

## 收敛状态（如实）

- **未达成「连续三轮无 B 类确认」收敛协议**：R7 三个域各自单轮完成攻击→验证→修复→落点（R7-A 有独立落点复核），没有对每个域再追加第二轮攻击。继续深挖仍可能产出新确认项；本轮以「覆盖未审查功能域」为优先目标收敛。
- 遗留裁决面集中且已登记：T88（探针 usage 口径）、T91（R7-B 9 条）、T93（R7-C 8 条）、T89/T90（jobs 夹具债/存量回填）。全部待用户逐条「修 / 不修 / 改文档」。
- 未提交改动：R7 全部修复 + R1–R6 尾批仍混存工作区（含并行会话 chat 域在制品），未 commit（遵守无授权不提交）。

## R7-B/R7-C B 类裁决结果（2026-11-01，用户逐条确认）

| 编号 | 裁决 | 实施要点 |
| --- | --- | --- |
| T91-1 / B1-1b | 修 | 流量迁移只允许 `status=active` 且未锁死的源账户；特殊状态走人工恢复 |
| T91-2 / B1-7 | 修 | runtime-reset 对 quality_isolated 一并清 `account_quality_enforcements` |
| T91-3 / B1-10 | 修 | `schedulable` 改为状态派生：Create/PATCH 不再接受用户入参（传入 400）；active=可调度、其余不可调度（系统写面除外）；升级归一存量 active+schedulable=0 |
| T91-4 / B2-H3 | 修 | 运行缓存 last-good retain 从 10min 缩到 2min；网关写面同步失效不变 |
| T91-5 / B2-M1 | 修（文档） | 明确「人工临时状态 TTL 到期后由冷却复测恢复」为设计内行为，统一两份文档并校验 generation |
| T91-6 / B2-M5 | 修 | 同账户安全重试前也调用电路 PrepareAttempt |
| T91-7 / B3-H12 | 修 | 计划同步每轮按「当前是否在窗口内」纠偏，不再只依赖边界分钟 |
| T91-8 / B3-H24 | 修 | keymodelrecovery Unknown 连续 12 次后转 CLOSED（capability_not_found）并移出 due |
| T91-9 / B3-H31 | 修 | J3b `business/key_model_runtime` ForegroundLimit 100000 → 2，与主链固定契约一致 |
| T93-1 / C3-H1 | 修 | 在途预占后移到选定账户、拿到 provider/映射模型之后，按真实成本预留 |
| T93-2 / C3-H2 | 修 | 自定义模型保存按能力域校验价格完整性，缺维度 400，不允许 active 半价模型 |
| T93-3 / C3-H4 | 修 | 未知/无价 service_tier 回退 standard 计价，不再整单 NULL |
| T93-4 / C3-H8 | 修 | 授权/团队额度纳入同一在途预留机制 |
| T93-5 / C1-C2 | 修 | 概览窗口缺行回退事实表实时聚合（≤31 天），响应标记来源/实时性 |
| T93-6 / C2-H1 | 修 | 安全水位纳入 gateway 内存 buffer 最大滞留；statsverify 增加按 usage_records 重算抽样对账 |
| T93-7 / C1-C9 | 修 | 由 T93-1/T93-4 在途预占覆盖并发窗口；文档明示「已结算投影 + 在途」口径 |
| T93-8 / C1-C18 | 修 | 统计读面非管理员空 scopedID fail-closed |

实施分四批（批13–批16）并行：账户/读面、运行态/电路/J3b、额度/定价、jobs 计划与恢复/对账。完成后统一回归并删除 T91/T93 条目。

## R7-B/R7-C B 类修复交付（批13–批17，2026-11-01）

用户逐条裁决 17 条全部「修」，四批并行实施 + 1 个补派，全部落地并验证；T91/T93 待办条目已按「解决即删」移除。

| 批 | 条目 | 落点与效果 | 钉住测试 |
| --- | --- | --- | --- |
| 批13 账户/读面 | T91-1 迁移源门、T91-2 重置清处罚、T91-3 schedulable 状态派生、T93-8 fail-closed | 迁移只允许 active+schedulable=1 且无 ENGAGED/DEAD_CONFIRMED；runtime-reset 同事务清 quality enforcement；Create/PATCH 拒绝 schedulable（400），用户状态写入按 active→1/其余→0 派生，升级链 revision 13 幂等归一存量 active+schedulable=0；统计读面非管理员空租户 403 | `TestT91TrafficMigrationSourceGateOwner/Authorized`、`TestT91RuntimeResetClearsQualityEnforcement`、`TestT91SchedulableRejectedOnCreateAndPatch`、`TestT91SchedulableDerivedFromStatusWrites`、`TestT91AccountSchedulableNormalizationStep`、`TestT93StatReadsFailClosedWithoutTenantScope`（均真 PG） |
| 批14 运行态/电路 | T91-4 retain 2min、T91-6 重试过电路、T91-9 J3b 并发 2 | 四族 retain 10min→2min（60s revalidate 不变）；新增 `CircuitService.CheckDispatchAdmission` 只读准入，同账户重试臂 OPEN/SUSPECT 阻断且不写状态；`business/key_model_runtime.ForegroundLimit` 100000→2 | `t91_4_retain_ttl_test.go`、`t91_6_sameaccount_circuit_test.go`、`t91_9_foreground_limit_test.go`、`TestT91J3bDispatchPathEnforcesForegroundLimitTwo` |
| 批15 额度/定价 | T93-1 预占后移、T93-4 授权预占、T93-2 价格完整性、T93-3 tier 回退 | 新增 `RequestInflightReserver` + `chain_inflightquota.go`，在选定账户/vendor/上游映射模型/请求体定稿后原子预留 API Key+授权+团队 scope（任一超限整体拒绝）；自定义模型按能力域校验价格完整性；未知/无价 service_tier 回退 standard 计价 | `TestT93RequestInflightReservesAPIKeyScope`、`...DeniesNthConcurrentRequest`、`...AuthorizationScopes`（真 PG）、`TestT93ReserveUsesUpstreamModelAndVendor`、`t93_price_completeness_test.go`、pricing golden 更新 |
| 批16 jobs 计划/恢复/对账 | T91-7 窗口纠偏、T91-8 Unknown 终结、T93-6 水位+对账 | 计划同步每轮按当前窗口补偿错过边界（人工启停仍到下一边界接管）；keymodelrecovery 连续 12 次 Unknown 转 CLOSED(capability_not_found) 并移出 due；ingestgate 安全水位纳入 gateway buffer 5min 保守下界，statsverify 增加按 usage_records 重算最近 1h 对账（差异→partial+Warning） | `TestT917*` 11 例、`TestT918*` 5 例、`TestT936*` 6 例 + worker 任务结果测试 |
| 批17 概览回退 | T93-5 窗口缺行回退 | `usageoverview_realtime.go`：summary/hourly-trend/model-distribution/errors 四节在窗口缺失时实时聚合事实表（≤31 天），窗口命中仍读快照；响应新增 `dataSource=window/realtime` | `TestT93UsageOverviewSummaryFallsBackToRealtime` 等 7 例（真 PG） |
| 文档 | T91-5、T93-7 | 明确人工临时状态 TTL 到期由冷却复测恢复（匹配 generation）；额度口径统一为「已结算投影 + 在途预留」 | 两处文档已更新 |

验证汇总：gateway `accounts/authz/accountkeystates/statreads/gatewayquota/pricing/providers/gatewaydispatch/gatewayruntimecache/business/key_model_runtime/cmd` 全绿（cmd 39.0s）；jobs `oauthrefresh 91.9s / keymodelrecovery 12.3s / ingestgate 0.2s / statsverify 11.5s / statsagg 60.4s` 全绿（真 PG）；maintenance 升级链 revision 13 + 归一测试真 PG PASS、cmd 整包 175s 绿；三模块 `go build ./...` 通过。已知既有失败（与本批无关）：`TestOAuthManagedDefaultsCreateWritesConnectionRows/gemini_google_oauth`（URL v2 遗留）、aipublic 夹具列缺失、jobs cmd 18 个夹具缺口（T89）。

## 本地可修项收口（2026-11-02）

| 事项 | 结论 | 证据 |
| --- | --- | --- |
| T95 schema 断言漂移 | 已修：`w1g2CoverSchemas` 6→5 删 `juhe_chat`；同源 `w1g2GrantSchemas` 同步（GRANT 不存在 schema 会 3F000）；`w1g2CoverPostgres` 子进程补升级账本种子 | 9 个调用方用例全 PASS，cmd 包 125.6s 绿；独立复验 `TestW2BBalanceRefreshLoadOutcomeArms`/`TestW1G2ComposeSystemAPIPostgresSuccess` 11.1s PASS |
| T89 jobs cmd 18 个既有失败 | 已修：夹具补 `accounts.background_probe_enabled`（与生产 DDL 同形）、夹具建库统一写 `schema_state`/`schema_migrations`（revision 取 `contracts.SchemaRevision`，不硬编码）、`w14q` 夹具 base_url 补 `/v1` 版本根 | 失败 18→0，`JUHE_AI_TEST_EXTERNAL=1 go test ./cmd/juhe-ai-jobs/` 114.1s / 复验 71.8s 绿 |
| T94 编辑 Base URL 不写连接域 | **HEAD `3d3ab1e32` 已修复**（`buildAccountUpdatePayload` 已透传 connections，`accountEditPatch` 按档案差量构造），本轮只补差量回归测试 5 例；前端 `test:unit` 94 文件/1172 例绿 | `accountSavePayload.test.ts` 新增用例组；typecheck 无错 |
| T94 同族审计 | 新登记 T99（克隆无条件覆盖连接，需裁决）、T100（批量改 endpointModes 只写凭据域）、T101（原生导入模板缺 connections/健康三件套）、T102（媒体/自定义协议档案派生缺口致编辑保存不可达） | 逐条含证据/归属/完成判定 |
| T88 探针 usage 口径 | 未改代码：属文档契约冲突，建议取「不写 usage」，待裁决后回写通用用量文档 | 台账 R7-A P-H4 |

T90（生产存量小时额度回填）与 T94 的生产账户数据修复需生产授权，本轮未执行。
