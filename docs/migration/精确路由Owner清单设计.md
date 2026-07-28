# 精确路由 Owner 清单设计

## 1. 目标与边界

本设计把 `deploy/owner-manifest.json` 从四大域粗粒度声明扩展为可选的 `HTTP method + path template` 精确声明，使管理接口、公开接口和网关接口可以按已验证纵切面逐条登记 owner，而不必等待整个域同时切换。

本批只交付声明格式、严格校验、解析函数和回滚 manifest 生成能力，不接入 Caddy、Nginx、Node、Go 或任何代理 dispatch，不改变当前监听、启动、owner lock、数据库 writer 和生产流量。当前仓库的 `routeAllowlist` 为空，四大域仍全部由 Node 持有。

worker 不是 HTTP route，继续只使用 `routeOwners.worker`；禁止伪造 method/path 规则拆分 worker owner。

## 2. Schema v2

`routeOwners` 保留并作为没有精确命中时的默认 owner。`rollbackRouteOwners` 保存四大域的上一个可恢复 owner。`routeAllowlist` 只保存明确列出的 HTTP 路由：

```json
{
  "schemaVersion": 2,
  "deploymentEpoch": "go-management-slice-2026-07-22-001",
  "release": {
    "nodeVersion": "0.1.0",
    "goVersion": "0.1.0-w0",
    "schemaVersion": 85
  },
  "routeOwners": {
    "management": "node",
    "public": "node",
    "gateway": "node",
    "worker": "node"
  },
  "rollbackRouteOwners": {
    "management": "node",
    "public": "node",
    "gateway": "node",
    "worker": "node"
  },
  "routeAllowlist": [
    {
      "surface": "management",
      "method": "GET",
      "path": "/__aisys__/api/accounts/{accountId}",
      "owner": "go",
      "rollbackOwner": "node"
    }
  ]
}
```

schema v1 的结构仍可被当前校验器识别，但发布 schema 仍必须等于当前 Goose catalog；自动回滚要求 v2，因为 v1 没有足够的前一 owner 证据。`deploy/owner-manifest.schema.json` 描述结构和基础枚举，`scripts/validate-owner-manifest.mjs` 额外承担 JSON Schema 无法直接表达的模板相交、surface 前缀和回滚语义校验。

### 2.1 capability v2 所需 Schema v3 bootstrap

当前实现仍是 schema v2；能力健康 v2 不能直接把生产 manifest 替换成 v3，因为旧 v2-only validator 会把未知字段拒绝并造成所有 owner 无法启动。完整 bootstrap 固定为：

1. 先在仍 active 的 v2 epoch 发布一个**行为不变**的兼容 release。它继续只写 v2 manifest，但 validator / startup preflight 能严格读取 v2 和 v3，能够验证下述 v3 字段，并有 v2 -> v3 -> v2 golden tests；该 release 的 digest 与部署证据先落库。
2. 只有全部生产 owner 都运行兼容 release 后，协调器才能创建 schema v3 prepared manifest。v3 的 canonical **被签名 payload** 顶层结构固定如下；字段只能使用 `contractVersions.*`，禁止再出现 `contracts.*`、单一 `producerSetDigest` 或 runbook 自定义别名：

```text
schemaVersion = 3
deploymentEpoch
targetDeploymentEpoch
targetEpochMode = prepared | active
release
routeOwners
rollbackRouteOwners
routeAllowlist
contractVersions:
  capability = { version, minReader, minWriter }
  sentinelWatermark = { version, minReader, minWriter }
  stats = { version, minReader, minWriter }
  capabilityHandoff = {
    version, minReader, minWriter,
    spoolRecordVersion, checksumVersion, receiptSchemaVersion,
    quarantineSchemaVersion, healthHoldSchemaVersion,
    deliveryResolutionVersion, ackCursorVersion,
    producerRegistrySchemaVersion, producerWatermarkSchemaVersion,
    producerInventoryCertificateSchemaVersion,
    replayLeaseFencingVersion, producerEpochPolicyVersion,
    replicationReceiptSchemaVersion,
    accountPhysicalProbeGateSchemaVersion, probeDueSchemaVersion,
    activationSelectionSchemaVersion, accountHoldSchemaVersion,
    globalBarrierSchemaVersion, tailQuarantineFormatVersion,
    quarantineArtifactFormatVersion, artifactArchiveManifestVersion,
    evidenceArtifactManifestVersion, canonicalManifestEnvelopeVersion,
    backupBarrierSchemaVersion, backupEvidenceVersion,
    maxReplayRecordVersion
  }
roleTopology
sourceProducerInventoryCertificateDigest
targetProducerInventoryCertificateDigest
sourceProducerSetDigest
targetProducerSetDigest
gooseCatalogDigest
capabilityCohort
```

