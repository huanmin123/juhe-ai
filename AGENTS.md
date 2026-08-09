# AGENTS.md

## 根文档职责

- 本文件只承担项目级导航、核心业务边界和高频事件入口，不复制专题文档正文。
- 具体架构、功能、前后端实现、测试、问题、重构、迁移和报告规则以 `docs/` 下对应权威文档为准。
- 本文件不自动触发生产部署或线上运维流程；只有用户主动提出生产操作并明确提供适用资料后，才读取和执行对应范围内的外部操作规范。
- 目前backend-go 不参与实际业务开发，目前在进行迁移中，所以我们不需要改node的同时改go 因为到时候迁移方会负责同步改动。

## 项目定位

- 这里是 `juhe-ai`，定位为轻量级 OpenAI 兼容中转与账号管理项目。
- 前端使用 Vue 3 + TypeScript + Ant Design Vue；后端使用 Node.js + TypeScript，并包含后台 worker 和本地 DB service。
- 当前启用 OpenAI 供应商，支持 OpenAI OAuth 与 OpenAI API Key 两种账户创建方式；其他供应商保留架构扩展空间。

## 核心业务边界

- 当前项目事实以 `docs/architecture/架构总览.md` 和 `docs/functions/README.md` 为准，历史计划不替代当前架构和功能文档。
- 路由层级固定为 `API Key -> 路由策略 -> 分组 -> AI 账户 -> 供应商 / 协议能力`。
- API Key 只绑定路由策略；路由策略负责路由模式和分组绑定；分组协调 AI 账户；AI 账户和供应商管理真实上游语义。
- 普通路由只绑定一个分组；混合智能、权重、故障回退和轮询规则由策略路由维护。
- 客户端画像由网关内部自动识别，不作为 API Key、路由策略、分组或普通 AI 账户的用户配置项。
- 跨协议转换属于混合供应商账户能力，不写成 API Key 或路由策略里的显式协议桥接规则。

## 本地管理页面测试

- 需要通过项目管理页面执行 AI 账户、模型、探针或其他本地联调测试时，默认启动隔离的开发实例并使用开发自动登录；不得把截图、识别或人工输入验证码作为默认测试步骤。
- 隔离后端至少设置 `JUHE_AI_DEV_AUTO_LOGIN_USERNAME=admin` 和 `JUHE_AI_AUTH_CAPTCHA_DISABLED=true`，并为业务库、用量库、统计库、日志和运行时文件设置独立临时目录，禁止污染现有开发数据。
- 隔离前端通过 `VITE_JUHE_AI_BACKEND_TARGET` 和 `VITE_JUHE_AI_GATEWAY_BASE_URL` 指向该隔离后端；使用未占用的新端口，不停止或复用用户已经运行的前后端进程。
- 浏览器打开前必须完成隔离预检：确认后端健康检查指向本次新端口，`/auth/me` 已返回配置的开发账户，且 `/auth/captcha` 返回 `required: false`；任一检查失败都必须先修复启动配置，禁止把登录页当作目标页面继续操作。
- 端口选择必须以实时监听检查为准；默认端口被占用时自动选择未占用的新后端端口和前端端口，并同步更新 `VITE_JUHE_AI_BACKEND_TARGET`，不得复用已有服务、抢占端口或停止用户进程。
- 只有自动登录功能本身就是被测对象，或自动登录经上述预检确认不可用时，才允许测试登录流程；仍不得尝试破解验证码。
- 测试凭据只能从用户明确指定的本地文件或当次消息读取，不写入源码、文档、日志或测试产物；输出、审计检查和最终报告必须脱敏。

## 私有环境资源

