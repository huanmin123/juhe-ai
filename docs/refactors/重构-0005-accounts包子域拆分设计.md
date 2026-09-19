# REFACTOR-0005 accounts 包子域拆分设计

## 基本信息

- 编号：REFACTOR-0005
- 状态：设计 v2 + 阶段 0/A/B 已实施（2026-09-18，见"实施记录"）；阶段 C 进行中
- 创建时间：2026-09-18
- 更新时间：2026-09-18
- 关联计划：PLAN-20260918T064119294Z（后端架构与性能优化改革，波次 R3）
- 关联模块：后端（`backend-go/projects/gateway/internal/accounts`）

## 实施记录（2026-09-18）

**阶段 B 已落地并通过独立核验**（全模块构建绿、外部引用 39 处逐条一致、门面覆盖率 95.0%、子域合计 95.4%、依赖方向无环、SQL 反字符串逐字节一致）：

- `accountsbalance/`（6 文件 1,380 行）+ 门面桥 `balance_subdomain_bridge.go`（446 行）：m11_balance/balance_config/oauth_usage_snapshot/quota_recovery 迁入；`snapshot_cleanup_port.go` 承载清理端口（执行器因依赖门面 `retryQueue` 泛型留根）。
- `accountsreset/`（2 文件 1,988 行）+ 门面桥（172 行）：runtime_reset 服务主体迁入（脚本化字节级变换，SQL 全程保护），`effects.go` 随迁；`runtime_reset_routes.go` 按阶段 A 降级先例留根（依赖 kernel/authsys 与路由 helper）。
- accountscore 增补：MinInt/MaxInt/IsAccountExpired/Itoa64/SortStrings/BoolInt/SQLForUpdate/PatchChange（消费方驱动的最小下沉）。
- 降级 8 项（沿用先例，均为"整体下沉需导出门面深读结构或重建装配"）：`prepareBalanceDraft`/`balanceDraftRow`/`findForceActivateSummary`（经 `Deps.FindAccountSummary` 注入）/`m11ScheduleAllowed`（经 `Deps.ScheduleGate` 注入）/`hydrateOAuthUsageSnapshots`/`StoreBalanceSnapshotCleaner` 执行器/`runtime_reset_routes.go`/批次 2 私有结构字段导出（约 110 处测试字面量适配）。
- 忠实度修正 1 处：子域内 `newID("dispatch")` 改经 `Deps.NewDispatchID` 闭包（初版误用 Store 注入的 newI，已对齐原语义）。

**阶段 0 + A 已落地并通过独立核验**（全模块构建绿、外部引用 82=82、门面覆盖率 95.1%、依赖方向无环）：

- `accountscore/`（8 文件 465 行）：错误三件套/AccessScope/Credentials/EncryptJSON/SQL 原语/endpoint-mode 谓词族/`StoreBase` 窄端口接口。**取舍：`Store` 结构体留根包**（下沉会牵动 221 个方法 receiver）。
- `accountstest/`（3 文件 2,459 行）：`Service{store accountscore.StoreBase}` 模式；`test_store.go`/`test_options_service.go`/`test_effects.go` 迁入。
- 门面桥 `test_subdomain_bridge.go`（411 行）：类型别名 + 现场构造 Service 的活适配器（保住 `clone := *store` 故障注入语义）+ 方法/自由函数转发 + `SetTestDispatchEffects` 原样保留（cmd 3 处消费零改动）。
- **降级 1**：`test_dispatch_routes.go`（1,373 行）留根包——依赖 list.go 类型面与路由 helper，经最小接口覆盖需下沉 list.go 并重建 HTTP 装配抽象，代价失控。
- **降级 2**：专属测试不物理随迁（12 个 DB 集成测试依赖根包 fixture）——采用"根包转发保持测试原位"策略，子域行为由根包测试经桥全量覆盖（子包无自有测试文件）。
- 最终形态备注：endpoint-mode 谓词族下沉 accountscore，写入侧归一化/driver 注册表留门面（设计 v2 表格的落地偏差，以此为准）。

## 重构目标

- `internal/accounts` 单包 51 个生产文件 / 29,841 行 / `Store` 上 221 个方法（2026-09-18 实测），是评估文档 2.3 节认定的第一 god 包。任何账户子域改动都在同一包内积累耦合。
- 按子域拆分包级边界，缩小改动半径；保持 `business/` 目录（18 个子包、根目录 0 生产行）已验证的包级拆分形态。
- 期望保留的边界：外部消费面零破坏（53 个外部文件引用 `accounts.XXX`）、对外路由与 SQLite/PG 双模语义不变、覆盖率不降（当前 95.0%）。

## 重构前问题

