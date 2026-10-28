# 深挖复审台账：模型目录卡片化与详情抽屉设计

- 锚定范围：`docs/functions/模型目录卡片化与详情抽屉设计.md`（实现前设计契约）及其引用落点（`frontend/src/views/providers/` 5 文件 + 回归脚本 + `types/domain/providers.ts`）；不锚定 backend-go 实现（设计非目标为后端零变更，仅核对字段下发事实）。
- 发起：2026-10-28，主会话（用户显式要求"复查复查看看有啥细节没考虑清楚"）；授权：A 类即修权（设计文档即修），B 类过目门与 C 类裁决清单语义已向用户说明。
- 需求脉络：目标 = 供应商模型目录弹窗从 3000px+ 动态列宽表格改为卡片网格 + 详情抽屉；非目标 = 后端契约零变更、不做跨供应商聚合页、不做 DeepSeek 时段价、不做轮询刷新；可见行为 = 弹窗内目录浏览形态、卡片三价槽位、抽屉全分组渲染、管理面操作等价。
- 涉及文件：设计文档 1；前端 `ProviderModelCatalogModal.vue`、`ProvidersView.vue`、`providerModelTableState.ts`、`providerModelFormatters.ts`、`providerModelCategoryRules.ts`（只读核对）、`types/domain/providers.ts`、`scripts/regression/account-gpt-request-overrides-regression.ts`。
- 关联契约：[供应商独立计费与模型目录展示设计](../functions/供应商独立计费与模型目录展示设计.md) §5（分组生成语义权威）、`docs/architecture/frontend/样式规范.md`、`docs/architecture/frontend/响应式列表规范.md`、后端 `providers/catalog.go`（ModelCatalogItem 下发字段事实）。
- 项目裁决出处：根 `AGENTS.md`（文档先行/样式规范路由）、`AGENTS.md` 事件导航、`docs/待办与阻塞事项.md` #T66。
- 代码基线：master @ c7fcebea6；工作区已有其他会话脏文件（backend-go oauthmgmt、frontend/accounts、docs 若干）——均不在本锚定范围内，禁止触碰。
- 工作区边界：`frontend/src/views/providers/` 当前无未提交改动（本会话新增的设计文档除外）；无并行会话占用本对象域。
- 当前阶段 / 当前轮次：R1（维度制攻击+验证）→ 修复批 1 → R2 落点复核（31 项 30 落位 + 回归攻击 H44-H52）→ 修复批 2 已落。收敛协议未走满：按出口②呈报用户裁决提前收口。
- 收敛计数：0 / 3（R1、R2 均有确认项）

## 问题条目表（R1 收口后压缩）

R1（维度制攻击，4 攻击角色 46 条假设 → 5 验证角色独立证伪）：**confirmed 31 / refuted 8 / duplicate 1（H35→H33）**。确认项全部为设计文档契约缺口（对象尚未实施，无代码行为变更），按 A 类即修，修复批 1 已全部落入设计文档。原逐条假设表压缩为下表（依据细节见验证轮结论，修复落点见设计文档对应章节）。

