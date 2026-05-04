# 聚合 AI

聚合 AI（`juhe-ai`）是一个轻量级 OpenAI 兼容中转与账号调度管理项目。它把客户端固定到一个本地 `/v1` 入口，把 OpenAI 上游账号、分组、API Key、代理、授权、错误切换和使用记录放到后台统一管理，适合个人或小团队维护多组上游账号、多套代理和多个客户端接入的场景。

当前阶段聚焦 OpenAI 供应商，支持通过 OpenAI OAuth 或 OpenAI API Key 接入上游账号。系统采用“分组绑定账号、API Key 绑定分组”的授权模型：客户端只需要把 Base URL 指向本服务的 `/v1`，并使用后台生成的本地 API Key，即可按 OpenAI 兼容协议发起请求。

> 项目仍处于第一阶段迭代，目标是先形成轻量闭环，再逐步扩展更多供应商和更复杂的网关能力。



![聚合 AI 管理后台预览](resources/images/home-page.png)

![聚合 AI 管理后台预览](resources/images/admin-page.png)


## 解决的问题

- 客户端只维护一个本地 Base URL 和一个本地 API Key，不再频繁切换上游地址或上游密钥。
- 多个 OpenAI OAuth / API Key 账号集中管理，账号状态、并发、代理、优先级和到期时间一处维护。
- API Key 通过分组获得调用边界，便于把不同客户端、不同用户或不同用途隔离开。
- 上游账号限流、异常或流式中断时，网关可按策略标记账号并尝试切换到同组其他可用账号。
- 请求明细、Token、耗时、成本估算、错误和命中账号统一记录，方便回溯问题来源。
- 管理后台支持系统账户、角色和授权关系，适合把自有账号或分组有限授权给其他用户使用。

## 当前可用功能

### 管理后台

- **登录与系统账户**：内置默认管理员 `admin/admin`，首次登录要求改密；支持 `admin` / `user` 两种角色、系统账户创建、停用、重置密码和强制会话失效。
- **登录安全**：登录使用后端一次性图形验证码；按客户端 IP 和用户名做短时失败限制，降低自动化撞库风险。
- **全局品牌**：管理员可维护系统名称和图标；登录页和应用壳统一读取公开品牌配置。
- **中文界面**：前端基于 Vue 3、TypeScript 和 Ant Design Vue，页面文案、空态、表单提示和组件 locale 均面向中文用户。

### OpenAI 账号接入

- **供应商定义**：内置 OpenAI，默认上游地址为 `https://api.openai.com/v1`，能力包含 `models`、`responses`、`stream` 和透传。
- **模型价格目录**：供应商页可查看 OpenAI 模型价格快照；网关按模型目录估算请求成本，未命中模型时只记录 Token 不猜测成本。
- **OAuth 账号**：支持手动授权链接创建，也支持直接粘贴 Refresh Token 创建；请求前可自动刷新 Access Token。
- **API Key 账号**：支持填写上游 OpenAI API Key 创建账号；敏感凭据只在编辑弹窗中展示和修改，列表不暴露。
- **账号配置**：支持账号启停、调度开关、优先级、并发上限、到期时间、代理绑定、错误策略和连接测试。
- **OAuth 额度快照**：OpenAI OAuth 账号支持后台刷新 Codex `5h` / `7d` 额度快照，并在账号列表中展示刷新状态和进度。

### 分组、授权与 API Key

- **分组管理**：每个系统账户自动拥有 OpenAI 默认分组；可创建、编辑分组并绑定同供应商账号，默认分组不允许删除。
- **账号授权**：自有账号可授权给其他系统账户；被授权用户可使用、测试、按自己维度启停调度、加入自己的同供应商分组或归还授权，但不能编辑、删除、查看敏感凭据或继续转授权。
- **分组授权**：自有分组可授权给其他系统账户；被授权用户可把授权分组绑定到自己的 API Key 使用，但不能编辑分组、删除分组或管理分组成员。
- **API Key 管理**：本地网关 API Key 可创建、编辑、禁用、设置到期时间和绑定分组；列表显示完整本地密钥并提供复制入口。
- **权限隔离**：管理员可查看全部数据并显示系统账户列；普通用户默认只看自有数据和授权给自己的资源。

### OpenAI 兼容网关

