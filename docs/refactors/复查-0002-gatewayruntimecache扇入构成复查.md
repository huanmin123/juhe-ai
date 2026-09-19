# 复查-0002 gatewayruntimecache 扇入构成复查

## 基本信息

- 编号：复查-0002（R2 波次 gatewayruntimecache 项验收证据）
- 状态：已完成（结论：**窄接口分组收益不足，维持现状、不拆包、零代码变更；"类型搬家语义子包 + 根包 alias"可选项已评估、不执行**）
- 创建时间：2026-09-18
- 关联计划：[PLAN-20260918T064119294Z](../plans/计划-20260918T064119294Z-后端架构与性能优化改革.md) 波次 R2（缓存与 preauth 扇入治理）
- 关联评估：[后端架构与性能现状评估（2026-09-18）](../reports/后端架构与性能现状评估-20260918.md) 2.5 节扇入表第 1 名（231 处引用）
- 参照框架：[复查-0001 gatewaypreauth 职责边界复查](复查-0001-gatewaypreauth职责边界复查.md)（完全同口径：类型词汇 / 接口 / 顶层函数 / 方法调用四分类与判定标准）；[REFACTOR-0005](重构-0005-accounts包子域拆分设计.md)（accountscore 模式，可选项对照来源）
- 复查对象：`backend-go/projects/gateway/internal/gatewayruntimecache`
- 复查方式：只读取证 + 本文档；未改动任何代码

## 1. 复查问题与事实纠正

R2 原始表述："`gatewayruntimecache`（231 处引用）按读快照/写失效/并发控制/SQL 模型拆分对外接口面（包内文件已分，重点是把'231 处都 import 整个 Service'收窄为按消费方语义的窄接口）"。

先纠正该表述中的两处口径偏差：

1. **"231 处"的 70.7% 是测试文件**。当前实测 import 本包的文件共 232 个（评估时点 231，+1 为当日新增测试文件的漂移），其中生产代码 68 文件、测试 164 文件。
2. **"都 import 整个 Service"不成立**。生产 68 个消费文件中，只有组合根 `cmd/juhe-ai-gateway` 的 19 个文件引用具体 `*gatewayruntimecache.Service` 类型（组合根接线职责）；其余 49 个文件消费的是类型词汇与端口接口。两个最大的行为消费方**已经各自定义窄端口接口**（依赖倒置已存在）：`gatewaypreauth.RuntimeCacheReader`（8 方法，`*Service` 直接实现）与 `gatewaydispatch.RuntimeCachePort`（1 方法，本地 `CachedAccountsOptions`/`AccountCandidate` 类型）。

## 2. 判定标准（复查取证前声明）

沿用复查-0001 声明的口径与 REFACTOR-0005 取证框架：

1. **若扇入主体是类型/常量词汇且行为方法调用点少** → "窄接口分组"收益不足，登记"复查通过、维持现状"，本文档即为 R2 该项验收证据；同时**附加评估**"被消费类型词汇按语义分组搬家到子包 + 根包 alias"方案（对照 accountscore 模式）是否值得，给收益/代价数字。
2. **若行为调用面大且存在清晰多职责块** → 产出窄接口分组设计（分组表、依赖方向、分阶段验收）。

## 3. 实测数字（2026-09-18 工作区）

### 3.1 体量与文件健康

- 生产 18 文件 / **5,111 生产行**（snapshot.go 死代码裁剪 -373 行后的现状）；测试 11 文件 / 5,649 行；合计 10,760 行。
- 生产 226 个 `func` 声明。
- 导出面（`go doc -all`）：导出类型 41（9 interface + 2 alias + 30 struct/命名类型）、顶层函数 16、`Service` 导出方法 19、`SQLReadModels` 导出方法 13、`Registry` 方法 5、`RedisSharedCache` 方法 3、值类型 `Clone` 访问器 8。
- 文件健康：最大 `service.go` 652 行，**全部 ≤3000 硬上限**（R2 该验收项达标），且无一接近 2000 预警线。

### 3.2 扇入口径复核

