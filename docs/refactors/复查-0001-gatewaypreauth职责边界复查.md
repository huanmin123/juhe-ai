# 复查-0001 gatewaypreauth 职责边界复查

## 基本信息

- 编号：复查-0001（R2 波次 gatewaypreauth 项验收证据）
- 状态：已完成（结论：**维持现状，不拆包、不加窄接口层，零代码变更**）
- 创建时间：2026-09-18
- 关联计划：[PLAN-20260918T064119294Z](../plans/计划-20260918T064119294Z-后端架构与性能优化改革.md) 波次 R2（缓存与 preauth 扇入治理）
- 关联评估：[后端架构与性能现状评估（2026-09-18）](../reports/后端架构与性能现状评估-20260918.md) 2.5 节扇入表 / 风险 A2
- 参照框架：[REFACTOR-0005](重构-0005-accounts包子域拆分设计.md)（accounts 包子域拆分设计，分类口径与判定框架来源）
- 复查对象：`backend-go/projects/gateway/internal/gatewaypreauth`（评估扇入表第 2 名，198 处引用）
- 复查方式：只读取证 + 本文档；未改动任何代码

## 1. 复查问题与事实纠正

评估文档 2.5 节原始表述："`gatewaypreauth`（198）同时被缓存、调度、公开面引用，职责边界（可用性/重试预算/预算调度）在包内有膨胀迹象"。R2 波次要求："职责边界复查——可用性/重试预算/预算调度是否需要拆包"。

先纠正评估中的一处事实：**生产代码中 `gatewayruntimecache` 并不 import `gatewaypreauth`**。依赖方向是 `gatewaypreauth → gatewayruntimecache`（经 `service.go` 的 `RuntimeCacheReader` 端口，`*gatewayruntimecache.Service` 直接实现）。"被缓存引用"不成立，扇入主体是调度（dispatch）、响应（response）、组合根（cmd）与端口实现包。

## 2. 判定标准（复查取证前声明）

沿用 REFACTOR-0005 取证口径（accounts："外部扇入以类型词汇为主、方法调用面相对小"）与 gatewayruntimecache 复查结论（"cache 包外方法调用点仅 39 处，扇入主体为类型词汇"）：

1. **若扇入主体是类型/常量词汇**（结构体、枚举、错误、端口接口）**且行为方法调用点少** → 不需要拆包/窄接口，登记"复查通过、维持现状"，本文档即为 R2 该项验收证据。
2. **若存在清晰的多职责块且行为调用面大** → 按 REFACTOR-0005 框架产出拆包/窄接口分组设计（子域边界表、依赖方向、循环依赖检查、分阶段验收）。

## 3. 实测数字（2026-09-18 工作区）

### 3.1 体量与方法分布

- 23 个生产文件 / 7,000 生产行；262 个 `func` 声明 = 51 个导出顶层函数 + 49 个导出方法 + 162 个非导出函数/方法。
- 导出符号总量 245：类型 124（81 struct + 29 interface + 14 alias/枚举）+ 顶层函数 51 + 常量 70。
- 文件健康：最大 `preflight.go` 1,651 行，**全部 ≤3000 硬上限，无一超 2000 预警线**。
- 包内最大逻辑块是单函数：`preflight.go` 的 `PrepareOpenAIGatewayDispatchContext` 约 930 行（139–1069 行），不构成文件级超标，仅作为观察记录。

### 3.2 扇入口径复核

| 口径 | 数值 |
| --- | --- |
| 评估口径（import 路径字符串出现，含测试文件） | 当前 201 文件（评估时点 198） |
| 其中生产代码 | **64 文件 / 8 个消费包** |
| 其中测试文件 | 137 文件（占扇入数字 68%） |

生产消费包分布：`gatewaydispatch` 20 / `gatewayresponse` 15 / `cmd/juhe-ai-gateway` 14 / `gatewaycodex` 9 / `gatewayclientip` 3 / `gatewaysession` 1 / `gatewayproxyhealth` 1 / `gatewaycircuit` 1。