`deploymentEpoch` 是该 manifest 的 epoch 身份，`targetDeploymentEpoch` 必须与它精确相等；`targetEpochMode` 约束同一 release 进程是无副作用 prepared 还是生产 active。`roleTopology` 固定每类进程、host / shard owner 和 fencing 域。`sourceProducerSetDigest` 绑定当前正在承接流量、待 drain 的完整 producer set；`targetProducerSetDigest` 绑定已注册并 fsync、状态仅为 `active_target_pending` 的目标 producer set；二者不得合并为一个摘要。source / target certificate digest 分别把两个 set 绑定到连续签名 inventory 证书链；manifest 不包含运行期会变化的 expected inventory revision。

每个 active epoch 另有 append-only `deployment_producer_inventory_certificates` 和单行 active pointer。证书固定 `deploymentEpoch / inventoryRevision / previousCertificateDigest / ownerManifestDigest / producerSetDigest / producerEntriesDigest / expectedHostSetDigest / replicationPolicyDigest / reason / issuedAt`，revision 连续、previous digest 不断链，第一张 target certificate 必须等于 manifest 的 `targetProducerInventoryCertificateDigest`。备份恢复、扩缩容或 producer 重启先把新 epoch 注册为 `active_target_pending`，再由 coordinator 校验 registry set / role / replication proof，并原子 CAS 新 certificate pointer 后转 active；旧证书和 sealed producer 不能再次授权 append。所有 activation / backup / rollback / stop barrier 冻结 `inventoryRevision + certificateDigest`，冻结后集合或 pointer 变化即失败。

`capabilityHandoff.receiptSchemaVersion` 的唯一 schema 必须同时覆盖 business、candidate 和 delivery receipt，并完整固定 spool / checksum、delivery resolution、ACK、producer registry / watermark / seal、replay fencing、producer epoch policy、record / tail quarantine、scope / account hold / global barrier、quarantine artifact、内容寻址 artifact archive、backup barrier / evidence、account physical 5 分钟 gate、probe due、activation selection、跨故障域 replication policy / receipt 和最大兼容 replay version；不得由各 runbook 手抄子集。gateway、每 host replay / quarantine owner、control / due / activation owner、backup coordinator 任一不兼容都拒绝 ready。

`capabilityCohort` 固定声明 `policyVersion`、`hashAlgorithm=sha256`、`hashSeedId`、`keySource=accountRuntimeKey`、`allowedBasisPoints=[0,100,500,2500,10000]`、`initialMutationBasisPoints=0`、`denySafetyProjection=true`、`allowedControlModes=[live_same_window,frozen_previous_cohort_baseline]`、类型化 threshold policy digest 和 epoch-fenced `stateRef`。1% / 5% / 25% 只允许 `live_same_window`；100% 必须引用已通过并冻结的 25% 基线，使用 `frozen_previous_cohort_baseline`，同时继续检查绝对 SLO 与 hard invariant。mutation cohort 降为 0 时 deny-only 仍应用既有 blocked / hold。

