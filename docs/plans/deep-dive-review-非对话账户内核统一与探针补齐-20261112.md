# 深挖复审报告：非对话账户内核统一、单菜单场景化与探针补齐（计划文档）

- 复审对象：`docs/plans/计划-20261111T154901000Z-非对话账户内核统一与探针补齐.md`（1039 行，当前状态“待开始/定稿”）
- 复审日期：2026-11-12
- 授权模式：report-only。只读取证，未改代码、未改目标文档行为、未运行测试。
- 代码基线：`master` `be3751696` + 当前工作区未提交改动；本计划文件为未跟踪状态。
- 复审方式：4 个独立攻击视角（数据迁移、探针、锁死/状态机、前端 IA）+ 4 个独立证伪角色复核高严重度结论。攻击与复核角色互不复用。
- 结论：**方向成立，但不能按现文开工。** 发现 3 个 blocker、14 个 major、12 个 minor，另有 6 处计划内部自相矛盾。建议先修订计划，再进入 B0/B1。
- **2026-11-12 追加裁决 D13 后的状态**：用户明确“不做向后兼容，非对话账户上线统一删除”。因此本报告 B1（旧二进制回滚/maintenance 兼容）与 M1（混合 audio/realtime 迁移）的兼容性结论按新口径失效，改为 cutover 删号重建；B2（J1 exactkeyprobe 真实生成）与 B3（D12 释放点不在锁死门）仍是 blocker，必须在 B4/B5 前解决。

---

## 1. Blocker（必须先改计划）

### B1. `capability_domain` 删 CHECK 会同时打断部署链与回滚链

**结论**：计划 §3.5/§10.1/B1.1 的“移除固定四值 CHECK，改文本列 + 注册表校验；回滚换回旧二进制即可”不成立。当前 gateway/jobs 的启动门禁和 maintenance 的 ensure 校验都硬性要求存在“覆盖四值”的 CHECK；删掉后，旧二进制无法启动，旧 maintenance 复跑也会失败。计划还漏掉了 maintenance 侧的形状函数与两处内联 DDL。

**证据**：
- gateway 启动门禁：`backend-go/projects/gateway/cmd/juhe-ai-gateway/capability_domain_schema.go:120-136`；调用点 `.../compose.go:312-314`，失败即 `main.go` 退出。jobs 镜像：`backend-go/projects/jobs/cmd/juhe-ai-jobs/capability_domain_schema.go:139-155`。
- maintenance 每次 `EnsurePostgres` 末尾都调用 `VerifyPostgresBusinessCapabilityDomainShape`：`backend-go/projects/maintenance/internal/schema/pg_schema.go:134-136`；无 CHECK 时报“未找到该列的 CHECK 约束”（`:213-216`）；判定函数只认四值（`:221-228`）。
- 建表与加列仍内联四值 CHECK：`backend-go/projects/maintenance/internal/schema/pg_schema_business_tables.go:668`、`:1587`。
- 计划 §10.3（第 683 行）写“回滚：换回旧二进制即可停止媒体探测”；对 B1 的 schema 变更不成立。

**影响**：B1 上线后无法按计划回滚；若新场景账户已写入 `embedding/realtime` 等值，反向重加四值 CHECK 还会被存量行拒绝，必须清洗数据。fresh 库与迁移库的列形状也会分叉。

**建议的计划修改（推荐 A）**：
- **A（推荐，最小风险）**：B1 不删 CHECK，改为把 CHECK 扩展到八值（现有四值 + embedding/rerank/moderation/realtime）。旧 gateway/jobs 的判据是“CHECK 包含四值”，旧 maintenance 同判据，所以新旧二进制、旧 maintenance 都能通过；回滚只需换二进制。新增场景仍由写侧注册表校验。代价是未来新增场景仍需一次小 DDL——这不是用户要求，用户要求的是“不再加菜单”，可以接受。
- **B（保留无 DDL 目标）**：两阶段发布。先发布 N-1：gateway/jobs/maintenance 门禁改为“接受无 CHECK 的文本列”，但仍兼容四值 CHECK；确认 N-1 可回滚后，N 再删 CHECK。计划必须补反向 DDL、新值行清洗、备份与回滚演练。
- 无论 A/B，B1.1 必须显式列入 maintenance 的三处落点：`VerifyPostgresBusinessCapabilityDomainShape`、`pgCapabilityDomainCheckComplete`、accounts 建表/加列 DDL；并写明**不得 bump `capabilitydomain.RuleVersion`**（`shared/platform/capabilitydomain/stage.go:66-68` 对存量记录做版本校验，bump 会让全网启动失败）。

