# 开发运行说明

> 面向 AI 与维护者。
> 本文件只说明如何把项目跑起来以及运行时注意事项；测试步骤、接口样例和烟测细节请放到 [开发测试与验证说明](测试与验证说明.md)。

## 1. 运行前提

- 先完成 [开发环境安装说明](安装指南.md)，确保依赖已安装。
- 后端和前端都读取项目内 `.env` 文件，不默认依赖系统环境变量。
- Node.js 需要满足项目要求，并支持 `node:sqlite`。
- 默认端口 `3000` 和 `5173` 不应被其他进程占用。
- 复用已有数据库时，`backend/.env` 里的 `JUHE_AI_SECRET` 必须保持稳定。

## 2. 启动项目

在项目根目录同时启动前后端：

```powershell
pnpm dev
```

只启动后端：

```powershell
pnpm --filter juhe-ai-backend dev
```

只启动前端：

```powershell
pnpm --filter juhe-ai-frontend dev
```

## 3. 默认访问地址

- 前端管理后台：`http://127.0.0.1:5173`
- 后端 API：`http://127.0.0.1:3000`
- OpenAI 兼容中转入口：`http://127.0.0.1:3000/v1`

开发模式下，前端会把 `/api` 和 `/v1` 转发到 `frontend/.env` 中的 `VITE_JUHE_AI_BACKEND_TARGET`。

## 4. 运行注意事项

- 本地运行优先保持 `JUHE_AI_HOST=127.0.0.1`；需要局域网访问时再改成 `0.0.0.0`。
- 分离部署或公网访问时，再按实际地址设置 `VITE_JUHE_AI_GATEWAY_BASE_URL`。
- SQLite 默认数据目录是 `backend/data/`，移动项目目录时需要一起保留。
- OAuth token 换取、刷新和账号测试优先使用账号绑定代理；`JUHE_AI_OAUTH_PROXY_URL` 只是兜底代理。
- 前端面向中文用户，页面应通过全局 `a-config-provider` 使用中文 locale，避免 Ant Design Vue 默认英文文案出现在界面。
- 启动后先看终端日志里的监听地址和报错信息，不要把具体接口测试矩阵写进本文件。

## 5. 运行后下一步

- 只确认项目能打开时，访问前端管理后台并检查页面能正常加载即可。
- 需要代码检查、接口检查、网关联通、账号测试或烟测时，参考 [开发测试与验证说明](测试与验证说明.md)。