| 口径 | 数值 |
| --- | --- |
| 评估口径（import 路径字符串出现，含测试文件） | 当前 232 文件（评估时点 231） |
| 其中生产代码 | **68 文件 / 11 个消费包** |
| 其中测试文件 | 164 文件（占扇入数字 **70.7%**） |

生产消费包分布：`cmd/juhe-ai-gateway` 19 / `gatewaypreauth` 11 / `gatewaydispatch` 10 / `gatewaycodex` 7 / `gatewayaccounteffects` 5 / `gatewayproxyhealth` 4 / `gatewayclientip` 4 / `gatewayresponse` 3 / `gatewaysession` 2 / `gatewaycircuit` 2 / `gatewayhotquality` 1。

别名核查：全工作区无 import 别名、无匿名导入，`gatewayruntimecache.` 限定引用口径闭合。注释位置的限定引用共 23 行（适配器说明注释，如 `chainRoutingCache adapts *gatewayruntimecache.Service to ...`），下列 600 处为含注释口径，净口径约 577 处，各项占比不受影响。

### 3.3 消费内容分类（关键证据）

生产代码 `gatewayruntimecache.X` 限定引用共 **600 处 / 42 个符号**，分类：

| 类别 | 引用次数 | 占比 | 说明 |
| --- | --- | --- | --- |
| ① 类型词汇（struct/alias/命名类型，含 `Service` 类型名声明引用） | **563** | **93.8%** | 30 个符号被消费 |
| ③ 端口接口（依赖倒置声明） | 18 | 3.0% | 8 个接口 |
| 常量（枚举值） | 11 | 1.8% | 5 个常量 |
| ② 被调用的顶层函数 | 8 | 1.3% | 7 个符号（构造 4 + Clone 2 + SystemClock 1，另 1 处为注释） |

Top 符号：`OpenAIAccountSecret` **252 处（42.0%）**——SQL 行模型/账号凭证词汇；其后 `GatewayAPIKeyRow` 46、`GatewaySettings` 37、`GroupUsageAccessMetadata` 35、`GroupSchedulingPolicy` 33、`Service` 27（全部是 cmd 字段/参数声明与 1 处注释，非调用）、`ProviderModelCatalogItem` 20。**Top5 合计 403 处（67.2%）**，全部是类型词汇。

- ③ 端口接口消费主体：`CatalogSource` 3、`GroupBindingOrderer` 3、`SharedCacheFactory`/`SharedCache`/`Logger`/`ConcurrencySource`/`Clock`/`AccountsSelector` 各 2——均为依赖倒置声明（实现在包内或组合根接线），小消费包（clientip/proxyhealth/session/circuit/accounteffects）的引用全部属于此类。
- ② 顶层函数调用点 7 处（生产）：`New` / `NewSQLReadModels` / `NewRedisSharedCacheFactory`（`chain_runtime.go` 组合根）、`NewRegistry`（`main.go`）、`CloneGatewayAPIKeyRow`（`chain_preflight.go`）、`CloneGatewaySettings`（preauth `preflight.go`）、`SystemClock`（`gatewayclientip/cache.go`）。
- 导出顶层符号 74 个（类型 41 + 顶层函数 16 + 导出常量 17）中被包外消费的 42 个（56.8%）；其余 32 个（未消费类型 11、函数 9——含 4 个未消费 `Clone*`、4 个谓词/散列函数，常量 12——含 `RouteStrategyModeFailover`/`RoundRobin`、`AccountStatus*`/`AccountAccessType*`/`ProviderModelRoute*` 族）没有包外消费者。

### 3.4 包外方法调用点（限定引用统计不到，已逐接收者核实，共 **39 处**）