- **统一入口**：后端提供 `/v1/*` 网关入口，客户端按 OpenAI 兼容格式请求即可。
- **鉴权模型**：网关只读取 `Authorization: Bearer <本地 API Key>`，不读取后台登录态，也不使用上游 OpenAI 密钥作为客户端密钥。
- **调度链路**：网关通过本地 API Key 找到绑定分组，再在分组内选择可用 OpenAI 账号；授权分组和授权账号会按调用方维度记录用量。
- **请求透传**：非管理接口默认按 OpenAI 兼容协议透传到上游，支持普通响应和流式响应，并保留必要响应头。
- **错误处理**：账号限流、上游错误、未知异常和流式中断可触发账号临时不可调用、冷却、限流标记或同组账号切换。
- **代理支持**：账号可绑定 HTTP、HTTPS 或 SOCKS5 代理；OAuth token 刷新、账号测试和网关真实转发都会使用账号代理。

### 记录、统计与设置

- **使用记录**：记录请求 ID、客户端 IP、API Key、分组、账号、模型、端点、流式标记、状态码、Token、成本、首 Token 耗时、总耗时、错误摘要和请求/响应快照。
- **统计概览**：管理员可查看今日请求、Token 趋势、模型分布、错误分布、成本概览和系统监控采样。
- **系统监控**：后台定时采样 CPU、内存、进程、事件循环、网络吞吐和数据库大小，并维护小时聚合数据。
- **调度设置**：每个系统账户可配置默认临时不可调用时长、短暂重试参数、流式请求超时、流式空闲超时和流熔断阈值。
- **后台任务**：内置使用统计聚合、系统指标采样和 OpenAI OAuth 额度快照刷新任务。

## 技术栈

- 前端：Vue 3 + TypeScript + Vite + Ant Design Vue + ECharts
- 后端：Node.js + TypeScript + Express + Zod
- 存储：SQLite（使用 Node.js `node:sqlite`）
- 包管理：pnpm workspace
- 发布：前端静态资源由后端托管，支持 Windows、macOS、Linux 发布包

## 项目结构

```text
.
├─ backend/             # Node.js API、网关、后台任务、SQLite 存储
├─ frontend/            # Vue 管理后台、路由、页面、API 请求层
├─ docs/                # 架构、功能、计划、开发、部署、问题和重构文档
├─ deploy/              # 发布包启动脚本模板
├─ resources/           # README 和品牌相关静态资源
├─ scripts/             # 跨平台打包脚本
└─ release/             # 本地打包产物目录
```

## 环境要求

- Node.js `>= 22.5.0`，部署前建议确认当前 Node 支持 `node:sqlite`。
- pnpm `>= 9.0.0`。
- Windows 默认使用 PowerShell 7（`pwsh`）；macOS / Linux 使用 Bash 启动脚本。

## 本地开发

安装依赖：

```powershell
pnpm install
```

复制本地配置：

```powershell
Copy-Item backend/.env.example backend/.env
Copy-Item frontend/.env.example frontend/.env
```

启动前后端开发服务：

```powershell
pnpm dev
```

默认访问地址：

- 前端管理后台：`http://127.0.0.1:5173`
- 后端管理 API：`http://127.0.0.1:3000/api`
- OpenAI 兼容网关：`http://127.0.0.1:3000/v1`
- 健康检查：`http://127.0.0.1:3000/health` 或 `http://127.0.0.1:3000/api/health`

默认登录账号：

- 用户名：`admin`
- 密码：`admin`
- 首次登录后请立即修改默认密码。

也可以只启动单端：

```powershell
pnpm --filter juhe-ai-backend dev
pnpm --filter juhe-ai-frontend dev
```

## 配置说明

项目按“目录可移植”设计，后端读取 `backend/.env`，前端读取 `frontend/.env`，默认 SQLite 文件放在 `backend/data/juhe-ai.sqlite3`。迁移项目时保留 `.env` 和 `backend/data/` 即可继续使用。

常用后端配置：

```dotenv
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3
JUHE_AI_SECRET=juhe-ai-dev-secret-change-me
JUHE_AI_OAUTH_PROXY_URL=
JUHE_AI_BACKEND_URL=http://127.0.0.1:3000
JUHE_AI_SMOKE_ACCOUNT_NAME=
JUHE_AI_SMOKE_MODEL=gpt-5.4-mini
JUHE_AI_SMOKE_PROMPT=只输出 OK
```

常用前端配置：

```dotenv
VITE_JUHE_AI_BACKEND_TARGET=http://127.0.0.1:3000
VITE_JUHE_AI_API_BASE_URL=/api
VITE_JUHE_AI_GATEWAY_BASE_URL=
```

部署到局域网或服务器时通常需要调整：

