# AGENTS.md

## 根文档职责

- 本文件只承担项目级导航、核心业务边界和高频事件入口，不复制专题文档正文。
- 具体架构、功能、前后端实现、测试、问题、重构、迁移和报告规则以 `docs/` 下对应权威文档为准。
- 本文件不自动触发生产部署或线上运维流程；只有用户主动提出生产操作并明确提供适用资料后，才读取和执行对应范围内的外部操作规范。
- 后端为 Go 三项目（`backend-go/projects/{gateway,jobs,maintenance}`）；原 Node 后端已于 2026-09-05 完成全量迁移并清零，2026-09-30 Node 足迹清理后仓库内不再保留归档（git 历史可溯），不得恢复，运行事实以 Go 实现为准。现行生产形态为国内单机 Docker go-only（服务器与凭据见 `.local/project-resources/prod/assets/国内单机-103.36.63.105.md`，部署配置在 `docker/single-server/`，运维入口见 `.local/project-resources/prod/runbooks/国内单机Docker部署与运维.md`）；PG/Redis 为云机本机 compose 容器，出海代理隧道为用户自管外部资产；旧 Mac + K3s + Edge 混合形态已下线，平台配置仓 `F:\k8s` 中 juhe-ai 现役配置已清理（历史报告类文档保留），残留核查另行任务。

## 文档与代码一致性

- **文档优先于代码**：文档是第一生产要素，承载项目当前的架构、契约与行为事实；代码是文档的实现。任何情况下不得让文档与代码各自演化。
- **始终一致**：任何改变可观察行为、契约或部署事实的变更（接口、字段、存储、流程、任务、配置、部署），必须在同一次交付内同步更新对应权威文档；文档未同步不得宣称任务完成。
- **文档先行**：新功能、新契约先落文档（设计、契约、验收标准）再实现；交付时若行为与文档不符，按文档回正代码。
- **冲突不静默选边**：发现代码与文档不一致时，先取证定位偏差方向；文档过时或有误则先修正文档并说明，再调整代码——禁止在无文档依据的情况下改变行为，也禁止保留与文档相悖的实现。
- 各领域权威文档入口见"事件导航"；部署域的同步细则见下文"部署变更文档同步"。

## 项目定位

- 这里是 `juhe-ai`，定位为轻量级 OpenAI 兼容中转与账号管理项目。
- 前端使用 Vue 3 + TypeScript + Ant Design Vue；后端使用 Go：`gateway`（管理面、公开面与 `/v1` 网关链主入口）、`jobs`（后台任务与探针/统计/retention 任务族）、`maintenance`（schema/seed 一次性 CLI）。
- 当前启用 OpenAI 供应商，支持 OpenAI OAuth 与 OpenAI API Key 两种账户创建方式；其他供应商保留架构扩展空间。

## 核心业务边界

- 当前项目事实以 `docs/architecture/架构总览.md` 和 `docs/functions/README.md` 为准，历史计划不替代当前架构和功能文档。
- 路由层级固定为 `API Key -> 路由策略 -> 分组 -> AI 账户 -> 供应商 / 协议能力`。
- API Key 只绑定路由策略；路由策略负责路由模式和分组绑定；分组协调 AI 账户；AI 账户和供应商管理真实上游语义。
- 普通路由只绑定一个分组；混合智能、权重、故障回退和轮询规则由策略路由维护。
- 客户端画像由网关内部自动识别，不作为 API Key、路由策略、分组或普通 AI 账户的用户配置项。
- 跨协议转换属于混合供应商账户能力，不写成 API Key 或路由策略里的显式协议桥接规则。

## 本地管理页面测试