- 单一 `Store` 类型承载全部 221 个方法：CRUD、导入导出（import.go 1,832 行 / batch.go 1,826 行）、运行时重置（runtime_reset.go 1,775 行）、M11 授权/余额/流量迁移（8 个 m11_*.go 约 5.2k 行）、手动测试会话（test_* 4 文件 3,751 行）、调度时间计划等子域互相可见对方的全部符号，包内无编译期隔离。
- 外部扇入以类型词汇为主（`accounts.Credentials`×36、`accounts.EncryptJSON`×32、`accounts.NewStore`×23、`accounts.AuthorizationQuotaCheckInput`×23；消费方 31 个 cmd 文件 + 22 个 internal 文件），方法调用面相对小——这决定了"兼容门面"策略的收益很高。
- 包内跨子域调用点少而清晰（如 import.go:1652 调 `s.Create`；test_* 子域基本自含，只共享 `s.table`/`s.bind`/`s.now` 等 SQL 原语），为按依赖拓扑分阶段拆分提供了条件。

## 拆分设计

### 目标形态

```text
internal/accounts/                 # 兼容门面（保持包名不变）
├── core.go                        # Store 基础 + SQL 原语 + 错误类型 + AccessScope（原 store.go 演化）
├── credentials.go                 # Credentials 类型 + EncryptJSON/DecryptJSON（原 crypto.go/list.go 类型定义收敛）
├── routes.go / m09 / m11 / test / runtime_reset 各 Mount   # 组合根接线聚合
├── accountstest/                  # 阶段 A：手动测试会话子域
├── accountsbalance/               # 阶段 B：余额与探片子域
├── accountsreset/                 # 阶段 B：运行时重置子域
├── accountstransfer/              # 阶段 C：导入导出 + 批量编辑子域
└── accountsauthorized/            # 阶段 C：M11 授权/流量迁移子域（评估后可并回门面）
```

根包保留 type alias（`Credentials`、`ListItem`、`AccessScope`、`ConflictError` 等高扇入类型）与 `Deps.Mount`，外部 53 个消费文件零改动——与 R1 波次 Clock 别名迁移同一模式。

### 子域边界（按实测文件/行数）

| 子域包 | 文件 | 生产行数 | 阶段 |
| --- | --- | --- | --- |
| `accountstest` | test_dispatch_routes.go 1,373 / test_options_service.go 1,355 / test_store.go 962 / test_effects.go 61 | 3,751 | A |
| `accountsbalance` | m11_balance.go 698 / balance_config.go 280 / balance_snapshot_cleanup.go 302 / oauth_usage_snapshot.go 298 / quota_recovery.go 172 | 1,750 | B |
| `accountsreset` | runtime_reset.go 1,775 / runtime_reset_effects.go 121 / runtime_reset_routes.go 139 | 2,035 | B |
| `accountstransfer` | import.go 1,832 / import_source.go 906 / import_source_yaml.go 95 / export.go 671 / batch.go 1,826 / batch_effects.go 336 | 5,666 | C |
| `accountsauthorized`（评估项） | m11_* 其余 5 文件约 2,100 + m11_routes.go 870 | ~3,000 | C（可并回门面） |
| 留在门面 | store/list/write/patch/delete/lock/tags/clone/credentials_normalize/credential_update/crypto/client_compatibility/endpoint_modes/upstream_base_url/model_mapping_protocol_matrix/model_catalog_validation/error_policy/schedule/retryqueue/response_inspection/invalidation/api_key_runtime_revalidate/patch_runtime_state/authorization_stats/list_usage/authorized/routes 系列 | ~11,500 | — |

### 共享类型归属（修订 v2，2026-09-18：引入中立 core 包）

阶段 A 试点（2026-09-18）证伪了 v1 的"core 留门面根"方案：根包 `Deps.Mount` 聚合子域路由要求 `accounts → accountstest`，而子域持 Store 窄接口要求 `accountstest → accounts`，构成 Go 禁止的双向 import；且 test_* 子域实际引用约 40 个根包**私有**符号（endpoint-mode 谓词族、`normalizeProviderToken`、`NormalizeAccountCredentialsForWrite`、`assertMappingUpstreamsAllowed`、`isoMillis/ensureCtx` 等原语），这些符号被根包十余个生产文件共享——随迁（根包反向引用）、复制（行为漂移）、注入（面太宽）均不可行。试点 agent 零写入停机，取证充分。

修订后的归属：