payload 使用 RFC 8785 canonical JSON 后计算 SHA-256。`manifestDigest` **不属于被摘要 payload**，只保存在 PostgreSQL epoch 行和外层签名 envelope，避免自引用。envelope 固定 `purpose / canonicalization=RFC8785 / digestAlgorithm=SHA-256 / signatureAlgorithm=Ed25519 / manifestDigest / keyId / trustRootId / environmentId / deploymentEpoch / barrierId / releaseDigest / issuedAt / expiresAt / sequence / signature`；签名覆盖域分隔符、digest 与除 signature 外全部 envelope metadata。purpose 只允许 `owner_activation / producer_inventory / runtime_authorization / backup_evidence / rollback_evidence / restore_authorization`。validator 必须检查 purpose、信任根、key 轮换 / 吊销、环境与 epoch、单调 sequence 和有效期，拒绝跨 purpose、旧 epoch或旧 sequence 重放。所有 payload 字段都进入 digest，未知字段或未知值 fail-closed。短期 owner activation envelope 过期后不能继续用来启动进程；每次启动、重启或重新 ready 都要求新鲜 runtime authorization，绑定 manifestDigest、当前 active inventory certificate digest、release 和 role / shard。长期 backup / rollback 内容证明即使 envelope 到期也不被改写，但实际恢复 / 回滚另需新鲜 restore authorization。

3. prepared 进程只允许隔离 namespace 的只读 smoke、shadow rebuild 和无副作用校验；数据库拒绝其业务 mutation，对外 listener 也不得接生产流量。prepared target producer 必须先把 metadata / initial sequence 写到 release 外并 fsync，再以 `active_target_pending` 注册到 `targetProducerSetDigest`，并签发与 manifest 锚点一致的首张 target inventory certificate，不得产生 delivery、业务副作用或生产流量。激活协调器写入 `ingress_frozen` 时同时 fence 新 ingress、command 创建、due claim、admission 和 probe scheduling，冻结 source active inventory certificate pointer，只允许冻结前已 accepted 的 request finalizer、outbox / projector、replay 与 result-ready fanout 继续收敛；随后才 seal / drain certificate 对应的 `sourceProducerSetDigest`，证明连续 watermark、ACK、跨故障域 replica receipt 和无 lost / unknown tail。source drain certificate 成立后才能在同一事务 CAS active epoch 与 target inventory pointer；目标 gateway、每 host replay、control、Asynq、stats、API 再凭新鲜 runtime authorization 从 prepared 晋升 / 重启为 active mode，目标 producer 由 `active_target_pending` CAS 为 active，并把 active epoch / manifest、outbox barrier、两个 producer certificate / set digest、replay lease 和 role-ready proof 写入 activation barrier。全部期望角色 ready 后才切代理；target producer 不得被误 seal。角色失败时保持流量冻结并生成新 rollback epoch。
4. v3 初次激活固定为 cohort `0`。`stateRef` 指向 PostgreSQL 中以 `deploymentEpoch + cohortRevision` 唯一的 append-only cohort state；晋级只能按 `0 -> 100 -> 500 -> 2500 -> 10000` 相邻 CAS。普通 SLO 退化只降一档；数据完整性、安全、签名、producer / replica barrier 失败可从任意档直接 CAS 到 0。每次记录 `enabledBasisPoints`、`controlMode`、证据 digest、操作者、时间和 previous revision；gateway 只接受 manifest digest、active epoch 和 cohort state digest 同时匹配的快照。这样 1% / 5% / 25% / 100% 晋级不会伪装成新的 owner epoch，但每次变化仍可审计、可回退且不能修改 hash seed。
5. v3 激活和完整回滚都创建新的 deployment epoch。回滚候选不能只是交换 `owner/rollbackOwner`；必须重新计算 contract 版本、目标 schema、进程 owner、source / target producer certificate 与 set、before-image、payload digest 和签名 envelope，并经协调器 CAS 激活。回滚 / 备份先 fence 新工作并冻结 current certificate，但冻结前已 accepted 的 finalizer、replay、fanout 和 projector 必须继续到 drain certificate；backup / rollback / stop 只 seal 该 certificate 的 current producer set。备份发布后不得重新打开已 sealed producer epoch；每个 gateway 创建并 fsync 新 producer epoch、以 `active_target_pending` 注册并取得 replay lease / role-ready proof，协调器签发 previous digest 连续的新 inventory certificate，并在 set / policy / signature 全部匹配后 CAS active pointer，随后才解除 mutation / ingress fence；任一验证失败都保持冻结。普通 cohort 阈值只按上一条规则降低 cohort revision；listener / 数据契约故障进入完整新 epoch 回滚。