- 需要通过项目管理页面执行 AI 账户、模型、探针或其他本地联调测试时，默认启动隔离的 Go 后端开发实例（`backend-go/projects/gateway` 的 `juhe-ai-gateway`，需 schema 时先用 `backend-go/projects/maintenance` 的 `juhe-ai-maintenance --ensure-schema/--seed` 初始化隔离库）并使用开发自动登录；不得把截图、识别或人工输入验证码作为默认测试步骤。
- 隔离后端至少设置 `JUHE_AI_DEV_AUTO_LOGIN_USERNAME=admin` 和 `JUHE_AI_AUTH_CAPTCHA_DISABLED=true`（Go gateway 支持这两个变量），并为业务库、用量库、统计库、日志和运行时文件设置独立临时目录，禁止污染现有开发数据。
- 隔离前端通过 `VITE_JUHE_AI_BACKEND_TARGET` 和 `VITE_JUHE_AI_GATEWAY_BASE_URL` 指向该隔离后端；使用未占用的新端口，不停止或复用用户已经运行的前后端进程。
- 浏览器打开前必须完成隔离预检：确认后端健康检查指向本次新端口，`/auth/me` 已返回配置的开发账户，且 `/auth/captcha` 返回 `required: false`；任一检查失败都必须先修复启动配置，禁止把登录页当作目标页面继续操作。
- 端口选择必须以实时监听检查为准；默认端口被占用时自动选择未占用的新后端端口和前端端口，并同步更新 `VITE_JUHE_AI_BACKEND_TARGET`，不得复用已有服务、抢占端口或停止用户进程。
- 只有自动登录功能本身就是被测对象，或自动登录经上述预检确认不可用时，才允许测试登录流程；仍不得尝试破解验证码。
- 测试凭据只能从用户明确指定的本地文件或当次消息读取，不写入源码、文档、日志或测试产物；输出、审计检查和最终报告必须脱敏。

## 部署变更文档同步

- 凡涉及部署行为的变动，必须在**同一次交付内**同步更新用户部署文档，未同步不得宣称任务完成；交付说明中列出已同步的文档清单。触发范围包括但不限于：
  - `docker/single-server/` 下 `compose.yml` / `Caddyfile` / `Dockerfile.runtime` 的任何变更（working_dir、卷挂载、健康检查、端口、镜像参数等）；
  - 环境变量契约变化：新增、删除、改名、默认值、必填性或取值约束（gateway/jobs/maintenance 全部进程）；
  - 运行时目录/路径契约、进程工作目录、跨进程交接路径（如 gateway↔jobs 的 usage-record-spool 同源要求）；
  - 初始化与迁移序列（maintenance 命令、schema 预置）、部署/更新/回滚/验证步骤；
  - systemd/裸进程直跑等非 compose 形态的差异。
- 同步去向：总览入 `docs/deploy/部署指南.md`，配置契约权威入 `docker/single-server/README.md`，直跑差异入 `docs/deploy/linux/README.md`；HTTPS、代理等专题入 `docs/deploy/` 对应子目录。
- 只影响当前实例的私有运行事实（服务器路径、密钥留档、当前 env 全集、发布记录）同步到 `.local/project-resources/prod/`，不进公开文档；公开/私有边界以"可复用规则进 docs、实例事实进 .local"划分。

## 私有环境资源

- `/.local/` 是当前工作区的私有环境资源根目录，已由 `.gitignore` 忽略；其中的任何文件都不得 `git add`、提交、打包进发布产物或复制到 `docs/`。
- 开发与生产资料统一放在 `.local/project-resources/`，仅供本机维护者和 Agent 在用户授权的范围内读取：

  ```text
  .local/project-resources/
  ├── dev/                 # 本地开发资源（远端 192.168.1.203 开发库 + 独立环境变量）
  │   ├── env/             # shared.env / standalone.env / server.env 等；*.example 是模板
  │   ├── database/        # 开发 PostgreSQL / Redis 隔离约定
  │   ├── runbooks/        # 开发环境初始化与验证、数据库生命周期
  │   ├── logs/            # 开发运行日志（按需生成）
  │   └── runtime/         # 隔离实例运行时文件（按需生成）
  └── prod/                # 生产资料：国内单机 Docker 103.36.63.105（唯一形态）
      ├── README.md         # 范围、导航与当前待办
      ├── assets/           # 服务器资产台账（含 SSH 密钥）
      ├── database/         # 单机 PostgreSQL/Redis 事实与密钥留档
      ├── issues/           # 当前形态生产问题记录
      ├── logs/             # 日志查询口径
      └── runbooks/         # 国内单机 Docker 部署与运维（唯一手册）
  ```

