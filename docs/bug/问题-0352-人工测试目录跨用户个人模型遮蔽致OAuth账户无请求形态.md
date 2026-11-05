# BUG-0352 人工测试目录跨用户个人模型遮蔽致 OAuth 账户无请求形态

## 基本信息

- 编号：BUG-0352
- 状态：已修复待发布（2026-11-04）
- 严重程度：P2
- 发现时间：2026-11-04
- 发现方式：生产账户手动测试 400 反馈 + 生产库取证
- 模块：后端（gateway / accounts / accountstest 人工测试目录解析）
- 关联计划：无
- 关联 bug：BUG-0344（同属 `models` 单表 scope 可见性口径）
- 责任人：账户域维护者

## 问题概述

- 现象：生产账户 `acc_f6a60b47564ab148`（Grok OAuth，`xai/oauth`，`health_check_model=grok-4.7`）在管理页面手动测试时返回 400「账户上游接口能力中没有可用于连接测试的请求形态」，无法发起连接测试。
- 期望：人工测试的模型目录只来自「当前账户所有者作用域可见」的目录行（[架构总览](../architecture/架构总览.md) §账户作用域；[AI账户检查模型与人工测试设计](../functions/AI账户检查模型与人工测试设计.md) §5/§8）；其他用户的 personal 模型不得覆盖本账户可用的 builtin/global 目录行。
- 实际：测试目录候选的 custom 臂未按 `system_account_id` 过滤归属，另一用户的同名 personal 行以 personal 优先级（3）压过 builtin（1）胜出；该 personal 行只声明 `chat_json/chat_sse`，而 xai OAuth 的测试形态顺序只允许 `responses_sse/responses_json`，协议过滤后为空 → 400。
- 影响范围：所有「目录中存在其他用户同名 personal 行」的账户人工测试/连接测试解析；生产已确认至少 2 个 OAuth 账户同型——
  - `acc_f6a60b47564ab148`（xai/oauth，`grok-4.7`）：builtin 行 `provider_model_xai_grok_4_7_a7e7bc6abdc9` 声明 `["chat_json","chat_sse","responses_json","responses_sse"]`；另一用户 `sysacc_1782397535511_cc38ab2a` 的 personal 行 `mdl_24e4c9893c8df41f1217` 只有 `["chat_json","chat_sse"]`，账户所属用户是 `sysacc_1777763935822_2461196e`（两者不同）。
  - gpt OAuth 账户 `acc_1783225399456_c8736acd` 的 `gpt-5.5` 同型（他人 personal 同名行遮蔽 builtin responses 能力）。
  - 授权实例 POST /test：`FindManualTestContextRow` 返回来源账户 owner（COALESCE），但 `AccountForTestView.manualContext()` 取实例行 owner（被授权用户/grantee），授权实例仍会被 grantee 名下同名 personal 行遮蔽。
  - 已保存账户草稿测试：`prepareAccountDraftTestSnapshot` 按 `group.ownerAccountID` 推导 owner，管理员用他人 `groupId` 提交已保存账户草稿时，目录解析使用 group 主而非账户 owner，同样可被遮蔽。
- 边界：只影响人工测试的目录解析与请求形态判定（400 发生在派发前），不影响 `/v1` 生产流量路由；但同型 personal 行还会让测试选项出现「选项可见、点击即 400」的割裂。

## 复现步骤

1. 在 `models` 单表准备 builtin 行：`vendor_code='gpt'`、`public_model='gpt-4o-mini'`、`scope='builtin'`、`supported_endpoint_modes_json='["chat_json","chat_sse","responses_json","responses_sse"]'`、`catalog_visible=true`。
2. 以另一用户身份插入同名 personal 行：`scope='personal'`、`system_account_id=<other>`、`supported_endpoint_modes_json='["chat_json","chat_sse"]'`、`status='active'`。
3. 取一个属于 owner 的 OAuth 账户（`type='oauth'`，凭证无 `supported_endpoint_modes`，默认形态为 responses 双形态）。
4. 调 `FindManualTestOptionsContext` 后请求 `AccountManualTestModelCapabilities("gpt-4o-mini")`：修复前 `FindTestCatalogItem` 选中他人 personal 行（mode=chat、protocols 仅 `chat_completions`），形态过滤为空，返回 400「账户上游接口能力中没有可用于连接测试的请求形态」。
5. 等价自动化复现见 `TestBug0352ManualTestCatalogOwnerScope`（真实 PG 夹具）。