在 v3 validator、schema、命令与测试真正实现前，本节只是 capability v2 的阻断设计，不把当前 manifest 误报为已升级。v2-only Node 二进制不是有效的 v3 rollback artifact。

### 2.2 W11 pure-Go Schema v4

W11 删除 Node 后端前必须完成独立的 v3 -> v4 bootstrap，不能在仍要求 Node rollback artifact 的 v3 上直接删包：

1. 当 management / public / gateway / worker 和 AI Chat 全部由 Go 持有且 W10 cohort、Chat 切流和回滚演练完成后，先发布只改变 validator 的 Go release；它读取 v3/v4、仍写 v3，active v3 epoch 的行为不变。
2. v4 删除 v2/v3 的强制 `release.nodeVersion`，改用 `releaseTargets.current` 与 `releaseTargets.rollback`。每个 target 固定 `targetId`、`runtime=go`、`goVersion`、artifact digest、Goose catalog digest 和独立的 release-metadata digest；两个 `targetId` 与 artifact digest 必须不同，且两个 release 都必须支持 v4、在冻结仓库中可取、可启动并通过相同数据契约 preflight。owner manifest 自身的 digest 仍只在对象外层计算，禁止形成自引用字段。
3. v4 的四大 `routeOwners` 固定为 `go`，删除粗粒度 `rollbackRouteOwners`。W11 前清空迁移期精确 allowlist 或把仍有业务含义的项规范化为只含 `owner=go` 的 v4 route entry；v4 route entry 不再包含 `rollbackOwner`，发布回滚目标完全由 `releaseTargets.rollback.targetId` 表达。
4. 协调器创建 v4 prepared epoch并验证当前 / 回滚两个 Go target，激活后完成一次“当前 Go -> 上一 Go -> 当前 Go”的新 epoch 回滚演练。只有 v4 active、旧 v3 owner 全部 retired、演练证据落库后，才允许删除 Node artifact、`nodeVersion` 配置和 Node 构建依赖。
5. v4 回滚仍生成新 epoch：把原 `rollback` target 作为新 `current`，把刚退出的 target 作为新 `rollback`，重新计算 schema、contract、cohort、before-image 和 digest；禁止只交换字符串或复用旧 epoch。

当前代码没有 v4 validator 或 schema。本节是 W11 的阻断契约，不表示现有 `deploy/owner-manifest.schema.json` 已支持 pure-Go manifest。

## 3. 精确 allowlist 规则

- `surface` 只允许 `management`、`public`、`gateway`。management 必须位于 `/__aisys__/api`，public 必须位于 `/__aipublic__`，gateway 禁止占用这两个保留前缀，且首段必须是 literal，防止 `/{surface}/...` 模板覆盖保留前缀。
- method 必须是明确的大写 HTTP method；不接受 `ANY`、小写或 `*`。owner 解析严格匹配 method，`GET` 不会隐式接管 `HEAD`；两者需要分别切流时必须分别登记。
- path 区分大小写，禁止 query、fragment、百分号编码、反斜杠、空段、尾斜杠和 `.` / `..` 段。
- 动态参数默认只允许占据完整 segment 的 `{name}`，名称必须以字母开头且单条模板内不重复。为覆盖 Gemini native action，额外允许受限的 `{model}:literalAction`；例如 `/v1beta/models/{model}:generateContent`。仍禁止 `*`、`**`、Express `:id`、`{id}.json`、多个参数拼接和正则表达式。
- 任意两条相同 method 的 path template 存在共同匹配输入时，manifest 整体拒绝。例如 `/accounts/{id}` 与 `/accounts/current` 不能同时声明；应合并为一个模板 owner，或先调整路由契约使其不相交。
- v2/v3 的 `owner` 与 `rollbackOwner` 必须都是 `node` 或 `go` 且互不相同；v4 pure-Go route entry 按 2.2 节不再包含 `rollbackOwner`。清单最多 2048 条，避免发布预检出现无界二次比较。
- schema 和每层对象都拒绝未知字段，拼写错误不能被静默忽略。