- `dev` 使用远端 `192.168.1.203` 的 PostgreSQL 数据库 `juhe_ai_sub2api_dev`、专用登录角色 `juhe_ai_sub2api_dev_app`，以及 cache/state/queue 三个 Redis 实例各自的 DB `9`；统一 namespace 是 `juhe-ai:dev`。真实连接配置在 `dev/env/shared.env`，初始化和加载入口在 `dev/runbooks/`。
- `F:\juhe-ai-public-welfare\.local` 仅供目录设计参考；当前项目不读取或复用其中的数据库、Redis、连接串或命名空间。
- `prod` 只保存当前唯一生产形态（国内单机 Docker，`103.36.63.105`）的私有资料：资产台账 `assets/国内单机-103.36.63.105.md`（含 SSH 密钥）、数据库事实 `database/国内单机-postgresql-redis.md`、唯一运维手册 `runbooks/国内单机Docker部署与运维.md`。旧 Mac + K3s + Edge 形态资料已于 2026-09-26 清理，清理前全量备份在 `.local/archive/project-resources-pre-cleanup-20260926.tar.gz`（确认无用后可删）。单机形态的变更通过 `docker/single-server/` 配置 + 上传发布；在当前任务获得明确授权时，允许在目标服务器上以受控方式执行限定的数据修复、回填和向前兼容的加法式 schema 变更（执行前先备份）。不允许把 Secret、env、logs、releases、backups 数据复制到本目录之外的任何位置。
- 操作 `.local` 后必须验证 `git check-ignore -v --no-index .local/<探针路径>` 命中忽略规则，并确认 `git ls-files -- .local` 没有输出。

## 事件导航

| 事件 | 必读入口 |
| --- | --- |
| 文档结构、新增文档、重命名或引用调整 | `docs/README.md` |
| 项目定位、模块边界、数据关系或网关主流程变化 | `docs/architecture/架构总览.md` |
| 新功能、字段、接口、存储、脚本或关键流程 | `docs/architecture/功能开发指导.md` 和 `docs/functions/README.md` |
| 前端页面、布局、样式、交互、文案或品牌 | `docs/architecture/frontend/README.md` |
| 后端接口、存储、网关、后台任务或队列 | `backend-go/README.md` 与 `docs/architecture/Go三项目架构基线.md`；后端实现专题见 `docs/architecture/backend/README.md`（历史） |
| 需求计划、执行进度或关联文档 | `docs/plans/README.md` |
| 本地安装、运行、联调、测试或验证 | `docs/develop/README.md` |
| 部署相关变动（compose/env/参数/运行时目录/初始化与验证步骤） | `docs/deploy/README.md` 与 `docker/single-server/README.md`（同步约束见上文"部署变更文档同步"） |
| bug、异常、测试失败或数据不一致 | `docs/architecture/问题修复指导.md`，必要时记录到 `docs/bug/README.md` |
| 大文件拆分、职责调整或重复逻辑收敛 | `docs/architecture/大文件重构指南.md`，复盘记录到 `docs/refactors/README.md` |
| 压测、性能分析、容量或验证报告 | `docs/reports/README.md` |

## CodeGraph 与 RTK

- CodeGraph MCP 可用于查询跨模块依赖、调用链和影响范围；其结果必须以当前源码、`rg`、未跟踪文件和刚修改文件复核。
- 对只读且输出量大的命令，优先使用匹配的 `rtk` 子命令：`git`、`rg`、`log`、`diff`、`test`、`mvn`、`npm`、`pnpm`、`read`、`find`、`ls`、`tree`。未列出的只读命令先用 `rtk rewrite "<command>"` 或 `rtk --help` 核实；写操作和精确排障使用原生命令。
- 只有工具注册表或 `--help` 未列出目标命令时，才能判定该命令不存在；其他工具错误保留原始输出，不得归因于能力缺失。
- 安装、配置修复、初始化或修复索引、健康检查、升级审查和回滚使用全局 `$agent-toolchain`；不得在日常开发中自行安装、升级、重配或维护工具链。

<!-- aoci:begin -->
## AOCI 仓库认知

AOCI 为本仓库维护一个稳定、可版本化、可增量更新的仓库级认知层，供模型跨任务复用对系统的理解。

