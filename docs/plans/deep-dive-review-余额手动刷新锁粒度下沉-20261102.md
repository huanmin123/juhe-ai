# 深挖复审台账:余额手动刷新锁粒度下沉

- 锚定范围:生产 409 根治变更集——owner lease key 参数化 + 手动刷新按账户独立租约 + 账户租约接管条件收窄(shared/platform/accountbalance 8 文件 + gateway cmd compose 2 文件 + docs 2 文件,+488/−149,当前工作区未提交)。
- 发起:2026-11-02,用户指令「复查复查」;授权:深挖环标准授权,含 A 类即修权(A=内部正确性/文档/测试/回归,合法用户无感知);B 类(合法用户可感知且无契约依据)过目后修;C 类(功能缺口/取舍)只登记裁决。
- 需求脉络:目标=手动余额刷新互斥从全局 owner lease 下沉为按账户租约,不同账户/用户并行,同账户才 409;非目标=不改前端/409 文案/路由/周期调度参数,jobs 周期与首探锁语义不变,无 DDL;可见行为=409 只在同账户被占时出现。
- 涉及文件:backend-go/shared/platform/accountbalance/{store.go,runner.go,manual_bridge_test.go,store_runner_test.go,accountbalance_pgkit_test.go,business_due_test.go,postgres_smoke_test.go,w7c_pg_gate_test.go};backend-go/projects/gateway/cmd/juhe-ai-gateway/{compose_account_balance_refresh.go,compose_account_balance_refresh_test.go};docs/functions/AI账户上游余额查询设计.md;docs/待办与阻塞事项.md(T95 条目)。
- 关联契约:docs/functions/AI账户上游余额查询设计.md §5「J2 租约互斥模型」+ §6 手动刷新节;m11_routes.go 409 映射(stale=「配置已变化」/lease_busy=「余额查询正在进行」)。
- 项目裁决出处:根 AGENTS.md(文档先行/最小改动/Mock 优先);orchestrate 流程已完成一轮 Critique(glm_5.3_high,7 项通过、3 条建议级修正已落,含接管动机表述纠正与防回潮钉子用例)。
- 代码基线:master 工作区未提交(基线 HEAD=f880690a9 附近);本变更集 10 文件。
- 工作区边界:并行会话占用 chain_compose.go/chain_test.go/chain_v1_loop.go/patch*.go/t9*.go 等(T91/T93/T94),不得触碰;docs/待办与阻塞事项.md 为共享文件,只追加不重写他人条目。
- 当前阶段 / 当前轮次:R1 收口(用户裁决提前收口,收敛协议未走满三轮)
- 收敛计数:R1 含 B 类确认(V10/V5),不计入干净轮;干净轮 0/3(未继续)

## R1 修复记录

| 批 | 内容 | 验证证据 | 落点复核 |
| --- | --- | --- | --- |
| R1-A | V1 日志/V2a 注释/V8 注释/V3+V4 文档归因/V13 测试超时(5 项 A 类) | shared 全包真 PG 全绿 19.2s;五模块 BUILD_OK;被改用例 0.31s PASS | 落点复核通过(glm_5.3_high 独立实例:逐项 diff 相符、§5/§6 自洽无旧归因残留、defer 语义未变、超时余量 >30 倍、无清单外混入) |
| R1-B(用户批) | V10:runner dbWorker/ioWorker safego.Handle 化(panic→recordError→Errors→500 正确分级;dbWorker 回调补释放账户租约)+ 注入 panic Doer 测试 TestRunManualRecordsErrorWhenProbePanics;V5:docker/single-server/README.md 新增 gateway/jobs 同批发布/回滚约束段(纪律路线,脚本不改) | 全包真 PG 全绿 22.8s;五模块 BUILD_OK;定向用例 0.34s PASS(栈迹证实 panic 经 ioWorker Handle 吸收);README 条目落点/表述核对通过;deploy.sh diff 为空 | 落点复核通过(glm_5.3_high:continue→return 闭包等价精确、无跨轮变量捕获、回调参数与正常路径一致、双 Release 幂等无害、断言组合钉住 Errors 分支) |
| R1-B 收尾(主代理) | 复核建议级两条:runner.go:291 注释「互斥执行」改「通常尚未执行+二次释放幂等无害」;README:133 示例行改「单服务发布仅限 maintenance」消歧 | gofmt 干净、BUILD_OK、定向用例 1.9s ok | 措辞级,无需再复核 |