精确解析函数先查 allowlist，再回退 `routeOwners[surface]`。不规范或未列出的请求路径只回退粗粒度 owner，不能模糊匹配到 Go。该函数目前仅供校验和测试，不得被误写为已完成生产 dispatch。

## 4. 切换与回滚流程

精确切换前必须先证明目标 Go route 的契约、依赖、writer、前端调用和最小 smoke，并确保 Node 与 Go 对同一路由不会同时接收生产写流量。然后在新 `deploymentEpoch` 中增加 allowlist 项，`owner=go`、`rollbackOwner=node`，执行：

```powershell
pnpm test:owner-manifest
pnpm validate:owner-manifest
```

全 Node 发布预检使用 `pnpm validate:owner-manifest:node`，它同时检查四大粗粒度 owner 和所有精确项，不能被精确 Go 项绕过。

生成只读回滚候选：

```powershell
node scripts/validate-owner-manifest.mjs --print-rollback=rollback-2026-07-22-001 deploy/owner-manifest.json
```

命令只向标准输出打印一个新 manifest：交换粗粒度 current/rollback owner，并交换每条精确路由的 `owner/rollbackOwner`。它不写仓库、不发布、不修改代理。新 epoch 是必填项且不能与当前 epoch 相同，防止 owner lock 把回滚误认成同一代发布。

生产回滚仍须走发布预检、目标进程 readiness、反向代理原子切换和稳定性观察；生成 manifest 不是回滚成功证据。capability v2 启用后，`--print-rollback` 生成的简单 owner 交换稿只能用于审阅，不能直接发布；W11 前必须按 2.1 节生成包含 contract / epoch / schema before-image 的新 v3 rollback manifest，W11 pure-Go 阶段则必须按 2.2 节用 release target 生成 v4 rollback manifest。

## 5. 后续接入门禁

后续代理 dispatch 接入必须单独设计并满足：

1. 代理使用与本校验器一致的 method、path 解码和模板匹配语义，不能自行发明宽松 wildcard。
2. 启动前一次性验证 manifest、release、schema 和 deployment epoch，并从已验证快照构建不可变路由索引；不能在每个请求上重复执行模板相交的二次校验。任何解析差异 fail-closed。
3. 同一 method/path 只有一个 owner，写接口不能镜像双写；影子验证只能发送明确无副作用的读请求。
4. 切流和回滚各自有独立 epoch、readiness、请求探针、错误率观察和旧 owner 保留窗口。
5. Node 删除只能发生在精确路由已经稳定归属 Go、回滚演练完成且对应减法清单证据齐全之后。

## 6. 验证覆盖

`scripts/validate-owner-manifest.test.mjs` 覆盖 v1 结构兼容、v2 空/非空 allowlist、当前 Goose schema、粗粒度回退、普通模板与 Gemini action 模板命中、未知 method、unsafe wildcard、编码/非规范路径、surface 越界、owner 非法、重复/相交模板、GET/HEAD 精确匹配、全 Node 断言和自动回滚往返。

v3 实现时必须另增：v2/v3 dual-read bootstrap；canonical payload 顶层字段完整性；拒绝 `contracts.*`、单一 `producerSetDigest`、payload 内自引用 `manifestDigest` 和未知字段；RFC 8785 digest golden vector；Ed25519 purpose / trust root / key rotation / revoke / expiry / sequence replay；过期 activation 与新鲜 runtime authorization；producer inventory 首张 anchor、连续 revision / previous digest、registry set / active pointer CAS、sealed epoch 不可 reopen；source sealed + target `active_target_pending` 的 activation transition；backup / rollback / stop 只 seal current certificate set；replica ACK gap；artifact bytes 缺失 / digest / length / schema / signed location remap；1% / 5% / 25% live control、100% frozen 25% baseline；相邻晋级、普通一档降级和 hard failure 任意档直降 0。v4 另覆盖 pure-Go release target 和双向回滚。上述测试不存在或未通过时，v3 capability 激活与 W11 Node 删除分别保持阻断。