`aoci.txt` 是面向模型的结构化认知索引。它以每个受管理文件、数据库表或其他受管理对象一条独立 Entry 的方式，用符号标签与 F/R/A/S 语义表达对象的核心职责、重要关系、对外契约，以及理解或修改系统时必须知道的非显然约束和设计决策。

Header、目录段和全部 Entry 共同组成完整仓库索引，可以覆盖前端、后端、配置、数据库结构及其他受管理内容。受管理内容发生变化时，通常只需维护受影响的认知条目，不需要重新生成整个索引。

AOCI 提供系统架构、对象职责、重要关系、对外契约和关键约束的高密度视图。

### 工作原理

AOCI 采用“模型生成、模型读取”的认知闭环。

Header、Entry 和 Curation 语义的创作只按当前机器签发的 Plan 与实时 Guide 执行；由 Host 模型基于当前绑定证据独立完成。

Entry 的语义必须来自模型对真实证据的理解。不得仅依据路径、文件名、扩展名、AST、符号列表、依赖扫描、正则、固定模板或规则引擎推导、预填、拼接或改写索引语义。

对 Fresh Bootstrap，只按当前机器签发的 Plan 和实时 Guide 执行。当它们要求创作时，Host 模型创作 Root、Meta、Tag 和 F/R/A/S，提供 authoring-run 声明，并把它绑定到 Plan、Evidence 与完整 Candidate。不得要求 AOCI 填写 `origin=host_model`、制造 Receipt 或把程序生成的 Framework 当作语义。本文件不自行重建 Onboarding 流程。内部批次不是用户决策；只有遇到既有批准边界或真实的安全、漂移、CAS、Recovery 条件才停止。

### 最小使用入口

- `aoci_rules`：取得当前AOCI版本的会话运行合同。
- `aoci_overview`：建立或恢复本仓库的完整认知。
- `aoci_maintain`：受管理对象达到最终稳定状态后检查认知是否需要维护。
- `aoci_update_entry`：提交与当前证据和源码摘要绑定的完整语义更新批次。
- `aoci_report`：仅当当前布局和工具状态支持时，在证据不足、无法可靠生成语义时登记待办，不猜写。

其他MCP工具、CLI命令、参数和专项流程，以当前工具说明、Guide和 `--help` 返回内容为准，不在本文件中重复完整手册。

本区块只规定仓库接入、认知使用和收尾原则。`aoci_rules` 承载当前会话合同，Guide实时输出承载当前Plan的执行顺序与停点，工具Schema、Spec和Validator承载机器结构与判据；Prompt、Description、README和静态文档不能覆盖这些机器事实。

### 建立、生成和恢复认知

1. 每个新的 Agent Run 开始时，应先判断：

   - 本仓库是否已经存在可用的完整AOCI索引；
   - 当前上下文中是否已有与本仓库根、当前索引版本和当前AOCI服务相匹配，并且模型仍可可靠使用的完整仓库认知。