- `/.local/` 是当前工作区的私有环境资源根目录，已由 `.gitignore` 忽略；其中的任何文件都不得 `git add`、提交、打包进发布产物或复制到 `docs/`。
- 开发与生产资料统一放在 `.local/project-resources/`，仅供本机维护者和 Agent 在用户授权的范围内读取：

  ```text
  .local/project-resources/
  ├── dev/                 # 本地开发资源、数据库约定、环境变量模板和运行记录
  │   ├── env/             # `shared.env.example` 是模板；真实 `shared.env` 不提交
  │   ├── database/        # 开发 PostgreSQL / Redis 隔离约定
  │   ├── accounts/
  │   ├── runbooks/
  │   ├── issues/
  │   └── logs/
  └── prod/                # 当前 Mac 部署与八台流量 Edge 的私有证据和手册
      ├── README.md         # 范围、资料导航和证据边界
      ├── assets/           # Mac / Edge 资产与访问台账
      ├── database/         # Mac PostgreSQL、PgBouncer、Redis 事实和验证入口
      ├── issues/           # 当前项目问题记录
      ├── runbooks/         # Mac 拓扑、部署验证、回滚和临时接管硬门禁
      ├── edge-cluster/     # 八台 Edge 的流量入口、WireGuard 回源、当前原件和现场验证
      │   └── current-config/ # 已核验 Edge 的 Caddy/systemd/WireGuard 私有快照
      ├── sources/          # Mac 当前配置/LaunchDaemon 原件、Mac 操作资料和临时接管旧实现
      │   ├── mac-active-config/
      │   ├── mac-launchdaemons/
      │   ├── mac-operations/
      │   └── temporary-cutover-legacy/
      ├── migration-manifest.json
      └── checksums.sha256
  ```

- `dev` 使用远端 `192.168.1.203` 的 PostgreSQL 数据库 `juhe_ai_sub2api_dev`、专用登录角色 `juhe_ai_sub2api_dev_app`，以及 cache/state/queue 三个 Redis 实例各自的 DB `9`；统一 namespace 是 `juhe-ai:dev`。真实连接配置在 `dev/env/shared.env`，初始化和加载入口在 `dev/runbooks/`。
- `F:\juhe-ai-public-welfare\.local` 仅供目录设计参考；当前项目不读取或复用其中的数据库、Redis、连接串或命名空间。
- `prod` 仅保存当前 Mac 部署与八台仅负责流量入口的 Edge 的私有证据和手册；`database/production-connection.env` 和 `edge-cluster/current-config/` 是用户明确授权保留的真实连接/配置快照。它不能自动执行部署、安装、切流或远端写操作。`sources/temporary-cutover-legacy` 明确标记旧日期/旧端口，仅供人工对齐，不作为现行默认。除这两处快照外，禁止复制 env、logs、releases、backups、shared、temporary 数据或向远端写操作。
- 操作 `.local` 后必须验证 `git check-ignore -v --no-index .local/<探针路径>` 命中忽略规则，并确认 `git ls-files -- .local` 没有输出。

## 事件导航

| 事件 | 必读入口 |
| --- | --- |
| 文档结构、新增文档、重命名或引用调整 | `docs/README.md` |
| 项目定位、模块边界、数据关系或网关主流程变化 | `docs/architecture/架构总览.md` |
| 新功能、字段、接口、存储、脚本或关键流程 | `docs/architecture/功能开发指导.md` 和 `docs/functions/README.md` |
| 前端页面、布局、样式、交互、文案或品牌 | `docs/architecture/frontend/README.md` |
| 后端接口、存储、网关、脚本、worker 或队列 | `docs/architecture/backend/README.md` |
| 需求计划、执行进度或关联文档 | `docs/plans/README.md` |
| 本地安装、运行、联调、测试或验证 | `docs/develop/README.md` |
| bug、异常、测试失败或数据不一致 | `docs/architecture/问题修复指导.md`，必要时记录到 `docs/bug/README.md` |
| 大文件拆分、职责调整或重复逻辑收敛 | `docs/architecture/大文件重构指南.md`，复盘记录到 `docs/refactors/README.md` |
| Node 后端向 Go 迁移 | `docs/migration/README.md` |
| 压测、性能分析、容量或验证报告 | `docs/reports/README.md` |

## AI 工具

- CodeGraph/RTK 的安装、初始化、维护和验证使用全局 `$agent-toolchain`。
- CodeGraph MCP 用于查询跨模块依赖、调用链和影响范围；处理跨模块任务时使用它。
- 对只读高输出命令，优先使用匹配的 `rtk` 子命令：`git`、`rg`、`log`、`diff`、`test`、`mvn`、`npm`、`pnpm`、`read`、`find`、`ls`、`tree`。未列出的只读命令先用 `rtk rewrite "<command>"` 或 `rtk --help` 判断；写操作和精确排障使用原生命令。
- 工具调用报错时，只有工具注册表或 `--help` 未列出目标命令，才可判定其不存在；否则不得归因于能力缺失。
