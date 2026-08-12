# Windows 部署流程示例

## 示例目标

- 目标路径：`D:\juhe-ai-release`
- 访问地址：`http://127.0.0.1:3000/__aisys__/`
- 启动方式：PowerShell 7 手动启动，后续再注册服务
- 上游代理：Windows 本机 sing-box，`socks5h://127.0.0.1:7890`

## 步骤

```powershell
Expand-Archive .\juhe-ai-release.zip -DestinationPath D:\ -Force
Set-Location D:\juhe-ai-release
Copy-Item .\backend\.env.example .\backend\.env -ErrorAction SilentlyContinue
notepad .\backend\.env
```

最低配置：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_ALLOWED_ORIGINS=http://127.0.0.1:3000
JUHE_AI_COOKIE_SECURE=false
JUHE_AI_SECRET=替换为至少32位稳定随机密钥
JUHE_AI_RUNTIME_LOG_INSTANCE_ID=juhe-ai-go-sidecar-runtime-log
JUHE_AI_TABLE_MONITOR_INSTANCE_ID=juhe-ai-go-sidecar-table-monitor
JUHE_AI_AUDIT_LOG_INSTANCE_ID=juhe-ai-go-sidecar-audit-log
JUHE_AI_AUDIT_LOG_STORE=sqlite
JUHE_AI_AUDIT_LOG_DATABASE_PATH=./data/juhe-ai-audit-log.sqlite3
JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=./data/audit-payload-blobs
JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=./data/audit-hot-search
JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS=127.0.0.1:3303
JUHE_AI_AUDIT_LOG_INPUT_URL=http://127.0.0.1:3303
JUHE_AI_AUDIT_LOG_INPUT_SECRET=替换为独立且至少32位的稳定随机密钥
```

启动并验证：

```powershell
pwsh .\start.ps1
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/health
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/api/health
Invoke-WebRequest http://127.0.0.1:3303/__aiinternal__/health
Get-Content .\backend\logs\juhe-ai-go-sidecar.log -Tail 100
```

Node 两个 health 应为 `200`，F3 health 应为 `204`。继续按 [AI 部署执行清单](../AI部署执行清单.md) 验证 F1/F2 新鲜度和 Node -> F3 -> Node 审计读回。

后台“代理管理”新增：

```text
名称：Windows sing-box
类型：socks5h
Host：127.0.0.1
端口：7890
```

保存后点“测试”，再绑定到需要访问上游的 AI 账户。
