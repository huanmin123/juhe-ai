# 聚合 AI

![聚合 AI 管理后台预览](resources/images/home-page.png)

聚合 AI（juhe-ai）是一个轻量级 OpenAI 兼容中转与账号调度项目，面向手里有多套中转地址、多组上游账号和多个服务商入口的个人或团队场景。它把客户端固定到一个本地 OpenAI 兼容入口，服务商、账户、代理和错误切换都放到后台统一管理，减少反复修改客户端 Base URL / API Key 带来的配置混乱和会话割裂。

第一阶段聚焦 OpenAI 供应商，支持通过 OpenAI OAuth 或 OpenAI API Key 接入上游账号。系统采用“分组绑定账号、API Key 绑定分组”的授权模型：客户端只需要把 Base URL 指向本服务的 `/v1`，并使用后台生成的本地 API Key，即可按 OpenAI 兼容协议发起请求。

## 解决的问题

- 客户端只保留一个本地入口，不再在多个中转、账号和服务商之间来回改配置。
- 会话尽量保持在同一个客户端入口下，避免切换服务商或密钥后出现历史会话不可见、上下文割裂的问题。
- 上游账号限流、异常或不可用时，网关按分组和策略自动切换到其他可用账号，降低手动排查和切换成本。
- 多个账号的归属、代理、并发、优先级和错误策略集中维护，避免散落在不同客户端里。
- 请求、用量、错误和命中账号统一记录，方便定位到底是哪个账号、分组或上游出了问题。

## 核心能力

- 固定本地入口：统一提供 `/v1` 网关入口，客户端长期使用同一个 Base URL 和本地 API Key
- 上游账号管理：支持 OpenAI OAuth 与 API Key 账号，提供启停、测试、代理和调度配置；账户与分组使用授权纳入阶段规划
- 分组与密钥授权：账号归入分组，API Key 绑定分组，按自有或授权边界选择可用账号
- 自动故障切换：账号异常、限流或临时不可用时，按状态、优先级和错误策略切换备用账号
- 透传开关：账号级控制是否开启透传，前端只显示开 / 关，不暴露内部处理细节
- 使用记录追踪：记录请求、模型、用量、耗时、错误和账号命中，方便排查与统计
- 轻量化部署：前后端均使用 TypeScript，默认 SQLite 存储，发布包可跨平台运行

## 技术栈

- 前端：Vue 3 + TypeScript + Ant Design Vue
- 后端：Node.js + TypeScript
- 存储：SQLite
- 文档：`docs/`

## 当前阶段

- 完整模块先按“系统账户、全局设置、供应商、AI 账户、API 密钥、代理、使用记录、系统设置、分组”设计
- 第一期只实现 OpenAI 供应商
- OpenAI 第一期账户类型：OAuth + API Key
- 分组绑定账户，API Key 再绑定分组
- 账户和分组使用授权纳入第一阶段设计：被授权用户只获得使用权，日志按调用方隔离，账户用量按真实账户统一累计，分组用量按真实分组统一累计
- 透传能力只保留账号级开关，用户不需要理解或选择内部实现细节

## 入口文档

- 文档目录总览与命名规范：`docs/README.md`
- 整体架构：`docs/architecture/架构总览.md`
- 架构设计目录：`docs/architecture/README.md`
- 第一阶段计划：`docs/plans/第一阶段计划.md`
- 构建发布文档：`docs/deploy/构建指南.md`
- 部署运行文档：`docs/deploy/部署指南.md`
- 开发文档目录：`docs/develop/README.md`
- 开发环境安装说明：`docs/develop/安装指南.md`
- 开发运行说明：`docs/develop/运行说明.md`
- 前端架构设计：`docs/architecture/frontend/README.md`
- 功能文档目录：`docs/functions/`
- 重构案例库：`docs/refactors/`
- 功能开发指导：`docs/architecture/功能开发指导.md`
- 大文件重构指南：`docs/architecture/大文件重构指南.md`
- 问题记录目录：`docs/bug/README.md`
- 问题修复指导：`docs/architecture/问题修复指导.md`

## 本地运行

```powershell
pnpm install
pnpm dev
```

默认端口：

- 前端：`http://127.0.0.1:5173`
- 后端：`http://127.0.0.1:3000`
- 中转 Base URL：`http://127.0.0.1:3000/v1`