## 环境信息

- 分支 / 版本：当前工作区（Go gateway，未发布）
- 数据状态：生产 `models` 单表 builtin/personal 同名行并存；生产账户连接 `account_endpoint_modes` 只有 `responses_json/responses_sse`（DB 事实）；人工测试的形态集合读取解密凭据的 `supported_endpoint_modes`，该账户凭据未声明时按 OAuth 默认 responses 双形态
- 浏览器 / 系统 / Node 版本：与后端缺陷无关
- 是否稳定复现：是（生产库数据 + 真实 PG 定向测试均稳定复现）

## 根因分析

- 表象：OAuth 账户的所有者目录看起来「只有 chat 形态」，但账户本身明明是 responses-only。
- 真实根因：`internal/accounts/accountstest/options_service.go` 的 `CollectTestCatalogCandidates` custom 臂 WHERE 只有 `scope IN ('global','personal')`，未按 `system_account_id` 过滤归属（注释还写着「owner 窗口沿旧表语义不加」）；`FindTestCatalogItem` 也没有把 `systemAccountID` 传入。合并阶段 `TestCatalogScopePriority` 中 personal=3 > builtin=1，他人 personal 行无条件胜出。
- 为什么会发生：`models` 单表合并后，builtin/global/personal 三种 scope 共用一张表，旧 `custom_provider_models` 表「天然只有 custom 行」的前提不再成立；custom 臂只排除了 builtin，却没补上单表化后必须的 owner 归属谓词。同包 `ListCustomTestCatalogOptions` 与 `internal/providers/catalog.go` 的 `listCustomModelRows` 早已使用正确谓词 `((scope = 'global' AND system_account_id IS NULL) OR (scope = 'personal' AND system_account_id = ?))`，测试目录解析漏对齐。
- 相邻漏点：授权实例的 owner 在 `FindManualTestContextRow` 已解析为来源账户 owner，但 POST /test 的 `AccountForTestView` 仍用实例行 owner（grantee）；已保存账户草稿链的 owner 由 group 主推导，管理员用他人 groupId 时偏离账户归属。两条路径都把错误 owner 继续传给同一目录解析。
- 佐证：生产取证（builtin 行四形态、他人 personal 行 chat 双形态、账户 owner 与 personal owner 不同）；400 文案精确对应 `AccountManualTestModelCapabilities` 的 `len(modes)==0` 分支。

## 修复方案

- 修改点：
  - 目录解析收敛为单一 owner-scoped 数据源：`loadBuiltInTestCatalogRows` / `loadCustomTestCatalogRows` 分别承载 builtin 与 global/personal 臂的 WHERE、参数与排序，`loadTestCatalogRows` 按 builtin→custom 顺序合并；options 模式保持 selected 置顶/release date 排序，exact 模式保持旧 winner 行序。
  - `AccountManualTestOptionsWithTotal`（非 generic 分支）与 `FindTestCatalogItem` 共用 `loadTestCatalogRows`：列表路径与能力/单模型路径不再各写一套 SQL；owner 归属谓词只在 `loadCustomTestCatalogRows` 一处维护，避免再次漂移。
  - `ListBuiltInTestCatalogOptions` / `ListCustomTestCatalogOptions` 改为 loader options 模式薄包装，既有调用方语义不变；删除被取代的 `CollectTestCatalogCandidates`、`TestCatalogCandidate`、`scanTestCandidates` 及对应桥接别名/包装。
  - `internal/accounts/test_subdomain_bridge.go` 删除 `collectTestCatalogCandidates` 包装，保留 `findTestCatalogItem` 等转发。
  - 授权实例链：`AccountForTestView` 新增 `ownerSystemAccountID`；`FindAccountForTestView` 在 context 非空时取 `context.OwnerSystemAccountID`（来源账户 owner，实例行 owner 是被授权用户/grantee），context 为空时回落 `item.OwnerSystemAccountID`；`manualContext()` 使用该字段。修复授权实例 POST /test 仍拿 grantee owner 查目录、可被 grantee 同名 personal 行遮蔽的 blocker。
  - saved draft 链：新增 `prepareAccountDraftTestSnapshotWithOwner`，在 group owner 三级回落后、判空报错前应用非空 `ownerOverride`；`savedAccountDraftTestSnapshot` 传入 `account.OwnerSystemAccountID`。group 只影响路由归属，账户归属是权威；`images_json` 的目录证据在 prepare 内部即按账户 owner 解析，不再依赖快照返回后的外层覆盖。