| 接收类型 | 调用点 | 分布 | 性质 |
| --- | --- | --- | --- |
| `*Service` 直连（组合根） | 22 | `cmd` 10 个文件：`chain_routing` 5 / `chain_chat` 3 / `chain_compose` 3 / `chain_dispatch` 3 / `chain_ports` 2 / `chain_pricing` 2 / `chain_driver` 1 / `chain_preflight` 1 / `chain_openaicompat` 1 / `compose_prewarm` 1 | 异步读族（ReadCached*/ListCached*/ResolveCached*）+ prewarm |
| `*SQLReadModels` setter | 4 | `cmd/chain_runtime.go`（`SetSettingsStore` / `SetAccountsSelector` / `SetCatalogSource` / `SetConcurrencySource`） | 组合根装配 |
| preauth `RuntimeCacheReader` 端口之后 | 12 | `gatewaypreauth` 3 个文件：`preflight.go` 8 / `preflighthelpers.go` 3 / `preauth.go` 1 | **已走消费方自有 8 方法窄接口**，签名引用本包词汇类型 |
| dispatch `RuntimeCachePort` 端口之后 | 1 | `gatewaydispatch/fallbackcandidate.go:122` | **已走消费方自有 1 方法窄接口**（本地 `CachedAccountsOptions`/`AccountCandidate`，零本包类型参与调用） |

- 排除项（接收者甄别）：`cmd/chain_dispatch.go:225` 的 `LoadAccountCurrentConcurrencyByID` 接收者是 `*gatewayclientip.MemoryAccountConcurrency`，非本包类型，不计入。
- **零包外调用面**：`Service` 19 个导出方法中 8 个零包外调用——含**全部写失效方法**（`ClearGatewayRuntimeCache` / `ClearGatewayRuntimeCacheLocal` / `InvalidateGatewayRuntimeCacheByAPIKeyID`）及 `AwaitBackgroundWork`、`Close`、同步族 `ListCachedOpenAIAccountsForGroup` / `ListCachedActiveResponseInspectionPoliciesAsync` / `ResolveCachedGroupUsageAccessMetadata`；`SQLReadModels` 9 个读方法零包外调用（经 `ReadModels` 接口注入后只在包内使用）；`Registry` 5 个方法与 `RedisSharedCache` 3 个方法零包外调用。
- 词汇 `Clone` 访问器包外调用仅 1 处（`cmd/chain_accounts_secret.go:219`，`AccountAPIKeyRuntimeSelectionState.Clone`；消费文件中另 3 处 `.Clone()` 是 `http.Header`，与本包无关）。
- 行为面合计：**方法调用 39 处 + 顶层函数 7 处 + Clone 访问器 1 处 = 47 处**。对照：gatewaypreauth 同口径行为调用约 59 处（含 5 处跨包编排入口）；本包 **39 处方法调用中没有任何一处是跨包编排入口**——读快照 API 的调用方是各消费包自己的流程，编排关系不经过本包。

## 4. 包内语义分组现状（R2 设想四个分组的核对）

| R2 设想分组 | 现状文件（行数） | 行数 | 包外暴露面 |
| --- | --- | --- | --- |
| 读快照 | runtime.go 431 / catalog.go 291 / groupaccess.go 220 / accounts.go 253 / inspection.go 196 / settings.go 87 + types.go 主体 642 | ≈2,120 | `ReadCached*`/`ListCached*`/`ResolveCached*` 异步族（39 处调用点的 34 处）+ 类型词汇主体 |
| 写失效 | invalidate.go 143 / cache.go 190（失效主题订阅） | 333 | `Clear*` / `Invalidate*`——**包外调用 0** |
| 并发控制 | concurrency.go 124 | 124 | 仅 `AwaitBackgroundWork`——**包外调用 0**，其余全部非导出 |
| SQL 模型 | sqlmodels.go 449 / sqlruntime.go 469 / sqlinspection.go 291 / sqlprewarm.go 70 | 1,279 | `NewSQLReadModels` + 4 个 setter（组合根装配），读方法经 `ReadModels` 接口只在包内被消费 |
| （评估未列）实例注册与共享缓存 | registry.go 415 / shared.go 115 / prewarm.go 73 / sqlprewarm.go 70 | 673 | `NewRegistry` 1 处、`NewRedisSharedCacheFactory` 1 处、`SharedCacheFactory`/`SharedCache` 接口类型（clientip + cmd） |

复核发现：