别名核查：全工作区无 import 别名、无匿名导入，`gatewaypreauth.` 限定引用口径闭合。

### 3.3 消费内容分类（关键证据）

生产代码 `gatewaypreauth.X` 匹配共 834 处，其中 4 处是 `chain_compose.go` 的诊断字符串字面量（非代码引用），剔除后 **830 处真实符号引用**：

| 类别 | 引用次数 | 占比 | 符号数 |
| --- | --- | --- | --- |
| ① 类型/常量词汇（struct/alias/枚举类型 + 常量 + 错误类型） | 611 | **73.6%** | 99（72 类型 + 27 常量） |
| ③ 接口 | 117 | 14.1% | 23 |
| ② 被调用的顶层函数 | 102 | 12.3% | 32 |

- ① 最大单体：`GatewayRequest` 184 处（占总引用 22.2%）——网关请求视图结构体；其后为 `GatewayFailureUsageContext` 28、`CircuitDecision` 26、`RequestModel` 18、`AuditFinalizeInput` 18 等。
- ③ 主体是端口接口：`GatewayResponseWriter` 16、`AuditCaptureContext` 14、`ClientIPPolicy` 8、`UserRequestLimits` 7、`ClientIPAccountAvoidanceFactory` 7、`PreAuthCircuits` 6、`CodexBridgePreflight` 6、`CandidatePipeline` 6 等——这些是依赖倒置声明（实现方在组合根接线），不是对包内行为的调用。
- ② 以纯函数为主：`GatewayErrorPayloadOf` 20、`RequestModel` 18、`RequestStream` 11、`SendGatewayJSONError` 3 等。
- 导出符号中被包外引用的仅 154/245（62.9%）。

### 3.4 包外方法调用点（限定引用统计不到，已逐接收者核实）

| 接收类型 | 调用点 | 分布 | 性质 |
| --- | --- | --- | --- |
| `Service` 编排入口 | 5 | 全部在 `cmd/juhe-ai-gateway`（`PreResolveGatewayRuntime` 1 / `PrepareOpenAIGatewayDispatchContext` 1 / `PrepareAPIKeyGroupFallbackDispatchContext` 2 / `HandleGatewayRequestKnownErrorResponse` 1，均在 `chain_v1.go`） | 行为入口 |
| `Service.NowMs` / `StartedAt` | 4 | 全部在 `cmd/juhe-ai-gateway` | 时钟 helper |
| `Service` 其余 11 个导出方法（`Reject*` / `Resolve*` / `Send*` / `Extract*` / `GatewayPreAuthSource` / `RecordClientIPRequestErrorSample` / `BuildGatewayUsageContext`） | **0** | 仅包内（含包内 7 个测试文件） | 零包外调用 |
| `ServerRetryBudget` | 50（49 方法调用 + 1 构造） | `gatewaydispatch` 45 / `cmd` 5 | 行为（重试预算推进） |
| `GatewayRequest` 访问器 | 129（`PathAndQuery` 40 / `MethodUpper` 33 / `Header` 23 / `BodyState` 15 / `ParsedJSONObjectBody` 10 / `Path` 8） | dispatch / response / codex / cmd | 词汇访问器 |
| `TrackingWriter` 专属方法 | ~24（`WritableEnded` 13 / `HeadersSent` 11） | dispatch / response / cmd | 词汇访问器（部分实为 `GatewayResponseWriter` 接口调用） |

行为调用点合计 ≈ **59 处**（Service 9 + budget 50），其中真正的编排入口仅 **5 处、全部在组合根**。对照：gatewayruntimecache 同口径为 39 处方法调用点。

## 4. 包内职责块（评估假设"三个边界膨胀"的复核）