---

### B2. J1 媒体探测实际走 `exactkeyprobe`，按计划只扩 SQL 白名单会真实生成图片/全部失败

**结论**：计划 B4 写“J1 候选 SQL 扩展 preset/mode、移除 image active 豁免”，但没有改 J1 的执行引擎。J1 用的是 `exactkeyprobe.ProbeOpenAI`，不是 `accountprobe`；其 `images_json` 构造的是**真实生成请求**（prompt + size + quality），`video_create`/`audio_job_get` 等未登记 mode 会 fail-closed。按现文实施：视频/音频探测全部失败并被误判异常；图像周期探测继续真实生成、产生费用。

**证据**：
- J1 执行器：`backend-go/projects/jobs/internal/accounthealth/executor.go:26-58` 调用 `exactkeyprobe.ProbeOpenAI`。
- 真实生成：`backend-go/shared/platform/accounttest/exactkeyprobe/probe.go:340-343,361-407,379-388`（prompt “Solid black”、size 1024x1024、webp）。
- 媒体 preset 被门禁拒绝：`exactkeyprobe/direct_input.go:331-350,421-459`；未登记 mode 在 `backend-go/shared/platform/upstreamenvelope/endpoint.go:108-274` fail-closed。
- J1 SQL 白名单：`backend-go/projects/jobs/internal/accounthealth/direct_input_reader.go:655,657`。

**影响**：B4 是本计划“后台探针/状态机并轨”的核心；按现文实施会同时造成误伤存量账户与真实计费，属于阻断级。

**建议的计划修改**：
- B4 增加明确条目：J1 媒体/工具探测改走“无副作用连通性构造器”。二选一：
  - 在 `exactkeyprobe` 增加 `connectivity` 分支（复用 `accountprobe` 的构造器），并扩展 `connectionPresetProfiles`、`isSupportedDirectProfile`、`upstreamenvelope` 登记；
  - 或让 J1 对媒体/工具账户改走 `accountprobe` 连通性实现，`exactkeyprobe` 只保留对话挑战。
- 验收增加负断言：J1 媒体探测不得发出 `POST /images/generations` 的真实生成请求，不得发出 `video_create`/`audio_job_create` 创建请求。
- B7.4 同样要核实 `exactkeyprobe` 对 `embed_content`/`rerank_json`/`moderation_json` 的支持；只加 J1 SQL 白名单不够。

---

### B3. D12 的限流释放不在锁死门，只改锁死文档/门禁兑现不了

**结论**：计划 D12 说“`rate_limited`/`temporary_unavailable` 在锁死窗口继续 hold”，B5 只写“改锁死设计文档 §4/§10 + 状态机测试 + 交接文案”。但现行代码里，transient 的“释放”主要由两条锁死门之外的路径完成：
1. 显式/系统错误策略 cooldown 先写账户状态，随后 `explicitPolicyFailure=true` 跳过锁死记账与锁死门，直接换号；
2. 账户被写成 non-active 后，候选 SQL 的 `statusSet='active'` 把它排除，锁死行虽然还在，但没有作用对象。

**证据**：
- `backend-go/projects/gateway/internal/gatewaydispatch/attemptoutcomes.go:376-377`（`explicitPolicyFailure` 定义）、`:515`、`:578`（只有 `!explicitPolicyFailure` 才走锁死门）。
- `backend-go/projects/gateway/cmd/juhe-ai-gateway/chain_ports.go:706-709`（`decision != nil` 即 `explicit_policy`）、`:664-690`（cooldown decision 先写状态）。
- `chain_accounts.go:728-731,789-792`（默认候选 `statusSet='active'`）；`internal/gatewayruntimecache/accounts.go:73-78`（主调度不传 `IncludeUnavailable`）。
- 计划 §6.4（第 489-493 行）与 B5（第 767 行）只写文档/测试/文案。

**影响**：按现文实施，行为几乎不变（opaque 429 本来就会 hold），用户要的“白嫖/限时账户被限流也继续用”无法兑现；实施者还可能各自选落点，产生分叉。