1. **R2 说"包内文件已分"属实**：四个设想分组在文件级边界已经存在且干净（写失效 333 行、并发 124 行都是自含小块），不存在混合文件需要先拆的问题。
2. **写失效与并发两个分组的包外行为消费为零**：把它们的"接口面"单独分组收窄，没有任何消费文件可以减少 import——无对象可收窄。
3. **读快照与 SQL 模型在词汇上不可分**：读快照 API 的入参/返回值就是 SQL 行模型本身（`OpenAIAccountSecret` 既是 sqlmodels 侧的行模型，又是流经 `ListCachedOpenAIAccountsForGroupAsync` 的热词汇，占全部限定引用 42.0%）。按语义把它们拆到两个包，等于把同一词汇链强行跨包化。
4. 包内最大逻辑块是 `service.go` 的构造与失效订阅接线（652 行），低于一切预警线，不构成 god 包。

## 5. 判定：窄接口分组收益不足，维持现状

事前判定标准第 1 条成立：

1. **扇入主体是类型词汇（93.8%）**，行为方法调用点 39 处且无一为跨包编排入口；`OpenAIAccountSecret` 单体占 42.0%，是典型的"共享词汇中心 + 读缓存端口"形态，与 REFACTOR-0005 取证时的 accounts（`accounts.Credentials`×36 等）和复查-0001 的 preauth（73.6% 词汇）同构。
2. **反事实测算（关键证据，窄接口能减少的 import 依赖文件数）**：逐文件核验，**68 个生产消费文件全部含至少 1 行 `gatewayruntimecache.X` 类型词汇引用**（最小的 11 个文件也各有 1 行，如 `preauth/types.go`、`dispatch/preparation.go`、`cmd/main.go`）。Go 的 import 耦合按文件不按符号——无论把接口面拆成多少个语义窄接口包，**没有任何一个现有消费文件可以不再 import 本包，收益 = 0 文件 / 0 import**。方法调用的入参与返回值就是本包词汇（`CachedOpenAIAccountsForGroupOptions`、`[]OpenAIAccountSecret`、`GatewayRuntime`、`GatewaySettings`、`*GroupUsageAccessMetadata` 等），调用方为消费返回值必须拿到类型。
3. **"可作用面"为零**：窄接口分组唯一能改变的是 39 处方法调用的声明形式，而其中 13 处（preauth 12 + dispatch 1）**已经走消费方自有端口接口**（这正是 R2 想要的终态，且已存在）；其余 26 处全部在组合根 `cmd`——组合根必须接线具体类型与构造函数，import 不可去除。额外新加一层"包级窄接口分组"只会把已存在的两个消费方端口重复一份。
4. **消费方 import 面按语义分组已经成立**（R2 验收要求的事实核对表）：

| 消费语义 | 包 | 依据 |
| --- | --- | --- |
| 组合根行为消费 + 接线 | `cmd/juhe-ai-gateway`（19 文件） | 构造函数 4 处 + `*Service` 直连 22 处 + `SQLReadModels` setter 4 处 + 大量类型词汇 |
| 读快照端口消费（已窄接口化） | `gatewaypreauth`（11 文件） | 自有 `RuntimeCacheReader` 8 方法端口 + `GatewayRequest`/preflight 词汇 |
| 读快照端口消费（已窄接口化） | `gatewaydispatch`（10 文件） | 自有 `RuntimeCachePort` 1 方法端口（本地 options/结果类型）+ `GatewayAPIKeyRow` 等词汇 |
| 类型词汇消费 | `gatewayresponse` / `gatewaycodex` / `gatewayaccounteffects` | `OpenAIAccountSecret`、`GatewayAPIKeyRow`、`ResponseInspectionPolicySummary` 等，行为调用 0 |
| 端口实现接线 | `gatewayclientip`（`AccountsSelector`/`CatalogSource`/`ConcurrencySource`/`SharedCacheFactory`/`SystemClock`）/ `gatewayproxyhealth`（`UserRequestLimits`）/ `gatewaysession`（`GroupBindingOrderer`）/ `gatewaycircuit` | 实现端口接口 + 消费词汇，行为调用 0 |
| 词汇消费 | `gatewayhotquality`（1 文件） | 3 行词汇引用，行为调用 0 |