| 职责块 | 文件（行数） | 行数 | 对外暴露面 |
| --- | --- | --- | --- |
| A 编排入口与配额预检 | service.go 137 / preauth.go 745 / coordinator.go 145 / authorizationpreflight.go 265 | 1,292 | `Service`（17 导出方法）、`New`、`RuntimeCacheReader` 等协作端口、`PreAuthFailure*` / `CircuitDecision` |
| B preflight 可用性与路由编排 | preflight.go 1,651 / preflighthelpers.go 762 / resolverport.go 121 | 2,534 | `PreflightInput/Result`、`RouteResolver`、`Normal*` / `Hybrid*` 路由结果类型、`DispatchContext` |
| C 重试预算 | retrybudget.go 152 | 152 | `ServerRetryBudget`、`GatewayAccountAvailability`、`ShouldHandoffClient`（自含 Node port，仅依赖 Clock） |
| D 错误响应与响应写 | errorresponse.go 593 / responses.go 451 / localrequesterrors.go 97 / validationerror.go 121 | 1,262 | `GatewayErrorPayload` 族、`GatewayResponseWriter` / `TrackingWriter`、`SendGatewayErrorResponse` |
| E 请求视图与协议识别 | request.go 155 / metadata.go 186 / metadata_endpointmodes.go 113 / protocolview.go 104 / modelsresponseprotocol.go 118 / abort.go 98 / imagepermission.go 84 | 858 | `GatewayRequest`、纯函数识别器/访问器、EndpointMode 常量 |
| F 类型/端口/审计 | types.go 300 / ports.go 525 / auditcapturecancel.go 50 / doc.go 27 | 902 | `DispatchContext`、审计输入、15+ 端口接口 |

复核发现：

1. **C（重试预算）不膨胀**：152 行自含 Node port（`runtime/server-retry-budget.ts` 移植），仅依赖 Clock，无外部状态。
2. **"预算调度"的行为主体不在本包**：budget 的 50 处外部行为调用中 45 处在 `gatewaydispatch`（恢复等待/墙钟预算推进发生在 dispatch 引擎内）；preauth 只定义预算词汇并持有起点。
3. **可用性判断（B 块）确实是包内最大块**（2,534 行），但其包外消费以类型词汇为主，行为入口 `PrepareOpenAIGatewayDispatchContext` 仅 1 处调用（组合根）。

## 5. 判定：不需要拆包，维持现状

事前判定标准第 1 条成立：

1. **扇入主体是类型词汇（73.6%），行为调用点少**（编排入口 5 处全在组合根、行为调用合计 59 处）。与 REFACTOR-0005 取证时的 accounts 同构（`accounts.Credentials`×36、`EncryptJSON`×32 等），与 gatewayruntimecache 复查口径（39 处）同量级。
2. **"三个职责块膨胀"假设复核不成立**（见第 4 节）：六块边界清晰、块间调用集中在 `Service` 编排入口，包 7,000 行不构成 god 包。
3. **拆包反事实无收益（关键证据）**：
   - 拆出 C（retrybudget）：`gatewaydispatch` 20 个 import 文件中仅 5 个使用 `ServerRetryBudget`，且这 5 个**全部同时消费其他 preauth 类型** → import 文件数减少 0，净增一个包。
   - 拆出 E（请求视图）：`GatewayRequest` 是最大词汇中心（184 处类型引用 + 129 处访问器调用），拆出只会把"共享词汇"跨包化、增加全工作区 import 数，与治理目标相反。
   - 拆出 D（错误响应）：`GatewayErrorPayload` / `GatewayResponseWriter` 同为跨包词汇，同理。
4. **接口收窄无杠杆**：Go 的 import 耦合按文件不按符号，收窄导出面不减少 import 数；唯一等效手段是拆包，而拆包反事实已证无收益。可选的低价值清理（不立项）：11 个零包外调用的 `Service` 导出方法与 91 个零包外引用的导出符号可评估 unexport，但收益仅是 API 表面积文档化，无耦合收益。
5. **端口接口面是依赖倒置的正常形态**：四个小消费包的引用全部是"端口实现接线 + 决策类型消费"，不是对本包行为编排的依赖。R2 验收要求的"消费方 import 面按语义分组"如下表：