**建议的计划修改**：
- B5 拆成 B5.0 设计 + B5.1 实现。B5.0 必须新增两个裁决：
  1. 错误策略决策增加“确定性/transient”分类轴；锁死 ENGAGED 账户遇到 transient 时，不执行 `explicitPolicyFailure` 旁路，继续 hold。额度耗尽用 `decision.RuleID=system.upstream_insufficient_quota` / `last_error_code=system_quota_*` 识别为确定性，释放。
  2. 若继续 hold 需要账户在 `rate_limited`/`temporary_unavailable` 期间仍可被选中：要么不把锁定账户写成 non-active（只写冷却 fence，保持 active），要么增加“锁死 ENGAGED 账户的候选准入臂”。二选一写进计划，不能留给实施者。
- 探针失败面也要裁决：J1 探活失败当前会把 active 账户写 `temporary_unavailable`（`backend-go/projects/jobs/internal/accounthealth/projection.go:980-984,1218`），锁死账户会提前退出候选。建议锁定 ENGAGED 账户的 J1 失败只记录健康事实、不写状态，或同样走候选准入臂。
- 裸 401 当前没有“确定性释放”分支：`chain_ports.go:727-741` 无显式策略时走 opaque→锁死记账→hold 到死期。计划把“凭据无效”列为立即释放，要么接受 300s 死期兜底，要么单列 401 确定性分类改造。

---

## 2. Major（应改，否则会带病上线）