项目按“目录可移植”设计：后端读取 `backend/.env`，前端读取 `frontend/.env`；默认 SQLite 文件放在 `backend/data/juhe-ai.sqlite3`。这些配置文件跟着项目目录走，不依赖系统环境变量，拷贝整个目录到其他电脑或服务器后只要保留 `.env` 和 `backend/data/` 即可继续使用。

```powershell
Copy-Item backend/.env.example backend/.env
Copy-Item frontend/.env.example frontend/.env
# 按需编辑 backend/.env 和 frontend/.env
pnpm --filter juhe-ai-backend dev
```

服务器或局域网部署时，通常只需要改项目内配置文件：

- `backend/.env`：`JUHE_AI_HOST=0.0.0.0`、`JUHE_AI_PORT=3000`、`JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3`
- `frontend/.env`：`VITE_JUHE_AI_API_BASE_URL=/api`、`VITE_JUHE_AI_GATEWAY_BASE_URL=http://你的域名或IP:3000/v1`
- 需要代理时在代理管理页给账户绑定代理；`JUHE_AI_OAUTH_PROXY_URL` 只作为 OAuth token 换取/刷新的可选兜底，不再默认写死本机代理。

对外请求统一兼容 OpenAI 协议：客户端填本服务 `/v1` 作为 Base URL，API Key 填 API 密钥页生成的本地网关密钥；后续提供方也优先适配成 OpenAI 兼容格式。

## 跨平台发布包

发布体系支持任意平台打包，并部署到 Windows、macOS 或 Linux。构建说明见 `docs/deploy/构建指南.md`，部署说明见 `docs/deploy/部署指南.md`。

打包命令：

```powershell
# Windows 打包
pnpm package:release:windows
```

```bash
# macOS 打包
pnpm package:release:mac

# Linux 打包
pnpm package:release:linux
```

默认生成：

- `release/juhe-ai-release.tar.gz`：推荐给 macOS/Linux 目标。
- `release/juhe-ai-release.zip`：推荐给 Windows 目标。

目标机器启动：

```powershell
# Windows 目标
pwsh ./start.ps1
```

```bash
# macOS/Linux 目标
bash ./start.sh
```

发布包会由后端直接托管 `frontend/dist`，因此无需额外静态服务器即可访问管理后台。部署前必须检查 Node.js 是否支持 `node:sqlite`，并配置 `backend/.env` 中的 `JUHE_AI_SECRET`。

如需固定公网网关地址：

```powershell
# Windows 打包机
pwsh ./scripts/package-release.ps1 -FrontendGatewayBaseUrl "https://你的域名/v1"
```

```bash
# macOS/Linux 打包机
bash ./scripts/package-release.sh --frontend-gateway-base-url "https://你的域名/v1"
```

旧的 Linux-only 打包命令仍保留，但新发布优先使用 `package:release:*`。
## 当前可用功能

- 供应商列表：内置 OpenAI，默认 Base URL 归属供应商定义
- 系统账户管理：管理员可管理登录账号、角色和重置密码
- 登录安全：登录接口需要一次性图形验证码，并按 IP / 用户名做短时失败限制，减少自动化撞库和暴力尝试
- AI 账户管理：创建 / 编辑 / 删除 OpenAI OAuth 与 API Key 账户，OAuth 支持手动授权和 Refresh Token 授权
- AI 账户管理：管理员视角会显示系统账户列；普通用户当前只看到自己的数据；账户与分组使用授权规划详见 `docs/plans/第一阶段计划.md`
- 分组管理：创建 / 编辑 / 删除分组，并绑定账户
- API Key 管理：创建 / 编辑 / 删除 API Key，列表直接显示完整密钥，页面展示中转 Base URL 并支持复制
- 代理管理：创建 / 编辑 / 删除 HTTP、HTTPS、SOCKS5 代理
- 使用记录：管理员可看全部记录并按系统账户筛选，普通用户只看自己的记录
- 系统设置：用户级默认并发、临时不可调用和流熔断参数；全局品牌由管理员单独维护


## 测试与验证

```powershell
pnpm typecheck
pnpm build
pnpm test:smoke
```

本地真实网关验证和烟测细节见 `docs/develop/测试与验证说明.md`；运行启动与注意事项见 `docs/develop/运行说明.md`。







