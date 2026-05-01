# SQLite 存储说明

## 为什么用 SQLite

当前项目只给个人使用，不需要复杂部署、水平扩展或极限并发。SQLite 足够稳定，文件备份也简单，更符合轻量项目定位。

## 默认位置

后端默认数据库文件：

```text
backend/data/sub2api-lite.sqlite3
```

可以通过环境变量指定：

```powershell
$env:SQLITE_PATH = "F:\sub2api-lite-data\sub2api-lite.sqlite3"
```

也兼容：

```powershell
$env:DATABASE_PATH = "F:\sub2api-lite-data\sub2api-lite.sqlite3"
```

## 当前实现

- 使用 Node 22 内置 `node:sqlite`
- 启动时自动建表
- 启动时自动写入 OpenAI 供应商、默认分组和默认系统设置
- 使用 `PRAGMA journal_mode = WAL`
- 通过 `backend/src/storage/repositories.ts` 统一访问数据

## 敏感字段

以下字段必须加密存储：

- OpenAI OAuth token
- OpenAI API Key
- 代理密码

这是单人自用系统，接口会返回前端需要展示的完整密钥；数据库中仍尽量加密保存。

API Key 明文只在创建时返回一次。