- 行为影响：人工测试目录解析恢复「当前所有者作用域可见」契约；本人 personal 行仍按优先级覆盖 global/builtin（回归测试第二臂钉住）；其他用户 personal 行不再影响本账户。列表展示形态与能力解析形态由同一 loader 产出，钉住「列表可见 = 能力可测」。授权实例 POST /test 与已保存账户草稿测试（含 `images_json` 证据）均以来源/账户 owner 解析目录，不再受 grantee/group 主 personal 行影响。
- 发布异常处理：纯读路径过滤修正，无 schema/配置变更；回退镜像即恢复旧行为（不涉及数据修复）。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 格式化 | 改动 Go 文件 | `gofmt -w`（7 个文件） | 无 diff | 无 diff | 通过 |
| 构建验证 | gateway 全量构建 | `go build ./...`（`backend-go/projects/gateway`） | 通过 | 通过（无输出） | 通过 |
| 静态检查 | gateway 全量 vet | `go vet ./...`（`backend-go/projects/gateway`） | 通过 | 通过（无输出） | 通过 |
| 回归验证 | BUG-0352 四组回归 + W14B/W13G/路由/草稿定向集 | `JUHE_AI_TEST_EXTERNAL=1 go test ./internal/accounts ./internal/accounts/accountstest -run 'TestBug0352\|TestW14B\|TestW13G\|TestCreateTestTask\|TestTestOptions\|TestTestDraft' -count=1` | 全部通过 | `ok .../internal/accounts 187.196s`、`ok .../internal/accounts/accountstest 0.282s [no tests to run]` | 通过 |
| 全包回归 | accounts 全包顺序/total/limit 无回归 | `JUHE_AI_TEST_EXTERNAL=1 go test ./internal/accounts -count=1 -timeout 20m` | 全部通过 | `ok .../internal/accounts 833.193s` | 通过 |
| 反向验证 | 统一 loader owner 谓词修复前必红 | 临时把 `loadCustomTestCatalogRows` 的 owner 谓词还原为 `scope IN ('global','personal')` 后运行 `go test ./internal/accounts -run '^TestBug0352ManualTestCatalogOwnerScope$' -count=1`，随后按备份恢复（sha256 一致） | 第一臂失败 | `--- FAIL ... 目录解析被跨用户 personal 行遮蔽（修复前必红）：{Model:gpt-4o-mini Mode:chat SupportedAPIProtocols:[chat_completions]}`，恢复后 PASS | 通过 |
| 反向验证 | 授权实例 owner 修复前必红 | 临时把 `manualContext()` 还原为 `v.item.OwnerSystemAccountID` 后运行 `go test ./internal/accounts -run '^TestBug0352AuthorizedInstanceTestViewUsesSourceOwner$' -count=1`，随后按备份恢复（sha256 一致） | 断言失败 | `--- FAIL ... manualContext 应使用来源账户 owner（修复前为 grantee）：sysacc_...`，恢复后 PASS | 通过 |
| 反向验证 | saved draft images ownerOverride 必红 | 临时移除 `prepareAccountDraftTestSnapshotWithOwner` 的 ownerOverride 覆盖后运行 `go test ./internal/accounts -run '^TestBug0352SavedDraftImagesEvidenceUsesAccountOwner$' -count=1`，随后按备份恢复（sha256 一致） | 断言失败 | `--- FAIL ... saved draft images 证据应按账户 owner 成功（修复前按 group 主拒绝）：检查模型未被模型目录证实支持 Images API`，恢复后 PASS | 通过 |
| 真实上游探测 | 生产 Grok OAuth 凭据真实 responses 探测 | 本机读取生产账户密文（仅内存解密，不写库/不刷新），经生产 sing-box 代理对 `cli-chat-proxy.grok.com/v1/responses` 发起 `grok-4.7` 探测 | HTTP 200 且协议完成 | `success=true status=200 message="OpenAI Responses 测试通过" durationMs=6785`（凭据 `expires_at=2026-11-04T18:29:24Z`，测试时未过期；临时凭据文件与探测脚本已删除） | 通过 |
| 功能验证 | 生产账户手动测试 | 发布后对 `acc_f6a60b47564ab148`（grok-4.7）与 `acc_1783225399456_c8736acd`（gpt-5.5）执行 POST /test | 不再 400，能进入任务/探测 | 待发布后执行（T122） | 未执行 |

