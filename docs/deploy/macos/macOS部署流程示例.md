# macOS 部署流程示例

## 示例目标

- 目标路径：`~/juhe-ai-release`
- 访问地址：`http://127.0.0.1:3000/__aisys__/`
- 启动方式：`start.sh` 手动启动，后续可转 launchd
- 上游代理：macOS 本机 sing-box，`socks5h://127.0.0.1:7890`

## 步骤

```bash
tar -xzf juhe-ai-release.tar.gz -C ~/
cd ~/juhe-ai-release
cp -n backend/.env.example backend/.env
nano backend/.env
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

```bash
bash ./start.sh
curl -i http://127.0.0.1:3000/__aisys__/health
curl -i http://127.0.0.1:3000/__aisys__/api/health
curl -i http://127.0.0.1:3303/__aiinternal__/health
tail -n 100 ./backend/logs/juhe-ai-go-sidecar.log
```

Node 两个 health 应为 `200`，F3 health 应为 `204`。唯一的 Go sidecar 会在同一进程内恢复 F1/F2/F3 中发生普通运行错误的组件；它不是三个独立 launchd 服务。生产 routine release 不在 active 槽手工执行本页命令：应启动独立 candidate Node 槽，验证 control/API/gateway、共享 Go health 和启动日志，再通过 [生产发布快速流程](../生产发布快速流程.md) 的快速 route 切换上线。浏览器、数据读回、稳定窗口和 handover controller 只用于首次新拓扑、故障或回切。

后台“代理管理”新增：

```text
名称：macOS sing-box
类型：socks5h
Host：127.0.0.1
端口：7890
```

保存后点“测试”，再绑定到需要访问上游的 AI 账户。