2. 仓库已经存在可用的完整索引，但当前Run没有可靠完整认知时，先调用 `aoci_rules`，再调用 `aoci_overview`。

   完整认知仍可靠时直接复用。局部不确定本身不要求机械重读系统全貌。

   本Run从已知Host上下文压缩恢复时（包括宿主注入的压缩摘要），必须把此前模型认知视为不可靠。压缩handoff不得保留或摘要正式Whole-Index，也不得保留或摘要任何Overview Header、Entry、Chunk、Challenge或Attestation正文；只能保留安全续接所需的receipt身份、未完成write或Recovery状态，以及立即重载指令。复制进handoff的Whole-Index语义或receipt不能证明恢复后模型的当前认知可靠。若当前上下文已无法可靠保留运行合同，先调用 `aoci_rules`。继续业务任务前，使用 `refresh_reasons=["context_compaction"]` 和新的 `refresh_event_id` 调用普通完整Whole-Index `aoci_overview`（不设置 `check_only` 或设为false）；不得使用 `check_only` 或认知probe。原样跟随每个 `next_cursor` 直到 `completed=true`，确认交付，并且只基于新交付正文提交一次Attestation。完成这次新的完整传输后，即使Attestation为partial或fail也消费该generation，并按既有合同继续source-bound任务，不再自动调用第二次Overview。

   AOCI可以针对 `context_compaction`、项目 `cognition_refresh_threshold` 下的机器 `semantic_threshold` 或主要 `phase_transition` 提供checkpoint与认知状态事实。只需要这些紧凑事实时使用 `check_only=true`；这些事实只向Agent提供建议，不替模型决定是否需要系统全貌。

   Agent显式调用普通 `aoci_overview`（未设置 `check_only` 或为false）时，只要能形成一致的CognitionSet，AOCI必须完整交付请求scope。不得因为已有receipt、阈值未达到或没有待处理刷新原因而抑制正文。正式认知Dirty或Stale时仍交付正文，但必须标记不可靠。存在未决恢复或无法形成一致snapshot时失败关闭，不返回混合正文。

   普通Overview返回 `continuation_required=true` 时，必须原样提交 `next_cursor` 并自动继续到 `completed=true`。不得询问用户、开始业务任务或给出阶段性系统结论。Host截断、缺块、重复、乱序、cursor失败、Index变化或`chunk_tokens`变化时停止本次认知链。Attestation完成前不得用Memory、源码、Spec、`aoci.txt`、历史会话、scope、search或Entry读取修补或补充Whole-Index认知。Challenge ordinal是正式Entry序列中的1-based位置；Header内容、注释、空行、Section/Overview/Chunk Marker、Receipt与Metadata均不计数，Chunk Receipt ordinal使用同一序列。Attestation必须原样回绑本次Challenge发布的当前`index_sha256`、`entry_sequence_sha256`与`entry_count`；旧Index、旧Entry序列、旧数量或旧Attestation均无效。完整链结束后只正式提交一次既有模型认知Attestation；同一响应只允许一次不改变语义答案的JSON Schema或字段格式修正。对象、Tag或F不匹配即失败且认知吸收不确定，不得语义重试或旁路补答。首次认知失败时还不得执行Root/Meta、Migration、全局布局或其他未重新绑定的系统级决策。上下文压缩刷新若传输完整、认知身份不变、治理对齐且没有Recovery或第三方冲突，即使Attestation为partial或fail也消耗该refresh generation，并继续原任务，不再自动重读Overview。`system_mastery_percent`只自评系统框架——架构、职责、强关系、稳定外部契约以及高熵安全和维护约束——不表示完整实现或运行实况知识；机器索引覆盖率必须分开。默认只向用户输出由本次真实覆盖率、Challenge、块数、Token和掌握度生成的规定成功或失败一句话。Host截断时提示用户把 `overview_delivery.chunk_tokens` 设置为更小的合法值后重新开始，不得自动修改。

   加法认知等级必须与严格证明字段分开解释。`delivery_verified`表示已加载Index且Host交付已确认，但完整认知验证仍未完成；应表达为“已加载且交付已验证”，不得描述为“没有认知”或“没有理解系统”。`cognition_verified`要求Attestation通过（Challenge至少80%的ordinal完全正确且对象身份至多失手一处），`cognition_governed`还要求治理对齐。通用完整读取失败句只用于真实交付故障。

   当Overview响应包含可选`cognition-state/v2`投影时，必须分别解释各维度。其Level止于`model_cognition_usable`；`strict_attestation_verified`、`governance_aligned`与`current_system_cognition_reliable`都是独立状态，绝不参与该Level。ordinal、对象身份、Tag或核心F不匹配可以导致严格Attestation失败，而模型认知仍然可用；不得仅凭这种不匹配就宣称模型没有理解系统。只有`current_system_cognition_reliable=true`允许无保留地声称当前完整系统认知可靠。投影缺失时继续使用上述Legacy解释。

   普通的只读审计、分析、检查、不修改代码或不提交、不push，不自动等于严格零写入，也不改变上述认知有效性判断。Codex Memory和历史Skill只能辅助恢复经验、用户偏好与调查方向，不能替代与当前仓库根、索引摘要、AOCI服务身份和认知范围匹配的当前认知收据；项目AGENTS和当前AOCI身份在AOCI状态上优先于历史Memory。

   只有用户明确禁止Ledger、元数据、`.aoci`运行资产及任何文件写入时，才按严格零写入处理。若必要的认知建立与该边界冲突，必须报告冲突并请求用户裁决或建议使用隔离副本，不得静默以Memory替代当前仓库认知。

