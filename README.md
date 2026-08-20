# juhe-ai

`juhe-ai` 是一个轻量、可扩展的 AI 账号调度与 OpenAI-compatible 网关。客户端只需配置本地 API Key 和一个稳定入口；账号凭据、模型能力、分组、路由、授权、用量与审计统一由后台管理。

> 整体迁移仍在进行：Web/网关、账户、管理 API、用量、统计和运维任务仍由 Node.js 承载；F1–F4 的运行日志、表监控、原始审计和操作日志职责已由 Go 承接。项目事实与迁移状态以 [整体架构](docs/architecture/架构总览.md) 和 [迁移文档](docs/migration/README.md) 为准。

## 适用场景

- 为 Codex、OpenAI SDK 与兼容客户端提供稳定的本地网关入口。
- 集中管理多个供应商账户、上游凭据、代理、模型和可用性策略。
- 将调用侧的 API Key 与上游账号解耦，按路由策略调度账号池。
- 需要查看用量、成本、性能、操作记录和请求审计的个人或团队。
- 优先从单机轻量部署开始，并可按需采用 PostgreSQL + Redis 性能模式。

## 核心模型

```text
客户端
  └─ 本地 API Key
       └─ 路由策略
            └─ 分组
                 └─ AI 账户
                      └─ 供应商 / 协议能力 / 上游凭据
```

- **API Key**：调用入口，负责鉴权、额度、状态和选择一个路由策略。
- **路由策略**：负责分组绑定与路由方式；支持普通、混合智能、权重、故障回退和轮询。
- **分组**：协调账号池和分组内调度，不承载供应商或协议语义。
- **AI 账户**：保存真实上游账户、凭据、代理、并发、时间计划、模型限制和错误策略。
- **供应商与协议档案**：定义上游协议、模型目录、价格与专项能力；客户端画像仅在网关内部自动识别。

详细边界见 [策略路由设计](docs/functions/策略路由设计.md) 与 [整体架构](docs/architecture/架构总览.md)。

## 功能概览

- OpenAI-compatible 网关：支持服务根路径和 `/v1` 入口。
- 多供应商账户：当前内置 `openai`、`gpt`、`xai`、`anthropic`、`deepseek`、`glm`、`gemini` 与 `hybrid` 账户能力。
- 账户管理：API Key、OAuth、代理、模型映射、账号测试、健康检测与时间计划。
- 调度与保护：分组调度、并发控制、来源级保护、显式故障回退和客户端会话亲和。
- 授权与管理：系统账户、团队、统一授权、API Key 额度与权限边界。
- 可观测性：使用记录、统计、性能、运行日志、操作日志、原始审计和模型检测。
- 存储模式：默认 standalone 使用 SQLite；显式 performance 模式使用 PostgreSQL + Redis。

OpenAI-compatible 指本地入口、认证方式和错误结构的兼容性；不同上游账户的协议与功能能力并不相同。默认情况下，真实上游失败会返回当前失败；仅在用户显式配置并满足重放条件时才会尝试 `retry_next`。

## 快速开始

### 环境要求

- Node.js `22.13.0+`（22.x）或 `24.11.0+`（24.x），并支持内置 `node:sqlite`
- pnpm `9+`
- Windows 环境推荐 PowerShell 7
- 根目录 `pnpm dev` 还需要可从 `PATH` 调用的 Go 1.26.x，用于启动已迁移的辅助进程

### 安装与启动

在项目根目录执行：

```powershell
pnpm install
Copy-Item backend/.env.example backend/.env
Copy-Item frontend/.env.example frontend/.env
pnpm --filter juhe-ai-backend check:runtime
pnpm dev
```

默认开发地址：

| 服务 | 地址 |
| --- | --- |
| 管理后台 | `http://127.0.0.1:5173/__aisys__/` |
| 系统 API | `http://127.0.0.1:3000/__aisys__/api` |
| 网关入口 | `http://127.0.0.1:3000/v1` |

当前运行流程与环境边界见 [开发运行说明](docs/develop/运行说明.md)；部署配置请从 [部署文档](docs/deploy/README.md) 选择对应场景。

### 接入客户端

1. 进入管理后台，创建或导入 AI 账户，并将账户加入分组。
2. 创建路由策略，按需要选择普通、混合智能、权重、故障回退或轮询模式。
3. 创建本地 API Key，并绑定该路由策略。
4. 在客户端填入网关地址与本地 API Key：

```text
Base URL: http://127.0.0.1:3000/v1
API Key: 后台创建的本地 API Key
```

客户端使用的是本地 API Key，不是上游供应商的 API Key。

## 常用命令

```powershell
# 启动开发环境（Node 后端、前端及已迁移的 Go 辅助进程）
pnpm dev

# 类型检查与静态检查
pnpm typecheck
pnpm lint

# 构建全部工作区
pnpm build

# 本地网关烟测
pnpm test:smoke

# 构建 Windows 发布包
pnpm package:release:windows
```

完整测试矩阵、真实账户验证和性能验证见 [开发测试与验证说明](docs/develop/测试与验证说明.md)。

## 架构与运行形态

- `frontend/`：Vue 3、TypeScript 与 Ant Design Vue 管理后台。
- `backend/`：Node.js、TypeScript 与 Express；承载网关、管理 API 代理、DB service 和现有 worker。
- `backend-go/`：渐进迁移层；`juhe-ai-jobs` 与 `juhe-ai-gateway` 已承接部分日志、审计与表监控职责，`juhe-ai-maintenance` 用于一次性维护命令。
- 默认单机运行由 Web/网关主进程、`ingest-worker`、`stats-worker`、`ops-worker`、DB service 和相应 Go 进程组成。

存储、进程职责和迁移边界见 [架构总览](docs/architecture/架构总览.md)、[后端架构](docs/architecture/backend/README.md) 与 [Go 后端迁移层](backend-go/README.md)。

## 部署与文档

- [开发文档](docs/develop/README.md)：安装、运行、测试与验证。
- [部署文档](docs/deploy/README.md)：按部署场景选择构建、Docker、Windows、Linux、macOS、代理与 HTTPS 指南。
- [功能文档](docs/functions/README.md)：账户、路由、模型、授权、存储、统计和网关的具体契约。
- [前端架构](docs/architecture/frontend/README.md) 与 [后端架构](docs/architecture/backend/README.md)：实现边界和维护约束。
- [迁移文档](docs/migration/README.md)：Node 到 Go 的渐进迁移状态、验收和归档规则。

## 项目边界

- 默认目标是可管理的单机 AI 网关与账号调度；分布式部署属于显式配置能力，不是默认运行形态。
- 本项目不将 API Key 作为跨协议转换或客户端画像的配置入口；这些能力分别归属混合供应商账户和网关运行时。
- 数据、配置或 schema 与当前契约不一致时，应按当前 schema 离线修复或重建，不在运行链路中保留未知兼容分支。
- 迁移中的功能是否已完成生产接管，以迁移与功能文档中的可核验验收记录为准。

## 其他
问题或建议：QQ群 `1105515344`
