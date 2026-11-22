# AI 账户上游接口能力设计

> **2026-11-21 五域退役修订（用户裁决「非对话域彻底去掉上游接口能力」）：** 「上游接口能力」自本修订起仅对话域（chat）账户持有。图像 / 音频 / 视频 / 向量 / 重排五个非对话域账户彻底去除该配置：前端不再展示、写侧不再校验与落库、`/v1` 派发不再按 endpoint mode 过滤——候选按「账户能力域 = 请求域」圈定，上游不支持时返回的错误原样透传，网关不关心也不限制。本文其余章节已按该口径重写；历史脉络（bridge 退役、向量/重排拆分）见文末修订记录。

## 1. 目标与非目标

**目标**：固定「上游接口能力」（endpoint mode 声明）在六值能力域中的适用边界——chat 域保留声明、校验与派发过滤；五域退役后域闸门成为唯一的候选域隔离防线；endpoint mode 词表继续作为健康检查 / 连通性探测的执行面词表存在。

**非目标**：

- 不改变 capability_domain 六值域本身（菜单、统计、调度偏好「无调度偏好」占位不动）。
- 不改变模型目录的 `supported_endpoint_modes_json`（模型协议面，另一体系）。
- 不删除 endpoint mode 词表（27 值）、探测规格（`ConnectivityProbeForModes`）、`health_check_endpoint_mode` 列与词表、`RequestSupportedEndpointMode`、exactkeyprobe——它们是健康检查与连通性探测的执行面，不是账户声明面。
- 不新增客户端请求限制配置，不展示派生的「可承接请求」列表。

## 2. 适用范围：chat 域专用声明

「上游接口能力」仅 chat 域账户持有，双层存储与运行时口径不变：

- 账户级：加密凭据 `credentials.supported_endpoint_modes`（历史字段名，现行配置面为 `account_endpoint_modes` 连接行）。
- 连接级：`account_endpoint_modes` 表行（endpoint_mode CHECK 27 值），运行时投影连接行优先、回落凭据键。

chat 域可声明集合 = 域 lane（13 值对话锚集）∩ 供应商写侧词表 × 域×供应商矩阵（`accountscore.AllowedEndpointModes` 权威）。chat 域账户的声明校验、缺省播种（`DefaultEndpointModesForDomain`）、PATCH 严格校验、批量编辑覆盖、模型映射右侧协议校验全部保留现状。

## 3. 五域退役（本批核心契约）

图像 / 音频 / 视频 / 向量 / 重排五域账户：

1. **前端**：创建 / 编辑 / 批量编辑表单不再出现「上游接口能力」勾选区；详情只读凭证不展示该行；导入协议页对五域不再声明枚举与回填口径。
2. **写侧**：创建不播种默认集、不做 ⊆ allowed 校验；PATCH / 批量编辑不再做声明校验、connections-only 变更不回写影子副本；凭据归一化不落 `supported_endpoint_modes` 键；连接行不写 `account_endpoint_modes`；克隆不透传 modes；连接 preset modes 组合校验仅服务 chat 域连接。
3. **导入**：native 导入文件中的 `supported_endpoint_modes` 仅用于 `DeriveDomain` 推域（历史文件兼容），落库时剥离凭据键、不写连接行 modes、不做五域 allowed 校验；modes 缺省 / 非法时沿用既有导入失败清单语义（不猜域）。
4. **派发**：五域账户运行时声明投影恒空；候选按域闸门圈定（§4），不再做 mode 级过滤；上游不支持该接口时返回的 4xx / 5xx 按真实上游失败透传（诊断、切号、计费口径不变）。
5. **存量清洗**：schema revision 30（`capability-domain-declaration-retirement`，`RequiredBeforeStartup=true`）删除五域账户凭据中的 `supported_endpoint_modes` 键与 `account_endpoint_modes` 中五域账户行，输出清洗报告。回滚安全：旧二进制读空声明 = 不受限，行为与目标一致。
6. **迁移 / 升级核验兼容（revision 30 清洗的直接推论）**：历史域步骤（revision 4 m2 迁移器、revision 29 v2 升级执行器）的扫描 / 核验按共享包 `capabilitydomain.DeclarationRetired`（gateway `EndpointModesDeclarationRetired` 同源委托，五域清单单一权威）把「落库域属五域 + 凭据缺 `supported_endpoint_modes` 键」识别为合法退役终态——不再判 `modes_missing`，域以 `capability_domain` 列为权威；五域源的授权实例继承源落库域，不再判 `instance_source_failed`。解密损坏仍阻断；键存在但格式错误（空数组 / null / 非数组 / 未知值）仍入失败清单（revision 30 自身核验同判键残留）；**chat 行凭据缺键仍判 `modes_missing`**（chat 声明契约保留，豁免不外溢）。m2/v2 共用判定入口为 maintenance `retiredDeclarationTerminal`。