| ID | 处置 | 修复落点 |
| --- | --- | --- |
| H1 | A已修 | §6.3 mode 读侧词表六值示例，注明 `image`→`image_generation` 派生 |
| H2/H28 | A已修 | §5.1+§5.2 卡片数值统一 `trimNumber` 口径，单位由槽位 label 表达 |
| H3 | A已修 | §5.2 档位徽标改 `supportedServiceTiers ∩ serviceTierPrices`（与抽屉 tier_* 同源） |
| H4 | A已修 | §5.2 容量行删 `maxTokens` 回退臂，仅 `maxOutputTokens` |
| H5/H6 | 已证伪 | H5：106-110 为覆盖式区间引用（106 是注释行）；H6：status 恒序列化且写路径词表封闭 |
| H7 | A已修 | §6.2 空投影归因改为「任何行投影为空」（含具名厂商 draft 行）+ unknown 行不对称声明 |
| H8 | A已修 | §6 卡片 keydown 须判别事件目标（参照实现无判别、不可照抄）+ §11.3 验收 |
| H9/H10/H11 | A已修 | §6 抽屉生命周期：列表变更即关抽屉；编辑/删除成功后关抽屉；loading 操作区空为现状继承 |
| H12/H13 | A已修 | §7 分页：四触发源回第 1 页 + 越界收敛 `max(1, ceil(total/pageSize))` |
| H14 | A已修 | §8 新状态归弹窗内部、关闭时重置（a-modal 未设 destroyOnClose） |
| H15 | A已修 | §8 网格区 `overflow-y: auto` 滚动接管 + 分页可达（最高影响项） |
| H16 | A已修 | §9 `a-drawer` `@after-open-change(false)` 回焦触发卡片 |
| H17 | A已修 | §8 状态优先级 loading > 错误 > 空 > 筛选空 |
| H18 | A已修 | §9 复用 `shared/clipboard.ts` `copyTextToClipboard`；§11.5 文案随现有工具 |
| H19/H20/H22/H23 | 已证伪 | H19：antd Drawer ESC stopPropagation 单层关闭；H20：弹层挂 body 不受裁剪且现状同形态；H22：Tab 重置属"行为不变"继承；H23：variant 可从文档推出 |
| H24 | A已修 | §2 目标 3 / §6.4 / §11.6 修正为仅编辑 / 删除 |
| H25 | A已修 | §5.1 单 Token 价合并规则（与后端 `usesSingleTokenPrice` 同口径） |
| H26 | 已证伪 | 「非 null/undefined」判据已覆盖 0 值；残留测试用例并入 H32 修复 |
| H27 | C1 | draft 行 nil mode 分类回退继承现状，进裁决清单 |
| H29/H30 | A已修 | §6.2 / §5.1 不对称与锚点价声明 |
| H31 | A已修 | §6 value 单元格 `overflow-wrap: anywhere` 继承 |
| H32 | A已修 | §9 单测补 0 价与极值用例 |
| H33/H34/H35 | A已修 | §9 补入 `frontend/scripts/regression/provider-model-formatters-regression.ts` 改写清单（高影响） |
| H36/H37 | A已修 | §9 第 60 行锚点声明 + 卡片模板 `modelPriceSlots` 接线断言改"必做" |
| H38 | A已修/C3 | §9 单测文件名落点；coverage 不含 views 属现状，进裁决清单 |
| H39 | A已修 | §9+§11.8 验收门补 lint，import/computed 清理要求显式化 |
| H40 | A已修 | §11.8 diff 基线口径定义 |
| H41 | A已修 | §8 响应式列表规范 §5 适用口径 + 移动端 0 跳→1 跳行为变化声明 |
| H42 | A已修 | §8 阴影用 `--juhe-shadow-card` 令牌 |
| H43 | A已修 | §11.2 长 ID 判据样本 |
| （自查） | A已修 | §8 ≤767px 媒体查询单列（auto-fill 在 560-767px 产生 2 列，与原文矛盾） |

## 覆盖矩阵（R1 维度制）

已查 | 未发现 | 理由 的格子（其余格子见上表编号）：

- 状态一致性：弹窗内状态（page/detailOpen/detailModel/keyword/category）的归属与重置已由 H14 覆盖；后端侧本设计零改动无新状态。
- 并发与时序：前端单用户浏览场景无并发写；reload 与抽屉交互已由 H9/H10/H11/H21 覆盖。
- 取消与中断：弹窗/抽屉关闭路径已由 H8/H9/H16/H19 覆盖。
- 资源与生命周期：无新增计时器/监听器（不采纳 portal 的 60s 定时器）；焦点生命周期由 H16 覆盖。
- 性能与放大：目录级行数量级（每供应商 ≤70 行）、卡片纯展示、无循环内重计算；分页 24/48/96 有界。未发现。
- 外部输入：目录数据经后端校验（负数拒绝、枚举封闭），前端仅展示；极值显示已由 H32 覆盖。
- 消费者契约：接口响应零变更（§2 非目标 1）；options 链与账户域不受影响（攻击 4 全量 rg 复核）。
- 回归与兼容：H33/H34/H41 覆盖主回归面；引用面全扫无其他遗漏。
- 测试有效性：H33-H39 覆盖门禁缺口。

## 扩散记录

| 轮次 | 新纳入对象 | 耦合类型 | 理由 |
| --- | --- | --- | --- |
| R1 | `frontend/scripts/regression/provider-model-formatters-regression.ts` | 共享文件（import 被删导出） | H33 发现的第二回归目录，设计改写清单原遗漏，已纳入 §9 |
| R1 | `frontend/src/shared/clipboard.ts` | 主题相似（复制能力复用） | H18 发现的现成工具，已纳入 §9 落点 |
| R1 排除 | `frontend/scripts/regression/` 其余脚本 | 主题相似 | 全扫确认仅 provider-model-formatters-regression.ts 引用被删导出（custom-provider-model-form-regression.ts 只用保留导出） |
| R1 排除 | backend-go 实现 | 非目标 | 设计非目标 1；仅只读核对字段下发事实 |