dbWorker panic 路径测试缺口(已知):RunnerConfig.Store 为具体类型 *Store,panic 注入需真实 Store 内部故障,现有形态不支持 Mock;该路径 Handle 化结构与 ioWorker 同形,未经自动化钉住,登记测试缺口。

## 终报(R1 收口)

- 终止原因:用户裁决提前收口(「复查复查」一轮深挖完成后 B/C 类呈报;收敛三轮协议未走满,如实说明)。
- 扫描:R1 维度制视角,攻击批 5 角色(glm_5.3_flash_high×4 + glm_5.3_high×1)并行,攻击点 7×5=35 个方向,产出假设 40+;去重 16 条进验证批(glm_5.3_max 独立档),confirmed 9 / refuted 1(V9)/ undetermined 0;V12 定性既有未放大;14 条在攻击轮内即被证伪(锁顺序/写锁覆盖/fence 原子性/释放 ctx/多 Key 预算/CheckSchema 兼容/回滚安全/零 DDL/恢复自愈等,全部有 file:line 反证)。
- 处置:A 类即修 5 项(V1/V2a/V8/V3+V4/V13)已落并通过落点复核;B 类 2 项(V10 panic 误报、V5 部署同源门禁)呈报用户;C 类 7 项(C1-C7)登记裁决清单;证伪存档 14 条见前文。
- 核心结论:变更集的并发正确性(锁顺序、fence 闭环、接管语义、跨 key 互斥)经对抗攻击未发现缺陷;确认项集中在注释/文档失真、一处可观测性缺口、一处测试健壮性,均已修;唯一 refuted 的重假设(版本回退永久 stale)有全仓 CAS 单调性反证。
- 移交:B 类 2 项裁决;C1(jobs manual 端点退役)、C2(部署同源门禁)、C3(孤儿行 retention)、C4(500 日志维度+runbook 排查口径)、C5(env 契约)、C6(deploy.sh ensure-schema 提示矛盾,既有)、C7(3d3ab1e32 提交信息未提锁语义变更)。
- R1 验证批统计:confirmed 9 / refuted 1(V9:dispatch_revision 全仓 CAS +1 单调,回退需库外人工手术,机制属使用契约)/ undetermined 0;V12 定性既有未放大。

### R1 A 类即修清单(验证批建议,全部为注释/文档/测试/一行日志)

| ID | 修复内容 | 钉住方式 |
| --- | --- | --- |
| V1 | runner.go:237 ReleaseOwnerLease 失败补 Debug 日志(对齐 :328-331 账户租约形态) | 落点复核轮核点 |
| V2a | JB main.go:840-842 注释回正(端点保留无调用方,全局 key 行为说明) | 落点复核轮核点 |
| V8 | compose:135-136 过时「全局单行」注释改双 key 空间表述 | 落点复核轮核点 |
| V3+V4 | 设计文档 §5:258 机制归因修正(同账户双 manual=manual owner 行互斥,跨 key=账户租约)+下沉三条件补写;§6:280 同步 | 落点复核轮核点 |
| V13 | manual_bridge_test.go gate 接收改 select+timeout | 测试运行通过 |

### R1 B 类过目清单(用户 2026-11-02 裁决:按建议执行)

| ID | 一句话 | 裁决与处置 |
| --- | --- | --- |
| V10 | dbWorker/ioWorker panic 被 safego.Recover 吞且未 recordError→用户收到 409「正在进行」误报(真实=内部 panic)+账户租约滞留 30s | **已批修**:safego.Handle 化(panic→recordError→Errors→500 正确分级;dbWorker panic 回调补释放账户租约);ioWorker 路径注入 panic Doer 测试钉住;dbWorker 路径复核轮核点 |
| V5 | deploy.sh 单服务发布/回滚无 gateway/jobs 同源校验→混跑窗口 manual 被旧 jobs 抢占 500;all 同批窗口仅秒级 | **按纪律约束**:docker/single-server/README.md 更新发布节加「gateway/jobs 必须同批发布(./deploy.sh all),禁止单服务发布/回滚其中之一」;脚本级校验不在本次(C2 留裁决) |

