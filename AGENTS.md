# AGENTS.md

## 根文档职责

- 本文件只承担项目级导航、核心业务边界和高频事件入口，不复制专题文档正文。
- 具体架构、功能、前后端实现、测试、问题、重构、迁移和报告规则以 `docs/` 下对应权威文档为准。
- 本文件不自动触发生产部署或线上运维流程；只有用户主动提出生产操作并明确提供适用资料后，才读取和执行对应范围内的外部操作规范。
- 后端为 Go 三项目（`backend-go/projects/{gateway,jobs,maintenance}`）；原 Node 后端（`backend/`）已完成 Node→Go 全量迁移并归档至 `migration-backup/node/final-archive/`（不得恢复、修改或运行），运行事实以 Go 实现为准。现行生产形态为国内单机 Docker go-only（服务器与凭据见 `.local/project-resources/prod/assets/国内单机-103.36.63.105.md`，部署配置在 `docker/single-server/`，运维入口见 `.local/project-resources/prod/runbooks/国内单机Docker部署与运维.md`）；PG/Redis 为云机本机 compose 容器，出海代理隧道为用户自管外部资产；旧 Mac + K3s + Edge 混合形态已下线，平台配置仓 `F:\k8s` 中 juhe-ai 现役配置已清理（历史报告类文档保留），残留核查另行任务。

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
| 后端接口、存储、网关、后台任务或队列 | `backend-go/README.md` 与 `docs/migration/Go三项目架构基线.md`；后端实现专题见 `docs/architecture/backend/README.md`（历史） |
| 需求计划、执行进度或关联文档 | `docs/plans/README.md` |
| 本地安装、运行、联调、测试或验证 | `docs/develop/README.md` |
| 部署相关变动（compose/env/参数/运行时目录/初始化与验证步骤） | `docs/deploy/README.md` 与 `docker/single-server/README.md`（同步约束见上文"部署变更文档同步"） |
| bug、异常、测试失败或数据不一致 | `docs/architecture/问题修复指导.md`，必要时记录到 `docs/bug/README.md` |
| 大文件拆分、职责调整或重复逻辑收敛 | `docs/architecture/大文件重构指南.md`，复盘记录到 `docs/refactors/README.md` |
| Node 后端向 Go 迁移 | `docs/migration/README.md` |
| 压测、性能分析、容量或验证报告 | `docs/reports/README.md` |

## CodeGraph 与 RTK

- CodeGraph MCP 可用于查询跨模块依赖、调用链和影响范围；其结果必须以当前源码、`rg`、未跟踪文件和刚修改文件复核。
- 对只读且输出量大的命令，优先使用匹配的 `rtk` 子命令：`git`、`rg`、`log`、`diff`、`test`、`mvn`、`npm`、`pnpm`、`read`、`find`、`ls`、`tree`。未列出的只读命令先用 `rtk rewrite "<command>"` 或 `rtk --help` 核实；写操作和精确排障使用原生命令。
- 只有工具注册表或 `--help` 未列出目标命令时，才能判定该命令不存在；其他工具错误保留原始输出，不得归因于能力缺失。
- 安装、配置修复、初始化或修复索引、健康检查、升级审查和回滚使用全局 `$agent-toolchain`；不得在日常开发中自行安装、升级、重配或维护工具链。
