# 开发环境安装说明

> 面向 AI 与维护者。
> 这里说明接手项目后，想让 `juhe-ai` 跑起来需要什么环境、怎么安装、怎么准备项目内配置。

## 1. 目标

- 让新机器或新环境能完成首次安装。
- 让 AI 能快速判断“缺什么、怎么补、补完后怎么启动”。
- 统一说明哪些配置必须放在项目内，而不是依赖系统环境变量。

## 2. 环境要求

- 操作系统：当前仓库默认按 Windows + PowerShell 7 书写命令。
- Node.js：`22.5.0` 或更高，且需要支持 `node:sqlite`。
- pnpm：`9.x` 或更高。
- Git：用于拉取代码和提交修改。
- 网络：安装依赖时通常需要能访问 npm registry 或可用代理。

建议先检查：

```powershell
$PSVersionTable.PSVersion
node -v
pnpm -v
node --input-type=module -e "import 'node:sqlite'; console.log('node:sqlite ok')"
```

## 3. 首次安装

在项目根目录执行：

```powershell
pnpm install
```

如果是刚拿到项目，建议先复制项目内配置文件：

```powershell
Copy-Item backend/.env.example backend/.env
Copy-Item frontend/.env.example frontend/.env
```

## 4. 项目内配置

### 4.1 后端配置

编辑 `backend/.env`：

```dotenv
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3
JUHE_AI_SECRET=请替换为稳定随机字符串
JUHE_AI_OAUTH_PROXY_URL=
```

说明：

- `JUHE_AI_DATABASE_PATH` 默认使用项目内 `backend/data/juhe-ai.sqlite3`。
- `JUHE_AI_SECRET` 会影响已加密数据解密，迁移已有数据库时必须保持一致。
- `JUHE_AI_OAUTH_PROXY_URL` 只是 OAuth 刷新和换取的兜底代理，账号代理优先在代理管理页绑定。

### 4.2 前端配置

编辑 `frontend/.env`：

```dotenv
VITE_JUHE_AI_BACKEND_TARGET=http://127.0.0.1:3000
VITE_JUHE_AI_API_BASE_URL=/api
VITE_JUHE_AI_GATEWAY_BASE_URL=
```

说明：

- 开发时前端会把 `/api` 和 `/v1` 转发到 `VITE_JUHE_AI_BACKEND_TARGET`。
- 分离部署或公网访问时，再按实际地址填写 `VITE_JUHE_AI_GATEWAY_BASE_URL`。
- 烟测专用变量不放在安装说明里，需要时参考 [开发测试与验证说明](测试与验证说明.md)。

## 5. 可移植性

- 项目默认不依赖系统环境变量，优先修改项目内的 `.env` 文件。
- 复制整个项目目录到其他电脑或服务器时，保留 `backend/.env`、`frontend/.env` 和 `backend/data/` 即可继续使用。
- 如果把后端放到反向代理后面，通常仍建议保持 `JUHE_AI_HOST=127.0.0.1`；需要局域网访问时再改成 `0.0.0.0`。

## 6. 安装完成后做什么

- 先看 [开发运行说明](运行说明.md)。
- 需要详细验证时，再看 [开发测试与验证说明](测试与验证说明.md)。
- 如果需要部署说明，再看 `docs/deploy/构建指南.md` 和 `docs/deploy/部署指南.md`。