5. **与 accounts（REFACTOR-0005 对象）的病理差异**：accounts 需要拆是因为包级边界过宽（29.8k 行、单 `Store` 221 方法、任何子域改动在同包积累耦合、且有明确的子域拆分后续需要中立基座）；gatewayruntimecache 无此病理——5,111 生产行、最大文件 652 行、文件级语义分组已存在、外部行为已全部端口化或集中在组合根。它的高扇入是"SQL 行模型词汇中心（`OpenAIAccountSecret`）+ 读缓存端口"的自然结果，不是边界失效。

## 6. 可选项评估：类型搬家到语义子包 + 根包 alias（对照 accountscore 模式，不推荐执行）

按任务要求把被消费的 42 个符号按语义分组（读快照/写失效/并发/SQL 模型），并评估 accountscore 式"中立下沉包 + 门面根 alias"方案：

| 语义分组 | 符号与引用数 | 合计 | 占比 |
| --- | --- | --- | --- |
| SQL 行模型/凭证词汇（types.go 主体） | `OpenAIAccountSecret` 252、`GatewayAPIKeyRow` 46、`GroupUsageAccessMetadata` 35、`GroupSchedulingPolicy` 33、`ProviderModelCatalogItem` 20、`GatewayAPIKeyGroupBindingRow` 16、`ResponseInspectionPolicySummary` 15、`AccountAPIKeyRuntimeSelectionState` 12、`AccountModelMapping` 9、`UserRequestLimits` 4、`ResponseInspectionPolicyMatch` 4 | 446 | 74.3% |
| 读快照请求/结果词汇 | `GatewaySettings` 37、`OpenAIAccountsForGroupResult` 15、`CachedOpenAIAccountsForGroupOptions` 14、`ModelCatalogListOptions` 10、`GatewayRuntime` 7、`OpenAIAccountsForGroupDiagnostics` 2、`RouteStrategyNormalRoutingConfig` 2、`OpenAIAccountsForGroupOptions` 1、`Options` 1、`RegistryConfig` 1 | 90 | 15.0% |
| 组合根接线类型 | `Service` 27 | 27 | 4.5% |
| 端口接口 | 8 接口（见 3.3） | 18 | 3.0% |
| 枚举常量 | `RouteStrategyMode*` 3 值 + `GroupAccessType*` 2 值 | 11 | 1.8% |
| 顶层函数引用 | 构造 4 + `Clone*` 2 + `SystemClock` 1 | 8 | 1.3% |

（写失效词汇 `ClearOptions` 与并发词汇的包外引用数为 **0**——四个语义分组中有两个没有外部消费者，分组搬家对它们无意义。）

**方案评估**：

- 方案形态：把 types.go 词汇（642 行）+ `Clone*` 函数下沉为中立子包（如 `gatewayruntimemodels`），根包保留 `type OpenAIAccountSecret = gatewayruntimemodels.OpenAIAccountSecret` 式 alias，依赖方向"所有人只 import 子包或经根 alias"。
- **收益（量化）**：现有 68 个生产消费文件 import 数减少 **0**（alias 保编译兼容，600 处限定引用全部照旧工作）；行为面 39 处调用不变；唯一真实收益是"若未来再拆包，子域可有中立依赖基座"——而本复查第 5 节结论是**没有拆包必要**，该收益没有兑现路径。R2 同时明确"不把缓存迁出 gateway"，本包没有 accounts 那样的待拆子域群（accountstest/balance/reset/transfer）作为下沉动机。
- **代价（量化）**：机械搬移 types.go 642 行 + 9 个 `Clone*` 函数及其测试，估算 diff ≥1,500 行；`go doc` 出现 alias 间接层；`GroupSchedulingPolicy = map[string]any`、`Clock = timeclock.Clock` 等 alias 链加深一层；文档联动（架构总览、功能文档、REFACTOR 登记）。
- **结论**：**不值得立项，登记"已评估、不执行"**。accountscore 模式的适用前提（god 包 + 明确的多子域拆分后续需要中立基座）在本包不成立；在收益为 0 的前提下付出 1.5k 行 diff 只增加间接层。