- **中立下沉包 `internal/accounts/accountscore`**（新增）：`Store` 基础结构（db/postgres/secret/now/newID 字段与构造）、SQL 原语（table/bind/boolTrueLiteral/instantParam）、错误三件套（`ConflictError`/`ValidationError`/`RevisionConflictError`）、`AccessScope`、`Credentials`、`EncryptJSON`/`DecryptJSON`、`AuthorizedAccountReader`、endpoint-mode 谓词族与协议常量、共享私有小原语（`isoMillis/ensureCtx/nullPtrString/placeholders/containsString/stringSet` 等）。**依赖方向：所有人只 import accountscore，accountscore 不 import 任何兄弟包。**
- **门面根 `internal/accounts`**：保留全部既有导出符号为别名/包装（`type Credentials = accountscore.Credentials`、`func EncryptJSON(...) { return accountscore.EncryptJSON(...) }`），53 个外部消费文件零改动；`Deps`/`Mount` 聚合各子域路由（根 → 子域单向）。
- **子域包**（accountstest/balance/reset/transfer/authorized）：import accountscore + 门面导出的窄接口；子域私有类型随迁私有化。
- 循环依赖总约束：`accountscore ← 门面 ← 子域`，子域间互不 import；阶段 C 的 transfer→Create 经 accountscore 定义的 `AccountWriter` 窄接口注入。

### 循环依赖检查（修订 v2）

v1 只核了 `import.go:1652`（transfer→Create）一点，阶段 A 试点补全了另外两处关键事实：

1. **Mount 聚合方向**：根包 `Deps.Mount` 调用子域 `Mount` ⇒ 根 → 子域；因此子域对根包的任何 import 都构成 cycle。解法：子域只 import `accountscore`（中立包），对根包能力的需要经 accountscore 窄接口由门面装配注入。
2. **外部消费面实测（试点取证）**：cmd 侧 test 子域消费仅 3 处（`compose_account_test_local.go:219` 调 `SetTestDispatchEffects`，两处测试调 `TestDispatchEffects()`）；`SetTestDispatchEffects` 移入子域后由根包 `Store` 保留同名转发字段/方法，外部零改动成立。
3. **transfer→Create**：经 accountscore 的 `AccountWriter` 窄接口注入（不变）。

结论：`accountscore ← 门面 ← 子域` 单向图成立，无 cycle。

## 变更范围

- 受影响：`internal/accounts/` 全部 51 个生产文件（迁移动作）+ 对应 `*_test.go` 随迁；新增 4-5 个子域包目录。
- 不动：全部 53 个外部消费文件（`accounts.` 前缀引用保持合法）、`cmd/` 组合根的 `Deps` 装配（`Mount` 签名不变）、路由路径、存储 schema。
- 文档联动：本文件 + `docs/plans/` 计划状态 + `docs/refactors/README.md` 索引。

## 行为边界

- 保持不变：HTTP 路由与方法面、鉴权 guard 链、SQLite/PG 双模 SQL、错误文案、审计事件、审计 metadata 字段、覆盖率口径（迁移文件的方法覆盖率随测试文件同包迁移，数值不因搬家改变）。
- 明确变化：仅包结构（新子域包）；`Store` 方法逐步改为子域服务类型的方法（阶段内一次性完成，不保留双入口）。
- 说明：`SetTestDispatchEffects` 等 Store 注入口在子域化后由子域服务构造函数接管，门面保留同名转发（组合根不变）。

## 分阶段实施与验收

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| 0（修订新增） | 抽取中立包 `accountscore`：类型/原语/谓词族下沉 + 门面根别名/包装（约 2k 行 diff，机械搬移），外部引用零改动 | 全模块构建 + accounts 包测试全绿 + 覆盖率 ≥95.0% + `rg "accounts\.(EncryptJSON|Credentials|ConflictError)"` 外部命中数不变 |
| A | `accountstest` 拆出（3,751 行，仅依赖 accountscore 与自身） | 该包测试全绿 + accounts 门面覆盖率 ≥95.0% + 外部引用零改动 |
| B | `accountsbalance` / `accountsreset` 拆出 | 同上，两子域独立提交独立验证 |
| C | `accountstransfer`（AccountWriter 窄接口方案）或回退方案 | 同上 + `go vet` 无 import cycle |
| 收尾 | 门面根行数复核（目标 ≤12k）、`rg "func (s *Store)"` 方法数收敛复核、复盘更新本文件 | 评估文档 god 包表复测更新 |

每阶段验证命令：`cd backend-go/projects/gateway && go build ./... && go test ./internal/accounts/... -count=1`，加 `go test -cover` 覆盖率核对。

## 风险与后续

- **测试随迁断裂风险**：accounts 包测试 31k+ 行与生产文件同包强绑定，阶段 A 先行验证随迁模式（同目录测试文件按子域前缀成批 `git mv` 等价改名 + 包名调整）。
- **门面 alias 与子域类型的双向可见**：只允许子域 → 门面方向 import，评审时以 `go list -deps` 断言无环。
- **与覆盖率收尾的协调**：M1 六包收尾完成前不动 `internal/accounts`（避免同包写入冲突）；本设计实施排在 M1 之后。
- 后续同类：`cmd/juhe-ai-gateway`（26.9k）与 `chat`（16.1k）的拆分设计在本阶段 A 验证随迁模式后复用本文件框架。