| ID | 问题 | 证据 | 建议的计划修改 | 复核 |
| --- | --- | --- | --- | --- |
| M1 | B8.1 对“audio 账户含 realtime_session 且还有其他 audio mode”直接移除该 mode，会切断现有 Realtime 流量；只报告受影响账户，不建替代账户 | 候选过滤 `chain_realtime.go:955-958`；无候选 503 `:475-481`；模式存于 credentials JSON `accountscore/capabilityallowed.go:23` | 混合账户**克隆出同凭据的 realtime 场景账户**，或把 B8 上线门禁改为“存在混合账户则阻止发布并输出人工拆分清单”；补迁移备份与反向恢复 | ✅ |
| M2 | §5.2/§5.3 至少 4 个 preset 的请求形态写错：qwen 图像不是 OpenAI images_json；volcengine 长音频是 POST query + 响应头状态，属 openspeech 不是 ark；gemini TTS 走 generateContent；minimax TTS 是 `/t2a_v2` | `gatewaymedia/qwen_image.go:1-4,115-123`；`volcengine_asr.go:105-159`；`gemini_tts.go:3-6,136`；`minimax_tts.go:57-58,115`；`shared/contracts/connector_registry.go:230-242,288-295` | 按 vendor 修正 §5.2/§5.3 表；明确“以 per-preset 构造器为准，不按 OpenAI 模板实现”；B0 回填必须逐项写真实路径/方法/错误码 | ✅ |
| M3 | “2xx/400/404/422 = Key 有效、401/403 = 凭据无效”的纯状态码判定不可靠：404 可能是路由不存在/WAF；volcengine 403/45000030 是缺 resource_id，不是 Key 无效；400/45001116 反而是已过鉴权的参数错误 | `accountprobe/probe_connectivity.go:48-53,50,137-139,151-155`；`docs/bug/问题-0358-火山语音连通性探针路径凭据与请求体三处失配.md:17-28` | §5.4 改为 per-vendor“状态码 + 错误码/错误体”白名单；B0 为每个组合回填“鉴权后拒绝”的样本；未回填组合不开自动探测（只人工） | ✅ |
| M4 | Realtime “101 + session.created”判据无仓库证据：网关只拨号透传，不消费 `session.created`；未发 `OpenAI-Beta`；空握手是否计费未验证 | `chain_realtime.go:16-18,320,539-541`；`docs/functions/实时语音Realtime网关设计.md:30`；`docs/functions/流式语音跨协议转换设计.md:40` | B0 先做一次真实 OpenAI 握手取证（事件序列、Beta 头、空连接计费），再定判据；未取证前 B8 只开人工测试，不开后台探测 | ✅ |
| M5 | 计划内部矛盾：§6.2 说视频冷却准入臂“保留并复核”，B4 说“复核并移除”；且该臂是视频账户在 `pending_test`/`rate_limited`/`temporary_unavailable` 下被真实请求选中并 CAS 恢复的唯一通道 | 计划第 454 行 vs 第 756 行；`chain_accounts.go:345-363`；J1 媒体白名单 `direct_input_reader.go:655,657`；CAS `chain_media_video.go:1681` | 先裁决“保留/移除”；若移除，必须与“该 preset×mode 已有可执行 J1 探针”逐项绑定、同一发布原子落地；`pending_test` 是否保留准入也要明确（建议不保留，与 D6 一致） | ✅ |
| M6 | B1.1 写入口清单漏了批量编辑、OAuth 托管建户/克隆、授权实例置备（直接 SQL 复制域）、maintenance 旧库导入、mock 数据；读侧统计/健康/性能/provider counts 也依赖同一四值词表 | `accountstransfer/batch.go:800-810`；`oauthmgmt/store.go:612-616`；`authz/instance_provision.go:211,246-255`；`legacybusinessimport/accounts.go:119,223`；`statreads/domainfilter.go:20`；`provider_counts.go:75` | B1.1 补全写入口清单；新增裁决“授权实例/团队授权对新场景是否开放、`status='active'` 直建语义是否保留”；读侧词表与前端筛选项必须同批扩展 | ✅（攻击取证） |
| M7 | `embed_content` 当前在 chat 的 13 值命中集内，`realtime_session` 在 audio 命中集内；拆 embedding/realtime 场景必须同步改 `DeriveDomain`/`KnownModes`/导入推导测试，否则新账户会被推导回 chat/audio | `shared/platform/capabilitydomain/capabilitydomain.go:51-73,90-110`；`accountscore/capabilitydomain.go:21-36`；`accountCapabilityDomains.ts:179` | B7.1/B8.1 写明：移动命中集成员、同步 `KnownModes` 与导入测试、沿用 v8-A 先例**不 bump RuleVersion** | ✅（攻击取证） |
| M8 | 单页化只写了路由/筛选 UI，未覆盖列表数据链路：`useAccountListData` 以 route meta 域为唯一来源，恒传具体 domain；页状态缓存 key 在挂载时固化；后端读侧对非法 domain 返回 400，不是“全部” | `useAccountListData.ts:38-44,68-71,486-502`；`AccountsView.vue:406-413`；`accounts/list.go:115-122`；`statreads/domainfilter.go:20-29` | B1.2 补：`AccountFilters` 增 scenario、`domain` 支持空=全部、页状态缓存 key/版本迁移、切场景重载语义；显式列出 `accountscore.AccountCapabilityDomainSet`、共享 `capabilitydomain.Domain.Valid`、statreads 校验的同批扩展 | ✅（部分修正：后端是 400，不是静默全部） |
| M9 | 批量编辑只写“按场景字段清单开放”，未定义单页跨场景多选；前端门控是路由级 `!isChatDomain`，后端有“批量同域”整批拒绝，混选提交必 400 | `AccountsView.vue:961-977`；`accountstransfer/batch.go:480-483,800-813` | B5 补：按选择集判定（全部同场景且 ≥2 才可编辑，混选禁用并显示原因）；批量弹窗字段=所选场景字段清单/交集；后端同域校验保留为兜底 | ✅ |
| M10 | 导入/导出未覆盖：`AccountImportModal` 以页面域为 prop，导出载荷携带 domain；单页后无路由域来源 | `AccountsView.vue:106-109,985-994`；`AccountImportModal.vue:220-224,238`；`useAccountExportActions.ts:22-23,67` | B1.2 补：导入弹窗增加场景选择（或按包内行域导入）；导出携带当前场景筛选；单页兜底不得默认 chat | ✅ |
| M11 | capability-catalog 只写管理面 `GET /accounts/capability-catalog`，又说“个人面共用、登录即可读”；accounts 包 `/accounts` 前缀全 RequireAdmin，个人面无落点，按字面实施会 403 | `accounts/routes.go:44-71,90-109`；`providers/routes.go:43-47` | B1.1 明确双面挂载：`/accounts/capability-catalog`（admin）+ `/my-accounts/capability-catalog`（session read），或仿 providers 的 session 级只读路由；补“非管理面过滤 disabled 供应商” | ✅ |
| M12 | 计划“删除 8 条域路由”漏掉两条旧反向 redirect：`/accounts → /accounts/chat`、`/my-accounts → /my-accounts/chat`，且注册在 menuRoutes 之前；不删会遮蔽新路由或形成循环 | `frontend/src/router/index.ts:775-778`；个人面默认入口 `useMenuMode.ts:73-75` | B1.2 删除清单改为“8 条域路由 + 2 条根路径 redirect”；redirect 用函数形式并透传其余 query；措辞改“前端 redirect”，不要写 301 | ✅ |
| M13 | 文档同步清单漏掉 `核心功能设计.md` 与前端《样式规范》§8.1；按项目规则，改菜单/路由/裁剪契约后这两处立即过时 | `docs/functions/核心功能设计.md:168`；`docs/architecture/frontend/样式规范.md:225,244-246` | §16/B6 增补这两处，并核对 `docs/architecture/架构总览.md` 的菜单口径 | ✅（攻击取证） |
| M14 | 创建流程“先选场景”后的依赖重置未定义：场景当前由路由注入且不可编辑；改选场景需重置 provider/profile/type/models 预取/OAuth/草稿测试载荷，计划 §9 未列 `useAccountEditForm.ts`、`accountSavePayload.ts`、`accountMediaCreateDefaults.ts` | `accountFormTypes.ts:85-89`；`useAccountEditForm.ts:94,190-208,384-393,421-423,717`；`accountSavePayload.ts:413,471` | B1.2 定义“切场景=重置 provider/profile/type/models/凭据派生默认值，并重跑该场景目录预取”；§9 补上述文件 | ✅（攻击取证） |

