#!/usr/bin/env bash
# 发布后验证脚本：在目标服务器 compose 目录（如 /opt/juhe-ai）下运行；
# deploy.sh 第 [6/6] 步自动上传并调用，也可手动运行。
#
# 检查项（均为无凭据强制项；原第 3 项"业务闭环（/v1 请求 → 审计+用量落库）"
# 已于 2026-10-03 按用户裁定移除——闭环依赖具体账户/模型的实时可用性，
# 无法保证稳定，作为强制门禁会随上游波动误报；链路落库正确性改由发布后
# 巡检（audit_logs/usage_records 新行字段核验）覆盖）：
#   1. usage-record-spool 同源断言：gateway 与 jobs 的 /app/backend/data
#      挂载必须解析到同一宿主机目录，且两侧容器内 usage-record-spool 目录
#      存在——不同源时接口照常 200 但用量永不到库（BUG-0193），只能在事故
#      后人工 triage；本检查把该不变量变成发布门禁。
#   2. jobs drain 接线断言：jobs 近 10 分钟日志不得出现
#      usage_record_spool_drain_unwired 告警（未接线时统计同样恒空）。
#   3. gateway 内存限制断言（BUG-0313）：容器 env 必须含 GOMEMLIMIT 且
#      mem_limit 非 0——双限缺一，流量高峰 RSS 会无封顶膨胀、挤光页缓存
#      引发整机 I/O 活锁（10-14 事故）。只断言"已配置"，不锁定具体数值
#      （数值调整属 compose 契约变更，随 deploy.sh 配置同步下发）。
#
# 容器内端口拓扑（实测 2026-10-03）：3000=业务链路（/v1/*），3306=health+metrics
# 专用（compose healthcheck；无 /v1 路由）。gateway 3306 / jobs 3305 另经宿主
# 回环端口映射发布（127.0.0.1:3306:3306 / 127.0.0.1:3305:3305），仅宿主回环可达，
# 供 infra 机 Prometheus 经 SSH 隧道抓取（部署层拒绝公网入口，见 monitoring/README.md）。
#
# 用法：cd <compose 目录> && bash verify-release.sh
# 退出码：0 = 全部通过；1 = 任一强制项失败。
set -euo pipefail

cd "$(dirname "$0")"
COMPOSE_DIR=$(pwd)
GW_CONTAINER=juhe-ai-go-gateway
JOBS_CONTAINER=juhe-ai-go-jobs
FAIL=0

fail() { echo "  [FAIL] $*" >&2; FAIL=1; }
pass() { echo "  [PASS] $*"; }

echo "== 发布后验证（compose 目录：$COMPOSE_DIR）=="

# ---------------------------------------------------------------------------
# 1. usage-record-spool 同源断言（BUG-0193 防复发）
# ---------------------------------------------------------------------------
echo "-- [1/3] usage-record-spool 同源断言"
GW_SRC=$(docker inspect "$GW_CONTAINER" --format '{{range .Mounts}}{{if eq .Destination "/app/backend/data"}}{{.Source}}{{end}}{{end}}')
JOBS_SRC=$(docker inspect "$JOBS_CONTAINER" --format '{{range .Mounts}}{{if eq .Destination "/app/backend/data"}}{{.Source}}{{end}}{{end}}')
echo "  gateway 挂载源：${GW_SRC:-<未挂载 /app/backend/data>}"
echo "  jobs    挂载源：${JOBS_SRC:-<未挂载 /app/backend/data>}"
if [ -z "$GW_SRC" ] || [ -z "$JOBS_SRC" ]; then
  fail "gateway/jobs 任一容器未挂载 /app/backend/data（working_dir 锚定被破坏）"
elif [ "$GW_SRC" != "$JOBS_SRC" ]; then
  fail "gateway 与 jobs 的 data 挂载不同源（$GW_SRC != $JOBS_SRC）：接口可能正常但用量记录永不到库"
else
  pass "两侧挂载同源：$GW_SRC"
fi
for c in "$GW_CONTAINER" "$JOBS_CONTAINER"; do
  if ! docker exec "$c" ls -ld /app/backend/data/usage-record-spool >/dev/null 2>&1; then
    fail "$c 容器内 /app/backend/data/usage-record-spool 不存在（首次运行会自动创建；gateway 写侧启动诊断会打印解析路径）"
  fi
done
[ "$FAIL" -eq 0 ] && pass "spool 目录两侧均存在"

# ---------------------------------------------------------------------------
# 2. jobs drain 接线断言
# ---------------------------------------------------------------------------
echo "-- [2/3] jobs usage spool drain 接线断言"
# 门禁 fail-closed：先取日志并检查 docker compose logs 退出码——logs 本身失败
# （compose 目录错误、jobs 容器缺失等）时无法断言接线，必须判失败；`|| true`
# 只兜 grep 无匹配（正常无告警）路径的退出码。
if ! JOBS_LOGS=$(docker compose logs jobs --since 10m 2>&1); then
  fail "docker compose logs jobs 读取失败，无法断言 drain 接线，按门禁失败处理。原始错误：${JOBS_LOGS:-<无输出>}"
else
  UNWIRED=$(printf '%s\n' "$JOBS_LOGS" | grep -c 'usage_record_spool_drain_unwired' || true)
  if [ "${UNWIRED:-0}" -gt 0 ]; then
    fail "jobs 近 10 分钟出现 usage_record_spool_drain_unwired（近 $UNWIRED 次）：用量交接未接线，统计将恒空"
  else
    pass "jobs 近 10 分钟无 drain 未接线告警"
  fi
fi

# ---------------------------------------------------------------------------
# 3. gateway 内存限制断言（BUG-0313）：GOMEMLIMIT 软限 + mem_limit 硬限缺一，
#    流量高峰 RSS 会无封顶膨胀、挤光页缓存引发整机 I/O 活锁（10-14 事故）。
#    compose 契约：GOMEMLIMIT=2300MiB、mem_limit 3g；此处只断言"已配置"
#    （存在且非 0），不锁定具体数值——数值调整属 compose 契约变更，随
#    deploy.sh 配置同步机制下发，验证只防"契约丢失"。
# ---------------------------------------------------------------------------
echo "-- [3/3] gateway 内存限制断言（BUG-0313）"
GW_MEMLIMIT_BYTES=$(docker inspect "$GW_CONTAINER" --format '{{.HostConfig.Memory}}')
if [ -n "$GW_MEMLIMIT_BYTES" ] && [ "$GW_MEMLIMIT_BYTES" != "0" ]; then
  pass "gateway mem_limit 已配置（${GW_MEMLIMIT_BYTES} bytes）"
else
  fail "gateway mem_limit 未配置（HostConfig.Memory=0）：内存耗尽会拖死整机而非只 OOM 杀 gateway 容器自身"
fi
GW_GOMEMLIMIT=$(docker exec "$GW_CONTAINER" printenv GOMEMLIMIT 2>/dev/null || true)
if [ -n "$GW_GOMEMLIMIT" ]; then
  pass "gateway 容器 env 含 GOMEMLIMIT=$GW_GOMEMLIMIT"
else
  fail "gateway 容器 env 缺 GOMEMLIMIT：Go 堆软限制缺失，RSS 无封顶膨胀"
fi

if [ "$FAIL" -eq 0 ]; then echo "== 结果：PASS =="; else echo "== 结果：FAIL ==" >&2; fi
exit "$FAIL"