## 修复记录

| 批 | 内容 | 验证证据 | 落点复核轮次 |
| --- | --- | --- | --- |
| 1 | 设计文档 31 项 A 类修订（§2/§5.1/§5.2/§6/§7/§8/§9/§11，见问题条目表修复落点列） | R2 落点复核：30/31 真实落位，1 项（H18）落位但有 §5.2 残留矛盾 | R2 |
| 2 | R2 回归攻击 9 项（H44-H52）全部 A 类修订：§5.2 clipboard 残留回正、§6.4 关抽屉因果改为行为性契约（编辑内存替换不 reload）+ 新增保存后关抽屉、§5.1 数值口径改"不要求字符串相等"+ 删错误后缀断言、单 Token 合并限定按 catalogDisplay 投影判定、徽标 ∩ 补第三条件（价格集非空）、95-103 行号、src 全域测试惯例措辞、§11.2 验收回正 | 批 2 修订直接按 R2 逐条落点执行，未再派复核轮（出口②呈报用户裁决） | （未做） |

## R2 落点复核摘要

31 项修复落点逐条核：30 落位 / 1 落位但有残留（H44）；修复间交互 8 对：5 一致 / 1 冲突（关抽屉 vs reload 因果，H45/H46）/ 2 需登记（∩ 第三条件 H49、RowActions 阻断实现落点——已由批 2 前者修复，后者经 §6 keydown 判别 target 机制覆盖卡片层，无需改公共组件）；回归攻击 9 条（H44-H52），批 2 全部处置。

## R2 新增条目处置

| ID | 处置 | 修复落点 |
| --- | --- | --- |
| H44 | A已修 | §5.2 模型 ID 行 clipboard 表述与 §9/§11.5 对齐 |
| H45 | A已修 | §6.4 关抽屉改行为性契约，删错误 reload 因果 |
| H46 | A已修 | §6 首段 + §6.4 补新增保存后关抽屉 |
| H47 | A已修 | §5.1 + §11.2 数值口径改"不要求字符串相等" |
| H48 | A已修 | §5.1 删"抽屉媒体条目无单位后缀"错误断言 |
| H49 | A已修 | §5.2 徽标 ∩ 补"价格集非空"第三条件 |
| H50 | A已修 | §5.1 单 Token 合并限定按 catalogDisplay 投影判定 |
| H51/H52 | A已修 | §9 行号 95-103、测试文件"src 全域惯例"措辞 |

未验证项（R2 标注）：§5.1"31 个 audio 模型中 20 个只带字符价/秒价"为论据性数据断言，静态种子口径未直接复现，不影响行为契约；实施单测按真实目录数据覆盖即可自证。

## 裁决清单（C 类，持续积累）

| ID | 一句话 | 来源轮次 | 状态 |
| --- | --- | --- | --- |
| C1 | custom draft/disabled 行 nil mode 落 text 分类、媒体价不在候选序列致价格区隐藏 | R1 | **已处置（2026-10-28 补充实施）**：`modelPriceSlots` 增全字段回退序列（分类候选 0 槽时按 text→image→audio→video 补取），价格区不因分类误配隐藏真实价；设计 §5.1 已补契约、单测 +2 例（16→18）。根治分类误配本身仍属「mode 必填校验」立项 |
| C2 | 18 个内置种子行「有 tier 价但 `supportedServiceTiers=[]`」——档位价在卡片与抽屉都不可见 | R1 | **维持待立项（2026-10-28 补充取证后不可随批修）**：`supportedServiceTiers` 是网关硬门（`stream_route.go:250` 请求 `service_tier` 不在声明列表即拒绝），这些行的档位价当前不可达；补声明需逐模型官方取证（realtime/transcribe/tts/live 是否真支持 service_tier），且修对后网关会开始放行这些模型的分档计费请求——是计费行为变更，必须单独立项（数据源取证 + 行为裁决），不得由展示批顺手改数据 |
| C3 | vitest `coverage.include` 不含 `src/views`——新增单测不进覆盖率门 | R1 | **部分处置（2026-10-28）**：vitest.config.ts 与实际行为矛盾的注释已修正（执行覆盖 src/**/*.test.ts 含 views，覆盖率门槛只守逻辑域）；views 纳入覆盖率门维持不立项——95/90/95 门槛下纳入无测试的 Vue 组件会直接砸穿门槛，纳入属测试基建立项（先补组件测试再抬 include） |