- `backend/.env`：把 `JUHE_AI_HOST` 改为 `0.0.0.0`，并按需修改 `JUHE_AI_PORT`。
- `backend/.env`：生产环境必须设置稳定且足够随机的 `JUHE_AI_SECRET`；已有数据库迁移时不能更换，否则 OAuth token、上游 API Key 和代理密码会无法解密。
- `frontend/.env`：同源部署保持 `VITE_JUHE_AI_API_BASE_URL=/api`；分离部署时改为完整后端 `/api` 地址。
- `frontend/.env`：公网或分离部署时建议显式设置 `VITE_JUHE_AI_GATEWAY_BASE_URL=https://你的域名/v1`。

## 客户端接入

1. 登录管理后台。
2. 在 `AI账户管理` 中创建 OpenAI OAuth 或 OpenAI API Key 账号。
3. 在 `分组` 中确认账号已绑定到目标分组。
4. 在 `API 密钥` 中创建本地 API Key，并绑定目标分组。
5. 在客户端中配置：

```text
Base URL: http://127.0.0.1:3000/v1
API Key : API 密钥页面展示的 sk-... 本地密钥
```

注意事项：

- 客户端填写的是本地网关 API Key，不是上游 OpenAI API Key，也不需要手动加 `Bearer ` 前缀。
- 网关请求不依赖后台登录 Cookie，适合 Codex、OpenAI SDK 或其他 OpenAI 兼容客户端直接接入。
- 如果返回 `Invalid API key`，优先检查 API Key 是否复制完整、是否启用、是否过期以及绑定分组是否可用。
- 如果返回 `No available upstream account`，优先检查分组内是否有启用、未过期、可调度且未冷却的账号。

## 测试与验证

代码级检查：

```powershell
pnpm typecheck
pnpm build
```

真实网关烟测：

```powershell
pnpm test:smoke
```

烟测会在项目已启动后自动选择可用 OpenAI 账号和本地 API Key，验证 `/v1/models`、`/v1/responses` 非流式、流式响应以及使用记录入库。需要固定账号或模型时编辑 `backend/.env` 中的 `JUHE_AI_SMOKE_ACCOUNT_NAME`、`JUHE_AI_SMOKE_MODEL` 和 `JUHE_AI_SMOKE_PROMPT`。

更多验证细节见 `docs/develop/测试与验证说明.md`。

## 跨平台发布包

Windows 打包：

```powershell
pnpm package:release:windows
```

macOS / Linux 打包：

```bash
pnpm package:release:mac
pnpm package:release:linux
```

默认生成：

- `release/juhe-ai-release.zip`：推荐给 Windows 目标机器。
- `release/juhe-ai-release.tar.gz`：推荐给 macOS / Linux 目标机器。

目标机器启动：

```powershell
# Windows
pwsh ./start.ps1
```

```bash
# macOS / Linux
bash ./start.sh
```

发布包由后端直接托管 `frontend/dist`，无需额外静态服务器即可访问管理后台。构建和部署前请阅读 `docs/deploy/构建指南.md` 与 `docs/deploy/部署指南.md`。

如需在打包时固定公网网关地址：

```powershell
pwsh ./scripts/package-release.ps1 -FrontendGatewayBaseUrl "https://你的域名/v1"
```

```bash
bash ./scripts/package-release.sh --frontend-gateway-base-url "https://你的域名/v1"
```

## 文档入口

- 文档目录规范：`docs/README.md`
- 整体架构：`docs/architecture/架构总览.md`
- 后端架构：`docs/architecture/backend/README.md`
- 前端架构：`docs/architecture/frontend/README.md`
- 核心功能设计：`docs/functions/核心功能设计.md`
- OpenAI 一期账号接入：`docs/functions/第一期OpenAI账号接入.md`
- 接口契约与权限矩阵：`docs/functions/接口契约与权限矩阵.md`
- SQLite 存储说明：`docs/functions/SQLite存储说明.md`
- 安全与日志策略：`docs/functions/安全与日志策略.md`
- 第一阶段计划：`docs/plans/第一阶段计划.md`
- 开发安装说明：`docs/develop/安装指南.md`
- 开发运行说明：`docs/develop/运行说明.md`
- 测试与验证说明：`docs/develop/测试与验证说明.md`
- 构建指南：`docs/deploy/构建指南.md`
- 部署指南：`docs/deploy/部署指南.md`

## 当前阶段边界

- 第一阶段只实现 OpenAI 供应商；其他供应商先保留架构扩展空间。
- 存储默认使用 SQLite，优先服务单机和轻量部署场景。
- 代理是管理员维护的全局资源，普通用户不进入代理管理页。
- 统计概览目前面向管理员；普通用户以使用记录页查看自己的调用明细。
- 更复杂的多实例会话、共享验证码存储、供应商插件化和重型网关策略不属于当前轻量闭环优先级。