## 7. 对 R2 的验收结论

- R2 范围中 gatewayruntimecache 项：**复查通过、维持现状**——不需要窄接口分组改造，不需要类型搬家，零代码变更；本文档为该项验收证据。
- R2 验收项处置建议：
  - "消费方 import 面按语义分组"：**以第 5.4 节分组表作为已满足的事实记录收口**——语义分组在消费方已经自然成立（两个大行为消费方各有自有端口、小包全部是端口接线/词汇消费），继续"收窄"没有可作用的 import 面（反事实收益 0 文件），该验收项应从"改造"改记为"复核确认已成立"。
  - "`gatewayruntimecache` 包内文件不超 3000 行"：达标（最大 652 行）。
  - "全量回归全绿、覆盖率不降"：本项零代码变更，随 R2 波次整体验证即可；包现状覆盖率 98.2%（R2 前置裁剪时点账面）。
- 评估文档 2.5 节风险 A 条目中 gatewayruntimecache 的"扇入过高"表述，建议按本文档口径补注：232 处中 70.7% 为测试文件，生产行为调用面 39 处且 13 处已端口化。
- 建议文档联动（超出本次写入授权，由主代理执行）：计划文档 R2 节补记本结论；`docs/refactors/README.md` 索引登记本文件。

## 附录 A：39 处包外方法调用点明细

| 文件 | 方法（处数） |
| --- | --- |
| `cmd/juhe-ai-gateway/chain_routing.go` | `ListCachedOpenAIAccountsForGroupAsync`×2（L55/L331）、`ResolveCachedGroupUsageAccessMetadataAsync`×2（L44/L275）、`ResolveCachedProviderModelRouteAsync`×1（L70） |
| `cmd/juhe-ai-gateway/chain_chat.go` | `ListCachedOpenAIAccountsForGroupAsync`（L164）、`ListCachedProviderModelCatalogAsync`（L200）、`ReadCachedGatewayRuntimeAsync`（L225） |
| `cmd/juhe-ai-gateway/chain_compose.go` | `ListCachedOpenAIAccountsForGroupAsync`（L839）、`ListCachedProviderModelCatalogAsync`（L1030）、`ReadCachedGatewaySettings`（L1367） |
| `cmd/juhe-ai-gateway/chain_dispatch.go` | `ListCachedOpenAIAccountsForGroupAsync`（L276）、`ResolveCachedGroupUsageAccessMetadataAsync`（L283）、`ReadCachedGatewaySettings`（L681） |
| `cmd/juhe-ai-gateway/chain_ports.go` | `ResolveCachedGroupUsageAccessMetadataAsync`（L68）、`ListCachedOpenAIAccountsForGroupAsync`（L75） |
| `cmd/juhe-ai-gateway/chain_pricing.go` | `ListCachedProviderModelCatalogAsync`×2（L76/L200） |
| `cmd/juhe-ai-gateway/chain_driver.go` | `ListCachedProviderModelCatalogAsync`（L77） |
| `cmd/juhe-ai-gateway/chain_preflight.go` | `ReadCachedGatewayRuntimeAsync`（L39） |
| `cmd/juhe-ai-gateway/chain_openaicompat.go` | `ReadCachedGatewayRuntimeAsync`（L81） |
| `cmd/juhe-ai-gateway/compose_prewarm.go` | `PrewarmGatewayAPIKeyValidationCache`（L34） |
| `cmd/juhe-ai-gateway/chain_runtime.go` | `SQLReadModels.SetSettingsStore`（L306）、`SetAccountsSelector`（L313）、`SetCatalogSource`（L323）、`SetConcurrencySource`（L333） |
| `internal/gatewaypreauth/preflight.go` | `ReadCachedGatewaySettingsAsync`（L251）、`ResolveCachedGroupUsageAccessMetadataAsync`×3（L417/L556/L608）、`ListCachedOpenAIAccountsForGroupAsync`×3（L423/L735/L1611）、`ListCachedActiveResponseInspectionPoliciesForAccountsAsync`（L1018）——经 `RuntimeCacheReader` 端口 |
| `internal/gatewaypreauth/preflighthelpers.go` | `ListCachedProviderModelCatalogAsync`（L341）、`ListFreshOpenAIAccountsForGroupAsync`（L537）、`ListRecoverableUnavailableOpenAIAccountsForGroupAsync`（L543）——经 `RuntimeCacheReader` 端口 |
| `internal/gatewaypreauth/preauth.go` | `ReadCachedGatewayRuntimeAsync`（L153）——经 `RuntimeCacheReader` 端口 |
| `internal/gatewaydispatch/fallbackcandidate.go` | `ListCachedOpenAIAccountsForGroupAsync`（L122）——经 `RuntimeCachePort` 端口（本地类型签名） |