### R1 C 类裁决清单(持续积累)

| ID | 一句话 | 来源轮次 | 状态 |
| --- | --- | --- | --- |
| C1 | jobs /account-balance/manual 端点本体退役(无生产调用方,涉 7 测试文件联动) | R1/V2 | 待裁决 |
| C2 | deploy.sh gateway/jobs 同源一致性门禁(若不采纳 B-V5 脚本化,则纯纪律) | R1/V5 | 待裁决 |
| C3 | 已删除账户的 owner/lease 孤儿行 retention(有界:每账户 ≤2 行,无清理路径;outcomes/snapshots 本就无 retention 属既有) | R1/V6 | 待裁决 |
| C4 | m11 err 路径 500 日志缺 lease key/account 维度;runbook 补「手动刷新 409/失败」排查口径 | R1/V7 | 待裁决 |
| C5 | 自定义 env OwnerLease<AccountLease 的接管时点语义(env 契约约束说明) | R1/V15 | 待裁决 |
| C6 | deploy.sh:438 「发布后跑 --ensure-schema」提示与 README「ensure 含破坏性 DROP TABLE 须停写窗口」矛盾(既有,与本次无关) | R1/V17 | 待裁决 |
| C7 | 3d3ab1e32 提交信息未提及余额锁语义变更(用户本人提交),追溯依赖设计文档 §5 | R1/A4-H8 | 已知悉(移交说明) |
- 基线更新:用户本人 2026-11-02 00:13 将全部并行任务改动(含本变更集)提交为 3d3ab1e32(feat(bridge),提交信息未提及余额锁语义变更);攻击轮以该提交为基线。注意 A4-H8:接管语义删除混入大提交、信息零提及,追溯性依赖设计文档 §5。

## 问题条目表(活跃条目)