---

## 3. Minor（可在修订时一并处理）

| ID | 问题 | 证据 | 建议 |
| --- | --- | --- | --- |
| m1 | 列设置 key 语义未定：§8.4 说全场景同列，B2 说 key 加场景；按场景分 key 会在同一页面切换筛选时重置/分叉 | `AccountsView.vue:888-896`；`tableColumnSettings.ts:24-58`；计划第 637 行 vs 第 715/727 行 | 改为单一 key + 一次性迁移；如确要分 key，先裁决“切筛选是否重置列设置” |
| m2 | 新单菜单项没有图标 key：`menuIconMap` 有 8 个旧路径项，但没有 `/accounts`、`/my-accounts` | `AppLayout.vue:226-292,314-315` | §9 补 AppLayout：删除旧 8 项，增加两个新路径图标 |
| m3 | 移动端 `AccountMobileCard` 仍按 `capabilityDomainIsMedia` 裁剪时间计划/优先级，B2 只写桌面列 | `AccountMobileCard.vue:22,71,184-185` | §9 补该文件；同批核对 `accountStatusPresentation.ts` |
| m4 | capability-catalog 缓存“一次”未定义失效/版本；非管理面是否过滤 disabled 供应商未写 | `providers/routes.go:160-174` | §8.2 补：非管理面过滤 disabled；页面进入/会话刷新，带版本或 ETag |
| m5 | `pending_test` 期间允许设置锁死，但账户不在候选 `statusSet`，表现为“锁死静默无效” | `accounts/lock.go:137-141`；`chain_accounts.go:728-731` | §6.2 增补：pending_test 允许设置但提示“探针通过后生效”，或禁止设置 |
| m6 | xai 复用 OpenAI 词表（含 `audio_speech`/`audio_job_*`），但 §5.3 把 xai 音频标成“—”；若目录有 xai 音频模型会无探针 | `shared/contracts/connector_registry.go:262-268` | B0 核对 xai 目录行；有则补构造器，无则显式“不支持自动探测” |
| m7 | `audio_job_create` 写侧白名单“移除或降级为仅记录”未定；存量行需要回填 | 计划第 187/666 行；`direct_input_reader.go:655,657` | 明确：从可选模式移除，DB 值保留兼容，B4 回填存量到 `audio_job_get`/`audio_speech` |
| m8 | 前端回归脚本含源码正则断言，B2 删除 image-only 条件/媒体排除键后这些脚本必失败，§12 未列更新 | `frontend/src/scripts/regression/account-background-probe-toggle-regression.ts:250,260`；`account-provider-card-picker-regression.ts:130` | §12/B2 补“更新 `frontend/src/scripts/regression/*` 的域门控断言” |
| m9 | “草稿仍要求至少一个支持模型”与“只测 Key + Base URL”矛盾；video/audio_job GET 探测不需要模型，images/audio_speech 需要 | 计划 §7.1/§7.2；`probe.go:1190-1208`；`probe_connectivity.go:150-158` | §5.3 为每个构造器标注“是否需要 model”；video/audio_job 允许无模型账户；测试弹窗不要强制选模型 |
| m10 | B7.3 说“重排契约未定前不实施重排”，B7 验收又要求 `/v1/rerank` 可用；没有契约裁决点 | 计划第 807/834 行 | 增加 B7.0 契约裁决：Cohere/Jina 兼容形态经用户确认后才实施 rerank；未确认则 B7 只交付 embedding/moderation |
| m11 | `capability_domain` 读侧词表（`AccountCapabilityDomainSet`、`capabilitydomain.Domain.Valid`、statreads）与写侧注册表可能不同步；新场景账户会被 400 或不可见 | `accountscore/capabilitydomain.go:21-36`；`statreads/domainfilter.go:20-29` | B1.1 写明“注册表是单一来源，读侧词表由注册表派生/同批扩展”，并加一致性测试 |
| m12 | 子批次依赖未写清：B8 依赖 B4 的 J1 WS 探测；B7 依赖 B1 注册表与 B4 生命周期；B5 依赖 B4 的冷却 fence | 计划 §11 | 在批次表加“前置批次/可并行性/发布单元”，避免 B8 在 B4 未完成时先迁移账户 |