## 附录 B：复核命令

```bash
cd backend-go/projects/gateway

# 体量与文件健康
wc -l $(ls internal/gatewayruntimecache/*.go | grep -v _test.go) | sort -rn
go doc -all ./internal/gatewayruntimecache   # 导出符号类别表（type/func/method/const）

# 扇入口径（评估口径 vs 生产口径）
rg -l "backend-go-gateway/internal/gatewayruntimecache" internal/ cmd/ -g '*.go' | wc -l                    # 232（含测试）
rg -l "backend-go-gateway/internal/gatewayruntimecache" internal/ cmd/ -g '*.go' -g '!*_test.go' | wc -l    # 68（生产）

# 限定引用逐符号分类（①②③ 归类的数据源；600 处 / 42 符号）
rg -o "gatewayruntimecache\.[A-Z][A-Za-z0-9_]*" internal/ cmd/ -g '*.go' -g '!*_test.go' --no-filename | sort | uniq -c | sort -rn

# 行为调用点（读快照族 + SQL setter；逐一核对接收者类型）
rg -n "\.(ReadCachedGatewayRuntimeAsync|ReadCachedGatewaySettingsAsync?|ResolveCachedGroupUsageAccessMetadataAsync|ResolveCachedProviderModelRouteAsync|ListCachedOpenAIAccountsForGroupAsync|ListCachedProviderModelCatalogAsync|ListFreshOpenAIAccountsForGroupAsync|ListRecoverableUnavailableOpenAIAccountsForGroupAsync|ListCachedActiveResponseInspectionPoliciesForAccountsAsync|PrewarmGatewayAPIKeyValidationCache)\(" internal/ cmd/ -g '*.go' -g '!*_test.go'
rg -n "\.Set(SettingsStore|AccountsSelector|CatalogSource|ConcurrencySource)\(" cmd/juhe-ai-gateway -g '!*_test.go'

# 写失效/并发/Registry 零包外调用核查（应无输出）
rg -n "\.(ClearGatewayRuntimeCache|ClearGatewayRuntimeCacheLocal|InvalidateGatewayRuntimeCacheByAPIKeyID|AwaitBackgroundWork|EntryKey|ListEndpoints)\(" internal/ cmd/ -g '*.go' -g '!*_test.go' | grep -v internal/gatewayruntimecache/

# 消费方自有端口（依赖倒置已存在的证据）
rg -n -A10 "type RuntimeCacheReader interface" internal/gatewaypreauth/service.go
rg -n -B2 -A4 "type RuntimeCachePort interface" internal/gatewaydispatch/ports.go

# 反事实测算数据源：每个生产消费文件的限定引用行数（最小 1）
rg -l "backend-go-gateway/internal/gatewayruntimecache" internal/ cmd/ -g '*.go' -g '!*_test.go' | while read f; do echo "$(rg -c "gatewayruntimecache\.[A-Z]" "$f" || echo 0) $f"; done | sort -n

# 别名核查（应无输出，保证限定引用口径闭合）
rg -n "^\s*[a-zA-Z0-9_]+ \"[^\"]*gatewayruntimecache\"" internal/ cmd/ -g '*.go'
```
