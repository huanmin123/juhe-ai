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
```

启动并验证：

```bash
bash ./start.sh
curl -i http://127.0.0.1:3000/__aisys__/health
```

后台“代理管理”新增：

```text
名称：macOS sing-box
类型：socks5h
Host：127.0.0.1
端口：7890
```

保存后点“测试”，再绑定到需要访问上游的 AI 账户。