| 消费语义 | 包 | 依据 |
| --- | --- | --- |
| 组合根行为消费 | `cmd/juhe-ai-gateway` | 5 个编排入口 + `NowMs`/`StartedAt` + budget 构造 + 大量类型词汇 |
| 预算行为消费 | `gatewaydispatch` | 45 处 budget 方法调用 + `GatewayRequest` / `DispatchContext` 等类型词汇 |
| 类型词汇消费 | `gatewayresponse` / `gatewaycodex` | `GatewayRequest`、`GatewayErrorPayload`、`GatewayResponseWriter` 等 |
| 端口实现接线 | `gatewaycircuit`（`RecoverableWait`）/ `gatewayproxyhealth`（`UserRequestLimits`、`AuthenticatedModelsRateLimit`）/ `gatewaysession`（`SessionAffinity`、`SessionIdentityResolver`）/ `gatewayclientip`（`ClientIPPolicy`、`PreAuthCircuits`、黑名单类型） | 实现端口接口 + 消费决策类型 |

6. **与 accounts（REFACTOR-0005 对象）的差异**：accounts 需要拆是因为包级边界过宽（29.8k 行、单 `Store` 221 方法、任何子域改动在同包积累耦合）；preauth 无此病理——7,000 行、文件全部低于预警线、块间耦合集中在编排入口。它的高扇入是"请求视图词汇中心（`GatewayRequest`）+ 编排入口"的自然结果，不是边界失效。

## 6. 对 R2 的验收结论

- R2 范围中 gatewaypreauth 项：**复查通过、维持现状**——不需要拆包，不需要窄接口分组改造，零代码变更；本文档为该项验收证据。
- R2 的另一半（gatewayruntimecache 窄接口分组）不受本结论影响，另行推进。
- 建议文档联动（超出本次写入授权，由主代理执行）：计划文档 R2 节补记本结论；`docs/refactors/README.md` 索引登记本文件。

## 附录：复核命令

```bash
cd backend-go/projects/gateway

# 体量与方法分布
wc -l $(ls internal/gatewaypreauth/*.go | grep -v _test.go) | sort -rn
rg -c "^func " internal/gatewaypreauth -g '!*_test.go'

# 扇入口径（评估口径 vs 生产口径）
rg -l "backend-go-gateway/internal/gatewaypreauth" internal/ cmd/ -g '*.go' | wc -l                    # 201（含测试）
rg -l "backend-go-gateway/internal/gatewaypreauth" internal/ cmd/ -g '*.go' -g '!*_test.go' | wc -l    # 64（生产）

# 限定引用逐符号分类（①②③ 归类的数据源）
rg -o "gatewaypreauth\.[A-Z][A-Za-z0-9_]*" internal/ cmd/ -g '*.go' -g '!*_test.go' --no-filename | sort | uniq -c | sort -rn
go doc -all ./internal/gatewaypreauth   # 导出符号类别表（type/func/method/const）

# 行为调用点（编排入口与预算）
rg -n "\.(PrepareOpenAIGatewayDispatchContext|PreResolveGatewayRuntime|PrepareAPIKeyGroupFallbackDispatchContext|HandleGatewayRequestKnownErrorResponse)\(" cmd/ -g '!*_test.go'
rg -c "\.(RemainingMs|PauseNoAvailableWait|HandoffRequired|BeginNoAvailableWait|SetWaitObserver|DeadlineAtMs)\(" internal/gatewaydispatch cmd/juhe-ai-gateway -g '!*_test.go'

# 别名核查（应无输出，保证限定引用口径闭合）
rg -n "^\s*[a-zA-Z0-9_]+ \"[^\"]*gatewaypreauth\"" internal/ cmd/ -g '*.go'
```