## 4. 域闸门（新增候选隔离防线）

候选池 SQL（`chainCandidateWhere`）按分组圈定、不按域圈定；五域退役后 mode 过滤对五域不再生效，必须补显式域闸门防跨域漏调度：

- 请求域由请求形态派生（chat / responses / messages / gemini 族 → chat；images → image；语音 / 转写 / realtime → audio；video 族 → video；embeddings → embedding；rerank → rerank）。
- 候选 driver 过滤（`gatewayRequestCapabilityMismatchReasonFor` 链）按 `账户 capability_domain` vs 请求域匹配，不匹配拒绝（reason `capability_domain_mismatch`）。账户投影（`AccountCandidate` / `OpenAIAccountSecret`）携带 `capability_domain`。
- 切号冻结门（`FilterAccountsForSwitchTarget`）、演练场模型目录分类同步按域（演练场 v1.4 起按所选 Key 模型并集做六 kind 分类，与域闸门同口径）。
- chat 域请求仍按既有声明 mode 过滤 chat 账户（`endpoint_mode_unsupported` 语义保留）；chat-pinned 直取路径目标账户明确，不经域闸门。

## 5. 健康检查与连通性探测（执行面保留）

- 五域恒连接测试（`TestKindForAccount` 不变）；探测规格选择从「账户声明集 × preset 规格优先级」改为「域默认形态集（`LaneModesForDomain`）× preset 规格优先级」——audio 域仍按 preset 差异落 `audio_job_create`（qwen）或 `audio_speech`（volcengine / glm / gemini native / minimax）等。
- `health_check_endpoint_mode` 列继续按域默认落值（`DefaultHealthCheckEndpointModeForDomain`：chat=chat_sse、image=images_json、audio=audio_speech、video=video_create、embedding=embeddings_json、rerank=rerank_json），作为检查记录值；前端「检查请求形态」对五域按域默认派生展示。
- jobs `account-connectivity-health` 候选按域判定（五域恒连接，不看 modes）。
- 媒体准入 SQL 臂沿用 `health_check_endpoint_mode='video_create'` 域代理，不受本批影响。

## 6. 网关判定顺序（修订后）

1. 识别下游请求路径、JSON / SSE、模型和客户端画像；派生请求域。
2. 按 `sourceModel + sourceEndpointFamily` 查找启用的账号模型映射（chat 域语义不变；五域映射不再按声明校验，见 [AI账户模型映射选择规则](AI账户模型映射选择规则.md)）。
3. 候选过滤：先域闸门（账户域 = 请求域），chat 域内再按声明 mode 过滤。
4. 模型过滤（`supportedModels`）与既有可用性闸不变。

## 7. 验证

- 五域创建 / 编辑 / 批量编辑表单无「上游接口能力」；存量五域账户经 revision 30 清洗后凭据无该键、连接行无 modes。
- chat 请求候选不出现五域账户、媒体 / 向量 / 重排请求候选不出现 chat 账户（域闸门回归钉死）。
- chat 账户声明过滤不变：未启用目标 endpoint mode 的 chat 账户仍被 `endpoint_mode_unsupported` 过滤。
- 媒体 / 向量 / 重排请求在上游不支持时收到上游原始错误（透传），不产生网关侧 mode 拒绝。
- 五域连接测试按域默认形态探测成功；健康检查形态列值为域默认。

## 修订记录

- 2026-11-07：跨协议模型映射与 `Responses -> Chat` bridge 退役；接口能力门保留；`credentials.supported_endpoint_modes` 现行配置面为 `account_endpoint_modes`。
- 2026-11-21（向量/重排拆分）：`embeddings_json` / `rerank_json` 移出对话锚集，落独立 embedding / rerank 域（[AI账户能力域向量重排拆分设计](AI账户能力域向量重排拆分设计.md)）。
- 2026-11-21（五域退役，本批）：五域彻底去除声明 / 校验 / 过滤；新增域闸门；revision 30 清洗。见 §3/§4。
- 2026-11-21（dev 演练收口）：m2/v2 迁移核验按 §3.6 兼容五域退役终态（dev 库 postflight 19/19 全绿验证）；mockdata 造数对齐声明契约——chat 域 mock 账户凭据落声明键（首个 chat plan 形态集）、五域 mock 不落键。