| ID | 来源 | 假设(一句话) | 关键依据 | 状态 |
| --- | --- | --- | --- | --- |
| V1 | A1-H7/A2-H3 交叉 | owner lease 释放失败 `_ =` 静默吞错无日志(账户租约释放有 Debug 日志,规格不对称),PG 抖动时同 key manual 409 最长 1min 且无痕迹 | runner.go:237 vs 328-331 | 待验证 |
| V2 | A1-H1/A4-H4 交叉 | jobs `/account-balance/manual` 端点仍存活(无 prefix→旧全局 key 症状旁路),且 main.go:840-842 注释声称手动桥已删除与在册 handler 矛盾(既有,非本次引入) | JB\main.go:861,889 + service.go:94 | 待验证→注释 A 类;端点退役 C 类 |
| V3 | A3-H2 | 文档 §5 称同账户双 manual 并发「由账户租约互斥」归因失真:实际同 key 空间 manual owner 行即拒,到不了账户租约(行为结果一致,归因误导排障) | runner.go:225-235 + store.go:337-348 + doc:258 | 待验证(A 类文档) |
| V4 | A3-H7 | 文档 §5 省略下沉条件「TriggerManual 且 prefix 非空且恰单 input」,未来多 input manual 会静默回退全局 key 且文档未覆盖 | runner.go:219-228 + doc:258 | 待验证(A 类文档) |
| V5 | A5-H1/H1b | 新 gateway+旧 jobs 组合(deploy.sh gateway 单发/jobs 单独回滚为intval一等用法)→manual 探测中被旧 jobs 立即抢锁→500 而非 409;deploy.sh 无版本一致性门禁(all 同批时仅秒级窗口,低概率) | deploy.sh:38,71,314-369,400 + store.go:395-410 | 待验证(C 类运维义务) |
| V6 | A5-H3/A4-H2 交叉 | manual owner 行/账户租约行随账户数永久残留无 retention(outcomes/snapshots 本就无 retention 属既有) | store.go:337-373 + 无 DELETE 全仓 | C 类取舍登记 |
| V7 | A5-H4 | runbook 无「手动刷新 409/失败」排查口径;m11 err 路径 500 日志缺 lease key/account 维度 | runbook 发布节 + m11_routes.go:873-880 | 文档同步 A 类(私有 runbook);日志维度 C 类 |
| V8 | A4-H3 | compose_account_balance_refresh.go:135-136 注释仍称「owner lease 行全局单行」未随下沉更新 | compose:135-136 | 待验证(A 类注释) |
| V9 | A1-H5 猜测 | dispatch_revision/config_revision 回退(删除重建复用 ID/数据回填)后该账户 manual 恒 stale、快照永不更新(CAS currentInput>inputVersion 拒绝+committed=FALSE 残留) | store.go:666,603-605,578-589 | 待验证(优先) |
| V10 | A2-H12 猜测 | dbWorker panic 被 safego.Recover 吞且未 recordError→Executed==0 无 Errors→409「正在进行」误报;account lease 滞留 30s | runner.go:272-293 | 待验证 |
| V11 | A3-H1b | manual 的 stale 经 CAS 臂可达(探针窗口内账户被保存推进代次)、request_id 冲突臂工程不可达;文档 §5 的 ErrOutcomeStale 澄清若声称 manual stale 走 request_id 臂则失真 | runner.go:405-433 + store.go:666-675,613-628 | 待验证(与 V3/V4 同点核对) |
| V12 | A2-H4/H8/H13 + A1-H3 | 取消/PG 慢场景:已探测结果因 ctx 取消或 input 20s 过期被丢弃→500(含 ERROR 噪音);cycleBudget 45s 到期排队任务整批丢弃——均为既有取消语义,非本修复引入,量级待定性 | runner.go:277-278,341-349,407-409 | 待验证(定性后登记观察) |
| V13 | A4-H5 | 测试 manualGateHTTP 的 <-gate.entered 无超时保护,隔离库故障时主 goroutine 永久阻塞 | manual_bridge_test.go | A 类测试补强 |
| V14 | A4-H6 | 持有者进程崩溃后 owner 行 1min TTL 存活期间该账户 manual 恒 409——与变化前等价,既定设计 | runner.go:229-236 | 已裁决接受(登记) |
| V15 | A4-H1 | 自定义 env OwnerLease<AccountLease 时接管时点变化(当前生产未配置,全默认) | runtime_config.go:114-117 | C 类(env 契约)登记 |
| V16 | A5 附带 | deploy.sh:438 提示「发布后跑 --ensure-schema」与 docker/single-server/README.md:30「ensure 含破坏性 DROP TABLE 须停写窗口」矛盾(既有) | deploy.sh:438 + README:30 | C 类登记移交 |

已证伪存档(一行,证据见攻击轮报告):锁顺序无反序无锁环(A1);writeMu 覆盖完整无绕过(A1);fence+1 单语句原子无丢失更新(A1);三处 Release 均 Background ctx 无滞留(A2);Errors 判定恒优先不吞成 409(A2);多 Key 共享 15s deadline 无 20s×N 场景(A2);TestDraft 与租约零交互(A2);CheckSchema 对 per-account 行不失败、旧二进制不读 manual 行(A5);回滚 gateway 旧版不受残留行影响(A5);零 DDL 逐 hunk 证实(A5);pg_dump 恢复后 manual 1min 内自愈(A5);jobs 判定含 prefix 非空,不存在裸 key(A4);多 Key 不拆 input 恒单 input(A4);candidate 资格谓词前后一致(A4);maintenance/runbook 无单行假设(A4);409 后前端无重试风暴(A3);权限/400/403/500 降级契约零变化(A3);acquire 中途取消不持锁(A2)。

## 覆盖矩阵

(待 R1 填充)

## 扩散记录

| 轮次 | 新纳入对象 | 耦合类型 | 理由 |
| --- | --- | --- | --- |

## 修复记录

| 批 | 内容 | 验证证据 | 落点复核轮次 |
| --- | --- | --- | --- |

(前置 Critique 批次,深挖环开始前已完成:3 条建议级修正[接管动机表述三处/T95 证据行/防回潮用例],验证=shared 全包真 PG 127 PASS/0 FAIL + 定向用例 PASS。)

## 裁决清单(C 类)

| ID | 一句话 | 来源轮次 | 状态 |
| --- | --- | --- | --- |