3. 仓库没有可用的完整索引，或当前只有最小骨架、Header不完整、Entries未完成、必要Curation尚未裁决时，如果需要建立正式完整AOCI索引，先取得 `aoci_rules`，然后进入当前AOCI Guide。由Guide依据仓库真实状态决定下一阶段并完成必要安全步骤。

   `aoci_maintain` 不替代索引建立流程。

   不在本文件中自行重建或硬编码完整索引生成状态机。

4. 在长程任务中，模型负责保留当前认知收据并正确使用刷新门禁：

   - Host报告上下文压缩或模型已知系统全貌丢失时，执行上述强制 `context_compaction` 重载规则；AOCI不能自行推断Host事件；
   - 进入真正的主要阶段时声明 `phase_transition`，不得把函数、测试运行或小步骤当作阶段；
   - 在有用的稳定检查点通过 `check_only=true` 取得机器语义计数；
   - 除已知压缩的强制重载外，由Agent判断当前任务是否需要再次显式获取指定scope或完整Overview；
   - 在维护和对齐完成前，保留AOCI报告的Dirty或Stale可靠性状态。

### 任务收尾与认知维护

5. 纯只读问答、分析、版本核验，或没有产生受AOCI管理对象变化的任务，不需要调用维护工具。当前AOCI版本是任意`aoci_overview` check_only或`aoci_maintain`响应里的`cognition_receipt.mcp_service_version`；二进制路径是项目`.mcp.json`里的`command`，CLI不必在PATH上。

6. 发生受AOCI管理对象变化时，待其达到本次任务的最终稳定状态后，只调用一次 `aoci_maintain`。不要在每次中间修改后逐文件维护。

7. 若维护结果返回真实语义候选，Host 模型必须基于每个候选绑定的对象和必要证据，独立创作完整标签与F/R/A/S更新。通过 `aoci_update_entry` 一次提交当前机器签发批次的完整候选集合，同时原样保留每项 `source_sha256`、`candidate_id` 与对应domain批次身份。`max_entries`只限制单次请求和原子事务，不限制logical plan、Whole-Index或Managed Scope。`remaining`非零时，在当前批次成功Apply后重新调用Maintain并从新preimage继续；绝不能为满足transport上限缩减Index覆盖或自行截取返回批次。

   没有足够证据且当前布局支持 `aoci_report` 时，使用它而不猜测、套用模板或为消除待办而生成缺乏证据的认知。

8. 必须遵守工具返回的结构化状态和安全边界：

   - `repair_required`：只修复明确命中的候选，再重新提交当前机器签发的完整批次；
   - `stopped`：结束当前写入尝试并检查 `failed_step`、错误、正式写入证据与Recovery。auto模式下，已证明零写入则记录closure并重新Plan；完整Intent和可证明postimage则Resume；策略要求Rollback且preimage可证明则精确恢复后重新Plan。只有证据不足、第三方正式字节冲突、需要审批或外部动作，或命中其他真实安全边界时，才停止整个用户任务；
   - 冲突、审批、人工裁决、权限和安全信号不得忽略；
   - 已经对齐后不得重复维护或重复写入；`refresh_ready_for_overview` 是checkpoint事实，由Agent决定是否为下一阶段请求普通完整Overview。

   维护完成后如果又修改了任何受管理对象，之前的维护结果失效，应在新的最终稳定状态重新完成收尾。

9. 用户只限制业务文件范围，但没有明确禁止仓库托管资产时，AOCI托管资产可以在收尾阶段为保持认知一致而更新，并应在审计和提交中与业务文件区分。

   用户明确禁止修改 `aoci.txt`、`.aoci`、元数据或任何额外文件时，以用户限制为准，不得写入，并如实报告剩余不一致。

### 专项流程

初始化、完整索引生成、Header生成、Entries生成、数据库结构索引、Curation、人工评审和故障恢复，只按当前AOCI Guide或工具在对应阶段返回的指令、命令和安全停点执行。

不预加载、不猜测，也不自行重建这些专项流程。平台调用方式、请求格式、批次上限、审批规则、索引格式细节和恢复步骤由对应Guide、工具说明、模型Prompt和CLI帮助按需提供。
<!-- aoci:end -->