---

## 4. 计划内部矛盾（必须在修订时消解）

| 位置 | 矛盾 | 建议 |
| --- | --- | --- |
| §6.2 第 5 条 vs B4 | 视频冷却准入臂“保留并复核” vs “复核并移除” | 二选一，写进 B4 验收与回滚 |
| §7.1 vs §7.2 | “只测 Key + Base URL” vs “草稿仍要求至少一个支持模型” | 按构造器标注是否需要 model；不需要的允许空 |
| §8.2 vs §10.2 | “管理与个人面共用、登录即可读” vs 只定义管理面 `GET /accounts/...` | 明确双面路由 |
| B7.3 vs B7 验收 | “重排契约未定前不实施” vs 验收要求 `/v1/rerank` | 增加 B7.0 契约裁决，或把 rerank 移出 B7 首批 |
| §15.5 vs B8.1 | “现有 Realtime 会话链零回归” vs 混合 audio 账户移除 realtime_session 会 503 | 改为“迁移后零回归”，并定义克隆/人工拆分补偿 |
| §3.5/§10.3 | “删 CHECK、未来不 DDL” vs “回滚换旧二进制即可” | 按 B1 推荐 A/B 方案修订 |
| §8.4 vs B2 | “全场景同列” vs “columnStorageKey 加场景” | 明确列集合与用户可见性设置是两个层；推荐单一 key |

---

## 5. 建议的修订顺序

1. **先定 B1 的 schema 策略**（推荐保留并扩展 CHECK 到八值 + 写侧注册表校验；若坚持无 DDL，改两阶段发布并补回滚 runbook）。同步补 maintenance 三处落点与 `RuleVersion` 约束。
2. **修 B4 的执行引擎**：J1 媒体/工具探测改走连通性构造器；补 `exactkeyprobe` 的 `connectionPresetProfiles`/`isSupportedDirectProfile`/`upstreamenvelope`；加“不真实生成”负断言。
3. **重写 B5/D12 的范围**：错误策略确定性/transient 分类、锁死优先级、non-active 候选准入、J1 失败不终结 hold、额度耗尽按 RuleID 释放；先改锁死设计文档，再改代码。
4. **修 B0 协议矩阵**：按 M2/M3 修正 vendor 请求形态与错误码分类；补生产数据审计（混合 audio/realtime 账户数量）。
5. **补前端单页化落点**：双面 catalog、列表数据链路、批量混选、导入/导出、旧 redirect、列设置、AppLayout、移动端、回归脚本、文档。
6. **拆 B7/B8 发布单元**：B7 先定 rerank 契约；B8 先做 Realtime 真实握手取证与混合账户迁移方案；不要把 B7/B8 与 B1-B6 放在同一发布窗口。

---

## 6. 需要用户裁决的 C 类事项

