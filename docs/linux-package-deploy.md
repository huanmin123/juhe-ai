# Linux 发布包说明

这个目录是 Windows 侧 `scripts/package-linux.ps1` 生成的 Linux 发布包内容。它包含已构建的后端 JS、前端静态文件、运行所需的 package/lock 文件，以及一键启动脚本。

## 目录结构

```text
juhe-ai-linux/
  backend/
    dist/
    data/
    package.json
    .env.example
    .env.example.local  # 如果打包机存在 backend/.env，会被复制为这个文件
  frontend/
    dist/
    .env.example
    .env.example.local  # 如果打包机存在 frontend/.env，会被复制为这个文件
  package.json
  pnpm-lock.yaml
  pnpm-workspace.yaml
  start.sh
  README.md
```

## 首次启动

```bash
tar -xzf juhe-ai-linux.tar.gz
cd juhe-ai-linux
bash start.sh
```

`start.sh` 会做这些事：

- 检查 Node.js 版本，要求 Node.js 22+。
- 尝试通过 corepack 启用 pnpm。
- 如果缺少 `backend/.env`，优先从 `backend/.env.example.local` 创建，否则从 `backend/.env.example` 创建。
- 自动创建 `backend/data`。
- 首次运行时执行 `pnpm install --prod --frozen-lockfile` 安装 Linux 生产依赖。
- 启动 `node backend/dist/server.js`。

## 生产配置

建议正式上线前编辑 `backend/.env`：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3
JUHE_AI_SECRET=换成一串足够长且固定保存的随机密钥
```

如果不使用 Nginx、Caddy 或其他反向代理，而是直接暴露 Node 服务，可以改为：

```env
JUHE_AI_HOST=0.0.0.0
```

## 访问方式

这个发布包的后端会直接托管 `frontend/dist`：

- 管理后台：`http://服务器IP:3000/`
- 管理 API：`http://服务器IP:3000/api`
- OpenAI 兼容网关：`http://服务器IP:3000/v1`

如果使用域名和 HTTPS，推荐用 Nginx/Caddy 反向代理到 `127.0.0.1:3000`。

## 数据与备份

SQLite 数据默认保存在：

```text
backend/data/juhe-ai.sqlite3
```

迁移和备份时至少保留：

- `backend/.env`
- `backend/data/juhe-ai.sqlite3`

`JUHE_AI_SECRET` 影响敏感字段解密，已有数据迁移时必须保持不变。

## 更新发布包

```bash
# 停止旧进程后，保留 backend/.env 和 backend/data
cp -a old/juhe-ai-linux/backend/.env new/juhe-ai-linux/backend/.env
cp -a old/juhe-ai-linux/backend/data new/juhe-ai-linux/backend/data
cd new/juhe-ai-linux
bash start.sh
```

如果 lockfile 或依赖变化，删除旧的 `node_modules` 后重新启动：

```bash
rm -rf node_modules backend/node_modules frontend/node_modules
bash start.sh
```

## 注意事项

- 不要把 Windows 的 `node_modules` 拷到 Linux。
- 前端配置是构建时写入的，修改 `frontend/.env` 后需要重新在 Windows 打包。
- 不要把带真实密钥的发布包发给不可信的人。
- 建议使用进程管理器运行，例如 `pm2` 或 `systemd`，避免 SSH 断开后服务退出。
