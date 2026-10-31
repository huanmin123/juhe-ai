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
- 当前阶段 / 当前轮次：R1（维度制）闭环；**R2 攻击轮（场景制）进行中**——入口×输入空间×时序，7 入口组：A /v1 文本主入口（chat/completions、responses、compact）｜B 原生协议+realtime（messages、gemini、interactions、WS）｜C 目录与文件族（models、files/containers/vector_stores）｜D 管理面写入口（accounts/apikeys/groups/routes/oauthmgmt 建号）｜E 认证与公开面（登录/验证码/改密/aipublic token）｜F 媒体域｜G 停机与审计收尾序列。R1 已证伪/已裁决条目列入派发排除声明；R1 修复批 25+ 处新代码为合法攻击面。并行会话在制品（internal/chat/**、internal/playground/）排除。
- 收敛计数：0 / 3（R1 存在 B 类确认与修复回归，不计干净轮；R2 起点重算）。

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

### R1 同源发现与遗留（攻击/修复过程中产出，未进修复流）

- audit_summary.go `SummarizeAuditPayloadForLimit`（:57-75）对超限 body 无门重算 sha——Node 镜像有门（sha256BufferIfSmall）；给它加门会改变 payload-summary JSON 的 originalSha256 契约，需先裁决字段形态 → C 类。
- DD-C6 剩余缺口已登记待办 **T86**（非流式桥接臂与其余四族流式桥方向未接 usage 快照供给，机制已就绪逐族接线）。
- DD-F5（undetermined）：native gemini `:generateContent`+body stream:true 的下游消费路径与上游字段行为缺证，待补证后裁决。

## 修复记录

| 批 | 内容（指向问题记录文档） | 验证证据 | 落点复核轮次 |
| --- | --- | --- | --- |
| R1-批1+批2（2026-10-31，A 类 22 条，7 写码档并行，未 commit） | DD-D1（producer 在途信封集合+撤回延迟生效；dd_d1_inflight_retract_test.go 5 例，w13g/w13h 保持绿）；DD-D4（Marshal 移入 worker：DeferredCapture/CaptureDeferred+估算记账；等价回归 3MB body 逐字节一致+记账测试）；DD-D5（1MB 内联门；L2 指纹独立门槛已核实不退化）；DD-D8（DroppedCount 死字段删除，主代理直修）；DD-G1（HTTP 排空包装为最后注册组件先关，supervisor 未动；shutdown_order_test 5 例）；DD-G2（failRuntime 有界排空 10s 后 exit(1)）；DD-G3/G15（注释回正）；DD-E1（rehydrate 按账户 BoundGroupID 直读候选；supergroup_test 链级 2 例）；DD-E4（四家 patch 收 error_handling_rule_overrides 与写面对齐）；DD-F1（覆盖入参改映射右侧 UpstreamModel）；DD-F2（service_tier 按最终 family 分支）；DD-A1（熔断计数 EvalLua 原子化）；DD-A2（compat 分支补入口保护）；DD-A3（aipublic 惩罚窗+realtime 失败计数）；DD-A4（aipublic 256KiB body 上限）；DD-G9（realtime 计数归零删键）；DD-C1（SSE CRLF 边界补齐三处词表）；DD-C2（pending 上限+越限放行）；DD-C6（桥接 usage 快照通道，剩余缺口=T86）；DD-F6（多值头保留）；DD-B7（weak memo 免重复 DB 查询） | 触及 11 包聚合测试全绿+五模块构建通过；文档同步 6 处+待办 T86 | R1-落点复核（19/22 确认，4 RC 即修闭环，见下） |
| R1-批3（2026-10-31，B 类批复 2 条 + 同族新发现 1 条，主代理与 2 写码档） | DD-F3（compact 预算豁免收 codexTurn 身份门，标记/预算面拆分，文档 4 处同步）；DD-D7（.corrupt 7 天保留 TTL 进 scanCapacity 清理周期）；**DD-D10 新发现即修**：spool 两镜像常量无类型整数按纳秒比较——容量缓存「30s」实际 30µs、.tmp「1h」实际 3.6ms（DD-D7 写码档初版同型错误暴露），恢复镜像契约语义（30*time.Second/time.Hour，注释立规）；.corrupt 修复初版曾把 err 作用域缺陷引入（空指针 panic，测试暴露已修） | gatewayusage 全包 2.2s 绿（含 w13d/.tmp 回归与 DDD 三例）、gatewaypreauth/gatewaycodex/cmd 定向与全包绿 | R1-落点复核（批3 为收口批复项，落点由各写码档自证+负向场景钉住；下轮复核覆盖） |

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

## B 类过目清单（R1 收口呈报；2026-10-31 用户批复「确认清楚是真的有问题那么就修复吧」→ 两条均先复核证据链成立后修复，随批交付）

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

## 终报（收口时写）