- `TestBug0352ManualTestCatalogOwnerScope` 第一臂：他人 personal 行（chat 双形态）存在时，`FindTestCatalogItem` 必须返回 builtin 行的 `Mode=="responses"` 且 protocols 含 `responses`——修复前 personal 优先级胜出返回 `chat`，该断言必红；`AccountManualTestModelCapabilities` 必须同时返回 `responses_sse`/`responses_json`，修复前返回上述 400 文案。同一测试还断言 `AccountManualTestOptions` 返回的 `gpt-4o-mini` 形态集合与能力接口完全一致，钉住列表/能力共用单一 loader。
- 第二臂：把该 personal 行归属改为 owner 后重新选中 personal 行（`Mode=="chat"`、protocols 仅 `chat_completions`），证明归属过滤没有误伤本人个人模型。
- `TestBug0352AuthorizedInstanceTestViewUsesSourceOwner`：真实 authz 授权链（`newAuthorizedTestEnv` + `authz.Store.Create` + `seedAuthorizationInstance`）下，实例行 owner 为被授权用户、`FindManualTestContextRow` 来源 owner 为源账户用户；断言 `view.ownerSystemAccountID` 与 `manualContext().OwnerSystemAccountID` 均为来源 owner，修复前为 grantee。
- `TestBug0352SavedDraftUsesAccountOwnerNotGroupOwner`：管理员用归属他人的 group 提交已保存账户草稿；断言 `draft.OwnerSystemAccountID` 为账户 owner（修复前为 group 主），且 `manualTestCapabilitiesContextFromDraft` 的目录能力仍含 `responses_sse`/`responses_json`（证明目录解析使用账户 owner 的 builtin 行，而非 group 主的 chat-only personal 行）。
- `TestBug0352SavedDraftImagesEvidenceUsesAccountOwner`：管理员跨属主 group 提交 `images_json` 草稿；builtin 同名行声明 `images_json`、group 主 personal 行只有 `chat_json`。断言 draft 成功且 owner 为账户 owner（修复前 prepare 内部 images 证据按 group 主命中 chat-only 行，报「检查模型未被模型目录证实支持 Images API」）。

## 复发记录

- 时间：无
- 环境：无
- 现象：无
- 关联处理：无

## 下次遇到

- 先查什么：先确认 `models` 单表中同名 `public_model` 是否同时存在 builtin/global/personal 行，并核对 personal 行的 `system_account_id` 是否等于账户 owner；再看人工测试目录解析的 custom 臂谓词是否带归属过滤。
- 重点看什么：`FindTestCatalogItem` 与 `AccountManualTestOptionsWithTotal` 共用的 `loadTestCatalogRows`（owner 谓词在 `loadCustomTestCatalogRows`）；`TestCatalogScopePriority` 的 personal=3 > builtin=1；OAuth 的 `AccountTestEndpointModeOrder` 只给 responses 双形态，任何 chat-only 目录行都会让形态集为空。
- 如何避免误判：不要把「选项列表能看到模型」当成「能力解析也会命中同一行」——两条路径现在共用 `loadTestCatalogRows` / `loadCustomTestCatalogRows`（owner 谓词只在该 loader 一处）；本次修复前列表走 `ListCustomTestCatalogOptions`（已有归属过滤）、能力走 `CollectTestCatalogCandidates`（缺失归属过滤），正是这种双实现导致的漂移。

## 完成总结

- 完成时间：2026-11-04
- 结论：代码已修复并通过真实 PG 定向回归；待随发布上线后在两台生产 OAuth 账户执行手动测试验证（T122）。
- 后续建议：发布后在 `acc_f6a60b47564ab148` 与 `acc_1783225399456_c8736acd` 各执行一次 POST /test，确认进入任务/探测并成功返回；确认后删除 T122。
