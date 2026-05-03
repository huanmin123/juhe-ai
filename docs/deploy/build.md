# 跨平台构建文档

> 本文只说明如何在 Windows、macOS、Linux 任一平台构建发布包。部署到目标机器的步骤见 `docs/deploy/deploy.md`。

## 1. 兼容目标

当前发布体系支持任意构建平台生成同一类发布包：

| 构建平台 | 推荐命令 | 支持的部署目标 |
| --- | --- | --- |
| Windows | `pnpm package:release:windows` | Windows / macOS / Linux |
| macOS | `pnpm package:release:mac` | Windows / macOS / Linux |
| Linux | `pnpm package:release:linux` | Windows / macOS / Linux |

发布包是平台无关的构建产物，不包含当前机器的 `node_modules`。目标机器首次启动时会安装目标平台自己的生产依赖。

## 2. 构建前必须检测

### Windows 构建机

```powershell
$PSVersionTable.PSVersion
node -v
node --input-type=module -e "import 'node:sqlite'; console.log('node:sqlite ok')"
corepack --version
pnpm -v
tar --version
Test-Path package.json
Test-Path backend/package.json
Test-Path frontend/package.json
```

### macOS 构建机

```bash
pwd
sw_vers
uname -a
node -v
node --input-type=module -e "import 'node:sqlite'; console.log('node:sqlite ok')"
corepack --version || true
pnpm -v || true
tar --version || bsdtar --version || true
test -f package.json
test -f backend/package.json
test -f frontend/package.json
```

### Linux 构建机

```bash
pwd
uname -a
node -v
node --input-type=module -e "import 'node:sqlite'; console.log('node:sqlite ok')"
corepack --version || true
pnpm -v || true
tar --version
test -f package.json
test -f backend/package.json
test -f frontend/package.json
```

必须确认：

- 当前目录是项目根目录。
- Node.js 支持 `node:sqlite`，建议 Node.js 22.5+。
- pnpm 可用；不可用时先执行 `corepack enable`。
- 不要把构建机的 `node_modules` 放进发布包。
- 前端配置是构建时注入的，修改域名或网关展示地址后必须重新打包。

## 3. 推荐构建命令

### Windows

```powershell
pnpm package:release:windows
```

等价显式命令：

```powershell
pwsh ./scripts/package-release.ps1
```

### macOS

```bash
pnpm package:release:mac
```

等价显式命令：

```bash
bash ./scripts/package-release.sh
```

### Linux

```bash
pnpm package:release:linux
```

等价显式命令：

```bash
bash ./scripts/package-release.sh
```

## 4. 产物说明

默认生成：

```text
release/juhe-ai-release.tar.gz
release/juhe-ai-release.zip
```

推荐选择：

| 部署目标 | 推荐上传 |
| --- | --- |
| Windows | `juhe-ai-release.zip` |
| macOS | `juhe-ai-release.tar.gz` |
| Linux | `juhe-ai-release.tar.gz` |

发布包目录包含：

```text
juhe-ai-release/
  backend/dist/
  backend/.env.example
  frontend/dist/
  docs/deploy/
  package.json
  pnpm-lock.yaml
  pnpm-workspace.yaml
  start.sh
  start.ps1
  README.md
```

## 5. 常用构建参数

### 固定公网网关地址

Windows：

```powershell
pwsh ./scripts/package-release.ps1 -FrontendGatewayBaseUrl "https://你的域名/v1"
```

macOS / Linux：

```bash
bash ./scripts/package-release.sh --frontend-gateway-base-url "https://你的域名/v1"
```

默认不传时：

```env
VITE_JUHE_AI_API_BASE_URL=/api
VITE_JUHE_AI_GATEWAY_BASE_URL=
```

含义：

- 管理后台请求同源 `/api`。
- API Key 页面按浏览器访问地址自动推断 `/v1`。

### 只生成一种压缩格式

Windows：

```powershell
pwsh ./scripts/package-release.ps1 -ArchiveFormat zip
pwsh ./scripts/package-release.ps1 -ArchiveFormat tar.gz
```

macOS / Linux：

```bash
bash ./scripts/package-release.sh --archive-format zip
bash ./scripts/package-release.sh --archive-format tar.gz
```

Linux 构建机如果没有 `zip` 命令，可以只生成 `tar.gz`。

### 自用迁移包带本地配置

默认不会把本机 `.env` 放入发布包。如果是自用迁移包，并确认不会泄露，可以显式携带：

Windows：

```powershell
pwsh ./scripts/package-release.ps1 -IncludeLocalEnv
```

macOS / Linux：

```bash
bash ./scripts/package-release.sh --include-local-env
```

这会把：

- `backend/.env` 复制为发布包内的 `backend/.env.example.local`。
- `frontend/.env` 复制为发布包内的 `frontend/.env.example.local`。

带真实配置的包不要发给不可信的人。

## 6. 构建后检查

Windows：

```powershell
Test-Path release/juhe-ai-release.tar.gz
Test-Path release/juhe-ai-release.zip
tar -tzf release/juhe-ai-release.tar.gz | Select-Object -First 50
```

macOS / Linux：

```bash
test -f release/juhe-ai-release.tar.gz
tar -tzf release/juhe-ai-release.tar.gz | head -50
```

必须看到：

- `juhe-ai-release/start.sh`
- `juhe-ai-release/start.ps1`
- `juhe-ai-release/README.md`
- `juhe-ai-release/backend/dist/server.js`
- `juhe-ai-release/frontend/dist/index.html`
- `juhe-ai-release/pnpm-lock.yaml`

## 7. 旧 Linux-only 命令兼容

旧命令仍保留，但新发布优先使用 `package:release:*`：

```powershell
pnpm package:linux:windows
```

```bash
bash ./scripts/package-linux.sh
```

旧命令生成：

```text
release/juhe-ai-linux.tar.gz
```

仅在明确需要兼容旧自动化时使用。

## 8. 给 AI 的构建摘要

1. 先判断构建机系统：Windows、macOS 或 Linux。
2. 检查 Node.js、`node:sqlite`、pnpm、tar 和项目根目录。
3. 使用对应 `package:release:*` 命令构建。
4. 不要打包 `node_modules`。
5. 检查发布包同时包含 `start.sh` 和 `start.ps1`。
6. 按目标系统转到 `docs/deploy/deploy.md`。