| ID | 事项 | 推荐 |
| --- | --- | --- |
| C1 | `capability_domain` 是保留并扩展 CHECK，还是两阶段去 CHECK | 推荐保留并扩展八值：回滚安全、旧二进制兼容；未来场景多了再做去 CHECK 专项 |
| C2 | D12 的锁死优先级：显式错误策略的 transient 是否也 hold；锁定账户 non-active 期间是否保留候选准入 | 推荐 transient hold（额度耗尽按 RuleID 释放）；锁定账户用“冷却 fence 保持 active”或专门准入臂，二选一写死 |
| C3 | 混合 audio+realtime 存量账户：克隆 realtime 账户，还是阻止 B8 发布并要求人工拆分 | 推荐克隆同凭据 realtime 账户；至少上线前生产审计，存在混合账户则阻断发布 |
| C4 | Realtime 探针是否在 B0 真实握手取证前就纳入后台周期 | 推荐先只做人工测试；取证通过后再开后台，避免“等一个不会来的 session.created” |
| C5 | `/v1/rerank` 契约是否采用 Cohere/Jina 兼容形态 | 推荐是；但必须用户确认后才实施，未确认则 B7 首批只做 embedding/moderation |
| C6 | B7/B8 是否与 B1-B6 同窗口发布 | 推荐分开：B1-B6 先交付并观察，B7/B8 各自独立发布与回滚 |

---

## 7. 覆盖与复核记录

### 7.1 覆盖矩阵（第 1 轮）

| 维度 \ 列 | 场景模型/迁移 | 探针（媒体/工具/实时） | 生命周期/锁死 | 单菜单/前端 | 工具场景 | 验收/回滚 |
| --- | --- | --- | --- | --- | --- | --- |
| 完整性 | ✅ 发现 B1/M6/M7 | ✅ 发现 B2/M2/M3/M4 | ✅ 发现 B3/M5 | ✅ 发现 M8-M12/M14 | ✅ 发现 M7/m11 | ✅ 发现 B1/M5 |
| 契约一致性 | ✅ B1/M7 | ✅ M2/M3/M4 | ✅ B3 | ✅ M8-M12 | ✅ M7 | ✅ 内部矛盾表 |
| 可实施性 | ✅ B1 | ✅ B2/M2 | ✅ B3 | ✅ M8-M14 | ✅ B7 拆分 | ✅ 修订顺序 |
| 安全性/副作用 | n/a（文档层） | ✅ B2/M2/M3 | ✅ B3 | n/a | ✅ B2 | ✅ 回滚 |
| 迁移/回滚 | ✅ B1/M1 | ✅ B2 | ✅ B3/M5 | ✅ M12/M13 | ✅ M7 | ✅ B1/C1 |
| 测试有效性 | ✅ B1 测试点 | ✅ B2 负断言 | ✅ B3 测试点 | ✅ m8 | ✅ B7.4 | ✅ §12 缺口 |

### 7.2 复核统计

- 攻击视角：4 个（迁移、探针、锁死、前端）。
- 证伪角色：4 个；对 3 个 blocker + 8 个 major 做了独立复核。
- 判定：高严重度结论均经独立证伪；其中 1 条子句被证伪（“后端对非法 domain 静默归一为不过滤”不成立，实际返回 400；store 层兜底不可外部触达），其余按 CONFIRMED/PARTIAL 分别吸收；B-F2 的“45000030 是 400”子句也被证伪（实测 403），但“纯状态码分类不可靠”的核心结论成立。
- 未验证：生产/开发库中混合 audio+realtime 账户数量；各厂商真实计费；OpenAI Realtime 空握手事件序列/计费/Beta 头；未运行任何测试、类型检查、构建或前端浏览器用例。
- 本轮未改代码、未改计划决策；本文件是复审报告，计划状态应改为“待修订”。

---

## 8. 终报

- 终止原因：report-only 复审完成，未声称收敛（计划存在 blocker，需修订后复审）。
- 扫描范围：计划全文 + 迁移/探针/锁死/前端四个切片 + 相关权威文档。
- 处置统计：3 blocker、14 major、12 minor、7 处内部矛盾；建议即修 3 项（B1/B2/B3），建议过目 14 项，裁决清单 6 项（C1-C6）。
- 修复与验证证据：无代码修复；证据均指向实际文件行号。
- 移交清单：C1-C6 需用户裁决；B1/B2/B3 需在计划中改写；M1-M14 需在计划中补落点；测试缺口见 §7.2。
- 未提交改动：本文件为新增复审报告；计划文件仍为未跟踪状态；未触碰任何代码。
