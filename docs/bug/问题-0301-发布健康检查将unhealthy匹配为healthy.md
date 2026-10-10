# BUG-0301 发布健康检查将 unhealthy 匹配为 healthy

## 基本信息

- 状态：已修复（2026-10-10，待下次授权发布实测 healthy 等待路径）
- 严重程度：P2
- 发现时间：2026-10-09（此前审查已发现，本轮补充建档）
- 模块：发布脚本 / 健康检查
- 关联：[全面审查复核与残留问题报告](../reports/全面审查复核与残留问题报告-2026-10-09.md)

## 现象与根因

`docker/single-server/deploy.sh:197` 用 `case "$st" in *healthy*)` 判断 `docker compose ps --format '{{.Status}}'` 的输出。`unhealthy` 包含 `healthy`，因此 `Up 2 minutes (unhealthy)` 也会设置 `ok=1` 并提前结束健康等待，随后打印容器 healthy。

远端构建流水线的 `pipefail` 修复没有改变这条判断。本问题在最初候选清单中出现过，但最终七项报告及外部逐项复审没有单独列出。

影响限定：后续二进制 MD5、公网健康检查和 spool 验证仍会执行，可能因其他问题中止发布；不能声称任何 unhealthy 容器都会导致整个发布成功。但是单个目标容器健康门禁已经失效，特别是 jobs 不健康而公网 gateway 健康时，后续检查不能替代其健康状态。

## 复现与证据

在本地 Bash 运行原判断即可，无需 Docker、SSH 或真实服务器：

```bash
st='Up 2 minutes (unhealthy)'
ok=''
case "$st" in *healthy*) ok=1 ;; esac
printf 'ok=%s\n' "$ok"
```

实际输出 `ok=1`；预期该状态应继续等待或最终失败。源码即可确定该匹配结果，本轮另外执行了本地 shell 重放。

## 待修复方案与验收

优先读取结构化的容器健康字段并精确比较 `healthy`，明确未配置 healthcheck、容器不存在或命令失败的处理。保留现有等待预算，不以跳过检查规避。

验收至少覆盖 `healthy`、`unhealthy`、`starting`、退出和空结果；除精确 healthy 外不得被当作健康通过。实施记录见下节。

## 修复记录（2026-10-10）

- 实施：`docker/single-server/deploy.sh` 健康等待循环改为 `docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}no-healthcheck{{end}}' juhe-ai-go-$p`（容器名与 `compose.yml` 的 `container_name` 一致，gateway/jobs 均配置 healthcheck，maintenance 在循环头已跳过），判断改精确相等 `[ "$st" = "healthy" ]`；`|| st=""` 兜底脚本 `set -euo pipefail`，单轮查询失败（容器不存在、ssh 失败）按未 healthy 继续等待至 30×10s 预算耗尽 fail-closed。等待预算、失败文案与其后容器 md5、前端 buildId、公网健康检查一行未动。
- 验证：`bash -n` 通过；五态本地 stub 重放（healthy / unhealthy / starting / 命令失败 / 空输出）仅精确 healthy 通过，命令失败不再中止脚本；独立复审以脚本原文 harness 复核六态一致（no-healthcheck 字符串亦 fail-closed）。真实 Docker daemon 的模板渲染未在本机验证，随下次授权发布由门禁本身实测。
- 文档核对：`docs/deploy/部署指南.md:89`（`docker compose ps` 全 healthy）与 `docker/single-server/README.md:91`（逐容器等待 healthy）描述的"仅 healthy 放行"契约与修复后行为一致，无需同步。
- 复审：独立只读复审无 blocker（2026-10-10）。
